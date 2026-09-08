package yamlwriter

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader/schemafile"
	"github.com/parable-work/superschematic/internal/loader/yamlreader"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/registry/registrytest"
	ir "github.com/parable-work/superschematic/ir"
)

// acmeRegistry is the core registry plus the fixture extension, so writer
// output carrying acme extensions and documents reads back.
func acmeRegistry(t *testing.T) *registry.Registry {
	t.Helper()
	reg := registry.New(naming.Default())
	if err := reg.Use(registrytest.Acme{}); err != nil {
		t.Fatal(err)
	}
	return reg
}

// readBack runs writer output through the YAML reader pipeline (node tree,
// JSON Schema validation, strict decode, native comment mapping).
func readBack(t *testing.T, data []byte) *schemafile.Document {
	t.Helper()
	doc, err := yamlreader.ReadWith(data, "writer-output", acmeRegistry(t))
	if err != nil {
		t.Fatalf("writer output does not read back: %v\noutput:\n%s", err, data)
	}
	return doc
}

func requireEqualDocs(t *testing.T, want, got *schemafile.Document, output []byte) {
	t.Helper()
	wantJSON, _ := json.MarshalIndent(want, "", "  ")
	gotJSON, _ := json.MarshalIndent(got, "", "  ")
	if string(wantJSON) != string(gotJSON) {
		t.Errorf("document mismatch\nwant:\n%s\ngot:\n%s\noutput:\n%s", wantJSON, gotJSON, output)
	}
}

func TestWriteEmitsNativeComments(t *testing.T) {
	doc := &schemafile.Document{
		Name:    "fixture-db",
		Kind:    ir.SchemaKindDB,
		Comment: "Top of file.",
		Types: map[string]*ir.TypeDef{
			"Widget": {
				Name:    "Widget",
				Comment: "A widget.",
				Role:    ir.RoleDBTable,
				Fields: []*ir.FieldDef{
					{
						Name:     "id",
						Comment:  "Primary key.",
						TypeRef:  ir.TypeRef{Name: "string"},
						Required: true,
						Key:      true,
					},
				},
			},
		},
		Enums: map[string]*ir.EnumDef{
			"Status": {
				Name:    "Status",
				Comment: "Lifecycle states.",
				Values: []ir.EnumValueDef{
					{Name: "Active", SerializedAs: "active", Comment: "Live."},
				},
			},
		},
		Unions: map[string]*ir.UnionDef{
			"Pet": {Name: "Pet", Comment: "Union comment.", Types: []string{"Widget"}},
		},
		Scalars: map[string]*ir.ScalarDef{
			"Identity.UUID": {Name: "Identity.UUID", Comment: "Scalar comment.", LanguagePrimitive: ir.LanguageString},
		},
	}

	out, err := Write(doc)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	text := string(out)
	if strings.Contains(text, "comment:") {
		t.Errorf("output contains explicit comment keys; comments must be native:\n%s", text)
	}
	for _, want := range []string{"# Top of file.", "# A widget.", "# Primary key.", "# Lifecycle states.", "# Live.", "# Union comment.", "# Scalar comment."} {
		if !strings.Contains(text, want) {
			t.Errorf("output is missing native comment %q:\n%s", want, text)
		}
	}
	requireEqualDocs(t, doc, readBack(t, out), out)
}

func TestWriteOperationSetComments(t *testing.T) {
	doc := &schemafile.Document{
		Name: "fixture-api",
		Kind: ir.SchemaKindAPI,
		OperationSets: []*ir.OperationSet{
			{
				Name:    "TenantQueries",
				Comment: "Read operations.",
				Operations: []*ir.FieldDef{
					{
						Name:       "getTenant",
						Comment:    "Fetch one tenant.",
						TypeRef:    ir.TypeRef{Name: "string"},
						Required:   true,
						HTTPMethod: "GET",
						Arguments: []*ir.ArgumentDef{
							{Name: "id", Comment: "Tenant id.", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
						},
					},
				},
			},
		},
	}
	out, err := Write(doc)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	requireEqualDocs(t, doc, readBack(t, out), out)
}

func TestWriteSingleDefinitionForms(t *testing.T) {
	cases := []struct {
		name string
		doc  *schemafile.Document
		want string
	}{
		{
			name: "type",
			doc: &schemafile.Document{Types: map[string]*ir.TypeDef{
				"Widget": {Name: "Widget", Comment: "A widget.", Role: ir.RoleEmbeddedStruct, Fields: []*ir.FieldDef{
					{Name: "id", Comment: "Key.", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
				}},
			}},
			want: "role: EmbeddedStruct",
		},
		{
			name: "enum",
			doc: &schemafile.Document{Enums: map[string]*ir.EnumDef{
				"Status": {Name: "Status", Comment: "States.", Values: []ir.EnumValueDef{{Name: "Active", Comment: "Live."}}},
			}},
			want: "kind: Enum",
		},
		{
			name: "operation set",
			doc: &schemafile.Document{OperationSets: []*ir.OperationSet{
				{Name: "Queries", Comment: "Reads.", Operations: []*ir.FieldDef{
					{Name: "get", Comment: "Get.", TypeRef: ir.TypeRef{Name: "string"}, Required: true, HTTPMethod: "GET"},
				}},
			}},
			want: "kind: OperationSet",
		},
		{
			name: "scalar",
			doc: &schemafile.Document{Scalars: map[string]*ir.ScalarDef{
				"Identity.UUID": {Name: "Identity.UUID", LanguagePrimitive: ir.LanguageString},
			}},
			want: "kind: Scalar",
		},
		{
			name: "union",
			doc: &schemafile.Document{Unions: map[string]*ir.UnionDef{
				"Pet": {Name: "Pet", Types: []string{"Cat"}},
			}},
			want: "kind: Union",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := Write(tc.doc)
			if err != nil {
				t.Fatalf("Write: %v", err)
			}
			if !strings.Contains(string(out), tc.want) {
				t.Errorf("expected single-definition form containing %q:\n%s", tc.want, out)
			}
			requireEqualDocs(t, tc.doc, readBack(t, out), out)
		})
	}
}

func TestWriteDoesNotMutateInput(t *testing.T) {
	doc := &schemafile.Document{
		Name:    "svc",
		Kind:    ir.SchemaKindGeneral,
		Comment: "Doc comment.",
		Types: map[string]*ir.TypeDef{
			"Widget": {Name: "Widget", Comment: "A widget.", Role: ir.RoleEmbeddedStruct},
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
