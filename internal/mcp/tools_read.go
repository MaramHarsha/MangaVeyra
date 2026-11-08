package mcp

import (
	"context"
	"errors"
	"strings"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
)

// Read tools need the read scope (projects:read).

type projectIn struct {
	ProjectID string `json:"project_id" jsonschema:"the project id from list_projects"`
}

type chapterIn struct {
	ProjectID string `json:"project_id" jsonschema:"the project id from list_projects"`
	ChapterID string `json:"chapter_id" jsonschema:"the chapter id from get_project"`
}

type jobIn struct {
	JobID string `json:"job_id" jsonschema:"the job id returned by queue_generation"`
}

type recentIn struct {
	Limit int `json:"limit,omitempty" jsonschema:"how many actions to return (1-50, default 20)"`
}

func (m *Module) addReadTools(s *mcpsdk.Server) {
	mcpsdk.AddTool(s, &mcpsdk.Tool{Name: "list_projects", Annotations: readOnly("List projects"),
		Description: "List the manga projects this token can use, with your role, chapter and page counts."}, m.listProjects)
	mcpsdk.AddTool(s, &mcpsdk.Tool{Name: "get_project", Annotations: readOnly("Read a project"),
		Description: "Read a project: premise, style and format, its chapters (number, status, lock, page count, revision) and its characters."}, m.getProject)
	mcpsdk.AddTool(s, &mcpsdk.Tool{Name: "get_script", Annotations: readOnly("Read a chapter script"),
		Description: "Read a chapter's script: scenes and panels (label like 7.3, camera, shot, emotion, action, dialogue, SFX, notes) and the revision edit_script needs."}, m.getScript)
	mcpsdk.AddTool(s, &mcpsdk.Tool{Name: "list_characters", Annotations: readOnly("List characters"),
		Description: "List a project's characters with their bible fields and the revision edit_character needs."}, m.listCharacters)
	mcpsdk.AddTool(s, &mcpsdk.Tool{Name: "get_pages", Annotations: readOnly("Summarise chapter pages"),
		Description: "Summarise a chapter's pages: page versions, locks, panels and their art layers (ids, kind, character, whether art exists). Use the version and layer ids for queue_generation."}, m.getPages)
	mcpsdk.AddTool(s, &mcpsdk.Tool{Name: "get_job", Annotations: readOnly("Read a job"),
		Description: "Read the status of a generation job you queued: state, progress, estimate and actual GPU time or spend, and an error code when it failed."}, m.getJob)
	mcpsdk.AddTool(s, &mcpsdk.Tool{Name: "list_recent_actions", Annotations: readOnly("List your recent actions"),
		Description: "List the recent changes and requests made with this token, including whether a person approved, declined or undid them."}, m.listRecent)
	mcpsdk.AddTool(s, &mcpsdk.Tool{Name: "request_human_action", Annotations: writes("Ask a person", false),
		Description: "Ask a person to do something only a person may do: publish or schedule a chapter, delete a page or the project, share the project or change a role, or unlock a locked page or panel. Nothing happens until a person reviews the request in Agents & MCP; check list_recent_actions for the answer."}, m.requestHuman)
}

func rev(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func parseRev(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(s))
	if err != nil {
		return time.Time{}, errors.New("revision must be the exact value returned when you read it")
	}
	return t.UTC(), nil
}

func (m *Module) listProjects(ctx context.Context, req *mcpsdk.CallToolRequest, _ struct{}) (*mcpsdk.CallToolResult, any, error) {
	a, err := m.agentFor(ctx, req, scopeRead)
	if err != nil {
		return nil, nil, err
	}
	list, err := m.studio.ListProjects(ctx, a.user.ID, 200)
	if err != nil {
		return nil, nil, toolError(err)
	}
	out := []map[string]any{}
	for _, p := range list {
		if !a.tok.AllowsProject(p.ID) {
			continue
		}
		out = append(out, map[string]any{"id": p.ID, "title": p.Title, "role": p.Role, "chapters": p.ChapterCount,
			"pages": p.PageCount, "updated_at": rev(p.UpdatedAt)})
	}
	return nil, map[string]any{"projects": out}, nil
}

func (m *Module) getProject(ctx context.Context, req *mcpsdk.CallToolRequest, in projectIn) (*mcpsdk.CallToolResult, any, error) {
	a, err := m.agentFor(ctx, req, scopeRead)
	if err != nil {
		return nil, nil, err
	}
	p, err := m.project(ctx, a, in.ProjectID, studio.ActRead)
	if err != nil {
		return nil, nil, err
	}
	chapters, err := m.studio.ListChapters(ctx, p.ID)
	if err != nil {
		return nil, nil, toolError(err)
	}
	chOut := []map[string]any{}
	for _, c := range chapters {
		chOut = append(chOut, map[string]any{"id": c.ID, "number": c.Number, "title": c.Title, "summary": c.Summary,
			"status": c.Status, "locked": c.Locked, "pages": c.PageCount, "ai_label": c.AILabel, "revision": rev(c.UpdatedAt)})
	}
	characters, err := m.studio.ListCharacters(ctx, p.ID)
	if err != nil {
		return nil, nil, toolError(err)
	}
	cOut := []map[string]any{}
	for _, c := range characters {
		cOut = append(cOut, map[string]any{"id": c.ID, "name": c.Name, "role": c.Role, "category": c.Category})
	}
	return nil, map[string]any{"project": map[string]any{"id": p.ID, "title": p.Title, "premise": p.Premise,
		"logline": p.Logline, "genres": p.Genres, "style": p.Style, "format": p.Format, "language": p.Language},
		"chapters": chOut, "characters": cOut}, nil
}

// chapterOf loads a chapter and checks it belongs to the project.
func (m *Module) chapterOf(ctx context.Context, projectID, chapterID string) (studio.Chapter, error) {
	if !db.ValidID(strings.TrimSpace(chapterID)) {
		return studio.Chapter{}, toolError(studio.ErrNotFound)
	}
	c, err := m.studio.GetChapter(ctx, strings.TrimSpace(chapterID))
	if err != nil {
		return c, toolError(err)
	}
	if c.ProjectID != projectID {
		return c, toolError(studio.ErrNotFound)
	}
	return c, nil
}

func scriptOut(c studio.Chapter, doc studio.ScriptDoc) map[string]any {
	scenes := []map[string]any{}
	for _, sc := range doc.Scenes {
		panels := []map[string]any{}
		for _, p := range sc.Panels {
			dialogue := []map[string]any{}
			for _, d := range p.Dialogue {
				dialogue = append(dialogue, map[string]any{"speaker": d.Speaker, "character_id": d.CharacterID, "kind": d.Kind, "text": d.Text})
			}
			sfx := []map[string]any{}
			for _, x := range p.SFX {
				sfx = append(sfx, map[string]any{"text": x.Text, "note": x.Note})
			}
			ids := p.CharacterIDs
			if ids == nil {
				ids = []string{}
			}
			panels = append(panels, map[string]any{"id": p.ID, "label": p.Label, "page": p.PageNumber, "camera": p.Camera,
				"shot": p.Shot, "emotion": p.Emotion, "action": p.Action, "dialogue": dialogue, "sfx": sfx,
				"character_ids": ids, "notes": p.Notes})
		}
		scenes = append(scenes, map[string]any{"id": sc.ID, "title": sc.Title, "summary": sc.Summary, "time_of_day": sc.TimeOfDay, "panels": panels})
	}
	return map[string]any{"chapter": map[string]any{"id": c.ID, "number": c.Number, "title": c.Title, "locked": c.Locked},
		"revision": rev(c.UpdatedAt), "summary": doc.ChapterSummary, "scenes": scenes}
}

func (m *Module) getScript(ctx context.Context, req *mcpsdk.CallToolRequest, in chapterIn) (*mcpsdk.CallToolResult, any, error) {
	a, err := m.agentFor(ctx, req, scopeRead)
	if err != nil {
		return nil, nil, err
	}
	p, err := m.project(ctx, a, in.ProjectID, studio.ActRead)
	if err != nil {
		return nil, nil, err
	}
	c, err := m.chapterOf(ctx, p.ID, in.ChapterID)
	if err != nil {
		return nil, nil, err
	}
	doc, err := m.studio.GetScript(ctx, c.ID)
	if err != nil {
		return nil, nil, toolError(err)
	}
	return nil, scriptOut(c, doc), nil
}

func characterOut(c studio.Character) map[string]any {
	return map[string]any{"id": c.ID, "name": c.Name, "kanji": c.Kanji, "role": c.Role, "role_line": c.RoleLine,
		"age": c.Age, "look": c.Look, "personality": c.Personality, "speech_style": c.SpeechStyle,
		"category": c.Category, "references": len(c.Refs), "revision": rev(c.UpdatedAt)}
}

func (m *Module) listCharacters(ctx context.Context, req *mcpsdk.CallToolRequest, in projectIn) (*mcpsdk.CallToolResult, any, error) {
	a, err := m.agentFor(ctx, req, scopeRead)
	if err != nil {
		return nil, nil, err
	}
	p, err := m.project(ctx, a, in.ProjectID, studio.ActRead)
	if err != nil {
		return nil, nil, err
	}
	list, err := m.studio.ListCharacters(ctx, p.ID)
	if err != nil {
		return nil, nil, toolError(err)
	}
	out := []map[string]any{}
	for _, c := range list {
		out = append(out, characterOut(c))
	}
	return nil, map[string]any{"characters": out}, nil
}

func (m *Module) getPages(ctx context.Context, req *mcpsdk.CallToolRequest, in chapterIn) (*mcpsdk.CallToolResult, any, error) {
	a, err := m.agentFor(ctx, req, scopeRead)
	if err != nil {
		return nil, nil, err
	}
	p, err := m.project(ctx, a, in.ProjectID, studio.ActRead)
	if err != nil {
		return nil, nil, err
	}
	c, err := m.chapterOf(ctx, p.ID, in.ChapterID)
	if err != nil {
		return nil, nil, err
	}
	docs, err := m.pages.ChapterPages(ctx, c.ID)
	if err != nil {
		return nil, nil, toolError(err)
	}
	out := []map[string]any{}
	for _, pg := range docs {
		panels := []map[string]any{}
		for _, pn := range pg.Panels {
			layers := []map[string]any{}
			for _, l := range pn.Layers {
				layers = append(layers, map[string]any{"id": l.ID, "kind": l.Kind, "name": l.Name, "character_id": l.CharacterID,
					"locked": l.Locked, "has_art": l.Asset != nil, "provenance": l.Provenance})
			}
			panels = append(panels, map[string]any{"id": pn.ID, "order": pn.Order, "locked": pn.Locked,
				"script_panel_id": pn.ScriptPanelID, "prompt": pn.Prompt, "layers": layers})
		}
		out = append(out, map[string]any{"id": pg.ID, "number": pg.Number, "version": pg.Version, "locked": pg.Locked,
			"bubbles": len(pg.Bubbles), "panels": panels})
	}
	return nil, map[string]any{"chapter": map[string]any{"id": c.ID, "number": c.Number, "locked": c.Locked}, "pages": out}, nil
}

// jobOut is the public view of a job: never its input, output, progress text
// or raw failure message.
func jobOut(j *jobs.Job) map[string]any {
	out := map[string]any{"id": j.ID, "type": j.Type, "state": j.State, "progress": j.Progress, "step": j.Step,
		"steps": j.Steps, "estimate": costOut(j.Estimate), "actual": costOut(j.Actual), "project_id": j.Scope.ProjectID,
		"chapter_id": j.Scope.ChapterID, "page_id": j.Scope.PageID, "created_at": rev(j.CreatedAt)}
	if j.Error != nil && j.State != jobs.StateDone {
		out["error_code"] = j.Error.Code
	}
	if j.FinishedAt != nil {
		out["finished_at"] = rev(*j.FinishedAt)
	}
	return out
}

func costOut(c jobs.Cost) map[string]any {
	return map[string]any{"usd": c.USD, "gpu_seconds": c.GPUSeconds}
}

func (m *Module) getJob(ctx context.Context, req *mcpsdk.CallToolRequest, in jobIn) (*mcpsdk.CallToolResult, any, error) {
	a, err := m.agentFor(ctx, req, scopeRead)
	if err != nil {
		return nil, nil, err
	}
	notFound := errors.New("no job with that id was queued by this token's owner in its projects")
	if !db.ValidID(strings.TrimSpace(in.JobID)) {
		return nil, nil, notFound
	}
	j, err := m.d.Jobs.Get(ctx, strings.TrimSpace(in.JobID))
	if err != nil || j.UserID != a.user.ID || !a.tok.AllowsProject(j.Scope.ProjectID) {
		return nil, nil, notFound
	}
	if err := m.studio.Authorize(ctx, a.actor, j.Scope.ProjectID, studio.ActRead); err != nil {
		return nil, nil, notFound
	}
	return nil, jobOut(j), nil
}

func (m *Module) listRecent(ctx context.Context, req *mcpsdk.CallToolRequest, in recentIn) (*mcpsdk.CallToolResult, any, error) {
	a, err := m.agentFor(ctx, req, scopeRead)
	if err != nil {
		return nil, nil, err
	}
	if in.Limit <= 0 || in.Limit > 50 {
		in.Limit = 20
	}
	out := []map[string]any{}
	rows, err := m.tokenActions(ctx, a.tok.ID, in.Limit)
	if err != nil {
		return nil, nil, toolError(err)
	}
	for _, r := range rows {
		item := map[string]any{"id": r.ID, "kind": r.Kind, "action": r.Action, "status": r.Status,
			"summary": summaryEN(r), "project_id": r.ProjectID.String, "created_at": rev(r.CreatedAt)}
		if r.JobID.Valid {
			item["job_id"] = r.JobID.String
		}
		if r.DecidedAt != nil {
			item["decided_at"] = rev(*r.DecidedAt)
		}
		if r.Kind == kindRequest && r.Action == reqGenerate || r.Kind == kindGenerate {
			item["estimate"] = costOut(r.Estimate.V)
		}
		out = append(out, item)
	}
	return nil, map[string]any{"actions": out}, nil
}
