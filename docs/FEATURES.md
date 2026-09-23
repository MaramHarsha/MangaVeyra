# MangaVeyra feature plan

Source of truth: the design project’s *01 Research and Feature Plan* (Rev 6). This file is the open-source repository’s copy of that plan, used to track what is built. Platform-only features (hosted credits, payments, paid marketplace, revenue splits, payouts) belong to the closed World platform and are **not** part of this repository; they are listed only so contributors know where the boundary is.

**98 features · 55 adopt · 24 improve · 19 innovate · 74 MVP · 21 v2 · 3 future · 36 research-driven · 10 Platform only · 13 self-host only**

Legend: `R` = research-driven, `U` = the owner’s idea. Edition: *Both* = in this open-source app; *Self-host* = only in the self-hosted build (this repo); *Platform* = closed World platform (not in this repo).

| Area | Feature | Class | When | Edition | Screens | How we do it better | Tags |
|---|---|---|---|---|---|---|---|
| Story | Premise → arcs, chapters, panel-by-panel script | IMPROVE | MVP | Both | B3 C1 | Structured script (camera, emotion, SFX, dialogue) that directly drives generation and stays editable — not a one-shot prompt. |  |
| Story | World-building: locations, props, style refs | ADOPT | MVP | Both | C5 | Locations and props become reusable references, like characters. |  |
| Story | Continuity checker | INNOVATE | v2 | Both | C6 C1 | Flags outfit, prop, injury and time-of-day slips across scenes before pages are generated. | R |
| Story | Story & layout templates | ADOPT | MVP | Both | B4 B3 |  |  |
| Characters | Character bible with reference sheets | ADOPT | MVP | Both | C2 C3 C4 | Anime-profile cards; outfits, expressions, voice and relationships in one place. |  |
| Characters | Character Lock — consistency score + drift fix | INNOVATE | MVP | Both | C3 D1 D8 D12 | Trained identity per character; every panel gets a score, drifted faces are flagged and fixed in one click. | R |
| Panels | Auto panel layouts (4-koma, action, splash, custom) | ADOPT | MVP | Both | D2 B3 | Layout suggested from the script beat count and pacing. |  |
| Panels | Panel generation with pose control | IMPROVE | MVP | Both | D1 | Pose from preset library or a 5-second stick sketch instead of prompt wrangling. |  |
| Panels | Layered panels (bg / characters / FX / text) | INNOVATE | MVP | Both | D1 D8 | Regenerate one character or the background without touching the rest. | R |
| Panels | Edit scope: chapter, page, panel, layer, region | INNOVATE | MVP | Both | D1 D13 | Every AI action has a scope; nothing outside it regenerates. | U |
| Panels | Region edit — brush, lasso, box or click an object | IMPROVE | MVP | Both | D13 D9 | Only the selected area changes; outside pixels are restored exactly; four variations; Character Lock still applies. | R U |
| Panels | Regenerate one page mid-chapter with neighbours as context | IMPROVE | MVP | Both | D1 | Page 7 is redone using pages 6 and 8 for continuity; all other pages untouched. | U |
| Panels | Lock approved panels and pages | ADOPT | MVP | Both | D1 D7 | Locked work is protected from every AI action, including batch and agent actions. | U |
| Panels | Before / after compare and one-click revert per edit | ADOPT | MVP | Both | D13 D10 |  | U |
| Panels | Sketch-to-manga | ADOPT | MVP | Both | D4 | Rough sketch → inked panel with screentone, strength slider. |  |
| Panels | Variations picker | ADOPT | MVP | Both | D8 | Four-up compare; keep, blend or reroll a single layer. |  |
| Lettering | Bubbles, thought clouds, SFX tools | ADOPT | MVP | Both | D3 |  |  |
| Lettering | Lettering engine: auto-fit, tail-to-speaker, RTL/LTR order, vertical JP | IMPROVE | MVP | Both | D3 D5 | Weak lettering is a top complaint about AI comics; bubbles are placed away from faces and reflow when text changes. | R |
| Formats | Manga RTL, comic LTR, vertical webtoon | ADOPT | MVP | Both | B3 E3 E4 |  |  |
| Formats | One story, every format — page ↔ webtoon reflow | INNOVATE | v2 | Both | D11 E2 | Panels and bubbles are data, so a paged chapter re-flows into a vertical strip (and back) with editable gutters. | R |
| Editor | Undo/redo, zoom, shortcuts, layers panel | ADOPT | MVP | Both | D1 |  |  |
| Editor | Live cursors, panel comments, roles | ADOPT | MVP | Both | D7 | Writer, artist, letterer, editor roles with scoped permissions. |  |
| Editor | Editor review mode — approve / request changes per panel | IMPROVE | v2 | Both | D7 | Turns comments into a review queue with status per panel. | R |
| Editor | Version history + branching pages | IMPROVE | v2 | Both | D10 | Named branches per page; compare side by side; merge the winner. | R |
| Editor | Mobile light editing | ADOPT | MVP | Both | D9 | Edit bubbles, regenerate panels, reorder pages. |  |
| AI | Honest credits — cost preview, auto-refund on failure | IMPROVE | MVP | Platform | D1 D8 G3 | Competitors’ credit systems make cost hard to predict; every action shows its cost and failures refund automatically. | R |
| AI | Compute preview — GPU time or API spend before each run | IMPROVE | MVP | Self-host | D1 D8 G2 | The self-host version of honest credits: estimated GPU minutes, or provider cost on your own keys, on every generate button. | R |
| AI | One-click translation + re-lettering with glossary | IMPROVE | MVP | Both | D5 | Creator-controlled and opt-in; glossary for names and SFX; per-bubble review before publishing. | R |
| AI | Animate mode — panel → clip with character voices | IMPROVE | MVP | Both | D6 E2 | Uses panel layers for parallax; voices come from the character bible; 9:16 export with captions. |  |
| AI | Living panels — GIF-style loops inside the page | INNOVATE | v2 | Both | D6 D1 E3 E4 E2 | One layer loops for a few seconds (rain, hair, flicker) while the page stays still. Plays only on screen, pauses under reduced motion and data saver, exports as a still frame to PDF, print and EPUB. Piloted on opted-in series first. | U |
| AI | Upscale & colorize | ADOPT | v2 | Both | D1 E2 |  |  |
| Read | Paged RTL/LTR, double-page, tap zones | ADOPT | MVP | Both | E3 | Mode remembered per series. |  |
| Read | Vertical webtoon scroll | ADOPT | MVP | Both | E4 |  |  |
| Read | Reading settings | ADOPT | MVP | Both | E3 E4 | Direction, background, brightness, zoom, reduced motion. |  |
| Read | Offline downloads, bookmarks, history | ADOPT | MVP | Both | E7 E3 |  |  |
| Read | Guided panel view + auto alt-text & read-aloud | INNOVATE | MVP | Both | E3 E4 E1 | Exact panel geometry from the editor; alt-text written from the script, so comics work with screen readers. | R |
| Read | Reader-side translation with creator glossary | INNOVATE | v2 | Both | E3 E4 E5 | Readers pick a language on any opted-in series; creators see and fix reported lines. | R |
| Trust | Provenance labels + reader filter | IMPROVE | MVP | Both | E1 E5 E6 | Per-page AI / human credit set at publish; readers can filter. Aligns with Korea’s labelling rules. | R |
| Social | Follows, chapter comments, panel comments | IMPROVE | MVP | Both | E5 E3 | Comments can anchor to a panel, collapsed by default to keep reading clean. |  |
| Social | Creator profile | ADOPT | MVP | Both | F1 |  |  |
| Social | Notifications center | ADOPT | MVP | Both | G4 |  |  |
| Find | Trending, new, rankings, genres | ADOPT | MVP | Both | E6 |  |  |
| Find | Search with filters | ADOPT | MVP | Both | E8 |  |  |
| Find | Discovery that surfaces newcomers | IMPROVE | MVP | Both | E6 | Rising-creator shelf and no pay-for-rank; answers Canvas discoverability complaints. | R |
| Money | Tips | ADOPT | MVP | Platform | E5 F2 |  |  |
| Money | External support links (in place of tips) | ADOPT | v2 | Self-host | E5 F1 | Creators on self-hosted instances link their own support pages. No payment handling. |  |
| Money | Credits & subscription billing | ADOPT | MVP | Platform | G3 |  |  |
| Money | Creator analytics | ADOPT | MVP | Both | F2 | Views, retention by page, followers. The earnings panel is Platform only. |  |
| Money | Automatic revenue splits between collaborators | INNOVATE | v2 | Platform | F5 D7 | Tips and sales split by agreed percentages across writer, artist and letterer. | R |
| Money | Marketplace: buy and sell character, style, template packs | ADOPT | v2 | Platform | F3 F4 |  |  |
| Open | Pack files — import and export character, style, template packs | ADOPT | v2 | Both | C2 B4 | Free .mvpack files anyone can share; the open-source route to what the marketplace sells. |  |
| Money | Reader membership / early-access chapters | ADOPT | Future | Platform | E5 |  |  |
| Open | Bring your own models & API keys, per-task routing | IMPROVE | MVP | Self-host | G2 | Route script, image, video, voice and translation to different backends with cost / speed / VRAM hints. |  |
| Open | GPU & backend config (local, ComfyUI, remote) | ADOPT | MVP | Self-host | G2 |  |  |
| Open | One-command Docker install with GPU check | IMPROVE | MVP | Self-host | A1 G2 | Removes the Python/CUDA setup wall common to open-source tools. | R |
| Open | Admin & moderation panel | ADOPT | MVP | Both | G5 | Reports, users, AI-label disputes. |  |
| AI | Model catalogue — 20+ image models via adapters, API and local | IMPROVE | MVP | Both | G2 | OpenAI, Google, xAI, Microsoft, ByteDance and Black Forest Labs APIs, plus open weights (FLUX.2, Qwen-Image, Z-Image, HunyuanImage, SD 3.5, anime SDXL). One manifest format for all. | R |
| AI | Auto-routing — picks the best model per panel | INNOVATE | MVP | Both | D1 D8 G11 | Reads the panel’s structured needs (characters, text in art, B/W, aspect), filters by VRAM, licence and rating, scores by quality, speed and cost, and explains the pick. | R U |
| AI | Providers & aggregators: OpenRouter, fal, Replicate, Together, Runware, Hugging Face | ADOPT | MVP | Both | G2 G11 | One key reaches many models; the router also picks the cheapest or fastest provider serving a model. | R |
| AI | Custom connections: OpenAI-compatible, ComfyUI workflow, generic HTTP, Hugging Face ID | IMPROVE | MVP | Both | G2 G12 | Add any model or endpoint without code; test, describe capabilities, then it joins routing. | R U |
| AI | MCP server — agents can read, edit and generate in projects | INNOVATE | MVP | Both | G13 D10 | Scoped tokens, cost approval, human-only publish and delete, every agent change labelled and undoable in version history. | R |
| AI | MCP client — script assistant can use connected tools | ADOPT | v2 | Both | G13 C1 | Story bibles from notes apps, reference lookup, research for real-world settings. | R |
| AI | Routing modes: balanced, best, fastest, local only, free only | ADOPT | MVP | Both | G11 D1 | Local only guarantees no prompt leaves the server. | U |
| AI | Automatic fallback to the next model on failure | IMPROVE | MVP | Both | D8 G9 | No dead ends on timeouts or safety blocks; the log records every switch. | R |
| AI | Licence guard | INNOVATE | MVP | Both | G11 E1 | Warns before a non-commercial model is used on a public or commercial series. | R |
| AI | Router learns from keep and reroll | INNOVATE | v2 | Both | G11 | Per-server scores adjust to what this community’s creators actually keep. | U |
| Open | Model manifests updated without a release | ADOPT | MVP | Both | G2 G7 | New models ship as manifest files or plugins. | R |
| Platform | Creators sell or give away characters, templates, styles and series | INNOVATE | v2 | Platform | Platform phase | Creator sets price, licence and region; free items carry no commission. | U |
| Platform | Paid chapters, early access and bundles | ADOPT | v2 | Platform | Platform phase | Read on the platform; self-hosted servers link out. | U |
| Platform | Checkout, creator payouts, published commission | ADOPT | v2 | Platform | Platform phase | One published rate for everyone; no pay-to-rank. | U |
| Open | World tab in the app — browse, then open on the platform | ADOPT | MVP | Both | H12 | Links out to world.mangaveyra.org; Free / Paid / Owned labels only; admins can hide it. | U |
| Platform | Monthly creator payouts | ADOPT | v2 | Platform | P1 | Cleared sales minus commission and fees, paid at the start of each month; minimum carries over. | U |
| Open | My World library — import items you own, no prices in the app | IMPROVE | v2 | Both | H11 | The app lists owned free and paid items and imports them with their licence token; buying happens only on the platform website. | U |
| Open | World hub — central directory you run, with manual review | INNOVATE | MVP | Both | H1 H2 H5 H6 | Characters, templates, styles and series listings from any server; public only after approval. | U |
| Open | Choose your World display name and handle | ADOPT | MVP | Both | H9 H10 | Stored on the central server; unique handles, reserved names blocked, old handles redirect. | U |
| Open | Auto-update with backup, health check and rollback | IMPROVE | MVP | Self-host | G14 | Prompt at every admin login; backup before update, deleted after success, restored automatically on failure. Signed releases only. | U |
| Open | Link a self-hosted server to World | ADOPT | MVP | Both | H8 | Device-code linking; per-server signing key; off by default. | U |
| Open | Submit to World with clear upload list | IMPROVE | MVP | Both | H3 H4 | Shows exactly what leaves the server; prompts and drafts never do. | U |
| Open | Automated checks: malware, safety, similarity, signature | INNOVATE | MVP | Both | H5 H6 | Sorts the review queue; high-risk items are held automatically. | R U |
| Open | Developer accounts & signed, sandboxed plugins | IMPROVE | v2 | Both | H7 G7 | Verified developers, public source, permission manifest, per-version review. | R U |
| Open | Trust levels, reports, takedowns, appeals | ADOPT | MVP | Both | H4 H5 |  | U |
| Open | First-run setup wizard | IMPROVE | MVP | Self-host | G8 | Admin account, instance name, public or private, storage, model check — in the browser, no config files. | R |
| Open | Instance modes: personal, private group, public | ADOPT | MVP | Self-host | G8 G5 A3 | Controls sign-up (closed, invite, open) and whether Explore is public. |  |
| Open | Job queue & fair GPU sharing | INNOVATE | MVP | Self-host | G9 D8 | Every generation is visible in one queue; per-user limits so one member can’t hold the GPU all night. | R |
| Open | Content safety filters & age gates | ADOPT | MVP | Self-host | G5 E1 | Instance-level NSFW filter and rating rules; the admin decides. |  |
| Open | Backup, restore & full data export | ADOPT | MVP | Both | G10 | One archive with projects, characters and settings. No lock-in, easy server moves. | R |
| Open | Import existing comics (CBZ, CBR, PDF, image folders) | ADOPT | MVP | Both | G10 B2 | Panel and bubble detection turns imported pages into editable projects. | R |
| Open | Updates & migrations from the admin panel | ADOPT | MVP | Self-host | G5 G14 | Shows the new version, changelog and a backup prompt before upgrading. |  |
| Open | Series licence (all rights or Creative Commons) | ADOPT | MVP | Both | E1 E5 | Clear reuse terms on every series; matters for a community of shared packs. | R |
| Open | Federation between instances (ActivityPub) | INNOVATE | Future | Both | E5 E6 | Follow and comment on series hosted on other MangaVeyra servers. | R |
| Open | Interface translation (UI i18n) and RTL UI | ADOPT | MVP | Both | G1 | Community-translated interface; Arabic and Hebrew layouts mirror. |  |
| Open | Keyboard shortcuts & command palette | ADOPT | MVP | Both | D1 | Shortcut sheet on “?”; every action searchable. |  |
| Open | Telemetry off by default | ADOPT | MVP | Self-host | G8 | No data leaves the server unless the admin opts in. | R |
| Open | Plugin directory (models, exporters, readers) | ADOPT | v2 | Self-host | G7 |  |  |
| Open | Public API + OPDS feed | ADOPT | v2 | Both | G2 | Lets Komga/Kavita-style readers subscribe to self-hosted series. | R |
| Publish | Publish flow: cover, tags, language, rating, schedule | ADOPT | MVP | Both | E1 |  |  |
| Publish | Export PNG, PDF, EPUB, print-ready, video | ADOPT | MVP | Both | E2 | Print presets with trim and bleed. |  |
| Future | Reader-choice branching chapters | INNOVATE | Future | Both | E3 |  |  |

## Roadmap

### MVP — Open-source launch · 71 features

- **INNOVATE** · Character Lock — consistency score + drift fix
- **INNOVATE** · Layered panels (bg / characters / FX / text)
- **INNOVATE** · Edit scope: chapter, page, panel, layer, region · your idea
- **INNOVATE** · Guided panel view + auto alt-text & read-aloud
- **INNOVATE** · Auto-routing — picks the best model per panel · your idea
- **INNOVATE** · MCP server — agents can read, edit and generate in projects
- **INNOVATE** · Licence guard
- **INNOVATE** · World hub — central directory you run, with manual review · your idea
- **INNOVATE** · Automated checks: malware, safety, similarity, signature · your idea
- **INNOVATE** · Job queue & fair GPU sharing · Self-host
- **IMPROVE** · Premise → arcs, chapters, panel-by-panel script
- **IMPROVE** · Panel generation with pose control
- **IMPROVE** · Region edit — brush, lasso, box or click an object · your idea
- **IMPROVE** · Regenerate one page mid-chapter with neighbours as context · your idea
- **IMPROVE** · Lettering engine: auto-fit, tail-to-speaker, RTL/LTR order, vertical JP
- **IMPROVE** · Compute preview — GPU time or API spend before each run · Self-host
- **IMPROVE** · One-click translation + re-lettering with glossary
- **IMPROVE** · Animate mode — panel → clip with character voices
- **IMPROVE** · Provenance labels + reader filter
- **IMPROVE** · Follows, chapter comments, panel comments
- **IMPROVE** · Discovery that surfaces newcomers
- **IMPROVE** · Bring your own models & API keys, per-task routing · Self-host
- **IMPROVE** · One-command Docker install with GPU check · Self-host
- **IMPROVE** · Model catalogue — 20+ image models via adapters, API and local
- **IMPROVE** · Custom connections: OpenAI-compatible, ComfyUI workflow, generic HTTP, Hugging Face ID · your idea
- **IMPROVE** · Automatic fallback to the next model on failure
- **IMPROVE** · Auto-update with backup, health check and rollback · Self-host · your idea
- **IMPROVE** · Submit to World with clear upload list · your idea
- **IMPROVE** · First-run setup wizard · Self-host

Also: World-building: locations, props, style refs · Story & layout templates · Character bible with reference sheets · Auto panel layouts (4-koma, action, splash, custom) · Lock approved panels and pages · your idea · Before / after compare and one-click revert per edit · your idea · Sketch-to-manga · Variations picker · Bubbles, thought clouds, SFX tools · Manga RTL, comic LTR, vertical webtoon · Undo/redo, zoom, shortcuts, layers panel · Live cursors, panel comments, roles · Mobile light editing · Paged RTL/LTR, double-page, tap zones · Vertical webtoon scroll · Reading settings · Offline downloads, bookmarks, history · Creator profile · Notifications center · Trending, new, rankings, genres · Search with filters · Creator analytics · GPU & backend config (local, ComfyUI, remote) · Self-host · Admin & moderation panel · Providers & aggregators: OpenRouter, fal, Replicate, Together, Runware, Hugging Face · Routing modes: balanced, best, fastest, local only, free only · your idea · Model manifests updated without a release · World tab in the app — browse, then open on the platform · your idea · Choose your World display name and handle · your idea · Link a self-hosted server to World · your idea · Trust levels, reports, takedowns, appeals · your idea · Instance modes: personal, private group, public · Self-host · Content safety filters & age gates · Self-host · Backup, restore & full data export · Import existing comics (CBZ, CBR, PDF, image folders) · Updates & migrations from the admin panel · Self-host · Series licence (all rights or Creative Commons) · Interface translation (UI i18n) and RTL UI · Keyboard shortcuts & command palette · Telemetry off by default · Self-host · Publish flow: cover, tags, language, rating, schedule · Export PNG, PDF, EPUB, print-ready, video

### v2 — Open-source next · 15 features

- **INNOVATE** · Continuity checker
- **INNOVATE** · One story, every format — page ↔ webtoon reflow
- **INNOVATE** · Living panels — GIF-style loops inside the page · your idea
- **INNOVATE** · Reader-side translation with creator glossary
- **INNOVATE** · Router learns from keep and reroll · your idea
- **IMPROVE** · Editor review mode — approve / request changes per panel
- **IMPROVE** · Version history + branching pages
- **IMPROVE** · My World library — import items you own, no prices in the app · your idea
- **IMPROVE** · Developer accounts & signed, sandboxed plugins · your idea

Also: Upscale & colorize · External support links (in place of tips) · Self-host · Pack files — import and export character, style, template packs · MCP client — script assistant can use connected tools · Plugin directory (models, exporters, readers) · Self-host · Public API + OPDS feed

### Future — Later · 2 features

- **INNOVATE** · Federation between instances (ActivityPub)
- **INNOVATE** · Reader-choice branching chapters
### Platform — Separate phase · 10 features

- **INNOVATE** · Automatic revenue splits between collaborators · Platform
- **INNOVATE** · Creators sell or give away characters, templates, styles and series · Platform · your idea
- **IMPROVE** · Honest credits — cost preview, auto-refund on failure · Platform

Also: Tips · Platform · Credits & subscription billing · Platform · Marketplace: buy and sell character, style, template packs · Platform · Reader membership / early-access chapters · Platform · Paid chapters, early access and bundles · Platform · your idea · Checkout, creator payouts, published commission · Platform · your idea · Monthly creator payouts · Platform · your idea


## Lead differentiators

- **Character Lock** (MVP, screens C3 · D1 · D12): Each character is trained once. Every generated panel is scored against them; drift is flagged on the canvas and fixed by regenerating only that character layer.
- **Layered panels** (MVP, screens D1 · D8): Background, each character, effects and lettering are separate layers with their own prompts. Edit one, keep the rest.
- **Every format** (v2, screens D11 · E2): One chapter becomes RTL manga, LTR comic or a vertical webtoon. Panels re-flow, bubbles follow their speakers.
- **Auto model routing** (MVP, screens D1 · D8 · G11): Every generation goes to the model best suited to it — local or API — with the reason shown and one-click override. Your idea.
- **Panel-aware reading** (MVP, screens E3 · E4): Guided panel-by-panel view on phones, alt-text and read-aloud from the script — accessibility that scanned pages can’t offer.

## Key flows

- **Flow 0 · Install a server (self-host)**: docker compose up → G8 setup wizard → G2 connect models → B1
- **Flow 1 · Arrive and set up**: A1 or E6 (self-host) → A3 → A5 → B1
- **Flow 2 · Start a story**: B3 → C1 → C4 → C2
- **Flow 3 · Make a page**: D1 → D2 → D8 → D3 → E2
- **Flow 4 · Publish and read**: E1 → E5 → E4 (mobile)
- **Flow 5 · Go multilingual**: D5 → E1 → E5 (second language)
- **Flow 6 · Character Lock, end to end**: C4 train → C3 status → D1 generate → D8 drift flagged → D12 one-click fix → score restored
- **Flow 8 · Share to World**: H8 link server → H3 submit → H5 queue → H6 review → H4 approved → H2 other user imports
- **Flow 7 · Fix one part of a finished chapter**: D1 page 7 → D13 lasso a hand → describe → pick variation → compare → keep (pages 1–6, 8–20 untouched)

## Feature matrix (research)

● core · ◐ partial / plugin · · absent. Directional, compiled Sep 2026.

| Group | Feature | WEBTOON / Canvas | Tapas | Mihon | Komga | Kavita | Clip Studio Paint | Krita | Anifusion | Dashtoon | KomikoAI | Midjourney | manga-image-translator | DiffSensei | ComfyUI | OpenRouter | fal | MangaVeyra |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| Create | Script / story generation | · | · | · | · | · | · | · | ◐ | ◐ | ◐ | · | · | · | · | · | · | MVP |
| Create | Character consistency | · | · | · | · | · | · | · | ● | ● | ● | ◐ | · | ● | ● | · | · | MVP |
| Create | Panel layouts | · | · | · | · | · | ◐ | ◐ | ● | ● | ● | · | · | ◐ | · | · | · | MVP |
| Create | Bubbles & lettering | · | · | · | · | · | ● | ◐ | ● | ● | ● | · | ◐ | ◐ | · | · | · | MVP |
| Create | Sketch-to-image | · | · | · | · | · | ◐ | · | ● | ◐ | ◐ | ◐ | · | · | ● | · | · | MVP |
| Create | Pose control | · | · | · | · | · | ● | · | ◐ | ◐ | ◐ | · | · | ◐ | ● | · | · | MVP |
| Create | Layered panel regeneration (new) | · | · | · | · | · | · | ◐ | ◐ | ◐ | ◐ | ◐ | · | · | ◐ | · | · | MVP |
| Create | Continuity checker (new) | · | · | · | · | · | · | · | · | · | · | · | · | · | · | · | · | v2 |
| Create | Region edit (only the selected area) (new) | · | · | · | · | · | · | ◐ | ● | ◐ | ◐ | ● | · | · | ● | ◐ | ◐ | MVP |
| Create | Lock approved panels from AI changes (new) | · | · | · | · | · | · | · | · | · | · | · | · | · | · | · | · | MVP |
| Edit | Layers | · | · | · | · | · | ● | ● | ◐ | ◐ | ◐ | · | · | · | · | · | · | MVP |
| Edit | Real-time collaboration | · | · | · | · | · | · | · | · | ● | · | · | · | · | · | · | · | MVP |
| Edit | Multi-page projects | ◐ | ◐ | · | · | · | ● | ◐ | ● | ● | ◐ | · | · | · | · | · | · | MVP |
| Edit | Version history / branches (new) | · | · | · | · | · | ◐ | · | · | · | · | · | · | · | · | · | · | v2 |
| AI | Translation / re-lettering | ● | · | · | · | · | · | · | · | ◐ | · | · | ● | · | · | · | · | MVP |
| AI | Animation / video | · | · | · | · | · | ● | ● | ◐ | · | ● | ◐ | · | · | ● | · | ● | MVP |
| AI | Upscale / colorize | · | · | · | · | · | ◐ | · | ◐ | ◐ | ● | ● | ◐ | · | ● | · | ● | v2 |
| AI | Multiple image models (new) | · | · | · | · | · | · | ◐ | ● | ◐ | ◐ | · | · | ◐ | ◐ | ● | ● | MVP |
| AI | Auto model routing per task (new) | · | · | · | · | · | · | · | · | · | · | · | · | · | · | ◐ | · | MVP |
| AI | Custom providers / endpoints (new) | · | · | · | · | · | · | ◐ | · | · | · | · | ◐ | · | ● | · | · | MVP |
| AI | Cost preview before generating (new) | · | · | · | · | · | · | · | ◐ | · | ◐ | · | · | · | · | ● | ● | MVP |
| AI | Animated panels inside the reader (new) | ◐ | · | · | · | · | · | · | · | · | · | · | · | · | · | · | · | v2 |
| Read | Paged RTL / LTR | · | · | ● | ● | ● | · | · | · | · | · | · | · | · | · | · | · | MVP |
| Read | Vertical scroll | ● | ● | ● | ● | ● | · | · | · | ● | · | · | · | · | · | · | · | MVP |
| Read | Offline downloads | ● | ● | ● | ◐ | ◐ | · | · | · | ◐ | · | · | · | · | · | · | · | MVP |
| Read | Guided panel view | · | · | · | · | · | · | · | · | · | · | · | · | · | · | · | · | MVP |
| Read | Alt-text & read-aloud (new) | · | · | · | · | · | · | · | · | · | · | · | · | · | · | · | · | MVP |
| Read | AI provenance labels (new) | · | · | · | · | · | · | · | · | · | · | · | · | · | · | · | · | MVP |
| Social | Comments & follows | ● | ● | · | · | · | · | · | ◐ | ● | ◐ | · | · | · | · | · | · | MVP |
| Social | Creator pages | ● | ● | · | · | · | · | · | ◐ | ● | ◐ | ◐ | · | · | · | · | · | MVP |
| Social | Federation between servers (new) | · | · | · | · | · | · | · | · | · | · | · | · | · | · | · | · | Future |
| Find | Rankings & recs | ● | ● | · | · | ◐ | · | · | ◐ | ● | · | ◐ | · | · | · | · | · | MVP |
| Money | Tips / revenue share | ● | ● | ◐ | · | · | · | · | ◐ | ● | · | · | · | · | · | · | · | Platform |
| Money | Credits / subscription | ● | ● | · | · | · | · | · | ● | ● | ● | · | · | · | · | · | · | Platform |
| Open | Self-host | · | · | ◐ | ● | ● | · | ◐ | · | · | · | ● | ● | ● | ● | · | · | MVP |
| Open | Plugins / extensions | · | · | ● | ◐ | ◐ | ● | ● | · | · | · | · | ◐ | · | ● | · | · | v2 |
| Open | Bring your own models | · | · | · | · | · | · | ● | · | · | · | · | ● | ● | ● | · | · | MVP |
| Open | Browser setup wizard (new) | · | · | · | ● | ● | · | · | · | · | · | · | · | · | · | · | · | MVP |
| Open | Backup & CBZ/PDF import (new) | · | · | ● | ● | ● | · | · | · | · | · | · | · | · | · | · | · | MVP |
| Open | Shared GPU job queue (new) | · | · | · | · | · | · | · | · | · | · | · | · | · | ◐ | · | · | MVP |
| Open | Public API / OPDS feed (new) | · | · | ◐ | ● | ● | · | · | · | · | · | · | · | · | · | · | · | v2 |
| Open | MCP server for AI agents (new) | · | · | · | · | · | · | · | · | · | · | · | · | · | · | · | · | MVP |

## Image model catalogue (Sep 2026, verify before relying on it)

| Model | Maker | Access | Licence / hardware | Router sends it |
|---|---|---|---|---|
| GPT Image 2 / 2.5 (Flare, Sunburst) | OpenAI | API | Per image | Covers, complex multi-character scenes, text inside the art. 2.5 released 8 Sep 2026; Flare for speed, Sunburst for edits. |
| Nano Banana Pro (Gemini 3 Pro Image) | Google | API | Per image · up to 4K | High-res covers and spreads, edits, multilingual in-art text; up to 5 character references. |
| Nano Banana 2 (Gemini 3.1 Flash Image) | Google | API | Per image · 0.5K–4K | Fast drafts and batches; 1:8 and 8:1 ratios suit webtoon strips; up to 14 references. |
| Grok Imagine Image 2.0 | xAI | API | Per image | General panels; #2 on llm-stats blind votes (Sep 2026). |
| MAI-Image-2.5 | Microsoft | API | Per image | General panels; #3 on llm-stats blind votes (Sep 2026). |
| Seedream 4.5 | ByteDance | API | Per image · up to 4K | Signs, posters and dense text; multi-image edits that keep reference details. |
| FLUX.2 [pro] / [flex] | Black Forest Labs | API | Per image | Cinematic splash pages and broad stylistic range. |
| FLUX.2 [dev] · 32B | Black Forest Labs | Open weights | Non-commercial terms · ~24 GB | Best open-weight quality; private or non-commercial series only. |
| FLUX.2 [klein] · 4B | Black Forest Labs | Open weights | Apache 2.0 · ~13 GB | Low-latency drafts and thumbnails on consumer GPUs. |
| Qwen-Image · Edit · Layered | Alibaba Qwen | Open weights | Apache 2.0 · ~24 GB | Text in art and layer edits; the Layered release maps directly to layered panels. |
| Qwen-Image 2.0 / 2.1 · 7B | Alibaba Qwen | Open weights | Check licence · ~16 GB quantized | Unified generate-and-edit. 2.1 (20 Sep 2026) is reported as non-commercial. |
| Z-Image-Turbo · 6B | Tongyi-MAI | Open weights | Apache 2.0 · 13–16 GB | Near-instant previews with bilingual text. |
| HunyuanImage 3.0 · 80B MoE | Tencent | Open weights | Conditions · ~160 GB full precision | Long, knowledge-heavy prompts on server-class hardware. |
| GLM-Image · HiDream-O1 | Zhipu · HiDream | Open weights | MIT | Typography alternatives with permissive licences. |
| Stable Diffusion 3.5 | Stability AI | Open weights | Community licence | Largest LoRA and ControlNet ecosystem. |
| Sana | NVIDIA | Open weights | ~8 GB | Low-VRAM machines and laptops. |
| Illustrious · NoobAI XL · Animagine XL 4.0 | Community (SDXL) | Open weights | Varies · 8–12 GB | Anime and manga linework, huge character-LoRA library; default for Character Lock training on small GPUs. |
| DiffSensei | Research (CVPR 2025) | Open weights | ~24 GB lite | Black-and-white manga panels with multi-character control. |

## Providers

| Provider | Type | What it adds |
|---|---|---|
| Direct vendor keys | Vendor | OpenAI, Google (AI Studio or Vertex), xAI, Microsoft, ByteDance, Black Forest Labs. Newest versions arrive here first. |
| OpenRouter | Aggregator | One key and one request format for 30+ image models from 8 providers (June 2026 Image API), with capability discovery the setup form can read. Also covers text models. |
| fal | Aggregator | 1,000+ image, video, audio and 3D models; low latency; per-image or per-megapixel pricing. |
| Replicate | Aggregator | Large catalogue and community fine-tunes; can host your own model. Billed per GPU-second, and idle models can cold-start. |
| Together AI | Aggregator | Open-weight image models alongside text models, with OpenAI-compatible endpoints. |
| Runware | Aggregator | Among the lowest per-image prices for open-weight models. |
| Hugging Face | Hub + endpoints | Every open checkpoint; run it in the local worker or on a paid endpoint. |
| Local worker | Self-host | Diffusers or ComfyUI on your own GPU. No per-image cost, nothing leaves the server. |
| Remote GPU (RunPod, Vast and similar) | Rented GPU | Run the same local worker on a rented card when you have no GPU. |

## Sources

- [AI in Webtoon Creation: Challenges, Perceptions, and Design Implications](https://dl.acm.org/doi/10.1145/3772318.3790343) — CHI 2026, ACM
- [WEBTOON announces unified international CANVAS platform](https://www.businesswire.com/news/home/20260326392552/en/WEBTOON-Entertainment-Announces-Unified-International-CANVAS-Platform-Helping-Creators-Grow-Their-Audience-and-Earnings) — Business Wire, 26 Mar 2026
- [What Webtoon’s new AI partnership could mean for translators](https://www.publishersweekly.com/pw/by-topic/industry-news/comics/article/101199-what-webtoon-s-ai-partnership-could-mean-for-translators.html) — Publishers Weekly, Sep 2026
- [The current state of big webtoon platforms](https://kcomicsbeat.com/2026/05/21/webtoons-are-dying-as-the-digital-landscape-struggles/) — K-Comics Beat, 21 May 2026
- [AI Webtoon in 2026](https://aitribune.net/2026/05/08/ai-webtoon-in-2026/) — The AI Tribune, 8 May 2026
- [South Korea imposes mandatory AI-labelling of content](https://thenewpublishingstandard.com/2026/01/28/korea-ai-act-webtoon-creators/) — The New Publishing Standard, Jan 2026
- [Best AI manga generators 2026 — compared](https://anifusion.ai/articles/best-ai-manga-generators-2026/) — Anifusion, updated Jun 2026 (vendor source; read critically)
- [DiffSensei (CVPR 2025) repository](https://github.com/jianzongwu/DiffSensei) — GitHub
- [DiffSensei project page](https://jianzongwu.github.io/projects/diffsensei/) — Wu et al.
- [comic-translation topic (manga-translator-ui, typeset-studio)](https://github.com/topics/comic-translation) — GitHub, Jul 2026
- [Tachiyomi](https://en.wikipedia.org/wiki/Tachiyomi) — Wikipedia
- [NAVER WEBTOON debuts Cuts, a short-form animated video feature](https://about.webtoon.com/press-release/198) — WEBTOON Entertainment press release, 2025
- [Bongcheon-Dong Ghost](https://en.wikipedia.org/wiki/Bongcheon-Dong_Ghost) — Wikipedia
- [Naver Webtoon — effects editor history](https://en.namu.wiki/w/%EB%84%A4%EC%9D%B4%EB%B2%84%20%EC%9B%B9%ED%88%B0) — NamuWiki, community-edited
- [DancingBoard: streamlining the creation of motion comics](https://arxiv.org/pdf/2503.09061) — arXiv, 2025
- [Motion Comic](https://tvtropes.org/pmwiki/pmwiki.php/Main/MotionComic) — TV Tropes
- [Motion comics: what they are and how to animate comics](https://www.ricolandia.com/en/blog/motion-comics-guide/) — Ricolandia, 2026
- [Best AI for image generation — blind-vote leaderboard](https://llm-stats.com/leaderboards/best-ai-for-image-generation) — llm-stats, Sep 2026
- [Best AI image generators, September 2026](https://www.buildmvpfast.com/articles/best-llms-2026-guide/image-generation-ai) — BuildMVPFast, Sep 2026
- [GPT-Image-2.5 vs Nano Banana 2](https://www.cometapi.com/gpt-image-2-5-vs-nano-banana-2/) — CometAPI, Sep 2026
- [GPT Image 2 vs Nano Banana Pro & 2](https://2slides.com/blog/gpt-image-2-vs-nano-banana) — 2Slides, Sep 2026
- [Best open-source image models to deploy in 2026](https://www.simplismart.ai/comparisons/best-open-source-image-generation-models-to-deploy-in-2026) — Simplismart, Sep 2026
- [Open-source image generation models guide](https://www.bentoml.com/blog/a-guide-to-open-source-image-generation-models) — BentoML
- [Open-source image model families to run locally](https://magichour.ai/blog/open-source-image-generation-models) — Magic Hour, Sep 2026
- [Qwen-Image 2.1 vs FLUX 3](https://www.orcarouter.ai/blog/qwen-image-2-1-vs-flux-3) — OrcaRouter, Sep 2026
- [Top open-source AI image generators](https://www.secondtalent.com/resources/top-open-source-ai-image-generators/) — Second Talent, Jun 2026
- [Best text-to-video generators, September 2026](https://www.buildmvpfast.com/articles/best-llms-2026-guide/video-generation-ai) — BuildMVPFast
- [Midjourney](https://en.wikipedia.org/wiki/Midjourney) — Wikipedia
- [Seedream 4.5](https://seed.bytedance.com/en/seedream4_5) — ByteDance Seed
- [Introducing the Unified Image API](https://openrouter.ai/blog/announcements/image-api/) — OpenRouter, 23 Jun 2026
- [OpenRouter image generation tutorial](https://openrouter.ai/blog/tutorials/image-generation/) — OpenRouter, Aug 2026
- [OpenRouter image API vs multi-modal gateways](https://www.edenai.co/post/openrouter-launches-image-api-how-multi-modal-ai-gateways-compare) — Eden AI, Jun 2026
- [Best image generation APIs in 2026](https://developer.puter.com/blog/best-image-generation-apis/) — Puter, Jun 2026
- [9 best AI image providers & APIs](https://techsy.io/en/blog/best-ai-image-providers) — Techsy, Jun 2026
