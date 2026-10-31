package tsreader

import (
	"github.com/parable-work/superschematic/internal/loader/verify"
	ir "github.com/parable-work/superschematic/ir"
)

// A table's behavior operations (D103), @behaviorOperations(Type) from
// @superschematic/api on a class with no members:
//
//	@behaviorOperations(Order)
//	@requirePermission(["orders.write"])
//	export abstract class OrderActions {}
//
// The walker reads such a class as an operation set with no operations,
// and the registry's Apply records the table on it
// (OperationSet.BehaviorOperations) and the class's @auth,
// @requirePermission and @publicRoute as the set's clause. The loader fills
// the set once its checks pass. The walker refuses, at their nodes, what
// the class cannot carry: a member, an Authenticated or Encrypted base,
// and the set's clause on any other set. The verification pass checks the
// same for every form, and the rest: the schema's kind, the table and the
// API's database.

// behaviorOperationsDecorator returns the class's @behaviorOperations, or
// nil.
func behaviorOperationsDecorator(decorators []decoratorRef) *decoratorRef {
	for i := range decorators {
		if decorators[i].id.is("@superschematic/api", "behaviorOperations") {
			return &decorators[i]
		}
	}
	return nil
}

// checkBehaviorOperationsClass refuses what a @behaviorOperations class
// cannot carry.
func (w *walker) checkBehaviorOperationsClass(node *astNode, set *ir.OperationSet, bases identityRoutesBases) {
	if bases.authenticated != nil {
		w.addErr(errorAtNode(bases.authenticated, "%s", verify.BehaviorOperationsMessage(set, verify.BehaviorOperationsAuthenticated)))
	}
	if bases.encrypted != nil {
		w.addErr(errorAtNode(bases.encrypted, "%s", verify.BehaviorOperationsMessage(set, verify.BehaviorOperationsEncrypted)))
	}
	for _, m := range node.AsClassDeclaration().Members.Nodes {
		w.addErr(errorAtNode(m, "%s", verify.BehaviorOperationsMessage(set, verify.BehaviorOperationsMember)))
	}
}

// checkSetClause refuses the class-level @auth, @requirePermission and
// @publicRoute of a set that is not @behaviorOperations, at each
// decorator, with the pass's message.
func (w *walker) checkSetClause(set *ir.OperationSet, decorators []decoratorRef) {
	for _, name := range []string{"auth", "requirePermission", "publicRoute"} {
		for _, d := range findDecorators(decorators, name) {
			if d.id.pkg != "@superschematic/api" {
				continue
			}
			w.addErr(errorAtNode(d.node, "%s", verify.SetClauseMessage(set, "@"+name)))
		}
	}
}
