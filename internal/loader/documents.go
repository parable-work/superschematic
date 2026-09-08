package loader

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	validator "github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"

	"github.com/parable-work/superschematic/internal/loader/executor"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// loadDocuments runs every registered sidecar document against the service
// (extension-model section 3.5). Discovery is by the spec's File next to
// schema.config.*: absence is not an error, presence in a schema kind the
// spec does not admit is. Each loaded document lands in Schema.Documents in
// canonical JSON, and its authoring imports join the schema's for the build
// cache. A data-form schema may carry the same document inline
// (`documents.<name>`); a sidecar file for a document the data form already
// defines is a conflict.
func loadDocuments(servicePath string, schema *ir.Schema, cfg *schemaconfig.SchemaConfig, o *loadOptions, reg *registry.Registry) error {
	for _, spec := range reg.Documents() {
		if spec.Loader == nil {
			continue
		}
		if spec.File != "" {
			if _, err := os.Stat(filepath.Join(servicePath, spec.File)); err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					continue
				}
				return fmt.Errorf("%s: %w", spec.File, err)
			}
		}
		if len(spec.Kinds) > 0 && !slices.Contains(spec.Kinds, string(schema.Kind)) {
			return fmt.Errorf("%s: document %s is not allowed in %s schemas (allowed kinds: %s)", spec.File, spec.Name, schema.Kind, strings.Join(spec.Kinds, ", "))
		}
		if _, dup := schema.Documents[spec.Name]; dup {
			return fmt.Errorf("%s: document %s is already defined by the schema files (documents.%s)", spec.File, spec.Name, spec.Name)
		}
		lc := registry.LoadContext{
			ServicePath: servicePath,
			Schema:      schema,
			Config:      cfg,
			Registry:    reg,
			Catalog:     o.schemaCatalog,
			RunModule: func(file string) (json.RawMessage, []string, error) {
				res, err := executor.RunDocument(servicePath, file, executor.WithProfiler(o.profile))
				if err != nil {
					return nil, nil, err
				}
				return res.Document, res.AuthoringImports, nil
			},
			DecodeData: func(file string, schema json.RawMessage) (json.RawMessage, error) {
				return decodeDataDocument(servicePath, file, schema)
			},
			Log: WarningWriter,
		}
		var doc json.RawMessage
		var imports []string
		if err := o.profile.Measure("loader.document."+spec.Name, func() error {
			var err error
			doc, imports, err = spec.Loader(context.Background(), lc)
			return err
		}); err != nil {
			return err
		}
		if doc == nil {
			continue
		}
		canonical, err := ir.CanonicalJSON(doc)
		if err != nil {
			return fmt.Errorf("document %s: %w", spec.Name, err)
		}
		if schema.Documents == nil {
			schema.Documents = make(map[string]json.RawMessage)
		}
		schema.Documents[spec.Name] = canonical
		schema.AuthoringImports = append(schema.AuthoringImports, imports...)
	}
	return nil
}

// decodeDataDocument reads a JSON or YAML sidecar (by extension) relative to
// the service directory, validates it against schema when one is given, and
// returns it as canonical JSON. It is the LoadContext.DecodeData the loader
// hands document loaders.
func decodeDataDocument(servicePath, file string, schema json.RawMessage) (json.RawMessage, error) {
	data, err := os.ReadFile(filepath.Join(servicePath, filepath.FromSlash(file)))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	var value any
	switch strings.ToLower(filepath.Ext(file)) {
	case ".yaml", ".yml":
		if err := yaml.Unmarshal(data, &value); err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
	default:
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.UseNumber()
		if err := dec.Decode(&value); err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		if dec.More() {
			return nil, fmt.Errorf("%s: trailing data after JSON value", file)
		}
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	if schema != nil {
		if err := validateDocument(raw, schema, file); err != nil {
			return nil, err
		}
	}
	return ir.CanonicalJSON(raw)
}

// validateDocument checks one JSON payload against a JSON Schema.
func validateDocument(raw, schema json.RawMessage, source string) error {
	resource, err := validator.UnmarshalJSON(bytes.NewReader(schema))
	if err != nil {
		return fmt.Errorf("%s: document schema: %w", source, err)
	}
	compiler := validator.NewCompiler()
	const url = "superschematic://documents/schema.json"
	if err := compiler.AddResource(url, resource); err != nil {
		return fmt.Errorf("%s: document schema: %w", source, err)
	}
	compiled, err := compiler.Compile(url)
	if err != nil {
		return fmt.Errorf("%s: document schema: %w", source, err)
	}
	instance, err := validator.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("%s: %w", source, err)
	}
	if err := compiled.Validate(instance); err != nil {
		return fmt.Errorf("%s: %w", source, err)
	}
	return nil
}
