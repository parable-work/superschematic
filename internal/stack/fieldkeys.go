package stack

import (
	ir "github.com/parable-work/superschematic/ir"
)

// checkFieldKeys refuses a deployable that runs code on a database with
// Encrypted<T> fields and reads no binding of the secret that holds their
// keys (D85). The secret reaches each server, job, worker and app as any
// @envVars Secret<T> does, through its API's config, so the build of an
// API on such a database already refuses one whose @envVars lacks it; this
// holds the stack to the same, since an environment's secret binding is
// what each target's lowering hands the deployable (local secrets.env,
// Secret Manager, the Secrets Store).
func (r *resolver) checkFieldKeys() {
	for _, name := range sortedKeys(r.deployables) {
		d := r.deployables[name].res
		if !d.Kind.RunsCode() {
			continue
		}
		for _, served := range d.Services {
			svc := r.services[served.Name]
			if svc == nil {
				continue
			}
			db, ok := r.databaseOf(svc)
			if !ok || r.services[db] == nil || r.services[db].FieldKeys == "" {
				continue
			}
			want := r.services[db].FieldKeys
			bound := false
			for _, b := range d.Bindings {
				if b.Source == ir.BindingSecret && b.Secret == want {
					bound = true
				}
			}
			if !bound {
				r.fail(CodeUnrealizable, "%s %s serves %s, whose database %s keeps Encrypted<T> fields under the keys of the secret %s, and it reads no such secret; give %s's @envVars class that field, declared by its type (D85)",
					d.Kind, d.Name, served.Name, db, want, served.Name)
			}
		}
	}
}
