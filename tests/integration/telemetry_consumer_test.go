//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NoTIPswe/notip-data-consumer/internal/adapter/driven"
	"github.com/NoTIPswe/notip-data-consumer/internal/adapter/driving"
	"github.com/NoTIPswe/notip-data-consumer/internal/domain/model"
)

// noopTelemetryHandler satisfies port.TelemetryMessageHandler without side-effects.
// The heartbeat/alert logic is out of scope for these integration tests.
type noopTelemetryHandler struct{}

func (noopTelemetryHandler) HandleTelemetry(_ context.Context, _ string, _ model.TelemetryEnvelope) error {
	return nil
}

// noopMetrics satisfies telemetryConsumerMetrics without recording anything.
type noopMetrics struct{}

func (*noopMetrics) IncMessagesReceived()                { /* no-op: metrics not under test */ }
func (*noopMetrics) IncMessagesWritten()                 { /* no-op: metrics not under test */ }
func (*noopMetrics) IncWriteErrors()                     { /* no-op: metrics not under test */ }
func (*noopMetrics) ObserveWriteLatency(_ time.Duration) { /* no-op: metrics not under test */ }

// TestNATSTelemetryConsumerIntegrationMessageLandsInDB publishes a well-formed
// telemetry envelope and verifies that it is written to TimescaleDB verbatim (Rule Zero).
func TestNATSTelemetryConsumerIntegrationMessageLandsInDB(t *testing.T) {
	purgeStream(t, streamTelemetry)
	truncateTelemetry(t)

	nc := connectNATSWithMTLS(t)
	js, err := nc.JetStream()
	require.NoError(t, err)

	durableName := fmt.Sprintf("e2e-valid-%d", time.Now().UnixNano())
	writer := driven.NewPostgresTelemetryWriter(sharedPool)
	consumer := driving.NewNATSTelemetryConsumer(
		js,
		noopTelemetryHandler{},
		writer,
		&noopMetrics{},
		durableName,
		10,
		100*time.Millisecond,
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = consumer.Run(ctx) }()

	// Wait until the durable consumer is registered in JetStream.
	require.Eventually(t, func() bool {
		_, err := sharedJS.ConsumerInfo(streamTelemetry, durableName)
		return err == nil
	}, 10*time.Second, 100*time.Millisecond, "consumer did not register in JetStream")

	ts := time.Now().UTC().Truncate(time.Microsecond)
	envelope := model.TelemetryEnvelope{
		GatewayID:     "gw-e2e",
		SensorID:      "sensor-e2e",
		SensorType:    "temperature",
		Timestamp:     ts,
		KeyVersion:    1,
		EncryptedData: model.OpaqueBlob{Value: "ZW5jcnlwdGVkLWRhdGE="},
		IV:            model.OpaqueBlob{Value: "aXYtdmFsdWU="},
		AuthTag:       model.OpaqueBlob{Value: "YXV0aC10YWc="},
	}
	payload, err := json.Marshal(envelope)
	require.NoError(t, err)

	_, err = js.Publish("telemetry.data.tenant-e2e.gw-e2e", payload)
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		var count int
		err := sharedPool.QueryRow(context.Background(),
			"SELECT COUNT(*) FROM telemetry WHERE tenant_id = 'tenant-e2e' AND sensor_id = 'sensor-e2e'",
		).Scan(&count)
		return err == nil && count == 1
	}, 10*time.Second, 100*time.Millisecond, "row never appeared in DB")

	// Rule Zero: blobs stored verbatim.
	var encryptedData, iv, authTag string
	err = sharedPool.QueryRow(context.Background(),
		"SELECT encrypted_data, iv, auth_tag FROM telemetry WHERE tenant_id = 'tenant-e2e' AND sensor_id = 'sensor-e2e'",
	).Scan(&encryptedData, &iv, &authTag)
	require.NoError(t, err)
	assert.Equal(t, "ZW5jcnlwdGVkLWRhdGE=", encryptedData)
	assert.Equal(t, "aXYtdmFsdWU=", iv)
	assert.Equal(t, "YXV0aC10YWc=", authTag)
}

// TestNATSTelemetryConsumerIntegrationMalformedMessageIsTermed publishes invalid
// JSON and verifies that the consumer Term()s the message (no DB row, no redelivery).
func TestNATSTelemetryConsumerIntegrationMalformedMessageIsTermed(t *testing.T) {
	purgeStream(t, streamTelemetry)
	truncateTelemetry(t)

	nc := connectNATSWithMTLS(t)
	js, err := nc.JetStream()
	require.NoError(t, err)

	durableName := fmt.Sprintf("e2e-bad-%d", time.Now().UnixNano())
	writer := driven.NewPostgresTelemetryWriter(sharedPool)
	consumer := driving.NewNATSTelemetryConsumer(
		js,
		noopTelemetryHandler{},
		writer,
		&noopMetrics{},
		durableName,
		10,
		100*time.Millisecond,
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = consumer.Run(ctx) }()

	require.Eventually(t, func() bool {
		_, err := sharedJS.ConsumerInfo(streamTelemetry, durableName)
		return err == nil
	}, 10*time.Second, 100*time.Millisecond, "consumer did not register in JetStream")

	_, err = js.Publish("telemetry.data.tenant-bad.gw-bad", []byte("{not valid json"))
	require.NoError(t, err)

	// A Term()d message is gone: NumPending and NumAckPending both drop to 0.
	require.Eventually(t, func() bool {
		info, err := sharedJS.ConsumerInfo(streamTelemetry, durableName)
		return err == nil && info.NumPending == 0 && info.NumAckPending == 0
	}, 10*time.Second, 100*time.Millisecond, "message was not Term()d")

	var count int
	err = sharedPool.QueryRow(context.Background(),
		"SELECT COUNT(*) FROM telemetry WHERE tenant_id = 'tenant-bad'",
	).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 0, count, "Term()d message must not produce a DB row")
}
