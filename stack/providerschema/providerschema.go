// Package providerschema is the format of an extension's pinned provider
// schemas (docs/stack-model.md, section 6.4): one file per Pulumi resource
// type the extension's platforms, connectors and DNS platforms emit, taken
// from the Pulumi provider at the version a pin file names. Each file
// records the type's input properties, the object types they reference,
// and the type's Terraform name and property renames from the bridged
// provider's published mapping.
//
// An extension embeds its pin file and the pinned files, and registers
// each as the JSON Schema of its type's properties (ResourceTypes), so
// resolution validates every node offline. Package pintool writes and
// checks the files; each extension runs it from a command of its own,
// internal/tools/providerschemas.
package providerschema

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

// PinFileName returns the name of the pin file of a Pulumi package:
// `pulumi-gcp.json` for `gcp`.
func PinFileName(pkg string) string { return "pulumi-" + pkg + ".json" }

// Pin is the content of a pin file: the provider's version, the upstream
// files the schemas come from with their digests, and the types to pin.
type Pin struct {
	// Package is the Pulumi package: `gcp`, `cloudflare`.
	Package string `json:"package"`

	// Version is the provider's release, without its `v`.
	Version string `json:"version"`

	// Sources are the upstream files the schemas are extracted from, with
	// the digest of each at Version.
	Sources []Source `json:"sources"`

	// Types are the resource type tokens pinned, sorted.
	Types []string `json:"types"`
}

// Source is one upstream file.
type Source struct {
	// Name is the file's name in the provider's repository, beside its
	// plugin's main package: schema.json or bridge-metadata.json.
	Name string `json:"name"`

	// URL is where the file is fetched from at Version.
	URL string `json:"url"`

	// SHA256 is the file's digest, hex-encoded.
	SHA256 string `json:"sha256"`
}

// Schema is one pinned resource type: the shape of section 6.4.
type Schema struct {
	// Token is the Pulumi type token (`gcp:cloudrunv2/service:Service`).
	Token string `json:"token"`

	// Terraform is the type's name in the Terraform provider the Pulumi
	// provider is bridged from, and the properties named otherwise there.
	Terraform Terraform `json:"terraform"`

	// InputProperties are the resource's inputs, as the Pulumi schema
	// declares them, with descriptions and language hints left out.
	InputProperties map[string]*Property `json:"inputProperties"`

	// RequiredInputs are the inputs a resource must set.
	RequiredInputs []string `json:"requiredInputs,omitempty"`

	// Types are the object and enum types the inputs reference, directly
	// or through other types, keyed by their `#/types/` token.
	Types map[string]*ObjectType `json:"types,omitempty"`
}

// Terraform is a type's Terraform name and property renames.
type Terraform struct {
	// Type is the Terraform resource type (`google_cloud_run_v2_service`).
	Type string `json:"type"`

	// Renames maps the path of each input property whose Terraform name
	// differs from its Pulumi name to the Terraform name. A path joins the
	// Pulumi names from the resource down with dots, through lists and
	// objects alike: `template.containers.envs` is the `env` block of a
	// container. A Terraform-family provisioner renames with it; the
	// Pulumi provisioner uses the token and names as they are.
	Renames map[string]string `json:"renames,omitempty"`
}

// Property is a property's type in the Pulumi schema format.
type Property struct {
	Type                 string      `json:"type,omitempty"`
	Ref                  string      `json:"$ref,omitempty"`
	Items                *Property   `json:"items,omitempty"`
	AdditionalProperties *Property   `json:"additionalProperties,omitempty"`
	OneOf                []*Property `json:"oneOf,omitempty"`

	// DeprecationMessage is set on a property the provider deprecates.
	DeprecationMessage string `json:"deprecationMessage,omitempty"`
}

// ObjectType is an object type, or an enum type when Enum is set.
type ObjectType struct {
	Type       string               `json:"type"`
	Properties map[string]*Property `json:"properties,omitempty"`
	Required   []string             `json:"required,omitempty"`
	Enum       []EnumValue          `json:"enum,omitempty"`
}

// EnumValue is one value of an enum type.
type EnumValue struct {
	Value any `json:"value"`
}

// TypeRefPrefix starts a reference to one of the schema's own types.
const TypeRefPrefix = "#/types/"

// ReadPin returns the pin in the file name of fsys.
func ReadPin(fsys fs.FS, name string) (*Pin, error) {
	data, err := fs.ReadFile(fsys, name)
	if err != nil {
		return nil, err
	}
	var pin Pin
	if err := json.Unmarshal(data, &pin); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return &pin, nil
}

// FileName returns the name of the file that pins a type: the token
// without its package, with each `/` and `:` a dot
// (`cloudrunv2.service.Service.json`).
func FileName(token string) string {
	_, rest, _ := strings.Cut(token, ":")
	return strings.NewReplacer("/", ".", ":", ".").Replace(rest) + ".json"
}

// Load returns the schema of a type from its file in fsys.
func Load(fsys fs.FS, token string) (*Schema, error) {
	data, err := fs.ReadFile(fsys, FileName(token))
	if err != nil {
		return nil, fmt.Errorf("resource type %s is not pinned: %w", token, err)
	}
	var s Schema
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("%s: %w", FileName(token), err)
	}
	if s.Token != token {
		return nil, fmt.Errorf("%s pins %s, not %s", FileName(token), s.Token, token)
	}
	return &s, nil
}

// ResourceTypes returns the JSON Schema of every type the pin file name
// of fsys pins, keyed by token, as a TargetSpec or a DNSPlatformSpec takes
// them. It refuses a pin at another version than version, the provider
// version the extension applies with, so what resolution validates is
// what applies.
func ResourceTypes(fsys fs.FS, name, version string) (map[string]json.RawMessage, error) {
	pin, err := ReadPin(fsys, name)
	if err != nil {
		return nil, err
	}
	if pin.Version != version {
		return nil, fmt.Errorf("%s pins pulumi-%s %s, but the extension applies %s", name, pin.Package, pin.Version, version)
	}
	out := make(map[string]json.RawMessage, len(pin.Types))
	for _, token := range pin.Types {
		s, err := Load(fsys, token)
		if err != nil {
			return nil, err
		}
		data, err := s.JSONSchema()
		if err != nil {
			return nil, err
		}
		out[token] = data
	}
	return out, nil
}

// Property returns the property at a path of input property names, read
// through lists and the object types they reference, or nil.
func (s *Schema) Property(path ...string) *Property {
	props := s.InputProperties
	var p *Property
	for i, name := range path {
		if props == nil {
			return nil
		}
		if p = props[name]; p == nil {
			return nil
		}
		if i == len(path)-1 {
			break
		}
		props = nil
		if t := s.Types[p.objectRef()]; t != nil {
			props = t.Properties
		}
	}
	return p
}

// objectRef returns the token of the object type a property, or the
// elements of a list property, reference; empty for none.
func (p *Property) objectRef() string {
	ref := p.Ref
	if p.Type == "array" && p.Items != nil {
		ref = p.Items.Ref
	}
	if !strings.HasPrefix(ref, TypeRefPrefix) {
		return ""
	}
	return strings.TrimPrefix(ref, TypeRefPrefix)
}

// JSONSchema returns the JSON Schema of a resource's properties: an object
// of the input properties that refuses any other, with the required ones
// required and each referenced type under $defs. Every object type is
// closed, as Pulumi refuses an unknown property.
func (s *Schema) JSONSchema() ([]byte, error) {
	props, err := s.properties(s.InputProperties)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", s.Token, err)
	}
	root := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(s.RequiredInputs) > 0 {
		root["required"] = s.RequiredInputs
	}
	if len(s.Types) > 0 {
		defs := map[string]any{}
		for token, t := range s.Types {
			def, err := s.objectType(t)
			if err != nil {
				return nil, fmt.Errorf("%s: type %s: %w", s.Token, token, err)
			}
			defs[defKey(token)] = def
		}
		root["$defs"] = defs
	}
	return json.Marshal(root)
}

func (s *Schema) properties(props map[string]*Property) (map[string]any, error) {
	out := make(map[string]any, len(props))
	names := make([]string, 0, len(props))
	for name := range props {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		v, err := s.property(props[name])
		if err != nil {
			return nil, fmt.Errorf("property %s: %w", name, err)
		}
		out[name] = v
	}
	return out, nil
}

func (s *Schema) objectType(t *ObjectType) (map[string]any, error) {
	if len(t.Enum) > 0 {
		values := make([]any, len(t.Enum))
		for i, e := range t.Enum {
			values[i] = e.Value
		}
		return map[string]any{"type": t.Type, "enum": values}, nil
	}
	props, err := s.properties(t.Properties)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(t.Required) > 0 {
		out["required"] = t.Required
	}
	return out, nil
}

// property converts one Pulumi type to JSON Schema. A reference to the
// schema's own type becomes a $defs reference; a reference into Pulumi's
// own schema (Any, Json, Archive, Asset) accepts any value.
func (s *Schema) property(p *Property) (map[string]any, error) {
	if p == nil {
		return map[string]any{}, nil
	}
	switch {
	case strings.HasPrefix(p.Ref, TypeRefPrefix):
		token := strings.TrimPrefix(p.Ref, TypeRefPrefix)
		if s.Types[token] == nil {
			return nil, fmt.Errorf("references type %s, which is not pinned with it", token)
		}
		return map[string]any{"$ref": "#/$defs/" + defKey(token)}, nil
	case strings.HasPrefix(p.Ref, "pulumi.json#/"):
		return map[string]any{}, nil
	case p.Ref != "":
		return nil, fmt.Errorf("references %s, which is neither a pinned type nor one of Pulumi's", p.Ref)
	case len(p.OneOf) > 0:
		var alts []any
		for _, alt := range p.OneOf {
			v, err := s.property(alt)
			if err != nil {
				return nil, err
			}
			alts = append(alts, v)
		}
		return map[string]any{"oneOf": alts}, nil
	}
	switch p.Type {
	case "string", "integer", "number", "boolean":
		return map[string]any{"type": p.Type}, nil
	case "array":
		items, err := s.property(p.Items)
		if err != nil {
			return nil, err
		}
		return map[string]any{"type": "array", "items": items}, nil
	case "object":
		out := map[string]any{"type": "object"}
		if p.AdditionalProperties != nil {
			v, err := s.property(p.AdditionalProperties)
			if err != nil {
				return nil, err
			}
			out["additionalProperties"] = v
		}
		return out, nil
	}
	return nil, fmt.Errorf("has type %q", p.Type)
}

// defKey is a type token as a $defs key: every character but a letter or
// a digit an underscore, so the key needs no JSON Pointer escaping.
func defKey(token string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			return r
		}
		return '_'
	}, token)
}
