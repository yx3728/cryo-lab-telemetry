# Hardware ↔ software interface — Unisoku STM cryostat temperature logging

Technical description of how cryostat temperatures get from the instrument to the
dashboards, and where the AWS lab-monitor ingest plugs in. This is the "last
piece" that connects the real lab to the system in this repo.

> **Privacy:** equipment models and the protocol are described in full; LAN
> addresses, shared secrets, and cloud API tokens are redacted (`<…>`).

## 1. Equipment & physical layer

- **Controller:** Lake Shore **Model 350** cryogenic temperature controller (4
  sensor inputs A–D), reading the cryostat's calibrated thermometers.
- **Link:** RS-232 serial from the Model 350 to the lab Windows PC, on **`COM6`**.
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

## 5. AWS mirroring architecture (final)

The guiding constraint: **leave the lab exactly as it is** — the original InfluxDB
logger and the PhD student's QCoDeS acquisition suite (which owns the vacuum gauge
on an exclusive serial port and already publishes pressures to InfluxDB) are not
modified. Two collision considerations drive the design:

- **LS350 / temperatures** are reached through the TCP **bridge** (`port 5001`),
  which is built for many clients (server-side lock). So a second temperature
  client is safe.
- **The vacuum gauge** (`COM1`) is read over an **exclusive** serial port owned by
  the suite — a second process can't open it. But the suite already publishes
  pressures to **InfluxDB Cloud**, which allows many readers.

So AWS is fed by two additions, neither of which disturbs the lab:

1. **InfluxDB → AWS forwarder** (`forwarder/`, runs on the EC2 box). A read-only
   InfluxDB consumer that mirrors both `Temperature_Logger` and `Pressure_Logger`
   into AWS — replicating the Grafana view. Idempotent (each point keeps its
   InfluxDB timestamp), skips a gauge reading 0 (off, not log-plottable).
2. **High-rate temperature producer** (`lab/ls350_fast_aws.py`, runs on the lab
   PC). A *second* bridge client that reads `KRDG?` at **1 s** and POSTs only to
   AWS — adding high-resolution temperatures the InfluxDB/Grafana path doesn't
   capture. The original InfluxDB logger keeps running as the slow client.

```
 Lab PC                                           InfluxDB Cloud        EC2
 ┌───────────────────────────────────────┐
 │ LS350 bridge (5001) ── original logger ──────▶ Temperature_Logger ──┐
 │           │           (slow, unchanged)                              │
 │           └────────── ls350_fast_aws.py ─────────────────────────┐  │  forwarder
 │                       (1 s, AWS only) ──────────────────────────┐│  ├─▶ (read InfluxDB)
 │ suite: gauge COM1 ── pressure logger ────────▶ Pressure_Logger ─┼┼──┘        │
 └───────────────────────────────────────┘                        ││           ▼
                                                                   │└──────▶ AWS /ingest
                                                                   └──────▶ AWS /ingest
                                                          (temps: dense 1 s + slow mirror;
                                                           pressures: from forwarder)
```

All AWS posts are idempotent (`(source, metric, ts)` unique), so the slow mirror
and the 1 s producer coexisting on the same channels never create duplicates.

## 6. Going live — operational notes

- **Forwarder (EC2):** a default service in `deploy/docker-compose.yml`; needs the
  lab's InfluxDB credentials in `deploy/.env` on the box. Replaces the demo mock
  feed (which stays off — `docker compose stop collector`).
- **Fast producer (lab PC):** copy `lab/`, `pip install requests`, fill
  `credentials.local.json` (bridge host/token + the `unisoku-stm` ingest key), run
  `ls350_fast_aws.py`. Runs alongside the unchanged original InfluxDB logger.
- **Verify:** temperatures refresh on `https://3.220.132.187.sslip.io` at ~1 s
  (producer) with a slow mirror baseline; pressures (LL/OC/PREP) come via the
  forwarder; `/metrics` `last_seen` advances.
- **Rollback:** stop `ls350_fast_aws.py` (lab untouched) and/or
  `docker compose stop forwarder`. The InfluxDB/Grafana path is never modified.

## 7. The `PC` gauge

The vacuum controller returns **three** values → `LL`, `OC`, `PREP` (the
`Pressure_Logger`). The dashboard also has a `PC` channel, but **no `PC` is logged
by this gauge**, so `PC` stays empty on AWS until its source is identified.
