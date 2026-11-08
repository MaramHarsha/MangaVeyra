package publish_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/publish"
	"github.com/MaramHarsha/MangaVeyra/internal/publish/publishtest"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

func TestListSeriesOrderAndPaging(t *testing.T) {
	forEachDialect(t, func(t *testing.T, ta *testutil.TestApp, f *publishtest.Fixture) {
		svc := f.Service()
		f.SetMode(settings.ModePublic)
		base := db.Now().Add(-time.Hour)
		type entry struct {
			id string
			at time.Time
		}
		var want []entry
		for i := range 7 {
			at := base.Add(-time.Duration(i*7%5) * time.Minute).Add(-time.Duration(i) * time.Second)
			s := f.Series(publishtest.SeriesSpec{PublishedAt: &at})
			want = append(want, entry{s.ID, at})
		}
		// Series published in the same microsecond are ordered by id, whatever
		// order they were stored in.
		tie := base.Add(-2 * time.Minute)
		ids := []string{db.NewID(), db.NewID(), db.NewID(), db.NewID()}
		for _, i := range []int{2, 0, 3, 1} {
			s := f.Series(publishtest.SeriesSpec{ID: ids[i], PublishedAt: &tie})
			want = append(want, entry{s.ID, tie})
		}
		sort.Slice(want, func(i, j int) bool {
			if !want[i].at.Equal(want[j].at) {
				return want[i].at.After(want[j].at)
			}
			return want[i].id > want[j].id
		})
		var wantIDs []string
		for _, e := range want {
			wantIDs = append(wantIDs, e.id)
		}
		// Not listed for anonymous visitors.
		for _, spec := range []publishtest.SeriesSpec{
			{Visibility: publish.VisibilityUnlisted}, {Visibility: publish.VisibilityMembers},
			{Visibility: publish.VisibilityPrivate}, {AgeRating: publish.RatingMature},
			{Status: publish.SeriesDraft}, {NeverPublished: true}, {PublishedAt: publishtest.In(time.Minute)},
		} {
			f.Series(spec)
		}

		for _, limit := range []int{1, 3, 4, 10, 50} {
			got := listAll(t, svc, publish.Viewer{}, publish.SeriesFilter{Listed: true, Limit: limit})
			if !slices.Equal(got, wantIDs) {
				t.Fatalf("limit %d: ListSeries = %v, want %v", limit, got, wantIDs)
			}
		}
		list, next, err := svc.ListSeries(context.Background(), publish.Viewer{}, publish.SeriesFilter{Listed: true, Limit: 4})
		mustOK(t, "first page", err)
		if len(list) != 4 || next == "" {
			t.Fatalf("first page = %d, next %q", len(list), next)
		}
		// The cursor is stable while new series are published ahead of it.
		f.Series(publishtest.SeriesSpec{})
		rest := listAll(t, svc, publish.Viewer{}, publish.SeriesFilter{Listed: true, Limit: 4, Cursor: next})
		if !slices.Equal(rest, wantIDs[4:]) {
			t.Fatalf("after cursor = %v, want %v", rest, wantIDs[4:])
		}
		// Every list item is decoded, with non-nil lists.
		if s := list[0]; s.Title == "" || s.Slug == "" || s.PublishedAt == nil || s.Genres == nil || s.SupportLinks == nil || s.AILabel == nil {
			t.Fatalf("list item not decoded: %+v", s)
		}
	})
}

func TestListSeriesFiltersAndBounds(t *testing.T) {
	forEachDialect(t, func(t *testing.T, ta *testutil.TestApp, f *publishtest.Fixture) {
		ctx := context.Background()
		svc := f.Service()
		f.SetMode(settings.ModePublic)
		owner := f.User(auth.RoleCreator)
		reader := f.User(auth.RoleReader)
		rv := publishtest.Viewer(reader)

		scifi := f.Series(publishtest.SeriesSpec{Owner: owner, Genres: []string{"sci-fi", "mystery"}})
		sci := f.Series(publishtest.SeriesSpec{Owner: owner, Genres: []string{"sci"}})
		romance := f.Series(publishtest.SeriesSpec{Genres: []string{"romance"}})
		weird := f.Series(publishtest.SeriesSpec{Genres: []string{`50%_off\"x`}})
		unlisted := f.Series(publishtest.SeriesSpec{Owner: owner, Visibility: publish.VisibilityUnlisted, Genres: []string{"sci"}})
		members := f.Series(publishtest.SeriesSpec{Owner: owner, Visibility: publish.VisibilityMembers})
		private := f.Series(publishtest.SeriesSpec{Owner: owner, Visibility: publish.VisibilityPrivate})

		set := func(ids []string) []string { s := slices.Clone(ids); slices.Sort(s); return s }
		check := func(name string, v publish.Viewer, filter publish.SeriesFilter, want ...publishtest.Series) {
			t.Helper()
			var wantIDs []string
			for _, s := range want {
				wantIDs = append(wantIDs, s.ID)
			}
			if got := listAll(t, svc, v, filter); !slices.Equal(set(got), set(wantIDs)) {
				t.Errorf("%s: got %v, want %v", name, got, wantIDs)
			}
		}

		check("owner anonymous listed", publish.Viewer{}, publish.SeriesFilter{OwnerID: owner.ID, Listed: true}, scifi, sci)
		check("owner anonymous", publish.Viewer{}, publish.SeriesFilter{OwnerID: owner.ID}, scifi, sci, unlisted)
		check("owner member listed", rv, publish.SeriesFilter{OwnerID: owner.ID, Listed: true}, scifi, sci, members)
		check("owner member", rv, publish.SeriesFilter{OwnerID: owner.ID}, scifi, sci, unlisted, members)
		// The owner sees their private series in unlisted views (library), never in listed ones (profile, Explore).
		check("owner self listed", publishtest.Viewer(owner), publish.SeriesFilter{OwnerID: owner.ID, Listed: true}, scifi, sci, members)
		check("owner self", publishtest.Viewer(owner), publish.SeriesFilter{OwnerID: owner.ID}, scifi, sci, unlisted, members, private)

		check("genre exact", publish.Viewer{}, publish.SeriesFilter{Genre: "sci", Listed: true}, sci)
		check("genre unlisted included", publish.Viewer{}, publish.SeriesFilter{Genre: "sci"}, sci, unlisted)
		check("genre case", publish.Viewer{}, publish.SeriesFilter{Genre: " SCI-FI "}, scifi)
		check("genre second", publish.Viewer{}, publish.SeriesFilter{Genre: "mystery"}, scifi)
		check("genre wildcard", publish.Viewer{}, publish.SeriesFilter{Genre: "%"})
		check("genre underscore", publish.Viewer{}, publish.SeriesFilter{Genre: "sc_"})
		check("genre metacharacters", publish.Viewer{}, publish.SeriesFilter{Genre: `50%_off\"x`}, weird)
		check("genre none", publish.Viewer{}, publish.SeriesFilter{Genre: "horror"})

		check("ids", publish.Viewer{}, publish.SeriesFilter{IDs: []string{romance.ID, private.ID, members.ID, " " + sci.ID, sci.ID, ""}}, romance, sci)
		check("ids member", rv, publish.SeriesFilter{IDs: []string{romance.ID, private.ID, members.ID}}, romance, members)
		check("ids listed", publish.Viewer{}, publish.SeriesFilter{IDs: []string{romance.ID, unlisted.ID}, Listed: true}, romance)
		check("ids empty", publish.Viewer{}, publish.SeriesFilter{IDs: []string{}})
		check("ids blank", publish.Viewer{}, publish.SeriesFilter{IDs: []string{" "}})
		check("ids and owner", publish.Viewer{}, publish.SeriesFilter{IDs: []string{romance.ID, sci.ID}, OwnerID: owner.ID}, sci)

		tooMany := make([]string, publish.MaxFilterIDs+1)
		for i := range tooMany {
			tooMany[i] = fmt.Sprintf("id-%d", i)
		}
		if _, _, err := svc.ListSeries(ctx, publish.Viewer{}, publish.SeriesFilter{IDs: tooMany}); !errors.Is(err, publish.ErrInvalid) {
			t.Errorf("too many ids err = %v, want ErrInvalid", err)
		}
		for _, cursor := range []string{"!!", "bm90LWpzb24", "e30", "eyJ0IjoiMjAyNi0xMS0wOFQwMDowMDowMFoifQ"} {
			if _, _, err := svc.ListSeries(ctx, publish.Viewer{}, publish.SeriesFilter{Cursor: cursor}); !errors.Is(err, publish.ErrInvalid) {
				t.Errorf("cursor %q err = %v, want ErrInvalid", cursor, err)
			}
		}

		// Limit bounds: 0 and negative take the default, large values are capped.
		for range publish.DefaultListLimit + 3 {
			f.Series(publishtest.SeriesSpec{})
		}
		for _, limit := range []int{0, -5} {
			list, next, err := svc.ListSeries(ctx, publish.Viewer{}, publish.SeriesFilter{Limit: limit})
			mustOK(t, "default limit", err)
			if len(list) != publish.DefaultListLimit || next == "" {
				t.Errorf("limit %d: %d items, next %q; want %d and a cursor", limit, len(list), next, publish.DefaultListLimit)
			}
		}
		list, next, err := svc.ListSeries(ctx, publish.Viewer{}, publish.SeriesFilter{Limit: 10_000})
		mustOK(t, "large limit", err)
		if total := len(listAll(t, svc, publish.Viewer{}, publish.SeriesFilter{})); len(list) != total || next != "" || total > publish.MaxListLimit {
			t.Errorf("large limit: %d items (total %d), next %q", len(list), total, next)
		}
	})
}

func TestListSeriesCapsLimit(t *testing.T) {
	ta := testutil.NewTestApp(t)
	f := publishtest.New(t, ta)
	f.SetMode(settings.ModePublic)
	owner := f.User(auth.RoleCreator)
	for range publish.MaxListLimit + 1 {
		f.Series(publishtest.SeriesSpec{Owner: owner})
	}
	list, next, err := f.Service().ListSeries(context.Background(), publish.Viewer{}, publish.SeriesFilter{Limit: 10_000})
	mustOK(t, "ListSeries", err)
	if len(list) != publish.MaxListLimit || next == "" {
		t.Fatalf("got %d items, next %q; want %d and a cursor", len(list), next, publish.MaxListLimit)
	}
}
