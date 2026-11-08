package backup

import (
	"context"
	"strings"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/storage"
)

// appKeyPrefixes are the top-level folders MangaVeyra stores objects under:
// project files (projects/<id>/…), files outside a project (assets/<kind>/…),
// worker artifacts (jobs/<id>/…) and import sources (imports/<user>/…). The
// store may be shared with other applications (an S3 bucket without a
// prefix), so backups and restores act only on these keys and on keys the
// database references.
var appKeyPrefixes = []string{"projects/", "assets/", "jobs/", "imports/"}

// appKey reports whether key lies in MangaVeyra's own key layout.
func appKey(key string) bool {
	for _, p := range appKeyPrefixes {
		if strings.HasPrefix(key, p) {
			return true
		}
	}
	return false
}

// referenced reports whether the database q references key (an asset, or a
// profile image).
func referenced(ctx context.Context, q db.Querier, key string) bool {
	var n int
	if err := q.Get(ctx, &n, `SELECT (SELECT COUNT(*) FROM assets WHERE key = ?) + (SELECT COUNT(*) FROM users WHERE avatar_key = ? OR banner_key = ?)`,
		key, key, key); err != nil {
		return false
	}
	return n > 0
}

// appObjects keeps the listed objects that belong to MangaVeyra: keys in its
// layout, and other keys only when q references them.
func appObjects(ctx context.Context, q db.Querier, list []storage.Info) []storage.Info {
	out := list[:0:0]
	for _, o := range list {
		if appKey(o.Key) || referenced(ctx, q, o.Key) {
			out = append(out, o)
		}
	}
	return out
}

// referencedOutsideLayout lists the stored keys outside MangaVeyra's layout
// that the running server's database references: they are MangaVeyra's even
// though their key does not say so, so a restore may remove them.
func (s *service) referencedOutsideLayout(ctx context.Context) (map[string]bool, error) {
	list, err := s.store.List(ctx, "")
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, o := range list {
		if !appKey(o.Key) && referenced(ctx, s.db, o.Key) {
			out[o.Key] = true
		}
	}
	return out, nil
}
