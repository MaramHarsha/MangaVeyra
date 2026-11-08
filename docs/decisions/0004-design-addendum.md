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
