package verify

import (
	ir "github.com/parable-work/superschematic/ir"
)

// checkUnknownFields refuses a type that both accepts and refuses the keys
// it does not declare (D87): @allowUnknownFields with @strictJSON, whose
// decoders refuse such a key, or with @denyUnknownFields, whose Rust serde
// decoder does. Every decoder of a type reads its undeclared keys one way.
func checkUnknownFields(schema *ir.Schema, r *Result) {
	for _, name := range sortedTypeNames(schema.Types) {
		td := schema.Types[name]
		if td == nil || !td.AllowUnknownFields {
			continue
		}
		switch {
		case td.StrictJSON:
			r.errorf(td.Owner, "%s: @allowUnknownFields contradicts @strictJSON: one accepts a key the type does not declare, the other refuses it", td.Name)
		case td.DenyUnknownFields:
			r.errorf(td.Owner, "%s: @allowUnknownFields contradicts @denyUnknownFields: one accepts a key the type does not declare, the other refuses it", td.Name)
		}
	}
}
