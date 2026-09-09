package parse

import (
	"fmt"
	"log"

	"github.com/parable-work/superschematic/ir"
)

// walkTypeDef walks every field in td, copying values from raw into result
// and applying defaults/coercion/normalize/parse along the way. Errors are
// accumulated into errs; the caller pre-allocates both maps.
func (p *Parser) walkTypeDef(td *ir.TypeDef, raw map[string]any, result map[string]any, errs ValidationErrors, strict bool) {
	known := make(map[string]struct{}, len(td.Fields))
	for _, field := range td.Fields {
		key := fieldKey(field)
		known[key] = struct{}{}
		p.walkField(field, key, raw, result, errs, strict)
	}

	if strict {
		for k := range raw {
			if _, ok := known[k]; ok {
				continue
			}
			errs.AddFieldError(k, "unknown_field", "unknown field")
		}
	}
}

// walkField processes a single field: applies defaults when absent, then
// dispatches to single-value or array handling.
func (p *Parser) walkField(field *ir.FieldDef, key string, raw, result map[string]any, errs ValidationErrors, strict bool) {
	value, present := raw[key]

	if !present {
		// Defaults only apply to absent fields and only when the value
		// resolves to a scalar / enum / builtin. Defaults on object types
		// and arrays are out of scope for Phase C.
		if field.Default == nil {
			return
		}
		if field.TypeRef.IsArray {
			return
		}
		kind := p.resolveRefKind(field.TypeRef)
		if kind == "type" || kind == "input" {
			return
		}

		var scalar *ir.ScalarDef
		if kind == "scalar" {
			scalar = p.schema.Scalars[field.TypeRef.Name]
		}
		v, ok := applyDefault(field, scalar)
		if !ok {
			log.Printf("parse: default %q for field %q is not parseable as primitive %s; skipping",
				*field.Default, key, defaultPrimitive(scalar, field.TypeRef.Name))
			errs.AddFieldError(key, "default", "default value is not parseable for declared type")
			return
		}
		result[key] = v
		return
	}

	if value == nil {
		result[key] = nil
		return
	}

	kind := p.resolveRefKind(field.TypeRef)
	if field.TypeRef.IsArray {
		p.walkArrayField(field, key, kind, value, result, errs, strict)
		return
	}
	p.walkSingleField(field, key, kind, value, result, errs, strict)
}

func (p *Parser) walkSingleField(field *ir.FieldDef, key, kind string, value any, result map[string]any, errs ValidationErrors, strict bool) {
	switch kind {
	case "scalar":
		scalar := p.schema.Scalars[field.TypeRef.Name]
		out, ok := p.applyScalar(scalar, value, strict)
		if !ok {
			errs.AddFieldError(key, "type", typeMismatchMessage(scalar))
			result[key] = value
			return
		}
		out, parseErrs := p.applyCustomParse(scalar, out)
		if len(parseErrs) > 0 {
			errs.SetFieldErrors(key, parseErrs)
			result[key] = out
			return
		}
		result[key] = out

	case "enum":
		result[key] = value

	case "builtin":
		out, ok := p.applyBuiltin(field.TypeRef.Name, value, strict)
		if !ok {
			errs.AddFieldError(key, "type", "expected "+field.TypeRef.Name+" value")
			result[key] = value
			return
		}
		result[key] = out

	case "type":
		td := p.schema.Types[field.TypeRef.Name]
		m, ok := value.(map[string]any)
		if !ok {
			errs.AddFieldError(key, "type", "expected object value")
			result[key] = value
			return
		}
		nestedRaw := m
		nestedResult := make(map[string]any, len(m))
		nestedErrs := NewValidationErrors()
		p.walkTypeDef(td, nestedRaw, nestedResult, nestedErrs, strict)
		if nestedErrs.HasErrors() {
			errs.AddNestedError(key, nestedErrs)
		}
		result[key] = nestedResult

	case "input":
		td := p.schema.Inputs[field.TypeRef.Name]
		m, ok := value.(map[string]any)
		if !ok {
			errs.AddFieldError(key, "type", "expected object value")
			result[key] = value
			return
		}
		nestedResult := make(map[string]any, len(m))
		nestedErrs := NewValidationErrors()
		p.walkTypeDef(td, m, nestedResult, nestedErrs, strict)
		if nestedErrs.HasErrors() {
			errs.AddNestedError(key, nestedErrs)
		}
		result[key] = nestedResult

	default:
		result[key] = value
	}
}

func (p *Parser) walkArrayField(field *ir.FieldDef, key, kind string, value any, result map[string]any, errs ValidationErrors, strict bool) {
	arr, ok := value.([]any)
	if !ok {
		errs.AddFieldError(key, "type", "expected array value")
		result[key] = value
		return
	}

	out := make([]any, len(arr))
	for i, elem := range arr {
		elemKey := fmt.Sprintf("%s[%d]", key, i)
		if elem == nil {
			out[i] = nil
			continue
		}

		switch kind {
		case "scalar":
			scalar := p.schema.Scalars[field.TypeRef.Name]
			v, ok := p.applyScalar(scalar, elem, strict)
			if !ok {
				errs.AddFieldError(elemKey, "type", typeMismatchMessage(scalar))
				out[i] = elem
				continue
			}
			v, parseErrs := p.applyCustomParse(scalar, v)
			if len(parseErrs) > 0 {
				errs.SetFieldErrors(elemKey, parseErrs)
				out[i] = v
				continue
			}
			out[i] = v

		case "enum":
			out[i] = elem

		case "builtin":
			v, ok := p.applyBuiltin(field.TypeRef.Name, elem, strict)
			if !ok {
				errs.AddFieldError(elemKey, "type", "expected "+field.TypeRef.Name+" value")
				out[i] = elem
				continue
			}
			out[i] = v

		case "type":
			td := p.schema.Types[field.TypeRef.Name]
			m, ok := elem.(map[string]any)
			if !ok {
				errs.AddFieldError(elemKey, "type", "expected object value")
				out[i] = elem
				continue
			}
			nestedResult := make(map[string]any, len(m))
			nestedErrs := NewValidationErrors()
			p.walkTypeDef(td, m, nestedResult, nestedErrs, strict)
			if nestedErrs.HasErrors() {
				errs.AddNestedError(elemKey, nestedErrs)
			}
			out[i] = nestedResult

		case "input":
			td := p.schema.Inputs[field.TypeRef.Name]
			m, ok := elem.(map[string]any)
			if !ok {
				errs.AddFieldError(elemKey, "type", "expected object value")
				out[i] = elem
				continue
			}
			nestedResult := make(map[string]any, len(m))
			nestedErrs := NewValidationErrors()
			p.walkTypeDef(td, m, nestedResult, nestedErrs, strict)
			if nestedErrs.HasErrors() {
				errs.AddNestedError(elemKey, nestedErrs)
			}
			out[i] = nestedResult

		default:
			out[i] = elem
		}
	}
	result[key] = out
}

func (p *Parser) applyCustomParse(scalar *ir.ScalarDef, value any) (any, []ValidationError) {
	if !scalar.HasCustomParse {
		return value, nil
	}
	fn, ok := p.parseReg.Get(scalar.Name)
	if !ok {
		return value, nil
	}
	parsed, errs := fn(fmt.Sprint(value))
	if len(errs) > 0 {
		return value, errs
	}
	if scalar.Primitive == "Int" {
		integer, ok := coerceInt(parsed, false)
		if !ok {
			return value, []ValidationError{{
				Validator: "parse",
				Message:   "scalar parser returned a non-integer value",
			}}
		}
		return integer, nil
	}
	return parsed, nil
}

// applyScalar applies coercion and normalize to a single scalar value.
// Returns the resulting value (possibly typed as int64/float64/bool/string)
// and true on success.
func (p *Parser) applyScalar(scalar *ir.ScalarDef, value any, strict bool) (any, bool) {
	if scalar == nil {
		return value, true
	}
	switch scalar.Primitive {
	case "Int":
		i, ok := coerceInt(value, strict)
		if !ok {
			return value, false
		}
		return i, true
	case "Float":
		f, ok := coerceFloat(value, strict)
		if !ok {
			return value, false
		}
		return f, true
	case "Boolean":
		b, ok := coerceBool(value, strict)
		if !ok {
			return value, false
		}
		return b, true
	case "String", "":
		s, ok := value.(string)
		if !ok {
			return value, false
		}
		if scalar.HasCustomNormalize {
			if fn, has := p.normReg.Get(scalar.Name); has {
				s = fn(s)
			}
		}
		return s, true
	default:
		return value, true
	}
}

// applyBuiltin handles built-in GraphQL scalars (Int, Float, Boolean, String, ID).
func (p *Parser) applyBuiltin(name string, value any, strict bool) (any, bool) {
	switch name {
	case "Int":
		return coerceInt(value, strict)
	case "Float":
		return coerceFloat(value, strict)
	case "Boolean":
		return coerceBool(value, strict)
	case "String", "ID":
		s, ok := value.(string)
		if !ok {
			return value, false
		}
		return s, true
	default:
		return value, true
	}
}

// resolveRefKind classifies a TypeRef's referent within the schema.
func (p *Parser) resolveRefKind(ref ir.TypeRef) string {
	if _, ok := p.schema.Scalars[ref.Name]; ok {
		return "scalar"
	}
	if _, ok := p.schema.Enums[ref.Name]; ok {
		return "enum"
	}
	if _, ok := p.schema.Types[ref.Name]; ok {
		return "type"
	}
	if _, ok := p.schema.Inputs[ref.Name]; ok {
		return "input"
	}
	if ir.IsBuiltinScalar(ref.Name) {
		return "builtin"
	}
	return "builtin"
}

// fieldKey returns the JSON key used for a field in a data map.
func fieldKey(f *ir.FieldDef) string {
	if f.JSONTag != "" {
		return f.JSONTag
	}
	return f.Name
}

func typeMismatchMessage(scalar *ir.ScalarDef) string {
	if scalar == nil {
		return "type mismatch"
	}
	switch scalar.Primitive {
	case "Int":
		return "expected integer value"
	case "Float":
		return "expected numeric value"
	case "Boolean":
		return "expected boolean value"
	case "String", "":
		return "expected string value"
	default:
		return "type mismatch"
	}
}

func defaultPrimitive(scalar *ir.ScalarDef, builtin string) string {
	if scalar != nil && scalar.Primitive != "" {
		return scalar.Primitive
	}
	return builtin
}
