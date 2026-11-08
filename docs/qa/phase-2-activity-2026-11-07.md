# Phase 2 activity: desktop design and runtime verification

Dates: 2026-11-07–08. Base: merged Phase 1, `12041ed`. Contract:
[plan 0006](../plans/0006-phase-2-consistency-and-control.md), milestone 1 only.

## Implemented behavior

- G9 `/queue` uses the real jobs service. Members see their own jobs; admins see the server queue. Cursor pages are
  bounded to 20 rows. Rows expose status, actual progress, member, model/provider and recorded compute metadata.
  Details expose at most 50 sanitized event kinds/timestamps. Prompt/input/output payloads, artifacts, raw worker
  errors and secrets are excluded. Failed work returns to Studio for reviewed retry; the queue never replays it.
- Cancellation distinguishes a queued cancellation from a running job's pending stop request. Admin worker
  pause/resume uses existing drain semantics and explicit named actions for multiple workers. The state update
  atomically refuses to turn an offline worker into a fictitious online one.
- G4 `/notifications` uses the real notification service, four designed category filters, bounded cursor pages,
  explicit per-notification read/open and Mark all read. GETs do not mutate read state. Other members' records are
  inaccessible; unsafe/external or unbuilt destinations return to the notification category. The shared bell keeps
  its visible dot and localized accessible unread count synchronized.
- Failure notification delivery no longer copies raw worker/provider error messages. G4 also replaces legacy raw
  failure bodies with safe recovery copy. New job-notification titles use the recipient's current locale. Queue
  details map stable failure codes. Existing policy remains: failed
  jobs and completed batch jobs notify; ordinary successful single jobs stay quiet.
- The D8 Open queue control, shell compute meter and notification bell now reach implemented routes. Existing
  navigation order and unavailable Soon destinations remain intact.

## Desktop artboard comparison

Opened and captured the approved G9/G4 desktop artboards from design files 09 and 08, and checked D8's queue link
against file 05. Compared matching runtime captures at 1440×900. No new palette, font, icon set or illustration was
introduced. All controls use the maintained UI kit and runtime tokens.

| Surface | Matched design and documented runtime differences |
|---|---|
| G9 | Existing Studio shell, heading/action, compact status rows, exact `110px / 1.4fr / 80px / 1.6fr / 120px / 40px` desktop column proportions and 14px gaps, ellipsis menus and three summary cards. Actual job count/types, routes, names, progress and usage replace samples. The no-worker subtitle/button state reflects the test server; no GPU metrics are invented. |
| G4 | Full-width shell without the sidebar, heading, compact four-category segments, Mark all read, 760px list, icons, timestamps and unread dots. The browser flow creates two genuine in-app test notifications; artboard sample alerts are not fabricated. |
| D8 | The existing Open queue position and copy remain; it is now a working link rather than a Soon control. No other editor layout changes. |
| Necessary states | Existing State/Notice/Box/KV/dropdown/button patterns cover empty lists, older pages, job details, unavailable workers and refresh errors. See [ADR 0004 §9](../decisions/0004-design-addendum.md). |

Existing shell Soon labels, actual fixture data, permissions and missing-worker state explain the differences from
the populated artboards. The desktop layout is the visual source; the mobile artboards were not implemented.
Basic 390×844 checks show readable stacked content without horizontal overflow; mobile optimisation remains deferred.

## Verification

| Gate | Result |
|---|---|
| Focused activity/jobs/notify integration | Pass on SQLite and isolated PostgreSQL 18 |
| Full `go test -count=1 ./...` with `MV_TEST_POSTGRES_URL` | Pass; 37 tested packages |
| Configured premium focused integration command | Pass; includes activity and notify |
| `go vet ./...`, `gofmt`, whitespace gate | Pass |
| `templ generate` and module tidy | Generated source retained; no unintended module changes |
| `go test -count=1 ./internal/editioncheck/...` | Pass |
| Frontend check, unit tests and production build | Pass; 110 tests |
| Strict premium audit, including new UI and shared browser utilities | Zero findings |
| Final focused browser flow | Pass in 36.2s: creation/art/lettering/exports plus G9/G4, keyboard menu recovery, category/empty states, explicit read state, safe details, English/Japanese, Paper, reduced motion, controlled refresh failure, basic narrow width and no-JavaScript routes |
| Ten-content-language export matrix | Pass in 2.5 minutes; 20 interface/content-language cases and 40 PDF/CBZ/PNG-ZIP downloads |

The broad Go run initially caught an existing action-menu assertion that expected a form without the newly applied
canonical `novalidate` attribute; its expectation was aligned and the complete suite rerun. The first browser run
assumed ordinary successful single jobs create notifications; it was corrected to use the existing in-app test
action, preserving notification policy. The final activity flow passed after that correction. The language matrix
passed independently before the focused rerun; it was not repeated after activity-only CSS/test changes.

Tests cover running progress and pending cancellation, ownership on pages/fragments/mutations, bearer rejection,
CSRF, worker authority/liveness, category filtering before paging, mark-all scope, safe redirects, bounded events,
legacy error redaction and parent/child usage double counting. Browser refresh failures retain the original
snapshot; open menus, focus, pending forms, stale requests and navigation are covered by frontend regressions.

The build retains the existing demo-island initial-state and HTMX dependency `eval` warnings; runtime HTMX eval
remains disabled under CSP. Worker code was unchanged, so worker pytest was not repeated. No dependency was
installed, no live provider was called, and no original app data or key was used. PostgreSQL used the cached
`postgres:18-alpine` image with temporary storage and a loopback-only port.

## Local evidence and remaining scope

Evidence is retained under `/private/tmp/mv-phase2-evidence/`: `artboards/`, `browser/`,
`go-sqlite-postgres-final.log` and `premium-audit.json`. Browser captures include G9/G4 in both interface languages,
Paper/reduced-motion views, the failed-refresh view and basic narrow-width views. Normal future runs use ignored
`e2e/test-results/`; the test server/data are isolated and removed by the harness.

Only Phase 2 milestone 1 is complete. History/scoped page control, Character Lock scoring/repair/training UI,
pose/sketch tools and the final 30-page quality assessment remain in plan 0006. No character-consistency or GPU
quality claim is made by these queue/notification checks. Japanese copy still requires native review before a
public release. No push or new PR was requested for this implementation.
