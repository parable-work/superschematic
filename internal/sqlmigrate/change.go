package sqlmigrate

import "strings"

// op names an operation. It is the Op of the step a change renders to; a
// dialect that renders a change as several steps names the others itself.
type op string

const (
	opCreateExtension  op = "createExtension"
	opCreateSchema     op = "createSchema"
	opRenameTable      op = "renameTable"
	opRenameColumn     op = "renameColumn"
	opRenameConstraint op = "renameConstraint"
	opRenameIndex      op = "renameIndex"
	opRenameFunction   op = "renameFunction"
	opRenameTrigger    op = "renameTrigger"
	opCreateTable      op = "createTable"
	opAddColumn        op = "addColumn"
	opDropNotNull      op = "dropNotNull"
	opSetDefault       op = "setDefault"
	opAlterColumnType  op = "alterColumnType"
	opRegenerateColumn op = "regenerateColumn"
	opCreateIndex      op = "createIndex"
	opReplaceIndex     op = "replaceIndex"
	opAddUnique        op = "addUnique"
	opAddForeignKey    op = "addForeignKey"
	opReplaceFK        op = "replaceForeignKey"
	opCreateFunction   op = "createFunction"
	opReplaceFunction  op = "replaceFunction"
	opCreateTrigger    op = "createTrigger"
	opReplaceTrigger   op = "replaceTrigger"
	opSeedHistory      op = "seedHistory"
	opCreateView       op = "createView"
	opReplaceView      op = "replaceView"
	opCommentOnTable   op = "commentOnTable"
	opGraphContent     op = "changeGraphContent"
	opSetNotNull       op = "setNotNull"
	opDropDefault      op = "dropDefault"
	opDropView         op = "dropView"
	opDropTrigger      op = "dropTrigger"
	opDropFunction     op = "dropFunction"
	opDropForeignKey   op = "dropForeignKey"
	opDropUnique       op = "dropUnique"
	opDropIndex        op = "dropIndex"
	opDropColumn       op = "dropColumn"
	opDropTable        op = "dropTable"
)

// rank is a change's place in its phase. Expand runs renames, then creates
// and adds, then alterations, then indexes, constraints, functions,
// triggers, views and comments, then the changes of a version graph's
// content that change no table. Contract runs tightenings, then drops in
// reverse dependency order (D27).
var rank = map[op]int{
	opCreateExtension:  0,
	opCreateSchema:     1,
	opRenameTable:      2,
	opRenameColumn:     3,
	opRenameConstraint: 4,
	opRenameIndex:      4,
	opRenameFunction:   5,
	opRenameTrigger:    6,
	opCreateTable:      7,
	opAddColumn:        8,
	opDropNotNull:      9,
	opSetDefault:       9,
	opAlterColumnType:  10,
	opRegenerateColumn: 10,
	opCreateIndex:      11,
	opReplaceIndex:     11,
	opAddUnique:        12,
	opAddForeignKey:    13,
	opReplaceFK:        13,
	opCreateFunction:   14,
	opReplaceFunction:  14,
	opCreateTrigger:    15,
	opReplaceTrigger:   15,
	opSeedHistory:      15,
	opCreateView:       16,
	opReplaceView:      16,
	opCommentOnTable:   17,
	opGraphContent:     18,

	opSetNotNull:     20,
	opDropDefault:    20,
	opDropView:       30,
	opDropTrigger:    31,
	opDropFunction:   32,
	opDropForeignKey: 33,
	opDropUnique:     34,
	opDropIndex:      35,
	opDropColumn:     36,
	opDropTable:      37,
}

// change is one difference between two models, in terms every dialect
// shares. Which fields are set depends on op; names are the database's at
// the time the change runs, so a table renamed in expand goes by its new
// name in every later change.
type change struct {
	op      op
	phase   Phase
	subject string
	// order breaks ties between changes of one rank: dropped tables go in
	// reverse dependency order, a generated column is added after the
	// plain columns and dropped before them, since its expression reads
	// them, and everything else goes by subject.
	order int

	// table is the name of the table the change is on; created is set when
	// the plan creates it, so it is empty and takes the plain forms.
	table   string
	created bool

	// name and oldName are the object's names for a rename, a schema or an
	// extension.
	name, oldName string

	tableDef *Table
	// dropsWith are the other tables of a reference cycle a dropTable
	// change drops with tableDef, for a dialect that cannot drop the
	// foreign key that closes the cycle first.
	dropsWith []*Table

	column     *Column
	constraint *Constraint
	foreignKey *ForeignKey
	oldFK      *ForeignKey
	index      *Index
	function   *Function
	oldFunc    *Function
	trigger    *Trigger
	oldTrigger *Trigger
	view       *View
	oldView    *View

	// alter is a column type or generated column change, with what it has
	// to take down and put back in the same step.
	alter *alteration

	// seed is a history table to fill from its source table, with the
	// triggers that record every later write.
	seed *historySeed

	// partitioned is set when the change's table is partitioned.
	partitioned bool

	hazards []*Hazard
}

// alteration is one or more column changes that run in one step because
// Postgres refuses each while a view or a generated column depends on the
// column: the views that read the columns are dropped first and created
// again last, and the generated columns that read them are dropped and
// added again around the type changes.
type alteration struct {
	retypes     []*retype
	regenerate  []*regeneration
	dropViews   []*View // as the previous model has them
	createViews []*View // as the new model has them
}

// retype changes one column's type.
type retype struct {
	table  string
	before *Column
	after  *Column
	conv   conversion
}

// regeneration drops a generated column and adds it again, with its new
// expression.
type regeneration struct {
	table  string
	before *Column
	after  *Column
}

// historySeed fills a new history table with one image per existing row of
// a table that becomes versioned, in the transaction that creates the
// table's history triggers.
type historySeed struct {
	source  *Table // as the new model has it
	history *Table
	// triggers are the source table's history triggers; replaced are those
	// of them a trigger of the same name already holds (the bump trigger of
	// a table that was @optimistic).
	triggers []*Trigger
	replaced map[string]bool
}

// subjects build Step.Subject paths.
func tableSubject(table string) string { return "table/" + table }

func columnSubject(table, column string) string {
	return "table/" + table + "/column/" + column
}

func indexSubject(table, index string) string {
	return "table/" + table + "/index/" + index
}

func constraintSubject(table, constraint string) string {
	return "table/" + table + "/constraint/" + constraint
}

func functionSubject(name string) string { return "function/" + name }

func triggerSubject(table, name string) string {
	return "trigger/" + table + "/" + name
}

func viewSubject(v *View) string { return "view/" + v.Schema + "." + v.Name }

// addHazard adds a hazard of class to c, or appends reason to the one c
// already has of that class and reader.
func (c *change) addHazard(class HazardClass, reader, reason string) {
	for _, h := range c.hazards {
		if h.Class == class && h.Reader == reader {
			if !strings.Contains(h.Reason, reason) {
				h.Reason += " " + reason
			}
			return
		}
	}
	c.hazards = append(c.hazards, &Hazard{
		ID:      HazardID(class, c.subject, reader),
		Class:   class,
		Subject: c.subject,
		Reader:  reader,
		Reason:  reason,
	})
}
