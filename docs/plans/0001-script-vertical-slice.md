# Milestone 0001: Provider to persisted script

- Status: Complete — Gate A of umbrella Phase 1 plan 0002 (verified 2026-11-07)
- Base commit: `a84023348c126908d6ebab1a12a3bd8cfd4d979a`
- Decision record: `docs/decisions/0001-mvp-scope.md`
- Spend decision: `docs/decisions/0003-hard-remote-spend-reservations.md`
- OpenRouter decision: `docs/decisions/0005-gate-a-openrouter-text.md`

Plan 0002 defines the full Phase 1 outcome. This plan remains the normative
contract for its first vertical slice: provider → persisted script. Gate A passed
before image generation, page persistence, lettering, compositing, export, or
editor implementation began. This plan is Complete rather than Superseded.

## Outcome

From a fresh self-hosted install, a creator can use a deterministic offline
placeholder or connect OpenRouter, create a project and chapter, request a
structured script-generation job, and persist an editable result without partial
writes.

This milestone does not include image generation, the page editor, lettering,
compositing, publishing, reading, export, hosted billing, or World.

## Frozen feature contracts

Feature packages expose narrow services through `core.Provide`/`core.Use`; they do
not edit `internal/app/modules.go`. The source contracts in
`internal/ai/contract.go` and `internal/studio/contract.go` are canonical. The
names and fields below describe their Gate A subset. Changing them requires
updating this plan before parallel work resumes.

### AI structured-text service

`internal/ai` owns these public types:

```go
type TextService interface {
    PlanText(context.Context, TextPlanRequest) (TextPlan, error)
    GenerateText(context.Context, TextGenerateRequest) (TextResult, error)
}

type TextPlanRequest struct {
    Task, Schema, ActorID, ProjectID, RoutingMode string
    PinProviderID, PinModelID string
    Input json.RawMessage
}

type TextPlan struct {
    Task, Schema, RoutingMode, ProviderID, ModelID string
    Remote bool
    Decision json.RawMessage
    Estimate jobs.Cost
}

type TextGenerateRequest struct {
    Plan TextPlan
    ActorID, JobID string
    ProjectID string
    Attempt int
    Input json.RawMessage
}

type TextResult struct {
    Document json.RawMessage
    ProviderID, ModelID string
    Actual jobs.Cost
}

type GenerateError struct {
    Code, SafeMessage string
    Retryable bool
    Actual jobs.Cost
    Err error
}
```

The pins support an explicit provider/model choice. `JobID` and `Attempt` bind
every remote call to a durable spend reservation and the claimed job attempt.
The umbrella AI contract also defines image generation, but that implementation
belongs to Phase 1 Gate B after this script slice passes.

`GenerateError.Error()` exposes only `SafeMessage`; raw upstream bodies, headers,
URLs, and secret-bearing request data are never persisted or logged. `Actual`
records billable usage even when a call or bounded repair fails.

`PlanText` chooses a provider/model and estimate but contains no decrypted key.
`GenerateText` treats the plan as untrusted, re-resolves the provider and key, and
rechecks settings immediately before every outbound call, repair, or failover.
The deterministic placeholder is local and remains available without a key.
OpenRouter text is the only remote adapter in this milestone. ADR 0005 freezes
`openai/gpt-4.1-mini`, the official endpoint, strict structured output, price
ceilings, a 16,384-token completion limit and the conservative per-call spend
upper bound. Custom base URLs and other remote models are out of scope.

`TextPlan.RoutingMode` preserves the validated request mode so generation can
enforce request-local `local_only` without trusting `Decision`, which is only an
explanation. A remote plan whose full prompt/schema envelope cannot fit the ADR
0005 context bound is excluded; an explicit OpenRouter pin returns `bad_input`
rather than truncating source material.

For this milestone, `jobs.decision` is the canonical routing record. The separate
`routing_decisions` history is deferred until routing analytics are implemented.

### `mangaveyra.script.v1`

The AI document has this exact shape:

```json
{
  "schema": "mangaveyra.script.v1",
  "chapter_summary": "",
  "scenes": [
    {
      "key": "scene-1",
      "title": "",
      "summary": "",
      "time_of_day": "",
      "panels": [
        {
          "key": "panel-1",
          "page_number": 1,
          "camera": "",
          "shot": "",
          "emotion": "",
          "action": "",
          "dialogue": [
            {"speaker": "", "character_id": "", "kind": "speech", "text": ""}
          ],
          "sfx": [{"text": "", "note": ""}],
          "character_ids": [],
          "notes": ""
        }
      ]
    }
  ]
}
```

Rules:

- Decode at most 1 MiB; reject duplicate keys, unknown fields, trailing data, a
  wrong schema value, invalid UTF-8, and invalid enum values.
- `dialogue.kind` is `speech`, `thought`, or `caption`.
- Accept 1–32 scenes and 1–256 panels total. Scene and panel keys are non-empty
  and unique. Page numbers are 1–9999 and nondecreasing in document order.
- Limit titles and speakers to 160 characters, summaries to 4,000, action/notes/
  dialogue text to 2,000, and camera/shot/emotion/SFX fields to 240. Trim strings;
  a panel needs non-empty action, dialogue, or SFX.
- `character_id` and `character_ids` may contain only project character IDs that
  were included in the request's allow-list. Duplicate IDs are rejected.
- Array order is authoritative. The server assigns database IDs and `order_idx`,
  and computes the display label from page number plus the panel's position on
  that page.
- Arcs and locations are not generated in this milestone. `chapter.arc_id` is
  unchanged. The existing chapter title is preserved.
- There is at most one repair attempt. Repair uses the same outbound guards and
  its own spend reservation. Failure after that is permanent bad output.

AI validates syntax, schema/version, size, and bounded repair. Gen strictly decodes
again and performs the task-specific semantic checks. Studio rechecks authorization,
relationships, locks, and transactional invariants.

### Studio script service

`internal/studio` owns the persisted document types corresponding to
`mangaveyra.script.v1`. Its shared `studio.Service`, `Actor`, `Action`, and
`Authorize` contract replaces the narrower proposed `Script*` names so the same
authorization boundary serves later Phase 1 features. Its Gate A subset is:

```go
type Service interface {
    Authorize(context.Context, Actor, string, Action) error
    CreateProject(context.Context, Actor, NewProject) (Project, error)
    CreateChapter(context.Context, Actor, string, string) (Chapter, error)
    CreateCharacter(context.Context, Actor, NewCharacter) (Character, error)
    SourceText(context.Context, string) (string, error)
    ReplaceGeneratedScriptTx(context.Context, *db.Tx, ReplaceScriptInput) (ReplaceScriptResult, error)
    EditScript(context.Context, EditScriptInput) (Chapter, error)
}
```

`Actor` contains the current user ID, optional bearer-token ID, and site role.
`ReplaceScriptInput` contains actor, project/chapter IDs, the fully validated
Studio document, the enqueue-time chapter revision, job ID, provider/model IDs,
and provenance. It intentionally does not accept an attempt number:
`jobs.Run.CommitSideEffect` is the authoritative attempt fence, so caller-supplied
attempt state cannot be trusted. The chapter revision is compared under the same
row lock as replacement, preventing two jobs planned from the same revision from
overwriting one another. It never accepts a raw model document.

`NewProject.SourceText` is private adaptation material stored under
`projects.settings.script_source_text`, never exposed by project/list DTOs or
logs. It is valid UTF-8, at most 1 MiB, omitted when empty, and settings updates
preserve unknown keys. `SourceText` returns an empty string when absent and is
called only after project/script authorization.

Project creation inserts the owner into `project_members` with role `owner` in the
same transaction. The authorization matrix is:

| Action | Allowed project roles |
|---|---|
| Read script | owner, writer, artist, letterer, editor, viewer; server admin |
| Edit or generate script | owner, writer, editor; server admin |
| Create project | active site creator or admin |

Bearer tokens additionally need `projects:read` for reads; `projects:write` and
`generate` for generation; and must allow the project. When an estimated remote
cost crosses `approve_over_usd`, this milestone returns `approval_required` rather
than bypassing the threshold. The enqueue input stores the token ID, not its secret.
The handler re-resolves it and rejects revoked, expired, wrong-project, or
insufficient-scope tokens. Session jobs store no token ID and recheck that the user
is active. Browser Studio routes and manual retry require a signed-in session.
Bearer job reads require `projects:read` and honor the token project allow-list;
bearer cancel requires `projects:write` plus `generate`.

Before the transaction, Studio validates the whole document. Inside the transaction
it rechecks project/chapter ownership, current membership, chapter lock, and the
character allow-list. It then replaces only that chapter's scenes and script panels,
updates the chapter summary/status/script provenance, and leaves arcs, title, and
unrelated story data unchanged. Any error preserves the previous script exactly.

Human edits use `EditScript` with the chapter `updated_at` value as an optimistic
revision. Gate A edits only an existing script's recognized text fields while
preserving its structure and ordering; scene and panel IDs remain stable so later
page references cannot be orphaned. The same authorization, ancestry, lock and
character checks run inside the transaction.
A stale revision returns `ErrConflict`, and editing AI output changes script
provenance from `ai` to `ai_assisted` rather than claiming it is wholly human.

### Generation handler

`internal/gen` owns `script.generate`. It plans before enqueue, stores only safe
plan/actor/input data in the job, and at execution rechecks the actor, remote guard,
provider, spend reservation, and chapter lock. After strict decoding, it commits the
Studio replacement through the root-owned lease-fenced job primitive described
below. It maps only safe AI errors into `jobs.JobError` and always reports actual
usage.

### Provider secret lifecycle

- Allocate the provider ID before sealing. AAD is exactly
  `provider:<provider-id>:key`.
- Provider ID, scope, and owner are immutable. Server scope requires a null owner;
  user scope requires an owner. Ownership transfer is unsupported.
- A blank key during an ordinary update retains the ciphertext. Explicit key
  replacement reseals with a fresh nonce and derives the hint from submitted
  plaintext server-side.
- Disable retains the encrypted key; delete removes the row.
- API/HTML DTOs never contain `key_enc`. `last_test` contains only sanitized state,
  time, and request ID.

### Outbound and spend rules

Effective local-only is `instance.local_only || request mode == local_only`.
Outbound checks use `Settings.Instance(ctx)`, never `MustInstance`; a settings read
or decode failure forbids remote access. Check at planning, immediately before every
HTTP call, repair, failover, and provider connection test. Disabled or deleted
providers and keys are re-resolved at dispatch. Counting-transport tests must prove
zero requests for each failure mode.

OpenRouter connection testing uses authenticated `GET /api/v1/key`, not a
completion. Generation sends `provider.require_parameters=true`, the ADR 0005
`provider.max_price` ceilings, strict JSON Schema output and usage accounting.
Missing or invalid returned cost reconciles at the reserved estimate. The text
plan's estimate covers the initial call plus its one possible repair for a single
claimed attempt, while each outbound call receives its own per-call reservation.
Generation multiplies that bound by the job's three attempts before enqueue; the
durable job estimate and bearer-token approval therefore cover the full automatic-
retry action.

Remote spend caps are hard, not advisory. A root-owned `0200` migration adds durable
reservations keyed by job ID, attempt, and call sequence. Each outbound call atomically
reserves its estimate against user and server monthly caps, then reconciles actual
usage on success or failure. A still-reserved row counts at its estimate. Reconciliation
is idempotent; startup repair closes stale reservations conservatively at the estimate.
The existing spend ledger remains the immutable actual-usage history.

## Root-owned prerequisites before Wave 1

The root/integrator must land and test these shared changes before AI and Studio
agents edit in parallel:

1. [x] Make `jobs.Scope.Validate` reject every extraneous ID/region field as well
   as missing required fields.
2. [x] Add `Run.CommitSideEffect`: one database transaction locks and verifies the job
   is still running at the same attempt, runs a supplied Studio callback, stores the
   job output/actual cost, and marks the job done. The normal pool completion path
   detects that this already committed. A stale attempt can never persist.
3. [x] Add the spend-reservation migration and shared transaction helpers. The
   accepted design and parity-test matrix are in
   `docs/decisions/0003-hard-remote-spend-reservations.md`.
4. [x] Add an auth helper that re-resolves a stored token ID and enforces active,
   unexpired, unrevoked state without needing the raw token.
5. [x] Reserve `internal/testutil/**` and shared fake transports for the root. Feature
   agents keep private test helpers unless the root deliberately promotes one.

Required race regression: attempt 1 stalls and expires, attempt 2 persists different
output, then attempt 1 resumes. Only attempt 2 may remain, on both SQLite and
PostgreSQL.

## Execution order and ownership

### Wave 0: repository readiness

- Root: Codex instructions, this plan, decision record, and integration ownership.
- Phase-0 agent: `README.md` pre-alpha/shipped-vs-planned correction and pull-request
  CI trigger; owns only `README.md` and `.github/workflows/**`.
- Research agent: read-only recommendation for a permissively licensed,
  anime-capable Character Lock embedder.
- Verification agent: read-only adversarial review of these contracts and security
  boundaries.

### Wave 1: parallel foundations

- Every root-owned prerequisite above is green.
- AI text/provider implementation and its focused test, race, vet and diff checks
  are complete in `internal/ai/**`, including PostgreSQL parity.
- Studio implementation and its SQLite/PostgreSQL tests are complete in
  `internal/studio/**`.
- Root owns shared interfaces, setup integration, app/core files, migrations, and
  global locale/catalogue changes.

### Wave 2: generation

- The `script.generate` enqueue/handler path is complete in `internal/gen/**`,
  including aggregate approval bounds, strict decoding, live authorization and
  lock rechecks, actual-cost preservation, and lease-fenced Studio persistence.
- Focused Generation tests, race, vet and diff checks pass on SQLite and PostgreSQL.

### Wave 3: minimal UI and integration

- A UI agent implemented the provider, project wizard, job-state and editable
  manuscript routes after backend contracts passed focused tests.
- Root completed generated-file updates, integration hardening and full verification.
- A read-only verifier reviewed the integrated authorization, retry, multipart,
  stable-ID and chapter/job race boundaries before completion.

## Acceptance criteria

- Admin can save, test, disable, and replace an OpenRouter connection.
- Provider secrets use the exact row-bound AAD and lifecycle above,
  never returned or logged, and displayed only as a non-secret hint.
- `local_only`, provider disable/delete, and settings failures prevent every remote
  attempt, including queued work, repair, failover, and connection tests.
- The complete site/project/token authorization matrix above passes.
- `script.generate` creates a Go-executor job and records routing/cost metadata.
- A current lease atomically persists scenes and script panels and completes the
  job; a stale attempt cannot persist.
- Invalid provider output leaves the existing script unchanged.
- Placeholder generation is deterministic and requires no network or key.
- Concurrent hard spend caps, cap/local-only changes during lock contention,
  failed calls, repairs, retries, failover, duplicate reconciliation, and
  stale-reservation repair are tested.
- The dashboard shows the created project.
- Focused SQLite tests pass; data behavior is also exercised against PostgreSQL.
- Full applicable gates in `AGENTS.md` are green before the milestone is marked
  complete.

## Blocking risks and review gates

- Structured-output repair must be bounded and deterministic; never silently
  accept malformed or schema-incompatible output.
- Retries and failover use the lease-fenced atomic completion boundary.
- Remote spend uses durable reservation before each call and idempotent
  reconciliation after every outcome.
- Project roles and scope/lock checks are server-side, not UI assumptions.
- The spend reservation table is the one accepted schema defect; no other migration
  is added merely to avoid using the existing schema.
- Editor or visual-design work cannot expand this milestone's scope.

## Completion evidence (2026-11-07)

- Full Go tests passed with `MV_TEST_POSTGRES_URL` set, exercising SQLite and
  PostgreSQL data paths; focused jobs/Studio/AI/Generation/Studio UI race tests passed.
- An adversarial concurrency review confirmed policy is reloaded uncached inside
  the spend-locked transaction; cap reductions and local-only changes committed
  during contention reject the waiting reservation on both dialects.
- The placeholder end-to-end test covers wizard → queued generation → persisted
  manuscript → stable-ID edit → save/reload, including IDOR, stale-revision and
  locked/read-only failure paths.
- `go build ./...`, `go vet ./...`, `go mod tidy` drift, templ generation,
  edition checks, frontend typecheck/tests/build and strict premium UI audit passed.
- Worker tests passed under Python 3.12: 153 passed, with only optional PyTorch and
  WebSockets backend tests skipped because those extras were not installed.
