package driving

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NoTIPswe/notip-data-consumer/internal/domain/model"
)

// ─── stubs ────────────────────────────────────────────────────────────────────

type stubTelemetryHandler struct {
	lastTenantID string
	lastEnvelope model.TelemetryEnvelope
	err          error
}

func (s *stubTelemetryHandler) HandleTelemetry(_ context.Context, tenantID string, env model.TelemetryEnvelope) error {
	s.lastTenantID = tenantID
	s.lastEnvelope = env
	return s.err
}

type stubTelemetryWriter struct {
	rows []model.TelemetryRow
	err  error
}

func (s *stubTelemetryWriter) Write(_ context.Context, row model.TelemetryRow) error {
	s.rows = append(s.rows, row)
	return s.err
}

func (s *stubTelemetryWriter) WriteBatch(_ context.Context, rows []model.TelemetryRow) error {
	s.rows = append(s.rows, rows...)
	return s.err
}

type stubTelemetryMetrics struct {
	received int
	written  int
	errors   int
}

func (s *stubTelemetryMetrics) IncMessagesReceived()                { s.received++ }
func (s *stubTelemetryMetrics) IncMessagesWritten()                 { s.written++ }
func (s *stubTelemetryMetrics) IncWriteErrors()                     { s.errors++ }
func (s *stubTelemetryMetrics) ObserveWriteLatency(_ time.Duration) { /* empty for unit test */ }

// ─── helpers ──────────────────────────────────────────────────────────────────

func newConsumer(handler *stubTelemetryHandler, writer *stubTelemetryWriter) (*NATSTelemetryConsumer, *stubTelemetryMetrics) {
	m := &stubTelemetryMetrics{}
	c := NewNATSTelemetryConsumer(nil, handler, writer, m, "test-durable", 10, time.Second)
	return c, m
}

func makeEnvelope(gatewayID string) model.TelemetryEnvelope {
	return model.TelemetryEnvelope{
		GatewayID:     gatewayID,
		SensorID:      "s-1",
		SensorType:    "temp",
		Timestamp:     time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC),
		KeyVersion:    1,
		EncryptedData: model.OpaqueBlob{Value: "ZW5j"},
		IV:            model.OpaqueBlob{Value: "aXY="},
		AuthTag:       model.OpaqueBlob{Value: "YXV0"},
	}
}

func marshaledMsg(subject string, env model.TelemetryEnvelope) *nats.Msg {
	data, _ := json.Marshal(env)
	return &nats.Msg{Subject: subject, Data: data}
}

// ─── extractTenantID ──────────────────────────────────────────────────────────

func TestNATSTelemetryConsumerExtractTenantIDSuccess(t *testing.T) {
	c, _ := newConsumer(&stubTelemetryHandler{}, &stubTelemetryWriter{})

	tenantID, err := c.extractTenantID("telemetry.data.tenant-1.gw-42")

	require.NoError(t, err)
	assert.Equal(t, "tenant-1", tenantID)
}

func TestNATSTelemetryConsumerExtractTenantIDMalformed(t *testing.T) {
	c, _ := newConsumer(&stubTelemetryHandler{}, &stubTelemetryWriter{})

	_, err := c.extractTenantID("telemetry.data")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected telemetry subject")
}

// ─── buildRow ─────────────────────────────────────────────────────────────────

func TestNATSTelemetryConsumerBuildRowMapsFields(t *testing.T) {
	c, _ := newConsumer(&stubTelemetryHandler{}, &stubTelemetryWriter{})
	env := makeEnvelope("gw-1")

	row := c.buildRow("t1", env)

	assert.Equal(t, env.Timestamp, row.Time, "Time must come from envelope, not time.Now()")
	assert.Equal(t, "t1", row.TenantID)
	assert.Equal(t, env.GatewayID, row.GatewayID)
	assert.Equal(t, env.EncryptedData, row.EncryptedData, "EncryptedData must be copied verbatim — Rule Zero")
	assert.Equal(t, env.IV, row.IV, "IV must be copied verbatim — Rule Zero")
	assert.Equal(t, env.AuthTag, row.AuthTag, "AuthTag must be copied verbatim — Rule Zero")
}

// ─── processMessage ───────────────────────────────────────────────────────────

func TestNATSTelemetryConsumerProcessMessageSuccess(t *testing.T) {
	handler := &stubTelemetryHandler{}
	c, _ := newConsumer(handler, &stubTelemetryWriter{})
	env := makeEnvelope("gw-1")

	row, err := c.processMessage(context.Background(), marshaledMsg("telemetry.data.t1.gw-1", env))

	require.NoError(t, err)
	assert.Equal(t, "t1", handler.lastTenantID)
	assert.Equal(t, "t1", row.TenantID)
	assert.Equal(t, env.Timestamp, row.Time)
}

func TestNATSTelemetryConsumerProcessMessageBadSubjectIsPermanent(t *testing.T) {
	c, _ := newConsumer(&stubTelemetryHandler{}, &stubTelemetryWriter{})

	_, err := c.processMessage(context.Background(), &nats.Msg{Subject: "bad", Data: []byte("{}")})

	require.Error(t, err)
	var pErr permanentError
	assert.True(t, errors.As(err, &pErr), "bad subject must return permanentError")
}

func TestNATSTelemetryConsumerProcessMessageBadJSONIsPermanent(t *testing.T) {
	c, _ := newConsumer(&stubTelemetryHandler{}, &stubTelemetryWriter{})

	_, err := c.processMessage(context.Background(), &nats.Msg{
		Subject: "telemetry.data.t1.gw-1",
		Data:    []byte("not json"),
	})

	require.Error(t, err)
	var pErr permanentError
	assert.True(t, errors.As(err, &pErr), "bad JSON must return permanentError")
}

func TestNATSTelemetryConsumerProcessMessageHandlerErrorIsTransient(t *testing.T) {
	handler := &stubTelemetryHandler{err: errors.New("db down")}
	c, _ := newConsumer(handler, &stubTelemetryWriter{})

	_, err := c.processMessage(context.Background(), marshaledMsg("telemetry.data.t1.gw-1", makeEnvelope("gw-1")))

	require.Error(t, err)
	var pErr permanentError
	assert.False(t, errors.As(err, &pErr), "handler error must be transient, not permanent")
}
