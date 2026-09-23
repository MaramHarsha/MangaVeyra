// Package db is the database layer shared by every module: a thin sqlx wrapper
// that hides the two supported dialects (SQLite through modernc.org/sqlite and
// PostgreSQL through pgx), lowercase ULID ids, JSON columns, transactions and
// the embedded migration runner.
//
// Queries are written once with "?" placeholders. Every query method on DB and
// Tx rebinds them for the active dialect, so modules never branch on the
// dialect for ordinary SQL. RETURNING and ON CONFLICT work on both.
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// Dialect identifies the SQL dialect of a connection.
type Dialect string

// Supported dialects.
const (
	SQLite   Dialect = "sqlite"
	Postgres Dialect = "postgres"
)

// ErrNotFound is returned by helpers that look up a single row which does not exist.
// It wraps sql.ErrNoRows so errors.Is(err, sql.ErrNoRows) keeps working.
var ErrNotFound = fmt.Errorf("db: not found: %w", sql.ErrNoRows)

// Querier is implemented by *DB and *Tx. Repository code should accept a
// Querier so it runs both standalone and inside a transaction.
type Querier interface {
	Get(ctx context.Context, dest any, query string, args ...any) error
	Select(ctx context.Context, dest any, query string, args ...any) error
	Exec(ctx context.Context, query string, args ...any) (sql.Result, error)
	Query(ctx context.Context, query string, args ...any) (*sqlx.Rows, error)
	QueryRow(ctx context.Context, query string, args ...any) *sqlx.Row
	Rebind(query string) string
	Dialect() Dialect
}

// DB is a connection pool bound to one dialect.
type DB struct {
	// X is the underlying sqlx handle for advanced use (named queries, custom scanning).
	// Queries sent through X are NOT rebound automatically.
	X       *sqlx.DB
	dialect Dialect
	// writeMu serialises write transactions on SQLite so concurrent goroutines
	// queue in-process instead of spinning on SQLITE_BUSY.
	writeMu sync.Mutex
}

// Options tunes Open.
type Options struct {
	// MaxOpenConns caps the pool (0 = dialect default: 16 for Postgres, 8 for SQLite).
	MaxOpenConns int
	// BusyTimeout is the SQLite busy timeout (default 10s).
	BusyTimeout time.Duration
}

// Open connects to the database. dialect is "sqlite" or "postgres"; dsn is a
// file path (or ":memory:") for SQLite and a postgres:// URL for Postgres.
func Open(ctx context.Context, dialect, dsn string, opts Options) (*DB, error) {
	switch Dialect(dialect) {
	case SQLite:
		return openSQLite(ctx, dsn, opts)
	case Postgres:
		return openPostgres(ctx, dsn, opts)
	default:
		return nil, fmt.Errorf("db: unsupported dialect %q", dialect)
	}
}

func openSQLite(ctx context.Context, path string, opts Options) (*DB, error) {
	busy := opts.BusyTimeout
	if busy <= 0 {
		busy = 10 * time.Second
	}
	memory := path == ":memory:" || strings.HasPrefix(path, "file::memory:")
	if !memory {
		if dir := filepath.Dir(path); dir != "." && dir != "" {
			if err := os.MkdirAll(dir, 0o750); err != nil {
				return nil, fmt.Errorf("db: create %s: %w", dir, err)
			}
		}
	}
	q := url.Values{}
	q.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", busy.Milliseconds()))
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "synchronous(NORMAL)")
	if !memory {
		q.Add("_pragma", "journal_mode(WAL)")
	}
	// Every BeginTx issues BEGIN IMMEDIATE: a transaction takes the write lock up
	// front, so it waits (busy_timeout) instead of failing on a lock upgrade.
	q.Set("_txlock", "immediate")
	q.Set("_time_format", "sqlite")
	dsn := "file:" + path + "?" + q.Encode()
	if memory {
		// A shared-cache named in-memory DB so every pooled connection sees the same data.
		dsn = "file:mv-" + NewID() + "?mode=memory&cache=shared&" + q.Encode()
	}
	sqldb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("db: open sqlite: %w", err)
	}
	maxConns := opts.MaxOpenConns
	if maxConns <= 0 {
		maxConns = 8
	}
	sqldb.SetMaxOpenConns(maxConns)
	sqldb.SetMaxIdleConns(maxConns)
	sqldb.SetConnMaxIdleTime(5 * time.Minute)
	if err := sqldb.PingContext(ctx); err != nil {
		_ = sqldb.Close()
		return nil, fmt.Errorf("db: ping sqlite: %w", err)
	}
	return &DB{X: sqlx.NewDb(sqldb, "sqlite3"), dialect: SQLite}, nil
}

func openPostgres(ctx context.Context, dsn string, opts Options) (*DB, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("db: parse postgres url: %w", err)
	}
	if cfg.RuntimeParams == nil {
		cfg.RuntimeParams = map[string]string{}
	}
	// TIMESTAMP columns hold UTC; make CURRENT_TIMESTAMP defaults agree.
	cfg.RuntimeParams["timezone"] = "UTC"
	sqldb := stdlib.OpenDB(*cfg)
	maxConns := opts.MaxOpenConns
	if maxConns <= 0 {
		maxConns = 16
	}
	sqldb.SetMaxOpenConns(maxConns)
	sqldb.SetMaxIdleConns(maxConns / 2)
	sqldb.SetConnMaxLifetime(30 * time.Minute)
	if err := sqldb.PingContext(ctx); err != nil {
		_ = sqldb.Close()
		return nil, fmt.Errorf("db: ping postgres: %w", err)
	}
	return &DB{X: sqlx.NewDb(sqldb, "pgx"), dialect: Postgres}, nil
}

// Dialect returns the active dialect.
func (d *DB) Dialect() Dialect { return d.dialect }

// Rebind converts "?" placeholders to the dialect's bind style.
func (d *DB) Rebind(query string) string { return rebind(d.dialect, query) }

// Close closes the pool.
func (d *DB) Close() error { return d.X.Close() }

// Ping checks the connection.
func (d *DB) Ping(ctx context.Context) error { return d.X.PingContext(ctx) }

// Get scans a single row into dest (struct or scalar). It returns an error
// satisfying errors.Is(err, sql.ErrNoRows) when there is no row.
func (d *DB) Get(ctx context.Context, dest any, query string, args ...any) error {
	return d.X.GetContext(ctx, dest, d.Rebind(query), args...)
}

// Select scans all rows into dest (pointer to slice).
func (d *DB) Select(ctx context.Context, dest any, query string, args ...any) error {
	return d.X.SelectContext(ctx, dest, d.Rebind(query), args...)
}

// Exec runs a statement.
func (d *DB) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return d.X.ExecContext(ctx, d.Rebind(query), args...)
}

// Query runs a query returning rows.
func (d *DB) Query(ctx context.Context, query string, args ...any) (*sqlx.Rows, error) {
	return d.X.QueryxContext(ctx, d.Rebind(query), args...)
}

// QueryRow runs a query returning at most one row.
func (d *DB) QueryRow(ctx context.Context, query string, args ...any) *sqlx.Row {
	return d.X.QueryRowxContext(ctx, d.Rebind(query), args...)
}

// Tx is a transaction with the same auto-rebinding helpers as DB.
type Tx struct {
	X       *sqlx.Tx
	dialect Dialect
}

// Dialect returns the active dialect.
func (t *Tx) Dialect() Dialect { return t.dialect }

// Rebind converts "?" placeholders to the dialect's bind style.
func (t *Tx) Rebind(query string) string { return rebind(t.dialect, query) }

// Get scans a single row into dest.
func (t *Tx) Get(ctx context.Context, dest any, query string, args ...any) error {
	return t.X.GetContext(ctx, dest, t.Rebind(query), args...)
}

// Select scans all rows into dest.
func (t *Tx) Select(ctx context.Context, dest any, query string, args ...any) error {
	return t.X.SelectContext(ctx, dest, t.Rebind(query), args...)
}

// Exec runs a statement.
func (t *Tx) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return t.X.ExecContext(ctx, t.Rebind(query), args...)
}

// Query runs a query returning rows.
func (t *Tx) Query(ctx context.Context, query string, args ...any) (*sqlx.Rows, error) {
	return t.X.QueryxContext(ctx, t.Rebind(query), args...)
}

// QueryRow runs a query returning at most one row.
func (t *Tx) QueryRow(ctx context.Context, query string, args ...any) *sqlx.Row {
	return t.X.QueryRowxContext(ctx, t.Rebind(query), args...)
}

// WithTx runs fn inside a transaction, committing when fn returns nil and
// rolling back otherwise (also on panic, which is re-raised).
//
// On SQLite write transactions are serialised in-process and start with
// BEGIN IMMEDIATE. Do not use the *DB (instead of tx) for writes inside fn:
// on SQLite that would wait for the lock the transaction itself holds.
func (d *DB) WithTx(ctx context.Context, fn func(tx *Tx) error) (err error) {
	if d.dialect == SQLite {
		d.writeMu.Lock()
		defer d.writeMu.Unlock()
	}
	sqltx, err := d.X.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("db: begin: %w", err)
	}
	tx := &Tx{X: sqltx, dialect: d.dialect}
	defer func() {
		if p := recover(); p != nil {
			_ = sqltx.Rollback()
			panic(p)
		}
		if err != nil {
			_ = sqltx.Rollback()
			return
		}
		if cerr := sqltx.Commit(); cerr != nil {
			err = fmt.Errorf("db: commit: %w", cerr)
		}
	}()
	return fn(tx)
}

// IsNotFound reports whether err means "no row".
func IsNotFound(err error) bool { return errors.Is(err, sql.ErrNoRows) }

// IsUniqueViolation reports whether err is a unique / primary key constraint violation on either dialect.
func IsUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") || // SQLite
		strings.Contains(msg, "SQLSTATE 23505") || // Postgres unique_violation
		strings.Contains(msg, "duplicate key value")
}

// IsForeignKeyViolation reports whether err is a foreign key violation on either dialect.
func IsForeignKeyViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "FOREIGN KEY constraint failed") || strings.Contains(msg, "SQLSTATE 23503")
}

// Now returns the current time in UTC truncated to microseconds, the precision both dialects store.
func Now() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }

// In expands a "?" placeholder list for an IN clause: In("a IN (?)", []string{"x","y"})
// returns "a IN (?, ?)" and the flattened args. It wraps sqlx.In.
func In(query string, args ...any) (string, []any, error) { return sqlx.In(query, args...) }

func rebind(d Dialect, query string) string {
	if d != Postgres || !strings.Contains(query, "?") {
		return query
	}
	return sqlx.Rebind(sqlx.DOLLAR, query)
}
