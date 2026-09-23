// Package testdb opens throwaway databases for tests: a temp-file SQLite
// database always, and a fresh Postgres database per test when
// MV_TEST_POSTGRES_URL points at a server the tests may create databases on.
//
// It only depends on internal/db, so low-level packages can use it from their
// external (_test) packages without pulling in the whole application.
package testdb

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
)

// PostgresEnv is the environment variable holding the Postgres admin URL for tests.
const PostgresEnv = "MV_TEST_POSTGRES_URL"

// Target is one database a test can run against.
type Target struct {
	Dialect string // "sqlite" or "postgres"
	DSN     string // driver DSN (file path or postgres URL)
	// URL is the MV_DB form of the DSN (sqlite:///path or postgres://...).
	URL string
}

// Dialects returns the dialects available to tests: always sqlite, plus postgres when MV_TEST_POSTGRES_URL is set.
func Dialects() []string {
	if os.Getenv(PostgresEnv) != "" {
		return []string{"sqlite", "postgres"}
	}
	return []string{"sqlite"}
}

// NewTarget creates an empty database of the given dialect and removes it when the test ends.
// Postgres targets are skipped when MV_TEST_POSTGRES_URL is not set.
func NewTarget(t testing.TB, dialect string) Target {
	t.Helper()
	switch dialect {
	case "sqlite":
		p := filepath.Join(t.TempDir(), "test.db")
		return Target{Dialect: "sqlite", DSN: p, URL: "sqlite://" + p}
	case "postgres":
		admin := os.Getenv(PostgresEnv)
		if admin == "" {
			t.Skip("MV_TEST_POSTGRES_URL not set")
		}
		name := "mvtest_" + strings.ToLower(db.NewID())
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		adb, err := db.Open(ctx, "postgres", admin, db.Options{MaxOpenConns: 2})
		if err != nil {
			t.Fatalf("testdb: connect admin postgres: %v", err)
		}
		if _, err := adb.X.ExecContext(ctx, `CREATE DATABASE `+name); err != nil {
			_ = adb.Close()
			t.Fatalf("testdb: create database: %v", err)
		}
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if _, err := adb.X.ExecContext(ctx, `DROP DATABASE IF EXISTS `+name+` WITH (FORCE)`); err != nil {
				t.Logf("testdb: drop %s: %v", name, err)
			}
			_ = adb.Close()
		})
		u, err := url.Parse(admin)
		if err != nil {
			t.Fatalf("testdb: parse %s: %v", PostgresEnv, err)
		}
		u.Path = "/" + name
		return Target{Dialect: "postgres", DSN: u.String(), URL: u.String()}
	default:
		t.Fatalf("testdb: unknown dialect %q", dialect)
		return Target{}
	}
}

// Open creates, opens and fully migrates a database of the given dialect.
func Open(t testing.TB, dialect string) *db.DB {
	t.Helper()
	tg := NewTarget(t, dialect)
	ctx := context.Background()
	d, err := db.Open(ctx, tg.Dialect, tg.DSN, db.Options{})
	if err != nil {
		t.Fatalf("testdb: open %s: %v", dialect, err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if _, err := d.Migrate(ctx); err != nil {
		t.Fatalf("testdb: migrate %s: %v", dialect, err)
	}
	return d
}

// ForEach runs fn as a subtest against every available dialect with a fresh, migrated database.
func ForEach(t *testing.T, fn func(t *testing.T, d *db.DB)) {
	t.Helper()
	for _, dialect := range Dialects() {
		t.Run(dialect, func(t *testing.T) { fn(t, Open(t, dialect)) })
	}
}
