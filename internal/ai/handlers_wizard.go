package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/ai/catalog"
	"github.com/MaramHarsha/MangaVeyra/internal/ai/providers"
	"github.com/MaramHarsha/MangaVeyra/internal/ai/router"
	"github.com/MaramHarsha/MangaVeyra/internal/ai/views"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/web"
	"github.com/MaramHarsha/MangaVeyra/internal/web/ui"
)

// customKinds are the four G12 connection types, in the design's order.
var customKinds = []string{providers.KindOpenAICompatible, providers.KindComfyUI, providers.KindHuggingFace, providers.KindHTTP}

func isCustom(kind string) bool { return slices.Contains(customKinds, kind) }

// safeNext keeps "next" to local paths.
func safeNext(r *http.Request) string {
	n := r.URL.Query().Get("next")
	if n == "" {
		n = r.PostFormValue("next")
	}
	if core.SafeRedirectTarget(n, "") == "" {
		return ""
	}
	return n
}

func (m *Module) canAdd(ctx context.Context, u *core.User) bool {
	return u.IsAdmin() || m.d.Settings.MustInstance(ctx).UsersMayAddProviders
}

func (m *Module) chooser(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	u := core.UserFrom(ctx)
	next := safeNext(r)
	if k := r.URL.Query().Get("kind"); k != "" {
		switch k {
		case providers.KindLocalWorker:
			core.Redirect(w, r, "/models/workers/new")
			return nil
		case "vendor":
			k = providers.KindOpenAI
		case "custom":
			k = providers.KindOpenAICompatible
		}
		target := "/models/providers/new/connect?kind=" + k
		if next != "" {
			target += "&next=" + next
		}
		core.Redirect(w, r, target)
		return nil
	}
	if !m.canAdd(ctx, u) {
		return ErrProvidersNotAllowed
	}
	var named, custom []views.KindChoice
	ordered := make([]KindInfo, 0, len(Kinds))
	for _, k := range Kinds {
		if !isCustom(k.Kind) {
			ordered = append(ordered, k)
		}
	}
	for _, ck := range customKinds {
		if k, ok := KindInfoFor(ck); ok {
			ordered = append(ordered, k)
		}
	}
	for _, k := range ordered {
		c := views.KindChoice{Kind: k.Kind, Name: m.svc.KindName(ctx, k.Kind), Desc: i18n.T(ctx, "ai.kinddesc."+k.Kind), Icon: k.Icon,
			Href: "/models/providers/new/connect?kind=" + k.Kind}
		if next != "" {
			c.Href += "&next=" + next
		}
		if isCustom(k.Kind) {
			c.Name = i18n.T(ctx, "ai.conn."+k.Kind)
			custom = append(custom, c)
		} else {
			named = append(named, c)
		}
	}
	return m.page(w, r, http.StatusOK, i18n.T(ctx, "ai.chooser.title"), i18n.T(ctx, "ai.chooser.mobile"), "/models",
		views.ChooserPage(named, custom, u.IsAdmin(), next))
}

// wizardView builds the common part of the G12 view.
func (m *Module) wizardView(ctx context.Context, u *core.User, p *Provider, kind, step, next string) views.WizardView {
	info, _ := KindInfoFor(kind)
	v := views.WizardView{Step: step, Kind: kind, KindName: m.svc.KindName(ctx, kind), Custom: isCustom(kind), Next: next, Errors: map[string]string{},
		NeedsURL: info.NeedsURL, NeedsKey: info.NeedsKey, KeyURL: info.KeyURL}
	if p != nil {
		v.ID, v.Name, v.BaseURL, v.KeyHint = p.ID, p.Name, p.BaseURL, p.KeyHint
		v.ModelID = p.Config.V.ModelID
	} else if info.DefaultURL != "" && info.NeedsURL {
		v.BaseURL = info.DefaultURL
	}
	if v.Custom {
		v.Title = i18n.T(ctx, "ai.wizard.title_custom")
	} else {
		v.Title = i18n.T(ctx, "ai.wizard.title_named", "name", v.KindName)
	}
	idx := slices.Index(views.Steps, step)
	v.StepIdx = idx
	v.Sub = i18n.T(ctx, "ai.wizard.step", "n", idx+1, "total", len(views.Steps), "name", i18n.T(ctx, "ai.wizard.step."+step))
	for i, s := range views.Steps {
		st := ui.Step{Label: fmt.Sprintf("%d. %s", i+1, i18n.T(ctx, "ai.wizard.step."+s)), Done: i < idx, Current: i == idx}
		v.Steps = append(v.Steps, st)
	}
	for _, k := range customKinds {
		href := "/models/providers/new/connect?kind=" + k
		if next != "" {
			href += "&next=" + next
		}
		v.Types = append(v.Types, views.KindChoice{Kind: k, Name: i18n.T(ctx, "ai.conn."+k), Href: href, Current: k == kind})
	}
	if u.IsAdmin() {
		v.ScopeOptions = []ui.Option{{Value: ScopeServer, Label: i18n.T(ctx, "ai.scope.server")}, {Value: ScopeUser, Label: i18n.T(ctx, "ai.scope.user")}}
		v.Scope = ScopeServer
	} else {
		v.ScopeOptions = []ui.Option{{Value: ScopeUser, Label: i18n.T(ctx, "ai.scope.user")}}
		v.Scope = ScopeUser
	}
	if p != nil {
		v.Scope = p.Scope
	}
	switch kind {
	case providers.KindOllama:
		v.URLHint = i18n.T(ctx, "ai.wizard.url_hint.ollama")
	case providers.KindComfyUI:
		v.URLHint = i18n.T(ctx, "ai.wizard.url_hint.comfyui")
	case providers.KindOpenAICompatible:
		v.URLHint = i18n.T(ctx, "ai.wizard.url_hint.compat")
	case providers.KindHuggingFace:
		v.URLHint = i18n.T(ctx, "ai.wizard.url_hint.hf")
	case providers.KindHTTP:
		v.URLHint = i18n.T(ctx, "ai.wizard.url_hint.http")
	}
	// Kind-specific options.
	if p != nil {
		switch kind {
		case providers.KindComfyUI:
			var o providers.ComfyOptions
			if json.Unmarshal(p.Config.V.Options, &o) == nil && len(o.Workflow) > 0 {
				v.WorkflowName = o.WorkflowName
				if wf, err := providers.ParseWorkflow(o.Workflow); err == nil {
					v.WorkflowNodes = len(wf.Nodes)
					v.Mapping = mappingRows(ctx, wf, o.Mapping)
				}
			}
		case providers.KindHTTP:
			if o, err := providers.ParseHTTPOptions(p.Config.V.Options); err == nil {
				v.BodyTemplate, v.ResultPath, v.ResultType, v.Mode = o.BodyTemplate, o.ResultPath, o.ResultType, o.Mode
				v.IDPath, v.PollURL, v.StatusPath, v.AuthHeader, v.AuthPrefix = o.IDPath, o.PollURL, o.StatusPath, o.AuthHeader, o.AuthPrefix
				v.DoneValues = strings.Join(o.DoneValues, ", ")
			}
		}
	}
	if kind == providers.KindHTTP && v.BodyTemplate == "" {
		v.BodyTemplate, v.ResultType, v.Mode = providers.DefaultHTTPTemplate, "auto", "sync"
	}
	return v
}

// primaryMapFields are shown first; the rest sit under "More inputs".
var primaryMapFields = []string{providers.MapPrompt, providers.MapNegative, providers.MapRef, providers.MapPose, providers.MapSeed}

// mappingRows builds the ComfyUI "Map inputs" selects.
func mappingRows(ctx context.Context, wf *providers.Workflow, mapping map[string]providers.NodeInput) []views.MapRow {
	if len(mapping) == 0 {
		mapping = wf.GuessMapping()
	}
	var rows []views.MapRow
	for _, f := range providers.MapFields {
		opts := []ui.Option{{Value: "", Label: i18n.T(ctx, "ai.map.none")}}
		for _, n := range wf.Nodes {
			for _, in := range n.Inputs {
				opts = append(opts, ui.Option{Value: n.ID + "." + in, Label: n.Label() + " · " + in})
			}
		}
		rows = append(rows, views.MapRow{Field: f, Label: i18n.T(ctx, "ai.map."+f), Value: mapping[f].String(), Options: opts,
			More: !slices.Contains(primaryMapFields, f)})
	}
	return rows
}

func (m *Module) loadOwned(ctx context.Context, u *core.User, id string) (*Provider, error) {
	if id == "" {
		return nil, nil
	}
	p, err := m.svc.GetProvider(ctx, id)
	if err != nil {
		return nil, err
	}
	if !CanManage(u, p) {
		return nil, core.ErrNotFound()
	}
	return p, nil
}

func (m *Module) wizard(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	u := core.UserFrom(ctx)
	step := r.PathValue("step")
	if !slices.Contains(views.Steps, step) {
		return core.ErrNotFound()
	}
	p, err := m.loadOwned(ctx, u, r.URL.Query().Get("id"))
	if err != nil {
		return err
	}
	if p == nil && !m.canAdd(ctx, u) {
		return ErrProvidersNotAllowed
	}
	kind := r.URL.Query().Get("kind")
	if p != nil {
		kind = p.Kind
	}
	if _, ok := KindInfoFor(kind); !ok || kind == providers.KindPlaceholder {
		kind = providers.KindOpenAICompatible
	}
	if kind == providers.KindLocalWorker {
		core.Redirect(w, r, "/models/workers/new")
		return nil
	}
	if p == nil && step != views.StepConnect {
		core.Redirect(w, r, "/models/providers/new/connect?kind="+kind)
		return nil
	}
	v := m.wizardView(ctx, u, p, kind, step, safeNext(r))
	switch step {
	case views.StepTest:
		m.fillTest(ctx, &v, p)
	case views.StepDescribe:
		m.fillDescribe(ctx, &v, p)
	case views.StepRoute:
		m.fillRoute(ctx, &v, p)
	}
	return m.page(w, r, http.StatusOK, v.Title, v.Title, "/models", views.WizardPage(v))
}

// maxWorkflowBytes caps an uploaded ComfyUI workflow.
const maxWorkflowBytes = 4 << 20

func (m *Module) connect(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	u := core.UserFrom(ctx)
	r.Body = http.MaxBytesReader(w, r.Body, maxWorkflowBytes+(1<<20))
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
		if err := r.ParseMultipartForm(maxWorkflowBytes); err != nil {
			return core.ErrBadRequest("invalid form")
		}
	} else if err := r.ParseForm(); err != nil {
		return core.ErrBadRequest("invalid form")
	}
	p, err := m.loadOwned(ctx, u, r.URL.Query().Get("id"))
	if err != nil {
		return err
	}
	kind := web.FormValue(r, "kind")
	if p != nil {
		kind = p.Kind
	}
	next := safeNext(r)
	scope := web.FormValue(r, "scope")
	if !u.IsAdmin() {
		scope = ScopeUser
	}
	in := ProviderInput{Kind: kind, Name: web.FormValue(r, "name"), BaseURL: web.FormValue(r, "base_url"), Key: r.PostFormValue("key"),
		KeepKey: true, Scope: scope, ModelID: web.FormValue(r, "model_id"), Draft: p == nil || p.Config.V.Draft}
	v := m.wizardView(ctx, u, p, kind, views.StepConnect, next)
	v.Name, v.BaseURL, v.ModelID = in.Name, in.BaseURL, in.ModelID
	fail := func(field, msg string) error {
		hidden := (field == "base_url" && !v.NeedsURL && kind != providers.KindHuggingFace) || (field == "key" && !v.NeedsKey)
		if field == "" || hidden {
			v.FormErr = msg
		} else {
			v.Errors[field] = msg
		}
		return m.page(w, r, http.StatusUnprocessableEntity, v.Title, v.Title, "/models", views.WizardPage(v))
	}
	switch kind {
	case providers.KindComfyUI:
		opts := providers.ComfyOptions{}
		if p != nil {
			_ = json.Unmarshal(p.Config.V.Options, &opts)
		}
		if f, hdr, err := r.FormFile("workflow"); err == nil {
			b, err := io.ReadAll(io.LimitReader(f, maxWorkflowBytes))
			f.Close()
			if err != nil {
				return err
			}
			wf, err := providers.ParseWorkflow(b)
			if err != nil {
				var pe *providers.Error
				msg := err.Error()
				if errors.As(err, &pe) {
					msg = pe.Message
				}
				return fail("workflow", i18n.T(ctx, "ai.error.workflow", "why", msg))
			}
			opts.Workflow, opts.WorkflowName, opts.Mapping = b, sanitizeName(hdr.Filename), wf.GuessMapping()
		}
		if len(opts.Workflow) == 0 {
			return fail("workflow", i18n.T(ctx, "ai.error.workflow_missing"))
		}
		in.Options, _ = json.Marshal(opts)
		if in.ModelID == "" {
			in.ModelID = strings.TrimSuffix(opts.WorkflowName, ".json")
		}
	case providers.KindHTTP:
		o := providers.HTTPOptions{AuthHeader: web.FormValue(r, "auth_header"), AuthPrefix: r.PostFormValue("auth_prefix"),
			BodyTemplate: strings.TrimSpace(r.PostFormValue("body_template")), ResultPath: web.FormValue(r, "result_path"),
			ResultType: web.FormValue(r, "result_type"), Mode: web.FormValue(r, "mode"), IDPath: web.FormValue(r, "id_path"),
			PollURL: web.FormValue(r, "poll_url"), StatusPath: web.FormValue(r, "status_path")}
		for _, d := range strings.Split(r.PostFormValue("done_values"), ",") {
			if d = strings.TrimSpace(d); d != "" {
				o.DoneValues = append(o.DoneValues, d)
			}
		}
		v.BodyTemplate, v.ResultPath, v.ResultType, v.Mode, v.IDPath, v.PollURL, v.StatusPath = o.BodyTemplate, o.ResultPath, o.ResultType, o.Mode, o.IDPath, o.PollURL, o.StatusPath
		v.AuthHeader, v.AuthPrefix, v.DoneValues = o.AuthHeader, o.AuthPrefix, r.PostFormValue("done_values")
		raw, _ := json.Marshal(o)
		if _, err := providers.ParseHTTPOptions(raw); err != nil {
			var pe *providers.Error
			msg := err.Error()
			if errors.As(err, &pe) {
				msg = pe.Message
			}
			field := "body_template"
			switch {
			case o.ResultPath == "":
				field = "result_path"
			case o.Mode == "poll":
				field = "poll_url"
			}
			return fail(field, msg)
		}
		if o.PollURL != "" {
			if _, err := m.svc.policyFor(kind, p != nil && p.Config.V.PrivateApproved).CheckURL(ctx, strings.ReplaceAll(o.PollURL, "{{id}}", "x"), nil); err != nil {
				return fail("poll_url", i18n.T(ctx, "ai.error.url_blocked"))
			}
		}
		in.Options = raw
		if in.ModelID == "" {
			in.ModelID = "http"
		}
	case providers.KindHuggingFace:
		if in.ModelID == "" {
			return fail("model_id", i18n.T(ctx, "ai.error.hf_model"))
		}
	}
	saved, err := m.svc.SaveProvider(ctx, u, idOf(p), in)
	if err != nil {
		var fe *FieldError
		if errors.As(err, &fe) {
			return fail(fe.Field, i18n.T(ctx, fe.Key))
		}
		var ae *core.APIError
		if errors.As(err, &ae) && ae.Status < 500 {
			return fail("", web.ErrorText(ctx, ae))
		}
		return err
	}
	target := "/models/providers/new/test?id=" + saved.ID
	if next != "" {
		target += "&next=" + next
	}
	core.Redirect(w, r, target)
	return nil
}

func idOf(p *Provider) string {
	if p == nil {
		return ""
	}
	return p.ID
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._ -]+`)

func sanitizeName(s string) string {
	s = unsafeName.ReplaceAllString(s, "")
	if len(s) > 80 {
		s = s[:80]
	}
	return s
}

func (m *Module) saveMapping(w http.ResponseWriter, r *http.Request) error {
	if err := web.ParseForm(w, r); err != nil {
		return core.ErrBadRequest("invalid form")
	}
	ctx := r.Context()
	p, err := m.loadOwned(ctx, core.UserFrom(ctx), r.URL.Query().Get("id"))
	if err != nil || p == nil {
		return core.ErrNotFound()
	}
	var o providers.ComfyOptions
	if err := json.Unmarshal(p.Config.V.Options, &o); err != nil {
		return core.ErrBadRequest("no workflow")
	}
	wf, err := providers.ParseWorkflow(o.Workflow)
	if err != nil {
		return core.ErrBadRequest("invalid workflow")
	}
	o.Mapping = map[string]providers.NodeInput{}
	for _, f := range providers.MapFields {
		ni := providers.ParseNodeInput(r.PostFormValue("map_" + f))
		if ni.Node != "" && wf.Has(ni) {
			o.Mapping[f] = ni
		}
	}
	raw, _ := json.Marshal(o)
	if err := m.svc.UpdateProviderConfig(ctx, p.ID, func(c *ProviderConfig) { c.Options = raw }); err != nil {
		return err
	}
	return m.done(w, r, i18n.T(ctx, "ai.wizard.map_saved"), "/models/providers/new/test?id="+p.ID+"&next="+safeNext(r))
}

// fillTest adds the connection summary and the last test.
func (m *Module) fillTest(ctx context.Context, v *views.WizardView, p *Provider) {
	if !v.Custom {
		v.Summary = append(v.Summary, [2]string{i18n.T(ctx, "ai.wizard.type"), v.KindName})
	}
	if p.BaseURL != "" {
		v.Summary = append(v.Summary, [2]string{urlLabelText(ctx, p.Kind), p.BaseURL})
	}
	if p.HasKey() {
		v.Summary = append(v.Summary, [2]string{i18n.T(ctx, "ai.wizard.key"), "•••• " + p.KeyHint})
	}
	if p.Config.V.ModelID != "" {
		v.Summary = append(v.Summary, [2]string{i18n.T(ctx, "ai.wizard.model_id"), p.Config.V.ModelID})
	}
	scope := i18n.T(ctx, "ai.scope."+p.Scope)
	v.Summary = append(v.Summary, [2]string{i18n.T(ctx, "ai.wizard.scope"), scope})
	if p.Config.V.Local {
		v.Summary = append(v.Summary, [2]string{i18n.T(ctx, "ai.wizard.network"), i18n.T(ctx, "ai.wizard.network_local")})
	}
	if p.Kind == providers.KindComfyUI && v.WorkflowName != "" {
		v.Summary = append(v.Summary, [2]string{i18n.T(ctx, "ai.wizard.workflow"), v.WorkflowName + " · " + i18n.T(ctx, "ai.wizard.nodes", "count", v.WorkflowNodes)})
	}
	v.Test = m.testView(ctx, p)
	v.BothModes = m.svc.public.Can(p.Kind, "text") && m.svc.public.Can(p.Kind, "image") && !isCustom(p.Kind)
	v.Manage = !p.Config.V.Draft
	v.Enabled = p.Enabled
	u := core.UserFrom(ctx)
	v.NeedsApproval = u.IsAdmin() && !p.Config.V.PrivateApproved && localCapable(p.Kind) && p.Kind != providers.KindHTTP && p.Kind != providers.KindHuggingFace
}

func urlLabelText(ctx context.Context, kind string) string {
	switch kind {
	case providers.KindComfyUI:
		return i18n.T(ctx, "ai.wizard.url.comfyui")
	case providers.KindHuggingFace:
		return i18n.T(ctx, "ai.wizard.url.huggingface")
	case providers.KindHTTP:
		return i18n.T(ctx, "ai.wizard.url.http")
	}
	return i18n.T(ctx, "ai.wizard.url")
}

// testView renders the last test of p.
func (m *Module) testView(ctx context.Context, p *Provider) views.TestView {
	t := p.LastTest.V
	tv := views.TestView{Placehold: i18n.T(ctx, "ai.test.placeholder", "prompt", TestImagePrompt)}
	if t.At.IsZero() {
		tv.Status, tv.Time, tv.Cost = i18n.T(ctx, "ai.test.not_run"), "—", "—"
		return tv
	}
	tv.Ran, tv.OK = true, t.OK
	if t.OK {
		tv.Status = i18n.T(ctx, "ai.test.success")
	} else {
		tv.Status = i18n.T(ctx, "ai.test.failed")
		tv.Message = t.Message
		if t.Code == providers.CodeBadRequest && strings.Contains(t.Message, "address not allowed") {
			tv.Message = i18n.T(ctx, "ai.error.url_private")
		}
	}
	tv.Time = i18n.T(ctx, "ai.test.seconds", "n", strconv.FormatFloat(t.Seconds, 'f', 1, 64))
	switch {
	case t.Local:
		tv.Cost = i18n.T(ctx, "ai.test.local_gpu")
	case t.CostUSD > 0:
		tv.Cost = USD(t.CostUSD)
	default:
		tv.Cost = i18n.T(ctx, "ai.test.not_reported")
	}
	if t.ImageKey != "" {
		tv.ImageURL = m.d.Media.URL(t.ImageKey, time.Hour)
		tv.Caption = i18n.T(ctx, "ai.test.caption", "prompt", t.Prompt)
	}
	if t.Text != "" {
		tv.Text = t.Text
	}
	if t.OK && t.Model != "" {
		tv.Message = i18n.T(ctx, "ai.test.model", "model", t.Model)
	}
	return tv
}

func (m *Module) runTest(w http.ResponseWriter, r *http.Request) error {
	if err := web.ParseForm(w, r); err != nil {
		return core.ErrBadRequest("invalid form")
	}
	ctx := r.Context()
	u := core.UserFrom(ctx)
	p, err := m.loadOwned(ctx, u, r.URL.Query().Get("id"))
	if err != nil || p == nil {
		return core.ErrNotFound()
	}
	if _, err := m.svc.TestProvider(ctx, u, p.ID, r.PostFormValue("mode")); err != nil {
		return err
	}
	p, _ = m.svc.GetProvider(ctx, p.ID)
	if core.IsHTMX(r) {
		v := m.wizardView(ctx, u, p, p.Kind, views.StepTest, safeNext(r))
		v.Test = m.testView(ctx, p)
		return web.Fragment(w, r, http.StatusOK, views.TestCard(v))
	}
	core.Redirect(w, r, "/models/providers/new/test?id="+p.ID+"&next="+safeNext(r))
	return nil
}

// fillDescribe lists served catalogue models and discovered models, or the custom model form.
func (m *Module) fillDescribe(ctx context.Context, v *views.WizardView, p *Provider) {
	s := m.svc
	c := s.Catalog(ctx)
	for _, man := range c.All() {
		if man.Source == catalog.SourceCustom {
			continue
		}
		if o, ok := man.Offer(p.Kind); ok && o.ProviderID == "" {
			v.Served = append(v.Served, views.DescribedModel{ID: man.ID, Name: man.Name, Detail: o.ModelID, InCat: true})
		}
	}
	existing := map[string]bool{}
	for _, cm := range s.CustomModelsOf(ctx, p.ID) {
		existing[cm.ID] = true
	}
	if lister, ok := s.registryFor(p).Get(p.Kind); ok {
		info, _ := KindInfoFor(p.Kind)
		keyed := p.HasKey() || !info.NeedsKey || info.NeedsURL
		if ml, ok := lister.(providers.ModelLister); ok && keyed && !p.Builtin && (!isCustom(p.Kind) || p.Kind == providers.KindOpenAICompatible) {
			cfg, err := s.adapterConfig(p)
			if err == nil {
				lctx, cancel := context.WithTimeout(ctx, 20*time.Second)
				models, err := ml.Models(lctx, cfg)
				cancel()
				if err != nil {
					v.DiscoverError = i18n.T(ctx, "ai.describe.discover_failed", "why", err.Error())
				} else {
					_ = s.UpdateProviderConfig(ctx, p.ID, func(pc *ProviderConfig) { pc.Models = models })
					served := map[string]bool{}
					for _, sm := range v.Served {
						served[sm.Detail] = true
					}
					for _, rm := range models {
						if served[rm.ID] || (rm.Kind != "image" && rm.Kind != "text") {
							continue
						}
						detail := i18n.T(ctx, "ai.kindtab."+rm.Kind)
						if rm.PriceUSD > 0 {
							switch rm.PriceUnit {
							case catalog.PerImage:
								detail += " · " + i18n.T(ctx, "ai.cost.per_image", "usd", USD(rm.PriceUSD))
							case catalog.Per1kTokens:
								detail += " · " + i18n.T(ctx, "ai.cost.per_mtok", "in", USD(rm.PriceUSD*1000), "out", USD(rm.PriceUSDOut*1000))
							}
						}
						v.Discovered = append(v.Discovered, views.DescribedModel{ID: rm.ID, Name: firstNonEmpty(rm.Name, rm.ID), Detail: detail,
							Checked: existing[customID(p, rm.ID)]})
						if len(v.Discovered) >= 60 {
							break
						}
					}
				}
			}
		}
	}
	if !v.Custom {
		return
	}
	// Custom connection: the model form, prefilled from an existing custom model or discovery.
	v.KindOptions = []ui.Option{}
	for _, k := range catalog.Kinds {
		v.KindOptions = append(v.KindOptions, ui.Option{Value: k, Label: i18n.T(ctx, "ai.kindtab."+k)})
	}
	var cur *catalog.Manifest
	if list := s.CustomModelsOf(ctx, p.ID); len(list) > 0 {
		cur = list[0]
	}
	if cur == nil {
		cur = &catalog.Manifest{ID: slug(p.Name), Name: p.Name, Kind: catalog.KindImage,
			Capabilities: catalog.Capabilities{Tasks: []string{catalog.TaskGenerate}, BWScreentone: true, Colour: true},
			Licence:      catalog.Licence{ID: "", Commercial: false}}
		if p.Kind == providers.KindOpenAICompatible && !strings.Contains(strings.ToLower(p.Config.V.ModelID), "image") {
			cur.Kind = catalog.KindText
			cur.Capabilities.Tasks = []string{catalog.TaskText, catalog.TaskTranslate}
		}
		if p.Kind == providers.KindHuggingFace {
			if hf, ok := s.registryFor(p).Get(p.Kind); ok {
				cfg, _ := s.adapterConfig(p)
				hctx, cancel := context.WithTimeout(ctx, 15*time.Second)
				if info, err := hf.(providers.HuggingFace).ModelInfo(hctx, cfg, p.Config.V.ModelID); err == nil {
					cur.Licence.ID = info.License
					cur.Licence.Commercial = permissive(info.License)
					if info.Kind != "" {
						cur.Kind = info.Kind
					}
					if info.InputImages {
						cur.Capabilities.Tasks = append(cur.Capabilities.Tasks, catalog.TaskEdit)
						cur.Capabilities.Edit = true
					}
				}
				cancel()
			}
		}
		if p.Kind == providers.KindComfyUI {
			var o providers.ComfyOptions
			if json.Unmarshal(p.Config.V.Options, &o) == nil {
				for f := range o.Mapping {
					switch f {
					case providers.MapRef:
						cur.Capabilities.Refs, cur.Capabilities.MaxRefs = true, 1
					case providers.MapPose:
						cur.Capabilities.Pose = true
					case providers.MapMask:
						cur.Capabilities.Inpaint = true
						cur.Capabilities.Tasks = append(cur.Capabilities.Tasks, catalog.TaskInpaint)
					case providers.MapImage:
						cur.Capabilities.Edit = true
						cur.Capabilities.Tasks = append(cur.Capabilities.Tasks, catalog.TaskEdit)
					}
				}
			}
		}
	}
	v.DescName, v.DescModelID, v.DescKind = cur.Name, cur.ID, cur.Kind
	v.LicenceID, v.Commercial = cur.Licence.ID, cur.Licence.Commercial
	if cur.Capabilities.MaxRefs > 0 {
		v.MaxRefs = strconv.Itoa(cur.Capabilities.MaxRefs)
	}
	if cur.Capabilities.MaxResolution > 0 {
		v.MaxRes = strconv.Itoa(cur.Capabilities.MaxResolution)
	}
	if p.Config.V.CostPerImage > 0 {
		v.CostPerImage = strconv.FormatFloat(p.Config.V.CostPerImage, 'f', -1, 64)
	}
	for _, t := range catalog.Tasks {
		v.TaskFields = append(v.TaskFields, views.CapField{Name: t, Label: i18n.T(ctx, "ai.task."+t), Checked: cur.Supports(t)})
	}
	caps := cur.Capabilities
	for _, f := range []struct {
		name string
		on   bool
	}{{"refs", caps.Refs}, {"lora", caps.LoRA}, {"edit", caps.Edit}, {"inpaint", caps.Inpaint}, {"layered", caps.Layered},
		{"text_in_art", caps.TextInArt}, {"bw_screentone", caps.BWScreentone}, {"colour", caps.Colour}, {"tall_strips", caps.TallStrips}, {"pose", caps.Pose}} {
		v.CapFields = append(v.CapFields, views.CapField{Name: f.name, Label: i18n.T(ctx, "ai.cap."+f.name), Checked: f.on})
	}
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// permissive reports licences that allow commercial use of outputs.
func permissive(id string) bool {
	switch strings.ToLower(id) {
	case "apache-2.0", "mit", "bsd-3-clause", "bsd-2-clause", "openrail++", "creativeml-openrail-m", "openrail", "cc-by-4.0", "cc0-1.0", "unlicense":
		return true
	}
	return false
}

var slugRe = regexp.MustCompile(`[^a-z0-9._-]+`)

func slug(s string) string {
	s = strings.Trim(slugRe.ReplaceAllString(strings.ToLower(s), "-"), "-.")
	if s == "" {
		s = "custom"
	}
	if len(s) > 60 {
		s = s[:60]
	}
	return s
}

// customID is the catalogue id of a discovered model added from provider p.
func customID(p *Provider, remoteID string) string {
	id := slug(strings.ReplaceAll(remoteID, "/", "-"))
	return slug(p.Kind) + "-" + id
}

func (m *Module) describe(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	u := core.UserFrom(ctx)
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
		if err := r.ParseMultipartForm(2 << 20); err != nil {
			return core.ErrBadRequest("invalid form")
		}
	} else if err := r.ParseForm(); err != nil {
		return core.ErrBadRequest("invalid form")
	}
	p, err := m.loadOwned(ctx, u, r.URL.Query().Get("id"))
	if err != nil || p == nil {
		return core.ErrNotFound()
	}
	next := safeNext(r)
	s := m.svc
	// Discovered models the member ticked become custom catalogue models.
	chosen := r.PostForm["add_model"]
	for _, rm := range p.Config.V.Models {
		if !slices.Contains(chosen, rm.ID) {
			continue
		}
		man := manifestFromRemote(p, rm)
		if err := s.SaveCustomModel(ctx, u, p.ID, man); err != nil {
			return err
		}
	}
	if isCustom(p.Kind) {
		v := m.wizardView(ctx, u, p, p.Kind, views.StepDescribe, next)
		man, field, msg := m.manifestFromForm(ctx, r, p)
		if field != "" {
			m.fillDescribe(ctx, &v, p)
			v.Errors[field] = msg
			v.DescName, v.DescModelID = r.PostFormValue("model_name"), r.PostFormValue("catalog_id")
			return m.page(w, r, http.StatusUnprocessableEntity, v.Title, v.Title, "/models", views.WizardPage(v))
		}
		if err := s.SaveCustomModel(ctx, u, p.ID, man); err != nil {
			var fe *FieldError
			if errors.As(err, &fe) {
				m.fillDescribe(ctx, &v, p)
				v.Errors[fe.Field] = i18n.T(ctx, fe.Key)
				return m.page(w, r, http.StatusUnprocessableEntity, v.Title, v.Title, "/models", views.WizardPage(v))
			}
			m.fillDescribe(ctx, &v, p)
			v.Errors["manifest"] = err.Error()
			return m.page(w, r, http.StatusUnprocessableEntity, v.Title, v.Title, "/models", views.WizardPage(v))
		}
		if cost, ok := parseUSD(r.PostFormValue("cost_per_image")); ok {
			_ = s.UpdateProviderConfig(ctx, p.ID, func(c *ProviderConfig) { c.CostPerImage = cost })
		}
	}
	core.Redirect(w, r, "/models/providers/new/route?id="+p.ID+"&next="+next)
	return nil
}

// manifestFromRemote describes a discovered model (capabilities from discovery).
func manifestFromRemote(p *Provider, rm providers.RemoteModel) *catalog.Manifest {
	man := &catalog.Manifest{ID: customID(p, rm.ID), Name: firstNonEmpty(rm.Name, rm.ID), Maker: p.Name, Kind: rm.Kind, Summary: rm.Description,
		Licence:  catalog.Licence{ID: "provider-terms", Commercial: true, Note: "Terms of the provider; verify before commercial use."},
		Hardware: catalog.Hardware{Local: p.Local()}, Verify: "Described from the provider's model list; verify before use."}
	offer := catalog.Offer{Provider: p.Kind, ProviderID: p.ID, ModelID: rm.ID, Notes: "discovered; verify before use"}
	if rm.PriceUSD > 0 {
		offer.Price = catalog.Price{Unit: rm.PriceUnit, USD: rm.PriceUSD, USDOut: rm.PriceUSDOut}
	}
	switch rm.Kind {
	case catalog.KindImage:
		man.Capabilities = catalog.Capabilities{Tasks: []string{catalog.TaskGenerate}, BWScreentone: true, Colour: true, TextInArt: true}
		if rm.InputImages {
			man.Capabilities.Tasks = append(man.Capabilities.Tasks, catalog.TaskEdit, catalog.TaskSheet)
			man.Capabilities.Edit, man.Capabilities.Refs, man.Capabilities.MaxRefs = true, true, 4
		}
		offer.Seconds = 15
	default:
		man.Kind = catalog.KindText
		man.Capabilities = catalog.Capabilities{Tasks: []string{catalog.TaskText, catalog.TaskTranslate}, Context: rm.Context}
		offer.Seconds = 10
	}
	man.Providers = []catalog.Offer{offer}
	return man
}

// manifestFromForm builds the custom model manifest from the describe form (or an imported manifest).
func (m *Module) manifestFromForm(ctx context.Context, r *http.Request, p *Provider) (*catalog.Manifest, string, string) {
	offer := catalog.Offer{Provider: p.Kind, ProviderID: p.ID, ModelID: firstNonEmpty(p.Config.V.ModelID, "default"), Seconds: 20}
	if cost, ok := parseUSD(r.PostFormValue("cost_per_image")); ok && cost > 0 {
		offer.Price = catalog.Price{Unit: catalog.PerImage, USD: cost}
	}
	if raw := strings.TrimSpace(r.PostFormValue("manifest")); raw != "" {
		man, err := catalog.Parse([]byte(raw))
		if err != nil {
			return nil, "manifest", i18n.T(ctx, "ai.error.manifest", "why", err.Error())
		}
		// Imported manifests are served by this provider.
		man.Providers = []catalog.Offer{offer}
		return man, "", ""
	}
	name := web.FormValue(r, "model_name")
	id := strings.ToLower(web.FormValue(r, "catalog_id"))
	if name == "" {
		return nil, "name", i18n.T(ctx, "ai.error.name")
	}
	if !catalog.ValidID(id) {
		return nil, "model_id", i18n.T(ctx, "ai.error.model_id")
	}
	kind := web.FormValue(r, "model_kind")
	if !slices.Contains(catalog.Kinds, kind) {
		kind = catalog.KindImage
	}
	tasks := r.PostForm["task"]
	var valid []string
	for _, t := range tasks {
		if slices.Contains(catalog.Tasks, t) {
			valid = append(valid, t)
		}
	}
	if len(valid) == 0 {
		return nil, "tasks", i18n.T(ctx, "ai.error.tasks")
	}
	caps := r.PostForm["cap"]
	has := func(c string) bool { return slices.Contains(caps, c) }
	maxRefs, _ := strconv.Atoi(web.FormValue(r, "max_refs"))
	maxRes, _ := strconv.Atoi(web.FormValue(r, "max_resolution"))
	lic := strings.TrimSpace(web.FormValue(r, "licence_id"))
	if lic == "" {
		lic = "unknown"
	}
	man := &catalog.Manifest{ID: id, Name: name, Maker: p.Name, Kind: kind, Summary: p.Config.V.Note,
		Capabilities: catalog.Capabilities{Tasks: valid, Refs: has("refs"), MaxRefs: max(0, min(maxRefs, 16)), LoRA: has("lora"), Edit: has("edit"),
			Inpaint: has("inpaint"), Layered: has("layered"), TextInArt: has("text_in_art"), BWScreentone: has("bw_screentone"), Colour: has("colour"),
			TallStrips: has("tall_strips"), Pose: has("pose"), MaxResolution: max(0, min(maxRes, 16384))},
		Licence:   catalog.Licence{ID: lic, Commercial: web.FormBool(r, "commercial")},
		Hardware:  catalog.Hardware{Local: p.Local()},
		Providers: []catalog.Offer{offer},
		Verify:    "Described by a member on the add-provider screen.",
	}
	return man, "", ""
}

func (m *Module) fillRoute(ctx context.Context, v *views.WizardView, p *Provider) {
	s := m.svc
	for _, man := range s.CustomModelsOf(ctx, p.ID) {
		v.RouteModels = append(v.RouteModels, man.Name)
	}
	if len(v.RouteModels) == 0 {
		for _, man := range s.Catalog(ctx).All() {
			if o, ok := man.Offer(p.Kind); ok && o.ProviderID == "" {
				v.RouteModels = append(v.RouteModels, man.Name)
			}
		}
		if len(v.RouteModels) > 12 {
			more := len(v.RouteModels) - 12
			v.RouteModels = append(v.RouteModels[:12], i18n.T(ctx, "ai.route.more", "count", more))
		}
	}
	if isCustom(p.Kind) {
		v.Explain = i18n.T(ctx, "ai.route.neutral")
	} else {
		v.Explain = i18n.T(ctx, "ai.route.catalogue")
	}
	u := core.UserFrom(ctx)
	if u.IsAdmin() && isCustom(p.Kind) {
		v.PinScopes = []ui.Option{{Value: "", Label: i18n.T(ctx, "ai.route.no_pin")}, {Value: router.PinServer, Label: i18n.T(ctx, "ai.pin.scope.server")}}
		v.PinTasks = []ui.Option{{Value: "", Label: i18n.T(ctx, "ai.pin.any_task")}}
		for _, t := range catalog.Tasks {
			v.PinTasks = append(v.PinTasks, ui.Option{Value: t, Label: i18n.T(ctx, "ai.task."+t)})
		}
	}
}

func (m *Module) finishRoute(w http.ResponseWriter, r *http.Request) error {
	if err := web.ParseForm(w, r); err != nil {
		return core.ErrBadRequest("invalid form")
	}
	ctx := r.Context()
	u := core.UserFrom(ctx)
	p, err := m.loadOwned(ctx, u, r.URL.Query().Get("id"))
	if err != nil || p == nil {
		return core.ErrNotFound()
	}
	if err := m.svc.UpdateProviderConfig(ctx, p.ID, func(c *ProviderConfig) { c.Draft = false }); err != nil {
		return err
	}
	if scope := r.PostFormValue("pin_scope"); scope == router.PinServer && u.IsAdmin() {
		if list := m.svc.CustomModelsOf(ctx, p.ID); len(list) > 0 {
			if _, err := m.svc.AddPin(ctx, u, router.Pin{ScopeKind: router.PinServer, Task: r.PostFormValue("pin_task"), ModelID: list[0].ID, ProviderID: p.ID}); err != nil {
				return err
			}
		}
	}
	target := safeNext(r)
	if target == "" {
		target = "/models"
	}
	web.Flash(w, r, "success", i18n.T(ctx, "ai.route.done", "name", p.Name), "")
	core.Redirect(w, r, target)
	return nil
}
