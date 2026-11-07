package studio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/assets"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/storage"
)

var _ BibleService = (*service)(nil)

// imageAssetMetadata accepts raster assets whose header assets.Service decoded.
// Public attachment methods decode the complete image before writing; upload
// handlers decode before staging and use the metadata-only Tx operations.
func imageAssetMetadata(a assets.Asset) error {
	if storage.CheckType(a.ContentType, storage.ImageTypes...) != nil || a.Width <= 0 || a.Height <= 0 {
		return fmt.Errorf("%w: reference must be a decoded raster image", ErrInvalid)
	}
	return nil
}

func (s *service) decodedImage(ctx context.Context, projectID, assetID string) error {
	data, a, err := s.assets.Read(ctx, assetID, assets.MaxBytes)
	if errors.Is(err, assets.ErrNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if a.ProjectID == nil || *a.ProjectID != projectID {
		return ErrForbidden
	}
	if err := imageAssetMetadata(a); err != nil {
		return err
	}
	if _, _, err := storage.DecodeImage(data, storage.DefaultImageLimits); err != nil {
		return fmt.Errorf("%w: invalid reference image", ErrInvalid)
	}
	return nil
}

func (s *service) imageInProjectTx(ctx context.Context, tx *db.Tx, projectID, assetID string) error {
	a, err := s.assets.GetTx(ctx, tx, assetID)
	if errors.Is(err, assets.ErrNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if a.ProjectID == nil || *a.ProjectID != projectID {
		return ErrForbidden
	}
	return imageAssetMetadata(a)
}

func touchCharacter(ctx context.Context, tx *db.Tx, id string) error {
	_, err := tx.Exec(ctx, `UPDATE characters SET updated_at = ? WHERE id = ?`, db.Now(), id)
	return err
}

func (s *service) editableCharacterTx(ctx context.Context, tx *db.Tx, actor Actor, projectID, characterID string, action Action, expected time.Time) (Character, error) {
	if expected.IsZero() {
		return Character{}, ErrInvalid
	}
	if err := s.AuthorizeTx(ctx, tx, actor, projectID, action); err != nil {
		return Character{}, err
	}
	c, err := s.GetCharacterTx(ctx, tx, characterID)
	if err != nil {
		return Character{}, err
	}
	if c.ProjectID != projectID {
		return Character{}, ErrNotFound
	}
	if !c.UpdatedAt.Equal(expected) {
		return Character{}, ErrConflict
	}
	return c, nil
}

func (s *service) UpdateCharacter(ctx context.Context, actor Actor, in UpdateCharacterInput) (Character, error) {
	in.ProjectID, in.CharacterID = strings.TrimSpace(in.ProjectID), strings.TrimSpace(in.CharacterID)
	in.Character.ProjectID = in.ProjectID
	in.Character = normalizeNewCharacter(in.Character)
	if err := validateNewCharacter(in.Character); err != nil {
		return Character{}, err
	}
	var out Character
	err := s.db.WithTx(ctx, func(tx *db.Tx) error {
		if _, err := s.editableCharacterTx(ctx, tx, actor, in.ProjectID, in.CharacterID, ActEditScript, in.ExpectedUpdatedAt); err != nil {
			return err
		}
		c := in.Character
		if _, err := tx.Exec(ctx, `UPDATE characters SET name = ?, kanji = ?, role = ?, role_line = ?, age = ?, look = ?,
			personality = ?, speech_style = ?, category = ?, updated_at = ? WHERE id = ?`, c.Name, c.Kanji,
			c.Role, c.RoleLine, c.Age, c.Look, c.Personality, c.SpeechStyle, c.Category, db.Now(), in.CharacterID); err != nil {
			return err
		}
		var err error
		out, err = getCharacter(ctx, tx, in.CharacterID)
		return err
	})
	return out, err
}

func (s *service) AddCharacterRef(ctx context.Context, actor Actor, in NewCharacterRef) (CharacterRef, error) {
	in.ProjectID, in.CharacterID, in.AssetID = strings.TrimSpace(in.ProjectID), strings.TrimSpace(in.CharacterID), strings.TrimSpace(in.AssetID)
	if err := s.Authorize(ctx, actor, in.ProjectID, ActEditArt); err != nil {
		return CharacterRef{}, err
	}
	if err := s.decodedImage(ctx, in.ProjectID, in.AssetID); err != nil {
		return CharacterRef{}, err
	}
	var out CharacterRef
	err := s.db.WithTx(ctx, func(tx *db.Tx) error {
		if _, err := s.editableCharacterTx(ctx, tx, actor, in.ProjectID, in.CharacterID, ActEditArt, in.ExpectedUpdatedAt); err != nil {
			return err
		}
		var err error
		out, err = s.AddCharacterRefTx(ctx, tx, in.CharacterID, in.AssetID, in.Kind, in.Label)
		if err != nil {
			return err
		}
		if in.Portrait {
			return s.SetCharacterPortraitTx(ctx, tx, in.CharacterID, in.AssetID)
		}
		return nil
	})
	return out, err
}

func (s *service) CreateOutfit(ctx context.Context, actor Actor, in NewOutfit) (Outfit, error) {
	if err := s.Authorize(ctx, actor, in.ProjectID, ActEditScript); err != nil {
		return Outfit{}, err
	}
	if in.AssetID != "" {
		if err := s.decodedImage(ctx, in.ProjectID, in.AssetID); err != nil {
			return Outfit{}, err
		}
	}
	var out Outfit
	err := s.db.WithTx(ctx, func(tx *db.Tx) error { var err error; out, err = s.CreateOutfitTx(ctx, tx, actor, in); return err })
	return out, err
}

func (s *service) CreateOutfitTx(ctx context.Context, tx *db.Tx, actor Actor, in NewOutfit) (Outfit, error) {
	if tx == nil {
		return Outfit{}, ErrInvalid
	}
	in.Name, in.Description, in.AssetID = strings.TrimSpace(in.Name), strings.TrimSpace(in.Description), strings.TrimSpace(in.AssetID)
	if err := validateText("outfit name", in.Name, maxShortText, true); err != nil {
		return Outfit{}, err
	}
	if err := validateText("outfit description", in.Description, 4000, false); err != nil {
		return Outfit{}, err
	}
	if _, err := s.editableCharacterTx(ctx, tx, actor, in.ProjectID, in.CharacterID, ActEditScript, in.ExpectedUpdatedAt); err != nil {
		return Outfit{}, err
	}
	if in.AssetID != "" {
		if err := s.imageInProjectTx(ctx, tx, in.ProjectID, in.AssetID); err != nil {
			return Outfit{}, err
		}
	}
	var order int
	if err := tx.Get(ctx, &order, `SELECT COALESCE(MAX(order_idx) + 1, 0) FROM character_outfits WHERE character_id = ?`, in.CharacterID); err != nil {
		return Outfit{}, err
	}
	if in.IsDefault {
		if _, err := tx.Exec(ctx, `UPDATE character_outfits SET is_default = FALSE WHERE character_id = ?`, in.CharacterID); err != nil {
			return Outfit{}, err
		}
	}
	out := Outfit{ID: db.NewID(), Name: in.Name, Description: in.Description, AssetID: in.AssetID, IsDefault: in.IsDefault}
	var imageID any
	if out.AssetID != "" {
		imageID = out.AssetID
	}
	if _, err := tx.Exec(ctx, `INSERT INTO character_outfits (id, character_id, name, description, asset_id, is_default, order_idx, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, out.ID, in.CharacterID, out.Name, out.Description, imageID, out.IsDefault, order, db.Now()); err != nil {
		return Outfit{}, err
	}
	return out, touchCharacter(ctx, tx, in.CharacterID)
}

func (s *service) CreateSettingItem(ctx context.Context, actor Actor, in NewSettingItem) (SettingItem, error) {
	if err := s.Authorize(ctx, actor, in.ProjectID, ActEditScript); err != nil {
		return SettingItem{}, err
	}
	if in.AssetID != "" {
		if err := s.decodedImage(ctx, in.ProjectID, in.AssetID); err != nil {
			return SettingItem{}, err
		}
	}
	var out SettingItem
	err := s.db.WithTx(ctx, func(tx *db.Tx) error { var err error; out, err = s.CreateSettingItemTx(ctx, tx, actor, in); return err })
	return out, err
}

func (s *service) CreateSettingItemTx(ctx context.Context, tx *db.Tx, actor Actor, in NewSettingItem) (SettingItem, error) {
	if tx == nil {
		return SettingItem{}, ErrInvalid
	}
	in.Name, in.Description = strings.TrimSpace(in.Name), strings.TrimSpace(in.Description)
	if in.Kind != "location" && in.Kind != "prop" && in.Kind != "style" {
		return SettingItem{}, ErrInvalid
	}
	if err := validateText("setting name", in.Name, maxShortText, true); err != nil {
		return SettingItem{}, err
	}
	if err := validateText("setting description", in.Description, 8000, false); err != nil {
		return SettingItem{}, err
	}
	if len(in.Tags) > 32 {
		return SettingItem{}, ErrInvalid
	}
	tags := make([]string, 0, len(in.Tags))
	seen := map[string]bool{}
	for _, tag := range in.Tags {
		tag = strings.TrimSpace(tag)
		if err := validateText("setting tag", tag, 80, true); err != nil {
			return SettingItem{}, err
		}
		if !seen[tag] {
			tags = append(tags, tag)
			seen[tag] = true
		}
	}
	if err := s.AuthorizeTx(ctx, tx, actor, in.ProjectID, ActEditScript); err != nil {
		return SettingItem{}, err
	}
	if in.AssetID != "" {
		if err := s.imageInProjectTx(ctx, tx, in.ProjectID, in.AssetID); err != nil {
			return SettingItem{}, err
		}
	}
	var order int
	if err := tx.Get(ctx, &order, `SELECT COALESCE(MAX(order_idx) + 1, 0) FROM setting_items WHERE project_id = ? AND kind = ?`, in.ProjectID, in.Kind); err != nil {
		return SettingItem{}, err
	}
	out := SettingItem{ID: db.NewID(), ProjectID: in.ProjectID, Kind: in.Kind, Name: in.Name, Description: in.Description, Tags: tags, AssetID: in.AssetID}
	var imageID any
	if in.AssetID != "" {
		imageID = in.AssetID
	}
	raw, _ := json.Marshal(tags)
	now := db.Now()
	_, err := tx.Exec(ctx, `INSERT INTO setting_items (id, project_id, kind, name, description, tags, asset_id, order_idx, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, out.ID, out.ProjectID, out.Kind, out.Name, out.Description, string(raw), imageID, order, now, now)
	return out, err
}
