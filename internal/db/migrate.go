package db

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strings"
	"time"
)

//go:embed migrations/*.sql
var embeddedMigrations embed.FS

// Migrations returns the embedded migration files.
func Migrations() fs.FS {
	sub, err := fs.Sub(embeddedMigrations, "migrations")
	if err != nil {
		panic(err)
	}
	return sub
}

// Migration is one migration file resolved for a dialect.
type Migration struct {
	// Version is the file name without the dialect suffix and extension, e.g. "0001_core".
	Version string
	// File is the file actually applied, e.g. "0005_search.sqlite.sql".
	File string
	SQL  string
}

// MigrationStatus pairs a migration with the time it was applied (zero if pending).
type MigrationStatus struct {
	Migration
	AppliedAt time.Time
}

var migrationName = regexp.MustCompile(`^(\d{4})_([a-z0-9_]+?)(?:\.(sqlite|postgres))?\.sql$`)

// LoadMigrations lists the migrations in fsys that apply to dialect, sorted
// by version. Portable files (NNNN_name.sql) apply to both dialects; files
// suffixed .sqlite.sql or .postgres.sql only to theirs. A dialect-specific file
// replaces a portable file of the same version.
func LoadMigrations(fsys fs.FS, dialect Dialect) ([]Migration, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("db: read migrations: %w", err)
	}
	byVersion := map[string]Migration{}
	specific := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		m := migrationName.FindStringSubmatch(e.Name())
		if m == nil {
			return nil, fmt.Errorf("db: bad migration file name %q (want NNNN_name[.sqlite|.postgres].sql)", e.Name())
		}
		version := m[1] + "_" + m[2]
		fileDialect := m[3]
		if fileDialect != "" && Dialect(fileDialect) != dialect {
			continue
		}
		if fileDialect == "" && specific[version] {
			continue
		}
		b, err := fs.ReadFile(fsys, e.Name())
		if err != nil {
			return nil, err
		}
		sqlText := string(b)
		if dialect == Postgres {
			sqlText = RewriteForPostgres(sqlText)
		}
		byVersion[version] = Migration{Version: version, File: e.Name(), SQL: sqlText}
		if fileDialect != "" {
			specific[version] = true
		}
	}
	out := make([]Migration, 0, len(byVersion))
	for _, m := range byVersion {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}

var blobWord = regexp.MustCompile(`(?i)\bBLOB\b`)

// RewriteForPostgres adapts portable DDL to Postgres: BLOB becomes BYTEA.
func RewriteForPostgres(sqlText string) string { return blobWord.ReplaceAllString(sqlText, "BYTEA") }

const migrationsTable = `CREATE TABLE IF NOT EXISTS schema_migrations (
	version TEXT PRIMARY KEY,
	file TEXT NOT NULL,
	applied_at TIMESTAMP NOT NULL
)`

// advisory lock key for concurrent migrators on Postgres ("mangaveyra" as int).
const pgMigrationLock = 7_468_326_819

// Migrate applies all pending embedded migrations. Each file runs in its own
// transaction together with its schema_migrations row. It returns the applied versions.
func (d *DB) Migrate(ctx context.Context) ([]string, error) {
	return d.MigrateFS(ctx, Migrations())
}

// MigrateFS applies pending migrations from fsys (used by tests and tools).
func (d *DB) MigrateFS(ctx context.Context, fsys fs.FS) ([]string, error) {
	migs, err := LoadMigrations(fsys, d.dialect)
	if err != nil {
		return nil, err
	}
	if d.dialect == Postgres {
		conn, err := d.X.Conn(ctx)
		if err != nil {
			return nil, err
		}
		defer conn.Close()
		if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_lock($1)", pgMigrationLock); err != nil {
			return nil, fmt.Errorf("db: migration lock: %w", err)
		}
		defer func() {
			_, _ = conn.ExecContext(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", pgMigrationLock)
		}()
	}
	if _, err := d.Exec(ctx, migrationsTable); err != nil {
		return nil, fmt.Errorf("db: create schema_migrations: %w", err)
	}
	applied, err := d.appliedVersions(ctx)
	if err != nil {
		return nil, err
	}
	var done []string
	for _, m := range migs {
		if _, ok := applied[m.Version]; ok {
			continue
		}
		stmts, err := SplitStatements(m.SQL)
		if err != nil {
			return done, fmt.Errorf("db: %s: %w", m.File, err)
		}
		err = d.WithTx(ctx, func(tx *Tx) error {
			for i, s := range stmts {
				if _, err := tx.X.ExecContext(ctx, s); err != nil {
					return fmt.Errorf("statement %d: %w\n%s", i+1, err, s)
				}
			}
			_, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version, file, applied_at) VALUES (?, ?, ?)`, m.Version, m.File, Now())
			return err
		})
		if err != nil {
			return done, fmt.Errorf("db: migrate %s: %w", m.File, err)
		}
		done = append(done, m.Version)
	}
	return done, nil
}

func (d *DB) appliedVersions(ctx context.Context) (map[string]time.Time, error) {
	rows := []struct {
		Version   string    `db:"version"`
		AppliedAt time.Time `db:"applied_at"`
	}{}
	if err := d.Select(ctx, &rows, `SELECT version, applied_at FROM schema_migrations`); err != nil {
		return nil, fmt.Errorf("db: read schema_migrations: %w", err)
	}
	out := make(map[string]time.Time, len(rows))
	for _, r := range rows {
		out[r.Version] = r.AppliedAt
	}
	return out, nil
}

// MigrationStatuses lists every embedded migration with its applied time.
func (d *DB) MigrationStatuses(ctx context.Context) ([]MigrationStatus, error) {
	migs, err := LoadMigrations(Migrations(), d.dialect)
	if err != nil {
		return nil, err
	}
	if _, err := d.Exec(ctx, migrationsTable); err != nil {
		return nil, err
	}
	applied, err := d.appliedVersions(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]MigrationStatus, 0, len(migs))
	for _, m := range migs {
		out = append(out, MigrationStatus{Migration: m, AppliedAt: applied[m.Version]})
	}
	return out, nil
}

// SplitStatements splits a SQL script on top-level semicolons. It understands
// single/double-quoted strings, -- and /* */ comments, Postgres dollar quotes
// and CREATE TRIGGER ... BEGIN ... END bodies.
func SplitStatements(script string) ([]string, error) {
	var (
		out       []string
		cur       strings.Builder
		inTrigger bool
	)
	flush := func() {
		s := strings.TrimSpace(cur.String())
		cur.Reset()
		inTrigger = false
		if s != "" && strings.Trim(stripComments(s), " \t\r\n;") != "" {
			out = append(out, s)
		}
	}
	n := len(script)
	for i := 0; i < n; i++ {
		c := script[i]
		switch {
		case c == '-' && i+1 < n && script[i+1] == '-':
			j := strings.IndexByte(script[i:], '\n')
			if j < 0 {
				j = n - i
			}
			cur.WriteString(script[i : i+j])
			i += j - 1
		case c == '/' && i+1 < n && script[i+1] == '*':
			j := strings.Index(script[i+2:], "*/")
			if j < 0 {
				return nil, fmt.Errorf("unterminated /* comment")
			}
			cur.WriteString(script[i : i+2+j+2])
			i += 2 + j + 1
		case c == '\'' || c == '"':
			j := i + 1
			for j < n {
				if script[j] == c {
					if j+1 < n && script[j+1] == c { // escaped quote
						j += 2
						continue
					}
					break
				}
				j++
			}
			if j >= n {
				return nil, fmt.Errorf("unterminated %c string", c)
			}
			cur.WriteString(script[i : j+1])
			i = j
		case c == '$':
			// dollar quote: $tag$ ... $tag$
			k := i + 1
			for k < n && (script[k] == '_' || isAlnum(script[k])) {
				k++
			}
			if k < n && script[k] == '$' {
				tag := script[i : k+1]
				end := strings.Index(script[k+1:], tag)
				if end < 0 {
					return nil, fmt.Errorf("unterminated dollar quote %s", tag)
				}
				stop := k + 1 + end + len(tag)
				cur.WriteString(script[i:stop])
				i = stop - 1
				continue
			}
			cur.WriteByte(c)
		case c == ';':
			cur.WriteByte(c)
			if !inTrigger {
				head := strings.ToUpper(strings.Join(strings.Fields(stripComments(cur.String())), " "))
				if strings.HasPrefix(head, "CREATE TRIGGER") || strings.HasPrefix(head, "CREATE TEMP TRIGGER") {
					if !strings.HasSuffix(strings.TrimSuffix(head, ";"), " END") {
						inTrigger = true
						continue
					}
				}
				flush()
				continue
			}
			// inside a trigger body: the statement ends at "END;"
			body := strings.ToUpper(strings.TrimSpace(stripComments(cur.String())))
			if strings.HasSuffix(strings.TrimSpace(strings.TrimSuffix(body, ";")), "END") {
				flush()
			}
		default:
			cur.WriteByte(c)
		}
	}
	flush()
	return out, nil
}

func isAlnum(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

func stripComments(s string) string {
	for {
		i := strings.Index(s, "/*")
		if i < 0 {
			break
		}
		j := strings.Index(s[i+2:], "*/")
		if j < 0 {
			s = s[:i]
			break
		}
		s = s[:i] + " " + s[i+2+j+2:]
	}
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}
