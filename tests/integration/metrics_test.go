//go:build integration

package integration

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NoTIPswe/notip-data-consumer/internal/metrics"
)

// allExpectedMetricNames is the canonical list of metric names the data consumer
// must expose. Any addition to metrics.go must also be added here to stay in sync.
var allExpectedMetricNames = []string{
	"notip_consumer_messages_received_total",
	"notip_consumer_message_parsing_errors_total",
	"notip_consumer_messages_written_total",
	"notip_consumer_write_errors_total",
	"notip_consumer_write_duration_seconds",
	"notip_consumer_batch_size",
	"notip_consumer_alerts_published_total",
	"notip_consumer_alert_publish_errors_total",
	"notip_consumer_heartbeat_map_size",
	"notip_consumer_heartbeat_tick_duration_seconds",
	"notip_consumer_status_update_errors_total",
	"notip_consumer_status_update_dropped_total",
	"notip_consumer_dispatch_queue_length",
	"notip_consumer_alert_cache_refresh_errors_total",
	"notip_consumer_alert_cache_last_success_timestamp",
	"notip_consumer_nats_reconnects_total",
	"notip_consumer_lifecycle_query_errors_total",
}

const endpoint = "/metrics"

// TestPrometheusMetricsScrapeEndpointExposesAllMetrics verifies that the Prometheus
// HTTP handler built from a real registry serves valid text-format output containing
// every expected metric name. This catches regressions where a metric is registered
// in metrics.go but the HTTP path is broken, or a metric name is typo'd.
func TestPrometheusMetricsScrapeEndpointExposesAllMetrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := metrics.New(reg)

	// Exercise every method once so all series appear in the output (Prometheus
	// only outputs series that have been observed at least once for counters/gauges).
	m.IncMessagesReceived()
	m.IncMessageParsingErrors()
	m.IncMessagesWritten()
	m.IncWriteErrors()
	m.ObserveWriteLatency(10 * time.Millisecond)
	m.ObserveBatchSize(50)
	m.IncAlertsPublished()
	m.IncAlertPublishErrors()
	m.SetHeartbeatMapSize(3)
	m.ObserveHeartbeatTickDuration(5 * time.Millisecond)
	m.IncStatusUpdateErrors()
	m.IncStatusUpdateDropped()
	m.SetDispatchQueueLength(2)
	m.IncAlertCacheRefreshErrors()
	m.SetAlertCacheLastSuccess(1711305600)
	m.IncNATSReconnects()
	m.IncLifecycleQueryErrors()

	srv := httptest.NewServer(promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + endpoint)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "text/plain; version=0.0.4; charset=utf-8; escaping=underscores",
		resp.Header.Get("Content-Type"))

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	scrape := string(body)

	for _, name := range allExpectedMetricNames {
		assert.True(t, strings.Contains(scrape, name),
			"metric %q missing from /metrics scrape output", name)
	}
}

// TestPrometheusMetricsScrapeEndpointReflectsIncrements verifies that counter
// values incremented before the scrape are correctly reflected in the output,
// not silently dropped or reset.
func TestPrometheusMetricsScrapeEndpointReflectsIncrements(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := metrics.New(reg)

	m.IncMessagesReceived()
	m.IncMessagesReceived()
	m.IncMessagesReceived()

	srv := httptest.NewServer(promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + endpoint)
	require.NoError(t, err)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	// Prometheus text format: "metric_name{labels} value"
	// For a counter with no labels: "notip_consumer_messages_received_total 3"
	assert.True(t,
		strings.Contains(string(body), "notip_consumer_messages_received_total 3"),
		"scrape output should reflect 3 increments of messages_received_total",
	)
}

// TestPrometheusMetricsScrapeEndpointGaugeReflectsSet verifies that gauge values
// set before the scrape appear with the correct value in the output.
func TestPrometheusMetricsScrapeEndpointGaugeReflectsSet(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := metrics.New(reg)

	m.SetHeartbeatMapSize(7)

	srv := httptest.NewServer(promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + endpoint)
	require.NoError(t, err)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	assert.True(t,
		strings.Contains(string(body), "notip_consumer_heartbeat_map_size 7"),
		"scrape output should reflect heartbeat_map_size = 7",
	)
}
