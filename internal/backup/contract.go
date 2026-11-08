package backup

// Frozen Phase 4 contract (docs/plans/0011-phase-4-run-a-server-for-others.md, ADR 0013).
//
// The backup module owns the backups table and the archives under
// $MV_DATA_DIR/backups. The update module takes a pre-update backup and
// restores it on a failed update only through Service; the CLI and G10 use the
// same service.

import (
	"context"
	"errors"
	"time"
)

// Backup kinds (backups.kind).
const (
	KindScheduled  = "scheduled"   // the admin's schedule (G10)
	KindManual     = "manual"      // "Back up now" or `mangaveyra backup create`
	KindPreUpdate  = "pre_update"  // taken by the update module before an update
	KindExport     = "export"      // "Export everything" for a server move
	KindUserExport = "user_export" // one member's own data
)

// Backup statuses (backups.status).
const (
	StatusRunning  = "running"
	StatusDone     = "done"
	StatusFailed   = "failed"
	StatusRestored = "restored"
	StatusDeleted  = "deleted"
)

// ErrNotFound marks an unknown backup or one the caller may not see;
// ErrBusy refuses a second backup or restore while one runs; ErrIncompatible
// refuses an archive from a newer schema or a damaged archive.
var (
	ErrNotFound     = errors.New("backup: not found")
	ErrBusy         = errors.New("backup: another backup or restore is running")
	ErrIncompatible = errors.New("backup: archive cannot be restored on this server")
)

// Backup is one archive and its record.
type Backup struct {
	ID         string     `json:"id"`
	Kind       string     `json:"kind"`
	Status     string     `json:"status"`
	Bytes      int64      `json:"bytes"`
	SHA256     string     `json:"sha256"`
	AppVersion string     `json:"app_version"`
	Schema     string     `json:"schema"` // newest applied migration version in the archive
	UserID     string     `json:"user_id,omitempty"`
	CreatedBy  string     `json:"created_by,omitempty"`
	Error      string     `json:"error,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
}

// Request asks for a new backup.
type Request struct {
	Kind      string
	CreatedBy string // acting user ("" for the scheduler)
	UserID    string // the member whose data a user_export holds
	// KeepFor sets ExpiresAt (0 = the kind's default: pre-update backups are
	// deleted after a successful update unless kept, ADR 0013).
	KeepFor time.Duration
}

// Service creates, lists and restores backups. Create and Restore run
// synchronously and are exclusive (ErrBusy); long work reports progress
// through the jobs queue when started from G10.
type Service interface {
	Create(ctx context.Context, req Request) (Backup, error)
	Get(ctx context.Context, id string) (Backup, error)
	List(ctx context.Context, kind string, limit int) ([]Backup, error)
	// Restore replaces this server's data with the archive's. The caller puts
	// the server in maintenance mode first; actorID is recorded in the audit log.
	Restore(ctx context.Context, id, actorID string) error
	// Delete removes an archive and marks its record deleted.
	Delete(ctx context.Context, id string) error
}
