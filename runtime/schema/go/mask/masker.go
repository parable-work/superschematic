package mask

import "github.com/parable-work/superschematic/ir"

// Masker interprets an IR Schema to mask secret fields at runtime.
// It mirrors generated MaskSecrets behavior for dynamic schemas.
type Masker struct {
	schema *ir.Schema
}

// New creates a Masker for the given schema.
func New(schema *ir.Schema) *Masker {
	return &Masker{schema: schema}
}

// MaskType masks a data map against a named object type definition.
// Returns nil when data is nil.
func (m *Masker) MaskType(typeName string, data map[string]any) map[string]any {
	if data == nil {
		return nil
	}
	td, ok := m.schema.Types[typeName]
	if !ok {
		return deepCopyMap(data)
	}
	return m.maskTypeDef(td, data)
}

// MaskInput masks a data map against a named input type definition.
// Returns nil when data is nil.
func (m *Masker) MaskInput(typeName string, data map[string]any) map[string]any {
	if data == nil {
		return nil
	}
	td, ok := m.schema.Inputs[typeName]
	if !ok {
		return deepCopyMap(data)
	}
	return m.maskTypeDef(td, data)
}

// maskTypeDef masks all known fields in a type definition while preserving
// unknown fields from the input map.
func (m *Masker) maskTypeDef(td *ir.TypeDef, data map[string]any) map[string]any {
	masked := deepCopyMap(data)
	for _, field := range td.Fields {
		key := fieldKey(field)
		value, exists := data[key]
		if !exists {
			continue
		}
		masked[key] = m.maskField(field, value)
	}
	return masked
}

// maskField masks one field value using field metadata and type information.
func (m *Masker) maskField(field *ir.FieldDef, value any) any {
	if value == nil {
		return nil
	}

	if field.Secret {
		return m.zeroSecretValue(field)
	}

	kind := m.resolveRefKind(field.TypeRef)
	if field.TypeRef.IsArray {
		arr, ok := value.([]any)
		if !ok {
			return deepCopyValue(value)
		}
		return m.maskArrayField(field, kind, arr)
	}

	switch kind {
	case "type":
		td := m.schema.Types[field.TypeRef.Name]
		if td == nil {
			return deepCopyValue(value)
		}
		mv, ok := value.(map[string]any)
		if !ok {
			return deepCopyValue(value)
		}
		return m.maskTypeDef(td, mv)

	case "input":
		td := m.schema.Inputs[field.TypeRef.Name]
		if td == nil {
			return deepCopyValue(value)
		}
		mv, ok := value.(map[string]any)
		if !ok {
			return deepCopyValue(value)
		}
		return m.maskTypeDef(td, mv)
	}

	return deepCopyValue(value)
}

func (m *Masker) maskArrayField(field *ir.FieldDef, kind string, arr []any) []any {
	masked := make([]any, len(arr))
	for i, elem := range arr {
		if elem == nil {
			masked[i] = nil
			continue
		}

		switch kind {
		case "type":
			td := m.schema.Types[field.TypeRef.Name]
			mv, ok := elem.(map[string]any)
			if td != nil && ok {
				masked[i] = m.maskTypeDef(td, mv)
				continue
			}
		case "input":
			td := m.schema.Inputs[field.TypeRef.Name]
			mv, ok := elem.(map[string]any)
			if td != nil && ok {
				masked[i] = m.maskTypeDef(td, mv)
				continue
			}
		}

		masked[i] = deepCopyValue(elem)
	}
	return masked
}

// zeroSecretValue returns the masked zero value for a secret field.
func (m *Masker) zeroSecretValue(field *ir.FieldDef) any {
	if !field.Required {
		return nil
	}

	if field.TypeRef.IsArray {
		return []any{}
	}

	kind := m.resolveRefKind(field.TypeRef)
	switch kind {
	case "type", "input":
		return map[string]any{}
	case "enum":
		return ""
	case "scalar":
		if scalar := m.schema.Scalars[field.TypeRef.Name]; scalar != nil {
			return zeroForScalarName(scalar.Primitive)
		}
		return nil
	case "builtin":
		return zeroForScalarName(field.TypeRef.Name)
	default:
		return nil
	}
}

func zeroForScalarName(name string) any {
	switch name {
	case "String", "ID":
		return ""
	case "Int":
		return 0
	case "Float":
		return float64(0)
	case "Boolean":
		return false
	default:
		return nil
	}
}

// resolveRefKind determines what category a TypeRef refers to within the schema.
// Returns "scalar", "enum", "type", "input", or "builtin".
func (m *Masker) resolveRefKind(ref ir.TypeRef) string {
	if _, ok := m.schema.Scalars[ref.Name]; ok {
		return "scalar"
	}
	if _, ok := m.schema.Enums[ref.Name]; ok {
		return "enum"
	}
	if _, ok := m.schema.Types[ref.Name]; ok {
		return "type"
	}
	if _, ok := m.schema.Inputs[ref.Name]; ok {
		return "input"
	}
	if ir.IsBuiltinScalar(ref.Name) {
		return "builtin"
	}
	return "builtin"
}

// fieldKey returns the JSON key used to look up a field value in a data map.
// Uses JSONTag if set, otherwise falls back to the field name.
func fieldKey(f *ir.FieldDef) string {
	if f.JSONTag != "" {
		return f.JSONTag
	}
	return f.Name
}

func deepCopyMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = deepCopyValue(v)
	}
	return out
}

func deepCopyValue(v any) any {
	switch tv := v.(type) {
	case map[string]any:
		return deepCopyMap(tv)
	case []any:
		out := make([]any, len(tv))
		for i, elem := range tv {
			out[i] = deepCopyValue(elem)
		}
		return out
	default:
		return v
	}
}
