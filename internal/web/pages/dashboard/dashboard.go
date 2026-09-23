// Package dashboard is the studio home (B1 – Dashboard) built from small
// components so the studio module can swap in real project data: it only has
// to provide a Source (core.Provide[dashboard.Source]) — or register its own
// GET /studio and reuse these components.
package dashboard

import (
	"context"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/web/ui"
)

// Project is one project as the dashboard shows it.
type Project struct {
	ID         string
	Title      string
	Href       string
	Meta       string
	ThumbLabel string
	Badges     []ui.BadgeSpec
	// Continue data for the most recent project.
	ChapterLine string
	Progress    float64
	EditorHref  string
	EditorLabel string
	IssuesHref  string
	IssuesLabel string
	PageLabel   string
	MobileMeta  string
	UpdatedAt   time.Time
}

// Source lists a member's recent projects. The studio module provides it;
// without one the dashboard shows the "No projects yet" empty state.
type Source interface {
	RecentProjects(ctx context.Context, userID string, limit int) ([]Project, error)
}

// Progress is a member's level progress (XP comes from users.xp).
type Progress struct {
	Level int
	XP    int64
	// From / To are the XP totals where the current level starts and the next begins.
	From, To int64
	Rank     string
	TitleKey string
}

// Fraction returns the progress to the next level (0–1).
func (p Progress) Fraction() float64 {
	if p.To <= p.From {
		return 1
	}
	f := float64(p.XP-p.From) / float64(p.To-p.From)
	if f < 0 {
		return 0
	}
	if f > 1 {
		return 1
	}
	return f
}

// LevelStart is the total XP needed to reach level n (n ≥ 1): 50·n·(n−1).
// It is the default curve until the xp module provides its own.
func LevelStart(n int) int64 {
	if n <= 1 {
		return 0
	}
	return 50 * int64(n) * int64(n-1)
}

// LevelFor returns the level for a total XP on the default curve.
func LevelFor(xp int64) int {
	n := 1
	for LevelStart(n+1) <= xp && n < 999 {
		n++
	}
	return n
}

// RankFor maps a level to the E–S rank tiers (ranks never come from money).
func RankFor(level int) string {
	switch {
	case level >= 45:
		return "S"
	case level >= 30:
		return "A"
	case level >= 20:
		return "B"
	case level >= 10:
		return "C"
	case level >= 5:
		return "D"
	}
	return "E"
}

// TitleKeyFor returns the i18n key of the level title ("xp.title.storyteller").
func TitleKeyFor(level int) string {
	switch {
	case level >= 50:
		return "xp.title.legend"
	case level >= 40:
		return "xp.title.veteran"
	case level >= 30:
		return "xp.title.mangaka"
	case level >= 20:
		return "xp.title.storyteller"
	case level >= 10:
		return "xp.title.inker"
	case level >= 5:
		return "xp.title.apprentice"
	}
	return "xp.title.novice"
}

// ProgressFor computes the level progress from total XP; the stored level is
// used when it is higher (levels granted by the xp module stick).
func ProgressFor(xp int64, storedLevel int) Progress {
	lvl := LevelFor(xp)
	if storedLevel > lvl {
		lvl = storedLevel
	}
	return Progress{Level: lvl, XP: xp, From: LevelStart(lvl), To: LevelStart(lvl + 1), Rank: RankFor(lvl), TitleKey: TitleKeyFor(lvl)}
}

// ComputeView is the compute card / compute line content.
type ComputeView struct {
	Name     string
	Online   bool
	GPU      string
	Queue    string
	Workers  string
	Remote   string
	Line     string
	QueueURL string
}

// Data is everything the dashboard renders.
type Data struct {
	Greeting  string
	Sub       string
	ShowXP    bool
	Progress  Progress
	CanCreate bool
	Projects  []Project
	Compute   ComputeView
}

// Idea is a "start from" suggestion in the bottom row.
type Idea struct {
	Href, Icon, TitleKey, DescKey string
}

// Ideas are the template, sketch and translate starting points.
var Ideas = []Idea{
	{"/studio/templates", "layout-grid", "dash.idea.template.title", "dash.idea.template.desc"},
	{"/studio/new?from=sketch", "scribble", "dash.idea.sketch.title", "dash.idea.sketch.desc"},
	{"/studio/projects?action=translate", "language", "dash.idea.translate.title", "dash.idea.translate.desc"},
}
