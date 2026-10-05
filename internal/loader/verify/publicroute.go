package verify

import (
	ir "github.com/parable-work/superschematic/ir"
)

// PublicRouteConflict names the decorator that contradicts an operation's
// @publicRoute: @auth, @requirePermission or @requireOwnership, each of which
// requires a caller. It is empty when op is not @publicRoute or nothing
// contradicts it. The TypeScript reader does not fold an Authenticated set
// into an @publicRoute operation, so there a conflict is the method's own;
// in a schema authored as IR, `auth` may also stand for the set.
func PublicRouteConflict(op *ir.FieldDef) string {
	if op == nil || !op.Public {
		return ""
	}
	switch {
	case op.Auth:
		return "@auth"
	case len(op.Permissions) > 0:
		return "@requirePermission"
	case op.RequireOwnership:
		return "@requireOwnership"
	}
	return ""
}

// checkPublicRoutes refuses an @publicRoute operation that also requires a
// caller. Every server, the OpenAPI document and the SDKs must agree on
// whether such a route is open, and the schema cannot say both.
func checkPublicRoutes(schema *ir.Schema, r *Result) {
	for _, set := range schema.OperationSets {
		if set == nil {
			continue
		}
		for _, op := range set.Operations {
			if conflict := PublicRouteConflict(op); conflict != "" {
				r.errorf("", "%s.%s: @publicRoute contradicts %s: a public route needs no caller", set.Name, op.Name, conflict)
			}
		}
	}
}
