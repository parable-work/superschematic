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
