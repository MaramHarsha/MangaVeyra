package activityui

import (
	"context"
	"encoding/json"
	"math"
	"regexp"
	"strings"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/gen"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/web/ui"
)

// Worker job types a parent starts on a Python worker. They appear as part
// of their parent, but older notifications may still name them.
const (
	workerScore = "character.score"
	workerTrain = "character.train"
)

// jobLabelKey is the message key naming what a job type makes.
func jobLabelKey(typ, executor string) string {
	switch typ {
	case gen.JobScript:
		return "activity.job.script"
	case gen.JobCharacterSheet:
		return "activity.job.character"
	case gen.JobPagesFromScript:
		return "activity.job.pages"
	case gen.JobPanel:
		return "activity.job.panel"
	case gen.JobRegionEdit:
		return "activity.job.region"
	case gen.JobPage:
		return "activity.job.page"
	case gen.JobRepair:
		return "activity.job.repair"
	case gen.JobTrain, workerTrain:
		return "activity.job.train"
	case gen.JobScore, workerScore:
		return "activity.job.score"
	case gen.JobLetterPage:
		return "activity.job.lettering"
	case gen.JobCover:
		return "activity.job.cover"
	}
	switch {
	case strings.HasPrefix(typ, "export."):
		return "activity.job.export"
	case executor == jobs.ExecutorWorker || strings.HasPrefix(typ, "image."):
		return "activity.job.worker"
	}
	return "activity.job.other"
}

var rawJobType = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z0-9_]+)+$`)

// looksLikeJobType reports whether s is an internal job type such as
// "page.generate", which older notifications stored as their body.
func looksLikeJobType(s string) bool { return len(s) <= 64 && rawJobType.MatchString(s) }

// jobCharacterID is the character a Character Lock job works on. The input
// stays private; only the id is read to name and link the character.
func jobCharacterID(j *jobs.Job) string {
	if j.Type != gen.JobScore && j.Type != gen.JobTrain {
		return ""
	}
	var p struct {
		CharacterID string `json:"character_id"`
	}
	if json.Unmarshal(j.Input, &p) != nil || !db.ValidID(p.CharacterID) {
		return ""
	}
	return p.CharacterID
}

// jobTitle names a job the way the queue shows it: "Panel 7.5 · Region edit",
// "Character assessment · Rin Asagiri" or the plain label. It never shows an
// internal type, id, prompt or payload.
func jobTitle(ctx context.Context, d *db.DB, j *jobs.Job) (string, error) {
	title := i18n.T(ctx, jobLabelKey(j.Type, j.Executor))
	var loc struct {
		Number int `db:"number"`
		Order  int `db:"order_idx"`
	}
	var err error
	switch {
	case j.Scope.ProjectID == "":
		err = db.ErrNotFound
	case j.Scope.PanelID != "":
		err = d.Get(ctx, &loc, `SELECT p.number, n.order_idx FROM panels n JOIN pages p ON p.id = n.page_id WHERE n.id = ? AND p.project_id = ?`, j.Scope.PanelID, j.Scope.ProjectID)
	case j.Scope.LayerID != "":
		err = d.Get(ctx, &loc, `SELECT p.number, n.order_idx FROM layers l JOIN panels n ON n.id = l.panel_id JOIN pages p ON p.id = n.page_id
			WHERE l.id = ? AND p.project_id = ?`, j.Scope.LayerID, j.Scope.ProjectID)
	default:
		err = db.ErrNotFound
	}
	if err != nil && !db.IsNotFound(err) {
		return "", err
	}
	if err == nil {
		return i18n.T(ctx, "activity.job.panel_number", "page", loc.Number, "panel", loc.Order+1) + " · " + title, nil
	}
	if id := jobCharacterID(j); id != "" {
		var name string
		err := d.Get(ctx, &name, `SELECT name FROM characters WHERE id = ? AND project_id = ?`, id, j.Scope.ProjectID)
		if err != nil && !db.IsNotFound(err) {
			return "", err
		}
		if strings.TrimSpace(name) != "" {
			title += " · " + name
		}
	}
	return title, nil
}

// foldedProgress is the progress of a running parent whose current work runs
// in a worker child (one row per job). Before the parent reports any progress
// of its own, the child's measurement is the job's; a parent that mirrors its
// child (same step counters) or measures without steps keeps its own value;
// otherwise the child adds at most one step's share (1/(steps+1)), so the bar
// never runs ahead of the measured work.
func foldedProgress(parent *jobs.Job, children []*jobs.Job) float64 {
	p := clamp01(parent.Progress)
	var child *jobs.Job
	for _, c := range children {
		if c.State == jobs.StateRunning {
			child = c
		}
	}
	if child == nil || parent.State != jobs.StateRunning {
		return p
	}
	c := clamp01(child.Progress)
	switch {
	case parent.Steps <= 0 && p == 0:
		return c
	case parent.Steps <= 0, parent.Step == child.Step && parent.Steps == child.Steps:
		return p
	}
	return min(1, p+c/float64(parent.Steps+1))
}

func clamp01(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return min(1, max(0, v))
}

// jobStatus is the queue badge: the state word, "Stopping" while a requested
// cancellation is not yet acknowledged, or the queued position.
func (m *Module) jobStatus(ctx context.Context, j *jobs.Job) (string, ui.Tone, error) {
	switch {
	case j.State == jobs.StateDone:
		return i18n.T(ctx, "activity.state.done"), ui.ToneSuccess, nil
	case j.State == jobs.StateFailed:
		return i18n.T(ctx, "activity.state.failed"), ui.ToneError, nil
	case j.Finished():
		return i18n.T(ctx, "activity.state."+j.State), ui.ToneMuted, nil
	case j.CancelRequested:
		return i18n.T(ctx, "activity.state.stopping"), ui.ToneWarning, nil
	case j.State == jobs.StateQueued:
		position, err := m.d.Jobs.Position(ctx, j)
		if err != nil {
			return "", "", err
		}
		return i18n.T(ctx, "activity.state.position", "count", position+1), ui.ToneInfo, nil
	}
	return i18n.T(ctx, "activity.state.running"), ui.ToneInfo, nil
}

func (m *Module) jobView(ctx context.Context, j *jobs.Job, children []*jobs.Job, st listState) (jobView, error) {
	v := jobView{ID: j.ID, State: j.State, Progress: percent(foldedProgress(j, children)), Running: j.State == jobs.StateRunning,
		Cancelable: !j.Finished() && !j.CancelRequested, DetailURL: st.url("/queue", j.ID), CancelURL: st.url("/queue/jobs/"+j.ID+"/cancel", "")}
	var err error
	if v.Status, v.Tone, err = m.jobStatus(ctx, j); err != nil {
		return v, err
	}
	if v.Title, err = jobTitle(ctx, m.d.DB, j); err != nil {
		return v, err
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
	v.Route = m.route(ctx, j)
	if j.Finished() {
		v.Metric = costText(ctx, j.Actual, false)
	} else {
		v.Metric = costText(ctx, j.Estimate, true)
	}
	v.StudioURL = m.studioURL(ctx, j)
	return v, nil
}

// route is "model · provider" with display names, never internal IDs.
func (m *Module) route(ctx context.Context, j *jobs.Job) string {
	model, provider := m.names.RouteNames(ctx, j.ModelID, j.ProviderID)
	// The routing catalogue does not list Character Lock scorers; gen names them
	// the same way on C3, so the queue and the character page always agree.
	if key := gen.LockModelLabelKey(j.ModelID); key != "" {
		model = i18n.T(ctx, key)
	}
	route := model
	if provider != "" {
		if route != "" {
			route += " · "
		}
		route += provider
	}
	if route == "" {
		route = i18n.T(ctx, "activity.queue.server")
	}
	return route
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
	path := base + "/story"
	switch {
	case j.Type == gen.JobCharacterSheet:
		path = base + "/characters"
	case j.Type == gen.JobScore || j.Type == gen.JobTrain:
		path = base + "/characters"
		if id := jobCharacterID(j); id != "" {
			path += "/" + id
		}
	default:
		chapter, pageID := j.Scope.ChapterID, j.Scope.PageID
		if pageID == "" && j.Scope.LayerID != "" {
			_ = m.d.DB.Get(ctx, &pageID, `SELECT n.page_id FROM layers l JOIN panels n ON n.id = l.panel_id JOIN pages p ON p.id = n.page_id WHERE l.id = ? AND p.project_id = ?`, j.Scope.LayerID, j.Scope.ProjectID)
		}
		if chapter == "" && pageID != "" {
			_ = m.d.DB.Get(ctx, &chapter, `SELECT chapter_id FROM pages WHERE id = ? AND project_id = ?`, pageID, j.Scope.ProjectID)
		}
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
	}
	if !ui.Ready(ctx, path) {
		return ""
	}
	return path
}
