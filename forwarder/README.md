# forwarder/ — InfluxDB → AWS mirror

A tiny, read-only service that copies what the lab already publishes to **InfluxDB
Cloud** (temperatures + pressures) into the **AWS lab-monitor**, so the AWS
dashboard replicates the Grafana view **without touching any lab hardware or the
acquisition suite**.

Why this design: the vacuum gauge is read over an *exclusive* serial port owned
by the PhD student's suite, so a second process can't read the hardware. InfluxDB
Cloud, however, allows many concurrent readers — so we tap that instead. Zero
collision, zero changes to the lab.

- **Read-only** InfluxDB consumer; runs on the EC2 box (a different machine from
  the lab PC entirely).
- **Idempotent:** forwards the last point per series stamped with its own InfluxDB
  timestamp, so polling faster than the lab logs produces no duplicates.
- Skips non-positive pressures (a gauge that's off reads 0, which isn't plottable
  on a log axis) — "off" shows as a gap, like the real gauges.
- Maps InfluxDB labels to the dashboard's channel names
  (`1KPOT→1K Pot`, `HE3POT→He3 Pot`, `LL/OC/PREP` pass through). The gauge does not
  report `PC`, so `PC` stays empty until its source is identified.

Config is entirely via environment variables (see repo `.env.example`):
`INFLUX_URL/TOKEN/ORG`, `INFLUX_TEMP_BUCKET`, `INFLUX_PRESSURE_BUCKET`,
`INGEST_URL`, `INGEST_TOKEN`, `SOURCE`, `FORWARDER_POLL_SECONDS`.

## Run (on the EC2 box, via compose)

It's a default service in `deploy/docker-compose.yml`. Put the InfluxDB
credentials in `deploy/.env` on the box, then:

```bash
cd deploy && docker compose up -d --build forwarder
```

## Run locally (test)

```bash
python -m venv .venv && ./.venv/bin/pip install -r requirements.txt
INFLUX_URL=... INFLUX_TOKEN=... INFLUX_ORG=... \
INGEST_URL=https://3.220.132.187.sslip.io INGEST_TOKEN=... SOURCE=unisoku-stm \
  ./.venv/bin/python forwarder.py
```
