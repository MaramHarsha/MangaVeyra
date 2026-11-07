package editor

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/gen"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/web"
)

// jobView is the private-input-free job shape the editor island reads.
type jobView struct {
	ID         string     `json:"id"`
	Type       string     `json:"type"`
	State      string     `json:"state"`
	Progress   float64    `json:"progress"`
	Step       int        `json:"step"`
	Steps      int        `json:"steps"`
	ModelID    string     `json:"model_id,omitempty"`
	ModelName  string     `json:"model_name,omitempty"`
	ProviderID string     `json:"provider_id,omitempty"`
	ErrorCode  string     `json:"error_code,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	// Position is the number of queued jobs ahead of a queued job (0 = next).
	Position int `json:"position"`
	// EstimateSeconds is the router's GPU-time estimate.
	EstimateSeconds float64 `json:"estimate_seconds,omitempty"`
	// Scope labels let the queue list name the panel ("7.3") without the input.
	PageID  string `json:"page_id,omitempty"`
	PanelID string `json:"panel_id,omitempty"`
	LayerID string `json:"layer_id,omitempty"`
	Prompt  string `json:"prompt,omitempty"`
}

// modelName returns the catalogue display name of a model ("Placeholder art"),
// or "" when the catalogue does not know it.
func (m *Module) modelName(ctx context.Context, id string) string {
	if id == "" {
		return ""
	}
	lookup, ok := core.Use[jobs.ManifestLookup](m.d)
	if !ok || lookup == nil {
		return ""
	}
	raw, err := lookup(ctx, id)
	if err != nil {
		return ""
	}
	var manifest struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(raw, &manifest) != nil {
		return ""
	}
	return manifest.Name
}

func (m *Module) jobSummary(ctx context.Context, j *jobs.Job) jobView {
	v := jobView{ID: j.ID, Type: j.Type, State: j.State, Progress: j.Progress, Step: j.Step, Steps: j.Steps, ModelID: j.ModelID, ModelName: m.modelName(ctx, j.ModelID), ProviderID: j.ProviderID, CreatedAt: j.CreatedAt, StartedAt: j.StartedAt, FinishedAt: j.FinishedAt, EstimateSeconds: j.Estimate.GPUSeconds, PageID: j.Scope.PageID, PanelID: j.Scope.PanelID, LayerID: j.Scope.LayerID}
	if j.Error != nil {
		v.ErrorCode = j.Error.Code
	}
	if j.State == jobs.StateQueued {
		if n, err := m.d.Jobs.Position(ctx, j); err == nil {
			v.Position = n
		}
	}
	var payload struct {
		Request json.RawMessage `json:"request"`
		PageID  string          `json:"page_id"`
		PanelID string          `json:"panel_id"`
	}
	if j.DecodeInput(&payload) == nil {
		// Region scope names only the layer; the stored payload carries the
		// page and panel it resolved at enqueue.
		if v.PageID == "" {
			v.PageID = payload.PageID
		}
		if v.PanelID == "" {
			v.PanelID = payload.PanelID
		}
		var req struct {
			Prompt string `json:"prompt"`
		}
		if json.Unmarshal(payload.Request, &req) == nil && j.Type == gen.JobRegionEdit {
			v.Prompt = truncate(req.Prompt, 80)
		}
	}
	return v
}

func truncate(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n-1]) + "…"
}

// computeView is the live compute state behind the top-bar meter and the
// footer's worker line.
type computeView struct {
	Queued        int     `json:"queued"`
	Running       int     `json:"running"`
	WorkersOnline int     `json:"workers_online"`
	Load          float64 `json:"load"`
	GPU           string  `json:"gpu,omitempty"`
	VRAMUsedMB    int     `json:"vram_used_mb,omitempty"`
	VRAMTotalMB   int     `json:"vram_total_mb,omitempty"`
	WorkerName    string  `json:"worker_name,omitempty"`
	Host          string  `json:"host"`
}

func (m *Module) compute(r *http.Request) computeView {
	c := m.ui.Compute(r.Context())
	return computeView{Queued: c.Queued, Running: c.Running, WorkersOnline: c.WorkersOnline, Load: c.Load(), GPU: c.GPU, VRAMUsedMB: c.VRAMUsedMB, VRAMTotalMB: c.VRAMTotalMB, WorkerName: c.WorkerName, Host: m.host(r)}
}

// host is the server shown in the footer ("Self-host · studio.local"), the
// same value the app shell and the export screen show: the instance's public
// URL, MV_PUBLIC_URL, or the request host.
func (m *Module) host(r *http.Request) string {
	public, configured := "", ""
	if m.d.Settings != nil {
		public = m.d.Settings.MustInstance(r.Context()).PublicURL
	}
	if m.d.Cfg != nil {
		configured = m.d.Cfg.PublicURL
	}
	return web.ServerHost(public, configured, r.Host)
}

// queue returns the live compute state and the signed-in user's recent panel
// and region jobs in this chapter, newest first, for the meter and D8 queue.
func (m *Module) queue(w http.ResponseWriter, r *http.Request) error {
	res, err := m.resource(r, false)
	if err != nil {
		return mapped(r, err)
	}
	list, err := m.d.Jobs.List(r.Context(), jobs.Filter{UserID: res.Actor.UserID, ProjectID: res.Project.ID, Types: []string{gen.JobPanel, gen.JobRegionEdit}, Limit: 40})
	if err != nil {
		return mapped(r, err)
	}
	pages, err := m.pages.ListPages(r.Context(), res.Chapter.ID)
	if err != nil {
		return mapped(r, err)
	}
	inChapter := map[string]bool{}
	for _, p := range pages {
		inChapter[p.ID] = true
	}
	out := []jobView{}
	for _, j := range list {
		if j.ParentID != "" {
			continue
		}
		v := m.jobSummary(r.Context(), j)
		if !inChapter[v.PageID] {
			continue
		}
		out = append(out, v)
		if len(out) == 8 {
			break
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	core.JSON(w, 200, map[string]any{"compute": m.compute(r), "jobs": out})
	return nil
}
