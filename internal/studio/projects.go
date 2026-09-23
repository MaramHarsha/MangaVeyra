package studio

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/notify"
)

// PageSize is a printable page size with its pixel canvas.
type PageSize struct {
	Key    string
	Label  string // "B5 · 182 × 257 mm"
	Width  int
	Height int
}

// PageSizes are the page sizes offered in the wizard and project settings.
var PageSizes = []PageSize{
	{"b5", "B5 · 182 × 257 mm", 1600, 2260},
	{"a5", "A5 · 148 × 210 mm", 1600, 2270},
	{"tankobon", "Tankōbon · 128 × 182 mm", 1600, 2275},
	{"a4", "A4 · 210 × 297 mm", 1600, 2263},
	{"us_comic", "US comic · 6.625 × 10.25 in", 1600, 2475},
	{"webtoon", "Webtoon strip · 800 px wide", 800, 1280},
}

// PageSizeFor returns the page size for key (B5 when unknown).
func PageSizeFor(key string) PageSize {
	for _, p := range PageSizes {
		if p.Key == key {
			return p
		}
	}
	return PageSizes[0]
}

// Styles are the art styles a project can use (keys of the style.* catalog).
var Styles = []string{"seinen_bw", "shonen_ink", "shojo_soft", "webtoon_color", "chibi", "sketch"}

// Formats lists the reading formats.
var Formats = []string{FormatMangaRTL, FormatComicLTR, FormatWebtoon}

// Genres are the genres offered in the wizard (keys of the genre.* catalog).
var Genres = []string{"action", "fantasy", "mystery", "romance", "horror", "scifi", "slice_of_life", "comedy", "sports",
	"historical", "isekai", "thriller", "drama", "mecha", "supernatural"}

// Routing modes a project may pin ("" follows the server default).
var RoutingModes = []string{"", "balanced", "best", "fastest", "local_only", "free_only"}

// NewProject describes a project to create.
type NewProject struct {
	Title           string
	Premise         string
	Logline         string
	Genres          []string
	Style           string
	Format          string
	PageSize        string
	Language        string
	Visibility      string
	ChaptersPlanned int
	PagesPerChapter string
	TemplateID      string
	Source          string
}

func (in *NewProject) normalize() map[string]string {
	errs := map[string]string{}
	in.Title = clip(in.Title, 120)
	in.Premise = clip(in.Premise, 2000)
	if in.Title == "" {
		errs["title"] = "required"
	}
	if in.Format == "" {
		in.Format = FormatMangaRTL
	}
	if !slices.Contains(Formats, in.Format) {
		errs["format"] = "invalid"
	}
	if in.Style == "" {
		in.Style = "seinen_bw"
	}
	if !slices.Contains(Styles, in.Style) {
		errs["style"] = "invalid"
	}
	if in.PageSize == "" {
		in.PageSize = "b5"
		if in.Format == FormatWebtoon {
			in.PageSize = "webtoon"
		}
	}
	if in.Visibility == "" {
		in.Visibility = VisibilityPrivate
	}
	if in.Visibility != VisibilityPrivate && in.Visibility != VisibilityPublic {
		errs["visibility"] = "invalid"
	}
	if in.Language == "" {
		in.Language = "en"
	}
	if in.Source == "" {
		in.Source = SourceStudio
	}
	var genres []string
	for _, g := range in.Genres {
		if slices.Contains(Genres, g) && !slices.Contains(genres, g) {
			genres = append(genres, g)
		}
	}
	in.Genres = genres
	if in.ChaptersPlanned < 0 || in.ChaptersPlanned > 500 {
		errs["chapters"] = "invalid"
	}
	in.PagesPerChapter = clip(in.PagesPerChapter, 20)
	return errs
}

// CreateProject creates a project owned by ownerID (who becomes its owner member).
func (s *Service) CreateProject(ctx context.Context, ownerID string, in NewProject) (*Project, error) {
	if errs := in.normalize(); len(errs) > 0 {
		return nil, core.ErrValidation(errs)
	}
	size := PageSizeFor(in.PageSize)
	id, now := db.NewID(), s.now()
	base := Slugify(in.Title)
	err := s.db.WithTx(ctx, func(tx *db.Tx) error {
		slug := base
		for i := 2; ; i++ {
			var n int
			if err := tx.Get(ctx, &n, `SELECT COUNT(*) FROM projects WHERE owner_id = ? AND slug = ?`, ownerID, slug); err != nil {
				return err
			}
			if n == 0 {
				break
			}
			slug = fmt.Sprintf("%s-%d", base, i)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO projects (id, owner_id, title, slug, premise, logline, genres, style, format, page_size,
			page_width, page_height, language, status, template_id, visibility, source, chapters_planned, pages_per_chapter, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'draft', ?, ?, ?, ?, ?, ?, ?)`,
			id, ownerID, in.Title, slug, in.Premise, clip(in.Logline, 300), db.MustJSON(jsonList(in.Genres)), in.Style, in.Format, size.Key,
			size.Width, size.Height, in.Language, db.NullString(in.TemplateID), in.Visibility, in.Source, in.ChaptersPlanned,
			in.PagesPerChapter, now, now); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO project_members (project_id, user_id, role, scope, created_at) VALUES (?, ?, 'owner', '{}', ?)`,
			id, ownerID, now)
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.Project(ctx, id)
}

// Project returns a project by id.
func (s *Service) Project(ctx context.Context, id string) (*Project, error) {
	var p Project
	if err := s.db.Get(ctx, &p, `SELECT `+projectCols+` FROM projects p WHERE p.id = ?`, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, core.ErrNotFound()
		}
		return nil, err
	}
	return &p, nil
}

// ProjectPatch is a partial update of project settings (nil = unchanged).
type ProjectPatch struct {
	Title           *string
	Premise         *string
	Logline         *string
	Genres          []string
	Style           *string
	Format          *string
	PageSize        *string
	Language        *string
	Visibility      *string
	RoutingMode     *string
	ChaptersPlanned *int
	PagesPerChapter *string
	CoverAssetID    *string
}

// UpdateProject applies a patch after checking EditProject rights.
func (s *Service) UpdateProject(ctx context.Context, a *Access, pt ProjectPatch) (*Project, error) {
	if !a.Can(EditProject) {
		return nil, core.ErrForbidden(i18n.T(ctx, "studio.error.role"))
	}
	p := *a.Project
	errs := map[string]string{}
	if pt.Title != nil {
		if p.Title = clip(*pt.Title, 120); p.Title == "" {
			errs["title"] = i18n.T(ctx, "studio.form.required")
		}
	}
	if pt.Premise != nil {
		p.Premise = clip(*pt.Premise, 2000)
	}
	if pt.Logline != nil {
		p.Logline = clip(*pt.Logline, 300)
	}
	if pt.Genres != nil {
		var g []string
		for _, v := range pt.Genres {
			if slices.Contains(Genres, v) && !slices.Contains(g, v) {
				g = append(g, v)
			}
		}
		p.Genres.V = g
	}
	if pt.Style != nil {
		if !slices.Contains(Styles, *pt.Style) {
			errs["style"] = i18n.T(ctx, "studio.form.invalid")
		}
		p.Style = *pt.Style
	}
	if pt.Format != nil {
		if !slices.Contains(Formats, *pt.Format) {
			errs["format"] = i18n.T(ctx, "studio.form.invalid")
		}
		p.Format = *pt.Format
	}
	if pt.PageSize != nil {
		size := PageSizeFor(*pt.PageSize)
		p.PageSize, p.PageWidth, p.PageHeight = size.Key, size.Width, size.Height
	}
	if pt.Language != nil && *pt.Language != "" {
		p.Language = clip(*pt.Language, 16)
	}
	if pt.Visibility != nil {
		if *pt.Visibility != VisibilityPrivate && *pt.Visibility != VisibilityPublic {
			errs["visibility"] = i18n.T(ctx, "studio.form.invalid")
		}
		p.Visibility = *pt.Visibility
	}
	if pt.RoutingMode != nil {
		if !slices.Contains(RoutingModes, *pt.RoutingMode) {
			errs["routing_mode"] = i18n.T(ctx, "studio.form.invalid")
		}
		p.RoutingMode = *pt.RoutingMode
	}
	if pt.ChaptersPlanned != nil {
		if *pt.ChaptersPlanned < 0 || *pt.ChaptersPlanned > 500 {
			errs["chapters_planned"] = i18n.T(ctx, "studio.form.invalid")
		}
		p.ChaptersPlanned = *pt.ChaptersPlanned
	}
	if pt.PagesPerChapter != nil {
		p.PagesPerChapter = clip(*pt.PagesPerChapter, 20)
	}
	if pt.CoverAssetID != nil {
		p.CoverAssetID = *pt.CoverAssetID
	}
	if len(errs) > 0 {
		return nil, core.ErrValidation(errs)
	}
	_, err := s.db.Exec(ctx, `UPDATE projects SET title = ?, premise = ?, logline = ?, genres = ?, style = ?, format = ?, page_size = ?,
		page_width = ?, page_height = ?, language = ?, visibility = ?, routing_mode = ?, chapters_planned = ?, pages_per_chapter = ?,
		cover_asset_id = ?, updated_at = ? WHERE id = ?`,
		p.Title, p.Premise, p.Logline, db.MustJSON(jsonList(p.Genres.V)), p.Style, p.Format, p.PageSize, p.PageWidth, p.PageHeight,
		p.Language, p.Visibility, p.RoutingMode, p.ChaptersPlanned, p.PagesPerChapter, db.NullString(p.CoverAssetID), s.now(), p.ID)
	if err != nil {
		return nil, err
	}
	s.Publish(ctx, p.ID, "studio.project.updated", Event{Kind: "project", ID: p.ID})
	return s.Project(ctx, p.ID)
}

// SetProjectSetting stores one key of projects.settings (JSON), keeping the others.
func (s *Service) SetProjectSetting(ctx context.Context, projectID, key string, v any) error {
	return s.db.WithTx(ctx, func(tx *db.Tx) error {
		var raw db.JSON[map[string]any]
		if err := tx.Get(ctx, &raw, `SELECT settings FROM projects WHERE id = ?`, projectID); err != nil {
			return err
		}
		m := nonNilMap(raw.V)
		if v == nil {
			delete(m, key)
		} else {
			m[key] = v
		}
		_, err := tx.Exec(ctx, `UPDATE projects SET settings = ? WHERE id = ?`, db.MustJSON(m), projectID)
		return err
	})
}

// ArchiveProject archives (or with archived=false restores) a project. Owner only, humans only.
func (s *Service) ArchiveProject(ctx context.Context, a *Access, archived bool) error {
	if a.Actor.IsAgent() || !a.IsOwner() {
		return core.ErrForbidden(i18n.T(ctx, "studio.error.human_owner"))
	}
	status, at := ProjectActive, any(nil)
	if archived {
		status, at = ProjectArchived, s.now()
	}
	_, err := s.db.Exec(ctx, `UPDATE projects SET status = ?, archived_at = ?, updated_at = ? WHERE id = ?`, status, at, s.now(), a.Project.ID)
	if err == nil {
		s.Publish(ctx, a.Project.ID, "studio.project.updated", Event{Kind: "project", ID: a.Project.ID, Summary: status})
	}
	return err
}

// DeleteProject deletes a project and everything in it. Owner only, humans
// only; confirm must equal the project title.
func (s *Service) DeleteProject(ctx context.Context, a *Access, confirm string) error {
	if a.Actor.IsAgent() || !a.IsOwner() {
		return core.ErrForbidden(i18n.T(ctx, "studio.error.human_owner"))
	}
	if strings.TrimSpace(confirm) != a.Project.Title {
		return core.ErrValidation(map[string]string{"confirm": i18n.T(ctx, "studio.settings.delete.mismatch")})
	}
	if _, err := s.db.Exec(ctx, `DELETE FROM projects WHERE id = ?`, a.Project.ID); err != nil {
		return err
	}
	s.Publish(ctx, a.Project.ID, "studio.project.deleted", Event{Kind: "project", ID: a.Project.ID})
	return nil
}

// ---- listing ------------------------------------------------------------------

// ProjectFilter selects projects for ListProjects.
type ProjectFilter struct {
	Query    string
	Badge    string // draft | published | script | webtoon | imported | shared
	Format   string
	Shared   bool   // only projects shared with the user (not owned)
	Archived bool   // include archived projects
	Sort     string // updated (default) | created | title
	Limit    int
}

// ProjectSummary is a project with the counts cards show.
type ProjectSummary struct {
	Project
	Role         string    `db:"role" json:"role"`
	OwnerName    string    `db:"owner_name" json:"owner_name"`
	Chapters     int       `db:"n_chapters" json:"chapters"`
	LastChapter  int       `db:"last_chapter" json:"last_chapter"`
	Pages        int       `db:"n_pages" json:"pages"`
	Panels       int       `db:"n_panels" json:"panels"`
	ScriptPanels int       `db:"n_script" json:"script_panels"`
	Published    int       `db:"n_published" json:"published"`
	ThumbKey     string    `db:"thumb_key" json:"-"`
	Activity     time.Time `db:"-" json:"activity"`
}

// Badge returns the card badge: imported, published, webtoon, script or draft.
func (p *ProjectSummary) Badge() string {
	switch {
	case p.Source == SourceImport:
		return BadgeImported
	case p.Published > 0:
		return BadgePublished
	case p.Format == FormatWebtoon:
		return BadgeWebtoon
	case p.Pages == 0 && p.ScriptPanels > 0:
		return BadgeScript
	}
	return BadgeDraft
}

// ListProjects returns the projects userID owns or is a member of.
func (s *Service) ListProjects(ctx context.Context, userID string, f ProjectFilter) ([]ProjectSummary, error) {
	where := []string{"(p.owner_id = ? OR m.user_id IS NOT NULL)"}
	// The first argument binds the member join, the second the owner check.
	args := []any{userID, userID}
	if !f.Archived {
		where = append(where, "p.status <> 'archived'")
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		where = append(where, "(LOWER(p.title) LIKE ? OR LOWER(p.premise) LIKE ?)")
		like := "%" + strings.ToLower(q) + "%"
		args = append(args, like, like)
	}
	if f.Format != "" {
		where = append(where, "p.format = ?")
		args = append(args, f.Format)
	}
	if f.Shared || f.Badge == "shared" {
		where = append(where, "p.owner_id <> ?")
		args = append(args, userID)
	}
	order := "p.updated_at DESC, p.id DESC"
	switch f.Sort {
	case "created":
		order = "p.created_at DESC, p.id DESC"
	case "title":
		order = "LOWER(p.title) ASC, p.id"
	}
	q := `SELECT ` + projectCols + `, COALESCE(m.role, 'owner') AS role,
		COALESCE(NULLIF(u.display_name, ''), u.username) AS owner_name,
		(SELECT COUNT(*) FROM chapters c WHERE c.project_id = p.id) AS n_chapters,
		COALESCE((SELECT c.number FROM pages pg JOIN chapters c ON c.id = pg.chapter_id WHERE pg.project_id = p.id ORDER BY pg.updated_at DESC LIMIT 1),
			(SELECT MAX(c.number) FROM chapters c WHERE c.project_id = p.id), 0) AS last_chapter,
		(SELECT COUNT(*) FROM pages pg WHERE pg.project_id = p.id) AS n_pages,
		(SELECT COUNT(*) FROM panels pn WHERE pn.project_id = p.id) AS n_panels,
		(SELECT COUNT(*) FROM script_panels sp WHERE sp.project_id = p.id) AS n_script,
		(SELECT COUNT(*) FROM series se JOIN chapter_publications cp ON cp.series_id = se.id
			WHERE se.project_id = p.id AND cp.status = 'published') AS n_published,
		COALESCE((SELECT a.key FROM pages pg JOIN assets a ON a.id = pg.thumb_asset_id WHERE pg.project_id = p.id
			ORDER BY pg.updated_at DESC LIMIT 1), (SELECT a.key FROM assets a WHERE a.id = p.cover_asset_id), '') AS thumb_key
		FROM projects p
		LEFT JOIN project_members m ON m.project_id = p.id AND m.user_id = ?
		JOIN users u ON u.id = p.owner_id
		WHERE ` + strings.Join(where, " AND ") + ` ORDER BY ` + order
	var out []ProjectSummary
	if err := s.db.Select(ctx, &out, q, args...); err != nil {
		return nil, err
	}
	filtered := out[:0]
	for _, p := range out {
		p.Activity = p.UpdatedAt
		if f.Badge != "" && f.Badge != "shared" && p.Badge() != f.Badge {
			continue
		}
		filtered = append(filtered, p)
	}
	if f.Limit > 0 && len(filtered) > f.Limit {
		filtered = filtered[:f.Limit]
	}
	return filtered, nil
}

// CountProjects returns how many active projects userID has and how many are shared with them.
func (s *Service) CountProjects(ctx context.Context, userID string) (total, shared int, err error) {
	err = s.db.QueryRow(ctx, `SELECT COUNT(*), COALESCE(SUM(CASE WHEN p.owner_id <> ? THEN 1 ELSE 0 END), 0) FROM projects p
		LEFT JOIN project_members m ON m.project_id = p.id AND m.user_id = ?
		WHERE (p.owner_id = ? OR m.user_id IS NOT NULL) AND p.status <> 'archived'`, userID, userID, userID).Scan(&total, &shared)
	return total, shared, err
}

// ---- members --------------------------------------------------------------------

// Members lists a project's members, owner first.
func (s *Service) Members(ctx context.Context, projectID string) ([]Member, error) {
	var out []Member
	err := s.db.Select(ctx, &out, `SELECT `+memberCols+` FROM project_members m JOIN users u ON u.id = m.user_id
		WHERE m.project_id = ? ORDER BY CASE m.role WHEN 'owner' THEN 0 ELSE 1 END, m.created_at`, projectID)
	return out, err
}

// AddMember invites an existing user (by username) to the project with a role and scope.
func (s *Service) AddMember(ctx context.Context, a *Access, username, role string, scope MemberScope) (*Member, error) {
	if !a.Can(EditMembers) {
		return nil, core.ErrForbidden(i18n.T(ctx, "studio.error.role"))
	}
	username = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(username)), "@")
	errs := map[string]string{}
	if role == RoleOwner || !slices.Contains(Roles, role) {
		errs["role"] = i18n.T(ctx, "studio.form.invalid")
	}
	if !ValidPageRange(scope.Pages) {
		errs["pages"] = i18n.T(ctx, "studio.members.pages_invalid")
	}
	var u struct {
		ID     string `db:"id"`
		Status string `db:"status"`
	}
	if err := s.db.Get(ctx, &u, `SELECT id, status FROM users WHERE username = ?`, username); err != nil || u.Status != "active" {
		errs["username"] = i18n.T(ctx, "studio.members.unknown_user")
	}
	if len(errs) > 0 {
		return nil, core.ErrValidation(errs)
	}
	scope.Layers = cleanLayerKinds(scope.Layers)
	_, err := s.db.Exec(ctx, `INSERT INTO project_members (project_id, user_id, role, scope, invited_by, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		a.Project.ID, u.ID, role, db.MustJSON(scope), db.NullString(a.Actor.UserID), s.now())
	if db.IsUniqueViolation(err) {
		return nil, core.ErrValidation(map[string]string{"username": i18n.T(ctx, "studio.members.already")})
	}
	if err != nil {
		return nil, err
	}
	if s.d.Notify != nil {
		if _, err := s.d.Notify.Notify(ctx, notify.Input{UserID: u.ID, Kind: notify.KindSystem, ActorID: a.Actor.UserID,
			Title: i18n.T(ctx, "studio.members.invited_title", "title", a.Project.Title),
			Body:  i18n.T(ctx, "studio.members.invited_body", "role", i18n.T(ctx, "studio.role."+role)),
			Link:  "/studio/p/" + a.Project.ID + "/story", Data: map[string]any{"project_id": a.Project.ID}}); err != nil {
			s.log.Warn("studio: notify member", "err", err)
		}
	}
	s.Publish(ctx, a.Project.ID, "studio.members.updated", Event{Kind: "member", ID: u.ID})
	return s.member(ctx, a.Project.ID, u.ID)
}

func cleanLayerKinds(in []string) []string {
	var out []string
	for _, k := range in {
		if slices.Contains(LayerKinds, k) && !slices.Contains(out, k) {
			out = append(out, k)
		}
	}
	return out
}

func (s *Service) member(ctx context.Context, projectID, userID string) (*Member, error) {
	var m Member
	if err := s.db.Get(ctx, &m, `SELECT `+memberCols+` FROM project_members m JOIN users u ON u.id = m.user_id
		WHERE m.project_id = ? AND m.user_id = ?`, projectID, userID); err != nil {
		return nil, err
	}
	return &m, nil
}

// UpdateMember changes a member's role and scope (the owner's row cannot change).
func (s *Service) UpdateMember(ctx context.Context, a *Access, userID, role string, scope MemberScope) error {
	if !a.Can(EditMembers) {
		return core.ErrForbidden(i18n.T(ctx, "studio.error.role"))
	}
	if userID == a.Project.OwnerID || role == RoleOwner || !slices.Contains(Roles, role) {
		return core.ErrValidation(map[string]string{"role": i18n.T(ctx, "studio.form.invalid")})
	}
	if !ValidPageRange(scope.Pages) {
		return core.ErrValidation(map[string]string{"pages": i18n.T(ctx, "studio.members.pages_invalid")})
	}
	scope.Layers = cleanLayerKinds(scope.Layers)
	res, err := s.db.Exec(ctx, `UPDATE project_members SET role = ?, scope = ? WHERE project_id = ? AND user_id = ?`,
		role, db.MustJSON(scope), a.Project.ID, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return core.ErrNotFound()
	}
	s.Publish(ctx, a.Project.ID, "studio.members.updated", Event{Kind: "member", ID: userID})
	return nil
}

// RemoveMember removes a member (members may also leave on their own).
func (s *Service) RemoveMember(ctx context.Context, a *Access, userID string) error {
	self := userID == a.Actor.UserID
	if !a.Can(EditMembers) && !self {
		return core.ErrForbidden(i18n.T(ctx, "studio.error.role"))
	}
	if a.Actor.IsAgent() {
		return core.ErrForbidden(i18n.T(ctx, "studio.error.human_owner"))
	}
	if userID == a.Project.OwnerID {
		return core.ErrValidation(map[string]string{"user": i18n.T(ctx, "studio.members.owner_stays")})
	}
	if _, err := s.db.Exec(ctx, `DELETE FROM project_members WHERE project_id = ? AND user_id = ?`, a.Project.ID, userID); err != nil {
		return err
	}
	s.Publish(ctx, a.Project.ID, "studio.members.updated", Event{Kind: "member", ID: userID})
	return nil
}

// ---- wizard drafts ---------------------------------------------------------------

// DraftData holds the new-project wizard answers.
type DraftData struct {
	Title           string   `json:"title,omitempty"`
	Premise         string   `json:"premise,omitempty"`
	Genres          []string `json:"genres,omitempty"`
	Style           string   `json:"style,omitempty"`
	Format          string   `json:"format,omitempty"`
	PageSize        string   `json:"page_size,omitempty"`
	Chapters        int      `json:"chapters,omitempty"`
	PagesPerChapter string   `json:"pages_per_chapter,omitempty"`
	TemplateID      string   `json:"template_id,omitempty"`
	LayoutID        string   `json:"layout_id,omitempty"`
	From            string   `json:"from,omitempty"`
}

// Draft is an autosaved new-project wizard.
type Draft struct {
	ID        string             `db:"id" json:"id"`
	UserID    string             `db:"user_id" json:"user_id"`
	Step      string             `db:"step" json:"step"`
	Data      db.JSON[DraftData] `db:"data" json:"data"`
	ProjectID string             `db:"project_id" json:"project_id,omitempty"`
	JobID     string             `db:"job_id" json:"job_id,omitempty"`
	CreatedAt time.Time          `db:"created_at" json:"created_at"`
	UpdatedAt time.Time          `db:"updated_at" json:"updated_at"`
}

const draftCols = `id, user_id, step, data, COALESCE(project_id, '') AS project_id, COALESCE(job_id, '') AS job_id, created_at, updated_at`

// CurrentDraft returns the user's open wizard draft, creating one when none exists.
func (s *Service) CurrentDraft(ctx context.Context, userID string, create bool) (*Draft, error) {
	var d Draft
	err := s.db.Get(ctx, &d, `SELECT `+draftCols+` FROM project_drafts WHERE user_id = ? AND project_id IS NULL
		ORDER BY updated_at DESC LIMIT 1`, userID)
	if err == nil {
		return &d, nil
	}
	if !errors.Is(err, sql.ErrNoRows) || !create {
		return nil, err
	}
	now := s.now()
	d = Draft{ID: db.NewID(), UserID: userID, Step: "premise", CreatedAt: now, UpdatedAt: now}
	d.Data.V = DraftData{Style: s.defaultStyle(ctx, userID), Format: FormatMangaRTL, PageSize: "b5", Chapters: 12, PagesPerChapter: "30-36"}
	if _, err := s.db.Exec(ctx, `INSERT INTO project_drafts (id, user_id, step, data, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		d.ID, d.UserID, d.Step, db.MustJSON(d.Data.V), now, now); err != nil {
		return nil, err
	}
	return &d, nil
}

// DraftByID returns a draft of userID.
func (s *Service) DraftByID(ctx context.Context, userID, id string) (*Draft, error) {
	var d Draft
	if err := s.db.Get(ctx, &d, `SELECT `+draftCols+` FROM project_drafts WHERE id = ? AND user_id = ?`, id, userID); err != nil {
		return nil, core.ErrNotFound()
	}
	return &d, nil
}

// SaveDraft stores the draft answers and step.
func (s *Service) SaveDraft(ctx context.Context, d *Draft) error {
	d.UpdatedAt = s.now()
	_, err := s.db.Exec(ctx, `UPDATE project_drafts SET step = ?, data = ?, project_id = ?, job_id = ?, updated_at = ? WHERE id = ? AND user_id = ?`,
		d.Step, db.MustJSON(d.Data.V), db.NullString(d.ProjectID), db.NullString(d.JobID), d.UpdatedAt, d.ID, d.UserID)
	return err
}

// DiscardDraft deletes an unfinished draft.
func (s *Service) DiscardDraft(ctx context.Context, userID, id string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM project_drafts WHERE id = ? AND user_id = ? AND project_id IS NULL`, id, userID)
	return err
}

// defaultStyle is the art style picked during onboarding (A5 step 3).
func (s *Service) defaultStyle(ctx context.Context, userID string) string {
	var raw db.JSON[struct {
		ArtStyle string `json:"art_style"`
	}]
	if err := s.db.Get(ctx, &raw, `SELECT onboarding FROM users WHERE id = ?`, userID); err == nil && slices.Contains(Styles, raw.V.ArtStyle) {
		return raw.V.ArtStyle
	}
	return "seinen_bw"
}

// sortStrings returns a sorted copy.
func sortStrings(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}
