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
