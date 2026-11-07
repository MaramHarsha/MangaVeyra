package assets_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/assets"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/storage"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil/testdb"
)

func TestStagedAssetCommitsOnlyItsMetadataAndSurvivesCleanup(t *testing.T) {
	for _, dialect := range testdb.Dialects() {
		t.Run(dialect, func(t *testing.T) {
			ta := testutil.NewTestApp(t, testutil.WithDialect(dialect))
			svc, ctx := assets.From(ta.Deps), context.Background()
			data := pngBytes(t, 24, 32)
			staged, err := svc.Stage(ctx, assets.SaveParams{OwnerID: ta.Admin.ID, Kind: "layer", Source: assets.SourceAI,
				Data: data, Meta: map[string]string{"stage": "pending"}})
			if err != nil {
				t.Fatal(err)
			}
			metadata := staged.Asset()
			if _, err := svc.Get(ctx, metadata.ID); !errors.Is(err, assets.ErrNotFound) {
				t.Fatalf("stage inserted row: %v", err)
			}
			metadata.ID = "forged"
			*metadata.OwnerID = "forged"
			metadata.Meta[0] = '!'
			if staged.Asset().ID == "forged" || *staged.Asset().OwnerID != ta.Admin.ID || staged.Asset().Meta[0] == '!' {
				t.Fatal("staged metadata could be mutated by its consumer")
			}
			if err := ta.Deps.DB.WithTx(ctx, func(tx *db.Tx) error {
				_, err := svc.CommitStagedTx(ctx, tx, staged)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if err := svc.DiscardStaged(ctx, staged); err == nil {
				t.Fatal("cleanup removed a committed object")
			}
			got, _, err := svc.Read(ctx, staged.Asset().ID, 0)
			if err != nil || !bytes.Equal(got, data) {
				t.Fatalf("committed bytes = %d, %v", len(got), err)
			}
		})
	}
}

func TestStagedAssetRollbackLeavesNoRowAndCanBeDiscarded(t *testing.T) {
	for _, dialect := range testdb.Dialects() {
		t.Run(dialect, func(t *testing.T) {
			ta := testutil.NewTestApp(t, testutil.WithDialect(dialect))
			svc, ctx := assets.From(ta.Deps), context.Background()
			staged, err := svc.Stage(ctx, assets.SaveParams{Kind: "export", Source: assets.SourceSystem, Data: []byte("pending export")})
			if err != nil {
				t.Fatal(err)
			}
			err = ta.Deps.DB.WithTx(ctx, func(tx *db.Tx) error {
				if _, err := svc.CommitStagedTx(ctx, tx, staged); err != nil {
					return err
				}
				return context.Canceled
			})
			if !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			if _, err := svc.Get(ctx, staged.Asset().ID); !errors.Is(err, assets.ErrNotFound) {
				t.Fatalf("rollback retained row: %v", err)
			}
			if err := svc.DiscardStaged(ctx, staged); err != nil {
				t.Fatal(err)
			}
			if _, err := storage.ReadAll(ctx, ta.Deps.Store, staged.Asset().Key, 1024); err == nil {
				t.Fatal("discard retained staged object")
			}
		})
	}
}
