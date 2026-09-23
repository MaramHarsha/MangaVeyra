"""Exponential backoff."""

from __future__ import annotations

import random
import threading

from mangaveyra_worker.backoff import Backoff


def test_grows_exponentially_and_caps() -> None:
    b = Backoff(base=1.0, cap=10.0, factor=2.0, jitter=0.0)
    assert [b.next() for _ in range(6)] == [1.0, 2.0, 4.0, 8.0, 10.0, 10.0]


def test_jitter_stays_within_bounds() -> None:
    b = Backoff(base=4.0, cap=4.0, jitter=0.5, rng=random.Random(1))
    delays = [b.next() for _ in range(200)]
    assert all(2.0 <= d <= 4.0 for d in delays)
    assert len({round(d, 3) for d in delays}) > 50  # actually randomised


def test_reset() -> None:
    b = Backoff(base=1.0, jitter=0.0)
    b.next()
    b.next()
    b.reset()
    assert b.next() == 1.0


def test_sleep_is_interruptible() -> None:
    stop = threading.Event()
    stop.set()
    assert Backoff(base=30.0, jitter=0.0).sleep(stop) is True
