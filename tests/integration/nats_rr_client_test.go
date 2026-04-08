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

const (
	expectedTimeoutError = "expected timeout error when no responder is available"
	testGatewayIDRR      = "gw-lc-test"
	testLifecycleTenant  = "tenant-lc"
)

// TestNATSRRClientIntegrationFetchAlertConfigs sets up a mock responder on the
// alert-configs subject and verifies that the RR client correctly deserialises
// the response into []model.AlertConfig.
func TestNATSRRClientIntegrationFetchAlertConfigs(t *testing.T) {
	nc := connectNATSWithMTLS(t)
	client := driven.NewNATSRRClient(nc, 5*time.Second)

	const tenantRR = "tenant-rr"

	gwID := "gw-specific"
	mockConfigs := []model.AlertConfig{
		{TenantID: tenantRR, GatewayID: &gwID, TimeoutMs: 30000},
		{TenantID: tenantRR, GatewayID: nil, TimeoutMs: 60000},
	}

	// Mock responder: reply with JSON-encoded alert configs.
	sub, err := nc.Subscribe("internal.mgmt.alert-configs.list", func(msg *nats.Msg) {
		data, _ := json.Marshal(mockConfigs)
		_ = msg.Respond(data)
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = sub.Unsubscribe() })

	configs, err := client.FetchAlertConfigs(context.Background())
	require.NoError(t, err)
	require.Len(t, configs, 2)

	assert.Equal(t, tenantRR, configs[0].TenantID)
	require.NotNil(t, configs[0].GatewayID)
	assert.Equal(t, "gw-specific", *configs[0].GatewayID)
	assert.Equal(t, int64(30000), configs[0].TimeoutMs)

	assert.Equal(t, tenantRR, configs[1].TenantID)
	assert.Nil(t, configs[1].GatewayID)
	assert.Equal(t, int64(60000), configs[1].TimeoutMs)
}

// TestNATSRRClientIntegrationFetchAlertConfigsTimeout verifies that the RR
// client returns an error when no responder answers within the configured timeout.
func TestNATSRRClientIntegrationFetchAlertConfigsTimeout(t *testing.T) {
	nc := connectNATSWithMTLS(t)
	// Very short timeout — no responder registered, so the request must time out.
	client := driven.NewNATSRRClient(nc, 200*time.Millisecond)

	_, err := client.FetchAlertConfigs(context.Background())
	require.Error(t, err, expectedTimeoutError)
}

// TestNATSRRClientIntegrationUpdateGatewayStatus sets up a mock responder on
// the gateway status subject and verifies the request body contains the
// serialised GatewayStatusUpdate.
func TestNATSRRClientIntegrationUpdateGatewayStatus(t *testing.T) {
	nc := connectNATSWithMTLS(t)
	client := driven.NewNATSRRClient(nc, 5*time.Second)

	received := make(chan model.GatewayStatusUpdate, 1)

	sub, err := nc.Subscribe("internal.mgmt.gateway.update-status", func(msg *nats.Msg) {
		var update model.GatewayStatusUpdate
		if err := json.Unmarshal(msg.Data, &update); err == nil {
			received <- update
		}
		_ = msg.Respond([]byte(`{"success":true}`))
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = sub.Unsubscribe() })

	now := time.Now().UTC().Truncate(time.Microsecond)
	update := model.GatewayStatusUpdate{
		GatewayID:  "gw-status-test",
		Status:     model.Online,
		LastSeenAt: now,
	}

	require.NoError(t, client.UpdateGatewayStatus(context.Background(), update))

	select {
	case got := <-received:
		assert.Equal(t, "gw-status-test", got.GatewayID)
		assert.Equal(t, model.Online, got.Status)
		assert.WithinDuration(t, now, got.LastSeenAt, time.Millisecond)
	case <-time.After(5 * time.Second):
		t.Fatal("mock responder never received the status update")
	}
}

// TestNATSRRClientIntegrationUpdateGatewayStatusTimeout verifies that the RR
// client returns an error when no responder answers the status update.
func TestNATSRRClientIntegrationUpdateGatewayStatusTimeout(t *testing.T) {
	nc := connectNATSWithMTLS(t)
	client := driven.NewNATSRRClient(nc, 200*time.Millisecond)

	err := client.UpdateGatewayStatus(context.Background(), model.GatewayStatusUpdate{
		GatewayID:  "gw-timeout",
		Status:     model.Offline,
		LastSeenAt: time.Now(),
	})
	require.Error(t, err, expectedTimeoutError)
}

// TestNATSRRClientIntegrationGetGatewayLifecycle sets up a mock responder on the
// get-status subject and verifies the RR client correctly deserialises the lifecycle state.
func TestNATSRRClientIntegrationGetGatewayLifecycle(t *testing.T) {
	nc := connectNATSWithMTLS(t)
	client := driven.NewNATSRRClient(nc, 5*time.Second)

	received := make(chan model.GatewayLifecycleRequest, 1)

	sub, err := nc.Subscribe("internal.mgmt.gateway.get-status", func(msg *nats.Msg) {
		var req model.GatewayLifecycleRequest
		if err := json.Unmarshal(msg.Data, &req); err == nil {
			received <- req
		}
		resp := model.GatewayLifecycleResponse{GatewayID: testGatewayIDRR, State: model.LifecyclePaused}
		data, _ := json.Marshal(resp)
		_ = msg.Respond(data)
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = sub.Unsubscribe() })

	state, err := client.GetGatewayLifecycle(context.Background(), testLifecycleTenant, testGatewayIDRR)
	require.NoError(t, err)
	assert.Equal(t, model.LifecyclePaused, state)

	select {
	case got := <-received:
		assert.Equal(t, testGatewayIDRR, got.GatewayID)
		assert.Equal(t, testLifecycleTenant, got.TenantID)
	case <-time.After(5 * time.Second):
		t.Fatal("mock responder never received the lifecycle request")
	}
}

// TestNATSRRClientIntegrationGetGatewayLifecycleTimeout verifies that the RR
// client returns an error when no responder answers the lifecycle query.
func TestNATSRRClientIntegrationGetGatewayLifecycleTimeout(t *testing.T) {
	nc := connectNATSWithMTLS(t)
	client := driven.NewNATSRRClient(nc, 200*time.Millisecond)

	_, err := client.GetGatewayLifecycle(context.Background(), "tenant-timeout", "gw-timeout")
	require.Error(t, err, expectedTimeoutError)
}

// TestNATSRRClientIntegrationGetGatewayLifecycleRejectsInvalidResponse verifies
// that malformed lifecycle responses are treated as errors and mapped to unknown.
func TestNATSRRClientIntegrationGetGatewayLifecycleRejectsInvalidResponse(t *testing.T) {
	nc := connectNATSWithMTLS(t)
	client := driven.NewNATSRRClient(nc, 5*time.Second)

	sub, err := nc.Subscribe("internal.mgmt.gateway.get-status", func(msg *nats.Msg) {
		_ = msg.Respond([]byte(`{"gateway_id":"gw-lc-test","state":"invalid-state"}`))
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = sub.Unsubscribe() })

	state, err := client.GetGatewayLifecycle(context.Background(), testLifecycleTenant, "gw-lc-test")
	require.Error(t, err)
	assert.Equal(t, model.LifecycleUnknown, state)
}
