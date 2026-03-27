package driven

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
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
	nc         natsRequester
	timeout    time.Duration
	maxRetries int
	backoff    []time.Duration
	sleep      func(time.Duration)
}

// NewNATSRRClient constructs a NATSRRClient. timeout is applied per-request on top of
// any deadline already present on the caller's context (Go takes the more restrictive).
func NewNATSRRClient(nc *nats.Conn, timeout time.Duration) *NATSRRClient {
	return &NATSRRClient{
		nc:         nc,
		timeout:    timeout,
		maxRetries: 3,
		backoff:    []time.Duration{time.Second, 2 * time.Second, 4 * time.Second},
		sleep:      time.Sleep,
	}
}

// FetchAlertConfigs issues a NATS RR call to internal.mgmt.alert-configs.list and
// deserialises the JSON response into a slice of AlertConfig.
func (c *NATSRRClient) FetchAlertConfigs(ctx context.Context) ([]model.AlertConfig, error) {
	msg, err := c.requestWithRetry(ctx, subjectAlertConfigsList, nil)
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
	body, err := json.Marshal(update)
	if err != nil {
		return fmt.Errorf("marshal gateway status update: %w", err)
	}

	resp, err := c.requestWithRetry(ctx, subjectGatewayUpdateStatus, body)
	if err != nil {
		return fmt.Errorf("nats rr %s: %w", subjectGatewayUpdateStatus, err)
	}

	var result model.GatewayStatusUpdateResponse
	if err := json.Unmarshal(resp.Data, &result); err != nil {
		return fmt.Errorf("unmarshal gateway status update response: %w", err)
	}
	if !result.Success {
		return fmt.Errorf("gateway status update rejected by management-api: %s", result.Error)
	}

	return nil
}

// requestWithRetry applies AsyncAPI RR policy: timeout 5s per attempt, retry 3x with
// exponential backoff 1s/2s/4s (configurable via fields for tests).
func (c *NATSRRClient) requestWithRetry(ctx context.Context, subject string, body []byte) (*nats.Msg, error) {
	var errs []string

	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		attemptCtx, cancel := context.WithTimeout(ctx, c.timeout)
		msg, err := c.nc.RequestWithContext(attemptCtx, subject, body)
		cancel()
		if err == nil {
			return msg, nil
		}

		errs = append(errs, err.Error())
		if attempt == c.maxRetries {
			break
		}

		delay := c.backoff[min(attempt, len(c.backoff)-1)]
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		c.sleep(delay)
	}

	return nil, fmt.Errorf("exhausted retries (%d): %s", c.maxRetries, strings.Join(errs, "; "))
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
