package sqlutil

import "strings"

// An upsert of many rows, its result and an upsert only if changed (D89),
// which both ORMs run: Go's UpsertOne, UpsertOneResult and UpsertMany, and
// TypeScript's upsertOne, upsertOneResult and upsertMany. One statement,
// TableStatements.Upsert, inserts a batch of rows, updates each row whose
// unique key one of them holds, and returns every row it wrote with
// UpsertInserted. A row an upsert only if changed leaves as it was is not
// returned; UniqueKey.Rows reads it back by its key.

// UpsertInserted is the alias Upsert returns whether it inserted a row as.
const UpsertInserted = "_inserted"

// MaxParameters is the most parameters Postgres binds in one statement, as
// its protocol counts them in 16 bits.
const MaxParameters = 65535

// UpsertStamps is the most parameters an upsert's update stamps beside its
// rows: updatedAt and updatedBy.
const UpsertStamps = 2

// CompareAs is the format, of the column or a value %s, that an upsert
// only if changed compares the column's values as (D89): the value itself,
// but a POINT's or a POINT[]'s text, since Postgres has no equality for a
// point.
func (c ColumnModel) CompareAs() string {
	if c.Class.Point {
		return "CAST(%s AS " + c.Type + ")::text"
	}
	return "%s"
}

// columnList is every column of the table, each in double quotes, joined
// by ", ", as TableStatements.Columns lists them.
func (t *TableModel) columnList() string {
	columns := make([]string, len(t.Columns))
	for i, c := range t.Columns {
		columns[i] = `"` + c.Name + `"`
	}
	return strings.Join(columns, ", ")
}

// InsertColumns are the columns a batch insert may write, in the order a
// row's cells name them (D79): the fields, the key at its place among
// them, then the to-one relations' keys. A create writes no internal
// metadata, and the database stamps createdAt and updatedAt.
func (t *TableModel) InsertColumns() []ColumnModel {
	var fields, relations []ColumnModel
	for _, c := range t.Columns {
		switch {
		case c.Relation != "":
			relations = append(relations, c)
		case c.Key || (!c.Internal && c.Audit != AuditCreatedAt && c.Audit != AuditUpdatedAt):
			fields = append(fields, c)
		}
	}
	return append(fields, relations...)
}

// BatchRows is the most rows one Upsert of the table writes: as many as
// bind no more than MaxParameters, a parameter for each of InsertColumns
// in each row and UpsertStamps beside them. UpsertMany writes a larger
// batch in statements of this many rows, in one transaction.
func (t *TableModel) BatchRows() int {
	columns := len(t.InsertColumns())
	if columns == 0 {
		return MaxParameters
	}
	return (MaxParameters - UpsertStamps) / columns
}
