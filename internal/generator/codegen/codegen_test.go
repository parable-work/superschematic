package codegen

import (
	"embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

//go:embed templates/*.tmpl
var testTemplatesFS embed.FS

func TestCaseHelpers(t *testing.T) {
	cases := []struct {
		fn   func(string) string
		in   string
		want string
	}{
		{ToSnakeCase, "UserName", "user_name"},
		{ToSnakeCase, "connector-requests", "connector_requests"},
		{ToKebabCase, "UserName", "user-name"},
		{ToCamelCase, "connector-requests", "connectorRequests"},
		{ToPascalCase, "some_field", "SomeField"},
		{ToPlural, "sync-job", "sync-jobs"},
		{ToPlural, "person", "people"},
		{ToPlural, "category", "categories"},
	}
	for _, tc := range cases {
		if got := tc.fn(tc.in); got != tc.want {
			t.Errorf("case helper(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestBuildScalarTokens(t *testing.T) {
	tokens := BuildScalarTokens("Identity.UUID")
	if tokens.Symbol != "IdentityUUID" {
		t.Errorf("Symbol = %q, want IdentityUUID", tokens.Symbol)
	}
	if tokens.Module != "identity_uuid" {
		t.Errorf("Module = %q, want identity_uuid", tokens.Module)
	}
	if tokens.Leaf() != "UUID" {
		t.Errorf("Leaf = %q, want UUID", tokens.Leaf())
	}

	flat := BuildScalarTokens("UUID")
	if flat.Symbol != "UUID" || flat.Leaf() != "UUID" {
		t.Errorf("flat tokens = %+v", flat)
	}
}

func TestLanguagePrimitives(t *testing.T) {
	if !IsLanguagePrimitive("string") || !IsLanguagePrimitive("number") || !IsLanguagePrimitive("boolean") {
		t.Error("language primitives not recognized")
	}
	if IsLanguagePrimitive("String") || IsLanguagePrimitive("Int") || IsLanguagePrimitive("ID") {
		t.Error("GraphQL builtins must not be recognized as v2 primitives")
	}
	if PrimitiveOf("number") != ir.LanguageNumber {
		t.Error("PrimitiveOf(number) wrong")
	}
}

func TestExtractScalarsUsesTypeMappingsAndFallback(t *testing.T) {
	schema := ir.NewSchema("svc", ir.SchemaKindGeneral)
	schema.Scalars["Identity.UUID"] = &ir.ScalarDef{
		Name:              "Identity.UUID",
		LanguagePrimitive: ir.LanguageString,
		TypeMappings:      map[string]string{"go": "UUID"},
		HasCustomParse:    true,
	}
	schema.Scalars["Temporal.DateTime"] = &ir.ScalarDef{
		Name:              "Temporal.DateTime",
		LanguagePrimitive: ir.LanguageString,
	}

	scalars := ExtractScalars(schema, ExtractionConfig{
		Language: "go",
		PrimitiveMapper: func(p ir.LanguagePrimitive, name string) string {
			if p == ir.LanguageString {
				return "string"
			}
			return ""
		},
	})

	if len(scalars) != 2 {
		t.Fatalf("got %d scalars, want 2", len(scalars))
	}
	// Sorted by name: Identity.UUID first.
	if scalars[0].TargetType != "UUID" {
		t.Errorf("Identity.UUID TargetType = %q, want UUID (from TypeMappings)", scalars[0].TargetType)
	}
	if !scalars[0].HasCustomParse {
		t.Error("HasCustomParse must come from the IR")
	}
	if !scalars[0].Traits.IsUUIDLike {
		t.Error("Identity.UUID should have UUID-like traits")
	}
	if scalars[1].TargetType != "string" {
		t.Errorf("Temporal.DateTime TargetType = %q, want string (fallback)", scalars[1].TargetType)
	}
	if !scalars[1].Traits.IsDateTimeLike {
		t.Error("Temporal.DateTime should have datetime-like traits")
	}
}

func TestExtractTypesFiltersByRole(t *testing.T) {
	schema := ir.NewSchema("svc", ir.SchemaKindDB)
	schema.Types["Tenant"] = &ir.TypeDef{
		Name: "Tenant",
		Role: ir.RoleDBTable,
		Fields: []*ir.FieldDef{
			{Name: "id", TypeRef: ir.TypeRef{Name: "string"}, Required: true, Key: true},
			{Name: "createdAt", TypeRef: ir.TypeRef{Name: "string"}, Required: true, InheritedFrom: "Auditable"},
		},
	}
	schema.Types["Helper"] = &ir.TypeDef{
		Name: "Helper",
		Role: ir.RoleEmbeddedStruct,
	}

	tables := ExtractTypes(schema, nil, ExtractionConfig{}, ir.RoleDBTable)
	if len(tables) != 1 || tables[0].Name != "Tenant" {
		t.Fatalf("ExtractTypes(DBTable) = %v", tables)
	}
	if tables[0].Fields[1].InheritedFrom != "Auditable" {
		t.Error("InheritedFrom not propagated to FieldInfo")
	}

	all := ExtractTypes(schema, nil, ExtractionConfig{}, ir.RoleDBTable, ir.RoleEmbeddedStruct)
	if len(all) != 2 {
		t.Fatalf("ExtractTypes(both roles) = %d types, want 2", len(all))
	}
}

func TestBaseTypeNames(t *testing.T) {
	schema := ir.NewSchema("svc", ir.SchemaKindDB)
	schema.Types["Auditable"] = &ir.TypeDef{Name: "Auditable", Role: ir.RoleDBTable}
	schema.Types["Tenant"] = &ir.TypeDef{Name: "Tenant", Role: ir.RoleDBTable, Extends: "Auditable"}

	bases := BaseTypeNames(schema)
	if !bases["Auditable"] || bases["Tenant"] {
		t.Errorf("BaseTypeNames = %v", bases)
	}
}

func TestClassifyDefault(t *testing.T) {
	str := "x"
	cases := []struct {
		field FieldInfo
		want  DefaultLiteralKind
	}{
		{FieldInfo{Type: "boolean", Default: ptr("true")}, DefaultLiteralBool},
		{FieldInfo{Type: "number", Default: ptr("5")}, DefaultLiteralInt},
		{FieldInfo{Type: "number", Default: ptr("2.5")}, DefaultLiteralFloat},
		{FieldInfo{Type: "string", Default: &str}, DefaultLiteralString},
		{FieldInfo{Type: "string"}, DefaultLiteralUnknown},
		{FieldInfo{Type: "string", Default: &str, IsArray: true}, DefaultLiteralUnknown},
	}
	for i, tc := range cases {
		if got := ClassifyDefault(tc.field, nil); got != tc.want {
			t.Errorf("case %d: ClassifyDefault = %v, want %v", i, got, tc.want)
		}
	}

	enumField := FieldInfo{Type: "TenantStatus", Default: ptr("active")}
	got := ClassifyDefault(enumField, func(name string) bool { return name == "TenantStatus" })
	if got != DefaultLiteralEnum {
		t.Errorf("enum default = %v, want DefaultLiteralEnum", got)
	}
}

func TestDocText(t *testing.T) {
	if DocText("desc", "comment") != "desc" {
		t.Error("description should win")
	}
	if DocText("", "comment") != "comment" {
		t.Error("comment should be the fallback")
	}
}

func TestRunParallelReturnsLowestIndexedError(t *testing.T) {
	firstErr := errors.New("first")
	secondErr := errors.New("second")

	err := RunParallel([]func() error{
		func() error { return nil },
		func() error { return fmt.Errorf("wrapped: %w", firstErr) },
		func() error { return fmt.Errorf("wrapped: %w", secondErr) },
	})
	if !errors.Is(err, firstErr) {
		t.Fatalf("RunParallel error = %v, want first indexed error", err)
	}
}

func TestGenerateFileCreatesOutputDirByDefault(t *testing.T) {
	outPath := filepath.Join(t.TempDir(), "nested", "out.txt")

	err := GenerateFile(NewFileConfig(testTemplatesFS, "plain.tmpl", outPath, map[string]string{"Value": "ok"}, nil))
	if err != nil {
		t.Fatalf("GenerateFile returned error: %v", err)
	}

	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("failed to read generated file: %v", err)
	}
	if string(got) != "ok\n" {
		t.Fatalf("generated file = %q, want ok newline", string(got))
	}
}

func TestGenerateFileCanSkipOutputDirCreation(t *testing.T) {
	outPath := filepath.Join(t.TempDir(), "nested", "out.txt")
	cfg := NewFileConfig(testTemplatesFS, "plain.tmpl", outPath, map[string]string{"Value": "ok"}, nil)
	cfg.OutputDirExists = true

	err := GenerateFile(cfg)
	if err == nil {
		t.Fatal("GenerateFile succeeded with missing output dir")
	}
}

func ptr(s string) *string { return &s }
