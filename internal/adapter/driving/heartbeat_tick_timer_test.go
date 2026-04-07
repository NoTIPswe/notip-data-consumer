package driving

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// ─── stubs ────────────────────────────────────────────────────────────────────

type stubHeartbeatTicker struct {
	calls atomic.Int32
}

func (s *stubHeartbeatTicker) Tick(_ context.Context) {
	s.calls.Add(1)
}

type stubTickMetrics struct {
	observations atomic.Int32
}

func (s *stubTickMetrics) ObserveHeartbeatTickDuration(_ time.Duration) {
	s.observations.Add(1)
}

// ─── tests ────────────────────────────────────────────────────────────────────

func TestHeartbeatTickTimerCallsTickOnInterval(t *testing.T) {
	stub := &stubHeartbeatTicker{}
	timer := NewHeartbeatTickTimer(stub, &stubTickMetrics{}, 20*time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Millisecond)
	defer cancel()

	timer.Run(ctx) // blocks until ctx expires

	calls := stub.calls.Load()
	assert.GreaterOrEqual(t, calls, int32(2),
		"Tick must be called at least twice within 75ms at 20ms interval")
}

func TestHeartbeatTickTimerStopsOnContextCancel(t *testing.T) {
	stub := &stubHeartbeatTicker{}
	timer := NewHeartbeatTickTimer(stub, &stubTickMetrics{}, time.Hour) // very long interval should never fire

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		timer.Run(ctx)
		close(done)
	}()

	cancel()

	select {
	// if happens in the same moment is pseudocasual (go manage it like that)
	case <-done:
		// Run returned promptly after cancel
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Run did not stop within 100ms after context cancellation")
	}

	assert.Equal(t, int32(0), stub.calls.Load(),
		"Tick must not be called when the ticker never fires")
}
