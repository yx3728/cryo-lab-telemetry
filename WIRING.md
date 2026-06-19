# WIRING.md — pointing the real lab at the deployed system

This is the **last step**, and the only one that touches real instruments. Up to
here, everything has been built and tested against mocks. Wiring the real lab is a
**small change to the existing Python** on the instrument PC — the script that
already reads the gauges/cryostat via PyVISA and uploads to Grafana. We reuse that
instrument I/O; we do **not** rewrite it.

Nothing new is needed on the Windows PC: just **Python + `requests` + the ingest
token**. No Go, no Docker, no Node.

---

## What changes

The existing script already does, every few seconds:

```python
# (existing, unchanged) read the instruments via PyVISA
oc   = float(gauge.query("PR1?"))
stm  = float(cryo.query("KRDG? A"))
sorb = float(cryo.query("KRDG? B"))
# ... etc, then: upload to Grafana
```

We add one function that POSTs the same readings to our ingest endpoint, and call
it instead of (or alongside) the Grafana upload.

### Endpoint and auth

- **URL:** `https://3.220.132.187.sslip.io/ingest`
- **Header:** `X-Api-Key: <INGEST_TOKEN>` — the token bound to source
  `unisoku-stm`. It lives in `deploy/.env` on the EC2 box (key `INGEST_TOKEN`);
  the admin copies it to the lab PC's environment. **Never commit it.**
- **Body:** a JSON array of `{source, metric, ts, value}`. `ts` is RFC3339/UTC.
- The endpoint is **idempotent**, so it is safe to retry.

### Use these exact metric names

So the readings land on the right charts:

`LL`, `PC`, `OC`, `PREP` (vacuum, Torr) and
`SORB`, `1K Pot`, `He3 Pot`, `STM` (temperature, Kelvin).

---

## Option A — minimal change (drop-in function)

Add this to the existing script and call `post_readings({...})` once per sample
with the values it already computed:

```python
import os, time, requests
from datetime import datetime, timezone

INGEST_URL = "https://3.220.132.187.sslip.io/ingest"
TOKEN      = os.environ["LAB_MONITOR_TOKEN"]   # set this in the PC's environment
SOURCE     = "unisoku-stm"

def post_readings(values: dict[str, float]) -> None:
    """values maps metric name -> reading, e.g. {'OC': 2.3e-9, 'STM': 4.2}."""
    ts = datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.%f")[:-3] + "Z"
    batch = [{"source": SOURCE, "metric": m, "ts": ts, "value": float(v)}
             for m, v in values.items()]
    try:
        r = requests.post(INGEST_URL, json=batch,
                          headers={"X-Api-Key": TOKEN}, timeout=10)
        r.raise_for_status()
    except requests.RequestException as e:
        # Don't let a network blip stop acquisition; log and move on. (For
        # guaranteed no-loss delivery across outages, use Option B.)
        print("ingest post failed:", e)

# In the existing loop, after reading the instruments:
post_readings({"OC": oc, "STM": stm, "SORB": sorb, "PREP": prep,
               "PC": pc, "He3 Pot": he3, "1K Pot": k1pot})  # add LL when on
```

That's the whole change. Run the script as before; the dashboard at
`https://3.220.132.187.sslip.io` now shows **real** data and `/metrics` shows
**real** uptime.

---

## Option B — reuse the collector (recommended: retry + offline buffer + remote config)

The repo's `collector/` already implements batching, **retry with backoff**, a
**no-loss offline disk buffer**, and **remote sampling-interval control**. To get
all of that on the lab PC, copy `collector/` there and supply a `RealReader` that
wraps the existing PyVISA queries (see `collector/readers.py`):

```python
# lab_main.py on the instrument PC
import pyvisa
from readers import RealReader
import collector

rm    = pyvisa.ResourceManager()
gauge = rm.open_resource("GPIB0::3::INSTR")
cryo  = rm.open_resource("GPIB0::5::INSTR")

# Map our metric names to the lab's EXISTING query calls (reuse, don't rewrite):
reader = RealReader({
    "OC":      lambda: float(gauge.query("PR1?")),
    "PC":      lambda: float(gauge.query("PR2?")),
    "PREP":    lambda: float(gauge.query("PR3?")),
    "STM":     lambda: float(cryo.query("KRDG? A")),
    "SORB":    lambda: float(cryo.query("KRDG? B")),
    "1K Pot":  lambda: float(cryo.query("KRDG? C")),
    "He3 Pot": lambda: float(cryo.query("KRDG? D")),
    # "LL": ...  # add when the load lock gauge is on
})

# Point the collector at the reader and run it (it reads env for URL/token):
collector.build_reader = lambda settings: reader   # inject the real reader
collector.main()
```

Run with environment:

```
set INGEST_URL=https://3.220.132.187.sslip.io
set INGEST_TOKEN=<the unisoku-stm token from deploy/.env>
set SOURCE=unisoku-stm
set READER=real
python lab_main.py
```

Now the lab PC gets reliable delivery (survives Wi-Fi drops without losing data),
and the admin can change the sampling interval from the dashboard and the lab PC
will adopt it.

---

## After wiring

- The public dashboard shows live, real cryostat/vacuum data.
- `/metrics` reflects genuine uptime and per-source last-seen.
- Alert thresholds (set in the admin panel) fire on real excursions.

The "deployed on AWS, used by a research group" story is now true end-to-end.

## Rolling back

Remove the `post_readings(...)` call (Option A) or stop `lab_main.py` (Option B).
The original Grafana upload is untouched, so the lab can run both in parallel
during the transition and cut over only when confident.
