package jsonwriter

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader/schemafile"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/registry/registrytest"
	ir "github.com/parable-work/superschematic/ir"
)

// decodeBack runs writer output through the reader pipeline (JSON Schema
// validation + strict decode); every writer byte stream must decode.
func decodeBack(t *testing.T, data []byte) *schemafile.Document {
	t.Helper()
	doc, err := schemafile.DecodeWith(data, "writer-output", acmeRegistry(t))
	if err != nil {
		t.Fatalf("writer output does not decode: %v\noutput:\n%s", err, data)
	}
	return doc
}

// acmeRegistry is the core registry plus the fixture extension, so writer
// output carrying acme extensions and documents decodes.
func acmeRegistry(t *testing.T) *registry.Registry {
	t.Helper()
	reg := registry.New(naming.Default())
	if err := reg.Use(registrytest.Acme{}); err != nil {
		t.Fatal(err)
	}
	return reg
}

func requireEqualDocs(t *testing.T, want, got *schemafile.Document) {
	t.Helper()
	wantJSON, _ := json.MarshalIndent(want, "", "  ")
	gotJSON, _ := json.MarshalIndent(got, "", "  ")
	if string(wantJSON) != string(gotJSON) {
		t.Errorf("document mismatch\nwant:\n%s\ngot:\n%s", wantJSON, gotJSON)
	}
}

func TestWriteDocumentFormRoundTrips(t *testing.T) {
	doc := &schemafile.Document{
		Name:    "fixture-db",
		Kind:    ir.SchemaKindDB,
		Comment: "Top of file.",
		Imports: []ir.Import{{Package: "@parable-platform/web-db", Types: []string{"Tenant"}}},
		Scalars: map[string]*ir.ScalarDef{
			"Identity.UUID": {Name: "Identity.UUID", LanguagePrimitive: ir.LanguageString},
		},
		Types: map[string]*ir.TypeDef{
			"Widget": {
				Name:    "Widget",
				Comment: "A widget.",
				Role:    ir.RoleDBTable,
				Fields: []*ir.FieldDef{
					{
						Name:     "id",
						Comment:  "Primary key.",
						TypeRef:  ir.TypeRef{Name: "Identity.UUID"},
						Required: true,
						Key:      true,
						Unique:   true,
					},
					{Name: "tags", TypeRef: ir.TypeRef{Name: "string", IsArray: true}},
				},
				Indexes: []ir.IndexDef{{Keys: []string{"id"}}},
			},
		},
		Enums: map[string]*ir.EnumDef{
			"Status": {
				Name: "Status",
				Values: []ir.EnumValueDef{
					{Name: "Active", SerializedAs: "active", Comment: "Live."},
				},
			},
		},
	}

	out, err := Write(doc)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if !strings.HasSuffix(string(out), "\n") {
		t.Error("output is missing a trailing newline")
	}
	requireEqualDocs(t, doc, decodeBack(t, out))
}

func TestWriteSingleDefinitionForms(t *testing.T) {
	cases := []struct {
		name string
		doc  *schemafile.Document
		key  string
	}{
		{
			name: "type",
			doc: &schemafile.Document{Types: map[string]*ir.TypeDef{
				"Widget": {Name: "Widget", Role: ir.RoleEmbeddedStruct, Fields: []*ir.FieldDef{
					{Name: "id", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
				}},
			}},
			key: `"role"`,
		},
		{
			name: "enum",
			doc: &schemafile.Document{Enums: map[string]*ir.EnumDef{
				"Status": {Name: "Status", Values: []ir.EnumValueDef{{Name: "Active"}}},
			}},
			key: `"kind": "Enum"`,
		},
		{
			name: "union",
			doc: &schemafile.Document{Unions: map[string]*ir.UnionDef{
				"Pet": {Name: "Pet", Types: []string{"Cat", "Dog"}},
			}},
			key: `"kind": "Union"`,
		},
		{
			name: "scalar",
			doc: &schemafile.Document{Scalars: map[string]*ir.ScalarDef{
				"Identity.UUID": {Name: "Identity.UUID", LanguagePrimitive: ir.LanguageString},
			}},
			key: `"kind": "Scalar"`,
		},
		{
			name: "operation set",
			doc: &schemafile.Document{OperationSets: []*ir.OperationSet{
				{Name: "TenantQueries", Operations: []*ir.FieldDef{
					{Name: "getTenant", TypeRef: ir.TypeRef{Name: "string"}, Required: true, HTTPMethod: "GET"},
				}},
			}},
			key: `"kind": "OperationSet"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := Write(tc.doc)
			if err != nil {
				t.Fatalf("Write: %v", err)
			}
			if !strings.Contains(string(out), tc.key) {
				t.Errorf("expected single-definition form containing %s, got:\n%s", tc.key, out)
			}
			requireEqualDocs(t, tc.doc, decodeBack(t, out))
		})
	}
}

// TestWriteValidatesUnionMemberTypesUntouched guards against the writer
// reordering or mutating definition content: writing is projection only.
func TestWriteDoesNotMutateInput(t *testing.T) {
	doc := &schemafile.Document{
		Name: "svc",
		Kind: ir.SchemaKindGeneral,
		Types: map[string]*ir.TypeDef{
			"Widget": {Name: "Widget", Role: ir.RoleEmbeddedStruct},
		},
	}
	before, _ := json.Marshal(doc)
	if _, err := Write(doc); err != nil {
		t.Fatalf("Write: %v", err)
	}
	after, _ := json.Marshal(doc)
	if string(before) != string(after) {
		t.Error("Write mutated its input document")
	}
}
