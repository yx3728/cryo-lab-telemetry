"""Lab-monitor edge collector — the real artifact that runs on the instrument PC.

Design: acquisition is decoupled from delivery through the durable on-disk queue
(buffer.py). Three small threads coordinate only via that queue and a shared
sampling interval:

  acquisition  read every channel each sampling interval -> journal to disk
  sender       drain the queue oldest-first, with retry + exponential backoff;
               on a transient failure it stops (preserving order) and backs off,
               so an outage simply makes the queue grow and then drain in order
  config       poll GET /api/config and adopt the admin-set sampling interval
               (the closed control loop)

Because every batch is journaled before any send, and the sender never skips a
batch it could not deliver, the collector loses no data across drops or restarts
and delivers strictly in order. The server's idempotent ingest means replaying
the queue after a crash never double-writes.

Configuration is via environment variables (see --help / the constants below).
Runs headless; Ctrl-C / SIGTERM shuts down cleanly.
"""

from __future__ import annotations

import logging
import os
import signal
import threading
import time

import requests

from buffer import DiskBuffer
from readers import MockReader, Reader

log = logging.getLogger("collector")


# --- configuration -----------------------------------------------------------


class Settings:
    """All collector configuration, read from the environment."""

    def __init__(self) -> None:
        base = os.getenv("INGEST_URL", "http://localhost:8080").rstrip("/")
        self.ingest_endpoint = base + "/ingest"
        self.config_endpoint = base + "/api/config"
        self.token = os.getenv("INGEST_TOKEN", "dev-ingest-token")
        self.source = os.getenv("SOURCE", "unisoku-stm")
        self.reader_kind = os.getenv("READER", "mock").lower()
        self.buffer_dir = os.getenv("BUFFER_DIR", os.path.join(os.path.dirname(__file__), "buffer"))
        # Bounded buffer (backpressure): 0 = unbounded; >0 caps queued batches,
        # dropping oldest on overflow so a long outage can't exhaust the disk.
        self.buffer_max_batches = int(os.getenv("BUFFER_MAX_BATCHES", "0"))
        self.sample_interval = float(os.getenv("SAMPLE_INTERVAL_SECONDS", "5"))
        self.config_poll_seconds = float(os.getenv("CONFIG_POLL_SECONDS", "15"))
        self.http_timeout = float(os.getenv("HTTP_TIMEOUT_SECONDS", "10"))
        # Retry/backoff for a single batch within one send attempt.
        self.max_attempts = int(os.getenv("SEND_MAX_ATTEMPTS", "4"))
        self.backoff_base = float(os.getenv("SEND_BACKOFF_BASE", "0.5"))
        self.backoff_cap = float(os.getenv("SEND_BACKOFF_CAP", "8"))
        self.seed = int(os.getenv("MOCK_SEED")) if os.getenv("MOCK_SEED") else None


class State:
    """Thread-safe live sampling interval (mutated by the config poller)."""

    def __init__(self, interval: float) -> None:
        self._lock = threading.Lock()
        self._interval = interval

    @property
    def interval(self) -> float:
        with self._lock:
            return self._interval

    @interval.setter
    def interval(self, value: float) -> None:
        with self._lock:
            self._interval = value


# --- delivery ----------------------------------------------------------------

# Result of a send attempt.
OK = "ok"
TRANSIENT = "transient"  # retry later, keep in buffer
PERMANENT = "permanent"  # bad request; drop so it can't block the queue forever


def post_batch(session: requests.Session, settings: Settings, batch: list[dict],
               stop: threading.Event) -> str:
    """POST one batch with retry + exponential backoff. Returns OK/TRANSIENT/PERMANENT."""
    headers = {"X-Api-Key": settings.token, "Content-Type": "application/json"}
    body = [{"source": settings.source, **r} for r in batch]

    delay = settings.backoff_base
    for attempt in range(1, settings.max_attempts + 1):
        try:
            resp = session.post(settings.ingest_endpoint, json=body,
                                headers=headers, timeout=settings.http_timeout)
            if resp.status_code < 300:
                return OK
            # 4xx (except 408 timeout / 429 too-many) means the request itself is
            # bad; retrying won't help, so don't let it block the queue.
            if 400 <= resp.status_code < 500 and resp.status_code not in (408, 429):
                log.error("permanent ingest error %s: %s", resp.status_code, resp.text[:200])
                return PERMANENT
            log.warning("transient ingest status %s (attempt %d/%d)",
                        resp.status_code, attempt, settings.max_attempts)
        except requests.RequestException as exc:
            log.warning("ingest connection error (attempt %d/%d): %s",
                        attempt, settings.max_attempts, exc)

        if attempt < settings.max_attempts:
            if stop.wait(delay):  # interruptible sleep
                return TRANSIENT
            delay = min(delay * 2, settings.backoff_cap)
    return TRANSIENT


# --- threads -----------------------------------------------------------------


def acquisition_loop(reader: Reader, buf: DiskBuffer, state: State,
                     stop: threading.Event, stats: dict) -> None:
    while not stop.is_set():
        start = time.time()
        readings = reader.read()
        if readings:
            buf.enqueue(readings)  # write-ahead: on disk before any send
            stats["read"] += len(readings)
        elapsed = time.time() - start
        stop.wait(max(0.0, state.interval - elapsed))


def sender_loop(buf: DiskBuffer, session: requests.Session, settings: Settings,
                stop: threading.Event, stats: dict) -> None:
    buffering = False  # track state transitions just for clear logs
    while not stop.is_set():
        pending = buf.pending()
        if not pending:
            stop.wait(0.2)
            continue
        progressed = False
        for path in pending:
            if stop.is_set():
                break
            batch = buf.load(path)
            result = post_batch(session, settings, batch, stop)
            if result == OK:
                buf.remove(path)
                stats["sent"] += len(batch)
                progressed = True
                if buffering:
                    log.info("recovered: draining buffer (%d batches left)", buf.count())
                    buffering = False
            elif result == PERMANENT:
                buf.remove(path)  # drop poison batch, keep going
                stats["dropped"] += len(batch)
                progressed = True
            else:  # TRANSIENT: stop to preserve order, back off, retry next sweep
                if not buffering:
                    log.warning("delivery failing; buffering to disk (%d batches queued)", buf.count())
                    buffering = True
                break
        if not progressed:
            stop.wait(2.0)


def config_loop(session: requests.Session, settings: Settings, state: State,
                stop: threading.Event) -> None:
    while not stop.is_set():
        try:
            resp = session.get(settings.config_endpoint, timeout=settings.http_timeout)
            if resp.ok:
                new_interval = float(resp.json().get("sampling_interval_seconds", state.interval))
                if new_interval > 0 and new_interval != state.interval:
                    log.info("control loop: sampling interval %.0fs -> %.0fs",
                             state.interval, new_interval)
                    state.interval = new_interval
        except (requests.RequestException, ValueError) as exc:
            log.debug("config poll failed: %s", exc)
        stop.wait(settings.config_poll_seconds)


def status_loop(buf: DiskBuffer, stats: dict, stop: threading.Event) -> None:
    while not stop.wait(30.0):
        log.info("status: read=%d sent=%d failed_perm=%d queued=%d buffer_dropped=%d",
                 stats["read"], stats["sent"], stats["dropped"], buf.count(), buf.dropped)


# --- entrypoint --------------------------------------------------------------


def build_reader(settings: Settings) -> Reader:
    if settings.reader_kind == "real":
        # The lab PC supplies its existing PyVISA query callables here; see
        # readers.RealReader and WIRING.md. Mock is the default for development.
        raise SystemExit("READER=real requires wiring the lab's PyVISA queries; see WIRING.md")
    return MockReader(seed=settings.seed)


def main() -> None:
    logging.basicConfig(level=logging.INFO,
                        format="%(asctime)s %(levelname)s %(name)s %(message)s")
    settings = Settings()
    state = State(settings.sample_interval)
    buf = DiskBuffer(settings.buffer_dir, max_batches=settings.buffer_max_batches)
    reader = build_reader(settings)
    session = requests.Session()
    stop = threading.Event()
    stats = {"read": 0, "sent": 0, "dropped": 0}

    # Fetch the server's current sampling interval before we start sampling, so
    # we begin already aligned with the admin's configuration.
    try:
        resp = session.get(settings.config_endpoint, timeout=settings.http_timeout)
        if resp.ok:
            state.interval = float(resp.json().get("sampling_interval_seconds", state.interval))
    except (requests.RequestException, ValueError):
        pass

    log.info("collector starting: source=%s endpoint=%s interval=%.0fs reader=%s buffer=%s (queued=%d)",
             settings.source, settings.ingest_endpoint, state.interval,
             settings.reader_kind, settings.buffer_dir, buf.count())

    def handle_signal(_signum, _frame):
        log.info("shutdown signal received")
        stop.set()

    signal.signal(signal.SIGINT, handle_signal)
    signal.signal(signal.SIGTERM, handle_signal)

    threads = [
        threading.Thread(target=acquisition_loop, args=(reader, buf, state, stop, stats), name="acq"),
        threading.Thread(target=sender_loop, args=(buf, session, settings, stop, stats), name="send"),
        threading.Thread(target=config_loop, args=(session, settings, state, stop), name="config"),
        threading.Thread(target=status_loop, args=(buf, stats, stop), name="status"),
    ]
    for t in threads:
        t.start()
    # Keep the main thread alive until a signal sets the stop event.
    while not stop.wait(0.5):
        pass
    for t in threads:
        t.join(timeout=5)
    log.info("collector stopped: read=%d sent=%d dropped=%d queued=%d",
             stats["read"], stats["sent"], stats["dropped"], buf.count())


if __name__ == "__main__":
    main()
