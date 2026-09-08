package envgen_test

import (
	"flag"
	"os"
	"path/filepath"
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
	for i, envVar := range valuesSchema.XPsgen.EnvVars {
		if envVar.Name == "ENVIRONMENT" {
			index = i
		}
	}
	if index < 0 {
		t.Fatal("missing ENVIRONMENT in values schema")
	}
	env := valuesSchema.XPsgen.EnvVars[index]
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

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
