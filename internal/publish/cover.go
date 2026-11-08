package publish

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/draw"
	"image/png"

	"github.com/MaramHarsha/MangaVeyra/internal/assets"
	"github.com/MaramHarsha/MangaVeyra/internal/compose"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/gen"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
)

// Covers. The series keeps its own cover_asset_id (what readers see on E5,
// Explore and the library). "Choose from pages" stores a 3:4 crop of a
// rendered chapter page; "Generate cover" runs the existing cover.generate
// job, and keeping a candidate sets the project cover (gen) and points the
// series cover at the same asset (no copy of the bytes).

// chooseCoverPage renders one page of the chapter in lang and stores the top
// 3:4 crop as the series cover.
func (s *service) chooseCoverPage(ctx context.Context, actor studio.Actor, p studio.Project, ch studio.Chapter, pageID, lang string) error {
	if err := s.authorizePublish(ctx, s.db, actor, p.ID); err != nil {
		return err
	}
	page, err := s.pages.GetPage(ctx, pageID)
	if err != nil || page.ProjectID != p.ID || page.ChapterID != ch.ID {
		return ErrNotFound
	}
	img, err := s.renderer.Image(ctx, page, compose.Options{Lang: lang, Scale: 1})
	if err != nil {
		return fmt.Errorf("publish: render cover page: %w", err)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, cropCover(img)); err != nil {
		return fmt.Errorf("publish: encode cover: %w", err)
	}
	a, err := s.assets.Save(ctx, assets.SaveParams{OwnerID: actor.UserID, ProjectID: p.ID, Kind: "series_cover",
		Source: assets.SourceSystem, ContentType: "image/png", Data: buf.Bytes(),
		Meta: map[string]any{"chapter_id": ch.ID, "page_id": page.ID, "lang": lang}})
	if err != nil {
		return fmt.Errorf("publish: save cover: %w", err)
	}
	return s.setSeriesCover(ctx, actor, p, a.ID)
}

// cropCover keeps the top of the page at 3:4, centred horizontally.
func cropCover(src image.Image) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return src
	}
	cw, ch := w, w*4/3
	if ch > h {
		cw, ch = h*3/4, h
	}
	x0 := b.Min.X + (w-cw)/2
	out := image.NewNRGBA(image.Rect(0, 0, cw, ch))
	draw.Draw(out, out.Bounds(), src, image.Pt(x0, b.Min.Y), draw.Src)
	return out
}

// setSeriesCover points the series (created from the project if needed) at assetID.
func (s *service) setSeriesCover(ctx context.Context, actor studio.Actor, p studio.Project, assetID string) error {
	return s.db.WithTx(ctx, func(tx *db.Tx) error {
		if err := s.authorizePublish(ctx, tx, actor, p.ID); err != nil {
			return err
		}
		series, err := s.ensureSeriesTx(ctx, tx, actor, p, "")
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE series SET cover_asset_id = ?, updated_at = ? WHERE id = ?`, assetID, s.now().UTC(), series.ID)
		return err
	})
}

// generateCover queues cover.generate for the chapter (four candidates).
func (s *service) generateCover(ctx context.Context, actor studio.Actor, p studio.Project, ch studio.Chapter) (*jobs.Job, error) {
	if s.cover == nil {
		return nil, ErrNotFound
	}
	if err := s.authorizePublish(ctx, s.db, actor, p.ID); err != nil {
		return nil, err
	}
	return s.cover.EnqueueCover(ctx, actor, gen.CoverRequest{ProjectID: p.ID, ChapterID: ch.ID, Variations: 4})
}

// coverCandidates reads a cover job's candidates for the E1 picker.
func (s *service) coverCandidates(ctx context.Context, actor studio.Actor, jobID string) (gen.CoverResult, error) {
	if s.cover == nil {
		return gen.CoverResult{}, ErrNotFound
	}
	return s.cover.CoverCandidates(ctx, actor, jobID)
}

// keepGeneratedCover keeps one candidate: gen sets the project cover (and
// refuses a locked chapter or a stale basis); the series then uses that asset.
func (s *service) keepGeneratedCover(ctx context.Context, actor studio.Actor, p studio.Project, jobID string, index int) error {
	if s.cover == nil {
		return ErrNotFound
	}
	if err := s.authorizePublish(ctx, s.db, actor, p.ID); err != nil {
		return err
	}
	assetID, err := s.cover.KeepCover(ctx, actor, jobID, index)
	if err != nil {
		return err
	}
	return s.setSeriesCover(ctx, actor, p, assetID)
}
