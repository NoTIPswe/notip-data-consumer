package driven

import (
	"context"

	"github.com/NoTIPswe/notip-data-consumer/internal/domain/model"
)

// gatewayStatusUpdateCaller is the subset of NATSRRClient used by NATSGatewayStatusUpdater.
type gatewayStatusUpdateCaller interface {
	UpdateGatewayStatus(ctx context.Context, update model.GatewayStatusUpdate) error
}

// statusUpdateErrRecorder is the metric interface for NATSGatewayStatusUpdater.
type statusUpdateErrRecorder interface {
	IncStatusUpdateErrors()
}

// implements port.GatewayStatusUpdater.
// delegates to NATSRRClient for the actual nats rr mechanics.
type NATSGatewayStatusUpdater struct {
	client  gatewayStatusUpdateCaller
	metrics statusUpdateErrRecorder
}

func NewNATSGatewayStatusUpdater(client gatewayStatusUpdateCaller, metrics statusUpdateErrRecorder) *NATSGatewayStatusUpdater {
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
