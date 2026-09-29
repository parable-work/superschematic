package engine

import (
	"errors"
	"strings"

	versiongraph "github.com/parable-work/superschematic/runtime/versiongraph/go"
	"github.com/parable-work/superschematic/runtime/versiongraph/go/storage"
)

// The engine's named errors. A write through a stale ref version returns
// ErrVersionConflict, which wraps ErrNotFound; a ref or commit that does not
// exist, or a discarded ref, returns ErrNotFound. An input the core refuses
// is a *versiongraph.Error.
var (
	// ErrNotFound is returned for a ref or a commit that does not exist,
	// and for a discarded ref.
	ErrNotFound = storage.ErrNotFound

	// ErrVersionConflict is returned by a write through a ref that has
	// moved past the version the caller expects.
	ErrVersionConflict = storage.ErrVersionConflict

	// ErrNameTaken is returned when the root already has a live ref of the
	// name.
	ErrNameTaken = storage.ErrNameTaken

	// ErrNoActor is returned by a write with no actor to record.
	ErrNoActor = errors.New("the write has no actor")

	// ErrRefSealed is returned by a write through a sealed ref.
	ErrRefSealed = errors.New("the ref is sealed")

	// ErrNothingToCommit is returned by Commit when the ref composes to the
	// tree of its last commit (or of its base).
	ErrNothingToCommit = errors.New("nothing to commit")

	// ErrWalkCeiling is returned when reading a commit's tree would walk
	// more parent commits than the engine's walk ceiling.
	ErrWalkCeiling = errors.New("the commit walk passed its ceiling")

	// ErrSchemaEpoch is returned when a commit was written at a newer
	// schema epoch than the engine's.
	ErrSchemaEpoch = errors.New("the commit is from a newer schema epoch")

	// ErrEntityNotFound is returned by an edit that deletes an entity the
	// ref does not hold, or unsets an override the ref does not have.
	ErrEntityNotFound = errors.New("entity not found on the ref")

	// ErrHistoryMissing is returned when a row version a commit names is no
	// longer in its table's history.
	ErrHistoryMissing = errors.New("a row version a commit names is missing from history")

	// ErrInvalidTree is returned by a commit whose tree breaks the graph's
	// rules; the error is an *InvalidTreeError that lists them.
	ErrInvalidTree = errors.New("the tree breaks the graph's rules")

	// ErrRootMismatch is returned when two refs, or a ref and a commit, of
	// one operation belong to different roots.
	ErrRootMismatch = errors.New("the ref and the commit or ref it names have different roots")

	// ErrMergeIntoItself is returned by a merge whose source is its target.
	ErrMergeIntoItself = errors.New("a ref cannot be merged into itself")

	// ErrPrimaryMergeOnly is returned by a Save, Commit, Seal or Revert on
	// a primary line, which takes writes only from Merge.
	ErrPrimaryMergeOnly = errors.New("a primary line takes writes only from a merge")

	// ErrNotTagged is returned by a Release of a commit that is not tagged.
	ErrNotTagged = errors.New("the commit is not tagged")

	// ErrNoParent is returned by a Rebase of a primary line, which has no
	// parent to rebase onto.
	ErrNoParent = errors.New("a primary line has no parent to rebase onto")
)

// InvalidTreeError lists what the core's validate found wrong with a tree.
// It wraps ErrInvalidTree.
type InvalidTreeError struct {
	Findings []versiongraph.Finding
}

func (e *InvalidTreeError) Error() string {
	messages := make([]string, 0, len(e.Findings))
	for _, finding := range e.Findings {
		messages = append(messages, finding.Kind+": "+finding.Message)
	}
	return ErrInvalidTree.Error() + ": " + strings.Join(messages, "; ")
}

func (e *InvalidTreeError) Unwrap() error { return ErrInvalidTree }

// codes are the stable codes of the named errors, most specific first: an
// ErrVersionConflict is also an ErrNotFound.
var codes = []struct {
	err  error
	code string
}{
	{ErrVersionConflict, "version_conflict"},
	{ErrNotFound, "not_found"},
	{ErrNameTaken, "name_taken"},
	{ErrNoActor, "no_actor"},
	{ErrRefSealed, "ref_sealed"},
	{ErrNothingToCommit, "nothing_to_commit"},
	{ErrWalkCeiling, "walk_ceiling"},
	{ErrSchemaEpoch, "schema_epoch"},
	{ErrEntityNotFound, "entity_not_found"},
	{ErrHistoryMissing, "history_missing"},
	{ErrInvalidTree, "invalid_tree"},
	{ErrRootMismatch, "root_mismatch"},
	{ErrMergeIntoItself, "merge_into_itself"},
	{ErrPrimaryMergeOnly, "primary_merge_only"},
	{ErrNotTagged, "not_tagged"},
	{ErrNoParent, "no_parent"},
}

// ErrorCode is the stable code of err, which every language's engine
// shares and the scenario files name: one of the named errors' codes
// ("version_conflict", "ref_sealed", ...), or the core's code for an input
// it refused ("unmatched_resolution", ...). It is "" for any other error.
func ErrorCode(err error) string {
	for _, c := range codes {
		if errors.Is(err, c.err) {
			return c.code
		}
	}
	var coreErr *versiongraph.Error
	if errors.As(err, &coreErr) {
		return coreErr.Code
	}
	return ""
}
