//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NoTIPswe/notip-data-consumer/internal/adapter/driven"
	"github.com/NoTIPswe/notip-data-consumer/internal/adapter/driving"
	"github.com/NoTIPswe/notip-data-consumer/internal/domain/model"
	"github.com/NoTIPswe/notip-data-consumer/internal/service"
)

const (
	e2eTenantID  = "tenant-e2e-full"
	e2eGatewayID = "gw-e2e-full"
	e2eSensorID  = "sensor-e2e-full"
)

// noopE2EMetrics satisfies all metric interfaces used by the production wiring
// (alertPublisher, alertCache, heartbeatTracker, statusUpdater, telemetryConsumer).
type noopE2EMetrics struct{}

func (*noopE2EMetrics) IncAlertsPublished()                          { /* no-op */ }
func (*noopE2EMetrics) IncAlertPublishErrors()                       { /* no-op */ }
func (*noopE2EMetrics) IncAlertCacheRefreshErrors()                  { /* no-op */ }
func (*noopE2EMetrics) SetAlertCacheLastSuccess(_ float64)           { /* no-op */ }
func (*noopE2EMetrics) IncStatusUpdateDropped()                      { /* no-op */ }
func (*noopE2EMetrics) SetHeartbeatMapSize(_ float64)                { /* no-op */ }
func (*noopE2EMetrics) SetDispatchQueueLength(_ float64)             { /* no-op */ }
func (*noopE2EMetrics) IncStatusUpdateErrors()                       { /* no-op */ }
func (*noopE2EMetrics) IncMessagesReceived()                         { /* no-op */ }
func (*noopE2EMetrics) IncMessageParsingErrors()                     { /* no-op */ }
func (*noopE2EMetrics) IncMessagesWritten()                          { /* no-op */ }
func (*noopE2EMetrics) IncWriteErrors()                              { /* no-op */ }
func (*noopE2EMetrics) ObserveWriteLatency(_ time.Duration)          { /* no-op */ }
func (*noopE2EMetrics) ObserveBatchSize(_ float64)                   { /* no-op */ }
func (*noopE2EMetrics) ObserveHeartbeatTickDuration(_ time.Duration) { /* no-op */ }

type noopE2ELifecycleProvider struct{}

func (*noopE2ELifecycleProvider) GetGatewayLifecycle(_ context.Context, _, _ string) (model.GatewayLifecycleState, error) {
	return model.LifecycleOnline, nil
}

// TestPipelineE2EFullDataFlow wires all production adapters together (mirroring
// main.go) and verifies the complete data path end-to-end:
//
//  1. Telemetry published to NATS JetStream → stored in TimescaleDB (Rule Zero).
//  2. HeartbeatTracker registers gateway as Online.
//  3. Clock advanced past timeout → Tick() fires offline alert on real ALERTS stream.
//  4. Offline status update dispatched via recording spy.
func TestPipelineE2EFullDataFlow(t *testing.T) {
	purgeStream(t, streamTelemetry)
	purgeStream(t, streamAlerts)
	truncateTelemetry(t)

	nc := connectNATSWithMTLS(t)
	js, err := nc.JetStream()
	require.NoError(t, err)

	// Subscribe to the alert subject before any telemetry is published.
	alertSub, err := js.SubscribeSync(
		fmt.Sprintf("alert.gw_offline.%s", e2eTenantID),
		nats.Durable(fmt.Sprintf("e2e-alert-sub-%d", time.Now().UnixNano())),
		nats.DeliverNew(),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = alertSub.Unsubscribe() })

	// Mock alert config responder — returns a 500ms timeout for the E2E tenant.
	configSub, err := nc.Subscribe("internal.mgmt.alert-configs.list", func(msg *nats.Msg) {
		configs := []model.AlertConfig{
			{TenantID: e2eTenantID, GatewayID: nil, TimeoutMs: 500},
		}
		data, _ := json.Marshal(configs)
		_ = msg.Respond(data)
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = configSub.Unsubscribe() })

	// ── Wire all production adapters (mirrors main.go) ───────────────────────
	m := &noopE2EMetrics{}
	rrClient := driven.NewNATSRRClient(nc, 5*time.Second)
	alertCache := driven.NewAlertConfigCache(rrClient, m, 60000, 1*time.Hour, 3)
	alertPublisher := driven.NewNATSAlertPublisher(js, m)
	statusSpy := &recordingStatusUpdater{}
	clock := newControllableClock(time.Now().UTC())

	tracker := service.NewHeartbeatTracker(
		clock,
		alertPublisher,
		statusSpy,
		alertCache,
		&noopE2ELifecycleProvider{},
		m,
		service.HeartbeatTrackerConfig{
			StatusUpdateBufSize: 100,
			GracePeriod:         0, // no grace period — alerts fire immediately after timeout
		},
	)
	defer tracker.Close()

	telemetryWriter := driven.NewPostgresTelemetryWriter(sharedPool)
	durableName := fmt.Sprintf("e2e-full-%d", time.Now().UnixNano())
	telemetryConsumer := driving.NewNATSTelemetryConsumer(
		js, tracker, telemetryWriter, m,
		durableName,
		10,
		100*time.Millisecond,
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go alertCache.Run(ctx)
	go func() { _ = telemetryConsumer.Run(ctx) }()

	// Wait for the JetStream consumer to register and the cache to be populated.
	require.Eventually(t, func() bool {
		_, err := sharedJS.ConsumerInfo(streamTelemetry, durableName)
		return err == nil
	}, 10*time.Second, 100*time.Millisecond, "telemetry consumer did not register in JetStream")

	require.Eventually(t, func() bool {
		return alertCache.TimeoutFor(e2eTenantID, "any") == 500
	}, 10*time.Second, 50*time.Millisecond, "alert config cache never populated")

	// ── Phase 1: Telemetry → TimescaleDB (Rule Zero) ─────────────────────────
	envelope := model.TelemetryEnvelope{
		GatewayID:     e2eGatewayID,
		SensorID:      e2eSensorID,
		SensorType:    "temperature",
		Timestamp:     clock.Now(),
		KeyVersion:    1,
		EncryptedData: model.OpaqueBlob{Value: "ZW5jcnlwdGVkLWRhdGE="},
		IV:            model.OpaqueBlob{Value: "aXYtdmFsdWU="},
		AuthTag:       model.OpaqueBlob{Value: "YXV0aC10YWc="},
	}
	payload, err := json.Marshal(envelope)
	require.NoError(t, err)

	_, err = js.Publish(fmt.Sprintf("telemetry.data.%s.%s", e2eTenantID, e2eGatewayID), payload)
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		var count int
		_ = sharedPool.QueryRow(ctx,
			"SELECT COUNT(*) FROM telemetry WHERE tenant_id = $1 AND sensor_id = $2",
			e2eTenantID, e2eSensorID,
		).Scan(&count)
		return count == 1
	}, 10*time.Second, 100*time.Millisecond, "telemetry row never appeared in TimescaleDB")

	// Rule Zero: encrypted blobs must be stored verbatim — never decoded.
	var encData, iv, authTag string
	err = sharedPool.QueryRow(ctx,
		"SELECT encrypted_data, iv, auth_tag FROM telemetry WHERE tenant_id = $1 AND sensor_id = $2",
		e2eTenantID, e2eSensorID,
	).Scan(&encData, &iv, &authTag)
	require.NoError(t, err)
	assert.Equal(t, "ZW5jcnlwdGVkLWRhdGE=", encData)
	assert.Equal(t, "aXYtdmFsdWU=", iv)
	assert.Equal(t, "YXV0aC10YWc=", authTag)

	// ── Phase 2: Gateway comes Online ────────────────────────────────────────
	require.Eventually(t, func() bool {
		for _, u := range statusSpy.getUpdates() {
			if u.GatewayID == e2eGatewayID && u.Status == model.Online {
				return true
			}
		}
		return false
	}, 5*time.Second, 20*time.Millisecond, "Online status update never dispatched")

	// ── Phase 3: Timeout → offline alert on real NATS ALERTS stream ──────────
	clock.Advance(600 * time.Millisecond) // > 500ms configured timeout
	tracker.Tick(ctx)

	alertMsg, err := alertSub.NextMsg(5 * time.Second)
	require.NoError(t, err, "offline alert never appeared on JetStream ALERTS stream")

	var alertPayload model.AlertPayload
	require.NoError(t, json.Unmarshal(alertMsg.Data, &alertPayload))
	assert.Equal(t, e2eGatewayID, alertPayload.GatewayID)
	assert.Equal(t, int64(500), alertPayload.TimeoutMs)

	// ── Phase 4: Offline status update dispatched ─────────────────────────────
	require.Eventually(t, func() bool {
		for _, u := range statusSpy.getUpdates() {
			if u.GatewayID == e2eGatewayID && u.Status == model.Offline {
				return true
			}
		}
		return false
	}, 5*time.Second, 20*time.Millisecond, "Offline status update never dispatched")
}
