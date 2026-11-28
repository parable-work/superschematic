package verify

import (
	"github.com/parable-work/superschematic/internal/generator/codegen"
	ir "github.com/parable-work/superschematic/ir"
)

// checkLocalDateTime holds a wall date-time (Temporal.LocalDateTime, a
// date and a time of day with no zone) to where the generated code carries
// one. A DB table's column of it is a TIMESTAMP on Postgres and TEXT on
// SQLite, its text in a row of every repository, which filters and sorts
// it; a payload's field of it is its text. What does not carry one yet is
// refused here, by name, rather than read wrongly: a list of them and a
// key in a table, a queue's message, a version graph's entity, a
// projection, and a table that composes a behavior, whose adapters convert
// a column by its layout and have no conversion for it. Its "now" has no
// meaning: a wall clock reads the time in a zone, and the type names none.
// An inherited field is checked on the class that declares it.
func checkLocalDateTime(schema *ir.Schema, r *Result) {
	for _, types := range []map[string]*ir.TypeDef{schema.Types, schema.Inputs} {
		for _, name := range sortedTypeNames(types) {
			td := types[name]
			if td == nil {
				continue
			}
			for _, fd := range td.Fields {
				if fd == nil || fd.InheritedFrom != "" || !codegen.GuessScalarTraits(fd.TypeRef.Name).IsLocalDateTimeLike {
					continue
				}
				where := td.Name + "." + fd.Name
				if fd.Default != nil && *fd.Default == codegen.DefaultNow {
					r.errorf(td.Owner, "%s: Default<T, \"now\"> is the time of the insert, an instant, and %s is a wall date-time, which has no zone to read that instant in; give it a Temporal.DateTime or no default", where, fd.TypeRef.Name)
				}
				switch {
				case td.Queue != nil:
					r.errorf(td.Owner, "%s: a queue's message does not carry a wall date-time (%s) yet; give it a string or a Temporal.DateTime", where, fd.TypeRef.Name)
				case schema.Kind != ir.SchemaKindDB || !isTableColumn(td, fd) || fd.TypeRef.IsMap:
					// A payload's field, or a value held in JSON, is its text.
				case td.GraphMember != nil:
					r.errorf(td.Owner, "%s: a version graph's entity does not carry a wall date-time (%s) yet", where, fd.TypeRef.Name)
				case td.Projection != nil:
					r.errorf(td.Owner, "%s: a projection does not carry a wall date-time (%s) yet", where, fd.TypeRef.Name)
				case len(td.TableBehaviors()) > 0:
					r.errorf(td.Owner, "%s: %s composes a behavior, whose adapters do not convert a wall date-time (%s) column yet", where, td.Name, fd.TypeRef.Name)
				case fd.TypeRef.IsArray:
					r.errorf(td.Owner, "%s: a table does not hold a list of wall date-times (%s) yet; hold one, or a @jsonField list", where, fd.TypeRef.Name)
				case fd.Key:
					r.errorf(td.Owner, "%s: a wall date-time (%s) is not a table's key yet", where, fd.TypeRef.Name)
				}
			}
		}
	}
}
