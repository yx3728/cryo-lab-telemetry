# bench/ — load + reliability harness

A small Go (stdlib-only) load generator used for the hardening pass documented in
[`../BENCHMARKS.md`](../BENCHMARKS.md). **Run it from a workstation against the
deployed box**, not localhost, so the numbers reflect the real `t4g.small` over
the real network.

## Build

```bash
cd bench && go build -o bench .
```

## Subcommands

```bash
# Single producer at a target readings/sec; compare batch sizes.
bench ingest -base https://3.220.132.187.sslip.io -source bench0 -token <tok> \
      -rate 10000 -batch 100 -duration 10 -concurrency 64

# N concurrent producers, each its own source token (from tokens.local.json).
bench producers -base <url> -tokens tokens.local.json -n 25 -rate 300 -batch 10

# M concurrent read clients on /api/series (random ranges).
bench read -base <url> -clients 50 -duration 12   # or -source/-metric to pin
```

Each prints a JSON result (and appends to `-csv <file>` if given): achieved
throughput, p50/p99/max latency, errors, and `db_rows_delta` (rows that actually
landed, via `/metrics`).

**Load model:** open-loop — each request gets an intended send time and latency
is measured from it, so falling behind shows up as tail latency (coordinated-
omission correction) rather than being hidden. The client uses HTTP/1.1 (a
dedicated connection per in-flight request) to model independent producers and
avoid HTTP/2 head-of-line stalls.

## Reproducing the full suite

Concurrent producers need per-source tokens on the server:

```bash
# 1. generate throwaway tokens (writes git-ignored tokens.local.json + bench_tokens.env)
./gen_tokens.sh 50
# 2. add them to the box and recreate go-api (EXTRA_INGEST_TOKENS), e.g.:
scp bench_tokens.env ubuntu@HOST:/tmp/ && \
  ssh ubuntu@HOST 'cd lab-monitor/deploy && cat /tmp/bench_tokens.env >> .env && docker compose up -d go-api'
# 3. run everything
./run_all.sh
# 4. afterwards: remove EXTRA_INGEST_TOKENS from the box .env, recreate go-api,
#    and delete bench data:  DELETE FROM readings WHERE source <> 'unisoku-stm';
```

`run_all.sh` drives the ingest ramp (batched vs unbatched), the producer ramp, and
the viewer ramp, writing CSVs. Verification of the failure-injection scenarios
(container/DB restart, reboot, network black hole) is scripted ad hoc in the
worklog — those involve `docker compose restart/pause` and `sudo reboot` on the
box. `reliability_restart.py` is the controlled no-loss producer used there.

> Throwaway bench tokens and CSV outputs are git-ignored. Never commit them.
