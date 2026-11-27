package verify

import (
	"fmt"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	ir "github.com/parable-work/superschematic/ir"
)

// checkSearchables checks a schema's @searchable declarations (D155)
// against the tables they are on, the fields and queues they name and the
// service, once the expansion added the index and filled the queues.
//
// Each is on a plain table of a DB service, with one key field: the index
// row names its record by one key. On SQLite the index is kept by row
// triggers and ranked by FTS5 (D186), so whichever ORM writes a row, its
// triggers keep its index row, as on Postgres; only the TypeScript runtime
// searches it, so a service that lists sqlite takes no Rust types, whose
// search runs on Postgres alone (refuseSQLiteReaders). The title and body
// are text fields
// of the table, and the scope a field of it holding one value. None of
// them is Encrypted<T>: an index row holds its text in the clear, so an
// encrypted field is never indexed. comments: true needs a table that
// implements Comments, and attachments: true one that declares
// @attachments. The queue is a @queue class of the schema that declares
// no fields, which the expansion fills. And no authored type takes the
// index's name.
func checkSearchables(schema *ir.Schema, in Input, r *Result) {
	tables := schema.SearchableTables()
	if len(tables) == 0 {
		return
	}
	bases := codegen.BaseTypeNames(schema)
	if index := schema.Types[ir.SearchIndexTypeName]; index == nil || !index.SearchIndex {
		r.errorf(tables[0].Owner, "@searchable on %s: the type %s is the search index the database's @searchable tables share; rename the schema's own %s", tables[0].Name, ir.SearchIndexTypeName, ir.SearchIndexTypeName)
		return
	}
	for _, td := range tables {
		fail := func(format string, args ...any) {
			r.errorf(td.Owner, "@searchable on %s: %s", td.Name, fmt.Sprintf(format, args...))
		}
		switch {
		case schema.Kind != ir.SchemaKindDB:
			fail("it is a table's, and %s is a %s service; declare it on the table, in the DB service's schema", schema.Name, schema.Kind)
			continue
		case bases[td.Name]:
			fail("%s is a base class, which has no table of its own; declare it on each table that extends it", td.Name)
			continue
		case !plainTable(td):
			fail("%s is no plain table of the database, whose rows the generated repositories read with statements of their own", td.Name)
			continue
		case oneKey(td) == nil:
			fail("%s's key is not one field, and an index row names its record by one key", td.Name)
			continue
		}
		refuseSQLiteReaders(schema, in, td, "@searchable", r)
		s := td.Searchable
		for i, name := range s.Fields() {
			role := "body"
			if i == 0 {
				role = "title"
			}
			fd := fieldNamed(td, name)
			switch {
			case fd == nil:
				fail("%s names %s, which is no field of %s", role, name, td.Name)
			case fd.EncryptedAtRest:
				fail("%s names %s, which is Encrypted<T>: an index row holds its text in the clear, so an encrypted field is never indexed; leave it out", role, name)
			case fd.Relation != nil || !ir.TextValue(schema, fd.TypeRef):
				fail("%s names %s, which holds %s; a title or a body is a text field", role, name, typeLabel(fd.TypeRef))
			}
		}
		if s.Scope != "" {
			fd := fieldNamed(td, s.Scope)
			switch {
			case fd == nil:
				fail("scope names %s, which is no field of %s", s.Scope, td.Name)
			case fd.EncryptedAtRest:
				fail("scope names %s, which is Encrypted<T>, and an index row copies its scope in the clear", s.Scope)
			case fd.TypeRef.IsArray || fd.TypeRef.IsMap || fd.JsonField:
				fail("scope names %s, which holds %s; a scope is one value, a text or a key", s.Scope, typeLabel(fd.TypeRef))
			}
		}
		if s.Comments != nil && *s.Comments && !implementsComments(schema, td) {
			fail("comments is true, and %s implements no Comments; leave it out", td.Name)
		}
		if s.Attachments != nil && *s.Attachments && td.Attachments == nil {
			fail("attachments is true, and %s declares no @attachments; leave it out", td.Name)
		}
		q := schema.Types[s.Queue]
		switch {
		case q == nil || q.Queue == nil:
			fail("queue names %s, which is no @queue class of %s", s.Queue, schema.Name)
		case len(q.Fields) == 0 || q.Fields[0].Origin != ir.OriginSearch:
			fail("queue names %s, which declares fields of its own; declare it with none, and the build fills them", s.Queue)
		}
	}
}

// implementsComments reports whether td keeps a conversation: Comments'
// messages sub-table is the schema's.
func implementsComments(schema *ir.Schema, td *ir.TypeDef) bool {
	for _, other := range schema.Types {
		if m := other.BehaviorTable; m != nil && m.Row == td.Name && m.Behavior == "Comments" && m.Table == "messages" {
			return true
		}
	}
	return false
}
