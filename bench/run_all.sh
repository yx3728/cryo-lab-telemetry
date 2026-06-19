#!/usr/bin/env bash
# Reproduce the load suite against the deployed box. Prereqs:
#   - `go build -o bench .`
#   - `./gen_tokens.sh 50` and EXTRA_INGEST_TOKENS set on the box (see README).
# Failure-injection tests (restart/pause/reboot) are run separately — see
# BENCHMARKS.md / WORKLOG.md.
set -euo pipefail
BASE="${BASE:-https://3.220.132.187.sslip.io}"
DIR="$(cd "$(dirname "$0")" && pwd)"
BIN="${BIN:-$DIR/bench}"
TOK=$(python3 -c "import json;print(json.load(open('$DIR/tokens.local.json'))[0]['token'])")

echo "### T1.1 unbatched ramp (batch=1)"
for R in 200 500 1000 1500 3000; do
  "$BIN" ingest -base "$BASE" -source bench0 -token "$TOK" -rate "$R" -batch 1 \
    -duration 8 -concurrency 128 -csv "$DIR/unbatched.csv"
done

echo "### T1.1 batched ramp (batch=100)"
for R in 5000 10000 25000 50000 100000 200000; do
  "$BIN" ingest -base "$BASE" -source bench0 -token "$TOK" -rate "$R" -batch 100 \
    -duration 8 -concurrency 64 -csv "$DIR/batched.csv"
done

echo "### T2 concurrent producers ramp"
for N in 2 5 10 25 50; do
  "$BIN" producers -base "$BASE" -tokens "$DIR/tokens.local.json" -n "$N" -rate 300 \
    -batch 10 -duration 12 -concurrency-per 8 -csv "$DIR/producers.csv"
done

echo "### T2 concurrent viewers ramp"
for M in 10 50 100; do
  "$BIN" read -base "$BASE" -clients "$M" -duration 12 -csv "$DIR/read.csv"
done

echo "done — see *.csv"
