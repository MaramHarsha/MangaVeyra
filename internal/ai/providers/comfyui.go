package providers

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ComfyUI runs an exported ComfyUI workflow (API format) on a ComfyUI server
// over its HTTP API: input images go to POST /upload/image, the workflow with
// the mapped inputs filled in goes to POST /prompt, GET /history/{id} is
// polled until the run finishes and output images are read from GET /view.
// The user maps MangaVeyra fields (prompt, negative, references, pose, seed,
// mask, size) to workflow node inputs once, on the add-provider screen.
type ComfyUI struct{ Env Env }

// Kind implements Adapter.
func (ComfyUI) Kind() string { return KindComfyUI }

// Mapping fields.
const (
	MapPrompt   = "prompt"
	MapNegative = "negative"
	MapImage    = "image"
	MapMask     = "mask"
	MapRef      = "reference"
	MapPose     = "pose"
	MapSeed     = "seed"
	MapWidth    = "width"
	MapHeight   = "height"
	MapSteps    = "steps"
	MapBatch    = "batch"
)

// MapFields lists the mappable fields in display order.
var MapFields = []string{MapPrompt, MapNegative, MapRef, MapPose, MapSeed, MapImage, MapMask, MapWidth, MapHeight, MapSteps, MapBatch}

// NodeInput points at one literal input of a workflow node ("6" + "text").
type NodeInput struct {
	Node  string `json:"node"`
	Input string `json:"input"`
}

// String renders "6.text".
func (n NodeInput) String() string {
	if n.Node == "" {
		return ""
	}
	return n.Node + "." + n.Input
}

// ParseNodeInput parses "6.text".
func ParseNodeInput(s string) NodeInput {
	node, input, ok := strings.Cut(s, ".")
	if !ok {
		return NodeInput{}
	}
	return NodeInput{Node: node, Input: input}
}

// ComfyOptions is the stored configuration of a ComfyUI connection.
type ComfyOptions struct {
	Workflow     json.RawMessage      `json:"workflow"`
	WorkflowName string               `json:"workflow_name,omitempty"`
	Mapping      map[string]NodeInput `json:"mapping"`
}

// WorkflowNode describes one node for the mapping selects.
type WorkflowNode struct {
	ID     string
	Class  string
	Title  string
	Inputs []string
}

// Label is "CLIPTextEncode #6".
func (n WorkflowNode) Label() string { return n.Class + " #" + n.ID }

// Workflow is a parsed API-format workflow.
type Workflow struct {
	Nodes []WorkflowNode
	raw   map[string]map[string]any
}

// ParseWorkflow parses an API-format workflow ("Save (API Format)" in ComfyUI).
func ParseWorkflow(b []byte) (*Workflow, error) {
	var raw map[string]map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		var ui struct {
			Nodes []any `json:"nodes"`
		}
		if json.Unmarshal(b, &ui) == nil && len(ui.Nodes) > 0 {
			return nil, Errf(CodeBadRequest, "this is the editor format; export the workflow with “Save (API Format)”")
		}
		return nil, Errf(CodeBadRequest, "not a ComfyUI workflow: %v", err)
	}
	wf := &Workflow{raw: raw}
	for id, n := range raw {
		class, _ := n["class_type"].(string)
		if class == "" {
			return nil, Errf(CodeBadRequest, "node %s has no class_type; export with “Save (API Format)”", id)
		}
		wn := WorkflowNode{ID: id, Class: class}
		if meta, ok := n["_meta"].(map[string]any); ok {
			wn.Title, _ = meta["title"].(string)
		}
		if ins, ok := n["inputs"].(map[string]any); ok {
			for k, v := range ins {
				if _, link := v.([]any); !link {
					wn.Inputs = append(wn.Inputs, k)
				}
			}
			sort.Strings(wn.Inputs)
		}
		wf.Nodes = append(wf.Nodes, wn)
	}
	if len(wf.Nodes) == 0 {
		return nil, Errf(CodeBadRequest, "the workflow has no nodes")
	}
	sort.Slice(wf.Nodes, func(i, j int) bool {
		a, _ := strconv.Atoi(wf.Nodes[i].ID)
		b, _ := strconv.Atoi(wf.Nodes[j].ID)
		if a != b {
			return a < b
		}
		return wf.Nodes[i].ID < wf.Nodes[j].ID
	})
	return wf, nil
}

// Has reports whether node.input exists as a literal input.
func (w *Workflow) Has(ni NodeInput) bool {
	for _, n := range w.Nodes {
		if n.ID == ni.Node {
			for _, in := range n.Inputs {
				if in == ni.Input {
					return true
				}
			}
		}
	}
	return false
}

// GuessMapping proposes a mapping: the first CLIPTextEncode is the prompt,
// the second the negative, KSampler seed/steps, EmptyLatentImage size and
// batch, LoadImage nodes for image / reference / pose (by title), LoadImageMask for the mask.
func (w *Workflow) GuessMapping() map[string]NodeInput {
	m := map[string]NodeInput{}
	set := func(field, node, input string) {
		if _, ok := m[field]; !ok {
			m[field] = NodeInput{Node: node, Input: input}
		}
	}
	for _, n := range w.Nodes {
		title := strings.ToLower(n.Title)
		has := func(in string) bool {
			for _, x := range n.Inputs {
				if x == in {
					return true
				}
			}
			return false
		}
		switch {
		case strings.Contains(n.Class, "TextEncode") && has("text"):
			if strings.Contains(title, "negative") {
				set(MapNegative, n.ID, "text")
			} else if _, ok := m[MapPrompt]; !ok {
				set(MapPrompt, n.ID, "text")
			} else {
				set(MapNegative, n.ID, "text")
			}
		case strings.Contains(n.Class, "KSampler") || n.Class == "RandomNoise":
			if has("seed") {
				set(MapSeed, n.ID, "seed")
			} else if has("noise_seed") {
				set(MapSeed, n.ID, "noise_seed")
			}
			if has("steps") {
				set(MapSteps, n.ID, "steps")
			}
		case strings.Contains(n.Class, "EmptyLatentImage") || strings.Contains(n.Class, "EmptySD3LatentImage"):
			set(MapWidth, n.ID, "width")
			set(MapHeight, n.ID, "height")
			if has("batch_size") {
				set(MapBatch, n.ID, "batch_size")
			}
		case n.Class == "LoadImageMask":
			set(MapMask, n.ID, "image")
		case n.Class == "LoadImage" && has("image"):
			switch {
			case strings.Contains(title, "pose"):
				set(MapPose, n.ID, "image")
			case strings.Contains(title, "mask"):
				set(MapMask, n.ID, "image")
			case strings.Contains(title, "ref"):
				set(MapRef, n.ID, "image")
			case strings.Contains(title, "source") || strings.Contains(title, "input"):
				set(MapImage, n.ID, "image")
			default:
				if _, ok := m[MapRef]; !ok {
					set(MapRef, n.ID, "image")
				} else {
					set(MapPose, n.ID, "image")
				}
			}
		}
	}
	return m
}

func (a ComfyUI) options(cfg Config) (*ComfyOptions, *Workflow, error) {
	var o ComfyOptions
	if len(cfg.Options) == 0 || json.Unmarshal(cfg.Options, &o) != nil || len(o.Workflow) == 0 {
		return nil, nil, Errf(CodeBadRequest, "no workflow uploaded")
	}
	wf, err := ParseWorkflow(o.Workflow)
	if err != nil {
		return nil, nil, err
	}
	return &o, wf, nil
}

// clone deep-copies the workflow graph.
func (w *Workflow) clone() map[string]map[string]any {
	b, _ := json.Marshal(w.raw)
	var out map[string]map[string]any
	_ = json.Unmarshal(b, &out)
	return out
}

func setInput(g map[string]map[string]any, ni NodeInput, v any) {
	if ni.Node == "" {
		return
	}
	n, ok := g[ni.Node]
	if !ok {
		return
	}
	ins, _ := n["inputs"].(map[string]any)
	if ins == nil {
		ins = map[string]any{}
		n["inputs"] = ins
	}
	ins[ni.Input] = v
}

func randomSeed() int64 {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return int64(binary.BigEndian.Uint64(b[:]) >> 12)
}

// upload stores an input image on the ComfyUI server and returns its reference name.
func (a ComfyUI) upload(ctx context.Context, cfg Config, name string, img Image) (string, error) {
	buf := &bytes.Buffer{}
	mw := multipart.NewWriter(buf)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="image"; filename=%q`, name))
	mime := img.MIME
	if mime == "" {
		mime = http.DetectContentType(img.Data)
	}
	h.Set("Content-Type", mime)
	w, err := mw.CreatePart(h)
	if err != nil {
		return "", Errf(CodeInternal, "%v", err)
	}
	_, _ = w.Write(img.Data)
	_ = mw.WriteField("overwrite", "true")
	_ = mw.WriteField("type", "input")
	_ = mw.WriteField("subfolder", "mangaveyra")
	_ = mw.Close()
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, joinURL(cfg.BaseURL, "upload/image"), buf)
	if err != nil {
		return "", Errf(CodeBadRequest, "%v", err)
	}
	r.Header.Set("Content-Type", mw.FormDataContentType())
	r.Header.Set("User-Agent", userAgent)
	for k, v := range bearer(cfg.APIKey) {
		r.Header.Set(k, v)
	}
	b, _, err := a.Env.send(r)
	if err != nil {
		return "", err
	}
	var out struct {
		Name      string `json:"name"`
		Subfolder string `json:"subfolder"`
	}
	if err := jsonUnmarshal(b, &out); err != nil {
		return "", err
	}
	if out.Subfolder != "" {
		return out.Subfolder + "/" + out.Name, nil
	}
	return out.Name, nil
}

type comfyImage struct {
	Filename  string `json:"filename"`
	Subfolder string `json:"subfolder"`
	Type      string `json:"type"`
}

// Image runs the workflow once per requested image (or once with a batch size).
func (a ComfyUI) Image(ctx context.Context, cfg Config, req ImageRequest, progress Progress) (*ImageResult, error) {
	if cfg.BaseURL == "" {
		return nil, Errf(CodeBadRequest, "ComfyUI server URL is required")
	}
	o, wf, err := a.options(cfg)
	if err != nil {
		return nil, err
	}
	mp := o.Mapping
	if len(mp) == 0 {
		mp = wf.GuessMapping()
	}
	uploads := map[string]string{}
	up := func(field string, img *Image) error {
		if img == nil || mp[field].Node == "" {
			return nil
		}
		name, err := a.upload(ctx, cfg, fmt.Sprintf("%s-%d.png", field, time.Now().UnixNano()), *img)
		if err != nil {
			return err
		}
		uploads[field] = name
		return nil
	}
	progress.report(0.05, "uploading inputs to ComfyUI")
	var ref *Image
	if len(req.Refs) > 0 {
		ref = &req.Refs[0]
	}
	for f, img := range map[string]*Image{MapImage: req.Image, MapMask: req.Mask, MapRef: ref, MapPose: req.PoseImage} {
		if err := up(f, img); err != nil {
			return nil, err
		}
	}
	w, h := req.Size()
	seed := randomSeed()
	if req.Seed != nil {
		seed = *req.Seed
	}
	runs, batch := req.Count(), 1
	if mp[MapBatch].Node != "" {
		runs, batch = 1, req.Count()
	}
	res := &ImageResult{}
	for i := 0; i < runs; i++ {
		g := wf.clone()
		setInput(g, mp[MapPrompt], stylePrompt(req.Prompt, req.Style))
		if req.Negative != "" {
			setInput(g, mp[MapNegative], req.Negative)
		}
		setInput(g, mp[MapSeed], seed+int64(i))
		setInput(g, mp[MapWidth], w/8*8)
		setInput(g, mp[MapHeight], h/8*8)
		if req.Steps > 0 {
			setInput(g, mp[MapSteps], req.Steps)
		}
		if batch > 1 {
			setInput(g, mp[MapBatch], batch)
		}
		for f, name := range uploads {
			setInput(g, mp[f], name)
		}
		imgs, err := a.run(ctx, cfg, g, progress, float64(i)/float64(runs), 1/float64(runs))
		if err != nil {
			return nil, err
		}
		for j := range imgs {
			imgs[j].Seed = seed + int64(i)
		}
		res.Images = append(res.Images, imgs...)
	}
	if len(res.Images) == 0 {
		return nil, Errf(CodeInternal, "the workflow produced no output images")
	}
	return res, nil
}

func (a ComfyUI) run(ctx context.Context, cfg Config, g map[string]map[string]any, progress Progress, from, span float64) ([]Image, error) {
	h := bearer(cfg.APIKey)
	var sub struct {
		PromptID   string         `json:"prompt_id"`
		NodeErrors map[string]any `json:"node_errors"`
	}
	if err := a.Env.call(ctx, http.MethodPost, joinURL(cfg.BaseURL, "prompt"), h, map[string]any{"prompt": g, "client_id": "mangaveyra"}, &sub); err != nil {
		return nil, err
	}
	if len(sub.NodeErrors) > 0 {
		b, _ := json.Marshal(sub.NodeErrors)
		return nil, Errf(CodeBadRequest, "workflow rejected: %s", errorMessage(b))
	}
	deadline := time.Now().Add(a.Env.maxWait())
	for {
		var hist map[string]struct {
			Outputs map[string]struct {
				Images []comfyImage `json:"images"`
			} `json:"outputs"`
			Status struct {
				StatusStr string `json:"status_str"`
				Completed bool   `json:"completed"`
				Messages  []any  `json:"messages"`
			} `json:"status"`
		}
		if err := a.Env.call(ctx, http.MethodGet, joinURL(cfg.BaseURL, "history/"+url.PathEscape(sub.PromptID)), h, nil, &hist); err != nil {
			return nil, err
		}
		if e, ok := hist[sub.PromptID]; ok && (e.Status.Completed || e.Status.StatusStr == "error" || e.Status.StatusStr == "success") {
			if e.Status.StatusStr == "error" {
				b, _ := json.Marshal(e.Status.Messages)
				return nil, Errf(CodeInternal, "ComfyUI run failed: %s", errorMessage(b))
			}
			progress.report(from+span*0.9, "downloading from ComfyUI")
			ids := make([]string, 0, len(e.Outputs))
			for id := range e.Outputs {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			var out []Image
			for _, id := range ids {
				for _, im := range e.Outputs[id].Images {
					if im.Type != "" && im.Type != "output" {
						continue
					}
					q := url.Values{"filename": {im.Filename}, "subfolder": {im.Subfolder}, "type": {"output"}}
					img, err := a.Env.fetch(ctx, joinURL(cfg.BaseURL, "view")+"?"+q.Encode(), h)
					if err != nil {
						return nil, err
					}
					out = append(out, img)
				}
			}
			return out, nil
		}
		progress.report(from+span*0.5, "running workflow")
		if time.Now().After(deadline) {
			_ = a.Env.call(context.WithoutCancel(ctx), http.MethodPost, joinURL(cfg.BaseURL, "interrupt"), h, map[string]any{}, nil)
			return nil, Errf(CodeTimeout, "ComfyUI did not finish in %s", a.Env.maxWait())
		}
		if err := sleep(ctx, a.Env.poll()); err != nil {
			return nil, err
		}
	}
}
