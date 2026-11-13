package verify

import (
	"fmt"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// checkFences checks a schema's @fenced declarations (D118) against the
// class each is on, its fields and the service, in every form.
//
// A fence is a trigger on a table and a bump in the statements of the
// table's generated repositories, so the class is a table of a DB service
// those repositories write: not a @jsonField type, a base class or any
// other class with no table, not a @queue class, not an engine type, not
// a member of a version graph, whose engines write its rows, and not a
// table that composes a behavior, by a field-anchored use or an
// implements one. There the update of a row, the update half of an
// upsert and a soft delete go through the behaviors' core, which writes
// the row's columns, the table's own among them, with statements of its
// own that advance no fence, so the fence's trigger would refuse them.
//
// The fence is a required field of the table whose column is SMALLINT,
// INTEGER or BIGINT, and each covered field is one of the table that
// stores a column the trigger can compare; a to-one relation is its key's
// column, as in @enqueueOnChange (D80).
//
// In a service that lists the sqlite dialect (Input.Dialects), a fence of
// an @optimistic table lists its covers: there the bump of _version is an
// update of its own that sets _version alone.
//
// A fence counts no relation the database itself rewrites
// (setOnDelete): the update a SET NULL makes of the rows that name a
// deleted row advances no fence, so the trigger would refuse it, and the
// delete with it, and no writer could make it pass. A covered relation
// has another delete action, and a fence with no covers stands on a table
// with no such relation.
//
// What the declarations decide alone (one fence per field, covers on
// every fence of a class with several, no field covered twice) is
// ir.Schema.Validate's.
func checkFences(schema *ir.Schema, in Input, r *Result) {
	var scalarTypes map[string]string
	var bases map[string]bool
	for _, name := range sortedTypeNames(schema.Types) {
		td := schema.Types[name]
		if td == nil || len(td.Fences) == 0 {
			continue
		}
		if scalarTypes == nil {
			scalarTypes = sqlutil.ScalarSQLTypes(schema)
			bases = codegen.BaseTypeNames(schema)
		}
		if reason := unfenceable(schema, td, bases); reason != "" {
			r.errorf(td.Owner, "@fenced on %s: %s", td.Name, reason)
			continue
		}
		for _, fence := range td.Fences {
			if fence != nil {
				checkFence(schema, in, td, fence, scalarTypes, r)
			}
		}
	}
}

// unfenceable says why td takes no fence, or "" for a table of a DB
// service whose rows the generated repositories write with statements of
// their own.
func unfenceable(schema *ir.Schema, td *ir.TypeDef, bases map[string]bool) string {
	switch {
	case schema.Kind != ir.SchemaKindDB:
		return fmt.Sprintf("a fence is a trigger on a table of a DB service, and %s is a %s service; declare it on the table, in the DB service's schema", schema.Name, schema.Kind)
	case codegen.IsEngineType(td):
		return fmt.Sprintf("%s composes a behavior with @behavior, so the engine keeps its instances in tables of its own, which no fence's trigger watches and no repository writes; declare the fence on a table", td.Name)
	case td.Queue != nil:
		return fmt.Sprintf("%s is a @queue class, whose rows are messages the queue's clients write with statements of their own, which advance no fence; declare the fence on a table", td.Name)
	case td.JsonField:
		return fmt.Sprintf("%s is a @jsonField type, kept as JSON in a column of another table, so it has no row of its own to fence; declare the fence on the table that holds it", td.Name)
	case td.Role != ir.RoleDBTable:
		return fmt.Sprintf("%s is no table of the database, so it has no row a write updates; declare the fence on a table", td.Name)
	case bases[td.Name]:
		return fmt.Sprintf("%s is a base class, which has no table of its own; declare the fence on each table that extends it", td.Name)
	case td.GraphMember != nil:
		return fmt.Sprintf("%s is a member of the version graph %s, whose engines write its rows with statements of their own, which advance no fence; a fence on a version graph's member is not built", td.Name, td.GraphMember.Graph)
	case len(td.TableBehaviors()) > 0:
		return fmt.Sprintf("%s composes the %s behavior, so the behaviors' core writes its rows, the table's own fields among them, with statements of its own, which advance no fence; a fence on a table that composes a behavior is not built", td.Name, td.TableBehaviors()[0].Name)
	}
	return ""
}

func checkFence(schema *ir.Schema, in Input, td *ir.TypeDef, fence *ir.Fence, scalarTypes map[string]string, r *Result) {
	where := fmt.Sprintf("@fenced on %s.%s", td.Name, fence.Field)
	fail := func(format string, args ...any) {
		r.errorf(td.Owner, "%s: %s", where, fmt.Sprintf(format, args...))
	}

	// The writer no fence without covers could follow.
	if fence.CoversRow() && td.Optimistic && slices.Contains(in.Dialects, registry.SQLDialectSQLite) {
		fail("%s is @optimistic and %s lists the sqlite dialect, where the bump of _version is an update of its own that sets _version alone, which a fence with no covers would refuse; list in covers the fields the fence counts, or build %s for postgres alone (outputs.sql.dialects: [\"postgres\"])", td.Name, schema.Name, schema.Name)
	}

	field := fieldNamed(td, fence.Field)
	if field == nil {
		fail("%s has no field %s; name the integer field of %s every covered write advances", td.Name, fence.Field, td.Name)
	} else {
		column, ok := sqlutil.ColumnOf(schema, field, scalarTypes)
		switch {
		case !ok:
			fail("%s is %s, whose rows are kept in a table of their own, so it stores no column to count in; name an integer field, as Default<Generic.Int64, 0>", fence.Field, typeName(field.TypeRef))
		case column.Relation:
			fail("%s is a relation to %s, whose column holds a key and no count; name an integer field, as Default<Generic.Int64, 0>", fence.Field, field.TypeRef.Name)
		case !integerColumn(column.Type):
			fail("%s's column is %s, and a fence is a whole number each covered write adds one to; type it by a scalar whose column is SMALLINT, INTEGER or BIGINT, as Default<Generic.Int64, 0>", fence.Field, column.Type)
		}
		if ok && !field.Required {
			fail("%s is nullable, and a null has no next value; make it required, as Default<Generic.Int64, 0>", fence.Field)
		}
	}

	for _, name := range fence.Covers {
		covered := fieldNamed(td, name)
		if covered == nil {
			fail("covers lists %s, which is no field of %s; list fields of %s", name, td.Name, td.Name)
			continue
		}
		column, ok := sqlutil.ColumnOf(schema, covered, scalarTypes)
		switch {
		case !ok:
			fail("covers lists %s, %s, whose rows are kept in a table of their own, so no write of %s changes it; leave it out", name, typeName(covered.TypeRef), td.Name)
		case !comparable(column.Type):
			fail("covers lists %s, a %s column, which IS DISTINCT FROM cannot compare, so the fence's trigger could not tell a write that changes it; leave it out", name, column.Type)
		}
		if action := setOnDelete(covered); ok && action != "" {
			fail("covers lists %s, a relation to %s whose onDelete is %s, so the database sets %s's column itself when the %s it names is deleted, an update that advances no fence, which the fence's trigger would refuse, and the delete with it; give the relation another onDelete (CASCADE, RESTRICT or NO ACTION), or leave it out of covers",
				name, relationTarget(covered), action, name, relationTarget(covered))
		}
	}

	// With no covers the fence counts every column, so each relation of
	// the table the database rewrites is one it counts.
	if fence.CoversRow() {
		for _, f := range td.Fields {
			if f == nil {
				continue
			}
			action := setOnDelete(f)
			if _, ok := sqlutil.ColumnOf(schema, f, scalarTypes); !ok || action == "" {
				continue
			}
			fail("the fence lists no covers, so it counts every update of %s, and %s is a relation to %s whose onDelete is %s, so the database sets %s's column itself when the %s it names is deleted, an update that advances no fence, which the fence's trigger would refuse, and the delete with it; give the relation another onDelete (CASCADE, RESTRICT or NO ACTION), or list in covers the fields the fence counts, without %s",
				td.Name, f.Name, relationTarget(f), action, f.Name, relationTarget(f), f.Name)
		}
	}
}

// setOnDelete returns the delete action of a relation whose foreign key
// has the database rewrite the field's column when the row it names is
// deleted, or "" for a field with no such action. Of the actions a
// relation takes (ir.RelationDef.OnDelete), CASCADE, the default, deletes
// the row, and RESTRICT and NO ACTION refuse the delete: none updates the
// row. SET NULL does, and so would any action that is none of those
// three, which is why the three are the ones listed here. A relation has
// no update action: sqlgen writes ON DELETE alone.
func setOnDelete(f *ir.FieldDef) string {
	if f.Relation == nil || f.JsonField || f.TypeRef.IsMap {
		return ""
	}
	switch f.Relation.OnDelete {
	case "", "CASCADE", "RESTRICT", "NO ACTION":
		return ""
	}
	return f.Relation.OnDelete
}

// integerColumn reports whether sqlType is a column a fence counts in:
// SMALLINT, INTEGER or BIGINT, which hold a whole number exactly.
func integerColumn(sqlType string) bool {
	switch strings.ToUpper(sqlType) {
	case "SMALLINT", "INTEGER", "BIGINT":
		return true
	}
	return false
}
