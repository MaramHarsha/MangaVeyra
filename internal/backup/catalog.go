package backup

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
)

// Logical column types. Archives store values by logical type so a SQLite
// archive restores on PostgreSQL and back (ADR 0013 §4).
const (
	typeText  = "text"
	typeInt   = "int"
	typeFloat = "float"
	typeBool  = "bool"
	typeTime  = "time"
	typeBlob  = "blob"
)

// column is one table column with its logical type.
type column struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Nullable bool   `json:"nullable,omitempty"`
}

// table is one base table of the schema.
type table struct {
	Name    string
	Columns []column
	PK      []string
	// Refs are the tables this table references through foreign keys (FK column → table).
	Refs map[string]string
}

func (t *table) column(name string) (column, bool) {
	for _, c := range t.Columns {
		if c.Name == name {
			return c, true
		}
	}
	return column{}, false
}

// catalog is the schema of one database: base tables in foreign-key-safe order
// (referenced tables first) and the FK columns that must be filled in after
// every row exists (self references and cycles).
type catalog struct {
	Tables   map[string]*table
	Order    []string
	Deferred map[string][]string
	// Virtual lists SQLite virtual tables (the FTS index), dropped and recreated by migrations.
	Virtual []string
}

// metaTable is the migration runner's own table: archives record the schema
// version in the manifest instead, because file names differ per dialect.
const metaTable = "schema_migrations"

var identPattern = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

func quote(ident string) string { return `"` + ident + `"` }

// loadCatalog reads the base tables, columns, primary keys and foreign keys.
func loadCatalog(ctx context.Context, q db.Querier) (*catalog, error) {
	c := &catalog{Tables: map[string]*table{}, Deferred: map[string][]string{}}
	var names []string
	switch q.Dialect() {
	case db.SQLite:
		var rows []struct {
			Name string `db:"name"`
			Type string `db:"type"`
		}
		if err := q.Select(ctx, &rows, `SELECT name, type FROM pragma_table_list WHERE schema = 'main' AND name NOT LIKE 'sqlite_%'`); err != nil {
			return nil, fmt.Errorf("backup: list tables: %w", err)
		}
		for _, r := range rows {
			switch r.Type {
			case "table":
				names = append(names, r.Name)
			case "virtual":
				c.Virtual = append(c.Virtual, r.Name)
			}
		}
	default:
		if err := q.Select(ctx, &names, `SELECT table_name FROM information_schema.tables
			WHERE table_schema = current_schema() AND table_type = 'BASE TABLE'`); err != nil {
			return nil, fmt.Errorf("backup: list tables: %w", err)
		}
	}
	sort.Strings(names)
	sort.Strings(c.Virtual)
	for _, name := range names {
		if !identPattern.MatchString(name) {
			return nil, fmt.Errorf("backup: unexpected table name %q", name)
		}
	}
	if q.Dialect() == db.SQLite {
		for _, name := range names {
			t, err := loadSQLiteTable(ctx, q, name)
			if err != nil {
				return nil, err
			}
			c.Tables[name] = t
		}
	} else if err := loadPostgresTables(ctx, q, names, c.Tables); err != nil {
		return nil, err
	}
	for _, t := range c.Tables {
		for _, col := range t.Columns {
			if !identPattern.MatchString(col.Name) {
				return nil, fmt.Errorf("backup: unexpected column name %s.%q", t.Name, col.Name)
			}
		}
		if len(t.Columns) == 0 {
			return nil, fmt.Errorf("backup: table %s has no columns", t.Name)
		}
	}
	if err := c.sort(); err != nil {
		return nil, err
	}
	return c, nil
}

func loadSQLiteTable(ctx context.Context, q db.Querier, name string) (*table, error) {
	t := &table{Name: name, Refs: map[string]string{}}
	var cols []struct {
		Name    string `db:"name"`
		Type    string `db:"type"`
		NotNull bool   `db:"notnull"`
		PK      int    `db:"pk"`
	}
	if err := q.Select(ctx, &cols, `SELECT name, type, "notnull", pk FROM pragma_table_info(?) ORDER BY cid`, name); err != nil {
		return nil, fmt.Errorf("backup: columns of %s: %w", name, err)
	}
	pk := map[int]string{}
	for _, col := range cols {
		t.Columns = append(t.Columns, column{Name: col.Name, Type: logicalType(col.Type), Nullable: !col.NotNull && col.PK == 0})
		if col.PK > 0 {
			pk[col.PK] = col.Name
		}
	}
	for i := 1; i <= len(pk); i++ {
		t.PK = append(t.PK, pk[i])
	}
	var fks []struct {
		Table string `db:"table"`
		From  string `db:"from"`
	}
	if err := q.Select(ctx, &fks, `SELECT "table", "from" FROM pragma_foreign_key_list(?)`, name); err != nil {
		return nil, fmt.Errorf("backup: foreign keys of %s: %w", name, err)
	}
	for _, fk := range fks {
		t.Refs[fk.From] = fk.Table
	}
	return t, nil
}

// loadPostgresTables reads every table's columns, primary key and foreign
// keys with one catalog query each.
func loadPostgresTables(ctx context.Context, q db.Querier, names []string, out map[string]*table) error {
	for _, name := range names {
		out[name] = &table{Name: name, Refs: map[string]string{}}
	}
	var cols []struct {
		Table    string `db:"table_name"`
		Name     string `db:"column_name"`
		Type     string `db:"data_type"`
		Nullable string `db:"is_nullable"`
	}
	if err := q.Select(ctx, &cols, `SELECT table_name, column_name, data_type, is_nullable FROM information_schema.columns
		WHERE table_schema = current_schema() ORDER BY table_name, ordinal_position`); err != nil {
		return fmt.Errorf("backup: columns: %w", err)
	}
	for _, col := range cols {
		if t := out[col.Table]; t != nil {
			t.Columns = append(t.Columns, column{Name: col.Name, Type: logicalType(col.Type), Nullable: col.Nullable == "YES"})
		}
	}
	var pks []struct {
		Table string `db:"tbl"`
		Col   string `db:"col"`
	}
	if err := q.Select(ctx, &pks, `SELECT cl.relname AS tbl, a.attname AS col FROM pg_index i
		JOIN pg_class cl ON cl.oid = i.indrelid
		JOIN pg_namespace n ON n.oid = cl.relnamespace
		JOIN LATERAL unnest(i.indkey::int2[]) WITH ORDINALITY AS k(attnum, pos) ON TRUE
		JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = k.attnum
		WHERE i.indisprimary AND n.nspname = current_schema() ORDER BY cl.relname, k.pos`); err != nil {
		return fmt.Errorf("backup: primary keys: %w", err)
	}
	for _, pk := range pks {
		t := out[pk.Table]
		if t == nil {
			continue
		}
		t.PK = append(t.PK, pk.Col)
		for i := range t.Columns {
			if t.Columns[i].Name == pk.Col {
				t.Columns[i].Nullable = false
			}
		}
	}
	var fks []struct {
		Table string `db:"tbl"`
		From  string `db:"col"`
		Ref   string `db:"ref"`
	}
	if err := q.Select(ctx, &fks, `SELECT cl.relname AS tbl, a.attname AS col, rc.relname AS ref FROM pg_constraint c
		JOIN pg_class cl ON cl.oid = c.conrelid
		JOIN pg_class rc ON rc.oid = c.confrelid
		JOIN pg_namespace n ON n.oid = cl.relnamespace
		JOIN LATERAL unnest(c.conkey) AS k(attnum) ON TRUE
		JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = k.attnum
		WHERE c.contype = 'f' AND n.nspname = current_schema()`); err != nil {
		return fmt.Errorf("backup: foreign keys: %w", err)
	}
	for _, fk := range fks {
		if t := out[fk.Table]; t != nil {
			t.Refs[fk.From] = fk.Ref
		}
	}
	return nil
}

// sort orders the tables so that every table comes after the tables it
// references. Self references, and the columns of any cycle, are deferred:
// rows are inserted with NULL there and updated once every row exists.
func (c *catalog) sort() error {
	deps := map[string]map[string][]string{} // table → referenced table → columns
	for name, t := range c.Tables {
		deps[name] = map[string][]string{}
		for col, ref := range t.Refs {
			if _, ok := c.Tables[ref]; !ok {
				continue
			}
			if ref == name {
				c.Deferred[name] = append(c.Deferred[name], col)
				continue
			}
			deps[name][ref] = append(deps[name][ref], col)
		}
	}
	done := map[string]bool{}
	for len(c.Order) < len(c.Tables) {
		var ready []string
		for name := range c.Tables {
			if done[name] {
				continue
			}
			blocked := false
			for ref := range deps[name] {
				if !done[ref] {
					blocked = true
					break
				}
			}
			if !blocked {
				ready = append(ready, name)
			}
		}
		if len(ready) == 0 {
			// A cycle: defer the first remaining table's references to unfinished tables.
			var rest []string
			for name := range c.Tables {
				if !done[name] {
					rest = append(rest, name)
				}
			}
			sort.Strings(rest)
			name := rest[0]
			for ref, cols := range deps[name] {
				if done[ref] {
					continue
				}
				for _, col := range cols {
					if cl, _ := c.Tables[name].column(col); !cl.Nullable {
						return fmt.Errorf("backup: foreign key cycle through non-null %s.%s", name, col)
					}
					c.Deferred[name] = append(c.Deferred[name], col)
				}
				delete(deps[name], ref)
			}
			continue
		}
		sort.Strings(ready)
		for _, name := range ready {
			done[name] = true
			c.Order = append(c.Order, name)
		}
	}
	for name, cols := range c.Deferred {
		sort.Strings(cols)
		if t := c.Tables[name]; len(t.PK) != 1 {
			return fmt.Errorf("backup: table %s defers foreign keys but has no single-column primary key", name)
		}
	}
	return nil
}

// logicalType maps a declared SQLite type or a PostgreSQL data_type.
func logicalType(declared string) string {
	t := strings.ToUpper(strings.TrimSpace(declared))
	switch {
	case strings.Contains(t, "INT"):
		return typeInt
	case strings.Contains(t, "BOOL"):
		return typeBool
	case strings.Contains(t, "TIMESTAMP"), strings.Contains(t, "DATE"), t == "TIME":
		return typeTime
	case strings.Contains(t, "BLOB"), strings.Contains(t, "BYTEA"):
		return typeBlob
	case strings.Contains(t, "DOUBLE"), strings.Contains(t, "REAL"), strings.Contains(t, "FLOAT"),
		strings.Contains(t, "NUMERIC"), strings.Contains(t, "DECIMAL"):
		return typeFloat
	}
	return typeText
}

var timeLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02 15:04:05.999999999-07:00",
	"2006-01-02 15:04:05.999999999Z07:00",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02T15:04:05.999999999",
	"2006-01-02 15:04",
	"2006-01-02",
}

func parseTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	for _, layout := range timeLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("backup: cannot read time %q", s)
}

// encodeValue turns a value scanned from either driver into its archive form.
func encodeValue(v any, typ string) (any, error) {
	if v == nil {
		return nil, nil
	}
	switch typ {
	case typeText:
		switch x := v.(type) {
		case string:
			return x, nil
		case []byte:
			return string(x), nil
		case time.Time:
			return x.UTC().Format(time.RFC3339Nano), nil
		}
		return fmt.Sprint(v), nil
	case typeInt:
		switch x := v.(type) {
		case int64:
			return x, nil
		case int32:
			return int64(x), nil
		case int:
			return int64(x), nil
		case float64:
			if x == math.Trunc(x) && !math.IsInf(x, 0) {
				return int64(x), nil
			}
		case bool:
			if x {
				return int64(1), nil
			}
			return int64(0), nil
		case string:
			return strconv.ParseInt(strings.TrimSpace(x), 10, 64)
		case []byte:
			return strconv.ParseInt(strings.TrimSpace(string(x)), 10, 64)
		}
	case typeFloat:
		var f float64
		switch x := v.(type) {
		case float64:
			f = x
		case float32:
			f = float64(x)
		case int64:
			f = float64(x)
		case string:
			p, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
			if err != nil {
				return nil, err
			}
			f = p
		case []byte:
			p, err := strconv.ParseFloat(strings.TrimSpace(string(x)), 64)
			if err != nil {
				return nil, err
			}
			f = p
		default:
			return nil, fmt.Errorf("backup: unexpected %T for a number", v)
		}
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, errors.New("backup: a number is not finite")
		}
		return f, nil
	case typeBool:
		switch x := v.(type) {
		case bool:
			return x, nil
		case int64:
			return x != 0, nil
		case string:
			return strconv.ParseBool(strings.TrimSpace(x))
		case []byte:
			return strconv.ParseBool(strings.TrimSpace(string(x)))
		}
	case typeTime:
		switch x := v.(type) {
		case time.Time:
			return x.UTC().Format(time.RFC3339Nano), nil
		case string:
			t, err := parseTime(x)
			if err != nil {
				return nil, err
			}
			return t.Format(time.RFC3339Nano), nil
		case []byte:
			t, err := parseTime(string(x))
			if err != nil {
				return nil, err
			}
			return t.Format(time.RFC3339Nano), nil
		}
	case typeBlob:
		switch x := v.(type) {
		case []byte:
			return base64.StdEncoding.EncodeToString(x), nil
		case string:
			return base64.StdEncoding.EncodeToString([]byte(x)), nil
		}
	}
	return nil, fmt.Errorf("backup: unexpected %T for a %s column", v, typ)
}

// decodeValue turns an archive value (decoded with UseNumber) into a driver
// argument for a column of the target database.
func decodeValue(v any, typ string) (any, error) {
	if v == nil {
		return nil, nil
	}
	switch typ {
	case typeText:
		switch x := v.(type) {
		case string:
			return x, nil
		case json.Number:
			return x.String(), nil
		case bool:
			return strconv.FormatBool(x), nil
		}
	case typeInt:
		switch x := v.(type) {
		case json.Number:
			if n, err := x.Int64(); err == nil {
				return n, nil
			}
			f, err := x.Float64()
			if err != nil || f != math.Trunc(f) {
				return nil, fmt.Errorf("backup: %q is not an integer", x)
			}
			return int64(f), nil
		case bool:
			if x {
				return int64(1), nil
			}
			return int64(0), nil
		case string:
			return strconv.ParseInt(x, 10, 64)
		}
	case typeFloat:
		switch x := v.(type) {
		case json.Number:
			return x.Float64()
		case string:
			return strconv.ParseFloat(x, 64)
		}
	case typeBool:
		switch x := v.(type) {
		case bool:
			return x, nil
		case json.Number:
			return x.String() != "0", nil
		case string:
			return strconv.ParseBool(x)
		}
	case typeTime:
		if s, ok := v.(string); ok {
			return parseTime(s)
		}
	case typeBlob:
		if s, ok := v.(string); ok {
			return base64.StdEncoding.DecodeString(s)
		}
	}
	return nil, fmt.Errorf("backup: unexpected %T for a %s column", v, typ)
}
