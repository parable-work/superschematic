package sqlmigrate

import (
	"fmt"
	"strings"
)

// sqliteDialect plans for SQLite. Its ALTER TABLE renames a table or a
// column, adds a column SQLite allows to add (no NOT NULL without a
// constant default, no default that is not a constant, a foreign key only
// on a nullable column with no default), and drops a column. An index, a
// unique field's index and their drops are statements of their own. Every
// other change to a table that exists rebuilds it by copying it (rebuild).
// Every step runs in a transaction: the runner opens each with BEGIN
// IMMEDIATE, which takes the database's write lock.
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
		opCreateIndex, opReplaceIndex, opAddUnique, opDropUnique, opDropIndex, opDropColumn:
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

// sqliteAddable reports whether ALTER TABLE ADD COLUMN adds col. A column
// under a foreign key must have a NULL default while foreign keys are
// enforced, a NOT NULL column a default other than NULL, and the default a
// constant.
func sqliteAddable(col *Column, references bool) bool {
	switch {
	case col.Generated != "":
		return false
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
		blocking(step, fmt.Sprintf("SQLite rewrites %s to drop the column, holding the database's write lock for time that grows with the table.", c.table))
		return one(step), nil
	case opGraphContent:
		return noSQL(c), nil
	case opDropTable:
		// With foreign keys on, DROP TABLE deletes every row first and
		// runs the ON DELETE actions of the tables that reference them,
		// which a RESTRICT on the table itself refuses. The tables of a
		// reference cycle go in one step: the runner checks the foreign
		// keys before its commit, when none of them is left to reference
		// another.
		statements := []string{"DROP TABLE " + qs(c.table)}
		for _, t := range c.dropsWith {
			statements = append(statements, "DROP TABLE "+qs(t.Name))
		}
		step := sqliteStep(c, statements...)
		step.ForeignKeysOff = true
		return one(step), nil
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
func sqliteColumnSQL(col *Column) string {
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

// rebuild is SQLite's copy-table procedure (https://sqlite.org/lang_altertable.html,
// "Making Other Kinds Of Table Schema Changes"), in one transaction with
// foreign keys off: create the table as after under a temporary name, copy
// the rows, drop the old table, rename the new one, and create its unique
// indexes and indexes. A column the new table adds is left out of the copy,
// so its default fills it; a column whose affinity changes is cast, and a
// list whose element changes has each element converted. With
// foreign keys off, dropping the old table runs no ON DELETE action on the
// tables that reference it, and those references resolve again once the
// new table takes the name; the runner checks every foreign key before the
// commit.
func (d sqliteDialect) rebuild(before, after *Table, changes []*change) (rendered, error) {
	temp := sqliteTempPrefix + after.Name
	var columns, values []string
	for _, col := range after.Columns {
		prev := columnNamed(before, col.Name)
		if prev == nil {
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
	statements := []string{
		sqliteCreateTableSQL(after, temp),
		"INSERT INTO " + qs(temp) + " (" + strings.Join(columns, ", ") + ")\nSELECT " + strings.Join(values, ", ") + "\nFROM " + qs(before.Name),
		"DROP TABLE " + qs(before.Name),
		"ALTER TABLE " + qs(temp) + " RENAME TO " + qs(after.Name),
	}
	for _, u := range after.Uniques {
		statements = append(statements, sqliteUniqueSQL(after.Name, u))
	}
	for _, idx := range after.Indexes {
		statements = append(statements, sqliteIndexSQL(after.Name, idx))
	}
	step := &Step{
		Op:             "copyTable",
		Subject:        tableSubject(after.Name),
		Statements:     statements,
		Transactional:  true,
		ForeignKeysOff: true,
	}
	var cannot []string
	for _, c := range changes {
		if !d.canAlter(c) {
			cannot = append(cannot, sqliteCannot(c))
		}
	}
	step.Hazards = append(step.Hazards, &Hazard{
		ID:      HazardID(HazardCopyTable, step.Subject, ""),
		Class:   HazardCopyTable,
		Subject: step.Subject,
		Reason: fmt.Sprintf("SQLite's ALTER TABLE cannot %s, so the step rebuilds %s: it copies the rows into a new table, drops the old one and renames the new one, with foreign keys off.",
			joinAnd(cannot), after.Name),
	})
	blocking(step, fmt.Sprintf("Copying %s holds the database's write lock for time that grows with the table.", after.Name))
	return one(step), nil
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
