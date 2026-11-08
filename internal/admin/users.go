package admin

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
	"github.com/MaramHarsha/MangaVeyra/internal/web"
	"github.com/MaramHarsha/MangaVeyra/internal/web/ui"
)

// membersPerPage is one page of the members list.
const membersPerPage = 50

// Invite expiry choices (form value → lifetime; negative never expires).
var inviteExpiries = []struct {
	Value string
	In    time.Duration
}{{"1d", 24 * time.Hour}, {"7d", 7 * 24 * time.Hour}, {"30d", 30 * 24 * time.Hour}, {"never", -1}}

// memberItem is one row of the members list.
type memberItem struct {
	ID, Name, Username, Role string
	Status, StatusText       string
	StatusTone               ui.Tone
	Usage, Meta              string
	Self                     bool
	Menu                     []ui.MenuItem
}

// inviteItem is one active invite.
type inviteItem struct {
	ID, URL, Role, Email, Uses, Expires string
	Highlight                           bool
}

// limitsView is the "Limits per member" card.
type limitsView struct {
	GPUJobs, Jobs        string
	Spend, ServerSpend   string
	StorageMB, ProjectsN string
	UploadMB             string
	Errors               map[string]string
}

// inviteForm is the invite dialog.
type inviteForm struct {
	Role, Expires, Email, Uses string
	Errors                     map[string]string
}

type usersView struct {
	Query       string
	Members     []memberItem
	Total       int
	More        string
	Pending     []memberItem
	Invites     []inviteItem
	SignUp      string
	Approval    bool
	Limits      limitsView
	Dialog      string
	Invite      inviteForm
	ResetLink   string
	ResetFor    string
	Delete      *memberItem
	DeleteError string
	Error       string
	Email       bool
}

func (m *Module) usersPage(w http.ResponseWriter, r *http.Request) error {
	v, err := m.usersView(r)
	if err != nil {
		return err
	}
	q := r.URL.Query()
	switch q.Get("dialog") {
	case "invite":
		v.Dialog = "invite"
	}
	if id := q.Get("delete"); id != "" {
		u, err := m.d.Auth.UserByID(r.Context(), id)
		if err != nil {
			return core.ErrNotFound()
		}
		it := m.memberItem(r.Context(), memberRow{User: u})
		v.Delete, v.Dialog = &it, "delete"
	}
	return m.renderUsers(w, r, http.StatusOK, v)
}

func (m *Module) renderUsers(w http.ResponseWriter, r *http.Request, status int, v usersView) error {
	f, err := m.frame(r.Context(), r, tabUsers)
	if err != nil {
		return err
	}
	return m.render(w, r, status, f, UsersTab(v))
}

// usersView loads the Users tab for the request's search and page.
func (m *Module) usersView(r *http.Request) (usersView, error) {
	ctx := r.Context()
	inst := m.d.Settings.MustInstance(ctx)
	q := r.URL.Query()
	v := usersView{Query: strings.TrimSpace(q.Get("q")), SignUp: inst.SignUp, Approval: inst.RequireApproval,
		Email: m.d.Notify.EmailEnabled(), Limits: limitsOf(inst, m.d.Cfg.MaxUploadMB),
		Invite: inviteForm{Role: auth.RoleCreator, Expires: "7d", Uses: "1"}}
	offset, _ := strconv.Atoi(q.Get("offset"))
	if offset < 0 || offset > 1_000_000 {
		offset = 0
	}
	list, total, err := m.d.Auth.ListUsers(ctx, auth.ListFilter{Query: v.Query, Limit: membersPerPage, Offset: offset})
	if err != nil {
		return v, err
	}
	rows, err := memberExtras(ctx, m.d.DB, list)
	if err != nil {
		return v, err
	}
	for _, row := range rows {
		v.Members = append(v.Members, m.memberItem(ctx, row))
	}
	v.Total = total
	if offset+len(list) < total {
		next := url.Values{"offset": {strconv.Itoa(offset + membersPerPage)}}
		if v.Query != "" {
			next.Set("q", v.Query)
		}
		v.More = "/admin/users?" + next.Encode()
	}
	pending, _, err := m.d.Auth.ListUsers(ctx, auth.ListFilter{Status: auth.StatusPending, Limit: membersPerPage})
	if err != nil {
		return v, err
	}
	for _, u := range pending {
		v.Pending = append(v.Pending, m.memberItem(ctx, memberRow{User: u}))
	}
	invites, err := m.d.Auth.ListInvites(ctx, false)
	if err != nil {
		return v, err
	}
	highlight := q.Get("invite")
	for _, inv := range invites {
		v.Invites = append(v.Invites, m.inviteItem(ctx, inv, inv.ID == highlight))
	}
	return v, nil
}

func limitsOf(inst settings.Instance, uploadMB int) limitsView {
	l := inst.Limits
	return limitsView{GPUJobs: itoa(l.RunningGPUJobs), Jobs: itoa(l.RunningJobs), Spend: amount(l.MonthlyRemoteSpendUSD),
		ServerSpend: amount(l.ServerMonthlyRemoteSpendUSD), StorageMB: formatMB(l.StorageQuotaMB), ProjectsN: itoa(l.MaxProjectsPerUser),
		UploadMB: formatMB(uploadMB), Errors: map[string]string{}}
}

func (m *Module) memberItem(ctx context.Context, row memberRow) memberItem {
	u := row.User
	me := core.UserFrom(ctx)
	it := memberItem{ID: u.ID, Name: u.Name(), Username: u.Username, Role: u.Role, Status: u.Status, Self: me != nil && me.ID == u.ID}
	switch u.Status {
	case auth.StatusActive:
		it.StatusTone = ui.ToneSuccess
	case auth.StatusSuspended:
		it.StatusTone = ui.ToneError
	default:
		it.StatusTone = ui.ToneWarning
	}
	it.StatusText = i18n.T(ctx, "admin.status."+u.Status)
	if row.Jobs > 0 || row.Bytes > 0 || u.Status == auth.StatusActive {
		it.Usage = i18n.T(ctx, "admin.users.usage", "jobs", i18n.T(ctx, "admin.users.jobs", "count", row.Jobs), "storage", formatBytes(row.Bytes))
	} else {
		it.Usage = "—"
	}
	seen := i18n.T(ctx, "admin.users.never")
	if !row.LastSeen.IsZero() {
		seen = i18n.T(ctx, "admin.users.seen", "when", ago(ctx, row.LastSeen, m.now()))
	}
	it.Meta = i18n.T(ctx, "admin.users.meta", "projects", i18n.T(ctx, "admin.users.projects", "count", row.Projects), "seen", seen)
	base := "/admin/users/" + url.PathEscape(u.ID)
	if !it.Self {
		switch u.Status {
		case auth.StatusActive:
			it.Menu = append(it.Menu, ui.MenuItem{Href: base + "/suspend", Label: i18n.T(ctx, "admin.users.suspend"), Icon: "user-off", Post: true})
		case auth.StatusSuspended:
			it.Menu = append(it.Menu, ui.MenuItem{Href: base + "/reactivate", Label: i18n.T(ctx, "admin.users.reactivate"), Icon: "user-check", Post: true})
		case auth.StatusPending:
			it.Menu = append(it.Menu, ui.MenuItem{Href: base + "/approve", Label: i18n.T(ctx, "admin.users.approve"), Icon: "user-check", Post: true})
		}
	}
	it.Menu = append(it.Menu, ui.MenuItem{Href: base + "/reset-link", Label: i18n.T(ctx, "admin.users.reset_link"), Icon: "key", Post: true})
	if !it.Self {
		it.Menu = append(it.Menu, ui.MenuItem{Separator: true},
			ui.MenuItem{Href: "/admin/users?delete=" + url.QueryEscape(u.ID), Label: i18n.T(ctx, "admin.users.delete"), Icon: "trash", Danger: true})
	}
	return it
}

func (m *Module) inviteItem(ctx context.Context, inv auth.Invite, highlight bool) inviteItem {
	it := inviteItem{ID: inv.ID, URL: inv.URL, Role: roleLabel(ctx, inv.Role), Email: inv.Email, Highlight: highlight,
		Uses: i18n.T(ctx, "admin.invites.uses", "used", inv.Uses, "max", inv.MaxUses)}
	if inv.ExpiresAt == nil {
		it.Expires = i18n.T(ctx, "admin.invites.never")
	} else {
		it.Expires = i18n.T(ctx, "admin.invites.expires", "date", dateText(ctx, *inv.ExpiresAt))
	}
	return it
}

// userError renders the Users tab with a message after a refused change.
func (m *Module) userError(w http.ResponseWriter, r *http.Request, err error) error {
	ctx := r.Context()
	msg := ""
	var fe *auth.FieldError
	switch {
	case errors.Is(err, auth.ErrLastAdmin):
		msg = i18n.T(ctx, "admin.error.last_admin")
	case errors.Is(err, auth.ErrUserNotFound):
		return core.ErrNotFound()
	case errors.As(err, &fe):
		msg = i18n.T(ctx, fe.Key, fe.Args...)
	default:
		return err
	}
	v, verr := m.usersView(r)
	if verr != nil {
		return verr
	}
	v.Error = msg
	return m.renderUsers(w, r, http.StatusConflict, v)
}

// target user of a /admin/users/{id}/... action (never the acting admin for
// status changes and deletion).
func (m *Module) targetUser(r *http.Request, allowSelf bool) (*auth.User, error) {
	u, err := m.d.Auth.UserByID(r.Context(), r.PathValue("id"))
	if err != nil {
		return nil, core.ErrNotFound()
	}
	if !allowSelf && u.ID == core.UserFrom(r.Context()).ID {
		return nil, core.ErrConflict("admins cannot change their own account here")
	}
	return u, nil
}

// POST /admin/users/{id}/role (role)
func (m *Module) setRole(w http.ResponseWriter, r *http.Request) error {
	if err := web.ParseForm(w, r); err != nil {
		return core.ErrBadRequest("form")
	}
	u, err := m.targetUser(r, true)
	if err != nil {
		return err
	}
	role := web.FormValue(r, "role")
	if role != u.Role {
		if err := m.d.Auth.SetRole(r.Context(), u.ID, role); err != nil {
			return m.userError(w, r, err)
		}
	}
	return done(w, r, "/admin/users")
}

// setStatus is POST /admin/users/{id}/{suspend|reactivate|approve}.
func (m *Module) setStatus(status string) core.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		u, err := m.targetUser(r, false)
		if err != nil {
			return err
		}
		if status == auth.StatusActive && u.Status == auth.StatusActive {
			return done(w, r, "/admin/users")
		}
		if err := m.d.Auth.SetStatus(r.Context(), u.ID, status, ""); err != nil {
			return m.userError(w, r, err)
		}
		return done(w, r, "/admin/users")
	}
}

// POST /admin/users/{id}/decline turns away a pending sign-up (the account,
// which has nothing in it yet, is deleted).
func (m *Module) declineUser(w http.ResponseWriter, r *http.Request) error {
	u, err := m.targetUser(r, false)
	if err != nil {
		return err
	}
	if u.Status != auth.StatusPending {
		return core.ErrConflict("only pending sign-ups can be declined")
	}
	if err := m.d.Auth.DeleteUser(r.Context(), u.ID); err != nil {
		return m.userError(w, r, err)
	}
	return done(w, r, "/admin/users")
}

// POST /admin/users/{id}/reset-link shows a one-time reset link once.
func (m *Module) resetLink(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	u, err := m.targetUser(r, true)
	if err != nil {
		return err
	}
	link, err := m.d.Auth.CreateResetLink(ctx, core.UserFrom(ctx).ID, u.ID)
	if err != nil {
		return m.userError(w, r, err)
	}
	v, err := m.usersView(r)
	if err != nil {
		return err
	}
	v.Dialog, v.ResetLink, v.ResetFor = "reset", link, u.Name()
	return m.renderUsers(w, r, http.StatusOK, v)
}

// POST /admin/users/{id}/delete (confirm = the username)
func (m *Module) deleteUser(w http.ResponseWriter, r *http.Request) error {
	if err := web.ParseForm(w, r); err != nil {
		return core.ErrBadRequest("form")
	}
	ctx := r.Context()
	u, err := m.targetUser(r, false)
	if err != nil {
		return err
	}
	if !strings.EqualFold(strings.TrimPrefix(web.FormValue(r, "confirm"), "@"), u.Username) {
		v, err := m.usersView(r)
		if err != nil {
			return err
		}
		it := m.memberItem(ctx, memberRow{User: u})
		v.Delete, v.Dialog, v.DeleteError = &it, "delete", i18n.T(ctx, "admin.users.delete_mismatch")
		return m.renderUsers(w, r, http.StatusUnprocessableEntity, v)
	}
	if err := m.d.Auth.DeleteUser(ctx, u.ID); err != nil {
		return m.userError(w, r, err)
	}
	return done(w, r, "/admin/users")
}

// POST /admin/signup (sign_up, require_approval)
func (m *Module) saveSignup(w http.ResponseWriter, r *http.Request) error {
	if err := web.ParseForm(w, r); err != nil {
		return core.ErrBadRequest("form")
	}
	ctx := r.Context()
	mode, approval := web.FormValue(r, "sign_up"), web.FormBool(r, "require_approval")
	inst, err := m.d.Settings.UpdateInstance(ctx, core.UserFrom(ctx).ID, func(i *settings.Instance) error {
		i.SignUp, i.RequireApproval = mode, approval
		return nil
	})
	if err != nil {
		return core.ErrBadRequest("sign_up")
	}
	m.audit(ctx, "admin.settings.signup", "instance", "sign_up", map[string]any{"sign_up": inst.SignUp, "require_approval": inst.RequireApproval})
	return done(w, r, "/admin/users")
}

// POST /admin/limits: the per-member limits the server enforces (running
// jobs, remote spend) and the whole server's spend cap.
func (m *Module) saveLimits(w http.ResponseWriter, r *http.Request) error {
	if err := web.ParseForm(w, r); err != nil {
		return core.ErrBadRequest("form")
	}
	ctx := r.Context()
	errs := map[string]string{}
	whole := func(name string) int {
		n, err := strconv.Atoi(web.FormValue(r, name))
		if err != nil || n < 1 || n > 64 {
			errs[name] = i18n.T(ctx, "admin.limits.error.jobs")
		}
		return n
	}
	usd := func(name string) float64 {
		raw := web.FormValue(r, name)
		if raw == "" {
			return 0
		}
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 1_000_000 {
			errs[name] = i18n.T(ctx, "admin.limits.error.spend")
		}
		return v
	}
	gpu, jobs := whole("running_gpu_jobs"), whole("running_jobs")
	spend, server := usd("monthly_spend"), usd("server_spend")
	if len(errs) > 0 {
		v, err := m.usersView(r)
		if err != nil {
			return err
		}
		v.Limits.GPUJobs, v.Limits.Jobs = web.FormValue(r, "running_gpu_jobs"), web.FormValue(r, "running_jobs")
		v.Limits.Spend, v.Limits.ServerSpend = web.FormValue(r, "monthly_spend"), web.FormValue(r, "server_spend")
		v.Limits.Errors = errs
		return m.renderUsers(w, r, http.StatusUnprocessableEntity, v)
	}
	inst, err := m.d.Settings.UpdateInstance(ctx, core.UserFrom(ctx).ID, func(i *settings.Instance) error {
		i.Limits.RunningGPUJobs, i.Limits.RunningJobs = gpu, jobs
		i.Limits.MonthlyRemoteSpendUSD, i.Limits.ServerMonthlyRemoteSpendUSD = spend, server
		return nil
	})
	if err != nil {
		return core.ErrBadRequest("limits")
	}
	m.audit(ctx, "admin.settings.limits", "instance", "limits", map[string]any{"running_gpu_jobs": inst.Limits.RunningGPUJobs,
		"running_jobs": inst.Limits.RunningJobs, "monthly_remote_spend_usd": inst.Limits.MonthlyRemoteSpendUSD,
		"server_monthly_remote_spend_usd": inst.Limits.ServerMonthlyRemoteSpendUSD})
	return done(w, r, "/admin/users")
}

// POST /admin/invites (role, expires, email, uses)
func (m *Module) createInvite(w http.ResponseWriter, r *http.Request) error {
	if err := web.ParseForm(w, r); err != nil {
		return core.ErrBadRequest("form")
	}
	ctx := r.Context()
	f := inviteForm{Role: web.FormValue(r, "role"), Expires: web.FormValue(r, "expires"), Email: web.FormValue(r, "email"),
		Uses: web.FormValue(r, "uses"), Errors: map[string]string{}}
	in := auth.InviteInput{Role: f.Role, Email: f.Email}
	found := false
	for _, e := range inviteExpiries {
		if e.Value == f.Expires {
			in.ExpiresIn, found = e.In, true
		}
	}
	if !found {
		f.Errors["expires"] = i18n.T(ctx, "admin.invites.error.expires")
	}
	if f.Role != auth.RoleCreator && f.Role != auth.RoleReader && f.Role != auth.RoleAdmin {
		f.Errors["role"] = i18n.T(ctx, "admin.invites.error.role")
	}
	if f.Uses == "" {
		f.Uses = "1"
	}
	if n, err := strconv.Atoi(f.Uses); err != nil || n < 1 || n > 100 {
		f.Errors["uses"] = i18n.T(ctx, "admin.invites.error.uses")
	} else {
		in.MaxUses = n
	}
	var inv *auth.Invite
	if len(f.Errors) == 0 {
		var err error
		inv, err = m.d.Auth.CreateInvite(ctx, core.UserFrom(ctx).ID, in)
		var fe *auth.FieldError
		switch {
		case errors.As(err, &fe):
			f.Errors[fe.Field] = i18n.T(ctx, fe.Key, fe.Args...)
		case err != nil:
			return err
		}
	}
	if len(f.Errors) > 0 {
		v, err := m.usersView(r)
		if err != nil {
			return err
		}
		v.Dialog, v.Invite = "invite", f
		return m.renderUsers(w, r, http.StatusUnprocessableEntity, v)
	}
	return done(w, r, "/admin/users?invite="+url.QueryEscape(inv.ID)+"#invites")
}

// POST /admin/invites/{id}/revoke
func (m *Module) revokeInvite(w http.ResponseWriter, r *http.Request) error {
	if err := m.d.Auth.RevokeInvite(r.Context(), r.PathValue("id")); err != nil {
		if errors.Is(err, auth.ErrInviteInvalid) {
			return core.ErrNotFound()
		}
		return err
	}
	return done(w, r, "/admin/users#invites")
}
