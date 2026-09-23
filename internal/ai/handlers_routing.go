package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/ai/catalog"
	"github.com/MaramHarsha/MangaVeyra/internal/ai/router"
	"github.com/MaramHarsha/MangaVeyra/internal/ai/views"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
	"github.com/MaramHarsha/MangaVeyra/internal/web"
	"github.com/MaramHarsha/MangaVeyra/internal/web/ui"
)

// DecisionRecord is a stored routing decision.
type DecisionRecord struct {
	ID               string    `db:"id" json:"id"`
	JobID            *string   `db:"job_id" json:"job_id,omitempty"`
	UserID           *string   `db:"user_id" json:"-"`
	ProjectID        *string   `db:"project_id" json:"project_id,omitempty"`
	Task             string    `db:"task" json:"task"`
	Mode             string    `db:"mode" json:"mode"`
	ChosenModelID    string    `db:"chosen_model_id" json:"chosen_model_id"`
	ChosenProviderID string    `db:"chosen_provider_id" json:"chosen_provider_id"`
	Candidates       string    `db:"candidates" json:"-"`
	Overridden       bool      `db:"overridden" json:"overridden"`
	Label            string    `db:"label" json:"label"`
	Why              string    `db:"why" json:"-"`
	Status           string    `db:"status" json:"status"`
	CreatedAt        time.Time `db:"created_at" json:"created_at"`
}

// Decisions lists recent decisions (all for admins, the member's own otherwise).
func (s *Service) Decisions(ctx context.Context, u *core.User, limit int) ([]DecisionRecord, error) {
	q, args := `SELECT id, job_id, user_id, project_id, task, mode, chosen_model_id, chosen_provider_id, candidates, overridden, label, why, status, created_at
		FROM routing_decisions`, []any{}
	if !u.IsAdmin() {
		q += ` WHERE user_id = ?`
		args = append(args, u.ID)
	}
	q += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	var out []DecisionRecord
	err := s.d.DB.Select(ctx, &out, q, append(args, limit)...)
	return out, err
}

type storedCandidates struct {
	Chain   []router.Candidate `json:"chain"`
	Skipped []router.Candidate `json:"skipped"`
	Warn    []router.Reason    `json:"warnings"`
}

// decisionRow translates a stored decision for G11.
func (m *Module) decisionRow(ctx context.Context, d DecisionRecord, now time.Time) views.DecisionRow {
	var cands storedCandidates
	_ = json.Unmarshal([]byte(d.Candidates), &cands)
	var why []router.Reason
	_ = json.Unmarshal([]byte(d.Why), &why)
	row := views.DecisionRow{Status: d.Status, When: whenText(ctx, d.CreatedAt, now)}
	row.Context = d.Label
	if row.Context == "" {
		row.Context = i18n.T(ctx, "ai.task."+d.Task)
	}
	var pick *router.Candidate
	for i := range cands.Chain {
		c := &cands.Chain[i]
		if c.ModelID == d.ChosenModelID && c.ProviderID == d.ChosenProviderID {
			pick = c
		}
	}
	if pick == nil && len(cands.Chain) > 0 {
		pick = &cands.Chain[0]
	}
	if pick != nil {
		where := pick.ProviderName
		if pick.ProviderKind == "local_worker" {
			where = i18n.T(ctx, "ai.chip.local")
		}
		row.Pick = pick.ModelName + " · " + where
		if pick.Placeholder {
			row.Pick = i18n.T(ctx, "ai.chip.placeholder")
		}
		if len(cands.Chain) > 0 && (cands.Chain[0].ModelID != pick.ModelID || cands.Chain[0].ProviderID != pick.ProviderID) {
			why = append(why, router.Reason{Code: router.RFallback, Args: map[string]any{"model": cands.Chain[0].ModelName}})
		}
	} else {
		row.Pick = i18n.T(ctx, "ai.chip.none")
	}
	var parts []string
	for _, r := range why {
		parts = append(parts, ReasonText(ctx, r))
	}
	row.Why = sentence(strings.Join(parts, i18n.T(ctx, "ai.list_sep")))
	for _, w := range cands.Warn {
		row.Warnings = append(row.Warnings, ReasonText(ctx, w))
	}
	for i, c := range cands.Chain {
		if i == 0 || c.Placeholder || len(row.Alts) >= 3 {
			continue
		}
		row.Alts = append(row.Alts, PickLabel(ctx, c))
	}
	for _, c := range cands.Skipped {
		if len(c.Reasons) == 0 || c.Reasons[0].Code == router.RLowerScore || len(row.Skipped) >= 8 {
			continue
		}
		var rs []string
		for _, r := range c.Reasons {
			rs = append(rs, ReasonText(ctx, r))
		}
		row.Skipped = append(row.Skipped, i18n.T(ctx, "ai.decision.skip_line", "model", c.ModelName, "why", strings.Join(rs, ", ")))
	}
	return row
}

// pinText describes a pin scope ("Covers", "Series “Glass Orchard”", "Character Lock jobs").
func pinText(ctx context.Context, p router.Pin) string {
	name := p.Label
	if name == "" {
		name = p.ScopeID
	}
	var s string
	switch p.ScopeKind {
	case router.PinServer:
		s = i18n.T(ctx, "ai.pin.text.server")
	case router.PinPanelType:
		key := "ai.panel_type." + p.ScopeID
		if t := i18n.T(ctx, key); t != key {
			s = t
		} else {
			s = i18n.T(ctx, "ai.pin.text.panel_type", "name", name)
		}
	case router.PinJobType:
		key := "ai.jobtype." + p.ScopeID
		if t := i18n.T(ctx, key); t != key {
			s = t
		} else {
			s = i18n.T(ctx, "ai.pin.text.job_type", "name", name)
		}
	default:
		s = i18n.T(ctx, "ai.pin.text."+p.ScopeKind, "name", name)
	}
	if p.Task != "" && p.ScopeKind != router.PinJobType {
		s += " · " + i18n.T(ctx, "ai.task."+p.Task)
	}
	return s
}

func (m *Module) routingView(ctx context.Context, u *core.User, errs map[string]string) (views.RoutingView, error) {
	s := m.svc
	inst := s.d.Settings.MustInstance(ctx)
	v := views.RoutingView{IsAdmin: u.IsAdmin(), LicenceGuard: inst.Content.LicenceGuard, UsersMayAdd: inst.UsersMayAddProviders, Errors: errs}
	g := s.Guards(ctx)
	v.Learn = g.Learn
	if u.IsAdmin() {
		v.Mode, v.ModeAction = inst.RoutingMode, "/models/routing/mode"
		if inst.LocalOnly {
			v.Mode = router.ModeLocalOnly
			v.ModeNote = i18n.T(ctx, "ai.routing.note_local_only")
		}
	} else {
		v.ModeAction = "/models/routing/mode"
		v.Mode = s.UserMode(ctx, u.ID)
		if v.Mode == "" {
			v.Mode = inst.RoutingMode
		}
		v.ModeNote = i18n.T(ctx, "ai.routing.note_member", "mode", i18n.T(ctx, "ai.mode."+inst.RoutingMode))
	}
	limit := inst.Limits.ServerMonthlyRemoteSpendUSD
	v.LimitOn = limit > 0
	if limit <= 0 {
		limit = g.LastLimitUSD
		if limit <= 0 {
			limit = 10
		}
	}
	v.LimitUSD = strconv.FormatFloat(limit, 'f', -1, 64)
	v.UserLimitUSD = strconv.FormatFloat(inst.Limits.MonthlyRemoteSpendUSD, 'f', -1, 64)
	spent, _ := s.MonthSpend(ctx, "")
	mine, _ := s.MonthSpend(ctx, u.ID)
	v.SpentLine = i18n.T(ctx, "ai.guard.spent", "server", USD(spent), "mine", USD(mine))
	recs, err := s.Decisions(ctx, u, 25)
	if err != nil {
		return v, err
	}
	now := s.Now()
	for _, d := range recs {
		v.Decisions = append(v.Decisions, m.decisionRow(ctx, d, now))
	}
	pins, err := s.Pins(ctx)
	if err != nil {
		return v, err
	}
	c := s.Catalog(ctx)
	_, hasAuth := core.Use[ScopeAuthorizer](s.d)
	for _, p := range pins {
		target := i18n.T(ctx, "ai.mode."+p.Mode)
		if p.Mode == "" {
			target = p.ModelID
			if man, ok := c.Get(p.ModelID); ok {
				target = man.Name
			}
		}
		v.Pins = append(v.Pins, views.PinRow{ID: p.ID, Scope: pinText(ctx, p), Target: target, CanDelete: u.IsAdmin()})
	}
	v.CanPin = u.IsAdmin() || hasAuth
	for _, k := range router.PinScopes {
		if !u.IsAdmin() && k != router.PinProject && k != router.PinSeries && k != router.PinCharacter {
			continue
		}
		v.PinScopes = append(v.PinScopes, ui.Option{Value: k, Label: i18n.T(ctx, "ai.pin.scope."+k)})
	}
	v.PinTasks = append(v.PinTasks, ui.Option{Value: "", Label: i18n.T(ctx, "ai.pin.any_task")})
	for _, t := range catalog.Tasks {
		v.PinTasks = append(v.PinTasks, ui.Option{Value: t, Label: i18n.T(ctx, "ai.task."+t)})
	}
	v.PinModels = append(v.PinModels, ui.Option{Value: "", Label: i18n.T(ctx, "ai.pin.no_model")})
	for _, man := range c.All() {
		if man.Source == catalog.SourceWorker {
			continue
		}
		v.PinModels = append(v.PinModels, ui.Option{Value: man.ID, Label: man.Name + " · " + i18n.T(ctx, "ai.kindtab."+man.Kind)})
	}
	v.PinModes = append(v.PinModes, ui.Option{Value: "", Label: i18n.T(ctx, "ai.pin.no_mode")})
	for _, md := range router.Modes {
		v.PinModes = append(v.PinModes, ui.Option{Value: md, Label: i18n.T(ctx, "ai.mode."+md)})
	}
	return v, nil
}

func (m *Module) routingPage(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	v, err := m.routingView(ctx, core.UserFrom(ctx), nil)
	if err != nil {
		return err
	}
	return m.page(w, r, http.StatusOK, i18n.T(ctx, "ai.routing.title"), i18n.T(ctx, "ai.routing.title"), "/models", views.RoutingPage(v))
}

func (m *Module) setMode(w http.ResponseWriter, r *http.Request) error {
	if err := web.ParseForm(w, r); err != nil {
		return core.ErrBadRequest("invalid form")
	}
	ctx := r.Context()
	u := core.UserFrom(ctx)
	mode := r.PostFormValue("mode")
	if !router.ValidMode(mode) {
		return core.ErrValidation(map[string]string{"mode": i18n.T(ctx, "ai.error.mode")})
	}
	var err error
	if u.IsAdmin() {
		err = m.svc.SetInstanceMode(ctx, u.ID, mode)
	} else {
		err = m.svc.SetUserMode(ctx, u.ID, mode)
	}
	if err != nil {
		return err
	}
	return m.done(w, r, i18n.T(ctx, "ai.routing.mode_saved", "mode", i18n.T(ctx, "ai.mode."+mode)), "/models/routing")
}

func parseUSD(s string) (float64, bool) {
	s = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "$"))
	if s == "" {
		return 0, true
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v < 0 || v > 1e6 {
		return 0, false
	}
	return v, true
}

func (m *Module) saveGuards(w http.ResponseWriter, r *http.Request) error {
	if err := web.ParseForm(w, r); err != nil {
		return core.ErrBadRequest("invalid form")
	}
	ctx := r.Context()
	u := core.UserFrom(ctx)
	limit, ok := parseUSD(r.PostFormValue("limit_usd"))
	userLimit, ok2 := parseUSD(r.PostFormValue("user_limit_usd"))
	if !ok || !ok2 {
		if core.IsHTMX(r) {
			web.Toast(w, "error", i18n.T(ctx, "ai.error.amount"), "")
			w.WriteHeader(http.StatusUnprocessableEntity)
			return nil
		}
		errs := map[string]string{"limit_usd": i18n.T(ctx, "ai.error.amount")}
		v, err := m.routingView(ctx, u, errs)
		if err != nil {
			return err
		}
		return m.page(w, r, http.StatusUnprocessableEntity, i18n.T(ctx, "ai.routing.title"), i18n.T(ctx, "ai.routing.title"), "/models", views.RoutingPage(v))
	}
	limitOn := web.FormBool(r, "limit_on")
	g := m.svc.Guards(ctx)
	g.Learn = web.FormBool(r, "learn")
	if limit > 0 {
		g.LastLimitUSD = limit
	}
	if limitOn && limit == 0 {
		limit = g.LastLimitUSD
		if limit == 0 {
			limit = 10
		}
	}
	if err := m.svc.SaveGuards(ctx, u.ID, g); err != nil {
		return err
	}
	_, err := m.d.Settings.UpdateInstance(ctx, u.ID, func(i *settings.Instance) error {
		i.Content.LicenceGuard = web.FormBool(r, "licence_guard")
		i.UsersMayAddProviders = web.FormBool(r, "users_may_add")
		if limitOn {
			i.Limits.ServerMonthlyRemoteSpendUSD = limit
		} else {
			i.Limits.ServerMonthlyRemoteSpendUSD = 0
		}
		if r.PostForm.Has("user_limit_usd") {
			i.Limits.MonthlyRemoteSpendUSD = userLimit
		}
		return nil
	})
	if err != nil {
		return err
	}
	return m.done(w, r, i18n.T(ctx, "ai.saved"), "/models/routing#guards")
}

func (m *Module) addPin(w http.ResponseWriter, r *http.Request) error {
	if err := web.ParseForm(w, r); err != nil {
		return core.ErrBadRequest("invalid form")
	}
	ctx := r.Context()
	u := core.UserFrom(ctx)
	p := router.Pin{ScopeKind: r.PostFormValue("scope_kind"), ScopeID: strings.TrimSpace(r.PostFormValue("scope_id")), Task: r.PostFormValue("task"),
		ModelID: r.PostFormValue("model_id"), Mode: r.PostFormValue("mode"), Label: r.PostFormValue("label")}
	if p.Mode != "" && p.ModelID != "" {
		p.ModelID = ""
	}
	if _, err := m.svc.AddPin(ctx, u, p); err != nil {
		var fe *FieldError
		if errors.As(err, &fe) {
			v, verr := m.routingView(ctx, u, map[string]string{fe.Field: i18n.T(ctx, fe.Key)})
			if verr != nil {
				return verr
			}
			return m.page(w, r, http.StatusUnprocessableEntity, i18n.T(ctx, "ai.routing.title"), i18n.T(ctx, "ai.routing.title"), "/models", views.RoutingPage(v))
		}
		return err
	}
	return m.done(w, r, i18n.T(ctx, "ai.routing.pinned"), "/models/routing")
}

func (m *Module) deletePin(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	if err := m.svc.DeletePin(ctx, core.UserFrom(ctx), r.PathValue("id")); err != nil {
		return err
	}
	return m.done(w, r, i18n.T(ctx, "ai.routing.unpinned"), "/models/routing")
}

var _ = fmt.Sprint
