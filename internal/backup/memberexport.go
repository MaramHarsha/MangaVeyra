package backup

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
)

// Member export limits (Settings → Data). Any member may export, so exports
// are bounded per member: one runs at a time, a new one is made at most once
// per exportEvery (the newest is served again in between), and only the
// newest is kept. Exports read the database in one snapshot and never take
// the server-wide backup lease, so they cannot hold up backups, restores or
// updates.
const (
	exportEvery = time.Hour
	// exportStale is how long a running export counts as running; one older
	// than that was interrupted (the request that wrote it ended long ago).
	exportStale = 2 * time.Hour
)

// ErrExportRunning refuses a member export while the member's previous one is
// still being written.
var ErrExportRunning = errors.New("backup: your export is still being prepared")

// MemberExport returns the member's export: their newest one when it was made
// within the last hour, otherwise a new one.
func (s *service) MemberExport(ctx context.Context, userID string) (Backup, error) {
	var r backupRow
	err := s.db.Get(ctx, &r, `SELECT `+backupCols+` FROM backups WHERE kind = 'user_export' AND user_id = ? AND status = 'done'
		AND created_at > ? ORDER BY created_at DESC, id DESC LIMIT 1`, userID, s.now().Add(-exportEvery))
	switch {
	case err == nil && r.Path != "":
		return r.backup(), nil
	case err != nil && !db.IsNotFound(err):
		return Backup{}, err
	}
	return s.Create(ctx, Request{Kind: KindUserExport, UserID: userID, CreatedBy: userID})
}

// startExport records a running member export unless one of the member's is
// running already.
func (s *service) startExport(ctx context.Context, id string, req Request, now time.Time, expires *time.Time) error {
	if _, busy := s.exporting.LoadOrStore(req.UserID, id); busy {
		return ErrExportRunning
	}
	createdBy := s.userOrNull(ctx, req.CreatedBy)
	err := s.db.WithTx(ctx, func(tx *db.Tx) error {
		if tx.Dialect() == db.Postgres {
			// Serialise the per-member bookkeeping (SQLite write transactions are serialised already).
			if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(7468326830)`); err != nil {
				return err
			}
		}
		var running int
		if err := tx.Get(ctx, &running, `SELECT COUNT(*) FROM backups WHERE kind = 'user_export' AND user_id = ? AND status = 'running'
			AND created_at > ?`, req.UserID, now.Add(-exportStale)); err != nil {
			return err
		}
		if running > 0 {
			return ErrExportRunning
		}
		_, err := tx.Exec(ctx, `INSERT INTO backups (id, kind, status, app_version, user_id, created_by, created_at, expires_at)
			VALUES (?, ?, 'running', ?, ?, ?, ?, ?)`, id, req.Kind, s.d.Version.Version, req.UserID, createdBy, now, expires)
		return err
	})
	if err != nil {
		s.exporting.Delete(req.UserID)
		if errors.Is(err, ErrExportRunning) {
			return err
		}
		return fmt.Errorf("backup: record: %w", err)
	}
	return nil
}

// finishExport ends the member's running export and, after a successful one,
// deletes their older exports: only the newest is kept.
func (s *service) finishExport(ctx context.Context, userID, id string, ok bool) {
	s.exporting.Delete(userID)
	if !ok {
		return
	}
	var older []string
	if err := s.db.Select(ctx, &older, `SELECT id FROM backups WHERE kind = 'user_export' AND user_id = ? AND id <> ?
		AND status IN ('done', 'restored', 'failed')`, userID, id); err != nil {
		s.log.Warn("backup: list older member exports", "err", err)
		return
	}
	for _, old := range older {
		if err := s.Delete(ctx, old); err != nil {
			s.log.Warn("backup: delete older member export", "id", old, "err", err)
		}
	}
}
