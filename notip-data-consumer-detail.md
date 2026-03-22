# NoTIP — `notip-data-consumer` · Detailed Design Reference

*Capitolato C7 — M31 S.r.l. — March 2026*
*Version 1.0 — Authoritative reference for implementors and code agents.*

> **How to read this document.** Every struct, interface, method signature, and relationship is defined here in plain notation. No Go source code is included intentionally — code agents must derive implementations from these specifications, not copy-paste from this document. Field types use Go type names for precision; method signatures follow Go conventions.

---

## 0. System Context — NoTIP Platform

Both Go services (`notip-data-consumer` and `notip-simulator-backend`) operate within the same NoTIP platform. The rules below apply platform-wide and must never be violated by either service.

### 0.1 The Opaque Pipeline

**Rule Zero:** AES-256-GCM encrypted telemetry payloads are **never decrypted server-side**. The fields `EncryptedData`, `IV`, and `AuthTag` travel as opaque base64 blobs from gateway → NATS → Data Consumer → TimescaleDB → frontend. Decryption happens exclusively in the Angular client via the WebCrypto API. Any server-side code that reads, parses, or inspects these fields is a design violation.

### 0.2 Infrastructure Topology

| Component | Role |
|---|---|
| NATS JetStream | All async messaging (telemetry ingestion, alerts, decommission events, gateway status) |
| NATS Request-Reply | Synchronous internal service communication (no HTTP between internal services) |
| TimescaleDB (PostgreSQL extension) | Persistent time-series storage for telemetry blobs |
| Nginx | Sole HTTP entry point for external traffic (ports 80/443) |
| Keycloak | Identity provider; issues JWT tokens for tenants and service accounts |
| Management API (NestJS) | Manages tenants, gateways, alert configurations; exposes REST + NATS RR subjects |
| Provisioning API (NestJS) | Handles gateway factory-key bootstrap, issues mTLS certificates and AES keys |

### 0.3 NATS Subject Conventions

| Subject Pattern | Direction | Description |
|---|---|---|
| `telemetry.data.{tenantId}.{gwId}` | Gateway → JetStream | Encrypted telemetry envelope |
| `alert.{tenantId}.gw_offline` | Data Consumer → JetStream | Gateway-offline alert payload |
| `gateway.decommissioned.{tenantId}.{gwId}` | Management API → JetStream | Decommission event broadcast |
| `internal.mgmt.alert-configs.list` | Data Consumer → Mgmt API (RR) | Fetch all alert configurations |
| `internal.mgmt.gateway.update-status` | Data Consumer → Mgmt API (RR) | Report gateway online/offline status |

### 0.4 Two-Tier Alert Model

- **Tier 1 — Gateway Offline (server-side, persistent):** Detected by the Data Consumer's heartbeat mechanism. Published to JetStream. Stored in the Management API. Survives browser closure.
- **Tier 2 — Sensor Threshold (client-side, visual only):** Evaluated in Angular after client-side decryption. Not server-persisted. Requires an open browser session. Documented with an explicit disclaimer in the frontend.

### 0.5 Architectural Patterns Used in Both Services

| Pattern | Purpose |
|---|---|
| Interface-driven design (Hexagonal / Ports & Adapters) | Every external dependency is behind a Go interface. No concrete type leaks into the domain. |
| Constructor dependency injection | All dependencies passed explicitly at construction time. No globals, no `init()` side effects. |
| Driving ports | Interfaces implemented by the domain; called by adapters to push events in. |
| Driven ports | Interfaces called by the domain; implemented by adapters to reach the outside world. |
| `Clock` abstraction | `time.Now()` is never called directly in domain logic; always injected via `Clock` interface. Enables deterministic tests. |
| Context propagation | Every operation that can block accepts `context.Context` as first parameter. |
| Retry with exponential backoff | Transient NATS and DB failures are retried with capped backoff, not crashed on. |

---

## 1. Service Overview — `notip-data-consumer`

The Data Consumer is a single Go binary with **no HTTP endpoints**. It:

1. Subscribes to `telemetry.data.>` via a JetStream durable consumer and persists every message as an opaque blob into TimescaleDB.
2. Tracks gateway liveness via an in-memory heartbeat map. On each periodic tick it evaluates which gateways have exceeded their configured offline timeout.
3. On an offline transition: publishes a `gw_offline` alert to JetStream and calls `internal.mgmt.gateway.update-status` via NATS RR.
4. On an online recovery (first message after offline): calls `internal.mgmt.gateway.update-status` with `online`.
5. Subscribes to `gateway.decommissioned.>` and removes decommissioned gateways from the heartbeat map.
6. Periodically refreshes alert configuration (per-tenant, per-gateway offline timeouts) from the Management API via NATS RR.

### 1.1 Package Layout

```
notip-data-consumer/
├── cmd/consumer/           Entry point, DI wiring, graceful shutdown
├── internal/
│   ├── config/             Config struct, environment loader
│   ├── domain/
│   │   ├── model/          Value objects, enums (no infrastructure imports)
│   │   └── port/           All driving and driven port interfaces
│   ├── service/            HeartbeatTracker (domain service)
│   ├── adapter/
│   │   ├── driven/         PostgresTelemetryWriter, NATSAlertPublisher,
│   │   │                   NATSGatewayStatusUpdater, AlertConfigCache,
│   │   │                   NATSRRClient, SystemClock
│   │   └── driving/        NATSTelemetryConsumer, NATSDecommissionConsumer,
│   │                       HeartbeatTickTimer
│   └── metrics/            Prometheus metric handles
└── migrations/             TimescaleDB SQL migrations
```

---

## 2. Domain Models — `internal/domain/model`

No infrastructure imports. All types are pure data; no methods except where noted.

---

### `OpaqueBlob` — value object

A named type wrapping a single `string` field (`Value`, base64-encoded bytes). Using a distinct named type rather than a plain `string` makes Rule Zero visible in the type system: any code that attempts to decode or inspect the blob contents is an obvious type violation.

| Field | Type | Notes |
|---|---|---|
| `Value` | `string` | base64-encoded bytes; never decoded server-side |

No methods. Purely a carrier type.

**Composed by:** `TelemetryEnvelope` (×3: `EncryptedData`, `IV`, `AuthTag`) and `TelemetryRow` (×3, same fields).

---

### `TelemetryEnvelope` — value object

The exact wire format of a NATS message published on `telemetry.data.{tenantId}.{gwId}`. Decoded from the raw NATS message body (JSON) by the driving adapter before being passed into the domain. `TenantID` is **not** in the JSON body; it is extracted from the NATS subject by the adapter.

| Field | Type | Source |
|---|---|---|
| `GatewayID` | `string` | JSON body |
| `SensorID` | `string` | JSON body |
| `SensorType` | `string` | JSON body |
| `Timestamp` | `time.Time` | JSON body |
| `KeyVersion` | `int` | JSON body |
| `EncryptedData` | `OpaqueBlob` | JSON body — never inspected |
| `IV` | `OpaqueBlob` | JSON body — never inspected |
| `AuthTag` | `OpaqueBlob` | JSON body — never inspected |

---

### `TelemetryRow` — value object

The normalised record written to TimescaleDB. Derived from `TelemetryEnvelope` plus the `TenantID` extracted from the NATS subject. The `Time` field is the parsed value of `Envelope.Timestamp` used as the hypertable partition key.

| Field | Type | Notes |
|---|---|---|
| `Time` | `time.Time` | Hypertable partition key |
| `TenantID` | `string` | Extracted from NATS subject |
| `GatewayID` | `string` | |
| `SensorID` | `string` | |
| `SensorType` | `string` | |
| `EncryptedData` | `OpaqueBlob` | Passed through opaque |
| `IV` | `OpaqueBlob` | Passed through opaque |
| `AuthTag` | `OpaqueBlob` | Passed through opaque |
| `KeyVersion` | `int` | |

---

### `AlertPayload` — value object

The payload serialised to JSON and published to `alert.{tenantId}.gw_offline` via JetStream when the heartbeat tracker detects an offline transition.

| Field | Type | Notes |
|---|---|---|
| `GatewayID` | `string` | |
| `LastSeen` | `time.Time` | Last heartbeat timestamp |
| `TimeoutMs` | `int64` | Configured offline threshold |
| `Timestamp` | `time.Time` | Moment the alert was generated |

---

### `AlertConfig` — value object

One entry from the Management API's alert configuration list. Represents either a gateway-specific override (`GatewayID` is non-nil) or a tenant-level default (`GatewayID` is nil).

| Field | Type | Notes |
|---|---|---|
| `TenantID` | `string` | |
| `GatewayID` | `*string` | nil = tenant-level default |
| `TimeoutMs` | `int64` | Offline detection threshold in milliseconds |

---

### `GatewayStatusUpdate` — value object

Payload for the `internal.mgmt.gateway.update-status` NATS RR call. Sent by the Data Consumer when a gateway transitions between online and offline.

| Field | Type |
|---|---|
| `GatewayID` | `string` |
| `Status` | `GatewayStatus` |
| `LastSeenAt` | `time.Time` |

---

### `GatewayStatus` — enum

| Constant | String value |
|---|---|
| `Online` | `"online"` |
| `Offline` | `"offline"` |

---

### `HeartbeatEntry` — value object (internal)

One entry per tracked gateway in the heartbeat map. All fields are unexported. Manipulated exclusively by `HeartbeatTracker`. Not exposed outside the `service` package.

| Field | Type |
|---|---|
| `tenantID` | `string` |
| `gatewayID` | `string` |
| `lastSeen` | `time.Time` |
| `knownStatus` | `GatewayStatus` |

---

## 3. Ports — `internal/domain/port`

Interfaces only. No implementation detail belongs here.

---

### 3.1 Driven Ports — called by the domain, implemented by adapters

---

#### `TelemetryWriter`

Persists a single normalised telemetry record. Called by `NATSTelemetryConsumer` (not the domain service itself).

| Method | Parameters | Returns |
|---|---|---|
| `Write` | `ctx context.Context`, `row TelemetryRow` | `error` |

---

#### `AlertPublisher`

Publishes a gateway-offline alert to JetStream.

| Method | Parameters | Returns |
|---|---|---|
| `Publish` | `ctx context.Context`, `tenantID string`, `payload AlertPayload` | `error` |

---

#### `GatewayStatusUpdater`

Sends a gateway status update to the Management API via NATS RR.

| Method | Parameters | Returns |
|---|---|---|
| `UpdateStatus` | `ctx context.Context`, `update GatewayStatusUpdate` | `error` |

---

#### `AlertConfigProvider`

Returns the configured offline timeout for a specific gateway, falling back to the tenant default, then the system default.

| Method | Parameters | Returns |
|---|---|---|
| `TimeoutFor` | `tenantID string`, `gatewayID string` | `int64` (milliseconds) |

---

#### `Clock`

Abstracts `time.Now()`. Injected into all domain code that depends on the current time. Never call `time.Now()` directly in domain or service code.

| Method | Parameters | Returns |
|---|---|---|
| `Now` | — | `time.Time` |

---

### 3.2 Driving Ports — implemented by the domain, called by adapters

---

#### `TelemetryMessageHandler`

Entry point for a decoded and validated telemetry envelope. The `NATSTelemetryConsumer` adapter calls this after parsing the NATS message body.

| Method | Parameters | Returns |
|---|---|---|
| `HandleTelemetry` | `ctx context.Context`, `tenantID string`, `envelope TelemetryEnvelope` | `error` |

---

#### `DecommissionEventHandler`

Entry point for a gateway decommission event arriving from the `gateway.decommissioned.>` JetStream subject.

| Method | Parameters | Returns |
|---|---|---|
| `HandleDecommission` | `tenantID string`, `gatewayID string` | — |

---

#### `HeartbeatTicker`

Entry point for the periodic liveness check. Called by the `HeartbeatTickTimer` adapter on each timer interval.

| Method | Parameters | Returns |
|---|---|---|
| `Tick` | `ctx context.Context` | — |

---

## 4. Domain Service — `internal/service`

---

### `HeartbeatTracker` — service

The entire domain core of `notip-data-consumer`. Owns the in-memory heartbeat map (`map[string]HeartbeatEntry`) and all liveness transition logic. Implements all three driving ports (`TelemetryMessageHandler`, `DecommissionEventHandler`, `HeartbeatTicker`). All side effects (alert publication, status update) are executed through injected driven-port interfaces — never directly.

#### Fields

| Field | Type | Notes |
|---|---|---|
| `beats` | `map[string]HeartbeatEntry` | Keyed by `"tenantID/gatewayID"` |
| `mu` | `sync.RWMutex` | Guards `beats`; read-lock for lookup, write-lock for mutation |
| `startTime` | `time.Time` | Recorded at construction; used to compute the grace period |
| `gracePeriod` | `time.Duration` | No offline alerts are fired during this window after startup |
| `alertPublisher` | `AlertPublisher` | Injected |
| `statusUpdater` | `GatewayStatusUpdater` | Injected |
| `configProvider` | `AlertConfigProvider` | Injected |
| `clock` | `Clock` | Injected |

#### Public Methods (implements driving ports)

| Method | Signature | Implements |
|---|---|---|
| `HandleTelemetry` | `(ctx context.Context, tenantID string, envelope TelemetryEnvelope) error` | `TelemetryMessageHandler` |
| `HandleDecommission` | `(tenantID string, gatewayID string)` | `DecommissionEventHandler` |
| `Tick` | `(ctx context.Context)` | `HeartbeatTicker` |

**`HandleTelemetry` behaviour:**
1. Derives the map key from `tenantID` + `envelope.GatewayID`.
2. Upserts the `HeartbeatEntry` with `lastSeen = clock.Now()`.
3. If the entry previously had status `Offline`, calls `statusUpdater.UpdateStatus` with `Online` and updates `knownStatus`.
4. Does **not** write to TimescaleDB — that is the adapter's responsibility.

**`HandleDecommission` behaviour:**
Deletes the entry for `tenantID/gatewayID` from the map. No side effects.

**`Tick` behaviour:**
1. If `isInGracePeriod()` returns true, returns immediately — no alerts during startup.
2. Acquires read lock, snapshots entries.
3. For each entry, calls `detectTransition` with the entry, `clock.Now()`, and the timeout from `configProvider.TimeoutFor`.
4. For any detected `Offline` transition: publishes alert via `alertPublisher.Publish`, then calls `statusUpdater.UpdateStatus` with `Offline`, then updates `knownStatus` in the map under write lock.

#### Private Methods

| Method | Parameters | Returns | Purpose |
|---|---|---|---|
| `isInGracePeriod` | — | `bool` | Compares `clock.Now()` against `startTime + gracePeriod` |
| `beatKey` | `tenantID string`, `gatewayID string` | `string` | Encodes map key as `"tenantID/gatewayID"` |
| `detectTransition` | `entry HeartbeatEntry`, `now time.Time`, `timeoutMs int64` | `*GatewayStatus` | Returns nil if no transition; returns pointer to new status otherwise |
| `dispatchStatusUpdate` | `ctx context.Context`, `entry HeartbeatEntry`, `newStatus GatewayStatus` | — | Non-blocking dispatch to avoid blocking Tick on slow RR calls |

---

## 5. Driven Adapters — `internal/adapter/driven`

Concrete implementations of driven ports. May import infrastructure packages. Invisible to the domain.

---

### `PostgresTelemetryWriter`

Implements `TelemetryWriter`. Writes a `TelemetryRow` verbatim (all three opaque blob fields are written as-is) to the TimescaleDB hypertable using a `pgxpool.Pool`. The hypertable is partitioned by the `time` column (mapped from `TelemetryRow.Time`).

| Field | Type |
|---|---|
| `pool` | `*pgxpool.Pool` |

| Method | Signature |
|---|---|
| `Write` | `(ctx context.Context, row TelemetryRow) error` |
| `Close` | `()` |

Implements: `TelemetryWriter`

---

### `NATSAlertPublisher`

Implements `AlertPublisher`. Serialises `AlertPayload` to JSON and publishes to `alert.{tenantId}.gw_offline` via JetStream. The subject is assembled at publish time by interpolating `tenantID`.

| Field | Type |
|---|---|
| `js` | `nats.JetStreamContext` |

| Method | Signature |
|---|---|
| `Publish` | `(ctx context.Context, tenantID string, payload AlertPayload) error` |

Implements: `AlertPublisher`

---

### `NATSGatewayStatusUpdater`

Implements `GatewayStatusUpdater`. Issues `internal.mgmt.gateway.update-status` as a NATS Request-Reply. Serialises `GatewayStatusUpdate` to JSON as the request body. The Management API is expected to acknowledge with a success/error response.

| Field | Type |
|---|---|
| `nc` | `*nats.Conn` |
| `requestTimeout` | `time.Duration` |

| Method | Signature |
|---|---|
| `UpdateStatus` | `(ctx context.Context, update GatewayStatusUpdate) error` |

Implements: `GatewayStatusUpdater`

---

### `AlertConfigCache`

Implements `AlertConfigProvider`. Maintains a periodically-refreshed, atomically-swapped in-memory snapshot of all alert configurations fetched from the Management API via NATS RR. The snapshot is stored as an `atomic.Pointer[alertConfigSnapshot]` so reads are always lock-free.

Lookup priority in `TimeoutFor`: gateway-specific config → tenant default → `defaultTimeoutMs`.

| Field | Type | Notes |
|---|---|---|
| `snapshot` | `atomic.Pointer[alertConfigSnapshot]` | Swapped atomically on each successful refresh |
| `rrClient` | `NATSRRClient` | Used to fetch config from Management API |
| `defaultTimeoutMs` | `int64` | Fallback when no config exists |
| `refreshInterval` | `time.Duration` | Default: 5 minutes |
| `maxRetries` | `int` | Default: 10; uses exponential backoff |

| Method | Signature | Notes |
|---|---|---|
| `TimeoutFor` | `(tenantID string, gatewayID string) int64` | Lock-free; reads from atomic pointer |
| `Run` | `(ctx context.Context)` | Background goroutine; refresh loop |
| `refresh` | `(ctx context.Context) error` | Fetches fresh config and atomically swaps snapshot |
| `fetchWithBackoff` | `(ctx context.Context) ([]AlertConfig, error)` | Retries up to `maxRetries` with backoff |

Implements: `AlertConfigProvider`

---

### `alertConfigSnapshot` — internal value object

Holds a point-in-time immutable copy of all alert configs. Replaced atomically on each successful refresh. Never mutated in place.

| Field | Type | Keys |
|---|---|---|
| `byGateway` | `map[string]AlertConfig` | Key: `"tenantID/gatewayID"` |
| `byTenant` | `map[string]AlertConfig` | Key: `tenantID` |
| `fetchedAt` | `time.Time` | Timestamp of last successful fetch |

---

### `NATSRRClient`

Not a port — a shared infrastructure helper used by both `AlertConfigCache` and `NATSGatewayStatusUpdater`. Encapsulates NATS Request-Reply mechanics (timeout, serialisation, error handling) so adapters do not duplicate them.

| Field | Type |
|---|---|
| `nc` | `*nats.Conn` |
| `timeout` | `time.Duration` |

| Method | Signature |
|---|---|
| `FetchAlertConfigs` | `(ctx context.Context) ([]AlertConfig, error)` |
| `UpdateGatewayStatus` | `(ctx context.Context, update GatewayStatusUpdate) error` |

Aggregated by: `AlertConfigCache`, `NATSGatewayStatusUpdater`

---

### `SystemClock`

Implements `Clock`. A trivial stateless adapter that delegates to the real system clock. Exists solely to satisfy the `Clock` interface in production wiring, allowing tests to inject a controlled clock.

| Method | Signature |
|---|---|
| `Now` | `() time.Time` |

Implements: `Clock`

---

## 6. Driving Adapters — `internal/adapter/driving`

Translate external events into domain calls through driving ports.

---

### `NATSTelemetryConsumer`

Subscribes to `telemetry.data.>` as a JetStream durable consumer. For each NATS message:

1. Parses the JSON body into a `TelemetryEnvelope`.
2. Extracts `tenantID` from the NATS subject (segment 3 of `telemetry.data.{tenantId}.{gwId}`).
3. Calls `TelemetryMessageHandler.HandleTelemetry` (heartbeat registration / online recovery).
4. Builds a `TelemetryRow` and calls `TelemetryWriter.Write` (persistence).
5. On success: ACK the NATS message.
6. On error: NAK with backoff for transient failures; term for parse failures.

| Field | Type |
|---|---|
| `js` | `nats.JetStreamContext` |
| `handler` | `TelemetryMessageHandler` |
| `writer` | `TelemetryWriter` |
| `durableName` | `string` |
| `metrics` | `*Metrics` |

| Method | Signature | Notes |
|---|---|---|
| `Run` | `(ctx context.Context) error` | Blocking; returns when ctx is cancelled |
| `processMessage` | `(msg *nats.Msg) error` | Orchestrates steps 1–4 above |
| `extractTenantID` | `(subject string) (string, error)` | Parses NATS subject |
| `buildRow` | `(tenantID string, envelope TelemetryEnvelope) TelemetryRow` | Maps envelope + tenantID → row |

Depends on: `TelemetryMessageHandler` (driving), `TelemetryWriter` (driven, called directly)

---

### `NATSDecommissionConsumer`

Subscribes to `gateway.decommissioned.>` via JetStream. For each message, extracts `tenantID` and `gatewayID` from the subject and calls `DecommissionEventHandler.HandleDecommission`.

| Field | Type |
|---|---|
| `js` | `nats.JetStreamContext` |
| `handler` | `DecommissionEventHandler` |

| Method | Signature |
|---|---|
| `Run` | `(ctx context.Context) error` |
| `extractIDs` | `(subject string) (tenantID string, gatewayID string, err error)` |

Depends on: `DecommissionEventHandler` (driving)

---

### `HeartbeatTickTimer`

Owns a `time.Ticker` that fires at a configured interval. On each tick calls `HeartbeatTicker.Tick`. Stops cleanly on context cancellation.

| Field | Type |
|---|---|
| `ticker` | `HeartbeatTicker` |
| `interval` | `time.Duration` |

| Method | Signature |
|---|---|
| `Run` | `(ctx context.Context)` |

Depends on: `HeartbeatTicker` (driving)

---

## 7. Cross-Cutting — `internal/config` and `internal/metrics`

---

### `Config` — value object

(DA AGGIORNARE)

All configuration is loaded from environment variables at startup. No hot-reload. Missing required fields are a hard startup failure.

| Field | Type | Default | Notes |
|---|---|---|---|
| `NATSUrl` | `string` | required | |
| `NATSCredentialsPath` | `string` | required | Path to `.creds` file for NATS auth |
| `NATSConsumerDurableName` | `string` | `"data-consumer-telemetry"` | JetStream durable consumer name |
| `NATSConnectTimeoutSeconds` | `int` | `10` | |
| `DatabaseURL` | `string` | required | PostgreSQL connection string |
| `DBMaxConns` | `int` | `10` | pgxpool max connections |
| `DBMinConns` | `int` | `2` | pgxpool min connections |
| `HeartbeatTickMs` | `int` | `10000` | Interval between heartbeat ticks (ms) |
| `HeartbeatGracePeriodSeconds` | `int` | `120` | Startup grace period before offline alerts fire |
| `AlertConfigRefreshMs` | `int` | `300000` | How often to refresh alert configs from Management API |
| `AlertConfigDefaultTimeoutMs` | `int64` | `60000` | Fallback timeout when no config entry exists |
| `AlertConfigMaxRetries` | `int` | `10` | Max retries for alert config fetch on startup |
| `MetricsAddr` | `string` | `":9090"` | Prometheus `/metrics` endpoint address |

---

### `ConfigLoader` — factory

| Method | Signature |
|---|---|
| `Load` | `() (*Config, error)` |

Creates: `Config`

---

### `Metrics` — value object

All Prometheus metric handles. Constructed once in `main` and injected into adapters that emit metrics. All counters/histograms are registered against the default Prometheus registry.

| Field | Type | Description |
|---|---|---|
| `MessagesReceived` | `prometheus.Counter` | NATS messages dequeued |
| `MessagesWritten` | `prometheus.Counter` | Successful TimescaleDB writes |
| `WriteErrors` | `prometheus.Counter` | Failed TimescaleDB writes |
| `WriteLatency` | `prometheus.Histogram` | TimescaleDB write duration |
| `AlertsPublished` | `prometheus.Counter` | Gateway-offline alerts sent |
| `AlertPublishErrors` | `prometheus.Counter` | |
| `StatusUpdateErrors` | `prometheus.Counter` | Failed NATS RR status updates |
| `StatusUpdateDropped` | `prometheus.Counter` | Status updates dropped (non-blocking dispatch) |
| `AlertCacheRefreshErrors` | `prometheus.Counter` | |
| `NATSReconnects` | `prometheus.Counter` | |
| `HeartbeatMapSize` | `prometheus.Gauge` | Number of gateways currently tracked |

---

## 8. Startup Sequence (`cmd/consumer/main.go`)

The following steps must execute in order. A failure at any blocking step terminates the process.

| Step | Component | Action | Blocking? |
|---|---|---|---|
| 1 | `ConfigLoader` | Load and validate config from environment | Yes — crash on missing required fields |
| 2 | NATS | Connect to NATS with credentials and TLS; configure reconnect handler | Yes — retry with backoff, no timeout cap |
| 3 | `pgxpool` | Create TimescaleDB connection pool | Yes — crash if unreachable |
| 4 | `AlertConfigCache.Run` | Fetch alert configs; retry up to `maxRetries` with backoff; initialise snapshot | No — uses default if all retries fail |
| 5 | `HeartbeatTickTimer` | Launch background goroutine | No |
| 6 | `AlertConfigCache` | Launch background refresh goroutine | No |
| 7 | `NATSDecommissionConsumer` | Launch background goroutine | No |
| 8 | `NATSTelemetryConsumer.Run` | Start JetStream consumer (blocks main goroutine) | Yes |
| — | Signal handler | On SIGTERM/SIGINT: cancel root context, NATS drain, pool close | — |

---

## 9. Relationship Summary

| From | Relationship | To |
|---|---|---|
| `HeartbeatTracker` | implements | `TelemetryMessageHandler` |
| `HeartbeatTracker` | implements | `DecommissionEventHandler` |
| `HeartbeatTracker` | implements | `HeartbeatTicker` |
| `HeartbeatTracker` | depends on | `AlertPublisher` |
| `HeartbeatTracker` | depends on | `GatewayStatusUpdater` |
| `HeartbeatTracker` | depends on | `AlertConfigProvider` |
| `HeartbeatTracker` | depends on | `Clock` |
| `HeartbeatTracker` | composes | `HeartbeatEntry` (0..*) |
| `PostgresTelemetryWriter` | implements | `TelemetryWriter` |
| `NATSAlertPublisher` | implements | `AlertPublisher` |
| `NATSGatewayStatusUpdater` | implements | `GatewayStatusUpdater` |
| `AlertConfigCache` | implements | `AlertConfigProvider` |
| `AlertConfigCache` | depends on | `NATSRRClient` |
| `AlertConfigCache` | composes | `alertConfigSnapshot` |
| `SystemClock` | implements | `Clock` |
| `NATSTelemetryConsumer` | depends on | `TelemetryMessageHandler` |
| `NATSTelemetryConsumer` | depends on | `TelemetryWriter` |
| `NATSDecommissionConsumer` | depends on | `DecommissionEventHandler` |
| `HeartbeatTickTimer` | depends on | `HeartbeatTicker` |
| `NATSRRClient` | aggregated by | `AlertConfigCache` |
| `NATSRRClient` | aggregated by | `NATSGatewayStatusUpdater` |
| `TelemetryEnvelope` | composes | `OpaqueBlob` (×3) |
| `TelemetryRow` | composes | `OpaqueBlob` (×3) |

