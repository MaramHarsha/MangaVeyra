package studio

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
)

const projectCols = `p.id, p.owner_id, p.title, p.slug, p.premise, p.logline, p.genres, p.tags, p.style,
	p.format, p.page_size, p.page_width, p.page_height, p.language, p.status, p.cover_asset_id, p.created_at, p.updated_at`

type projectRow struct {
	ID           string         `db:"id"`
	OwnerID      string         `db:"owner_id"`
	Title        string         `db:"title"`
	Slug         string         `db:"slug"`
	Premise      string         `db:"premise"`
	Logline      string         `db:"logline"`
	Genres       string         `db:"genres"`
	Tags         string         `db:"tags"`
	Style        string         `db:"style"`
	Format       string         `db:"format"`
	PageSize     string         `db:"page_size"`
	PageWidth    int            `db:"page_width"`
	PageHeight   int            `db:"page_height"`
	Language     string         `db:"language"`
	Status       string         `db:"status"`
	CoverAssetID sql.NullString `db:"cover_asset_id"`
	CreatedAt    time.Time      `db:"created_at"`
	UpdatedAt    time.Time      `db:"updated_at"`
}

func (r projectRow) project() (Project, error) {
	var genres, tags []string
	if err := json.Unmarshal([]byte(r.Genres), &genres); err != nil {
		return Project{}, fmt.Errorf("studio: decode project genres: %w", err)
	}
	if err := json.Unmarshal([]byte(r.Tags), &tags); err != nil {
		return Project{}, fmt.Errorf("studio: decode project tags: %w", err)
	}
	if genres == nil {
		genres = []string{}
	}
	if tags == nil {
		tags = []string{}
	}
	return Project{
		ID: r.ID, OwnerID: r.OwnerID, Title: r.Title, Slug: r.Slug, Premise: r.Premise, Logline: r.Logline,
		Genres: genres, Tags: tags, Style: r.Style, Format: r.Format, PageSize: r.PageSize, PageWidth: r.PageWidth,
		PageHeight: r.PageHeight, Language: r.Language, Status: r.Status, CoverAssetID: r.CoverAssetID.String,
		CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC(),
	}, nil
}

func (s *service) CreateProject(ctx context.Context, actor Actor, in NewProject) (Project, error) {
	in = normalizeNewProject(in)
	if err := validateNewProject(in); err != nil {
		return Project{}, err
	}

	id := db.NewID()
	now := db.Now()
	settings := map[string]any{}
	if in.SourceText != "" {
		settings["script_source_text"] = in.SourceText
	}
	settingsJSON, err := json.Marshal(settings)
	if err != nil {
		return Project{}, fmt.Errorf("studio: encode project settings: %w", err)
	}
	var out Project
	err = s.db.WithTx(ctx, func(tx *db.Tx) error {
		state, err := s.resolveActor(ctx, tx, actor)
		if err != nil {
			return err
		}
		if !state.user.CanCreate() {
			return ErrForbidden
		}
		if state.token != nil && (!state.token.HasScope("projects:write") || !state.token.AllowsProject(id)) {
			return ErrForbidden
		}
		// Project number/slug allocation is serialized per owner on PostgreSQL;
		// SQLite's WithTx already holds the process write lock.
		var lockedID string
		if err := tx.Get(ctx, &lockedID, `SELECT id FROM users WHERE id = ?`+forUpdate(tx), state.user.ID); err != nil {
			return fmt.Errorf("studio: lock project owner: %w", err)
		}
		slug := slugify(in.Title)
		var n int
		if err := tx.Get(ctx, &n, `SELECT COUNT(*) FROM projects WHERE owner_id = ? AND slug = ?`, state.user.ID, slug); err != nil {
			return fmt.Errorf("studio: check project slug: %w", err)
		}
		if n != 0 {
			slug += "-" + id[:6]
		}
		_, err = tx.Exec(ctx, `INSERT INTO projects
			(id, owner_id, title, slug, premise, logline, genres, tags, style, format, page_size, page_width, page_height,
			 language, status, settings, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'draft', ?, ?, ?)`,
			id, state.user.ID, in.Title, slug, in.Premise, in.Logline, db.MustJSON(in.Genres), db.MustJSON(in.Tags),
			in.Style, in.Format, in.PageSize, in.PageWidth, in.PageHeight, in.Language, string(settingsJSON), now, now)
		if err != nil {
			if db.IsUniqueViolation(err) {
				return ErrConflict
			}
			return fmt.Errorf("studio: insert project: %w", err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO project_members (project_id, user_id, role, created_at) VALUES (?, ?, ?, ?)`,
			id, state.user.ID, RoleOwner, now); err != nil {
			return fmt.Errorf("studio: insert owner membership: %w", err)
		}
		out, err = getProject(ctx, tx, id)
		return err
	})
	return out, err
}

func (s *service) GetProject(ctx context.Context, id string) (Project, error) {
	return getProject(ctx, s.db, strings.TrimSpace(id))
}

func getProject(ctx context.Context, q db.Querier, id string) (Project, error) {
	var row projectRow
	if err := q.Get(ctx, &row, `SELECT `+projectCols+` FROM projects p WHERE p.id = ?`, id); err != nil {
		if db.IsNotFound(err) {
			return Project{}, ErrNotFound
		}
		return Project{}, fmt.Errorf("studio: get project: %w", err)
	}
	return row.project()
}

func (s *service) ListProjects(ctx context.Context, userID string, limit int) ([]ProjectSummary, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, ErrInvalid
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	type row struct {
		projectRow
		MemberRole       string         `db:"member_role"`
		ChapterCount     int            `db:"chapter_count"`
		PageCount        int            `db:"page_count"`
		LatestChapterID  sql.NullString `db:"latest_chapter_id"`
		LatestChapterNum sql.NullInt64  `db:"latest_chapter_number"`
	}
	var rows []row
	err := s.db.Select(ctx, &rows, `SELECT `+projectCols+`, pm.role AS member_role,
		(SELECT COUNT(*) FROM chapters c WHERE c.project_id = p.id) AS chapter_count,
		(SELECT COUNT(*) FROM pages pg WHERE pg.project_id = p.id) AS page_count,
		(SELECT c.id FROM chapters c WHERE c.project_id = p.id ORDER BY c.updated_at DESC, c.number DESC LIMIT 1) AS latest_chapter_id,
		(SELECT c.number FROM chapters c WHERE c.project_id = p.id ORDER BY c.updated_at DESC, c.number DESC LIMIT 1) AS latest_chapter_number
		FROM projects p JOIN project_members pm ON pm.project_id = p.id
		WHERE pm.user_id = ? AND p.archived_at IS NULL ORDER BY p.updated_at DESC, p.id DESC LIMIT ?`, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("studio: list projects: %w", err)
	}
	out := make([]ProjectSummary, 0, len(rows))
	for _, row := range rows {
		p, err := row.projectRow.project()
		if err != nil {
			return nil, err
		}
		out = append(out, ProjectSummary{Project: p, Role: row.MemberRole, ChapterCount: row.ChapterCount,
			PageCount: row.PageCount, LatestChapterID: row.LatestChapterID.String,
			LatestChapterNumber: int(row.LatestChapterNum.Int64), ThumbAssetID: p.CoverAssetID})
	}
	return out, nil
}

func (s *service) SourceText(ctx context.Context, projectID string) (string, error) {
	var raw string
	if err := s.db.Get(ctx, &raw, `SELECT settings FROM projects WHERE id = ?`, strings.TrimSpace(projectID)); err != nil {
		if db.IsNotFound(err) {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("studio: get source text: %w", err)
	}
	var settings map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &settings); err != nil {
		return "", fmt.Errorf("studio: decode project settings: %w", err)
	}
	v, ok := settings["script_source_text"]
	if !ok || string(v) == "null" {
		return "", nil
	}
	var source string
	if err := json.Unmarshal(v, &source); err != nil || !utf8.ValidString(source) || len(source) > maxSourceBytes {
		return "", fmt.Errorf("studio: invalid stored source text")
	}
	return source, nil
}

func normalizeNewProject(in NewProject) NewProject {
	in.Title = strings.TrimSpace(in.Title)
	in.Premise = strings.TrimSpace(in.Premise)
	in.Logline = strings.TrimSpace(in.Logline)
	in.Style = strings.TrimSpace(in.Style)
	in.Format = strings.TrimSpace(in.Format)
	in.PageSize = strings.TrimSpace(in.PageSize)
	in.Language = strings.TrimSpace(in.Language)
	if in.Style == "" {
		in.Style = "bw_screentone"
	}
	if in.Format == "" {
		in.Format = "manga_rtl"
	}
	if in.PageSize == "" {
		in.PageSize = "b5"
	}
	if in.Language == "" {
		in.Language = "en"
	}
	if in.PageWidth == 0 {
		in.PageWidth = 1600
	}
	if in.PageHeight == 0 {
		in.PageHeight = 2400
	}
	in.Genres = normalizeList(in.Genres)
	in.Tags = normalizeList(in.Tags)
	return in
}

func validateNewProject(in NewProject) error {
	checks := []error{
		validateText("title", in.Title, maxShortText, true),
		validateText("premise", in.Premise, 4000, false),
		validateText("logline", in.Logline, 1000, false),
		validateText("style", in.Style, 80, true),
		validateText("page_size", in.PageSize, 80, true),
		validateText("language", in.Language, 35, true),
	}
	for _, err := range checks {
		if err != nil {
			return err
		}
	}
	switch in.Format {
	case "manga_rtl", "comic_ltr", "webtoon":
	default:
		return fmt.Errorf("%w: invalid project format", ErrInvalid)
	}
	if in.PageWidth < 1 || in.PageWidth > 20000 || in.PageHeight < 1 || in.PageHeight > 20000 {
		return fmt.Errorf("%w: invalid page dimensions", ErrInvalid)
	}
	if !utf8.ValidString(in.SourceText) || len(in.SourceText) > maxSourceBytes {
		return fmt.Errorf("%w: source text must be valid UTF-8 and at most 1 MiB", ErrInvalid)
	}
	if err := validateList("genres", in.Genres); err != nil {
		return err
	}
	return validateList("tags", in.Tags)
}

func normalizeList(in []string) []string {
	if in == nil {
		return []string{}
	}
	out := make([]string, len(in))
	for i := range in {
		out[i] = strings.TrimSpace(in[i])
	}
	return out
}

func validateList(name string, values []string) error {
	if len(values) > 64 {
		return fmt.Errorf("%w: too many %s", ErrInvalid, name)
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if err := validateText(name, value, 80, true); err != nil {
			return err
		}
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			return fmt.Errorf("%w: duplicate %s", ErrInvalid, name)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func slugify(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		if r <= unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
			b.WriteRune(r)
			dash = false
			continue
		}
		if b.Len() != 0 && !dash {
			b.WriteByte('-')
			dash = true
		}
	}
	result := strings.Trim(b.String(), "-")
	if result == "" {
		return "project"
	}
	if len(result) > 120 {
		result = strings.TrimRight(result[:120], "-")
	}
	return result
}
