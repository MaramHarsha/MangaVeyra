package backup_test

import (
	"context"
	"database/sql"
	"io/fs"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/backup"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

// withAuditProbeMigration adds an N+1 migration that changes a kept table, so
// the rollback path must map the live rows' columns onto the restored schema.
func withAuditProbeMigration(t *testing.T) fs.FS {
	t.Helper()
	files := withProbeMigration(t).(fstest.MapFS)
	files["9901_audit_probe.sql"] = &fstest.MapFile{Data: []byte(`ALTER TABLE audit_log ADD COLUMN probe TEXT NOT NULL DEFAULT '';`)}
	return files
}

// A restore never rolls back history that describes this server rather than
// the archive: update attempts and the update.* settings stay as they are,
// the audit log keeps every entry written after the archive (and gains the
// restore entry), and spend recorded after the archive is kept so spend caps
// never count less than was spent.
func TestRestoreKeepsLiveUpdatesAuditAndSpend(t *testing.T) {
	for _, olderSchema := range []bool{false, true} {
		name := "same schema"
		if olderSchema {
			name = "after a newer migration"
		}
		t.Run(name, func(t *testing.T) {
			forEach(t, func(t *testing.T, ta *testutil.TestApp) {
				ctx := context.Background()
				f := seedServer(t, ta)
				s := svcOf(t, ta)
				d := ta.Deps.DB
				exec := func(q string, args ...any) {
					t.Helper()
					if _, err := d.Exec(ctx, q, args...); err != nil {
						t.Fatal(err)
					}
				}
				now := db.Now()
				exec(`INSERT INTO updates (id, from_version, to_version, status, created_at) VALUES ('upd-offer', '1.0.0', '1.0.1', 'available', ?)`, now)
				exec(`INSERT INTO instance_settings (key, value, updated_at) VALUES ('update.config', '{"checks":false}', ?)`, now)
				exec(`INSERT INTO audit_log (id, action, created_at) VALUES ('aud-before', 'test.before', ?)`, now)
				b, err := s.Create(ctx, backup.Request{Kind: backup.KindPreUpdate})
				if err != nil {
					t.Fatal(err)
				}
				if olderSchema {
					if _, err := d.MigrateFS(ctx, withAuditProbeMigration(t)); err != nil {
						t.Fatal(err)
					}
				}
				later := now.Add(time.Minute)
				exec(`UPDATE updates SET status = 'rolled_back', started_at = ?, steps = '{"mode":"binary"}' WHERE id = 'upd-offer'`, later)
				exec(`INSERT INTO updates (id, from_version, to_version, status, backup_id, started_by, started_at, created_at)
					VALUES ('upd-attempt', '1.0.0', '1.0.2', 'migrating', ?, ?, ?, ?)`, b.ID, ta.Admin.ID, later, later)
				exec(`UPDATE instance_settings SET value = '{"checks":true}' WHERE key = 'update.config'`)
				exec(`INSERT INTO instance_settings (key, value, updated_at) VALUES ('update.lock', '{"update_id":"upd-attempt","owner":"p1"}', ?)`, later)
				exec(`INSERT INTO audit_log (id, actor_id, action, created_at) VALUES ('aud-after', ?, 'update.start', ?)`, f.member.ID, later)
				exec(`INSERT INTO spend_ledger (id, user_id, provider_id, kind, usd, month, created_at) VALUES ('spend-after', ?, 'fal', 'remote', 4.5, '2026-11', ?)`,
					f.member.ID, later)
				exec(`INSERT INTO spend_reservations (id, job_id, attempt, call_seq, purpose, user_id, provider_id, model_id, provider_scope, month,
					estimate_microusd, expires_at, created_at) VALUES ('resv-after', 'job-gone', 1, 1, 'initial', ?, 'fal', 'flux', 'server', '2026-11', 4500000, ?, ?)`,
					f.member.ID, later.Add(time.Hour), later)
				// Spend of a member the archive does not have is kept with the member cleared.
				ghost := ta.CreateUser(t, "ghost", auth.RoleCreator)
				exec(`INSERT INTO spend_ledger (id, user_id, provider_id, kind, usd, month, created_at) VALUES ('spend-ghost', ?, 'fal', 'remote', 1.25, '2026-11', ?)`,
					ghost.ID, later)
				maintenance(t, ta, true)
				if _, err := s.RestoreArchive(ctx, b.ID, ta.Admin.ID); err != nil {
					t.Fatal(err)
				}
				var updates []struct {
					ID     string `db:"id"`
					Status string `db:"status"`
				}
				if err := d.Select(ctx, &updates, `SELECT id, status FROM updates ORDER BY id`); err != nil {
					t.Fatal(err)
				}
				if len(updates) != 2 || updates[0].ID != "upd-attempt" || updates[0].Status != "migrating" || updates[1].Status != "rolled_back" {
					t.Fatalf("updates after the restore = %+v", updates)
				}
				var cfg, lock string
				_ = d.Get(ctx, &cfg, `SELECT value FROM instance_settings WHERE key = 'update.config'`)
				_ = d.Get(ctx, &lock, `SELECT value FROM instance_settings WHERE key = 'update.lock'`)
				if cfg != `{"checks":true}` || !strings.Contains(lock, "upd-attempt") {
					t.Fatalf("update settings after the restore: config %s lock %s", cfg, lock)
				}
				var audit []string
				if err := d.Select(ctx, &audit, `SELECT action FROM audit_log WHERE id IN ('aud-before', 'aud-after') OR action = 'backup.restore'
					ORDER BY action`); err != nil {
					t.Fatal(err)
				}
				if !slices.Equal(audit, []string{"backup.restore", "test.before", "update.start"}) {
					t.Fatalf("audit log after the restore = %v", audit)
				}
				var spend []struct {
					ID   string         `db:"id"`
					User sql.NullString `db:"user_id"`
				}
				if err := d.Select(ctx, &spend, `SELECT id, user_id FROM spend_ledger WHERE id LIKE 'spend-%' ORDER BY id`); err != nil {
					t.Fatal(err)
				}
				if len(spend) != 2 || spend[0].ID != "spend-after" || spend[0].User.String != f.member.ID || spend[1].User.Valid {
					t.Fatalf("spend ledger after the restore = %+v", spend)
				}
				var resv int
				_ = d.Get(ctx, &resv, `SELECT COUNT(*) FROM spend_reservations WHERE id = 'resv-after' AND estimate_microusd = 4500000`)
				if resv != 1 {
					t.Fatal("a spend reservation written after the archive was rolled back")
				}
			})
		})
	}
}
