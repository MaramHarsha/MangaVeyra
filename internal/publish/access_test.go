package publish_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/publish"
	"github.com/MaramHarsha/MangaVeyra/internal/publish/publishtest"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

// The oracle below restates ADR 0012 §1 independently of the implementation.

type viewerKind string

const (
	anonymous     viewerKind = "anonymous"
	member        viewerKind = "member"         // signed-in server member, not in the project
	projectMember viewerKind = "project_member" // studio viewer of the series' project
	admin         viewerKind = "admin"          // server admin, not in the project
)

// visibilityAllowed: may this viewer read content of this visibility (ignoring rating and state)?
func visibilityAllowed(mode string, k viewerKind, vis string) bool {
	switch k {
	case projectMember:
		return true
	case member, admin:
		return vis != publish.VisibilityPrivate
	default:
		return mode == settings.ModePublic && (vis == publish.VisibilityPublic || vis == publish.VisibilityUnlisted)
	}
}

func ratingAllowed(k viewerKind, rating string) bool {
	return k != anonymous || rating == publish.RatingAll || rating == publish.RatingTeen
}

// expectSeries: may the viewer open the series by ID or slug?
func expectSeries(mode string, k viewerKind, c seriesCase) bool {
	if k == projectMember {
		return true // project members preview their own series in any state
	}
	return c.published && visibilityAllowed(mode, k, c.vis) && ratingAllowed(k, c.rating)
}

// expectListed: does the series appear in ListSeries?
func expectListed(mode string, k viewerKind, c seriesCase, listed bool) bool {
	if !c.published {
		return false
	}
	if listed && (c.vis == publish.VisibilityUnlisted || c.vis == publish.VisibilityPrivate) {
		return false
	}
	return visibilityAllowed(mode, k, c.vis) && ratingAllowed(k, c.rating)
}

type seriesCase struct {
	name      string
	vis       string
	rating    string
	published bool
	spec      publishtest.SeriesSpec
	series    publishtest.Series
	pubs      []pubCase
}

type pubCase struct {
	name     string
	vis      string
	readable bool // status published and published_at passed
	spec     publishtest.PublicationSpec
	pub      publishtest.Publication
}

func pubCases() []pubCase {
	return []pubCase{
		{name: "published", vis: publish.VisibilityPublic, readable: true, spec: publishtest.PublicationSpec{}},
		{name: "published_future", vis: publish.VisibilityPublic, spec: publishtest.PublicationSpec{PublishedAt: publishtest.In(time.Hour)}},
		{name: "scheduled", vis: publish.VisibilityPublic, spec: publishtest.PublicationSpec{Status: publish.PublicationScheduled}},
		{name: "scheduled_due", vis: publish.VisibilityPublic, spec: publishtest.PublicationSpec{Status: publish.PublicationScheduled,
			ScheduledAt: publishtest.Ago(time.Minute)}},
		{name: "unpublished", vis: publish.VisibilityPublic, spec: publishtest.PublicationSpec{Status: publish.PublicationUnpublished}},
		{name: "pub_unlisted", vis: publish.VisibilityUnlisted, readable: true},
		{name: "pub_members", vis: publish.VisibilityMembers, readable: true},
		{name: "pub_private", vis: publish.VisibilityPrivate, readable: true},
	}
}

func TestAccessMatrix(t *testing.T) {
	forEachDialect(t, func(t *testing.T, ta *testutil.TestApp, f *publishtest.Fixture) {
		ctx := context.Background()
		svc := f.Service()
		var cases []*seriesCase
		for _, vis := range []string{publish.VisibilityPublic, publish.VisibilityUnlisted, publish.VisibilityMembers, publish.VisibilityPrivate} {
			for _, rating := range []string{publish.RatingAll, publish.RatingTeen, publish.RatingMature, publish.RatingAdult} {
				cases = append(cases, &seriesCase{name: vis + "/" + rating, vis: vis, rating: rating, published: true,
					spec: publishtest.SeriesSpec{Visibility: vis, AgeRating: rating}})
			}
		}
		cases = append(cases,
			&seriesCase{name: "draft_status", vis: publish.VisibilityPublic, rating: publish.RatingAll,
				spec: publishtest.SeriesSpec{Status: publish.SeriesDraft}},
			&seriesCase{name: "never_published", vis: publish.VisibilityPublic, rating: publish.RatingAll,
				spec: publishtest.SeriesSpec{NeverPublished: true}},
			&seriesCase{name: "future_series", vis: publish.VisibilityPublic, rating: publish.RatingAll,
				spec: publishtest.SeriesSpec{PublishedAt: publishtest.In(time.Hour)}},
			&seriesCase{name: "completed_members_mature", vis: publish.VisibilityMembers, rating: publish.RatingMature, published: true,
				spec: publishtest.SeriesSpec{Status: publish.SeriesCompleted, Visibility: publish.VisibilityMembers, AgeRating: publish.RatingMature}},
		)

		viewers := map[viewerKind]*auth.User{
			anonymous:     nil,
			member:        f.User(auth.RoleReader),
			projectMember: f.User(auth.RoleReader),
			admin:         ta.Admin,
		}
		for _, c := range cases {
			c.spec.Title = "Lanterns of Oru " + c.name
			c.series = f.Series(c.spec)
			f.AddMember(c.series.ProjectID, viewers[projectMember].ID, studio.RoleViewer)
			for _, pc := range pubCases() {
				pc.spec.Visibility = pc.vis
				pc.spec.Pages = 1
				pc.pub = f.Publish(c.series, pc.spec)
				c.pubs = append(c.pubs, pc)
			}
		}

		for _, mode := range []string{settings.ModePersonal, settings.ModePrivate, settings.ModePublic} {
			f.SetMode(mode)
			for _, k := range []viewerKind{anonymous, member, projectMember, admin} {
				v := publishtest.Viewer(viewers[k])
				t.Run(mode+"/"+string(k), func(t *testing.T) {
					listedAll := listAll(t, svc, v, publish.SeriesFilter{Limit: 7})
					listedOnly := listAll(t, svc, v, publish.SeriesFilter{Listed: true, Limit: 5})
					for _, c := range cases {
						checkSeries(t, ctx, svc, mode, k, v, c, listedAll, listedOnly)
					}
				})
			}
		}
	})
}

func checkSeries(t *testing.T, ctx context.Context, svc publish.Service, mode string, k viewerKind, v publish.Viewer,
	c *seriesCase, listedAll, listedOnly []string) {
	t.Helper()
	want := expectSeries(mode, k, *c)
	got, err := svc.SeriesByID(ctx, v, c.series.ID)
	if want {
		if err != nil || got.ID != c.series.ID {
			t.Errorf("%s: SeriesByID = %v, want readable", c.name, err)
		}
	} else if !errors.Is(err, publish.ErrNotFound) {
		t.Errorf("%s: SeriesByID err = %v, want ErrNotFound", c.name, err)
	}
	if _, err := svc.SeriesBySlug(ctx, v, c.series.Slug); (err == nil) != want || (err != nil && !errors.Is(err, publish.ErrNotFound)) {
		t.Errorf("%s: SeriesBySlug err = %v, want readable=%v", c.name, err, want)
	}
	if in := slices.Contains(listedAll, c.series.ID); in != expectListed(mode, k, *c, false) {
		t.Errorf("%s: in ListSeries = %v, want %v", c.name, in, !in)
	}
	if in := slices.Contains(listedOnly, c.series.ID); in != expectListed(mode, k, *c, true) {
		t.Errorf("%s: in ListSeries(Listed) = %v, want %v", c.name, in, !in)
	}

	pubs, err := svc.Publications(ctx, v, c.series.ID, "")
	if !want {
		if !errors.Is(err, publish.ErrNotFound) {
			t.Errorf("%s: Publications err = %v, want ErrNotFound", c.name, err)
		}
	} else if err != nil {
		t.Errorf("%s: Publications: %v", c.name, err)
	}
	var gotPubs, wantPubs []string
	for _, p := range pubs {
		gotPubs = append(gotPubs, p.ID)
	}
	for _, pc := range c.pubs {
		readable := want && pc.readable && visibilityAllowed(mode, k, pc.vis)
		// Unlisted chapters open by their link but are listed only for project members.
		if readable && (pc.vis != publish.VisibilityUnlisted || k == projectMember) {
			wantPubs = append(wantPubs, pc.pub.ID)
		}
		what := fmt.Sprintf("%s/%s", c.name, pc.name)
		p, pages, err := svc.Publication(ctx, v, c.series.ID, pc.pub.Number, "")
		switch {
		case readable && (err != nil || p.ID != pc.pub.ID || len(pages) != 1):
			t.Errorf("%s: Publication = %v (%d pages), want readable", what, err, len(pages))
		case !readable && !errors.Is(err, publish.ErrNotFound):
			t.Errorf("%s: Publication err = %v, want ErrNotFound", what, err)
		}
		if ok := svc.CanRead(ctx, v, publish.Series{ID: c.series.ID}, publish.Publication{ID: pc.pub.ID, SeriesID: c.series.ID}); ok != readable {
			t.Errorf("%s: CanRead = %v, want %v", what, ok, readable)
		}
		page := publish.PublicationPage{ID: pc.pub.Pages[0].ID, PublicationID: pc.pub.ID, AssetID: pc.pub.Pages[0].AssetID}
		u, err := svc.PageImageURL(ctx, v, page, time.Minute)
		switch {
		case readable && (err != nil || u == ""):
			t.Errorf("%s: PageImageURL = %q, %v, want a URL", what, u, err)
		case !readable && (!errors.Is(err, publish.ErrNotFound) || u != ""):
			t.Errorf("%s: PageImageURL = %q, %v, want ErrNotFound", what, u, err)
		}
	}
	if !slices.Equal(gotPubs, wantPubs) {
		t.Errorf("%s: Publications = %v, want %v", c.name, gotPubs, wantPubs)
	}
}

func TestViewerResolution(t *testing.T) {
	forEachDialect(t, func(t *testing.T, ta *testutil.TestApp, f *publishtest.Fixture) {
		ctx := context.Background()
		svc := f.Service()
		f.SetMode(settings.ModePublic)
		public := f.ReadableSeries(publishtest.SeriesSpec{})
		members := f.ReadableSeries(publishtest.SeriesSpec{Visibility: publish.VisibilityMembers})
		mature := f.ReadableSeries(publishtest.SeriesSpec{AgeRating: publish.RatingAdult})
		private := f.ReadableSeries(publishtest.SeriesSpec{Visibility: publish.VisibilityPrivate})

		suspended := f.User(auth.RoleReader)
		if err := ta.Deps.Auth.SetStatus(ctx, suspended.ID, auth.StatusSuspended, "test"); err != nil {
			t.Fatal(err)
		}
		f.AddMember(private.ProjectID, suspended.ID, studio.RoleViewer)
		for name, v := range map[string]publish.Viewer{
			"suspended":          publishtest.Viewer(suspended),
			"unknown user":       {UserID: "01hzzzzzzzzzzzzzzzzzzzzzzz"},
			"admin flag no user": {Admin: true},
			"blank user":         {UserID: "   "},
		} {
			if _, err := svc.SeriesByID(ctx, v, public.ID); err != nil {
				t.Errorf("%s: public series: %v", name, err)
			}
			for _, s := range []publishtest.Series{members, mature, private} {
				if _, err := svc.SeriesByID(ctx, v, s.ID); !errors.Is(err, publish.ErrNotFound) {
					t.Errorf("%s: series %s err = %v, want ErrNotFound (reads as anonymous)", name, s.Title, err)
				}
			}
		}

		// Admins read like any signed-in member: members-only and mature series yes, other projects' private series no.
		av := publishtest.Viewer(ta.Admin)
		for _, s := range []publishtest.Series{public, members, mature} {
			if _, err := svc.SeriesByID(ctx, av, s.ID); err != nil {
				t.Errorf("admin: series %s: %v", s.Title, err)
			}
		}
		wantNotFound(t, "admin private series", func() error { _, err := svc.SeriesByID(ctx, av, private.ID); return err }())

		// The owner is a project member: their private and draft series are previews, scheduled chapters stay hidden.
		draft := f.Series(publishtest.SeriesSpec{Owner: private.Owner, Status: publish.SeriesDraft, NeverPublished: true})
		ov := publishtest.Viewer(private.Owner)
		for _, s := range []publishtest.Series{private, draft} {
			if _, err := svc.SeriesByID(ctx, ov, s.ID); err != nil {
				t.Errorf("owner preview %s: %v", s.Title, err)
			}
		}
		scheduled := f.Publish(private, publishtest.PublicationSpec{Status: publish.PublicationScheduled})
		_, _, err := svc.Publication(ctx, ov, private.ID, scheduled.Number, "")
		wantNotFound(t, "owner scheduled chapter", err)

		// Personal and private servers show anonymous visitors nothing.
		for _, mode := range []string{settings.ModePersonal, settings.ModePrivate} {
			f.SetMode(mode)
			wantNotFound(t, mode+" anonymous", func() error { _, err := svc.SeriesByID(ctx, publish.Viewer{}, public.ID); return err }())
			if list, next, err := svc.ListSeries(ctx, publish.Viewer{}, publish.SeriesFilter{}); err != nil || len(list) != 0 || next != "" {
				t.Errorf("%s anonymous list = %d, %q, %v", mode, len(list), next, err)
			}
		}
	})
}

func TestNotFoundInputs(t *testing.T) {
	forEachDialect(t, func(t *testing.T, ta *testutil.TestApp, f *publishtest.Fixture) {
		ctx := context.Background()
		svc := f.Service()
		f.SetMode(settings.ModePublic)
		s := f.Series(publishtest.SeriesSpec{})
		p := f.Publish(s, publishtest.PublicationSpec{})
		v := publish.Viewer{}

		_, err := svc.SeriesBySlug(ctx, v, "")
		wantNotFound(t, "empty slug", err)
		_, err = svc.SeriesBySlug(ctx, v, "no-such-series")
		wantNotFound(t, "unknown slug", err)
		_, err = svc.SeriesByID(ctx, v, "")
		wantNotFound(t, "empty id", err)
		_, err = svc.Publications(ctx, v, "01hzzzzzzzzzzzzzzzzzzzzzzz", "")
		wantNotFound(t, "unknown series publications", err)
		for _, n := range []float64{0, 2, -1} {
			_, _, err = svc.Publication(ctx, v, s.ID, n, "")
			wantNotFound(t, fmt.Sprint("number ", n), err)
		}
		_, _, err = svc.Publication(ctx, v, s.ID, p.Number, "ja")
		wantNotFound(t, "other language", err)
		_, err = svc.PageImageURL(ctx, v, publish.PublicationPage{}, 0)
		wantNotFound(t, "empty page", err)
		_, err = svc.PageImageURL(ctx, v, publish.PublicationPage{ID: "01hzzzzzzzzzzzzzzzzzzzzzzz"}, 0)
		wantNotFound(t, "unknown page", err)

		// Upper-case and padded slugs resolve to the canonical lowercase slug.
		if got, err := svc.SeriesBySlug(ctx, v, "  "+upper(s.Slug)+" "); err != nil || got.ID != s.ID {
			t.Errorf("SeriesBySlug(upper) = %v, %v", got.ID, err)
		}

		// CanRead never trusts the caller's copies.
		other := f.Series(publishtest.SeriesSpec{})
		for name, args := range map[string][2]any{
			"empty publication": {publish.Series{ID: s.ID}, publish.Publication{}},
			"empty series":      {publish.Series{}, publish.Publication{ID: p.ID}},
			"wrong series":      {publish.Series{ID: other.ID}, publish.Publication{ID: p.ID}},
			"mismatched ids":    {publish.Series{ID: s.ID}, publish.Publication{ID: p.ID, SeriesID: other.ID}},
		} {
			if svc.CanRead(ctx, v, args[0].(publish.Series), args[1].(publish.Publication)) {
				t.Errorf("CanRead(%s) = true", name)
			}
		}
		if !svc.CanRead(ctx, v, publish.Series{ID: s.ID}, publish.Publication{ID: p.ID}) {
			t.Error("CanRead(valid) = false")
		}
		// A forged "public" copy of a private series does not open it.
		priv := f.Series(publishtest.SeriesSpec{Visibility: publish.VisibilityPrivate})
		pp := f.Publish(priv, publishtest.PublicationSpec{})
		if svc.CanRead(ctx, v, publish.Series{ID: priv.ID, Visibility: publish.VisibilityPublic},
			publish.Publication{ID: pp.ID, SeriesID: priv.ID, Status: publish.PublicationPublished, Visibility: publish.VisibilityPublic}) {
			t.Error("CanRead trusted a forged series copy")
		}
	})
}

func upper(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'a' && c <= 'z' {
			b[i] = c - 32
		}
	}
	return string(b)
}

// Unlisted chapters are readable with their link but never listed, except to
// the project's own members (ADR 0012 §1).
func TestUnlistedChaptersAreNotListed(t *testing.T) {
	forEachDialect(t, func(t *testing.T, ta *testutil.TestApp, f *publishtest.Fixture) {
		ctx := context.Background()
		svc := f.Service()
		f.SetMode(settings.ModePublic)
		s := f.Series(publishtest.SeriesSpec{})
		one := f.Publish(s, publishtest.PublicationSpec{})
		two := f.Publish(s, publishtest.PublicationSpec{Visibility: publish.VisibilityUnlisted})
		three := f.Publish(s, publishtest.PublicationSpec{})
		reader, colleague := f.User(auth.RoleReader), f.User(auth.RoleReader)
		f.AddMember(s.ProjectID, colleague.ID, studio.RoleViewer)

		for name, tc := range map[string]struct {
			v    publish.Viewer
			want []string
		}{
			"anonymous":      {publish.Viewer{}, []string{one.ID, three.ID}},
			"member":         {publishtest.Viewer(reader), []string{one.ID, three.ID}},
			"admin":          {publishtest.Viewer(ta.Admin), []string{one.ID, three.ID}},
			"owner":          {publishtest.Viewer(s.Owner), []string{one.ID, two.ID, three.ID}},
			"project member": {publishtest.Viewer(colleague), []string{one.ID, two.ID, three.ID}},
		} {
			pubs, err := svc.Publications(ctx, tc.v, s.ID, "")
			var got []string
			for _, p := range pubs {
				got = append(got, p.ID)
			}
			if err != nil || !slices.Equal(got, tc.want) {
				t.Errorf("%s: Publications = %v, %v; want %v", name, got, err, tc.want)
			}
			// The link still opens it.
			if p, _, err := svc.Publication(ctx, tc.v, s.ID, two.Number, ""); err != nil || p.ID != two.ID {
				t.Errorf("%s: unlisted Publication = %v, %v", name, p.ID, err)
			}
			if !svc.CanRead(ctx, tc.v, publish.Series{ID: s.ID}, publish.Publication{ID: two.ID}) {
				t.Errorf("%s: CanRead(unlisted) = false", name)
			}
		}
	})
}

// A series is visible to a reader only while the reader can read one of its
// chapters; project members always see their own series (preview). A series
// whose only readable chapters are unlisted opens by link but is not listed.
func TestSeriesNeedsAReadableChapter(t *testing.T) {
	forEachDialect(t, func(t *testing.T, ta *testutil.TestApp, f *publishtest.Fixture) {
		ctx := context.Background()
		svc := f.Service()
		f.SetMode(settings.ModePublic)
		owner := f.User(auth.RoleCreator)
		empty := f.Series(publishtest.SeriesSpec{Owner: owner})
		membersOnly := f.Series(publishtest.SeriesSpec{Owner: owner})
		f.Publish(membersOnly, publishtest.PublicationSpec{Visibility: publish.VisibilityMembers})
		unlistedOnly := f.Series(publishtest.SeriesSpec{Owner: owner})
		f.Publish(unlistedOnly, publishtest.PublicationSpec{Visibility: publish.VisibilityUnlisted})
		notYet := f.Series(publishtest.SeriesSpec{Owner: owner})
		f.Publish(notYet, publishtest.PublicationSpec{Status: publish.PublicationScheduled})
		f.Publish(notYet, publishtest.PublicationSpec{Status: publish.PublicationUnpublished})
		f.Publish(notYet, publishtest.PublicationSpec{PublishedAt: publishtest.In(time.Hour)})
		privateOnly := f.Series(publishtest.SeriesSpec{Owner: owner})
		f.Publish(privateOnly, publishtest.PublicationSpec{Visibility: publish.VisibilityPrivate})
		readable := f.Series(publishtest.SeriesSpec{Owner: owner})
		f.Publish(readable, publishtest.PublicationSpec{Visibility: publish.VisibilityPrivate})
		f.Publish(readable, publishtest.PublicationSpec{})
		all := []publishtest.Series{empty, membersOnly, unlistedOnly, notYet, privateOnly, readable}
		reader := f.User(auth.RoleReader)

		type want struct{ get, list, listed []publishtest.Series }
		for name, tc := range map[string]struct {
			v publish.Viewer
			want
		}{
			"anonymous": {publish.Viewer{}, want{get: []publishtest.Series{unlistedOnly, readable},
				list: []publishtest.Series{unlistedOnly, readable}, listed: []publishtest.Series{readable}}},
			"member": {publishtest.Viewer(reader), want{get: []publishtest.Series{membersOnly, unlistedOnly, readable},
				list: []publishtest.Series{membersOnly, unlistedOnly, readable}, listed: []publishtest.Series{membersOnly, readable}}},
			"admin": {publishtest.Viewer(ta.Admin), want{get: []publishtest.Series{membersOnly, unlistedOnly, readable},
				list: []publishtest.Series{membersOnly, unlistedOnly, readable}, listed: []publishtest.Series{membersOnly, readable}}},
			"owner": {publishtest.Viewer(owner), want{get: all, list: all, listed: all}},
		} {
			ids := func(list []publishtest.Series) []string {
				out := []string{}
				for _, s := range list {
					out = append(out, s.ID)
				}
				slices.Sort(out)
				return out
			}
			var got []publishtest.Series
			for _, s := range all {
				if _, err := svc.SeriesByID(ctx, tc.v, s.ID); err == nil {
					got = append(got, s)
				} else if !errors.Is(err, publish.ErrNotFound) {
					t.Fatalf("%s: SeriesByID: %v", name, err)
				}
				if _, err := svc.SeriesBySlug(ctx, tc.v, s.Slug); (err == nil) != slices.Contains(tc.get, s) {
					t.Errorf("%s: SeriesBySlug(%s) err = %v", name, s.Title, err)
				}
			}
			if !slices.Equal(ids(got), ids(tc.get)) {
				t.Errorf("%s: SeriesByID readable = %v, want %v", name, ids(got), ids(tc.get))
			}
			filter := publish.SeriesFilter{OwnerID: owner.ID}
			if list := listAll(t, svc, tc.v, filter); !slices.Equal(sorted(list), ids(tc.list)) {
				t.Errorf("%s: ListSeries = %v, want %v", name, sorted(list), ids(tc.list))
			}
			filter.Listed = true
			if list := listAll(t, svc, tc.v, filter); !slices.Equal(sorted(list), ids(tc.listed)) {
				t.Errorf("%s: ListSeries(Listed) = %v, want %v", name, sorted(list), ids(tc.listed))
			}
		}
	})
}

func sorted(ids []string) []string {
	out := append([]string{}, ids...)
	slices.Sort(out)
	return out
}
