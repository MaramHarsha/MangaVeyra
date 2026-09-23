// Package catalog is the model manifest format and the embedded catalogue.
//
// Every model MangaVeyra can route to is a small manifest: what it can do
// (capabilities), under which terms (licence), on which hardware, which
// providers serve it (with the provider-side model id and published cost on
// the user's own key) and how well it does per task category (scores). The
// catalogue ships as YAML files embedded from this directory; admins add or
// override manifests with files in <data dir>/manifests and custom
// connections add manifests stored in the database. New models therefore
// arrive as manifest files or plugins, without a release.
package catalog

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Kinds of model.
const (
	KindImage   = "image"
	KindText    = "text"
	KindVideo   = "video"
	KindVoice   = "voice"
	KindOCR     = "ocr"
	KindUpscale = "upscale"
)

// Kinds lists every kind in display order (G2 tabs).
var Kinds = []string{KindImage, KindText, KindVideo, KindVoice, KindOCR, KindUpscale}

// Tasks the router routes.
const (
	TaskGenerate  = "generate"
	TaskEdit      = "edit"
	TaskInpaint   = "inpaint"
	TaskSheet     = "sheet"
	TaskTrain     = "train"
	TaskScore     = "score"
	TaskText      = "text"
	TaskTranslate = "translate"
	TaskVideo     = "video"
	TaskVoice     = "voice"
	TaskOCR       = "ocr"
	TaskUpscale   = "upscale"
)

// Tasks lists every task.
var Tasks = []string{TaskGenerate, TaskEdit, TaskInpaint, TaskSheet, TaskTrain, TaskScore, TaskText, TaskTranslate,
	TaskVideo, TaskVoice, TaskOCR, TaskUpscale}

// KindForTask returns the model kind that serves a task.
func KindForTask(task string) string {
	switch task {
	case TaskText, TaskTranslate:
		return KindText
	case TaskVideo:
		return KindVideo
	case TaskVoice:
		return KindVoice
	case TaskOCR:
		return KindOCR
	case TaskUpscale:
		return KindUpscale
	}
	return KindImage
}

// JobTypeForTask maps a routed task to the worker protocol job type (sdk/api/worker.md).
func JobTypeForTask(task string) string {
	switch task {
	case TaskGenerate:
		return "image.generate"
	case TaskEdit:
		return "image.edit"
	case TaskInpaint:
		return "image.inpaint"
	case TaskSheet:
		return "character.sheet"
	case TaskTrain:
		return "character.train"
	case TaskScore:
		return "character.score"
	case TaskOCR:
		return "ocr"
	case TaskUpscale:
		return "upscale"
	case TaskVideo:
		return "video.animate"
	case TaskVoice:
		return "voice.tts"
	}
	return ""
}

// Score categories.
const (
	ScorePanel       = "panel"
	ScoreCover       = "cover"
	ScoreEdit        = "edit"
	ScoreTextInArt   = "text_in_art"
	ScoreConsistency = "character_consistency"
	ScoreSpeed       = "speed"
	ScoreText        = "text"
)

// Price units (cost on the user's own key, as the provider publishes it).
const (
	PerImage     = "per_image"
	PerMegapixel = "per_megapixel"
	PerSecond    = "per_second"
	Per1kTokens  = "per_1k_tokens"
)

// Manifest describes one model.
type Manifest struct {
	ID      string `yaml:"id" json:"id"`
	Name    string `yaml:"name" json:"name"`
	Maker   string `yaml:"maker" json:"maker"`
	Kind    string `yaml:"kind" json:"kind"`
	Family  string `yaml:"family,omitempty" json:"family,omitempty"`
	Summary string `yaml:"summary,omitempty" json:"summary,omitempty"`
	// Uses are what the router sends to this model (shown in G2 "Router uses it for").
	Uses         []string     `yaml:"uses,omitempty" json:"uses,omitempty"`
	Capabilities Capabilities `yaml:"capabilities" json:"capabilities"`
	Licence      Licence      `yaml:"licence" json:"licence"`
	Hardware     Hardware     `yaml:"hardware" json:"hardware"`
	Providers    []Offer      `yaml:"providers" json:"providers"`
	// Scores are 0–100 per category (panel, cover, edit, text_in_art, character_consistency, speed, text).
	Scores map[string]float64 `yaml:"scores,omitempty" json:"scores,omitempty"`
	// Ratings are the content ratings the model may be used for ("all", "teen", "mature", "adult"); empty = all but adult.
	Ratings []string `yaml:"ratings,omitempty" json:"ratings,omitempty"`
	Release string   `yaml:"release,omitempty" json:"release,omitempty"`
	// Verify notes when ids and costs were last checked.
	Verify string `yaml:"verify,omitempty" json:"verify,omitempty"`
	// Deprecated models stay routable only when pinned.
	Deprecated bool `yaml:"deprecated,omitempty" json:"deprecated,omitempty"`
	// Worker carries backend settings for the Python worker (see worker/mangaveyra_worker/manifests.py).
	Worker map[string]any `yaml:"worker,omitempty" json:"worker,omitempty"`

	// Source is where the manifest came from: builtin, file, custom, plugin, worker (not stored in files).
	Source string `yaml:"-" json:"source,omitempty"`
}

// Capabilities describe what a model can do.
type Capabilities struct {
	Tasks []string `yaml:"tasks" json:"tasks"`
	// Refs is reference-image support (character sheets); MaxRefs caps them.
	Refs    bool `yaml:"refs,omitempty" json:"refs,omitempty"`
	MaxRefs int  `yaml:"max_refs,omitempty" json:"max_refs,omitempty"`
	LoRA    bool `yaml:"lora,omitempty" json:"lora,omitempty"`
	Edit    bool `yaml:"edit,omitempty" json:"edit,omitempty"`
	Inpaint bool `yaml:"inpaint,omitempty" json:"inpaint,omitempty"`
	// Layered outputs separate layers (background / characters / FX).
	Layered      bool `yaml:"layered,omitempty" json:"layered,omitempty"`
	TextInArt    bool `yaml:"text_in_art,omitempty" json:"text_in_art,omitempty"`
	BWScreentone bool `yaml:"bw_screentone,omitempty" json:"bw_screentone,omitempty"`
	Colour       bool `yaml:"colour,omitempty" json:"colour,omitempty"`
	// AspectRatios lists supported ratios ("2:3", "1:8"); empty = any.
	AspectRatios []string `yaml:"aspect_ratios,omitempty" json:"aspect_ratios,omitempty"`
	// TallStrips means ratios of 1:4 or taller work (webtoon strips).
	TallStrips bool `yaml:"tall_strips,omitempty" json:"tall_strips,omitempty"`
	// MaxResolution is the longest side in pixels (0 = unknown).
	MaxResolution int  `yaml:"max_resolution,omitempty" json:"max_resolution,omitempty"`
	Pose          bool `yaml:"pose,omitempty" json:"pose,omitempty"`
	ControlNet    bool `yaml:"controlnet,omitempty" json:"controlnet,omitempty"`
	// Context is the context window in tokens (text models).
	Context int `yaml:"context,omitempty" json:"context,omitempty"`
	// MaxSeconds is the longest clip (video, voice).
	MaxSeconds float64 `yaml:"max_seconds,omitempty" json:"max_seconds,omitempty"`
	// Languages the model reads or writes well (text in art, OCR, voice).
	Languages []string `yaml:"languages,omitempty" json:"languages,omitempty"`
}

// Licence are the model's usage terms.
type Licence struct {
	ID         string `yaml:"id" json:"id"`
	Commercial bool   `yaml:"commercial" json:"commercial"`
	Note       string `yaml:"note,omitempty" json:"note,omitempty"`
}

// Hardware describes local requirements.
type Hardware struct {
	// VRAMGB is comfortable VRAM, MinVRAMGB the minimum with offloading.
	VRAMGB    float64 `yaml:"vram_gb,omitempty" json:"vram_gb,omitempty"`
	MinVRAMGB float64 `yaml:"min_vram_gb,omitempty" json:"min_vram_gb,omitempty"`
	// Local means open weights that run in the local worker.
	Local bool `yaml:"local" json:"local"`
	// DownloadGB is the weight download size.
	DownloadGB float64 `yaml:"download_gb,omitempty" json:"download_gb,omitempty"`
	// AnyWorker means any worker offering the job type can run it (model-agnostic engines such as OCR).
	AnyWorker bool `yaml:"any_worker,omitempty" json:"any_worker,omitempty"`
}

// Offer is one provider serving the model.
type Offer struct {
	// Provider is a provider kind ("fal", "openrouter", "local_worker"...).
	Provider string `yaml:"provider" json:"provider"`
	// ProviderID pins the offer to one configured provider (custom connections).
	ProviderID string `yaml:"provider_id,omitempty" json:"provider_id,omitempty"`
	// ModelID is the provider-side model name.
	ModelID string `yaml:"model_id" json:"model_id"`
	Price   Price  `yaml:"price,omitempty" json:"price,omitempty"`
	// Seconds is the typical time for one result (image, 1k tokens of text, or one second of video).
	Seconds float64 `yaml:"seconds,omitempty" json:"seconds,omitempty"`
	// Free marks free tiers ("free only" mode).
	Free  bool   `yaml:"free,omitempty" json:"free,omitempty"`
	Notes string `yaml:"notes,omitempty" json:"notes,omitempty"`
}

// Price is the published cost on the user's own key.
type Price struct {
	Unit string  `yaml:"unit,omitempty" json:"unit,omitempty"`
	USD  float64 `yaml:"usd,omitempty" json:"usd,omitempty"`
	// USDOut is the output-token rate for text models (USD is the input rate).
	USDOut float64 `yaml:"usd_out,omitempty" json:"usd_out,omitempty"`
}

// Remote reports whether the offer sends data off the server.
func (o Offer) Remote() bool { return !IsLocalProvider(o.Provider) }

// IsLocalProvider reports provider kinds that run on the operator's own hardware.
func IsLocalProvider(kind string) bool {
	switch kind {
	case "local_worker", "placeholder", "comfyui", "ollama", "openai_compatible_local":
		return true
	}
	return false
}

// Estimate returns the USD cost of one result: an image of w×h pixels, text of
// tokensIn/tokensOut tokens or seconds of audio/video.
func (p Price) Estimate(w, h int, seconds float64, tokensIn, tokensOut int) float64 {
	switch p.Unit {
	case PerImage:
		return p.USD
	case PerMegapixel:
		mp := float64(w*h) / 1e6
		if mp <= 0 {
			mp = 1
		}
		return p.USD * mp
	case PerSecond:
		return p.USD * seconds
	case Per1kTokens:
		out := p.USDOut
		if out == 0 {
			out = p.USD
		}
		return p.USD*float64(tokensIn)/1000 + out*float64(tokensOut)/1000
	}
	return 0
}

// Supports reports whether the model lists task.
func (m *Manifest) Supports(task string) bool { return slices.Contains(m.Capabilities.Tasks, task) }

// Score returns the category score (50 = neutral when unknown).
func (m *Manifest) Score(cat string) float64 {
	if v, ok := m.Scores[cat]; ok {
		return v
	}
	return 50
}

// AllowsRating reports whether content rating r may be generated with this model.
func (m *Manifest) AllowsRating(r string) bool {
	if r == "" {
		r = "all"
	}
	if len(m.Ratings) == 0 {
		return r != "adult"
	}
	return slices.Contains(m.Ratings, r)
}

// Offer returns the offer for a provider kind (or provider id).
func (m *Manifest) Offer(kindOrID string) (Offer, bool) {
	for _, o := range m.Providers {
		if o.ProviderID == kindOrID || (o.ProviderID == "" && o.Provider == kindOrID) {
			return o, true
		}
	}
	return Offer{}, false
}

// Local reports whether any offer runs on local hardware.
func (m *Manifest) Local() bool {
	for _, o := range m.Providers {
		if !o.Remote() {
			return true
		}
	}
	return false
}

var idRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,79}$`)

// ValidID reports whether id is a valid manifest id (lowercase, digits, . _ -).
func ValidID(id string) bool { return idRe.MatchString(id) }

// Validate checks required fields and enumerations.
func (m *Manifest) Validate() error {
	var errs []error
	if !ValidID(m.ID) {
		errs = append(errs, fmt.Errorf("id %q: lowercase letters, digits, dot, dash and underscore only", m.ID))
	}
	if strings.TrimSpace(m.Name) == "" {
		errs = append(errs, errors.New("name is required"))
	}
	if !slices.Contains(Kinds, m.Kind) {
		errs = append(errs, fmt.Errorf("kind %q is not one of %s", m.Kind, strings.Join(Kinds, ", ")))
	}
	if len(m.Capabilities.Tasks) == 0 {
		errs = append(errs, errors.New("capabilities.tasks is empty"))
	}
	for _, t := range m.Capabilities.Tasks {
		if !slices.Contains(Tasks, t) {
			errs = append(errs, fmt.Errorf("unknown task %q", t))
		}
	}
	if m.Licence.ID == "" {
		errs = append(errs, errors.New("licence.id is required"))
	}
	if len(m.Providers) == 0 {
		errs = append(errs, errors.New("at least one provider is required"))
	}
	for i, o := range m.Providers {
		if o.Provider == "" || o.ModelID == "" {
			errs = append(errs, fmt.Errorf("providers[%d]: provider and model_id are required", i))
		}
		switch o.Price.Unit {
		case "", PerImage, PerMegapixel, PerSecond, Per1kTokens:
		default:
			errs = append(errs, fmt.Errorf("providers[%d]: unknown price unit %q", i, o.Price.Unit))
		}
		if o.Price.USD < 0 || o.Price.USDOut < 0 {
			errs = append(errs, fmt.Errorf("providers[%d]: negative cost", i))
		}
	}
	for k, v := range m.Scores {
		if v < 0 || v > 100 {
			errs = append(errs, fmt.Errorf("score %s=%v out of 0–100", k, v))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("manifest %s: %w", m.ID, errors.Join(errs...))
	}
	return nil
}

// Clone returns a deep-enough copy (slices and maps are copied).
func (m *Manifest) Clone() *Manifest {
	c := *m
	c.Uses = slices.Clone(m.Uses)
	c.Providers = slices.Clone(m.Providers)
	c.Ratings = slices.Clone(m.Ratings)
	c.Capabilities.Tasks = slices.Clone(m.Capabilities.Tasks)
	c.Capabilities.AspectRatios = slices.Clone(m.Capabilities.AspectRatios)
	c.Capabilities.Languages = slices.Clone(m.Capabilities.Languages)
	if m.Scores != nil {
		c.Scores = make(map[string]float64, len(m.Scores))
		for k, v := range m.Scores {
			c.Scores[k] = v
		}
	}
	return &c
}
