package rustgen

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/testpaths"
)

// TestCatalogRustTypes pins the Rust aliases of scalars whose Rust type
// comes from the catalog through the loader: Generic.StringMap is the
// catalog's HashMap, Generic.JSON its serde_json::Value, and Geo.Location,
// whose catalog row declares a struct instead of naming a type, falls back
// to rustgen's own mapping. With cargo available, the crate is built.
func TestCatalogRustTypes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "catalog-rust")
	names := []string{"Generic.StringMap", "Generic.JSON", "Geo.Location", "Contact.Email"}
	scalars := map[string]any{}
	fields := []any{}
	for i, name := range names {
		scalars[name] = map[string]any{"name": name, "languagePrimitive": "string"}
		fields = append(fields, map[string]any{"name": string(rune('a' + i)), "typeRef": map[string]any{"name": name}, "required": true})
	}
	files := map[string]any{
		"schema.config.json": map[string]any{"name": "catalog-rust", "kind": "General", "outputs": map[string]any{}},
		filepath.Join("src", "record.schema.json"): map[string]any{
			"scalars": scalars,
			"types":   map[string]any{"Record": map[string]any{"name": "Record", "role": "EmbeddedStruct", "fields": fields}},
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
	output, err := Generate(schema, Options{SchemaName: "catalog-rust", Clock: codegen.FixedClock(time.Unix(0, 0).UTC())})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	// Resolve symlinks (macOS /var -> /private/var) so the relative crate
	// path in Cargo.toml resolves from the real directory.
	outDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// The Generic.JSON field decodes through the scalar crate's adapter.
	if err := SetScalarLibPath(output, testpaths.Local(t), outDir); err != nil {
		t.Fatal(err)
	}
	if err := WriteTypes(output, outDir); err != nil {
		t.Fatal(err)
	}
	scalarsRs, err := os.ReadFile(filepath.Join(outDir, "src", "scalars.rs"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"pub type GenericStringMap = std::collections::HashMap<String, String>;",
		"pub type GenericJSON = serde_json::Value;",
		"pub type GeoLocation = serde_json::Value;",
		"pub type ContactEmail = String;",
	} {
		if !strings.Contains(string(scalarsRs), want) {
			t.Fatalf("scalars.rs is missing %q:\n%s", want, scalarsRs)
		}
	}

	if testing.Short() {
		t.Skip("skipping cargo build in short mode")
	}
	cargoPath, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not available; skipping cargo build")
	}
	cmd := exec.Command(cargoPath, "build", "--quiet")
	cmd.Dir = outDir
	cmd.Env = append(os.Environ(), "CARGO_TARGET_DIR="+filepath.Join(outDir, "target"))
	if combined, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cargo build failed: %v\n%s", err, combined)
	}
}
