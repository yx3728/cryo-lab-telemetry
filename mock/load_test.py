"""Load + chaos test for the ingest pipeline.

It drives several concurrent producers — plus one dense "high-frequency" producer
standing in for the planned fast STM channel — through the ChaosProxy (latency,
packet loss, ambiguous failures, and a hard outage window) into the REAL Go
ingest + TimescaleDB. The producers reuse the collector's actual reliability code
(DiskBuffer write-ahead queue + post_batch retry/backoff), so this exercises the
exact delivery path that runs in production.

Each producer emits readings with value = 0,1,2,... at strictly increasing
timestamps, so after the dust settles we can prove three things directly from the
database:

  (a) NO DATA LOSS      every generated reading is present
  (b) CORRECT ORDERING  ordered by ts, the values are exactly 0..N-1
  (c) IDEMPOTENCY       no duplicates, despite retries of ambiguous writes

Run (with the dev stack up and a 'loadtest' ingest token configured):
    INGEST_TOKEN=loadtest-token python mock/load_test.py
"""

from __future__ import annotations

import os
import subprocess
import sys
import threading
import time
from datetime import datetime, timedelta, timezone
from types import SimpleNamespace

import requests

# Reuse the collector's real reliability code.
sys.path.insert(0, os.path.abspath(os.path.join(os.path.dirname(__file__), "..", "collector")))
from buffer import DiskBuffer  # noqa: E402
from collector import OK, PERMANENT, post_batch  # noqa: E402

sys.path.insert(0, os.path.dirname(__file__))
from chaos_proxy import ChaosProxy  # noqa: E402

# --- configuration -----------------------------------------------------------
UPSTREAM = os.getenv("UPSTREAM", "http://localhost:8080")
SOURCE = os.getenv("SOURCE", "loadtest")
TOKEN = os.getenv("INGEST_TOKEN", "loadtest-token")
NORMAL_PRODUCERS = int(os.getenv("NORMAL_PRODUCERS", "4"))
NORMAL_N = int(os.getenv("NORMAL_N", "400"))
HIFREQ_N = int(os.getenv("HIFREQ_N", "2000"))
BATCH = int(os.getenv("BATCH", "40"))
OUTAGE_AT = float(os.getenv("OUTAGE_AT", "2.0"))
OUTAGE_SECS = float(os.getenv("OUTAGE_SECS", "3.0"))
MAX_WALL = float(os.getenv("MAX_WALL", "120"))


def psql(sql: str) -> str:
    base = os.getenv("PSQL_CMD")
    cmd = (base.split() if base else
           ["docker", "exec", "-i", "lab-monitor-dev-timescaledb-1",
            "psql", "-U", "labmon", "-d", "labmon", "-At", "-c"]) + [sql]
    return subprocess.check_output(cmd, text=True).strip()


def iso_us(dt: datetime) -> str:
    return dt.strftime("%Y-%m-%dT%H:%M:%S.%f") + "Z"


def generate(metric: str, n: int, base: datetime) -> list[dict]:
    # value i at base + i ms -> unique, strictly-increasing timestamps.
    return [{"metric": metric, "ts": iso_us(base + timedelta(milliseconds=i)), "value": float(i)}
            for i in range(n)]


def make_settings(endpoint: str) -> SimpleNamespace:
    return SimpleNamespace(
        ingest_endpoint=endpoint, token=TOKEN, source=SOURCE, http_timeout=10,
        max_attempts=6, backoff_base=0.05, backoff_cap=0.5,
    )


def drain(buf: DiskBuffer, settings: SimpleNamespace, stop: threading.Event, stats: dict) -> None:
    session = requests.Session()
    while not stop.is_set():
        pending = buf.pending()
        if not pending:
            return
        progressed = False
        for path in pending:
            if stop.is_set():
                return
            batch = buf.load(path)
            result = post_batch(session, settings, batch, stop)
            if result == OK:
                buf.remove(path)
                stats["delivered"] += len(batch)
                progressed = True
            elif result == PERMANENT:
                buf.remove(path)
                stats["permanent"] += len(batch)
                progressed = True
            else:  # transient -> stop to preserve order, retry next sweep
                break
        if not progressed:
            stop.wait(0.3)


def main() -> int:
    print(f"clearing prior '{SOURCE}' data ...")
    psql(f"DELETE FROM readings WHERE source='{SOURCE}'")

    proxy = ChaosProxy(UPSTREAM, port=8099, seed=1, latency_ms=15,
                       drop_rate=0.15, ambiguous_rate=0.10)
    proxy.start()
    endpoint = proxy.url() + "/ingest"
    settings = make_settings(endpoint)

    # Build producers: N normal + 1 dense high-frequency, each its own metric +
    # write-ahead buffer (pre-loaded so the test focuses on delivery under chaos).
    producers: list[tuple[str, int, DiskBuffer]] = []
    expected: dict[str, int] = {}
    base = datetime(2026, 1, 1, tzinfo=timezone.utc)
    plan = [(f"p{i}", NORMAL_N) for i in range(NORMAL_PRODUCERS)] + [("hifreq", HIFREQ_N)]
    for idx, (metric, n) in enumerate(plan):
        buf = DiskBuffer(os.path.join("/tmp", f"loadtest-{metric}"))
        for p in buf.pending():
            DiskBuffer.remove(p)  # start clean
        readings = generate(metric, n, base + timedelta(hours=idx))
        for s in range(0, n, BATCH):
            buf.enqueue(readings[s:s + BATCH])
        producers.append((metric, n, buf))
        expected[metric] = n

    total = sum(expected.values())
    stats = {"delivered": 0, "permanent": 0}
    stop = threading.Event()

    # Monitor peak buffer depth across all producers.
    peak = {"depth": 0}

    def monitor():
        while not stop.wait(0.1):
            depth = sum(b.count() for _, _, b in producers)
            peak["depth"] = max(peak["depth"], depth)

    threading.Thread(target=monitor, daemon=True).start()

    print(f"starting {len(producers)} concurrent producers, {total} readings "
          f"(hifreq={HIFREQ_N}), chaos: 15ms latency, 15% drop, 10% ambiguous, "
          f"{OUTAGE_SECS:.0f}s outage @ t+{OUTAGE_AT:.0f}s")

    t0 = time.time()
    senders = [threading.Thread(target=drain, args=(b, settings, stop, stats))
               for _, _, b in producers]
    for s in senders:
        s.start()

    # Schedule the hard outage window.
    def outage_window():
        time.sleep(OUTAGE_AT)
        proxy.outage = True
        time.sleep(OUTAGE_SECS)
        proxy.outage = False

    threading.Thread(target=outage_window, daemon=True).start()

    # Wait for every producer's buffer to drain (bounded by MAX_WALL).
    for s in senders:
        s.join(timeout=max(0.0, t0 + MAX_WALL - time.time()))
    drained = time.time() - t0
    stop.set()
    proxy.stop()

    remaining = sum(b.count() for _, _, b in producers)
    print(f"\ndrain finished in {drained:.1f}s "
          f"(delivered batches acked={stats['delivered']}, permanent={stats['permanent']}, "
          f"buffer remaining={remaining})")
    print(f"proxy: {proxy.stats}, peak buffer depth={peak['depth']} batches")

    # --- verification straight from the database -----------------------------
    ok = True
    db_total = int(psql(f"SELECT count(*) FROM readings WHERE source='{SOURCE}'"))
    for metric, n in expected.items():
        cnt = int(psql(f"SELECT count(*) FROM readings WHERE source='{SOURCE}' AND metric='{metric}'"))
        ordered = psql(
            "SELECT coalesce(bool_and(value = rn), false) FROM "
            "(SELECT value, row_number() over (ORDER BY ts) - 1 AS rn "
            f"FROM readings WHERE source='{SOURCE}' AND metric='{metric}') t")
        loss = n - cnt
        status = "OK" if (cnt == n and ordered == "t") else "FAIL"
        if status == "FAIL":
            ok = False
        print(f"  {metric:8s} generated={n:5d} stored={cnt:5d} loss={loss:3d} "
              f"ordered={ordered} -> {status}")

    throughput = total / drained if drained > 0 else 0
    print(f"\ntotals: generated={total} stored={db_total} "
          f"loss={total - db_total} duplicates={db_total - (total - 0)} "
          f"throughput={throughput:.0f} readings/s through chaos")

    if ok and db_total == total and stats["permanent"] == 0:
        print("\nRESULT: PASS — no data loss, correct ordering, idempotent under chaos")
        return 0
    print("\nRESULT: FAIL")
    return 1


if __name__ == "__main__":
    sys.exit(main())
