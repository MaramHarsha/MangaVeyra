package importer

import (
	"archive/zip"
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/a-h/templ"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/storage"
	"github.com/MaramHarsha/MangaVeyra/internal/web"
)

// Cards renders the G10 "Import comics" card for a member.
type Cards interface {
	ImportCard(ctx context.Context, userID, returnTo string) (templ.Component, error)
}

// Module is the importer feature module.
type Module struct {
	d   *core.Deps
	svc *service
}

// New creates the module and provides importer.Service and importer.Cards.
func New(d *core.Deps) *Module {
	svc := newService(d)
	m := &Module{d: d, svc: svc}
	core.Provide[Service](d, svc)
	core.Provide[Cards](d, m)
	return m
}

// Name implements core.Module.
func (m *Module) Name() string { return "importer" }

// Init registers the job bookkeeping hook.
func (m *Module) Init(context.Context) error {
	m.d.Jobs.MarkNoRetry(JobImport)
	m.d.Jobs.MarkPrivateInput(JobDetect)
	m.d.Jobs.OnFinish(m.svc.onFinish)
	return nil
}

// JobHandlers implements core.JobHandlers.
func (m *Module) JobHandlers() map[string]jobs.Handler {
	return map[string]jobs.Handler{JobImport: m.svc.run}
}

// Temporary files the importer writes to the system temp directory: an
// upload's image batch (and its staging file) and an import's work directory.
// A crash can leave them behind; Start removes any older than tempMaxAge.
const (
	tempPrefix        = "mv-import-"
	uploadTempPattern = tempPrefix + "upload-*"
	workTempPattern   = tempPrefix + "work-*"
	tempMaxAge        = 24 * time.Hour
)

// Start sweeps leftover temporary files once an hour (and at start).
func (m *Module) Start(ctx context.Context) error {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		if n := SweepTemp(os.TempDir(), time.Now().Add(-tempMaxAge)); n > 0 {
			m.svc.log.Info("importer: removed leftover temporary files", "count", n)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

// SweepTemp removes the importer's temporary files and directories in dir
// last modified before cutoff and reports how many it removed. Nothing else
// in dir is touched.
func SweepTemp(dir string, cutoff time.Time) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), tempPrefix) {
			continue
		}
		fi, err := e.Info()
		if err != nil || !fi.ModTime().Before(cutoff) {
			continue
		}
		if e.Type()&os.ModeSymlink != 0 || (!e.IsDir() && !e.Type().IsRegular()) {
			continue
		}
		if os.RemoveAll(filepath.Join(dir, e.Name())) == nil {
			n++
		}
	}
	return n
}

// Routes implements core.Module. Importing creates a private project owned
// by the member, so creators and admins may import.
func (m *Module) Routes(r *core.Router) {
	r.Page("POST /imports", m.upload, core.RequireSession, core.RequireRole(core.RoleCreator))
	r.Page("GET /imports/queue", m.queue, core.RequireSession, core.RequireRole(core.RoleCreator))
	r.Page("POST /imports/{id}/retry", m.retry, core.RequireSession, core.RequireRole(core.RoleCreator))
}

func returnTarget(r *http.Request) string {
	return core.SafeRedirectTarget(r.URL.Query().Get("return"), "/admin/backup")
}

// partName is the uploaded name with its folder (webkitdirectory sends
// "folder/page.png"; multipart.Part.FileName keeps only the base name).
func partName(h string) string {
	_, params, err := mime.ParseMediaType(h)
	if err != nil {
		return ""
	}
	n := strings.ReplaceAll(params["filename"], "\\", "/")
	return strings.TrimPrefix(path.Clean("/"+n), "/")
}

type limitedReader struct {
	r   io.Reader
	n   int64
	max int64
}

var errSourceTooLarge = errors.New("importer: file too large")

func (l *limitedReader) Read(p []byte) (int, error) {
	n, err := l.r.Read(p)
	l.n += int64(n)
	if l.n > l.max {
		return n, errSourceTooLarge
	}
	return n, err
}

// classify sniffs an upload: an archive, a PDF, an image or something else.
func classify(head []byte, name string) string {
	ct := storage.DetectContentType(head)
	switch {
	case ct == "application/zip":
		return KindCBZ
	case ct == "application/vnd.rar":
		return KindCBR
	case ct == "application/pdf" || strings.HasPrefix(string(head), "%PDF-"):
		return KindPDF
	case storage.CheckType(ct, storage.ImageTypes...) == nil:
		return KindImages
	}
	return ""
}

// upload stores each archive or PDF as its own import and gathers loose
// images (an image folder) into one import.
func (m *Module) upload(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	u := core.UserFrom(ctx)
	ret := returnTarget(r)
	lim := m.svc.limits
	r.Body = http.MaxBytesReader(w, r.Body, 2*lim.MaxSourceBytes+(1<<20))
	mr, err := r.MultipartReader()
	if err != nil {
		web.Flash(w, r, "error", i18n.T(ctx, "importer.flash.none"), "")
		core.Redirect(w, r, ret)
		return nil
	}
	var started, skipped int
	var refused []string
	var batch *imageBatch
	defer func() {
		if batch != nil {
			batch.discard()
		}
	}()
	pdf := m.svc.PDFAvailable()
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			refused = append(refused, i18n.T(ctx, "importer.error.too_large"))
			break
		}
		if part.FormName() != "files" {
			_ = part.Close()
			continue
		}
		name := partName(part.Header.Get("Content-Disposition"))
		if name == "" {
			_ = part.Close()
			continue
		}
		br := bufio.NewReaderSize(part, 4096)
		head, _ := br.Peek(512)
		kind := classify(head, name)
		switch {
		case kind == "":
			skipped++
			_ = part.Close()
			continue
		case kind == KindPDF && !pdf:
			refused = append(refused, i18n.T(ctx, "importer.error.pdf_unavailable"))
			_ = part.Close()
			continue
		case kind == KindImages:
			if batch == nil {
				if batch, err = newImageBatch(); err != nil {
					_ = part.Close()
					return err
				}
			}
			if err := batch.add(name, br, lim); err != nil {
				_ = part.Close()
				refused = append(refused, path.Base(name)+": "+i18n.T(ctx, "importer.error."+codeOf(err)))
				continue
			}
			_ = part.Close()
			continue
		}
		id := db.NewID()
		key := "imports/" + u.ID + "/" + id + "/source." + kind
		lr := &limitedReader{r: br, max: lim.MaxSourceBytes}
		perr := m.d.Store.Put(ctx, key, lr, -1, "application/octet-stream")
		_ = part.Close()
		if perr != nil {
			_ = m.d.Store.Delete(context.WithoutCancel(ctx), key)
			if errors.Is(perr, errSourceTooLarge) {
				refused = append(refused, path.Base(name)+": "+i18n.T(ctx, "importer.error.too_large"))
				continue
			}
			return perr
		}
		if _, err := m.svc.Start(ctx, u.ID, kind, path.Base(name), key, 0); err != nil {
			_ = m.d.Store.Delete(context.WithoutCancel(ctx), key)
			return err
		}
		started++
	}
	if batch != nil && batch.n > 0 {
		key := "imports/" + u.ID + "/" + db.NewID() + "/source.zip"
		if err := batch.store(ctx, m.d.Store, key); err != nil {
			return err
		}
		if _, err := m.svc.Start(ctx, u.ID, KindImages, batch.title(), key, batch.n); err != nil {
			_ = m.d.Store.Delete(context.WithoutCancel(ctx), key)
			return err
		}
		started++
	}
	switch {
	case started > 0:
		// Files left out (not comics or images, or over a limit) are noted
		// under the confirmation; the rest was imported.
		var notes []string
		if skipped > 0 {
			notes = append(notes, i18n.T(ctx, "importer.flash.skipped", "count", skipped))
		}
		notes = append(notes, refused...)
		web.Flash(w, r, "success", i18n.T(ctx, "importer.flash.started", "count", started), strings.Join(notes, " · "))
	case len(refused) > 0:
		web.Flash(w, r, "error", i18n.T(ctx, "importer.flash.refused"), strings.Join(refused, " · "))
	default:
		web.Flash(w, r, "error", i18n.T(ctx, "importer.flash.none"), "")
	}
	core.Redirect(w, r, ret)
	return nil
}

func codeOf(err error) string {
	var ref *refusal
	if errors.As(err, &ref) {
		return ref.Code
	}
	return CodeInternal
}

// imageBatch collects loose images into a zip (stored as the import source).
// Each image is staged first, so one that breaks a limit is left out (and
// reported) without a partial entry in the zip.
type imageBatch struct {
	f     *os.File
	stage *os.File
	zw    *zip.Writer
	n     int
	total int64
	names []string
}

func newImageBatch() (*imageBatch, error) {
	f, err := os.CreateTemp("", uploadTempPattern+".zip")
	if err != nil {
		return nil, err
	}
	stage, err := os.CreateTemp("", uploadTempPattern+".part")
	if err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return nil, err
	}
	return &imageBatch{f: f, stage: stage, zw: zip.NewWriter(f)}, nil
}

func (b *imageBatch) add(name string, r io.Reader, lim Limits) error {
	if b.n >= lim.MaxPages {
		return refuse(CodeTooManyPages, "more than %d images", lim.MaxPages)
	}
	clean, err := safeName(name)
	if err != nil {
		return err
	}
	if err := b.stage.Truncate(0); err != nil {
		return err
	}
	if _, err := b.stage.Seek(0, io.SeekStart); err != nil {
		return err
	}
	n, err := io.Copy(b.stage, io.LimitReader(r, lim.MaxEntryBytes+1))
	if err != nil {
		return err
	}
	if n > lim.MaxEntryBytes {
		return refuse(CodeEntryTooLarge, "%q is larger than %d MB", name, lim.MaxEntryBytes>>20)
	}
	if b.total+n > lim.MaxTotalBytes {
		return refuse(CodeTooLarge, "more than %d MB", lim.MaxTotalBytes>>20)
	}
	if _, err := b.stage.Seek(0, io.SeekStart); err != nil {
		return err
	}
	w, err := b.zw.CreateHeader(&zip.FileHeader{Name: clean, Method: zip.Store})
	if err != nil {
		return err
	}
	if _, err := io.Copy(w, io.LimitReader(b.stage, n)); err != nil {
		return err
	}
	b.total += n
	b.n++
	b.names = append(b.names, clean)
	return nil
}

// title is the folder the images came from ("old-sketches/") or the first name.
func (b *imageBatch) title() string {
	if len(b.names) == 0 {
		return ""
	}
	dir := path.Dir(b.names[0])
	if dir != "." && dir != "/" {
		return path.Base(dir) + "/"
	}
	return b.names[0]
}

func (b *imageBatch) store(ctx context.Context, st storage.Store, key string) error {
	if err := b.zw.Close(); err != nil {
		return err
	}
	if _, err := b.f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	fi, err := b.f.Stat()
	if err != nil {
		return err
	}
	return st.Put(ctx, key, b.f, fi.Size(), "application/zip")
}

func (b *imageBatch) discard() {
	for _, f := range []*os.File{b.f, b.stage} {
		_ = f.Close()
		_ = os.Remove(f.Name())
	}
}

func (m *Module) queue(w http.ResponseWriter, r *http.Request) error {
	v, err := m.card(r.Context(), core.UserFrom(r.Context()).ID, returnTarget(r))
	if err != nil {
		return err
	}
	return web.Fragment(w, r, http.StatusOK, queueList(v))
}

func (m *Module) retry(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	if _, err := m.svc.Retry(ctx, core.UserFrom(ctx).ID, r.PathValue("id")); err != nil {
		return core.ErrNotFound()
	}
	web.Flash(w, r, "success", i18n.T(ctx, "importer.flash.retry"), "")
	core.Redirect(w, r, returnTarget(r))
	return nil
}

// ImportCard implements Cards.
func (m *Module) ImportCard(ctx context.Context, userID, returnTo string) (templ.Component, error) {
	v, err := m.card(ctx, userID, returnTo)
	if err != nil {
		return nil, err
	}
	return Card(v), nil
}

type rowView struct {
	Name, Status, OpenURL, RetryURL string
}

type cardView struct {
	ReturnTo string
	PDF      bool
	Active   bool
	Rows     []rowView
	Action   string
	QueueURL string
}

func (m *Module) card(ctx context.Context, userID, returnTo string) (cardView, error) {
	v := cardView{ReturnTo: returnTo, PDF: m.svc.PDFAvailable(), Action: "/imports?return=" + returnTo,
		QueueURL: "/imports/queue?return=" + returnTo}
	list, err := m.svc.Recent(ctx, userID, 6)
	if err != nil {
		return v, err
	}
	for _, imp := range list {
		row := rowView{Name: imp.Filename, Status: statusText(ctx, imp)}
		switch imp.Status {
		case StatusQueued, StatusRunning:
			v.Active = true
		case StatusDone:
			if imp.ProjectID != "" && imp.Result.ChapterID != "" {
				row.OpenURL = "/studio/p/" + imp.ProjectID + "/chapters/" + imp.Result.ChapterID + "/edit"
			}
		case StatusFailed, StatusCanceled:
			row.RetryURL = "/imports/" + imp.ID + "/retry?return=" + returnTo
		}
		v.Rows = append(v.Rows, row)
	}
	return v, nil
}

func statusText(ctx context.Context, imp Import) string {
	pct := fmt.Sprintf("%d%%", int(imp.Progress*100+0.5))
	switch imp.Status {
	case StatusQueued:
		if imp.Result.Images > 0 {
			return i18n.T(ctx, "importer.status.queued_images", "count", imp.Result.Images)
		}
		return i18n.T(ctx, "importer.status.queued")
	case StatusRunning:
		switch imp.Result.Phase {
		case PhaseDetecting:
			return i18n.T(ctx, "importer.status.detecting", "pct", pct)
		case PhaseCreating:
			return i18n.T(ctx, "importer.status.creating", "pct", pct)
		}
		return i18n.T(ctx, "importer.status.reading", "pct", pct)
	case StatusDone:
		return i18n.T(ctx, "importer.status.done", "count", imp.Pages)
	case StatusCanceled:
		return i18n.T(ctx, "importer.status.canceled")
	}
	code := imp.Result.Code
	if code == "" {
		code = CodeInternal
	}
	return i18n.T(ctx, "importer.status.failed", "reason", i18n.T(ctx, "importer.error."+code))
}
