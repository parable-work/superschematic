package tasks

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/tasks"
)

func TestCovers(t *testing.T) {
	for _, c := range []struct {
		pattern, name string
		want          bool
	}{
		{"go.mod", "go.mod", true},
		{"go.mod", "go.sum", false},
		{"go", "go/main.go", true},
		{"go", "go/pkg/deep/file.go", true},
		{"go", "golang/main.go", false},
		{"go/*.go", "go/main.go", true},
		{"go/*.go", "go/pkg/main.go", false},
		{"go/**", "go/pkg/main.go", true},
		{"go/**/*.go", "go/main.go", true},
		{"go/**/*.go", "go/a/b/main.go", true},
		{"go/**/*.go", "go/a/b/main.rs", false},
		{"**/*.proto", "api/v1/shop.proto", true},
		{"**/testdata", "go/pkg/testdata/golden.json", true},
		{"schemas/services/*/src", "schemas/services/shop-api/src/api.schema.ts", true},
		{"schemas/services/*/src", "schemas/services/shop-api/package.json", false},
		{"docs/[a-c]*.md", "docs/build.md", true},
		{"docs/[a-c]*.md", "docs/readme.md", false},
		{"a/b/c", "a/b", false},
	} {
		require.Equal(t, c.want, Covers(c.pattern, c.name), "%s covers %s", c.pattern, c.name)
	}
}

func TestCheckPattern(t *testing.T) {
	for _, ok := range []string{"go", "go/**", "go/**/*.go", "**/*.proto", "a/b/c.txt", "docs/[a-c]*.md"} {
		require.NoError(t, CheckPattern(ok), ok)
	}
	for pattern, want := range map[string]string{
		"":          "is empty",
		"/etc":      "is absolute",
		"../other":  "leaves the repository",
		".":         "leaves the repository or names all of it",
		"a//b":      "not in its shortest form",
		"a/./b":     "not in its shortest form",
		`a\b`:       "backslash",
		"go/**.go":  "holds ** inside a segment",
		"docs/[a-c": "is no pattern",
	} {
		require.ErrorContains(t, CheckPattern(pattern), want, pattern)
	}
}

func resolve(t *testing.T, files map[string]MemoryFile, kind, spec string) (registry.TaskResolution, error) {
	t.Helper()
	resolver, ok := testRegistry(t).TaskResolver(kind)
	require.True(t, ok)
	res, err := resolver.Resolve(registry.TaskResolveRequest{Tree: MemoryTree(FormatSHA1, files), Task: "task", Spec: json.RawMessage(spec)})
	sort.Strings(res.Paths)
	return res, err
}

func TestPathResolver(t *testing.T) {
	files := testFiles()
	res, err := resolve(t, files, ir.TaskPathInput, `{"paths":["go","go.mod"]}`)
	require.NoError(t, err)
	require.Equal(t, []string{"go.mod", "go/main.go", "go/main_test.go"}, res.Paths)

	res, err = resolve(t, files, ir.TaskPathInput, `{"paths":["go"],"exclude":["**/*_test.go"]}`)
	require.NoError(t, err)
	require.Equal(t, []string{"go/main.go"}, res.Paths)

	// A glob may cover nothing: a repository with no .proto file has none
	// to read.
	res, err = resolve(t, files, ir.TaskPathInput, `{"paths":["**/*.proto"]}`)
	require.NoError(t, err)
	require.Empty(t, res.Paths)

	// A path with no wildcard that covers nothing is a slip, or a file a
	// task writes.
	_, err = resolve(t, files, ir.TaskPathInput, `{"paths":["schemas/dist"]}`)
	require.ErrorContains(t, err, `path "schemas/dist" covers no tracked file of the commit; a file a task writes is read through uses`)

	_, err = resolve(t, files, ir.TaskPathInput, `{"paths":["../outside"]}`)
	require.ErrorContains(t, err, "leaves the repository")
}

func TestSchemaResolver(t *testing.T) {
	files := testFiles()

	// shop-api names shop-db by its package; nothing names shop, whose
	// name is a part of both names and not a whole name in either.
	res, err := resolve(t, files, ir.TaskSchemaInput, `{"service":"shop-api","root":"schemas"}`)
	require.NoError(t, err)
	require.Equal(t, []string{
		"schemas/services/_drafts/notes.md",
		"schemas/services/shop-api/package.json",
		"schemas/services/shop-api/schema.config.ts",
		"schemas/services/shop-api/src/api.schema.ts",
		"schemas/services/shop-db/package.json",
		"schemas/services/shop-db/schema.config.ts",
		"schemas/services/shop-db/src/db.schema.ts",
		"schemas/tsconfig.base.json",
	}, res.Paths)
	require.Empty(t, res.Generated)
	// The naming file is in the set by what it says, and not as a file.
	require.Len(t, res.Values, 1)
	require.Equal(t, "naming schemas/superschematic.toml", res.Values[0].Name)

	// A service that names none is itself and the root's shared files.
	res, err = resolve(t, files, ir.TaskSchemaInput, `{"service":"shop-db"}`)
	require.NoError(t, err)
	require.Len(t, res.Paths, 5)

	// The data forms name a service by its directory's name.
	files["schemas/services/shop/schema.config.yaml"] = MemoryFile{Data: []byte("name: shop\nkind: General\ndependencies:\n  - name: shop-db\n    kind: DB\n")}
	res, err = resolve(t, files, ir.TaskSchemaInput, `{"service":"shop"}`)
	require.NoError(t, err)
	require.Contains(t, res.Paths, "schemas/services/shop-db/src/db.schema.ts")
	require.NotContains(t, res.Paths, "schemas/services/shop-api/src/api.schema.ts")

	// Two schemas roots hold a shop-api.
	_, err = resolve(t, files, ir.TaskSchemaInput, `{"service":"shop-api"}`)
	require.ErrorContains(t, err, `the commit holds a schema service shop-api under 2 schemas roots ("other/schemas", "schemas"); name one with root`)
	_, err = resolve(t, files, ir.TaskSchemaInput, `{"service":"shop-web"}`)
	require.ErrorContains(t, err, "the commit holds no schema service shop-web")
	_, err = resolve(t, files, ir.TaskSchemaInput, `{"service":"shop-db","root":"other/schemas"}`)
	require.ErrorContains(t, err, "the commit holds no schema service shop-db under other/schemas/services")
}

func TestSchemaResolverReadsTheNamingFilesCacheInputs(t *testing.T) {
	files := testFiles()
	files["schemas/superschematic.toml"] = MemoryFile{Data: []byte("[cache]\ninputs = [\"permissions.yaml\", \"gen/roles.json\"]\n")}
	files["permissions.yaml"] = MemoryFile{Data: []byte("roles: []\n")}
	res, err := resolve(t, files, ir.TaskSchemaInput, `{"service":"shop-db"}`)
	require.NoError(t, err)
	require.Contains(t, res.Paths, "permissions.yaml", "an entry is relative to the parent of the schemas root")
	// One the commit does not hold is a file a task writes.
	require.Equal(t, []registry.TaskGeneratedPath{{Path: "gen/roles.json", From: "schemas/superschematic.toml"}}, res.Generated)
}

func TestSchemaResolverTakesAFileItCannotReadToNameEveryService(t *testing.T) {
	files := testFiles()
	files["schemas/services/shop-db/src/fixtures.json"] = MemoryFile{Data: bytes.Repeat([]byte("x"), ManifestLimit+1)}
	res, err := resolve(t, files, ir.TaskSchemaInput, `{"service":"shop-db"}`)
	require.NoError(t, err)
	require.Contains(t, res.Paths, "schemas/services/shop/src/shop.schema.yaml")
	require.Contains(t, res.Paths, "schemas/services/shop-api/src/api.schema.ts")

	// A file with a NUL byte holds no name, whatever bytes it has.
	files = testFiles()
	files["schemas/services/shop-db/src/logo.png"] = MemoryFile{Data: []byte("\x00@acme/shop-api\x00")}
	res, err = resolve(t, files, ir.TaskSchemaInput, `{"service":"shop-db"}`)
	require.NoError(t, err)
	require.NotContains(t, res.Paths, "schemas/services/shop-api/src/api.schema.ts")
}

func TestHoldsName(t *testing.T) {
	for _, c := range []struct {
		text, name string
		want       bool
	}{
		{`import { ShopDb } from "@acme/shop-db";`, "@acme/shop-db", true},
		{`import { ShopDb } from "@acme/shop-db";`, "shop-db", true},
		{`import { ShopDb } from "@acme/shop-db-types";`, "shop-db", false},
		{`import x from "../shop/src/model";`, "shop", true},
		{`import x from "../shop-api/src/model";`, "shop", false},
		{`authDb: shop_db`, "shop", false},
		{"name: shop\n", "shop", true},
		{"shop", "shop", true},
		{"workshop", "shop", false},
	} {
		require.Equal(t, c.want, holdsName([]byte(c.text), c.name), "%q in %q", c.name, c.text)
	}
}

// git runs git in dir with an identity of its own and no configuration
// of the machine.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
		"GIT_AUTHOR_DATE=2026-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2026-01-01T00:00:00Z",
	)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %s: %s", strings.Join(args, " "), out)
	return strings.TrimSpace(string(out))
}

// writeRepo makes a repository of the format with the files committed.
func writeRepo(t *testing.T, format string, files map[string]MemoryFile) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	git(t, dir, "init", "-q", "--object-format="+format, "-b", "main")
	for name, file := range files {
		target := filepath.Join(dir, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(target), 0o755))
		switch file.Mode {
		case ModeSymlink:
			require.NoError(t, os.Symlink(string(file.Data), target))
		case ModeExecutable:
			require.NoError(t, os.WriteFile(target, file.Data, 0o755))
		default:
			require.NoError(t, os.WriteFile(target, file.Data, 0o644))
		}
	}
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "files")
	return dir
}

// TestARepositoryGivesTheTreeItsFilesMake: a commit read through git has
// the entries, the modes and the object ids a memory tree of the same
// files has, in both object formats, so a plan of one is the plan of the
// other.
func TestARepositoryGivesTheTreeItsFilesMake(t *testing.T) {
	files := testFiles()
	files["link"] = MemoryFile{Data: []byte("go/main.go"), Mode: ModeSymlink}
	for _, format := range []string{FormatSHA1, FormatSHA256} {
		t.Run(format, func(t *testing.T) {
			dir := writeRepo(t, format, files)
			repo, err := OpenRepository(filepath.Join(dir, "go"))
			require.NoError(t, err)
			defer func() { require.NoError(t, repo.Close()) }()
			require.Equal(t, format, repo.Format())
			commit, err := repo.Commit("HEAD")
			require.NoError(t, err)
			require.Equal(t, git(t, dir, "rev-parse", "HEAD"), commit)

			tree, err := repo.Tree(commit)
			require.NoError(t, err)
			require.Equal(t, MemoryTree(format, files).Entries(), tree.Entries())
			entry, ok := tree.Entry("scripts/run.sh")
			require.True(t, ok)
			require.Equal(t, ModeExecutable, entry.Mode)

			data, err := tree.Read("go.mod")
			require.NoError(t, err)
			require.Equal(t, "module example.com/shop\n", string(data))
			// A second read goes through the same process.
			data, err = tree.Read("schemas/services/shop-db/package.json")
			require.NoError(t, err)
			require.Equal(t, `{"name": "@acme/shop-db"}`, string(data))
			_, err = tree.Read("go/missing.go")
			require.ErrorContains(t, err, "go/missing.go is not a tracked file of the commit")

			whole, err := repo.File(commit, "docs/readme.md")
			require.NoError(t, err)
			require.Equal(t, "# shop\n", string(whole))
			_, err = repo.File(commit, "docs/none.md")
			require.ErrorContains(t, err, "holds no file docs/none.md")
		})
	}
}

// TestARepositoryReadsCommitsAndNothingElse: a plan is a function of a
// commit. An edit that is not committed, a file git does not track and a
// replace ref change nothing a tree gives.
func TestARepositoryReadsCommitsAndNothingElse(t *testing.T) {
	dir := writeRepo(t, FormatSHA1, testFiles())
	repo, err := OpenRepository(dir)
	require.NoError(t, err)
	defer func() { require.NoError(t, repo.Close()) }()
	commit, err := repo.Commit("HEAD")
	require.NoError(t, err)
	before, err := repo.Tree(commit)
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/edited\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "untracked.txt"), []byte("new\n"), 0o644))
	// A replace ref makes git show other bytes under an id.
	other := git(t, dir, "hash-object", "-w", "untracked.txt")
	entry, _ := before.Entry("docs/readme.md")
	git(t, dir, "replace", entry.ID, other)

	after, err := repo.Tree(commit)
	require.NoError(t, err)
	require.Equal(t, before.Entries(), after.Entries())
	data, err := after.Read("go.mod")
	require.NoError(t, err)
	require.Equal(t, "module example.com/shop\n", string(data))
	data, err = after.Read("docs/readme.md")
	require.NoError(t, err)
	require.Equal(t, "# shop\n", string(data), "an id names the bytes it was made from")

	_, err = repo.Commit("--upload-pack=x")
	require.ErrorContains(t, err, "names no commit")
	_, err = repo.Commit("no-such-branch")
	require.ErrorContains(t, err, "names no commit of the repository")
}

func TestATreeBoundsTheFilesAResolverReads(t *testing.T) {
	files := map[string]MemoryFile{
		"big.json":   {Data: bytes.Repeat([]byte("x"), ManifestLimit+1)},
		"small.json": {Data: []byte("{}")},
	}
	dir := writeRepo(t, FormatSHA1, files)
	git(t, dir, "-c", "protocol.file.allow=always", "submodule", "--quiet", "add", "-q", dir, "vendor/sub")
	git(t, dir, "commit", "-q", "-m", "submodule")
	repo, err := OpenRepository(dir)
	require.NoError(t, err)
	defer func() { require.NoError(t, repo.Close()) }()
	commit, err := repo.Commit("HEAD")
	require.NoError(t, err)
	tree, err := repo.Tree(commit)
	require.NoError(t, err)

	_, err = tree.Read("big.json")
	var tooLarge *TooLargeError
	require.ErrorAs(t, err, &tooLarge)
	require.Equal(t, int64(ManifestLimit+1), tooLarge.Size)
	// The next read still starts at a header.
	data, err := tree.Read("small.json")
	require.NoError(t, err)
	require.Equal(t, "{}", string(data))
	// The task file itself is read whole.
	whole, err := repo.File(commit, "big.json")
	require.NoError(t, err)
	require.Len(t, whole, ManifestLimit+1)

	sub, ok := tree.Entry("vendor/sub")
	require.True(t, ok)
	require.Equal(t, ModeSubmodule, sub.Mode)
	_, err = tree.Read("vendor/sub")
	require.ErrorContains(t, err, "vendor/sub is a submodule")
}

// renderSchema is a Tasks schema as a loader builds it, with its inputs,
// edges and outputs out of order.
func renderSchema(reverse bool) *ir.Schema {
	no := false
	order := func(inputs ...ir.TaskInput) []ir.TaskInput {
		if reverse {
			for i, j := 0, len(inputs)-1; i < j; i, j = i+1, j-1 {
				inputs[i], inputs[j] = inputs[j], inputs[i]
			}
		}
		return inputs
	}
	path := func(p string) ir.TaskInput {
		return ir.TaskInput{Kind: ir.TaskPathInput, Spec: map[string]any{"paths": []any{p}}}
	}
	return &ir.Schema{
		Name: "repo-tasks",
		Kind: ir.SchemaKindTasks,
		Types: map[string]*ir.TypeDef{
			"Repo": {Name: "Repo", Tasks: &ir.TasksDecl{
				Image: testImage, Platform: "linux/amd64",
				Sections: map[string]map[string]any{"forge": {"tiers": []any{"pr", "nightly"}}},
			}},
			"SchemaBuild": {Name: "SchemaBuild", Task: &ir.TaskDecl{
				Run:     []string{"superschematic", "build-all", "schemas/services"},
				Inputs:  order(ir.TaskInput{Kind: ir.TaskSchemaInput, Spec: map[string]any{"service": "shop-api", "root": "schemas"}}, path("go.mod")),
				Outputs: []ir.TaskOutput{{Name: "sdk", Kind: ir.TaskOutputImage}, {Name: "generated", Paths: []string{"schemas/gen", "schemas/dist"}}},
			}},
			"Lint": {Name: "Lint", Task: &ir.TaskDecl{Run: []string{"golangci-lint", "run"}, Inputs: order(path("go"), path(".golangci.yml"))}},
			"GoTest": {Name: "GoTest", Task: &ir.TaskDecl{
				Name:     "test",
				Run:      []string{"go", "test", "./..."},
				Inputs:   order(path("go"), path("go.mod")),
				Uses:     []ir.TaskUse{{Task: "SchemaBuild", Output: "sdk"}, {Task: "SchemaBuild", Output: "generated"}},
				After:    []string{"Lint", "SchemaBuild"},
				Services: []ir.TaskService{{Name: "valkey", Image: "valkey@" + testOther}, {Name: "postgres", Image: "postgres@" + testOther}},
				Env:      map[string]string{"CGO_ENABLED": "1"},
				Image:    testOther, Platform: "linux/arm64",
				Sections: map[string]map[string]any{"forge": {"tier": "pr", "timeoutSeconds": float64(600)}},
			}},
			"Smoke": {Name: "Smoke", Task: &ir.TaskDecl{Run: []string{"scripts/run.sh"}, After: []string{"GoTest"}, Hermetic: &no}},
		},
	}
}

// TestRenderGolden: the task file of a schema, byte for byte, and the same
// bytes whatever order the schema lists a task's inputs in. What Render
// writes is what a reader decodes.
func TestRenderGolden(t *testing.T) {
	const golden = "testdata/tasks.golden.json"
	got, err := RenderBytes(renderSchema(false))
	require.NoError(t, err)
	reversed, err := RenderBytes(renderSchema(true))
	require.NoError(t, err)
	require.Equal(t, string(got), string(reversed))
	require.True(t, bytes.HasSuffix(got, []byte("}\n")))

	file, err := tasks.Decode(got)
	require.NoError(t, err)
	require.Equal(t, []string{"lint", "schema-build", "smoke", "test"}, file.Names())
	again, err := file.Encode()
	require.NoError(t, err)
	require.Equal(t, string(got), string(again))
	require.False(t, file.Tasks["smoke"].Hermetic)
	require.Equal(t, testImage, file.ImageOf("lint"))
	require.Equal(t, testOther, file.ImageOf("test"))
	require.Equal(t, "linux/arm64", file.PlatformOf("test"))

	if *update {
		require.NoError(t, os.WriteFile(golden, got, 0o644))
		return
	}
	want, err := os.ReadFile(golden)
	require.NoError(t, err, "run go test ./internal/tasks -update")
	require.Equal(t, string(want), string(got))
}

func TestRenderOfASchemaWithNoTask(t *testing.T) {
	got, err := RenderBytes(&ir.Schema{Name: "repo-tasks", Kind: ir.SchemaKindTasks})
	require.NoError(t, err)
	require.Equal(t, "{\n  \"tasks\": {},\n  \"version\": 1\n}\n", string(got))
}
