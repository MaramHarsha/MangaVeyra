<!-- SPDX-License-Identifier: MIT -->
# MangaVeyra APIs

Three HTTP APIs touch a MangaVeyra server. The descriptions in this directory are licensed MIT
(see `sdk/LICENSE`) so anyone can build clients and compatible servers.

| API | Who calls whom | Description | Status |
|---|---|---|---|
| **World API v1** | A self-hosted server → World (world.mangaveyra.org) | [`world.openapi.yaml`](world.openapi.yaml) (OpenAPI 3.1) | Stable contract; dev stub in `cmd/worldstub` |
| **Worker protocol v1** | An AI worker → a MangaVeyra server (`/api/worker/v1`) | [`worker.md`](worker.md) | Stable contract; reference worker in `worker/` |
| **App REST API v1** | Scripts, apps, agents → a MangaVeyra server (`/api/v1`) | Generated and documented by the app (`docs/api/`) | Grows with the app |

All three use JSON over HTTPS and the same error envelope:

```json
{ "error": { "code": "validation_failed", "message": "Some fields are not valid.", "details": [ … ] } }
```

(OAuth endpoints use the RFC 6749 `{"error", "error_description"}` shape instead, as the RFCs require.)

## World API

The small public API through which a self-hosted server links to a World account and shares items. It is the
**only** way the AGPL app talks to the closed World platform.

* **Linking** — OAuth 2.0 device authorization grant (RFC 8628): `POST /oauth/device_authorization`, then poll
  `POST /oauth/token`; the person approves the code on World with their World account and 2FA. Refresh tokens
  rotate; `POST /oauth/revoke` unlinks. Scopes: `profile`, `servers`, `submit`, `library`.
* **World name** — `GET /v1/me`, `PATCH /v1/me/profile` (display name 1–40; handle 3–30 of `a–z 0–9 -`, unique,
  changeable once every 30 days, old handles reserved for six months and redirected, reserved and blocked names
  rejected with a reason), `GET /v1/handles/{handle}` for live availability.
* **Server keys** — `POST /v1/servers` registers the server's ed25519 public key and returns its key id;
  `DELETE /v1/servers/{id}` revokes it instantly. Every submitted pack is signed with a registered key.
* **Submissions** — `POST /v1/submissions` (multipart: signed manifest, declarations, the `.mvpack`), then
  `GET /v1/submissions[/{id}]` for automated checks (`files`, `malware`, `safety`, `similarity`,
  `known_character`, `signature`, `licence`) and the review status (`in_review`, `changes_requested`, `approved`,
  `rejected`, `held`) with the reviewer's message; `POST /v1/submissions/{id}/appeal`, once per decision.
* **Catalogue** — `GET /v1/catalog` (kind `character|template|style|series|plugin`, text search, licence,
  `works_with`, `hand_drawn_only`, cursor pagination) and `GET /v1/catalog/{id}`. Items carry
  `availability: free | paid | owned` and **never a price**; a paid item links to its World page.
* **Library** — `GET /v1/library` (items the account owns, each with a licence token), `POST /v1/library` to claim a
  free item, `GET /v1/library/{id}/download` for a short-lived download URL plus licence token.
* **Keys and meta** — `GET /v1/keys` publishes World's pack-signing and licence-token keys; `GET /v1/meta` describes
  the deployment (the dev stub answers `"stub": true`).

Every response carries `RateLimit-Limit`, `RateLimit-Remaining` and `RateLimit-Reset`; a `429` adds `Retry-After`.

**Rules for clients.** World is off by default. A server never calls the World API in local-only mode, never
sends anything without a person pressing *Submit*, and never sends prompts, drafts or project history. No telemetry.

**Developing against it.** Run the development stub — a fake, in-memory implementation that is *not* the real
platform — and point the app at it:

```sh
go run ./cmd/worldstub --seed --auto-approve          # http://127.0.0.1:8090, control panel at /_stub
go run ./cmd/worldstub --seed --review-delay 30s      # approve clean submissions after 30 s
```

`/_stub` shows pending device codes, submissions (with a manual review form), accounts and the seeded catalogue;
`POST /_stub/review` with `{"submission_id", "decision", "message"}` decides a submission from a script.
`python3 sdk/api/conformance.py` validates responses recorded by the stub's tests against this OpenAPI document.

## Worker protocol

AI workers (the Python package in `worker/` or any other implementation) **pull** jobs from a MangaVeyra server:
register, heartbeat, claim (long poll), stream progress and previews, upload results, complete or fail. The server
never connects to a worker, so workers can run behind NAT on a home GPU or a rented one. See [`worker.md`](worker.md).

## App REST API

A MangaVeyra server's own API under `/api/v1` — projects, chapters, pages, panels, characters, jobs, series,
reader data, and the OPDS feed — used by the editor islands, scripts, other apps and MCP agents with a session
cookie or a scoped bearer token. It follows the same conventions (JSON, error envelope, cursor pagination,
rate-limit headers) and is documented by the app itself under `docs/api/`. Change scope and lock rules apply to every call, and publish, delete, share and unlock
always require a human.
