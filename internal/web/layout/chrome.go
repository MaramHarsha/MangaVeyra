// Package layout holds the page frames every MangaVeyra screen renders in:
// Base (the HTML document), AppShell (top bar, studio sidebar, mobile bottom
// nav), AuthShell, SetupShell, OnboardingShell, SettingsLayout, AdminLayout,
// ReaderShell, EditorShell and PlainShell.
//
// Layouts are pure templates: the web package collects the request-dependent
// data into a Chrome value (user, preferences, instance, compute meter,
// unread count) and pages pass it in.
package layout

import (
	"strconv"

	"github.com/MaramHarsha/MangaVeyra/internal/core/httpx"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
	"github.com/MaramHarsha/MangaVeyra/internal/web/ui"
)

// Assets are the built frontend files a page links.
type Assets struct {
	CSS     []string
	Script  string
	Preload []string
	Fonts   []string
}

// Flash is a toast queued for the next full page load.
type Flash struct {
	Kind  string `json:"kind"`
	Title string `json:"title"`
	Body  string `json:"body,omitempty"`
	Href  string `json:"href,omitempty"`
}

// Studio sidebar keys (Chrome.Nav).
const (
	NavHome       = "home"
	NavProjects   = "projects"
	NavCharacters = "characters"
	NavSetting    = "setting"
	NavTemplates  = "templates"
	NavAnalytics  = "analytics"
	NavModels     = "models"
	NavAgents     = "agents"
	NavSettings   = "settings"
	NavAdmin      = "admin"
)

// Top bar sections (Chrome.Section).
const (
	SectionStudio  = "studio"
	SectionExplore = "explore"
	SectionLibrary = "library"
)

// Mobile bottom nav keys (Chrome.Tab).
const (
	TabHome    = "home"
	TabExplore = "explore"
	TabCreate  = "create"
	TabLibrary = "library"
	TabMe      = "me"
)

// Chrome is everything a layout needs to know about the request.
type Chrome struct {
	// SignedOut marks a page rendered for a visitor known to be signed out
	// (body[data-signed-out]); pages that do not know who is asking carry
	// neither it nor data-user, so client code never mistakes them for either.
	SignedOut bool
	// Title is the page title (without the instance name).
	Title string
	// MobileTitle is the centred mobile header title (defaults to Title).
	MobileTitle string
	// BackURL shows a back arrow instead of the logo in the mobile header.
	BackURL string
	// ProjectID keeps Studio destinations in the current project context.
	ProjectID string

	User  *httpx.User
	Prefs settings.UserPrefs
	// Theme is "dark", "light" or "system".
	Theme string
	Lang  string
	Dir   string

	InstanceName string
	// ServerHost is shown in the sidebar footer ("studio.local").
	ServerHost string
	Version    string
	PublicMode bool
	LocalOnly  bool
	SignUpOpen bool

	// Meter is the compute meter (nil hides it).
	Meter  *ui.MeterProps
	Unread int

	Section string
	Nav     string
	Tab     string
	// NoSidebar hides the studio sidebar (reader-facing pages).
	NoSidebar bool
	// Transition "turn" enables the page-turn view transition (wizards).
	Transition string

	Assets Assets
	Flash  []Flash
	// SearchQuery pre-fills the top-bar search.
	SearchQuery string
	// Dev enables development conveniences in the page (no service worker).
	Dev bool
	// RequestPath is the current path (for "next" links).
	RequestPath string
}

// IsAdmin reports whether the viewer is an admin.
func (c *Chrome) IsAdmin() bool { return c != nil && c.User.IsAdmin() }

// Motion returns the html data-motion value: "reduce", "full" or "" (follow the OS).
func (c *Chrome) Motion() string {
	switch c.Prefs.ReducedMotion {
	case "on":
		return "reduce"
	case "off":
		return "full"
	}
	return ""
}

// TextSize returns the html data-text-size value ("" for 100 %).
func (c *Chrome) TextSize() string {
	n := NearestTextSize(c.Prefs.TextSize)
	if n == 100 {
		return ""
	}
	return strconv.Itoa(n)
}

// NearestTextSize snaps a text size preference to the supported steps.
func NearestTextSize(n int) int {
	best := 100
	for _, s := range []int{87, 100, 112, 125, 150} {
		if abs(s-n) < abs(best-n) {
			best = s
		}
	}
	return best
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// ThemeColor is the browser UI colour for the theme.
func (c *Chrome) ThemeColor() string {
	if c.Theme == "light" {
		return "#E8EEFF"
	}
	return "#070A12"
}

// Logo returns the gate-blue lockup for the theme.
func (c *Chrome) Logo() string {
	if c.Theme == "light" {
		return "/static/brand/lockup-color-on-light-gate-blue.svg"
	}
	return "/static/brand/lockup-color-on-dark-gate-blue.svg"
}

// Mark returns the gate-blue mark for the theme.
func (c *Chrome) Mark() string {
	if c.Theme == "light" {
		return "/static/brand/mark-color-on-light-gate-blue.svg"
	}
	return "/static/brand/mark-color-on-dark-gate-blue.svg"
}

// BrandMark returns the vermilion brand mark (hero moments on auth pages).
func (c *Chrome) BrandMark() string {
	if c.Theme == "light" {
		return "/static/brand/mark-color-on-light.svg"
	}
	return "/static/brand/mark-color-on-dark.svg"
}

func (c *Chrome) mobileTitle() string {
	if c.MobileTitle != "" {
		return c.MobileTitle
	}
	return c.Title
}

func (c *Chrome) fullTitle() string {
	name := c.InstanceName
	if name == "" {
		name = "MangaVeyra"
	}
	if c.Title == "" || c.Title == name {
		return name
	}
	return c.Title + " · " + name
}

func (c *Chrome) userID() string {
	if c.User == nil {
		return ""
	}
	return c.User.ID
}

func (c *Chrome) userName() string {
	if c.User == nil {
		return ""
	}
	return c.User.Name()
}

func (c *Chrome) texturesOff() bool { return c.User != nil && !c.Prefs.Textures }

func attrIf(cond bool, v string) string {
	if cond {
		return v
	}
	return ""
}

func itoa(n int) string { return strconv.Itoa(n) }
