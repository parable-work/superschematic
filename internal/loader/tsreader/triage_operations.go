package tsreader

import (
	"github.com/parable-work/superschematic/internal/loader/verify"
	ir "github.com/parable-work/superschematic/ir"
)

// The inbound path's triage as routes (#746, D157),
// @triageOperations(Party, { ... }) from @superschematic/api on a class with
// no members:
//
//	@triageOperations(Contact, { refersTo: ContactView })
//	@requirePermission(["parties.triage"])
//	export abstract class Triage {}
//
// The walker reads such a class as an operation set with no operations,
// and the registry's Apply records the party table and the options on it
// (OperationSet.TriageOperations) and the class's @auth,
// @requirePermission and @publicRoute as the set's clause. The loader fills
// the set once its checks pass. The walker refuses, at their nodes, what
// the class cannot carry: a member, and an Authenticated or Encrypted base.
// The verification pass checks the same for every form, and the rest.

// triageOperationsDecorator returns the class's @triageOperations, or nil.
func triageOperationsDecorator(decorators []decoratorRef) *decoratorRef {
	for i := range decorators {
		if decorators[i].id.is("@superschematic/api", "triageOperations") {
			return &decorators[i]
		}
	}
	return nil
}

// checkTriageOperationsClass refuses what a @triageOperations class cannot
// carry.
func (w *walker) checkTriageOperationsClass(node *astNode, set *ir.OperationSet, bases identityRoutesBases) {
	if bases.authenticated != nil {
		w.addErr(errorAtNode(bases.authenticated, "%s", verify.TriageOperationsMessage(set, verify.TriageOperationsAuthenticated)))
	}
	if bases.encrypted != nil {
		w.addErr(errorAtNode(bases.encrypted, "%s", verify.TriageOperationsMessage(set, verify.TriageOperationsEncrypted)))
	}
	for _, m := range node.AsClassDeclaration().Members.Nodes {
		w.addErr(errorAtNode(m, "%s", verify.TriageOperationsMessage(set, verify.TriageOperationsMember)))
	}
}
