from datetime import datetime

from readers import MockReader, iso_now
from signals import CHANNELS


def test_mockreader_returns_known_channels():
    readings = MockReader(seed=1).read()
    metrics = {r["metric"] for r in readings}
    known = {c.name for c in CHANNELS}
    assert metrics.issubset(known)
    # The always-on channels must appear every sample (LL may be off).
    for m in ["PC", "OC", "PREP", "SORB", "1K Pot", "He3 Pot", "STM"]:
        assert m in metrics
    for r in readings:
        assert isinstance(r["value"], float)
        assert r["ts"].endswith("Z")


def test_iso_now_is_rfc3339_utc():
    s = iso_now(0.0)
    assert s == "1970-01-01T00:00:00.000Z"
    # Parseable back to a datetime.
    datetime.fromisoformat(s.replace("Z", "+00:00"))
