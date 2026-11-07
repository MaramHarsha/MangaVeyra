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
	"regexp"
	"sort"
	"strings"
	"time"

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

var safeName = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

func (s *service) build(ctx context.Context, p prepared, progress func(int, int) error) ([]byte, Result, error) {
	req := p.input.Request
	pages := append([]pagedoc.Page(nil), p.pages...)
	if req.CreditsPage {
		pages = append(pages, s.credits(p))
	}
	result := Result{Pages: len(pages)}
	stem := strings.Trim(safeName.ReplaceAllString(p.project.Title, "-"), "-")
	if stem == "" {
		stem = "mangaveyra"
	}
	if len(stem) > 80 {
		stem = stem[:80]
	}
	stem += fmt.Sprintf("-ch-%03d", p.chapter.Number)
	var out boundedBuffer
	if req.Format == FormatPDF {
		t, _ := findTrim(req.TrimID)
		mw, mh := t.WidthMM+2*t.BleedMM, t.HeightMM+2*t.BleedMM
		pdf := canvaspdf.New(&out, mw, mh, nil)
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
			c, err := s.renderer.Canvas(ctx, page, compose.Options{Lang: req.Lang, Scale: scale, Placeholder: true})
			if err != nil {
				return nil, result, err
			}
			c.Transform(canvas.Identity.Translate(t.BleedMM+(t.WidthMM-float64(page.Width)*fit)/2, t.BleedMM+(t.HeightMM-float64(page.Height)*fit)/2).Scale(fit, fit))
			c.RenderTo(pdf)
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
		png, err := s.renderer.PNG(ctx, page, compose.Options{Lang: req.Lang, Scale: scale, Placeholder: true})
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
		manga := "YesAndRightToLeft"
		if p.project.Format != "manga_rtl" {
			manga = "No"
		}
		info := struct {
			XMLName     xml.Name `xml:"ComicInfo"`
			Title       string
			Series      string
			Number      int
			PageCount   int
			LanguageISO string
			Manga       string
		}{Title: p.chapter.Title, Series: p.project.Title, Number: p.chapter.Number, PageCount: len(pages), LanguageISO: req.Lang, Manga: manga}
		if info.LanguageISO == "" {
			info.LanguageISO = p.project.Language
		}
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
	result.Bytes = int64(out.Len())
	return out.Bytes(), result, nil
}
func (s *service) credits(p prepared) pagedoc.Page {
	page := pagedoc.New(db.NewID(), p.project.ID, p.chapter.ID, len(p.pages)+1, p.project.PageWidth, p.project.PageHeight, pagedoc.DirLTR)
	text := "CONTRIBUTIONS\n\n" + p.project.Title + "\n" + p.chapter.Title + "\n\n"
	for _, role := range []string{"script", "art", "lettering"} {
		value := p.chapter.AILabel[role]
		switch value {
		case "ai":
			value = "AI"
		case "ai_assisted":
			value = "Human + AI assistance"
		case "human":
			value = "Human"
		default:
			value = "Not recorded"
		}
		text += strings.ToUpper(role) + " · " + value + "\n\n"
	}
	text += "Created with MangaVeyra.\nHuman and AI contributions are recorded by role."
	lang := p.input.Request.Lang
	if lang == "" {
		lang = p.project.Language
	}
	if strings.HasPrefix(lang, "ja") {
		text = "制作情報\n\n" + p.project.Title + "\n" + p.chapter.Title + "\n\n"
		for _, role := range []string{"script", "art", "lettering"} {
			label := map[string]string{"script": "脚本", "art": "作画", "lettering": "写植"}[role]
			value := map[string]string{"human": "人間", "ai": "AI", "ai_assisted": "人間とAIの共同制作"}[p.chapter.AILabel[role]]
			if value == "" {
				value = "未記録"
			}
			text += label + "・" + value + "\n\n"
		}
		text += "MangaVeyraで制作。\n人間とAIの担当を役割ごとに記載しています。"
	}
	if lang == "" {
		lang = "en"
	}

	page.Bubbles = []pagedoc.Bubble{{ID: db.NewID(), Kind: pagedoc.BubbleCaption, Shape: pagedoc.ShapeNone, Box: pagedoc.Rect{X: float64(page.Width) * .1, Y: float64(page.Height) * .15, W: float64(page.Width) * .8, H: float64(page.Height) * .7}, Style: pagedoc.BubbleStyle{AutoFit: true, Size: 44}, Texts: map[string]pagedoc.BubbleText{lang: {Text: text, Status: "approved", Source: "human"}}}}
	return page
}
