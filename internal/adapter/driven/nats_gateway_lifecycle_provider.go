package driven

import (
	"context"

	"github.com/NoTIPswe/notip-data-consumer/internal/domain/model"
)

// gatewayLifecycleCaller is the subset of NATSRRClient used by NATSGatewayLifecycleProvider.
type gatewayLifecycleCaller interface {
	GetGatewayLifecycle(ctx context.Context, tenantID, gatewayID string) (model.GatewayLifecycleState, error)
}

// lifecycleQueryErrRecorder is the metric interface for NATSGatewayLifecycleProvider.
type lifecycleQueryErrRecorder interface {
	IncLifecycleQueryErrors()
}

// NATSGatewayLifecycleProvider implements port.GatewayLifecycleProvider.
// Delegates to NATSRRClient for the actual NATS RR mechanics.
type NATSGatewayLifecycleProvider struct {
	client  gatewayLifecycleCaller
	metrics lifecycleQueryErrRecorder
}

func NewNATSGatewayLifecycleProvider(client gatewayLifecycleCaller, metrics lifecycleQueryErrRecorder) *NATSGatewayLifecycleProvider {
	return &NATSGatewayLifecycleProvider{client: client, metrics: metrics}
}

// GetGatewayLifecycle queries the Management API for the current lifecycle state of a gateway.
// On NATS error, increments the lifecycle query error counter and returns the error to the caller.
func (p *NATSGatewayLifecycleProvider) GetGatewayLifecycle(ctx context.Context, tenantID, gatewayID string) (model.GatewayLifecycleState, error) {
	state, err := p.client.GetGatewayLifecycle(ctx, tenantID, gatewayID)
	if err != nil {
		p.metrics.IncLifecycleQueryErrors()
		return model.LifecycleUnknown, err
	}
	return state, nil
}
