# Hardware ↔ software interface — Unisoku STM cryostat temperature logging

Technical description of how cryostat temperatures get from the instrument to the
dashboards, and where the AWS lab-monitor ingest plugs in. This is the "last
piece" that connects the real lab to the system in this repo.

> **Privacy:** equipment models and the protocol are described in full; LAN
> addresses, shared secrets, and cloud API tokens are redacted (`<…>`).

## 1. Equipment & physical layer

- **Controller:** Lake Shore **Model 350** cryogenic temperature controller (4
  sensor inputs A–D), reading the cryostat's calibrated thermometers.
- **Link:** RS-232 serial from the Model 350 to the lab Windows PC
  ("CRYOGENICSYSTEM"), on **`COM6`**.
- **Serial parameters (note the unusual framing):**
  **57600 baud, 7 data bits, odd parity, 1 stop bit (7-O-1)**, no flow control.
  This 7O1 framing is a Lake Shore quirk and must match exactly or replies are
  garbled.
- Commands and replies are **ASCII, LF-terminated** (`\n`).

## 2. Command protocol (Lake Shore SCPI subset)

The logger only *reads*; the controller's full command set is also used
interactively for setup (see the lab's test notebook). Commands actually used:

| Command | Direction | Meaning |
|---|---|---|
| `KRDG? <ch>` | query | Read channel `<ch>` temperature in **Kelvin** (returns a float) |
| `RANGE? <out>` | query | Heater output range (0 = off, 1–5 = range) — setup/diagnostics |
| `RANGE <out>,<n>` | set | Set heater output range — **manual control only** |
| `SETP <out>,<K>` | set | Set heater setpoint — **manual control only** |

The logging path issues **only `KRDG?`**; it never changes heater state.

## 3. Channel map

| LS350 input | Logger label | Physical meaning | AWS dashboard metric |
|---|---|---|---|
| `A` | `SORB`   | Sorption-pump stage | `SORB` |
| `B` | `1KPOT`  | 1 K pot | `1K Pot` |
| `C` | `HE3POT` | He-3 pot | `He3 Pot` |
| `D` | `STM`    | STM stage | `STM` |

(The AWS-side names differ only in formatting; the dual-upload logger maps them
so readings land on the right charts — see §5.)

## 4. Software architecture

Three small Python pieces on the lab PC (Miniconda environment):

```
 Lake Shore 350 ──RS-232 (COM6, 7O1, 57600)──▶  U_Lakeshore350_Server.py
                                                  • holds the one serial handle
                                                  • TCP server on port 5001
                                                  • shared-secret auth + a lock
                                                  • multiplexes many clients
                                                        │  TCP (localhost / LAN)
                                                        ▼
                                                 U_Lakeshore350_Logger.py  (client)
                                                  • polls KRDG? A/B/C/D every 20 s
                                                  • writes to InfluxDB Cloud ─▶ Grafana
```

**`U_Lakeshore350_Server.py` — the serial↔TCP bridge (the actual hardware owner).**
- Opens `COM6` (7O1/57600) once and keeps it open.
- Listens on TCP **port 5001**; spawns a daemon thread per client.
- **Auth:** every request line must begin with `"<SHARED_SECRET> "`; otherwise it
  replies `ERROR: unauthorized`. (The secret is a simple shared token, not the
  AWS system's per-source key.)
- Strips the secret, forwards the remaining text as a command to the LS350 under a
  `threading.Lock` (so concurrent clients can't interleave on the serial line),
  reads one LF-terminated reply, and returns it `\r\n`-terminated.
- A reply is only awaited when the command contains `?` (a query).

Why a server in front of one serial port: a serial port is single-owner, but
several tools (the logger, ad-hoc notebooks, setup scripts) need the controller.
The TCP bridge serialises access behind one lock and lets multiple clients share
the instrument safely.

**`U_Lakeshore350_Logger.py` — the logging client.**
- Opens a short-lived TCP connection per query to the bridge
  (`<LAN-IP>:5001`), sends `"<SHARED_SECRET> KRDG? <ch>"`, parses the float.
- For each of A/B/C/D, writes an InfluxDB point
  (`measurement=temperature_reading`, tags `channel`/`label`, field
  `temperature`, ns timestamp) to **InfluxDB Cloud** → the existing **Grafana**
  dashboard.
- Loops every **20 s** (`time.sleep(20)`).

**`Start_temperature_logger.bat` — launcher.** Activates Miniconda, `cd`s to the
folder, and `start /min python U_Lakeshore350_Logger.py`. (The bridge server runs
persistently/separately.)

> InfluxDB Cloud endpoint/org/token are configured in-file and are **redacted
> here**: `url=https://<region>.aws.cloud2.influxdata.com`, `org=<lab org>`,
> `token=<INFLUXDB_TOKEN>`, `bucket=Temperature_Logger`.

## 5. Where AWS ingest plugs in (the last piece)

The self-hosted system in this repo wants the **same readings** the logger already
has. The integration is a **single addition inside the existing loop** — the
hardware read path and the InfluxDB write are untouched (see
`U_Lakeshore350_Logger_aws.py`):

1. Each loop, collect the four `KRDG?` readings into a small batch of
   `{source, metric, ts, value}` (mapping labels → AWS metric names per §3, `ts`
   = current UTC RFC3339).
2. After the InfluxDB writes, `POST` the batch once to
   `https://3.220.132.187.sslip.io/ingest` with header `X-Api-Key: <unisoku-stm
   ingest token>`.
3. The POST is **best-effort** (`try/except`, 5 s timeout): if AWS is unreachable
   the InfluxDB/Grafana logging the lab depends on is **never** affected. The
   server's ingest is **idempotent** (`(source, metric, ts)` unique), so a
   retried or duplicated point is harmless.

Result: every 20 s the readings go to **both** InfluxDB/Grafana **and** the AWS
dashboard. Rate is the same single `time.sleep(20)`.

```
 ... U_Lakeshore350_Logger_aws.py loop (every 20 s):
        read A/B/C/D via the bridge ──▶ InfluxDB Cloud ──▶ Grafana   (unchanged)
                                   └──▶ POST /ingest    ──▶ AWS lab-monitor  (added)
```

## 6. Going live — operational notes

- **Dependency:** the dual-upload logger needs `requests`
  (`pip install requests` / `conda install requests`). Once.
- **Token:** set the `unisoku-stm` ingest key (the `INGEST_TOKEN` value in
  `deploy/.env` on the EC2 box) via the `LABMON_TOKEN` env var (e.g. in the
  `.bat`) or by pasting it into `AWS_TOKEN` in the script.
- **Stop the demo feed:** the cloud runs a mock producer on `source=unisoku-stm`
  so the dashboard isn't empty pre-launch. When real data starts flowing, stop it
  so the dashboard shows only real readings:
  `cd deploy && docker compose stop collector`.
- **Verify:** within ~20 s the four temperatures appear on
  `https://3.220.132.187.sslip.io`; `/metrics` shows `unisoku-stm` `last_seen`
  advancing.
- **Rollback:** revert to `Start_temperature_logger.bat` / the original logger.
  Nothing about the InfluxDB/Grafana path changed, so the two can run in parallel
  during the transition.

## 7. Out of scope (this folder)

This folder is the **temperature** path (Lake Shore 350, 4 channels). The vacuum
**pressure** channels (`LL`, `PC`, `OC`, `PREP`) shown on the dashboard come from
a **separate gauge logger** not in this folder; wiring those to AWS is the same
one-loop addition applied to that logger (map gauge labels → `LL/PC/OC/PREP`,
batch, `POST /ingest`).
