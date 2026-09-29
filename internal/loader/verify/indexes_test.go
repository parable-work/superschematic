package verify

import (
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/sqlgen"
	ir "github.com/parable-work/superschematic/ir"
)

// indexSchema has a to-one relation (Post.author), two list relations
// (Author.posts, and Author.comments, whose author_id column sqlgen adds to
// comment only after it resolves Comment's indexes), a map and a @jsonField
// of a table type, and a table without a @key (Tag), whose id is generated.
func indexSchema() *ir.Schema {
	str := ir.TypeRef{Name: "string"}
	schema := ir.NewSchema("svc", ir.SchemaKindDB)
	schema.Types["Author"] = &ir.TypeDef{Name: "Author", Role: ir.RoleDBTable, Fields: []*ir.FieldDef{
		{Name: "id", TypeRef: str, Required: true, Key: true},
		{Name: "name", TypeRef: str, Required: true},
		{Name: "posts", TypeRef: ir.TypeRef{Name: "Post", IsArray: true}, HasMany: true},
		{Name: "comments", TypeRef: ir.TypeRef{Name: "Comment", IsArray: true}, HasMany: true},
	}}
	schema.Types["Post"] = &ir.TypeDef{Name: "Post", Role: ir.RoleDBTable, Fields: []*ir.FieldDef{
		{Name: "id", TypeRef: str, Required: true, Key: true},
		{Name: "author", TypeRef: ir.TypeRef{Name: "Author"}, Required: true, Relation: &ir.RelationDef{Type: "Author"}},
		{Name: "displayTitle", TypeRef: str, Required: true},
		{Name: "reviewerId", TypeRef: str},
		{Name: "byLocale", TypeRef: ir.TypeRef{Name: "Author", IsMap: true}},
		{Name: "snapshot", TypeRef: ir.TypeRef{Name: "Author"}, JsonField: true},
	}}
	schema.Types["Comment"] = &ir.TypeDef{Name: "Comment", Role: ir.RoleDBTable, Fields: []*ir.FieldDef{
		{Name: "id", TypeRef: str, Required: true, Key: true},
		{Name: "body", TypeRef: str, Required: true},
	}}
	schema.Types["Tag"] = &ir.TypeDef{Name: "Tag", Role: ir.RoleDBTable, Fields: []*ir.FieldDef{
		{Name: "label", TypeRef: str, Required: true},
	}}
	return schema
}

// TestIndexKeys: verify accepts exactly the @index keys sqlgen can build an
// index from, and sqlgen fails on every key verify refuses.
func TestIndexKeys(t *testing.T) {
	cases := []struct {
		name string
		typ  string
		keys []string
		want string // "" when the index is accepted
	}{
		{"field", "Post", []string{"displayTitle"}, ""},
		{"field in snake_case", "Post", []string{"display_title"}, ""},
		{"relation by field name", "Post", []string{"author"}, ""},
		{"relation by column name", "Post", []string{"authorId"}, ""},
		{"composite", "Post", []string{"author", "displayTitle"}, ""},
		{"map of a table type", "Post", []string{"byLocale"}, ""},
		{"@jsonField of a table type", "Post", []string{"snapshot"}, ""},
		// sqlgen tries <key>_id against every column, not only a relation's.
		{"column by its _id stem", "Post", []string{"reviewer"}, ""},
		{"unknown key", "Post", []string{"author", "titel"}, `Post: @index(["author", "titel"]) key "titel" names no field of Post`},
		{"list relation", "Author", []string{"posts"}, `Author: @index(["posts"]) key "posts" is a list relation, which has no column in table author`},
		{"generated id", "Tag", []string{"id"}, `Tag: @index(["id"]) key "id" names no field of Tag`},
		{"@hasMany back reference", "Comment", []string{"author"}, `Comment: @index(["author"]) key "author" names no field of Comment`},
		{"no keys", "Post", nil, "Post: @index requires at least one key"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			schema := indexSchema()
			schema.Types[tc.typ].Indexes = []ir.IndexDef{{Keys: tc.keys}}

			r := &Result{}
			checkIndexKeys(schema, r)
			var msgs []string
			for _, d := range r.Errors {
				msgs = append(msgs, d.Msg)
			}
			if tc.want == "" && len(msgs) != 0 || tc.want != "" && (len(msgs) != 1 || msgs[0] != tc.want) {
				t.Fatalf("errors = %q, want %q", msgs, tc.want)
			}

			_, err := sqlgen.Generate(schema, sqlgen.Options{SchemaName: "svc", Clock: codegen.FixedClock(time.Unix(0, 0).UTC())})
			if (err == nil) != (tc.want == "") {
				t.Fatalf("sqlgen error = %v, but verify wants %q", err, tc.want)
			}
		})
	}
}

// TestIndexKeyOfNestedTableList: a key naming a Table[][] field is reported
// once, by checkArraysOfArrays.
func TestIndexKeyOfNestedTableList(t *testing.T) {
	schema := indexSchema()
	post := schema.Types["Post"]
	post.Fields = append(post.Fields, &ir.FieldDef{Name: "grid", TypeRef: ir.TypeRef{Name: "Author", IsArray: true, IsArrayOfArrays: true}})
	post.Indexes = []ir.IndexDef{{Keys: []string{"grid"}}}

	var reported []string
	for _, msg := range errorStrings(Run(schema, Input{})) {
		if strings.Contains(msg, `key "grid"`) {
			reported = append(reported, msg)
		}
	}
	want := `Post: @index key "grid" is an array of arrays, which cannot be an index column`
	if len(reported) != 1 || reported[0] != want {
		t.Fatalf("errors = %q, want [%q]", reported, want)
	}
}

// tableSchema has a base class two levels deep (Auditable, the base of
// Record and Tenant, and Record, the base of Invoice), a base whose only
// subclass is a @jsonField type (Coord), a @jsonField type (Snapshot), a
// @trait (SoftDeletable) and an API input (TenantInput). Only Invoice and
// Tenant get a table.
func tableSchema() *ir.Schema {
	str := ir.TypeRef{Name: "string"}
	field := func(name, from string) *ir.FieldDef {
		return &ir.FieldDef{Name: name, TypeRef: str, Required: true, InheritedFrom: from}
	}
	schema := ir.NewSchema("svc", ir.SchemaKindDB)
	schema.Types["Auditable"] = &ir.TypeDef{Name: "Auditable", Role: ir.RoleDBTable, Fields: []*ir.FieldDef{field("createdAt", "")}}
	schema.Types["Record"] = &ir.TypeDef{Name: "Record", Role: ir.RoleDBTable, Extends: "Auditable", Fields: []*ir.FieldDef{field("createdAt", "Auditable"), field("note", "")}}
	schema.Types["Invoice"] = &ir.TypeDef{Name: "Invoice", Role: ir.RoleDBTable, Extends: "Record", Fields: []*ir.FieldDef{field("createdAt", "Auditable"), field("note", "Record"), field("total", "")}}
	schema.Types["Tenant"] = &ir.TypeDef{Name: "Tenant", Role: ir.RoleDBTable, Extends: "Auditable", Fields: []*ir.FieldDef{field("createdAt", "Auditable"), field("slug", "")}}
	schema.Types["Coord"] = &ir.TypeDef{Name: "Coord", Role: ir.RoleDBTable, Fields: []*ir.FieldDef{field("x", "")}}
	schema.Types["Point"] = &ir.TypeDef{Name: "Point", Role: ir.RoleDBTable, JsonField: true, Extends: "Coord", Fields: []*ir.FieldDef{field("x", "Coord")}}
	schema.Types["Snapshot"] = &ir.TypeDef{Name: "Snapshot", Role: ir.RoleDBTable, JsonField: true, Fields: []*ir.FieldDef{field("takenAt", "")}}
	schema.Types["SoftDeletable"] = &ir.TypeDef{Name: "SoftDeletable", Role: ir.RoleTrait, IsTrait: true, Fields: []*ir.FieldDef{field("deletedAt", "")}}
	schema.Types["TenantInput"] = &ir.TypeDef{Name: "TenantInput", Role: ir.RoleAPIInput, Fields: []*ir.FieldDef{field("slug", "")}}
	return schema
}

// TestIndexTables: verify refuses an @index on every type sqlgen makes no
// table of, naming the type, and sqlgen fails on each of them too.
func TestIndexTables(t *testing.T) {
	cases := []struct {
		name string
		typ  string
		keys []string
		want string // "" when the index is accepted
	}{
		{"subclass", "Tenant", []string{"createdAt"}, ""},
		{"subclass of a subclass", "Invoice", []string{"createdAt", "note"}, ""},
		{"base class", "Auditable", []string{"createdAt"}, `Auditable: @index(["createdAt"]) is on a base class, which gets no table; declare it on each table that extends Auditable (Invoice, Tenant)`},
		{"base class that is a subclass", "Record", []string{"note"}, `Record: @index(["note"]) is on a base class, which gets no table; declare it on each table that extends Record (Invoice)`},
		{"base class of no table", "Coord", []string{"x"}, `Coord: @index(["x"]) is on a base class, which gets no table`},
		{"@jsonField type", "Snapshot", []string{"takenAt"}, `Snapshot: @index(["takenAt"]) is on a @jsonField type, which is stored as JSON and gets no table`},
		{"@jsonField subclass", "Point", []string{"x"}, `Point: @index(["x"]) is on a @jsonField type, which is stored as JSON and gets no table`},
		{"@trait", "SoftDeletable", []string{"deletedAt"}, `SoftDeletable: @index(["deletedAt"]) is on a @trait, which gets no table; declare it on each table that implements SoftDeletable`},
		{"API input", "TenantInput", []string{"slug"}, `TenantInput: @index(["slug"]) is on a type of role APIInput, which gets no table`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			schema := tableSchema()
			schema.Types[tc.typ].Indexes = []ir.IndexDef{{Keys: tc.keys}}

			r := &Result{}
			checkIndexTables(schema, r)
			var msgs []string
			for _, d := range r.Errors {
				msgs = append(msgs, d.Msg)
			}
			if tc.want == "" && len(msgs) != 0 || tc.want != "" && (len(msgs) != 1 || msgs[0] != tc.want) {
				t.Fatalf("errors = %q, want %q", msgs, tc.want)
			}

			_, err := sqlgen.Generate(schema, sqlgen.Options{SchemaName: "svc", Clock: codegen.FixedClock(time.Unix(0, 0).UTC())})
			if (err == nil) != (tc.want == "") {
				t.Fatalf("sqlgen error = %v, but verify wants %q", err, tc.want)
			}
		})
	}
}

// TestIndexTablesReportedOnce: an @index on a type without a table is
// reported once, by checkIndexTables, however its keys would fare on a
// table: here one key names no field and one names an array of arrays.
func TestIndexTablesReportedOnce(t *testing.T) {
	for _, typ := range []string{"Auditable", "Snapshot"} {
		schema := tableSchema()
		td := schema.Types[typ]
		td.Fields = append(td.Fields, &ir.FieldDef{Name: "grid", TypeRef: ir.TypeRef{Name: "string", IsArray: true, IsArrayOfArrays: true}})
		td.Indexes = []ir.IndexDef{{Keys: []string{"titel", "grid"}}}

		var reported []string
		for _, msg := range errorStrings(Run(schema, Input{})) {
			if strings.HasPrefix(msg, typ+":") {
				reported = append(reported, msg)
			}
		}
		if len(reported) != 1 || !strings.Contains(reported[0], `@index(["titel", "grid"]) is on a`) {
			t.Fatalf("%s: errors = %q, want one refusing the index", typ, reported)
		}
	}
}
