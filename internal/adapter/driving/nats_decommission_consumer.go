package driving

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/nats-io/nats.go"

	"github.com/NoTIPswe/notip-data-consumer/internal/domain/port"
)

// JetStream subject for decommission events.
const subjectDecommissioned = "gateway.decommissioned.>"
const durableDecommissionListener = "data-consumer-decommission-listener"

// drainableSubscription abstracts *nats.Subscription so both consumers can call
// Drain() on shutdown without importing the concrete type in test stubs.
type drainableSubscription interface {
	Drain() error
	Unsubscribe() error
}

// natsJSSubscriber is a narrow interface over nats.JetStreamContext for subscriptions.
// Returning drainableSubscription keeps tests free of live *nats.Subscription instances.
type natsJSSubscriber interface {
	Subscribe(subj string, cb nats.MsgHandler, opts ...nats.SubOpt) (drainableSubscription, error)
}

// jsAdapter wraps nats.JetStreamContext and implements natsJSSubscriber so tests
// can inject stub subscribers without a live NATS server.
type jsAdapter struct{ js nats.JetStreamContext }

func (a *jsAdapter) Subscribe(subj string, cb nats.MsgHandler, opts ...nats.SubOpt) (drainableSubscription, error) {
	return a.js.Subscribe(subj, cb, opts...)
}

// NATSDecommissionConsumer subscribes to gateway.decommissioned.> and calls DecommissionEventHandler.
type NATSDecommissionConsumer struct {
	js      natsJSSubscriber
	handler port.DecommissionEventHandler
	logger  *slog.Logger
}

// NewNATSDecommissionConsumer is the public constructor — wraps js in jsAdapter so
// production code passes nats.JetStreamContext; tests use the internal constructor.
func NewNATSDecommissionConsumer(js nats.JetStreamContext, handler port.DecommissionEventHandler) *NATSDecommissionConsumer {
	return newNATSDecommissionConsumer(&jsAdapter{js: js}, handler)
}

func newNATSDecommissionConsumer(js natsJSSubscriber, handler port.DecommissionEventHandler) *NATSDecommissionConsumer {
	return &NATSDecommissionConsumer{js: js, handler: handler, logger: slog.Default()}
}

// Run subscribes to the decommission subject and blocks until ctx is cancelled.
// Drain() is called on shutdown so in-flight messages are processed before the
// subscription is closed — required for durable consumers to avoid message loss.
func (c *NATSDecommissionConsumer) Run(ctx context.Context) error {
	sub, err := c.js.Subscribe(
		subjectDecommissioned,
		c.handleMsg,
		nats.ManualAck(),
		nats.Durable(durableDecommissionListener),
	)
	if err != nil {
		return fmt.Errorf("subscribe %s: %w", subjectDecommissioned, err)
	}
	defer func() { _ = sub.Drain() }()

	<-ctx.Done()
	return nil
}

func (c *NATSDecommissionConsumer) handleMsg(msg *nats.Msg) {
	tenantID, gatewayID, err := c.extractIDs(msg.Subject)
	if err != nil {
		c.logger.Warn("decommission message has invalid subject format")
		_ = msg.Term()
		return
	}
	c.handler.HandleDecommission(tenantID, gatewayID)
	c.logger.Info("gateway decommission processed")
	_ = msg.Ack()
}

// extractIDs parses gateway.decommissioned.{tenantID}.{gatewayID}.
func (c *NATSDecommissionConsumer) extractIDs(subject string) (string, string, error) {
	parts := strings.Split(subject, ".")
	if len(parts) != 4 {
		return "", "", fmt.Errorf("unexpected subject format %q", subject)
	}
	return parts[2], parts[3], nil
}
