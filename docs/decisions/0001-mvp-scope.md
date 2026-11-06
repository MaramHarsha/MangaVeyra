# ADR 0001: First MVP user and scope

- Status: Accepted
- Date: 2026-11-07

## Decision

MangaVeyra's first usable target is a self-hosted solo creator or small creative
team. They may use their own cloud API key or the deterministic built-in
placeholder path. A MangaVeyra-hosted service is not an MVP requirement.

The release remains **pre-alpha** until the creation, editing, and export path is
complete. The XP/E–S-rank layer is deferred from the MVP and remains dormant.

The first implementation milestone is a script vertical slice:

1. Connect OpenRouter for text generation, or use the offline placeholder.
2. Create a project and chapter.
3. Generate a structured chapter script.
4. Edit and persist the result.

OpenRouter image generation, the visual editor, lettering, compositing,
publishing, reading, and export follow only after this slice proves the provider,
studio, generation, authorization, transaction, and job contracts.

The placeholder provider is a permanent deterministic test oracle and offline
demo path, not a temporary mock to delete. Existing migrations are used unless a
test demonstrates a real schema defect.

Character Lock defaults to reference-image guidance. LoRA training remains
optional. ADR 0002 selects DINOv2 Small as the replacement candidate for the
commercial default, gated on a rights-cleared MangaVeyra benchmark.

## Consequences

- Phase 1 begins with text and structured data, not the Pixi editor.
- Provider and pricing details are verified from current primary documentation at
  implementation time; planning notes are not treated as an API contract.
- Cloud access is optional, and `local_only` remains a hard outbound boundary.
- The UI and README must describe the project as pre-alpha and distinguish shipped
  foundation capabilities from planned product features.
