package envgen_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/envgen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

func numberScalarSchema(t *testing.T) *ir.Schema {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-general"))
	if err != nil {
		t.Fatalf("load fixture-general: %v", err)
	}
	schema.Scalars["Generic.Float64"] = &ir.ScalarDef{Name: "Generic.Float64"}
	if err := loader.HydrateScalars(schema, registry.CoreScalars()); err != nil {
		t.Fatal(err)
	}
	fifty, half := "50", "0.5"
	config := schema.Types["FixtureConfig"]
	config.Fields = append(config.Fields,
		&ir.FieldDef{Name: "MAX_RETRIES", TypeRef: ir.TypeRef{Name: "Generic.Int64"}},
		&ir.FieldDef{Name: "BATCH_SIZE", TypeRef: ir.TypeRef{Name: "Generic.Int64"}, Default: &fifty},
		&ir.FieldDef{Name: "SAMPLE_RATE", TypeRef: ir.TypeRef{Name: "Generic.Float64"}, Default: &half},
	)
	return schema
}

// TestEnvNumberScalarsParseWithTheTypesPackageByDefault: without
// go.env_number_scalars the loader hands a number scalar's text to the
// types package's Parse function, as before.
func TestEnvNumberScalarsParseWithTheTypesPackageByDefault(t *testing.T) {
	output, err := envgen.Generate(numberScalarSchema(t), "fixture-general")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := envgen.WriteConfigModule(output, dir); err != nil {
		t.Fatal(err)
	}
	config, err := os.ReadFile(filepath.Join(dir, "config.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"types.ParseGenericInt64(val)", `types.ParseGenericInt64("50")`, "types.ParseGenericFloat64(val)"} {
		if !strings.Contains(string(config), want) {
			t.Errorf("config.go lacks %s", want)
		}
	}
	if strings.Contains(string(config), "parseIntScalar") || strings.Contains(string(config), "parseFloatScalar") {
		t.Error("config.go declares the strconv parsers with the option off")
	}
}

// TestEnvNumberScalarsParseWithStrconv builds the env loader under
// go.env_number_scalars = "strconv" next to its types module and runs it.
// The loader reads each number scalar with strconv after trimming the
// text: an unset variable takes the default, a set one its value, and a
// text that is not a number, or a float that is not finite, fails the
// load with the variable's name. Validate still checks the scalar's
// rules.
func TestEnvNumberScalarsParseWithStrconv(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping generated-module run in -short mode")
	}
	strconvNumbers := naming.Naming{Go: naming.GoConfig{EnvNumberScalars: naming.GoEnvStrconv}}
	runGeneratedLoaderTestWith(t, numberScalarSchema(t), strconvNumbers, "number_scalars_test.go", func(output *envgen.ConfigOutput) string {
		return `package ` + output.PackageName + `

import (
	"os"
	"strings"
	"testing"
)

func load(t *testing.T, env map[string]string) (*` + output.TypeName + `, error) {
	t.Helper()
	t.Setenv("DATABASE_URL", "https://db.example.com")
	t.Setenv("JWT_SECRET", "secret")
	for _, key := range []string{"MAX_RETRIES", "BATCH_SIZE", "SAMPLE_RATE"} {
		t.Setenv(key, env[key])
	}
	return LoadConfig()
}

func TestNumberScalars(t *testing.T) {
	source, err := os.ReadFile("config.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(source), "types.ParseGeneric") {
		t.Fatal("config.go still calls the types package's Parse for a number scalar")
	}
	unset, err := load(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	if unset.MaxRetries != nil || unset.BatchSize != 50 || unset.SampleRate == nil || *unset.SampleRate != 0.5 {
		t.Fatalf("unset = %v %v %v, want nil, 50 and 0.5", unset.MaxRetries, unset.BatchSize, unset.SampleRate)
	}
	set, err := load(t, map[string]string{"MAX_RETRIES": " 3 ", "BATCH_SIZE": "7", "SAMPLE_RATE": "0.25"})
	if err != nil {
		t.Fatal(err)
	}
	if set.MaxRetries == nil || *set.MaxRetries != 3 || set.BatchSize != 7 || set.SampleRate == nil || *set.SampleRate != 0.25 {
		t.Fatalf("set = %v %v %v", set.MaxRetries, set.BatchSize, set.SampleRate)
	}
	for env, want := range map[string]string{
		"MAX_RETRIES=abc": "environment variable MAX_RETRIES: not a valid integer",
		"BATCH_SIZE=1.5":  "environment variable BATCH_SIZE: not a valid integer",
		"SAMPLE_RATE=NaN": "environment variable SAMPLE_RATE: not a finite number",
		"SAMPLE_RATE=x":   "environment variable SAMPLE_RATE: not a valid number",
	} {
		key, value, _ := strings.Cut(env, "=")
		if _, err := load(t, map[string]string{key: value}); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want it to hold %q", env, err, want)
		}
	}
}
`
	})
}
