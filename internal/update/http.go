package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/a-h/templ"

	"github.com/MaramHarsha/MangaVeyra/internal/backup"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/web"
	"github.com/MaramHarsha/MangaVeyra/internal/web/layout"
	"github.com/MaramHarsha/MangaVeyra/internal/web/ui"
)

// laterCookie snoozes the admin modal for one login ("Remind me next login").
const laterCookie = "mv_update_later"

// Routes implements core.Module. Every route is for signed-in admins in a
// browser session; anyone else gets 404 (ADR 0013 §1).
func (m *Module) Routes(r *core.Router) {
	r.Page("GET "+Page, m.page, core.RequireUser, core.RequireSession, adminOnly)
	g := r.Group(Page, core.RequireUser, core.RequireSession, adminOnly)
	g.Page("GET /settings", m.settingsPage)
	g.Page("POST /settings", m.saveSettings)
	g.Page("POST /check", m.checkNow)
	g.Page("POST /start", m.start)
	g.Page("POST /later", m.later)
	g.Page("POST /ack", m.ack)
	g.Page("GET /progress", m.progress)
	g.HandleFunc("GET /log", m.log)
}

func adminOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !core.UserFrom(r.Context()).IsAdmin() {
			core.WriteError(w, r, core.ErrNotFound())
			return
		}
		next.ServeHTTP(w, r)
	})
}

// G14 states.
const (
	stateSettings  = "settings"
	stateAvailable = "available"
	stateRunning   = "running"
	stateDone      = "done"
	stateFailed    = "failed"
	stateContainer = "container"
)

// view is everything the G14 templates need.
type view struct {
	State     string
	Host      string
	Running   string
	Config    Config
	Check     CheckState
	KeyOK     bool
	Container bool
	BackupsOK bool
	// LastBackup is the size of the newest finished backup (0 when unknown).
	LastBackup int64

	Offer   *Record
	O       Offer
	Attempt *Record
	Run     Run

	// Overlay renders the window over another page (admin login modal).
	Overlay bool
	// Next is where the overlay's buttons return.
	Next string
	// Back is where the page's close button leads.
	Back string
	// Problem is a refusal shown above the actions (422/409).
	Problem string
	// Values typed into the settings form when it was refused.
	Form Config
	Loc  *time.Location
}

func (m *Module) baseView(ctx context.Context, r *http.Request) view {
	inst := m.d.Settings.MustInstance(ctx)
	_, backupsOK := m.backups()
	v := view{Host: web.ServerHost(inst.PublicURL, m.d.Cfg.PublicURL, r.Host), Running: m.running(), Config: m.config(ctx),
		Check: m.checkState(ctx), KeyOK: len(m.keys()) > 0, Container: m.ContainerMode(), BackupsOK: backupsOK,
		Back: firstReady(ctx, "/admin/server", "/admin", "/studio"), Loc: userLocation(ctx)}
	v.Form = v.Config
	if svc, ok := m.backups(); ok {
		if list, err := svc.List(ctx, "", 20); err == nil {
			for _, b := range list {
				if b.Status == backup.StatusDone && b.Kind != backup.KindUserExport {
					v.LastBackup = b.Bytes
					break
				}
			}
		}
	}
	return v
}

func firstReady(ctx context.Context, paths ...string) string {
	for _, p := range paths {
		if ui.Ready(ctx, p) {
			return p
		}
	}
	return paths[len(paths)-1]
}

func userLocation(ctx context.Context) *time.Location {
	if u := core.UserFrom(ctx); u != nil && u.Timezone != "" {
		if loc, err := time.LoadLocation(u.Timezone); err == nil {
			return loc
		}
	}
	return time.UTC
}

// resolve picks the G14 state: a running install, an unacknowledged result,
// the container commands, the offer, or the settings.
func (m *Module) resolve(ctx context.Context, v *view) error {
	attempt, err := m.latestAttempt(ctx)
	if err != nil {
		return err
	}
	offer, err := m.offer(ctx)
	if err != nil {
		return err
	}
	v.Offer = offer
	if offer != nil {
		v.O = offer.Offer()
	}
	if attempt != nil {
		v.Attempt, v.Run = attempt, attempt.Run()
		switch {
		case isActive(attempt.Status):
			v.State = stateRunning
			return nil
		case attempt.Finished() && !v.Run.Ack:
			v.State = stateFailed
			if attempt.Status == StatusDone {
				v.State = stateDone
			}
			return nil
		}
	}
	if offer != nil {
		if run := offer.Run(); run.Mode == ModeContainer && offer.BackupID != nil {
			v.Attempt, v.Run, v.State = offer, run, stateContainer
			return nil
		}
		v.State = stateAvailable
		return nil
	}
	v.State = stateSettings
	return nil
}

func (m *Module) render(w http.ResponseWriter, r *http.Request, status int, v view) error {
	title := i18n.T(r.Context(), "update.page.title")
	if v.State == stateRunning {
		w.Header().Set("Cache-Control", "no-store")
	}
	return web.From(m.d).Page(w, r, status, web.PageOpts{Title: title, Nav: layout.NavAdmin}, PageView(v))
}

func (m *Module) page(w http.ResponseWriter, r *http.Request) error {
	v := m.baseView(r.Context(), r)
	if err := m.resolve(r.Context(), &v); err != nil {
		return err
	}
	return m.render(w, r, http.StatusOK, v)
}

func (m *Module) settingsPage(w http.ResponseWriter, r *http.Request) error {
	v := m.baseView(r.Context(), r)
	if err := m.resolve(r.Context(), &v); err != nil {
		return err
	}
	v.State = stateSettings
	return m.render(w, r, http.StatusOK, v)
}

// progress is the HTMX poll of the Updating window. A finished attempt (or
// the restart) reloads the whole page.
func (m *Module) progress(w http.ResponseWriter, r *http.Request) error {
	v := m.baseView(r.Context(), r)
	if err := m.resolve(r.Context(), &v); err != nil {
		return err
	}
	if !core.IsHTMX(r) {
		core.Redirect(w, r, Page)
		return nil
	}
	if v.State != stateRunning || v.Run.Phase == PhaseRestarting {
		w.Header().Set("HX-Refresh", "true")
		w.WriteHeader(http.StatusNoContent)
		return nil
	}
	return web.Fragment(w, r, http.StatusOK, RunningWindow(v))
}

func formConfig(r *http.Request, cur Config) Config {
	c := cur
	_, settingsForm := r.PostForm["release_url"]
	_, toggles := r.PostForm["toggles"]
	if settingsForm {
		c.Checks = r.PostForm.Get("checks") != ""
		c.ReleaseURL = r.PostForm.Get("release_url")
		c.Channel = r.PostForm.Get("channel")
	}
	if settingsForm || toggles {
		c.KeepBackup = r.PostForm.Get("keep") != ""
		c.AutoPatch = r.PostForm.Get("auto_patch") != ""
	}
	c.Normalize()
	return c
}

func (m *Module) saveSettings(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	if err := r.ParseForm(); err != nil {
		return core.ErrBadRequest("bad form")
	}
	c := formConfig(r, m.config(ctx))
	if err := m.saveConfig(ctx, c, core.UserFrom(ctx).ID); err != nil {
		v := m.baseView(ctx, r)
		_ = m.resolve(ctx, &v)
		v.State, v.Form, v.Problem = stateSettings, c, settingsProblem(ctx, c)
		return m.render(w, r, http.StatusUnprocessableEntity, v)
	}
	m.d.Auth.Audit(ctx, authEntry("update.settings", "", map[string]any{"checks": c.Checks, "release_url": c.ReleaseURL,
		"channel": c.Channel, "auto_patch": c.AutoPatch, "keep_backup": c.KeepBackup}))
	web.Flash(w, r, "success", i18n.T(ctx, "update.settings.saved"), "")
	core.Redirect(w, r, SettingsPage)
	return nil
}

func settingsProblem(ctx context.Context, c Config) string {
	if c.Checks && c.ReleaseURL == "" {
		return i18n.T(ctx, "update.settings.need_url")
	}
	return i18n.T(ctx, "update.settings.bad_url")
}

func (m *Module) checkNow(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	if err := r.ParseForm(); err != nil {
		return core.ErrBadRequest("bad form")
	}
	if _, ok := r.PostForm["release_url"]; ok {
		c := formConfig(r, m.config(ctx))
		if err := m.saveConfig(ctx, c, core.UserFrom(ctx).ID); err != nil {
			v := m.baseView(ctx, r)
			_ = m.resolve(ctx, &v)
			v.State, v.Form, v.Problem = stateSettings, c, settingsProblem(ctx, c)
			return m.render(w, r, http.StatusUnprocessableEntity, v)
		}
	}
	rec, err := m.Check(ctx)
	switch {
	case errors.Is(err, ErrChecksOff):
		web.Flash(w, r, "error", i18n.T(ctx, "update.check.off"), "")
		core.Redirect(w, r, SettingsPage)
	case errors.Is(err, ErrBusy):
		core.Redirect(w, r, Page)
	case err != nil:
		web.Flash(w, r, "error", i18n.T(ctx, "update.check.failed"), checkMessage(ctx, checkCode(err)))
		core.Redirect(w, r, SettingsPage)
	case rec == nil:
		web.Flash(w, r, "success", i18n.T(ctx, "update.check.current", "version", m.running()), "")
		core.Redirect(w, r, SettingsPage)
	default:
		core.Redirect(w, r, Page)
	}
	return nil
}

func (m *Module) start(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	if err := r.ParseForm(); err != nil {
		return core.ErrBadRequest("bad form")
	}
	u := core.UserFrom(ctx)
	c := formConfig(r, m.config(ctx))
	if err := m.saveConfig(ctx, c, u.ID); err != nil {
		m.d.Log.Warn("update: save toggles", "err", err)
	}
	id := r.PostForm.Get("id")
	_, err := m.Install(ctx, id, u.ID, InstallOptions{Keep: c.KeepBackup})
	if err != nil {
		v := m.baseView(ctx, r)
		if rerr := m.resolve(ctx, &v); rerr != nil {
			return rerr
		}
		status := http.StatusUnprocessableEntity
		if errors.Is(err, ErrBusy) || errors.Is(err, backup.ErrBusy) {
			status = http.StatusConflict
		}
		if v.State == stateFailed || v.State == stateDone {
			v.State = stateAvailable
		}
		v.Problem = installProblem(ctx, err)
		m.d.Log.Warn("update: refused to start", "err", err)
		return m.render(w, r, status, v)
	}
	core.Redirect(w, r, Page)
	return nil
}

func installProblem(ctx context.Context, err error) string {
	switch {
	case errors.Is(err, ErrBusy):
		return i18n.T(ctx, "update.problem.busy")
	case errors.Is(err, ErrNoBackups):
		return i18n.T(ctx, "update.problem.no_backups")
	case errors.Is(err, ErrNoDownload):
		return i18n.T(ctx, "update.problem.no_download")
	case errors.Is(err, ErrNotOffered), errors.Is(err, ErrStale):
		return i18n.T(ctx, "update.problem.stale")
	case errors.Is(err, ErrNoKey):
		return i18n.T(ctx, "update.problem.no_key")
	case errors.Is(err, ErrChecksOff):
		return i18n.T(ctx, "update.problem.checks_off")
	case errors.Is(err, ErrBadSignature), errors.Is(err, ErrBadManifest), errors.Is(err, ErrChannel):
		return i18n.T(ctx, "update.problem.signature")
	}
	var fe fetchError
	if errors.As(err, &fe) {
		return i18n.T(ctx, "update.problem.fetch")
	}
	return i18n.T(ctx, "update.problem.replace", "error", truncate(err.Error(), 200))
}

// snoozeValue binds "Remind me next login" to this session and release: a
// new login (a new session) shows the modal again.
func snoozeValue(sessionID, version string) string {
	h := sha256.Sum256([]byte("update-later\x00" + sessionID + "\x00" + version))
	return hex.EncodeToString(h[:16])
}

func (m *Module) later(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	if err := r.ParseForm(); err != nil {
		return core.ErrBadRequest("bad form")
	}
	if _, ok := r.PostForm["toggles"]; ok {
		if err := m.saveConfig(ctx, formConfig(r, m.config(ctx)), core.UserFrom(ctx).ID); err != nil {
			m.d.Log.Warn("update: save toggles", "err", err)
		}
	}
	if offer, err := m.offer(ctx); err == nil && offer != nil {
		http.SetCookie(w, &http.Cookie{Name: laterCookie, Value: snoozeValue(core.IdentityFrom(ctx).SessionID, offer.ToVersion),
			Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: m.d.Cfg.SecureCookies()})
	}
	core.Redirect(w, r, core.SafeRedirectTarget(r.PostForm.Get("next"), "/studio"))
	return nil
}

// ack hides a finished result (Continue, or closing it).
func (m *Module) ack(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	if err := r.ParseForm(); err != nil {
		return core.ErrBadRequest("bad form")
	}
	id := r.PostForm.Get("attempt")
	if id == "" {
		id = r.PostForm.Get("id")
	}
	if rec, err := m.record(ctx, id); err == nil && rec.Finished() {
		run := rec.Run()
		run.Ack = true
		if err := m.saveRun(ctx, rec, run); err != nil {
			return err
		}
	}
	core.Redirect(w, r, core.SafeRedirectTarget(r.PostForm.Get("next"), "/studio"))
	return nil
}

// log serves the update log as text ("Show log", "Copy log").
func (m *Module) log(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rec, err := m.latestAttempt(ctx)
	if id := r.URL.Query().Get("id"); id != "" {
		rec, err = m.record(ctx, id)
	}
	if err != nil || rec == nil {
		if offer, oerr := m.offer(ctx); oerr == nil && offer != nil && offer.StartedAt != nil {
			rec, err = offer, nil
		}
	}
	if err != nil || rec == nil {
		core.WriteError(w, r, core.ErrNotFound())
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = fmt.Fprint(w, LogText(rec))
}

// LogText renders an attempt's log as plain text.
func LogText(rec *Record) string {
	run := rec.Run()
	var b strings.Builder
	fmt.Fprintf(&b, "MangaVeyra update %s → %s (%s)\nstatus: %s\n", rec.FromVersion, rec.ToVersion, rec.ID, rec.Status)
	if run.Code != "" {
		fmt.Fprintf(&b, "error code: %s\n", run.Code)
	}
	if rec.Error != "" {
		fmt.Fprintf(&b, "error: %s\n", rec.Error)
	}
	b.WriteString("\n")
	for _, l := range run.Log {
		fmt.Fprintf(&b, "%s  %s\n", l.At.UTC().Format(time.RFC3339), l.Text)
	}
	return b.String()
}

// Shell returns the extras G14 adds to an app-shell page for this request:
// the members' banner, or the admin's modal (an offer this login has not
// snoozed, or a finished result not yet acknowledged). Both are nil when
// nothing applies. The web layer calls it for full pages (see the root hook).
func (m *Module) Shell(r *http.Request) (banner, overlay templ.Component) {
	ctx := r.Context()
	u := core.UserFrom(ctx)
	if u == nil || core.IsHTMX(r) || strings.HasPrefix(r.URL.Path, Page) {
		return nil, nil
	}
	attempt, err := m.latestAttempt(ctx)
	if err != nil {
		return nil, nil
	}
	if attempt != nil && isActive(attempt.Status) {
		return nil, nil
	}
	offer, err := m.offer(ctx)
	if err != nil {
		return nil, nil
	}
	if !u.IsAdmin() {
		if offer == nil {
			return nil, nil
		}
		inst := m.d.Settings.MustInstance(ctx)
		return MemberBanner(offer.ToVersion, web.ServerHost(inst.PublicURL, m.d.Cfg.PublicURL, r.Host)), nil
	}
	if core.IdentityFrom(ctx).SessionID == "" {
		return nil, nil
	}
	showResult := attempt != nil && attempt.Finished() && !attempt.Run().Ack
	snoozed := false
	if offer != nil {
		if c, err := r.Cookie(laterCookie); err == nil && c.Value == snoozeValue(core.IdentityFrom(ctx).SessionID, offer.ToVersion) {
			snoozed = true
		}
	}
	if !showResult && (offer == nil || snoozed) {
		return nil, nil
	}
	v := m.baseView(ctx, r)
	if err := m.resolve(ctx, &v); err != nil {
		return nil, nil
	}
	if v.State == stateSettings || v.State == stateContainer || (v.State == stateAvailable && snoozed) {
		return nil, nil
	}
	v.Overlay = true
	v.Next = r.URL.RequestURI()
	return nil, Overlay(v)
}
