# notip-data-consumer

Go microservice in the NoTIP platform. Consumes encrypted telemetry from NATS JetStream, persists it to TimescaleDB, and tracks gateway liveness to fire offline alerts.

## Responsibilities

1. Subscribe to `telemetry.data.>` via a JetStream durable consumer and write every message verbatim to TimescaleDB (**Rule Zero** — encrypted blobs are never decoded server-side).
2. Maintain an in-memory heartbeat map; on each periodic tick detect gateways that have exceeded their configured offline timeout.
3. On offline transition: publish a `gw_offline` alert to JetStream and call the Management API via NATS Request-Reply.
4. On online recovery: call the Management API to mark the gateway online.
5. Subscribe to `gateway.decommissioned.>` and remove decommissioned gateways from the heartbeat map.
6. Periodically refresh per-tenant alert configurations from the Management API.

## NATS Subjects

| Subject | Direction | Description |
|---|---|---|
| `telemetry.data.{tenantId}.{gwId}` | Gateway → consumer | Encrypted telemetry envelope |
| `alert.gw_offline.{tenantId}` | Consumer → JetStream | Gateway-offline alert |
| `gateway.decommissioned.{tenantId}.{gwId}` | Management API → consumer | Decommission broadcast |
| `internal.mgmt.alert-configs.list` | Consumer → Mgmt API (RR) | Fetch alert configurations |
| `internal.mgmt.gateway.update-status` | Consumer → Mgmt API (RR) | Report gateway online/offline |

## Configuration

All configuration is loaded from environment variables at startup. Missing required variables cause an immediate crash.

| Variable | Required | Default | Description |
|---|---|---|---|
| `NATS_URL` | yes | — | NATS server URL (`tls://…`) |
| `NATS_TLS_CA` | yes | — | Path to CA certificate |
| `NATS_TLS_CERT` | yes | — | Path to client certificate |
| `NATS_TLS_KEY` | yes | — | Path to client private key |
| `DB_HOST` | yes | — | TimescaleDB host |
| `DB_NAME` | yes | — | Database name |
| `DB_USER` | yes | — | Database user |
| `DB_PASSWORD_FILE` | yes | — | Path to file containing the DB password (Docker secret) |
| `DB_PORT` | no | `5432` | |
| `DB_MAX_CONNS` | no | `10` | pgxpool max connections |
| `DB_MIN_CONNS` | no | `2` | pgxpool min connections |
| `DB_SSL_MODE` | no | `require` | PostgreSQL TLS mode (`disable`, `require`, `verify-ca`, `verify-full`) |
| `NATS_CONSUMER_DURABLE_NAME` | no | `data-consumer-telemetry` | JetStream durable consumer name |
| `NATS_CONNECT_TIMEOUT_SECONDS` | no | `10` | |
| `GATEWAY_BUFFER_SIZE` | no | `1000` | Status update dispatch buffer |
| `HEARTBEAT_TICK_MS` | no | `10000` | Liveness check interval |
| `HEARTBEAT_GRACE_PERIOD_MS` | no | `120000` | Startup window before offline alerts fire |
| `ALERT_CONFIG_REFRESH_MS` | no | `300000` | Alert config cache refresh interval |
| `ALERT_CONFIG_DEFAULT_TIMEOUT_MS` | no | `60000` | Fallback offline timeout |
| `ALERT_CONFIG_MAX_RETRIES` | no | `10` | Max retries for initial alert config fetch |
| `METRICS_ADDR` | no | `:9090` | Prometheus `/metrics` endpoint |

## Running tests

```bash
# Unit tests
go test ./internal/...

# Integration tests (requires Docker)
go test -tags integration -timeout 5m ./tests/integration/

# Coverage (unit + integration)
go test -tags integration -coverprofile=cover.out -coverpkg=./internal/... ./internal/... ./tests/integration/
go tool cover -html=cover.out
```

The integration suite spins up ephemeral NATS JetStream (mTLS, `verify_and_map`) and TimescaleDB containers via testcontainers-go. No external infrastructure is required.
