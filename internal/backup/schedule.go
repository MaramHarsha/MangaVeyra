package backup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
)

// Schedule frequencies.
const (
	FrequencyOff    = "off"
	FrequencyDaily  = "daily"
	FrequencyWeekly = "weekly"
)

// Module-owned settings keys (instance_settings, namespaced by "backup.").
const (
	scheduleKey = "backup.schedule"
	slotKey     = "backup.slot"
	presencePre = "backup.server."
)

// Schedule is the admin's backup schedule (G10). It is stored as the
// module-owned setting backup.schedule, so it needs no instance settings field.
type Schedule struct {
	Frequency string `json:"frequency"`
	// Weekday is the day of a weekly backup (0 = Sunday).
	Weekday int `json:"weekday"`
	// Hour is the hour of day (0–23) in TZ.
	Hour int    `json:"hour"`
	TZ   string `json:"tz"`
	// Keep is how many scheduled archives are kept (the newest).
	Keep int `json:"keep"`
	// Destination is an absolute directory; "" is $MV_DATA_DIR/backups.
	Destination string    `json:"destination"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// DefaultSchedule is used until the admin saves one: off, keep 14.
func DefaultSchedule() Schedule {
	return Schedule{Frequency: FrequencyOff, Hour: 3, TZ: "UTC", Keep: 14}
}

// Validate checks a schedule.
func (sc Schedule) Validate() error {
	switch sc.Frequency {
	case FrequencyOff, FrequencyDaily, FrequencyWeekly:
	default:
		return fmt.Errorf("%w: frequency", ErrInvalid)
	}
	if sc.Hour < 0 || sc.Hour > 23 || sc.Weekday < 0 || sc.Weekday > 6 || sc.Keep < 1 || sc.Keep > 365 {
		return fmt.Errorf("%w: schedule", ErrInvalid)
	}
	if _, err := time.LoadLocation(sc.TZ); err != nil {
		return fmt.Errorf("%w: time zone", ErrInvalid)
	}
	if sc.Destination != "" && !filepath.IsAbs(sc.Destination) {
		return fmt.Errorf("%w: the destination must be an absolute path", ErrDestination)
	}
	return nil
}

func (sc Schedule) location() *time.Location {
	if loc, err := time.LoadLocation(sc.TZ); err == nil {
		return loc
	}
	return time.UTC
}

// Slot returns the most recent scheduled time at or before now ("" when off).
func (sc Schedule) Slot(now time.Time) (time.Time, bool) {
	if sc.Frequency == FrequencyOff || sc.Frequency == "" {
		return time.Time{}, false
	}
	loc := sc.location()
	local := now.In(loc)
	slot := time.Date(local.Year(), local.Month(), local.Day(), sc.Hour, 0, 0, 0, loc)
	if slot.After(local) {
		slot = slot.AddDate(0, 0, -1)
	}
	if sc.Frequency == FrequencyWeekly {
		for int(slot.Weekday()) != sc.Weekday {
			slot = slot.AddDate(0, 0, -1)
		}
	}
	return slot.UTC(), true
}

// Next returns the next scheduled time after now.
func (sc Schedule) Next(now time.Time) (time.Time, bool) {
	last, ok := sc.Slot(now)
	if !ok {
		return time.Time{}, false
	}
	step := 1
	if sc.Frequency == FrequencyWeekly {
		step = 7
	}
	return last.In(sc.location()).AddDate(0, 0, step).UTC(), true
}

// Schedule returns the saved schedule (or the default).
func (s *service) Schedule(ctx context.Context) (Schedule, error) {
	sc := DefaultSchedule()
	if s.d.Settings == nil {
		return sc, nil
	}
	if _, err := s.d.Settings.GetKey(ctx, scheduleKey, &sc); err != nil {
		return DefaultSchedule(), err
	}
	if sc.Keep <= 0 {
		sc.Keep = DefaultSchedule().Keep
	}
	if sc.TZ == "" {
		sc.TZ = "UTC"
	}
	return sc, nil
}

// SaveSchedule validates and stores the schedule; a destination directory
// must exist and be writable by the server.
func (s *service) SaveSchedule(ctx context.Context, sc Schedule, by string) (Schedule, error) {
	sc.Destination = strings.TrimSpace(sc.Destination)
	if sc.Destination != "" {
		sc.Destination = filepath.Clean(sc.Destination)
	}
	if err := sc.Validate(); err != nil {
		return sc, err
	}
	if sc.Destination != "" {
		if err := checkWritable(sc.Destination); err != nil {
			return sc, err
		}
	}
	sc.UpdatedAt = s.now()
	if err := s.d.Settings.SetKey(ctx, scheduleKey, sc, by); err != nil {
		return sc, err
	}
	return sc, nil
}

// checkWritable proves the server can create private files in dir.
func checkWritable(dir string) error {
	fi, err := os.Stat(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: %s does not exist", ErrDestination, dir)
		}
		return fmt.Errorf("%w: %v", ErrDestination, err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("%w: %s is not a directory", ErrDestination, dir)
	}
	f, err := os.CreateTemp(dir, ".mangaveyra-write-check-*")
	if err != nil {
		return fmt.Errorf("%w: %v", ErrDestination, err)
	}
	name := f.Name()
	_ = f.Close()
	if err := os.Remove(name); err != nil {
		return fmt.Errorf("%w: %v", ErrDestination, err)
	}
	return nil
}

// destination is the directory new archives are written to.
func (s *service) destination(ctx context.Context) (string, error) {
	sc, err := s.Schedule(ctx)
	if err != nil {
		return "", err
	}
	if sc.Destination != "" {
		if err := checkWritable(sc.Destination); err != nil {
			return "", err
		}
		return sc.Destination, nil
	}
	dir := s.d.Cfg.BackupDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("%w: %v", ErrDestination, err)
	}
	return dir, nil
}

// claimSlot records slot as taken; only one process sharing the database wins.
func (s *service) claimSlot(ctx context.Context, slot time.Time) (bool, error) {
	v := db.MustJSON(slot.UTC().Format(time.RFC3339))
	res, err := s.db.Exec(ctx, `INSERT INTO instance_settings (key, value, updated_by, updated_at) VALUES (?, ?, NULL, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at
		WHERE instance_settings.value < excluded.value`, slotKey, v, s.now())
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// runDue takes a scheduled backup when a slot is due and this process claims it.
func (s *service) runDue(ctx context.Context) error {
	sc, err := s.Schedule(ctx)
	if err != nil {
		return err
	}
	now := s.now()
	slot, ok := sc.Slot(now)
	// A slot before the schedule was saved never runs, and a missed slot runs
	// only within a day (a server that was off does not catch up for weeks).
	if !ok || slot.Before(sc.UpdatedAt) || now.Sub(slot) > 24*time.Hour {
		return nil
	}
	won, err := s.claimSlot(ctx, slot)
	if err != nil || !won {
		return err
	}
	for attempt := 0; ; attempt++ {
		_, err = s.Create(ctx, Request{Kind: KindScheduled})
		if !errors.Is(err, ErrBusy) || attempt >= 20 {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(30 * time.Second):
		}
	}
	if err != nil {
		return err
	}
	return s.prune(ctx, sc.Keep)
}

// prune keeps the newest keep scheduled archives this server wrote.
// Archives registered from elsewhere keep their manifest's kind but are never
// pruned: they are the admin's to delete.
func (s *service) prune(ctx context.Context, keep int) error {
	rows, err := s.listRows(ctx, KindScheduled, 500)
	if err != nil {
		return err
	}
	kept := 0
	for _, r := range rows {
		if r.Status != StatusDone && r.Status != StatusRestored || r.includes().Uploaded {
			continue
		}
		kept++
		if kept > keep {
			if err := s.Delete(ctx, r.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

// sweep deletes expired archives and marks backups whose writer died as failed.
func (s *service) sweep(ctx context.Context) error {
	now := s.now()
	var ids []string
	if err := s.db.Select(ctx, &ids, `SELECT id FROM backups WHERE status IN ('done', 'restored', 'failed') AND expires_at IS NOT NULL AND expires_at < ?`, now); err != nil {
		return err
	}
	for _, id := range ids {
		if err := s.Delete(ctx, id); err != nil {
			s.log.Warn("backup: delete expired archive", "id", id, "err", err)
		}
	}
	// Member exports hold no lease: one still running after exportStale was interrupted.
	if _, err := s.db.Exec(ctx, `UPDATE backups SET status = 'failed', error = 'interrupted', finished_at = ? WHERE status = 'running'
		AND kind = 'user_export' AND created_at < ?`, now, now.Add(-exportStale)); err != nil {
		return err
	}
	if !s.busy.Load() {
		var held int
		if err := s.db.Get(ctx, &held, `SELECT COUNT(*) FROM instance_settings WHERE key = ? AND updated_at >= ?`, lockKey, now.Add(-lockStale)); err != nil {
			return err
		}
		if held == 0 {
			_, err := s.db.Exec(ctx, `UPDATE backups SET status = 'failed', error = 'interrupted', finished_at = ? WHERE status = 'running'
				AND kind <> 'user_export' AND created_at < ?`, now, now.Add(-lockStale))
			return err
		}
	}
	return nil
}

// ServerRunning reports whether a server process announced itself recently
// (the CLI refuses to restore under a running server).
func (s *service) ServerRunning(ctx context.Context) (bool, error) {
	var n int
	err := s.db.Get(ctx, &n, `SELECT COUNT(*) FROM instance_settings WHERE key LIKE ? AND updated_at >= ?`, presencePre+"%", s.now().Add(-90*time.Second))
	return n > 0, err
}

func (s *service) announce(ctx context.Context, key string) {
	_, err := s.db.Exec(ctx, `INSERT INTO instance_settings (key, value, updated_by, updated_at) VALUES (?, ?, NULL, ?)
		ON CONFLICT (key) DO UPDATE SET updated_at = excluded.updated_at`, key, db.MustJSON(map[string]int{"pid": os.Getpid()}), s.now())
	if err != nil {
		s.log.Debug("backup: announce server", "err", err)
	}
}

// loop runs scheduled backups, expiry and the server presence heartbeat.
func (s *service) loop(ctx context.Context) error {
	key := presencePre + db.NewID()
	s.announce(ctx, key)
	defer func() {
		bctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, _ = s.db.Exec(bctx, `DELETE FROM instance_settings WHERE key = ?`, key)
	}()
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
		s.announce(ctx, key)
		if err := s.runDue(ctx); err != nil && !errors.Is(err, context.Canceled) {
			s.log.Warn("backup: scheduled backup", "err", err)
		}
		if err := s.sweep(ctx); err != nil && !errors.Is(err, context.Canceled) {
			s.log.Warn("backup: sweep", "err", err)
		}
	}
}
