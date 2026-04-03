package port

import (
	"context"
	"time"

	"github.com/NoTIPswe/notip-data-consumer/internal/domain/model"
)

// =============================================================================
// Driven ports — implemented by adapters, called by the domain.
// =============================================================================

// TelemetryWriter persists a single normalised telemetry record.
type TelemetryWriter interface {
	Write(ctx context.Context, row model.TelemetryRow) error
	WriteBatch(ctx context.Context, rows []model.TelemetryRow) error
}

// AlertPublisher publishes a gateway-offline alert to JetStream.
type AlertPublisher interface {
	Publish(ctx context.Context, tenantID string, payload model.AlertPayload) error
}

// GatewayStatusUpdater sends a gateway status update to the Management API via NATS RR.
type GatewayStatusUpdater interface {
	UpdateStatus(ctx context.Context, update model.GatewayStatusUpdate) error
}

// AlertConfigProvider returns the configured offline timeout for a specific gateway.
type AlertConfigProvider interface {
	TimeoutFor(tenantID, gatewayID string) int64
}

// GatewayLifecycleProvider returns the current administrative lifecycle state of a gateway
// from the Management API. Used by HeartbeatTracker to suppress false offline alerts
// when a gateway has been intentionally paused.
type GatewayLifecycleProvider interface {
	GetGatewayLifecycle(ctx context.Context, tenantID, gatewayID string) (model.GatewayLifecycleState, error)
}

// ClockProvider abstracts time.Now() to enable deterministic tests.
type ClockProvider interface {
	Now() time.Time
}

// =============================================================================
// Driving ports — implemented by the domain, called by adapters.
// =============================================================================

// TelemetryMessageHandler is the entry point for a decoded telemetry envelope.
type TelemetryMessageHandler interface {
	HandleTelemetry(ctx context.Context, tenantID string, envelope model.TelemetryEnvelope) error
}

// DecommissionEventHandler is the entry point for a gateway decommission event.
type DecommissionEventHandler interface {
	HandleDecommission(tenantID, gatewayID string)
}

// HeartbeatTicker is the entry point for the periodic liveness check.
type HeartbeatTicker interface {
	Tick(ctx context.Context)
}
