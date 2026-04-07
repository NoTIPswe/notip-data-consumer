package driven

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/NoTIPswe/notip-data-consumer/internal/domain/model"
)

// alertConfigFetcher is the subset of NATSRRClient used by AlertConfigCache.
type alertConfigFetcher interface {
	FetchAlertConfigs(ctx context.Context) ([]model.AlertConfig, error)
}

// alertCacheErrRecorder is the narrow metric interface for AlertConfigCache.
type alertCacheErrRecorder interface {
	IncAlertCacheRefreshErrors()
}

// alertConfigSnapshot is an immutable point-in-time copy of all alert configs.
// Replaced atomically on each successful refresh; never mutated in place.
type alertConfigSnapshot struct {
	byGateway map[string]model.AlertConfig // key: "tenantID/gatewayID"
	byTenant  map[string]model.AlertConfig // key: tenantID
	fetchedAt time.Time
}

// AlertConfigCache implements port.AlertConfigProvider.
// Maintains a periodically-refreshed, atomically-swapped in-memory snapshot of all
// alert configurations fetched from the Management API via NATS RR.
// Reads are always lock-free via atomic.Pointer.
type AlertConfigCache struct {
	snapshot         atomic.Pointer[alertConfigSnapshot]
	rrClient         alertConfigFetcher
	metrics          alertCacheErrRecorder
	logger           *slog.Logger
	defaultTimeoutMs int64
	refreshInterval  time.Duration
	maxRetries       int
	initialBackoff   time.Duration
	maxBackoff       time.Duration
	wait             func(ctx context.Context, d time.Duration) error
}

// NewAlertConfigCache constructs an AlertConfigCache. Run must be called in a goroutine
// to start the initial fetch and the periodic refresh loop.
func NewAlertConfigCache(
	rrClient alertConfigFetcher,
	metrics alertCacheErrRecorder,
	defaultTimeoutMs int64,
	refreshInterval time.Duration,
	maxRetries int,
	initialBackoffMs int,
	maxBackoffMs int,
) *AlertConfigCache {
	c := &AlertConfigCache{
		rrClient:         rrClient,
		metrics:          metrics,
		logger:           slog.Default(),
		defaultTimeoutMs: defaultTimeoutMs,
		refreshInterval:  refreshInterval,
		maxRetries:       maxRetries,
		initialBackoff:   time.Duration(initialBackoffMs) * time.Millisecond,
		maxBackoff:       time.Duration(maxBackoffMs) * time.Millisecond,
		wait: func(ctx context.Context, d time.Duration) error {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-t.C:
				return nil
			}
		},
	}
	// Initialise with an empty snapshot so TimeoutFor never reads a nil pointer.
	c.snapshot.Store(&alertConfigSnapshot{
		byGateway: make(map[string]model.AlertConfig),
		byTenant:  make(map[string]model.AlertConfig),
	})
	return c
}

// TimeoutFor returns the configured offline timeout for a specific gateway.
// Lock-free: reads from atomic snapshot pointer.
func (c *AlertConfigCache) TimeoutFor(tenantID, gatewayID string) int64 {
	snap := c.snapshot.Load()

	if cfg, ok := snap.byGateway[tenantID+"/"+gatewayID]; ok {
		return cfg.TimeoutMs
	}
	if cfg, ok := snap.byTenant[tenantID]; ok {
		return cfg.TimeoutMs
	}
	return c.defaultTimeoutMs
}

// Run performs an initial fetch with retries, then runs the periodic refresh loop.
// Blocks until ctx is cancelled. Intended to be called in a goroutine.
// If the initial fetch fails after all retries, the cache starts with the empty
// snapshot and TimeoutFor will return the system default until the next refresh.
func (c *AlertConfigCache) Run(ctx context.Context) {
	// Non-fatal: service still starts using defaults; error already counted inside fetchWithBackoff.
	_ = c.fetchWithBackoff(ctx)

	ticker := time.NewTicker(c.refreshInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := c.refresh(ctx); err != nil {
				c.metrics.IncAlertCacheRefreshErrors()
				c.logger.Error("alert config cache refresh failed", "err", err)
			} else {
				gw, t := c.snapshotCounts()
				c.logger.Info("alert config cache refreshed", "gateway_configs", gw, "tenant_configs", t)
			}
		}
	}
}

// refresh fetches a fresh config snapshot and atomically swaps it in.
func (c *AlertConfigCache) refresh(ctx context.Context) error {
	configs, err := c.rrClient.FetchAlertConfigs(ctx)
	if err != nil {
		return fmt.Errorf("fetch alert configs: %w", err)
	}

	snap := &alertConfigSnapshot{
		byGateway: make(map[string]model.AlertConfig, len(configs)),
		byTenant:  make(map[string]model.AlertConfig),
		fetchedAt: time.Now(),
	}

	for _, cfg := range configs {
		if cfg.GatewayID != nil {
			snap.byGateway[cfg.TenantID+"/"+*cfg.GatewayID] = cfg
		} else {
			snap.byTenant[cfg.TenantID] = cfg
		}
	}

	c.snapshot.Store(snap) // atomic swap
	return nil
}

// snapshotCounts returns the current gateway and tenant config counts for logging.
func (c *AlertConfigCache) snapshotCounts() (int, int) {
	snap := c.snapshot.Load()
	return len(snap.byGateway), len(snap.byTenant)
}

// fetchWithBackoff retries up to maxRetries times with exponential backoff.
// Increments the error metric on each failed attempt.
func (c *AlertConfigCache) fetchWithBackoff(ctx context.Context) error {
	delay := c.initialBackoff
	var lastErr error

	for attempt := 0; attempt < c.maxRetries; attempt++ {
		if err := c.refresh(ctx); err == nil {
			gw, t := c.snapshotCounts()
			c.logger.Info("alert config cache loaded",
				"attempt", attempt+1,
				"gateway_configs", gw,
				"tenant_configs", t,
			)
			return nil
		} else {
			lastErr = err
			c.metrics.IncAlertCacheRefreshErrors()
			c.logger.Error("alert config cache initial fetch failed", "attempt", attempt+1, "err", err)
		}

		if err := c.wait(ctx, delay); err != nil {
			return err
		}

		delay *= 2
		if delay > c.maxBackoff {
			delay = c.maxBackoff
		}
	}

	return fmt.Errorf("exhausted %d retries: %w", c.maxRetries, lastErr)
}
