"""Diffusers helpers that work without torch: specs, catalogue, VRAM cache, LoRA resolution."""

from __future__ import annotations

import threading
from pathlib import Path

import pytest

from mangaveyra_worker.diffusion.cache import PipelineCache
from mangaveyra_worker.diffusion.loras import resolve_loras
from mangaveyra_worker.diffusion.spec import ModelSpec, fits
from mangaveyra_worker.errors import BadInput
from mangaveyra_worker.manifests import ModelCatalog


def test_packaged_catalogue_parses() -> None:
    catalog = ModelCatalog.load(None)
    ids = catalog.ids()
    assert {"illustrious-xl", "sdxl-base", "qwen-image-edit", "flux2-klein"} <= set(ids)
    for m in catalog.with_section("diffusers"):
        spec = ModelSpec.from_manifest(m["id"], m["worker"]["diffusers"], m)
        assert spec.repo and spec.vram_mb > 0
    ill = ModelSpec.from_manifest(
        "illustrious-xl", catalog.get("illustrious-xl")["worker"]["diffusers"], catalog.get("illustrious-xl")
    )
    assert ill.trainable and "character.train" in ill.served_types(can_train=True)
    assert "character.train" not in ill.served_types(can_train=False)
    qwen = catalog.get("qwen-image-edit")
    q = ModelSpec.from_manifest("qwen-image-edit", qwen["worker"]["diffusers"], qwen)
    assert q.served_types(can_train=True) == ["image.edit"] and q.edit == "instruct" and q.edit_repo == q.repo


def test_spec_sizes_and_prompts() -> None:
    spec = ModelSpec.from_manifest(
        "m",
        {
            "repo": "x/y",
            "family": "sdxl",
            "prompt_suffix": "best quality",
            "negative_prefix": "worst quality",
        },
    )
    assert spec.size(None, None) == (896, 1152)
    assert spec.size(1001, 777) == (1000, 776)
    assert spec.size(4096, 2048) == (2048, 1024)  # capped at max_side, aspect kept
    assert spec.prompt("Rin", "bw_screentone").startswith("Rin, monochrome")
    assert spec.prompt("Rin", None).endswith("best quality")
    assert spec.negative("blurry") == "worst quality, blurry"
    flux = ModelSpec.from_manifest("f", {"repo": "a/b", "family": "flux"})
    assert flux.size(1000, 1000) == (1008, 1008)


@pytest.mark.parametrize(
    "section",
    [
        {},
        {"repo": "a/b", "family": "dalle"},
        {"repo": "a/b", "edit": "magic"},
        {"repo": "a/b", "offload_mode": "sometimes"},
    ],
)
def test_spec_validation(section: dict) -> None:
    with pytest.raises(ValueError):
        ModelSpec.from_manifest("m", section)


def test_fits_vram() -> None:
    spec = ModelSpec.from_manifest("m", {"repo": "a/b"}, {"vram_mb": 10000, "min_vram_mb": 5000})
    assert fits(spec, 24000, "auto", "cuda")
    assert fits(spec, 8000, "auto", "cuda")  # with offload
    assert not fits(spec, 8000, "none", "cuda")
    assert not fits(spec, 4000, "auto", "cuda")
    assert not fits(spec, 0, "auto", "cuda")  # no GPU
    assert fits(spec, 0, "auto", "cpu")  # explicit CPU


def test_catalog_resolve_prefers_local(tmp_path: Path) -> None:
    (tmp_path / "m.yaml").write_text("id: m\nworker:\n  diffusers:\n    repo: /models/local\n")
    catalog = ModelCatalog.load(tmp_path)
    server = {"worker": {"diffusers": {"repo": "hub/remote", "default_steps": 12}}}
    assert catalog.resolve("m", "diffusers", server) == {"repo": "/models/local", "default_steps": 12}
    assert catalog.resolve("other", "diffusers", server) == {"repo": "hub/remote", "default_steps": 12}


def test_pipeline_cache_evicts_lru_within_budget() -> None:
    evicted: list[str] = []
    cache = PipelineCache(10000, on_evict=lambda pipe: evicted.append(pipe))
    cache.get("a", 6000, lambda: "A")
    cache.get("b", 4000, lambda: "B")
    assert cache.get("a", 6000, lambda: "never").pipe == "A"  # hit; "a" is now most recent
    cache.get("c", 3000, lambda: "C")
    assert evicted == ["B"] and cache.keys() == ["a", "c"]
    # An entry in use is never evicted.
    busy = cache.get("a", 6000, lambda: "never")
    with busy.lock:
        cache.get("d", 6000, lambda: "D")
    assert "A" not in evicted and "C" in evicted
    cache.clear()
    assert cache.keys() == [] and cache.used_mb == 0


def test_pipeline_cache_loads_once_under_concurrency() -> None:
    loads = []
    cache = PipelineCache(10**6)

    def loader() -> str:
        loads.append(1)
        threading.Event().wait(0.1)
        return "P"

    threads = [threading.Thread(target=lambda: cache.get("k", 1, loader)) for _ in range(5)]
    for t in threads:
        t.start()
    for t in threads:
        t.join()
    assert len(loads) == 1


def test_lora_resolution(tmp_path: Path) -> None:
    lora_dir = tmp_path / "loras"
    lora_dir.mkdir()
    (lora_dir / "tidebreak-style.safetensors").write_bytes(b"x")
    downloads: list[str] = []

    def download(url: str) -> bytes:
        downloads.append(url)
        return b"trained"

    specs = [
        {"key": "jobs/01J/lora.safetensors", "weight": 0.8},
        {"key": "styles/tidebreak-style.safetensors"},
    ]
    files = {"lora_0": "https://studio/media/lora?sig=1"}
    refs = resolve_loras(specs, files, cache_dir=tmp_path / "cache", lora_dir=lora_dir, download=download)
    assert refs[0].path.read_bytes() == b"trained" and refs[0].weight == 0.8
    assert refs[1].path == (lora_dir / "tidebreak-style.safetensors").resolve() and refs[1].weight == 1.0
    assert refs[0].adapter != refs[1].adapter
    # Cached: a second job needs no download even without the signed URL.
    again = resolve_loras(specs[:1], {}, cache_dir=tmp_path / "cache", lora_dir=None, download=download)
    assert again[0].path == refs[0].path and len(downloads) == 1


def test_lora_errors(tmp_path: Path) -> None:
    kw = {"cache_dir": tmp_path, "lora_dir": tmp_path, "download": lambda u: b""}
    with pytest.raises(BadInput, match="not available"):
        resolve_loras([{"key": "missing.safetensors"}], {}, **kw)
    with pytest.raises(BadInput, match="needs a key"):
        resolve_loras([{"weight": 1}], {}, **kw)
    with pytest.raises(BadInput, match="out of range"):
        resolve_loras([{"key": "a", "weight": 9}], {}, **kw)
    outside = tmp_path.parent / "secret.safetensors"
    outside.write_bytes(b"s")
    with pytest.raises(BadInput):  # path traversal out of MV_LORA_DIR is refused
        resolve_loras(
            [{"key": "../secret.safetensors"}],
            {},
            cache_dir=tmp_path / "c",
            lora_dir=tmp_path,
            download=lambda u: b"",
        )
