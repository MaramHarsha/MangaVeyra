package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/admin/adminapi"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
)

// Operations that hold the server (HoldRequest.What).
const (
	HoldUpdate  = adminapi.HoldUpdate
	HoldRestore = adminapi.HoldRestore
)

// HoldRequest asks for the server for an update or a restore.
type HoldRequest struct {
	// What is HoldUpdate or HoldRestore.
	What string
	// Message is what members read on the maintenance page while the hold
	// lasts; "" keeps the current message when maintenance is already on.
	Message string
	// ActorID is the admin taking the hold ("" for the scheduler).
	ActorID string
	// Wait bounds how long Hold waits for a running backup or restore to
	// finish (0 refuses at once with ErrBusy).
	Wait time.Duration
	// Log receives a progress line while Hold waits.
	Log func(string)
	// Resume is the id of a hold an earlier process took for the same
	// operation (an update interrupted by a crash); it is taken over even
	// while its lease is live.
	Resume string
}

// HoldInfo describes a hold.
type HoldInfo struct {
	ID    string    `json:"holder"`
	What  string    `json:"what"`
	Since time.Time `json:"since"`
	// Before is the maintenance setting from before the hold.
	Before settings.Maintenance `json:"before"`
}

// Lease is a running hold.
type Lease interface {
	Info() HoldInfo
	// Context marks ctx so Create and Restore run under the hold: they take
	// no lease of their own and a restore needs no maintenance check.
	Context(ctx context.Context) context.Context
	// Release ends the hold and sets maintenance to m (nil leaves it as it
	// is). It is safe to call more than once.
	Release(ctx context.Context, m *settings.Maintenance) error
	// Detach stops renewing the hold without ending it: the restarted server
	// ends it with EndHold, or its lease runs out.
	Detach()
}

// Holder runs updates and restores exclusively (ADR 0013 §3, §6). A hold is
// the backup lease (no backup or other restore starts while it lasts) plus
// ownership of maintenance mode: maintenance is on, the G5 toggle refuses to
// change it, and the job queue claims no new work (ClaimsAllowed).
type Holder interface {
	Hold(ctx context.Context, req HoldRequest) (Lease, error)
	// EndHold ends hold id (taken by an earlier process: the update the
	// restarted server finishes) and sets maintenance to m (nil leaves it).
	EndHold(ctx context.Context, id string, m *settings.Maintenance) error
	// Pending names backup work an automatic update should let run first: a
	// backup or restore holding the lease, or a scheduled backup that is due
	// and has not started yet.
	Pending(ctx context.Context, now time.Time) (string, bool)
}

// lockState is the value of the backup lease row (lockKey). Regular backups
// write holder and what; a hold also owns maintenance mode.
type lockState struct {
	Holder      string                `json:"holder"`
	What        string                `json:"what"`
	Maintenance bool                  `json:"maintenance,omitempty"`
	Since       time.Time             `json:"since,omitzero"`
	Before      *settings.Maintenance `json:"before,omitempty"`
	// Gen changes whenever a hold is taken over, so the previous holder's
	// renewals stop matching.
	Gen string `json:"gen,omitempty"`
}

func decodeLockState(raw string) lockState {
	var st lockState
	_ = json.Unmarshal([]byte(raw), &st)
	return st
}

// lockMaintenance serialises every write of maintenance mode against the
// holds on PostgreSQL (one advisory lock); SQLite write transactions are
// serialised already.
func lockMaintenance(ctx context.Context, tx *db.Tx) error {
	if tx.Dialect() != db.Postgres {
		return nil
	}
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(7468326831)`)
	return err
}

// leaseRow reads the backup lease row.
func leaseRow(ctx context.Context, q db.Querier) (raw string, at time.Time, ok bool, err error) {
	var row struct {
		Value     string    `db:"value"`
		UpdatedAt time.Time `db:"updated_at"`
	}
	err = q.Get(ctx, &row, `SELECT value, updated_at FROM instance_settings WHERE key = ?`, lockKey)
	if db.IsNotFound(err) {
		return "", time.Time{}, false, nil
	}
	if err != nil {
		return "", time.Time{}, false, err
	}
	return row.Value, row.UpdatedAt, true, nil
}

// heldMaintenance returns the hold that owns maintenance mode now, if any.
func (s *service) heldMaintenance(ctx context.Context, q db.Querier) (lockState, bool, error) {
	raw, at, ok, err := leaseRow(ctx, q)
	if err != nil || !ok {
		return lockState{}, false, err
	}
	st := decodeLockState(raw)
	if !st.Maintenance || at.Before(s.now().Add(-lockStale)) {
		return lockState{}, false, nil
	}
	return st, true, nil
}

func writeMaintenance(ctx context.Context, tx *db.Tx, m settings.Maintenance, by any, now time.Time) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO instance_settings (key, value, updated_by, updated_at) VALUES ('maintenance', ?, ?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_by = excluded.updated_by, updated_at = excluded.updated_at`,
		string(b), by, now)
	return err
}

// leaseKey marks a context that runs under a hold.
type leaseKey struct{}

func (s *service) leaseFrom(ctx context.Context) *lease {
	l, _ := ctx.Value(leaseKey{}).(*lease)
	if l == nil || l.s != s || l.ended() {
		return nil
	}
	return l
}

type lease struct {
	s    *service
	info HoldInfo
	raw  string

	mu   sync.Mutex
	stop chan struct{}
	done chan struct{}
	end  bool
}

func (l *lease) Info() HoldInfo { return l.info }

func (l *lease) Context(ctx context.Context) context.Context {
	return context.WithValue(ctx, leaseKey{}, l)
}

func (l *lease) ended() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.end
}

// stopRenewing ends the heartbeat once; it reports whether this call did.
func (l *lease) stopRenewing() bool {
	l.mu.Lock()
	if l.end {
		l.mu.Unlock()
		return false
	}
	l.end = true
	l.mu.Unlock()
	close(l.stop)
	<-l.done
	l.s.hold.CompareAndSwap(l, nil)
	l.s.busy.Store(false)
	return true
}

func (l *lease) Detach() { l.stopRenewing() }

func (l *lease) Release(ctx context.Context, m *settings.Maintenance) error {
	if !l.stopRenewing() {
		return nil
	}
	return l.s.endHold(context.WithoutCancel(ctx), l.raw, m)
}

func (l *lease) renew(ctx context.Context) {
	defer close(l.done)
	t := time.NewTicker(lockBeat)
	defer t.Stop()
	for {
		select {
		case <-l.stop:
			return
		case <-t.C:
			bctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			_, _ = l.s.db.Exec(bctx, `UPDATE instance_settings SET updated_at = ? WHERE key = ? AND value = ?`, l.s.now(), lockKey, l.raw)
			cancel()
		}
	}
}

// Hold implements Holder: it waits up to req.Wait for running backup work,
// then takes the backup lease and turns maintenance mode on, in one
// transaction with the maintenance setting.
func (s *service) Hold(ctx context.Context, req HoldRequest) (Lease, error) {
	if req.What != HoldUpdate && req.What != HoldRestore {
		return nil, ErrInvalid
	}
	deadline := time.Now().Add(req.Wait)
	var said time.Time
	for {
		l, what, err := s.tryHold(ctx, req)
		if !errors.Is(err, ErrBusy) || !time.Now().Before(deadline) {
			if err != nil {
				return nil, err
			}
			return l, nil
		}
		if req.Log != nil && time.Since(said) >= time.Minute {
			req.Log(waitingLine(what))
			said = time.Now()
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(s.holdPoll):
		}
	}
}

func waitingLine(what string) string {
	switch what {
	case HoldRestore:
		return "Waiting for a running restore to finish."
	case HoldUpdate:
		return "Waiting for a running update to finish."
	case "":
		return "Waiting for a running backup to finish."
	}
	return fmt.Sprintf("Waiting for a running %s backup to finish.", what)
}

// tryHold takes the hold once; on ErrBusy it also names what runs.
func (s *service) tryHold(ctx context.Context, req HoldRequest) (*lease, string, error) {
	if !s.busy.CompareAndSwap(false, true) {
		what := "backup"
		if l := s.hold.Load(); l != nil {
			what = l.info.What
		} else if s.State().Running {
			what = "restore"
		}
		return nil, what, ErrBusy
	}
	by := s.userOrNull(ctx, req.ActorID)
	now := s.now()
	l := &lease{s: s, info: HoldInfo{ID: db.NewID(), What: req.What, Since: now}, stop: make(chan struct{}), done: make(chan struct{})}
	busyWith := ""
	err := s.db.WithTx(ctx, func(tx *db.Tx) error {
		if err := lockMaintenance(ctx, tx); err != nil {
			return err
		}
		inst, err := s.d.Settings.InstanceTx(ctx, tx)
		if err != nil {
			return err
		}
		l.info.Before = inst.Maintenance
		raw, at, ok, err := leaseRow(ctx, tx)
		if err != nil {
			return err
		}
		resumed := ""
		if ok {
			cur := decodeLockState(raw)
			switch {
			case req.Resume != "" && cur.Holder == req.Resume && cur.Maintenance:
				resumed = raw
				l.info.ID, l.info.Since = cur.Holder, cur.Since
				if cur.Before != nil {
					l.info.Before = *cur.Before
				}
			case !at.Before(now.Add(-lockStale)):
				busyWith = cur.What
				return ErrBusy
			}
		}
		before := l.info.Before
		l.raw = db.MustJSON(lockState{Holder: l.info.ID, What: req.What, Maintenance: true, Since: l.info.Since, Before: &before, Gen: db.NewID()})
		// Insert the lease, or replace an expired one or the hold being resumed:
		// the condition is evaluated against the row as committed, so a lease
		// another process took in the meantime is never overwritten.
		res, err := tx.Exec(ctx, `INSERT INTO instance_settings (key, value, updated_by, updated_at) VALUES (?, ?, NULL, ?)
			ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at
			WHERE instance_settings.updated_at < ? OR instance_settings.value = ?`, lockKey, l.raw, now, now.Add(-lockStale), resumed)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return ErrBusy
		}
		msg := req.Message
		if msg == "" && before.Enabled {
			msg = before.Message
		}
		return writeMaintenance(ctx, tx, settings.Maintenance{Enabled: true, Message: msg}, by, now)
	})
	if err != nil {
		s.busy.Store(false)
		if errors.Is(err, ErrBusy) {
			return nil, busyWith, ErrBusy
		}
		return nil, "", fmt.Errorf("backup: take the server for the %s: %w", req.What, err)
	}
	s.d.Settings.Invalidate()
	s.hold.Store(l)
	go l.renew(ctx)
	return l, "", nil
}

// endHold deletes the lease row raw and sets maintenance to m. When the lease
// was lost (it ran out and another operation holds the server now), the
// maintenance setting is left to that operation.
func (s *service) endHold(ctx context.Context, raw string, m *settings.Maintenance) error {
	err := s.db.WithTx(ctx, func(tx *db.Tx) error {
		if err := lockMaintenance(ctx, tx); err != nil {
			return err
		}
		res, err := tx.Exec(ctx, `DELETE FROM instance_settings WHERE key = ? AND value = ?`, lockKey, raw)
		if err != nil {
			return err
		}
		if m == nil {
			return nil
		}
		if n, _ := res.RowsAffected(); n == 0 {
			if _, held, err := s.heldMaintenance(ctx, tx); err != nil || held {
				return err
			}
		}
		return writeMaintenance(ctx, tx, *m, nil, s.now())
	})
	s.d.Settings.Invalidate()
	if err != nil {
		return fmt.Errorf("backup: end the hold: %w", err)
	}
	return nil
}

// EndHold implements Holder.
func (s *service) EndHold(ctx context.Context, id string, m *settings.Maintenance) error {
	raw, _, ok, err := leaseRow(ctx, s.db)
	if err != nil {
		return err
	}
	if !ok || decodeLockState(raw).Holder != id {
		raw = "" // the hold already ended or ran out: only the maintenance setting is left
	}
	return s.endHold(ctx, raw, m)
}

// Pending implements Holder.
func (s *service) Pending(ctx context.Context, now time.Time) (string, bool) {
	if raw, at, ok, err := leaseRow(ctx, s.db); err == nil && ok && !at.Before(s.now().Add(-lockStale)) {
		if what := decodeLockState(raw).What; what != "" {
			return what, true
		}
		return "backup", true
	}
	sc, err := s.Schedule(ctx)
	if err != nil {
		return "", false
	}
	slot, ok := sc.Slot(now)
	if !ok || slot.Before(sc.UpdatedAt) || now.Sub(slot) >= time.Hour {
		return "", false
	}
	var claimed string
	if _, err := s.d.Settings.GetKey(ctx, slotKey, &claimed); err != nil {
		return "", false
	}
	if t, err := time.Parse(time.RFC3339, claimed); err == nil && !t.Before(slot) {
		return "", false // this slot's backup started (and holds the lease while it runs)
	}
	return KindScheduled, true
}

// SetMaintenance implements adminapi.MaintenanceGuard: the admin's change is
// written in the same transaction that checks no update or restore owns
// maintenance mode.
func (s *service) SetMaintenance(ctx context.Context, by string, m settings.Maintenance) error {
	who := s.userOrNull(ctx, by)
	err := s.db.WithTx(ctx, func(tx *db.Tx) error {
		if err := lockMaintenance(ctx, tx); err != nil {
			return err
		}
		st, held, err := s.heldMaintenance(ctx, tx)
		if err != nil {
			return err
		}
		if held {
			return &adminapi.MaintenanceHeldError{What: st.What, Since: st.Since}
		}
		return writeMaintenance(ctx, tx, m, who, s.now())
	})
	s.d.Settings.Invalidate()
	return err
}

// ClaimsAllowed reports whether the job queue may hand out work: not while an
// update or a restore holds the server, so nothing it would roll back is
// started. It runs inside the claim's transaction.
func (s *service) ClaimsAllowed(ctx context.Context, tx *db.Tx) (bool, error) {
	_, held, err := s.heldMaintenance(ctx, tx)
	return !held, err
}
