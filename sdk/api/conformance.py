# SPDX-License-Identifier: MIT
# Copyright (c) 2026 MangaVeyra contributors
"""Validate recorded World API responses against world.openapi.yaml.

The development stub's Go tests can record every API response they receive:

    rm -rf /tmp/wsrec && mkdir /tmp/wsrec
    WORLDSTUB_RECORD=/tmp/wsrec go test -count=1 ./cmd/worldstub/stub
    python3 sdk/api/conformance.py /tmp/wsrec/responses.jsonl

Each record is matched to its operation and status code and its JSON body is
validated against the documented schema (OpenAPI 3.1 schemas are JSON Schema
2020-12). Needs `pip install jsonschema pyyaml`.
"""

from __future__ import annotations

import json
import pathlib
import re
import sys

import jsonschema
import yaml

SPEC = pathlib.Path(__file__).with_name("world.openapi.yaml")


def main(records_path: str) -> int:
    doc = yaml.safe_load(SPEC.read_text(encoding="utf-8"))
    routes = []
    for template, ops in doc["paths"].items():
        pattern = re.compile("^" + re.sub(r"\{[^}]+\}", "[^/]+", template) + "$")
        routes.append((pattern, template, ops))

    def resolve(obj):
        while isinstance(obj, dict) and "$ref" in obj and len(obj) == 1:
            ptr = obj["$ref"].lstrip("#/").split("/")
            obj = doc
            for part in ptr:
                obj = obj[part]
        return obj

    checked, failures, undocumented = 0, 0, set()
    for line in pathlib.Path(records_path).read_text(encoding="utf-8").splitlines():
        rec = json.loads(line)
        method, path, status = rec["method"].lower(), rec["path"], str(rec["status"])
        match = [(t, ops) for p, t, ops in routes if p.match(path)]
        if not match:
            if status != "404":  # unknown endpoints answer with the error envelope
                undocumented.add(f"{method.upper()} {path}")
            continue
        template, ops = match[0]
        op = ops.get(method)
        if op is None:
            if status not in ("404", "405"):
                undocumented.add(f"{method.upper()} {template}")
            continue
        response = op["responses"].get(status) or op["responses"].get("default")
        if response is None:
            undocumented.add(f"{method.upper()} {template} -> {status}")
            continue
        response = resolve(response)
        media = response.get("content", {}).get("application/json")
        if media is None:
            continue
        schema = media["schema"]
        try:
            body = json.loads(rec["body"])
        except json.JSONDecodeError:
            print(f"FAIL {rec['test']}: {method.upper()} {path} {status}: body is not JSON")
            failures += 1
            continue
        # Embed the components next to the response schema so "#/components/..." references resolve
        # against the validated root with every jsonschema release (RefResolver is deprecated).
        rooted = {"$schema": "https://json-schema.org/draft/2020-12/schema", "allOf": [schema],
                  "components": doc.get("components", {})}
        validator = jsonschema.Draft202012Validator(rooted)
        errors = sorted(validator.iter_errors(body), key=lambda e: list(e.path))
        checked += 1
        for err in errors[:3]:
            loc = "/".join(str(p) for p in err.path)
            print(f"FAIL {rec['test']}: {method.upper()} {template} {status} at /{loc}: {err.message[:200]}")
        failures += bool(errors)
    for u in sorted(undocumented):
        print(f"UNDOCUMENTED {u}")
    print(f"{checked} responses checked, {failures} failures, {len(undocumented)} undocumented")
    return 1 if failures or undocumented else 0


if __name__ == "__main__":
    if len(sys.argv) != 2:
        print(__doc__)
        sys.exit(2)
    sys.exit(main(sys.argv[1]))
