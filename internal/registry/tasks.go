package registry

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"

	validator "github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/parable-work/superschematic/tasks"
)

// The Tasks kind's two seams (D203): an input resolver turns a task's
// input into tracked files of a commit, and a task section is a part of
// the task file that one forge owns and reads. The core computes with the
// first and only copies the second.

// TaskTreeEntry is one tracked entry of a commit's tree.
type TaskTreeEntry struct {
	// Path is the entry's path from the repository root, slash-separated.
	Path string
	// Mode is the entry's mode as git writes it: 100644 a file, 100755 an
	// executable file, 120000 a symbolic link, 160000 a submodule.
	Mode string
	// ID is the entry's object id, in the repository's object format.
	ID string
}

// TaskTree is a commit as an input resolver reads it: its tracked entries
// and their bytes. It holds nothing of a working tree, so a file that is
// generated, ignored or not yet committed is not in it.
type TaskTree interface {
	// Entries returns the tracked entries sorted by path. The caller must
	// not change the slice.
	Entries() []TaskTreeEntry
	// Entry returns the entry at path.
	Entry(path string) (TaskTreeEntry, bool)
	// Read returns the bytes of the tracked file at path. It refuses a
	// path the commit does not hold, a submodule, and a file larger than
	// the tree's limit for one manifest.
	Read(path string) ([]byte, error)
}

// TaskResolveRequest is what an input resolver receives.
type TaskResolveRequest struct {
	// Tree is the commit.
	Tree TaskTree
	// Task is the name of the task the input belongs to, for errors.
	Task string
	// Spec is the input's spec, which has passed the resolver's schema.
	Spec json.RawMessage
}

// TaskResolution is what an input resolves to.
type TaskResolution struct {
	// Paths are the tracked files the input covers, each an entry of the
	// tree, in any order and with repeats allowed.
	Paths []string
	// Generated are the paths a manifest points at that no tracked file
	// holds. The plan requires each to be covered by an output the task
	// uses, and fails with the path and its manifest when one is not.
	Generated []TaskGeneratedPath
}

// TaskGeneratedPath is a path a manifest of the commit names and the
// commit does not hold, such as a workspace member a build writes.
type TaskGeneratedPath struct {
	// Path is the path, a file or a directory, from the repository root.
	Path string
	// From is the tracked manifest that names it.
	From string
}

// TaskResolverSpec registers an input resolver: the code that turns one
// kind of input into tracked files (D203).
//
// A resolver runs before anything is generated, on a commit somebody else
// may have written, and the keys it feeds decide what a forge reuses. So it
// reads tracked files only, through req.Tree; it runs no build tool and
// starts no process; it uses no network; and it executes nothing the tree
// holds. A manifest that names a path the tree lacks is reported in
// Generated, never read from a working tree.
type TaskResolverSpec struct {
	// Kind is the registry key and the `kind` an input names (`path`).
	Kind string

	// Extension is the registering extension's Name(); "" for core.
	Extension string

	// Spec is the JSON Schema of the input's spec. nil means the kind
	// takes no spec.
	Spec json.RawMessage

	// Resolve returns the tracked files the input covers. It is pure: one
	// tree and one spec give one answer.
	Resolve func(TaskResolveRequest) (TaskResolution, error)

	compiledSpec *validator.Schema
}

// TaskSectionRequest is what a task section's Verify receives: the parts
// of the section a schema holds, with the names of the schema's tasks.
type TaskSectionRequest struct {
	// File is the section's part that belongs to the file, or nil.
	File json.RawMessage
	// Tasks holds the section's part of each task that has one, by task
	// name.
	Tasks map[string]json.RawMessage
	// Names are the names of every task of the schema, sorted.
	Names []string
}

// TaskSectionSpec registers a task section: a part of the task file that
// one forge owns (D203). The core checks each part against its schema,
// runs Verify, copies the parts into the file under `sections.<name>`, and
// reads nothing in them. A forge's triggers, tiers, runner placement and
// timeouts are a section; the core holds none of them.
type TaskSectionSpec struct {
	// Name is the registry key and the key under `sections`.
	Name string

	// Extension is the registering extension's Name(). It is "" for a
	// section the naming file registers as data.
	Extension string

	// File is the JSON Schema of the section's part that belongs to the
	// file, and Task the schema of its part of one task. nil means the
	// section has no such part, and a schema that gives one is refused.
	File json.RawMessage
	Task json.RawMessage

	// Verify checks what a JSON Schema cannot say, such as a trigger that
	// names a tier the section does not declare. It reports through r.
	// nil adds no check; a section registered as data has none, and its
	// forge checks the rest when it reads the file.
	Verify func(req TaskSectionRequest, r VerifyReporter)

	compiledFile *validator.Schema
	compiledTask *validator.Schema
}

// taskSpecs holds the Tasks kind's registrations.
type taskSpecs struct {
	resolvers map[string]TaskResolverSpec
	sections  map[string]TaskSectionSpec
}

func newTaskSpecs() taskSpecs {
	return taskSpecs{resolvers: map[string]TaskResolverSpec{}, sections: map[string]TaskSectionSpec{}}
}

var taskNamePattern = regexp.MustCompile(tasks.NamePattern)

// RegisterTaskResolver adds an input resolver. It refuses a malformed or
// duplicate kind, a missing Resolve and a Spec that does not compile.
func (r *Registry) RegisterTaskResolver(spec TaskResolverSpec) error {
	if err := r.registrable("task resolver " + spec.Kind); err != nil {
		return err
	}
	if !taskNamePattern.MatchString(spec.Kind) {
		return fmt.Errorf("registry: task resolver kind %q must be lowercase words joined by hyphens", spec.Kind)
	}
	if _, dup := r.tasks.resolvers[spec.Kind]; dup {
		return fmt.Errorf("registry: task resolver %q is already registered", spec.Kind)
	}
	if spec.Resolve == nil {
		return fmt.Errorf("registry: task resolver %q has no Resolve function", spec.Kind)
	}
	if spec.Spec != nil {
		compiled, err := compileSchema(spec.Spec, "superschematic://tasks/resolvers/"+spec.Kind+".json")
		if err != nil {
			return fmt.Errorf("registry: task resolver %q Spec: %w", spec.Kind, err)
		}
		spec.compiledSpec = compiled
	}
	r.tasks.resolvers[spec.Kind] = spec
	r.noteExtension(spec.Extension)
	return nil
}

// TaskResolver returns the resolver of an input kind.
func (r *Registry) TaskResolver(kind string) (TaskResolverSpec, bool) {
	spec, ok := r.tasks.resolvers[kind]
	return spec, ok
}

// TaskResolvers returns the registered input kinds, sorted.
func (r *Registry) TaskResolvers() []string { return keysOf(r.tasks.resolvers) }

// CheckTaskInput checks an input's spec against its resolver's schema. It
// refuses a kind no resolver registers, a spec for a kind that takes none,
// and a spec the schema refuses.
func (r *Registry) CheckTaskInput(kind string, spec json.RawMessage) error {
	resolver, ok := r.tasks.resolvers[kind]
	if !ok {
		return fmt.Errorf("input kind %q has no resolver (registered: %v)", kind, r.TaskResolvers())
	}
	if resolver.compiledSpec == nil {
		if len(bytes.TrimSpace(spec)) > 0 && !bytes.Equal(bytes.TrimSpace(spec), []byte("null")) {
			return fmt.Errorf("input kind %q takes no spec", kind)
		}
		return nil
	}
	if len(bytes.TrimSpace(spec)) == 0 {
		spec = json.RawMessage("null")
	}
	if err := validateRaw(resolver.compiledSpec, spec); err != nil {
		return fmt.Errorf("input kind %q spec: %w", kind, err)
	}
	return nil
}

// RegisterTaskSection adds a task section. It refuses a malformed or
// duplicate name, a section with neither part, and a schema that does not
// compile.
func (r *Registry) RegisterTaskSection(spec TaskSectionSpec) error {
	if err := r.registrable("task section " + spec.Name); err != nil {
		return err
	}
	if !taskNamePattern.MatchString(spec.Name) {
		return fmt.Errorf("registry: task section name %q must be lowercase words joined by hyphens", spec.Name)
	}
	if _, dup := r.tasks.sections[spec.Name]; dup {
		return fmt.Errorf("registry: task section %q is already registered", spec.Name)
	}
	if spec.File == nil && spec.Task == nil {
		return fmt.Errorf("registry: task section %q has neither a File nor a Task schema, so it holds nothing", spec.Name)
	}
	for part, schema := range map[string]json.RawMessage{"file": spec.File, "task": spec.Task} {
		if schema == nil {
			continue
		}
		compiled, err := compileSchema(schema, "superschematic://tasks/sections/"+spec.Name+"/"+part+".json")
		if err != nil {
			return fmt.Errorf("registry: task section %q %s schema: %w", spec.Name, part, err)
		}
		if part == "file" {
			spec.compiledFile = compiled
		} else {
			spec.compiledTask = compiled
		}
	}
	r.tasks.sections[spec.Name] = spec
	r.noteExtension(spec.Extension)
	return nil
}

// TaskSection returns a registered task section.
func (r *Registry) TaskSection(name string) (TaskSectionSpec, bool) {
	spec, ok := r.tasks.sections[name]
	return spec, ok
}

// TaskSections returns the registered task sections' names, sorted.
func (r *Registry) TaskSections() []string { return keysOf(r.tasks.sections) }

// CheckTaskSectionFile checks the part of section name that belongs to the
// file, and CheckTaskSectionTask its part of one task. Each refuses a
// section nothing registers, a part the section does not have, and a part
// its schema refuses.
func (r *Registry) CheckTaskSectionFile(name string, part json.RawMessage) error {
	return r.checkTaskSection(name, "file", part)
}

// CheckTaskSectionTask is CheckTaskSectionFile for a task's part.
func (r *Registry) CheckTaskSectionTask(name string, part json.RawMessage) error {
	return r.checkTaskSection(name, "task", part)
}

func (r *Registry) checkTaskSection(name, part string, value json.RawMessage) error {
	section, ok := r.tasks.sections[name]
	if !ok {
		return fmt.Errorf("section %q is not registered (registered: %v); an extension registers a section, or the naming file's [tasks.sections.%s] names its schemas", name, r.TaskSections(), name)
	}
	compiled, where := section.compiledFile, "for the file"
	if part == "task" {
		compiled, where = section.compiledTask, "for a task"
	}
	if compiled == nil {
		return fmt.Errorf("section %q has no part %s", name, where)
	}
	if err := validateRaw(compiled, value); err != nil {
		return fmt.Errorf("section %q: %w", name, err)
	}
	return nil
}

// validateRaw checks a JSON value against a compiled schema.
func validateRaw(schema *validator.Schema, value json.RawMessage) error {
	instance, err := validator.UnmarshalJSON(bytes.NewReader(value))
	if err != nil {
		return err
	}
	return schema.Validate(instance)
}
