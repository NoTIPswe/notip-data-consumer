package driven

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NoTIPswe/notip-data-consumer/internal/domain/model"
)

const (
	notJsonErrMsg      = "not json"
	noRespondersErrMsg = "no responders"
	tenantID           = "tenant-1"
)

// mockRequester records every call to RequestWithContext.
type mockRequester struct {
	subject string
	payload []byte
	resp    *nats.Msg
	err     error
	calls   int
	errs    []error
}

func (m *mockRequester) RequestWithContext(_ context.Context, subj string, data []byte) (*nats.Msg, error) {
	m.calls++
	m.subject = subj
	m.payload = data
	if len(m.errs) > 0 {
		err := m.errs[0]
		m.errs = m.errs[1:]
		if err != nil {
			return nil, err
		}
	}
	return m.resp, m.err
}

func newRRClient(r natsRequester) *NATSRRClient {
	return &NATSRRClient{
		nc:         r,
		logger:     slog.Default(),
		timeout:    5 * time.Second,
		maxRetries: 3,
		backoff:    []time.Duration{time.Second, 2 * time.Second, 4 * time.Second},
		sleep:      func(context.Context, time.Duration) error { return nil },
	}
}

func TestNewNATSRRClientSetsFields(t *testing.T) {
	c := NewNATSRRClient(nil, 3*time.Second)
	require.NotNil(t, c)
	assert.Nil(t, c.nc)
	assert.Equal(t, 3*time.Second, c.timeout)
	assert.Equal(t, 3, c.maxRetries)
	assert.Equal(t, []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}, c.backoff)
	require.NotNil(t, c.sleep)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := c.sleep(ctx, time.Second)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}

// ─── FetchAlertConfigs ────────────────────────────────────────────────────────

func TestNATSRRClientFetchAlertConfigsSuccess(t *testing.T) {
	gwID := "gw-1"
	configs := []model.AlertConfig{
		{TenantID: "t1", GatewayID: &gwID, TimeoutMs: 30000},
		{TenantID: "t1", GatewayID: nil, TimeoutMs: 60000},
	}
	body, _ := json.Marshal(configs)

	mock := &mockRequester{resp: &nats.Msg{Data: body}}
	client := newRRClient(mock)

	result, err := client.FetchAlertConfigs(context.Background())

	require.NoError(t, err)
	assert.Equal(t, configs, result)
	assert.Equal(t, subjectAlertConfigsList, mock.subject)
	assert.Nil(t, mock.payload) // no request body expected
}

func TestNATSRRClientFetchAlertConfigsNATSError(t *testing.T) {
	mock := &mockRequester{err: errors.New("timeout")}
	client := newRRClient(mock)

	_, err := client.FetchAlertConfigs(context.Background())

	require.Error(t, err)
	assert.Contains(t, err.Error(), subjectAlertConfigsList)
}

func TestNATSRRClientFetchAlertConfigsMalformedJSON(t *testing.T) {
	mock := &mockRequester{resp: &nats.Msg{Data: []byte(notJsonErrMsg)}}
	client := newRRClient(mock)

	_, err := client.FetchAlertConfigs(context.Background())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "unmarshal")
}

// ─── UpdateGatewayStatus ──────────────────────────────────────────────────────

func TestNATSRRClientUpdateGatewayStatusSuccess(t *testing.T) {
	mock := &mockRequester{resp: &nats.Msg{Data: []byte(`{"success":true}`)}}
	client := newRRClient(mock)

	update := model.GatewayStatusUpdate{
		GatewayID:  "gw-1",
		Status:     model.Online,
		LastSeenAt: time.Now(),
	}

	err := client.UpdateGatewayStatus(context.Background(), update)

	require.NoError(t, err)
	assert.Equal(t, subjectGatewayUpdateStatus, mock.subject)

	// Verify the request body round-trips correctly.
	var decoded model.GatewayStatusUpdate
	require.NoError(t, json.Unmarshal(mock.payload, &decoded))
	assert.Equal(t, update.GatewayID, decoded.GatewayID)
	assert.Equal(t, update.Status, decoded.Status)
}

func TestNATSRRClientUpdateGatewayStatusNATSError(t *testing.T) {
	mock := &mockRequester{err: errors.New(noRespondersErrMsg)}
	client := newRRClient(mock)

	err := client.UpdateGatewayStatus(context.Background(), model.GatewayStatusUpdate{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), subjectGatewayUpdateStatus)
}

func TestNATSRRClientUpdateGatewayStatusRejectedByManagementAPI(t *testing.T) {
	mock := &mockRequester{resp: &nats.Msg{Data: []byte(`{"success":false,"error":"invalid transition"}`)}}
	client := newRRClient(mock)

	err := client.UpdateGatewayStatus(context.Background(), model.GatewayStatusUpdate{GatewayID: "gw-1", Status: model.Offline})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "rejected by management-api")
	assert.Contains(t, err.Error(), "invalid transition")
}

func TestNATSRRClientUpdateGatewayStatusMalformedResponseJSON(t *testing.T) {
	mock := &mockRequester{resp: &nats.Msg{Data: []byte(notJsonErrMsg)}}
	client := newRRClient(mock)

	err := client.UpdateGatewayStatus(context.Background(), model.GatewayStatusUpdate{GatewayID: "gw-1", Status: model.Offline})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "unmarshal gateway status update response")
}

// ─── GetGatewayLifecycle ──────────────────────────────────────────────────────

func TestNATSRRClientGetGatewayLifecycleSuccess(t *testing.T) {
	resp := model.GatewayLifecycleResponse{GatewayID: "gw-1", State: model.LifecyclePaused}
	body, _ := json.Marshal(resp)

	mock := &mockRequester{resp: &nats.Msg{Data: body}}
	client := newRRClient(mock)

	state, err := client.GetGatewayLifecycle(context.Background(), tenantID, "gw-1")

	require.NoError(t, err)
	assert.Equal(t, model.LifecyclePaused, state)
	assert.Equal(t, subjectGatewayGetStatus, mock.subject)

	var decoded model.GatewayLifecycleRequest
	require.NoError(t, json.Unmarshal(mock.payload, &decoded))
	assert.Equal(t, "gw-1", decoded.GatewayID)
	assert.Equal(t, "tenant-1", decoded.TenantID)
}

func TestNATSRRClientGetGatewayLifecycleNATSError(t *testing.T) {
	mock := &mockRequester{err: errors.New(noRespondersErrMsg)}
	client := newRRClient(mock)

	state, err := client.GetGatewayLifecycle(context.Background(), tenantID, "gw-1")

	require.Error(t, err)
	assert.Equal(t, model.LifecycleUnknown, state)
	assert.Contains(t, err.Error(), subjectGatewayGetStatus)
}

func TestNATSRRClientGetGatewayLifecycleMalformedJSON(t *testing.T) {
	mock := &mockRequester{resp: &nats.Msg{Data: []byte(notJsonErrMsg)}}
	client := newRRClient(mock)

	state, err := client.GetGatewayLifecycle(context.Background(), tenantID, "gw-1")

	require.Error(t, err)
	assert.Equal(t, model.LifecycleUnknown, state)
	assert.Contains(t, err.Error(), "unmarshal")
}

func TestNATSRRClientGetGatewayLifecycleInvalidState(t *testing.T) {
	resp := model.GatewayLifecycleResponse{GatewayID: "gw-1", State: model.GatewayLifecycleState("broken")}
	body, _ := json.Marshal(resp)

	mock := &mockRequester{resp: &nats.Msg{Data: body}}
	client := newRRClient(mock)

	state, err := client.GetGatewayLifecycle(context.Background(), tenantID, "gw-1")

	require.Error(t, err)
	assert.Equal(t, model.LifecycleUnknown, state)
	assert.Contains(t, err.Error(), "invalid gateway lifecycle state")
}

func TestNATSRRClientGetGatewayLifecycleGatewayIDMismatch(t *testing.T) {
	resp := model.GatewayLifecycleResponse{GatewayID: "gw-other", State: model.LifecycleOnline}
	body, _ := json.Marshal(resp)

	mock := &mockRequester{resp: &nats.Msg{Data: body}}
	client := newRRClient(mock)

	state, err := client.GetGatewayLifecycle(context.Background(), tenantID, "gw-1")

	require.Error(t, err)
	assert.Equal(t, model.LifecycleUnknown, state)
	assert.Contains(t, err.Error(), "gateway_id mismatch")
}

func TestNATSRRClientFetchAlertConfigsRetriesThenSucceeds(t *testing.T) {
	mock := &mockRequester{
		errs: []error{errors.New("timeout 1"), errors.New("timeout 2"), nil},
		resp: &nats.Msg{Data: []byte(`[]`)},
	}
	client := newRRClient(mock)

	got, err := client.FetchAlertConfigs(context.Background())

	require.NoError(t, err)
	assert.Equal(t, 3, mock.calls)
	assert.Empty(t, got)
}

func TestNATSRRClientFetchAlertConfigsExhaustsRetries(t *testing.T) {
	mock := &mockRequester{err: errors.New(noRespondersErrMsg)}
	client := newRRClient(mock)

	_, err := client.FetchAlertConfigs(context.Background())

	require.Error(t, err)
	assert.Equal(t, 4, mock.calls) // 1 attempt + 3 retries
	assert.Contains(t, err.Error(), "exhausted retries")
}

func TestNATSRRClientFetchAlertConfigsStopsWhenSleepIsInterrupted(t *testing.T) {
	mock := &mockRequester{err: errors.New("timeout")}
	client := newRRClient(mock)
	client.sleep = func(_ context.Context, _ time.Duration) error { return context.Canceled }

	_, err := client.FetchAlertConfigs(context.Background())

	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 1, mock.calls, "must stop retrying once sleep returns an error")
}
