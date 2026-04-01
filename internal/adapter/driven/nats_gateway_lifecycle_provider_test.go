package driven

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NoTIPswe/notip-data-consumer/internal/domain/model"
)

type stubLifecycleClient struct {
	state model.GatewayLifecycleState
	err   error
}

func (s *stubLifecycleClient) GetGatewayLifecycle(_ context.Context, _, _ string) (model.GatewayLifecycleState, error) {
	return s.state, s.err
}

type stubLifecycleMetrics struct {
	errors int
}

func (s *stubLifecycleMetrics) IncLifecycleQueryErrors() {
	s.errors++
}

func newLifecycleProvider(client gatewayLifecycleCaller) (*NATSGatewayLifecycleProvider, *stubLifecycleMetrics) {
	m := &stubLifecycleMetrics{}
	return NewNATSGatewayLifecycleProvider(client, m), m
}

func TestNATSGatewayLifecycleProviderSuccess(t *testing.T) {
	stub := &stubLifecycleClient{state: model.LifecyclePaused}
	p, m := newLifecycleProvider(stub)

	state, err := p.GetGatewayLifecycle(context.Background(), "tenant-1", "gw-1")

	require.NoError(t, err)
	assert.Equal(t, model.LifecyclePaused, state)
	assert.Equal(t, 0, m.errors)
}

func TestNATSGatewayLifecycleProviderOnlineState(t *testing.T) {
	stub := &stubLifecycleClient{state: model.LifecycleOnline}
	p, _ := newLifecycleProvider(stub)

	state, err := p.GetGatewayLifecycle(context.Background(), "tenant-1", "gw-1")

	require.NoError(t, err)
	assert.Equal(t, model.LifecycleOnline, state)
}

func TestNATSGatewayLifecycleProviderNATSError(t *testing.T) {
	stub := &stubLifecycleClient{err: errors.New("no responders")}
	p, m := newLifecycleProvider(stub)

	state, err := p.GetGatewayLifecycle(context.Background(), "tenant-1", "gw-1")

	require.Error(t, err)
	assert.Equal(t, model.LifecycleUnknown, state)
	assert.Equal(t, 1, m.errors, "a failed query must increment the lifecycle query error counter")
}
