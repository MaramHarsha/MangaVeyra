package publish

import (
	"context"
	"slices"

	"github.com/MaramHarsha/MangaVeyra/internal/admin/adminapi"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
	"github.com/MaramHarsha/MangaVeyra/internal/web/ui"
)

// Content rules on the write side (ADR 0013 §2): E1 and series settings offer
// only the age ratings the server allows, and refuse series and chapter
// titles and synopses that contain a blocked word. The read side lives in
// access.go.

// contentRules returns the instance content rules (the defaults when the
// settings cannot be read).
func (s *service) contentRules(ctx context.Context) settings.Content {
	return s.settings.MustInstance(ctx).Content
}

// ratingOptions lists the ratings the rules allow, plus current when it is
// set but no longer allowed, so the form shows what the series carries and
// saving it explains why that rating is refused.
func ratingOptions(ctx context.Context, rules settings.Content, current string) []ui.Option {
	out := make([]ui.Option, 0, len(Ratings))
	for _, r := range Ratings {
		if adminapi.RatingAllowed(rules, r) || (r == current && slices.Contains(Ratings, current)) {
			out = append(out, ui.Option{Value: r, Label: i18n.T(ctx, "publish.rating."+r)})
		}
	}
	return out
}

// checkRules adds the content-rule errors for a series form: a rating the
// server does not allow, and blocked words in the title or synopsis.
func checkSeriesRules(rules settings.Content, in seriesInput, errs fieldErrors) {
	if _, bad := errs["age_rating"]; !bad && !adminapi.RatingAllowed(rules, in.AgeRating) {
		errs["age_rating"] = "publish.error.rating_off"
	}
	if _, bad := errs["title"]; !bad {
		if _, ok := adminapi.Blocked(rules.BlockedWords, in.Title); ok {
			errs["title"] = "publish.error.blocked"
		}
	}
	if _, bad := errs["synopsis"]; !bad {
		if _, ok := adminapi.Blocked(rules.BlockedWords, in.Synopsis); ok {
			errs["synopsis"] = "publish.error.blocked"
		}
	}
}

// ratingHidden reports whether readers outside the project cannot see the
// series because the rules no longer allow its rating.
func ratingHidden(rules settings.Content, s Series) bool {
	return s.ID != "" && !adminapi.RatingAllowed(rules, s.AgeRating)
}
