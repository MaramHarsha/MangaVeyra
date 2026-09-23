# SPDX-License-Identifier: MIT
# Copyright (c) 2026 MangaVeyra contributors
"""Check the JSON Schemas against the example documents in sdk/*/testdata.

Every file under testdata/**/valid must validate and every file under
testdata/**/invalid must not. The Go tests check the same files with the Go
validators, so the schema and the code cannot drift apart silently.

Usage:  python3 sdk/mvpack/schema/validate_examples.py   (needs `pip install jsonschema`)
"""

from __future__ import annotations

import json
import pathlib
import sys

import jsonschema

SDK = pathlib.Path(__file__).resolve().parents[2]


def load(path: pathlib.Path):
    with path.open(encoding="utf-8") as f:
        return json.load(f)


def validator_for(schema_path: pathlib.Path):
    schema = load(schema_path)
    cls = jsonschema.validators.validator_for(schema)
    cls.check_schema(schema)
    store = {}
    for other in schema_path.parent.glob("*.schema.json"):
        doc = load(other)
        store[doc["$id"]] = doc
        store[other.name] = doc
    resolver = jsonschema.RefResolver.from_schema(schema, store=store)
    return cls(schema, resolver=resolver, format_checker=cls.FORMAT_CHECKER)


def check(schema_path: pathlib.Path, valid_dir: pathlib.Path, invalid_dir: pathlib.Path | None) -> int:
    v = validator_for(schema_path)
    failures = 0
    for f in sorted(valid_dir.glob("*.json")):
        errs = sorted(v.iter_errors(load(f)), key=lambda e: list(e.path))
        if errs:
            failures += 1
            print(f"FAIL {f.relative_to(SDK)} should be valid: {errs[0].message}")
    if invalid_dir is not None:
        for f in sorted(invalid_dir.glob("*.json")):
            if v.is_valid(load(f)):
                failures += 1
                print(f"FAIL {f.relative_to(SDK)} should be invalid")
    count = len(list(valid_dir.glob("*.json"))) + (len(list(invalid_dir.glob("*.json"))) if invalid_dir else 0)
    print(f"{schema_path.relative_to(SDK)}: {count} examples, {failures} failures")
    return failures


def main() -> int:
    mv = SDK / "mvpack"
    pl = SDK / "plugin"
    failures = 0
    failures += check(mv / "schema/manifest.schema.json", mv / "testdata/manifests/valid", mv / "testdata/manifests/invalid")
    failures += check(mv / "schema/signature.schema.json", mv / "testdata/signatures", None)
    if (pl / "schema/plugin.schema.json").exists():
        failures += check(pl / "schema/plugin.schema.json", pl / "testdata/valid", pl / "testdata/invalid")
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())
