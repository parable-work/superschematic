package verify

import (
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader/access"
	ir "github.com/parable-work/superschematic/ir"
)

// accessSchema is a DB schema of folders and documents that hold an
// audience (D156): a folder's owner is its owner field, and a document
// inherits its folder's audience, its owner createdBy. A note holds none.
// The input lists postgres alone.
func accessSchema() (*ir.Schema, Input) {
	schema := ir.NewSchema("docs-db", ir.SchemaKindDB)
	for name, sqlType := range map[string]string{"Identity.UUID": "UUID", "Temporal.DateTime": "TIMESTAMPTZ", "Crypto.SHA256": "TEXT"} {
		schema.Scalars[name] = &ir.ScalarDef{Name: name, TypeMappings: map[string]string{"sql": sqlType}}
	}
	id := func() *ir.FieldDef {
		return &ir.FieldDef{Name: "id", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true, Key: true}
	}
	uuid := func(name string) *ir.FieldDef {
		return &ir.FieldDef{Name: name, TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true}
	}
	schema.Types["Folder"] = &ir.TypeDef{Name: "Folder", Role: ir.RoleDBTable, Audience: &ir.AudienceDef{Owner: "owner"}, Fields: []*ir.FieldDef{
		id(), uuid("owner"),
	}}
	schema.Types["Document"] = &ir.TypeDef{Name: "Document", Role: ir.RoleDBTable, Audience: &ir.AudienceDef{InheritFrom: "folder", Agents: ir.AgentsReadOnly}, Fields: []*ir.FieldDef{
		id(),
		{Name: "folder", TypeRef: ir.TypeRef{Name: "Folder"}, Relation: &ir.RelationDef{Type: "Folder", OnDelete: "SET NULL"}},
		uuid("createdBy"),
	}}
	schema.Types["Note"] = &ir.TypeDef{Name: "Note", Role: ir.RoleDBTable, Fields: []*ir.FieldDef{id(), uuid("author")}}
	return schema, Input{Dialects: []string{"postgres"}}
}

// checked is the access findings of schema once the expansion ran, as the
// loader runs them.
func checked(schema *ir.Schema, in Input) []string {
	access.Expand(schema)
	r := &Result{}
	checkAccess(schema, in, r)
	var out []string
	for _, err := range r.Errors {
		out = append(out, err.Error())
	}
	return out
}

func TestAudienceDeclarationsHoldToTheirTables(t *testing.T) {
	schema, in := accessSchema()
	if got := checked(schema, in); len(got) != 0 {
		t.Fatalf("a sound schema has findings: %v", got)
	}
	// On SQLite the TypeScript repositories serve it (D182).
	if got := checked(schema, Input{Dialects: []string{"postgres", "sqlite"}, TypesLanguages: []string{"typescript"}}); len(got) != 0 {
		t.Fatalf("a schema whose TypeScript types alone read it on SQLite has findings: %v", got)
	}
	// The grants and the access log run on SQLite in TypeScript and Rust,
	// whose repositories keep a caller's reads to its grants there.
	grants, _ := accessSchema()
	grants.Types["Folder"].Audience, grants.Types["Note"].Audience, grants.Types["Document"].Audience = nil, nil, nil
	grants.Types["Grant"] = &ir.TypeDef{Name: "Grant", Role: ir.RoleDBTable, AccessGrants: &ir.AccessGrantsDef{}}
	grants.Types["Log"] = &ir.TypeDef{Name: "Log", Role: ir.RoleDBTable, AccessLog: &ir.AccessLogDef{}}
	if got := checked(grants, Input{Dialects: []string{"postgres", "sqlite"}, TypesLanguages: []string{"typescript", "rust"}}); len(got) != 0 {
		t.Fatalf("grants and an access log read on SQLite by TypeScript and Rust have findings: %v", got)
	}
	for _, tc := range []struct {
		name string
		edit func(*ir.Schema, *Input)
		want string
	}{
		{"the sqlite dialect with Go's types", func(_ *ir.Schema, in *Input) {
			in.Dialects, in.TypesLanguages = []string{"postgres", "sqlite"}, []string{"go", "typescript"}
		}, "docs-db lists the sqlite dialect and enables the Go types"},
		{"the sqlite dialect with Rust's types", func(_ *ir.Schema, in *Input) {
			in.Dialects, in.TypesLanguages = []string{"sqlite"}, []string{"rust"}
		}, "on SQLite only the TypeScript repositories serve @audience so far (D182)"},
		{"an owner that is no field", func(s *ir.Schema, _ *Input) { s.Types["Folder"].Audience.Owner = "keeper" }, "owner keeper is no field of Folder"},
		{"an owner that holds no id", func(s *ir.Schema, _ *Input) {
			s.Types["Folder"].Fields = append(s.Types["Folder"].Fields, &ir.FieldDef{Name: "label", TypeRef: ir.TypeRef{Name: "string"}})
			s.Types["Folder"].Audience.Owner = "label"
		}, "owner label is string"},
		{"no owner and no createdBy", func(s *ir.Schema, _ *Input) { s.Types["Note"].Audience = &ir.AudienceDef{} }, "its owner is createdBy, which Note does not declare"},
		{"inheritFrom that is no relation", func(s *ir.Schema, _ *Input) { s.Types["Document"].Audience.InheritFrom = "createdBy" }, "inheritFrom createdBy is no to-one relation"},
		{"inheritFrom a table with no audience", func(s *ir.Schema, _ *Input) { s.Types["Folder"].Audience = nil }, "which is no @audience table"},
		{"a column the table declares", func(s *ir.Schema, _ *Input) {
			s.Types["Folder"].Fields = append(s.Types["Folder"].Fields, &ir.FieldDef{Name: ir.AudienceField, TypeRef: ir.TypeRef{Name: "string"}})
		}, "declares audience, a column @audience adds"},
		{"grants on the sqlite dialect with Go's types", func(s *ir.Schema, in *Input) {
			s.Types["Grant"] = &ir.TypeDef{Name: "Grant", Role: ir.RoleDBTable, AccessGrants: &ir.AccessGrantsDef{}}
			s.Types["Folder"].Audience, s.Types["Note"].Audience, s.Types["Document"].Audience = nil, nil, nil
			in.Dialects, in.TypesLanguages = []string{"postgres", "sqlite"}, []string{"go", "typescript"}
		}, "@accessGrants on Grant: docs-db lists the sqlite dialect and enables the Go types"},
		{"an access log on the sqlite dialect with Go's types", func(s *ir.Schema, in *Input) {
			s.Types["Log"] = &ir.TypeDef{Name: "Log", Role: ir.RoleDBTable, AccessLog: &ir.AccessLogDef{}}
			s.Types["Folder"].Audience, s.Types["Note"].Audience, s.Types["Document"].Audience = nil, nil, nil
			in.Dialects, in.TypesLanguages = []string{"sqlite"}, []string{"go"}
		}, "@accessLog on Log: docs-db lists the sqlite dialect and enables the Go types"},
		{"two grants tables", func(s *ir.Schema, _ *Input) {
			s.Types["GrantA"] = &ir.TypeDef{Name: "GrantA", Role: ir.RoleDBTable, AccessGrants: &ir.AccessGrantsDef{}}
			s.Types["GrantB"] = &ir.TypeDef{Name: "GrantB", Role: ir.RoleDBTable, AccessGrants: &ir.AccessGrantsDef{}}
		}, "@accessGrants marks GrantA and GrantB; a schema has one"},
		{"a log that declares a field", func(s *ir.Schema, _ *Input) {
			s.Types["Log"] = &ir.TypeDef{Name: "Log", Role: ir.RoleDBTable, AccessLog: &ir.AccessLogDef{}, Fields: []*ir.FieldDef{
				{Name: "id", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true, Key: true},
			}}
		}, "the class declares id, and the build fills its fields"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			schema, in := accessSchema()
			tc.edit(schema, &in)
			got := checked(schema, in)
			if !containsFinding(got, tc.want) {
				t.Errorf("findings %v hold no %q", got, tc.want)
			}
		})
	}
}

func containsFinding(findings []string, want string) bool {
	for _, f := range findings {
		if strings.Contains(f, want) {
			return true
		}
	}
	return false
}

// TestAudienceExpansionAddsItsColumns: the expansion adds the five
// columns, with the defaults the declaration gives, and fills an empty
// @accessGrants and @accessLog class (D156).
func TestAudienceExpansionAddsItsColumns(t *testing.T) {
	schema, _ := accessSchema()
	schema.Types["Grant"] = &ir.TypeDef{Name: "Grant", Role: ir.RoleDBTable, AccessGrants: &ir.AccessGrantsDef{}}
	schema.Types["Log"] = &ir.TypeDef{Name: "Log", Role: ir.RoleDBTable, AccessLog: &ir.AccessLogDef{}}
	access.Expand(schema)
	defaults := map[string]map[string]string{
		"Folder":   {ir.AudienceField: ir.AudienceEveryone, ir.AgentAccessField: ir.AgentsFollowRole},
		"Document": {ir.AudienceField: ir.AudienceInherit, ir.AgentAccessField: ir.AgentsReadOnly},
	}
	for table, want := range defaults {
		td := schema.Types[table]
		for _, name := range ir.AudienceFields {
			fd := fieldNamed(td, name)
			if fd == nil || fd.Origin != ir.OriginAccess {
				t.Fatalf("%s has no %s of the expansion's: %+v", table, name, fd)
			}
			if value, ok := want[name]; ok && (fd.Default == nil || *fd.Default != value) {
				t.Errorf("%s.%s defaults to %v, want %s", table, name, fd.Default, value)
			}
		}
	}
	for _, name := range []string{ir.AccessGrantHolderField, ir.AccessGrantRoleField, ir.AccessGrantTargetIDField, ir.AccessGrantEndsAtField, ir.AccessGrantTokenHashField} {
		if fieldNamed(schema.Types["Grant"], name) == nil {
			t.Errorf("Grant has no %s", name)
		}
	}
	for _, name := range []string{ir.AccessLogKindField, ir.AccessLogPrincipalField, ir.AccessLogActingForField, ir.AccessLogFieldField} {
		if fieldNamed(schema.Types["Log"], name) == nil {
			t.Errorf("Log has no %s", name)
		}
	}
	// The expansion is idempotent: a second run adds nothing.
	before := len(schema.Types["Folder"].Fields)
	access.Expand(schema)
	if after := len(schema.Types["Folder"].Fields); after != before {
		t.Errorf("a second expansion took Folder from %d fields to %d", before, after)
	}
}
