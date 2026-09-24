package tsgen

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

// TestWriteTypesGolden generates the TypeScript types package for fixture
// services and compares every emitted file against its golden copy.
// Regenerate with:
// go test ./internal/generator/tsgen -run TestWriteTypesGolden -update
func TestWriteTypesGolden(t *testing.T) {
	fixedClock := codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))

	cases := []struct {
		service string
		deps    []string
	}{
		{service: "fixture-db"},
		{service: "fixture-api", deps: []string{"fixture-db"}},
		{service: "fixture-general"},
		{service: "fixture-nested-arrays"},
		{service: "fixture-nested-arrays-db"},
		{service: "fixture-nested-arrays-api"},
	}

	for _, tc := range cases {
		t.Run(tc.service, func(t *testing.T) {
			schema, err := loader.LoadService(filepath.Join(fixturesDir, tc.service))
			if err != nil {
				t.Fatalf("load %s: %v", tc.service, err)
			}

			deps := map[string]*ir.Schema{}
			depPackages := map[string]string{}
			for _, dep := range tc.deps {
				depSchema, err := loader.LoadService(filepath.Join(fixturesDir, dep))
				if err != nil {
					t.Fatalf("load dependency %s: %v", dep, err)
				}
				deps[dep] = depSchema
				depPackages[dep] = naming.Default().NpmTypesPackage(dep)
			}

			output, err := Generate(schema, Options{
				SchemaName:         tc.service,
				Dependencies:       deps,
				DependencyPackages: depPackages,
				Clock:              fixedClock,
			})
			if err != nil {
				t.Fatalf("generate: %v", err)
			}

			// Use a superscalar path inside the temp tree so the computed
			// relative file: spec is deterministic across machines.
			tempRoot := t.TempDir()
			outDir := filepath.Join(tempRoot, tc.service)
			paths := naming.LocalPaths{ScalarTypeScript: filepath.Join(tempRoot, "scalars", "typescript")}
			if err := SetScalarLibSpec(output, paths, outDir); err != nil {
				t.Fatalf("set superscalar spec: %v", err)
			}

			if err := WriteTypes(output, outDir); err != nil {
				t.Fatalf("write types: %v", err)
			}

			if tc.service == "fixture-general" {
				generated, err := os.ReadFile(filepath.Join(outDir, "types", "types.ts"))
				if err != nil {
					t.Fatalf("read generated types.ts: %v", err)
				}
				const accessor = `export function defaultFixtureConfig(): FixtureConfig {
  return JSON.parse(`
				if !strings.Contains(string(generated), accessor) {
					t.Fatalf("types.ts missing fresh composite default accessor:\n%s", generated)
				}
			}

			compareWithGolden(t, outDir, filepath.Join("testdata", "golden", tc.service))
		})
	}
}

func TestGenerateTypedRecordObjectHelpers(t *testing.T) {
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-maps"))
	if err != nil {
		t.Fatalf("load fixture-maps: %v", err)
	}
	output, err := Generate(schema, Options{
		SchemaName: "fixture-maps",
		Clock:      codegen.FixedClock(time.Unix(0, 0).UTC()),
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	outDir := filepath.Join(t.TempDir(), "fixture-maps")
	if err := WriteTypes(output, outDir); err != nil {
		t.Fatalf("write types: %v", err)
	}

	maskSource, err := os.ReadFile(filepath.Join(outDir, "mask", "types", "mapcontainer.ts"))
	if err != nil {
		t.Fatalf("read map mask: %v", err)
	}
	if !strings.Contains(string(maskSource), "Object.entries(value.nested).map") {
		t.Fatal("nested map masking must iterate typed values")
	}

	validatorSource, err := os.ReadFile(
		filepath.Join(outDir, "validators", "types", "mapcontainer.ts"),
	)
	if err != nil {
		t.Fatalf("read map validator: %v", err)
	}
	if !strings.Contains(string(validatorSource), "[k, parseMapValueFromJSON(v)]") {
		t.Fatal("required nested map parsing must preserve non-null values")
	}
}

// TestWriteTypesValidatesImportedEnumFields pins that a field typed with an
// enum from a dependency package is validated with that package's enum
// validator, not only checked for presence.
func TestWriteTypesValidatesImportedEnumFields(t *testing.T) {
	names := naming.Default()
	enumsPackage := names.NpmTypesPackage("fixture-enums")
	output := &ModuleOutput{
		PackageName: names.NpmTypesPackage("fixture-consumer"),
		SchemaName:  "fixture-consumer",
		Naming:      names,
		Types: []TypeInfo{
			{
				Name: "FixtureConsumer",
				Fields: []FieldInfo{
					{
						Name:     "status",
						TSName:   "status",
						Type:     "FixtureStatus",
						TSType:   "FixtureStatus",
						Required: true,
					},
				},
			},
		},
		ImportedTypes: []ImportedTypeInfo{
			{
				Name:          "FixtureStatus",
				ImportAlias:   "fixtureEnums",
				ImportPackage: enumsPackage,
				IsEnum:        true,
			},
		},
		TypeImports: []TypeImport{
			{
				Alias:          "fixtureEnums",
				Path:           enumsPackage + "/types",
				DependencyName: "fixture-enums",
			},
		},
		PackageDependencies: []PackageDependency{
			{
				Name: enumsPackage,
				Spec: "file:../fixture-enums",
			},
		},
	}

	outDir := filepath.Join(t.TempDir(), "fixture-consumer")
	if err := WriteTypes(output, outDir); err != nil {
		t.Fatalf("write types: %v", err)
	}

	generated, err := os.ReadFile(filepath.Join(outDir, "validators", "types", "fixtureconsumer.ts"))
	if err != nil {
		t.Fatalf("read generated validator: %v", err)
	}
	source := string(generated)
	if want := "from '" + enumsPackage + "/validators/enums';"; !strings.Contains(source, want) {
		t.Fatalf("validator does not import the dependency's enum validators (%s):\n%s", want, source)
	}
	if strings.Contains(source, "validateFixtureStatus } from '../enums'") {
		t.Fatalf("validator imports an imported enum from the local enums module:\n%s", source)
	}
	if !strings.Contains(source, "validateFixtureStatusRequired(value.status)") {
		t.Fatalf("validator does not validate the imported enum field:\n%s", source)
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

// TestGenerateUnionsAndDefaults exercises union type aliases and @default
// literal emission on a hand-built IR (the TS fixtures do not declare unions
// or defaults).
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
	members := output.UnionMemberTypeNames()
	if len(members) != 2 || members[0] != "CardPayment" || members[1] != "WirePayment" {
		t.Errorf("unexpected union member names: %v", members)
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
		"enabled": "true",
		"limit":   "25",
		"label":   `"pending"`,
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

	outDir := t.TempDir()
	if err := WriteTypes(output, outDir); err != nil {
		t.Fatalf("write types: %v", err)
	}
	for _, name := range []string{
		"package.json",
		"tsconfig.json",
		"index.ts",
		filepath.Join("types", "types.ts"),
		filepath.Join("types", "unions.ts"),
		filepath.Join("validators", "types", "settings.ts"),
		filepath.Join("mask", "types", "settings.ts"),
	} {
		if _, err := os.Stat(filepath.Join(outDir, name)); err != nil {
			t.Errorf("expected output file %s: %v", name, err)
		}
	}

	unionsTS, err := os.ReadFile(filepath.Join(outDir, "types", "unions.ts"))
	if err != nil {
		t.Fatalf("read unions.ts: %v", err)
	}
	if !strings.Contains(string(unionsTS), "export type Payment = CardPayment | WirePayment;") {
		t.Errorf("unions.ts missing Payment alias:\n%s", unionsTS)
	}

	typesTS, err := os.ReadFile(filepath.Join(outDir, "types", "types.ts"))
	if err != nil {
		t.Fatalf("read types.ts: %v", err)
	}
	if !strings.Contains(string(typesTS), "export const SettingsDefaults: Partial<Settings>") {
		t.Errorf("types.ts missing SettingsDefaults:\n%s", typesTS)
	}
	if !strings.Contains(string(typesTS), "export function makeSettings(") {
		t.Errorf("types.ts missing makeSettings factory:\n%s", typesTS)
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
	if version.TSName != "_version" || version.TSType != "number" || !version.Required || !version.InternalMetadata {
		t.Fatalf("unexpected _version field: %+v", *version)
	}

	outDir := t.TempDir()
	if err := WriteTypes(output, outDir); err != nil {
		t.Fatalf("write types: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(outDir, "types", "types.ts"))
	if err != nil {
		t.Fatalf("read generated types.ts: %v", err)
	}
	got := string(data)
	for _, want := range []string{
		"export interface HistoryRecord<T> {",
		"version: number;",
		"recordedAt: JSDate;",
		"_version: number;",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("generated types.ts missing %q", want)
		}
	}
}
