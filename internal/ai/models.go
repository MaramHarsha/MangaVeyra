package ai

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/setup"
	"github.com/MaramHarsha/MangaVeyra/internal/web"
	"github.com/MaramHarsha/MangaVeyra/internal/web/layout"
	"github.com/MaramHarsha/MangaVeyra/internal/web/ui"
)

const providerTestTimeout = 15 * time.Second

var _ setup.ModelStep = (*Module)(nil)

type modelsPageView struct {
	Providers []providerView
	CanAdd    bool
	LocalOnly bool
}

type providerFormView struct {
	Kind      string
	Name      string
	Scope     string
	Next      string
	Admin     bool
	LocalOnly bool
	Errors    web.FormErrors
}

type providerCardView struct {
	Provider  providerView
	LocalOnly bool
	Tone      ui.Tone
	Notice    string
}

func browserProviderActor(ctx context.Context) (providerActor, error) {
	u := core.UserFrom(ctx)
	if u == nil || strings.TrimSpace(u.ID) == "" {
		return providerActor{}, errProviderForbidden
	}
	return providerActor{UserID: u.ID, Admin: u.IsAdmin()}, nil
}

func (m *Module) modelsPage(w http.ResponseWriter, r *http.Request) error {
	if m.ui == nil {
		return core.ErrInternal(errors.New("ai: web UI unavailable"))
	}
	actor, err := browserProviderActor(r.Context())
	if err != nil {
		return core.ErrForbidden(i18n.T(r.Context(), "models.error.session"))
	}
	providers, err := m.providers.ListManageable(r.Context(), actor)
	if err != nil {
		return err
	}
	inst, err := loadRemoteSettings(r.Context(), m.d.Settings)
	if err != nil {
		return err
	}
	v := modelsPageView{Providers: providers, LocalOnly: inst.LocalOnly,
		CanAdd: actor.Admin || inst.UsersMayAddProviders}
	return m.ui.Page(w, r, http.StatusOK, web.PageOpts{Title: i18n.T(r.Context(), "models.title"),
		Section: layout.SectionStudio, Nav: layout.NavModels}, ModelsPage(v))
}

func (m *Module) newProviderPage(w http.ResponseWriter, r *http.Request) error {
	if m.ui == nil {
		return core.ErrInternal(errors.New("ai: web UI unavailable"))
	}
	kind := r.URL.Query().Get("kind")
	if !validProviderKind(kind) {
		return core.ErrNotFound()
	}
	actor, err := browserProviderActor(r.Context())
	if err != nil {
		return core.ErrForbidden(i18n.T(r.Context(), "models.error.session"))
	}
	inst, err := loadRemoteSettings(r.Context(), m.d.Settings)
	if err != nil {
		return err
	}
	if !actor.Admin && !inst.UsersMayAddProviders {
		return core.ErrForbidden(i18n.T(r.Context(), "models.error.personal_disabled"))
	}
	scope := providerScopeUser
	if actor.Admin {
		scope = providerScopeServer
	}
	v := providerFormView{Kind: kind, Name: providerKindName(kind), Scope: scope,
		Next: core.SafeRedirectTarget(r.URL.Query().Get("next"), "/models"), Admin: actor.Admin,
		LocalOnly: inst.LocalOnly, Errors: web.FormErrors{}}
	return m.renderProviderForm(w, r, http.StatusOK, v)
}

func (m *Module) createProvider(w http.ResponseWriter, r *http.Request) error {
	if err := web.ParseForm(w, r); err != nil {
		return core.ErrBadRequest("invalid form")
	}
	ctx := r.Context()
	actor, err := browserProviderActor(ctx)
	if err != nil {
		return core.ErrForbidden(i18n.T(ctx, "models.error.session"))
	}
	inst, err := loadRemoteSettings(ctx, m.d.Settings)
	if err != nil {
		return err
	}
	if !actor.Admin && !inst.UsersMayAddProviders {
		return core.ErrForbidden(i18n.T(ctx, "models.error.personal_disabled"))
	}
	scope := web.FormValue(r, "scope")
	if !actor.Admin && scope != providerScopeUser {
		return core.ErrForbidden(i18n.T(ctx, "models.error.scope"))
	}
	kind := web.FormValue(r, "kind")
	if kind == "" {
		kind = providerKindOpenRouter
	}
	if !validProviderKind(kind) {
		return core.ErrNotFound()
	}
	v := providerFormView{Kind: kind, Name: web.FormValue(r, "name"), Scope: scope,
		Next: core.SafeRedirectTarget(web.FormValue(r, "next"), "/models"), Admin: actor.Admin,
		LocalOnly: inst.LocalOnly, Errors: web.FormErrors{}}
	key := strings.TrimSpace(r.PostFormValue("key"))
	if !validProviderName(v.Name) {
		v.Errors.Add("name", i18n.T(ctx, "models.form.name_invalid"))
	}
	if key == "" || len(key) > maxProviderKeyBytes {
		v.Errors.Add("key", i18n.T(ctx, "models.form.key_invalid"))
	}
	if v.Scope != providerScopeServer && v.Scope != providerScopeUser {
		v.Errors.Add("scope", i18n.T(ctx, "models.form.scope_invalid"))
	}
	if v.Errors.Has() {
		return m.renderProviderForm(w, r, http.StatusUnprocessableEntity, v)
	}
	if _, err := m.providers.Create(ctx, actor, providerCreate{Kind: kind, Name: v.Name, Key: key, Scope: v.Scope}); err != nil {
		switch {
		case errors.Is(err, errProviderInvalid):
			v.Errors.Add("", i18n.T(ctx, "models.error.invalid"))
			return m.renderProviderForm(w, r, http.StatusUnprocessableEntity, v)
		case errors.Is(err, errProviderForbidden):
			return core.ErrForbidden(i18n.T(ctx, "models.error.scope"))
		default:
			return err
		}
	}
	web.Flash(w, r, "success", i18n.T(ctx, "models.flash.connected"), "")
	core.Redirect(w, r, v.Next)
	return nil
}

func (m *Module) renderProviderForm(w http.ResponseWriter, r *http.Request, status int, v providerFormView) error {
	return m.ui.Page(w, r, status, web.PageOpts{Title: i18n.T(r.Context(), "models."+v.Kind+".connect"),
		BackURL: "/models", Section: layout.SectionStudio, Nav: layout.NavModels}, ProviderFormPage(v))
}

func (m *Module) testProvider(w http.ResponseWriter, r *http.Request) error {
	if err := web.ParseForm(w, r); err != nil {
		return core.ErrBadRequest("invalid form")
	}
	ctx := r.Context()
	actor, id, err := m.providerMutationTarget(ctx, r.PathValue("id"))
	if err != nil {
		return err
	}
	testCtx, cancel := context.WithTimeout(ctx, providerTestTimeout)
	view, testErr := m.providerAPI.TestConnection(testCtx, actor, id)
	cancel()
	if testErr != nil && view.ID == "" {
		view, err = m.providers.GetManageable(ctx, actor, id)
		if err != nil {
			return m.providerPageError(err)
		}
	}
	inst, settingsErr := loadRemoteSettings(ctx, m.d.Settings)
	if settingsErr != nil {
		return settingsErr
	}
	card := providerCardView{Provider: view, LocalOnly: inst.LocalOnly}
	if testErr != nil {
		card.Tone, card.Notice = ui.ToneError, i18n.T(ctx, "models.test.failed")
	} else {
		card.Tone, card.Notice = ui.ToneSuccess, i18n.T(ctx, "models.test.ok")
	}
	if core.IsHTMX(r) {
		return web.Render(w, r, http.StatusOK, ProviderCard(card), nil)
	}
	kind := "success"
	if testErr != nil {
		kind = "error"
	}
	web.Flash(w, r, kind, card.Notice, "")
	core.Redirect(w, r, "/models")
	return nil
}

func (m *Module) setProviderEnabled(w http.ResponseWriter, r *http.Request) error {
	if err := web.ParseForm(w, r); err != nil {
		return core.ErrBadRequest("invalid form")
	}
	ctx := r.Context()
	actor, id, err := m.providerMutationTarget(ctx, r.PathValue("id"))
	if err != nil {
		return err
	}
	var enabled bool
	switch web.FormValue(r, "enabled") {
	case "true":
		enabled = true
	case "false":
		enabled = false
	default:
		return core.ErrBadRequest("invalid enabled state")
	}
	view, err := m.providers.SetEnabled(ctx, actor, id, enabled)
	if err != nil {
		return m.providerPageError(err)
	}
	inst, err := loadRemoteSettings(ctx, m.d.Settings)
	if err != nil {
		return err
	}
	message := i18n.T(ctx, "models.flash.disabled")
	if enabled {
		message = i18n.T(ctx, "models.flash.enabled")
	}
	if core.IsHTMX(r) {
		return web.Render(w, r, http.StatusOK, ProviderCard(providerCardView{Provider: view, LocalOnly: inst.LocalOnly,
			Tone: ui.ToneSuccess, Notice: message}), nil)
	}
	web.Flash(w, r, "success", message, "")
	core.Redirect(w, r, "/models")
	return nil
}

func (m *Module) replaceProviderKey(w http.ResponseWriter, r *http.Request) error {
	if err := web.ParseForm(w, r); err != nil {
		return core.ErrBadRequest("invalid form")
	}
	ctx := r.Context()
	actor, id, err := m.providerMutationTarget(ctx, r.PathValue("id"))
	if err != nil {
		return err
	}
	key := strings.TrimSpace(r.PostFormValue("key"))
	inst, err := loadRemoteSettings(ctx, m.d.Settings)
	if err != nil {
		return err
	}
	if key == "" || len(key) > maxProviderKeyBytes {
		view, getErr := m.providers.GetManageable(ctx, actor, id)
		if getErr != nil {
			return m.providerPageError(getErr)
		}
		card := providerCardView{Provider: view, LocalOnly: inst.LocalOnly, Tone: ui.ToneError,
			Notice: i18n.T(ctx, "models.form.key_invalid")}
		if core.IsHTMX(r) {
			return web.Render(w, r, http.StatusUnprocessableEntity, ProviderCard(card), nil)
		}
		web.Flash(w, r, "error", card.Notice, "")
		core.Redirect(w, r, "/models")
		return nil
	}
	view, err := m.providers.ReplaceKey(ctx, actor, id, key)
	if err != nil {
		return m.providerPageError(err)
	}
	message := i18n.T(ctx, "models.flash.key_replaced")
	if core.IsHTMX(r) {
		return web.Render(w, r, http.StatusOK, ProviderCard(providerCardView{Provider: view, LocalOnly: inst.LocalOnly,
			Tone: ui.ToneSuccess, Notice: message}), nil)
	}
	web.Flash(w, r, "success", message, "")
	core.Redirect(w, r, "/models")
	return nil
}

func (m *Module) providerMutationTarget(ctx context.Context, id string) (providerActor, string, error) {
	actor, err := browserProviderActor(ctx)
	if err != nil {
		return providerActor{}, "", core.ErrForbidden(i18n.T(ctx, "models.error.session"))
	}
	id = strings.TrimSpace(id)
	if !db.ValidID(id) {
		return providerActor{}, "", core.ErrNotFound()
	}
	if _, err := m.providers.GetManageable(ctx, actor, id); err != nil {
		return providerActor{}, "", m.providerPageError(err)
	}
	return actor, id, nil
}

func (m *Module) providerPageError(err error) error {
	if errors.Is(err, errProviderNotFound) || errors.Is(err, errProviderForbidden) {
		return core.ErrNotFound()
	}
	if errors.Is(err, errProviderInvalid) || errors.Is(err, errProviderUnavailable) {
		return core.ErrBadRequest("provider unavailable")
	}
	return err
}

// Providers implements setup.ModelStep without making a network call. It
// returns one card per remote route kind with that kind's connection status.
func (m *Module) Providers(ctx context.Context) ([]setup.ProviderCard, error) {
	cards := make([]setup.ProviderCard, 0, len(remoteRoutes))
	for _, route := range remoteRoutes {
		cards = append(cards, setup.ProviderCard{Kind: route.Kind, NameKey: "setup.provider." + route.Kind + ".name",
			DescKey: "setup.provider." + route.Kind + ".desc", Icon: providerKindIcon(route.Kind),
			Href: "/models/providers/new?kind=" + route.Kind + "&next=/setup/models"})
	}
	actor, err := browserProviderActor(ctx)
	if err != nil {
		for i := range cards {
			cards[i].Status = setup.ProviderError
		}
		return cards, nil
	}
	providers, err := m.providers.ListManageable(ctx, actor)
	if err != nil {
		if m.d.Log != nil {
			m.d.Log.WarnContext(ctx, "list providers for setup", "err", err)
		}
		for i := range cards {
			cards[i].Status = setup.ProviderError
		}
		return cards, nil
	}
	for i := range cards {
		cards[i].Status = providerKindStatus(providers, cards[i].Kind)
	}
	return cards, nil
}

// providerKindStatus summarises the enabled connections of one kind exactly
// as the single-kind card did: a verified connection wins, a failed check
// marks the card as an error, and an unchecked connection counts as connected
// only while nothing failed.
func providerKindStatus(providers []providerView, kind string) string {
	status := setup.ProviderNotConnected
	for _, provider := range providers {
		if provider.Kind != kind || !provider.Enabled {
			continue
		}
		switch provider.LastTest.State {
		case "ok":
			return setup.ProviderConnected
		case "error":
			status = setup.ProviderError
		default:
			if status == setup.ProviderNotConnected {
				status = setup.ProviderConnected
			}
		}
	}
	return status
}

func providerKindName(kind string) string {
	if route, ok := routeForKind(kind); ok {
		return route.Label
	}
	return kind
}

func providerKindIcon(kind string) string {
	if kind == providerKindOpenAI {
		return "brand-openai"
	}
	return "route"
}

func providerKindModel(kind string) string {
	if route, ok := routeForKind(kind); ok {
		return route.Model
	}
	return ""
}

// Recommend implements setup.ModelStep using the setup package's deterministic
// hardware recommendation. It performs no network I/O.
func (m *Module) Recommend(_ context.Context, gpus []jobs.GPU) (*setup.Recommendation, error) {
	return setup.RecommendFor(gpus), nil
}
