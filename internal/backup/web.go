package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/a-h/templ"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/importer"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/web"
	"github.com/MaramHarsha/MangaVeyra/internal/web/layout"
)

// confirmWord is typed to confirm a restore.
const confirmWord = "restore"

// maxUploadBytes bounds an uploaded archive (64 GiB).
const maxUploadBytes = 64 << 30

// adminOnly answers 404 to everyone but site admins (ADR 0013 §1).
func adminOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !core.UserFrom(r.Context()).IsAdmin() {
			core.WriteError(w, r, core.ErrNotFound())
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Routes implements core.Module.
func (m *Module) Routes(r *core.Router) {
	admin := []core.Middleware{core.RequireSession, adminOnly}
	r.Page("GET /admin/backup", m.page, admin...)
	r.Page("GET /admin/backup/status", m.status, admin...)
	r.Page("POST /admin/backup/now", m.backupNow, admin...)
	r.Page("POST /admin/backup/export", m.exportAll, admin...)
	r.Page("POST /admin/backup/schedule", m.saveSchedule, admin...)
	r.Page("POST /admin/backup/upload", m.upload, admin...)
	r.Page("POST /admin/backup/restore", m.startRestore, admin...)
	r.Page("GET /admin/backup/archives/{id}/download", m.download, admin...)
	r.Page("POST /admin/backup/archives/{id}/delete", m.deleteArchive, admin...)
	// Settings → Data: a member's own export.
	r.Page("POST /api/v1/export/me", m.exportMe, core.RequireSession)
	r.Page("GET /settings/data/exports/{id}", m.downloadMine, core.RequireSession)
}

type archiveView struct {
	ID, Kind, When, Size, Schema, DownloadURL, DeleteURL string
	Uploaded                                             bool
}

type liveView struct {
	// Label is "Backing up", "Exporting everything" or "Restoring".
	Label    string
	Progress float64
	Active   bool
}

type noticeView struct {
	Tone  string // success | warning | error
	Icon  string
	Title string
	Body  string
	Extra string
}

type pageView struct {
	LastBackup, ScheduleText, Destination, DefaultDir string
	Schedule                                          Schedule
	Archives                                          []archiveView
	Restorable                                        []archiveView
	Live                                              *liveView
	Notices                                           []noticeView
	Export                                            *archiveView
	ExportRunning                                     bool
	ImportCard                                        templ.Component
	// RestoreOpen renders the restore panel open (after an upload or an error).
	RestoreOpen   bool
	RestoreTarget string
	ConfirmError  string
	UploadError   string
	ScheduleOpen  bool
	Errors        web.FormErrors
	Busy          bool
}

func (m *Module) page(w http.ResponseWriter, r *http.Request) error {
	v, err := m.load(r)
	if err != nil {
		return err
	}
	if id := r.URL.Query().Get("restore"); db.ValidID(id) {
		v.RestoreOpen, v.RestoreTarget = true, id
	}
	return m.render(w, r, http.StatusOK, v)
}

func (m *Module) render(w http.ResponseWriter, r *http.Request, status int, v pageView) error {
	return m.ui.Page(w, r, status, web.PageOpts{Title: i18n.T(r.Context(), "backup.title"), Section: layout.SectionStudio,
		Nav: layout.NavSettings, Tab: layout.TabMe}, pageBody(v))
}

// status is the polled progress fragment; when nothing runs any more the page reloads.
func (m *Module) status(w http.ResponseWriter, r *http.Request) error {
	v, err := m.load(r)
	if err != nil {
		return err
	}
	if v.Live == nil || !v.Live.Active {
		w.Header().Set("HX-Refresh", "true")
	}
	return web.Fragment(w, r, http.StatusOK, liveBlock(v))
}

func (m *Module) load(r *http.Request) (pageView, error) {
	ctx := r.Context()
	s := m.svc
	sc, err := s.Schedule(ctx)
	if err != nil {
		return pageView{}, err
	}
	now := time.Now()
	loc := viewerLocation(ctx, sc.TZ)
	v := pageView{Schedule: sc, ScheduleText: scheduleText(ctx, sc), Destination: sc.Destination, DefaultDir: m.d.Cfg.BackupDir(),
		Errors: web.FormErrors{}, LastBackup: i18n.T(ctx, "backup.never")}
	if v.Destination == "" {
		if abs, err := filepath.Abs(v.DefaultDir); err == nil {
			v.Destination = abs
		} else {
			v.Destination = v.DefaultDir
		}
	}
	rows, err := s.listRows(ctx, "", 200)
	if err != nil {
		return v, err
	}
	var lastFailed *Backup
	lastDone := time.Time{}
	for _, row := range rows {
		b := row.backup()
		if b.Kind == KindUserExport {
			continue
		}
		var inc Includes
		_ = json.Unmarshal([]byte(row.Includes), &inc)
		switch b.Status {
		case StatusDone, StatusRestored:
			av := archiveView{ID: b.ID, Kind: kindLabel(ctx, b.Kind), When: formatWhen(ctx, b.CreatedAt, now, loc), Size: formatSize(b.Bytes),
				Schema: b.Schema, DownloadURL: "/admin/backup/archives/" + b.ID + "/download", DeleteURL: "/admin/backup/archives/" + b.ID + "/delete",
				Uploaded: inc.Uploaded}
			if lastDone.IsZero() && !inc.Uploaded {
				lastDone = b.CreatedAt
				v.LastBackup = av.When + " · " + av.Size
			}
			if b.Kind == KindExport && v.Export == nil && !inc.Uploaded {
				e := av
				v.Export = &e
			}
			if len(v.Archives) < 10 {
				v.Archives = append(v.Archives, av)
			}
			v.Restorable = append(v.Restorable, av)
		case StatusFailed:
			if lastFailed == nil {
				lastFailed = &b
			}
		}
	}
	if lastFailed != nil && lastFailed.CreatedAt.After(lastDone) && now.Sub(lastFailed.CreatedAt) < 24*time.Hour {
		v.Notices = append(v.Notices, noticeView{Tone: "error", Icon: "alert-triangle", Title: i18n.T(ctx, "backup.notice.failed"),
			Body: i18n.T(ctx, "backup.notice.failed_body")})
	}
	// Running work: a restore in this process, else a queued or running backup job.
	st := s.State()
	if st.Running {
		v.Live = &liveView{Label: i18n.T(ctx, "backup.live.restoring"), Progress: st.Progress, Active: true}
		v.Busy = true
	} else if l := s.hold.Load(); l != nil && l.info.What == HoldUpdate {
		// An update holds the server (G14 shows its progress): nothing can start here.
		v.Busy = true
	} else {
		active, err := m.d.Jobs.List(ctx, jobs.Filter{Types: []string{JobCreate}, States: []string{jobs.StateQueued, jobs.StateRunning}, Limit: 1})
		if err != nil {
			return v, err
		}
		if len(active) > 0 {
			label := i18n.T(ctx, "backup.live.backing_up")
			var in createInput
			_ = active[0].DecodeInput(&in)
			if in.Kind == KindExport {
				label = i18n.T(ctx, "backup.live.exporting")
				v.ExportRunning = true
			}
			v.Live = &liveView{Label: label, Progress: active[0].Progress, Active: true}
			v.Busy = true
		} else if s.busy.Load() {
			v.Live = &liveView{Label: i18n.T(ctx, "backup.live.backing_up"), Active: true}
			v.Busy = true
		}
	}
	if !st.Running && st.Err != nil && st.BackupID != "" {
		msg := st.Err.Error()
		if errors.Is(st.Err, ErrIncompatible) {
			msg = strings.TrimPrefix(msg, ErrIncompatible.Error()+": ")
			v.Notices = append(v.Notices, noticeView{Tone: "error", Icon: "alert-triangle", Title: i18n.T(ctx, "backup.notice.restore_refused"),
				Body: i18n.T(ctx, "backup.notice.restore_refused_body"), Extra: msg})
		} else {
			v.Notices = append(v.Notices, noticeView{Tone: "error", Icon: "alert-triangle", Title: i18n.T(ctx, "backup.notice.restore_failed"),
				Body: i18n.T(ctx, "backup.notice.restore_failed_body")})
		}
	}
	if rep, err := s.LastRestore(ctx); err == nil && rep != nil && now.Sub(rep.At) < 24*time.Hour && !st.Running {
		n := noticeView{Tone: "success", Icon: "circle-check", Title: i18n.T(ctx, "backup.notice.restored", "when", formatWhen(ctx, rep.At, now, loc)),
			Body: i18n.T(ctx, "backup.notice.restored_body", "tables", rep.Tables, "rows", i18n.FormatInt(rep.Rows, i18n.LocaleFrom(ctx)), "media", rep.Media)}
		v.Notices = append(v.Notices, n)
		if rep.MasterKeyDiffers {
			v.Notices = append(v.Notices, noticeView{Tone: "warning", Icon: "key", Title: i18n.T(ctx, "backup.notice.master_key"),
				Body: i18n.T(ctx, "backup.notice.master_key_body")})
		}
	}
	if c, ok := core.Use[importer.Cards](m.d); ok {
		card, err := c.ImportCard(ctx, core.UserFrom(ctx).ID, "/admin/backup")
		if err != nil {
			return v, err
		}
		v.ImportCard = card
	}
	return v, nil
}

// LastRestore reads the newest restore record from the audit log.
func (s *service) LastRestore(ctx context.Context) (*RestoreReport, error) {
	var data string
	err := s.db.Get(ctx, &data, `SELECT data FROM audit_log WHERE action = 'backup.restore' ORDER BY created_at DESC, id DESC LIMIT 1`)
	if db.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var rep RestoreReport
	if err := json.Unmarshal([]byte(data), &rep); err != nil {
		return nil, nil
	}
	return &rep, nil
}

func (m *Module) busyFlash(w http.ResponseWriter, r *http.Request) error {
	web.Flash(w, r, "error", i18n.T(r.Context(), "backup.flash.busy"), "")
	core.Redirect(w, r, "/admin/backup")
	return nil
}

func (m *Module) enqueue(w http.ResponseWriter, r *http.Request, kind string) error {
	if m.svc.busy.Load() || m.svc.State().Running {
		return m.busyFlash(w, r)
	}
	u := core.UserFrom(r.Context())
	if _, err := m.d.Jobs.Enqueue(r.Context(), jobs.EnqueueParams{Type: JobCreate, Scope: jobs.Scope{Kind: jobs.ScopeNone}, UserID: u.ID,
		Input: createInput{Kind: kind}, MaxAttempts: 1, Priority: 5, DedupeKey: "server:" + kind}); err != nil {
		return err
	}
	core.Redirect(w, r, "/admin/backup")
	return nil
}

func (m *Module) backupNow(w http.ResponseWriter, r *http.Request) error {
	return m.enqueue(w, r, KindManual)
}
func (m *Module) exportAll(w http.ResponseWriter, r *http.Request) error {
	return m.enqueue(w, r, KindExport)
}

func (m *Module) saveSchedule(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		return core.ErrBadRequest("")
	}
	cur, err := m.svc.Schedule(ctx)
	if err != nil {
		return err
	}
	sc := cur
	fe := web.FormErrors{}
	sc.Frequency = r.PostForm.Get("frequency")
	atoi := func(field string, lo, hi int) int {
		n, err := strconv.Atoi(strings.TrimSpace(r.PostForm.Get(field)))
		if err != nil || n < lo || n > hi {
			fe.Add(field, i18n.T(ctx, "backup.schedule.error."+field))
		}
		return n
	}
	sc.Hour = atoi("hour", 0, 23)
	sc.Weekday = atoi("weekday", 0, 6)
	sc.Keep = atoi("keep", 1, 365)
	sc.Destination = strings.TrimSpace(r.PostForm.Get("destination"))
	if u := core.UserFrom(ctx); u != nil && u.Timezone != "" {
		sc.TZ = u.Timezone
	}
	if len(fe) == 0 {
		if _, err := m.svc.SaveSchedule(ctx, sc, core.UserFrom(ctx).ID); err != nil {
			switch {
			case errors.Is(err, ErrDestination):
				fe.Add("destination", i18n.T(ctx, "backup.schedule.error.destination"))
			case errors.Is(err, ErrInvalid):
				fe.Add("frequency", i18n.T(ctx, "backup.schedule.error.frequency"))
			default:
				return err
			}
		}
	}
	if len(fe) > 0 {
		v, err := m.load(r)
		if err != nil {
			return err
		}
		v.Schedule, v.Errors, v.ScheduleOpen = sc, fe, true
		return m.render(w, r, http.StatusUnprocessableEntity, v)
	}
	m.d.Auth.Audit(ctx, auth.AuditEntry{Action: "backup.schedule", TargetKind: "backup", Data: map[string]any{"frequency": sc.Frequency, "keep": sc.Keep}})
	web.Flash(w, r, "success", i18n.T(ctx, "backup.flash.schedule_saved"), "")
	core.Redirect(w, r, "/admin/backup")
	return nil
}

// upload streams an archive to the backup directory and records it, then
// reopens the restore panel with it selected.
func (m *Module) upload(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	fail := func(msg string) error {
		v, err := m.load(r)
		if err != nil {
			return err
		}
		v.RestoreOpen, v.UploadError = true, msg
		return m.render(w, r, http.StatusUnprocessableEntity, v)
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
	mr, err := r.MultipartReader()
	if err != nil {
		return fail(i18n.T(ctx, "backup.upload.error.missing"))
	}
	dir, err := m.svc.destination(ctx)
	if err != nil {
		return fail(i18n.T(ctx, "backup.schedule.error.destination"))
	}
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			return fail(i18n.T(ctx, "backup.upload.error.missing"))
		}
		if err != nil {
			return fail(i18n.T(ctx, "backup.upload.error.missing"))
		}
		if part.FormName() != "archive" || part.FileName() == "" {
			_ = part.Close()
			continue
		}
		tmp, err := os.CreateTemp(dir, ".upload-*"+Extension)
		if err != nil {
			_ = part.Close()
			return fail(i18n.T(ctx, "backup.schedule.error.destination"))
		}
		_ = tmp.Chmod(0o600)
		_, cerr := io.Copy(tmp, part)
		_ = part.Close()
		if err := tmp.Close(); err != nil && cerr == nil {
			cerr = err
		}
		if cerr != nil {
			_ = os.Remove(tmp.Name())
			return fail(i18n.T(ctx, "backup.upload.error.missing"))
		}
		b, err := m.svc.Register(ctx, tmp.Name(), core.UserFrom(ctx).ID, true)
		if err != nil {
			_ = os.Remove(tmp.Name())
			if errors.Is(err, ErrIncompatible) {
				return fail(i18n.T(ctx, "backup.upload.error.invalid", "reason", strings.TrimPrefix(err.Error(), ErrIncompatible.Error()+": ")))
			}
			return err
		}
		core.Redirect(w, r, "/admin/backup?restore="+b.ID)
		return nil
	}
}

func (m *Module) startRestore(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		return core.ErrBadRequest("")
	}
	id := r.PostForm.Get("archive")
	confirm := strings.TrimSpace(strings.ToLower(r.PostForm.Get("confirm")))
	b, err := m.svc.Get(ctx, id)
	if err != nil || b.Kind == KindUserExport || (b.Status != StatusDone && b.Status != StatusRestored) {
		v, lerr := m.load(r)
		if lerr != nil {
			return lerr
		}
		v.RestoreOpen, v.ConfirmError = true, i18n.T(ctx, "backup.restore.error.archive")
		return m.render(w, r, http.StatusUnprocessableEntity, v)
	}
	if confirm != confirmWord {
		v, err := m.load(r)
		if err != nil {
			return err
		}
		v.RestoreOpen, v.RestoreTarget, v.ConfirmError = true, id, i18n.T(ctx, "backup.restore.error.confirm", "word", confirmWord)
		return m.render(w, r, http.StatusUnprocessableEntity, v)
	}
	if m.svc.State().Running {
		return m.busyFlash(w, r)
	}
	actor := core.UserFrom(ctx).ID
	// The hold is taken here, in the request: of two submits only one gets it
	// (the other sees the busy notice), and it owns maintenance mode until the
	// restore ends, when the setting from before the restore is put back.
	bg := context.WithoutCancel(ctx)
	l, err := m.svc.Hold(bg, HoldRequest{What: HoldRestore, Message: i18n.T(ctx, "backup.restore.maintenance"), ActorID: actor})
	if errors.Is(err, ErrBusy) {
		return m.busyFlash(w, r)
	}
	if err != nil {
		return err
	}
	m.svc.setState(func(s *RestoreState) { *s = RestoreState{Running: true, BackupID: id} })
	go func() {
		pctx := WithProgress(l.Context(bg), func(p float64) { m.svc.setState(func(s *RestoreState) { s.Progress = p }) })
		rep, err := m.svc.restoreArchive(pctx, id, actor)
		before := l.Info().Before
		if merr := l.Release(bg, &before); merr != nil {
			m.svc.log.Error("backup: put maintenance mode back after the restore", "err", merr)
		}
		if err != nil {
			m.svc.log.Warn("backup: restore", "backup", id, "err", err)
		}
		m.svc.setState(func(s *RestoreState) { s.Running, s.Err, s.Report, s.Progress = false, err, rep, 1 })
	}()
	core.Redirect(w, r, "/admin/backup")
	return nil
}

func serveArchive(w http.ResponseWriter, r *http.Request, path string, b Backup) error {
	f, err := os.Open(path)
	if err != nil {
		return core.ErrNotFound()
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return core.ErrNotFound()
	}
	h := w.Header()
	h.Set("Content-Type", "application/zip")
	h.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filepath.Base(path)}))
	h.Set("Cache-Control", "no-store")
	http.ServeContent(w, r, "", fi.ModTime(), f)
	return nil
}

func (m *Module) download(w http.ResponseWriter, r *http.Request) error {
	path, b, err := m.svc.Path(r.Context(), r.PathValue("id"))
	if err != nil || b.Kind == KindUserExport {
		return core.ErrNotFound()
	}
	return serveArchive(w, r, path, b)
}

func (m *Module) deleteArchive(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	b, err := m.svc.Get(ctx, r.PathValue("id"))
	if err != nil || b.Kind == KindUserExport {
		return core.ErrNotFound()
	}
	if err := m.svc.Delete(ctx, b.ID); err != nil {
		if errors.Is(err, ErrBusy) {
			return m.busyFlash(w, r)
		}
		return err
	}
	m.d.Auth.Audit(ctx, auth.AuditEntry{Action: "backup.delete", TargetKind: "backup", TargetID: b.ID, Data: map[string]any{"kind": b.Kind}})
	web.Flash(w, r, "success", i18n.T(ctx, "backup.flash.deleted"), "")
	core.Redirect(w, r, "/admin/backup")
	return nil
}

// exportMe sends the member's own export as a download: their newest one
// when it is less than an hour old, otherwise a new one (MemberExport).
func (m *Module) exportMe(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	u := core.UserFrom(ctx)
	b, err := m.svc.MemberExport(ctx, u.ID)
	if err != nil {
		if errors.Is(err, ErrExportRunning) {
			web.Flash(w, r, "error", i18n.T(ctx, "backup.flash.export_running"), "")
			core.Redirect(w, r, "/settings/data")
			return nil
		}
		return fmt.Errorf("backup: member export: %w", err)
	}
	path, _, err := m.svc.Path(ctx, b.ID)
	if err != nil {
		return err
	}
	return serveArchive(w, r, path, b)
}

func (m *Module) downloadMine(w http.ResponseWriter, r *http.Request) error {
	path, b, err := m.svc.Path(r.Context(), r.PathValue("id"))
	if err != nil || b.Kind != KindUserExport || b.UserID != core.UserFrom(r.Context()).ID {
		return core.ErrNotFound()
	}
	return serveArchive(w, r, path, b)
}
