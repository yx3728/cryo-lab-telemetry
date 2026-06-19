# Technical Report — Lab Instrument Monitoring Platform

A full-stack, deployed telemetry platform that ingests, stores, visualizes,
alerts on, and remotely controls the vacuum and cryogenic instrumentation of a
research-lab scanning tunneling microscope (STM). Built end-to-end and running in
production on AWS, ingesting **real instrument data** from the lab.

This document is the engineering record: what was built, why, and the measured
outcomes. The [`README`](./README.md) is the recruiter-facing summary;
[`PLAN.md`](./PLAN.md), [`BENCHMARKS.md`](./BENCHMARKS.md),
[`docs/HARDWARE_INTERFACE.md`](./docs/HARDWARE_INTERFACE.md), and
[`WIRING.md`](./WIRING.md) hold the detailed design, load results, hardware
interface, and lab-integration notes.

---

## 1. Executive summary

The lab logged instrument data to a Grafana Cloud free tier with hard limits:
30-day retention, no bulk export, and no remote control of sampling. This project
replaces that with a self-owned stack:

- **Go** ingest + API service with **idempotent** batch writes and a three-plane
  auth model, backed by **TimescaleDB** (Postgres) for unlimited-retention
  time-series.
- A **React + TypeScript** dashboard (public read; JWT-gated admin) with live
  charts, time-range selection, CSV export, and remote configuration.
- A **Python** edge layer that reuses the lab's existing PyVISA instrument code,
  adding reliable delivery (retry + offline buffering) and a remote control loop.
- Deployed on **AWS EC2** under **Docker Compose** behind **Caddy** with
  automatic HTTPS, **load- and failure-tested** on the real instance.
- Wired to the live lab **without modifying the lab's acquisition software**,
  with **email alerting** and a **30-day history backfill** so data outlives the
  old 30-day retention cap.

Headline measured results (real `t4g.small`, over the public internet):

| Result | Value |
|---|---|
| Ingest throughput, batched vs. unbatched | **≈22,300 vs ≈1,060 readings/s (~21×)** |
| Long-range read, continuous aggregate vs. raw | **~0.12 s vs ~3.2 s (~28×)** |
| Data loss across restart / 22 s network outage / DB restart / full reboot | **0** |
| Real history preserved into AWS | **257,597 points (30 days)** backfilled |

---

## 2. Problem & context

A physics lab runs a Unisoku STM with a closed-cycle cryostat (sorb pump, 1 K pot,
He-3 pot, STM stage) and a multi-channel vacuum gauge. An instrument PC samples
these every few seconds via PyVISA and uploaded to Grafana Cloud (free tier).

Constraints to remove:
- **30-day retention** — older data silently discarded.
- **No easy export** — hard to pull data for analysis.
- **No remote control** — sampling cadence fixed in code on the lab PC.

Design tenets carried throughout: **reuse the working edge code** (don't rewrite
GPIB/VISA I/O), **be honest about scale** (reliable multi-channel ingestion, not
"web-scale"), and **don't over-engineer** (no broker/sharding/k8s until the data
justifies it).

---

## 3. System architecture

```
Lab PC (Windows, Python)            AWS EC2 (Docker Compose)                 Browser (anywhere)
  instruments → PyVISA  (reuse)       Caddy  (reverse proxy, auto-HTTPS)
        │                              ├─ Go ingest + API ── public read ──▶ React dashboard
        ▼                              │   3-tier auth, idempotent writes ── login (JWT) ─▶ admin UI
  Python collector ─HTTPS POST───────▶ │   time_bucket + continuous aggs
   batch · retry+backoff              └─ TimescaleDB (hypertable + rollups)
   offline disk buffer ◀── config ──   Go alerter → email (debounced, daily cap)
   polls /api/config  ───────────────▶
                                        InfluxDB Cloud ──▶ forwarder (read-only) ──▶ ingest
                                        (lab's existing pipeline, mirrored to AWS)
```

Component boundaries are deliberate: **Python owns acquisition** (the lab's GPIB
hardware already works in PyVISA), **Go owns the cloud** (concurrency, idempotent
ingest, the API), **TimescaleDB owns storage** (SQL + time-series), **Caddy owns
TLS/routing**.

---

## 4. Technology stack

| Layer | Technology |
|---|---|
| Ingest + API | **Go** — chi (router), pgx v5 (no ORM), golang-jwt v5, `log/slog`, `net/smtp` |
| Database | **TimescaleDB** (PostgreSQL): hypertables, `time_bucket`, continuous aggregates, real-time aggregation |
| Frontend | **React 18 + TypeScript** (strict), Recharts, Vite |
| Edge / producers | **Python 3** — `pyserial`/PyVISA (reused), `requests`, stdlib sockets, `influxdb-client` |
| Reverse proxy / TLS | **Caddy 2** (automatic Let's Encrypt via `sslip.io`) |
| Packaging / deploy | **Docker** (multi-stage, distroless, ARM/`linux/arm64`), **Docker Compose** |
| Cloud | **AWS EC2** (`t4g.small`, Graviton/ARM), Elastic IP |
| Load/bench tooling | **Go** (custom open-loop, coordinated-omission-corrected harness) |
| Tests | Go `testing` (unit + DB integration), `pytest`, a chaos/load harness |

---

## 5. Data model & storage

A single fact table, promoted to a TimescaleDB hypertable:

```sql
readings(source TEXT, metric TEXT, ts TIMESTAMPTZ, value DOUBLE PRECISION)
        -- hypertable on ts; UNIQUE (source, metric, ts)
config(key, value, updated_at)                 -- sampling interval (control loop)
alert_threshold(metric, min_value, max_value, enabled)
alert_log(id, source, metric, value, kind, threshold_value, fired_at, notified)
```

- The `UNIQUE (source, metric, ts)` constraint is the basis of **idempotent
  ingest**: retried or replayed batches `ON CONFLICT DO NOTHING`. (The unique key
  includes the partition column, as TimescaleDB requires.)
- **Continuous aggregates** `readings_1m` (and `readings_1s`) pre-bucket the data
  with `avg/min/max/count`; the read API routes wide ranges to the rollups and
  re-buckets them with **count-weighted averages** (exact, not average-of-
  averages). **Real-time aggregation** is enabled so live views still include the
  newest, not-yet-materialized points.
- **Migrations are embedded** in the Go binary (`embed.FS`) and applied at boot in
  per-file transactions, tracked in `schema_migrations`. A no-transaction path
  handles DDL that can't run in a transaction (continuous-aggregate creation).

---

## 6. Backend service (Go)

- **`POST /ingest`** — per-source API token (`X-Api-Key`); batch body; idempotent
  multi-row INSERT; fires alert evaluation; updates in-memory telemetry. Source is
  taken from the token, never trusted from the body.
- **`GET /api/series`**, **`GET /api/export.csv`** — public; downsampled via
  `time_bucket`/rollups; CSV supports single-metric or whole-source export.
- **`GET /api/channels`** — public discovery (latest value per channel).
- **`GET /api/config` / `PUT /api/config`** — read public, write JWT-gated;
  sampling interval + thresholds.
- **`POST /api/login`** — username/password → HS256 JWT.
- **`GET /healthz`, `GET /metrics`** — liveness + honest telemetry (uptime, rows
  ingested, per-source last-seen; counters that reset on restart are labeled as
  such, alongside DB-backed totals).

**Three-tier auth** (a deliberate design point):

| Plane | Who | Mechanism |
|---|---|---|
| Ingest | instrument → cloud | per-source API token, source-bound, revocable |
| Read | anyone → dashboard | public / anonymous |
| Control | admin → config | username/password → JWT (HS256) |

Token comparison is constant-time; the JWT verifier pins the signing algorithm
(rejecting `alg:none`/confusion). Handlers are stateless; the DB is the only
state, so any number run concurrently (goroutine per request). All configuration
and secrets come from the environment (12-factor); none are in source.

---

## 7. Edge layer & producers (Python)

- **Collector** (`collector/`) — the deployable artifact. Pluggable read path:
  `MockReader` (high-fidelity simulator for dev) and `RealReader` (template that
  reuses the lab's existing PyVISA queries). Acquisition is **decoupled from
  delivery** via a durable on-disk FIFO: every batch is journaled to disk
  (atomic temp-then-rename) before any send, so a crash or outage loses nothing;
  delivery drains the queue **oldest-first** with retry + exponential backoff and
  drops only "poison" 4xx batches. A config-poll thread adopts the admin-set
  sampling interval — the closed control loop.
- **Mock + chaos/load harness** (`mock/`) — reproduces the real channels and
  injects latency / loss / disconnect for the reliability test.
- **Signal model** — a stdlib simulator reproducing the real channels' shapes
  (vacuum log-scale baselines with spikes, sorb-pump sawtooth, STM thermal
  spikes) so the whole pipeline could be built and tested before touching
  hardware.

---

## 8. Frontend (React + TypeScript)

A single-page dashboard built to static files and served by Caddy. Public,
read-only charts: log-scale vacuum pressures and linear cryogenic temperatures,
each multi-series, with live refresh, time-range presets (15 m … 90 d),
latest-value stat chips, and per-channel + whole-range CSV download. A JWT-gated
admin panel edits the sampling interval and alert thresholds. The client uses
relative API paths only, so the same build works behind the Vite dev proxy and
Caddy in production. Strict TypeScript; channels are discovered at runtime.

---

## 9. Reliability engineering

Scope was deliberately time-boxed to **reliable delivery + multiple producers**,
not consensus/sharding. Four mechanisms:

1. Collector **retry + exponential backoff** on transient failures.
2. Collector **offline disk buffer** (write-ahead, ordered, crash-safe).
3. **Idempotent ingest** (unique key + `ON CONFLICT DO NOTHING`).
4. **Concurrent-producer** correctness (per-source tokens; DB handles concurrent
   inserts; stateless handlers).

**Failure injection against the deployed box** (results in `BENCHMARKS.md`):

| Injected failure | Outcome |
|---|---|
| go-api container restart mid-ingest | 1500/1500 delivered, ordered, **0 loss** |
| 22 s network black-hole | buffered to disk, drained in order, **0 loss, 0 dupes** |
| TimescaleDB restart | **9.3 M rows persisted** (named volume), service reconnected |
| Full EC2 reboot | whole stack **self-healed in seconds** via `restart: unless-stopped` |

Even at **~9× the measured ingest ceiling**, the system returned 0 errors and
lost 0 of 1.6 M readings — it degraded gracefully (latency rose), never dropped.

---

## 10. Performance & load testing

A custom Go harness drove the **deployed** instance over the public internet
(open-loop, coordinated-omission-corrected latency). Key findings:

- **Batching is the dominant lever:** sustainable ingest ~1,000 → ~10,000
  readings/s, ceiling ~1,060 → **~22,300 (≈21×)** — per-request/transaction
  overhead, not row volume, was the wall.
- **Bottleneck is TimescaleDB CPU on 2 vCPUs**, measured (TimescaleDB ~144% CPU
  vs the Go service ~27% / 16 MiB RSS) — not the Go service, memory, or network.
  Postgres was already auto-tuned by the image; no manual tuning needed.
- **Continuous aggregate** cut a 30-day query from **~3.2 s to ~0.12 s (~28×)**,
  scanning 120× fewer rows.
- **Concurrency:** ~25 producers / ~40 read clients comfortably; `landed == sent`
  under concurrency (no loss, no duplicates).

A documented **scaling roadmap** (batching ✓ → continuous aggregates ✓ → bigger
instance → managed TimescaleDB → queue/broker → horizontal) was written and
**deliberately not built** — at fewer than ten instruments the single box has
100–1000× headroom.

---

## 11. Real-lab integration (without disturbing the lab)

The lab's acquisition is split across an existing temperature logger and a
graduate student's QCoDeS GUI suite (which owns the vacuum gauge on an exclusive
serial port and already publishes to InfluxDB Cloud). A **collision analysis**
drove the integration:

- **Temperatures** are reached through a TCP bridge built for multiple clients, so
  a second client is safe → a **high-rate (1 Hz) producer** was added that reads
  the bridge and posts only to AWS, while the original logger runs unchanged.
- **The vacuum gauge** is exclusive serial owned by the suite → not touchable. So
  an **InfluxDB → AWS forwarder** (read-only) mirrors what the suite already
  publishes (temps + pressures) into AWS, replicating the old dashboard with
  **zero changes to the lab software**. Forwarding is idempotent (each point keeps
  its source timestamp).
- A one-time, idempotent **backfill** imported the full retained InfluxDB window
  — **257,597 points across 30 days** — so AWS preserves history the free tier
  discards.

---

## 12. Alerting

On each ingested reading the server evaluates per-metric thresholds; a cross is
recorded in `alert_log` and dispatched to email. Production rule: alert when any
vacuum gauge exceeds **1e-8 Torr**. Volume is bounded by a **per-metric debounce**
and a **hard daily email cap** (configurable) — protecting both the inbox and the
mail provider's free tier; crosses past the cap are still recorded. Email is via
authenticated SMTP (credentials in the host environment only); the path was
verified end-to-end with a live email on a real threshold cross.

---

## 13. Deployment & operations

- **Multi-stage, distroless** Go image; ARM-native build for the Graviton box.
- **Docker Compose** runs Caddy + go-api + TimescaleDB (+ optional forwarder /
  demo producer), with named volumes and `restart: unless-stopped`.
- **Caddy** obtains and renews a real Let's Encrypt certificate automatically for
  the `sslip.io` host and routes `/api`,`/ingest`,`/healthz`,`/metrics` to the Go
  service, else the static SPA.
- **Secrets** (DB password, ingest tokens, JWT secret, SMTP, InfluxDB token) live
  only in a host `.env` (chmod 600), never in git; the repo ships an
  `.env.example` shape.
- Deploy is `rsync` + `docker compose up -d --build`; migrations apply at boot.

---

## 14. Testing

- **Go unit** (hermetic): auth (token + JWT, expiry/tamper/alg), alert evaluation
  + debounce + daily-cap (injected clock), ingest validation, series step/range
  helpers, all three auth planes (httptest), metrics tracker.
- **Go integration** (gated on a live TimescaleDB): ingest idempotency,
  `time_bucket` + both continuous-aggregate rollups, config/threshold round-trips.
- **Python** (`pytest`): durable buffer ordering/atomicity/restart, reader output,
  retry semantics.
- **Chaos/load**: concurrent + high-frequency producers through injected
  latency/loss/disconnect, asserting no loss, correct ordering, idempotency.

---

## 15. Security posture

- Three isolated auth planes; source-bound, revocable ingest tokens; constant-time
  credential checks; algorithm-pinned JWT verification.
- 12-factor secrets (environment only); nothing sensitive in source or git;
  `.gitignore` excludes all credential files and build artifacts.
- TLS everywhere in production (Caddy/Let's Encrypt). SSH restricted by key + IP.
- Honest telemetry endpoints expose no secrets.

---

## 16. Outcomes — résumé-ready claims

- Designed and shipped a **full-stack** monitoring platform (**React/TS + Go +
  TimescaleDB/Postgres**), **deployed on AWS (EC2 + Docker + Caddy/HTTPS)**, now
  ingesting **real instrument data for a research group**.
- Built a **Go** ingest service with **idempotent batch writes** and a
  **three-tier auth** model (per-source tokens / public reads / JWT control).
- Engineered **reliable delivery** (retry + write-ahead offline buffering +
  idempotency); verified **zero data loss** across container/DB restarts, a full
  host reboot, and injected network outages.
- **Load-tested the deployed system**, identified the bottleneck, and implemented
  **batching (~21×)** and **TimescaleDB continuous aggregates (~28× on long-range
  reads)**; documented a scaling roadmap without over-engineering.
- Integrated with live lab instrumentation **without modifying the lab's
  software** (read-only InfluxDB→AWS mirror + a high-rate bridge client) and
  **backfilled 30 days** of history; added **threshold email alerting** with
  debounce + a daily cap.
- Comprehensive **tests** (unit, DB integration, chaos/load) and CI-friendly,
  reproducible benchmarks.

---

## 17. Honest scope & limitations

- A planned ~50 Hz fast STM-current channel is **designed for and load-tested**,
  but **not yet live** (separate, not-yet-connected instrument). Never claimed as
  live.
- Single-box deployment by design; horizontal scaling is roadmap, not built.
- Alerting uses authenticated SMTP; an account-scoped credential is a known
  trade-off, mitigated by host isolation and easy revocation/rotation.
