package envgen_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/envgen"
	"github.com/parable-work/superschematic/internal/generator/typegen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// TestOptionalBoolEnvVarStaysNilWhenUnset builds the env loader next to its
// types module and runs it: an optional boolean without a default is *bool,
// nil when its variable is unset and set, false included, when it is. One
// with a default keeps a plain bool.
func TestOptionalBoolEnvVarStaysNilWhenUnset(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping generated-module run in -short mode")
	}
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-general"))
	if err != nil {
		t.Fatalf("load fixture-general: %v", err)
	}
	yes, no := "true", "false"
	config := schema.Types["FixtureConfig"]
	config.Fields = append(config.Fields,
		&ir.FieldDef{Name: "FEATURE_ON", TypeRef: ir.TypeRef{Name: "boolean"}},
		&ir.FieldDef{Name: "FEATURE_DEFAULT_ON", TypeRef: ir.TypeRef{Name: "boolean"}, Default: &yes},
		&ir.FieldDef{Name: "FEATURE_DEFAULT_OFF", TypeRef: ir.TypeRef{Name: "boolean"}, Default: &no},
	)

	output, err := envgen.Generate(schema, "fixture-general")
	if err != nil {
		t.Fatalf("generate env config: %v", err)
	}
	types, err := typegen.Generate(schema, typegen.Options{
		SchemaName: "fixture-general",
		ModulePath: output.TypesModule,
		Clock:      codegen.FixedClock(time.Unix(0, 0).UTC()),
	})
	if err != nil {
		t.Fatalf("generate types: %v", err)
	}

	paths := testpaths.Local(t)
	root := t.TempDir()
	typesDir := filepath.Join(root, "types", "go", "fixture-general")
	configDir := filepath.Join(root, "api", "fixture-general")
	if err := typegen.SetReplacePaths(types, paths, typesDir); err != nil {
		t.Fatal(err)
	}
	if err := typegen.WriteTypes(types, typesDir); err != nil {
		t.Fatal(err)
	}
	if err := envgen.SetReplacePaths(output, paths, configDir); err != nil {
		t.Fatal(err)
	}
	if err := envgen.WriteConfigModule(output, configDir); err != nil {
		t.Fatal(err)
	}

	code := `package ` + output.PackageName + `

import "testing"

func load(t *testing.T, env map[string]string) *` + output.TypeName + ` {
	t.Helper()
	t.Setenv("DATABASE_URL", "https://db.example.com")
	t.Setenv("JWT_SECRET", "secret")
	for _, key := range []string{"FEATURE_ON", "FEATURE_DEFAULT_ON", "FEATURE_DEFAULT_OFF"} {
		t.Setenv(key, env[key])
	}
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestOptionalBool(t *testing.T) {
	unset := load(t, nil)
	if unset.FeatureOn != nil {
		t.Fatalf("unset FEATURE_ON = %v, want nil", *unset.FeatureOn)
	}
	if !unset.FeatureDefaultOn || unset.FeatureDefaultOff {
		t.Fatalf("defaults = %t, %t, want true, false", unset.FeatureDefaultOn, unset.FeatureDefaultOff)
	}
	off := load(t, map[string]string{"FEATURE_ON": "false", "FEATURE_DEFAULT_ON": "false"})
	if off.FeatureOn == nil || *off.FeatureOn {
		t.Fatalf("FEATURE_ON=false loaded as %v, want false", off.FeatureOn)
	}
	if off.FeatureDefaultOn {
		t.Fatal("FEATURE_DEFAULT_ON=false loaded as true")
	}
	on := load(t, map[string]string{"FEATURE_ON": "true"})
	if on.FeatureOn == nil || !*on.FeatureOn {
		t.Fatalf("FEATURE_ON=true loaded as %v, want true", on.FeatureOn)
	}
}
`
	if err := os.WriteFile(filepath.Join(configDir, "optional_bool_test.go"), []byte(code), 0o644); err != nil {
		t.Fatal(err)
	}
	tidy := exec.Command("go", "mod", "tidy")
	tidy.Dir = configDir
	if out, err := tidy.CombinedOutput(); err != nil {
		t.Skipf("go mod tidy failed (likely offline): %v\n%s", err, out)
	}
	run := exec.Command("go", "test", "-count=1", ".")
	run.Dir = configDir
	if out, err := run.CombinedOutput(); err != nil {
		t.Fatalf("generated env loader test failed: %v\n%s", err, out)
	}
}
