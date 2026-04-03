package service

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/NoTIPswe/notip-data-consumer/internal/domain/model"
	"github.com/NoTIPswe/notip-data-consumer/internal/domain/port"
)

// HeartbeatTrackerMetrics is the subset of metrics emitted by HeartbeatTracker.
// Using a narrow interface keeps the tracker decoupled from the full Metrics struct.
type HeartbeatTrackerMetrics interface {
	IncStatusUpdateDropped()
	SetHeartbeatMapSize(v float64)
	SetDispatchQueueLength(v float64)
}

// gatewayKey is the composite map key.
type gatewayKey struct {
	tenantID  string
	gatewayID string
}

// heartbeatEntry is the internal per-gateway liveness record.
type heartbeatEntry struct {
	tenantID    string
	gatewayID   string
	lastSeen    time.Time
	knownStatus model.GatewayStatus
}

// statusUpdateJob is queued for the background dispatch worker.
type statusUpdateJob struct {
	update model.GatewayStatusUpdate
}

// HeartbeatTracker implements TelemetryMessageHandler, DecommissionEventHandler,
// and HeartbeatTicker. It is the core domain service of this application.
// Telemetry persistence is the responsibility of the driving adapter, not this service.
type HeartbeatTracker struct {
	clock             port.ClockProvider
	alertPublisher    port.AlertPublisher
	statusUpdater     port.GatewayStatusUpdater
	configProvider    port.AlertConfigProvider
	lifecycleProvider port.GatewayLifecycleProvider
	metrics           HeartbeatTrackerMetrics

	startTime   time.Time
	gracePeriod time.Duration

	mu    sync.RWMutex
	beats map[gatewayKey]*heartbeatEntry

	dispatchCh chan statusUpdateJob
	done       chan struct{}
	closeOnce  sync.Once
}

// HeartbeatTrackerConfig holds the scalar configuration values for HeartbeatTracker.
type HeartbeatTrackerConfig struct {
	// StatusUpdateBufSize is the capacity of the async status-update dispatch channel.
	StatusUpdateBufSize int
	// GracePeriod suppresses offline transitions for this duration after startup,
	// preventing false alerts while the service collects initial heartbeats.
	GracePeriod time.Duration
}

func NewHeartbeatTracker(
	clock port.ClockProvider,
	alertPublisher port.AlertPublisher,
	statusUpdater port.GatewayStatusUpdater,
	configProvider port.AlertConfigProvider,
	lifecycleProvider port.GatewayLifecycleProvider,
	metrics HeartbeatTrackerMetrics,
	cfg HeartbeatTrackerConfig,
) *HeartbeatTracker {
	t := &HeartbeatTracker{
		clock:             clock,
		alertPublisher:    alertPublisher,
		statusUpdater:     statusUpdater,
		configProvider:    configProvider,
		lifecycleProvider: lifecycleProvider,
		metrics:           metrics,
		startTime:         clock.Now(),
		gracePeriod:       cfg.GracePeriod,
		beats:             make(map[gatewayKey]*heartbeatEntry),
		dispatchCh:        make(chan statusUpdateJob, cfg.StatusUpdateBufSize),
		done:              make(chan struct{}),
	}
	go t.dispatchWorker()
	return t
}

// Close drains the dispatch channel and waits for the background goroutine to exit.
// Safe to call multiple times — only the first call has effect.
func (t *HeartbeatTracker) Close() {
	t.closeOnce.Do(func() {
		close(t.dispatchCh)
		<-t.done
	})
}

// dispatchWorker runs in a background goroutine and serialises all UpdateStatus
// RR calls, keeping Tick and HandleTelemetry non-blocking.
// Errors are counted by the NATSGatewayStatusUpdater adapter before bubbling up.
func (t *HeartbeatTracker) dispatchWorker() {
	defer close(t.done)
	for job := range t.dispatchCh {
		t.metrics.SetDispatchQueueLength(float64(len(t.dispatchCh)))
		_ = t.statusUpdater.UpdateStatus(context.Background(), job.update)
	}
}

// HandleTelemetry updates heartbeat state for the gateway.
// On first-seen or recovery (Offline→Online), a status update is dispatched asynchronously.
// Telemetry persistence is handled by the calling adapter before invoking this method.
func (t *HeartbeatTracker) HandleTelemetry(ctx context.Context, tenantID string, env model.TelemetryEnvelope) error {
	now := t.clock.Now()
	key := gatewayKey{tenantID, env.GatewayID}

	t.mu.Lock()
	defer t.mu.Unlock()

	entry, exists := t.beats[key]
	if !exists {
		t.beats[key] = &heartbeatEntry{
			tenantID:    tenantID,
			gatewayID:   env.GatewayID,
			lastSeen:    now,
			knownStatus: model.Online,
		}
		t.metrics.SetHeartbeatMapSize(float64(len(t.beats)))
		t.dispatchStatusUpdate(model.GatewayStatusUpdate{
			GatewayID:  env.GatewayID,
			Status:     model.Online,
			LastSeenAt: now,
		})
		return nil
	}

	entry.lastSeen = now

	if entry.knownStatus == model.Offline {
		entry.knownStatus = model.Online
		t.dispatchStatusUpdate(model.GatewayStatusUpdate{
			GatewayID:  env.GatewayID,
			Status:     model.Online,
			LastSeenAt: now,
		})
	}

	return nil
}

// HandleDecommission removes a gateway from the heartbeat map.
// Subsequent Tick calls will ignore this gateway.
func (t *HeartbeatTracker) HandleDecommission(tenantID, gatewayID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.beats, gatewayKey{tenantID, gatewayID})
	t.metrics.SetHeartbeatMapSize(float64(len(t.beats)))
}

// Tick checks all tracked gateways for liveness
// Uses a two-phase approach: snapshot under RLock to minimise contention, then
// performs I/O outside the lock, and re-acquires WLock only to update state.
func (t *HeartbeatTracker) Tick(ctx context.Context) {
	// no-op during grace period
	if t.clock.Now().Before(t.startTime.Add(t.gracePeriod)) {
		return
	}

	now := t.clock.Now()

	// Phase 1 — snapshot under read lock.
	t.mu.RLock()
	snapshot := make([]heartbeatEntry, 0, len(t.beats))
	for _, entry := range t.beats {
		snapshot = append(snapshot, *entry) // value copy: safe to use outside the lock
	}
	t.mu.RUnlock()

	// Phase 2 — evaluate and perform I/O outside the lock.
	for i := range snapshot {
		entry := &snapshot[i]

		if entry.knownStatus == model.Offline {
			continue
		}

		timeoutMs := t.configProvider.TimeoutFor(entry.tenantID, entry.gatewayID)
		deadline := entry.lastSeen.Add(time.Duration(timeoutMs) * time.Millisecond)

		if now.Before(deadline) {
			continue
		}

		// Lifecycle gate: query management API before acting on the timeout.
		// Query only on timeout candidate to minimise RR load.
		state, err := t.lifecycleProvider.GetGatewayLifecycle(ctx, entry.tenantID, entry.gatewayID)
		if err != nil {
			// Fail-open: proceed with alert to avoid missing real offline events.
			// The adapter has already incremented notip_consumer_lifecycle_query_errors_total.
			// Monitor that counter for sustained failures indicating management-api is unreachable.
			slog.Warn("lifecycle query failed, proceeding with alert (fail-open)",
				"tenantID", entry.tenantID,
				"gatewayID", entry.gatewayID,
				"err", err,
			)
		} else if state == model.LifecyclePaused {
			// Gateway is intentionally paused — silence alert and offline status update.
			continue
		}

		_ = t.alertPublisher.Publish(ctx, entry.tenantID, model.AlertPayload{
			GatewayID: entry.gatewayID,
			LastSeen:  entry.lastSeen,
			TimeoutMs: timeoutMs,
			Timestamp: now,
		})

		t.dispatchStatusUpdate(model.GatewayStatusUpdate{
			GatewayID:  entry.gatewayID,
			Status:     model.Offline,
			LastSeenAt: entry.lastSeen,
		})

		// Phase 3 — write lock to update state.
		// Re-validate lastSeen: if a new telemetry arrived between the snapshot and now,
		// the gateway recovered and must NOT be marked offline.
		t.mu.Lock()
		if real, exists := t.beats[gatewayKey{entry.tenantID, entry.gatewayID}]; exists &&
			!real.lastSeen.After(entry.lastSeen) {
			real.knownStatus = model.Offline
		}
		t.mu.Unlock()
	}
}

// dispatchStatusUpdate sends a status update to the worker channel without blocking.
// If the channel is full the update is dropped and the dropped counter is incremented.
func (t *HeartbeatTracker) dispatchStatusUpdate(update model.GatewayStatusUpdate) {
	select {
	case t.dispatchCh <- statusUpdateJob{update: update}:
	default:
		t.metrics.IncStatusUpdateDropped()
	}
}
