package stackinit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// deployableKinds are the deployable kinds a target types settings for,
// in the order @superschematic/stack's LocalTarget lists them.
var deployableKinds = []ir.DeployableKind{
	ir.DeployableServer,
	ir.DeployableDatabase,
	ir.DeployableJob,
	ir.DeployableSite,
	ir.DeployableBucket,
	ir.DeployableWorker,
}

var identifier = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*$`)

// TargetTypes is the file that augments @superschematic/stack's Targets
// with a target's values and the settings of each deployable kind it
// places, as tsc checks an environment against them (docs/stack-model.md,
// section 4.3). Each type is read from the JSON Schema the target or its
// platform registers, which the loader checks the same values against, so
// the file says what the binary that wrote it checks.
func TargetTypes(target registry.TargetSpec, reg *registry.Registry, names naming.Naming) ([]byte, error) {
	var b bytes.Buffer
	fmt.Fprintf(&b, `// The authoring types of the %s target: its values and the settings of each
// deployable kind, read from the JSON Schemas the target and its platforms
// register, which the loader checks the same values against
// (docs/stack-model.md, section 4.3). superschematic stack init wrote it.
export {};

declare module %s {
  interface Targets {
    %s: {
`, target.Name, quote(names.Specifier("@superschematic/stack")), propertyName(target.Name))
	values, err := schemaType(target.Values)
	if err != nil {
		return nil, fmt.Errorf("target %s: values: %w", target.Name, err)
	}
	fmt.Fprintf(&b, "      values: %s;\n", values)
	for _, kind := range deployableKinds {
		platform, ok := target.Platforms[kind]
		if !ok {
			continue
		}
		spec, ok := reg.Platform(platform)
		if !ok {
			return nil, fmt.Errorf("target %s places %s deployables on platform %s, which is not registered", target.Name, kind, platform)
		}
		settings, err := schemaType(spec.Settings)
		if err != nil {
			return nil, fmt.Errorf("platform %s: settings: %w", platform, err)
		}
		fmt.Fprintf(&b, "      %s: %s;\n", kind, settings)
	}
	b.WriteString("    };\n  }\n}\n")
	return b.Bytes(), nil
}

// schemaType renders a JSON Schema as a TypeScript type: an object's
// properties in the schema's order, each optional unless required, a
// string, number or boolean, an enum's or a const's literals, an array of
// its items, and unknown for the rest. A nil schema, which accepts
// nothing, is an object with no keys.
func schemaType(raw json.RawMessage) (string, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return "{}", nil
	}
	var s jsonSchema
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", err
	}
	return s.typeScript()
}

// jsonSchema is the part of a JSON Schema schemaType reads. Properties
// keeps the order the schema lists them in.
type jsonSchema struct {
	Type       any               `json:"type"`
	Enum       []any             `json:"enum"`
	Const      any               `json:"const"`
	Properties orderedProperties `json:"properties"`
	Required   []string          `json:"required"`
	Items      *jsonSchema       `json:"items"`
	OneOf      []*jsonSchema     `json:"oneOf"`
	AnyOf      []*jsonSchema     `json:"anyOf"`
}

// orderedProperties is a schema's properties, in order.
type orderedProperties struct {
	present bool
	names   []string
	schemas map[string]*jsonSchema
}

func (o *orderedProperties) UnmarshalJSON(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return fmt.Errorf("properties: want an object")
	}
	o.present = true
	o.schemas = map[string]*jsonSchema{}
	for {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		if tok == json.Delim('}') {
			break
		}
		name, ok := tok.(string)
		if !ok {
			return fmt.Errorf("properties: a key is not a string")
		}
		var s jsonSchema
		if err := dec.Decode(&s); err != nil {
			return fmt.Errorf("property %s: %w", name, err)
		}
		o.names = append(o.names, name)
		o.schemas[name] = &s
	}
	if _, err := dec.Token(); err != io.EOF {
		return fmt.Errorf("properties: trailing data")
	}
	return nil
}

func (s *jsonSchema) typeScript() (string, error) {
	switch {
	case s.Enum != nil:
		return literals(s.Enum)
	case s.Const != nil:
		return literals([]any{s.Const})
	case s.OneOf != nil:
		return union(s.OneOf)
	case s.AnyOf != nil:
		return union(s.AnyOf)
	}
	var types []string
	switch t := s.Type.(type) {
	case string:
		types = []string{t}
	case []any:
		for _, v := range t {
			if name, ok := v.(string); ok {
				types = append(types, name)
			}
		}
	}
	if len(types) == 0 && s.Properties.present {
		types = []string{"object"}
	}
	var out []string
	for _, t := range types {
		rendered, err := s.typeOf(t)
		if err != nil {
			return "", err
		}
		out = append(out, rendered)
	}
	if len(out) == 0 {
		return "unknown", nil
	}
	return strings.Join(out, " | "), nil
}

func (s *jsonSchema) typeOf(name string) (string, error) {
	switch name {
	case "string", "boolean", "number":
		return name, nil
	case "integer":
		return "number", nil
	case "null":
		return "null", nil
	case "array":
		if s.Items == nil {
			return "unknown[]", nil
		}
		item, err := s.Items.typeScript()
		if err != nil {
			return "", err
		}
		if strings.Contains(item, " | ") {
			item = "(" + item + ")"
		}
		return item + "[]", nil
	case "object":
		if len(s.Properties.names) == 0 {
			if s.Properties.present {
				return "{}", nil
			}
			return "{ [key: string]: unknown }", nil
		}
		parts := make([]string, 0, len(s.Properties.names))
		for _, prop := range s.Properties.names {
			t, err := s.Properties.schemas[prop].typeScript()
			if err != nil {
				return "", fmt.Errorf("property %s: %w", prop, err)
			}
			optional := "?"
			for _, required := range s.Required {
				if required == prop {
					optional = ""
				}
			}
			parts = append(parts, propertyName(prop)+optional+": "+t)
		}
		return "{ " + strings.Join(parts, "; ") + " }", nil
	}
	return "unknown", nil
}

// literals renders JSON values as a union of TypeScript literal types.
func literals(values []any) (string, error) {
	parts := make([]string, len(values))
	for i, v := range values {
		data, err := json.Marshal(v)
		if err != nil {
			return "", err
		}
		parts[i] = string(data)
	}
	return strings.Join(parts, " | "), nil
}

func union(schemas []*jsonSchema) (string, error) {
	parts := make([]string, 0, len(schemas))
	for _, s := range schemas {
		t, err := s.typeScript()
		if err != nil {
			return "", err
		}
		parts = append(parts, t)
	}
	return strings.Join(parts, " | "), nil
}
