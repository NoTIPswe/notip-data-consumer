package driven

import (
	"context"

	"github.com/NoTIPswe/notip-data-consumer/internal/domain/model"
)

// subset of NATSRRClient used by NATSGatewayStatusUpdater.
type gatewayStatusClient interface {
	UpdateGatewayStatus(ctx context.Context, update model.GatewayStatusUpdate) error
}

// metric interface for NATSGatewayStatusUpdater.
type gatewayStatusMetrics interface {
	IncStatusUpdateErrors()
}

// implements port.GatewayStatusUpdater.
// delegates to NATSRRClient for the actual nats rr mechanics.
type NATSGatewayStatusUpdater struct {
	client  gatewayStatusClient
	metrics gatewayStatusMetrics
}

func NewNATSGatewayStatusUpdater(client gatewayStatusClient, metrics gatewayStatusMetrics) *NATSGatewayStatusUpdater {
	return &NATSGatewayStatusUpdater{client: client, metrics: metrics}
}

// calls the Management API via NATS RR to report a gateway status transition.
func (u *NATSGatewayStatusUpdater) UpdateStatus(ctx context.Context, update model.GatewayStatusUpdate) error {
	if err := u.client.UpdateGatewayStatus(ctx, update); err != nil {
		u.metrics.IncStatusUpdateErrors()
		return err
	}
	return nil
}
