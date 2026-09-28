package verify

import (
	"slices"
	"strconv"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	ir "github.com/parable-work/superschematic/ir"
)

// checkIndexKeys refuses an @index of a DB table that the SQL generator
// cannot build: one without keys, or one with a key that resolves to no
// column of the table. sqlgen resolves a key (sqlutil.IndexKeyMatches)
// against the columns the type's own fields become (indexColumns), so a
// relation field is indexed by its name or <name>Id. A list relation has
// no column in the table, and the generated id, the _version column and a
// @hasMany back reference's column are added after the keys are resolved,
// so none of them can be a key. sqlgen fails on the same indexes, as a
// backstop.
func checkIndexKeys(schema *ir.Schema, r *Result) {
	for _, name := range sortedTypeNames(schema.Types) {
		td := schema.Types[name]
		if !isDBTable(td) || len(td.Indexes) == 0 {
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
