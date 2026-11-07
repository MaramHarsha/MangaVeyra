package studioui

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/MaramHarsha/MangaVeyra/internal/ai"
	"github.com/MaramHarsha/MangaVeyra/internal/authui"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/gen"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/web"
)

const (
	maxSourceBytes    = 1 << 20
	newProjectSlop    = 256 << 10
	newProjectMaxBody = maxSourceBytes + newProjectSlop
)

// The wizard offers exactly the genres and art styles of onboarding (A5), so a
// creator meets one list and one set of labels ("genre.*", "style.*").
var (
	genreValues  = authui.Genres
	styleValues  = artStyleValues()
	formatValues = []string{"manga_rtl", "comic_ltr", "webtoon"}
	// languageValues are the manga content languages (shared contract).
	languageValues = pagedoc.ContentLanguages
)

// pageSize maps a paged print size to the project's canvas. B5 keeps the
// page-document default (1600 × 2400); the A sizes keep the 1600 px base width
// at their own aspect ratio. Webtoon projects always use the strip canvas.
type pageSize struct {
	Value         string
	Width, Height int
}

var pageSizes = []pageSize{{"b5", 1600, 2400}, {"a5", 1600, 2270}, {"a4", 1600, 2263}}

func artStyleValues() []string {
	out := make([]string, 0, len(authui.ArtStyles))
	for _, style := range authui.ArtStyles {
		out = append(out, style.Value)
	}
	return out
}

func styleIcon(value string) string {
	for _, style := range authui.ArtStyles {
		if style.Value == value {
			return style.Icon
		}
	}
	return "palette"
}

func pageSizeFor(value string) (pageSize, bool) {
	for _, size := range pageSizes {
		if size.Value == value {
			return size, true
		}
	}
	return pageSizes[0], false
}

func (m *Module) newProjectRedirect(w http.ResponseWriter, r *http.Request) error {
	core.Redirect(w, r, "/studio/new/premise")
	return nil
}

// newProjectPage renders the B3 wizard. Every step URL serves the same
// complete form; the step only decides which one opens first.
func (m *Module) newProjectPage(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	user := core.UserFrom(ctx)
	if user == nil || !user.CanCreate() {
		return core.ErrForbidden(i18n.T(ctx, "studioui.error.creator_required"))
	}
	step := r.PathValue("step")
	if !contains(wizardSteps, step) {
		return core.ErrNotFound()
	}
	values := defaultNewProjectValues(i18n.LocaleFrom(ctx))
	if style := m.onboardingStyle(ctx, user.ID); style != "" {
		values.Style = style
	}
	v := newProjectView{Values: values, Errors: web.FormErrors{}, Step: step}
	return m.renderNewProject(w, r, http.StatusOK, v)
}

// onboardingStyle is the default art style the creator picked in onboarding
// (A5 step 3 asks for the default style of new projects), if any.
func (m *Module) onboardingStyle(ctx context.Context, userID string) string {
	var raw string
	if err := m.d.DB.Get(ctx, &raw, `SELECT onboarding FROM users WHERE id = ?`, userID); err != nil {
		return ""
	}
	var o authui.Onboarding
	if json.Unmarshal([]byte(raw), &o) != nil || !contains(styleValues, o.ArtStyle) {
		return ""
	}
	return o.ArtStyle
}

func (m *Module) createProject(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	actor, err := browserActor(r)
	if err != nil {
		return core.ErrForbidden(i18n.T(ctx, "studioui.error.session"))
	}
	if user := core.UserFrom(ctx); user == nil || !user.CanCreate() {
		return core.ErrForbidden(i18n.T(ctx, "studioui.error.creator_required"))
	}

	values, formErrors, err := parseNewProjectForm(w, r)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return core.NewError(http.StatusRequestEntityTooLarge, "too_large", "")
		}
		return core.ErrBadRequest(i18n.T(ctx, "studioui.error.form"))
	}
	if formErrors.Has() {
		return m.renderNewProject(w, r, http.StatusUnprocessableEntity, newProjectView{Values: values, Errors: formErrors})
	}

	size, _ := pageSizeFor(values.PageSize)
	pageSize, width, height := size.Value, size.Width, size.Height
	if values.Format == "webtoon" {
		pageSize, width, height = "webtoon", 1600, 2400
	}
	project, err := m.studio.CreateProject(ctx, actor, studio.NewProject{
		Title: values.Title, Premise: values.Premise, Style: values.Style, Format: values.Format,
		PageSize: pageSize, Language: values.Language, Genres: values.Genres, Tags: []string{},
		PageWidth: width, PageHeight: height, SourceText: values.SourceText,
	})
	if err != nil {
		switch {
		case errors.Is(err, studio.ErrForbidden):
			return core.ErrForbidden(i18n.T(ctx, "studioui.error.creator_required"))
		case errors.Is(err, studio.ErrInvalid), errors.Is(err, studio.ErrConflict):
			formErrors.Add("", i18n.T(ctx, "studioui.new.error.invalid"))
			return m.renderNewProject(w, r, http.StatusUnprocessableEntity, newProjectView{Values: values, Errors: formErrors})
		default:
			return err
		}
	}

	chapter, err := m.studio.CreateChapter(ctx, actor, project.ID, values.ChapterTitle)
	if err != nil {
		web.Flash(w, r, "error", i18n.T(ctx, "studioui.new.chapter_failed"), i18n.T(ctx, "studioui.new.recovery"))
		core.Redirect(w, r, storyPath(project.ID, ""))
		return nil
	}

	_, err = m.gen.EnqueueScript(ctx, actor, gen.ScriptRequest{
		ProjectID: project.ID, ChapterID: chapter.ID, Mode: values.Mode, Premise: values.Premise,
		SourceText: values.SourceText, PagesTarget: values.PagesTarget,
		// The wizard deliberately leaves routing pins blank. The router applies
		// instance/user policy and keeps the offline placeholder available.
		PinProviderID: "", PinModelID: "",
	})
	if err != nil {
		web.Flash(w, r, "error", i18n.T(ctx, "studioui.generate.failed"), safeGenerationMessage(ctx, err))
		core.Redirect(w, r, storyPath(project.ID, chapter.ID))
		return nil
	}
	web.Flash(w, r, "success", i18n.T(ctx, "studioui.new.created"), i18n.T(ctx, "studioui.new.queued"))
	core.Redirect(w, r, storyPath(project.ID, chapter.ID))
	return nil
}

func (m *Module) renderNewProject(w http.ResponseWriter, r *http.Request, status int, v newProjectView) error {
	return m.ui.Plain(w, r, status, web.PageOpts{Title: i18n.T(r.Context(), "studioui.new.title"),
		BackURL: "/studio", NoSidebar: true}, NewProjectPage(v))
}

func parseNewProjectForm(w http.ResponseWriter, r *http.Request) (newProjectValues, web.FormErrors, error) {
	values := defaultNewProjectValues(i18n.LocaleFrom(r.Context()))
	formErrors := web.FormErrors{}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" {
		return values, formErrors, errors.New("studioui: expected multipart form")
	}
	r.Body = http.MaxBytesReader(w, r.Body, newProjectMaxBody)
	if err := r.ParseMultipartForm(64 << 10); err != nil {
		return values, formErrors, err
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}

	values.Title = strings.TrimSpace(r.PostFormValue("title"))
	values.ChapterTitle = strings.TrimSpace(r.PostFormValue("chapter_title"))
	values.Mode = strings.TrimSpace(r.PostFormValue("mode"))
	values.Premise = strings.TrimSpace(r.PostFormValue("premise"))
	values.Style = strings.TrimSpace(r.PostFormValue("style"))
	values.Format = strings.TrimSpace(r.PostFormValue("format"))
	values.Language = strings.TrimSpace(r.PostFormValue("language"))
	// A disabled page-size control (webtoon) posts nothing; keep the default.
	if size := strings.TrimSpace(r.PostFormValue("page_size")); size != "" {
		values.PageSize = size
	}
	values.SourceText = r.PostFormValue("source_text")
	values.Genres = normalizeFormList(r.MultipartForm.Value["genres"])
	values.PagesTarget, err = strconv.Atoi(strings.TrimSpace(r.PostFormValue("pages_target")))
	if err != nil || values.PagesTarget < 1 || values.PagesTarget > 256 {
		formErrors.Add("pages_target", i18n.T(r.Context(), "studioui.new.error.pages"))
	}

	if values.Title == "" || utf8.RuneCountInString(values.Title) > 160 {
		formErrors.Add("title", i18n.T(r.Context(), "studioui.new.error.title"))
	}
	if values.ChapterTitle == "" || utf8.RuneCountInString(values.ChapterTitle) > 160 {
		formErrors.Add("chapter_title", i18n.T(r.Context(), "studioui.new.error.chapter_title"))
	}
	if !contains(styleValues, values.Style) {
		formErrors.Add("style", i18n.T(r.Context(), "studioui.new.error.style"))
	}
	if !contains(formatValues, values.Format) {
		formErrors.Add("format", i18n.T(r.Context(), "studioui.new.error.format"))
	}
	if _, ok := pageSizeFor(values.PageSize); !ok {
		formErrors.Add("page_size", i18n.T(r.Context(), "studioui.new.error.page_size"))
	}
	if !contains(languageValues, values.Language) {
		formErrors.Add("language", i18n.T(r.Context(), "studioui.new.error.language"))
	}
	for _, genre := range values.Genres {
		if !contains(genreValues, genre) {
			formErrors.Add("genres", i18n.T(r.Context(), "studioui.new.error.genres"))
			break
		}
	}

	upload, uploadSet := readSourceUpload(r, formErrors)
	pasteSet := strings.TrimSpace(values.SourceText) != ""
	if uploadSet {
		values.SourceText = upload
	}
	switch values.Mode {
	case "premise":
		if values.Premise == "" || utf8.RuneCountInString(values.Premise) > 4000 {
			formErrors.Add("premise", i18n.T(r.Context(), "studioui.new.error.premise"))
		}
		if pasteSet || uploadSet {
			formErrors.Add("source_text", i18n.T(r.Context(), "studioui.new.error.premise_source"))
		}
		values.SourceText = ""
	case "adapt":
		values.Premise = ""
		if pasteSet == uploadSet {
			formErrors.Add("source_text", i18n.T(r.Context(), "studioui.new.error.source_one"))
		}
	default:
		formErrors.Add("mode", i18n.T(r.Context(), "studioui.new.error.mode"))
	}
	if len(values.SourceText) > maxSourceBytes {
		formErrors.Add("source_text", i18n.T(r.Context(), "studioui.new.error.source_size"))
	}
	if !utf8.ValidString(values.SourceText) {
		formErrors.Add("source_text", i18n.T(r.Context(), "studioui.new.error.source_utf8"))
	}
	return values, formErrors, nil
}

func readSourceUpload(r *http.Request, formErrors web.FormErrors) (string, bool) {
	if r.MultipartForm == nil {
		return "", false
	}
	for field, files := range r.MultipartForm.File {
		if field != "source_file" && len(files) > 0 {
			formErrors.Add("source_file", i18n.T(r.Context(), "studioui.new.error.upload"))
		}
	}
	files := r.MultipartForm.File["source_file"]
	if len(files) == 0 {
		return "", false
	}
	if len(files) != 1 {
		formErrors.Add("source_file", i18n.T(r.Context(), "studioui.new.error.upload"))
		return "", true
	}
	fh := files[0]
	if !strings.EqualFold(filepath.Ext(fh.Filename), ".txt") {
		formErrors.Add("source_file", i18n.T(r.Context(), "studioui.new.error.upload_type"))
	}
	f, err := fh.Open()
	if err != nil {
		formErrors.Add("source_file", i18n.T(r.Context(), "studioui.new.error.upload"))
		return "", true
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxSourceBytes+1))
	if err != nil {
		formErrors.Add("source_file", i18n.T(r.Context(), "studioui.new.error.upload"))
		return "", true
	}
	if len(b) > maxSourceBytes {
		formErrors.Add("source_file", i18n.T(r.Context(), "studioui.new.error.source_size"))
	}
	if !utf8.Valid(b) {
		formErrors.Add("source_file", i18n.T(r.Context(), "studioui.new.error.source_utf8"))
	}
	if strings.TrimSpace(string(b)) == "" {
		formErrors.Add("source_file", i18n.T(r.Context(), "studioui.new.error.source_empty"))
	}
	return string(b), true
}

func normalizeFormList(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	return out
}

func safeGenerationMessage(ctx context.Context, err error) string {
	var generateErr *ai.GenerateError
	if errors.As(err, &generateErr) {
		switch generateErr.Code {
		case ai.CodeNoProvider:
			return i18n.T(ctx, "studioui.generate.no_provider")
		case ai.CodeLocalOnly:
			return i18n.T(ctx, "studioui.generate.local_only")
		case ai.CodeSpendLimit:
			return i18n.T(ctx, "studioui.generate.spend_limit")
		case ai.CodeApproval:
			return i18n.T(ctx, "studioui.generate.approval")
		case ai.CodeBadInput:
			return i18n.T(ctx, "studioui.generate.bad_input")
		case ai.CodeProviderAuth:
			return i18n.T(ctx, "studioui.generate.provider_auth")
		default:
			return i18n.T(ctx, "studioui.generate.try_again")
		}
	}
	switch {
	case errors.Is(err, studio.ErrLocked):
		return i18n.T(ctx, "studioui.generate.locked")
	case errors.Is(err, studio.ErrForbidden), errors.Is(err, studio.ErrNotFound):
		return i18n.T(ctx, "studioui.generate.unavailable")
	default:
		return i18n.T(ctx, "studioui.generate.try_again")
	}
}
