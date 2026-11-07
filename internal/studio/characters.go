package studio

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/assets"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
)

const characterCols = `c.id, c.project_id, c.name, c.kanji, c.role, c.role_line, c.age, c.look, c.personality,
	c.speech_style, c.category, c.lock_status, c.lock_score, c.portrait_asset_id, c.lora_asset_id, c.trigger_word, c.order_idx, c.updated_at`

type characterRow struct {
	ID              string          `db:"id"`
	ProjectID       string          `db:"project_id"`
	Name            string          `db:"name"`
	Kanji           string          `db:"kanji"`
	Role            string          `db:"role"`
	RoleLine        string          `db:"role_line"`
	Age             string          `db:"age"`
	Look            string          `db:"look"`
	Personality     string          `db:"personality"`
	SpeechStyle     string          `db:"speech_style"`
	Category        string          `db:"category"`
	LockStatus      string          `db:"lock_status"`
	LockScore       sql.NullFloat64 `db:"lock_score"`
	PortraitAssetID sql.NullString  `db:"portrait_asset_id"`
	LoraAssetID     sql.NullString  `db:"lora_asset_id"`
	TriggerWord     string          `db:"trigger_word"`
	Order           int             `db:"order_idx"`
	UpdatedAt       time.Time       `db:"updated_at"`
}

func (r characterRow) character() Character {
	c := Character{ID: r.ID, ProjectID: r.ProjectID, Name: r.Name, Kanji: r.Kanji, Role: r.Role, RoleLine: r.RoleLine,
		Age: r.Age, Look: r.Look, Personality: r.Personality, SpeechStyle: r.SpeechStyle, Category: r.Category,
		LockStatus: r.LockStatus, PortraitAssetID: r.PortraitAssetID.String, LoraAssetID: r.LoraAssetID.String,
		TriggerWord: r.TriggerWord, Refs: []CharacterRef{}, Outfits: []Outfit{}, Order: r.Order, UpdatedAt: r.UpdatedAt.UTC()}
	if r.LockScore.Valid {
		v := r.LockScore.Float64
		c.LockScore = &v
	}
	return c
}

func (s *service) CreateCharacter(ctx context.Context, actor Actor, in NewCharacter) (Character, error) {
	in = normalizeNewCharacter(in)
	if err := validateNewCharacter(in); err != nil {
		return Character{}, err
	}
	id, now := db.NewID(), db.Now()
	var out Character
	err := s.db.WithTx(ctx, func(tx *db.Tx) error {
		if err := s.authorize(ctx, tx, actor, in.ProjectID, ActEditScript); err != nil {
			return err
		}
		var lockedID string
		if err := tx.Get(ctx, &lockedID, `SELECT id FROM projects WHERE id = ?`+forUpdate(tx), in.ProjectID); err != nil {
			if db.IsNotFound(err) {
				return ErrNotFound
			}
			return fmt.Errorf("studio: lock project for character: %w", err)
		}
		var order int
		if err := tx.Get(ctx, &order, `SELECT COALESCE(MAX(order_idx) + 1, 0) FROM characters WHERE project_id = ?`, in.ProjectID); err != nil {
			return fmt.Errorf("studio: allocate character order: %w", err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO characters
			(id, project_id, name, kanji, role, role_line, age, look, personality, speech_style, category, order_idx, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, in.ProjectID, in.Name, in.Kanji, in.Role, in.RoleLine,
			in.Age, in.Look, in.Personality, in.SpeechStyle, in.Category, order, now, now); err != nil {
			return fmt.Errorf("studio: insert character: %w", err)
		}
		var err error
		out, err = getCharacter(ctx, tx, id)
		return err
	})
	return out, err
}

func (s *service) GetCharacter(ctx context.Context, id string) (Character, error) {
	return getCharacter(ctx, s.db, strings.TrimSpace(id))
}

func getCharacter(ctx context.Context, q db.Querier, id string) (Character, error) {
	var row characterRow
	if err := q.Get(ctx, &row, `SELECT `+characterCols+` FROM characters c WHERE c.id = ?`, id); err != nil {
		if db.IsNotFound(err) {
			return Character{}, ErrNotFound
		}
		return Character{}, fmt.Errorf("studio: get character: %w", err)
	}
	out := row.character()
	if err := loadCharacterChildren(ctx, q, &out); err != nil {
		return Character{}, err
	}
	return out, nil
}

func (s *service) ListCharacters(ctx context.Context, projectID string) ([]Character, error) {
	var rows []characterRow
	if err := s.db.Select(ctx, &rows, `SELECT `+characterCols+` FROM characters c WHERE c.project_id = ? ORDER BY c.order_idx, c.id`,
		strings.TrimSpace(projectID)); err != nil {
		return nil, fmt.Errorf("studio: list characters: %w", err)
	}
	out := make([]Character, 0, len(rows))
	for _, row := range rows {
		c := row.character()
		if err := loadCharacterChildren(ctx, s.db, &c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

func loadCharacterChildren(ctx context.Context, q db.Querier, c *Character) error {
	if err := q.Select(ctx, &c.Refs, `SELECT id, asset_id AS "assetid", kind, label, order_idx AS "order", COALESCE(outfit_id,'') AS "outfitid" FROM character_refs
		WHERE character_id = ? ORDER BY order_idx, id`, c.ID); err != nil {
		return fmt.Errorf("studio: list character references: %w", err)
	}
	if err := q.Select(ctx, &c.Outfits, `SELECT id, name, description, COALESCE(asset_id, '') AS "assetid",
		is_default AS "isdefault" FROM character_outfits WHERE character_id = ? ORDER BY order_idx, id`, c.ID); err != nil {
		return fmt.Errorf("studio: list character outfits: %w", err)
	}
	if c.Refs == nil {
		c.Refs = []CharacterRef{}
	}
	if c.Outfits == nil {
		c.Outfits = []Outfit{}
	}
	return nil
}

func (s *service) AddCharacterRefTx(ctx context.Context, tx *db.Tx, characterID, assetID, kind, label string) (CharacterRef, error) {
	if tx == nil {
		return CharacterRef{}, ErrInvalid
	}
	characterID, assetID, kind, label = strings.TrimSpace(characterID), strings.TrimSpace(assetID), strings.TrimSpace(kind), strings.TrimSpace(label)
	if characterID == "" || assetID == "" {
		return CharacterRef{}, ErrInvalid
	}
	switch kind {
	case "front", "side", "back", "face", "expression", "outfit", "sheet", "other":
	default:
		return CharacterRef{}, fmt.Errorf("%w: invalid character reference kind", ErrInvalid)
	}
	if err := validateText("reference label", label, maxShortText, false); err != nil {
		return CharacterRef{}, err
	}
	var projectID string
	if err := tx.Get(ctx, &projectID, `SELECT project_id FROM characters WHERE id = ?`+forUpdate(tx), characterID); err != nil {
		if db.IsNotFound(err) {
			return CharacterRef{}, ErrNotFound
		}
		return CharacterRef{}, fmt.Errorf("studio: find character for reference: %w", err)
	}
	asset, err := s.assets.GetTx(ctx, tx, assetID)
	if err != nil {
		if errors.Is(err, assets.ErrNotFound) {
			return CharacterRef{}, ErrNotFound
		}
		return CharacterRef{}, fmt.Errorf("studio: get character reference asset: %w", err)
	}
	if asset.ProjectID == nil || *asset.ProjectID != projectID {
		return CharacterRef{}, ErrForbidden
	}
	if err := imageAssetMetadata(asset); err != nil {
		return CharacterRef{}, err
	}
	var order int
	if err := tx.Get(ctx, &order, `SELECT COALESCE(MAX(order_idx) + 1, 0) FROM character_refs WHERE character_id = ?`, characterID); err != nil {
		return CharacterRef{}, fmt.Errorf("studio: allocate reference order: %w", err)
	}
	ref := CharacterRef{ID: db.NewID(), AssetID: assetID, Kind: kind, Label: label, Order: order}
	var outfitID any
	if kind == "outfit" {
		var found string
		err := tx.Get(ctx, &found, `SELECT id FROM character_outfits WHERE character_id=? AND is_default=TRUE ORDER BY order_idx,id LIMIT 1`, characterID)
		if err != nil && !db.IsNotFound(err) {
			return CharacterRef{}, fmt.Errorf("studio: bind outfit reference: %w", err)
		}
		if err == nil {
			ref.OutfitID, outfitID = found, found
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO character_refs (id, character_id, asset_id, kind, label, order_idx, created_at,outfit_id)
		VALUES (?, ?, ?, ?, ?, ?, ?,?)`, ref.ID, characterID, ref.AssetID, ref.Kind, ref.Label, ref.Order, db.Now(), outfitID); err != nil {
		return CharacterRef{}, fmt.Errorf("studio: insert character reference: %w", err)
	}
	if err := touchCharacter(ctx, tx, characterID); err != nil {
		return CharacterRef{}, err
	}
	return ref, nil
}

func (s *service) SetCharacterPortraitTx(ctx context.Context, tx *db.Tx, characterID, assetID string) error {
	if tx == nil {
		return ErrInvalid
	}
	characterID, assetID = strings.TrimSpace(characterID), strings.TrimSpace(assetID)
	if characterID == "" || assetID == "" {
		return ErrInvalid
	}
	var projectID string
	if err := tx.Get(ctx, &projectID, `SELECT project_id FROM characters WHERE id = ?`+forUpdate(tx), characterID); err != nil {
		if db.IsNotFound(err) {
			return ErrNotFound
		}
		return fmt.Errorf("studio: find character for portrait: %w", err)
	}
	asset, err := s.assets.GetTx(ctx, tx, assetID)
	if err != nil {
		if errors.Is(err, assets.ErrNotFound) {
			return ErrNotFound
		}
		return fmt.Errorf("studio: get character portrait asset: %w", err)
	}
	if asset.ProjectID == nil || *asset.ProjectID != projectID {
		return ErrForbidden
	}
	if err := imageAssetMetadata(asset); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE characters SET portrait_asset_id = ?, updated_at = ? WHERE id = ?`, assetID, db.Now(), characterID); err != nil {
		return fmt.Errorf("studio: update character portrait: %w", err)
	}
	return nil
}

func (s *service) ListSettingItems(ctx context.Context, projectID, kind string) ([]SettingItem, error) {
	projectID, kind = strings.TrimSpace(projectID), strings.TrimSpace(kind)
	if kind != "" && kind != "location" && kind != "prop" && kind != "style" {
		return nil, ErrInvalid
	}
	type row struct {
		ID          string         `db:"id"`
		ProjectID   string         `db:"project_id"`
		Kind        string         `db:"kind"`
		Name        string         `db:"name"`
		Description string         `db:"description"`
		Tags        string         `db:"tags"`
		AssetID     sql.NullString `db:"asset_id"`
	}
	query := `SELECT id, project_id, kind, name, description, tags, asset_id FROM setting_items WHERE project_id = ?`
	args := []any{projectID}
	if kind != "" {
		query += ` AND kind = ?`
		args = append(args, kind)
	}
	query += ` ORDER BY kind, order_idx, id`
	var rows []row
	if err := s.db.Select(ctx, &rows, query, args...); err != nil {
		return nil, fmt.Errorf("studio: list setting items: %w", err)
	}
	out := make([]SettingItem, 0, len(rows))
	for _, row := range rows {
		var tags []string
		if err := json.Unmarshal([]byte(row.Tags), &tags); err != nil {
			return nil, fmt.Errorf("studio: decode setting tags: %w", err)
		}
		if tags == nil {
			tags = []string{}
		}
		out = append(out, SettingItem{ID: row.ID, ProjectID: row.ProjectID, Kind: row.Kind, Name: row.Name,
			Description: row.Description, Tags: tags, AssetID: row.AssetID.String})
	}
	return out, nil
}

func normalizeNewCharacter(in NewCharacter) NewCharacter {
	in.ProjectID = strings.TrimSpace(in.ProjectID)
	in.Name = strings.TrimSpace(in.Name)
	in.Kanji = strings.TrimSpace(in.Kanji)
	in.Role = strings.TrimSpace(in.Role)
	in.RoleLine = strings.TrimSpace(in.RoleLine)
	in.Age = strings.TrimSpace(in.Age)
	in.Look = strings.TrimSpace(in.Look)
	in.Personality = strings.TrimSpace(in.Personality)
	in.SpeechStyle = strings.TrimSpace(in.SpeechStyle)
	in.Category = strings.TrimSpace(in.Category)
	if in.Category == "" {
		in.Category = "supporting"
	}
	return in
}

func validateNewCharacter(in NewCharacter) error {
	if in.ProjectID == "" {
		return ErrInvalid
	}
	for _, check := range []struct {
		name, value string
		max         int
		required    bool
	}{
		{"character name", in.Name, maxShortText, true}, {"kanji", in.Kanji, maxShortText, false},
		{"role", in.Role, maxShortText, false}, {"role line", in.RoleLine, 1000, false}, {"age", in.Age, 80, false},
		{"look", in.Look, 4000, false}, {"personality", in.Personality, 4000, false}, {"speech style", in.SpeechStyle, 2000, false},
	} {
		if err := validateText(check.name, check.value, check.max, check.required); err != nil {
			return err
		}
	}
	switch in.Category {
	case "main", "supporting", "extra":
		return nil
	default:
		return fmt.Errorf("%w: invalid character category", ErrInvalid)
	}
}
