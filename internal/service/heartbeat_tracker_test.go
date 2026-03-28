package service_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/NoTIPswe/notip-data-consumer/internal/domain/model"
	"github.com/NoTIPswe/notip-data-consumer/internal/service"
)

// mock clock, important to safely simulate time.
type mockClock struct{ now time.Time }

func (m *mockClock) Now() time.Time { return m.now }

// mock alert publisher.
type mockAlertPublisher struct{ mock.Mock }

func (m *mockAlertPublisher) Publish(ctx context.Context, tenantID string, payload model.AlertPayload) error {
	return m.Called(ctx, tenantID, payload).Error(0)
}

// mock status updater.
type mockStatusUpdater struct{ mock.Mock }

func (m *mockStatusUpdater) UpdateStatus(ctx context.Context, update model.GatewayStatusUpdate) error {
	return m.Called(ctx, update).Error(0)
}

// mock config provider.
type mockConfigProvider struct{ timeoutMs int64 }

func (m *mockConfigProvider) TimeoutFor(_, _ string) int64 { return m.timeoutMs }

// mock metrics — race-safe.
type mockMetrics struct {
	mu                  sync.Mutex
	statusUpdateDropped int
	heartbeatMapSize    float64
}

func (m *mockMetrics) IncStatusUpdateDropped() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.statusUpdateDropped++
}

func (m *mockMetrics) SetHeartbeatMapSize(v float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.heartbeatMapSize = v
}

func (m *mockMetrics) dropped() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.statusUpdateDropped
}

func (m *mockMetrics) mapSize() float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.heartbeatMapSize
}

// ─── helpers ──────────────────────────────────────────────────────────────────

const (
	tenantID  = "tenant-1"
	gatewayID = "gw-abc"
)

var epoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func envelope(gwID string) model.TelemetryEnvelope {
	return model.TelemetryEnvelope{GatewayID: gwID}
}

// syncRun returns a testify Run callback that signals wg exactly once when the mock is entered.
func syncRun(wg *sync.WaitGroup) func(mock.Arguments) {
	return func(_ mock.Arguments) { wg.Done() }
}

// newTracker builds a tracker with gracePeriod=0 and registers t.Cleanup(tr.Close).
func newTracker(
	t *testing.T,
	clk *mockClock,
	publisher *mockAlertPublisher,
	updater *mockStatusUpdater,
	timeoutMs int64,
	bufSize int,
) (*service.HeartbeatTracker, *mockMetrics) {
	t.Helper()
	m := &mockMetrics{}
	tr := service.NewHeartbeatTracker(
		clk, publisher, updater,
		&mockConfigProvider{timeoutMs},
		m,
		bufSize,
		0, // gracePeriod=0: Tick runs immediately from startup
	)
	t.Cleanup(tr.Close)
	return tr, m
}

// ─── HandleTelemetry ──────────────────────────────────────────────────────────

func TestHandleTelemetry(t *testing.T) {
	t.Run("new gateway becomes online and dispatches status update", func(t *testing.T) {
		clk := &mockClock{now: epoch}
		publisher := &mockAlertPublisher{}
		updater := &mockStatusUpdater{}

		var wg sync.WaitGroup
		wg.Add(1)
		updater.On("UpdateStatus", mock.Anything, mock.MatchedBy(func(u model.GatewayStatusUpdate) bool {
			return u.GatewayID == gatewayID && u.Status == model.Online
		})).Run(syncRun(&wg)).Return(nil).Once()

		tr, metrics := newTracker(t, clk, publisher, updater, 60000, 10)

		err := tr.HandleTelemetry(context.Background(), tenantID, envelope(gatewayID))
		assert.NoError(t, err)
		wg.Wait()

		updater.AssertExpectations(t)
		publisher.AssertNotCalled(t, "Publish") // didn't call the alert publisher
		assert.Equal(t, float64(1), metrics.mapSize())
	})

	t.Run("existing online gateway does not produce a second status update", func(t *testing.T) {
		clk := &mockClock{now: epoch}
		publisher := &mockAlertPublisher{}
		updater := &mockStatusUpdater{}

		var wg sync.WaitGroup
		wg.Add(1)
		updater.On("UpdateStatus", mock.Anything, mock.Anything).
			Run(syncRun(&wg)).Return(nil).Once()

		tr, _ := newTracker(t, clk, publisher, updater, 60000, 10)

		_ = tr.HandleTelemetry(context.Background(), tenantID, envelope(gatewayID))
		wg.Wait()

		clk.now = epoch.Add(5 * time.Second)
		_ = tr.HandleTelemetry(context.Background(), tenantID, envelope(gatewayID))
		tr.Close() // drain before asserting call counts

		updater.AssertNumberOfCalls(t, "UpdateStatus", 1)
		publisher.AssertNotCalled(t, "Publish")
	})

	t.Run("offline gateway recovery dispatches online status update", func(t *testing.T) {
		clk := &mockClock{now: epoch}
		publisher := &mockAlertPublisher{}
		updater := &mockStatusUpdater{}

		var wg sync.WaitGroup

		wg.Add(1)
		updater.On("UpdateStatus", mock.Anything, mock.MatchedBy(func(u model.GatewayStatusUpdate) bool {
			return u.Status == model.Online
		})).Run(syncRun(&wg)).Return(nil).Once()

		tr, _ := newTracker(t, clk, publisher, updater, 60000, 10)
		_ = tr.HandleTelemetry(context.Background(), tenantID, envelope(gatewayID))
		wg.Wait()

		// Force offline via Tick.
		publisher.On("Publish", mock.Anything, tenantID, mock.MatchedBy(func(p model.AlertPayload) bool {
			return p.GatewayID == gatewayID
		})).Return(nil).Once()
		wg.Add(1)
		updater.On("UpdateStatus", mock.Anything, mock.MatchedBy(func(u model.GatewayStatusUpdate) bool {
			return u.Status == model.Offline
		})).Run(syncRun(&wg)).Return(nil).Once()

		clk.now = epoch.Add(2 * time.Minute)
		tr.Tick(context.Background())
		wg.Wait()

		// Recovery.
		wg.Add(1)
		updater.On("UpdateStatus", mock.Anything, mock.MatchedBy(func(u model.GatewayStatusUpdate) bool {
			return u.Status == model.Online
		})).Run(syncRun(&wg)).Return(nil).Once()

		_ = tr.HandleTelemetry(context.Background(), tenantID, envelope(gatewayID))
		wg.Wait()

		updater.AssertExpectations(t)
		publisher.AssertExpectations(t)
	})
}

// ─── HandleDecommission ───────────────────────────────────────────────────────

func TestHandleDecommission(t *testing.T) {
	t.Run("known gateway is removed from the map", func(t *testing.T) {
		clk := &mockClock{now: epoch}
		publisher := &mockAlertPublisher{}
		updater := &mockStatusUpdater{}

		var wg sync.WaitGroup
		wg.Add(1)
		updater.On("UpdateStatus", mock.Anything, mock.Anything).Run(syncRun(&wg)).Return(nil).Once()

		tr, metrics := newTracker(t, clk, publisher, updater, 60000, 10)

		_ = tr.HandleTelemetry(context.Background(), tenantID, envelope(gatewayID))
		wg.Wait()
		assert.Equal(t, float64(1), metrics.mapSize())

		tr.HandleDecommission(tenantID, gatewayID)
		assert.Equal(t, float64(0), metrics.mapSize())

		// Tick past timeout — decommissioned gateway must never trigger an alert.
		clk.now = epoch.Add(2 * time.Minute)
		tr.Tick(context.Background())
		tr.Close()

		publisher.AssertNotCalled(t, "Publish")
	})

	t.Run("unknown gateway is a no-op", func(t *testing.T) {
		clk := &mockClock{now: epoch}
		tr, _ := newTracker(t, clk, &mockAlertPublisher{}, &mockStatusUpdater{}, 60000, 10)

		assert.NotPanics(t, func() {
			tr.HandleDecommission(tenantID, "nonexistent-gw")
		})
	})
}

// ─── Tick ─────────────────────────────────────────────────────────────────────

func TestTick(t *testing.T) {
	t.Run("grace period suppresses offline transitions", func(t *testing.T) {
		clk := &mockClock{now: epoch}
		publisher := &mockAlertPublisher{}
		updater := &mockStatusUpdater{}

		var wg sync.WaitGroup
		wg.Add(1)
		updater.On("UpdateStatus", mock.Anything, mock.Anything).Run(syncRun(&wg)).Return(nil).Once()

		m := &mockMetrics{}
		tr := service.NewHeartbeatTracker(
			clk, publisher, updater, &mockConfigProvider{60000}, m, 10,
			5*time.Minute,
		)
		t.Cleanup(tr.Close)

		_ = tr.HandleTelemetry(context.Background(), tenantID, envelope(gatewayID))
		wg.Wait()

		// Advance past the timeout but still within the 5-minute grace period.
		clk.now = epoch.Add(2 * time.Minute)
		tr.Tick(context.Background())
		tr.Close()

		publisher.AssertNotCalled(t, "Publish")
	})

	t.Run("gateway within timeout produces no transition", func(t *testing.T) {
		clk := &mockClock{now: epoch}
		publisher := &mockAlertPublisher{}
		updater := &mockStatusUpdater{}

		var wg sync.WaitGroup
		wg.Add(1)
		updater.On("UpdateStatus", mock.Anything, mock.Anything).Run(syncRun(&wg)).Return(nil).Once()

		tr, _ := newTracker(t, clk, publisher, updater, 60000, 10)

		_ = tr.HandleTelemetry(context.Background(), tenantID, envelope(gatewayID))
		wg.Wait()

		clk.now = epoch.Add(10 * time.Second)
		tr.Tick(context.Background())
		tr.Close()

		publisher.AssertNotCalled(t, "Publish")
		updater.AssertNumberOfCalls(t, "UpdateStatus", 1)
	})

	t.Run("gateway exceeding timeout becomes offline", func(t *testing.T) {
		clk := &mockClock{now: epoch}
		publisher := &mockAlertPublisher{}
		updater := &mockStatusUpdater{}

		var wg sync.WaitGroup

		wg.Add(1)
		updater.On("UpdateStatus", mock.Anything, mock.MatchedBy(func(u model.GatewayStatusUpdate) bool {
			return u.Status == model.Online
		})).Run(syncRun(&wg)).Return(nil).Once()

		tr, _ := newTracker(t, clk, publisher, updater, 60000, 10)
		_ = tr.HandleTelemetry(context.Background(), tenantID, envelope(gatewayID))
		wg.Wait()

		publisher.On("Publish", mock.Anything, tenantID, mock.MatchedBy(func(p model.AlertPayload) bool {
			return p.GatewayID == gatewayID && p.TimeoutMs == int64(60000)
		})).Return(nil).Once()
		wg.Add(1)
		updater.On("UpdateStatus", mock.Anything, mock.MatchedBy(func(u model.GatewayStatusUpdate) bool {
			return u.GatewayID == gatewayID && u.Status == model.Offline
		})).Run(syncRun(&wg)).Return(nil).Once()

		clk.now = epoch.Add(2 * time.Minute)
		tr.Tick(context.Background())
		wg.Wait()

		publisher.AssertExpectations(t)
		updater.AssertExpectations(t)
	})

	t.Run("already offline gateway does not produce a repeat alert", func(t *testing.T) {
		clk := &mockClock{now: epoch}
		publisher := &mockAlertPublisher{}
		updater := &mockStatusUpdater{}

		var wg sync.WaitGroup

		wg.Add(1)
		updater.On("UpdateStatus", mock.Anything, mock.MatchedBy(func(u model.GatewayStatusUpdate) bool {
			return u.Status == model.Online
		})).Run(syncRun(&wg)).Return(nil).Once()

		tr, _ := newTracker(t, clk, publisher, updater, 60000, 10)
		_ = tr.HandleTelemetry(context.Background(), tenantID, envelope(gatewayID))
		wg.Wait()

		publisher.On("Publish", mock.Anything, tenantID, mock.Anything).Return(nil).Once()
		wg.Add(1)
		updater.On("UpdateStatus", mock.Anything, mock.MatchedBy(func(u model.GatewayStatusUpdate) bool {
			return u.Status == model.Offline
		})).Run(syncRun(&wg)).Return(nil).Once()

		clk.now = epoch.Add(2 * time.Minute)
		tr.Tick(context.Background())
		wg.Wait()

		clk.now = epoch.Add(3 * time.Minute)
		tr.Tick(context.Background())
		tr.Close()

		publisher.AssertNumberOfCalls(t, "Publish", 1)
	})

	t.Run("full dispatch buffer drops update and increments counter", func(t *testing.T) {
		clk := &mockClock{now: epoch}
		publisher := &mockAlertPublisher{}
		updater := &mockStatusUpdater{}

		entered := make(chan struct{}, 1)
		unblock := make(chan struct{})

		updater.On("UpdateStatus", mock.Anything, mock.Anything).
			Run(func(_ mock.Arguments) {
				select {
				case entered <- struct{}{}:
				default:
				}
				<-unblock
			}).Return(nil).Maybe()
		publisher.On("Publish", mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()

		metrics := &mockMetrics{}
		tr := service.NewHeartbeatTracker(
			clk, publisher, updater, &mockConfigProvider{60000}, metrics, 1, 0,
		)
		t.Cleanup(func() { close(unblock); tr.Close() })

		// Item 1: gw-1 → Online → into buffer; goroutine picks it up and blocks.
		_ = tr.HandleTelemetry(context.Background(), tenantID, envelope("gw-1"))
		<-entered // goroutine is blocking inside UpdateStatus; buffer is empty

		// Item 2: gw-2 → Online → fills the buffer (1/1).
		_ = tr.HandleTelemetry(context.Background(), tenantID, envelope("gw-2"))

		// Item 3: gw-3 → Online → buffer full → DROPS → counter incremented.
		_ = tr.HandleTelemetry(context.Background(), tenantID, envelope("gw-3"))

		assert.Equal(t, 1, metrics.dropped())
	})
}
