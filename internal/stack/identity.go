package stack

import (
	ir "github.com/parable-work/superschematic/ir"
)

// The identity config of an API over the user model (D50). A served API
// whose server authenticates with the identity runtime (Service.Identity)
// gets an identity config field, named by ir.IdentityConfigField, whose
// value is the runtime's config as JSON: the session's lifetime, the
// cookie, the trusted origins and the password hash's cost. The
// environment sets it through its env settings, a literal or a parameter
// like any config field; without one the server's platform supplies its
// identity config (registry.PlatformSpec.IdentityConfig), as the local
// platform turns the cookie's Secure off over plain HTTP; without either
// the field is unbound and the server runs with the runtime's defaults.

// identityFields returns the identity config fields of server d: one per
// API it serves whose server authenticates with the identity runtime, by
// field name.
func (r *resolver) identityFields(d *deployable) map[string]string {
	fields := map[string]string{}
	for _, served := range d.res.Services {
		if svc := r.services[served.Name]; svc != nil && svc.Identity {
			fields[ir.IdentityConfigField(svc.Name)] = svc.Name
		}
	}
	return fields
}

// checkIdentityFields refuses an identity config field of server d that a
// config field, an edge's derived field or a callers field takes, by its
// name or a name it begins with and an underscore, either way round.
func (r *resolver) checkIdentityFields(d *deployable, identity, callers map[string]string, derived map[string]*edge) {
	for _, name := range sortedKeys(identity) {
		api := identity[name]
		claims := func(other string) bool {
			return ir.DerivedFieldClaims(name, other) || ir.DerivedFieldClaims(other, name)
		}
		for _, fieldName := range sortedKeys(d.fields) {
			if f := d.fields[fieldName]; claims(f.name) {
				r.fail(CodeFieldCollision, "server %s: config field %s of %s collides with %s, the identity config field of %s", d.res.Name, f.name, f.declaring, name, api)
			}
		}
		for _, other := range sortedKeys(derived) {
			if claims(other) {
				r.fail(CodeFieldCollision, "server %s: %s, the field edge %s derives, collides with %s, the identity config field of %s", d.res.Name, other, derived[other].res.ID, name, api)
			}
		}
		for _, other := range sortedKeys(callers) {
			if claims(other) {
				r.fail(CodeFieldCollision, "server %s: %s, the callers field of %s, collides with %s, the identity config field of %s", d.res.Name, other, callers[other], name, api)
			}
		}
	}
}

// identityBindings binds each identity config field of server d: to the
// environment's env setting when it sets one, otherwise to the platform's
// identity config as a default literal, otherwise not at all.
func (r *resolver) identityBindings(d *deployable, identity map[string]string) []*ir.Binding {
	var out []*ir.Binding
	for _, name := range sortedKeys(identity) {
		f := &field{name: name, declaring: "the identity config of " + identity[name]}
		if def := d.platform.IdentityConfig; def != "" {
			f.def = &def
		}
		if b := r.bindField(d, f); b != nil {
			b.IdentityOf = identity[name]
			out = append(out, b)
		}
	}
	return out
}
