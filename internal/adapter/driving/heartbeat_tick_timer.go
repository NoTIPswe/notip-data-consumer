package driving

import (
	"context"
	"time"

	"github.com/NoTIPswe/notip-data-consumer/internal/domain/port"
)

// owns a time.Tiker and drives port.HeartbeatTicker on each interval.
type HeartbeatTickTimer struct {
	ticker   port.HeartbeatTicker
	interval time.Duration
}

// constructs the heartbeatticker.
func NewHeartbeatTickTimer(ticker port.HeartbeatTicker, interval time.Duration) *HeartbeatTickTimer {
	return &HeartbeatTickTimer{ticker: ticker, interval: interval}
}

func (h *HeartbeatTickTimer) Run(ctx context.Context) {
	t := time.NewTicker(h.interval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			h.ticker.Tick(ctx) // calls the tick
		}
	}
}
