package serialize

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/parable-work/superschematic/ir"
)

// writeObject emits a JSON object for td using sanitized as the source map.
// Keys are written in td.Fields declaration order so output is byte-stable
// across calls (Go's encoding/json sorts map keys alphabetically; this
// writer mirrors the struct-field-order output of generated MarshalJSON).
//
// Only keys actually present in sanitized are emitted, matching generated
// omitempty for absent fields. Nested fields whose IR resolves to a known
// Type / Input recurse so their keys are also declaration-ordered.
func (s *Serializer) writeObject(buf *bytes.Buffer, td *ir.TypeDef, sanitized map[string]any) error {
	buf.WriteByte('{')
	first := true
	emitted := make(map[string]struct{}, len(td.Fields))
	for _, field := range td.Fields {
		key := fieldKey(field)
		value, ok := sanitized[key]
		if !ok {
			continue
		}
		emitted[key] = struct{}{}
		if !first {
			buf.WriteByte(',')
		}
		first = false
		if err := writeString(buf, key); err != nil {
			return err
		}
		buf.WriteByte(':')
		if err := s.writeFieldValue(buf, field, value); err != nil {
			return err
		}
	}
	// In lenient mode the sanitize step already dropped unknown keys so the
	// only remaining entries should be schema-declared. If a caller hands us
	// a map containing extra keys (e.g. bypassing TypeToMap), preserve them
	// at the end in stable sorted order via json.Marshal so output is still
	// deterministic.
	for key, value := range sanitized {
		if _, done := emitted[key]; done {
			continue
		}
		if !first {
			buf.WriteByte(',')
		}
		first = false
		if err := writeString(buf, key); err != nil {
			return err
		}
		buf.WriteByte(':')
		raw, err := json.Marshal(value)
		if err != nil {
			return err
		}
		buf.Write(raw)
	}
	buf.WriteByte('}')
	return nil
}

func (s *Serializer) writeFieldValue(buf *bytes.Buffer, field *ir.FieldDef, value any) error {
	if value == nil {
		if field.TypeRef.IsArray {
			buf.WriteString("[]")
			return nil
		}
		buf.WriteString("null")
		return nil
	}

	kind := s.resolveRefKind(field.TypeRef)

	if field.TypeRef.IsArray {
		arr, ok := value.([]any)
		if !ok {
			raw, err := json.Marshal(value)
			if err != nil {
				return err
			}
			buf.Write(raw)
			return nil
		}
		buf.WriteByte('[')
		for i, elem := range arr {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := s.writeElemValue(buf, field, kind, elem); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
		return nil
	}

	switch kind {
	case "type":
		if nested := s.schema.Types[field.TypeRef.Name]; nested != nil {
			if m, ok := value.(map[string]any); ok {
				return s.writeObject(buf, nested, m)
			}
		}
	case "input":
		if nested := s.schema.Inputs[field.TypeRef.Name]; nested != nil {
			if m, ok := value.(map[string]any); ok {
				return s.writeObject(buf, nested, m)
			}
		}
	}

	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	buf.Write(raw)
	return nil
}

func (s *Serializer) writeElemValue(buf *bytes.Buffer, field *ir.FieldDef, kind string, elem any) error {
	if elem == nil {
		buf.WriteString("null")
		return nil
	}
	switch kind {
	case "type":
		if nested := s.schema.Types[field.TypeRef.Name]; nested != nil {
			if m, ok := elem.(map[string]any); ok {
				return s.writeObject(buf, nested, m)
			}
		}
	case "input":
		if nested := s.schema.Inputs[field.TypeRef.Name]; nested != nil {
			if m, ok := elem.(map[string]any); ok {
				return s.writeObject(buf, nested, m)
			}
		}
	}
	raw, err := json.Marshal(elem)
	if err != nil {
		return err
	}
	buf.Write(raw)
	return nil
}

func writeString(buf *bytes.Buffer, s string) error {
	raw, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("marshal key %q: %w", s, err)
	}
	buf.Write(raw)
	return nil
}
