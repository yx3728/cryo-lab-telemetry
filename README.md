# Lab Monitor — reliable multi-channel telemetry for a physics lab

Real-time monitoring, history, alerting and remote configuration for the vacuum
and cryogenic channels of a **Unisoku scanning-tunneling-microscope (STM)** rig.
It replaces a Grafana free-tier dashboard (30-day retention cap, no easy export,
no remote control of sampling) with a self-hosted stack the lab fully owns.

> **Status:** built and load-tested end-to-end against a high-fidelity mock of
> the instruments, then deployed to AWS. The edge collector reuses the lab's
> existing PyVISA instrument code, so wiring it to the real rig is a one-file
> change (see [`WIRING.md`](./WIRING.md)).

**Live:** `https://3.220.132.187.sslip.io`

![Dashboard](docs/dashboard.png)

*Public dashboard: log-scale vacuum pressures and linear cryogenic temperatures
(note the STM thermal spike to ~21 K decaying back to 4.2 K), with live values,
time-range selection, per-channel CSV export, and a JWT-gated admin panel.*

---

## What it does

- **Ingests** batches of time-series readings (vacuum pressure in Torr,
  temperatures in Kelvin) from an instrument PC over HTTPS.
- **Stores** them in **TimescaleDB** (Postgres) with unlimited retention and SQL.
- **Serves** a public, read-only **React dashboard**: per-channel live + history
  charts, time-range selection, and CSV export.
- **Alerts** (email / Slack, debounced) when a channel crosses a threshold.
- **Controls** the instrument PC remotely: an admin sets the sampling interval
  and thresholds in the UI; the collector polls and applies them — a closed
  configuration loop.

## Architecture

```
Lab PC (Windows, Python)          AWS EC2 (Docker)                        Browser (anywhere)
  instruments → PyVISA  (reuse)     Caddy (auto-HTTPS, Let's Encrypt)
        │                            ├─ Go ingest + API ── public read ──▶ React dashboard
        ▼                            │   (3-tier auth,   ── login (JWT) ─▶ admin config UI
  Python collector ─HTTPS POST──────▶│    idempotent writes)
   batch · retry+backoff            └─ TimescaleDB (Postgres hypertable)
   offline disk buffer ◀── config ── Go alerter → email / Slack
   polls /api/config  ──────────────▶
```

### Three-tier auth (a deliberate design point)

| Plane     | Who                | Mechanism                              | Gates                       |
|-----------|--------------------|----------------------------------------|-----------------------------|
| **Ingest**| instrument → cloud | per-source API token (`X-Api-Key`)     | `POST /ingest`              |
| **Read**  | anyone → dashboard | public / anonymous                     | `GET /api/series`, CSV      |
| **Control**| admin → config    | username/password → **JWT** (HS256)    | `PUT /api/config`, admin UI |

Each ingest token is bound to exactly one source, so a leaked lab token can only
write that instrument's data — never forge another's.

## Tech stack & why

| Choice | Rationale |
|--------|-----------|
| **Go** (chi + pgx) for ingest/API | The honest home for concurrency: many producers, goroutine-per-request, idempotent writes. No ORM — every SQL query is explicit and owned. |
| **Python** at the edge | The lab's instrument I/O already works in PyVISA (GPIB needs NI-VISA). Reuse it; don't rewrite hardware I/O in Go. |
| **TimescaleDB** | Postgres + time-series: `time_bucket` downsampling, unlimited retention, trivial CSV export — the things the Grafana free tier wouldn't give. |
| **React + TS + Recharts** | Standard, typed dashboard; builds to static files served by Caddy. |
| **Caddy** | Automatic HTTPS (Let's Encrypt) for `sslip.io` with a one-line config. |
| **Docker Compose** | One `up` brings the whole stack up on the ARM EC2 box. |

## Scope (honest)

**In scope (MVP):** the existing few-second-cadence, multi-channel vacuum +
temperature telemetry, flowing end-to-end with alerting and remote config.

**Designed for, not yet live:** a planned ~2 ms / 50 Hz fast STM-current channel
lives on a *separate, not-yet-connected* computer. The ingest path takes
per-source tokens and the system is **load-tested for that future channel's
headroom** (see [load test](#testing)) — but it is **not claimed to be live**.

**Explicitly out of scope:** consensus, sharding, custom brokers/queues,
Kubernetes, "web-scale" throughput. The real engineering here is *reliable
delivery* and *correct handling of concurrent producers*, not raw QPS.

## Repository layout

```
server/      Go ingest + API service (chi, pgx), embedded SQL migrations, tests
collector/   Python edge collector — RealReader (PyVISA) + MockReader,
             batching, retry+backoff, offline disk buffer, config polling
mock/        Chaos + load harness (latency/loss/disconnect; high-freq + N producers)
web/         React + TS + Recharts dashboard (public charts + admin panel)
deploy/      docker-compose.yml (prod) + Caddyfile (auto-HTTPS)
PLAN.md      Architecture, design decisions, scope rationale
WORKLOG.md   Append-only build log (findings, test numbers, deploy verification)
WIRING.md    How to point the real lab PC at this system (last step)
```

## Quick start (local, against the mock)

Requires Docker (+ Compose), Go 1.23+, Node 18+, Python 3.10+.

```bash
# 1. Bring up TimescaleDB + the Go API (migrations run automatically at boot)
cp .env.example .env                       # dev defaults work as-is
docker compose -f deploy/docker-compose.dev.yml up -d --build

# 2. Run the collector with the built-in mock instrument feed
python3 -m venv collector/.venv && source collector/.venv/bin/activate
pip install -r collector/requirements.txt
INGEST_URL=http://localhost:8080 INGEST_TOKEN=dev-ingest-token-change-me \
  SOURCE=unisoku-stm python collector/collector.py

# 3. Run the dashboard
cd web && npm install && npm run dev        # http://localhost:5173
```

See [`PLAN.md`](./PLAN.md) for the full design and [`WORKLOG.md`](./WORKLOG.md)
for the build/test/deploy record.

## Testing

- **Unit (Go):** ingest validation, dedupe/idempotency, all three auth planes,
  config apply, alert threshold logic.
- **Integration:** collector → ingest → DB → read API round-trip vs. the mock.
- **Load / chaos:** mock producers with injected latency, packet loss and
  disconnects, asserting **no data loss**, **correct ordering**, and stability
  under a simulated high-frequency producer plus multiple concurrent producers.
  Numbers are recorded in [`WORKLOG.md`](./WORKLOG.md).

## License

MIT (see `LICENSE`).
