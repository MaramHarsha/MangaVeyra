package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/secrets"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
	"github.com/MaramHarsha/MangaVeyra/internal/web"
	"github.com/MaramHarsha/MangaVeyra/internal/web/layout"
)

type customProviderView struct {
	Step, ID, Name, Endpoint, Model, Scope, TestState, Notice string
	Context, Output                                           int
	Admin, LocalOnly                                          bool
	// PrivateAllowed is the live private-network endpoint setting. While it
	// is off, step 1 offers admins the switch and tells others to ask one.
	PrivateAllowed bool
	// AllowPrivate is the admin's switch on step 1 (on by default, because a
	// custom connection is always a server on this machine or network).
	AllowPrivate bool
	// HasKey reports a stored key on a draft being edited (the field may stay
	// blank to keep it).
	HasKey bool
	// DurationMS is the last test's duration; Modes the step 4 choices.
	DurationMS int64
	Modes      []string
	// ServerMode is this server's default routing mode, shown on step 4.
	ServerMode string
	// Blocked is a member reaching the wizard while personal providers are
	// off: the page explains who can connect one instead of the form.
	Blocked bool
	Errors  web.FormErrors
	customTypedView
}

var customSteps = []string{"connect", "test", "describe", "route"}

func customStepNumber(step string) int {
	for i, s := range customSteps {
		if s == step {
			return i + 1
		}
	}
	return 0
}
func customURL(step, id string) string {
	u := "/models/providers/new/" + step
	if id != "" {
		u += "?id=" + id
	}
	return u
}

// customBase is the view every step starts from.
func (m *Module) customBase(r *http.Request, actor providerActor, step string, inst settings.Instance) customProviderView {
	v := customProviderView{Step: step, Name: "Ollama", Endpoint: "http://localhost:11434/v1", Scope: providerScopeUser,
		Context: 32768, Output: 4096, Admin: actor.Admin, LocalOnly: inst.LocalOnly, ServerMode: inst.RoutingMode,
		PrivateAllowed: m.private.Allowed(r.Context()), AllowPrivate: true, Modes: defaultLocalModes, Errors: web.FormErrors{},
		customTypedView: customTypedView{Type: customTypeCompatible}}
	if actor.Admin {
		v.Scope = providerScopeServer
	}
	return v
}

// withRecord fills a view from a stored draft.
func (v *customProviderView) withRecord(ctx context.Context, record providerRecord) {
	last := record.view().LastTest
	v.ID, v.Name, v.Endpoint, v.Scope = record.ID, record.Name, record.BaseURL, record.Scope
	v.TestState, v.DurationMS, v.HasKey = last.State, last.DurationMS, record.KeyEnc != ""
	if record.Kind != providerKindLocalText {
		v.withTypedRecord(ctx, record)
		return
	}
	cfg := record.localConfig()
	v.Model, v.Context, v.Output, v.Modes = cfg.Model, cfg.ContextTokens, cfg.OutputTokens, cfg.routeModes()
}

func (m *Module) customPage(w http.ResponseWriter, r *http.Request) error {
	step := strings.TrimPrefix(r.URL.Path, "/models/providers/new/")
	actor, err := browserProviderActor(r.Context())
	if err != nil {
		return core.ErrForbidden("")
	}
	inst, err := m.image.readSettings(r.Context())
	if err != nil {
		return err
	}
	v := m.customBase(r, actor, step, inst)
	if !actor.Admin && !inst.UsersMayAddProviders {
		v.Blocked = true
		return m.renderCustom(w, r, http.StatusForbidden, v)
	}
	if step != "connect" || r.URL.Query().Get("id") != "" {
		record, err := m.customRecord(r.Context(), actor, r.URL.Query().Get("id"))
		if err != nil {
			return core.ErrNotFound()
		}
		if customStepNumber(step) > customStepNumber(record.view().LastTest.Step) {
			return core.ErrForbidden(i18n.T(r.Context(), "aiimage.custom.complete_previous"))
		}
		v.withRecord(r.Context(), record)
	} else if v.Type = customTypeFromQuery(r.URL.Query().Get("type")); v.Type != customTypeCompatible {
		v.typeDefaults(r.Context())
	}
	return m.renderCustom(w, r, http.StatusOK, v)
}

func (m *Module) customRecord(ctx context.Context, actor providerActor, id string) (providerRecord, error) {
	r, err := providerByID(ctx, m.d.DB, id, false)
	if err != nil || !customKind(r.Kind) || r.Enabled || authorizeProvider(actor, r) != nil || validateProviderRecord(r) != nil || r.view().LastTest.Step == "" {
		return providerRecord{}, errProviderUnavailable
	}
	return r, nil
}

func (m *Module) customPost(w http.ResponseWriter, r *http.Request) error {
	if err := web.ParseForm(w, r); err != nil {
		return err
	}
	step := strings.TrimPrefix(r.URL.Path, "/models/providers/new/")
	actor, err := browserProviderActor(r.Context())
	if err != nil {
		return core.ErrForbidden("")
	}
	inst, err := m.image.readSettings(r.Context())
	if err != nil {
		return err
	}
	if !actor.Admin && !inst.UsersMayAddProviders {
		return core.ErrForbidden(i18n.T(r.Context(), "models.error.personal_disabled"))
	}
	if step == "connect" {
		if id := web.FormValue(r, "id"); id != "" {
			record, err := m.customRecord(r.Context(), actor, id)
			if err != nil {
				return core.ErrNotFound()
			}
			if record.Kind != providerKindLocalText {
				return m.customConnectTyped(w, r, actor, inst, customKindType(record.Kind), &record)
			}
		} else if typ := customTypeFromQuery(web.FormValue(r, "type")); typ != customTypeCompatible {
			return m.customConnectTyped(w, r, actor, inst, typ, nil)
		}
		return m.customConnect(w, r, actor, inst)
	}
	record, err := m.customRecord(r.Context(), actor, web.FormValue(r, "id"))
	if err != nil {
		return core.ErrNotFound()
	}
	last := record.view().LastTest
	if customStepNumber(step) > customStepNumber(last.Step) {
		return core.ErrForbidden(i18n.T(r.Context(), "aiimage.custom.complete_previous"))
	}
	v := m.customBase(r, actor, step, inst)
	v.withRecord(r.Context(), record)
	switch step {
	case "test":
		// A passed test moves on; otherwise this is "Test again".
		if web.FormValue(r, "action") == "next" && last.State == "ok" {
			if err := m.setCustomStage(r.Context(), record.ID, "describe", "ok", last.DurationMS); err != nil {
				return err
			}
			core.Redirect(w, r, customURL("describe", record.ID))
			return nil
		}
		if inst.LocalOnly {
			v.Notice = i18n.T(r.Context(), "models.local_only.body")
			return m.renderCustom(w, r, http.StatusUnprocessableEntity, v)
		}
		if err := m.runCustomTest(r.Context(), record); err != nil {
			return err
		}
		core.Redirect(w, r, customURL("test", record.ID))
		return nil
	case "describe":
		if record.Kind != providerKindLocalText {
			return m.customDescribeTyped(w, r, v, record, last)
		}
		v.Name = web.FormValue(r, "name")
		v.Context, _ = strconv.Atoi(web.FormValue(r, "context"))
		v.Output, _ = strconv.Atoi(web.FormValue(r, "output"))
		if !validProviderName(v.Name) {
			v.Errors.Add("name", i18n.T(r.Context(), "models.form.name_invalid"))
		}
		cfg := record.localConfig()
		cfg.ContextTokens, cfg.OutputTokens = v.Context, v.Output
		if validateLocalTextConfig(v.Endpoint, cfg) != nil {
			v.Errors.Add("context", i18n.T(r.Context(), "aiimage.custom.tokens_invalid"))
		}
		if v.Errors.Has() {
			return m.renderCustom(w, r, http.StatusUnprocessableEntity, v)
		}
		raw, _ := json.Marshal(cfg)
		_, err = m.d.DB.Exec(r.Context(), `UPDATE providers SET name = ?, config = ?, updated_at = ? WHERE id = ? AND enabled = FALSE`, v.Name, string(raw), db.Now(), record.ID)
		if err != nil {
			return err
		}
		if err := m.setCustomStage(r.Context(), record.ID, "route", last.State, last.DurationMS); err != nil {
			return err
		}
		core.Redirect(w, r, customURL("route", record.ID))
		return nil
	case "route":
		if last.State != "ok" || last.Step != "route" {
			return core.ErrForbidden(i18n.T(r.Context(), "aiimage.custom.complete_previous"))
		}
		modes := normalizeFormModes(r.PostForm["modes"])
		v.Modes = modes
		capability := capText
		if record.Kind != providerKindLocalText {
			var err error
			if capability, err = m.customRouteTyped(r, record, modes); err != nil {
				v.Errors.Add("modes", i18n.T(r.Context(), "aiimage.custom.modes_invalid"))
				return m.renderCustom(w, r, http.StatusUnprocessableEntity, v)
			}
		} else {
			cfg := record.localConfig()
			cfg.Modes = modes
			if len(modes) == 0 || validateLocalTextConfig(record.BaseURL, cfg) != nil {
				v.Errors.Add("modes", i18n.T(r.Context(), "aiimage.custom.modes_invalid"))
				return m.renderCustom(w, r, http.StatusUnprocessableEntity, v)
			}
			raw, _ := json.Marshal(cfg)
			if _, err := m.d.DB.Exec(r.Context(), `UPDATE providers SET config = ?, updated_at = ? WHERE id = ? AND enabled = FALSE`, string(raw), db.Now(), record.ID); err != nil {
				return err
			}
		}
		if _, err := m.providers.SetEnabled(r.Context(), actor, record.ID, true); err != nil {
			return err
		}
		if err := m.setCustomStage(r.Context(), record.ID, "", "ok", last.DurationMS); err != nil {
			return err
		}
		web.Flash(w, r, "success", i18n.T(r.Context(), "models.flash.connected"), "")
		core.Redirect(w, r, "/models?cap="+capability)
		return nil
	}
	return core.ErrNotFound()
}

// customConnect saves step 1 as a disabled draft (or updates the draft the
// user came back to) and runs the connection test, so step 2 opens on its
// result. The test only lists installed models: no prompt is sent.
func (m *Module) customConnect(w http.ResponseWriter, r *http.Request, actor providerActor, inst settings.Instance) error {
	ctx := r.Context()
	v := m.customBase(r, actor, "connect", inst)
	v.ID = web.FormValue(r, "id")
	v.Name, v.Model, v.Scope = web.FormValue(r, "name"), web.FormValue(r, "model"), web.FormValue(r, "scope")
	v.Endpoint = strings.TrimRight(web.FormValue(r, "endpoint"), "/")
	v.AllowPrivate = web.FormValue(r, "allow_private") == "true"
	var draft *providerRecord
	if v.ID != "" {
		record, err := m.customRecord(ctx, actor, v.ID)
		if err != nil {
			return core.ErrNotFound()
		}
		draft = &record
		cfg := record.localConfig()
		v.Context, v.Output, v.Modes, v.HasKey = cfg.ContextTokens, cfg.OutputTokens, cfg.routeModes(), record.KeyEnc != ""
	}
	if !actor.Admin && v.Scope != providerScopeUser {
		return core.ErrForbidden("")
	}
	key := strings.TrimSpace(r.PostFormValue("key"))
	if !validProviderName(v.Name) {
		v.Errors.Add("name", i18n.T(ctx, "models.form.name_invalid"))
	}
	switch {
	case validateLocalEndpoint(v.Endpoint) != nil:
		v.Errors.Add("endpoint", i18n.T(ctx, "aiimage.custom.endpoint_invalid"))
	case endpointNeverAllowed(v.Endpoint):
		v.Errors.Add("endpoint", i18n.T(ctx, "aiimage.custom.endpoint_blocked"))
	case !v.PrivateAllowed && !(actor.Admin && v.AllowPrivate):
		if actor.Admin {
			v.Errors.Add("endpoint", i18n.T(ctx, "aiimage.custom.private_off_admin"))
		} else {
			v.Errors.Add("endpoint", i18n.T(ctx, "aiimage.custom.private_off_user"))
		}
	}
	if strings.TrimSpace(v.Model) == "" || !validLocalModel(v.Model) {
		v.Errors.Add("model", i18n.T(ctx, "aiimage.custom.model_invalid"))
	}
	if key != "" && !validProviderKey(key) {
		v.Errors.Add("key", i18n.T(ctx, "models.form.key_invalid"))
	}
	if v.Scope != providerScopeServer && v.Scope != providerScopeUser {
		v.Errors.Add("scope", i18n.T(ctx, "models.form.scope_invalid"))
	}
	if v.Errors.Has() {
		return m.renderCustom(w, r, http.StatusUnprocessableEntity, v)
	}
	if !v.PrivateAllowed {
		// Only an admin reaches here with the setting off: they left the
		// visible switch on, which allows private networks on this server.
		if err := m.private.Set(ctx, true, actor.UserID); err != nil {
			return err
		}
	}
	local := localTextConfig{Model: v.Model, ContextTokens: v.Context, OutputTokens: v.Output}
	if draft != nil {
		local.Modes = draft.localConfig().Modes
	}
	var id string
	if draft == nil {
		created, err := m.providers.Create(ctx, actor, providerCreate{Kind: providerKindLocalText, Name: v.Name, BaseURL: v.Endpoint, Local: local, Key: key, Scope: v.Scope, Draft: true})
		if err != nil {
			v.Errors.Add("", i18n.T(ctx, "aiimage.form.invalid"))
			return m.renderCustom(w, r, http.StatusUnprocessableEntity, v)
		}
		id = created.ID
	} else {
		if err := m.updateCustomDraft(ctx, actor, draft.ID, v.Name, v.Endpoint, v.Scope, key, local); err != nil {
			v.Errors.Add("", i18n.T(ctx, "aiimage.form.invalid"))
			return m.renderCustom(w, r, http.StatusUnprocessableEntity, v)
		}
		id = draft.ID
	}
	if err := m.setCustomStage(ctx, id, "test", "", 0); err != nil {
		return err
	}
	if !inst.LocalOnly {
		record, err := m.customRecord(ctx, actor, id)
		if err != nil {
			return core.ErrNotFound()
		}
		if err := m.runCustomTest(ctx, record); err != nil {
			return err
		}
	}
	core.Redirect(w, r, customURL("test", id))
	return nil
}

// updateCustomDraft replaces step 1 of a disabled draft. A blank key keeps
// the stored one; the test result is cleared because the target changed.
func (m *Module) updateCustomDraft(ctx context.Context, actor providerActor, id, name, endpoint, scope, key string, local localTextConfig) error {
	if !validProviderName(name) || validateLocalTextConfig(endpoint, local) != nil || key != "" && !validProviderKey(key) {
		return errProviderInvalid
	}
	owner, err := m.providers.ownerForCreate(ctx, actor, scope)
	if err != nil {
		return err
	}
	raw, _ := json.Marshal(local)
	return m.d.DB.WithTx(ctx, func(tx *db.Tx) error {
		r, err := providerByID(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if r.Kind != providerKindLocalText || r.Enabled || authorizeProvider(actor, r) != nil {
			return errProviderForbidden
		}
		keyEnc, hint := r.KeyEnc, r.KeyHint
		if key != "" {
			keyEnc, hint = m.providers.box.SealString(key, providerKeyAAD(r.ID)), secrets.Hint(key)
		}
		_, err = tx.Exec(ctx, `UPDATE providers SET name = ?, base_url = ?, config = ?, scope = ?, owner_id = ?, key_enc = ?, key_hint = ?, last_test = '{}', updated_at = ? WHERE id = ? AND enabled = FALSE`,
			name, endpoint, string(raw), scope, db.NullString(owner), keyEnc, hint, db.Now(), r.ID)
		return err
	})
}

// runCustomTest lists the endpoint's installed models through the guarded
// local client under the live private-endpoint policy, and stores the result
// and its duration. Only a passed test unlocks step 3.
func (m *Module) runCustomTest(ctx context.Context, record providerRecord) error {
	if record.Kind != providerKindLocalText {
		return m.runTypedTest(ctx, record)
	}
	state, stage := "error", "test"
	started := time.Now()
	if _, err := m.private.netPolicy(ctx).CheckURL(ctx, record.BaseURL, nil); err == nil {
		p := dispatchProvider{ID: record.ID, Kind: record.Kind, BaseURL: record.BaseURL, Local: record.localConfig()}
		keyOK := true
		if record.KeyEnc != "" {
			key, err := m.d.Secrets.OpenString(record.KeyEnc, providerKeyAAD(record.ID))
			keyOK = err == nil
			p.Key = key
		}
		if keyOK {
			testCtx, cancel := context.WithTimeout(ctx, providerTestTimeout)
			if testLocalText(testCtx, p, m.text.localDoer) == nil {
				state, stage = "ok", "describe"
			}
			cancel()
		}
	}
	return m.setCustomStage(context.WithoutCancel(ctx), record.ID, stage, state, time.Since(started).Milliseconds())
}

// normalizeFormModes keeps known modes in G11 order.
func normalizeFormModes(values []string) []string {
	out := []string{}
	for _, mode := range localRouteModes {
		for _, value := range values {
			if value == mode {
				out = append(out, mode)
				break
			}
		}
	}
	return out
}

func (m *Module) setCustomStage(ctx context.Context, id, stage, state string, durationMS int64) error {
	if durationMS < 0 {
		durationMS = 0
	}
	raw, _ := json.Marshal(providerTestState{Step: stage, State: state, Time: db.Now(), DurationMS: durationMS})
	_, err := m.d.DB.Exec(ctx, `UPDATE providers SET last_test = ?, updated_at = ? WHERE id = ?`, string(raw), db.Now(), id)
	return err
}
func (m *Module) renderCustom(w http.ResponseWriter, r *http.Request, status int, v customProviderView) error {
	return m.ui.Page(w, r, status, web.PageOpts{Title: i18n.T(r.Context(), "aiimage.custom.title"), BackURL: "/models", Section: layout.SectionStudio, Nav: layout.NavModels}, CustomProviderPage(v))
}

type falKeyTester struct{ doer httpDoer }

func (f falKeyTester) TestKey(ctx context.Context, key string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.fal.ai/v1/models/pricing?endpoint_id=fal-ai/flux/schnell", nil)
	if err != nil {
		return "", errProviderUnavailable
	}
	req.Header.Set("Authorization", "Key "+key)
	resp, err := f.doer.Do(req)
	if err != nil || resp == nil || resp.Body == nil {
		return "", errProviderUnavailable
	}
	defer resp.Body.Close()
	_, large, err := readBounded(resp.Body, 64<<10)
	if err != nil || large || resp.StatusCode != 200 {
		return "", errProviderUnavailable
	}
	return sanitizeRequestID(resp.Header.Get("X-Request-ID")), nil
}
