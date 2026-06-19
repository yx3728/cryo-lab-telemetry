-- 0001_init: core schema for the lab-monitor platform.
--
-- Applied automatically at server boot by the embedded migration runner
-- (see internal/store/migrate.go). Each migration runs once inside a
-- transaction and is recorded in schema_migrations.

-- TimescaleDB turns Postgres into a time-series database. The extension ships
-- with the timescale/timescaledb image; this is a no-op if already present.
CREATE EXTENSION IF NOT EXISTS timescaledb;

-- readings is the single fact table: one row per (source, metric, timestamp).
--   source  e.g. 'unisoku-stm'         (which instrument PC sent it)
--   metric  e.g. 'OC', 'SORB', 'STM'   (which channel)
--   ts      the sample time (authoritative, set by the instrument)
--   value   the reading (Torr for pressure, Kelvin for temperature)
CREATE TABLE IF NOT EXISTS readings (
    source TEXT             NOT NULL,
    metric TEXT             NOT NULL,
    ts     TIMESTAMPTZ      NOT NULL,
    value  DOUBLE PRECISION NOT NULL,
    -- The unique key is what makes ingest idempotent: a retried or replayed
    -- batch hits ON CONFLICT DO NOTHING. The partition column `ts` is part of
    -- the key, as TimescaleDB requires for unique indexes on a hypertable.
    UNIQUE (source, metric, ts)
);

-- Promote readings to a hypertable partitioned on ts. if_not_exists keeps this
-- idempotent if the migration is ever re-run against an existing table.
SELECT create_hypertable('readings', 'ts', if_not_exists => TRUE);

-- Helpful index for the read API's "latest per channel" and range queries.
CREATE INDEX IF NOT EXISTS readings_source_metric_ts_idx
    ON readings (source, metric, ts DESC);

-- config is a tiny key/value table for control-plane settings the admin edits
-- and the collector polls. sampling_interval_seconds is the closed-loop knob.
CREATE TABLE IF NOT EXISTS config (
    key        TEXT PRIMARY KEY,
    value      TEXT        NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO config (key, value) VALUES ('sampling_interval_seconds', '5')
    ON CONFLICT (key) DO NOTHING;

-- alert_threshold drives both server-side alerting and the admin UI editor.
-- A reading below min_value or above max_value (when enabled) fires an alert.
CREATE TABLE IF NOT EXISTS alert_threshold (
    metric    TEXT PRIMARY KEY,
    min_value DOUBLE PRECISION,
    max_value DOUBLE PRECISION,
    enabled   BOOLEAN NOT NULL DEFAULT TRUE
);

-- Seed a couple of sensible thresholds so alerting is demonstrable out of the
-- box. STM normally sits ~4.2 K and spikes during events; OC vacuum should stay
-- well below 1e-7 Torr. Admins can edit/disable these in the UI.
INSERT INTO alert_threshold (metric, min_value, max_value, enabled) VALUES
    ('STM', NULL, 20.0,  TRUE),
    ('OC',  NULL, 1e-7,  TRUE)
    ON CONFLICT (metric) DO NOTHING;

-- alert_log is an audit trail of every threshold cross, whether or not a
-- notifier (email/Slack) was configured to deliver it.
CREATE TABLE IF NOT EXISTS alert_log (
    id              BIGSERIAL PRIMARY KEY,
    source          TEXT,
    metric          TEXT             NOT NULL,
    value           DOUBLE PRECISION NOT NULL,
    kind            TEXT             NOT NULL,  -- 'min' or 'max'
    threshold_value DOUBLE PRECISION NOT NULL,
    fired_at        TIMESTAMPTZ      NOT NULL DEFAULT now(),
    notified        BOOLEAN          NOT NULL DEFAULT FALSE
);

CREATE INDEX IF NOT EXISTS alert_log_fired_at_idx ON alert_log (fired_at DESC);
