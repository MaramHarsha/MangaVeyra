package studio_test

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/assets"
	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

func biblePNG(t *testing.T) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := png.Encode(&out, image.NewRGBA(image.Rect(0, 0, 4, 5))); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestBibleProfileReferencesOutfitsAndSettings(t *testing.T) {
	forEachDialect(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		svc := studioService(ta)
		bible := core.MustUse[studio.BibleService](ta.Deps)
		owner, actor, p := createProject(t, ta, "Bible CRUD")
		c, err := svc.CreateCharacter(ctx, actor, studio.NewCharacter{ProjectID: p.ID, Name: "凛 Asagiri", Category: "main"})
		if err != nil {
			t.Fatal(err)
		}
		original := c.UpdatedAt
		c, err = bible.UpdateCharacter(ctx, actor, studio.UpdateCharacterInput{ProjectID: p.ID, CharacterID: c.ID, ExpectedUpdatedAt: c.UpdatedAt, Character: studio.NewCharacter{Name: "凛 朝霧", Look: "青いコート", Category: "main"}})
		if err != nil || c.Name != "凛 朝霧" || !c.UpdatedAt.After(original) {
			t.Fatalf("update = %#v %v", c, err)
		}
		if _, err := bible.UpdateCharacter(ctx, actor, studio.UpdateCharacterInput{ProjectID: p.ID, CharacterID: c.ID, ExpectedUpdatedAt: original, Character: studio.NewCharacter{Name: "stale"}}); !errors.Is(err, studio.ErrConflict) {
			t.Fatalf("stale = %v", err)
		}
		a, err := assets.From(ta.Deps).Save(ctx, assets.SaveParams{ProjectID: p.ID, OwnerID: owner.ID, Data: biblePNG(t)})
		if err != nil {
			t.Fatal(err)
		}
		before := c.UpdatedAt
		ref, err := bible.AddCharacterRef(ctx, actor, studio.NewCharacterRef{ProjectID: p.ID, CharacterID: c.ID, AssetID: a.ID, Kind: "front", Label: "正面", Portrait: true, ExpectedUpdatedAt: c.UpdatedAt})
		if err != nil || ref.AssetID != a.ID {
			t.Fatalf("ref=%#v %v", ref, err)
		}
		c, err = svc.GetCharacter(ctx, c.ID)
		if err != nil || c.PortraitAssetID != a.ID || len(c.Refs) != 1 || !c.UpdatedAt.After(before) {
			t.Fatalf("portrait=%#v %v", c, err)
		}
		first, err := bible.CreateOutfit(ctx, actor, studio.NewOutfit{ProjectID: p.ID, CharacterID: c.ID, Name: "Courier coat", AssetID: a.ID, IsDefault: true, ExpectedUpdatedAt: c.UpdatedAt})
		if err != nil {
			t.Fatal(err)
		}
		c, _ = svc.GetCharacter(ctx, c.ID)
		second, err := bible.CreateOutfit(ctx, actor, studio.NewOutfit{ProjectID: p.ID, CharacterID: c.ID, Name: "Storm gear", IsDefault: true, ExpectedUpdatedAt: c.UpdatedAt})
		if err != nil {
			t.Fatal(err)
		}
		c, _ = svc.GetCharacter(ctx, c.ID)
		if len(c.Outfits) != 2 || c.Outfits[0].ID != first.ID || c.Outfits[0].IsDefault || c.Outfits[1].ID != second.ID || !c.Outfits[1].IsDefault {
			t.Fatalf("outfits=%#v", c.Outfits)
		}
		for _, kind := range []string{"location", "prop", "style"} {
			item, err := bible.CreateSettingItem(ctx, actor, studio.NewSettingItem{ProjectID: p.ID, Kind: kind, Name: "灯台", Description: "夕暮れ", Tags: []string{" dusk ", "dusk", "港"}, AssetID: a.ID})
			if err != nil || len(item.Tags) != 2 {
				t.Fatalf("setting=%#v %v", item, err)
			}
			items, err := svc.ListSettingItems(ctx, p.ID, kind)
			if err != nil || len(items) != 1 || items[0].ID != item.ID {
				t.Fatalf("list=%#v %v", items, err)
			}
		}
		items, err := svc.ListSettingItems(ctx, p.ID, "")
		if err != nil || len(items) != 3 {
			t.Fatalf("all setting=%d %v", len(items), err)
		}
	})
}

func TestBibleAuthorizationValidationAndAtomicRollback(t *testing.T) {
	forEachDialect(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		svc := studioService(ta)
		bible := core.MustUse[studio.BibleService](ta.Deps)
		owner, actor, p := createProject(t, ta, "Protected bible")
		_, _, other := createProject(t, ta, "Other bible")
		c, err := svc.CreateCharacter(ctx, actor, studio.NewCharacter{ProjectID: p.ID, Name: "Rin"})
		if err != nil {
			t.Fatal(err)
		}
		imageAsset, err := assets.From(ta.Deps).Save(ctx, assets.SaveParams{ProjectID: p.ID, OwnerID: owner.ID, Data: biblePNG(t)})
		if err != nil {
			t.Fatal(err)
		}
		foreignAsset, err := assets.From(ta.Deps).Save(ctx, assets.SaveParams{ProjectID: other.ID, Data: biblePNG(t)})
		if err != nil {
			t.Fatal(err)
		}
		badAsset, err := assets.From(ta.Deps).Save(ctx, assets.SaveParams{ProjectID: p.ID, Data: []byte("not an image"), ContentType: "image/png"})
		if err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct {
			name, id string
			want     error
		}{{"foreign", foreignAsset.ID, studio.ErrForbidden}, {"not image", badAsset.ID, studio.ErrInvalid}} {
			t.Run(tc.name, func(t *testing.T) {
				_, err := bible.AddCharacterRef(ctx, actor, studio.NewCharacterRef{ProjectID: p.ID, CharacterID: c.ID, AssetID: tc.id, Kind: "front", ExpectedUpdatedAt: c.UpdatedAt})
				if !errors.Is(err, tc.want) {
					t.Fatalf("add=%v", err)
				}
			})
		}
		for _, kind := range []string{"bad", ""} {
			if _, err := bible.CreateSettingItem(ctx, actor, studio.NewSettingItem{ProjectID: p.ID, Kind: kind, Name: "Harbour"}); !errors.Is(err, studio.ErrInvalid) {
				t.Fatalf("invalid kind=%v", err)
			}
		}
		if _, err := bible.CreateSettingItem(ctx, actor, studio.NewSettingItem{ProjectID: p.ID, Kind: "location", Name: "Harbour", Tags: []string{""}}); !errors.Is(err, studio.ErrInvalid) {
			t.Fatalf("invalid tag=%v", err)
		}
		for _, role := range []string{studio.RoleViewer, studio.RoleArtist, studio.RoleWriter} {
			u := ta.CreateUser(t, "", auth.RoleCreator)
			_, err := ta.Deps.DB.Exec(ctx, `INSERT INTO project_members (project_id,user_id,role) VALUES (?,?,?)`, p.ID, u.ID, role)
			if err != nil {
				t.Fatal(err)
			}
			a, _ := svc.ActorFromUser(ctx, u.ID)
			_, editErr := bible.UpdateCharacter(ctx, a, studio.UpdateCharacterInput{ProjectID: p.ID, CharacterID: c.ID, ExpectedUpdatedAt: c.UpdatedAt, Character: studio.NewCharacter{Name: "Rin"}})
			if role == studio.RoleWriter {
				if editErr != nil {
					t.Fatal(editErr)
				}
				c, _ = svc.GetCharacter(ctx, c.ID)
			} else if !errors.Is(editErr, studio.ErrForbidden) {
				t.Fatalf("role %s edit=%v", role, editErr)
			}
			_, refErr := bible.AddCharacterRef(ctx, a, studio.NewCharacterRef{ProjectID: p.ID, CharacterID: c.ID, AssetID: imageAsset.ID, Kind: "front", ExpectedUpdatedAt: c.UpdatedAt})
			if role == studio.RoleArtist {
				if refErr != nil {
					t.Fatal(refErr)
				}
				c, _ = svc.GetCharacter(ctx, c.ID)
			} else if !errors.Is(refErr, studio.ErrForbidden) {
				t.Fatalf("role %s ref=%v", role, refErr)
			}
		}
		before := c.UpdatedAt
		rollback := errors.New("rollback")
		err = ta.Deps.DB.WithTx(ctx, func(tx *db.Tx) error {
			if _, err := bible.CreateOutfitTx(ctx, tx, actor, studio.NewOutfit{ProjectID: p.ID, CharacterID: c.ID, Name: "Temporary", IsDefault: true, ExpectedUpdatedAt: c.UpdatedAt}); err != nil {
				return err
			}
			return rollback
		})
		if !errors.Is(err, rollback) {
			t.Fatal(err)
		}
		c, _ = svc.GetCharacter(ctx, c.ID)
		if len(c.Outfits) != 0 || !c.UpdatedAt.Equal(before) {
			t.Fatalf("rollback leaked=%#v", c)
		}
		err = ta.Deps.DB.WithTx(ctx, func(tx *db.Tx) error {
			_, err := svc.AddCharacterRefTx(ctx, tx, c.ID, badAsset.ID, "front", "Invalid")
			return err
		})
		if !errors.Is(err, studio.ErrInvalid) {
			t.Fatalf("Tx nonimage=%v", err)
		}
	})
}
