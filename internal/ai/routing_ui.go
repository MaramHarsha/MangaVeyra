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
	Limit                            float64
	Decisions                        []routingDecisionView
	Pins                             []routingPinView
	Models                           []ModelManifest
	Notice                           string
}
type routingDecisionView struct {
	Task        string `db:"task"`
	Model       string `db:"chosen_model_id"`
	Explanation string `db:"explanation"`
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

func (m *Module) routingPage(w http.ResponseWriter, r *http.Request) error {
	actor, err := browserProviderActor(r.Context())
	if err != nil {
		return core.ErrForbidden("")
	}
	inst, err := m.image.readSettings(r.Context())
	if err != nil {
		return err
	}
	v := routingView{Mode: inst.RoutingMode, Admin: actor.Admin, LicenceGuard: inst.Content.LicenceGuard, UsersMayAdd: inst.UsersMayAddProviders, Limit: inst.Limits.MonthlyRemoteSpendUSD, Notice: r.URL.Query().Get("saved")}
	v.Models, err = m.catalog.list(r.Context())
	if err != nil {
		return err
	}
	if err := m.d.DB.Select(r.Context(), &v.Decisions, `SELECT task, chosen_model_id, explanation FROM routing_decisions WHERE user_id = ? ORDER BY id DESC LIMIT 20`, actor.UserID); err != nil {
		return err
	}
	var pins []routingPinView
	if err := m.d.DB.Select(r.Context(), &pins, `SELECT id, scope_kind, scope_id, task, model_id, provider_id FROM routing_pins ORDER BY id DESC LIMIT 201`); err != nil {
		return err
	}
	for _, pin := range pins {
		if pin.Scope == "server" || pin.Scope == "panel_type" {
			pin.Editable = actor.Admin
			v.Pins = append(v.Pins, pin)
		} else if m.authorizePin(r.Context(), actor, pin.Scope, pin.ScopeID) == nil {
			pin.Editable = true
			v.Pins = append(v.Pins, pin)
		}
	}
	return m.ui.Page(w, r, http.StatusOK, web.PageOpts{Title: i18n.T(r.Context(), "aiimage.routing.title"), BackURL: "/models", Section: layout.SectionStudio, Nav: layout.NavModels}, RoutingPage(v))
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
	limit, err := strconv.ParseFloat(web.FormValue(r, "limit"), 64)
	if !validRoutingMode(mode) || err != nil || math.IsNaN(limit) || math.IsInf(limit, 0) || limit < 0 {
		return core.ErrBadRequest(i18n.T(r.Context(), "aiimage.form.invalid"))
	}
	_, err = m.d.Settings.UpdateInstance(r.Context(), actor.UserID, func(s *settings.Instance) error {
		s.RoutingMode = mode
		s.Content.LicenceGuard = web.FormValue(r, "licence_guard") == "true"
		s.UsersMayAddProviders = web.FormValue(r, "users_may_add") == "true"
		s.Limits.MonthlyRemoteSpendUSD = limit
		return s.Validate()
	})
	if err != nil {
		return core.ErrBadRequest(i18n.T(r.Context(), "aiimage.form.invalid"))
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
		return core.ErrBadRequest(i18n.T(r.Context(), "aiimage.form.invalid"))
	}
	if err := m.authorizePin(r.Context(), actor, scope, id); err != nil {
		return core.ErrForbidden("")
	}
	manifest, err := m.catalog.lookup(r.Context(), model)
	if err != nil {
		return core.ErrBadRequest(i18n.T(r.Context(), "aiimage.form.invalid"))
	}
	if provider != "" && provider != PlaceholderID && provider != providerKindWorker {
		p, err := m.providers.Candidate(r.Context(), actor.UserID, provider)
		if err != nil || !hasProvider(manifest, p.Kind) {
			return core.ErrBadRequest(i18n.T(r.Context(), "aiimage.form.invalid"))
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

func (s *imageService) recordDecision(ctx context.Context, req ImageGenerateRequest) error {
	candidates := []string{}
	var d imageDecision
	if json.Unmarshal(req.Plan.Decision, &d) == nil {
		for _, id := range d.Alternatives {
			if validModelID(id) {
				candidates = append(candidates, id)
			}
		}
	}
	explanation := fmt.Sprintf("%s · %s · %d × %d · %d", req.Plan.RoutingMode, req.Plan.ModelID, req.Width, req.Height, req.Count)
	_, err := s.d.DB.Exec(ctx, `INSERT INTO routing_decisions (id, job_id, user_id, project_id, task, mode, chosen_model_id, chosen_provider_id, candidates, explanation, overridden, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, db.NewID(), req.JobID, req.ActorID, req.ProjectID, req.Plan.Task, req.Plan.RoutingMode, req.Plan.ModelID, req.Plan.ProviderID, db.MustJSON(candidates), explanation, req.Plan.PinModelID != "" || req.Plan.PinProviderID != "", db.Now())
	return err
}
