package tsreader

import (
	"github.com/parable-work/superschematic/internal/loader/verify"
	ir "github.com/parable-work/superschematic/ir"
)

// A version graph's operations (D138), @graphOperations(Root, { ... })
// from @superschematic/api on a class with no members:
//
//	@graphOperations(Recipe, { by: ["createdBy"] })
//	@requirePermission(["recipes.write"])
//	export abstract class RecipeOperations {}
//
// The walker reads such a class as an operation set with no operations,
// and the registry's Apply records the root and the options on it
// (OperationSet.GraphOperations) and the class's @auth, @requirePermission
// and @publicRoute as the set's clause. The loader fills the set once its
// checks pass. The walker refuses, at their nodes, what the class cannot
// carry: a member, and an Authenticated or Encrypted base. The
// verification pass checks the same for every form, and the rest: the
// schema's kind, the options' names, the root and the API's database.

// graphOperationsDecorator returns the class's @graphOperations, or nil.
func graphOperationsDecorator(decorators []decoratorRef) *decoratorRef {
	for i := range decorators {
		if decorators[i].id.is("@superschematic/api", "graphOperations") {
			return &decorators[i]
		}
	}
	return nil
}

// checkGraphOperationsClass refuses what a @graphOperations class cannot
// carry.
func (w *walker) checkGraphOperationsClass(node *astNode, set *ir.OperationSet, bases identityRoutesBases) {
	if bases.authenticated != nil {
		w.addErr(errorAtNode(bases.authenticated, "%s", verify.GraphOperationsMessage(set, verify.GraphOperationsAuthenticated)))
	}
	if bases.encrypted != nil {
		w.addErr(errorAtNode(bases.encrypted, "%s", verify.GraphOperationsMessage(set, verify.GraphOperationsEncrypted)))
	}
	for _, m := range node.AsClassDeclaration().Members.Nodes {
		w.addErr(errorAtNode(m, "%s", verify.GraphOperationsMessage(set, verify.GraphOperationsMember)))
	}
}
