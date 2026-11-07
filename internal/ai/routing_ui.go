package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/web"
	"github.com/MaramHarsha/MangaVeyra/internal/web/layout"
)

type routingView struct {
	Mode                             string
	Admin, LicenceGuard, UsersMayAdd bool
	// CanPin is true for admins and for owners of at least one project: only
	// they can save a pin, so only they get the add-pin form.
	CanPin bool
	// PrivateEndpoints is the live private-network endpoint setting (G12).
	PrivateEndpoints bool
	Limit            float64
	Decisions        []routingDecisionView
	Pins             []routingPinView
	Models           []ModelManifest
	Notice           string
	Error            string
}
type routingDecisionView struct {
	Task         string `db:"task"`
	Mode         string `db:"mode"`
	Model        string `db:"chosen_model_id"`
	Provider     string `db:"chosen_provider_id"`
	ProviderKind string `db:"provider_kind"`
	Overridden   bool   `db:"overridden"`
	Explanation  string `db:"explanation"`
}
type routingPinView struct {
	ID       string `db:"id"`
	Scope    string `db:"scope_kind"`
	ScopeID  string `db:"scope_id"`
	Task     string `db:"task"`
	Model    string `db:"model_id"`
	Provider string `db:"provider_id"`
	Editable bool   `db:"-"`
}

// limitAmount is the amount shown beside the spend-limit switch. An
// unlimited server shows the default amount the switch would turn on.
func (v routingView) limitAmount() float64 {
	if v.Limit > 0 {
		return v.Limit
	}
	return settings.DefaultInstance().Limits.MonthlyRemoteSpendUSD
}

// modelName is a catalogue model's display name (its ID when unknown).
func (v routingView) modelName(id string) string {
	for _, m := range v.Models {
		if m.ID == id {
			return m.Name
		}
	}
	if id == PlaceholderArt {
		return "Placeholder art"
	}
	return id
}

func (m *Module) routingPage(w http.ResponseWriter, r *http.Request) error {
	actor, err := browserProviderActor(r.Context())
	if err != nil {
		return core.ErrForbidden("")
	}
	v, err := m.loadRoutingView(r.Context(), actor)
	if err != nil {
		return err
	}
	v.Notice = r.URL.Query().Get("saved")
	return m.renderRouting(w, r, http.StatusOK, v)
}

func (m *Module) renderRouting(w http.ResponseWriter, r *http.Request, status int, v routingView) error {
	return m.ui.Page(w, r, status, web.PageOpts{Title: i18n.T(r.Context(), "aiimage.routing.title"), BackURL: "/models", Section: layout.SectionStudio, Nav: layout.NavModels}, RoutingPage(v))
}

func (m *Module) loadRoutingView(ctx context.Context, actor providerActor) (routingView, error) {
	inst, err := m.image.readSettings(ctx)
	if err != nil {
		return routingView{}, err
	}
	v := routingView{Mode: inst.RoutingMode, Admin: actor.Admin, LicenceGuard: inst.Content.LicenceGuard, UsersMayAdd: inst.UsersMayAddProviders,
		Limit: inst.Limits.MonthlyRemoteSpendUSD, PrivateEndpoints: m.private.Allowed(ctx), CanPin: actor.Admin}
	v.Models, err = m.catalog.list(ctx)
	if err != nil {
		return v, err
	}
	if err := m.d.DB.Select(ctx, &v.Decisions, `SELECT d.task, d.mode, d.chosen_model_id, d.chosen_provider_id, COALESCE(p.kind, '') AS provider_kind, d.overridden, d.explanation
		FROM routing_decisions d LEFT JOIN providers p ON p.id = d.chosen_provider_id
		WHERE d.user_id = ? ORDER BY d.created_at DESC, d.id DESC LIMIT 20`, actor.UserID); err != nil {
		return v, err
	}
	if !v.CanPin {
		var owned int
		if err := m.d.DB.Get(ctx, &owned, `SELECT COUNT(*) FROM project_members WHERE user_id = ? AND role = ?`, actor.UserID, studio.RoleOwner); err != nil {
			return v, err
		}
		v.CanPin = owned > 0
	}
	var pins []routingPinView
	if err := m.d.DB.Select(ctx, &pins, `SELECT id, scope_kind, scope_id, task, model_id, provider_id FROM routing_pins ORDER BY id DESC LIMIT 201`); err != nil {
		return v, err
	}
	for _, pin := range pins {
		if pin.Scope == "server" || pin.Scope == "panel_type" {
			pin.Editable = actor.Admin
			v.Pins = append(v.Pins, pin)
		} else if m.authorizePin(ctx, actor, pin.Scope, pin.ScopeID) == nil {
			pin.Editable = true
			v.Pins = append(v.Pins, pin)
		}
	}
	return v, nil
}

// routingProblem re-renders G11 with a validation message instead of a bare
// error page, so a rejected pin or setting keeps the user on the screen.
func (m *Module) routingProblem(w http.ResponseWriter, r *http.Request, actor providerActor, status int, key string) error {
	v, err := m.loadRoutingView(r.Context(), actor)
	if err != nil {
		return err
	}
	v.Error = i18n.T(r.Context(), key)
	return m.renderRouting(w, r, status, v)
}

func (m *Module) saveRouting(w http.ResponseWriter, r *http.Request) error {
	if err := web.ParseForm(w, r); err != nil {
		return err
	}
	actor, err := browserProviderActor(r.Context())
	if err != nil || !actor.Admin {
		return core.ErrForbidden("")
	}
	mode := web.FormValue(r, "mode")
	limit := 0.0
	if web.FormValue(r, "limit_on") == "true" {
		limit, err = strconv.ParseFloat(web.FormValue(r, "limit"), 64)
		if err != nil || math.IsNaN(limit) || math.IsInf(limit, 0) || limit <= 0 {
			return m.routingProblem(w, r, actor, http.StatusUnprocessableEntity, "aiimage.routing.limit_invalid")
		}
	}
	if !validRoutingMode(mode) {
		return m.routingProblem(w, r, actor, http.StatusUnprocessableEntity, "aiimage.form.invalid")
	}
	_, err = m.d.Settings.UpdateInstance(r.Context(), actor.UserID, func(s *settings.Instance) error {
		s.RoutingMode = mode
		s.Content.LicenceGuard = web.FormValue(r, "licence_guard") == "true"
		s.UsersMayAddProviders = web.FormValue(r, "users_may_add") == "true"
		s.Limits.MonthlyRemoteSpendUSD = limit
		return s.Validate()
	})
	if err != nil {
		return m.routingProblem(w, r, actor, http.StatusUnprocessableEntity, "aiimage.routing.limit_invalid")
	}
	// Store the private-endpoint choice only when the admin changed it, so an
	// unrelated save never freezes the MV_ALLOW_PRIVATE_PROVIDER_URLS default.
	if allow := web.FormValue(r, "private_endpoints") == "true"; allow != m.private.Allowed(r.Context()) {
		if err := m.private.Set(r.Context(), allow, actor.UserID); err != nil {
			return err
		}
	}
	core.Redirect(w, r, "/models/routing?saved=1")
	return nil
}

func (m *Module) authorizePin(ctx context.Context, actor providerActor, scope, id string) error {
	if scope == "server" || scope == "panel_type" {
		if !actor.Admin {
			return errProviderForbidden
		}
		if scope == "server" && id != "" || scope == "panel_type" && (id == "" || len(id) > 80) {
			return errProviderInvalid
		}
		return nil
	}
	service, ok := core.Use[studio.Service](m.d)
	if !ok {
		return errProviderForbidden
	}
	projectID := id
	if scope == "character" {
		c, err := service.GetCharacter(ctx, id)
		if err != nil {
			return errProviderForbidden
		}
		projectID = c.ProjectID
	} else if scope != "project" {
		return errProviderInvalid
	}
	role := "creator"
	if actor.Admin {
		role = "admin"
	}
	return service.Authorize(ctx, studio.Actor{UserID: actor.UserID, SiteRole: role}, projectID, studio.ActManage)
}

func (m *Module) savePin(w http.ResponseWriter, r *http.Request) error {
	if err := web.ParseForm(w, r); err != nil {
		return err
	}
	actor, err := browserProviderActor(r.Context())
	if err != nil {
		return core.ErrForbidden("")
	}
	scope, id, task, model, provider := web.FormValue(r, "scope"), web.FormValue(r, "scope_id"), web.FormValue(r, "task"), web.FormValue(r, "model"), web.FormValue(r, "provider")
	if !imageTask(task) || !validModelID(model) {
		return m.routingProblem(w, r, actor, http.StatusUnprocessableEntity, "aiimage.form.invalid")
	}
	if err := m.authorizePin(r.Context(), actor, scope, id); err != nil {
		return m.routingProblem(w, r, actor, http.StatusForbidden, "aiimage.routing.pin_forbidden")
	}
	manifest, err := m.catalog.lookup(r.Context(), model)
	if err != nil {
		return m.routingProblem(w, r, actor, http.StatusUnprocessableEntity, "aiimage.form.invalid")
	}
	if provider != "" && provider != PlaceholderID && provider != providerKindWorker {
		p, err := m.providers.Candidate(r.Context(), actor.UserID, provider)
		if err != nil || !hasProvider(manifest, p.Kind) {
			return m.routingProblem(w, r, actor, http.StatusUnprocessableEntity, "aiimage.form.invalid")
		}
	}
	_, err = m.d.DB.Exec(r.Context(), `INSERT INTO routing_pins (id, scope_kind, scope_id, task, model_id, provider_id, created_by, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (scope_kind, scope_id, task) DO UPDATE SET model_id = excluded.model_id, provider_id = excluded.provider_id, created_by = excluded.created_by`, db.NewID(), scope, id, task, model, provider, actor.UserID, db.Now())
	if err != nil {
		return err
	}
	core.Redirect(w, r, "/models/routing?saved=1")
	return nil
}

func (m *Module) deletePin(w http.ResponseWriter, r *http.Request) error {
	if err := web.ParseForm(w, r); err != nil {
		return err
	}
	actor, err := browserProviderActor(r.Context())
	if err != nil {
		return core.ErrForbidden("")
	}
	var p routingPinView
	if err := m.d.DB.Get(r.Context(), &p, `SELECT id, scope_kind, scope_id, task, model_id, provider_id FROM routing_pins WHERE id = ?`, r.PathValue("id")); err != nil {
		return core.ErrNotFound()
	}
	if err := m.authorizePin(r.Context(), actor, p.Scope, p.ScopeID); err != nil {
		return core.ErrForbidden("")
	}
	_, err = m.d.DB.Exec(r.Context(), `DELETE FROM routing_pins WHERE id = ?`, p.ID)
	if err != nil {
		return err
	}
	core.Redirect(w, r, "/models/routing?saved=1")
	return nil
}

// recordDecision stores the router's choice for G11 "Recent decisions". One
// image job attempt makes one decision, so only its first call slot records
// it; the other slots reuse the same plan.
func (s *imageService) recordDecision(ctx context.Context, req ImageGenerateRequest) error {
	if req.CallSlot != 0 {
		return nil
	}
	candidates := []string{}
	var d imageDecision
	if json.Unmarshal(req.Plan.Decision, &d) == nil {
		for _, id := range d.Alternatives {
			if validModelID(id) {
				candidates = append(candidates, id)
			}
		}
	}
	explanation := fmt.Sprintf("%d × %d", req.Width, req.Height)
	if req.Count > 1 {
		explanation += fmt.Sprintf(" · ×%d", req.Count)
	}
	_, err := s.d.DB.Exec(ctx, `INSERT INTO routing_decisions (id, job_id, user_id, project_id, task, mode, chosen_model_id, chosen_provider_id, candidates, explanation, overridden, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, db.NewID(), db.NullString(req.JobID), req.ActorID, req.ProjectID, req.Plan.Task, req.Plan.RoutingMode, req.Plan.ModelID, req.Plan.ProviderID, db.MustJSON(candidates), explanation, req.Plan.PinModelID != "" || req.Plan.PinProviderID != "", db.Now())
	return err
}
