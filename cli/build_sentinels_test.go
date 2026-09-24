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
// sweep is decided by KindSpec.ImportsSiblingSentinels, read from the kind a
// data-form config states and from the kind a TypeScript config names, as a
// SchemaKind member or a string. The data-form config schema admits any kind
// name, so an extension kind reaches the registry from both forms.
func TestTargetImportsSiblingSentinelsReadsTheKindSpec(t *testing.T) {
	reg := siblingRegistry(t)

	dataFormDB := writeServiceDir(t, map[string]string{
		"schema.config.json": `{"name": "db", "kind": "DB", "outputs": {}}`,
	})
	if targetImportsSiblingSentinels(dataFormDB, reg) {
		t.Error("data-form DB config was detected as importing sibling sentinels")
	}
	dataFormFlagged := writeServiceDir(t, map[string]string{
		"schema.config.json": `{"name": "grp", "kind": "Grouping", "outputs": {}}`,
	})
	if !targetImportsSiblingSentinels(dataFormFlagged, reg) {
		t.Error("data-form config naming a flagged kind was not detected")
	}
	yamlFormFlagged := writeServiceDir(t, map[string]string{
		"schema.config.yaml": "name: grp\nkind: Grouping\noutputs: {}\n",
	})
	if !targetImportsSiblingSentinels(yamlFormFlagged, reg) {
		t.Error("YAML config naming a flagged kind was not detected")
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
