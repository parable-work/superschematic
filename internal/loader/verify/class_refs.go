package verify

import (
	"encoding/json"
	"fmt"
	"sort"

	ir "github.com/parable-work/superschematic/ir"
)

// checkClassRefs checks every class reference ({"class": name},
// ir.ClassRef) in a decorator's value under an extensions slot of a type,
// field, operation set or operation: the schema declares a type of that name
// or imports one. The TypeScript frontend resolves a class through the
// compiler and records an import for one from another service's package; the
// data forms write the name, so this check is what fails a misspelt or
// missing class in every form. The schema root's slot holds no decorator, so
// it is not read.
func checkClassRefs(schema *ir.Schema, r *Result) {
	imported := make(map[string]bool)
	for _, imp := range schema.Imports {
		for _, name := range imp.Types {
			imported[name] = true
		}
	}
	known := func(name string) bool {
		_, declared := schema.Types[name]
		return declared || imported[name]
	}
	for _, name := range sortedTypeNames(schema.Types) {
		td := schema.Types[name]
		where := fmt.Sprintf("type %q", name)
		checkSlotClassRefs(td.Extensions, td.Owner, where, known, r)
		for _, fd := range td.Fields {
			checkSlotClassRefs(fd.Extensions, td.Owner, fmt.Sprintf("%s field %q", where, fd.Name), known, r)
		}
	}
	for _, set := range schema.OperationSets {
		if set == nil {
			continue
		}
		where := fmt.Sprintf("operation set %q", set.Name)
		checkSlotClassRefs(set.Extensions, "", where, known, r)
		for _, op := range set.Operations {
			if op != nil {
				checkSlotClassRefs(op.Extensions, "", fmt.Sprintf("%s operation %q", where, op.Name), known, r)
			}
		}
	}
}

// checkSlotClassRefs reports each class reference in one node's extensions
// whose class known rejects. Each slot is an object keyed by decorator name.
func checkSlotClassRefs(exts map[string]json.RawMessage, file, where string, known func(string) bool, r *Result) {
	for _, ext := range sortedTypeNames(exts) {
		var decorators map[string]any
		if err := json.Unmarshal(exts[ext], &decorators); err != nil {
			// The readers and the codecs only store objects here.
			continue
		}
		for _, decorator := range sortedTypeNames(decorators) {
			for _, ref := range classRefsIn(decorators[decorator]) {
				if !known(ref.Class) {
					r.errorf(file, "%s: @%s names class %q, which this schema neither declares nor imports", where, decorator, ref.Class)
				}
			}
		}
	}
}

// classRefsIn returns the class references in a decoded JSON value, objects'
// keys in sorted order. A class reference's own value is not searched.
func classRefsIn(v any) []ir.ClassRef {
	if ref, ok := ir.AsClassRef(v); ok {
		return []ir.ClassRef{ref}
	}
	var refs []ir.ClassRef
	switch v := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			refs = append(refs, classRefsIn(v[k])...)
		}
	case []any:
		for _, e := range v {
			refs = append(refs, classRefsIn(e)...)
		}
	}
	return refs
}
