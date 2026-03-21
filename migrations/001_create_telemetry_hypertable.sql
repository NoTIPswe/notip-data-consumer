-- Migration: 001_create_telemetry_hypertable
-- Creates the telemetry table and converts it to a TimescaleDB hypertable.
-- Encrypted payload columns (encrypted_data, iv, auth_tag) are stored as TEXT
-- (base64 strings) and are NEVER decoded server-side — Rule Zero.

CREATE TABLE IF NOT EXISTS telemetry (
    time           TIMESTAMPTZ        NOT NULL,
    tenant_id      TEXT               NOT NULL,
    gateway_id     TEXT               NOT NULL,
    sensor_id      TEXT               NOT NULL,
    sensor_type    TEXT               NOT NULL,
    encrypted_data TEXT               NOT NULL,
    iv             TEXT               NOT NULL,
    auth_tag       TEXT               NOT NULL,
    key_version    INTEGER            NOT NULL
);

-- Convert to a TimescaleDB hypertable partitioned by the gateway timestamp.
-- Using 1-day chunks balances query locality against chunk count.
SELECT create_hypertable(
    'telemetry',
    'time',
    chunk_time_interval => INTERVAL '1 day',
    if_not_exists       => TRUE
);

-- Composite index optimised for per-gateway time-range queries.
-- Covering tenant_id first narrows the scan before gateway_id and time.
CREATE INDEX IF NOT EXISTS idx_telemetry_tenant_gateway_time
    ON telemetry (tenant_id, gateway_id, time DESC);
