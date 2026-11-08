---
version: "1.0"
name: "MangaVeyra"
description: "A bilingual manga production desk shaped by gate architecture, night-studio surfaces, and paper-proof clarity."
colors:
  background-dark: "#070A12"
  surface-dark: "#0D1322"
  surface-raised-dark: "#141C30"
  line-dark: "#22304F"
  text-dark: "#E8EEFF"
  text-secondary-dark: "#A3B3D9"
  background-light: "#E8EEFF"
  surface-light: "#F7F9FF"
  text-light: "#070A12"
  primary: "#3D8BFF"
  primary-soft: "#6AA8FF"
  brand-mark: "#E8452C"
  success: "#3CCB7F"
  warning: "#F5B83D"
  danger: "#FF5C7A"
typography:
  display:
    fontFamily: "Anton, Impact, sans-serif"
  sans:
    fontFamily: "Hanken Grotesk, system-ui, sans-serif"
  mono:
    fontFamily: "IBM Plex Mono, ui-monospace, monospace"
  japanese:
    fontFamily: "system-ui, sans-serif"
rounded:
  frame: "0"
  control: "2px"
  bubble: "16px"
  toast: "14px"
  pill: "999px"
spacing:
  base: "4px"
  page-gap: "24px"
  section-gap: "32px"
  page-max: "1440px"
components:
  button: {}
  input: {}
  textarea: {}
  select: {}
  card: {}
  notice: {}
  generation-state: {}
---

# MangaVeyra Design System

## Overview

### Creative North Star

MangaVeyra is a working manga studio at a blue-lit city gate: dark ink-like work surfaces, crisp paper proofs, narrow architectural rules, and one controlled gate-blue signal that tells the creator where work moves next. It should feel like production software made by people who understand panels and manuscripts, not a generic AI dashboard.

### Product context and register

- **Audience and primary job:** Independent manga creators and small teams planning, generating, revising, lettering, and exporting chapters while retaining control of provenance and spend.
- **Target markets and evidence:** English- and Japanese-language creators are explicit in `docs/FEATURES.md`, `docs/SCREENS.md`, the `en`/`ja` catalogues, right-to-left manga formats, and the Japanese lettering/runtime contracts. Native Japanese copy still requires human review before a public release.
- **Locale and language policy:** English is the fallback locale; Japanese is a complete mirrored catalogue. Never combine English and Japanese in one control. User content is preserved in its original script and valid UTF-8.
- **Usage scene:** Desktop-first, long production sessions with dense chapter data; mobile is a complete review/status surface with the same DOM and actions where practical.
- **Register:** A quiet production-tool register with expressive display type reserved for page identity, gates, and major empty states.
- **Memorable signature:** Gate-blue focus/action light against near-black “Gate night” surfaces, paired with squared panel rules and occasional manga SFX treatment.
- **Restraint:** Forms, provider secrets, conflict recovery, and script editing use familiar controls and plain language. Decorative manga treatment never competes with editable content.
- **Anti-references:** No neon cyberpunk rainbow, glassmorphism, casino/gacha language, generic purple AI gradients, dashboard card mosaics, or fake “live” metrics.
- **Token ownership/runtime mapping:** This file mirrors the mature runtime source in `web/src/styles/tokens.css`, `fonts.css`, `base.css`, and `components.css`; it does not generate tokens. Runtime CSS remains canonical. `premium-ui.json`, frontend checks, edition checks, and the strict premium audit are the drift gates.

## Colors

Gate night is the default: `#070A12` background, `#0D1322` surface, `#141C30` raised surface, and `#22304F` rules. Paper mode uses `#E8EEFF` behind `#F7F9FF` sheets. Gate blue `#3D8BFF` is the sole routine accent; vermilion `#E8452C` belongs to the logo, not ordinary calls to action. Text uses the existing three-level hierarchy. Success, warning, danger, and info always include text or an icon label, never color alone. Selection, focus, and primary actions remain visible in both themes; high-contrast focus increases ring width through existing preference tokens.

## Typography

Anton is for short display labels only. Hanken Grotesk owns Latin UI and prose; IBM Plex Mono owns IDs, counts, states, and technical metadata. Japanese UI falls through `system-ui` to the platform's native Japanese sans at normal interface weights; bundled Noto Sans JP Black is intentionally limited to SFX. Controls use sentence case in English and natural Japanese casing. Japanese is never italicized; emphasis uses weight or color. Mixed-script copy permits natural line wrapping with `lang` set by the locale, avoids manual spaces around Japanese punctuation, and keeps compact labels unbroken only when necessary. Ruby is reserved for authored story content, not navigation.

## Layout

The runtime 4 px spacing scale is canonical. The application shell keeps its 56 px top bar, 224 px desktop sidebar, and 76 px responsive bottom navigation. B3 uses the approved five-step wizard and live summary. C1 uses the approved chapter/scene tree, compact manuscript table and assistant pane; the assistant hides below 1180 px and the tree hides on phones. B2/C2–C5, G11/G12 and the editor/export workbench retain their desktop artboard grids. Narrow screens adapt those desktop grids with the existing responsive patterns; unfinished mobile artboards are outside Phase 1. Forms keep labels above fields, stable action geometry and 44 px touch targets on phones.

## Elevation & Depth

Hierarchy comes primarily from tonal surfaces and one-pixel rules. Shadows are reserved for menus, dialogs, toasts, and the approved primary-action ink/glow treatment. Editors, forms, and provider cards stay flat. Blur and translucent glass are forbidden. Sticky headers use an opaque surface and border so text never overlaps content.

## Shapes

Frames are square and controls use the existing 2 px radius. Pills are limited to state/routing chips. Speech bubbles, toasts, and tooltips retain their purposeful larger radii. Icons are not placed in decorative rounded tiles by default. Panel-like rules and asymmetric action shadows supply character without adding arbitrary containers.

## Components

### Foundational visual states

Every interactive owner implements default, hover, focus-visible, active, disabled, busy, success, warning, and error states from `components.css`. Disabled controls remain legible and explain why when the reason is not obvious. Busy buttons keep width and replace or animate the leading icon. `ui.GenState` is the app-owned long-running indicator. Skeletons are optional only for known fixed geometry; server-rendered forms use explicit loading/status copy.

### Buttons and actions

One primary button advances a flow. Secondary actions use outline or quiet treatments; low-priority navigation is ghost. Destructive actions use danger styling and physical separation. Icon-only buttons require accessible names and are reserved for conventional actions. Provider-key replacement is explicit and never conflated with a blank ordinary update.

### Navigation and data display

The app shell owns desktop sidebar/mobile bottom navigation. Breadcrumbs and chapter rails reflect real routes. Lists and cards remain whole-row links only when no nested action conflicts. Badges carry short factual states; routing uses `ui.RouteChip`, provenance uses `ui.ProvenanceBadge`, and job state uses `ui.GenState`.

### Forms and overlays

`internal/web/ui` owns fields, inputs, textareas, native selects, segmented controls, notices, and dialogs. The editor's Svelte adapters consume those same classes/tokens because templ cannot render client-side controls. Forms declare `novalidate` so server validation is authoritative and errors are rendered inline plus in a summary when multiple fields fail. `web/src/lib/forms.ts` owns submission busy state, duplicate prevention and unsaved-navigation protection for forms and rich islands. Textareas use `resize: none`; deliberate sizes are set by component modifiers and responsive CSS. Secret inputs are always blank after submission, never copied back into HTML, logs, notices, or query strings. Uploads show type/size constraints before selection. Designed editor overlays and the shared unsaved/destructive confirmation modal use the existing dialog owner.

### Iconography

Tabler Icons is canonical at 16–20 px for controls, using the existing webfont. Icons support labels rather than replacing unfamiliar text. Optical alignment follows the shared `.ti` rules. Manga SFX typography is content, not iconography.

### Motion

Motion confirms state: 120 ms control feedback, 200–320 ms reveal/step transitions, and existing easing tokens. Polling updates do not animate the whole page. All motion is interruptible, and `prefers-reduced-motion` removes nonessential transitions and looping effects.

### Content and data visualization

Voice is direct, calm, and creator-owned: “Connect OpenRouter,” “Generate script,” “Save changes,” “Reload latest.” Do not anthropomorphize the model or promise quality. Costs are presented as bounded estimates with six-decimal precision only where needed for approval/audit. Errors translate stable codes and never render upstream text. Gate A has no charts; future visualizations must include a textual equivalent.

Phase 2 control workbenches follow D4/D10/D12 exactly: full canvas, 340 px inspector, 52 px bar, two proofs and the
handoff's compact sections/actions. Hide the ordinary filmstrip/footer in these modes. Use real, attributable
similarities and warning states; absent evidence stays unassessed. Branch merging, camera capture and automatic
sketch detection use the designed Soon treatment. Necessary states and desktop comparisons are maintained in
`docs/decisions/0004-design-addendum.md` §10 and `docs/qa/phase-2-control-2026-11-08.md`.

## Do's and Don'ts

- **Do:** Make the next production action unmistakable with one gate-blue primary control.
- **Do:** Reuse shared route, provenance, notice, field, and generation-state owners across setup, providers, and story screens.
- **Do:** Preserve Japanese input, long names, and IME composition without client-side transformation.
- **Don't:** Turn every subsection into a floating card or every status into a glowing badge.
- **Don't:** Expose provider secrets, raw model errors, hidden authorization assumptions, or optimistic success before the server commits.
- **Don't:** Use decorative gradients, glass panels, fake activity, or English all-caps for Japanese-facing controls.
