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
INTERVAL = float(CFG.get("sample_interval_s", 1.0))      # starting rate; the
# dashboard (admin → sampling interval) can change this live (see config poll).
HTTP_TIMEOUT = float(CFG.get("http_timeout_s", 5.0))
CONFIG_POLL_S = float(CFG.get("config_poll_s", 15.0))
# The public config endpoint lives next to /ingest on the same host.
CONFIG_URL = AWS_INGEST_URL.rsplit("/ingest", 1)[0] + "/api/config"

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


def post(session: requests.Session, batch: list[dict]) -> None:
    """Best-effort POST; never crashes the loop."""
    if not batch:
        return
    try:
        session.post(AWS_INGEST_URL, json=batch,
                     headers={"X-Api-Key": AWS_TOKEN}, timeout=HTTP_TIMEOUT)
    except requests.RequestException as exc:
        print(f"[aws] post failed: {exc}", flush=True)


def fetch_interval(session: requests.Session, current: float) -> float:
    """Read the admin-set sampling interval from the dashboard config (public).
    Returns `current` unchanged on any error, so the dashboard being unreachable
    never disrupts logging."""
    try:
        resp = session.get(CONFIG_URL, timeout=HTTP_TIMEOUT)
        if resp.ok:
            v = float(resp.json().get("sampling_interval_seconds", current))
            if v > 0:
                return v
    except (requests.RequestException, ValueError):
        pass
    return current


def main() -> None:
    session = requests.Session()
    interval = fetch_interval(session, INTERVAL)  # adopt the dashboard's value at start
    last_cfg = time.time()
    print(f"ls350_fast_aws: {BRIDGE_HOST}:{BRIDGE_PORT} -> {AWS_INGEST_URL} "
          f"source={SOURCE} every {interval:g}s (rate controlled from the dashboard)", flush=True)
    while True:
        start = time.time()
        # Periodically adopt the dashboard's sampling interval (the control loop).
        if time.time() - last_cfg >= CONFIG_POLL_S:
            new = fetch_interval(session, interval)
            if new != interval:
                print(f"[config] sampling interval {interval:g}s -> {new:g}s", flush=True)
                interval = new
            last_cfg = time.time()

        ts = datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.%f")[:-3] + "Z"
        batch = []
        for ch, metric in CHANNELS.items():
            value = read_kelvin(ch)
            if value is not None:
                batch.append({"source": SOURCE, "metric": metric, "ts": ts, "value": value})
        post(session, batch)
        # keep a steady cadence regardless of how long the reads took
        time.sleep(max(0.0, interval - (time.time() - start)))


if __name__ == "__main__":
    main()
