package sqlmigrate

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/sqlutil"
)

/*
ReadDatabase reads a SQLite file built by hand into a model (D116), so a
plan can start from what the file holds: `superschematic migrate plan
--from-database`. The file is read for the service being planned, against
that service's SQLite model, the schema's: a file made from the schema's
create.sql reads back as a model whose plan to the schema's has no step.

The file states its tables, columns, keys, indexes, checks and triggers,
and the model takes them from it, in the model's forms:

  - a column's type is its affinity, which is all SQLite keeps of the
    declared type (https://sqlite.org/datatype3.html): VARCHAR(40) is TEXT,
    BIGINT is INTEGER, a column with no type is BLOB. COLLATE NOCASE on a
    TEXT column is TEXT COLLATE NOCASE. A STORED generated column reads as
    the VIRTUAL one the model writes, since both hold the same values;
  - a default is the expression PRAGMA table_xinfo gives, in parentheses
    unless it is a literal, as create.sql writes it;
  - the lone INTEGER PRIMARY KEY column of a rowid table is the rowid,
    which is never NULL whatever its definition says;
  - an index of one key that is not a column in the column's own
    collation is an expression index over the key as written; a descending
    key reads as its column.

What only the compiler knows, the file cannot carry. The model takes it
from the schema's model for the tables and columns both hold, by name, a
table or a column --rename names by its new name, and leaves it empty for
the rest:

  - a table's kind and origin, and its queue's key; a table the schema
    does not declare is an entity table with no origin;
  - a column's origin, and what a TEXT column holds as JSON (Holds,
    Element): the file's text is taken to hold what the schema says it
    holds, since nothing in the file says otherwise;
  - the names SQLite keeps none of: a primary key's, a foreign key's and a
    check's are the schema's for the same key or column, else the names
    the compiler would give them;
  - whether a unique index is a unique constraint: it is when the schema's
    table has a unique constraint over the same columns, and an index
    otherwise;
  - a trigger's origin and backfill. A trigger is the schema's when the
    schema's model has one of its name on its table;
  - an index's InTransaction, and the columns of an expression index, which
    are the schema's for the same index, else the columns its keys name;
  - a spelling: a default, a generated column's expression, an index's
    keys and predicate and a trigger that are the schema's but for space,
    case or quoting are written as the schema writes them;
  - a version graph, which the model carries when the file holds every
    member table, with the columns the schema does not know as content;
  - the raw SQL that has run (@rawSql). A table, an index, a view, a
    trigger or a virtual table that a statement of the schema's raw SQL
    creates, by name, is the raw SQL's, so it is in no table of the model
    and no --drop names it. A piece has run when the file holds every
    object its statements create, each as its statement writes it; a piece
    that creates none, or one the file holds otherwise, has not, and the
    plan runs it, as D83 lets any piece run again;
  - the roles @seedRoles keeps, which are rows: the model holds none, so
    the plan seeds each, with the insert that leaves a role of its name
    alone.

What a table's definition holds and the model has no form of is listed in
Table.Unmodeled, and a plan from it rebuilds the table as the schema
writes it: a UNIQUE or a CHECK the schema's forms cannot say, AUTOINCREMENT,
ON CONFLICT, ON UPDATE, a deferred foreign key, a collation other than
NOCASE, WITHOUT ROWID and STRICT. A trigger that is not the schema's, a
view, a virtual table and an index of several keys with an expression or
another collation among them are objects the model has no form of at all:
they stop the read until a Drop names each, and the plan then drops them
first.

SQLite's own tables and the runner's state tables are not in the
inspection, so they are not in the model.
*/
func ReadDatabase(in *Inspection, schema *Model, renames []Rename, drops []Drop) (*Database, error) {
	if in == nil || schema == nil || schema.Dialect != SQLite {
		return nil, errors.New("sqlmigrate: a database is read against the SQLite model of the service being planned")
	}
	r := newDatabaseReader(in, schema, renames)
	m := &Model{Version: ModelVersion, Dialect: SQLite, Service: schema.Service}
	var errs []error
	for i := range in.Tables {
		t := &in.Tables[i]
		switch {
		case t.Kind == inspectedShadow:
			// Its virtual table's, and dropped with it.
		case t.Kind == inspectedVirtual:
			if !r.rawSQLMakes(objectVirtualTable, t.Name) {
				r.unmodeled = append(r.unmodeled, databaseObject{kind: DropVirtualTable, name: t.Name})
			}
		case r.rawSQLMakes(objectTable, t.Name):
		default:
			table, err := r.table(t)
			if err != nil {
				errs = append(errs, fmt.Errorf("sqlmigrate: table %s: %w", t.Name, err))
				continue
			}
			m.Tables = append(m.Tables, table)
		}
	}
	for _, v := range in.Views {
		if !r.rawSQLMakes(objectView, v.Name) {
			r.unmodeled = append(r.unmodeled, databaseObject{kind: DropView, name: v.Name})
		}
	}
	for _, t := range in.Triggers {
		if r.rawSQLMakes(objectTrigger, t.Name) {
			continue
		}
		if trigger := r.trigger(t); trigger != nil {
			m.Triggers = append(m.Triggers, trigger)
			continue
		}
		r.unmodeled = append(r.unmodeled, databaseObject{kind: DropTrigger, name: t.Name, table: t.Table})
	}
	errs = append(errs, r.sameNames(m)...)
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	steps, err := r.dropSteps(drops)
	if err != nil {
		return nil, err
	}
	m.Graphs = r.graphs(m)
	m.RawSQL = r.ranRawSQL()
	m.sort()
	return &Database{Model: m, Drops: steps}, nil
}

// Database is a SQLite file read into a model (ReadDatabase).
type Database struct {
	// Model is what the file holds, in the model's forms. It is the model
	// `superschematic-migrate adopt` records for the file, and the one a
	// plan from the file starts from.
	Model *Model
	// Drops are the steps that drop the objects the read was told to drop,
	// which the model has no form of. DiffDatabase runs them first.
	Drops []*Step
}

// DiffDatabase plans the migration of a database read by ReadDatabase to a
// model: the drops the read was given, then the steps of the plan from the
// file's model. The drops run first, in expand, because the steps after
// them may fail while the objects stand: SQLite refuses to rename a
// rebuilt table into place, or to drop a column, while a view or a trigger
// names what the step changes. opts.Renames are the renames the read was
// given.
func DiffDatabase(db *Database, to *Model, opts Options) (*Plan, error) {
	plan, err := Diff(db.Model, to, opts)
	if err != nil || len(db.Drops) == 0 {
		return plan, err
	}
	steps := make([]*Step, 0, len(db.Drops)+len(plan.Steps))
	for _, drop := range db.Drops {
		step := *drop
		steps = append(steps, &step)
	}
	steps = append(steps, plan.Steps...)
	for i, step := range steps {
		step.Index = i + 1
	}
	plan.Steps = steps
	if err := plan.Seal(); err != nil {
		return nil, err
	}
	return plan, nil
}

// The kinds of object a Drop names: those a file may hold and a model has
// no form of.
const (
	DropTrigger      = "trigger"
	DropView         = "view"
	DropIndex        = "index"
	DropVirtualTable = "virtual-table"
)

// DropKinds lists the kinds a Drop names.
var DropKinds = []string{DropTrigger, DropView, DropIndex, DropVirtualTable}

// Drop names an object of a file for the plan to drop (--drop
// <kind>:<name>).
type Drop struct {
	Kind, Name string
}

func (d Drop) String() string { return d.Kind + ":" + d.Name }

// ParseDrop parses a --drop value: "<kind>:<name>", the kind one of
// DropKinds, the name as the file has it.
func ParseDrop(value string) (Drop, error) {
	kind, name, ok := strings.Cut(value, ":")
	if !ok || name == "" || !slices.Contains(DropKinds, kind) {
		return Drop{}, fmt.Errorf("drop %q: want <kind>:<name>, the kind one of %s (trigger:audit_order)", value, joinAnd(DropKinds))
	}
	return Drop{Kind: kind, Name: name}, nil
}

// databaseObject is an object of a file the model has no form of.
type databaseObject struct {
	// kind is the Drop kind that names it.
	kind, name string
	// table is the table or view a trigger is on, or an index's table.
	table string
}

func (o databaseObject) String() string {
	switch o.kind {
	case DropTrigger, DropIndex:
		return o.kind + " " + o.name + " on " + o.table
	case DropVirtualTable:
		return "virtual table " + o.name
	}
	return o.kind + " " + o.name
}

// step is the step that drops the object. It is destructive, since no
// model holds the object and so no plan creates it again, and compat,
// since it runs before the previous version's servers are gone.
func (o databaseObject) step() *Step {
	step := &Step{Phase: Expand, Transactional: true}
	var gone, relies string
	switch o.kind {
	case DropTrigger:
		step.Op, step.Subject = string(opDropTrigger), triggerSubject(o.table, o.name)
		step.Statements = []string{"DROP TRIGGER " + qs(o.name)}
		gone = fmt.Sprintf("Dropping trigger %s of %s removes it for good", o.name, o.table)
		relies = fmt.Sprintf("rely on trigger %s of %s", o.name, o.table)
	case DropIndex:
		step.Op, step.Subject = string(opDropIndex), indexSubject(o.table, o.name)
		step.Statements = []string{"DROP INDEX " + qs(o.name)}
		gone = fmt.Sprintf("Dropping index %s of %s removes it for good, with what it keeps unique", o.name, o.table)
		relies = fmt.Sprintf("rely on index %s of %s", o.name, o.table)
	case DropView:
		step.Op, step.Subject = string(opDropView), "view/"+o.name
		step.Statements = []string{"DROP VIEW " + qs(o.name)}
		gone = fmt.Sprintf("Dropping view %s removes it for good", o.name)
		relies = "read view " + o.name
	default:
		step.Op, step.Subject = string(opDropTable), tableSubject(o.name)
		step.Statements = []string{"DROP TABLE " + qs(o.name)}
		gone = fmt.Sprintf("Dropping virtual table %s deletes its rows and removes it for good", o.name)
		relies = "use virtual table " + o.name
	}
	step.Hazards = []*Hazard{
		{
			ID: HazardID(HazardDestructive, step.Subject, ""), Class: HazardDestructive, Subject: step.Subject,
			Reason: gone + ": the model has no form of it, so no plan creates it again.",
		},
		{
			ID: HazardID(HazardCompat, step.Subject, ""), Class: HazardCompat, Subject: step.Subject,
			Reason: "Servers built from the previous version may " + relies + ", which this step drops before they are gone.",
		},
	}
	return step
}

// databaseReader holds one read in progress.
type databaseReader struct {
	in     *Inspection
	schema *Model
	// schemaTables are the schema's tables, by name.
	schemaTables map[string]*Table
	// tableTo maps a table of the file to the schema's name for it, and
	// columnTo a column, by its table in the file, where a rename names
	// one. Every other table and column keeps its name.
	tableTo  map[string]string
	columnTo map[string]map[string]string
	// tables are the file's ordinary tables, by name in lower case.
	tables map[string]*InspectedTable
	// holds are the statements of the file's objects, by kind and by name
	// in lower case; makes are the statements of the schema's raw SQL that
	// create an object, as tokens (createdBy), by the same.
	holds map[string]map[string]string
	makes map[string]map[string][]sqlToken
	// unmodeled are the objects of the file the model has no form of.
	unmodeled []databaseObject
}

func newDatabaseReader(in *Inspection, schema *Model, renames []Rename) *databaseReader {
	r := &databaseReader{
		in: in, schema: schema, schemaTables: tablesByName(schema),
		tableTo: map[string]string{}, columnTo: map[string]map[string]string{},
		tables: map[string]*InspectedTable{},
		holds:  map[string]map[string]string{}, makes: map[string]map[string][]sqlToken{},
	}
	for _, rn := range renames {
		prevTable, prevColumn, isColumn := strings.Cut(rn.From, ".")
		_, column, _ := strings.Cut(rn.To, ".")
		if !isColumn {
			r.tableTo[rn.From] = rn.To
			continue
		}
		if r.columnTo[prevTable] == nil {
			r.columnTo[prevTable] = map[string]string{}
		}
		r.columnTo[prevTable][prevColumn] = column
	}
	hold := func(kind, name, sql string) {
		if r.holds[kind] == nil {
			r.holds[kind] = map[string]string{}
		}
		r.holds[kind][strings.ToLower(name)] = sql
	}
	for i := range in.Tables {
		t := &in.Tables[i]
		switch t.Kind {
		case inspectedVirtual:
			hold(objectVirtualTable, t.Name, t.SQL)
		case inspectedOrdinary:
			hold(objectTable, t.Name, t.SQL)
			r.tables[strings.ToLower(t.Name)] = t
			for _, ix := range t.Indexes {
				hold(objectIndex, ix.Name, ix.SQL)
			}
		}
	}
	for _, v := range in.Views {
		hold(objectView, v.Name, v.SQL)
	}
	for _, t := range in.Triggers {
		hold(objectTrigger, t.Name, t.SQL)
	}
	for _, piece := range schema.RawSQL {
		for _, statement := range piece.Statements {
			if kind, name, body, ok := createdBy(statement); ok {
				if r.makes[kind] == nil {
					r.makes[kind] = map[string][]sqlToken{}
				}
				r.makes[kind][strings.ToLower(name)] = body
			}
		}
	}
	return r
}

// rawSQLMakes reports whether a statement of the schema's raw SQL creates
// the object of kind named name.
func (r *databaseReader) rawSQLMakes(kind, name string) bool {
	_, ok := r.makes[kind][strings.ToLower(name)]
	return ok
}

// ranRawSQL lists the pieces of the schema's raw SQL the file shows to
// have run, in the schema's order: those whose statements create at least
// one object, each of which the file holds as the statement writes it.
func (r *databaseReader) ranRawSQL() []*RawSQL {
	var out []*RawSQL
	for _, piece := range r.schema.RawSQL {
		created, held := 0, 0
		for _, statement := range piece.Statements {
			kind, name, body, ok := createdBy(statement)
			if !ok {
				continue
			}
			created++
			if sql, ok := r.holds[kind][strings.ToLower(name)]; ok && sameTokens(lexSQLite(strings.TrimRight(strings.TrimSpace(sql), ";")), body) {
				held++
			}
		}
		if created > 0 && created == held {
			ran := *piece
			out = append(out, &ran)
		}
	}
	return out
}

// peerName is the schema's name for the file's table.
func (r *databaseReader) peerName(table string) string {
	if name, ok := r.tableTo[table]; ok {
		return name
	}
	return table
}

// peerTable is the schema's table for the file's, or nil.
func (r *databaseReader) peerTable(table string) *Table {
	return r.schemaTables[r.peerName(table)]
}

// peerColumnName is the schema's name for a column of the file's table.
func (r *databaseReader) peerColumnName(table, column string) string {
	if name, ok := r.columnTo[table][column]; ok {
		return name
	}
	return column
}

func (r *databaseReader) peerColumnNames(table string, columns []string) []string {
	out := make([]string, len(columns))
	for i, column := range columns {
		out[i] = r.peerColumnName(table, column)
	}
	return out
}

// peerColumn is the schema's column for a column of the file's table, or
// nil.
func (r *databaseReader) peerColumn(table, column string) *Column {
	peer := r.peerTable(table)
	if peer == nil {
		return nil
	}
	return columnNamed(peer, r.peerColumnName(table, column))
}

// fileColumnNames are the file's names for columns of the schema's table
// for the file's table: the inverse of peerColumnNames.
func (r *databaseReader) fileColumnNames(table string, columns []string) []string {
	back := map[string]string{}
	for column, name := range r.columnTo[table] {
		back[name] = column
	}
	out := make([]string, len(columns))
	for i, column := range columns {
		out[i] = column
		if name, ok := back[column]; ok {
			out[i] = name
		}
	}
	return out
}

// sqliteDeclaredAffinity is the affinity SQLite gives a column of a
// declared type (https://sqlite.org/datatype3.html, 3.1), as the model
// spells the type of that affinity.
func sqliteDeclaredAffinity(declared string) string {
	t := strings.ToUpper(declared)
	switch {
	case strings.Contains(t, "INT"):
		return sqliteInteger
	case strings.Contains(t, "CHAR"), strings.Contains(t, "CLOB"), strings.Contains(t, "TEXT"):
		return sqliteText
	case t == "", strings.Contains(t, "BLOB"):
		return sqliteBlob
	case strings.Contains(t, "REAL"), strings.Contains(t, "FLOA"), strings.Contains(t, "DOUB"):
		return sqliteReal
	}
	return sqliteNumeric
}

// readDefault is a column's default in the model's form: nothing for
// none, a literal as written, any other expression in parentheses, which
// PRAGMA table_xinfo leaves off. A default that is the schema's column's
// but for its spelling is written as the schema writes it.
func readDefault(def *string, peer *Column) string {
	read := ""
	if def != nil {
		read = strings.TrimSpace(*def)
		if !sqlLiteral.MatchString(strings.ToUpper(read)) {
			read = "(" + read + ")"
		}
	}
	if peer != nil && peer.Generated == "" && sameTokens(defaultTokens(read), defaultTokens(peer.Default)) {
		return peer.Default
	}
	return read
}

// defaultTokens are a default's tokens as SQLite takes it: without the
// parentheses around all of it, TRUE and FALSE as the 1 and 0 they name,
// and NULL as no default.
func defaultTokens(def string) []sqlToken {
	tokens := unwrap(lexSQLite(def))
	if len(tokens) != 1 {
		return tokens
	}
	switch {
	case tokens[0].is("TRUE"):
		return []sqlToken{{kind: sqlNumber, text: "1", value: "1"}}
	case tokens[0].is("FALSE"):
		return []sqlToken{{kind: sqlNumber, text: "0", value: "0"}}
	case tokens[0].is("NULL"):
		return nil
	}
	return tokens
}

// table reads one ordinary table of the file.
func (r *databaseReader) table(t *InspectedTable) (*Table, error) {
	def, err := parseSQLiteTable(t.SQL)
	if err != nil {
		return nil, err
	}
	peer := r.peerTable(t.Name)
	table := &Table{Name: t.Name, Kind: TableEntity, Columns: []*Column{}}
	if peer != nil {
		table.Kind, table.Origin = peer.Kind, peer.Origin
	}
	unmodeled := append([]string(nil), def.unmodeled...)
	if t.WithoutRowID {
		unmodeled = append(unmodeled, "WITHOUT ROWID")
	}
	if t.Strict {
		unmodeled = append(unmodeled, "STRICT")
	}

	var names []string
	var key []InspectedColumn
	collation := map[string]string{} // column -> its collation
	for _, c := range t.Columns {
		names = append(names, c.Name)
		parsed := def.columns[strings.ToLower(c.Name)]
		if parsed == nil {
			parsed = &sqliteColumnClauses{}
		}
		peerColumn := r.peerColumn(t.Name, c.Name)
		col := &Column{Name: c.Name, Type: sqliteDeclaredAffinity(c.Type), Nullable: !c.NotNull}
		collation[c.Name] = "BINARY"
		switch {
		case parsed.collation == "" || parsed.collation == "BINARY":
		case parsed.collation == "NOCASE" && col.Type == sqliteText:
			col.Type = sqliteNoCase
			collation[c.Name] = parsed.collation
		default:
			unmodeled = append(unmodeled, "COLLATE "+parsed.collation+" on "+c.Name)
			collation[c.Name] = parsed.collation
		}
		if c.Hidden == 2 || c.Hidden == 3 {
			if parsed.generated == "" {
				return nil, fmt.Errorf("column %s is generated, and its definition holds no expression: %s", c.Name, t.SQL)
			}
			col.Generated = parsed.generated
			if peerColumn != nil && peerColumn.Generated != "" && sameSQL(col.Generated, peerColumn.Generated) {
				col.Generated = peerColumn.Generated
			}
		} else {
			col.Default = readDefault(c.Default, peerColumn)
		}
		if peerColumn != nil {
			col.Origin = peerColumn.Origin
			if sqliteAffinity(col.Type) == sqliteText {
				col.Holds, col.Element = peerColumn.Holds, peerColumn.Element
			}
		}
		if c.PrimaryKey > 0 {
			key = append(key, c)
		}
		table.Columns = append(table.Columns, col)
	}

	if len(key) > 0 {
		sort.Slice(key, func(i, j int) bool { return key[i].PrimaryKey < key[j].PrimaryKey })
		pk := &Constraint{Name: makeObjectName(t.Name, "", "pkey")}
		if peer != nil && peer.PrimaryKey != nil {
			pk.Name = peer.PrimaryKey.Name
		}
		for _, c := range key {
			pk.Columns = append(pk.Columns, c.Name)
		}
		table.PrimaryKey = pk
		if len(key) == 1 && !t.WithoutRowID && strings.EqualFold(key[0].Type, sqliteInteger) {
			columnNamed(table, key[0].Name).Nullable = false
		}
	}
	if peer != nil && peer.Queue != nil && columnNamed(table, r.fileColumnNames(t.Name, []string{peer.Queue.Key})[0]) != nil {
		queue := *peer.Queue
		queue.Key = r.fileColumnNames(t.Name, []string{peer.Queue.Key})[0]
		table.Queue = &queue
	}

	// A column has at most one check in the model; a second one, and one
	// that is none of the schema's forms, is unmodeled.
	checked := map[string]bool{}
	for _, ck := range def.checks {
		check, ok := parseSQLiteCheck(ck.expr, names)
		if !ok || checked[check.Column] {
			unmodeled = append(unmodeled, "CHECK ("+ck.expr+")")
			continue
		}
		checked[check.Column] = true
		check.Name = clipUTF8(sqlutil.CheckName(t.Name, check.Column), namedataLen-1)
		if peer != nil {
			for _, pc := range peer.Checks {
				if pc.Column == r.peerColumnName(t.Name, check.Column) {
					check.Name = pc.Name
				}
			}
		}
		table.Checks = append(table.Checks, check)
	}

	for _, ix := range t.Indexes {
		switch {
		case ix.Origin == "pk":
			// The primary key's own index.
		case ix.Origin == "u":
			unmodeled = append(unmodeled, r.inlineUnique(table, peer, ix))
		case r.rawSQLMakes(objectIndex, ix.Name):
		default:
			index, ok, err := r.index(t, ix, collation)
			switch {
			case err != nil:
				return nil, fmt.Errorf("index %s: %w", ix.Name, err)
			case !ok:
				r.unmodeled = append(r.unmodeled, databaseObject{kind: DropIndex, name: ix.Name, table: t.Name})
			case r.isConstraint(t, peer, index):
				table.Uniques = append(table.Uniques, &Constraint{Name: index.Name, Columns: index.Columns})
			default:
				table.Indexes = append(table.Indexes, index)
			}
		}
	}

	taken := map[string]bool{}
	for _, fk := range t.ForeignKeys {
		foreignKey, note := r.foreignKey(t, peer, fk, names, taken)
		if note != "" {
			unmodeled = append(unmodeled, note)
		}
		if foreignKey != nil {
			table.ForeignKeys = append(table.ForeignKeys, foreignKey)
		}
	}
	table.Unmodeled = unmodeled
	return table, nil
}

// inlineUnique reads a UNIQUE constraint of the table's definition, whose
// index SQLite names itself and lets no statement drop: it is unmodeled,
// so a plan rebuilds the table, and the phrase returned says so. When the
// schema's table makes the same columns unique, it is also that unique
// constraint, or that unique index, under the name SQLite gave it, so the
// plan does not report the columns as becoming unique: the rebuild then
// builds the schema's index in its place.
func (r *databaseReader) inlineUnique(table, peer *Table, ix InspectedIndex) string {
	var columns []string
	for _, key := range ix.Keys {
		columns = append(columns, key.Column)
	}
	if peer != nil {
		want := strings.Join(r.peerColumnNames(table.Name, columns), ",")
		for _, u := range peer.Uniques {
			if strings.Join(u.Columns, ",") == want {
				table.Uniques = append(table.Uniques, &Constraint{Name: ix.Name, Columns: columns})
				return "UNIQUE (" + strings.Join(columns, ", ") + ") in the table's definition"
			}
		}
		for _, idx := range peer.Indexes {
			if idx.Unique && idx.Where == "" && idx.Expression == "" && strings.Join(idx.Columns, ",") == want {
				table.Indexes = append(table.Indexes, &Index{Name: ix.Name, Columns: columns, Method: "BTREE", Unique: true})
				break
			}
		}
	}
	return "UNIQUE (" + strings.Join(columns, ", ") + ") in the table's definition"
}

// index reads an index CREATE INDEX made. PRAGMA index_xinfo gives its
// keys. An index all of whose keys are columns in their columns' own
// collations is over those columns; a descending key reads as the column,
// since the index holds the same rows and keeps the same values unique. An
// index of one key that is anything else is an expression index over that
// key as its statement writes it, without the parentheses create.sql
// writes around an expression. An index of several keys, one of them an
// expression or in another collation, is none the model has a form of:
// ok is false, and the index is dropped by name or not at all. A partial
// index's predicate is in its statement alone.
func (r *databaseReader) index(t *InspectedTable, ix InspectedIndex, collation map[string]string) (index *Index, ok bool, err error) {
	index = &Index{Name: ix.Name, Columns: []string{}, Method: "BTREE", Unique: ix.Unique}
	plain := true
	for _, key := range ix.Keys {
		if key.Column == "" || !strings.EqualFold(key.Collation, collation[key.Column]) {
			plain = false
		}
		index.Columns = append(index.Columns, key.Column)
	}
	if !plain && len(ix.Keys) != 1 {
		return nil, false, nil
	}
	if !plain || ix.Partial {
		keys, where, err := parseSQLiteIndex(ix.SQL)
		if err != nil {
			return nil, false, err
		}
		index.Where = where
		if !plain {
			index.Expression, index.Columns = keyExpression(keys), []string{}
			seen := map[string]bool{}
			for _, tok := range lexSQLite(index.Expression) {
				if tok.kind != sqlWord && tok.kind != sqlName {
					continue
				}
				for _, c := range t.Columns {
					if strings.EqualFold(c.Name, tok.value) && !seen[c.Name] {
						seen[c.Name] = true
						index.Columns = append(index.Columns, c.Name)
					}
				}
			}
		}
	}
	if peer := r.peerIndex(t.Name, index); peer != nil {
		index.Where, index.Expression, index.InTransaction = peer.Where, peer.Expression, peer.InTransaction
		index.Columns = r.fileColumnNames(t.Name, peer.Columns)
	}
	return index, true, nil
}

// keyExpression is the expression of an index's one key, as its statement
// writes the key: without a trailing ASC or DESC, and without the one pair
// of parentheses around all of it that create.sql writes an expression in.
func keyExpression(key string) string {
	tokens := lexSQLite(key)
	if n := len(tokens); n > 1 && (tokens[n-1].is("ASC") || tokens[n-1].is("DESC")) {
		tokens = tokens[:n-1]
	}
	if len(tokens) == 0 {
		return ""
	}
	if n := len(tokens); n > 2 && tokens[0].punct("(") && closing(tokens, 0) == n-1 {
		tokens = tokens[1 : n-1]
	}
	return strings.TrimSpace(key[tokens[0].start:tokens[len(tokens)-1].end])
}

// peerIndex is the index of the schema's table that index is, but for the
// spelling of its predicate and keys: the one of its name when there is
// one, else the first by name.
func (r *databaseReader) peerIndex(table string, index *Index) *Index {
	peer := r.peerTable(table)
	if peer == nil {
		return nil
	}
	var found *Index
	for _, idx := range peer.Indexes {
		same := idx.Unique == index.Unique && sameSQL(idx.Where, index.Where) && sameSQL(idx.Expression, index.Expression)
		if same && index.Expression == "" {
			same = strings.Join(idx.Columns, ",") == strings.Join(r.peerColumnNames(table, index.Columns), ",")
		}
		if same && (found == nil || idx.Name == index.Name) {
			found = idx
		}
	}
	return found
}

// isConstraint reports whether a unique index over plain columns is a
// unique constraint of the schema's table, which SQLite keeps as a unique
// index of the constraint's name: the schema's table has a unique
// constraint over the same columns, and either it has the index's name or
// no index of the schema's table has.
func (r *databaseReader) isConstraint(t *InspectedTable, peer *Table, index *Index) bool {
	if peer == nil || !index.Unique || index.Where != "" || index.Expression != "" {
		return false
	}
	want := strings.Join(r.peerColumnNames(t.Name, index.Columns), ",")
	constraint := false
	for _, u := range peer.Uniques {
		if strings.Join(u.Columns, ",") != want {
			continue
		}
		if u.Name == index.Name {
			return true
		}
		constraint = true
	}
	for _, idx := range peer.Indexes {
		if idx.Name == index.Name {
			return false
		}
	}
	return constraint
}

// foreignKey reads one foreign key of PRAGMA foreign_key_list. A key that
// names no columns references its table's primary key. SQLite names no
// foreign key: the name is the schema's for the same key, else the
// compiler's fk_<table>_<column>. The phrase returned is what of the key
// the model has no form of: an ON UPDATE action, or the whole key when the
// file does not hold the columns it references, which then is in no model.
func (r *databaseReader) foreignKey(t *InspectedTable, peer *Table, fk InspectedForeignKey, names []string, taken map[string]bool) (*ForeignKey, string) {
	spell := func(column string, among []string) string {
		for _, name := range among {
			if strings.EqualFold(name, column) {
				return name
			}
		}
		return column
	}
	key := &ForeignKey{RefTable: fk.Table, OnDelete: strings.ToUpper(fk.OnDelete)}
	for _, column := range fk.Columns {
		key.Columns = append(key.Columns, spell(column, names))
	}
	over := "the foreign key over (" + strings.Join(key.Columns, ", ") + ")"
	var parentColumns, parentKey []string
	if parent := r.tables[strings.ToLower(fk.Table)]; parent != nil {
		key.RefTable = parent.Name
		keyed := append([]InspectedColumn(nil), parent.Columns...)
		sort.SliceStable(keyed, func(i, j int) bool { return keyed[i].PrimaryKey < keyed[j].PrimaryKey })
		for _, c := range keyed {
			parentColumns = append(parentColumns, c.Name)
			if c.PrimaryKey > 0 {
				parentKey = append(parentKey, c.Name)
			}
		}
	}
	for _, column := range fk.RefColumns {
		key.RefColumns = append(key.RefColumns, spell(column, parentColumns))
	}
	if len(fk.RefColumns) == 0 {
		key.RefColumns = parentKey
	}
	if len(key.RefColumns) != len(key.Columns) {
		return nil, over + " to " + fk.Table + ", whose key the file does not hold"
	}

	key.Name = clipUTF8("fk_"+t.Name+"_"+strings.Join(key.Columns, "_"), namedataLen-1)
	if peer != nil {
		refTable, refColumns := r.peerName(key.RefTable), r.peerColumnNames(key.RefTable, key.RefColumns)
		for _, pk := range peer.ForeignKeys {
			if pk.RefTable == refTable && strings.Join(pk.RefColumns, ",") == strings.Join(refColumns, ",") &&
				strings.Join(pk.Columns, ",") == strings.Join(r.peerColumnNames(t.Name, key.Columns), ",") {
				key.Name = pk.Name
			}
		}
	}
	for base, n := key.Name, 2; taken[key.Name]; n++ {
		key.Name = fmt.Sprintf("%s_%d", base, n)
	}
	taken[key.Name] = true
	if !strings.EqualFold(fk.OnUpdate, "NO ACTION") {
		return key, "ON UPDATE " + strings.ToUpper(fk.OnUpdate) + " on " + over
	}
	return key, ""
}

// trigger reads a trigger of the file that is the schema's: the schema's
// model has a trigger of its name on its table, or, on a table --rename
// names, the trigger named after the table's new name (trg_<table>_...).
// It returns nil for any other trigger, which the model has no form of.
// The definition is the statement sqlite_schema keeps.
func (r *databaseReader) trigger(t InspectedTrigger) *Trigger {
	peerTable := r.peerName(t.Table)
	for _, st := range r.schema.Triggers {
		if st.Table != peerTable {
			continue
		}
		renamed := "trg_" + t.Table + "_" + strings.TrimPrefix(st.Name, "trg_"+peerTable+"_")
		if st.Name != t.Name && (peerTable == t.Table || !strings.HasPrefix(st.Name, "trg_"+peerTable+"_") || renamed != t.Name) {
			continue
		}
		definition := strings.TrimSpace(strings.TrimRight(strings.TrimSpace(t.SQL), ";"))
		if sameSQL(definition, st.Definition) {
			definition = st.Definition
		}
		return &Trigger{Name: t.Name, Table: t.Table, Definition: definition, Origin: st.Origin, Backfill: st.Backfill}
	}
	return nil
}

// graphs carries each version graph of the schema whose member tables the
// file all holds, so a plan reports a change to a member's content (the
// history hazard). A member's content is the schema's, by the file's
// names, and every column of the file's table the schema does not know,
// which it cannot tell from content.
func (r *databaseReader) graphs(m *Model) []*Graph {
	byPeer := map[string]*Table{}
	for _, t := range m.Tables {
		byPeer[r.peerName(t.Name)] = t
	}
	var out []*Graph
next:
	for _, g := range r.schema.Graphs {
		graph := &Graph{Name: g.Name, SchemaEpoch: g.SchemaEpoch}
		for _, member := range g.Members {
			ft, st := byPeer[member.Table], r.schemaTables[member.Table]
			if ft == nil || st == nil {
				continue next
			}
			content := map[string]bool{}
			for _, column := range member.Content {
				content[column] = true
			}
			fm := &GraphMember{Table: ft.Name, Content: []string{}}
			for _, col := range ft.Columns {
				if name := r.peerColumnName(ft.Name, col.Name); content[name] || columnNamed(st, name) == nil {
					fm.Content = append(fm.Content, col.Name)
				}
			}
			sort.Strings(fm.Content)
			graph.Members = append(graph.Members, fm)
		}
		out = append(out, graph)
	}
	return out
}

// sameNames refuses a table or a column the file names as the schema
// does but for its case. SQLite takes the two as one name and the plan as
// two, so the plan would create what the file already holds.
func (r *databaseReader) sameNames(m *Model) []error {
	var errs []error
	for _, ft := range m.Tables {
		peer := r.peerTable(ft.Name)
		if peer == nil {
			for _, st := range r.schema.Tables {
				if strings.EqualFold(st.Name, ft.Name) {
					errs = append(errs, fmt.Errorf("sqlmigrate: the file's table %s is the schema's %s but for its case, and SQLite takes the two as one name: name the table %s in the schema (@table)", ft.Name, st.Name, ft.Name))
				}
			}
			continue
		}
		for _, fc := range ft.Columns {
			name := r.peerColumnName(ft.Name, fc.Name)
			if columnNamed(peer, name) != nil {
				continue
			}
			for _, sc := range peer.Columns {
				if strings.EqualFold(sc.Name, name) {
					errs = append(errs, fmt.Errorf("sqlmigrate: the file's column %s.%s is the schema's %s.%s but for its case, and SQLite takes the two as one name: name the column %s in the schema (@column)", ft.Name, fc.Name, peer.Name, sc.Name, fc.Name))
				}
			}
		}
	}
	return errs
}

// dropSteps checks the drops against the objects of the file the model
// has no form of, and returns the steps that drop them: triggers, then
// views, then indexes, then virtual tables, each by name. Every such object must be
// named, since a plan that left one would leave the file holding what no
// model records; and a drop must name one, since the plan itself decides
// what becomes of everything else.
func (r *databaseReader) dropSteps(drops []Drop) ([]*Step, error) {
	unmodeled := r.unmodeled
	order := map[string]int{DropTrigger: 0, DropView: 1, DropIndex: 2, DropVirtualTable: 3}
	sort.SliceStable(unmodeled, func(i, j int) bool {
		a, b := unmodeled[i], unmodeled[j]
		if a.kind != b.kind {
			return order[a.kind] < order[b.kind]
		}
		return a.name < b.name
	})
	named := map[int]bool{}
	var errs []error
	seen := map[string]bool{}
	for _, drop := range drops {
		if seen[drop.String()] {
			errs = append(errs, fmt.Errorf("sqlmigrate: --drop %s is given twice", drop))
			continue
		}
		seen[drop.String()] = true
		found := false
		for i, object := range unmodeled {
			if object.kind == drop.Kind && strings.EqualFold(object.name, drop.Name) {
				named[i], found = true, true
			}
		}
		if found {
			continue
		}
		kind := map[string]string{DropTrigger: objectTrigger, DropView: objectView, DropIndex: objectIndex, DropVirtualTable: objectVirtualTable}[drop.Kind]
		if _, held := r.holds[kind][strings.ToLower(drop.Name)]; held {
			errs = append(errs, fmt.Errorf("sqlmigrate: --drop %s: the %s is the schema's own, and the plan decides what becomes of it", drop, kind))
			continue
		}
		errs = append(errs, fmt.Errorf("sqlmigrate: --drop %s: the file holds no %s %s", drop, kind, drop.Name))
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	var steps []*Step
	var left []databaseObject
	for i, object := range unmodeled {
		if named[i] {
			steps = append(steps, object.step())
		} else {
			left = append(left, object)
		}
	}
	if len(left) > 0 {
		var b strings.Builder
		noun, each := "objects", "each"
		if len(left) == 1 {
			noun, each = "object", "it"
		}
		fmt.Fprintf(&b, "sqlmigrate: the file holds %d %s the model has no form of, which a plan would leave in a file no model describes:\n", len(left), noun)
		for _, object := range left {
			fmt.Fprintf(&b, "  %s\n", object)
		}
		fmt.Fprintf(&b, "Name %s to have the plan drop it first:", each)
		for _, object := range left {
			fmt.Fprintf(&b, "\n  --drop %s", Drop{Kind: object.kind, Name: object.name})
		}
		return nil, errors.New(b.String())
	}
	return steps, nil
}
