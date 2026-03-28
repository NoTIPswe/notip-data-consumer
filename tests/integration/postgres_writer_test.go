//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NoTIPswe/notip-data-consumer/internal/adapter/driven"
	"github.com/NoTIPswe/notip-data-consumer/internal/domain/model"
)

func TestPostgresTelemetryWriterIntegrationWriteSingleRow(t *testing.T) {
	truncateTelemetry(t)
	ctx := context.Background()

	writer := driven.NewPostgresTelemetryWriter(sharedPool)

	ts := time.Date(2025, 3, 10, 8, 30, 0, 0, time.UTC)
	row := model.TelemetryRow{
		Time:          ts,
		TenantID:      "tenant-single",
		GatewayID:     "gw-single",
		SensorID:      "sensor-single",
		SensorType:    "pressure",
		EncryptedData: model.OpaqueBlob{Value: "c2luZ2xl"},
		IV:            model.OpaqueBlob{Value: "aXZz"},
		AuthTag:       model.OpaqueBlob{Value: "dGFncw=="},
		KeyVersion:    3,
	}

	require.NoError(t, writer.Write(ctx, row))

	var encData, iv, authTag, sensorType string
	var keyVersion int
	err := sharedPool.QueryRow(ctx,
		"SELECT encrypted_data, iv, auth_tag, sensor_type, key_version FROM telemetry WHERE tenant_id = $1 AND sensor_id = $2",
		"tenant-single", "sensor-single",
	).Scan(&encData, &iv, &authTag, &sensorType, &keyVersion)
	require.NoError(t, err)

	// Rule Zero: stored verbatim.
	assert.Equal(t, "c2luZ2xl", encData)
	assert.Equal(t, "aXZz", iv)
	assert.Equal(t, "dGFncw==", authTag)
	assert.Equal(t, "pressure", sensorType)
	assert.Equal(t, 3, keyVersion)
}

func TestPostgresTelemetryWriterIntegrationWriteBatch(t *testing.T) {
	truncateTelemetry(t)
	ctx := context.Background()

	const tenantDBTest = "tenant-db-test"

	writer := driven.NewPostgresTelemetryWriter(sharedPool)

	ts := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)
	rows := []model.TelemetryRow{
		{
			Time:          ts,
			TenantID:      tenantDBTest,
			GatewayID:     "gw-001",
			SensorID:      "sensor-A",
			SensorType:    "temperature",
			EncryptedData: model.OpaqueBlob{Value: "aGVsbG8gd29ybGQ="},
			IV:            model.OpaqueBlob{Value: "aXZkYXRh"},
			AuthTag:       model.OpaqueBlob{Value: "dGFnZGF0YQ=="},
			KeyVersion:    1,
		},
		{
			Time:          ts.Add(time.Second),
			TenantID:      tenantDBTest,
			GatewayID:     "gw-001",
			SensorID:      "sensor-B",
			SensorType:    "humidity",
			EncryptedData: model.OpaqueBlob{Value: "c2Vjb25kZW5j"},
			IV:            model.OpaqueBlob{Value: "aXYy"},
			AuthTag:       model.OpaqueBlob{Value: "dGFnMg=="},
			KeyVersion:    2,
		},
	}

	require.NoError(t, writer.WriteBatch(ctx, rows))

	type dbRow struct {
		encryptedData, iv, authTag string
		keyVersion                 int
	}
	var got []dbRow

	dbRows, err := sharedPool.Query(ctx,
		"SELECT encrypted_data, iv, auth_tag, key_version FROM telemetry WHERE tenant_id = $1 ORDER BY time",
		tenantDBTest,
	)
	require.NoError(t, err)
	defer dbRows.Close()
	for dbRows.Next() {
		var r dbRow
		require.NoError(t, dbRows.Scan(&r.encryptedData, &r.iv, &r.authTag, &r.keyVersion))
		got = append(got, r)
	}
	require.NoError(t, dbRows.Err())
	require.Len(t, got, 2)

	// Rule Zero: encrypted_data, iv, auth_tag are stored verbatim — never decoded or re-encoded.
	assert.Equal(t, "aGVsbG8gd29ybGQ=", got[0].encryptedData)
	assert.Equal(t, "aXZkYXRh", got[0].iv)
	assert.Equal(t, "dGFnZGF0YQ==", got[0].authTag)
	assert.Equal(t, 1, got[0].keyVersion)

	assert.Equal(t, "c2Vjb25kZW5j", got[1].encryptedData)
	assert.Equal(t, "aXYy", got[1].iv)
	assert.Equal(t, "dGFnMg==", got[1].authTag)
	assert.Equal(t, 2, got[1].keyVersion)
}
