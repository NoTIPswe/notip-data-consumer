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
	require.Error(t, err, "expected timeout error when no responder is available")
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
	require.Error(t, err, "expected timeout error when no responder is available")
}
