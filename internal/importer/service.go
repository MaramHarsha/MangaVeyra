package importer

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"io"
	"log/slog"
	"math"
	"os"
	"path"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/MaramHarsha/MangaVeyra/internal/assets"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
	"github.com/MaramHarsha/MangaVeyra/internal/pagestore"
	"github.com/MaramHarsha/MangaVeyra/internal/storage"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
)

// Import kinds (imports.kind).
const (
	KindCBZ    = "cbz"
	KindCBR    = "cbr"
	KindPDF    = "pdf"
	KindImages = "images"
)

// Import statuses (imports.status).
const (
	StatusQueued   = "queued"
	StatusRunning  = "running"
	StatusDone     = "done"
	StatusFailed   = "failed"
	StatusCanceled = "canceled"
)

// Phases shown in the queue.
const (
	PhaseReading   = "reading"
	PhaseDetecting = "detecting"
	PhaseCreating  = "creating"
)

// Job types: the import itself (Go executor) and the worker capability it uses.
const (
	JobImport = "import.comic"
	JobDetect = "detect.panels"
)

// detectTimeout bounds one page's worker detection; the page falls back to
// one full-page panel when it passes.
var detectTimeout = 2 * time.Minute

// failAfter stops an import once that many pages are saved (tests of resuming; -1 = never).
var failAfter = -1

// Import is one import and its progress.
type Import struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	ProjectID string    `json:"project_id,omitempty"`
	Kind      string    `json:"kind"`
	Filename  string    `json:"filename"`
	Status    string    `json:"status"`
	Progress  float64   `json:"progress"`
	Pages     int       `json:"pages"`
	JobID     string    `json:"job_id,omitempty"`
	Result    Result    `json:"result"`
	Error     string    `json:"error,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Result is the resumable state of an import (imports.result).
type Result struct {
	SourceKey string `json:"source_key,omitempty"`
	Phase     string `json:"phase,omitempty"`
	Images    int    `json:"images,omitempty"`
	Skipped   int    `json:"skipped,omitempty"`
	ChapterID string `json:"chapter_id,omitempty"`
	PagesDone int    `json:"pages_done,omitempty"`
	Detected  int    `json:"detected,omitempty"`
	Bubbles   int    `json:"bubbles,omitempty"`
	Code      string `json:"code,omitempty"`
}

// Service is the importer's API.
type Service interface {
	// Start records an import of a stored source and queues its job.
	Start(ctx context.Context, userID, kind, filename, sourceKey string, images int) (Import, error)
	Get(ctx context.Context, id string) (Import, error)
	Recent(ctx context.Context, userID string, limit int) ([]Import, error)
	// Retry resumes a failed or canceled import from where it stopped.
	Retry(ctx context.Context, userID, id string) (Import, error)
	PDFAvailable() bool
}

type service struct {
	d      *core.Deps
	log    *slog.Logger
	limits Limits
}

func newService(d *core.Deps) *service {
	log := d.Log
	if log == nil {
		log = slog.Default()
	}
	return &service{d: d, log: log, limits: DefaultLimits}
}

type importRow struct {
	ID        string         `db:"id"`
	UserID    string         `db:"user_id"`
	ProjectID sql.NullString `db:"project_id"`
	Kind      string         `db:"kind"`
	Filename  string         `db:"filename"`
	Status    string         `db:"status"`
	Progress  float64        `db:"progress"`
	JobID     sql.NullString `db:"job_id"`
	Pages     int            `db:"pages"`
	Result    string         `db:"result"`
	Error     string         `db:"error"`
	CreatedAt time.Time      `db:"created_at"`
	UpdatedAt time.Time      `db:"updated_at"`
}

const importCols = `id, user_id, project_id, kind, filename, status, progress, job_id, pages, result, error, created_at, updated_at`

func (r importRow) imp() Import {
	i := Import{ID: r.ID, UserID: r.UserID, ProjectID: r.ProjectID.String, Kind: r.Kind, Filename: r.Filename, Status: r.Status,
		Progress: r.Progress, JobID: r.JobID.String, Pages: r.Pages, Error: r.Error, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
	_ = json.Unmarshal([]byte(r.Result), &i.Result)
	return i
}

func (s *service) PDFAvailable() bool { return PDFTool() != "" }

func (s *service) Get(ctx context.Context, id string) (Import, error) {
	var r importRow
	if !db.ValidID(id) {
		return Import{}, studio.ErrNotFound
	}
	if err := s.d.DB.Get(ctx, &r, `SELECT `+importCols+` FROM imports WHERE id = ?`, id); err != nil {
		if db.IsNotFound(err) {
			return Import{}, studio.ErrNotFound
		}
		return Import{}, err
	}
	return r.imp(), nil
}

func (s *service) Recent(ctx context.Context, userID string, limit int) ([]Import, error) {
	if limit <= 0 || limit > 100 {
		limit = 10
	}
	var rows []importRow
	if err := s.d.DB.Select(ctx, &rows, `SELECT `+importCols+` FROM imports WHERE user_id = ? AND kind IN ('cbz', 'cbr', 'pdf', 'images')
		ORDER BY created_at DESC, id DESC LIMIT ?`, userID, limit); err != nil {
		return nil, err
	}
	out := make([]Import, len(rows))
	for i, r := range rows {
		out[i] = r.imp()
	}
	return out, nil
}

func (s *service) Start(ctx context.Context, userID, kind, filename, sourceKey string, images int) (Import, error) {
	switch kind {
	case KindCBZ, KindCBR, KindPDF, KindImages:
	default:
		return Import{}, studio.ErrInvalid
	}
	filename = cutUTF8(strings.ToValidUTF8(filename, "�"), maxFilenameBytes)
	id := db.NewID()
	now := db.Now()
	res := Result{SourceKey: sourceKey, Images: images}
	err := s.d.DB.WithTx(ctx, func(tx *db.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO imports (id, user_id, kind, filename, status, result, created_at, updated_at)
			VALUES (?, ?, ?, ?, 'queued', ?, ?, ?)`, id, userID, kind, filename, db.MustJSON(res), now, now); err != nil {
			return err
		}
		return s.enqueueTx(ctx, tx, id, userID)
	})
	if err != nil {
		return Import{}, err
	}
	return s.Get(ctx, id)
}

// maxFilenameBytes bounds the stored import name.
const maxFilenameBytes = 200

// cutUTF8 shortens s to at most n bytes without splitting a character.
func cutUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func (s *service) enqueueTx(ctx context.Context, tx *db.Tx, id, userID string) error {
	j, err := s.d.Jobs.EnqueueTx(ctx, tx, jobs.EnqueueParams{Type: JobImport, Scope: jobs.Scope{Kind: jobs.ScopeNone}, UserID: userID,
		Input: map[string]string{"import_id": id}, MaxAttempts: 1, Batch: false, DedupeKey: "import:" + id})
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE imports SET job_id = ? WHERE id = ?`, j.ID, id)
	return err
}

func (s *service) Retry(ctx context.Context, userID, id string) (Import, error) {
	imp, err := s.Get(ctx, id)
	if err != nil || imp.UserID != userID {
		return Import{}, studio.ErrNotFound
	}
	if imp.Status != StatusFailed && imp.Status != StatusCanceled {
		return Import{}, studio.ErrConflict
	}
	if imp.Result.SourceKey == "" {
		return Import{}, studio.ErrInvalid
	}
	err = s.d.DB.WithTx(ctx, func(tx *db.Tx) error {
		res := imp.Result
		res.Code = ""
		if _, err := tx.Exec(ctx, `UPDATE imports SET status = 'queued', error = '', result = ?, updated_at = ? WHERE id = ? AND status IN ('failed', 'canceled')`,
			db.MustJSON(res), db.Now(), id); err != nil {
			return err
		}
		return s.enqueueTx(ctx, tx, id, userID)
	})
	if err != nil {
		return Import{}, err
	}
	return s.Get(ctx, id)
}

func (s *service) update(ctx context.Context, q db.Querier, imp *Import) error {
	imp.UpdatedAt = db.Now()
	_, err := q.Exec(ctx, `UPDATE imports SET status = ?, progress = ?, pages = ?, project_id = ?, result = ?, error = ?, updated_at = ? WHERE id = ?`,
		imp.Status, imp.Progress, imp.Pages, db.NullString(imp.ProjectID), db.MustJSON(imp.Result), imp.Error, imp.UpdatedAt, imp.ID)
	return err
}

// fail records a failure with a public code; the source stays for Retry.
func (s *service) fail(ctx context.Context, imp *Import, err error) error {
	code := CodeInternal
	var ref *refusal
	if errors.As(err, &ref) {
		code = ref.Code
	}
	imp.Status, imp.Result.Code = StatusFailed, code
	imp.Error = code
	if errors.Is(err, jobs.ErrCanceled) || errors.Is(err, context.Canceled) {
		imp.Status, imp.Result.Code, imp.Error = StatusCanceled, "", ""
	}
	if uerr := s.update(context.WithoutCancel(ctx), s.d.DB, imp); uerr != nil {
		s.log.Warn("importer: record failure", "import", imp.ID, "err", uerr)
	}
	if imp.Status == StatusCanceled {
		return jobs.ErrCanceled
	}
	if ref != nil {
		return jobs.Permanent(jobs.ErrCodeBadInput, errors.New("the file cannot be imported: "+ref.Code))
	}
	return err
}

// onFinish marks imports whose job ended without the handler recording it
// (an expired lease after a crash, or a cancellation before it started).
func (s *service) onFinish(ctx context.Context, j *jobs.Job) {
	if j.Type != JobImport || j.State == jobs.StateDone {
		return
	}
	var in struct {
		ImportID string `json:"import_id"`
	}
	if j.DecodeInput(&in) != nil || !db.ValidID(in.ImportID) {
		return
	}
	status := StatusFailed
	if j.State == jobs.StateCanceled {
		status = StatusCanceled
	}
	_, _ = s.d.DB.Exec(context.WithoutCancel(ctx), `UPDATE imports SET status = ?, error = CASE WHEN error = '' THEN ? ELSE error END, updated_at = ?
		WHERE id = ? AND job_id = ? AND status IN ('queued', 'running')`, status, CodeInternal, db.Now(), in.ImportID, j.ID)
}

// run is the import.comic job.
func (s *service) run(ctx context.Context, run *jobs.Run) error {
	var in struct {
		ImportID string `json:"import_id"`
	}
	if err := run.DecodeInput(&in); err != nil {
		return jobs.Permanent(jobs.ErrCodeBadInput, err)
	}
	imp, err := s.Get(ctx, in.ImportID)
	if err != nil {
		return jobs.Permanent(jobs.ErrCodeBadInput, err)
	}
	if imp.Status == StatusDone {
		return nil
	}
	imp.Status, imp.Error, imp.Result.Code, imp.Result.Phase = StatusRunning, "", "", PhaseReading
	if err := s.update(ctx, s.d.DB, &imp); err != nil {
		return err
	}
	if err := s.importPages(ctx, run, &imp); err != nil {
		return s.fail(ctx, &imp, err)
	}
	return nil
}

func (s *service) progress(ctx context.Context, run *jobs.Run, imp *Import, p float64) error {
	imp.Progress = math.Min(1, math.Max(0, p))
	if run != nil {
		if err := run.Progress(ctx, imp.Progress, imp.Result.PagesDone, imp.Result.Images, ""); err != nil {
			return err
		}
	}
	return s.update(ctx, s.d.DB, imp)
}

func (s *service) importPages(ctx context.Context, run *jobs.Run, imp *Import) error {
	st := core.MustUse[studio.Service](s.d)
	pages := core.MustUse[pagestore.Service](s.d)
	as := core.MustUse[*assets.Service](s.d)
	actor, err := st.ActorFromUser(ctx, imp.UserID)
	if err != nil {
		return err
	}
	if imp.Kind == KindPDF && !s.PDFAvailable() {
		return refuse(CodePDFUnavailable, "pdftoppm is not installed")
	}
	work, err := os.MkdirTemp("", "mv-import-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	src, err := s.fetchSource(ctx, imp.Result.SourceKey, work)
	if err != nil {
		return err
	}
	x, err := extract(ctx, imp.Kind, src, work, s.limits)
	if err != nil {
		return err
	}
	_ = os.Remove(src)
	imp.Result.Images, imp.Result.Skipped = len(x.Images), x.Skipped
	if err := s.progress(ctx, run, imp, 0.05); err != nil {
		return err
	}
	// The project and chapter are created once; a resumed import adds the remaining pages.
	if imp.ProjectID == "" || imp.Result.ChapterID == "" {
		first := x.Images[0]
		w, h := pageSize(first.Width, first.Height)
		p, err := st.CreateProject(ctx, actor, studio.NewProject{Title: projectTitle(imp.Filename), PageWidth: w, PageHeight: h})
		if err != nil {
			return fmt.Errorf("importer: create project: %w", err)
		}
		ch, err := st.CreateChapter(ctx, actor, p.ID, "Chapter 1")
		if err != nil {
			return fmt.Errorf("importer: create chapter: %w", err)
		}
		err = s.d.DB.WithTx(ctx, func(tx *db.Tx) error {
			for _, role := range []string{"script", "art", "lettering"} {
				if err := st.SetChapterAILabelTx(ctx, tx, ch.ID, role, "human"); err != nil {
					return err
				}
			}
			imp.ProjectID, imp.Result.ChapterID = p.ID, ch.ID
			return s.update(ctx, tx, imp)
		})
		if err != nil {
			return err
		}
	}
	proj, err := st.GetProject(ctx, imp.ProjectID)
	if err != nil {
		return err
	}
	direction := pagedoc.DirectionForFormat(proj.Format)
	detect := s.detectorAvailable(ctx)
	if detect {
		imp.Result.Phase = PhaseDetecting
	} else {
		imp.Result.Phase = PhaseCreating
	}
	lang := proj.Language
	for i := imp.Result.PagesDone; i < len(x.Images); i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if failAfter >= 0 && i >= failAfter {
			return errors.New("importer: stopped for a test")
		}
		img := x.Images[i]
		data, err := os.ReadFile(img.Path)
		if err != nil {
			return err
		}
		doc, staged, thumb, detected, err := s.buildPage(ctx, run, as, imp, proj, img, data, direction, lang, detect)
		if err != nil {
			for _, a := range staged {
				_ = as.DiscardStaged(context.WithoutCancel(ctx), a)
			}
			return err
		}
		err = s.d.DB.WithTx(ctx, func(tx *db.Tx) error {
			for _, a := range staged {
				if _, err := as.CommitStagedTx(ctx, tx, a); err != nil {
					return err
				}
			}
			created, err := pages.CreatePagesTx(ctx, tx, imp.Result.ChapterID, []pagedoc.Page{doc}, pagestore.Change{ActorKind: "user", ActorID: imp.UserID,
				JobID: jobID(run), Action: "import", Summary: fmt.Sprintf("Imported page %d · %s", i+1, path.Base(img.Name))})
			if err != nil {
				return err
			}
			if thumb != nil && len(created) == 1 {
				if err := pages.SetThumbTx(ctx, tx, created[0].ID, thumb.Asset().ID); err != nil {
					return err
				}
			}
			imp.Result.PagesDone = i + 1
			imp.Pages = i + 1
			if detected != nil {
				imp.Result.Detected++
				imp.Result.Bubbles += detected.bubbles
			}
			imp.Progress = 0.05 + 0.95*float64(i+1)/float64(len(x.Images))
			return s.update(ctx, tx, imp)
		})
		if err != nil {
			for _, a := range staged {
				_ = as.DiscardStaged(context.WithoutCancel(ctx), a)
			}
			return err
		}
		if run != nil {
			if err := run.Progress(ctx, imp.Progress, i+1, len(x.Images), ""); err != nil {
				return err
			}
		}
	}
	imp.Status, imp.Progress, imp.Result.Phase = StatusDone, 1, ""
	if err := s.update(ctx, s.d.DB, imp); err != nil {
		return err
	}
	if imp.Result.SourceKey != "" {
		if err := s.d.Store.Delete(context.WithoutCancel(ctx), imp.Result.SourceKey); err != nil {
			s.log.Warn("importer: remove source", "import", imp.ID, "err", err)
		}
	}
	if run != nil {
		run.SetOutput(map[string]any{"import_id": imp.ID, "project_id": imp.ProjectID, "chapter_id": imp.Result.ChapterID, "pages": imp.Pages})
	}
	return nil
}

func jobID(run *jobs.Run) string {
	if run == nil {
		return ""
	}
	return run.Job.ID
}

func (s *service) fetchSource(ctx context.Context, key, dir string) (string, error) {
	if key == "" {
		return "", refuse(CodeDamaged, "the uploaded file is gone")
	}
	obj, info, err := s.d.Store.Get(ctx, key)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return "", refuse(CodeDamaged, "the uploaded file is gone")
		}
		return "", err
	}
	defer obj.Close()
	if info.Size > s.limits.MaxSourceBytes {
		return "", refuse(CodeTooLarge, "the file is larger than %d MB", s.limits.MaxSourceBytes>>20)
	}
	p := dir + string(os.PathSeparator) + "source"
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(f, io.LimitReader(obj, s.limits.MaxSourceBytes+1)); err != nil {
		_ = f.Close()
		return "", err
	}
	return p, f.Close()
}

// pageSize keeps the image's size, scaled down to the page limit.
func pageSize(w, h int) (int, int) {
	return storage.Fit(w, h, pagedoc.MaxPageSide, pagedoc.MaxPageSide)
}

func projectTitle(filename string) string {
	t := strings.TrimSuffix(strings.TrimSpace(filename), "/")
	t = path.Base(strings.ReplaceAll(t, "\\", "/"))
	if ext := path.Ext(t); ext != "" && len(ext) <= 5 {
		t = strings.TrimSuffix(t, ext)
	}
	t = strings.TrimSpace(t)
	if t == "" || t == "." {
		t = "Imported comic"
	}
	if r := []rune(t); len(r) > 120 {
		t = string(r[:120])
	}
	return t
}

// detectorAvailable reports an online worker offering panel detection; Local
// only keeps page images on the server, so detection is skipped there.
func (s *service) detectorAvailable(ctx context.Context) bool {
	inst, err := s.d.Settings.Instance(ctx)
	if err != nil || inst.LocalOnly {
		return false
	}
	workers, err := s.d.Jobs.ListWorkers(ctx)
	if err != nil {
		return false
	}
	for _, w := range workers {
		if w.Status == jobs.WorkerOnline && slices.Contains(w.JobTypes.V, JobDetect) {
			return true
		}
	}
	return false
}

type detection struct {
	panels  []detectedPanel
	bubbles int
	bubble  []pagedoc.Bubble
}

type detectedPanel struct {
	points []pagedoc.Point // image pixels
	order  int
}

// detectOutput is the worker protocol's detect.panels output (sdk/api/worker.md).
type detectOutput struct {
	Panels []struct {
		Points [][]float64 `json:"points"`
		Order  int         `json:"order"`
	} `json:"panels"`
	Bubbles []struct {
		Points   [][]float64 `json:"points"`
		Text     string      `json:"text"`
		Vertical bool        `json:"vertical"`
	} `json:"bubbles"`
}

// buildPage stages the page's art (and per-panel crops when a worker found
// panels) and returns the page document.
func (s *service) buildPage(ctx context.Context, run *jobs.Run, as *assets.Service, imp *Import, proj studio.Project,
	img pageImage, data []byte, direction, lang string, detect bool) (pagedoc.Page, []*assets.StagedAsset, *assets.StagedAsset, *detection, error) {
	var staged []*assets.StagedAsset
	stage := func(kind, ct string, b []byte, meta map[string]any) (*assets.StagedAsset, error) {
		a, err := as.Stage(ctx, assets.SaveParams{OwnerID: imp.UserID, ProjectID: proj.ID, Kind: kind, Source: assets.SourceImport,
			ContentType: ct, Data: b, Meta: meta})
		if err != nil {
			return nil, err
		}
		staged = append(staged, a)
		return a, nil
	}
	pageArt, err := stage("import_page", img.ContentType, data, map[string]any{"import_id": imp.ID, "name": path.Base(img.Name)})
	if err != nil {
		return pagedoc.Page{}, staged, nil, nil, err
	}
	pw, ph := pageSize(img.Width, img.Height)
	sx, sy := float64(pw)/float64(img.Width), float64(ph)/float64(img.Height)
	doc := pagedoc.New(db.NewID(), proj.ID, "", 0, pw, ph, direction)
	var det *detection
	if detect && run != nil {
		det = s.detect(ctx, run, imp, proj.ID, pageArt.Asset().Key, direction, img.Width, img.Height)
	}
	decoded, _, err := storage.DecodeImage(data, s.limits.Image)
	if err != nil {
		return pagedoc.Page{}, staged, nil, nil, refuse(CodeDamaged, "%q cannot be decoded", img.Name)
	}
	layer := func(a *assets.StagedAsset) pagedoc.Layer {
		info := a.Asset()
		return pagedoc.Layer{ID: db.NewID(), Kind: pagedoc.LayerBackground, Name: "Imported art",
			Asset: &pagedoc.AssetRef{ID: info.ID, Width: info.Width, Height: info.Height}, Visible: true, Opacity: 1,
			Provenance: pagedoc.ProvenanceHuman}
	}
	if det != nil && len(det.panels) > 0 {
		format := "png"
		if img.ContentType == "image/jpeg" {
			format = "jpeg"
		}
		for i, pn := range det.panels {
			b := pagedoc.Bounds(pn.points)
			r := image.Rect(int(math.Floor(b.X)), int(math.Floor(b.Y)), int(math.Ceil(b.X+b.W)), int(math.Ceil(b.Y+b.H))).Intersect(decoded.Bounds())
			sub, ok := decoded.(interface {
				SubImage(image.Rectangle) image.Image
			})
			if !ok || r.Empty() {
				det, doc.Panels = nil, nil
				break
			}
			var buf bytes.Buffer
			ct, err := storage.Encode(&buf, sub.SubImage(r), format)
			if err != nil {
				return pagedoc.Page{}, staged, nil, nil, err
			}
			crop, err := stage("import_panel", ct, buf.Bytes(), map[string]any{"import_id": imp.ID, "panel": i + 1})
			if err != nil {
				return pagedoc.Page{}, staged, nil, nil, err
			}
			pts := make([]pagedoc.Point, len(pn.points))
			for k, p := range pn.points {
				pts[k] = pagedoc.Point{X: round1(p.X * sx), Y: round1(p.Y * sy)}
			}
			doc.Panels = append(doc.Panels, pagedoc.Panel{ID: db.NewID(), Order: i, Points: pts, PromptSource: "user", Layers: []pagedoc.Layer{layer(crop)}})
		}
	}
	if det == nil || len(doc.Panels) == 0 {
		doc.Panels = []pagedoc.Panel{{ID: db.NewID(), Points: pagedoc.RectPoints(pagedoc.Rect{W: float64(pw), H: float64(ph)}),
			PromptSource: "user", Layers: []pagedoc.Layer{layer(pageArt)}}}
		det = nil
	}
	if det != nil {
		for _, b := range det.bubble {
			bb := b
			bb.Box = pagedoc.Rect{X: round1(b.Box.X * sx), Y: round1(b.Box.Y * sy), W: round1(b.Box.W * sx), H: round1(b.Box.H * sy)}
			if bb.Box.W < 4 || bb.Box.H < 4 || len(doc.Bubbles) >= pagedoc.MaxBubbles {
				continue
			}
			center := pagedoc.Point{X: bb.Box.X + bb.Box.W/2, Y: bb.Box.Y + bb.Box.H/2}
			for _, pn := range doc.Panels {
				if pagedoc.Contains(pn.Points, center) {
					bb.PanelID = pn.ID
					break
				}
			}
			bb.ID, bb.Order = db.NewID(), len(doc.Bubbles)
			if bb.Texts != nil {
				if t, ok := bb.Texts["_"]; ok {
					delete(bb.Texts, "_")
					bb.Texts[lang] = t
				}
			}
			doc.Bubbles = append(doc.Bubbles, bb)
		}
		det.bubbles = len(doc.Bubbles)
	}
	if err := doc.Validate(); err != nil {
		// A detection the page model refuses falls back to the whole page.
		doc.Panels = []pagedoc.Panel{{ID: db.NewID(), Points: pagedoc.RectPoints(pagedoc.Rect{W: float64(pw), H: float64(ph)}),
			PromptSource: "user", Layers: []pagedoc.Layer{layer(pageArt)}}}
		doc.Bubbles = []pagedoc.Bubble{}
		det = nil
		if err := doc.Validate(); err != nil {
			return pagedoc.Page{}, staged, nil, nil, refuse(CodeDamaged, "%q cannot become a page (%v)", img.Name, err)
		}
	}
	var thumb *assets.StagedAsset
	if tb, ct, err := storage.ThumbnailBytes(data, 480, 720, s.limits.Image); err == nil {
		if thumb, err = stage("thumb", ct, tb, map[string]any{"import_id": imp.ID}); err != nil {
			return pagedoc.Page{}, staged, nil, nil, err
		}
	}
	return doc, staged, thumb, det, nil
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }

// detect runs detect.panels on a worker for one page. Any failure, a timeout
// or an unusable result returns nil: the page then becomes one panel.
func (s *service) detect(ctx context.Context, run *jobs.Run, imp *Import, projectID, key, direction string, w, h int) *detection {
	child, err := s.d.Jobs.Enqueue(ctx, jobs.EnqueueParams{Type: JobDetect, Scope: jobs.Scope{Kind: jobs.ScopeNone}, UserID: imp.UserID,
		Executor: jobs.ExecutorWorker, ProviderID: "local-worker", ParentID: run.Job.ID, Input: map[string]any{"reading": direction, "import_id": imp.ID},
		Files: map[string]string{"page": key}, MaxAttempts: 1})
	if err != nil {
		s.log.Debug("importer: queue detection", "err", err)
		return nil
	}
	wait, cancel := context.WithTimeout(ctx, detectTimeout)
	defer cancel()
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for !child.Finished() {
		select {
		case <-wait.Done():
			bg, c := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			_, _ = s.d.Jobs.Cancel(bg, child.ID)
			c()
			return nil
		case <-tick.C:
		}
		cur, err := s.d.Jobs.Get(wait, child.ID)
		if err != nil {
			return nil
		}
		child = cur
	}
	if child.State != jobs.StateDone {
		return nil
	}
	var out detectOutput
	if child.DecodeOutput(&out) != nil {
		return nil
	}
	return parseDetection(out, w, h)
}

// parseDetection keeps valid polygons inside the image, in reading order.
func parseDetection(out detectOutput, w, h int) *detection {
	det := &detection{}
	clamp := func(v, hi float64) float64 { return math.Max(0, math.Min(hi, v)) }
	toPoints := func(raw [][]float64) []pagedoc.Point {
		var pts []pagedoc.Point
		for _, p := range raw {
			if len(p) != 2 || math.IsNaN(p[0]) || math.IsNaN(p[1]) || math.IsInf(p[0], 0) || math.IsInf(p[1], 0) {
				return nil
			}
			pts = append(pts, pagedoc.Point{X: clamp(p[0], float64(w)), Y: clamp(p[1], float64(h))})
		}
		return pts
	}
	type ordered struct {
		pts   []pagedoc.Point
		order int
	}
	var panels []ordered
	for i, p := range out.Panels {
		pts := toPoints(p.Points)
		if len(pts) < 3 {
			continue
		}
		if len(pts) > pagedoc.MaxPolygonPoints {
			pts = pagedoc.RectPoints(pagedoc.Bounds(pts))
		}
		b := pagedoc.Bounds(pts)
		if b.W < float64(w)*0.03 || b.H < float64(h)*0.03 {
			continue
		}
		order := p.Order
		if order <= 0 {
			order = i + 1
		}
		panels = append(panels, ordered{pts: pts, order: order})
	}
	slices.SortStableFunc(panels, func(a, b ordered) int { return a.order - b.order })
	if len(panels) > pagedoc.MaxPanels {
		panels = panels[:pagedoc.MaxPanels]
	}
	for _, p := range panels {
		det.panels = append(det.panels, detectedPanel{points: p.pts, order: p.order})
	}
	for _, b := range out.Bubbles {
		pts := toPoints(b.Points)
		if len(pts) < 3 {
			continue
		}
		box := pagedoc.Bounds(pts)
		bub := pagedoc.Bubble{Kind: pagedoc.BubbleSpeech, Shape: pagedoc.ShapeEllipse, Box: box, Vertical: b.Vertical,
			Style: pagedoc.BubbleStyle{AutoFit: true}, Texts: map[string]pagedoc.BubbleText{}}
		if t := strings.TrimSpace(b.Text); t != "" {
			if r := []rune(t); len(r) > pagedoc.MaxTextRunes {
				t = string(r[:pagedoc.MaxTextRunes])
			}
			// OCR text is a draft for the creator to review (ADR 0013 §5).
			bub.Texts["_"] = pagedoc.BubbleText{Text: t, Status: "draft", Source: "ai"}
		}
		det.bubble = append(det.bubble, bub)
	}
	if len(det.panels) == 0 {
		return nil
	}
	return det
}
