package tasks

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/tasks"
)

var update = flag.Bool("update", false, "rewrite testdata/plan.golden.json and testdata/tasks.golden.json")

const (
	testImage = "sha256:6a1f0c5d2b7e4f8093a1c6d5e4b3a29187f6e5d4c3b2a1908f7e6d5c4b3a2918"
	testOther = "sha256:0b1c2d3e4f5061728394a5b6c7d8e9f00b1c2d3e4f5061728394a5b6c7d8e9f0"
	commitA   = "1111111111111111111111111111111111111111"
	commitB   = "2222222222222222222222222222222222222222"
)

// testRegistry is a registry with the core's two resolvers and `manifest`,
// a resolver that reads go.work as a manifest would be read: each line is
// a path, tracked or one a build writes.
func testRegistry(t *testing.T) *registry.Registry {
	t.Helper()
	reg := registry.New(naming.Default())
	require.NoError(t, Register(reg))
	require.NoError(t, reg.RegisterTaskResolver(registry.TaskResolverSpec{
		Kind: "manifest",
		Spec: json.RawMessage(`{"type":"object","required":["file"],"additionalProperties":false,"properties":{"file":{"type":"string"}}}`),
		Resolve: func(req registry.TaskResolveRequest) (registry.TaskResolution, error) {
			var spec struct {
				File string `json:"file"`
			}
			if err := json.Unmarshal(req.Spec, &spec); err != nil {
				return registry.TaskResolution{}, err
			}
			data, err := req.Tree.Read(spec.File)
			if err != nil {
				return registry.TaskResolution{}, err
			}
			out := registry.TaskResolution{Paths: []string{spec.File}}
			for _, line := range strings.Fields(string(data)) {
				if tracked := match(req.Tree, line); len(tracked) > 0 {
					out.Paths = append(out.Paths, tracked...)
					continue
				}
				out.Generated = append(out.Generated, registry.TaskGeneratedPath{Path: line, From: spec.File})
			}
			return out, nil
		},
	}))
	return reg
}

// testFiles is a small repository: a Go module, a script, and a schemas
// root of three services, of which shop-api names shop-db and nothing
// names shop.
func testFiles() map[string]MemoryFile {
	file := func(s string) MemoryFile { return MemoryFile{Data: []byte(s)} }
	return map[string]MemoryFile{
		".golangci.yml":               file("linters: {}\n"),
		"go.mod":                      file("module example.com/shop\n"),
		"go/main.go":                  file("package main\n"),
		"go/main_test.go":             file("package main\n"),
		"scripts/run.sh":              {Data: []byte("#!/bin/sh\n"), Mode: ModeExecutable},
		"docs/readme.md":              file("# shop\n"),
		"schemas/superschematic.toml": file("npm_scope = \"@acme\"\n"),
		"schemas/tsconfig.base.json":  file(`{"paths": {"@acme/shop-db": [], "@acme/shop-api": [], "@acme/shop": []}}`),
		"schemas/services/shop-db/schema.config.ts":        file("export default defineConfig({ name: \"shop-db\" });\n"),
		"schemas/services/shop-db/package.json":            file(`{"name": "@acme/shop-db"}`),
		"schemas/services/shop-db/src/db.schema.ts":        file("export class Product {}\n"),
		"schemas/services/shop-api/schema.config.ts":       file("import { ShopDb } from \"@acme/shop-db\";\nexport default defineConfig({ name: \"shop-api\", authDb: ShopDb });\n"),
		"schemas/services/shop-api/package.json":           file(`{"name": "@acme/shop-api"}`),
		"schemas/services/shop-api/src/api.schema.ts":      file("export class Products {}\n"),
		"schemas/services/shop/schema.config.yaml":         file("name: shop\nkind: General\n"),
		"schemas/services/shop/src/shop.schema.yaml":       file("name: shop\n"),
		"schemas/services/_drafts/notes.md":                file("not a service\n"),
		"other/schemas/services/shop-api/schema.config.ts": file("export default defineConfig({ name: \"shop-api\" });\n"),
	}
}

// testFile is the task file the plan tests read: schema-build writes the
// generated tree, lint reads the Go sources, test reads them and uses what
// schema-build wrote and follows lint, and smoke is not hermetic.
func testFile() *tasks.File {
	spec := func(v any) json.RawMessage {
		data, err := tasks.Canonical(v)
		if err != nil {
			panic(err)
		}
		return data
	}
	paths := func(p ...string) tasks.Input {
		return tasks.Input{Kind: tasks.InputPath, Spec: spec(map[string]any{"paths": p})}
	}
	return &tasks.File{
		Version:  tasks.FileVersion,
		Image:    testImage,
		Platform: "linux/amd64",
		Tasks: map[string]tasks.Task{
			"schema-build": {
				Run:      []string{"superschematic", "build-all", "schemas/services"},
				Inputs:   []tasks.Input{{Kind: tasks.InputSchema, Spec: spec(map[string]any{"service": "shop-api", "root": "schemas"})}},
				Outputs:  []tasks.Output{{Name: "generated", Kind: tasks.OutputPaths, Paths: []string{"schemas/dist"}}},
				Hermetic: true,
			},
			"lint": {
				Run:      []string{"golangci-lint", "run"},
				Inputs:   []tasks.Input{paths(".golangci.yml"), paths("go")},
				Hermetic: true,
			},
			"test": {
				Run:      []string{"go", "test", "./..."},
				Inputs:   []tasks.Input{paths("go", "go.mod"), paths("scripts/run.sh")},
				Uses:     []tasks.Use{{Task: "schema-build", Output: "generated"}},
				After:    []string{"lint"},
				Hermetic: true,
				Sections: map[string]json.RawMessage{"forge": json.RawMessage(`{"tier":"pr"}`)},
			},
			"smoke": {
				Run:   []string{"scripts/run.sh"},
				After: []string{"test"},
			},
		},
		Sections: map[string]json.RawMessage{"forge": json.RawMessage(`{"tiers":["pr"]}`)},
	}
}

func planOf(t *testing.T, format string, files map[string]MemoryFile, file *tasks.File, base *tasks.File) *tasks.Plan {
	t.Helper()
	in := PlanInput{Registry: testRegistry(t), File: file, Tree: MemoryTree(format, files), Format: format, Commit: commitA}
	if base != nil {
		in.Base, in.BaseFile = commitB, base
	}
	plan, err := Plan(in)
	require.NoError(t, err)
	return plan
}

func keysOf(plan *tasks.Plan) map[string]string {
	out := map[string]string{}
	for name, task := range plan.Tasks {
		if task.Key != nil {
			out[name] = *task.Key
		}
	}
	return out
}

// changed lists the tasks whose key differs between two plans.
func changed(t *testing.T, before, after *tasks.Plan) []string {
	t.Helper()
	var names []string
	a, b := keysOf(before), keysOf(after)
	require.Len(t, b, len(a))
	for _, name := range before.Order {
		if a[name] != b[name] {
			names = append(names, name)
		}
	}
	return names
}

// TestKeyVectors holds the keys of a fixed tree and a fixed task file. A
// key is what a forge reuses work by, so a change to how a key is made
// must raise tasks.KeyVersion: the golden is rewritten only when the key
// version moved, or when no key did.
func TestKeyVectors(t *testing.T) {
	const golden = "testdata/plan.golden.json"
	for _, format := range []string{FormatSHA1, FormatSHA256} {
		plan := planOf(t, format, testFiles(), testFile(), nil)
		got, err := plan.Encode()
		require.NoError(t, err)
		// What `tasks plan` prints is what a reader decodes.
		_, err = tasks.DecodePlan(got)
		require.NoError(t, err)
		path := strings.Replace(golden, ".golden", "."+format+".golden", 1)
		want, readErr := os.ReadFile(path)
		if *update {
			if readErr == nil {
				old, err := tasks.DecodePlan(want)
				require.NoError(t, err)
				if old.KeyVersion == plan.KeyVersion {
					require.Equal(t, keysOf(old), keysOf(plan), "the keys of a fixed tree changed and tasks.KeyVersion did not: raise it, since no key made before the change may equal one made after")
				}
			}
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
			require.NoError(t, os.WriteFile(path, got, 0o644))
			continue
		}
		require.NoError(t, readErr, "run go test ./internal/tasks -update")
		require.Equal(t, string(want), string(got), "the plan of a fixed tree changed; if a key did, raise tasks.KeyVersion before go test ./internal/tasks -update")
	}
}

func TestKeysOfTwoObjectFormatsNeverMatch(t *testing.T) {
	one := planOf(t, FormatSHA1, testFiles(), testFile(), nil)
	two := planOf(t, FormatSHA256, testFiles(), testFile(), nil)
	require.Equal(t, "1:sha1", one.KeyVersion)
	require.Equal(t, "1:sha256", two.KeyVersion)
	for name, key := range keysOf(one) {
		require.NotEqual(t, key, keysOf(two)[name], name)
	}
}

// TestAChangedInputChangesTheKeysDownstreamThroughUses: an edit changes the
// key of each task that reads the file, and of each task that uses an
// output of one of those. A task that only comes after one keeps its key.
func TestAChangedInputChangesTheKeysDownstreamThroughUses(t *testing.T) {
	before := planOf(t, FormatSHA1, testFiles(), testFile(), nil)
	edit := func(path, content string) *tasks.Plan {
		files := testFiles()
		files[path] = MemoryFile{Data: []byte(content), Mode: files[path].Mode}
		return planOf(t, FormatSHA1, files, testFile(), nil)
	}
	// shop-db is reached by shop-api, the service schema-build names, and
	// test uses schema-build's output.
	require.Equal(t, []string{"schema-build", "test"}, changed(t, before, edit("schemas/services/shop-db/src/db.schema.ts", "export class Product { sku: string }\n")))
	// Only lint reads its config; test comes after lint and reads nothing
	// of it.
	require.Equal(t, []string{"lint"}, changed(t, before, edit(".golangci.yml", "linters: { enable: [govet] }\n")))
	// Both lint and test read the Go sources.
	require.Equal(t, []string{"lint", "test"}, changed(t, before, edit("go/main.go", "package main\n\nfunc main() {}\n")))
	// Nothing reads the docs, and nothing names the service shop.
	require.Empty(t, changed(t, before, edit("docs/readme.md", "# the shop\n")))
	require.Empty(t, changed(t, before, edit("schemas/services/shop/src/shop.schema.yaml", "name: shop\ntypes: {}\n")))
	// A file of the schemas root that belongs to no service is every
	// schema input's.
	require.Equal(t, []string{"schema-build", "test"}, changed(t, before, edit("schemas/tsconfig.base.json", "{}")))
}

func TestAModeChangeChangesTheKey(t *testing.T) {
	before := planOf(t, FormatSHA1, testFiles(), testFile(), nil)
	files := testFiles()
	files["scripts/run.sh"] = MemoryFile{Data: files["scripts/run.sh"].Data, Mode: ModeFile}
	require.Equal(t, []string{"test"}, changed(t, before, planOf(t, FormatSHA1, files, testFile(), nil)))
	files["scripts/run.sh"] = MemoryFile{Data: []byte("../go/main.go"), Mode: ModeSymlink}
	require.Equal(t, []string{"test"}, changed(t, before, planOf(t, FormatSHA1, files, testFile(), nil)))
}

func TestOnlyWorkFieldsEnterAKey(t *testing.T) {
	before := planOf(t, FormatSHA1, testFiles(), testFile(), nil)
	edit := func(change func(*tasks.File)) *tasks.Plan {
		file := testFile()
		change(file)
		return planOf(t, FormatSHA1, testFiles(), file, nil)
	}
	set := func(name string, change func(*tasks.Task)) func(*tasks.File) {
		return func(f *tasks.File) {
			task := f.Tasks[name]
			change(&task)
			f.Tasks[name] = task
		}
	}
	// A section and an after edge are a forge's and the order's.
	require.Empty(t, changed(t, before, edit(set("test", func(task *tasks.Task) {
		task.Sections = map[string]json.RawMessage{"forge": json.RawMessage(`{"tier":"nightly","timeoutSeconds":900}`)}
	}))))
	require.Empty(t, changed(t, before, edit(set("test", func(task *tasks.Task) { task.After = nil }))))
	require.Empty(t, changed(t, before, edit(func(f *tasks.File) { f.Sections = nil })))
	// The command, the environment, the image, the platform and a
	// service container are the work.
	require.Equal(t, []string{"lint"}, changed(t, before, edit(set("lint", func(task *tasks.Task) { task.Run = append(task.Run, "--fix") }))))
	require.Equal(t, []string{"lint"}, changed(t, before, edit(set("lint", func(task *tasks.Task) { task.Env = map[string]string{"CGO_ENABLED": "0"} }))))
	require.Equal(t, []string{"lint"}, changed(t, before, edit(set("lint", func(task *tasks.Task) { task.Image = testOther }))))
	require.Equal(t, []string{"lint"}, changed(t, before, edit(set("lint", func(task *tasks.Task) { task.Platform = "linux/arm64" }))))
	require.Equal(t, []string{"lint"}, changed(t, before, edit(set("lint", func(task *tasks.Task) {
		task.Services = []tasks.Service{{Name: "postgres", Image: "postgres@" + testOther}}
	}))))
	// The file's image is every task's that names none.
	require.Equal(t, []string{"lint", "schema-build", "test"}, changed(t, before, edit(func(f *tasks.File) { f.Image = testOther })))
	// An output's paths are what a task that uses it reads.
	require.Equal(t, []string{"schema-build", "test"}, changed(t, before, edit(set("schema-build", func(task *tasks.Task) {
		task.Outputs = []tasks.Output{{Name: "generated", Kind: tasks.OutputPaths, Paths: []string{"schemas/dist", "schemas/gen"}}}
	}))))
}

func TestATaskThatIsNotHermeticHasNoKey(t *testing.T) {
	plan := planOf(t, FormatSHA1, testFiles(), testFile(), nil)
	require.Nil(t, plan.Tasks["smoke"].Key)
	require.NotEmpty(t, plan.Tasks["smoke"].WorkHash)
	require.Equal(t, []string{"lint", "schema-build", "test", "smoke"}, plan.Order)

	// A file edited by hand may hold what the kind's verification
	// refuses: a hermetic task over one that is not.
	file := testFile()
	build := file.Tasks["schema-build"]
	build.Hermetic = false
	file.Tasks["schema-build"] = build
	_, err := Plan(PlanInput{Registry: testRegistry(t), File: file, Tree: MemoryTree(FormatSHA1, testFiles()), Format: FormatSHA1, Commit: commitA})
	require.ErrorContains(t, err, "task test is hermetic and uses output generated of task schema-build, which has no key")
}

// TestABaseKeysAnEditedTaskOnBothDefinitions: with a base, a task whose
// work fields differ from the base's is keyed on both definitions and on
// the inputs of both, and so is each task that uses it. Those keys differ
// from the keys the same tree has with no base.
func TestABaseKeysAnEditedTaskOnBothDefinitions(t *testing.T) {
	landed := planOf(t, FormatSHA1, testFiles(), testFile(), nil)

	// The same file at the base: nothing was edited, so every key is the
	// key of the landed tree.
	same := planOf(t, FormatSHA1, testFiles(), testFile(), testFile())
	require.Empty(t, changed(t, landed, same))
	require.Equal(t, commitB, same.Base)
	for _, task := range same.Tasks {
		require.Empty(t, task.BaseWorkHash)
	}

	// The base ran lint over the docs too. The change narrows it.
	base := testFile()
	lint := base.Tasks["lint"]
	lint.Inputs = append(lint.Inputs, tasks.Input{Kind: tasks.InputPath, Spec: json.RawMessage(`{"paths":["docs"]}`)})
	base.Tasks["lint"] = lint
	edited := planOf(t, FormatSHA1, testFiles(), testFile(), base)
	require.Equal(t, []string{"lint"}, changed(t, landed, edited))
	baseHash, err := base.WorkHash("lint")
	require.NoError(t, err)
	require.Equal(t, baseHash, edited.Tasks["lint"].BaseWorkHash)
	require.Equal(t, landed.Tasks["lint"].WorkHash, edited.Tasks["lint"].WorkHash)

	// The key still covers what the base's definition read: an edit to
	// the docs changes it, though the new definition reads none of them.
	files := testFiles()
	files["docs/readme.md"] = MemoryFile{Data: []byte("# the shop\n")}
	require.Equal(t, []string{"lint"}, changed(t, edited, planOf(t, FormatSHA1, files, testFile(), base)))

	// An edit to a task others use reaches them through uses.
	base = testFile()
	build := base.Tasks["schema-build"]
	build.Run = []string{"superschematic", "build-all", "schemas/services", "--no-cache"}
	base.Tasks["schema-build"] = build
	require.Equal(t, []string{"schema-build", "test"}, changed(t, landed, planOf(t, FormatSHA1, testFiles(), testFile(), base)))

	// A task the base does not hold is new, and keyed on what it is.
	base = testFile()
	delete(base.Tasks, "lint")
	test := base.Tasks["test"]
	test.After = nil
	base.Tasks["test"] = test
	require.Empty(t, changed(t, landed, planOf(t, FormatSHA1, testFiles(), testFile(), base)))

	// A path the base's definition named and the commit dropped digests
	// as nothing; it does not fail the plan.
	base = testFile()
	lint = base.Tasks["lint"]
	lint.Inputs = []tasks.Input{{Kind: tasks.InputPath, Spec: json.RawMessage(`{"paths":["removed/dir"]}`)}}
	base.Tasks["lint"] = lint
	require.Equal(t, []string{"lint"}, changed(t, landed, planOf(t, FormatSHA1, testFiles(), testFile(), base)))
}

// TestAPathAManifestNamesAndNoFileHoldsMustBeUsed: a resolver reports a
// path a manifest points at and the commit does not hold. The plan fails
// unless the task uses an output that covers it, and names the edge.
func TestAPathAManifestNamesAndNoFileHoldsMustBeUsed(t *testing.T) {
	files := testFiles()
	files["go.work"] = MemoryFile{Data: []byte("go\nschemas/dist/types/go/shop-api\n")}
	manifest := tasks.Input{Kind: "manifest", Spec: json.RawMessage(`{"file":"go.work"}`)}
	plan := func(change func(*tasks.File)) (*tasks.Plan, error) {
		file := testFile()
		change(file)
		return Plan(PlanInput{Registry: testRegistry(t), File: file, Tree: MemoryTree(FormatSHA1, files), Format: FormatSHA1, Commit: commitA})
	}
	add := func(name string) func(*tasks.File) {
		return func(f *tasks.File) {
			task := f.Tasks[name]
			task.Inputs = append(task.Inputs, manifest)
			f.Tasks[name] = task
		}
	}

	// test uses the output that covers the path.
	got, err := plan(add("test"))
	require.NoError(t, err)
	require.Equal(t, 3, got.Tasks["test"].Inputs[2].Files, "go.work and the two Go files it names")

	// lint does not, and the output that would cover it is named.
	_, err = plan(add("lint"))
	require.ErrorContains(t, err, "task lint: go.work names schemas/dist/types/go/shop-api, which no tracked file of the commit holds")
	require.ErrorContains(t, err, `Output generated of task schema-build covers it: add `+"`"+`uses: [{ task: schema-build, output: "generated" }]`+"`")

	// No output covers a path nothing declares.
	files["go.work"] = MemoryFile{Data: []byte("vendor/generated\n")}
	_, err = plan(add("test"))
	require.ErrorContains(t, err, "task test: go.work names vendor/generated, which no tracked file of the commit holds and no task's output covers")
}

func TestOrderRefusesWhatAHandEditedFileMayHold(t *testing.T) {
	edit := func(change func(*tasks.File)) error {
		file := testFile()
		change(file)
		_, err := Order(file)
		return err
	}
	require.ErrorContains(t, edit(func(f *tasks.File) {
		lint := f.Tasks["lint"]
		lint.After = []string{"smoke"}
		f.Tasks["lint"] = lint
	}), "the tasks lint, smoke, test form a cycle through uses and after")
	require.ErrorContains(t, edit(func(f *tasks.File) {
		lint := f.Tasks["lint"]
		lint.After = []string{"vet"}
		f.Tasks["lint"] = lint
	}), "task lint names task vet, which the task file does not hold")
	require.ErrorContains(t, edit(func(f *tasks.File) {
		test := f.Tasks["test"]
		test.Uses = []tasks.Use{{Task: "schema-build", Output: "sdk"}}
		f.Tasks["test"] = test
	}), "task test uses output sdk of task schema-build, which declares no such output")
}

func TestAPlanRefusesAnInputNoResolverResolves(t *testing.T) {
	file := testFile()
	lint := file.Tasks["lint"]
	lint.Inputs = []tasks.Input{{Kind: "cargo", Spec: json.RawMessage(`{"crate":"shop"}`)}}
	file.Tasks["lint"] = lint
	_, err := Plan(PlanInput{Registry: testRegistry(t), File: file, Tree: MemoryTree(FormatSHA1, testFiles()), Format: FormatSHA1, Commit: commitA})
	require.ErrorContains(t, err, `task lint: inputs[0] is of kind "cargo", which no resolver of this binary resolves (registered: manifest, path, schema)`)
}
