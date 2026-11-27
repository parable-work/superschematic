package ir

import (
	"reflect"
	"testing"
)

func TestTaskName(t *testing.T) {
	for class, want := range map[string]string{
		"Lint":        "lint",
		"SchemaBuild": "schema-build",
		"GoTest":      "go-test",
		"APIBuild":    "api-build",
		"E2e":         "e2e",
		"Build2Go":    "build2-go",
		"snake_case":  "snake-case",
	} {
		if got := TaskName(class, nil); got != want {
			t.Errorf("TaskName(%s) = %s, want %s", class, got, want)
		}
	}
	if got := TaskName("GoTest", &TaskDecl{Name: "test"}); got != "test" {
		t.Errorf("a declared name: %s", got)
	}
}

// TestTasksOf: the tasks come sorted by name, each edge's class read as
// its task's name, with the image, the platform and hermetic in effect.
func TestTasksOf(t *testing.T) {
	no := false
	s := NewSchema("repo-tasks", SchemaKindTasks)
	s.Types["Repo"] = &TypeDef{Name: "Repo", Tasks: &TasksDecl{
		Image: "sha256:default", Platform: "linux/amd64",
		Sections: map[string]map[string]any{"forge": {"tiers": []any{"pr"}}},
	}}
	s.Types["SchemaBuild"] = &TypeDef{Name: "SchemaBuild", Owner: "src/tasks.schema.ts", Task: &TaskDecl{
		Run:     []string{"build"},
		Outputs: []TaskOutput{{Name: "generated", Paths: []string{"dist"}}, {Name: "image", Kind: TaskOutputImage}},
	}}
	s.Types["GoTest"] = &TypeDef{Name: "GoTest", Task: &TaskDecl{
		Name: "test", Run: []string{"go", "test"},
		Uses: []TaskUse{{Task: "SchemaBuild", Output: "generated"}}, After: []string{"Lint", "Missing"},
		Image: "sha256:own", Platform: "linux/arm64",
	}}
	s.Types["Lint"] = &TypeDef{Name: "Lint", Task: &TaskDecl{Run: []string{"lint"}, Hermetic: &no}}

	got := TasksOf(s)
	if got.Service != "repo-tasks" || got.Image != "sha256:default" || got.Platform != "linux/amd64" || len(got.Sections) != 1 {
		t.Fatalf("defaults: %+v", got)
	}
	var names []string
	for _, task := range got.Tasks {
		names = append(names, task.Name)
	}
	if !reflect.DeepEqual(names, []string{"lint", "schema-build", "test"}) {
		t.Fatalf("names = %v", names)
	}
	lint, build, test := got.Task("lint"), got.Task("schema-build"), got.Task("test")
	if lint.Hermetic || !build.Hermetic || !test.Hermetic {
		t.Errorf("hermetic: lint %v, schema-build %v, test %v", lint.Hermetic, build.Hermetic, test.Hermetic)
	}
	if build.Image != "sha256:default" || build.OwnImage || build.Platform != "linux/amd64" || build.OwnPlatform {
		t.Errorf("a task takes the defaults: %+v", build)
	}
	if test.Image != "sha256:own" || !test.OwnImage || test.Platform != "linux/arm64" || !test.OwnPlatform {
		t.Errorf("a task's own image and platform: %+v", test)
	}
	if build.Class != "SchemaBuild" || build.File != "src/tasks.schema.ts" {
		t.Errorf("a task's class and file: %+v", build)
	}
	// An edge that names a class with no @task keeps the class's name,
	// for the kind's verification to name.
	if !reflect.DeepEqual(test.Uses, []TaskUse{{Task: "schema-build", Output: "generated"}}) || !reflect.DeepEqual(test.After, []string{"lint", "Missing"}) {
		t.Errorf("edges: uses %v, after %v", test.Uses, test.After)
	}
	if out, ok := build.Output("generated"); !ok || out.OutputKind() != TaskOutputPaths {
		t.Errorf("an output with paths is of kind paths: %+v, %v", out, ok)
	}
	if out, ok := build.Output("image"); !ok || out.OutputKind() != TaskOutputImage {
		t.Errorf("an image output: %+v, %v", out, ok)
	}
	if _, ok := build.Output("sdk"); ok || got.Task("deploy") != nil {
		t.Error("an output and a task the schema does not declare")
	}

	if TasksOf(NewSchema("empty", SchemaKindTasks)) != nil || TasksOf(nil) != nil {
		t.Error("a schema that declares no task has none")
	}
}
