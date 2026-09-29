// Package engine is the Go engine of the version graph (D17, D19): every
// graph operation, written once over a storage adapter (package storage)
// and the core's Go binding.
//
// The engine reads and writes canonical rows only: JSON objects keyed by
// column name, whose values are each column's canonical JSON
// (runtime/versiongraph/README.md). An adapter normalizes what its database
// returns; a typed facade, such as the one the ORM generator writes per
// graph, turns typed values into canonical rows and back. Every id the
// engine takes or returns is a UUID; it reads the canonical form (base62)
// and the hyphenated one, and returns the canonical form.
//
// Each operation runs in one transaction of the adapter. Every write takes
// an actor, recorded in the audit columns, and every write through a ref
// takes the ref's expected version and fails with ErrVersionConflict when
// the ref has moved on.
//
// A primary line (a ref with no parent) takes writes only from Merge: work
// happens on a change set, which Rebase catches up with its parent's head
// and Merge brings back. A root's release pointer names one tagged commit,
// which Release moves and Released reads. A commit is snapshotted, its full
// pin set stored, when it is tagged, released, or Options.SnapshotEvery
// commits past the nearest snapshot on its chain, and Materialize stops at
// the nearest snapshot. Sweep and RunSweeper are the graph's maintenance.
package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	versiongraph "github.com/parable-work/superschematic/runtime/versiongraph/go"
	"github.com/parable-work/superschematic/runtime/versiongraph/go/canonical"
	"github.com/parable-work/superschematic/runtime/versiongraph/go/storage"
)

// DefaultWalkCeiling is how many commits Materialize reads, walking a
// commit's parents, before it stops with ErrWalkCeiling.
const DefaultWalkCeiling = 4096

// DefaultSnapshotEvery is how many commits past the nearest snapshot on its
// chain a commit is snapshotted at, unless Options.SnapshotEvery says
// otherwise.
const DefaultSnapshotEvery = 64

// Options configure an Engine.
type Options struct {
	// SchemaEpoch is the graph's schema epoch: every commit records it, and
	// Materialize refuses a commit from a newer one.
	SchemaEpoch int64
	// WalkCeiling bounds a commit walk; 0 is DefaultWalkCeiling.
	WalkCeiling int
	// SnapshotEvery is the graph's snapshot interval
	// (@versionGraph({ snapshotEvery })); 0 is DefaultSnapshotEvery.
	SnapshotEvery int
}

// Engine runs one graph's operations.
type Engine struct {
	descriptor    json.RawMessage
	kinds         []kindRoles
	byName        map[string]*kindRoles
	storage       storage.Storage
	schemaEpoch   int64
	walkCeiling   int
	snapshotEvery int
}

// kindRoles are the columns of a kind's rows the engine reads.
type kindRoles struct {
	Name      string `json:"kind"`
	Key       string `json:"key"`
	ID        string `json:"id"`
	Tombstone string `json:"tombstone"`
	Version   string `json:"version"`
}

// New returns the engine of the graph descriptor describes (version 2),
// over s. The core checks the descriptor.
func New(descriptor json.RawMessage, s storage.Storage, opts Options) (*Engine, error) {
	if _, err := versiongraph.Validate(versiongraph.TreeRequest{Descriptor: descriptor, Tree: json.RawMessage(`{}`)}); err != nil {
		return nil, err
	}
	var d struct {
		Kinds []kindRoles `json:"kinds"`
	}
	if err := json.Unmarshal(descriptor, &d); err != nil {
		return nil, fmt.Errorf("engine: read the descriptor: %w", err)
	}
	e := &Engine{
		descriptor:    append(json.RawMessage(nil), descriptor...),
		kinds:         d.Kinds,
		byName:        make(map[string]*kindRoles, len(d.Kinds)),
		storage:       s,
		schemaEpoch:   opts.SchemaEpoch,
		walkCeiling:   opts.WalkCeiling,
		snapshotEvery: opts.SnapshotEvery,
	}
	for i := range e.kinds {
		e.byName[e.kinds[i].Name] = &e.kinds[i]
	}
	if e.walkCeiling <= 0 {
		e.walkCeiling = DefaultWalkCeiling
	}
	if e.snapshotEvery <= 0 {
		e.snapshotEvery = DefaultSnapshotEvery
	}
	return e, nil
}

// WithStorage returns a copy of e over s.
func (e *Engine) WithStorage(s storage.Storage) *Engine {
	copied := *e
	copied.storage = s
	return &copied
}

// WithWalkCeiling returns a copy of e that walks at most n commits to read
// a commit's tree, and fails with ErrWalkCeiling past them. n <= 0 is
// DefaultWalkCeiling.
func (e *Engine) WithWalkCeiling(n int) *Engine {
	copied := *e
	copied.walkCeiling = n
	if n <= 0 {
		copied.walkCeiling = DefaultWalkCeiling
	}
	return &copied
}

// Tree is a tree of canonical rows by kind. A kind with no rows is absent.
type Tree map[string][]json.RawMessage

func (t Tree) add(kind string, row json.RawMessage) {
	t[kind] = append(t[kind], row)
}

func (t Tree) json() (json.RawMessage, error) {
	if len(t) == 0 {
		return json.RawMessage(`{}`), nil
	}
	return json.Marshal(map[string][]json.RawMessage(t))
}

func decodeTree(raw json.RawMessage) (Tree, error) {
	tree := Tree{}
	if err := json.Unmarshal(raw, &tree); err != nil {
		return nil, fmt.Errorf("engine: decode tree: %w", err)
	}
	for kind, rows := range tree {
		if len(rows) == 0 {
			delete(tree, kind)
		}
	}
	return tree, nil
}

// KindEdits are one kind's edits of a ref. Upsert writes each row as the
// ref's row of its entity, found by its entity key; a row without one is a
// new entity, whose key the database generates. Delete writes a row that
// deletes each entity, by entity key, on the ref. Unset removes the ref's
// own row of each entity, so the ref reads the entity through its base
// again.
type KindEdits struct {
	Upsert []json.RawMessage
	Delete []string
	Unset  []string
}

// Edits are a Save's edits by kind. Save applies every kind's upserts, then
// every kind's deletes, then every kind's unsets, each in descriptor order.
type Edits map[string]KindEdits

// SaveResult is a saved ref at its new version and the rows Save upserted,
// as stored, in edit order per kind.
type SaveResult struct {
	Ref   storage.Ref
	Saved Tree
}

// CommitOptions are a commit's message and whether it is tagged: a tagged
// commit takes the root's next sequence.
type CommitOptions struct {
	Message string
	Tag     bool
}

// CommitResult is a ref at its new version and the commit written, nil when
// there was nothing to commit.
type CommitResult struct {
	Ref    storage.Ref
	Commit *storage.Commit
}

// MergeResult is the target of a merge, or the change set a rebase moved,
// and the commit written. When Conflicts is not empty nothing was written
// and Ref is the ref as it was.
type MergeResult struct {
	Ref       storage.Ref
	Commit    *storage.Commit
	Conflicts []versiongraph.Conflict
}

// TreeResult is a tree in the core's order (rows by their order column,
// then by entity key), its content hash, and, for a composed ref, the
// problems compose found.
type TreeResult struct {
	Tree        Tree
	ContentHash string
	Findings    []versiongraph.Finding
}

// ReleasedResult is a root's release pointer and the tree of the commit it
// names.
type ReleasedResult struct {
	Release storage.Release
	TreeResult
}

// id normalizes a UUID argument to its canonical form.
func id(what, value string) (string, error) {
	quoted, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	out, err := canonical.Postgres(canonical.UUID, quoted)
	if err != nil {
		return "", fmt.Errorf("engine: %s: %w", what, err)
	}
	var s string
	err = json.Unmarshal(out, &s)
	return s, err
}

// actorID normalizes a write's actor; an empty one is ErrNoActor.
func actorID(actor string) (string, error) {
	if actor == "" {
		return "", ErrNoActor
	}
	return id("actor", actor)
}

// ids normalizes several UUID arguments in place.
func ids(pairs ...*string) error {
	for _, p := range pairs {
		normalized, err := id("id", *p)
		if err != nil {
			return err
		}
		*p = normalized
	}
	return nil
}

func (e *Engine) kind(name string) (*kindRoles, error) {
	k, ok := e.byName[name]
	if !ok {
		return nil, fmt.Errorf("engine: unknown kind %q", name)
	}
	return k, nil
}

// transact runs fn in one transaction of the engine's storage.
func (e *Engine) transact(ctx context.Context, fn func(ctx context.Context, tx storage.Tx) error) error {
	return e.storage.Transact(ctx, fn)
}

// CreatePrimary creates a primary line of root: a ref with no parent.
func (e *Engine) CreatePrimary(ctx context.Context, actor, root, name string) (storage.Ref, error) {
	actor, err := actorID(actor)
	if err != nil {
		return storage.Ref{}, err
	}
	if err := ids(&root); err != nil {
		return storage.Ref{}, err
	}
	var ref storage.Ref
	err = e.transact(ctx, func(ctx context.Context, tx storage.Tx) error {
		ref, err = tx.CreateRef(ctx, storage.NewRef{Root: root, Name: name, Actor: actor})
		return err
	})
	return ref, err
}

// Branch creates a change set of fromRef whose base is fromRef's head.
func (e *Engine) Branch(ctx context.Context, actor, fromRef, name string) (storage.Ref, error) {
	actor, err := actorID(actor)
	if err != nil {
		return storage.Ref{}, err
	}
	if err := ids(&fromRef); err != nil {
		return storage.Ref{}, err
	}
	var ref storage.Ref
	err = e.transact(ctx, func(ctx context.Context, tx storage.Tx) error {
		from, err := e.readRef(ctx, tx, fromRef, nil, false)
		if err != nil {
			return err
		}
		ref, err = tx.CreateRef(ctx, storage.NewRef{Root: from.Root, Parent: from.ID, Base: from.Head, Name: name, Actor: actor})
		return err
	})
	return ref, err
}

// Save applies edits to a change set at version. It refuses a sealed ref,
// and a primary line with ErrPrimaryMergeOnly.
func (e *Engine) Save(ctx context.Context, actor, ref string, version int64, edits Edits) (*SaveResult, error) {
	actor, err := actorID(actor)
	if err != nil {
		return nil, err
	}
	if err := ids(&ref); err != nil {
		return nil, err
	}
	for name := range edits {
		if _, err := e.kind(name); err != nil {
			return nil, err
		}
	}
	result := &SaveResult{Saved: Tree{}}
	err = e.transact(ctx, func(ctx context.Context, tx storage.Tx) error {
		r, err := e.readDraft(ctx, tx, ref, version)
		if err != nil {
			return err
		}
		deletes := false
		for _, k := range e.kinds {
			for _, row := range edits[k.Name].Upsert {
				stored, err := e.writeRow(ctx, tx, k.Name, r, row, false, actor)
				if err != nil {
					return err
				}
				result.Saved.add(k.Name, stored)
			}
			deletes = deletes || len(edits[k.Name].Delete) > 0
		}
		if deletes {
			composed, _, _, err := e.compose(ctx, tx, r)
			if err != nil {
				return err
			}
			byKey, err := e.index(composed)
			if err != nil {
				return err
			}
			for _, k := range e.kinds {
				for _, key := range edits[k.Name].Delete {
					if err := ids(&key); err != nil {
						return err
					}
					if err := e.deleteEntity(ctx, tx, r, byKey, k.Name, key, actor); err != nil {
						return err
					}
				}
			}
		}
		for _, k := range e.kinds {
			for _, key := range edits[k.Name].Unset {
				if err := ids(&key); err != nil {
					return err
				}
				removed, err := tx.RemoveRow(ctx, k.Name, r.ID, key, actor)
				if err != nil {
					return err
				}
				if !removed {
					return fmt.Errorf("%w: no %s override of %s", ErrEntityNotFound, k.Name, key)
				}
			}
		}
		result.Ref, err = tx.UpdateRef(ctx, storage.RefUpdate{ID: r.ID, Version: r.Version, Actor: actor})
		return err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// Commit composes a change set at version, diffs it against its last
// commit (or its base), writes a commit with a patch per changed entity and
// moves the ref's head. It returns ErrNothingToCommit when nothing changed,
// an *InvalidTreeError when the composed tree breaks the graph's rules, and
// ErrPrimaryMergeOnly for a primary line, whose commits Merge writes.
func (e *Engine) Commit(ctx context.Context, actor, ref string, version int64, opts CommitOptions) (*CommitResult, error) {
	return e.commitRef(ctx, actor, ref, version, opts, false, false)
}

// Seal commits a change set at version when it has changes, and seals it:
// the ref then refuses writes. A primary line is ErrPrimaryMergeOnly.
func (e *Engine) Seal(ctx context.Context, actor, ref string, version int64) (*CommitResult, error) {
	return e.commitRef(ctx, actor, ref, version, CommitOptions{}, true, true)
}

func (e *Engine) commitRef(ctx context.Context, actor, ref string, version int64, opts CommitOptions, allowEmpty, seal bool) (*CommitResult, error) {
	actor, err := actorID(actor)
	if err != nil {
		return nil, err
	}
	if err := ids(&ref); err != nil {
		return nil, err
	}
	result := &CommitResult{}
	err = e.transact(ctx, func(ctx context.Context, tx storage.Tx) error {
		r, err := e.readDraft(ctx, tx, ref, version)
		if err != nil {
			return err
		}
		result.Ref, result.Commit, err = e.commitAndMove(ctx, tx, r, opts, allowEmpty, seal, actor)
		return err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// Merge merges source's head into target at targetVersion, against
// source's base. Without conflicts it writes the result onto target and
// commits it in the same transaction, with opts' message and tag. With
// conflicts left after resolutions it returns them and writes nothing.
// Merge is the only write a primary line takes.
func (e *Engine) Merge(ctx context.Context, actor, source, target string, targetVersion int64, resolutions []versiongraph.Resolution, opts CommitOptions) (*MergeResult, error) {
	actor, err := actorID(actor)
	if err != nil {
		return nil, err
	}
	if err := ids(&source, &target); err != nil {
		return nil, err
	}
	result := &MergeResult{}
	err = e.transact(ctx, func(ctx context.Context, tx storage.Tx) error {
		t, err := e.readRef(ctx, tx, target, &targetVersion, true)
		if err != nil {
			return err
		}
		s, err := e.readRef(ctx, tx, source, nil, false)
		if err != nil {
			return err
		}
		if s.Root != t.Root {
			return ErrRootMismatch
		}
		if s.ID == t.ID {
			return ErrMergeIntoItself
		}
		conflicts, err := e.merge(ctx, tx, s, t, resolutions, actor)
		if err != nil {
			return err
		}
		if len(conflicts) > 0 {
			result.Ref, result.Conflicts = t, conflicts
			return nil
		}
		result.Ref, result.Commit, err = e.commitAndMove(ctx, tx, t, opts, true, false, actor)
		return err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// Rebase moves a change set at version onto its parent's head. It merges
// the parent's head into the change set, with the change set's base as the
// merge base and its composed tree, uncommitted work included, as ours.
// With conflicts left after resolutions it returns them and writes nothing.
// Otherwise it writes the change set's rows so it composes to the merged
// tree over the parent's head, makes that head its base, and commits on it
// with its previous head (or, with none, its new base) as the parent, so
// its History keeps its commits. A primary line has no parent to rebase
// onto: ErrNoParent.
func (e *Engine) Rebase(ctx context.Context, actor, draft string, version int64, resolutions []versiongraph.Resolution) (*MergeResult, error) {
	actor, err := actorID(actor)
	if err != nil {
		return nil, err
	}
	if err := ids(&draft); err != nil {
		return nil, err
	}
	result := &MergeResult{}
	err = e.transact(ctx, func(ctx context.Context, tx storage.Tx) error {
		d, err := e.readRef(ctx, tx, draft, &version, true)
		if err != nil {
			return err
		}
		if d.Parent == "" {
			return ErrNoParent
		}
		parent, err := e.readRef(ctx, tx, d.Parent, nil, false)
		if err != nil {
			return err
		}
		if parent.Head == d.Base {
			// Already on the parent's head: nothing to merge.
			result.Ref, err = tx.UpdateRef(ctx, storage.RefUpdate{ID: d.ID, Version: d.Version, Actor: actor})
			return err
		}
		base, err := e.materialize(ctx, tx, d.Base)
		if err != nil {
			return err
		}
		theirs, err := e.materialize(ctx, tx, parent.Head)
		if err != nil {
			return err
		}
		ours, _, own, err := e.compose(ctx, tx, d)
		if err != nil {
			return err
		}
		merged, conflicts, err := e.coreMerge(base, ours, theirs, resolutions)
		if err != nil {
			return err
		}
		if len(conflicts) > 0 {
			result.Ref, result.Conflicts = d, conflicts
			return nil
		}
		moved := d
		moved.Base = parent.Head
		if err := e.overlay(ctx, tx, moved, theirs, merged, ours, own, actor); err != nil {
			return err
		}
		var written *storage.Commit
		commit, err := e.commit(ctx, tx, moved, CommitOptions{}, actor)
		switch {
		case err == nil:
			written = &commit
		case errors.Is(err, ErrNothingToCommit):
		default:
			return err
		}
		update := storage.RefUpdate{ID: d.ID, Version: d.Version, Base: parent.Head, Actor: actor}
		if written != nil {
			update.Head = written.ID
		}
		result.Commit = written
		result.Ref, err = tx.UpdateRef(ctx, update)
		return err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// Revert writes the rows that make a change set at version compose to the
// tree of toCommit, and commits them. History is never rewritten. A primary
// line is ErrPrimaryMergeOnly: revert a change set of it and merge that.
func (e *Engine) Revert(ctx context.Context, actor, ref string, version int64, toCommit string) (*CommitResult, error) {
	actor, err := actorID(actor)
	if err != nil {
		return nil, err
	}
	if err := ids(&ref, &toCommit); err != nil {
		return nil, err
	}
	result := &CommitResult{}
	err = e.transact(ctx, func(ctx context.Context, tx storage.Tx) error {
		r, err := e.readDraft(ctx, tx, ref, version)
		if err != nil {
			return err
		}
		commit, err := tx.ReadCommit(ctx, toCommit)
		if err != nil {
			return err
		}
		if commit.Root != r.Root {
			return ErrRootMismatch
		}
		tree, err := e.materialize(ctx, tx, toCommit)
		if err != nil {
			return err
		}
		if err := e.revert(ctx, tx, r, tree, actor); err != nil {
			return err
		}
		result.Ref, result.Commit, err = e.commitAndMove(ctx, tx, r, CommitOptions{}, true, false, actor)
		return err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// Release points root's release at commit, a tagged commit of root, fenced
// by the pointer's version: 0 for the root's first release. It snapshots
// the commit and writes no member rows, so a rollback is a Release to an
// earlier tagged commit, and the pointer's history is the release log. An
// untagged commit is ErrNotTagged; another root's is ErrRootMismatch.
func (e *Engine) Release(ctx context.Context, actor, root, commit string, version int64) (storage.Release, error) {
	actor, err := actorID(actor)
	if err != nil {
		return storage.Release{}, err
	}
	if err := ids(&root, &commit); err != nil {
		return storage.Release{}, err
	}
	var release storage.Release
	err = e.transact(ctx, func(ctx context.Context, tx storage.Tx) error {
		c, err := tx.ReadCommit(ctx, commit)
		if err != nil {
			return err
		}
		if c.Root != root {
			return ErrRootMismatch
		}
		if c.Sequence == nil {
			return ErrNotTagged
		}
		if _, err := e.ensureSnapshot(ctx, tx, c); err != nil {
			return err
		}
		release, err = tx.WriteRelease(ctx, storage.ReleaseWrite{Root: root, Commit: c.ID, Version: version, Actor: actor})
		return err
	})
	return release, err
}

// Released reads root's release pointer and the tree of the commit it
// names. A root that has never been released is ErrNotFound.
func (e *Engine) Released(ctx context.Context, root string) (*ReleasedResult, error) {
	if err := ids(&root); err != nil {
		return nil, err
	}
	var result *ReleasedResult
	err := e.transact(ctx, func(ctx context.Context, tx storage.Tx) error {
		release, err := tx.ReadRelease(ctx, root)
		if err != nil {
			return err
		}
		tree, err := e.materialize(ctx, tx, release.Commit)
		if err != nil {
			return err
		}
		if tree, err = e.order(tree); err != nil {
			return err
		}
		read, err := e.treeResult(tree, nil)
		if err != nil {
			return err
		}
		result = &ReleasedResult{Release: release, TreeResult: *read}
		return nil
	})
	return result, err
}

// Materialize reads a commit's tree by walking its parents: each entity's
// nearest patch wins, and a DELETE removes it.
func (e *Engine) Materialize(ctx context.Context, commit string) (*TreeResult, error) {
	if err := ids(&commit); err != nil {
		return nil, err
	}
	var result *TreeResult
	err := e.transact(ctx, func(ctx context.Context, tx storage.Tx) error {
		tree, err := e.materialize(ctx, tx, commit)
		if err != nil {
			return err
		}
		if tree, err = e.order(tree); err != nil {
			return err
		}
		result, err = e.treeResult(tree, nil)
		return err
	})
	return result, err
}

// Compose reads a ref's tree: its base commit's tree with the ref's own
// rows laid over it.
func (e *Engine) Compose(ctx context.Context, ref string) (*TreeResult, error) {
	if err := ids(&ref); err != nil {
		return nil, err
	}
	var result *TreeResult
	err := e.transact(ctx, func(ctx context.Context, tx storage.Tx) error {
		r, err := e.readRef(ctx, tx, ref, nil, false)
		if err != nil {
			return err
		}
		tree, findings, _, err := e.compose(ctx, tx, r)
		if err != nil {
			return err
		}
		result, err = e.treeResult(tree, findings)
		return err
	})
	return result, err
}

// Diff lists the entities the trees of two commits differ on.
func (e *Engine) Diff(ctx context.Context, from, to string) ([]versiongraph.Change, error) {
	if err := ids(&from, &to); err != nil {
		return nil, err
	}
	var changes []versiongraph.Change
	err := e.transact(ctx, func(ctx context.Context, tx storage.Tx) error {
		fromTree, err := e.materialize(ctx, tx, from)
		if err != nil {
			return err
		}
		toTree, err := e.materialize(ctx, tx, to)
		if err != nil {
			return err
		}
		changes, err = e.diff(fromTree, toTree)
		return err
	})
	return changes, err
}

// History lists the commits a ref wrote, newest first.
func (e *Engine) History(ctx context.Context, ref string) ([]storage.Commit, error) {
	if err := ids(&ref); err != nil {
		return nil, err
	}
	var commits []storage.Commit
	err := e.transact(ctx, func(ctx context.Context, tx storage.Tx) error {
		r, err := e.readRef(ctx, tx, ref, nil, false)
		if err != nil {
			return err
		}
		if r.Head == "" {
			return nil
		}
		commits, err = tx.RefCommits(ctx, r.ID, r.Head, e.walkCeiling)
		return err
	})
	return commits, err
}

// Discard soft-deletes a ref at version, which frees its name.
func (e *Engine) Discard(ctx context.Context, actor, ref string, version int64) error {
	actor, err := actorID(actor)
	if err != nil {
		return err
	}
	if err := ids(&ref); err != nil {
		return err
	}
	return e.transact(ctx, func(ctx context.Context, tx storage.Tx) error {
		if _, err := e.readRef(ctx, tx, ref, &version, false); err != nil {
			return err
		}
		return tx.DiscardRef(ctx, ref, version, actor)
	})
}

// readRef reads a ref. With expected set it locks the ref's row, and
// refuses a ref at another version, and with write set a sealed ref.
func (e *Engine) readRef(ctx context.Context, tx storage.Tx, id string, expected *int64, write bool) (storage.Ref, error) {
	read := tx.ReadRef
	if expected != nil {
		read = tx.LockRef
	}
	ref, err := read(ctx, id)
	if err != nil {
		return ref, err
	}
	if ref.Discarded {
		return ref, ErrNotFound
	}
	if expected != nil && ref.Version != *expected {
		return ref, ErrVersionConflict
	}
	if write && ref.Sealed {
		return ref, ErrRefSealed
	}
	return ref, nil
}

// readDraft reads a ref to write through at version: a live, unsealed
// change set. A primary line takes writes only from Merge.
func (e *Engine) readDraft(ctx context.Context, tx storage.Tx, id string, version int64) (storage.Ref, error) {
	ref, err := e.readRef(ctx, tx, id, &version, true)
	if err != nil {
		return ref, err
	}
	if ref.Parent == "" {
		return ref, ErrPrimaryMergeOnly
	}
	return ref, nil
}

// row is what the engine reads of one row.
type row struct {
	key       string
	id        string
	version   int64
	tombstone bool
}

func (e *Engine) readRow(kind *kindRoles, raw json.RawMessage) (row, error) {
	var columns map[string]json.RawMessage
	if err := json.Unmarshal(raw, &columns); err != nil {
		return row{}, fmt.Errorf("engine: decode %s row: %w", kind.Name, err)
	}
	var r row
	if err := json.Unmarshal(columns[kind.Key], &r.key); err != nil {
		return row{}, fmt.Errorf("engine: %s row %s: %w", kind.Name, kind.Key, err)
	}
	if err := json.Unmarshal(columns[kind.ID], &r.id); err != nil {
		return row{}, fmt.Errorf("engine: %s row %s: %w", kind.Name, kind.ID, err)
	}
	if err := json.Unmarshal(columns[kind.Version], &r.version); err != nil {
		return row{}, fmt.Errorf("engine: %s row %s: %w", kind.Name, kind.Version, err)
	}
	if raw := columns[kind.Tombstone]; len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &r.tombstone); err != nil {
			return row{}, fmt.Errorf("engine: %s row %s: %w", kind.Name, kind.Tombstone, err)
		}
	}
	return r, nil
}

// index maps each kind's rows by entity key.
func (e *Engine) index(tree Tree) (map[string]map[string]json.RawMessage, error) {
	byKey := map[string]map[string]json.RawMessage{}
	for name, rows := range tree {
		kind, err := e.kind(name)
		if err != nil {
			return nil, err
		}
		byKey[name] = map[string]json.RawMessage{}
		for _, raw := range rows {
			r, err := e.readRow(kind, raw)
			if err != nil {
				return nil, err
			}
			byKey[name][r.key] = raw
		}
	}
	return byKey, nil
}

// ownRows reads every row a ref holds, tombstones included.
func (e *Engine) ownRows(ctx context.Context, tx storage.Tx, ref string) (Tree, error) {
	tree := Tree{}
	for _, k := range e.kinds {
		rows, err := tx.Rows(ctx, k.Name, ref)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			tree.add(k.Name, r)
		}
	}
	return tree, nil
}

// entity names one entity of a tree.
type entity struct{ kind, key string }

// pinSet is a commit's tree as the row versions it holds, by entity.
type pinSet map[entity]storage.SnapshotEntry

// resolve reads the pin set of a commit's tree: it walks the commit's
// parents to the nearest snapshot, takes that snapshot's pins, and lays
// each nearer commit's patches over them, the nearest winning and a DELETE
// removing the entity. It also returns the commit's distance from the
// nearest snapshot on its chain: 0 for a snapshotted commit, else how many
// commits separate them, the snapshot excluded, counting a chain with no
// snapshot from before its first commit. An empty commit is the empty set
// at distance 0.
func (e *Engine) resolve(ctx context.Context, tx storage.Tx, commit string) (pinSet, int, error) {
	pins := pinSet{}
	if commit == "" {
		return pins, 0, nil
	}
	chain, err := tx.Walk(ctx, commit, e.walkCeiling)
	if err != nil {
		return nil, 0, err
	}
	if len(chain) == 0 {
		return nil, 0, ErrNotFound
	}
	for _, c := range chain {
		if c.SchemaEpoch > e.schemaEpoch {
			return nil, 0, fmt.Errorf("%w: commit %s has epoch %d, this graph %d", ErrSchemaEpoch, c.ID, c.SchemaEpoch, e.schemaEpoch)
		}
	}
	last := chain[len(chain)-1]
	distance := len(chain)
	patched := chain
	switch {
	case last.Snapshot:
		entries, err := tx.Snapshot(ctx, last.ID)
		if err != nil {
			return nil, 0, err
		}
		for _, entry := range entries {
			pins[entity{entry.Kind, entry.EntityKey}] = entry
		}
		distance = len(chain) - 1
		patched = chain[:len(chain)-1]
	case last.Parent != "":
		return nil, 0, fmt.Errorf("%w: %d commits", ErrWalkCeiling, e.walkCeiling)
	}
	if len(patched) == 0 {
		return pins, distance, nil
	}
	depth := make(map[string]int, len(patched))
	patchedIDs := make([]string, len(patched))
	for i, c := range patched {
		depth[c.ID] = i
		patchedIDs[i] = c.ID
	}
	patches, err := tx.Patches(ctx, patchedIDs)
	if err != nil {
		return nil, 0, err
	}
	nearest := map[entity]storage.Patch{}
	for _, p := range patches {
		at := entity{p.Kind, p.EntityKey}
		if seen, ok := nearest[at]; !ok || depth[p.Commit] < depth[seen.Commit] {
			nearest[at] = p
		}
	}
	pins.apply(nearest)
	return pins, distance, nil
}

// apply lays patches over the pin set: a DELETE removes its entity, and
// any other patch pins its row version.
func (s pinSet) apply(patches map[entity]storage.Patch) {
	for at, p := range patches {
		if p.Operation == "DELETE" {
			delete(s, at)
			continue
		}
		s[at] = storage.SnapshotEntry{Kind: p.Kind, EntityKey: p.EntityKey, EntityID: p.EntityID, EntityVersion: p.EntityVersion}
	}
}

// entries lists the pin set as a snapshot's entries, ordered by kind and
// entity key.
func (s pinSet) entries() []storage.SnapshotEntry {
	out := make([]storage.SnapshotEntry, 0, len(s))
	for _, entry := range s {
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].EntityKey < out[j].EntityKey
	})
	return out
}

// images reads the history image of every row version a pin set holds.
func (e *Engine) images(ctx context.Context, tx storage.Tx, pins pinSet) (Tree, error) {
	byKind := map[string][]storage.Pin{}
	for _, entry := range pins {
		byKind[entry.Kind] = append(byKind[entry.Kind], storage.Pin{ID: entry.EntityID, Version: entry.EntityVersion})
	}
	tree := Tree{}
	for _, k := range e.kinds {
		want := byKind[k.Name]
		if len(want) == 0 {
			continue
		}
		images, err := tx.Images(ctx, k.Name, want)
		if err != nil {
			return nil, err
		}
		if len(images) != len(want) {
			return nil, fmt.Errorf("%w: %d of %d %s rows", ErrHistoryMissing, len(want)-len(images), len(want), k.Name)
		}
		for _, image := range images {
			tree.add(k.Name, image)
		}
	}
	return tree, nil
}

// materialize reads a commit's tree: the row versions its pin set holds,
// read from history. An empty commit is the empty tree.
func (e *Engine) materialize(ctx context.Context, tx storage.Tx, commit string) (Tree, error) {
	tree, _, _, err := e.materializePins(ctx, tx, commit)
	return tree, err
}

// materializePins is materialize, with the commit's pin set and its
// distance from the nearest snapshot (see resolve).
func (e *Engine) materializePins(ctx context.Context, tx storage.Tx, commit string) (Tree, pinSet, int, error) {
	pins, distance, err := e.resolve(ctx, tx, commit)
	if err != nil {
		return nil, nil, 0, err
	}
	tree, err := e.images(ctx, tx, pins)
	if err != nil {
		return nil, nil, 0, err
	}
	return tree, pins, distance, nil
}

// ensureSnapshot snapshots a commit that has no snapshot yet. It reports
// whether it wrote one: a commit whose tree is empty has no entries to
// write.
func (e *Engine) ensureSnapshot(ctx context.Context, tx storage.Tx, commit storage.Commit) (bool, error) {
	if commit.Snapshot {
		return false, nil
	}
	pins, _, err := e.resolve(ctx, tx, commit.ID)
	if err != nil {
		return false, err
	}
	if len(pins) == 0 {
		return false, nil
	}
	return true, tx.InsertSnapshot(ctx, commit.ID, pins.entries())
}

// compose is core.compose(materialize(ref.base) or empty, the ref's own
// rows). It returns the composed tree, the core's findings and the own rows.
func (e *Engine) compose(ctx context.Context, tx storage.Tx, ref storage.Ref) (Tree, []versiongraph.Finding, Tree, error) {
	base, err := e.materialize(ctx, tx, ref.Base)
	if err != nil {
		return nil, nil, nil, err
	}
	own, err := e.ownRows(ctx, tx, ref.ID)
	if err != nil {
		return nil, nil, nil, err
	}
	baseJSON, err := base.json()
	if err != nil {
		return nil, nil, nil, err
	}
	ownJSON, err := own.json()
	if err != nil {
		return nil, nil, nil, err
	}
	result, err := versiongraph.Compose(versiongraph.ComposeRequest{Descriptor: e.descriptor, Base: baseJSON, Overlay: ownJSON})
	if err != nil {
		return nil, nil, nil, err
	}
	tree, err := decodeTree(result.Tree)
	if err != nil {
		return nil, nil, nil, err
	}
	return tree, result.Findings, own, nil
}

// order returns tree in the core's order: rows by their order column, then
// by entity key. A materialized tree comes out of history in no order.
func (e *Engine) order(tree Tree) (Tree, error) {
	treeJSON, err := tree.json()
	if err != nil {
		return nil, err
	}
	result, err := versiongraph.Compose(versiongraph.ComposeRequest{Descriptor: e.descriptor, Base: treeJSON, Overlay: json.RawMessage(`{}`)})
	if err != nil {
		return nil, err
	}
	return decodeTree(result.Tree)
}

func (e *Engine) contentHash(tree Tree) (string, error) {
	treeJSON, err := tree.json()
	if err != nil {
		return "", err
	}
	result, err := versiongraph.ContentHash(versiongraph.TreeRequest{Descriptor: e.descriptor, Tree: treeJSON})
	if err != nil {
		return "", err
	}
	return result.ContentHash, nil
}

func (e *Engine) treeResult(tree Tree, findings []versiongraph.Finding) (*TreeResult, error) {
	hash, err := e.contentHash(tree)
	if err != nil {
		return nil, err
	}
	return &TreeResult{Tree: tree, ContentHash: hash, Findings: findings}, nil
}

func (e *Engine) diff(from, to Tree) ([]versiongraph.Change, error) {
	fromJSON, err := from.json()
	if err != nil {
		return nil, err
	}
	toJSON, err := to.json()
	if err != nil {
		return nil, err
	}
	result, err := versiongraph.Diff(versiongraph.DiffRequest{Descriptor: e.descriptor, From: fromJSON, To: toJSON})
	if err != nil {
		return nil, err
	}
	return result.Changes, nil
}

// writeRow writes a row onto a ref as the ref's row of its entity: live, or
// with tombstone set, the row that deletes the entity on the ref. It
// returns the row as stored.
func (e *Engine) writeRow(ctx context.Context, tx storage.Tx, kind string, ref storage.Ref, row json.RawMessage, tombstone bool, actor string) (json.RawMessage, error) {
	return tx.UpsertRow(ctx, kind, storage.RowWrite{Ref: ref.ID, Root: ref.Root, Row: row, Tombstone: tombstone, Actor: actor})
}

// deleteEntity writes the row that deletes an entity on a ref: a copy of
// the entity's effective row, so every required column holds, with its
// tombstone set.
func (e *Engine) deleteEntity(ctx context.Context, tx storage.Tx, ref storage.Ref, composed map[string]map[string]json.RawMessage, kind, key, actor string) error {
	row, ok := composed[kind][key]
	if !ok {
		return fmt.Errorf("%w: %s %s", ErrEntityNotFound, kind, key)
	}
	_, err := e.writeRow(ctx, tx, kind, ref, row, true, actor)
	return err
}

// commit composes the ref, diffs it against its last commit (or its base)
// and writes a commit with a patch per changed entity. It returns the new
// commit, or ErrNothingToCommit. The caller moves the ref's head.
func (e *Engine) commit(ctx context.Context, tx storage.Tx, ref storage.Ref, opts CommitOptions, actor string) (storage.Commit, error) {
	composed, _, own, err := e.compose(ctx, tx, ref)
	if err != nil {
		return storage.Commit{}, err
	}
	composedJSON, err := composed.json()
	if err != nil {
		return storage.Commit{}, err
	}
	validated, err := versiongraph.Validate(versiongraph.TreeRequest{Descriptor: e.descriptor, Tree: composedJSON})
	if err != nil {
		return storage.Commit{}, err
	}
	if len(validated.Findings) > 0 {
		return storage.Commit{}, &InvalidTreeError{Findings: validated.Findings}
	}
	parent := ref.Head
	if parent == "" {
		parent = ref.Base
	}
	previous, pins, distance, err := e.materializePins(ctx, tx, parent)
	if err != nil {
		return storage.Commit{}, err
	}
	changes, err := e.diff(previous, composed)
	if err != nil {
		return storage.Commit{}, err
	}
	if len(changes) == 0 {
		return storage.Commit{}, ErrNothingToCommit
	}
	ownByKey, err := e.index(own)
	if err != nil {
		return storage.Commit{}, err
	}
	previousByKey, err := e.index(previous)
	if err != nil {
		return storage.Commit{}, err
	}
	patches := make([]storage.Patch, 0, len(changes))
	nearest := make(map[entity]storage.Patch, len(changes))
	for _, change := range changes {
		kind, err := e.kind(change.Kind)
		if err != nil {
			return storage.Commit{}, err
		}
		// An ADD or UPDATE pins the winning row. A DELETE pins the ref's
		// tombstone, or the entity's last committed row when a removed
		// parent took it with it.
		pinned := change.Row
		if change.Operation == "DELETE" {
			pinned = previousByKey[change.Kind][change.EntityKey]
			if raw, ok := ownByKey[change.Kind][change.EntityKey]; ok {
				if r, err := e.readRow(kind, raw); err == nil && r.tombstone {
					pinned = raw
				}
			}
		}
		r, err := e.readRow(kind, pinned)
		if err != nil {
			return storage.Commit{}, err
		}
		patch := storage.Patch{Kind: change.Kind, EntityKey: change.EntityKey, EntityID: r.id, EntityVersion: r.version, Operation: change.Operation}
		patches = append(patches, patch)
		nearest[entity{patch.Kind, patch.EntityKey}] = patch
	}
	hash, err := e.contentHash(composed)
	if err != nil {
		return storage.Commit{}, err
	}
	var sequence *int64
	if opts.Tag {
		next, err := tx.NextSequence(ctx, ref.Root)
		if err != nil {
			return storage.Commit{}, err
		}
		sequence = &next
	}
	written, err := tx.InsertCommit(ctx, storage.NewCommit{
		Root: ref.Root, Ref: ref.ID, Parent: parent, Message: opts.Message,
		SchemaEpoch: e.schemaEpoch, ContentHash: hash, Sequence: sequence, Actor: actor,
	})
	if err != nil {
		return storage.Commit{}, err
	}
	if err := tx.InsertPatches(ctx, written.ID, patches); err != nil {
		return storage.Commit{}, err
	}
	// A tagged commit is snapshotted, and so is one snapshotEvery commits
	// past the nearest snapshot on its chain: its parent's pins with its
	// own patches laid over them.
	if opts.Tag || distance+1 >= e.snapshotEvery {
		pins.apply(nearest)
		if len(pins) > 0 {
			if err := tx.InsertSnapshot(ctx, written.ID, pins.entries()); err != nil {
				return storage.Commit{}, err
			}
			written.Snapshot = true
		}
	}
	return written, nil
}

// commitAndMove commits the ref and moves its head, sealing it when asked.
// Nothing to commit is not an error when allowEmpty is set: the ref's
// version still moves and the returned commit is nil.
func (e *Engine) commitAndMove(ctx context.Context, tx storage.Tx, ref storage.Ref, opts CommitOptions, allowEmpty, seal bool, actor string) (storage.Ref, *storage.Commit, error) {
	var written *storage.Commit
	commit, err := e.commit(ctx, tx, ref, opts, actor)
	switch {
	case err == nil:
		written = &commit
	case allowEmpty && errors.Is(err, ErrNothingToCommit):
	default:
		return storage.Ref{}, nil, err
	}
	update := storage.RefUpdate{ID: ref.ID, Version: ref.Version, Seal: seal, Actor: actor}
	if written != nil {
		update.Head = written.ID
	}
	moved, err := tx.UpdateRef(ctx, update)
	if err != nil {
		return storage.Ref{}, nil, err
	}
	return moved, written, nil
}

// merge merges source's head into target against source's base. With no
// conflicts it writes every entity the target does not already hold as the
// merge left it: the merged row, or the row that deletes the entity.
func (e *Engine) merge(ctx context.Context, tx storage.Tx, source, target storage.Ref, resolutions []versiongraph.Resolution, actor string) ([]versiongraph.Conflict, error) {
	base, err := e.materialize(ctx, tx, source.Base)
	if err != nil {
		return nil, err
	}
	theirs := base
	if source.Head != "" {
		if theirs, err = e.materialize(ctx, tx, source.Head); err != nil {
			return nil, err
		}
	}
	ours, _, _, err := e.compose(ctx, tx, target)
	if err != nil {
		return nil, err
	}
	result, err := e.coreMergeResult(base, ours, theirs, resolutions)
	if err != nil {
		return nil, err
	}
	if len(result.Conflicts) > 0 {
		return result.Conflicts, nil
	}
	merged, err := decodeTree(result.Merged)
	if err != nil {
		return nil, err
	}
	mergedByKey, err := e.index(merged)
	if err != nil {
		return nil, err
	}
	oursByKey, err := e.index(ours)
	if err != nil {
		return nil, err
	}
	for _, outcome := range result.Entities {
		// A result equal to ours is ours' side, a delete on both sides
		// included, so a delete from another side deletes a live entity of
		// ours.
		if outcome.Side == "ours" {
			continue
		}
		if outcome.Deleted {
			if err := e.deleteEntity(ctx, tx, target, oursByKey, outcome.Kind, outcome.EntityKey, actor); err != nil {
				return nil, err
			}
			continue
		}
		r, ok := mergedByKey[outcome.Kind][outcome.EntityKey]
		if !ok {
			return nil, fmt.Errorf("engine: the merge left no %s row for %s", outcome.Kind, outcome.EntityKey)
		}
		if _, err := e.writeRow(ctx, tx, outcome.Kind, target, r, false, actor); err != nil {
			return nil, err
		}
	}
	return nil, nil
}

// coreMergeResult runs the core's three-way merge of three trees, with the
// resolutions' entity keys in their canonical form.
func (e *Engine) coreMergeResult(base, ours, theirs Tree, resolutions []versiongraph.Resolution) (*versiongraph.MergeResult, error) {
	resolutions = append([]versiongraph.Resolution(nil), resolutions...)
	var err error
	for i := range resolutions {
		if resolutions[i].EntityKey, err = id("resolution entity key", resolutions[i].EntityKey); err != nil {
			return nil, err
		}
	}
	request := versiongraph.MergeRequest{Descriptor: e.descriptor, Resolutions: resolutions}
	if request.Base, err = base.json(); err != nil {
		return nil, err
	}
	if request.Ours, err = ours.json(); err != nil {
		return nil, err
	}
	if request.Theirs, err = theirs.json(); err != nil {
		return nil, err
	}
	return versiongraph.Merge(request)
}

// coreMerge is coreMergeResult's merged tree, or its conflicts.
func (e *Engine) coreMerge(base, ours, theirs Tree, resolutions []versiongraph.Resolution) (Tree, []versiongraph.Conflict, error) {
	result, err := e.coreMergeResult(base, ours, theirs, resolutions)
	if err != nil {
		return nil, nil, err
	}
	if len(result.Conflicts) > 0 {
		return nil, result.Conflicts, nil
	}
	merged, err := decodeTree(result.Merged)
	return merged, nil, err
}

// overlay writes a ref's own rows so that, over the tree of its base
// (base), it composes to want. current is what the ref composes to now and
// own its rows. Each entity want holds differently from base gets the ref's
// row of it (a tombstone where want lacks it) unless the ref's row already
// gives it; every other row the ref holds is removed, so the entity reads
// through the base.
func (e *Engine) overlay(ctx context.Context, tx storage.Tx, ref storage.Ref, base, want, current, own Tree, actor string) error {
	changes, err := e.diff(base, want)
	if err != nil {
		return err
	}
	moved, err := e.diff(current, want)
	if err != nil {
		return err
	}
	differs := make(map[entity]bool, len(moved))
	for _, change := range moved {
		differs[entity{change.Kind, change.EntityKey}] = true
	}
	ownByKey, err := e.index(own)
	if err != nil {
		return err
	}
	baseByKey, err := e.index(base)
	if err != nil {
		return err
	}
	kept := make(map[entity]bool, len(changes))
	for _, change := range changes {
		at := entity{change.Kind, change.EntityKey}
		kept[at] = true
		kind, err := e.kind(change.Kind)
		if err != nil {
			return err
		}
		ownRow, hasOwn := ownByKey[change.Kind][change.EntityKey]
		tombstone := false
		if hasOwn {
			r, err := e.readRow(kind, ownRow)
			if err != nil {
				return err
			}
			tombstone = r.tombstone
		}
		if change.Operation == "DELETE" {
			if hasOwn && tombstone {
				continue
			}
			if err := e.deleteEntity(ctx, tx, ref, baseByKey, change.Kind, change.EntityKey, actor); err != nil {
				return err
			}
			continue
		}
		if hasOwn && !tombstone && !differs[at] {
			continue
		}
		if _, err := e.writeRow(ctx, tx, change.Kind, ref, change.Row, false, actor); err != nil {
			return err
		}
	}
	for _, k := range e.kinds {
		for key := range ownByKey[k.Name] {
			if kept[entity{k.Name, key}] {
				continue
			}
			if _, err := tx.RemoveRow(ctx, k.Name, ref.ID, key, actor); err != nil {
				return err
			}
		}
	}
	return nil
}

// revert writes the rows that make the ref compose to tree: each entity
// tree holds that the ref composes differently is written from tree, and
// each entity only the ref holds is deleted on it.
func (e *Engine) revert(ctx context.Context, tx storage.Tx, ref storage.Ref, tree Tree, actor string) error {
	current, _, _, err := e.compose(ctx, tx, ref)
	if err != nil {
		return err
	}
	changes, err := e.diff(current, tree)
	if err != nil {
		return err
	}
	currentByKey, err := e.index(current)
	if err != nil {
		return err
	}
	for _, change := range changes {
		if change.Operation == "DELETE" {
			if err := e.deleteEntity(ctx, tx, ref, currentByKey, change.Kind, change.EntityKey, actor); err != nil {
				return err
			}
			continue
		}
		if _, err := e.writeRow(ctx, tx, change.Kind, ref, change.Row, false, actor); err != nil {
			return err
		}
	}
	return nil
}
