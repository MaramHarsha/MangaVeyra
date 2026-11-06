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
  The current repository contains a tested server foundation, UI kit and AI worker,<br>
  but it does not yet provide an end-to-end story, editor, publishing or reading workflow.
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

> **MangaVeyra is pre-alpha development software.** You can build and run the foundation, complete first-run setup,
> manage accounts and settings, and exercise the worker and SDK independently. You cannot yet turn a premise into an
> editable, publishable manga chapter through the application.

### Implemented and tested

- **Server foundation:** configuration, SQLite and PostgreSQL migrations, authentication, sessions, roles, 2FA,
  storage, encrypted secrets, security middleware and signed media URLs.
- **Runtime services:** database-backed jobs, worker protocol, WebSockets, notifications, English/Japanese i18n and
  the PWA/static-asset shell.
- **Working screens:** first-run setup, login, signup, password reset, onboarding, account/settings pages, error pages,
  the design-system guide and an empty dashboard.
- **Python AI worker:** deterministic placeholder output, diffusers and ComfyUI adapters, LoRA training, panel and
  bubble detection, OCR, image helpers and Character Lock scoring. The real GPU path still needs hardware validation.
- **Frontend foundation:** design tokens, responsive components, themes, motion, HTMX integration and the Svelte
  island registry.
- **SDK and operations foundation:** `.mvpack` and plugin schemas, the worker and World API contracts, a development
  World stub, Docker/Compose files, systemd and Caddy examples.

### Not yet an end product

The server-side AI catalogue, cloud-provider adapters, routing UI, Studio data services, script generation, visual
editor, lettering, compositing, publishing, reader, export, application MCP server, backup/update flows, admin UI and
World link are incomplete or intentional stubs. Database tables or designs for a feature do not mean that feature is
available at runtime.

The active first milestone is deliberately smaller: connect OpenRouter for text or use the deterministic offline
placeholder, create a project and chapter, generate a structured script, then edit and persist it atomically. See the
[accepted MVP decision](docs/decisions/0001-mvp-scope.md), the
[hard remote-spend decision](docs/decisions/0003-hard-remote-spend-reservations.md),
and the [active execution plan](docs/plans/0001-script-vertical-slice.md).

## Why MangaVeyra

Most AI comic tools generate a flat picture per panel. MangaVeyra's product architecture instead treats a panel as
structured data: polygon geometry plus independent background, character, effects and lettering layers. Script,
dialogue, provenance and edit scope travel with the page so a creator can eventually change one part without
rerolling everything else.

The deployment target is one Go binary and a database, with optional Python workers for local AI. The open-source
application is designed to accept the operator's own models and keys, send no telemetry, and keep the complete
studio, reader and community self-hostable. These are repository invariants; the creation workflow is still being
implemented.

## Product direction

The following differentiators describe the intended product. They are **planned**, not claims about the current
pre-alpha runtime.

| Planned capability | Intended behavior |
|---|---|
| **Character Lock** | Score character consistency and fix a drifted character without replacing the rest of a panel. Reference-image guidance is the default; LoRA training is optional. |
| **Scoped edits** | Target a chapter, page, panel, layer or region while preserving everything outside the explicit scope. |
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

These commands start the current pre-alpha foundation for development and evaluation. Expect setup, authentication,
settings and an empty dashboard, not the planned creation workflow.

### Docker Compose (PostgreSQL)

```sh
git clone https://github.com/MaramHarsha/MangaVeyra.git
cd MangaVeyra
cp .env.example .env
# edit .env: POSTGRES_PASSWORD (letters and digits; it goes into MV_DB),
#            MV_PUBLIC_URL and MV_WORKER_TOKEN
docker compose --env-file .env -f deploy/docker-compose.yml up -d --build
```

Open <http://localhost:8080> and follow first-run setup. The provider connection controls shown during setup are not
implemented yet.

The Python worker can be started and exercised independently through its protocol and local CLI. The application does
not yet route Studio creation flows to it:

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

`seed --demo`, `backup` and `mcp-stdio` are command placeholders and currently report that they are unavailable.

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

The **server side** is not implemented yet: there is no working model catalogue, cloud-provider registry, routing
engine or provider-management UI in the application. The active milestone starts with deterministic structured text
and OpenRouter text generation before expanding to image providers and automatic routing.

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
| Server | Go `net/http`, embedded assets | Foundation implemented |
| Pages | templ + HTMX | Setup, auth, settings and UI kit implemented |
| Rich UI | TypeScript, Svelte 5, PixiJS | Build foundation and demo island only |
| AI workers | Python, PyTorch/diffusers, ComfyUI adapter | Implemented; GPU path needs hardware validation |
| Data | PostgreSQL or SQLite | Schema and migration runner implemented |
| Files | Local disk or S3-compatible storage | Implemented |
| Realtime | WebSockets and Yjs relay | Foundation implemented |
| Product modules | Studio, editor, publishing, reader, export, MCP | Mostly stubs |

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

The workflow in [`.github/workflows/ci.yml`](.github/workflows/ci.yml) runs for pull requests and can also be started
manually from the Actions tab.

## Privacy and security

- The implemented foundation has no telemetry or usage tracking.
- Passwords use argon2id; session tokens are stored hashed; 2FA, CSRF protection, strict CSP, rate limits, encrypted
  secrets and signed media URLs are implemented.
- Future provider, World and update code must remain opt-in. The `local_only` outbound block is a required contract
  for the routing layer now under development, not a claim that the unfinished router has already been validated.

## License

MangaVeyra is free software under the [GNU Affero General Public License v3.0](LICENSE): if you run a modified version
as a network service, you must offer its source code to its users. The API schemas, `.mvpack` format and plugin SDK in
[`sdk/`](sdk/LICENSE) are MIT. The MangaVeyra name and logo identify the official project; forks must not present
themselves as official MangaVeyra builds. Third-party notices are listed in [NOTICE](NOTICE).
