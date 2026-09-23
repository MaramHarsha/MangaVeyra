"""Structured logging.

Every log call may carry structured fields through ``extra={"fields": {...}}`` (use :func:`kv`). Two output
formats are available: ``text`` (human-friendly ``logfmt``-style lines, the default on a terminal) and ``json``
(one JSON object per line, the default in containers, easy to ship to Loki/Elastic/journald).
"""

from __future__ import annotations

import json
import logging
import sys
import time
from typing import Any

_RESERVED = set(logging.LogRecord("", 0, "", 0, "", None, None).__dict__) | {"message", "asctime"}


def kv(**fields: Any) -> dict[str, Any]:
    """Build the ``extra`` argument for a structured log call: ``log.info("claimed", extra=kv(job=id))``."""
    return {"fields": fields}


def _fields(record: logging.LogRecord) -> dict[str, Any]:
    out: dict[str, Any] = {}
    extra = getattr(record, "fields", None)
    if isinstance(extra, dict):
        out.update(extra)
    for key, value in record.__dict__.items():
        if key not in _RESERVED and key != "fields" and not key.startswith("_"):
            out[key] = value
    return out


class JsonFormatter(logging.Formatter):
    """One JSON object per line: ``ts``, ``level``, ``logger``, ``msg`` plus the structured fields."""

    def format(self, record: logging.LogRecord) -> str:
        doc: dict[str, Any] = {
            "ts": time.strftime("%Y-%m-%dT%H:%M:%S", time.gmtime(record.created))
            + f".{int(record.msecs):03d}Z",
            "level": record.levelname.lower(),
            "logger": record.name,
            "msg": record.getMessage(),
        }
        doc.update(_fields(record))
        if record.exc_info:
            doc["exc"] = self.formatException(record.exc_info)
        return json.dumps(doc, default=str, ensure_ascii=False)


class TextFormatter(logging.Formatter):
    """``2026-09-23T10:00:00Z INFO  worker  claimed job=01J… type=image.generate`` lines."""

    def format(self, record: logging.LogRecord) -> str:
        ts = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime(record.created))
        parts = [
            f"{ts} {record.levelname:<5} {record.name.removeprefix('mangaveyra_worker.'):<10} {record.getMessage()}"
        ]
        for key, value in _fields(record).items():
            text = str(value)
            if not text or any(c.isspace() for c in text) or '"' in text:
                text = json.dumps(text, ensure_ascii=False)
            parts.append(f"{key}={text}")
        line = " ".join(parts)
        if record.exc_info:
            line += "\n" + self.formatException(record.exc_info)
        return line


def setup_logging(level: str = "info", fmt: str = "auto", stream: Any = None) -> None:
    """Configure the root logger once. ``fmt`` is ``json``, ``text`` or ``auto`` (text on a TTY, else json)."""
    stream = stream or sys.stderr
    if fmt == "auto":
        fmt = "text" if getattr(stream, "isatty", lambda: False)() else "json"
    handler = logging.StreamHandler(stream)
    handler.setFormatter(JsonFormatter() if fmt == "json" else TextFormatter())
    root = logging.getLogger()
    for old in list(root.handlers):
        root.removeHandler(old)
    root.addHandler(handler)
    root.setLevel(getattr(logging, level.upper(), logging.INFO))
    # Third-party libraries are chatty at INFO; keep their warnings only.
    for noisy in ("httpx", "httpcore", "urllib3", "PIL", "diffusers", "transformers", "websockets"):
        logging.getLogger(noisy).setLevel(logging.WARNING)


class FieldsAdapter(logging.LoggerAdapter):
    """A logger adapter that merges its bound fields with the call's ``extra=kv(...)`` fields."""

    def process(self, msg: Any, kwargs: Any) -> tuple[Any, Any]:
        bound = dict((self.extra or {}).get("fields", {}))
        extra = kwargs.get("extra") or {}
        bound.update(extra.get("fields", {}))
        kwargs["extra"] = {**extra, "fields": bound}
        return msg, kwargs


def bind(logger: logging.Logger, **fields: Any) -> FieldsAdapter:
    """Return ``logger`` with ``fields`` attached to every record."""
    return FieldsAdapter(logger, {"fields": fields})
