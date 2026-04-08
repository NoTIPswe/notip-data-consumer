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

type stubCacheMetrics struct {
	refreshErrors int
	lastSuccessTs float64
}

func (s *stubCacheMetrics) IncAlertCacheRefreshErrors()         { s.refreshErrors++ }
func (s *stubCacheMetrics) SetAlertCacheLastSuccess(ts float64) { s.lastSuccessTs = ts }

// ─── helpers ──────────────────────────────────────────────────────────────────

func gwID(s string) *string { return &s }

func newCache(fetcher alertConfigFetcher, defaultMs int64) (*AlertConfigCache, *stubCacheMetrics) {
	m := &stubCacheMetrics{}
	c := NewAlertConfigCache(fetcher, m, defaultMs, time.Hour, 3, 1000, 30000)
	return c, m
}

func TestNewAlertConfigCacheSetsBackoffFromMilliseconds(t *testing.T) {
	cache := NewAlertConfigCache(&stubFetcher{}, &stubCacheMetrics{}, 60000, time.Hour, 3, 25, 250)

	assert.Equal(t, 25*time.Millisecond, cache.initialBackoff)
	assert.Equal(t, 250*time.Millisecond, cache.maxBackoff)
}

func TestNewAlertConfigCacheWaitReturnsOnTimer(t *testing.T) {
	cache, _ := newCache(&stubFetcher{}, 60000)

	err := cache.wait(context.Background(), time.Millisecond)

	require.NoError(t, err)
}

func TestNewAlertConfigCacheWaitReturnsOnContextCancel(t *testing.T) {
	cache, _ := newCache(&stubFetcher{}, 60000)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := cache.wait(ctx, time.Second)

	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
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

// ─── Run ──────────────────────────────────────────────────────────────────────

func TestAlertConfigCacheRunPerformsInitialFetch(t *testing.T) {
	fetcher := &stubFetcher{configs: []model.AlertConfig{
		{TenantID: "t1", GatewayID: nil, TimeoutMs: 5000},
	}}
	cache, _ := newCache(fetcher, 60000)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		cache.Run(ctx)
		close(done)
	}()

	// Give Run time to complete the initial fetch.
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	assert.Equal(t, int64(5000), cache.TimeoutFor("t1", "any"),
		"Run must perform an initial fetch before entering the refresh loop")
}

func TestAlertConfigCacheRunRefreshesOnTick(t *testing.T) {
	fetcher := &stubFetcher{configs: []model.AlertConfig{
		{TenantID: "t1", GatewayID: nil, TimeoutMs: 1000},
	}}
	m := &stubCacheMetrics{}
	// Very short refresh interval so the ticker fires quickly.
	cache := NewAlertConfigCache(fetcher, m, 60000, 20*time.Millisecond, 1, 1000, 30000)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		cache.Run(ctx)
		close(done)
	}()

	<-done

	// At 20ms interval in 100ms window the refresh must be called multiple times.
	assert.GreaterOrEqual(t, fetcher.calls, 3,
		"Run must refresh the cache on every tick")
}

func TestAlertConfigCacheRunStopsOnContextCancel(t *testing.T) {
	fetcher := &stubFetcher{configs: nil}
	cache, _ := newCache(fetcher, 60000)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		cache.Run(ctx)
		close(done)
	}()

	cancel()

	select {
	case <-done:
		// Run exited promptly.
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Run did not stop within 200ms after context cancellation")
	}
}

// ─── fetchWithBackoff ─────────────────────────────────────────────────────────

func TestAlertConfigCacheFetchWithBackoffStopsAfterMaxRetries(t *testing.T) {
	fetcher := &stubFetcher{err: errors.New("permanent error")}
	m := &stubCacheMetrics{}
	// maxRetries=2, tiny interval so the test doesn't sleep.
	cache := NewAlertConfigCache(fetcher, m, 60000, time.Hour, 2, 1000, 30000)

	// Override delay by cancelling context immediately after first failure.
	// We use a very short first delay: inject a context that cancels after 10ms.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	err := cache.fetchWithBackoff(ctx)
	require.Error(t, err)
	assert.GreaterOrEqual(t, m.refreshErrors, 1,
		"each failed attempt must increment the error counter")
}

func TestAlertConfigCacheFetchWithBackoffSucceedsOnFirstAttempt(t *testing.T) {
	fetcher := &stubFetcher{configs: []model.AlertConfig{
		{TenantID: "t1", TimeoutMs: 5000},
	}}
	m := &stubCacheMetrics{}
	cache := NewAlertConfigCache(fetcher, m, 60000, time.Hour, 3, 1000, 30000)

	err := cache.fetchWithBackoff(context.Background())

	require.NoError(t, err, "fetchWithBackoff must return nil when the first attempt succeeds")
	assert.Equal(t, int64(5000), cache.TimeoutFor("t1", "any"),
		"a successful fetch must populate the snapshot")
	assert.Equal(t, 0, m.refreshErrors, "no error counter incremented on success")
}

func TestAlertConfigCacheFetchWithBackoffExhaustsRetries(t *testing.T) {
	fetcher := &stubFetcher{err: errors.New("still failing")}
	m := &stubCacheMetrics{}
	cache := NewAlertConfigCache(fetcher, m, 60000, time.Hour, 3, 1000, 30000)
	cache.initialBackoff = time.Millisecond
	cache.maxBackoff = 2 * time.Millisecond
	cache.wait = func(context.Context, time.Duration) error { return nil }

	err := cache.fetchWithBackoff(context.Background())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "exhausted 3 retries")
	assert.Contains(t, err.Error(), "still failing")
	assert.Equal(t, 3, fetcher.calls)
	assert.Equal(t, 3, m.refreshErrors)
}

func TestAlertConfigCacheFetchWithBackoffReturnsWaitError(t *testing.T) {
	fetcher := &stubFetcher{err: errors.New("temporary failure")}
	m := &stubCacheMetrics{}
	cache := NewAlertConfigCache(fetcher, m, 60000, time.Hour, 2, 1000, 30000)
	cache.initialBackoff = time.Millisecond
	cache.wait = func(context.Context, time.Duration) error { return errors.New("wait interrupted") }

	err := cache.fetchWithBackoff(context.Background())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "wait interrupted")
	assert.Equal(t, 1, fetcher.calls)
	assert.Equal(t, 1, m.refreshErrors)
}

func TestAlertConfigCacheRunIncrementsMetricOnRefreshError(t *testing.T) {
	fetcher := &stubFetcher{err: errors.New("refresh error")}
	m := &stubCacheMetrics{}
	cache := NewAlertConfigCache(fetcher, m, 60000, 10*time.Millisecond, 0, 1000, 30000)

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		cache.Run(ctx)
		close(done)
	}()

	<-done

	assert.GreaterOrEqual(t, fetcher.calls, 1)
	assert.GreaterOrEqual(t, m.refreshErrors, 1,
		"Run must increment refresh error metric on failed periodic refresh")
}
