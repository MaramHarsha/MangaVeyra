# UX Contract

## Product context

- Audience: independent manga creators and small production teams.
- Primary jobs: connect a provider, create a project/chapter, generate a bounded script job, inspect status, edit the persisted manuscript, and recover without losing work.
- Target markets: English- and Japanese-language creator workflows documented in `docs/FEATURES.md`, `docs/SCREENS.md`, and ADR 0004.
- Active locales: `en` fallback and mirrored `ja`; public Japanese copy requires native review.
- Timezone/calendar policy: store UTC and use the existing user-locale formatter. Gate A collects no dates.
- Accessibility target: WCAG 2.2 AA.

## Business-context sources

| Domain / scope | Authoritative source | Source type | Reviewed date |
|---|---|---|---|
| Permission model | `internal/studio/contract.go`, `internal/auth`, plan 0001 | Runtime contract / plan | 2026-11-07 |
| Data lifecycle and provenance | `docs/ARCHITECTURE.md`, ADR 0003, ADR 0005 | Architecture / ADR | 2026-11-07 |
| Deletion / retention | `AGENTS.md`, provider lifecycle in plan 0001 | Product policy / plan | 2026-11-07 |
| Billing / payment | `AGENTS.md`; user-owned provider keys only | Product policy | 2026-11-07 |
| Legal / regulatory copy | No regulated Gate A flow; `AGENTS.md` remains binding | Product policy | 2026-11-07 |
| Market / content conventions | ADR 0004 and `docs/SCREENS.md` | Design ADR / route map | 2026-11-07 |

## Visual contract

- Project `DESIGN.md`: `DESIGN.md`.
- Token ownership model: existing runtime CSS is canonical; `DESIGN.md` mirrors it.
- Runtime design-system/token source: `web/src/styles/tokens.css`, `fonts.css`, `base.css`, `components.css`, and `internal/web/ui`.
- Mapping/export/adapters: CSS custom properties consumed by templ and optional Svelte islands.
- Token drift gate: frontend check/test/build, edition check, templ drift check, and strict premium audit.
- Supported themes: Gate night, Paper, and system; high-contrast focus and text-size preferences.
- Design-context owner/review policy: root owns shared contracts; touched flows migrate to canonical components in the same change.

## Canonical UI Map

| Capability | Canonical owner | Source of truth | Allowed variants | Verification |
|---|---|---|---|---|
| Select/Listbox | `ui.Select` over native `select` | `internal/web/ui/form.templ` | native single-select | keyboard/browser + handler tests |
| Form | `ui.Field` and shared form controls | `internal/web/ui/form.templ`, `components.css` | create / edit / secret | HTTP flow + validation tests |
| Toast | shared notice/toast owner | `internal/web/ui/state.templ` | success / warning / info / error | live-region + edition check |
| CRUD | feature handler plus canonical form | `internal/ai`, `internal/studioui`, service contracts | PRG page / HTMX fragment | full-flow + failure-path tests |

## Component behavior

| Component | Default | Hover | Focus | Active | Disabled | Busy | Error |
|---|---|---|---|---|---|---|---|
| Button | labelled action | token hover | visible shared ring | pressed geometry | dimmed, non-actionable | stable width, `aria-busy` | nearby notice |
| Icon button | conventional icon + name | surface change | visible shared ring | pressed | dimmed | spinner | nearby notice |
| Input | value or hint | border stable | ring + stronger border | n/a | dimmed/read-only | form owns pending | `aria-invalid` + inline text |
| Secret input | masked and blank | border stable | ring + stronger border | n/a | dimmed | submit owns pending | cleared; never re-rendered |
| Search | not used in Gate A | n/a | n/a | n/a | n/a | n/a | n/a |
| Textarea | `resize: none`; component-sized | border stable | ring + stronger border | n/a | dimmed | form owns pending | `aria-invalid` + inline text |
| Table/list | semantic list/card or chapter rail | row affordance | focus on real action | selected route | unavailable action hidden/disabled | local state row | scoped notice |

## Dataset navigation

- Admin tables: not in Gate A.
- Exploratory lists: provider cards and chapter rail are bounded server-rendered lists.
- URL state: project/chapter identity is in the route; secrets, unsaved state, job errors, and source text never enter the URL.
- Page size: no pagination in the Gate A lists; service limits remain explicit.
- Empty/no-results/error/loading treatment: distinct empty states, recoverable notices, and `ui.GenState`.
- Back/scroll restoration: normal browser behavior after GET; successful mutations use PRG.
- Selection scope: native single selects only; no bulk selection.

## Flow ledger

| Operation | Trigger | Pending | Success destination | Success feedback | Failure recovery | Focus outcome | Source ref |
|---|---|---|---|---|---|---|---|
| Connect provider | provider form submit | disabled busy submit | `/models` | sanitized provider card | inline error, key cleared | summary then invalid field | ADR 0005 |
| Test provider | Test action | card-local busy | same card | sanitized test state/request ID | card notice; retry | Test action | plan 0001 |
| Enable/disable provider | explicit card action | card-local busy | `/models` | state badge | card notice; retry | action or notice | plan 0001 |
| Replace provider key | replacement form | disabled busy submit | `/models` | non-secret key hint | inline error, key cleared | key field | ADR 0005 |
| Create project/chapter | B3 form submit | disabled busy submit | canonical story route | created/no-script state | safe values retained; project stays recoverable | heading or invalid field | plan 0001 |
| Generate script | story action | queued `ui.GenState` | same story route | terminal reloads manuscript | translated error + retry | state heading/action | plan 0001 |
| Edit script | Save changes | pessimistic busy submit | same story route | saved notice/new revision | unsaved values retained; Reload on conflict | notice then editor | `studio.EditScript` |
| Upload source | file + create submit | submit owns pending | project/story route | adaptation summary | type/size/UTF-8 error | upload field | plan 0001 |
| Cancel/back | normal link | none | previous canonical GET | none | browser retains GET | destination heading | route map |

## Navigation and responsive behavior

- Route document title policy: resource/action first, then MangaVeyra; locale-aware.
- Route error / 403 behavior: unauthorized resource IDs render 404; use shared 403 only where existence disclosure is safe.
- Breadcrumb/tab/route-state policy: routes are canonical; the chapter rail follows the chapter path.
- Sidebar/drawer/bottom-sheet transformation: the existing shell owns desktop sidebar/mobile bottom navigation; no duplicate mobile DOM.
- Responsive table strategy: no Gate A tables. Cards stack and the story rail moves above the manuscript.
- Truncation/full-value access: non-secret hints/IDs may truncate visually; secrets never render.
- Focus restoration and sticky-obstruction policy: focus a replaced heading when needed and preserve visible focus margins.

## Overlays and feedback

- Dialog primitive: shared dialog only for destructive or high-consequence actions.
- Destructive confirmation levels: provider deletion is deferred; disabling is reversible and explicit.
- Toast placement/duration/deduplication: shared live region; durable errors remain inline.
- Alert/banner scope and persistence: provider-card notice for local issues, page notice for create/save/job issues.
- Tooltip delay/dismissal: shared owner; never the sole carrier of instructions.
- Unsaved-changes behavior: server errors/conflicts retain submitted non-secret values. No false draft/autosave claim.
- Layer/z-index contract: dialog > drawer > popover > toast; existing CSS is canonical.

## Async and resilience

- Mutation default: pessimistic server commit with PRG; generation is an explicit queued job.
- Idempotency and duplicate-submit policy: disable active submit; queue/spend layers fence side effects; retry is explicit.
- Auto-save/draft recovery: none in Gate A. Never display “Draft saved.”
- Offline/read-stale/write behavior: keep GET visible; writes fail clearly and retain safe values. No offline queue.
- Retry/backoff/timeout behavior: poll jobs every two seconds; terminal retry only when allowed; provider testing is bounded.
- Version conflict and multi-tab behavior: `chapter.updated_at` optimistic fence; show submitted values plus Reload, never last-write-wins.
- Session expiry/re-authentication: shared middleware owns full-page and HTMX re-authentication.
- Long-running progress and return path: `ui.GenState`, `no-store` polling, terminal `HX-Redirect`, and a normal Refresh link.
- Stale-request cancellation/invalidation and pending-state ownership: terminal state stops polling; every poll rechecks user/type/project/chapter.
- Dialog/form preservation and retry after failure: retain safe fields; provider keys are intentionally cleared.

## Validation

- Schema/validation layer: domain services are authoritative; handlers parse bounded forms/uploads and map stable errors.
- Trigger timing: submit; forms declare `novalidate` so server validation has one owner.
- Error summary/inline policy: summary for multiple errors, inline per field, first invalid control focused.
- Server error mapping: stable AI/job/studio codes to locale keys; never raw upstream/database text.
- Sensitive-value handling: keys, tokens, private prompts, and source text in URLs never enter HTML errors/logs. Failed key submits render blank.
- Duplicate-submit prevention and recovery: busy/disabled active submit, restored after response; no optimistic success.

## Permission and clipboard

- Permission UI strategy: hide unavailable actions; direct unauthorized object access resolves to 404 where sensitive. Server authorization remains authoritative.
- Clipboard copy policy: no Gate A secret-copy action. Future non-secret copy uses explicit controls and never echoes values in toasts.
- Disabled-state explanation: local-only provider connection uses a real disabled control plus reason, not a live anchor styled disabled.

## Verification

- Required static commands: `premium-ui.json`, plus `gofmt`, `go vet ./...`, `go test ./...`, templ/mod drift, and `git diff --check`.
- Browser/device/locale/theme matrix: 1440 px and 390 px; English/Japanese; Gate night/Paper/system; JavaScript on/off.
- Accessibility checks: keyboard flow, visible focus, labelled secret/file controls, live job state, reduced motion, AA contrast, 200% text.
- Native-language/domain review: Japanese catalogue needs native review; verify manga RTL and provenance language.
- Japan readiness: IME composition, mixed kana/Latin names, Unicode preservation, long titles, kinsoku-friendly wrapping, visual regression.
- Component-state coverage: provider empty/connected/disabled/test-failed; create valid/invalid/upload-failed; story empty/queued/running/failed/conflict/saved.
- Canonical sibling flow: existing setup and account server-rendered forms.
- Project audit command: strict `audit_project.py` with `premium-ui.json`, run separately.
- CRUD full-flow evidence: placeholder create → generate → render → edit → save → reload test.
- Failure-path evidence: provider secret/local-only, IDOR, job retry, and script conflict tests.
