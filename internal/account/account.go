package account

import (
	"bytes"
	"encoding/base64"
	"errors"
	"image/png"
	"net/http"
	"strings"

	"github.com/a-h/templ"
	"github.com/pquerna/otp"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/web"
)

type templComponent = templ.Component

// SessionView is one active session row.
type SessionView struct {
	ID      string
	Device  string
	When    string
	Current bool
}

type accountView struct {
	Host        string
	DisplayName string
	Username    string
	Email       string
	Bio         string
	TwoFactor   bool
	Sessions    []SessionView
	Errors      web.FormErrors
}

func (m *Module) accountView(r *http.Request, fe web.FormErrors) (accountView, error) {
	ctx := r.Context()
	id := core.IdentityFrom(ctx)
	u, err := m.d.Auth.UserByID(ctx, id.User.ID)
	if err != nil {
		return accountView{}, err
	}
	inst := m.d.Settings.MustInstance(ctx)
	v := accountView{Host: web.ServerHost(inst.PublicURL, m.d.Cfg.PublicURL, r.Host), DisplayName: u.DisplayName, Username: u.Username,
		Email: u.Email, Bio: u.Bio, TwoFactor: u.TOTPEnabled, Errors: fe}
	list, err := m.d.Auth.ListSessions(ctx, u.ID, id.SessionID)
	if err != nil {
		return v, err
	}
	for _, s := range list {
		when := i18n.T(ctx, "time.just_now")
		if s.Current {
			when = i18n.T(ctx, "settings.account.now")
		} else if !s.LastSeenAt.IsZero() {
			when = s.LastSeenAt.Format("2 Jan 15:04")
		}
		v.Sessions = append(v.Sessions, SessionView{ID: s.ID, Device: DeviceName(s.UserAgent), When: when, Current: s.Current})
	}
	return v, nil
}

// DeviceName turns a user agent into "Firefox · Linux".
func DeviceName(ua string) string {
	browser, osName := "Browser", ""
	switch {
	case strings.Contains(ua, "Firefox/"):
		browser = "Firefox"
	case strings.Contains(ua, "Edg/"):
		browser = "Edge"
	case strings.Contains(ua, "Chrome/"):
		browser = "Chrome"
	case strings.Contains(ua, "Safari/"):
		browser = "Safari"
	case strings.HasPrefix(ua, "Go-http-client"), strings.HasPrefix(ua, "curl/"):
		browser = "API client"
	}
	switch {
	case strings.Contains(ua, "Android"):
		osName = "Android"
	case strings.Contains(ua, "iPhone"), strings.Contains(ua, "iPad"):
		osName = "iOS"
	case strings.Contains(ua, "Windows"):
		osName = "Windows"
	case strings.Contains(ua, "Mac OS X"), strings.Contains(ua, "Macintosh"):
		osName = "macOS"
	case strings.Contains(ua, "Linux"):
		osName = "Linux"
	}
	if osName == "" {
		return browser
	}
	return browser + " · " + osName
}

func (m *Module) renderAccount(w http.ResponseWriter, r *http.Request, status int, fe web.FormErrors) error {
	v, err := m.accountView(r, fe)
	if err != nil {
		return err
	}
	return m.render(w, r, status, SecAccount, false, AccountSection(v))
}

func (m *Module) saveProfile(w http.ResponseWriter, r *http.Request) error {
	if err := web.ParseForm(w, r); err != nil {
		return core.ErrBadRequest("invalid form")
	}
	ctx := r.Context()
	name, email, bio := web.FormValue(r, "display_name"), web.FormValue(r, "email"), web.FormValue(r, "bio")
	fe := web.FormErrors{}
	if name == "" {
		fe.Add("display_name", i18n.T(ctx, "auth.page.required"))
	}
	if len([]rune(bio)) > 280 {
		fe.Add("bio", i18n.T(ctx, "settings.account.bio_long"))
	}
	if !fe.Has() {
		_, err := m.d.Auth.UpdateProfile(ctx, core.UserFrom(ctx).ID, auth.ProfileUpdate{DisplayName: &name, Email: &email, Bio: &bio})
		if err == nil {
			return m.saved(w, r, SecAccount, nil)
		}
		if !fe.AddError(ctx, err) {
			return err
		}
	}
	return m.renderAccount(w, r, http.StatusUnprocessableEntity, fe)
}

func (m *Module) passwordPage(w http.ResponseWriter, r *http.Request) error {
	return m.render(w, r, http.StatusOK, SecAccount, false, PasswordSection(web.FormErrors{}))
}

func (m *Module) changePassword(w http.ResponseWriter, r *http.Request) error {
	if err := web.ParseForm(w, r); err != nil {
		return core.ErrBadRequest("invalid form")
	}
	ctx := r.Context()
	id := core.IdentityFrom(ctx)
	cur, next, confirm := r.PostFormValue("current"), r.PostFormValue("password"), r.PostFormValue("confirm")
	fe := web.FormErrors{}
	if next != confirm {
		fe.Add("confirm", i18n.T(ctx, "auth.page.reset.mismatch"))
	} else if err := m.d.Auth.ChangePassword(ctx, id.User.ID, cur, next, id.SessionID); err != nil {
		if errors.Is(err, auth.ErrWrongPassword) {
			fe.Add("current", i18n.T(ctx, "auth.password.wrong"))
		} else if !fe.AddError(ctx, err) {
			return err
		}
	}
	if fe.Has() {
		return m.render(w, r, http.StatusUnprocessableEntity, SecAccount, false, PasswordSection(fe))
	}
	web.Flash(w, r, "success", i18n.T(ctx, "settings.account.password_changed"), i18n.T(ctx, "settings.account.password_changed_body"))
	core.Redirect(w, r, "/settings/account")
	return nil
}

// twoFactorView is the enrolment page: secret, QR code and the code field.
type twoFactorView struct {
	Enabled       bool
	Secret        string
	OTPAuth       string
	QR            string
	RecoveryCodes []string
	Errors        web.FormErrors
}

func (m *Module) twoFactorPage(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	u, err := m.d.Auth.UserByID(ctx, core.UserFrom(ctx).ID)
	if err != nil {
		return err
	}
	if u.TOTPEnabled {
		return m.render(w, r, http.StatusOK, SecAccount, false, TwoFactorSection(twoFactorView{Enabled: true, Errors: web.FormErrors{}}))
	}
	enr, err := m.d.Auth.BeginTOTP(ctx, u.ID)
	if err != nil {
		return err
	}
	v := twoFactorView{Secret: enr.Secret, OTPAuth: enr.OTPAuthURL, QR: qrDataURL(enr.OTPAuthURL), Errors: web.FormErrors{}}
	w.Header().Set("Cache-Control", "no-store")
	return m.render(w, r, http.StatusOK, SecAccount, false, TwoFactorSection(v))
}

// qrDataURL renders the otpauth URL as a PNG data URL ("" on failure; the secret is shown as text too).
func qrDataURL(otpauth string) string {
	key, err := otp.NewKeyFromURL(otpauth)
	if err != nil {
		return ""
	}
	img, err := key.Image(200, 200)
	if err != nil {
		return ""
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return ""
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
}

func (m *Module) confirmTwoFactor(w http.ResponseWriter, r *http.Request) error {
	if err := web.ParseForm(w, r); err != nil {
		return core.ErrBadRequest("invalid form")
	}
	ctx := r.Context()
	uid := core.UserFrom(ctx).ID
	codes, err := m.d.Auth.ConfirmTOTP(ctx, uid, strings.ReplaceAll(web.FormValue(r, "code"), " ", ""))
	if err != nil {
		// The pending secret stays stored; show the same one again (it travels in hidden fields).
		v := twoFactorView{Secret: web.FormValue(r, "secret"), Errors: web.FormErrors{}}
		if u := web.FormValue(r, "otpauth"); strings.HasPrefix(u, "otpauth://") {
			v.OTPAuth, v.QR = u, qrDataURL(u)
		}
		v.Errors.Add("code", i18n.T(ctx, "auth.login.totp_invalid"))
		return m.render(w, r, http.StatusUnprocessableEntity, SecAccount, false, TwoFactorSection(v))
	}
	w.Header().Set("Cache-Control", "no-store")
	web.Toast(w, "success", i18n.T(ctx, "settings.account.2fa_on"), "")
	return m.render(w, r, http.StatusOK, SecAccount, false, TwoFactorSection(twoFactorView{Enabled: true, RecoveryCodes: codes, Errors: web.FormErrors{}}))
}

func (m *Module) disableTwoFactor(w http.ResponseWriter, r *http.Request) error {
	if err := web.ParseForm(w, r); err != nil {
		return core.ErrBadRequest("invalid form")
	}
	ctx := r.Context()
	if err := m.d.Auth.DisableTOTP(ctx, core.UserFrom(ctx).ID, r.PostFormValue("password")); err != nil {
		fe := web.FormErrors{}
		if errors.Is(err, auth.ErrWrongPassword) {
			fe.Add("", i18n.T(ctx, "auth.password.wrong"))
		} else if !fe.AddError(ctx, err) {
			return err
		}
		return m.render(w, r, http.StatusUnprocessableEntity, SecAccount, false, TwoFactorSection(twoFactorView{Enabled: true, Errors: fe}))
	}
	web.Flash(w, r, "success", i18n.T(ctx, "settings.account.2fa_off"), "")
	core.Redirect(w, r, "/settings/account")
	return nil
}

func (m *Module) revokeSession(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	if err := m.d.Auth.RevokeSession(ctx, core.UserFrom(ctx).ID, r.PathValue("id")); err != nil {
		return err
	}
	web.Flash(w, r, "success", i18n.T(ctx, "settings.account.signed_out"), "")
	core.Redirect(w, r, "/settings/account")
	return nil
}

func (m *Module) revokeOthers(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	id := core.IdentityFrom(ctx)
	n, err := m.d.Auth.RevokeOtherSessions(ctx, id.User.ID, id.SessionID)
	if err != nil {
		return err
	}
	web.Flash(w, r, "success", i18n.T(ctx, "settings.account.signed_out_others", "count", int(n)), "")
	core.Redirect(w, r, "/settings/account")
	return nil
}

// deleteAccount deletes the member's account after re-checking the password.
func (m *Module) deleteAccount(w http.ResponseWriter, r *http.Request) error {
	if err := web.ParseForm(w, r); err != nil {
		return core.ErrBadRequest("invalid form")
	}
	ctx := r.Context()
	u := core.UserFrom(ctx)
	fe := web.FormErrors{}
	if web.FormValue(r, "confirm") != u.Username {
		fe.Add("", i18n.T(ctx, "settings.account.delete_confirm_wrong", "username", u.Username))
	} else if err := m.d.Auth.CheckPassword(ctx, u.ID, r.PostFormValue("password")); err != nil {
		if !errors.Is(err, auth.ErrWrongPassword) {
			return err
		}
		fe.Add("", i18n.T(ctx, "auth.password.wrong"))
	} else if err := m.d.Auth.DeleteUser(ctx, u.ID); err != nil {
		if errors.Is(err, auth.ErrLastAdmin) {
			fe.Add("", i18n.T(ctx, "settings.account.last_admin"))
		} else {
			return err
		}
	}
	if fe.Has() {
		return m.renderAccount(w, r, http.StatusUnprocessableEntity, fe)
	}
	m.d.Auth.ClearSessionCookie(w, r)
	web.Flash(w, r, "info", i18n.T(ctx, "settings.account.deleted"), "")
	core.Redirect(w, r, "/login")
	return nil
}
