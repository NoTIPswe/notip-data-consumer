//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NoTIPswe/notip-data-consumer/internal/adapter/driven"
	"github.com/NoTIPswe/notip-data-consumer/internal/domain/model"
)

const testGatewayIDLifecycle = "gw-prov-test"

// recordingLifecycleMetrics counts lifecycle query errors for assertion.
type recordingLifecycleMetrics struct {
	errors int
}

func (r *recordingLifecycleMetrics) IncLifecycleQueryErrors() { r.errors++ }

// TestNATSGatewayLifecycleProviderIntegrationReturnsPausedState sets up a mock
// responder and verifies the provider correctly returns a paused lifecycle state.
func TestNATSGatewayLifecycleProviderIntegrationReturnsPausedState(t *testing.T) {
	nc := connectNATSWithMTLS(t)
	rrClient := driven.NewNATSRRClient(nc, 5*time.Second)
	m := &recordingLifecycleMetrics{}
	provider := driven.NewNATSGatewayLifecycleProvider(rrClient, m)

	received := make(chan model.GatewayLifecycleRequest, 1)
	sub, err := nc.Subscribe("internal.mgmt.gateway.get-status", func(msg *nats.Msg) {
		var req model.GatewayLifecycleRequest
		if err := json.Unmarshal(msg.Data, &req); err == nil {
			received <- req
		}
		resp := model.GatewayLifecycleResponse{GatewayID: testGatewayIDLifecycle, State: model.LifecyclePaused}
		data, _ := json.Marshal(resp)
		_ = msg.Respond(data)
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = sub.Unsubscribe() })

	state, err := provider.GetGatewayLifecycle(context.Background(), "tenant-prov", testGatewayIDLifecycle)

	require.NoError(t, err)
	assert.Equal(t, model.LifecyclePaused, state)
	assert.Equal(t, 0, m.errors)

	select {
	case got := <-received:
		assert.Equal(t, testGatewayIDLifecycle, got.GatewayID)
		assert.Equal(t, "tenant-prov", got.TenantID)
	case <-time.After(5 * time.Second):
		t.Fatal("mock responder never received the lifecycle request")
	}
}

// TestNATSGatewayLifecycleProviderIntegrationOnlineState verifies the online state
// path, which is the common case when no lifecycle override is active.
func TestNATSGatewayLifecycleProviderIntegrationOnlineState(t *testing.T) {
	nc := connectNATSWithMTLS(t)
	rrClient := driven.NewNATSRRClient(nc, 5*time.Second)
	provider := driven.NewNATSGatewayLifecycleProvider(rrClient, &recordingLifecycleMetrics{})

	sub, err := nc.Subscribe("internal.mgmt.gateway.get-status", func(msg *nats.Msg) {
		resp := model.GatewayLifecycleResponse{GatewayID: "gw-online-test", State: model.LifecycleOnline}
		data, _ := json.Marshal(resp)
		_ = msg.Respond(data)
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = sub.Unsubscribe() })

	state, err := provider.GetGatewayLifecycle(context.Background(), "tenant-online", "gw-online-test")

	require.NoError(t, err)
	assert.Equal(t, model.LifecycleOnline, state)
}

// TestNATSGatewayLifecycleProviderIntegrationTimeoutIncrementsErrorMetric verifies
// that when the management API is unreachable, the provider returns an error AND
// increments the lifecycle query error counter — the same path Tick relies on for
// its fail-open log line and metric.
func TestNATSGatewayLifecycleProviderIntegrationTimeoutIncrementsErrorMetric(t *testing.T) {
	nc := connectNATSWithMTLS(t)
	// Very short timeout — no responder registered, so the request must time out.
	rrClient := driven.NewNATSRRClient(nc, 200*time.Millisecond)
	m := &recordingLifecycleMetrics{}
	provider := driven.NewNATSGatewayLifecycleProvider(rrClient, m)

	state, err := provider.GetGatewayLifecycle(context.Background(), "tenant-timeout", "gw-timeout")

	require.Error(t, err)
	assert.Equal(t, model.LifecycleUnknown, state)
	assert.Equal(t, 1, m.errors,
		"a failed lifecycle query must increment the error counter exactly once")
}
