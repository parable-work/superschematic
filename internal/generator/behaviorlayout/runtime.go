package behaviorlayout

import (
	"fmt"

	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// RuntimeOf is the layout document a runtime environment's handler serves
// schema, a DB schema, through (D117's runtime mode): every table a
// schema document defines, by its type's name, where [Of] holds only the
// tables that compose behaviors. A runtime environment has no generated
// code to read a layout from, so a publish asks the compiler for this
// document beside its plan and records it next to the model it migrates
// the database to (`migrate plan --request` with layout).
//
// A table that composes behaviors is laid out as [Of] lays it out, but for
// its own fields; a table that composes none has no uses and no queues, and
// its version column is "" unless @optimistic gives it one. Own holds every
// column a schema field stores but the key and the owned fields: the audit
// fields and the internal ones among them, in column order, since the
// handler holds each field a document declares as the row's own, and
// writes each one (D117 leaves the createdBy stamp to a later change).
// Audit is empty. The sub-tables, shared tables and queues the loader adds
// for a table's behaviors are that table's, so none is a type of the
// document.
func RuntimeOf(schema *ir.Schema, reg *registry.Registry) (*Layout, error) {
	layout := &Layout{Types: map[string]*Table{}}
	if schema == nil {
		return layout, nil
	}
	tables, err := sqlutil.TablesOf(schema, nil)
	if err != nil {
		return nil, err
	}
	for _, td := range sqlutil.TableTypes(schema) {
		if td.BehaviorTable != nil || td.BehaviorQueue != nil || td.RowTable != nil {
			continue
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
		var table *Table
		if len(td.TableBehaviors()) > 0 {
			if table, err = tableOf(schema, td, tables, reg); err != nil {
				return nil, err
			}
		} else {
			if table, err = plainTableOf(td, model); err != nil {
				return nil, err
			}
		}
		table.Own = runtimeOwnColumns(schema, model)
		table.Audit = nil
		layout.Types[td.Name] = table
	}
	return layout, nil
}

// plainTableOf is the layout of td, a table that composes no behavior: its
// table, its key, its version column when it has one, its live condition,
// and no uses. Its spec lists no field, since no use's config names one.
func plainTableOf(td *ir.TypeDef, model *sqlutil.TableModel) (*Table, error) {
	t := &Table{Type: td.Name, Table: model.Name, Uses: []Use{}, Spec: TypeSpec{Name: td.Name, Uses: []UseSpec{}}}
	if model.HasVersion {
		t.Version = VersionColumn
	}
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
	return t, nil
}

// runtimeOwnColumns are the columns of the table model holds that a
// runtime handler reads and writes as a row's own fields: each column a
// schema field stores but the key and the fields a behavior owns, audit
// and internal fields among them, in column order. A to-one relation's
// column is its target's key, which Field names as <field>Id, typed as the
// target's key.
func runtimeOwnColumns(schema *ir.Schema, model *sqlutil.TableModel) []Column {
	own := []Column{}
	for _, c := range model.Columns {
		switch {
		case c.Key, c.Def == nil, c.Def.Owned():
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
		default:
			own = append(own, Column{Field: c.Field, Column: c.Name, Type: c.Def.TypeRef.Name, Convert: convertOfRef(c.Def.TypeRef)})
		}
	}
	return own
}
