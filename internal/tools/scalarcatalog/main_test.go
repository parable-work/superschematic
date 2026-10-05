package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	scalars "github.com/parable-work/superscalar/go"

	"github.com/parable-work/superschematic/internal/generator/graphdesc"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// TestCatalogValueClassesAreGraphdescs holds the value class the committed
// TypeScript catalog gives each builtin scalar to the class the graph
// descriptor gives a field of it in a compiled schema: a service loaded
// through the loader, whose one type has a field of every scalar of the
// core catalog, each field's graphdesc.ValueClass is the catalog's class,
// and a scalar whose field graphdesc refuses has none in the catalog.
func TestCatalogValueClassesAreGraphdescs(t *testing.T) {
	names := make([]string, 0, len(scalars.ScalarMetadataByCanonical))
	for name := range scalars.ScalarMetadataByCanonical {
		names = append(names, name)
	}
	sort.Strings(names)
	schema := loadShelf(t, names)
	committed := committedClasses(t)

	shelf := schema.Types["Shelf"]
	for _, name := range names {
		var field *ir.FieldDef
		for _, fd := range shelf.Fields {
			if fd.TypeRef.Name == name {
				field = fd
			}
		}
		if field == nil {
			t.Fatalf("the loaded Shelf has no field of %s", name)
		}
		key := strings.ReplaceAll(name, ".", "_")
		want, err := graphdesc.ValueClass(schema, field)
		got, listed := committed[key]
		switch {
		case err != nil && listed:
			t.Errorf("%s: the catalog gives class %q, but graphdesc refuses a field of it: %v", name, got, err)
		case err == nil && !listed:
			t.Errorf("%s: the catalog gives no class, graphdesc gives a field of it %q", name, want)
		case err == nil && got != want:
			t.Errorf("%s: the catalog gives class %q, graphdesc gives a field of it %q", name, got, want)
		}
		delete(committed, key)
	}
	for key := range committed {
		t.Errorf("the catalog gives a class to %s, which the core catalog does not hold", key)
	}
}

// loadShelf loads a General service through the loader whose type Shelf has
// one optional field of each named scalar, each declared by name as a
// schema file names a catalog scalar, and returns its schema.
func loadShelf(t *testing.T, names []string) *ir.Schema {
	t.Helper()
	declared := map[string]any{}
	fields := make([]map[string]any, 0, len(names))
	for _, name := range names {
		declared[name] = map[string]any{"name": name, "languagePrimitive": "string"}
		fields = append(fields, map[string]any{
			"name":    "of" + strings.ReplaceAll(name, ".", ""),
			"typeRef": map[string]any{"name": name},
		})
	}
	document, err := json.Marshal(map[string]any{
		"scalars": declared,
		"types":   map[string]any{"Shelf": map[string]any{"name": "Shelf", "role": "EmbeddedStruct", "fields": fields}},
	})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "catalog-classes")
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	for file, text := range map[string][]byte{
		"schema.config.json":                  []byte(`{"name": "catalog-classes", "kind": "General", "outputs": {}}`),
		filepath.Join("src", "s.schema.json"): document,
	} {
		if err := os.WriteFile(filepath.Join(dir, file), text, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	schema, err := loader.LoadService(dir)
	if err != nil {
		t.Fatalf("load the catalog's scalars: %v", err)
	}
	return schema
}

// committedClasses reads BUILTIN_SCALAR_VALUE_CLASSES from the committed
// TypeScript catalog: a JSON object literal, by scalar key.
func committedClasses(t *testing.T) map[string]string {
	t.Helper()
	text, err := os.ReadFile(filepath.Join(testpaths.RepoRoot(t), filepath.FromSlash(tsPath)))
	if err != nil {
		t.Fatal(err)
	}
	const head = "export const BUILTIN_SCALAR_VALUE_CLASSES: Record<string, string> = "
	start := strings.Index(string(text), head)
	if start < 0 {
		t.Fatalf("%s declares no BUILTIN_SCALAR_VALUE_CLASSES", tsPath)
	}
	literal := string(text[start+len(head):])
	end := strings.Index(literal, "};")
	if end < 0 {
		t.Fatalf("%s: BUILTIN_SCALAR_VALUE_CLASSES does not end", tsPath)
	}
	var classes map[string]string
	if err := json.Unmarshal([]byte(literal[:end+1]), &classes); err != nil {
		t.Fatalf("%s: BUILTIN_SCALAR_VALUE_CLASSES is not a JSON object of strings: %v", tsPath, err)
	}
	return classes
}
