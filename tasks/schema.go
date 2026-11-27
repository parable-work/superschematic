package tasks

import (
	"bytes"
	_ "embed"
	"fmt"
	"sync"

	validator "github.com/santhosh-tekuri/jsonschema/v6"
)

// The JSON Schemas of the task file and of the plan. Each is strict:
// `additionalProperties` is false everywhere but in the three places that
// are open by design, an input's spec, a section, and a task's part of a
// section, which the resolver or the reader that registered them checks.

//go:embed task-file.schema.json
var fileSchema []byte

//go:embed task-plan.schema.json
var planSchema []byte

// FileSchema returns the JSON Schema of the task file, which
// `json-schema --task-file` prints.
func FileSchema() []byte {
	return bytes.Clone(fileSchema)
}

// PlanSchema returns the JSON Schema of the plan, which
// `json-schema --task-plan` prints.
func PlanSchema() []byte {
	return bytes.Clone(planSchema)
}

var (
	compiledMu sync.Mutex
	compiled   = map[string]*validator.Schema{}
)

// validate checks data against one of the package's schemas.
func validate(schema, data []byte) error {
	sch, err := compile(schema)
	if err != nil {
		return err
	}
	instance, err := validator.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return err
	}
	return sch.Validate(instance)
}

func compile(schema []byte) (*validator.Schema, error) {
	compiledMu.Lock()
	defer compiledMu.Unlock()
	key := string(schema)
	if sch, ok := compiled[key]; ok {
		return sch, nil
	}
	doc, err := validator.UnmarshalJSON(bytes.NewReader(schema))
	if err != nil {
		return nil, fmt.Errorf("tasks: schema: %w", err)
	}
	c := validator.NewCompiler()
	const url = "superschematic://tasks/schema.json"
	if err := c.AddResource(url, doc); err != nil {
		return nil, fmt.Errorf("tasks: schema: %w", err)
	}
	sch, err := c.Compile(url)
	if err != nil {
		return nil, fmt.Errorf("tasks: schema: %w", err)
	}
	compiled[key] = sch
	return sch, nil
}

// The patterns the schemas hold, which the Tasks kind's verification
// applies to a schema before any file is written.
const (
	// NamePattern is a task's, an output's, a service container's and a
	// section's name: lowercase words joined by hyphens.
	NamePattern = `^[a-z][a-z0-9]*(-[a-z0-9]+)*$`
	// ImagePattern is an image's digest.
	ImagePattern = `^sha256:[0-9a-f]{64}$`
	// ServiceImagePattern is a service container's image: a repository
	// and its digest. A tag alone is refused, since it names no bytes.
	ServiceImagePattern = `^[a-z0-9][a-z0-9._/:-]*@sha256:[0-9a-f]{64}$`
	// PlatformPattern is a platform: an operating system and an
	// architecture, with a variant when there is one.
	PlatformPattern = `^[a-z0-9]+/[a-z0-9_]+(/[a-z0-9]+)?$`
	// EnvPattern is an environment variable's name.
	EnvPattern = `^[A-Za-z_][A-Za-z0-9_]*$`
)
