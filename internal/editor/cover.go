package editor

import (
	"errors"
	"net/http"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/gen"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/pagestore"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
)

// Chapter cover JSON API (backend for the E1 Publish flow's "Generate
// cover"; its screen arrives with E1). Session only, like the rest of the
// editor API. A project the user cannot read is 404; a member without art and
// generation access gets 403.

// coverError maps cover failures. Unlike page edits, an authorization
// failure after the project was readable is reported as 403.
func coverError(r *http.Request, err error) error {
	ctx := r.Context()
	switch {
	case errors.Is(err, studio.ErrForbidden):
		return core.ErrForbidden(i18n.T(ctx, "editor.cover.error.forbidden"))
	case errors.Is(err, gen.ErrCoverKept):
		return core.ErrConflict(i18n.T(ctx, "editor.cover.error.kept"))
	case errors.Is(err, gen.ErrCoverPending):
		return core.ErrConflict(i18n.T(ctx, "editor.cover.error.pending"))
	case errors.Is(err, studio.ErrLocked), errors.Is(err, pagestore.ErrLocked):
		return core.ErrLocked(i18n.T(ctx, "editor.cover.error.locked"))
	case errors.Is(err, studio.ErrConflict):
		return core.ErrConflict(i18n.T(ctx, "editor.cover.error.conflict"))
	case errors.Is(err, studio.ErrInvalid):
		return core.ErrValidation(map[string]string{"cover": i18n.T(ctx, "editor.cover.error.invalid")})
	}
	return mapped(r, err)
}

// coverAccess resolves the chapter (404 unless the project is readable) and
// then requires the panel-art permissions (403).
func (m *Module) coverAccess(r *http.Request) (resource, error) {
	res, err := m.resource(r, false)
	if err != nil {
		return res, mapped(r, err)
	}
	for _, action := range []studio.Action{studio.ActGenerate, studio.ActEditArt} {
		if err := m.authorize(r, res, action); err != nil {
			return res, coverError(r, err)
		}
	}
	return res, nil
}

// coverJobOf returns the caller's own cover job of this chapter, or 404.
func (m *Module) coverJobOf(r *http.Request, res resource) (*jobs.Job, error) {
	id := r.PathValue("job")
	if !db.ValidID(id) {
		return nil, core.ErrNotFound()
	}
	j, err := m.d.Jobs.Get(r.Context(), id)
	if err != nil || j.Type != gen.JobCover || j.UserID != res.Actor.UserID || j.Scope.ProjectID != res.Project.ID || j.Scope.ChapterID != res.Chapter.ID {
		return nil, core.ErrNotFound()
	}
	return j, nil
}

// generateCover queues candidate covers: 202 with the new job, or 200 with
// existing:true when the user's identical request is already queued or running.
func (m *Module) generateCover(w http.ResponseWriter, r *http.Request) error {
	res, err := m.coverAccess(r)
	if err != nil {
		return err
	}
	var in struct {
		RoutingMode string `json:"routing_mode"`
	}
	if err := decode(w, r, &in); err != nil {
		return err
	}
	j, err := m.covers.EnqueueCover(r.Context(), res.Actor, gen.CoverRequest{ProjectID: res.Project.ID, ChapterID: res.Chapter.ID, Variations: 4, RoutingMode: in.RoutingMode})
	if err != nil {
		return coverError(r, err)
	}
	if j.Deduplicated {
		core.JSON(w, 200, map[string]any{"job": m.jobSummary(r.Context(), j), "existing": true})
		return nil
	}
	core.JSON(w, 202, map[string]any{"job": m.jobSummary(r.Context(), j)})
	return nil
}

// coverJob reports a cover job and, once it is done, its candidates.
func (m *Module) coverJob(w http.ResponseWriter, r *http.Request) error {
	res, err := m.resource(r, false)
	if err != nil {
		return mapped(r, err)
	}
	j, err := m.coverJobOf(r, res)
	if err != nil {
		return err
	}
	result, err := m.covers.CoverCandidates(r.Context(), res.Actor, j.ID)
	if err != nil {
		return coverError(r, err)
	}
	w.Header().Set("Cache-Control", "no-store")
	core.JSON(w, 200, map[string]any{"job": m.jobSummary(r.Context(), j), "candidates": result.Candidates, "kept_index": result.KeptIndex})
	return nil
}

// keepCover is the human choice of one candidate as the project cover.
func (m *Module) keepCover(w http.ResponseWriter, r *http.Request) error {
	res, err := m.coverAccess(r)
	if err != nil {
		return err
	}
	j, err := m.coverJobOf(r, res)
	if err != nil {
		return err
	}
	var in struct {
		Index int `json:"index"`
	}
	if err := decode(w, r, &in); err != nil {
		return err
	}
	if in.Index < 0 || in.Index > 3 {
		return coverError(r, studio.ErrInvalid)
	}
	kept, err := m.covers.KeepCover(r.Context(), res.Actor, j.ID, in.Index)
	if err != nil {
		return coverError(r, err)
	}
	core.JSON(w, 200, map[string]any{"cover_asset_id": kept, "url": m.assets.URLFor(r.Context(), kept, 15*time.Minute), "kept_index": in.Index})
	return nil
}
