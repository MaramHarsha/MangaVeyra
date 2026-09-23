package studio

import (
	"context"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/web"
	"github.com/MaramHarsha/MangaVeyra/internal/web/layout"
)

// Upload limits for reference images.
const (
	maxUploadFile  = 20 << 20
	maxUploadTotal = 90 << 20
	maxUploadFiles = 12
)

// Character roles offered in the create form (role → category).
var CharacterRoles = []string{"lead", "rival", "mentor", "antagonist", "sidekick", "supporting", "extra"}

func categoryOf(role string) string {
	switch role {
	case "lead", "rival", "antagonist":
		return CategoryMain
	case "extra":
		return CategoryExtra
	}
	return CategorySupporting
}

// CharCard is a character as the bible shows it.
type CharCard struct {
	Character
	ArtURL   string
	First    string
	Last     string
	Progress float64
	Href     string
}

func (m *Module) charCards(ctx context.Context, list []Character) []CharCard {
	var ids []string
	for _, c := range list {
		ids = append(ids, c.PortraitAssetID)
	}
	urls := m.svc.AssetURLs(ctx, ids...)
	out := make([]CharCard, 0, len(list))
	for _, c := range list {
		cc := CharCard{Character: c, ArtURL: urls[c.PortraitAssetID], Href: "/studio/p/" + c.ProjectID + "/characters/" + c.ID}
		cc.First, cc.Last = SplitName(c.Name)
		cc.Progress = m.svc.LockProgress(ctx, &c)
		out = append(out, cc)
	}
	return out
}

// CharactersView is C2.
type CharactersView struct {
	Access   *Access
	Project  *Project
	Cards    []CharCard
	Counts   map[string]int
	Locked   int
	Training int
	Filter   string
}

func (m *Module) charactersPage(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	a := AccessFrom(ctx)
	v := CharactersView{Access: a, Project: a.Project, Filter: r.URL.Query().Get("cat")}
	if !slices.Contains(Categories, v.Filter) {
		v.Filter = ""
	}
	all, err := m.svc.Characters(ctx, a.Project.ID, "")
	if err != nil {
		return err
	}
	v.Counts = map[string]int{"": len(all)}
	var shown []Character
	for _, c := range all {
		v.Counts[c.Category]++
		switch c.LockStatus {
		case LockReady:
			v.Locked++
		case LockTraining:
			v.Training++
		}
		if v.Filter == "" || c.Category == v.Filter {
			shown = append(shown, c)
		}
	}
	v.Cards = m.charCards(ctx, shown)
	return m.ui.Page(w, r, http.StatusOK, web.PageOpts{Title: i18n.T(ctx, "studio.chars.title", "title", a.Project.Title),
		MobileTitle: i18n.T(ctx, "nav.characters"), BackURL: "/studio/p/" + a.Project.ID + "/story", Section: layout.SectionStudio,
		Nav: layout.NavCharacters, Tab: layout.TabHome}, CharactersPage(v))
}

// AllCharactersView is /studio/characters: every project's characters.
type AllCharactersView struct {
	Groups []CharGroup
}

// CharGroup is one project's characters.
type CharGroup struct {
	Project ProjectSummary
	Cards   []CharCard
	CanAdd  bool
}

func (m *Module) allCharacters(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	usr := core.UserFrom(ctx)
	list, err := m.svc.ListProjects(ctx, usr.ID, ProjectFilter{})
	if err != nil {
		return err
	}
	var v AllCharactersView
	for _, p := range list {
		chars, err := m.svc.Characters(ctx, p.ID, "")
		if err != nil {
			return err
		}
		a := &Access{Project: &p.Project, Role: p.Role, Actor: ActorFrom(ctx)}
		v.Groups = append(v.Groups, CharGroup{Project: p, Cards: m.charCards(ctx, chars), CanAdd: a.Can(EditCharacters)})
	}
	return m.ui.Page(w, r, http.StatusOK, web.PageOpts{Title: i18n.T(ctx, "nav.characters"), Section: layout.SectionStudio,
		Nav: layout.NavCharacters, Tab: layout.TabHome}, AllCharactersPage(v))
}

// CharacterView is C3.
type CharacterView struct {
	Access    *Access
	Project   *Project
	Card      CharCard
	Refs      []CharacterRef
	RefURLs   map[string]string
	Outfits   []Outfit
	Relations []Relation
	Others    []Character
	Stats     CharacterStats
	Tab       string
	EditorN   int
	Errors    web.FormErrors
}

func (m *Module) loadCharacter(r *http.Request) (*CharacterView, error) {
	ctx := r.Context()
	a := AccessFrom(ctx)
	c, err := m.svc.Character(ctx, a.Project.ID, r.PathValue("id"))
	if err != nil {
		return nil, err
	}
	v := &CharacterView{Access: a, Project: a.Project, Card: m.charCards(ctx, []Character{*c})[0], Tab: r.URL.Query().Get("tab"), Errors: web.FormErrors{}}
	if !slices.Contains([]string{"profile", "outfits", "relations", "refs"}, v.Tab) {
		v.Tab = "profile"
	}
	if v.Refs, err = m.svc.CharacterRefs(ctx, c.ID); err != nil {
		return nil, err
	}
	v.RefURLs = map[string]string{}
	for _, ref := range v.Refs {
		v.RefURLs[ref.ID] = m.svc.MediaURL(ref.Key)
	}
	if v.Outfits, err = m.svc.Outfits(ctx, c.ID); err != nil {
		return nil, err
	}
	var outfitAssets []string
	for _, o := range v.Outfits {
		outfitAssets = append(outfitAssets, o.AssetID)
	}
	for id, u := range m.svc.AssetURLs(ctx, outfitAssets...) {
		v.RefURLs[id] = u
	}
	if v.Relations, err = m.svc.Relations(ctx, c.ID); err != nil {
		return nil, err
	}
	all, err := m.svc.Characters(ctx, a.Project.ID, "")
	if err != nil {
		return nil, err
	}
	for _, o := range all {
		if o.ID != c.ID {
			v.Others = append(v.Others, o)
		}
	}
	if v.Stats, err = m.svc.CharacterStats(ctx, c); err != nil {
		return nil, err
	}
	_ = m.d.DB.Get(ctx, &v.EditorN, `SELECT COALESCE(MAX(number), 1) FROM chapters WHERE project_id = ?`, a.Project.ID)
	return v, nil
}

func (m *Module) characterPage(w http.ResponseWriter, r *http.Request) error {
	v, err := m.loadCharacter(r)
	if err != nil {
		return err
	}
	ctx := r.Context()
	return m.ui.Page(w, r, http.StatusOK, web.PageOpts{Title: v.Card.Name, MobileTitle: " ", BackURL: "/studio/p/" + v.Project.ID + "/characters",
		Section: layout.SectionStudio, Nav: layout.NavCharacters, Tab: layout.TabHome}, CharacterPage(*v, i18n.T(ctx, "studio.chars.title", "title", v.Project.Title)))
}

func (m *Module) characterEdit(w http.ResponseWriter, r *http.Request) error {
	v, err := m.loadCharacter(r)
	if err != nil {
		return err
	}
	if !v.Access.Can(EditCharacters) {
		return core.ErrForbidden(i18n.T(r.Context(), "studio.error.role"))
	}
	return m.ui.Page(w, r, http.StatusOK, web.PageOpts{Title: i18n.T(r.Context(), "studio.character.edit_title", "name", v.Card.Name),
		BackURL: v.Card.Href, Section: layout.SectionStudio, Nav: layout.NavCharacters, Tab: layout.TabHome}, CharacterEditPage(*v, nil))
}

func readCharacter(r *http.Request) CharacterInput {
	role := r.PostFormValue("role")
	in := CharacterInput{Name: r.PostFormValue("name"), Kanji: r.PostFormValue("kanji"), Role: role, RoleLine: r.PostFormValue("role_line"),
		Age: r.PostFormValue("age"), Look: r.PostFormValue("look"), Personality: r.PostFormValue("personality"),
		SpeechStyle: r.PostFormValue("speech_style"), Backstory: r.PostFormValue("backstory"), Category: r.PostFormValue("category"),
		Style: r.PostFormValue("style"), Voice: Voice{Description: r.PostFormValue("voice"), Preset: r.PostFormValue("voice_preset")}}
	if in.Category == "" && role != "" {
		in.Category = categoryOf(role)
	}
	if in.RoleLine == "" && role != "" {
		in.RoleLine = i18n.T(r.Context(), "studio.role_word."+role)
	}
	return in
}

func (m *Module) characterSave(w http.ResponseWriter, r *http.Request) error {
	if err := m.parse(w, r); err != nil {
		return err
	}
	ctx := r.Context()
	a := AccessFrom(ctx)
	c, err := m.svc.UpdateCharacter(ctx, a, r.PathValue("id"), readCharacter(r))
	if err != nil {
		if ae, ok := err.(*core.APIError); ok && ae.Fields != nil {
			v, lerr := m.loadCharacter(r)
			if lerr != nil {
				return lerr
			}
			in := readCharacter(r)
			return m.ui.Page(w, r, http.StatusUnprocessableEntity, web.PageOpts{Title: v.Card.Name, BackURL: v.Card.Href,
				Section: layout.SectionStudio, Nav: layout.NavCharacters}, CharacterEditPage(*v, &characterForm{In: in, Errors: ae.Fields}))
		}
		return err
	}
	web.Flash(w, r, "success", i18n.T(ctx, "studio.character.saved", "name", c.Name), "")
	m.back(w, r, "/studio/p/"+a.Project.ID+"/characters/"+c.ID)
	return nil
}

type characterForm struct {
	In     CharacterInput
	Errors map[string]string
}

func (m *Module) characterDelete(w http.ResponseWriter, r *http.Request) error {
	if err := m.parse(w, r); err != nil {
		return err
	}
	ctx := r.Context()
	a := AccessFrom(ctx)
	if err := m.svc.DeleteCharacter(ctx, a, r.PathValue("id")); err != nil {
		return err
	}
	web.Flash(w, r, "success", i18n.T(ctx, "studio.character.deleted"), "")
	core.Redirect(w, r, "/studio/p/"+a.Project.ID+"/characters")
	return nil
}

// ---- C4: create ------------------------------------------------------------------------

// CreateView is C4.
type CreateView struct {
	Access    *Access
	Project   *Project
	Mode      string
	Character *Character
	In        CharacterInput
	Errors    map[string]string
	Job       *jobs.Job
	Sheets    []SheetImage
	SheetURLs map[string]string
	Refs      []CharacterRef
	RefURLs   map[string]string
	Route     RoutePreview
	GPU       string
}

func (m *Module) createView(r *http.Request) (*CreateView, error) {
	ctx := r.Context()
	a := AccessFrom(ctx)
	if !a.Can(EditCharacters) {
		return nil, core.ErrForbidden(i18n.T(ctx, "studio.error.role"))
	}
	v := &CreateView{Access: a, Project: a.Project, Mode: r.URL.Query().Get("mode"), Errors: map[string]string{}, SheetURLs: map[string]string{}, RefURLs: map[string]string{}}
	if v.Mode != "upload" {
		v.Mode = "describe"
	}
	v.In.Style = "series"
	if id := r.URL.Query().Get("character"); id != "" {
		c, err := m.svc.Character(ctx, a.Project.ID, id)
		if err != nil {
			return nil, err
		}
		v.Character = c
		v.In = CharacterInput{Name: c.Name, Role: c.Role, RoleLine: c.RoleLine, Look: c.Look, Personality: c.Personality, Category: c.Category, Style: c.Style}
		v.Job, v.Sheets, _ = m.svc.SheetCandidates(ctx, c)
		var ids []string
		for _, s := range v.Sheets {
			ids = append(ids, s.AssetID)
		}
		v.SheetURLs = m.svc.AssetURLs(ctx, ids...)
		if v.Refs, _ = m.svc.CharacterRefs(ctx, c.ID); len(v.Refs) > 0 {
			for _, ref := range v.Refs {
				v.RefURLs[ref.AssetID] = m.svc.MediaURL(ref.Key)
			}
		}
	}
	v.Route = m.routePreview(ctx, a.Actor.UserID, a.Project.ID, JobCharacterSheet)
	if c, err := web.ComputeSummary(ctx, m.d.Jobs); err == nil {
		v.GPU = c.GPU
	}
	return v, nil
}

func (m *Module) characterNew(w http.ResponseWriter, r *http.Request) error {
	v, err := m.createView(r)
	if err != nil {
		return err
	}
	return m.renderCreate(w, r, http.StatusOK, v)
}

func (m *Module) renderCreate(w http.ResponseWriter, r *http.Request, status int, v *CreateView) error {
	ctx := r.Context()
	if htmx(r) && r.Header.Get("HX-Target") == "mv-sheets" {
		return web.Fragment(w, r, status, SheetPicker(*v))
	}
	return m.ui.Page(w, r, status, web.PageOpts{Title: i18n.T(ctx, "studio.create.title"), MobileTitle: i18n.T(ctx, "studio.create.title"),
		BackURL: "/studio/p/" + v.Project.ID + "/characters", Section: layout.SectionStudio, Nav: layout.NavCharacters, Tab: layout.TabHome},
		CreateCharacterPage(*v))
}

// parseUpload parses a multipart form with room for several reference images.
func parseUpload(w http.ResponseWriter, r *http.Request) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadTotal)
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		if err := r.ParseMultipartForm(8 << 20); err != nil {
			return core.NewError(http.StatusRequestEntityTooLarge, "too_large", "")
		}
		return nil
	}
	if err := r.ParseForm(); err != nil {
		return core.ErrBadRequest("invalid form")
	}
	return nil
}

// uploadImages stores every file of field as a project image asset.
func (m *Module) uploadImages(r *http.Request, a *Access, field, kind string) ([]*Asset, error) {
	if r.MultipartForm == nil {
		return nil, nil
	}
	files := r.MultipartForm.File[field]
	if len(files) > maxUploadFiles {
		return nil, core.ErrValidation(map[string]string{field: i18n.T(r.Context(), "studio.upload.too_many", "count", maxUploadFiles)})
	}
	var out []*Asset
	for _, fh := range files {
		if fh.Size == 0 {
			continue
		}
		if fh.Size > maxUploadFile {
			return nil, core.ErrValidation(map[string]string{field: i18n.T(r.Context(), "studio.upload.too_big", "name", fh.Filename)})
		}
		f, err := fh.Open()
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(io.LimitReader(f, maxUploadFile+1))
		f.Close()
		if err != nil {
			return nil, err
		}
		as, err := m.svc.SaveImage(r.Context(), a.Project.ID, a.Actor.UserID, kind, "upload", data)
		if err != nil {
			if ae, ok := err.(*core.APIError); ok && ae.Status < 500 {
				return nil, core.ErrValidation(map[string]string{field: i18n.T(r.Context(), "studio.upload.not_image", "name", fh.Filename)})
			}
			return nil, err
		}
		out = append(out, as)
	}
	return out, nil
}

func (m *Module) characterCreate(w http.ResponseWriter, r *http.Request) error {
	if err := parseUpload(w, r); err != nil {
		return err
	}
	ctx := r.Context()
	a := AccessFrom(ctx)
	mode := r.URL.Query().Get("mode")
	if mode == "" {
		mode = r.PostFormValue("mode")
	}
	in := readCharacter(r)
	c, err := m.svc.CreateCharacter(ctx, a, in)
	if err != nil {
		if ae, ok := err.(*core.APIError); ok && ae.Fields != nil {
			v, verr := m.createView(r)
			if verr != nil {
				return verr
			}
			v.Mode, v.In, v.Errors = mode, in, ae.Fields
			if v.Mode != "upload" {
				v.Mode = "describe"
			}
			return m.renderCreate(w, r, http.StatusUnprocessableEntity, v)
		}
		return err
	}
	if mode == "upload" {
		assets, err := m.uploadImages(r, a, "refs", "character_ref")
		if err != nil {
			_ = m.svc.DeleteCharacter(ctx, a, c.ID)
			return err
		}
		kind := r.PostFormValue("ref_kind")
		for _, as := range assets {
			if _, err := m.svc.AddCharacterRef(ctx, a, c.ID, as.ID, kind, ""); err != nil {
				return err
			}
		}
		core.Redirect(w, r, "/studio/p/"+a.Project.ID+"/characters/new?mode=upload&character="+c.ID)
		return nil
	}
	if _, err := m.svc.GenerateSheets(ctx, a, c, in.Style); err != nil {
		return err
	}
	core.Redirect(w, r, "/studio/p/"+a.Project.ID+"/characters/new?character="+c.ID)
	return nil
}

// characterGenerate saves the description and generates a new set of sheets.
func (m *Module) characterGenerate(w http.ResponseWriter, r *http.Request) error {
	if err := m.parse(w, r); err != nil {
		return err
	}
	ctx := r.Context()
	a := AccessFrom(ctx)
	c, err := m.svc.Character(ctx, a.Project.ID, r.PathValue("id"))
	if err != nil {
		return err
	}
	if r.PostFormValue("name") != "" {
		in := readCharacter(r)
		in.Kanji, in.Age, in.SpeechStyle, in.Backstory, in.Voice = c.Kanji, c.Age, c.SpeechStyle, c.Backstory, c.Voice.V
		if c, err = m.svc.UpdateCharacter(ctx, a, c.ID, in); err != nil {
			return err
		}
	}
	if _, err := m.svc.GenerateSheets(ctx, a, c, r.PostFormValue("style")); err != nil {
		return err
	}
	core.Redirect(w, r, "/studio/p/"+a.Project.ID+"/characters/new?character="+c.ID)
	return nil
}

func (m *Module) characterSheets(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	q.Set("character", r.PathValue("id"))
	r.URL.RawQuery = q.Encode()
	v, err := m.createView(r)
	if err != nil {
		return err
	}
	return web.Fragment(w, r, http.StatusOK, SheetPicker(*v))
}

func (m *Module) characterPick(w http.ResponseWriter, r *http.Request) error {
	if err := m.parse(w, r); err != nil {
		return err
	}
	ctx := r.Context()
	a := AccessFrom(ctx)
	c, err := m.svc.Character(ctx, a.Project.ID, r.PathValue("id"))
	if err != nil {
		return err
	}
	if err := m.svc.PickSheet(ctx, a, c, web.FormValue(r, "sheet")); err != nil {
		return err
	}
	if web.FormValue(r, "train") == "1" {
		if _, err := m.svc.TrainLock(ctx, a, c, nil); err != nil {
			return err
		}
		web.Flash(w, r, "info", i18n.T(ctx, "studio.character.training_started", "name", c.Name), i18n.T(ctx, "studio.character.training_body"))
		core.Redirect(w, r, "/studio/p/"+a.Project.ID+"/characters/"+c.ID)
		return nil
	}
	m.back(w, r, "/studio/p/"+a.Project.ID+"/characters/new?character="+c.ID)
	return nil
}

func (m *Module) characterTrain(w http.ResponseWriter, r *http.Request) error {
	if err := m.parse(w, r); err != nil {
		return err
	}
	ctx := r.Context()
	a := AccessFrom(ctx)
	c, err := m.svc.Character(ctx, a.Project.ID, r.PathValue("id"))
	if err != nil {
		return err
	}
	if _, err := m.svc.TrainLock(ctx, a, c, r.PostForm["refs"]); err != nil {
		return err
	}
	web.Flash(w, r, "info", i18n.T(ctx, "studio.character.training_started", "name", c.Name), i18n.T(ctx, "studio.character.training_body"))
	m.back(w, r, "/studio/p/"+a.Project.ID+"/characters/"+c.ID)
	return nil
}

func (m *Module) characterRefUpload(w http.ResponseWriter, r *http.Request) error {
	if err := parseUpload(w, r); err != nil {
		return err
	}
	ctx := r.Context()
	a := AccessFrom(ctx)
	c, err := m.svc.Character(ctx, a.Project.ID, r.PathValue("id"))
	if err != nil {
		return err
	}
	assets, err := m.uploadImages(r, a, "refs", "character_ref")
	if err != nil {
		return err
	}
	kind := r.PostFormValue("kind")
	if kind == "" {
		kind = r.PostFormValue("ref_kind")
	}
	for _, as := range assets {
		if _, err := m.svc.AddCharacterRef(ctx, a, c.ID, as.ID, kind, r.PostFormValue("label")); err != nil {
			return err
		}
	}
	web.Flash(w, r, "success", i18n.T(ctx, "studio.character.refs_added", "count", len(assets)), "")
	m.back(w, r, "/studio/p/"+a.Project.ID+"/characters/"+c.ID+"?tab=refs")
	return nil
}

func (m *Module) characterRefUpdate(w http.ResponseWriter, r *http.Request) error {
	if err := m.parse(w, r); err != nil {
		return err
	}
	ctx := r.Context()
	a := AccessFrom(ctx)
	if err := m.svc.UpdateCharacterRef(ctx, a, r.PathValue("id"), r.PathValue("ref"), r.PostFormValue("kind"), r.PostFormValue("label")); err != nil {
		return err
	}
	if web.FormValue(r, "portrait") == "1" {
		var assetID string
		_ = m.d.DB.Get(ctx, &assetID, `SELECT asset_id FROM character_refs WHERE id = ?`, r.PathValue("ref"))
		if err := m.svc.SetPortrait(ctx, a, r.PathValue("id"), assetID); err != nil {
			return err
		}
	}
	m.back(w, r, "/studio/p/"+a.Project.ID+"/characters/"+r.PathValue("id")+"?tab=refs")
	return nil
}

func (m *Module) characterRefDelete(w http.ResponseWriter, r *http.Request) error {
	if err := m.parse(w, r); err != nil {
		return err
	}
	ctx := r.Context()
	a := AccessFrom(ctx)
	if err := m.svc.DeleteCharacterRef(ctx, a, r.PathValue("id"), r.PathValue("ref")); err != nil {
		return err
	}
	m.back(w, r, "/studio/p/"+a.Project.ID+"/characters/"+r.PathValue("id")+"?tab=refs")
	return nil
}

func (m *Module) outfitSave(w http.ResponseWriter, r *http.Request) error {
	if err := parseUpload(w, r); err != nil {
		return err
	}
	ctx := r.Context()
	a := AccessFrom(ctx)
	o := Outfit{Name: r.PostFormValue("name"), Description: r.PostFormValue("description"), Chapters: r.PostFormValue("chapters"),
		IsDefault: r.PostFormValue("is_default") == "on"}
	assets, err := m.uploadImages(r, a, "image", "outfit")
	if err != nil {
		return err
	}
	if len(assets) > 0 {
		o.AssetID = assets[0].ID
	}
	if _, err := m.svc.SaveOutfit(ctx, a, r.PathValue("id"), r.PathValue("oid"), o); err != nil {
		return err
	}
	m.back(w, r, "/studio/p/"+a.Project.ID+"/characters/"+r.PathValue("id")+"?tab=outfits")
	return nil
}

func (m *Module) outfitDelete(w http.ResponseWriter, r *http.Request) error {
	if err := m.parse(w, r); err != nil {
		return err
	}
	ctx := r.Context()
	a := AccessFrom(ctx)
	if err := m.svc.DeleteOutfit(ctx, a, r.PathValue("id"), r.PathValue("oid")); err != nil {
		return err
	}
	m.back(w, r, "/studio/p/"+a.Project.ID+"/characters/"+r.PathValue("id")+"?tab=outfits")
	return nil
}

func (m *Module) relationAdd(w http.ResponseWriter, r *http.Request) error {
	if err := m.parse(w, r); err != nil {
		return err
	}
	ctx := r.Context()
	a := AccessFrom(ctx)
	if err := m.svc.AddRelation(ctx, a, r.PathValue("id"), web.FormValue(r, "other_id"), web.FormValue(r, "kind"), web.FormValue(r, "note")); err != nil {
		return err
	}
	m.back(w, r, "/studio/p/"+a.Project.ID+"/characters/"+r.PathValue("id")+"?tab=relations")
	return nil
}

func (m *Module) relationDelete(w http.ResponseWriter, r *http.Request) error {
	if err := m.parse(w, r); err != nil {
		return err
	}
	ctx := r.Context()
	a := AccessFrom(ctx)
	if err := m.svc.DeleteRelation(ctx, a, r.PathValue("id"), r.PathValue("rid")); err != nil {
		return err
	}
	m.back(w, r, "/studio/p/"+a.Project.ID+"/characters/"+r.PathValue("id")+"?tab=relations")
	return nil
}
