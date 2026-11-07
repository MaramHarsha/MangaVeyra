package pagestore_test

import (
	"context"
	"errors"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/pagestore"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

func TestBrowserActionsRequireLoadedRevision(t *testing.T) {
	forEach(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx, f := context.Background(), newFixture(t, ta)
		page := f.create(t, 1)[0]
		history, err := f.pages.Versions(ctx, page.ID, 10)
		if err != nil {
			t.Fatal(err)
		}
		ch := f.change
		ch.ExpectedVersion = page.Version
		page.Panels[0].Prompt = "A new tide rises"
		page, err = f.pages.SavePage(ctx, page, page.Version, f.change)
		if err != nil {
			t.Fatal(err)
		}
		for _, action := range []struct {
			name string
			run  func() error
		}{
			{"lock", func() error { return f.pages.SetLock(ctx, page.ID, "", "", true, ch) }},
			{"delete", func() error { return f.pages.DeletePage(ctx, page.ID, ch) }},
			{"revert", func() error { _, err := f.pages.Revert(ctx, page.ID, history[0].ID, ch); return err }},
		} {
			if err := action.run(); !errors.Is(err, pagestore.ErrVersion) {
				t.Fatalf("stale %s = %v", action.name, err)
			}
		}
		current, err := f.pages.GetPage(ctx, page.ID)
		if err != nil || current.Version != page.Version || current.Locked || current.Panels[0].Prompt != "A new tide rises" {
			t.Fatalf("stale action changed page: %+v %v", current, err)
		}
		ch.ExpectedVersion = current.Version
		if err := f.pages.SetLock(ctx, page.ID, "", "", true, ch); err != nil {
			t.Fatal(err)
		}
	})
}
