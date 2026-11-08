package analytics

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
)

// saltTable holds one random salt per UTC day for anonymous session hashes
// (proposed migration 0403, see testdata/0403_analytics_day_salts.sql).
const saltTable = "analytics_day_salts"

// daySalts hands out the random salt of a UTC day (ADR 0012 §8). The first
// server process to need a day's salt inserts a random one (INSERT … ON
// CONFLICT (day) DO NOTHING) and every process reads the stored row back, so
// hashes agree across processes and restarts within the day. Salts of
// finished days are deleted, from the table and from memory, so a past day's
// hashes can no longer be recomputed, not even with the master key and a
// visitor's cookie.
//
// Until the table exists (migration 0403) the salt is random and kept only in
// this process's memory: still never derivable, only not shared between
// processes or restarts.
type daySalts struct {
	db *db.DB

	mu   sync.Mutex
	day  string
	salt []byte
}

// get returns the salt of t's UTC day.
func (s *daySalts) get(ctx context.Context, t time.Time) ([]byte, error) {
	day := t.UTC().Format(time.DateOnly)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.day == day {
		return s.salt, nil
	}
	salt, err := s.load(ctx, day)
	if err != nil {
		return nil, err
	}
	s.day, s.salt = day, salt
	return salt, nil
}

func (s *daySalts) load(ctx context.Context, day string) ([]byte, error) {
	fresh := make([]byte, 32)
	if _, err := rand.Read(fresh); err != nil {
		return nil, fmt.Errorf("analytics: day salt: %w", err)
	}
	if ok, err := s.hasTable(ctx); err != nil {
		return nil, err
	} else if !ok {
		return fresh, nil
	}
	if _, err := s.db.Exec(ctx, `INSERT INTO `+saltTable+` (day, salt, created_at) VALUES (?, ?, ?) ON CONFLICT (day) DO NOTHING`,
		day, hex.EncodeToString(fresh), db.Now()); err != nil {
		return nil, fmt.Errorf("analytics: store day salt: %w", err)
	}
	var stored string
	if err := s.db.Get(ctx, &stored, `SELECT salt FROM `+saltTable+` WHERE day = ?`, day); err != nil {
		return nil, fmt.Errorf("analytics: read day salt: %w", err)
	}
	salt, err := hex.DecodeString(stored)
	if err != nil || len(salt) < 16 {
		return nil, fmt.Errorf("analytics: day salt of %s is malformed", day)
	}
	if _, err := s.db.Exec(ctx, `DELETE FROM `+saltTable+` WHERE day < ?`, day); err != nil {
		return nil, fmt.Errorf("analytics: delete old day salts: %w", err)
	}
	return salt, nil
}

// purge deletes the salts of days before now's UTC day and forgets a cached
// one, so a finished day's hashes cannot be recomputed even when no reader
// came by since midnight.
func (s *daySalts) purge(ctx context.Context, now time.Time) error {
	day := now.UTC().Format(time.DateOnly)
	s.mu.Lock()
	if s.day != "" && s.day < day {
		s.day, s.salt = "", nil
	}
	s.mu.Unlock()
	if ok, err := s.hasTable(ctx); err != nil || !ok {
		return err
	}
	if _, err := s.db.Exec(ctx, `DELETE FROM `+saltTable+` WHERE day < ?`, day); err != nil {
		return fmt.Errorf("analytics: delete old day salts: %w", err)
	}
	return nil
}

// hasTable reports whether the salts table exists; err is set when the
// database itself does not answer (so an outage never switches a process to
// a salt of its own). The probes run outside any transaction so a missing
// table cannot abort one on PostgreSQL.
func (s *daySalts) hasTable(ctx context.Context) (bool, error) {
	var n int
	if s.db.Get(ctx, &n, `SELECT COUNT(*) FROM `+saltTable+` WHERE 1 = 0`) == nil {
		return true, nil
	}
	if err := s.db.Get(ctx, &n, `SELECT 1`); err != nil {
		return false, fmt.Errorf("analytics: day salt: %w", err)
	}
	return false, nil
}
