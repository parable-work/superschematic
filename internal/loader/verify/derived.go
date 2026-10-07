package verify

import (
	ir "github.com/parable-work/superschematic/ir"
)

// checkDerivedFields refuses an @envVars field of an API whose name
// collides with a config field one of the API's edges derives in a stack:
// the field's name, or one of its variables' (docs/stack-model.md, section
// 3.4). The platform sets the derived field, so a setting of that name
// would be set twice. The naming file's [derived_fields] names the derived
// fields. An API with a service clause also has its callers field, which
// the http edges to it derive (ir.CallersField).
func checkDerivedFields(schema *ir.Schema, in Input, r *Result) {
	type derivedField struct{ name, from string }
	var derived []derivedField
	for _, d := range schema.DerivedConfigFields(in.Naming.OrDefault().DerivedFields.FieldNames()) {
		derived = append(derived, derivedField{d.Name, "the config field the " + d.From + " " + d.Service + " derives"})
	}
	if schema.Kind == ir.SchemaKindAPI && schema.HasServiceCallers() {
		derived = append(derived, derivedField{ir.CallersField(schema.Name), "the callers field the http edges to " + schema.Name + " derive"})
	}
	if len(derived) == 0 {
		return
	}
	for _, name := range sortedTypeNames(schema.Types) {
		typ := schema.Types[name]
		if !typ.EnvVars {
			continue
		}
		for _, field := range typ.Fields {
			for _, d := range derived {
				if ir.DerivedFieldClaims(d.name, field.Name) {
					r.errorf("", "@envVars field %s of %s collides with %s, %s; a stack's platform sets it, so rename the setting", field.Name, typ.Name, d.name, d.from)
				}
			}
		}
	}
}
