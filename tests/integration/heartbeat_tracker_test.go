//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NoTIPswe/notip-data-consumer/internal/adapter/driven"
	"github.com/NoTIPswe/notip-data-consumer/internal/domain/model"
	"github.com/NoTIPswe/notip-data-consumer/internal/service"
)

// noopLifecycleMetrics satisfies lifecycleQueryErrRecorder without recording.
type noopLifecycleMetrics struct{}

func (*noopLifecycleMetrics) IncLifecycleQueryErrors() { /* no-op: metrics not under test */ }

// controllableClock lets us advance time deterministically for the heartbeat tracker.
type controllableClock struct {
	mu  sync.Mutex
	now time.Time
}

func newControllableClock(t time.Time) *controllableClock {
	return &controllableClock{now: t}
}

func (c *controllableClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *controllableClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// fixedAlertConfigProvider returns a fixed timeout for all gateways.
type fixedAlertConfigProvider struct {
	timeoutMs int64
}

func (p *fixedAlertConfigProvider) TimeoutFor(_, _ string) int64 { return p.timeoutMs }

// noopTrackerMetrics satisfies HeartbeatTrackerMetrics without recording.
type noopTrackerMetrics struct {
	mapSize atomic.Int64
}

func (m *noopTrackerMetrics) IncStatusUpdateDropped()          { /* no-op */ }
func (m *noopTrackerMetrics) SetHeartbeatMapSize(v float64)    { m.mapSize.Store(int64(v)) }
func (m *noopTrackerMetrics) SetDispatchQueueLength(_ float64) { /* no-op */ }

// noopLifecycleProvider always reports online — lifecycle gating is not under test here.
type noopLifecycleProvider struct{}

func (p *noopLifecycleProvider) GetGatewayLifecycle(_ context.Context, _, _ string) (model.GatewayLifecycleState, error) {
	return model.LifecycleOnline, nil
}

// recordingStatusUpdater captures status updates dispatched by the tracker.
type recordingStatusUpdater struct {
	mu      sync.Mutex
	updates []model.GatewayStatusUpdate
}

func (r *recordingStatusUpdater) UpdateStatus(_ context.Context, update model.GatewayStatusUpdate) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.updates = append(r.updates, update)
	return nil
}

func (r *recordingStatusUpdater) getUpdates() []model.GatewayStatusUpdate {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := make([]model.GatewayStatusUpdate, len(r.updates))
	copy(cp, r.updates)
	return cp
}

// TestHeartbeatTrackerIntegrationFullLifecycle exercises the complete flow:
//  1. First telemetry → registers gateway as Online, dispatches Online status update
//  2. Advance clock past timeout → Tick() publishes offline alert to real JetStream,
//     dispatches Offline status update
//  3. New telemetry → gateway recovers to Online, dispatches Online status update
//
// Alert publishing goes through the real NATSAlertPublisher → real JetStream.
// Status updates go through a recording spy (the NATS RR path is already
// covered by nats_rr_client_test.go).
func TestHeartbeatTrackerIntegrationFullLifecycle(t *testing.T) {
	purgeStream(t, streamAlerts)

	const gwID = "gw-hb-test"
	const tenantID = "tenant-hb"

	nc := connectNATSWithMTLS(t)
	js, err := nc.JetStream()
	require.NoError(t, err)

	// Subscribe to the alert subject BEFORE any alerts are published.
	alertSub, err := js.SubscribeSync(
		fmt.Sprintf("alert.gw_offline.%s", tenantID),
		nats.Durable("hb-alert-sub"),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = alertSub.Unsubscribe() })

	// Build the tracker with real alert publisher, controllable clock, and recording spy.
	alertPublisher := driven.NewNATSAlertPublisher(js, &noopAlertPublisherMetrics{})
	statusUpdater := &recordingStatusUpdater{}
	clock := newControllableClock(time.Now().UTC())
	trackerMetrics := &noopTrackerMetrics{}

	tracker := service.NewHeartbeatTracker(
		clock,
		alertPublisher,
		statusUpdater,
		&fixedAlertConfigProvider{timeoutMs: 500}, // 500ms timeout for fast test
		&noopLifecycleProvider{},
		trackerMetrics,
		service.HeartbeatTrackerConfig{StatusUpdateBufSize: 100}, // zero GracePeriod — alerts active immediately
	)
	defer tracker.Close()

	ctx := context.Background()

	// ── Phase 1: First telemetry registers the gateway as Online ────────────
	envelope := model.TelemetryEnvelope{
		GatewayID:     gwID,
		SensorID:      "sensor-1",
		SensorType:    "temperature",
		Timestamp:     clock.Now(),
		KeyVersion:    1,
		EncryptedData: model.OpaqueBlob{Value: "ZW5j"},
		IV:            model.OpaqueBlob{Value: "aXY="},
		AuthTag:       model.OpaqueBlob{Value: "dGFn"},
	}
	require.NoError(t, tracker.HandleTelemetry(ctx, tenantID, envelope))

	// The tracker dispatches status updates asynchronously — wait for it.
	require.Eventually(t, func() bool {
		updates := statusUpdater.getUpdates()
		return len(updates) >= 1 && updates[0].Status == model.Online
	}, 5*time.Second, 20*time.Millisecond, "Online status update never dispatched")

	assert.Equal(t, int64(1), trackerMetrics.mapSize.Load())

	// ── Phase 2: Advance clock past timeout and tick ────────────────────────
	clock.Advance(600 * time.Millisecond) // > 500ms timeout
	tracker.Tick(ctx)

	// Offline alert should appear on real JetStream.
	alertMsg, err := alertSub.NextMsg(5 * time.Second)
	require.NoError(t, err, "offline alert never appeared on JetStream")

	var alertPayload model.AlertPayload
	require.NoError(t, json.Unmarshal(alertMsg.Data, &alertPayload))
	assert.Equal(t, gwID, alertPayload.GatewayID)
	assert.Equal(t, int64(500), alertPayload.TimeoutMs)

	// Offline status update should also have been dispatched.
	require.Eventually(t, func() bool {
		updates := statusUpdater.getUpdates()
		for _, u := range updates {
			if u.Status == model.Offline && u.GatewayID == gwID {
				return true
			}
		}
		return false
	}, 5*time.Second, 20*time.Millisecond, "Offline status update never dispatched")

	// ── Phase 3: New telemetry recovers the gateway ─────────────────────────
	clock.Advance(100 * time.Millisecond)
	envelope.Timestamp = clock.Now()
	require.NoError(t, tracker.HandleTelemetry(ctx, tenantID, envelope))

	// Recovery: another Online status update.
	require.Eventually(t, func() bool {
		updates := statusUpdater.getUpdates()
		onlineCount := 0
		for _, u := range updates {
			if u.Status == model.Online && u.GatewayID == gwID {
				onlineCount++
			}
		}
		return onlineCount >= 2 // first registration + recovery
	}, 5*time.Second, 20*time.Millisecond, "recovery Online status update never dispatched")
}

// TestHeartbeatTrackerIntegrationGracePeriodSuppressesAlerts verifies that
// Tick() does not fire alerts during the grace period, even if the gateway
// timeout has expired.
func TestHeartbeatTrackerIntegrationGracePeriodSuppressesAlerts(t *testing.T) {
	purgeStream(t, streamAlerts)

	nc := connectNATSWithMTLS(t)
	js, err := nc.JetStream()
	require.NoError(t, err)

	// Use a unique durable name + DeliverNew to avoid picking up stale messages
	// from prior tests that published to alert.> subjects.
	alertSub, err := js.SubscribeSync(
		"alert.gw_offline.tenant-grace",
		nats.Durable(fmt.Sprintf("grace-alert-sub-%d", time.Now().UnixNano())),
		nats.DeliverNew(),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = alertSub.Unsubscribe() })

	alertPublisher := driven.NewNATSAlertPublisher(js, &noopAlertPublisherMetrics{})
	statusUpdater := &recordingStatusUpdater{}
	clock := newControllableClock(time.Now().UTC())

	tracker := service.NewHeartbeatTracker(
		clock,
		alertPublisher,
		statusUpdater,
		&fixedAlertConfigProvider{timeoutMs: 100},
		&noopLifecycleProvider{},
		&noopTrackerMetrics{},
		service.HeartbeatTrackerConfig{StatusUpdateBufSize: 100, GracePeriod: 5 * time.Second},
	)
	defer tracker.Close()

	ctx := context.Background()

	// Register a gateway.
	require.NoError(t, tracker.HandleTelemetry(ctx, "tenant-grace", model.TelemetryEnvelope{
		GatewayID:     "gw-grace",
		SensorID:      "s1",
		SensorType:    "temp",
		Timestamp:     clock.Now(),
		KeyVersion:    1,
		EncryptedData: model.OpaqueBlob{Value: "ZQ=="},
		IV:            model.OpaqueBlob{Value: "aQ=="},
		AuthTag:       model.OpaqueBlob{Value: "dA=="},
	}))

	// Advance past the gateway timeout but still within the grace period.
	clock.Advance(200 * time.Millisecond) // > 100ms timeout, but < 5s grace
	tracker.Tick(ctx)

	// No alert should fire during the grace period.
	_, err = alertSub.NextMsg(500 * time.Millisecond)
	assert.ErrorIs(t, err, nats.ErrTimeout, "no alert should fire during grace period")
}

// TestHeartbeatTrackerIntegrationDecommissionRemovesGateway verifies that
// after HandleDecommission, the gateway is no longer tracked and Tick()
// does not fire an alert for it.
func TestHeartbeatTrackerIntegrationDecommissionRemovesGateway(t *testing.T) {
	purgeStream(t, streamAlerts)

	nc := connectNATSWithMTLS(t)
	js, err := nc.JetStream()
	require.NoError(t, err)

	alertSub, err := js.SubscribeSync(
		"alert.gw_offline.tenant-dec",
		nats.Durable(fmt.Sprintf("dec-alert-sub-%d", time.Now().UnixNano())),
		nats.DeliverNew(),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = alertSub.Unsubscribe() })

	alertPublisher := driven.NewNATSAlertPublisher(js, &noopAlertPublisherMetrics{})
	statusUpdater := &recordingStatusUpdater{}
	clock := newControllableClock(time.Now().UTC())
	trackerMetrics := &noopTrackerMetrics{}

	tracker := service.NewHeartbeatTracker(
		clock,
		alertPublisher,
		statusUpdater,
		&fixedAlertConfigProvider{timeoutMs: 100},
		&noopLifecycleProvider{},
		trackerMetrics,
		service.HeartbeatTrackerConfig{StatusUpdateBufSize: 100}, // zero GracePeriod — no grace period
	)
	defer tracker.Close()

	ctx := context.Background()

	// Register a gateway.
	require.NoError(t, tracker.HandleTelemetry(ctx, "tenant-dec", model.TelemetryEnvelope{
		GatewayID:     "gw-dec",
		SensorID:      "s1",
		SensorType:    "temp",
		Timestamp:     clock.Now(),
		KeyVersion:    1,
		EncryptedData: model.OpaqueBlob{Value: "ZQ=="},
		IV:            model.OpaqueBlob{Value: "aQ=="},
		AuthTag:       model.OpaqueBlob{Value: "dA=="},
	}))
	assert.Equal(t, int64(1), trackerMetrics.mapSize.Load())

	// Decommission the gateway.
	tracker.HandleDecommission("tenant-dec", "gw-dec")
	assert.Equal(t, int64(0), trackerMetrics.mapSize.Load())

	// Advance past timeout and tick — no alert should fire.
	clock.Advance(200 * time.Millisecond)
	tracker.Tick(ctx)

	_, err = alertSub.NextMsg(500 * time.Millisecond)
	assert.ErrorIs(t, err, nats.ErrTimeout, "decommissioned gateway should not trigger alert")
}

// TestHeartbeatTrackerIntegrationPausedLifecycleSuppressesAlert verifies that when
// the Management API reports a gateway as paused, Tick() does not publish an offline
// alert even after the heartbeat timeout has expired.
//
// Uses a real NATSRRClient + NATSGatewayLifecycleProvider wired to a mock NATS
// responder, so the full lifecycle-gate path (RR → provider → Tick gate) is exercised.
func TestHeartbeatTrackerIntegrationPausedLifecycleSuppressesAlert(t *testing.T) {
	purgeStream(t, streamAlerts)

	const gwID = "gw-paused-test"
	const tenantID = "tenant-paused"

	nc := connectNATSWithMTLS(t)
	js, err := nc.JetStream()
	require.NoError(t, err)

	// Subscribe to alert subject with DeliverNew so stale messages are ignored.
	alertSub, err := js.SubscribeSync(
		fmt.Sprintf("alert.gw_offline.%s", tenantID),
		nats.Durable(fmt.Sprintf("paused-alert-sub-%d", time.Now().UnixNano())),
		nats.DeliverNew(),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = alertSub.Unsubscribe() })

	// Mock Management API responder: always reports this gateway as paused.
	lifecycleSub, err := nc.Subscribe("internal.mgmt.gateway.get-status", func(msg *nats.Msg) {
		resp := model.GatewayLifecycleResponse{GatewayID: gwID, State: model.LifecyclePaused}
		data, _ := json.Marshal(resp)
		_ = msg.Respond(data)
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = lifecycleSub.Unsubscribe() })

	// Wire real RR client + lifecycle provider.
	rrClient := driven.NewNATSRRClient(nc, 5*time.Second)
	lifecycleProvider := driven.NewNATSGatewayLifecycleProvider(rrClient, &noopLifecycleMetrics{})

	alertPublisher := driven.NewNATSAlertPublisher(js, &noopAlertPublisherMetrics{})
	statusUpdater := &recordingStatusUpdater{}
	clock := newControllableClock(time.Now().UTC())

	tracker := service.NewHeartbeatTracker(
		clock,
		alertPublisher,
		statusUpdater,
		&fixedAlertConfigProvider{timeoutMs: 500},
		lifecycleProvider,
		&noopTrackerMetrics{},
		service.HeartbeatTrackerConfig{StatusUpdateBufSize: 100},
	)
	defer tracker.Close()

	ctx := context.Background()

	// Register the gateway via telemetry.
	require.NoError(t, tracker.HandleTelemetry(ctx, tenantID, model.TelemetryEnvelope{
		GatewayID:     gwID,
		SensorID:      "s1",
		SensorType:    "temp",
		Timestamp:     clock.Now(),
		KeyVersion:    1,
		EncryptedData: model.OpaqueBlob{Value: "ZQ=="},
		IV:            model.OpaqueBlob{Value: "aQ=="},
		AuthTag:       model.OpaqueBlob{Value: "dA=="},
	}))

	// Advance clock past timeout and tick — lifecycle gate should suppress the alert.
	clock.Advance(600 * time.Millisecond)
	tracker.Tick(ctx)

	// No offline alert must appear on the ALERTS stream.
	_, err = alertSub.NextMsg(500 * time.Millisecond)
	assert.ErrorIs(t, err, nats.ErrTimeout,
		"paused gateway must not trigger an offline alert")

	// No Offline status update must have been dispatched either.
	time.Sleep(200 * time.Millisecond) // let dispatchWorker drain
	for _, u := range statusUpdater.getUpdates() {
		assert.NotEqual(t, model.Offline, u.Status,
			"paused gateway must not produce an Offline status update")
	}
}
