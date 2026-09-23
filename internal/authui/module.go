// Package authui serves the account entry pages built on the auth service:
// A2 log in (with the two-step code step), log out, A3 sign up (invites,
// server rules, password strength), A4 forgot / reset password (with the
// self-host note when email is not configured) and A5 onboarding (creator
// type, genres with an Explore shelf preview, default art style).
//
// Every form is a plain HTML form that works without JavaScript.
package authui

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
	"github.com/MaramHarsha/MangaVeyra/internal/web"
	"github.com/MaramHarsha/MangaVeyra/internal/web/layout"
)

// Module is the auth pages module.
type Module struct {
	d  *core.Deps
	ui *web.UI
}

// New creates the module.
func New(d *core.Deps) *Module { return &Module{d: d} }

// Name implements core.Module.
func (m *Module) Name() string { return "authui" }

// Init looks up the web layer.
func (m *Module) Init(ctx context.Context) error {
	m.ui = web.From(m.d)
	return nil
}

// Routes implements core.Module.
//
//	GET  /login            POST /login            POST /login/code (two-step)
//	POST /logout
//	GET  /signup?invite=   POST /signup
//	GET  /forgot           POST /forgot
//	GET  /reset/{token}    POST /reset/{token}
//	GET  /onboarding[/{step}]  POST /onboarding/{step}  POST /onboarding/skip  GET /onboarding/preview
func (m *Module) Routes(r *core.Router) {
	r.Page("GET /login", m.loginPage)
	r.Page("POST /login", m.login)
	r.Page("POST /login/code", m.loginCode)
	r.Page("POST /logout", m.logout)
	r.Page("GET /signup", m.signupPage)
	r.Page("POST /signup", m.signup)
	r.Page("GET /forgot", m.forgotPage)
	r.Page("POST /forgot", m.forgot)
	r.Page("GET /reset/{token}", m.resetPage)
	r.Page("POST /reset/{token}", m.reset)
	r.Page("GET /onboarding", func(w http.ResponseWriter, r *http.Request) error {
		core.Redirect(w, r, "/onboarding/1")
		return nil
	}, core.RequireUser)
	r.Page("GET /onboarding/preview", m.onboardingPreview, core.RequireUser)
	r.Page("POST /onboarding/skip", m.onboardingSkip, core.RequireUser)
	r.Page("GET /onboarding/{step}", m.onboardingPage, core.RequireUser)
	r.Page("POST /onboarding/{step}", m.onboardingSave, core.RequireUser)
}

// page renders an auth page (split frame, no navigation).
func (m *Module) page(w http.ResponseWriter, r *http.Request, status int, title string, o layout.AuthOpts, body templComponent) error {
	c := m.ui.Chrome(w, r, web.PageOpts{Title: title})
	return web.Render(w, r, status, web.Wrap(layout.AuthShell(c, o), body), nil)
}

// serverLine is "studio.local · private group".
func (m *Module) serverLine(r *http.Request, inst settings.Instance) string {
	host := web.ServerHost(inst.PublicURL, m.d.Cfg.PublicURL, r.Host)
	return host + " · " + i18n.T(r.Context(), "instance.mode."+inst.Mode)
}

// ---- log in -----------------------------------------------------------------

type loginForm struct {
	Identifier string
	Remember   bool
	Next       string
	Errors     web.FormErrors
	Footer     string
	FooterLink string
	FooterHref string
	ServerLine string
}

func (m *Module) loginForm(r *http.Request, next string) loginForm {
	ctx := r.Context()
	inst := m.d.Settings.MustInstance(ctx)
	f := loginForm{Next: next, Remember: true, Errors: web.FormErrors{}, ServerLine: m.serverLine(r, inst)}
	switch inst.SignUp {
	case settings.SignUpOpen:
		f.Footer, f.FooterLink, f.FooterHref = i18n.T(ctx, "auth.page.login.new_here"), i18n.T(ctx, "auth.page.login.create"), "/signup"
	case settings.SignUpInvite:
		f.Footer = i18n.T(ctx, "auth.page.login.ask_invite")
	default:
		f.Footer = i18n.T(ctx, "auth.signup.closed")
	}
	return f
}

func (m *Module) loginPage(w http.ResponseWriter, r *http.Request) error {
	next := core.SafeRedirectTarget(r.URL.Query().Get("next"), "")
	if core.UserFrom(r.Context()) != nil {
		core.Redirect(w, r, core.SafeRedirectTarget(next, "/studio"))
		return nil
	}
	f := m.loginForm(r, next)
	f.Identifier = strings.TrimSpace(r.URL.Query().Get("u"))
	return m.page(w, r, http.StatusOK, i18n.T(r.Context(), "auth.page.login.title"), layout.AuthOpts{Art: layout.AuthArtMark}, LoginPage(f))
}

func (m *Module) login(w http.ResponseWriter, r *http.Request) error {
	if err := web.ParseForm(w, r); err != nil {
		return core.ErrBadRequest("invalid form")
	}
	ctx := r.Context()
	f := m.loginForm(r, core.SafeRedirectTarget(r.PostFormValue("next"), ""))
	f.Identifier = web.FormValue(r, "identifier")
	f.Remember = web.FormBool(r, "remember")
	password := r.PostFormValue("password")
	if f.Identifier == "" {
		f.Errors.Add("identifier", i18n.T(ctx, "auth.page.required"))
	}
	if password == "" {
		f.Errors.Add("password", i18n.T(ctx, "auth.page.required"))
	}
	if !f.Errors.Has() {
		res, err := m.d.Auth.Login(ctx, auth.LoginInput{Identifier: f.Identifier, Password: password, Remember: f.Remember,
			IP: core.ClientIP(r), UserAgent: r.UserAgent()})
		switch {
		case err == nil:
			m.finishLogin(w, r, res, f.Remember, f.Next)
			return nil
		case errors.Is(err, auth.ErrTOTPRequired):
			setPending(w, r, m.d.Secrets, pendingLogin{Identifier: f.Identifier, Password: password, Remember: f.Remember, Next: f.Next,
				Expires: time.Now().Add(pendingTTL).Unix()})
			return m.page(w, r, http.StatusOK, i18n.T(ctx, "auth.page.code.title"), layout.AuthOpts{Art: layout.AuthArtMark},
				CodePage(codeForm{Errors: web.FormErrors{}, ServerLine: f.ServerLine}))
		default:
			if !f.Errors.AddError(ctx, err) {
				return err
			}
		}
	}
	return m.page(w, r, statusFor(f.Errors), i18n.T(ctx, "auth.page.login.title"), layout.AuthOpts{Art: layout.AuthArtMark}, LoginPage(f))
}

type codeForm struct {
	Errors     web.FormErrors
	ServerLine string
}

func (m *Module) loginCode(w http.ResponseWriter, r *http.Request) error {
	if err := web.ParseForm(w, r); err != nil {
		return core.ErrBadRequest("invalid form")
	}
	ctx := r.Context()
	inst := m.d.Settings.MustInstance(ctx)
	f := codeForm{Errors: web.FormErrors{}, ServerLine: m.serverLine(r, inst)}
	p, ok := readPending(r, m.d.Secrets)
	if !ok {
		web.Flash(w, r, "warning", i18n.T(ctx, "auth.page.code.expired"), "")
		core.Redirect(w, r, "/login")
		return nil
	}
	code := strings.ReplaceAll(web.FormValue(r, "code"), " ", "")
	if code == "" {
		f.Errors.Add("code", i18n.T(ctx, "auth.page.required"))
	} else {
		res, err := m.d.Auth.Login(ctx, auth.LoginInput{Identifier: p.Identifier, Password: p.Password, TOTPCode: code,
			Remember: p.Remember, IP: core.ClientIP(r), UserAgent: r.UserAgent()})
		if err == nil {
			clearPending(w, r)
			m.finishLogin(w, r, res, p.Remember, p.Next)
			return nil
		}
		if errors.Is(err, auth.ErrTOTPInvalid) {
			f.Errors.Add("code", i18n.T(ctx, "auth.login.totp_invalid"))
		} else if !f.Errors.AddError(ctx, err) {
			return err
		}
	}
	return m.page(w, r, statusFor(f.Errors), i18n.T(ctx, "auth.page.code.title"), layout.AuthOpts{Art: layout.AuthArtMark}, CodePage(f))
}

func (m *Module) finishLogin(w http.ResponseWriter, r *http.Request, res *auth.LoginResult, remember bool, next string) {
	m.d.Auth.SetSessionCookie(w, r, res.Token, remember)
	target := core.SafeRedirectTarget(next, "/studio")
	if next == "" && !onboardingDone(r.Context(), m.d, res.User.ID) && res.User.Role != auth.RoleAdmin {
		target = "/onboarding/1"
	}
	core.Redirect(w, r, target)
}

func (m *Module) logout(w http.ResponseWriter, r *http.Request) error {
	if c, err := r.Cookie(auth.SessionCookie); err == nil {
		_ = m.d.Auth.Logout(r.Context(), c.Value)
	}
	m.d.Auth.ClearSessionCookie(w, r)
	web.Flash(w, r, "info", i18n.T(r.Context(), "auth.page.logout.done"), "")
	core.Redirect(w, r, "/login")
	return nil
}

func statusFor(fe web.FormErrors) int {
	if fe.Has() {
		return http.StatusUnprocessableEntity
	}
	return http.StatusOK
}
