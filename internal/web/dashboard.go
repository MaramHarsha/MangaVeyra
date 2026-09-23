package web

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/web/layout"
	"github.com/MaramHarsha/MangaVeyra/internal/web/pages/dashboard"
)

// dashboard is the studio home (B1). The studio module replaces the data by
// providing a dashboard.Source, or the whole page by registering GET /studio.
func (u *UI) dashboard(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	usr := core.UserFrom(ctx)
	prefs := core.PrefsFrom(ctx)
	d := dashboard.Data{
		Greeting:  Greeting(ctx, usr.Name(), usr.Timezone, time.Now()),
		ShowXP:    prefs.ShowRanksXP,
		Progress:  dashboard.ProgressFor(usr.XP, usr.Level),
		CanCreate: usr.CanCreate(),
		Compute:   u.computeView(ctx),
	}
	if src, ok := core.Use[dashboard.Source](u.d); ok {
		ps, err := src.RecentProjects(ctx, usr.ID, 4)
		if err != nil {
			u.log.Warn("web: dashboard projects", "err", err)
		}
		d.Projects = ps
	}
	if len(d.Projects) == 0 {
		d.Sub = i18n.T(ctx, "dash.sub.empty")
	} else if p := d.Projects[0]; p.ChapterLine != "" {
		d.Sub = i18n.T(ctx, "dash.sub.continue", "title", p.Title)
	}
	return u.Page(w, r, http.StatusOK, PageOpts{
		Title: i18n.T(ctx, "dash.title"), MobileTitle: i18n.T(ctx, "nav.studio"),
		Section: layout.SectionStudio, Nav: layout.NavHome, Tab: layout.TabHome,
	}, dashboard.Page(d))
}

// Greeting returns "Good morning/afternoon/evening, <first name>" for the member's time zone.
func Greeting(ctx context.Context, name, tz string, now time.Time) string {
	loc := time.UTC
	if tz != "" {
		if l, err := time.LoadLocation(tz); err == nil {
			loc = l
		}
	}
	key := "dash.greeting.evening"
	switch h := now.In(loc).Hour(); {
	case h >= 5 && h < 12:
		key = "dash.greeting.morning"
	case h >= 12 && h < 18:
		key = "dash.greeting.afternoon"
	}
	first := strings.Fields(name)
	if len(first) > 0 {
		name = first[0]
	}
	return i18n.T(ctx, key, "name", name)
}

// computeView renders the compute state for the dashboard card and mobile line.
func (u *UI) computeView(ctx context.Context) dashboard.ComputeView {
	c := u.Compute(ctx)
	inst := u.d.Settings.MustInstance(ctx)
	v := dashboard.ComputeView{Name: c.WorkerName, Online: c.WorkersOnline > 0, QueueURL: "/queue"}
	if v.Name == "" {
		v.Name = inst.Name
	}
	switch {
	case c.GPU != "":
		v.GPU = i18n.T(ctx, "dash.compute.gpu_value", "gpu", c.GPU, "used", GB(c.VRAMUsedMB), "total", GB(c.VRAMTotalMB))
	case c.WorkersOnline > 0:
		v.GPU = i18n.T(ctx, "dash.compute.gpu_cpu")
	default:
		v.GPU = i18n.T(ctx, "dash.compute.gpu_none")
	}
	busy := c.Queued + c.Running
	if busy == 0 {
		v.Queue = i18n.T(ctx, "dash.compute.queue_empty")
	} else {
		v.Queue = i18n.T(ctx, "dash.compute.queue_value", "queued", c.Queued, "running", c.Running)
	}
	v.Workers = i18n.T(ctx, "dash.compute.workers_value", "online", c.WorkersOnline, "total", c.WorkersTotal)
	if inst.LocalOnly {
		v.Remote = i18n.T(ctx, "dash.compute.remote_local_only")
	} else {
		v.Remote = i18n.T(ctx, "dash.compute.remote_none")
	}
	state := i18n.T(ctx, "workers.status.offline")
	if v.Online {
		state = i18n.T(ctx, "workers.status.online")
	}
	v.Line = i18n.T(ctx, "dash.compute.line", "name", v.Name, "state", strings.ToLower(state), "queue", v.Queue)
	return v
}
