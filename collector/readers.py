"""Pluggable instrument read path.

The collector is deliberately agnostic about *where* readings come from:

  * MockReader (default) drives the high-fidelity Simulator, so the whole
    pipeline can be developed and load-tested on a laptop with no hardware.
  * RealReader is the template for the lab PC: it reuses the existing PyVISA
    instrument queries. We do NOT re-implement GPIB/VISA I/O — that already works
    in the lab's Python; this just maps each channel read to a reading dict. See
    WIRING.md for how the real lab script adopts this.

A Reader returns a list of {"metric", "ts", "value"} dicts; the collector adds
the authenticated source and handles batching, buffering, and delivery.
"""

from __future__ import annotations

import time
from abc import ABC, abstractmethod
from datetime import datetime, timezone

from signals import Simulator


def iso_now(now: float | None = None) -> str:
    """RFC3339/UTC timestamp with millisecond precision (Go's parser accepts it)."""
    now = now if now is not None else time.time()
    return datetime.fromtimestamp(now, tz=timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.%f")[:-3] + "Z"


class Reader(ABC):
    @abstractmethod
    def read(self) -> list[dict]:
        """Sample every channel once; return reading dicts stamped with ts=now."""
        raise NotImplementedError


class MockReader(Reader):
    """Generates realistic multi-channel data from the Simulator."""

    def __init__(self, seed: int | None = None) -> None:
        self._sim = Simulator(seed=seed)

    def read(self) -> list[dict]:
        now = time.time()
        ts = iso_now(now)
        return [
            {"metric": metric, "ts": ts, "value": value}
            for metric, value in self._sim.sample(now).items()
        ]


class RealReader(Reader):
    """Template that reuses the lab's existing PyVISA instrument code.

    `channels` maps our metric name -> a callable returning a float, so the lab
    can drop in its existing query functions unchanged. pyvisa is imported lazily
    so the collector runs in mock mode without it installed.

    Example wiring on the lab PC (pseudocode, see WIRING.md):

        rm = pyvisa.ResourceManager()
        gauge = rm.open_resource("GPIB0::3::INSTR")
        cryo  = rm.open_resource("GPIB0::5::INSTR")
        reader = RealReader({
            "OC":   lambda: float(gauge.query("PR1?")),
            "STM":  lambda: float(cryo.query("KRDG? A")),
            # ... the rest of the existing read calls ...
        })
    """

    def __init__(self, channels: dict[str, "callable[[], float]"]) -> None:
        self._channels = channels

    def read(self) -> list[dict]:
        ts = iso_now()
        out: list[dict] = []
        for metric, query in self._channels.items():
            try:
                out.append({"metric": metric, "ts": ts, "value": float(query())})
            except Exception:
                # A flaky single-channel read must not drop the whole batch; the
                # other channels still go through. (The lab can log here.)
                continue
        return out
