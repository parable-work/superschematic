package typegen

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

var update = flag.Bool("update", false, "rewrite golden files")

const fixturesDir = "../../loader/tsreader/testdata/services"

// TestWriteTypesGolden generates the Go types module for fixture services
// and compares every emitted file against its golden copy. Regenerate with:
// go test ./internal/generator/typegen -run TestWriteTypesGolden -update
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
			depModules := map[string]string{}
			for _, dep := range tc.deps {
				depSchema, err := loader.LoadService(filepath.Join(fixturesDir, dep))
				if err != nil {
					t.Fatalf("load dependency %s: %v", dep, err)
				}
				deps[dep] = depSchema
				depModules[dep] = "example.com/schemas/types/go/" + dep
			}

			output, err := Generate(schema, Options{
				SchemaName:        tc.service,
				ModulePath:        "example.com/schemas/types/go/" + tc.service,
				Dependencies:      deps,
				DependencyModules: depModules,
				Clock:             fixedClock,
			})
			if err != nil {
				t.Fatalf("generate: %v", err)
			}

			// Use a superscalar path inside the temp tree so the computed
			// relative replace paths are deterministic across machines.
			tempRoot := t.TempDir()
			outDir := filepath.Join(tempRoot, tc.service)
			paths := naming.LocalPaths{ScalarGo: filepath.Join(tempRoot, "scalars", "go"), SchemaIR: filepath.Join(tempRoot, "ir")}
			if err := SetReplacePaths(output, paths, outDir); err != nil {
				t.Fatalf("set replace paths: %v", err)
			}

			if err := WriteTypes(output, outDir); err != nil {
				t.Fatalf("write types: %v", err)
			}

			if tc.service == "fixture-general" {
				generated, err := os.ReadFile(filepath.Join(outDir, "types.go"))
				if err != nil {
					t.Fatalf("read generated types.go: %v", err)
				}
				const accessor = `func DefaultFixtureConfig() FixtureConfig {
	var value FixtureConfig
	if err := json.Unmarshal([]byte(`
				if !strings.Contains(string(generated), accessor) {
					t.Fatalf("types.go missing fresh composite default accessor:\n%s", generated)
				}
			}

			compareWithGolden(t, outDir, filepath.Join("testdata", "golden", tc.service))
		})
	}
}

func TestGenerateTypedRecordFields(t *testing.T) {
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-maps"))
	if err != nil {
		t.Fatalf("load fixture-maps: %v", err)
	}
	output, err := Generate(schema, Options{
		ModulePath: "github.com/acme-platform/fixture-maps",
		Clock:      codegen.FixedClock(time.Unix(0, 0).UTC()),
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	var fields []FieldInfo
	for _, typeInfo := range output.Types {
		if typeInfo.Name == "MapContainer" {
			fields = typeInfo.Fields
			break
		}
	}
	if len(fields) != 2 {
		t.Fatalf("MapContainer fields = %d, want 2", len(fields))
	}
	if fields[0].GoType != "map[string]string" {
		t.Fatalf("strings GoType = %q", fields[0].GoType)
	}
	if fields[1].GoType != "map[string]MapValue" {
		t.Fatalf("nested GoType = %q", fields[1].GoType)
	}

	outDir := filepath.Join(t.TempDir(), "fixture-maps")
	if err := WriteTypes(output, outDir); err != nil {
		t.Fatalf("write types: %v", err)
	}
	typesSource, err := os.ReadFile(filepath.Join(outDir, "types.go"))
	if err != nil {
		t.Fatalf("read types.go: %v", err)
	}
	source := string(typesSource)
	if !strings.Contains(source, "for key, item := range t.Nested") {
		t.Fatal("nested map methods must iterate typed values")
	}
	if strings.Contains(source, "t.Nested.MaskSecrets()") || strings.Contains(source, "t.Nested.Validate()") {
		t.Fatal("generated methods must not call object methods on maps")
	}
}

func TestGenerateRequiredNestedArrayWithZeroMinimumAllowsEmpty(t *testing.T) {
	schema := ir.NewSchema("archive-contract", ir.SchemaKindGeneral)
	schema.Types["ArchiveChange"] = &ir.TypeDef{
		Name: "ArchiveChange",
		Role: ir.RoleAPIView,
		Fields: []*ir.FieldDef{
			{Name: "name", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
		},
	}
	zero := 0
	one := 1
	schema.Types["ArchivePlan"] = &ir.TypeDef{
		Name: "ArchivePlan",
		Role: ir.RoleAPIView,
		Fields: []*ir.FieldDef{
			{
				Name:            "changes",
				TypeRef:         ir.TypeRef{Name: "ArchiveChange", IsArray: true},
				Required:        true,
				ValidateListMin: &zero,
			},
			{
				Name:            "requiredChanges",
				TypeRef:         ir.TypeRef{Name: "ArchiveChange", IsArray: true},
				Required:        true,
				ValidateListMin: &one,
			},
		},
	}

	output, err := Generate(schema, Options{
		SchemaName: "archive-contract",
		ModulePath: "github.com/acme-platform/archive-contract",
		Clock:      codegen.FixedClock(time.Unix(0, 0).UTC()),
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	outDir := filepath.Join(t.TempDir(), "archive-contract")
	if err := WriteTypes(output, outDir); err != nil {
		t.Fatalf("write types: %v", err)
	}
	generated, err := os.ReadFile(filepath.Join(outDir, "types.go"))
	if err != nil {
		t.Fatalf("read generated types: %v", err)
	}
	source := string(generated)

	if !strings.Contains(source, "if t.Changes == nil {") {
		t.Fatal("listMin: 0 must require a present array without rejecting an empty array")
	}
	if strings.Contains(source, "if t.Changes == nil || len(t.Changes) == 0 {") {
		t.Fatal("listMin: 0 must not reject an empty array")
	}
	if strings.Contains(source, "len(value) < 0") {
		t.Fatal("listMin: 0 must not emit an impossible validation check")
	}
	if !strings.Contains(source, "if t.RequiredChanges == nil || len(t.RequiredChanges) == 0 {") {
		t.Fatal("positive listMin must keep the required non-empty array check")
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

// TestGenerateUnionsAndDefaults exercises union wrapper metadata and @default
// literal classification on a hand-built IR (the TS fixtures do not declare
// unions or defaults).
func TestGenerateUnionsAndDefaults(t *testing.T) {
	schema := ir.NewSchema("synthetic", ir.SchemaKindGeneral)

	discriminatorA := "card"
	discriminatorB := "wire"
	enabledDefault := "true"
	limitDefault := "25"
	labelDefault := "pending"
	modeDefault := "strict"
	maxViolationsDefault := "0"

	schema.Enums["Mode"] = &ir.EnumDef{
		Name: "Mode",
		Values: []ir.EnumValueDef{
			{Name: "STRICT", SerializedAs: "strict"},
			{Name: "RELAXED", SerializedAs: "relaxed"},
		},
	}
	schema.Scalars["Generic.Int64"] = &ir.ScalarDef{
		Name:              "Generic.Int64",
		LanguagePrimitive: ir.LanguageNumber,
	}

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
			{Name: "mode", TypeRef: ir.TypeRef{Name: "Mode"}, Required: true, Default: &modeDefault},
			{Name: "maxViolations", TypeRef: ir.TypeRef{Name: "Generic.Int64"}, Required: true, Default: &maxViolationsDefault},
			{Name: "payment", TypeRef: ir.TypeRef{Name: "Payment"}, Required: false},
		},
	}

	output, err := Generate(schema, Options{
		SchemaName: "synthetic",
		ModulePath: "example.com/synthetic",
		Clock:      codegen.FixedClock(time.Unix(0, 0).UTC()),
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	if len(output.Unions) != 1 {
		t.Fatalf("expected 1 union, got %d", len(output.Unions))
	}
	union := output.Unions[0]
	if union.Discriminator != "kind" {
		t.Errorf("expected discriminator %q, got %q", "kind", union.Discriminator)
	}
	if !union.DispatchByDiscriminator {
		t.Error("expected discriminator dispatch")
	}
	if union.Members[0].DiscriminatorValue != "card" || union.Members[1].DiscriminatorValue != "wire" {
		t.Errorf("unexpected member discriminator values: %+v", union.Members)
	}

	var settings *TypeInfo
	for i := range output.Types {
		if output.Types[i].Name == "Settings" {
			settings = &output.Types[i]
		}
	}
	if settings == nil {
		t.Fatal("Settings type not generated")
	}
	if !settings.HasDefaults {
		t.Fatal("expected Settings.HasDefaults")
	}

	wantDefaults := map[string]string{
		"enabled":       "true",
		"limit":         "25",
		"label":         `"pending"`,
		"mode":          `Mode("strict")`,
		"maxViolations": "0",
	}
	for _, field := range settings.Fields {
		want, ok := wantDefaults[field.Name]
		if !ok {
			continue
		}
		if !field.HasDefault || field.DefaultLiteral != want {
			t.Errorf("field %s: expected default literal %q, got %q (has=%v)", field.Name, want, field.DefaultLiteral, field.HasDefault)
		}
	}

	var payment *FieldInfo
	for i := range settings.Fields {
		if settings.Fields[i].Name == "payment" {
			payment = &settings.Fields[i]
		}
	}
	if payment == nil || !payment.IsUnion {
		t.Fatalf("expected payment field flagged as union, got %+v", payment)
	}

	outDir := t.TempDir()
	if err := WriteTypes(output, outDir); err != nil {
		t.Fatalf("write types: %v", err)
	}
	for _, name := range []string{"go.mod", "types.go", "unions.go", "README.md"} {
		if _, err := os.Stat(filepath.Join(outDir, name)); err != nil {
			t.Errorf("expected output file %s: %v", name, err)
		}
	}
	typesSource, err := os.ReadFile(filepath.Join(outDir, "types.go"))
	if err != nil {
		t.Fatalf("read generated types: %v", err)
	}
	if strings.Contains(string(typesSource), "t.Mode.ValidateRequired()") {
		t.Error("defaulted enum should not receive required zero-value validation")
	}
	if strings.Contains(string(typesSource), "t.MaxViolations.ValidateRequired()") {
		t.Error("defaulted scalar should not receive required zero-value validation")
	}
}

func TestGenerateVersionedTypeMetadata(t *testing.T) {
	schema := ir.NewSchema("synthetic", ir.SchemaKindDB)
	schema.Types["Widget"] = &ir.TypeDef{
		Name:      "Widget",
		Role:      ir.RoleDBTable,
		Versioned: true,
		Fields: []*ir.FieldDef{
			{Name: "id", TypeRef: ir.TypeRef{Name: "string"}, Required: true, Key: true},
			{Name: "name", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
		},
	}

	output, err := Generate(schema, Options{
		SchemaName: "synthetic",
		ModulePath: "example.com/synthetic",
		Clock:      codegen.FixedClock(time.Unix(0, 0).UTC()),
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if !output.HasVersionedTypes {
		t.Fatal("expected HasVersionedTypes")
	}

	var widget *TypeInfo
	for i := range output.Types {
		if output.Types[i].Name == "Widget" {
			widget = &output.Types[i]
			break
		}
	}
	if widget == nil {
		t.Fatal("Widget type not generated")
	}

	var version *FieldInfo
	for i := range widget.Fields {
		if widget.Fields[i].Name == "_version" {
			version = &widget.Fields[i]
			break
		}
	}
	if version == nil {
		t.Fatal("versioned type missing _version field")
	}
	if version.GoName != "Version" || version.GoType != "int64" || !version.Required || !version.InternalMetadata {
		t.Fatalf("unexpected _version field: %+v", *version)
	}

	outDir := t.TempDir()
	if err := WriteTypes(output, outDir); err != nil {
		t.Fatalf("write types: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(outDir, "types.go"))
	if err != nil {
		t.Fatalf("read generated types.go: %v", err)
	}
	got := string(data)
	for _, want := range []string{
		"type HistoryRecord[T any] struct",
		"Version    int64",
		"RecordedAt time.Time `json:\"recordedAt\"`",
		"Version int64 `json:\"_version\"`",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("generated types.go missing %q", want)
		}
	}
}
