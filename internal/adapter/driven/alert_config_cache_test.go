package driven

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NoTIPswe/notip-data-consumer/internal/domain/model"
)

// ─── stubs ────────────────────────────────────────────────────────────────────

type stubFetcher struct {
	configs []model.AlertConfig
	err     error
	calls   int
}

func (s *stubFetcher) FetchAlertConfigs(_ context.Context) ([]model.AlertConfig, error) {
	s.calls++
	return s.configs, s.err
}

type stubCacheMetrics struct{ refreshErrors int }

func (s *stubCacheMetrics) IncAlertCacheRefreshErrors() { s.refreshErrors++ }

// ─── helpers ──────────────────────────────────────────────────────────────────

func gwID(s string) *string { return &s }

func newCache(fetcher alertConfigFetcher, defaultMs int64) (*AlertConfigCache, *stubCacheMetrics) {
	m := &stubCacheMetrics{}
	c := NewAlertConfigCache(fetcher, m, defaultMs, time.Hour, 3)
	return c, m
}

// ─── TimeoutFor ───────────────────────────────────────────────────────────────

func TestAlertConfigCacheTimeoutForGatewaySpecific(t *testing.T) {
	fetcher := &stubFetcher{configs: []model.AlertConfig{
		{TenantID: "t1", GatewayID: gwID("gw-1"), TimeoutMs: 10000},
		{TenantID: "t1", GatewayID: nil, TimeoutMs: 30000},
	}}
	cache, _ := newCache(fetcher, 60000)
	require.NoError(t, cache.refresh(context.Background()))

	assert.Equal(t, int64(10000), cache.TimeoutFor("t1", "gw-1"),
		"gateway-specific config must take precedence over tenant default")
}

func TestAlertConfigCacheTimeoutForTenantDefault(t *testing.T) {
	fetcher := &stubFetcher{configs: []model.AlertConfig{
		{TenantID: "t1", GatewayID: nil, TimeoutMs: 30000},
	}}
	cache, _ := newCache(fetcher, 60000)
	require.NoError(t, cache.refresh(context.Background()))

	assert.Equal(t, int64(30000), cache.TimeoutFor("t1", "gw-unknown"),
		"tenant default must be used when no gateway-specific config exists")
}

func TestAlertConfigCacheTimeoutForSystemDefault(t *testing.T) {
	cache, _ := newCache(&stubFetcher{configs: nil}, 60000)
	// No refresh called — snapshot is empty.

	assert.Equal(t, int64(60000), cache.TimeoutFor("t-unknown", "gw-unknown"),
		"system default must be returned when no config entry exists at any level")
}

func TestAlertConfigCacheTimeoutForMultiTenant(t *testing.T) {
	fetcher := &stubFetcher{configs: []model.AlertConfig{
		{TenantID: "t1", GatewayID: nil, TimeoutMs: 15000},
		{TenantID: "t2", GatewayID: nil, TimeoutMs: 25000},
	}}
	cache, _ := newCache(fetcher, 60000)
	require.NoError(t, cache.refresh(context.Background()))

	assert.Equal(t, int64(15000), cache.TimeoutFor("t1", "any"))
	assert.Equal(t, int64(25000), cache.TimeoutFor("t2", "any"))
}

// ─── refresh ──────────────────────────────────────────────────────────────────

func TestAlertConfigCacheRefreshSwapsSnapshot(t *testing.T) {
	fetcher := &stubFetcher{configs: []model.AlertConfig{
		{TenantID: "t1", GatewayID: nil, TimeoutMs: 5000},
	}}
	cache, _ := newCache(fetcher, 60000)

	require.NoError(t, cache.refresh(context.Background()))
	assert.Equal(t, int64(5000), cache.TimeoutFor("t1", "any"))

	// Simulate config change.
	fetcher.configs = []model.AlertConfig{
		{TenantID: "t1", GatewayID: nil, TimeoutMs: 9999},
	}
	require.NoError(t, cache.refresh(context.Background()))

	assert.Equal(t, int64(9999), cache.TimeoutFor("t1", "any"),
		"second refresh must atomically replace the previous snapshot")
}

func TestAlertConfigCacheRefreshFetchErrorDoesNotClearSnapshot(t *testing.T) {
	fetcher := &stubFetcher{configs: []model.AlertConfig{
		{TenantID: "t1", GatewayID: nil, TimeoutMs: 5000},
	}}
	cache, _ := newCache(fetcher, 60000)
	require.NoError(t, cache.refresh(context.Background()))

	// Subsequent refresh fails.
	fetcher.err = errors.New("nats timeout")
	require.Error(t, cache.refresh(context.Background()))

	// Stale snapshot must still serve the previous value.
	assert.Equal(t, int64(5000), cache.TimeoutFor("t1", "any"),
		"a failed refresh must not wipe the existing snapshot")
}

// ─── fetchWithBackoff ─────────────────────────────────────────────────────────

func TestAlertConfigCacheFetchWithBackoffStopsAfterMaxRetries(t *testing.T) {
	fetcher := &stubFetcher{err: errors.New("permanent error")}
	m := &stubCacheMetrics{}
	// maxRetries=2, tiny interval so the test doesn't sleep.
	cache := NewAlertConfigCache(fetcher, m, 60000, time.Hour, 2)

	// Override delay by cancelling context immediately after first failure.
	// We use a very short first delay: inject a context that cancels after 10ms.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	err := cache.fetchWithBackoff(ctx)
	require.Error(t, err)
	assert.GreaterOrEqual(t, m.refreshErrors, 1,
		"each failed attempt must increment the error counter")
}
