# lab/ — high-rate temperature → AWS producer

`ls350_fast_aws.py` is a **second client** of the lab's existing Lake Shore 350
TCP bridge (`U_Lakeshore350_Server.py`). It reads the four temperature channels
at a high rate (default **1 s**) and pushes them **only to AWS** — it does not
touch InfluxDB.

The lab is otherwise **unchanged**: keep running the original InfluxDB logger
(`U_Lakeshore350_Logger.py`) as the slow client; this runs alongside it as the
fast client. The bridge serializes both behind its lock, so there's no collision.
Pressures and the Grafana mirror are handled separately by the `forwarder/`
service on the EC2 box.

## Setup (on the lab PC)

```bash
pip install requests                      # once
copy credentials.example.json credentials.local.json   # then edit it
python ls350_fast_aws.py
```

`credentials.local.json` (git-ignored — never commit it) holds:

| key | value |
|-----|-------|
| `bridge_host` | the LS350 bridge IP (same machine → `127.0.0.1`) |
| `bridge_port` | `5001` |
| `bridge_token`| the bridge shared secret |
| `aws_ingest_url` | `https://3.220.132.187.sslip.io/ingest` |
| `aws_token` | the `unisoku-stm` ingest key (`INGEST_TOKEN` in `deploy/.env`) |
| `sample_interval_s` | `1.0` |

The producer maps channels A/B/C/D → `SORB / 1K Pot / He3 Pot / STM`, the same
metric names the AWS dashboard uses, so the data lands on the right charts.
