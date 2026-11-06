# ADR 0004: Design addendum — naming, missing states and additions

- Status: Accepted
- Date: 2026-11-07
- Applies to: the design bundle `MangaVeyra_All_Pages` (kept outside this repository) and every screen built from it

The design bundle is the visual source of truth. This record captures the corrections and additions agreed after the
design review, so they survive even though the bundle is not under version control.

## 1. Copy and naming corrections (already applied to the bundle)

- **Accent colour.** The in-app accent is Gate blue `#3D8BFF` with ink text `#070A12` in both themes. Vermilion
  `#E8452C` appears only on the logo slash. Older copy that called the primary button or the selected panel
  "vermilion" is wrong.
- **One name for the paid side.** The closed marketplace is **World** (world.mangaveyra.org, repository 2). "Cloud" and
  "Platform credits" are retired names. The open-source app never shows prices, credits or checkout.
- **"Setting", not "World", inside a project.** The per-project locations/props/style-references screen (C5) and its
  sidebar entry are called **Setting**, so they can't be confused with World.
- Fixed copy bugs: E3 guided reader shows "Panel 3 of 5"; the landing hero SFX sits above the sample page.

## 2. Game layer

- XP, levels, E–S ranks, quests and level-up toasts are **deferred** (ADR 0001) and are not built in the MVP.
- Glow and status-window styling stays limited to the primary action, focus rings and system messages (generation
  status, updates). No rank letters on people or items, and no "quest" wording, until a later decision.

## 3. States every affected screen must design for

The design shows the happy path on an RTX 4090. Build these states with existing components (status window, empty
state, error state, compute meter, routing chip):

| State | Where | Behaviour |
|---|---|---|
| **No models yet** | G8, B1, every generate button | The built-in **placeholder** provider is selected and labelled ("Placeholder art — connect a model"). Every flow still completes. |
| **API only (no GPU)** | G8, B1, C4, D1, D8, G9 | Compute meter shows estimated spend on the user's own keys instead of VRAM. Character Lock uses reference images only; training is hidden behind "Optional: train with a GPU worker". |
| **Low VRAM (8–12 GB)** | G8, G2, C4 | Recommend SDXL-family models with offload; FLUX/Qwen marked "needs more VRAM"; training at 768 px. |
| **Worker offline** | B1, D1, G9 | Status window "Image worker offline — queued jobs wait, or switch them to an API model", with a one-click switch. |
| **Spend limit reached** | D1, D8, G9, G4 | Remote jobs pause with the limit and the reset date; local and placeholder jobs continue. |
| **Provider refused / safety block** | D8, D13 | Shows the provider's safe message, offers the next model (router fallback), never retries silently forever. |
| **Locked scope** | D1, D13, D12 | Generate buttons on locked panels/pages are disabled with "Locked — unlock to change" and the lock owner. |

## 4. Additions

- **Adapt existing text.** B3 step 1 offers "Start from a premise" or "Adapt my text" (paste or upload a chapter of a
  novel or script). C1 gains "Import text into this chapter". The script job turns prose into the panel-by-panel
  script.
- **Self-publishing export.** E2 adds KDP trim presets (5 × 7.5 in, 5.5 × 8.5 in, 6 × 9 in, with 0.125 in bleed) beside
  B5, and a reminder that Amazon KDP requires disclosing AI-generated images and text.
- **Furigana.** Lettering (D3) supports ruby text above kanji in horizontal and vertical layouts.
- **Prompt guard.** Prompt boxes (D1, C4, D13) show a soft warning when a prompt names a living artist's style or a
  known existing character. The list is admin-editable; the warning never blocks local-only private projects.

## 5. Character Lock

- Reference-image guidance is the default path for every provider (ADR 0001); LoRA training is optional and needs a
  GPU worker.
- The Lock score adds an outfit check (colour and silhouette of clothing regions) beside identity, because outfit
  drift is the most common complaint (implemented in Phase 2, embedder per ADR 0002).
