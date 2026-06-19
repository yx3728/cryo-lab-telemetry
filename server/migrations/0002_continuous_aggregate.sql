-- migrate:no-transaction
-- 0002: continuous aggregate for fast long-range reads.
--
-- A 1-minute rollup (avg/min/max + count) of readings, maintained incrementally
-- by TimescaleDB. Wide-range dashboard queries ("last 30 days") read pre-bucketed
-- rows from here instead of scanning millions of raw points. Real-time
-- aggregation is on by default, so the most recent (not-yet-materialized) minutes
-- are still computed from raw data — queries never miss live points.
--
-- This file is marked no-transaction (see the runner): CREATE MATERIALIZED VIEW
-- ... WITH (timescaledb.continuous) cannot run inside a transaction block.

CREATE MATERIALIZED VIEW IF NOT EXISTS readings_1m
WITH (timescaledb.continuous) AS
SELECT source,
       metric,
       time_bucket('1 minute', ts) AS bucket,
       avg(value) AS avg_value,
       min(value) AS min_value,
       max(value) AS max_value,
       count(*)   AS n_value          -- weight for correct re-bucketing to coarser steps
FROM readings
GROUP BY source, metric, time_bucket('1 minute', ts)
WITH NO DATA;

-- Refresh the last 30 days every 10 minutes. Refreshes are incremental — only
-- buckets invalidated by new/changed rows are re-materialized — so a wide
-- start_offset is cheap. The most recent 10 minutes are left to real-time
-- aggregation so live data is never delayed behind the materializer.
SELECT add_continuous_aggregate_policy('readings_1m',
    start_offset      => INTERVAL '30 days',
    end_offset        => INTERVAL '10 minutes',
    schedule_interval => INTERVAL '10 minutes',
    if_not_exists     => true);
