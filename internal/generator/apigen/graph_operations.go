package apigen

import (
	"slices"
	"strconv"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
)

// GraphOperation is the graph operation the endpoint runs (D138): the
// loader's marker on its operation, nil for every authored endpoint.
func (e EndpointInfo) GraphOperation() *ir.GraphOperation {
	if e.Operation == nil {
		return nil
	}
	return e.Operation.GraphOperation
}

// HasGraphOperations reports whether the API serves a version graph's
// operations (D138): an endpoint the loader added for a @graphOperations
// set.
func (o *APIOutput) HasGraphOperations() bool {
	return slices.ContainsFunc(o.Endpoints, func(ep EndpointInfo) bool { return ep.GraphOperation() != nil })
}

// The operations a refusal of a graph operation is answered for, by what
// they do.
var (
	graphWritesAtVersion = []string{
		ir.GraphOpSave, ir.GraphOpArrange, ir.GraphOpCommit, ir.GraphOpSeal, ir.GraphOpMerge,
		ir.GraphOpRebase, ir.GraphOpRevert, ir.GraphOpDiscard, ir.GraphOpRelease,
	}
	graphWrites        = append([]string{ir.GraphOpBranch}, graphWritesAtVersion...)
	graphWritesARef    = []string{ir.GraphOpSave, ir.GraphOpArrange, ir.GraphOpCommit, ir.GraphOpSeal, ir.GraphOpMerge, ir.GraphOpRebase, ir.GraphOpRevert}
	graphWritesAChange = []string{ir.GraphOpSave, ir.GraphOpArrange, ir.GraphOpCommit, ir.GraphOpSeal, ir.GraphOpRevert}
	graphCommits       = []string{ir.GraphOpCommit, ir.GraphOpSeal, ir.GraphOpMerge, ir.GraphOpRebase, ir.GraphOpRevert}
	graphMerges        = []string{ir.GraphOpMerge, ir.GraphOpRebase}
)

// graphOperationErrors are the refusals a graph operation answers beside
// the 400 and 500 of every route (D138), by status, each code with what
// it means and the operations that answer it; none lists every one. The
// TypeScript runtime's graph-routes.ts answers them so.
var graphOperationErrors = []struct {
	status     int
	code       string
	meaning    string
	operations []string
}{
	{403, "no_actor", "the caller's subject is no UUID, which a write records as its actor.", graphWrites},
	{404, "not_found", "no live row of the root holds the key, or the root has no such ref, commit or kind; a change set the set's row rule keeps from the caller is not told apart from one that is not there.", nil},
	{409, "version_conflict", "the ref, or the release pointer, is at another version than the one the body names.", graphWritesAtVersion},
	{409, "ref_sealed", "the ref is sealed.", graphWritesARef},
	{409, "primary_merge_only", "the ref is a primary line, which takes writes only from a merge.", graphWritesAChange},
	{409, "name_taken", "a live ref of the root holds the name.", []string{ir.GraphOpBranch}},
	{409, "nothing_to_commit", "the change set composes to the tree of its last commit.", []string{ir.GraphOpCommit, ir.GraphOpRevert}},
	{409, "not_tagged", "the commit is not tagged.", []string{ir.GraphOpRelease}},
	{409, "no_parent", "the ref is a primary line, which has no parent.", graphMerges},
	{409, "merge_into_itself", "the ref merged into is the ref merged.", []string{ir.GraphOpMerge}},
	{409, "root_mismatch", "a commit the body names belongs to another root.", []string{ir.GraphOpRevert, ir.GraphOpRelease}},
	{409, "schema_epoch", "a commit read is from a newer schema epoch than the server's.", nil},
	{422, "invalid_row", "a row of the edits does not fit its kind's columns.", []string{ir.GraphOpSave}},
	{422, "entity_not_found", "an edit deletes an entity the ref does not hold, or unsets a row the ref does not have.", []string{ir.GraphOpSave}},
	{422, "invalid_arrangement", "keys are not exactly the parent's live children.", []string{ir.GraphOpArrange}},
	{422, "order_not_a_key", "the kind's order is not a string key.", []string{ir.GraphOpArrange}},
	{422, "invalid_tree", "the composed tree breaks the graph's rules; details.findings lists what the core found.", graphCommits},
	{422, "invalid_resolution", "a resolution takes no side and gives no value, or both.", graphMerges},
	{422, "unmatched_resolution", "a resolution names no conflict of the merge.", graphMerges},
}

// addGraphErrorResponses adds to responses the problems the graph
// operation op answers with (D138): each refusal that reaches it, by its
// status.
func addGraphErrorResponses(responses map[string]interface{}, op string) {
	var statuses []int
	meanings := map[int][]string{}
	for _, e := range graphOperationErrors {
		if e.operations != nil && !slices.Contains(e.operations, op) {
			continue
		}
		if _, seen := meanings[e.status]; !seen {
			statuses = append(statuses, e.status)
		}
		meanings[e.status] = append(meanings[e.status], e.code+": "+e.meaning)
	}
	for _, status := range statuses {
		responses[strconv.Itoa(status)] = problemResponse(strings.Join(meanings[status], " "))
	}
}
