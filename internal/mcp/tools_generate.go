package mcp

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"math"
	"strconv"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/MaramHarsha/MangaVeyra/internal/ai"
	"github.com/MaramHarsha/MangaVeyra/internal/assets"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/gen"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
)

// queue_generation needs the generate scope (Studio also requires edit).

type regionIn struct {
	X      float64 `json:"x" jsonschema:"left edge as a fraction of the layer image width (0 to 1)"`
	Y      float64 `json:"y" jsonschema:"top edge as a fraction of the layer image height (0 to 1)"`
	Width  float64 `json:"width" jsonschema:"width as a fraction of the layer image width"`
	Height float64 `json:"height" jsonschema:"height as a fraction of the layer image height"`
}

type generateIn struct {
	ProjectID    string    `json:"project_id" jsonschema:"the project id"`
	Kind         string    `json:"kind" jsonschema:"script (write a chapter's script), page (draw all art of one page) or region (repaint a rectangle of one art layer; text is never touched)"`
	ChapterID    string    `json:"chapter_id,omitempty" jsonschema:"script: the chapter id"`
	Revision     string    `json:"revision,omitempty" jsonschema:"script: the chapter revision from get_project or get_script"`
	Mode         string    `json:"mode,omitempty" jsonschema:"script: premise (default) or adapt (from the project's source text)"`
	Premise      string    `json:"premise,omitempty" jsonschema:"script: an optional premise for this chapter"`
	PagesTarget  int       `json:"pages_target,omitempty" jsonschema:"script: how many pages to aim for"`
	PageID       string    `json:"page_id,omitempty" jsonschema:"page and region: the page id from get_pages"`
	Version      int       `json:"version,omitempty" jsonschema:"page and region: the page version from get_pages"`
	LayerID      string    `json:"layer_id,omitempty" jsonschema:"region: the art layer id from get_pages"`
	Region       *regionIn `json:"region,omitempty" jsonschema:"region: the rectangle to repaint; pixels outside it are kept exactly"`
	Prompt       string    `json:"prompt,omitempty" jsonschema:"region: what to draw in the rectangle"`
	RoutingMode  string    `json:"routing_mode,omitempty" jsonschema:"balanced, best, fastest, local_only or free_only (default: the project's routing)"`
	EstimateOnly bool      `json:"estimate_only,omitempty" jsonschema:"only return the estimate and whether a person must approve it; nothing is queued"`
}

func (m *Module) addGenerateTools(s *mcpsdk.Server) {
	mcpsdk.AddTool(s, &mcpsdk.Tool{Name: "queue_generation", Annotations: writes("Queue generation", false),
		Description: "Queue AI generation with an estimate: a chapter script, the art of one page, or a region edit of one art layer. Locked targets are refused. When the estimate is above this token's approval threshold nothing runs: a request waits for a person (status pending_approval). Use get_job to follow a queued job."}, m.queueGeneration)
}

// quoteResult is a generation estimate.
type quoteResult struct {
	Estimate   jobs.Cost
	ModelID    string
	ProviderID string
}

type quoteKey struct{}

type quoteCapture struct {
	ok  bool
	res quoteResult
}

var errQuoteOnly = errors.New("mcp: estimate only")

// captureQuote is the enqueue validator behind estimates: inside a dry run it
// records the job the generation service would store and refuses it, so the
// estimate comes from exactly the planning a real request goes through.
func captureQuote(ctx context.Context, p *jobs.EnqueueParams) error {
	c, ok := ctx.Value(quoteKey{}).(*quoteCapture)
	if !ok || c == nil {
		return nil
	}
	if !c.ok {
		c.ok, c.res = true, quoteResult{Estimate: p.Estimate, ModelID: p.ModelID, ProviderID: p.ProviderID}
	}
	return errQuoteOnly
}

// dryRunQuote plans the request as the token's owner (the agent's checks have
// already passed) and stops at the enqueue, so nothing is stored or spent.
func (m *Module) dryRunQuote(ctx context.Context, a *agent, req genRequest) (quoteResult, error) {
	owner, err := m.studio.ActorFromUser(ctx, a.user.ID)
	if err != nil {
		return quoteResult{}, toolError(err)
	}
	c := &quoteCapture{}
	j, err := m.enqueue(context.WithValue(ctx, quoteKey{}, c), owner, req)
	switch {
	case c.ok:
		return c.res, nil
	case err == nil && j != nil:
		// An identical active job already exists; its estimate stands.
		return quoteResult{Estimate: j.Estimate, ModelID: j.ModelID, ProviderID: j.ProviderID}, nil
	}
	return quoteResult{}, toolError(err)
}

// enqueue runs a generation request through the generation service as actor.
func (m *Module) enqueue(ctx context.Context, actor studio.Actor, req genRequest) (*jobs.Job, error) {
	switch req.Kind {
	case "script":
		return m.gen.EnqueueScript(ctx, actor, gen.ScriptRequest{ProjectID: req.ProjectID, ChapterID: req.ChapterID, Mode: req.Mode,
			Premise: req.Premise, PagesTarget: req.PagesTarget, RoutingMode: req.RoutingMode, ExpectedUpdatedAt: req.ExpectedUpdatedAt})
	case "page":
		return m.control.EnqueuePage(ctx, actor, gen.PageRequest{ProjectID: req.ProjectID, PageID: req.PageID,
			ExpectedVersion: req.ExpectedVersion, RoutingMode: req.RoutingMode})
	case "region":
		return m.gen.EnqueueRegionEdit(ctx, actor, gen.RegionEditRequest{ProjectID: req.ProjectID, LayerID: req.LayerID,
			MaskAssetID: req.MaskAssetID, Prompt: req.Prompt, Variations: 1, RoutingMode: req.RoutingMode, ExpectedVersion: req.ExpectedVersion})
	}
	return nil, studio.ErrInvalid
}

// exceeds reports estimate > threshold in whole microdollars (equal passes),
// the same rule the generation service applies.
func exceeds(estimate, threshold float64) bool {
	micro := func(v float64) int64 {
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
			return math.MaxInt64
		}
		return int64(math.Ceil(math.Nextafter(v*1_000_000, math.Inf(-1))))
	}
	return micro(estimate) > micro(threshold)
}

func validRouting(mode string) bool {
	switch mode {
	case "", ai.ModeBalanced, ai.ModeBest, ai.ModeFastest, ai.ModeLocalOnly, ai.ModeFreeOnly:
		return true
	}
	return false
}

// buildRequest validates a tool request into a genRequest and the facts G13
// shows. A region edit stores its rectangle as a project mask image.
func (m *Module) buildRequest(ctx context.Context, a *agent, p studio.Project, in generateIn) (genRequest, actionDetail, error) {
	req := genRequest{Kind: strings.TrimSpace(in.Kind), ProjectID: p.ID, RoutingMode: strings.TrimSpace(in.RoutingMode)}
	var detail actionDetail
	if !validRouting(req.RoutingMode) {
		return req, detail, errors.New("routing_mode must be balanced, best, fastest, local_only or free_only")
	}
	switch req.Kind {
	case "script":
		c, err := m.chapterOf(ctx, p.ID, in.ChapterID)
		if err != nil {
			return req, detail, err
		}
		revision, err := parseRev(in.Revision)
		if err != nil {
			return req, detail, err
		}
		mode := strings.TrimSpace(in.Mode)
		if mode == "" {
			mode = "premise"
		}
		if mode != "premise" && mode != "adapt" {
			return req, detail, errors.New("mode must be premise or adapt")
		}
		req.ChapterID, req.ExpectedUpdatedAt, req.Mode = c.ID, revision, mode
		req.Premise, req.PagesTarget = strings.TrimSpace(in.Premise), in.PagesTarget
		detail.ChapterNumber = c.Number
	case "page", "region":
		if !db.ValidID(strings.TrimSpace(in.PageID)) {
			return req, detail, errors.New("page_id is required (from get_pages)")
		}
		page, err := m.pages.GetPage(ctx, strings.TrimSpace(in.PageID))
		if err != nil || page.ProjectID != p.ID {
			return req, detail, toolError(studio.ErrNotFound)
		}
		if in.Version < 1 {
			return req, detail, errors.New("version is required (the page version from get_pages)")
		}
		if in.Version != page.Version {
			return req, detail, toolError(studio.ErrConflict)
		}
		chapter, err := m.studio.GetChapter(ctx, page.ChapterID)
		if err != nil {
			return req, detail, toolError(err)
		}
		req.PageID, req.ChapterID, req.ExpectedVersion = page.ID, page.ChapterID, page.Version
		detail.ChapterNumber, detail.PageNumber, detail.PageID = chapter.Number, page.Number, page.ID
		if req.Kind == "region" {
			if err := m.regionMask(ctx, a, page, in, &req, &detail); err != nil {
				return req, detail, err
			}
		}
	default:
		return req, detail, errors.New("kind must be script, page or region")
	}
	return req, detail, nil
}

// regionMask validates the region request and stores its mask (white = change)
// in the layer image's pixel space, as the editor's mask upload does.
func (m *Module) regionMask(ctx context.Context, a *agent, page pagedoc.Page, in generateIn, req *genRequest, detail *actionDetail) error {
	var layer *pagedoc.Layer
	panelNo := 0
	for i := range page.Panels {
		if l := page.Panels[i].Layer(strings.TrimSpace(in.LayerID)); l != nil {
			layer, panelNo = l, i+1
		}
	}
	if layer == nil {
		return errors.New("layer_id is not an art layer of that page")
	}
	if layer.Kind == pagedoc.LayerLettering || layer.Asset == nil {
		return errors.New("region edits change art layers that already have art; lettering is never repainted")
	}
	prompt := strings.TrimSpace(in.Prompt)
	if prompt == "" || len([]rune(prompt)) > pagedoc.MaxPromptRunes {
		return errors.New("prompt is required for a region edit")
	}
	r := in.Region
	if r == nil || !(r.Width > 0) || !(r.Height > 0) || r.X < 0 || r.Y < 0 || r.X+r.Width > 1.000001 || r.Y+r.Height > 1.000001 {
		return errors.New("region must be a rectangle inside the layer (x, y, width, height as fractions from 0 to 1)")
	}
	art, err := m.assets.Get(ctx, layer.Asset.ID)
	if err != nil || art.Width <= 0 || art.Height <= 0 {
		return errors.New("the layer's art cannot be read")
	}
	mask := image.NewGray(image.Rect(0, 0, art.Width, art.Height))
	x0, y0 := int(math.Floor(r.X*float64(art.Width))), int(math.Floor(r.Y*float64(art.Height)))
	x1, y1 := int(math.Ceil((r.X+r.Width)*float64(art.Width))), int(math.Ceil((r.Y+r.Height)*float64(art.Height)))
	if x1 <= x0 || y1 <= y0 {
		return errors.New("region is too small")
	}
	for y := max(0, y0); y < min(art.Height, y1); y++ {
		for x := max(0, x0); x < min(art.Width, x1); x++ {
			mask.SetGray(x, y, color.Gray{Y: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, mask); err != nil {
		return toolError(err)
	}
	stored, err := m.assets.Save(ctx, assets.SaveParams{OwnerID: a.user.ID, ProjectID: page.ProjectID, Kind: "region_mask",
		Source: assets.SourceSystem, ContentType: "image/png", Data: buf.Bytes(), Meta: map[string]string{"agent_token_id": a.tok.ID}})
	if err != nil {
		return toolError(err)
	}
	req.LayerID, req.MaskAssetID, req.Prompt = layer.ID, stored.ID, prompt
	detail.PanelLabel = strconv.Itoa(page.Number) + "." + strconv.Itoa(panelNo)
	return nil
}

func (m *Module) queueGeneration(ctx context.Context, req *mcpsdk.CallToolRequest, in generateIn) (*mcpsdk.CallToolResult, any, error) {
	a, err := m.agentFor(ctx, req, scopeRead, scopeWrite, scopeGenerate)
	if err != nil {
		return nil, nil, err
	}
	if !m.ready(ctx) {
		return nil, nil, errUnavailable
	}
	p, err := m.project(ctx, a, in.ProjectID, studio.ActGenerate)
	if err != nil {
		return nil, nil, err
	}
	if !in.EstimateOnly {
		if err := m.writeAllowed(a); err != nil {
			return nil, nil, err
		}
	}
	greq, detail, err := m.buildRequest(ctx, a, p, in)
	if err != nil {
		return nil, nil, err
	}
	if in.EstimateOnly && greq.MaskAssetID != "" {
		defer func() { _ = m.assets.Delete(context.WithoutCancel(ctx), greq.MaskAssetID) }()
	}
	q, err := m.quote(ctx, a, greq)
	if err != nil {
		return nil, nil, err
	}
	threshold := a.tok.ApproveOverUSD
	needs := threshold != nil && exceeds(q.Estimate.USD, *threshold)
	out := map[string]any{"estimate": costOut(q.Estimate), "needs_approval": needs}
	if threshold != nil {
		out["approval_threshold_usd"] = *threshold
	}
	if in.EstimateOnly {
		out["status"] = "estimate"
		return nil, out, nil
	}
	if needs {
		return m.pendingGeneration(ctx, a, p, greq, detail, q.Estimate, out)
	}
	j, err := m.enqueue(ctx, a.actor, greq)
	var ge *ai.GenerateError
	if errors.As(err, &ge) && ge.Code == ai.CodeApproval {
		// The service's own check saw a larger estimate than the quote.
		return m.pendingGeneration(ctx, a, p, greq, detail, q.Estimate, out)
	}
	if err != nil {
		return nil, nil, toolError(err)
	}
	out["status"], out["job"], out["estimate"] = "queued", jobOut(j), costOut(j.Estimate)
	if j.Deduplicated {
		out["existing"] = true
		return nil, out, nil
	}
	var undo any
	if greq.PageID != "" {
		undo = map[string]string{"page_id": greq.PageID}
	}
	row, err := m.insertAction(ctx, m.d.DB, newAction{UserID: a.user.ID, TokenID: a.tok.ID, TokenName: a.tok.Name, ProjectID: p.ID,
		Kind: kindGenerate, Action: greq.Kind, TargetKind: targetKind(greq), TargetID: targetID(greq), Status: statusDone,
		Detail: detail, Undo: undo, JobID: j.ID, Estimate: j.Estimate})
	if err != nil {
		m.d.Log.ErrorContext(ctx, "mcp: record agent generation", "job", j.ID, "err", err)
		return nil, nil, toolError(err)
	}
	m.audit(ctx, a, "agent.generate", targetKind(greq), targetID(greq), p.ID, map[string]any{"action_id": row.ID, "job_id": j.ID, "kind": greq.Kind})
	out["action_id"] = row.ID
	return nil, out, nil
}

func targetKind(r genRequest) string {
	switch r.Kind {
	case "script":
		return "chapter"
	case "region":
		return "layer"
	}
	return "page"
}

func targetID(r genRequest) string {
	switch r.Kind {
	case "script":
		return r.ChapterID
	case "region":
		return r.LayerID
	}
	return r.PageID
}

// pendingGeneration stores a generation that waits for a person.
func (m *Module) pendingGeneration(ctx context.Context, a *agent, p studio.Project, greq genRequest, detail actionDetail, estimate jobs.Cost, out map[string]any) (*mcpsdk.CallToolResult, any, error) {
	detail.Generation = &greq
	detail.ThresholdUSD = a.tok.ApproveOverUSD
	row, err := m.createRequest(ctx, a, p, reqGenerate, targetKind(greq), targetID(greq), detail, estimate)
	if err != nil {
		return nil, nil, err
	}
	out["status"], out["request_id"] = "pending_approval", row.ID
	out["message"] = "The estimate is above this token's approval threshold, so nothing was queued. A person decides in Agents & MCP; check list_recent_actions."
	return nil, out, nil
}
