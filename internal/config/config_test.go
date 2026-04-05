package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Set all the required env varibles.
func setRequiredEnv(t *testing.T) {
	t.Helper()
	t.Setenv("NATS_URL", "tls://nats:4222")
	t.Setenv("NATS_TLS_CA", "/certs/ca.crt")
	t.Setenv("NATS_TLS_CERT", "/certs/data-consumer.crt")
	t.Setenv("NATS_TLS_KEY", "/certs/data-consumer.key")
	t.Setenv("DB_HOST", "measures-db")
	t.Setenv("DB_NAME", "notip_measures")
	t.Setenv("DB_USER", "notip_measures")
	t.Setenv("DB_PASSWORD_FILE", "/run/secrets/measures_db_password")
}

// Test if defaults are working.
func TestLoadDefaults(t *testing.T) {
	setRequiredEnv(t)

	cfg, err := Load()
	require.NoError(t, err)

	assert.Equal(t, "data-consumer-telemetry", cfg.NATSConsumerDurableName)
	assert.Equal(t, 10, cfg.NATSConnectTimeoutSeconds)
	assert.Equal(t, 5432, cfg.DBPort)
	assert.Equal(t, 10, cfg.DBMaxConns)
	assert.Equal(t, 2, cfg.DBMinConns)
	assert.Equal(t, "require", cfg.DBSSLMode)
	assert.Equal(t, "", cfg.DBSSLRootCert)
	assert.Equal(t, 1000, cfg.GatewayBufferSize)
	assert.Equal(t, 10000, cfg.HeartbeatTickMs)
	assert.Equal(t, 120000, cfg.HeartbeatGracePeriodMs)
	assert.Equal(t, 300000, cfg.AlertConfigRefreshMs)
	assert.Equal(t, int64(60000), cfg.AlertConfigDefaultTimeoutMs)
	assert.Equal(t, 10, cfg.AlertConfigMaxRetries)
	assert.Equal(t, ":9090", cfg.MetricsAddr)
}

// Test a missing env variabile.
func TestLoadMissingNATSUrl(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("NATS_URL", "")

	_, err := Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "NATS_URL")
}

func TestLoadMissingNATSTLS(t *testing.T) {
	for _, key := range []string{"NATS_TLS_CA", "NATS_TLS_CERT", "NATS_TLS_KEY"} {
		t.Run(key, func(t *testing.T) {
			setRequiredEnv(t)
			t.Setenv(key, "")

			_, err := Load()
			require.Error(t, err)
			assert.Contains(t, err.Error(), key)
		})
	}
}

func TestLoadMissingDBFields(t *testing.T) {
	for _, key := range []string{"DB_HOST", "DB_NAME", "DB_USER", "DB_PASSWORD_FILE"} {
		t.Run(key, func(t *testing.T) {
			setRequiredEnv(t)
			t.Setenv(key, "")

			_, err := Load()
			require.Error(t, err)
			assert.Contains(t, err.Error(), key)
		})
	}
}

func TestLoadOptionalOverrides(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("DB_PORT", "5433")
	t.Setenv("HEARTBEAT_TICK_MS", "5000")
	t.Setenv("GATEWAY_BUFFER_SIZE", "500")
	t.Setenv("METRICS_ADDR", ":8888")

	cfg, err := Load()
	require.NoError(t, err)

	assert.Equal(t, 5433, cfg.DBPort)
	assert.Equal(t, 5000, cfg.HeartbeatTickMs)
	assert.Equal(t, 500, cfg.GatewayBufferSize)
	assert.Equal(t, ":8888", cfg.MetricsAddr)
}

func TestLoadInvalidIntegerField(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("HEARTBEAT_TICK_MS", "not-a-number")

	_, err := Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HEARTBEAT_TICK_MS")
}

func TestLoadInvalidDBSSLMode(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("DB_SSL_MODE", "invalid-mode")

	_, err := Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DB_SSL_MODE")
}

func TestLoadVerifyFullRequiresRootCert(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("DB_SSL_MODE", "verify-full")
	t.Setenv("DB_SSL_ROOT_CERT", "")

	_, err := Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DB_SSL_ROOT_CERT")
}

func TestLoadVerifyFullWithRootCert(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("DB_SSL_MODE", "verify-full")
	t.Setenv("DB_SSL_ROOT_CERT", "/certs/ca.crt")

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "verify-full", cfg.DBSSLMode)
	assert.Equal(t, "/certs/ca.crt", cfg.DBSSLRootCert)
}

func TestGetDatabaseDSN(t *testing.T) {
	secretFile := filepath.Join(t.TempDir(), "db_password")
	require.NoError(t, os.WriteFile(secretFile, []byte("s3cr3t\n"), 0o600))

	cfg := &Config{
		DBUser:         "notip_measures",
		DBHost:         "measures-db",
		DBPort:         5432,
		DBName:         "notip_measures",
		DBPasswordFile: secretFile,
		DBSSLMode:      "disable",
	}

	dsn, err := cfg.GetDatabaseDSN()
	require.NoError(t, err)
	assert.Equal(t, "postgres://notip_measures:s3cr3t@measures-db:5432/notip_measures?sslmode=disable", dsn)
}

func TestGetDatabaseDSNWithRootCert(t *testing.T) {
	secretFile := filepath.Join(t.TempDir(), "db_password")
	require.NoError(t, os.WriteFile(secretFile, []byte("s3cr3t\n"), 0o600))

	cfg := &Config{
		DBUser:         "notip_measures",
		DBHost:         "measures-db",
		DBPort:         5432,
		DBName:         "notip_measures",
		DBPasswordFile: secretFile,
		DBSSLMode:      "verify-full",
		DBSSLRootCert:  "/certs/ca.crt",
	}

	dsn, err := cfg.GetDatabaseDSN()
	require.NoError(t, err)
	assert.Equal(t, "postgres://notip_measures:s3cr3t@measures-db:5432/notip_measures?sslmode=verify-full&sslrootcert=/certs/ca.crt", dsn)
}

func TestGetDatabaseDSNMissingFile(t *testing.T) {
	cfg := &Config{DBPasswordFile: "/nonexistent/secret"}

	_, err := cfg.GetDatabaseDSN()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to read database password file")
}

func TestParseInt64EmptyLeavesValueUnchanged(t *testing.T) {
	v := int64(42)

	err := parseInt64("", &v)

	require.NoError(t, err)
	assert.Equal(t, int64(42), v)
}

func TestParseInt64ParsesValue(t *testing.T) {
	v := int64(0)

	err := parseInt64("60000", &v)

	require.NoError(t, err)
	assert.Equal(t, int64(60000), v)
}

func TestParseInt64InvalidValueReturnsError(t *testing.T) {
	v := int64(42)

	err := parseInt64("not-a-number", &v)

	require.Error(t, err)
	assert.Equal(t, int64(42), v)
}
