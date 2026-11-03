package verify

import (
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	ir "github.com/parable-work/superschematic/ir"
)

// checkDefaultNow holds Default<T, "now"> (codegen.DefaultNow, D88) to
// where it means something: a required, single date, time or timestamp
// field of a DB table, whose column's DEFAULT is the time of the insert.
// No decoder fills it in, so on a payload's field, an optional column (its
// default is NULL), a list, a JSON value, an Encrypted<T> field, an engine
// type or a queue's message, whose enqueues write every required field,
// it would say a default that nothing gives. An inherited field is checked
// on the class that declares it.
func checkDefaultNow(schema *ir.Schema, r *Result) {
	scalarTypes := sqlutil.ScalarSQLTypes(schema)
	for _, types := range []map[string]*ir.TypeDef{schema.Types, schema.Inputs} {
		for _, name := range sortedTypeNames(types) {
			td := types[name]
			if td == nil {
				continue
			}
			for _, fd := range td.Fields {
				if fd == nil || fd.InheritedFrom != "" || !codegen.IsDefaultNow(fd.TypeRef.Name, fd.Default) {
					continue
				}
				where := td.Name + "." + fd.Name
				switch {
				case td.Queue != nil:
					r.errorf(td.Owner, "%s: Default<T, \"now\"> is a table column's default, and a queue's enqueues write every required field of the message; the queue's own created_at holds when a message was enqueued", where)
				case schema.Kind != ir.SchemaKindDB || td.Role != ir.RoleDBTable || td.JsonField:
					r.errorf(td.Owner, "%s: Default<T, \"now\"> is a DB table column's default, the time of the insert, which the database fills in; no decoder fills in a payload's, so give %s no default or a literal one", where, fd.Name)
				case codegen.IsEngineType(td):
					r.errorf(td.Owner, "%s: Default<T, \"now\"> is a DB table column's default, and %s composes a behavior, so the engine keeps its instances in tables of its own", where, td.Name)
				case fd.TypeRef.IsArray || fd.TypeRef.IsMap || fd.JsonField || fd.EncryptedAtRest:
					r.errorf(td.Owner, "%s: Default<T, \"now\"> is the default of a single date, time or timestamp column, and %s is a list, a map, a JSON value or an Encrypted<T> field", where, fd.Name)
				case !fd.Required || fd.Key:
					r.errorf(td.Owner, "%s: Default<T, \"now\"> is a required column's default; an optional column's is NULL, which the ORMs write for a value left unset, and a key's is generated", where)
				case sqlutil.NowDefault(sqlutil.ColumnType(schema, fd, scalarTypes), fd.TypeRef.Name) == "":
					r.errorf(td.Owner, "%s: Default<T, \"now\"> needs a date, time or timestamp column, and %s's is %s", where, fd.Name, sqlutil.ColumnType(schema, fd, scalarTypes))
				}
			}
		}
	}
}
