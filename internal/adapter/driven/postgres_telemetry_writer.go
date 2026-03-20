package driven

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/NoTIPswe/notip-data-consumer/internal/domain/model"
)

// insert query when we access the db.
const insertTelemetry = `
INSERT INTO telemetry (
	time, tenant_id, gateway_id, sensor_id, sensor_type,
	encrypted_data, iv, auth_tag, key_version
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`

// interface over *pgxpool.Pool.
type dbPool interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults
	Close()
}

// implements port.TelemetryWriter
// writes TelemetryRow verbatim to the TimescaleDB hypertable.
// opaque blob fields are never inspected.
type PostgresTelemetryWriter struct {
	pool dbPool
}

// constructs a PostgresTelemetryWriter.
// accepts *pgxpool.Pool which satisfies dbPool interface.
func NewPostgresTelemetryWriter(pool *pgxpool.Pool) *PostgresTelemetryWriter {
	return &PostgresTelemetryWriter{pool: pool}
}

// write ONE telemetry row. The time column is row.Time (gateway timestamp).
func (w *PostgresTelemetryWriter) Write(ctx context.Context, row model.TelemetryRow) error {
	_, err := w.pool.Exec(ctx, insertTelemetry,
		row.Time,
		row.TenantID,
		row.GatewayID,
		row.SensorID,
		row.SensorType,
		row.EncryptedData.Value,
		row.IV.Value,
		row.AuthTag.Value,
		row.KeyVersion,
	)
	if err != nil {
		return fmt.Errorf("insert telemetry row: %w", err)
	}
	return nil
}

// write batch, insert multiple rows in a single network round-trip.
func (w *PostgresTelemetryWriter) WriteBatch(ctx context.Context, rows []model.TelemetryRow) error {
	if len(rows) == 0 {
		return nil
	}

	batch := &pgx.Batch{}
	for _, row := range rows {
		batch.Queue(insertTelemetry,
			row.Time,
			row.TenantID,
			row.GatewayID,
			row.SensorID,
			row.SensorType,
			row.EncryptedData.Value, // opaque base64 TEXT — never decoded
			row.IV.Value,            // opaque base64 TEXT — never decoded
			row.AuthTag.Value,       // opaque base64 TEXT — never decoded
			row.KeyVersion,
		)
	}

	br := w.pool.SendBatch(ctx, batch)
	defer func() { _ = br.Close() }()

	for i := range rows {
		if _, err := br.Exec(); err != nil {
			return fmt.Errorf("batch insert row %d: %w", i, err)
		}
	}

	return nil
}

// releases all pool connections called during graceful shutdown.
func (w *PostgresTelemetryWriter) Close() {
	w.pool.Close()
}
