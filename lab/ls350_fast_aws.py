"""High-rate Lake Shore 350 -> AWS producer (AWS only).

A *second* client of the LS350 TCP bridge (the same `U_Lakeshore350_Server.py`
the lab already runs). It reads the four temperature channels at a high rate
(default 1 s) and POSTs them only to the AWS lab-monitor — it does NOT write to
InfluxDB. The lab's original InfluxDB logger keeps running unchanged as the slow
client; the bridge serializes both behind its lock, so they coexist.

This gives the AWS dashboard high-resolution temperatures the Grafana/InfluxDB
path doesn't capture, while leaving the lab setup exactly as it was.

Credentials live in a git-ignored file next to this script
(`credentials.local.json`); copy `credentials.example.json` and fill it in. The
code carries no secrets, so it can live in the repo.
"""

from __future__ import annotations

import json
import os
import socket
import sys
import time
from datetime import datetime, timezone

import requests

HERE = os.path.dirname(os.path.abspath(__file__))


def load_config() -> dict:
    path = os.path.join(HERE, "credentials.local.json")
    if not os.path.exists(path):
        sys.exit(
            f"missing {path}\n"
            "Copy credentials.example.json -> credentials.local.json and fill it in."
        )
    with open(path) as fh:
        return json.load(fh)


CFG = load_config()
BRIDGE_HOST = CFG["bridge_host"]
BRIDGE_PORT = int(CFG["bridge_port"])
BRIDGE_TOKEN = CFG["bridge_token"]
AWS_INGEST_URL = CFG["aws_ingest_url"]
AWS_TOKEN = CFG["aws_token"]
SOURCE = CFG.get("source", "unisoku-stm")
INTERVAL = float(CFG.get("sample_interval_s", 1.0))
HTTP_TIMEOUT = float(CFG.get("http_timeout_s", 5.0))

# LS350 input channel -> AWS dashboard metric name.
CHANNELS = {"A": "SORB", "B": "1K Pot", "C": "He3 Pot", "D": "STM"}


def ls350_query(cmd: str) -> str:
    """Same bridge protocol as the lab's logger: '<TOKEN> <cmd>' over TCP."""
    with socket.create_connection((BRIDGE_HOST, BRIDGE_PORT), timeout=3) as s:
        s.sendall(f"{BRIDGE_TOKEN} {cmd}\r\n".encode("ascii"))
        return s.recv(4096).decode("ascii", errors="ignore").strip()


def read_kelvin(channel: str) -> float | None:
    try:
        reply = ls350_query(f"KRDG? {channel}")
        return float(reply.split(",")[-1].strip())
    except (OSError, ValueError) as exc:
        print(f"[read] {channel}: {exc}", flush=True)
        return None


def post(batch: list[dict]) -> None:
    """Best-effort POST; never crashes the loop."""
    if not batch:
        return
    try:
        requests.post(AWS_INGEST_URL, json=batch,
                      headers={"X-Api-Key": AWS_TOKEN}, timeout=HTTP_TIMEOUT)
    except requests.RequestException as exc:
        print(f"[aws] post failed: {exc}", flush=True)


def main() -> None:
    print(f"ls350_fast_aws: {BRIDGE_HOST}:{BRIDGE_PORT} -> {AWS_INGEST_URL} "
          f"source={SOURCE} every {INTERVAL:g}s", flush=True)
    while True:
        start = time.time()
        ts = datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.%f")[:-3] + "Z"
        batch = []
        for ch, metric in CHANNELS.items():
            value = read_kelvin(ch)
            if value is not None:
                batch.append({"source": SOURCE, "metric": metric, "ts": ts, "value": value})
        post(batch)
        # keep a steady cadence regardless of how long the reads took
        time.sleep(max(0.0, INTERVAL - (time.time() - start)))


if __name__ == "__main__":
    main()
