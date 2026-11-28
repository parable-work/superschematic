package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"

	"github.com/parable-work/superschematic/internal/registry"
	inttasks "github.com/parable-work/superschematic/internal/tasks"
	"github.com/parable-work/superschematic/tasks"
)

// runTasksSplit runs the core CLI and returns its standard output and
// its standard error apart.
func runTasksSplit(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	out, errOut := new(bytes.Buffer), new(bytes.Buffer)
	root := New(Config{})
	root.SetOut(out)
	root.SetErr(errOut)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), errOut.String(), err
}

// TestTasksPlanProfilesWhatItCost: --profile writes the plan's cost to
// standard error, by resolver kind, and leaves the plan on standard
// output as it is without the flag.
func TestTasksPlanProfilesWhatItCost(t *testing.T) {
	root := prepareTasksRepo(t)
	out, err := runTasks(t, "tasks", filepath.Join(root, "schemas", "services", "repo-tasks"))
	require.NoError(t, err, out)
	commitAll(t, root, "tasks")

	plain, _, err := runTasksSplit(t, "tasks", "plan", "HEAD", "--repo", root, "--file", "ci/tasks.json")
	require.NoError(t, err)
	profiled, profile, err := runTasksSplit(t, "tasks", "plan", "HEAD", "--repo", root, "--file", "ci/tasks.json", "--profile")
	require.NoError(t, err)
	require.Equal(t, plain, profiled)
	_, err = tasks.DecodePlan([]byte(profiled))
	require.NoError(t, err)

	require.Contains(t, profile, "tasks plan: 3 tasks over ")
	require.Contains(t, profile, "read the task file and the tree")
	require.Contains(t, profile, "resolve the inputs and key")
	// lint and test both read `go`: two inputs, one resolution.
	require.Regexp(t, `path\s+2 inputs,\s+1 resolved,\s+1 files`, profile)
	require.Regexp(t, `schema\s+1 inputs,\s+1 resolved,\s+3 files`, profile)
}

// TestTasksReachMeasuresAHistory: `tasks reach` walks the commits that
// changed a schemas root and says how many of them changed every schema
// input, some and none.
func TestTasksReachMeasuresAHistory(t *testing.T) {
	root := prepareTasksRepo(t)
	schemas := filepath.Join(root, "schemas")
	commitAll(t, root, "files")
	// One service: the two others' inputs keep their digests.
	require.NoError(t, os.WriteFile(filepath.Join(schemas, "services", "shop-extra", "notes.md"), []byte("notes\n"), 0o644))
	commitAll(t, root, "extra")
	// A comment in the naming file says nothing new: no input changes.
	editFile(t, filepath.Join(schemas, "superschematic.toml"), "[tasks.sections.forge]", "# the forge's section\n[tasks.sections.forge]")
	commitAll(t, root, "a comment")
	// What the naming file says is every service's.
	editFile(t, filepath.Join(schemas, "superschematic.toml"), "# the forge's section\n", "package_author = \"Acme\"\n\n# the forge's section\n")
	commitAll(t, root, "naming")
	// Outside the root: no commit of the walk.
	require.NoError(t, os.WriteFile(filepath.Join(root, "go", "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644))
	commitAll(t, root, "go")

	out, err := runTasks(t, "tasks", "reach", schemas)
	require.NoError(t, err, out)
	require.Contains(t, out, "schemas: 3 commits that changed it, back from HEAD (1 more left out: no parent, or the root begins there)")
	require.Regexp(t, `changed every schema input's digest\s+1\s+33%`, out)
	require.Regexp(t, `changed some\s+1\s+33%`, out)
	require.Regexp(t, `changed none\s+1\s+33%`, out)
	require.Regexp(t, `changed a file that belongs to no service\s+1\s+33%`, out)
	require.Regexp(t, `median schema inputs changed\s+1 of 3`, out)

	out, err = runTasks(t, "tasks", "reach", schemas, "--json", "--commits", "1")
	require.NoError(t, err, out)
	var reach inttasks.Reach
	require.NoError(t, json.Unmarshal([]byte(out), &reach))
	require.Equal(t, "schemas", reach.Root)
	require.Len(t, reach.Commits, 1)
	require.Equal(t, 3, reach.Commits[0].Changed)
	require.True(t, reach.Commits[0].Shared)

	_, err = runTasks(t, "tasks", "reach", filepath.Join(root, "none"))
	require.ErrorContains(t, err, "tasks reach: schemas root not found")
	_, err = runTasks(t, "tasks", "reach", schemas, "--commits", "0")
	require.ErrorContains(t, err, "--commits is 0")
}

// tasksSandboxHelperEnv holds, for the helper below, the arguments of the
// command it runs, joined by newlines.
const tasksSandboxHelperEnv = "SUPERSCHEMATIC_CLI_TASKS_SANDBOX_HELPER"

// leakyResolvers registers `working-tree`, a resolver that breaks a
// resolver's rules: it reads the file its spec names from the machine,
// and not from the commit's tree.
type leakyResolvers struct{}

func (leakyResolvers) Name() string { return "leaky" }

func (leakyResolvers) Register(r *registry.Registry) error {
	return r.RegisterTaskResolver(registry.TaskResolverSpec{
		Kind:      "working-tree",
		Extension: "leaky",
		Spec:      json.RawMessage(`{"type":"object","required":["file"],"properties":{"file":{"type":"string"}}}`),
		Resolve: func(req registry.TaskResolveRequest) (registry.TaskResolution, error) {
			var spec struct {
				File string `json:"file"`
			}
			if err := json.Unmarshal(req.Spec, &spec); err != nil {
				return registry.TaskResolution{}, err
			}
			if _, err := os.ReadFile(spec.File); err != nil {
				return registry.TaskResolution{}, err
			}
			return registry.TaskResolution{Paths: []string{"go/main.go"}}, nil
		},
	})
}

// TestTasksSandboxHelper runs one command in a process of its own: the
// sandbox holds a process for good, so a test never turns it on in the
// process that runs the other tests.
func TestTasksSandboxHelper(t *testing.T) {
	joined := os.Getenv(tasksSandboxHelperEnv)
	if joined == "" {
		t.Skip("run by TestTasksPlanInTheSandbox")
	}
	root := New(Config{}, leakyResolvers{})
	root.SetOut(os.Stdout)
	root.SetErr(os.Stdout)
	root.SetArgs(strings.Split(joined, "\n"))
	err := root.Execute()
	fmt.Printf("\nhelper: error: %v\n", err)
}

// TestTasksPlanInTheSandbox: with --sandbox a plan of the core's
// resolvers is the plan without it, and a resolver that reads the
// machine fails the plan, where without the sandbox it would have keyed
// a task on a file no commit holds.
func TestTasksPlanInTheSandbox(t *testing.T) {
	root := prepareTasksRepo(t)
	out, err := runTasks(t, "tasks", filepath.Join(root, "schemas", "services", "repo-tasks"))
	require.NoError(t, err, out)
	// A task file with an input of the leaky kind, as a hand would write
	// it: it reads a file of the checkout that no commit holds.
	stray := filepath.Join(root, "stray.txt")
	require.NoError(t, os.WriteFile(stray, []byte("not committed\n"), 0o644))
	leaky := `{"tasks": {"read": {"hermetic": false, "run": ["true"], "inputs": [{"kind": "working-tree", "spec": {"file": ` + fmt.Sprintf("%q", stray) + `}}]}}, "version": 1}` + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, "ci", "leaky.json"), []byte(leaky), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".git", "info", "exclude"), []byte("stray.txt\n"), 0o644))
	commitAll(t, root, "tasks")

	helper := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(os.Args[0], "-test.run=^TestTasksSandboxHelper$", "-test.v")
		cmd.Env = append(os.Environ(), tasksSandboxHelperEnv+"="+strings.Join(args, "\n"))
		out, _ := cmd.CombinedOutput()
		return string(out)
	}
	plan := []string{"tasks", "plan", "HEAD", "--repo", root, "--file", "ci/tasks.json"}
	sandboxed := helper(append(plan, "--sandbox", "--profile")...)
	if strings.Contains(sandboxed, inttasks.ErrSandboxUnsupported.Error()) || strings.Contains(sandboxed, "seccomp:") {
		t.Skipf("this machine has no sandbox: %s", sandboxed)
	}
	require.Contains(t, sandboxed, "helper: error: <nil>", sandboxed)
	require.Contains(t, sandboxed, ", in the sandbox")
	plain, _, err := runTasksSplit(t, plan...)
	require.NoError(t, err)
	for _, name := range []string{"lint", "schema-build", "test"} {
		p, err := tasks.DecodePlan([]byte(plain))
		require.NoError(t, err)
		require.Contains(t, sandboxed, `"key": "`+*p.Tasks[name].Key+`"`, "the sandbox changes no key")
	}

	// Without the sandbox the leaky resolver reads the checkout, and the
	// plan passes.
	leak := []string{"tasks", "plan", "HEAD", "--repo", root, "--file", "ci/leaky.json"}
	require.Contains(t, helper(leak...), "helper: error: <nil>")
	// With it, the read is refused, and the plan says whose rule it broke.
	refused := helper(append(leak, "--sandbox")...)
	require.Contains(t, refused, "operation not permitted")
	require.Contains(t, refused, "The sandbox refused it: a resolver reads tracked files through the commit's tree, starts no process and uses no network")
	require.NotContains(t, refused, "helper: error: <nil>")
}

// githubTasksConfig turns the fixture's Tasks service into one the
// github renderer renders: the config names the renderer, and the schema
// holds the github section.
func githubTasksConfig(t *testing.T, root, install string) {
	t.Helper()
	service := filepath.Join(root, "schemas", "services", "repo-tasks")
	ci := "    ci:\n      github: {}\n"
	if install != "" {
		ci = "    ci:\n      github:\n        install: " + install + "\n"
	}
	editFile(t, filepath.Join(service, "schema.config.yaml"), "    install: ci/tasks.json\n", "    install: ci/tasks.json\n"+ci)
	editFile(t, filepath.Join(service, "src", "tasks.schema.yaml"), "          tiers: [pr, nightly]\n", `          tiers: [pr, nightly]
        github:
          "on":
            pull_request: {}
          imageRepository: ghcr.io/acme/ci
`)
}

// TestTasksWritesAndChecksTheWorkflow: a Tasks service whose config names
// the github renderer has a workflow beside its task file. `tasks` writes
// both, `--check` holds both current, and build-all installs both on
// every build, a restore from the cache included.
func TestTasksWritesAndChecksTheWorkflow(t *testing.T) {
	root := prepareTasksRepo(t)
	services := filepath.Join(root, "schemas", "services")
	service := filepath.Join(services, "repo-tasks")
	githubTasksConfig(t, root, "")
	workflow := filepath.Join(root, ".github", "workflows", "repo-tasks.yml")

	// The renderer's own directory, which must exist.
	_, err := runTasks(t, "tasks", service)
	require.ErrorContains(t, err, "the github workflow installs at "+workflow)
	require.ErrorContains(t, err, "create it, or correct outputs.tasks.ci.github.install")
	require.NoError(t, os.MkdirAll(filepath.Dir(workflow), 0o755))

	out, err := runTasks(t, "tasks", service)
	require.NoError(t, err, out)
	require.Contains(t, out, "repo-tasks: "+workflow+" written")
	written, err := os.ReadFile(workflow)
	require.NoError(t, err)
	text := string(written)
	require.Contains(t, text, "# Code generated by superschematic from Tasks service repo-tasks. DO NOT EDIT.")
	require.Contains(t, text, "on:\n  pull_request: {}\n")
	require.Contains(t, text, "  test:\n    name: test\n    needs: [lint, schema-build]\n")
	require.Contains(t, text, "container: ghcr.io/acme/ci@"+tasksTestImage)
	require.Contains(t, text, "name: schema-build.generated")

	out, err = runTasks(t, "tasks", service, "--check")
	require.NoError(t, err, out)
	require.Contains(t, out, "repo-tasks: ci/tasks.json is current")
	require.Contains(t, out, "repo-tasks: .github/workflows/repo-tasks.yml is current")

	// The workflow follows the schema as the task file does.
	require.NoError(t, os.WriteFile(workflow, append(written, []byte("# edited by hand\n")...), 0o644))
	_, err = runTasks(t, "tasks", service, "--check")
	require.ErrorContains(t, err, "tasks --check: .github/workflows/repo-tasks.yml is not what repo-tasks gives")

	// build-all installs both, and again when it restores the service.
	cache := t.TempDir()
	require.NoError(t, os.Remove(workflow))
	out, err = runTasks(t, "build-all", services, "--cache", "--cache-root", cache)
	require.NoError(t, err, out)
	require.Contains(t, out, "github workflow of repo-tasks installed at "+workflow)
	built, err := os.ReadFile(workflow)
	require.NoError(t, err)
	require.Equal(t, text, string(built))
	generated, err := os.ReadFile(filepath.Join(root, "schemas", "dist", "tasks", "repo-tasks", "ci", "github", "repo-tasks.yml"))
	require.NoError(t, err)
	require.Equal(t, text, string(generated))
	require.NoError(t, os.Remove(workflow))
	require.NoError(t, os.RemoveAll(filepath.Join(root, "schemas", "dist")))
	out, err = runTasks(t, "build-all", services, "--cache", "--cache-root", cache)
	require.NoError(t, err, out)
	require.Contains(t, out, "restored from cache")
	restored, err := os.ReadFile(workflow)
	require.NoError(t, err)
	require.Equal(t, text, string(restored))
}

// TestAReusingWorkflowReadsTheKeysThePlanPrints: with the section's
// `reuse` the workflow's first job plans the commit from the committed
// task file, and each keyed task's job reads its key from an output of
// that job named as the task. `tasks plan --keys` prints exactly those
// names, each with the key the plan gives it.
func TestAReusingWorkflowReadsTheKeysThePlanPrints(t *testing.T) {
	root := prepareTasksRepo(t)
	service := filepath.Join(root, "schemas", "services", "repo-tasks")
	githubTasksConfig(t, root, "ci/workflows")
	editFile(t, filepath.Join(service, "src", "tasks.schema.yaml"), "          imageRepository: ghcr.io/acme/ci\n", "          imageRepository: ghcr.io/acme/ci\n          reuse: {}\n")
	require.NoError(t, os.MkdirAll(filepath.Join(root, "ci", "workflows"), 0o755))
	out, err := runTasks(t, "tasks", service)
	require.NoError(t, err, out)
	commitAll(t, root, "tasks")

	written, err := os.ReadFile(filepath.Join(root, "ci", "workflows", "repo-tasks.yml"))
	require.NoError(t, err)
	var workflow struct {
		Jobs map[string]struct {
			Needs   []string          `yaml:"needs"`
			Outputs map[string]string `yaml:"outputs"`
			Steps   []struct {
				Name string            `yaml:"name"`
				Run  string            `yaml:"run"`
				Env  map[string]string `yaml:"env"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	require.NoError(t, yaml.Unmarshal(written, &workflow))
	planJob := workflow.Jobs["plan"]
	require.Len(t, planJob.Steps, 2)
	// The plan reads the file where the service commits it.
	require.Contains(t, planJob.Steps[1].Run, `superschematic tasks plan HEAD --file ci/tasks.json --keys >> "$GITHUB_OUTPUT"`)

	keys, err := runTasks(t, "tasks", "plan", "HEAD", "--repo", root, "--file", "ci/tasks.json", "--keys")
	require.NoError(t, err, keys)
	full, err := runTasks(t, "tasks", "plan", "HEAD", "--repo", root, "--file", "ci/tasks.json")
	require.NoError(t, err, full)
	plan, err := tasks.DecodePlan([]byte(full))
	require.NoError(t, err)
	printed := map[string]string{}
	var order []string
	for _, line := range strings.Split(strings.TrimSuffix(keys, "\n"), "\n") {
		name, key, ok := strings.Cut(line, "=")
		require.True(t, ok, line)
		printed[name] = key
		order = append(order, name)
	}
	require.Equal(t, []string{"lint", "schema-build", "test"}, order)
	for name, task := range plan.Tasks {
		require.NotNil(t, task.Key, name)
		require.Equal(t, *task.Key, printed[name], name)
		// The plan job gives the key under the task's name, and the task's
		// job reads it there.
		require.Equal(t, "${{ steps.keys.outputs."+name+" }}", planJob.Outputs[name])
		require.Contains(t, workflow.Jobs[name].Needs, "plan")
		require.Equal(t, "${{ needs.plan.outputs."+name+" }}", workflow.Jobs[name].Steps[0].Env["TASK_KEY"])
	}
	require.Len(t, planJob.Outputs, len(plan.Tasks))

	// A service that commits no task file has none to plan from.
	editFile(t, filepath.Join(service, "schema.config.yaml"), "    install: ci/tasks.json\n", "")
	_, err = runTasks(t, "tasks", service)
	require.ErrorContains(t, err, "sections.github.reuse plans the commit from its committed task file, and the service commits none; give outputs.tasks.install the file's path")
}

// TestTasksRefusesAWorkflowItCannotRender: a renderer nobody registered,
// a schema with no github section, and a section its schema refuses each
// fail the build of the service, and say what to write.
func TestTasksRefusesAWorkflowItCannotRender(t *testing.T) {
	root := prepareTasksRepo(t)
	service := filepath.Join(root, "schemas", "services", "repo-tasks")
	config := filepath.Join(service, "schema.config.yaml")
	schema := filepath.Join(service, "src", "tasks.schema.yaml")
	require.NoError(t, os.MkdirAll(filepath.Join(root, "ops", "workflows"), 0o755))

	// No github section: the workflow has no triggers.
	editFile(t, config, "    install: ci/tasks.json\n", "    install: ci/tasks.json\n    ci:\n      github:\n        install: ops/workflows\n")
	_, err := runTasks(t, "tasks", service)
	require.ErrorContains(t, err, "CI renderer github: the workflow's triggers are the github section's, and @tasks holds none")

	// A section its schema refuses fails as any section does.
	editFile(t, schema, "          tiers: [pr, nightly]\n", "          tiers: [pr, nightly]\n        github:\n          imageRepository: ghcr.io/acme/ci\n")
	out, err := runTasks(t, "check", service)
	require.Error(t, err)
	require.Contains(t, out, `section "github"`)
	require.Contains(t, out, "on")

	// With its triggers, the workflow is written where the config says.
	editFile(t, schema, "          imageRepository: ghcr.io/acme/ci\n", "          imageRepository: ghcr.io/acme/ci\n          \"on\":\n            push: {}\n")
	out, err = runTasks(t, "tasks", service)
	require.NoError(t, err, out)
	require.FileExists(t, filepath.Join(root, "ops", "workflows", "repo-tasks.yml"))

	editFile(t, config, "      github:\n", "      jenkins:\n")
	_, err = runTasks(t, "tasks", service)
	require.ErrorContains(t, err, "outputs.tasks.ci.jenkins names a CI renderer that is not registered (registered CI renderers: github)")
}
