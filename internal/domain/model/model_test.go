package model

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── OpaqueBlob ───────────────────────────────────────────────────────────────

func TestOpaqueBlobUnmarshalJSONPreservesValue(t *testing.T) {
	raw := `"ZW5jcnlwdGVkZGF0YQ=="`
	var blob OpaqueBlob

	require.NoError(t, json.Unmarshal([]byte(raw), &blob))
	assert.Equal(t, "ZW5jcnlwdGVkZGF0YQ==", blob.Value,
		"UnmarshalJSON must store the base64 string verbatim — never decoded")
}

func TestOpaqueBlobMarshalJSONPreservesValue(t *testing.T) {
	blob := OpaqueBlob{Value: "ZW5jcnlwdGVkZGF0YQ=="}

	data, err := json.Marshal(blob)

	require.NoError(t, err)
	assert.Equal(t, `"ZW5jcnlwdGVkZGF0YQ=="`, string(data),
		"MarshalJSON must emit the base64 string verbatim — never re-encoded")
}

func TestOpaqueBlobRoundTrip(t *testing.T) {
	original := OpaqueBlob{Value: "aXY9dGVzdA=="}

	data, err := json.Marshal(original)
	require.NoError(t, err)

	var decoded OpaqueBlob
	require.NoError(t, json.Unmarshal(data, &decoded))

	assert.Equal(t, original.Value, decoded.Value, "round-trip must preserve the opaque value")
}

func TestOpaqueBlobEmptyValue(t *testing.T) {
	blob := OpaqueBlob{Value: ""}

	data, err := json.Marshal(blob)
	require.NoError(t, err)
	assert.Equal(t, `""`, string(data))

	var decoded OpaqueBlob
	require.NoError(t, json.Unmarshal(data, &decoded))
	assert.Equal(t, "", decoded.Value)
}

func TestOpaqueBlobUnmarshalJSONRejectsNonString(t *testing.T) {
	var blob OpaqueBlob
	err := json.Unmarshal([]byte(`123`), &blob)
	assert.Error(t, err, "UnmarshalJSON must reject non-string JSON values")
}

// ─── TelemetryEnvelope JSON ───────────────────────────────────────────────────

func TestTelemetryEnvelopeOpaqueFieldsUnmarshal(t *testing.T) {
	raw := `{
		"gatewayId":     "gw-1",
		"sensorId":      "s-1",
		"sensorType":    "temp",
		"timestamp":     "2025-01-15T12:00:00Z",
		"keyVersion":    2,
		"encryptedData": "ZW5j",
		"iv":            "aXY=",
		"authTag":       "YXV0"
	}`

	var env TelemetryEnvelope
	require.NoError(t, json.Unmarshal([]byte(raw), &env))

	assert.Equal(t, "ZW5j", env.EncryptedData.Value, "encryptedData must be stored as-is")
	assert.Equal(t, "aXY=", env.IV.Value, "iv must be stored as-is")
	assert.Equal(t, "YXV0", env.AuthTag.Value, "authTag must be stored as-is")
	assert.Equal(t, "gw-1", env.GatewayID)
	assert.Equal(t, 2, env.KeyVersion)
}
