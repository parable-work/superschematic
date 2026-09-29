// Package storage is the seam between the version-graph engine and the
// database that holds a graph (D19): the operations a storage adapter
// implements, and the values they take and return.
//
// The engine (package engine) reaches storage only through this interface,
// and asks for one transaction per operation. Every row an adapter returns,
// live or from history, is a canonical row: a JSON object keyed by column
// name whose values are in the canonical form of their value class
// (runtime/versiongraph/README.md, "Canonical rows"). Every row the engine
// hands an adapter is canonical too, and every id, of a ref, a commit, a
// root, a row or an actor, is a UUID in its canonical form (base62).
//
// The package is plain Go with no cgo, so an adapter can implement it
// without linking the core. Package postgres is the Postgres adapter.
package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Errors an adapter returns. The engine passes them through.
var (
	// ErrNotFound is returned for a ref or a commit that does not exist.
	ErrNotFound = errors.New("not found")

	// ErrVersionConflict is returned by a fenced write when the row is at
	// another version. It wraps ErrNotFound, so errors.Is(err, ErrNotFound)
	// still holds, as it does for the generated ORM's error of that name.
	ErrVersionConflict = fmt.Errorf("%w at the expected version", ErrNotFound)

	// ErrNameTaken is returned when a root already has a live ref of that
	// name.
	ErrNameTaken = errors.New("the root already has a live ref of that name")
)

// Storage holds one version graph. An adapter builds it from the graph's
// descriptor.
type Storage interface {
	// Transact runs fn in one transaction. It commits when fn returns nil
	// and rolls back otherwise, returning fn's error.
	Transact(ctx context.Context, fn func(ctx context.Context, tx Tx) error) error
}

// Tx is one transaction's view of a graph. Its methods are the operations
// the engine builds every graph operation from.
type Tx interface {
	// CreateRef writes a ref and returns it.
	CreateRef(ctx context.Context, ref NewRef) (Ref, error)
	// ReadRef reads a ref, a discarded one included. A ref that does not
	// exist is ErrNotFound.
	ReadRef(ctx context.Context, id string) (Ref, error)
	// LockRef reads a ref as ReadRef does and locks it until the
	// transaction ends.
	LockRef(ctx context.Context, id string) (Ref, error)
	// UpdateRef moves a ref's head or base, seals it, or only bumps its
	// version, fenced by the version it expects. It returns the ref as
	// written, or ErrVersionConflict.
	UpdateRef(ctx context.Context, update RefUpdate) (Ref, error)
	// DiscardRef soft-deletes a live ref at the version it expects, or
	// returns ErrVersionConflict and leaves the transaction usable.
	DiscardRef(ctx context.Context, id string, version int64, actor string) error

	// Rows reads every row a ref holds of one kind, tombstones included,
	// in no particular order.
	Rows(ctx context.Context, kind, ref string) ([]json.RawMessage, error)
	// UpsertRow writes a row as the ref's row of its entity: an update of
	// the ref's row of the entity when it has one, else a new row. It
	// returns the row as stored.
	UpsertRow(ctx context.Context, kind string, write RowWrite) (json.RawMessage, error)
	// RemoveRow hard-deletes the ref's row of an entity, recording actor
	// as the delete's actor in history. It reports whether there was one.
	RemoveRow(ctx context.Context, kind, ref, entityKey, actor string) (bool, error)
	// Images reads the history images of row versions. A pin whose image
	// history no longer holds is left out.
	Images(ctx context.Context, kind string, pins []Pin) ([]json.RawMessage, error)

	// ReadCommit reads a commit, or returns ErrNotFound.
	ReadCommit(ctx context.Context, id string) (Commit, error)
	// InsertCommit writes a commit and returns it.
	InsertCommit(ctx context.Context, commit NewCommit) (Commit, error)
	// InsertPatches writes a commit's patches.
	InsertPatches(ctx context.Context, commit string, patches []Patch) error
	// Walk reads a commit and its parents, nearest first, at most limit
	// of them, and stops after the first that has a snapshot. A commit
	// that does not exist reads as no commits.
	Walk(ctx context.Context, commit string, limit int) ([]Commit, error)
	// RefCommits reads head and the parents of it that ref wrote, nearest
	// first, at most limit of them.
	RefCommits(ctx context.Context, ref, head string, limit int) ([]Commit, error)
	// Patches reads every patch of the commits.
	Patches(ctx context.Context, commits []string) ([]Patch, error)
	// NextSequence locks the root against other taggers until the
	// transaction ends, and returns the root's next published sequence.
	NextSequence(ctx context.Context, root string) (int64, error)

	// Snapshot reads a commit's snapshot: its full pin set, in no
	// particular order. A commit without one reads as no entries.
	Snapshot(ctx context.Context, commit string) ([]SnapshotEntry, error)
	// InsertSnapshot writes a commit's snapshot.
	InsertSnapshot(ctx context.Context, commit string, entries []SnapshotEntry) error
	// Commits reads every commit of the graph, in no particular order, with
	// whether each is tagged and snapshotted.
	Commits(ctx context.Context) ([]CommitNode, error)

	// ReadRelease reads a root's release pointer, or returns ErrNotFound
	// when the root has none.
	ReadRelease(ctx context.Context, root string) (Release, error)
	// WriteRelease points a root's release at a commit, fenced by the
	// pointer's version: version 0 writes the root's first pointer, and
	// any other moves the pointer at that version. A pointer at another
	// version, or one that already exists when version is 0, is
	// ErrVersionConflict. It returns the pointer as written.
	WriteRelease(ctx context.Context, write ReleaseWrite) (Release, error)

	// Prune deletes the history images of one kind older than
	// retentionDays (0 for the kind's declared retention), keeping every
	// image a patch or a snapshot pins, at most batchSize of them (0 for no
	// limit). It returns how many it deleted.
	Prune(ctx context.Context, kind string, retentionDays, batchSize int) (int64, error)
	// DiscardedRefs reads the refs discarded longer ago than grace.
	DiscardedRefs(ctx context.Context, grace time.Duration) ([]Ref, error)
	// IdleDrafts reads the live change sets whose last write is older than
	// idle.
	IdleDrafts(ctx context.Context, idle time.Duration) ([]Ref, error)
	// RemoveRefRows hard-deletes every row a ref holds of one kind,
	// recording actor as each delete's actor in history. It returns how
	// many it deleted.
	RemoveRefRows(ctx context.Context, kind, ref, actor string) (int64, error)
	// SweepLock takes the graph's sweep lock until the transaction ends. It
	// reports false, without waiting, when another transaction holds it.
	SweepLock(ctx context.Context) (bool, error)
}

// Ref is a ref's graph columns. An absent reference is "".
type Ref struct {
	ID     string
	Root   string
	Parent string
	Base   string
	Head   string
	Name   string
	Sealed bool
	// Discarded is true once the ref is soft-deleted.
	Discarded bool
	Version   int64
}

// NewRef is a ref to write: a primary line when Parent is "", else a change
// set of Parent whose base is Base ("" for none).
type NewRef struct {
	Root   string
	Parent string
	Base   string
	Name   string
	Actor  string
}

// RefUpdate changes a ref at Version: it moves the head to Head and the
// base to Base (each unless it is ""), seals the ref when Seal is set, and
// bumps its version in any case.
type RefUpdate struct {
	ID      string
	Version int64
	Head    string
	Base    string
	Seal    bool
	Actor   string
}

// RowWrite is a row to write onto a ref. The adapter writes Ref, Root and
// Tombstone into the row's ref, root and tombstone columns, and Actor and
// the time into its audit columns, whatever the row says; it never writes
// the row's id or version columns. A column the row lacks keeps its stored
// value on an update and its default on an insert; a row without an entity
// key is a new entity, whose key the database generates.
type RowWrite struct {
	Ref       string
	Root      string
	Row       json.RawMessage
	Tombstone bool
	Actor     string
}

// Pin names one row version: the row's id and its version.
type Pin struct {
	ID      string
	Version int64
}

// Commit is a commit. Parent is "" for a commit with no parent, Message ""
// for none, and Sequence nil for an untagged commit.
type Commit struct {
	ID          string
	Root        string
	Ref         string
	Parent      string
	Message     string
	SchemaEpoch int64
	ContentHash string
	Sequence    *int64
	// CreatedAt is the commit's time as a canonical dateTime.
	CreatedAt string
	CreatedBy string
	// Snapshot is true when the commit has a snapshot.
	Snapshot bool
}

// NewCommit is a commit to write.
type NewCommit struct {
	Root        string
	Ref         string
	Parent      string
	Message     string
	SchemaEpoch int64
	ContentHash string
	Sequence    *int64
	Actor       string
}

// Patch pins one entity a commit changed to the row version it sealed.
// Operation is ADD, UPDATE or DELETE. Commit is set on a patch read back.
type Patch struct {
	Commit        string
	Kind          string
	EntityKey     string
	EntityID      string
	EntityVersion int64
	Operation     string
}

// SnapshotEntry pins one entity of a snapshotted commit's tree to the row
// version the tree holds.
type SnapshotEntry struct {
	Kind          string
	EntityKey     string
	EntityID      string
	EntityVersion int64
}

// CommitNode is one commit of the graph as a sweep reads it: its parent
// ("" for none), and whether it is tagged and has a snapshot.
type CommitNode struct {
	ID       string
	Parent   string
	Tagged   bool
	Snapshot bool
}

// Release is a root's release pointer: the commit it names, fenced by its
// version.
type Release struct {
	ID      string
	Root    string
	Commit  string
	Version int64
}

// ReleaseWrite points a root's release at Commit, fenced by Version (0 for
// the root's first pointer).
type ReleaseWrite struct {
	Root    string
	Commit  string
	Version int64
	Actor   string
}
