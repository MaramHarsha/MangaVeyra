package mcp

import (
	"context"
	"errors"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/core/httpx"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/pagestore"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/web"
	"github.com/MaramHarsha/MangaVeyra/internal/web/layout"
	"github.com/MaramHarsha/MangaVeyra/internal/web/ui"
)

// G13 view models.

type pageView struct {
	Allowed bool
	MCPURL  string
	Tokens  []tokenView
	Actions []actionView
	Form    tokenForm
	// FormOpen renders the create-token dialog open in the page (no script,
	// or a form with errors).
	FormOpen bool
	Created  *createdView
}

type tokenView struct {
	ID, Name, Scope, Limit string
	Expired                bool
}

type actionView struct {
	ID, Text, Status, Title string
	Pending, Undo           bool
	// EditedSince marks a finished agent generation that cannot be undone
	// because the page changed otherwise since the job's first version.
	EditedSince bool
	// ApproveSoon marks a human-only request whose screen is not built yet.
	ApproveSoon bool
}

type projectOption struct {
	ID, Title string
	Checked   bool
}

type tokenForm struct {
	Name, Projects, Approve, Expires string
	Edit, Generate                   bool
	Options                          []projectOption
	Errors                           map[string]string
}

type createdView struct {
	Name, Secret, Command string
}

const (
	expiryDefault = "90"
	approveFirst  = "0.50"
)

func (m *Module) baseURL(r *http.Request) string {
	inst := m.d.Settings.MustInstance(r.Context())
	for _, u := range []string{inst.PublicURL, m.d.Cfg.PublicURL} {
		if strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") {
			return strings.TrimRight(u, "/")
		}
	}
	scheme := "http"
	if httpx.IsHTTPS(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func (m *Module) agentsPage(w http.ResponseWriter, r *http.Request) error {
	v, err := m.load(r, tokenForm{Projects: "all", Edit: true, Approve: approveFirst, Expires: expiryDefault})
	if err != nil {
		return err
	}
	v.FormOpen = r.URL.Query().Get("new") == "1"
	return m.render(w, r, http.StatusOK, v)
}

func (m *Module) render(w http.ResponseWriter, r *http.Request, status int, v pageView) error {
	return m.ui.Page(w, r, status, web.PageOpts{Title: i18n.T(r.Context(), "agents.title"), Section: layout.SectionStudio,
		Nav: layout.NavAgents, Tab: layout.TabMe}, agentsBody(v))
}

// load collects everything G13 shows for the signed-in person.
func (m *Module) load(r *http.Request, form tokenForm) (pageView, error) {
	ctx := r.Context()
	u := sessionUser(r)
	v := pageView{Allowed: m.allowed(ctx, u.ID), MCPURL: m.baseURL(r) + "/mcp", Form: form}
	projects, err := m.studio.ListProjects(ctx, u.ID, 200)
	if err != nil {
		return v, err
	}
	titles := map[string]string{}
	for _, p := range projects {
		titles[p.ID] = p.Title
		v.Form.Options = append(v.Form.Options, projectOption{ID: p.ID, Title: p.Title, Checked: slices.Contains(form.selected(), p.ID)})
	}
	tokens, err := m.d.Auth.ListTokens(ctx, u.ID, auth.TokenAgent)
	if err != nil {
		return v, err
	}
	now := time.Now()
	for _, t := range tokens {
		v.Tokens = append(v.Tokens, tokenRow(ctx, t, titles, now))
	}
	v.Actions, err = m.actionRows(ctx, u, now)
	return v, err
}

func tokenRow(ctx context.Context, t auth.TokenInfo, titles map[string]string, now time.Time) tokenView {
	tok := t.Token()
	where := i18n.T(ctx, "agents.token.all_projects")
	if len(tok.ProjectIDs) > 0 {
		names := []string{}
		for _, id := range tok.ProjectIDs {
			if title, ok := titles[id]; ok {
				names = append(names, title)
			}
		}
		switch {
		case len(names) == 0:
			where = i18n.T(ctx, "agents.token.no_projects")
		case len(names) <= 2:
			where = strings.Join(names, ", ")
		default:
			where = i18n.T(ctx, "agents.token.projects", "count", len(names))
		}
	}
	access := i18n.T(ctx, "agents.token.read_only")
	limit := "—"
	switch {
	case tok.HasScope(scopeWrite) && tok.HasScope(scopeGenerate):
		access = i18n.T(ctx, "agents.token.edit_generate")
		if tok.ApproveOverUSD != nil {
			limit = i18n.T(ctx, "agents.token.approve_over", "amount", spend(*tok.ApproveOverUSD))
		} else {
			limit = i18n.T(ctx, "agents.token.no_approval")
		}
	case tok.HasScope(scopeWrite):
		access = i18n.T(ctx, "agents.token.edit")
	}
	view := tokenView{ID: t.ID, Name: t.Name, Scope: where + " · " + access, Limit: limit}
	if t.ExpiresAt != nil && !t.ExpiresAt.After(now) {
		view.Expired = true
		view.Limit = i18n.T(ctx, "agents.token.expired")
	}
	return view
}

// actionRows lists every pending request plus the latest other actions.
func (m *Module) actionRows(ctx context.Context, u *httpx.User, now time.Time) ([]actionView, error) {
	rows, err := m.visibleActions(ctx, u.ID, 60)
	if err != nil {
		return nil, err
	}
	out := []actionView{}
	others := 0
	for _, r := range rows {
		if r.Status != statusPending {
			if others >= 10 {
				continue
			}
			others++
		}
		var j *jobs.Job
		if r.JobID.Valid {
			j, _ = m.d.Jobs.Get(ctx, r.JobID.String)
		}
		v := actionView{ID: r.ID, Text: describe(ctx, r), Status: statusText(ctx, r, j, now),
			Title: r.TokenName, Pending: r.Status == statusPending, Undo: undoable(r, j)}
		if v.Undo && r.Kind == kindGenerate {
			// Undo reverts page history: offer it only when nobody else
			// changed the page since the job's first version, and say why not.
			h, err := m.generationHistory(ctx, r)
			v.Undo = err == nil && !h.editedSince
			v.EditedSince = err == nil && h.editedSince
		}
		if r.Detail.V.Note != "" {
			v.Title = r.TokenName + " · " + r.Detail.V.Note
		}
		if v.Pending && r.Action != reqGenerate {
			target := m.humanURL(ctx, r)
			v.ApproveSoon = target == "" || !ui.Ready(ctx, target)
		}
		out = append(out, v)
	}
	return out, nil
}

func (f tokenForm) selected() []string {
	if f.Projects != "some" {
		return nil
	}
	ids := []string{}
	for _, o := range f.Options {
		if o.Checked {
			ids = append(ids, o.ID)
		}
	}
	return ids
}

func (m *Module) setAllowed(w http.ResponseWriter, r *http.Request) error {
	if err := web.ParseForm(w, r); err != nil {
		return core.ErrBadRequest("")
	}
	on := web.FormBool(r, "allowed")
	u := sessionUser(r)
	if _, err := m.d.Settings.UpdateUserPrefs(r.Context(), u.ID, func(p *settings.UserPrefs) error {
		if p.Flags == nil {
			p.Flags = map[string]bool{}
		}
		p.Flags[allowFlag] = on
		return nil
	}); err != nil {
		return err
	}
	m.d.Auth.Audit(r.Context(), auth.AuditEntry{Action: "agent.allow", TargetKind: "user", TargetID: u.ID, Data: map[string]any{"allowed": on}})
	key := "agents.flash.off"
	if on {
		key = "agents.flash.on"
	}
	web.Flash(w, r, "success", i18n.T(r.Context(), key), "")
	core.Redirect(w, r, "/agents")
	return nil
}

func (m *Module) createToken(w http.ResponseWriter, r *http.Request) error {
	if err := web.ParseForm(w, r); err != nil {
		return core.ErrBadRequest("")
	}
	ctx := r.Context()
	u := sessionUser(r)
	form := tokenForm{Name: web.FormValue(r, "name"), Projects: web.FormValue(r, "projects"), Approve: web.FormValue(r, "approve_over"),
		Expires: web.FormValue(r, "expires"), Edit: web.FormBool(r, "edit"), Generate: web.FormBool(r, "generate"), Errors: map[string]string{}}
	picked := r.PostForm["project_ids"]
	projects, err := m.studio.ListProjects(ctx, u.ID, 200)
	if err != nil {
		return err
	}
	for _, p := range projects {
		form.Options = append(form.Options, projectOption{ID: p.ID, Title: p.Title, Checked: slices.Contains(picked, p.ID)})
	}
	if form.Projects != "some" {
		form.Projects = "all"
	}
	name := strings.TrimSpace(form.Name)
	if name == "" || len([]rune(name)) > 80 {
		form.Errors["name"] = i18n.T(ctx, "agents.form.name_error")
	}
	ids := form.selected()
	if form.Projects == "some" && len(ids) == 0 {
		form.Errors["projects"] = i18n.T(ctx, "agents.form.projects_error")
	}
	if form.Generate && !form.Edit {
		form.Errors["access"] = i18n.T(ctx, "agents.form.generate_error")
	}
	var threshold *float64
	if form.Generate && strings.TrimSpace(form.Approve) != "" {
		v, err := strconv.ParseFloat(strings.TrimPrefix(strings.TrimSpace(form.Approve), "$"), 64)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 1000 {
			form.Errors["approve_over"] = i18n.T(ctx, "agents.form.approve_error")
		} else {
			v = math.Round(v*100) / 100
			threshold = &v
		}
	}
	var expires *time.Time
	switch form.Expires {
	case "30", "90", "365":
		days, _ := strconv.Atoi(form.Expires)
		t := time.Now().UTC().Add(time.Duration(days) * 24 * time.Hour)
		expires = &t
	case "never":
	default:
		form.Expires = expiryDefault
		form.Errors["expires"] = i18n.T(ctx, "agents.form.expires_error")
	}
	if len(form.Errors) > 0 {
		v, err := m.load(r, form)
		if err != nil {
			return err
		}
		v.Form, v.FormOpen = form, true
		return m.render(w, r, http.StatusUnprocessableEntity, v)
	}
	scopes := []string{scopeRead}
	if form.Edit {
		scopes = append(scopes, scopeWrite)
	}
	if form.Generate {
		scopes = append(scopes, scopeGenerate)
	}
	raw, info, err := m.d.Auth.CreateToken(ctx, auth.TokenInput{UserID: u.ID, Kind: auth.TokenAgent, Name: name, Scopes: scopes,
		ProjectIDs: ids, ApproveOverUSD: threshold, ExpiresAt: expires})
	if err != nil {
		fe := web.FormErrors{}
		if !fe.AddError(ctx, err) {
			return err
		}
		for k, msg := range fe {
			if k == "" || k == "kind" || k == "scopes" {
				k = "name"
			}
			form.Errors[k] = msg
		}
		v, lerr := m.load(r, form)
		if lerr != nil {
			return lerr
		}
		v.Form, v.FormOpen = form, true
		return m.render(w, r, http.StatusUnprocessableEntity, v)
	}
	v, err := m.load(r, tokenForm{Projects: "all", Edit: true, Approve: approveFirst, Expires: expiryDefault})
	if err != nil {
		return err
	}
	// The token goes in the environment, never on the command line, where
	// other users of the machine can read it in the process list.
	v.Created = &createdView{Name: info.Name, Secret: raw, Command: "MV_AGENT_TOKEN=" + raw + " mangaveyra mcp-stdio --url " + m.baseURL(r)}
	// The secret is shown once and must not be cached anywhere.
	w.Header().Set("Cache-Control", "no-store")
	return m.render(w, r, http.StatusCreated, v)
}

func (m *Module) revokeToken(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	u := sessionUser(r)
	id := r.PathValue("id")
	t, err := m.d.Auth.GetToken(ctx, id)
	if err != nil || t.UserID != u.ID || t.Kind != auth.TokenAgent {
		return core.ErrNotFound()
	}
	if err := m.d.Auth.RevokeToken(ctx, u.ID, id, false); err != nil {
		return err
	}
	web.Flash(w, r, "success", i18n.T(ctx, "agents.flash.revoked", "name", t.Name), "")
	core.Redirect(w, r, "/agents")
	return nil
}

func (m *Module) approveAction(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	d, err := m.approve(ctx, sessionUser(r), r.PathValue("id"))
	var rose *estimateRoseError
	if errors.As(err, &rose) {
		// Nothing ran; the request waits with the new estimate shown.
		web.Flash(w, r, "error", i18n.T(ctx, "agents.error.estimate_rose", "amount", spend(rose.Estimate.USD)), "")
		core.Redirect(w, r, "/agents")
		return nil
	}
	if errors.Is(err, studio.ErrConflict) || errors.Is(err, pagestore.ErrVersion) {
		// The target changed since the agent asked; the request still waits.
		web.Flash(w, r, "error", i18n.T(ctx, "agents.error.stale"), "")
		core.Redirect(w, r, "/agents")
		return nil
	}
	if err != nil {
		return m.decisionError(w, r, err)
	}
	if d.Redirect != "" {
		core.Redirect(w, r, d.Redirect)
		return nil
	}
	web.Flash(w, r, "success", i18n.T(ctx, "agents.flash.approved"), "")
	core.Redirect(w, r, "/agents")
	return nil
}

func (m *Module) declineAction(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	if err := m.decline(ctx, sessionUser(r), r.PathValue("id")); err != nil {
		return m.decisionError(w, r, err)
	}
	web.Flash(w, r, "success", i18n.T(ctx, "agents.flash.declined"), "")
	core.Redirect(w, r, "/agents")
	return nil
}

func (m *Module) undoAction(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	if err := m.undo(ctx, sessionUser(r), r.PathValue("id")); err != nil {
		return m.decisionError(w, r, err)
	}
	web.Flash(w, r, "success", i18n.T(ctx, "agents.flash.undone"), "")
	core.Redirect(w, r, "/agents")
	return nil
}

// decisionError explains a refused decision with a toast back on G13; other
// people's actions are simply not found.
func (m *Module) decisionError(w http.ResponseWriter, r *http.Request, err error) error {
	ctx := r.Context()
	key := ""
	switch {
	case errors.Is(err, errNotYours):
		return core.ErrNotFound()
	case errors.Is(err, errDecided):
		key = "agents.error.decided"
	case errors.Is(err, errChanged), errors.Is(err, studio.ErrConflict):
		key = "agents.error.changed"
	case errors.Is(err, errNoUndo):
		key = "agents.error.no_undo"
	case errors.Is(err, errNoScreen):
		key = "agents.error.no_screen"
	case errors.Is(err, studio.ErrLocked):
		key = "agents.error.locked"
	case errors.Is(err, studio.ErrForbidden):
		key = "agents.error.forbidden"
	}
	if key == "" {
		var ae *httpx.APIError
		if errors.As(err, &ae) && ae.Status >= 500 {
			return err
		}
		m.d.Log.WarnContext(ctx, "mcp: decision refused", "err", err)
		key = "agents.error.failed"
	}
	web.Flash(w, r, "error", i18n.T(ctx, key), "")
	core.Redirect(w, r, "/agents")
	return nil
}
