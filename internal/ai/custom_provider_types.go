package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/secrets"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
	"github.com/MaramHarsha/MangaVeyra/internal/web"
	"github.com/MaramHarsha/MangaVeyra/internal/web/ui"
)

// G12 connection types (design order). "compatible" is the OpenAI-compatible
// local text server of Phase 1; the others are ADR 0013 §9 connections.
const (
	customTypeCompatible = "compatible"
	customTypeComfyUI    = "comfyui"
	customTypeHF         = "huggingface"
	customTypeHTTP       = "http"
)

var customTypeOrder = []string{customTypeCompatible, customTypeComfyUI, customTypeHF, customTypeHTTP}

func customTypeKind(typ string) string {
	switch typ {
	case customTypeComfyUI:
		return providerKindComfyUI
	case customTypeHF:
		return providerKindHuggingFace
	case customTypeHTTP:
		return providerKindHTTP
	}
	return providerKindLocalText
}

func customKindType(kind string) string {
	switch kind {
	case providerKindComfyUI:
		return customTypeComfyUI
	case providerKindHuggingFace:
		return customTypeHF
	case providerKindHTTP:
		return customTypeHTTP
	}
	return customTypeCompatible
}

func customTypeFromQuery(raw string) string {
	for _, t := range customTypeOrder {
		if raw == t {
			return t
		}
	}
	return customTypeCompatible
}

// customMapSlots are G12's "Map inputs" rows, in design order.
var customMapSlots = []string{"prompt", "negative", "reference", "pose", "seed", "width", "height", "batch"}

// customTypedView holds the fields of the ComfyUI, Hugging Face and HTTP
// connection types.
type customTypedView struct {
	Type string
	// ComfyUI: the workflow's file name and node count, the input map and
	// the choices for each map row (step 1 when editing a draft).
	WorkflowName string
	Nodes        int
	MapValues    map[string]string
	MapOptions   map[string][]ui.Option
	// Image description (step 3).
	MaxWidth, MaxHeight, MaxCount int
	Commercial                    bool
	// Hugging Face.
	HFEndpoint, Task string
	// HTTP.
	Template, Mode, ImagePath, ImageKind, StatusURLPath, StatusPath, Done, Failed, CheckURL string
	// CostLabel is the step 2 Cost tile.
	CostLabel string
}

const defaultHTTPTemplate = `{"prompt": "{{prompt}}", "negative_prompt": "{{negative}}", "width": "{{width}}", "height": "{{height}}", "seed": "{{seed}}"}`

// typeDefaults fills a new connection of the chosen type.
func (v *customProviderView) typeDefaults(ctx context.Context) {
	v.MaxWidth, v.MaxHeight, v.MaxCount = defaultImageSpec.MaxWidth, defaultImageSpec.MaxHeight, defaultImageSpec.MaxCount
	switch v.Type {
	case customTypeComfyUI:
		v.Name, v.Endpoint = "ComfyUI", "http://localhost:8188"
	case customTypeHF:
		v.Name, v.Endpoint, v.HFEndpoint, v.Task = "Hugging Face", "", hfServerless, hfTaskText
		v.Model = "Qwen/Qwen3-32B:together"
		v.Modes = hfDefaultModes
	case customTypeHTTP:
		v.Name, v.Endpoint = "Image server", "http://localhost:7860/generate"
		v.Template, v.Mode, v.ImagePath, v.ImageKind = defaultHTTPTemplate, httpModeSync, "images.0", httpBase64
	}
	v.CostLabel = customCostLabel(ctx, v)
}

// customCostLabel is the step 2 Cost tile: local servers cost GPU time, a
// Hugging Face serverless connection its per-call bound, a dedicated
// endpoint its hourly rate (never per call).
func customCostLabel(ctx context.Context, v *customProviderView) string {
	switch v.Type {
	case customTypeComfyUI:
		return i18n.T(ctx, "aiimage.custom.cost_gpu")
	case customTypeHF:
		if v.HFEndpoint == hfDedicated {
			return i18n.T(ctx, "models.cost.endpoint_hours")
		}
		return i18n.T(ctx, "models.cost.listed")
	}
	return i18n.T(ctx, "aiimage.custom.cost_local")
}

// withTypedRecord fills the typed fields from a stored draft.
func (v *customProviderView) withTypedRecord(ctx context.Context, r providerRecord) {
	v.Type = customKindType(r.Kind)
	switch r.Kind {
	case providerKindComfyUI:
		cfg, err := decodeComfyConfig(r.Config)
		if err != nil {
			return
		}
		v.WorkflowName, v.Modes = cfg.WorkflowName, cfg.routeModes()
		v.setImage(cfg.Image)
		if nodes, err := parseComfyWorkflow(cfg.Workflow); err == nil {
			v.Nodes = len(nodes)
			v.MapOptions = comfyMapOptions(nodes)
		}
		v.MapValues = comfyMapValues(cfg.Map)
	case providerKindHuggingFace:
		cfg, err := decodeHFConfig(r.Config)
		if err != nil {
			return
		}
		v.HFEndpoint, v.Task, v.Model, v.Modes = cfg.Endpoint, cfg.Task, cfg.Model, cfg.routeModes()
		v.Context, v.Output = cfg.ContextTokens, cfg.OutputTokens
		v.setImage(cfg.Image)
		if cfg.Endpoint == hfServerless {
			v.Endpoint = ""
		}
		v.CostLabel = customCostLabel(ctx, v)
		if route, ok := hfTextRoute(cfg); ok && cfg.Endpoint == hfServerless {
			v.CostLabel = i18n.T(ctx, "models.cost.per_call", "amount", formatUSD(ceilCents(route.ReserveUSD)))
		}
		return
	case providerKindHTTP:
		cfg, err := decodeHTTPConfig(r.Config)
		if err != nil {
			return
		}
		v.Template, v.Mode, v.ImagePath, v.ImageKind = string(cfg.Request), cfg.Mode, cfg.ImagePath, cfg.ImageKind
		v.StatusURLPath, v.StatusPath, v.Done, v.Failed, v.CheckURL, v.Modes = cfg.StatusURLPath, cfg.StatusPath, cfg.Done, cfg.Failed, cfg.CheckURL, cfg.routeModes()
		v.setImage(cfg.Image)
	}
	v.CostLabel = customCostLabel(ctx, v)
}

func (v *customProviderView) setImage(s customImageSpec) {
	v.MaxWidth, v.MaxHeight, v.MaxCount, v.Commercial = s.MaxWidth, s.MaxHeight, s.MaxCount, s.Commercial
}

func ceilCents(usd float64) float64 {
	cents := int64(usd*100 + 0.999999)
	return float64(cents) / 100
}

func comfyMapValues(m comfyMap) map[string]string {
	out := map[string]string{"prompt": m.Prompt.Node + "." + m.Prompt.Input}
	for slot, in := range map[string]*comfyInput{"negative": m.Negative, "reference": m.Reference, "pose": m.Pose, "seed": m.Seed,
		"width": m.Width, "height": m.Height, "batch": m.Batch} {
		if in.set() {
			out[slot] = in.Node + "." + in.Input
		}
	}
	return out
}

// comfyMapOptions lists, for each map row, the literal inputs a run can
// overwrite: text inputs for the prompts, numbers for seed and size,
// LoadImage nodes for the images.
func comfyMapOptions(nodes map[string]comfyNode) map[string][]ui.Option {
	ids := make([]string, 0, len(nodes))
	for id := range nodes {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return comfyLess(ids[i], ids[j]) })
	text, number, images := []ui.Option{}, []ui.Option{}, []ui.Option{}
	for _, id := range ids {
		n := nodes[id]
		names := make([]string, 0, len(n.Inputs))
		for name := range n.Inputs {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			in := comfyInput{Node: id, Input: name}
			opt := ui.Option{Value: id + "." + name, Label: comfyInputLabel(n.ClassType, id, name)}
			switch {
			case n.ClassType == "LoadImage" && name == "image":
				images = append(images, opt)
			case comfyLiteral(nodes, &in, false):
				text = append(text, opt)
			case comfyLiteral(nodes, &in, true):
				number = append(number, opt)
			}
		}
	}
	none := ui.Option{Value: "", Label: "—"}
	out := map[string][]ui.Option{"prompt": text}
	for _, slot := range []string{"negative"} {
		out[slot] = append([]ui.Option{none}, text...)
	}
	for _, slot := range []string{"reference", "pose"} {
		out[slot] = append([]ui.Option{none}, images...)
	}
	for _, slot := range []string{"seed", "width", "height", "batch"} {
		out[slot] = append([]ui.Option{none}, number...)
	}
	return out
}

// comfyInputLabel is the design's "CLIPTextEncode #6 · text" form.
func comfyInputLabel(class, id, input string) string {
	if class == "LoadImage" && input == "image" {
		return class + " #" + id
	}
	return class + " #" + id + " · " + input
}

// customMapRows are the step 2 "Map inputs" rows that are set.
func customMapRows(ctx context.Context, v customProviderView) [][2]string {
	rows := [][2]string{}
	for _, slot := range customMapSlots {
		value := v.MapValues[slot]
		if value == "" {
			continue
		}
		label := value
		for _, o := range v.MapOptions[slot] {
			if o.Value == value {
				label = o.Label
			}
		}
		rows = append(rows, [2]string{i18n.T(ctx, "aiimage.custom.map."+slot), label})
	}
	return rows
}

// parseComfyMap reads the map selects of a ComfyUI draft (Back to step 1).
func parseComfyMap(r *http.Request, nodes map[string]comfyNode) (comfyMap, bool) {
	get := func(slot string) *comfyInput {
		v := web.FormValue(r, "map_"+slot)
		node, input, ok := strings.Cut(v, ".")
		if !ok || node == "" || input == "" {
			return nil
		}
		return &comfyInput{Node: node, Input: input}
	}
	m := comfyMap{Negative: get("negative"), Reference: get("reference"), Pose: get("pose"), Seed: get("seed"),
		Width: get("width"), Height: get("height"), Batch: get("batch")}
	if p := get("prompt"); p != nil {
		m.Prompt = *p
	}
	return m, m.Prompt.set()
}

// readWorkflowUpload returns the uploaded workflow file, if any.
func readWorkflowUpload(r *http.Request) ([]byte, string, bool) {
	if r.MultipartForm == nil {
		return nil, "", false
	}
	files := r.MultipartForm.File["workflow"]
	if len(files) != 1 || files[0].Size == 0 {
		return nil, "", false
	}
	f, err := files[0].Open()
	if err != nil {
		return nil, "", false
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxCustomConfigBytes))
	if err != nil {
		return nil, "", false
	}
	name := files[0].Filename
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	if len(name) > 160 {
		name = name[:160]
	}
	return data, name, true
}

// customConnectTyped is step 1 of a ComfyUI, Hugging Face or HTTP
// connection: it validates the fields, saves (or updates) the disabled draft
// and runs the non-generating test, so step 2 opens on its result.
func (m *Module) customConnectTyped(w http.ResponseWriter, r *http.Request, actor providerActor, inst settings.Instance, typ string, draft *providerRecord) error {
	ctx := r.Context()
	v := m.customBase(r, actor, "connect", inst)
	v.Type = typ
	v.typeDefaults(ctx)
	v.Name, v.Scope = web.FormValue(r, "name"), web.FormValue(r, "scope")
	v.Endpoint = strings.TrimRight(web.FormValue(r, "endpoint"), "/")
	v.AllowPrivate = web.FormValue(r, "allow_private") == "true"
	kind := customTypeKind(typ)
	var prior any
	if draft != nil {
		v.withRecord(ctx, *draft)
		v.Name, v.Scope = web.FormValue(r, "name"), web.FormValue(r, "scope")
		v.Endpoint = strings.TrimRight(web.FormValue(r, "endpoint"), "/")
		v.ID, v.HasKey = draft.ID, draft.KeyEnc != ""
	}
	if !actor.Admin && v.Scope != providerScopeUser {
		return core.ErrForbidden("")
	}
	key := strings.TrimSpace(r.PostFormValue("key"))
	if !validProviderName(v.Name) {
		v.Errors.Add("name", i18n.T(ctx, "models.form.name_invalid"))
	}
	if key != "" && !validProviderKey(key) {
		v.Errors.Add("key", i18n.T(ctx, "models.form.key_invalid"))
	}
	if v.Scope != providerScopeServer && v.Scope != providerScopeUser {
		v.Errors.Add("scope", i18n.T(ctx, "models.form.scope_invalid"))
	}
	if kind != providerKindHuggingFace {
		// A server on this machine or network: the private-network rules.
		switch {
		case !validServerURL(v.Endpoint, kind == providerKindHTTP):
			v.Errors.Add("endpoint", i18n.T(ctx, "aiimage.custom.server_invalid"))
		case endpointNeverAllowed(v.Endpoint):
			v.Errors.Add("endpoint", i18n.T(ctx, "aiimage.custom.endpoint_blocked"))
		case !v.PrivateAllowed && !(actor.Admin && v.AllowPrivate):
			if actor.Admin {
				v.Errors.Add("endpoint", i18n.T(ctx, "aiimage.custom.private_off_admin"))
			} else {
				v.Errors.Add("endpoint", i18n.T(ctx, "aiimage.custom.private_off_user"))
			}
		}
	}
	base := v.Endpoint
	spec := customImageSpec{MaxWidth: v.MaxWidth, MaxHeight: v.MaxHeight, MaxCount: v.MaxCount, Commercial: v.Commercial}
	switch kind {
	case providerKindComfyUI:
		var cfg comfyConfig
		if draft != nil {
			cfg, _ = decodeComfyConfig(draft.Config)
		}
		if data, name, ok := readWorkflowUpload(r); ok {
			nodes, err := parseComfyWorkflow(data)
			if err != nil {
				v.Errors.Add("workflow", i18n.T(ctx, "aiimage.custom.workflow_invalid"))
				break
			}
			cfg.Workflow, cfg.WorkflowName, cfg.Map = json.RawMessage(data), name, detectComfyMap(nodes)
		} else if draft != nil {
			if nodes, err := parseComfyWorkflow(cfg.Workflow); err == nil {
				if mapped, ok := parseComfyMap(r, nodes); ok {
					cfg.Map = mapped
				}
			}
		}
		if len(cfg.Workflow) == 0 {
			v.Errors.Add("workflow", i18n.T(ctx, "aiimage.custom.workflow_invalid"))
			break
		}
		if draft == nil {
			cfg.Image = spec
		}
		if !cfg.Map.Batch.set() {
			cfg.Image.MaxCount = 1
		}
		if err := validateComfyConfig(base, cfg); err != nil && !v.Errors.Has() {
			v.Errors.Add("workflow", i18n.T(ctx, "aiimage.custom.map_invalid"))
		}
		prior = cfg
	case providerKindHuggingFace:
		var cfg hfConfig
		if draft != nil {
			cfg, _ = decodeHFConfig(draft.Config)
		}
		cfg.Endpoint, cfg.Task, cfg.Model = web.FormValue(r, "hf_endpoint"), web.FormValue(r, "task"), strings.TrimSpace(web.FormValue(r, "model"))
		v.HFEndpoint, v.Task, v.Model = cfg.Endpoint, cfg.Task, cfg.Model
		if cfg.Endpoint == hfServerless {
			base = hfRouterBase
			cfg.Task = hfTaskText
			v.Task = hfTaskText
		}
		if cfg.Task == hfTaskText {
			if cfg.ContextTokens == 0 {
				cfg.ContextTokens, cfg.OutputTokens = 32768, 4096
			}
			cfg.Image = customImageSpec{}
		} else {
			cfg.ContextTokens, cfg.OutputTokens = 0, 0
			if !cfg.Image.valid() {
				cfg.Image = defaultImageSpec
			}
		}
		cfg.Priced, cfg.InputNano, cfg.OutputNano = false, 0, 0
		switch {
		case cfg.Endpoint == hfDedicated && !validHFDedicatedURL(base):
			v.Errors.Add("endpoint", i18n.T(ctx, "aiimage.custom.hf_endpoint_invalid"))
		case !validHFRepo(cfg.Model, cfg.Endpoint == hfServerless):
			v.Errors.Add("model", i18n.T(ctx, "aiimage.custom.hf_model_invalid"))
		case validateHFConfig(base, cfg) != nil:
			v.Errors.Add("", i18n.T(ctx, "aiimage.form.invalid"))
		}
		if key == "" && (draft == nil || draft.KeyEnc == "") {
			v.Errors.Add("key", i18n.T(ctx, "models.form.key_invalid"))
		}
		prior = cfg
	case providerKindHTTP:
		var cfg httpConfig
		if draft != nil {
			cfg, _ = decodeHTTPConfig(draft.Config)
		} else {
			cfg.Image = spec
		}
		cfg.Image.MaxCount = 1
		cfg.Request = json.RawMessage(strings.TrimSpace(web.FormValue(r, "template")))
		cfg.Mode, cfg.ImagePath, cfg.ImageKind = web.FormValue(r, "mode"), strings.TrimSpace(web.FormValue(r, "image_path")), web.FormValue(r, "image_kind")
		cfg.StatusURLPath, cfg.StatusPath = strings.TrimSpace(web.FormValue(r, "status_url_path")), strings.TrimSpace(web.FormValue(r, "status_path"))
		cfg.Done, cfg.Failed, cfg.CheckURL = strings.TrimSpace(web.FormValue(r, "done")), strings.TrimSpace(web.FormValue(r, "failed")), strings.TrimSpace(web.FormValue(r, "check_url"))
		if cfg.Mode != httpModePoll {
			cfg.StatusURLPath, cfg.StatusPath, cfg.Done, cfg.Failed = "", "", "", ""
		}
		v.Template, v.Mode, v.ImagePath, v.ImageKind = string(cfg.Request), cfg.Mode, cfg.ImagePath, cfg.ImageKind
		v.StatusURLPath, v.StatusPath, v.Done, v.Failed, v.CheckURL = cfg.StatusURLPath, cfg.StatusPath, cfg.Done, cfg.Failed, cfg.CheckURL
		if _, ok := templatePlaceholders(cfg.Request); !ok || !strings.Contains(string(cfg.Request), "{{prompt}}") {
			v.Errors.Add("template", i18n.T(ctx, "aiimage.custom.template_invalid"))
		} else if !v.Errors.Has() && validateHTTPConfig(base, cfg) != nil {
			v.Errors.Add("image_path", i18n.T(ctx, "aiimage.custom.paths_invalid"))
		}
		prior = cfg
	}
	if v.Errors.Has() {
		return m.renderCustom(w, r, http.StatusUnprocessableEntity, v)
	}
	if kind != providerKindHuggingFace && !v.PrivateAllowed {
		if err := m.private.Set(ctx, true, actor.UserID); err != nil {
			return err
		}
	}
	id := ""
	if draft == nil {
		created, err := m.providers.Create(ctx, actor, providerCreate{Kind: kind, Name: v.Name, BaseURL: base, Config: prior, Key: key, Scope: v.Scope, Draft: true})
		if err != nil {
			v.Errors.Add("", i18n.T(ctx, "aiimage.form.invalid"))
			return m.renderCustom(w, r, http.StatusUnprocessableEntity, v)
		}
		id = created.ID
	} else {
		if err := m.updateTypedDraft(ctx, actor, *draft, v.Name, base, v.Scope, key, prior); err != nil {
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

// updateTypedDraft replaces step 1 of a disabled typed draft. A blank key
// keeps the stored one; the test result is cleared because the target changed.
func (m *Module) updateTypedDraft(ctx context.Context, actor providerActor, draft providerRecord, name, base, scope, key string, cfg any) error {
	raw, err := json.Marshal(cfg)
	if err != nil || !validProviderName(name) || validateCustomConfig(draft.Kind, base, string(raw)) != nil || key != "" && !validProviderKey(key) {
		return errProviderInvalid
	}
	owner, err := m.providers.ownerForCreate(ctx, actor, scope)
	if err != nil {
		return err
	}
	return m.d.DB.WithTx(ctx, func(tx *db.Tx) error {
		r, err := providerByID(ctx, tx, draft.ID, true)
		if err != nil {
			return err
		}
		if r.Kind != draft.Kind || r.Enabled || authorizeProvider(actor, r) != nil {
			return errProviderForbidden
		}
		keyEnc, hint := r.KeyEnc, r.KeyHint
		if key != "" {
			keyEnc, hint = m.providers.box.SealString(key, providerKeyAAD(r.ID)), secrets.Hint(key)
		}
		_, err = tx.Exec(ctx, `UPDATE providers SET name = ?, base_url = ?, config = ?, scope = ?, owner_id = ?, key_enc = ?, key_hint = ?, last_test = '{}', updated_at = ? WHERE id = ? AND enabled = FALSE`,
			name, base, string(raw), scope, db.NullString(owner), keyEnc, hint, db.Now(), r.ID)
		return err
	})
}

// runTypedTest is step 2's test for a typed draft (or a re-test): the
// non-generating check of checkCustomConnection, with the private-network
// policy applied before a local server is dialled.
func (m *Module) runTypedTest(ctx context.Context, record providerRecord) error {
	state, stage := "error", "test"
	started := time.Now()
	ok := true
	if record.Kind != providerKindHuggingFace {
		_, err := m.private.netPolicy(ctx).CheckURL(ctx, record.BaseURL, nil)
		ok = err == nil
	}
	if ok {
		p := dispatchProvider{ID: record.ID, Kind: record.Kind, BaseURL: record.BaseURL, Config: record.Config, KeyEnc: record.KeyEnc}
		if record.KeyEnc != "" {
			key, err := m.d.Secrets.OpenString(record.KeyEnc, providerKeyAAD(record.ID))
			ok = err == nil
			p.Key = key
		}
		if ok {
			testCtx, cancel := context.WithTimeout(ctx, providerTestTimeout)
			_, err := checkCustomConnection(testCtx, m.providers, m.text.localDoer, m.providerAPI.hfDoer, p)
			cancel()
			if err == nil {
				state, stage = "ok", "describe"
			}
		}
	}
	return m.setCustomStage(context.WithoutCancel(ctx), record.ID, stage, state, time.Since(started).Milliseconds())
}

// customDescribeTyped is step 3 of a typed connection: the name, and either
// the text token budgets (Hugging Face text) or the image description.
func (m *Module) customDescribeTyped(w http.ResponseWriter, r *http.Request, v customProviderView, record providerRecord, last providerTestState) error {
	ctx := r.Context()
	v.Name = web.FormValue(r, "name")
	if !validProviderName(v.Name) {
		v.Errors.Add("name", i18n.T(ctx, "models.form.name_invalid"))
	}
	atoi := func(name string) int { n, _ := strconv.Atoi(web.FormValue(r, name)); return n }
	spec := customImageSpec{MaxWidth: atoi("max_width"), MaxHeight: atoi("max_height"), MaxCount: atoi("max_count"), Commercial: web.FormValue(r, "commercial") == "true"}
	v.setImage(spec)
	var cfg any
	switch record.Kind {
	case providerKindComfyUI:
		c, _ := decodeComfyConfig(record.Config)
		if !c.Map.Batch.set() {
			spec.MaxCount = 1
		}
		c.Image = spec
		cfg = c
	case providerKindHTTP:
		c, _ := decodeHTTPConfig(record.Config)
		spec.MaxCount = 1
		c.Image = spec
		cfg = c
	case providerKindHuggingFace:
		c, _ := decodeHFConfig(record.Config)
		if c.Task == hfTaskText {
			v.Context, v.Output = atoi("context"), atoi("output")
			c.ContextTokens, c.OutputTokens = v.Context, v.Output
		} else {
			spec.MaxCount = 1
			c.Image = spec
		}
		cfg = c
	}
	raw, err := json.Marshal(cfg)
	if err != nil || validateCustomConfig(record.Kind, record.BaseURL, string(raw)) != nil {
		if record.Kind == providerKindHuggingFace && v.Task == hfTaskText {
			v.Errors.Add("context", i18n.T(ctx, "aiimage.custom.tokens_invalid"))
		} else {
			v.Errors.Add("max_width", i18n.T(ctx, "aiimage.custom.size_invalid"))
		}
	}
	if v.Errors.Has() {
		return m.renderCustom(w, r, http.StatusUnprocessableEntity, v)
	}
	if _, err := m.d.DB.Exec(ctx, `UPDATE providers SET name = ?, config = ?, updated_at = ? WHERE id = ? AND enabled = FALSE`, v.Name, string(raw), db.Now(), record.ID); err != nil {
		return err
	}
	// A changed token budget changes a serverless bound: test again before
	// routing (the tariff stays frozen from the passed test).
	if err := m.setCustomStage(ctx, record.ID, "route", last.State, last.DurationMS); err != nil {
		return err
	}
	core.Redirect(w, r, customURL("route", record.ID))
	return nil
}

// customRouteTyped is step 4 of a typed connection: the routing modes.
func (m *Module) customRouteTyped(r *http.Request, record providerRecord, modes []string) (string, error) {
	var cfg any
	capability := capImage
	switch record.Kind {
	case providerKindComfyUI:
		c, _ := decodeComfyConfig(record.Config)
		c.Modes = modes
		cfg = c
	case providerKindHTTP:
		c, _ := decodeHTTPConfig(record.Config)
		c.Modes = modes
		cfg = c
	case providerKindHuggingFace:
		c, _ := decodeHFConfig(record.Config)
		c.Modes = modes
		if c.Task == hfTaskText {
			capability = capText
		}
		cfg = c
	}
	raw, err := json.Marshal(cfg)
	if err != nil || len(modes) == 0 || validateCustomConfig(record.Kind, record.BaseURL, string(raw)) != nil {
		return "", errProviderInvalid
	}
	_, err = m.d.DB.Exec(r.Context(), `UPDATE providers SET config = ?, updated_at = ? WHERE id = ? AND enabled = FALSE`, string(raw), db.Now(), record.ID)
	return capability, err
}

// customTypeOptions are the G12 Hugging Face choices.
func hfEndpointOptions(ctx context.Context) []ui.Option {
	return []ui.Option{{Value: hfServerless, Label: i18n.T(ctx, "aiimage.custom.hf_serverless")}, {Value: hfDedicated, Label: i18n.T(ctx, "aiimage.custom.hf_dedicated")}}
}

func hfTaskOptions(ctx context.Context) []ui.Option {
	return []ui.Option{{Value: hfTaskText, Label: i18n.T(ctx, "models.cap.text")}, {Value: hfTaskImage, Label: i18n.T(ctx, "models.cap.image")}}
}

func httpModeOptions(ctx context.Context) []ui.Option {
	return []ui.Option{{Value: httpModeSync, Label: i18n.T(ctx, "aiimage.custom.http_sync")}, {Value: httpModePoll, Label: i18n.T(ctx, "aiimage.custom.http_poll")}}
}

func httpImageKindOptions(ctx context.Context) []ui.Option {
	return []ui.Option{{Value: httpBase64, Label: i18n.T(ctx, "aiimage.custom.http_base64")}, {Value: httpURL, Label: i18n.T(ctx, "aiimage.custom.http_url")}}
}
