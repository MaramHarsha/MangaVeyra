package backup_test

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/backup"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

// A member cannot fill the disk or block the server with exports: one export
// runs per member at a time, a new one is made at most once an hour (the
// newest is served again in between), only the newest is kept, and exports
// never take the server-wide backup lease.
func TestMemberExportsAreLimitedPerMember(t *testing.T) {
	forEach(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		s := svcOf(t, ta)
		now := time.Now().UTC()
		s.SetNow(func() time.Time { return now })
		reader := ta.CreateUser(t, "tama", auth.RoleReader)

		// The server-wide lease does not stop an export, and an export does not take it.
		release, err := backup.NewTestService(ta.Deps).Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		first, err := s.MemberExport(ctx, reader.ID)
		release()
		if err != nil {
			t.Fatalf("export while a backup holds the lease: %v", err)
		}
		// Within the hour the newest export is served again.
		again, err := s.MemberExport(ctx, reader.ID)
		if err != nil || again.ID != first.ID {
			t.Fatalf("second export within the hour = %s, %v (want %s)", again.ID, err, first.ID)
		}
		var n int
		_ = ta.Deps.DB.Get(ctx, &n, `SELECT COUNT(*) FROM backups WHERE kind = 'user_export' AND user_id = ?`, reader.ID)
		if n != 1 {
			t.Fatalf("%d exports recorded", n)
		}
		// One export at a time: another process is still writing one.
		now = now.Add(61 * time.Minute)
		running := db.NewID()
		if _, err := ta.Deps.DB.Exec(ctx, `INSERT INTO backups (id, kind, status, user_id, created_at) VALUES (?, 'user_export', 'running', ?, ?)`,
			running, reader.ID, now.Add(-time.Minute)); err != nil {
			t.Fatal(err)
		}
		if _, err := s.MemberExport(ctx, reader.ID); !errors.Is(err, backup.ErrExportRunning) {
			t.Fatalf("export while one runs: %v", err)
		}
		c := ta.Login(t, "tama")
		r := c.Do(http.MethodPost, "/api/v1/export/me", strings.NewReader("scope=everything"), "application/x-www-form-urlencoded")
		if r.StatusCode != http.StatusSeeOther || !strings.Contains(string(c.Get("/settings/data").Body), "Your export is still being prepared") {
			t.Fatalf("export while one runs: %d", r.StatusCode)
		}
		if _, err := ta.Deps.DB.Exec(ctx, `UPDATE backups SET status = 'failed' WHERE id = ?`, running); err != nil {
			t.Fatal(err)
		}
		// After the hour a new export replaces the old one.
		oldPath := pathOf(t, ta, first.ID)
		next, err := s.MemberExport(ctx, reader.ID)
		if err != nil || next.ID == first.ID {
			t.Fatalf("export after the hour = %s, %v", next.ID, err)
		}
		if got, _ := s.Get(ctx, first.ID); got.Status != backup.StatusDeleted {
			t.Fatalf("the older export is %s, want deleted", got.Status)
		}
		if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
			t.Fatalf("the older export's file stayed: %v", err)
		}
		// Another member's exports are independent.
		if _, err := s.MemberExport(ctx, ta.Admin.ID); err != nil {
			t.Fatalf("another member's export: %v", err)
		}
	})
}
