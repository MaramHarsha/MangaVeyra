package ai

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/a-h/templ"

	"github.com/MaramHarsha/MangaVeyra/internal/ai/router"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/web/ui"
)

// Alternative is another candidate with why it was not picked.
type Alternative struct {
	ModelID    string          `json:"model_id"`
	ProviderID string          `json:"provider_id,omitempty"`
	Label      string          `json:"label"`
	Why        string          `json:"why"`
	Estimate   router.Estimate `json:"estimate"`
	Usable     bool            `json:"usable"`
}

// Explanation is the routing chip data: "Auto → Qwen-Image Edit · local · ~40 s", why, and alternatives.
type Explanation struct {
	Label string `json:"label"`
	// Auto is the chip prefix: "Auto", "Pinned" or "Manual".
	Auto         string           `json:"auto"`
	Pick         string           `json:"pick"`
	Why          string           `json:"why"`
	Mode         string           `json:"mode"`
	ModeLabel    string           `json:"mode_label"`
	ModelID      string           `json:"model_id"`
	ModelName    string           `json:"model_name"`
	ProviderID   string           `json:"provider_id"`
	ProviderName string           `json:"provider_name"`
	Local        bool             `json:"local"`
	Placeholder  bool             `json:"placeholder,omitempty"`
	Estimate     router.Estimate  `json:"estimate"`
	EstimateText string           `json:"estimate_text"`
	Alternatives []Alternative    `json:"alternatives"`
	Skipped      []Alternative    `json:"skipped"`
	Warnings     []string         `json:"warnings,omitempty"`
	Decision     *router.Decision `json:"-"`
}

// Explain routes need (without running anything) and describes the decision
// in the request's language. Other modules use it for routing chips and
// compute previews on generate buttons.
func (s *Service) Explain(ctx context.Context, need router.Need, prompt string) (*Explanation, error) {
	dec, err := s.Route(ctx, need, prompt)
	if err != nil {
		return nil, err
	}
	return s.Describe(ctx, dec), nil
}

// Chip renders the routing chip for generate buttons: the pick with its
// estimate and a "Why" link to the routing screen (ui.RouteChip).
func (e *Explanation) Chip(ctx context.Context) templ.Component {
	auto, pick := e.Auto, e.Pick
	if pick == "" {
		auto, pick = "", e.Label
	}
	return ui.RouteChip(auto, pick, "/models/routing", i18n.T(ctx, "ai.chip.why"))
}

// Estimate returns the expected cost of need with the current routing.
func (s *Service) Estimate(ctx context.Context, need router.Need) (router.Estimate, error) {
	dec, err := s.Route(ctx, need, "")
	if err != nil {
		return router.Estimate{}, err
	}
	if p := dec.Pick(); p != nil {
		return p.Estimate, nil
	}
	return router.Estimate{}, ErrNoModel
}

// Describe turns a decision into translated text.
func (s *Service) Describe(ctx context.Context, dec *router.Decision) *Explanation {
	e := &Explanation{Mode: dec.Mode, ModeLabel: i18n.T(ctx, "ai.mode."+dec.Mode), Decision: dec}
	p := dec.Pick()
	if p == nil {
		e.Label = i18n.T(ctx, "ai.chip.none")
		e.Why = i18n.T(ctx, "ai.chip.none_why")
		for _, c := range dec.Skipped {
			e.Skipped = append(e.Skipped, s.alternative(ctx, c, false))
		}
		return e
	}
	e.ModelID, e.ModelName, e.ProviderID, e.ProviderName = p.ModelID, p.ModelName, p.ProviderID, p.ProviderName
	e.Local, e.Placeholder, e.Estimate = p.Local, p.Placeholder, p.Estimate
	e.EstimateText = EstimateText(ctx, p.Estimate, p.Local)
	e.Pick = PickLabel(ctx, *p)
	prefix := i18n.T(ctx, "ai.chip.auto")
	if dec.Overridden {
		prefix = i18n.T(ctx, "ai.chip.manual")
	} else if dec.PinID != "" && len(dec.Why) > 0 && dec.Why[0].Code == router.RPinned {
		prefix = i18n.T(ctx, "ai.chip.pinned")
	}
	e.Auto = prefix
	e.Label = prefix + " → " + e.Pick
	var why []string
	for _, r := range dec.Why {
		why = append(why, ReasonText(ctx, r))
	}
	e.Why = strings.Join(why, ", ")
	for _, w := range dec.Warnings {
		e.Warnings = append(e.Warnings, ReasonText(ctx, w))
	}
	for i, c := range dec.Chain {
		if i == 0 || c.Placeholder {
			continue
		}
		if len(e.Alternatives) >= 3 {
			break
		}
		e.Alternatives = append(e.Alternatives, s.alternative(ctx, c, true))
	}
	for _, c := range dec.Skipped {
		if len(c.Reasons) > 0 && c.Reasons[0].Code == router.RLowerScore {
			continue
		}
		e.Skipped = append(e.Skipped, s.alternative(ctx, c, false))
	}
	return e
}

func (s *Service) alternative(ctx context.Context, c router.Candidate, usable bool) Alternative {
	a := Alternative{ModelID: c.ModelID, ProviderID: c.ProviderID, Estimate: c.Estimate, Usable: usable, Label: c.ModelName}
	if usable {
		a.Label = PickLabel(ctx, c)
		a.Why = i18n.T(ctx, "ai.reason.lower_score", "score", fmt.Sprintf("%.0f", c.Score))
		return a
	}
	var why []string
	for _, r := range c.Reasons {
		why = append(why, ReasonText(ctx, r))
	}
	a.Why = strings.Join(why, ", ")
	return a
}

// PickLabel is "Qwen-Image Edit · local · ~40 s" or "Nano Banana Pro · OpenRouter · ~$0.13".
func PickLabel(ctx context.Context, c router.Candidate) string {
	where := c.ProviderName
	switch {
	case c.Placeholder:
		return i18n.T(ctx, "ai.chip.placeholder")
	case c.ProviderKind == "local_worker":
		where = i18n.T(ctx, "ai.chip.local")
	case where == "":
		where = i18n.T(ctx, "ai.kind."+c.ProviderKind)
	}
	parts := []string{c.ModelName, where}
	if est := EstimateText(ctx, c.Estimate, c.Local); est != "" {
		parts = append(parts, est)
	}
	return strings.Join(parts, " · ")
}

// EstimateText is "~40 s", "~$0.13" or "~12 s · ~$0.13".
func EstimateText(ctx context.Context, e router.Estimate, local bool) string {
	var parts []string
	if e.Seconds > 0 {
		parts = append(parts, DurationText(ctx, e.Seconds))
	}
	if e.USD > 0 {
		parts = append(parts, "~"+USD(e.USD))
	}
	return strings.Join(parts, " · ")
}

// DurationText is "~40 s", "~3 min" or "~2 h".
func DurationText(ctx context.Context, secs float64) string {
	switch {
	case secs < 90:
		return i18n.T(ctx, "ai.dur.seconds", "n", int(math.Max(1, math.Round(secs))))
	case secs < 5400:
		return i18n.T(ctx, "ai.dur.minutes", "n", int(math.Round(secs/60)))
	}
	return i18n.T(ctx, "ai.dur.hours", "n", fmt.Sprintf("%.1f", secs/3600))
}

// USD formats a cost on the user's own key ("$0.13", "$0.004", "$12").
func USD(v float64) string {
	switch {
	case v == 0:
		return "$0"
	case v < 0.01:
		return "$" + strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.4f", v), "0"), ".")
	case v == math.Trunc(v) || v >= 100:
		return fmt.Sprintf("$%.0f", v)
	}
	return fmt.Sprintf("$%.2f", v)
}

// ReasonText translates a reason code with its arguments.
func ReasonText(ctx context.Context, r router.Reason) string {
	args := make([]any, 0, len(r.Args)*2)
	for _, k := range sortedKeys(r.Args) {
		v := r.Args[k]
		switch t := v.(type) {
		case float64:
			if t == math.Trunc(t) {
				v = int(t)
			} else if k == "remaining" {
				v = USD(t)
			} else {
				v = fmt.Sprintf("%.1f", t)
			}
		case string:
			switch k {
			case "series":
				v = i18n.T(ctx, "ai.series."+t)
			case "provider":
				v = i18n.T(ctx, "ai.kind."+t)
			case "scope":
				v = i18n.T(ctx, "ai.pin.for."+t)
			case "why":
				v = i18n.T(ctx, "ai.reason."+t)
			case "rating":
				if t == "" {
					t = "all"
				}
				v = i18n.T(ctx, "ai.rating."+t)
			}
		}
		args = append(args, k, v)
	}
	return i18n.T(ctx, "ai.reason."+r.Code, args...)
}
