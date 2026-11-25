package typegen

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// TestADurationDefaultIsATypedDuration: a Temporal.Duration is a
// time.Duration in Go, so its text default is written as a count of
// nanoseconds. The quoted text did not compile. The generated module runs
// a test that reads the defaults back from the constructor and from a
// decode of {}.
func TestADurationDefaultIsATypedDuration(t *testing.T) {
	schema := loadJSONService(t, "duration-default", map[string]any{
		"scalars": map[string]any{
			"Temporal.Duration": map[string]any{"name": "Temporal.Duration", "languagePrimitive": "string"},
		},
		"types": map[string]any{
			"Backoff": map[string]any{"name": "Backoff", "role": "EmbeddedStruct", "jsonField": true, "fields": []any{
				map[string]any{"name": "lookback", "typeRef": map[string]any{"name": "Temporal.Duration"}, "required": true, "default": "0s"},
				map[string]any{"name": "wait", "typeRef": map[string]any{"name": "Temporal.Duration"}, "required": true, "default": "1m30s"},
			}},
		},
	})
	output, err := Generate(schema, Options{
		SchemaName: "duration-default",
		ModulePath: "example.com/schemas/types/go/duration-default",
		Clock:      codegen.FixedClock(time.Unix(0, 0).UTC()),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, typeInfo := range output.Types {
		if typeInfo.Name != "Backoff" {
			continue
		}
		for _, field := range typeInfo.Fields {
			want := map[string]string{"lookback": "TemporalDuration(0)", "wait": "TemporalDuration(90000000000)"}[field.Name]
			if field.DefaultLiteral != want {
				t.Errorf("%s default literal = %q, want %q", field.Name, field.DefaultLiteral, want)
			}
		}
	}
	code := `package types

import (
	"encoding/json"
	"testing"
	"time"
)

func TestDurationDefaults(t *testing.T) {
	built := NewBackoff()
	var decoded Backoff
	if err := json.Unmarshal([]byte(` + "`{}`" + `), &decoded); err != nil {
		t.Fatal(err)
	}
	for _, got := range []*Backoff{built, &decoded} {
		if time.Duration(got.Lookback) != 0 || time.Duration(got.Wait) != 90*time.Second {
			t.Fatalf("defaults %v and %v, want 0s and 1m30s", time.Duration(got.Lookback), time.Duration(got.Wait))
		}
	}
}
`
	runGeneratedModuleTest(t, output, "duration_default", code)
}

// loadJSONService writes a General service whose one schema file is doc
// and loads it, so each scalar it names is hydrated from the registry.
func loadJSONService(t *testing.T, service string, doc map[string]any) *ir.Schema {
	t.Helper()
	dir := filepath.Join(t.TempDir(), service)
	files := map[string]any{
		"schema.config.json": map[string]any{
			"name":    service,
			"kind":    "General",
			"outputs": map[string]any{"types": map[string]any{"go": map[string]any{"enabled": true}}},
		},
		filepath.Join("src", service+".schema.json"): doc,
	}
	for rel, content := range files {
		data, err := json.MarshalIndent(content, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, rel)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, rel), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	schema, err := loader.LoadService(dir)
	if err != nil {
		t.Fatalf("load %s: %v", service, err)
	}
	return schema
}
