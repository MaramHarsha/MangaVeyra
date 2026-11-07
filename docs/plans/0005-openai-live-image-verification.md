# Live OpenAI manga image verification

- Status: Complete — basic direct OpenAI generation verified; ADR 0009's remaining live matrix is separate.
- Authorization: owner requested live image generation with the saved OpenAI connection on 2026-11-07.
- Inputs: ADR 0009, the existing generation pipeline and the completed desktop QA plan 0004.

## Outcome and scope

Prove that the saved OpenAI connection produces actual manga artwork through MangaVeyra's image jobs. Inspect the
returned art, transparency, persisted provider/model provenance and composed page. Fix any demonstrated integration
failure and retain reproducible evidence. This continues ADR 0009's pending live check; plan 0004's scope is unchanged.

Only OpenAI is authorized for this check. Use a separate local data directory and an original test project; preserve
the original database, stored keys and manuscripts. Keys remain sealed at rest and are never printed. Bound the test
instance's remote spend to USD 3 (within ADR 0009's USD 10 ceiling), request one variation, and run jobs deliberately
one at a time. Do not publish, share, delete, unlock, push or change the approved interface design.

## Steps and ownership

The root owns the test harness, provider integration fixes, verification and decision/evidence records.

1. [x] Locate and unlock the saved connection without exposing it; confirm account/model access with OpenAI.
2. [x] Run the normal job pipeline with the live OpenAI route; inspect and preserve actual manga output.
3. [x] Verify independent layers, transparency, lettering and recorded spend/provenance; investigate any failure.
4. [x] Record the observed results and run relevant regression checks for any code changes.

Results are recorded in [the live QA evidence](../qa/openai-live-2026-11-07.md). All three layer jobs succeeded on
their first attempt, with USD 0.129615 recorded spend. No application code, approved design or original app data
needed changing. Reference conditioning, masks, multi-output accounting and backup routes remain outside this run.

## Acceptance and risks

- Every inspected live image names OpenAI and the selected image model; placeholder output cannot pass the check.
- Generated artwork has recognizable drawn characters/environment and manga ink/screentone treatment.
- The server keeps lettering structured, enforces scopes/locks and records remote reservations and actuals.
- No provider key or raw secret-bearing response enters logs, source, screenshots or the evidence record.
- A provider rejection is reported with its actual safe code; it is never presented as successful manga generation.
- Remote quality, latency, token counts and account permissions require live evidence. The test instance has an
  independent spend ledger; its activity still uses the owner's real OpenAI account and is reported explicitly.
- For fixes: focused Go tests, vet/full Go suite, SQLite/PostgreSQL where applicable, templ/module drift checks;
  frontend/edition checks only if their code changes. No dependency installation or unrelated UI work.
