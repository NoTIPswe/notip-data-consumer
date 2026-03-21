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
}

func (m *mockRequester) RequestWithContext(_ context.Context, subj string, data []byte) (*nats.Msg, error) {
	m.subject = subj
	m.payload = data
	return m.resp, m.err
}

func newRRClient(r natsRequester) *NATSRRClient {
	return &NATSRRClient{nc: r, timeout: 5 * time.Second}
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
	mock := &mockRequester{resp: &nats.Msg{Data: []byte(`{"ok":true}`)}}
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
