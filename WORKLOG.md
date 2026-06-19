# WORKLOG

Append-only, timestamped record of the build: findings, components completed,
test numbers, deploy verification, and scope decisions.

---

## 2026-06-18 — Step 1: skeleton, docs, git init

- **Toolchain (dev Mac, Apple Silicon / arm64):** Go 1.26.4, Node 24.11, Python
  3.12, Docker 29.5 via colima, Docker Compose 5.1.4. ARM dev box matches the
  `t4g.small` ARM deploy target, so images build natively.
- **Grafana shape check (optional, succeeded):** fetched the lab's public
  dashboard API
  (`/api/public/dashboards/9a1b0a82458c4240a22e60e541a20587`). Confirmed:
  - Stat panels `LL (Torr)`, `PC (Torr)`, `OC (Torr)`; time-series panels
    `Temperature` and `Pressure (Torr)`.
  - Vacuum channels **LL, PC, OC, PREP** (Torr, **log scale**).
  - Temperature channels **SORB, 1K Pot, He3 Pot, STM** (Kelvin, linear).
  This matches the build spec exactly; the mock uses the documented numeric
  shapes. **The runtime does not depend on Grafana.**
- **Scope decision:** MVP = existing few-second multi-channel vacuum + temp data,
  end-to-end with alerting + remote config. The ~2 ms / 50 Hz fast STM-current
  channel is on a separate, not-connected computer → **out of scope, but the
  ingest path and load test are built to accept it as a future per-source
  producer**. Never claimed live.
- **Reliability decision:** build exactly retry+backoff, offline disk buffer,
  idempotent ingest, concurrent-producer correctness. No consensus / broker /
  sharding / k8s.
- Repo: `git init` on branch `main`, local identity set to `yx3728`. Wrote
  `README.md`, `PLAN.md`, `.gitignore` (excludes secrets/`node_modules`/builds),
  `.env.example` (all config via env, no secrets in code). Directory skeleton:
  `server/ collector/ mock/ web/ deploy/`.

## 2026-06-18 — Step 2: Go ingest + API + TimescaleDB

- **Stack:** Go 1.23 module `github.com/yx3728/lab-monitor/server`; chi v5 router,
  pgx v5 (no ORM), golang-jwt v5. Migrations embedded via `embed.FS`, applied at
  boot inside per-file transactions, tracked in `schema_migrations`.
- **Schema:** `readings` hypertable, `UNIQUE (source, metric, ts)`; `config`,
  `alert_threshold` (seeded STM max 20 K, OC max 1e-7 Torr), `alert_log`.
- **Built the full service** (server side of steps 2/4/5): idempotent ingest,
  `time_bucket` series + CSV export, config get/put, login, threshold alerting
  (email/Slack notifiers, debounced) + alert_log, `/healthz`, honest `/metrics`.
- **Verified on the Mac** against TimescaleDB 2.17.2-pg17 (ARM image, pulled
  natively):
  - ingest of a 4-row batch → `{received:4, inserted:4}`; replay → `inserted:0`
    (idempotent).
  - `readings` confirmed a hypertable; rows present in DB.
  - STM=25.5 K crossed the seeded max=20 → `alert_log` row + WARN log line,
    `notified=false` (log-only mode, honest — no SMTP/Slack configured).
  - all three auth planes: bad/missing `X-Api-Key` → 401; source-mismatch → 400;
    public `GET /api/series|config|export.csv` → 200; `PUT /api/config` without
    JWT → 401, with admin JWT → 200 (interval updated 5→10).
- `go build ./...` and `go vet ./...` clean; `gofmt -l` empty.

## 2026-06-18 — Steps 3 + 6: mock signal model + Python collector

- **signals.py** (stdlib only): `Simulator` reproducing all channels. Seeded
  30-min check: LL mostly OFF (17/360 on, ~1e-9), PC/OC/PREP ~1e-9 with rare
  spikes to ~1e-7, SORB sawtooth 7.9→18 K, 1K Pot 4.36→5.01, He3 Pot ~4.2,
  STM 4.2 baseline spiking to 25.2 K. Matches the real dashboard shapes.
- **collector** (the deployable artifact): pluggable `MockReader` (default) /
  `RealReader` (PyVISA template, lazy-imported, reuses the lab's queries).
  Acquisition decoupled from delivery via a **durable on-disk FIFO**
  (`buffer.py`, write-ahead, atomic temp+rename, oldest-first drain). Sender does
  retry + exponential backoff; transient failures stop the drain (preserving
  order) and back off; 4xx poison batches are dropped so they can't block the
  queue. **config_loop** polls `GET /api/config` and adopts the admin sampling
  interval (closed control loop).
- **Verified** against the live local server: collector adopted server interval
  (2 s) at startup; fed all 7 active channels; over the run **read=112,
  sent=112, dropped=0, queued=0**; SIGINT → graceful shutdown with the buffer
  fully drained to 0. Reliability features (buffer/retry) get hammered in the
  step-7 chaos test.
