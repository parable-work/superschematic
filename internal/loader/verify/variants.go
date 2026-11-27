package verify

import (
	"github.com/parable-work/superschematic/internal/generator/codegen"
	ir "github.com/parable-work/superschematic/ir"
)

// checkVariants holds the schema's variant unions (D149), each a
// discriminated-union field type whose member a sibling field names: the
// union's own arms (ir.UnionDef.ValidateVariant), each a member that is
// an object type of the schema; each field of one (codegen.ResolveVariant:
// a single value with no default, beside a string or enum sibling whose
// enum has every arm's value); and no other use of one, as a list or map
// element, an operation's result or an argument, where no sibling names
// the member. A dependency's variant union an API field names is held by
// the generators, which read the dependency (codegen.VariantFieldsOf).
func checkVariants(schema *ir.Schema, in Input, r *Result) {
	for _, name := range sortedTypeNames(schema.Unions) {
		u := schema.Unions[name]
		if !u.IsVariant() && len(u.Arms) == 0 {
			continue
		}
		if err := u.ValidateVariant(); err != nil {
			r.errorf("", "%v (D149)", err)
			continue
		}
		for _, member := range u.Types {
			if td := schema.Types[member]; td == nil || isTable(td) {
				r.errorf("", "union %s's member %s is an object type of this schema stored in its field, not a table (D149)", u.Name, member)
			}
		}
	}
	union := func(name string) *ir.UnionDef { return schema.Unions[name] }
	enum := func(name string) *ir.EnumDef {
		if def := schema.Enums[name]; def != nil {
			return def
		}
		return in.ExternalEnums[name]
	}
	for _, defs := range []map[string]*ir.TypeDef{schema.Types, schema.Inputs} {
		for _, name := range sortedTypeNames(defs) {
			td := defs[name]
			for _, field := range td.Fields {
				if _, err := codegen.ResolveVariant(td, field, union, enum); err != nil {
					r.errorf(td.Owner, "%v", err)
				}
			}
		}
	}
	for _, set := range schema.OperationSets {
		if set == nil {
			continue
		}
		for _, op := range set.Operations {
			if op == nil {
				continue
			}
			if u := schema.Unions[op.TypeRef.Name]; u.IsVariant() {
				r.errorf("", "%s.%s: answers the variant union %s, whose member a sibling field names; answer a type that holds it beside %s (D149)", set.Name, op.Name, u.Name, u.By)
			}
			for _, arg := range op.Arguments {
				if arg == nil {
					continue
				}
				if u := schema.Unions[arg.TypeRef.Name]; u.IsVariant() {
					r.errorf("", "%s.%s: argument %s is of the variant union %s, whose member a sibling field names; take an input type that holds it beside %s (D149)", set.Name, op.Name, arg.Name, u.Name, u.By)
				}
			}
		}
	}
}
