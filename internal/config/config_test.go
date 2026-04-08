package config

import (
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testCertCAPath  = "/certs/ca.crt"
	testDBHost      = "measures-db"
	testDBSSLVerify = "verify-full"
	testInvalidNum  = "not-a-number"
	testNATSURL     = "tls://nats:4222"
)

// Set all the required env varibles.
func setRequiredEnv(t *testing.T) {
	t.Helper()
	t.Setenv("NATS_URL", testNATSURL)
	t.Setenv("NATS_TLS_CA", testCertCAPath)
	t.Setenv("NATS_TLS_CERT", "/certs/data-consumer.crt")
	t.Setenv("NATS_TLS_KEY", "/certs/data-consumer.key")
	t.Setenv("DB_HOST", testDBHost)
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
	assert.Equal(t, 1000, cfg.AlertConfigInitialBackoffMs)
	assert.Equal(t, 30000, cfg.AlertConfigMaxBackoffMs)
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
	t.Setenv("DB_SSL_ROOT_CERT", testCertCAPath)
	t.Setenv("HEARTBEAT_TICK_MS", "5000")
	t.Setenv("GATEWAY_BUFFER_SIZE", "500")
	t.Setenv("METRICS_ADDR", ":8888")

	cfg, err := Load()
	require.NoError(t, err)

	assert.Equal(t, 5433, cfg.DBPort)
	assert.Equal(t, testCertCAPath, cfg.DBSSLRootCert)
	assert.Equal(t, 5000, cfg.HeartbeatTickMs)
	assert.Equal(t, 500, cfg.GatewayBufferSize)
	assert.Equal(t, ":8888", cfg.MetricsAddr)
}

func TestLoadInvalidIntegerField(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("HEARTBEAT_TICK_MS", testInvalidNum)

	_, err := Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HEARTBEAT_TICK_MS")
}

func TestLoadInvalidAlertConfigDefaultTimeoutMs(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("ALERT_CONFIG_DEFAULT_TIMEOUT_MS", testInvalidNum)

	_, err := Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ALERT_CONFIG_DEFAULT_TIMEOUT_MS")
}

func TestLoadInvalidAlertConfigInitialBackoffMs(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("ALERT_CONFIG_INITIAL_BACKOFF_MS", testInvalidNum)

	_, err := Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ALERT_CONFIG_INITIAL_BACKOFF_MS")
}

func TestLoadInvalidAlertConfigMaxBackoffMs(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("ALERT_CONFIG_MAX_BACKOFF_MS", testInvalidNum)

	_, err := Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ALERT_CONFIG_MAX_BACKOFF_MS")
}

func TestLoadFirstErrorShortCircuitsOptionalParsing(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("NATS_URL", "")
	t.Setenv("ALERT_CONFIG_DEFAULT_TIMEOUT_MS", testInvalidNum)

	_, err := Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "NATS_URL")
	assert.NotContains(t, err.Error(), "ALERT_CONFIG_DEFAULT_TIMEOUT_MS")
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
	t.Setenv("DB_SSL_MODE", testDBSSLVerify)
	t.Setenv("DB_SSL_ROOT_CERT", "")

	_, err := Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DB_SSL_ROOT_CERT")
}

func TestLoadVerifyFullWithRootCert(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("DB_SSL_MODE", testDBSSLVerify)
	t.Setenv("DB_SSL_ROOT_CERT", testCertCAPath)

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, testDBSSLVerify, cfg.DBSSLMode)
	assert.Equal(t, testCertCAPath, cfg.DBSSLRootCert)
}

func TestGetDatabaseDSN(t *testing.T) {
	secretFile := filepath.Join(t.TempDir(), "db_password")
	testFileContent := "test-db-pass-1"
	require.NoError(t, os.WriteFile(secretFile, []byte(testFileContent+"\n"), 0o600))

	cfg := &Config{
		DBUser:         "notip_measures",
		DBHost:         testDBHost,
		DBPort:         5432,
		DBName:         "notip_measures",
		DBPasswordFile: secretFile,
		DBSSLMode:      "disable",
	}

	dsn, err := cfg.GetDatabaseDSN()
	require.NoError(t, err)

	parsed, err := url.Parse(dsn)
	require.NoError(t, err)
	userPassword, ok := parsed.User.Password()
	require.True(t, ok)

	assert.Equal(t, "postgres", parsed.Scheme)
	assert.Equal(t, "notip_measures", parsed.User.Username())
	assert.Equal(t, testFileContent, userPassword)
	assert.Equal(t, testDBHost+":5432", parsed.Host)
	assert.Equal(t, "/notip_measures", parsed.Path)
	assert.Equal(t, "disable", parsed.Query().Get("sslmode"))
}

func TestGetDatabaseDSNWithRootCert(t *testing.T) {
	secretFile := filepath.Join(t.TempDir(), "db_password")
	testFileContent := "test-db-pass-2"
	require.NoError(t, os.WriteFile(secretFile, []byte(testFileContent+"\n"), 0o600))

	cfg := &Config{
		DBUser:         "notip_measures",
		DBHost:         testDBHost,
		DBPort:         5432,
		DBName:         "notip_measures",
		DBPasswordFile: secretFile,
		DBSSLMode:      testDBSSLVerify,
		DBSSLRootCert:  testCertCAPath,
	}

	dsn, err := cfg.GetDatabaseDSN()
	require.NoError(t, err)

	parsed, err := url.Parse(dsn)
	require.NoError(t, err)
	userPassword, ok := parsed.User.Password()
	require.True(t, ok)

	assert.Equal(t, "postgres", parsed.Scheme)
	assert.Equal(t, "notip_measures", parsed.User.Username())
	assert.Equal(t, testFileContent, userPassword)
	assert.Equal(t, testDBHost+":5432", parsed.Host)
	assert.Equal(t, "/notip_measures", parsed.Path)
	assert.Equal(t, testDBSSLVerify, parsed.Query().Get("sslmode"))
	assert.Equal(t, testCertCAPath, parsed.Query().Get("sslrootcert"))
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

	err := parseInt64(testInvalidNum, &v)

	require.Error(t, err)
	assert.Equal(t, int64(42), v)
}
