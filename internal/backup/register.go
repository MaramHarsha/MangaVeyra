package backup

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
)

// Register records an archive that was not written by this server (an
// upload in G10 or a path given to the CLI). The whole archive is verified
// first. With move set the file is moved into the backup directory;
// otherwise it stays where it is.
func (s *service) Register(ctx context.Context, path, actorID string, move bool) (Backup, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return Backup{}, err
	}
	fi, err := os.Stat(abs)
	if err != nil || !fi.Mode().IsRegular() {
		return Backup{}, incompatible("the archive cannot be read")
	}
	a, err := openArchive(abs)
	if err != nil {
		return Backup{}, err
	}
	m := a.M
	for name := range m.Entries {
		if err := a.verify(name); err != nil {
			_ = a.Close()
			return Backup{}, err
		}
	}
	_ = a.Close()
	if m.Kind == KindUserExport || !validKind(m.Kind) || !m.Includes.Database {
		return Backup{}, incompatible("the archive holds no server database")
	}
	sum, err := fileSHA256(abs)
	if err != nil {
		return Backup{}, err
	}
	id := db.NewID()
	final := abs
	if move {
		dir, err := s.destination(ctx)
		if err != nil {
			return Backup{}, err
		}
		final = filepath.Join(dir, fileName(m.Kind, m.CreatedAt, id))
		if err := os.Rename(abs, final); err != nil {
			return Backup{}, fmt.Errorf("%w: %v", ErrDestination, err)
		}
		_ = os.Chmod(final, 0o600)
	}
	inc := m.Includes
	inc.Uploaded = true
	incJSON, _ := json.Marshal(inc)
	created := m.CreatedAt.UTC()
	if created.IsZero() {
		created = s.now()
	}
	if _, err := s.db.Exec(ctx, `INSERT INTO backups (id, kind, status, path, bytes, sha256, includes, app_version, schema, created_by, created_at, finished_at)
		VALUES (?, ?, 'done', ?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, m.Kind, final, fi.Size(), sum, string(incJSON), m.AppVersion, m.Schema,
		s.userOrNull(ctx, actorID), created, s.now()); err != nil {
		if move {
			_ = os.Rename(final, abs)
		}
		return Backup{}, err
	}
	return s.Get(ctx, id)
}
