package serialize

import (
	"bytes"
	"fmt"

	"github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/runtime/schema/go/mask"
)

// Serializer walks an IR Schema to produce a sanitized output map (or JSON
// bytes) from runtime data. It is the runtime mirror of generated ToMap /
// MarshalJSON helpers.
type Serializer struct {
	schema      *ir.Schema
	maskSecrets bool
	strict      bool
	masker      *mask.Masker
}

// Option configures a Serializer during construction.
type Option func(*Serializer)

// WithMaskSecrets zeroes @secret fields before walking by composing with the
// [mask] package; values are zeroed according to mask's contract (nil for
// optional fields, scalar zero for required). Off by default.
func WithMaskSecrets(mask bool) Option {
	return func(s *Serializer) {
		s.maskSecrets = mask
	}
}

// WithStrict reports unknown fields as {Validator: "unknown_field"} errors.
// Off by default, in which case unknown fields are silently dropped to match
// generated ToMap behavior.
func WithStrict(strict bool) Option {
	return func(s *Serializer) {
		s.strict = strict
	}
}

// New creates a Serializer for the given schema.
func New(schema *ir.Schema, opts ...Option) *Serializer {
	s := &Serializer{schema: schema}
	for _, opt := range opts {
		opt(s)
	}
	if s.maskSecrets {
		s.masker = mask.New(schema)
	}
	return s
}

// TypeToMap returns a sanitized map for the named object type. Unknown
// fields are dropped unless [WithStrict] is set.
func (s *Serializer) TypeToMap(typeName string, data map[string]any) (map[string]any, ValidationErrors) {
	td, ok := s.schema.Types[typeName]
	if !ok {
		errs := NewValidationErrors()
		errs.AddFieldError("", "type", fmt.Sprintf("unknown type %q", typeName))
		return nil, errs
	}
	return s.serialize(td, data)
}

// InputToMap returns a sanitized map for the named input type. Same
// semantics as TypeToMap.
func (s *Serializer) InputToMap(inputName string, data map[string]any) (map[string]any, ValidationErrors) {
	td, ok := s.schema.Inputs[inputName]
	if !ok {
		errs := NewValidationErrors()
		errs.AddFieldError("", "type", fmt.Sprintf("unknown input %q", inputName))
		return nil, errs
	}
	return s.serialize(td, data)
}

// TypeToJSON serializes data as canonical JSON bytes. Keys are emitted in
// schema declaration order (matching generated MarshalJSON's struct-field
// order, not encoding/json's alphabetical map order).
func (s *Serializer) TypeToJSON(typeName string, data map[string]any) ([]byte, ValidationErrors) {
	td, ok := s.schema.Types[typeName]
	if !ok {
		errs := NewValidationErrors()
		errs.AddFieldError("", "type", fmt.Sprintf("unknown type %q", typeName))
		return nil, errs
	}
	return s.toJSON(td, data)
}

// InputToJSON serializes data as canonical JSON bytes for inputName.
func (s *Serializer) InputToJSON(inputName string, data map[string]any) ([]byte, ValidationErrors) {
	td, ok := s.schema.Inputs[inputName]
	if !ok {
		errs := NewValidationErrors()
		errs.AddFieldError("", "type", fmt.Sprintf("unknown input %q", inputName))
		return nil, errs
	}
	return s.toJSON(td, data)
}

func (s *Serializer) serialize(td *ir.TypeDef, data map[string]any) (map[string]any, ValidationErrors) {
	errs := NewValidationErrors()
	src := s.applyMask(td, data)
	out := s.walkTypeDef(td, src, errs)
	return out, errs
}

func (s *Serializer) toJSON(td *ir.TypeDef, data map[string]any) ([]byte, ValidationErrors) {
	sanitized, errs := s.serialize(td, data)
	if errs.HasErrors() {
		return nil, errs
	}
	var buf bytes.Buffer
	if err := s.writeObject(&buf, td, sanitized); err != nil {
		errs.AddFieldError("", "json", err.Error())
		return nil, errs
	}
	return buf.Bytes(), errs
}

// applyMask delegates to the mask package when WithMaskSecrets is set,
// returning a new map with @secret fields zeroed. When the option is off it
// returns data unchanged so the walk operates on the caller's map.
func (s *Serializer) applyMask(td *ir.TypeDef, data map[string]any) map[string]any {
	if !s.maskSecrets || data == nil {
		return data
	}
	if td.Kind == ir.TypeKindInput {
		return s.masker.MaskInput(td.Name, data)
	}
	return s.masker.MaskType(td.Name, data)
}

func (s *Serializer) walkTypeDef(td *ir.TypeDef, data map[string]any, errs ValidationErrors) map[string]any {
	out := make(map[string]any, len(td.Fields))
	known := make(map[string]struct{}, len(td.Fields))
	for _, field := range td.Fields {
		key := fieldKey(field)
		known[key] = struct{}{}
		value, present := data[key]
		if !present {
			continue
		}
		out[key] = s.walkField(field, value, errs, key)
	}

	if s.strict {
		for k := range data {
			if _, ok := known[k]; ok {
				continue
			}
			errs.AddFieldError(k, "unknown_field", "unknown field")
		}
	}
	return out
}

func (s *Serializer) walkField(field *ir.FieldDef, value any, errs ValidationErrors, path string) any {
	if value == nil {
		if field.TypeRef.IsArray {
			return []any{}
		}
		return nil
	}

	kind := s.resolveRefKind(field.TypeRef)

	if field.TypeRef.IsArray {
		arr, ok := value.([]any)
		if !ok {
			return value
		}
		if arr == nil {
			arr = []any{}
		}
		out := make([]any, len(arr))
		for i, elem := range arr {
			elemPath := fmt.Sprintf("%s[%d]", path, i)
			if field.TypeRef.IsArrayOfArrays {
				out[i] = s.walkInnerList(field, kind, elem, errs, elemPath)
				continue
			}
			out[i] = s.walkElem(field, kind, elem, errs, elemPath)
		}
		return out
	}

	switch kind {
	case "type":
		td := s.schema.Types[field.TypeRef.Name]
		m, ok := value.(map[string]any)
		if !ok || td == nil {
			return value
		}
		nested := NewValidationErrors()
		result := s.walkTypeDef(td, m, nested)
		if nested.HasErrors() {
			errs.AddNestedError(path, nested)
		}
		return result

	case "input":
		td := s.schema.Inputs[field.TypeRef.Name]
		m, ok := value.(map[string]any)
		if !ok || td == nil {
			return value
		}
		nested := NewValidationErrors()
		result := s.walkTypeDef(td, m, nested)
		if nested.HasErrors() {
			errs.AddNestedError(path, nested)
		}
		return result
	}

	return value
}

// walkInnerList walks one inner list of an array of arrays (T[][]). A nil
// inner list becomes an empty list, as generated MarshalJSON writes it; a
// value that is not a list passes through.
func (s *Serializer) walkInnerList(field *ir.FieldDef, kind string, value any, errs ValidationErrors, path string) any {
	if value == nil {
		return []any{}
	}
	inner, ok := value.([]any)
	if !ok {
		return value
	}
	out := make([]any, len(inner))
	for j, elem := range inner {
		out[j] = s.walkElem(field, kind, elem, errs, fmt.Sprintf("%s[%d]", path, j))
	}
	return out
}

func (s *Serializer) walkElem(field *ir.FieldDef, kind string, elem any, errs ValidationErrors, path string) any {
	if elem == nil {
		return nil
	}
	switch kind {
	case "type":
		td := s.schema.Types[field.TypeRef.Name]
		m, ok := elem.(map[string]any)
		if !ok || td == nil {
			return elem
		}
		nested := NewValidationErrors()
		result := s.walkTypeDef(td, m, nested)
		if nested.HasErrors() {
			errs.AddNestedError(path, nested)
		}
		return result
	case "input":
		td := s.schema.Inputs[field.TypeRef.Name]
		m, ok := elem.(map[string]any)
		if !ok || td == nil {
			return elem
		}
		nested := NewValidationErrors()
		result := s.walkTypeDef(td, m, nested)
		if nested.HasErrors() {
			errs.AddNestedError(path, nested)
		}
		return result
	}
	return elem
}

func (s *Serializer) resolveRefKind(ref ir.TypeRef) string {
	if _, ok := s.schema.Scalars[ref.Name]; ok {
		return "scalar"
	}
	if _, ok := s.schema.Enums[ref.Name]; ok {
		return "enum"
	}
	if _, ok := s.schema.Types[ref.Name]; ok {
		return "type"
	}
	if _, ok := s.schema.Inputs[ref.Name]; ok {
		return "input"
	}
	return "builtin"
}

func fieldKey(f *ir.FieldDef) string {
	if f.JSONTag != "" {
		return f.JSONTag
	}
	return f.Name
}
