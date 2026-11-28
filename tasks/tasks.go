// Package tasks is the shape of the task file and of the plan (D203): what
// a Tasks schema's build writes into the repository, and what `tasks plan`
// prints for a commit. A reader in Go decodes them with this package. A
// reader in another language generates its types from the JSON Schemas
// FileSchema and PlanSchema return, which `json-schema --task-file` and
// `--task-plan` print, so nobody writes a mirror of either shape by hand.
//
// The package imports nothing of the compiler, so a forge links it without
// the generators.
package tasks

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

// FileVersion is the version of the task file this package writes and the
// newest it reads. PlanVersion is the plan's. A reader refuses a newer
// version and an unknown field, and names it (Decode, DecodePlan).
const (
	FileVersion = 1
	PlanVersion = 1
)

// KeyVersion is the version of the input key's definition. A change to
// what a key covers, or to how it is encoded, raises it, and no key of one
// version equals a key of another.
const KeyVersion = 2

// FileName is the task file's name in its install directory.
const FileName = "tasks.json"

// The input kinds the core resolves.
const (
	InputPath   = "path"
	InputSchema = "schema"
)

// The output kinds.
const (
	OutputPaths = "paths"
	OutputImage = "image"
	OutputValue = "value"
)

// File is the task file: a repository's tasks at a commit.
type File struct {
	// Version is FileVersion.
	Version int `json:"version"`

	// Image is the digest of the image a task runs from unless it names
	// its own, and the image that holds the plan command. Platform is the
	// platform a task runs on unless it names its own.
	Image    string `json:"image,omitempty"`
	Platform string `json:"platform,omitempty"`

	// Tasks are the tasks by name.
	Tasks map[string]Task `json:"tasks"`

	// Sections holds, by section name, the part of each section that
	// belongs to the file. Only the reader that registered a section reads
	// it, and a reader skips a section it did not register.
	Sections map[string]json.RawMessage `json:"sections,omitempty"`
}

// Task is one unit of work. Every field but After and Sections is a work
// field: the input key reads it.
type Task struct {
	// Run is the command, an argument list. Nothing starts a shell.
	Run []string `json:"run"`

	// Inputs are what the task reads of the commit.
	Inputs []Input `json:"inputs,omitempty"`

	// Uses are the outputs of other tasks this one reads. A runner puts
	// each in place, from the bytes stored under the other task's key,
	// before the command starts.
	Uses []Use `json:"uses,omitempty"`

	// After names the tasks that must pass before this one starts, and
	// from which it reads nothing.
	After []string `json:"after,omitempty"`

	// Outputs are the named results the task declares.
	Outputs []Output `json:"outputs,omitempty"`

	// Services are the service containers the task runs beside.
	Services []Service `json:"services,omitempty"`

	// Env is the environment the command starts with.
	Env map[string]string `json:"env,omitempty"`

	// Image and Platform replace the file's for this task.
	Image    string `json:"image,omitempty"`
	Platform string `json:"platform,omitempty"`

	// Hermetic is false for a task whose result its inputs do not decide.
	// It has no key and is never reused, and its outputs are a run's.
	Hermetic bool `json:"hermetic"`

	// Sections holds, by section name, the part of each section that
	// belongs to this task.
	Sections map[string]json.RawMessage `json:"sections,omitempty"`
}

// Input is one input: the kind of the resolver that turns it into tracked
// files, and the spec that resolver reads. Kind and Spec are open: a
// resolver an extension registers adds a kind and checks its own spec.
type Input struct {
	Kind string          `json:"kind"`
	Spec json.RawMessage `json:"spec,omitempty"`
}

// Use is one output of another task.
type Use struct {
	Task   string `json:"task"`
	Output string `json:"output"`
}

// Output is one named result: the files at Paths, an image's digest, or a
// value.
type Output struct {
	Name  string   `json:"name"`
	Kind  string   `json:"kind"`
	Paths []string `json:"paths,omitempty"`
}

// Service is one service container, by its image's digest.
type Service struct {
	Name  string `json:"name"`
	Image string `json:"image"`
}

// Work is the work fields of a task with the image and the platform in
// effect: what the work hash covers.
type Work struct {
	Run      []string          `json:"run"`
	Inputs   []Input           `json:"inputs,omitempty"`
	Uses     []Use             `json:"uses,omitempty"`
	Outputs  []Output          `json:"outputs,omitempty"`
	Services []Service         `json:"services,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
	Image    string            `json:"image,omitempty"`
	Platform string            `json:"platform,omitempty"`
	Hermetic bool              `json:"hermetic"`
}

// ImageOf is the image the task named name runs from: its own, or the
// file's.
func (f *File) ImageOf(name string) string {
	if image := f.Tasks[name].Image; image != "" {
		return image
	}
	return f.Image
}

// PlatformOf is the platform the task named name runs on: its own, or the
// file's.
func (f *File) PlatformOf(name string) string {
	if platform := f.Tasks[name].Platform; platform != "" {
		return platform
	}
	return f.Platform
}

// Work returns the work fields of the task named name.
func (f *File) Work(name string) Work {
	task := f.Tasks[name]
	return Work{
		Run:      task.Run,
		Inputs:   task.Inputs,
		Uses:     task.Uses,
		Outputs:  task.Outputs,
		Services: task.Services,
		Env:      task.Env,
		Image:    f.ImageOf(name),
		Platform: f.PlatformOf(name),
		Hermetic: task.Hermetic,
	}
}

// WorkHash is the hex SHA-256 of the task's work fields in canonical JSON.
// After and every section are outside it, so a change to when a task runs
// or to what a forge reads of it changes no key.
func (f *File) WorkHash(name string) (string, error) {
	if _, ok := f.Tasks[name]; !ok {
		return "", fmt.Errorf("task file has no task %q", name)
	}
	data, err := Canonical(f.Work(name))
	if err != nil {
		return "", fmt.Errorf("task %s: %w", name, err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// Names returns the tasks' names, sorted.
func (f *File) Names() []string {
	names := make([]string, 0, len(f.Tasks))
	for name := range f.Tasks {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Encode writes the file as it is committed: keys sorted, a two-space
// indent and a trailing newline. The same file gives the same bytes on
// every machine.
func (f *File) Encode() ([]byte, error) {
	return indented(f)
}

// Decode reads a task file. It refuses a version newer than FileVersion
// and a field this version does not know, and names it: a file a newer
// compiler wrote says more than this reader would act on.
func Decode(data []byte) (*File, error) {
	if err := checkVersion(data, "task file", FileVersion); err != nil {
		return nil, err
	}
	var f File
	if err := strict(data, &f); err != nil {
		return nil, fmt.Errorf("task file: %w", err)
	}
	if err := validate(fileSchema, data); err != nil {
		return nil, fmt.Errorf("task file: %w", err)
	}
	return &f, nil
}

// Plan is what `tasks plan` prints for a commit: each task with its work
// hash, its key and its edges, in an order that runs every task after the
// tasks it uses or follows.
type Plan struct {
	// Version is PlanVersion.
	Version int `json:"version"`

	// KeyVersion names the key definition and the repository's object
	// format, as `2:sha1`. Keys of two key versions never compare equal.
	KeyVersion string `json:"keyVersion"`

	// Commit is the commit the plan is for. Base is the commit it lands
	// on, when the plan was asked for one.
	Commit string `json:"commit"`
	Base   string `json:"base,omitempty"`

	// Order lists the tasks' names so that each comes after the tasks it
	// uses and the tasks it follows.
	Order []string `json:"order"`

	// Tasks are the planned tasks by name.
	Tasks map[string]PlanTask `json:"tasks"`

	// Sections is the file's sections, as the file holds them.
	Sections map[string]json.RawMessage `json:"sections,omitempty"`
}

// PlanTask is one task of a plan.
type PlanTask struct {
	// WorkHash is the hash of the task's work fields at the commit.
	// BaseWorkHash is the hash at the base, when the plan has a base and
	// the two differ; the key then covers both, and the inputs of both.
	WorkHash     string `json:"workHash"`
	BaseWorkHash string `json:"baseWorkHash,omitempty"`

	// Key is the input key, or nil for a task that is not hermetic.
	Key *string `json:"key"`

	// Inputs are the digests of the task's inputs, in the task's order.
	Inputs []PlanInput `json:"inputs,omitempty"`

	// Uses and After are the task's edges, as the file holds them.
	Uses  []Use    `json:"uses,omitempty"`
	After []string `json:"after,omitempty"`

	// Sections is the task's part of each section.
	Sections map[string]json.RawMessage `json:"sections,omitempty"`
}

// PlanInput is one resolved input: the digest of the tracked files it
// matched, and how many.
type PlanInput struct {
	Kind   string `json:"kind"`
	Digest string `json:"digest"`
	Files  int    `json:"files"`
}

// Encode writes the plan with its keys sorted, a two-space indent and a
// trailing newline.
func (p *Plan) Encode() ([]byte, error) {
	return indented(p)
}

// DecodePlan reads a plan, with Decode's refusals.
func DecodePlan(data []byte) (*Plan, error) {
	if err := checkVersion(data, "plan", PlanVersion); err != nil {
		return nil, err
	}
	var p Plan
	if err := strict(data, &p); err != nil {
		return nil, fmt.Errorf("plan: %w", err)
	}
	if err := validate(planSchema, data); err != nil {
		return nil, fmt.Errorf("plan: %w", err)
	}
	return &p, nil
}

// Canonical encodes v as canonical JSON: object keys sorted, no space
// between tokens, no HTML escaping, numbers as written. It is what the
// work hash is taken over.
func Canonical(v any) ([]byte, error) {
	generic, err := normal(v)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(generic); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// normal turns v into maps, lists and scalars, so that encoding it sorts
// every object's keys, a struct's and a raw message's alike.
func normal(v any) (any, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var generic any
	if err := dec.Decode(&generic); err != nil {
		return nil, err
	}
	return generic, nil
}

func indented(v any) ([]byte, error) {
	generic, err := normal(v)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(generic); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// strict decodes data into v and refuses a field v does not hold.
func strict(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.More() {
		return fmt.Errorf("more than one JSON value")
	}
	return nil
}

// checkVersion refuses a document with no version, or a newer one than
// this package reads.
func checkVersion(data []byte, what string, newest int) error {
	var head struct {
		Version *int `json:"version"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	switch {
	case head.Version == nil:
		return fmt.Errorf("%s has no version", what)
	case *head.Version > newest:
		return fmt.Errorf("%s has version %d, and this reader reads up to version %d: update the reader before the file", what, *head.Version, newest)
	case *head.Version < 1:
		return fmt.Errorf("%s has version %d; versions start at 1", what, *head.Version)
	}
	return nil
}
