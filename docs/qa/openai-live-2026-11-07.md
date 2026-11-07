# Live OpenAI manga verification

Date: 2026-11-07. Contract: [plan 0005](../plans/0005-openai-live-image-verification.md).
Application: `ee53ad5`, with its existing direct image route from [ADR 0009](../decisions/0009-openai-image-route.md).

## Outcome

The owner's saved **OpenAI-Personal** connection authenticated successfully and produced real manga artwork with
`gpt-image-2.5-sunburst`. Background, character and FX jobs each succeeded on the first attempt. The resulting page
shows an original courier leaping across a harbour footbridge, in detailed black-and-white seinen linework, with
separate editable speech/caption text. No application or design changes were needed.

The old preview at port 6299 was using a different data folder containing test providers, without the saved OpenAI
connection. Its geometric artwork was placeholder output. The saved connection was found in the original app's
data; no key re-entry was necessary.

## Method and isolation

- Read the original closed database without modifying it. Opened the saved sealed key in memory only; no plaintext
  key was printed or saved. A model-list request returned HTTP 200 and included the selected image model.
- Created a separate local test database and an original project, **Saltglass Crossing · Live OpenAI**. Copied only
  the required existing account and sealed provider connection into it, preserving their identities and encryption.
  The original provider settings, manuscripts and database were left unchanged.
- Set the isolated instance's remote spend guards to USD 3. Used the actual `gen.Service.EnqueuePanel` and job
  executor, one layer and one variation at a time, pinned to the saved OpenAI connection's image model. Each result
  was applied through the existing document/version guards and asset service, then composed by the page renderer.
- The test ledger is independent of the original app's ledger, but the calls used the owner's real OpenAI account.
  No fallback provider or placeholder output was used for these three jobs.

## Observed results

| Layer | First-attempt job | Duration | Recorded USD | Persisted image |
|---|---|---:|---:|---|
| Background | `01m6vbrra2eqgkbcmk7kcaeqnm` | 35.46 s | 0.043075 | 658×1024 PNG; opaque harbour plate |
| Character | `01m6vbv8fpm33grrh55gn1gbq3` | 29.52 s | 0.043465 | 658×1024 PNG; drawn courier with transparent background |
| FX | `01m6vby0tjgrzh5scvgt8d7mdg` | 35.34 s | 0.043075 | 658×1024 PNG; ink motion effects with transparency |

Total recorded spend: **USD 0.129615**. These are the app's amounts calculated from reported API usage using the
frozen ADR 0009 tariffs, rather than an invoice comparison. Each call reserved USD 0.07188 and finished in
`spend_reservations.state = reconciled`; none left an outstanding reservation. The larger USD 0.21564 job estimate
covered three possible attempts, but each job needed only one.

Visual inspection confirmed drawn manga characters, environment and motion effects. Both character and FX files
contain actual transparent pixels. All three persisted layers retain the saved OpenAI provider identity, the image
model and `ai` provenance. The composed page is 1600×2400 and contains two structured, editable lettering bubbles;
the lettering was not baked into generated artwork.

The actual editor loaded at 1440×900 with HTTP 200, all three live layer assets and both bubbles. Browser checks
reported no page errors or external browser requests. Editor and Preview captures were inspected. The live run
exercised the existing scoped/versioned generation path; negative lock cases remain covered by the existing tests,
not by additional remote calls in this run.

## Local evidence and preview

Evidence is retained locally under `/private/tmp/mangaveyra-openai-live-922818346/evidence/`:

- `manga-page.png`, `background.png`, `character.png`, `fx.png`
- `manifest.json`, `browser-check.json`
- `editor-live-desktop.png`, `preview-live-desktop.png`

The isolated preview is running at <http://localhost:8091>. Its generated chapter is at
<http://localhost:8091/studio/p/01m6vbrr9z8j95ecnc0az98ge3/chapters/01m6vbrra0fz8r6xx3znx04h1w/edit>.
It uses the existing account credentials; the connection is already present. This is a temporary local preview,
and the evidence directory should be retained if needed after temporary-directory cleanup or reboot.

## Verification and limits

`go test ./internal/ai ./internal/gen` passed after the live run. No production code, dependencies, generated source
or interface copy changed, so the completed plan 0004 gates did not need repeating. The temporary Go verification
harness and browser authentication-state file were removed after the check. No push or PR update was performed.

This proves basic direct OpenAI manga generation and three-layer composition with the saved key. It is one
original test panel, not a whole-chapter quality assessment. Reference-conditioned characters, exact square-image
token ceilings, masked edits, n = 2 accounting, fight-scene moderation and OpenRouter backups remain pending in
ADR 0009; this run does not claim those checks passed.
