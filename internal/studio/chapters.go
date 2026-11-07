package studio

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
)

const chapterCols = `c.id, c.project_id, c.number, c.title, c.summary, c.status, c.locked, c.ai_label, c.updated_at,
	(SELECT COUNT(*) FROM pages pg WHERE pg.chapter_id = c.id) AS page_count`

type chapterRow struct {
	ID        string    `db:"id"`
	ProjectID string    `db:"project_id"`
	Number    int       `db:"number"`
	Title     string    `db:"title"`
	Summary   string    `db:"summary"`
	Status    string    `db:"status"`
	Locked    bool      `db:"locked"`
	AILabel   string    `db:"ai_label"`
	UpdatedAt time.Time `db:"updated_at"`
	PageCount int       `db:"page_count"`
}

func (r chapterRow) chapter() (Chapter, error) {
	labels := map[string]string{}
	if err := json.Unmarshal([]byte(r.AILabel), &labels); err != nil {
		return Chapter{}, fmt.Errorf("studio: decode chapter AI label: %w", err)
	}
	if labels == nil {
		labels = map[string]string{}
	}
	return Chapter{ID: r.ID, ProjectID: r.ProjectID, Number: r.Number, Title: r.Title, Summary: r.Summary,
		Status: r.Status, Locked: r.Locked, AILabel: labels, PageCount: r.PageCount, UpdatedAt: r.UpdatedAt.UTC()}, nil
}

func (s *service) CreateChapter(ctx context.Context, actor Actor, projectID, title string) (Chapter, error) {
	projectID = strings.TrimSpace(projectID)
	title = strings.TrimSpace(title)
	if projectID == "" {
		return Chapter{}, ErrInvalid
	}
	if err := validateText("chapter title", title, maxShortText, true); err != nil {
		return Chapter{}, err
	}
	id, now := db.NewID(), db.Now()
	var out Chapter
	err := s.db.WithTx(ctx, func(tx *db.Tx) error {
		if err := s.authorize(ctx, tx, actor, projectID, ActEditScript); err != nil {
			return err
		}
		var lockedID string
		if err := tx.Get(ctx, &lockedID, `SELECT id FROM projects WHERE id = ?`+forUpdate(tx), projectID); err != nil {
			if db.IsNotFound(err) {
				return ErrNotFound
			}
			return fmt.Errorf("studio: lock project for chapter: %w", err)
		}
		var number sql.NullInt64
		if err := tx.Get(ctx, &number, `SELECT MAX(number) FROM chapters WHERE project_id = ?`, projectID); err != nil {
			return fmt.Errorf("studio: allocate chapter number: %w", err)
		}
		next := int(number.Int64) + 1
		if _, err := tx.Exec(ctx, `INSERT INTO chapters (id, project_id, number, title, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?)`, id, projectID, next, title, now, now); err != nil {
			if db.IsUniqueViolation(err) {
				return ErrConflict
			}
			return fmt.Errorf("studio: insert chapter: %w", err)
		}
		var err error
		out, err = getChapter(ctx, tx, id)
		return err
	})
	return out, err
}

func (s *service) GetChapter(ctx context.Context, id string) (Chapter, error) {
	return getChapter(ctx, s.db, strings.TrimSpace(id))
}

func getChapter(ctx context.Context, q db.Querier, id string) (Chapter, error) {
	var row chapterRow
	if err := q.Get(ctx, &row, `SELECT `+chapterCols+` FROM chapters c WHERE c.id = ?`, id); err != nil {
		if db.IsNotFound(err) {
			return Chapter{}, ErrNotFound
		}
		return Chapter{}, fmt.Errorf("studio: get chapter: %w", err)
	}
	return row.chapter()
}

func (s *service) ListChapters(ctx context.Context, projectID string) ([]Chapter, error) {
	var rows []chapterRow
	if err := s.db.Select(ctx, &rows, `SELECT `+chapterCols+` FROM chapters c WHERE c.project_id = ? ORDER BY c.number, c.id`,
		strings.TrimSpace(projectID)); err != nil {
		return nil, fmt.Errorf("studio: list chapters: %w", err)
	}
	out := make([]Chapter, 0, len(rows))
	for _, row := range rows {
		chapter, err := row.chapter()
		if err != nil {
			return nil, err
		}
		out = append(out, chapter)
	}
	return out, nil
}

func (s *service) SetChapterAILabelTx(ctx context.Context, tx *db.Tx, chapterID, role, value string) error {
	if tx == nil {
		return ErrInvalid
	}
	chapterID, role, value = strings.TrimSpace(chapterID), strings.TrimSpace(role), strings.TrimSpace(value)
	if chapterID == "" {
		return ErrInvalid
	}
	switch role {
	case "script", "art", "lettering":
	default:
		return fmt.Errorf("%w: invalid AI-label role", ErrInvalid)
	}
	switch value {
	case "human", "ai_assisted", "ai":
	default:
		return fmt.Errorf("%w: invalid AI-label value", ErrInvalid)
	}
	var raw string
	if err := tx.Get(ctx, &raw, `SELECT ai_label FROM chapters WHERE id = ?`+forUpdate(tx), chapterID); err != nil {
		if db.IsNotFound(err) {
			return ErrNotFound
		}
		return fmt.Errorf("studio: get chapter AI label: %w", err)
	}
	labels := map[string]string{}
	if err := json.Unmarshal([]byte(raw), &labels); err != nil {
		return fmt.Errorf("studio: decode chapter AI label: %w", err)
	}
	labels[role] = value
	b, err := json.Marshal(labels)
	if err != nil {
		return fmt.Errorf("studio: encode chapter AI label: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE chapters SET ai_label = ?, updated_at = ? WHERE id = ?`, string(b), db.Now(), chapterID); err != nil {
		return fmt.Errorf("studio: update chapter AI label: %w", err)
	}
	return nil
}
