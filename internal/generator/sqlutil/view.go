package sqlutil

import (
	"fmt"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	ir "github.com/parable-work/superschematic/ir"
)

// A projection read through the ORMs (D90): its view as a table the
// statements of TableStatementsOf read, and no write touches. Each of its
// fields is a column of the view, in the declaration's order, named as
// sqlgen names it, the field in snake case, and of the field's type, which
// keeps its source column's; a column filters and sorts as a table's
// column of that type does. The view's key is the column that reads its
// source's key, or the one column it collapses by: what getOne reads by
// and a page's keyset places a row by. A projection with neither has no
// repository. A view keyed by its source's key over a source with search
// fields searches as the source does (D99): its filter's search text keeps
// the rows whose source row the source's filter's would keep.

// ViewName is the view of a projection, as a statement names it: its
// pool-qualified name, quoted where it must be, or its name alone when it
// has no pool and is created beside the tables.
func ViewName(def *ir.ProjectionDef) string {
	if def.Pool == "" {
		return QuoteIdentifier(def.Name)
	}
	return QuoteIdentifier(def.Pool) + "." + QuoteIdentifier(def.Name)
}

// ViewsOf models each projection of schema with a key as the table the
// ORMs read, sorted by name: Name is the view's name, unqualified, which
// qualifies its columns, and Quoted the view as a FROM names it (ViewName).
// A projection without a key is left out.
func ViewsOf(schema *ir.Schema, dependencies map[string]*ir.Schema) ([]TableModel, error) {
	scalars := ScalarClasses(schema)
	sqlTypes := ScalarSQLTypes(schema)
	enums := EnumNames(schema, dependencies)
	unions := UnionNames(schema, dependencies)
	var views []TableModel
	for _, td := range schema.Projections() {
		def := td.Projection
		if def == nil {
			continue
		}
		key, ofSource := viewKey(schema, td)
		if key == "" {
			continue
		}
		view := TableModel{Type: td.Name, Name: def.Name, Quoted: ViewName(def)}
		source := schema.Types[def.Source]
		for _, f := range td.Fields {
			ref := f.TypeRef
			scalar, isScalar := scalars[ref.Name]
			_, localEnum := schema.Enums[ref.Name]
			class := ClassifyColumn(f, schema, scalar, isScalar, localEnum || enums[ref.Name], unions[ref.Name])
			class.SQLType = ElementType(ref.Name, sqlTypes)
			column := codegen.ToSnakeCase(f.Name)
			c := ColumnModel{
				Field:     f.Name,
				Name:      column,
				Quoted:    QuoteIdentifier(column),
				Qualified: Qualify(def.Name, column),
				Def:       f,
				Key:       f.Name == key,
				Required:  f.Required,
				Class:     class,
				Type:      ColumnType(schema, f, sqlTypes),
				Filter:    class.FilterKind(ref.IsArray),
			}
			c.Sortable = c.Key || sortableColumn(c, ir.SortValue(schema, ref))
			if c.Key {
				if !f.Required {
					return nil, fmt.Errorf("projection %s: its key %s may be null, which no row's key is", td.Name, f.Name)
				}
				view.Key, view.KeyField = column, f.Name
			}
			view.Columns = append(view.Columns, c)
		}
		if ofSource && searches(source) {
			table := TableName(source)
			view.Search = true
			view.SearchColumn = Qualify(table, SearchTextColumn)
			view.SearchProbe = Qualify(def.Name, view.Key) + " IN (SELECT " + Qualify(table, KeyColumnOf(source)) + " FROM " + QuoteIdentifier(table) + "%s)"
		}
		views = append(views, view)
	}
	return views, nil
}

// searches reports whether td, a table, has a search field, and so the
// search_text column.
func searches(td *ir.TypeDef) bool {
	for _, f := range td.Fields {
		if f != nil && f.SearchField {
			return true
		}
	}
	return false
}

// ViewKey is the field of a projection that is its key, or "": the column
// that reads its source table's key, else the one column it collapses by.
func ViewKey(schema *ir.Schema, td *ir.TypeDef) string {
	key, _ := viewKey(schema, td)
	return key
}

// viewKey is ViewKey, and whether the key reads the source's key, so each
// of the view's rows is one of the source's.
func viewKey(schema *ir.Schema, td *ir.TypeDef) (string, bool) {
	def := td.Projection
	if def == nil {
		return "", false
	}
	source := schema.Types[def.Source]
	if source == nil {
		return "", false
	}
	sourceKey := ""
	for _, f := range source.Fields {
		if f != nil && f.Key {
			sourceKey = f.Name
		}
	}
	if sourceKey == "" && FieldNamed(source, "id") != nil {
		sourceKey = "id"
	}
	reads := func(ref string) string {
		for _, f := range td.Fields {
			if f.ProjectedFunction == nil && f.ProjectionSource() == ref {
				return f.Name
			}
		}
		return ""
	}
	if sourceKey != "" {
		if key := reads(ir.ProjectionBaseAlias + "." + sourceKey); key != "" {
			return key, true
		}
	}
	if def.Collapse != nil && len(def.Collapse.By) == 1 {
		return reads(def.Collapse.By[0]), false
	}
	return "", false
}
