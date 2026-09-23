package auth

import (
	"net/http"
	"strings"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/core/httpx"
)

// Module exposes the auth JSON API. HTML pages are rendered by the web layer.
type Module struct {
	S *Service
}

// NewModule wraps the service as an HTTP module.
func NewModule(s *Service) *Module { return &Module{S: s} }

// Name implements core.Module.
func (m *Module) Name() string { return "auth" }

// Routes registers /api/v1/auth/*.
//
//	POST   /api/v1/auth/login                 {identifier, password, totp_code?, remember?}
//	POST   /api/v1/auth/logout
//	POST   /api/v1/auth/signup                {username, email, password, display_name?, invite?}
//	GET    /api/v1/auth/me
//	PATCH  /api/v1/auth/me                    {display_name?, email?, bio?, locale?, timezone?, links?}
//	GET    /api/v1/auth/sessions              DELETE /api/v1/auth/sessions/{id}   POST /api/v1/auth/sessions/revoke-others
//	POST   /api/v1/auth/password/forgot       {email}
//	GET    /api/v1/auth/password/reset/{token}
//	POST   /api/v1/auth/password/reset        {token, password}
//	POST   /api/v1/auth/password/change       {current, password}
//	POST   /api/v1/auth/totp/begin            POST /api/v1/auth/totp/confirm {code}   POST /api/v1/auth/totp/disable {password}
//	POST   /api/v1/auth/totp/recovery-codes   {password}
//	GET    /api/v1/auth/tokens                POST /api/v1/auth/tokens {kind, name, scopes, project_ids, approve_over_usd, expires_in_days}
//	DELETE /api/v1/auth/tokens/{id}
//	GET    /api/v1/auth/invites/{code}        (public validity check for the sign-up page)
//	Sessions, password change, two-factor and token management need a browser session (not a bearer token);
//	admin routes called with a bearer token need its "admin" scope.
//	admin: GET/POST /api/v1/auth/invites, DELETE /api/v1/auth/invites/{id},
//	       GET /api/v1/auth/users, POST /api/v1/auth/users/{id}/role|status|reset-link|disable-2fa,
//	       GET /api/v1/auth/audit
func (m *Module) Routes(r *httpx.Router) {
	g := r.Group("/api/v1/auth")
	g.API("POST /login", m.login)
	g.API("POST /logout", m.logout)
	g.API("POST /signup", m.signup)
	g.API("GET /me", m.me, httpx.RequireUser)
	g.API("PATCH /me", m.updateMe, httpx.RequireUser, httpx.RequireHuman)
	g.API("GET /sessions", m.sessions, httpx.RequireSession)
	g.API("DELETE /sessions/{id}", m.revokeSession, httpx.RequireSession)
	g.API("POST /sessions/revoke-others", m.revokeOthers, httpx.RequireSession)
	g.API("POST /password/forgot", m.forgot)
	g.API("GET /password/reset/{token}", m.checkReset)
	g.API("POST /password/reset", m.reset)
	g.API("POST /password/change", m.changePassword, httpx.RequireSession)
	g.API("POST /totp/begin", m.totpBegin, httpx.RequireSession)
	g.API("POST /totp/confirm", m.totpConfirm, httpx.RequireSession)
	g.API("POST /totp/disable", m.totpDisable, httpx.RequireSession)
	g.API("POST /totp/recovery-codes", m.totpRecovery, httpx.RequireSession)
	g.API("GET /tokens", m.listTokens, httpx.RequireSession)
	g.API("POST /tokens", m.createToken, httpx.RequireSession)
	g.API("DELETE /tokens/{id}", m.revokeToken, httpx.RequireSession)
	g.API("GET /invites/{code}", m.checkInvite)

	admin := g.Group("", httpx.RequireRole(RoleAdmin), httpx.RequireHuman, httpx.RequireScope("admin"))
	admin.API("GET /invites", m.listInvites)
	admin.API("POST /invites", m.createInvite)
	admin.API("DELETE /invites/{id}", m.revokeInvite)
	admin.API("GET /users", m.listUsers)
	admin.API("POST /users/{id}/role", m.setRole)
	admin.API("POST /users/{id}/status", m.setStatus)
	admin.API("POST /users/{id}/reset-link", m.resetLink)
	admin.API("POST /users/{id}/disable-2fa", m.adminDisable2FA)
	admin.API("GET /audit", m.audit)
}

type loginReq struct {
	Identifier string `json:"identifier"`
	Password   string `json:"password"`
	TOTPCode   string `json:"totp_code"`
	Remember   bool   `json:"remember"`
}

func (m *Module) login(w http.ResponseWriter, r *http.Request) error {
	var in loginReq
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		return err
	}
	res, err := m.S.Login(r.Context(), LoginInput{Identifier: in.Identifier, Password: in.Password, TOTPCode: in.TOTPCode,
		Remember: in.Remember, IP: httpx.ClientIP(r), UserAgent: r.UserAgent()})
	if err != nil {
		return APIError(r.Context(), err)
	}
	m.S.SetSessionCookie(w, r, res.Token, in.Remember)
	httpx.JSON(w, http.StatusOK, map[string]any{"user": res.User})
	return nil
}

func (m *Module) logout(w http.ResponseWriter, r *http.Request) error {
	if c, err := r.Cookie(SessionCookie); err == nil {
		_ = m.S.Logout(r.Context(), c.Value)
	}
	m.S.ClearSessionCookie(w, r)
	httpx.OK(w)
	return nil
}

type signupReq struct {
	Username    string `json:"username"`
	Email       string `json:"email"`
	Password    string `json:"password"`
	DisplayName string `json:"display_name"`
	Invite      string `json:"invite"`
}

func (m *Module) signup(w http.ResponseWriter, r *http.Request) error {
	var in signupReq
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		return err
	}
	u, err := m.S.Signup(r.Context(), SignupInput{Username: in.Username, Email: in.Email, Password: in.Password,
		DisplayName: in.DisplayName, InviteCode: in.Invite, Locale: ""})
	if err != nil {
		return APIError(r.Context(), err)
	}
	if u.Status == StatusActive {
		token, _, err := m.S.CreateSession(r.Context(), u.ID, false, httpx.ClientIP(r), r.UserAgent())
		if err != nil {
			return err
		}
		m.S.SetSessionCookie(w, r, token, false)
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{"user": u})
	return nil
}

func (m *Module) me(w http.ResponseWriter, r *http.Request) error {
	id := httpx.IdentityFrom(r.Context())
	out := map[string]any{"user": id.User}
	if id.Token != nil {
		out["token"] = id.Token
	}
	httpx.JSON(w, http.StatusOK, out)
	return nil
}

type profileReq struct {
	DisplayName *string   `json:"display_name"`
	Email       *string   `json:"email"`
	Bio         *string   `json:"bio"`
	Locale      *string   `json:"locale"`
	Timezone    *string   `json:"timezone"`
	Links       *[]string `json:"links"`
}

func (m *Module) updateMe(w http.ResponseWriter, r *http.Request) error {
	var in profileReq
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		return err
	}
	u, err := m.S.UpdateProfile(r.Context(), httpx.UserFrom(r.Context()).ID, ProfileUpdate{DisplayName: in.DisplayName,
		Email: in.Email, Bio: in.Bio, Locale: in.Locale, Timezone: in.Timezone, Links: in.Links})
	if err != nil {
		return APIError(r.Context(), err)
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"user": u})
	return nil
}

func (m *Module) sessions(w http.ResponseWriter, r *http.Request) error {
	id := httpx.IdentityFrom(r.Context())
	list, err := m.S.ListSessions(r.Context(), id.User.ID, id.SessionID)
	if err != nil {
		return err
	}
	if list == nil {
		list = []Session{}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": list})
	return nil
}

func (m *Module) revokeSession(w http.ResponseWriter, r *http.Request) error {
	if err := m.S.RevokeSession(r.Context(), httpx.UserFrom(r.Context()).ID, r.PathValue("id")); err != nil {
		return err
	}
	httpx.OK(w)
	return nil
}

func (m *Module) revokeOthers(w http.ResponseWriter, r *http.Request) error {
	id := httpx.IdentityFrom(r.Context())
	n, err := m.S.RevokeOtherSessions(r.Context(), id.User.ID, id.SessionID)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"revoked": n})
	return nil
}

func (m *Module) forgot(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Email string `json:"email"`
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		return err
	}
	if err := m.S.RequestPasswordReset(r.Context(), in.Email, httpx.ClientIP(r)); err != nil {
		return err
	}
	httpx.OK(w)
	return nil
}

func (m *Module) checkReset(w http.ResponseWriter, r *http.Request) error {
	u, err := m.S.CheckResetToken(r.Context(), r.PathValue("token"))
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"username": u.Username})
	return nil
}

func (m *Module) reset(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		return err
	}
	if _, err := m.S.ResetPassword(r.Context(), in.Token, in.Password); err != nil {
		return APIError(r.Context(), err)
	}
	httpx.OK(w)
	return nil
}

func (m *Module) changePassword(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Current  string `json:"current"`
		Password string `json:"password"`
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		return err
	}
	id := httpx.IdentityFrom(r.Context())
	if err := m.S.ChangePassword(r.Context(), id.User.ID, in.Current, in.Password, id.SessionID); err != nil {
		return APIError(r.Context(), err)
	}
	httpx.OK(w)
	return nil
}

func (m *Module) totpBegin(w http.ResponseWriter, r *http.Request) error {
	e, err := m.S.BeginTOTP(r.Context(), httpx.UserFrom(r.Context()).ID)
	if err != nil {
		return httpx.ErrConflict(err.Error())
	}
	httpx.JSON(w, http.StatusOK, e)
	return nil
}

func (m *Module) totpConfirm(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Code string `json:"code"`
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		return err
	}
	codes, err := m.S.ConfirmTOTP(r.Context(), httpx.UserFrom(r.Context()).ID, in.Code)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"recovery_codes": codes})
	return nil
}

func (m *Module) totpDisable(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Password string `json:"password"`
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		return err
	}
	if err := m.S.DisableTOTP(r.Context(), httpx.UserFrom(r.Context()).ID, in.Password); err != nil {
		return err
	}
	httpx.OK(w)
	return nil
}

func (m *Module) totpRecovery(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Password string `json:"password"`
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		return err
	}
	codes, err := m.S.RegenerateRecoveryCodes(r.Context(), httpx.UserFrom(r.Context()).ID, in.Password)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"recovery_codes": codes})
	return nil
}

func (m *Module) listTokens(w http.ResponseWriter, r *http.Request) error {
	u := httpx.UserFrom(r.Context())
	kind := r.URL.Query().Get("kind")
	userID := u.ID
	if kind == TokenWorker && u.IsAdmin() {
		userID = ""
	}
	list, err := m.S.ListTokens(r.Context(), userID, kind)
	if err != nil {
		return err
	}
	if list == nil {
		list = []TokenInfo{}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": list})
	return nil
}

type tokenReq struct {
	Kind           string   `json:"kind"`
	Name           string   `json:"name"`
	Scopes         []string `json:"scopes"`
	ProjectIDs     []string `json:"project_ids"`
	ApproveOverUSD *float64 `json:"approve_over_usd"`
	ExpiresInDays  int      `json:"expires_in_days"`
}

func (m *Module) createToken(w http.ResponseWriter, r *http.Request) error {
	var in tokenReq
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		return err
	}
	if in.Kind == "" {
		in.Kind = TokenAPI
	}
	var exp *time.Time
	if in.ExpiresInDays > 3650 {
		return httpx.ErrValidation(map[string]string{"expires_in_days": "at most 3650 days"})
	}
	if in.ExpiresInDays > 0 {
		t := time.Now().UTC().Add(time.Duration(in.ExpiresInDays) * 24 * time.Hour)
		exp = &t
	}
	raw, info, err := m.S.CreateToken(r.Context(), TokenInput{UserID: httpx.UserFrom(r.Context()).ID, Kind: in.Kind, Name: in.Name,
		Scopes: in.Scopes, ProjectIDs: in.ProjectIDs, ApproveOverUSD: in.ApproveOverUSD, ExpiresAt: exp})
	if err != nil {
		return APIError(r.Context(), err)
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{"token": raw, "info": info})
	return nil
}

func (m *Module) revokeToken(w http.ResponseWriter, r *http.Request) error {
	u := httpx.UserFrom(r.Context())
	if err := m.S.RevokeToken(r.Context(), u.ID, r.PathValue("id"), u.IsAdmin()); err != nil {
		return err
	}
	httpx.OK(w)
	return nil
}

func (m *Module) checkInvite(w http.ResponseWriter, r *http.Request) error {
	inv, err := m.S.LookupInvite(r.Context(), r.PathValue("code"))
	if err != nil {
		return err
	}
	if !inv.Valid(m.S.now()) {
		return ErrInviteInvalid
	}
	email := ""
	if inv.Email != "" {
		email = maskEmail(inv.Email)
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"valid": true, "role": inv.Role, "email": email})
	return nil
}

func maskEmail(e string) string {
	at := strings.LastIndex(e, "@")
	if at <= 1 {
		return "…" + e[at:]
	}
	return e[:1] + "…" + e[at:]
}

func (m *Module) listInvites(w http.ResponseWriter, r *http.Request) error {
	list, err := m.S.ListInvites(r.Context(), r.URL.Query().Get("all") == "1")
	if err != nil {
		return err
	}
	if list == nil {
		list = []Invite{}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": list})
	return nil
}

func (m *Module) createInvite(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Email         string `json:"email"`
		Role          string `json:"role"`
		MaxUses       int    `json:"max_uses"`
		ExpiresInDays int    `json:"expires_in_days"`
		Note          string `json:"note"`
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		return err
	}
	exp := time.Duration(in.ExpiresInDays) * 24 * time.Hour
	if in.ExpiresInDays < 0 {
		exp = -1
	}
	inv, err := m.S.CreateInvite(r.Context(), httpx.UserFrom(r.Context()).ID, InviteInput{Email: in.Email, Role: in.Role, MaxUses: in.MaxUses, ExpiresIn: exp, Note: in.Note})
	if err != nil {
		return APIError(r.Context(), err)
	}
	httpx.JSON(w, http.StatusCreated, inv)
	return nil
}

func (m *Module) revokeInvite(w http.ResponseWriter, r *http.Request) error {
	if err := m.S.RevokeInvite(r.Context(), r.PathValue("id")); err != nil {
		return err
	}
	httpx.OK(w)
	return nil
}

func (m *Module) listUsers(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	p := httpx.ParsePage(r, 50, 200)
	users, total, err := m.S.ListUsers(r.Context(), ListFilter{Query: q.Get("q"), Role: q.Get("role"), Status: q.Get("status"), Limit: p.Limit, Offset: p.Offset()})
	if err != nil {
		return err
	}
	if users == nil {
		users = []*User{}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": users, "total": total, "page": p.Page})
	return nil
}

func (m *Module) setRole(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Role string `json:"role"`
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		return err
	}
	if err := m.S.SetRole(r.Context(), r.PathValue("id"), in.Role); err != nil {
		return APIError(r.Context(), err)
	}
	httpx.OK(w)
	return nil
}

func (m *Module) setStatus(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Status string `json:"status"`
		Reason string `json:"reason"`
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		return err
	}
	if err := m.S.SetStatus(r.Context(), r.PathValue("id"), in.Status, in.Reason); err != nil {
		return APIError(r.Context(), err)
	}
	httpx.OK(w)
	return nil
}

func (m *Module) resetLink(w http.ResponseWriter, r *http.Request) error {
	link, err := m.S.CreateResetLink(r.Context(), httpx.UserFrom(r.Context()).ID, r.PathValue("id"))
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"link": link})
	return nil
}

func (m *Module) adminDisable2FA(w http.ResponseWriter, r *http.Request) error {
	if err := m.S.AdminDisableTOTP(r.Context(), r.PathValue("id")); err != nil {
		return err
	}
	httpx.OK(w)
	return nil
}

func (m *Module) audit(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	p := httpx.ParsePage(r, 100, 500)
	list, err := m.S.ListAudit(r.Context(), AuditFilter{ActorID: q.Get("actor"), Action: q.Get("action"), TargetKind: q.Get("target_kind"),
		TargetID: q.Get("target_id"), ProjectID: q.Get("project"), Before: p.Cursor, Limit: p.Limit + 1})
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, httpx.Paginate(list, p.Limit, func(a AuditRecord) string { return a.ID }))
	return nil
}
