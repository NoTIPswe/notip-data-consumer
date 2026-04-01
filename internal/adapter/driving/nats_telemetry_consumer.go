package driving

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/NoTIPswe/notip-data-consumer/internal/domain/model"
	"github.com/NoTIPswe/notip-data-consumer/internal/domain/port"
)

const subjectTelemetry = "telemetry.data.>"

// permanentError marks failures that must not be retried (parse errors, bad subjects).
// The message is Term()d instead of Nak()d.
type permanentError struct{ cause error }

func (e permanentError) Error() string { return e.cause.Error() }
func (e permanentError) Unwrap() error { return e.cause }

// telemetryConsumerMetrics is the narrow metric interface for NATSTelemetryConsumer.
type telemetryConsumerMetrics interface {
	IncMessagesReceived()
	IncMessagesWritten()
	IncWriteErrors()
	ObserveWriteLatency(d time.Duration)
}

// msgAcknowledger abstracts NATS message acknowledgement so writeBatch can be
// tested without a live NATS connection. *nats.Msg satisfies this interface.
type msgAcknowledger interface {
	Ack(opts ...nats.AckOpt) error
	NakWithDelay(delay time.Duration, opts ...nats.AckOpt) error
	Term(opts ...nats.AckOpt) error
}

// pendingMsg pairs a NATS message acknowledger with its decoded row so the flush
// loop can ACK/NAK the original message after WriteBatch returns.
type pendingMsg struct {
	msg msgAcknowledger
	row model.TelemetryRow
	err error // non-nil means permanentError — row is zero value
}

// NATSTelemetryConsumer subscribes to telemetry.data.> as a JetStream durable consumer.
// Messages are buffered and flushed to TimescaleDB in batches for throughput.
type NATSTelemetryConsumer struct {
	js          natsJSSubscriber
	handler     port.TelemetryMessageHandler
	writer      port.TelemetryWriter
	metrics     telemetryConsumerMetrics
	durableName string
	batchSize   int
	flushEvery  time.Duration
}

func NewNATSTelemetryConsumer(
	js nats.JetStreamContext,
	handler port.TelemetryMessageHandler,
	writer port.TelemetryWriter,
	metrics telemetryConsumerMetrics,
	durableName string,
	batchSize int,
	flushEvery time.Duration,
) *NATSTelemetryConsumer {
	return newNATSTelemetryConsumer(&jsAdapter{js: js}, handler, writer, metrics, durableName, batchSize, flushEvery)
}

func newNATSTelemetryConsumer(
	js natsJSSubscriber,
	handler port.TelemetryMessageHandler,
	writer port.TelemetryWriter,
	metrics telemetryConsumerMetrics,
	durableName string,
	batchSize int,
	flushEvery time.Duration,
) *NATSTelemetryConsumer {
	return &NATSTelemetryConsumer{
		js:          js,
		handler:     handler,
		writer:      writer,
		metrics:     metrics,
		durableName: durableName,
		batchSize:   batchSize,
		flushEvery:  flushEvery,
	}
}

// Run subscribes to the telemetry subject and blocks until ctx is cancelled.
func (c *NATSTelemetryConsumer) Run(ctx context.Context) error {
	pending := make(chan pendingMsg, c.batchSize*2)

	sub, err := c.js.Subscribe(
		subjectTelemetry,
		func(msg *nats.Msg) {
			c.metrics.IncMessagesReceived()
			row, err := c.processMessage(ctx, msg)
			c.enqueuePending(ctx, pending, pendingMsg{msg: msg, row: row, err: err})
		},
		nats.Durable(c.durableName),
		nats.ManualAck(),
	)
	if err != nil {
		return fmt.Errorf("subscribe %s: %w", subjectTelemetry, err)
	}
	defer func() { _ = sub.Drain() }()

	c.flushLoop(ctx, pending)
	return nil
}

// If ctx is done before the message can be queued, it requests redelivery for
// transient failures and Term()s permanent parse failures.
func (c *NATSTelemetryConsumer) enqueuePending(ctx context.Context, pending chan<- pendingMsg, pm pendingMsg) {
	select {
	case pending <- pm:
		return
	case <-ctx.Done():
		if pm.err != nil {
			var pErr permanentError
			if errors.As(pm.err, &pErr) {
				_ = pm.msg.Term()
				return
			}
		}
		_ = pm.msg.NakWithDelay(5 * time.Second)
	}
}

// flushLoop drains the pending channel and writes batches to TimescaleDB.
func (c *NATSTelemetryConsumer) flushLoop(ctx context.Context, pending <-chan pendingMsg) {
	ticker := time.NewTicker(c.flushEvery)
	defer ticker.Stop()

	buf := make([]pendingMsg, 0, c.batchSize)

	flush := func() {
		if len(buf) == 0 {
			return
		}
		c.writeBatch(ctx, buf)
		buf = buf[:0]
	}

	for {
		select {
		case pm := <-pending:
			buf = append(buf, pm)
			if len(buf) >= c.batchSize {
				flush()
			}
		case <-ctx.Done():
			flush()
			return
		case <-ticker.C:
			flush()
		}
	}
}

// writeBatch separates permanent failures from valid rows, writes the valid rows,
// then ACKs or NAKs each message individually.
func (c *NATSTelemetryConsumer) writeBatch(ctx context.Context, batch []pendingMsg) {
	rows := make([]model.TelemetryRow, 0, len(batch))
	for _, pm := range batch {
		if pm.err == nil {
			rows = append(rows, pm.row)
		}
	}

	var writeErr error
	if len(rows) > 0 {
		start := time.Now()
		writeErr = c.writer.WriteBatch(ctx, rows)
		c.metrics.ObserveWriteLatency(time.Since(start))
	}

	for _, pm := range batch {
		if pm.err != nil {
			// Permanent parse error term so NATS never redelivers.
			_ = pm.msg.Term()
			continue
		}
		if writeErr != nil {
			c.metrics.IncWriteErrors()
			_ = pm.msg.NakWithDelay(5 * time.Second)
		} else {
			c.metrics.IncMessagesWritten()
			_ = pm.msg.Ack()
		}
	}
}

// processMessage parses and validates one NATS message, calls the heartbeat handler,
// and returns the TelemetryRow ready for batch insertion.
func (c *NATSTelemetryConsumer) processMessage(ctx context.Context, msg *nats.Msg) (model.TelemetryRow, error) {
	tenantID, err := c.extractTenantID(msg.Subject)
	if err != nil {
		return model.TelemetryRow{}, permanentError{cause: err}
	}

	var envelope model.TelemetryEnvelope
	if err := json.Unmarshal(msg.Data, &envelope); err != nil {
		return model.TelemetryRow{}, permanentError{
			cause: fmt.Errorf("unmarshal telemetry envelope: %w", err),
		}
	}

	if err := c.handler.HandleTelemetry(ctx, tenantID, envelope); err != nil {
		return model.TelemetryRow{}, fmt.Errorf("handle telemetry: %w", err)
	}

	return c.buildRow(tenantID, envelope), nil
}

// extractTenantID parses telemetry.data.{tenantID}.{gwID} — tenantID is at index 2.
func (c *NATSTelemetryConsumer) extractTenantID(subject string) (string, error) {
	parts := strings.Split(subject, ".")
	if len(parts) != 4 {
		return "", fmt.Errorf("unexpected telemetry subject: %q", subject)
	}
	return parts[2], nil
}

// buildRow maps a TelemetryEnvelope + tenantID into a TelemetryRow for persistence.
func (c *NATSTelemetryConsumer) buildRow(tenantID string, env model.TelemetryEnvelope) model.TelemetryRow {
	return model.TelemetryRow{
		Time:          env.Timestamp,
		TenantID:      tenantID,
		GatewayID:     env.GatewayID,
		SensorID:      env.SensorID,
		SensorType:    string(env.SensorType),
		EncryptedData: env.EncryptedData,
		IV:            env.IV,
		AuthTag:       env.AuthTag,
		KeyVersion:    env.KeyVersion,
	}
}
