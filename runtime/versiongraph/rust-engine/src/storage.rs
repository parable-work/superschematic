//! The seam between the engine and the database that holds a graph (D19):
//! the operations a storage adapter implements, and the values they take
//! and return.
//!
//! The engine reaches storage only through [`Storage`], and asks for one
//! transaction ([`Tx`]) per operation. Every row an adapter returns, live or
//! from history, is a canonical row: a JSON object keyed by column name
//! whose values are in the canonical form of their value class
//! (`runtime/versiongraph/README.md`, "Canonical rows"). Every row the engine
//! hands an adapter is canonical too, and every id, of a ref, a commit, a
//! root, a row or an actor, is a UUID in its canonical form (base62).
//!
//! An adapter returns [`Error::NotFound`], [`Error::VersionConflict`] and
//! [`Error::NameTaken`] where the methods say, and [`Error::Storage`] for
//! anything its database fails with.

use std::time::Duration;

use async_trait::async_trait;
use serde_json::Value;

use crate::Error;

/// Holds one version graph. An adapter builds it from the graph's
/// descriptor.
#[async_trait]
pub trait Storage: Send + Sync {
    /// Begins one transaction. The engine ends it with [`Tx::commit`] when
    /// its operation succeeds and [`Tx::rollback`] otherwise.
    async fn begin(&self) -> Result<Box<dyn Tx + '_>, Error>;
}

/// One transaction's view of a graph. Its methods are the operations the
/// engine builds every graph operation from.
#[async_trait]
pub trait Tx: Send {
    /// Commits the transaction.
    async fn commit(self: Box<Self>) -> Result<(), Error>;
    /// Rolls the transaction back.
    async fn rollback(self: Box<Self>) -> Result<(), Error>;

    /// Writes a ref and returns it; [`Error::NameTaken`] when the root
    /// already has a live ref of the name.
    async fn create_ref(&mut self, new: NewRef) -> Result<Ref, Error>;
    /// Reads a ref, a discarded one included. A ref that does not exist is
    /// [`Error::NotFound`].
    async fn read_ref(&mut self, id: &str) -> Result<Ref, Error>;
    /// Reads a ref as `read_ref` does and locks it until the transaction
    /// ends.
    async fn lock_ref(&mut self, id: &str) -> Result<Ref, Error>;
    /// Moves a ref's head or base, seals it, or only bumps its version,
    /// fenced by the version it expects. It returns the ref as written, or
    /// [`Error::VersionConflict`].
    async fn update_ref(&mut self, update: RefUpdate) -> Result<Ref, Error>;
    /// Soft-deletes a live ref at the version it expects, or returns
    /// [`Error::VersionConflict`] and leaves the transaction usable.
    async fn discard_ref(&mut self, id: &str, version: i64, actor: &str) -> Result<(), Error>;

    /// Reads every row a ref holds of one kind, tombstones included, in no
    /// particular order.
    async fn rows(&mut self, kind: &str, ref_id: &str) -> Result<Vec<Value>, Error>;
    /// Writes a row as the ref's row of its entity: an update of the ref's
    /// row of the entity when it has one, else a new row. It returns the row
    /// as stored.
    async fn upsert_row(&mut self, kind: &str, write: RowWrite) -> Result<Value, Error>;
    /// Hard-deletes the ref's row of an entity, recording `actor` as the
    /// delete's actor in history. It reports whether there was one.
    async fn remove_row(
        &mut self,
        kind: &str,
        ref_id: &str,
        entity_key: &str,
        actor: &str,
    ) -> Result<bool, Error>;
    /// Reads the history images of row versions. A pin whose image history
    /// no longer holds is left out.
    async fn images(&mut self, kind: &str, pins: &[Pin]) -> Result<Vec<Value>, Error>;

    /// Reads a commit, or returns [`Error::NotFound`].
    async fn read_commit(&mut self, id: &str) -> Result<Commit, Error>;
    /// Writes a commit and returns it.
    async fn insert_commit(&mut self, commit: NewCommit) -> Result<Commit, Error>;
    /// Writes a commit's patches.
    async fn insert_patches(&mut self, commit: &str, patches: &[Patch]) -> Result<(), Error>;
    /// Reads a commit and its parents, nearest first, at most `limit` of
    /// them, and stops after the first that has a snapshot. A commit that
    /// does not exist reads as no commits.
    async fn walk(&mut self, commit: &str, limit: usize) -> Result<Vec<Commit>, Error>;
    /// Reads `head` and the parents of it that `ref_id` wrote, nearest
    /// first, at most `limit` of them.
    async fn ref_commits(
        &mut self,
        ref_id: &str,
        head: &str,
        limit: usize,
    ) -> Result<Vec<Commit>, Error>;
    /// Reads every patch of the commits.
    async fn patches(&mut self, commits: &[String]) -> Result<Vec<Patch>, Error>;
    /// Locks the root against other taggers until the transaction ends, and
    /// returns the root's next published sequence.
    async fn next_sequence(&mut self, root: &str) -> Result<i64, Error>;

    /// Reads a commit's snapshot: its full pin set, in no particular order.
    /// A commit without one reads as no entries.
    async fn snapshot(&mut self, commit: &str) -> Result<Vec<SnapshotEntry>, Error>;
    /// Writes a commit's snapshot.
    async fn insert_snapshot(
        &mut self,
        commit: &str,
        entries: &[SnapshotEntry],
    ) -> Result<(), Error>;
    /// Reads every commit of the graph, in no particular order, with whether
    /// each is tagged and snapshotted.
    async fn commits(&mut self) -> Result<Vec<CommitNode>, Error>;

    /// Reads a root's release pointer, or returns [`Error::NotFound`] when the
    /// root has none.
    async fn read_release(&mut self, root: &str) -> Result<Release, Error>;
    /// Points a root's release at a commit, fenced by the pointer's version:
    /// version 0 writes the root's first pointer, and any other moves the
    /// pointer at that version. A pointer at another version, or one that
    /// already exists when version is 0, is [`Error::VersionConflict`]. It
    /// returns the pointer as written.
    async fn write_release(&mut self, write: ReleaseWrite) -> Result<Release, Error>;

    /// Deletes the history images of one kind older than `retention_days`
    /// (0 for the kind's declared retention), keeping every image a patch or
    /// a snapshot pins, at most `batch_size` of them (0 for no limit). It
    /// returns how many it deleted.
    async fn prune(
        &mut self,
        kind: &str,
        retention_days: i64,
        batch_size: i64,
    ) -> Result<i64, Error>;
    /// Reads the refs discarded longer ago than `grace`.
    async fn discarded_refs(&mut self, grace: Duration) -> Result<Vec<Ref>, Error>;
    /// Reads the live change sets whose last write is older than `idle`.
    async fn idle_drafts(&mut self, idle: Duration) -> Result<Vec<Ref>, Error>;
    /// Hard-deletes every row a ref holds of one kind, recording `actor` as
    /// each delete's actor in history. It returns how many it deleted.
    async fn remove_ref_rows(
        &mut self,
        kind: &str,
        ref_id: &str,
        actor: &str,
    ) -> Result<i64, Error>;
    /// Takes the graph's sweep lock until the transaction ends. It reports
    /// false, without waiting, when another transaction holds it.
    async fn sweep_lock(&mut self) -> Result<bool, Error>;
}

/// A ref's graph columns.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Ref {
    pub id: String,
    pub root: String,
    /// The ref this change set branched from; `None` for a primary line.
    pub parent: Option<String>,
    /// The commit the ref's rows are laid over; `None` for none.
    pub base: Option<String>,
    /// The ref's last commit; `None` before its first.
    pub head: Option<String>,
    pub name: String,
    pub sealed: bool,
    /// True once the ref is soft-deleted.
    pub discarded: bool,
    pub version: i64,
}

/// A ref to write: a primary line when `parent` is `None`, else a change set
/// of `parent` whose base is `base`.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct NewRef {
    pub root: String,
    pub parent: Option<String>,
    pub base: Option<String>,
    pub name: String,
    pub actor: String,
}

/// Changes a ref at `version`: it moves the head to `head` and the base to
/// `base` (each when given), seals the ref when `seal` is set, and bumps its
/// version in any case.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct RefUpdate {
    pub id: String,
    pub version: i64,
    pub head: Option<String>,
    pub base: Option<String>,
    pub seal: bool,
    pub actor: String,
}

/// A row to write onto a ref. The adapter writes `ref_id`, `root` and
/// `tombstone` into the row's ref, root and tombstone columns, and `actor`
/// and the time into its audit columns, whatever the row says; it never
/// writes the row's id or version columns. A column the row lacks keeps its
/// stored value on an update and its default on an insert; a row without
/// an entity key is a new entity, whose key the database generates.
#[derive(Debug, Clone, PartialEq)]
pub struct RowWrite {
    pub ref_id: String,
    pub root: String,
    pub row: Value,
    pub tombstone: bool,
    pub actor: String,
}

/// One row version: the row's id and its version.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Pin {
    pub id: String,
    pub version: i64,
}

/// A commit.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Commit {
    pub id: String,
    pub root: String,
    pub ref_id: String,
    /// `None` for a commit with no parent.
    pub parent: Option<String>,
    /// `""` for none.
    pub message: String,
    pub schema_epoch: i64,
    pub content_hash: String,
    /// `None` for an untagged commit.
    pub sequence: Option<i64>,
    /// The commit's time as a canonical dateTime.
    pub created_at: String,
    pub created_by: String,
    /// True when the commit has a snapshot.
    pub snapshot: bool,
}

/// A commit to write.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct NewCommit {
    pub root: String,
    pub ref_id: String,
    pub parent: Option<String>,
    pub message: String,
    pub schema_epoch: i64,
    pub content_hash: String,
    pub sequence: Option<i64>,
    pub actor: String,
}

/// Pins one entity a commit changed to the row version it sealed.
/// `operation` is `ADD`, `UPDATE` or `DELETE`. `commit` is set on a patch
/// read back and empty on one to write.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Patch {
    pub commit: String,
    pub kind: String,
    pub entity_key: String,
    pub entity_id: String,
    pub entity_version: i64,
    pub operation: String,
}

/// Pins one entity of a snapshotted commit's tree to the row version the
/// tree holds.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct SnapshotEntry {
    pub kind: String,
    pub entity_key: String,
    pub entity_id: String,
    pub entity_version: i64,
}

/// One commit of the graph as a sweep reads it: its parent (`None` for
/// none), and whether it is tagged and has a snapshot.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct CommitNode {
    pub id: String,
    pub parent: Option<String>,
    pub tagged: bool,
    pub snapshot: bool,
}

/// A root's release pointer: the commit it names, fenced by its version.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Release {
    pub id: String,
    pub root: String,
    pub commit: String,
    pub version: i64,
}

/// Points a root's release at `commit`, fenced by `version` (0 for the
/// root's first pointer).
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct ReleaseWrite {
    pub root: String,
    pub commit: String,
    pub version: i64,
    pub actor: String,
}
