package verify

import (
	ir "github.com/parable-work/superschematic/ir"
)

// checkDerivedFields refuses an @envVars field of an API whose name
// collides with a config field one of the API's edges derives in a stack:
// the field's name, or one of its variables' (docs/stack-model.md, section
// 3.4). The platform sets the derived field, so a setting of that name
// would be set twice. The naming file's [derived_fields] names the derived
// fields.
func checkDerivedFields(schema *ir.Schema, in Input, r *Result) {
	derived := schema.DerivedConfigFields(in.Naming.OrDefault().DerivedFields.FieldNames())
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
				if ir.DerivedFieldClaims(d.Name, field.Name) {
					r.errorf("", "@envVars field %s of %s collides with %s, the config field the %s %s derives; a stack's platform sets it, so rename the setting", field.Name, typ.Name, d.Name, d.From, d.Service)
				}
			}
		}
	}
}
