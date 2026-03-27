package driven

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NoTIPswe/notip-data-consumer/internal/domain/model"
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
		timeout:    5 * time.Second,
		maxRetries: 3,
		backoff:    []time.Duration{time.Second, 2 * time.Second, 4 * time.Second},
		sleep:      func(time.Duration) {},
	}
}

func TestNewNATSRRClientSetsFields(t *testing.T) {
	mock := &mockRequester{}
	c := NewNATSRRClient(nil, 3*time.Second)
	assert.NotNil(t, c)
	_ = mock // satisfy linter
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
	mock := &mockRequester{resp: &nats.Msg{Data: []byte("not json")}}
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
	mock := &mockRequester{err: errors.New("no responders")}
	client := newRRClient(mock)

	err := client.UpdateGatewayStatus(context.Background(), model.GatewayStatusUpdate{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), subjectGatewayUpdateStatus)
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
	mock := &mockRequester{err: errors.New("no responders")}
	client := newRRClient(mock)

	_, err := client.FetchAlertConfigs(context.Background())

	require.Error(t, err)
	assert.Equal(t, 4, mock.calls) // 1 attempt + 3 retries
	assert.Contains(t, err.Error(), "exhausted retries")
}
