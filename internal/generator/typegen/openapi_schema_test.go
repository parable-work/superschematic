package typegen

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	ir "github.com/parable-work/superschematic/ir"
)

// TestStrictGeneralTypesExposeStandaloneOpenAPISchema: a @strictJSON type
// in a General schema gets <Type>OpenAPISchema(), whose schema forbids
// undeclared keys at the root and in nested strict types, lists required
// fields, carries scalar constraints and the value type of a string-map
// scalar. A type without @strictJSON gets no accessor.
func TestStrictGeneralTypesExposeStandaloneOpenAPISchema(t *testing.T) {
	schema := ir.NewSchema("contracts", ir.SchemaKindGeneral)
	schema.Scalars["Identity.UUID"] = &ir.ScalarDef{
		Name:              "Identity.UUID",
		LanguagePrimitive: ir.LanguageString,
		Pattern:           "^[a-z0-9-]+$",
		Format:            "uuid",
		TypeMappings:      map[string]string{"json_schema": "string", "go": "UUID"},
	}
	schema.Scalars["Generic.StringMap"] = &ir.ScalarDef{
		Name:              "Generic.StringMap",
		LanguagePrimitive: ir.LanguageString,
		TypeMappings: map[string]string{
			"json_schema": "object",
			"go":          "GenericStringMap",
			"python":      "Dict[str, str]",
		},
	}
	schema.Types["Dispatch"] = &ir.TypeDef{
		Name:       "Dispatch",
		Role:       ir.RoleEmbeddedStruct,
		StrictJSON: true,
		Fields: []*ir.FieldDef{
			{Name: "job_id", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true},
			{Name: "trace_context", TypeRef: ir.TypeRef{Name: "Generic.StringMap"}},
		},
	}
	schema.Types["RunParameters"] = &ir.TypeDef{
		Name:       "RunParameters",
		Role:       ir.RoleEmbeddedStruct,
		StrictJSON: true,
		Fields: []*ir.FieldDef{
			{Name: "dispatch", TypeRef: ir.TypeRef{Name: "Dispatch"}, Required: true},
		},
	}
	schema.Types["Ordinary"] = &ir.TypeDef{
		Name: "Ordinary",
		Role: ir.RoleEmbeddedStruct,
		Fields: []*ir.FieldDef{
			{Name: "name", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
		},
	}

	output, err := Generate(schema, Options{
		SchemaName: "contracts",
		ModulePath: "example.com/schemas/types/go/contracts",
		Clock:      codegen.FixedClock(time.Unix(0, 0).UTC()),
	})
	if err != nil {
		t.Fatal(err)
	}

	var runSchemaJSON string
	for _, typeInfo := range output.Types {
		switch typeInfo.Name {
		case "RunParameters":
			runSchemaJSON, err = strconv.Unquote(typeInfo.OpenAPISchemaJSON)
			if err != nil {
				t.Fatal(err)
			}
		case "Ordinary":
			if typeInfo.OpenAPISchemaJSON != "" {
				t.Fatal("a type without @strictJSON received a schema accessor")
			}
		}
	}
	if runSchemaJSON == "" {
		t.Fatal("the strict General type has no OpenAPI schema")
	}

	var runSchema map[string]any
	if err := json.Unmarshal([]byte(runSchemaJSON), &runSchema); err != nil {
		t.Fatal(err)
	}
	if runSchema["title"] != "RunParameters" || runSchema["additionalProperties"] != false {
		t.Fatalf("root schema = %#v", runSchema)
	}
	assertStrings(t, runSchema["required"], "dispatch")
	properties := runSchema["properties"].(map[string]any)
	if properties["dispatch"].(map[string]any)["$ref"] != "#/definitions/Dispatch" {
		t.Fatalf("dispatch schema = %#v", properties["dispatch"])
	}

	dispatch := runSchema["definitions"].(map[string]any)["Dispatch"].(map[string]any)
	if dispatch["additionalProperties"] != false {
		t.Fatalf("nested additionalProperties = %#v", dispatch["additionalProperties"])
	}
	assertStrings(t, dispatch["required"], "job_id")
	dispatchProperties := dispatch["properties"].(map[string]any)
	jobID := dispatchProperties["job_id"].(map[string]any)
	if jobID["format"] != "uuid" || jobID["pattern"] != "^[a-z0-9-]+$" {
		t.Fatalf("Identity.UUID constraints = %#v", jobID)
	}
	traceContext := dispatchProperties["trace_context"].(map[string]any)
	if traceContext["type"] != "object" || traceContext["nullable"] != true {
		t.Fatalf("nullable trace_context schema = %#v", traceContext)
	}
	if values := traceContext["additionalProperties"].(map[string]any); values["type"] != "string" {
		t.Fatalf("trace_context values = %#v", values)
	}

	outDir := t.TempDir()
	if err := WriteTypes(output, outDir); err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile(filepath.Join(outDir, "types.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(source), "func RunParametersOpenAPISchema() map[string]any {") ||
		strings.Contains(string(source), "func OrdinaryOpenAPISchema()") {
		t.Fatal("the accessor must be emitted for the strict type only")
	}
}

func assertStrings(t *testing.T, value any, expected ...string) {
	t.Helper()
	values, ok := value.([]any)
	if !ok || len(values) != len(expected) {
		t.Fatalf("string list = %#v, want %#v", value, expected)
	}
	for i := range expected {
		if values[i] != expected[i] {
			t.Fatalf("string list = %#v, want %#v", value, expected)
		}
	}
}
