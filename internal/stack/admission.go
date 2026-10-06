package stack

import (
	"fmt"
	"slices"

	ir "github.com/parable-work/superschematic/ir"
)

// checkCalls is the check D37 adds to those of section 5.2
// (docs/stack-model.md, section 9.3): every calls edge from a server C to
// an API A reaches at least one operation of A that C may invoke. C may
// invoke an operation open to anyone; an @allowService one that lists C; a
// @requireService one that lists C, when it has no user clause or C can
// forward an end user; and one with a user clause and no @requireService,
// when C can forward an end user. A clause lists C when its from is empty
// or names an API C serves, and C can forward an end user when an API it
// serves has an operation with a user clause. A from handle naming a
// service the stack does not deploy lists no server here, and is not an
// error: an API is written once and deployed in many stacks.
func (r *resolver) checkCalls() {
	for _, name := range sortedKeys(r.deployables) {
		d := r.deployables[name]
		if d.res.Kind != ir.DeployableServer || len(d.res.Calls) == 0 {
			continue
		}
		serves := map[string]bool{}
		forwards := false
		for _, served := range d.res.Services {
			serves[served.Name] = true
			if slices.ContainsFunc(r.services[served.Name].Operations, func(op Operation) bool { return op.UserClause }) {
				forwards = true
			}
		}
		for _, call := range d.res.Calls {
			api := r.services[call.Name]
			if slices.ContainsFunc(api.Operations, func(op Operation) bool { return admits(op, serves, forwards) }) {
				continue
			}
			msg := fmt.Sprintf("%s calls %s, but no %s operation admits %s", d.res.Name, api.Name, api.Name, d.res.Name)
			if !forwards {
				msg += fmt.Sprintf(" (%s forwards no end user: no API it serves has an operation with a user clause)", d.res.Name)
			}
			r.fail(CodeUnreachableEdge, "%s", msg)
		}
	}
}

// admits reports whether op admits a server that serves the APIs in serves,
// and that can forward an end user when forwards is set.
func admits(op Operation, serves map[string]bool, forwards bool) bool {
	clause := op.ServiceCallers
	switch {
	case clause == nil:
		return !op.UserClause || forwards
	case clause.Mode == ir.ServiceCallersAllow:
		return lists(clause, serves) || (op.UserClause && forwards)
	}
	return lists(clause, serves) && (!op.UserClause || forwards)
}

// lists reports whether a service clause lists a server that serves the
// APIs in serves: from is empty, or names one of them.
func lists(clause *ir.ServiceCallers, serves map[string]bool) bool {
	if len(clause.From) == 0 {
		return true
	}
	return slices.ContainsFunc(clause.From, func(name string) bool { return serves[name] })
}
