package export

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/MaramHarsha/MangaVeyra/internal/assets"
	"github.com/MaramHarsha/MangaVeyra/internal/compose"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/lettering"
	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
	"github.com/MaramHarsha/MangaVeyra/internal/pagestore"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/tdewolff/canvas"
	canvaspdf "github.com/tdewolff/canvas/renderers/pdf"
)

const maxPages = 256

var ErrInvalid = errors.New("export: invalid request")
var ErrChanged = errors.New("export: chapter changed; create a new export")
var ErrActive = errors.New("export: an export is already active")

type service struct {
	db        *db.DB
	queue     *jobs.Queue
	studio    studio.Service
	pages     pagestore.Service
	renderer  compose.Renderer
	assets    *assets.Service
	lettering lettering.Engine
}

var _ Service = (*service)(nil)
var _ QueueService = (*service)(nil)

type pageRevision struct {
	ID      string `json:"id" db:"id"`
	Version int    `json:"version" db:"version"`
	Number  int    `json:"number" db:"number"`
}
type exportInput struct {
	Schema          int            `json:"schema"`
	Request         Request        `json:"request"`
	TokenID         string         `json:"token_id,omitempty"`
	ChapterRevision time.Time      `json:"chapter_revision"`
	Pages           []pageRevision `json:"pages"`
}
type prepared struct {
	input   exportInput
	project studio.Project
	chapter studio.Chapter
	pages   []pagedoc.Page
}

func normalize(req Request) (Request, error) {
	req.ProjectID = strings.TrimSpace(req.ProjectID)
	req.ChapterID = strings.TrimSpace(req.ChapterID)
	req.Format = strings.ToLower(strings.TrimSpace(req.Format))
	req.Lang = strings.TrimSpace(req.Lang)
	if !db.ValidID(req.ProjectID) || !db.ValidID(req.ChapterID) || len(req.Lang) > 35 || len(req.PageIDs) > maxPages {
		return req, ErrInvalid
	}
	switch req.Format {
	case FormatPNG, FormatCBZ, FormatPDF:
	default:
		return req, ErrInvalid
	}
	if req.DPI != 0 && (req.DPI < 72 || req.DPI > 600) {
		return req, ErrInvalid
	}
	if req.TrimID == "" {
		req.TrimID = "b5"
	}
	if _, ok := findTrim(req.TrimID); !ok {
		return req, ErrInvalid
	}
	seen := map[string]bool{}
	for _, id := range req.PageIDs {
		if !db.ValidID(id) || seen[id] {
			return req, ErrInvalid
		}
		seen[id] = true
	}
	return req, nil
}
func findTrim(id string) (Trim, bool) {
	for _, t := range Trims {
		if t.ID == id {
			return t, true
		}
	}
	return Trim{}, false
}
func (s *service) prepare(ctx context.Context, actor studio.Actor, req Request) (prepared, error) {
	req, err := normalize(req)
	if err != nil {
		return prepared{}, err
	}
	if err := s.studio.Authorize(ctx, actor, req.ProjectID, studio.ActRead); err != nil {
		return prepared{}, err
	}
	project, err := s.studio.GetProject(ctx, req.ProjectID)
	if err != nil {
		return prepared{}, err
	}
	chapter, err := s.studio.GetChapter(ctx, req.ChapterID)
	if err != nil {
		return prepared{}, err
	}
	if chapter.ProjectID != project.ID {
		return prepared{}, studio.ErrForbidden
	}
	docs, err := s.pages.ChapterPages(ctx, chapter.ID)
	if err != nil {
		return prepared{}, err
	}
	if len(docs) > maxPages {
		return prepared{}, ErrInvalid
	}
	selected := map[string]bool{}
	for _, id := range req.PageIDs {
		selected[id] = true
	}
	var pages []pagedoc.Page
	for _, p := range docs {
		if p.ProjectID != project.ID || p.ChapterID != chapter.ID {
			return prepared{}, studio.ErrForbidden
		}
		if len(selected) == 0 || selected[p.ID] {
			pages = append(pages, p)
		}
	}
	if req.Lang == "" {
		req.Lang = project.Language
	}
	if len(pages) == 0 || (len(selected) > 0 && len(pages) != len(selected)) {
		return prepared{}, ErrInvalid
	}
	sort.SliceStable(pages, func(i, j int) bool {
		if pages[i].Number == pages[j].Number {
			return pages[i].ID < pages[j].ID
		}
		return pages[i].Number < pages[j].Number
	})
	revs := make([]pageRevision, len(pages))
	for i, p := range pages {
		revs[i] = pageRevision{p.ID, p.Version, p.Number}
	}
	return prepared{input: exportInput{Schema: 1, Request: req, TokenID: actor.TokenID, ChapterRevision: chapter.UpdatedAt, Pages: revs}, project: project, chapter: chapter, pages: pages}, nil
}
func (s *service) verifyTx(ctx context.Context, tx *db.Tx, actor studio.Actor, in exportInput) error {
	if err := s.studio.AuthorizeTx(ctx, tx, actor, in.Request.ProjectID, studio.ActRead); err != nil {
		return err
	}
	suffix := ""
	if tx.Dialect() == db.Postgres {
		suffix = " FOR UPDATE"
	}
	var ch struct {
		ProjectID string    `db:"project_id"`
		UpdatedAt time.Time `db:"updated_at"`
	}
	if err := tx.Get(ctx, &ch, `SELECT project_id, updated_at FROM chapters WHERE id = ?`+suffix, in.Request.ChapterID); err != nil {
		return err
	}
	if ch.ProjectID != in.Request.ProjectID {
		return studio.ErrForbidden
	}
	if !ch.UpdatedAt.UTC().Equal(in.ChapterRevision.UTC()) {
		return ErrChanged
	}
	var rows []pageRevision
	if err := tx.Select(ctx, &rows, `SELECT id, version, number FROM pages WHERE chapter_id = ? ORDER BY number, id`+suffix, in.Request.ChapterID); err != nil {
		return err
	}
	selected := map[string]bool{}
	for _, id := range in.Request.PageIDs {
		selected[id] = true
	}
	var current []pageRevision
	for _, r := range rows {
		if len(selected) == 0 || selected[r.ID] {
			current = append(current, r)
		}
	}
	if len(current) != len(in.Pages) {
		return ErrChanged
	}
	for i, r := range current {
		if r != in.Pages[i] {
			return ErrChanged
		}
	}
	return nil
}
func (s *service) Enqueue(ctx context.Context, actor studio.Actor, req Request) (*jobs.Job, error) {
	p, err := s.prepare(ctx, actor, req)
	if err != nil {
		return nil, err
	}
	return s.queue.Enqueue(ctx, jobs.EnqueueParams{Type: JobType, Executor: jobs.ExecutorGo, Scope: jobs.Scope{Kind: jobs.ScopeProject, ProjectID: p.project.ID}, UserID: actor.UserID, Input: p.input, MaxAttempts: 1, Guard: func(ctx context.Context, tx *db.Tx) error {
		if err := s.verifyTx(ctx, tx, actor, p.input); err != nil {
			return err
		}
		var active int
		if err := tx.Get(ctx, &active, `SELECT COUNT(*) FROM jobs WHERE type = ? AND project_id = ? AND user_id = ? AND state IN (?, ?)`, JobType, p.project.ID, actor.UserID, jobs.StateQueued, jobs.StateRunning); err != nil {
			return err
		}
		if active != 0 {
			return ErrActive
		}
		return nil
	}})
}
func (s *service) Export(ctx context.Context, userID string, req Request) (Result, error) {
	actor, err := s.studio.ActorFromUser(ctx, userID)
	if err != nil {
		return Result{}, err
	}
	p, err := s.prepare(ctx, actor, req)
	if err != nil {
		return Result{}, err
	}
	data, result, err := s.build(ctx, p, nil)
	if err != nil {
		return Result{}, err
	}
	stage, err := s.stage(ctx, p, actor.UserID, data, result)
	if err != nil {
		return Result{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = s.assets.DiscardStaged(context.WithoutCancel(ctx), stage)
		}
	}()
	err = s.db.WithTx(ctx, func(tx *db.Tx) error {
		if err := s.verifyTx(ctx, tx, actor, p.input); err != nil {
			return err
		}
		_, err := s.assets.CommitStagedTx(ctx, tx, stage)
		return err
	})
	if err != nil {
		return Result{}, err
	}
	committed = true
	result.AssetID = stage.Asset().ID
	result.URL = s.assets.URL(stage.Asset(), time.Hour)
	return result, nil
}
func (s *service) stage(ctx context.Context, p prepared, userID string, data []byte, result Result) (*assets.StagedAsset, error) {
	return s.assets.Stage(ctx, assets.SaveParams{OwnerID: userID, ProjectID: p.project.ID, Kind: "export", Source: assets.SourceSystem, ContentType: result.ContentType, Data: data, Meta: map[string]any{"filename": result.Filename, "chapter_id": p.chapter.ID, "pages": p.input.Pages, "format": p.input.Request.Format}})
}
func decodeInput(data []byte) (exportInput, error) {
	var in exportInput
	if len(data) > 1<<20 {
		return in, ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&in); err != nil {
		return in, ErrInvalid
	}
	var extra any
	if d.Decode(&extra) != io.EOF || in.Schema != 1 || in.ChapterRevision.IsZero() || len(in.Pages) == 0 || len(in.Pages) > maxPages {
		return in, ErrInvalid
	}
	_, err := normalize(in.Request)
	if err != nil {
		return in, err
	}
	return in, nil
}
func (s *service) handle(ctx context.Context, run *jobs.Run) error {
	j := run.Job
	if j == nil || j.Type != JobType || j.Executor != jobs.ExecutorGo || j.Scope.Kind != jobs.ScopeProject || j.Scope.Validate() != nil {
		return jobs.Permanent(jobs.ErrCodeBadInput, ErrInvalid)
	}
	in, err := decodeInput(j.Input)
	if err != nil || in.Request.ProjectID != j.Scope.ProjectID {
		return jobs.Permanent(jobs.ErrCodeBadInput, ErrInvalid)
	}
	actor := studio.Actor{UserID: j.UserID, TokenID: in.TokenID}
	// Reauthorize and reject stale work before rendering; repeat under the fence.
	if err = s.db.WithTx(ctx, func(tx *db.Tx) error { return s.verifyTx(ctx, tx, actor, in) }); err != nil {
		return jobs.Permanent(jobs.ErrCodeBadInput, errors.New("The chapter changed or export access is no longer available."))
	}
	p, err := s.prepare(ctx, actor, in.Request)
	if err != nil {
		return jobs.Permanent(jobs.ErrCodeBadInput, errors.New("The chapter cannot be exported."))
	}
	p.input = in
	data, result, err := s.build(ctx, p, func(i, n int) error { return run.Progress(ctx, float64(i)/float64(n+1), i, n+1, "Rendering pages") })
	if err != nil {
		return exportJobError(ctx, err)
	}
	stage, err := s.stage(ctx, p, j.UserID, data, result)
	if err != nil {
		return exportJobError(ctx, err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = s.assets.DiscardStaged(context.WithoutCancel(ctx), stage)
		}
	}()
	result.AssetID = stage.Asset().ID
	// URL signing is pure CPU. Refresh it again when presenting the result.
	result.URL = s.assets.URL(stage.Asset(), time.Hour)
	err = run.CommitSideEffect(ctx, func(ctx context.Context, tx *db.Tx) (any, error) {
		if err := s.verifyTx(ctx, tx, actor, in); err != nil {
			return nil, err
		}
		if _, err := s.assets.CommitStagedTx(ctx, tx, stage); err != nil {
			return nil, err
		}
		return result, nil
	})
	if err != nil {
		return exportJobError(ctx, err)
	}
	committed = true
	return nil
}
func exportJobError(ctx context.Context, err error) error {
	if errors.Is(err, jobs.ErrLeaseLost) {
		return jobs.ErrLeaseLost
	}
	if ctx.Err() != nil || errors.Is(err, jobs.ErrCanceled) {
		return jobs.Permanent(jobs.ErrCodeCanceled, errors.New("Export canceled."))
	}
	if errors.Is(err, ErrChanged) || errors.Is(err, studio.ErrForbidden) {
		return jobs.Permanent(jobs.ErrCodeBadInput, errors.New("The chapter changed or export access is no longer available. Create a new export."))
	}
	return jobs.Permanent(jobs.ErrCodeBadInput, errors.New("Export could not be completed. Check the pages and output size, then try again."))
}

type boundedBuffer struct{ bytes.Buffer }

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > assets.MaxBytes {
		return 0, errors.New("export: file exceeds 64 MiB limit")
	}
	return b.Buffer.Write(p)
}

const maxStemRunes = 80

// filenameStem keeps the title readable in any script: letters, marks and
// digits survive (Devanagari matras included), white space collapses to '-',
// and only path separators, characters Windows reserves, control and
// invisible format characters are removed. The result is capped by runes.
func filenameStem(title string) string {
	var b strings.Builder
	dash := false
	n := 0
	for _, r := range title {
		if unicode.IsSpace(r) || r == '-' {
			dash = b.Len() > 0
			continue
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || strings.ContainsRune(`/\:*?"<>|`, r) || r == utf8.RuneError {
			continue
		}
		if dash {
			if n+2 > maxStemRunes {
				break
			}
			b.WriteByte('-')
			n++
			dash = false
		}
		if n >= maxStemRunes {
			break
		}
		b.WriteRune(r)
		n++
	}
	stem := strings.Trim(b.String(), ".-_ ")
	if !strings.ContainsFunc(stem, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) {
		return "mangaveyra"
	}
	return stem
}

// asciiFilename is the Content-Disposition fallback for clients without RFC
// 6266 filename* support: ASCII letters, digits, '-', '_' and the extension.
// A title without ASCII letters becomes "mangaveyra-ch-001".
func asciiFilename(name string) string {
	ext := ""
	if i := strings.LastIndexByte(name, '.'); i > 0 && len(name)-i <= 5 {
		name, ext = name[:i], name[i:]
	}
	var b strings.Builder
	for _, r := range name {
		if r < utf8.RuneSelf && (unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_') {
			b.WriteRune(r)
		} else if b.Len() > 0 && !strings.HasSuffix(b.String(), "-") {
			b.WriteByte('-')
		}
	}
	stem := strings.Trim(b.String(), "-")
	title := name
	if i := strings.LastIndex(name, "-ch-"); i >= 0 {
		title = name[:i]
	}
	if !strings.ContainsFunc(title, func(r rune) bool { return r < utf8.RuneSelf && (unicode.IsLetter(r) || unicode.IsDigit(r)) }) {
		stem = strings.Trim("mangaveyra-"+stem, "-")
	}
	safeExt := ""
	for _, r := range ext {
		if r < utf8.RuneSelf && (unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.') {
			safeExt += string(r)
		}
	}
	return stem + safeExt
}

// exportLanguage is the canonical content language of an export, falling back
// to the project language; unknown stored tags pass through unchanged.
func exportLanguage(req Request, project studio.Project) string {
	lang := strings.TrimSpace(req.Lang)
	if lang == "" {
		lang = project.Language
	}
	if canonical := pagedoc.NormalizeContentLanguage(lang); canonical != "" {
		return canonical
	}
	if lang == "" {
		return "en"
	}
	return lang
}

// comicInfoManga maps the project format to ComicInfo's Manga field: right to
// left manga, top-to-bottom webtoon (manga read in page order) and
// left-to-right comics. It depends on the reading direction, not the language.
func comicInfoManga(format string) string {
	switch format {
	case "manga_rtl":
		return "YesAndRightToLeft"
	case "webtoon":
		return "Yes"
	}
	return "No"
}

func (s *service) build(ctx context.Context, p prepared, progress func(int, int) error) ([]byte, Result, error) {
	req := p.input.Request
	pages := append([]pagedoc.Page(nil), p.pages...)
	if req.CreditsPage {
		pages = append(pages, s.credits(p))
	}
	result := Result{Pages: len(pages)}
	stem := filenameStem(p.project.Title) + fmt.Sprintf("-ch-%03d", p.chapter.Number)
	var out boundedBuffer
	if req.Format == FormatPDF {
		t, _ := findTrim(req.TrimID)
		mw, mh := t.WidthMM+2*t.BleedMM, t.HeightMM+2*t.BleedMM
		pdf := canvaspdf.New(&out, mw, mh, nil)
		stream := streamingPDFRenderer{Renderer: pdf}
		pdf.SetInfo(p.project.Title, p.chapter.Title, "", "", "MangaVeyra")
		for i, page := range pages {
			if err := ctx.Err(); err != nil {
				return nil, result, err
			}
			if progress != nil {
				if err := progress(i, len(pages)); err != nil {
					return nil, result, err
				}
			}
			if i > 0 {
				pdf.NewPage(mw, mh)
			}
			dpi := req.DPI
			if dpi == 0 {
				dpi = 300
			}
			fit := math.Min(t.WidthMM/float64(page.Width), t.HeightMM/float64(page.Height))
			scale := fit * float64(dpi) / 25.4
			c, err := s.renderer.Canvas(ctx, page, compose.Options{Lang: req.Lang, Scale: scale})
			if err != nil {
				return nil, result, err
			}
			c.Transform(canvas.Identity.Translate(t.BleedMM+(t.WidthMM-float64(page.Width)*fit)/2, t.BleedMM+(t.HeightMM-float64(page.Height)*fit)/2).Scale(fit, fit))
			c.RenderTo(stream)
		}
		if err := pdf.Close(); err != nil {
			return nil, result, err
		}
		boxed, err := boxCanvasPDF(out.Bytes(), t, len(pages))
		if err != nil {
			return nil, result, err
		}
		if len(boxed) > assets.MaxBytes {
			return nil, result, errors.New("export: PDF exceeds file limit")
		}
		result.ContentType = "application/pdf"
		result.Filename = stem + ".pdf"
		result.ASCIIFilename = asciiFilename(result.Filename)
		result.Bytes = int64(len(boxed))
		return boxed, result, nil
	}
	z := zip.NewWriter(&out)
	for i, page := range pages {
		if err := ctx.Err(); err != nil {
			return nil, result, err
		}
		if progress != nil {
			if err := progress(i, len(pages)); err != nil {
				return nil, result, err
			}
		}
		scale := 2.0
		if req.DPI != 0 {
			t, _ := findTrim(req.TrimID)
			scale = math.Min(t.WidthMM/float64(page.Width), t.HeightMM/float64(page.Height)) * float64(req.DPI) / 25.4
		}
		png, err := s.renderer.PNG(ctx, page, compose.Options{Lang: req.Lang, Scale: scale})
		if err != nil {
			return nil, result, err
		}
		w, err := z.Create(fmt.Sprintf("%03d.png", i+1))
		if err != nil {
			return nil, result, err
		}
		if _, err = w.Write(png); err != nil {
			return nil, result, err
		}
	}
	if req.Format == FormatCBZ {
		w, err := z.Create("ComicInfo.xml")
		if err != nil {
			return nil, result, err
		}
		info := struct {
			XMLName     xml.Name `xml:"ComicInfo"`
			Title       string
			Series      string
			Number      int
			PageCount   int
			LanguageISO string
			Manga       string
		}{Title: p.chapter.Title, Series: p.project.Title, Number: p.chapter.Number, PageCount: len(pages),
			LanguageISO: exportLanguage(req, p.project), Manga: comicInfoManga(p.project.Format)}
		if _, err = w.Write([]byte(xml.Header)); err != nil {
			return nil, result, err
		}
		if err = xml.NewEncoder(w).Encode(info); err != nil {
			return nil, result, err
		}
	}
	if err := z.Close(); err != nil {
		return nil, result, err
	}
	result.Filename = stem + ".zip"
	result.ContentType = "application/zip"
	if req.Format == FormatCBZ {
		result.Filename = stem + ".cbz"
		result.ContentType = "application/vnd.comicbook+zip"
	}
	result.ASCIIFilename = asciiFilename(result.Filename)
	result.Bytes = int64(out.Len())
	return out.Bytes(), result, nil
}

// creditsCopy is the contributions page in each content language. It records
// who contributed per role; it never names prices.
type creditsCopy struct {
	heading, sep           string
	roles                  [3]string // script, art, lettering
	human, ai, mixed, none string
	footer                 string
}

var creditsCopies = map[string]creditsCopy{
	"en":      {"CONTRIBUTIONS", " · ", [3]string{"SCRIPT", "ART", "LETTERING"}, "Human", "AI", "Human + AI assistance", "Not recorded", "Created with MangaVeyra.\nHuman and AI contributions are recorded by role."},
	"ja":      {"制作情報", "・", [3]string{"脚本", "作画", "写植"}, "人間", "AI", "人間とAIの共同制作", "未記録", "MangaVeyraで制作。\n人間とAIの担当を役割ごとに記載しています。"},
	"ko":      {"제작 정보", " · ", [3]string{"각본", "작화", "식자"}, "사람", "AI", "사람과 AI의 공동 작업", "기록 없음", "MangaVeyra로 제작했습니다.\n사람과 AI의 기여를 역할별로 기록합니다."},
	"zh-Hans": {"制作信息", "：", [3]string{"脚本", "作画", "嵌字"}, "人工", "AI", "人工与AI协作", "未记录", "使用 MangaVeyra 制作。\n按职能记录人工与AI的贡献。"},
	"zh-Hant": {"製作資訊", "：", [3]string{"腳本", "作畫", "嵌字"}, "人工", "AI", "人工與AI協作", "未記錄", "使用 MangaVeyra 製作。\n依職能記錄人工與AI的貢獻。"},
	"es":      {"CONTRIBUCIONES", " · ", [3]string{"GUION", "DIBUJO", "ROTULACIÓN"}, "Humano", "IA", "Humano con ayuda de IA", "Sin registrar", "Creado con MangaVeyra.\nLas contribuciones humanas y de IA se registran por función."},
	"fr":      {"CONTRIBUTIONS", " · ", [3]string{"SCÉNARIO", "DESSIN", "LETTRAGE"}, "Humain", "IA", "Humain assisté par IA", "Non renseigné", "Créé avec MangaVeyra.\nLes contributions humaines et de l'IA sont indiquées par rôle."},
	"de":      {"MITWIRKENDE", " · ", [3]string{"SKRIPT", "ZEICHNUNG", "LETTERING"}, "Mensch", "KI", "Mensch mit KI-Unterstützung", "Nicht erfasst", "Erstellt mit MangaVeyra.\nMenschliche und KI-Beiträge werden je Rolle erfasst."},
	"pt":      {"CONTRIBUIÇÕES", " · ", [3]string{"ROTEIRO", "ARTE", "LETREIRAMENTO"}, "Humano", "IA", "Humano com auxílio de IA", "Não registrado", "Criado com MangaVeyra.\nAs contribuições humanas e de IA são registradas por função."},
	"hi":      {"योगदान", " · ", [3]string{"पटकथा", "चित्रांकन", "अक्षरांकन"}, "मानव", "एआई", "मानव और एआई सहायता", "दर्ज नहीं", "MangaVeyra से बनाया गया।\nमानव और एआई योगदान भूमिका के अनुसार दर्ज किए गए हैं।"},
}

func (s *service) credits(p prepared) pagedoc.Page {
	page := pagedoc.New(db.NewID(), p.project.ID, p.chapter.ID, len(p.pages)+1, p.project.PageWidth, p.project.PageHeight, pagedoc.DirLTR)
	lang := exportLanguage(p.input.Request, p.project)
	c, ok := creditsCopies[lang]
	if !ok {
		c = creditsCopies["en"]
	}
	text := c.heading + "\n\n" + p.project.Title + "\n" + p.chapter.Title + "\n\n"
	for i, role := range []string{"script", "art", "lettering"} {
		value := map[string]string{"human": c.human, "ai": c.ai, "ai_assisted": c.mixed}[p.chapter.AILabel[role]]
		if value == "" {
			value = c.none
		}
		text += c.roles[i] + c.sep + value + "\n\n"
	}
	text += c.footer
	// The lettering engine picks the language's font and falls back per glyph
	// run, so a title in another script still renders with real glyphs.
	page.Bubbles = []pagedoc.Bubble{{ID: db.NewID(), Kind: pagedoc.BubbleCaption, Shape: pagedoc.ShapeNone, Box: pagedoc.Rect{X: float64(page.Width) * .1, Y: float64(page.Height) * .15, W: float64(page.Width) * .8, H: float64(page.Height) * .7}, Style: pagedoc.BubbleStyle{AutoFit: true, Size: 44}, Texts: map[string]pagedoc.BubbleText{lang: {Text: text, Status: "approved", Source: "human"}}}}
	return page
}
