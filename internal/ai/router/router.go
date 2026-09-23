// Package router picks a model and a provider for each job.
//
// The pipeline is: read the job (Need: task, characters and references, text
// in art, black-and-white, aspect, resolution, series visibility, rating) →
// filter every model × provider pair (installed or keyed, VRAM, reference or
// LoRA support, rating, licence guard, local only / free only, monthly spend
// limit) → score the rest (category quality, speed and cost weighted by the
// routing mode, optionally adjusted by keep/reroll statistics) → explain (the
// pick, why, and why every other model was skipped) → fall back (the ordered
// chain the executor walks when a candidate fails).
//
// The router is pure: callers pass a snapshot (Inputs) and get a Decision.
// Reasons are codes with arguments so every screen can translate them.
package router

import (
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/MaramHarsha/MangaVeyra/internal/ai/catalog"
)

// Modes.
const (
	ModeBalanced  = "balanced"
	ModeBest      = "best"
	ModeFastest   = "fastest"
	ModeLocalOnly = "local_only"
	ModeFreeOnly  = "free_only"
)

// Modes lists every mode in display order.
var Modes = []string{ModeBalanced, ModeBest, ModeFastest, ModeLocalOnly, ModeFreeOnly}

// ValidMode reports whether m is a routing mode.
func ValidMode(m string) bool { return slices.Contains(Modes, m) }

// Pin scopes, most specific first.
const (
	PinCharacter = "character"
	PinPanelType = "panel_type"
	PinProject   = "project"
	PinSeries    = "series"
	PinJobType   = "job_type"
	PinServer    = "server"
)

// PinScopes lists pin scopes from most to least specific.
var PinScopes = []string{PinCharacter, PinPanelType, PinProject, PinSeries, PinJobType, PinServer}

// Need is what a job needs, read from its structured data.
type Need struct {
	// Kind defaults to the task's kind.
	Kind string `json:"kind,omitempty"`
	Task string `json:"task"`
	// Characters in the scope; LockedCharacters have Character Lock (refs or LoRA required).
	Characters       int    `json:"characters,omitempty"`
	LockedCharacters int    `json:"locked_characters,omitempty"`
	Refs             int    `json:"refs,omitempty"`
	LoRA             bool   `json:"lora,omitempty"`
	TextInArt        bool   `json:"text_in_art,omitempty"`
	BW               bool   `json:"bw,omitempty"`
	Layered          bool   `json:"layered,omitempty"`
	Pose             bool   `json:"pose,omitempty"`
	Aspect           string `json:"aspect,omitempty"`
	Width            int    `json:"width,omitempty"`
	Height           int    `json:"height,omitempty"`
	// Count is the number of images (variations).
	Count int `json:"count,omitempty"`
	// Seconds of video or audio; TokensIn/TokensOut estimate text jobs.
	Seconds   float64 `json:"seconds,omitempty"`
	TokensIn  int     `json:"tokens_in,omitempty"`
	TokensOut int     `json:"tokens_out,omitempty"`
	// Public / Commercial describe the series (licence guard).
	Public     bool   `json:"public,omitempty"`
	Commercial bool   `json:"commercial,omitempty"`
	Rating     string `json:"rating,omitempty"`
	// Ids used for pins.
	ProjectID   string `json:"project_id,omitempty"`
	SeriesID    string `json:"series_id,omitempty"`
	CharacterID string `json:"character_id,omitempty"`
	PanelType   string `json:"panel_type,omitempty"`
	JobType     string `json:"job_type,omitempty"`
	UserID      string `json:"user_id,omitempty"`
	// Mode, ModelID and ProviderID are one-click overrides.
	Mode       string `json:"mode,omitempty"`
	ModelID    string `json:"model_id,omitempty"`
	ProviderID string `json:"provider_id,omitempty"`
	// Label describes the job in decision logs ("p.7 panel 3 · regenerate").
	Label string `json:"label,omitempty"`
}

// KindOf returns the model kind the need asks for.
func (n Need) KindOf() string {
	if n.Kind != "" {
		return n.Kind
	}
	return catalog.KindForTask(n.Task)
}

func (n Need) count() int {
	if n.Count < 1 {
		return 1
	}
	return n.Count
}

func (n Need) size() (int, int) {
	w, h := n.Width, n.Height
	if w <= 0 || h <= 0 {
		w, h = aspectSize(n.Aspect)
	}
	return w, h
}

// aspectSize turns "2:3" into a ~1 MP size.
func aspectSize(a string) (int, int) {
	x, y, ok := strings.Cut(a, ":")
	if !ok {
		return 1024, 1024
	}
	fx, _ := strconv.ParseFloat(x, 64)
	fy, _ := strconv.ParseFloat(y, 64)
	if fx <= 0 || fy <= 0 {
		return 1024, 1024
	}
	h := math.Sqrt(1048576 * fy / fx)
	w := h * fx / fy
	return int(w/8) * 8, int(h/8) * 8
}

// tall reports a webtoon strip (4:1 or taller).
func (n Need) tall() bool {
	w, h := n.size()
	return w > 0 && float64(h)/float64(w) >= 3.9
}

// Provider is a configured provider the user may use.
type Provider struct {
	ID      string
	Kind    string
	Name    string
	Local   bool
	Enabled bool
	// Ready means the key is saved (or none is needed) and the provider can be called.
	Ready bool
}

// Worker is an online AI worker.
type Worker struct {
	ID       string
	Name     string
	Models   []string
	JobTypes []string
	// VRAMGB is the largest GPU's VRAM.
	VRAMGB float64
	// QueueSeconds is the estimated wait before a new job starts.
	QueueSeconds float64
}

// Pin forces a model (or a mode) for a scope.
type Pin struct {
	ID         string `db:"id" json:"id"`
	ScopeKind  string `db:"scope_kind" json:"scope_kind"`
	ScopeID    string `db:"scope_id" json:"scope_id"`
	Task       string `db:"task" json:"task"`
	ModelID    string `db:"model_id" json:"model_id"`
	ProviderID string `db:"provider_id" json:"provider_id"`
	Mode       string `db:"mode" json:"mode"`
	Label      string `db:"label" json:"label"`
}

// Stat is per-model usage history (keep/reroll learning, v2).
type Stat struct {
	Runs, Failures, Keeps, Rerolls int64
}

// Inputs is everything the router reads.
type Inputs struct {
	Catalog   *catalog.Catalog
	Providers []Provider
	Workers   []Worker
	// Disabled models (server-wide).
	Disabled map[string]bool
	Pins     []Pin
	// DefaultMode is the instance default; UserMode the member's override ("" = none).
	DefaultMode string
	UserMode    string
	// LocalOnly is the instance's local only mode (nothing leaves the server).
	LocalOnly    bool
	LicenceGuard bool
	// RemainingUSD is what is left of the monthly remote spend limit (negative = unlimited).
	RemainingUSD float64
	// Stats by model id (used when Learn is on).
	Stats map[string]Stat
	Learn bool
}

// Reason is a translatable explanation.
type Reason struct {
	Code string         `json:"code"`
	Args map[string]any `json:"args,omitempty"`
}

func reason(code string, kv ...any) Reason {
	r := Reason{Code: code}
	if len(kv) > 0 {
		r.Args = map[string]any{}
		for i := 0; i+1 < len(kv); i += 2 {
			r.Args[kv[i].(string)] = kv[i+1]
		}
	}
	return r
}

// Reason codes.
const (
	// Skips.
	RDisabled     = "disabled"
	RNotConnected = "not_connected"
	RNotInstalled = "not_installed"
	RVRAM         = "vram"
	RNoRefs       = "no_refs"
	RNoLoRA       = "no_lora"
	RRating       = "rating"
	RLicence      = "licence"
	RLocalOnly    = "local_only"
	RFreeOnly     = "free_only"
	RSpendLimit   = "spend_limit"
	RNewCustom    = "new_custom"
	RDeprecated   = "deprecated"
	RLowerScore   = "lower_score"
	// Why the pick.
	RPinned      = "pinned"
	ROverride    = "override"
	RLocked      = "locked_characters"
	RTextInArt   = "text_in_art"
	RBW          = "bw"
	RTall        = "tall_strip"
	RFits        = "fits_vram"
	RModeBest    = "mode_best"
	RModeFastest = "mode_fastest"
	RModeLocal   = "mode_local"
	RModeFree    = "mode_free"
	RBalanced    = "balanced"
	RFallback    = "fallback"
	RPlaceholder = "placeholder"
	RLayered     = "layered"
	RRefs        = "refs"
	// Warnings.
	WLicence     = "licence_warning"
	WPinSkipped  = "pin_unavailable"
	WDownscale   = "max_resolution"
	WRefsCapped  = "refs_capped"
	WPlaceholder = "placeholder_only"
)

// Estimate is the expected cost of one job.
type Estimate struct {
	Seconds    float64 `json:"seconds"`
	USD        float64 `json:"usd"`
	GPUSeconds float64 `json:"gpu_seconds"`
}

// Candidate is one model served by one provider.
type Candidate struct {
	ModelID       string   `json:"model_id"`
	ModelName     string   `json:"model_name"`
	ProviderID    string   `json:"provider_id"`
	ProviderKind  string   `json:"provider_kind"`
	ProviderName  string   `json:"provider_name"`
	ProviderModel string   `json:"provider_model"`
	Local         bool     `json:"local"`
	Score         float64  `json:"score"`
	Estimate      Estimate `json:"estimate"`
	Pinned        bool     `json:"pinned,omitempty"`
	Reasons       []Reason `json:"reasons,omitempty"`
	// Placeholder is the built-in no-AI provider.
	Placeholder bool `json:"placeholder,omitempty"`
}

// Decision is the router's answer.
type Decision struct {
	Task string `json:"task"`
	Mode string `json:"mode"`
	// Chain is every usable candidate, best first: Chain[0] is the pick, the rest are fallbacks.
	Chain []Candidate `json:"chain"`
	// Skipped lists models that could have done the task and why they were not used.
	Skipped  []Candidate `json:"skipped"`
	Why      []Reason    `json:"why"`
	Warnings []Reason    `json:"warnings,omitempty"`
	// PinID is the pin that forced the pick.
	PinID string `json:"pin_id,omitempty"`
	// Overridden marks a one-click override.
	Overridden bool `json:"overridden,omitempty"`
}

// Pick returns the chosen candidate (nil when nothing can run the task).
func (d *Decision) Pick() *Candidate {
	if d == nil || len(d.Chain) == 0 {
		return nil
	}
	return &d.Chain[0]
}

// Next returns the first candidate after the one that failed (by model and provider).
func (d *Decision) Next(failedModel, failedProvider string, tried map[string]bool) *Candidate {
	for i := range d.Chain {
		c := &d.Chain[i]
		key := c.ModelID + "|" + c.ProviderID
		if tried[key] || (c.ModelID == failedModel && c.ProviderID == failedProvider) {
			continue
		}
		return c
	}
	return nil
}

// ResolveMode returns the effective mode and the pin that set it.
func ResolveMode(in Inputs, need Need) (string, *Pin) {
	if in.LocalOnly {
		return ModeLocalOnly, nil
	}
	if ValidMode(need.Mode) {
		return need.Mode, nil
	}
	if p := findPin(in.Pins, need, true); p != nil {
		return p.Mode, p
	}
	if ValidMode(in.UserMode) {
		return in.UserMode, nil
	}
	if ValidMode(in.DefaultMode) {
		return in.DefaultMode, nil
	}
	return ModeBalanced, nil
}

func scopeID(n Need, kind string) string {
	switch kind {
	case PinCharacter:
		return n.CharacterID
	case PinPanelType:
		return n.PanelType
	case PinProject:
		return n.ProjectID
	case PinSeries:
		return n.SeriesID
	case PinJobType:
		if n.JobType != "" {
			return n.JobType
		}
		return n.Task
	case PinServer:
		return ""
	}
	return "\x00"
}

// findPin returns the most specific pin for the need (a mode pin when wantMode, else a model pin).
func findPin(pins []Pin, n Need, wantMode bool) *Pin {
	for _, kind := range PinScopes {
		id := scopeID(n, kind)
		if kind != PinServer && id == "" {
			continue
		}
		var best *Pin
		for i := range pins {
			p := &pins[i]
			if p.ScopeKind != kind || p.ScopeID != id {
				continue
			}
			if wantMode != (p.Mode != "") || (wantMode && !ValidMode(p.Mode)) {
				continue
			}
			if p.Task == n.Task {
				return p
			}
			if p.Task == "" && best == nil {
				best = p
			}
		}
		if best != nil {
			return best
		}
	}
	return nil
}

// category weights per task.
func quality(m *catalog.Manifest, n Need) float64 {
	type w struct {
		cat string
		v   float64
	}
	var ws []w
	switch n.Task {
	case catalog.TaskEdit, catalog.TaskInpaint:
		ws = []w{{catalog.ScoreEdit, 3}}
	case catalog.TaskSheet, catalog.TaskTrain, catalog.TaskScore:
		ws = []w{{catalog.ScoreConsistency, 3}}
	case catalog.TaskText, catalog.TaskTranslate, catalog.TaskVoice, catalog.TaskOCR:
		ws = []w{{catalog.ScoreText, 3}}
	default:
		if n.PanelType == "cover" || n.PanelType == "splash" {
			ws = []w{{catalog.ScoreCover, 3}}
		} else {
			ws = []w{{catalog.ScorePanel, 3}}
		}
	}
	if n.TextInArt && n.KindOf() == catalog.KindImage {
		ws = append(ws, w{catalog.ScoreTextInArt, 2})
	}
	if (n.LockedCharacters > 0 || n.Refs > 0 || n.Characters > 0) && n.KindOf() == catalog.KindImage && n.Task != catalog.TaskSheet {
		ws = append(ws, w{catalog.ScoreConsistency, 1.5 + float64(n.LockedCharacters)})
	}
	sum, tot := 0.0, 0.0
	for _, x := range ws {
		sum += m.Score(x.cat) * x.v
		tot += x.v
	}
	return sum / tot
}

func speedScore(m *catalog.Manifest, seconds float64) float64 {
	s := 100 * 12 / (12 + seconds)
	if v, ok := m.Scores[catalog.ScoreSpeed]; ok {
		s = (s + v) / 2
	}
	return s
}

func costScore(usd float64) float64 {
	if usd <= 0 {
		return 100
	}
	return 100 * 0.03 / (0.03 + usd)
}

func weights(mode string) (q, s, c float64) {
	switch mode {
	case ModeBest:
		return 0.94, 0.05, 0.01
	case ModeFastest:
		return 0.25, 0.7, 0.05
	}
	return 0.55, 0.25, 0.2
}

type pair struct {
	m *catalog.Manifest
	o catalog.Offer
	p Provider
}

// Route decides.
func Route(in Inputs, need Need) *Decision {
	mode, modePin := ResolveMode(in, need)
	d := &Decision{Task: need.Task, Mode: mode}
	kind := need.KindOf()
	modelPin := findPin(in.Pins, need, false)
	if need.ModelID != "" {
		modelPin = &Pin{ModelID: need.ModelID, ProviderID: need.ProviderID}
		d.Overridden = true
	}
	w, h := need.size()

	// Pair every model of the right kind with every provider serving it.
	type scored struct {
		c       Candidate
		placeh  bool
		custom  bool
		hardErr []Reason
		penalty []Reason
	}
	var viable, skipped []scored
	byModel := map[string]bool{}
	for _, m := range in.Catalog.All() {
		if m.Kind != kind || !m.Supports(need.Task) {
			continue
		}
		pinned := modelPin != nil && modelPin.ModelID == m.ID
		var modelSkips []scored
		for _, o := range m.Providers {
			pairs := providersFor(in, m, o)
			if len(pairs) == 0 {
				c := Candidate{ModelID: m.ID, ModelName: m.Name, ProviderKind: o.Provider, ProviderModel: o.ModelID, Local: !o.Remote()}
				r := reason(RNotConnected, "provider", o.Provider)
				if o.Provider == "local_worker" {
					r = localReason(in, m)
				}
				modelSkips = append(modelSkips, scored{c: c, hardErr: []Reason{r}})
				continue
			}
			for _, pp := range pairs {
				c := Candidate{ModelID: m.ID, ModelName: m.Name, ProviderID: pp.p.ID, ProviderKind: pp.p.Kind, ProviderName: pp.p.Name,
					ProviderModel: pp.o.ModelID, Local: pp.p.Local, Pinned: pinned, Placeholder: pp.p.Kind == "placeholder"}
				if pinned && modelPin.ProviderID != "" && modelPin.ProviderID != pp.p.ID {
					c.Pinned = false
				}
				c.Estimate = estimate(in, m, pp, need, w, h)
				hard, soft := filter(in, m, pp, need, mode, c.Estimate, w, h, c.Pinned)
				s := scored{c: c, placeh: c.Placeholder, custom: m.Source == catalog.SourceCustom, hardErr: hard, penalty: soft}
				if len(hard) > 0 {
					modelSkips = append(modelSkips, s)
					continue
				}
				q := quality(m, need)
				if in.Learn {
					if st, ok := in.Stats[m.ID]; ok && st.Runs >= 5 {
						keep := float64(st.Keeps+1) / float64(st.Keeps+st.Rerolls+2)
						q += (keep - 0.5) * 20
						q -= float64(st.Failures) / float64(st.Runs) * 30
					}
				}
				for _, p := range soft {
					switch p.Code {
					case RTextInArt:
						q -= 20
					case RBW, RTall:
						q -= 12
					case WDownscale, WRefsCapped:
						q -= 8
					}
				}
				qw, sw, cw := weights(mode)
				s.c.Score = math.Round((qw*q+sw*speedScore(m, c.Estimate.Seconds/float64(need.count()))+cw*costScore(c.Estimate.USD))*10) / 10
				viable = append(viable, s)
				byModel[m.ID] = true
			}
		}
		if !byModel[m.ID] && len(modelSkips) > 0 {
			// Report the most relevant reason for the model once (prefer policy reasons over "not connected").
			sort.SliceStable(modelSkips, func(i, j int) bool { return skipRank(modelSkips[i].hardErr) < skipRank(modelSkips[j].hardErr) })
			s := modelSkips[0]
			s.c.Reasons = s.hardErr
			skipped = append(skipped, s)
			if pinned {
				d.Warnings = append(d.Warnings, reason(WPinSkipped, "model", m.Name, "why", s.hardErr[0].Code))
			}
		}
	}

	// Custom models with no history join auto-routing only when pinned (or nothing else can run).
	var real, placeholders, fresh []scored
	for _, s := range viable {
		switch {
		case s.placeh:
			placeholders = append(placeholders, s)
		case s.custom && !s.c.Pinned && in.Stats[s.c.ModelID].Runs < 3:
			fresh = append(fresh, s)
		default:
			real = append(real, s)
		}
	}
	sortScored := func(list []scored) {
		sort.SliceStable(list, func(i, j int) bool {
			a, b := list[i].c, list[j].c
			if a.Pinned != b.Pinned {
				return a.Pinned
			}
			if a.Score != b.Score {
				return a.Score > b.Score
			}
			if a.Local != b.Local {
				return a.Local
			}
			return a.Estimate.USD < b.Estimate.USD
		})
	}
	sortScored(real)
	sortScored(fresh)
	for _, s := range real {
		d.Chain = append(d.Chain, s.c)
	}
	freshIDs := map[string]bool{}
	for _, s := range fresh {
		freshIDs[s.c.ModelID] = true
		if len(real) > 0 {
			c := s.c
			c.Reasons = []Reason{reason(RNewCustom)}
			skipped = append(skipped, scored{c: c})
		}
		d.Chain = append(d.Chain, s.c)
	}
	for _, s := range placeholders {
		d.Chain = append(d.Chain, s.c)
	}

	// Models that could run but lost on score are "skipped: lower score" (one entry per model).
	seen := map[string]bool{}
	if len(d.Chain) > 0 {
		seen[d.Chain[0].ModelID] = true
	}
	for i := 1; i < len(d.Chain); i++ {
		c := d.Chain[i]
		if seen[c.ModelID] || c.Placeholder || freshIDs[c.ModelID] {
			seen[c.ModelID] = true
			continue
		}
		seen[c.ModelID] = true
		lc := c
		lc.Reasons = []Reason{reason(RLowerScore, "score", c.Score)}
		d.Skipped = append(d.Skipped, lc)
	}
	for _, s := range skipped {
		d.Skipped = append(d.Skipped, s.c)
	}
	// Policy skips first (licence, VRAM, local only) — they are the ones people need to see.
	sort.SliceStable(d.Skipped, func(i, j int) bool { return skipRank(d.Skipped[i].Reasons) < skipRank(d.Skipped[j].Reasons) })

	pick := d.Pick()
	if pick == nil {
		return d
	}
	// Why the pick.
	switch {
	case d.Overridden && pick.Pinned:
		d.Why = append(d.Why, reason(ROverride))
	case pick.Pinned && modelPin != nil:
		d.Why = append(d.Why, reason(RPinned, "scope", modelPin.ScopeKind, "label", modelPin.Label))
		d.PinID = modelPin.ID
	}
	if modePin != nil && d.PinID == "" {
		d.PinID = modePin.ID
	}
	if pick.Placeholder {
		d.Why = append(d.Why, reason(RPlaceholder))
		d.Warnings = append(d.Warnings, reason(WPlaceholder))
		return d
	}
	m, _ := in.Catalog.Get(pick.ModelID)
	if need.LockedCharacters > 0 {
		d.Why = append(d.Why, reason(RLocked, "count", need.LockedCharacters))
	} else if need.Refs > 0 && m.Capabilities.Refs {
		d.Why = append(d.Why, reason(RRefs, "count", need.Refs))
	}
	if need.TextInArt && m.Capabilities.TextInArt {
		d.Why = append(d.Why, reason(RTextInArt))
	}
	if need.Layered && m.Capabilities.Layered {
		d.Why = append(d.Why, reason(RLayered))
	}
	if need.BW && m.Capabilities.BWScreentone {
		d.Why = append(d.Why, reason(RBW))
	}
	if need.tall() && m.Capabilities.TallStrips {
		d.Why = append(d.Why, reason(RTall))
	}
	if pick.ProviderKind == "local_worker" {
		if v := maxVRAM(in.Workers); v > 0 {
			d.Why = append(d.Why, reason(RFits, "gb", v))
		}
	}
	switch mode {
	case ModeBest:
		d.Why = append(d.Why, reason(RModeBest))
	case ModeFastest:
		d.Why = append(d.Why, reason(RModeFastest))
	case ModeLocalOnly:
		d.Why = append(d.Why, reason(RModeLocal))
	case ModeFreeOnly:
		d.Why = append(d.Why, reason(RModeFree))
	default:
		if len(d.Why) == 0 {
			d.Why = append(d.Why, reason(RBalanced))
		}
	}
	for _, s := range real {
		if s.c.ModelID == pick.ModelID && s.c.ProviderID == pick.ProviderID {
			for _, p := range s.penalty {
				if p.Code == WLicence || p.Code == WDownscale || p.Code == WRefsCapped {
					d.Warnings = append(d.Warnings, p)
				}
			}
		}
	}
	return d
}

func skipRank(rs []Reason) int {
	if len(rs) == 0 {
		return 9
	}
	switch rs[0].Code {
	case RLicence:
		return 0
	case RVRAM:
		return 1
	case RLocalOnly, RFreeOnly, RSpendLimit:
		return 2
	case RNoRefs, RNoLoRA, RRating:
		return 3
	case RLowerScore:
		return 4
	case RNewCustom:
		return 5
	case RDisabled, RDeprecated:
		return 6
	case RNotInstalled:
		return 7
	}
	return 8
}

func maxVRAM(ws []Worker) float64 {
	v := 0.0
	for _, w := range ws {
		v = math.Max(v, w.VRAMGB)
	}
	return v
}

// providersFor returns the configured providers that can serve offer o of m.
func providersFor(in Inputs, m *catalog.Manifest, o catalog.Offer) []pair {
	var out []pair
	for _, p := range in.Providers {
		if !p.Enabled || !p.Ready {
			continue
		}
		if o.ProviderID != "" {
			if o.ProviderID == p.ID {
				out = append(out, pair{m, o, p})
			}
			continue
		}
		if o.Provider != p.Kind {
			continue
		}
		if p.Kind == "local_worker" && !workerServes(in.Workers, m) {
			continue
		}
		out = append(out, pair{m, o, p})
	}
	return out
}

// workerServes reports whether an online worker can run m.
func workerServes(ws []Worker, m *catalog.Manifest) bool {
	for _, w := range ws {
		if slices.Contains(w.Models, m.ID) {
			return true
		}
		if m.Hardware.AnyWorker {
			for _, t := range m.Capabilities.Tasks {
				if jt := catalog.JobTypeForTask(t); jt != "" && slices.Contains(w.JobTypes, jt) {
					return true
				}
			}
		}
	}
	return false
}

// Availability says whether a model can run for this snapshot, on which
// provider (the cheapest ready one, local first) and, if not, why.
type Availability struct {
	OK       bool
	Provider Provider
	Offer    catalog.Offer
	Reason   Reason
}

// Available checks one model without a task (the G2 models table).
func Available(in Inputs, m *catalog.Manifest) Availability {
	if in.Disabled[m.ID] {
		var a Availability
		for _, o := range m.Providers {
			if ps := providersFor(in, m, o); len(ps) > 0 {
				a.Provider, a.Offer = ps[0].p, ps[0].o
				break
			}
		}
		a.Reason = reason(RDisabled)
		return a
	}
	var best *pair
	for _, o := range m.Providers {
		for _, pp := range providersFor(in, m, o) {
			pp := pp
			if best == nil || (pp.p.Local && !best.p.Local) ||
				(pp.p.Local == best.p.Local && pp.o.Price.Estimate(1024, 1536, 1, 1000, 500) < best.o.Price.Estimate(1024, 1536, 1, 1000, 500)) {
				best = &pp
			}
		}
	}
	if best != nil {
		return Availability{OK: true, Provider: best.p, Offer: best.o}
	}
	var r Reason
	for _, o := range m.Providers {
		if o.Provider == "local_worker" {
			r = localReason(in, m)
			if r.Code == RVRAM {
				break
			}
		} else if r.Code == "" {
			r = reason(RNotConnected, "provider", o.Provider)
		}
	}
	return Availability{Reason: r}
}

// localReason explains why a local model cannot run: VRAM or not installed.
func localReason(in Inputs, m *catalog.Manifest) Reason {
	have := maxVRAM(in.Workers)
	need := m.Hardware.MinVRAMGB
	if need == 0 {
		need = m.Hardware.VRAMGB
	}
	if len(in.Workers) > 0 && need > 0 && have > 0 && have < need {
		return reason(RVRAM, "need", need, "have", have)
	}
	return reason(RNotInstalled)
}

func estimate(in Inputs, m *catalog.Manifest, pp pair, n Need, w, h int) Estimate {
	count := float64(n.count())
	secs := pp.o.Seconds
	if secs <= 0 {
		secs = 20
	}
	var e Estimate
	switch m.Kind {
	case catalog.KindText:
		tokOut := n.TokensOut
		if tokOut <= 0 {
			tokOut = 800
		}
		tokIn := n.TokensIn
		if tokIn <= 0 {
			tokIn = 1500
		}
		e.Seconds = secs * float64(tokOut) / 1000
		e.USD = pp.o.Price.Estimate(0, 0, 0, tokIn, tokOut)
	case catalog.KindVideo, catalog.KindVoice:
		dur := n.Seconds
		if dur <= 0 {
			dur = 5
		}
		e.Seconds = secs * dur
		if m.Kind == catalog.KindVoice {
			e.Seconds = math.Max(1, secs*dur)
		}
		e.USD = pp.o.Price.Estimate(w, h, dur, 0, 0)
	default:
		e.Seconds = secs * count
		e.USD = pp.o.Price.Estimate(w, h, 0, 0, 0) * count
	}
	if pp.p.Kind == "local_worker" {
		e.GPUSeconds = e.Seconds
		e.USD = 0
		best := -1.0
		for _, wk := range in.Workers {
			if slices.Contains(wk.Models, m.ID) || m.Hardware.AnyWorker {
				if best < 0 || wk.QueueSeconds < best {
					best = wk.QueueSeconds
				}
			}
		}
		if best > 0 {
			e.Seconds += best
		}
	}
	if pp.p.Local && pp.p.Kind != "local_worker" {
		e.USD = 0
	}
	e.USD = math.Round(e.USD*10000) / 10000
	e.Seconds = math.Round(e.Seconds*10) / 10
	return e
}

// filter returns hard reasons (skip) and soft penalties.
func filter(in Inputs, m *catalog.Manifest, pp pair, n Need, mode string, est Estimate, w, h int, pinned bool) (hard, soft []Reason) {
	caps := m.Capabilities
	if in.Disabled[m.ID] {
		hard = append(hard, reason(RDisabled))
	}
	if m.Deprecated && !pinned {
		hard = append(hard, reason(RDeprecated))
	}
	if n.LockedCharacters > 0 && !caps.Refs && !caps.LoRA {
		hard = append(hard, reason(RNoRefs, "count", n.LockedCharacters))
	} else if n.Refs > 0 && !caps.Refs && !caps.LoRA && n.Task != catalog.TaskUpscale {
		hard = append(hard, reason(RNoRefs, "count", n.Refs))
	}
	if n.LoRA && !caps.LoRA {
		hard = append(hard, reason(RNoLoRA))
	}
	if !m.AllowsRating(n.Rating) {
		hard = append(hard, reason(RRating, "rating", n.Rating))
	}
	if !m.Licence.Commercial && (n.Public || n.Commercial) {
		what := "public"
		if n.Commercial {
			what = "commercial"
		}
		if in.LicenceGuard {
			hard = append(hard, reason(RLicence, "licence", m.Licence.ID, "series", what))
		} else {
			soft = append(soft, reason(WLicence, "licence", m.Licence.ID, "series", what))
		}
	}
	remote := !pp.p.Local
	if remote && (mode == ModeLocalOnly || in.LocalOnly) {
		hard = append(hard, reason(RLocalOnly))
	}
	if remote && mode == ModeFreeOnly && !pp.o.Free && est.USD > 0 {
		hard = append(hard, reason(RFreeOnly))
	}
	if remote && in.RemainingUSD >= 0 && est.USD > in.RemainingUSD+1e-9 {
		hard = append(hard, reason(RSpendLimit, "remaining", math.Max(0, in.RemainingUSD)))
	}
	if n.TextInArt && !caps.TextInArt && m.Kind == catalog.KindImage {
		soft = append(soft, reason(RTextInArt))
	}
	if n.BW && !caps.BWScreentone && m.Kind == catalog.KindImage {
		soft = append(soft, reason(RBW))
	}
	if n.tall() && !caps.TallStrips && m.Kind == catalog.KindImage {
		soft = append(soft, reason(RTall))
	}
	if caps.MaxResolution > 0 && max(w, h) > caps.MaxResolution {
		soft = append(soft, reason(WDownscale, "max", caps.MaxResolution))
	}
	if caps.MaxRefs > 0 && n.Refs > caps.MaxRefs {
		soft = append(soft, reason(WRefsCapped, "max", caps.MaxRefs))
	}
	return hard, soft
}

// ReadPrompt fills need fields a prompt makes obvious (rules only): quoted
// words or "reads"/"sign"/"says" mean text in the art; "black and white",
// "screentone" or "b/w" mean B/W. The optional text-model intent reader
// refines this when configured.
func ReadPrompt(need *Need, prompt string) {
	p := strings.ToLower(prompt)
	for _, k := range []string{"\"", "“", "「", " reads ", " sign ", " says ", "title text", "logo", "caption", "lettering", "poster"} {
		if strings.Contains(p, k) {
			need.TextInArt = true
			break
		}
	}
	for _, k := range []string{"black and white", "black-and-white", "b/w", "screentone", "monochrome", "greyscale", "grayscale"} {
		if strings.Contains(p, k) {
			need.BW = true
			break
		}
	}
}
