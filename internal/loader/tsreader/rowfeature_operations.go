package tsreader

import (
	"github.com/parable-work/superschematic/internal/loader/verify"
	ir "github.com/parable-work/superschematic/ir"
)

// A table's references and files as routes (#723, D154),
// @rowFeatureOperations(Type, { ... }) from @superschematic/api on a class
// with no members:
//
//	@rowFeatureOperations(Issue, { bucket: "issue-files" })
//	@auth
//	export abstract class IssueLinks {}
//
// The walker reads such a class as an operation set with no operations,
// and the registry's Apply records the table and the options on it
// (OperationSet.RowFeatureOperations) and the class's @auth,
// @requirePermission and @publicRoute as the set's clause. The loader fills
// the set once its checks pass. The walker refuses, at their nodes, what
// the class cannot carry: a member, and an Authenticated or Encrypted
// base. The verification pass checks the same for every form, and the
// rest: the schema's kind, the options' names, the table, the API's
// database and its buckets.

// rowFeatureOperationsDecorator returns the class's @rowFeatureOperations,
// or nil.
func rowFeatureOperationsDecorator(decorators []decoratorRef) *decoratorRef {
	for i := range decorators {
		if decorators[i].id.is("@superschematic/api", "rowFeatureOperations") {
			return &decorators[i]
		}
	}
	return nil
}

// checkRowFeatureOperationsClass refuses what a @rowFeatureOperations
// class cannot carry.
func (w *walker) checkRowFeatureOperationsClass(node *astNode, set *ir.OperationSet, bases identityRoutesBases) {
	if bases.authenticated != nil {
		w.addErr(errorAtNode(bases.authenticated, "%s", verify.RowFeatureOperationsMessage(set, verify.RowFeatureOperationsAuthenticated)))
	}
	if bases.encrypted != nil {
		w.addErr(errorAtNode(bases.encrypted, "%s", verify.RowFeatureOperationsMessage(set, verify.RowFeatureOperationsEncrypted)))
	}
	for _, m := range node.AsClassDeclaration().Members.Nodes {
		w.addErr(errorAtNode(m, "%s", verify.RowFeatureOperationsMessage(set, verify.RowFeatureOperationsMember)))
	}
}
