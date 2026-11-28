package cigen_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

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
