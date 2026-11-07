<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="brand/logo/svg/lockup-color-on-dark.svg">
    <source media="(prefers-color-scheme: light)" srcset="brand/logo/svg/lockup-color-on-light.svg">
    <img alt="MangaVeyra" src="brand/logo/svg/lockup-color-on-light.svg" width="520">
  </picture>
</p>

<h3 align="center">Your story. Every panel. <em>Pre-alpha.</em></h3>

<p align="center">
  MangaVeyra is being built as an open-source, self-hosted AI manga studio, reader and community.<br>
  Phase 1 takes an original premise through scripts, characters, layered pages and lettering<br>
  to PNG, CBZ and print-ready PDF exports. Publishing and reading remain planned.
</p>

<p align="center">
  <a href="LICENSE"><img alt="License: AGPL-3.0" src="https://img.shields.io/badge/license-AGPL--3.0-3D8BFF?style=flat-square"></a>
  <img alt="Status: pre-alpha" src="https://img.shields.io/badge/status-pre--alpha-F5B83D?style=flat-square">
  <a href="go.mod"><img alt="Go 1.27" src="https://img.shields.io/badge/Go-1.27-00ADD8?style=flat-square&logo=go&logoColor=white"></a>
  <a href="#quick-start"><img alt="Self-hosted" src="https://img.shields.io/badge/self--hosted-yes-3CCB7F?style=flat-square"></a>
  <a href="#privacy-and-security"><img alt="No telemetry" src="https://img.shields.io/badge/telemetry-none-3CCB7F?style=flat-square"></a>
</p>

<p align="center">
  <a href="#current-status">Current&nbsp;status</a> ·
  <a href="#quick-start">Quick&nbsp;start</a> ·
  <a href="#product-direction">Product&nbsp;direction</a> ·
  <a href="#development">Development</a> ·
  <a href="docs/FEATURES.md">Feature&nbsp;plan</a>
</p>

---

## Current status

> **MangaVeyra is pre-alpha development software.** The first playable studio workflow uses your own configured
> providers or the built-in offline placeholder. Create a project, edit its script, build a character bible, make
> pages, choose art variations, edit structured lettering and export a chapter.

### Implemented and tested

- **Server foundation:** configuration, SQLite and PostgreSQL migrations, authentication, sessions, roles, 2FA,
  storage, encrypted secrets, security middleware and signed media URLs.
- **Runtime services:** database-backed jobs, worker protocol, WebSockets, notifications, English/Japanese i18n and
  the PWA/static-asset shell.
- **Working screens:** first-run setup, provider connections, login/signup/onboarding, account/settings, project
  creation/adaptation, the real Studio dashboard, generation state and the editable chapter manuscript.
- **Phase 2 activity:** the designed G9 job queue and G4 notification center, with scoped job visibility, progress,
  cancellation, admin worker pause/resume, categorized notifications and explicit read actions. Remaining Phase 2
  work is tracked in [plan 0006](docs/plans/0006-phase-2-consistency-and-control.md).
- **Gate A generation:** deterministic offline script generation and fixed-model OpenRouter or OpenAI structured
  text, with hard spend reservations, project/token authorization, revision fencing and honest AI provenance.
- **Visual generation:** deterministic offline images, fixed OpenAI/OpenRouter/fal image adapters, configured worker
  jobs, local OpenAI-compatible text, capability/licence filtering, routing pins and bounded fallback. The direct
  OpenAI image route has fake-provider coverage and passed a [live three-layer manga check](docs/qa/openai-live-2026-11-07.md);
  reference/mask/accounting matrix checks remain pending in ADR 0009.
- **Studio bibles:** project library, character reference sheets/uploads, profile edits, outfits and setting items.
- **Page production:** polygon panels with independent background/character/FX layers, four variations, exact masked
  region edits, revision-bound saves, locks, history/revert and a lazy Svelte/Pixi editor with undo/redo.
- **Lettering and export:** shared shaped glyph paths, bubble shapes/tails, RTL reading order, vertical Japanese and
  Chinese, and ruby; PNG ZIP, CBZ and vector-lettered PDF with B5/A5/KDP trims, actual trim/bleed boxes and optional
  credits.
- **Manga content languages:** projects, lettering and exports in English, Japanese, Korean, Simplified and
  Traditional Chinese, Spanish, French, German, Portuguese and Hindi, with bundled OFL fonts and per-glyph fallback
  (ADR 0008). The interface is English and Japanese.
- **Original demo:** `mangaveyra seed --demo` creates the unpublished Tidebreak Courier project for an existing
  account, with labelled placeholder artwork. Rerunning preserves your edits.
- **Python AI worker:** deterministic placeholder output, diffusers and ComfyUI adapters, LoRA training, panel and
  bubble detection, OCR, image helpers and Character Lock scoring. The real GPU path still needs hardware validation.
- **Frontend foundation:** design tokens, responsive components, themes, motion, HTMX integration and the Svelte
  island registry.
- **SDK and operations foundation:** `.mvpack` and plugin schemas, the worker and World API contracts, a development
  World stub, Docker/Compose files, systemd and Caddy examples.

### Not yet an end product

Publishing, reader, application MCP server, backup/update flows, admin UI, training/Character Lock repair,
translation, animation, EPUB/video export and the World link remain incomplete or intentional stubs. Their
designed controls stay visible as disabled **Soon** actions. GPU/model quality still needs hardware validation;
remote routes have fake-transport coverage, and direct OpenAI has a recorded basic live check; its extended
reference/mask/accounting matrix remains pending in ADR 0009. See the
[accepted MVP decision](docs/decisions/0001-mvp-scope.md), the
[hard remote-spend decision](docs/decisions/0003-hard-remote-spend-reservations.md),
the [completed script plan](docs/plans/0001-script-vertical-slice.md), the
[Phase 1 plan](docs/plans/0002-phase-1-first-playable.md), and the
[desktop QA and content-language follow-up](docs/plans/0004-phase-1-qa-fixes-and-content-languages.md).

## Why MangaVeyra

Most AI comic tools generate a flat picture per panel. MangaVeyra's product architecture instead treats a panel as
structured data: polygon geometry plus independent background, character, effects and lettering layers. Script,
dialogue, provenance and edit scope travel with the page so a creator can change one part without
rerolling everything else.

The deployment target is one Go binary and a database, with optional Python workers for local AI. The open-source
application is designed to accept the operator's own models and keys, send no telemetry, and keep the complete
studio, reader and community self-hostable. The studio's first playable workflow is implemented; the reader and
community are later phases.

## Product direction

The first playable establishes the production workflow; the remaining roadmap adds consistency repair and reading.

| Planned capability | Intended behavior |
|---|---|
| **Character Lock** | Score character consistency and fix a drifted character without replacing the rest of a panel. Reference-image guidance is the default; LoRA training is optional. |
| **Scoped edits** | Phase 1 generates chapter layouts, panel/layer art and masked regions with explicit scopes and locks. |
| **Layered panels** | Regenerate backgrounds, characters, effects and lettering independently while keeping lettering as structured text. |
| **Explainable routing** | Choose among configured local and remote models, show why, respect spend and licence guards, and make `local_only` a hard outbound boundary. |
| **Panel-aware reading** | Support guided mobile reading, structured alt text, read-aloud and honest per-role AI provenance. |

### Planned feature set

**Create** — premise to structured arcs, chapters and panel scripts; character and setting bibles; layouts and
sketch-to-manga.

**Edit** — a PixiJS/Svelte page editor, explicit change scope, region edits, variations, lettering, locks,
before/after comparison, collaboration and mobile light editing.

**Publish and read** — manga, comics and webtoons; PNG/PDF/EPUB/CBZ/video export; translation; paged, spread, guided
and scroll readers; accessibility, offline reading and community features.

**Operate** — provider management, fair GPU sharing, moderation, import, backup/restore, signed updates, MCP and an
optional World directory connection.

The full classification and release intent live in [docs/FEATURES.md](docs/FEATURES.md). That document is a roadmap,
not a list of shipped behavior.

## Quick start

These commands start the first playable studio for development and evaluation. No provider keys or GPU are required
for the complete placeholder workflow.

### Docker Compose (PostgreSQL)

```sh
git clone https://github.com/MaramHarsha/MangaVeyra.git
cd MangaVeyra
cp .env.example .env
# edit .env: POSTGRES_PASSWORD (letters and digits; it goes into MV_DB),
#            MV_PUBLIC_URL and MV_WORKER_TOKEN
docker compose --env-file .env -f deploy/docker-compose.yml up -d --build
```

Open <http://localhost:8080>, follow first-run setup, and either keep the offline placeholder or connect OpenRouter or
OpenAI.

Script jobs run in Go. Configured image workers receive bounded child jobs from the Go generation orchestrator:

```sh
# NVIDIA GPU worker
docker compose --env-file .env -f deploy/docker-compose.yml --profile gpu up -d --build
# CPU worker with the deterministic placeholder backend
docker compose --env-file .env -f deploy/docker-compose.yml --profile worker-cpu up -d --build
```

See [worker/README.md](worker/README.md) for `mangaveyra-worker check` and local job commands.

### Single binary (SQLite)

```sh
make build               # web assets + templ generate + ./bin/mangaveyra
./bin/mangaveyra serve   # SQLite in ./data, http://localhost:8080
```

Building from source requires Go 1.27.1 or newer and Node.js 24 LTS with npm. The
[templ](https://templ.guide) CLI (`make tools`) is needed only when changing `.templ` files.

### Command line

Implemented commands:

```text
mangaveyra serve                     Run the server
mangaveyra migrate [status]          Apply pending migrations or list them
mangaveyra user create               Create an account
mangaveyra user reset-password       Set a new password
mangaveyra user set-role             Change a site role
mangaveyra version                   Show version information
```

After setup, run `./bin/mangaveyra seed --demo` (or `--owner username`) for the original demo. It creates no accounts
and publishes nothing. `backup` and `mcp-stdio` remain unavailable command placeholders.

## Configuration

The implemented foundation is configured through `MV_*` environment variables or a YAML file
(`mangaveyra serve --config mangaveyra.yaml`, or `MV_CONFIG=mangaveyra.yaml`). Common settings include:

| Variable | Default | Purpose |
|---|---|---|
| `MV_ADDR` | `:8080` | Listen address |
| `MV_DATA_DIR` | `data` | SQLite database, media and key storage |
| `MV_DB` | SQLite in the data dir | Use a `postgres://...` URL for PostgreSQL |
| `MV_PUBLIC_URL` | — | Public URL for links, signed media and CSRF origin; `https://` enables Secure cookies and HSTS |
| `MV_TRUSTED_PROXIES` | — | Reverse proxies whose forwarded headers are trusted |
| `MV_STORAGE` | `local` | Use `s3` for an S3-compatible store (`MV_S3_*`) |
| `MV_WORKER_TOKEN` | — | Shared secret used by the worker protocol |
| `MV_ALLOW_PRIVATE_PROVIDER_URLS` | `false` | Permit explicitly configured private provider endpoints while keeping metadata/link-local targets blocked |
| `MV_SMTP_*` | — | Optional password-reset and notification email |
| `MV_MASTER_KEY` | generated `data/secret.key` | Encrypt stored secrets; back it up with the data directory |

See [.env.example](.env.example) for the complete list. Configuration entries for a stubbed feature do not make that
feature operational.

## Models and providers

The **worker side** is implemented: its local catalogue includes supported diffusion/edit models, and it provides
diffusers, ComfyUI, mock and vision backends. It can register, claim jobs, report progress and upload results through
the worker protocol.

The server provides sealed OpenRouter/OpenAI text connections, fixed OpenRouter/fal image routes, custom local
OpenAI-compatible text, an offline placeholder and a model catalogue. Routing modes, scoped pins, commercial licence
checks and fallback are enforced again at dispatch. Remote calls use durable spend reservations; the fixed boundaries
are documented in ADRs [0005](docs/decisions/0005-gate-a-openrouter-text.md),
[0006](docs/decisions/0006-openai-direct-text.md) and
[0007](docs/decisions/0007-phase-1-image-and-local-routes.md).

**Local only** permits the in-process placeholder and blocks custom HTTP/provider calls and worker handoff. A worker
may be on another machine, and a nominally local text service may forward to a cloud model; configured workers/local
services are available in eligible ordinary/free routing modes instead. The app sends no telemetry.

## Agents (MCP, planned)

The SDK and database contain MCP-related contracts, but the application MCP server is not implemented and
`mangaveyra mcp-stdio` is currently unavailable. The planned design uses project-scoped agent tokens, labelled and
undoable changes, cost approvals, and mandatory human action for publishing, deleting, sharing and unlocking.

## World (optional, planned integration)

World is designed as an optional external directory for sharing characters, templates, style packs and series
listings. It is not part of this repository. The public API schema, `.mvpack` format and development `worldstub` are
implemented under [`sdk/`](sdk/README.md), but the MangaVeyra application does not yet link to World or submit items.
World remains off by default, and this open-source repository never contains payment UI.

## Architecture

| Part | Technology | Current state |
|---|---|---|
| Server | Go `net/http`, embedded assets | First playable studio and bounded generation pipeline |
| Pages | templ + HTMX | Setup, auth, settings, providers/routing, Studio bibles, script and export screens |
| Rich UI | TypeScript, Svelte 5, PixiJS | Page/layout/lettering/variation/mask editor, loaded only on editor routes |
| AI workers | Python, PyTorch/diffusers, ComfyUI adapter | Implemented; GPU path needs hardware validation |
| Data | PostgreSQL or SQLite | Schema and migration runner implemented |
| Files | Local disk or S3-compatible storage | Implemented |
| Realtime | WebSockets and Yjs relay | Foundation implemented |
| Product modules | Studio, editor, publishing, reader, export, MCP | Studio/editor/export implemented; publishing, reader and MCP remain planned |

```text
cmd/mangaveyra     server and CLI           internal/   core and feature packages
web/               frontend source          worker/     Python AI worker
sdk/               MIT APIs and formats     deploy/     container and service files
docs/              architecture and plans   brand/      logo pack
```

Read [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for repository contracts and
[docs/SCREENS.md](docs/SCREENS.md) for the design-to-route map.

## Development

```sh
make tools        # install templ and optional golangci-lint
make build        # web assets + templ generation + Go binary
make run          # build and serve on :8080 with SQLite in dev mode
make test         # Go, frontend and worker suites
make test-pg      # add PostgreSQL coverage through a local container
```

The browser suite starts its own server and a fresh temporary database:

```sh
make build
cd e2e
npm ci
npx playwright install chromium  # once, or use an installed Chrome below
npm test
# MV_BROWSER_CHANNEL=chrome npm test
```

It completes setup without keys, the five-step wizard, script/character generation, layered art, lettering and PDF/CBZ
downloads, and checks the desktop-derived layout at phone width. See [e2e/README.md](e2e/README.md) for design evidence.

The workflow in [`.github/workflows/ci.yml`](.github/workflows/ci.yml) runs for pull requests and can also be started
manually from the Actions tab.

## Privacy and security

- The implemented foundation has no telemetry or usage tracking.
- Passwords use argon2id; session tokens are stored hashed; 2FA, CSRF protection, strict CSP, rate limits, encrypted
  secrets and signed media URLs are implemented.
- Providers are explicitly configured; `local_only` is enforced at planning, dispatch and worker claims. World and
  update integration remain planned and opt-in.

## License

MangaVeyra is free software under the [GNU Affero General Public License v3.0](LICENSE): if you run a modified version
as a network service, you must offer its source code to its users. The API schemas, `.mvpack` format and plugin SDK in
[`sdk/`](sdk/LICENSE) are MIT. The MangaVeyra name and logo identify the official project; forks must not present
themselves as official MangaVeyra builds. Third-party notices are listed in [NOTICE](NOTICE).
