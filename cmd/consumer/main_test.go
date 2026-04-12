package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NoTIPswe/notip-data-consumer/internal/adapter/driven"
	"github.com/NoTIPswe/notip-data-consumer/internal/adapter/driving"
	"github.com/NoTIPswe/notip-data-consumer/internal/config"
	"github.com/NoTIPswe/notip-data-consumer/internal/metrics"
	"github.com/NoTIPswe/notip-data-consumer/migrations"
)

// requiredEnvVars are the env vars that config.Load() requires.
// Clearing them forces run() to fail immediately at the config step.
var requiredEnvVars = []string{
	"NATS_URL", "NATS_TLS_CA", "NATS_TLS_CERT", "NATS_TLS_KEY",
	"DB_HOST", "DB_NAME", "DB_USER", "DB_PASSWORD_FILE",
}

const testDBSecretName = "db-secret"

func setRunRequiredEnv(t *testing.T, passwordFile string) {
	t.Helper()
	t.Setenv("NATS_URL", "tls://127.0.0.1:4222")
	t.Setenv("NATS_TLS_CA", "/nonexistent/ca.pem")
	t.Setenv("NATS_TLS_CERT", "/nonexistent/client.crt")
	t.Setenv("NATS_TLS_KEY", "/nonexistent/client.key")
	t.Setenv("DB_HOST", "127.0.0.1")
	t.Setenv("DB_NAME", "notip")
	t.Setenv("DB_USER", "notip")
	t.Setenv("DB_PASSWORD_FILE", passwordFile)
	t.Setenv("DB_SSL_MODE", "disable")
	t.Setenv("NATS_CONNECT_TIMEOUT_SECONDS", "1")
	t.Setenv("METRICS_ADDR", "127.0.0.1:0")
}

func patchRunSeams(t *testing.T) {
	t.Helper()

	prevLoadConfig := loadConfig
	prevNewMetrics := newMetrics
	prevConnectNATS := connectNATS
	prevJetStreamFromConn := jetStreamFromConn
	prevDrainNATS := drainNATS
	prevParsePoolConfig := parsePoolConfig
	prevNewPoolWithConfig := newPoolWithConfig
	prevApplyMigrations := applyMigrations
	prevClosePool := closePool
	prevRunAlertCache := runAlertCache
	prevRunDecommConsumer := runDecommConsumer
	prevRunTelemetryConsumer := runTelemetryConsumer

	t.Cleanup(func() {
		loadConfig = prevLoadConfig
		newMetrics = prevNewMetrics
		connectNATS = prevConnectNATS
		jetStreamFromConn = prevJetStreamFromConn
		drainNATS = prevDrainNATS
		parsePoolConfig = prevParsePoolConfig
		newPoolWithConfig = prevNewPoolWithConfig
		applyMigrations = prevApplyMigrations
		closePool = prevClosePool
		runAlertCache = prevRunAlertCache
		runDecommConsumer = prevRunDecommConsumer
		runTelemetryConsumer = prevRunTelemetryConsumer
	})

	newMetrics = func() *metrics.Metrics {
		return metrics.New(prometheus.NewRegistry())
	}
	drainNATS = func(_ *nats.Conn) { /* no-op: stub conn has no live connections to drain */ }
	closePool = func(_ *pgxpool.Pool) { /* no-op: stub pool has no live connections to close */ }
	runAlertCache = func(_ context.Context, _ *driven.AlertConfigCache) { /* no-op: avoids NATS calls on stub conn */ }
	runDecommConsumer = func(_ context.Context, _ *driving.NATSDecommissionConsumer) error { return nil }
	runTelemetryConsumer = func(_ context.Context, _ *driving.NATSTelemetryConsumer) error { return nil }
}

// stubInfraSeams stubs all infrastructure seams so that run() reaches the
// service-building phase without touching real NATS or Postgres.
func stubInfraSeams(t *testing.T) {
	t.Helper()
	connectNATS = func(_ *config.Config, _ *metrics.Metrics) (*nats.Conn, error) {
		return &nats.Conn{}, nil
	}
	jetStreamFromConn = func(_ *nats.Conn) (nats.JetStreamContext, error) {
		return nil, nil
	}
	parsePoolConfig = func(_ string) (*pgxpool.Config, error) {
		return &pgxpool.Config{}, nil
	}
	newPoolWithConfig = func(_ context.Context, _ *pgxpool.Config) (*pgxpool.Pool, error) {
		return &pgxpool.Pool{}, nil
	}
}

// TestRunReturnsErrorOnMissingConfig verifies that run() fails fast and returns
// a wrapped error when required environment variables are absent.
func TestRunReturnsErrorOnMissingConfig(t *testing.T) {
	for _, key := range requiredEnvVars {
		t.Setenv(key, "")
	}

	err := run()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "load config")
}

func TestRunReturnsErrorOnInvalidDBPasswordFile(t *testing.T) {
	missingSecret := filepath.Join(t.TempDir(), "missing-db-secret")
	setRunRequiredEnv(t, missingSecret)

	err := run()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "build database DSN")
}

func TestRunReturnsErrorOnNATSConnectFailure(t *testing.T) {
	secretFile := filepath.Join(t.TempDir(), testDBSecretName)
	require.NoError(t, os.WriteFile(secretFile, []byte("pw\n"), 0o600))
	setRunRequiredEnv(t, secretFile)

	err := run()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "nats connect")
}

func TestRunReturnsErrorOnJetStreamContextFailure(t *testing.T) {
	patchRunSeams(t)

	secretFile := filepath.Join(t.TempDir(), testDBSecretName)
	require.NoError(t, os.WriteFile(secretFile, []byte("pw\n"), 0o600))
	setRunRequiredEnv(t, secretFile)

	connectNATS = func(_ *config.Config, _ *metrics.Metrics) (*nats.Conn, error) {
		return &nats.Conn{}, nil
	}
	jetStreamFromConn = func(_ *nats.Conn) (nats.JetStreamContext, error) {
		return nil, errors.New("jetstream unavailable")
	}

	err := run()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "nats jetstream context")
}

func TestRunReturnsErrorOnParsePoolConfigFailure(t *testing.T) {
	patchRunSeams(t)

	secretFile := filepath.Join(t.TempDir(), testDBSecretName)
	require.NoError(t, os.WriteFile(secretFile, []byte("pw\n"), 0o600))
	setRunRequiredEnv(t, secretFile)

	connectNATS = func(_ *config.Config, _ *metrics.Metrics) (*nats.Conn, error) {
		return &nats.Conn{}, nil
	}
	jetStreamFromConn = func(_ *nats.Conn) (nats.JetStreamContext, error) {
		return nil, nil
	}
	parsePoolConfig = func(_ string) (*pgxpool.Config, error) {
		return nil, errors.New("bad dsn")
	}

	err := run()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse pool config")
}

func TestRunReturnsErrorOnCreatePGXPoolFailure(t *testing.T) {
	patchRunSeams(t)

	secretFile := filepath.Join(t.TempDir(), testDBSecretName)
	require.NoError(t, os.WriteFile(secretFile, []byte("pw\n"), 0o600))
	setRunRequiredEnv(t, secretFile)

	connectNATS = func(_ *config.Config, _ *metrics.Metrics) (*nats.Conn, error) {
		return &nats.Conn{}, nil
	}
	jetStreamFromConn = func(_ *nats.Conn) (nats.JetStreamContext, error) {
		return nil, nil
	}
	parsePoolConfig = func(_ string) (*pgxpool.Config, error) {
		return &pgxpool.Config{}, nil
	}
	newPoolWithConfig = func(_ context.Context, _ *pgxpool.Config) (*pgxpool.Pool, error) {
		return nil, errors.New("connection refused")
	}

	err := run()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "create pgxpool")
}

// TestRunReturnsErrorOnMigrationsFailure verifies that run() returns a wrapped error
// when migrations.Apply fails, after a successful pool connection.
func TestRunReturnsErrorOnMigrationsFailure(t *testing.T) {
	patchRunSeams(t)
	secretFile := filepath.Join(t.TempDir(), testDBSecretName)
	require.NoError(t, os.WriteFile(secretFile, []byte("pw\n"), 0o600))
	setRunRequiredEnv(t, secretFile)
	stubInfraSeams(t)

	applyMigrations = func(_ context.Context, _ migrations.SQLExecutor) error {
		return errors.New("migration: permission denied")
	}

	err := run()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "apply database migrations")
}

// TestRunReturnsErrorOnTelemetryConsumerFailure verifies that run() propagates an error
// returned by the telemetry consumer, covering the full adapter/service build path.
func TestRunReturnsErrorOnTelemetryConsumerFailure(t *testing.T) {
	patchRunSeams(t)
	secretFile := filepath.Join(t.TempDir(), testDBSecretName)
	require.NoError(t, os.WriteFile(secretFile, []byte("pw\n"), 0o600))
	setRunRequiredEnv(t, secretFile)
	stubInfraSeams(t)

	applyMigrations = func(_ context.Context, _ migrations.SQLExecutor) error { return nil }
	runTelemetryConsumer = func(_ context.Context, _ *driving.NATSTelemetryConsumer) error {
		return errors.New("subscribe: no responders")
	}

	err := run()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "telemetry consumer")
}

// TestRunSucceeds verifies the full happy-path: run() starts all components and
// returns nil when the telemetry consumer exits cleanly.
func TestRunSucceeds(t *testing.T) {
	patchRunSeams(t)
	secretFile := filepath.Join(t.TempDir(), testDBSecretName)
	require.NoError(t, os.WriteFile(secretFile, []byte("pw\n"), 0o600))
	setRunRequiredEnv(t, secretFile)
	stubInfraSeams(t)

	applyMigrations = func(_ context.Context, _ migrations.SQLExecutor) error { return nil }
	// runTelemetryConsumer is already stubbed to return nil by patchRunSeams.

	err := run()

	require.NoError(t, err)
}

// TestMainExitsOneOnError verifies that main() calls os.Exit(1) when run() fails.
// Uses the subprocess-exec pattern: the child sets BE_MAIN=1 and calls main(),
// which exits 1; the parent asserts the exit code.
func TestMainExitsOneOnError(t *testing.T) {
	if os.Getenv("BE_MAIN") == "1" {
		main()
		return
	}

	// Strip infrastructure vars so run() fails at config, not at NATS/DB connect.
	skip := make(map[string]bool, len(requiredEnvVars))
	for _, k := range requiredEnvVars {
		skip[k] = true
	}
	var filtered []string
	for _, e := range os.Environ() {
		if key := strings.SplitN(e, "=", 2)[0]; !skip[key] {
			filtered = append(filtered, e)
		}
	}

	cmd := exec.CommandContext(context.Background(), os.Args[0], "-test.run=TestMainExitsOneOnError")
	cmd.Env = append(filtered, "BE_MAIN=1")

	err := cmd.Run()
	var exitErr *exec.ExitError
	require.True(t, errors.As(err, &exitErr), "main() must exit non-zero when run() fails")
	assert.Equal(t, 1, exitErr.ExitCode())
}
