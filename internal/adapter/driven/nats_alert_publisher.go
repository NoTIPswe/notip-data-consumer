package driven

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/NoTIPswe/notip-data-consumer/internal/domain/model"
	"github.com/nats-io/nats.go"
)

// interface over nats.JetStreamContext used by NATSAlertPublisher.
// intend to expose only Publish so tests can pass a lightweight stub instead of a real JetStream context.
type natsJSPublisher interface {
	Publish(subj string, data []byte, opts ...nats.PubOpt) (*nats.PubAck, error)
}

// alertPublisherMetrics is a narrow metric interface for NATSAlertPublisher.
type alertPublisherMetrics interface {
	IncAlertsPublished()
	IncAlertPublishErrors()
}

// NATSAlertPublisher implements port.AlertPublisher.
// serializes AlertPayload to JSON and publishes to alert.gw_offline.{tenantId} via JetStream.
type NATSAlertPublisher struct {
	js      natsJSPublisher
	metrics alertPublisherMetrics
}

// constructor of NATSAlertPublisher.
// accepts natsJSPublisher so production code passes nats.JetStreamContext.
// test can be lightweight stubs without a live NATS server.
func NewNATSAlertPublisher(js natsJSPublisher, metrics alertPublisherMetrics) *NATSAlertPublisher {
	return &NATSAlertPublisher{js: js, metrics: metrics}
}

// serialise payload to JSON and publish it to alert.gw_offline.{tenantID}
// context is accepted for interface compiance but not threaded into js.Publish
// synchronous Publish API does not support per-call contex cancellation.
func (p *NATSAlertPublisher) Publish(_ context.Context, tenantID string, payload model.AlertPayload) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal alert payload: %w", err)
	}

	subject := fmt.Sprintf("alert.gw_offline.%s", tenantID)
	if _, err := p.js.Publish(subject, data); err != nil {
		p.metrics.IncAlertPublishErrors()
		return fmt.Errorf("nats publish %s: %w", subject, err)
	}

	p.metrics.IncAlertsPublished() // increment the number of alerts published
	return nil
}
