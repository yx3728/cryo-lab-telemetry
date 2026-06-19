"""A fault-injecting HTTP proxy that sits between the load producers and the real
Go ingest, so the load/chaos test exercises the genuine ingest + DB path while we
control the network weather.

Fault modes (all honest, all toggleable):
  latency       add up to latency_ms of delay before forwarding
  drop          refuse a fraction of requests with 503 WITHOUT forwarding
  ambiguous     forward to upstream (so it IS written) but then hide the success
                behind a 503 — this is the nasty case that forces the client to
                retry an already-applied write, and is exactly what idempotent
                ingest must absorb without creating duplicates
  outage        while .outage is True, refuse everything without forwarding
                (a hard disconnect window)

Pure standard library so the harness has no extra dependencies.
"""

from __future__ import annotations

import json
import random
import threading
import time
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


class ChaosProxy:
    def __init__(self, upstream: str, host: str = "127.0.0.1", port: int = 8099,
                 seed: int = 0, latency_ms: float = 0.0,
                 drop_rate: float = 0.0, ambiguous_rate: float = 0.0) -> None:
        self.upstream = upstream.rstrip("/")
        self.host = host
        self.port = port
        self.latency_ms = latency_ms
        self.drop_rate = drop_rate
        self.ambiguous_rate = ambiguous_rate
        self.outage = False
        self.stats = {"forwarded": 0, "dropped": 0, "ambiguous": 0, "outage_blocked": 0}
        self._rng = random.Random(seed)
        self._rng_lock = threading.Lock()
        self._server: ThreadingHTTPServer | None = None

    def url(self) -> str:
        return f"http://{self.host}:{self.port}"

    def _rand(self) -> float:
        with self._rng_lock:
            return self._rng.random()

    def start(self) -> None:
        proxy = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *_args):  # silence default logging
                pass

            def handle_one(self):
                length = int(self.headers.get("Content-Length", "0"))
                body = self.rfile.read(length) if length else b""

                if proxy.latency_ms:
                    time.sleep(proxy._rand() * proxy.latency_ms / 1000.0)

                if proxy.outage:
                    proxy.stats["outage_blocked"] += 1
                    return self.reply(503, b'{"error":"chaos outage"}')

                if proxy._rand() < proxy.drop_rate:
                    proxy.stats["dropped"] += 1
                    return self.reply(503, b'{"error":"chaos drop"}')

                code, resp = proxy.forward(self.command, self.path, self.headers, body)

                # Ambiguous failure: upstream already applied the write, but we
                # report failure so the client retries an applied request.
                if self.command == "POST" and proxy._rand() < proxy.ambiguous_rate:
                    proxy.stats["ambiguous"] += 1
                    return self.reply(503, b'{"error":"chaos ambiguous"}')

                proxy.stats["forwarded"] += 1
                self.reply(code, resp)

            def reply(self, code: int, body: bytes):
                self.send_response(code)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            do_POST = handle_one
            do_GET = handle_one

        self._server = ThreadingHTTPServer((self.host, self.port), Handler)
        threading.Thread(target=self._server.serve_forever, daemon=True).start()

    def forward(self, method: str, path: str, headers, body: bytes):
        fwd = {k: v for k, v in headers.items()
               if k.lower() in ("content-type", "x-api-key", "authorization")}
        req = urllib.request.Request(
            self.upstream + path,
            data=body if method == "POST" else None,
            headers=fwd, method=method,
        )
        try:
            with urllib.request.urlopen(req, timeout=10) as r:
                return r.status, r.read()
        except urllib.error.HTTPError as e:
            return e.code, e.read()
        except Exception as e:  # upstream unreachable
            return 502, json.dumps({"error": str(e)}).encode()

    def stop(self) -> None:
        if self._server:
            self._server.shutdown()
