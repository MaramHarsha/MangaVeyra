package update

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
)

// Record statuses (updates.status, migration 0005).
const (
	StatusAvailable   = "available"
	StatusBackingUp   = "backing_up"
	StatusDownloading = "downloading"
	StatusVerifying   = "verifying"
	StatusMigrating   = "migrating"
	StatusHealthCheck = "health_check" // also covers the swap and restart that follow a passed check
	StatusDone        = "done"
	StatusFailed      = "failed"      // stopped before anything needed restoring, or the restore failed
	StatusRolledBack  = "rolled_back" // the pre-update backup was restored
)

// activeStatuses are the statuses of an install in progress.
var activeStatuses = []string{StatusBackingUp, StatusDownloading, StatusVerifying, StatusMigrating, StatusHealthCheck}

func isActive(status string) bool {
	for _, s := range activeStatuses {
		if s == status {
			return true
		}
	}
	return false
}

// Record is one row of the updates table: the current offer (status
// available) or one install attempt.
type Record struct {
	ID          string     `db:"id"`
	FromVersion string     `db:"from_version"`
	ToVersion   string     `db:"to_version"`
	Channel     string     `db:"channel"`
	Status      string     `db:"status"`
	Changelog   string     `db:"changelog"`
	DownloadURL string     `db:"download_url"`
	Signature   string     `db:"signature"`
	BackupID    *string    `db:"backup_id"`
	Steps       string     `db:"steps"`
	Error       string     `db:"error"`
	StartedBy   *string    `db:"started_by"`
	StartedAt   *time.Time `db:"started_at"`
	FinishedAt  *time.Time `db:"finished_at"`
	CreatedAt   time.Time  `db:"created_at"`
}

const recordCols = `id, from_version, to_version, channel, status, changelog, download_url, signature, backup_id, steps, error,
	started_by, started_at, finished_at, created_at`

// Offer is what a check found (stored as JSON in updates.changelog).
type Offer struct {
	Kind      string    `json:"kind"`
	Published time.Time `json:"published"`
	NotesURL  string    `json:"notes_url,omitempty"`
	Releases  []Release `json:"releases"`
	// Asset is this platform's download (nil when the release has none).
	Asset *Asset `json:"asset,omitempty"`
	// Security reports a security fix in any release since the running version.
	Security bool `json:"security,omitempty"`
	// AllPatch reports that every release since the running version is a
	// fixes-only patch release (needed for automatic installs).
	AllPatch bool `json:"all_patch,omitempty"`
}

// Step keys, in order. "restore" is added after a failure.
const (
	StepBackup   = "backup"
	StepDownload = "download"
	StepMigrate  = "migrate"
	StepHealth   = "health"
	StepCleanup  = "cleanup"
	StepRestore  = "restore"
)

// Step states.
const (
	StateWaiting = "waiting"
	StateRunning = "running"
	StateDone    = "done"
	StateFailed  = "failed"
	StateSkipped = "skipped"
)

// Step is one row of G14's five steps.
type Step struct {
	Key        string     `json:"key"`
	State      string     `json:"state"`
	Error      string     `json:"error,omitempty"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// Install modes.
const (
	ModeBinary    = "binary"    // swap the executable and restart
	ModeContainer = "container" // back up, then show the commands for the host
)

// Run phases after the health check.
const (
	PhaseRestarting = "restarting" // swapped; waiting for the new binary to start
)

// Run is the progress of one install attempt (stored as JSON in updates.steps).
type Run struct {
	Mode  string `json:"mode"`
	Steps []Step `json:"steps"`
	// Keep keeps the pre-update backup for 7 days after success.
	Keep bool `json:"keep,omitempty"`
	// Auto marks an automatic patch install (03:00).
	Auto bool `json:"auto,omitempty"`
	// Maintenance is the admin's maintenance setting before the update, put back afterwards.
	Maintenance *settings.Maintenance `json:"maintenance_before,omitempty"`
	Phase       string                `json:"phase,omitempty"`
	// Code is the public error code (UPD-MIGRATE-0602...).
	Code string `json:"code,omitempty"`
	// Restored reports that the pre-update backup was restored.
	Restored bool `json:"restored,omitempty"`
	// RestoreFailed reports that restoring the backup failed (maintenance stays on).
	RestoreFailed bool `json:"restore_failed,omitempty"`
	// Measured values shown in G14.
	BackupBytes   int64     `json:"backup_bytes,omitempty"`
	BackupAt      time.Time `json:"backup_at,omitempty"`
	BackupDeleted bool      `json:"backup_deleted,omitempty"`
	Downloaded    int64     `json:"downloaded,omitempty"`
	DownloadSize  int64     `json:"download_size,omitempty"`
	Pending       int       `json:"pending_migrations"`
	Applied       int       `json:"applied_migrations"`
	Workers       int       `json:"workers_online"`
	// Ack hides the finished result once the admin pressed Continue or closed it.
	Ack bool      `json:"ack,omitempty"`
	Log []LogLine `json:"log,omitempty"`
}

// LogLine is one line of the update log ("Show log", "Copy log").
type LogLine struct {
	At   time.Time `json:"at"`
	Text string    `json:"text"`
}

const maxLogLines = 400

func newRun(mode string) Run {
	r := Run{Mode: mode}
	for _, k := range []string{StepBackup, StepDownload, StepMigrate, StepHealth, StepCleanup} {
		r.Steps = append(r.Steps, Step{Key: k, State: StateWaiting})
	}
	return r
}

// Step returns the step with key (a zero step when missing).
func (r *Run) Step(key string) Step {
	for _, s := range r.Steps {
		if s.Key == key {
			return s
		}
	}
	return Step{Key: key, State: StateWaiting}
}

func (r *Run) setStep(key, state, errText string) {
	now := db.Now()
	for i := range r.Steps {
		if r.Steps[i].Key != key {
			continue
		}
		s := &r.Steps[i]
		s.State = state
		s.Error = errText
		switch state {
		case StateRunning:
			s.StartedAt, s.FinishedAt = &now, nil
		case StateDone, StateFailed, StateSkipped:
			if s.StartedAt == nil {
				s.StartedAt = &now
			}
			s.FinishedAt = &now
		}
		return
	}
	s := Step{Key: key, State: state, Error: errText}
	if state != StateWaiting {
		s.StartedAt = &now
	}
	if state == StateDone || state == StateFailed {
		s.FinishedAt = &now
	}
	r.Steps = append(r.Steps, s)
}

func (r *Run) logf(text string) {
	r.Log = append(r.Log, LogLine{At: db.Now(), Text: redact(text)})
	if len(r.Log) > maxLogLines {
		r.Log = append(r.Log[:1:1], r.Log[len(r.Log)-maxLogLines+1:]...)
	}
}

// Offer decodes the offer stored with the record.
func (rec *Record) Offer() Offer {
	var o Offer
	_ = json.Unmarshal([]byte(rec.Changelog), &o)
	return o
}

// Run decodes the install progress stored with the record.
func (rec *Record) Run() Run {
	var r Run
	if s := strings.TrimSpace(rec.Steps); s != "" && s[0] == '{' {
		_ = json.Unmarshal([]byte(s), &r)
	}
	return r
}

func (rec *Record) backupID() string {
	if rec.BackupID == nil {
		return ""
	}
	return *rec.BackupID
}

// Finished reports a terminal install attempt.
func (rec *Record) Finished() bool {
	return rec.Status == StatusDone || rec.Status == StatusFailed || rec.Status == StatusRolledBack
}

var errNoRecord = errors.New("update: no such update")

func (m *Module) offer(ctx context.Context) (*Record, error) {
	var rec Record
	err := m.d.DB.Get(ctx, &rec, `SELECT `+recordCols+` FROM updates WHERE status = ? ORDER BY created_at DESC, id DESC LIMIT 1`, StatusAvailable)
	if db.IsNotFound(err) {
		return nil, nil
	}
	return &rec, err
}

// latestAttempt returns the newest install attempt (any status but available).
func (m *Module) latestAttempt(ctx context.Context) (*Record, error) {
	var rec Record
	err := m.d.DB.Get(ctx, &rec, `SELECT `+recordCols+` FROM updates WHERE status <> ? AND started_at IS NOT NULL
		ORDER BY started_at DESC, id DESC LIMIT 1`, StatusAvailable)
	if db.IsNotFound(err) {
		return nil, nil
	}
	return &rec, err
}

func (m *Module) record(ctx context.Context, id string) (*Record, error) {
	var rec Record
	err := m.d.DB.Get(ctx, &rec, `SELECT `+recordCols+` FROM updates WHERE id = ?`, id)
	if db.IsNotFound(err) {
		return nil, errNoRecord
	}
	return &rec, err
}

// saveOffer replaces the current offer with rec (one available row at a time).
func (m *Module) saveOffer(ctx context.Context, rec *Record) error {
	return m.d.DB.WithTx(ctx, func(tx *db.Tx) error {
		var cur Record
		err := tx.Get(ctx, &cur, `SELECT `+recordCols+` FROM updates WHERE status = ? ORDER BY created_at DESC, id DESC LIMIT 1`, StatusAvailable)
		switch {
		case db.IsNotFound(err):
			if rec.ID == "" {
				rec.ID = db.NewID()
			}
			rec.CreatedAt = db.Now()
			_, err = tx.Exec(ctx, `INSERT INTO updates (id, from_version, to_version, channel, status, changelog, download_url, signature,
				steps, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, rec.ID, rec.FromVersion, rec.ToVersion, rec.Channel,
				StatusAvailable, rec.Changelog, rec.DownloadURL, rec.Signature, nonEmpty(rec.Steps, "[]"), rec.CreatedAt)
			return err
		case err != nil:
			return err
		}
		rec.ID, rec.CreatedAt = cur.ID, cur.CreatedAt
		if cur.ToVersion != rec.ToVersion {
			// A different release: the old offer's container backup (if any) no longer applies.
			rec.BackupID, rec.Steps = nil, "[]"
			rec.CreatedAt = db.Now()
		} else {
			rec.BackupID, rec.Steps = cur.BackupID, cur.Steps
		}
		_, err = tx.Exec(ctx, `UPDATE updates SET from_version = ?, to_version = ?, channel = ?, changelog = ?, download_url = ?,
			signature = ?, backup_id = ?, steps = ?, created_at = ? WHERE id = ? AND status = ?`, rec.FromVersion, rec.ToVersion,
			rec.Channel, rec.Changelog, rec.DownloadURL, rec.Signature, rec.BackupID, nonEmpty(rec.Steps, "[]"), rec.CreatedAt,
			rec.ID, StatusAvailable)
		return err
	})
}

// dropOffers removes offers that are no longer newer than the running version
// and have no backup attached.
func (m *Module) dropOffer(ctx context.Context, id string) error {
	_, err := m.d.DB.Exec(ctx, `DELETE FROM updates WHERE id = ? AND status = ?`, id, StatusAvailable)
	return err
}

// claim turns the available offer into a running install attempt. It
// succeeds once, for one process: the row must still be available.
func (m *Module) claim(ctx context.Context, id, actor string, run Run) (*Record, error) {
	now := db.Now()
	res, err := m.d.DB.Exec(ctx, `UPDATE updates SET status = ?, steps = ?, error = '', started_by = ?, started_at = ?, finished_at = NULL
		WHERE id = ? AND status = ?`, StatusBackingUp, mustJSON(run), db.NullString(actor), now, id, StatusAvailable)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, ErrBusy
	}
	return m.record(ctx, id)
}

// saveRun writes the attempt's progress. After a restore the row may hold the
// archive's older copy or be missing, so it is inserted again when needed.
func (m *Module) saveRun(ctx context.Context, rec *Record, run Run) error {
	rec.Steps = mustJSON(run)
	res, err := m.d.DB.Exec(ctx, `UPDATE updates SET status = ?, backup_id = ?, steps = ?, error = ?, started_by = ?, started_at = ?,
		finished_at = ? WHERE id = ?`, rec.Status, rec.BackupID, rec.Steps, rec.Error, rec.StartedBy, rec.StartedAt, rec.FinishedAt, rec.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return nil
	}
	_, err = m.d.DB.Exec(ctx, `INSERT INTO updates (`+recordCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.ID, rec.FromVersion, rec.ToVersion, rec.Channel, rec.Status, rec.Changelog, rec.DownloadURL, rec.Signature,
		backupRef(ctx, m, rec.BackupID), rec.Steps, rec.Error, rec.StartedBy, rec.StartedAt, rec.FinishedAt, rec.CreatedAt)
	return err
}

// backupRef keeps the foreign key valid when a restore removed the backup row.
func backupRef(ctx context.Context, m *Module, id *string) *string {
	if id == nil {
		return nil
	}
	var n int
	if err := m.d.DB.Get(ctx, &n, `SELECT COUNT(*) FROM backups WHERE id = ?`, *id); err != nil || n == 0 {
		return nil
	}
	return id
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func nonEmpty(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}
