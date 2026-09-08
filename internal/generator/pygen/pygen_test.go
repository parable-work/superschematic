package pygen

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

var update = flag.Bool("update", false, "rewrite golden files")

const fixturesDir = "../../loader/tsreader/testdata/services"

// TestWriteTypesGolden generates the Python types package for fixture
// services and compares every emitted file against its golden copy.
// Regenerate with:
// go test ./internal/generator/pygen -run TestWriteTypesGolden -update
func TestWriteTypesGolden(t *testing.T) {
	fixedClock := codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))

	cases := []struct {
		service string
		deps    []string
	}{
		{service: "fixture-db"},
		{service: "fixture-api", deps: []string{"fixture-db"}},
		{service: "fixture-general"},
	}

	for _, tc := range cases {
		t.Run(tc.service, func(t *testing.T) {
			schema, err := loader.LoadService(filepath.Join(fixturesDir, tc.service))
			if err != nil {
				t.Fatalf("load %s: %v", tc.service, err)
			}

			deps := map[string]*ir.Schema{}
			for _, dep := range tc.deps {
				depSchema, err := loader.LoadService(filepath.Join(fixturesDir, dep))
				if err != nil {
					t.Fatalf("load dependency %s: %v", dep, err)
				}
				deps[dep] = depSchema
			}

			output, err := Generate(schema, Options{
				SchemaName:   tc.service,
				Dependencies: deps,
				Clock:        fixedClock,
			})
			if err != nil {
				t.Fatalf("generate: %v", err)
			}

			outDir := filepath.Join(t.TempDir(), tc.service)
			if err := WriteTypes(output, outDir); err != nil {
				t.Fatalf("write types: %v", err)
			}

			if tc.service == "fixture-general" {
				generated, err := os.ReadFile(filepath.Join(outDir, "schemas_types_fixture_general", "types.py"))
				if err != nil {
					t.Fatalf("read generated types.py: %v", err)
				}
				const accessor = `def default_fixture_config() -> FixtureConfig:
    """Return a fresh FixtureConfig platform default."""
    return FixtureConfig.model_validate_json(`
				if !strings.Contains(string(generated), accessor) {
					t.Fatalf("types.py missing fresh composite default accessor:\n%s", generated)
				}
			}

			compareWithGolden(t, outDir, filepath.Join("testdata", "golden", tc.service))
		})
	}
}

func TestGenerateIntegerScalarWithoutPythonMapping(t *testing.T) {
	schema := ir.NewSchema("fixture", ir.SchemaKindGeneral)
	schema.Scalars["Temporal.Seconds"] = &ir.ScalarDef{
		Name:              "Temporal.Seconds",
		LanguagePrimitive: ir.LanguageNumber,
		Primitive:         "Int",
		TypeMappings: map[string]string{
			"sql": "BIGINT",
		},
	}
	schema.Types["Timeout"] = &ir.TypeDef{
		Name: "Timeout",
		Role: ir.RoleEmbeddedStruct,
		Fields: []*ir.FieldDef{
			{
				Name:     "seconds",
				TypeRef:  ir.TypeRef{Name: "Temporal.Seconds"},
				Required: true,
			},
		},
	}

	output, err := Generate(schema, Options{
		SchemaName: "fixture",
		Clock:      codegen.FixedClock(time.Unix(0, 0).UTC()),
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	if output.Scalars[0].TargetType != "int" {
		t.Fatalf("scalar TargetType = %q, want int", output.Scalars[0].TargetType)
	}
	if output.Types[0].Fields[0].TargetType != "TemporalSeconds" {
		t.Fatalf("field TargetType = %q, want TemporalSeconds", output.Types[0].Fields[0].TargetType)
	}
}

func TestPythonFieldNameAvoidsPydanticPrivateAttribute(t *testing.T) {
	if got := toPythonFieldName("_registrationKey"); got != "registration_key_" {
		t.Fatalf("toPythonFieldName() = %q, want registration_key_", got)
	}
}

// compareWithGolden compares every file in gotDir against goldenDir,
// rewriting the goldens when -update is set.
func compareWithGolden(t *testing.T, gotDir, goldenDir string) {
	t.Helper()

	gotFiles := listFiles(t, gotDir)

	if *update {
		if err := os.RemoveAll(goldenDir); err != nil {
			t.Fatalf("clear golden dir: %v", err)
		}
		for _, rel := range gotFiles {
			data, err := os.ReadFile(filepath.Join(gotDir, rel))
			if err != nil {
				t.Fatalf("read generated %s: %v", rel, err)
			}
			dest := filepath.Join(goldenDir, rel)
			if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
				t.Fatalf("create golden dir: %v", err)
			}
			if err := os.WriteFile(dest, data, 0o644); err != nil {
				t.Fatalf("write golden %s: %v", rel, err)
			}
		}
		return
	}

	goldenFiles := listFiles(t, goldenDir)

	gotSet := map[string]bool{}
	for _, rel := range gotFiles {
		gotSet[rel] = true
	}
	for _, rel := range goldenFiles {
		if !gotSet[rel] {
			t.Errorf("golden file %s was not generated", rel)
		}
	}

	goldenSet := map[string]bool{}
	for _, rel := range goldenFiles {
		goldenSet[rel] = true
	}

	for _, rel := range gotFiles {
		if !goldenSet[rel] {
			t.Errorf("generated unexpected file %s (run with -update to accept)", rel)
			continue
		}
		got, err := os.ReadFile(filepath.Join(gotDir, rel))
		if err != nil {
			t.Fatalf("read generated %s: %v", rel, err)
		}
		want, err := os.ReadFile(filepath.Join(goldenDir, rel))
		if err != nil {
			t.Fatalf("read golden %s: %v", rel, err)
		}
		if string(got) != string(want) {
			t.Errorf("%s differs from golden (run with -update to accept):\n--- got ---\n%s", rel, got)
		}
	}
}

func listFiles(t *testing.T, dir string) []string {
	t.Helper()
	var files []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	return files
}

// TestGenerateUnionsAndDefaults exercises discriminated unions, Literal
// rewriting, model_rebuild collection, and @default literal emission on a
// hand-built IR (the fixtures do not declare unions).
func TestGenerateUnionsAndDefaults(t *testing.T) {
	schema := ir.NewSchema("synthetic", ir.SchemaKindGeneral)

	discriminatorA := "card"
	discriminatorB := "wire"
	enabledDefault := "true"
	limitDefault := "25"
	labelDefault := "pending"

	schema.Types["CardPayment"] = &ir.TypeDef{
		Name: "CardPayment",
		Role: ir.RoleEmbeddedStruct,
		Fields: []*ir.FieldDef{
			{Name: "kind", TypeRef: ir.TypeRef{Name: "string"}, Required: true, InternalMetadata: true, Default: &discriminatorA},
			{Name: "last4", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
		},
	}
	schema.Types["WirePayment"] = &ir.TypeDef{
		Name: "WirePayment",
		Role: ir.RoleEmbeddedStruct,
		Fields: []*ir.FieldDef{
			{Name: "kind", TypeRef: ir.TypeRef{Name: "string"}, Required: true, InternalMetadata: true, Default: &discriminatorB},
			{Name: "iban", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
		},
	}
	schema.Unions["Payment"] = &ir.UnionDef{
		Name:  "Payment",
		Types: []string{"CardPayment", "WirePayment"},
	}
	schema.Types["Settings"] = &ir.TypeDef{
		Name: "Settings",
		Role: ir.RoleEmbeddedStruct,
		Fields: []*ir.FieldDef{
			{Name: "enabled", TypeRef: ir.TypeRef{Name: "boolean"}, Required: true, Default: &enabledDefault},
			{Name: "limit", TypeRef: ir.TypeRef{Name: "number"}, Required: true, Default: &limitDefault},
			{Name: "label", TypeRef: ir.TypeRef{Name: "string"}, Required: true, Default: &labelDefault},
			{Name: "payment", TypeRef: ir.TypeRef{Name: "Payment"}, Required: false},
		},
	}

	output, err := Generate(schema, Options{
		SchemaName: "synthetic",
		Clock:      codegen.FixedClock(time.Unix(0, 0).UTC()),
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	if len(output.Unions) != 1 {
		t.Fatalf("expected 1 union, got %d", len(output.Unions))
	}
	if !output.NeedsLiteralImport {
		t.Error("expected NeedsLiteralImport for discriminated union members")
	}
	if len(output.ModelRebuildTypes) != 1 || output.ModelRebuildTypes[0] != "Settings" {
		t.Errorf("expected ModelRebuildTypes [Settings], got %v", output.ModelRebuildTypes)
	}

	var card *codegen.TypeInfo
	for i := range output.Types {
		if output.Types[i].Name == "CardPayment" {
			card = &output.Types[i]
		}
	}
	if card == nil {
		t.Fatal("CardPayment type not generated")
	}
	for _, field := range card.Fields {
		if field.Name == "kind" && field.TargetType != `Literal["card"]` {
			t.Errorf("kind field: expected Literal[\"card\"], got %q", field.TargetType)
		}
	}

	outDir := t.TempDir()
	if err := WriteTypes(output, outDir); err != nil {
		t.Fatalf("write types: %v", err)
	}

	moduleDir := filepath.Join(outDir, "schemas_types_synthetic")
	for _, name := range []string{
		"__init__.py",
		"types.py",
		"unions.py",
		"validation_errors.py",
		"py.typed",
	} {
		if _, err := os.Stat(filepath.Join(moduleDir, name)); err != nil {
			t.Errorf("expected output file %s: %v", name, err)
		}
	}

	typesPy, err := os.ReadFile(filepath.Join(moduleDir, "types.py"))
	if err != nil {
		t.Fatalf("read types.py: %v", err)
	}
	for _, want := range []string{
		`kind: Literal["card"] = Field(default="card", alias="kind", serialization_alias="kind")`,
		`enabled: bool = Field(default=True, alias="enabled", serialization_alias="enabled")`,
		`limit: float = Field(default=25, alias="limit", serialization_alias="limit")`,
		`label: str = Field(default="pending", alias="label", serialization_alias="label")`,
	} {
		if !strings.Contains(string(typesPy), want) {
			t.Errorf("types.py missing %q:\n%s", want, typesPy)
		}
	}

	unionsPy, err := os.ReadFile(filepath.Join(moduleDir, "unions.py"))
	if err != nil {
		t.Fatalf("read unions.py: %v", err)
	}
	if !strings.Contains(string(unionsPy), `Field(discriminator="kind")`) {
		t.Errorf("unions.py missing discriminator annotation:\n%s", unionsPy)
	}

	initPy, err := os.ReadFile(filepath.Join(moduleDir, "__init__.py"))
	if err != nil {
		t.Fatalf("read __init__.py: %v", err)
	}
	if !strings.Contains(string(initPy), "_types_mod.Settings.model_rebuild(_types_namespace=_types_namespace)") {
		t.Errorf("__init__.py missing model_rebuild for Settings:\n%s", initPy)
	}
}

func TestPythonDateTimeTypeMappingUsesDateTimeValidator(t *testing.T) {
	schema := ir.NewSchema("python-datetime-mapping", ir.SchemaKindGeneral)
	schema.Scalars["ExternalTimestamp"] = &ir.ScalarDef{
		Name:              "ExternalTimestamp",
		LanguagePrimitive: ir.LanguageString,
		TypeMappings:      map[string]string{"python": "datetime.datetime"},
		Comment:           "External timestamp",
	}
	schema.Types["AuditEvent"] = &ir.TypeDef{
		Name: "AuditEvent",
		Role: ir.RoleEmbeddedStruct,
		Fields: []*ir.FieldDef{
			{Name: "createdAt", TypeRef: ir.TypeRef{Name: "ExternalTimestamp"}, Required: true},
		},
	}

	output, err := Generate(schema, Options{
		SchemaName: "python-datetime-mapping",
		Clock:      codegen.FixedClock(time.Unix(0, 0).UTC()),
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	outDir := t.TempDir()
	if err := WriteTypes(output, outDir); err != nil {
		t.Fatalf("write types: %v", err)
	}

	moduleDir := filepath.Join(outDir, "schemas_types_python_datetime_mapping")
	scalarsContent, err := os.ReadFile(filepath.Join(moduleDir, "scalars.py"))
	if err != nil {
		t.Fatalf("read scalars.py: %v", err)
	}
	typesContent, err := os.ReadFile(filepath.Join(moduleDir, "types.py"))
	if err != nil {
		t.Fatalf("read types.py: %v", err)
	}

	for _, want := range []string{
		"def _parse_datetime(v: Any) -> datetime:",
		"PlainValidator(_parse_datetime),",
		`PlainSerializer(_serialize_datetime, return_type=str, when_used="json"),`,
	} {
		if !strings.Contains(string(scalarsContent), want) {
			t.Errorf("scalars.py missing %q:\n%s", want, scalarsContent)
		}
	}
	if !strings.Contains(string(typesContent), "created_at: ExternalTimestamp") {
		t.Errorf("types.py should reference the generated DateTime-like scalar:\n%s", typesContent)
	}
}

// TestPythonFieldNames verifies snake_case conversion and Python keyword
// suffixing for field names.
func TestPythonFieldNames(t *testing.T) {
	cases := map[string]string{
		"userName":  "user_name",
		"id":        "id",
		"from":      "from_",
		"importRef": "import_ref",
		"class":     "class_",
	}
	for in, want := range cases {
		if got := toPythonFieldName(in); got != want {
			t.Errorf("toPythonFieldName(%q) = %q, want %q", in, got, want)
		}
	}
}
