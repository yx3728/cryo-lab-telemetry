from buffer import DiskBuffer


def test_enqueue_preserves_order(tmp_path):
    b = DiskBuffer(tmp_path)
    for v in (1, 2, 3):
        b.enqueue([{"metric": "a", "value": v}])
    pending = b.pending()
    assert len(pending) == 3
    values = [DiskBuffer.load(p)[0]["value"] for p in pending]
    assert values == [1, 2, 3]  # oldest-first
    assert b.count() == 3


def test_pending_excludes_partial_writes(tmp_path):
    b = DiskBuffer(tmp_path)
    b.enqueue([{"x": 1}])
    # Only fully-written .json files are listed; no .tmp ever leaks into pending.
    assert all(p.suffix == ".json" for p in b.pending())


def test_counter_resumes_after_restart(tmp_path):
    first = DiskBuffer(tmp_path)
    first.enqueue([{"v": 1}])
    first.enqueue([{"v": 2}])

    # Simulate a process restart: a fresh buffer over the same directory must
    # number new batches AFTER the ones already on disk (no reordering, no clobber).
    second = DiskBuffer(tmp_path)
    second.enqueue([{"v": 3}])

    values = [DiskBuffer.load(p)[0]["v"] for p in second.pending()]
    assert values == [1, 2, 3]


def test_remove(tmp_path):
    b = DiskBuffer(tmp_path)
    path = b.enqueue([{"v": 1}])
    DiskBuffer.remove(path)
    assert b.count() == 0
    # Removing a missing file is a no-op, not an error.
    DiskBuffer.remove(path)
