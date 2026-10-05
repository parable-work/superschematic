// Package sqlmigrate plans the migration of a DB service's database from
// one version of its schema to another (D27 in docs/DECISIONS.md).
//
// Both versions are resolved to a [Model], the relational schema sqlgen
// builds from the IR before it renders create.sql. [Diff] compares two
// models and returns a [Plan]: ordered steps in two phases, each with its
// SQL and its hazards. No database is read. The runner in
// runtime/migrate/go applies a plan; this package never does.
package sqlmigrate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	ir "github.com/parable-work/superschematic/ir"
)

// ModelVersion is the version of the Model JSON form.
const ModelVersion = 1

// Dialect names the database a model and a plan are for.
type Dialect string

const (
	Postgres Dialect = "postgres"
	SQLite   Dialect = "sqlite"
)

// Model is the relational schema of one DB service in one dialect: what
// create.sql creates, as data. Object lists are sorted by name so the JSON
// form, and so the hash, depend only on the schema and the compiler. Column
// order within a table is the order create.sql writes; the diff never
// reorders columns and ignores order when it compares.
type Model struct {
	Version int     `json:"version"`
	Dialect Dialect `json:"dialect"`
	Service string  `json:"service"`

	// Extensions are the Postgres extensions create.sql enables. A plan
	// creates the missing ones and never drops one.
	Extensions []string `json:"extensions,omitempty"`

	// Schemas are the namespaces the model creates besides the default
	// one: the projection views' pools.
	Schemas []string `json:"schemas,omitempty"`

	Tables    []*Table    `json:"tables,omitempty"`
	Functions []*Function `json:"functions,omitempty"`
	Triggers  []*Trigger  `json:"triggers,omitempty"`
	Views     []*View     `json:"views,omitempty"`

	// Graphs are the version graphs whose member tables the model holds,
	// so a change to a member's content columns can be reported (the
	// history hazard) with the graph's schema epoch.
	Graphs []*Graph `json:"graphs,omitempty"`
}

// TableKind says what declared a table.
type TableKind string

const (
	// TableEntity is a table of a DB type.
	TableEntity TableKind = "entity"
	// TableJoin is a @manyToMany join table.
	TableJoin TableKind = "join"
	// TableHistory is a @versioned table's history table.
	TableHistory TableKind = "history"
)

// Table is one table and what hangs off it.
type Table struct {
	Name string    `json:"name"`
	Kind TableKind `json:"kind"`

	// Origin is the schema element the table comes from: the type name for
	// an entity table ("Order"), "Order.tags" for the join table that field
	// declares, and "Order" for its history table.
	Origin string `json:"origin,omitempty"`

	// Comment is the COMMENT ON TABLE text.
	Comment string `json:"comment,omitempty"`

	Columns     []*Column     `json:"columns"`
	PrimaryKey  *Constraint   `json:"primaryKey,omitempty"`
	Uniques     []*Constraint `json:"uniques,omitempty"`
	ForeignKeys []*ForeignKey `json:"foreignKeys,omitempty"`
	Indexes     []*Index      `json:"indexes,omitempty"`

	// PartitionBy is the partition clause of a partitioned table, as
	// create.sql writes it after PARTITION BY ("RANGE (recorded_at)").
	PartitionBy string `json:"partitionBy,omitempty"`
	// Partitions are the partitions create.sql creates of this table.
	Partitions []*Partition `json:"partitions,omitempty"`

	// History names the history table of a @versioned table; empty
	// otherwise.
	History string `json:"history,omitempty"`
	// HistoryExclude lists the columns @versioned({ exclude }) leaves out of
	// every history image, in declaration order. A plan seeds a new history
	// table with images that leave them out too.
	HistoryExclude []string `json:"historyExclude,omitempty"`
	// Optimistic is true for an @optimistic table (a _version bump with no
	// history).
	Optimistic bool `json:"optimistic,omitempty"`
}

// Column is one column of a table.
type Column struct {
	Name string `json:"name"`

	// Origin is the schema field the column stores ("Order.total"; a
	// relation field's foreign-key column has the relation field's origin).
	// Empty for a column the generator adds on its own (an added id,
	// _version, search_text, a history table's columns).
	Origin string `json:"origin,omitempty"`

	// Type is the column type in the dialect's spelling, as create.sql
	// writes it ("UUID", "TEXT[]", "TIMESTAMPTZ").
	Type     string `json:"type"`
	Nullable bool   `json:"nullable,omitempty"`

	// Default is the DEFAULT expression as create.sql writes it; empty
	// means none.
	Default string `json:"default,omitempty"`

	// Generated is the expression of a stored generated column; empty
	// means the column is not generated.
	Generated string `json:"generated,omitempty"`

	// Holds says what a SQLite column holds as JSON TEXT: "list" for a
	// list field's JSON array, "json" for a JSON value. It is empty for
	// every other column, and for every column of a Postgres model, whose
	// types tell a list and JSON from text.
	Holds string `json:"holds,omitempty"`

	// Element is what each element of a SQLite list column's JSON array
	// holds: TEXT, INTEGER, REAL or NUMERIC as SQLite stores the element's
	// type, BOOLEAN for JSON's true and false, JSON for a JSON value, and
	// BLOB for bytes. It is empty for every other column, and for every
	// column of a Postgres model, whose types name the element.
	Element string `json:"element,omitempty"`
}

// What a SQLite column holds as JSON TEXT (Column.Holds).
const (
	holdsList = "list"
	holdsJSON = "json"
)

// Constraint is a primary key or a unique constraint: its name, as the
// database gives it, and its columns.
type Constraint struct {
	Name    string   `json:"name"`
	Columns []string `json:"columns"`
}

// ForeignKey is one foreign key constraint.
type ForeignKey struct {
	Name       string   `json:"name"`
	Columns    []string `json:"columns"`
	RefTable   string   `json:"refTable"`
	RefColumns []string `json:"refColumns"`
	OnDelete   string   `json:"onDelete"`
}

// Index is one index that is not a constraint's.
type Index struct {
	Name    string   `json:"name"`
	Columns []string `json:"columns"`
	// Method is the access method: "BTREE", "GIN", "GIST".
	Method string `json:"method"`
	// Opclass is the operator class every column is indexed with
	// ("gin_trgm_ops"); empty means the default.
	Opclass string `json:"opclass,omitempty"`
	Unique  bool   `json:"unique,omitempty"`
	// Where is a partial index's predicate, without WHERE.
	Where string `json:"where,omitempty"`
}

// Partition is a partition of a partitioned table.
type Partition struct {
	Name string `json:"name"`
	// Bound is the partition bound as create.sql writes it ("DEFAULT").
	Bound string `json:"bound"`
}

// Function is a function the model creates, such as a history capture
// function.
type Function struct {
	Name string `json:"name"`
	// Arguments is the argument type list DROP FUNCTION names it by,
	// without parentheses ("INTEGER, INTEGER"; empty for none).
	Arguments string `json:"arguments,omitempty"`
	// Definition is the CREATE OR REPLACE FUNCTION statement, rendered as
	// create.sql renders it, without a trailing semicolon.
	Definition string `json:"definition"`
	// Origin is the type whose decorator adds it ("Order").
	Origin string `json:"origin,omitempty"`
}

// Trigger is a trigger the model creates.
type Trigger struct {
	Name  string `json:"name"`
	Table string `json:"table"`
	// Definition is the CREATE TRIGGER statement, without a trailing
	// semicolon.
	Definition string `json:"definition"`
	Origin     string `json:"origin,omitempty"`
}

// View is a projection view.
type View struct {
	// Schema is the view's pool; Name its name inside it.
	Schema string `json:"schema"`
	Name   string `json:"name"`
	// Owner is the role the view is created as (outputs.sql.viewOwner);
	// empty creates it as the runner.
	Owner string `json:"owner,omitempty"`
	// Definition is the CREATE VIEW statement and its COMMENT statements,
	// each without a trailing semicolon, in order.
	Definition []string `json:"definition"`
	// Origin is the projection type ("AppPreference").
	Origin string `json:"origin,omitempty"`
	// Columns are the columns the view publishes, in order: its readers'
	// contract.
	Columns []*ViewColumn `json:"columns"`
	// Reads are the table columns the view reads, sorted.
	Reads []*ColumnRef `json:"reads,omitempty"`
}

// ViewColumn is one column a view publishes.
type ViewColumn struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Nullable bool   `json:"nullable,omitempty"`
}

// ColumnRef names a table column.
type ColumnRef struct {
	Table  string `json:"table"`
	Column string `json:"column"`
}

// Graph is a version graph whose tables the model holds.
type Graph struct {
	Name        string `json:"name"`
	SchemaEpoch int    `json:"schemaEpoch"`
	// Members lists each member table with its content columns: the
	// columns a commit hashes and merges (D17), so not the audit fields,
	// the graph's own columns or an excluded conflict unit.
	Members []*GraphMember `json:"members"`
}

// GraphMember is one member table of a version graph.
type GraphMember struct {
	Table   string   `json:"table"`
	Content []string `json:"content"`
}

// CanonicalJSON returns the model in the form its hash is taken over:
// ir.CanonicalJSON of its JSON encoding (compact, object keys sorted).
func (m *Model) CanonicalJSON() ([]byte, error) {
	raw, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	return ir.CanonicalJSON(raw)
}

// Hash returns the lowercase hex SHA-256 of the model's canonical JSON.
func (m *Model) Hash() (string, error) {
	canonical, err := m.CanonicalJSON()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}
