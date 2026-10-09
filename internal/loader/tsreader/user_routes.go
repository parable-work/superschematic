package tsreader

import (
	"github.com/parable-work/superschematic/internal/loader/verify"
	ir "github.com/parable-work/superschematic/ir"
)

// The user model's route sets (D50), @userSessions and @userAdministration
// from @superschematic/api, each on a class with no methods:
//
//	@userSessions({ register: true })
//	export class Account {}
//
// The walker reads such a class as an operation set with no operations,
// and the registry's Apply records the decorator's config on it
// (OperationSet.UserSessions or UserAdministration). The loader fills the
// set once the schema verifies. The walker refuses, at their nodes, what
// the class cannot carry: a member, an Authenticated or Encrypted base, a
// service clause, and a second class of the same decorator. The
// verification pass checks the same for every form, and the rest: the
// schema's kind, the path's form, the reserved type names and the authDb.

// identityRoutesDecorator returns the class's @userSessions or
// @userAdministration, the first when it has several, or nil.
func identityRoutesDecorator(decorators []decoratorRef) *decoratorRef {
	for i := range decorators {
		if decorators[i].id.is("@superschematic/api", "userSessions") || decorators[i].id.is("@superschematic/api", "userAdministration") {
			return &decorators[i]
		}
	}
	return nil
}

// identityRoutesBases holds the operation-set bases a route set's class
// extends, which it may not.
type identityRoutesBases struct {
	authenticated, encrypted *astNode
}

// checkIdentityRoutesClass refuses what a route set's class cannot carry.
func (w *walker) checkIdentityRoutesClass(node *astNode, set *ir.OperationSet, decorators []decoratorRef, bases identityRoutesBases) {
	if bases.authenticated != nil {
		w.addErr(errorAtNode(bases.authenticated, "%s", verify.IdentityRoutesMessage(set, verify.IdentityRoutesAuthenticated)))
	}
	if bases.encrypted != nil {
		w.addErr(errorAtNode(bases.encrypted, "%s", verify.IdentityRoutesMessage(set, verify.IdentityRoutesEncrypted)))
	}
	if set.ServiceCallers != nil {
		at := node
		for _, name := range []string{"requireService", "allowService"} {
			if d := findDecorator(decorators, name); d != nil {
				at = d.node
			}
		}
		w.addErr(errorAtNode(at, "%s", verify.IdentityRoutesMessage(set, verify.IdentityRoutesServiceClause)))
	}
	for _, m := range node.AsClassDeclaration().Members.Nodes {
		w.addErr(errorAtNode(m, "%s", verify.IdentityRoutesMessage(set, verify.IdentityRoutesMember)))
	}
	for _, other := range w.schema.OperationSets {
		switch {
		case set.UserSessions != nil && other.UserSessions != nil:
			w.addErr(errorAtNode(node, "%s", verify.IdentityRoutesSecond(set, other, "@userSessions")))
		case set.UserAdministration != nil && other.UserAdministration != nil:
			w.addErr(errorAtNode(node, "%s", verify.IdentityRoutesSecond(set, other, "@userAdministration")))
		}
	}
}
