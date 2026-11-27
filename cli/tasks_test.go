package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/parable-work/superschematic/tasks"
)

const tasksTestImage = "sha256:6a1f0c5d2b7e4f8093a1c6d5e4b3a29187f6e5d4c3b2a1908f7e6d5c4b3a2918"

// The Tasks fixture, in the data forms: repo-tasks declares three tasks
// over a repository with a Go file and two Bucket services. The naming
// file registers a section, forge, as data, by the JSON Schemas of its
// two parts.
var tasksFixture = map[string]string{
	"go/main.go":     "package main\n",
	"docs/readme.md": "# shop\n",
	"schemas/superschematic.toml": `[tasks.sections.forge]
file = "forge/file.schema.json"
task = "forge/task.schema.json"
`,
	"schemas/forge/file.schema.json":                 `{"type":"object","required":["tiers"],"additionalProperties":false,"properties":{"tiers":{"type":"array","items":{"type":"string"}}}}`,
	"schemas/forge/task.schema.json":                 `{"type":"object","required":["tier"],"additionalProperties":false,"properties":{"tier":{"type":"string"},"timeoutSeconds":{"type":"integer"}}}`,
	"schemas/services/shop-media/schema.config.yaml": "name: shop-media\nkind: Bucket\noutputs: {}\n",
	"schemas/services/shop-extra/schema.config.yaml": "name: shop-extra\nkind: Bucket\noutputs: {}\n",
	"schemas/services/repo-tasks/schema.config.yaml": `name: repo-tasks
kind: Tasks
outputs:
  tasks:
    install: ci/tasks.json
`,
	"schemas/services/repo-tasks/src/tasks.schema.yaml": `name: repo-tasks
kind: Tasks
types:
  Repo:
    name: Repo
    role: EmbeddedStruct
    tasks:
      image: ` + tasksTestImage + `
      platform: linux/amd64
      sections:
        forge:
          tiers: [pr, nightly]
  SchemaBuild:
    name: SchemaBuild
    role: EmbeddedStruct
    task:
      run: [superschematic, build-all, schemas/services]
      inputs:
        - kind: schema
          spec: { service: shop-media }
      outputs:
        - name: generated
          paths: [schemas/dist]
      sections:
        forge: { tier: pr }
  Lint:
    name: Lint
    role: EmbeddedStruct
    task:
      run: [golangci-lint, run]
      inputs:
        - kind: path
          spec: { paths: [go] }
      sections:
        forge: { tier: pr, timeoutSeconds: 120 }
  GoTest:
    name: GoTest
    role: EmbeddedStruct
    task:
      name: test
      run: [go, test, ./...]
      inputs:
        - kind: path
          spec: { paths: [go] }
      uses:
        - task: SchemaBuild
          output: generated
      after: [Lint]
      sections:
        forge: { tier: nightly }
`,
}

// tasksGit runs git in dir with an identity of its own and none of the
// machine's configuration.
func tasksGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
	)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %s: %s", strings.Join(args, " "), out)
	return strings.TrimSpace(string(out))
}

// prepareTasksRepo writes the Tasks fixture into a git repository of its
// own, with the directory the task file installs into, and returns the
// repository root.
func prepareTasksRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	writeTasksFiles(t, root, tasksFixture)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "ci"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".gitignore"), []byte("dist/\n*.generated.ts\n"), 0o644))
	tasksGit(t, root, "init", "-q", "-b", "main")
	return root
}

func writeTasksFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		target := filepath.Join(root, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(target), 0o755))
		require.NoError(t, os.WriteFile(target, []byte(content), 0o644))
	}
}

// runTasks runs the core CLI and returns what it wrote and its error.
func runTasks(t *testing.T, args ...string) (string, error) {
	t.Helper()
	buf := new(bytes.Buffer)
	root := New(Config{})
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetArgs(args)
	err := root.Execute()
	return buf.String(), err
}

func commitAll(t *testing.T, root, message string) string {
	t.Helper()
	tasksGit(t, root, "add", "-A")
	tasksGit(t, root, "commit", "-q", "-m", message)
	return tasksGit(t, root, "rev-parse", "HEAD")
}

func editFile(t *testing.T, path, old, replacement string) {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(data), old)
	require.NoError(t, os.WriteFile(path, []byte(strings.Replace(string(data), old, replacement, 1)), 0o644))
}

// TestTasksWritesAndChecksTheFile: `tasks` installs the file where the
// config says, `--check` holds the committed file to the schema and names
// the first line that differs, and writes nothing.
func TestTasksWritesAndChecksTheFile(t *testing.T) {
	root := prepareTasksRepo(t)
	service := filepath.Join(root, "schemas", "services", "repo-tasks")
	installed := filepath.Join(root, "ci", "tasks.json")

	_, err := runTasks(t, "tasks", service, "--check")
	require.ErrorContains(t, err, "tasks --check: ci/tasks.json is missing")
	require.NoFileExists(t, installed)

	out, err := runTasks(t, "tasks", service)
	require.NoError(t, err, out)
	require.Contains(t, out, "repo-tasks: "+installed+" written")
	data, err := os.ReadFile(installed)
	require.NoError(t, err)
	file, err := tasks.Decode(data)
	require.NoError(t, err)
	require.Equal(t, []string{"lint", "schema-build", "test"}, file.Names())
	require.Equal(t, []tasks.Use{{Task: "schema-build", Output: "generated"}}, file.Tasks["test"].Uses)
	require.Equal(t, []string{"lint"}, file.Tasks["test"].After)
	require.JSONEq(t, `{"tier":"pr","timeoutSeconds":120}`, string(file.Tasks["lint"].Sections["forge"]))
	require.JSONEq(t, `{"tiers":["pr","nightly"]}`, string(file.Sections["forge"]))

	out, err = runTasks(t, "tasks", service)
	require.NoError(t, err, out)
	require.Contains(t, out, "is current")
	out, err = runTasks(t, "tasks", service, "--check")
	require.NoError(t, err, out)
	require.Contains(t, out, "repo-tasks: ci/tasks.json is current")

	// The schema moves and the committed file does not.
	editFile(t, filepath.Join(service, "src", "tasks.schema.yaml"), "run: [golangci-lint, run]", "run: [golangci-lint, run, --fix]")
	_, err = runTasks(t, "tasks", service, "--check")
	require.ErrorContains(t, err, "tasks --check: ci/tasks.json is not what repo-tasks gives")
	require.ErrorContains(t, err, `line 27: committed "\"run\"", schema "\"run\","`)
	after, err := os.ReadFile(installed)
	require.NoError(t, err)
	require.Equal(t, string(data), string(after), "--check writes nothing")

	// A service of another kind has no task file.
	_, err = runTasks(t, "tasks", filepath.Join(root, "schemas", "services", "shop-media"))
	require.ErrorContains(t, err, "shop-media is of kind Bucket; a Tasks service declares tasks")
}

// TestTheTaskFileIsTheSameFromTwoCheckouts: the render reads the schema
// and nothing of the machine, so two checkouts at two paths write the
// same bytes.
func TestTheTaskFileIsTheSameFromTwoCheckouts(t *testing.T) {
	var files [][]byte
	for range 2 {
		root := prepareTasksRepo(t)
		out, err := runTasks(t, "tasks", filepath.Join(root, "schemas", "services", "repo-tasks"))
		require.NoError(t, err, out)
		data, err := os.ReadFile(filepath.Join(root, "ci", "tasks.json"))
		require.NoError(t, err)
		require.NotContains(t, string(data), root)
		files = append(files, data)
	}
	require.Equal(t, string(files[0]), string(files[1]))
}

// TestBuildAllInstallsTheTaskFileOnEveryBuild: the install is a build-all
// hook's, so a build that restores the service from the cache, or finds
// it up to date, installs the file as a build that generated it does.
func TestBuildAllInstallsTheTaskFileOnEveryBuild(t *testing.T) {
	root := prepareTasksRepo(t)
	services := filepath.Join(root, "schemas", "services")
	installed := filepath.Join(root, "ci", "tasks.json")
	cache := t.TempDir()

	out, err := runTasks(t, "build-all", services, "--cache", "--cache-root", cache)
	require.NoError(t, err, out)
	first, err := os.ReadFile(installed)
	require.NoError(t, err, out)
	generated, err := os.ReadFile(filepath.Join(root, "schemas", "dist", "tasks", "repo-tasks", tasks.FileName))
	require.NoError(t, err)
	require.Equal(t, string(generated), string(first))

	// Up to date: nothing is generated, and the file is installed.
	require.NoError(t, os.Remove(installed))
	out, err = runTasks(t, "build-all", services, "--cache", "--cache-root", cache)
	require.NoError(t, err, out)
	again, err := os.ReadFile(installed)
	require.NoError(t, err, out)
	require.Equal(t, string(first), string(again))

	// Restored from the cache: the output root is gone, and no generator
	// runs.
	require.NoError(t, os.Remove(installed))
	require.NoError(t, os.RemoveAll(filepath.Join(root, "schemas", "dist")))
	out, err = runTasks(t, "build-all", services, "--cache", "--cache-root", cache)
	require.NoError(t, err, out)
	require.Contains(t, out, "restored from cache")
	again, err = os.ReadFile(installed)
	require.NoError(t, err, out)
	require.Equal(t, string(first), string(again))

	// A path whose directory does not exist fails the build, and names
	// it.
	editFile(t, filepath.Join(services, "repo-tasks", "schema.config.yaml"), "install: ci/tasks.json", "install: ops/tasks.json")
	out, err = runTasks(t, "build-all", services, "--cache", "--cache-root", cache)
	require.Error(t, err, out)
	require.Contains(t, err.Error()+out, "does not exist; create it, or correct outputs.tasks.install")
}

// TestTasksPlanKeysACommit: `tasks plan` reads a commit and prints each
// task's key. An edit changes the key of each task that reads the file
// and of each that uses one of those, and leaves the others'. A dirty
// checkout changes nothing.
func TestTasksPlanKeysACommit(t *testing.T) {
	root := prepareTasksRepo(t)
	service := filepath.Join(root, "schemas", "services", "repo-tasks")
	out, err := runTasks(t, "tasks", service)
	require.NoError(t, err, out)
	first := commitAll(t, root, "tasks")

	plan := func(args ...string) *tasks.Plan {
		t.Helper()
		out, err := runTasks(t, append([]string{"tasks", "plan", "--repo", root, "--file", "ci/tasks.json"}, args...)...)
		require.NoError(t, err, out)
		p, err := tasks.DecodePlan([]byte(out))
		require.NoError(t, err, out)
		return p
	}
	before := plan("HEAD")
	require.Equal(t, first, before.Commit)
	require.Equal(t, "1:sha1", before.KeyVersion)
	require.Equal(t, []string{"lint", "schema-build", "test"}, before.Order)
	for name, task := range before.Tasks {
		require.NotNil(t, task.Key, name)
	}
	// The schema input holds shop-media's directory and the root's shared
	// files, and not shop-extra, which shop-media does not name.
	require.Equal(t, 4, before.Tasks["schema-build"].Inputs[0].Files)

	// An edit that is not committed is not the commit's.
	require.NoError(t, os.WriteFile(filepath.Join(root, "go", "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644))
	require.Equal(t, before.Tasks, plan(first).Tasks)

	second := commitAll(t, root, "go")
	after := plan(second)
	require.NotEqual(t, *before.Tasks["lint"].Key, *after.Tasks["lint"].Key)
	require.NotEqual(t, *before.Tasks["test"].Key, *after.Tasks["test"].Key)
	require.Equal(t, *before.Tasks["schema-build"].Key, *after.Tasks["schema-build"].Key)

	// A service the schema input does not reach changes no key.
	require.NoError(t, os.WriteFile(filepath.Join(root, "schemas", "services", "shop-extra", "notes.md"), []byte("notes\n"), 0o644))
	third := commitAll(t, root, "extra")
	require.Equal(t, after.Tasks, plan(third).Tasks)

	// With a base that holds the same file, the keys are the commit's own.
	based := plan(third, "--base", first)
	require.Equal(t, first, based.Base)
	require.Equal(t, after.Tasks, based.Tasks)

	// A change that edits a task is keyed on both definitions.
	editFile(t, filepath.Join(service, "src", "tasks.schema.yaml"), "run: [golangci-lint, run]", "run: [golangci-lint, run, --fast]")
	out, err = runTasks(t, "tasks", service)
	require.NoError(t, err, out)
	fourth := commitAll(t, root, "lint")
	landed, proposed := plan(fourth), plan(fourth, "--base", third)
	require.Equal(t, after.Tasks["lint"].WorkHash, proposed.Tasks["lint"].BaseWorkHash)
	require.NotEqual(t, *landed.Tasks["lint"].Key, *proposed.Tasks["lint"].Key)
	require.Equal(t, *landed.Tasks["test"].Key, *proposed.Tasks["test"].Key, "test comes after lint and uses nothing of it")

	_, err = runTasks(t, "tasks", "plan", "HEAD", "--repo", root)
	require.ErrorContains(t, err, "--file names the task file's path")
	_, err = runTasks(t, "tasks", "plan", "HEAD", "--repo", root, "--file", "ci/none.json")
	require.ErrorContains(t, err, "holds no file ci/none.json")
	_, err = runTasks(t, "tasks", "plan", "no-such-ref", "--repo", root, "--file", "ci/tasks.json")
	require.ErrorContains(t, err, "no-such-ref names no commit of the repository")
}

// TestTasksInputsHoldsASchemaInputToWhatTheBuildRead: after a build,
// `tasks inputs` compares each schema input's static set with what the
// build recorded, and names a service the set lacks.
func TestTasksInputsHoldsASchemaInputToWhatTheBuildRead(t *testing.T) {
	root := prepareTasksRepo(t)
	services := filepath.Join(root, "schemas", "services")
	service := filepath.Join(services, "repo-tasks")
	out, err := runTasks(t, "build-all", services, "--cache", "--cache-root", t.TempDir())
	require.NoError(t, err, out)
	commitAll(t, root, "tasks")

	out, err = runTasks(t, "tasks", "inputs", service)
	require.NoError(t, err, out)
	require.Contains(t, out, "repo-tasks: 1 schema input(s) hold what the last build read")

	// The build of shop-media read shop-extra and a file outside the
	// schemas root, as a schema file that references a service and a
	// document that imports a file make it record.
	refs := filepath.Join(root, "schemas", "dist", ".schema-references", "shop-media.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(refs), 0o755))
	require.NoError(t, os.WriteFile(refs, []byte(`{"references":["shop-extra"]}`), 0o644))
	imports := filepath.Join(root, "schemas", "dist", ".authoring-imports", "shop-media.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(imports), 0o755))
	require.NoError(t, os.WriteFile(imports, []byte(`["docs/readme.md","schemas/forge/task.schema.json"]`), 0o644))
	_, err = runTasks(t, "tasks", "inputs", service)
	require.ErrorContains(t, err, "task schema-build: schema input shop-media: service shop-extra is outside the set (a schema file of shop-media references it)")
	require.ErrorContains(t, err, "task schema-build: schema input shop-media: file docs/readme.md is outside the set (a document of shop-media imports it)")
	require.NotContains(t, err.Error(), "forge/task.schema.json", "a shared file of the schemas root is in every set")
}

// TestATaskSectionRegisteredAsData: the naming file names the JSON
// Schemas of a section's parts, and the core holds each part to them
// with no extension linked.
func TestATaskSectionRegisteredAsData(t *testing.T) {
	root := prepareTasksRepo(t)
	service := filepath.Join(root, "schemas", "services", "repo-tasks")
	schema := filepath.Join(service, "src", "tasks.schema.yaml")

	editFile(t, schema, "forge: { tier: nightly }", "forge: { tier: 3 }")
	out, err := runTasks(t, "check", service)
	require.Error(t, err, out)
	require.Contains(t, out, `task test (class GoTest): section "forge":`)

	editFile(t, schema, "forge: { tier: 3 }", "jenkins: { node: big }")
	out, err = runTasks(t, "check", service)
	require.Error(t, err, out)
	require.Contains(t, out, `section "jenkins" is not registered (registered: [forge])`)

	// A schema file the naming file names and the root lacks.
	require.NoError(t, os.Remove(filepath.Join(root, "schemas", "forge", "task.schema.json")))
	out, err = runTasks(t, "check", service)
	require.Error(t, err, out)
	require.Contains(t, err.Error()+out, "[tasks.sections.forge] task:")
}

// TestTasksFormatsToTypeScript: the data form of a Tasks schema is
// written back as its decorators, a task named by an edge as its class
// and a one-path input as the path.
func TestTasksFormatsToTypeScript(t *testing.T) {
	root := prepareTasksRepo(t)
	out, err := runTasks(t, "format", "--to=ts", "--stdout", filepath.Join(root, "schemas", "services", "repo-tasks", "src", "tasks.schema.yaml"))
	require.NoError(t, err, out)
	for _, want := range []string{
		`import { task, tasks } from "@superschematic/tasks";`,
		`@tasks({ image: "` + tasksTestImage + `", platform: "linux/amd64", sections: { forge: { tiers: ["pr", "nightly"] } } })`,
		`@task({ run: ["golangci-lint", "run"], inputs: ["go"], sections: { forge: { tier: "pr", timeoutSeconds: 120 } } })`,
		`@task({ run: ["superschematic", "build-all", "schemas/services"], inputs: [{ kind: "schema", spec: { service: "shop-media" } }], outputs: [{ name: "generated", paths: ["schemas/dist"] }], sections: { forge: { tier: "pr" } } })`,
		`@task({ name: "test", run: ["go", "test", "./..."], inputs: ["go"], uses: [{ task: SchemaBuild, output: "generated" }], after: [Lint], sections: { forge: { tier: "nightly" } } })`,
	} {
		require.Contains(t, out, want)
	}
	// A class a task names is declared above it.
	require.Less(t, strings.Index(out, "class SchemaBuild"), strings.Index(out, "class GoTest"))
	require.Less(t, strings.Index(out, "class Lint"), strings.Index(out, "class GoTest"))
}

func TestJSONSchemaPrintsTheTaskFileAndThePlan(t *testing.T) {
	for flag, want := range map[string][]byte{"--task-file": tasks.FileSchema(), "--task-plan": tasks.PlanSchema()} {
		out, err := runTasks(t, "json-schema", flag)
		require.NoError(t, err)
		require.Equal(t, string(want), out)
		require.True(t, json.Valid([]byte(out)))
	}
	_, err := runTasks(t, "json-schema", "--task-file", "--task-plan")
	require.Error(t, err)
}
