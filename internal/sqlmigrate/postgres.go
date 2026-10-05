package sqlmigrate

import (
	"errors"
	"fmt"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/sqlutil"
)

// postgresDialect plans for Postgres. Its ALTER TABLE makes every change
// in place, so it never rebuilds a table. On a table the previous version
// has, it uses the online forms: an index built concurrently outside a
// transaction, a foreign key or a NOT NULL added through a NOT VALID
// constraint validated in a later transaction, a unique constraint added
// over an index built concurrently. A table the plan creates is empty and
// gets the plain forms. Statements set no lock_timeout: the runner sets it.
type postgresDialect struct{}

func (postgresDialect) name() Dialect { return Postgres }

func (postgresDialect) canAlter(*change) bool { return true }

func (postgresDialect) rebuild(*tableRebuild) (rendered, error) {
	return rendered{}, errors.New("sqlmigrate: postgres alters every table in place")
}

var q = sqlutil.QuoteIdentifier

// literal renders a SQL string literal.
func literal(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// quoteList quotes each name and joins them with commas.
func quoteList(names []string) string {
	quoted := make([]string, len(names))
	for i, name := range names {
		quoted[i] = q(name)
	}
	return strings.Join(quoted, ", ")
}

// stepFor starts a transactional step of c with statements.
func stepFor(c *change, statements ...string) *Step {
	return &Step{Op: string(c.op), Subject: c.subject, Statements: statements, Transactional: true}
}

// blocking adds the blocking hazard to a step.
func blocking(step *Step, reason string) {
	step.Hazards = append(step.Hazards, &Hazard{
		ID:      HazardID(HazardBlocking, step.Subject, ""),
		Class:   HazardBlocking,
		Subject: step.Subject,
		Reason:  reason,
	})
}

func (p postgresDialect) render(c *change) (rendered, error) {
	switch c.op {
	case opCreateExtension:
		return one(stepFor(c, "CREATE EXTENSION IF NOT EXISTS "+q(c.name))), nil
	case opCreateSchema:
		return one(stepFor(c, "CREATE SCHEMA IF NOT EXISTS "+q(c.name))), nil
	case opRenameTable:
		return one(stepFor(c, "ALTER TABLE "+q(c.oldName)+" RENAME TO "+q(c.name))), nil
	case opRenameColumn:
		return one(stepFor(c, "ALTER TABLE "+q(c.table)+" RENAME COLUMN "+q(c.oldName)+" TO "+q(c.name))), nil
	case opRenameConstraint:
		return one(stepFor(c, "ALTER TABLE "+q(c.table)+" RENAME CONSTRAINT "+q(c.oldName)+" TO "+q(c.name))), nil
	case opRenameIndex:
		return one(stepFor(c, "ALTER INDEX "+q(c.oldName)+" RENAME TO "+q(c.name))), nil
	case opRenameFunction:
		return one(stepFor(c, "ALTER FUNCTION "+q(c.oldName)+"("+c.oldFunc.Arguments+") RENAME TO "+q(c.name))), nil
	case opRenameTrigger:
		return one(stepFor(c, "ALTER TRIGGER "+q(c.oldName)+" ON "+q(c.table)+" RENAME TO "+q(c.name))), nil
	case opCreateTable:
		return one(stepFor(c, createTableSQL(c.tableDef)...)), nil
	case opAddColumn:
		return one(p.addColumn(c)), nil
	case opDropNotNull:
		return one(stepFor(c, "ALTER TABLE "+q(c.table)+" ALTER COLUMN "+q(c.column.Name)+" DROP NOT NULL")), nil
	case opSetDefault:
		return one(stepFor(c, "ALTER TABLE "+q(c.table)+" ALTER COLUMN "+q(c.column.Name)+" SET DEFAULT "+c.column.Default)), nil
	case opDropDefault:
		return one(stepFor(c, "ALTER TABLE "+q(c.table)+" ALTER COLUMN "+q(c.column.Name)+" DROP DEFAULT")), nil
	case opAlterColumnType, opRegenerateColumn:
		return one(p.alter(c)), nil
	case opCreateIndex, opReplaceIndex:
		return one(p.index(c)), nil
	case opAddUnique:
		return p.addUnique(c), nil
	case opAddForeignKey, opReplaceFK:
		return p.foreignKey(c), nil
	case opCreateFunction:
		return one(stepFor(c, c.function.Definition)), nil
	case opReplaceFunction:
		if c.oldFunc.Arguments != c.function.Arguments {
			return one(stepFor(c,
				"DROP FUNCTION "+q(c.function.Name)+"("+c.oldFunc.Arguments+")",
				c.function.Definition)), nil
		}
		return one(stepFor(c, c.function.Definition)), nil
	case opCreateTrigger:
		return one(stepFor(c, c.trigger.Definition)), nil
	case opReplaceTrigger:
		return one(stepFor(c, "DROP TRIGGER "+q(c.trigger.Name)+" ON "+q(c.table), c.trigger.Definition)), nil
	case opSeedHistory:
		return one(p.seedHistory(c)), nil
	case opCreateView:
		return one(stepFor(c, createViewSQL(c.view)...)), nil
	case opReplaceView:
		return one(stepFor(c, append([]string{"DROP VIEW " + qualifiedView(c.oldView)}, createViewSQL(c.view)...)...)), nil
	case opCommentOnTable:
		return one(stepFor(c, "COMMENT ON TABLE "+q(c.table)+" IS "+literal(c.tableDef.Comment))), nil
	case opGraphContent:
		return noSQL(c), nil
	case opSetNotNull:
		return p.setNotNull(c), nil
	case opDropView:
		return one(stepFor(c, "DROP VIEW "+qualifiedView(c.oldView))), nil
	case opDropTrigger:
		return one(stepFor(c, "DROP TRIGGER "+q(c.oldTrigger.Name)+" ON "+q(c.table))), nil
	case opDropFunction:
		return one(stepFor(c, "DROP FUNCTION "+q(c.oldFunc.Name)+"("+c.oldFunc.Arguments+")")), nil
	case opDropForeignKey:
		return one(stepFor(c, "ALTER TABLE "+q(c.table)+" DROP CONSTRAINT "+q(c.foreignKey.Name))), nil
	case opDropUnique:
		return one(stepFor(c, "ALTER TABLE "+q(c.table)+" DROP CONSTRAINT "+q(c.constraint.Name))), nil
	case opDropIndex:
		return one(stepFor(c, "DROP INDEX "+q(c.index.Name))), nil
	case opDropColumn:
		return one(stepFor(c, "ALTER TABLE "+q(c.table)+" DROP COLUMN "+q(c.column.Name))), nil
	case opDropTable:
		return one(stepFor(c, "DROP TABLE "+q(c.table))), nil
	}
	return rendered{}, fmt.Errorf("sqlmigrate: postgres cannot render %s", c.op)
}

// columnSQL is a column's definition in CREATE TABLE and ADD COLUMN.
func columnSQL(col *Column) string {
	if col.Generated != "" {
		return q(col.Name) + " " + col.Type + " GENERATED ALWAYS AS (" + col.Generated + ") STORED"
	}
	def := q(col.Name) + " " + col.Type
	if col.Default != "" {
		def += " DEFAULT " + col.Default
	}
	if !col.Nullable {
		def += " NOT NULL"
	}
	return def
}

// createTableSQL creates a table with its primary key and unique
// constraints, under the names the model gives them, its partitions and
// its comment.
func createTableSQL(t *Table) []string {
	var lines []string
	for _, col := range t.Columns {
		lines = append(lines, "  "+columnSQL(col))
	}
	if t.PrimaryKey != nil {
		lines = append(lines, "  CONSTRAINT "+q(t.PrimaryKey.Name)+" PRIMARY KEY ("+quoteList(t.PrimaryKey.Columns)+")")
	}
	for _, u := range t.Uniques {
		lines = append(lines, "  CONSTRAINT "+q(u.Name)+" UNIQUE ("+quoteList(u.Columns)+")")
	}
	create := "CREATE TABLE " + q(t.Name) + " (\n" + strings.Join(lines, ",\n") + "\n)"
	if t.PartitionBy != "" {
		create += " PARTITION BY " + t.PartitionBy
	}
	statements := []string{create}
	for _, part := range t.Partitions {
		bound := part.Bound
		if bound != "DEFAULT" {
			bound = "FOR VALUES " + bound
		}
		statements = append(statements, "CREATE TABLE "+q(part.Name)+" PARTITION OF "+q(t.Name)+" "+bound)
	}
	if t.Comment != "" {
		statements = append(statements, "COMMENT ON TABLE "+q(t.Name)+" IS "+literal(t.Comment))
	}
	return statements
}

// volatileDefaults are the defaults Postgres evaluates per row, so adding a
// column with one rewrites the table.
var volatileDefaults = []string{"gen_random_uuid()", "clock_timestamp()", "random()", "uuid_generate_v4()", "timeofday()"}

func (postgresDialect) addColumn(c *change) *Step {
	step := stepFor(c, "ALTER TABLE "+q(c.table)+" ADD COLUMN "+columnSQL(c.column))
	switch {
	case c.column.Generated != "":
		blocking(step, fmt.Sprintf("Adding a stored generated column rewrites %s under an ACCESS EXCLUSIVE lock.", c.table))
	default:
		for _, fn := range volatileDefaults {
			if strings.Contains(strings.ToLower(c.column.Default), fn) {
				blocking(step, fmt.Sprintf("The default %s is volatile, so adding the column rewrites %s under an ACCESS EXCLUSIVE lock.",
					c.column.Default, c.table))
				break
			}
		}
	}
	return step
}

// alter renders column type changes and generated column changes, with the
// views that read them dropped first and created again last. Each table's
// changes are one ALTER TABLE, so the table is rewritten once: Postgres
// drops, then changes types, then adds.
func (postgresDialect) alter(c *change) *Step {
	a := c.alter
	var statements []string
	for _, v := range a.dropViews {
		statements = append(statements, "DROP VIEW "+qualifiedView(v))
	}
	var tables []string
	actions := map[string][]string{}
	act := func(table, action string) {
		if _, ok := actions[table]; !ok {
			tables = append(tables, table)
		}
		actions[table] = append(actions[table], action)
	}
	rewrites := false
	for _, g := range a.regenerate {
		act(g.table, "DROP COLUMN "+q(g.before.Name))
		rewrites = true
	}
	for _, r := range a.retypes {
		col := q(r.after.Name)
		if r.before.Default != "" {
			act(r.table, "ALTER COLUMN "+col+" DROP DEFAULT")
		}
		alterType := "ALTER COLUMN " + col + " TYPE " + r.after.Type
		if r.conv.kind != convertCoercible {
			alterType += " USING " + col + "::" + r.after.Type
			rewrites = true
		}
		act(r.table, alterType)
		if r.after.Default != "" {
			act(r.table, "ALTER COLUMN "+col+" SET DEFAULT "+r.after.Default)
		}
	}
	for _, g := range a.regenerate {
		act(g.table, "ADD COLUMN "+columnSQL(g.after))
	}
	for _, table := range tables {
		statements = append(statements, "ALTER TABLE "+q(table)+"\n  "+strings.Join(actions[table], ",\n  "))
	}
	for _, v := range a.createViews {
		statements = append(statements, createViewSQL(v)...)
	}
	step := stepFor(c, statements...)
	if rewrites {
		blocking(step, fmt.Sprintf("The change rewrites %s and rebuilds its indexes under an ACCESS EXCLUSIVE lock.",
			strings.Join(tables, ", ")))
	}
	return step
}

// indexSQL creates an index, concurrently or not.
func indexSQL(table string, idx *Index, concurrently bool) string {
	var b strings.Builder
	b.WriteString("CREATE ")
	if idx.Unique {
		b.WriteString("UNIQUE ")
	}
	b.WriteString("INDEX ")
	if concurrently {
		b.WriteString("CONCURRENTLY ")
	}
	columns := make([]string, len(idx.Columns))
	for i, col := range idx.Columns {
		columns[i] = q(col)
		if idx.Opclass != "" {
			columns[i] += " " + idx.Opclass
		}
	}
	method := idx.Method
	if method == "" {
		method = "BTREE"
	}
	fmt.Fprintf(&b, "%s ON %s USING %s (%s)", q(idx.Name), q(table), method, strings.Join(columns, ", "))
	if idx.Where != "" {
		b.WriteString(" WHERE " + idx.Where)
	}
	return b.String()
}

// index creates or replaces an index. On a table the previous version has
// it is built concurrently, outside a transaction, except on a partitioned
// table, where Postgres cannot.
func (postgresDialect) index(c *change) *Step {
	name := q(c.index.Name)
	if c.created || c.partitioned {
		var statements []string
		if c.op == opReplaceIndex {
			statements = append(statements, "DROP INDEX "+name)
		}
		step := stepFor(c, append(statements, indexSQL(c.table, c.index, false))...)
		if !c.created {
			blocking(step, fmt.Sprintf("%s is partitioned, so the index is built without CONCURRENTLY and blocks writes to it for the length of the build.", c.table))
		}
		return step
	}
	var statements []string
	if c.op == opReplaceIndex {
		statements = append(statements, "DROP INDEX CONCURRENTLY IF EXISTS "+name)
	}
	step := stepFor(c, append(statements, indexSQL(c.table, c.index, true))...)
	step.Transactional = false
	step.Recovery = []string{"DROP INDEX CONCURRENTLY IF EXISTS " + name}
	return step
}

// addUnique adds a unique constraint to a table the previous version has:
// its index is built concurrently, then the constraint takes it over.
func (postgresDialect) addUnique(c *change) rendered {
	u := c.constraint
	build := &Step{
		Op:      "buildUniqueIndex",
		Subject: c.subject,
		Statements: []string{
			"CREATE UNIQUE INDEX CONCURRENTLY " + q(u.Name) + " ON " + q(c.table) + " USING BTREE (" + quoteList(u.Columns) + ")",
		},
		Recovery: []string{"DROP INDEX CONCURRENTLY IF EXISTS " + q(u.Name)},
	}
	attach := stepFor(c, "ALTER TABLE "+q(c.table)+" ADD CONSTRAINT "+q(u.Name)+" UNIQUE USING INDEX "+q(u.Name))
	return rendered{steps: []*Step{build, attach}, main: 0}
}

// foreignKeySQL adds a foreign key, NOT VALID when it is validated later.
func foreignKeySQL(table string, fk *ForeignKey, notValid bool) string {
	s := "ALTER TABLE " + q(table) + " ADD CONSTRAINT " + q(fk.Name) +
		" FOREIGN KEY (" + quoteList(fk.Columns) + ") REFERENCES " + q(fk.RefTable) + " (" + quoteList(fk.RefColumns) + ")" +
		" ON DELETE " + fk.OnDelete
	if notValid {
		s += " NOT VALID"
	}
	return s
}

// foreignKey adds or replaces a foreign key. On a table the previous
// version has, it is added NOT VALID, which takes its locks only briefly,
// and validated in a step of its own, which reads the table without
// blocking writes.
func (postgresDialect) foreignKey(c *change) rendered {
	fk := c.foreignKey
	if c.created {
		return one(stepFor(c, foreignKeySQL(c.table, fk, false)))
	}
	var statements []string
	if c.op == opReplaceFK {
		// The key being replaced already has the new key's name: the diff
		// matched it by name, or by its columns and expand renamed it.
		statements = append(statements, "ALTER TABLE "+q(c.table)+" DROP CONSTRAINT "+q(fk.Name))
	}
	add := stepFor(c, append(statements, foreignKeySQL(c.table, fk, true))...)
	validate := stepFor(c, "ALTER TABLE "+q(c.table)+" VALIDATE CONSTRAINT "+q(fk.Name))
	validate.Op = "validateForeignKey"
	return rendered{steps: []*Step{add, validate}, main: 1}
}

// setNotNull makes a column of a table the previous version has NOT NULL
// without holding an exclusive lock for a scan: a CHECK constraint is
// added NOT VALID and validated, and SET NOT NULL then uses it instead of
// scanning the table.
func (postgresDialect) setNotNull(c *change) rendered {
	check := q(makeObjectName(c.table, c.column.Name, "not_null_check"))
	table, col := q(c.table), q(c.column.Name)
	add := stepFor(c, "ALTER TABLE "+table+" ADD CONSTRAINT "+check+" CHECK ("+col+" IS NOT NULL) NOT VALID")
	add.Op = "addNotNullCheck"
	validate := stepFor(c, "ALTER TABLE "+table+" VALIDATE CONSTRAINT "+check)
	validate.Op = "validateNotNullCheck"
	set := stepFor(c,
		"ALTER TABLE "+table+" ALTER COLUMN "+col+" SET NOT NULL",
		"ALTER TABLE "+table+" DROP CONSTRAINT "+check)
	return rendered{steps: []*Step{add, validate, set}, main: 1}
}

// seedHistory creates the history triggers of a table that becomes
// versioned and, in the same transaction, records one image of every row
// it holds, at the row's version. The triggers' lock keeps every write out
// until the transaction commits, so no write is missed or recorded twice.
func (postgresDialect) seedHistory(c *change) *Step {
	s := c.seed
	var statements []string
	for _, t := range s.triggers {
		if s.replaced[t.Name] {
			statements = append(statements, "DROP TRIGGER "+q(t.Name)+" ON "+q(t.Table))
		}
		statements = append(statements, t.Definition)
	}
	key := q(s.source.PrimaryKey.Columns[0])
	image := "to_jsonb(src)"
	if len(s.source.HistoryExclude) > 0 {
		excluded := make([]string, len(s.source.HistoryExclude))
		for i, col := range s.source.HistoryExclude {
			excluded[i] = literal(col)
		}
		image += " - ARRAY[" + strings.Join(excluded, ", ") + "]"
	}
	statements = append(statements, fmt.Sprintf(
		"INSERT INTO %s (%s, _version, operation, data)\nSELECT src.%s, src._version, 'INSERT', %s\nFROM %s AS src",
		q(s.history.Name), key, key, image, q(s.source.Name)))
	step := stepFor(c, statements...)
	blocking(step, fmt.Sprintf("Seeding %s writes one image of every row of %s while the new triggers' lock blocks writes to it.",
		s.history.Name, s.source.Name))
	return step
}

// qualifiedView is a view's schema-qualified name.
func qualifiedView(v *View) string {
	return q(v.Schema) + "." + q(v.Name)
}

// createViewSQL creates a view and its comments, as its owner when it has
// one: SET LOCAL ROLE lasts until the transaction ends, and RESET ROLE
// gives the runner its own role back for the rest of it.
func createViewSQL(v *View) []string {
	var statements []string
	if v.Owner != "" {
		statements = append(statements, "SET LOCAL ROLE "+q(v.Owner))
	}
	statements = append(statements, v.Definition...)
	if v.Owner != "" {
		statements = append(statements, "RESET ROLE")
	}
	return statements
}
