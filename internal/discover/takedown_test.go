package discover_test

import (
	"context"
	"testing"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/admin/adminapi"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/publish/publishtest"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

// Review fix: Explore orders and features series by their newest chapter
// readers can actually open; a chapter an admin took down does not count.
func TestExploreIgnoresTakenDownChaptersWhenOrdering(t *testing.T) {
	forEachDialect(t, func(t *testing.T, ta *testutil.TestApp, f *publishtest.Fixture) {
		ctx := context.Background()
		at := func(d time.Duration) *time.Time { v := db.Now().Add(-d); return &v }
		kettle := f.Series(publishtest.SeriesSpec{Title: "Kettle Knight"})
		f.Publish(kettle, publishtest.PublicationSpec{PublishedAt: at(3 * time.Hour)})
		harbour := f.Series(publishtest.SeriesSpec{Title: "Harbour Lantern"})
		f.Publish(harbour, publishtest.PublicationSpec{PublishedAt: at(5 * time.Hour)})
		newest := f.Publish(harbour, publishtest.PublicationSpec{PublishedAt: at(time.Hour)})
		hero := func() string {
			return section(t, get(t, ta.NewClient(), "/explore"), `class="mv-explore__hero"`, "</section>")
		}
		has(t, "featured before the takedown", hero(), "Harbour Lantern")

		if err := ta.Deps.Settings.SetKey(ctx, adminapi.TakedownKey(adminapi.TakedownChapter, newest.ChapterID),
			adminapi.Takedown{Kind: adminapi.TakedownChapter, TargetID: newest.ChapterID, SeriesID: harbour.ID, By: ta.Admin.ID, At: db.Now().Format(time.RFC3339)}, ta.Admin.ID); err != nil {
			t.Fatal(err)
		}
		featured := hero()
		has(t, "featured after the takedown", featured, "Kettle Knight")
		hasNot(t, "featured after the takedown", featured, "Harbour Lantern")
	})
}
