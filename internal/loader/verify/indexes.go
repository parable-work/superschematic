package verify

import (
	"slices"
	"strconv"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	ir "github.com/parable-work/superschematic/ir"
)

// checkIndexTables refuses an @index on a type that gets no table of its
// own, which sqlgen would leave out of the DDL: a base class, whose fields
// are copied onto each subclass but whose indexes are not; a @jsonField
// type, stored as JSON in its parent's column; a @trait; and any other type
// that is not a DB table. checkProjectionClass refuses one on a @projection
// class. sqlgen fails on the same indexes, as a backstop.
func checkIndexTables(schema *ir.Schema, r *Result) {
	bases := codegen.BaseTypeNames(schema)
	for _, name := range sortedTypeNames(schema.Types) {
		td := schema.Types[name]
		if len(td.Indexes) == 0 || hasTable(td, bases) || td.Role == ir.RoleProjection {
			continue
		}
		var on string
		switch {
		case td.Role == ir.RoleTrait:
			on = "a @trait, which gets no table; declare it on each table that implements " + td.Name
		case td.Role != ir.RoleDBTable:
			on = "a type of role " + string(td.Role) + ", which gets no table"
		case td.JsonField:
			on = "a @jsonField type, which is stored as JSON and gets no table"
		default:
			on = "a base class, which gets no table"
			if tables := extendingTables(schema, td.Name, bases); len(tables) > 0 {
				on += "; declare it on each table that extends " + td.Name + " (" + strings.Join(tables, ", ") + ")"
			}
		}
		for _, idx := range td.Indexes {
			r.errorf(td.Owner, "%s: %s is on %s", td.Name, indexLabel(idx), on)
		}
	}
}

// hasTable reports whether td gets a table of its own, as sqlgen decides: a
// DB table type that is not a @jsonField payload and is not a base class
// (bases, from codegen.BaseTypeNames).
func hasTable(td *ir.TypeDef, bases map[string]bool) bool {
	return isDBTable(td) && !bases[td.Name]
}

// extendingTables returns the tables whose chain of base classes reaches
// base, sorted: the tables base's fields are copied onto.
func extendingTables(schema *ir.Schema, base string, bases map[string]bool) []string {
	var tables []string
	for _, name := range sortedTypeNames(schema.Types) {
		td := schema.Types[name]
		if !hasTable(td, bases) {
			continue
		}
		// The bound ends a cyclic chain, which a data form can write.
		cur := td.Extends
		for range len(schema.Types) {
			if cur == "" || cur == base || schema.Types[cur] == nil {
				break
			}
			cur = schema.Types[cur].Extends
		}
		if cur == base {
			tables = append(tables, name)
		}
	}
	return tables
}

// checkIndexKeys refuses an @index of a DB table that the SQL generator
// cannot build: one without keys, or one with a key that resolves to no
// column of the table. sqlgen resolves a key (sqlutil.IndexKeyMatches)
// against the columns the type's own fields become (indexColumns), so a
// relation field is indexed by its name or <name>Id. A list relation has
// no column in the table, and the generated id, the _version column and a
// @hasMany back reference's column are added after the keys are resolved,
// so none of them can be a key. sqlgen fails on the same indexes, as a
// backstop. An @index on a type without a table is left to
// checkIndexTables.
func checkIndexKeys(schema *ir.Schema, r *Result) {
	bases := codegen.BaseTypeNames(schema)
	for _, name := range sortedTypeNames(schema.Types) {
		td := schema.Types[name]
		if !hasTable(td, bases) || len(td.Indexes) == 0 {
			continue
		}
		columns, lists := indexColumns(schema, td)
		for _, idx := range td.Indexes {
			if len(idx.Keys) == 0 {
				r.errorf(td.Owner, "%s: @index requires at least one key", td.Name)
				continue
			}
			for _, key := range idx.Keys {
				list := lists[key]
				switch {
				case slices.ContainsFunc(columns, func(column string) bool { return sqlutil.IndexKeyMatches(key, column) }):
				case list != nil && list.TypeRef.IsArrayOfArrays:
					// checkArraysOfArrays refuses the key.
				case list != nil:
					r.errorf(td.Owner, "%s: %s key %q is a list relation, which has no column in table %s", td.Name, indexLabel(idx), key, codegen.ToSnakeCase(td.Name))
				default:
					r.errorf(td.Owner, "%s: %s key %q names no field of %s", td.Name, indexLabel(idx), key, td.Name)
				}
			}
		}
	}
}

// indexColumns returns the columns td's own fields become, as sqlgen builds
// them before it resolves the @index keys: a to-one relation to a table of
// this schema is its foreign key column, <field>_id, and any other field
// that is not a list relation is its snake_case name. The list relations,
// which become no column, are returned by field name.
func indexColumns(schema *ir.Schema, td *ir.TypeDef) ([]string, map[string]*ir.FieldDef) {
	var columns []string
	lists := map[string]*ir.FieldDef{}
	for _, fd := range td.Fields {
		relation := !fd.JsonField && !fd.TypeRef.IsMap && isTable(schema.Types[fd.TypeRef.Name])
		switch {
		case relation && fd.TypeRef.IsArray:
			lists[fd.Name] = fd
		case relation:
			columns = append(columns, codegen.ToSnakeCase(fd.Name)+"_id")
		default:
			columns = append(columns, codegen.ToSnakeCase(fd.Name))
		}
	}
	return columns, lists
}

// indexLabel names an index by its keys, as the TypeScript form writes it.
func indexLabel(idx ir.IndexDef) string {
	keys := make([]string, len(idx.Keys))
	for i, key := range idx.Keys {
		keys[i] = strconv.Quote(key)
	}
	return "@index([" + strings.Join(keys, ", ") + "])"
}
