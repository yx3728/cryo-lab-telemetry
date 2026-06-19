# mock/ — chaos + load harness

This directory holds the fault-injection tooling used by the **load/chaos test**.
The normal few-second multi-channel producer is just the collector running with
its `MockReader` (see `../collector`); this harness is specifically for stressing
*delivery reliability*.

- **`chaos_proxy.py`** — a fault-injecting HTTP proxy that forwards `/ingest` to
  the real Go server while injecting latency, packet loss, *ambiguous* failures
  (forward-then-hide-success, the case idempotency must absorb), and hard outage
  windows. Standard library only.
- **`load_test.py`** — spins up several concurrent producers plus one dense
  high-frequency producer (standing in for the planned ~50 Hz STM-current
  channel), driving them through the proxy using the collector's **real**
  `DiskBuffer` + `post_batch` code. It then verifies, straight from TimescaleDB,
  that there was **no data loss**, **correct ordering**, and **no duplicates**
  (idempotency) despite the injected chaos.

## Run

With the dev stack up (`deploy/docker-compose.dev.yml`) and a `loadtest` ingest
token configured on the server (`INGEST_TOKENS=...,loadtest:loadtest-token`):

```bash
INGEST_TOKEN=loadtest-token ../collector/.venv/bin/python load_test.py
```

It prints a per-producer table and a final `PASS`/`FAIL`. Verification reads the
database directly via `docker exec ... psql` (override with `PSQL_CMD`).

## Representative result

```
5 concurrent producers, 3600 readings (hifreq=2000),
chaos: 15ms latency, 15% drop, 10% ambiguous, 3s outage
proxy: forwarded=90 dropped=21 ambiguous=7 outage_blocked=13, peak buffer depth=76 batches
totals: generated=3600 stored=3600 loss=0 duplicates=0  ~665 readings/s through chaos
RESULT: PASS — no data loss, correct ordering, idempotent under chaos
```
