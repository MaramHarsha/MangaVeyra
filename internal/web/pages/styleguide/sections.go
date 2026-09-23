package styleguide

import "github.com/a-h/templ"

// section is one design-system section of the style guide.
type section struct {
	ID, Num, Title, Desc string
	// Render draws the section's samples; id keeps element ids unique per theme column.
	Render func(id string) templ.Component
}

var sections = []section{
	{"colour", "01", "Colour tokens", "Two themes share one token set. Accent fill is gate blue with ink text in both; accent text shifts to hold 4.5:1. Status colours always pair with an icon and a word.", colours},
	{"type", "02", "Typography", "Anton for display and names, Hanken Grotesk for everything interactive, Comic Neue inside bubbles, Bangers and Noto Sans JP Black for sound effects.", typeScale},
	{"space", "03", "Spacing, radius, elevation", "A 4 px base. Frames are square like panels; only speech-bubble shapes get large radii. The editor uses borders only.", spaceRadius},
	{"icons", "04", "Icons", "Tabler Icons (MIT), self-hosted, used at 16 / 18 / 22 px.", icons},
	{"textures", "05", "Textures", "Seasoning: at most one strong texture per view, never under body text, none inside the editor workspace.", textures},
	{"buttons", "06", "Buttons", "Primary is gate blue with ink text in both themes. Hover lifts with a hard ink shadow; focus is a 2 px ring offset 3 px. Touch targets are 48 px on mobile.", buttons},
	{"inputs", "07", "Inputs", "Square fields on a surface tone. Errors say what happened and what to do, with an icon.", inputs},
	{"selection", "08", "Toggles, checks, sliders", "Switches for settings, checks and radios for forms, sliders with a diamond thumb.", selection},
	{"tabs", "09", "Tabs, badges, avatars", "Provenance badges use the same badge component as status so they read as information, not decoration.", tabsBadges},
	{"cards", "10", "Cards", "Project, series, character and chapter cards. The project card's angled bottom edge is the one allowed tilt in app UI.", cards},
	{"ai", "11", "AI components", "Self-host edition: a compute meter shows GPU and queue load, and every generate action shows its time estimate. The routing chip explains its pick.", aiParts},
	{"overlays", "12", "Modals, drawers, menus, tooltips, toasts", "Modals get the seal border and glow. Tooltips and toasts are speech bubbles with a tail pointing at their source.", overlays},
	{"navigation", "13", "Navigation", "Top bar for reading and studio, sidebar inside the studio, bottom nav on mobile with Create as the one accent action.", navigation},
	{"motion", "14", "Motion", "CSS-first. Every animation has a reduced-motion fallback (OS setting and the in-app toggle). Demos loop here; in the product each plays once.", motion},
	{"states", "15", "Loading, empty and error states", "Skeletons use a moving screentone and only appear for AI results. Empty and error states pair an ink splash or speed lines with an SFX word and one clear next action.", states},
	{"gate", "16b", "Gate motifs", "Status windows, rank badges, XP bars and level-up toasts for dashboards, profiles and celebration moments. Ranks never come from money.", gate},
	{"islands", "17", "Islands", "Rich screens are Svelte 5 islands mounted on data-island elements and loaded on demand.", islandDemo},
}

type swatch struct{ Class, Use string }

var tokenSwatches = []swatch{
	{"bg", "Base background"}, {"surface", "Cards, panels"}, {"surface-2", "Hover, tracks"}, {"line", "Borders"},
	{"text", "Text primary"}, {"text-2", "Text secondary"}, {"text-3", "Meta, captions"}, {"accent", "Accent fill"},
	{"acc-text", "Accent text, links"}, {"success", "Success"}, {"warning", "Warning"}, {"error", "Error"}, {"info", "Info, queued"},
}

var iconNames = []string{"sparkles", "brush", "pencil", "layers-subtract", "lasso", "eraser", "message-circle", "message-2",
	"bolt", "lock", "route", "cpu", "language", "movie", "book", "books", "users", "download", "share", "history",
	"git-branch", "wand", "photo", "layout-grid"}
