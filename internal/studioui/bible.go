package studioui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/MaramHarsha/MangaVeyra/internal/assets"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/gen"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/storage"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/web"
	"github.com/MaramHarsha/MangaVeyra/internal/web/layout"
	"github.com/MaramHarsha/MangaVeyra/internal/web/ui"
)

const bibleUploadLimit = 16 << 20

type bibleForm struct {
	Name, Kanji, Role, RoleLine, Age, Look, Personality, SpeechStyle, Category string
	Mode, Style, Revision, CharacterID, Kind, Label, Description, Tags         string
	Default, Portrait                                                          bool
}

type bibleMedia struct {
	AssetID, URL, Label, Description string
	Default                          bool
}
type biblePage struct {
	Kind, Title, Query, Status, Format, Sort, View, Filter, Target, Notice, Error string
	Projects                                                                      []studio.ProjectSummary
	Project                                                                       studio.Project
	ProjectCards                                                                  []ui.ProjectCardProps
	CharacterCards                                                                []ui.CharacterCardProps
	Character                                                                     studio.Character
	Characters                                                                    []studio.Character
	Settings                                                                      []studio.SettingItem
	Media, Outfits                                                                []bibleMedia
	Images                                                                        map[string]string
	Counts                                                                        map[string]int
	Form                                                                          bibleForm
	Errors                                                                        web.FormErrors
	CanEdit, CanArt, CanGenerate, Editing, AddingOutfit, AddingRef, AddingSetting bool
	Job                                                                           *jobs.Job
	JobPosition                                                                   int
	Candidates                                                                    []gen.Candidate
}

func characterPath(projectID, characterID string) string {
	base := "/studio/p/" + url.PathEscape(projectID) + "/characters"
	if characterID != "" {
		return base + "/" + url.PathEscape(characterID)
	}
	return base
}
func settingPath(projectID string) string {
	return "/studio/p/" + url.PathEscape(projectID) + "/setting"
}
func sheetPath(projectID, characterID, jobID string) string {
	return characterPath(projectID, characterID) + "/sheets/" + url.PathEscape(jobID)
}
func sheetPagePath(projectID, characterID, jobID string) string {
	return characterPath(projectID, "") + "/new?character=" + url.QueryEscape(characterID) + "&job=" + url.QueryEscape(jobID)
}
func bibleRevision(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// RegisterBibleRoutes mounts the complete server-rendered Studio libraries.
func (m *Module) RegisterBibleRoutes(r *core.Router) {
	r.Page("GET /studio/projects", m.projectsPage, core.RequireSession)
	r.Page("GET /studio/characters", m.globalBiblePage, core.RequireSession)
	r.Page("GET /studio/setting", m.globalBiblePage, core.RequireSession)
	r.Page("GET /studio/p/{project}/characters", m.charactersPage, core.RequireSession)
	r.Page("GET /studio/p/{project}/characters/new", m.newCharacterPage, core.RequireSession)
	r.Page("POST /studio/p/{project}/characters/new", m.createBibleCharacter, core.RequireSession)
	r.Page("GET /studio/p/{project}/characters/{character}", m.characterPage, core.RequireSession)
	r.Page("POST /studio/p/{project}/characters/{character}", m.updateBibleCharacter, core.RequireSession)
	r.Page("POST /studio/p/{project}/characters/{character}/references", m.uploadBibleReference, core.RequireSession)
	r.Page("POST /studio/p/{project}/characters/{character}/outfits", m.createBibleOutfit, core.RequireSession)
	r.Page("GET /studio/p/{project}/characters/{character}/sheets/{job}", m.characterSheetStatus, core.RequireSession)
	r.Page("POST /studio/p/{project}/characters/{character}/sheets/{job}/apply", m.applyCharacterSheet, core.RequireSession)
	r.Page("GET /studio/p/{project}/setting", m.settingPage, core.RequireSession)
	r.Page("POST /studio/p/{project}/setting", m.createBibleSetting, core.RequireSession)
}

func (m *Module) projectsPage(w http.ResponseWriter, r *http.Request) error {
	return m.renderProjectLibrary(w, r, "")
}
func (m *Module) globalBiblePage(w http.ResponseWriter, r *http.Request) error {
	target := "characters"
	if r.URL.Path == "/studio/setting" {
		target = "setting"
	}
	if id := r.URL.Query().Get("project"); id != "" {
		actor, err := browserActor(r)
		if err != nil {
			return core.ErrNotFound()
		}
		if _, _, err := m.loadProject(r.Context(), actor, id); err != nil {
			return pageObjectError(err)
		}
		path := characterPath(id, "")
		if target == "setting" {
			path = settingPath(id)
		}
		core.Redirect(w, r, path)
		return nil
	}
	return m.renderProjectLibrary(w, r, target)
}

func (m *Module) renderProjectLibrary(w http.ResponseWriter, r *http.Request, target string) error {
	actor, err := browserActor(r)
	if err != nil {
		return core.ErrNotFound()
	}
	projects, err := m.studio.ListProjects(r.Context(), actor.UserID, 200)
	if err != nil {
		return err
	}
	q := r.URL.Query()
	v := biblePage{Kind: "projects", Projects: projects, Target: target, Query: strings.TrimSpace(q.Get("q")), Status: q.Get("status"), Format: q.Get("format"), Sort: q.Get("sort"), View: q.Get("view"), Counts: map[string]int{}}
	v.Title = i18n.T(r.Context(), "bible.projects.title")
	if target != "" {
		v.Title = i18n.T(r.Context(), "bible.choose."+target)
	}
	if v.Sort != "title" && v.Sort != "oldest" {
		v.Sort = "edited"
	}
	if v.View != "list" {
		v.View = "grid"
	}
	if !contains(projectStatuses, v.Status) {
		v.Status = ""
	}
	if !contains(formatValues, v.Format) {
		v.Format = ""
	}
	for _, p := range projects {
		if p.Role != studio.RoleOwner {
			v.Counts["shared"]++
		}
	}
	sort.SliceStable(projects, func(i, j int) bool {
		if v.Sort == "title" {
			return strings.ToLower(projects[i].Title) < strings.ToLower(projects[j].Title)
		}
		if v.Sort == "oldest" {
			return projects[i].UpdatedAt.Before(projects[j].UpdatedAt)
		}
		return projects[i].UpdatedAt.After(projects[j].UpdatedAt)
	})
	for _, p := range projects {
		if v.Query != "" && !strings.Contains(strings.ToLower(p.Title), strings.ToLower(v.Query)) {
			continue
		}
		if v.Status != "" && p.Status != v.Status {
			continue
		}
		if v.Format != "" && p.Format != v.Format {
			continue
		}
		href := storyPath(p.ID, p.LatestChapterID)
		if target == "characters" {
			href = characterPath(p.ID, "")
		}
		if target == "setting" {
			href = settingPath(p.ID)
		}
		meta := i18n.T(r.Context(), "bible.projects.premise")
		if p.LatestChapterNumber > 0 {
			meta = i18n.T(r.Context(), "bible.projects.chapter", "number", p.LatestChapterNumber)
		}
		v.ProjectCards = append(v.ProjectCards, ui.ProjectCardProps{Href: href, Title: p.Title, Meta: meta, ThumbLabel: i18n.T(r.Context(), "bible.projects.thumbnail"), ThumbURL: m.assetURL(r.Context(), p.ThumbAssetID), Variant: "studio", Badges: []ui.BadgeSpec{{Tone: statusTone(p.Status), Text: i18n.T(r.Context(), "studioui.status."+projectStatus(p.Status))}}})
	}
	return m.renderBible(w, r, http.StatusOK, v)
}

func (m *Module) bibleBase(r *http.Request, kind string) (biblePage, studio.Actor, error) {
	actor, err := browserActor(r)
	if err != nil {
		return biblePage{}, actor, studio.ErrNotFound
	}
	p, _, err := m.loadProject(r.Context(), actor, r.PathValue("project"))
	if err != nil {
		return biblePage{}, actor, err
	}
	v := biblePage{Kind: kind, Project: p, Images: map[string]string{}, Counts: map[string]int{}, Errors: web.FormErrors{}}
	for action, dst := range map[studio.Action]*bool{studio.ActEditScript: &v.CanEdit, studio.ActEditArt: &v.CanArt, studio.ActGenerate: &v.CanGenerate} {
		*dst, err = m.can(r.Context(), actor, p.ID, action)
		if err != nil {
			return v, actor, err
		}
	}
	return v, actor, nil
}

func (m *Module) charactersPage(w http.ResponseWriter, r *http.Request) error {
	v, _, err := m.bibleBase(r, "characters")
	if err != nil {
		return pageObjectError(err)
	}
	v.Title = i18n.T(r.Context(), "bible.characters.title", "project", v.Project.Title)
	v.Filter = r.URL.Query().Get("category")
	if !contains([]string{"main", "supporting", "extra"}, v.Filter) {
		v.Filter = ""
	}
	v.Characters, err = m.studio.ListCharacters(r.Context(), v.Project.ID)
	if err != nil {
		return err
	}
	for _, c := range v.Characters {
		v.Counts[c.Category]++
		if c.LockStatus == "ready" {
			v.Counts["locked"]++
		}
		if c.LockStatus == "training" {
			v.Counts["training"]++
		}
		if v.Filter != "" && c.Category != v.Filter {
			continue
		}
		first, last := c.Name, ""
		if parts := strings.SplitN(c.Name, " ", 2); len(parts) == 2 {
			first, last = parts[0], parts[1]
		}
		lock := -1.0
		if c.LockStatus == "ready" && c.LockScore != nil {
			lock = *c.LockScore
		}
		status := i18n.T(r.Context(), "bible.lock.soon")
		if c.LockStatus == "training" || c.LockStatus == "failed" {
			status = i18n.T(r.Context(), "bible.lock."+c.LockStatus)
		}
		v.CharacterCards = append(v.CharacterCards, ui.CharacterCardProps{Href: characterPath(v.Project.ID, c.ID), Kanji: c.Kanji, Role: characterRole(c), FirstName: first, LastName: last, Desc: characterDescription(c), Lock: lock, LockLabel: i18n.T(r.Context(), "bible.lock.title"), ArtLabel: i18n.T(r.Context(), "bible.characters.art"), ArtURL: m.assetURL(r.Context(), c.PortraitAssetID), Variant: "bible", StatusLabel: status, StatusTone: ui.ToneMuted})
	}
	return m.renderBible(w, r, http.StatusOK, v)
}

func characterDescription(c studio.Character) string {
	if c.Age != "" && c.Personality != "" {
		return c.Age + ". " + c.Personality
	}
	if c.Personality != "" {
		return c.Personality
	}
	return c.Look
}

func characterRole(c studio.Character) string {
	if c.RoleLine != "" {
		return c.RoleLine
	}
	return c.Role
}

func bibleFirstError(v biblePage) string {
	for _, key := range []string{"name", "role", "look", "personality", "style", "file", "kanji", "role_line", "age", "speech_style", "category", "kind", "label", "description", "tags"} {
		if v.Errors[key] != "" {
			return key
		}
	}
	return ""
}
func (m *Module) assetURL(ctx context.Context, id string) string {
	if id == "" {
		return ""
	}
	return m.assets.URLFor(ctx, id, 15*time.Minute)
}

func formFromCharacter(c studio.Character) bibleForm {
	return bibleForm{Name: c.Name, Kanji: c.Kanji, Role: c.Role, RoleLine: c.RoleLine, Age: c.Age, Look: c.Look, Personality: c.Personality, SpeechStyle: c.SpeechStyle, Category: c.Category, CharacterID: c.ID, Revision: bibleRevision(c.UpdatedAt), Mode: "describe", Style: "series"}
}
func (m *Module) loadBibleCharacter(r *http.Request, v *biblePage, id string) error {
	if !db.ValidID(id) {
		return studio.ErrNotFound
	}
	c, err := m.studio.GetCharacter(r.Context(), id)
	if err != nil {
		return err
	}
	if c.ProjectID != v.Project.ID {
		return studio.ErrNotFound
	}
	v.Character = c
	v.Form = formFromCharacter(c)
	v.Images[c.PortraitAssetID] = m.assetURL(r.Context(), c.PortraitAssetID)
	for _, ref := range c.Refs {
		v.Media = append(v.Media, bibleMedia{AssetID: ref.AssetID, URL: m.assetURL(r.Context(), ref.AssetID), Label: ref.Label, Description: ref.Kind})
	}
	for _, o := range c.Outfits {
		v.Outfits = append(v.Outfits, bibleMedia{AssetID: o.AssetID, URL: m.assetURL(r.Context(), o.AssetID), Label: o.Name, Description: o.Description, Default: o.IsDefault})
	}
	return nil
}

func (m *Module) characterPage(w http.ResponseWriter, r *http.Request) error {
	v, _, err := m.bibleBase(r, "detail")
	if err != nil {
		return pageObjectError(err)
	}
	if err := m.loadBibleCharacter(r, &v, r.PathValue("character")); err != nil {
		return pageObjectError(err)
	}
	v.Title = v.Character.Name
	v.Editing = r.URL.Query().Get("edit") == "profile"
	v.AddingOutfit = r.URL.Query().Get("add") == "outfit"
	v.AddingRef = r.URL.Query().Get("add") == "reference"
	if v.AddingOutfit {
		v.Form = bibleForm{Revision: bibleRevision(v.Character.UpdatedAt)}
	}
	if notice := r.URL.Query().Get("saved"); contains([]string{"profile", "reference", "outfit", "sheet"}, notice) {
		v.Notice = i18n.T(r.Context(), "bible.saved."+notice)
	}
	return m.renderBible(w, r, http.StatusOK, v)
}

func (m *Module) newCharacterPage(w http.ResponseWriter, r *http.Request) error {
	v, actor, err := m.bibleBase(r, "new")
	if err != nil {
		return pageObjectError(err)
	}
	if !v.CanEdit {
		return core.ErrNotFound()
	}
	v.Title = i18n.T(r.Context(), "bible.new.title")
	v.Form = bibleForm{Mode: "describe", Style: "series", Category: "supporting"}
	if id := r.URL.Query().Get("character"); id != "" {
		if err := m.loadBibleCharacter(r, &v, id); err != nil {
			return pageObjectError(err)
		}
	}
	if r.URL.Query().Get("mode") == "upload" {
		v.Form.Mode = "upload"
	}
	if jobID := r.URL.Query().Get("job"); jobID != "" {
		v.Job, err = m.ownedSheetJob(r.Context(), actor, v.Project.ID, v.Character.ID, jobID)
		if err != nil {
			return pageObjectError(err)
		}
		if v.Job.State == jobs.StateQueued {
			v.JobPosition, err = m.d.Jobs.Position(r.Context(), v.Job)
			if err != nil {
				return err
			}
		}
		if v.Job.State == jobs.StateDone {
			v.Candidates, err = m.gen.Candidates(r.Context(), actor, jobID)
			if err != nil {
				return pageObjectError(err)
			}
		}
	}
	return m.renderBible(w, r, http.StatusOK, v)
}

func readBibleForm(r *http.Request) bibleForm {
	f := bibleForm{Name: web.FormValue(r, "name"), Kanji: web.FormValue(r, "kanji"), Role: web.FormValue(r, "role"), RoleLine: web.FormValue(r, "role_line"), Age: web.FormValue(r, "age"), Look: web.FormValue(r, "look"), Personality: web.FormValue(r, "personality"), SpeechStyle: web.FormValue(r, "speech_style"), Category: web.FormValue(r, "category"), Mode: web.FormValue(r, "mode"), Style: web.FormValue(r, "style"), Revision: web.FormValue(r, "revision"), CharacterID: web.FormValue(r, "character_id"), Kind: web.FormValue(r, "kind"), Label: web.FormValue(r, "label"), Description: web.FormValue(r, "description"), Tags: web.FormValue(r, "tags"), Default: web.FormBool(r, "default"), Portrait: web.FormBool(r, "portrait")}
	if f.Category == "" {
		f.Category = "supporting"
	}
	return f
}
func (f bibleForm) character(projectID string) studio.NewCharacter {
	return studio.NewCharacter{ProjectID: projectID, Name: f.Name, Kanji: f.Kanji, Role: f.Role, RoleLine: f.RoleLine, Age: f.Age, Look: f.Look, Personality: f.Personality, SpeechStyle: f.SpeechStyle, Category: f.Category}
}

func validateBibleCharacter(ctx context.Context, f bibleForm) web.FormErrors {
	e := web.FormErrors{}
	for _, check := range []struct {
		name, value string
		max         int
		required    bool
	}{{"name", f.Name, 160, true}, {"kanji", f.Kanji, 160, false}, {"role", f.Role, 160, false}, {"role_line", f.RoleLine, 1000, false}, {"age", f.Age, 80, false}, {"look", f.Look, 4000, false}, {"personality", f.Personality, 4000, false}, {"speech_style", f.SpeechStyle, 2000, false}} {
		if !utf8.ValidString(check.value) || utf8.RuneCountInString(check.value) > check.max || (check.required && check.value == "") {
			e[check.name] = i18n.T(ctx, "bible.error.field", "max", check.max)
		}
	}
	if !contains([]string{"main", "supporting", "extra"}, f.Category) {
		e["category"] = i18n.T(ctx, "bible.error.choice")
	}
	return e
}

func parseBibleMultipart(w http.ResponseWriter, r *http.Request) error {
	if r.ContentLength > bibleUploadLimit+(1<<20) {
		return core.NewError(http.StatusRequestEntityTooLarge, "request_too_large", i18n.T(r.Context(), "bible.error.upload"))
	}
	r.Body = http.MaxBytesReader(w, r.Body, bibleUploadLimit+(1<<20))
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			var size *http.MaxBytesError
			if errors.As(err, &size) {
				return core.NewError(http.StatusRequestEntityTooLarge, "request_too_large", i18n.T(r.Context(), "bible.error.upload"))
			}
			return core.ErrValidation(map[string]string{"file": i18n.T(r.Context(), "bible.error.upload")})
		}
		return nil
	}
	if err := r.ParseForm(); err != nil {
		var size *http.MaxBytesError
		if errors.As(err, &size) {
			return core.NewError(http.StatusRequestEntityTooLarge, "request_too_large", i18n.T(r.Context(), "bible.error.upload"))
		}
		return err
	}
	return nil
}

func (m *Module) stageBibleUpload(r *http.Request, actor studio.Actor, projectID, kind string, required bool) (*assets.StagedAsset, error) {
	f, h, err := r.FormFile("file")
	if errors.Is(err, http.ErrMissingFile) && !required {
		return nil, nil
	}
	if err != nil {
		return nil, studio.ErrInvalid
	}
	defer f.Close()
	if h.Size <= 0 || h.Size > bibleUploadLimit {
		return nil, studio.ErrInvalid
	}
	data, err := io.ReadAll(io.LimitReader(f, bibleUploadLimit+1))
	if err != nil {
		return nil, err
	}
	if len(data) > bibleUploadLimit {
		return nil, studio.ErrInvalid
	}
	ct := storage.DetectContentType(data)
	if storage.CheckType(ct, storage.ImageTypes...) != nil {
		return nil, studio.ErrInvalid
	}
	if _, _, err := storage.DecodeImage(data, storage.DefaultImageLimits); err != nil {
		return nil, studio.ErrInvalid
	}
	return m.assets.Stage(r.Context(), assets.SaveParams{OwnerID: actor.UserID, ProjectID: projectID, Kind: kind, Source: assets.SourceUpload, ContentType: ct, Data: data})
}

func (m *Module) createBibleCharacter(w http.ResponseWriter, r *http.Request) error {
	v, actor, err := m.bibleBase(r, "new")
	if err != nil {
		return pageObjectError(err)
	}
	if !v.CanEdit {
		return core.ErrNotFound()
	}
	v.Title = i18n.T(r.Context(), "bible.new.title")
	if err := parseBibleMultipart(w, r); err != nil {
		return err
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	v.Form = readBibleForm(r)
	v.Errors = validateBibleCharacter(r.Context(), v.Form)
	if !contains([]string{"describe", "upload", "save"}, v.Form.Mode) {
		v.Errors["mode"] = i18n.T(r.Context(), "bible.error.choice")
	}
	if !contains([]string{"series", "colour", "chibi"}, v.Form.Style) {
		v.Errors["style"] = i18n.T(r.Context(), "bible.error.choice")
	}
	if v.Form.Mode == "describe" && !v.CanGenerate {
		return core.ErrNotFound()
	}
	if v.Form.Mode == "upload" && !v.CanArt {
		return core.ErrNotFound()
	}
	if v.Errors.Has() {
		return m.renderBible(w, r, 422, v)
	}
	staged, err := m.stageBibleUpload(r, actor, v.Project.ID, "character_ref", v.Form.Mode == "upload")
	if err != nil {
		v.Errors["file"] = i18n.T(r.Context(), "bible.error.upload")
		return m.renderBible(w, r, 422, v)
	}
	if staged != nil {
		defer m.assets.DiscardStaged(context.WithoutCancel(r.Context()), staged)
	}
	var c studio.Character
	if v.Form.CharacterID != "" {
		if err := m.loadBibleCharacter(r, &v, v.Form.CharacterID); err != nil {
			return pageObjectError(err)
		}
		// Preserve the submitted values/revision when loading the current record.
		v.Form = readBibleForm(r)
		revision, parseErr := time.Parse(time.RFC3339Nano, v.Form.Revision)
		if parseErr != nil {
			v.Errors[""] = i18n.T(r.Context(), "bible.error.conflict")
			return m.renderBible(w, r, 409, v)
		}
		if !revision.Equal(v.Character.UpdatedAt) {
			return m.bibleMutationError(w, r, v, studio.ErrConflict)
		}
		// Returning to the candidate screen and submitting the unchanged brief
		// must not invalidate a sheet job already bound to this revision.
		if v.Form.character(v.Project.ID) == formFromCharacter(v.Character).character(v.Project.ID) {
			c = v.Character
		} else {
			c, err = m.bible.UpdateCharacter(r.Context(), actor, studio.UpdateCharacterInput{ProjectID: v.Project.ID, CharacterID: v.Form.CharacterID, Character: v.Form.character(v.Project.ID), ExpectedUpdatedAt: revision})
		}
	} else {
		c, err = m.studio.CreateCharacter(r.Context(), actor, v.Form.character(v.Project.ID))
	}
	if err != nil {
		return m.bibleMutationError(w, r, v, err)
	}
	v.Character = c
	v.Form.CharacterID = c.ID
	v.Form.Revision = bibleRevision(c.UpdatedAt)
	if staged != nil {
		err = m.attachStagedReference(r.Context(), actor, c, staged, "sheet", i18n.T(r.Context(), "bible.refs.sheet"), true)
		if err != nil {
			return m.bibleMutationError(w, r, v, err)
		}
		core.Redirect(w, r, characterPath(v.Project.ID, c.ID)+"?saved=reference")
		return nil
	}
	if v.Form.Mode == "save" {
		core.Redirect(w, r, characterPath(v.Project.ID, c.ID)+"?saved=profile")
		return nil
	}
	style := v.Project.Style
	if v.Form.Style == "colour" {
		style = "colour"
	}
	if v.Form.Style == "chibi" {
		style = "chibi"
	}
	job, err := m.gen.EnqueueCharacterSheet(r.Context(), actor, gen.CharacterSheetRequest{ProjectID: v.Project.ID, CharacterID: c.ID, Count: 4, Style: style, ExpectedUpdatedAt: c.UpdatedAt})
	if err != nil {
		v.Error = i18n.T(r.Context(), "bible.error.generate")
		return m.renderBible(w, r, 422, v)
	}
	core.Redirect(w, r, sheetPagePath(v.Project.ID, c.ID, job.ID))
	return nil
}

func (m *Module) updateBibleCharacter(w http.ResponseWriter, r *http.Request) error {
	v, actor, err := m.bibleBase(r, "detail")
	if err != nil {
		return pageObjectError(err)
	}
	if !v.CanEdit {
		return core.ErrNotFound()
	}
	if err := m.loadBibleCharacter(r, &v, r.PathValue("character")); err != nil {
		return pageObjectError(err)
	}
	v.Title = v.Character.Name
	v.Editing = true
	if err := web.ParseForm(w, r); err != nil {
		return err
	}
	v.Form = readBibleForm(r)
	v.Errors = validateBibleCharacter(r.Context(), v.Form)
	if v.Errors.Has() {
		return m.renderBible(w, r, 422, v)
	}
	revision, err := time.Parse(time.RFC3339Nano, v.Form.Revision)
	if err != nil {
		return m.bibleMutationError(w, r, v, studio.ErrConflict)
	}
	_, err = m.bible.UpdateCharacter(r.Context(), actor, studio.UpdateCharacterInput{ProjectID: v.Project.ID, CharacterID: v.Character.ID, Character: v.Form.character(v.Project.ID), ExpectedUpdatedAt: revision})
	if err != nil {
		return m.bibleMutationError(w, r, v, err)
	}
	core.Redirect(w, r, characterPath(v.Project.ID, v.Character.ID)+"?saved=profile")
	return nil
}

func (m *Module) attachStagedReference(ctx context.Context, actor studio.Actor, c studio.Character, staged *assets.StagedAsset, kind, label string, portrait bool) error {
	return m.d.DB.WithTx(ctx, func(tx *db.Tx) error {
		if err := m.studio.AuthorizeTx(ctx, tx, actor, c.ProjectID, studio.ActEditArt); err != nil {
			return err
		}
		current, err := m.studio.GetCharacterTx(ctx, tx, c.ID)
		if err != nil {
			return err
		}
		if current.ProjectID != c.ProjectID {
			return studio.ErrNotFound
		}
		if !current.UpdatedAt.Equal(c.UpdatedAt) {
			return studio.ErrConflict
		}
		a, err := m.assets.CommitStagedTx(ctx, tx, staged)
		if err != nil {
			return err
		}
		if _, err := m.studio.AddCharacterRefTx(ctx, tx, c.ID, a.ID, kind, label); err != nil {
			return err
		}
		if portrait {
			return m.studio.SetCharacterPortraitTx(ctx, tx, c.ID, a.ID)
		}
		return nil
	})
}

func (m *Module) uploadBibleReference(w http.ResponseWriter, r *http.Request) error {
	v, actor, err := m.bibleBase(r, "detail")
	if err != nil {
		return pageObjectError(err)
	}
	if !v.CanArt {
		return core.ErrNotFound()
	}
	if err := m.loadBibleCharacter(r, &v, r.PathValue("character")); err != nil {
		return pageObjectError(err)
	}
	v.Title = v.Character.Name
	v.AddingRef = true
	if err := parseBibleMultipart(w, r); err != nil {
		return err
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	v.Form = readBibleForm(r)
	revision, err := time.Parse(time.RFC3339Nano, v.Form.Revision)
	if err != nil || !revision.Equal(v.Character.UpdatedAt) {
		return m.bibleMutationError(w, r, v, studio.ErrConflict)
	}
	if !contains([]string{"front", "side", "back", "face", "expression", "outfit", "sheet", "other"}, v.Form.Kind) {
		v.Errors["kind"] = i18n.T(r.Context(), "bible.error.choice")
	}
	if utf8.RuneCountInString(v.Form.Label) > 160 {
		v.Errors["label"] = i18n.T(r.Context(), "bible.error.field", "max", 160)
	}
	if v.Errors.Has() {
		return m.renderBible(w, r, 422, v)
	}
	staged, err := m.stageBibleUpload(r, actor, v.Project.ID, "character_ref", true)
	if err != nil {
		v.Errors["file"] = i18n.T(r.Context(), "bible.error.upload")
		return m.renderBible(w, r, 422, v)
	}
	defer m.assets.DiscardStaged(context.WithoutCancel(r.Context()), staged)
	if err := m.attachStagedReference(r.Context(), actor, v.Character, staged, v.Form.Kind, v.Form.Label, v.Form.Portrait); err != nil {
		return m.bibleMutationError(w, r, v, err)
	}
	core.Redirect(w, r, characterPath(v.Project.ID, v.Character.ID)+"?saved=reference")
	return nil
}

func (m *Module) createBibleOutfit(w http.ResponseWriter, r *http.Request) error {
	v, actor, err := m.bibleBase(r, "detail")
	if err != nil {
		return pageObjectError(err)
	}
	if !v.CanEdit {
		return core.ErrNotFound()
	}
	if err := m.loadBibleCharacter(r, &v, r.PathValue("character")); err != nil {
		return pageObjectError(err)
	}
	v.Title = v.Character.Name
	v.AddingOutfit = true
	if err := parseBibleMultipart(w, r); err != nil {
		return err
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	v.Form = readBibleForm(r)
	if v.Form.Name == "" || utf8.RuneCountInString(v.Form.Name) > 160 {
		v.Errors["name"] = i18n.T(r.Context(), "bible.error.field", "max", 160)
	}
	if utf8.RuneCountInString(v.Form.Description) > 4000 {
		v.Errors["description"] = i18n.T(r.Context(), "bible.error.field", "max", 4000)
	}
	if v.Errors.Has() {
		return m.renderBible(w, r, 422, v)
	}
	revision, err := time.Parse(time.RFC3339Nano, v.Form.Revision)
	if err != nil {
		return m.bibleMutationError(w, r, v, studio.ErrConflict)
	}
	staged, err := m.stageBibleUpload(r, actor, v.Project.ID, "outfit", false)
	if err != nil {
		v.Errors["file"] = i18n.T(r.Context(), "bible.error.upload")
		return m.renderBible(w, r, 422, v)
	}
	if staged != nil {
		defer m.assets.DiscardStaged(context.WithoutCancel(r.Context()), staged)
	}
	err = m.d.DB.WithTx(r.Context(), func(tx *db.Tx) error {
		if err := m.studio.AuthorizeTx(r.Context(), tx, actor, v.Project.ID, studio.ActEditScript); err != nil {
			return err
		}
		assetID := ""
		if staged != nil {
			a, err := m.assets.CommitStagedTx(r.Context(), tx, staged)
			if err != nil {
				return err
			}
			assetID = a.ID
		}
		_, err := m.bible.CreateOutfitTx(r.Context(), tx, actor, studio.NewOutfit{ProjectID: v.Project.ID, CharacterID: v.Character.ID, Name: v.Form.Name, Description: v.Form.Description, AssetID: assetID, IsDefault: v.Form.Default, ExpectedUpdatedAt: revision})
		return err
	})
	if err != nil {
		return m.bibleMutationError(w, r, v, err)
	}
	core.Redirect(w, r, characterPath(v.Project.ID, v.Character.ID)+"?saved=outfit")
	return nil
}

func (m *Module) settingPage(w http.ResponseWriter, r *http.Request) error {
	v, _, err := m.bibleBase(r, "setting")
	if err != nil {
		return pageObjectError(err)
	}
	v.Title = i18n.T(r.Context(), "bible.setting.title", "project", v.Project.Title)
	v.Filter = r.URL.Query().Get("kind")
	if !contains([]string{"location", "prop", "style"}, v.Filter) {
		v.Filter = "location"
	}
	v.AddingSetting = r.URL.Query().Get("add") == "1"
	v.Form.Kind = v.Filter
	if err := m.loadSettingLibrary(r, &v); err != nil {
		return err
	}
	if r.URL.Query().Get("saved") == "setting" {
		v.Notice = i18n.T(r.Context(), "bible.saved.setting")
	}
	return m.renderBible(w, r, 200, v)
}
func (m *Module) loadSettingLibrary(r *http.Request, v *biblePage) error {
	all, err := m.studio.ListSettingItems(r.Context(), v.Project.ID, "")
	if err != nil {
		return err
	}
	for _, item := range all {
		v.Counts[item.Kind]++
		if item.Kind == v.Filter {
			v.Settings = append(v.Settings, item)
			v.Images[item.AssetID] = m.assetURL(r.Context(), item.AssetID)
		}
	}
	return nil
}
func (m *Module) createBibleSetting(w http.ResponseWriter, r *http.Request) error {
	v, actor, err := m.bibleBase(r, "setting")
	if err != nil {
		return pageObjectError(err)
	}
	if !v.CanEdit {
		return core.ErrNotFound()
	}
	v.Title = i18n.T(r.Context(), "bible.setting.title", "project", v.Project.Title)
	v.AddingSetting = true
	if err := parseBibleMultipart(w, r); err != nil {
		return err
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	v.Form = readBibleForm(r)
	v.Filter = v.Form.Kind
	if !contains([]string{"location", "prop", "style"}, v.Filter) {
		v.Filter = "location"
		v.Errors["kind"] = i18n.T(r.Context(), "bible.error.choice")
	}
	if err := m.loadSettingLibrary(r, &v); err != nil {
		return err
	}
	if v.Form.Name == "" || utf8.RuneCountInString(v.Form.Name) > 160 {
		v.Errors["name"] = i18n.T(r.Context(), "bible.error.field", "max", 160)
	}
	if utf8.RuneCountInString(v.Form.Description) > 8000 {
		v.Errors["description"] = i18n.T(r.Context(), "bible.error.field", "max", 8000)
	}
	tags := []string{}
	for _, tag := range strings.Split(v.Form.Tags, ",") {
		if tag = strings.TrimSpace(tag); tag != "" {
			tags = append(tags, tag)
		}
	}
	if len(tags) > 32 {
		v.Errors["tags"] = i18n.T(r.Context(), "bible.error.tags")
	}
	for _, tag := range tags {
		if utf8.RuneCountInString(tag) > 80 {
			v.Errors["tags"] = i18n.T(r.Context(), "bible.error.tags")
		}
	}
	if v.Errors.Has() {
		return m.renderBible(w, r, 422, v)
	}
	staged, err := m.stageBibleUpload(r, actor, v.Project.ID, "setting", false)
	if err != nil {
		v.Errors["file"] = i18n.T(r.Context(), "bible.error.upload")
		return m.renderBible(w, r, 422, v)
	}
	if staged != nil {
		defer m.assets.DiscardStaged(context.WithoutCancel(r.Context()), staged)
	}
	err = m.d.DB.WithTx(r.Context(), func(tx *db.Tx) error {
		if err := m.studio.AuthorizeTx(r.Context(), tx, actor, v.Project.ID, studio.ActEditScript); err != nil {
			return err
		}
		assetID := ""
		if staged != nil {
			a, err := m.assets.CommitStagedTx(r.Context(), tx, staged)
			if err != nil {
				return err
			}
			assetID = a.ID
		}
		_, err := m.bible.CreateSettingItemTx(r.Context(), tx, actor, studio.NewSettingItem{ProjectID: v.Project.ID, Kind: v.Form.Kind, Name: v.Form.Name, Description: v.Form.Description, Tags: tags, AssetID: assetID})
		return err
	})
	if err != nil {
		return m.bibleMutationError(w, r, v, err)
	}
	core.Redirect(w, r, settingPath(v.Project.ID)+"?kind="+url.QueryEscape(v.Form.Kind)+"&saved=setting")
	return nil
}

func (m *Module) ownedSheetJob(ctx context.Context, actor studio.Actor, projectID, characterID, jobID string) (*jobs.Job, error) {
	if !db.ValidID(jobID) || characterID == "" {
		return nil, studio.ErrNotFound
	}
	if err := m.studio.Authorize(ctx, actor, projectID, studio.ActRead); err != nil {
		return nil, err
	}
	j, err := m.d.Jobs.Get(ctx, jobID)
	if err != nil {
		return nil, studio.ErrNotFound
	}
	var payload struct {
		Request gen.CharacterSheetRequest `json:"request"`
	}
	if j.Type != gen.JobCharacterSheet || j.UserID != actor.UserID || j.Scope.Kind != jobs.ScopeProject || j.Scope.ProjectID != projectID || j.DecodeInput(&payload) != nil || payload.Request.CharacterID != characterID || payload.Request.ProjectID != projectID {
		return nil, studio.ErrNotFound
	}
	return j, nil
}
func (m *Module) characterSheetStatus(w http.ResponseWriter, r *http.Request) error {
	v, actor, err := m.bibleBase(r, "new")
	if err != nil {
		return pageObjectError(err)
	}
	if err := m.loadBibleCharacter(r, &v, r.PathValue("character")); err != nil {
		return pageObjectError(err)
	}
	v.Job, err = m.ownedSheetJob(r.Context(), actor, v.Project.ID, v.Character.ID, r.PathValue("job"))
	if err != nil {
		return pageObjectError(err)
	}
	w.Header().Set("Cache-Control", "no-store")
	if v.Job.State == jobs.StateQueued {
		v.JobPosition, err = m.d.Jobs.Position(r.Context(), v.Job)
		if err != nil {
			return err
		}
	}
	if v.Job.State == jobs.StateDone {
		w.Header().Set("HX-Redirect", sheetPagePath(v.Project.ID, v.Character.ID, v.Job.ID))
	}
	return web.Render(w, r, 200, BibleSheetJob(v), nil)
}
func (m *Module) applyCharacterSheet(w http.ResponseWriter, r *http.Request) error {
	v, actor, err := m.bibleBase(r, "new")
	if err != nil {
		return pageObjectError(err)
	}
	if !v.CanGenerate {
		return core.ErrNotFound()
	}
	if err := m.loadBibleCharacter(r, &v, r.PathValue("character")); err != nil {
		return pageObjectError(err)
	}
	v.Job, err = m.ownedSheetJob(r.Context(), actor, v.Project.ID, v.Character.ID, r.PathValue("job"))
	if err != nil {
		return pageObjectError(err)
	}
	if err := web.ParseForm(w, r); err != nil {
		return err
	}
	if v.Job.State != jobs.StateDone {
		return core.ErrConflict(i18n.T(r.Context(), "bible.error.not_ready"))
	}
	index, err := strconv.Atoi(web.FormValue(r, "index"))
	if err != nil || index < 0 || index > 3 {
		return core.ErrValidation(map[string]string{"index": i18n.T(r.Context(), "bible.error.choice")})
	}
	if err := m.gen.ApplyVariation(r.Context(), actor, v.Job.ID, index); err != nil {
		v.Title = i18n.T(r.Context(), "bible.new.title")
		v.Candidates, _ = m.gen.Candidates(r.Context(), actor, v.Job.ID)
		return m.bibleMutationError(w, r, v, err)
	}
	core.Redirect(w, r, characterPath(v.Project.ID, v.Character.ID)+"?saved=sheet")
	return nil
}

func (m *Module) bibleMutationError(w http.ResponseWriter, r *http.Request, v biblePage, err error) error {
	if errors.Is(err, studio.ErrConflict) {
		v.Error = i18n.T(r.Context(), "bible.error.conflict")
		return m.renderBible(w, r, 409, v)
	}
	if errors.Is(err, studio.ErrInvalid) {
		v.Error = i18n.T(r.Context(), "bible.error.invalid")
		return m.renderBible(w, r, 422, v)
	}
	return pageObjectError(err)
}
func (m *Module) renderBible(w http.ResponseWriter, r *http.Request, status int, v biblePage) error {
	nav := layout.NavProjects
	if v.Kind == "characters" || v.Kind == "detail" || v.Kind == "new" || v.Target == "characters" {
		nav = layout.NavCharacters
	}
	if v.Kind == "setting" || v.Target == "setting" {
		nav = layout.NavSetting
	}
	return m.ui.Page(w, r, status, web.PageOpts{Title: v.Title, Section: layout.SectionStudio, Nav: nav, BackURL: "/studio/projects"}, BiblePage(v))
}

func bibleListURL(v biblePage, changes map[string]string) string {
	q := url.Values{}
	for k, value := range map[string]string{"q": v.Query, "status": v.Status, "format": v.Format, "sort": v.Sort, "view": v.View} {
		if value != "" {
			q.Set(k, value)
		}
	}
	for k, value := range changes {
		if value == "" {
			q.Del(k)
		} else {
			q.Set(k, value)
		}
	}
	path := "/studio/projects"
	if v.Target != "" {
		path = "/studio/" + v.Target
	}
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	return path
}
func bibleOptions(ctx context.Context, group string, values ...string) []ui.Option {
	out := make([]ui.Option, 0, len(values))
	for _, value := range values {
		out = append(out, ui.Option{Value: value, Label: i18n.T(ctx, "bible."+group+"."+value)})
	}
	return out
}
func bibleFieldLabel(ctx context.Context, key string) string { return i18n.T(ctx, "bible.field."+key) }
func bibleCandidateVars(ctx context.Context, candidates []gen.Candidate) []ui.Variation {
	out := make([]ui.Variation, 0, len(candidates))
	for _, c := range candidates {
		out = append(out, ui.Variation{Value: strconv.Itoa(c.Index), Label: i18n.T(ctx, "bible.new.sheet", "number", c.Index+1), Lock: -1, ImageURL: c.URL})
	}
	return out
}
func bibleCandidateTitle(ctx context.Context, count int) string {
	return i18n.T(ctx, "bible.new.pick", "count", count)
}
func bibleSheetGen(v biblePage, ctx context.Context) ui.GenProps {
	state := ui.GenQueued
	if v.Job.State == jobs.StateRunning {
		state = ui.GenGenerating
	}
	if v.Job.State == jobs.StateDone {
		state = ui.GenReady
	}
	if v.Job.State == jobs.StateFailed || v.Job.State == jobs.StateCanceled {
		state = ui.GenFailed
	}
	detail := i18n.T(ctx, "bible.new.wait")
	title := i18n.T(ctx, "bible.new.generating")
	if state == ui.GenQueued {
		title = i18n.T(ctx, "bible.new.queued")
	}
	if state == ui.GenReady {
		title = i18n.T(ctx, "bible.new.ready")
		detail = i18n.T(ctx, "bible.new.keep_hint")
	}
	if state == ui.GenFailed {
		title = i18n.T(ctx, "bible.new.failed")
		detail = i18n.T(ctx, jobErrorKey(v.Job))
	}
	return ui.GenProps{State: state, Position: v.JobPosition, Progress: v.Job.Progress, Title: title, Detail: detail, ProgressText: fmt.Sprintf("%d%%", percent(v.Job.Progress)), JobID: v.Job.ID}
}
