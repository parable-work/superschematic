package verify

import (
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	ir "github.com/parable-work/superschematic/ir"
)

// searchTextColumn is sqlgen.SearchTextColumn, the column @searchField
// generates. verify cannot import sqlgen, whose tests load schemas through
// the loader, which runs verify; this package's test holds the two to one
// name.
const searchTextColumn = "search_text"

// checkSearchColumns refuses, on a table with search fields
// (@searchField), a field whose column would be search_text, the column
// sqlgen generates from the search fields (D62, amended): the table would
// declare that column twice, and the ORM's filter would have two
// SearchText fields. A field's column is its snake_case name; a to-one
// relation's is <field>_id, and a list relation has none, so neither
// clashes. As sqlgen counts them, only a field that is a column of its own
// makes the table searched; a base class's fields are checked on each
// table that extends it, where they are copied.
func checkSearchColumns(schema *ir.Schema, r *Result) {
	bases := codegen.BaseTypeNames(schema)
	for _, name := range sortedTypeNames(schema.Types) {
		td := schema.Types[name]
		if !hasTable(td, bases) {
			continue
		}
		searched := false
		var clashes []*ir.FieldDef
		for _, fd := range td.Fields {
			if fd == nil || (!fd.JsonField && !fd.TypeRef.IsMap && isTable(schema.Types[fd.TypeRef.Name])) {
				continue
			}
			if fd.SearchField {
				searched = true
			}
			if sqlutil.ColumnName(fd) == searchTextColumn {
				clashes = append(clashes, fd)
			}
		}
		if !searched {
			continue
		}
		for _, fd := range clashes {
			r.errorf(td.Owner, "%s.%s: its column is %s, the column @searchField generates", td.Name, fd.Name, searchTextColumn)
		}
	}
}
