package verify

import (
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	ir "github.com/parable-work/superschematic/ir"
)

// checkSQLNames holds @table and @column (D85) to the tables the
// generators name through sqlutil: a DB table the ORMs keep, and a field
// of one that is a column, a to-one relation's foreign key among them, and
// a queue's table and its message's columns. A name the engine, a version
// graph or a projection view gives itself takes no override, and two
// tables, or two columns of one table, may not take one name, a column the
// generator adds among them; checkQueues holds a queue's table and columns
// to the same.
func checkSQLNames(schema *ir.Schema, r *Result) {
	bases := codegen.BaseTypeNames(schema)
	tables := map[string]string{}
	for _, name := range sortedTypeNames(schema.Types) {
		td := schema.Types[name]
		if td.SQLName != "" {
			checkTableName(schema, td, bases, r)
		}
		for _, fd := range td.Fields {
			if fd != nil && fd.SQLName != "" {
				checkColumnName(schema, td, fd, bases, r)
			}
		}
		if !hasTable(td, bases) || codegen.IsEngineType(td) {
			continue
		}
		table := sqlutil.TableName(td)
		if other, taken := tables[table]; taken {
			r.errorf(td.Owner, "%s: its table is %s, the table of %s; give one of them another name with @table", td.Name, table, other)
		} else {
			tables[table] = td.Name
		}
		checkColumnCollisions(schema, td, r)
	}
	for table, owner := range tables {
		if other, taken := tables[table+"_history"]; taken && schema.Types[owner].Versioned {
			r.errorf(schema.Types[other].Owner, "%s: its table is %s, the history table of %s; give it another name with @table", other, table+"_history", owner)
		}
	}
}

// overrideRefusal is why td's names take no override, or "".
func overrideRefusal(td *ir.TypeDef) string {
	switch {
	case codegen.IsEngineType(td):
		return "it composes behaviors, so the engine keeps its rows in tables of its own"
	case td.VersionGraph != nil || td.GraphMember != nil:
		return "a version graph's runtimes name its tables themselves (support follows)"
	case td.Origin == ir.OriginIdentity:
		return "the user model adds it"
	case td.Role == ir.RoleProjection:
		return "it is a projection view, which @projection names"
	}
	return ""
}

func checkTableName(schema *ir.Schema, td *ir.TypeDef, bases map[string]bool, r *Result) {
	switch {
	case schema.Kind != ir.SchemaKindDB:
		r.errorf(td.Owner, "%s: @table names a DB table, and %s is a schema of kind %s", td.Name, schema.Name, schema.Kind)
	case td.Queue != nil:
		// A queue's table takes @table's name whole, with no _queue.
	case bases[td.Name]:
		r.errorf(td.Owner, "%s: @table names a table, and %s is a base class, whose fields its subclasses' tables hold; name each subclass's table instead", td.Name, td.Name)
	case overrideRefusal(td) != "":
		r.errorf(td.Owner, "%s: @table names a table the ORMs keep, and %s: %s", td.Name, td.Name, overrideRefusal(td))
	case !hasTable(td, bases):
		r.errorf(td.Owner, "%s: @table names a table, and %s is none: its value is stored whole with the row that holds it", td.Name, td.Name)
	}
}

func checkColumnName(schema *ir.Schema, td *ir.TypeDef, fd *ir.FieldDef, bases map[string]bool, r *Result) {
	owner := td.Name + "." + fd.Name
	switch {
	case schema.Kind != ir.SchemaKindDB:
		r.errorf(td.Owner, "%s: @column names a DB table's column, and %s is a schema of kind %s", owner, schema.Name, schema.Kind)
	case td.Role == ir.RoleProjection:
		r.errorf(td.Owner, "%s: a projection column's @column names the alias.field it reads, such as base.%s", owner, fd.Name)
	case td.Queue != nil:
		// A message's field is a column of the queue's table.
	case overrideRefusal(td) != "":
		r.errorf(td.Owner, "%s: @column names a column the ORMs keep, and %s: %s", owner, td.Name, overrideRefusal(td))
	case !hasTable(td, bases) && !bases[td.Name]:
		r.errorf(td.Owner, "%s: @column names a table's column, and %s is no table", owner, td.Name)
	case sqlutil.IsTableRelation(schema, fd) && fd.TypeRef.IsArray:
		r.errorf(td.Owner, "%s: @column names a column, and a list relation has none: its foreign key is a column of the other table", owner)
	}
}

// checkColumnCollisions refuses two of td's columns that take one name: two
// fields, or a field and a column the generator adds (the id of a table
// without a @key, _version). checkSearchColumns refuses a field whose
// column is search_text, and a field whose column is a @hasMany's foreign
// key is that key, as sqlgen keeps it.
func checkColumnCollisions(schema *ir.Schema, td *ir.TypeDef, r *Result) {
	columns := map[string]string{}
	hasKey := false
	for _, fd := range td.Fields {
		if fd == nil || (sqlutil.IsTableRelation(schema, fd) && fd.TypeRef.IsArray) {
			continue
		}
		hasKey = hasKey || fd.Key
		column := sqlutil.ColumnNameIn(schema, fd)
		if other, taken := columns[column]; taken {
			r.errorf(td.Owner, "%s.%s: its column is %s, the column of %s.%s; give one of them another name with @column", td.Name, fd.Name, column, td.Name, other)
			continue
		}
		columns[column] = fd.Name
	}
	added := func(column, why string) {
		if field, taken := columns[column]; taken {
			r.errorf(td.Owner, "%s.%s: its column is %s, the column %s; give it another name with @column", td.Name, field, column, why)
		}
	}
	if !hasKey && sqlutil.FieldNamed(td, "id") == nil {
		added("id", "the generator adds a table without a @key")
	}
	if td.Versioned || td.Optimistic {
		added(sqlutil.VersionColumn, "a @versioned or @optimistic table keeps its version in")
	}
}
