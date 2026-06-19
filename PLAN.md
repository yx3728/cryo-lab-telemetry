# PLAN — Lab Instrument Monitoring Platform

This is the design document for the project. The [`README.md`](./README.md) is the
recruiter-facing summary; this file is the engineering rationale a reviewer (or
the owner, in an interview) can read to understand *why* each piece is the way it
is. The append-only build record is in [`WORKLOG.md`](./WORKLOG.md).

## 1. Problem

A physics lab runs a Unisoku STM with a cryostat and vacuum system. Its
instrument PC (Windows) already samples the instruments via **PyVISA** every few
seconds and uploads to a **Grafana free-tier** cloud dashboard. That free tier
has real limits: **30-day retention**, no easy bulk export, and no way to change
the instrument's sampling behaviour remotely. We want a self-hosted replacement
the lab owns end-to-end: unlimited history, CSV export, alerting, and a remote
"set the sampling interval / thresholds" control loop.

## 2. The channels (what the data actually is)

Confirmed against the lab's public Grafana dashboard ("Unisoku STM"):

**Vacuum pressure** — Torr, **log scale**, baseline ~1e-9, occasional spikes to
~1e-7:
- `LL` (load lock) — frequently OFF / no signal
- `PC` (prep chamber) — ~1.3e-9
- `OC` (outer chamber) — ~2.3e-9
- `PREP` — ~1e-9

**Temperature** — Kelvin, ~2–28 K:
- `SORB` — ~8 K, slow **sawtooth** (cryostat sorb-pump regen cycles ramp to
  ~16–20 K then drop)
- `1K Pot` — ~4.4 K, sawtooth
- `He3 Pot` — ~4.2 K
- `STM` — ~4.2 K baseline, occasional **spikes to ~25 K** during events

Cadence: every few seconds. The mock reproduces these channels, units, ranges,
and shapes (sawtooth / flat / spike) so the dashboard looks like the real thing
before the real thing is connected.

## 3. Architecture & component boundaries

```
Lab PC (Python/PyVISA)  ──HTTPS POST /ingest──▶  Go ingest+API  ──▶  TimescaleDB
        ▲                                              │
        └────────── GET /api/config (poll) ◀───────────┤  (Caddy auto-HTTPS in front)
                                                        ├──▶  Read API  ──▶  React dashboard (public)
                                                        ├──▶  Config API ──▶  Admin UI (JWT)
                                                        └──▶  Alerter  ──▶  email / Slack
```

**Why the edge stays Python.** The instruments talk GPIB; reaching them needs
NI-VISA / PyVISA, which the lab already has working. Rewriting hardware I/O in Go
would mean re-solving a solved problem with worse tooling. So Python owns
*acquisition*, Go owns *the cloud*.

**Why Go is the cloud service.** Ingest is the part that benefits from real
concurrency — multiple producers posting at once, a goroutine per request, an
idempotent write path, cheap fan-out to the alerter. It is also the résumé Go
credential, written to be idiomatic and explainable line by line.

**Why TimescaleDB.** It is Postgres, so the read API is plain SQL and CSV export
is trivial; `time_bucket()` gives cheap server-side downsampling for charts; the
`readings` hypertable handles unlimited retention. This directly buys back the
three things the Grafana free tier denied us.

## 4. Data model (`server/migrations`)

```sql
readings(source TEXT, metric TEXT, ts TIMESTAMPTZ, value DOUBLE PRECISION)
        -- hypertable on ts; UNIQUE (source, metric, ts)
config(key TEXT PRIMARY KEY, value TEXT, updated_at TIMESTAMPTZ)
alert_threshold(metric TEXT PRIMARY KEY, min_value, max_value, enabled)
alert_log(id, source, metric, value, kind, threshold_value, fired_at, notified)
```

- The `UNIQUE (source, metric, ts)` constraint is what makes ingest **idempotent**:
  retried batches `ON CONFLICT DO NOTHING`. (The unique key includes the
  partition column `ts`, as Timescale requires.)
- `config` is a tiny key/value table — `sampling_interval_seconds` lives here.
- Thresholds drive both alerting and what the admin UI edits.

Migrations are embedded in the Go binary (`embed.FS`) and applied in order at
boot, tracked in a `schema_migrations` table — no external migration tool, works
identically on the Mac and the ARM box.

## 5. API surface (`server`)

| Method & path            | Auth         | Purpose |
|--------------------------|--------------|---------|
| `POST /ingest`           | API token    | Batch insert, idempotent (`ON CONFLICT DO NOTHING`), fires alerts |
| `GET /api/series`        | public       | Downsampled series via `time_bucket(step)` |
| `GET /api/export.csv`    | public       | Same query, streamed as CSV |
| `GET /api/config`        | public read  | Sampling interval + thresholds (the collector polls this) |
| `PUT /api/config`        | JWT (admin)  | Update sampling interval + thresholds |
| `POST /api/login`        | password     | Issue JWT |
| `GET /healthz`           | public       | Liveness + DB ping |
| `GET /metrics`           | public       | Honest telemetry: uptime, rows ingested, per-source last-seen |

All handlers are stateless; the database is the only state. Config comes from
environment variables exclusively.

## 6. Reliability (deliberately time-boxed)

The honest engineering target is **reliable delivery + multiple producers**, not
web-scale QPS. Exactly four mechanisms, no more:

1. **Collector retry + exponential backoff** on transient POST failures.
2. **Collector offline disk buffer** — readings are journaled to disk before
   send; on a network drop they accumulate and are flushed **in order** on
   reconnect. No data is lost across outages.
3. **Idempotent ingest** — the unique constraint + `ON CONFLICT DO NOTHING`
   means a replayed buffer (or a retried batch) never double-writes.
4. **Concurrent producers** — per-source tokens; the DB handles concurrent
   inserts. Nothing in the handler holds cross-request state.

**Not built (on purpose):** consensus, exactly-once-across-partitions, sharding,
a custom broker/queue, Kubernetes. Those would be a different (and unjustified)
project.

## 7. The configuration control loop

1. Admin logs in (JWT) and sets `sampling_interval_seconds` / thresholds via the
   UI → `PUT /api/config`.
2. The collector polls `GET /api/config` on an interval and adopts the new
   sampling interval and thresholds.
3. Thresholds also feed the server-side alerter, so "change the threshold" both
   re-arms alerts and changes what the instrument PC does.

## 8. Alerting

On each ingested reading the server checks the metric's threshold; a cross is
recorded in `alert_log` and, **debounced** per metric, dispatched to email
(SMTP) and/or Slack (webhook). If neither notifier is configured the cross is
still logged to `alert_log` and stdout — alerting is observable without secrets.

## 9. Testing strategy

- **Unit (Go):** ingest validation, dedupe/idempotency, the three auth planes,
  config apply, alert threshold + debounce logic.
- **Integration:** collector → ingest → DB → read API round-trip against the mock
  feed, asserting what went in comes back out.
- **One load/chaos test:** mock producers with injected latency, loss and
  disconnects, asserting (a) no data loss, (b) correct ordering, (c) stability
  under a simulated high-frequency producer + several concurrent producers — the
  evidence for the planned fast-channel headroom. Numbers land in `WORKLOG.md`.

## 10. Deployment

A single `deploy/docker-compose.yml` on the provisioned `t4g.small` (ARM) EC2
box brings up three services: `caddy` (reverse proxy + automatic HTTPS for
`3.220.132.187.sslip.io`), `go-api`, and `timescaledb` (named volume). Images
build natively for ARM on the Apple-Silicon dev machine. Secrets are set as env
on the box and never committed.

## 11. Build order (mock-first)

1. Docs + skeleton + `git init`.
2. Schema + Go ingest + TimescaleDB; verify ingest→DB.
3. Mock producer reproducing the real channels.
4. Read API + React dashboard; verify end-to-end on the Mac.
5. Three-tier auth + config loop + alerting.
6. Python collector (the real artifact) with all reliability features.
7. Tests (unit + integration + one load/chaos).
8. Deploy to EC2; verify public HTTPS, login, telemetry.
9. **Last:** `WIRING.md` — the one-file change that points the real lab at this
   system.

Everything through step 8 runs on the Mac against mocks; the real lab is touched
only at step 9.
