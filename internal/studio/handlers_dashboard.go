package studio

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/web"
	"github.com/MaramHarsha/MangaVeyra/internal/web/layout"
	"github.com/MaramHarsha/MangaVeyra/internal/web/pages/dashboard"
	"github.com/MaramHarsha/MangaVeyra/internal/web/ui"
)

// dashSource lets the foundation's dashboard use real data too.
type dashSource struct{ m *Module }

func (d dashSource) RecentProjects(ctx context.Context, userID string, limit int) ([]dashboard.Project, error) {
	list, err := d.m.svc.ListProjects(ctx, userID, ProjectFilter{Limit: limit})
	if err != nil {
		return nil, err
	}
	out := make([]dashboard.Project, 0, len(list))
	for i := range list {
		out = append(out, d.m.cardProject(ctx, &list[i], userID).Project)
	}
	return out, nil
}

// ProjectCard is a project as the dashboard and projects list show it.
type ProjectCard struct {
	dashboard.Project
	ThumbURL  string
	Summary   *ProjectSummary
	Mobile    string // "Draft · 2 h"
	Shared    bool
	RoleLabel string
}

// cardProject builds the card data for a project summary.
func (m *Module) cardProject(ctx context.Context, p *ProjectSummary, userID string) ProjectCard {
	badge := p.Badge()
	c := ProjectCard{Summary: p, ThumbURL: m.svc.MediaURL(p.ThumbKey)}
	c.ID, c.Title, c.Href, c.UpdatedAt = p.ID, p.Title, "/studio/p/"+p.ID+"/story", p.UpdatedAt
	c.ThumbLabel = i18n.T(ctx, "studio.card.thumb")
	when := RelTime(ctx, p.UpdatedAt)
	switch {
	case p.OwnerID != userID:
		c.Shared = true
		c.Meta = i18n.T(ctx, "studio.card.shared_by", "name", p.OwnerName)
	case badge == BadgeImported:
		c.Meta = i18n.T(ctx, "studio.card.imported")
	case badge == BadgePublished:
		c.Meta = i18n.T(ctx, "studio.card.published", "n", p.LastChapter, "when", when)
	case badge == BadgeScript:
		c.Meta = i18n.T(ctx, "studio.card.script", "count", p.Chapters)
	case badge == BadgeWebtoon:
		c.Meta = i18n.T(ctx, "studio.card.webtoon", "count", p.Panels)
	case p.Chapters == 0:
		c.Meta = i18n.T(ctx, "studio.card.premise")
	default:
		c.Meta = i18n.T(ctx, "studio.card.draft", "n", p.LastChapter, "pages", p.Pages, "when", when)
	}
	c.Badges = []ui.BadgeSpec{{Tone: badgeTone(badge), Text: i18n.T(ctx, "studio.badge."+badge)}}
	if c.Shared {
		c.RoleLabel = i18n.T(ctx, "studio.role."+p.Role)
		c.Badges = []ui.BadgeSpec{{Tone: ui.ToneText, Icon: "users", Text: c.RoleLabel}}
	} else if p.Format == FormatMangaRTL && badge == BadgeDraft {
		c.Badges = append(c.Badges, ui.BadgeSpec{Tone: ui.ToneMuted, Text: "RTL"})
	}
	c.Mobile = i18n.T(ctx, "studio.badge."+badge) + " · " + when
	if c.Shared {
		c.Mobile = c.Meta
	}
	return c
}

func badgeTone(b string) ui.Tone {
	switch b {
	case BadgePublished:
		return ui.ToneSuccess
	case BadgeScript, BadgeWebtoon:
		return ui.ToneInfo
	case BadgeImported:
		return ui.ToneMuted
	}
	return ui.ToneText
}

func badgeIcon(b string) string {
	switch b {
	case BadgePublished:
		return "circle-check"
	case BadgeScript:
		return "script"
	case BadgeWebtoon:
		return "arrows-vertical"
	case BadgeImported:
		return "file-import"
	}
	return "pencil"
}

// RelTime formats a time as "2 h ago", "yesterday", "3 days ago", "last week" or a date.
func RelTime(ctx context.Context, t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return i18n.T(ctx, "studio.time.now")
	case d < time.Hour:
		return i18n.T(ctx, "studio.time.minutes", "count", int(d.Minutes()))
	case d < 24*time.Hour:
		return i18n.T(ctx, "studio.time.hours", "count", int(d.Hours()))
	case d < 48*time.Hour:
		return i18n.T(ctx, "studio.time.yesterday")
	case d < 7*24*time.Hour:
		return i18n.T(ctx, "studio.time.days", "count", int(d.Hours()/24))
	case d < 14*24*time.Hour:
		return i18n.T(ctx, "studio.time.last_week")
	}
	return t.Format("2006-01-02")
}

// DashComputeView is the compute card: GPU, queue, this week, remote spend.
type DashComputeView struct {
	dashboard.ComputeView
	Week string
}

// DashData is the B1 dashboard.
type DashData struct {
	dashboard.Data
	Cards    []ProjectCard
	Continue *ContinueView
	Compute  DashComputeView
}

// ContinueView is the "continue where you left off" card.
type ContinueView struct {
	dashboard.Project
	ThumbURL   string
	MobileLine string
	OpenPage   string
	// Chapter is the chapter number; Left is how many of its pages have no art yet.
	Chapter int
	Left    int
}

func (m *Module) dashboard(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	usr := core.UserFrom(ctx)
	prefs := core.PrefsFrom(ctx)
	d := DashData{}
	d.Greeting = web.Greeting(ctx, usr.Name(), usr.Timezone, time.Now())
	d.ShowXP = prefs.ShowRanksXP
	d.Progress = dashboard.ProgressFor(usr.XP, usr.Level)
	d.CanCreate = usr.CanCreate()
	list, err := m.svc.ListProjects(ctx, usr.ID, ProjectFilter{Limit: 4})
	if err != nil {
		return err
	}
	for i := range list {
		d.Cards = append(d.Cards, m.cardProject(ctx, &list[i], usr.ID))
	}
	d.Continue = m.continueCard(ctx, usr.ID)
	if src, ok := core.Use[dashboard.Source](m.d); ok {
		// Another module feeds the project list (e.g. a hosted catalogue).
		if ps, err := src.RecentProjects(ctx, usr.ID, 4); err == nil && len(ps) > 0 {
			d.Cards = d.Cards[:0]
			for _, p := range ps {
				d.Cards = append(d.Cards, ProjectCard{Project: p, Mobile: p.MobileMeta})
			}
			d.Continue = &ContinueView{Project: ps[0], MobileLine: ps[0].ChapterLine, OpenPage: ps[0].EditorLabel}
		}
	}
	d.Compute = m.dashCompute(ctx, usr.ID)
	switch {
	case len(d.Cards) == 0:
		d.Sub = i18n.T(ctx, "dash.sub.empty")
	case d.Continue != nil && d.Continue.Left > 0:
		d.Sub = i18n.T(ctx, "studio.continue.sub", "n", d.Continue.Chapter, "title", d.Continue.Title, "count", d.Continue.Left)
	default:
		d.Sub = i18n.T(ctx, "dash.sub.continue", "title", d.Cards[0].Title)
	}
	return m.ui.Page(w, r, http.StatusOK, web.PageOpts{Title: i18n.T(ctx, "dash.title"), MobileTitle: i18n.T(ctx, "nav.studio"),
		Section: layout.SectionStudio, Nav: layout.NavHome, Tab: layout.TabHome}, DashboardPage(d))
}

// continueCard finds the most recently edited page (or chapter) of the user's projects.
func (m *Module) continueCard(ctx context.Context, userID string) *ContinueView {
	var row struct {
		ProjectID string `db:"project_id"`
		Title     string `db:"title"`
		Format    string `db:"format"`
		ChapterID string `db:"chapter_id"`
		Number    int    `db:"number"`
		ChTitle   string `db:"ch_title"`
		Page      int    `db:"page"`
		ThumbKey  string `db:"thumb_key"`
	}
	err := m.d.DB.Get(ctx, &row, `SELECT p.id AS project_id, p.title, p.format, c.id AS chapter_id, c.number, c.title AS ch_title,
		pg.number AS page, COALESCE(a.key, '') AS thumb_key
		FROM pages pg JOIN chapters c ON c.id = pg.chapter_id JOIN projects p ON p.id = pg.project_id
		LEFT JOIN project_members m ON m.project_id = p.id AND m.user_id = ?
		LEFT JOIN assets a ON a.id = pg.thumb_asset_id
		WHERE (p.owner_id = ? OR m.user_id IS NOT NULL) AND p.status <> 'archived'
		ORDER BY pg.updated_at DESC LIMIT 1`, userID, userID)
	if err != nil {
		// No pages yet: continue the latest chapter's script.
		err = m.d.DB.Get(ctx, &row, `SELECT p.id AS project_id, p.title, p.format, c.id AS chapter_id, c.number, c.title AS ch_title,
			0 AS page, '' AS thumb_key FROM chapters c JOIN projects p ON p.id = c.project_id
			LEFT JOIN project_members m ON m.project_id = p.id AND m.user_id = ?
			WHERE (p.owner_id = ? OR m.user_id IS NOT NULL) AND p.status <> 'archived'
			ORDER BY c.updated_at DESC LIMIT 1`, userID, userID)
		if err != nil {
			return nil
		}
	}
	var counts struct {
		Total int `db:"total"`
		Done  int `db:"done"`
	}
	_ = m.d.DB.Get(ctx, &counts, `SELECT COUNT(*) AS total, COALESCE(SUM(CASE WHEN EXISTS (SELECT 1 FROM panels pn JOIN layers l ON l.panel_id = pn.id
		WHERE pn.page_id = pg.id AND l.asset_id IS NOT NULL) OR pg.status = 'done' THEN 1 ELSE 0 END), 0) AS done FROM pages pg WHERE pg.chapter_id = ?`, row.ChapterID)
	cv := &ContinueView{ThumbURL: m.svc.MediaURL(row.ThumbKey), Chapter: row.Number, Left: counts.Total - counts.Done}
	cv.ID, cv.Title = row.ProjectID, row.Title
	cv.Href = "/studio/p/" + row.ProjectID + "/story/" + itoa(row.Number)
	chTitle := row.ChTitle
	if chTitle == "" {
		chTitle = i18n.T(ctx, "studio.story.chapter_n", "n", row.Number)
	}
	parts := []string{i18n.T(ctx, "studio.ch_short", "n", row.Number), chTitle}
	if counts.Total > 0 {
		parts = append(parts, i18n.T(ctx, "studio.continue.pages", "done", counts.Done, "total", counts.Total))
		cv.Progress = float64(counts.Done) / float64(counts.Total)
		cv.MobileLine = i18n.T(ctx, "studio.continue.mobile", "n", row.Number, "done", counts.Done, "total", counts.Total)
	} else {
		parts = append(parts, i18n.T(ctx, "studio.continue.script_only"))
		cv.MobileLine = i18n.T(ctx, "studio.ch_short", "n", row.Number) + " · " + chTitle
	}
	if names := m.editingNow(ctx, row.ChapterID, userID); names != "" {
		parts = append(parts, i18n.T(ctx, "studio.continue.editing", "names", names))
	}
	cv.ChapterLine = strings.Join(parts, " · ")
	if row.Page > 0 {
		cv.PageLabel = i18n.T(ctx, "studio.continue.page_label", "ch", row.Number, "page", row.Page)
		cv.EditorHref = "/studio/p/" + row.ProjectID + "/chapters/" + itoa(row.Number) + "/edit?page=" + itoa(row.Page)
		cv.EditorLabel = i18n.T(ctx, "studio.continue.open_editor")
		cv.OpenPage = i18n.T(ctx, "studio.continue.open_page", "n", row.Page)
	} else {
		cv.PageLabel = i18n.T(ctx, "studio.ch_short", "n", row.Number)
		cv.EditorHref = cv.Href
		cv.EditorLabel = i18n.T(ctx, "studio.continue.open_script")
		cv.OpenPage = cv.EditorLabel
	}
	if n := m.svc.OpenContinuityIssues(ctx, row.ChapterID); n > 0 {
		cv.IssuesHref = "/studio/p/" + row.ProjectID + "/chapters/" + itoa(row.Number) + "/continuity"
		cv.IssuesLabel = i18n.T(ctx, "studio.continue.issues", "count", n)
	}
	return cv
}

// editingNow names the other members who have one of the chapter's pages open right now.
func (m *Module) editingNow(ctx context.Context, chapterID, userID string) string {
	if m.d.Hub == nil {
		return ""
	}
	var pages []string
	_ = m.d.DB.Select(ctx, &pages, `SELECT id FROM pages WHERE chapter_id = ?`, chapterID)
	seen := map[string]bool{}
	var names []string
	for _, id := range pages {
		for _, p := range m.d.Hub.Presence("doc:page:" + id) {
			if p.UserID == "" || seen[p.UserID] {
				continue
			}
			seen[p.UserID] = true
			n := p.DisplayName
			if n == "" {
				n = p.Username
			}
			if f := strings.Fields(n); len(f) > 0 {
				n = f[0]
			}
			if p.UserID == userID {
				n = i18n.T(ctx, "studio.continue.you")
			}
			names = append(names, n)
		}
	}
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " " + i18n.T(ctx, "studio.and") + " " + names[len(names)-1]
}

// dashCompute fills the compute card.
func (m *Module) dashCompute(ctx context.Context, userID string) DashComputeView {
	c, _ := web.ComputeSummary(ctx, m.d.Jobs)
	inst := m.d.Settings.MustInstance(ctx)
	v := DashComputeView{}
	v.Name, v.Online, v.QueueURL = c.WorkerName, c.WorkersOnline > 0, "/queue"
	if v.Name == "" {
		v.Name = inst.Name
	}
	switch {
	case c.GPU != "":
		v.GPU = i18n.T(ctx, "dash.compute.gpu_value", "gpu", c.GPU, "used", web.GB(c.VRAMUsedMB), "total", web.GB(c.VRAMTotalMB))
	case c.WorkersOnline > 0:
		v.GPU = i18n.T(ctx, "dash.compute.gpu_cpu")
	default:
		v.GPU = i18n.T(ctx, "dash.compute.gpu_none")
	}
	n, secs := m.queueEstimate(ctx)
	if n == 0 {
		v.Queue = i18n.T(ctx, "dash.compute.queue_empty")
	} else {
		v.Queue = i18n.T(ctx, "studio.compute.queue", "count", n, "eta", FormatDuration(ctx, secs))
	}
	panels, gpu := m.computeWeek(ctx, userID)
	v.Week = i18n.T(ctx, "studio.compute.week", "count", panels, "gpu", FormatDuration(ctx, gpu))
	usd, prov := m.computeSpend(ctx, userID)
	switch {
	case inst.LocalOnly:
		v.Remote = i18n.T(ctx, "dash.compute.remote_local_only")
	case usd > 0 && prov != "":
		v.Remote = i18n.T(ctx, "studio.compute.spend_on", "usd", FormatUSD(usd), "provider", prov)
	case usd > 0:
		v.Remote = i18n.T(ctx, "studio.compute.spend", "usd", FormatUSD(usd))
	default:
		v.Remote = i18n.T(ctx, "dash.compute.remote_none")
	}
	state := i18n.T(ctx, "workers.status.offline")
	if v.Online {
		state = i18n.T(ctx, "workers.status.online")
	}
	v.Line = i18n.T(ctx, "dash.compute.line", "name", v.Name, "state", strings.ToLower(state), "queue", v.Queue)
	return v
}
