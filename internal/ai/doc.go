// Package ai is MangaVeyra's model layer: the manifest catalogue, the
// provider registry and adapters, the router (modes, pins, guards, explain,
// fallback), the execution bridge other modules call, spend tracking and the
// compute meter.
//
// Screens: G2 Models & providers (/models, also G2m on phones), G11 Routing
// (/models/routing), G12 Add custom provider (/models/providers/new/{connect,
// test,describe,route}), local workers (/models/workers/new, "Local worker →
// Add worker") and the local model set (/models/local).
//
// # Sub-packages
//
//   - catalog: the manifest format and the embedded YAML catalogue (image,
//     text, video, voice, OCR and upscale models). Files in <data>/manifests
//     override or extend it (merged by id) and custom connections add
//     manifests stored in the models table. Provider model ids and costs are
//     marked "verify before use".
//   - providers: one adapter per vendor or connection type (OpenAI,
//     OpenRouter, fal, Replicate, Together, Runware, Hugging Face, Google
//     Gemini, xAI, Anthropic, Ollama, OpenAI-compatible, ComfyUI workflow,
//     generic HTTP) plus the built-in placeholder ("Built-in placeholder · no
//     AI"). Every call goes through the SSRF-guarded client of
//     internal/core/netguard.
//   - router: the pure routing pipeline (read → filter → score → explain →
//     fall back) over a snapshot of models, providers, workers, pins and guards.
//   - views: the templ screens.
//
// # Using the service from other modules
//
// Look the service up in Init:
//
//	svc := core.MustUse[*ai.Service](d)
//
// Generation (inside a Go-executor job handler; run may be nil outside the queue):
//
//	res, err := svc.Run(ctx, run, router.Need{Task: "generate", Characters: 1, LockedCharacters: 1,
//	    TextInArt: true, BW: true, Aspect: "2:3", ProjectID: p, CharacterID: c, PanelType: "panel",
//	    Public: series.Public, Rating: series.Rating, Label: "p.12 panel 3"},
//	    ai.Request{Prompt: prompt, Width: 832, Height: 1248, N: 4, Refs: []ai.Input{{Key: sheetKey}}, Label: "p.12 panel 3"})
//	// res.Images[i].Key are stored files; res.Cost is on the job already; res.Placeholder
//	// marks results from the built-in placeholder (label them honestly).
//
// Run routes the need, calls the chosen adapter or re-dispatches the job to a
// worker (a child job with executor "worker", the worker-protocol input and
// the inputs as stored files), forwards progress to run, records the spend
// (spend_ledger: remote USD on the member's own keys, or GPU seconds) and the
// actual cost on the job, updates model_stats and routing_decisions, and on a
// failure, timeout or safety block moves to the next candidate and writes a
// "fallback" event to the job log. The built-in placeholder closes every image
// and text chain, so there is never a dead end.
//
// Text (script, translation, alt-text, continuity):
//
//	out, err := svc.Text(ctx, router.Need{Task: "translate", UserID: u, ProjectID: p},
//	    ai.TextRequest{System: "...", Prompt: "...", Lang: "ja"})
//
// Routing chips and compute previews (no job, nothing persisted):
//
//	e, err := svc.Explain(ctx, need, prompt) // e.Label = "Auto → Qwen-Image Edit · local · ~40 s"
//	@e.Chip(ctx)                             // ui.RouteChip with a "Why" link
//	est, err := svc.Estimate(ctx, need)      // {seconds, usd, gpu_seconds}
//
// Keep/reroll learning (v2): svc.Feedback(ctx, modelID, providerID, task, kept).
//
// # Hooks the module installs or provides
//
//   - jobs.Queue failover (SetFailover): a routed worker job that fails is
//     re-queued on its next candidate — another worker model, or a remote
//     provider on the Go executor. The module registers Go handlers for the
//     worker image job types (image.generate, image.edit, image.inpaint,
//     character.sheet, upscale) for that purpose; other modules use their own
//     job types and call Run.
//   - an enqueue validator rejecting jobs whose estimated remote spend passes
//     the monthly limit, and an OnFinish hook that books worker GPU time.
//   - core.Provide: *ai.Service, ai.ComputeMeter, web.ComputeExtender (remote
//     spend and queue ETA on the top-bar meter and the dashboard compute card),
//     setup.ModelStep (first-run step 3) and jobs.ManifestLookup (manifests for
//     claimed worker jobs; the app sets jobs.API.Manifests from it when wired).
//   - optional: ai.ScopeAuthorizer (the studio decides who may pin models for
//     a project, series or character; admins always may) and an IntentReader
//     (a small text model that reads prompts into a Need; rules-only routing
//     works without it) via Service.SetIntentReader.
//
// # Rules the module enforces
//
// Keys are sealed with internal/secrets and never returned after saving.
// Provider URLs pass the SSRF guard: link-local and metadata addresses are
// always blocked; private networks only with MV_ALLOW_PRIVATE_PROVIDER_URLS
// or, for ComfyUI / Ollama / OpenAI-compatible runtimes, admin approval.
// Local only mode (and the local_only routing mode) never calls a remote
// provider. The licence guard keeps non-commercial models away from public or
// commercial series. Members add their own providers only when the admin
// allows it; their providers are visible to them alone.
package ai
