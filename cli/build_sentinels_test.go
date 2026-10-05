package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

// TestConfigImportsSentinelsScansForAnyOtherModule: a single build sweeps
// the sibling sentinels when the target's config imports anything but the
// config package, under its name or an alias.
func TestConfigImportsSentinelsScansForAnyOtherModule(t *testing.T) {
	n := naming.Default()
	n.PackageAliases = map[string]string{"@acme/schema-config": "@superschematic/schema-config"}
	configOnly := writeServiceDir(t, map[string]string{
		"schema.config.ts": `import { defineConfig, SchemaKind } from "@superschematic/schema-config";
import { service } from "@acme/schema-config";
export default defineConfig({ name: "x", kind: SchemaKind.API, authDb: service({ name: "db", kind: SchemaKind.DB }), outputs: {} });
`,
	})
	if configImportsSentinels(configOnly, n) {
		t.Error("a config importing only the config package (and its alias) was taken for one importing sentinels")
	}
	importsSentinel := writeServiceDir(t, map[string]string{
		"schema.config.ts": `import { defineConfig, SchemaKind } from "@superschematic/schema-config";
import { ShopDb } from "@acme/shop-db";
export default defineConfig({ name: "x", kind: SchemaKind.API, authDb: ShopDb, outputs: {} });
`,
	})
	if !configImportsSentinels(importsSentinel, n) {
		t.Error("a config importing a sibling's sentinel was not detected")
	}
	if configImportsSentinels(writeServiceDir(t, map[string]string{"schema.config.json": `{}`}), n) {
		t.Error("a data-form config imports nothing")
	}
}

// TestEveryBuildReadsAConfigWhoseSiblingHasNoSentinelYet: fixture-authdb-import's
// config imports FixtureDb from fixture-db. With fixture-db's sentinel
// deleted, as for a service never built, each build command writes the
// sentinel before it reads the config (D34).
func TestEveryBuildReadsAConfigWhoseSiblingHasNoSentinelYet(t *testing.T) {
	for _, args := range [][]string{
		{"build-all", "SERVICES"},
		{"build", "--with-deps", "SERVICES/fixture-authdb-import"},
		{"build", "SERVICES/fixture-authdb-import"},
	} {
		t.Run(strings.Join(args[:len(args)-1], " "), func(t *testing.T) {
			servicesRoot := prepareTSServicesRoot(t, "fixture-db", "fixture-authdb-import")
			dbSentinel := filepath.Join(servicesRoot, "fixture-db", "src", "service.generated.ts")
			require.NoError(t, os.Remove(dbSentinel))

			argv := append([]string(nil), args...)
			argv[len(argv)-1] = strings.Replace(argv[len(argv)-1], "SERVICES", servicesRoot, 1)
			buf := new(bytes.Buffer)
			root := New(Config{})
			root.SetOut(buf)
			root.SetErr(new(bytes.Buffer))
			root.SetArgs(append(argv, "--out", t.TempDir()))
			require.NoError(t, root.Execute(), buf.String())

			assert.Contains(t, buf.String(), "sentinel written for fixture-db")
			assert.FileExists(t, dbSentinel)
		})
	}
}

// TestEveryBuildRefusesTheSameConfigImport: the import rule is in the static
// read every command shares (D34), so build, build --with-deps and build-all
// refuse a config that imports a schema class beside its sentinel, with one
// message that names the import.
func TestEveryBuildRefusesTheSameConfigImport(t *testing.T) {
	const want = `schema.config.ts imports Tenant from "@schemas/fixture-db", which is a class, not a service sentinel`
	for _, args := range [][]string{
		{"build-all", "SERVICES"},
		{"build", "--with-deps", "SERVICES/fixture-authdb-import"},
		{"build", "SERVICES/fixture-authdb-import"},
	} {
		t.Run(strings.Join(args[:len(args)-1], " "), func(t *testing.T) {
			servicesRoot := prepareTSServicesRoot(t, "fixture-db", "fixture-authdb-import")
			configPath := filepath.Join(servicesRoot, "fixture-authdb-import", "schema.config.ts")
			config, err := os.ReadFile(configPath)
			require.NoError(t, err)
			refused := strings.Replace(string(config), "import { FixtureDb } from", "import { FixtureDb, Tenant } from", 1)
			require.NotEqual(t, string(config), refused)
			require.NoError(t, os.WriteFile(configPath, []byte(refused), 0o644))

			argv := append([]string(nil), args...)
			argv[len(argv)-1] = strings.Replace(argv[len(argv)-1], "SERVICES", servicesRoot, 1)
			root := New(Config{})
			root.SetOut(new(bytes.Buffer))
			root.SetErr(new(bytes.Buffer))
			root.SetArgs(append(argv, "--out", t.TempDir()))
			err = root.Execute()
			require.Error(t, err)
			assert.Contains(t, err.Error(), want)
		})
	}
}
