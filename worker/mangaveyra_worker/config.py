"""Worker configuration.

Precedence (highest first): command-line flags → ``MV_*`` environment variables → the optional TOML file
(``--config`` or ``MV_WORKER_CONFIG``) → defaults. Every option has an environment variable so the worker is easy
to run in Docker; see the README for the full table.
"""

from __future__ import annotations

import os
import socket
import tomllib
from dataclasses import dataclass, field, fields
from pathlib import Path
from typing import Any, Mapping

from mangaveyra_worker.errors import ConfigError

#: Backends in the order ``auto`` tries them (and the order jobs are dispatched when several accept one).
KNOWN_BACKENDS = ("diffusers", "comfyui", "vision", "mock")


def _split(value: str | list[str] | tuple[str, ...] | None) -> list[str]:
    if value is None:
        return []
    if isinstance(value, (list, tuple)):
        return [str(v).strip() for v in value if str(v).strip()]
    return [part.strip() for part in str(value).split(",") if part.strip()]


def _bool(value: Any) -> bool:
    if isinstance(value, bool):
        return value
    text = str(value).strip().lower()
    if text in ("1", "true", "yes", "on"):
        return True
    if text in ("0", "false", "no", "off", ""):
        return False
    raise ConfigError(f"not a boolean: {value!r}")


def _default_data_dir() -> Path:
    base = os.environ.get("XDG_CACHE_HOME") or str(Path.home() / ".cache")
    return Path(base) / "mangaveyra-worker"


@dataclass
class Config:
    """All worker settings. Field docs double as ``--help`` text."""

    server: str = "http://localhost:8080"
    """Base URL of the MangaVeyra server (MV_SERVER)."""
    token: str = ""
    """Worker token ``mvw_…`` (MV_WORKER_TOKEN, or a file path in MV_WORKER_TOKEN_FILE)."""
    name: str = field(default_factory=socket.gethostname)
    """Worker name shown in the admin UI (MV_WORKER_NAME). Re-registering with the same name keeps the id."""
    backends: list[str] = field(default_factory=lambda: ["auto"])
    """Comma list of backends, or ``auto`` (MV_BACKENDS)."""
    max_concurrent: int = 1
    """Jobs run in parallel (MV_MAX_CONCURRENT). Keep 1 per GPU unless models are small."""
    claim_wait_s: int = 20
    """Long-poll duration for ``claim`` (MV_CLAIM_WAIT_S)."""
    drain_timeout_s: float = 120.0
    """On SIGTERM, how long running jobs may finish before they are handed back (MV_DRAIN_TIMEOUT_S)."""
    data_dir: Path = field(default_factory=_default_data_dir)
    """Scratch space, LoRA cache and training runs (MV_WORKER_DATA_DIR)."""
    models_dir: Path | None = None
    """Directory of local model manifests (*.yaml/*.json) (MV_MODELS_DIR). Unset = the packaged catalogue."""
    models: list[str] = field(default_factory=list)
    """Only advertise these model ids (MV_MODELS). Empty = every manifest that fits the GPU."""
    lora_dir: Path | None = None
    """Directory holding LoRA files referenced by key (MV_LORA_DIR)."""
    hf_cache_dir: Path | None = None
    """Hugging Face cache for downloaded weights (MV_HF_CACHE → HF_HOME)."""
    device: str = "auto"
    """``auto``, ``cuda``, ``cuda:1``, ``mps`` or ``cpu`` (MV_DEVICE)."""
    offload: str = "auto"
    """``auto``, ``none``, ``model`` (model CPU offload) or ``sequential`` (MV_OFFLOAD)."""
    vram_budget_mb: int | None = None
    """Upper bound for cached pipelines' estimated VRAM (MV_VRAM_BUDGET_MB). Unset = 90 % of the GPU."""
    train_command: str | None = None
    """External LoRA trainer command template (MV_TRAIN_COMMAND); unset = built-in peft trainer."""
    comfyui_url: str | None = None
    """ComfyUI server URL, e.g. http://127.0.0.1:8188 (MV_COMFYUI_URL)."""
    comfyui_workflows_dir: Path | None = None
    """Directory of workflow bundles (MV_COMFYUI_WORKFLOWS)."""
    comfyui_timeout_s: float = 1800.0
    """Maximum time a single ComfyUI prompt may run (MV_COMFYUI_TIMEOUT_S)."""
    ocr_engine: str = "auto"
    """``auto``, ``manga-ocr``, ``tesseract``, ``http`` or ``none`` (MV_OCR_ENGINE)."""
    ocr_url: str | None = None
    """Endpoint for the ``http`` OCR engine (MV_OCR_URL)."""
    dino_model_dir: Path | None = None
    """Explicit offline DINOv2 Small model directory (MV_DINO_MODEL_DIR); no automatic downloads."""
    embedder: str = "auto"
    """Identity embedder: ``auto``/``dinov2``, or a legacy explicit diagnostic mode (MV_EMBEDDER)."""
    mock_models: list[str] = field(default_factory=lambda: ["mock"])
    """Model ids the mock backend advertises (MV_MOCK_MODELS)."""
    mock_step_delay: float = 0.05
    """Seconds per simulated denoising step in the mock backend (MV_MOCK_STEP_DELAY)."""
    max_download_mb: int = 64
    """Largest input file the worker downloads (MV_MAX_DOWNLOAD_MB)."""
    tls_verify: bool = True
    """Verify the server's TLS certificate (MV_TLS_VERIFY). Only disable on a trusted LAN."""
    health_file: Path | None = None
    """Touched after every successful server contact; for container health checks (MV_HEALTH_FILE)."""
    log_level: str = "info"
    """debug, info, warning, error (MV_LOG_LEVEL)."""
    log_format: str = "auto"
    """``auto``, ``text`` or ``json`` (MV_LOG_FORMAT)."""

    # ------------------------------------------------------------------ loading

    @classmethod
    def load(
        cls,
        overrides: Mapping[str, Any] | None = None,
        env: Mapping[str, str] | None = None,
        config_file: str | os.PathLike[str] | None = None,
    ) -> "Config":
        """Build a config from defaults, a TOML file, the environment and explicit overrides."""
        env = os.environ if env is None else env
        values: dict[str, Any] = {}
        path = config_file or env.get("MV_WORKER_CONFIG")
        if path:
            try:
                with open(path, "rb") as fh:
                    doc = tomllib.load(fh)
            except (OSError, tomllib.TOMLDecodeError) as exc:
                raise ConfigError(f"cannot read config file {path}: {exc}") from exc
            values.update(doc.get("worker", doc))
        for name, var in ENV_VARS.items():
            if var in env and env[var] != "":
                values[name] = env[var]
            elif env.get(ENV_ALIASES.get(name, "")):
                values[name] = env[ENV_ALIASES[name]]
        if not values.get("token") and env.get("MV_WORKER_TOKEN_FILE"):
            try:
                values["token"] = Path(env["MV_WORKER_TOKEN_FILE"]).read_text().strip()
            except OSError as exc:
                raise ConfigError(f"cannot read MV_WORKER_TOKEN_FILE: {exc}") from exc
        for key, value in (overrides or {}).items():
            if value is not None:
                values[key] = value
        return cls.from_mapping(values)

    @classmethod
    def from_mapping(cls, values: Mapping[str, Any]) -> "Config":
        """Coerce loosely typed values (strings from env/TOML) into a validated config."""
        known = {f.name for f in fields(cls)}
        unknown = set(values) - known
        if unknown:
            raise ConfigError(f"unknown config keys: {', '.join(sorted(unknown))}")
        cfg = cls()
        for key, raw in values.items():
            setattr(cfg, key, _coerce(key, raw))
        cfg.validate()
        return cfg

    def validate(self) -> None:
        """Raise :class:`ConfigError` for impossible settings."""
        self.server = self.server.rstrip("/")
        if not self.server.startswith(("http://", "https://")):
            raise ConfigError(f"MV_SERVER must be an http(s) URL, got {self.server!r}")
        if self.max_concurrent < 1:
            raise ConfigError("MV_MAX_CONCURRENT must be at least 1")
        if not 0 <= self.claim_wait_s <= 120:
            raise ConfigError("MV_CLAIM_WAIT_S must be between 0 and 120")
        for backend in self.backends:
            if backend != "auto" and backend not in KNOWN_BACKENDS:
                raise ConfigError(f"unknown backend {backend!r}; known: auto, {', '.join(KNOWN_BACKENDS)}")
        if self.offload not in ("auto", "none", "model", "sequential"):
            raise ConfigError("MV_OFFLOAD must be auto, none, model or sequential")
        if self.ocr_engine not in ("auto", "manga-ocr", "tesseract", "http", "none"):
            raise ConfigError("MV_OCR_ENGINE must be auto, manga-ocr, tesseract, http or none")
        if self.embedder not in ("auto", "dinov2", "insightface", "clip", "structural"):
            raise ConfigError("MV_EMBEDDER must be auto, dinov2, insightface, clip or structural")
        if self.log_format not in ("auto", "text", "json"):
            raise ConfigError("MV_LOG_FORMAT must be auto, text or json")
        if self.comfyui_url:
            self.comfyui_url = self.comfyui_url.rstrip("/")

    def redacted(self) -> dict[str, Any]:
        """Settings safe to log (the token is masked)."""
        out = {f.name: getattr(self, f.name) for f in fields(self)}
        if self.token:
            out["token"] = self.token[:8] + "…"
        return {k: (str(v) if isinstance(v, Path) else v) for k, v in out.items()}


#: Config field → environment variable.
ENV_VARS: dict[str, str] = {
    "server": "MV_SERVER",
    "token": "MV_WORKER_TOKEN",
    "name": "MV_WORKER_NAME",
    "backends": "MV_BACKENDS",
    "max_concurrent": "MV_MAX_CONCURRENT",
    "claim_wait_s": "MV_CLAIM_WAIT_S",
    "drain_timeout_s": "MV_DRAIN_TIMEOUT_S",
    "data_dir": "MV_WORKER_DATA_DIR",
    "models_dir": "MV_MODELS_DIR",
    "models": "MV_MODELS",
    "lora_dir": "MV_LORA_DIR",
    "hf_cache_dir": "MV_HF_CACHE",
    "device": "MV_DEVICE",
    "offload": "MV_OFFLOAD",
    "vram_budget_mb": "MV_VRAM_BUDGET_MB",
    "train_command": "MV_TRAIN_COMMAND",
    "comfyui_url": "MV_COMFYUI_URL",
    "comfyui_workflows_dir": "MV_COMFYUI_WORKFLOWS",
    "comfyui_timeout_s": "MV_COMFYUI_TIMEOUT_S",
    "ocr_engine": "MV_OCR_ENGINE",
    "ocr_url": "MV_OCR_URL",
    "embedder": "MV_EMBEDDER",
    "dino_model_dir": "MV_DINO_MODEL_DIR",
    "mock_models": "MV_MOCK_MODELS",
    "mock_step_delay": "MV_MOCK_STEP_DELAY",
    "max_download_mb": "MV_MAX_DOWNLOAD_MB",
    "tls_verify": "MV_TLS_VERIFY",
    "health_file": "MV_HEALTH_FILE",
    "log_level": "MV_LOG_LEVEL",
    "log_format": "MV_LOG_FORMAT",
}

#: Alternative variable names accepted when the primary one is unset (used by deploy/docker-compose.yml).
ENV_ALIASES: dict[str, str] = {
    "server": "MV_SERVER_URL",
    "backends": "MV_WORKER_BACKEND",
}

_LISTS = {"backends", "models", "mock_models"}
_INTS = {"max_concurrent", "claim_wait_s", "max_download_mb"}
_FLOATS = {"drain_timeout_s", "comfyui_timeout_s", "mock_step_delay"}
_PATHS = {"data_dir", "models_dir", "lora_dir", "hf_cache_dir", "dino_model_dir", "comfyui_workflows_dir", "health_file"}
_OPT_INTS = {"vram_budget_mb"}
_BOOLS = {"tls_verify"}


def _coerce(key: str, raw: Any) -> Any:
    try:
        if key in _LISTS:
            return _split(raw)
        if key in _INTS:
            return int(raw)
        if key in _FLOATS:
            return float(raw)
        if key in _OPT_INTS:
            return None if raw in (None, "") else int(raw)
        if key in _BOOLS:
            return _bool(raw)
        if key in _PATHS:
            return None if raw in (None, "") else Path(str(raw)).expanduser()
        if key in ("train_command", "comfyui_url", "ocr_url"):
            return None if raw in (None, "") else str(raw)
        return str(raw)
    except (TypeError, ValueError) as exc:
        raise ConfigError(f"invalid value for {key}: {raw!r}") from exc
