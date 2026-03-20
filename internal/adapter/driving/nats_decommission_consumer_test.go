package driving

import (
	"testing"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubs

type stubDecommissionHandler struct {
	tenantID  string
	gatewayID string
	calls     int
}

func (s *stubDecommissionHandler) HandleDecommission(tenantID, gatewayID string) {
	s.tenantID = tenantID
	s.gatewayID = gatewayID
	s.calls++
}

// extractIDs

func TestNATSDecommissionConsumerExtractIDsSuccess(t *testing.T) {
	c := &NATSDecommissionConsumer{}

	tenantID, gatewayID, err := c.extractIDs("gateway.decommissioned.tenant-1.gw-42")

	require.NoError(t, err)
	assert.Equal(t, "tenant-1", tenantID)
	assert.Equal(t, "gw-42", gatewayID)
}

func TestNATSDecommissionConsumerExtractIDsMalformedSubject(t *testing.T) {
	c := &NATSDecommissionConsumer{}

	_, _, err := c.extractIDs("gateway.decommissioned.only-three")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected subject format")
}

// handleMsg

func TestNATSDecommissionConsumerHandleMsgDispatches(t *testing.T) {
	handler := &stubDecommissionHandler{}
	c := &NATSDecommissionConsumer{handler: handler}

	msg := &nats.Msg{
		Subject: "gateway.decommissioned.t1.gw-1",
		Reply:   "_INBOX.test",
	}
	msg.Sub = &nats.Subscription{}

	// Manually call handleMsg — avoids needing a live NATS server.
	c.handleMsg(msg)

	// Handler must have been called with the correct IDs.
	assert.Equal(t, 1, handler.calls)
	assert.Equal(t, "t1", handler.tenantID)
	assert.Equal(t, "gw-1", handler.gatewayID)
}

func TestNATSDecommissionConsumerHandleMsgBadSubjectTerms(t *testing.T) {
	handler := &stubDecommissionHandler{}
	c := &NATSDecommissionConsumer{handler: handler}

	msg := &nats.Msg{Subject: "gateway.decommissioned.missing-gwid"}
	c.handleMsg(msg)

	assert.Equal(t, 0, handler.calls,
		"handler must not be called when the subject cannot be parsed")
}
