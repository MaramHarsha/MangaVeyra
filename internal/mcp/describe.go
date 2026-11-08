package mcp

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
)

// describe is the one-line description of an action in G13 ("Rewrote caption
// 7.4", "Asked to publish ch. 8").
func describe(ctx context.Context, r actionRow) string {
	d := r.Detail.V
	switch r.Kind {
	case kindScriptEdit:
		switch {
		case len(d.Labels) == 1 && d.Caption:
			return i18n.T(ctx, "agents.action.caption", "label", d.Labels[0])
		case len(d.Labels) == 1 && d.Dialogue:
			return i18n.T(ctx, "agents.action.dialogue", "label", d.Labels[0])
		case len(d.Labels) == 1:
			return i18n.T(ctx, "agents.action.panel", "label", d.Labels[0])
		}
		return i18n.T(ctx, "agents.action.panels", "count", len(d.Labels), "chapter", d.ChapterNumber)
	case kindCharacterCreate:
		return i18n.T(ctx, "agents.action.character_create", "name", d.Name)
	case kindCharacterEdit:
		return i18n.T(ctx, "agents.action.character_edit", "name", d.Name)
	case kindGenerate:
		return generationText(ctx, r.Action, d, false)
	case kindRequest:
		switch r.Action {
		case reqGenerate:
			kind := ""
			if d.Generation != nil {
				kind = d.Generation.Kind
			}
			text := generationText(ctx, kind, d, true)
			if r.Estimate.V.USD > 0 {
				text += " · ~" + spend(r.Estimate.V.USD)
			}
			return text
		case reqPublish:
			return i18n.T(ctx, "agents.ask.publish", "chapter", d.ChapterNumber)
		case reqSchedule:
			return i18n.T(ctx, "agents.ask.schedule", "chapter", d.ChapterNumber)
		case reqDeletePage:
			return i18n.T(ctx, "agents.ask.delete_page", "page", d.PageNumber, "chapter", d.ChapterNumber)
		case reqDeleteProject:
			return i18n.T(ctx, "agents.ask.delete_project")
		case reqShare:
			return i18n.T(ctx, "agents.ask.share")
		case reqChangeRole:
			return i18n.T(ctx, "agents.ask.change_role")
		case reqUnlock:
			switch {
			case d.PanelLabel != "":
				return i18n.T(ctx, "agents.ask.unlock_panel", "label", d.PanelLabel)
			case d.PageNumber > 0:
				return i18n.T(ctx, "agents.ask.unlock_page", "page", d.PageNumber, "chapter", d.ChapterNumber)
			}
			return i18n.T(ctx, "agents.ask.unlock_chapter", "chapter", d.ChapterNumber)
		}
	}
	return i18n.T(ctx, "agents.action.other")
}

func generationText(ctx context.Context, kind string, d actionDetail, ask bool) string {
	prefix := "agents.action.generate."
	if ask {
		prefix = "agents.ask.generate."
	}
	switch kind {
	case "script":
		return i18n.T(ctx, prefix+"script", "chapter", d.ChapterNumber)
	case "page":
		return i18n.T(ctx, prefix+"page", "page", d.PageNumber, "chapter", d.ChapterNumber)
	case "region":
		return i18n.T(ctx, prefix+"region", "label", d.PanelLabel)
	}
	return i18n.T(ctx, prefix+"other")
}

// summaryEN is the action description list_recent_actions returns to agents.
func summaryEN(r actionRow) string {
	return describe(i18n.WithLocale(context.Background(), "en"), r)
}

// age is the design's short elapsed time ("2 h").
func age(ctx context.Context, then, now time.Time) string {
	d := max(time.Duration(0), now.Sub(then))
	switch {
	case d < time.Minute:
		return i18n.T(ctx, "agents.time.now")
	case d < time.Hour:
		return i18n.T(ctx, "agents.time.minutes", "count", int(d/time.Minute))
	case d < 24*time.Hour:
		return i18n.T(ctx, "agents.time.hours", "count", int(d/time.Hour))
	}
	return i18n.T(ctx, "agents.time.days", "count", int(d/(24*time.Hour)))
}

// spend is remote spend on the person's own keys in two decimals ("$0.50");
// an amount below one cent reads "<$0.01".
func spend(usd float64) string {
	switch {
	case math.IsNaN(usd) || math.IsInf(usd, 0) || !(usd > 0):
		return "$0.00"
	case usd < 0.005:
		return "<$0.01"
	}
	return fmt.Sprintf("$%.2f", usd)
}

// gpuTime is a job's GPU time as the design writes it ("1 min GPU").
func gpuTime(ctx context.Context, seconds float64) string {
	s := max(0, seconds)
	if s < 59.5 {
		return i18n.T(ctx, "agents.gpu.seconds", "count", max(1, int(math.Round(s))))
	}
	return i18n.T(ctx, "agents.gpu.minutes", "count", int(math.Round(s/60)))
}

// statusText is the right-hand status of an action row.
func statusText(ctx context.Context, r actionRow, j *jobs.Job, now time.Time) string {
	when := age(ctx, r.CreatedAt, now)
	switch r.Status {
	case statusPending:
		return i18n.T(ctx, "agents.status.waiting")
	case statusApproved:
		return when + " · " + i18n.T(ctx, "agents.status.approved")
	case statusDeclined:
		return when + " · " + i18n.T(ctx, "agents.status.declined")
	case statusUndone:
		return when + " · " + i18n.T(ctx, "agents.status.undone")
	}
	if r.Kind == kindGenerate && j != nil {
		switch j.State {
		case jobs.StateQueued:
			return when + " · " + i18n.T(ctx, "agents.status.queued")
		case jobs.StateRunning:
			return when + " · " + i18n.T(ctx, "agents.status.running")
		case jobs.StateFailed:
			return when + " · " + i18n.T(ctx, "agents.status.failed")
		case jobs.StateCanceled:
			return when + " · " + i18n.T(ctx, "agents.status.canceled")
		case jobs.StateDone:
			switch {
			case j.Actual.USD > 0:
				return when + " · " + spend(j.Actual.USD)
			case j.Actual.GPUSeconds > 0:
				return when + " · " + gpuTime(ctx, j.Actual.GPUSeconds)
			}
		}
	}
	return when
}
