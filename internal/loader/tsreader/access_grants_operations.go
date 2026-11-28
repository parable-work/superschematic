package tsreader

import (
	"github.com/parable-work/superschematic/internal/loader/verify"
	ir "github.com/parable-work/superschematic/ir"
)

// The active grants as a route (#735), @accessGrantsOperations({ ... })
// from @superschematic/api on a class with no members:
//
//	@accessGrantsOperations({ permission: "access.admin" })
//	export abstract class Grants {}
//
// The walker reads such a class as an operation set with no operations,
// and the registry's Apply records the options on it
// (OperationSet.AccessGrantsOperations) and the class's @auth,
// @requirePermission and @publicRoute as the set's clause. The loader fills
// the set once its checks pass. The walker refuses, at their nodes, what
// the class cannot carry: a member, and an Authenticated or Encrypted base.
// The verification pass checks the same for every form, and the rest.

// accessGrantsOperationsDecorator returns the class's
// @accessGrantsOperations, or nil.
func accessGrantsOperationsDecorator(decorators []decoratorRef) *decoratorRef {
	for i := range decorators {
		if decorators[i].id.is("@superschematic/api", "accessGrantsOperations") {
			return &decorators[i]
		}
	}
	return nil
}

// checkAccessGrantsOperationsClass refuses what an @accessGrantsOperations
// class cannot carry.
func (w *walker) checkAccessGrantsOperationsClass(node *astNode, set *ir.OperationSet, bases identityRoutesBases) {
	if bases.authenticated != nil {
		w.addErr(errorAtNode(bases.authenticated, "%s", verify.AccessGrantsOperationsMessage(set, verify.AccessGrantsOperationsAuthenticated)))
	}
	if bases.encrypted != nil {
		w.addErr(errorAtNode(bases.encrypted, "%s", verify.AccessGrantsOperationsMessage(set, verify.AccessGrantsOperationsEncrypted)))
	}
	for _, m := range node.AsClassDeclaration().Members.Nodes {
		w.addErr(errorAtNode(m, "%s", verify.AccessGrantsOperationsMessage(set, verify.AccessGrantsOperationsMember)))
	}
}
