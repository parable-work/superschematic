package sqlgen

import (
	"fmt"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	ir "github.com/parable-work/superschematic/ir"
)

// The triggers of references and attachments (D154). A @references table
// keeps its rows' targets in a sub-table beside it, each a table and a key
// held as text; an @attachments table keeps its rows' files in another,
// each naming the object that holds its bytes. Neither target nor object is
// a foreign key, since a target is a row of any table and an object is the
// bucket's, so two triggers stand in for one:
//
//   - ReferencesClear, on each table a reference may point at, after each
//     delete statement, deletes the references to the rows it deleted from
//     every references sub-table that may hold one, and for each enqueues a
//     message on the holder's cleared queue, when its declaration names
//     one, so the row that held it hears of it. A table's own references
//     to its deleted rows go with them through the sub-table's foreign key
//     first, so no message names a holder the statement deleted. A soft
//     delete is an update, and clears nothing: a read leaves a deleted row
//     out on its own, and a row restored keeps its references.
//   - AttachmentsRelease, on each attachments sub-table, after each delete
//     statement, enqueues a message on the declaration's released queue for
//     each object the deleted rows named, which the worker that drains it
//     deletes once no row of any attachments table names it.
//
// Both are statement triggers over the statement's deleted rows
// (REFERENCING OLD TABLE), so a delete of many rows runs each delete and
// insert once. SQLite's forms, row triggers (D182), are in
// rowrefs_sqlite.go.

// The suffixes of the functions, after the table; each trigger's name is
// its function's after trg_.
const (
	referencesClearSuffix    = "_references_clear"
	attachmentsReleaseSuffix = "_release"
)

// transitionOld is the clause that names a delete statement's rows for a
// statement trigger.
const transitionOld = "REFERENCING OLD TABLE AS old_rows"

// ReferencesClear is the clearing trigger of one table a reference may
// point at.
type ReferencesClear struct {
	// Origin is the target table's type.
	Origin string

	Table, QuotedTable string
	// Key is the target table's key column, quoted.
	Key string

	FunctionName, TriggerName string

	// Holders are the references sub-tables that may hold a reference to
	// the table's rows, by table.
	Holders []ReferencesHolder
}

// ReferencesHolder is one references sub-table a target's delete clears,
// with the queue its declaration names.
type ReferencesHolder struct {
	// Type is the holder table's type, which a message names.
	Type string
	// QuotedTable is the sub-table; RowColumn its foreign key to the
	// holder, and the target's and role's columns, each quoted.
	QuotedTable, RowColumn, TargetTable, TargetKey, Role string
	// Queue is the cleared queue's quoted table, and Columns its message's
	// columns, quoted, in ir.RowReferencesClearedFields' order; empty for a
	// declaration that names none.
	Queue   string
	Columns []string
}

// AttachmentsRelease is the release trigger of one attachments sub-table.
type AttachmentsRelease struct {
	// Origin is the sub-table's type.
	Origin string

	Table, QuotedTable string
	// ObjectKey and ContentHash are the sub-table's columns, quoted.
	ObjectKey, ContentHash string

	FunctionName, TriggerName string

	// Queue is the released queue's quoted table, and Columns its message's
	// columns, quoted, in ir.AttachmentsReleasedFields' order.
	Queue   string
	Columns []string
}

// buildRowFeatures builds the triggers of the schema's @references and
// @attachments declarations from the tables as sqlgen built them, sorted
// by table.
func buildRowFeatures(schema *ir.Schema, tableTypes []*ir.TypeDef, tables []Table) ([]ReferencesClear, []AttachmentsRelease, error) {
	byType := map[string]*Table{}
	for i := range tables {
		if td := typeNamed(tableTypes, tables[i].OriginalName); td != nil && tables[i].Name == sqlutil.TableName(td) {
			byType[tables[i].OriginalName] = &tables[i]
		}
	}
	column := func(typeName, field string) (string, error) {
		table := byType[typeName]
		if table == nil {
			return "", fmt.Errorf("sqlgen: %s has no table", typeName)
		}
		for i := range table.Columns {
			if table.Columns[i].Origin == typeName+"."+field {
				return table.Columns[i].QuotedName, nil
			}
		}
		return "", fmt.Errorf("sqlgen: table %s has no column of field %s", table.Name, field)
	}
	queueOf := func(name string, fields []string) (string, []string, error) {
		td := schema.Types[name]
		if td == nil || td.Queue == nil {
			return "", nil, fmt.Errorf("sqlgen: %s is no queue of %s", name, schema.Name)
		}
		var columns []string
		for _, field := range fields {
			var fd *ir.FieldDef
			for _, f := range td.Fields {
				if f != nil && f.Name == field {
					fd = f
				}
			}
			if fd == nil {
				return "", nil, fmt.Errorf("sqlgen: queue %s has no field %s", name, field)
			}
			columns = append(columns, sqlutil.QuoteIdentifier(sqlutil.ColumnName(fd)))
		}
		return sqlutil.QuoteIdentifier(QueueTableName(td)), columns, nil
	}

	// The targets each holder may point at: the tables it lists, or every
	// plain table of the schema with one key.
	targets := map[string][]ReferencesHolder{}
	for _, holder := range schema.RowReferencesTables() {
		sub := ir.RowReferencesTypeName(holder.Name)
		h := ReferencesHolder{Type: holder.Name}
		subTable := byType[sub]
		if subTable == nil {
			return nil, nil, fmt.Errorf("sqlgen: @references on %s: its sub-table %s has no table", holder.Name, sub)
		}
		h.QuotedTable = subTable.QuotedName
		var err error
		for _, c := range []struct {
			field string
			into  *string
		}{
			{schema.Types[sub].RowTable.RowField, &h.RowColumn},
			{ir.RefTargetTableField, &h.TargetTable},
			{ir.RefTargetKeyField, &h.TargetKey},
			{ir.RefRoleField, &h.Role},
		} {
			if *c.into, err = column(sub, c.field); err != nil {
				return nil, nil, err
			}
		}
		if cleared := holder.RowReferences.Cleared; cleared != "" {
			if h.Queue, h.Columns, err = queueOf(cleared, ir.RowReferencesClearedFields); err != nil {
				return nil, nil, err
			}
		}
		names := holder.RowReferences.Tables
		if len(names) == 0 {
			for _, td := range tableTypes {
				if referenceTarget(td) {
					names = append(names, td.Name)
				}
			}
		}
		for _, name := range names {
			targets[name] = append(targets[name], h)
		}
	}
	var clears []ReferencesClear
	for name, holders := range targets {
		td := typeNamed(tableTypes, name)
		table := byType[name]
		if td == nil || table == nil || table.PrimaryKey == "" {
			return nil, nil, fmt.Errorf("sqlgen: a reference may point at %s, which has no table with one key", name)
		}
		slices.SortStableFunc(holders, func(a, b ReferencesHolder) int { return strings.Compare(a.QuotedTable, b.QuotedTable) })
		c := ReferencesClear{
			Origin:       name,
			Table:        table.Name,
			QuotedTable:  table.QuotedName,
			Key:          sqlutil.QuoteIdentifier(table.PrimaryKey),
			FunctionName: table.Name + referencesClearSuffix,
			TriggerName:  "trg_" + table.Name + referencesClearSuffix,
			Holders:      holders,
		}
		if len(c.TriggerName) > sqlutil.PostgresIdentifierLimit {
			return nil, nil, fmt.Errorf("sqlgen: a reference may point at %s, and the trigger that clears its references is %s, %d bytes, over PostgreSQL's %d-byte limit; give the table a shorter name (D154)",
				name, c.TriggerName, len(c.TriggerName), sqlutil.PostgresIdentifierLimit)
		}
		clears = append(clears, c)
	}
	slices.SortStableFunc(clears, func(a, b ReferencesClear) int { return strings.Compare(a.Table, b.Table) })

	var releases []AttachmentsRelease
	for _, holder := range schema.AttachmentsTables() {
		sub := ir.AttachmentsTypeName(holder.Name)
		table := byType[sub]
		if table == nil {
			return nil, nil, fmt.Errorf("sqlgen: @attachments on %s: its sub-table %s has no table", holder.Name, sub)
		}
		r := AttachmentsRelease{
			Origin:       sub,
			Table:        table.Name,
			QuotedTable:  table.QuotedName,
			FunctionName: table.Name + attachmentsReleaseSuffix,
			TriggerName:  "trg_" + table.Name + attachmentsReleaseSuffix,
		}
		var err error
		if r.ObjectKey, err = column(sub, ir.AttObjectKeyField); err != nil {
			return nil, nil, err
		}
		if r.ContentHash, err = column(sub, ir.AttContentHashField); err != nil {
			return nil, nil, err
		}
		if r.Queue, r.Columns, err = queueOf(holder.Attachments.Released, ir.AttachmentsReleasedFields); err != nil {
			return nil, nil, err
		}
		if len(r.TriggerName) > sqlutil.PostgresIdentifierLimit {
			return nil, nil, fmt.Errorf("sqlgen: @attachments on %s: its release trigger is %s, %d bytes, over PostgreSQL's %d-byte limit; give the table a shorter name (D154)",
				holder.Name, r.TriggerName, len(r.TriggerName), sqlutil.PostgresIdentifierLimit)
		}
		releases = append(releases, r)
	}
	slices.SortStableFunc(releases, func(a, b AttachmentsRelease) int { return strings.Compare(a.Table, b.Table) })
	return clears, releases, nil
}

// referenceTarget reports whether a reference that names no tables may
// point at td's rows: a plain table of the database with one key.
func referenceTarget(td *ir.TypeDef) bool {
	if td.Role != ir.RoleDBTable || td.Queue != nil || td.Projection != nil || td.JsonField || td.GraphMember != nil ||
		td.BehaviorTable != nil || td.BehaviorQueue != nil || td.RowTable != nil || td.SearchIndex {
		return false
	}
	keys := 0
	for _, fd := range td.Fields {
		if fd != nil && fd.Key {
			keys++
		}
	}
	return keys == 1
}

// Trigger is the statement trigger that clears a deleted row's references.
func (c ReferencesClear) Trigger() Trigger {
	return Trigger{Name: c.TriggerName, Timing: "AFTER DELETE", Table: c.Table, QuotedTable: c.QuotedTable, Function: c.FunctionName, Transition: transitionOld}
}

// FunctionSQL renders the clearing function: for each sub-table that may
// hold a reference to the table's rows, the references to the rows the
// statement deleted, deleted, and a cleared message for each.
func (c ReferencesClear) FunctionSQL() string {
	var b strings.Builder
	fmt.Fprintf(&b, "BEGIN\n  -- The references to the rows of %s a statement deleted (D154),\n  -- cleared, and the row that held each told on its cleared queue.\n", c.Origin)
	for _, h := range c.Holders {
		where := fmt.Sprintf("r.%s = %s AND r.%s = o.%s::text", h.TargetTable, stringLiteral(c.Origin), h.TargetKey, c.Key)
		if h.Queue == "" {
			fmt.Fprintf(&b, "  DELETE FROM %s AS r USING old_rows AS o WHERE %s;\n", h.QuotedTable, where)
			continue
		}
		fmt.Fprintf(&b, "  WITH cleared AS (\n    DELETE FROM %s AS r USING old_rows AS o WHERE %s\n    RETURNING r.%s::text AS holder_key, r.%s AS target_table, r.%s AS target_key, r.%s AS reference_role\n  )\n",
			h.QuotedTable, where, h.RowColumn, h.TargetTable, h.TargetKey, h.Role)
		fmt.Fprintf(&b, "  INSERT INTO %s (%s)\n  SELECT %s, holder_key, target_table, target_key, reference_role FROM cleared;\n",
			h.Queue, strings.Join(h.Columns, ", "), stringLiteral(h.Type))
	}
	b.WriteString("  RETURN NULL;\nEND;\n")
	body := b.String()
	tag := dollarQuote(body)
	return fmt.Sprintf("CREATE OR REPLACE FUNCTION %s() RETURNS trigger AS %s\n%s%s LANGUAGE plpgsql", sqlutil.QuoteIdentifier(c.FunctionName), tag, body, tag)
}

// Trigger is the statement trigger that enqueues the release of the
// objects deleted files named.
func (r AttachmentsRelease) Trigger() Trigger {
	return Trigger{Name: r.TriggerName, Timing: "AFTER DELETE", Table: r.Table, QuotedTable: r.QuotedTable, Function: r.FunctionName, Transition: transitionOld}
}

// FunctionSQL renders the release function: one released message per
// object the deleted rows named.
func (r AttachmentsRelease) FunctionSQL() string {
	var b strings.Builder
	fmt.Fprintf(&b, "BEGIN\n  -- The objects the files of %s a statement deleted named (D154),\n  -- each released once: its worker deletes it once no file names it.\n", r.Origin)
	fmt.Fprintf(&b, "  INSERT INTO %s (%s)\n  SELECT DISTINCT ON (o.%s) o.%s, o.%s, 'detached' FROM old_rows AS o;\n",
		r.Queue, strings.Join(r.Columns, ", "), r.ObjectKey, r.ObjectKey, r.ContentHash)
	b.WriteString("  RETURN NULL;\nEND;\n")
	body := b.String()
	tag := dollarQuote(body)
	return fmt.Sprintf("CREATE OR REPLACE FUNCTION %s() RETURNS trigger AS %s\n%s%s LANGUAGE plpgsql", sqlutil.QuoteIdentifier(r.FunctionName), tag, body, tag)
}
