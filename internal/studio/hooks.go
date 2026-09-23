package studio

import (
	"context"
	"fmt"
	"time"

	"github.com/a-h/templ"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
)

// Hooks other modules fill through core.Provide. Every one is optional: the
// studio falls back to built-in behaviour when it is absent.

// RouteRequest describes an AI task the studio is about to enqueue.
type RouteRequest struct {
	UserID    string
	ProjectID string
	// Task is the job type ("script.outline", "character.sheet", "character.train", "script.assist").
	Task string
}

// RoutePreview is what the routing chip shows: "Auto → Llama 3.3 70B via Ollama · ~2 min".
type RoutePreview struct {
	// Auto is the mode word ("Auto", "Pinned").
	Auto string
	// Pick is the chosen model, where it runs and the estimate.
	Pick string
	// WhyHref links to the router explanation ("" hides the link).
	WhyHref string
	// Seconds is the estimated duration (0 = unknown).
	Seconds float64
}

// RoutePreviewer is provided by the ai module (core.Provide[studio.RoutePreviewer])
// to explain which model the router would pick for a task. Without it the
// studio shows the built-in placeholder provider.
type RoutePreviewer interface {
	PreviewRoute(ctx context.Context, req RouteRequest) (RoutePreview, error)
}

// ComputeStats is provided by the ai module to fill the dashboard compute
// card's "this week" and "remote spend" lines from its spend tracking.
// Without it the studio reads the jobs and spend_ledger tables.
type ComputeStats interface {
	// WeekUsage returns finished panel-generating jobs and GPU seconds in the last 7 days for userID.
	WeekUsage(ctx context.Context, userID string) (panels int, gpuSeconds float64, err error)
	// MonthSpend returns remote spend on the user's own keys this month and the main provider's name.
	MonthSpend(ctx context.Context, userID string) (usd float64, provider string, err error)
}

// ProjectSettingsSection lets another module add a section to the project
// settings page (the ai module shows routing pins there).
type ProjectSettingsSection interface {
	ProjectSettingsSection(ctx context.Context, p *Project, canEdit bool) (templ.Component, error)
}

// routePreview asks the router, or describes the built-in placeholder provider.
func (m *Module) routePreview(ctx context.Context, userID, projectID, task string) RoutePreview {
	if rp, ok := core.Use[RoutePreviewer](m.d); ok {
		if p, err := rp.PreviewRoute(ctx, RouteRequest{UserID: userID, ProjectID: projectID, Task: task}); err == nil && p.Pick != "" {
			if p.Auto == "" {
				p.Auto = i18n.T(ctx, "studio.route.auto")
			}
			return p
		}
	}
	return RoutePreview{Auto: i18n.T(ctx, "studio.route.auto"), Pick: i18n.T(ctx, "studio.route.placeholder"), Seconds: 2}
}

// computeWeek returns this week's panels and GPU seconds for a user.
func (m *Module) computeWeek(ctx context.Context, userID string) (int, float64) {
	if cs, ok := core.Use[ComputeStats](m.d); ok {
		if n, s, err := cs.WeekUsage(ctx, userID); err == nil {
			return n, s
		}
	}
	since := time.Now().UTC().Add(-7 * 24 * time.Hour)
	var n int
	_ = m.d.DB.Get(ctx, &n, `SELECT COUNT(*) FROM jobs WHERE user_id = ? AND state = 'done' AND finished_at > ?
		AND type IN ('image.generate', 'image.edit', 'image.inpaint', 'panel.generate', 'layer.generate', 'region.edit', 'sketch.ink')`, userID, since)
	var secs float64
	_ = m.d.DB.Get(ctx, &secs, `SELECT COALESCE(SUM(gpu_seconds), 0) FROM spend_ledger WHERE user_id = ? AND created_at > ?`, userID, since)
	return n, secs
}

// computeSpend returns this month's remote spend on the user's own keys.
func (m *Module) computeSpend(ctx context.Context, userID string) (float64, string) {
	if cs, ok := core.Use[ComputeStats](m.d); ok {
		if usd, prov, err := cs.MonthSpend(ctx, userID); err == nil {
			return usd, prov
		}
	}
	month := time.Now().UTC().Format("2006-01")
	var usd float64
	_ = m.d.DB.Get(ctx, &usd, `SELECT COALESCE(SUM(usd), 0) FROM spend_ledger WHERE user_id = ? AND month = ? AND kind = 'remote'`, userID, month)
	var prov string
	_ = m.d.DB.Get(ctx, &prov, `SELECT COALESCE(p.name, l.provider_id) FROM spend_ledger l LEFT JOIN providers p ON p.id = l.provider_id
		WHERE l.user_id = ? AND l.month = ? AND l.kind = 'remote' GROUP BY l.provider_id, p.name ORDER BY SUM(l.usd) DESC LIMIT 1`, userID, month)
	return usd, prov
}

// queueEstimate returns queued + running jobs and their estimated seconds left.
func (m *Module) queueEstimate(ctx context.Context) (int, float64) {
	var n int
	_ = m.d.DB.Get(ctx, &n, `SELECT COUNT(*) FROM jobs WHERE state IN ('queued', 'running')`)
	// Estimates are JSON; sum them in Go (running jobs count half: they are under way).
	var rows []struct {
		State    string `db:"state"`
		Estimate string `db:"estimate"`
	}
	_ = m.d.DB.Select(ctx, &rows, `SELECT state, estimate FROM jobs WHERE state IN ('queued', 'running') LIMIT 500`)
	total := 0.0
	for _, r := range rows {
		var c struct {
			GPUSeconds float64 `json:"gpu_seconds"`
		}
		if jsonUnmarshal(r.Estimate, &c) == nil {
			if r.State == "running" {
				c.GPUSeconds /= 2
			}
			total += c.GPUSeconds
		}
	}
	return n, total
}

// FormatDuration formats seconds as "~1 min 40 s", "3 h 10 m".
func FormatDuration(ctx context.Context, secs float64) string {
	s := int(secs + .5)
	switch {
	case s < 60:
		return i18n.T(ctx, "studio.dur.s", "s", s)
	case s < 3600:
		if s%60 == 0 {
			return i18n.T(ctx, "studio.dur.m", "m", s/60)
		}
		return i18n.T(ctx, "studio.dur.ms", "m", s/60, "s", s%60)
	}
	return i18n.T(ctx, "studio.dur.hm", "h", s/3600, "m", (s%3600)/60)
}

// FormatUSD formats remote spend ("$4.18").
func FormatUSD(v float64) string { return fmt.Sprintf("$%.2f", v) }
