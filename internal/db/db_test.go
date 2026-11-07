package db_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil/testdb"
)

func TestMigrationsApplyAndAreIdempotent(t *testing.T) {
	testdb.ForEach(t, func(t *testing.T, d *db.DB) {
		ctx := context.Background()
		again, err := d.Migrate(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(again) != 0 {
			t.Fatalf("second run applied %v", again)
		}
		st, err := d.MigrationStatuses(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(st) < 6 {
			t.Fatalf("expected at least 6 migrations, got %d", len(st))
		}
		for _, s := range st {
			if s.AppliedAt.IsZero() {
				t.Errorf("%s not applied", s.Version)
			}
		}
		// Every base table exists.
		for _, table := range []string{
			"users", "sessions", "invites", "password_resets", "api_tokens", "instance_settings", "user_prefs",
			"audit_log", "assets", "projects", "project_members", "arcs", "chapters", "scenes", "script_panels",
			"pages", "panels", "layers", "bubbles", "bubble_texts", "characters", "character_refs",
			"character_outfits", "character_relations", "setting_items", "templates", "versions", "branches",
			"comments", "continuity_issues", "glossaries", "providers", "models", "routing_pins",
			"routing_decisions", "model_stats", "spend_ledger", "workers", "jobs", "job_events", "series",
			"chapter_publications", "publication_pages", "series_creators", "follows", "user_follows",
			"read_progress", "reader_prefs", "bookmarks", "downloads", "read_events", "reports", "notifications",
			"notification_prefs", "push_subscriptions", "xp_events", "world_links", "world_server",
			"world_submissions", "world_library", "packs", "plugins", "backups", "updates", "imports",
			"page_doc_updates",
		} {
			var n int
			if err := d.Get(ctx, &n, "SELECT COUNT(*) FROM "+table); err != nil {
				t.Errorf("table %s: %v", table, err)
			}
		}
	})
}

func TestCRUDAcrossDialects(t *testing.T) {
	testdb.ForEach(t, func(t *testing.T, d *db.DB) {
		ctx := context.Background()
		uid := db.NewID()
		now := db.Now()
		if _, err := d.Exec(ctx, `INSERT INTO users (id, username, email, role, links, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			uid, "rin", "rin@example.org", "creator", db.MustJSON([]string{"https://example.org"}), now, now); err != nil {
			t.Fatal(err)
		}
		var u struct {
			ID         string                  `db:"id"`
			Username   string                  `db:"username"`
			Links      db.JSON[[]string]       `db:"links"`
			TOTP       bool                    `db:"totp_enabled"`
			CreatedAt  time.Time               `db:"created_at"`
			Onboarding db.JSON[map[string]any] `db:"onboarding"`
		}
		if err := d.Get(ctx, &u, `SELECT id, username, links, totp_enabled, created_at, onboarding FROM users WHERE id = ?`, uid); err != nil {
			t.Fatal(err)
		}
		if u.Username != "rin" || len(u.Links.V) != 1 || u.TOTP {
			t.Fatalf("round trip: %+v", u)
		}
		if !u.CreatedAt.Equal(now) {
			t.Fatalf("time round trip: got %v want %v", u.CreatedAt, now)
		}
		// Unique violation detection.
		_, err := d.Exec(ctx, `INSERT INTO users (id, username) VALUES (?, ?)`, db.NewID(), "rin")
		if !db.IsUniqueViolation(err) {
			t.Fatalf("expected unique violation, got %v", err)
		}
		// FK violation detection.
		_, err = d.Exec(ctx, `INSERT INTO sessions (id, user_id, token_hash, expires_at) VALUES (?, ?, ?, ?)`, db.NewID(), "missing", "h", now)
		if !db.IsForeignKeyViolation(err) {
			t.Fatalf("expected FK violation, got %v", err)
		}
		// ON CONFLICT upsert + RETURNING work on both dialects.
		var v string
		for _, val := range []string{`"a"`, `"b"`} {
			if err := d.Get(ctx, &v, `INSERT INTO instance_settings (key, value, updated_at) VALUES (?, ?, ?)
				ON CONFLICT (key) DO UPDATE SET value = excluded.value RETURNING value`, "k", val, now); err != nil {
				t.Fatal(err)
			}
		}
		if v != `"b"` {
			t.Fatalf("upsert returned %q", v)
		}
		// BLOB column round trip (BYTEA on Postgres) with cascade delete.
		pid := seedPage(t, d, uid)
		if _, err := d.Exec(ctx, `INSERT INTO page_doc_updates (id, page_id, data) VALUES (?, ?, ?)`, db.NewID(), pid, []byte{0, 1, 2, 255}); err != nil {
			t.Fatal(err)
		}
		var blob []byte
		if err := d.Get(ctx, &blob, `SELECT data FROM page_doc_updates WHERE page_id = ?`, pid); err != nil || len(blob) != 4 || blob[3] != 255 {
			t.Fatalf("blob round trip %v %v", blob, err)
		}
		if _, err := d.Exec(ctx, `DELETE FROM users WHERE id = ?`, uid); err != nil {
			t.Fatal(err)
		}
		var n int
		if err := d.Get(ctx, &n, `SELECT COUNT(*) FROM page_doc_updates`); err != nil || n != 0 {
			t.Fatalf("cascade: n=%d err=%v", n, err)
		}
		// IN expansion.
		q, args, err := db.In(`SELECT COUNT(*) FROM users WHERE id IN (?)`, []string{"a", "b"})
		if err != nil {
			t.Fatal(err)
		}
		if err := d.Get(ctx, &n, q, args...); err != nil {
			t.Fatal(err)
		}
		var missing string
		if err := d.Get(ctx, &missing, `SELECT id FROM users WHERE id = ?`, "nope"); !db.IsNotFound(err) {
			t.Fatalf("expected not found, got %v", err)
		}
	})
}

func seedPage(t *testing.T, d *db.DB, userID string) string {
	t.Helper()
	ctx := context.Background()
	proj, ch, page := db.NewID(), db.NewID(), db.NewID()
	stmts := []struct {
		q    string
		args []any
	}{
		{`INSERT INTO projects (id, owner_id, title, slug) VALUES (?, ?, ?, ?)`, []any{proj, userID, "Tidebreak Courier", "tidebreak-courier"}},
		{`INSERT INTO chapters (id, project_id, number, title) VALUES (?, ?, ?, ?)`, []any{ch, proj, 1, "Low Tide"}},
		{`INSERT INTO pages (id, project_id, chapter_id, number) VALUES (?, ?, ?, ?)`, []any{page, proj, ch, 1}},
	}
	for _, s := range stmts {
		if _, err := d.Exec(ctx, s.q, s.args...); err != nil {
			t.Fatal(err)
		}
	}
	return page
}

func TestWithTxRollbackAndCommit(t *testing.T) {
	testdb.ForEach(t, func(t *testing.T, d *db.DB) {
		ctx := context.Background()
		boom := errors.New("boom")
		err := d.WithTx(ctx, func(tx *db.Tx) error {
			if _, err := tx.Exec(ctx, `INSERT INTO instance_settings (key, value) VALUES (?, ?)`, "x", "1"); err != nil {
				return err
			}
			return boom
		})
		if !errors.Is(err, boom) {
			t.Fatalf("got %v", err)
		}
		var n int
		_ = d.Get(ctx, &n, `SELECT COUNT(*) FROM instance_settings WHERE key = ?`, "x")
		if n != 0 {
			t.Fatal("rollback did not happen")
		}
		if err := d.WithTx(ctx, func(tx *db.Tx) error {
			_, err := tx.Exec(ctx, `INSERT INTO instance_settings (key, value) VALUES (?, ?)`, "x", "1")
			return err
		}); err != nil {
			t.Fatal(err)
		}
		_ = d.Get(ctx, &n, `SELECT COUNT(*) FROM instance_settings WHERE key = ?`, "x")
		if n != 1 {
			t.Fatal("commit did not happen")
		}
	})
}

func TestConcurrentWriters(t *testing.T) {
	testdb.ForEach(t, func(t *testing.T, d *db.DB) {
		ctx := context.Background()
		if _, err := d.Exec(ctx, `INSERT INTO instance_settings (key, value) VALUES ('counter', '0')`); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		errs := make(chan error, 40)
		for i := 0; i < 40; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				errs <- d.WithTx(ctx, func(tx *db.Tx) error {
					var v string
					if err := tx.Get(ctx, &v, `SELECT value FROM instance_settings WHERE key = 'counter'`); err != nil {
						return err
					}
					n := 0
					for _, c := range v {
						n = n*10 + int(c-'0')
					}
					_, err := tx.Exec(ctx, `UPDATE instance_settings SET value = ? WHERE key = 'counter'`, itoa(n+1))
					return err
				})
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil && d.Dialect() == db.SQLite {
				t.Fatal(err)
			}
		}
		if d.Dialect() == db.SQLite {
			var v string
			_ = d.Get(ctx, &v, `SELECT value FROM instance_settings WHERE key = 'counter'`)
			if v != "40" {
				t.Fatalf("lost updates: %s", v)
			}
		}
	})
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func TestLoadMigrationsDialectSelection(t *testing.T) {
	fsys := fstest.MapFS{
		"0001_a.sql":          {Data: []byte("CREATE TABLE a (b BLOB);")},
		"0002_b.sql":          {Data: []byte("SELECT 1;")},
		"0002_b.postgres.sql": {Data: []byte("SELECT 2;")},
		"0003_c.sqlite.sql":   {Data: []byte("SELECT 3;")},
	}
	pg, err := db.LoadMigrations(fsys, db.Postgres)
	if err != nil {
		t.Fatal(err)
	}
	if len(pg) != 2 || !strings.Contains(pg[0].SQL, "BYTEA") || pg[1].SQL != "SELECT 2;" {
		t.Fatalf("postgres selection: %+v", pg)
	}
	lite, err := db.LoadMigrations(fsys, db.SQLite)
	if err != nil {
		t.Fatal(err)
	}
	if len(lite) != 3 || lite[1].SQL != "SELECT 1;" || !strings.Contains(lite[0].SQL, "BLOB") {
		t.Fatalf("sqlite selection: %+v", lite)
	}
	if _, err := db.LoadMigrations(fstest.MapFS{"bad.sql": {}}, db.SQLite); err == nil {
		t.Fatal("expected bad name error")
	}
}

func TestSplitStatements(t *testing.T) {
	script := `-- comment; with semicolon
CREATE TABLE t (a TEXT DEFAULT 'x;y');
/* block ; comment */
CREATE TRIGGER tr AFTER INSERT ON t BEGIN
  INSERT INTO u VALUES (1);
  INSERT INTO u VALUES (2);
END;
CREATE FUNCTION f() RETURNS int AS $$ SELECT 1; $$ LANGUAGE sql;
SELECT "semi;colon";`
	stmts, err := db.SplitStatements(script)
	if err != nil {
		t.Fatal(err)
	}
	if len(stmts) != 4 {
		for i, s := range stmts {
			t.Logf("%d: %s", i, s)
		}
		t.Fatalf("got %d statements", len(stmts))
	}
	if !strings.HasSuffix(stmts[1], "END;") {
		t.Fatalf("trigger split wrong: %s", stmts[1])
	}
}

func TestIDs(t *testing.T) {
	prev := ""
	for i := 0; i < 1000; i++ {
		id := db.NewID()
		if !db.ValidID(id) || id != strings.ToLower(id) || id <= prev {
			t.Fatalf("bad id sequence %q after %q", id, prev)
		}
		prev = id
	}
	ts, err := db.IDTime(prev)
	if err != nil || time.Since(ts) > time.Minute {
		t.Fatalf("IDTime %v %v", ts, err)
	}
}

func TestSearchIndex(t *testing.T) {
	testdb.ForEach(t, func(t *testing.T, d *db.DB) {
		ctx := context.Background()
		uid := db.NewID()
		if _, err := d.Exec(ctx, `INSERT INTO users (id, username) VALUES (?, ?)`, uid, "kaito"); err != nil {
			t.Fatal(err)
		}
		proj := db.NewID()
		if _, err := d.Exec(ctx, `INSERT INTO projects (id, owner_id, title, slug) VALUES (?, ?, ?, ?)`, proj, uid, "Glass Orchard", "glass-orchard"); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Exec(ctx, `INSERT INTO series (id, project_id, owner_id, slug, title, synopsis) VALUES (?, ?, ?, ?, ?, ?)`,
			db.NewID(), proj, uid, "glass-orchard", "Glass Orchard", "A lighthouse keeper grows glass apples"); err != nil {
			t.Fatal(err)
		}
		var n int
		var q string
		if d.Dialect() == db.SQLite {
			q = `SELECT COUNT(*) FROM series_fts WHERE series_fts MATCH ?`
		} else {
			q = `SELECT COUNT(*) FROM series WHERE to_tsvector('simple', title || ' ' || synopsis || ' ' || tags || ' ' || genres) @@ websearch_to_tsquery('simple', ?)`
		}
		if err := d.Get(ctx, &n, q, "lighthouse"); err != nil || n != 1 {
			t.Fatalf("search n=%d err=%v", n, err)
		}
	})
}

// WithReadTx statements share one snapshot: a write committed in between is
// not visible until the read transaction ends, and it does not block writers.
func TestWithReadTxSeesOneSnapshot(t *testing.T) {
	testdb.ForEach(t, func(t *testing.T, d *db.DB) {
		ctx := context.Background()
		count := func(q interface {
			Get(context.Context, any, string, ...any) error
		}) int {
			t.Helper()
			var n int
			if err := q.Get(ctx, &n, `SELECT COUNT(*) FROM instance_settings WHERE key LIKE ?`, "snapshot-%"); err != nil {
				t.Fatal(err)
			}
			return n
		}
		if _, err := d.Exec(ctx, `INSERT INTO instance_settings (key, value) VALUES (?, ?)`, "snapshot-1", "1"); err != nil {
			t.Fatal(err)
		}
		err := d.WithReadTx(ctx, func(tx *db.Tx) error {
			if n := count(tx); n != 1 {
				t.Fatalf("first read = %d, want 1", n)
			}
			if err := d.WithTx(ctx, func(w *db.Tx) error {
				_, err := w.Exec(ctx, `INSERT INTO instance_settings (key, value) VALUES (?, ?)`, "snapshot-2", "2")
				return err
			}); err != nil {
				t.Fatalf("concurrent write during read transaction: %v", err)
			}
			if n := count(tx); n != 1 {
				t.Fatalf("second read inside the transaction = %d, want the same snapshot (1)", n)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if n := count(d); n != 2 {
			t.Fatalf("read after the transaction = %d, want 2", n)
		}
	})
}
