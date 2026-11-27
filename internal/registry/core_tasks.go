package registry

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/tasks"
)

// pkgTasks is the authoring package of the Tasks kind's decorators, @tasks
// and @task (D203).
const pkgTasks = "@superschematic/tasks"

// tasksGenerator is the Tasks kind's one generator: it writes the task
// file. It is registered by generator.RegisterCore, with the other core
// generators.
const tasksGenerator = "tasks"

// tasksKind is the core Tasks kind (D203): the work a commit of a
// repository needs. Its schema declares each task with @task, a class per
// task, and the defaults of all of them with @tasks. Like a Stack's, its
// classes declare: they hold no fields and are no language's types, so the
// kind's one generator writes the task file. Nothing names a Tasks
// service, so it has no sentinel. A schema input may name its service by
// the service's handle, so its schema files import its siblings'
// sentinels, which names them and references no type of theirs. Its
// verification reads the registry, for the input kinds and the sections
// an extension registers.
func tasksKind(r *Registry) KindSpec {
	return KindSpec{
		Name:       string(ir.SchemaKindTasks),
		StructRole: ir.RoleEmbeddedStruct,
		ForbiddenPackages: map[string]bool{
			pkgAPI:   true,
			pkgDB:    true,
			pkgStack: true,
		},
		Pipeline:                []string{tasksGenerator},
		NoSentinel:              true,
		ImportsSiblingSentinels: true,
		Verify:                  func(schema *ir.Schema, rep VerifyReporter) { verifyTasks(r, schema, rep) },
	}
}

// taskClassSchema is the JSON Schema of the place that takes a task's
// class: a class reference in every form.
const taskClassSchema = `{"type":"object","required":["class"],"additionalProperties":false,"properties":{"class":{"type":"string","minLength":1}}}`

// taskSectionsSchema is the JSON Schema of a declaration's `sections`: an
// object per section, which the section's own schema then checks.
const taskSectionsSchema = `{"type":"object","additionalProperties":{"type":"object"}}`

var (
	tasksArgs = json.RawMessage(`{
		"type": "object",
		"additionalProperties": false,
		"properties": {
			"image": {"type": "string", "minLength": 1},
			"platform": {"type": "string", "minLength": 1},
			"sections": ` + taskSectionsSchema + `
		}
	}`)
	taskArgs = json.RawMessage(`{
		"type": "object",
		"required": ["run"],
		"additionalProperties": false,
		"properties": {
			"name": {"type": "string", "minLength": 1},
			"run": {"type": "array", "minItems": 1, "items": {"type": "string"}},
			"inputs": {"type": "array", "items": {"oneOf": [
				{"type": "string", "minLength": 1},
				{"type": "object", "required": ["kind"], "additionalProperties": false, "properties": {"kind": {"type": "string", "minLength": 1}, "spec": {"type": "object"}}}
			]}},
			"uses": {"type": "array", "items": {
				"type": "object", "required": ["task", "output"], "additionalProperties": false,
				"properties": {"task": ` + taskClassSchema + `, "output": {"type": "string", "minLength": 1}}
			}},
			"after": {"type": "array", "items": ` + taskClassSchema + `},
			"outputs": {"type": "array", "items": {
				"type": "object", "required": ["name"], "additionalProperties": false,
				"properties": {
					"name": {"type": "string", "minLength": 1},
					"kind": {"enum": ["paths", "image", "value"]},
					"paths": {"type": "array", "items": {"type": "string", "minLength": 1}}
				}
			}},
			"services": {"type": "array", "items": {
				"type": "object", "required": ["name", "image"], "additionalProperties": false,
				"properties": {"name": {"type": "string", "minLength": 1}, "image": {"type": "string", "minLength": 1}}
			}},
			"env": {"type": "object", "additionalProperties": {"type": "string"}},
			"image": {"type": "string", "minLength": 1},
			"platform": {"type": "string", "minLength": 1},
			"hermetic": {"type": "boolean"},
			"sections": ` + taskSectionsSchema + `
		}
	}`)
)

// tasksDecorators returns @tasks and @task, from @superschematic/tasks and
// only in Tasks schemas. Each writes its class's declaration (ir.TypeDef's
// Tasks or Task), and a class takes one. A task named as a value is a
// class reference in every form, so Apply reads the same value from each.
// Whether an edge names a task of the schema is verifyTasks's, so it holds
// for the data forms too.
func tasksDecorators() []DecoratorSpec {
	kinds := []string{string(ir.SchemaKindTasks)}
	spec := func(name string, args json.RawMessage, apply func(*ir.TypeDef, any) error) DecoratorSpec {
		return DecoratorSpec{
			Name: name, Packages: []string{pkgTasks}, Target: TargetType, Kinds: kinds, Args: args,
			// A handle in an input, as a schema input's service, only
			// names its service: the task file holds the name, and the
			// build of a Tasks schema reads nothing of the service.
			Identities: []string{"inputs"},
			Apply: func(n Node, args []any, _ Site) error {
				if n.Type.Tasks != nil || n.Type.Task != nil {
					return fmt.Errorf("class %s already has a declaration; a class takes one of @tasks and @task", n.Type.Name)
				}
				if len(args) != 1 {
					return fmt.Errorf("@%s takes exactly one argument", name)
				}
				return apply(n.Type, args[0])
			},
		}
	}
	return []DecoratorSpec{
		spec("tasks", tasksArgs, func(td *ir.TypeDef, arg any) error {
			var args struct {
				Image    string                    `json:"image"`
				Platform string                    `json:"platform"`
				Sections map[string]map[string]any `json:"sections"`
			}
			if err := DecodeArgs([]any{arg}, &args); err != nil {
				return err
			}
			td.Tasks = &ir.TasksDecl{Image: args.Image, Platform: args.Platform, Sections: args.Sections}
			return nil
		}),
		spec("task", taskArgs, applyTask),
	}
}

// applyTask reads @task's argument. An input written as a string is a path
// input, a schema input's service written as a handle is its name, and a
// task named as a value is its class.
func applyTask(td *ir.TypeDef, arg any) error {
	var args struct {
		Name   string   `json:"name"`
		Run    []string `json:"run"`
		Inputs []any    `json:"inputs"`
		Uses   []struct {
			Task   any    `json:"task"`
			Output string `json:"output"`
		} `json:"uses"`
		After    []any                     `json:"after"`
		Outputs  []ir.TaskOutput           `json:"outputs"`
		Services []ir.TaskService          `json:"services"`
		Env      map[string]string         `json:"env"`
		Image    string                    `json:"image"`
		Platform string                    `json:"platform"`
		Hermetic *bool                     `json:"hermetic"`
		Sections map[string]map[string]any `json:"sections"`
	}
	if err := DecodeArgs([]any{arg}, &args); err != nil {
		return err
	}
	decl := &ir.TaskDecl{
		Name: args.Name, Run: args.Run, Outputs: args.Outputs, Services: args.Services,
		Env: args.Env, Image: args.Image, Platform: args.Platform, Hermetic: args.Hermetic,
		Sections: args.Sections,
	}
	for _, v := range args.Inputs {
		if s, ok := v.(string); ok {
			decl.Inputs = append(decl.Inputs, ir.TaskInput{Kind: ir.TaskPathInput, Spec: map[string]any{"paths": []any{s}}})
			continue
		}
		obj, _ := v.(map[string]any)
		input := ir.TaskInput{}
		input.Kind, _ = obj["kind"].(string)
		input.Spec, _ = obj["spec"].(map[string]any)
		// A schema input's service is written as its name, or as its
		// handle, which a wrong name cannot be: the declaration keeps the
		// name, as the data forms write it.
		if handle, ok := input.Spec["service"].(map[string]any); ok && input.Kind == ir.TaskSchemaInput {
			name, _ := handle["name"].(string)
			if name == "" {
				return ArgErrorf(0, "@task inputs: a schema input's service is a service's name or its handle")
			}
			spec := make(map[string]any, len(input.Spec))
			for key, value := range input.Spec {
				spec[key] = value
			}
			spec["service"] = name
			input.Spec = spec
		}
		decl.Inputs = append(decl.Inputs, input)
	}
	for i, use := range args.Uses {
		class, err := DecodeClassRef(use.Task)
		if err != nil {
			return ArgErrorf(0, "@task uses[%d] task is the class of the task whose output this one reads: %v", i, err)
		}
		decl.Uses = append(decl.Uses, ir.TaskUse{Task: class, Output: use.Output})
	}
	for i, v := range args.After {
		class, err := DecodeClassRef(v)
		if err != nil {
			return ArgErrorf(0, "@task after[%d] is the class of a task this one follows: %v", i, err)
		}
		decl.After = append(decl.After, class)
	}
	td.Task = decl
	return nil
}

var (
	taskImagePattern        = regexp.MustCompile(tasks.ImagePattern)
	taskServiceImagePattern = regexp.MustCompile(tasks.ServiceImagePattern)
	taskPlatformPattern     = regexp.MustCompile(tasks.PlatformPattern)
	taskEnvPattern          = regexp.MustCompile(tasks.EnvPattern)
)

// CheckTaskPath checks one path of an output or of a path input: a path
// or a glob from the repository root, slash-separated, that stays inside
// it.
func CheckTaskPath(p string) error { return tasks.CheckPath(p) }

// verifyTasks is the Tasks kind's verification (D203), in every form.
//
// Every class is an @tasks or an @task class, and at most one is @tasks.
// Each task has a name of its own, a command, inputs whose kind a resolver
// registers and whose spec that resolver's schema takes, and outputs,
// service containers and environment variables that are well formed. An
// image and a service container's image are digests. An edge names a task
// of the schema, a `uses` edge an output that task declares, and the two
// kinds of edge together hold no cycle. A hermetic task has an image and
// a platform, and uses no task that is not hermetic: its key would not
// decide its result. Each section is registered and passes its schema.
func verifyTasks(reg *Registry, schema *ir.Schema, r VerifyReporter) {
	classes := make([]string, 0, len(schema.Types))
	for name := range schema.Types {
		classes = append(classes, name)
	}
	sort.Strings(classes)
	var defaults []string
	for _, class := range classes {
		td := schema.Types[class]
		if td == nil {
			continue
		}
		switch {
		case td.Tasks != nil && td.Task != nil:
			r.Errorf(td.Owner, "class %s is both an @tasks and an @task class; a class takes one", class)
		case td.Tasks != nil:
			defaults = append(defaults, class)
		case td.Task == nil:
			r.Errorf(td.Owner, "class %s declares nothing; every class of a %s schema is an @task class, or the one @tasks class, from %s", class, ir.SchemaKindTasks, pkgTasks)
		}
		if len(td.Fields) > 0 || td.Extends != "" || len(td.Implements) > 0 {
			r.Errorf(td.Owner, "class %s has fields or a parent; a class of a %s schema is a declaration, and its decorator's argument holds all it says", class, ir.SchemaKindTasks)
		}
	}
	if len(defaults) > 1 {
		r.Errorf("", "%s declares %d @tasks classes (%s); a schema has one, since it gives the defaults of every task", schema.Name, len(defaults), strings.Join(defaults, ", "))
	}
	var declared []string
	for name := range schema.Enums {
		declared = append(declared, "enum "+name)
	}
	for name := range schema.Unions {
		declared = append(declared, "union "+name)
	}
	for _, set := range schema.OperationSets {
		if set != nil {
			declared = append(declared, "operation set "+set.Name)
		}
	}
	for _, job := range schema.Jobs {
		if job != nil {
			declared = append(declared, "job "+job.Name)
		}
	}
	for _, worker := range schema.Workers {
		if worker != nil {
			declared = append(declared, "worker "+worker.Name)
		}
	}
	sort.Strings(declared)
	for _, what := range declared {
		r.Errorf("", "%s is a %s service, whose schema declares only tasks (%s)", schema.Name, ir.SchemaKindTasks, what)
	}

	decl := ir.TasksOf(schema)
	if decl == nil {
		return
	}
	if decl.Image != "" && !taskImagePattern.MatchString(decl.Image) {
		r.Errorf("", "@tasks image is %q; an image is named by its digest, sha256:<64 hex>, since a tag names no bytes", decl.Image)
	}
	if decl.Platform != "" && !taskPlatformPattern.MatchString(decl.Platform) {
		r.Errorf("", "@tasks platform is %q; a platform is an operating system and an architecture, as linux/amd64", decl.Platform)
	}

	byName := map[string]*ir.Task{}
	for _, task := range decl.Tasks {
		where := fmt.Sprintf("task %s (class %s)", task.Name, task.Class)
		if !taskNamePattern.MatchString(task.Name) {
			r.Errorf(task.File, "%s: its name is lowercase words joined by hyphens", where)
			continue
		}
		if other, dup := byName[task.Name]; dup {
			r.Errorf(task.File, "%s and class %s are both named %s; each task takes its own name", where, other.Class, task.Name)
			continue
		}
		byName[task.Name] = task
	}

	for _, task := range decl.Tasks {
		if byName[task.Name] != task {
			continue
		}
		where := fmt.Sprintf("task %s (class %s)", task.Name, task.Class)
		errorf := func(format string, args ...any) {
			r.Errorf(task.File, "%s: %s", where, fmt.Sprintf(format, args...))
		}
		verifyTaskWork(reg, task, errorf)
		verifyTaskEdges(task, byName, errorf)
		for _, name := range keysOf(task.Sections) {
			part, err := rawObject(task.Sections[name])
			if err == nil {
				err = reg.CheckTaskSectionTask(name, part)
			}
			if err != nil {
				errorf("%v", err)
			}
		}
	}
	for _, cycle := range taskCycles(decl.Tasks, byName) {
		r.Errorf("", "%s: the tasks %s form a cycle through uses and after, so none of them can start first", schema.Name, strings.Join(cycle, " -> "))
	}

	// The sections: each part against its schema, then each section's own
	// Verify over the parts the schema holds.
	held := map[string]TaskSectionRequest{}
	for _, name := range keysOf(decl.Sections) {
		part, err := rawObject(decl.Sections[name])
		if err == nil {
			err = reg.CheckTaskSectionFile(name, part)
		}
		if err != nil {
			r.Errorf("", "@tasks: %v", err)
			continue
		}
		held[name] = TaskSectionRequest{File: part}
	}
	for _, task := range decl.Tasks {
		for name, value := range task.Sections {
			part, err := rawObject(value)
			if err != nil {
				continue
			}
			req := held[name]
			if req.Tasks == nil {
				req.Tasks = map[string]json.RawMessage{}
			}
			req.Tasks[task.Name] = part
			held[name] = req
		}
	}
	names := make([]string, 0, len(byName))
	for name := range byName {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range keysOf(held) {
		section, ok := reg.TaskSection(name)
		if !ok || section.Verify == nil {
			continue
		}
		req := held[name]
		req.Names = names
		section.Verify(req, r)
	}
}

// verifyTaskWork checks one task's work fields.
func verifyTaskWork(reg *Registry, task *ir.Task, errorf func(string, ...any)) {
	if len(task.Run) == 0 || task.Run[0] == "" {
		errorf("run is empty; it is the command as an argument list, as [\"go\", \"test\", \"./...\"]")
	}
	for i, input := range task.Inputs {
		spec, err := rawObject(input.Spec)
		if err == nil {
			err = reg.CheckTaskInput(input.Kind, spec)
		}
		if err != nil {
			errorf("inputs[%d]: %v", i, err)
		}
	}
	outputs := map[string]bool{}
	for i, out := range task.Outputs {
		switch {
		case !taskNamePattern.MatchString(out.Name):
			errorf("outputs[%d] is named %q; an output's name is lowercase words joined by hyphens", i, out.Name)
			continue
		case outputs[out.Name]:
			errorf("output %s is declared twice", out.Name)
			continue
		}
		outputs[out.Name] = true
		switch out.OutputKind() {
		case ir.TaskOutputPaths:
			if len(out.Paths) == 0 {
				errorf("output %s names no paths; an output of kind paths is the files the task writes there", out.Name)
			}
			for _, p := range out.Paths {
				if err := CheckTaskPath(p); err != nil {
					errorf("output %s: path %v", out.Name, err)
				}
			}
		case ir.TaskOutputImage, ir.TaskOutputValue:
			if len(out.Paths) > 0 {
				errorf("output %s is of kind %s and names paths; only an output of kind paths has them", out.Name, out.Kind)
			}
		default:
			errorf("output %s is of kind %q; an output is paths, image or value", out.Name, out.Kind)
		}
	}
	services := map[string]bool{}
	for i, svc := range task.Services {
		switch {
		case !taskNamePattern.MatchString(svc.Name):
			errorf("services[%d] is named %q; a service container's name is lowercase words joined by hyphens", i, svc.Name)
		case services[svc.Name]:
			errorf("service container %s is declared twice", svc.Name)
		}
		services[svc.Name] = true
		if !taskServiceImagePattern.MatchString(svc.Image) {
			errorf("service container %s runs image %q; an image is named by its digest, <repository>@sha256:<64 hex>, since a tag names no bytes", svc.Name, svc.Image)
		}
	}
	for _, name := range keysOf(task.Env) {
		if !taskEnvPattern.MatchString(name) {
			errorf("env names the variable %q; a name is letters, digits and underscores, and starts with no digit", name)
		}
	}
	if task.Image != "" && task.OwnImage && !taskImagePattern.MatchString(task.Image) {
		errorf("image is %q; an image is named by its digest, sha256:<64 hex>, since a tag names no bytes", task.Image)
	}
	if task.Platform != "" && task.OwnPlatform && !taskPlatformPattern.MatchString(task.Platform) {
		errorf("platform is %q; a platform is an operating system and an architecture, as linux/amd64", task.Platform)
	}
	if task.Hermetic {
		if task.Image == "" {
			errorf("is hermetic and has no image; its tools would be a machine's, so its inputs would not decide its result. Give @tasks an image, give the task one, or say hermetic: false")
		}
		if task.Platform == "" {
			errorf("is hermetic and has no platform; its key would not say where it ran. Give @tasks a platform, give the task one, or say hermetic: false")
		}
	}
}

// verifyTaskEdges checks one task's uses and after.
func verifyTaskEdges(task *ir.Task, byName map[string]*ir.Task, errorf func(string, ...any)) {
	used := map[string]bool{}
	for _, use := range task.Uses {
		other := byName[use.Task]
		edge := use.Task + "." + use.Output
		switch {
		case use.Task == task.Name:
			errorf("uses its own output %s", use.Output)
			continue
		case other == nil:
			errorf("uses output %s of %s, which is no task of this schema", use.Output, use.Task)
			continue
		case used[edge]:
			errorf("uses output %s of task %s twice", use.Output, use.Task)
			continue
		}
		used[edge] = true
		if _, ok := other.Output(use.Output); !ok {
			errorf("uses output %s of task %s, which declares no such output (declared: %s)", use.Output, use.Task, outputNames(other))
		}
		if task.Hermetic && !other.Hermetic {
			errorf("is hermetic and uses output %s of task %s, which is not; the key of %s would not decide what it read. Say hermetic: false here too, or make %s hermetic", use.Output, use.Task, task.Name, use.Task)
		}
	}
	followed := map[string]bool{}
	for _, after := range task.After {
		switch {
		case after == task.Name:
			errorf("comes after itself")
		case byName[after] == nil:
			errorf("comes after %s, which is no task of this schema", after)
		case followed[after]:
			errorf("names task %s twice in after", after)
		}
		followed[after] = true
	}
}

// outputNames lists a task's outputs for an error message.
func outputNames(task *ir.Task) string {
	if len(task.Outputs) == 0 {
		return "none"
	}
	names := make([]string, len(task.Outputs))
	for i, out := range task.Outputs {
		names[i] = out.Name
	}
	return strings.Join(names, ", ")
}

// taskCycles returns each cycle the tasks' edges hold, uses and after
// together, as the names along it with the first repeated at the end. The
// tasks are visited by name, so the result is the same on every run.
func taskCycles(list []*ir.Task, byName map[string]*ir.Task) [][]string {
	const (
		unseen = iota
		open
		done
	)
	state := map[string]int{}
	var stack []string
	var cycles [][]string
	var visit func(name string)
	visit = func(name string) {
		state[name] = open
		stack = append(stack, name)
		task := byName[name]
		var next []string
		for _, use := range task.Uses {
			next = append(next, use.Task)
		}
		next = append(next, task.After...)
		sort.Strings(next)
		for _, other := range next {
			if byName[other] == nil || other == name {
				continue
			}
			switch state[other] {
			case unseen:
				visit(other)
			case open:
				for i, on := range stack {
					if on == other {
						cycle := append(append([]string{}, stack[i:]...), other)
						cycles = append(cycles, cycle)
						break
					}
				}
			}
		}
		stack = stack[:len(stack)-1]
		state[name] = done
	}
	for _, task := range list {
		if byName[task.Name] == task && state[task.Name] == unseen {
			visit(task.Name)
		}
	}
	return cycles
}

// rawObject encodes a decoded JSON object, an empty one as none: the data
// forms leave an empty object out, so the two are one.
func rawObject(value map[string]any) (json.RawMessage, error) {
	if len(value) == 0 {
		return nil, nil
	}
	return json.Marshal(value)
}

// pathInputSpec is the JSON Schema of a path input's spec.
var pathInputSpec = json.RawMessage(`{
	"type": "object",
	"required": ["paths"],
	"additionalProperties": false,
	"properties": {
		"paths": {"type": "array", "minItems": 1, "items": {"type": "string", "minLength": 1}},
		"exclude": {"type": "array", "items": {"type": "string", "minLength": 1}}
	}
}`)

// TaskPathSpec is a path input's spec: the paths and globs it covers, and
// those it leaves out.
type TaskPathSpec struct {
	Paths   []string `json:"paths"`
	Exclude []string `json:"exclude,omitempty"`
}

// PathInputSpec returns the JSON Schema of a path input's spec, which the
// core's path resolver registers with.
func PathInputSpec() json.RawMessage { return bytes.Clone(pathInputSpec) }
