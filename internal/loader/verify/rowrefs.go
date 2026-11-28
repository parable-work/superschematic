package verify

import (
	"fmt"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	ir "github.com/parable-work/superschematic/ir"
)

// checkRowFeatures checks a schema's @references and @attachments
// declarations (D154) against the tables they are on, the tables and
// queues they name and the service, once the expansion added the
// sub-tables and filled the queues.
//
// Each is on a plain table of a DB service, with one key field: a target's
// delete clears its references through a trigger, and referencedBy and a
// release read across tables. On SQLite only the TypeScript repositories
// do so far, and the Go ones refuse those calls at run time, so a service
// that lists sqlite takes no Rust types (D182, refuseSQLiteReaders). The
// tables a
// @references lists are plain tables of the database with one key each.
// The queues each names are @queue classes of the schema that declare no
// fields, which the expansion fills. And no authored type takes the name
// of a sub-table the expansion adds.
func checkRowFeatures(schema *ir.Schema, in Input, r *Result) {
	var bases map[string]bool
	for _, td := range schema.RowReferencesTables() {
		if bases == nil {
			bases = codegen.BaseTypeNames(schema)
		}
		if !checkRowFeatureTable(schema, in, td, "@references", ir.RowReferencesTypeName(td.Name), ir.OriginReferences, bases, r) {
			continue
		}
		for _, name := range td.RowReferences.Tables {
			target := schema.Types[name]
			if target == nil || !plainTable(target) || bases[name] {
				r.errorf(td.Owner, "@references on %s: tables lists %s, which is no table of %s; list the tables of the database a reference may point at", td.Name, name, schema.Name)
				continue
			}
			if oneKey(target) == nil {
				r.errorf(td.Owner, "@references on %s: tables lists %s, whose key is not one field, so a reference cannot name a row of it by one key", td.Name, name)
			}
		}
		checkFilledQueue(schema, td, "@references", "cleared", td.RowReferences.Cleared, ir.OriginReferences, r)
	}
	for _, td := range schema.AttachmentsTables() {
		if bases == nil {
			bases = codegen.BaseTypeNames(schema)
		}
		if !checkRowFeatureTable(schema, in, td, "@attachments", ir.AttachmentsTypeName(td.Name), ir.OriginAttachments, bases, r) {
			continue
		}
		checkFilledQueue(schema, td, "@attachments", "released", td.Attachments.Released, ir.OriginAttachments, r)
	}
}

// plainTable reports whether td is a table of the database whose rows the
// repositories read with statements of their own: no view, queue, JSON
// type, graph member or sub-table the loader added.
func plainTable(td *ir.TypeDef) bool {
	return td.Role == ir.RoleDBTable && td.Projection == nil && td.Queue == nil && !td.JsonField &&
		td.GraphMember == nil && td.BehaviorTable == nil && td.BehaviorQueue == nil && td.RowTable == nil && !td.SearchIndex
}

// oneKey is td's one key field, or nil when it has none or several.
func oneKey(td *ir.TypeDef) *ir.FieldDef {
	var key *ir.FieldDef
	for _, fd := range td.Fields {
		if fd != nil && fd.Key {
			if key != nil {
				return nil
			}
			key = fd
		}
	}
	return key
}

// checkRowFeatureTable checks the table a declaration is on, and that the
// type its sub-table takes the name of is the expansion's own; it reports
// whether the declaration's own checks may go on.
func checkRowFeatureTable(schema *ir.Schema, in Input, td *ir.TypeDef, decorator, subName, origin string, bases map[string]bool, r *Result) bool {
	fail := func(format string, args ...any) {
		r.errorf(td.Owner, "%s on %s: %s", decorator, td.Name, fmt.Sprintf(format, args...))
	}
	switch {
	case schema.Kind != ir.SchemaKindDB:
		fail("it is a table's, and %s is a %s service; declare it on the table, in the DB service's schema", schema.Name, schema.Kind)
		return false
	case bases[td.Name]:
		fail("%s is a base class, which has no table of its own; declare it on each table that extends it", td.Name)
		return false
	case !plainTable(td):
		fail("%s is no plain table of the database, whose rows the generated repositories read with statements of their own", td.Name)
		return false
	case oneKey(td) == nil:
		fail("%s's key is not one field, and the sub-table's rows name their row by one key", td.Name)
		return false
	}
	refuseSQLiteReaders(schema, in, td, decorator, r)
	if sub := schema.Types[subName]; sub == nil || sub.Origin != origin {
		fail("the type %s is the sub-table it adds beside %s; rename the schema's own %s", subName, td.Name, subName)
		return false
	}
	return true
}

// checkFilledQueue checks a queue a declaration names: a @queue class of
// the schema whose fields the expansion filled.
func checkFilledQueue(schema *ir.Schema, td *ir.TypeDef, decorator, option, queue, origin string, r *Result) {
	if queue == "" {
		return
	}
	q := schema.Types[queue]
	switch {
	case q == nil || q.Queue == nil:
		r.errorf(td.Owner, "%s on %s: %s names %s, which is no @queue class of %s", decorator, td.Name, option, queue, schema.Name)
	case len(q.Fields) == 0 || q.Fields[0].Origin != origin:
		r.errorf(td.Owner, "%s on %s: %s names %s, which declares fields of its own; declare it with none, and the build fills them", decorator, td.Name, option, queue)
	}
}
