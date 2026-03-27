//go:build integration

package integration

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	streamTelemetry    = "TELEMETRY"
	streamAlerts       = "ALERTS"
	streamDecommission = "DECOMMISSION"
)

var (
	sharedNATSURL   string
	sharedNATSCerts *testCerts
	sharedPool      *pgxpool.Pool
	sharedJS        nats.JetStreamContext
)

// natsConf mirrors the production nats-server.conf structure:
// verify_and_map extracts the client cert DN and looks it up in authorization.users.
// default_permissions denies everything — any DN not in users[] is blocked.
const natsConf = `
listen: "0.0.0.0:4222"

jetstream {
  store_dir: "/data"
}

tls {
  ca_file:        "/certs/ca.pem"
  cert_file:      "/certs/server-cert.pem"
  key_file:       "/certs/server-key.pem"
  verify:         true
  verify_and_map: true
}

authorization {
  default_permissions: {
    publish:   { deny: [">"] }
    subscribe: { deny: [">"] }
  }
  users = [
    { user: "CN=data-consumer", permissions: { publish: { allow: [">"] }, subscribe: { allow: [">"] } } }
  ]
}
`

func TestMain(m *testing.M) {
	os.Exit(runSuite(m))
}

// fatalStep logs an error and returns 1 (non-zero exit) for use in runSuite.
func fatalStep(format string, args ...any) int {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	return 1
}

func runSuite(m *testing.M) int {
	ctx := context.Background()

	certDir, certs, err := setupCerts(ctx)
	if err != nil {
		return fatalStep("setup certs: %v", err)
	}
	defer os.RemoveAll(certDir)
	sharedNATSCerts = certs

	natsCleanup, err := setupNATS(ctx, certDir, certs)
	if err != nil {
		return fatalStep("setup NATS: %v", err)
	}
	defer natsCleanup()

	dbCleanup, err := setupTimescaleDB(ctx)
	if err != nil {
		return fatalStep("setup TimescaleDB: %v", err)
	}
	defer dbCleanup()

	return m.Run()
}

// setupCerts generates an ephemeral TLS cert bundle for mTLS with NATS.
// Returns the temp directory path, generated certs, and any error.
func setupCerts(ctx context.Context) (string, *testCerts, error) {
	var extraIPs []net.IP
	provider, err := testcontainers.NewDockerProvider()
	if err != nil {
		return "", nil, fmt.Errorf("new docker provider: %w", err)
	}
	dockerHost, err := provider.DaemonHost(ctx)
	if err != nil {
		return "", nil, fmt.Errorf("docker daemon host: %w", err)
	}
	if ip := net.ParseIP(dockerHost); ip != nil {
		extraIPs = append(extraIPs, ip)
	}
	for i := 16; i <= 31; i++ {
		extraIPs = append(extraIPs, net.ParseIP(fmt.Sprintf("172.%d.0.1", i)))
	}

	certDir, err := os.MkdirTemp("", "notip-integration-certs-*")
	if err != nil {
		return "", nil, fmt.Errorf("create cert dir: %w", err)
	}

	certs, err := generateCerts(certDir, extraIPs)
	if err != nil {
		os.RemoveAll(certDir)
		return "", nil, fmt.Errorf("generate certs: %w", err)
	}

	return certDir, certs, nil
}

// setupNATS starts a NATS JetStream container with mTLS, creates streams, and
// stores the shared connection/JS context in package vars. Returns a cleanup
// function that terminates the container and drains the connection.
func setupNATS(ctx context.Context, certDir string, certs *testCerts) (func(), error) {
	confPath := filepath.Join(certDir, "nats.conf")
	if err := os.WriteFile(confPath, []byte(natsConf), 0o600); err != nil {
		return nil, fmt.Errorf("write nats config: %w", err)
	}

	natsC, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "nats:latest",
			ExposedPorts: []string{"4222/tcp"},
			Cmd:          []string{"-c", "/etc/nats/nats.conf"},
			Files: []testcontainers.ContainerFile{
				{HostFilePath: confPath, ContainerFilePath: "/etc/nats/nats.conf", FileMode: 0o644},
				{HostFilePath: certs.CACert, ContainerFilePath: "/certs/ca.pem", FileMode: 0o644},
				{HostFilePath: certs.ServerCert, ContainerFilePath: "/certs/server-cert.pem", FileMode: 0o644},
				{HostFilePath: certs.ServerKey, ContainerFilePath: "/certs/server-key.pem", FileMode: 0o600},
			},
			WaitingFor: wait.ForLog("Server is ready"),
		},
		Started: true,
	})
	if err != nil {
		return nil, fmt.Errorf("start NATS container: %w", err)
	}

	natsHost, err := natsC.Host(ctx)
	if err != nil {
		_ = natsC.Terminate(ctx)
		return nil, fmt.Errorf("get NATS host: %w", err)
	}
	natsPort, err := natsC.MappedPort(ctx, "4222/tcp")
	if err != nil {
		_ = natsC.Terminate(ctx)
		return nil, fmt.Errorf("get NATS port: %w", err)
	}
	sharedNATSURL = fmt.Sprintf("tls://%s:%s", natsHost, natsPort.Port())

	nc, err := nats.Connect(
		sharedNATSURL,
		nats.RootCAs(certs.CACert),
		nats.ClientCert(certs.ClientCert, certs.ClientKey),
		nats.Timeout(10*time.Second),
	)
	if err != nil {
		_ = natsC.Terminate(ctx)
		return nil, fmt.Errorf("nats connect: %w", err)
	}

	js, err := nc.JetStream()
	if err != nil {
		_ = nc.Drain()
		_ = natsC.Terminate(ctx)
		return nil, fmt.Errorf("nats jetstream: %w", err)
	}
	sharedJS = js

	if err := createStreams(js); err != nil {
		_ = nc.Drain()
		_ = natsC.Terminate(ctx)
		return nil, err
	}

	cleanup := func() {
		_ = nc.Drain()
		_ = natsC.Terminate(ctx)
	}
	return cleanup, nil
}

// createStreams adds the JetStream streams required by the integration tests.
func createStreams(js nats.JetStreamContext) error {
	streams := []nats.StreamConfig{
		{
			Name:      streamTelemetry,
			Subjects:  []string{"telemetry.data.>"},
			Retention: nats.LimitsPolicy,
			MaxAge:    30 * 24 * time.Hour,
			Storage:   nats.FileStorage,
			Replicas:  1,
			Discard:   nats.DiscardOld,
		},
		{
			Name:      streamAlerts,
			Subjects:  []string{"alert.>"},
			Retention: nats.LimitsPolicy,
			MaxAge:    30 * 24 * time.Hour,
			Storage:   nats.FileStorage,
			Replicas:  1,
			Discard:   nats.DiscardOld,
		},
		{
			Name:      streamDecommission,
			Subjects:  []string{"gateway.decommissioned.>"},
			Retention: nats.LimitsPolicy,
			MaxAge:    30 * 24 * time.Hour,
			Storage:   nats.FileStorage,
			Replicas:  1,
			Discard:   nats.DiscardOld,
		},
	}
	for _, sc := range streams {
		if _, err := js.AddStream(&sc); err != nil {
			return fmt.Errorf("add %s stream: %w", sc.Name, err)
		}
	}
	return nil
}

// setupTimescaleDB starts a TimescaleDB container, applies migrations, and
// stores the shared connection pool in a package var. Returns a cleanup
// function that closes the pool and terminates the container.
func setupTimescaleDB(ctx context.Context) (func(), error) {
	dbC, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "timescale/timescaledb:latest-pg17",
			ExposedPorts: []string{"5432/tcp"},
			Env: map[string]string{
				"POSTGRES_USER":     "test",
				"POSTGRES_PASSWORD": "test",
				"POSTGRES_DB":       "testdb",
			},
			// TimescaleDB restarts Postgres once during init — the message appears twice.
			WaitingFor: wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60 * time.Second),
		},
		Started: true,
	})
	if err != nil {
		return nil, fmt.Errorf("start TimescaleDB container: %w", err)
	}

	dbHost, err := dbC.Host(ctx)
	if err != nil {
		_ = dbC.Terminate(ctx)
		return nil, fmt.Errorf("get DB host: %w", err)
	}
	dbPort, err := dbC.MappedPort(ctx, "5432/tcp")
	if err != nil {
		_ = dbC.Terminate(ctx)
		return nil, fmt.Errorf("get DB port: %w", err)
	}
	dsn := fmt.Sprintf("postgres://test:test@%s:%s/testdb?sslmode=disable", dbHost, dbPort.Port())

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		_ = dbC.Terminate(ctx)
		return nil, fmt.Errorf("create pool: %w", err)
	}

	migration, err := os.ReadFile("../../migrations/001_create_telemetry_hypertable.sql")
	if err != nil {
		pool.Close()
		_ = dbC.Terminate(ctx)
		return nil, fmt.Errorf("read migration: %w", err)
	}
	if _, err := pool.Exec(ctx, string(migration)); err != nil {
		pool.Close()
		_ = dbC.Terminate(ctx)
		return nil, fmt.Errorf("apply migration: %w", err)
	}
	sharedPool = pool

	cleanup := func() {
		pool.Close()
		_ = dbC.Terminate(ctx)
	}
	return cleanup, nil
}

// connectNATSWithMTLS opens a new mTLS NATS connection for one test and registers
// a cleanup that drains it when the test ends.
func connectNATSWithMTLS(t *testing.T) *nats.Conn {
	t.Helper()
	nc, err := nats.Connect(
		sharedNATSURL,
		nats.RootCAs(sharedNATSCerts.CACert),
		nats.ClientCert(sharedNATSCerts.ClientCert, sharedNATSCerts.ClientKey),
		nats.Timeout(10*time.Second),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = nc.Drain() })
	return nc
}

// truncateTelemetry removes all rows from the telemetry table before a test.
func truncateTelemetry(t *testing.T) {
	t.Helper()
	_, err := sharedPool.Exec(context.Background(), "TRUNCATE telemetry")
	require.NoError(t, err)
}

// purgeStream removes all messages from a JetStream stream before a test
// so new durable consumers always start with an empty stream.
func purgeStream(t *testing.T, stream string) {
	t.Helper()
	require.NoError(t, sharedJS.PurgeStream(stream))
}
