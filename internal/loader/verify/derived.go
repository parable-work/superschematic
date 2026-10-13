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
// the http edges to it derive (ir.CallersField). An API whose authDb has a
// User table also has its identity config field (ir.IdentityConfigField,
// D50), which each of its servers reads in a stack: in.AuthDB is that
// authDb, which the loader reads for an API with an @envVars field that
// takes the field's name (TakesIdentityConfigField).
func checkDerivedFields(schema *ir.Schema, in Input, r *Result) {
	type derivedField struct{ name, from string }
	var derived []derivedField
	for _, d := range schema.DerivedConfigFields(in.Naming.OrDefault().DerivedFields.FieldNames()) {
		derived = append(derived, derivedField{d.Name, "the config field the " + d.From + " " + d.Service + " derives"})
	}
	if schema.Kind == ir.SchemaKindAPI && schema.HasServiceCallers() {
		derived = append(derived, derivedField{ir.CallersField(schema.Name), "the callers field the http edges to " + schema.Name + " derive"})
	}
	if schema.Kind == ir.SchemaKindAPI && in.AuthDB != nil && in.AuthDB.UserTable() != nil {
		derived = append(derived, derivedField{ir.IdentityConfigField(schema.Name), "the identity config field of " + schema.Name + ", whose authDb " + in.AuthDB.Name + " has a User table"})
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

// TakesIdentityConfigField reports whether an @envVars field of the API
// schema takes the name of its identity config field
// (ir.IdentityConfigField), or a name that begins with it and an
// underscore. The API has that field when its authDb has a User table
// (D50), so the loader reads the authDb of such a schema, as it does for
// one with a user model route set, and checkDerivedFields refuses the
// field.
func TakesIdentityConfigField(schema *ir.Schema) bool {
	if schema.Kind != ir.SchemaKindAPI {
		return false
	}
	identity := ir.IdentityConfigField(schema.Name)
	for _, typ := range schema.Types {
		if typ == nil || !typ.EnvVars {
			continue
		}
		for _, field := range typ.Fields {
			if ir.DerivedFieldClaims(identity, field.Name) {
				return true
			}
		}
	}
	return false
}
