package writer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/parable-work/superschematic/internal/loader"
)

// TestTSWriterAcceptsHydratedCatalogScalars pins that the TypeScript writer
// strips all the metadata the loader hydrates from the scalar catalog, so
// a schema that references catalog scalars writes to TypeScript and reloads
// to the same IR. That covers the bounds of integer scalars
// (Generic.Int64, Temporal.Seconds, Ordering.Rank) and the TypeScript,
// Python and Rust type mappings.
func TestTSWriterAcceptsHydratedCatalogScalars(t *testing.T) {
	names := []string{
		"Generic.Int64", "Temporal.Seconds", "Ordering.Rank", "Generic.JSON",
		"Generic.StringMap", "Identity.UUID", "Temporal.DateTime", "Geo.Location",
	}
	scalars := map[string]any{}
	fields := []any{}
	for i, name := range names {
		scalars[name] = map[string]any{"name": name, "languagePrimitive": "string"}
		fields = append(fields, map[string]any{
			"name":     string(rune('a' + i)),
			"typeRef":  map[string]any{"name": name},
			"required": true,
		})
	}
	dir := filepath.Join(t.TempDir(), "catalog-scalars")
	files := map[string]any{
		"schema.config.json": map[string]any{"name": "catalog-scalars", "kind": "General", "outputs": map[string]any{}},
		filepath.Join("src", "record.schema.json"): map[string]any{
			"scalars": scalars,
			"types": map[string]any{
				"Record": map[string]any{"name": "Record", "role": "EmbeddedStruct", "fields": fields},
			},
		},
	}
	for rel, doc := range files {
		data, err := json.Marshal(doc)
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
		t.Fatalf("load: %v", err)
	}
	if schema.Scalars["Generic.Int64"].Maximum == nil {
		t.Fatal("Generic.Int64 was not hydrated with its bounds")
	}

	reloaded := writeAndReload(t, schema, FormatTS)
	for _, name := range names {
		if !reflect.DeepEqual(reloaded.Scalars[name], schema.Scalars[name]) {
			t.Errorf("%s changed in the TypeScript round trip:\n got  %+v\n want %+v", name, reloaded.Scalars[name], schema.Scalars[name])
		}
	}
	if got, want := len(reloaded.Types["Record"].Fields), len(names); got != want {
		t.Fatalf("Record has %d fields after the round trip, want %d", got, want)
	}
}
