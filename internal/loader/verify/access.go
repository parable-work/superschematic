package verify

import (
	"fmt"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	ir "github.com/parable-work/superschematic/ir"
)

// checkAccess checks a schema's per-record access declarations (D156)
// against the classes they are on, their fields and the service, in every
// form, once the access expansion added their fields.
//
// An @audience table is a plain table of a DB service: its effective
// audience is a list of UUIDs the reads compare by array containment on
// Postgres and through json_each on SQLite, which triggers keep. On SQLite
// only the TypeScript repositories read it so far, and the Go ones refuse
// its calls at run time, so a service that lists sqlite takes no Rust types
// (D182, refuseSQLiteReaders). Its
// owner is a field that holds a UUID or a
// to-one relation, or createdBy, which the table then declares. Its
// inheritFrom is a to-one relation to a table that is @audience too. It
// declares none of the audience columns itself.
//
// A projection of an @audience table holds its source's key, so a read of
// it keeps to the rows of the source the caller sees; one without would
// show every row.
//
// The classes marked @accessGrants and @accessLog are one each per schema,
// declare no fields, which the expansion fills, and are tables of the
// database.
func checkAccess(schema *ir.Schema, in Input, r *Result) {
	var bases map[string]bool
	for _, td := range schema.AudienceTables() {
		if bases == nil {
			bases = codegen.BaseTypeNames(schema)
		}
		checkAudience(schema, in, td, bases, r)
	}
	checkAccessTable(schema, in, "@accessGrants", func(td *ir.TypeDef) bool { return td.AccessGrants != nil }, r)
	checkAccessTable(schema, in, "@accessLog", func(td *ir.TypeDef) bool { return td.AccessLog != nil }, r)
	for _, name := range sortedTypeNames(schema.Types) {
		td := schema.Types[name]
		if td == nil || td.Projection == nil {
			continue
		}
		source := schema.Types[td.Projection.Source]
		if source == nil || source.Audience == nil {
			continue
		}
		if _, ofSource := sqlutil.ViewKeyOfSource(schema, td); !ofSource {
			r.errorf(td.Owner, "projection %s reads %s, an @audience table, and holds no field that reads its key, so a read of it could not keep to the rows the caller sees (D156); project %s's key", td.Name, source.Name, source.Name)
		}
	}
}

func checkAudience(schema *ir.Schema, in Input, td *ir.TypeDef, bases map[string]bool, r *Result) {
	fail := func(format string, args ...any) {
		r.errorf(td.Owner, "@audience on %s: %s", td.Name, fmt.Sprintf(format, args...))
	}
	switch {
	case schema.Kind != ir.SchemaKindDB:
		fail("an audience is a table's, and %s is a %s service; declare it on the table, in the DB service's schema", schema.Name, schema.Kind)
		return
	case td.Role != ir.RoleDBTable || td.Projection != nil:
		fail("%s is no table of the database, so it has no rows to hold to an audience", td.Name)
		return
	case bases[td.Name]:
		fail("%s is a base class, which has no table of its own; declare the audience on each table that extends it", td.Name)
		return
	case td.Queue != nil || td.JsonField || td.GraphMember != nil || codegen.IsEngineType(td):
		fail("%s is no table the generated repositories read with statements of their own; an audience is built for a plain table", td.Name)
		return
	}
	refuseSQLiteReaders(schema, in, td, "@audience", r)
	for _, fd := range td.Fields {
		if fd != nil && slices.Contains(ir.AudienceFields, fd.Name) && fd.Origin != ir.OriginAccess {
			fail("%s declares %s, a column @audience adds; rename the field", td.Name, fd.Name)
		}
	}
	a := td.Audience
	if a.Owner == "" {
		if f := fieldNamed(td, sqlutil.AuditCreatedBy); f == nil || f.TypeRef.Name != "Identity.UUID" || f.TypeRef.IsArray {
			fail("names no owner, so its owner is createdBy, which %s does not declare as an Identity.UUID; declare createdBy, or name the owner's field (owner: \"ownerId\")", td.Name)
		}
	} else {
		owner := fieldNamed(td, strings.TrimSuffix(a.Owner, "Id"))
		if owner == nil || owner.Relation == nil {
			owner = fieldNamed(td, a.Owner)
		}
		switch {
		case owner == nil:
			fail("owner %s is no field of %s; name a field that holds the owner's id, or a to-one relation", a.Owner, td.Name)
		case owner.TypeRef.IsArray:
			fail("owner %s is a list, and a row has one owner", a.Owner)
		case owner.Relation != nil:
			target := schema.Types[owner.Relation.Type]
			if target == nil || keyScalar(target) != "Identity.UUID" {
				fail("owner %s is a relation to %s, whose key is no Identity.UUID, and an audience names principals by their ids", a.Owner, owner.Relation.Type)
			}
		case owner.TypeRef.Name != "Identity.UUID":
			fail("owner %s is %s, and an audience names principals by their ids; name a field that is an Identity.UUID, or a to-one relation", a.Owner, typeName(owner.TypeRef))
		}
	}
	if a.InheritFrom != "" {
		parent := fieldNamed(td, a.InheritFrom)
		switch {
		case parent == nil || parent.Relation == nil || parent.TypeRef.IsArray:
			fail("inheritFrom %s is no to-one relation of %s; name the relation to the row whose audience a row inherits", a.InheritFrom, td.Name)
		case schema.Types[parent.Relation.Type] == nil || schema.Types[parent.Relation.Type].Audience == nil:
			fail("inheritFrom %s is a relation to %s, which is no @audience table, so it has no audience to inherit; mark %s @audience", a.InheritFrom, parent.Relation.Type, parent.Relation.Type)
		}
	}
}

// checkAccessTable holds the classes is marks to one per schema, each a
// table of the database whose fields the expansion filled.
func checkAccessTable(schema *ir.Schema, in Input, decorator string, is func(*ir.TypeDef) bool, r *Result) {
	var marked []*ir.TypeDef
	for _, name := range sortedTypeNames(schema.Types) {
		if td := schema.Types[name]; td != nil && is(td) {
			marked = append(marked, td)
		}
	}
	if len(marked) > 1 {
		names := make([]string, len(marked))
		for i, td := range marked {
			names[i] = td.Name
		}
		r.errorf(marked[1].Owner, "%s marks %s; a schema has one", decorator, strings.Join(names, " and "))
	}
	for _, td := range marked {
		switch {
		case schema.Kind != ir.SchemaKindDB || td.Role != ir.RoleDBTable:
			r.errorf(td.Owner, "%s on %s: the class is a table of a DB service", decorator, td.Name)
		case td.Audience != nil:
			r.errorf(td.Owner, "%s on %s: the class is the access tables' own, and takes no @audience", decorator, td.Name)
		}
		for _, fd := range td.Fields {
			if fd != nil && fd.Origin != ir.OriginAccess {
				r.errorf(td.Owner, "%s on %s: the class declares %s, and the build fills its fields; leave the class empty", decorator, td.Name, fd.Name)
				break
			}
		}
		// The grants a party's or an agent run's reads keep to, and the
		// access log a route records its calls in (D156), on SQLite only in
		// the languages that serve them there (D182).
		refuseSQLiteReaders(schema, in, td, decorator, r)
	}
}

// keyScalar is the scalar of td's key field, or "".
func keyScalar(td *ir.TypeDef) string {
	for _, fd := range td.Fields {
		if fd != nil && fd.Key {
			return fd.TypeRef.Name
		}
	}
	return ""
}
