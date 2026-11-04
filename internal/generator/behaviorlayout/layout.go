// Package behaviorlayout is the layout of the tables that compose behaviors
// (D103 in docs/DECISIONS.md): how a host's adapter maps the pure core's
// logical names onto a build-time project's tables and columns. A
// behavior adds no code to any adapter or generator; each reads the layout
// document this package writes, which the generated TypeScript ORM embeds
// in its orm.ts and the HTTP runtime's adapter
// (runtime/http/typescript/src/rules.ts) reads.
//
// The layout rule lives here and nowhere else in Go: [OwnedFieldName] and
// [OwnedColumn] name the field and the column a behavior's declared field
// takes on a table, the loader's pass that adds the owned fields calls
// them, and [Of] reads the fields that pass added. The core's README
// (runtime/engine/rust/rules/README.md, "Logical names and a host's
// layout") states the same rule for the adapters of every language.
//
// The document of a schema is a [Layout]: each table that composes
// behaviors as a table, implemented or anchored on its fields, by its
// type's name. For an Order whose status is a Workflow:
//
//	{
//	  "types": {
//	    "Order": {
//	      "type": "Order",
//	      "table": "order",
//	      "key": { "field": "id", "column": "id", "type": "Identity.UUID" },
//	      "version": "_version",
//	      "live": { "column": "deleted_at" },
//	      "own": [
//	        { "field": "title", "column": "title", "type": "string" }
//	      ],
//	      "uses": [
//	        {
//	          "id": "status",
//	          "behavior": "Workflow",
//	          "anchor": "status",
//	          "member": "status",
//	          "fields": [
//	            { "name": "status", "field": "status", "column": "status", "type": "string", "value": true },
//	            { "name": "enteredAt", "field": "statusEnteredAt", "column": "status_entered_at", "type": "Temporal.DateTime", "convert": "epochMillis" }
//	          ],
//	          "operations": [ { "name": "transition", "writes": true } ]
//	        }
//	      ],
//	      "spec": {
//	        "name": "Order",
//	        "uses": [
//	          {
//	            "behavior": "Workflow",
//	            "config": { "initial": "draft", "states": ["draft", "paid", "shipped"], "transitions": [{ "from": "draft", "to": "paid" }, { "from": "paid", "to": "shipped" }] },
//	            "anchor": { "field": "status" }
//	          }
//	        ],
//	        "fields": [
//	          { "name": "title", "type": "string" }
//	        ]
//	      }
//	    }
//	  }
//	}
//
// A use the table implements, `implements Timers<Ticket, { ... }>`, has no
// anchor and sits under its member (timers); each sub-table its behavior
// declares is in the use's tables, by its declared name, with its table,
// its column of the row's key, its key and its fields; a table whose
// behaviors arm deadlines names its deadline queue's table and columns in
// deadlines (testdata/fixture-timers-db.layout.json is the whole document
// of such a schema); and a table whose writes its behaviors hear, or
// another table's do through a relation of it, names its change queue's
// table and columns in changes, and its spec's fields name each to-one
// relation an own field holds the key of
// (testdata/fixture-reactions-db.layout.json); and a table with a scalar
// createdBy that a Workflow transition's by names names it in audit, which
// a row read holds among its own fields and its spec's fields list after
// them (testdata/fixture-workflow-principals-db.layout.json).
package behaviorlayout

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// VersionColumn is the column a table that composes a behavior holds each
// row's version in: @optimistic's _version, which a trigger bumps on every
// update (BEFORE UPDATE on Postgres, AFTER UPDATE on SQLite and D1) and
// every change set holds a written row to (D103).
const VersionColumn = "_version"

// Convert says how an adapter converts a field's value between the core's
// JSON and the column that holds it.
type Convert string

const (
	// ConvertNone is a column that holds the core's value as it is: a
	// string, a number or a boolean, and a JSON column its JSON.
	ConvertNone Convert = ""

	// ConvertEpochMillis is a timestamp the core holds as epoch
	// milliseconds, the time a command runs at being one, in a column of a
	// date-time type: on Postgres a TIMESTAMPTZ, bound and read as a Date;
	// on SQLite a TEXT of the instant as Date.prototype.toISOString writes
	// it, read back with Date.parse.
	ConvertEpochMillis Convert = "epochMillis"
)

// ConvertOf is how an adapter converts a value of the IR type typeName: a
// date-time type ([codegen.GuessScalarTraits]) is epoch milliseconds in
// the core, and every other type is its value as it is.
func ConvertOf(typeName string) Convert {
	if codegen.GuessScalarTraits(typeName).IsDateTimeLike {
		return ConvertEpochMillis
	}
	return ConvertNone
}

// convertOfRef is how an adapter converts a field of type ref: ConvertOf
// its type for a value, and none for a list or a map, whose elements no
// adapter converts, so a list of date-times reaches the core as the column
// holds it.
func convertOfRef(ref ir.TypeRef) Convert {
	if ref.IsArray || ref.IsMap {
		return ConvertNone
	}
	return ConvertOf(ref.Name)
}

// Layout is the layout document of a DB schema: each table that composes a
// behavior as a table (ir.TypeDef.TableBehaviors: implements and field
// uses), by its type's name. A table with none is not in it, and a schema
// with none has no document.
type Layout struct {
	Types map[string]*Table `json:"types"`
}

// Table is the layout of one table that composes behaviors: where the core's
// logical names of its rows live.
type Table struct {
	// Type is the type's name, as the core names it ([TypeSpec.Name]).
	Type string `json:"type"`

	// Table is the table, unquoted (sqlutil.TableName).
	Table string `json:"table"`

	// Key is the primary key: the field the core's row key names, its
	// column (sqlutil.KeyColumnOf) and its IR type, which the adapter
	// converts the core's string key to.
	Key Column `json:"key"`

	// Version is the column each row's version is in, [VersionColumn].
	Version string `json:"version"`

	// Live is the condition a row of a soft-deletable table is live under,
	// which the repository's reads hold its rows to; absent for a table
	// without deletedAt. The adapter reads and writes a live row alone, so
	// no command reaches a row the repository holds deleted: the core
	// refuses it as not_found, and a soft delete made under a command
	// moves the row (D103).
	Live *Live `json:"live,omitempty"`

	// Own are the type's own fields the core reads and writes: each a
	// column of the table, in column order, that a row's own fields
	// (superschematic_rules::Row.own), a create's own fields and an
	// update's patch name by Field. The key, the version, the owned fields,
	// the audit fields and internal metadata are not among them, and
	// neither is a relation's list, which holds no column. A to-one
	// relation's column is its target's key, which Field names as a
	// generated row holds it, <field>Id, typed as the target's key.
	Own []Column `json:"own"`

	// Audit are the audit fields of the table the core reads beside its own
	// fields and no command writes: createdBy (D87), a scalar column the
	// repositories stamp with an operation's caller, when a Workflow
	// transition's by or a StateRules state's by names it, or an Approvals
	// gate excludes the creator (D104). A row read holds each in its own
	// fields by Field; a create's own fields, an update's patch and a change
	// set never carry one, and a write's image (ImageColumns) leaves it out.
	// Absent for a table without a scalar createdBy, and for one whose
	// behaviors read it of none.
	Audit []Column `json:"audit,omitempty"`

	// Uses are the type's table uses, implements and field uses, in the
	// type's list order.
	Uses []Use `json:"uses"`

	// Deadlines is where the deadlines its behaviors arm are kept, the
	// queue the loader adds (D103); absent for a table whose behaviors arm
	// none.
	Deadlines *Deadlines `json:"deadlines,omitempty"`

	// Changes is the queue the table's change trigger writes a message of
	// for each write its behaviors hear (D103); absent for a table nothing
	// hears.
	Changes *Changes `json:"changes,omitempty"`

	// Spec is the type as the core runs it.
	Spec TypeSpec `json:"spec"`
}

// Deadlines is the queue a table's deadlines are messages of (D103): one
// live message per row, use and deadline, due when the deadline falls
// due, which an adapter arms and clears as a change set's deadline changes
// say and a behavior worker claims. Each column is unquoted.
type Deadlines struct {
	// Queue is the queue's type: TicketDeadline.
	Queue string `json:"queue"`
	// Table is the queue's table: ticket_deadline_queue.
	Table string `json:"table"`
	// RowKey is the column of the row's key, which binds as the row
	// table's key binds.
	RowKey string `json:"rowKey"`
	// Use is the column of the use's id.
	Use string `json:"use"`
	// Name is the column of the deadline's name.
	Name string `json:"name"`
	// At is the column of the time it was armed to fall due at, epoch
	// milliseconds.
	At string `json:"at"`
	// Payload is the column of the arming's payload, JSON.
	Payload string `json:"payload"`
}

// Changes is a hearing table's change queue (D103), each column unquoted:
// one message per write of a row, whoever makes it, which the table's
// change trigger writes and a behavior worker claims and runs through the
// core's change command.
type Changes struct {
	// Queue is the queue's type: TicketChange.
	Queue string `json:"queue"`
	// Table is the queue's table: ticket_change_queue.
	Table string `json:"table"`
	// RowKey is the column of the written row's key, which binds as the
	// row table's key binds.
	RowKey string `json:"rowKey"`
	// Write is the column of what the write did: insert, update or delete.
	Write string `json:"write"`
	// At is the column of the write's time, epoch milliseconds by the
	// database's clock.
	At string `json:"at"`
	// Before is the column of the row's image before the write, JSON by
	// column; null for an insert.
	Before string `json:"before"`
	// After is the column of the row's image after the write, JSON by
	// column; null for a delete.
	After string `json:"after"`
}

// Live is the condition a live row of a soft-deletable table meets: its
// deletedAt column is NULL.
type Live struct {
	// Column is the deletedAt column, unquoted.
	Column string `json:"column"`
}

// Column is a field of a table and the column that holds it.
type Column struct {
	// Field is the field's name in the IR: the member a generated row
	// holds it under, and the name the core gives an own field.
	Field string `json:"field"`

	// Column is its column, unquoted.
	Column string `json:"column"`

	// Type is its IR type's name (string, number, boolean, or a scalar
	// such as Temporal.DateTime or Identity.UUID).
	Type string `json:"type"`

	// Convert is how the adapter converts its value; absent for none, and
	// for a list or a map, whose depth the column does not carry.
	Convert Convert `json:"convert,omitempty"`
}

// Use is one use of a behavior on a table: implemented by the table, or
// anchored on one of its fields.
type Use struct {
	// ID is the use's id, by which the core names its owned fields,
	// sub-tables and operations: its anchor field's name, or the
	// behavior's name for an implements use.
	ID string `json:"id"`

	// Behavior is the behavior's registered name.
	Behavior string `json:"behavior"`

	// Anchor is the anchor field's name; an implements use, anchored on
	// the type, has none.
	Anchor string `json:"anchor,omitempty"`

	// Member is the name the use's operations sit under in a repository and
	// an SDK ([UseMember]): the anchor field, or the behavior's storage key
	// in camel case for an implements use (timers).
	Member string `json:"member"`

	// Fields are the behavior's declared fields that take a column, each
	// the column it takes on the table, in the declaration's order. An
	// untyped field of an implements use (Timers' deadlines, a read the
	// behavior computes) takes none and is not here.
	Fields []OwnedField `json:"fields"`

	// Operations are the behavior's declared operations on a row, in the
	// declaration's order: each a method of the generated repository under
	// the use's member (orm.order.status.transition,
	// orm.ticket.timers.fireTimer).
	Operations []Operation `json:"operations"`

	// Tables are the sub-tables the behavior keeps per row, in the
	// declaration's order; absent when it declares none.
	Tables []SubTable `json:"tables,omitempty"`
}

// SubTable is one sub-table a use keeps per row (D103): a child table of
// the row the loader adds, keyed within the row by its declared key.
type SubTable struct {
	// Name is the table's name in the behavior's declaration, as the core
	// names it in a read and a change set (deadlines).
	Name string `json:"name"`

	// Table is the table, unquoted (ticket_timers_deadlines).
	Table string `json:"table"`

	// Row is the column holding the row's key, which binds as the row
	// table's key binds.
	Row string `json:"row"`

	// Key is the declared key field and its column.
	Key OwnedField `json:"key"`

	// Fields are its other declared fields and their columns, in the
	// declaration's order.
	Fields []OwnedField `json:"fields"`
}

// OwnedField is a behavior's declared field and the column it takes.
type OwnedField struct {
	// Name is the field's declared name, as the core names it in a row's
	// owned fields and a change set's (enteredAt).
	Name string `json:"name"`

	Column

	// Value is true for the use's value field, the anchor field itself.
	Value bool `json:"value,omitempty"`
}

// Operation is one operation a behavior declares on a row: its name, and
// whether it writes, as the core's declaration has it.
type Operation struct {
	Name   string `json:"name"`
	Writes bool   `json:"writes"`
}

// TypeSpec is the type as the core runs it: superschematic_rules::TypeSpec
// in its JSON shape, which serde writes in camelCase. Uses are in the
// type's list order, and Fields are its own fields ([OwnFields]) and then
// its audit fields ([AuditFields]), what a config that names a field is
// held to (a Timers field start, a Workflow transition's by).
type TypeSpec struct {
	Name   string      `json:"name"`
	Uses   []UseSpec   `json:"uses"`
	Fields []FieldSpec `json:"fields,omitempty"`
}

// FieldSpec is one own field as the core takes it,
// superschematic_rules::FieldSpec: its name, its IR type's name, whether
// its values are instants, epoch milliseconds from the adapter, and the
// to-one relation whose target's key it holds, if any (D103).
type FieldSpec struct {
	Name     string        `json:"name"`
	Type     string        `json:"type"`
	DateTime bool          `json:"dateTime,omitempty"`
	Relation *RelationSpec `json:"relation,omitempty"`
}

// RelationSpec is a to-one relation an own field holds the key of, as the
// core's spec names it: the relation field's name and its target type.
type RelationSpec struct {
	Name string `json:"name"` // project
	Type string `json:"type"` // Project
}

// UseSpec is one use as the core takes it, superschematic_rules::UseSpec:
// the behavior's name, the config the schema gives it, absent when it
// gives none or gives {} (ir.CanonicalizeBehaviors stores {} as none,
// which the core checks as {}), and the anchor.
type UseSpec struct {
	Behavior string          `json:"behavior"`
	Config   json.RawMessage `json:"config,omitempty"`
	Anchor   Anchor          `json:"anchor"`
}

// Anchor is superschematic_rules::Anchor: the whole type, which writes as
// "type", or one field, which writes as {"field": "<name>"}.
type Anchor struct {
	// Field is the anchor field's name; empty for the whole type.
	Field string
}

// MarshalJSON writes the anchor as serde writes the core's: "type", or
// {"field": name}.
func (a Anchor) MarshalJSON() ([]byte, error) {
	if a.Field == "" {
		return []byte(`"type"`), nil
	}
	return json.Marshal(map[string]string{"field": a.Field})
}

// UnmarshalJSON reads the anchor as serde reads the core's.
func (a *Anchor) UnmarshalJSON(data []byte) error {
	var whole string
	if json.Unmarshal(data, &whole) == nil {
		if whole != "type" {
			return fmt.Errorf("behaviorlayout: anchor %q is not \"type\" or {\"field\": name}", whole)
		}
		a.Field = ""
		return nil
	}
	var field struct {
		Field string `json:"field"`
	}
	if err := json.Unmarshal(data, &field); err != nil || field.Field == "" {
		return fmt.Errorf("behaviorlayout: anchor %s is not \"type\" or {\"field\": name}", data)
	}
	a.Field = field.Field
	return nil
}

// OwnedFieldName is the name of the field a behavior's declared field takes
// on a type that carries the behavior on the field anchor (D103): the
// value field is the anchor field itself, and every other field is the
// anchor's name with the declared name after it in Pascal case (status and
// enteredAt: statusEnteredAt). The loader's pass names each owned field it
// adds by it.
func OwnedFieldName(anchor, declared string, value bool) string {
	if value {
		return anchor
	}
	return anchor + codegen.ToPascalCase(declared)
}

// OwnedColumn is the column a behavior's declared field takes on a table
// (D103), the layout rule of runtime/engine/rust/rules/README.md. On a
// field anchor f the value field is f's column and every other field is
// f's column, an underscore and the declared name in snake case (Workflow
// on status: status, status_entered_at). On a type anchor every field is
// the behavior's storage key ([StorageKey]), an underscore and the
// declared name in snake case (workflow_status, workflow_entered_at), so
// an own field of the same name sits beside it.
func OwnedColumn(use ir.BehaviorRef, declared string, value bool) string {
	if use.FieldAnchored() {
		column := codegen.ToSnakeCase(use.Field)
		if value {
			return column
		}
		return column + "_" + codegen.ToSnakeCase(declared)
	}
	return StorageKey(use.Name) + "_" + codegen.ToSnakeCase(declared)
}

// UseMember is the name a use's operations sit under in a repository and
// an SDK, and the use's sub-tables' names begin with (D103): its anchor
// field for a field use, the behavior's storage key in camel case for a
// use anchored on the type (Timers: timers; acme.Rating: acmeRating). The
// loader, the operations' expansion and the ORM generators call it.
func UseMember(use ir.BehaviorRef) string {
	if use.FieldAnchored() {
		return use.Field
	}
	return codegen.ToCamelCase(StorageKey(use.Name))
}

// SubTableTypeName is the name of the type the loader adds for the
// sub-table table of the use on the table row (D103): the row's type, the
// use's member and the table's name, in Pascal case
// (TicketTimersDeadlines). Its table is the name in snake case
// (ticket_timers_deadlines).
func SubTableTypeName(row string, use ir.BehaviorRef, table string) string {
	return row + codegen.ToPascalCase(UseMember(use)) + codegen.ToPascalCase(table)
}

// DeadlineQueueTypeName is the name of the queue the loader adds for the
// deadlines the behaviors of the table row arm (D103): TicketDeadline,
// whose table is ticket_deadline_queue.
func DeadlineQueueTypeName(row string) string {
	return row + "Deadline"
}

// ChangeQueueTypeName is the name of the queue the loader adds for the
// writes of the table row that its behaviors hear (D103): TicketChange,
// whose table is ticket_change_queue.
func ChangeQueueTypeName(row string) string { return row + "Change" }

// The fields of the change queue's message (D103): the written row's key,
// what the write did, when, and the row's image before and after it.
const (
	ChangeRowKeyField = "rowKey"
	ChangeWriteField  = "write"
	ChangeAtField     = "at"
	ChangeBeforeField = "before"
	ChangeAfterField  = "after"
)

// The fields of the deadline queue's message, by the core's names of a
// deadline (D103): the row's key, the use's id, the deadline's name, the
// time it was armed to fall due at and the arming's payload.
const (
	DeadlineRowKeyField  = "rowKey"
	DeadlineUseField     = "useId"
	DeadlineNameField    = "name"
	DeadlineAtField      = "at"
	DeadlinePayloadField = "payload"
)

var notLowerAlphanumeric = regexp.MustCompile(`[^a-z0-9]+`)

// StorageKey is a behavior's name as a type anchor's columns begin with
// it: lowercased, each run of other characters one underscore (Workflow:
// workflow; acme.Rating: acme_rating).
func StorageKey(behavior string) string {
	return strings.Trim(notLowerAlphanumeric.ReplaceAllString(strings.ToLower(behavior), "_"), "_")
}

// Of is the layout document of schema, a DB schema: [TableOf] of each
// table that composes a behavior as a table, by its type's name. It
// returns nil for a schema with none.
func Of(schema *ir.Schema, reg *registry.Registry) (*Layout, error) {
	if schema == nil {
		return nil, nil
	}
	var tables []sqlutil.TableModel
	var layout *Layout
	for _, td := range sqlutil.TableTypes(schema) {
		if len(td.TableBehaviors()) == 0 {
			continue
		}
		if tables == nil {
			var err error
			if tables, err = sqlutil.TablesOf(schema, nil); err != nil {
				return nil, err
			}
		}
		table, err := tableOf(schema, td, tables, reg)
		if err != nil {
			return nil, err
		}
		if layout == nil {
			layout = &Layout{Types: map[string]*Table{}}
		}
		layout.Types[td.Name] = table
	}
	return layout, nil
}

// TableOf is the layout of td, a table of schema that composes behaviors
// as a table, from the fields the loader's pass added
// (ir.FieldDef.Behavior), the sub-tables and the deadline queue it added
// (ir.TypeDef.BehaviorTable and BehaviorQueue), their columns
// (sqlutil.ColumnName) and the registry's declarations of its behaviors
// (each declared field, the value field, the tables, the operations). It
// fails when a declared typed field has no owned field on td, or a
// declared table no sub-table, which the loader's pass makes impossible,
// so a failure is a defect of the build.
func TableOf(schema *ir.Schema, td *ir.TypeDef, reg *registry.Registry) (*Table, error) {
	tables, err := sqlutil.TablesOf(schema, nil)
	if err != nil {
		return nil, err
	}
	return tableOf(schema, td, tables, reg)
}

// tableOf is TableOf over the tables sqlutil models of schema.
func tableOf(schema *ir.Schema, td *ir.TypeDef, tables []sqlutil.TableModel, reg *registry.Registry) (*Table, error) {
	if reg == nil {
		return nil, fmt.Errorf("behaviorlayout: table %s composes behaviors, and no registry declares them", td.Name)
	}
	var model *sqlutil.TableModel
	for i := range tables {
		if tables[i].Type == td.Name {
			model = &tables[i]
		}
	}
	if model == nil {
		return nil, fmt.Errorf("behaviorlayout: %s is not a table of schema %s", td.Name, schema.Name)
	}
	if !model.HasVersion {
		return nil, fmt.Errorf("behaviorlayout: table %s composes behaviors and keeps no %s column, which the loader gives it", td.Name, VersionColumn)
	}
	own, relations := ownColumnsOf(schema, model)
	t := &Table{Type: td.Name, Table: model.Name, Version: VersionColumn, Own: own, Audit: readAudit(td, model), Uses: []Use{}}
	if deletedAt := model.AuditColumn(sqlutil.AuditDeletedAt); model.SoftDelete && deletedAt != nil {
		t.Live = &Live{Column: deletedAt.Name}
	}
	for _, c := range model.Columns {
		if c.Key {
			t.Key = Column{Field: c.Field, Column: c.Name, Type: keyType(c)}
			t.Key.Convert = ConvertOf(t.Key.Type)
		}
	}
	if t.Key.Column == "" {
		return nil, fmt.Errorf("behaviorlayout: table %s has no key column", td.Name)
	}
	t.Spec = TypeSpec{Name: td.Name, Uses: []UseSpec{}, Fields: fieldSpecs(t.Own, relations)}
	for _, c := range t.Audit {
		t.Spec.Fields = append(t.Spec.Fields, FieldSpec{Name: c.Field, Type: c.Type, DateTime: c.Convert == ConvertEpochMillis})
	}
	armsDeadlines := false
	for _, ref := range td.TableBehaviors() {
		where := fmt.Sprintf("behaviorlayout: table %s: field %s", td.Name, ref.Field)
		if !ref.FieldAnchored() {
			where = fmt.Sprintf("behaviorlayout: table %s: implements %s", td.Name, ref.Name)
		}
		behavior, ok := reg.Behavior(ref.Name)
		if !ok {
			return nil, fmt.Errorf("%s: behavior %s is not registered", where, ref.Name)
		}
		use := Use{ID: ref.UseID(), Behavior: ref.Name, Anchor: ref.Field, Member: UseMember(ref), Fields: []OwnedField{}, Operations: []Operation{}}
		for _, declared := range behavior.Fields {
			// A volatile field is computed at a read, and so is an
			// untyped one of a use the table implements (Timers'
			// deadlines): neither takes a column.
			if declared.Volatile || (declared.Type == "" && !ref.FieldAnchored()) {
				continue
			}
			value := declared.Name == behavior.Value
			fd := ownedField(td, ref, declared.Name)
			if fd == nil {
				return nil, fmt.Errorf("%s: behavior %s's field %s has no owned field on the table", where, ref.Name, declared.Name)
			}
			column := sqlutil.ColumnName(fd)
			if want := OwnedColumn(ref, declared.Name, value); column != want {
				return nil, fmt.Errorf("%s: behavior %s's field %s is the column %s, and the layout rule names it %s", where, ref.Name, declared.Name, column, want)
			}
			use.Fields = append(use.Fields, OwnedField{
				Name:   declared.Name,
				Column: Column{Field: fd.Name, Column: column, Type: fd.TypeRef.Name, Convert: convertOfRef(fd.TypeRef)},
				Value:  value,
			})
		}
		for _, op := range behavior.Operations {
			if op.Scope != "" && op.Scope != registry.OperationScopeInstance {
				continue
			}
			use.Operations = append(use.Operations, Operation{Name: op.Name, Writes: op.Writes})
		}
		for _, declared := range behavior.Tables {
			sub, err := subTableOf(schema, td, ref, declared, tables)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", where, err)
			}
			use.Tables = append(use.Tables, sub)
		}
		armsDeadlines = armsDeadlines || behavior.ArmsDeadlines
		t.Uses = append(t.Uses, use)
		spec := UseSpec{Behavior: ref.Name, Anchor: Anchor{Field: ref.Field}}
		if len(ref.Config) > 0 {
			spec.Config = ref.Config
		}
		t.Spec.Uses = append(t.Spec.Uses, spec)
	}
	if armsDeadlines {
		deadlines, err := deadlinesOf(schema, td)
		if err != nil {
			return nil, err
		}
		t.Deadlines = deadlines
	}
	if queue := schema.BehaviorQueueOf(td.Name, ir.BehaviorQueueChanges); queue != nil {
		changes, err := changesOf(td, queue)
		if err != nil {
			return nil, err
		}
		t.Changes = changes
	}
	return t, nil
}

// ownColumns are the own fields of the table model holds, of schema: each
// column but the key, the version, the owned fields, the audit fields,
// internal metadata and the generated ones, in column order. A to-one
// relation's column holds its target's key, which a generated row holds as
// <field>Id, typed as the target's key.
func ownColumns(schema *ir.Schema, model *sqlutil.TableModel) []Column {
	own, _ := ownColumnsOf(schema, model)
	return own
}

// ownColumnsOf is ownColumns with, beside each column, the to-one relation
// whose target's key it holds (its relation field and target type), nil
// for any other column.
func ownColumnsOf(schema *ir.Schema, model *sqlutil.TableModel) ([]Column, []*RelationSpec) {
	own := []Column{}
	var relations []*RelationSpec
	for _, c := range model.Columns {
		switch {
		case c.Key, c.Def == nil, c.Def.Owned(), c.Audit != "", c.Internal:
		case c.Relation != "":
			column := Column{Field: c.Field + "Id", Column: c.Name, Type: keyType(c)}
			if target := schema.Types[c.Relation]; target != nil {
				for _, f := range target.Fields {
					if f != nil && f.Key {
						column.Type = f.TypeRef.Name
					}
				}
			}
			column.Convert = ConvertOf(column.Type)
			own = append(own, column)
			relations = append(relations, &RelationSpec{Name: c.Field, Type: c.Relation})
		default:
			own = append(own, Column{Field: c.Field, Column: c.Name, Type: c.Def.TypeRef.Name, Convert: convertOfRef(c.Def.TypeRef)})
			relations = append(relations, nil)
		}
	}
	return own, relations
}

// OwnFields are td's own fields, a table of schema, as the layout's own
// holds them (D103): each column but the key, the version, the fields a
// behavior owns, the audit fields, internal metadata and the generated
// ones, in column order. A config that names a field is held to them, by
// the core at a command (the spec's fields) and by the loader's checks
// before it (a Timers field start), so both list the same names. It is nil
// when schema's tables cannot be modelled, which the build reports
// elsewhere.
func OwnFields(schema *ir.Schema, td *ir.TypeDef) []Column {
	if schema == nil || td == nil {
		return nil
	}
	tables, err := sqlutil.TablesOf(schema, nil)
	if err != nil {
		return nil
	}
	for i := range tables {
		if tables[i].Type == td.Name {
			return ownColumns(schema, &tables[i])
		}
	}
	return nil
}

// SpecFields are the fields of td, a table of schema, the core's spec of
// the type lists (D103, D104): [OwnFields] in column order, then the
// layout's audit, createdBy when a use reads it. A refusal of a config that
// names a field the type lacks names them as the core's does ("its fields:
// ..."), so the loader's checks that word it as the core does list these.
// It is nil when schema's tables cannot be modelled, which the build
// reports elsewhere.
func SpecFields(schema *ir.Schema, td *ir.TypeDef) []Column {
	model := modelOf(schema, td)
	if model == nil {
		return nil
	}
	return append(ownColumns(schema, model), readAudit(td, model)...)
}

// auditColumns are the audit fields of the table model holds that the core
// reads (D104): its createdBy, when it is a scalar column of the type's own
// (no relation, no list, no map), which the repositories stamp with an
// operation's caller (D87). updatedBy, deletedBy and the audit times are
// not among them.
func auditColumns(model *sqlutil.TableModel) []Column {
	c := model.AuditColumn(sqlutil.AuditCreatedBy)
	if c == nil || c.Def == nil || c.Relation != "" || c.Def.TypeRef.IsArray || c.Def.TypeRef.IsMap {
		return nil
	}
	return []Column{{Field: sqlutil.AuditCreatedBy, Column: c.Name, Type: c.Def.TypeRef.Name, Convert: convertOfRef(c.Def.TypeRef)}}
}

// readAudit are the audit columns of the table model holds that the
// layout's audit names (D104): auditColumns when a use of td reads
// createdBy, and none otherwise, so a table whose behaviors read no audit
// field converts none in its row reads. A use reads it when a Workflow
// transition's by names it, when a StateRules state's by names it, and when
// an Approvals gate excludes the creator (excludeCreator absent or true),
// which creator_cannot_sign reads it for.
func readAudit(td *ir.TypeDef, model *sqlutil.TableModel) []Column {
	for _, ref := range td.TableBehaviors() {
		if len(ref.Config) > 0 && readsCreatedBy(ref) {
			return auditColumns(model)
		}
	}
	return nil
}

// readsCreatedBy reports whether the table use ref reads the row's
// createdBy (D104): a Workflow whose transition's by names it, a StateRules
// whose state's by names it, or an Approvals with a gate that excludes the
// creator. A config that does not read reads none; the loader refuses it.
func readsCreatedBy(ref ir.BehaviorRef) bool {
	switch ref.Name {
	case "Workflow":
		var config struct {
			Transitions []struct {
				By []string `json:"by"`
			} `json:"transitions"`
		}
		if json.Unmarshal(ref.Config, &config) != nil {
			return false
		}
		for _, transition := range config.Transitions {
			if slices.Contains(transition.By, sqlutil.AuditCreatedBy) {
				return true
			}
		}
	case "StateRules":
		var config struct {
			States map[string]struct {
				By []string `json:"by"`
			} `json:"states"`
		}
		if json.Unmarshal(ref.Config, &config) != nil {
			return false
		}
		for _, rule := range config.States {
			if slices.Contains(rule.By, sqlutil.AuditCreatedBy) {
				return true
			}
		}
	case "Approvals":
		var config struct {
			Gates map[string]struct {
				ExcludeCreator *bool `json:"excludeCreator"`
			} `json:"gates"`
		}
		if json.Unmarshal(ref.Config, &config) != nil {
			return false
		}
		for _, gate := range config.Gates {
			if gate.ExcludeCreator == nil || *gate.ExcludeCreator {
				return true
			}
		}
	}
	return false
}

// AuditFields are td's audit fields, a table of schema, that a Workflow
// transition's by or a StateRules state's by may name, and an Approvals gate
// that excludes the creator reads (D104): its createdBy, when it is a scalar
// column, which the layout's audit names when a use reads it. A by is held to
// them beside [OwnFields], by the core at a command (the spec's fields) and
// by the loader's checks before it, so both list the same names. It is nil
// for a table without one, and when schema's tables cannot be modelled,
// which the build reports elsewhere.
func AuditFields(schema *ir.Schema, td *ir.TypeDef) []Column {
	model := modelOf(schema, td)
	if model == nil {
		return nil
	}
	return auditColumns(model)
}

// fieldSpecs are own's columns as the core's spec holds the type's own
// fields: each by its name and IR type, a date-time (epoch milliseconds
// from the adapter) marked, and a to-one relation's column with its
// relation, relations[i] being own[i]'s.
func fieldSpecs(own []Column, relations []*RelationSpec) []FieldSpec {
	var specs []FieldSpec
	for i, c := range own {
		specs = append(specs, FieldSpec{Name: c.Field, Type: c.Type, DateTime: c.Convert == ConvertEpochMillis, Relation: relations[i]})
	}
	return specs
}

// ImageColumns are the columns of td's rows that its change trigger writes
// as a write's image (D103), unquoted: the layout's own columns and each
// use's owned columns, in the layout's order, a binary, vector or
// geometric column left out. It is nil when schema's tables cannot be
// modelled.
func ImageColumns(schema *ir.Schema, td *ir.TypeDef) []string {
	model := modelOf(schema, td)
	if model == nil {
		return nil
	}
	byName := map[string]sqlutil.ColumnModel{}
	for _, c := range model.Columns {
		byName[c.Name] = c
	}
	// A binary column (BYTEA, a BLOB on SQLite) or a vector is left out:
	// no behavior reads one, and SQLite's json_object refuses a BLOB. So is
	// a POINT, GEOGRAPHY or GEOMETRY column, which Postgres has no equality
	// for: the trigger's comparison of one would fail every write of the
	// table.
	imaged := func(name string) bool {
		c, ok := byName[name]
		if !ok || c.Name == VersionColumn {
			return false
		}
		return !sqlutil.IsBinaryType(c.Type) && (c.Def == nil || c.Def.TypeRef.Name != ir.VectorScalarName) && !sqlutil.IsVectorType(c.Type) && sqlutil.HasEquality(c.Type)
	}
	columns := []string{}
	for _, c := range ownColumns(schema, model) {
		if imaged(c.Column) {
			columns = append(columns, c.Column)
		}
	}
	for _, fd := range td.OwnedFields() {
		if column := sqlutil.ColumnName(fd); imaged(column) {
			columns = append(columns, column)
		}
	}
	return columns
}

// LiveColumn is the column a soft delete of td's rows stamps, deleted_at,
// unquoted, as the layout's live condition names it, or "" for a table
// that is not soft-deletable or when schema's tables cannot be modelled.
// The change trigger hears a write that moves it alone (D103): a soft
// delete, or a restore.
func LiveColumn(schema *ir.Schema, td *ir.TypeDef) string {
	model := modelOf(schema, td)
	if model == nil || !model.SoftDelete {
		return ""
	}
	if deletedAt := model.AuditColumn(sqlutil.AuditDeletedAt); deletedAt != nil {
		return deletedAt.Name
	}
	return ""
}

// modelOf is the table model of td, a table of schema, or nil when it is
// none or schema's tables cannot be modelled.
func modelOf(schema *ir.Schema, td *ir.TypeDef) *sqlutil.TableModel {
	if schema == nil || td == nil {
		return nil
	}
	tables, err := sqlutil.TablesOf(schema, nil)
	if err != nil {
		return nil
	}
	for i := range tables {
		if tables[i].Type == td.Name {
			return &tables[i]
		}
	}
	return nil
}

// subTableOf is the layout of the sub-table declared for the use ref on
// td: the type the loader added for it (ir.TypeDef.BehaviorTable), its
// row's column and each declared field's column.
func subTableOf(schema *ir.Schema, td *ir.TypeDef, ref ir.BehaviorRef, declared registry.BehaviorTable, tables []sqlutil.TableModel) (SubTable, error) {
	var sub *ir.TypeDef
	for _, other := range schema.Types {
		if m := other.BehaviorTable; m != nil && m.Row == td.Name && m.Use == ref.UseID() && m.Table == declared.Name {
			sub = other
		}
	}
	if sub == nil {
		return SubTable{}, fmt.Errorf("behavior %s's table %s has no sub-table in the schema", ref.Name, declared.Name)
	}
	var model *sqlutil.TableModel
	for i := range tables {
		if tables[i].Type == sub.Name {
			model = &tables[i]
		}
	}
	if model == nil {
		return SubTable{}, fmt.Errorf("behavior %s's table %s is the type %s, which is no table of the schema", ref.Name, declared.Name, sub.Name)
	}
	out := SubTable{Name: declared.Name, Table: model.Name, Fields: []OwnedField{}}
	columns := map[string]string{}
	for _, c := range model.Columns {
		if c.Def == nil {
			continue
		}
		if c.Def.Name == sub.BehaviorTable.RowField {
			out.Row = c.Name
			continue
		}
		columns[c.Def.Name] = c.Name
	}
	if out.Row == "" {
		return SubTable{}, fmt.Errorf("behavior %s's table %s, the type %s, has no column of its row %s", ref.Name, declared.Name, sub.Name, sub.BehaviorTable.RowField)
	}
	for _, field := range declared.Fields {
		fd := sqlutil.FieldNamed(sub, field.Name)
		column, ok := columns[field.Name]
		if fd == nil || !ok {
			return SubTable{}, fmt.Errorf("behavior %s's table %s, the type %s, has no column of its field %s", ref.Name, declared.Name, sub.Name, field.Name)
		}
		owned := OwnedField{Name: field.Name, Column: Column{Field: fd.Name, Column: column, Type: fd.TypeRef.Name, Convert: convertOfRef(fd.TypeRef)}}
		if field.Name == declared.Key {
			out.Key = owned
			continue
		}
		out.Fields = append(out.Fields, owned)
	}
	return out, nil
}

// deadlinesOf is the layout of the deadline queue the loader added for td
// (ir.TypeDef.BehaviorQueue).
func deadlinesOf(schema *ir.Schema, td *ir.TypeDef) (*Deadlines, error) {
	queue := schema.BehaviorQueueOf(td.Name, ir.BehaviorQueueDeadlines)
	if queue == nil {
		return nil, fmt.Errorf("behaviorlayout: table %s: its behaviors arm deadlines, and the schema has no deadline queue of it", td.Name)
	}
	column := func(field string) (string, error) {
		fd := sqlutil.FieldNamed(queue, field)
		if fd == nil {
			return "", fmt.Errorf("behaviorlayout: table %s: its deadline queue %s has no field %s", td.Name, queue.Name, field)
		}
		return sqlutil.ColumnName(fd), nil
	}
	out := &Deadlines{Queue: queue.Name, Table: sqlutil.QueueTableName(queue)}
	for _, f := range []struct {
		field string
		into  *string
	}{
		{DeadlineRowKeyField, &out.RowKey},
		{DeadlineUseField, &out.Use},
		{DeadlineNameField, &out.Name},
		{DeadlineAtField, &out.At},
		{DeadlinePayloadField, &out.Payload},
	} {
		c, err := column(f.field)
		if err != nil {
			return nil, err
		}
		*f.into = c
	}
	return out, nil
}

// changesOf is the layout of the change queue the loader added for td
// (ir.TypeDef.BehaviorQueue of kind changes).
func changesOf(td *ir.TypeDef, queue *ir.TypeDef) (*Changes, error) {
	out := &Changes{Queue: queue.Name, Table: sqlutil.QueueTableName(queue)}
	for _, f := range []struct {
		field string
		into  *string
	}{
		{ChangeRowKeyField, &out.RowKey},
		{ChangeWriteField, &out.Write},
		{ChangeAtField, &out.At},
		{ChangeBeforeField, &out.Before},
		{ChangeAfterField, &out.After},
	} {
		fd := sqlutil.FieldNamed(queue, f.field)
		if fd == nil {
			return nil, fmt.Errorf("behaviorlayout: table %s: its change queue %s has no field %s", td.Name, queue.Name, f.field)
		}
		*f.into = sqlutil.ColumnName(fd)
	}
	return out, nil
}

// keyType is the IR type of a key or a relation's column: its field's, or
// Identity.UUID for the id the generator adds to a table without a @key.
func keyType(c sqlutil.ColumnModel) string {
	if c.Def != nil && c.Relation == "" {
		return c.Def.TypeRef.Name
	}
	return "Identity.UUID"
}

// ownedField is td's field that holds the use ref's declared field.
func ownedField(td *ir.TypeDef, ref ir.BehaviorRef, declared string) *ir.FieldDef {
	for _, fd := range td.OwnedFields() {
		if fd.Behavior.Use == ref.UseID() && fd.Behavior.Field == declared {
			return fd
		}
	}
	return nil
}
