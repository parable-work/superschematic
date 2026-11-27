package ir

import (
	"sort"
	"strings"
	"unicode"
)

// The Tasks kind (D203): a schema of kind Tasks declares the work a commit
// of its repository needs. Its classes are declarations, as a Stack
// schema's are: `@task` from `@superschematic/tasks` writes a [TaskDecl] on
// its class's TypeDef, and `@tasks` a [TasksDecl]. [TasksOf] assembles them
// into the schema's [Tasks], with each edge's class read as its task's
// name. The task file the build writes, the input key and the plan are made
// from that value (the public `tasks` package and internal/tasks).

// TaskOutputKind is what a task's output is.
type TaskOutputKind string

// The output kinds. An output of kind paths is the files the task writes
// at its paths; an image is an image's digest; a value is a string. A
// runner stores each under the task's name and key on a trusted pass, and
// puts it in place for a task that uses it.
const (
	TaskOutputPaths TaskOutputKind = "paths"
	TaskOutputImage TaskOutputKind = "image"
	TaskOutputValue TaskOutputKind = "value"
)

// TaskPathInput is the kind of the core's path input: tracked files by
// path or glob. TaskSchemaInput is the kind of the core's schema-service
// input: a service's sources with all it reaches.
const (
	TaskPathInput   = "path"
	TaskSchemaInput = "schema"
)

// TasksDecl is the `@tasks` declaration of a Tasks schema's class: the
// defaults of every task, and each section's part that belongs to the
// file, keyed by the section's name. A schema has at most one.
type TasksDecl struct {
	// Image is the digest of the image a task runs from unless it names
	// its own, `sha256:<64 hex>`. The plan command ships in it.
	Image string `json:"image,omitempty" yaml:"image,omitempty"`

	// Platform is the platform a task runs on unless it names its own, as
	// `linux/amd64`.
	Platform string `json:"platform,omitempty" yaml:"platform,omitempty"`

	// Sections holds each section's file part, which the core checks
	// against the section's schema and copies into the task file.
	Sections map[string]map[string]any `json:"sections,omitempty" yaml:"sections,omitempty"`
}

// TaskDecl is the `@task` declaration of a Tasks schema's class: one unit
// of work. Run, Inputs, Uses, Outputs, Services, Env, Image, Platform and
// Hermetic are the work fields, which the input key reads. After orders the
// plan and Sections belongs to the forges that read it; neither enters the
// key.
type TaskDecl struct {
	// Name is the task's name when it is not the class's in kebab case.
	Name string `json:"name,omitempty" yaml:"name,omitempty"`

	// Run is the command, an argument list. Nothing starts a shell: a task
	// that wants one says `["bash", "-c", "..."]`.
	Run []string `json:"run" yaml:"run"`

	// Inputs are what the task reads of the commit, each by a resolver's
	// kind.
	Inputs []TaskInput `json:"inputs,omitempty" yaml:"inputs,omitempty"`

	// Uses are the outputs of other tasks this one reads.
	Uses []TaskUse `json:"uses,omitempty" yaml:"uses,omitempty"`

	// After names the classes of the tasks that must pass before this one
	// starts, and from which it reads nothing.
	After []string `json:"after,omitempty" yaml:"after,omitempty"`

	// Outputs are the named results the task declares.
	Outputs []TaskOutput `json:"outputs,omitempty" yaml:"outputs,omitempty"`

	// Services are the service containers the task runs beside, each by
	// its image's digest.
	Services []TaskService `json:"services,omitempty" yaml:"services,omitempty"`

	// Env is the environment the command starts with.
	Env map[string]string `json:"env,omitempty" yaml:"env,omitempty"`

	// Image and Platform replace the file's defaults for this task.
	Image    string `json:"image,omitempty" yaml:"image,omitempty"`
	Platform string `json:"platform,omitempty" yaml:"platform,omitempty"`

	// Hermetic is false for a task whose result its inputs do not decide:
	// it has no key and is never reused. Unset is true.
	Hermetic *bool `json:"hermetic,omitempty" yaml:"hermetic,omitempty"`

	// Sections holds each section's part that belongs to this task.
	Sections map[string]map[string]any `json:"sections,omitempty" yaml:"sections,omitempty"`
}

// TaskInput is one input of a task: a resolver's kind and the spec the
// resolver reads, an object as the data forms decode it. A kind that takes
// no spec has none.
type TaskInput struct {
	Kind string         `json:"kind" yaml:"kind"`
	Spec map[string]any `json:"spec,omitempty" yaml:"spec,omitempty"`
}

// TaskUse is one output of another task that a task reads. Task is the
// other task's class in a declaration and its name in a [Task].
type TaskUse struct {
	Task   string `json:"task" yaml:"task"`
	Output string `json:"output" yaml:"output"`
}

// TaskOutput is one named result of a task. Paths are the files an output
// of kind paths holds, slash-separated and relative to the repository
// root, each a path or a glob; the other kinds have none.
type TaskOutput struct {
	Name  string         `json:"name" yaml:"name"`
	Kind  TaskOutputKind `json:"kind,omitempty" yaml:"kind,omitempty"`
	Paths []string       `json:"paths,omitempty" yaml:"paths,omitempty"`
}

// OutputKind is the output's kind, paths when it is unset.
func (o TaskOutput) OutputKind() TaskOutputKind {
	if o.Kind == "" {
		return TaskOutputPaths
	}
	return o.Kind
}

// TaskService is one service container: the name the task reaches it by
// and its image, `<repository>@sha256:<64 hex>`.
type TaskService struct {
	Name  string `json:"name" yaml:"name"`
	Image string `json:"image" yaml:"image"`
}

// Tasks is what a Tasks schema declares, assembled by [TasksOf].
type Tasks struct {
	// Service is the schema's name.
	Service string

	// Image and Platform are the defaults `@tasks` gives.
	Image    string
	Platform string

	// Sections is each section's file part.
	Sections map[string]map[string]any

	// Tasks are the declared tasks, sorted by name.
	Tasks []*Task
}

// Task is one declared task with its edges read: Uses and After hold task
// names, and Image, Platform and Hermetic the values in effect.
type Task struct {
	Name string

	// Class is the declaring class, and File the schema file that holds
	// it.
	Class string
	File  string

	Run      []string
	Inputs   []TaskInput
	Uses     []TaskUse
	After    []string
	Outputs  []TaskOutput
	Services []TaskService
	Env      map[string]string
	Image    string
	Platform string
	Hermetic bool
	Sections map[string]map[string]any

	// OwnImage and OwnPlatform report that the task names its own image
	// or platform, not the file's default.
	OwnImage    bool
	OwnPlatform bool
}

// Task returns the task named name, or nil.
func (t *Tasks) Task(name string) *Task {
	if t == nil {
		return nil
	}
	for _, task := range t.Tasks {
		if task.Name == name {
			return task
		}
	}
	return nil
}

// Output returns the task's output named name.
func (t *Task) Output(name string) (TaskOutput, bool) {
	for _, out := range t.Outputs {
		if out.Name == name {
			return out, true
		}
	}
	return TaskOutput{}, false
}

// TaskName is the name of the task a class declares: the declaration's
// name, or the class's in kebab case (`SchemaBuild` is `schema-build`).
func TaskName(class string, decl *TaskDecl) string {
	if decl != nil && decl.Name != "" {
		return decl.Name
	}
	runes := []rune(class)
	var b strings.Builder
	for i, r := range runes {
		if unicode.IsUpper(r) {
			if i > 0 {
				prev := runes[i-1]
				nextLower := i+1 < len(runes) && unicode.IsLower(runes[i+1])
				if unicode.IsLower(prev) || unicode.IsDigit(prev) || (unicode.IsUpper(prev) && nextLower) {
					b.WriteByte('-')
				}
			}
			b.WriteRune(unicode.ToLower(r))
			continue
		}
		if r == '_' {
			b.WriteByte('-')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// TasksOf assembles the tasks a Tasks schema declares. It reads the first
// `@tasks` class by name for the defaults, and every `@task` class. An
// edge that names a class with no `@task` keeps the class's name, so the
// Tasks kind's verification can name it. It checks nothing itself, and
// returns nil when the schema declares neither.
func TasksOf(schema *Schema) *Tasks {
	if schema == nil {
		return nil
	}
	classes := make([]string, 0, len(schema.Types))
	for name := range schema.Types {
		classes = append(classes, name)
	}
	sort.Strings(classes)
	out := &Tasks{Service: schema.Name}
	found, defaults := false, false
	names := map[string]string{}
	for _, class := range classes {
		td := schema.Types[class]
		if td == nil {
			continue
		}
		if td.Tasks != nil && !defaults {
			defaults, found = true, true
			out.Image, out.Platform, out.Sections = td.Tasks.Image, td.Tasks.Platform, td.Tasks.Sections
		}
		if td.Task != nil {
			found = true
			names[class] = TaskName(class, td.Task)
		}
	}
	if !found {
		return nil
	}
	taskOf := func(class string) string {
		if name, ok := names[class]; ok {
			return name
		}
		return class
	}
	for _, class := range classes {
		td := schema.Types[class]
		if td == nil || td.Task == nil {
			continue
		}
		decl := td.Task
		task := &Task{
			Name:        names[class],
			Class:       class,
			File:        td.Owner,
			Run:         decl.Run,
			Inputs:      decl.Inputs,
			Outputs:     decl.Outputs,
			Services:    decl.Services,
			Env:         decl.Env,
			Image:       decl.Image,
			Platform:    decl.Platform,
			Hermetic:    decl.Hermetic == nil || *decl.Hermetic,
			Sections:    decl.Sections,
			OwnImage:    decl.Image != "",
			OwnPlatform: decl.Platform != "",
		}
		if task.Image == "" {
			task.Image = out.Image
		}
		if task.Platform == "" {
			task.Platform = out.Platform
		}
		for _, use := range decl.Uses {
			task.Uses = append(task.Uses, TaskUse{Task: taskOf(use.Task), Output: use.Output})
		}
		for _, after := range decl.After {
			task.After = append(task.After, taskOf(after))
		}
		out.Tasks = append(out.Tasks, task)
	}
	sort.SliceStable(out.Tasks, func(i, j int) bool { return out.Tasks[i].Name < out.Tasks[j].Name })
	return out
}
