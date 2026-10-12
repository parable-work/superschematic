package sqlmigrate

import (
	"fmt"
	"sort"
	"strings"
)

// sqliteDialect plans for SQLite. Its ALTER TABLE renames a table or a
// column, adds a column SQLite allows to add (no NOT NULL without a
// constant default, no default that is not a constant, a foreign key only
// on a nullable column with no default), and drops a column. A generated
// column is VIRTUAL, which ADD COLUMN can add, so a new expression drops
// the column and adds it again (D27, amended). An index, a
// unique field's index and their drops are statements of their own. Every
// other change to a table that exists rebuilds it by copying it (rebuild).
// Every step runs in a transaction: the runner opens each with BEGIN
// IMMEDIATE, which takes the database's write lock, or sends it to D1 as
// one batch. Foreign keys stay on throughout, as D1 keeps them (D27,
// amended), so a step that drops a table makes sure no ON DELETE action
// reaches a row the plan keeps.
type sqliteDialect struct{}

func (sqliteDialect) name() Dialect { return SQLite }

// qs quotes an identifier for SQLite. Every identifier is quoted, so a
// name never reads as one of SQLite's keywords.
func qs(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func qsList(names []string) string {
	quoted := make([]string, len(names))
	for i, name := range names {
		quoted[i] = qs(name)
	}
	return strings.Join(quoted, ", ")
}

// canAlter reports the changes SQLite makes in place. A dropped column is
// never indexed, unique or under a foreign key when DROP COLUMN runs: its
// indexes and unique indexes are dropped before it in contract, a foreign
// key over it is dropped by a rebuild the drop then joins, and the primary
// key never changes.
func (sqliteDialect) canAlter(c *change) bool {
	switch c.op {
	case opRenameTable, opRenameColumn, opRenameConstraint, opRenameIndex,
		opCreateIndex, opReplaceIndex, opAddUnique, opDropUnique, opDropIndex, opDropColumn,
		opRegenerateColumn:
		return true
	case opAddColumn:
		return sqliteAddable(c.column, foreignKeyOver(c.tableDef, c.column.Name) != nil)
	case opAddForeignKey:
		// A foreign key added in expand to a table that exists is over a
		// column the plan adds: ADD COLUMN declares it, or the rebuild
		// that adds the column does.
		return c.phase == Expand && len(c.foreignKey.Columns) == 1
	}
	return false
}

// sqliteAddable reports whether ALTER TABLE ADD COLUMN adds col. A
// generated column is VIRTUAL, which it adds. A column under a foreign key
// must have a NULL default while foreign keys are enforced, a NOT NULL
// column a default other than NULL, and the default a constant.
func sqliteAddable(col *Column, references bool) bool {
	switch {
	case col.Generated != "":
		return true
	case references:
		return col.Nullable && col.Default == ""
	case col.Default == "":
		return col.Nullable
	case !sqliteConstant(col.Default):
		return false
	default:
		return col.Nullable || !strings.EqualFold(col.Default, "NULL")
	}
}

// foreignKeyOver returns the foreign key of t over column alone, or nil.
func foreignKeyOver(t *Table, column string) *ForeignKey {
	if t == nil {
		return nil
	}
	for _, fk := range t.ForeignKeys {
		if len(fk.Columns) == 1 && fk.Columns[0] == column {
			return fk
		}
	}
	return nil
}

// sqliteStep starts a transactional step of c with statements.
func sqliteStep(c *change, statements ...string) *Step {
	return &Step{Op: string(c.op), Subject: c.subject, Statements: statements, Transactional: true}
}

func (d sqliteDialect) render(c *change) (rendered, error) {
	switch c.op {
	case opRenameTable:
		return one(sqliteStep(c, "ALTER TABLE "+qs(c.oldName)+" RENAME TO "+qs(c.name))), nil
	case opRenameColumn:
		return one(sqliteStep(c, "ALTER TABLE "+qs(c.table)+" RENAME COLUMN "+qs(c.oldName)+" TO "+qs(c.name))), nil
	case opRenameConstraint:
		// SQLite does not name a primary key or a foreign key. A unique
		// constraint is an index, which SQLite cannot rename.
		if c.constraint == nil {
			return rendered{}, nil
		}
		step := sqliteStep(c, "DROP INDEX "+qs(c.oldName), sqliteUniqueSQL(c.table, c.constraint))
		sqliteIndexBlocking(step, c.table, "SQLite cannot rename an index, so the step drops it and builds it again under its new name")
		return one(step), nil
	case opRenameIndex:
		step := sqliteStep(c, "DROP INDEX "+qs(c.oldName), sqliteIndexSQL(c.table, c.index))
		sqliteIndexBlocking(step, c.table, "SQLite cannot rename an index, so the step drops it and builds it again under its new name")
		return one(step), nil
	case opCreateTable:
		statements := []string{sqliteCreateTableSQL(c.tableDef, c.tableDef.Name)}
		for _, u := range c.tableDef.Uniques {
			statements = append(statements, sqliteUniqueSQL(c.table, u))
		}
		return one(sqliteStep(c, statements...)), nil
	case opAddColumn:
		return one(sqliteStep(c, sqliteAddColumnSQL(c))), nil
	case opAddForeignKey:
		// CREATE TABLE and ADD COLUMN declare the foreign key.
		if c.created || c.phase == Expand {
			return rendered{}, nil
		}
	case opCreateIndex, opReplaceIndex:
		var statements []string
		if c.op == opReplaceIndex {
			statements = append(statements, "DROP INDEX "+qs(c.index.Name))
		}
		step := sqliteStep(c, append(statements, sqliteIndexSQL(c.table, c.index))...)
		if !c.created {
			sqliteIndexBlocking(step, c.table, "Building the index")
		}
		return one(step), nil
	case opAddUnique:
		step := sqliteStep(c, sqliteUniqueSQL(c.table, c.constraint))
		sqliteIndexBlocking(step, c.table, "Building the unique index")
		return one(step), nil
	case opDropUnique:
		return one(sqliteStep(c, "DROP INDEX "+qs(c.constraint.Name))), nil
	case opDropIndex:
		return one(sqliteStep(c, "DROP INDEX "+qs(c.index.Name))), nil
	case opDropColumn:
		step := sqliteStep(c, "ALTER TABLE "+qs(c.table)+" DROP COLUMN "+qs(c.column.Name))
		// A VIRTUAL column has no values in the table's rows to rewrite.
		if c.column.Generated == "" {
			blocking(step, fmt.Sprintf("SQLite rewrites %s to drop the column, holding the database's write lock for time that grows with the table.", c.table))
		}
		return one(step), nil
	case opRegenerateColumn:
		// The columns are VIRTUAL and nothing indexes them, so neither
		// statement reads or rewrites the table's rows.
		var statements []string
		for _, g := range c.alter.regenerate {
			statements = append(statements,
				"ALTER TABLE "+qs(g.table)+" DROP COLUMN "+qs(g.before.Name),
				"ALTER TABLE "+qs(g.table)+" ADD COLUMN "+sqliteColumnSQL(g.after))
		}
		return one(sqliteStep(c, statements...)), nil
	case opGraphContent:
		return noSQL(c), nil
	case opDropTable:
		// The tables of a reference cycle go in one step (dropTables).
		// Every other table that references one of them is gone by then.
		statements, internal := sqliteDrops(append([]*Table{c.tableDef}, c.dropsWith...))
		if internal {
			statements = append([]string{sqliteDeferForeignKeys}, statements...)
		}
		return one(sqliteStep(c, statements...)), nil
	}
	return rendered{}, fmt.Errorf("sqlmigrate: sqlite cannot render %s %s", c.op, c.subject)
}

// sqliteIndexBlocking adds the blocking hazard of building an index on a
// table that exists: the step holds the database's write lock while it
// reads the table.
func sqliteIndexBlocking(step *Step, table, what string) {
	blocking(step, fmt.Sprintf("%s holds the database's write lock for time that grows with %s.", what, table))
}

// sqliteColumnSQL is a column's definition in CREATE TABLE and ADD COLUMN.
// A generated column is VIRTUAL: SQLite computes it when a row is read, and
// ADD COLUMN cannot add a STORED one.
func sqliteColumnSQL(col *Column) string {
	if col.Generated != "" {
		return qs(col.Name) + " " + col.Type + " GENERATED ALWAYS AS (" + col.Generated + ") VIRTUAL"
	}
	def := qs(col.Name) + " " + col.Type
	if col.Default != "" {
		def += " DEFAULT " + col.Default
	}
	if !col.Nullable {
		def += " NOT NULL"
	}
	return def
}

// sqliteReferencesSQL is the REFERENCES clause of a foreign key.
func sqliteReferencesSQL(fk *ForeignKey) string {
	return "REFERENCES " + qs(fk.RefTable) + " (" + qsList(fk.RefColumns) + ") ON DELETE " + fk.OnDelete
}

// sqliteCreateTableSQL creates table t under name, with its primary key and
// foreign keys. SQLite names neither, so the statement does not either.
func sqliteCreateTableSQL(t *Table, name string) string {
	var lines []string
	for _, col := range t.Columns {
		lines = append(lines, "  "+sqliteColumnSQL(col))
	}
	if t.PrimaryKey != nil {
		lines = append(lines, "  PRIMARY KEY ("+qsList(t.PrimaryKey.Columns)+")")
	}
	for _, fk := range t.ForeignKeys {
		lines = append(lines, "  FOREIGN KEY ("+qsList(fk.Columns)+") "+sqliteReferencesSQL(fk))
	}
	return "CREATE TABLE " + qs(name) + " (\n" + strings.Join(lines, ",\n") + "\n)"
}

// sqliteAddColumnSQL adds a column, with the foreign key the new table has
// over it.
func sqliteAddColumnSQL(c *change) string {
	s := "ALTER TABLE " + qs(c.table) + " ADD COLUMN " + sqliteColumnSQL(c.column)
	if fk := foreignKeyOver(c.tableDef, c.column.Name); fk != nil {
		s += " " + sqliteReferencesSQL(fk)
	}
	return s
}

// sqliteIndexSQL creates an index. SQLite has one access method, so the
// model's BTREE is not written.
func sqliteIndexSQL(table string, idx *Index) string {
	s := "CREATE "
	if idx.Unique {
		s += "UNIQUE "
	}
	s += "INDEX " + qs(idx.Name) + " ON " + qs(table) + " (" + qsList(idx.Columns) + ")"
	if idx.Where != "" {
		s += " WHERE " + idx.Where
	}
	return s
}

// sqliteUniqueSQL creates the unique index a unique constraint is.
func sqliteUniqueSQL(table string, u *Constraint) string {
	return "CREATE UNIQUE INDEX " + qs(u.Name) + " ON " + qs(table) + " (" + qsList(u.Columns) + ")"
}

// sqliteTempPrefix starts the name a table is rebuilt under. A table the
// compiler names never starts with an underscore.
const sqliteTempPrefix = "_new_"

// sqliteDeferForeignKeys starts every rebuild, and a step that drops a
// table whose rows another table may still reference when the drop runs:
// the table itself, or a table the step drops after it. SQLite then checks
// every foreign key at the commit, not at each statement: a NO ACTION and a
// RESTRICT alike, since SQLite runs no RESTRICT action while the checks are
// deferred. A CASCADE or a SET NULL still acts at once. So a rebuild may
// copy a row before the row it references, and drop a table whose rows
// reference one another. The setting ends with the transaction.
const sqliteDeferForeignKeys = "PRAGMA defer_foreign_keys = ON"

// rebuild is SQLite's copy-table procedure (https://sqlite.org/lang_altertable.html,
// "Making Other Kinds Of Table Schema Changes") for every table of rb, in
// one transaction with foreign keys on, as D1 keeps them (D27, amended).
// It defers the foreign key checks to the commit; creates each table rb
// keeps as the phase leaves it, under a temporary name, with its foreign
// keys naming the other new tables; copies the rows, referenced tables
// first; drops the old tables (sqliteDrops), referencing ones first, so no
// ON DELETE action reaches a table the step keeps; renames each new table,
// which rewrites the foreign keys naming it; and creates the unique
// indexes and indexes. A column a table gains is left out of its copy, so
// its default fills it, and so is a generated column, which SQLite
// computes; a column whose affinity changes is cast, and a list
// whose element changes has each element converted. Renaming an old table
// out of the way instead would rewrite the foreign keys that reference it
// to the old table, whose drop would then run their ON DELETE actions.
func (d sqliteDialect) rebuild(rb *tableRebuild) (rendered, error) {
	temp := map[string]string{}
	var kept []*rebuiltTable
	var old []*Table
	for _, rt := range rb.tables {
		old = append(old, rt.before)
		if rt.after != nil {
			temp[rt.name] = sqliteTempPrefix + rt.name
			kept = append(kept, rt)
		}
	}
	kept = referencedFirst(kept)
	statements := []string{sqliteDeferForeignKeys}
	for _, rt := range kept {
		statements = append(statements, sqliteCreateTableSQL(withTempRefs(rt.after, temp), temp[rt.name]))
	}
	for _, rt := range kept {
		statements = append(statements, sqliteCopySQL(rt.before, rt.after, temp[rt.name]))
	}
	drops, _ := sqliteDrops(old)
	statements = append(statements, drops...)
	for _, rt := range kept {
		statements = append(statements, "ALTER TABLE "+qs(temp[rt.name])+" RENAME TO "+qs(rt.name))
	}
	for _, rt := range kept {
		for _, u := range rt.after.Uniques {
			statements = append(statements, sqliteUniqueSQL(rt.name, u))
		}
		for _, idx := range rt.after.Indexes {
			statements = append(statements, sqliteIndexSQL(rt.name, idx))
		}
	}
	step := &Step{
		Op:            "copyTable",
		Subject:       tableSubject(rb.at.table),
		Statements:    statements,
		Transactional: true,
	}
	step.Hazards = append(step.Hazards, &Hazard{
		ID:      HazardID(HazardCopyTable, step.Subject, ""),
		Class:   HazardCopyTable,
		Subject: step.Subject,
		Reason:  d.rebuildReason(rb),
	})
	copied := make([]string, len(kept))
	for i, rt := range kept {
		copied[i] = rt.name
	}
	sort.Strings(copied)
	if len(copied) == 1 {
		blocking(step, fmt.Sprintf("Copying %s holds the database's write lock for time that grows with the table.", copied[0]))
	} else {
		blocking(step, fmt.Sprintf("Copying %s holds the database's write lock for time that grows with the tables.", joinAnd(copied)))
	}
	return one(step), nil
}

// rebuildReason is the copy-table hazard's reason: what SQLite's ALTER
// TABLE cannot do, and every table the step copies or drops.
func (d sqliteDialect) rebuildReason(rb *tableRebuild) string {
	cannot := map[string][]string{}
	var changed []string
	for _, c := range rb.changes {
		if c.op == opDropTable || d.canAlter(c) {
			continue
		}
		if cannot[c.table] == nil {
			changed = append(changed, c.table)
		}
		cannot[c.table] = append(cannot[c.table], sqliteCannot(c))
	}
	var referencing, dropped []string
	for _, rt := range rb.tables {
		switch {
		case rt.after == nil:
			dropped = append(dropped, rt.name)
		case cannot[rt.name] == nil:
			referencing = append(referencing, rt.name)
		}
	}
	what := joinAnd(cannot[changed[0]])
	if len(changed) > 1 {
		parts := make([]string, len(changed))
		for i, table := range changed {
			parts[i] = joinAnd(cannot[table]) + " in " + table
		}
		what = strings.Join(parts, ", nor ")
	}
	reason := fmt.Sprintf("SQLite's ALTER TABLE cannot %s, so the step rebuilds %s", what, joinAnd(changed))
	if len(rb.tables) == 1 {
		return reason + ": it copies the rows into a new table, drops the old one and renames the new one."
	}
	it := "it"
	if len(changed) > 1 {
		it = "them"
	}
	if len(referencing) > 0 {
		reason += fmt.Sprintf(" with the tables that reference %s, directly or through another table: %s", it, joinAnd(referencing))
	}
	reason += "."
	switch len(dropped) {
	case 0:
	case 1:
		reason += fmt.Sprintf(" The phase drops %s, which references %s too, so the step drops it without copying it.", dropped[0], it)
	default:
		reason += fmt.Sprintf(" The phase drops %s, which reference %s too, so the step drops them without copying them.", joinAnd(dropped), it)
	}
	return reason + " It copies the rows of each table it keeps into a new table, drops the old tables and renames the new ones."
}

// sqliteCopySQL copies the rows of before into the new table into, as
// after has its columns.
func sqliteCopySQL(before, after *Table, into string) string {
	var columns, values []string
	for _, col := range after.Columns {
		prev := columnNamed(before, col.Name)
		if prev == nil || prev.Generated != "" || col.Generated != "" {
			continue
		}
		value := qs(col.Name)
		switch {
		case prev.Holds == holdsList && col.Holds == holdsList:
			if sqliteListConvert(prev.Element, col.Element).kind != convertSame {
				value = sqliteListValue(before.Name, col.Name, prev.Element, col.Element)
			}
		case sqliteAffinity(prev.Type) != sqliteAffinity(col.Type):
			value = "CAST(" + value + " AS " + sqliteAffinity(col.Type) + ")"
		}
		columns = append(columns, qs(col.Name))
		values = append(values, value)
	}
	return "INSERT INTO " + qs(into) + " (" + strings.Join(columns, ", ") + ")\nSELECT " + strings.Join(values, ", ") + "\nFROM " + qs(before.Name)
}

// withTempRefs is t with each foreign key that references a table in temp
// naming that table's temporary name instead.
func withTempRefs(t *Table, temp map[string]string) *Table {
	out := *t
	out.ForeignKeys = nil
	for _, fk := range t.ForeignKeys {
		ref := *fk
		if name, ok := temp[fk.RefTable]; ok {
			ref.RefTable = name
		}
		out.ForeignKeys = append(out.ForeignKeys, &ref)
	}
	return &out
}

// referencedFirst orders tables, given by name, so that each comes after
// the tables it references among them, as the phase leaves them; by name
// among equals. A reference cycle is broken in name order.
func referencedFirst(tables []*rebuiltTable) []*rebuiltTable {
	in := map[string]bool{}
	for _, rt := range tables {
		in[rt.name] = true
	}
	placed := map[string]bool{}
	var out []*rebuiltTable
	for len(out) < len(tables) {
		next := -1
		for i, rt := range tables {
			if placed[rt.name] {
				continue
			}
			if next < 0 {
				next = i
			}
			ready := true
			for _, fk := range rt.after.ForeignKeys {
				if fk.RefTable != rt.name && in[fk.RefTable] && !placed[fk.RefTable] {
					ready = false
					break
				}
			}
			if ready {
				next = i
				break
			}
		}
		placed[tables[next].name] = true
		out = append(out, tables[next])
	}
	return out
}

// sqliteDrops drops tables with foreign keys on, as D1 keeps them. DROP
// TABLE deletes a table's rows first, which runs the ON DELETE actions of
// the tables that reference them and checks their keys. The tables go by
// dropUnits, those that reference others first, so no drop finds a row of
// a table not yet dropped that references it, but within a reference cycle
// or on a table that references itself: internal reports whether any key
// is there. Such a step must defer the checks to its commit
// (sqliteDeferForeignKeys). Then a CASCADE deletes rows the step drops
// anyway and a SET NULL changes them, while the checks of a NO ACTION and a
// RESTRICT wait for the commit, when no table left references another;
// with the checks not deferred, a drop that finds a row the key protects
// fails at once.
func sqliteDrops(tables []*Table) (statements []string, internal bool) {
	dropped := map[string]bool{}
	for _, t := range tables {
		dropped[t.Name] = true
	}
	for _, unit := range dropUnits(append([]*Table(nil), tables...), dropped) {
		inUnit := map[string]bool{}
		for _, t := range unit {
			inUnit[t.Name] = true
		}
		for _, t := range unit {
			for _, fk := range t.ForeignKeys {
				internal = internal || inUnit[fk.RefTable]
			}
			statements = append(statements, "DROP TABLE "+qs(t.Name))
		}
	}
	return statements, internal
}

// sqliteElementAlias names json_each's rows in a list's conversion. A
// table the compiler names never starts with an underscore, so the alias
// never hides the table the rebuild copies.
const sqliteElementAlias = `"_element"`

// sqliteListValue is what a rebuild copies into a list column whose
// element changes: a new JSON array of the column's elements, each
// converted (sqliteElementValue), in the order the array keeps them. A NULL
// list stays NULL, and an empty one stays []. The list is named with its
// table, which no column of json_each can hide. An aggregate's ORDER BY
// needs SQLite 3.44 or later, which the runner's driver has.
func sqliteListValue(table, column, from, to string) string {
	list := qs(table) + "." + qs(column)
	element := sqliteElementValue(sqliteElementAlias+`."value"`, from, to)
	return "CASE WHEN " + list + " IS NOT NULL THEN (SELECT json_group_array(" + element +
		" ORDER BY " + sqliteElementAlias + `."key") FROM json_each(` + list + ") AS " + sqliteElementAlias + ") END"
}

// sqliteElementValue converts one element of a list, as json_each reads
// it: a JSON boolean as 1 or 0. A cast converts between TEXT, INTEGER, REAL
// and NUMERIC, and a boolean to a number. SQLite's truth test makes an
// element true or false: text for a boolean that becomes text, and JSON
// for an element that becomes a boolean. A null element stays null.
func sqliteElementValue(element, from, to string) string {
	truth := "CASE WHEN " + element + " THEN 'true' WHEN NOT " + element + " THEN 'false' END"
	switch {
	case to == elementBoolean:
		return "json(" + truth + ")"
	case from == elementBoolean && to == sqliteText:
		return truth
	}
	return "CAST(" + element + " AS " + to + ")"
}

// sqliteCannot says what SQLite's ALTER TABLE cannot do that c does.
func sqliteCannot(c *change) string {
	switch c.op {
	case opAddColumn:
		col := c.column
		switch {
		case foreignKeyOver(c.tableDef, col.Name) != nil:
			return "add " + col.Name + " with a foreign key unless it is nullable with no default"
		case col.Default == "":
			return "add " + col.Name + " NOT NULL without a default"
		case !sqliteConstant(col.Default):
			return "add " + col.Name + " with a default that is not a constant"
		}
		return "add " + col.Name
	case opDropNotNull:
		return "drop NOT NULL from " + c.column.Name
	case opSetNotNull:
		return "make " + c.column.Name + " NOT NULL"
	case opSetDefault:
		return "change the default of " + c.column.Name
	case opDropDefault:
		return "drop the default of " + c.column.Name
	case opAlterColumnType:
		// A list stays TEXT; its elements change.
		var retyped, converted, what []string
		for _, r := range c.alter.retypes {
			if r.before.Holds == holdsList && r.after.Holds == holdsList {
				converted = append(converted, r.after.Name)
			} else {
				retyped = append(retyped, r.after.Name)
			}
		}
		if len(retyped) > 0 {
			what = append(what, "change the type of "+joinAnd(retyped))
		}
		if len(converted) > 0 {
			what = append(what, "convert the elements of "+joinAnd(converted))
		}
		return strings.Join(what, " and ")
	case opAddForeignKey:
		return "add foreign key " + c.foreignKey.Name
	case opReplaceFK:
		return "change foreign key " + c.foreignKey.Name
	case opDropForeignKey:
		return "drop foreign key " + c.foreignKey.Name
	}
	return string(c.op) + " " + c.subject
}

// joinAnd joins items as a list in a sentence: "a", "a and b", "a, b and c".
func joinAnd(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}
