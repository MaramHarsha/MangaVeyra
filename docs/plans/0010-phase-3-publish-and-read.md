# Phase 3: Publish and read

- Status: Active
- Branch: `phase-3/publish-and-read`; base `d3fa6de` on `main`.
- Normative inputs: AGENTS.md, ADRs 0001–0012, the approved desktop handoff in `../MangaVeyra_All_Pages/`.

## Outcome

A creator publishes a chapter, and a reader on the same server follows the series, reads it (paged, spread,
webtoon or guided with alt-text and read-aloud) and downloads it for offline reading. The launch MVP is complete
after this phase; tagging v1.0.0 is an owner action after acceptance.

Out of scope: the D9 mobile editor and mobile artboards (deferred mobile pass), translation mode and EPUB/video
(Phase 5), admin/moderation UI, backup, import and update (Phase 4), the XP layer (deferred, ADR 0001), World.

## Screens (desktop artboards)

| Screen | Route | Owner |
|---|---|---|
| E1 Publish flow / Published | /studio/p/{project}/chapters/{n}/publish, …/published | publish |
| E5 Series page | /s/{series} | social |
| E3 Reader paged / spread / guided | /s/{series}/{chapter} | reader |
| E4 Reader webtoon (desktop, file 13) | /s/{series}/{chapter} | reader |
| E7 Library (desktop, file 13) | /library/{following\|history\|downloads\|bookmarks} | reader |
| F1 Creator profile | /u/{username} | social |
| E6 Explore | /explore | discover |
| E8 Search | /search?q= | discover |
| F2 Analytics | /studio/analytics[?series=] | analytics |
| A1 Landing | site/index.html, /about | site |

## Shared contracts (root-owned, frozen)

- `internal/publish/contract.go`: `publish.Service`, series/publication/page types, visibility, status and rating
  constants, `ErrNotFound`/`ErrInvalid`/`ErrConflict`.
- `internal/analytics/contract.go`: `analytics.Recorder` and event kinds.
- `internal/social/contract.go`: `social.Follows`.
- ADR 0012 decides access, frozen publications, provenance, alt-text, read-aloud, offline, analytics privacy,
  comments, notifications and scope.
- Existing schema (`0004_publish_social.sql`, `0006_search`, `comments` in `0002_studio.sql`) is reused; a new
  migration is root-owned and needs a failing test that proves it necessary.

## Milestones and ownership

Agents work in separate worktrees from the commit that adds this plan, edit only their files, make logical commits,
and return changed files, tests and ADR 0004 text. Root alone edits `docs/**`, `README.md`, migrations,
`internal/core/**`, `internal/app/**`, `internal/web/layout/**`, `go.mod`/`go.sum`, `web/package*.json` and `e2e/**`.

1. [x] **P — Publish.** `internal/publish/**` (service implementation, E1 flow and Published, series create/edit
   from a project, freezing pages through compose/lettering, alt-text drafting, cover choose/generate through the
   existing `cover.generate` API, scheduler loop, unpublish/update, follower notifications via `social.Follows`
   and `notify`), publish locales and styles.
2. [x] **R — Reader and library.** `internal/reader/**`, `web/src/islands/reader/**`, the service worker's offline
   chapter cache, reader locales and styles. E3 paged/spread/guided with tap zones and RTL/LTR, E4 webtoon, reading
   settings per series, progress/history, bookmarks, local-voice read-aloud, offline downloads, E7 library, event
   recording through `analytics.Recorder`.
3. [x] **S — Series page and community.** `internal/social/**`: E5 series page, follows (`social.Follows`), user
   follows, chapter and panel comments, reports, F1 creator profile; social locales and styles.
4. [x] **D — Discover and analytics.** `internal/discover/**` (E6 Explore with rising creators, genres and the
   hand-drawn filter; E8 search over the existing full-text index) and `internal/analytics/**` (`Recorder`, daily
   session hashing, 90-day aggregation, F2); their locales and styles.
5. [x] **W — Landing and install docs.** `site/**` (A1, static, no external requests) and `docs/install/**`.
6. [x] **Root integration.** Activate Explore/Library/Search navigation once routes exist, wire contracts, e2e
   (publish → follow → read guided → download offline), ADR 0004 records, README, full gates on SQLite and
   PostgreSQL, desktop design comparison, independent review.

## Acceptance and verification

- Phase done when: a creator publishes a chapter; a signed-in reader follows the series, reads it in guided mode with
  alt-text, and downloads it offline; an anonymous visitor on a public server can read public chapters and cannot
  read members/private/mature content; readers' copies stay frozen while the creator keeps editing.
- Gates: `gofmt -l cmd internal` empty, `go vet ./...`, `go test ./...` on SQLite and PostgreSQL 18, `templ generate`
  without drift, `go mod tidy` without drift, editioncheck, web check/test/build, worker pytest, the e2e suite.
- Desktop artboard comparison at 1440×900 for every Phase 3 screen, with remaining differences in ADR 0004.

## Risks

- Anonymous access must never expose members/private/mature content or reveal existence (404 everywhere).
- Frozen page rendering is CPU-heavy for long chapters; publishing runs as a job with progress.
- Read events must stay cheap, rate limited and privacy-preserving; failures never break reading.
- Service-worker caches must not serve content after access is revoked beyond the offline expiry.
