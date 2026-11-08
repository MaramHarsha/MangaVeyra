package publish

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/notify"
)

// Scheduling: a scheduled publication already has its frozen pages; the loop
// only flips it to published once scheduled_at has passed. The flip is a
// conditional UPDATE under a row lock, so with several server processes each
// due row is published, and its followers notified, exactly once.

const scheduleBatch = 50

// publishDue publishes every due scheduled chapter through d and returns how
// many it published.
func (s *service) publishDue(ctx context.Context, d *db.DB) (int, error) {
	now := s.now().UTC()
	var ids []string
	if err := d.Select(ctx, &ids, `SELECT id FROM chapter_publications WHERE status = ? AND scheduled_at IS NOT NULL
		AND scheduled_at <= ? ORDER BY scheduled_at, id LIMIT ?`, PublicationScheduled, now, scheduleBatch); err != nil {
		return 0, fmt.Errorf("publish: due publications: %w", err)
	}
	published := 0
	for _, id := range ids {
		fired, first := false, false
		err := d.WithTx(ctx, func(tx *db.Tx) error {
			var row struct {
				SeriesID string `db:"series_id"`
				Flags    string `db:"flags"`
			}
			if err := tx.Get(ctx, &row, `SELECT series_id, flags FROM chapter_publications WHERE id = ? AND status = ?
				AND scheduled_at <= ?`+forUpdate(tx), id, PublicationScheduled, now); err != nil {
				if db.IsNotFound(err) {
					return nil // another process published it first
				}
				return err
			}
			flags := decodeFlags(row.Flags)
			first, flags.EverPublished = !flags.EverPublished, true
			res, err := tx.Exec(ctx, `UPDATE chapter_publications SET status = ?, published_at = ?, unpublished_at = NULL, flags = ?,
				updated_at = ? WHERE id = ? AND status = ?`, PublicationPublished, now, db.MustJSON(flags), now, id, PublicationScheduled)
			if err != nil {
				return err
			}
			if n, err := res.RowsAffected(); err != nil || n != 1 {
				return err
			}
			fired = true
			return s.refreshSeriesTx(ctx, tx, row.SeriesID)
		})
		if err != nil {
			return published, fmt.Errorf("publish: publish due %s: %w", id, err)
		}
		if fired {
			published++
			if first {
				s.notifyFollowers(ctx, id)
			}
		}
	}
	return published, nil
}

// Start runs the schedule loop until ctx ends.
func (m *Module) Start(ctx context.Context) error {
	every := m.svc.scheduleEvery
	if every <= 0 {
		every = 30 * time.Second
	}
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		if _, err := m.svc.publishDue(ctx, m.svc.db); err != nil && ctx.Err() == nil && m.d.Log != nil {
			m.d.Log.WarnContext(ctx, "publish scheduled chapters", "err", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}

// FollowerSource is the part of social.Follows that publish needs. publish
// cannot import internal/social (social reads published series through
// publish.Service, so the import would be a cycle); the social module, or the
// app wiring, provides its social.Follows under this type as well:
//
//	core.Provide[publish.FollowerSource](d, follows)
type FollowerSource interface {
	// Followers lists the users following a series who asked to be notified.
	Followers(ctx context.Context, seriesID, cursor string, limit int) (userIDs []string, next string, err error)
}

// follows resolves the follower source lazily; without one nobody is notified.
func (s *service) follows() FollowerSource {
	if s.followsOverride != nil {
		return s.followsOverride
	}
	if s.deps == nil {
		return nil
	}
	f, _ := core.Use[FollowerSource](s.deps)
	return f
}

// notifyFollowers sends the new-chapter notification to every follower who
// asked to be notified and may read the chapter (ADR 0012 §10). It claims
// the publication first, so it runs at most once per publication even when
// the publish job and the schedule loop race; it never runs for updates.
// Unlisted chapters are shared by link only, so they notify nobody: the claim
// records zero notifications.
func (s *service) notifyFollowers(ctx context.Context, publicationID string) {
	var info struct {
		SeriesID    string  `db:"series_id"`
		Number      float64 `db:"number"`
		Title       string  `db:"title"`
		Lang        string  `db:"lang"`
		Slug        string  `db:"slug"`
		SeriesTitle string  `db:"series_title"`
	}
	var flags pubFlags
	claimed := false
	err := s.db.WithTx(ctx, func(tx *db.Tx) error {
		var row struct {
			Flags      string `db:"flags"`
			Visibility string `db:"visibility"`
		}
		if err := tx.Get(ctx, &row, `SELECT flags, visibility FROM chapter_publications WHERE id = ? AND status = ?`+forUpdate(tx),
			publicationID, PublicationPublished); err != nil {
			if db.IsNotFound(err) {
				return nil
			}
			return err
		}
		flags = decodeFlags(row.Flags)
		if !flags.Notify || flags.Notified != nil {
			return nil
		}
		zero, now := 0, s.now().UTC()
		flags.Notified, flags.NotifiedAt = &zero, &now
		if _, err := tx.Exec(ctx, `UPDATE chapter_publications SET flags = ? WHERE id = ?`, db.MustJSON(flags), publicationID); err != nil {
			return err
		}
		claimed = row.Visibility != VisibilityUnlisted
		if !claimed {
			return nil
		}
		return tx.Get(ctx, &info, `SELECT p.series_id, p.number, p.title, p.lang, s.slug, s.title AS series_title
			FROM chapter_publications p JOIN series s ON s.id = p.series_id WHERE p.id = ?`, publicationID)
	})
	if err != nil || !claimed {
		if err != nil && s.log != nil {
			s.log.WarnContext(ctx, "claim chapter notifications", "publication", publicationID, "err", err)
		}
		return
	}
	sent := 0
	if f := s.follows(); f != nil {
		inst := s.settings.MustInstance(ctx)
		number := strconv.FormatFloat(info.Number, 'f', -1, 64)
		link := "/s/" + info.Slug + "/" + number
		cursor := ""
		for page := 0; page < 10_000; page++ {
			ids, next, err := f.Followers(ctx, info.SeriesID, cursor, 200)
			if err != nil {
				if s.log != nil {
					s.log.WarnContext(ctx, "list series followers", "series", info.SeriesID, "err", err)
				}
				break
			}
			for _, uid := range ids {
				if uid == "" || uid == flags.PublishedBy ||
					!s.CanRead(ctx, Viewer{UserID: uid}, Series{ID: info.SeriesID}, Publication{ID: publicationID}) {
					continue
				}
				locale := ""
				if u, err := s.auth.UserByID(ctx, uid); err == nil && u != nil {
					locale = u.Locale
				}
				lctx := i18n.WithLocale(ctx, i18n.Negotiate(s.i18n.Languages(), inst.DefaultLocale, locale, "", ""))
				title := i18n.T(lctx, "publish.notify.title", "series", info.SeriesTitle, "number", number)
				if _, err := s.notify.Notify(ctx, notify.Input{UserID: uid, Kind: notify.KindChapter, Title: title, Body: info.Title,
					Link: link, ActorID: flags.PublishedBy, GroupKey: "chapter:" + publicationID,
					Data: map[string]any{"series_id": info.SeriesID, "publication_id": publicationID, "lang": info.Lang, "number": info.Number}}); err == nil {
					sent++
				}
			}
			if next == "" {
				break
			}
			cursor = next
		}
	}
	now := s.now().UTC()
	flags.Notified, flags.NotifiedAt = &sent, &now
	if _, err := s.db.Exec(ctx, `UPDATE chapter_publications SET flags = ? WHERE id = ?`, db.MustJSON(flags), publicationID); err != nil && s.log != nil {
		s.log.WarnContext(ctx, "record chapter notifications", "publication", publicationID, "err", err)
	}
}
