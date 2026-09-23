package studio

import (
	"context"
	"strings"

	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/web/ui"
)

func usageLine(ctx context.Context, u SettingUsage) string {
	s := i18n.T(ctx, "studio.setting.used_in", "count", u.Panels)
	if len(u.TimesOfDay) > 0 {
		var names []string
		for _, t := range u.TimesOfDay {
			names = append(names, strings.ToLower(i18n.T(ctx, "studio.time_of_day."+t)))
		}
		s += " · " + strings.Join(names, " & ")
	}
	return s
}

// ContentLanguages are the languages a project can be written in.
var ContentLanguages = []string{"en", "ja", "es", "fr", "de", "pt", "it", "ko", "zh", "ar", "he", "id", "th", "vi"}

func languageOptions(ctx context.Context, cur string) []ui.Option {
	var out []ui.Option
	found := false
	for _, l := range ContentLanguages {
		out = append(out, ui.Option{Value: l, Label: i18n.T(ctx, "studio.lang."+l)})
		found = found || l == cur
	}
	if !found && cur != "" {
		out = append(out, ui.Option{Value: cur, Label: cur})
	}
	return out
}

func formatSelect(ctx context.Context) []ui.Option {
	out := make([]ui.Option, 0, len(Formats))
	for _, f := range Formats {
		out = append(out, ui.Option{Value: f, Label: i18n.T(ctx, "studio.format."+f)})
	}
	return out
}

func styleSelect(ctx context.Context) []ui.Option {
	out := make([]ui.Option, 0, len(Styles))
	for _, s := range Styles {
		out = append(out, ui.Option{Value: s, Label: i18n.T(ctx, "style."+s)})
	}
	return out
}

func visIcon(v string) string {
	if v == VisibilityPublic {
		return "world"
	}
	return "lock"
}

func routingOptions(ctx context.Context) []ui.Option {
	out := make([]ui.Option, 0, len(RoutingModes))
	for _, m := range RoutingModes {
		key := m
		if key == "" {
			key = "default"
		}
		out = append(out, ui.Option{Value: m, Label: i18n.T(ctx, "studio.routing."+key)})
	}
	return out
}

func memberRoleOptions(ctx context.Context) []ui.Option {
	var out []ui.Option
	for _, r := range Roles {
		if r == RoleOwner {
			continue
		}
		out = append(out, ui.Option{Value: r, Label: i18n.T(ctx, "studio.role."+r)})
	}
	return out
}

func roleTone(r string) ui.Tone {
	switch r {
	case RoleOwner:
		return ui.ToneAccent
	case RoleViewer:
		return ui.ToneMuted
	}
	return ui.ToneText
}

func roleIcon(r string) string {
	switch r {
	case RoleOwner:
		return "crown"
	case RoleWriter:
		return "script"
	case RoleArtist:
		return "brush"
	case RoleLetterer:
		return "message-circle"
	case RoleEditor:
		return "checks"
	}
	return "eye"
}

func scopeLine(ctx context.Context, s MemberScope) string {
	var parts []string
	if s.Pages != "" {
		parts = append(parts, i18n.T(ctx, "studio.members.scope_pages", "pages", s.Pages))
	}
	if contains(s.Layers, LayerLettering) {
		parts = append(parts, i18n.T(ctx, "studio.members.lettering_only"))
	}
	return strings.Join(parts, " · ")
}
