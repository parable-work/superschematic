package envgen_test

import (
	"path/filepath"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/envgen"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// TestOptionalIntegerEnvVarKeepsExplicitZero builds the env loader next to
// its types module and runs it: an optional integer scalar without a default
// is a pointer, nil when its variable is unset and set, 0 included, when it
// is. One with a default is a value, so the loader must assign each the way
// the types module declares it, or the module does not compile.
func TestOptionalIntegerEnvVarKeepsExplicitZero(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping generated-module run in -short mode")
	}
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-general"))
	if err != nil {
		t.Fatalf("load fixture-general: %v", err)
	}
	fifty := "50"
	config := schema.Types["FixtureConfig"]
	config.Fields = append(config.Fields,
		&ir.FieldDef{Name: "MAX_RETRIES", TypeRef: ir.TypeRef{Name: "Generic.Int64"}},
		&ir.FieldDef{Name: "BATCH_SIZE", TypeRef: ir.TypeRef{Name: "Generic.Int64"}, Default: &fifty},
	)

	runGeneratedLoaderTest(t, schema, "optional_integer_test.go", func(output *envgen.ConfigOutput) string {
		return `package ` + output.PackageName + `

import "testing"

func load(t *testing.T, env map[string]string) *` + output.TypeName + ` {
	t.Helper()
	t.Setenv("DATABASE_URL", "https://db.example.com")
	t.Setenv("JWT_SECRET", "secret")
	for _, key := range []string{"MAX_RETRIES", "BATCH_SIZE"} {
		t.Setenv(key, env[key])
	}
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestOptionalInteger(t *testing.T) {
	unset := load(t, nil)
	if unset.MaxRetries != nil {
		t.Fatalf("unset MAX_RETRIES = %v, want nil", *unset.MaxRetries)
	}
	if unset.BatchSize != 50 {
		t.Fatalf("unset BATCH_SIZE = %v, want 50", unset.BatchSize)
	}
	zero := load(t, map[string]string{"MAX_RETRIES": "0", "BATCH_SIZE": "7"})
	if zero.MaxRetries == nil || *zero.MaxRetries != 0 {
		t.Fatalf("MAX_RETRIES=0 loaded as %v, want 0", zero.MaxRetries)
	}
	if zero.BatchSize != 7 {
		t.Fatalf("BATCH_SIZE=7 loaded as %v", zero.BatchSize)
	}
	three := load(t, map[string]string{"MAX_RETRIES": "3"})
	if three.MaxRetries == nil || *three.MaxRetries != 3 {
		t.Fatalf("MAX_RETRIES=3 loaded as %v, want 3", three.MaxRetries)
	}
}
`
	})
}
