// Package schemafile defines the on-disk document model shared by the JSON
// and YAML schema frontends. The on-disk shape mirrors the Schema IR
// directly: a schema file is either a multi-definition document (an
// ir.Schema slice using the IR field names verbatim) or a single definition
// object dispatched on a discriminator.
//
// The package owns the exhaustive JSON Schema both readers validate against
// (see schema.go) and the merge of decoded documents into a service's
// ir.Schema (see merge.go).
package schemafile

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// Document is the multi-definition schema file form: a slice of an
// ir.Schema. Every collection uses the IR types and field names verbatim, so
// reading is decode + struct conversion. Name and Kind are optional on disk;
// when present they must match the service config (checked by Merge).
type Document struct {
	// Name optionally restates the service name.
	Name string `json:"name,omitempty" yaml:"name,omitempty"`

	// Kind optionally restates the service schema kind.
	Kind ir.SchemaKind `json:"kind,omitempty" yaml:"kind,omitempty"`

	// Description is the human-readable schema description.
	Description string `json:"description,omitempty" yaml:"description,omitempty"`

	// Comment is the node-attached comment for the schema declaration.
	Comment string `json:"comment,omitempty" yaml:"comment,omitempty"`

	// Imports lists cross-schema dependencies. Always named; the "*"
	// wildcard does not exist in v2.
	Imports []ir.Import `json:"imports,omitempty" yaml:"imports,omitempty"`

	// Scalars maps scalar names to definitions.
	Scalars map[string]*ir.ScalarDef `json:"scalars,omitempty" yaml:"scalars,omitempty"`

	// Types maps type names to definitions.
	Types map[string]*ir.TypeDef `json:"types,omitempty" yaml:"types,omitempty"`

	// Enums maps enum names to definitions.
	Enums map[string]*ir.EnumDef `json:"enums,omitempty" yaml:"enums,omitempty"`

	// Unions maps union names to definitions.
	Unions map[string]*ir.UnionDef `json:"unions,omitempty" yaml:"unions,omitempty"`

	// OperationSets lists operation sets in declaration order.
	OperationSets []*ir.OperationSet `json:"operationSets,omitempty" yaml:"operationSets,omitempty"`

	// Documents holds sidecar documents keyed by document name, copied into
	// [ir.Schema.Documents] in canonical JSON (see [ir.CanonicalJSON]).
	Documents map[string]json.RawMessage `json:"documents,omitempty" yaml:"documents,omitempty"`

	// Extensions holds schema-level extension data keyed by extension name,
	// copied into [ir.Schema.Extensions] in canonical JSON; an entry that is
	// `{}` is dropped, as [ir.SetExtension] does. Nested nodes carry their
	// own extensions through the IR types above.
	Extensions map[string]json.RawMessage `json:"extensions,omitempty" yaml:"extensions,omitempty"`
}

// singleDefKinds maps the single-definition discriminator values to their
// document form. SchemaKind values dispatch to the multi-definition document
// instead.
var singleDefKinds = map[string]bool{
	"Enum":         true,
	"Union":        true,
	"Scalar":       true,
	"OperationSet": true,
}

// documentCollectionKeys are the top-level keys that identify a
// multi-definition document when no discriminator is present: the json name
// of every Document field except name, kind, description and comment.
// TestDocumentCollectionKeysMatchTheDocument keeps the two in step.
var documentCollectionKeys = []string{
	"imports", "scalars", "types", "enums", "unions",
	"operationSets", "documents", "extensions",
}

// form identifies which on-disk shape a payload uses.
type form int

const (
	formDocument form = iota
	formType
	formEnum
	formUnion
	formScalar
	formOperationSet
)

// defName returns the JSON Schema definition name validated against for a form.
func (f form) defName() string {
	switch f {
	case formType:
		return "TypeDef"
	case formEnum:
		return "EnumFile"
	case formUnion:
		return "UnionFile"
	case formScalar:
		return "ScalarFile"
	case formOperationSet:
		return "OperationSetFile"
	default:
		return "Document"
	}
}

// dispatch decides which on-disk form a decoded payload uses:
//
//  1. An explicit "kind" key set to Enum, Union, Scalar, or OperationSet is a
//     single definition of that sort; a "kind" naming a schema kind (DB, API,
//     ...) is a multi-definition document.
//  2. A "role" key is a single type definition.
//  3. Any document collection key (types, enums, imports, ...) is a
//     multi-definition document.
func dispatch(payload map[string]any, reg *registry.Registry) (form, error) {
	if rawKind, ok := payload["kind"]; ok {
		kind, ok := rawKind.(string)
		if !ok {
			return formDocument, fmt.Errorf("the %q key must be a string", "kind")
		}
		if singleDefKinds[kind] {
			switch kind {
			case "Enum":
				return formEnum, nil
			case "Union":
				return formUnion, nil
			case "Scalar":
				return formScalar, nil
			default:
				return formOperationSet, nil
			}
		}
		if _, ok := reg.Kind(kind); ok {
			return formDocument, nil
		}
		return formDocument, fmt.Errorf("unknown kind %q: expected a definition kind (Enum, Union, Scalar, OperationSet) or a schema kind", kind)
	}
	if _, ok := payload["role"]; ok {
		return formType, nil
	}
	for _, key := range documentCollectionKeys {
		if _, ok := payload[key]; ok {
			return formDocument, nil
		}
	}
	return formDocument, fmt.Errorf("cannot determine schema file shape: expected a %q or %q discriminator or a document collection key (types, enums, ...)", "kind", "role")
}

// IsDocumentForm reports whether a decoded payload uses the multi-definition
// document form (as opposed to a single-definition file).
func IsDocumentForm(payload map[string]any) bool {
	return IsDocumentFormWith(payload, core())
}

// IsDocumentFormWith is IsDocumentForm against a registry's kinds (nil for
// the core registry).
func IsDocumentFormWith(payload map[string]any, reg *registry.Registry) bool {
	f, err := dispatch(payload, orCore(reg))
	return err == nil && f == formDocument
}

// Decode validates a JSON payload against the schema-file JSON Schema and
// decodes it into a Document. Single-definition payloads are normalized into
// a Document holding that one definition. The source argument names the
// payload origin (a file path or a runtime-mode label) for error messages.
//
// Decode is the shared backend of both the JSON and YAML readers: the YAML
// reader converts its node tree to JSON before handing it here. It validates
// against the core registry; callers holding a registry use DecodeWith.
func Decode(data []byte, source string) (*Document, error) {
	return DecodeWith(data, source, core())
}

// DecodeWith is Decode against a registry (nil for the core one): its kinds
// dispatch the document form, its extensions and documents close the
// corresponding slots, and an "extensions" or "documents" key naming
// something the registry does not know is rejected by name before JSON
// Schema validation runs.
func DecodeWith(data []byte, source string, reg *registry.Registry) (*Document, error) {
	reg = orCore(reg)
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("%s: %w", source, err)
	}

	f, err := dispatch(payload, reg)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", source, err)
	}

	if err := checkSlotNames(payload, f, reg); err != nil {
		return nil, fmt.Errorf("%s: %w", source, err)
	}
	if err := validateAgainstDef(reg, data, f.defName(), source); err != nil {
		return nil, err
	}

	doc, err := decodeForm(data, f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", source, err)
	}
	if err := canonicalizeDocument(doc); err != nil {
		return nil, fmt.Errorf("%s: %w", source, err)
	}
	fillMCPInvocationDefaults(doc, reg.ToolInvocationPolicy())
	return doc, nil
}

// fillMCPInvocationDefaults gives every visible @mcp record that omits the
// invocation policy the registry's default, as the TypeScript frontend's
// @mcp does, so the IR is the same from every form. The JSON Schema has
// already held the key and the value to the registry's policy.
func fillMCPInvocationDefaults(doc *Document, policy registry.ToolInvocationPolicy) {
	for _, set := range doc.OperationSets {
		if set == nil {
			continue
		}
		for _, op := range set.Operations {
			if op == nil || op.MCP == nil || op.MCP.Hidden || !op.MCP.Invocation.IsZero() {
				continue
			}
			op.MCP.Invocation = ir.MCPInvocation{Key: policy.Key, Value: policy.Default}
		}
	}
}

// decodeForm strict-decodes a validated payload into the Document for its
// on-disk form.
func decodeForm(data []byte, f form) (*Document, error) {
	switch f {
	case formDocument:
		doc := &Document{}
		if err := strictDecode(data, doc); err != nil {
			return nil, err
		}
		return doc, nil
	case formType:
		def := &ir.TypeDef{}
		if err := strictDecode(data, def); err != nil {
			return nil, err
		}
		return &Document{Types: map[string]*ir.TypeDef{def.Name: def}}, nil
	case formEnum:
		def := &ir.EnumDef{}
		if err := strictDecodeWithoutKind(data, def); err != nil {
			return nil, err
		}
		return &Document{Enums: map[string]*ir.EnumDef{def.Name: def}}, nil
	case formUnion:
		def := &ir.UnionDef{}
		if err := strictDecodeWithoutKind(data, def); err != nil {
			return nil, err
		}
		return &Document{Unions: map[string]*ir.UnionDef{def.Name: def}}, nil
	case formScalar:
		def := &ir.ScalarDef{}
		if err := strictDecodeWithoutKind(data, def); err != nil {
			return nil, err
		}
		return &Document{Scalars: map[string]*ir.ScalarDef{def.Name: def}}, nil
	default:
		set := &ir.OperationSet{}
		if err := strictDecodeWithoutKind(data, set); err != nil {
			return nil, err
		}
		return &Document{OperationSets: []*ir.OperationSet{set}}, nil
	}
}

// strictDecode unmarshals JSON into v, rejecting unknown keys. The JSON
// Schema validation already rejects unknown keys; this is belt and braces.
func strictDecode(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// strictDecodeWithoutKind strips the single-definition "kind" discriminator
// (which is not an IR field) before strict-decoding into the IR type.
func strictDecodeWithoutKind(data []byte, v any) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	delete(raw, "kind")
	stripped, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	return strictDecode(stripped, v)
}
