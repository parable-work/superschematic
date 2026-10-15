package verify

import (
	ir "github.com/parable-work/superschematic/ir"
)

// checkReferences holds each RefersTo (D61) to the schema, in every form:
//
//   - a reference is an API's, on a field of a type or an operation's
//     argument, never on an operation's result;
//   - it holds one value or a list of them, not a map or a list of lists;
//   - ir.ResolveReference derives its target, options, value, label and
//     lookup, reporting the first rule one breaks;
//   - the value's field holds the type the reference's holder holds, its
//     element type for a list (Identity.UUID to Identity.UUID).
//
// It runs after checkDisplays, whose titleField is a reference's label
// when it names none. A dependency's target reads from
// Input.DependencySchemas, which the loader fills for a reference that
// names a type the schema imports.
func checkReferences(schema *ir.Schema, in Input, r *Result) {
	for _, held := range ir.ReferencesOf(schema) {
		if held.Type != nil && held.Field.InheritedFrom != "" && schema.Types[held.Field.InheritedFrom] != nil {
			// The base class holds the reference and reports it.
			continue
		}
		owner := ""
		if held.Type != nil {
			owner = held.Type.Owner
		}
		switch {
		case schema.Kind != ir.SchemaKindAPI:
			r.errorf(owner, "%s: RefersTo is an API's; %s is a %s schema", held.Holder, schema.Name, schema.Kind)
			continue
		case held.Type == nil && held.Argument == nil:
			r.errorf(owner, "%s: RefersTo on an operation's result; a reference sits on a field or an argument", held.Holder)
			continue
		case held.TypeRef.IsMap:
			r.errorf(owner, "%s: RefersTo on a map; a reference holds one value or a list of them", held.Holder)
			continue
		case held.TypeRef.IsArrayOfArrays:
			r.errorf(owner, "%s: RefersTo on a list of lists", held.Holder)
			continue
		}
		resolved, err := ir.ResolveReference(schema, in.DependencySchemas, held.Ref)
		if err != nil {
			r.errorf(owner, "%s: %v", held.Holder, err)
			continue
		}
		if value := resolved.Value; value.TypeRef.Name != held.TypeRef.Name {
			r.errorf(owner, "%s: is %s, but %s.%s, its value, is %s", held.Holder, held.TypeRef.Name, resolved.TargetName, value.Name, typeLabel(value.TypeRef))
		}
	}
}
