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

## 2026-06-18 — Step 4: read API + React dashboard

- Added public `GET /api/channels` (latest value per source/metric) for UI
  discovery. Server restarted; endpoint returns the 7 active channels.
- **web/** Vite + React 18 + TypeScript (strict) + Recharts. Relative API paths
  only (Vite proxy in dev, Caddy in prod). Public charts: log-scale Pressure
  (LL/PC/OC/PREP) + linear Temperature (SORB/1K Pot/He3 Pot/STM), live 5 s
  refresh, time-range presets (15m…7d), per-channel CSV links, latest-value stat
  chips. JWT-gated admin panel for sampling interval + thresholds.
- **Verified:** `npm run build` clean (tsc strict + vite). Vite dev server
  serves the app and proxies live `/api/channels`. Captured a **headless-Chrome
  screenshot** of the running dashboard (saved to `docs/dashboard.png`): both
  charts render real mock data, the STM spike to ~21 K is visible decaying to
  4.2 K, log pressure axis correct, LL correctly absent (off), admin login form
  shown. Control plane (login → JWT → PUT config) already verified via curl in
  step 2.

## 2026-06-18 — Step 5: three-tier auth + config control loop + alerting

(Server + UI built in steps 2/4; this step is the end-to-end verification.)

- **Three-tier auth** verified: `X-Api-Key` ingest (source-bound, 401 on bad
  key, 400 on source mismatch); public reads; admin login → JWT → `PUT /api/config`
  (401 without token, 200 with).
- **Closed config control loop** verified *live*: admin `PUT /api/config`
  changed the sampling interval 2 s → 4 s; the running collector's `config_loop`
  polled, logged `control loop: sampling interval 2s -> 4s`, and adopted it
  (still `dropped=0`). This is the headline "remote control of the instrument PC"
  feature working end-to-end.
- **Alerting + debounce** verified: admin added a `TESTCH max=100` threshold
  (PUT triggers an immediate alerter cache reload); two crossing ingests (150,
  160) within the 60 s window produced **exactly one** `alert_log` row (second
  debounced), recorded `value=150 kind=max threshold=100 notified=false`
  (log-only mode — honest, no SMTP/Slack secrets configured locally). Precise
  debounce-window-expiry / re-arm behaviour is covered deterministically by the
  step-7 unit tests with an injected clock.

## 2026-06-18 — Step 7: tests (unit + integration + load/chaos)

- **Go unit tests** (no DB, always run): auth (token resolution, JWT round-trip,
  expired/tampered/wrong-secret rejection, credential check); alert (`Evaluate`
  cases + debounce window with injected clock); ingest `validateReading`; series
  `resolveStep`/`parseTimeRange`; all three auth planes via httptest middleware;
  metrics tracker. `go test ./...` → all packages **ok**.
- **Go integration tests** (gated on `TEST_DATABASE_URL`): against live
  TimescaleDB — ingest **idempotency** (replay writes 0 rows; partial overlap
  writes only the new row), `time_bucket` series averaging, config + threshold
  upsert/read. All **PASS**.
- **Python collector tests** (pytest, 11 passed): DiskBuffer order/atomicity/
  restart-counter/remove; MockReader channels + RFC3339 timestamp; `post_batch`
  retry semantics (200→OK, 4xx→PERMANENT-no-retry, 5xx/conn/429→retry, exhaust→
  TRANSIENT).
- **Load/chaos test** (`mock/`, the one big test) — 5 concurrent producers (4
  normal + 1 high-frequency, 2000 readings; the planned-fast-channel stand-in),
  **3600 readings** through a fault-injecting proxy: 15 ms latency, 15% drop,
  10% ambiguous (forward-then-hide-success), and a 3 s hard outage. Producers use
  the collector's real DiskBuffer + post_batch. Proxy saw 131 requests for 90
  batches (41 retries); **peak on-disk buffer depth 76 batches** during the
  outage. Verified straight from the DB:
  **loss = 0, duplicates = 0, ordering correct for every producer**
  (the 7 ambiguous double-writes absorbed by idempotent ingest), ~**665
  readings/s sustained through the chaos**. `RESULT: PASS`. This is the evidence
  for "reliable delivery + headroom for a planned high-frequency channel" — not a
  web-scale-QPS claim.

## 2026-06-18/19 — Step 8: deploy to AWS EC2

- **Box:** t4g.small (ARM/aarch64), Ubuntu, Docker 29.6 + Compose 5.1.4, 1.8 GB
  RAM, 26 GB free. SSH OK.
- **Artifacts:** `deploy/docker-compose.yml` (caddy + go-api + timescaledb,
  named volumes; optional `mockfeed` collector profile), `deploy/Caddyfile`
  (auto-HTTPS + `/api`,`/ingest`,`/healthz`,`/metrics` → go-api, else static
  SPA), `collector/Dockerfile`, `deploy/deploy.sh`, `.dockerignore`s.
- **Process:** built `web/dist` on the Mac, rsynced repo to
  `ubuntu@…:/home/ubuntu/lab-monitor` (no secrets/node_modules/venv). Generated
  secrets **on the box** into `deploy/.env` (chmod 600, never committed/printed):
  POSTGRES_PASSWORD, INGEST_TOKEN, ADMIN_PASSWORD, JWT_SECRET (openssl rand).
  `docker compose --profile mockfeed up -d --build` — images built natively for
  ARM on the box.
- **Verified live at `https://3.220.132.187.sslip.io`:**
  - Caddy obtained a **Let's Encrypt cert** (tls-alpn-01); `openssl s_client`
    confirms issuer = Let's Encrypt, CN = 3.220.132.187.sslip.io, valid
    2026-06-19 → 2026-09-17. `/healthz` 200 over valid TLS (no `-k`).
  - `/api/channels` returns the 7 live channels; `/metrics` shows honest uptime
    + rows ingested (mockfeed producing); dashboard `index.html` served.
  - **Three planes over public HTTPS:** ingest bad key → 401 / good key → 200;
    public reads → 200; `PUT /api/config` no JWT → 401, with admin JWT → 200
    (interval updated); wrong admin password → 401.
  - Mock producer left running (compose `restart: unless-stopped`) so the
    dashboard stays live and `/metrics` uptime accrues honestly. The real lab PC
    replaces this feed per WIRING.md.
  - Captured a headless-Chrome screenshot of the **live public site** (saved to
    `docs/dashboard.png`): all 8 channels render from the cloud API, with a PC
    vacuum spike and the SORB sawtooth visible.

## 2026-06-19 — Step 9: WIRING.md (real-lab cutover)

- Wrote `WIRING.md`: the one-file change on the lab Windows PC. Two paths:
  (A) a drop-in `post_readings()` that POSTs the existing PyVISA readings to
  `/ingest` with the `X-Api-Key` (Python + requests + token only — no Go/Docker/
  Node); (B) reuse the repo collector with a `RealReader` wrapping the existing
  GPIB queries to also get retry + offline buffering + remote sampling-interval
  control. Exact metric names documented so data lands on the right charts.
  Includes a parallel-run/rollback path (Grafana upload left untouched).
- **The real lab is intentionally NOT touched** in this build — steps 2–8 were
  all mock-tested first. Wiring is left as the documented, reversible final
  action for the lab owner.

## Milestone: MVP complete (mock-tested) — superseded by production cutover below

All 9 steps done. System deployed and verified on AWS; mock-tested end-to-end
including chaos/load; ready for the real-lab cutover (WIRING.md). The real-lab
wiring, backfill, and production status follow in the later entries.

## 2026-06-19 — Load + reliability hardening pass (see BENCHMARKS.md)

One disciplined pass against the **deployed** `t4g.small`, from the Mac over the
real network. Full numbers in `BENCHMARKS.md`; built `bench/` (Go harness),
migration `0002` (continuous aggregate), pgx-pool config, bounded buffer.

**What actually surprised me (the point of this section):**
- **The bottleneck is TimescaleDB CPU, not Go.** Under saturation: timescaledb
  ~144 % CPU vs go-api ~27 % / **16 MiB RSS**, 543 MiB free (no OOM). I'd have
  guessed the Go ingest or the WAN; the real wall is Postgres insert/index on
  2 vCPUs. Go has enormous headroom.
- **Postgres was already auto-tuned** by the Timescale image (`timescaledb-tune`):
  `shared_buffers ≈ 458 MB`, `max_connections = 25`. My planned "tune
  shared_buffers for 2 GB" fix was unnecessary — overriding would have hurt.
  Deleted that from the plan.
- **The long-range raw query really does blow up:** ~3,200 ms to scan 5.18 M raw
  rows for a 30-day/1 h aggregate; the 1-minute continuous aggregate (43 k rows,
  120× fewer) does it in ~115 ms (**~28×**). Concrete justification, not a guess.
- **Graceful degradation beat my expectation:** at a 200,000 rd/s target (~9× the
  measured ceiling) the box returned **0 errors and lost 0 of 1.6 M readings** —
  it only slowed (p99 → 63 s). Retry + offline buffer + idempotency hold under
  ~9× overload; nothing drops.
- **Idempotency fooled my first landing check:** reusing timestamps across bench
  runs got deduped (data landed once), so `db_rows_delta` under-counted. A nice
  live proof of idempotency — and a reminder to vary keys when measuring loss.
- **A client-side gotcha:** the harness's HTTP/2 client stalled on concurrent
  large gzipped GETs against Caddy (h2 flow control / head-of-line). Forcing
  HTTP/1.1 (a connection per in-flight request) fixed it — and models independent
  viewers better anyway.

**Headline numbers:** unbatched ingest ceiling ~1,060 rd/s; **batched ~22,300
rd/s (~21×)**; sustainable ~10,000 rd/s at p99 ~67 ms. Long-range read ~28×
faster via continuous aggregate. No data loss across go-api restart, a 22 s
network black hole, a DB restart (9.3 M rows persisted), and a full EC2 reboot
(stack self-healed in seconds). Concurrent: ~25 producers / ~40 viewers
comfortably — far above real lab usage. Test data removed; box restored to the
clean live demo.

**Scaling roadmap (documented, deliberately NOT built):** batching ✓ → continuous
aggregates ✓ → bigger instance → managed TimescaleDB → queue/broker → horizontal.
Unnecessary at < 10 instruments (100–1000× headroom). See BENCHMARKS.md.

## 2026-06-19 — Wired to the real lab: NOW DEPLOYED, INGESTING REAL DATA

This is the cutover from mock to production. The system now ingests **real
instrument data** from the lab's Unisoku STM cryostat. See
`docs/HARDWARE_INTERFACE.md`.

- **Hardware interface understood (Lake Shore Model 350):** RS-232 over COM6 at
  **57600 8-O-1** (unusual 7-bit/odd framing), SCPI `KRDG?` reads; a serial↔TCP
  bridge (port 5001, shared-secret) multiplexes clients; channels A/B/C/D →
  SORB / 1K Pot / He3 Pot / STM.
- **Constraint:** don't modify the lab's acquisition software (an original
  InfluxDB temperature logger + a PhD student's QCoDeS GUI suite that owns the
  vacuum gauge on an exclusive serial port and publishes to InfluxDB Cloud).
- **Collision analysis drove the design:** temperatures go through the multi-client
  TCP bridge (safe to add a client); the gauge is exclusive serial (cannot
  double-open) but the suite already publishes to InfluxDB (many readers allowed).
- **Final architecture (lab unchanged):**
  - `forwarder/` — read-only **InfluxDB → AWS mirror** on the EC2 box, replicating
    both temps + pressures (the Grafana view) into AWS. Idempotent (keeps each
    point's InfluxDB timestamp); skips off-gauge zeros.
  - `lab/ls350_fast_aws.py` — a **second bridge client** adding **1 Hz** temps to
    AWS, polling `GET /api/config` so the dashboard controls its rate.
- **Verified live:** stopped the demo mock feed; confirmed only the real
  temperature channels keep refreshing while pressures came via the forwarder.
  Later confirmed **1 Hz** producer output (1.00 pt/s) and that an admin change to
  **5 s** via the dashboard was adopted by the producer (closed control loop).
- **Surprise:** EC2 had no IAM role / no AWS CLI, so SES couldn't be provisioned
  from the box — initially planned SES, ended on Gmail SMTP at the lab's request.

## 2026-06-19 — 30-day history backfill (nothing lost)

- InfluxDB Cloud free tier discards data after 30 days. Wrote a one-time,
  idempotent backfill (`forwarder/backfill.py`) that imported the full retained
  window day-by-day: **read 257,845 points, inserted 257,597** (248 already
  present, ~42k off-gauge zeros skipped), spanning **2026-05-20 → now**.
- Refreshed the 1-minute continuous aggregate over the range; a 30-day query
  returns the full month fast (720 points, ~0.12 s). Going forward AWS retains
  what InfluxDB drops.

## 2026-06-19 — Purged all mock data (production is 100% real)

- Cut over from mock to real cleanly: the demo mock fed channel `PC` (the gauge
  has no PC), so the last `PC` point marked the mock boundary. Deleted **45,933
  mock-era rows** (kept the real rows after the cutover), cleared mock
  `alert_log`, refreshed the rollup, VACUUMed. Dashboard now shows only real data.

## 2026-06-19 — Dashboard usability + alerting (live, on real data)

- **Admin login** (credentials in box `.env`) → change sampling interval / alert
  thresholds; verified live (set 5 s).
- **All-channels CSV download** button (public; data is already public, EC2 is
  fixed-cost — confirmed no per-download cost concern).
- **30 d / 90 d range presets** + coarser server step tiers so the full history is
  viewable in the browser.
- **1-second continuous aggregate** (`readings_1s`) + routing (≥60 s → 1 m,
  1–59 s → 1 s, else raw); enabled **real-time aggregation** on both rollups so
  live views include the newest points (caught and fixed: recent TimescaleDB
  defaults aggregates to materialized-only). Future-proofs high-rate data.
- **Email alerting LIVE:** vacuum gauges (LL/OC/PREP/PC) alert when **p > 1e-8
  Torr**, via Gmail SMTP, with per-metric debounce **+ a daily email cap**
  (`ALERT_MAX_EMAILS_PER_DAY`). Verified end-to-end — a real threshold cross sent
  an email that was received. Credential lives only in the box `.env` (chmod 600),
  never in git.

## 2026-06-19 — Published as portfolio

- Wrote a production `README.md` (live link + real-data screenshot, rendered &
  visually checked) and a full `REPORT.md` (technical record for résumé writing).
- Pre-publish hygiene: scrubbed all credentials/PII from files **and** commit
  history; every commit re-authored as `yx3728`, `Co-Authored-By` trailers
  stripped.
- Pushed public: **github.com/yx3728/cryo-lab-telemetry**, with **GitHub Actions
  CI** (Go unit + DB-integration against a TimescaleDB service, Python pytest, web
  build) — **green on first run**.

## Status: IN PRODUCTION — deployed on AWS, ingesting real instrument data

Live dashboard: `https://3.220.132.187.sslip.io`. Real cryostat temperatures
(1 Hz) and vacuum pressures flowing from the lab; 30 days of history preserved and
growing; threshold email alerting active; remote sampling control working;
public, tested (CI green), and documented.
