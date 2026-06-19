"""Controlled no-data-loss test across a real failure of the DEPLOYED service.

A producer emits a known sequence (value 0..N-1, unique increasing timestamps)
to the deployed /ingest, using the collector's REAL reliability code (DiskBuffer
write-ahead queue + post_batch retry/backoff). While it runs, restart the
go-api container (or drop the network) out-of-band. The producer buffers during
the outage and drains in order on recovery; the server's idempotent ingest
prevents duplicates from any retried-but-applied batch.

Verify afterwards (separately, via psql on the box) that the source holds exactly
values 0..N-1 ordered by ts.

Env: BASE, SOURCE, TOKEN, METRIC, N, RATE, BATCH.
Run:  BASE=https://... SOURCE=bench1 TOKEN=... python bench/reliability_restart.py
"""

from __future__ import annotations

import os
import sys
import threading
import time
from datetime import datetime, timedelta, timezone

import requests

sys.path.insert(0, os.path.abspath(os.path.join(os.path.dirname(__file__), "..", "collector")))
from buffer import DiskBuffer  # noqa: E402
from collector import OK, PERMANENT, post_batch  # noqa: E402
from types import SimpleNamespace  # noqa: E402

BASE = os.getenv("BASE", "https://3.220.132.187.sslip.io")
SOURCE = os.getenv("SOURCE", "bench1")
TOKEN = os.getenv("TOKEN", "")
METRIC = os.getenv("METRIC", "rel")
N = int(os.getenv("N", "1500"))
RATE = float(os.getenv("RATE", "30"))   # readings/sec (paced acquisition)
BATCH = int(os.getenv("BATCH", "5"))


def main() -> int:
    buf = DiskBuffer(os.path.join("/tmp", "rel-restart-buf"))
    for p in buf.pending():
        DiskBuffer.remove(p)
    settings = SimpleNamespace(
        ingest_endpoint=BASE.rstrip("/") + "/ingest", token=TOKEN, source=SOURCE,
        http_timeout=10, max_attempts=6, backoff_base=0.3, backoff_cap=4.0,
    )
    stop = threading.Event()
    done = threading.Event()
    stats = {"sent": 0, "perm": 0}
    epoch = datetime(2026, 6, 1, tzinfo=timezone.utc)

    def acquire():
        # Pace generation at RATE so the run spans ~N/RATE seconds, leaving a
        # window to inject the failure mid-run.
        interval = BATCH / RATE
        i = 0
        while i < N:
            batch = []
            for _ in range(min(BATCH, N - i)):
                batch.append({"metric": METRIC,
                              "ts": (epoch + timedelta(milliseconds=i)).strftime("%Y-%m-%dT%H:%M:%S.%f") + "Z",
                              "value": float(i)})
                i += 1
            buf.enqueue(batch)
            time.sleep(interval)
        done.set()

    def send():
        sess = requests.Session()
        buffering = False
        while not (done.is_set() and buf.count() == 0):
            pending = buf.pending()
            if not pending:
                time.sleep(0.1)
                continue
            progressed = False
            for path in pending:
                batch = buf.load(path)
                res = post_batch(sess, settings, batch, stop)
                if res == OK:
                    buf.remove(path); stats["sent"] += len(batch); progressed = True
                    if buffering:
                        print(f"  [recovered] draining buffer, {buf.count()} batches left", flush=True)
                        buffering = False
                elif res == PERMANENT:
                    buf.remove(path); stats["perm"] += 1; progressed = True
                else:
                    if not buffering:
                        print(f"  [buffering] delivery failing, {buf.count()} batches queued on disk", flush=True)
                        buffering = True
                    break
            if not progressed:
                time.sleep(1.0)

    ta = threading.Thread(target=acquire)
    ts = threading.Thread(target=send)
    t0 = time.time()
    ta.start(); ts.start()
    print(f"producing {N} readings to {SOURCE}/{METRIC} over ~{N/RATE:.0f}s — restart go-api now", flush=True)
    ta.join(); ts.join()
    print(f"done in {time.time()-t0:.1f}s (sent={stats['sent']}, permanent_failures={stats['perm']}, buffer_remaining={buf.count()})", flush=True)
    return 0


if __name__ == "__main__":
    sys.exit(main())
