# ADR 0012: Publishing, reading and community on one server

- Status: Accepted for Phase 3
- Date: 2026-11-08
- Boundary: `internal/publish` (contract in `contract.go`), consumed by `reader`, `social`, `discover`, `analytics`

## Context

Phase 3 lets a creator publish a chapter and readers on the same server follow, read, comment and download it.
Readers include anonymous visitors on public servers, so access, provenance, privacy and offline behaviour must be
decided before the modules are built in parallel.

## Decisions

1. **Access.** Effective access is the intersection of the instance mode, series visibility, publication
   visibility/status and age rating. Every reader-facing read goes through `publish.Service`, which returns
   `ErrNotFound` for anything not visible, so existence is never revealed.

   | Instance mode | Anonymous visitor | Signed-in member |
   |---|---|---|
   | personal | nothing (sign-in page) | public, unlisted, members series |
   | private (group) | nothing (sign-in page) | public, unlisted, members series |
   | public | public and unlisted series | public, unlisted, members series |

   Private series are readable only by project members, as a preview. Unlisted series are never listed in Explore,
   search, profiles or feeds. Only `published` publications whose `published_at` has passed are readable; scheduled
   and unpublished ones are not. Mature and adult series are never shown to anonymous visitors; a signed-in reader
   confirms once per series (stored in `age_confirmations`, migration 0400, separate from reading preferences so a
   confirmation never pins a reading mode or direction). `publish.Service` returns such series to signed-in viewers;
   the reader and series page show the confirmation step until it is stored. Instance-wide content rules and filters arrive
   with the admin panel in Phase 4.
2. **Frozen publications.** Publishing renders each page in the chosen language through the existing compose and
   lettering engines into an image asset and freezes the guided-view data (panel geometry, reading-order bubbles,
   alt-text) in `publication_pages`. Later Studio edits never change what readers see until the creator explicitly
   updates the published chapter, which creates a new frozen copy.
3. **Human actions and scheduling.** Publishing, scheduling, updating and unpublishing are explicit human actions;
   agent and bearer tokens cannot perform them. A background loop moves due scheduled publications to published,
   idempotently across server processes.
4. **Provenance.** Each publication copies the chapter's per-role AI labels (script, art, lettering) at publish
   time; the series aggregates its publications. Labels appear on E1, E5 and the reader. The Explore "hand-drawn
   only" filter means every role is `human`.
5. **Alt-text.** Publishing drafts each panel's alt-text deterministically from the script panel (action, setting,
   characters) and its lettering in reading order, with no model call and no cost. The creator reviews and edits it
   in E1. If a model ever writes alt-text, the text carries AI provenance.
6. **Read-aloud.** The reader uses the browser Web Speech API with voices whose `localService` is true only. Network
   voices would send chapter text to a third party, so they are never used; when no local voice exists the control
   is disabled with an explanation.
7. **Offline downloads.** The service worker caches a publication's frozen page images and guided data under
   `mv-offline-<publication>`; the `downloads` table records them per member. Downloads respect access at download
   time, expire, and are removed by the existing "Clear offline downloads" action. Anonymous visitors cannot
   download.
8. **Analytics stay on the server.** Reading events are recorded server-side through `analytics.Recorder` only. No
   IP address, user agent or fingerprint is stored and nothing leaves the server. Anonymous visitors are counted by
   a salted session hash that rotates daily. Creators see aggregates for their own series only (reads, completion,
   drop-off by page, followers over time). Raw events older than 90 days are aggregated and deleted.
9. **Comments.** Public comments anchor to a chapter or a panel and are collapsed by default in the reader. Authors
   may edit or delete their own; the series owner may hide comments on their series. Reports go to the existing
   reports table for the Phase 4 admin panel. Comment and report writes are rate limited and require sign-in.
10. **Notifications.** Followers who asked to be notified receive an in-app notification (and email or push per their
    preferences) when a new chapter is published. No notification is sent for updates to an already published
    chapter.
11. **Scope.** Screens follow the approved desktop artboards. Phones get the responsive version of the desktop design
    using existing patterns; the D9 mobile editor and the unfinished mobile artboards are deferred to the
    end-of-project mobile pass (owner, 2026-11-07). Phase 3's phone acceptance is therefore checked for basic
    usability of the responsive reader, not against mobile artboards.
12. **Landing page.** `site/` is a static page with no external fonts, CDN scripts or analytics; it is deployable to
    GitHub Pages and uses only original MangaVeyra content.

## Consequences

Reader-facing modules depend only on `publish.Service`, `social.Follows` and `analytics.Recorder`, so they can be
built in parallel. Frozen publications cost storage per published language but make reading fast, cacheable and
stable while creators keep editing. Tagging v1.0.0 remains an owner action after the Phase 3 acceptance passes.

## Additions during implementation (2026-11-08)

- `publish.Series` carries `Creators` (from `series_creators`, owner first) and `Schedule`, a creator-written release
  note (migration 0401), because E5 and F1 credit co-creators and show it.
- Daily read-event aggregates live in `read_event_days` (migration 0402); the roll-up claims each raw day with
  `DELETE … RETURNING`, so two server processes never count a day twice and raw events are never deleted without
  being aggregated.
- `publish` notifies followers through `publish.FollowerSource`, a one-method view of `social.Follows` that the
  social module also provides, because publish cannot import social without an import cycle. Every follower is
  re-checked with `CanRead` and an active account before a notification is sent.
- The reader owns age confirmations through the leaf package `internal/reader/agegate` (`POST
  /s/{series}/confirm-age`); the series page and the reader show the step until a confirmation is stored, and the
  reader's chapter API and writes return 404 until then.
- Publication IDs stay stable when a published chapter is updated; panel comments and bookmarks anchor to the
  publication plus page and panel order, and anchors that no longer exist after an update are shown at chapter level.

## Review follow-ups (2026-11-08)

- **Series and chapter listing (§1).** A series is shown to readers outside its project only while they can read at
  least one of its chapters: a private chapter never publishes a series, and unpublishing every chapter hides it again.
  Unlisted chapters open by their link but are left out of a series' chapter list (except for project members); in
  listed views a series counts only chapters that are not unlisted. The series aggregates (languages, AI labels, first
  publish time) come from its chapters that are not private (§4).
- **Publishing decisions (§3).** Publish jobs cannot be retried through the jobs API, by owners or admins; the creator
  publishes again from E1. A publish applies only the chapter and series revisions E1 showed; if either changed, or a
  publish of that chapter and language is already running, nothing is applied. When a scheduled chapter comes due, the
  user who scheduled it must still be an owner or editor with an active account; otherwise the chapter becomes
  unpublished and nobody is notified.
- **Notifications (§10).** No notification is sent for unlisted chapters.
- **Comments (§9).** Comments on mature and adult series wait for the reader's age confirmation, like the series page
  and the reader; the owner previews without it.
- **Search engines.** Series pages and reader pages of anything not public send `X-Robots-Tag: noindex`.
- **Offline downloads on shared devices (§7).** Each offline chapter's meta entry records the member who downloaded it.
  Signing out deletes every offline chapter on the device before the sign-out request is sent. A page fresh from the
  server that states who is signed in (`data-user`, or `data-signed-out` for a known visitor) deletes chapters
  downloaded by anyone else, chapters without an owner, expired chapters, and every chapter when nobody is signed in;
  pages that do not know who is asking, and pages the service worker served from a cache (`data-offline-copy`), are
  skipped. The service worker never serves a chapter without a valid expiry. Without JavaScript, signing out cannot
  clear offline chapters; they go on the next page load with JavaScript.
- **Anonymous reading sessions (§8).** The `mv_rs` session is minted and signed by the server (a random ID plus an
  HMAC under a key derived from the master key); values a client chooses are ignored. Every new anonymous session counts
  against a per-address limit of 20, then one a minute, kept in memory only; a visitor over the limit still reads but is
  not counted. The session-hash salt is 32 random bytes per UTC day in `analytics_day_salts` (migration 0403), shared by
  all server processes and deleted once the day is over, so past hashes cannot be recomputed even with the master key.
  Opens and completions are stored once per reader, publication and UTC day through a deterministic row ID. F2 is
  limited to the series owner and project members with a creator role; project viewers see no analytics.
  `analytics.ReadingSessions` is a contract addition.
- **Unlisted chapters in the reader (§1).** The reader never lists an unlisted chapter: Episodes, previous/next and the
  chapter-end card skip it, and the library's new-chapter badge offers the newest listed chapter. A member who read,
  bookmarked or downloaded an unlisted chapter by its link keeps it in the library.
