# MangaVeyra architecture

This is the contract every part of the codebase follows. Read it before changing code.
Design references live outside the repo for the build team (see *Design references* at the end).

MangaVeyra is the **open-source, self-hosted** AI manga studio, reader and community (repository 1, AGPL-3.0).
The closed World marketplace (world.mangaveyra.org, repository 2) is **not** in this repository; this app only
talks to it through the small public World API documented in `sdk/api/`.

## Non-negotiable product rules

1. **No payment UI, ever.** No prices, checkout, credits, wallets, tips, subscriptions, payouts or "upgrade"
   prompts anywhere in this repo. Cost is shown as GPU time (local) or estimated spend on the user's *own* API keys
   (the *compute meter*). World items are labelled `Free`, `Paid · on World` or `Owned`, never priced.
   `internal/editioncheck` has a test that fails the build if forbidden words appear in UI templates or locale files.
2. **A panel is structured data, not a flat image.** Pages have panels (polygon geometry), panels have layers
   (background, one per character, FX, lettering), bubbles are text data on the lettering layer.
3. **Change scope.** Every AI action targets exactly one scope: chapter, page, panel, layer or region. Nothing outside
   the scope is modified. Region edits copy outside pixels back exactly. Image edits never touch lettering.
4. **Locks.** Locked panels/pages cannot be changed by any AI, batch or agent action (enforced server-side in one
   place: `studio.CheckScopeWritable`). Unlocking always needs a human.
5. **Honest provenance.** Every chapter carries a per-role AI label (script / art / lettering: human, AI-assisted, AI).
6. **Local only mode** guarantees no prompt or image leaves the server (router + MCP client + World are blocked).
7. **Telemetry off.** Nothing is sent anywhere unless the admin connects a remote provider or links World.
8. **Server-rendered pages arrive complete.** Skeletons are only for AI results. Everything works without JS except
   the rich islands (editor, reader, pose tool, animate timeline).
9. **Accessibility.** WCAG AA contrast, visible focus (2 px ring, 2–3 px offset), reduced-motion support (OS setting +
   user toggle), status never by colour alone (icon + word), 44–48 px touch targets on mobile.
10. **Original content only** in samples, seeds, tests and docs: no existing titles, characters, artwork or brands.
    The demo world is *Tidebreak Courier* (Rin Asagiri, Kaito Mbeki, Old Wren, Sora Vell, Pip the Gull) and the
    other titles used in the design (Glass Orchard, Ninth Bell, Low Tide Radio, Lanterns of Oru, Paper Crows,
    Iron Koi, Salt & Static, Harbour Ghosts, Moth & Mortar…).

## Stack

| Part | Choice |
|---|---|
| Core server | Go (module `github.com/MaramHarsha/MangaVeyra`, `go 1.27.1`, the latest stable toolchain), `net/http` with Go 1.22+ pattern routing, one static binary (`CGO_ENABLED=0`), all web assets embedded with `embed.FS` |
| Server-rendered screens | [templ](https://templ.guide) components + HTMX 2 (vendored, no CDN). Generated `*_templ.go` files are committed so `go build` works without the templ CLI |
| Rich screens | TypeScript + Svelte 5 islands built by Vite: editor (PixiJS 8 WebGL canvas + SVG lettering layer), reader, pose tool, animate timeline |
| Realtime | One WebSocket per tab at `/ws` (github.com/coder/websocket): topic subscriptions for jobs, notifications, presence; Yjs updates relayed per page document |
| AI workers | Python 3.14 package in `worker/` (3.11+ supported) (diffusers / ComfyUI adapter / built-in mock). Workers *pull* jobs from the Go server over HTTP and stream progress back |
| Text models | Any OpenAI-compatible endpoint (Ollama, vLLM, llama.cpp, LM Studio, LiteLLM) or vendor keys (Anthropic, OpenAI, Google, xAI) called from Go |
| Data | PostgreSQL 18 (16+ works) or SQLite (modernc.org/sqlite, pure Go) through `database/sql` + `sqlx`. Job queue and search live in the database — no Redis, no search server |
| Files | Local disk or any S3-compatible store (minio-go) behind `storage.Store` |
| Media | FFmpeg (optional binary on PATH) for animate clips and living-panel loops; `pdftoppm` (optional) for PDF import |
| Agents | MCP server built into the binary (official Go SDK `github.com/modelcontextprotocol/go-sdk`), streamable HTTP at `/mcp` + `mangaveyra mcp-stdio` bridge |
| Mobile | Installable PWA (manifest + service worker, offline chapters) |
| Icons & fonts | Tabler Icons webfont (MIT) and Anton, Hanken Grotesk, IBM Plex Mono, Comic Neue, Bangers, Noto Sans JP (OFL) — all self-hosted via npm packages, never loaded from a CDN |

## Repository layout

```
cmd/mangaveyra/          CLI entry: serve, migrate, seed, mcp-stdio, user (create/reset-password), backup, version
cmd/worldstub/           DEV ONLY fake World API server for tests and local development (not the real platform)
internal/
  core/                  Deps container, Module interface, Router + middleware helpers, errors, context keys (leaf package)
  app/                   Wiring: config → deps → modules → http.Server; background loops; graceful shutdown
  config/                Env + optional YAML config file (MV_* variables); data dir layout
  db/                    DB wrapper (sqlx), dialects, Rebind, tx helpers, embedded migrations + runner, ULID ids
  db/migrations/         NNNN_name.sql (portable) or NNNN_name.sqlite.sql / NNNN_name.postgres.sql (dialect-specific)
  auth/                  users, sessions, argon2id, TOTP 2FA, invites, password reset, roles, API/agent/worker tokens, middleware
  settings/              instance settings (key/value JSON) + per-user preferences
  i18n/                  message catalogs (locales/<lang>/<module>.json), locale negotiation, RTL, T() helper
  storage/               Store interface; local disk + S3; signed media URLs; image helpers (sniff, decode limits, thumbnails)
  secrets/               AES-256-GCM box for API keys and World tokens (master key from MV_MASTER_KEY or data/secret.key)
  jobs/                  queue (enqueue, claim, progress, cancel, retry, fair share), Go executor pool, handler registry, events
  ws/                    WebSocket hub: topics, presence, Yjs relay + persistence
  notify/                notifications (in-app, email via SMTP, web push), preferences, quiet hours
  activityui/            session-only G9 queue and G4 notification screens over the jobs/notify foundation services
  web/                   templ UI kit (ui/), layouts (layout/), page helpers, static assets (static/, embedded), Vite manifest
  studio/                projects, members & roles, chapters, arcs/scenes, script panels, pages, panels, layers, bubbles,
                         characters (refs, outfits, relations, voice), setting items, templates, wizard, story editor,
                         scope & lock rules, versions & branches
  editor/                JSON API for the editor island (page documents, mutations, variations, region edit apply)
  lettering/             bubble layout engine (auto-fit, tails, reading order, vertical JP), SVG + raster rendering
  compose/               page compositing (panel polygons + layers → page image), thumbnails
  ai/                    manifest format + embedded catalogue, provider registry, adapters, router (modes, pins, guards,
                         explain, fallback), compute meter, spend tracking
  gen/                   generation job handlers: script, panel, layer, region, variations, character sheet/train/score,
                         sketch-to-manga, cover, continuity, alt-text, upscale
  translate/             chapter translation, glossary, per-bubble review, overflow detection
  animate/               animate mode (9:16 clips via FFmpeg), living panels (v2)
  publish/               series, chapter publishing per language, schedule, visibility, age rating, licence, AI labels
  reader/                reader pages + island data, progress, bookmarks, history, downloads manifest, OPDS feed
  social/                follows, comments (chapter + panel anchored), reports, notifications fan-out
  discover/              explore shelves, rankings (rising/week/all-time), search with filters, recommendations
  analytics/             read events, completion/drop-off by page, followers over time
  xp/                    levels, XP events, E–S ranks, quests (can be hidden per user)
  export/                PNG, print-ready PDF (trim/bleed), EPUB fixed layout, CBZ, 9:16 video, credits page
  importer/              CBZ, CBR, PDF, image folders → projects (panel/bubble detection via worker, fallback whole page)
  backup/                scheduled backups, restore, export-everything archive, per-user data export
  update/                release check, signed download verify, backup → migrate → health check → cleanup / rollback
  admin/                 reports, users, AI-label disputes, content rules, server status, maintenance mode, invites
  setup/                 first-run wizard, GPU detection, recommended local models
  mcp/                   MCP server tools/resources/prompts, agent tokens, approvals, agent action labels
  world/                 World client: device-code link, server signing key, names, submissions, catalogue, My World library
  packs/                 .mvpack read/write + signature (spec and schema under sdk/, MIT)
  plugins/               plugin directory: manifests, install/enable, WASM sandbox (v2)
  editioncheck/          test-only package guarding rule 1
  seed/                  demo content (placeholder art) for `mangaveyra seed --demo`
  testutil/              NewTestApp, fixtures, login helpers, fake providers
web/                     frontend source: package.json, vite.config.ts, src/{styles,htmx,islands/*,lib}
worker/                  Python AI worker (pyproject.toml, mangaveyra_worker/, tests/), Dockerfile.gpu
sdk/                     MIT-licensed: api/ (OpenAPI for public + World + worker APIs), mvpack/ spec + JSON schema,
                         plugin SDK stubs; LICENSE (MIT)
deploy/                  docker-compose.yml, Dockerfile, Dockerfile.worker, systemd unit, Caddy example
e2e/                     Playwright end-to-end flows against a built binary
site/                    static project website (landing page A1), deployable to GitHub Pages
docs/                    ARCHITECTURE.md, FEATURES.md, SCREENS.md, install/, admin/, api/, contributing docs
brand/logo/{svg,png}     logo pack
```

## Core contracts (Go)

### Module
```go
// package core
type Module interface {
    Name() string
    Routes(r *Router)            // register HTTP routes
}
type Initer interface { Init(context.Context) error } // cross-module lookup after construction
type Starter interface { Start(ctx context.Context) error } // optional background loops
type JobHandlers interface { JobHandlers() map[string]jobs.Handler } // optional Go-executor job handlers
```
Each feature package exposes `func New(d *core.Deps) *Module`. `internal/app/modules.go` lists every module once.
Feature packages never import `internal/app`. Shared foundation services such as `d.Jobs` and `d.Notify` are fields;
feature services use the type-keyed registry. Providers call `core.Provide[studio.Service](d, service)` in `New`, and
consumers call `core.MustUse[studio.Service](d)` in `Init` or a handler. Register and request the interface itself,
never a pointer to an interface. All modules are constructed before any `Init` runs, avoiding dependency-order
lookups while keeping imports acyclic.

### Router and handlers
* `core.Router` wraps `http.ServeMux`: `r.Handle("GET /studio/projects", h, core.RequireUser)`.
  Helpers: `r.Page`, `r.API`, `r.Group(prefix, mws...)`.
* Middleware order: request id → recover → logging (slog) → security headers/CSP → maintenance gate → setup gate
  (redirect to `/setup` until an admin exists) → session/user → locale → CSRF (`http.CrossOriginProtection` +
  SameSite=Lax cookies) → rate limit (auth + API) → handler.
* HTML: `web.Render(w, r, status, component)`. If the request has `HX-Request` and the handler provides a fragment,
  render only the fragment; otherwise wrap in the page layout. Use `HX-Redirect`/`HX-Trigger` headers for flows.
* JSON: `/api/v1/...` (session cookie or bearer token), errors as `{"error":{"code":"...","message":"..."}}`.
* Auth levels: `core.RequireUser`, `core.RequireRole("admin")`, `studio.RequireProjectRole("editor")`.

### Data
* IDs are lowercase ULIDs (`db.NewID()`), stored as `TEXT`. Public URLs use slugs for series and usernames.
* All times UTC `TIMESTAMP`. JSON columns are `TEXT` holding JSON (use `db.JSON[T]` scanner). Booleans `BOOLEAN`.
  Binary `BLOB` in portable migrations is rewritten to `BYTEA` for Postgres by the migration runner.
* Write queries with `?` placeholders; `db.Rebind` converts for Postgres. Use `RETURNING` and `ON CONFLICT` (both dialects).
* Migrations: `internal/db/migrations`. Numbering ranges keep parallel work conflict-free:
  0001–0099 foundation (core + full base schema) · 0100 studio · 0200 ai/gen · 0300 editor/lettering/compose ·
  0400 publish/reader · 0500 social/discover/analytics/xp · 0600 admin/setup/update/backup · 0700 mcp ·
  0800 world/packs/plugins · 0900 export/import · 1000 translate/animate/collab.
* Tests run on SQLite by default; set `MV_TEST_POSTGRES_URL` to also run against Postgres (CI does).

### Jobs and AI
* `jobs.Job` has: `type`, `scope` (`{kind, project_id, chapter_id, page_id, panel_id, layer_id, region}`), `user_id`,
  `state` (`queued|running|done|failed|canceled`), `progress` 0–1, `step/steps`, `executor` (`go|worker`), `model_id`,
  `provider_id`, `decision` (router explanation JSON), `input`, `output`, `error`, `estimate` and `actual`
  (`{gpu_seconds, usd}`), `attempts`, `parent_id`, `priority`, timestamps.
  Scope fields are a strict per-kind allow-list, not caller-trusted ancestry. In particular, region scope contains a
  project ID, unique layer ID and non-null region object; Studio resolves layer → panel → page → chapter → project,
  verifies the project and every lock at enqueue and persistence.
* Enqueue validates scope + locks first. Fair share: per-user running limit (admin setting, default 2); night mode
  can defer batch jobs.
* Browser Studio pages and manual retry require a signed-in session. Bearer job reads require `projects:read`,
  bearer cancellation requires `projects:write` plus `generate`, and both per-job access and lists enforce the
  token's project allow-list. Aggregate job stats and manual retry are not bearer-token operations.
* Go executor: `jobs.Handler func(ctx, *jobs.Run) error` registered by job type (remote APIs, text LLM, exports,
  compositing). Worker executor: Python workers call `/api/worker/v1` (`register`, `claim`, `progress`, `upload`,
  `complete`, `fail`, `heartbeat`) with a worker token. Progress is broadcast on WS topics `job:{id}`, `user:{id}`,
  `project:{id}`.
* Failures: the router's fallback moves the job to the next candidate and records the switch in the job log.
* Router pipeline: **read the job** (panel structure: characters + refs, text in art, B/W, aspect, resolution, edit vs
  new) → **filter** (installed / has key, VRAM, reference or LoRA support, content rating, licence vs series
  visibility, mode) → **score** (task quality, speed, cost; per-server keep/reroll learning in v2) → **explain**
  (`Auto → Qwen-Image Edit · local · ~40 s`, alternatives, why skipped) → **fall back**. Modes: `balanced`, `best`,
  `fastest`, `local_only`, `free_only`. Pins per project / character / panel type. Guards: licence guard, monthly
  remote spend limit, users-may-add-providers.
* Models are manifests (`internal/ai/catalog/*.yaml`, overridable in `data/manifests/`): id, name, maker, kind
  (image/text/video/voice/ocr/upscale), capabilities, licence (commercial ok?), VRAM, per-provider model ids and
  published prices, category scores. New models arrive as manifest files or plugins, no release needed.
* Adapters (Go): OpenAI-compatible (text + images), OpenAI, Google Gemini/Vertex images, xAI, OpenRouter (text +
  unified image API), fal, Replicate, Together, Runware, Hugging Face endpoints, ComfyUI workflow (input mapping),
  generic HTTP (request template + response path, sync/poll), Anthropic (text), Ollama, local worker, and the
  **built-in placeholder** provider (labelled screentone images + template text) so every flow works with no model.
* Keys are encrypted at rest (`secrets`), never shown after saving, scoped server-wide (admin) or per user.

### Realtime
Client connects to `/ws`, sends `{"op":"sub","topic":"job:01H..."}`. Server pushes `{"topic","type","data"}`.
Page documents use topic `doc:page:{id}` carrying Yjs sync/awareness messages (binary frames), relayed to peers and
appended to `page_doc_updates`; clients send a compacted snapshot periodically.

### Frontend
* Tokens are CSS variables (`--mv-*`, see *Design system*) shared by templ pages and Svelte islands. Theme via
  `data-theme="dark|light"` on `<html>` (default dark "Gate night"; "Paper" light; "system").
* CSS lives in `web/src/styles/` (hand-written, BEM-like classes `mv-btn mv-btn--primary`), bundled by Vite together
  with HTMX, fonts and icons into `internal/web/static/dist/` (git-ignored; `make web` builds it). Go resolves hashed
  names through the Vite manifest.
* Islands mount on `<div data-island="editor" data-props='{"..."}'>`; `web/src/islands/<name>/main.ts` entries.
* One responsive DOM per page: desktop 1440 layouts collapse to the 390 mobile layouts via CSS (sidebar → bottom nav
  with *Create* as the accent action, tables → lists, modals → bottom sheets).
* Motion is CSS-first and every animation has a reduced-motion fallback (`@media (prefers-reduced-motion)` and
  `html[data-motion="reduce"]`).

### i18n
`internal/i18n/locales/<lang>/<module>.json` (flat `key → text`, ICU-lite `{name}` placeholders). English is complete;
Japanese ships as the second language. `dir="rtl"` for Arabic/Hebrew mirrors layouts. Templates call `i18n.T(ctx, "key")`.

### Security baseline
argon2id passwords · TOTP 2FA · session tokens stored hashed · CSRF protection · strict CSP (`default-src 'self'`) ·
upload sniffing + size limits + image decode limits · SSRF guard for user-supplied provider URLs (block link-local /
metadata ranges unless admin allows private networks) · rate limits · audit log of admin and agent actions ·
secrets encrypted at rest · MCP/agent tokens scoped per project with approval thresholds, and publish / delete /
share / unlock always require a human.

## Design system (summary — full spec in the design project, file 04)

Colours (Gate night / Paper):
`--mv-bg #070A12/#E8EEFF`, `--mv-surface #0D1322/#F7F9FF`, `--mv-surface-2 #141C30/#DCE3F3`, `--mv-line #22304F/#C9D3EA`,
`--mv-text #E8EEFF/#070A12`, `--mv-text-2 #A3B3D9/#3E4A66`, `--mv-text-3 #7C8DB5/#5A6788`, `--mv-accent #3D8BFF` (ink
text `#070A12` on it, both themes), `--mv-acc-text #6AA8FF/#2459C9`, `--mv-success #3CCB7F/#1F7A4A`,
`--mv-warning #F5B83D/#8A5A00`, `--mv-error #FF5C7A/#C0264A`, `--mv-info #4DA8FF/#1F5FBF`, `--mv-focus #E8EEFF/#070A12`,
editor canvas `#04060C/#DCE3F3`. Brand: vermilion `#E8452C` stays on the logo slash; in-app use the `*-gate-blue` logos.

Type: Anton (display, uppercase: 96/0.9, 56/0.95, 32/1) · Hanken Grotesk (H1 28/1.2/700, H2 20/1.3/600, body 16/1.55,
UI 14/1.5/500) · IBM Plex Mono (meta, costs, ids 11–12, uppercase labels letter-spacing .05–.06em) · Comic Neue 700
(bubbles, uppercase) · Bangers + Noto Sans JP 900 (SFX).

Spacing 4/8/12/16/24/32/48/64/96. Radius 0 (panels, frames), 2px (buttons, inputs, badges), 14–18px (speech-bubble
toasts/tooltips), 999px (toggles, avatars, chips). Elevation: editor borders only; menus/toasts `0 12px 32px
rgba(0,0,0,.55)`; primary button glow `0 0 0 1px rgba(106,168,255,.35), 0 0 22px rgba(61,139,255,.45)`; status window
`0 0 0 1px #3D8BFF, 0 0 36px rgba(61,139,255,.4)` + 12–14 px corner brackets. Glow only on the primary action, focus,
status windows and milestones — never on body text, artwork or the editor canvas.

Motion: slash 420 ms `cubic-bezier(.7,0,.2,1)` · panel reveal 320 ms/panel, 60 ms stagger `(.2,.8,.2,1)` · ink-fill
loader tied to real progress · impact burst 600 ms `(.2,.9,.3,1.2)` · page turn 480 ms `(.6,0,.3,1)` with View
Transitions. Reduced motion: instant / 120 ms fade / static bar / static badge / 150 ms cross-fade.

Components: buttons (primary, secondary, ghost, danger, quiet, icon; 32/40/48), inputs/textareas/selects with error
icon + message, toggles, checkboxes, radios, sliders (diamond thumb), segmented controls, tabs (3 px accent underline),
badges (status + provenance), avatars with online ring, cards (project with angled bottom edge, series 3:4 cover,
character profile with kanji watermark + Character Lock score, chapter row), modals (seal shadow), drawers, dropdowns,
speech-bubble tooltips and toasts, top bar, studio sidebar (slanted active marker), mobile bottom nav, compute meter,
routing chip, generation states (queued, generating ink-fill, ready, failed + retry), variation picker (keys 1–4),
status window, rank badges E–S, XP bar, level-up toast, screentone skeleton, empty and error states with SFX words
(no mascot: ink splashes, speed lines, SFX lettering).

## Build and test

Phase 2 adds `gen.ControlService` for atomic page generation, selected-character repair, optional worker training
and attributable learned assessments. Migration 0202 stores current asset/reference/method evidence separately
from historical page percentages. D4/D10/D12 are documented rich editor islands; C3 remains server rendered with
ordinary POST forms. See [ADR 0010](decisions/0010-phase-2-scoped-control-and-character-evidence.md) for their fences,
offline-model rules and remaining quality acceptance. `cmd/phase2check` is an explicitly gated, temporary-data live
acceptance harness and is not part of normal application startup or routing.

```
make tools     # templ, golangci-lint (optional)
make web       # npm ci && vite build → internal/web/static/dist
make generate  # templ generate
make build     # web + generate + go build ./cmd/mangaveyra
make test      # go test ./... (SQLite) + npm test (vitest) + worker pytest
make e2e       # Playwright flows against ./bin/mangaveyra with the placeholder provider
make run       # ./bin/mangaveyra serve (SQLite in ./data, :8080)
```
CI (`.github/workflows/ci.yml`): Go build/vet/test on SQLite and Postgres, templ drift check, web build + vitest,
worker pytest, e2e smoke, licence scan (no GPL files under `sdk/`), edition check.

## Design references (build team only)

The complete design (184 labelled screens, desktop 1440 + mobile 390, 16 clickable flows) lives in the design project.
Every screen is labelled `ID – Name – Platform` and is mapped to a route in `docs/SCREENS.md`.
