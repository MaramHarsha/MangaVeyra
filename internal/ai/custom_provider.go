package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/web"
	"github.com/MaramHarsha/MangaVeyra/internal/web/layout"
)

type customProviderView struct {
	Step, ID, Name, Endpoint, Model, Scope, TestState, Notice string
	Context, Output                                           int
	Admin, LocalOnly                                          bool
	Errors                                                    web.FormErrors
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
	if !actor.Admin && !inst.UsersMayAddProviders {
		return core.ErrForbidden(i18n.T(r.Context(), "models.error.personal_disabled"))
	}
	v := customProviderView{Step: step, Name: "Ollama", Endpoint: "http://localhost:11434/v1", Scope: providerScopeUser, Context: 32768, Output: 4096, Admin: actor.Admin, LocalOnly: inst.LocalOnly, Errors: web.FormErrors{}}
	if actor.Admin {
		v.Scope = providerScopeServer
	}
	if step != "connect" || r.URL.Query().Get("id") != "" {
		record, err := m.customRecord(r.Context(), actor, r.URL.Query().Get("id"))
		if err != nil {
			return core.ErrNotFound()
		}
		last := record.view().LastTest
		if customStepNumber(step) > customStepNumber(last.Step) {
			return core.ErrForbidden(i18n.T(r.Context(), "aiimage.custom.complete_previous"))
		}
		cfg := record.localConfig()
		v.ID = record.ID
		v.Name = record.Name
		v.Endpoint = record.BaseURL
		v.Model = cfg.Model
		v.Context = cfg.ContextTokens
		v.Output = cfg.OutputTokens
		v.Scope = record.Scope
		v.TestState = last.State
	}
	return m.renderCustom(w, r, http.StatusOK, v)
}

func (m *Module) customRecord(ctx context.Context, actor providerActor, id string) (providerRecord, error) {
	r, err := providerByID(ctx, m.d.DB, id, false)
	if err != nil || r.Kind != providerKindLocalText || r.Enabled || authorizeProvider(actor, r) != nil || validateProviderRecord(r) != nil || r.view().LastTest.Step == "" {
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
		v := customProviderView{Step: step, Name: web.FormValue(r, "name"), Endpoint: strings.TrimRight(web.FormValue(r, "endpoint"), "/"), Model: web.FormValue(r, "model"), Scope: web.FormValue(r, "scope"), Context: 32768, Output: 4096, Admin: actor.Admin, LocalOnly: inst.LocalOnly, Errors: web.FormErrors{}}
		key := strings.TrimSpace(r.PostFormValue("key"))
		if !validProviderName(v.Name) {
			v.Errors.Add("name", i18n.T(r.Context(), "models.form.name_invalid"))
		}
		if validateLocalTextConfig(v.Endpoint, localTextConfig{v.Model, v.Context, v.Output}) != nil {
			v.Errors.Add("endpoint", i18n.T(r.Context(), "aiimage.custom.endpoint_invalid"))
		}
		if key != "" && !validProviderKey(key) {
			v.Errors.Add("key", i18n.T(r.Context(), "models.form.key_invalid"))
		}
		if !actor.Admin && v.Scope != providerScopeUser {
			return core.ErrForbidden("")
		}
		// Check saving policy without dispatching to the endpoint. DNS/IP policy is
		// rechecked by the guarded transport when the human presses Test.
		if strings.Contains(v.Endpoint, "localhost") && !m.private.Allowed(r.Context()) {
			v.Errors.Add("endpoint", i18n.T(r.Context(), "aiimage.custom.private_disabled"))
		}
		if v.Errors.Has() {
			return m.renderCustom(w, r, http.StatusUnprocessableEntity, v)
		}
		created, err := m.providers.Create(r.Context(), actor, providerCreate{Kind: providerKindLocalText, Name: v.Name, BaseURL: v.Endpoint, Local: localTextConfig{v.Model, v.Context, v.Output}, Key: key, Scope: v.Scope, Draft: true})
		if err != nil {
			v.Errors.Add("", i18n.T(r.Context(), "aiimage.form.invalid"))
			return m.renderCustom(w, r, http.StatusUnprocessableEntity, v)
		}
		if err := m.setCustomStage(r.Context(), created.ID, "test", ""); err != nil {
			return err
		}
		core.Redirect(w, r, customURL("test", created.ID))
		return nil
	}
	record, err := m.customRecord(r.Context(), actor, web.FormValue(r, "id"))
	if err != nil {
		return core.ErrNotFound()
	}
	last := record.view().LastTest
	if customStepNumber(step) > customStepNumber(last.Step) {
		return core.ErrForbidden(i18n.T(r.Context(), "aiimage.custom.complete_previous"))
	}
	cfg := record.localConfig()
	v := customProviderView{Step: step, ID: record.ID, Name: record.Name, Endpoint: record.BaseURL, Model: cfg.Model, Scope: record.Scope, Context: cfg.ContextTokens, Output: cfg.OutputTokens, TestState: last.State, Admin: actor.Admin, LocalOnly: inst.LocalOnly, Errors: web.FormErrors{}}
	switch step {
	case "test":
		if inst.LocalOnly {
			v.Notice = i18n.T(r.Context(), "models.local_only.body")
			return m.renderCustom(w, r, http.StatusUnprocessableEntity, v)
		}
		if _, err := m.private.netPolicy(r.Context()).CheckURL(r.Context(), record.BaseURL, nil); err != nil {
			v.Notice = i18n.T(r.Context(), "aiimage.custom.endpoint_invalid")
			return m.renderCustom(w, r, http.StatusUnprocessableEntity, v)
		}
		p := dispatchProvider{ID: record.ID, Kind: record.Kind, BaseURL: record.BaseURL, Local: cfg}
		if record.KeyEnc != "" {
			p.Key, err = m.d.Secrets.OpenString(record.KeyEnc, providerKeyAAD(record.ID))
			if err != nil {
				return core.ErrInternal(errProviderUnavailable)
			}
		}
		testCtx, cancel := context.WithTimeout(r.Context(), providerTestTimeout)
		err = testLocalText(testCtx, p, m.text.localDoer)
		cancel()
		state := "ok"
		stage := "describe"
		if err != nil {
			state = "error"
			stage = "test"
		}
		if err := m.setCustomStage(r.Context(), record.ID, stage, state); err != nil {
			return err
		}
		if state == "error" {
			v.TestState = state
			v.Notice = i18n.T(r.Context(), "aiimage.custom.test_failed")
			return m.renderCustom(w, r, http.StatusUnprocessableEntity, v)
		}
		core.Redirect(w, r, customURL("describe", record.ID))
		return nil
	case "describe":
		v.Name = web.FormValue(r, "name")
		v.Context, _ = strconv.Atoi(web.FormValue(r, "context"))
		v.Output, _ = strconv.Atoi(web.FormValue(r, "output"))
		if !validProviderName(v.Name) || validateLocalTextConfig(v.Endpoint, localTextConfig{v.Model, v.Context, v.Output}) != nil {
			v.Errors.Add("", i18n.T(r.Context(), "aiimage.form.invalid"))
			return m.renderCustom(w, r, http.StatusUnprocessableEntity, v)
		}
		raw, _ := json.Marshal(localTextConfig{v.Model, v.Context, v.Output})
		_, err = m.d.DB.Exec(r.Context(), `UPDATE providers SET name = ?, config = ?, updated_at = ? WHERE id = ? AND enabled = FALSE`, v.Name, string(raw), db.Now(), record.ID)
		if err != nil {
			return err
		}
		if err := m.setCustomStage(r.Context(), record.ID, "route", last.State); err != nil {
			return err
		}
		core.Redirect(w, r, customURL("route", record.ID))
		return nil
	case "route":
		if last.State != "ok" || last.Step != "route" {
			return core.ErrForbidden(i18n.T(r.Context(), "aiimage.custom.complete_previous"))
		}
		if _, err := m.providers.SetEnabled(r.Context(), actor, record.ID, true); err != nil {
			return err
		}
		if err := m.setCustomStage(r.Context(), record.ID, "", "ok"); err != nil {
			return err
		}
		web.Flash(w, r, "success", i18n.T(r.Context(), "models.flash.connected"), "")
		core.Redirect(w, r, "/models?cap=text")
		return nil
	}
	return core.ErrNotFound()
}

func (m *Module) setCustomStage(ctx context.Context, id, stage, state string) error {
	raw, _ := json.Marshal(providerTestState{Step: stage, State: state, Time: db.Now()})
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
