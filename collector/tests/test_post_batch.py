import threading
from types import SimpleNamespace

import requests

from collector import OK, PERMANENT, TRANSIENT, post_batch


def settings():
    # Tiny backoff so retry tests run instantly.
    return SimpleNamespace(
        ingest_endpoint="http://example/ingest",
        token="t",
        source="s",
        http_timeout=1,
        max_attempts=4,
        backoff_base=0.001,
        backoff_cap=0.004,
    )


class FakeResp:
    def __init__(self, code, text=""):
        self.status_code = code
        self.text = text


class FakeSession:
    """Returns/raises a scripted sequence of behaviours, counting calls."""

    def __init__(self, behaviours):
        self.behaviours = list(behaviours)
        self.calls = 0

    def post(self, url, json=None, headers=None, timeout=None):
        b = self.behaviours[min(self.calls, len(self.behaviours) - 1)]
        self.calls += 1
        if isinstance(b, Exception):
            raise b
        return b


def test_ok_on_first_try():
    s = FakeSession([FakeResp(200)])
    assert post_batch(s, settings(), [{"metric": "x", "value": 1}], threading.Event()) == OK
    assert s.calls == 1


def test_4xx_is_permanent_and_not_retried():
    s = FakeSession([FakeResp(400, "bad request")])
    assert post_batch(s, settings(), [{"metric": "x", "value": 1}], threading.Event()) == PERMANENT
    assert s.calls == 1  # not retried — would block the queue forever


def test_5xx_then_success_retries():
    s = FakeSession([FakeResp(503), FakeResp(503), FakeResp(200)])
    assert post_batch(s, settings(), [{"metric": "x", "value": 1}], threading.Event()) == OK
    assert s.calls == 3


def test_connection_error_exhausts_attempts_as_transient():
    s = FakeSession([requests.ConnectionError("down")])
    assert post_batch(s, settings(), [{"metric": "x", "value": 1}], threading.Event()) == TRANSIENT
    assert s.calls == 4  # max_attempts, then give up (kept in buffer for next sweep)


def test_429_is_transient():
    s = FakeSession([FakeResp(429), FakeResp(200)])
    assert post_batch(s, settings(), [{"metric": "x", "value": 1}], threading.Event()) == OK
    assert s.calls == 2
