package migrate

import (
	"context"
	"fmt"
)

/*
An inspection is what a SQLite file's catalog holds (`superschematic-migrate
inspect`, D116): each table with what PRAGMA table_xinfo, index_list,
index_xinfo and foreign_key_list answer of it and the statement
sqlite_schema keeps, and each view and trigger with its statement.
`superschematic migrate plan --from-database` reads a file built by hand
through it and turns the answers into a model. The runner holds the SQLite
driver, which stays out of the compiler's module (D27, Apply), and judges
nothing itself: it leaves out SQLite's own tables (sqlite_%) and its own
(superschematic_%), and reports the rest as SQLite gives it.
*/

// InspectVersion is the version of the inspection the inspect command
// prints.
const InspectVersion = 1

// Inspection is what the inspect command prints. Each list is in name
// order.
type Inspection struct {
	Version  int                `json:"version"`
	Tables   []InspectedTable   `json:"tables"`
	Views    []InspectedView    `json:"views"`
	Triggers []InspectedTrigger `json:"triggers"`
}

// The kinds of an InspectedTable, as PRAGMA table_list names them.
const (
	InspectedOrdinary = "table"
	InspectedVirtual  = "virtual"
	InspectedShadow   = "shadow"
)

// InspectedTable is one table. Columns, Indexes and ForeignKeys are read
// of an ordinary table alone: a virtual table's come from its module, which
// the runner may not hold, and a shadow table is its virtual table's.
type InspectedTable struct {
	Name string `json:"name"`
	// Kind is the table's type in PRAGMA table_list: table, virtual, or
	// shadow for a table a virtual table keeps its content in.
	Kind string `json:"kind"`
	// SQL is the CREATE statement sqlite_schema keeps.
	SQL          string `json:"sql"`
	WithoutRowID bool   `json:"withoutRowid,omitempty"`
	Strict       bool   `json:"strict,omitempty"`

	Columns     []InspectedColumn     `json:"columns,omitempty"`
	Indexes     []InspectedIndex      `json:"indexes,omitempty"`
	ForeignKeys []InspectedForeignKey `json:"foreignKeys,omitempty"`
}

// InspectedColumn is a row of PRAGMA table_xinfo, in the table's order.
type InspectedColumn struct {
	Name string `json:"name"`
	// Type is the declared type, as written; "" when the column has none.
	Type    string `json:"type"`
	NotNull bool   `json:"notNull,omitempty"`
	// Default is the DEFAULT expression without the parentheses an
	// expression is written in; nil when the column has none.
	Default *string `json:"default,omitempty"`
	// PrimaryKey is the column's 1-based place in the primary key; 0 when
	// it is not in it.
	PrimaryKey int `json:"primaryKey,omitempty"`
	// Hidden is 0 for an ordinary column, 2 for a VIRTUAL generated column
	// and 3 for a STORED one.
	Hidden int `json:"hidden,omitempty"`
}

// InspectedIndex is a row of PRAGMA index_list with its key columns.
type InspectedIndex struct {
	Name   string `json:"name"`
	Unique bool   `json:"unique,omitempty"`
	// Origin is c for CREATE INDEX, u for a UNIQUE constraint of the
	// table's definition and pk for its PRIMARY KEY.
	Origin  string `json:"origin"`
	Partial bool   `json:"partial,omitempty"`
	// SQL is the CREATE INDEX statement sqlite_schema keeps; "" for an
	// index SQLite made for a constraint.
	SQL  string              `json:"sql,omitempty"`
	Keys []InspectedIndexKey `json:"keys"`
}

// InspectedIndexKey is a key row of PRAGMA index_xinfo, in key order.
type InspectedIndexKey struct {
	// Column is the column the key is; "" for an expression and for the
	// rowid.
	Column string `json:"column,omitempty"`
	// Expression is set for a key that is an expression.
	Expression bool   `json:"expression,omitempty"`
	Descending bool   `json:"descending,omitempty"`
	Collation  string `json:"collation"`
}

// InspectedForeignKey is one foreign key of PRAGMA foreign_key_list, in
// the pragma's order.
type InspectedForeignKey struct {
	Table   string   `json:"table"`
	Columns []string `json:"columns"`
	// RefColumns are the columns referenced; empty when the key names none
	// and so references the table's primary key.
	RefColumns []string `json:"refColumns,omitempty"`
	OnUpdate   string   `json:"onUpdate"`
	OnDelete   string   `json:"onDelete"`
	Match      string   `json:"match"`
}

// InspectedView is a view and the statement sqlite_schema keeps.
type InspectedView struct {
	Name string `json:"name"`
	SQL  string `json:"sql"`
}

// InspectedTrigger is a trigger, the table or view it is on, and the
// statement sqlite_schema keeps.
type InspectedTrigger struct {
	Name  string `json:"name"`
	Table string `json:"table"`
	SQL   string `json:"sql"`
}

// Inspect reads the catalog of the SQLite database the driver is
// connected to, in one read-only transaction.
func (r *Runner) Inspect(ctx context.Context) (*Inspection, error) {
	if dialect := r.Driver.Dialect(); dialect != SQLite {
		return nil, refusef("inspect reads a SQLite file's catalog, and the database is %s", dialect)
	}
	out := &Inspection{Version: InspectVersion, Tables: []InspectedTable{}, Views: []InspectedView{}, Triggers: []InspectedTrigger{}}
	err := r.Driver.Transact(ctx, TxOptions{ReadOnly: true}, func(ctx context.Context, conn Conn) error {
		return inspect(ctx, conn, out)
	})
	if err != nil {
		return nil, fmt.Errorf("read the database's catalog: %w", err)
	}
	return out, nil
}

// inspect fills out from conn. No query returns NULL: each takes a NULL as
// "" beside a column that says whether the value was there.
func inspect(ctx context.Context, conn Conn, out *Inspection) error {
	// An underscore matches any character in LIKE, so SQLite's prefix is
	// escaped.
	err := conn.Query(ctx, `SELECT l.name, l.type, l.wr, l.strict, COALESCE(s.sql, '')
		FROM pragma_table_list AS l LEFT JOIN sqlite_schema AS s ON s.type = 'table' AND s.name = l.name
		WHERE l.schema = 'main' AND l.type IN ('table', 'virtual', 'shadow') AND l.name NOT LIKE 'sqlite\_%' ESCAPE '\'
		ORDER BY l.name`, nil,
		func(scan func(dest ...any) error) error {
			var t InspectedTable
			var withoutRowID, strict int64
			if err := scan(&t.Name, &t.Kind, &withoutRowID, &strict, &t.SQL); err != nil {
				return err
			}
			t.WithoutRowID, t.Strict = withoutRowID != 0, strict != 0
			if !runnerTable(t.Name) {
				out.Tables = append(out.Tables, t)
			}
			return nil
		})
	if err != nil {
		return err
	}
	for i := range out.Tables {
		if t := &out.Tables[i]; t.Kind == InspectedOrdinary {
			if err := inspectTable(ctx, conn, t); err != nil {
				return fmt.Errorf("table %s: %w", t.Name, err)
			}
		}
	}
	err = conn.Query(ctx, `SELECT name, COALESCE(sql, '') FROM sqlite_schema WHERE type = 'view' ORDER BY name`, nil,
		func(scan func(dest ...any) error) error {
			var v InspectedView
			if err := scan(&v.Name, &v.SQL); err != nil {
				return err
			}
			out.Views = append(out.Views, v)
			return nil
		})
	if err != nil {
		return err
	}
	return conn.Query(ctx, `SELECT name, tbl_name, COALESCE(sql, '') FROM sqlite_schema WHERE type = 'trigger' ORDER BY name`, nil,
		func(scan func(dest ...any) error) error {
			var t InspectedTrigger
			if err := scan(&t.Name, &t.Table, &t.SQL); err != nil {
				return err
			}
			if !runnerTable(t.Table) {
				out.Triggers = append(out.Triggers, t)
			}
			return nil
		})
}

// inspectTable reads an ordinary table's columns, indexes and foreign
// keys.
func inspectTable(ctx context.Context, conn Conn, t *InspectedTable) error {
	err := conn.Query(ctx, `SELECT name, type, "notnull", dflt_value IS NOT NULL, COALESCE(dflt_value, ''), pk, hidden
		FROM pragma_table_xinfo(?) ORDER BY cid`, []any{t.Name},
		func(scan func(dest ...any) error) error {
			var c InspectedColumn
			var def string
			var notNull, hasDefault, pk, hidden int64
			if err := scan(&c.Name, &c.Type, &notNull, &hasDefault, &def, &pk, &hidden); err != nil {
				return err
			}
			c.NotNull, c.PrimaryKey, c.Hidden = notNull != 0, int(pk), int(hidden)
			if hasDefault != 0 {
				c.Default = &def
			}
			t.Columns = append(t.Columns, c)
			return nil
		})
	if err != nil {
		return err
	}
	err = conn.Query(ctx, `SELECT l.name, l."unique", l.origin, l.partial, COALESCE(s.sql, '')
		FROM pragma_index_list(?) AS l LEFT JOIN sqlite_schema AS s ON s.type = 'index' AND s.name = l.name
		ORDER BY l.name`, []any{t.Name},
		func(scan func(dest ...any) error) error {
			ix := InspectedIndex{Keys: []InspectedIndexKey{}}
			var unique, partial int64
			if err := scan(&ix.Name, &unique, &ix.Origin, &partial, &ix.SQL); err != nil {
				return err
			}
			ix.Unique, ix.Partial = unique != 0, partial != 0
			t.Indexes = append(t.Indexes, ix)
			return nil
		})
	if err != nil {
		return err
	}
	for i := range t.Indexes {
		ix := &t.Indexes[i]
		// A key's cid is -2 for an expression and -1 for the rowid.
		err := conn.Query(ctx, `SELECT cid, COALESCE(name, ''), "desc", COALESCE(coll, '')
			FROM pragma_index_xinfo(?) WHERE key = 1 ORDER BY seqno`, []any{ix.Name},
			func(scan func(dest ...any) error) error {
				var k InspectedIndexKey
				var cid, desc int64
				if err := scan(&cid, &k.Column, &desc, &k.Collation); err != nil {
					return err
				}
				k.Expression, k.Descending = cid == -2, desc != 0
				ix.Keys = append(ix.Keys, k)
				return nil
			})
		if err != nil {
			return fmt.Errorf("index %s: %w", ix.Name, err)
		}
	}
	// The rows of one key share an id and come in the order of its columns.
	last := int64(-1)
	return conn.Query(ctx, `SELECT id, "table", "from", "to" IS NOT NULL, COALESCE("to", ''), on_update, on_delete, "match"
		FROM pragma_foreign_key_list(?) ORDER BY id, seq`, []any{t.Name},
		func(scan func(dest ...any) error) error {
			var id, hasTo int64
			var table, from, to, onUpdate, onDelete, match string
			if err := scan(&id, &table, &from, &hasTo, &to, &onUpdate, &onDelete, &match); err != nil {
				return err
			}
			if id != last {
				last = id
				t.ForeignKeys = append(t.ForeignKeys, InspectedForeignKey{Table: table, OnUpdate: onUpdate, OnDelete: onDelete, Match: match})
			}
			fk := &t.ForeignKeys[len(t.ForeignKeys)-1]
			fk.Columns = append(fk.Columns, from)
			if hasTo != 0 {
				fk.RefColumns = append(fk.RefColumns, to)
			}
			return nil
		})
}
