"""The backend interface every job executor implements."""

from __future__ import annotations

import abc
from typing import TYPE_CHECKING, Any, ClassVar

from mangaveyra_worker.jobs import Job, JobContext, JobResult

if TYPE_CHECKING:  # pragma: no cover
    from mangaveyra_worker.config import Config


class Backend(abc.ABC):
    """A job executor (mock, diffusers, comfyui, vision).

    Lifecycle: :meth:`available` is checked before construction; the constructor must be cheap (no model
    loading — that happens lazily in :meth:`run`); :meth:`close` releases resources on shutdown.

    Dispatch: the runner asks each configured backend, in order, whether it :meth:`accepts` a claimed job and
    runs the first that does.
    """

    #: Backend name used in ``MV_BACKENDS`` and at registration.
    name: ClassVar[str] = ""

    def __init__(self, config: "Config", gpus: list[dict[str, Any]]) -> None:
        self.config = config
        self.gpus = gpus

    @classmethod
    def available(cls, config: "Config") -> tuple[bool, str]:
        """Return ``(True, "")`` if this backend can run here, else ``(False, reason)``."""
        return True, ""

    @abc.abstractmethod
    def job_types(self) -> list[str]:
        """Job types this backend can execute (advertised at registration)."""

    def models(self) -> list[str]:
        """Model ids this backend serves (advertised at registration). Empty for model-less job types."""
        return []

    #: Job types that do not need a specific model (vision helpers); any ``model.id`` is accepted for them.
    model_agnostic_types: ClassVar[frozenset[str]] = frozenset(
        {"detect.panels", "character.score", "ocr", "upscale"}
    )

    def accepts(self, job: Job) -> bool:
        """Whether this backend should run ``job``."""
        if job.type not in self.job_types():
            return False
        if job.model_id is None or job.type in self.model_agnostic_types:
            return True
        return job.model_id in self.models()

    @abc.abstractmethod
    def run(self, job: Job, ctx: JobContext) -> JobResult:
        """Execute ``job``: download inputs via ``ctx``, report progress, upload artifacts, return the output."""

    def describe(self) -> dict[str, Any]:
        """Short summary for startup logs."""
        return {"backend": self.name, "job_types": self.job_types(), "models": self.models()}

    def close(self) -> None:  # noqa: B027 - optional hook with a no-op default
        """Release resources (models, sessions). Called once on shutdown."""
