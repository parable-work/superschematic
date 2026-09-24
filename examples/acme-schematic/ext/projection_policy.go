package ext

import (
	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
)

// ProjectionScopeRule is the name of acme's projection policy check.
const ProjectionScopeRule = "acmeProjectionScope"

// registerProjectionPolicy adds acme's policy for projection views: every
// @projection in a DB schema must open its where list with an unconditional,
// required binding of the scope setting [extension.acme]
// projection_scope_setting names, so no view serves one shop's rows to
// another. The core requires no rule of its own; the DB kind is the core's,
// so the policy is a registered check rather than a KindSpec.Verify. With
// the key unset, acme registers no check.
func registerProjectionPolicy(r *registry.Registry, cfg Config) error {
	setting := cfg.ProjectionScopeSetting
	if setting == "" {
		return nil
	}
	return r.RegisterCheck(registry.CheckSpec{
		Name:      ProjectionScopeRule,
		Extension: Name,
		Kinds:     []string{string(ir.SchemaKindDB)},
		Verify: func(schema *ir.Schema, rep registry.VerifyReporter) {
			for _, td := range schema.Projections() {
				if td.Projection == nil || !scopedBy(td.Projection, setting) {
					rep.Errorf(td.Owner, "%s: the first where rule of a projection must bind %s, unconditionally and not optional", td.Name, setting)
				}
			}
		},
	})
}

// scopedBy reports whether a projection's first row rule is a plain,
// required, unconditional binding of setting.
func scopedBy(def *ir.ProjectionDef, setting string) bool {
	if len(def.Predicates) == 0 {
		return false
	}
	first := def.Predicates[0]
	return first.IsBinding() && !first.Optional && first.When == nil && first.Setting == setting
}
