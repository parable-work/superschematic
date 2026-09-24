package ir

import "fmt"

// FindArrayOfArrays returns the first place the schema declares an array
// of arrays (T[][]), as "Type.field", "Type.field(arg)", "Set.op" for an
// operation's response or "Set.op(arg)" for an operation argument. Types
// are visited in name order, then operation sets in declaration order.
// found is false when the schema declares none.
//
// Generators that cannot render T[][] yet call it to fail instead of
// emitting T[]; a reader that does not know TypeRef.IsArrayOfArrays can
// call it to refuse such a schema.
func (s *Schema) FindArrayOfArrays() (where string, found bool) {
	if s == nil {
		return "", false
	}
	for _, types := range []map[string]*TypeDef{s.Types, s.Inputs} {
		for _, name := range sortedStringMapKeys(types) {
			if where, found := typeArrayOfArrays(types[name]); found {
				return where, true
			}
		}
	}
	for _, set := range s.OperationSets {
		if set == nil {
			continue
		}
		for _, op := range set.Operations {
			if where, found := fieldArrayOfArrays(set.Name, op); found {
				return where, true
			}
		}
	}
	return "", false
}

func typeArrayOfArrays(td *TypeDef) (string, bool) {
	if td == nil {
		return "", false
	}
	for _, f := range td.Fields {
		if where, found := fieldArrayOfArrays(td.Name, f); found {
			return where, true
		}
	}
	if td.TraitConfig != nil {
		for _, f := range td.TraitConfig.Fields {
			if where, found := fieldArrayOfArrays(td.Name, f); found {
				return where, true
			}
		}
	}
	return "", false
}

func fieldArrayOfArrays(owner string, f *FieldDef) (string, bool) {
	if f == nil {
		return "", false
	}
	if f.TypeRef.IsArrayOfArrays {
		return owner + "." + f.Name, true
	}
	for _, arg := range f.Arguments {
		if arg != nil && arg.TypeRef.IsArrayOfArrays {
			return fmt.Sprintf("%s.%s(%s)", owner, f.Name, arg.Name), true
		}
	}
	return "", false
}

// validateArrayOfArrays checks the TypeRef invariants of T[][]:
// IsArrayOfArrays requires IsArray and excludes IsMap.
func validateArrayOfArrays(owner string, ref TypeRef) []error {
	if !ref.IsArrayOfArrays {
		return nil
	}
	var errs []error
	if !ref.IsArray {
		errs = append(errs, fmt.Errorf("%s: isArrayOfArrays requires isArray", owner))
	}
	if ref.IsMap {
		errs = append(errs, fmt.Errorf("%s: a map value cannot be an array of arrays (isArrayOfArrays with isMap)", owner))
	}
	return errs
}
