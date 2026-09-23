package studio

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/a-h/templ"

	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/web/ui"
)

func jsonUnmarshal(s string, v any) error { return json.Unmarshal([]byte(s), v) }

func projectMenu(ctx context.Context, c ProjectCard) []ui.MenuItem {
	base := "/studio/p/" + c.ID
	return []ui.MenuItem{
		{Href: base + "/story", Label: i18n.T(ctx, "studio.nav.story"), Icon: "script"},
		{Href: base + "/characters", Label: i18n.T(ctx, "studio.nav.characters"), Icon: "users"},
		{Href: base + "/setting", Label: i18n.T(ctx, "studio.nav.setting"), Icon: "world"},
		{Href: base + "/settings", Label: i18n.T(ctx, "studio.nav.settings"), Icon: "settings"},
	}
}

func routePick(ctx context.Context, p RoutePreview) string {
	if p.Seconds > 0 {
		return p.Pick + " · ~" + FormatDuration(ctx, p.Seconds)
	}
	return p.Pick
}

type projectTab struct{ Key, Href, Icon, Label string }

func projectTabs(ctx context.Context, p *Project) []projectTab {
	base := "/studio/p/" + p.ID
	return []projectTab{
		{"story", base + "/story", "script", i18n.T(ctx, "studio.nav.story")},
		{"characters", base + "/characters", "users", i18n.T(ctx, "studio.nav.characters")},
		{"setting", base + "/setting", "world", i18n.T(ctx, "studio.nav.setting")},
		{"settings", base + "/settings", "settings", i18n.T(ctx, "studio.nav.settings")},
	}
}

func pct(v float64) string { return strconv.Itoa(int(v*100+.5)) + "%" }

func fmtScore(v *float64) string {
	if v == nil {
		return "–"
	}
	return pct(*v)
}

func upper(s string) string { return strings.ToUpper(s) }

func fmtInt(ctx context.Context, n int) string { return i18n.FormatInt(int64(n), i18n.LocaleFrom(ctx)) }

func attrs(kv ...string) templ.Attributes {
	a := templ.Attributes{}
	for i := 0; i+1 < len(kv); i += 2 {
		a[kv[i]] = kv[i+1]
	}
	return a
}

// polyPoints renders a polygon for an SVG viewBox of 100 × h (page percent scaled on y).
func polyPoints(p Polygon, h float64) string {
	var b strings.Builder
	for i, pt := range p {
		if i > 0 {
			b.WriteByte(' ')
		}
		fmt.Fprintf(&b, "%.1f,%.1f", pt[0], pt[1]*h/100)
	}
	return b.String()
}

func ftoa(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

func joinLines(ls []DialogueLine) string {
	var b strings.Builder
	for i, l := range ls {
		if i > 0 {
			b.WriteString(" / ")
		}
		if l.Speaker != "" {
			b.WriteString(l.Speaker + ": ")
		}
		b.WriteString(l.Text)
	}
	return b.String()
}
