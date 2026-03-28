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

// interface over nats.JetstreamContext for subsriptions.
type natsJSSubscriber interface {
	Subscribe(subj string, cb nats.MsgHandler, opts ...nats.SubOpt) (*nats.Subscription, error)
}

// NATSDecommissionConsumer subscribes to gateway.decommissioned.> and calls DecomissionEventHandler.
type NATSDecommissionConsumer struct {
	js      natsJSSubscriber
	handler port.DecommissionEventHandler
}

func NewNATSDecommissionConsumer(js natsJSSubscriber, handler port.DecommissionEventHandler) *NATSDecommissionConsumer {
	return &NATSDecommissionConsumer{js: js, handler: handler}
}

// Run subscribes to the decommission subject and blocks until ctx is cancelled.
func (c *NATSDecommissionConsumer) Run(ctx context.Context) error {
	sub, err := c.js.Subscribe(subjectDecommissioned, c.handleMsg, nats.ManualAck())
	if err != nil {
		return fmt.Errorf("subscribe %s: %w", subjectDecommissioned, err)
	}
	defer func() { _ = sub.Unsubscribe() }()

	<-ctx.Done()
	return nil
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
