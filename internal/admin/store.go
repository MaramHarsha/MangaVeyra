package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/admin/adminapi"
	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/social"
)

// The admin panel reads moderation metadata (titles, chapter numbers, AI
// labels, owners) of reported content straight from the publish tables, and
// the reported comment through social.Moderation. It never reads pages,
// scripts or anything else inside a project.

// Report statuses (reports.status).
const (
	statusOpen      = "open"
	statusActioned  = "actioned"
	statusDismissed = "dismissed"
)

// reasonAILabel marks an AI-label dispute.
const reasonAILabel = "ai_label"

// reportRow is one report with its reporter.
type reportRow struct {
	ID               string         `db:"id"`
	ReporterID       sql.NullString `db:"reporter_id"`
	TargetKind       string         `db:"target_kind"`
	TargetID         string         `db:"target_id"`
	Reason           string         `db:"reason"`
	Details          string         `db:"details"`
	Status           string         `db:"status"`
	Resolution       string         `db:"resolution"`
	CreatedAt        time.Time      `db:"created_at"`
	ResolvedAt       sql.NullTime   `db:"resolved_at"`
	ReporterUsername sql.NullString `db:"reporter_username"`
	ReporterName     sql.NullString `db:"reporter_name"`
	ReporterRole     sql.NullString `db:"reporter_role"`
}

const reportCols = `r.id, r.reporter_id, r.target_kind, r.target_id, r.reason, r.details, r.status, r.resolution,
	r.created_at, r.resolved_at, u.username AS reporter_username, u.display_name AS reporter_name, u.role AS reporter_role`

const reportFrom = ` FROM reports r LEFT JOIN users u ON u.id = r.reporter_id`

// maxReportsShown bounds one page of a report queue.
const maxReportsShown = 50

// openReports lists open reports, newest first; disputes limits them to AI-label disputes.
func openReports(ctx context.Context, q db.Querier, disputes bool) ([]reportRow, error) {
	where := "r.status = ?"
	args := []any{statusOpen}
	if disputes {
		where += " AND r.reason = ?"
		args = append(args, reasonAILabel)
	}
	var rows []reportRow
	if err := q.Select(ctx, &rows, `SELECT `+reportCols+reportFrom+` WHERE `+where+
		` ORDER BY r.created_at DESC, r.id DESC LIMIT ?`, append(args, maxReportsShown)...); err != nil {
		return nil, fmt.Errorf("admin: list reports: %w", err)
	}
	return rows, nil
}

func reportByID(ctx context.Context, q db.Querier, id string) (reportRow, error) {
	var row reportRow
	if !db.ValidID(id) {
		return row, errNotFound
	}
	if err := q.Get(ctx, &row, `SELECT `+reportCols+reportFrom+` WHERE r.id = ?`, id); err != nil {
		if db.IsNotFound(err) {
			return row, errNotFound
		}
		return row, fmt.Errorf("admin: get report: %w", err)
	}
	return row, nil
}

var errNotFound = errors.New("admin: not found")

// counts are the G5 header and stat card numbers.
type counts struct {
	OpenReports  int
	Disputes     int
	Members      int
	StorageBytes int64
	StorageKnown bool
}

func loadCounts(ctx context.Context, q db.Querier) (counts, error) {
	var c counts
	if err := q.Get(ctx, &c.OpenReports, `SELECT COUNT(*) FROM reports WHERE status = ?`, statusOpen); err != nil {
		return c, fmt.Errorf("admin: count reports: %w", err)
	}
	if err := q.Get(ctx, &c.Disputes, `SELECT COUNT(*) FROM reports WHERE status = ? AND reason = ?`, statusOpen, reasonAILabel); err != nil {
		return c, fmt.Errorf("admin: count disputes: %w", err)
	}
	if err := q.Get(ctx, &c.Members, `SELECT COUNT(*) FROM users WHERE status = ?`, auth.StatusActive); err != nil {
		return c, fmt.Errorf("admin: count members: %w", err)
	}
	if err := q.Get(ctx, &c.StorageBytes, `SELECT CAST(COALESCE(SUM(bytes), 0) AS BIGINT) FROM assets`); err == nil {
		c.StorageKnown = true
	}
	return c, nil
}

// ---- report targets --------------------------------------------------------

// target is what an admin sees of a reported thing.
type target struct {
	Kind  string // series | chapter | comment | user (others are shown as they are)
	Found bool

	SeriesID, SeriesSlug, SeriesTitle string
	ProjectID, OwnerID                string
	// ChapterID is the Studio chapter of a chapter (or chapter comment) target.
	ChapterID     string
	PublicationID string
	Number        float64
	Lang          string
	ChapterTitle  string
	// Labels are the per-role AI labels (script, art, lettering) of the
	// chapter, or of the series for series targets.
	Labels map[string]string

	Comment *social.ReportedComment

	// Account is the account a suspension applies to: the comment's author,
	// the series owner or the reported user.
	Account *auth.User

	SeriesTakenDown, ChapterTakenDown bool
}

type seriesMeta struct {
	ID        string `db:"id"`
	Slug      string `db:"slug"`
	Title     string `db:"title"`
	ProjectID string `db:"project_id"`
	OwnerID   string `db:"owner_id"`
	AILabel   string `db:"ai_label"`
}

type publicationMeta struct {
	ID        string  `db:"id"`
	SeriesID  string  `db:"series_id"`
	ChapterID string  `db:"chapter_id"`
	Number    float64 `db:"number"`
	Lang      string  `db:"lang"`
	Title     string  `db:"title"`
	AILabel   string  `db:"ai_label"`
}

func decodeLabels(raw string) map[string]string {
	out := map[string]string{}
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}

// loadTarget resolves a report's target for the review panel.
func (m *Module) loadTarget(ctx context.Context, kind, id string) (target, error) {
	t := target{Kind: kind}
	q := m.d.DB
	switch kind {
	case "series":
		if err := m.fillSeries(ctx, q, &t, id); err != nil {
			return t, err
		}
		t.Labels = map[string]string{}
	case "chapter":
		if err := m.fillPublication(ctx, q, &t, id); err != nil {
			return t, err
		}
	case "comment":
		if m.mod == nil {
			return t, nil
		}
		c, err := m.mod.ReportedComment(ctx, id)
		if errors.Is(err, social.ErrNotFound) {
			return t, nil
		}
		if err != nil {
			return t, err
		}
		t.Comment = &c
		if c.TargetKind == "chapter" || c.TargetKind == "panel" {
			if err := m.fillPublication(ctx, q, &t, c.TargetID); err != nil {
				return t, err
			}
		} else if err := m.fillSeries(ctx, q, &t, c.SeriesID); err != nil {
			return t, err
		}
		t.Found = true
		t.Account = m.userOrNil(ctx, c.AuthorID)
		return t, nil
	case "user":
		t.Account = m.userOrNil(ctx, id)
		t.Found = t.Account != nil
		return t, nil
	default:
		return t, nil
	}
	if t.Found {
		t.Account = m.userOrNil(ctx, t.OwnerID)
	}
	return t, nil
}

func (m *Module) fillSeries(ctx context.Context, q db.Querier, t *target, id string) error {
	var s seriesMeta
	if err := q.Get(ctx, &s, `SELECT id, slug, title, project_id, owner_id, ai_label FROM series WHERE id = ?`, id); err != nil {
		if db.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("admin: report series: %w", err)
	}
	t.Found = true
	t.SeriesID, t.SeriesSlug, t.SeriesTitle, t.ProjectID, t.OwnerID = s.ID, s.Slug, s.Title, s.ProjectID, s.OwnerID
	t.Labels = decodeLabels(s.AILabel)
	return m.fillTakedowns(ctx, q, t)
}

func (m *Module) fillPublication(ctx context.Context, q db.Querier, t *target, id string) error {
	var p publicationMeta
	if err := q.Get(ctx, &p, `SELECT id, series_id, chapter_id, number, lang, title, ai_label FROM chapter_publications WHERE id = ?`, id); err != nil {
		if db.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("admin: report chapter: %w", err)
	}
	if err := m.fillSeries(ctx, q, t, p.SeriesID); err != nil || !t.Found {
		return err
	}
	t.PublicationID, t.ChapterID, t.Number, t.Lang, t.ChapterTitle = p.ID, p.ChapterID, p.Number, p.Lang, p.Title
	t.Labels = decodeLabels(p.AILabel)
	return m.fillTakedowns(ctx, q, t)
}

func (m *Module) fillTakedowns(ctx context.Context, q db.Querier, t *target) error {
	keys := []any{adminapi.TakedownKey(adminapi.TakedownSeries, t.SeriesID)}
	if t.ChapterID != "" {
		keys = append(keys, adminapi.TakedownKey(adminapi.TakedownChapter, t.ChapterID))
	}
	var found []string
	qs, args, err := db.In(`SELECT key FROM instance_settings WHERE key IN (?)`, keys)
	if err != nil {
		return err
	}
	if err := q.Select(ctx, &found, qs, args...); err != nil {
		return fmt.Errorf("admin: takedown state: %w", err)
	}
	for _, k := range found {
		switch {
		case strings.HasPrefix(k, adminapi.SeriesTakedownPrefix):
			t.SeriesTakenDown = true
		case strings.HasPrefix(k, adminapi.ChapterTakedownPrefix):
			t.ChapterTakenDown = true
		}
	}
	return nil
}

func (m *Module) userOrNil(ctx context.Context, id string) *auth.User {
	if id == "" {
		return nil
	}
	u, err := m.d.Auth.UserByID(ctx, id)
	if err != nil {
		return nil
	}
	return u
}

// ---- takedowns -------------------------------------------------------------

// takedownRow is one active takedown with the titles it hides.
type takedownRow struct {
	Key  string
	Data adminapi.Takedown
	// SeriesTitle and Number describe the target (Number only for chapters).
	SeriesTitle string
	Number      float64
	HasNumber   bool
	At          time.Time
}

func listTakedowns(ctx context.Context, q db.Querier) ([]takedownRow, error) {
	var rows []struct {
		Key   string `db:"key"`
		Value string `db:"value"`
	}
	if err := q.Select(ctx, &rows, `SELECT key, value FROM instance_settings WHERE key LIKE ? ORDER BY updated_at DESC, key`,
		adminapi.TakedownPrefix+"%"); err != nil {
		return nil, fmt.Errorf("admin: list takedowns: %w", err)
	}
	out := make([]takedownRow, 0, len(rows))
	for _, r := range rows {
		t := takedownRow{Key: r.Key}
		if json.Unmarshal([]byte(r.Value), &t.Data) != nil || t.Data.SeriesID == "" {
			continue
		}
		t.At, _ = time.Parse(time.RFC3339Nano, t.Data.At)
		if err := q.Get(ctx, &t.SeriesTitle, `SELECT title FROM series WHERE id = ?`, t.Data.SeriesID); err != nil && !db.IsNotFound(err) {
			return nil, fmt.Errorf("admin: takedown series: %w", err)
		}
		if t.Data.Kind == adminapi.TakedownChapter {
			var nums []float64
			if err := q.Select(ctx, &nums, `SELECT number FROM chapter_publications WHERE chapter_id = ? ORDER BY number LIMIT 1`, t.Data.TargetID); err != nil {
				return nil, fmt.Errorf("admin: takedown chapter: %w", err)
			}
			if len(nums) > 0 {
				t.Number, t.HasNumber = nums[0], true
			}
		}
		out = append(out, t)
	}
	return out, nil
}

// ---- members ---------------------------------------------------------------

// memberRow is one account on the Users tab with what it uses.
type memberRow struct {
	User     *auth.User
	Jobs     int
	Bytes    int64
	Projects int
	LastSeen time.Time
}

// memberExtras fills jobs, storage, projects and last-seen for a page of users.
func memberExtras(ctx context.Context, q db.Querier, users []*auth.User) ([]memberRow, error) {
	out := make([]memberRow, len(users))
	if len(users) == 0 {
		return out, nil
	}
	ids := make([]string, len(users))
	index := map[string]int{}
	for i, u := range users {
		ids[i], index[u.ID] = u.ID, i
		out[i] = memberRow{User: u}
		if u.LastLoginAt != nil {
			out[i].LastSeen = *u.LastLoginAt
		}
	}
	type kv struct {
		ID string `db:"id"`
		N  int64  `db:"n"`
	}
	sum := func(query string, apply func(i int, n int64)) error {
		qs, args, err := db.In(query, ids)
		if err != nil {
			return err
		}
		var rows []kv
		if err := q.Select(ctx, &rows, qs, args...); err != nil {
			return err
		}
		for _, r := range rows {
			if i, ok := index[r.ID]; ok {
				apply(i, r.N)
			}
		}
		return nil
	}
	if err := sum(`SELECT owner_id AS id, CAST(COUNT(*) AS BIGINT) AS n FROM projects WHERE owner_id IN (?) GROUP BY owner_id`,
		func(i int, n int64) { out[i].Projects = int(n) }); err != nil {
		return nil, fmt.Errorf("admin: member projects: %w", err)
	}
	if err := sum(`SELECT owner_id AS id, CAST(COALESCE(SUM(bytes), 0) AS BIGINT) AS n FROM assets WHERE owner_id IN (?) GROUP BY owner_id`,
		func(i int, n int64) { out[i].Bytes = n }); err != nil {
		return nil, fmt.Errorf("admin: member storage: %w", err)
	}
	if err := sum(`SELECT user_id AS id, CAST(COUNT(*) AS BIGINT) AS n FROM jobs WHERE user_id IN (?) AND state IN ('queued', 'running') GROUP BY user_id`,
		func(i int, n int64) { out[i].Jobs = int(n) }); err != nil {
		return nil, fmt.Errorf("admin: member jobs: %w", err)
	}
	qs, args, err := db.In(`SELECT user_id, last_seen_at FROM sessions WHERE user_id IN (?)`, ids)
	if err != nil {
		return nil, err
	}
	var seen []struct {
		UserID string    `db:"user_id"`
		At     time.Time `db:"last_seen_at"`
	}
	if err := q.Select(ctx, &seen, qs, args...); err != nil {
		return nil, fmt.Errorf("admin: member sessions: %w", err)
	}
	for _, s := range seen {
		if i, ok := index[s.UserID]; ok && s.At.After(out[i].LastSeen) {
			out[i].LastSeen = s.At
		}
	}
	return out, nil
}
