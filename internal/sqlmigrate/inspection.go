package sqlmigrate

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// An inspection is what `superschematic-migrate inspect` prints of a
// SQLite file's catalog (runtime/migrate/README.md, Inspect): each table
// with what PRAGMA table_xinfo, index_list, index_xinfo and
// foreign_key_list answer of it and the statement sqlite_schema keeps, and
// each view and trigger with its statement. The runner holds the SQLite
// driver, which stays out of the compiler's module (D27, Apply), so the
// compiler reads a file through it and judges the answers here
// (ReadDatabase, D116). The types mirror the runner's, and
// runtime/migrate/testdata/inspect holds an inspection both sides' tests
// read.

// InspectVersion is the version of the inspection this compiler reads.
const InspectVersion = 1

// Inspection is the inspect command's result.
type Inspection struct {
	Version  int                `json:"version"`
	Tables   []InspectedTable   `json:"tables"`
	Views    []InspectedView    `json:"views"`
	Triggers []InspectedTrigger `json:"triggers"`
}

// The kinds of an InspectedTable, as PRAGMA table_list names them.
const (
	inspectedOrdinary = "table"
	inspectedVirtual  = "virtual"
	inspectedShadow   = "shadow"
)

// InspectedTable is one table. The runner reads the columns, indexes and
// foreign keys of an ordinary table alone.
type InspectedTable struct {
	Name string `json:"name"`
	// Kind is table, virtual, or shadow for a table a virtual table keeps
	// its content in.
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
	Column     string `json:"column,omitempty"`
	Expression bool   `json:"expression,omitempty"`
	Descending bool   `json:"descending,omitempty"`
	Collation  string `json:"collation"`
}

// InspectedForeignKey is one foreign key of PRAGMA foreign_key_list.
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

// ReadInspection decodes what `superschematic-migrate inspect` printed,
// refusing a member it does not know and another version: both mean the
// runner is of another release.
func ReadInspection(data []byte) (*Inspection, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var in Inspection
	if err := decoder.Decode(&in); err != nil {
		return nil, fmt.Errorf("sqlmigrate: not an inspection this compiler reads; use the migration runner of this release: %w", err)
	}
	if in.Version != InspectVersion {
		return nil, fmt.Errorf("sqlmigrate: the migration runner inspects in version %d, and this compiler reads version %d; use the runner of this release", in.Version, InspectVersion)
	}
	return &in, nil
}
