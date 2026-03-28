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

// noopAlertPublisherMetrics satisfies alertPublisherMetrics without recording anything.
type noopAlertPublisherMetrics struct{}

func (*noopAlertPublisherMetrics) IncAlertsPublished()    { /* empty for decision */ }
func (*noopAlertPublisherMetrics) IncAlertPublishErrors() { /* empty for decision */ }

// TestNATSAlertPublisherIntegrationPublishToCorrectSubject publishes an alert
// and verifies that a subscriber on alert.gw_offline.{tenantId} receives the
// exact JSON payload.
func TestNATSAlertPublisherIntegrationPublishToCorrectSubject(t *testing.T) {
	purgeStream(t, streamAlerts)

	nc := connectNATSWithMTLS(t)
	js, err := nc.JetStream()
	require.NoError(t, err)

	// Subscribe before publishing so nothing is missed.
	sub, err := js.SubscribeSync("alert.gw_offline.tenant-alert-test", nats.Durable("alert-test-sub"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = sub.Unsubscribe() })

	publisher := driven.NewNATSAlertPublisher(js, &noopAlertPublisherMetrics{})

	now := time.Now().UTC().Truncate(time.Microsecond)
	payload := model.AlertPayload{
		GatewayID: "gw-alert-test",
		LastSeen:  now.Add(-2 * time.Minute),
		TimeoutMs: 60000,
		Timestamp: now,
	}

	require.NoError(t, publisher.Publish(context.Background(), "tenant-alert-test", payload))

	msg, err := sub.NextMsg(5 * time.Second)
	require.NoError(t, err, "alert message never arrived on JetStream")

	var got model.AlertPayload
	require.NoError(t, json.Unmarshal(msg.Data, &got))
	assert.Equal(t, payload.GatewayID, got.GatewayID)
	assert.Equal(t, payload.TimeoutMs, got.TimeoutMs)
	assert.WithinDuration(t, payload.LastSeen, got.LastSeen, time.Millisecond)
	assert.WithinDuration(t, payload.Timestamp, got.Timestamp, time.Millisecond)
}

// TestNATSAlertPublisherIntegrationMultiTenantIsolation publishes alerts for
// two tenants and verifies each subscriber only receives its own tenant's alert.
func TestNATSAlertPublisherIntegrationMultiTenantIsolation(t *testing.T) {
	purgeStream(t, streamAlerts)

	nc := connectNATSWithMTLS(t)
	js, err := nc.JetStream()
	require.NoError(t, err)

	subA, err := js.SubscribeSync("alert.gw_offline.tenant-A", nats.Durable("iso-sub-a"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = subA.Unsubscribe() })

	subB, err := js.SubscribeSync("alert.gw_offline.tenant-B", nats.Durable("iso-sub-b"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = subB.Unsubscribe() })

	publisher := driven.NewNATSAlertPublisher(js, &noopAlertPublisherMetrics{})
	now := time.Now().UTC().Truncate(time.Microsecond)

	require.NoError(t, publisher.Publish(context.Background(), "tenant-A", model.AlertPayload{
		GatewayID: "gw-A", LastSeen: now, TimeoutMs: 60000, Timestamp: now,
	}))
	require.NoError(t, publisher.Publish(context.Background(), "tenant-B", model.AlertPayload{
		GatewayID: "gw-B", LastSeen: now, TimeoutMs: 30000, Timestamp: now,
	}))

	msgA, err := subA.NextMsg(5 * time.Second)
	require.NoError(t, err)
	var gotA model.AlertPayload
	require.NoError(t, json.Unmarshal(msgA.Data, &gotA))
	assert.Equal(t, "gw-A", gotA.GatewayID)

	msgB, err := subB.NextMsg(5 * time.Second)
	require.NoError(t, err)
	var gotB model.AlertPayload
	require.NoError(t, json.Unmarshal(msgB.Data, &gotB))
	assert.Equal(t, "gw-B", gotB.GatewayID)

	// Tenant A should not receive tenant B's alert.
	_, err = subA.NextMsg(500 * time.Millisecond)
	assert.ErrorIs(t, err, nats.ErrTimeout)
}
