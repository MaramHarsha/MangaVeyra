# Milestone 0001: Provider to persisted script

- Status: Active (Wave 0 complete; feature implementation is blocked on the
  root-owned prerequisites below)
- Base commit: `a84023348c126908d6ebab1a12a3bd8cfd4d979a`
- Decision record: `docs/decisions/0001-mvp-scope.md`
- Spend decision: `docs/decisions/0003-hard-remote-spend-reservations.md`

## Outcome

From a fresh self-hosted install, a creator can use a deterministic offline
placeholder or connect OpenRouter, create a project and chapter, request a
structured script-generation job, and persist an editable result without partial
writes.

This milestone does not include image generation, the page editor, lettering,
compositing, publishing, reading, export, hosted billing, or World.

## Frozen feature contracts

Feature packages expose narrow services through `core.Provide`/`core.Use`; they do
not edit `internal/app/modules.go`. The names and fields below are the Wave 1
contract. Changing them requires updating this plan before parallel work resumes.

### AI structured-text service

`internal/ai` owns these public types:

```go
type TextService interface {
    PlanText(context.Context, TextPlanRequest) (TextPlan, error)
    GenerateText(context.Context, TextGenerateRequest) (TextResult, error)
}

type TextPlanRequest struct {
    Task, Schema, ActorID, ProjectID, RoutingMode string
    Input json.RawMessage
}

type TextPlan struct {
    Task, Schema, ProviderID, ModelID string
    Remote bool
    Decision json.RawMessage
    Estimate jobs.Cost
}

type TextGenerateRequest struct {
    Plan TextPlan
    ActorID, ProjectID string
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

`GenerateError.Error()` exposes only `SafeMessage`; raw upstream bodies, headers,
URLs, and secret-bearing request data are never persisted or logged. `Actual`
records billable usage even when a call or bounded repair fails.

`PlanText` chooses a provider/model and estimate but contains no decrypted key.
`GenerateText` treats the plan as untrusted, re-resolves the provider and key, and
rechecks settings immediately before every outbound call, repair, or failover.
The deterministic placeholder is local and remains available without a key.
OpenRouter text is the only remote adapter in this milestone. Its fixed official
endpoint is used; custom base URLs are out of scope.

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
`mangaveyra.script.v1` and exposes:

```go
type ScriptService interface {
    AuthorizeScript(context.Context, ScriptActor, string, string, ScriptAction) error
    ReplaceGeneratedScriptTx(context.Context, *db.Tx, ReplaceScriptInput) (ReplaceScriptResult, error)
}
```

`ScriptActor` contains the current user ID and optional bearer-token ID.
`ReplaceScriptInput` contains actor, project/chapter IDs, the fully validated Studio
document, job ID, attempt, provider/model IDs, and provenance. It never accepts a
raw model document.

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
is active.

Before the transaction, Studio validates the whole document. Inside the transaction
it rechecks project/chapter ownership, current membership, chapter lock, and the
character allow-list. It then replaces only that chapter's scenes and script panels,
updates the chapter summary/status/script provenance, and leaves arcs, title, and
unrelated story data unchanged. Any error preserves the previous script exactly.

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
3. [ ] Add the spend-reservation migration and shared transaction helpers. The
   accepted design and parity-test matrix are in
   `docs/decisions/0003-hard-remote-spend-reservations.md`.
4. [ ] Add an auth helper that re-resolves a stored token ID and enforces active,
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

- Wave 1 starts only after every root-owned prerequisite above is green.
- AI agent owns `internal/ai/**` and new module-specific locale files.
- Studio agent owns `internal/studio/**` and new module-specific locale files.
- Root owns shared interfaces, setup integration, app/core files, migrations, and
  global locale/catalogue changes.

### Wave 2: generation

- Gen agent owns `internal/gen/**` and new module-specific locale files.
- It works against the frozen AI/Studio contracts and fakes before integration.

### Wave 3: minimal UI and integration

- One UI agent receives exclusive templ/route ownership after backend contracts
  pass focused tests.
- Root performs generated-file updates, integration fixes, and full verification.
- A read-only verifier reviews the integrated diff before completion.

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
- Concurrent hard spend caps, failed calls, repairs, retries, failover, duplicate
  reconciliation, and stale-reservation repair are tested.
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
