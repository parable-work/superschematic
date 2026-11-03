package verify

import (
	"errors"

	ir "github.com/parable-work/superschematic/ir"
)

// checkEnvVarsTypes refuses a schema whose @envVars classes make no one
// config (D94). A service's config is the @envVars class no other extends
// (ir.Schema.EnvVarsType), which holds the fields of every class it
// extends, so two or more such classes, which no loader, stack or handle
// could choose between, fail the load where the first is written, naming
// each and the fix.
func checkEnvVarsTypes(schema *ir.Schema, r *Result) {
	_, err := schema.EnvVarsType()
	var typesErr *ir.EnvVarsTypesError
	if !errors.As(err, &typesErr) {
		return
	}
	r.errorf(typesErr.Types[0].Owner, "%s", typesErr.Error())
}
