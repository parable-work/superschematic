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
// field-anchored behaviors, by its type's name. For an Order whose status
// is a Workflow:
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
//	        ]
//	      }
//	    }
//	  }
//	}
package behaviorlayout

import (
	"encoding/json"
	"fmt"
	"regexp"
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
// field-anchored behavior, by its type's name. A table with none is not in
// it, and a schema with none has no document.
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

	// Uses are the type's field-anchored uses, in the type's list order.
	Uses []Use `json:"uses"`

	// Spec is the type as the core runs it.
	Spec TypeSpec `json:"spec"`
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

// Use is one field-anchored use of a behavior on a table.
type Use struct {
	// ID is the use's id, by which the core names its owned fields,
	// sub-tables and operations: its anchor field's name.
	ID string `json:"id"`

	// Behavior is the behavior's registered name.
	Behavior string `json:"behavior"`

	// Anchor is the anchor field's name. A type anchor, which a table does
	// not carry until D103's step 5, has none.
	Anchor string `json:"anchor,omitempty"`

	// Fields are the behavior's declared fields, each the column it takes
	// on the table, in the declaration's order.
	Fields []OwnedField `json:"fields"`

	// Operations are the behavior's declared operations on a row, in the
	// declaration's order: each a method of the generated repository under
	// the use's id (orm.order.status.transition).
	Operations []Operation `json:"operations"`
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
// type's list order.
type TypeSpec struct {
	Name string    `json:"name"`
	Uses []UseSpec `json:"uses"`
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

var notLowerAlphanumeric = regexp.MustCompile(`[^a-z0-9]+`)

// StorageKey is a behavior's name as a type anchor's columns begin with
// it: lowercased, each run of other characters one underscore (Workflow:
// workflow; acme.Rating: acme_rating).
func StorageKey(behavior string) string {
	return strings.Trim(notLowerAlphanumeric.ReplaceAllString(strings.ToLower(behavior), "_"), "_")
}

// Of is the layout document of schema, a DB schema: [TableOf] of each
// table that composes a field-anchored behavior, by its type's name. It
// returns nil for a schema with none.
func Of(schema *ir.Schema, reg *registry.Registry) (*Layout, error) {
	if schema == nil {
		return nil, nil
	}
	var tables []sqlutil.TableModel
	var layout *Layout
	for _, td := range sqlutil.TableTypes(schema) {
		if len(td.FieldBehaviors()) == 0 {
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

// TableOf is the layout of td, a table of schema that composes field
// behaviors, from the fields the loader's pass added (ir.FieldDef.Behavior),
// their columns (sqlutil.ColumnName) and the registry's declarations of its
// behaviors (each declared field, the value field, the operations). It
// fails when a declared field has no owned field on td, which the
// loader's pass makes impossible, so a failure is a defect of the build.
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
	t := &Table{Type: td.Name, Table: model.Name, Version: VersionColumn, Own: []Column{}, Uses: []Use{}}
	if deletedAt := model.AuditColumn(sqlutil.AuditDeletedAt); model.SoftDelete && deletedAt != nil {
		t.Live = &Live{Column: deletedAt.Name}
	}
	for _, c := range model.Columns {
		switch {
		case c.Key:
			t.Key = Column{Field: c.Field, Column: c.Name, Type: keyType(c)}
			t.Key.Convert = ConvertOf(t.Key.Type)
		case c.Def == nil, c.Def.Owned(), c.Audit != "", c.Internal:
		case c.Relation != "":
			// A to-one relation's column holds its target's key, which a
			// generated row holds as <field>Id.
			column := Column{Field: c.Field + "Id", Column: c.Name, Type: keyType(c)}
			if target := schema.Types[c.Relation]; target != nil {
				for _, f := range target.Fields {
					if f != nil && f.Key {
						column.Type = f.TypeRef.Name
					}
				}
			}
			column.Convert = ConvertOf(column.Type)
			t.Own = append(t.Own, column)
		default:
			t.Own = append(t.Own, Column{Field: c.Field, Column: c.Name, Type: c.Def.TypeRef.Name, Convert: convertOfRef(c.Def.TypeRef)})
		}
	}
	if t.Key.Column == "" {
		return nil, fmt.Errorf("behaviorlayout: table %s has no key column", td.Name)
	}
	t.Spec = TypeSpec{Name: td.Name, Uses: []UseSpec{}}
	for _, ref := range td.FieldBehaviors() {
		behavior, ok := reg.Behavior(ref.Name)
		if !ok {
			return nil, fmt.Errorf("behaviorlayout: table %s: field %s: behavior %s is not registered", td.Name, ref.Field, ref.Name)
		}
		use := Use{ID: ref.UseID(), Behavior: ref.Name, Anchor: ref.Field, Fields: []OwnedField{}, Operations: []Operation{}}
		for _, declared := range behavior.Fields {
			if declared.Volatile {
				continue
			}
			value := declared.Name == behavior.Value
			fd := ownedField(td, ref, declared.Name)
			if fd == nil {
				return nil, fmt.Errorf("behaviorlayout: table %s: field %s: behavior %s's field %s has no owned field on the table", td.Name, ref.Field, ref.Name, declared.Name)
			}
			column := sqlutil.ColumnName(fd)
			if want := OwnedColumn(ref, declared.Name, value); column != want {
				return nil, fmt.Errorf("behaviorlayout: table %s: field %s: behavior %s's field %s is the column %s, and the layout rule names it %s", td.Name, ref.Field, ref.Name, declared.Name, column, want)
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
		t.Uses = append(t.Uses, use)
		spec := UseSpec{Behavior: ref.Name, Anchor: Anchor{Field: ref.Field}}
		if len(ref.Config) > 0 {
			spec.Config = ref.Config
		}
		t.Spec.Uses = append(t.Spec.Uses, spec)
	}
	return t, nil
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
