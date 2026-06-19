-- migrate:no-transaction
-- 0003: 1-second continuous aggregate.
--
-- Sibling to the 1-minute rollup (0002), for fast SUB-minute queries on
-- high-rate channels. Wide ranges (step >= 60 s) read readings_1m; narrower
-- ranges (step 1-59 s) read readings_1s instead of scanning raw — which matters
-- once a channel logs at ~1 Hz or the planned high-frequency STM channel lands.
-- At the current few-second cadence each 1 s bucket holds ~one point (so it's a
-- harmless no-op now); it pays off as the data rate rises.
--
-- no-transaction: CREATE MATERIALIZED VIEW ... WITH (timescaledb.continuous)
-- cannot run inside a transaction block.

CREATE MATERIALIZED VIEW IF NOT EXISTS readings_1s
WITH (timescaledb.continuous) AS
SELECT source,
       metric,
       time_bucket('1 second', ts) AS bucket,
       avg(value) AS avg_value,
       min(value) AS min_value,
       max(value) AS max_value,
       count(*)   AS n_value
FROM readings
GROUP BY source, metric, time_bucket('1 second', ts)
WITH NO DATA;

-- Keep the last 2 days of 1 s buckets continuously fresh (older buckets are
-- materialized once and rarely re-queried at 1 s resolution). The most recent
-- 10 s is left to real-time aggregation so live views never lag.
SELECT add_continuous_aggregate_policy('readings_1s',
    start_offset      => INTERVAL '2 days',
    end_offset        => INTERVAL '10 seconds',
    schedule_interval => INTERVAL '1 minute',
    if_not_exists     => true);
