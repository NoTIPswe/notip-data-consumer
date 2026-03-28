package model

import (
	"encoding/json"
	"time"
)

// OpaqueBlob is a named type for a base64-encoded payload.
type OpaqueBlob struct {
	Value string
}

func (o *OpaqueBlob) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	o.Value = s
	return nil
}

func (o OpaqueBlob) MarshalJSON() ([]byte, error) {
	return json.Marshal(o.Value)
}

// SensorType enumerates the valid sensor types from the AsyncAPI contract.
type SensorType string

const (
	SensorTypeTemperature SensorType = "temperature"
	SensorTypeHumidity    SensorType = "humidity"
	SensorTypeMovement    SensorType = "movement"
	SensorTypePressure    SensorType = "pressure"
	SensorTypeBiometric   SensorType = "biometric"
)

// TelemetryEnvelope is the wire format of a NATS message on telemetry.data.{tenantId}.{gwId}.
type TelemetryEnvelope struct {
	GatewayID     string     `json:"gatewayId"`
	SensorID      string     `json:"sensorId"`
	SensorType    SensorType `json:"sensorType"`
	Timestamp     time.Time  `json:"timestamp"`
	KeyVersion    int        `json:"keyVersion"`
	EncryptedData OpaqueBlob `json:"encryptedData"`
	IV            OpaqueBlob `json:"iv"`
	AuthTag       OpaqueBlob `json:"authTag"`
}

// TelemetryRow is the normalised record written to TimescaleDB.
type TelemetryRow struct {
	Time          time.Time
	TenantID      string
	GatewayID     string
	SensorID      string
	SensorType    string
	EncryptedData OpaqueBlob
	IV            OpaqueBlob
	AuthTag       OpaqueBlob
	KeyVersion    int
}

// AlertPayload is published to alert.gw_offline.{tenantId} on an offline transition.
type AlertPayload struct {
	GatewayID string    `json:"gatewayId"`
	LastSeen  time.Time `json:"lastSeen"`
	TimeoutMs int64     `json:"timeoutMs"`
	Timestamp time.Time `json:"timestamp"`
}

// AlertConfig is one entry from the Management API alert configuration list.
type AlertConfig struct {
	TenantID  string  `json:"tenant_id"`
	GatewayID *string `json:"gateway_id,omitempty"`
	TimeoutMs int64   `json:"timeout_ms"`
}

// GatewayStatus represents the online/offline state of a gateway.
type GatewayStatus string

const (
	Offline GatewayStatus = "offline"
	Online  GatewayStatus = "online"
)

// GatewayStatusUpdate is the payload for the internal.mgmt.gateway.update-status RR call.
type GatewayStatusUpdate struct {
	GatewayID  string        `json:"gateway_id"`
	Status     GatewayStatus `json:"status"`
	LastSeenAt time.Time     `json:"last_seen_at"`
}

// GatewayStatusUpdateResponse is the response from the internal.mgmt.gateway.update-status RR call.
type GatewayStatusUpdateResponse struct {
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}
