package registry

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	ir "github.com/parable-work/superschematic/ir"
)

const (
	taskTestImage   = "sha256:6a1f0c5d2b7e4f8093a1c6d5e4b3a29187f6e5d4c3b2a1908f7e6d5c4b3a2918"
	taskTestService = "postgres@sha256:0b1c2d3e4f5061728394a5b6c7d8e9f00b1c2d3e4f5061728394a5b6c7d8e9f0"
)

// tasksRegistry is a registry with a path resolver and a section, forge,
// whose Verify holds each task to a tier the file declares.
func tasksRegistry(t *testing.T) *Registry {
	t.Helper()
	reg := New(naming.Naming{})
	if err := reg.RegisterTaskResolver(TaskResolverSpec{
		Kind: ir.TaskPathInput, Spec: PathInputSpec(),
		Resolve: func(TaskResolveRequest) (TaskResolution, error) { return TaskResolution{}, nil },
	}); err != nil {
		t.Fatal(err)
	}
	if err := reg.RegisterTaskResolver(TaskResolverSpec{
		Kind:    "all",
		Resolve: func(TaskResolveRequest) (TaskResolution, error) { return TaskResolution{}, nil },
	}); err != nil {
		t.Fatal(err)
	}
	if err := reg.RegisterTaskSection(TaskSectionSpec{
		Name: "forge",
		File: json.RawMessage(`{"type":"object","required":["tiers"],"additionalProperties":false,"properties":{"tiers":{"type":"array","items":{"type":"string"}}}}`),
		Task: json.RawMessage(`{"type":"object","required":["tier"],"additionalProperties":false,"properties":{"tier":{"type":"string"}}}`),
		Verify: func(req TaskSectionRequest, r VerifyReporter) {
			var file struct {
				Tiers []string `json:"tiers"`
			}
			_ = json.Unmarshal(req.File, &file)
			for _, name := range req.Names {
				var part struct {
					Tier string `json:"tier"`
				}
				if raw, ok := req.Tasks[name]; ok {
					_ = json.Unmarshal(raw, &part)
				}
				found := false
				for _, tier := range file.Tiers {
					found = found || tier == part.Tier
				}
				if !found {
					r.Errorf("", "section forge: task %s runs in tier %q, which the file does not declare", name, part.Tier)
				}
			}
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := reg.RegisterTaskSection(TaskSectionSpec{Name: "labels", Task: json.RawMessage(`{"type":"object"}`)}); err != nil {
		t.Fatal(err)
	}
	return reg
}

// TestVerifyTasks: the Tasks kind's rules over an assembled schema, which
// hold for every form.
func TestVerifyTasks(t *testing.T) {
	spec, _ := tasksRegistry(t).Kind(string(ir.SchemaKindTasks))
	no := false
	path := func(p string) ir.TaskInput {
		return ir.TaskInput{Kind: ir.TaskPathInput, Spec: map[string]any{"paths": []any{p}}}
	}
	tier := func(name string) map[string]map[string]any {
		return map[string]map[string]any{"forge": {"tier": name}}
	}
	valid := func() *ir.Schema {
		s := ir.NewSchema("repo-tasks", ir.SchemaKindTasks)
		s.Types["Repo"] = &ir.TypeDef{Name: "Repo", Tasks: &ir.TasksDecl{
			Image: taskTestImage, Platform: "linux/amd64",
			Sections: map[string]map[string]any{"forge": {"tiers": []any{"pr", "nightly"}}},
		}}
		s.Types["SchemaBuild"] = &ir.TypeDef{Name: "SchemaBuild", Task: &ir.TaskDecl{
			Run:      []string{"superschematic", "build-all"},
			Inputs:   []ir.TaskInput{path("schemas"), {Kind: "all"}},
			Outputs:  []ir.TaskOutput{{Name: "generated", Paths: []string{"schemas/dist"}}, {Name: "image", Kind: ir.TaskOutputImage}},
			Sections: tier("pr"),
		}}
		s.Types["Lint"] = &ir.TypeDef{Name: "Lint", Task: &ir.TaskDecl{Run: []string{"golangci-lint", "run"}, Inputs: []ir.TaskInput{path("go")}, Sections: tier("pr")}}
		s.Types["GoTest"] = &ir.TypeDef{Name: "GoTest", Task: &ir.TaskDecl{
			Name:     "test",
			Run:      []string{"go", "test", "./..."},
			Uses:     []ir.TaskUse{{Task: "SchemaBuild", Output: "generated"}},
			After:    []string{"Lint"},
			Services: []ir.TaskService{{Name: "postgres", Image: taskTestService}},
			Env:      map[string]string{"CGO_ENABLED": "1"},
			Sections: map[string]map[string]any{"forge": {"tier": "pr"}, "labels": {"team": "shop"}},
		}}
		// A task that is not hermetic needs no image, and may use one
		// that is not hermetic either.
		s.Types["Deploy"] = &ir.TypeDef{Name: "Deploy", Task: &ir.TaskDecl{Run: []string{"deploy.sh"}, Hermetic: &no, Outputs: []ir.TaskOutput{{Name: "url", Kind: ir.TaskOutputValue}}, Sections: tier("nightly")}}
		s.Types["Smoke"] = &ir.TypeDef{Name: "Smoke", Task: &ir.TaskDecl{
			Run: []string{"smoke.sh"}, Hermetic: &no,
			Uses: []ir.TaskUse{{Task: "Deploy", Output: "url"}, {Task: "SchemaBuild", Output: "image"}}, After: []string{"GoTest"}, Sections: tier("nightly"),
		}}
		return s
	}
	var f findings
	spec.Verify(valid(), &f)
	if len(f.errs) > 0 {
		t.Fatalf("a valid schema: %v", f.errs)
	}
	// A schema with no @tasks class, whose tasks are none of them
	// hermetic, needs no image.
	bare := ir.NewSchema("repo-tasks", ir.SchemaKindTasks)
	bare.Types["Lint"] = &ir.TypeDef{Name: "Lint", Task: &ir.TaskDecl{Run: []string{"lint"}, Hermetic: &no}}
	spec.Verify(bare, &f)
	if len(f.errs) > 0 {
		t.Fatalf("a schema of one task that is not hermetic: %v", f.errs)
	}

	task := func(s *ir.Schema, class string) *ir.TaskDecl { return s.Types[class].Task }
	for _, tc := range []struct {
		name   string
		change func(*ir.Schema)
		want   string
	}{
		{"a class that declares nothing", func(s *ir.Schema) { s.Types["Plain"] = &ir.TypeDef{Name: "Plain"} }, "class Plain declares nothing; every class of a Tasks schema is an @task class, or the one @tasks class"},
		{"a class with both declarations", func(s *ir.Schema) { s.Types["Repo"].Task = &ir.TaskDecl{Run: []string{"true"}} }, "class Repo is both an @tasks and an @task class"},
		{"two @tasks classes", func(s *ir.Schema) { s.Types["Other"] = &ir.TypeDef{Name: "Other", Tasks: &ir.TasksDecl{}} }, "declares 2 @tasks classes (Other, Repo)"},
		{"a class with fields", func(s *ir.Schema) { s.Types["Lint"].Fields = []*ir.FieldDef{{Name: "level"}} }, "class Lint has fields or a parent"},
		{"an enum", func(s *ir.Schema) { s.Enums["Tier"] = &ir.EnumDef{Name: "Tier"} }, "whose schema declares only tasks (enum Tier)"},
		{"a tag for the file's image", func(s *ir.Schema) { s.Types["Repo"].Tasks.Image = "ubuntu:24.04" }, `@tasks image is "ubuntu:24.04"; an image is named by its digest`},
		{"a platform that is no platform", func(s *ir.Schema) { s.Types["Repo"].Tasks.Platform = "amd64" }, `@tasks platform is "amd64"`},
		{"a name that is no name", func(s *ir.Schema) { task(s, "Lint").Name = "Go Lint" }, "task Go Lint (class Lint): its name is lowercase words joined by hyphens"},
		{"two tasks of one name", func(s *ir.Schema) { task(s, "Lint").Name = "test" }, "task test (class Lint) and class GoTest are both named test"},
		{"no command", func(s *ir.Schema) { task(s, "Lint").Run = nil }, "task lint (class Lint): run is empty"},
		{"an input kind no resolver registers", func(s *ir.Schema) { task(s, "Lint").Inputs = []ir.TaskInput{{Kind: "cargo"}} }, `inputs[0]: input kind "cargo" has no resolver (registered: [all path])`},
		{"a spec the resolver's schema refuses", func(s *ir.Schema) {
			task(s, "Lint").Inputs = []ir.TaskInput{{Kind: ir.TaskPathInput, Spec: map[string]any{"path": "go"}}}
		}, `inputs[0]: input kind "path" spec:`},
		{"a spec for a kind that takes none", func(s *ir.Schema) {
			task(s, "Lint").Inputs = []ir.TaskInput{{Kind: "all", Spec: map[string]any{"deep": true}}}
		}, `inputs[0]: input kind "all" takes no spec`},
		{"an output named twice", func(s *ir.Schema) {
			task(s, "SchemaBuild").Outputs = append(task(s, "SchemaBuild").Outputs, ir.TaskOutput{Name: "image", Kind: ir.TaskOutputValue})
		}, "output image is declared twice"},
		{"an output of paths with none", func(s *ir.Schema) { task(s, "SchemaBuild").Outputs[0].Paths = nil }, "output generated names no paths"},
		{"an output whose path leaves the repository", func(s *ir.Schema) { task(s, "SchemaBuild").Outputs[0].Paths = []string{"../dist"} }, `output generated: path "../dist" leaves the repository`},
		{"an image output with paths", func(s *ir.Schema) { task(s, "SchemaBuild").Outputs[1].Paths = []string{"out"} }, "output image is of kind image and names paths"},
		{"an output of no kind", func(s *ir.Schema) { task(s, "SchemaBuild").Outputs[1].Kind = "tarball" }, `output image is of kind "tarball"; an output is paths, image or value`},
		{"a service container by tag", func(s *ir.Schema) { task(s, "GoTest").Services[0].Image = "postgres:16" }, `service container postgres runs image "postgres:16"; an image is named by its digest`},
		{"a service container named twice", func(s *ir.Schema) {
			task(s, "GoTest").Services = append(task(s, "GoTest").Services, ir.TaskService{Name: "postgres", Image: taskTestService})
		}, "service container postgres is declared twice"},
		{"an env name that is no name", func(s *ir.Schema) { task(s, "GoTest").Env = map[string]string{"2FAST": "1"} }, `env names the variable "2FAST"`},
		{"a task's image by tag", func(s *ir.Schema) { task(s, "Lint").Image = "golang:1.27" }, `task lint (class Lint): image is "golang:1.27"; an image is named by its digest`},
		{"a hermetic task with no image", func(s *ir.Schema) { s.Types["Repo"].Tasks.Image = "" }, "task lint (class Lint): is hermetic and has no image"},
		{"a hermetic task with no platform", func(s *ir.Schema) { s.Types["Repo"].Tasks.Platform = "" }, "task lint (class Lint): is hermetic and has no platform"},
		{"a use of no task", func(s *ir.Schema) { task(s, "GoTest").Uses[0].Task = "Repo" }, "uses output generated of Repo, which is no task of this schema"},
		{"a use of an output its task does not declare", func(s *ir.Schema) { task(s, "GoTest").Uses[0].Output = "sdk" }, "uses output sdk of task schema-build, which declares no such output (declared: generated, image)"},
		{"a use of its own output", func(s *ir.Schema) {
			task(s, "SchemaBuild").Uses = []ir.TaskUse{{Task: "SchemaBuild", Output: "generated"}}
		}, "task schema-build (class SchemaBuild): uses its own output generated"},
		{"a use written twice", func(s *ir.Schema) { task(s, "GoTest").Uses = append(task(s, "GoTest").Uses, task(s, "GoTest").Uses[0]) }, "uses output generated of task schema-build twice"},
		{"a hermetic task that uses one that is not", func(s *ir.Schema) {
			task(s, "GoTest").Uses = append(task(s, "GoTest").Uses, ir.TaskUse{Task: "Deploy", Output: "url"})
		}, "task test (class GoTest): is hermetic and uses output url of task deploy, which is not; the key of test would not decide what it read"},
		{"an after of no task", func(s *ir.Schema) { task(s, "GoTest").After = []string{"Vet"} }, "comes after Vet, which is no task of this schema"},
		{"an after of itself", func(s *ir.Schema) { task(s, "Lint").After = []string{"Lint"} }, "task lint (class Lint): comes after itself"},
		{"a cycle through uses and after", func(s *ir.Schema) { task(s, "SchemaBuild").After = []string{"GoTest"} }, "the tasks schema-build -> test -> schema-build form a cycle through uses and after"},
		{"a section nothing registers", func(s *ir.Schema) { task(s, "Lint").Sections = map[string]map[string]any{"jenkins": {"node": "big"}} }, `section "jenkins" is not registered (registered: [forge labels])`},
		{"a task's part its schema refuses", func(s *ir.Schema) { task(s, "Lint").Sections = map[string]map[string]any{"forge": {"tier": 3}} }, `task lint (class Lint): section "forge":`},
		{"a file part its schema refuses", func(s *ir.Schema) { s.Types["Repo"].Tasks.Sections["forge"] = map[string]any{"tiers": "pr"} }, `@tasks: section "forge":`},
		{"a file part of a section that has none", func(s *ir.Schema) { s.Types["Repo"].Tasks.Sections["labels"] = map[string]any{"team": "shop"} }, `@tasks: section "labels" has no part for the file`},
		{"what a schema cannot say, the section's Verify does", func(s *ir.Schema) { task(s, "Lint").Sections = tier("weekly") }, `section forge: task lint runs in tier "weekly", which the file does not declare`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := valid()
			tc.change(s)
			var f findings
			spec.Verify(s, &f)
			for _, err := range f.errs {
				if strings.Contains(err, tc.want) {
					return
				}
			}
			t.Fatalf("want an error holding %q, got %v", tc.want, f.errs)
		})
	}
}

func TestRegisterTaskSeams(t *testing.T) {
	reg := tasksRegistry(t)
	resolve := func(TaskResolveRequest) (TaskResolution, error) { return TaskResolution{}, nil }
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"a resolver registered twice", reg.RegisterTaskResolver(TaskResolverSpec{Kind: "path", Resolve: resolve}), `task resolver "path" is already registered`},
		{"a resolver with no Resolve", reg.RegisterTaskResolver(TaskResolverSpec{Kind: "cargo"}), `task resolver "cargo" has no Resolve function`},
		{"a resolver whose kind is no name", reg.RegisterTaskResolver(TaskResolverSpec{Kind: "Go Module", Resolve: resolve}), "must be lowercase words joined by hyphens"},
		{"a resolver whose schema does not compile", reg.RegisterTaskResolver(TaskResolverSpec{Kind: "cargo", Spec: json.RawMessage(`{"type": 3}`), Resolve: resolve}), `task resolver "cargo" Spec:`},
		{"a section registered twice", reg.RegisterTaskSection(TaskSectionSpec{Name: "forge", Task: json.RawMessage(`{}`)}), `task section "forge" is already registered`},
		{"a section with neither part", reg.RegisterTaskSection(TaskSectionSpec{Name: "empty"}), `task section "empty" has neither a File nor a Task schema`},
		{"a section whose schema does not compile", reg.RegisterTaskSection(TaskSectionSpec{Name: "broken", File: json.RawMessage(`{"type": 3}`)}), `task section "broken" file schema:`},
	} {
		if tc.err == nil || !strings.Contains(tc.err.Error(), tc.want) {
			t.Errorf("%s: want an error holding %q, got %v", tc.name, tc.want, tc.err)
		}
	}
	if got := reg.TaskResolvers(); strings.Join(got, ",") != "all,path" {
		t.Errorf("resolvers: %v", got)
	}
	if got := reg.TaskSections(); strings.Join(got, ",") != "forge,labels" {
		t.Errorf("sections: %v", got)
	}
}
