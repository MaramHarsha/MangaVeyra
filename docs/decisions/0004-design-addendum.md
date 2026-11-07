# ADR 0004: Design addendum — naming, missing states and additions

- Status: Accepted
- Date: 2026-11-07
- Applies to: the design bundle `MangaVeyra_All_Pages` (kept outside this repository) and every screen built from it

The design bundle is the visual source of truth. This record captures the corrections and additions agreed after the
design review, so they survive even though the bundle is not under version control.

> **Owner decision, 2026-11-07:** the HTML design bundle is final. The app matches it screen for screen, using the
> design's own copy, including the game layer shown in the design (§2 is superseded for anything the design draws).
> Milestone simplifications are not allowed (§6). A feature that does not exist yet keeps its designed control in
> the disabled "Soon" state (§7). Only states the design never drew may be added (§3), built from the design's
> components. The desktop artboards are the source of truth; the mobile artboards are not final and are not built
> to yet. Phones get the responsive version of the desktop design.

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

## 6. Gate A simplifications (removed 2026-11-07)

Gate A first shipped narrower versions of four designed screens. On the owner's decision these are being replaced by
the designed screens now, with unbuilt parts in the Soon state; the table records what each one was.

| Screen | Design | Gate A version | Status (2026-11-07) |
|---|---|---|---|
| B3 New project wizard | Five steps (Premise, Genre, Style, Format, Generate) with format cards and a live summary | One page with the same fields as dropdowns | **Replaced** by the designed five-step wizard |
| C1 Story editor | Arc/chapter/scene tree, compact panel table, Prose/Panels toggle, Script assistant, "Make pages from script" | Chapter list and a form per scene and panel | **Replaced** by the designed three-pane editor (assistant and "Make pages" in the Soon state) |
| G2 Models & providers | Provider tiles, capability tabs, model table with router use, status and cost | One card per text connection | **Replaced** by the designed tiles, tabs and model table |
| G12 Add custom provider | Four-step wizard for custom endpoints | A single key form for the fixed OpenRouter and OpenAI routes | Not built; "Add custom" is in the Soon state, and "Add provider" opens the key form for OpenRouter and OpenAI |

## 7. Unbuilt destinations

Navigation keeps every designed item in the designed order. Until a destination's page exists, its item renders
disabled with a small "Soon" tag and no link, and buttons that would reach a missing page or endpoint render
disabled with the same tag. No link or button may lead to a 404 or a missing endpoint.

## 8. States and controls the design does not draw (recorded 2026-11-07)

These exist because a real server needs them. Each one is built from the design's own components and sits where the
nearest designed screen would put it.

- **A2 Log in:** "Use a passkey" is shown as designed, in the Soon state, until passkeys exist.
- **A3 Sign up:**
  - Closed server: the designed "Join {server}" heading with a notice and a Log in button instead of the form.
  - The username is derived from the email address, because the designed form has no username field.
- **A4 Forgot password:** when the server has no email set up, the designed form stays, and a notice confirms the
  admin route after submitting.
- **G8 setup, step 3:** the GPU card also has "no worker" and "worker without a GPU" variants.
- **G6 404:** until Explore exists, the primary action goes to the studio (signed in) or Log in.
- **Top bar:** the compute meter shows status only until the queue page exists.
- **G2 Models:**
  - a "⋯" row menu (Test connection, Replace key, with a modal) and a scope line under the provider name, so each
    connection can still be managed;
  - the built-in placeholder row reads "Scripts · fallback" with cost "None";
  - the fal and Ollama tiles carry short descriptions with the Soon tag.
- **B3 wizard:**
  - step 5 (Generate) and the premise "Try:" suggestions have no artboard;
  - without JS all steps show stacked;
  - "Draft saved" reads "Not saved until you generate" (no draft saving exists);
  - pages per chapter is one number;
  - the routing chip says the route is set by model routing;
  - "Chapters to plan" is in the Soon state;
  - design copy that promises features that do not exist yet (reflow, the 12-chapter outline) is worded as not yet
    available.
- **C1 story editor:**
  - chapters are the top level of the tree (arcs are not stored yet);
  - there is no continuity banner until a checker exists;
  - each scene section has its own "Ch. N · Scene M" header;
  - on narrow screens the assistant pane hides below 1180 px and the tree on phones (responsive layout).

