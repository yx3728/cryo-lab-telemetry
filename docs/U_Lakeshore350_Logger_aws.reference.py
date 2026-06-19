# REDACTED REFERENCE — not run from this repo.
#
# This is the lab-PC dual-upload logger (InfluxDB/Grafana + AWS lab-monitor) with
# all lab secrets replaced by <placeholders>. The real, runnable copy lives on the
# instrument PC next to the original logger. See docs/HARDWARE_INTERFACE.md.
#
# It is a MINIMAL edit of the lab's existing U_Lakeshore350_Logger.py: the serial-
# bridge read path and the InfluxDB write are unchanged; the only additions
# (marked "ADDED") collect each loop's readings and POST them to the AWS ingest.

import os
import socket
import time
from datetime import datetime, timezone

import requests
from influxdb_client import InfluxDBClient, Point
from influxdb_client.client.write_api import SYNCHRONOUS
from influxdb_client.rest import ApiException

LS350_HOST = "<LAN-IP>"        # the serial↔TCP bridge (U_Lakeshore350_Server.py)
LS350_PORT = 5001
TOKEN = "<SHARED_SECRET>"      # MUST MATCH THE SERVER


def ls350_query(cmd):
    with socket.create_connection((LS350_HOST, LS350_PORT), timeout=3) as s:
        s.sendall(f"{TOKEN} {cmd}\r\n".encode("ascii"))
        return s.recv(4096).decode("ascii").strip()


# === INFLUXDB CONFIG (unchanged — keeps the existing Grafana dashboard working) ===
bucket = "Temperature_Logger"
token = "<INFLUXDB_TOKEN>"
org = "<lab org>"
url = "https://<region>.aws.cloud2.influxdata.com"
client = InfluxDBClient(url=url, token=token, org=org)
write_api = client.write_api(write_options=SYNCHRONOUS)

# === AWS lab-monitor ingest (ADDED) ===
AWS_INGEST_URL = "https://3.220.132.187.sslip.io/ingest"
AWS_SOURCE = "unisoku-stm"
AWS_TOKEN = os.environ.get("LABMON_TOKEN", "<UNISOKU_STM_INGEST_TOKEN>")
AWS_TIMEOUT = 5

aws_metric = {"SORB": "SORB", "1KPOT": "1K Pot", "HE3POT": "He3 Pot", "STM": "STM"}


def push_to_aws(batch):
    """Best-effort POST; never raises into the main loop (AWS down ≠ InfluxDB down)."""
    if not batch:
        return
    try:
        requests.post(AWS_INGEST_URL, json=batch,
                      headers={"X-Api-Key": AWS_TOKEN}, timeout=AWS_TIMEOUT)
    except Exception as e:
        print(f"[AWS Warning] {e}")


channels = {"A": "SORB", "B": "1KPOT", "C": "HE3POT", "D": "STM"}


def query_temperature(channel):
    reply = ls350_query(f"KRDG? {channel}")
    try:
        return float(reply)
    except ValueError:
        print(f"[Parse Error] {channel}: {reply}")
        return None


# === MAIN LOOP ===
while True:
    aws_batch = []                                                          # ADDED
    aws_ts = datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.%f")[:-3] + "Z"  # ADDED
    try:
        for ch, label in channels.items():
            temp = query_temperature(ch)
            if temp is not None:
                aws_batch.append({                                          # ADDED
                    "source": AWS_SOURCE,
                    "metric": aws_metric.get(label, label),
                    "ts": aws_ts,
                    "value": temp,
                })
                point = (
                    Point("temperature_reading")
                    .tag("channel", ch)
                    .tag("label", label)
                    .field("temperature", temp)
                    .time(time.time_ns())
                )
                write_api.write(bucket=bucket, org=org, record=point)
            else:
                print(f"[Warning] No data for {label} ({ch})")
    except ApiException as e:
        print(f"[InfluxDB Error] {e.status} - {e.reason}\n{e.body}")
    except Exception as e:
        print(f"[Error] {e}")

    push_to_aws(aws_batch)                                                  # ADDED
    time.sleep(20)
