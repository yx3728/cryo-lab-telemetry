-- 0004: enable real-time aggregation on the continuous aggregates.
--
-- Recent TimescaleDB defaults new continuous aggregates to materialized_only =
-- true, which returns ONLY already-materialized buckets — so the newest points
-- (since the last refresh) would be missing. With real-time aggregation on, a
-- query unions the materialized rollup with the most recent raw data computed on
-- the fly, so live (sub-minute, routed to readings_1s) and wide (readings_1m)
-- views always include the latest readings. Plain ALTERs — transaction-safe.

ALTER MATERIALIZED VIEW readings_1m SET (timescaledb.materialized_only = false);
ALTER MATERIALIZED VIEW readings_1s SET (timescaledb.materialized_only = false);
