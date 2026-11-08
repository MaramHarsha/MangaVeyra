package update

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
)

// stepRow is one row of G14's step list.
type stepRow struct {
	N      int
	Key    string
	State  string // waiting, running, done, failed, restored, restore_failed
	Title  string
	Detail string
	Pct    string
}

var stepOrder = []string{StepBackup, StepDownload, StepMigrate, StepHealth, StepCleanup}

func stepTitle(ctx context.Context, n int, key string) string {
	return i18n.T(ctx, "update.step.title", "n", n, "name", i18n.T(ctx, "update.step."+key))
}

// sizeText renders bytes in decimal units ("18.2 GB"), as the design does.
func sizeText(ctx context.Context, n int64) string {
	units := []string{"B", "KB", "MB", "GB", "TB"}
	f := float64(n)
	u := 0
	for f >= 1000 && u < len(units)-1 {
		f /= 1000
		u++
	}
	if u == 0 {
		return i18n.T(ctx, "update.size", "n", fmt.Sprintf("%d", n), "unit", units[u])
	}
	return i18n.T(ctx, "update.size", "n", fmt.Sprintf("%.1f", f), "unit", units[u])
}

func (v view) clock(t *time.Time) string {
	if t == nil {
		return ""
	}
	loc := v.Loc
	if loc == nil {
		loc = time.UTC
	}
	s := t.In(loc).Format("15:04")
	if loc == time.UTC {
		s += " UTC"
	}
	return s
}

func (v view) dateTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	loc := v.Loc
	if loc == nil {
		loc = time.UTC
	}
	s := t.In(loc).Format("2006-01-02 15:04")
	if loc == time.UTC {
		s += " UTC"
	}
	return s
}

// backupName is the design's backup label: backup-<date>-<version>.
func (v view) backupName() string {
	at := v.Run.BackupAt
	if at.IsZero() && v.Attempt != nil && v.Attempt.StartedAt != nil {
		at = *v.Attempt.StartedAt
	}
	from := v.Running
	if v.Attempt != nil {
		from = v.Attempt.FromVersion
	}
	return "backup-" + at.UTC().Format("2006-01-02") + "-" + from
}

// versions joins the releases' versions for "What's new in 1.0.1 and 1.0.2" (oldest first).
func (v view) versions(ctx context.Context) string {
	var list []string
	for i := len(v.O.Releases) - 1; i >= 0; i-- {
		list = append(list, v.O.Releases[i].Version)
	}
	if len(list) == 0 && v.Offer != nil {
		list = []string{v.Offer.ToVersion}
	}
	switch len(list) {
	case 0:
		return ""
	case 1:
		return list[0]
	}
	head := strings.Join(list[:len(list)-1], i18n.T(ctx, "update.list_sep"))
	return i18n.T(ctx, "update.list_and", "a", head, "b", list[len(list)-1])
}

// notes are the changelog lines, oldest release first as in the design (at most 12).
func (v view) notes() []string {
	var out []string
	for i := len(v.O.Releases) - 1; i >= 0; i-- {
		out = append(out, v.O.Releases[i].Notes...)
	}
	if len(out) > 12 {
		out = append(out[:12:12], "…")
	}
	return out
}

func (v view) toVersion() string {
	if v.Attempt != nil {
		return v.Attempt.ToVersion
	}
	if v.Offer != nil {
		return v.Offer.ToVersion
	}
	return ""
}

func (v view) fromVersion() string {
	if v.Attempt != nil {
		return v.Attempt.FromVersion
	}
	return v.Running
}

// canStart reports whether "Back up and update" can run, and why not.
func (v view) blocker(ctx context.Context) string {
	switch {
	case !v.BackupsOK:
		return i18n.T(ctx, "update.problem.no_backups")
	case !v.KeyOK:
		return i18n.T(ctx, "update.problem.no_key")
	case !v.Container && v.O.Asset == nil:
		return i18n.T(ctx, "update.problem.no_download")
	}
	return ""
}

func (v view) availableRows(ctx context.Context) []stepRow {
	backupDetail := i18n.T(ctx, "update.detail.backup.plan")
	if v.LastBackup > 0 {
		backupDetail = i18n.T(ctx, "update.detail.backup.plan_size", "size", sizeText(ctx, v.LastBackup))
	}
	cleanup := i18n.T(ctx, "update.detail.cleanup.plan")
	if v.Config.KeepBackup {
		cleanup = i18n.T(ctx, "update.detail.cleanup.plan_keep")
	}
	details := map[string]string{
		StepBackup:   backupDetail,
		StepDownload: i18n.T(ctx, "update.detail.download.plan"),
		StepMigrate:  i18n.T(ctx, "update.detail.migrate.plan"),
		StepHealth:   i18n.T(ctx, "update.detail.health.plan"),
		StepCleanup:  cleanup,
	}
	var rows []stepRow
	for i, k := range stepOrder {
		rows = append(rows, stepRow{N: i + 1, Key: k, State: StateWaiting, Title: stepTitle(ctx, i+1, k), Detail: details[k]})
	}
	return rows
}

func (v view) runDetail(ctx context.Context, s Step) (string, string) {
	to := v.toVersion()
	switch s.Key {
	case StepBackup:
		switch s.State {
		case StateRunning:
			return i18n.T(ctx, "update.detail.backup.running"), ""
		case StateDone:
			if v.State == stateFailed {
				return i18n.T(ctx, "update.detail.backup.saved_size", "size", sizeText(ctx, v.Run.BackupBytes)), ""
			}
			return i18n.T(ctx, "update.detail.backup.saved", "name", v.backupName(), "size", sizeText(ctx, v.Run.BackupBytes)), ""
		}
	case StepDownload:
		switch s.State {
		case StateRunning:
			if v.Attempt != nil && v.Attempt.Status == StatusVerifying {
				return i18n.T(ctx, "update.detail.download.verifying"), ""
			}
			pct := ""
			if v.Run.DownloadSize > 0 {
				pct = fmt.Sprintf("%d%%", v.Run.Downloaded*100/v.Run.DownloadSize)
			}
			return i18n.T(ctx, "update.detail.download.running", "done", sizeText(ctx, v.Run.Downloaded),
				"total", sizeText(ctx, v.Run.DownloadSize)), pct
		case StateDone:
			if v.State == stateFailed {
				return i18n.T(ctx, "update.detail.ok"), ""
			}
			return i18n.T(ctx, "update.detail.download.done"), ""
		case StateSkipped:
			return i18n.T(ctx, "update.detail.download.host"), ""
		}
	case StepMigrate:
		switch s.State {
		case StateRunning:
			if v.Run.Pending > 0 {
				return i18n.T(ctx, "update.detail.migrate.running_n", "count", v.Run.Pending), ""
			}
			return i18n.T(ctx, "update.detail.migrate.running"), ""
		case StateDone:
			if v.Run.Mode == ModeContainer {
				return i18n.T(ctx, "update.detail.migrate.host"), ""
			}
			if v.Run.Applied > 0 {
				return i18n.T(ctx, "update.detail.migrate.done_n", "count", v.Run.Applied), ""
			}
			return i18n.T(ctx, "update.detail.migrate.none"), ""
		case StateFailed:
			cause := migrateCause(ctx, s.Error)
			if n := strings.TrimPrefix(v.Run.Code, "UPD-MIGRATE-"); n != v.Run.Code && n != "" {
				return i18n.T(ctx, "update.detail.migrate.failed_n", "n", n, "error", cause), ""
			}
			return i18n.T(ctx, "update.detail.failed", "error", cause), ""
		}
	case StepHealth:
		switch s.State {
		case StateRunning:
			return i18n.T(ctx, "update.detail.health.running", "version", to), ""
		case StateDone:
			if v.Run.Phase == PhaseRestarting {
				return i18n.T(ctx, "update.detail.health.restarting", "version", to), ""
			}
			if v.Run.Mode == ModeContainer {
				return i18n.T(ctx, "update.detail.health.host", "version", to), ""
			}
			if v.Run.Workers > 0 {
				return i18n.T(ctx, "update.detail.health.done_workers", "count", v.Run.Workers), ""
			}
			return i18n.T(ctx, "update.detail.health.done"), ""
		}
	case StepCleanup:
		switch s.State {
		case StateRunning:
			return i18n.T(ctx, "update.detail.cleanup.running"), ""
		case StateDone:
			if v.Run.BackupDeleted {
				return i18n.T(ctx, "update.detail.cleanup.deleted"), ""
			}
			return i18n.T(ctx, "update.detail.cleanup.kept"), ""
		}
	case StepRestore:
		switch s.State {
		case StateRunning:
			return i18n.T(ctx, "update.detail.restore.running", "version", v.fromVersion()), ""
		case StateDone:
			return i18n.T(ctx, "update.detail.restore.done", "version", v.fromVersion()), ""
		case StateFailed:
			return i18n.T(ctx, "update.detail.restore.failed", "error", short(s.Error)), ""
		}
	}
	if s.State == StateFailed {
		return i18n.T(ctx, "update.detail.failed", "error", short(s.Error)), ""
	}
	return i18n.T(ctx, "update.detail.waiting"), ""
}

var migratePrefix = regexp.MustCompile(`^(db: )?migrate \S+: (statement \d+: )?`)

// migrateCause shortens a failed migration's error to its cause ("disk full").
func migrateCause(ctx context.Context, s string) string {
	s = short(s)
	low := strings.ToLower(s)
	if strings.Contains(low, "no space left") || strings.Contains(low, "disk is full") || strings.Contains(low, "disk full") {
		return i18n.T(ctx, "update.cause.disk_full")
	}
	return migratePrefix.ReplaceAllString(s, "")
}

func short(s string) string {
	s = strings.TrimSpace(strings.SplitN(s, "\n", 2)[0])
	if utf8.RuneCountInString(s) > 140 {
		r := []rune(s)
		s = string(r[:140]) + "…"
	}
	return s
}

// runRows are the five steps of a running install.
func (v view) runRows(ctx context.Context) []stepRow {
	var rows []stepRow
	for i, k := range stepOrder {
		s := v.Run.Step(k)
		d, pct := v.runDetail(ctx, s)
		rows = append(rows, stepRow{N: i + 1, Key: k, State: s.State, Title: stepTitle(ctx, i+1, k), Detail: d, Pct: pct})
	}
	if r := v.Run.Step(StepRestore); r.State != StateWaiting {
		d, _ := v.runDetail(ctx, r)
		rows = append(rows, stepRow{N: 6, Key: StepRestore, State: restoreState(r.State), Title: stepTitle(ctx, 6, StepRestore), Detail: d})
	}
	return rows
}

func restoreState(s string) string {
	switch s {
	case StateDone:
		return "restored"
	case StateFailed:
		return "restore_failed"
	}
	return s
}

// failedRows are the steps up to the failure, then the restore (G14 failed).
func (v view) failedRows(ctx context.Context) []stepRow {
	var rows []stepRow
	for _, k := range stepOrder {
		s := v.Run.Step(k)
		if s.State == StateWaiting || s.State == StateSkipped {
			break
		}
		d, _ := v.runDetail(ctx, s)
		n := len(rows) + 1
		rows = append(rows, stepRow{N: n, Key: k, State: s.State, Title: stepTitle(ctx, n, k), Detail: d})
		if s.State == StateFailed {
			break
		}
	}
	if r := v.Run.Step(StepRestore); r.State != StateWaiting {
		d, _ := v.runDetail(ctx, r)
		n := len(rows) + 1
		rows = append(rows, stepRow{N: n, Key: StepRestore, State: restoreState(r.State), Title: stepTitle(ctx, n, StepRestore), Detail: d})
	}
	return rows
}

// doneRows are the design's two summary rows: the backup and the health check.
func (v view) doneRows(ctx context.Context) []stepRow {
	backupDetail := i18n.T(ctx, "update.detail.backup.deleted")
	if !v.Run.BackupDeleted {
		backupDetail = i18n.T(ctx, "update.detail.backup.kept")
	}
	h := v.Run.Step(StepHealth)
	hd, _ := v.runDetail(ctx, h)
	return []stepRow{
		{N: 1, Key: StepBackup, State: StateDone, Title: stepTitle(ctx, 1, StepBackup), Detail: backupDetail},
		{N: 4, Key: StepHealth, State: StateDone, Title: stepTitle(ctx, 4, StepHealth), Detail: hd},
	}
}

// progress is the overall progress of a running install (0–1): finished
// steps plus the measured part of the download.
func (v view) progress() float64 {
	done := 0.0
	for _, k := range stepOrder {
		s := v.Run.Step(k)
		switch {
		case s.State == StateDone || s.State == StateSkipped:
			done++
		case s.State == StateRunning && k == StepDownload && v.Run.DownloadSize > 0:
			done += float64(v.Run.Downloaded) / float64(v.Run.DownloadSize)
		}
	}
	return done / float64(len(stepOrder))
}

func (v view) doneSub(ctx context.Context) string {
	switch {
	case v.Run.BackupDeleted && v.Run.BackupBytes > 0:
		return i18n.T(ctx, "update.done.freed", "size", sizeText(ctx, v.Run.BackupBytes))
	case v.Run.BackupDeleted:
		return i18n.T(ctx, "update.done.deleted")
	}
	return i18n.T(ctx, "update.done.kept")
}

func (v view) failedTitle(ctx context.Context) string {
	switch {
	case v.Run.Restored:
		return i18n.T(ctx, "update.failed.title_restored", "version", v.fromVersion())
	case v.Run.RestoreFailed:
		return i18n.T(ctx, "update.failed.title_restore_failed")
	}
	return i18n.T(ctx, "update.failed.title")
}

func (v view) failedSub(ctx context.Context) string {
	switch {
	case v.Run.Restored:
		return i18n.T(ctx, "update.failed.sub_restored")
	case v.Run.RestoreFailed:
		return i18n.T(ctx, "update.failed.sub_restore_failed")
	}
	return i18n.T(ctx, "update.failed.sub", "version", v.fromVersion())
}

// failedAdvice is the warning box under the steps.
func (v view) failedAdvice(ctx context.Context) string {
	if v.Run.RestoreFailed {
		return i18n.T(ctx, "update.advice.restore_failed", "id", v.Attempt.backupID())
	}
	reason := i18n.T(ctx, "update.advice.generic")
	code := v.Run.Code
	errText := ""
	if v.Attempt != nil {
		errText = strings.ToLower(v.Attempt.Error)
	}
	switch {
	case strings.Contains(errText, "no space left") || strings.Contains(errText, "disk full"):
		reason = i18n.T(ctx, "update.advice.disk")
	case strings.HasPrefix(code, "UPD-MIGRATE"):
		reason = i18n.T(ctx, "update.advice.migrate")
	case code == "UPD-HEALTH":
		reason = i18n.T(ctx, "update.advice.health")
	case code == "UPD-DOWNLOAD" || code == "UPD-VERIFY":
		reason = i18n.T(ctx, "update.advice.download")
	case code == "UPD-SWAP":
		reason = i18n.T(ctx, "update.advice.swap")
	case code == "UPD-BACKUP":
		return i18n.T(ctx, "update.advice.backup")
	case code == "UPD-INTERRUPTED":
		reason = i18n.T(ctx, "update.advice.interrupted")
	}
	if v.Run.Restored {
		return i18n.T(ctx, "update.advice.kept", "reason", reason)
	}
	return reason
}

func (v view) kindBadge(ctx context.Context) (string, string, string) {
	switch v.O.Kind {
	case KindMajor:
		return "warning", "alert-triangle", i18n.T(ctx, "update.badge.major")
	case KindMinor:
		return "info", "sparkles", i18n.T(ctx, "update.badge.minor")
	}
	return "success", "shield-check", i18n.T(ctx, "update.badge.patch")
}

func checkMessage(ctx context.Context, code string) string {
	switch code {
	case "":
		return ""
	case checkNoKey, checkSignature, checkManifest, checkChannel, checkVersion, checkFetch:
		return i18n.T(ctx, "update.check.error."+code)
	}
	return i18n.T(ctx, "update.check.error.fetch")
}

func (v view) settingsSub(ctx context.Context) string {
	switch {
	case !v.Config.Checks:
		return i18n.T(ctx, "update.settings.sub_off")
	case v.Check.Error != "":
		return i18n.T(ctx, "update.settings.sub_error", "reason", checkMessage(ctx, v.Check.Error))
	case v.Offer != nil:
		return i18n.T(ctx, "update.settings.sub_available", "version", v.Offer.ToVersion)
	case v.Check.CheckedAt != nil:
		return i18n.T(ctx, "update.settings.sub_current", "time", v.dateTime(v.Check.CheckedAt))
	}
	return i18n.T(ctx, "update.settings.sub_never")
}

func (v view) lastChecked(ctx context.Context) string {
	if v.Check.CheckedAt == nil {
		return i18n.T(ctx, "update.settings.never")
	}
	return i18n.T(ctx, "update.settings.checked", "time", v.dateTime(v.Check.CheckedAt))
}

func (v view) attemptLine(ctx context.Context) string {
	a := v.Last
	if a == nil || a.StartedAt == nil {
		return ""
	}
	return i18n.T(ctx, "update.settings.last_update", "from", a.FromVersion, "to", a.ToVersion,
		"status", i18n.T(ctx, "update.status."+a.Status), "time", v.dateTime(a.StartedAt))
}

func (v view) idOf(r *Record) string {
	if r == nil {
		return ""
	}
	return r.ID
}

// containerCommands are the host commands from docs/install ("Upgrading",
// Docker Compose), run from the directory with compose.yaml.
var containerCommands = []string{
	"git pull",
	"docker compose up -d --build",
	`docker compose logs app | grep "database migrated"`,
}
