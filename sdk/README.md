<!-- SPDX-License-Identifier: MIT -->
# MangaVeyra SDK (MIT)

Everything under `sdk/` is licensed **MIT** ([`LICENSE`](LICENSE)), while the rest of this repository — the
self-hosted MangaVeyra app — is licensed **AGPL-3.0**.

## Why a separate licence

MangaVeyra is two products in two repositories:

1. **MangaVeyra** (this repository, AGPL-3.0): the open-source, self-hosted studio, reader and community that anyone
   can run on their own hardware.
2. **World** (world.mangaveyra.org, a separate private repository): the hub where creators share characters,
   templates, styles, series listings and plugins across servers, with human review.

The two only meet at a small, documented boundary: an HTTP API and a file format. Putting that boundary under a
permissive licence keeps the arrangement clean and fair in both directions:

* **Interoperability for everyone.** Anyone — other apps, tools, scripts, alternative hubs — can implement the
  World API or read and write `.mvpack` files without taking on the AGPL, and without the MangaVeyra team's
  permission.
* **A clean line around the AGPL.** The closed platform and third-party tools use only this MIT code, never AGPL
  code, so nobody has to wonder whether talking to a MangaVeyra server makes their program a derivative of it.
* **The app stays fully open.** Nothing the self-hosted app needs is hidden behind World: the app works completely
  without it (World is off by default), and every byte it sends to World is described here.
* **Contributions stay reusable.** Code contributed to `sdk/` can be copied into any project, including the app,
  the platform and plugins.

The boundary is checked automatically: CI's licence scan fails if a GPL/AGPL-licensed file appears under `sdk/`,
and `go test ./sdk/...` fails if a Go file here imports anything but the standard library and other `sdk/`
packages (so no AGPL code from `internal/` can leak in) or lacks its MIT SPDX header.

## Contents

| Path | What it is |
|---|---|
| [`api/`](api/) | API descriptions: the World public API ([`world.openapi.yaml`](api/world.openapi.yaml), OpenAPI 3.1), the AI worker protocol ([`worker.md`](api/worker.md)) and a guide to the app's own REST API. See [`api/README.md`](api/README.md). |
| [`mvpack/`](mvpack/) | The `.mvpack` pack format: [`SPEC.md`](mvpack/SPEC.md), JSON Schemas in [`schema/`](mvpack/schema/), and the Go reference implementation (package `mvpack`: Writer, streaming Reader with zip-slip / zip-bomb / allow-list protection, canonical JSON, ed25519 Sign/Verify, licence tokens), with test vectors in `testdata/`. |
| [`plugin/`](plugin/) | The plugin manifest (`plugin.json`): [`PLUGINS.md`](plugin/PLUGINS.md) (manifest-only plugins in v1, the WebAssembly sandbox ABI planned for v2), the JSON Schema, and the Go package `plugin` that validates manifests and computes permission diffs. |

Related, but part of the AGPL app: `cmd/worldstub` is a **development-only fake of World** that implements
`world.openapi.yaml` in memory, so the app's World module can be developed and tested without an account on the
real platform (`go run ./cmd/worldstub --seed --auto-approve`).

## Using the Go packages

```go
import (
    "github.com/MaramHarsha/MangaVeyra/sdk/mvpack"
    mvplugin "github.com/MaramHarsha/MangaVeyra/sdk/plugin"
)
```

Both packages depend only on the Go standard library, so they are easy to vendor into other projects.

## Checking the SDK

```sh
go vet ./sdk/... && go test ./sdk/...                  # Go packages, golden test vectors
python3 sdk/mvpack/schema/validate_examples.py          # JSON Schemas vs. valid/invalid examples (pip install jsonschema)
npx @redocly/cli lint sdk/api/world.openapi.yaml        # OpenAPI document
WORLDSTUB_RECORD=/tmp/rec go test ./cmd/worldstub/stub \
  && python3 sdk/api/conformance.py /tmp/rec/responses.jsonl   # stub responses vs. the OpenAPI schemas
```

After an intentional change to the pack format, regenerate the test vectors with
`go test ./sdk/mvpack -run Golden -update` and explain the change in `mvpack/SPEC.md`.

## Product rules that apply here too

* No prices, currencies, checkout or payment state in any schema, pack or API response. World items are `free`,
  `paid` or `owned` — labels, never amounts.
* Credit always uses the creator's World name, and every pack carries an honest per-role AI label.
* Examples and test data use original sample content only (Tidebreak Courier, Rin Asagiri, Pip the Gull, …).
