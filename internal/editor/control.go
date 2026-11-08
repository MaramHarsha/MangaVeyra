package editor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/assets"
	"github.com/MaramHarsha/MangaVeyra/internal/compose"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/gen"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
	"github.com/MaramHarsha/MangaVeyra/internal/storage"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
)

func (m *Module) versionPage(w http.ResponseWriter, r *http.Request) error {
	res, err := m.resource(r, true)
	if err != nil {
		return mapped(r, err)
	}
	if !db.ValidID(r.PathValue("version")) {
		return core.ErrNotFound()
	}
	p, err := m.pages.VersionDoc(r.Context(), r.PathValue("version"))
	if err != nil {
		return mapped(r, err)
	}
	if p.ID != res.Page.ID || p.ProjectID != res.Project.ID || p.ChapterID != res.Chapter.ID {
		return core.ErrNotFound()
	}
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Query().Get("image") == "1" {
		return m.proofPNG(w, r, p, res.Project.Language)
	}
	out, err := m.response(r.Context(), p, res.Project.Language)
	if err != nil {
		return mapped(r, err)
	}
	core.JSON(w, 200, out)
	return nil
}
func (m *Module) pageProof(w http.ResponseWriter, r *http.Request) error {
	res, err := m.resource(r, true)
	if err != nil {
		return mapped(r, err)
	}
	w.Header().Set("Cache-Control", "no-store")
	return m.proofPNG(w, r, res.Page, res.Project.Language)
}
func (m *Module) proofPNG(w http.ResponseWriter, r *http.Request, p pagedoc.Page, lang string) error {
	renderer := core.MustUse[compose.Renderer](m.d)
	pixels, err := renderer.PNG(r.Context(), p, compose.Options{Lang: lang, Scale: math.Min(1, 640/float64(p.Width))})
	if err != nil {
		return mapped(r, err)
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, err = w.Write(pixels)
	return err
}
func (m *Module) pageHistory(w http.ResponseWriter, r *http.Request) error {
	res, err := m.resource(r, true)
	if err != nil {
		return mapped(r, err)
	}
	core.Redirect(w, r, fmt.Sprintf("/studio/p/%s/chapters/%s/edit?page=%d&panel=history", res.Project.ID, res.Chapter.ID, res.Page.Number))
	return nil
}
func (m *Module) generatePage(w http.ResponseWriter, r *http.Request) error {
	res, err := m.resource(r, true)
	if err != nil {
		return mapped(r, err)
	}
	if err := m.authorize(r, res, studio.ActGenerate); err != nil {
		return mapped(r, err)
	}
	if err := m.authorize(r, res, studio.ActEditArt); err != nil {
		return mapped(r, err)
	}
	var in struct {
		BaseVersion   int     `json:"base_version"`
		SketchAssetID string  `json:"sketch_asset_id"`
		Strength      float64 `json:"strength"`
		Finish        string  `json:"finish"`
	}
	if err := decode(w, r, &in); err != nil {
		return err
	}
	if in.SketchAssetID != "" && !db.ValidID(in.SketchAssetID) {
		return sketchInvalid(r)
	}
	// Serialize interactive generation per page and reuse the exact active
	// coordinator. The service still fences every target inside its transaction.
	release := m.generating.lock(res.Actor.UserID + "/" + res.Page.ID)
	defer release()
	existing, err := m.d.Jobs.List(r.Context(), jobs.Filter{ProjectID: res.Project.ID, UserID: res.Actor.UserID, Types: []string{gen.JobPage}, States: []string{jobs.StateQueued, jobs.StateRunning}, Limit: 100})
	if err != nil {
		return err
	}
	if in.BaseVersion != res.Page.Version {
		return mapped(r, studio.ErrConflict)
	}
	for _, j := range existing {
		var p struct {
			Request gen.PageRequest `json:"request"`
		}
		if j.DecodeInput(&p) == nil && p.Request.PageID == res.Page.ID && !j.CancelRequested {
			strength := in.Strength
			if strength == 0 && in.SketchAssetID != "" {
				strength = .62
			}
			if p.Request.SketchAssetID != in.SketchAssetID || p.Request.Strength != strength || p.Request.Finish != in.Finish {
				return mapped(r, studio.ErrConflict)
			}
			core.JSON(w, 200, map[string]any{"job": m.jobSummary(r.Context(), j), "existing": true})
			return nil
		}
	}

	j, err := m.control.EnqueuePage(r.Context(), res.Actor, gen.PageRequest{ProjectID: res.Project.ID, PageID: res.Page.ID, ExpectedVersion: in.BaseVersion, SketchAssetID: in.SketchAssetID, Strength: in.Strength, Finish: in.Finish})
	if in.SketchAssetID != "" && errors.Is(err, assets.ErrNotFound) {
		return sketchInvalid(r)
	}
	if err != nil {
		return mapped(r, err)
	}
	core.JSON(w, 202, map[string]any{"job": m.jobSummary(r.Context(), j)})
	return nil
}
func (m *Module) uploadSketch(w http.ResponseWriter, r *http.Request) error {
	res, err := m.resource(r, true)
	if err != nil {
		return mapped(r, err)
	}
	if err := m.authorize(r, res, studio.ActEditArt); err != nil {
		return mapped(r, err)
	}
	if err := m.authorize(r, res, studio.ActGenerate); err != nil {
		return mapped(r, err)
	}
	r.Body = http.MaxBytesReader(w, r.Body, 17<<20)
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		return sketchInvalid(r)
	}
	defer r.MultipartForm.RemoveAll()
	version, err := strconv.Atoi(r.FormValue("base_version"))
	if err != nil || version != res.Page.Version {
		return mapped(r, studio.ErrConflict)
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		return sketchInvalid(r)
	}
	defer file.Close()
	if header.Size < 1 || header.Size > 16<<20 {
		return sketchInvalid(r)
	}
	data, err := io.ReadAll(io.LimitReader(file, (16<<20)+1))
	if err != nil || len(data) > 16<<20 {
		return sketchInvalid(r)
	}
	img, _, err := storage.DecodeImage(data, storage.ImageLimits{MaxWidth: 4096, MaxHeight: 4096, MaxPixels: 16 << 20})
	if err != nil || img.Bounds().Dx() < 64 || img.Bounds().Dy() < 64 {
		return sketchInvalid(r)
	}
	staged, err := m.assets.Stage(r.Context(), assets.SaveParams{OwnerID: res.Actor.UserID, ProjectID: res.Project.ID, Kind: "sketch", Source: assets.SourceUpload, Data: data, ContentType: storage.DetectContentType(data)})
	if err != nil {
		return err
	}
	defer m.assets.DiscardStaged(context.WithoutCancel(r.Context()), staged)
	var a assets.Asset
	err = m.d.DB.WithTx(r.Context(), func(tx *db.Tx) error {
		if err := m.studio.AuthorizeTx(r.Context(), tx, res.Actor, res.Project.ID, studio.ActEditArt); err != nil {
			return err
		}
		if err := m.studio.AuthorizeTx(r.Context(), tx, res.Actor, res.Project.ID, studio.ActGenerate); err != nil {
			return err
		}
		p, err := m.pages.GetPageTx(r.Context(), tx, res.Page.ID)
		if err != nil {
			return err
		}
		if p.Version != version {
			return studio.ErrConflict
		}
		if err := m.studio.CheckScopeWritableTx(r.Context(), tx, jobs.Scope{Kind: jobs.ScopePage, ProjectID: res.Project.ID, PageID: p.ID}); err != nil {
			return err
		}
		for _, pn := range p.Panels {
			if pn.Locked {
				return studio.ErrLocked
			}
			for _, l := range pn.Layers {
				if l.Locked && l.Kind != pagedoc.LayerLettering {
					return studio.ErrLocked
				}
			}
		}
		a, err = m.assets.CommitStagedTx(r.Context(), tx, staged)
		return err
	})
	if err != nil {
		return mapped(r, err)
	}
	core.JSON(w, 201, map[string]any{"id": a.ID, "url": m.assets.URL(a, 15*time.Minute), "width": a.Width, "height": a.Height})
	return nil
}
func (m *Module) lockStatus(w http.ResponseWriter, r *http.Request) error {
	res, err := m.resource(r, false)
	if err != nil {
		return mapped(r, err)
	}
	id := r.PathValue("character")
	if !db.ValidID(id) {
		return characterMissing(r)
	}
	status, err := m.control.LockChapterStatus(r.Context(), res.Actor, res.Project.ID, id, res.Chapter.ID)
	if errors.Is(err, studio.ErrNotFound) {
		return characterMissing(r)
	}
	if err != nil {
		return mapped(r, err)
	}
	c, err := m.studio.GetCharacter(r.Context(), id)
	if err != nil {
		return mapped(r, err)
	}
	// D12 is chapter-scoped; the C3 card retains its project-wide summary.
	drift := []gen.DriftPanel{}
	for _, d := range status.Drift {
		if d.ChapterID == res.Chapter.ID {
			drift = append(drift, d)
		}
	}
	status.Drift = drift
	refs := []map[string]string{}
	for _, ref := range c.Refs {
		if len(refs) == 3 {
			break
		}
		refs = append(refs, map[string]string{"id": ref.ID, "url": m.assets.URLFor(r.Context(), ref.AssetID, 15*time.Minute), "kind": ref.Kind})
	}
	w.Header().Set("Cache-Control", "no-store")
	core.JSON(w, 200, map[string]any{"status": status, "references": refs, "name": c.Name})
	return nil
}
func (m *Module) repairCharacter(w http.ResponseWriter, r *http.Request) error {
	res, err := m.resource(r, false)
	if err != nil {
		return mapped(r, err)
	}
	var in struct {
		CharacterID string             `json:"character_id"`
		Targets     []gen.RepairTarget `json:"targets"`
	}
	if err := decode(w, r, &in); err != nil {
		return err
	}
	j, err := m.control.EnqueueRepair(r.Context(), res.Actor, gen.RepairRequest{ProjectID: res.Project.ID, ChapterID: res.Chapter.ID, CharacterID: in.CharacterID, Targets: in.Targets})
	if err != nil {
		return mapped(r, err)
	}
	core.JSON(w, 202, map[string]any{"job": m.jobSummary(r.Context(), j)})
	return nil
}

func (m *Module) controlQuote(w http.ResponseWriter, r *http.Request) error {
	res, err := m.resource(r, true)
	if err != nil {
		return mapped(r, err)
	}
	quote, err := m.control.QuotePage(r.Context(), res.Actor, gen.PageRequest{ProjectID: res.Project.ID, PageID: res.Page.ID, ExpectedVersion: res.Page.Version}, r.URL.Query().Get("kind") == "sketch")
	if err != nil {
		return mapped(r, err)
	}
	w.Header().Set("Cache-Control", "no-store")
	core.JSON(w, 200, map[string]any{"quote": quote, "model_name": m.modelName(r.Context(), quote.ModelID)})
	return nil
}
func (m *Module) repairQuote(w http.ResponseWriter, r *http.Request) error {
	res, err := m.resource(r, false)
	if err != nil {
		return mapped(r, err)
	}
	var in struct {
		CharacterID string             `json:"character_id"`
		Targets     []gen.RepairTarget `json:"targets"`
	}
	if err := decode(w, r, &in); err != nil {
		return err
	}
	quote, err := m.control.QuoteRepair(r.Context(), res.Actor, gen.RepairRequest{ProjectID: res.Project.ID, ChapterID: res.Chapter.ID, CharacterID: in.CharacterID, Targets: in.Targets})
	if err != nil {
		return mapped(r, err)
	}
	w.Header().Set("Cache-Control", "no-store")
	core.JSON(w, 200, map[string]any{"quote": quote, "model_name": m.modelName(r.Context(), quote.ModelID)})
	return nil
}

// sketchInvalid is the one D4 validation message for files and sketch
// references outside the documented limits (ADR 0010).
func sketchInvalid(r *http.Request) error {
	msg := i18n.T(r.Context(), "editor.control.sketch_invalid")
	e := core.NewError(http.StatusUnprocessableEntity, "validation", msg)
	e.Fields = map[string]string{"file": msg}
	return e
}

// characterMissing tells D12 that the requested character is not in this
// project, so it can offer the project's characters instead.
func characterMissing(r *http.Request) error {
	return core.NewError(http.StatusNotFound, "not_found", i18n.T(r.Context(), "editor.control.character_missing"))
}
