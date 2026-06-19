"""A durable, ordered, crash-safe on-disk buffer (a tiny write-ahead queue).

The collector journals every batch to disk *before* trying to send it. Delivery
then drains the buffer strictly oldest-first. Two properties fall out of this:

  * No data loss across network drops or crashes — a batch is on disk before any
    send is attempted, so a process restart resumes exactly where it left off.
  * Correct ordering — each batch is a separate file named with a zero-padded
    monotonic index, so lexical filename order == chronological order, and the
    sender never skips ahead of a batch it could not deliver.

Each batch lives in its own file (written to a .tmp then atomically renamed), so
a crash mid-write can never produce a half-readable batch.
"""

from __future__ import annotations

import json
import os
import threading
from pathlib import Path


class DiskBuffer:
    def __init__(self, directory: str | os.PathLike) -> None:
        self.dir = Path(directory)
        self.dir.mkdir(parents=True, exist_ok=True)
        self._lock = threading.Lock()
        self._counter = self._init_counter()

    def _init_counter(self) -> int:
        """Resume numbering after the highest index already on disk."""
        max_idx = -1
        for p in self.dir.glob("*.json"):
            try:
                max_idx = max(max_idx, int(p.stem))
            except ValueError:
                continue
        return max_idx + 1

    def enqueue(self, batch: list[dict]) -> Path:
        """Persist one batch atomically and return its path."""
        with self._lock:
            idx = self._counter
            self._counter += 1
        name = f"{idx:012d}.json"
        tmp = self.dir / (name + ".tmp")
        final = self.dir / name
        tmp.write_text(json.dumps(batch))
        os.replace(tmp, final)  # atomic on POSIX and Windows
        return final

    def pending(self) -> list[Path]:
        """All buffered batches, oldest first."""
        return sorted(self.dir.glob("*.json"))

    def count(self) -> int:
        return sum(1 for _ in self.dir.glob("*.json"))

    @staticmethod
    def load(path: Path) -> list[dict]:
        return json.loads(Path(path).read_text())

    @staticmethod
    def remove(path: Path) -> None:
        try:
            Path(path).unlink()
        except FileNotFoundError:
            pass
