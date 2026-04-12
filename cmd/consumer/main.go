package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/NoTIPswe/notip-data-consumer/internal/adapter/driven"
	"github.com/NoTIPswe/notip-data-consumer/internal/adapter/driving"
	"github.com/NoTIPswe/notip-data-consumer/internal/config"
	"github.com/NoTIPswe/notip-data-consumer/internal/metrics"
	"github.com/NoTIPswe/notip-data-consumer/internal/service"
	"github.com/NoTIPswe/notip-data-consumer/migrations"
)

const (
	natsRRTimeout          = 5 * time.Second
	telemetryBatchSize     = 100
	telemetryFlushInterval = 500 * time.Millisecond
)

var (
	loadConfig = config.Load
	newMetrics = func() *metrics.Metrics {
		return metrics.New(prometheus.DefaultRegisterer)
	}
	connectNATS = func(cfg *config.Config, m *metrics.Metrics) (*nats.Conn, error) {
		return nats.Connect(cfg.NATSUrl,
			nats.RootCAs(cfg.NATSTlsCa),
			nats.ClientCert(cfg.NATSTlsCert, cfg.NATSTlsKey),
			nats.Timeout(time.Duration(cfg.NATSConnectTimeoutSeconds)*time.Second),
			nats.ReconnectHandler(func(_ *nats.Conn) {
				m.IncNATSReconnects()
				slog.Warn("nats reconnected")
			}),
		)
	}
	jetStreamFromConn = func(nc *nats.Conn) (nats.JetStreamContext, error) {
		return nc.JetStream()
	}
	drainNATS = func(nc *nats.Conn) {
		_ = nc.Drain()
	}
	parsePoolConfig   = pgxpool.ParseConfig
	newPoolWithConfig = pgxpool.NewWithConfig

	applyMigrations = migrations.Apply
	closePool       = func(p *pgxpool.Pool) { p.Close() }

	runAlertCache     = func(ctx context.Context, c *driven.AlertConfigCache) { c.Run(ctx) }
	runDecommConsumer = func(ctx context.Context, c *driving.NATSDecommissionConsumer) error {
		return c.Run(ctx)
	}
	runTelemetryConsumer = func(ctx context.Context, c *driving.NATSTelemetryConsumer) error {
		return c.Run(ctx)
	}
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	// ── Step 1: Config ──────────────────────────────────────────────────────────
	cfg, err := loadConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	dsn, err := cfg.GetDatabaseDSN()
	if err != nil {
		return fmt.Errorf("build database DSN: %w", err)
	}

	// ── Metrics ─────────────────────────────────────────────────────────────────
	m := newMetrics()

	// ── Step 2: NATS ────────────────────────────────────────────────────────────
	nc, err := connectNATS(cfg, m)
	if err != nil {
		return fmt.Errorf("nats connect: %w", err)
	}
	defer drainNATS(nc)
	slog.Info("nats connected", "url", cfg.NATSUrl)

	js, err := jetStreamFromConn(nc)
	if err != nil {
		return fmt.Errorf("nats jetstream context: %w", err)
	}

	// ── Step 3: Database pool ───────────────────────────────────────────────────
	poolCfg, err := parsePoolConfig(dsn)
	if err != nil {
		return fmt.Errorf("parse pool config: %w", err)
	}
	poolCfg.MaxConns = int32(cfg.DBMaxConns)
	poolCfg.MinConns = int32(cfg.DBMinConns)

	// Signal context created here so pool.Connect respects cancellation.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	pool, err := newPoolWithConfig(ctx, poolCfg)
	if err != nil {
		return fmt.Errorf("create pgxpool: %w", err)
	}
	defer closePool(pool)

	if err := applyMigrations(ctx, pool); err != nil {
		return fmt.Errorf("apply database migrations: %w", err)
	}
	slog.Info("database ready", "max_conns", cfg.DBMaxConns, "min_conns", cfg.DBMinConns)

	// ── Build adapters ──────────────────────────────────────────────────────────
	rrClient := driven.NewNATSRRClient(nc, natsRRTimeout)

	alertCache := driven.NewAlertConfigCache(
		rrClient, m,
		cfg.AlertConfigDefaultTimeoutMs,
		time.Duration(cfg.AlertConfigRefreshMs)*time.Millisecond,
		cfg.AlertConfigMaxRetries,
		cfg.AlertConfigInitialBackoffMs,
		cfg.AlertConfigMaxBackoffMs,
	)

	alertPublisher := driven.NewNATSAlertPublisher(js, m)
	statusUpdater := driven.NewNATSGatewayStatusUpdater(rrClient, m)
	lifecycleProvider := driven.NewNATSGatewayLifecycleProvider(rrClient, m)
	telemetryWriter := driven.NewPostgresTelemetryWriter(pool)
	clock := &driven.SystemClock{}

	// ── Build service ───────────────────────────────────────────────────────────
	tracker := service.NewHeartbeatTracker(
		clock,
		alertPublisher,
		statusUpdater,
		alertCache,
		lifecycleProvider,
		m,
		service.HeartbeatTrackerConfig{
			StatusUpdateBufSize: cfg.GatewayBufferSize,
			GracePeriod:         time.Duration(cfg.HeartbeatGracePeriodMs) * time.Millisecond,
		},
	)
	defer tracker.Close() // drain dispatch channel before NATS drains (LIFO: runs before nc.Drain)

	// ── Build driving adapters ──────────────────────────────────────────────────
	tickTimer := driving.NewHeartbeatTickTimer(
		tracker,
		m,
		time.Duration(cfg.HeartbeatTickMs)*time.Millisecond,
	)
	decommConsumer := driving.NewNATSDecommissionConsumer(js, tracker)
	telemetryConsumer := driving.NewNATSTelemetryConsumer(
		js, tracker, telemetryWriter, m,
		cfg.NATSConsumerDurableName,
		telemetryBatchSize,
		telemetryFlushInterval,
	)

	// ── Prometheus metrics server + health check ───────────────────────────────
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := pool.Ping(r.Context()); err != nil {
			http.Error(w, "db: "+err.Error(), http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	metricsSrv := &http.Server{Addr: cfg.MetricsAddr, Handler: mux}
	go func() {
		if err := metricsSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("metrics server", "err", err)
		}
	}()
	defer metricsSrv.Shutdown(context.Background()) //nolint:errcheck

	// ── Step 4: AlertConfigCache initial fetch + refresh loop ──────────────────
	go runAlertCache(ctx, alertCache)

	// ── Step 5: HeartbeatTickTimer ──────────────────────────────────────────────
	go tickTimer.Run(ctx)

	// ── Step 7: DecommissionConsumer ────────────────────────────────────────────
	go func() {
		if err := runDecommConsumer(ctx, decommConsumer); err != nil {
			slog.Error("decommission consumer", "err", err)
		}
	}()

	slog.Info("service started",
		"heartbeat_tick_ms", cfg.HeartbeatTickMs,
		"grace_period_ms", cfg.HeartbeatGracePeriodMs,
		"alert_config_refresh_ms", cfg.AlertConfigRefreshMs,
		"alert_config_default_timeout_ms", cfg.AlertConfigDefaultTimeoutMs,
		"alert_config_initial_backoff_ms", cfg.AlertConfigInitialBackoffMs,
		"alert_config_max_backoff_ms", cfg.AlertConfigMaxBackoffMs,
		"batch_size", telemetryBatchSize,
		"flush_interval_ms", telemetryFlushInterval.Milliseconds(),
		"metrics_addr", cfg.MetricsAddr,
	)

	// ── Step 8: TelemetryConsumer — blocks until ctx is cancelled ───────────────
	if err := runTelemetryConsumer(ctx, telemetryConsumer); err != nil {
		return fmt.Errorf("telemetry consumer: %w", err)
	}

	slog.Info("shutting down")
	return nil
}
