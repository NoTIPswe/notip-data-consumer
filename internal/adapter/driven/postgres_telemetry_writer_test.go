package driven

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NoTIPswe/notip-data-consumer/internal/domain/model"
)

// stubs

type stubBatchResults struct {
	err    error
	called int
}

func (s *stubBatchResults) Exec() (pgconn.CommandTag, error) {
	s.called++
	return pgconn.CommandTag{}, s.err
}
func (s *stubBatchResults) Query() (pgx.Rows, error) { return nil, nil }
func (s *stubBatchResults) QueryRow() pgx.Row        { return nil }
func (s *stubBatchResults) Close() error             { return nil }

type stubPool struct {
	execErr      error
	lastSQL      string
	lastArgs     []any
	batchResults *stubBatchResults
	batchQueued  int
}

func (s *stubPool) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	s.lastSQL = sql
	s.lastArgs = args
	return pgconn.CommandTag{}, s.execErr
}

func (s *stubPool) SendBatch(_ context.Context, b *pgx.Batch) pgx.BatchResults {
	s.batchQueued = b.Len()
	return s.batchResults
}

func (s *stubPool) Close() { /* empty for unit testing */ }

// helpers

func newWriter(pool dbPool) *PostgresTelemetryWriter {
	return &PostgresTelemetryWriter{pool: pool}
}

func makeRow(tenantID, gatewayID string) model.TelemetryRow {
	return model.TelemetryRow{
		Time:          time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC),
		TenantID:      tenantID,
		GatewayID:     gatewayID,
		SensorID:      "s-1",
		SensorType:    "temperature",
		EncryptedData: model.OpaqueBlob{Value: "ZW5jcnlwdGVk"},
		IV:            model.OpaqueBlob{Value: "aXY="},
		AuthTag:       model.OpaqueBlob{Value: "YXV0aA=="},
		KeyVersion:    3,
	}
}

// Write

func TestPostgresTelemetryWriterWriteSuccess(t *testing.T) {
	pool := &stubPool{}
	w := newWriter(pool)

	require.NoError(t, w.Write(context.Background(), makeRow("t1", "gw-1")))
	assert.Contains(t, pool.lastSQL, "INSERT INTO telemetry")
	require.Len(t, pool.lastArgs, 9)
}

func TestPostgresTelemetryWriterWritePassesOpaqueBlobs(t *testing.T) {
	pool := &stubPool{}
	w := newWriter(pool)
	row := makeRow("t1", "gw-1")

	require.NoError(t, w.Write(context.Background(), row))

	// Args: time(0) tenant(1) gateway(2) sensor_id(3) sensor_type(4) enc(5) iv(6) auth(7) keyver(8)
	assert.Equal(t, row.EncryptedData.Value, pool.lastArgs[5], "EncryptedData.Value must be passed verbatim — Rule Zero")
	assert.Equal(t, row.IV.Value, pool.lastArgs[6], "IV.Value must be passed verbatim — Rule Zero")
	assert.Equal(t, row.AuthTag.Value, pool.lastArgs[7], "AuthTag.Value must be passed verbatim — Rule Zero")
}

func TestPostgresTelemetryWriterWriteUsesRowTime(t *testing.T) {
	pool := &stubPool{}
	w := newWriter(pool)
	row := makeRow("t1", "gw-1")

	require.NoError(t, w.Write(context.Background(), row))

	assert.Equal(t, row.Time, pool.lastArgs[0], "partition key must be row.Time, not time.Now()")
}

func TestPostgresTelemetryWriterWriteDBError(t *testing.T) {
	pool := &stubPool{execErr: errors.New("connection closed")}
	w := newWriter(pool)

	err := w.Write(context.Background(), makeRow("t1", "gw-1"))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "insert telemetry row")
}

// ─── WriteBatch ───────────────────────────────────────────────────────────────

func TestPostgresTelemetryWriterWriteBatchSuccess(t *testing.T) {
	br := &stubBatchResults{}
	pool := &stubPool{batchResults: br}
	w := newWriter(pool)

	rows := []model.TelemetryRow{makeRow("t1", "gw-1"), makeRow("t1", "gw-2")}
	require.NoError(t, w.WriteBatch(context.Background(), rows))

	assert.Equal(t, 2, pool.batchQueued, "both rows must be queued in the batch")
	assert.Equal(t, 2, br.called, "Exec must be called once per row")
}

func TestPostgresTelemetryWriterWriteBatchEmptyIsNoop(t *testing.T) {
	pool := &stubPool{batchResults: &stubBatchResults{}}
	w := newWriter(pool)

	require.NoError(t, w.WriteBatch(context.Background(), nil))
	assert.Equal(t, 0, pool.batchQueued, "empty slice must not send a batch")
}

func TestPostgresTelemetryWriterWriteBatchRowError(t *testing.T) {
	br := &stubBatchResults{err: errors.New("unique violation")}
	pool := &stubPool{batchResults: br}
	w := newWriter(pool)

	rows := []model.TelemetryRow{makeRow("t1", "gw-1"), makeRow("t1", "gw-2")}
	err := w.WriteBatch(context.Background(), rows)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "batch insert row 0")
}
