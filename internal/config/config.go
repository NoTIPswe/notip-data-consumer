package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Config holds all configuration loaded from environment variables at startup.
type Config struct {
	NATSUrl                   string
	NATSTlsCa                 string
	NATSTlsCert               string
	NATSTlsKey                string
	NATSConsumerDurableName   string
	NATSConnectTimeoutSeconds int

	DBHost         string
	DBPort         int
	DBName         string
	DBUser         string
	DBPasswordFile string
	DBMaxConns     int
	DBMinConns     int
	DBSSLMode      string
	DBSSLRootCert  string

	GatewayBufferSize           int
	HeartbeatTickMs             int
	HeartbeatGracePeriodMs      int
	AlertConfigRefreshMs        int
	AlertConfigDefaultTimeoutMs int64
	AlertConfigMaxRetries       int
	MetricsAddr                 string
}

// Load reads and validates configuration from environment variables.
func Load() (*Config, error) {
	cfg := &Config{
		NATSConsumerDurableName:     envOrDefault("NATS_CONSUMER_DURABLE_NAME", "data-consumer-telemetry"),
		NATSConnectTimeoutSeconds:   10,
		DBPort:                      5432,
		DBMaxConns:                  10,
		DBMinConns:                  2,
		DBSSLMode:                   envOrDefault("DB_SSL_MODE", "require"),
		GatewayBufferSize:           1000,
		HeartbeatTickMs:             10000,
		HeartbeatGracePeriodMs:      120000,
		AlertConfigRefreshMs:        300000,
		AlertConfigDefaultTimeoutMs: 60000,
		AlertConfigMaxRetries:       10,
		MetricsAddr:                 envOrDefault("METRICS_ADDR", ":9090"),
	}

	l := &loader{cfg: cfg}

	// NATS — mTLS
	l.require("NATS_URL", &cfg.NATSUrl)
	l.require("NATS_TLS_CA", &cfg.NATSTlsCa)
	l.require("NATS_TLS_CERT", &cfg.NATSTlsCert)
	l.require("NATS_TLS_KEY", &cfg.NATSTlsKey)

	// Database
	l.require("DB_HOST", &cfg.DBHost)
	l.require("DB_NAME", &cfg.DBName)
	l.require("DB_USER", &cfg.DBUser)
	l.require("DB_PASSWORD_FILE", &cfg.DBPasswordFile)
	cfg.DBSSLRootCert = os.Getenv("DB_SSL_ROOT_CERT")

	// Optional overrides
	l.optInt("DB_PORT", &cfg.DBPort)
	l.optInt("NATS_CONNECT_TIMEOUT_SECONDS", &cfg.NATSConnectTimeoutSeconds)
	l.optInt("DB_MAX_CONNS", &cfg.DBMaxConns)
	l.optInt("DB_MIN_CONNS", &cfg.DBMinConns)
	l.optInt("GATEWAY_BUFFER_SIZE", &cfg.GatewayBufferSize)
	l.optInt("HEARTBEAT_TICK_MS", &cfg.HeartbeatTickMs)
	l.optInt("HEARTBEAT_GRACE_PERIOD_MS", &cfg.HeartbeatGracePeriodMs)
	l.optInt("ALERT_CONFIG_REFRESH_MS", &cfg.AlertConfigRefreshMs)
	l.optInt64("ALERT_CONFIG_DEFAULT_TIMEOUT_MS", &cfg.AlertConfigDefaultTimeoutMs)
	l.optInt("ALERT_CONFIG_MAX_RETRIES", &cfg.AlertConfigMaxRetries)

	if l.err == nil {
		if err := validateDBSSLMode(cfg.DBSSLMode); err != nil {
			l.err = err
		}
		if (cfg.DBSSLMode == "verify-ca" || cfg.DBSSLMode == "verify-full") && cfg.DBSSLRootCert == "" {
			l.err = fmt.Errorf("DB_SSL_ROOT_CERT is required when DB_SSL_MODE=%q", cfg.DBSSLMode)
		}
	}

	return cfg, l.err
}

// loader accumulates the first error encountered during sequential env var loading.
// Subsequent calls are no-ops once an error is set, keeping Load() branch-free.
type loader struct {
	cfg *Config
	err error
}

func (l *loader) require(key string, dst *string) {
	if l.err != nil {
		return
	}
	v := os.Getenv(key)
	if v == "" {
		l.err = fmt.Errorf("required environment variable %q is not set", key)
		return
	}
	*dst = v
}

func (l *loader) optInt(key string, dst *int) {
	if l.err != nil {
		return
	}
	if err := parseInt(os.Getenv(key), dst); err != nil {
		l.err = fmt.Errorf("%s: %w", key, err)
	}
}

func (l *loader) optInt64(key string, dst *int64) {
	if l.err != nil {
		return
	}
	if err := parseInt64(os.Getenv(key), dst); err != nil {
		l.err = fmt.Errorf("%s: %w", key, err)
	}
}

// GetDatabaseDSN constructs the Postgres DSN by reading the password from the Docker secret file.
// SSL mode is controlled by the DB_SSL_MODE environment variable (default: require).
// DB_SSL_ROOT_CERT is appended to the DSN when provided.
func (c *Config) GetDatabaseDSN() (string, error) {
	passwordBytes, err := os.ReadFile(c.DBPasswordFile)
	if err != nil {
		return "", fmt.Errorf("failed to read database password file: %w", err)
	}
	password := strings.TrimSpace(string(passwordBytes))

	dsn := fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=%s",
		c.DBUser, password, c.DBHost, c.DBPort, c.DBName, c.DBSSLMode)
	if c.DBSSLRootCert != "" {
		dsn += fmt.Sprintf("&sslrootcert=%s", c.DBSSLRootCert)
	}

<<<<<<< Updated upstream
	return dsn, nil
=======
	// Safely append query parameters
	q := dsnURL.Query()
	q.Set("sslmode", c.DBSSLMode)
	if c.DBSSLRootCert != "" {
		switch c.DBSSLMode {
		case "verify-ca", "verify-full":
			q.Set("sslrootcert", c.DBSSLRootCert)
		}
	}
	dsnURL.RawQuery = q.Encode()

	return dsnURL.String(), nil
>>>>>>> Stashed changes
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func parseInt(s string, dst *int) error {
	if s == "" {
		return nil
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return err
	}
	*dst = v
	return nil
}

func parseInt64(s string, dst *int64) error {
	if s == "" {
		return nil
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return err
	}
	*dst = v
	return nil
}

func validateDBSSLMode(v string) error {
	switch v {
	case "disable", "require", "verify-ca", "verify-full":
		return nil
	default:
		return fmt.Errorf("DB_SSL_MODE: invalid value %q", v)
	}
}
