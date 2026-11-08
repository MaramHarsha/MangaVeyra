package activityui

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/web"
	"github.com/MaramHarsha/MangaVeyra/internal/web/layout"
	"github.com/MaramHarsha/MangaVeyra/internal/web/ui"
)

type jobView struct {
	ID, Title, Owner, Route, State, Status, Metric, StudioURL, DetailURL, CancelURL string
	Tone                                                                            ui.Tone
	Progress                                                                        int
	Running, Cancelable                                                             bool
}

type eventView struct{ Label, Time, ISOTime string }
type jobDetail struct {
	Job                                        jobView
	Estimate, Actual, Created, Error, CloseURL string
	Events                                     []eventView
}
type workerAction struct{ URL, Label string }
type queueView struct {
	Past, Admin, All                                                       bool
	Subtitle, NextURL, PageURL, RefreshURL, LatestURL, Limit, Night, Usage string
	WorkerHint, WorkerLabel, WorkerIcon                                    string
	WorkerActions                                                          []workerAction
	Scope                                                                  []ui.SegLink
	Jobs                                                                   []jobView
	Detail                                                                 *jobDetail
	Active                                                                 bool
}

// listState is the URL-restorable queue position: the page cursor and, for
// admins only, the explicit all-members view. Every queue link keeps it.
type listState struct {
	cursor string
	all    bool
}

func (s listState) url(path, selected string) string {
	q := url.Values{}
	if s.all {
		q.Set("all", "1")
	}
	if s.cursor != "" {
		q.Set("cursor", s.cursor)
	}
	if selected != "" {
		q.Set("job", selected)
	}
	if len(q) == 0 {
		return path
	}
	return path + "?" + q.Encode()
}

// queueState reads the list state from a request. Only admins can ask for
// every member's jobs; everyone else always sees their own.
func queueState(r *http.Request) (listState, error) {
	q := r.URL.Query()
	st := listState{cursor: q.Get("cursor"), all: q.Get("all") == "1" && core.UserFrom(r.Context()).IsAdmin()}
	if st.cursor != "" && !db.ValidID(st.cursor) {
		return st, core.ErrBadRequest(i18n.T(r.Context(), "activity.error.cursor"))
	}
	return st, nil
}

func (m *Module) queuePage(w http.ResponseWriter, r *http.Request) error {
	v, err := m.loadQueue(r)
	if err != nil {
		return err
	}
	return m.ui.Page(w, r, http.StatusOK, web.PageOpts{Title: i18n.T(r.Context(), "activity.queue.title"),
		Section: layout.SectionStudio, Nav: layout.NavModels, Tab: layout.TabHome}, queueBody(v))
}

func (m *Module) loadDetail(r *http.Request, st listState, v *queueView) error {
	selected := r.URL.Query().Get("job")
	if selected == "" {
		return nil
	}
	ctx := r.Context()
	j, err := m.loadJob(r, selected)
	if err != nil {
		return err
	}
	// A worker job is part of its parent (one row per job): show the parent.
	if j.ParentID != "" && j.Executor == jobs.ExecutorWorker {
		if parent, err := m.loadJob(r, j.ParentID); err == nil {
			j = parent
			v.PageURL, v.RefreshURL = st.url("/queue", j.ID), st.url("/queue/status", j.ID)
		} else if !errors.Is(err, jobs.ErrNotFound) && !isNotFound(err) {
			return err
		}
	}
	children, err := m.d.Jobs.ActiveWorkerChildren(ctx, []string{j.ID})
	if err != nil {
		return err
	}
	view, err := m.jobView(ctx, j, children[j.ID], st)
	if err != nil {
		return err
	}
	detail := &jobDetail{Job: view, Estimate: costText(ctx, j.Estimate, false), Actual: costText(ctx, j.Actual, false),
		Created: timestamp(ctx, j.CreatedAt), CloseURL: st.url("/queue", "")}
	if !j.Finished() {
		detail.Actual = i18n.T(ctx, "activity.queue.unreported")
	}
	if j.Error != nil && j.State != jobs.StateDone {
		detail.Error = jobError(ctx, j.Error.Code)
	}
	events, err := m.d.Jobs.RecentEvents(ctx, j.ID, 50)
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
		detail.Events = append(detail.Events, eventView{Label: i18n.T(ctx, key), Time: timestamp(ctx, ev.CreatedAt), ISOTime: ev.CreatedAt.UTC().Format(time.RFC3339)})
	}
	v.Detail = detail
	v.Active = v.Active || !j.Finished()
	return nil
}

func isNotFound(err error) bool {
	var e *core.APIError
	return errors.As(err, &e) && e.Status == http.StatusNotFound
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
	st, err := queueState(r)
	if err != nil {
		return queueView{}, err
	}
	owner := u.ID
	if st.all {
		owner = ""
	}
	items, err := m.d.Jobs.List(ctx, jobs.Filter{UserID: owner, Before: st.cursor, Limit: listLimit + 1, ExcludeWorkerChildren: true})
	if err != nil {
		return queueView{}, err
	}
	selected := r.URL.Query().Get("job")
	v := queueView{Past: st.cursor != "", Admin: u.IsAdmin(), All: st.all, PageURL: st.url("/queue", selected),
		RefreshURL: st.url("/queue/status", selected), LatestURL: listState{all: st.all}.url("/queue", "")}
	if u.IsAdmin() {
		v.Scope = []ui.SegLink{
			{Href: "/queue", Label: i18n.T(ctx, "activity.queue.mine"), Current: !st.all},
			{Href: listState{all: true}.url("/queue", ""), Label: i18n.T(ctx, "activity.queue.all_users"), Current: st.all},
		}
	}
	if len(items) > listLimit {
		items = items[:listLimit]
		v.NextURL = listState{cursor: items[len(items)-1].ID, all: st.all}.url("/queue", "")
	}
	var parents []string
	for _, j := range items {
		if !j.Finished() {
			parents = append(parents, j.ID)
		}
	}
	children, err := m.d.Jobs.ActiveWorkerChildren(ctx, parents)
	if err != nil {
		return v, err
	}
	for _, j := range items {
		view, err := m.jobView(ctx, j, children[j.ID], st)
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
	workers, err := m.d.Jobs.ListWorkers(ctx)
	if err != nil {
		return v, err
	}
	m.workerSummary(ctx, &v, workers)
	v.Limit = i18n.T(ctx, "activity.queue.limits", "gpu", limitText(ctx, "activity.queue.limit_gpu", inst.Limits.RunningGPUJobs),
		"server", limitText(ctx, "activity.queue.limit_server", inst.Limits.RunningJobs))
	v.Night = i18n.T(ctx, "activity.queue.night_off")
	if inst.NightMode.Enabled {
		v.Night = i18n.T(ctx, "activity.queue.night_hours", "start", integer(inst.NightMode.StartHour)+":00", "end", integer(inst.NightMode.EndHour)+":00")
	}
	usage, err := m.d.Jobs.CompletedUsage(ctx, owner, weekStart(time.Now()))
	if err != nil {
		return v, err
	}
	v.Usage = i18n.T(ctx, "activity.queue.usage", "gpu", gpuTime(ctx, usage.GPUSeconds), "spend", spend(usage.USD))
	if err := m.loadDetail(r, st, &v); err != nil {
		return v, err
	}
	return v, nil
}

// limitText is a configured per-user running limit; 0 means no limit.
func limitText(ctx context.Context, key string, n int) string {
	if n <= 0 {
		return i18n.T(ctx, key+"_none")
	}
	return i18n.T(ctx, key, "count", n)
}

// workerSummary fills the subtitle and the designed worker button. Slots and
// the GPU name come from registered workers: online ones, or the paused
// (draining) ones while every worker is paused. The button names the action
// it performs: Pause while any worker runs, otherwise Resume.
func (m *Module) workerSummary(ctx context.Context, v *queueView, workers []jobs.Worker) {
	var online, paused []jobs.Worker
	for _, wk := range workers {
		switch wk.Status {
		case jobs.WorkerOnline:
			online = append(online, wk)
		case jobs.WorkerDraining:
			paused = append(paused, wk)
		}
	}
	v.Subtitle = i18n.T(ctx, "activity.queue.remote_subtitle")
	shown, key := online, "activity.queue.subtitle"
	if len(online) == 0 {
		shown, key = paused, "activity.queue.subtitle_paused"
	}
	if len(shown) > 0 {
		slots, name, best := 0, "", 0
		for _, wk := range shown {
			slots += wk.MaxConcurrent
			for _, g := range wk.GPUs.V {
				if g.VRAMTotalMB > best || name == "" {
					best, name = g.VRAMTotalMB, g.Name
				}
			}
		}
		if name == "" {
			name = i18n.T(ctx, "activity.queue.worker_pool")
		}
		v.Subtitle = i18n.T(ctx, key, "compute", name, "count", slots)
	}
	v.WorkerLabel, v.WorkerIcon, v.WorkerHint = i18n.T(ctx, "activity.queue.pause"), "player-pause", i18n.T(ctx, "activity.queue.admin_only")
	if !v.Admin {
		return
	}
	v.WorkerHint = i18n.T(ctx, "activity.queue.no_worker")
	for _, wk := range online {
		v.WorkerActions = append(v.WorkerActions, workerAction{URL: "/queue/workers/" + wk.ID + "/drain", Label: i18n.T(ctx, "activity.queue.pause_named", "worker", wk.Name)})
	}
	for _, wk := range paused {
		v.WorkerActions = append(v.WorkerActions, workerAction{URL: "/queue/workers/" + wk.ID + "/resume", Label: i18n.T(ctx, "activity.queue.resume_named", "worker", wk.Name)})
	}
	if len(online) == 0 && len(paused) > 0 {
		v.WorkerLabel, v.WorkerIcon = i18n.T(ctx, "activity.queue.resume"), "player-play"
	}
	if len(v.WorkerActions) == 1 {
		v.WorkerHint = v.WorkerActions[0].Label
	}
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
	st, err := queueState(r)
	if err != nil {
		return err
	}
	j, err := m.loadJob(r, r.PathValue("id"))
	if err != nil {
		return err
	}
	// Canceling a parent also stops the worker job it runs (jobs.Queue.Cancel).
	_, err = m.d.Jobs.Cancel(r.Context(), j.ID)
	if errors.Is(err, jobs.ErrNotActive) {
		web.Flash(w, r, "info", i18n.T(r.Context(), "activity.queue.already_finished"), "")
	} else if err != nil {
		return err
	} else {
		web.Flash(w, r, "success", i18n.T(r.Context(), "activity.queue.cancel_requested"), "")
	}
	core.Redirect(w, r, st.url("/queue", j.ID))
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

// costText is a recorded actual or, with estimate, an approximate estimate:
// remote spend when there is any, otherwise GPU time.
func costText(ctx context.Context, c jobs.Cost, estimate bool) string {
	var s string
	switch {
	case c.USD > 0 && !nonFinite(c.USD):
		s = spend(c.USD)
	case c.GPUSeconds > 0 && !nonFinite(c.GPUSeconds):
		s = shortDuration(ctx, c.GPUSeconds)
	default:
		return i18n.T(ctx, "activity.queue.no_remote_spend")
	}
	if estimate {
		return i18n.T(ctx, "activity.queue.approx", "value", s)
	}
	return s
}

// jobError is the same safe recovery copy the job API returns for a code.
func jobError(ctx context.Context, code string) string {
	return i18n.T(ctx, jobs.PublicErrorKey(code))
}
