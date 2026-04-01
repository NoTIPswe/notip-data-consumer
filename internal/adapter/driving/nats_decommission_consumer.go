package driving

import (
	"context"
	"fmt"
	"strings"

	"github.com/nats-io/nats.go"

	"github.com/NoTIPswe/notip-data-consumer/internal/domain/port"
)

// JetStream subject where it receives news.
const subjectDecommissioned = "gateway.decommissioned.>"

// drainableSubscription is the subset of *nats.Subscription used by consumers.
type drainableSubscription interface {
	Drain() error
	Unsubscribe() error
}

// natsJSSubscriber is a narrow interface over nats.JetStreamContext for subscriptions.
// Returns drainableSubscription so unit tests can inject a fake without a live NATS connection.
type natsJSSubscriber interface {
	Subscribe(subj string, cb nats.MsgHandler, opts ...nats.SubOpt) (drainableSubscription, error)
}

// jsAdapter wraps nats.JetStreamContext to satisfy natsJSSubscriber.
type jsAdapter struct{ js nats.JetStreamContext }

func (a *jsAdapter) Subscribe(subj string, cb nats.MsgHandler, opts ...nats.SubOpt) (drainableSubscription, error) {
	return a.js.Subscribe(subj, cb, opts...)
}

// NATSDecommissionConsumer subscribes to gateway.decommissioned.> and calls DecomissionEventHandler.
type NATSDecommissionConsumer struct {
	js      natsJSSubscriber
	handler port.DecommissionEventHandler
}

// NewNATSDecommissionConsumer is the production constructor — wraps js in a jsAdapter internally.
func NewNATSDecommissionConsumer(js nats.JetStreamContext, handler port.DecommissionEventHandler) *NATSDecommissionConsumer {
	return newNATSDecommissionConsumer(&jsAdapter{js: js}, handler)
}

// newNATSDecommissionConsumer is the internal constructor used by unit tests to inject stubs.
func newNATSDecommissionConsumer(js natsJSSubscriber, handler port.DecommissionEventHandler) *NATSDecommissionConsumer {
	return &NATSDecommissionConsumer{js: js, handler: handler}
}

// Run subscribes to the decommission subject and blocks until ctx is cancelled.
func (c *NATSDecommissionConsumer) Run(ctx context.Context) error {
	opts := []nats.SubOpt{
		nats.ManualAck(),
		nats.Durable("data-consumer-decommission-listener"),
	}
	sub, err := c.js.Subscribe(subjectDecommissioned, c.handleMsg, opts...)
	if err != nil {
		return fmt.Errorf("subscribe %s: %w", subjectDecommissioned, err)
	}

	<-ctx.Done()
	return sub.Drain()
}

func (c *NATSDecommissionConsumer) handleMsg(msg *nats.Msg) {
	tenantID, gatewayID, err := c.extractIDs(msg.Subject)
	if err != nil {
		_ = msg.Term() // not re-delivers the message
		return
	}
	c.handler.HandleDecommission(tenantID, gatewayID)
	_ = msg.Ack()
}

// extract IDs parses gateway.decomissiones.{tenantID}.{gatewayID}.
func (c *NATSDecommissionConsumer) extractIDs(subject string) (string, string, error) {
	parts := strings.Split(subject, ".")
	if len(parts) != 4 {
		return "", "", fmt.Errorf("unexpected subject format %q", subject)
	}
	return parts[2], parts[3], nil
}
