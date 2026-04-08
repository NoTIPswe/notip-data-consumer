package metrics_test

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NoTIPswe/notip-data-consumer/internal/metrics"
)

// newMetrics creates a fresh registry per test to avoid duplicate-registration panics.
func newMetrics() *metrics.Metrics {
	return metrics.New(prometheus.NewRegistry())
}

// ─── Registration ─────────────────────────────────────────────────────────────

func TestMetricsNewRegistersWithoutPanic(t *testing.T) {
	assert.NotPanics(t, func() { newMetrics() })
}

func TestMetricsNewReturnNonNil(t *testing.T) {
	m := newMetrics()
	require.NotNil(t, m)
	assert.NotNil(t, m.MessagesReceived)
	assert.NotNil(t, m.MessageParsingErrors)
	assert.NotNil(t, m.MessagesWritten)
	assert.NotNil(t, m.WriteErrors)
	assert.NotNil(t, m.WriteLatency)
	assert.NotNil(t, m.BatchSize)
	assert.NotNil(t, m.AlertsPublished)
	assert.NotNil(t, m.AlertPublishErrors)
	assert.NotNil(t, m.HeartbeatMapSize)
	assert.NotNil(t, m.HeartbeatTickDuration)
	assert.NotNil(t, m.StatusUpdateErrors)
	assert.NotNil(t, m.StatusUpdateDropped)
	assert.NotNil(t, m.DispatchQueueLength)
	assert.NotNil(t, m.AlertCacheRefreshErrors)
	assert.NotNil(t, m.AlertCacheLastSuccess)
	assert.NotNil(t, m.NATSReconnects)
	assert.NotNil(t, m.LifecycleQueryErrors)
}

func TestMetricsDuplicateRegistrationPanics(t *testing.T) {
	reg := prometheus.NewRegistry()
	assert.NotPanics(t, func() { metrics.New(reg) })
	assert.Panics(t, func() { metrics.New(reg) },
		"registering the same metrics twice on the same registry must panic")
}

// ─── Counter methods ──────────────────────────────────────────────────────────

func TestMetricsIncMessagesReceived(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := metrics.New(reg)
	assert.NotPanics(t, func() { m.IncMessagesReceived() })
}

func TestMetricsIncMessagesWritten(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := metrics.New(reg)
	assert.NotPanics(t, func() { m.IncMessagesWritten() })
}

func TestMetricsIncWriteErrors(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := metrics.New(reg)
	assert.NotPanics(t, func() { m.IncWriteErrors() })
}

func TestMetricsIncAlertsPublished(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := metrics.New(reg)
	assert.NotPanics(t, func() { m.IncAlertsPublished() })
}

func TestMetricsIncAlertPublishErrors(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := metrics.New(reg)
	assert.NotPanics(t, func() { m.IncAlertPublishErrors() })
}

func TestMetricsIncStatusUpdateErrors(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := metrics.New(reg)
	assert.NotPanics(t, func() { m.IncStatusUpdateErrors() })
}

func TestMetricsIncStatusUpdateDropped(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := metrics.New(reg)
	assert.NotPanics(t, func() { m.IncStatusUpdateDropped() })
}

func TestMetricsIncAlertCacheRefreshErrors(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := metrics.New(reg)
	assert.NotPanics(t, func() { m.IncAlertCacheRefreshErrors() })
}

func TestMetricsIncNATSReconnects(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := metrics.New(reg)
	assert.NotPanics(t, func() { m.IncNATSReconnects() })
}

func TestMetricsIncLifecycleQueryErrors(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := metrics.New(reg)
	assert.NotPanics(t, func() { m.IncLifecycleQueryErrors() })
}

func TestMetricsIncMessageParsingErrors(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := metrics.New(reg)
	assert.NotPanics(t, func() { m.IncMessageParsingErrors() })
}

// ─── Gauge and Histogram ──────────────────────────────────────────────────────

func TestMetricsSetHeartbeatMapSize(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := metrics.New(reg)
	assert.NotPanics(t, func() { m.SetHeartbeatMapSize(42.0) })
}

func TestMetricsObserveWriteLatency(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := metrics.New(reg)
	assert.NotPanics(t, func() { m.ObserveWriteLatency(250 * time.Millisecond) })
}

func TestMetricsObserveBatchSize(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := metrics.New(reg)
	assert.NotPanics(t, func() { m.ObserveBatchSize(50) })
}

func TestMetricsObserveHeartbeatTickDuration(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := metrics.New(reg)
	assert.NotPanics(t, func() { m.ObserveHeartbeatTickDuration(100 * time.Millisecond) })
}

func TestMetricsSetDispatchQueueLength(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := metrics.New(reg)
	assert.NotPanics(t, func() { m.SetDispatchQueueLength(5) })
}

func TestMetricsSetAlertCacheLastSuccess(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := metrics.New(reg)
	assert.NotPanics(t, func() { m.SetAlertCacheLastSuccess(1711305600) })
}

// ─── Narrow interface satisfaction ────────────────────────────────────────────

// Compile-time checks: *Metrics must satisfy every narrow interface it is passed as.
// If any method signature is wrong, this file won't compile.

func TestMetricsSatisfiesNarrowInterfaces(t *testing.T) {
	m := newMetrics()

	type telemetryConsumerMetrics interface {
		IncMessagesReceived()
		IncMessageParsingErrors()
		IncMessagesWritten()
		IncWriteErrors()
		ObserveWriteLatency(d time.Duration)
		ObserveBatchSize(size float64)
	}
	type alertPublisherMetrics interface {
		IncAlertsPublished()
		IncAlertPublishErrors()
	}
	type statusErrRecorder interface {
		IncStatusUpdateErrors()
	}
	type alertCacheMetrics interface {
		IncAlertCacheRefreshErrors()
		SetAlertCacheLastSuccess(ts float64)
	}
	type heartbeatTrackerMetrics interface {
		IncStatusUpdateDropped()
		SetHeartbeatMapSize(v float64)
		SetDispatchQueueLength(v float64)
	}
	type heartbeatTickDurationObserver interface {
		ObserveHeartbeatTickDuration(d time.Duration)
	}
	type natsReconnectRecorder interface {
		IncNATSReconnects()
	}
	type lifecycleQueryErrRecorder interface {
		IncLifecycleQueryErrors()
	}

	var _ telemetryConsumerMetrics = m
	var _ alertPublisherMetrics = m
	var _ statusErrRecorder = m
	var _ alertCacheMetrics = m
	var _ heartbeatTrackerMetrics = m
	var _ heartbeatTickDurationObserver = m
	var _ natsReconnectRecorder = m
	var _ lifecycleQueryErrRecorder = m
}
