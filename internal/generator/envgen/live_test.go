package envgen_test

import (
	"path/filepath"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/envgen"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// TestGoLiveAppliesRuntimeFields builds the Go env loader of a config with
// two Runtime<T> fields next to its types module and runs it (D129): Live
// holds the config LoadConfig read, Apply changes a Runtime<T> field and
// calls each subscriber, ignores a key that is not one, reads a value as
// LoadConfig does, and reports no change when nothing changed; a cancelled
// subscriber is not called. LiveFields lists the Runtime<T> fields.
func TestGoLiveAppliesRuntimeFields(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping generated-module run in -short mode")
	}
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-general"))
	if err != nil {
		t.Fatalf("load fixture-general: %v", err)
	}
	info, fifty := "info", "50"
	config := schema.Types["FixtureConfig"]
	config.Fields = append(config.Fields,
		&ir.FieldDef{Name: "LOG_LEVEL", TypeRef: ir.TypeRef{Name: "string"}, Default: &info, Runtime: true},
		&ir.FieldDef{Name: "BATCH_SIZE", TypeRef: ir.TypeRef{Name: "number"}, Default: &fifty, Runtime: true},
	)

	runGeneratedLoaderTest(t, schema, "live_test.go", func(output_ *envgen.ConfigOutput) string {
		return `package ` + output_.PackageName + `

import (
	"reflect"
	"testing"
)

func TestLive(t *testing.T) {
	t.Setenv("DATABASE_URL", "https://db.example.com")
	t.Setenv("JWT_SECRET", "secret")
	t.Setenv("LOG_LEVEL", "")
	t.Setenv("BATCH_SIZE", "")
	boot, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(LiveFields, []string{"LOG_LEVEL", "BATCH_SIZE"}) {
		t.Fatalf("LiveFields = %v", LiveFields)
	}
	live := NewLive(boot)
	if live.Current() != boot {
		t.Fatal("Current is not the boot config")
	}
	var seen []string
	cancel := live.Subscribe(func(cfg *` + output_.TypeName + `) { seen = append(seen, cfg.LogLevel) })

	changed, err := live.Apply(map[string]string{"LOG_LEVEL": "debug", "DATABASE_URL": "https://other.example.com"})
	if err != nil || !changed {
		t.Fatalf("Apply = %v, %v; want a change", changed, err)
	}
	if got := live.Current(); got.LogLevel != "debug" || got.DatabaseUrl != boot.DatabaseUrl || got.BatchSize != 50 {
		t.Fatalf("after Apply: %+v", got)
	}
	if boot.LogLevel != "info" {
		t.Fatalf("Apply changed the boot config: %q", boot.LogLevel)
	}
	if !reflect.DeepEqual(seen, []string{"debug"}) {
		t.Fatalf("subscriber saw %v", seen)
	}

	// Apply reads a value as LoadConfig does: a BATCH_SIZE that is no
	// integer takes the default, so nothing changes.
	if changed, err := live.Apply(map[string]string{"LOG_LEVEL": "debug", "BATCH_SIZE": "many"}); err != nil || changed {
		t.Fatalf("Apply of an unparsable BATCH_SIZE = %v, %v", changed, err)
	}
	if got := live.Current(); got.LogLevel != "debug" || got.BatchSize != 50 {
		t.Fatalf("after Apply: %+v", got)
	}

	if changed, err := live.Apply(map[string]string{"LOG_LEVEL": "debug"}); err != nil || changed {
		t.Fatalf("an Apply that changes nothing = %v, %v", changed, err)
	}
	cancel()
	if changed, err := live.Apply(map[string]string{"LOG_LEVEL": "warn"}); err != nil || !changed {
		t.Fatalf("Apply = %v, %v", changed, err)
	}
	if len(seen) != 1 {
		t.Fatalf("a cancelled subscriber was called: %v", seen)
	}
}
`
	})
}

// TestValuesSchemaMarksRuntimeFields: values-schema.json writes
// "runtime": true on each Runtime<T> field's entry, and no runtime key on
// the others (D129).
func TestValuesSchemaMarksRuntimeFields(t *testing.T) {
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-general"))
	if err != nil {
		t.Fatalf("load fixture-general: %v", err)
	}
	info := "info"
	config := schema.Types["FixtureConfig"]
	config.Fields = append(config.Fields, &ir.FieldDef{Name: "LOG_LEVEL", TypeRef: ir.TypeRef{Name: "string"}, Default: &info, Runtime: true})
	output, err := envgen.Generate(schema, "fixture-general")
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if !output.HasRuntime {
		t.Fatal("HasRuntime is false for a config with a Runtime<T> field")
	}
	runtime := map[string]bool{}
	for _, v := range envgen.BuildValuesSchema(output).XSuperschematic.EnvVars {
		runtime[v.Name] = v.Runtime
	}
	if !runtime["LOG_LEVEL"] || runtime["DATABASE_URL"] {
		t.Fatalf("runtime marks = %v, want LOG_LEVEL alone", runtime)
	}
}
