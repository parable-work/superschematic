// Package yamlreader is the YAML frontend: *.schema.yaml -> Schema IR.
//
// The YAML format is the same IR-mirroring shape as the JSON format, in YAML
// syntax. The reader works on gopkg.in/yaml.v3 yaml.Node values directly so
// native '#' head and line comments survive a read -> write round trip: they
// are mapped onto the IR's Comment metadata (see comments.go).
package yamlreader

import (
	"encoding/json"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"

	"github.com/parable-work/superschematic/internal/loader/schemafile"
	"github.com/parable-work/superschematic/internal/registry"
)

// Read validates and decodes a YAML schema payload against the core
// registry. The source argument names the payload origin (a file path or a
// runtime-mode label) for error messages.
//
// The node tree is converted to JSON and handed to the same validation and
// decode pipeline the JSON reader uses, then native comments are mapped from
// the node tree onto the decoded document.
func Read(data []byte, source string) (*schemafile.Document, error) {
	return ReadWith(data, source, nil)
}

// ReadWith is Read against a registry (nil for the core one): its kinds,
// extensions and documents decide what the payload may carry.
func ReadWith(data []byte, source string, reg *registry.Registry) (*schemafile.Document, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("%s: %w", source, err)
	}
	if root.Kind != yaml.DocumentNode || len(root.Content) == 0 {
		return nil, fmt.Errorf("%s: empty schema document", source)
	}
	mapping := root.Content[0]

	var raw any
	if err := mapping.Decode(&raw); err != nil {
		return nil, fmt.Errorf("%s: %w", source, err)
	}
	jsonBytes, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: converting YAML to JSON: %w", source, err)
	}

	doc, err := schemafile.DecodeWith(jsonBytes, source, reg)
	if err != nil {
		return nil, err
	}

	payload, _ := raw.(map[string]any)
	applyComments(&root, mapping, doc, schemafile.IsDocumentFormWith(payload, reg))
	return doc, nil
}

// ReadFile reads one *.schema.yaml file. The source argument is the path
// used in error messages and definition Owner stamps -- conventionally the
// path relative to the service directory.
func ReadFile(path, source string) (*schemafile.Document, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", source, err)
	}
	return Read(data, source)
}

// ReadFileWith is ReadFile against a registry.
func ReadFileWith(path, source string, reg *registry.Registry) (*schemafile.Document, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", source, err)
	}
	return ReadWith(data, source, reg)
}
