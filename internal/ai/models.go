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
	if r.URL.Query().Get("kind") != providerKindOpenRouter {
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
	v := providerFormView{Name: "OpenRouter", Scope: scope,
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
	v := providerFormView{Name: web.FormValue(r, "name"), Scope: scope,
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
	if _, err := m.providers.Create(ctx, actor, providerCreate{Name: v.Name, Key: key, Scope: v.Scope}); err != nil {
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
	return m.ui.Page(w, r, status, web.PageOpts{Title: i18n.T(r.Context(), "models.form.title"),
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

// Providers implements setup.ModelStep without making a network call.
func (m *Module) Providers(ctx context.Context) ([]setup.ProviderCard, error) {
	card := setup.ProviderCard{Kind: providerKindOpenRouter, NameKey: "setup.provider.openrouter.name",
		DescKey: "setup.provider.openrouter.desc", Icon: "route",
		Href: "/models/providers/new?kind=openrouter&next=/setup/models"}
	actor, err := browserProviderActor(ctx)
	if err != nil {
		card.Status = setup.ProviderError
		return []setup.ProviderCard{card}, nil
	}
	providers, err := m.providers.ListManageable(ctx, actor)
	if err != nil {
		if m.d.Log != nil {
			m.d.Log.WarnContext(ctx, "list providers for setup", "err", err)
		}
		card.Status = setup.ProviderError
		return []setup.ProviderCard{card}, nil
	}
	for _, provider := range providers {
		if !provider.Enabled {
			continue
		}
		if provider.LastTest.State == "ok" {
			card.Status = setup.ProviderConnected
			return []setup.ProviderCard{card}, nil
		}
		if provider.LastTest.State == "error" {
			card.Status = setup.ProviderError
			continue
		}
		if card.Status == setup.ProviderNotConnected {
			card.Status = setup.ProviderConnected
		}
	}
	return []setup.ProviderCard{card}, nil
}

// Recommend implements setup.ModelStep using the setup package's deterministic
// hardware recommendation. It performs no network I/O.
func (m *Module) Recommend(_ context.Context, gpus []jobs.GPU) (*setup.Recommendation, error) {
	return setup.RecommendFor(gpus), nil
}
