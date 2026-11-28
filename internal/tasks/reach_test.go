package tasks

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/tasks"
)

// commitEdit writes files into the repository at dir and commits them.
func commitEdit(t *testing.T, dir, message string, files map[string]string) string {
	t.Helper()
	for name, content := range files {
		target := filepath.Join(dir, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(target), 0o755))
		require.NoError(t, os.WriteFile(target, []byte(content), 0o644))
	}
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", message)
	return git(t, dir, "rev-parse", "HEAD")
}

// TestSchemaReach measures a history commit by commit: an edit to a
// service changes the schema inputs that reach it and no other, an edit
// to a shared file of the root changes every one, and an edit to what a
// build writes changes none.
func TestSchemaReach(t *testing.T) {
	dir := writeRepo(t, FormatSHA1, testFiles())
	service := commitEdit(t, dir, "shop-db", map[string]string{"schemas/services/shop-db/src/db.schema.ts": "export class Product {}\nexport class Order {}\n"})
	shared := commitEdit(t, dir, "tsconfig", map[string]string{"schemas/tsconfig.base.json": `{"paths": {}}`})
	written := commitEdit(t, dir, "lockfile", map[string]string{"schemas/dist/bun.lock": "{}\n"})
	commitEdit(t, dir, "docs", map[string]string{"docs/readme.md": "# the shop\n"})

	repo, err := OpenRepository(dir)
	require.NoError(t, err)
	defer func() { require.NoError(t, repo.Close()) }()

	// The walk holds the commits that changed the root, newest first, and
	// not the one that changed the docs.
	history, err := repo.Log("HEAD", "schemas", 10)
	require.NoError(t, err)
	require.Len(t, history, 4)
	require.Equal(t, written, history[0].Commit)
	require.Equal(t, shared, history[0].Parent)
	require.Empty(t, history[3].Parent, "the first commit has no parent")
	limited, err := repo.Log("HEAD", "schemas", 2)
	require.NoError(t, err)
	require.Len(t, limited, 2)

	reach, err := SchemaReach(repo, "schemas", "HEAD", 10)
	require.NoError(t, err)
	require.Equal(t, "schemas", reach.Root)
	require.Equal(t, 1, reach.Skipped, "the first commit has nothing to be measured against")
	require.Equal(t, []ReachCommit{
		// A lockfile under the output root is in no schema input's set.
		{Commit: written, Services: 3, Edited: 0, Changed: 0, Shared: false},
		// A shared file of the root is in every set.
		{Commit: shared, Services: 3, Edited: 0, Changed: 3, Shared: true},
		// shop-api names shop-db, and shop names neither.
		{Commit: service, Services: 3, Edited: 1, Changed: 2, Shared: false},
	}, reach.Commits)
	require.Equal(t, []int{1, 1, 1}, []int{reach.All(), reach.Some(), reach.None()})

	// A root the history never held measures nothing.
	none, err := SchemaReach(repo, "no/such/root", "HEAD", 10)
	require.NoError(t, err)
	require.Empty(t, none.Commits)
	_, err = SchemaReach(repo, "schemas", "no-such-ref", 10)
	require.Error(t, err)
}

// TestARepositoryKeepsTheBlobsAWalkReads: with the cache on, a blob two
// commits hold is read from git once, and gives the same bytes.
func TestARepositoryKeepsTheBlobsAWalkReads(t *testing.T) {
	dir := writeRepo(t, FormatSHA1, testFiles())
	first := git(t, dir, "rev-parse", "HEAD")
	second := commitEdit(t, dir, "docs", map[string]string{"docs/readme.md": "# the shop\n"})
	repo, err := OpenRepository(dir)
	require.NoError(t, err)
	defer func() { require.NoError(t, repo.Close()) }()
	repo.CacheBlobs()
	for _, commit := range []string{first, second} {
		tree, err := repo.Tree(commit)
		require.NoError(t, err)
		data, err := tree.Read("go.mod")
		require.NoError(t, err)
		require.Equal(t, "module example.com/shop\n", string(data))
	}
	require.Len(t, repo.blobs, 1, "one blob, read at two commits")
}

// TestSchemaResolverLeavesOutWhatABuildWrites: a schema input holds no
// file of a directory a build writes or a tool keeps, wherever it sits
// under the root, and not the copy of the dependency graph the naming
// file names. An edit to one changes no schema input's digest.
func TestSchemaResolverLeavesOutWhatABuildWrites(t *testing.T) {
	files := testFiles()
	before, err := resolve(t, files, ir.TaskSchemaInput, `{"service":"shop-api","root":"schemas"}`)
	require.NoError(t, err)

	files["schemas/superschematic.toml"] = MemoryFile{Data: []byte("npm_scope = \"@acme\"\n\n[deps]\ncopy = \"schemas/deps.json\"\n")}
	files["schemas/deps.json"] = MemoryFile{Data: []byte("{}\n")}
	files["schemas/dist/bun.lock"] = MemoryFile{Data: []byte("{}\n")}
	files["schemas/dist/Cargo.lock"] = MemoryFile{Data: []byte("version = 4\n")}
	files["schemas/node_modules/.bin/tsc"] = MemoryFile{Data: []byte("#!/bin/sh\n")}
	files["schemas/services/shop-db/node_modules/left-pad/index.js"] = MemoryFile{Data: []byte("\n")}
	files["schemas/services/shop-db/target/debug/build.rs"] = MemoryFile{Data: []byte("\n")}
	// A file named as a skipped directory is a file.
	files["schemas/services/shop-db/src/dist"] = MemoryFile{Data: []byte("a file\n")}
	after, err := resolve(t, files, ir.TaskSchemaInput, `{"service":"shop-api","root":"schemas"}`)
	require.NoError(t, err)
	require.Equal(t, sorted(append(append([]string{}, before.Paths...), "schemas/services/shop-db/src/dist")), after.Paths)

	set, err := SchemaInputSet(MemoryTree(FormatSHA1, files), SchemaRoots(MemoryTree(FormatSHA1, files))[1], "shop-api")
	require.NoError(t, err)
	require.Equal(t, []string{"schemas/services/_drafts/notes.md", "schemas/superschematic.toml", "schemas/tsconfig.base.json"}, set.Shared)
}

func sorted(list []string) []string {
	out := append([]string{}, list...)
	sort.Strings(out)
	return out
}

// TestAPlanResolvesEachInputOnce: tasks that name one input share its
// resolution, and the timings say how many inputs the tasks name, how
// many of those differ, and how many files they hold. A plan with
// timings is the plan without.
func TestAPlanResolvesEachInputOnce(t *testing.T) {
	file := testFile()
	// vet reads what lint reads, written the same way.
	file.Tasks["vet"] = tasks.Task{Run: []string{"go", "vet", "./..."}, Inputs: file.Tasks["lint"].Inputs, Hermetic: true}
	// The base holds the same file: each input is the base's too.
	calls := 0
	reg := testRegistry(t)
	require.NoError(t, reg.RegisterTaskResolver(registry.TaskResolverSpec{
		Kind: "counted",
		Spec: []byte(`{"type":"object"}`),
		Resolve: func(req registry.TaskResolveRequest) (registry.TaskResolution, error) {
			calls++
			return registry.TaskResolution{Paths: []string{"go.mod"}}, nil
		},
	}))
	for _, name := range []string{"lint", "vet", "test"} {
		task := file.Tasks[name]
		task.Inputs = append(append([]tasks.Input{}, task.Inputs...), tasks.Input{Kind: "counted", Spec: []byte(`{"unit":"a"}`)})
		file.Tasks[name] = task
	}
	timings := &Timings{}
	in := PlanInput{Registry: reg, File: file, Tree: MemoryTree(FormatSHA1, testFiles()), Format: FormatSHA1, Commit: commitA, Timings: timings}
	timed, err := Plan(in)
	require.NoError(t, err)
	require.Equal(t, 1, calls, "three tasks name one input, which is resolved once")

	counted := timings.Kinds["counted"]
	require.Equal(t, 3, counted.Inputs)
	require.Equal(t, 1, counted.Resolved)
	require.Equal(t, 1, counted.Files)
	paths := timings.Kinds[tasks.InputPath]
	// lint and vet name two inputs each, the same two; test names two
	// more.
	require.Equal(t, 6, paths.Inputs)
	require.Equal(t, 4, paths.Resolved)
	require.Equal(t, 1, timings.Kinds[tasks.InputSchema].Resolved)

	in.Timings = nil
	plain, err := Plan(in)
	require.NoError(t, err)
	require.Equal(t, plain, timed)
	require.Equal(t, 2, calls, "a second plan resolves again: nothing is kept between plans")
}
