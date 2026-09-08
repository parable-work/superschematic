package verify

import (
	ir "github.com/parable-work/superschematic/ir"
)

// checkTraits asserts trait shapes on the assembled IR. A configurable trait
// declares a `<Config extends TraitConfig>` generic whose resolved schema
// the readers capture as TypeDef.TraitConfig; each implementer's resolved
// Config arguments land in TraitRef.ConfigArgs. The pass asserts the two
// sides against each other:
//
//   - implementing a non-trait type is an error;
//   - supplying config arguments to a marker (non-configurable) trait is an
//     error;
//   - a configurable trait's required Config fields must all be supplied;
//   - unknown Config keys are errors when the trait declares a closed Config
//     schema (an empty schema means the constraint was the open TraitConfig
//     shape, which accepts any keys);
//   - argument values must match the declared primitive field types.
//
// Traits resolved from other services are skipped: the import validation and
// the owning service's own verification cover them.
func checkTraits(schema *ir.Schema, r *Result) {
	for _, name := range sortedTypeNames(schema.Types) {
		td := schema.Types[name]
		for i := range td.Implements {
			tr := &td.Implements[i]
			trait, ok := schema.Types[tr.Name]
			if !ok {
				continue
			}
			if !trait.IsTrait && trait.Role != ir.RoleTrait {
				r.errorf(td.Owner, "%s implements %s, which is not a trait", td.Name, tr.Name)
				continue
			}
			if trait.TraitConfig == nil {
				if len(tr.ConfigArgs) > 0 {
					r.errorf(td.Owner, "trait %s takes no configuration but %s supplies config arguments", tr.Name, td.Name)
				}
				continue
			}
			checkTraitConfigArgs(td, tr, trait, r)
		}
	}
}

// checkTraitConfigArgs asserts one implementer's Config arguments against
// the trait's declared Config schema.
func checkTraitConfigArgs(td *ir.TypeDef, tr *ir.TraitRef, trait *ir.TypeDef, r *Result) {
	declared := make(map[string]*ir.FieldDef, len(trait.TraitConfig.Fields))
	for _, cf := range trait.TraitConfig.Fields {
		declared[cf.Name] = cf
		if cf.Required {
			if _, supplied := tr.ConfigArgs[cf.Name]; !supplied {
				r.errorf(td.Owner, "%s implements %s without required config %q", td.Name, tr.Name, cf.Name)
			}
		}
	}

	// An empty declared schema means the constraint was the open TraitConfig
	// shape: any keys are accepted.
	open := len(trait.TraitConfig.Fields) == 0

	for _, key := range sortedTypeNames(tr.ConfigArgs) {
		cf, known := declared[key]
		if !known {
			if !open {
				r.errorf(td.Owner, "%s supplies unknown config %q to trait %s", td.Name, key, tr.Name)
			}
			continue
		}
		if !configValueMatches(cf.TypeRef, tr.ConfigArgs[key]) {
			r.errorf(td.Owner, "%s config %q for trait %s must be a %s",
				td.Name, key, tr.Name, typeRefString(cf.TypeRef))
		}
	}
}

// configValueMatches checks a resolved config argument value against the
// declared field type for the host-language primitives. Non-primitive field
// types are accepted as-is (the authoring compiler enforced them).
func configValueMatches(ref ir.TypeRef, value any) bool {
	if ref.IsArray || ref.IsMap {
		return true
	}
	switch ref.Name {
	case "string":
		_, ok := value.(string)
		return ok
	case "number":
		switch value.(type) {
		case float64, int:
			return true
		}
		return false
	case "boolean":
		_, ok := value.(bool)
		return ok
	}
	return true
}
