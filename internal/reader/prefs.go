package reader

import (
	"math"
	"slices"

	"github.com/MaramHarsha/MangaVeyra/internal/publish"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
)

// Reading modes (reader_prefs.mode). The E3 segmented control calls them
// Single, Spread, Guided and Scroll.
const (
	ModePaged   = "paged"
	ModeSpread  = "spread"
	ModeGuided  = "guided"
	ModeWebtoon = "webtoon"
	// ModeAuto lets the island pick Spread on wide screens and Single on narrow ones.
	ModeAuto = "auto"
)

// Reading directions (reader_prefs.direction).
const (
	DirRTL      = "rtl"
	DirLTR      = "ltr"
	DirVertical = "vertical"
)

// Reader backgrounds. reader_prefs.background historically defaults to
// "black", which is the designed "Ink".
const (
	BgInk   = "ink"
	BgGrey  = "grey"
	BgPaper = "paper"
)

// Zoom bounds for reader_prefs.zoom.
const (
	MinZoom = 0.5
	MaxZoom = 3
)

// Prefs are the resolved reading settings the reader opens with.
type Prefs struct {
	Mode       string  `json:"mode"`
	Direction  string  `json:"direction"`
	Background string  `json:"background"`
	Zoom       float64 `json:"zoom"`
	// TapZones and the toggles below are member defaults from G1 · Reading;
	// their per-series overrides stay on the device (no reader_prefs column).
	TapZones      bool `json:"tap_zones"`
	TapLShape     bool `json:"tap_l_shape"`
	PanelComments bool `json:"panel_comments"`
	ReadAloud     bool `json:"read_aloud"`
	HideChrome    bool `json:"hide_chrome"`
	ReducedMotion bool `json:"reduced_motion"`
	// Stored reports whether a reader_prefs row exists for this series.
	Stored bool `json:"stored"`
}

// formatDirection is the series' natural reading direction.
func formatDirection(format string) string {
	switch format {
	case "comic_ltr":
		return DirLTR
	case "webtoon":
		return DirVertical
	}
	return DirRTL
}

// isWebtoon reports whether the series reads as a vertical strip.
func isWebtoon(format string) bool { return format == "webtoon" }

// modesFor lists the modes the E3/E4 segmented control offers for a format.
func modesFor(format string) []string {
	if isWebtoon(format) {
		return []string{ModeWebtoon, ModeGuided}
	}
	return []string{ModePaged, ModeSpread, ModeGuided, ModeWebtoon}
}

func normBackground(v string) string {
	switch v {
	case "grey":
		return BgGrey
	case "paper", "white":
		return BgPaper
	}
	return BgInk
}

// storedBackground maps the designed name back to the column value.
func storedBackground(v string) string {
	switch v {
	case BgGrey:
		return "grey"
	case BgPaper:
		return "paper"
	}
	return "black"
}

func clampZoom(z float64) float64 {
	if math.IsNaN(z) || z <= 0 {
		return 1
	}
	return math.Max(MinZoom, math.Min(MaxZoom, z))
}

// resolvePrefs combines the series format, the member's G1 defaults and the
// stored per-series row (nil when none) into the settings the reader opens with.
func resolvePrefs(s publish.Series, user *settings.UserPrefs, row *SeriesPrefs, motionReduced bool) Prefs {
	d := settings.DefaultUserPrefs()
	if user != nil {
		d = *user
	}
	p := Prefs{
		Mode:          ModeAuto,
		Direction:     formatDirection(s.Format),
		Background:    normBackground(d.Reading.Background),
		Zoom:          1,
		TapZones:      d.Reading.TapZones,
		TapLShape:     d.Flag("reading.tap_l_shape", false),
		PanelComments: d.Reading.ShowPanelComments,
		ReadAloud:     d.Reading.ReadAloud,
		HideChrome:    d.Flag("reading.hide_chrome", false),
		ReducedMotion: motionReduced,
	}
	if isWebtoon(s.Format) {
		p.Mode = ModeWebtoon
		if d.Flag("reading.webtoon_guided", false) {
			p.Mode = ModeGuided
		}
	} else if slices.Contains([]string{ModePaged, ModeSpread, ModeGuided, ModeWebtoon}, d.Reading.Mode) {
		p.Mode = d.Reading.Mode
	}
	if !isWebtoon(s.Format) && (d.Reading.Direction == DirRTL || d.Reading.Direction == DirLTR) {
		p.Direction = d.Reading.Direction
	}
	if row != nil {
		p.Stored = true
		if slices.Contains(modesFor(s.Format), row.Mode) {
			p.Mode = row.Mode
		}
		if !isWebtoon(s.Format) && (row.Direction == DirRTL || row.Direction == DirLTR) {
			p.Direction = row.Direction
		}
		p.Background = normBackground(row.Background)
		p.Zoom = clampZoom(row.Zoom)
	}
	return p
}

// validPrefsInput checks a per-series settings update from the island and
// returns the row to store.
func validPrefsInput(s publish.Series, in SeriesPrefs) (SeriesPrefs, bool) {
	if !slices.Contains(modesFor(s.Format), in.Mode) {
		return SeriesPrefs{}, false
	}
	switch {
	case isWebtoon(s.Format):
		in.Direction = DirVertical
	case in.Direction != DirRTL && in.Direction != DirLTR:
		return SeriesPrefs{}, false
	}
	switch in.Background {
	case BgInk, BgGrey, BgPaper:
	default:
		return SeriesPrefs{}, false
	}
	if math.IsNaN(in.Zoom) || math.IsInf(in.Zoom, 0) || in.Zoom < MinZoom || in.Zoom > MaxZoom {
		return SeriesPrefs{}, false
	}
	in.Background = storedBackground(in.Background)
	return in, true
}
