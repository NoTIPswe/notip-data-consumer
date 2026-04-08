package driving

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubs

type stubDecommissionHandler struct {
	tenantID  string
	gatewayID string
	calls     int
}

func (s *stubDecommissionHandler) HandleDecommission(tenantID, gatewayID string) {
	s.tenantID = tenantID
	s.gatewayID = gatewayID
	s.calls++
}

// fakeSubscription satisfies drainableSubscription without a live NATS connection.
type fakeSubscription struct{}

func (f *fakeSubscription) Drain() error       { return nil }
func (f *fakeSubscription) Unsubscribe() error { return nil }

// stubFailingSubscriber simulates a JetStream context whose Subscribe always fails.
type stubFailingSubscriber struct{ err error }

func (s *stubFailingSubscriber) Subscribe(_ string, _ nats.MsgHandler, _ ...nats.SubOpt) (drainableSubscription, error) {
	return nil, s.err
}

type stubDecommissionSubscriber struct {
	subject string
}

func (s *stubDecommissionSubscriber) Subscribe(subj string, _ nats.MsgHandler, _ ...nats.SubOpt) (drainableSubscription, error) {
	s.subject = subj
	return &fakeSubscription{}, nil
}

// constructor

func TestNATSDecommissionConsumerSetsFields(t *testing.T) {
	handler := &stubDecommissionHandler{}
	c := newNATSDecommissionConsumer(nil, handler)

	assert.NotNil(t, c)
	assert.Equal(t, handler, c.handler)
}

// extractIDs

func TestNATSDecommissionConsumerExtractIDsSuccess(t *testing.T) {
	c := &NATSDecommissionConsumer{}

	tenantID, gatewayID, err := c.extractIDs("gateway.decommissioned.tenant-1.gw-42")

	require.NoError(t, err)
	assert.Equal(t, "tenant-1", tenantID)
	assert.Equal(t, "gw-42", gatewayID)
}

func TestNATSDecommissionConsumerExtractIDsMalformedSubject(t *testing.T) {
	c := &NATSDecommissionConsumer{}

	_, _, err := c.extractIDs("gateway.decommissioned.only-three")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected subject format")
}

// handleMsg

func TestNATSDecommissionConsumerHandleMsgDispatches(t *testing.T) {
	handler := &stubDecommissionHandler{}
	c := &NATSDecommissionConsumer{handler: handler, logger: slog.Default()}

	msg := &nats.Msg{
		Subject: "gateway.decommissioned.t1.gw-1",
		Reply:   "_INBOX.test",
	}
	msg.Sub = &nats.Subscription{}

	// Manually call handleMsg — avoids needing a live NATS server.
	c.handleMsg(msg)

	// Handler must have been called with the correct IDs.
	assert.Equal(t, 1, handler.calls)
	assert.Equal(t, "t1", handler.tenantID)
	assert.Equal(t, "gw-1", handler.gatewayID)
}

func TestNATSDecommissionConsumerHandleMsgBadSubjectTerms(t *testing.T) {
	handler := &stubDecommissionHandler{}
	c := &NATSDecommissionConsumer{handler: handler, logger: slog.Default()}

	msg := &nats.Msg{Subject: "gateway.decommissioned.missing-gwid"}
	c.handleMsg(msg)

	assert.Equal(t, 0, handler.calls,
		"handler must not be called when the subject cannot be parsed")
}

// Run — subscribe error path

func TestNATSDecommissionConsumerRunReturnsSubscribeError(t *testing.T) {
	sentinel := errors.New("nats: not connected")
	c := newNATSDecommissionConsumer(&stubFailingSubscriber{err: sentinel}, &stubDecommissionHandler{})

	err := c.Run(context.Background())

	require.Error(t, err)
	assert.ErrorIs(t, err, sentinel)
	assert.Contains(t, err.Error(), "subscribe")
}

func TestNATSDecommissionConsumerRunStopsOnContextCancel(t *testing.T) {
	sub := &stubDecommissionSubscriber{}
	c := newNATSDecommissionConsumer(sub, &stubDecommissionHandler{})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- c.Run(ctx)
	}()

	cancel()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Run did not return after context cancellation")
	}

	assert.Equal(t, subjectDecommissioned, sub.subject)
}
