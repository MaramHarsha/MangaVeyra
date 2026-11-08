package publish_test

import (
	"context"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/publish"
	"github.com/MaramHarsha/MangaVeyra/internal/publish/publishtest"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

func TestCreatorsAreReadOwnerFirstInCreditOrder(t *testing.T) {
	forEachDialect(t, func(t *testing.T, ta *testutil.TestApp, pf *publishtest.Fixture) {
		ctx := context.Background()
		svc := pf.Service()
		pf.SetMode(settings.ModePublic)
		jun, sora := pf.User(auth.RoleCreator), pf.User(auth.RoleCreator)
		a := pf.Series(publishtest.SeriesSpec{Schedule: "Weekly, Saturday", OwnerCredit: "Jerry Okafor",
			Creators: []publish.Creator{{UserID: jun.ID, Role: "letterer", CreditName: "Jun Sato"}, {UserID: sora.ID, Role: "artist"}}})
		b := pf.Series(publishtest.SeriesSpec{})
		c := pf.Series(publishtest.SeriesSpec{Creators: []publish.Creator{{UserID: jun.ID, Role: "translator"}}})
		// Credit order follows order_idx, not insertion; the owner is first even without a row.
		exec(t, ta, `UPDATE series_creators SET order_idx = 5 WHERE series_id = ? AND user_id = ?`, a.ID, jun.ID)
		exec(t, ta, `DELETE FROM series_creators WHERE series_id = ? AND role = 'owner'`, c.ID)

		want := map[string][]publish.Creator{
			a.ID: {{UserID: a.Owner.ID, Role: "owner", CreditName: "Jerry Okafor"}, {UserID: sora.ID, Role: "artist"}, {UserID: jun.ID, Role: "letterer", CreditName: "Jun Sato"}},
			b.ID: {{UserID: b.Owner.ID, Role: "owner"}},
			c.ID: {{UserID: c.Owner.ID, Role: "owner"}, {UserID: jun.ID, Role: "translator"}},
		}
		for id, creators := range want {
			s, err := svc.SeriesByID(ctx, publish.Viewer{}, id)
			if err != nil || !reflect.DeepEqual(s.Creators, creators) {
				t.Fatalf("SeriesByID(%s).Creators = %+v, want %+v (%v)", id, s.Creators, creators, err)
			}
		}
		if s, _ := svc.SeriesBySlug(ctx, publish.Viewer{}, a.Slug); s.Schedule != "Weekly, Saturday" || len(s.Creators) != 3 {
			t.Fatalf("SeriesBySlug = %+v", s)
		}
		// Lists load every series' credits in one batch.
		list, _, err := svc.ListSeries(ctx, publish.Viewer{}, publish.SeriesFilter{Limit: 10})
		if err != nil || len(list) != 3 {
			t.Fatalf("ListSeries = %d %v", len(list), err)
		}
		for _, s := range list {
			if !reflect.DeepEqual(s.Creators, want[s.ID]) {
				t.Fatalf("ListSeries %s creators = %+v, want %+v", s.ID, s.Creators, want[s.ID])
			}
		}
	})
}

func TestSeriesSettingsCreditsAndReleaseNote(t *testing.T) {
	forEachDialect(t, func(t *testing.T, ta *testutil.TestApp, pf *publishtest.Fixture) {
		ctx := context.Background()
		pf.SetMode(settings.ModePublic)
		f := newChapter(t, ta, false)
		path := "/studio/p/" + f.project.ID + "/series"
		letterer := ta.CreateUser(t, "junsato", auth.RoleCreator)
		pf.AddMember(f.project.ID, letterer.ID, studio.RoleLetterer)
		editor := ta.CreateUser(t, "soravell", auth.RoleCreator)
		pf.AddMember(f.project.ID, editor.ID, studio.RoleEditor)
		outsider := ta.CreateUser(t, "", auth.RoleCreator)

		page := f.client.Get(path)
		for _, want := range []string{"Release note", "Creators", "junsato", "soravell", `name="owner_credit"`, "Not listed"} {
			if !contains(page.Body, want) {
				t.Fatalf("series settings lack %q", want)
			}
		}
		// Members are listed after the owner in the order they joined.
		idx := func(id string) string {
			for i, m := range []string{f.owner.ID, letterer.ID, editor.ID} {
				if m == id {
					return strconv.Itoa(i)
				}
			}
			return "x"
		}
		form := seriesForm("tidebreak-courier", "schedule", "  Weekly,   Saturday ", "owner_credit", "Jerry Okafor",
			"credit_user_"+idx(letterer.ID), letterer.ID, "credit_role_"+idx(letterer.ID), "letterer", "credit_name_"+idx(letterer.ID), "Jun Sato",
			"credit_user_"+idx(editor.ID), editor.ID, "credit_role_"+idx(editor.ID), "")
		if resp := postForm(f.client, path, form); resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("save = %d %s", resp.StatusCode, resp.Body)
		}
		if j := f.publish(t, f.publishForm("en")); j.State != "done" {
			t.Fatalf("publish: %+v", j.Error)
		}
		s, err := pf.Service().SeriesByID(ctx, publish.Viewer{}, seriesID(t, ta, f.project.ID))
		want := []publish.Creator{{UserID: f.owner.ID, Role: "owner", CreditName: "Jerry Okafor"}, {UserID: letterer.ID, Role: "letterer", CreditName: "Jun Sato"}}
		if err != nil || s.Schedule != "Weekly, Saturday" || !reflect.DeepEqual(s.Creators, want) {
			t.Fatalf("series = %q %+v %v", s.Schedule, s.Creators, err)
		}
		// The form shows the saved credits again.
		if page := f.client.Get(path); !contains(page.Body, `value="Jun Sato"`) || !contains(page.Body, `value="Weekly, Saturday"`) {
			t.Fatal("saved credits are not shown")
		}
		rev := func() string {
			b := string(f.client.Get(path).Body)
			i := strings.Index(b, `name="revision" value="`)
			return b[i+len(`name="revision" value="`) : i+len(`name="revision" value="`)+strings.Index(b[i+len(`name="revision" value="`):], `"`)]
		}
		// A non-member, an unknown role or a long release note is refused and nothing changes.
		for name, extra := range map[string][]string{
			"non-member":   {"credit_user_1", outsider.ID, "credit_role_1", "artist"},
			"unknown role": {"credit_user_1", letterer.ID, "credit_role_1", "producer"},
			"long note":    {"schedule", strings.Repeat("x", 61)},
		} {
			resp := postForm(f.client, path, seriesForm("tidebreak-courier", append([]string{"revision", rev()}, extra...)...))
			if resp.StatusCode != http.StatusUnprocessableEntity {
				t.Fatalf("%s = %d", name, resp.StatusCode)
			}
		}
		if again, _ := pf.Service().SeriesByID(ctx, publish.Viewer{}, s.ID); !reflect.DeepEqual(again.Creators, want) || again.Schedule != "Weekly, Saturday" {
			t.Fatalf("refused saves changed the series: %+v", again)
		}
		// Uncrediting a member removes the credit.
		if resp := postForm(f.client, path, seriesForm("tidebreak-courier", "revision", rev(), "credit_user_1", letterer.ID, "credit_role_1", "")); resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("uncredit = %d %s", resp.StatusCode, resp.Body)
		}
		if again, _ := pf.Service().SeriesByID(ctx, publish.Viewer{}, s.ID); len(again.Creators) != 1 || again.Creators[0].UserID != f.owner.ID || again.Schedule != "" {
			t.Fatalf("after uncredit: %+v", again)
		}
	})
}
