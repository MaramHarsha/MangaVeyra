"""Configuration loading and validation."""

from __future__ import annotations

from pathlib import Path

import pytest

from mangaveyra_worker.config import Config
from mangaveyra_worker.errors import ConfigError


def test_env_overrides_defaults(tmp_path: Path) -> None:
    env = {
        "MV_SERVER": "https://studio.example/",
        "MV_WORKER_TOKEN": "mvw_abc",
        "MV_WORKER_NAME": "gpu-box",
        "MV_BACKENDS": "diffusers, mock",
        "MV_MAX_CONCURRENT": "2",
        "MV_MODELS_DIR": str(tmp_path),
        "MV_COMFYUI_URL": "http://127.0.0.1:8188/",
        "MV_TLS_VERIFY": "false",
        "MV_MOCK_STEP_DELAY": "0.2",
    }
    cfg = Config.load(env=env)
    assert cfg.server == "https://studio.example"
    assert cfg.token == "mvw_abc"
    assert cfg.name == "gpu-box"
    assert cfg.backends == ["diffusers", "mock"]
    assert cfg.max_concurrent == 2
    assert cfg.models_dir == tmp_path
    assert cfg.comfyui_url == "http://127.0.0.1:8188"
    assert cfg.tls_verify is False
    assert cfg.mock_step_delay == pytest.approx(0.2)


def test_precedence_file_env_flags(tmp_path: Path) -> None:
    toml = tmp_path / "worker.toml"
    toml.write_text('[worker]\nserver = "http://from-file:8080"\nname = "file-name"\nmax_concurrent = 3\n')
    cfg = Config.load({"name": "flag-name"}, env={"MV_SERVER": "http://from-env:8080"}, config_file=toml)
    assert cfg.server == "http://from-env:8080"  # env beats file
    assert cfg.name == "flag-name"  # flags beat env
    assert cfg.max_concurrent == 3  # file beats default


def test_token_file(tmp_path: Path) -> None:
    secret = tmp_path / "token"
    secret.write_text("mvw_from_file\n")
    cfg = Config.load(env={"MV_WORKER_TOKEN_FILE": str(secret)})
    assert cfg.token == "mvw_from_file"


@pytest.mark.parametrize(
    "env",
    [
        {"MV_SERVER": "ftp://x"},
        {"MV_MAX_CONCURRENT": "0"},
        {"MV_BACKENDS": "mock,teleport"},
        {"MV_OFFLOAD": "sometimes"},
        {"MV_MAX_CONCURRENT": "two"},
        {"MV_TLS_VERIFY": "perhaps"},
    ],
)
def test_invalid_values_raise(env: dict[str, str]) -> None:
    with pytest.raises(ConfigError):
        Config.load(env=env)


def test_unknown_file_key(tmp_path: Path) -> None:
    toml = tmp_path / "worker.toml"
    toml.write_text('colour = "blue"\n')
    with pytest.raises(ConfigError, match="unknown config keys"):
        Config.load(env={}, config_file=toml)


def test_redacted_masks_token() -> None:
    cfg = Config.load(env={"MV_WORKER_TOKEN": "mvw_supersecretvalue"})
    assert "supersecret" not in str(cfg.redacted())


def test_compose_aliases() -> None:
    cfg = Config.load(env={"MV_SERVER_URL": "http://app:8080", "MV_WORKER_BACKEND": "mock"})
    assert cfg.server == "http://app:8080" and cfg.backends == ["mock"]
    both = Config.load(env={"MV_SERVER": "http://a:1", "MV_SERVER_URL": "http://b:2"})
    assert both.server == "http://a:1"  # the primary name wins
