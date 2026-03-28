package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// holds all Prometheus metric handles for the data consumer.
// Constructed once in main and injected into any component that emits metrics.
type Metrics struct {
	MessagesReceived        prometheus.Counter
	MessagesWritten         prometheus.Counter
	WriteErrors             prometheus.Counter
	WriteLatency            prometheus.Histogram
	AlertsPublished         prometheus.Counter
	AlertPublishErrors      prometheus.Counter
	StatusUpdateErrors      prometheus.Counter
	StatusUpdateDropped     prometheus.Counter
	AlertCacheRefreshErrors prometheus.Counter
	NATSReconnects          prometheus.Counter
	HeartbeatMapSize        prometheus.Gauge
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
		AlertsPublished: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "notip_consumer_alerts_published_total",
			Help: "Total gateway-offline alerts published to JetStream.",
		}),
		AlertPublishErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "notip_consumer_alert_publish_errors_total",
			Help: "Total failed alert publish attempts.",
		}),
		StatusUpdateErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "notip_consumer_status_update_errors_total",
			Help: "Total failed NATS RR gateway status update calls.",
		}),
		StatusUpdateDropped: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "notip_consumer_status_update_dropped_total",
			Help: "Total status updates dropped due to a full dispatch buffer.",
		}),
		AlertCacheRefreshErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "notip_consumer_alert_cache_refresh_errors_total",
			Help: "Total failed alert config cache refresh attempts.",
		}),
		NATSReconnects: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "notip_consumer_nats_reconnects_total",
			Help: "Total NATS reconnection events.",
		}),
		HeartbeatMapSize: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "notip_consumer_heartbeat_map_size",
			Help: "Number of gateways currently tracked in the heartbeat map.",
		}),
	}

	reg.MustRegister(
		m.MessagesReceived,
		m.MessagesWritten,
		m.WriteErrors,
		m.WriteLatency,
		m.AlertsPublished,
		m.AlertPublishErrors,
		m.StatusUpdateErrors,
		m.StatusUpdateDropped,
		m.AlertCacheRefreshErrors,
		m.NATSReconnects,
		m.HeartbeatMapSize,
	)

	return m
}

// Methods below allow *Metrics to satisfy the narrow metric interfaces
// defined in each adapter package, keeping adapters decoupled from this struct.

func (m *Metrics) IncMessagesReceived()                { m.MessagesReceived.Inc() }
func (m *Metrics) IncMessagesWritten()                 { m.MessagesWritten.Inc() }
func (m *Metrics) IncWriteErrors()                     { m.WriteErrors.Inc() }
func (m *Metrics) ObserveWriteLatency(d time.Duration) { m.WriteLatency.Observe(d.Seconds()) }
func (m *Metrics) IncAlertsPublished()                 { m.AlertsPublished.Inc() }
func (m *Metrics) IncAlertPublishErrors()              { m.AlertPublishErrors.Inc() }
func (m *Metrics) IncStatusUpdateErrors()              { m.StatusUpdateErrors.Inc() }
func (m *Metrics) IncStatusUpdateDropped()             { m.StatusUpdateDropped.Inc() }
func (m *Metrics) IncAlertCacheRefreshErrors()         { m.AlertCacheRefreshErrors.Inc() }
func (m *Metrics) IncNATSReconnects()                  { m.NATSReconnects.Inc() }
func (m *Metrics) SetHeartbeatMapSize(v float64)       { m.HeartbeatMapSize.Set(v) }
