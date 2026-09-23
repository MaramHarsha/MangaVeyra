package setup

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
	"github.com/MaramHarsha/MangaVeyra/internal/web"
	"github.com/MaramHarsha/MangaVeyra/internal/web/layout"
)

// Step names in order.
const (
	StepAdmin    = "admin"
	StepInstance = "instance"
	StepModels   = "models"
	StepFinish   = "finish"
)

var steps = []struct{ Name, Icon string }{
	{StepAdmin, "user-shield"}, {StepInstance, "server"}, {StepModels, "cpu"}, {StepFinish, "circle-check"},
}

// Module is the first-run wizard.
type Module struct {
	d  *core.Deps
	ui *web.UI
	// adminMu serialises the admin step so two browsers cannot both create the first admin.
	adminMu sync.Mutex
}

// New creates the module.
func New(d *core.Deps) *Module { return &Module{d: d} }

// Name implements core.Module.
func (m *Module) Name() string { return "setup" }

// Init looks up the web layer.
func (m *Module) Init(ctx context.Context) error {
	m.ui = web.From(m.d)
	return nil
}

// modelStep returns the provided ModelStep or the built-in one.
func (m *Module) modelStep() ModelStep {
	if ms, ok := core.Use[ModelStep](m.d); ok {
		return ms
	}
	return defaultModelStep{}
}

// Routes implements core.Module.
func (m *Module) Routes(r *core.Router) {
	r.Page("GET /setup", m.index)
	r.Page("GET /setup/admin", m.adminPage)
	r.Page("POST /setup/admin", m.createAdmin)
	g := r.Group("/setup", core.RequireRole(auth.RoleAdmin), core.RequireSession)
	g.Page("GET /instance", m.instancePage)
	g.Page("POST /instance", m.saveInstance)
	g.Page("GET /models", m.modelsPage)
	g.Page("POST /models", m.saveModels)
	g.Page("GET /finish", m.finishPage)
	g.Page("POST /finish", m.finish)
}

// index sends the visitor to the right step.
func (m *Module) index(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	ok, err := m.d.Auth.HasAdmin(ctx)
	if err != nil {
		return err
	}
	u := core.UserFrom(ctx)
	switch {
	case !ok:
		core.Redirect(w, r, "/setup/admin")
	case u == nil:
		core.Redirect(w, r, "/login?next=/setup")
	case !u.IsAdmin():
		core.Redirect(w, r, "/studio")
	case m.d.Settings.MustInstance(ctx).SetupCompletedAt != nil:
		core.Redirect(w, r, "/studio")
	default:
		core.Redirect(w, r, "/setup/instance")
	}
	return nil
}

// render draws a wizard step.
func (m *Module) render(w http.ResponseWriter, r *http.Request, status int, current string, body templComponent) error {
	ctx := r.Context()
	inst := m.d.Settings.MustInstance(ctx)
	hasAdmin, _ := m.d.Auth.HasAdmin(ctx)
	idx := stepIndex(current)
	o := layout.SetupOpts{Host: web.ServerHost(inst.PublicURL, m.d.Cfg.PublicURL, r.Host)}
	for i, s := range steps {
		done := i < idx || (s.Name == StepAdmin && hasAdmin) || (s.Name == StepFinish && inst.SetupCompletedAt != nil)
		href := "/setup/" + s.Name
		if !hasAdmin && s.Name != StepAdmin {
			href = "/setup/admin"
		}
		o.Steps = append(o.Steps, layout.SetupStep{Label: i18n.T(ctx, "setup.step."+s.Name), Href: href, Icon: s.Icon, Done: done, Current: s.Name == current})
	}
	o.Facts = []string{
		i18n.T(ctx, "setup.fact.telemetry"),
		i18n.T(ctx, "setup.fact.mode", "mode", i18n.T(ctx, "instance.mode."+inst.Mode), "signup", strings.ToLower(i18n.T(ctx, "instance.signup."+inst.SignUp))),
	}
	c := m.ui.Chrome(w, r, web.PageOpts{Title: i18n.T(ctx, "setup.title"), Transition: "turn"})
	return web.Render(w, r, status, web.Wrap(layout.SetupShell(c, o), body), nil)
}

func stepIndex(name string) int {
	for i, s := range steps {
		if s.Name == name {
			return i
		}
	}
	return 0
}

// ---- step 1: admin account ----------------------------------------------------------

type adminForm struct {
	DisplayName string
	Username    string
	Email       string
	Errors      web.FormErrors
}

func (m *Module) adminPage(w http.ResponseWriter, r *http.Request) error {
	ok, err := m.d.Auth.HasAdmin(r.Context())
	if err != nil {
		return err
	}
	if ok {
		core.Redirect(w, r, "/setup")
		return nil
	}
	return m.render(w, r, http.StatusOK, StepAdmin, AdminStep(adminForm{Errors: web.FormErrors{}}))
}

func (m *Module) createAdmin(w http.ResponseWriter, r *http.Request) error {
	if err := web.ParseForm(w, r); err != nil {
		return core.ErrBadRequest("invalid form")
	}
	ctx := r.Context()
	f := adminForm{DisplayName: web.FormValue(r, "display_name"), Username: auth.NormalizeUsername(web.FormValue(r, "username")),
		Email: web.FormValue(r, "email"), Errors: web.FormErrors{}}
	password, confirm := r.PostFormValue("password"), r.PostFormValue("confirm")
	if f.Username == "" {
		f.Errors.Add("username", i18n.T(ctx, "setup.required"))
	}
	if password != confirm {
		f.Errors.Add("confirm", i18n.T(ctx, "setup.admin.mismatch"))
	}
	if f.Errors.Has() {
		return m.render(w, r, http.StatusUnprocessableEntity, StepAdmin, AdminStep(f))
	}

	m.adminMu.Lock()
	defer m.adminMu.Unlock()
	ok, err := m.d.Auth.HasAdmin(ctx)
	if err != nil {
		return err
	}
	if ok {
		return core.ErrForbidden(i18n.T(ctx, "setup.admin.exists"))
	}
	u, err := m.d.Auth.CreateUser(ctx, auth.NewUser{Username: f.Username, Email: f.Email, DisplayName: f.DisplayName,
		Password: password, Role: auth.RoleAdmin, Locale: i18n.LocaleFrom(ctx)})
	if err != nil {
		if f.Errors.AddError(ctx, err) {
			return m.render(w, r, http.StatusUnprocessableEntity, StepAdmin, AdminStep(f))
		}
		return err
	}
	token, _, err := m.d.Auth.CreateSession(ctx, u.ID, true, core.ClientIP(r), r.UserAgent())
	if err != nil {
		return err
	}
	m.d.Auth.SetSessionCookie(w, r, token, true)
	// The admin skips member onboarding.
	_, _ = m.d.DB.Exec(ctx, `UPDATE users SET onboarding = ? WHERE id = ?`, `{"skipped":true}`, u.ID)
	web.Flash(w, r, "success", i18n.T(ctx, "setup.admin.created", "name", u.Name()), "")
	core.Redirect(w, r, "/setup/instance")
	return nil
}

// ---- step 2: instance & sign-up -----------------------------------------------------

type instanceForm struct {
	Name      string
	PublicURL string
	Mode      string
	SignUp    string
	Errors    web.FormErrors
}

func (m *Module) instancePage(w http.ResponseWriter, r *http.Request) error {
	inst := m.d.Settings.MustInstance(r.Context())
	f := instanceForm{Name: inst.Name, PublicURL: inst.PublicURL, Mode: inst.Mode, SignUp: inst.SignUp, Errors: web.FormErrors{}}
	if f.PublicURL == "" {
		f.PublicURL = m.suggestURL(r)
	}
	return m.render(w, r, http.StatusOK, StepInstance, InstanceStep(f))
}

func (m *Module) suggestURL(r *http.Request) string {
	if m.d.Cfg.PublicURL != "" {
		return m.d.Cfg.PublicURL
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Mv-Https") == "1" {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// ValidatePublicURL checks an absolute http(s) URL without path, query or credentials.
func ValidatePublicURL(raw string) (string, bool) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return "", true
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return raw, false
	}
	return raw, true
}

func (m *Module) saveInstance(w http.ResponseWriter, r *http.Request) error {
	if err := web.ParseForm(w, r); err != nil {
		return core.ErrBadRequest("invalid form")
	}
	ctx := r.Context()
	f := instanceForm{Name: web.FormValue(r, "name"), PublicURL: web.FormValue(r, "public_url"), Mode: web.FormValue(r, "mode"),
		SignUp: web.FormValue(r, "sign_up"), Errors: web.FormErrors{}}
	if f.Name == "" || len(f.Name) > 80 {
		f.Errors.Add("name", i18n.T(ctx, "setup.instance.name_invalid"))
	}
	u, ok := ValidatePublicURL(f.PublicURL)
	if !ok {
		f.Errors.Add("public_url", i18n.T(ctx, "setup.instance.url_invalid"))
	}
	f.PublicURL = u
	if f.Mode != settings.ModePersonal && f.Mode != settings.ModePrivate && f.Mode != settings.ModePublic {
		f.Errors.Add("mode", i18n.T(ctx, "setup.required"))
	}
	if f.SignUp != settings.SignUpClosed && f.SignUp != settings.SignUpInvite && f.SignUp != settings.SignUpOpen {
		f.Errors.Add("sign_up", i18n.T(ctx, "setup.required"))
	}
	if f.Errors.Has() {
		return m.render(w, r, http.StatusUnprocessableEntity, StepInstance, InstanceStep(f))
	}
	_, err := m.d.Settings.UpdateInstance(ctx, core.UserFrom(ctx).ID, func(i *settings.Instance) error {
		i.Name, i.PublicURL, i.Mode, i.SignUp = f.Name, f.PublicURL, f.Mode, f.SignUp
		i.Telemetry = false
		return nil
	})
	if err != nil {
		f.Errors.Add("", err.Error())
		return m.render(w, r, http.StatusUnprocessableEntity, StepInstance, InstanceStep(f))
	}
	core.Redirect(w, r, "/setup/models")
	return nil
}

// ---- step 3: connect models ----------------------------------------------------------

type modelsView struct {
	Compute   web.Compute
	Recommend *Recommendation
	Providers []ProviderCard
	LocalOnly bool
}

func (m *Module) modelsView(ctx context.Context) (modelsView, error) {
	c, err := web.ComputeSummary(ctx, m.d.Jobs)
	if err != nil {
		return modelsView{}, err
	}
	ms := m.modelStep()
	v := modelsView{Compute: c, LocalOnly: m.d.Settings.MustInstance(ctx).LocalOnly}
	if v.Recommend, err = ms.Recommend(ctx, c.GPUs); err != nil {
		m.d.Log.Warn("setup: model recommendation", "err", err)
	}
	if v.Providers, err = ms.Providers(ctx); err != nil || len(v.Providers) == 0 {
		v.Providers = DefaultProviders()
	}
	return v, nil
}

func (m *Module) modelsPage(w http.ResponseWriter, r *http.Request) error {
	v, err := m.modelsView(r.Context())
	if err != nil {
		return err
	}
	return m.render(w, r, http.StatusOK, StepModels, ModelsStep(v))
}

func (m *Module) saveModels(w http.ResponseWriter, r *http.Request) error {
	if err := web.ParseForm(w, r); err != nil {
		return core.ErrBadRequest("invalid form")
	}
	ctx := r.Context()
	local := web.FormBool(r, "local_only")
	if _, err := m.d.Settings.UpdateInstance(ctx, core.UserFrom(ctx).ID, func(i *settings.Instance) error {
		i.LocalOnly = local
		if local {
			i.RoutingMode = settings.RoutingLocalOnly
		} else if i.RoutingMode == settings.RoutingLocalOnly {
			i.RoutingMode = settings.RoutingBalanced
		}
		return nil
	}); err != nil {
		return err
	}
	if core.IsHTMX(r) && r.PostFormValue("autosave") == "1" {
		msg := i18n.T(ctx, "setup.models.local_off")
		if local {
			msg = i18n.T(ctx, "setup.models.local_on")
		}
		web.Toast(w, "success", msg, "")
		w.WriteHeader(http.StatusNoContent)
		return nil
	}
	core.Redirect(w, r, "/setup/finish")
	return nil
}

// ---- step 4: check & finish -----------------------------------------------------------

// Check is one line of the final checklist.
type Check struct {
	OK    bool
	Warn  bool
	Title string
	Body  string
}

func (m *Module) checks(ctx context.Context) []Check {
	inst := m.d.Settings.MustInstance(ctx)
	var out []Check
	out = append(out, Check{OK: true, Title: i18n.T(ctx, "setup.check.admin"), Body: i18n.T(ctx, "setup.check.admin.body")})
	dbErr := m.d.DB.Ping(ctx)
	out = append(out, Check{OK: dbErr == nil, Title: i18n.T(ctx, "setup.check.database"), Body: i18n.T(ctx, "setup.check.database.body", "dialect", string(m.d.DB.Dialect()))})
	out = append(out, Check{OK: true, Title: i18n.T(ctx, "setup.check.storage"), Body: i18n.T(ctx, "setup.check.storage.body", "kind", m.d.Store.Kind())})
	if m.d.Notify.EmailEnabled() {
		out = append(out, Check{OK: true, Title: i18n.T(ctx, "setup.check.email"), Body: i18n.T(ctx, "setup.check.email.on")})
	} else {
		out = append(out, Check{Warn: true, Title: i18n.T(ctx, "setup.check.email"), Body: i18n.T(ctx, "setup.check.email.off")})
	}
	c, err := web.ComputeSummary(ctx, m.d.Jobs)
	switch {
	case err != nil:
		out = append(out, Check{Warn: true, Title: i18n.T(ctx, "setup.check.workers"), Body: err.Error()})
	case c.WorkersOnline > 0:
		out = append(out, Check{OK: true, Title: i18n.T(ctx, "setup.check.workers"), Body: i18n.T(ctx, "setup.check.workers.on", "count", c.WorkersOnline)})
	default:
		out = append(out, Check{Warn: true, Title: i18n.T(ctx, "setup.check.workers"), Body: i18n.T(ctx, "setup.check.workers.off")})
	}
	if inst.PublicURL == "" && m.d.Cfg.PublicURL == "" {
		out = append(out, Check{Warn: true, Title: i18n.T(ctx, "setup.check.url"), Body: i18n.T(ctx, "setup.check.url.off")})
	} else {
		out = append(out, Check{OK: true, Title: i18n.T(ctx, "setup.check.url"), Body: web.ServerHost(inst.PublicURL, m.d.Cfg.PublicURL)})
	}
	out = append(out, Check{OK: true, Title: i18n.T(ctx, "setup.check.telemetry"), Body: i18n.T(ctx, "setup.check.telemetry.body")})
	if inst.LocalOnly {
		out = append(out, Check{OK: true, Title: i18n.T(ctx, "setup.check.local"), Body: i18n.T(ctx, "setup.check.local.on")})
	}
	return out
}

func (m *Module) finishPage(w http.ResponseWriter, r *http.Request) error {
	return m.render(w, r, http.StatusOK, StepFinish, FinishStep(m.checks(r.Context())))
}

func (m *Module) finish(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	if _, err := m.d.Settings.UpdateInstance(ctx, core.UserFrom(ctx).ID, func(i *settings.Instance) error {
		now := time.Now().UTC()
		i.SetupCompletedAt = &now
		return nil
	}); err != nil {
		return err
	}
	web.Flash(w, r, "success", i18n.T(ctx, "setup.done.title"), i18n.T(ctx, "setup.done.body"))
	core.Redirect(w, r, "/studio")
	return nil
}
