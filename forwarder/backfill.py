"""One-time backfill of InfluxDB Cloud history -> AWS.

InfluxDB Cloud (free tier) discards data after 30 days. This pulls the entire
retained window (temperatures + pressures) into AWS TimescaleDB so nothing is
lost. It is idempotent: each point keeps its original InfluxDB timestamp, so
re-running it (or overlapping with the live forwarder, which also uses InfluxDB
timestamps) never creates duplicates — the AWS ingest dedups on
(source, metric, ts).

Reads everything day-by-day to bound memory; posts in large batches. Config via
the same env vars as the forwarder (INFLUX_*, INGEST_URL, INGEST_TOKEN, SOURCE),
plus BACKFILL_DAYS (default 31).

Run once, e.g. from a laptop:
    INFLUX_URL=... INFLUX_TOKEN=... INFLUX_ORG=... \
    INGEST_URL=https://3.220.132.187.sslip.io INGEST_TOKEN=... SOURCE=unisoku-stm \
      python backfill.py
"""

from __future__ import annotations

import os
import sys
from datetime import datetime, timedelta, timezone

import requests
from influxdb_client import InfluxDBClient


def env(name, default=None, required=False):
    v = os.environ.get(name, default)
    if required and not v:
        sys.exit(f"missing required env {name}")
    return v


INFLUX_URL = env("INFLUX_URL", required=True)
INFLUX_TOKEN = env("INFLUX_TOKEN", required=True)
INFLUX_ORG = env("INFLUX_ORG", required=True)
TEMP_BUCKET = env("INFLUX_TEMP_BUCKET", "Temperature_Logger")
PRESSURE_BUCKET = env("INFLUX_PRESSURE_BUCKET", "Pressure_Logger")
INGEST_URL = env("INGEST_URL", "https://3.220.132.187.sslip.io").rstrip("/") + "/ingest"
INGEST_TOKEN = env("INGEST_TOKEN", required=True)
SOURCE = env("SOURCE", "unisoku-stm")
DAYS = int(env("BACKFILL_DAYS", "31"))
BATCH = int(env("BACKFILL_BATCH", "5000"))

TEMP_MAP = {"SORB": "SORB", "1KPOT": "1K Pot", "HE3POT": "He3 Pot", "STM": "STM"}
PRESSURE_MAP = {"LL": "LL", "OC": "OC", "PREP": "PREP", "PC": "PC"}


def iso(dt) -> str:
    return dt.isoformat().replace("+00:00", "Z")


def post(session, batch):
    if not batch:
        return 0
    resp = session.post(INGEST_URL, json=batch, headers={"X-Api-Key": INGEST_TOKEN}, timeout=60)
    resp.raise_for_status()
    return resp.json().get("inserted", 0)


def day_query(bucket, field, start, stop):
    return (
        f'from(bucket: "{bucket}") '
        f'|> range(start: {iso(start)}, stop: {iso(stop)}) '
        f'|> filter(fn: (r) => r._field == "{field}") '
        f'|> keep(columns: ["label", "gauge", "_value", "_time"])'
    )


def main():
    client = InfluxDBClient(url=INFLUX_URL, token=INFLUX_TOKEN, org=INFLUX_ORG, timeout=120_000)
    q = client.query_api()
    session = requests.Session()

    end = datetime.now(timezone.utc) + timedelta(minutes=1)
    start = end - timedelta(days=DAYS)
    print(f"backfilling {iso(start)} .. {iso(end)} -> {INGEST_URL} source={SOURCE}", flush=True)

    total_read = total_inserted = 0
    batch: list[dict] = []

    def flush():
        nonlocal batch, total_inserted
        while batch:
            chunk, batch = batch[:BATCH], batch[BATCH:]
            total_inserted += post(session, chunk)

    day = start
    while day < end:
        nxt = min(day + timedelta(days=1), end)
        n_day = 0
        # temperatures
        for table in q.query(day_query(TEMP_BUCKET, "temperature", day, nxt), org=INFLUX_ORG):
            for r in table.records:
                metric = TEMP_MAP.get(r.values.get("label"))
                val = r.get_value()
                if metric is None or val is None:
                    continue
                batch.append({"source": SOURCE, "metric": metric, "ts": iso(r.get_time()), "value": float(val)})
                n_day += 1
        # pressures (skip non-positive — gauge off, not log-plottable)
        for table in q.query(day_query(PRESSURE_BUCKET, "pressure", day, nxt), org=INFLUX_ORG):
            for r in table.records:
                metric = PRESSURE_MAP.get(r.values.get("gauge"))
                val = r.get_value()
                if metric is None or val is None or val <= 0:
                    continue
                batch.append({"source": SOURCE, "metric": metric, "ts": iso(r.get_time()), "value": float(val)})
                n_day += 1
        total_read += n_day
        if len(batch) >= BATCH:
            flush()
        print(f"  {day.date()}: read {n_day:6d}  (cumulative read {total_read}, inserted {total_inserted})", flush=True)
        day = nxt

    flush()
    print(f"\nDONE: read {total_read} points, inserted {total_inserted} new "
          f"({total_read - total_inserted} were already present / deduped).", flush=True)
    client.close()


if __name__ == "__main__":
    main()
