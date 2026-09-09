package merge

import "github.com/parable-work/superschematic/ir"

// Merger interprets an IR Schema to merge secret fields at runtime.
// When a secret field in the new config has a zero value, the existing
// value is preserved -- preventing accidental overwrite of credentials
// that are masked to empty on the client side.
type Merger struct {
	schema *ir.Schema
}

// New creates a Merger for the given schema.
func New(schema *ir.Schema) *Merger {
	return &Merger{schema: schema}
}

// MergeType merges a data map against a named object type definition.
// Secret fields with zero values in newData are replaced by values from existingData.
func (m *Merger) MergeType(typeName string, newData, existingData map[string]any) map[string]any {
	if newData == nil {
		return nil
	}
	if existingData == nil {
		return deepCopyMap(newData)
	}
	td, ok := m.schema.Types[typeName]
	if !ok {
		return deepCopyMap(newData)
	}
	return m.mergeTypeDef(td, newData, existingData)
}

// MergeInput merges a data map against a named input type definition.
// Secret fields with zero values in newData are replaced by values from existingData.
func (m *Merger) MergeInput(typeName string, newData, existingData map[string]any) map[string]any {
	if newData == nil {
		return nil
	}
	if existingData == nil {
		return deepCopyMap(newData)
	}
	td, ok := m.schema.Inputs[typeName]
	if !ok {
		return deepCopyMap(newData)
	}
	return m.mergeTypeDef(td, newData, existingData)
}

// mergeTypeDef walks all known fields in a type definition.
// For secret fields with zero new values, the existing value is used.
// For non-secret fields, the new value is always used.
func (m *Merger) mergeTypeDef(td *ir.TypeDef, newData, existingData map[string]any) map[string]any {
	merged := deepCopyMap(newData)
	for _, field := range td.Fields {
		key := fieldKey(field)
		newVal, newExists := newData[key]
		existingVal, existingExists := existingData[key]

		if field.Secret {
			if (!newExists || isZeroValue(newVal)) && existingExists {
				merged[key] = deepCopyValue(existingVal)
			}
			continue
		}

		// Non-secret nested types: recurse to merge any secret fields within them.
		if !newExists || newVal == nil {
			continue
		}
		if existingVal == nil {
			continue
		}

		kind := m.resolveRefKind(field.TypeRef)
		if field.TypeRef.IsArray {
			newArr, newOk := newVal.([]any)
			existingArr, existingOk := existingVal.([]any)
			if newOk && existingOk {
				merged[key] = m.mergeArrayField(field, kind, newArr, existingArr)
			}
			continue
		}

		switch kind {
		case "type":
			td := m.schema.Types[field.TypeRef.Name]
			newMap, newOk := newVal.(map[string]any)
			existingMap, existingOk := existingVal.(map[string]any)
			if td != nil && newOk && existingOk {
				merged[key] = m.mergeTypeDef(td, newMap, existingMap)
			}
		case "input":
			td := m.schema.Inputs[field.TypeRef.Name]
			newMap, newOk := newVal.(map[string]any)
			existingMap, existingOk := existingVal.(map[string]any)
			if td != nil && newOk && existingOk {
				merged[key] = m.mergeTypeDef(td, newMap, existingMap)
			}
		}
	}
	return merged
}

// mergeArrayField merges arrays element-by-element for nested types that may
// contain secret fields. Elements are matched by index.
func (m *Merger) mergeArrayField(field *ir.FieldDef, kind string, newArr, existingArr []any) []any {
	merged := make([]any, len(newArr))
	for i, newElem := range newArr {
		if newElem == nil {
			merged[i] = nil
			continue
		}

		var existingElem any
		if i < len(existingArr) {
			existingElem = existingArr[i]
		}

		switch kind {
		case "type":
			td := m.schema.Types[field.TypeRef.Name]
			newMap, newOk := newElem.(map[string]any)
			existingMap, existingOk := existingElem.(map[string]any)
			if td != nil && newOk && existingOk {
				merged[i] = m.mergeTypeDef(td, newMap, existingMap)
				continue
			}
		case "input":
			td := m.schema.Inputs[field.TypeRef.Name]
			newMap, newOk := newElem.(map[string]any)
			existingMap, existingOk := existingElem.(map[string]any)
			if td != nil && newOk && existingOk {
				merged[i] = m.mergeTypeDef(td, newMap, existingMap)
				continue
			}
		}

		merged[i] = deepCopyValue(newElem)
	}
	return merged
}

// isZeroValue returns true when the value represents a zero/empty value
// that indicates the client didn't provide a real value for a secret field.
func isZeroValue(v any) bool {
	if v == nil {
		return true
	}
	switch tv := v.(type) {
	case string:
		return tv == ""
	case int:
		return tv == 0
	case float64:
		return tv == 0
	case bool:
		return !tv
	case map[string]any:
		return len(tv) == 0
	case []any:
		return len(tv) == 0
	default:
		return false
	}
}

// resolveRefKind determines what category a TypeRef refers to within the schema.
func (m *Merger) resolveRefKind(ref ir.TypeRef) string {
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
