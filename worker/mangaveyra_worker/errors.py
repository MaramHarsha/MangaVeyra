"""Exception types shared by the protocol client, the runner and the backends.

Backends raise :class:`JobFailure` subclasses to report a failure with a protocol error code; the runner maps
them onto ``POST /jobs/{id}/fail``. Any other exception is reported as ``internal`` (retryable).
"""

from __future__ import annotations


class WorkerError(Exception):
    """Base class of every error raised by this package."""


class ConfigError(WorkerError):
    """Invalid or incomplete worker configuration."""


class JobFailure(WorkerError):
    """A job failed with one of the protocol error codes.

    ``code`` is one of ``oom``, ``timeout``, ``safety``, ``bad_input``, ``canceled`` or ``internal``;
    ``retryable`` tells the server whether retrying (or falling back to another model) makes sense.
    """

    code = "internal"
    retryable = True

    def __init__(self, message: str, *, retryable: bool | None = None, code: str | None = None) -> None:
        super().__init__(message)
        self.message = message
        if retryable is not None:
            self.retryable = retryable
        if code is not None:
            self.code = code


class BadInput(JobFailure):
    """The job input or its files are invalid; retrying the same job cannot succeed."""

    code = "bad_input"
    retryable = False


class JobCanceled(JobFailure):
    """The job was canceled (by the server, a user, or a worker shutdown)."""

    code = "canceled"
    retryable = False


class JobTimeout(JobFailure):
    """The job exceeded a time limit (for example a ComfyUI prompt that never finished)."""

    code = "timeout"
    retryable = True


class OutOfMemory(JobFailure):
    """The accelerator (or host) ran out of memory."""

    code = "oom"
    retryable = True


class SafetyBlocked(JobFailure):
    """A safety checker refused the output."""

    code = "safety"
    retryable = False


class BackendUnavailable(WorkerError):
    """A backend cannot run on this machine (missing optional dependency, no server, ...)."""


class ApiError(WorkerError):
    """The MangaVeyra server answered with an error status.

    ``status`` is the HTTP status, ``code`` and ``message`` come from the ``{"error": {...}}`` body when present.
    """

    def __init__(self, status: int, code: str, message: str) -> None:
        super().__init__(f"HTTP {status} {code}: {message}")
        self.status = status
        self.code = code
        self.message = message

    @property
    def retryable(self) -> bool:
        """Whether the request may succeed when repeated (server errors and rate limits)."""
        return self.status >= 500 or self.status in (408, 425, 429)


class Unauthorized(ApiError):
    """The worker token was rejected (revoked, wrong server, or missing)."""


class UnknownWorker(ApiError):
    """The server no longer knows this worker id (restart or offline timeout); re-register."""


class LeaseLost(ApiError):
    """The job is no longer leased to this worker (``409 lease_lost``, or the job is gone / owned by another
    worker). The server has already re-queued, failed or finished it: drop the job without reporting it."""


class Aborted(WorkerError):
    """A retry loop was interrupted because the worker is shutting down."""
