package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// holds all Prometheus metric handles for the data consumer.
// Constructed once in main and injected into any component that emits metrics.
type Metrics struct {
	MessagesReceived        prometheus.Counter
	MessageParsingErrors    prometheus.Counter
	MessagesWritten         prometheus.Counter
	WriteErrors             prometheus.Counter
	WriteLatency            prometheus.Histogram
	BatchSize               prometheus.Histogram
	AlertsPublished         prometheus.Counter
	AlertPublishErrors      prometheus.Counter
	HeartbeatMapSize        prometheus.Gauge
	HeartbeatTickDuration   prometheus.Histogram
	StatusUpdateErrors      prometheus.Counter
	StatusUpdateDropped     prometheus.Counter
	DispatchQueueLength     prometheus.Gauge
	AlertCacheRefreshErrors prometheus.Counter
	AlertCacheLastSuccess   prometheus.Gauge
	NATSReconnects          prometheus.Counter
	LifecycleQueryErrors    prometheus.Counter
}

// New registers all metrics with the provided registry and returns the handles.
// Accepts a prometheus.Registerer (rather than using the global default) so tests
// can pass prometheus.NewRegistry() and avoid duplicate-registration panics.
func New(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		MessagesReceived: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "notip_consumer_messages_received_total",
			Help: "Total NATS messages dequeued from the TELEMETRY stream.",
		}),
		MessageParsingErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "notip_consumer_message_parsing_errors_total",
			Help: "Total messages permanently discarded (Term) due to malformed JSON or invalid subject.",
		}),
		MessagesWritten: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "notip_consumer_messages_written_total",
			Help: "Total telemetry records successfully written to TimescaleDB.",
		}),
		WriteErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "notip_consumer_write_errors_total",
			Help: "Total failed TimescaleDB write attempts.",
		}),
		WriteLatency: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "notip_consumer_write_duration_seconds",
			Help:    "TimescaleDB write latency in seconds.",
			Buckets: prometheus.DefBuckets,
		}),
		BatchSize: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "notip_consumer_batch_size",
			Help:    "Number of telemetry records per batch flush operation.",
			Buckets: prometheus.ExponentialBuckets(1, 2, 8),
		}),
		AlertsPublished: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "notip_consumer_alerts_published_total",
			Help: "Total gateway-offline alerts published to JetStream.",
		}),
		AlertPublishErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "notip_consumer_alert_publish_errors_total",
			Help: "Total failed alert publish attempts.",
		}),
		HeartbeatMapSize: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "notip_consumer_heartbeat_map_size",
			Help: "Number of gateways currently tracked in the heartbeat map.",
		}),
		HeartbeatTickDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "notip_consumer_heartbeat_tick_duration_seconds",
			Help:    "Duration of a complete heartbeat Tick cycle in seconds.",
			Buckets: prometheus.DefBuckets,
		}),
		StatusUpdateErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "notip_consumer_status_update_errors_total",
			Help: "Total failed NATS RR gateway status update calls.",
		}),
		StatusUpdateDropped: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "notip_consumer_status_update_dropped_total",
			Help: "Total status updates dropped due to a full dispatch buffer.",
		}),
		DispatchQueueLength: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "notip_consumer_dispatch_queue_length",
			Help: "Number of jobs currently queued in the async dispatch channel.",
		}),
		AlertCacheRefreshErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "notip_consumer_alert_cache_refresh_errors_total",
			Help: "Total failed alert config cache refresh attempts.",
		}),
		AlertCacheLastSuccess: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "notip_consumer_alert_cache_last_success_timestamp",
			Help: "Unix timestamp of the last successful alert config fetch.",
		}),
		NATSReconnects: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "notip_consumer_nats_reconnects_total",
			Help: "Total NATS reconnection events.",
		}),
		LifecycleQueryErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "notip_consumer_lifecycle_query_errors_total",
			Help: "Total failed NATS RR gateway lifecycle query calls (per Tick candidate).",
		}),
	}

	reg.MustRegister(
		m.MessagesReceived,
		m.MessageParsingErrors,
		m.MessagesWritten,
		m.WriteErrors,
		m.WriteLatency,
		m.BatchSize,
		m.AlertsPublished,
		m.AlertPublishErrors,
		m.HeartbeatMapSize,
		m.HeartbeatTickDuration,
		m.StatusUpdateErrors,
		m.StatusUpdateDropped,
		m.DispatchQueueLength,
		m.AlertCacheRefreshErrors,
		m.AlertCacheLastSuccess,
		m.NATSReconnects,
		m.LifecycleQueryErrors,
	)

	return m
}

// Methods below allow *Metrics to satisfy the narrow metric interfaces
// defined in each adapter package, keeping adapters decoupled from this struct.

func (m *Metrics) IncMessagesReceived()                { m.MessagesReceived.Inc() }
func (m *Metrics) IncMessageParsingErrors()            { m.MessageParsingErrors.Inc() }
func (m *Metrics) IncMessagesWritten()                 { m.MessagesWritten.Inc() }
func (m *Metrics) IncWriteErrors()                     { m.WriteErrors.Inc() }
func (m *Metrics) ObserveWriteLatency(d time.Duration) { m.WriteLatency.Observe(d.Seconds()) }
func (m *Metrics) ObserveBatchSize(size float64)       { m.BatchSize.Observe(size) }
func (m *Metrics) IncAlertsPublished()                 { m.AlertsPublished.Inc() }
func (m *Metrics) IncAlertPublishErrors()              { m.AlertPublishErrors.Inc() }
func (m *Metrics) SetHeartbeatMapSize(v float64)       { m.HeartbeatMapSize.Set(v) }
func (m *Metrics) ObserveHeartbeatTickDuration(d time.Duration) {
	m.HeartbeatTickDuration.Observe(d.Seconds())
}
func (m *Metrics) IncStatusUpdateErrors()              { m.StatusUpdateErrors.Inc() }
func (m *Metrics) IncStatusUpdateDropped()             { m.StatusUpdateDropped.Inc() }
func (m *Metrics) SetDispatchQueueLength(v float64)    { m.DispatchQueueLength.Set(v) }
func (m *Metrics) IncAlertCacheRefreshErrors()         { m.AlertCacheRefreshErrors.Inc() }
func (m *Metrics) SetAlertCacheLastSuccess(ts float64) { m.AlertCacheLastSuccess.Set(ts) }
func (m *Metrics) IncNATSReconnects()                  { m.NATSReconnects.Inc() }
func (m *Metrics) IncLifecycleQueryErrors()            { m.LifecycleQueryErrors.Inc() }
