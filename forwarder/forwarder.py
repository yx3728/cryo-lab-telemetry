"""InfluxDB -> AWS forwarder.

Mirrors what the lab already publishes to InfluxDB Cloud (temperatures and
pressures) into the self-hosted AWS lab-monitor, so the AWS dashboard replicates
Grafana without touching any lab hardware or the PhD student's acquisition suite.

It is a *read-only* InfluxDB consumer (InfluxDB Cloud allows many readers), so it
never collides with the suite's exclusive serial ports. It runs anywhere with
internet — here it runs on the EC2 box next to the AWS API.

Idempotency: each poll forwards the LAST point per series stamped with that
point's own InfluxDB timestamp. Re-reading an unchanged point posts the same
(source, metric, ts), which the AWS ingest dedups — so polling faster than the
lab logs is harmless (no duplicates).

All configuration is via environment variables (see .env.example). No secrets in
code.
"""

from __future__ import annotations

import os
import sys
import time

import requests
from influxdb_client import InfluxDBClient


def env(name: str, default: str | None = None, required: bool = False) -> str:
    val = os.environ.get(name, default)
    if required and not val:
        print(f"[fatal] missing required env {name}", flush=True)
        sys.exit(1)
    return val or ""


INFLUX_URL = env("INFLUX_URL", required=True)
INFLUX_TOKEN = env("INFLUX_TOKEN", required=True)
INFLUX_ORG = env("INFLUX_ORG", required=True)
TEMP_BUCKET = env("INFLUX_TEMP_BUCKET", "Temperature_Logger")
PRESSURE_BUCKET = env("INFLUX_PRESSURE_BUCKET", "Pressure_Logger")

INGEST_URL = env("INGEST_URL", "http://go-api:8080").rstrip("/") + "/ingest"
INGEST_TOKEN = env("INGEST_TOKEN", required=True)
SOURCE = env("SOURCE", "unisoku-stm")
POLL_SECONDS = float(env("FORWARDER_POLL_SECONDS", "15"))
LOOKBACK = env("FORWARDER_LOOKBACK", "-15m")
HTTP_TIMEOUT = float(env("HTTP_TIMEOUT_SECONDS", "10"))

# InfluxDB label/gauge -> AWS dashboard metric name.
TEMP_MAP = {"SORB": "SORB", "1KPOT": "1K Pot", "HE3POT": "He3 Pot", "STM": "STM"}
PRESSURE_MAP = {"LL": "LL", "OC": "OC", "PREP": "PREP", "PC": "PC"}

TEMP_FLUX = f'''
from(bucket: "{TEMP_BUCKET}")
  |> range(start: {LOOKBACK})
  |> filter(fn: (r) => r._measurement == "temperature_reading" and r._field == "temperature")
  |> last()
  |> keep(columns: ["label", "_value", "_time"])
'''

PRESSURE_FLUX = f'''
from(bucket: "{PRESSURE_BUCKET}")
  |> range(start: {LOOKBACK})
  |> filter(fn: (r) => r._measurement == "pressure_reading" and r._field == "pressure")
  |> last()
  |> keep(columns: ["gauge", "_value", "_time"])
'''


def iso(dt) -> str:
    """influxdb_client returns a tz-aware UTC datetime; render RFC3339 (Z)."""
    return dt.isoformat().replace("+00:00", "Z")


def collect(query_api, flux: str, tag: str, mapping: dict, drop_nonpositive: bool) -> list[dict]:
    """Run a Flux 'last per series' query and build AWS reading dicts."""
    out: list[dict] = []
    tables = query_api.query(flux, org=INFLUX_ORG)
    for table in tables:
        for rec in table.records:
            label = rec.values.get(tag)
            metric = mapping.get(label)
            if metric is None:
                continue
            value = rec.get_value()
            if value is None:
                continue
            # On a log pressure axis a 0/negative reading (gauge off) is not
            # plottable; skip it so "off" shows as a gap, matching the gauges.
            if drop_nonpositive and value <= 0:
                continue
            out.append({"source": SOURCE, "metric": metric,
                        "ts": iso(rec.get_time()), "value": float(value)})
    return out


def post(batch: list[dict]) -> None:
    if not batch:
        return
    try:
        resp = requests.post(INGEST_URL, json=batch,
                             headers={"X-Api-Key": INGEST_TOKEN}, timeout=HTTP_TIMEOUT)
        if resp.status_code >= 300:
            print(f"[ingest] HTTP {resp.status_code}: {resp.text[:200]}", flush=True)
    except requests.RequestException as exc:
        print(f"[ingest] post failed: {exc}", flush=True)


def main() -> None:
    print(f"forwarder up: {INFLUX_URL} ({TEMP_BUCKET},{PRESSURE_BUCKET}) -> {INGEST_URL} "
          f"source={SOURCE} every {POLL_SECONDS:g}s", flush=True)
    client = InfluxDBClient(url=INFLUX_URL, token=INFLUX_TOKEN, org=INFLUX_ORG, timeout=int(HTTP_TIMEOUT * 1000))
    query_api = client.query_api()
    try:
        while True:
            batch: list[dict] = []
            try:
                batch += collect(query_api, TEMP_FLUX, "label", TEMP_MAP, drop_nonpositive=False)
                batch += collect(query_api, PRESSURE_FLUX, "gauge", PRESSURE_MAP, drop_nonpositive=True)
            except Exception as exc:  # InfluxDB read error — log, keep looping
                print(f"[influx] query failed: {exc}", flush=True)
            post(batch)
            time.sleep(POLL_SECONDS)
    finally:
        client.close()


if __name__ == "__main__":
    main()
