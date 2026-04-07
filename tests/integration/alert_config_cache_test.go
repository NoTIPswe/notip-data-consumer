//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NoTIPswe/notip-data-consumer/internal/adapter/driven"
	"github.com/NoTIPswe/notip-data-consumer/internal/domain/model"
)

const subjectAlertConfigsList = "internal.mgmt.alert-configs.list"

// noopCacheMetrics satisfies alertCacheMetrics without recording anything.
type noopCacheMetrics struct {
	refreshErrors atomic.Int64
}

func (m *noopCacheMetrics) IncAlertCacheRefreshErrors() { m.refreshErrors.Add(1) }

// TestAlertConfigCacheIntegrationInitialFetchPopulatesCache starts the cache
// with a mock responder and verifies that TimeoutFor returns the fetched value
// rather than the default.
func TestAlertConfigCacheIntegrationInitialFetchPopulatesCache(t *testing.T) {
	nc := connectNATSWithMTLS(t)
	rrClient := driven.NewNATSRRClient(nc, 5*time.Second)

	const tenantCache = "tenant-cache"

	gwID := "gw-cached"
	mockConfigs := []model.AlertConfig{
		{TenantID: tenantCache, GatewayID: &gwID, TimeoutMs: 15000},
		{TenantID: tenantCache, GatewayID: nil, TimeoutMs: 45000},
	}

	sub, err := nc.Subscribe(subjectAlertConfigsList, func(msg *nats.Msg) {
		data, _ := json.Marshal(mockConfigs)
		_ = msg.Respond(data)
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = sub.Unsubscribe() })

	cache := driven.NewAlertConfigCache(
		rrClient,
		&noopCacheMetrics{},
		99999, // default — should NOT be returned when config exists
		1*time.Hour,
		3,
		1000,
		30000,
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go cache.Run(ctx)

	// Poll until the cache is populated (initial fetch completes).
	require.Eventually(t, func() bool {
		return cache.TimeoutFor(tenantCache, "gw-cached") == 15000
	}, 10*time.Second, 50*time.Millisecond, "gateway-specific timeout never appeared in cache")

	// Tenant-level fallback.
	assert.Equal(t, int64(45000), cache.TimeoutFor(tenantCache, "gw-unknown"))

	// Unknown tenant falls back to system default.
	assert.Equal(t, int64(99999), cache.TimeoutFor("unknown-tenant", "gw-x"))
}

// TestAlertConfigCacheIntegrationRefreshUpdatesCache verifies that the periodic
// refresh picks up changes from the Management API.
func TestAlertConfigCacheIntegrationRefreshUpdatesCache(t *testing.T) {
	nc := connectNATSWithMTLS(t)
	rrClient := driven.NewNATSRRClient(nc, 5*time.Second)

	const tenantRefresh = "tenant-refresh"

	var version atomic.Int32
	version.Store(1)

	sub, err := nc.Subscribe(subjectAlertConfigsList, func(msg *nats.Msg) {
		var configs []model.AlertConfig
		if version.Load() == 1 {
			configs = []model.AlertConfig{
				{TenantID: tenantRefresh, GatewayID: nil, TimeoutMs: 30000},
			}
		} else {
			configs = []model.AlertConfig{
				{TenantID: tenantRefresh, GatewayID: nil, TimeoutMs: 90000},
			}
		}
		data, _ := json.Marshal(configs)
		_ = msg.Respond(data)
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = sub.Unsubscribe() })

	// Short refresh interval to speed up the test.
	cache := driven.NewAlertConfigCache(
		rrClient,
		&noopCacheMetrics{},
		99999,
		200*time.Millisecond, // refresh every 200ms
		3,
		1000,
		30000,
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go cache.Run(ctx)

	// Wait for initial fetch.
	require.Eventually(t, func() bool {
		return cache.TimeoutFor(tenantRefresh, "any") == 30000
	}, 10*time.Second, 50*time.Millisecond, "initial fetch never completed")

	// Bump version so the next refresh returns the updated config.
	version.Store(2)

	// Wait for the refresh cycle to pick up the new value.
	require.Eventually(t, func() bool {
		return cache.TimeoutFor(tenantRefresh, "any") == 90000
	}, 10*time.Second, 50*time.Millisecond, "cache never refreshed to new timeout value")
}

// TestAlertConfigCacheIntegrationFallbackToDefaultOnNoResponder verifies that
// when no responder is available, the cache falls back to the system default
// and increments the error metric.
func TestAlertConfigCacheIntegrationFallbackToDefaultOnNoResponder(t *testing.T) {
	nc := connectNATSWithMTLS(t)
	// Very short timeout so retries complete quickly.
	rrClient := driven.NewNATSRRClient(nc, 200*time.Millisecond)

	m := &noopCacheMetrics{}
	cache := driven.NewAlertConfigCache(
		rrClient,
		m,
		77777,
		1*time.Hour,
		2, // only 2 retries to keep the test fast
		1000,
		30000,
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go cache.Run(ctx)

	// Each FetchAlertConfigs call retries 3x inside NATSRRClient (200ms × 4 attempts + 1s+2s+4s backoff ≈ 7.8s).
	// fetchWithBackoff runs 2 attempts, so we need up to ~20s for errors to accumulate.
	require.Eventually(t, func() bool {
		return m.refreshErrors.Load() > 0
	}, 25*time.Second, 200*time.Millisecond, "expected at least one refresh error")

	// Should still return the default since no responder ever answered.
	assert.Equal(t, int64(77777), cache.TimeoutFor("any-tenant", "any-gw"))
}
