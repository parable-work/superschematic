package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/parable-work/superschematic/internal/generator/naming"
)

// runCoreCmd runs the core binary with args and no terminal.
func runCoreCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	buf := new(bytes.Buffer)
	root := New(Config{})
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetIn(strings.NewReader(""))
	root.SetArgs(args)
	err := root.Execute()
	return buf.String(), err
}

// repoCheckout is this checkout, which init's tests build against: the
// parent of this file's directory, whatever the working directory is.
func repoCheckout(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	return filepath.Dir(filepath.Dir(file))
}

// initTestProject runs init into a project of its own, made the working
// directory, and returns the project's directory.
func initTestProject(t *testing.T) string {
	t.Helper()
	project := t.TempDir()
	t.Chdir(project)
	out, err := runCoreCmd(t, "init", "--superschematic", repoCheckout(t), "--go-module-root", "example.com/notes", "--npm-scope", "@notes")
	require.NoError(t, err, out)
	return project
}

// TestInitWritesASchemasRoot: init writes the naming file, the base
// tsconfig, the ignore rules and an empty services directory, with paths
// relative to the files that hold them, and names the next command.
func TestInitWritesASchemasRoot(t *testing.T) {
	checkout := repoCheckout(t)
	project := t.TempDir()
	t.Chdir(project)
	out, err := runCoreCmd(t, "init", "--superschematic", checkout, "--go-module-root", "example.com/notes", "--npm-scope", "@notes")
	require.NoError(t, err, out)

	schemasRoot := filepath.Join(project, "schemas")
	assert.DirExists(t, filepath.Join(schemasRoot, "services"))
	entries, err := os.ReadDir(filepath.Join(schemasRoot, "services"))
	require.NoError(t, err)
	assert.Empty(t, entries)

	fromRoot, err := filepath.Rel(schemasRoot, checkout)
	require.NoError(t, err)
	base := readFile(t, filepath.Join(schemasRoot, "tsconfig.base.json"))
	assert.Contains(t, base, `"experimentalDecorators": true,`)
	assert.Contains(t, base, `"strict": true,`)
	assert.Contains(t, base, `"@superschematic/*": ["`+filepath.ToSlash(fromRoot)+`/packages/*/src/index.ts"],`)
	assert.Contains(t, base, `"superscalar": ["`+filepath.ToSlash(fromRoot)+`/third_party/superscalar/bindings/typescript/src/index.ts"],`)
	assert.Contains(t, base, `"@notes/*": ["./services/*/src/index.ts"]`)

	assert.Equal(t, "!/dist/\n/dist/*\n!/dist/bun.lock\n!/dist/Cargo.lock\n", lastLines(readFile(t, filepath.Join(schemasRoot, ".gitignore")), 4))

	names, err := naming.Load(schemasRoot)
	require.NoError(t, err)
	assert.Equal(t, "example.com/notes", names.GoModuleRoot)
	assert.Equal(t, "@notes", names.NpmScope)
	fromRepo, err := filepath.Rel(project, checkout)
	require.NoError(t, err)
	assert.Equal(t, filepath.ToSlash(filepath.Join(fromRepo, "runtime/schema/go")), names.Paths.SchemaRuntimeGo)
	assert.Equal(t, filepath.ToSlash(filepath.Join(fromRepo, "third_party/superscalar/go")), names.Paths.ScalarGo)
	assert.Empty(t, names.Paths.BuildContext, "the checkout does not hold the project")

	assert.Contains(t, out, "Wrote the schemas root ./schemas:\n  superschematic.toml\n  tsconfig.base.json\n  .gitignore\n  services/\n")
	assert.Contains(t, out, "Go modules under example.com/notes, npm packages under @notes")
	assert.True(t, strings.HasSuffix(out, "\nNext, add a service:\n  superschematic new service <name> --kind DB|API|General|Bucket|Site|MemoryStore\n"), out)
}

// TestInitDefaults: without a terminal, the module root and the scope come
// from the project's directory, and the checkout is the one that holds the
// schemas root; a schemas root outside any checkout needs the flag.
func TestInitDefaults(t *testing.T) {
	t.Run("outside a checkout", func(t *testing.T) {
		t.Chdir(t.TempDir())
		_, err := runCoreCmd(t, "init")
		require.EqualError(t, err, "init needs a superschematic checkout to build against while nothing is published; name one with --superschematic")
		assert.NoDirExists(t, "schemas")
	})
	t.Run("from the project", func(t *testing.T) {
		project := filepath.Join(t.TempDir(), "Notes App")
		require.NoError(t, os.MkdirAll(project, 0o755))
		t.Chdir(project)
		out, err := runCoreCmd(t, "init", "--superschematic", repoCheckout(t))
		require.NoError(t, err, out)
		names, err := naming.Load("schemas")
		require.NoError(t, err)
		assert.Equal(t, "example.com/notes-app", names.GoModuleRoot)
		assert.Equal(t, "@notes-app", names.NpmScope)
	})
	t.Run("inside the checkout", func(t *testing.T) {
		// A project inside the checkout takes it, and its server images
		// build from it.
		project, err := os.MkdirTemp(repoCheckout(t), "init-test-")
		require.NoError(t, err)
		t.Cleanup(func() { _ = os.RemoveAll(project) })
		t.Chdir(project)
		out, err := runCoreCmd(t, "init")
		require.NoError(t, err, out)
		names, err := naming.Load("schemas")
		require.NoError(t, err)
		assert.Equal(t, "..", names.Paths.BuildContext)
		assert.Equal(t, "../ir", names.Paths.SchemaIR)
		assert.Contains(t, readFile(t, filepath.Join("schemas", "tsconfig.base.json")), `"@superschematic/*": ["../../packages/*/src/index.ts"],`)
	})
}

// TestInitAsks: at a terminal, init asks for each value no flag gives,
// with the defaults, and asks again after an answer that does not hold.
func TestInitAsks(t *testing.T) {
	checkout := repoCheckout(t)
	project := filepath.Join(t.TempDir(), "notes")
	require.NoError(t, os.MkdirAll(project, 0o755))
	t.Chdir(project)
	p := &scriptedPrompter{answers: []string{
		"",       // example.com/notes
		"acme",   // refused: no @
		"@acme",  // the scope
		"",       // refused: no checkout holds the project
		project,  // refused: no checkout
		checkout, // the checkout
	}}
	withTerminal(t, p, nil)
	out, err := runCoreCmd(t, "init", "./schemas")
	require.NoError(t, err, out)
	assert.Equal(t, []string{
		"Go module root of the generated packages [example.com/notes]: ",
		"npm scope of the generated packages [@notes]: ",
		"npm scope of the generated packages [@notes]: ",
		"superschematic checkout: ",
		"superschematic checkout: ",
		"superschematic checkout: ",
	}, p.prompts)
	require.Len(t, p.said, 3)
	assert.Contains(t, p.said[0], `npm_scope "acme"`)
	assert.Contains(t, p.said[1], "name one with --superschematic")
	assert.Contains(t, p.said[2], "is no superschematic checkout ready to build against")
	names, err := naming.Load("schemas")
	require.NoError(t, err)
	assert.Equal(t, "example.com/notes", names.GoModuleRoot)
	assert.Equal(t, "@acme", names.NpmScope)
}

// TestInitRefuses: init writes no file that exists, and writes nothing
// when one does; a value that does not hold is an error.
func TestInitRefuses(t *testing.T) {
	checkout := repoCheckout(t)
	t.Run("an existing file", func(t *testing.T) {
		t.Chdir(t.TempDir())
		require.NoError(t, os.MkdirAll("schemas", 0o755))
		require.NoError(t, os.WriteFile(filepath.Join("schemas", ".gitignore"), []byte("node_modules/\n"), 0o644))
		_, err := runCoreCmd(t, "init", "--superschematic", checkout)
		require.EqualError(t, err, "init writes no file that exists, and ./schemas/.gitignore exists; move it aside or write the schemas root elsewhere")
		assert.NoFileExists(t, filepath.Join("schemas", "superschematic.toml"))
		assert.NoDirExists(t, filepath.Join("schemas", "services"))
		assert.Equal(t, "node_modules/\n", readFile(t, filepath.Join("schemas", ".gitignore")))
	})
	t.Run("a second run", func(t *testing.T) {
		initTestProject(t)
		_, err := runCoreCmd(t, "init", "--superschematic", checkout)
		require.EqualError(t, err, "init writes no file that exists, and ./schemas/superschematic.toml, ./schemas/tsconfig.base.json and ./schemas/.gitignore exist; move them aside or write the schemas root elsewhere")
	})
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"scope", []string{"--npm-scope", "acme"}, `npm_scope "acme": want @ then`},
		{"module root", []string{"--go-module-root", "example.com/acme/"}, `go_module_root "example.com/acme/": want a module path`},
		{"checkout", []string{"--superschematic", "."}, "is no superschematic checkout ready to build against: it has no packages/schema-config/src/index.ts"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			args := append([]string{"init", "--superschematic", checkout}, tc.args...)
			_, err := runCoreCmd(t, args...)
			require.ErrorContains(t, err, tc.want)
			assert.NoDirExists(t, "schemas")
		})
	}
}

// TestNewServiceEachKind: new service writes each kind's files, with an
// outputs block that loads, and builds it once so its handle exists; the
// tree init and new service wrote then builds whole with build-all.
func TestNewServiceEachKind(t *testing.T) {
	project := initTestProject(t)
	services := filepath.Join(project, "schemas", "services")
	for _, tc := range []struct {
		name, kind string
		files      []string
		config     []string
		index      string
	}{
		{
			name: "notes-db", kind: "DB",
			files:  []string{"src/notes-db.schema.ts"},
			config: []string{"kind: SchemaKind.DB,", "[TargetLanguage.Go]: { enabled: true }"},
			index:  "export { Item } from \"./notes-db.schema\";\nexport * from \"./service.generated\";\n",
		},
		{
			name: "notes-api", kind: "api",
			files:  []string{"src/notes-api.schema.ts"},
			config: []string{"kind: SchemaKind.API,", "api: { enabled: true },", "sdk: {"},
			index:  "export { ItemQueries, ItemView } from \"./notes-api.schema\";\nexport * from \"./service.generated\";\n",
		},
		{
			name: "notes-common", kind: "General",
			files:  []string{"src/notes-common.schema.ts"},
			config: []string{"kind: SchemaKind.General,", "[TargetLanguage.TypeScript]: { enabled: true }"},
			index:  "export { Item } from \"./notes-common.schema\";\nexport * from \"./service.generated\";\n",
		},
		{
			name: "notes-media", kind: "Bucket",
			config: []string{"kind: SchemaKind.Bucket,", "outputs: {}"},
			index:  "export * from \"./service.generated\";\n",
		},
		{
			name: "notes-web", kind: "Site",
			config: []string{"kind: SchemaKind.Site\n"},
			index:  "export * from \"./service.generated\";\n",
		},
		{
			name: "notes-cache", kind: "memorystore",
			files:  []string{"src/notes-cache.schema.ts"},
			config: []string{"kind: SchemaKind.MemoryStore,", "[TargetLanguage.Go]: { enabled: true }", "[TargetLanguage.TypeScript]: { enabled: true }"},
			index:  "export { ItemChanged, ItemNoticeSent } from \"./notes-cache.schema\";\nexport * from \"./service.generated\";\n",
		},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			out, err := runCoreCmd(t, "new", "service", tc.name, "--kind", tc.kind)
			require.NoError(t, err, out)
			dir := filepath.Join(services, tc.name)
			for _, f := range append([]string{"schema.config.ts", "package.json", "tsconfig.json", "src/index.ts", "src/service.generated.ts"}, tc.files...) {
				assert.FileExists(t, filepath.Join(dir, filepath.FromSlash(f)))
			}
			if len(tc.files) == 0 {
				matches, err := filepath.Glob(filepath.Join(dir, "src", "*.schema.ts"))
				require.NoError(t, err)
				assert.Empty(t, matches, "a %s declares nothing in schema files", tc.kind)
			}
			config := readFile(t, filepath.Join(dir, "schema.config.ts"))
			assert.Contains(t, config, `name: "`+tc.name+`",`)
			for _, want := range tc.config {
				assert.Contains(t, config, want)
			}
			assert.JSONEq(t, `{"name": "@notes/`+tc.name+`", "private": true}`, readFile(t, filepath.Join(dir, "package.json")))
			assert.Equal(t, newServiceTSConfig, readFile(t, filepath.Join(dir, "tsconfig.json")))
			assert.Equal(t, tc.index, readFile(t, filepath.Join(dir, "src", "index.ts")))
			assert.Contains(t, out, "Wrote the ")
			assert.Contains(t, out, "\nNext:\n")
			assert.True(t, strings.HasSuffix(out, "  superschematic build-all ./schemas/services\n"), out)
		})
	}

	// Another service imports a handle new service's first build wrote.
	apiConfig := filepath.Join(services, "notes-api", "schema.config.ts")
	config := readFile(t, apiConfig)
	config = strings.Replace(config, "import {", "import { NotesCache } from \"@notes/notes-cache\";\nimport { NotesDb } from \"@notes/notes-db\";\nimport { NotesMedia } from \"@notes/notes-media\";\nimport {", 1)
	config = strings.Replace(config, "  kind: SchemaKind.API,\n", "  kind: SchemaKind.API,\n  authDb: NotesDb,\n  buckets: [NotesMedia],\n  memoryStores: [NotesCache],\n", 1)
	require.NoError(t, os.WriteFile(apiConfig, []byte(config), 0o644))

	dist := t.TempDir()
	out, err := runCoreCmd(t, "build-all", services, "--out", dist)
	require.NoError(t, err, out)
	assert.Contains(t, out, "All 6 schema services built successfully")
	// The memory store's starter lists Go and TypeScript types, so its
	// first build writes its definitions in both.
	assert.FileExists(t, filepath.Join(dist, "types", "go", "notes-cache", "definitions.go"))
	assert.FileExists(t, filepath.Join(dist, "types", "typescript", "notes-cache", "index.ts"))
}

// TestNewServiceFindsTheRoot: in the schemas root, new service writes
// there without --root; elsewhere --root names it.
func TestNewServiceFindsTheRoot(t *testing.T) {
	project := initTestProject(t)
	t.Chdir(filepath.Join(project, "schemas"))
	out, err := runCoreCmd(t, "new", "service", "notes-common", "--kind", "General")
	require.NoError(t, err, out)
	assert.FileExists(t, filepath.Join(project, "schemas", "services", "notes-common", "src", "service.generated.ts"))

	t.Chdir(t.TempDir())
	out, err = runCoreCmd(t, "new", "service", "notes-db", "--kind", "DB", "--root", filepath.Join(project, "schemas"))
	require.NoError(t, err, out)
	assert.FileExists(t, filepath.Join(project, "schemas", "services", "notes-db", "src", "service.generated.ts"))
}

// TestNewServiceRefuses: a file that exists, a name or a kind that does
// not hold, a name another service has, and a directory with no schemas
// root.
func TestNewServiceRefuses(t *testing.T) {
	project := initTestProject(t)
	services := filepath.Join(project, "schemas", "services")

	_, err := runCoreCmd(t, "new", "service", "Notes DB", "--kind", "DB")
	require.EqualError(t, err, `service name "Notes DB": want lower-case letters and digits in words joined by hyphens, such as shop-db`)
	_, err = runCoreCmd(t, "new", "service", "notes-stack", "--kind", "Stack")
	require.EqualError(t, err, "--kind Stack: want one of DB, API, General, Bucket, Site, MemoryStore; superschematic stack init writes a Stack service over the tree's APIs")
	_, err = runCoreCmd(t, "new", "service", "notes-db")
	require.EqualError(t, err, `required flag(s) "kind" not set`)

	dir := filepath.Join(services, "notes-db")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "src"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "src", "index.ts"), []byte("export {};\n"), 0o644))
	_, err = runCoreCmd(t, "new", "service", "notes-db", "--kind", "DB")
	require.EqualError(t, err, "new service writes no file that exists, and ./schemas/services/notes-db/src/index.ts exists; name the service another way or move it aside")
	assert.NoFileExists(t, filepath.Join(dir, "schema.config.ts"))
	assert.Equal(t, "export {};\n", readFile(t, filepath.Join(dir, "src", "index.ts")))
	require.NoError(t, os.RemoveAll(dir))

	out, err := runCoreCmd(t, "new", "service", "notes-db", "--kind", "DB")
	require.NoError(t, err, out)
	require.NoError(t, os.Rename(dir, filepath.Join(services, "db")))
	_, err = runCoreCmd(t, "new", "service", "notes-db", "--kind", "DB")
	require.EqualError(t, err, "the service in ./schemas/services/db is named notes-db already")

	t.Chdir(t.TempDir())
	_, err = runCoreCmd(t, "new", "service", "notes-db", "--kind", "DB")
	require.ErrorContains(t, err, "/schemas has no tsconfig.base.json for the service's tsconfig.json to extend; superschematic init writes a schemas root, or --root names one")
}
