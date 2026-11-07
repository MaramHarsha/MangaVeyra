package authui

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/a-h/templ"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
	"github.com/MaramHarsha/MangaVeyra/internal/web"
	"github.com/MaramHarsha/MangaVeyra/internal/web/layout"
)

type templComponent = templ.Component

// ---- sign up ----------------------------------------------------------------

type signupForm struct {
	Host        string
	Line        string
	Invite      string
	InvitedBy   string
	InviteEmail string
	// Closed is true when sign-up is closed (or needs an invite) and none was given.
	Closed      bool
	ClosedText  string
	Rules       string
	DisplayName string
	Username    string
	Email       string
	Agree       bool
	Errors      web.FormErrors
}

func (m *Module) signupForm(r *http.Request, invite string) signupForm {
	ctx := r.Context()
	inst := m.d.Settings.MustInstance(ctx)
	host := web.ServerHost(inst.PublicURL, m.d.Cfg.PublicURL, r.Host)
	f := signupForm{Host: host, Line: i18n.T(ctx, "instance.mode."+inst.Mode), Invite: strings.TrimSpace(invite), Rules: inst.Rules, Errors: web.FormErrors{}}
	if f.Invite != "" {
		inv, err := m.d.Auth.LookupInvite(ctx, f.Invite)
		if err != nil || !inv.Valid(m.d.Auth.Now()) {
			f.Errors.Add("", i18n.T(ctx, "auth.signup.invite_invalid"))
			f.Invite = ""
		} else {
			if inv.CreatedBy != nil {
				if u, err := m.d.Auth.UserByID(ctx, *inv.CreatedBy); err == nil {
					f.InvitedBy = u.Name()
				}
			}
			f.InviteEmail = inv.Email
			f.Email = inv.Email
		}
	}
	if f.Invite == "" {
		switch inst.SignUp {
		case settings.SignUpClosed:
			f.Closed, f.ClosedText = true, i18n.T(ctx, "auth.signup.closed")
		case settings.SignUpInvite:
			f.Closed, f.ClosedText = true, i18n.T(ctx, "auth.signup.invite_required")
		}
	}
	return f
}

func (m *Module) signupPage(w http.ResponseWriter, r *http.Request) error {
	if core.UserFrom(r.Context()) != nil {
		core.Redirect(w, r, "/studio")
		return nil
	}
	f := m.signupForm(r, r.URL.Query().Get("invite"))
	return m.renderSignup(w, r, http.StatusOK, f)
}

func (m *Module) renderSignup(w http.ResponseWriter, r *http.Request, status int, f signupForm) error {
	return m.page(w, r, status, i18n.T(r.Context(), "auth.page.signup.title", "host", f.Host),
		layout.AuthOpts{Art: layout.AuthArtPage, SFX: "BA-DUM", HideArtMobile: true, BackURL: "/login"}, SignupPage(f))
}

func (m *Module) signup(w http.ResponseWriter, r *http.Request) error {
	if err := web.ParseForm(w, r); err != nil {
		return core.ErrBadRequest("invalid form")
	}
	ctx := r.Context()
	f := m.signupForm(r, r.PostFormValue("invite"))
	if f.Closed {
		return m.renderSignup(w, r, http.StatusForbidden, f)
	}
	f.DisplayName = web.FormValue(r, "display_name")
	f.Username = auth.NormalizeUsername(web.FormValue(r, "username"))
	f.Email = web.FormValue(r, "email")
	f.Agree = web.FormBool(r, "agree")
	password := r.PostFormValue("password")
	// The designed sign-up form (A3) has no username field: the handle is
	// derived from the email address and made unique on conflict.
	derived := f.Username == ""
	if derived {
		f.Username = deriveUsername(f.Email, f.DisplayName)
	}
	if f.Email == "" {
		f.Errors.Add("email", i18n.T(ctx, "auth.page.required"))
	}
	if !f.Agree {
		f.Errors.Add("agree", i18n.T(ctx, "auth.page.signup.agree_required"))
	}
	if !f.Errors.Has() {
		in := auth.SignupInput{Username: f.Username, Email: f.Email, Password: password,
			DisplayName: f.DisplayName, InviteCode: f.Invite, Locale: i18n.LocaleFrom(ctx)}
		u, err := m.d.Auth.Signup(ctx, in)
		for attempt := 2; derived && attempt <= 20 && usernameTaken(err); attempt++ {
			in.Username = usernameVariant(f.Username, attempt)
			u, err = m.d.Auth.Signup(ctx, in)
		}
		if err == nil {
			if u.Status != auth.StatusActive {
				web.Flash(w, r, "info", i18n.T(ctx, "auth.page.signup.pending"), "")
				core.Redirect(w, r, "/login")
				return nil
			}
			token, _, err := m.d.Auth.CreateSession(ctx, u.ID, false, core.ClientIP(r), r.UserAgent())
			if err != nil {
				return err
			}
			m.d.Auth.SetSessionCookie(w, r, token, false)
			web.Flash(w, r, "success", i18n.T(ctx, "auth.page.signup.welcome", "name", u.Name()), "")
			core.Redirect(w, r, "/onboarding/1")
			return nil
		}
		if errors.Is(err, auth.ErrInviteInvalid) {
			f.Invite = ""
		}
		if !f.Errors.AddError(ctx, err) {
			return err
		}
	}
	return m.renderSignup(w, r, http.StatusUnprocessableEntity, f)
}

// ---- forgot / reset -------------------------------------------------------------

type forgotForm struct {
	Email   string
	NoEmail bool
	Sent    bool
	Errors  web.FormErrors
	// TTL is the reset link lifetime in minutes.
	TTL int
}

func (m *Module) forgotForm() forgotForm {
	return forgotForm{Errors: web.FormErrors{}, TTL: int(auth.ResetTTL.Minutes())}
}

func (m *Module) forgotPage(w http.ResponseWriter, r *http.Request) error {
	return m.renderForgot(w, r, http.StatusOK, m.forgotForm())
}

func (m *Module) renderForgot(w http.ResponseWriter, r *http.Request, status int, f forgotForm) error {
	return m.page(w, r, status, i18n.T(r.Context(), "auth.page.forgot.title"),
		layout.AuthOpts{Art: layout.AuthArtMark, HideArtMobile: true, BackURL: "/login"}, ForgotPage(f))
}

func (m *Module) forgot(w http.ResponseWriter, r *http.Request) error {
	if err := web.ParseForm(w, r); err != nil {
		return core.ErrBadRequest("invalid form")
	}
	ctx := r.Context()
	f := m.forgotForm()
	f.Email = web.FormValue(r, "email")
	if !m.d.Auth.EmailEnabled() {
		// The designed lead already explains the admin route; confirm it.
		f.NoEmail = true
		return m.renderForgot(w, r, http.StatusConflict, f)
	}
	if err := auth.ValidateEmail(auth.NormalizeEmail(f.Email)); err != nil || f.Email == "" {
		f.Errors.Add("email", i18n.T(ctx, "auth.email.invalid"))
		return m.renderForgot(w, r, http.StatusUnprocessableEntity, f)
	}
	if err := m.d.Auth.RequestPasswordReset(ctx, f.Email, core.ClientIP(r)); err != nil {
		if errors.Is(err, auth.ErrNoEmail) {
			f.NoEmail = true
			return m.renderForgot(w, r, http.StatusConflict, f)
		}
		if !f.Errors.AddError(ctx, err) {
			return err
		}
		return m.renderForgot(w, r, http.StatusTooManyRequests, f)
	}
	f.Sent = true
	return m.renderForgot(w, r, http.StatusOK, f)
}

type resetForm struct {
	Token    string
	Username string
	Invalid  bool
	Errors   web.FormErrors
}

func (m *Module) resetPage(w http.ResponseWriter, r *http.Request) error {
	f := resetForm{Token: r.PathValue("token"), Errors: web.FormErrors{}}
	u, err := m.d.Auth.CheckResetToken(r.Context(), f.Token)
	if err != nil {
		f.Invalid = true
		return m.renderReset(w, r, http.StatusBadRequest, f)
	}
	f.Username = u.Username
	return m.renderReset(w, r, http.StatusOK, f)
}

func (m *Module) renderReset(w http.ResponseWriter, r *http.Request, status int, f resetForm) error {
	w.Header().Set("Referrer-Policy", "no-referrer")
	return m.page(w, r, status, i18n.T(r.Context(), "auth.page.reset.title"),
		layout.AuthOpts{Art: layout.AuthArtMark, HideArtMobile: true, BackURL: "/login"}, ResetPage(f))
}

func (m *Module) reset(w http.ResponseWriter, r *http.Request) error {
	if err := web.ParseForm(w, r); err != nil {
		return core.ErrBadRequest("invalid form")
	}
	ctx := r.Context()
	f := resetForm{Token: r.PathValue("token"), Errors: web.FormErrors{}}
	password, confirm := r.PostFormValue("password"), r.PostFormValue("confirm")
	if password != confirm {
		f.Errors.Add("confirm", i18n.T(ctx, "auth.page.reset.mismatch"))
		if u, err := m.d.Auth.CheckResetToken(ctx, f.Token); err == nil {
			f.Username = u.Username
		}
		return m.renderReset(w, r, http.StatusUnprocessableEntity, f)
	}
	u, err := m.d.Auth.ResetPassword(ctx, f.Token, password)
	if err != nil {
		if errors.Is(err, auth.ErrResetInvalid) {
			f.Invalid = true
			return m.renderReset(w, r, http.StatusBadRequest, f)
		}
		if !f.Errors.AddError(ctx, err) {
			return err
		}
		if cu, err := m.d.Auth.CheckResetToken(ctx, f.Token); err == nil {
			f.Username = cu.Username
		}
		return m.renderReset(w, r, http.StatusUnprocessableEntity, f)
	}
	web.Flash(w, r, "success", i18n.T(ctx, "auth.page.reset.done"), "")
	core.Redirect(w, r, "/login?u="+url.QueryEscape(u.Username))
	return nil
}

// deriveUsername builds a valid handle from the email's local part (or the
// display name) for the designed sign-up form, which has no username field.
func deriveUsername(email, display string) string {
	source := email
	if at := strings.IndexByte(source, '@'); at > 0 {
		source = source[:at]
	} else if strings.TrimSpace(display) != "" {
		source = display
	}
	var b strings.Builder
	for _, r := range strings.ToLower(source) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '.' || r == '-' || r == '_':
			if b.Len() > 0 {
				b.WriteRune(r)
			}
		}
	}
	name := strings.Trim(b.String(), "._-")
	for strings.Contains(name, "..") {
		name = strings.ReplaceAll(name, "..", ".")
	}
	if len(name) > 28 {
		name = strings.Trim(name[:28], "._-")
	}
	if len(name) < 3 {
		name = "member"
	}
	if auth.ValidateUsername(name) != nil {
		name = usernameVariant(name, 1)
	}
	return name
}

// usernameVariant appends a number so a taken or reserved handle becomes free.
func usernameVariant(base string, n int) string {
	suffix := strconv.Itoa(n)
	if len(base)+len(suffix) > 32 {
		base = strings.Trim(base[:32-len(suffix)], "._-")
	}
	return base + suffix
}

func usernameTaken(err error) bool {
	var fe *auth.FieldError
	return errors.As(err, &fe) && fe.Field == "username"
}
