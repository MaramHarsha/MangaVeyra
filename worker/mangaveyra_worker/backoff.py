"""Exponential backoff with jitter."""

from __future__ import annotations

import random
import threading
from dataclasses import dataclass, field


@dataclass
class Backoff:
    """Exponential backoff: ``base * factor**n`` capped at ``cap`` with proportional jitter.

    ``jitter`` is the fraction of the delay that is randomised downwards ("equal jitter" when 0.5), which keeps
    many workers that lost the same server from reconnecting in lockstep.
    """

    base: float = 0.5
    cap: float = 60.0
    factor: float = 2.0
    jitter: float = 0.5
    rng: random.Random = field(default_factory=random.Random, repr=False)
    attempt: int = 0

    def peek(self) -> float:
        """Return the un-jittered delay the next call to :meth:`next` is based on."""
        return min(self.cap, self.base * (self.factor**self.attempt))

    def next(self) -> float:
        """Return the next delay in seconds and advance the attempt counter."""
        delay = self.peek()
        self.attempt += 1
        if self.jitter:
            delay *= 1.0 - self.jitter * self.rng.random()
        return max(0.0, delay)

    def reset(self) -> None:
        """Start over after a successful request."""
        self.attempt = 0

    def sleep(self, stop: threading.Event | None = None) -> bool:
        """Sleep for the next delay. Returns ``True`` if ``stop`` was set while waiting."""
        delay = self.next()
        if stop is None:
            threading.Event().wait(delay)
            return False
        return stop.wait(delay)
