# Editor island contracts

`Editor.svelte` owns D1/D2/D3/D8/D13 inside the existing `EditorShell`. It is loaded only by the island registry when the server renders `data-island="editor"`. Without JavaScript the server shell (`internal/editor/page.templ`) is a read-only page proof with page navigation and export.

`Button.svelte` is the Svelte adapter for `internal/web/ui.Button`: it consumes the canonical `mv-btn` classes and runtime tokens. Fields use the canonical `mv-field`, `mv-input`, `mv-textarea`, `mv-select-wrap`, `mv-check`, `mv-toggle`, `mv-seg`, `mv-menu`, `mv-meter`, `mv-avatar`, `mv-notice`, and modal classes; confirmations use the shared speech-bubble toast (`lib/toast`). Native selects keep the platform-owned popup; dialog focus uses the native modal dialog contract. No independent palette or control system is introduced.

Pixi renders polygon-clipped independent raster art layers. The SVG overlay renders authoritative server `shape_path` and `text_path` in unrotated page coordinates, grouped with the supplied rotation about the supplied center. Raster generation never receives or writes lettering pixels.

The page store owns every commit, authorization, lock, version, and history snapshot. Drafts and bounded undo/redo are in memory, never local storage. `Autosave` (in `document.ts`) saves the draft 1.5 s after the last change and at once on Cmd/Ctrl+S, mode or page switches, in-app links and when the tab is hidden; it never runs two saves at once, keeps edits made during a save, and keeps undo history across saves. A revision conflict (409) or lock (423) stops saving and keeps the draft until the creator reloads through the shared `mv:guard-navigation` dialog; network failures retry with backoff. The island announces dirty state with `mv:unsaved`. There is no offline write queue.

New bubbles are placed in free space inside the selected panel, in reading order. Font keys follow the plan 0004 content-language contract (`comic`, `sfx`, `jp`, `ko`, `sc`, `tc`, `deva`); vertical lettering is offered only where `verticalAllowed` allows it.

Region masks use the target raster's pixel dimensions. White selects changes, black preserves; feathering is applied to the PNG mask. Brush, lasso, box, and keyboard-operable rectangle coordinates offer equivalent access; the before/after handle is backed by a keyboard slider. Object segmentation remains disabled Soon.
