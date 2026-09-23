"""Command line: ``mangaveyra-worker [run|check|job]``.

* ``run`` (default) — connect to the server and process jobs until SIGINT/SIGTERM.
* ``check`` — print detected GPUs, backends, job types and models as JSON, without contacting a server.
* ``job`` — run one job locally without a server (inputs from disk, outputs to a directory); handy to test a
  model manifest or a ComfyUI workflow bundle::

      mangaveyra-worker job image.generate --input '{"prompt": "Rin Asagiri on the pier", "n": 2}' --out /tmp/out
      mangaveyra-worker job detect.panels --file page=scan.png --input '{"reading": "rtl"}'
"""

from __future__ import annotations

import argparse
import json
import logging
import sys
import time
from pathlib import Path
from typing import Any, Mapping, Sequence

from mangaveyra_worker import __version__
from mangaveyra_worker.backends import build_backends
from mangaveyra_worker.config import Config
from mangaveyra_worker.errors import BackendUnavailable, ConfigError, JobFailure
from mangaveyra_worker.gpu import detect_gpus
from mangaveyra_worker.jobs import Job, JobContext
from mangaveyra_worker.log import setup_logging

log = logging.getLogger("mangaveyra_worker")


def _parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(
        prog="mangaveyra-worker",
        description="MangaVeyra AI worker. Every option also has an MV_* environment variable (see README).",
    )
    p.add_argument("--version", action="version", version=f"mangaveyra-worker {__version__}")
    p.add_argument("--config", help="TOML config file (MV_WORKER_CONFIG)")
    p.add_argument("--server", help="server base URL (MV_SERVER)")
    p.add_argument(
        "--token", help="worker token (MV_WORKER_TOKEN); prefer the env var or MV_WORKER_TOKEN_FILE"
    )
    p.add_argument("--name", help="worker name (MV_WORKER_NAME)")
    p.add_argument("--backends", help="comma list or auto (MV_BACKENDS)")
    p.add_argument("--max-concurrent", type=int, help="parallel jobs (MV_MAX_CONCURRENT)")
    p.add_argument("--models-dir", help="local model manifests (MV_MODELS_DIR)")
    p.add_argument("--comfyui-url", help="ComfyUI server (MV_COMFYUI_URL)")
    p.add_argument("--log-level", help="debug|info|warning|error (MV_LOG_LEVEL)")
    p.add_argument("--log-format", choices=["auto", "text", "json"], help="(MV_LOG_FORMAT)")
    sub = p.add_subparsers(dest="command")
    sub.add_parser("run", help="process jobs from the server (default)")
    sub.add_parser("check", help="print capabilities as JSON and exit")
    job = sub.add_parser("job", help="run one job locally without a server")
    job.add_argument("type", help="job type, e.g. image.generate")
    job.add_argument("--input", default="{}", help="job input as JSON (or @file.json)")
    job.add_argument("--model", help="model id")
    job.add_argument(
        "--file", action="append", default=[], metavar="NAME=PATH", help="input file (repeatable)"
    )
    job.add_argument("--out", default="./mv-out", help="output directory")
    return p


def _overrides(args: argparse.Namespace) -> dict[str, Any]:
    return {
        "server": args.server,
        "token": args.token,
        "name": args.name,
        "backends": args.backends,
        "max_concurrent": args.max_concurrent,
        "models_dir": args.models_dir,
        "comfyui_url": args.comfyui_url,
        "log_level": args.log_level,
        "log_format": args.log_format,
    }


def main(argv: Sequence[str] | None = None) -> int:
    """Entry point of the ``mangaveyra-worker`` console script."""
    args = _parser().parse_args(argv)
    try:
        config = Config.load(_overrides(args), config_file=args.config)
    except ConfigError as exc:
        print(f"mangaveyra-worker: {exc}", file=sys.stderr)
        return 2
    setup_logging(config.log_level, config.log_format)
    if config.hf_cache_dir:
        import os

        os.environ.setdefault("HF_HOME", str(config.hf_cache_dir))
    command = args.command or "run"
    try:
        if command == "check":
            return _check(config)
        if command == "job":
            return _local_job(config, args)
        return _run(config)
    except (ConfigError, BackendUnavailable) as exc:
        log.error(str(exc))
        return 2


def _run(config: Config) -> int:
    from mangaveyra_worker.runner import Worker

    if not config.token:
        raise ConfigError(
            "MV_WORKER_TOKEN is required (create one in Admin → Models & providers → Local worker)"
        )
    gpus = detect_gpus()
    backends = build_backends(config, gpus)
    log.info(
        "starting",
        extra={
            "fields": {
                "version": __version__,
                "server": config.server,
                "name": config.name,
                "backends": [b.name for b in backends],
                "gpus": len(gpus),
            }
        },
    )
    for b in backends:
        log.info("backend ready", extra={"fields": b.describe()})
    # Re-probe on every heartbeat for fresh free-VRAM numbers; a GPU-less host has nothing to refresh.
    worker = Worker(config, backends, gpu_probe=detect_gpus if gpus else (lambda: []))
    return worker.run()


def _check(config: Config) -> int:
    gpus = detect_gpus()
    backends = build_backends(config, gpus)
    doc = {
        "version": __version__,
        "gpus": gpus,
        "backends": [b.describe() for b in backends],
        "job_types": sorted({t for b in backends for t in b.job_types()}),
        "models": sorted({m for b in backends for m in b.models()}),
    }
    print(json.dumps(doc, indent=2, default=str))
    for b in backends:
        b.close()
    return 0


class LocalClient:
    """A stand-in for :class:`ServerClient` that reads inputs from disk and writes artifacts to a directory."""

    def __init__(self, out: Path) -> None:
        self.out = out
        self.out.mkdir(parents=True, exist_ok=True)
        self.abort = None

    def progress(self, job_id: str, progress: float, **kw: Any) -> bool:
        """Print progress to stderr."""
        step, steps = kw.get("step"), kw.get("steps")
        where = f" {step}/{steps}" if step is not None and steps else ""
        print(f"\r{progress * 100:5.1f}%{where} {kw.get('message') or ''}".ljust(60), end="", file=sys.stderr)
        return False

    def upload_artifact(
        self,
        job_id: str,
        name: str,
        data: bytes,
        *,
        filename: str | None = None,
        content_type: str = "",
        meta: Mapping[str, Any] | None = None,
    ) -> str:
        """Write the artifact next to the others."""
        path = self.out / (filename or name)
        path.write_bytes(data)
        return str(path)

    def download(self, url: str) -> bytes:
        """Read a local file."""
        return Path(url).read_bytes()


def _local_job(config: Config, args: argparse.Namespace) -> int:
    raw = args.input
    if raw.startswith("@"):
        raw = Path(raw[1:]).read_text()
    files = {}
    for spec in args.file:
        name, _, path = spec.partition("=")
        if not path:
            raise ConfigError(f"--file expects NAME=PATH, got {spec!r}")
        files[name] = str(Path(path).expanduser().resolve())
    job = Job.from_payload(
        {
            "id": f"local-{int(time.time())}",
            "type": args.type,
            "input": json.loads(raw),
            "files": files,
            "model": {"id": args.model} if args.model else None,
        }
    )
    gpus = detect_gpus()
    backends = build_backends(config, gpus)
    backend = next((b for b in backends if b.accepts(job)), None)
    if backend is None:
        raise ConfigError(f"no configured backend accepts {job.type} (model {job.model_id})")
    client = LocalClient(Path(args.out))
    ctx = JobContext(job, client, progress_interval=0.2)  # type: ignore[arg-type]
    try:
        result = backend.run(job, ctx)
    except JobFailure as exc:
        print(file=sys.stderr)
        log.error("job failed", extra={"fields": {"code": exc.code, "error": exc.message}})
        return 1
    finally:
        ctx.cleanup()
        for b in backends:
            b.close()
    print(file=sys.stderr)
    print(
        json.dumps(
            {"backend": backend.name, "output": result.output, "gpu_seconds": result.gpu_seconds}, indent=2
        )
    )
    return 0


if __name__ == "__main__":  # pragma: no cover
    raise SystemExit(main())
