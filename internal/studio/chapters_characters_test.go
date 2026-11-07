package studio_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/assets"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

func TestChapterAndCharacterCreationOrder(t *testing.T) {
	forEachDialect(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		svc := studioService(ta)
		_, actor, project := createProject(t, ta, "Ordered Story")
		chapter1, err := svc.CreateChapter(ctx, actor, project.ID, "Departure")
		if err != nil {
			t.Fatal(err)
		}
		chapter2, err := svc.CreateChapter(ctx, actor, project.ID, "Crossing")
		if err != nil {
			t.Fatal(err)
		}
		if chapter1.Number != 1 || chapter2.Number != 2 {
			t.Fatalf("chapter numbers = %d, %d", chapter1.Number, chapter2.Number)
		}
		chapters, err := svc.ListChapters(ctx, project.ID)
		if err != nil || len(chapters) != 2 || chapters[0].ID != chapter1.ID || chapters[1].ID != chapter2.ID {
			t.Fatalf("ListChapters = %#v, %v", chapters, err)
		}

		first, err := svc.CreateCharacter(ctx, actor, studio.NewCharacter{ProjectID: project.ID, Name: "Rin Asagiri", Role: "courier", Category: "main"})
		if err != nil {
			t.Fatal(err)
		}
		second, err := svc.CreateCharacter(ctx, actor, studio.NewCharacter{ProjectID: project.ID, Name: "Pip", Role: "gull", Category: "supporting"})
		if err != nil {
			t.Fatal(err)
		}
		if first.Order != 0 || second.Order != 1 {
			t.Fatalf("character order = %d, %d", first.Order, second.Order)
		}
		characters, err := svc.ListCharacters(ctx, project.ID)
		if err != nil || len(characters) != 2 || characters[0].ID != first.ID || characters[1].ID != second.ID {
			t.Fatalf("ListCharacters = %#v, %v", characters, err)
		}
	})
}

func TestConcurrentChapterAndCharacterAllocation(t *testing.T) {
	forEachDialect(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		svc := studioService(ta)
		_, actor, project := createProject(t, ta, "Concurrent Ordering")
		const count = 8
		errs := make(chan error, count*2)
		var wg sync.WaitGroup
		for i := range count {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				_, err := svc.CreateChapter(ctx, actor, project.ID, fmt.Sprintf("Chapter %d", i))
				errs <- err
			}(i)
		}
		wg.Wait()
		for range count {
			if err := <-errs; err != nil {
				t.Fatalf("concurrent CreateChapter: %v", err)
			}
		}
		chapters, err := svc.ListChapters(ctx, project.ID)
		if err != nil || len(chapters) != count {
			t.Fatalf("ListChapters count = %d, %v", len(chapters), err)
		}
		for i, chapter := range chapters {
			if chapter.Number != i+1 {
				t.Fatalf("chapter[%d].Number = %d", i, chapter.Number)
			}
		}

		for i := range count {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				_, err := svc.CreateCharacter(ctx, actor, studio.NewCharacter{ProjectID: project.ID,
					Name: fmt.Sprintf("Character %d", i), Category: "supporting"})
				errs <- err
			}(i)
		}
		wg.Wait()
		for range count {
			if err := <-errs; err != nil {
				t.Fatalf("concurrent CreateCharacter: %v", err)
			}
		}
		characters, err := svc.ListCharacters(ctx, project.ID)
		if err != nil || len(characters) != count {
			t.Fatalf("ListCharacters count = %d, %v", len(characters), err)
		}
		for i, character := range characters {
			if character.Order != i {
				t.Fatalf("character[%d].Order = %d", i, character.Order)
			}
		}
	})
}

func TestCharacterReferencesPortraitSettingsAndAILabel(t *testing.T) {
	forEachDialect(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		svc := studioService(ta)
		owner, actor, project := createProject(t, ta, "Character Bible")
		character, err := svc.CreateCharacter(ctx, actor, studio.NewCharacter{ProjectID: project.ID, Name: "Kaito Mbeki", Category: "main"})
		if err != nil {
			t.Fatal(err)
		}
		asset, err := assets.From(ta.Deps).Save(ctx, assets.SaveParams{OwnerID: owner.ID, ProjectID: project.ID,
			Kind: "character_ref", Source: assets.SourceUpload, Data: biblePNG(t)})
		if err != nil {
			t.Fatal(err)
		}
		chapter, err := svc.CreateChapter(ctx, actor, project.ID, "Portrait Test")
		if err != nil {
			t.Fatal(err)
		}
		if err := ta.Deps.DB.WithTx(ctx, func(tx *db.Tx) error {
			ref, err := svc.AddCharacterRefTx(ctx, tx, character.ID, asset.ID, "front", "Front view")
			if err != nil {
				return err
			}
			if ref.Order != 0 || ref.AssetID != asset.ID {
				t.Fatalf("reference = %#v", ref)
			}
			if err := svc.SetCharacterPortraitTx(ctx, tx, character.ID, asset.ID); err != nil {
				return err
			}
			return svc.SetChapterAILabelTx(ctx, tx, chapter.ID, "script", "ai_assisted")
		}); err != nil {
			t.Fatal(err)
		}
		got, err := svc.GetCharacter(ctx, character.ID)
		if err != nil || got.PortraitAssetID != asset.ID || len(got.Refs) != 1 || got.Refs[0].Label != "Front view" {
			t.Fatalf("GetCharacter = %#v, %v", got, err)
		}
		gotChapter, err := svc.GetChapter(ctx, chapter.ID)
		if err != nil || gotChapter.AILabel["script"] != "ai_assisted" {
			t.Fatalf("GetChapter label = %#v, %v", gotChapter.AILabel, err)
		}

		settingID := db.NewID()
		if _, err := ta.Deps.DB.Exec(ctx, `INSERT INTO setting_items (id, project_id, kind, name, tags, created_at, updated_at)
			VALUES (?, ?, 'location', 'Harbour Gate', ?, ?, ?)`, settingID, project.ID, `["storm"]`, db.Now(), db.Now()); err != nil {
			t.Fatal(err)
		}
		items, err := svc.ListSettingItems(ctx, project.ID, "location")
		if err != nil || len(items) != 1 || items[0].ID != settingID || len(items[0].Tags) != 1 {
			t.Fatalf("ListSettingItems = %#v, %v", items, err)
		}

		_, _, otherProject := createProject(t, ta, "Other Assets")
		otherAsset, err := assets.From(ta.Deps).Save(ctx, assets.SaveParams{OwnerID: owner.ID, ProjectID: otherProject.ID,
			Kind: "character_ref", Source: assets.SourceUpload, Data: []byte("wrong-project")})
		if err != nil {
			t.Fatal(err)
		}
		err = ta.Deps.DB.WithTx(ctx, func(tx *db.Tx) error {
			_, err := svc.AddCharacterRefTx(ctx, tx, character.ID, otherAsset.ID, "side", "Wrong")
			return err
		})
		if !errors.Is(err, studio.ErrForbidden) {
			t.Fatalf("cross-project reference error = %v, want ErrForbidden", err)
		}
	})
}
