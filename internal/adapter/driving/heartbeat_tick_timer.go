package driving

import (
	"context"
	"time"

	"github.com/NoTIPswe/notip-data-consumer/internal/domain/port"
)

// heartbeatTickMetrics is the narrow metric interface for HeartbeatTickTimer.
type heartbeatTickMetrics interface {
	ObserveHeartbeatTickDuration(d time.Duration)
}

// HeartbeatTickTimer owns a time.Ticker and drives port.HeartbeatTicker on each interval.
type HeartbeatTickTimer struct {
	ticker   port.HeartbeatTicker
	metrics  heartbeatTickMetrics
	interval time.Duration
}

// NewHeartbeatTickTimer constructs a HeartbeatTickTimer.
func NewHeartbeatTickTimer(ticker port.HeartbeatTicker, metrics heartbeatTickMetrics, interval time.Duration) *HeartbeatTickTimer {
	return &HeartbeatTickTimer{ticker: ticker, metrics: metrics, interval: interval}
}

func (h *HeartbeatTickTimer) Run(ctx context.Context) {
	t := time.NewTicker(h.interval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			start := time.Now()
			h.ticker.Tick(ctx)
			h.metrics.ObserveHeartbeatTickDuration(time.Since(start))
		}
	}
}
