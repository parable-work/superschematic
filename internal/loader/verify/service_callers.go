package verify

import (
	"fmt"

	ir "github.com/parable-work/superschematic/ir"
)

// ServiceClauseConflict is why the service clause that reaches an
// operation (@requireService or @allowService, its own or its set's) is
// refused. Section 9.3 of docs/stack-model.md and D37 have the rules.
type ServiceClauseConflict struct {
	// Rule names the refused clause: "@requireService" or "@allowService",
	// or "the set's @requireService" when the operation takes its set's.
	Rule string
	// FromSet reports that the clause is the operation set's.
	FromSet bool
	// With is the decorator the clause contradicts on the operation:
	// "@publicRoute", "@webhook" or "@hmacVerified". It is empty for an
	// @allowService without a user clause.
	With string
	// Reason says why the schema cannot mean both.
	Reason string
}

// On renders the conflict for the operation named op, as the TypeScript
// reader reports it.
func (c ServiceClauseConflict) On(op string) string {
	if c.With == "" {
		return fmt.Sprintf("%s on %s needs a user clause: %s", c.Rule, op, c.Reason)
	}
	return fmt.Sprintf("%s contradicts %s on %s: %s", c.Rule, c.With, op, c.Reason)
}

// String renders the conflict without the operation, for a diagnostic that
// names it first.
func (c ServiceClauseConflict) String() string {
	if c.With == "" {
		return fmt.Sprintf("%s needs a user clause: %s", c.Rule, c.Reason)
	}
	return fmt.Sprintf("%s contradicts %s: %s", c.Rule, c.With, c.Reason)
}

// ServiceCallersConflict reports why the service clause that reaches op in
// set is refused, and false when nothing refuses it or there is none:
//
//   - @requireService or @allowService on an @publicRoute operation, which
//     admits anyone. An @publicRoute operation takes no clause from its
//     set, so only its own conflicts;
//   - either clause, its own or its set's, on a @webhook or @hmacVerified
//     operation: a third party calls it, and a third party holds no service
//     credential;
//   - @allowService, its own or its set's, on an operation without a user
//     clause (@auth, an Authenticated set, @requirePermission or
//     @requireOwnership). An operation only services call is
//     @requireService, so each rule has one spelling.
//
// The TypeScript reader reports it at the decorator, and the verify pass for
// a schema authored as IR. Both clauses on one operation or one set are
// refused where they are declared (the registry's Apply); the data forms
// cannot write both.
func ServiceCallersConflict(set *ir.OperationSet, op *ir.FieldDef) (ServiceClauseConflict, bool) {
	clause := ir.EffectiveServiceCallers(set, op)
	if clause == nil {
		return ServiceClauseConflict{}, false
	}
	c := ServiceClauseConflict{Rule: "@" + serviceClauseDecorator(clause.Mode), FromSet: op.ServiceCallers == nil}
	if c.FromSet {
		c.Rule = "the set's " + c.Rule
	}
	const thirdParty = "a third party calls it, and holds no service credential"
	switch {
	case op.Public:
		c.With, c.Reason = "@publicRoute", "a public route needs no caller"
	case op.Webhook:
		c.With, c.Reason = "@webhook", thirdParty
	case op.HMACVerifiedProvider != "":
		c.With, c.Reason = "@hmacVerified", thirdParty
	case clause.Mode == ir.ServiceCallersAllow && !op.HasUserClause():
		c.Reason = "@auth, an Authenticated set, @requirePermission or @requireOwnership; an operation only services call is @requireService"
	default:
		return ServiceClauseConflict{}, false
	}
	return c, true
}

// serviceClauseDecorator names the decorator that declares mode.
func serviceClauseDecorator(mode ir.ServiceCallersMode) string {
	if mode == ir.ServiceCallersAllow {
		return "allowService"
	}
	return "requireService"
}

// checkServiceCallers refuses, in every format, a service clause whose mode
// is neither require nor allow, and each clause ServiceCallersConflict
// refuses. The TypeScript reader has refused the latter already at the
// decorator; this pass is what holds the rules for a schema authored as IR.
func checkServiceCallers(schema *ir.Schema, r *Result) {
	validMode := func(clause *ir.ServiceCallers) bool {
		return clause == nil || clause.Mode == ir.ServiceCallersRequire || clause.Mode == ir.ServiceCallersAllow
	}
	for _, set := range schema.OperationSets {
		if set == nil {
			continue
		}
		if !validMode(set.ServiceCallers) {
			r.errorf("", "%s: service clause mode %q is not %s or %s", set.Name, set.ServiceCallers.Mode, ir.ServiceCallersRequire, ir.ServiceCallersAllow)
			continue
		}
		for _, op := range set.Operations {
			if op == nil {
				continue
			}
			if !validMode(op.ServiceCallers) {
				r.errorf("", "%s.%s: service clause mode %q is not %s or %s", set.Name, op.Name, op.ServiceCallers.Mode, ir.ServiceCallersRequire, ir.ServiceCallersAllow)
				continue
			}
			if conflict, ok := ServiceCallersConflict(set, op); ok {
				r.errorf("", "%s.%s: %s", set.Name, op.Name, conflict)
			}
		}
	}
}
