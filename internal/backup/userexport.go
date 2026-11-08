package backup

import (
	"context"
	"strings"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/storage"
)

// exportScope selects one member's own data for a user_export (ADR 0013 §4):
// their account (without credentials), their projects with everything in them,
// their characters, published works, comments and reading data. Other
// members' rows, sessions, tokens, provider keys, jobs and server settings are
// never included.
type exportScope struct {
	user   string
	tables map[string]scopeRule
}

type scopeRule struct {
	where string
	args  int // how many times the user id is bound
	// redact replaces column values (credentials stay out of the export).
	redact map[string]any
}

// Subqueries naming the member's projects, series, chapters, pages and panels.
const (
	ownProjects  = `SELECT id FROM projects WHERE owner_id = ?`
	ownSeries    = `SELECT id FROM series WHERE owner_id = ? OR project_id IN (SELECT id FROM projects WHERE owner_id = ?)`
	ownPages     = `SELECT id FROM pages WHERE project_id IN (SELECT id FROM projects WHERE owner_id = ?)`
	ownPanels    = `SELECT id FROM panels WHERE project_id IN (SELECT id FROM projects WHERE owner_id = ?)`
	ownChars     = `SELECT id FROM characters WHERE project_id IN (SELECT id FROM projects WHERE owner_id = ?)`
	ownBubbles   = `SELECT id FROM bubbles WHERE page_id IN (SELECT id FROM pages WHERE project_id IN (SELECT id FROM projects WHERE owner_id = ?))`
	ownPubs      = `SELECT id FROM chapter_publications WHERE series_id IN (SELECT id FROM series WHERE owner_id = ? OR project_id IN (SELECT id FROM projects WHERE owner_id = ?))`
	ownAssetsSQL = `owner_id = ? OR project_id IN (SELECT id FROM projects WHERE owner_id = ?)`
)

func userScope(userID string) *exportScope {
	inProject := scopeRule{where: `project_id IN (` + ownProjects + `)`, args: 1}
	byUser := scopeRule{where: `user_id = ?`, args: 1}
	return &exportScope{user: userID, tables: map[string]scopeRule{
		"users": {where: `id = ?`, args: 1, redact: map[string]any{"password_hash": "", "totp_secret_enc": "",
			"totp_recovery": "[]", "totp_last_step": int64(0), "invite_id": nil}},
		"user_prefs":            byUser,
		"notification_prefs":    byUser,
		"projects":              {where: `owner_id = ?`, args: 1},
		"project_members":       byUser,
		"arcs":                  inProject,
		"chapters":              inProject,
		"characters":            inProject,
		"character_refs":        {where: `character_id IN (` + ownChars + `)`, args: 1},
		"character_outfits":     {where: `character_id IN (` + ownChars + `)`, args: 1},
		"character_relations":   {where: `character_id IN (` + ownChars + `)`, args: 1},
		"character_assessments": inProject,
		"setting_items":         inProject,
		"scenes":                inProject,
		"script_panels":         inProject,
		"pages":                 inProject,
		"panels":                inProject,
		"layers":                {where: `panel_id IN (` + ownPanels + `)`, args: 1},
		"bubbles":               {where: `page_id IN (` + ownPages + `)`, args: 1},
		"bubble_texts":          {where: `bubble_id IN (` + ownBubbles + `)`, args: 1},
		"branches":              {where: `page_id IN (` + ownPages + `)`, args: 1},
		"versions":              {where: `page_id IN (` + ownPages + `)`, args: 1},
		"continuity_issues":     inProject,
		"glossaries":            inProject,
		"templates":             {where: `owner_id = ?`, args: 1},
		"assets":                {where: ownAssetsSQL, args: 2},
		// Published works.
		"series":               {where: `id IN (` + ownSeries + `)`, args: 2},
		"series_creators":      byUser,
		"chapter_publications": {where: `id IN (` + ownPubs + `)`, args: 2},
		"publication_pages":    {where: `publication_id IN (` + ownPubs + `)`, args: 2},
		// Comments they wrote.
		"comments": {where: `author_id = ?`, args: 1},
		// Reading data.
		"follows":           byUser,
		"user_follows":      {where: `follower_id = ?`, args: 1},
		"read_progress":     byUser,
		"reader_prefs":      byUser,
		"bookmarks":         byUser,
		"downloads":         byUser,
		"read_events":       byUser,
		"age_confirmations": byUser,
	}}
}

func (s *exportScope) has(table string) bool {
	if s == nil {
		return true
	}
	_, ok := s.tables[table]
	return ok
}

func (s *exportScope) where(table string) (string, []any) {
	if s == nil {
		return "", nil
	}
	r := s.tables[table]
	args := make([]any, r.args)
	for i := range args {
		args[i] = s.user
	}
	return r.where, args
}

func (s *exportScope) redact(table string) map[string]any {
	if s == nil {
		return nil
	}
	return s.tables[table].redact
}

// mediaKeys lists the member's stored files: their assets and profile images.
func (s *exportScope) mediaKeys(ctx context.Context, tx *db.Tx, store storage.Store) ([]storage.Info, error) {
	var keys []string
	if err := tx.Select(ctx, &keys, `SELECT key FROM assets WHERE `+ownAssetsSQL+` ORDER BY key`, s.user, s.user); err != nil {
		return nil, err
	}
	var profile struct {
		Avatar string `db:"avatar_key"`
		Banner string `db:"banner_key"`
	}
	if err := tx.Get(ctx, &profile, `SELECT avatar_key, banner_key FROM users WHERE id = ?`, s.user); err != nil {
		return nil, err
	}
	for _, k := range []string{profile.Avatar, profile.Banner} {
		if strings.TrimSpace(k) != "" {
			keys = append(keys, k)
		}
	}
	seen := map[string]bool{}
	var out []storage.Info
	for _, k := range keys {
		if seen[k] || storage.ValidateKey(k) != nil {
			continue
		}
		seen[k] = true
		info, err := store.Stat(ctx, k)
		if err != nil {
			continue // missing objects are counted when the archive is written
		}
		out = append(out, info)
	}
	return out, nil
}
