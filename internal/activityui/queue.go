package activityui

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/gen"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/web"
	"github.com/MaramHarsha/MangaVeyra/internal/web/layout"
	"github.com/MaramHarsha/MangaVeyra/internal/web/ui"
)

type jobView struct {
	ID, Title, Owner, Route, State, Status, Metric, StudioURL string
	Progress                                                  int
	Running, Cancelable                                       bool
}

type eventView struct{ Label, Time, ISOTime string }
type jobDetail struct {
	Job                              jobView
	Estimate, Actual, Created, Error string
	Events                           []eventView
}
type workerAction struct{ URL, Label string }
type queueView struct {
	Past, Admin                                        bool
	Subtitle, NextURL, RefreshURL, Limit, Night, Usage string
	Notice, WorkerHint, WorkerLabel                    string
	WorkerActions                                      []workerAction
	Jobs                                               []jobView
	Detail                                             *jobDetail
	Active                                             bool
}

func queueURL(cursor, selected string) string {
	q := url.Values{}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	if selected != "" {
		q.Set("job", selected)
	}
	if len(q) == 0 {
		return "/queue"
	}
	return "/queue?" + q.Encode()
}

func (m *Module) queuePage(w http.ResponseWriter, r *http.Request) error {
	v, err := m.loadQueue(r)
	if err != nil {
		return err
	}
	return m.ui.Page(w, r, http.StatusOK, web.PageOpts{Title: i18n.T(r.Context(), "activity.queue.title"),
		Section: layout.SectionStudio, Nav: layout.NavModels, Tab: layout.TabHome}, queueBody(v))
}

func (m *Module) loadDetail(r *http.Request, v *queueView) error {
	if selected := r.URL.Query().Get("job"); selected != "" {
		j, err := m.loadJob(r, selected)
		if err != nil {
			return err
		}
		view, err := m.jobView(r.Context(), j)
		if err != nil {
			return err
		}
		detail := &jobDetail{Job: view, Estimate: costText(r.Context(), j.Estimate), Actual: costText(r.Context(), j.Actual), Created: timestamp(r.Context(), j.CreatedAt)}
		if !j.Finished() {
			detail.Actual = i18n.T(r.Context(), "activity.queue.unreported")
		}
		if j.Error != nil {
			detail.Error = jobError(r.Context(), j.Error.Code)
		}
		events, err := m.d.Jobs.RecentEvents(r.Context(), j.ID, 50)
		if err != nil {
			return err
		}
		for _, ev := range events {
			// Deliberately do not render ev.Message, ev.Data or arbitrary kind text.
			key := "activity.event.update"
			switch ev.Kind {
			case "created", "claimed", "progress", "done", "failed", "canceled", "cancel_requested", "retried", "fallback":
				key = "activity.event." + ev.Kind
			}
			detail.Events = append(detail.Events, eventView{Label: i18n.T(r.Context(), key), Time: timestamp(r.Context(), ev.CreatedAt), ISOTime: ev.CreatedAt.UTC().Format(time.RFC3339)})
		}
		v.Detail = detail
		v.Active = v.Active || !j.Finished()
	}
	return nil
}

func (m *Module) queueStatus(w http.ResponseWriter, r *http.Request) error {
	v, err := m.loadQueue(r)
	if err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "no-store")
	return web.Fragment(w, r, http.StatusOK, queueLive(v))
}

func (m *Module) loadQueue(r *http.Request) (queueView, error) {
	ctx := r.Context()
	u := core.UserFrom(ctx)
	cursor := r.URL.Query().Get("cursor")
	if cursor != "" && !db.ValidID(cursor) {
		return queueView{}, core.ErrBadRequest(i18n.T(ctx, "activity.error.cursor"))
	}
	owner := u.ID
	if u.IsAdmin() {
		owner = ""
	}
	items, err := m.d.Jobs.List(ctx, jobs.Filter{UserID: owner, Before: cursor, Limit: listLimit + 1})
	if err != nil {
		return queueView{}, err
	}
	v := queueView{RefreshURL: "/queue/status", Past: cursor != "", Admin: u.IsAdmin()}
	if cursor != "" {
		v.RefreshURL += "?cursor=" + url.QueryEscape(cursor)
	}
	if selected := r.URL.Query().Get("job"); selected != "" {
		separator := "?"
		if cursor != "" {
			separator = "&"
		}
		v.RefreshURL += separator + "job=" + url.QueryEscape(selected)
	}
	if len(items) > listLimit {
		items = items[:listLimit]
		v.NextURL = queueURL(items[len(items)-1].ID, "")
	}
	for _, j := range items {
		view, err := m.jobView(ctx, j)
		if err != nil {
			return v, err
		}
		v.Jobs = append(v.Jobs, view)
		v.Active = v.Active || !j.Finished()
	}
	inst, err := m.d.Settings.Instance(ctx)
	if err != nil {
		return v, err
	}
	compute := m.ui.Compute(ctx)
	workers, err := m.d.Jobs.ListWorkers(ctx)
	if err != nil {
		return v, err
	}
	slots := 0
	for _, wk := range workers {
		if wk.Status == jobs.WorkerOnline {
			slots += wk.MaxConcurrent
		}
	}
	v.Subtitle = i18n.T(ctx, "activity.queue.remote_subtitle")
	if slots > 0 {
		name := compute.GPU
		if name == "" {
			name = i18n.T(ctx, "activity.queue.worker_pool")
		}
		v.Subtitle = i18n.T(ctx, "activity.queue.subtitle", "compute", name, "slots", slots)
	}
	v.Limit = i18n.T(ctx, "activity.queue.limits", "worker", inst.Limits.RunningGPUJobs, "server", inst.Limits.RunningJobs)
	v.Night = i18n.T(ctx, "activity.queue.night_off")
	if inst.NightMode.Enabled {
		v.Night = i18n.T(ctx, "activity.queue.night_hours", "start", integer(inst.NightMode.StartHour)+":00", "end", integer(inst.NightMode.EndHour)+":00")
	}
	usage, err := m.d.Jobs.CompletedUsage(ctx, owner, weekStart(time.Now()))
	if err != nil {
		return v, err
	}
	v.Usage = i18n.T(ctx, "activity.queue.usage", "gpu", gpuTime(ctx, usage.GPUSeconds), "spend", spend(usage.USD))
	v.WorkerLabel, v.WorkerHint = i18n.T(ctx, "activity.queue.pause"), i18n.T(ctx, "activity.queue.admin_only")
	if u.IsAdmin() {
		v.WorkerHint = i18n.T(ctx, "activity.queue.no_worker")
		for _, wk := range workers {
			action, key := "drain", "activity.queue.pause_named"
			if wk.Status == jobs.WorkerDraining {
				action, key = "resume", "activity.queue.resume_named"
			} else if wk.Status != jobs.WorkerOnline {
				continue
			}
			v.WorkerActions = append(v.WorkerActions, workerAction{URL: "/queue/workers/" + wk.ID + "/" + action, Label: i18n.T(ctx, key, "worker", wk.Name)})
			if len(workers) == 1 && action == "resume" {
				v.WorkerLabel = i18n.T(ctx, "activity.queue.resume")
			}
		}
		if len(v.WorkerActions) == 1 {
			v.WorkerHint = v.WorkerActions[0].Label
		}
	}
	if err := m.loadDetail(r, &v); err != nil {
		return v, err
	}
	return v, nil
}

func (m *Module) jobView(ctx context.Context, j *jobs.Job) (jobView, error) {
	v := jobView{ID: j.ID, State: j.State, Progress: percent(j.Progress), Running: j.State == jobs.StateRunning,
		Cancelable: !j.Finished() && !j.CancelRequested}
	v.Status = i18n.T(ctx, "activity.state."+j.State)
	if j.CancelRequested {
		v.Status = i18n.T(ctx, "activity.state.stopping")
	}
	if j.State == jobs.StateQueued {
		position, err := m.d.Jobs.Position(ctx, j)
		if err != nil {
			return v, err
		}
		v.Status = i18n.T(ctx, "activity.state.position", "count", position+1)
	}
	key := "activity.job.other"
	switch j.Type {
	case gen.JobScript:
		key = "activity.job.script"
	case gen.JobCharacterSheet:
		key = "activity.job.character"
	case gen.JobPagesFromScript:
		key = "activity.job.pages"
	case gen.JobPanel:
		key = "activity.job.panel"
	case gen.JobRegionEdit:
		key = "activity.job.region"
	case gen.JobPage:
		key = "activity.job.page"
	case gen.JobRepair:
		key = "activity.job.repair"
	case gen.JobTrain:
		key = "activity.job.train"
	case gen.JobScore:
		key = "activity.job.score"
	case gen.JobLetterPage:
		key = "activity.job.lettering"
	default:
		if strings.HasPrefix(j.Type, "export.") {
			key = "activity.job.export"
		}
		if j.Executor == jobs.ExecutorWorker {
			key = "activity.job.worker"
		}
	}
	v.Title = i18n.T(ctx, key)
	if j.Scope.PanelID != "" {
		var loc struct {
			Number int `db:"number"`
			Order  int `db:"order_idx"`
		}
		err := m.d.DB.Get(ctx, &loc, `SELECT p.number, n.order_idx FROM panels n JOIN pages p ON p.id = n.page_id WHERE n.id = ? AND p.project_id = ?`, j.Scope.PanelID, j.Scope.ProjectID)
		if err != nil && !db.IsNotFound(err) {
			return v, err
		}
		if err == nil {
			v.Title = i18n.T(ctx, "activity.job.panel_number", "page", loc.Number, "panel", loc.Order+1) + " · " + v.Title
		}
	}
	v.Owner = i18n.T(ctx, "activity.queue.server")
	if j.UserID != "" {
		var name struct {
			Name     string `db:"display_name"`
			Username string `db:"username"`
		}
		err := m.d.DB.Get(ctx, &name, `SELECT display_name, username FROM users WHERE id = ?`, j.UserID)
		if err != nil && !db.IsNotFound(err) {
			return v, err
		}
		v.Owner = name.Name
		if v.Owner == "" {
			v.Owner = name.Username
		}
		if v.Owner == "" {
			v.Owner = i18n.T(ctx, "activity.queue.former_member")
		}
	}
	model, provider := m.names.RouteNames(ctx, j.ModelID, j.ProviderID)
	v.Route = model
	if provider != "" {
		if v.Route != "" {
			v.Route += " · "
		}
		v.Route += provider
	}
	if v.Route == "" {
		v.Route = i18n.T(ctx, "activity.queue.server")
	}
	cost := j.Estimate
	if j.Finished() {
		cost = j.Actual
	}
	v.Metric = costText(ctx, cost)
	v.StudioURL = m.studioURL(ctx, j)
	return v, nil
}

// Retry belongs to the original feature's reviewed scope/estimate form. The
// queue never replays a stale private input or bypasses its feature guards.
func (m *Module) studioURL(ctx context.Context, j *jobs.Job) string {
	if j.Scope.ProjectID == "" {
		return ""
	}
	u := core.UserFrom(ctx)
	s := core.MustUse[studio.Service](m.d)
	if s.Authorize(ctx, studio.Actor{UserID: u.ID, SiteRole: u.Role}, j.Scope.ProjectID, studio.ActRead) != nil {
		return ""
	}
	base := "/studio/p/" + j.Scope.ProjectID
	if j.Type == gen.JobCharacterSheet {
		return base + "/characters"
	}
	chapter, pageID := j.Scope.ChapterID, j.Scope.PageID
	if pageID == "" && j.Scope.LayerID != "" {
		_ = m.d.DB.Get(ctx, &pageID, `SELECT n.page_id FROM layers l JOIN panels n ON n.id = l.panel_id JOIN pages p ON p.id = n.page_id WHERE l.id = ? AND p.project_id = ?`, j.Scope.LayerID, j.Scope.ProjectID)
	}
	if chapter == "" && pageID != "" {
		_ = m.d.DB.Get(ctx, &chapter, `SELECT chapter_id FROM pages WHERE id = ? AND project_id = ?`, pageID, j.Scope.ProjectID)
	}
	path := base + "/story"
	if chapter != "" {
		switch {
		case strings.HasPrefix(j.Type, "export."):
			path = base + "/chapters/" + chapter + "/export?job=" + j.ID
		case j.Type == gen.JobScript:
			path += "/" + chapter
		default:
			path = base + "/chapters/" + chapter + "/edit?job=" + j.ID
			if pageID != "" {
				path += "&page=" + pageID
			}
		}
	}
	if !ui.Ready(ctx, path) {
		return ""
	}
	return path
}

func (m *Module) loadJob(r *http.Request, id string) (*jobs.Job, error) {
	if !db.ValidID(id) {
		return nil, core.ErrNotFound()
	}
	j, err := m.d.Jobs.Get(r.Context(), id)
	if err != nil {
		return nil, err
	}
	if !m.d.Jobs.CanAccess(r.Context(), core.IdentityFrom(r.Context()), j) {
		return nil, core.ErrNotFound()
	}
	return j, nil
}

func (m *Module) cancelJob(w http.ResponseWriter, r *http.Request) error {
	if err := parseForm(w, r); err != nil {
		return core.ErrBadRequest(i18n.T(r.Context(), "activity.error.form"))
	}
	j, err := m.loadJob(r, r.PathValue("id"))
	if err != nil {
		return err
	}
	_, err = m.d.Jobs.Cancel(r.Context(), j.ID)
	if errors.Is(err, jobs.ErrNotActive) {
		web.Flash(w, r, "info", i18n.T(r.Context(), "activity.queue.already_finished"), "")
	} else if err != nil {
		return err
	} else {
		web.Flash(w, r, "success", i18n.T(r.Context(), "activity.queue.cancel_requested"), "")
	}
	core.Redirect(w, r, queueURL("", j.ID))
	return nil
}

func (m *Module) workerAction(w http.ResponseWriter, r *http.Request) error {
	if err := parseForm(w, r); err != nil {
		return core.ErrBadRequest(i18n.T(r.Context(), "activity.error.form"))
	}
	status := ""
	switch r.PathValue("action") {
	case "drain":
		status = jobs.WorkerDraining
	case "resume":
		status = jobs.WorkerOnline
	default:
		return core.ErrNotFound()
	}
	if err := m.d.Jobs.SetWorkerStatus(r.Context(), r.PathValue("id"), status); err != nil {
		if errors.Is(err, jobs.ErrNotActive) {
			return core.ErrConflict(i18n.T(r.Context(), "activity.queue.worker_unavailable"))
		}
		return err
	}
	web.Flash(w, r, "success", i18n.T(r.Context(), "activity.queue.worker_updated"), "")
	core.Redirect(w, r, "/queue")
	return nil
}

func costText(ctx context.Context, c jobs.Cost) string {
	if c.USD > 0 {
		return spend(c.USD)
	}
	if c.GPUSeconds > 0 {
		return gpuTime(ctx, c.GPUSeconds)
	}
	return i18n.T(ctx, "activity.queue.no_remote_spend")
}

// jobError is the same safe recovery copy the job API returns for a code.
func jobError(ctx context.Context, code string) string {
	return i18n.T(ctx, jobs.PublicErrorKey(code))
}
