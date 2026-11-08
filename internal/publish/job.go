package publish

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/assets"
	"github.com/MaramHarsha/MangaVeyra/internal/compose"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
)

// JobType freezes one chapter in one language and publishes or schedules it.
const JobType = "publish.chapter"

// AI label values in increasing order of AI involvement.
var aiLevels = []string{"human", "ai_assisted", "ai"}

// aiRoles are the per-role labels (ADR 0012 §4).
var aiRoles = []string{"script", "art", "lettering"}

func aiRank(v string) int { return slices.Index(aiLevels, v) }

// publishInput is the human decision a publish job carries out. It is stored
// as private job input (it holds alt-text and synopsis drafts).
type publishInput struct {
	ProjectID   string            `json:"project_id"`
	ChapterID   string            `json:"chapter_id"`
	SeriesID    string            `json:"series_id"`
	Lang        string            `json:"lang"`
	Title       string            `json:"title"`
	Synopsis    string            `json:"synopsis"`
	Tags        []string          `json:"tags"`
	Visibility  string            `json:"visibility"`
	ScheduledAt *time.Time        `json:"scheduled_at,omitempty"`
	Notify      bool              `json:"notify"`
	AILabel     map[string]string `json:"ai_label"`
	AltText     map[string]string `json:"alt_text"`
}

// publishOutput is the job result.
type publishOutput struct {
	PublicationID string `json:"publication_id"`
	Status        string `json:"status"`
	Pages         int    `json:"pages"`
	// FirstLive is true when the chapter went live for the first time
	// (followers are notified only then, never for updates).
	FirstLive bool `json:"first_live"`
}

// pubFlags is the publish-owned JSON in chapter_publications.flags.
type pubFlags struct {
	Synopsis string   `json:"synopsis,omitempty"`
	Tags     []string `json:"tags,omitempty"`
	// Notify asks for follower notifications when the chapter first goes live.
	Notify bool `json:"notify"`
	// EverPublished is set the first time the chapter went live; updates and
	// re-publishing after an unpublish never notify again.
	EverPublished bool       `json:"ever_published,omitempty"`
	Notified      *int       `json:"notified,omitempty"`
	NotifiedAt    *time.Time `json:"notified_at,omitempty"`
	PublishedBy   string     `json:"published_by,omitempty"`
	JobID         string     `json:"job_id,omitempty"`
}

func decodeFlags(raw string) pubFlags {
	var f pubFlags
	_ = json.Unmarshal([]byte(raw), &f)
	return f
}

// errActive means a publish job for this chapter and language is running.
var errActive = fmt.Errorf("%w: a publish job is already running", ErrConflict)

// enqueuePublish validates the human decision and queues the freeze. The
// job uses a project scope: a locked (finished) chapter stays publishable.
func (s *service) enqueuePublish(ctx context.Context, actor studio.Actor, in publishInput) (*jobs.Job, error) {
	if actor.TokenID != "" {
		return nil, errForbidden
	}
	if !db.ValidID(in.ProjectID) || !db.ValidID(in.ChapterID) || !db.ValidID(in.SeriesID) || in.Lang == "" ||
		!slices.Contains(Visibilities, in.Visibility) {
		return nil, ErrInvalid
	}
	return s.jobs.Enqueue(ctx, jobs.EnqueueParams{
		Type: JobType, Scope: jobs.Scope{Kind: jobs.ScopeProject, ProjectID: in.ProjectID}, UserID: actor.UserID,
		Executor: jobs.ExecutorGo, Input: in, MaxAttempts: 2, Priority: 5,
		DedupeKey: "publish:" + in.ChapterID + ":" + in.Lang,
		Guard: func(ctx context.Context, tx *db.Tx) error {
			return s.authorizePublish(ctx, tx, actor, in.ProjectID)
		},
	})
}

// handlePublish renders every page, then commits the publication, its pages
// and the series aggregates in one lease-fenced transaction. Nothing a reader
// can see changes unless every page rendered.
func (s *service) handlePublish(ctx context.Context, run *jobs.Run) (err error) {
	j := run.Job
	var in publishInput
	if j.Type != JobType || j.Scope.Kind != jobs.ScopeProject || run.DecodeInput(&in) != nil || in.ProjectID != j.Scope.ProjectID {
		return jobs.Permanent(jobs.ErrCodeBadInput, errors.New("Publish request is invalid."))
	}
	actor := studio.Actor{UserID: j.UserID}
	if err := s.authorizePublish(ctx, s.db, actor, in.ProjectID); err != nil {
		return jobs.Permanent(jobs.ErrCodeBadInput, errors.New("You can no longer publish this chapter."))
	}
	src, err := s.loadChapter(ctx, in.ProjectID, in.ChapterID)
	if err != nil {
		return publishJobError(ctx, err)
	}
	if len(src.pages) == 0 {
		return jobs.Permanent(jobs.ErrCodeBadInput, errors.New("The chapter has no pages."))
	}
	if n := missingBubbles(src.pages, in.Lang); n > 0 {
		return jobs.Permanent(jobs.ErrCodeBadInput, fmt.Errorf("%d bubbles have no text in this language.", n))
	}
	total := len(src.pages) + 1
	type frozen struct {
		number, width, height int
		panels                []frozenPanel
		alt                   string
		staged                *assets.StagedAsset
	}
	pages := make([]frozen, 0, len(src.pages))
	committed := false
	defer func() {
		if committed {
			return
		}
		for _, p := range pages {
			_ = s.assets.DiscardStaged(context.WithoutCancel(ctx), p.staged)
		}
	}()
	for i, p := range src.pages {
		if err := run.Progress(ctx, float64(i)/float64(total), i+1, total, "Freezing pages"); err != nil {
			return publishJobError(ctx, err)
		}
		png, err := s.renderer.PNG(ctx, p, compose.Options{Lang: in.Lang, Scale: 1})
		if err != nil {
			return publishJobError(ctx, fmt.Errorf("render page %d: %w", p.Number, err))
		}
		staged, err := s.assets.Stage(ctx, assets.SaveParams{OwnerID: j.UserID, ProjectID: in.ProjectID, Kind: "publication_page",
			Source: assets.SourceSystem, ContentType: "image/png", Data: png,
			Meta: map[string]any{"chapter_id": in.ChapterID, "page_id": p.ID, "lang": in.Lang, "job_id": j.ID}})
		if err != nil {
			return publishJobError(ctx, err)
		}
		panels, alt := s.guidedPage(ctx, src, p, in.Lang, in.AltText)
		pages = append(pages, frozen{number: i + 1, width: p.Width, height: p.Height, panels: panels, alt: alt, staged: staged})
	}
	if err := run.Progress(ctx, float64(total-1)/float64(total), total, total, "Publishing"); err != nil {
		return publishJobError(ctx, err)
	}

	var oldAssets []string
	var out publishOutput
	err = run.CommitSideEffect(ctx, func(ctx context.Context, tx *db.Tx) (any, error) {
		if err := s.authorizePublish(ctx, tx, actor, in.ProjectID); err != nil {
			return nil, err
		}
		rev, err := readRevision(ctx, tx, in.ProjectID, in.ChapterID)
		if err != nil {
			return nil, err
		}
		if !rev.equal(src.rev) {
			return nil, fmt.Errorf("%w: chapter changed while publishing", ErrConflict)
		}
		series, ok, err := seriesForProject(ctx, tx, in.ProjectID)
		if err != nil {
			return nil, err
		}
		if !ok || series.ID != in.SeriesID {
			return nil, ErrNotFound
		}
		now := s.now().UTC()
		var prev struct {
			ID          string     `db:"id"`
			Status      string     `db:"status"`
			PublishedAt *time.Time `db:"published_at"`
			Flags       string     `db:"flags"`
		}
		exists := true
		if err := tx.Get(ctx, &prev, `SELECT id, status, published_at, flags FROM chapter_publications
			WHERE chapter_id = ? AND lang = ?`+forUpdate(tx), in.ChapterID, in.Lang); err != nil {
			if !db.IsNotFound(err) {
				return nil, err
			}
			exists = false
		}
		flags := decodeFlags(prev.Flags)
		flags.Synopsis, flags.Tags, flags.Notify, flags.PublishedBy, flags.JobID = in.Synopsis, in.Tags, in.Notify, j.UserID, j.ID

		status, publishedAt, scheduledAt := PublicationPublished, &now, (*time.Time)(nil)
		switch {
		case exists && prev.Status == PublicationPublished:
			// Updating a live chapter re-freezes it and keeps its publish time.
			publishedAt = prev.PublishedAt
		case in.ScheduledAt != nil && in.ScheduledAt.After(now):
			at := in.ScheduledAt.UTC()
			status, publishedAt, scheduledAt = PublicationScheduled, nil, &at
		}
		out = publishOutput{Status: status, Pages: len(pages)}
		if status == PublicationPublished && !flags.EverPublished {
			out.FirstLive, flags.EverPublished = true, true
		}
		label := map[string]string{}
		for _, role := range aiRoles {
			if v := in.AILabel[role]; aiRank(v) >= 0 {
				label[role] = v
			}
		}
		pubID := prev.ID
		if exists {
			if err := tx.Select(ctx, &oldAssets, `SELECT asset_id FROM publication_pages WHERE publication_id = ? AND asset_id IS NOT NULL`, pubID); err != nil {
				return nil, err
			}
			if _, err := tx.Exec(ctx, `DELETE FROM publication_pages WHERE publication_id = ?`, pubID); err != nil {
				return nil, err
			}
			if _, err := tx.Exec(ctx, `UPDATE chapter_publications SET series_id = ?, number = ?, title = ?, visibility = ?, flags = ?,
				ai_label = ?, status = ?, page_count = ?, scheduled_at = ?, published_at = ?, unpublished_at = NULL, updated_at = ?
				WHERE id = ?`, series.ID, float64(src.chapter.Number), in.Title, in.Visibility, db.MustJSON(flags), db.MustJSON(label),
				status, len(pages), timeArg(scheduledAt), timeArg(publishedAt), now, pubID); err != nil {
				return nil, err
			}
		} else {
			pubID = db.NewID()
			if _, err := tx.Exec(ctx, `INSERT INTO chapter_publications (id, series_id, chapter_id, lang, number, title, visibility,
				flags, ai_label, status, page_count, scheduled_at, published_at, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, pubID, series.ID, in.ChapterID, in.Lang, float64(src.chapter.Number),
				in.Title, in.Visibility, db.MustJSON(flags), db.MustJSON(label), status, len(pages), timeArg(scheduledAt),
				timeArg(publishedAt), now, now); err != nil {
				return nil, err
			}
		}
		for _, p := range pages {
			a, err := s.assets.CommitStagedTx(ctx, tx, p.staged)
			if err != nil {
				return nil, err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO publication_pages (id, publication_id, number, asset_id, width, height, panels,
				alt_text, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, db.NewID(), pubID, p.number, a.ID, p.width, p.height,
				db.MustJSON(p.panels), p.alt, now); err != nil {
				return nil, err
			}
		}
		if err := s.refreshSeriesTx(ctx, tx, series.ID); err != nil {
			return nil, err
		}
		out.PublicationID = pubID
		return out, nil
	})
	if err != nil {
		return publishJobError(ctx, err)
	}
	committed = true
	for _, id := range oldAssets {
		_ = s.assets.Delete(context.WithoutCancel(ctx), id)
	}
	if out.FirstLive {
		s.notifyFollowers(context.WithoutCancel(ctx), out.PublicationID)
	}
	return nil
}

func timeArg(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC()
}

// publishJobError maps failures to public job error codes.
func publishJobError(ctx context.Context, err error) error {
	var je *jobs.JobError
	switch {
	case errors.As(err, &je):
		return err
	case errors.Is(err, jobs.ErrLeaseLost):
		return jobs.ErrLeaseLost
	case ctx.Err() != nil || errors.Is(err, jobs.ErrCanceled):
		return jobs.Permanent(jobs.ErrCodeCanceled, errors.New("Publishing was canceled."))
	case errors.Is(err, ErrConflict):
		return jobs.Permanent("conflict", errors.New("The chapter changed while it was being published. Publish again."))
	case errors.Is(err, ErrNotFound), errors.Is(err, errForbidden), errors.Is(err, ErrInvalid):
		return jobs.Permanent(jobs.ErrCodeBadInput, errors.New("This chapter can no longer be published."))
	case db.IsTransient(err):
		return jobs.Retryable(jobs.ErrCodeInternal, errors.New("The database was busy. Retrying."))
	default:
		return jobs.Permanent(jobs.ErrCodeInternal, errors.New("Publishing failed."))
	}
}

// refreshSeriesTx recomputes what a series aggregates from its live chapters:
// the per-role AI label (the most AI any live chapter declares), the
// published languages, the first publish time and draft → ongoing.
func (s *service) refreshSeriesTx(ctx context.Context, tx *db.Tx, seriesID string) error {
	var rows []struct {
		Lang    string `db:"lang"`
		AILabel string `db:"ai_label"`
	}
	if err := tx.Select(ctx, &rows, `SELECT lang, ai_label FROM chapter_publications WHERE series_id = ? AND status = ?`,
		seriesID, PublicationPublished); err != nil {
		return err
	}
	var cur struct {
		OriginalLang string     `db:"original_lang"`
		Status       string     `db:"status"`
		PublishedAt  *time.Time `db:"published_at"`
	}
	if err := tx.Get(ctx, &cur, `SELECT original_lang, status, published_at FROM series WHERE id = ?`+forUpdate(tx), seriesID); err != nil {
		return err
	}
	label := map[string]string{}
	langs := map[string]bool{}
	for _, r := range rows {
		langs[r.Lang] = true
		var l map[string]string
		_ = json.Unmarshal([]byte(r.AILabel), &l)
		for _, role := range aiRoles {
			if v := l[role]; aiRank(v) > aiRank(label[role]) {
				label[role] = v
			}
		}
	}
	languages := []string{cur.OriginalLang}
	for _, l := range append(slices.Clone(pagedoc.ContentLanguages), sortedKeys(langs)...) {
		if langs[l] && !slices.Contains(languages, l) {
			languages = append(languages, l)
		}
	}
	now := s.now().UTC()
	publishedAt, status := cur.PublishedAt, cur.Status
	if len(rows) > 0 {
		if publishedAt == nil {
			publishedAt = &now
		}
		if status == SeriesDraft {
			status = SeriesOngoing
		}
	}
	_, err := tx.Exec(ctx, `UPDATE series SET ai_label = ?, languages = ?, published_at = ?, status = ?, updated_at = ? WHERE id = ?`,
		db.MustJSON(label), db.MustJSON(languages), timeArg(publishedAt), status, now, seriesID)
	return err
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// unpublish hides a live or scheduled chapter. Readers get ErrNotFound for it
// at once; its frozen pages stay so the creator can publish it again.
func (s *service) unpublish(ctx context.Context, actor studio.Actor, projectID, chapterID, lang string) error {
	return s.db.WithTx(ctx, func(tx *db.Tx) error {
		if err := s.authorizePublish(ctx, tx, actor, projectID); err != nil {
			return err
		}
		var seriesID string
		if err := tx.Get(ctx, &seriesID, `SELECT p.series_id FROM chapter_publications p JOIN series s ON s.id = p.series_id
			WHERE p.chapter_id = ? AND p.lang = ? AND s.project_id = ? AND p.status IN (?, ?)`+forUpdate(tx),
			chapterID, strings.TrimSpace(lang), projectID, PublicationPublished, PublicationScheduled); err != nil {
			if db.IsNotFound(err) {
				return ErrNotFound
			}
			return err
		}
		now := s.now().UTC()
		if _, err := tx.Exec(ctx, `UPDATE chapter_publications SET status = ?, unpublished_at = ?, scheduled_at = NULL, updated_at = ?
			WHERE chapter_id = ? AND lang = ?`, PublicationUnpublished, now, now, chapterID, strings.TrimSpace(lang)); err != nil {
			return err
		}
		return s.refreshSeriesTx(ctx, tx, seriesID)
	})
}
