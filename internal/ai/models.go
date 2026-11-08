package ai

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
	"github.com/MaramHarsha/MangaVeyra/internal/setup"
	"github.com/MaramHarsha/MangaVeyra/internal/web"
	"github.com/MaramHarsha/MangaVeyra/internal/web/layout"
	"github.com/MaramHarsha/MangaVeyra/internal/web/ui"
)

const providerTestTimeout = 15 * time.Second

var _ setup.ModelStep = (*Module)(nil)

// Designed destinations that are not built yet. They are distinct paths that
// no route serves, so ui.Ready renders every control that leads to them as a
// disabled "Soon" item (ADR 0004 §7). Ready checks paths, not queries.
const (
	modelsCustomHref  = "/models/providers/custom"
	falConnectHref    = "/models/providers/new/fal"
	googleConnectHref = "/models/providers/new/google"
	xaiConnectHref    = "/models/providers/new/xai"
	ollamaConnectHref = "/models/providers/new/ollama"
)

// Capability tabs of G2, in design order. Image and Text are populated.
const (
	capImage = "image"
	capText  = "text"
	capVideo = "video"
	capVoice = "voice"
	capOCR   = "ocr"
)

var modelCapabilities = []string{capImage, capText, capVideo, capVoice, capOCR}

// Summary tile states.
const (
	tileOn    = "on"
	tileOff   = "off"
	tileError = "error"
)

type modelsPageView struct {
	// Server is the host shown in the subtitle (the sidebar's server name).
	Server    string
	CanAdd    bool
	LocalOnly bool
	// Cap is the selected capability tab.
	Cap       string
	Tiles     []modelsTileView
	Providers []providerView
	Images    []imageModelView
}

// capCount is the number of model rows behind one capability tab: the
// built-in placeholder plus every manageable connection for text, none yet
// for the other capabilities.
func (v modelsPageView) capCount(capability string) int {
	if capability == capText {
		count := 1
		for _, p := range v.Providers {
			if p.Kind != providerKindFal {
				count++
			}
		}
		return count
	}
	if capability == capImage {
		return len(v.Images)
	}
	return 0
}

// modelsTileView is one summary tile. A tile with an Href is a designed
// provider that is not built: it renders as Soon while Href is not served.
type modelsTileView struct {
	Icon   string
	Name   string
	Detail string
	State  string
	Href   string
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

// providerRowView is one connection's rows in the model table. Notice is the
// result of the action that produced this fragment (test, toggle, key).
type providerRowView struct {
	Provider  providerView
	LocalOnly bool
	Tone      ui.Tone
	Notice    string
	// Tested marks the fragment returned by Test connection, whose notice
	// also lists the capabilities the check found.
	Tested bool
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
	ctx := r.Context()
	actor, err := browserProviderActor(ctx)
	if err != nil {
		return core.ErrForbidden(i18n.T(ctx, "models.error.session"))
	}
	providers, err := m.providers.ListManageable(ctx, actor)
	if err != nil {
		return err
	}
	inst, err := loadRemoteSettings(ctx, m.d.Settings)
	if err != nil {
		return err
	}
	publicURL := ""
	if m.d.Cfg != nil {
		publicURL = m.d.Cfg.PublicURL
	}
	v := modelsPageView{Server: web.ServerHost(inst.PublicURL, publicURL, r.Host), Providers: providers,
		LocalOnly: inst.LocalOnly, CanAdd: actor.Admin || inst.UsersMayAddProviders,
		Cap: modelsCapability(r.URL.Query().Get("cap"))}
	v.Tiles = m.modelsTiles(ctx, actor, providers, inst)
	v.Images, err = m.catalogueRows(ctx, actor, providers, inst)
	if err != nil {
		return err
	}
	return m.ui.Page(w, r, http.StatusOK, web.PageOpts{Title: i18n.T(ctx, "models.title"),
		MobileTitle: i18n.T(ctx, "models.mobile_title"), BackURL: "/settings",
		Section: layout.SectionStudio, Nav: layout.NavModels, Tab: layout.TabMe}, ModelsPage(v))
}

// modelsCapability returns the selected capability tab. The designed G2
// opens on Image.
func modelsCapability(raw string) string {
	for _, c := range modelCapabilities {
		if raw == c {
			return c
		}
	}
	return capImage
}

// modelsTiles builds the designed row of four summary tiles with real status:
// the local worker from the job queue, OpenRouter from the connections this
// user's jobs may route to, and fal and Ollama as designed but unbuilt.
func (m *Module) modelsTiles(ctx context.Context, actor providerActor, manageable []providerView, inst settings.Instance) []modelsTileView {
	compute := m.ui.Compute(ctx)
	local := modelsTileView{Icon: "cpu", Name: i18n.T(ctx, "models.tile.local"), State: tileOff,
		Detail: i18n.T(ctx, "models.tile.local_none")}
	if compute.WorkersOnline > 0 {
		local.State = tileOn
		if compute.GPU != "" {
			local.Detail = compute.GPU
		} else {
			local.Detail = i18n.T(ctx, "models.tile.local_cpu", "count", compute.WorkersOnline)
		}
	}

	openRouter := modelsTileView{Icon: "route", Name: providerKindName(providerKindOpenRouter), State: tileOff,
		Detail: i18n.T(ctx, "models.tile.not_connected")}
	candidates, err := m.providers.Candidates(ctx, actor.UserID)
	if err != nil && m.d.Log != nil {
		m.d.Log.WarnContext(ctx, "list providers for models tiles", "err", err)
	}
	if count := connectedTextModels(candidates, providerKindOpenRouter); count > 0 {
		openRouter.State = tileOn
		openRouter.Detail = i18n.T(ctx, "models.tile.text_models", "count", count)
		if providerKindStatus(manageable, providerKindOpenRouter) == setup.ProviderError {
			openRouter.State = tileError
		}
	}
	if inst.LocalOnly {
		openRouter.State, openRouter.Detail = tileOff, i18n.T(ctx, "models.tile.blocked")
	}

	falTile := modelsTileView{Icon: "bolt", Name: "fal", Detail: i18n.T(ctx, "models.tile.not_connected"), State: tileOff, Href: falConnectHref}
	ollamaTile := modelsTileView{Icon: "server-2", Name: "Ollama", Detail: i18n.T(ctx, "models.tile.not_connected"), State: tileOff, Href: ollamaConnectHref}
	for _, candidate := range candidates {
		switch candidate.Kind {
		case providerKindFal:
			falTile.State = tileOn
			falTile.Detail = i18n.T(ctx, "aiimage.tile.fal")
		case providerKindLocalText:
			ollamaTile.State = tileOn
			ollamaTile.Detail = candidate.Model
		}
	}
	if inst.LocalOnly {
		falTile.State = tileOff
		ollamaTile.State = tileOff
		falTile.Detail = i18n.T(ctx, "models.tile.blocked")
		ollamaTile.Detail = falTile.Detail
	}
	return []modelsTileView{
		local,
		openRouter,
		falTile,
		ollamaTile,
	}
}

// connectedTextModels counts the distinct text models reachable through the
// enabled connections of one kind (every route has exactly one model).
func connectedTextModels(candidates []providerCandidate, kind string) int {
	models := map[string]bool{}
	for _, c := range candidates {
		if c.Kind == kind {
			if model := providerKindModel(c.Kind); model != "" {
				models[model] = true
			}
		}
	}
	return len(models)
}

func (m *Module) newProviderPage(w http.ResponseWriter, r *http.Request) error {
	if m.ui == nil {
		return core.ErrInternal(errors.New("ai: web UI unavailable"))
	}
	kind := r.URL.Query().Get("kind")
	if kind == providerKindLocalText {
		core.Redirect(w, r, customURL("connect", ""))
		return nil
	}
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
		return core.ErrBadRequest(i18n.T(r.Context(), "models.error.form"))
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
	if !validProviderKind(kind) || kind == providerKindLocalText {
		return core.ErrNotFound()
	}
	v := providerFormView{Kind: kind, Name: web.FormValue(r, "name"), Scope: scope,
		Next: core.SafeRedirectTarget(web.FormValue(r, "next"), "/models"), Admin: actor.Admin,
		LocalOnly: inst.LocalOnly, Errors: web.FormErrors{}}
	key := strings.TrimSpace(r.PostFormValue("key"))
	if !validProviderName(v.Name) {
		v.Errors.Add("name", i18n.T(ctx, "models.form.name_invalid"))
	}
	if !validProviderKey(key) {
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
		return core.ErrBadRequest(i18n.T(r.Context(), "models.error.form"))
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
			return m.providerPageError(r.Context(), err)
		}
	}
	inst, settingsErr := loadRemoteSettings(ctx, m.d.Settings)
	if settingsErr != nil {
		return settingsErr
	}
	row := providerRowView{Provider: view, LocalOnly: inst.LocalOnly, Tested: true}
	if testErr != nil {
		row.Tone, row.Notice = ui.ToneError, i18n.T(ctx, "models.test.failed")
	} else {
		row.Tone, row.Notice = ui.ToneSuccess, i18n.T(ctx, "models.test.ok")
	}
	if core.IsHTMX(r) {
		return web.Render(w, r, http.StatusOK, ProviderRows(row), nil)
	}
	kind := "success"
	if testErr != nil {
		kind = "error"
	}
	web.Flash(w, r, kind, row.Notice, providerCapabilitySummary(ctx, view))
	core.Redirect(w, r, modelsReturnURL(r))
	return nil
}

func (m *Module) setProviderEnabled(w http.ResponseWriter, r *http.Request) error {
	if err := web.ParseForm(w, r); err != nil {
		return core.ErrBadRequest(i18n.T(r.Context(), "models.error.form"))
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
		return core.ErrBadRequest(i18n.T(r.Context(), "models.error.enabled"))
	}
	view, err := m.providers.SetEnabled(ctx, actor, id, enabled)
	if err != nil {
		return m.providerPageError(r.Context(), err)
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
		return web.Render(w, r, http.StatusOK, ProviderRows(providerRowView{Provider: view, LocalOnly: inst.LocalOnly,
			Tone: ui.ToneSuccess, Notice: message}), nil)
	}
	web.Flash(w, r, "success", message, "")
	core.Redirect(w, r, modelsReturnURL(r))
	return nil
}

func (m *Module) replaceProviderKey(w http.ResponseWriter, r *http.Request) error {
	if err := web.ParseForm(w, r); err != nil {
		return core.ErrBadRequest(i18n.T(r.Context(), "models.error.form"))
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
	if !validProviderKey(key) {
		view, getErr := m.providers.GetManageable(ctx, actor, id)
		if getErr != nil {
			return m.providerPageError(r.Context(), getErr)
		}
		row := providerRowView{Provider: view, LocalOnly: inst.LocalOnly, Tone: ui.ToneError,
			Notice: i18n.T(ctx, "models.form.key_invalid")}
		if core.IsHTMX(r) {
			return web.Render(w, r, http.StatusUnprocessableEntity, ProviderRows(row), nil)
		}
		web.Flash(w, r, "error", row.Notice, "")
		core.Redirect(w, r, modelsReturnURL(r))
		return nil
	}
	view, err := m.providers.ReplaceKey(ctx, actor, id, key)
	if err != nil {
		return m.providerPageError(r.Context(), err)
	}
	message := i18n.T(ctx, "models.flash.key_replaced")
	if core.IsHTMX(r) {
		return web.Render(w, r, http.StatusOK, ProviderRows(providerRowView{Provider: view, LocalOnly: inst.LocalOnly,
			Tone: ui.ToneSuccess, Notice: message}), nil)
	}
	web.Flash(w, r, "success", message, "")
	core.Redirect(w, r, modelsReturnURL(r))
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
		return providerActor{}, "", m.providerPageError(ctx, err)
	}
	return actor, id, nil
}

func (m *Module) providerPageError(ctx context.Context, err error) error {
	if errors.Is(err, errProviderNotFound) || errors.Is(err, errProviderForbidden) {
		return core.ErrNotFound()
	}
	if errors.Is(err, errProviderInvalid) || errors.Is(err, errProviderUnavailable) {
		return core.ErrBadRequest(i18n.T(ctx, "models.error.unavailable"))
	}
	return err
}

// setupProviderKinds maps a setup card kind to the connection kind whose
// status it shows. Cards without an entry are designed but not built.
var setupProviderKinds = map[string]string{
	providerKindOpenRouter: providerKindOpenRouter,
	"fal":                  providerKindFal,
	"ollama":               providerKindLocalText,
	"custom":               providerKindLocalText,
	"vendor":               providerKindOpenAI,
}

// Providers implements setup.ModelStep without making a network call. It
// returns the five provider rows of design G8 step 3 in design order:
// OpenRouter, fal, direct vendor keys (OpenAI today), Ollama and custom
// connections. fal, Ollama and custom link to paths no route serves yet, so
// their Connect buttons render as "Soon".
func (m *Module) Providers(ctx context.Context) ([]setup.ProviderCard, error) {
	const next = "&next=/setup/models"
	cards := []setup.ProviderCard{
		{Kind: providerKindOpenRouter, NameKey: "setup.provider.openrouter.name", DescKey: "setup.provider.openrouter.desc",
			Icon: "route", Href: "/models/providers/new?kind=" + providerKindOpenRouter + next},
		{Kind: "fal", NameKey: "setup.provider.fal.name", DescKey: "setup.provider.fal.desc", Icon: "bolt", Href: falConnectHref},
		{Kind: "vendor", NameKey: "setup.provider.vendor.name", DescKey: "setup.provider.vendor.desc",
			Icon: "brand-openai", Href: "/models/providers/new?kind=" + providerKindOpenAI + next},
		{Kind: "ollama", NameKey: "setup.provider.ollama.name", DescKey: "setup.provider.ollama.desc", Icon: "server-2",
			Href: ollamaConnectHref},
		{Kind: "custom", NameKey: "setup.provider.custom.name", DescKey: "setup.provider.custom.desc", Icon: "adjustments",
			Href: modelsCustomHref},
	}
	setStatus := func(status func(kind string) string) {
		for i := range cards {
			if kind, ok := setupProviderKinds[cards[i].Kind]; ok {
				cards[i].Status = status(kind)
			}
		}
	}
	actor, err := browserProviderActor(ctx)
	if err != nil {
		setStatus(func(string) string { return setup.ProviderError })
		return cards, nil
	}
	providers, err := m.providers.ListManageable(ctx, actor)
	if err != nil {
		if m.d.Log != nil {
			m.d.Log.WarnContext(ctx, "list providers for setup", "err", err)
		}
		setStatus(func(string) string { return setup.ProviderError })
		return cards, nil
	}
	setStatus(func(kind string) string { return providerKindStatus(providers, kind) })
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
	if kind == providerKindFal {
		return "fal"
	}
	if kind == providerKindLocalText {
		return "OpenAI-compatible"
	}
	if kind == providerKindWorker {
		return "Local worker"
	}
	if route, ok := routeForKind(kind); ok {
		return route.Label
	}
	return kind
}

func providerKindModel(kind string) string {
	if kind == providerKindFal {
		return "FLUX.1 schnell / inpainting"
	}
	if route, ok := routeForKind(kind); ok {
		return route.Model
	}
	return ""
}

// providerCostLabel is the COST column for a remote route: the hard per-call
// reservation from the route table, rounded up to whole cents so the "≤"
// stays true. It is the user's own spend on their key, not a price.
func providerCostLabel(ctx context.Context, kind string) string {
	if kind == providerKindLocalText {
		return i18n.T(ctx, "models.cost.none")
	}
	if kind == providerKindFal {
		return "≤ $0.280 / call"
	}
	route, ok := routeForKind(kind)
	if !ok {
		return ""
	}
	return i18n.T(ctx, "models.cost.per_call", "amount", formatUSD(math.Ceil(route.ReserveUSD*100)/100))
}

// formatUSD formats a dollar amount for spend limits and estimates ("$20",
// "$0.45").
func formatUSD(v float64) string {
	return "$" + strings.TrimSuffix(strconv.FormatFloat(v, 'f', 2, 64), ".00")
}

// Recommend implements setup.ModelStep using the setup package's deterministic
// hardware recommendation. It performs no network I/O.
func (m *Module) Recommend(_ context.Context, gpus []jobs.GPU) (*setup.Recommendation, error) {
	return setup.RecommendFor(gpus), nil
}

// modelsReturnURL brings a no-JavaScript row action back to its tab: image
// rows post with ?cap=image; the text table's connection rows do not.
func modelsReturnURL(r *http.Request) string {
	if r.URL.Query().Get("cap") == capImage {
		return "/models?cap=image"
	}
	return "/models?cap=text"
}
