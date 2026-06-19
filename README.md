# Lab Monitor

**Reliable, real-time telemetry for a physics-lab scanning tunneling microscope (STM)** —
ingest, store, visualize, alert, and remotely control the vacuum and cryogenic
instrumentation. Built end-to-end and **deployed on AWS, ingesting real
instrument data** from the lab.

[![CI](https://github.com/yx3728/cryo-lab-telemetry/actions/workflows/ci.yml/badge.svg)](https://github.com/yx3728/cryo-lab-telemetry/actions/workflows/ci.yml)
[![Go](https://img.shields.io/badge/Go-1.23-00ADD8?logo=go&logoColor=white)](#)
[![TypeScript](https://img.shields.io/badge/TypeScript-React%2018-3178C6?logo=typescript&logoColor=white)](#)
[![TimescaleDB](https://img.shields.io/badge/TimescaleDB-PostgreSQL-336791?logo=postgresql&logoColor=white)](#)
[![Docker](https://img.shields.io/badge/Docker-Compose-2496ED?logo=docker&logoColor=white)](#)
[![AWS](https://img.shields.io/badge/AWS-EC2%20(ARM)-FF9900?logo=amazonaws&logoColor=white)](#)
[![Caddy](https://img.shields.io/badge/Caddy-auto--HTTPS-1F88C0?logo=caddy&logoColor=white)](#)
[![License: MIT](https://img.shields.io/badge/License-MIT-green.svg)](./LICENSE)

### 🔗 Live: **https://3.220.132.187.sslip.io**  ·  public, read-only

![Lab Monitor dashboard](docs/dashboard.png)

*Live dashboard showing real instrument data — log-scale vacuum pressures and
cryogenic temperatures (note the real sorb-pump regeneration: SORB warms, then the
stage cools back to ~4 K). Live values, time-range selection (15 m … 90 d),
per-channel CSV export, and a JWT-gated admin panel.*

---

## Why it exists

The lab logged to a Grafana Cloud free tier with hard limits — **30-day
retention, no bulk export, no remote control of sampling**. Lab Monitor replaces
it with a stack the lab fully owns: **unlimited history, one-click CSV, threshold
email alerts, and remote configuration**, wired to the live instruments **without
modifying the lab's existing acquisition software**.

## Architecture

```
Lab PC (Python / PyVISA)            AWS EC2 — Docker Compose                Browser (anywhere)
  instruments ─reuse─┐                Caddy  (reverse proxy · auto-HTTPS)
                     ▼                 ├─ Go ingest + API ── public read ──▶ React dashboard
  collector ─HTTPS POST /ingest─────▶ │   3-tier auth · idempotent ── JWT ─▶ admin / config
   batch · retry · offline buffer    └─ TimescaleDB (hypertable + rollups)
   polls /api/config ◀── control ───   Go alerter ──▶ email (debounced, capped)
```

- **Python** owns acquisition (reuses the lab's working GPIB/PyVISA code).
- **Go** owns the cloud: concurrent, idempotent ingest + the API.
- **TimescaleDB** owns storage: SQL, unlimited retention, fast time-series rollups.
- **Caddy** owns TLS + routing (real Let's Encrypt cert, auto-renewed).

## Highlights

- **Idempotent, batched ingest** in Go (`ON CONFLICT DO NOTHING` on a TimescaleDB
  hypertable) — retries and replays never double-write.
- **Three-tier auth** — per-source API tokens (ingest), public reads, JWT admin
  (control). A leaked lab token can only write its own instrument's data.
- **Reliable delivery** — write-ahead offline disk buffer + retry/backoff;
  **verified zero data loss** across a service restart, a 22 s network outage, a
  DB restart, and a full host reboot.
- **Load-tested on the real box** — batching lifts ingest **~21×** (≈1,060 →
  ≈22,300 readings/s); a **continuous aggregate** makes a 30-day query **~28×**
  faster (~3.2 s → ~0.12 s). Bottleneck identified by measurement, fixes applied,
  scaling roadmap documented — and deliberately *not* over-built.
- **Closed control loop** — admins change the sampling rate from the dashboard;
  the edge producer polls and adopts it live.
- **Real-lab integration without touching the lab's software** — a read-only
  InfluxDB→AWS mirror plus a high-rate bridge client; **30 days of history
  backfilled** so data outlives the old retention cap.
- **Threshold email alerting** with per-metric debounce and a daily cap.

## Tech stack

**Backend** Go · chi · pgx (no ORM) · golang-jwt · TimescaleDB / PostgreSQL ·
embedded SQL migrations · `net/smtp`
**Frontend** React 18 · TypeScript (strict) · Recharts · Vite
**Edge** Python 3 · PyVISA (reused) · `requests` · durable disk queue · `influxdb-client`
**Infra** Docker (multi-stage, distroless, ARM) · Docker Compose · Caddy 2 · AWS EC2 (Graviton)
**Quality** Go `testing` (unit + DB integration) · `pytest` · custom Go load/chaos harness

## Repository layout

```
server/      Go ingest + API (chi, pgx); embedded migrations; unit + integration tests
collector/   Python edge collector — MockReader / RealReader, retry, offline buffer, config loop
forwarder/   Read-only InfluxDB → AWS mirror + one-time history backfill (runs on EC2)
lab/         High-rate (1 Hz) instrument → AWS producer for the lab PC
mock/         Chaos + load harness (latency/loss/disconnect; concurrent + high-frequency)
bench/        Go load + reliability harness used for BENCHMARKS.md
web/          React + TS + Recharts dashboard (public charts + admin panel)
deploy/       docker-compose.yml (prod) + Caddyfile (auto-HTTPS) + deploy.sh
docs/         Hardware↔software interface, screenshots
```

## Quick start (local, against the mock)

Requires Docker (+ Compose), Go 1.23+, Node 18+, Python 3.10+.

```bash
cp .env.example .env                                   # dev defaults work as-is
docker compose -f deploy/docker-compose.dev.yml up -d --build   # TimescaleDB + API

python3 -m venv collector/.venv && source collector/.venv/bin/activate
pip install -r collector/requirements.txt
INGEST_URL=http://localhost:8080 INGEST_TOKEN=dev-ingest-token \
  SOURCE=unisoku-stm python collector/collector.py     # realistic mock feed

cd web && npm install && npm run dev                    # http://localhost:5173
```

## Testing

```bash
cd server && go test ./...                                          # Go unit (hermetic)
TEST_DATABASE_URL=postgres://labmon:labmon-dev-password@localhost:5432/labmon?sslmode=disable \
  go test ./internal/store -run Integration                        # DB integration
cd collector && ./.venv/bin/python -m pytest tests/ -q             # Python unit
INGEST_TOKEN=loadtest-token ./collector/.venv/bin/python mock/load_test.py   # chaos/load
```

## Documentation

| Doc | What |
|---|---|
| [REPORT.md](./REPORT.md) | Full technical report — stack, decisions, measured outcomes |
| [PLAN.md](./PLAN.md) | Architecture & design rationale |
| [BENCHMARKS.md](./BENCHMARKS.md) | Load + reliability results, bottleneck analysis, scaling roadmap |
| [docs/HARDWARE_INTERFACE.md](./docs/HARDWARE_INTERFACE.md) | Instrument ↔ software interface (Lake Shore 350, SCPI) |
| [WIRING.md](./WIRING.md) | How the live lab connects to the system |
| [WORKLOG.md](./WORKLOG.md) | Append-only build/test/deploy log |

## Scope (honest)

In scope: the existing few-second, multi-channel vacuum + temperature telemetry,
end-to-end, with alerting and remote config — running in production on real data.
A planned ~50 Hz fast STM-current channel is **designed for and load-tested**, but
not yet connected (never claimed live). No consensus/sharding/broker/Kubernetes —
unnecessary at this scale and deliberately left as a documented roadmap.

## License

[MIT](./LICENSE)
