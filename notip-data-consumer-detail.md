# NoTIP — `notip-data-consumer` · Detailed Design Reference

*Capitolato C7 — M31 S.r.l. — March 2026*
*Version 3.0 — Authoritative reference for implementors and code agents.*

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
| `alert.gw_offline.{tenantId}` | Data Consumer → JetStream | Gateway-offline alert payload |
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
| Narrow metric interfaces | Each adapter defines its own minimal metric interface. The full `Metrics` struct satisfies all of them, keeping adapters decoupled. |
| `ClockProvider` abstraction | `time.Now()` is never called directly in domain logic; always injected via `ClockProvider` interface. Enables deterministic tests. |
| Context propagation | Every operation that can block accepts `context.Context` as first parameter. |
| Retry with exponential backoff | Transient NATS and DB failures are retried with capped backoff, not crashed on. |
| Non-blocking dispatch | Status updates are queued to a bounded channel and processed by a background worker, preventing slow NATS RR calls from blocking the heartbeat tick or telemetry handler. |

---

## 1. Service Overview — `notip-data-consumer`

The Data Consumer is a single Go binary exposing one HTTP server with **two endpoints**: Prometheus `/metrics` and health `/healthz`. It:

1. Subscribes to `telemetry.data.>` via a JetStream durable consumer and batch-persists every message as an opaque blob into TimescaleDB.
2. Tracks gateway liveness via an in-memory heartbeat map. On each periodic tick it evaluates which gateways have exceeded their configured offline timeout.
3. On an offline transition: publishes a `gw_offline` alert to JetStream and dispatches an asynchronous `internal.mgmt.gateway.update-status` call via NATS RR with `offline`.
4. On first-seen or online recovery (first message after offline): dispatches an asynchronous `internal.mgmt.gateway.update-status` with `online`.
5. Subscribes to `gateway.decommissioned.>` and removes decommissioned gateways from the heartbeat map.
6. Periodically refreshes alert configuration (per-tenant, per-gateway offline timeouts) from the Management API via NATS RR.

### 1.1 Package Layout

```
notip-data-consumer/
├── cmd/consumer/           Entry point, DI wiring, graceful shutdown
├── internal/
│   ├── config/             Config struct, environment loader, DSN builder
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
│   └── metrics/            Prometheus metric handles and narrow-interface methods
├── tests/
│   └── integration/        Testcontainers-based integration tests (NATS mTLS, TimescaleDB)
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

**Methods:** Custom `MarshalJSON` / `UnmarshalJSON` that serialise the `Value` as a plain JSON string, ensuring the raw base64 payload is preserved verbatim through any JSON round-trip.

**Composed by:** `TelemetryEnvelope` (×3: `EncryptedData`, `IV`, `AuthTag`) and `TelemetryRow` (×3, same fields).

---

### `TelemetryEnvelope` — value object

The exact wire format of a NATS message published on `telemetry.data.{tenantId}.{gwId}`. Decoded from the raw NATS message body (JSON) by the driving adapter before being passed into the domain. `TenantID` is **not** in the JSON body; it is extracted from the NATS subject by the adapter.

| Field | Type | JSON tag | Source |
|---|---|---|---|
| `GatewayID` | `string` | `gatewayId` | JSON body |
| `SensorID` | `string` | `sensorId` | JSON body |
| `SensorType` | `SensorType` | `sensorType` | JSON body |
| `Timestamp` | `time.Time` | `timestamp` | JSON body |
| `KeyVersion` | `int` | `keyVersion` | JSON body |
| `EncryptedData` | `OpaqueBlob` | `encryptedData` | JSON body — never inspected |
| `IV` | `OpaqueBlob` | `iv` | JSON body — never inspected |
| `AuthTag` | `OpaqueBlob` | `authTag` | JSON body — never inspected |

---

### `SensorType` — enum

Named string type. Constrains `TelemetryEnvelope.SensorType` to the five values defined in the AsyncAPI contract.

| Constant | String value |
|---|---|
| `SensorTypeTemperature` | `"temperature"` |
| `SensorTypeHumidity` | `"humidity"` |
| `SensorTypeMovement` | `"movement"` |
| `SensorTypePressure` | `"pressure"` |
| `SensorTypeBiometric` | `"biometric"` |

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

The payload serialised to JSON and published to `alert.gw_offline.{tenantId}` via JetStream when the heartbeat tracker detects an offline transition.

| Field | Type | JSON tag | Notes |
|---|---|---|---|
| `GatewayID` | `string` | `gatewayId` | |
| `LastSeen` | `time.Time` | `lastSeen` | Last heartbeat timestamp |
| `TimeoutMs` | `int64` | `timeoutMs` | Configured offline threshold |
| `Timestamp` | `time.Time` | `timestamp` | Moment the alert was generated |

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

| Field | Type | JSON tag |
|---|---|---|
| `GatewayID` | `string` | `gateway_id` |
| `Status` | `GatewayStatus` | `status` |
| `LastSeenAt` | `time.Time` | `last_seen_at` |

---

### `GatewayStatus` — enum

| Constant | String value |
|---|---|
| `Offline` | `"offline"` |
| `Online` | `"online"` |

---

### `gatewayKey` — struct (internal to `service` package)

Composite map key for the heartbeat map. Using a struct key instead of a formatted string avoids allocation and prevents key-collision ambiguity.

| Field | Type |
|---|---|
| `tenantID` | `string` |
| `gatewayID` | `string` |

---

### `heartbeatEntry` — value object (internal to `service` package)

One entry per tracked gateway in the heartbeat map. All fields are unexported. Manipulated exclusively by `HeartbeatTracker`. Not exposed outside the `service` package.

| Field | Type |
|---|---|
| `tenantID` | `string` |
| `gatewayID` | `string` |
| `lastSeen` | `time.Time` |
| `knownStatus` | `GatewayStatus` |

---

### `statusUpdateJob` — value object (internal to `service` package)

Wraps a `GatewayStatusUpdate` for the asynchronous dispatch channel.

| Field | Type |
|---|---|
| `update` | `GatewayStatusUpdate` |

---

## 3. Ports — `internal/domain/port`

Interfaces only. No implementation detail belongs here.

---

### 3.1 Driven Ports — called by the domain, implemented by adapters

---

#### `TelemetryWriter`

Persists normalised telemetry records. Called by `NATSTelemetryConsumer` (not the domain service itself).

| Method | Parameters | Returns |
|---|---|---|
| `Write` | `ctx context.Context`, `row TelemetryRow` | `error` |
| `WriteBatch` | `ctx context.Context`, `rows []TelemetryRow` | `error` |

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

#### `ClockProvider`

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

The entire domain core of `notip-data-consumer`. Owns the in-memory heartbeat map (`map[gatewayKey]*heartbeatEntry`) and all liveness transition logic. Implements all three driving ports (`TelemetryMessageHandler`, `DecommissionEventHandler`, `HeartbeatTicker`). All side effects (alert publication, status update) are executed through injected driven-port interfaces — never directly. Status updates are dispatched asynchronously via a bounded channel to a background worker goroutine, keeping `Tick` and `HandleTelemetry` non-blocking.

#### Narrow Metric Interface

`HeartbeatTrackerMetrics` — the subset of metrics emitted by the tracker. Keeps the service decoupled from the full `Metrics` struct.

| Method | Returns |
|---|---|
| `IncStatusUpdateDropped()` | — |
| `SetHeartbeatMapSize(v float64)` | — |

#### Fields

| Field | Type | Notes |
|---|---|---|
| `clock` | `ClockProvider` | Injected |
| `alertPublisher` | `AlertPublisher` | Injected |
| `statusUpdater` | `GatewayStatusUpdater` | Injected |
| `configProvider` | `AlertConfigProvider` | Injected |
| `metrics` | `HeartbeatTrackerMetrics` | Injected |
| `startTime` | `time.Time` | Recorded at construction via `clock.Now()`; used to compute the grace period |
| `gracePeriod` | `time.Duration` | No offline alerts are fired during this window after startup |
| `mu` | `sync.RWMutex` | Guards `beats`; read-lock for snapshot, write-lock for mutation |
| `beats` | `map[gatewayKey]*heartbeatEntry` | Composite struct key `{tenantID, gatewayID}` |
| `dispatchCh` | `chan statusUpdateJob` | Bounded async queue for status updates |
| `done` | `chan struct{}` | Closed when `dispatchWorker` exits |
| `closeOnce` | `sync.Once` | Ensures `Close()` runs only once |

#### Constructor

```
NewHeartbeatTracker(clock, alertPublisher, statusUpdater, configProvider, metrics, statusUpdateBufSize int, gracePeriod time.Duration) *HeartbeatTracker
```

Initialises the empty heartbeat map, creates the dispatch channel with `statusUpdateBufSize` capacity, and spawns the background `dispatchWorker` goroutine.

#### Public Methods (implements driving ports)

| Method | Signature | Implements |
|---|---|---|
| `HandleTelemetry` | `(ctx context.Context, tenantID string, envelope TelemetryEnvelope) error` | `TelemetryMessageHandler` |
| `HandleDecommission` | `(tenantID string, gatewayID string)` | `DecommissionEventHandler` |
| `Tick` | `(ctx context.Context)` | `HeartbeatTicker` |
| `Close` | `()` | — |

**`HandleTelemetry` behaviour:**
1. Derives the composite key `gatewayKey{tenantID, envelope.GatewayID}`.
2. Acquires write lock.
3. If entry does **not** exist: creates a new entry with `lastSeen = clock.Now()`, `knownStatus = Online`, updates heartbeat map size metric, dispatches an `Online` status update, and returns.
4. If entry exists: updates `lastSeen = clock.Now()`.
5. If the entry previously had status `Offline`: transitions to `Online` and dispatches an `Online` status update.
6. Does **not** write to TimescaleDB — that is the adapter's responsibility.

**`HandleDecommission` behaviour:**
Acquires write lock. Deletes the entry for `gatewayKey{tenantID, gatewayID}` from the map. Updates heartbeat map size metric. No other side effects.

**`Tick` behaviour (three-phase approach):**
1. **Grace period check:** If `clock.Now()` is before `startTime + gracePeriod`, returns immediately — no alerts during startup.
2. **Phase 1 — RLock snapshot:** Acquires read lock, copies all entries into a local snapshot slice (value copies), releases read lock immediately to minimise contention.
3. **Phase 2 — I/O outside the lock:** For each entry in the snapshot:
   - If `knownStatus == Offline`: skip (no repeat alerts).
   - Fetches configured timeout via `configProvider.TimeoutFor`.
   - If `clock.Now()` is before `lastSeen + timeout`: skip (not yet timed out).
   - Publishes alert via `alertPublisher.Publish`.
   - Dispatches `Offline` status update via the async channel.
4. **Phase 3 — WLock state update with re-validation:** Acquires write lock. Re-reads the real entry from the map. If the entry's `lastSeen` has advanced since the snapshot (telemetry arrived during I/O), aborts the state update (gateway recovered). Otherwise, marks `knownStatus = Offline`.

**`Close` behaviour:**
Closes the dispatch channel (guarded by `sync.Once` — safe to call multiple times). Waits for the `dispatchWorker` goroutine to drain and exit.

#### Private Methods

| Method | Parameters | Returns | Purpose |
|---|---|---|---|
| `dispatchStatusUpdate` | `update GatewayStatusUpdate` | — | Non-blocking send to `dispatchCh`; drops and increments `StatusUpdateDropped` metric if channel is full |
| `dispatchWorker` | — | — | Background goroutine; serially processes status updates from `dispatchCh`; closes `done` on exit |

---

## 5. Driven Adapters — `internal/adapter/driven`

Concrete implementations of driven ports. May import infrastructure packages. Invisible to the domain. Each adapter defines its own narrow metric interface to avoid coupling to the full `Metrics` struct.

---

### `PostgresTelemetryWriter`

Implements `TelemetryWriter`. Writes `TelemetryRow` records verbatim (all three opaque blob fields are written as-is) to the TimescaleDB hypertable. Depends on a `dbPool` interface (satisfied by `*pgxpool.Pool`).

| Field | Type |
|---|---|
| `pool` | `dbPool` |

**`dbPool` interface:**

| Method | Signature |
|---|---|
| `Exec` | `(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)` |
| `SendBatch` | `(ctx context.Context, b *pgx.Batch) pgx.BatchResults` |
| `Close` | `()` |

| Method | Signature | Notes |
|---|---|---|
| `Write` | `(ctx context.Context, row TelemetryRow) error` | Single INSERT via `pool.Exec` |
| `WriteBatch` | `(ctx context.Context, rows []TelemetryRow) error` | Constructs a `pgx.Batch` with one INSERT per row; single network round-trip via `pool.SendBatch`; early failure on first row error; no-op on empty slice |
| `Close` | `()` | Releases all pool connections |

Implements: `TelemetryWriter`

---

### `NATSAlertPublisher`

Implements `AlertPublisher`. Serialises `AlertPayload` to JSON and publishes to `alert.gw_offline.{tenantId}` via JetStream. The subject is assembled at publish time by interpolating `tenantID`. Depends on a `natsJSPublisher` interface (satisfied by `nats.JetStreamContext`).

| Field | Type |
|---|---|
| `js` | `natsJSPublisher` |
| `metrics` | `alertPublisherMetrics` |

**`natsJSPublisher` interface:**

| Method | Signature |
|---|---|
| `Publish` | `(subj string, data []byte, opts ...nats.PubOpt) (*nats.PubAck, error)` |

**`alertPublisherMetrics` interface:**

| Method | Returns |
|---|---|
| `IncAlertsPublished()` | — |
| `IncAlertPublishErrors()` | — |

| Method | Signature |
|---|---|
| `Publish` | `(ctx context.Context, tenantID string, payload AlertPayload) error` |

Context is accepted for interface compliance but not threaded into `js.Publish` — the synchronous JetStream Publish API does not support per-call context cancellation.

Implements: `AlertPublisher`

---

### `NATSGatewayStatusUpdater`

Implements `GatewayStatusUpdater`. Delegates to a `gatewayStatusUpdateCaller` interface (satisfied by `NATSRRClient`) for the actual NATS RR mechanics. Increments the error metric on failures.

| Field | Type |
|---|---|
| `client` | `gatewayStatusUpdateCaller` |
| `metrics` | `statusUpdateErrRecorder` |

**`gatewayStatusUpdateCaller` interface:**

| Method | Signature |
|---|---|
| `UpdateGatewayStatus` | `(ctx context.Context, update GatewayStatusUpdate) error` |

**`statusUpdateErrRecorder` interface:**

| Method | Returns |
|---|---|
| `IncStatusUpdateErrors()` | — |

| Method | Signature |
|---|---|
| `UpdateStatus` | `(ctx context.Context, update GatewayStatusUpdate) error` |

Implements: `GatewayStatusUpdater`

---

### `AlertConfigCache`

Implements `AlertConfigProvider`. Maintains a periodically-refreshed, atomically-swapped in-memory snapshot of all alert configurations fetched from the Management API via NATS RR. The snapshot is stored as an `atomic.Pointer[alertConfigSnapshot]` so reads are always lock-free. Constructor initialises with an empty snapshot so `TimeoutFor` never reads a nil pointer.

Lookup priority in `TimeoutFor`: gateway-specific config → tenant default → `defaultTimeoutMs`.

Depends on an `alertConfigFetcher` interface (satisfied by `NATSRRClient`).

| Field | Type | Notes |
|---|---|---|
| `snapshot` | `atomic.Pointer[alertConfigSnapshot]` | Swapped atomically on each successful refresh |
| `rrClient` | `alertConfigFetcher` | Used to fetch config from Management API |
| `metrics` | `alertCacheErrRecorder` | Error counter for failed refreshes |
| `defaultTimeoutMs` | `int64` | Fallback when no config exists |
| `refreshInterval` | `time.Duration` | Default: 5 minutes |
| `maxRetries` | `int` | Default: 10; uses exponential backoff |

**`alertConfigFetcher` interface:**

| Method | Signature |
|---|---|
| `FetchAlertConfigs` | `(ctx context.Context) ([]AlertConfig, error)` |

**`alertCacheErrRecorder` interface:**

| Method | Returns |
|---|---|
| `IncAlertCacheRefreshErrors()` | — |

| Method | Signature | Notes |
|---|---|---|
| `TimeoutFor` | `(tenantID string, gatewayID string) int64` | Lock-free; reads from atomic pointer |
| `Run` | `(ctx context.Context)` | Blocking; performs initial fetch with backoff, then enters refresh loop |
| `refresh` | `(ctx context.Context) error` | Fetches fresh config and atomically swaps snapshot |
| `fetchWithBackoff` | `(ctx context.Context) error` | Retries up to `maxRetries` with exponential backoff (1 s → 30 s cap); increments error metric on each failed attempt |

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

Not a port — a shared infrastructure helper used by both `AlertConfigCache` (via `alertConfigFetcher`) and `NATSGatewayStatusUpdater` (via `gatewayStatusUpdateCaller`). Encapsulates NATS Request-Reply mechanics (timeout, serialisation, error handling) so adapters do not duplicate them. Depends on a `natsRequester` interface (satisfied by `*nats.Conn`).

| Field | Type | Notes |
|---|---|---|
| `nc` | `natsRequester` | |
| `timeout` | `time.Duration` | Applied per-attempt |
| `maxRetries` | `int` | Default: 3 |
| `backoff` | `[]time.Duration` | Default: `[1s, 2s, 4s]` |
| `sleep` | `func(time.Duration)` | Injected for test control; production uses `time.Sleep` |

**`natsRequester` interface:**

| Method | Signature |
|---|---|
| `RequestWithContext` | `(ctx context.Context, subj string, data []byte) (*nats.Msg, error)` |

| Method | Signature |
|---|---|
| `FetchAlertConfigs` | `(ctx context.Context) ([]AlertConfig, error)` |
| `UpdateGatewayStatus` | `(ctx context.Context, update GatewayStatusUpdate) error` |

Each method delegates to `requestWithRetry`, which applies the retry policy: up to `maxRetries` retries (default 3), with a fresh per-attempt context timeout (`timeout`, default 5s), exponential backoff from `backoff` slice (default 1s/2s/4s), and context-cancellation checks between retries.

`FetchAlertConfigs` sends a nil-body request to `internal.mgmt.alert-configs.list` and deserialises the JSON array response into `[]AlertConfig`.

`UpdateGatewayStatus` serialises the update to JSON, sends to `internal.mgmt.gateway.update-status`, deserialises the `GatewayStatusUpdateResponse` and returns an error if `success` is false.

Aggregated by: `AlertConfigCache`, `NATSGatewayStatusUpdater`

---

### `SystemClock`

Implements `ClockProvider`. A trivial stateless adapter that delegates to the real system clock. Exists solely to satisfy the `ClockProvider` interface in production wiring, allowing tests to inject a controlled clock.

| Method | Signature |
|---|---|
| `Now` | `() time.Time` |

Implements: `ClockProvider`

---

## 6. Driving Adapters — `internal/adapter/driving`

Translate external events into domain calls through driving ports. Both `NATSTelemetryConsumer` and `NATSDecommissionConsumer` depend on a shared `natsJSSubscriber` interface (satisfied by `nats.JetStreamContext`).

**`natsJSSubscriber` interface:**

| Method | Signature |
|---|---|
| `Subscribe` | `(subj string, cb nats.MsgHandler, opts ...nats.SubOpt) (*nats.Subscription, error)` |

---

### `NATSTelemetryConsumer`

Subscribes to `telemetry.data.>` as a JetStream durable consumer. Messages are buffered and flushed to TimescaleDB in batches for throughput. For each NATS message:

1. Increments `MessagesReceived` metric.
2. Calls `processMessage` which: parses the JSON body into a `TelemetryEnvelope`, extracts `tenantID` from the NATS subject, calls `TelemetryMessageHandler.HandleTelemetry` (heartbeat registration / online recovery), and builds a `TelemetryRow`.
3. Sends the result to a pending channel for batch processing.
4. The `flushLoop` accumulates pending messages and flushes via `WriteBatch` when: the buffer reaches `batchSize`, a `flushEvery` timer fires, or ctx is cancelled (final flush).
5. After `WriteBatch`: ACK each successful message, NAK with 5 s delay for transient write failures, Term for permanent parse errors.

**Error classification:** Parse failures (bad subject, invalid JSON) are wrapped in `permanentError` and Term()'d — NATS will not redeliver. Handler errors are treated as transient and NAK()'d with delay.

| Field | Type |
|---|---|
| `js` | `natsJSSubscriber` |
| `handler` | `TelemetryMessageHandler` |
| `writer` | `TelemetryWriter` |
| `metrics` | `telemetryConsumerMetrics` |
| `durableName` | `string` |
| `batchSize` | `int` |
| `flushEvery` | `time.Duration` |

**`telemetryConsumerMetrics` interface:**

| Method | Returns |
|---|---|
| `IncMessagesReceived()` | — |
| `IncMessagesWritten()` | — |
| `IncWriteErrors()` | — |
| `ObserveWriteLatency(d time.Duration)` | — |

| Method | Signature | Notes |
|---|---|---|
| `Run` | `(ctx context.Context) error` | Blocking; returns when ctx is cancelled |
| `processMessage` | `(ctx context.Context, msg *nats.Msg) (TelemetryRow, error)` | Orchestrates parse → handle → build; returns `permanentError` or transient error |
| `flushLoop` | `(ctx context.Context, pending <-chan pendingMsg)` | Batches messages; three-trigger flush |
| `writeBatch` | `(ctx context.Context, batch []pendingMsg)` | Separates permanent errors from valid rows; calls `WriteBatch`; ACK/NAK/Term per message |
| `extractTenantID` | `(subject string) (string, error)` | Parses NATS subject |
| `buildRow` | `(tenantID string, envelope TelemetryEnvelope) TelemetryRow` | Maps envelope + tenantID → row |

**Internal types:**

`permanentError` — wraps an error to signal non-retryable failures.

`pendingMsg` — pairs a `msgAcknowledger` interface (satisfied by `*nats.Msg`) with its decoded `TelemetryRow` and any error. Enables the flush loop to ACK/NAK the original NATS message after batch write.

Depends on: `TelemetryMessageHandler` (driving), `TelemetryWriter` (driven, called directly)

---

### `NATSDecommissionConsumer`

Subscribes to `gateway.decommissioned.>` via JetStream with manual ACK. For each message, extracts `tenantID` and `gatewayID` from the subject (expects exactly 4 dot-separated parts) and calls `DecommissionEventHandler.HandleDecommission`. Parse errors are Term()'d.

| Field | Type |
|---|---|
| `js` | `natsJSSubscriber` |
| `handler` | `DecommissionEventHandler` |

| Method | Signature |
|---|---|
| `Run` | `(ctx context.Context) error` |
| `handleMsg` | `(msg *nats.Msg)` |
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

All configuration is loaded from environment variables at startup. No hot-reload. Missing required fields are a hard startup failure. The loader uses a single-pass error accumulation pattern — subsequent env var reads are no-ops once the first error is set.

| Field | Type | Env Var | Default | Required? |
|---|---|---|---|---|
| `NATSUrl` | `string` | `NATS_URL` | — | Yes |
| `NATSTlsCa` | `string` | `NATS_TLS_CA` | — | Yes |
| `NATSTlsCert` | `string` | `NATS_TLS_CERT` | — | Yes |
| `NATSTlsKey` | `string` | `NATS_TLS_KEY` | — | Yes |
| `NATSConsumerDurableName` | `string` | `NATS_CONSUMER_DURABLE_NAME` | `"data-consumer-telemetry"` | No |
| `NATSConnectTimeoutSeconds` | `int` | `NATS_CONNECT_TIMEOUT_SECONDS` | `10` | No |
| `DBHost` | `string` | `DB_HOST` | — | Yes |
| `DBPort` | `int` | `DB_PORT` | `5432` | No |
| `DBName` | `string` | `DB_NAME` | — | Yes |
| `DBUser` | `string` | `DB_USER` | — | Yes |
| `DBPasswordFile` | `string` | `DB_PASSWORD_FILE` | — | Yes |
| `DBMaxConns` | `int` | `DB_MAX_CONNS` | `10` | No |
| `DBMinConns` | `int` | `DB_MIN_CONNS` | `2` | No |
| `DBSSLMode` | `string` | `DB_SSL_MODE` | `"require"` | No |
| `GatewayBufferSize` | `int` | `GATEWAY_BUFFER_SIZE` | `1000` | No |
| `HeartbeatTickMs` | `int` | `HEARTBEAT_TICK_MS` | `10000` | No |
| `HeartbeatGracePeriodMs` | `int` | `HEARTBEAT_GRACE_PERIOD_MS` | `120000` | No |
| `AlertConfigRefreshMs` | `int` | `ALERT_CONFIG_REFRESH_MS` | `300000` | No |
| `AlertConfigDefaultTimeoutMs` | `int64` | `ALERT_CONFIG_DEFAULT_TIMEOUT_MS` | `60000` | No |
| `AlertConfigMaxRetries` | `int` | `ALERT_CONFIG_MAX_RETRIES` | `10` | No |
| `MetricsAddr` | `string` | `METRICS_ADDR` | `":9090"` | No |

#### Methods

| Method | Signature | Notes |
|---|---|---|
| `GetDatabaseDSN` | `() (string, error)` | Reads password from Docker secret file (`DBPasswordFile`), trims whitespace, constructs DSN as `postgres://user:pass@host:port/dbname?sslmode=<DBSSLMode>` |

---

### `Load` — package-level factory function

| Function | Signature |
|---|---|
| `Load` | `() (*Config, error)` |

Returns: `*Config`. Internally uses an unexported `loader` helper struct that accumulates the first error encountered during sequential env var reading — subsequent calls are no-ops once an error is set, keeping the function branch-free.

---

### `Metrics` — value object

All Prometheus metric handles. Constructed once in `main` via `New(reg prometheus.Registerer)` — accepts a `prometheus.Registerer` (rather than using the global default) so tests can pass `prometheus.NewRegistry()` and avoid duplicate-registration panics. Injected into adapters that emit metrics via narrow interfaces.

| Field | Type | Prometheus Name | Description |
|---|---|---|---|
| `MessagesReceived` | `prometheus.Counter` | `notip_consumer_messages_received_total` | NATS messages dequeued |
| `MessagesWritten` | `prometheus.Counter` | `notip_consumer_messages_written_total` | Successful TimescaleDB writes |
| `WriteErrors` | `prometheus.Counter` | `notip_consumer_write_errors_total` | Failed TimescaleDB writes |
| `WriteLatency` | `prometheus.Histogram` | `notip_consumer_write_duration_seconds` | TimescaleDB write duration (DefBuckets) |
| `AlertsPublished` | `prometheus.Counter` | `notip_consumer_alerts_published_total` | Gateway-offline alerts sent |
| `AlertPublishErrors` | `prometheus.Counter` | `notip_consumer_alert_publish_errors_total` | Failed alert publish attempts |
| `StatusUpdateErrors` | `prometheus.Counter` | `notip_consumer_status_update_errors_total` | Failed NATS RR status updates |
| `StatusUpdateDropped` | `prometheus.Counter` | `notip_consumer_status_update_dropped_total` | Status updates dropped (full dispatch buffer) |
| `AlertCacheRefreshErrors` | `prometheus.Counter` | `notip_consumer_alert_cache_refresh_errors_total` | Failed alert config cache refreshes |
| `NATSReconnects` | `prometheus.Counter` | `notip_consumer_nats_reconnects_total` | NATS reconnection events |
| `HeartbeatMapSize` | `prometheus.Gauge` | `notip_consumer_heartbeat_map_size` | Number of gateways currently tracked |

#### Narrow Interface Methods

The `Metrics` struct satisfies all narrow metric interfaces defined in adapter packages via the following methods:

`IncMessagesReceived`, `IncMessagesWritten`, `IncWriteErrors`, `ObserveWriteLatency(d time.Duration)`, `IncAlertsPublished`, `IncAlertPublishErrors`, `IncStatusUpdateErrors`, `IncStatusUpdateDropped`, `IncAlertCacheRefreshErrors`, `IncNATSReconnects`, `SetHeartbeatMapSize(v float64)`

---

## 8. Startup Sequence (`cmd/consumer/main.go`)

The following steps must execute in order. A failure at any blocking step terminates the process. The application defines three compile-time constants: `natsRRTimeout = 5s`, `telemetryBatchSize = 100`, `telemetryFlushInterval = 500ms`.

| Step | Component | Action | Blocking? |
|---|---|---|---|
| 0 | `slog` | Set JSON handler on stderr as default logger | No |
| 1 | `config.Load` | Load and validate config from environment; build database DSN from Docker secret file | Yes — crash on missing required fields or unreadable password file |
| 2 | `Metrics` | Create Prometheus metrics with `prometheus.DefaultRegisterer` | No |
| 3 | NATS | Connect to NATS with mTLS (`RootCAs`, `ClientCert`); configure connect timeout and reconnect handler | Yes — crash if connection fails |
| 4 | JetStream | Acquire JetStream context from NATS connection | Yes — crash on error |
| 5 | `pgxpool` | Parse DSN, set `MaxConns`/`MinConns`, create TimescaleDB connection pool; create signal context (`SIGTERM`, `SIGINT`) | Yes — crash if unreachable |
| 6 | Driven adapters | Wire `NATSRRClient`, `AlertConfigCache`, `NATSAlertPublisher`, `NATSGatewayStatusUpdater`, `PostgresTelemetryWriter`, `SystemClock` | No |
| 7 | `HeartbeatTracker` | Construct service with all dependencies; starts background `dispatchWorker` goroutine | No |
| 8 | Driving adapters | Wire `HeartbeatTickTimer`, `NATSDecommissionConsumer`, `NATSTelemetryConsumer` | No |
| 9 | Prometheus HTTP | Start HTTP server on `MetricsAddr` serving `/metrics` and `/healthz` in a background goroutine (`/healthz` checks DB reachability via `pool.Ping`) | No |
| 10 | `AlertConfigCache.Run` | Launch background goroutine: initial fetch with backoff, then periodic refresh loop | No — uses defaults if all retries fail |
| 11 | `HeartbeatTickTimer` | Launch background goroutine | No |
| 12 | `NATSDecommissionConsumer` | Launch background goroutine (errors logged) | No |
| 13 | `NATSTelemetryConsumer.Run` | Start JetStream consumer (blocks main goroutine) | Yes |
| — | Signal handler | On SIGTERM/SIGINT: cancel root context → telemetry consumer stops → decommission consumer stops → tick timer stops → alert cache stops → metrics server shuts down → `tracker.Close()` drains dispatch channel → pool closes → NATS drains | — |

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
| `HeartbeatTracker` | depends on | `ClockProvider` |
| `HeartbeatTracker` | depends on | `HeartbeatTrackerMetrics` |
| `HeartbeatTracker` | composes | `heartbeatEntry` (0..*) |
| `HeartbeatTracker` | composes | `statusUpdateJob` (via channel) |
| `PostgresTelemetryWriter` | implements | `TelemetryWriter` |
| `NATSAlertPublisher` | implements | `AlertPublisher` |
| `NATSAlertPublisher` | depends on | `alertPublisherMetrics` |
| `NATSGatewayStatusUpdater` | implements | `GatewayStatusUpdater` |
| `NATSGatewayStatusUpdater` | depends on | `gatewayStatusUpdateCaller` |
| `NATSGatewayStatusUpdater` | depends on | `statusUpdateErrRecorder` |
| `AlertConfigCache` | implements | `AlertConfigProvider` |
| `AlertConfigCache` | depends on | `alertConfigFetcher` |
| `AlertConfigCache` | depends on | `alertCacheErrRecorder` |
| `AlertConfigCache` | composes | `alertConfigSnapshot` |
| `SystemClock` | implements | `ClockProvider` |
| `NATSTelemetryConsumer` | depends on | `TelemetryMessageHandler` |
| `NATSTelemetryConsumer` | depends on | `TelemetryWriter` |
| `NATSTelemetryConsumer` | depends on | `telemetryConsumerMetrics` |
| `NATSDecommissionConsumer` | depends on | `DecommissionEventHandler` |
| `HeartbeatTickTimer` | depends on | `HeartbeatTicker` |
| `NATSRRClient` | aggregated by | `AlertConfigCache` (via `alertConfigFetcher`) |
| `NATSRRClient` | aggregated by | `NATSGatewayStatusUpdater` (via `gatewayStatusUpdateCaller`) |
| `TelemetryEnvelope` | composes | `OpaqueBlob` (×3) |
| `TelemetryRow` | composes | `OpaqueBlob` (×3) |
| `Metrics` | satisfies | `HeartbeatTrackerMetrics` |
| `Metrics` | satisfies | `telemetryConsumerMetrics` |
| `Metrics` | satisfies | `alertPublisherMetrics` |
| `Metrics` | satisfies | `statusUpdateErrRecorder` |
| `Metrics` | satisfies | `alertCacheErrRecorder` |

---

## 10. Database Schema — `migrations/001_create_telemetry_hypertable.sql`

```sql
CREATE TABLE IF NOT EXISTS telemetry (
    time           TIMESTAMPTZ NOT NULL,
    tenant_id      TEXT        NOT NULL,
    gateway_id     TEXT        NOT NULL,
    sensor_id      TEXT        NOT NULL,
    sensor_type    TEXT        NOT NULL,
    encrypted_data TEXT        NOT NULL,
    iv             TEXT        NOT NULL,
    auth_tag       TEXT        NOT NULL,
    key_version    INTEGER     NOT NULL
);

SELECT create_hypertable('telemetry', 'time',
    chunk_time_interval => INTERVAL '1 day',
    if_not_exists       => TRUE);

CREATE INDEX IF NOT EXISTS idx_telemetry_tenant_gateway_time
    ON telemetry (tenant_id, gateway_id, time DESC);
```

Encrypted payload columns are stored as `TEXT` (base64 strings) and are **never decoded server-side** — Rule Zero.

---

## Changelog (v1.0 → v2.0)

### Section 0 — System Context

| # | Change | Detail |
|---|---|---|
| 1 | **Added** row in §0.5 | New pattern: "Narrow metric interfaces" — each adapter defines its own minimal metric interface. |
| 2 | **Renamed** in §0.5 | `Clock` abstraction → `ClockProvider` abstraction, matching the actual interface name in code. |
| 3 | **Added** row in §0.5 | New pattern: "Non-blocking dispatch" — status updates queued to a bounded channel processed by a background worker. |

### Section 1 — Service Overview

| # | Change | Detail |
|---|---|---|
| 4 | **Changed** opening line | "no HTTP endpoints" → "one HTTP server exposing `/metrics` and `/healthz`" — the service now exposes metrics plus a DB-backed health endpoint. |
| 5 | **Changed** item 1 | "persists every message" → "batch-persists every message" — telemetry is now written in batches. |
| 6 | **Changed** item 3 | "calls `internal.mgmt.gateway.update-status` via NATS RR" → "dispatches an asynchronous `internal.mgmt.gateway.update-status` call via NATS RR with `offline`" — status updates are non-blocking. |
| 7 | **Changed** item 4 | "On an online recovery (first message after offline)" → "On first-seen or online recovery (first message after offline)" — Online status is dispatched for first-seen gateways too. |
| 8 | **Changed** package layout | `config/` description → "Config struct, environment loader, DSN builder". `metrics/` → "Prometheus metric handles and narrow-interface methods". Added `tests/integration/` directory. |

### Section 2 — Domain Models

| # | Change | Detail |
|---|---|---|
| 9 | **Added** `OpaqueBlob` methods | Documented custom `MarshalJSON` / `UnmarshalJSON` methods that serialise `Value` as a plain JSON string. |
| 10 | **Added** JSON tags | `TelemetryEnvelope` table now includes JSON tag column (`gatewayId`, `sensorId`, etc.). |
| 11 | **Added** JSON tags | `AlertPayload` table now includes JSON tag column. |
| 12 | **Added** JSON tags | `GatewayStatusUpdate` table now includes JSON tag column. |
| 13 | **Added** `gatewayKey` struct | New internal type in the `service` package — composite map key replacing the `"tenantID/gatewayID"` string. |
| 14 | **Changed** `HeartbeatEntry` | Renamed from `HeartbeatEntry` to `heartbeatEntry` (unexported). Specified as "internal to `service` package" rather than just "internal". |
| 15 | **Added** `statusUpdateJob` | New internal type wrapping `GatewayStatusUpdate` for the async dispatch channel. |

### Section 3 — Ports

| # | Change | Detail |
|---|---|---|
| 16 | **Added** `WriteBatch` | `TelemetryWriter` interface now has a second method: `WriteBatch(ctx context.Context, rows []TelemetryRow) error`. |
| 17 | **Renamed** `Clock` → `ClockProvider` | Interface name changed to match the actual codebase. |

### Section 4 — Domain Service

| # | Change | Detail |
|---|---|---|
| 18 | **Added** `HeartbeatTrackerMetrics` | New narrow metric interface with `IncStatusUpdateDropped()` and `SetHeartbeatMapSize(v float64)`. |
| 19 | **Added** field `metrics` | `HeartbeatTracker` now has a `metrics` field of type `HeartbeatTrackerMetrics`. |
| 20 | **Changed** `beats` type | `map[string]HeartbeatEntry` → `map[gatewayKey]*heartbeatEntry` — struct key, pointer values. |
| 21 | **Added** fields | `dispatchCh chan statusUpdateJob`, `done chan struct{}`, `closeOnce sync.Once` for async dispatch and clean shutdown. |
| 22 | **Changed** `startTime` init | Now set via `clock.Now()` (not `time.Now()`) — respects injected clock for tests. |
| 23 | **Changed** constructor signature | Added `metrics HeartbeatTrackerMetrics` and `statusUpdateBufSize int` parameters. Constructor now spawns `dispatchWorker` goroutine. |
| 24 | **Added** `Close()` method | Closes `dispatchCh`, waits for `dispatchWorker` to drain; idempotent via `sync.Once`. |
| 25 | **Changed** `HandleTelemetry` behaviour | Now dispatches an `Online` status update on **first-seen** gateways (not just on Offline→Online recovery). Updates heartbeat map size metric. |
| 26 | **Changed** `HandleDecommission` | Now updates heartbeat map size metric after deletion. |
| 27 | **Changed** `Tick` behaviour | Complete rewrite to three-phase approach: (1) RLock snapshot, (2) I/O outside lock, (3) WLock with re-validation to handle concurrent telemetry arrival. |
| 28 | **Removed** private methods | `beatKey`, `detectTransition` — logic replaced by struct key and inline evaluation. |
| 29 | **Changed** `dispatchStatusUpdate` | Now sends to `dispatchCh` channel non-blocking; drops and increments metric if full. Replaces the previous context-based dispatch. |
| 30 | **Added** `dispatchWorker` | Background goroutine that serially processes status updates from the channel. |

### Section 5 — Driven Adapters

| # | Change | Detail |
|---|---|---|
| 31 | **Changed** `PostgresTelemetryWriter.pool` type | `*pgxpool.Pool` → `dbPool` (interface). Documented the `dbPool` interface. |
| 32 | **Added** `WriteBatch` method | `PostgresTelemetryWriter` now implements `WriteBatch` using `pgx.Batch` + `SendBatch` for single-roundtrip batch writes. |
| 33 | **Changed** `NATSAlertPublisher.js` type | `nats.JetStreamContext` → `natsJSPublisher` (interface). Documented the interface. |
| 34 | **Added** `NATSAlertPublisher.metrics` field | Type `alertPublisherMetrics`. Documented the narrow interface. |
| 35 | **Changed** `NATSGatewayStatusUpdater` fields | Replaced `nc *nats.Conn` + `requestTimeout time.Duration` with `client gatewayStatusUpdateCaller` + `metrics statusUpdateErrRecorder`. Documented both narrow interfaces. |
| 36 | **Changed** `AlertConfigCache` | Added `metrics alertCacheErrRecorder` field. Documented the narrow interface. Changed `rrClient` type from `NATSRRClient` to `alertConfigFetcher` (interface). Constructor now initialises with empty snapshot. |
| 37 | **Changed** `NATSRRClient.nc` type | `*nats.Conn` → `natsRequester` (interface). Documented the interface. Added detail about context timeout derivation. |
| 38 | **Changed** `SystemClock` | Now documented as implementing `ClockProvider` (renamed from `Clock`). |

### Section 6 — Driving Adapters

| # | Change | Detail |
|---|---|---|
| 39 | **Added** `natsJSSubscriber` interface | Shared interface over `nats.JetStreamContext` used by both driving consumers. |
| 40 | **Major rewrite** `NATSTelemetryConsumer` | Added batch-processing architecture: `batchSize`, `flushEvery` fields, `metrics` field, `flushLoop`, `writeBatch` methods, `permanentError` type, `pendingMsg` type, `telemetryConsumerMetrics` interface. `processMessage` now returns `(TelemetryRow, error)`. Error classification: permanent (Term) vs transient (NakWithDelay). |
| 41 | **Changed** `NATSDecommissionConsumer.js` type | `nats.JetStreamContext` → `natsJSSubscriber`. Added `handleMsg` method documentation. |

### Section 7 — Cross-Cutting

| # | Change | Detail |
|---|---|---|
| 42 | **Replaced** `NATSCredentialsPath` | Replaced single credentials path with mTLS triple: `NATSTlsCa`, `NATSTlsCert`, `NATSTlsKey` (all required). |
| 43 | **Replaced** `DatabaseURL` | Replaced single DSN string with individual fields: `DBHost`, `DBPort`, `DBName`, `DBUser`, `DBPasswordFile`, `DBSSLMode`. Password is read from Docker secret file; `DBSSLMode` controls DSN `sslmode` (default `require`). |
| 44 | **Changed** `HeartbeatGracePeriodSeconds` | Renamed to `HeartbeatGracePeriodMs` (now in milliseconds, default `120000`). |
| 45 | **Added** `GatewayBufferSize` | New config field (default `1000`) — controls dispatch channel buffer size for `HeartbeatTracker`. |
| 46 | **Added** `GetDatabaseDSN` method | On `Config` struct. Reads password from Docker secret file and constructs DSN with `sslmode=<DBSSLMode>` (default `require`). |
| 47 | **Added** env var column | Config table now includes environment variable names for each field. |
| 48 | **Added** required column | Config table now indicates which fields are required vs optional. |
| 49 | **Changed** `Metrics` constructor | `New()` → `New(reg prometheus.Registerer)` — accepts a registerer for test isolation. |
| 50 | **Added** Prometheus metric names | Each metric field now lists its Prometheus name (all prefixed `notip_consumer_`). |
| 51 | **Added** narrow interface methods | Documented all Inc/Set/Observe methods that satisfy adapter-specific metric interfaces. |

### Section 8 — Startup Sequence

| # | Change | Detail |
|---|---|---|
| 52 | **Added** step 0 | JSON logger setup via `slog`. |
| 53 | **Changed** step 1 | Now includes DSN construction from Docker secret file. |
| 54 | **Added** step 2 (Metrics) | Metrics now created before NATS connection (reconnect handler needs it). |
| 55 | **Changed** NATS connection | Credentials auth → mTLS auth (`RootCAs`, `ClientCert`). |
| 56 | **Added** step 4 (JetStream) | Explicit JetStream context acquisition step. |
| 57 | **Renumbered** all subsequent steps | Steps renumbered to reflect new ordering. |
| 58 | **Added** step 9 (Prometheus HTTP) | HTTP server for `/metrics` and `/healthz` endpoints (`/healthz` checks DB reachability). |
| 59 | **Added** compile-time constants | `natsRRTimeout = 5s`, `telemetryBatchSize = 100`, `telemetryFlushInterval = 500ms`. |
| 60 | **Changed** shutdown sequence | Now includes metrics server shutdown, `tracker.Close()` to drain dispatch channel, and detailed LIFO ordering. |

### Section 9 — Relationship Summary

| # | Change | Detail |
|---|---|---|
| 61 | **Changed** `Clock` → `ClockProvider` | All references updated. |
| 62 | **Added** 12 new relationships | `HeartbeatTracker` → `HeartbeatTrackerMetrics`, `HeartbeatTracker` → `statusUpdateJob`, `NATSAlertPublisher` → `alertPublisherMetrics`, `NATSGatewayStatusUpdater` → `gatewayStatusUpdateCaller`, `NATSGatewayStatusUpdater` → `statusUpdateErrRecorder`, `AlertConfigCache` → `alertConfigFetcher`, `AlertConfigCache` → `alertCacheErrRecorder`, `NATSTelemetryConsumer` → `telemetryConsumerMetrics`, `Metrics` satisfies all 5 narrow interfaces. |
| 63 | **Changed** `NATSRRClient` aggregation notes | Now specifies the narrow interfaces through which aggregation occurs. |
| 64 | **Removed** `HeartbeatEntry` composition | Replaced by lowercase `heartbeatEntry`. |

### New Sections

| # | Change | Detail |
|---|---|---|
| 65 | **Added** Section 10 | Database Schema — full SQL migration with Rule Zero annotation. |

---

## Changelog (v2.0 → v3.0)

### Section 0 — System Context

| # | Change | Detail |
|---|---|---|
| 1 | **Fixed** §0.3 subject | `alert.{tenantId}.gw_offline` → `alert.gw_offline.{tenantId}` — naming convention was violated (tenantId and action token were swapped). |

### Section 2 — Domain Models

| # | Change | Detail |
|---|---|---|
| 2 | **Added** `SensorType` enum | New named string type with 5 constants (`SensorTypeTemperature`, `SensorTypeHumidity`, `SensorTypeMovement`, `SensorTypePressure`, `SensorTypeBiometric`) matching the AsyncAPI contract enum values. Placed between `TelemetryEnvelope` and `TelemetryRow`. |
| 3 | **Changed** `TelemetryEnvelope.SensorType` | Field type `string` → `SensorType` — now a typed enum instead of a free string. |
| 4 | **Fixed** `AlertPayload` description | Subject reference updated: `alert.{tenantId}.gw_offline` → `alert.gw_offline.{tenantId}`. |
| 5 | **Fixed** `GatewayStatusUpdate` json tags | `GatewayID`: `gatewayId` → `gateway_id`; `LastSeenAt`: `lastSeenAt` → `last_seen_at`. Both were wrong in v2.0 — not aligned with the actual Go struct tags. |

### Section 5 — Driven Adapters

| # | Change | Detail |
|---|---|---|
| 6 | **Fixed** `NATSAlertPublisher` description | Subject reference updated: `alert.{tenantId}.gw_offline` → `alert.gw_offline.{tenantId}`. |

---

## Changelog (v3.0 → v3.1)

### Section 5 — Driven Adapters

| # | Change | Detail |
|---|---|---|
| 1 | **Added** `NATSRRClient` fields | Added `maxRetries int`, `backoff []time.Duration`, `sleep func(time.Duration)` — were present in code but missing from doc. |
| 2 | **Fixed** `NATSRRClient` description | Replaced "response body is ignored" with accurate description: `requestWithRetry` applies retry policy (3x, backoff 1s/2s/4s, per-attempt 5s timeout); `UpdateGatewayStatus` parses and validates `success` field in response. |
