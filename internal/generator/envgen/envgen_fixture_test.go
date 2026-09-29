package envgen_test

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/envgen"
	"github.com/parable-work/superschematic/internal/loader"
)

// These tests load fixtures through the loader, which imports the registry,
// which imports apigen and so envgen; an in-package test would be an import
// cycle, so they run as an external test package.

var update = flag.Bool("update", false, "rewrite golden files")

const fixturesDir = "../../loader/tsreader/testdata/services"

func TestGenerateFixtureGeneral(t *testing.T) {
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-general"))
	if err != nil {
		t.Fatalf("load fixture-general: %v", err)
	}

	output, err := envgen.Generate(schema, "fixture-general")
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if output == nil {
		t.Fatal("expected env config output for fixture-general")
	}
	if output.TypeName != "FixtureConfig" {
		t.Errorf("TypeName = %q, want FixtureConfig", output.TypeName)
	}
	if len(output.Fields) != 4 {
		t.Fatalf("expected 4 env fields, got %d", len(output.Fields))
	}
}

func TestBuildValuesSchema_EnumMetadata(t *testing.T) {
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-general"))
	if err != nil {
		t.Fatalf("load fixture-general: %v", err)
	}

	output, err := envgen.Generate(schema, "fixture-general")
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	valuesSchema := envgen.BuildValuesSchema(output)
	index := -1
	for i, envVar := range valuesSchema.XSuperschematic.EnvVars {
		if envVar.Name == "ENVIRONMENT" {
			index = i
		}
	}
	if index < 0 {
		t.Fatal("missing ENVIRONMENT in values schema")
	}
	env := valuesSchema.XSuperschematic.EnvVars[index]
	if env.IRType != "FixtureEnvironment" {
		t.Fatalf("IRType = %q, want FixtureEnvironment", env.IRType)
	}
	if !env.IsEnum {
		t.Fatal("ENVIRONMENT should be marked isEnum")
	}
	if !containsString(env.EnumValues, "development") || !containsString(env.EnumValues, "production") {
		t.Fatalf("unexpected enum values: %v", env.EnumValues)
	}
	if env.Default != "development" {
		t.Fatalf("default = %q, want development", env.Default)
	}
}

// TestValuesSchemaMarksSecrets: an env var declared Secret<T> carries
// "secret": true in values-schema.json, so a deployment can check that its
// values bind it through a secret reference and deliver it from a secret
// store; other env vars leave the key out.
func TestValuesSchemaMarksSecrets(t *testing.T) {
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-general"))
	if err != nil {
		t.Fatalf("load fixture-general: %v", err)
	}
	output, err := envgen.Generate(schema, "fixture-general")
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	outDir := t.TempDir()
	if err := envgen.WriteConfigModule(output, outDir); err != nil {
		t.Fatalf("write config: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(outDir, "values-schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Extension struct {
			EnvVars []map[string]any `json:"envVars"`
		} `json:"x-superschematic"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	secrets := map[string]any{}
	for _, envVar := range document.Extension.EnvVars {
		name, _ := envVar["name"].(string)
		secrets[name] = envVar["secret"]
	}
	if len(secrets) != 4 {
		t.Fatalf("values schema lists %d env vars, want 4: %v", len(secrets), secrets)
	}
	for name, secret := range secrets {
		want := any(nil)
		if name == "JWT_SECRET" {
			want = true
		}
		if secret != want {
			t.Errorf("%s secret = %v, want %v", name, secret, want)
		}
	}
}

func TestWriteConfigGolden(t *testing.T) {
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-general"))
	if err != nil {
		t.Fatalf("load fixture-general: %v", err)
	}

	output, err := envgen.Generate(schema, "fixture-general")
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	outDir := t.TempDir()
	if err := envgen.WriteConfigModule(output, outDir); err != nil {
		t.Fatalf("write config: %v", err)
	}

	for _, name := range []string{"config.go", "go.mod", "values-schema.json"} {
		got, err := os.ReadFile(filepath.Join(outDir, name))
		if err != nil {
			t.Fatalf("read generated %s: %v", name, err)
		}

		goldenPath := filepath.Join("testdata", "golden", "fixture-general", name)
		if *update {
			if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
				t.Fatalf("mkdir golden: %v", err)
			}
			if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
				t.Fatalf("write golden %s: %v", name, err)
			}
			continue
		}

		want, err := os.ReadFile(goldenPath)
		if err != nil {
			t.Fatalf("read golden %s: %v", name, err)
		}
		if string(got) != string(want) {
			t.Errorf("%s differs from golden (run with -update to accept)", name)
		}
	}
}

// TestWriteConfigModuleRequiresIndirectModules: the standalone module
// requires the types modules its types module reaches as indirect and
// replaces each with its directory under types/go.
func TestWriteConfigModuleRequiresIndirectModules(t *testing.T) {
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-general"))
	if err != nil {
		t.Fatalf("load fixture-general: %v", err)
	}
	output, err := envgen.Generate(schema, "fixture-general")
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	output.IndirectModules = []string{"example.com/schemas/types/go/base", "example.com/schemas/types/go/common"}

	outDir := t.TempDir()
	if err := envgen.WriteConfigModule(output, outDir); err != nil {
		t.Fatalf("write config: %v", err)
	}
	gomod, err := os.ReadFile(filepath.Join(outDir, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"base", "common"} {
		module := "example.com/schemas/types/go/" + name
		for _, line := range []string{
			"\t" + module + " v0.0.0-00010101000000-000000000000 // indirect\n",
			"replace " + module + " => ../../types/go/" + name + "\n",
		} {
			if !strings.Contains(string(gomod), line) {
				t.Errorf("go.mod lacks %q:\n%s", line, gomod)
			}
		}
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
