# Editor island contracts

`Editor.svelte` owns D1/D2/D3/D8/D13 inside the existing `EditorShell`. It is loaded only by the island registry when the server renders `data-island="editor"`.

`Button.svelte` is the Svelte adapter for `internal/web/ui.Button`: it consumes the canonical `mv-btn` classes and runtime tokens. Fields use the canonical `mv-field`, `mv-input`, `mv-textarea`, `mv-select-wrap`, `mv-check`, `mv-seg`, `mv-notice`, and modal classes. Native selects keep the platform-owned popup; dialog focus uses the native modal dialog contract. No independent palette or control system is introduced.

Pixi renders polygon-clipped independent raster art layers. The SVG overlay renders authoritative server `shape_path` and `text_path` in unrotated page coordinates, grouped with the supplied rotation about the supplied center. Raster generation never receives or writes lettering pixels.

The page store owns every commit, authorization, lock, version, and history snapshot. Drafts and bounded undo/redo are in memory, never local storage. A failed or stale save retains the draft. The island announces dirty state with `mv:unsaved` and asks the canonical `mv:guard-navigation` flow before replacing it. There is no offline write queue or autosave claim.

Region masks use the target raster's pixel dimensions. White selects changes, black preserves; feathering is applied to the PNG mask. Brush, lasso, box, and keyboard-operable rectangle coordinates offer equivalent access. Object segmentation remains disabled Soon.
