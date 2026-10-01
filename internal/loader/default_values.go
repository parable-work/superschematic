package loader

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
)

// validateDefaultValues checks the values a schema author writes into the
// schema itself against the rules every validator applies to them: each
// field's and argument's default, and each scalar's example. A value that
// breaks its scalar's or its field's length, pattern or range rules fails
// the load, as a composite default does, so a schema cannot build with a
// value every runtime would refuse. Lengths count code points (D14,
// amended); a list default is a JSON array whose elements are each checked
// (D12, amended).
//
// It runs after the scalars are hydrated, so a catalog scalar's rules
// apply. A default of a type that has no literal form (an object, a union,
// a map) is not checked; the generators do not emit one either.
func validateDefaultValues(schema *ir.Schema, externalEnums map[string]*ir.EnumDef) error {
	var errs []error
	check := func(owner string, ref ir.TypeRef, rules valueRules, text string) {
		if err := validateDefaultValue(schema, externalEnums, ref, rules, text); err != nil {
			errs = append(errs, fmt.Errorf("%s: %s: %w", schema.Name, owner, err))
		}
	}
	for _, types := range []map[string]*ir.TypeDef{schema.Types, schema.Inputs} {
		for _, name := range sortedTypeNames(types) {
			for _, field := range types[name].Fields {
				if field.Default != nil {
					check(name+"."+field.Name, field.TypeRef, fieldValueRules(field), *field.Default)
				}
				for _, arg := range field.Arguments {
					if arg.Default != nil {
						check(fmt.Sprintf("%s.%s(%s)", name, field.Name, arg.Name), arg.TypeRef, argumentValueRules(arg), *arg.Default)
					}
				}
			}
		}
	}
	for _, set := range schema.OperationSets {
		for _, op := range set.Operations {
			for _, arg := range op.Arguments {
				if arg.Default != nil {
					check(fmt.Sprintf("%s.%s(%s)", set.Name, op.Name, arg.Name), arg.TypeRef, argumentValueRules(arg), *arg.Default)
				}
			}
		}
	}

	scalarNames := make([]string, 0, len(schema.Scalars))
	for name := range schema.Scalars {
		scalarNames = append(scalarNames, name)
	}
	sort.Strings(scalarNames)
	for _, name := range scalarNames {
		scalar := schema.Scalars[name]
		if scalar == nil || scalar.Example == "" {
			continue
		}
		value, ok := literalValue(scalar.LanguagePrimitive, scalar.Example)
		if !ok {
			continue
		}
		if err := validateCompositeScalar(scalar, value, "example"); err != nil {
			errs = append(errs, fmt.Errorf("%s: scalar %s: %w", schema.Name, name, err))
		}
	}
	return errors.Join(errs...)
}

// validateDefaultValue checks one default, written as the IR's default
// text, against its type and the rules of the field or argument it fills.
func validateDefaultValue(schema *ir.Schema, externalEnums map[string]*ir.EnumDef, ref ir.TypeRef, rules valueRules, text string) error {
	value, ok, err := defaultValue(schema, externalEnums, ref, text)
	if err != nil || !ok {
		return err
	}
	const path = "default"
	if err := validateCompositeValue(schema, externalEnums, ref, value, path); err != nil {
		return err
	}
	return validateCompositeFieldConstraints(rules, ref, value, path)
}

// defaultValue reads the IR's default text as the JSON value it stands for:
// the text of a string, a scalar with the string primitive or an enum; a
// number or a boolean for those primitives; a JSON array for a list. ok is
// false for a value the loader cannot check here: a map, a single object or
// union, which has no literal form, and a list of a type the schema imports
// but does not define.
func defaultValue(schema *ir.Schema, externalEnums map[string]*ir.EnumDef, ref ir.TypeRef, text string) (any, bool, error) {
	if ref.IsMap {
		return nil, false, nil
	}
	if ref.IsArray {
		value, err := decodeJSONText(text)
		items, isList := value.([]any)
		if err != nil || !isList {
			return nil, false, fmt.Errorf("default must be a JSON array")
		}
		_, isType := schema.Types[ref.Name]
		_, isUnion := schema.Unions[ref.Name]
		if len(items) > 0 && !isType && !isUnion && !literalType(schema, externalEnums, ref.Name) {
			return nil, false, nil
		}
		return value, true, nil
	}
	if !literalType(schema, externalEnums, ref.Name) {
		return nil, false, nil
	}
	switch ref.Name {
	case "string":
		return text, true, nil
	case "number":
		value, _ := literalValue(ir.LanguageNumber, text)
		return value, true, nil
	case "boolean":
		value, _ := literalValue(ir.LanguageBoolean, text)
		return value, true, nil
	}
	if scalar, isScalar := schema.Scalars[ref.Name]; isScalar {
		value, ok := literalValue(scalar.LanguagePrimitive, text)
		return value, ok, nil
	}
	return text, true, nil
}

// literalType reports whether a default of the named type is a literal the
// loader can check: a builtin primitive, a scalar or an enum.
func literalType(schema *ir.Schema, externalEnums map[string]*ir.EnumDef, name string) bool {
	switch name {
	case "string", "number", "boolean":
		return true
	}
	if _, ok := schema.Scalars[name]; ok {
		return true
	}
	if _, ok := schema.Enums[name]; ok {
		return true
	}
	_, ok := externalEnums[name]
	return ok
}

// literalValue reads text as a value of the primitive: the text itself for
// a string, a JSON number, or a boolean. Text that is not a number or a
// boolean stays a string, so the type check reports it. ok is false for a
// primitive with no literal form.
func literalValue(primitive ir.LanguagePrimitive, text string) (any, bool) {
	switch primitive {
	case ir.LanguageString:
		return text, true
	case ir.LanguageNumber:
		if value, err := decodeJSONText(text); err == nil {
			if number, ok := value.(json.Number); ok {
				return number, true
			}
		}
		return text, true
	case ir.LanguageBoolean:
		switch text {
		case "true":
			return true, true
		case "false":
			return false, true
		}
		return text, true
	}
	return nil, false
}

// decodeJSONText decodes text as exactly one JSON value, keeping numbers as
// json.Number.
func decodeJSONText(text string) (any, error) {
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, err
	}
	return value, nil
}

func sortedTypeNames(types map[string]*ir.TypeDef) []string {
	names := make([]string, 0, len(types))
	for name := range types {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
