package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"mime/multipart"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// ComfyUI workflow connections (G12 "ComfyUI workflow", ADR 0013 §9). Facts
// from the ComfyUI server documentation and source (Comfy-Org/ComfyUI
// v0.44.0, server.py), accessed 2026-11-09:
//   - POST /prompt {"prompt": <API-format workflow>, "client_id"} →
//     {"prompt_id", "number", "node_errors"}; 400 with "error" and
//     "node_errors" for an invalid workflow:
//     https://docs.comfy.org/development/comfyui-server/comms_routes
//   - API format: node id → {"class_type", "inputs"}, links ["4", 0]:
//     https://docs.comfy.org/development/api-development/workflow-api-format
//   - GET /history/{prompt_id} is {} until the prompt finishes, then holds
//     outputs → node → images [{filename, subfolder, type}] and status
//     {status_str, completed}; GET /view?filename=&subfolder=&type= returns
//     the file; POST /upload/image (multipart image, type, subfolder,
//     overwrite) → {name, subfolder, type}; GET /system_stats and GET
//     /object_info describe the server without running anything; POST
//     /interrupt {"prompt_id"} and POST /queue {"delete": [ids]}:
//     https://docs.comfy.org/development/comfyui-server/comms_routes,
//     https://docs.comfy.org/development/comfyui-server/api-examples
//   - "Partner Nodes" are paid nodes that call api.comfy.org with a key passed
//     in extra_data.api_key_comfy_org; /object_info marks them api_node:
//     https://docs.comfy.org/development/comfyui-server/api-key-integration
//
// A ComfyUI connection is a server on this machine or network: it follows the
// custom text rules of ADR 0007 (private-network setting, loopback/private
// addresses only, no redirects, blocked by local_only, no spend
// reservation). The connection test refuses workflows that use a partner
// (api_node) class, and MangaVeyra never sends a Comfy account key, so a
// workflow cannot spend through the Comfy API.
// Status: fake-transport tested, not live-verified.
const (
	comfyMaxNodes    = 512
	comfyClientID    = "mangaveyra"
	comfyUploadDir   = "mangaveyra"
	comfyHistoryWait = imageCallTimeout
)

// comfyInput names one input of one workflow node ("6" · "text").
type comfyInput struct {
	Node  string `json:"node"`
	Input string `json:"input"`
}

func (i *comfyInput) set() bool { return i != nil && i.Node != "" }

// comfyMap is G12's "Map inputs": where MangaVeyra writes each value.
type comfyMap struct {
	Prompt    comfyInput  `json:"prompt"`
	Negative  *comfyInput `json:"negative,omitempty"`
	Seed      *comfyInput `json:"seed,omitempty"`
	Width     *comfyInput `json:"width,omitempty"`
	Height    *comfyInput `json:"height,omitempty"`
	Batch     *comfyInput `json:"batch,omitempty"`
	Reference *comfyInput `json:"reference,omitempty"`
	Pose      *comfyInput `json:"pose,omitempty"`
}

type comfyConfig struct {
	Workflow     json.RawMessage `json:"workflow"`
	WorkflowName string          `json:"workflow_name"`
	Map          comfyMap        `json:"map"`
	Image        customImageSpec `json:"image"`
	Modes        []string        `json:"modes,omitempty"`
}

type comfyNode struct {
	ClassType string         `json:"class_type"`
	Inputs    map[string]any `json:"inputs"`
	Meta      any            `json:"_meta,omitempty"`
}

func (c comfyConfig) routeModes() []string { return customModes(c.Modes, defaultLocalModes) }

func decodeComfyConfig(raw string) (comfyConfig, error) {
	var c comfyConfig
	return c, decodeStrict(raw, &c)
}

// parseComfyWorkflow reads an API-format workflow. The UI "Save" format
// (with a "nodes" array) is refused: ComfyUI's "Export (API)" is required.
func parseComfyWorkflow(raw []byte) (map[string]comfyNode, error) {
	var nodes map[string]comfyNode
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if len(raw) == 0 || len(raw) > maxCustomConfigBytes-16<<10 || dec.Decode(&nodes) != nil || len(nodes) == 0 || len(nodes) > comfyMaxNodes {
		return nil, errProviderInvalid
	}
	for id, n := range nodes {
		if id == "" || len(id) > 32 || n.ClassType == "" || len(n.ClassType) > 128 || n.Inputs == nil {
			return nil, errProviderInvalid
		}
	}
	return nodes, nil
}

func comfyLiteral(nodes map[string]comfyNode, in *comfyInput, number bool) bool {
	if !in.set() {
		return true
	}
	n, ok := nodes[in.Node]
	if !ok {
		return false
	}
	v, ok := n.Inputs[in.Input]
	if !ok {
		return false
	}
	if _, link := v.([]any); link {
		return false
	}
	if number {
		_, isNum := v.(json.Number)
		return isNum
	}
	_, isText := v.(string)
	return isText
}

func comfyLoadImage(nodes map[string]comfyNode, in *comfyInput) bool {
	if !in.set() {
		return true
	}
	n, ok := nodes[in.Node]
	return ok && n.ClassType == "LoadImage" && in.Input == "image"
}

func validateComfyConfig(base string, c comfyConfig) error {
	if !validServerURL(base, false) || endpointNeverAllowed(base) || !c.Image.valid() || !validLocalModes(c.Modes) ||
		len(c.WorkflowName) > 160 {
		return errProviderInvalid
	}
	nodes, err := parseComfyWorkflow(c.Workflow)
	if err != nil {
		return err
	}
	m := c.Map
	if !m.Prompt.set() || !comfyLiteral(nodes, &m.Prompt, false) || !comfyLiteral(nodes, m.Negative, false) ||
		!comfyLiteral(nodes, m.Seed, true) || !comfyLiteral(nodes, m.Width, true) || !comfyLiteral(nodes, m.Height, true) ||
		!comfyLiteral(nodes, m.Batch, true) || !comfyLoadImage(nodes, m.Reference) || !comfyLoadImage(nodes, m.Pose) {
		return errProviderInvalid
	}
	// Without a batch input every run makes one image.
	if !m.Batch.set() && c.Image.MaxCount != 1 {
		return errProviderInvalid
	}
	return nil
}

// detectComfyMap proposes the input map for a workflow: the CLIPTextEncode
// nodes feeding a KSampler's positive and negative inputs, its seed, the
// EmptyLatentImage size and batch, and the first two LoadImage nodes as the
// reference and pose images. The admin can change every choice.
func detectComfyMap(nodes map[string]comfyNode) comfyMap {
	var m comfyMap
	ids := make([]string, 0, len(nodes))
	for id := range nodes {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return comfyLess(ids[i], ids[j]) })
	link := func(v any) string {
		if l, ok := v.([]any); ok && len(l) == 2 {
			if s, ok := l[0].(string); ok {
				return s
			}
		}
		return ""
	}
	loads := []string{}
	for _, id := range ids {
		n := nodes[id]
		switch n.ClassType {
		case "KSampler", "KSamplerAdvanced":
			if m.Prompt.Node == "" {
				if p := link(n.Inputs["positive"]); p != "" && nodes[p].ClassType == "CLIPTextEncode" {
					m.Prompt = comfyInput{Node: p, Input: "text"}
				}
				if q := link(n.Inputs["negative"]); q != "" && nodes[q].ClassType == "CLIPTextEncode" {
					m.Negative = &comfyInput{Node: q, Input: "text"}
				}
			}
			for _, name := range []string{"seed", "noise_seed"} {
				if _, ok := n.Inputs[name]; ok && m.Seed == nil {
					m.Seed = &comfyInput{Node: id, Input: name}
				}
			}
		case "EmptyLatentImage", "EmptySD3LatentImage":
			if m.Width == nil {
				m.Width, m.Height = &comfyInput{Node: id, Input: "width"}, &comfyInput{Node: id, Input: "height"}
				if _, ok := n.Inputs["batch_size"]; ok {
					m.Batch = &comfyInput{Node: id, Input: "batch_size"}
				}
			}
		case "LoadImage":
			loads = append(loads, id)
		}
	}
	if m.Prompt.Node == "" {
		for _, id := range ids {
			if nodes[id].ClassType == "CLIPTextEncode" {
				m.Prompt = comfyInput{Node: id, Input: "text"}
				break
			}
		}
	}
	if len(loads) > 0 {
		m.Reference = &comfyInput{Node: loads[0], Input: "image"}
	}
	if len(loads) > 1 {
		m.Pose = &comfyInput{Node: loads[1], Input: "image"}
	}
	// Keep only literal inputs a run can overwrite.
	for _, p := range []**comfyInput{&m.Negative, &m.Seed, &m.Width, &m.Height, &m.Batch} {
		if *p != nil && !comfyLiteral(nodes, *p, *p != m.Negative) {
			*p = nil
		}
	}
	if !comfyLiteral(nodes, &m.Prompt, false) {
		m.Prompt = comfyInput{}
	}
	return m
}

// comfyLess orders node ids numerically when they are numbers ("3" < "12").
func comfyLess(a, b string) bool {
	if len(a) != len(b) {
		return len(a) < len(b)
	}
	return a < b
}

// comfyManifest describes the workflow for the router.
func comfyManifest(id, name string, c comfyConfig) ModelManifest {
	refs := 0
	if c.Map.Reference.set() {
		refs = 1
	}
	return ModelManifest{ID: customModelPrefix + id, Name: name, Maker: "ComfyUI", Kind: "image", Licence: "workflow",
		Commercial: c.Image.Commercial, Providers: []string{providerKindComfyUI}, References: refs, BW: true,
		MaxWidth: c.Image.MaxWidth, MaxHeight: c.Image.MaxHeight, MaxCount: c.Image.MaxCount, Quality: 60, Speed: 50}
}

// comfyClient talks to one ComfyUI server through the private-network
// client. key is optional (a reverse proxy in front of ComfyUI).
type comfyClient struct {
	doer      httpDoer
	base, key string
	poll      time.Duration
}

func (c comfyClient) url(path string, q url.Values) string {
	u := strings.TrimRight(c.base, "/") + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return u
}

func (c comfyClient) do(ctx context.Context, method, path string, q url.Values, body []byte, contentType string, limit int64) ([]byte, int, error) {
	r, err := http.NewRequestWithContext(ctx, method, c.url(path, q), bytes.NewReader(body))
	if err != nil {
		return nil, 0, badInputAdapterError()
	}
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	if c.key != "" {
		r.Header.Set("Authorization", "Bearer "+c.key)
	}
	if c.doer == nil {
		return nil, 0, upstreamAdapterError(false)
	}
	resp, err := c.doer.Do(r)
	if err != nil {
		return nil, 0, localTransportError(err)
	}
	if resp == nil || resp.Body == nil {
		return nil, 0, upstreamAdapterError(true)
	}
	defer resp.Body.Close()
	data, large, err := readBounded(resp.Body, limit)
	if err != nil || large {
		return nil, resp.StatusCode, badOutputAdapterError(false)
	}
	return data, resp.StatusCode, nil
}

// comfyTestResult is what the G12 test found: the node count and whether
// every class and mapped input exists on the server.
type comfyTestResult struct {
	Nodes   int
	Missing []string
	Partner []string
}

// testComfy checks the server and the workflow without running it:
// /system_stats proves the server answers, /object_info that every node class
// exists, is not a paid partner node, and has the mapped inputs.
func testComfy(ctx context.Context, c comfyClient, cfg comfyConfig) (comfyTestResult, error) {
	var out comfyTestResult
	nodes, err := parseComfyWorkflow(cfg.Workflow)
	if err != nil {
		return out, badInputAdapterError()
	}
	out.Nodes = len(nodes)
	data, status, err := c.do(ctx, http.MethodGet, "/system_stats", nil, nil, "", 1<<20)
	if err != nil {
		return out, err
	}
	if status != http.StatusOK || !json.Valid(data) {
		return out, classifyHTTPStatus(status)
	}
	data, status, err = c.do(ctx, http.MethodGet, "/object_info", nil, nil, "", 32<<20)
	if err != nil {
		return out, err
	}
	var info map[string]struct {
		APINode bool `json:"api_node"`
		Input   struct {
			Required map[string]json.RawMessage `json:"required"`
			Optional map[string]json.RawMessage `json:"optional"`
		} `json:"input"`
	}
	if status != http.StatusOK || json.Unmarshal(data, &info) != nil {
		return out, upstreamAdapterError(false)
	}
	classes := map[string]bool{}
	for _, n := range nodes {
		classes[n.ClassType] = true
	}
	for class := range classes {
		spec, ok := info[class]
		switch {
		case !ok:
			out.Missing = append(out.Missing, class)
		case spec.APINode:
			out.Partner = append(out.Partner, class)
		}
	}
	m := cfg.Map
	for _, in := range []*comfyInput{&m.Prompt, m.Negative, m.Seed, m.Width, m.Height, m.Batch, m.Reference, m.Pose} {
		if !in.set() {
			continue
		}
		spec := info[nodes[in.Node].ClassType]
		_, req := spec.Input.Required[in.Input]
		_, opt := spec.Input.Optional[in.Input]
		if !req && !opt {
			out.Missing = append(out.Missing, nodes[in.Node].ClassType+"."+in.Input)
		}
	}
	sort.Strings(out.Missing)
	sort.Strings(out.Partner)
	if len(out.Missing) > 0 || len(out.Partner) > 0 {
		return out, &adapterError{code: CodeNoProvider, safe: "The ComfyUI server cannot run this workflow.", cause: errNoListedModels}
	}
	return out, nil
}

// comfyUpload stores one input image on the server and returns the value a
// LoadImage node takes.
func (c comfyClient) upload(ctx context.Context, data []byte, name string) (string, error) {
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	part, err := w.CreateFormFile("image", name)
	if err != nil {
		return "", badInputAdapterError()
	}
	if _, err := part.Write(data); err != nil {
		return "", badInputAdapterError()
	}
	for k, v := range map[string]string{"type": "input", "subfolder": comfyUploadDir, "overwrite": "true"} {
		if err := w.WriteField(k, v); err != nil {
			return "", badInputAdapterError()
		}
	}
	if err := w.Close(); err != nil {
		return "", badInputAdapterError()
	}
	resp, status, err := c.do(ctx, http.MethodPost, "/upload/image", nil, b.Bytes(), w.FormDataContentType(), 64<<10)
	if err != nil {
		return "", err
	}
	var up struct {
		Name      string `json:"name"`
		Subfolder string `json:"subfolder"`
	}
	if status != http.StatusOK || json.Unmarshal(resp, &up) != nil || up.Name == "" || strings.Contains(up.Name, "..") || strings.Contains(up.Subfolder, "..") {
		return "", upstreamAdapterError(false)
	}
	if up.Subfolder != "" {
		return up.Subfolder + "/" + up.Name, nil
	}
	return up.Name, nil
}

func comfySide(v, limit int) int {
	v = int(math.Round(float64(v)/8)) * 8
	return min(max(v, 64), limit)
}

// comfyWorkflowFor writes the run's values into a copy of the workflow.
func comfyWorkflowFor(cfg comfyConfig, req ImageGenerateRequest, images map[string]string) (map[string]comfyNode, error) {
	nodes, err := parseComfyWorkflow(cfg.Workflow)
	if err != nil {
		return nil, badInputAdapterError()
	}
	set := func(in *comfyInput, v any) {
		if in.set() {
			nodes[in.Node].Inputs[in.Input] = v
		}
	}
	m := cfg.Map
	set(&m.Prompt, plainImagePromptNoAvoid(req))
	set(m.Negative, req.Negative)
	set(m.Seed, req.Seed&math.MaxInt32)
	set(m.Width, comfySide(req.Width, cfg.Image.MaxWidth))
	set(m.Height, comfySide(req.Height, cfg.Image.MaxHeight))
	set(m.Batch, req.Count)
	if v, ok := images["reference"]; ok {
		set(m.Reference, v)
	}
	if v, ok := images["pose"]; ok {
		set(m.Pose, v)
	}
	return nodes, nil
}

func comfyImage(ctx context.Context, c comfyClient, cfg comfyConfig, req ImageGenerateRequest) (imageCall, error) {
	var out imageCall
	if req.Init != nil || req.Mask != nil || req.Count > cfg.Image.MaxCount {
		return out, badInputAdapterError()
	}
	callCtx, cancel := context.WithTimeout(ctx, comfyHistoryWait)
	defer cancel()
	images := map[string]string{}
	for _, r := range req.References {
		slot := "reference"
		if r.Role == "pose" {
			slot = "pose"
		}
		if _, done := images[slot]; done || (slot == "reference" && !cfg.Map.Reference.set()) || (slot == "pose" && !cfg.Map.Pose.set()) {
			continue
		}
		data, err := downscaleReference(r.Data, gptImageRefEdge)
		if err != nil {
			return out, badInputAdapterError()
		}
		out.Sent = true
		name, err := c.upload(callCtx, data, fmt.Sprintf("%s-%d.png", slot, time.Now().UnixNano()))
		if err != nil {
			return out, err
		}
		images[slot] = name
	}
	workflow, err := comfyWorkflowFor(cfg, req, images)
	if err != nil {
		return out, err
	}
	body, _ := json.Marshal(map[string]any{"prompt": workflow, "client_id": comfyClientID})
	out.Sent = true
	data, status, err := c.do(callCtx, http.MethodPost, "/prompt", nil, body, "application/json", 1<<20)
	if err != nil {
		return out, err
	}
	if status == http.StatusBadRequest {
		return out, &adapterError{code: CodeBadInput, safe: "The ComfyUI server rejected the workflow.", cause: errInvalidScriptInput}
	}
	var queued struct {
		PromptID string `json:"prompt_id"`
	}
	if status != http.StatusOK || json.Unmarshal(data, &queued) != nil || queued.PromptID == "" || strings.ContainsAny(queued.PromptID, "/?#") {
		return out, classifyHTTPStatus(status)
	}
	stop := func() {
		stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		del, _ := json.Marshal(map[string]any{"delete": []string{queued.PromptID}})
		_, _, _ = c.do(stopCtx, http.MethodPost, "/queue", nil, del, "application/json", 64<<10)
		intr, _ := json.Marshal(map[string]string{"prompt_id": queued.PromptID})
		_, _, _ = c.do(stopCtx, http.MethodPost, "/interrupt", nil, intr, "application/json", 64<<10)
	}
	var files []struct{ Filename, Subfolder, Type string }
	for {
		if err := sleepCtx(callCtx, c.poll); err != nil {
			stop()
			return out, classifyTransportError(err)
		}
		data, status, err := c.do(callCtx, http.MethodGet, "/history/"+url.PathEscape(queued.PromptID), nil, nil, "", 4<<20)
		if err != nil {
			stop()
			return out, err
		}
		var history map[string]struct {
			Outputs map[string]struct {
				Images []struct {
					Filename  string `json:"filename"`
					Subfolder string `json:"subfolder"`
					Type      string `json:"type"`
				} `json:"images"`
			} `json:"outputs"`
			Status struct {
				StatusStr string `json:"status_str"`
				Completed bool   `json:"completed"`
			} `json:"status"`
		}
		if status != http.StatusOK || json.Unmarshal(data, &history) != nil {
			stop()
			return out, upstreamAdapterError(true)
		}
		entry, ok := history[queued.PromptID]
		if !ok {
			continue
		}
		if entry.Status.StatusStr == "error" {
			return out, &adapterError{code: CodeUpstream, safe: "The ComfyUI workflow failed.", cause: errInvalidScriptInput}
		}
		if !entry.Status.Completed {
			continue
		}
		ids := make([]string, 0, len(entry.Outputs))
		for id := range entry.Outputs {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return comfyLess(ids[i], ids[j]) })
		for _, id := range ids {
			for _, im := range entry.Outputs[id].Images {
				if im.Type == "output" && len(files) < req.Count {
					files = append(files, struct{ Filename, Subfolder, Type string }{im.Filename, im.Subfolder, im.Type})
				}
			}
		}
		break
	}
	if len(files) != req.Count {
		return out, badOutputAdapterError(false)
	}
	for _, f := range files {
		data, status, err := c.do(callCtx, http.MethodGet, "/view", url.Values{"filename": {f.Filename}, "subfolder": {f.Subfolder}, "type": {f.Type}}, nil, "", maxImageBytes)
		if err != nil {
			return out, err
		}
		if status != http.StatusOK {
			return out, badOutputAdapterError(false)
		}
		g, err := fitGeneratedImage(data, req.Width, req.Height, req.Seed, true)
		if err != nil {
			return out, err
		}
		out.Images = append(out.Images, g)
	}
	return out, nil
}
