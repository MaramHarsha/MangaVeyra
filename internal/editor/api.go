package editor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/ai"
	"github.com/MaramHarsha/MangaVeyra/internal/assets"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/gen"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/lettering"
	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
	"github.com/MaramHarsha/MangaVeyra/internal/pagestore"
	"github.com/MaramHarsha/MangaVeyra/internal/storage"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
)

type permissions struct {
	Art      bool `json:"art"`
	Letter   bool `json:"letter"`
	Generate bool `json:"generate"`
	Lock     bool `json:"lock"`
}
type layoutView struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Panels    int               `json:"panels"`
	Points    [][]pagedoc.Point `json:"points"`
	Suggested bool              `json:"suggested"`
	// Rank orders suggested layouts, best fit first (0 when not suggested).
	Rank int `json:"rank,omitempty"`
}
type pageResponse struct {
	Page      pagedoc.Page             `json:"page"`
	Lettering []lettering.BubbleLayout `json:"lettering"`
}
type bootstrapData struct {
	PosePresets map[string]pagedoc.Pose `json:"pose_presets"`
	Project     studio.Project          `json:"project"`
	Chapter     studio.Chapter          `json:"chapter"`
	Pages       []pagestore.PageSummary `json:"pages"`
	Characters  []studio.Character      `json:"characters"`
	Layouts     []layoutView            `json:"layouts"`
	Permissions permissions             `json:"permissions"`
	Current     *pageResponse           `json:"current,omitempty"`
	API         string                  `json:"api"`
	Lang        string                  `json:"lang"`
	Messages    map[string]string       `json:"messages"`
	// Chapters feed the chapter switcher in the top bar.
	Chapters []chapterLink `json:"chapters"`
	// Styles are the project art styles with their localised labels.
	Styles []option `json:"styles"`
	// Print holds the localised footer labels and the trim size in mm.
	Print printInfo `json:"print"`
	// Compute is the live queue and worker state (top-bar meter, footer).
	Compute computeView `json:"compute"`
	// Beats summarise the script panels per page number (D2 hint).
	Beats map[int]beat `json:"beats"`
	// ScriptActions maps script panels to their action text (short titles).
	ScriptActions map[string]string `json:"script_actions"`
}
type resource struct {
	Actor   studio.Actor
	Project studio.Project
	Chapter studio.Chapter
	Page    pagedoc.Page
}

func actor(r *http.Request) (studio.Actor, error) {
	id := core.IdentityFrom(r.Context())
	if id.User == nil || id.SessionID == "" || id.Token != nil {
		return studio.Actor{}, studio.ErrForbidden
	}
	return studio.Actor{UserID: id.User.ID, SiteRole: id.User.Role}, nil
}
func (m *Module) resource(r *http.Request, withPage bool) (resource, error) {
	a, err := actor(r)
	if err != nil {
		return resource{}, err
	}
	out := resource{Actor: a}
	projectID := r.PathValue("project")
	if !db.ValidID(projectID) {
		return out, studio.ErrNotFound
	}
	if err := m.studio.Authorize(r.Context(), a, projectID, studio.ActRead); err != nil {
		return out, err
	}
	out.Project, err = m.studio.GetProject(r.Context(), projectID)
	if err != nil {
		return out, err
	}
	chapter := r.PathValue("chapter")
	if db.ValidID(chapter) {
		out.Chapter, err = m.studio.GetChapter(r.Context(), chapter)
	} else {
		chapters, e := m.studio.ListChapters(r.Context(), projectID)
		err = e
		for _, c := range chapters {
			if strconv.Itoa(c.Number) == chapter {
				out.Chapter = c
				break
			}
		}
	}
	if err != nil {
		return out, err
	}
	if out.Chapter.ID == "" || out.Chapter.ProjectID != projectID {
		return out, studio.ErrNotFound
	}
	if withPage {
		pageID := r.PathValue("page")
		if !db.ValidID(pageID) {
			return out, studio.ErrNotFound
		}
		out.Page, err = m.pages.GetPage(r.Context(), pageID)
		if err != nil {
			return out, err
		}
		if out.Page.ProjectID != projectID || out.Page.ChapterID != out.Chapter.ID {
			return out, studio.ErrNotFound
		}
	}
	return out, nil
}
func (m *Module) authorize(r *http.Request, res resource, action studio.Action) error {
	return m.studio.Authorize(r.Context(), res.Actor, res.Project.ID, action)
}
func change(res resource, action string, version int) pagestore.Change {
	return pagestore.Change{ActorKind: "user", ActorID: res.Actor.UserID, Action: action, Summary: action, ExpectedVersion: version}
}
func decode(w http.ResponseWriter, r *http.Request, out any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		var max *http.MaxBytesError
		if errors.As(err, &max) {
			return core.NewError(413, "too_large", i18n.T(r.Context(), "editor.error.invalid"))
		}
		return core.ErrValidation(map[string]string{"document": i18n.T(r.Context(), "editor.error.invalid")})
	}
	if d.Decode(new(any)) != io.EOF {
		return core.ErrBadRequest(i18n.T(r.Context(), "editor.error.invalid"))
	}
	return nil
}
func mapped(r *http.Request, err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, studio.ErrNotFound), errors.Is(err, studio.ErrForbidden), errors.Is(err, pagestore.ErrNotFound), errors.Is(err, jobs.ErrNotFound):
		return core.ErrNotFound()
	case errors.Is(err, pagestore.ErrVersion), errors.Is(err, studio.ErrConflict):
		return core.ErrConflict(i18n.T(r.Context(), "editor.error.conflict"))
	case errors.Is(err, pagestore.ErrLocked), errors.Is(err, studio.ErrLocked):
		return core.ErrLocked(i18n.T(r.Context(), "editor.error.locked"))
	case errors.Is(err, pagestore.ErrInvalid), errors.Is(err, studio.ErrInvalid):
		return core.ErrValidation(map[string]string{"document": i18n.T(r.Context(), "editor.error.invalid")})
	case errors.Is(err, gen.ErrDeferredFeature):
		return core.ErrConflict(i18n.T(r.Context(), "editor.error.unavailable"))
	}
	// The image router's refusals (no eligible model, spend limit, an image
	// too small to edit) carry a safe message; they are not server faults.
	var refused *ai.GenerateError
	if errors.As(err, &refused) && refused.SafeMessage != "" {
		return core.NewError(422, "generation_refused", refused.SafeMessage)
	}
	return err
}
func (m *Module) response(ctx context.Context, p pagedoc.Page, lang string) (pageResponse, error) {
	id := core.IdentityFrom(ctx)
	if id.User != nil {
		if err := m.control.WithLockScores(ctx, studio.Actor{UserID: id.User.ID}, &p); err != nil {
			return pageResponse{}, err
		}
	}
	layouts, err := m.letter.LayoutPage(p, lang)
	if err != nil {
		return pageResponse{}, err
	}
	m.pages.WithURLs(ctx, &p, 15*time.Minute)
	return pageResponse{Page: p, Lettering: layouts}, nil
}
func (m *Module) data(r *http.Request, res resource) (bootstrapData, error) {
	ctx := r.Context()
	v := bootstrapData{Project: res.Project, Chapter: res.Chapter, Lang: i18n.LocaleFrom(ctx), Messages: editorMessages(ctx), API: fmt.Sprintf("/api/v1/editor/p/%s/chapters/%s", res.Project.ID, res.Chapter.ID)}
	var err error
	v.Pages, err = m.pages.ListPages(ctx, res.Chapter.ID)
	if err != nil {
		return v, err
	}
	v.Characters, err = m.studio.ListCharacters(ctx, res.Project.ID)
	if err != nil {
		return v, err
	}
	for action, dst := range map[studio.Action]*bool{studio.ActEditArt: &v.Permissions.Art, studio.ActLetter: &v.Permissions.Letter, studio.ActGenerate: &v.Permissions.Generate, studio.ActLock: &v.Permissions.Lock} {
		e := m.studio.Authorize(ctx, res.Actor, res.Project.ID, action)
		if e != nil && !errors.Is(e, studio.ErrForbidden) {
			return v, e
		}
		*dst = e == nil
	}
	selected := r.URL.Query().Get("page")
	for _, s := range v.Pages {
		if selected == s.ID || selected == strconv.Itoa(s.Number) || (selected == "" && v.Current == nil) {
			p, e := m.pages.GetPage(ctx, s.ID)
			if e != nil {
				return v, e
			}
			pRes, e := m.response(ctx, p, res.Project.Language)
			if e != nil {
				return v, e
			}
			v.Current = &pRes
			if selected == "" {
				break
			}
		}
	}
	if selected != "" && v.Current == nil {
		return v, studio.ErrNotFound
	}
	chapters, err := m.studio.ListChapters(ctx, res.Project.ID)
	if err != nil {
		return v, err
	}
	v.Chapters = chapterLinks(chapters, res.Project.ID)
	v.Styles = styles(ctx, res.Project.Style)
	v.Print = printFor(ctx, res.Project)
	v.Compute = m.compute(r)
	v.PosePresets = pagedoc.PosePresets()
	v.Beats = m.beats(ctx, res.Chapter.ID)
	v.ScriptActions = m.scriptActions(ctx, res.Chapter.ID)
	panels, number := 0, 0
	if v.Current != nil {
		panels, number = len(v.Current.Page.Panels), v.Current.Page.Number
	}
	b, ok := v.Beats[number]
	v.Layouts = layouts(ctx, res.Project, panelTarget(b, ok, panels))
	return v, nil
}
func (m *Module) bootstrap(w http.ResponseWriter, r *http.Request) error {
	res, err := m.resource(r, false)
	if err != nil {
		return mapped(r, err)
	}
	v, err := m.data(r, res)
	if err != nil {
		return mapped(r, err)
	}
	w.Header().Set("Cache-Control", "no-store")
	core.JSON(w, 200, v)
	return nil
}
func (m *Module) getPage(w http.ResponseWriter, r *http.Request) error {
	res, err := m.resource(r, true)
	if err != nil {
		return mapped(r, err)
	}
	v, err := m.response(r.Context(), res.Page, res.Project.Language)
	if err != nil {
		return mapped(r, err)
	}
	w.Header().Set("Cache-Control", "no-store")
	core.JSON(w, 200, v)
	return nil
}

func (m *Module) savePage(w http.ResponseWriter, r *http.Request) error {
	res, err := m.resource(r, true)
	if err != nil {
		return mapped(r, err)
	}
	var in struct {
		Page        pagedoc.Page `json:"page"`
		BaseVersion int          `json:"base_version"`
	}
	if err := decode(w, r, &in); err != nil {
		return err
	}
	if in.Page.ID != res.Page.ID || in.Page.ProjectID != res.Project.ID || in.Page.ChapterID != res.Chapter.ID {
		return mapped(r, studio.ErrInvalid)
	}
	if err := safeDocument(&in.Page, res.Page); err != nil {
		return mapped(r, err)
	}
	p, err := m.pages.SavePage(r.Context(), in.Page, in.BaseVersion, change(res, "save", in.BaseVersion))
	if err != nil {
		return mapped(r, err)
	}
	out, err := m.response(r.Context(), p, res.Project.Language)
	if err != nil {
		return mapped(r, err)
	}
	core.JSON(w, 200, out)
	return nil
}

// Art attachments and generation provenance are written by their own fenced
// services. A generic document save cannot forge them or rasterize lettering.
func safeDocument(p *pagedoc.Page, old pagedoc.Page) error {
	for i := range p.Panels {
		pn := &p.Panels[i]
		op := old.Panel(pn.ID)
		for j := range pn.Layers {
			l := &pn.Layers[j]
			var ol *pagedoc.Layer
			if op != nil {
				ol = op.Layer(l.ID)
			}
			if ol == nil {
				if l.Asset != nil || l.Mask != nil {
					return studio.ErrInvalid
				}
				l.Provenance = pagedoc.ProvenanceHuman
				l.ModelID = ""
				l.ProviderID = ""
				l.JobID = ""
				l.Seed = nil
				l.LockScore = nil
			} else {
				if assetID(l.Asset) != assetID(ol.Asset) || assetID(l.Mask) != assetID(ol.Mask) {
					return studio.ErrInvalid
				}
				l.Provenance = ol.Provenance
				l.ModelID = ol.ModelID
				l.ProviderID = ol.ProviderID
				l.JobID = ol.JobID
				l.Seed = ol.Seed
				l.LockScore = ol.LockScore
			}
		}
	}
	for i := range p.Bubbles {
		b := &p.Bubbles[i]
		ob := old.Bubble(b.ID)
		for lang, text := range b.Texts {
			var previous pagedoc.BubbleText
			if ob != nil {
				previous = ob.Texts[lang]
			}
			if ob != nil && text.Text == previous.Text && reflect.DeepEqual(text.Ruby, previous.Ruby) {
				text.Source = previous.Source
				text.Status = previous.Status
			} else {
				text.Source = pagedoc.ProvenanceHuman
				text.Status = "draft"
			}
			b.Texts[lang] = text
		}
	}
	if p.Validate() != nil {
		return studio.ErrInvalid
	}
	return nil
}
func assetID(a *pagedoc.AssetRef) string {
	if a == nil {
		return ""
	}
	return a.ID
}

func (m *Module) createPage(w http.ResponseWriter, r *http.Request) error {
	res, err := m.resource(r, false)
	if err != nil {
		return mapped(r, err)
	}
	if err := m.authorize(r, res, studio.ActEditArt); err != nil {
		return mapped(r, err)
	}
	var in struct {
		LayoutID string `json:"layout_id"`
	}
	if err := decode(w, r, &in); err != nil {
		return err
	}
	p := pagedoc.New(db.NewID(), res.Project.ID, res.Chapter.ID, 0, res.Project.PageWidth, res.Project.PageHeight, pagedoc.DirectionForFormat(res.Project.Format))
	if in.LayoutID != "" {
		l, ok := pagedoc.LayoutByID(in.LayoutID)
		if !ok {
			return mapped(r, studio.ErrInvalid)
		}
		p.LayoutID = l.ID
		for i, points := range l.PanelPoints(p.Width, p.Height, p.Direction) {
			p.Panels = append(p.Panels, pagedoc.Panel{ID: db.NewID(), Order: i, Points: points, PromptSource: "user", Layers: []pagedoc.Layer{{ID: db.NewID(), Kind: pagedoc.LayerBackground, Visible: true, Opacity: 1, Provenance: pagedoc.ProvenanceHuman}, {ID: db.NewID(), Kind: pagedoc.LayerLettering, Visible: true, Opacity: 1, Provenance: pagedoc.ProvenanceHuman}}})
		}
	}
	var made []pagedoc.Page
	err = m.d.DB.WithTx(r.Context(), func(tx *db.Tx) error {
		var err error
		made, err = m.pages.CreatePagesTx(r.Context(), tx, res.Chapter.ID, []pagedoc.Page{p}, change(res, "create_page", 0))
		return err
	})
	if err != nil {
		return mapped(r, err)
	}
	out, err := m.response(r.Context(), made[0], res.Project.Language)
	if err != nil {
		return mapped(r, err)
	}
	core.JSON(w, 201, out)
	return nil
}
func (m *Module) reorderPages(w http.ResponseWriter, r *http.Request) error {
	res, err := m.resource(r, false)
	if err != nil {
		return mapped(r, err)
	}
	if err := m.authorize(r, res, studio.ActEditArt); err != nil {
		return mapped(r, err)
	}
	var in struct {
		IDs []string `json:"ids"`
	}
	if err := decode(w, r, &in); err != nil {
		return err
	}
	if len(in.IDs) > 256 {
		return mapped(r, studio.ErrInvalid)
	}
	if err := m.pages.ReorderPages(r.Context(), res.Chapter.ID, in.IDs, change(res, "reorder_pages", 0)); err != nil {
		return mapped(r, err)
	}
	list, err := m.pages.ListPages(r.Context(), res.Chapter.ID)
	if err != nil {
		return mapped(r, err)
	}
	core.JSON(w, 200, map[string]any{"pages": list})
	return nil
}
func (m *Module) deletePage(w http.ResponseWriter, r *http.Request) error {
	res, err := m.resource(r, true)
	if err != nil {
		return mapped(r, err)
	}
	var in struct {
		BaseVersion int  `json:"base_version"`
		Confirm     bool `json:"confirm"`
	}
	if err := decode(w, r, &in); err != nil {
		return err
	}
	if !in.Confirm || in.BaseVersion <= 0 {
		return mapped(r, studio.ErrInvalid)
	}
	if err := m.authorize(r, res, studio.ActEditArt); err != nil {
		return mapped(r, err)
	}
	if err := m.pages.DeletePage(r.Context(), res.Page.ID, change(res, "delete_page", in.BaseVersion)); err != nil {
		return mapped(r, err)
	}
	core.JSON(w, 200, map[string]bool{"deleted": true})
	return nil
}
func (m *Module) setLock(w http.ResponseWriter, r *http.Request) error {
	res, err := m.resource(r, true)
	if err != nil {
		return mapped(r, err)
	}
	var in struct {
		PanelID     string `json:"panel_id"`
		LayerID     string `json:"layer_id"`
		Locked      bool   `json:"locked"`
		Confirm     bool   `json:"confirm"`
		BaseVersion int    `json:"base_version"`
	}
	if err := decode(w, r, &in); err != nil {
		return err
	}
	if (!in.Locked && !in.Confirm) || in.BaseVersion <= 0 {
		return mapped(r, studio.ErrInvalid)
	}
	if err := m.authorize(r, res, studio.ActLock); err != nil {
		return mapped(r, err)
	}
	if err := m.pages.SetLock(r.Context(), res.Page.ID, in.PanelID, in.LayerID, in.Locked, change(res, "lock", in.BaseVersion)); err != nil {
		return mapped(r, err)
	}
	return m.getPage(w, r)
}
func (m *Module) history(w http.ResponseWriter, r *http.Request) error {
	res, err := m.resource(r, true)
	if err != nil {
		return mapped(r, err)
	}
	items, err := m.pages.Versions(r.Context(), res.Page.ID, 50)
	if err != nil {
		return mapped(r, err)
	}
	core.JSON(w, 200, map[string]any{"versions": items})
	return nil
}
func (m *Module) revert(w http.ResponseWriter, r *http.Request) error {
	res, err := m.resource(r, true)
	if err != nil {
		return mapped(r, err)
	}
	var in struct {
		VersionID   string `json:"version_id"`
		BaseVersion int    `json:"base_version"`
		Confirm     bool   `json:"confirm"`
	}
	if err := decode(w, r, &in); err != nil {
		return err
	}
	if !in.Confirm || in.BaseVersion <= 0 || !db.ValidID(in.VersionID) {
		return mapped(r, studio.ErrInvalid)
	}
	p, err := m.pages.Revert(r.Context(), res.Page.ID, in.VersionID, change(res, "revert", in.BaseVersion))
	if err != nil {
		return mapped(r, err)
	}
	out, err := m.response(r.Context(), p, res.Project.Language)
	if err != nil {
		return err
	}
	core.JSON(w, 200, out)
	return nil
}

// abandoned answers a lettering preview whose request context ended (the
// client went away): there is nobody to answer, so it is reported as
// canceled (499, nothing written, not logged as a server error).
func abandoned(r *http.Request) error {
	return fmt.Errorf("editor: lettering preview abandoned: %w: %w", context.Canceled, r.Context().Err())
}

// layoutLettering previews the lettering of an unsaved page draft. Only
// members who can change the page (art or lettering) preview drafts: the
// editor asks for a preview after their edits, and read-only members only
// ever see saved pages. The layout stops when the request is abandoned (the
// editor aborts a preview superseded by the next keystroke).
func (m *Module) layoutLettering(w http.ResponseWriter, r *http.Request) error {
	res, err := m.resource(r, true)
	if err != nil {
		return mapped(r, err)
	}
	if err := m.authorize(r, res, studio.ActLetter); errors.Is(err, studio.ErrForbidden) {
		if err := m.authorize(r, res, studio.ActEditArt); err != nil {
			return mapped(r, err)
		}
	} else if err != nil {
		return mapped(r, err)
	}
	var in struct {
		Page   pagedoc.Page `json:"page"`
		Reflow bool         `json:"reflow"`
	}
	if err := decode(w, r, &in); err != nil {
		return err
	}
	if in.Page.ID != res.Page.ID || in.Page.ProjectID != res.Project.ID || in.Page.ChapterID != res.Chapter.ID || in.Page.Validate() != nil {
		return mapped(r, studio.ErrInvalid)
	}
	if in.Reflow {
		if err := m.authorize(r, res, studio.ActLetter); err != nil {
			return mapped(r, err)
		}
		for _, pn := range in.Page.Panels {
			if err := r.Context().Err(); err != nil {
				return abandoned(r)
			}
			lines := []lettering.Line{}
			indices := []int{}
			for i, b := range in.Page.Bubbles {
				if b.PanelID == pn.ID {
					lines = append(lines, lettering.Line{Kind: b.Kind, Text: b.Text(res.Project.Language).Text, CharacterID: b.SpeakerCharacterID})
					indices = append(indices, i)
				}
			}
			if len(lines) == 0 {
				continue
			}
			bubbles, err := m.letter.AutoPlace(in.Page, pn.ID, lines, res.Project.Language, pagedoc.VerticalByDefault(res.Project.Language))
			if err != nil {
				return mapped(r, studio.ErrInvalid)
			}
			for i, b := range bubbles {
				if i < len(indices) {
					in.Page.Bubbles[indices[i]].Box = b.Box
					in.Page.Bubbles[indices[i]].Tail = b.Tail
				}
			}
		}
	}
	l, err := lettering.LayoutPageContext(r.Context(), m.letter, in.Page, res.Project.Language)
	if err != nil {
		if r.Context().Err() != nil {
			return abandoned(r)
		}
		return mapped(r, studio.ErrInvalid)
	}
	core.JSON(w, 200, map[string]any{"lettering": l, "bubbles": in.Page.Bubbles, "reading_order": m.letter.ReadingOrder(in.Page)})
	return nil
}

func (m *Module) generate(w http.ResponseWriter, r *http.Request) error {
	res, err := m.resource(r, true)
	if err != nil {
		return mapped(r, err)
	}
	if err := m.authorize(r, res, studio.ActGenerate); err != nil {
		return mapped(r, err)
	}
	var in struct {
		Kind        string `json:"kind"`
		PanelID     string `json:"panel_id"`
		LayerID     string `json:"layer_id"`
		MaskID      string `json:"mask_id"`
		Prompt      string `json:"prompt"`
		BaseVersion int    `json:"base_version"`
		RoutingMode string `json:"routing_mode"`
	}
	if err := decode(w, r, &in); err != nil {
		return err
	}
	if in.BaseVersion != res.Page.Version {
		return mapped(r, pagestore.ErrVersion)
	}
	pn := res.Page.Panel(in.PanelID)
	if pn == nil {
		return mapped(r, studio.ErrInvalid)
	}
	if in.LayerID != "" {
		l := pn.Layer(in.LayerID)
		if l == nil || l.Kind == pagedoc.LayerLettering {
			return mapped(r, studio.ErrInvalid)
		}
	}
	switch in.Kind {
	case "panel":
		in.LayerID = ""
	case "layer":
		if in.LayerID == "" {
			return mapped(r, studio.ErrInvalid)
		}
	case "region":
		if in.LayerID == "" || !db.ValidID(in.MaskID) {
			return mapped(r, studio.ErrInvalid)
		}
	default:
		return mapped(r, studio.ErrInvalid)
	}
	// A double click, a retried request or the same request sent to another
	// server process receives the user's identical active job: Gen keys the
	// enqueue (user, page, panel/layer, mask content, prompt, routing mode)
	// and the database stores at most one active job per key.
	var j *jobs.Job
	if in.Kind == "region" {
		j, err = m.gen.EnqueueRegionEdit(r.Context(), res.Actor, gen.RegionEditRequest{ProjectID: res.Project.ID, LayerID: in.LayerID, MaskAssetID: in.MaskID, Prompt: in.Prompt, Variations: 4, RoutingMode: in.RoutingMode, ExpectedVersion: in.BaseVersion})
	} else {
		j, err = m.gen.EnqueuePanel(r.Context(), res.Actor, gen.PanelRequest{ProjectID: res.Project.ID, PageID: res.Page.ID, PanelID: in.PanelID, LayerID: in.LayerID, Variations: 4, Prompt: in.Prompt, RoutingMode: in.RoutingMode, ExpectedVersion: in.BaseVersion})
	}
	if err != nil {
		return mapped(r, err)
	}
	if j.Deduplicated {
		core.JSON(w, 200, map[string]any{"job": m.jobSummary(r.Context(), j), "existing": true})
		return nil
	}
	core.JSON(w, 202, map[string]any{"job": m.jobSummary(r.Context(), j)})
	return nil
}

func (m *Module) uploadMask(w http.ResponseWriter, r *http.Request) error {
	res, err := m.resource(r, true)
	if err != nil {
		return mapped(r, err)
	}
	if err := m.authorize(r, res, studio.ActGenerate); err != nil {
		return mapped(r, err)
	}
	r.Body = http.MaxBytesReader(w, r.Body, 17<<20)
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		return mapped(r, studio.ErrInvalid)
	}
	defer r.MultipartForm.RemoveAll()
	layerID := strings.TrimSpace(r.FormValue("layer_id"))
	version, err := strconv.Atoi(r.FormValue("base_version"))
	if err != nil || version != res.Page.Version {
		return mapped(r, pagestore.ErrVersion)
	}
	var target *pagedoc.Layer
	targetPanelID := ""
	for _, pn := range res.Page.Panels {
		if l := pn.Layer(layerID); l != nil {
			target = l
			targetPanelID = pn.ID
		}
	}
	if target == nil || target.Kind == pagedoc.LayerLettering || target.Asset == nil {
		return mapped(r, studio.ErrInvalid)
	}
	f, h, err := r.FormFile("file")
	if err != nil {
		return mapped(r, studio.ErrInvalid)
	}
	defer f.Close()
	if h.Size <= 0 || h.Size > 16<<20 {
		return mapped(r, studio.ErrInvalid)
	}
	data, err := io.ReadAll(io.LimitReader(f, (16<<20)+1))
	if err != nil || len(data) > 16<<20 || storage.DetectContentType(data) != "image/png" {
		return mapped(r, studio.ErrInvalid)
	}
	img, format, err := storage.DecodeImage(data, storage.DefaultImageLimits)
	if err != nil || format != "png" || img.Bounds().Dx() != target.Asset.Width || img.Bounds().Dy() != target.Asset.Height {
		return mapped(r, studio.ErrInvalid)
	}
	staged, err := m.assets.Stage(r.Context(), assets.SaveParams{OwnerID: res.Actor.UserID, ProjectID: res.Project.ID, Kind: "region_mask", Source: assets.SourceUpload, ContentType: "image/png", Data: data})
	if err != nil {
		return err
	}
	defer m.assets.DiscardStaged(context.WithoutCancel(r.Context()), staged)
	var stored assets.Asset
	err = m.d.DB.WithTx(r.Context(), func(tx *db.Tx) error {
		if err := m.studio.AuthorizeTx(r.Context(), tx, res.Actor, res.Project.ID, studio.ActGenerate); err != nil {
			return err
		}
		current, err := m.pages.GetPageTx(r.Context(), tx, res.Page.ID)
		if err != nil {
			return err
		}
		if current.Version != version {
			return pagestore.ErrVersion
		}
		if err := m.studio.CheckScopeWritableTx(r.Context(), tx, jobs.Scope{Kind: jobs.ScopeLayer, ProjectID: res.Project.ID, PanelID: targetPanelID, LayerID: layerID}); err != nil {
			return err
		}
		stored, err = m.assets.CommitStagedTx(r.Context(), tx, staged)
		return err
	})
	if err != nil {
		return mapped(r, err)
	}
	core.JSON(w, 201, map[string]string{"id": stored.ID})
	return nil
}

func (m *Module) ownedJob(r *http.Request, res resource) (*jobs.Job, error) {
	id := r.PathValue("job")
	if !db.ValidID(id) {
		return nil, studio.ErrNotFound
	}
	j, err := m.d.Jobs.Get(r.Context(), id)
	if err != nil || j.UserID != res.Actor.UserID || j.Scope.ProjectID != res.Project.ID {
		return nil, studio.ErrNotFound
	}
	var payload struct {
		Request json.RawMessage `json:"request"`
	}
	if j.DecodeInput(&payload) != nil {
		return nil, studio.ErrNotFound
	}
	if j.Type == gen.JobPanel {
		var req gen.PanelRequest
		if json.Unmarshal(payload.Request, &req) != nil || req.ProjectID != res.Project.ID || req.PageID != res.Page.ID || res.Page.Panel(req.PanelID) == nil {
			return nil, studio.ErrNotFound
		}
	}
	if j.Type == gen.JobRegionEdit {
		var req gen.RegionEditRequest
		if json.Unmarshal(payload.Request, &req) != nil || req.ProjectID != res.Project.ID {
			return nil, studio.ErrNotFound
		}
		found := false
		for _, pn := range res.Page.Panels {
			if pn.Layer(req.LayerID) != nil {
				found = true
			}
		}
		if !found {
			return nil, studio.ErrNotFound
		}
	}
	if j.Type == gen.JobRepair && j.Scope.ChapterID == res.Chapter.ID {
		return j, nil
	}
	if j.Type == gen.JobPage || j.Type == gen.JobRepair {
		var p struct {
			Panels []struct {
				PageID string `json:"page_id"`
			} `json:"panels"`
		}
		if j.DecodeInput(&p) != nil {
			return nil, studio.ErrNotFound
		}
		found := false
		for _, item := range p.Panels {
			if item.PageID == res.Page.ID {
				found = true
			}
		}
		if !found {
			return nil, studio.ErrNotFound
		}
		return j, nil
	}
	if j.Type != gen.JobPanel && j.Type != gen.JobRegionEdit {
		return nil, studio.ErrNotFound
	}
	return j, nil
}
func (m *Module) job(w http.ResponseWriter, r *http.Request) error {
	res, err := m.resource(r, true)
	if err != nil {
		return mapped(r, err)
	}
	j, err := m.ownedJob(r, res)
	if err != nil {
		return mapped(r, err)
	}
	candidates := []gen.Candidate{}
	if j.State == jobs.StateDone && (j.Type == gen.JobPanel || j.Type == gen.JobRegionEdit) {
		candidates, err = m.gen.Candidates(r.Context(), res.Actor, j.ID)
		if err != nil {
			return mapped(r, err)
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	core.JSON(w, 200, map[string]any{"job": m.jobSummary(r.Context(), j), "candidates": candidates})
	return nil
}
func (m *Module) apply(w http.ResponseWriter, r *http.Request) error {
	res, err := m.resource(r, true)
	if err != nil {
		return mapped(r, err)
	}
	j, err := m.ownedJob(r, res)
	if err != nil {
		return mapped(r, err)
	}
	if err := m.authorize(r, res, studio.ActGenerate); err != nil {
		return mapped(r, err)
	}
	var in struct {
		Index int `json:"index"`
	}
	if err := decode(w, r, &in); err != nil {
		return err
	}
	if in.Index < 0 || in.Index > 3 {
		return mapped(r, studio.ErrInvalid)
	}
	if err := m.gen.ApplyVariation(r.Context(), res.Actor, j.ID, in.Index); err != nil {
		return mapped(r, err)
	}
	return m.getPage(w, r)
}
