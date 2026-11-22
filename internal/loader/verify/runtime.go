package verify

import (
	ir "github.com/parable-work/superschematic/ir"
)

// checkRuntimeFields refuses a Runtime<T> mark where it cannot mean anything
// (D129). Runtime<T> says a service reads an @envVars field again while it
// runs, so the mark belongs on a field of an @envVars class. A Secret<T>
// field cannot be Runtime<T> either: a credential changes with a new secret
// version and a restart, never as a Live value.
func checkRuntimeFields(schema *ir.Schema, r *Result) {
	for _, name := range sortedTypeNames(schema.Types) {
		td := schema.Types[name]
		if td == nil {
			continue
		}
		for _, field := range td.Fields {
			if field == nil || !field.Runtime {
				continue
			}
			if !td.EnvVars {
				r.errorf(td.Owner, "%s.%s: Runtime<T> marks a field of an @envVars class; %s is not @envVars", td.Name, field.Name, td.Name)
			}
			if field.Secret {
				r.errorf(td.Owner, "%s.%s: a Secret<T> field cannot be Runtime<T>; a credential changes with a restart", td.Name, field.Name)
			}
		}
	}
}
