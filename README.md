<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="brand/logo/svg/lockup-color-on-dark.svg">
    <source media="(prefers-color-scheme: light)" srcset="brand/logo/svg/lockup-color-on-light.svg">
    <img alt="MangaVeyra" src="brand/logo/svg/lockup-color-on-light.svg" width="520">
  </picture>
</p>

<h3 align="center">Your story. Every panel.</h3>

<p align="center">
  The open-source, self-hosted AI manga studio, reader and community.<br>
  Write a premise — MangaVeyra drafts the script, keeps your characters on-model,<br>
  lays out the pages and letters them. Then you fix exactly the part you want, and publish.
</p>

<p align="center">
  <a href="LICENSE"><img alt="License: AGPL-3.0" src="https://img.shields.io/badge/license-AGPL--3.0-3D8BFF?style=flat-square"></a>
  <a href="https://github.com/MaramHarsha/MangaVeyra/releases"><img alt="Version 1.0.0" src="https://img.shields.io/badge/version-1.0.0-3D8BFF?style=flat-square"></a>
  <a href="go.mod"><img alt="Go 1.27" src="https://img.shields.io/badge/Go-1.27-00ADD8?style=flat-square&logo=go&logoColor=white"></a>
  <a href="#quick-start"><img alt="Self-hosted" src="https://img.shields.io/badge/self--hosted-yes-3CCB7F?style=flat-square"></a>
  <a href="#privacy-and-security"><img alt="No telemetry" src="https://img.shields.io/badge/telemetry-none-3CCB7F?style=flat-square"></a>
</p>

<p align="center">
  <a href="#quick-start">Quick&nbsp;start</a> ·
  <a href="#what-sets-mangaveyra-apart">What&nbsp;sets&nbsp;it&nbsp;apart</a> ·
  <a href="#features">Features</a> ·
  <a href="#models-and-providers">Models</a> ·
  <a href="#architecture">Architecture</a> ·
  <a href="docs/FEATURES.md">Feature&nbsp;plan</a>
</p>

---

## Why MangaVeyra

Most AI comic tools generate a flat picture per panel. Fixing one face means rerolling the whole panel, characters
drift between pages, lettering is an afterthought, and readers get scanned images with no text, no alt-text and no
honest label of what the AI did. MangaVeyra is built on one decision: **a panel is structured data, not a flat image.**
Background, each character, effects and lettering are separate layers with their own prompts; panel geometry, script
and dialogue travel with the page from the editor to the reader.

It is **one Go binary and a database**. Bring your own models and keys, or stay fully local. No account with us, no
usage tracking, no paywall — the open-source edition is the complete studio, reader and community.

## What sets MangaVeyra apart

| Feature | What it does |
|---|---|
| **Character Lock** | Train a character once. Every generated panel gets a consistency score; drifted faces are flagged on the canvas and fixed by regenerating only that character's layer. |
| **Fix one part, keep the rest** | Every AI action has a scope — chapter, page, panel, layer or region. Lasso a hand, describe the change, pick one of four variations; pixels outside the selection are copied back exactly. |
| **Layered panels** | Background, each character, effects and lettering regenerate independently. Bubbles and lettering are never touched by image edits. |
| **Auto model routing** | Each job goes to the best model you have — local GPU, OpenRouter, fal or a direct key — with the reason shown on the button and a one-click override. |
| **Panel-aware reading** | Guided panel-by-panel view on phones, alt-text and read-aloud written from the script, per-page AI provenance labels readers can filter by. |

## Features

**Create** — premise → arcs, chapters and a panel-by-panel script (camera, emotion, action, dialogue, SFX) · character
bible with reference sheets, outfits, voices and relationships · locations, props and style references · story and
layout templates (4-koma, action spread, splash, webtoon strip, custom grids) · sketch-to-manga.

**Edit** — three-column page editor on a WebGL canvas · change scope for every action · region edit with brush, lasso,
box or object selection · four-up variation picker (keys 1–4) · lettering engine with auto-fit, tails that follow
the speaker, right-to-left or left-to-right reading order and vertical Japanese · lock approved panels and pages ·
before/after compare and one-click revert · live cursors and panel comments · keyboard shortcuts and a command
palette · light editing on phones.

**Formats** — manga (right to left), comics (left to right) and vertical webtoons · export to PNG, print-ready PDF
(trim and bleed), EPUB, CBZ and 9:16 video · one-click translation and re-lettering with a creator glossary ·
animate mode (panels → short clips with character voices).

**Read** — paged, spread, guided and scroll modes remembered per series · tap zones, reading settings, bookmarks,
history and offline downloads (installable PWA) · series pages, follows, chapter and panel comments · explore with
rankings that surface newcomers (no pay-to-rank), search and recommendations · creator profiles and analytics.

**Run it yourself** — browser setup wizard with GPU detection · personal, private-group or public instances with
closed, invite-only or open sign-up · job queue with fair GPU sharing · admin and moderation · backups, restore and
full data export · CBZ, CBR, PDF and image-folder import · signed auto-updates with automatic rollback · an MCP
server so AI assistants can work in your projects, with limits.

**Next (v2)** — continuity checker · version history with branching pages · editor review mode · page ↔ webtoon
format reflow · living panels · reader-side translation · plugin directory with sandboxed plugins · public API and
OPDS feed · importing items you own from World.

The full plan — every feature classified as adopt, improve or innovate and scheduled as MVP, v2 or later — is in
[docs/FEATURES.md](docs/FEATURES.md).

## Quick start

### Docker Compose (PostgreSQL)

```sh
git clone https://github.com/MaramHarsha/MangaVeyra.git
cd MangaVeyra
cp .env.example .env
# edit .env: POSTGRES_PASSWORD (letters and digits; it goes into MV_DB),
#            MV_PUBLIC_URL and MV_WORKER_TOKEN
docker compose --env-file .env -f deploy/docker-compose.yml up -d --build
```

Open <http://localhost:8080> and follow the first-run setup: admin account, instance and sign-up mode, models, done.

Add an AI worker when you want local generation (it pairs with the server through `MV_WORKER_TOKEN`):

```sh
# NVIDIA GPU (diffusers backend; ComfyUI and vision helpers: see worker/README.md)
docker compose --env-file .env -f deploy/docker-compose.yml --profile gpu up -d --build
# No GPU: mock backend for trying things out
docker compose --env-file .env -f deploy/docker-compose.yml --profile worker-cpu up -d --build
```

No GPU? Connect API keys (OpenRouter, fal, OpenAI, Google, xAI, …) or a local text-model server such as Ollama from
**Models & providers** instead. Until any model is connected, a clearly labelled built-in placeholder model keeps
every flow working.

### Single binary (SQLite)

Single-user and small installs need nothing but the binary: SQLite and a local folder.

```sh
make build               # web assets + templ generate + ./bin/mangaveyra
./bin/mangaveyra serve   # SQLite in ./data, http://localhost:8080
```

Requirements to build from source: Go 1.27.1 or newer and Node.js 24 LTS (npm). The [templ](https://templ.guide) CLI
(`make tools`) is only needed when you change `.templ` files. FFmpeg (animate clips) and `pdftoppm` (PDF import) are
optional runtime helpers; the Docker image includes both.

### Command line

```text
mangaveyra serve                     Run the server
mangaveyra migrate [status]          Apply pending database migrations (or list them)
mangaveyra user create               Create an account (--username --email --name --password
                                       --role admin|creator|reader; no --password prints one)
mangaveyra user reset-password       Set a new password (--user <username|email> [--password])
mangaveyra user set-role             Change a site role (--user <username|email> --role …)
mangaveyra seed --demo               Load the demo content (Tidebreak Courier and friends)
mangaveyra mcp-stdio --token mva_…   Serve MCP over stdin/stdout for desktop assistants
mangaveyra backup create|list|restore
mangaveyra version
```

## Configuration

Everything is configured with `MV_*` environment variables, or a YAML file
(`mangaveyra serve --config mangaveyra.yaml`, or `MV_CONFIG=mangaveyra.yaml`). The most common:

| Variable | Default | Purpose |
|---|---|---|
| `MV_ADDR` | `:8080` | Listen address |
| `MV_DATA_DIR` | `data` | Database (SQLite), media, keys and backups |
| `MV_DB` | SQLite in the data dir | `postgres://…` to use PostgreSQL |
| `MV_PUBLIC_URL` | — | Public URL for links, emails, signed media URLs and the CSRF origin; `https://` turns on Secure cookies and HSTS |
| `MV_TRUSTED_PROXIES` | — | Reverse proxies (IPs/CIDRs) whose `X-Forwarded-*` headers are trusted, e.g. `127.0.0.1` behind Caddy |
| `MV_STORAGE` | `local` | `s3` for any S3-compatible store (`MV_S3_*`) |
| `MV_WORKER_TOKEN` | — | Shared secret that pairs AI workers started with Docker Compose |
| `MV_ALLOW_PRIVATE_PROVIDER_URLS` | `false` | `true` lets providers live on private or loopback addresses (Ollama, vLLM on your LAN); link-local and cloud metadata stay blocked |
| `MV_SMTP_*` | — | Email for password resets and notifications (optional) |
| `MV_MASTER_KEY` | — (uses `data/secret.key`, created on first run) | Encrypts API keys, World tokens and 2FA secrets at rest. Back up `secret.key` with your data |
| `MV_WORLD_URL` | `https://world.mangaveyra.org` | The optional World directory (off by default) |

See [.env.example](.env.example) for the complete, documented list.

## Models and providers

A model and a provider are separate: the same weights can come from the local worker, a vendor API or an aggregator.
MangaVeyra ships a catalogue of image, text, video, voice and OCR models as small manifest files, and the router picks a
model *and* a provider for every job.

- **Local** — the Python worker in [`worker/`](worker/README.md) runs diffusers pipelines, ComfyUI workflows,
  LoRA training for Character Lock and vision helpers on your own GPU. Nothing leaves the server.
- **APIs and aggregators** — OpenAI, Google, xAI, Anthropic (text), OpenRouter, fal, Replicate, Together, Runware and
  Hugging Face, with your own keys. Keys are encrypted at rest and never shown again after saving.
- **Custom connections** — any OpenAI-compatible endpoint (Ollama, vLLM, llama.cpp, LM Studio, LiteLLM), an exported
  ComfyUI workflow, a Hugging Face model id or a generic HTTP API — no code required.
- **Routing modes** — balanced, best quality, fastest, local only (no prompt ever leaves the server) and free only.
  A licence guard warns before a non-commercial model is used on a public series; a monthly spend limit caps remote
  costs on your own keys. Costs are shown as GPU time or estimated spend — never credits.

## Agents (MCP)

MangaVeyra is an MCP server. Claude, ChatGPT, Gemini, coding agents and local agents can connect to
`https://<your-server>/mcp` (or through `mangaveyra mcp-stdio` for desktop apps) to read scripts, draft chapters, place
bubbles or queue generations. Agents act with scoped tokens per project, anything above a cost threshold asks for
approval, every agent change is labelled and undoable in version history, and publishing, deleting, sharing and
unlocking always need a human.

## World (optional)

World is an optional directory at `world.mangaveyra.org` where creators can share characters, templates, style packs
and series listings from any MangaVeyra server. It is run separately by the MangaVeyra project and is not part of this
repository; only its API and the `.mvpack` pack format are open, under [`sdk/`](sdk/README.md) (MIT). World is **off by
default**; a server links to it with a device code and signs every submission with its own key, and nothing is ever
sent without someone pressing Submit. The open-source app never contains payment code — it shows World items as
*Free*, *Paid · on World* or *Owned*, never with a price.

## Architecture

| Part | Technology |
|---|---|
| Server | Go (`net/http`), one static binary with embedded assets |
| Pages | [templ](https://templ.guide) + HTMX, server-rendered — fast on phones, complete before JavaScript loads |
| Editor & reader | TypeScript + Svelte 5 islands, PixiJS WebGL canvas, SVG lettering layer |
| AI workers | Python (PyTorch, diffusers, ComfyUI adapter) pulling jobs over HTTP |
| Data | PostgreSQL or SQLite; the job queue and search live in the database — no Redis, no search server |
| Files | Local disk or S3-compatible storage |
| Realtime | WebSockets + Yjs for live cursors, comments and generation progress |
| Media | FFmpeg for animate clips |

```text
cmd/mangaveyra     server and CLI           internal/   core, auth, jobs, ai, studio, editor, …
web/               Svelte islands, CSS      worker/     Python AI worker
sdk/               MIT: APIs, .mvpack       deploy/     Dockerfiles, Compose, systemd, Caddy
docs/              architecture, plan       brand/      logo pack (SVG + PNG)
```

Read [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for the contracts every package follows and
[docs/SCREENS.md](docs/SCREENS.md) for how each designed screen maps to a route.

## Development

```sh
make tools        # install the templ CLI pinned in go.mod (and golangci-lint, optional)
make build        # web assets + templ generate + Go binary
make run          # build and serve on :8080 with SQLite, in dev mode
make test         # Go (SQLite), web (vitest) and worker (pytest) tests
make test-pg      # Go tests against a throwaway PostgreSQL container as well
```

The CI workflow in `.github/workflows/ci.yml` runs on demand (Actions → CI → Run workflow).

## Privacy and security

- **No telemetry, no usage data.** The server only calls out for what you turn on: remote providers, World, email and
  the update check.
- **Local only mode** blocks every remote provider, remote MCP server and World call.
- Passwords are hashed with argon2id; two-factor authentication, session management and a strict Content Security
  Policy are built in; API keys and tokens are encrypted or hashed at rest.

## License

MangaVeyra is free software under the [GNU Affero General Public License v3.0](LICENSE): if you run a modified version
as a network service, you must offer its source code to its users. The API schemas, the `.mvpack` format and the plugin
SDK in [`sdk/`](sdk/LICENSE) are MIT so anyone can build compatible tools. The MangaVeyra name and logo in `brand/`
identify the official project; forks must not present themselves as the official MangaVeyra. Third-party notices are
listed in [NOTICE](NOTICE).
