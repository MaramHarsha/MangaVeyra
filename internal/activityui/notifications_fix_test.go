package activityui_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/activityui"
	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/notify"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

// Older job notifications stored the raw job type as their body. G4 shows a
// localized label for those, keeps new human summaries as written, and lists
// rows by the time shown (a regrouped notification moves to the top).
func TestNotificationsRenderLegacyJobBodiesAndShownOrder(t *testing.T) {
	each(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		u, c := ta.UserClient(t, auth.RoleCreator)
		clock := time.Now().UTC().Add(-10 * time.Minute)
		ta.Deps.Notify.Now = func() time.Time { return clock }
		for _, in := range []notify.Input{
			{Kind: notify.KindJobDone, Title: "Generation finished", Body: "page.generate", GroupKey: notify.KindJobDone},
			{Kind: notify.KindJobFailed, Title: "Generation failed", Body: "character.repair"},
			{Kind: notify.KindJobDone, Title: "Panel 7.3 variations ready", Body: "Tidebreak Courier · Panel 7.3 · Regenerate · 38 s",
				Data: map[string]any{"body": activityui.JobSummaryBody}},
			{Kind: notify.KindComment, Title: "Kaito commented on panel 7.3", Body: "character.lock_score"},
		} {
			in.UserID = u.ID
			if _, err := ta.Deps.Notify.Notify(ctx, in); err != nil {
				t.Fatal(err)
			}
			clock = clock.Add(time.Minute)
		}
		body := string(c.Get("/notifications").Body)
		for _, want := range []string{">Page art<", ">Character repair<", ">Tidebreak Courier · Panel 7.3 · Regenerate · 38 s<", ">character.lock_score<"} {
			if !strings.Contains(body, want) {
				t.Fatalf("G4 lacks %q", want)
			}
		}
		for _, raw := range []string{">page.generate<", ">character.repair<"} {
			if strings.Contains(body, raw) {
				t.Fatalf("G4 shows the raw job type %q", raw)
			}
		}
		ja := ta.Login(t, u.Username)
		ja.Header.Set("Accept-Language", "ja")
		if body := string(ja.Get("/notifications").Body); !strings.Contains(body, ">ページの絵<") || !strings.Contains(body, ">キャラクター修正<") {
			t.Fatal("legacy job bodies are not localized")
		}
		// The grouped "page.generate" row is updated last: it moves to the top.
		clock = clock.Add(5 * time.Minute)
		if _, err := ta.Deps.Notify.Notify(ctx, notify.Input{UserID: u.ID, Kind: notify.KindJobDone, Title: "Generation finished again",
			Body: "Tidebreak Courier · Page art · 2 min", Data: map[string]any{"body": activityui.JobSummaryBody}, GroupKey: notify.KindJobDone}); err != nil {
			t.Fatal(err)
		}
		body = string(c.Get("/notifications").Body)
		first, older := strings.Index(body, "Generation finished again"), strings.Index(body, "Kaito commented on panel 7.3")
		if first < 0 || older < 0 || first > older {
			t.Fatal("notifications are not newest first by the time shown")
		}
	})
}
