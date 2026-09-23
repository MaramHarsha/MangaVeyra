// Package studio is the creator studio core: the data model and every studio
// screen except the page editor itself.
//
// Screens: B1 dashboard (/studio), B2 projects (/studio/projects), B3 new
// project wizard (/studio/new/{premise|genre|style|format|generate}), B4
// templates (/studio/templates), C1 story editor
// (/studio/p/{project}/story[/{chapter}[/{scene}]]), C2 character bible
// (/studio/p/{project}/characters and /studio/characters), C3 character detail,
// C4 create character, C5 world-building (/studio/p/{project}/setting) and the
// project settings with members & roles (/studio/p/{project}/settings).
//
// # Service layer
//
// Other modules (editor, gen, compose, publish, reader, mcp, translate,
// export) use the exported *Service:
//
//	svc := core.MustUse[*studio.Service](d)
//
// It has typed repositories for projects, members, arcs, chapters, scenes,
// script panels, pages, panels, layers, bubbles (+ texts per language),
// characters (+ refs, outfits, relations), setting items and templates; the
// PageDoc loader/saver (LoadPageDoc, SavePageDoc — every save records a
// Version with the actor kind user|agent|system and a summary); versions and
// branches (Versions, RestoreVersion, UndoLast, CreateBranch, SwitchBranch,
// MergeBranch, CompareVersions); layout application (ApplyLayout,
// SuggestLayout, BuiltinLayouts); script → pages (MakePagesFromScript);
// permissions (RequireProjectRole middleware, Access, CanEdit); and
// CheckScopeWritable, the one place that enforces scopes and locks for AI,
// batch and agent actions. It is registered as a jobs enqueue validator.
//
// # Job contracts
//
// The studio enqueues script.outline (input OutlineInput, output ScriptDraft,
// applied with ApplyScriptDraft when the job finishes), script.assist (input
// AssistInput, output ScriptDiff, applied on the member's click with
// ApplyScriptDiff), character.sheet (SheetInput → SheetOutput) and
// character.train (TrainInput → TrainOutput). At start-up it registers
// built-in placeholder handlers for these types only when no other module
// registered one, so the gen module's handlers always take over.
//
// # Hooks
//
// Optional services other modules provide with core.Provide: RoutePreviewer
// (routing chips), ComputeStats (dashboard compute card) and
// ProjectSettingsSection (an extra section on the project settings page).
//
// # Realtime
//
// Changes are published on the WebSocket topic project:{id} (see Event);
// project members may subscribe.
package studio
