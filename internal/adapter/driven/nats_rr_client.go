package driven

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/NoTIPswe/notip-data-consumer/internal/domain/model"
)

const (
	subjectAlertConfigsList    = "internal.mgmt.alert-configs.list"
	subjectGatewayUpdateStatus = "internal.mgmt.gateway.update-status"
)

// natsRequester is a narrow interface over *nats.Conn to enable unit testing.
type natsRequester interface {
	RequestWithContext(ctx context.Context, subj string, data []byte) (*nats.Msg, error)
}

// NATSRRClient encapsulates NATS Request-Reply mechanics (timeout, serialisation, error handling).
// Shared infrastructure helper used by AlertConfigCache and NATSGatewayStatusUpdater.
type NATSRRClient struct {
	nc      natsRequester
	timeout time.Duration
}

// NewNATSRRClient constructs a NATSRRClient. timeout is applied per-request on top of
// any deadline already present on the caller's context (Go takes the more restrictive).
func NewNATSRRClient(nc *nats.Conn, timeout time.Duration) *NATSRRClient {
	return &NATSRRClient{nc: nc, timeout: timeout}
}

// FetchAlertConfigs issues a NATS RR call to internal.mgmt.alert-configs.list and
// deserialises the JSON response into a slice of AlertConfig.
func (c *NATSRRClient) FetchAlertConfigs(ctx context.Context) ([]model.AlertConfig, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout) // set a timeout for a request to NATS
	defer cancel()

	msg, err := c.nc.RequestWithContext(ctx, subjectAlertConfigsList, nil)
	if err != nil {
		return nil, fmt.Errorf("nats rr %s: %w", subjectAlertConfigsList, err)
	}

	var configs []model.AlertConfig
	if err := json.Unmarshal(msg.Data, &configs); err != nil {
		return nil, fmt.Errorf("unmarshal alert configs: %w", err)
	}

	return configs, nil
}

// UpdateGatewayStatus issues a NATS RR call to internal.mgmt.gateway.update-status
// with the serialised GatewayStatusUpdate as the request body.
func (c *NATSRRClient) UpdateGatewayStatus(ctx context.Context, update model.GatewayStatusUpdate) error {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	body, err := json.Marshal(update)
	if err != nil {
		return fmt.Errorf("marshal gateway status update: %w", err)
	}

	if _, err = c.nc.RequestWithContext(ctx, subjectGatewayUpdateStatus, body); err != nil {
		return fmt.Errorf("nats rr %s: %w", subjectGatewayUpdateStatus, err)
	}

	return nil
}
