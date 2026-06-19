# BENCHMARKS — load + reliability hardening pass

A single, deliberate load/reliability pass on the **deployed** system before
wiring it to the real lab: find the single-box limits, fix the obvious
bottlenecks, validate headroom for the planned high-frequency channel, and write
down a scaling roadmap — **without** prematurely building distributed infra.

> **Honest framing.** This is *reliable multi-channel telemetry ingestion for a
> research lab, load-tested for a planned high-frequency channel* — not a
> high-throughput / web-scale service. Every number below was measured **from a
> laptop over the public internet against the real `t4g.small` (2 vCPU / 2 GB)**
> at `https://3.220.132.187.sslip.io` — never localhost.

## Method

- Harness: [`bench/`](./bench) (Go, stdlib only). Open-loop scheduler assigns each
  request an intended send time; latency is measured from that intended time, so
  when the box can't keep up the queueing delay shows in the tail (coordinated-
  omission correction) instead of being hidden.
- Each benchmark uses unique, monotonic timestamps, so "rows landed in DB" is a
  true count and idempotent dedup never silently masks loss.
- Box CPU/mem sampled with `ssh … docker stats`. Reproduce: [`bench/run_all.sh`](./bench/run_all.sh).
- Test data was isolated under throwaway sources/tokens and **fully removed
  afterwards**; the box is back to the clean live demo (only `unisoku-stm`).

---

## T1.1 — Ingest throughput: batching is everything

Single producer, ramped until it broke. `landed == sent` (0 errors) at **every**
rate, including far past saturation.

**Unbatched** (1 reading/request):

| target rd/s | achieved | p50 | p99 | landed |
|---|---|---|---|---|
| 200  | 199  | 29 ms | 39 ms | 1600/1600 |
| 500  | 498  | 31 ms | 64 ms | 4000/4000 |
| 1000 | 982  | 56 ms | 213 ms | 8000/8000 |
| 1500 | **1056** | 1753 ms | 3337 ms | 12000/12000 |
| 3000 | 1064 | 7369 ms | 14448 ms | 24000/24000 |

**Batched** (100 readings/request, multi-row `INSERT`):

| target rd/s | achieved | p50 | p99 | landed |
|---|---|---|---|---|
| 5000   | 4986  | 36 ms | 53 ms | 40000/40000 |
| 10000  | 9959  | 39 ms | 67 ms | 80000/80000 |
| 25000  | **21996** | 740 ms | 1133 ms | 200000/200000 |
| 50000  | 22259 | 5011 ms | 9928 ms | 400000/400000 |
| 200000 | 22348 | — | 63045 ms | 1600000/1600000 |

**Findings**
- **Unbatched ceiling ≈ 1,060 rd/s** (sustainable ≈ 1,000 at p99 ~200 ms).
- **Batched ceiling ≈ 22,300 rd/s** (sustainable ≈ 10,000 at p99 ~67 ms).
- **Batching delta ≈ 21× ceiling / 10× sustainable.** Per-request + per-transaction
  overhead, not row volume, was the wall — exactly what batching removes.
- **Graceful degradation:** at 200,000 rd/s (≈9× the ceiling) the box stayed up,
  returned **0 errors**, and **lost 0 of 1,600,000 readings** — it just slowed
  down (p99 → 63 s). The retry + offline-buffer + idempotency design holds under
  extreme overload; nothing is dropped.

### The bottleneck (measured, not guessed)

`docker stats` during a saturating batched run:

| container | CPU | mem |
|---|---|---|
| **timescaledb** | **~144 %** (of 200 %) | 556 MiB |
| go-api | ~27 % | **16 MiB** |
| caddy | ~22 % | 50 MiB |

Box: load avg ~7, **543 MiB free (no OOM)**. → **The single-box limit is
TimescaleDB insert/index CPU on 2 vCPUs.** Go is nearly idle (16 MiB RSS, 27 %
CPU) with huge headroom; memory and the network are not the constraint. This was
the pass's biggest surprise — the natural guess (the Go service or the WAN) was
wrong.

### Fixes applied
- **Client batching** is the dominant lever (already in the collector; the 21×
  above quantifies it).
- **Multi-row idempotent `INSERT … ON CONFLICT DO NOTHING`** server-side (kept
  over `COPY`, which can't express the idempotency the reliability story needs).
- **pgx pool lifted 4 → 10** (`DB_MAX_CONNS`), under Postgres' tuned
  `max_connections = 25`.
- **Postgres needed no manual tuning** — the Timescale image's `timescaledb-tune`
  had already set `shared_buffers ≈ 458 MB`, `max_connections = 25`,
  `effective_cache_size ≈ 1.4 GB` for the 2 GB box. Overriding would have made it
  worse; left as-is. (A "didn't over-engineer" win.)

---

## T1.2 — Long-range reads: continuous aggregate

Seeded **30 days of 2 Hz data (5,184,000 raw rows)** for one channel, then ran the
"last 30 days, 1-hour buckets" query.

| query path | rows scanned | execution time |
|---|---|---|
| **raw `readings`** | ~5.18 M | **~3,200 ms** |
| **`readings_1m` continuous aggregate** | ~43 k (120× fewer) | **~115 ms** |

→ **~28× faster.** End-to-end `GET /api/series` for the 30-day range is now
**~0.15 s** (721 points) vs. ~3.3 s on raw.

### Fix applied
- Added a **1-minute TimescaleDB continuous aggregate** (`avg/min/max/count`,
  migration `0002`, incremental refresh policy, real-time aggregation on so live
  data is never delayed). `GET /api/series` routes wide ranges (step ≥ 60 s) to
  the rollup and narrow/live ranges to raw, re-bucketing the rollup with
  count-weighted averages so coarser buckets stay exact (unit-tested). A 1-second
  rollup for mid-range high-frequency queries is a trivial symmetric addition if
  the fast channel ever needs it (roadmap, not built).

---

## T1.3 — Failure injection (against the live box)

| injected failure | result |
|---|---|
| **`go-api` restart** mid-ingest | 1500/1500 delivered, ordered, **0 loss** — retry/backoff absorbed the sub-second outage |
| **22 s network black hole** (`docker pause go-api`) | producer hit 10 s read-timeouts, **buffered to disk**, drained **in order** on recovery; 1200/1200, **0 loss, 0 duplicates** |
| **TimescaleDB restart** | **9,313,833 rows persisted** (named volume); go-api reconnected (`/healthz` 200) |
| **Full EC2 reboot** | whole stack **auto-recovered in seconds** via `restart: unless-stopped`; 9.3 M rows persisted; the deliberately-stopped mock collector correctly stayed down |

### Fixes applied
- **`restart: unless-stopped`** on every compose service (verified by the reboot).
- **Bounded offline buffer** (`BUFFER_MAX_BATCHES`): backpressure by dropping the
  oldest batches on overflow, with a counter so loss is visible — a long outage
  can't exhaust the disk (unit-tested). Default unbounded.

---

## T2 — Concurrency

**Concurrent producers** (each its own source token, 300 rd/s each, batch 10).
`landed == sent` at every N (no loss, no duplicates under concurrent inserts).

| producers | aggregate rd/s | p50 | p99 |
|---|---|---|---|
| 2  | 600  | 30 ms | 42 ms |
| 10 | 2999 | 40 ms | 54 ms |
| 25 | 7478 | 68 ms | 182 ms |
| 50 | 7276 (saturated) | 6649 ms | 12662 ms |

→ Comfortable to **~25 concurrent producers / ~7,500 rd/s at batch 10** (the
ceiling scales with batch size — batch 100 reached 22 k/s); same DB-CPU wall;
graceful degradation past it. Dedup-under-concurrency with ambiguous retries was
proven separately (the chaos test: 0 duplicates). No per-source fairness limit
was added — under saturation latency rises uniformly, no starvation observed; a
per-source token bucket is a documented option if a greedy fast producer ever
needs isolating.

**Concurrent viewers** on `/api/series` (random ranges incl. 40-day → rollup):

| clients | req/s | p50 | p99 | errors |
|---|---|---|---|---|
| 10  | 41 | 204 ms | 581 ms | 0 |
| 50  | 42 | 792 ms | 3012 ms | 0 |
| 100 | 35 | 1530 ms | 6874 ms | 0 |

→ ~40 read-queries/s ceiling (wide-range aggregations over 30 days of
high-frequency data — a worst case). Real usage is **< 2 req/s** (a handful of
viewers polling every few seconds over narrow ranges), so reads are a non-issue;
the dashboard is read-mostly and trivially cacheable/CDN-able if it ever weren't.

---

## Scaling roadmap (deliberately NOT built)

The bottleneck is **TimescaleDB CPU on one small box**. In priority order, if it
were ever needed:

1. **Batching** — done (~21×). The single highest-leverage change.
2. **Continuous aggregates** — done. Keeps long-range reads cheap regardless of
   raw volume; add a 1 s rollup for the fast channel when it lands.
3. **Bigger instance** — the bottleneck is DB CPU, so more vCPUs is the obvious,
   cheap next lever (vertical before horizontal).
4. **Managed TimescaleDB** (Timescale Cloud / RDS) — offload DB ops, scale compute
   and storage independently, get backups/HA.
5. **A queue/broker in front of ingest** — *only* if producers ever outpaced
   sustained DB write throughput for long stretches. The on-disk offline buffer
   already absorbs bursts and outages, so this is unnecessary today.
6. **Read replicas / sharding / horizontal** — only at a scale (many labs, many
   high-frequency instruments) far beyond this deployment.

**Why stop here.** The lab has **< 10 instruments** emitting every few seconds
(a few readings/s total); the one planned high-frequency channel is ~50 Hz
(50 rd/s). The current single box sustains **~10,000 rd/s batched** with a
**~22,000 rd/s** ceiling — **100–1000× headroom**. Building consensus, sharding,
a broker, or Kubernetes would be premature engineering, not robustness. The
honest, disciplined move is: batch, roll up, set restart policies, measure, write
the roadmap — and stop.
