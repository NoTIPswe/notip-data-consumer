//go:build integration

package integration

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NoTIPswe/notip-data-consumer/internal/adapter/driving"
)

// recordingDecommissionHandler captures decommission events for assertion.
type recordingDecommissionHandler struct {
	mu     sync.Mutex
	events []decommissionEvent
}

type decommissionEvent struct {
	tenantID  string
	gatewayID string
}

func (h *recordingDecommissionHandler) HandleDecommission(tenantID, gatewayID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.events = append(h.events, decommissionEvent{tenantID, gatewayID})
}

func (h *recordingDecommissionHandler) getEvents() []decommissionEvent {
	h.mu.Lock()
	defer h.mu.Unlock()
	cp := make([]decommissionEvent, len(h.events))
	copy(cp, h.events)
	return cp
}

// TestNATSDecommissionConsumerIntegrationProcessesEvent publishes a
// decommission event and verifies the handler is called with the correct
// tenant and gateway IDs extracted from the subject.
func TestNATSDecommissionConsumerIntegrationProcessesEvent(t *testing.T) {
	purgeStream(t, streamDecommission)

	nc := connectNATSWithMTLS(t)
	js, err := nc.JetStream()
	require.NoError(t, err)

	handler := &recordingDecommissionHandler{}
	consumer := driving.NewNATSDecommissionConsumer(js, handler)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = consumer.Run(ctx) }()

	// Publish a decommission event. The body is irrelevant — IDs come from the subject.
	_, err = js.Publish("gateway.decommissioned.tenant-decomm.gw-decomm-001", []byte("{}"))
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		return len(handler.getEvents()) == 1
	}, 10*time.Second, 50*time.Millisecond, "decommission handler never called")

	events := handler.getEvents()
	assert.Equal(t, "tenant-decomm", events[0].tenantID)
	assert.Equal(t, "gw-decomm-001", events[0].gatewayID)
}

// TestNATSDecommissionConsumerIntegrationMultipleEvents verifies that
// multiple decommission events for different tenants/gateways are all processed.
func TestNATSDecommissionConsumerIntegrationMultipleEvents(t *testing.T) {
	purgeStream(t, streamDecommission)

	nc := connectNATSWithMTLS(t)
	js, err := nc.JetStream()
	require.NoError(t, err)

	handler := &recordingDecommissionHandler{}
	consumer := driving.NewNATSDecommissionConsumer(js, handler)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = consumer.Run(ctx) }()

	subjects := []string{
		"gateway.decommissioned.tenant-1.gw-A",
		"gateway.decommissioned.tenant-1.gw-B",
		"gateway.decommissioned.tenant-2.gw-C",
	}
	for _, subj := range subjects {
		_, err := js.Publish(subj, []byte("{}"))
		require.NoError(t, err)
	}

	require.Eventually(t, func() bool {
		return len(handler.getEvents()) == 3
	}, 10*time.Second, 50*time.Millisecond, "not all decommission events were processed")

	events := handler.getEvents()
	seen := make(map[string]bool)
	for _, e := range events {
		seen[e.tenantID+"/"+e.gatewayID] = true
	}
	assert.True(t, seen["tenant-1/gw-A"])
	assert.True(t, seen["tenant-1/gw-B"])
	assert.True(t, seen["tenant-2/gw-C"])
}

// TestNATSDecommissionConsumerIntegrationMalformedSubjectIsTermed publishes
// an event with an invalid subject (too many tokens) and verifies it is Term()d —
// meaning NATS drops it permanently (NumPending and NumAckPending both reach 0)
// and the handler is never called.
func TestNATSDecommissionConsumerIntegrationMalformedSubjectIsTermed(t *testing.T) {
	purgeStream(t, streamDecommission)

	nc := connectNATSWithMTLS(t)
	js, err := nc.JetStream()
	require.NoError(t, err)

	handler := &recordingDecommissionHandler{}
	consumer := driving.NewNATSDecommissionConsumer(js, handler)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = consumer.Run(ctx) }()

	// The decommission consumer expects exactly 4 subject tokens.
	// "gateway.decommissioned.>" matches this, but the consumer's extractIDs
	// rejects subjects with != 4 parts. A 5-token subject is accepted by the
	// stream but Term()d by the consumer.
	_, err = js.Publish("gateway.decommissioned.tenant.gw.extra", []byte("{}"))
	require.NoError(t, err)

	// A Term()d message is gone: NumPending and NumAckPending both drop to 0.
	const durableDecommission = "data-consumer-decommission-listener"
	require.Eventually(t, func() bool {
		info, err := sharedJS.ConsumerInfo(streamDecommission, durableDecommission)
		return err == nil && info.NumPending == 0 && info.NumAckPending == 0
	}, 10*time.Second, 100*time.Millisecond, "message was not Term()d")

	// Handler must never have been called.
	assert.Empty(t, handler.getEvents(), "malformed subject should not trigger handler")
}

// TestNATSDecommissionConsumerIntegrationRunStopsOnContextCancel verifies that
// Run() unsubscribes and returns nil when the context is cancelled.
func TestNATSDecommissionConsumerIntegrationRunStopsOnContextCancel(t *testing.T) {
	nc := connectNATSWithMTLS(t)
	js, err := nc.JetStream()
	require.NoError(t, err)

	consumer := driving.NewNATSDecommissionConsumer(js, &recordingDecommissionHandler{})

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- consumer.Run(ctx) }()

	cancel()

	select {
	case err := <-done:
		require.NoError(t, err, "Run must return nil on clean shutdown")
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after context cancellation")
	}
}
