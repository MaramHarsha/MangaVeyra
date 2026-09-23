package studio

import (
	"context"
	"slices"
	"strings"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
)

// Categories lists the character categories in tab order.
var Categories = []string{CategoryMain, CategorySupporting, CategoryExtra}

// DriftThreshold is the Character Lock score below which a panel is flagged as drifted.
const DriftThreshold = 0.9

// Characters lists a project's characters ("" category = all).
func (s *Service) Characters(ctx context.Context, projectID, category string) ([]Character, error) {
	var out []Character
	var err error
	if category == "" {
		err = s.db.Select(ctx, &out, `SELECT `+characterCols+` FROM characters WHERE project_id = ?
			ORDER BY CASE category WHEN 'main' THEN 0 WHEN 'supporting' THEN 1 ELSE 2 END, order_idx, created_at`, projectID)
	} else {
		err = s.db.Select(ctx, &out, `SELECT `+characterCols+` FROM characters WHERE project_id = ? AND category = ? ORDER BY order_idx, created_at`,
			projectID, category)
	}
	return out, err
}

// Character returns one character of a project.
func (s *Service) Character(ctx context.Context, projectID, id string) (*Character, error) {
	var c Character
	if err := s.db.Get(ctx, &c, `SELECT `+characterCols+` FROM characters WHERE id = ? AND project_id = ?`, id, projectID); err != nil {
		return nil, notFound(err)
	}
	return &c, nil
}

// CharacterByID returns a character by id (any project).
func (s *Service) CharacterByID(ctx context.Context, id string) (*Character, error) {
	var c Character
	if err := s.db.Get(ctx, &c, `SELECT `+characterCols+` FROM characters WHERE id = ?`, id); err != nil {
		return nil, notFound(err)
	}
	return &c, nil
}

// CharacterCounts returns the character count per category.
func (s *Service) CharacterCounts(ctx context.Context, projectID string) (map[string]int, error) {
	rows := []struct {
		Category string `db:"category"`
		N        int    `db:"n"`
	}{}
	if err := s.db.Select(ctx, &rows, `SELECT category, COUNT(*) AS n FROM characters WHERE project_id = ? GROUP BY category`, projectID); err != nil {
		return nil, err
	}
	out := map[string]int{}
	for _, r := range rows {
		out[r.Category] = r.N
		out[""] += r.N
	}
	return out, nil
}

// CharacterInput is the editable profile of a character.
type CharacterInput struct {
	Name        string
	Kanji       string
	Role        string // short role ("lead", "rival") shown in the role line
	RoleLine    string // "Lead · Courier"
	Age         string
	Look        string
	Personality string
	SpeechStyle string
	Backstory   string
	Voice       Voice
	Category    string
	Style       string
}

func (s *Service) validCharacter(ctx context.Context, in *CharacterInput) error {
	errs := map[string]string{}
	if in.Name = clip(in.Name, 80); in.Name == "" {
		errs["name"] = i18n.T(ctx, "studio.form.required")
	}
	if in.Category == "" {
		in.Category = CategorySupporting
	}
	if !slices.Contains(Categories, in.Category) {
		errs["category"] = i18n.T(ctx, "studio.form.invalid")
	}
	in.Kanji = clip(in.Kanji, 2)
	in.Role, in.RoleLine, in.Age = clip(in.Role, 40), clip(in.RoleLine, 80), clip(in.Age, 20)
	in.Look, in.Personality, in.SpeechStyle, in.Backstory = clip(in.Look, 1500), clip(in.Personality, 1500), clip(in.SpeechStyle, 400), clip(in.Backstory, 4000)
	in.Voice.Description, in.Voice.Preset = clip(in.Voice.Description, 200), clip(in.Voice.Preset, 60)
	in.Style = clip(in.Style, 40)
	if len(errs) > 0 {
		return core.ErrValidation(errs)
	}
	return nil
}

// CreateCharacter adds a character to the bible.
func (s *Service) CreateCharacter(ctx context.Context, a *Access, in CharacterInput) (*Character, error) {
	if err := s.require(ctx, a, EditCharacters); err != nil {
		return nil, err
	}
	if err := s.validCharacter(ctx, &in); err != nil {
		return nil, err
	}
	return s.createCharacter(ctx, s.db, a.Project.ID, in)
}

func (s *Service) createCharacter(ctx context.Context, q db.Querier, projectID string, in CharacterInput) (*Character, error) {
	var next int
	_ = q.Get(ctx, &next, `SELECT COALESCE(MAX(order_idx), -1) + 1 FROM characters WHERE project_id = ?`, projectID)
	now := s.now()
	id := db.NewID()
	if in.Kanji == "" {
		in.Kanji = KanjiFor(in.Name)
	}
	if _, err := q.Exec(ctx, `INSERT INTO characters (id, project_id, name, kanji, role, role_line, age, look, personality, speech_style,
		backstory, voice, category, lock_status, order_idx, style, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'none', ?, ?, ?, ?)`, id, projectID, in.Name, in.Kanji, in.Role, in.RoleLine, in.Age,
		in.Look, in.Personality, in.SpeechStyle, in.Backstory, db.MustJSON(in.Voice), in.Category, next, in.Style, now, now); err != nil {
		return nil, err
	}
	s.touchProject(ctx, q, projectID)
	s.Publish(ctx, projectID, "studio.character.updated", Event{Kind: "character", ID: id})
	var c Character
	if err := q.Get(ctx, &c, `SELECT `+characterCols+` FROM characters WHERE id = ?`, id); err != nil {
		return nil, err
	}
	return &c, nil
}

// UpdateCharacter replaces a character's profile.
func (s *Service) UpdateCharacter(ctx context.Context, a *Access, id string, in CharacterInput) (*Character, error) {
	if err := s.require(ctx, a, EditCharacters); err != nil {
		return nil, err
	}
	if _, err := s.Character(ctx, a.Project.ID, id); err != nil {
		return nil, err
	}
	if err := s.validCharacter(ctx, &in); err != nil {
		return nil, err
	}
	if in.Kanji == "" {
		in.Kanji = KanjiFor(in.Name)
	}
	if _, err := s.db.Exec(ctx, `UPDATE characters SET name = ?, kanji = ?, role = ?, role_line = ?, age = ?, look = ?, personality = ?,
		speech_style = ?, backstory = ?, voice = ?, category = ?, style = ?, updated_at = ? WHERE id = ?`, in.Name, in.Kanji, in.Role, in.RoleLine,
		in.Age, in.Look, in.Personality, in.SpeechStyle, in.Backstory, db.MustJSON(in.Voice), in.Category, in.Style, s.now(), id); err != nil {
		return nil, err
	}
	s.touchProject(ctx, s.db, a.Project.ID)
	s.Publish(ctx, a.Project.ID, "studio.character.updated", Event{Kind: "character", ID: id})
	return s.Character(ctx, a.Project.ID, id)
}

// DeleteCharacter removes a character (layers keep their art, unlinked).
func (s *Service) DeleteCharacter(ctx context.Context, a *Access, id string) error {
	if err := s.require(ctx, a, EditCharacters); err != nil {
		return err
	}
	res, err := s.db.Exec(ctx, `DELETE FROM characters WHERE id = ? AND project_id = ?`, id, a.Project.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return core.ErrNotFound()
	}
	s.Publish(ctx, a.Project.ID, "studio.character.updated", Event{Kind: "character", ID: id})
	return nil
}

// SetPortrait sets the main reference image of a character.
func (s *Service) SetPortrait(ctx context.Context, a *Access, id, assetID string) error {
	if err := s.require(ctx, a, EditCharacters); err != nil {
		return err
	}
	_, err := s.db.Exec(ctx, `UPDATE characters SET portrait_asset_id = ?, updated_at = ? WHERE id = ? AND project_id = ?`,
		db.NullString(assetID), s.now(), id, a.Project.ID)
	return err
}

// ---- references -------------------------------------------------------------------

// CharacterRefs lists a character's reference images.
func (s *Service) CharacterRefs(ctx context.Context, characterID string) ([]CharacterRef, error) {
	var out []CharacterRef
	err := s.db.Select(ctx, &out, `SELECT r.id, r.character_id, r.asset_id, r.kind, r.label, r.order_idx, r.created_at, a.key
		FROM character_refs r JOIN assets a ON a.id = r.asset_id WHERE r.character_id = ? ORDER BY r.order_idx, r.created_at`, characterID)
	return out, err
}

// AddCharacterRef attaches a stored image to a character as a reference of kind.
func (s *Service) AddCharacterRef(ctx context.Context, a *Access, characterID, assetID, kind, label string) (*CharacterRef, error) {
	if err := s.require(ctx, a, EditCharacters); err != nil {
		return nil, err
	}
	c, err := s.Character(ctx, a.Project.ID, characterID)
	if err != nil {
		return nil, err
	}
	return s.addRef(ctx, c, assetID, kind, label)
}

func (s *Service) addRef(ctx context.Context, c *Character, assetID, kind, label string) (*CharacterRef, error) {
	if !slices.Contains(RefKinds, kind) {
		kind = RefOther
	}
	var next int
	_ = s.db.Get(ctx, &next, `SELECT COALESCE(MAX(order_idx), -1) + 1 FROM character_refs WHERE character_id = ?`, c.ID)
	r := &CharacterRef{ID: db.NewID(), CharacterID: c.ID, AssetID: assetID, Kind: kind, Label: clip(label, 60), OrderIdx: next, CreatedAt: s.now()}
	if _, err := s.db.Exec(ctx, `INSERT INTO character_refs (id, character_id, asset_id, kind, label, order_idx, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.CharacterID, r.AssetID, r.Kind, r.Label, r.OrderIdx, r.CreatedAt); err != nil {
		return nil, err
	}
	if c.PortraitAssetID == "" {
		_, _ = s.db.Exec(ctx, `UPDATE characters SET portrait_asset_id = ? WHERE id = ?`, assetID, c.ID)
		c.PortraitAssetID = assetID
	}
	s.Publish(ctx, c.ProjectID, "studio.character.updated", Event{Kind: "character_ref", ID: c.ID})
	return r, nil
}

// UpdateCharacterRef changes a reference's kind and label.
func (s *Service) UpdateCharacterRef(ctx context.Context, a *Access, characterID, refID, kind, label string) error {
	if err := s.require(ctx, a, EditCharacters); err != nil {
		return err
	}
	if _, err := s.Character(ctx, a.Project.ID, characterID); err != nil {
		return err
	}
	if !slices.Contains(RefKinds, kind) {
		return core.ErrValidation(map[string]string{"kind": i18n.T(ctx, "studio.form.invalid")})
	}
	_, err := s.db.Exec(ctx, `UPDATE character_refs SET kind = ?, label = ? WHERE id = ? AND character_id = ?`, kind, clip(label, 60), refID, characterID)
	return err
}

// DeleteCharacterRef removes a reference image from a character.
func (s *Service) DeleteCharacterRef(ctx context.Context, a *Access, characterID, refID string) error {
	if err := s.require(ctx, a, EditCharacters); err != nil {
		return err
	}
	c, err := s.Character(ctx, a.Project.ID, characterID)
	if err != nil {
		return err
	}
	var assetID string
	if err := s.db.Get(ctx, &assetID, `SELECT asset_id FROM character_refs WHERE id = ? AND character_id = ?`, refID, characterID); err != nil {
		return notFound(err)
	}
	if _, err := s.db.Exec(ctx, `DELETE FROM character_refs WHERE id = ?`, refID); err != nil {
		return err
	}
	if c.PortraitAssetID == assetID {
		_, _ = s.db.Exec(ctx, `UPDATE characters SET portrait_asset_id = (SELECT asset_id FROM character_refs WHERE character_id = ?
			ORDER BY order_idx LIMIT 1) WHERE id = ?`, characterID, characterID)
	}
	s.Publish(ctx, a.Project.ID, "studio.character.updated", Event{Kind: "character_ref", ID: characterID})
	return nil
}

// ---- outfits ------------------------------------------------------------------------

// Outfits lists a character's outfits.
func (s *Service) Outfits(ctx context.Context, characterID string) ([]Outfit, error) {
	var out []Outfit
	err := s.db.Select(ctx, &out, `SELECT id, character_id, name, description, COALESCE(asset_id, '') AS asset_id, is_default, order_idx, chapters,
		created_at FROM character_outfits WHERE character_id = ? ORDER BY order_idx, created_at`, characterID)
	return out, err
}

// SaveOutfit creates (id "") or updates an outfit. A default outfit clears the others' default flag.
func (s *Service) SaveOutfit(ctx context.Context, a *Access, characterID, id string, o Outfit) (*Outfit, error) {
	if err := s.require(ctx, a, EditCharacters); err != nil {
		return nil, err
	}
	if _, err := s.Character(ctx, a.Project.ID, characterID); err != nil {
		return nil, err
	}
	if o.Name = clip(o.Name, 80); o.Name == "" {
		return nil, core.ErrValidation(map[string]string{"name": i18n.T(ctx, "studio.form.required")})
	}
	o.Description, o.Chapters = clip(o.Description, 600), clip(o.Chapters, 40)
	err := s.db.WithTx(ctx, func(tx *db.Tx) error {
		if o.IsDefault {
			if _, err := tx.Exec(ctx, `UPDATE character_outfits SET is_default = ? WHERE character_id = ?`, false, characterID); err != nil {
				return err
			}
		}
		if id == "" {
			var next int
			_ = tx.Get(ctx, &next, `SELECT COALESCE(MAX(order_idx), -1) + 1 FROM character_outfits WHERE character_id = ?`, characterID)
			o.ID, o.OrderIdx, o.CreatedAt = db.NewID(), next, s.now()
			_, err := tx.Exec(ctx, `INSERT INTO character_outfits (id, character_id, name, description, asset_id, is_default, order_idx, chapters, created_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, o.ID, characterID, o.Name, o.Description, db.NullString(o.AssetID), o.IsDefault, o.OrderIdx,
				o.Chapters, o.CreatedAt)
			return err
		}
		res, err := tx.Exec(ctx, `UPDATE character_outfits SET name = ?, description = ?, asset_id = COALESCE(?, asset_id), is_default = ?, chapters = ?
			WHERE id = ? AND character_id = ?`, o.Name, o.Description, db.NullString(o.AssetID), o.IsDefault, o.Chapters, id, characterID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return core.ErrNotFound()
		}
		o.ID = id
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.Publish(ctx, a.Project.ID, "studio.character.updated", Event{Kind: "outfit", ID: characterID})
	o.CharacterID = characterID
	return &o, nil
}

// DeleteOutfit removes an outfit.
func (s *Service) DeleteOutfit(ctx context.Context, a *Access, characterID, id string) error {
	if err := s.require(ctx, a, EditCharacters); err != nil {
		return err
	}
	if _, err := s.Character(ctx, a.Project.ID, characterID); err != nil {
		return err
	}
	_, err := s.db.Exec(ctx, `DELETE FROM character_outfits WHERE id = ? AND character_id = ?`, id, characterID)
	return err
}

// ---- relations ------------------------------------------------------------------------

// Relations lists a character's relationships with the other character's name.
func (s *Service) Relations(ctx context.Context, characterID string) ([]Relation, error) {
	var out []Relation
	err := s.db.Select(ctx, &out, `SELECT r.id, r.character_id, r.other_id, r.kind, r.note, r.created_at, c.name AS other_name
		FROM character_relations r JOIN characters c ON c.id = r.other_id WHERE r.character_id = ? ORDER BY r.created_at`, characterID)
	return out, err
}

// AddRelation links two characters of the same project.
func (s *Service) AddRelation(ctx context.Context, a *Access, characterID, otherID, kind, note string) error {
	if err := s.require(ctx, a, EditCharacters); err != nil {
		return err
	}
	if _, err := s.Character(ctx, a.Project.ID, characterID); err != nil {
		return err
	}
	if _, err := s.Character(ctx, a.Project.ID, otherID); err != nil || otherID == characterID {
		return core.ErrValidation(map[string]string{"other_id": i18n.T(ctx, "studio.form.invalid")})
	}
	if kind = clip(kind, 60); kind == "" {
		return core.ErrValidation(map[string]string{"kind": i18n.T(ctx, "studio.form.required")})
	}
	_, err := s.db.Exec(ctx, `INSERT INTO character_relations (id, character_id, other_id, kind, note, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		db.NewID(), characterID, otherID, kind, clip(note, 200), s.now())
	if db.IsUniqueViolation(err) {
		return core.ErrValidation(map[string]string{"kind": i18n.T(ctx, "studio.character.relation_exists")})
	}
	if err == nil {
		s.Publish(ctx, a.Project.ID, "studio.character.updated", Event{Kind: "relation", ID: characterID})
	}
	return err
}

// DeleteRelation removes a relationship.
func (s *Service) DeleteRelation(ctx context.Context, a *Access, characterID, id string) error {
	if err := s.require(ctx, a, EditCharacters); err != nil {
		return err
	}
	if _, err := s.Character(ctx, a.Project.ID, characterID); err != nil {
		return err
	}
	_, err := s.db.Exec(ctx, `DELETE FROM character_relations WHERE id = ? AND character_id = ?`, id, characterID)
	return err
}

// ---- Character Lock statistics ---------------------------------------------------------

// DriftedPanel is a panel whose character layer scored below DriftThreshold.
type DriftedPanel struct {
	PanelID string  `db:"panel_id" json:"panel_id"`
	PageID  string  `db:"page_id" json:"page_id"`
	Page    int     `db:"page_number" json:"page"`
	Chapter int     `db:"chapter_number" json:"chapter"`
	Score   float64 `db:"lock_score" json:"score"`
}

// ChapterScore is the average Character Lock score of one chapter.
type ChapterScore struct {
	Chapter int     `db:"chapter_number" json:"chapter"`
	Score   float64 `db:"score" json:"score"`
	Panels  int     `db:"panels" json:"panels"`
}

// CharacterStats is what C3 shows about a character's use and consistency.
type CharacterStats struct {
	Chapters     int            `json:"chapters"`
	Panels       int            `json:"panels"`
	ScoredPanels int            `json:"scored_panels"`
	AverageScore float64        `json:"average_score"`
	ByChapter    []ChapterScore `json:"by_chapter"`
	Drifted      []DriftedPanel `json:"drifted"`
}

// CharacterStats computes appearances (drawn layers and script mentions) and Character Lock scores.
func (s *Service) CharacterStats(ctx context.Context, c *Character) (CharacterStats, error) {
	var st CharacterStats
	like := `%"` + c.ID + `"%`
	var chapters []string
	if err := s.db.Select(ctx, &chapters, `SELECT DISTINCT pg.chapter_id FROM layers l JOIN panels pn ON pn.id = l.panel_id
		JOIN pages pg ON pg.id = pn.page_id WHERE l.character_id = ?
		UNION SELECT DISTINCT chapter_id FROM script_panels WHERE project_id = ? AND character_ids LIKE ?`, c.ID, c.ProjectID, like); err != nil {
		return st, err
	}
	st.Chapters = len(chapters)
	var drawn, scripted int
	if err := s.db.Get(ctx, &drawn, `SELECT COUNT(DISTINCT l.panel_id) FROM layers l WHERE l.character_id = ?`, c.ID); err != nil {
		return st, err
	}
	if err := s.db.Get(ctx, &scripted, `SELECT COUNT(*) FROM script_panels sp WHERE sp.project_id = ? AND sp.character_ids LIKE ?
		AND NOT EXISTS (SELECT 1 FROM panels pn JOIN layers l ON l.panel_id = pn.id WHERE pn.script_panel_id = sp.id AND l.character_id = ?)`,
		c.ProjectID, like, c.ID); err != nil {
		return st, err
	}
	st.Panels = drawn + scripted
	var agg struct {
		N   int     `db:"n"`
		Avg float64 `db:"avg"`
	}
	if err := s.db.Get(ctx, &agg, `SELECT COUNT(*) AS n, COALESCE(AVG(lock_score), 0) AS avg FROM layers WHERE character_id = ? AND lock_score IS NOT NULL`, c.ID); err != nil {
		return st, err
	}
	st.ScoredPanels, st.AverageScore = agg.N, agg.Avg
	if err := s.db.Select(ctx, &st.ByChapter, `SELECT ch.number AS chapter_number, AVG(l.lock_score) AS score, COUNT(*) AS panels
		FROM layers l JOIN panels pn ON pn.id = l.panel_id JOIN pages pg ON pg.id = pn.page_id JOIN chapters ch ON ch.id = pg.chapter_id
		WHERE l.character_id = ? AND l.lock_score IS NOT NULL GROUP BY ch.number ORDER BY ch.number`, c.ID); err != nil {
		return st, err
	}
	if len(st.ByChapter) > 12 {
		st.ByChapter = st.ByChapter[len(st.ByChapter)-12:]
	}
	err := s.db.Select(ctx, &st.Drifted, `SELECT pn.id AS panel_id, pg.id AS page_id, pg.number AS page_number, ch.number AS chapter_number, l.lock_score
		FROM layers l JOIN panels pn ON pn.id = l.panel_id JOIN pages pg ON pg.id = pn.page_id JOIN chapters ch ON ch.id = pg.chapter_id
		WHERE l.character_id = ? AND l.lock_score IS NOT NULL AND l.lock_score < ? ORDER BY ch.number DESC, pg.number, pn.order_idx`, c.ID, DriftThreshold)
	return st, err
}

// kanjiHints give original watermark glyphs for common name sounds; anything
// else falls back to a glyph picked from the name.
var kanjiHints = map[string]string{"rin": "凛", "kai": "海", "ren": "蓮", "sora": "空", "mio": "澪", "yu": "夕", "hana": "花",
	"aki": "秋", "haru": "春", "natsu": "夏", "fuyu": "冬", "tsuki": "月", "hoshi": "星", "kaze": "風", "yuki": "雪", "wren": "鷦", "pip": "笛"}

var kanjiPool = []rune("灯波港潮翼刃影炎霧羽鈴光航紙鉄鐘雷砂塩")

// KanjiFor suggests a watermark kanji for a name.
func KanjiFor(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	for _, part := range strings.Fields(n) {
		for _, k := range []string{part, strings.TrimSuffix(part, "o"), firstN(part, 3), firstN(part, 4)} {
			if g, ok := kanjiHints[k]; ok {
				return g
			}
		}
	}
	var h uint32
	for _, r := range n {
		h = h*31 + uint32(r)
	}
	return string(kanjiPool[int(h%uint32(len(kanjiPool)))])
}

func firstN(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

// SplitName splits a name over two lines for the profile cards ("RIN" / "ASAGIRI").
func SplitName(name string) (string, string) {
	f := strings.Fields(name)
	switch len(f) {
	case 0:
		return "", ""
	case 1:
		return f[0], ""
	}
	return strings.Join(f[:len(f)-1], " "), f[len(f)-1]
}
