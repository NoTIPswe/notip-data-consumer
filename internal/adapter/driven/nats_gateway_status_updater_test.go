package driven

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NoTIPswe/notip-data-consumer/internal/domain/model"
)

// stubs.
type stubGatewayStatusClient struct {
	lastUpdate model.GatewayStatusUpdate
	err        error
}

func (s *stubGatewayStatusClient) UpdateGatewayStatus(_ context.Context, update model.GatewayStatusUpdate) error {
	s.lastUpdate = update
	return s.err
}

type stubGatewayStatusMetrics struct {
	errors int
}

func (s *stubGatewayStatusMetrics) IncStatusUpdateErrors() {
	s.errors++
}

// helpers

func newStatusUpdater(client gatewayStatusClient) (*NATSGatewayStatusUpdater, *stubGatewayStatusMetrics) {
	m := &stubGatewayStatusMetrics{}
	return NewNATSGatewayStatusUpdater(client, m), m
}

// tests

func TestNATSGatewayStatusUpdaterSuccess(t *testing.T) {
	stub := &stubGatewayStatusClient{}
	u, m := newStatusUpdater(stub)

	update := model.GatewayStatusUpdate{GatewayID: "gw-1", Status: model.Online}
	err := u.UpdateStatus(context.Background(), update)

	require.NoError(t, err)
	assert.Equal(t, update, stub.lastUpdate, "update must be forwarde verbatim to the RR client")
	assert.Equal(t, 0, m.errors)
}

func TestNATSGatewayStatusUpdaterOfflineStatus(t *testing.T) {
	stub := &stubGatewayStatusClient{}
	u, _ := newStatusUpdater(stub)

	update := model.GatewayStatusUpdate{GatewayID: "gw-2", Status: model.Offline}
	require.NoError(t, u.UpdateStatus(context.Background(), update))

	assert.Equal(t, model.Offline, stub.lastUpdate.Status)
}

func TestNATSGatewayStatusUpdaterNATSError(t *testing.T) {
	stub := &stubGatewayStatusClient{err: errors.New("no responders")}
	u, m := newStatusUpdater(stub)

	err := u.UpdateStatus(context.Background(), model.GatewayStatusUpdate{})

	require.Error(t, err)
	assert.Equal(t, 1, m.errors,
		"a failed RR call must increment the status update error counter")
}
