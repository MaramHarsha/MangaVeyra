package update

import (
	"context"
	"encoding/json"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
)

// The install lock makes an update exclusive across every server process on
// the database. It is one instance_settings row (key update.lock) inserted
// with ON CONFLICT DO NOTHING, so exactly one inserter wins on SQLite and
// PostgreSQL. Its value never changes while held; the holder renews the lease
// by moving updated_at forward, and a lease older than lockTTL may be taken
// over (a crashed process).
const (
	lockTTL   = 2 * time.Minute
	lockRenew = 20 * time.Second
	// restartOwner hands the lock to the next process after the swap.
	restartOwner = "restart"
)

type lockValue struct {
	UpdateID string `json:"update_id"`
	Owner    string `json:"owner"`
}

func (l lockValue) encode() string { return mustJSON(l) }

// acquireLock takes the lock for updateID. It returns false when another
// live process holds it.
func (m *Module) acquireLock(ctx context.Context, v lockValue) (bool, error) {
	now := db.Now()
	res, err := m.d.DB.Exec(ctx, `INSERT INTO instance_settings (key, value, updated_at) VALUES (?, ?, ?) ON CONFLICT (key) DO NOTHING`,
		keyLock, v.encode(), now)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return true, nil
	}
	cur, at, ok, err := m.readLock(ctx)
	if err != nil || !ok {
		return false, err
	}
	if now.Sub(at) < lockTTL {
		return false, nil
	}
	return m.takeOverLock(ctx, cur, v)
}

// takeOverLock replaces an expired lease whose value was observed as old. The
// swap is conditional on both the observed value and the lease still being
// expired when it runs: a holder that renewed in between (updated_at moved
// forward) keeps its lease.
func (m *Module) takeOverLock(ctx context.Context, old string, v lockValue) (bool, error) {
	now := db.Now()
	res, err := m.d.DB.Exec(ctx, `UPDATE instance_settings SET value = ?, updated_at = ? WHERE key = ? AND value = ? AND updated_at < ?`,
		v.encode(), now, keyLock, old, now.Add(-lockTTL))
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// swapLock replaces the exact value old with v (a restart hand-off or an
// expired lease).
func (m *Module) swapLock(ctx context.Context, old string, v lockValue) (bool, error) {
	res, err := m.d.DB.Exec(ctx, `UPDATE instance_settings SET value = ?, updated_at = ? WHERE key = ? AND value = ?`,
		v.encode(), db.Now(), keyLock, old)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func (m *Module) readLock(ctx context.Context) (raw string, at time.Time, ok bool, err error) {
	var row struct {
		Value     string    `db:"value"`
		UpdatedAt time.Time `db:"updated_at"`
	}
	err = m.d.DB.Get(ctx, &row, `SELECT value, updated_at FROM instance_settings WHERE key = ?`, keyLock)
	if db.IsNotFound(err) {
		return "", time.Time{}, false, nil
	}
	if err != nil {
		return "", time.Time{}, false, err
	}
	return row.Value, row.UpdatedAt, true, nil
}

func decodeLock(raw string) lockValue {
	var v lockValue
	_ = json.Unmarshal([]byte(raw), &v)
	return v
}

// renewLock moves the lease forward while v is still the holder. A restore
// may have put back an older copy of the row; the value is identical, so the
// renewal keeps working.
func (m *Module) renewLock(ctx context.Context, v lockValue) (bool, error) {
	res, err := m.d.DB.Exec(ctx, `UPDATE instance_settings SET updated_at = ? WHERE key = ? AND value = ?`, db.Now(), keyLock, v.encode())
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	if n == 1 {
		return true, nil
	}
	// The row is missing (a restore from an archive without it): take it again.
	res, err = m.d.DB.Exec(ctx, `INSERT INTO instance_settings (key, value, updated_at) VALUES (?, ?, ?) ON CONFLICT (key) DO NOTHING`,
		keyLock, v.encode(), db.Now())
	if err != nil {
		return false, err
	}
	n, _ = res.RowsAffected()
	return n == 1, nil
}

func (m *Module) releaseLock(ctx context.Context, v lockValue) {
	if _, err := m.d.DB.Exec(context.WithoutCancel(ctx), `DELETE FROM instance_settings WHERE key = ? AND value = ?`, keyLock, v.encode()); err != nil {
		m.d.Log.Warn("update: release lock", "err", err)
	}
}

// holdLock renews the lease until ctx ends.
func (m *Module) holdLock(ctx context.Context, v lockValue) {
	t := time.NewTicker(lockRenew)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if ok, err := m.renewLock(ctx, v); err != nil || !ok {
				m.d.Log.Warn("update: renew lock", "ok", ok, "err", err)
			}
		}
	}
}
