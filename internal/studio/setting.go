package studio

import (
	"context"
	"slices"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
)

// SettingItems lists a project's setting items of a kind ("" = all kinds).
func (s *Service) SettingItems(ctx context.Context, projectID, kind string) ([]SettingItem, error) {
	var out []SettingItem
	var err error
	if kind == "" {
		err = s.db.Select(ctx, &out, `SELECT `+settingCols+` FROM setting_items WHERE project_id = ? ORDER BY kind, order_idx, created_at`, projectID)
	} else {
		err = s.db.Select(ctx, &out, `SELECT `+settingCols+` FROM setting_items WHERE project_id = ? AND kind = ? ORDER BY order_idx, created_at`, projectID, kind)
	}
	return out, err
}

// SettingItem returns one setting item of a project.
func (s *Service) SettingItem(ctx context.Context, projectID, id string) (*SettingItem, error) {
	var it SettingItem
	if err := s.db.Get(ctx, &it, `SELECT `+settingCols+` FROM setting_items WHERE id = ? AND project_id = ?`, id, projectID); err != nil {
		return nil, notFound(err)
	}
	return &it, nil
}

// SettingCounts returns how many items of each kind a project has.
func (s *Service) SettingCounts(ctx context.Context, projectID string) (map[string]int, error) {
	rows := []struct {
		Kind string `db:"kind"`
		N    int    `db:"n"`
	}{}
	if err := s.db.Select(ctx, &rows, `SELECT kind, COUNT(*) AS n FROM setting_items WHERE project_id = ? GROUP BY kind`, projectID); err != nil {
		return nil, err
	}
	out := map[string]int{}
	for _, r := range rows {
		out[r.Kind] = r.N
	}
	return out, nil
}

// SettingUsage is how often a setting item appears in the script.
type SettingUsage struct {
	Panels     int
	TimesOfDay []string
}

// SettingUsages counts, per item, the script panels that use it (panels
// referencing it, and for locations every panel of scenes set there) and the
// times of day of those scenes.
func (s *Service) SettingUsages(ctx context.Context, projectID string) (map[string]SettingUsage, error) {
	out := map[string]SettingUsage{}
	var panels []struct {
		SettingIDs db.JSON[[]string] `db:"setting_ids"`
		LocationID string            `db:"location_id"`
		TimeOfDay  string            `db:"time_of_day"`
	}
	if err := s.db.Select(ctx, &panels, `SELECT sp.setting_ids, COALESCE(sc.location_id, '') AS location_id, COALESCE(sc.time_of_day, '') AS time_of_day
		FROM script_panels sp LEFT JOIN scenes sc ON sc.id = sp.scene_id WHERE sp.project_id = ?`, projectID); err != nil {
		return nil, err
	}
	for _, p := range panels {
		seen := map[string]bool{}
		ids := append([]string{}, p.SettingIDs.V...)
		if p.LocationID != "" {
			ids = append(ids, p.LocationID)
		}
		for _, id := range ids {
			if seen[id] {
				continue
			}
			seen[id] = true
			u := out[id]
			u.Panels++
			if p.TimeOfDay != "" && !slices.Contains(u.TimesOfDay, p.TimeOfDay) {
				u.TimesOfDay = append(u.TimesOfDay, p.TimeOfDay)
			}
			out[id] = u
		}
	}
	return out, nil
}

// SettingInput is the editable content of a setting item.
type SettingInput struct {
	Kind           string
	Name           string
	Description    string
	Tags           []string
	AssetID        *string
	TrackTimeOfDay bool
	Notes          string
}

func (s *Service) validSetting(ctx context.Context, in *SettingInput) error {
	errs := map[string]string{}
	if in.Name = clip(in.Name, 120); in.Name == "" {
		errs["name"] = i18n.T(ctx, "studio.form.required")
	}
	if !slices.Contains(SettingKinds, in.Kind) {
		errs["kind"] = i18n.T(ctx, "studio.form.invalid")
	}
	in.Description, in.Notes = clip(in.Description, 2000), clip(in.Notes, 1000)
	if len(errs) > 0 {
		return core.ErrValidation(errs)
	}
	return nil
}

// CreateSettingItem adds a location, prop or style reference.
func (s *Service) CreateSettingItem(ctx context.Context, a *Access, in SettingInput) (*SettingItem, error) {
	if err := s.require(ctx, a, EditSetting); err != nil {
		return nil, err
	}
	if err := s.validSetting(ctx, &in); err != nil {
		return nil, err
	}
	return s.createSettingItem(ctx, s.db, a.Project.ID, in)
}

func (s *Service) createSettingItem(ctx context.Context, q db.Querier, projectID string, in SettingInput) (*SettingItem, error) {
	var next int
	_ = q.Get(ctx, &next, `SELECT COALESCE(MAX(order_idx), -1) + 1 FROM setting_items WHERE project_id = ? AND kind = ?`, projectID, in.Kind)
	now := s.now()
	asset := ""
	if in.AssetID != nil {
		asset = *in.AssetID
	}
	id := db.NewID()
	if _, err := q.Exec(ctx, `INSERT INTO setting_items (id, project_id, kind, name, description, tags, asset_id, refs, data, order_idx, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, '[]', ?, ?, ?, ?)`, id, projectID, in.Kind, in.Name, in.Description, db.MustJSON(jsonList(in.Tags)),
		db.NullString(asset), db.MustJSON(SettingData{TrackTimeOfDay: in.TrackTimeOfDay, Notes: in.Notes}), next, now, now); err != nil {
		return nil, err
	}
	s.touchProject(ctx, q, projectID)
	s.Publish(ctx, projectID, "studio.setting.updated", Event{Kind: "setting", ID: id})
	var it SettingItem
	if err := q.Get(ctx, &it, `SELECT `+settingCols+` FROM setting_items WHERE id = ?`, id); err != nil {
		return nil, err
	}
	return &it, nil
}

// UpdateSettingItem changes a setting item (AssetID nil keeps the image).
func (s *Service) UpdateSettingItem(ctx context.Context, a *Access, id string, in SettingInput) (*SettingItem, error) {
	if err := s.require(ctx, a, EditSetting); err != nil {
		return nil, err
	}
	it, err := s.SettingItem(ctx, a.Project.ID, id)
	if err != nil {
		return nil, err
	}
	in.Kind = it.Kind
	if err := s.validSetting(ctx, &in); err != nil {
		return nil, err
	}
	asset := it.AssetID
	if in.AssetID != nil {
		asset = *in.AssetID
	}
	if _, err := s.db.Exec(ctx, `UPDATE setting_items SET name = ?, description = ?, tags = ?, asset_id = ?, data = ?, updated_at = ? WHERE id = ?`,
		in.Name, in.Description, db.MustJSON(jsonList(in.Tags)), db.NullString(asset),
		db.MustJSON(SettingData{TrackTimeOfDay: in.TrackTimeOfDay, Notes: in.Notes}), s.now(), id); err != nil {
		return nil, err
	}
	s.touchProject(ctx, s.db, a.Project.ID)
	s.Publish(ctx, a.Project.ID, "studio.setting.updated", Event{Kind: "setting", ID: id})
	return s.SettingItem(ctx, a.Project.ID, id)
}

// DeleteSettingItem removes a setting item (scenes set there lose their location).
func (s *Service) DeleteSettingItem(ctx context.Context, a *Access, id string) error {
	if err := s.require(ctx, a, EditSetting); err != nil {
		return err
	}
	res, err := s.db.Exec(ctx, `DELETE FROM setting_items WHERE id = ? AND project_id = ?`, id, a.Project.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return core.ErrNotFound()
	}
	s.Publish(ctx, a.Project.ID, "studio.setting.updated", Event{Kind: "setting", ID: id})
	return nil
}
