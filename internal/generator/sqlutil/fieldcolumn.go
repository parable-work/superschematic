package sqlutil

import (
	"github.com/parable-work/superschematic/internal/generator/codegen"
	ir "github.com/parable-work/superschematic/ir"
)

// FieldColumn is the column a field of a DB table stores, as sqlgen writes
// it: its name, its SQL type and whether it is nullable. A to-one relation
// is the column of its target's key, `<field>_id`; a list of a table's
// rows has no column.
type FieldColumn struct {
	Name     string
	Type     string
	Nullable bool
	// Relation is set for a to-one relation's key column.
	Relation bool
}

// ColumnOf returns the column field stores on its table in schema, or
// false for a field with none (a @hasMany or @manyToMany list). Readers
// that must agree with sqlgen before it runs, verification among them,
// resolve a field's column through it (D80).
func ColumnOf(schema *ir.Schema, field *ir.FieldDef, scalarTypes map[string]string) (FieldColumn, bool) {
	target := schema.Types[field.TypeRef.Name]
	relational := target != nil && target.Role == ir.RoleDBTable && !target.JsonField && !field.JsonField && !field.TypeRef.IsMap
	switch {
	case relational && field.TypeRef.IsArray:
		return FieldColumn{}, false
	case relational:
		return FieldColumn{
			Name:     codegen.ToSnakeCase(field.Name) + "_id",
			Type:     KeyColumnType(schema, target, scalarTypes),
			Nullable: !field.Required,
			Relation: true,
		}, true
	}
	return FieldColumn{
		Name:     codegen.ToSnakeCase(field.Name),
		Type:     ColumnType(schema, field, scalarTypes),
		Nullable: !field.Required,
	}, true
}

// KeyColumnType is the SQL type of a table's key column, td of schema: its
// @key field's, else its id field's, else the UUID of the id sqlgen adds.
func KeyColumnType(schema *ir.Schema, td *ir.TypeDef, scalarTypes map[string]string) string {
	if td == nil {
		return "UUID"
	}
	for _, field := range td.Fields {
		if field.Key {
			return ColumnType(schema, field, scalarTypes)
		}
	}
	for _, field := range td.Fields {
		if field.Name == "id" {
			return ColumnType(schema, field, scalarTypes)
		}
	}
	return "UUID"
}

// HasIDColumn reports whether a table has the id column sqlgen adds to a
// table that declares no key: it has no @key field and no field named id.
func HasIDColumn(td *ir.TypeDef) bool {
	for _, field := range td.Fields {
		if field.Key || field.Name == "id" {
			return false
		}
	}
	return true
}
