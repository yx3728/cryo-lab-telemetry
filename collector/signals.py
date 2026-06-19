"""High-fidelity simulator of the Unisoku STM's vacuum and cryogenic channels.

This is the single source of truth for "what the data looks like". The
collector's MockReader uses it for local development, and the load/chaos harness
in ../mock reuses it, so the dashboard looks like the real instrument long before
the real instrument is connected.

Channels (confirmed against the lab's public Grafana dashboard):

  Vacuum pressure (Torr, log scale, ~1e-9 baseline, rare spikes to ~1e-7):
    LL   load lock      - frequently OFF / no signal
    PC   prep chamber   - ~1.3e-9
    OC   outer chamber  - ~2.3e-9
    PREP                - ~1e-9

  Temperature (Kelvin, ~2-28 K):
    SORB     ~8 K, slow sawtooth (sorb-pump regen ramps to ~16-20 K then drops)
    1K Pot   ~4.4 K sawtooth
    He3 Pot  ~4.2 K
    STM      ~4.2 K baseline, occasional spikes to ~25 K during events

Pure standard library (math/random/time) so the collector stays a tiny,
dependency-light artifact that can run anywhere Python does.
"""

from __future__ import annotations

import math
import random
import time
from dataclasses import dataclass


@dataclass(frozen=True)
class ChannelSpec:
    """Static description of one channel, used by the UI for axes/grouping."""

    name: str
    unit: str
    group: str  # "pressure" or "temperature"


# The canonical channel set, in display order.
CHANNELS: list[ChannelSpec] = [
    ChannelSpec("LL", "Torr", "pressure"),
    ChannelSpec("PC", "Torr", "pressure"),
    ChannelSpec("OC", "Torr", "pressure"),
    ChannelSpec("PREP", "Torr", "pressure"),
    ChannelSpec("SORB", "K", "temperature"),
    ChannelSpec("1K Pot", "K", "temperature"),
    ChannelSpec("He3 Pot", "K", "temperature"),
    ChannelSpec("STM", "K", "temperature"),
]


class _Spike:
    """An exponentially-decaying transient added on top of a channel's baseline."""

    __slots__ = ("t0", "peak", "tau")

    def __init__(self, t0: float, peak: float, tau: float) -> None:
        self.t0 = t0
        self.peak = peak
        self.tau = tau

    def value(self, t: float) -> float:
        return self.peak * math.exp(-(t - self.t0) / self.tau)

    def expired(self, t: float) -> bool:
        # Done once the contribution has decayed to ~1% of the peak.
        return (t - self.t0) > 5 * self.tau


class Simulator:
    """Stateful generator producing one reading per channel per call to sample().

    State (active spikes, the LL on/off flag) persists between calls so transients
    decay smoothly across the few-second sampling cadence. Pass a seed for
    reproducible runs (used by the tests).
    """

    def __init__(self, seed: int | None = None, start: float | None = None) -> None:
        self._rng = random.Random(seed)
        self._start = start if start is not None else time.time()
        self._spikes: dict[str, _Spike] = {}
        self._ll_on = False

    def sample(self, now: float | None = None) -> dict[str, float]:
        """Return {metric: value} for the current moment.

        LL is omitted when the gauge is "off", exactly as the real dashboard shows
        a frequently-blank load-lock channel.
        """
        now = now if now is not None else time.time()
        t = now - self._start

        out: dict[str, float] = {
            "PC": self._pressure(t, "PC", 1.3e-9),
            "OC": self._pressure(t, "OC", 2.3e-9),
            "PREP": self._pressure(t, "PREP", 1.0e-9),
            "SORB": self._sawtooth(t, base=8.0, amp=10.0, period=1200.0, noise=0.2),
            "1K Pot": self._sawtooth(t, base=4.4, amp=0.6, period=300.0, noise=0.05),
            "He3 Pot": 4.2 + 0.1 * math.sin(t / 180.0) + self._rng.uniform(-0.03, 0.03),
            "STM": self._stm(t),
        }

        ll = self._load_lock(t)
        if ll is not None:
            out["LL"] = ll
        return out

    # --- channel models ------------------------------------------------------

    def _pressure(self, t: float, metric: str, base: float) -> float:
        # Multiplicative jitter around the baseline, plus a rare decaying spike
        # toward ~1e-7 Torr (a brief vacuum excursion).
        val = base * (1.0 + self._rng.uniform(-0.05, 0.05))
        val += self._spike(metric, t, trigger_prob=0.004, peak=1.0e-7, tau=25.0)
        return val

    def _sawtooth(self, t: float, base: float, amp: float, period: float, noise: float) -> float:
        # Linear ramp that resets each period — the cryostat regen cycle shape.
        phase = (t % period) / period
        return base + amp * phase + self._rng.uniform(-noise, noise)

    def _stm(self, t: float) -> float:
        # 4.2 K baseline with small noise and occasional large spikes (~+21 K, so
        # peaks near 25 K) representing thermal events.
        base = 4.2 + self._rng.uniform(-0.05, 0.05)
        return base + self._spike("STM", t, trigger_prob=0.006, peak=21.0, tau=20.0)

    def _load_lock(self, t: float) -> float | None:
        # Mostly off; flips on/off with small per-sample probabilities. Reads
        # ~1e-9 Torr when on.
        if self._ll_on:
            if self._rng.random() < 0.10:
                self._ll_on = False
                return None
            return 1.0e-9 * (1.0 + self._rng.uniform(-0.05, 0.05))
        if self._rng.random() < 0.02:
            self._ll_on = True
            return 1.0e-9
        return None

    def _spike(self, metric: str, t: float, trigger_prob: float, peak: float, tau: float) -> float:
        """Manage a single decaying spike per metric; return its contribution."""
        spike = self._spikes.get(metric)
        if spike is not None:
            if spike.expired(t):
                del self._spikes[metric]
                spike = None
            else:
                return spike.value(t)
        if spike is None and self._rng.random() < trigger_prob:
            self._spikes[metric] = _Spike(t0=t, peak=peak, tau=tau)
            return peak
        return 0.0
