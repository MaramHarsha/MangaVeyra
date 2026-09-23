package settings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
)

// UserPrefs are one member's interface, accessibility and reading preferences.
// Notification preferences are kept by the notify package.
type UserPrefs struct {
	// Theme is "dark" (Gate night, the default), "light" (Paper) or "system".
	Theme string `json:"theme"`
	// ReducedMotion is "system" (follow the OS), "on" or "off".
	ReducedMotion string `json:"reduced_motion"`
	// Textures toggles screentone/halftone background textures.
	Textures bool `json:"textures"`
	// LivingPanels plays living-panel loops in the reader.
	LivingPanels bool `json:"living_panels"`
	// ShowRanksXP shows ranks, XP bars and level-up moments.
	ShowRanksXP bool `json:"show_ranks_xp"`
	// TextSize scales interface text in percent (87, 100, 112, 125, 150).
	TextSize int `json:"text_size"`
	// DyslexiaFont switches body text to a dyslexia-friendly font.
	DyslexiaFont bool `json:"dyslexia_font"`
	// UnderlineLinks always underlines links.
	UnderlineLinks bool `json:"underline_links"`
	// HighContrastFocus uses a thicker, higher-contrast focus ring.
	HighContrastFocus bool `json:"high_contrast_focus"`
	// DataSaver loads smaller images and never autoplays loops.
	DataSaver bool     `json:"data_saver"`
	Reading   Reading  `json:"reading"`
	Languages Language `json:"languages"`
	// Editor holds editor island conveniences (last tool, grid, rulers...).
	Editor map[string]any `json:"editor,omitempty"`
	// Flags holds optional on/off preferences owned by feature modules, keyed
	// "<area>.<name>" (e.g. "reading.hide_chrome", "a11y.alt_text_always").
	Flags map[string]bool `json:"flags,omitempty"`
}

// Flag returns the value of an optional boolean preference (def when unset).
func (p UserPrefs) Flag(key string, def bool) bool {
	if v, ok := p.Flags[key]; ok {
		return v
	}
	return def
}

// Reading holds reader defaults (per-series overrides live in reader_prefs).
type Reading struct {
	// Mode is "paged", "spread", "webtoon" or "guided"; "auto" follows the series format.
	Mode string `json:"mode"`
	// Direction is "auto" (follow the series), "rtl" or "ltr".
	Direction  string `json:"direction"`
	Background string `json:"background"`
	// TapZones enables left/right tap zones on touch screens.
	TapZones bool `json:"tap_zones"`
	// ShowPanelComments shows panel-anchored comment pins (collapsed by default).
	ShowPanelComments bool `json:"show_panel_comments"`
	// ProvenanceFilter hides series by AI label: "all", "human_art" or "human_only".
	ProvenanceFilter string `json:"provenance_filter"`
	// ShowMature shows mature series (only if the instance allows them).
	ShowMature bool `json:"show_mature"`
	// ReadAloud enables alt-text read-aloud in guided view.
	ReadAloud bool `json:"read_aloud"`
}

// Language holds language preferences.
type Language struct {
	// UI is the interface language ("" = negotiate from the browser).
	UI string `json:"ui"`
	// Reading lists preferred reading languages, most preferred first.
	Reading []string `json:"reading"`
	// AutoTranslate offers reader-side translation on opted-in series.
	AutoTranslate bool `json:"auto_translate"`
}

// DefaultUserPrefs returns the defaults for a new member.
func DefaultUserPrefs() UserPrefs {
	return UserPrefs{
		Theme:         "dark",
		ReducedMotion: "system",
		Textures:      true,
		LivingPanels:  true,
		ShowRanksXP:   true,
		TextSize:      100,
		Reading: Reading{
			Mode:             "auto",
			Direction:        "auto",
			Background:       "black",
			TapZones:         true,
			ProvenanceFilter: "all",
		},
		Languages: Language{Reading: []string{}},
	}
}

// Validate checks enumerations.
func (p UserPrefs) Validate() error {
	var errs []error
	if !oneOf(p.Theme, "dark", "light", "system") {
		errs = append(errs, fmt.Errorf("theme %q must be dark, light or system", p.Theme))
	}
	if !oneOf(p.ReducedMotion, "system", "on", "off") {
		errs = append(errs, fmt.Errorf("reduced_motion %q must be system, on or off", p.ReducedMotion))
	}
	if p.TextSize < 75 || p.TextSize > 200 {
		errs = append(errs, fmt.Errorf("text_size %d must be 75–200", p.TextSize))
	}
	if !oneOf(p.Reading.Mode, "auto", "paged", "spread", "webtoon", "guided") {
		errs = append(errs, fmt.Errorf("reading.mode %q is invalid", p.Reading.Mode))
	}
	if !oneOf(p.Reading.Direction, "auto", "rtl", "ltr") {
		errs = append(errs, fmt.Errorf("reading.direction %q is invalid", p.Reading.Direction))
	}
	if !oneOf(p.Reading.Background, "black", "white", "grey", "paper") {
		errs = append(errs, fmt.Errorf("reading.background %q is invalid", p.Reading.Background))
	}
	if !oneOf(p.Reading.ProvenanceFilter, "all", "human_art", "human_only") {
		errs = append(errs, fmt.Errorf("reading.provenance_filter %q is invalid", p.Reading.ProvenanceFilter))
	}
	return errors.Join(errs...)
}

// UserPrefs returns the member's preferences with defaults for unset fields.
func (s *Service) UserPrefs(ctx context.Context, userID string) (UserPrefs, error) {
	p := DefaultUserPrefs()
	if userID == "" {
		return p, nil
	}
	var raw string
	err := s.db.Get(ctx, &raw, `SELECT prefs FROM user_prefs WHERE user_id = ?`, userID)
	if db.IsNotFound(err) {
		return p, nil
	}
	if err != nil {
		return p, fmt.Errorf("settings: load prefs: %w", err)
	}
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return DefaultUserPrefs(), fmt.Errorf("settings: decode prefs: %w", err)
	}
	return p, nil
}

// UpdateUserPrefs applies fn to the member's preferences, validates and saves them.
func (s *Service) UpdateUserPrefs(ctx context.Context, userID string, fn func(*UserPrefs) error) (UserPrefs, error) {
	cur, err := s.UserPrefs(ctx, userID)
	if err != nil {
		return cur, err
	}
	next := cur
	next.Languages.Reading = append([]string{}, cur.Languages.Reading...)
	if err := fn(&next); err != nil {
		return cur, err
	}
	if err := next.Validate(); err != nil {
		return cur, fmt.Errorf("settings: %w", err)
	}
	b, err := json.Marshal(next)
	if err != nil {
		return cur, err
	}
	_, err = s.db.Exec(ctx, `INSERT INTO user_prefs (user_id, prefs, updated_at) VALUES (?, ?, ?)
		ON CONFLICT (user_id) DO UPDATE SET prefs = excluded.prefs, updated_at = excluded.updated_at`, userID, string(b), db.Now())
	if err != nil {
		return cur, fmt.Errorf("settings: save prefs: %w", err)
	}
	return next, nil
}
