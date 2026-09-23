package ai

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/MaramHarsha/MangaVeyra/internal/ai/catalog"
	"github.com/MaramHarsha/MangaVeyra/internal/ai/router"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
)

// apiError turns a field error into the JSON validation envelope.
func apiError(r *http.Request, err error) error {
	var fe *FieldError
	if errors.As(err, &fe) {
		return core.ErrValidation(map[string]string{fe.Field: i18n.T(r.Context(), fe.Key)})
	}
	return err
}

type apiModel struct {
	*catalog.Manifest
	Available bool           `json:"available"`
	Enabled   bool           `json:"enabled"`
	Provider  string         `json:"provider_id,omitempty"`
	Reason    *router.Reason `json:"unavailable_reason,omitempty"`
}

// apiModels lists the catalogue with availability for the caller (GET /api/v1/ai/models?kind=image).
func (m *Module) apiModels(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	u := core.UserFrom(ctx)
	in, err := m.svc.Inputs(ctx, u.ID)
	if err != nil {
		return err
	}
	kind := r.URL.Query().Get("kind")
	out := []apiModel{}
	for _, man := range in.Catalog.All() {
		if kind != "" && man.Kind != kind {
			continue
		}
		av := router.Available(in, man)
		am := apiModel{Manifest: man, Available: av.OK, Enabled: !in.Disabled[man.ID], Provider: av.Provider.ID}
		if !av.OK && av.Reason.Code != "" {
			rr := av.Reason
			am.Reason = &rr
		}
		out = append(out, am)
	}
	core.JSON(w, http.StatusOK, map[string]any{"items": out})
	return nil
}

func (m *Module) apiProviders(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	list, err := m.svc.ListProviders(ctx, core.UserFrom(ctx).ID, false)
	if err != nil {
		return err
	}
	core.JSON(w, http.StatusOK, map[string]any{"items": list})
	return nil
}

type providerBody struct {
	Kind         string          `json:"kind"`
	Name         string          `json:"name"`
	BaseURL      string          `json:"base_url"`
	Key          string          `json:"key"`
	Scope        string          `json:"scope"`
	Options      json.RawMessage `json:"options"`
	ModelID      string          `json:"model_id"`
	Note         string          `json:"note"`
	CostPerImage *float64        `json:"cost_per_image"`
	Enabled      *bool           `json:"enabled"`
}

func (b providerBody) input() ProviderInput {
	return ProviderInput{Kind: b.Kind, Name: b.Name, BaseURL: b.BaseURL, Key: b.Key, KeepKey: true, Scope: b.Scope, Options: b.Options,
		ModelID: b.ModelID, Note: b.Note, CostPerImage: b.CostPerImage}
}

func (m *Module) apiCreateProvider(w http.ResponseWriter, r *http.Request) error {
	var b providerBody
	if err := core.DecodeJSON(w, r, &b); err != nil {
		return err
	}
	ctx := r.Context()
	p, err := m.svc.SaveProvider(ctx, core.UserFrom(ctx), "", b.input())
	if err != nil {
		return apiError(r, err)
	}
	core.JSON(w, http.StatusCreated, p)
	return nil
}

func (m *Module) apiUpdateProvider(w http.ResponseWriter, r *http.Request) error {
	var b providerBody
	if err := core.DecodeJSON(w, r, &b); err != nil {
		return err
	}
	ctx := r.Context()
	u := core.UserFrom(ctx)
	id := r.PathValue("id")
	cur, err := m.svc.GetProvider(ctx, id)
	if err != nil {
		return err
	}
	in := b.input()
	if in.Name == "" {
		in.Name = cur.Name
	}
	if in.BaseURL == "" {
		in.BaseURL = cur.BaseURL
	}
	if in.ModelID == "" {
		in.ModelID = cur.Config.V.ModelID
	}
	p, err := m.svc.SaveProvider(ctx, u, id, in)
	if err != nil {
		return apiError(r, err)
	}
	if b.Enabled != nil {
		if err := m.svc.SetProviderEnabled(ctx, u, id, *b.Enabled); err != nil {
			return err
		}
		p, _ = m.svc.GetProvider(ctx, id)
	}
	core.JSON(w, http.StatusOK, p)
	return nil
}

func (m *Module) apiDeleteProvider(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	if err := m.svc.DeleteProvider(ctx, core.UserFrom(ctx), r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (m *Module) apiTestProvider(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	var b struct {
		Mode string `json:"mode"`
	}
	if r.ContentLength > 0 {
		if err := core.DecodeJSON(w, r, &b); err != nil {
			return err
		}
	}
	res, err := m.svc.TestProvider(ctx, core.UserFrom(ctx), r.PathValue("id"), b.Mode)
	if err != nil {
		return err
	}
	core.JSON(w, http.StatusOK, res)
	return nil
}

type routeBody struct {
	router.Need
	Prompt string `json:"prompt"`
}

// apiRoute explains the routing for a need (the routing chip and "Why" popover).
func (m *Module) apiRoute(w http.ResponseWriter, r *http.Request) error {
	var b routeBody
	if err := core.DecodeJSON(w, r, &b); err != nil {
		return err
	}
	ctx := r.Context()
	b.UserID = core.UserFrom(ctx).ID
	if b.Task == "" {
		b.Task = catalog.TaskGenerate
	}
	e, err := m.svc.Explain(ctx, b.Need, b.Prompt)
	if err != nil {
		return err
	}
	core.JSON(w, http.StatusOK, map[string]any{"explanation": e, "decision": e.Decision})
	return nil
}

func (m *Module) apiEstimate(w http.ResponseWriter, r *http.Request) error {
	var b routeBody
	if err := core.DecodeJSON(w, r, &b); err != nil {
		return err
	}
	ctx := r.Context()
	b.UserID = core.UserFrom(ctx).ID
	if b.Task == "" {
		b.Task = catalog.TaskGenerate
	}
	e, err := m.svc.Explain(ctx, b.Need, b.Prompt)
	if err != nil {
		return err
	}
	core.JSON(w, http.StatusOK, map[string]any{"estimate": e.Estimate, "text": e.EstimateText, "local": e.Local, "model_id": e.ModelID})
	return nil
}

func (m *Module) apiDecisions(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	list, err := m.svc.Decisions(ctx, core.UserFrom(ctx), 50)
	if err != nil {
		return err
	}
	type item struct {
		DecisionRecord
		Candidates json.RawMessage `json:"candidates"`
		Why        json.RawMessage `json:"why"`
	}
	out := make([]item, 0, len(list))
	for _, d := range list {
		out = append(out, item{DecisionRecord: d, Candidates: json.RawMessage(d.Candidates), Why: json.RawMessage(d.Why)})
	}
	core.JSON(w, http.StatusOK, map[string]any{"items": out})
	return nil
}

func (m *Module) apiMeter(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	mt, err := m.svc.Meter(ctx, core.UserFrom(ctx).ID)
	if err != nil {
		return err
	}
	core.JSON(w, http.StatusOK, mt)
	return nil
}

func (m *Module) apiPins(w http.ResponseWriter, r *http.Request) error {
	pins, err := m.svc.Pins(r.Context())
	if err != nil {
		return err
	}
	if pins == nil {
		pins = []router.Pin{}
	}
	core.JSON(w, http.StatusOK, map[string]any{"items": pins})
	return nil
}

func (m *Module) apiAddPin(w http.ResponseWriter, r *http.Request) error {
	var p router.Pin
	if err := core.DecodeJSON(w, r, &p); err != nil {
		return err
	}
	ctx := r.Context()
	out, err := m.svc.AddPin(ctx, core.UserFrom(ctx), p)
	if err != nil {
		return apiError(r, err)
	}
	core.JSON(w, http.StatusCreated, out)
	return nil
}

func (m *Module) apiDeletePin(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	if err := m.svc.DeletePin(ctx, core.UserFrom(ctx), r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// apiFeedback records keep / reroll for learning (v2).
func (m *Module) apiFeedback(w http.ResponseWriter, r *http.Request) error {
	var b struct {
		ModelID    string `json:"model_id"`
		ProviderID string `json:"provider_id"`
		Task       string `json:"task"`
		Kept       bool   `json:"kept"`
	}
	if err := core.DecodeJSON(w, r, &b); err != nil {
		return err
	}
	if b.ModelID == "" {
		return core.ErrValidation(map[string]string{"model_id": "required"})
	}
	if err := m.svc.Feedback(r.Context(), b.ModelID, b.ProviderID, b.Task, b.Kept); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
