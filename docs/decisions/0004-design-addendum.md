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
- **Top bar:** the compute meter shows status only (real queued/running count and load) until the queue page exists.
- **Shared controls:** unavailable primary and danger buttons that carry a Soon tag use a muted fill instead of
  40–60% opacity so label and tag stay legible (≥ 4.5:1). File uploads use the kit's dashed upload area (the G10
  "Import comics" pattern) around the native picker. Textarea character counters update while typing.
- **Sidebar:** a Soon sidebar item whose label does not fit beside its Soon tag (Japanese "エージェントとプラグイン")
  truncates the label; the full name stays in the tooltip and accessible name.
- **Shared forms:** mutation buttons expose busy state and reject duplicate submission without dropping their form
  intent. Character/setting forms use the shared modal to protect unsaved edits during in-app navigation and the
  browser's unload guard when closing a tab. Validation retains safe values; files must be reselected.
- **B2 Projects:** card badges use the artboard words: the member role on a project shared with you, "Script" while a
  chapter has a script but no pages, otherwise "Draft"; "Archived" is the one undrawn state, and the status filter
  offers the same states. Card meta is "Ch. N · {relative time}". Without JavaScript an Apply button submits the
  filters; with it the selects apply on change.
- **B3 wizard:**
  - step 5 (Generate) and the premise "Try:" suggestions have no artboard; the suggestions sit on one row of dashed
    chips so step 1 keeps Next in view at 1440×900; in Adapt mode the paste box and the .txt upload sit side by side,
    so Next stays in view in both starting points;
  - step 5 offers the ten manga content languages of ADR 0008;
  - without JS all steps show stacked;
  - "Draft saved" reads "Not saved until you generate" (no draft saving exists);
  - pages per chapter is one number;
  - the routing chip says the route is set by model routing;
  - "Chapters to plan" is in the Soon state;
  - design copy that promises features that do not exist yet (reflow, the 12-chapter outline) is worded as not yet
    available; the adapt mode's .txt upload uses the kit upload area.
- **C1 story editor:**
  - chapters are the top level of the tree (arcs are not stored yet); there is no continuity banner until a checker
    exists;
  - C1 shows one scene at /story/{chapter}/{scene} (the first scene at /story/{chapter}) with "Ch. N · Scene M" over
    the editable scene title, and the tree marks the open scene;
  - fields the artboard does not draw but generation reads are folded away: chapter summary, time of day and scene
    summary in one "Scene details" row above the table (which also carries the script provenance badge), and each
    row's page, camera angle, notes, line types with Remove, and SFX notes in a per-row "⋯" disclosure; an add-line
    icon under the panel label adds a dialogue line;
  - with JavaScript edits autosave after a pause and the save state ("All changes saved", "Saving…", "Unsaved
    changes", changed elsewhere, locked) sits on the crumb line like B3's "Draft saved"; without JavaScript a Save
    button in the header submits the form and Add/Remove line re-render the unsaved draft;
  - once a chapter has pages, "Make pages from script" opens the editor instead of queuing new pages;
  - "Import text into this chapter" (§4) sits at the foot of the tree in the Soon state; the assistant route chip
    reads "Auto → model routing", dimmed, and its Soon tag stays on the ask field;
  - with JavaScript dialogue wraps to the column edge under the speaker; without it each line keeps the speaker
    beside a text box (wrapped lines indent under the text); when the script assistant's title and route chip do not
    fit on one row (the Japanese title), the chip moves under the title;
  - on narrow screens the assistant pane hides below 1180 px and the tree on phones (responsive layout).
- **C2–C5:** global Characters and Setting destinations show a project chooser. Empty lists, no filter results,
  editable profile/reference/outfit/setting forms, upload errors, conflicts, and queued/failed sheet states use the
  shared cards, fields, notices and variation picker. Reference-only Character Lock shows its actual state rather
  than an invented trained score. Training, voice, Appears in and pack operations stay in the Soon state.
  Relationships keep the designed chip row with one dashed "Relationship" chip in the Soon state. The kanji watermark
  keeps the design's light ink and blends so it stays visible over light reference art. The role line sits inside the
  Role field after the role select (C4 and the C3 profile form). While a sheet job runs, the C4 form and sheet column
  scroll in their own tracks so the route chip and Generate stay at the foot of the viewport. After a sheet job is
  routed, the C4 route chip names the route it used (model · provider · estimate). C3 shows the kept reference sheet
  in the FRONT slot until a front view is uploaded. C5 setting cards keep the designed usage line with "Panel usage"
  in the Soon state until panel usage is tracked, followed by the item's description on the same line (truncated);
  the item's own tags use the designed tag badge.
- **C4 ready state:** completed reference sheets replace the in-progress job proof in the designed two-by-two
  candidate grid and keep their sheet captions; they do not retain an extra loading panel above the results. Keeping a
  look remains an explicit human action.
- **G2 Models:** a "⋯" row menu (Test connection, Replace key, with a modal) and a scope line under the provider name
  keep each connection manageable. The header carries only the designed Add custom / Add provider actions; the
  subtitle's "auto-routing" links to G11. Image rows show the published per-image estimate ("~$0.04 / img") and
  local-worker rows "GPU time"; the built-in placeholder rows read "Scripts · fallback" (text) and "Panels · sheets ·
  edits · fallback" (image) with cost "None". The fal and Ollama tiles carry short descriptions; Google and xAI keep
  disabled Soon destinations. While members may not add their own providers, Add custom is disabled with the same
  reason as Add provider, and G12 reached directly shows an "Ask an admin to add this provider" state built from the
  empty-state component. The OpenAI Test connection result, which has no artboard, appears in the row's notice using
  G12's test-result key/value pattern with status badges: "Text · Ready" and "Images · Ready / Not available for this
  key / Needs organisation verification"; the no-JavaScript flash from the image tab carries the same line. A row
  whose route the key cannot use shows the design's warning badge with an alert icon, as "Licence guard" does: "No
  access", with the full reason in the tooltip and for screen readers, or "Verify org" after GPT Image refused an
  unverified organisation.
- **G11 Routing:** the Guards box adds a "Private-network endpoints (local servers such as Ollama)" row, defaulting to
  MV_ALLOW_PRIVATE_PROVIDER_URLS; link-local and metadata addresses stay blocked. The spend row keeps "Monthly remote
  spend limit · $20" with its switch; admins edit the amount inline. With JavaScript guard and mode changes save at
  once with a toast; without it a Save button submits. People who cannot change routing see disabled controls with
  "Only an admin can change the routing mode and guards.", and the add-pin form is shown only to admins and project
  owners. Recent decisions show the task, "model · provider" with a remote route's per-call estimate in the compute
  meter's mono format ("~$0.08"), and the reason in words, including placeholder routes and "OpenAI images are not
  available for this key". After an automatic fallback, a second reason line reads "Switched from GPT Image 2.5
  Sunburst · OpenAI (rate limited)".
- **G12 Custom provider:** custom local endpoints use the designed four-step wizard; only explicitly registered
  provider types are served. "Next: test" saves a disabled draft and runs the model-list check (no generation, no
  paid content), so step 2 opens on the designed result (Status, Time, Cost; "Next: describe", or "Test again" after a
  failure). Back on step 2 reopens step 1 with the draft. While private-network endpoints are off, step 1 shows admins
  an "Allow private-network endpoints on this server" switch (on by default) and tells everyone else to ask an admin.
  Step 4 (no artboard) uses the G11 mode chips for "Auto-routing may use it in" (Balanced, Best, Fastest, Free only;
  Local only disabled) and the pins-row pattern for use, default mode and who can use it.
- **E2 Export:** CBZ is an additional format row using the designed format-card component. B5/A5/KDP trim selection,
  DPI and a page-numbers field (shown under the Pages control only while "Page range" is chosen) fill the designed
  print options; the middle Pages segment reads "Page range" instead of the artboard's sample "1–34". Format
  descriptions state the real defaults (PDF "Trim and bleed, 300 dpi by default"; video without a sample duration).
  Counts and the 64 MiB output limit replace invented sample size/time estimates. The primary button names the chosen
  format (Export PNG / PDF / CBZ); without JavaScript CSS `:has()` keeps it in step. The language select lists the
  project language and every language with text in the chapter, and "+N translation(s)" counts the others. EPUB,
  video and loops retain their designed rows in the disabled Soon state. The editor behind the modal is a read-only,
  inert server rendering of D1 from the chapter's real data; presence shows only the current viewer, the status bar
  reads "Saved" without an elapsed time, and Publish carries the Soon tag. Without JavaScript the modal renders as a
  complete static panel over the same shell.
- **D1 / D2 / D3 / D8 / D13 shared:** empty chapters, queued page creation, revision conflicts, scope locks and failed
  jobs use the existing notices, fields and status components. Numeric polygon/mask coordinates provide a keyboard
  alternative to dragging/painting. Furigana controls use the existing fields in the lettering inspector. Unlocking,
  deleting and reverting require an explicit human confirmation. The page-level AI scope, pose/sketch, automatic
  object selection, training and saved-layout controls remain Soon where their generation contracts belong to later
  phases. Default layer names (Background, FX, Lettering, Character) display in the interface language; custom names
  and character names display as stored.
- **Editor saving:** the editor autosaves a page about 1.5 s after the last change and at once on Cmd/Ctrl+S, mode,
  page or chapter switches and in-app links. The footer shows the save state where the design shows "✓ Saved 12 s
  ago" (Saving…, Unsaved changes, Not saved · changed elsewhere / locked / retrying). Revision conflicts and locks keep
  the Reload latest notice and the shared unsaved dialog. Confirmations (variation applied, layout applied, version
  restored) use the toast pattern over the canvas.
- **Editor top bar and footer:** below 1366 px the project name gives way to the chapter title, as in the D2 bar, and
  the chapter title truncates. On mode tabs, segmented items, icon controls and the Publish button the shared Soon
  tag sits on the control's top edge, so the designed widths and full names fit at 1440 px. The collaborator avatars
  are one disabled collaboration control with the Soon tag, in D1 and in the E2 editor shell, until real-time presence
  exists. Translate, Animate and Publish carry the Soon tag. The footer's right side shows "Self-host · {host}"
  (Pages; the same value as the app shell: the instance public URL or request host, including the port), the worker
  GPU and VRAM or "No image worker · {host}" (Variations), the lettering note (Lettering) and "Region edit · step N in
  version history" (Region).
- **D1 page and layer actions:** the "…" beside Pages holds Change layout, Move page earlier/later, Regenerate this
  page, Sketch to manga, Version history, Lock/Unlock page and Delete page; Regenerate this page and Sketch to manga
  are disabled with the lock reason while the page, a panel or an art layer is locked. The layout-grid-add icon adds a page and opens the layout picker. The Layers "+"
  adds a layer for a bible character not yet in the panel, or an FX layer. The dashed "Add" character chip is a native
  select styled as the chip; chips show the character's name and wrap to a second row rather than truncate. A page
  without panels shows "No panels yet" and Choose layout in the inspector; otherwise a panel is always selected.
  Canvas labels read "N.M · BEAT · CAMERA", taken from the first clause of the script action (or of an edited prompt)
  and a notable camera angle or shot size; they are ellipsized and clipped to the panel, show over art with a light
  halo, and move to the panel's top when the context toolbar covers its bottom. When a panel's camera or shot is not
  one of the options, Eye and Medium show as the default selection. Before generating, the routing chip and help line
  show the router's current choice for the panel ("{model} · {local|built-in|provider} · ~{s} s"; "no cost" or "on
  your API key"). "Layer properties · {layer}" (opacity, blend, offset, scale, rotation, flip) and D3's "More
  lettering options" (layer opacity, tail, box geometry, rotation, ruby/furigana, delete bubble) are collapsed sections
  at the end of the inspector: the design draws no controls for these document properties, and furigana is required
  by §4. Page thumbnails are renders of the saved page with art and lettering, matching the export shell.
- **D2 Layout picker:** the custom-grid tile is in the Soon state; the 3 × 3 preset appears under All as "Nine-panel
  grid". Suggested layouts are those within two panels of the page's script beat or panel count, best fit first. The
  hint states the script beat for the page when the chapter has a script, otherwise the page's panel count; a blank
  page says it has no panels and preselects nothing. Tile captions are the layout names, and each tile's panel count
  is in its accessible name. The dialog body scrolls so its footer stays visible.
- **D3 Lettering:** the bubble-type row shows and changes the selected bubble's type. With no bubble selected it adds
  one of that type, and a "+" in the Lettering header (the section-header pattern of Layers) adds another of the
  highlighted type in free space of the selected panel. Size is shown and entered in points, with the automatic size
  as placeholder. Vertical lettering is a switch shown only for Japanese, Korean and Chinese (ADR 0008). The font
  select lists the seven lettering fonts. The speaker field shows the speaker's initials. The "tail follows her" hint
  is not shown because tails do not follow speakers yet (the tail tip is edited under Geometry). "Keep bubbles off
  faces" stays Soon until face detection exists. Reading-order rows reorder by drag or with the arrow keys on the
  grip, and the order persists. Empty bubbles read "No text yet" (muted) in the reading order.
  Converting an auto-placed caption to Speech or Shout initializes its unused tail near the bubble, using the same
  placement as a new speech bubble. Previously positioned tails remain where the creator put them.
- **D8 Variations:** the status line reads "{ready} of 4 ready · {seconds} s · {model}" from the job's real timing.
  Lock captions and DRIFT appear only when a job reports lock scores. The queue lists the user's recent panel and
  region jobs in the chapter; Open queue links to G9. Repeating an identical panel or region request while its job is
  queued or running returns that job instead of starting another.
- **D13 Region edit:** region editing fits the actual layer's aspect ratio within the desktop proof area, and the
  panel's screentone shows through transparent layer areas. The compare handle, comparison area and four-tile strip
  are shown before a result exists; the idle handle lets brush strokes through. Tiles are captioned VAR 1–4 (the
  design's "HAND n" names come from a prompt subject the app does not infer). "Clear" in the Select heading empties
  the selection. Brush size, rectangle coordinates and the keyboard note sit in the collapsed "Brush size and
  coordinates" section. Feathering keeps an opaque selected core, consistent with the
  frozen exact-white mask contract. Automatic object selection and Character Lock/style-preservation options stay
  visibly Soon.
- **Editor without JavaScript:** the shell shows the page scaled to the canvas with its art, page thumbnails and
  export, and the inspector lists each panel's prompt and layers read-only.

## 9. Phase 2 activity states (G9/G4)

The desktop artboards define these screens' layout, copy, iconography and hierarchy. G9 keeps the full queue table,
worker action and three summary cards. G4 keeps the full-width shell without the Studio sidebar, the four category
segments, Mark all read and the compact 760 px notification list. Existing tokens and UI-kit owners are reused.

- **G9 real data:** rows use the actual job type, panel number when available, member, route, state and progress.
  Members see their own jobs. Admins see their own jobs by default and every member's jobs through an explicit "All
  users" filter (`?all=1`, URL-restorable), drawn as the existing segmented-link control before the worker button;
  the This week summary follows the same filter. No registered online or paused worker means the subtitle identifies
  server/remote jobs; while every worker is paused the subtitle keeps the GPU or worker name and reads "{n} running
  slot(s) paused". No hardware name or worker slot count is fabricated. Online worker slots come from registered
  worker capacity.
  Per-user limits distinguish configured GPU/server limits. Night mode uses its configured window and server time.
  This week's summary uses recorded actuals for finished top-level jobs since Monday 00:00 UTC; parent actuals
  include their child work. These values replace the artboard's sample metrics. A worker job started by a parent
  job (Character Lock assessment or training, local image generation) is part of that job: the queue shows one row
  whose bar uses the running child's measurement, never ahead of the parent's measured steps; the child's detail link
  opens the parent, and canceling the parent also stops the child. An unacknowledged cancellation reads Stopping in
  the warning tone; finished jobs show their final state. Remote spend uses two decimals ("$4.18"), amounts below one
  cent read "<$0.01", queued estimates read "~3 min"/"~$0.24", and a finished job's time reads "38 s", "2 min" or
  "1 h 5 m". Character Lock scoring models absent from the routing catalogue are named DINOv2, CCIP, CCIP + outfit
  and CCIP + outfit views.
- **G9 action states not drawn in the handoff:** reuse the shared dropdown for View details, Review in Studio and
  cancellation. Retry is reviewed in the original Studio flow so scope, revision and spend approval remain there.
  A selected job expands a shared Box/KV/Notice section below the designed queue and summary, with at most 50
  sanitized recent event kinds/timestamps. No prompt, payload or raw provider/worker log is shown. A single online or
  paused worker uses the designed button, labelled and iconed by the action it performs (Pause worker with the pause
  icon; Resume worker with the play icon when every worker is paused); several use the existing dropdown with named
  actions. Offline workers are not counted. No worker
  or insufficient role disables the action with its reason. Pause drains new claims and preserves running work.
- **G4 real data:** titles and bodies are persisted notification content; times, controls and read status follow
  the current interface locale. Notification text retains the language in which it was created. Category filters
  use the existing segmented-link component; Mark all read covers the selected category for the current member and
  is disabled when that category has nothing unread. Rows are ordered by the time shown, newest first (a regrouped
  notification moves to the top). Job notification bodies are a summary in the recipient's language — project ·
  what was made · duration ("Tidebreak Courier · Panel 1.2 · Regenerate · 38 s") — never a job type or worker text;
  older rows that stored a raw job type show that job's localized label.
  Clicking a row explicitly marks it read and opens an existing local destination. Missing or external destinations
  return to the notification category instead of offering a dead/external link.
- **Necessary empty, paging and recovery states:** use the existing State, Notice and button components. Cursor
  pages use Latest/Older below the list. Empty states explain where future activity appears; they do not fabricate
  sample jobs or notices. Queue polling retains loaded content on errors and offers Refresh, stops when idle and
  does not replace an open menu, focused control, dialog or pending form. No-JavaScript views use ordinary complete
  pages, POST forms and Refresh. Timestamps explicitly label UTC.
- **Narrow widths:** existing responsive patterns stack queue rows/cards and wrap header actions. The unapproved
  mobile artboards remain deferred, following the owner's instruction.

## 10. Phase 2 control states (D4/D10/D12 and C3/C4)

The approved desktop workbenches use the full canvas plus a 340 px inspector, a 52 px bar and the handoff's
two-proof composition. D4 uses 340×480 proofs and an arrow; D12 uses 380×300 character proofs, the warning outline
and the arrow. D10 uses 400 px proofs, compact history rows, branch section and paired footer actions. The normal
filmstrip/footer and redundant history shortcut are hidden in these workbenches. No mobile-artboard layout is used.

- **Actual data:** chapter/page/character names, reference thumbnails, measured drift, versions, route and estimates
  replace samples. An absent result uses the existing dashed State/Notice pattern; it never shows a fabricated
  97% repair score. A successful measured repair uses the existing success token; a low result remains a warning.
- **D10 basic history:** one real `main` branch and the latest 50 snapshots are shown. The selected snapshot is the
  second proof with the designed accent outline. Restore uses the existing confirmation dialog and creates a new
  revision. New branch/Merge branch stay visible, disabled and labelled Soon until Phase 6. UTC timestamps and
  localized action labels replace sample author/time/change strings. A Close icon follows the existing inspector
  pattern so the creator can return to the normal editor.
- **D4 source and detection:** Upload and Draw here are active; Camera remains disabled Soon. The three designed
  check rows describe the actual saved layout, character layers and editable speech. Automatic detection is Soon,
  rather than claiming invented detected counts. A helper states limits and the unchanged-content scope. Pointer
  drawing has an upload alternative; the canvas dimensions are bounded even for extreme page aspect ratios.
- **D12 unavailable/evidence states:** no kept reference, unassessed/outfit-pending, locked targets, no eligible
  worker and failed/canceled work use existing Notice, badge, progress and disabled-control patterns. Selections
  are bounded to 24 character layers per action. A narrow helper explains that limit. Quote errors retain the
  workbench and disable dispatch until Refresh succeeds. Leaving aborts obsolete requests and protects an unsaved
  sketch. The before proof is retained after repair; the drift list and selected target revisions refresh.
- **C3/C4 evidence:** the existing Character Lock card contains actual status, evidence count, pending outfit count,
  optional training/assessment actions, queue progress and the D12 link. Similarity is explicitly not an identity
  guarantee. Missing capabilities/three distinct references keep training unavailable. No sample percentage or
  invented training duration is shown. C4 links to the real character status after keeping a reference.
  The measurement caption names the actually selected CCIP or DINOv2 method; changing model/weight revisions
  never mixes different methods in a displayed aggregate. A newly selected model remains unassessed until fresh
  evidence exists. Whole-percent labels truncate measurements so a failing score cannot round up to 90%.
  This changes no artboard geometry or components.
  The designed 36px, twelve-bar chart now uses up to twelve recent current-method measurements, with accessible
  numeric descriptions. Missing evidence never creates placeholder bars. D12's kept-reference thumbnails retain
  the designed frames and contain the full images, matching the existing reference-card pattern, so a portrait's
  face and boots are not cropped away.
  The existing reference-upload form explains that outfit views bind the current default outfit. This is needed
  to keep variants separate; it adds bilingual helper text using the existing form/help pattern. The measured
  caption distinguishes learned matching to kept outfit views from raw cosine. Actual longer names wrap and
  the necessary readiness/actions make C3 taller than the sample, without changing its grid or components.
- **C3 drift line:** the drift line follows the handoff copy and amber tone ("1 drifted panel in Ch. 8 · Fix"). It
  counts drifted appearances in the lowest-numbered chapter that has drift, which is the chapter its Fix opens in
  D12. The score, chart and D12 still cover every assessed appearance; once that chapter is fixed the line names the
  next one. The lock icon takes the card's state colour (green pass, amber drift). The worker helper appears only
  while no scoring worker is online.
- **D1 entry points:** the design draws no D1 control for D4 or D12. The character chip's designed lock score
  ("🔒 96%") opens D12 for that character, D8's drift Fix opens D12 for the drifted character, and Sketch to manga
  sits in the page "…" menu. No inspector button is added, so Style stays in view at 1440 × 900.
- **D10 actions and comparison:** the newest snapshot is the saved page, so its row offers the design's Undo, which
  restores the snapshot before it through the same confirmation and a new revision; older rows offer Restore.
  Restoring is not destructive, so the confirmation uses the primary tone. The compared proof outlines, with the
  design's dashed accent line, each panel whose geometry, prompt, camera, style, pose, locks, art layers or lettering
  differ from the saved page; its caption counts those panels and panels added since ("Version 8 · 3 changes", or
  "no changes"). Signed URLs and derived lock scores are not changes. A restore that meets the version fence shows
  the shared conflict notice with Reload latest inside the workbench. Rows keep the design's 16 px inset and 52 px
  height; the compared row uses the branch rows' surface and accent bar. Footer Soon tags sit on the buttons' top
  edge like the other editor Soon tags.
- **D12 evidence states:** the before proof outlines the visible pixels of the character layer with the design's
  dashed warning line; no face or part region is inferred, and an opaque layer gets no outline. Its tag names only
  what stored evidence shows ("{name} · identity differs", "outfit differs" or both), and a repaired result carries
  "Only {name}'s layer regenerated". Before anything is measured the caption reads "Before · not assessed" in the
  neutral caption colour and the subtitle counts the chapter's unassessed panels. The route chip asks for a
  selection, shows "Estimating…" or names a lock; only a failed estimate reads "Estimate unavailable", next to the
  notice's Refresh. A finished job reloads the selected targets' revisions before the next estimate. A character
  outside the project opens a chooser built from the shared select. Japanese uses the C3 term キャラクターロック.
  Assessment is read-only for artwork, so locked approved art can be assessed without unlocking it; repair and
  generation still refuse locked targets.
- **D4 proofs and limits:** both proofs use the page's aspect inside the designed 340 × 480 frame; in Draw here the
  drawing help and Clear sit below the frame so the proofs stay aligned. The caption names the source ("Your sketch ·
  uploaded" / "drawn") and the frame carries the design's "Rough pencil · page N" tag with the real page number.
  In-proof tags (D4 and D12) use the reference-caption chip (surface background, secondary text) so they stay
  legible over any sketch or art. Strength uses the editor's thin slider. Type, 16 MiB, 64–4096 px per side and 16 MP
  are checked before upload and again on the server, with one message. Switching to Draw here replaces an uploaded
  sketch. Locked panels or art layers disable Ink this page with the lock reason under the button. The "Art only · N
  layers · lettering kept" helper appears under Ink this page only.
- **Pose input:** the existing Leap/Crouch/Run cards show their own skeletons. Sketch opens the normalized joint
  editor; arrow keys (Shift for larger steps) and pointer dragging move joints. Its helper and clear control use
  existing inspector patterns. Generated control images are machine inputs, not a new product illustration style.

Desktop and basic 390×844 evidence is recorded in [the control QA record](../qa/phase-2-control-2026-11-08.md).

## 11. Phase 3 screens

The approved desktop artboards are followed at 1440 × 900. Phones get the responsive version of the desktop design;
the mobile artboards stay deferred (ADR 0012 §11).

- **A1 Landing (`site/`).** The hero badges add a `Pre-alpha` warning badge. The hero command chip keeps the design's
  `docker compose up`, which works from the repository root through `compose.yaml`; the self-host terminal adds the
  required `cp .env.example .env` and spells out the clone URL, with the design's output lines unchanged. The
  designer note beside the sample page is built as the reveal-and-slash animation it describes (a 120 ms fade
  without the slash under reduced motion), not shown as text. Step 03 carries a `Publishing soon` tag and the
  Panel-aware reader card a `Soon` tag until the reader ships. Showcase covers are labelled `Placeholder cover`, with
  the note "Original sample titles. Covers are placeholders until the first series are published." The first answer
  adds "MangaVeyra is pre-alpha: the studio and exports work today, and publishing, the reader and the community are
  being built now."; the four collapsed answers, which the design does not draw, describe shipped behaviour. The
  footer's Community item is a `Soon` item and Security links to the README's privacy and security section. The page
  follows the operating-system theme (Gate night by default, Paper when the system prefers light), keeps ink text on
  the Gate-blue band in both themes, and uses `--mv-acc-text` for large accent text on Paper. Fonts, the icon font and
  logos are self-hosted with their licences; the page makes no external requests and runs no JavaScript.
- **E6 Explore.** The featured series is the listed series with the newest public chapter. Its eyebrow reads "New
  chapter today" (as drawn), "New chapter" within seven days, "Latest chapter" otherwise, and "Featured series" when
  no chapter is readable. Key art is the banner, else the cover, else the kit screentone labelled "Featured key art".
  Anonymous visitors are sent to Log in to follow. Rankings use only activity recorded on this server: Rising is the
  last seven days' growth in reads plus new followers against the seven days before (only growth above zero is
  ranked), Week is reads in the last seven days and All time is the series view count; with nothing recorded the card
  reads "No rankings yet". The genre chips and the "Art: any / Hand-drawn only" toggle filter the shelves below them,
  while the featured series and rankings stay server-wide; hand-drawn means every AI label role is human, and without
  JavaScript an Apply button submits the toggle. Members with reading history get "Because you read {series}"
  (shared genres, then tags, then recent reads, without series they read); everyone else gets "New & updated",
  newest public chapter first. More opens Search with the shelf's genre. "Rising creators · first series" ranks owners
  with exactly one listed series by follows plus reads in the last 30 days, with an empty note when none qualify.
  Cards show the cover image or the kit screentone labelled "Cover", and "New ch." marks a public chapter published in
  the last seven days. A server with nothing listed shows "Nothing to read yet".
- **E8 Search.** Tabs are Series N, Creators N (creators whose name contains every word, among owners of the
  results), Chapters in the Soon state, and Tags (the results' tags with their counts, without a tab count as drawn).
  Words match by prefix and any word may match, ranked so series matching more words come first; creator names and
  CJK substrings also match. The snippet quotes the synopsis around the first match with matches marked. Filters
  apply on change (Apply without JavaScript), Clear filters appears once a filter is set, and Has Japanese means a
  Japanese original or edition. Results page by 20 with Previous/Next. Empty states read "Nothing matches
  “{query}”", "No series match these filters" or "No published series yet". The search box is not auto-focused.
- **E6/E8/E5/F1 access.** Anonymous visitors on personal and private servers are sent to Log in (ADR 0012 §1).
- **F2 Analytics.** A series selector sits beside "All chapters" only when the member has more than one series; F2
  opens on the most read one. Reads shows its change against the twelve weeks before only when that period had
  reads; Followers shows followers gained in the window; Completion and "ch. N" describe the selected chapter, or with
  All chapters the highest-numbered chapter read in the window; Avg. time / chapter is recorded page time per read;
  missing values read "—". Weekly reads covers twelve Monday-based UTC weeks with an accessible list of the values,
  and followers per week are given to screen readers on the Followers card, since the artboard draws no followers
  chart. Drop-off rows fall every ⌈pages/7⌉ pages plus the last page (every page up to eight pages); the largest drop
  uses the warning tone. The note reads "Biggest drop at p. A–B" without a cause, which the server cannot know, or
  "No drop so far". Empty states cover no series, no reads and no published chapters. There is no revenue panel, and
  real counts replace the sample numbers.
- **Segmented controls** in E5, E6 and F2 use the kit's 13 px labels where the artboards draw 12 px.
- **E1 Publish flow.** The visibility segment "Followers" is labelled "Members": the `members` visibility means
  signed-in members of this server (ADR 0012 §1), not followers. AI label rows are selects offering the recorded
  level and anything above it, so a correction never shows less AI than the project history; the per-role detail
  ("212 panels, 41 hand-edited", names) is not shown because no per-panel statistics are stored. Tags are a
  comma-separated field with the chips shown below it, since the in-field chip editor needs script. Schedule uses
  native Date and Time inputs, shown only while Schedule is chosen, with the user's time zone (UTC when unset) in
  place of "· local", which the server cannot know without script. "Offer download for offline reading" and "Allow
  panel comments" are disabled with Soon until publications carry per-chapter flags. "Preview as reader" links to
  the reader once the chapter is live and is Soon before that. The primary button reads "Publish now" or "Schedule"
  following the When control, and "Update published chapter" for a live chapter, next to an "Unpublish"
  danger-outline button. Existing kit components add: notices above the grid (first chapter creates the series;
  series stricter than the chapter; scheduled; unpublished; publishing progress with polling and Refresh; failure
  with the public job error), a "Series settings" link under Licence, an in-page grid of rendered pages for "Choose
  from pages", generated cover candidates with Keep buttons under the cover, an "Alt-text" review section below the
  grid (one disclosure per page, a textarea per panel, ADR 0012 §5), and language options that show unlettered
  languages with a bubble count and cannot be chosen. The editor's Publish opens E1 for the owner and editors and is
  disabled with an explanation for other members.
- **E1 Published.** "QUEST COMPLETE · +1,200 XP · LEVEL 24 REACHED" becomes "Quests and XP" with a Soon tag (the XP
  layer is deferred, ADR 0001; no invented numbers). Share is a no-script disclosure showing the chapter link. "Publish
  in {language}" appears only when another lettered language is ready and not yet live, and the follower count is the
  real number notified. A "Back to publish settings" link is added; a scheduled chapter shows "Chapter N is scheduled
  · Goes live {date}". The Japanese UI letters the SFX ドーン！ in the JP SFX face.
- **Series settings (`/studio/p/{project}/series`, no artboard).** Built from E1's three-column grid and fields:
  read-only cover on the left; title, address with a `/s/` prefix, synopsis, release note, genres, tags and five
  support-link rows in the middle; visibility, format, age rating, status, licence, original language, creators (owner
  first, members with a role and an optional display name) and published-language chips on the right.
- **E5 Series page.** The creators line credits the series' creators in credit order ("by Jerry Okafor & Jun Sato"),
  each linked to their F1 profile, followed by the release note (or the status when none is set), format, age rating
  and licence. AI badges show every stored role (art, script, lettering), sparkles for AI-assisted/AI and the pencil
  for human. Chapter thumbnails keep the designed screentone placeholder. Rows read "date · N pages" with New for
  chapters published in the last seven days. The languages stat doubles as the language switch (codes are links; the
  current one is underlined in the accent). Under the designed comment list, members get a "Write a comment" field
  with Post and visitors "Log in to comment"; each comment has a "⋯" menu (Edit and Delete for its author, Hide from
  readers / Show to readers for the series owner, Report for others) that opens inline forms built from the shared
  field, select and textarea; hidden comments carry a Hidden badge, edited ones an "edited" note, and Newest/Older
  comments links page after five comments. The support box is omitted when the series has no support links. Mature and
  adult series show signed-in readers a one-time confirmation step built from the boxed empty-state pattern ("!?",
  "{title} is rated Mature 17+", "I'm 17 or older", Back to Explore) over the banner; the owner previews without it.
- **F1 Creator profile.** The sub line is the A5 creator type, the server host and the first line of the bio. The rank
  line uses the B1 rank badge from users.xp/level, follows the viewer's ranks-and-XP preference and reads "READER" for
  readers. The avatar colour comes from the account, not the sample yellow. Link buttons are the creator's profile
  links (up to three) labelled by host. On your own profile Edit profile replaces Follow. Series cards show the release
  note (or status) and chapter count. Chapters lists the 30 newest chapters across the creator's series with the E5
  row; About shows the bio, links, @username and join date. A creator without readable series gets the empty state
  (シーン…, "No series yet", with Open the studio on your own profile or Explore series otherwise). Genre badges show
  the stored genre words.
- **E3 Reader.** Reading settings adds Zoom and Brightness as two kit sliders under Background, because the feature
  plan names both and the artboard has no control for them; Zoom also follows + / − / 0. Play living panels and the
  top-bar reader-translation icon keep their designed places in the Soon state. Read aloud from script is disabled with
  "No voice on this device. Read-aloud only uses voices that run on your device, so the chapter text never leaves it."
  when the browser has no on-device voice (ADR 0012 §6); otherwise the row names the voice. Language lists the
  languages the chapter is published in. Anonymous readers see "Saved for this series on this device". The panel's close
  button is visually hidden until it has keyboard focus. Guided view on desktop, drawn only on the mobile artboard,
  uses its pieces: the panel framed with a 3 px border, the "PANEL n OF m" label and the dimmed surround, then the
  alt-text line and the back chevron, Single | Guided and next chevron row; the frame stays left of an open settings
  panel. A chapter-end card (from the research's chapter-end card) shows "End of chapter", the per-role AI-label badges
  (ADR 0012 §4), Previous chapter, Next chapter and Series page. With Show panel comments on, a Comments button at the
  stage's bottom left opens a 320 px drawer in the settings panel's place listing the open page's panel comments and
  the chapter comments (comments whose anchor no longer exists after an update are listed with the chapter); members
  get a form, others Log in. Offline, a status line under the bar uses the G6 copy. Without JavaScript the bar keeps
  Back, the titles and the Soon translation icon, and pages stack in a 720 px column with alt-text before the
  chapter-end card. The age confirmation (not drawn) is a box under the reader bar with the rating badge, "This series
  is for mature readers" / "This series is for adults only", "{title} is rated {rating}. Confirm that you are old enough
  to read it.", "I'm old enough · Read", "Go back" and "We remember this for this series only." On phones the bar drops
  the mode switch and Reading settings opens full width with a Reading mode group at the top.
- **E4 Webtoon.** Shows the same Download control as E3 (between Bookmark and Settings) so episodes can be read
  offline. Episodes lists the six latest; "Next episode ↓" appears only when there is one.
- **E7 Library.** Category is disabled with a Soon tag (user categories are not built) and Auto-download new chapters
  is disabled Soon. Sort offers New chapters first / Recently read / Title A–Z, with Apply when JavaScript is off.
  Following shows a count when follows exist and Downloads/Bookmarks only above zero; History has no count, as drawn.
  History, Downloads and Bookmarks, not drawn on desktop, use the Continue reading card in the same three-column grid.
  Downloads adds the E7-mobile summary "{n} chapters · {size} on this device", the note "Downloads stay on the device
  that saved them and expire after 30 days.", a status icon and a remove button; rows saved on another device say "Not
  on this device". Bookmarks add a remove button and "· panel n". The empty Following tab reads "Nothing followed yet"
  with Explore series. On phones the H1 is hidden because the mobile header already says Library.
- **E5 downloads.** Members get each readable chapter's download icon as a link that starts the offline download in
  the reader; visitors, who cannot download (ADR 0012 §7), see no icon.
- **E1 conflict states.** Built from the existing Notice component with the alert-triangle icon: a 409 error notice
  when the chapter or series changed after E1 was opened ("This chapter or its series changed after you opened this
  page. Nothing was published: review the details below, then publish again."), and a 409 warning notice while a publish
  of the same chapter and language is running ("This chapter is already being published in {language}. Nothing was
  changed: publish again once it finishes."), followed by the job progress notice only for the viewer's own job. A
  scheduled chapter whose scheduler lost the right to publish reuses the unpublished notice.

## 12. Phase 4 screens

The approved desktop artboards are followed at 1440 × 900; the mobile artboards stay deferred (ADR 0013 §10).

- **G13 Agents & MCP.** The header switch is the member's "Allow AI agents (MCP)" setting (accessible name from the
  mobile artboard; a Save button without JavaScript); when it is off a warning notice reads "AI agents are off. Your
  tokens are kept, but every agent request is refused until you turn this back on." Connect an assistant shows the
  server's own `/mcp` address. Create token opens a dialog built from the kit's modal and fields (Name; Projects: all or
  a checklist; Access: Read always on, "Edit scripts and characters", "Generate with your models"; an approval threshold
  with a `$` prefix, default 0.50, empty never asks; Expires: 30 days, 90 days, 1 year, Never); without JavaScript it
  renders open under Connect an assistant. After creation a success box shows the secret once with the
  `mangaveyra mcp-stdio --url … --token …` command. Token rows keep the three drawn lines ("Approve over $0.50", "No
  approval needed" or "—"; "Expired" in the warning colour). Recent agent actions lists real rows newest first (every
  waiting request plus the latest ten others) with Undo where version history supports a revert and, on a waiting
  request, the kit's small Approve and Decline buttons for the token owner and the project owner; Approve on a
  human-only request opens its real screen (E1, the editor) and is disabled with Soon while that screen does not exist
  (project deletion, sharing, roles). Empty states describe where items appear. MCP client (v2) keeps its card with
  Connect a server in the Soon state.
- **D7 Collaboration & review.** `?panel=review` replaces the inspector with the 340 px review drawer and gives the
  editor D7's workbench layout (the left rail, footer, undo/redo, history, zoom, Preview, Export and the presence
  avatars give way; the page sits at the artboard's 60 / 30 px offset). The drawer header adds the inspector's close (×)
  and a "Comment on panel N.M" button (the D1 panel toolbar's Comment opens the same composer: a textarea, an optional
  "Assign to" member select, Cancel / Post). A status chip the viewer may change carries a chevron opening the allowed
  statuses (Reopen, Needs changes, Mark done, Approve). Open lists every comment not yet approved; the header counts
  those as open and approved ones as resolved. Each roles row shows the member's one project role with its limit
  ("Letterer · lettering layer only", "Artist · pages 1–20"); the artboard's "Owner · Writer" has no equivalent because
  a member holds one role. Owners see a pencil per member (Role, "Can change": everything the role allows / lettering
  layer only / only some pages, Remove from project) and an "Add member" row. "Approve page N" is disabled for members
  who do not review and while the current version is approved; "Approved by {name} · {time}" (or "· changed since")
  sits above it. Below 768 px the drawer stacks under the page.
- **D1 presence and pins.** The disabled collaboration control becomes the designed avatar stack (people on the page
  first with the online ring, then other members, up to four and "+N"), opening the review drawer. Cursors are the
  filled pointer with a name label in the member's avatar tone. Pins for open comments are numbered in Open-list
  order, sit at the comment's anchor (the panel's top-left corner by default) and step sideways when several share a
  corner.
- **G5 Admin · Reports and disputes.** Review opens a review panel under the row (not drawn), built from key-value
  rows, quote boxes and buttons: the reported thing, reason, reporter, time, the account, the reporter's note, the
  reported comment and, for AI-label disputes, the per-role labels as provenance badges. A "Note for the log" field
  sits above only the actions that apply (Ask the creator to review the label, Hide comment, Take chapter down, Take
  series down, Suspend {name}, Dismiss). A resolved report shows its outcome. Dispute rows add the labels on a mono line.
  A "Taken down" card lists each takedown with Restore ("Hidden from everyone outside its project. Nothing was deleted;
  restoring shows it again."). Empty queues use the kit's empty state. The header action reads "Update to {version}"
  when the update module knows a release, and "Updates" otherwise.
- **G5 Admin · Users.** Member rows add a mono line "{n} projects · seen {when}". Sign-up is followed by an "Approve
  open sign-ups" switch; "Waiting for approval · {n}" (Decline / Approve) and "Invites" (read-only link with Copy link,
  role, who may use it, "{used} / {max} used · expires {date}", Revoke) cards follow the Limits and Roles cards. Invite
  opens an "Invite someone" dialog (Role, Expires, Uses, Email). The ⋯ menu offers Suspend / Reactivate / Approve, a
  reset-password link shown once, and Delete account (typing @username to confirm). Limits make the enforced limits
  editable and add Other running jobs, Upload size (read-only from `MV_MAX_UPLOAD_MB`) and the whole-server remote
  spend; Storage quota and Projects per member stay Soon until something enforces them.
- **G5 Admin · Content rules.** The artboard's single "Allow Mature 18+ series" becomes "Allow Mature 17+ series" and
  "Allow Adult 18+ series", matching the four age ratings (ADR 0012). "Default age rating for new series" and "Licence
  guard" rows are added. "Require an AI label on every chapter" is an always-on disabled switch. NSFW filter, Auto-hold
  reported chapters, Comments, Panel comments and "New members need approval before publishing" keep their places in
  the Soon state showing the real state. "{n} words · edit" opens the list in place. Server rules gets a Save button.
- **G5 Admin · Server.** The Instance card adds Description; Services adds Job queue and Uptime; Database reads "{name}
  {version} · OK · {size}". Maintenance mode opens a dialog (message, Turn on / Save message / Turn off) and the card
  shows a warning notice while it is on. View logs is Soon.
- **G2 Models (Phase 4 providers).** Google and xAI keep their designed Add provider places and open the one-page key
  form. Replicate, Together and Runware, which have no designed place, follow fal among the hosted aggregators
  (OpenRouter, fal, Replicate, Together, Runware, OpenAI, Google, xAI, Ollama). Rows of fake-transport-tested routes carry
  the meta line "Fake-transport tested · not live-verified". A Hugging Face dedicated endpoint's cost reads "Billed per
  endpoint hour", and a serverless connection not yet tested reads "Listed rate at the test". G12 image connections
  appear in the Image tab with the connection's name and "GPU time".
- **G12 (Phase 4 connection types).** On step 1 of a new connection the four designed types are segmented links
  (OpenAI-compatible, ComfyUI workflow, Hugging Face, HTTP); once a draft exists the designed switch shows the type
  without links. ComfyUI step 1 takes the server and the workflow exported with "Export (API)"; step 2 shows "Map
  inputs" as designed, with each row as a select after Back. The ComfyUI test checks the server and the workflow's nodes
  without generating, so the result reads "NODE CHECK · N nodes on the server" instead of the sample test image, and Cost
  reads "Local GPU". Hugging Face and HTTP step 1 (no artboard) use the existing fields, segmented controls and textarea.
  Step 3 for image types (no artboard) asks for the largest width and height, images per run, and "Its model licence
  allows commercial use" (off by default). Free only is disabled for Hugging Face in step 4.
- **G14 Update available.** The third badge shows the signed download size ("46.8 MB download") instead of "~3 min ·
  no downtime for readers": no duration is measured and readers see G6 Maintenance while an update runs. A minor
  release shows "Minor · new features" (info), a major one "Major · read the notes" (warning), and a release with a
  security fix adds "Security fix". Step 1 reads "Database, settings and projects · last backup {size}" (omitted when
  none); the destination is not shown. Step 4 reads "App, database and storage tested", the checks `/readyz` makes.
  "Full changelog" appears only when the manifest has `notes_url`. Without a backup service, a trusted key or a download
  for this platform, a warning notice explains why and "Back up and update" is disabled; in a container a notice says the
  backup is taken and the host commands follow. The close button leads back to Admin.
- **G14 Updating / Done / Failed, restored.** Updating's subtitle reads "Don’t close this tab. The studio is paused;
  members see the maintenance page."; the footer shows "Started {time}" without a remaining-time estimate; step details
  use measured values and only the download shows a percentage; without JavaScript the page refreshes every 3 s (8 s
  while restarting). Done's footer note is a link to the plain-text log; step 4 adds "· {n} workers online" when any is
  online and says the backup is kept for 7 days when chosen. Failed names the failed step or migration (UPD-MIGRATE-0602)
  with a short cause and advice for that failure; a failure before the backup reads "Nothing was changed. Your server is
  still on {version}."; a failed restore keeps maintenance on and gives the restore command. Copy log opens the log
  without JavaScript and copies it with JavaScript.
- **G14 states without artboards** are built from G14's window: Update settings ("Check for updates", release address,
  channel, the 7-day and 03:00 toggles, last update, Check now, Save), Container ("Update to {version} on the host", the
  backup step and the exact Compose commands), and the members' banner (a kit info notice: "Update {version} available.
  Only an admin can install it on {host}; you don’t need to do anything."). The window uses `--mv-glow-status` and the
  backdrop `--mv-scrim`. G6 Maintenance shows the update's progress line ("Started 21:04 · step 3 of 5 · migrating
  database") while an update runs.
- **G10 Backup & import.** Built to the desktop artboard (two cards and the dashed Export everything row; the sidebar
  highlights Settings as drawn). Last backup reads the newest archive this server wrote ("Never" before the first),
  Schedule reads "Daily · keep N", "Weekly · keep N" or "Off", and Destination shows the backup folder; both open a
  "Backup schedule" dialog (frequency, day, time in the admin's time zone, keep 1–365, a destination folder that must
  exist and be writable). Restore… opens "Restore a backup" (a warning notice, archives as radio rows, "Type restore to
  confirm", a danger Restore button, "Or upload an archive"). The Backups card adds a progress line while a backup,
  export or restore runs (polled; Refresh without JavaScript) and an "Archives" list (kind · time, size, download, delete
  with a confirming dialog). Notices above the grid report a restore, keys sealed with another master key, a refused
  archive and a failed backup. The import drop zone is the kit's file picker with an Import button; without pdftoppm the
  prompt reads "Drop CBZ, CBR or image folders" with a "PDF" Soon tag. Queue rows read "Queued · N images", "Reading ·
  N%", "Detecting panels & bubbles · N%", "Creating pages · N%", "Done · N pages" with Open, or "Failed · {reason}" with
  Retry. The Export everything row adds a quiet button and, once an export exists, "Download · {time} · {size}".
- **G1 Settings · Data & export.** Export everything downloads the member's own archive (their account without
  credentials, projects, characters, published works, comments and reading data); Last export shows "{date} · {size}"
  as a link to the newest export, kept for 7 days, or Never.
- **G13 and G12/G2 after review.** G13's success box shows `MV_AGENT_TOKEN=… mangaveyra mcp-stdio --url …`; Recent
  actions read "Can't undo: edited since" where history holds a person's edit, and a waiting generation whose price rose
  shows the new estimate before it can be approved. A member choosing ComfyUI or HTTP in G12 sees an "An admin connects
  this server" state (403) built like the existing Blocked state with the type switch kept; the ComfyUI and HTTP first
  step has no scope picker. Test connection is available on a custom connection that is off, and the ON switch refuses
  an untested one with a notice; a connection changed since its passed test shows "Not checked".
