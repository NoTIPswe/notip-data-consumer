package driven

import (
	"context"
	"errors"
	"testing"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NoTIPswe/notip-data-consumer/internal/domain/model"
)

// stubs.
type stubJSPublisher struct {
	lastSubject string
	lastData    []byte
	err         error
}

func (s *stubJSPublisher) Publish(subj string, data []byte, _ ...nats.PubOpt) (*nats.PubAck, error) {
	s.lastSubject = subj
	s.lastData = data
	return nil, s.err
}

type stubAlertPublisherMetrics struct {
	published int
	errors    int
}

func (s *stubAlertPublisherMetrics) IncAlertsPublished() {
	s.published++
}
func (s *stubAlertPublisherMetrics) IncAlertPublishErrors() {
	s.errors++
}

// helpers

func newAlertPublisher(js natsJSPublisher) (*NATSAlertPublisher, *stubAlertPublisherMetrics) {
	m := &stubAlertPublisherMetrics{}
	return NewNATSAlertPublisher(js, m), m
}

// tests

func TestNATSAlertPublisherSuccess(t *testing.T) {
	js := &stubJSPublisher{}
	p, m := newAlertPublisher(js)

	err := p.Publish(context.Background(), "t1", model.AlertPayload{GatewayID: "gw-1"})

	require.NoError(t, err)
	assert.Equal(t, "alert.t1.gw_offline", js.lastSubject, "subject must follow alert.{tenantID}.gw_offline format")
	assert.NotEmpty(t, js.lastData, "request body must not be empty")
	assert.Equal(t, 1, m.published)
	assert.Equal(t, 0, m.errors)
}

func TestNATSAlertPublisherSubjectFormat(t *testing.T) {
	tests := []struct {
		tenantID        string
		expectedSubject string
	}{
		{"acme", "alert.acme.gw_offline"},
		{"tenant-99", "alert.tenant-99.gw_offline"},
	}

	for _, tc := range tests {
		js := &stubJSPublisher{}
		p, _ := newAlertPublisher(js)

		require.NoError(t, p.Publish(context.Background(), tc.tenantID, model.AlertPayload{}))
		assert.Equal(t, tc.expectedSubject, js.lastSubject)
	}
}

func TestNATSAlertPublisherNATSError(t *testing.T) {
	js := &stubJSPublisher{err: errors.New("stream not found")}
	p, m := newAlertPublisher(js)

	err := p.Publish(context.Background(), "t1", model.AlertPayload{GatewayID: "gw-1"})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "alert.t1.gw_offline", "error must include the subject for observability")
	assert.Equal(t, 0, m.published)
	assert.Equal(t, 1, m.errors)
}
