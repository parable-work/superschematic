package cigen_test

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/parable-work/superschematic/internal/generator/cigen"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/tasks"
)

// tasksGolden is the workflow the task file below renders.
const tasksGolden = "testdata/golden/tasks/repo-tasks.yml"

const taskImage = "sha256:6a1f0c5d2b7e4f8093a1c6d5e4b3a29187f6e5d4c3b2a1908f7e6d5c4b3a2918"

// repoTasks is a task file with each thing the renderer carries: both
// edges, a paths output with a pattern, a value and an image output, a
// service container, an environment, a task with its own image and
// platform, one that is not hermetic and one with its own placement.
func repoTasks() *tasks.File {
	section := func(v any) map[string]json.RawMessage {
		data, err := json.Marshal(v)
		if err != nil {
			panic(err)
		}
		return map[string]json.RawMessage{cigen.GitHubRenderer: data}
	}
	return &tasks.File{
		Version:  tasks.FileVersion,
		Image:    taskImage,
		Platform: "linux/amd64",
		Sections: section(map[string]any{
			"on": map[string]any{
				"pull_request": map[string]any{"branches": []string{"main"}},
				"push":         map[string]any{"branches": []string{"main"}},
				"schedule":     []map[string]any{{"cron": "0 5 * * *"}},
			},
			"imageRepository": "ghcr.io/acme/ci",
			"permissions":     map[string]string{"contents": "read", "packages": "read"},
		}),
		Tasks: map[string]tasks.Task{
			"lint": {Run: []string{"golangci-lint", "run"}, Hermetic: true},
			"schema-build": {
				Run:      []string{"superschematic", "build-all", "schemas/services"},
				Hermetic: true,
				Outputs: []tasks.Output{
					{Name: "generated", Kind: tasks.OutputPaths, Paths: []string{"schemas/dist", "go/**/*_gen.go", "docs/it's here/[a-c]*.md"}},
					{Name: "schema-version", Kind: tasks.OutputValue},
				},
			},
			"go-test": {
				Run:      []string{"go", "test", "-run", "Test(A|B)", "./..."},
				Hermetic: true,
				Uses:     []tasks.Use{{Task: "schema-build", Output: "generated"}, {Task: "schema-build", Output: "schema-version"}},
				After:    []string{"lint"},
				Services: []tasks.Service{{Name: "postgres", Image: "docker.io/library/postgres@" + taskImage}},
				Env:      map[string]string{"PGHOST": "postgres", "GOFLAGS": "-mod=readonly -count=1"},
				Sections: section(map[string]any{"timeoutMinutes": 20}),
			},
			"image": {
				Run:      []string{"sh", "-c", `docker build -q . > "$TASK_OUTPUTS/api"`},
				Hermetic: true,
				Uses:     []tasks.Use{{Task: "schema-build", Output: "generated"}},
				Outputs:  []tasks.Output{{Name: "api", Kind: tasks.OutputImage}},
				Image:    "sha256:" + strings.Repeat("b", 64),
				Platform: "linux/arm64",
			},
			"deploy": {
				Run:      []string{"scripts/deploy.sh"},
				Hermetic: false,
				Uses:     []tasks.Use{{Task: "image", Output: "api"}},
				After:    []string{"go-test"},
				Sections: section(map[string]any{"if": "github.event_name == 'push'", "runsOn": "deploy-runner"}),
			},
		},
	}
}

func renderTasks(t *testing.T, file *tasks.File) ([]byte, error) {
	t.Helper()
	files, err := cigen.GitHub().RenderTasks(registry.CITasksRequest{Service: "repo-tasks", File: file})
	if err != nil {
		return nil, err
	}
	if len(files) != 1 || files[0].Path != "repo-tasks.yml" {
		t.Fatalf("the renderer writes %v, want repo-tasks.yml alone", files)
	}
	return files[0].Data, nil
}

// TestGitHubRendersATasksService holds the workflow of a task file to its
// golden, which -update rewrites, and to its structure: a job per task,
// both edges as needs, a paths output as an artifact between the two
// jobs, a value as a job output, and the section's triggers.
func TestGitHubRendersATasksService(t *testing.T) {
	got, err := renderTasks(t, repoTasks())
	if err != nil {
		t.Fatal(err)
	}
	if *update {
		if err := os.MkdirAll(filepath.Dir(tasksGolden), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(tasksGolden, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if want := readFile(t, tasksGolden); !bytes.Equal(got, want) {
		t.Errorf("the workflow differs from %s; run with -update and review the diff", tasksGolden)
	}
	// One file gives one workflow.
	if again, err := renderTasks(t, repoTasks()); err != nil || !bytes.Equal(got, again) {
		t.Errorf("two renders of one file differ: %v", err)
	}

	var workflow struct {
		Name        string            `yaml:"name"`
		On          map[string]any    `yaml:"on"`
		Permissions map[string]string `yaml:"permissions"`
		Jobs        map[string]struct {
			Needs     []string          `yaml:"needs"`
			If        string            `yaml:"if"`
			RunsOn    string            `yaml:"runs-on"`
			Timeout   int               `yaml:"timeout-minutes"`
			Container string            `yaml:"container"`
			Outputs   map[string]string `yaml:"outputs"`
			Services  map[string]struct {
				Image string `yaml:"image"`
			} `yaml:"services"`
			Steps []struct {
				Name string            `yaml:"name"`
				Uses string            `yaml:"uses"`
				Run  string            `yaml:"run"`
				With map[string]string `yaml:"with"`
				Env  map[string]string `yaml:"env"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(got, &workflow); err != nil {
		t.Fatalf("the workflow is no YAML: %v", err)
	}
	if workflow.Name != "repo-tasks" || len(workflow.Jobs) != 5 || len(workflow.On) != 3 {
		t.Errorf("name %q, %d jobs, on %v", workflow.Name, len(workflow.Jobs), workflow.On)
	}
	if workflow.Permissions["packages"] != "read" {
		t.Errorf("permissions = %v", workflow.Permissions)
	}
	test := workflow.Jobs["go-test"]
	if strings.Join(test.Needs, ",") != "lint,schema-build" {
		t.Errorf("go-test needs %v; it uses schema-build and follows lint", test.Needs)
	}
	if test.Container != "ghcr.io/acme/ci@"+taskImage || test.Timeout != 20 || test.RunsOn != "ubuntu-latest" {
		t.Errorf("go-test runs in %q on %q for %d minutes", test.Container, test.RunsOn, test.Timeout)
	}
	if test.Services["postgres"].Image != "docker.io/library/postgres@"+taskImage {
		t.Errorf("go-test's services = %v", test.Services)
	}
	var names []string
	for _, s := range test.Steps {
		names = append(names, s.Name)
		switch s.Name {
		case "Download generated of schema-build":
			if s.With["name"] != "schema-build.generated" || !strings.HasPrefix(s.Uses, "actions/download-artifact@") {
				t.Errorf("the download step = %+v", s)
			}
		case "Put what the task uses in place":
			if !strings.Contains(s.Run, `tar -xf "$TASK_ARTIFACTS/schema-build.generated/generated.tar"`) ||
				!strings.Contains(s.Run, `> "$TASK_USES/schema-build/schema-version"`) ||
				s.Env["USE_1"] != "${{ needs.schema-build.outputs.schema-version }}" {
				t.Errorf("the place step = %+v", s)
			}
		case "Run":
			// The command is the argument list, quoted for the shell.
			if s.Run != "go test -run 'Test(A|B)' ./..." || s.Env["GOFLAGS"] != "-mod=readonly -count=1" || s.Env["PGHOST"] != "postgres" {
				t.Errorf("the run step = %+v", s)
			}
		}
	}
	if want := "Check out,Name the task's directories,Download generated of schema-build,Put what the task uses in place,Run"; strings.Join(names, ",") != want {
		t.Errorf("go-test's steps = %v, want %s", names, want)
	}
	build := workflow.Jobs["schema-build"]
	if build.Outputs["schema-version"] != "${{ steps.outputs.outputs.schema-version }}" {
		t.Errorf("schema-build's outputs = %v", build.Outputs)
	}
	for _, s := range build.Steps {
		switch s.Name {
		case "Pack generated":
			// A pattern is the shell's to expand; all else is quoted.
			if !strings.Contains(s.Run, `-- schemas/dist go/**/*_gen.go 'docs/it'\''s here/'[a-c]*.md`) {
				t.Errorf("the pack step runs %q", s.Run)
			}
		case "Upload generated":
			if s.With["name"] != "schema-build.generated" || s.With["if-no-files-found"] != "error" {
				t.Errorf("the upload step = %+v", s)
			}
		}
	}
	// A task's own image, platform and placement.
	if image := workflow.Jobs["image"]; image.RunsOn != "ubuntu-24.04-arm" || image.Container != "ghcr.io/acme/ci@sha256:"+strings.Repeat("b", 64) {
		t.Errorf("image runs in %q on %q", image.Container, image.RunsOn)
	}
	if deploy := workflow.Jobs["deploy"]; deploy.RunsOn != "deploy-runner" || deploy.If != "github.event_name == 'push'" || strings.Join(deploy.Needs, ",") != "go-test,image" {
		t.Errorf("deploy = %+v", deploy)
	}
	// A task with no edge and no output is a checkout and its command.
	if lint := workflow.Jobs["lint"]; len(lint.Steps) != 2 || lint.Needs != nil {
		t.Errorf("lint = %+v", lint)
	}
	lint(t, tasksGolden)
}

// TestGitHubRefusesATaskFileItCannotRun: each thing the workflow cannot
// carry is refused, by the task and the section field that settles it.
func TestGitHubRefusesATaskFileItCannotRun(t *testing.T) {
	for name, c := range map[string]struct {
		change func(*tasks.File)
		want   string
	}{
		"no section": {
			func(f *tasks.File) { f.Sections = nil },
			"the workflow's triggers are the github section's, and @tasks holds none; give @tasks sections.github.on",
		},
		"no repository": {
			func(f *tasks.File) { f.Sections["github"] = json.RawMessage(`{"on": {"push": {}}}`) },
			"runs from image sha256:",
		},
		"an unknown field": {
			func(f *tasks.File) { f.Sections["github"] = json.RawMessage(`{"on": {"push": {}}, "runner": "x"}`) },
			`sections.github: json: unknown field "runner"`,
		},
		"an expression in a command": {
			func(f *tasks.File) {
				task := f.Tasks["lint"]
				task.Run = []string{"echo", "${{ secrets.TOKEN }}"}
				f.Tasks["lint"] = task
			},
			"task lint: its command holds \"${{ secrets.TOKEN }}\", and GitHub would read the ${{ }} as an expression",
		},
		"an expression in a variable": {
			func(f *tasks.File) {
				task := f.Tasks["lint"]
				task.Env = map[string]string{"TOKEN": "${{ secrets.TOKEN }}"}
				f.Tasks["lint"] = task
			},
			"task lint: its env TOKEN holds",
		},
		"a variable the workflow sets": {
			func(f *tasks.File) {
				task := f.Tasks["lint"]
				task.Env = map[string]string{"TASK_OUTPUTS": "/tmp"}
				f.Tasks["lint"] = task
			},
			"task lint: its env sets TASK_OUTPUTS, which the workflow sets for every task",
		},
		"a platform with no runner": {
			func(f *tasks.File) {
				task := f.Tasks["lint"]
				task.Platform = "darwin/arm64"
				f.Tasks["lint"] = task
			},
			"task lint: runs on platform darwin/arm64, which no GitHub-hosted runner the renderer knows is; name its runner in sections.github.runsOn",
		},
		"a service GitHub cannot name": {
			func(f *tasks.File) {
				task := f.Tasks["go-test"]
				task.Services = []tasks.Service{{Name: "the db", Image: "postgres@" + taskImage}}
				f.Tasks["go-test"] = task
			},
			`task go-test: service container "the db" has a name GitHub does not take as a service's id`,
		},
	} {
		file := repoTasks()
		c.change(file)
		_, err := renderTasks(t, file)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want an error with %q", name, err, c.want)
		}
	}
}

// reuseGolden is the workflow of the same task file with the section's
// `reuse`.
const reuseGolden = "testdata/golden/tasks/repo-tasks-reuse.yml"

// reuseTasks is repoTasks with the section asking for reuse.
func reuseTasks(t *testing.T, reuse string) *tasks.File {
	t.Helper()
	file := repoTasks()
	var section map[string]json.RawMessage
	if err := json.Unmarshal(file.Sections[cigen.GitHubRenderer], &section); err != nil {
		t.Fatal(err)
	}
	section["reuse"] = json.RawMessage(reuse)
	data, err := json.Marshal(section)
	if err != nil {
		t.Fatal(err)
	}
	file.Sections[cigen.GitHubRenderer] = data
	return file
}

func renderReuse(t *testing.T, file *tasks.File, taskFile string) ([]byte, error) {
	t.Helper()
	files, err := cigen.GitHub().RenderTasks(registry.CITasksRequest{Service: "repo-tasks", File: file, TaskFile: taskFile})
	if err != nil {
		return nil, err
	}
	if len(files) != 1 || files[0].Path != "repo-tasks.yml" {
		t.Fatalf("the renderer writes %v, want repo-tasks.yml alone", files)
	}
	return files[0].Data, nil
}

// reuseWorkflow is what the tests below read of a workflow.
type reuseWorkflow struct {
	Jobs map[string]struct {
		Needs     []string          `yaml:"needs"`
		Container string            `yaml:"container"`
		RunsOn    string            `yaml:"runs-on"`
		Outputs   map[string]string `yaml:"outputs"`
		Steps     []struct {
			Name  string            `yaml:"name"`
			ID    string            `yaml:"id"`
			If    string            `yaml:"if"`
			Uses  string            `yaml:"uses"`
			Shell string            `yaml:"shell"`
			Run   string            `yaml:"run"`
			With  map[string]string `yaml:"with"`
			Env   map[string]string `yaml:"env"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
}

// TestGitHubReusesAPassByItsInputKey: with the section's `reuse` a job
// plans the commit, and each task that has a key looks for a pass under
// it, runs its steps only when it found none, keeps the pass it made, and
// hands on its outputs either way. A task with no key is as it was.
func TestGitHubReusesAPassByItsInputKey(t *testing.T) {
	got, err := renderReuse(t, reuseTasks(t, `{}`), "ci/tasks.json")
	if err != nil {
		t.Fatal(err)
	}
	if *update {
		if err := os.WriteFile(reuseGolden, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if want := readFile(t, reuseGolden); !bytes.Equal(got, want) {
		t.Errorf("the workflow differs from %s; run with -update and review the diff", reuseGolden)
	}
	var workflow reuseWorkflow
	if err := yaml.Unmarshal(got, &workflow); err != nil {
		t.Fatalf("the workflow is no YAML: %v", err)
	}
	if len(workflow.Jobs) != 6 {
		t.Fatalf("%d jobs, want a job per task and the plan", len(workflow.Jobs))
	}

	// The plan job runs the plan command in the file's image and gives a
	// key per hermetic task.
	plan := workflow.Jobs["plan"]
	if plan.Container != "ghcr.io/acme/ci@"+taskImage || plan.RunsOn != "ubuntu-latest" || plan.Needs != nil {
		t.Errorf("plan runs in %q on %q after %v", plan.Container, plan.RunsOn, plan.Needs)
	}
	if len(plan.Outputs) != 4 || plan.Outputs["go-test"] != "${{ steps.keys.outputs.go-test }}" || plan.Outputs["deploy"] != "" {
		t.Errorf("plan's outputs = %v; deploy is not hermetic and has no key", plan.Outputs)
	}
	if len(plan.Steps) != 2 || plan.Steps[0].With["fetch-depth"] != "2" || plan.Steps[1].ID != "keys" {
		t.Fatalf("plan's steps = %+v", plan.Steps)
	}
	for _, want := range []string{
		`superschematic tasks plan HEAD --file ci/tasks.json --base 'HEAD^1' --keys >> "$GITHUB_OUTPUT"`,
		`superschematic tasks plan HEAD --file ci/tasks.json --keys >> "$GITHUB_OUTPUT"`,
		`git cat-file -e 'HEAD^1:ci/tasks.json'`,
	} {
		if !strings.Contains(plan.Steps[1].Run, want) {
			t.Errorf("the plan step lacks %q:\n%s", want, plan.Steps[1].Run)
		}
	}

	// A task with a key: the pass is looked up and kept under one key,
	// and what a pass stands in for runs only when none was found.
	const missed = "steps.pass.outputs.cache-hit != 'true'"
	test := workflow.Jobs["go-test"]
	if strings.Join(test.Needs, ",") != "lint,plan,schema-build" {
		t.Errorf("go-test needs %v", test.Needs)
	}
	var names []string
	keys := map[string]string{}
	for _, s := range test.Steps {
		names = append(names, s.Name)
		switch s.Name {
		case "Name the task's directories":
			if s.Env["TASK_KEY"] != "${{ needs.plan.outputs.go-test }}" || !strings.HasPrefix(s.Run, `test -n "$TASK_KEY"`) || s.If != "" {
				t.Errorf("the directories step = %+v", s)
			}
		case "Look for a pass under the task's key":
			keys["restore"] = s.With["key"]
			if s.ID != "pass" || !strings.HasPrefix(s.Uses, "actions/cache/restore@") || s.With["path"] != "${{ env.TASK_PASS }}" || s.If != "" {
				t.Errorf("the restore step = %+v", s)
			}
		case "Keep the pass under the task's key":
			keys["save"] = s.With["key"]
			if !strings.HasPrefix(s.Uses, "actions/cache/save@") || s.With["path"] != "${{ env.TASK_PASS }}" || s.If != missed {
				t.Errorf("the save step = %+v", s)
			}
		default:
			if s.If != missed {
				t.Errorf("step %q runs when a pass was found: if = %q", s.Name, s.If)
			}
		}
	}
	if want := "Name the task's directories,Look for a pass under the task's key,Check out,Download generated of schema-build,Put what the task uses in place,Run,Record the pass,Keep the pass under the task's key"; strings.Join(names, ",") != want {
		t.Errorf("go-test's steps = %v, want %s", names, want)
	}
	if keys["restore"] != "tasks-v1-repo-tasks-go-test-${{ env.TASK_KEY }}" || keys["save"] != keys["restore"] {
		t.Errorf("the pass is looked up under %q and kept under %q", keys["restore"], keys["save"])
	}

	// A task's outputs go on from the pass, found or made: the upload and
	// the job's outputs run either way and read under $TASK_PASS.
	build := workflow.Jobs["schema-build"]
	seen := map[string]bool{}
	for _, s := range build.Steps {
		seen[s.Name] = true
		switch s.Name {
		case "Pack generated":
			if s.If != missed || !strings.Contains(s.Run, `tar -cf "$TASK_PASS/generated.tar" -- `) {
				t.Errorf("the pack step = %+v", s)
			}
		case "Record the pass":
			if s.If != missed || strings.TrimSpace(s.Run) != "test -f \"$TASK_OUTPUTS/schema-version\"\nprintf '%s\\n' \"$TASK_KEY\" > \"$TASK_PASS/key\"" {
				t.Errorf("the record step = %+v", s)
			}
		case "Upload generated":
			if s.If != "" || s.With["path"] != "${{ env.TASK_PASS }}/generated.tar" {
				t.Errorf("the upload step = %+v", s)
			}
		case "Read the task's outputs":
			if s.If != "" || s.ID != "outputs" || !strings.Contains(s.Run, `cat "$TASK_OUTPUTS/schema-version"`) {
				t.Errorf("the outputs step = %+v", s)
			}
		case "Name the task's directories":
			if !strings.Contains(s.Run, `echo "TASK_OUTPUTS=$RUNNER_TEMP/task/pass/outputs"`) || !strings.Contains(s.Run, `echo "TASK_PASS=$RUNNER_TEMP/task/pass"`) {
				t.Errorf("the directories step runs %q", s.Run)
			}
		}
	}
	for _, name := range []string{"Pack generated", "Record the pass", "Keep the pass under the task's key", "Upload generated", "Read the task's outputs"} {
		if !seen[name] {
			t.Errorf("schema-build has no step %q", name)
		}
	}

	// A task that is not hermetic has no key: its job is the job of a
	// workflow that reuses nothing.
	plain, err := renderTasks(t, repoTasks())
	if err != nil {
		t.Fatal(err)
	}
	var without reuseWorkflow
	if err := yaml.Unmarshal(plain, &without); err != nil {
		t.Fatal(err)
	}
	if deploy := workflow.Jobs["deploy"]; strings.Join(deploy.Needs, ",") != "go-test,image" || len(deploy.Steps) != len(without.Jobs["deploy"].Steps) {
		t.Errorf("deploy = %+v", deploy)
	}
	for _, s := range workflow.Jobs["deploy"].Steps {
		if s.If != "" || strings.Contains(s.Uses, "actions/cache") {
			t.Errorf("deploy's step %q is a keyed task's: %+v", s.Name, s)
		}
	}

	// Every script the workflow adds is one its shell reads.
	for id, job := range workflow.Jobs {
		for _, s := range job.Steps {
			if s.Run == "" || s.Name == "Run" {
				continue
			}
			shell := "sh"
			if s.Shell != "" {
				shell = s.Shell
			}
			cmd := exec.Command(shell, "-n")
			cmd.Stdin = strings.NewReader(s.Run)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("job %s, step %q: %s does not read its script: %v\n%s", id, s.Name, shell, err, out)
			}
		}
	}
	lint(t, reuseGolden)

	// The section names the plan command, and a task may be named plan.
	file := reuseTasks(t, `{"command": ["acme-schematic", "--naming", "ci/naming file.toml"]}`)
	file.Tasks["plan"] = tasks.Task{Run: []string{"terraform", "plan"}, Hermetic: true}
	got, err = renderReuse(t, file, "ci/tasks.json")
	if err != nil {
		t.Fatal(err)
	}
	workflow = reuseWorkflow{}
	if err := yaml.Unmarshal(got, &workflow); err != nil {
		t.Fatal(err)
	}
	planner, ok := workflow.Jobs["plan-2"]
	if !ok || len(planner.Steps) != 2 || !strings.Contains(planner.Steps[1].Run, `acme-schematic --naming 'ci/naming file.toml' tasks plan HEAD --file ci/tasks.json --keys`) {
		t.Fatalf("the plan job of a file with a task named plan = %+v", planner)
	}
	if task := workflow.Jobs["plan"]; strings.Join(task.Needs, ",") != "plan-2" || task.Steps[0].Env["TASK_KEY"] != "${{ needs.plan-2.outputs.plan }}" {
		t.Errorf("the task named plan = %+v", task)
	}
}

// TestThePlanStepPlansAgainstTheBase runs the plan job's script as a
// runner would, with a command that prints what it was given: on a pull
// request the plan is given the merge commit's first parent as its base,
// when that commit holds the task file, and on any other event none.
func TestThePlanStepPlansAgainstTheBase(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	got, err := renderReuse(t, reuseTasks(t, `{"command": ["fake-plan"]}`), "ci/tasks.json")
	if err != nil {
		t.Fatal(err)
	}
	var workflow reuseWorkflow
	if err := yaml.Unmarshal(got, &workflow); err != nil {
		t.Fatal(err)
	}
	script := workflow.Jobs["plan"].Steps[1].Run

	dir := t.TempDir()
	repo, bin, home := filepath.Join(dir, "repo"), filepath.Join(dir, "bin"), filepath.Join(dir, "home")
	for _, d := range []string{filepath.Join(repo, "ci"), bin, home} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(bin, "fake-plan"), []byte("#!/bin/sh\necho \"lint=$*\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), "HOME="+home, "GITHUB_WORKSPACE="+repo, "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir, cmd.Env = repo, env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	commit := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repo, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		git("add", "-A")
		git("commit", "-q", "-m", name)
	}
	run := func(event string) string {
		t.Helper()
		output := filepath.Join(dir, "output")
		_ = os.Remove(output)
		cmd := exec.Command("sh", "-e", "-c", script)
		cmd.Dir = repo
		cmd.Env = append(append([]string{}, env...), "GITHUB_EVENT_NAME="+event, "GITHUB_OUTPUT="+output)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("the plan step failed on %s: %v\n%s", event, err, out)
		}
		data, err := os.ReadFile(output)
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(data))
	}
	git("init", "-q")
	commit("README.md", "one\n")
	commit("ci/tasks.json", "{}\n")

	// The base holds no task file: no definition to hold a task to.
	if got := run("pull_request"); got != "lint=tasks plan HEAD --file ci/tasks.json --keys" {
		t.Errorf("on a pull request whose base holds no task file the step ran %q", got)
	}
	commit("ci/tasks.json", "{ }\n")
	if got := run("pull_request"); got != "lint=tasks plan HEAD --file ci/tasks.json --base HEAD^1 --keys" {
		t.Errorf("on a pull request the step ran %q", got)
	}
	if got := run("push"); got != "lint=tasks plan HEAD --file ci/tasks.json --keys" {
		t.Errorf("on a push the step ran %q", got)
	}
}

// TestGitHubRefusesAReuseItCannotPlan: a workflow that reuses needs the
// committed task file, the image the plan command ships in, and a runner
// for it.
func TestGitHubRefusesAReuseItCannotPlan(t *testing.T) {
	for name, c := range map[string]struct {
		taskFile string
		reuse    string
		change   func(*tasks.File)
		want     string
	}{
		"no committed task file": {"", `{}`, func(*tasks.File) {},
			"sections.github.reuse plans the commit from its committed task file, and the service commits none; give outputs.tasks.install the file's path"},
		"no image": {"ci/tasks.json", `{}`, func(f *tasks.File) { f.Image = "" },
			"sections.github.reuse plans the commit with the plan command, which ships in the image @tasks names, and @tasks names none"},
		"no runner for the plan": {"ci/tasks.json", `{}`, func(f *tasks.File) { f.Platform = "darwin/arm64" },
			"sections.github.reuse runs the plan command on platform darwin/arm64"},
		"an expression in the command": {"ci/tasks.json", `{"command": ["${{ inputs.cli }}"]}`, func(*tasks.File) {},
			"sections.github.reuse.command holds"},
		"an unknown field": {"ci/tasks.json", `{"scope": "branch"}`, func(*tasks.File) {},
			`sections.github: json: unknown field "scope"`},
		"a variable the workflow sets": {"ci/tasks.json", `{}`, func(f *tasks.File) {
			task := f.Tasks["lint"]
			task.Env = map[string]string{"TASK_KEY": "x"}
			f.Tasks["lint"] = task
		}, "task lint: its env sets TASK_KEY, which the workflow sets for every task"},
	} {
		file := reuseTasks(t, c.reuse)
		c.change(file)
		_, err := renderReuse(t, file, c.taskFile)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want an error with %q", name, err, c.want)
		}
	}
	// The section's schema holds `reuse` to its one field.
	section := cigen.GitHubTaskSection()
	if !bytes.Contains(section.File, []byte(`"reuse"`)) {
		t.Errorf("the section's schema holds no reuse: %s", section.File)
	}
}
