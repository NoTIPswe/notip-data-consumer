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
	received  int
	written   int
	errors    int
	latencies int
}

func (s *stubTelemetryMetrics) IncMessagesReceived()                { s.received++ }
func (s *stubTelemetryMetrics) IncMessagesWritten()                 { s.written++ }
func (s *stubTelemetryMetrics) IncWriteErrors()                     { s.errors++ }
func (s *stubTelemetryMetrics) ObserveWriteLatency(_ time.Duration) { s.latencies++ }

type stubTelemetrySubscriber struct {
	subject string
	err     error
	onSub   func(cb nats.MsgHandler)
}

func (s *stubTelemetrySubscriber) Subscribe(subj string, cb nats.MsgHandler, _ ...nats.SubOpt) (drainableSubscription, error) {
	s.subject = subj
	if s.err != nil {
		return nil, s.err
	}
	if s.onSub != nil {
		s.onSub(cb)
	}
	return &fakeSubscription{}, nil
}

// stubMsg records which ACK operation was called — satisfies msgAcknowledger.
type stubMsg struct {
	acked    bool
	nacked   bool
	termed   bool
	nakDelay time.Duration
}

func (s *stubMsg) Ack(_ ...nats.AckOpt) error { s.acked = true; return nil }
func (s *stubMsg) NakWithDelay(d time.Duration, _ ...nats.AckOpt) error {
	s.nacked = true
	s.nakDelay = d
	return nil
}
func (s *stubMsg) Term(_ ...nats.AckOpt) error { s.termed = true; return nil }

// ─── helpers ──────────────────────────────────────────────────────────────────

const testSubjectT1GW1 = "telemetry.data.t1.gw-1"
const testDurableName = "test-durable"

func newConsumer(handler *stubTelemetryHandler, writer *stubTelemetryWriter) (*NATSTelemetryConsumer, *stubTelemetryMetrics) {
	m := &stubTelemetryMetrics{}
	c := newNATSTelemetryConsumer(nil, handler, writer, m, testDurableName, 10, time.Second)
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

// ─── permanentError ───────────────────────────────────────────────────────────

func TestPermanentErrorMessageDelegatesToCause(t *testing.T) {
	cause := errors.New("bad subject")
	pe := permanentError{cause: cause}

	assert.Equal(t, "bad subject", pe.Error())
}

func TestPermanentErrorUnwrapReturnsCause(t *testing.T) {
	cause := errors.New("original cause")
	pe := permanentError{cause: cause}

	assert.True(t, errors.Is(pe, cause), "Unwrap must expose the wrapped cause")
}

func TestNATSTelemetryConsumerRunReturnsSubscribeError(t *testing.T) {
	handler := &stubTelemetryHandler{}
	writer := &stubTelemetryWriter{}
	metrics := &stubTelemetryMetrics{}

	consumer := newNATSTelemetryConsumer(
		&stubTelemetrySubscriber{err: errors.New("nats unavailable")},
		handler,
		writer,
		metrics,
		testDurableName,
		1,
		time.Second,
	)

	err := consumer.Run(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "subscribe")
	assert.Contains(t, err.Error(), subjectTelemetry)
}

func TestNATSTelemetryConsumerRunSubscribesAndStopsOnCancel(t *testing.T) {
	handler := &stubTelemetryHandler{}
	writer := &stubTelemetryWriter{}
	metrics := &stubTelemetryMetrics{}
	sub := &stubTelemetrySubscriber{
		onSub: func(cb nats.MsgHandler) {
			cb(marshaledMsg(testSubjectT1GW1, makeEnvelope("gw-1")))
		},
	}

	consumer := newNATSTelemetryConsumer(
		sub,
		handler,
		writer,
		metrics,
		testDurableName,
		1,
		time.Hour,
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- consumer.Run(ctx)
	}()

	require.Eventually(t, func() bool {
		return len(writer.rows) == 1
	}, 500*time.Millisecond, 10*time.Millisecond)

	cancel()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Run did not return after context cancellation")
	}

	assert.Equal(t, subjectTelemetry, sub.subject)
	assert.Equal(t, 1, metrics.received)
	assert.Equal(t, 1, metrics.written)
	assert.Equal(t, "t1", handler.lastTenantID)
}

func TestEnqueuePendingCanceledContextNaksTransientError(t *testing.T) {
	c, _ := newConsumer(&stubTelemetryHandler{}, &stubTelemetryWriter{})
	pending := make(chan pendingMsg)
	msg := &stubMsg{}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	pm := pendingMsg{msg: msg, row: model.TelemetryRow{}, err: errors.New("transient")}

	done := make(chan struct{})
	go func() {
		c.enqueuePending(ctx, pending, pm)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("enqueuePending blocked after context cancellation")
	}

	assert.True(t, msg.nacked, "transient failure must be NAKed on canceled context")
	assert.Equal(t, 5*time.Second, msg.nakDelay)
	assert.False(t, msg.termed)
	assert.False(t, msg.acked)
}

func TestEnqueuePendingCanceledContextTermsPermanentError(t *testing.T) {
	c, _ := newConsumer(&stubTelemetryHandler{}, &stubTelemetryWriter{})
	pending := make(chan pendingMsg)
	msg := &stubMsg{}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	pm := pendingMsg{msg: msg, err: permanentError{cause: errors.New("bad json")}}

	done := make(chan struct{})
	go func() {
		c.enqueuePending(ctx, pending, pm)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("enqueuePending blocked after context cancellation")
	}

	assert.True(t, msg.termed, "permanent failure must be Termed on canceled context")
	assert.False(t, msg.nacked)
	assert.False(t, msg.acked)
}

// ─── flushLoop ────────────────────────────────────────────────────────────────

func TestFlushLoopFlushesOnBatchSizeThreshold(t *testing.T) {
	writer := &stubTelemetryWriter{}
	c, _ := newConsumer(&stubTelemetryHandler{}, writer)
	c.batchSize = 2

	pending := make(chan pendingMsg, 4)
	pm1, _ := validPending("t1")
	pm2, _ := validPending("t1")
	pending <- pm1
	pending <- pm2

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		c.flushLoop(ctx, pending)
		close(done)
	}()

	// Give the loop time to detect the batch is full and flush.
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	assert.Len(t, writer.rows, 2, "flushLoop must flush when batchSize is reached")
}

func TestFlushLoopFlushesOnTicker(t *testing.T) {
	writer := &stubTelemetryWriter{}
	c, _ := newConsumer(&stubTelemetryHandler{}, writer)
	c.flushEvery = 20 * time.Millisecond // short ticker so the test runs fast
	c.batchSize = 100                    // large batch so size-threshold never fires

	pending := make(chan pendingMsg, 4)
	pm, _ := validPending("t1")
	pending <- pm

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		c.flushLoop(ctx, pending)
		close(done)
	}()

	<-done

	assert.Len(t, writer.rows, 1, "flushLoop must flush buffered messages on ticker tick")
}

func TestFlushLoopFlushesPendingOnContextCancel(t *testing.T) {
	writer := &stubTelemetryWriter{}
	c, _ := newConsumer(&stubTelemetryHandler{}, writer)
	c.batchSize = 100 // large so size-threshold never fires
	c.flushEvery = time.Hour

	pending := make(chan pendingMsg, 4)
	pm, _ := validPending("t1")
	pending <- pm

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		c.flushLoop(ctx, pending)
		close(done)
	}()

	// Let the goroutine pick up the message, then cancel.
	time.Sleep(20 * time.Millisecond)
	cancel()
	<-done

	assert.Len(t, writer.rows, 1, "flushLoop must flush remaining buffer on context cancellation")
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

func TestNATSTelemetryConsumerExtractTenantIDRejectsExtraTokens(t *testing.T) {
	c, _ := newConsumer(&stubTelemetryHandler{}, &stubTelemetryWriter{})

	_, err := c.extractTenantID("telemetry.data.tenant-1.gw-42.extra")

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

	row, err := c.processMessage(context.Background(), marshaledMsg(testSubjectT1GW1, env))

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
		Subject: testSubjectT1GW1,
		Data:    []byte("not json"),
	})

	require.Error(t, err)
	var pErr permanentError
	assert.True(t, errors.As(err, &pErr), "bad JSON must return permanentError")
}

func TestNATSTelemetryConsumerProcessMessageHandlerErrorIsTransient(t *testing.T) {
	handler := &stubTelemetryHandler{err: errors.New("db down")}
	c, _ := newConsumer(handler, &stubTelemetryWriter{})

	_, err := c.processMessage(context.Background(), marshaledMsg(testSubjectT1GW1, makeEnvelope("gw-1")))

	require.Error(t, err)
	var pErr permanentError
	assert.False(t, errors.As(err, &pErr), "handler error must be transient, not permanent")
}

// ─── writeBatch ───────────────────────────────────────────────────────────────

// validPending builds a pendingMsg with a valid row and a stub acknowledger.
func validPending(tenantID string) (pendingMsg, *stubMsg) {
	m := &stubMsg{}
	pm := pendingMsg{
		msg: m,
		row: model.TelemetryRow{TenantID: tenantID, GatewayID: "gw-1"},
	}
	return pm, m
}

// permanentPending builds a pendingMsg representing a parse failure.
func permanentPending() (pendingMsg, *stubMsg) {
	m := &stubMsg{}
	pm := pendingMsg{
		msg: m,
		err: permanentError{cause: errors.New("bad json")},
	}
	return pm, m
}

func TestWriteBatchAllValidAcksAndCountsWritten(t *testing.T) {
	writer := &stubTelemetryWriter{}
	c, metrics := newConsumer(&stubTelemetryHandler{}, writer)

	pm1, msg1 := validPending("t1")
	pm2, msg2 := validPending("t1")

	c.writeBatch(context.Background(), []pendingMsg{pm1, pm2})

	assert.Len(t, writer.rows, 2, "both rows must be written")
	assert.True(t, msg1.acked, "msg1 must be Acked on success")
	assert.True(t, msg2.acked, "msg2 must be Acked on success")
	assert.Equal(t, 2, metrics.written, "IncMessagesWritten must be called once per row")
	assert.Equal(t, 0, metrics.errors)
	assert.Equal(t, 1, metrics.latencies, "ObserveWriteLatency must be called once per batch")
}

func TestWriteBatchAllPermanentErrorsTermsAndNoWrite(t *testing.T) {
	writer := &stubTelemetryWriter{}
	c, metrics := newConsumer(&stubTelemetryHandler{}, writer)

	pm1, msg1 := permanentPending()
	pm2, msg2 := permanentPending()

	c.writeBatch(context.Background(), []pendingMsg{pm1, pm2})

	assert.Empty(t, writer.rows, "no rows must be written for permanent errors")
	assert.True(t, msg1.termed, "msg1 must be Term'd for permanent error")
	assert.True(t, msg2.termed, "msg2 must be Term'd for permanent error")
	assert.Equal(t, 0, metrics.written)
	assert.Equal(t, 0, metrics.errors)
	assert.Equal(t, 0, metrics.latencies, "ObserveWriteLatency must not be called when there are no valid rows")
}

func TestWriteBatchMixedBatchTermsPermanentAcksValid(t *testing.T) {
	writer := &stubTelemetryWriter{}
	c, metrics := newConsumer(&stubTelemetryHandler{}, writer)

	valid, validMsg := validPending("t1")
	perm, permMsg := permanentPending()

	c.writeBatch(context.Background(), []pendingMsg{valid, perm})

	assert.Len(t, writer.rows, 1, "only the valid row must be written")
	assert.True(t, validMsg.acked, "valid msg must be Acked")
	assert.True(t, permMsg.termed, "permanent-error msg must be Term'd")
	assert.Equal(t, 1, metrics.written)
	assert.Equal(t, 0, metrics.errors)
}

func TestWriteBatchWriteErrorNaksValidAndCountsErrors(t *testing.T) {
	writer := &stubTelemetryWriter{err: errors.New("db timeout")}
	c, metrics := newConsumer(&stubTelemetryHandler{}, writer)

	pm1, msg1 := validPending("t1")
	pm2, msg2 := validPending("t1")

	c.writeBatch(context.Background(), []pendingMsg{pm1, pm2})

	assert.False(t, msg1.acked, "msg must not be Acked on write error")
	assert.True(t, msg1.nacked, "msg must be NakWithDelay'd on write error")
	assert.Equal(t, 5*time.Second, msg1.nakDelay, "NAK delay must be 5 seconds")
	assert.False(t, msg2.acked)
	assert.True(t, msg2.nacked)
	assert.Equal(t, 2, metrics.errors, "IncWriteErrors must be called for each failed valid msg")
	assert.Equal(t, 0, metrics.written)
	assert.Equal(t, 1, metrics.latencies, "ObserveWriteLatency must still be observed on write error")
}

func TestWriteBatchWriteErrorDoesNotTermPermanentErrors(t *testing.T) {
	writer := &stubTelemetryWriter{err: errors.New("db timeout")}
	c, _ := newConsumer(&stubTelemetryHandler{}, writer)

	valid, validMsg := validPending("t1")
	perm, permMsg := permanentPending()

	c.writeBatch(context.Background(), []pendingMsg{valid, perm})

	assert.True(t, permMsg.termed, "permanent-error msg must always be Term'd regardless of write outcome")
	assert.True(t, validMsg.nacked, "valid msg must be NakWithDelay'd when write fails")
	assert.False(t, permMsg.nacked, "permanent-error msg must never be Nacked")
}

func TestWriteBatchEmptyBatchIsNoop(t *testing.T) {
	writer := &stubTelemetryWriter{}
	c, metrics := newConsumer(&stubTelemetryHandler{}, writer)

	c.writeBatch(context.Background(), []pendingMsg{})

	assert.Empty(t, writer.rows)
	assert.Equal(t, 0, metrics.written)
	assert.Equal(t, 0, metrics.latencies)
}
