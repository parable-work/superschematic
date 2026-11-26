package envgen_test

import (
	"path/filepath"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/envgen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// TestOptionalZeroEnvLoaderAssignsThePointers builds the env loader next
// to its types module under go.optional_zero = "keep" and runs it. An
// optional integer scalar is a pointer with or without a default, set to
// the default when its variable is unset; an optional number without a
// default is *float64, nil when unset and set, 0 included, when it is. A
// number with a default stays a value. The loader must assign each the
// way the types module declares it, or the module does not compile.
func TestOptionalZeroEnvLoaderAssignsThePointers(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping generated-module run in -short mode")
	}
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-general"))
	if err != nil {
		t.Fatalf("load fixture-general: %v", err)
	}
	fifty, five := "50", "5"
	config := schema.Types["FixtureConfig"]
	config.Fields = append(config.Fields,
		&ir.FieldDef{Name: "MAX_RETRIES", TypeRef: ir.TypeRef{Name: "Generic.Int64"}},
		&ir.FieldDef{Name: "BATCH_SIZE", TypeRef: ir.TypeRef{Name: "Generic.Int64"}, Default: &fifty},
		&ir.FieldDef{Name: "SAMPLE_RATE", TypeRef: ir.TypeRef{Name: "number"}},
		&ir.FieldDef{Name: "JITTER", TypeRef: ir.TypeRef{Name: "number"}, Default: &five},
	)
	keep := naming.Naming{Go: naming.GoConfig{OptionalZero: naming.GoOptionalKeep}}

	runGeneratedLoaderTestWith(t, schema, keep, "optional_zero_test.go", func(output *envgen.ConfigOutput) string {
		return `package ` + output.PackageName + `

import "testing"

func load(t *testing.T, env map[string]string) *` + output.TypeName + ` {
	t.Helper()
	t.Setenv("DATABASE_URL", "https://db.example.com")
	t.Setenv("JWT_SECRET", "secret")
	for _, key := range []string{"MAX_RETRIES", "BATCH_SIZE", "SAMPLE_RATE", "JITTER"} {
		t.Setenv(key, env[key])
	}
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestOptionalZero(t *testing.T) {
	unset := load(t, nil)
	if unset.MaxRetries != nil || unset.SampleRate != nil {
		t.Fatalf("unset MAX_RETRIES %v and SAMPLE_RATE %v, want nil", unset.MaxRetries, unset.SampleRate)
	}
	if unset.BatchSize == nil || *unset.BatchSize != 50 || unset.Jitter != 5 {
		t.Fatalf("unset BATCH_SIZE %v and JITTER %v, want 50 and 5", unset.BatchSize, unset.Jitter)
	}
	zero := load(t, map[string]string{"MAX_RETRIES": "0", "BATCH_SIZE": "0", "SAMPLE_RATE": "0", "JITTER": "0"})
	if zero.MaxRetries == nil || *zero.MaxRetries != 0 || zero.BatchSize == nil || *zero.BatchSize != 0 ||
		zero.SampleRate == nil || *zero.SampleRate != 0 || zero.Jitter != 0 {
		t.Fatalf("zeros loaded as %v %v %v %v", zero.MaxRetries, zero.BatchSize, zero.SampleRate, zero.Jitter)
	}
}
`
	})
}
