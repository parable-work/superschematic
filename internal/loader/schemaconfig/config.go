// Package schemaconfig owns the service configuration contract shared by the
// three schema frontends: the SchemaConfig struct, and validation of the
// JSON/YAML config forms (schema.config.json, schema.config.yaml) against
// the JSON Schema generated from @superschematic/schema-config.
//
// The TypeScript form (schema.config.ts) is read statically by the tsreader
// off the defineConfig({...}) object literal; the data forms decode directly
// into SchemaConfig here. Either way the shape contract is the same package:
// @superschematic/schema-config declares it, gen-json-schema projects it to the
// embedded JSON Schema, and this package enforces it.
package schemaconfig

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	validator "github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"

	ir "github.com/parable-work/superschematic/ir"
)

// definitionBytes is the JSON Schema generated from the SchemaConfigDocument
// type in @superschematic/schema-config. Regenerate with:
//
//	cd packages/schema-config && bun run gen-json-schema
//
//go:embed schema-config.schema.json
var definitionBytes []byte

// ServiceDependency names another schema service this service depends on.
type ServiceDependency struct {
	// Name is the dependency's service name.
	Name string `json:"name" yaml:"name"`

	// Kind is the dependency's schema kind.
	Kind ir.SchemaKind `json:"kind" yaml:"kind"`
}

// SchemaConfig is the service configuration extracted from
// schema.config.{ts,json,yaml}. The TypeScript form is read statically off
// the defineConfig({...}) object literal -- never executed. The JSON and YAML
// forms decode directly into this struct after JSON Schema validation.
type SchemaConfig struct {
	// Name is the service name (e.g., "web-db").
	Name string `json:"name" yaml:"name"`

	// Kind classifies the schema and drives role assignment and the
	// kind/import rules during the walk.
	Kind ir.SchemaKind `json:"kind" yaml:"kind"`

	// Public marks a service whose API is exposed publicly.
	Public bool `json:"public,omitempty" yaml:"public,omitempty"`

	// AuthDB names the DB service used for authentication, when set.
	AuthDB string `json:"authDb,omitempty" yaml:"authDb,omitempty"`

	// Dependencies lists the services this service imports types from. The
	// JSON/YAML forms carry this as an explicit array: there is no import
	// system in the data forms to derive it from.
	Dependencies []ServiceDependency `json:"dependencies,omitempty" yaml:"dependencies,omitempty"`

	// Outputs is the raw outputs configuration. The v2 generators
	// own its typed interpretation.
	Outputs map[string]any `json:"outputs,omitempty" yaml:"outputs,omitempty"`
}

// Definition returns the JSON Schema the data config forms validate against.
// This is what `superschematic json-schema --config` emits.
func Definition() []byte {
	return definitionBytes
}

var (
	compileOnce sync.Once
	compiled    *validator.Schema
	compileErr  error
)

func compiledSchema() (*validator.Schema, error) {
	compileOnce.Do(func() {
		resource, err := validator.UnmarshalJSON(bytes.NewReader(definitionBytes))
		if err != nil {
			compileErr = fmt.Errorf("decoding embedded schema-config JSON Schema: %w", err)
			return
		}
		compiler := validator.NewCompiler()
		if err := compiler.AddResource("superschematic://schema-config.schema.json", resource); err != nil {
			compileErr = fmt.Errorf("registering schema-config JSON Schema: %w", err)
			return
		}
		compiled, compileErr = compiler.Compile("superschematic://schema-config.schema.json")
	})
	return compiled, compileErr
}

// KindSet is the set of schema kinds a config may name: the caller's
// registry (*registry.Registry satisfies it). This package cannot import
// registry, and there is no closed set of kinds here, so the readers take
// the set as an interface. Kinds is what the unknown-kind diagnostic lists.
type KindSet interface {
	KnowsKind(kind ir.SchemaKind) bool
	Kinds() []string
}

// ReadFile loads and validates the data-form service config: it prefers
// schema.config.json, then schema.config.yaml. It returns os.ErrNotExist
// (wrapped) when neither exists, letting callers fall through to the
// TypeScript form.
func ReadFile(servicePath string, known KindSet) (*SchemaConfig, error) {
	jsonPath := filepath.Join(servicePath, "schema.config.json")
	if data, err := os.ReadFile(jsonPath); err == nil {
		return decodeJSON(data, "schema.config.json", known)
	}
	yamlPath := filepath.Join(servicePath, "schema.config.yaml")
	if data, err := os.ReadFile(yamlPath); err == nil {
		var raw any
		if err := yaml.Unmarshal(data, &raw); err != nil {
			return nil, fmt.Errorf("schema.config.yaml: %w", err)
		}
		jsonBytes, err := json.Marshal(raw)
		if err != nil {
			return nil, fmt.Errorf("schema.config.yaml: converting to JSON: %w", err)
		}
		return decodeJSON(jsonBytes, "schema.config.yaml", known)
	}
	return nil, fmt.Errorf("service %s has no schema.config.{json,yaml}: %w", servicePath, os.ErrNotExist)
}

// decodeJSON validates a config payload against the generated JSON Schema,
// then strict-decodes it.
func decodeJSON(data []byte, source string, known KindSet) (*SchemaConfig, error) {
	sch, err := compiledSchema()
	if err != nil {
		return nil, err
	}
	instance, err := validator.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", source, err)
	}
	if err := sch.Validate(instance); err != nil {
		return nil, fmt.Errorf("%s: %w", source, err)
	}

	cfg := &SchemaConfig{}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(cfg); err != nil {
		return nil, fmt.Errorf("%s: %w", source, err)
	}
	return ValidateShapeWith(cfg, known)
}

// ValidateShapeWith checks the required config fields, accepting the kinds
// in known. Both forms use it with the registry's kinds, so an
// extension-registered kind loads without touching this package. The JSON
// Schema the data forms are checked against first leaves kind open (any
// string), and the TS form's kind type admits any string too, so this is
// the one place a typo in kind is caught; the error lists the kinds the
// binary knows so the author can tell a typo from a missing extension.
func ValidateShapeWith(cfg *SchemaConfig, known KindSet) (*SchemaConfig, error) {
	if cfg.Name == "" {
		return nil, fmt.Errorf("schema config is missing a name")
	}
	if !known.KnowsKind(cfg.Kind) {
		return nil, fmt.Errorf("schema config for %s has unknown kind %q (registered kinds: %s)", cfg.Name, cfg.Kind, strings.Join(known.Kinds(), ", "))
	}
	for _, dep := range cfg.Dependencies {
		if dep.Name == "" || !known.KnowsKind(dep.Kind) {
			return nil, fmt.Errorf("schema config for %s has a malformed dependency (name %q, kind %q; registered kinds: %s)", cfg.Name, dep.Name, dep.Kind, strings.Join(known.Kinds(), ", "))
		}
	}
	return cfg, nil
}
