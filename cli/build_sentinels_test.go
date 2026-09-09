package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/registry"
)

// siblingRegistry returns a core registry plus a fixture kind that sets
// ImportsSiblingSentinels.
func siblingRegistry(t *testing.T) *registry.Registry {
	t.Helper()
	reg := registry.New(naming.Naming{})
	if err := reg.RegisterKind(registry.KindSpec{Name: "Grouping", Extension: "test", ImportsSiblingSentinels: true}); err != nil {
		t.Fatal(err)
	}
	return reg
}

func writeServiceDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// TestTargetImportsSiblingSentinelsReadsTheKindSpec: the pre-load sentinel
// sweep is decided by KindSpec.ImportsSiblingSentinels, read from the
// data-form config directly and from the SchemaKind member named in a
// TypeScript config, never from a kind literal. The data-form positive case
// waits on @superschematic/schema-config widening its kind enum (W10): the JSON
// Schema rejects a fixture kind before the registry sees it.
func TestTargetImportsSiblingSentinelsReadsTheKindSpec(t *testing.T) {
	reg := siblingRegistry(t)

	dataFormDB := writeServiceDir(t, map[string]string{
		"schema.config.json": `{"name": "db", "kind": "DB", "outputs": {}}`,
	})
	if targetImportsSiblingSentinels(dataFormDB, reg) {
		t.Error("data-form DB config was detected as importing sibling sentinels")
	}

	tsForm := writeServiceDir(t, map[string]string{
		"schema.config.ts": "export default defineConfig({ name: \"grp\", kind: SchemaKind.Grouping, outputs: {} });\n",
	})
	if !targetImportsSiblingSentinels(tsForm, reg) {
		t.Error("TypeScript config naming a flagged kind was not detected")
	}
	tsFormLiteral := writeServiceDir(t, map[string]string{
		"schema.config.ts": "export default defineConfig({ name: \"grp\", kind: \"Grouping\" as SchemaKind, outputs: {} });\n",
	})
	if !targetImportsSiblingSentinels(tsFormLiteral, reg) {
		t.Error("TypeScript config naming a flagged kind as a string literal was not detected")
	}
	tsFormDB := writeServiceDir(t, map[string]string{
		"schema.config.ts": "export default defineConfig({ name: \"db\", kind: SchemaKind.DB, outputs: {} });\n",
	})
	if targetImportsSiblingSentinels(tsFormDB, reg) {
		t.Error("TypeScript DB config was detected as importing sibling sentinels")
	}
	if targetImportsSiblingSentinels(t.TempDir(), reg) {
		t.Error("a directory with no config was detected")
	}
}
