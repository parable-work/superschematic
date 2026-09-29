//! The engine: every graph operation, written once over a storage adapter
//! and the core.

use std::collections::{BTreeMap, HashMap, HashSet};
use std::sync::Arc;

use serde::Deserialize;
use serde_json::Value;

use crate::canonical;
use crate::core::{Change, Conflict, Core, Finding, Resolution, Tree};
use crate::storage::{
    Commit, NewCommit, NewRef, Patch, Pin, Ref, RefUpdate, Release, ReleaseWrite, RowWrite,
    SnapshotEntry, Storage, Tx,
};
use crate::Error;

/// How many commits a read walks, following a commit's parents, before it
/// stops with [`Error::WalkCeiling`].
pub const DEFAULT_WALK_CEILING: usize = 4096;

/// How many commits past the nearest snapshot on its chain a commit is
/// snapshotted at, unless [`Options::snapshot_every`] says otherwise.
pub const DEFAULT_SNAPSHOT_EVERY: usize = 64;

/// Configures an [`Engine`].
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub struct Options {
    /// The graph's schema epoch: every commit records it, and reads refuse a
    /// commit from a newer one.
    pub schema_epoch: i64,
    /// Bounds a commit walk; 0 is [`DEFAULT_WALK_CEILING`].
    pub walk_ceiling: usize,
    /// The graph's snapshot interval (`@versionGraph({ snapshotEvery })`); 0
    /// is [`DEFAULT_SNAPSHOT_EVERY`].
    pub snapshot_every: usize,
}

/// Runs one graph's operations over a storage adapter.
///
/// Every operation runs in one transaction of the adapter. Every write takes
/// an actor, recorded in the audit columns, and every write through a ref
/// takes the ref's expected version and fails with
/// [`Error::VersionConflict`] when the ref has moved on. Every id the engine
/// takes is a UUID, in its canonical form (base62) or hyphenated; every id
/// it returns is canonical.
#[derive(Clone)]
pub struct Engine {
    pub(crate) core: Core,
    pub(crate) kinds: Arc<Vec<KindRoles>>,
    pub(crate) storage: Arc<dyn Storage>,
    pub(crate) schema_epoch: i64,
    pub(crate) walk_ceiling: usize,
    pub(crate) snapshot_every: usize,
}

impl std::fmt::Debug for Engine {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Engine")
            .field("schema_epoch", &self.schema_epoch)
            .field("walk_ceiling", &self.walk_ceiling)
            .field("snapshot_every", &self.snapshot_every)
            .finish_non_exhaustive()
    }
}

/// The columns of a kind's rows the engine reads.
#[derive(Debug, Clone, Deserialize)]
pub(crate) struct KindRoles {
    #[serde(rename = "kind")]
    pub name: String,
    pub key: String,
    pub id: String,
    pub tombstone: String,
    pub version: String,
    #[serde(default)]
    pub columns: BTreeMap<String, String>,
}

/// One kind's edits of a ref. `upsert` writes each row as the ref's row of
/// its entity, found by its entity key; a row without one is a new entity,
/// whose key the database generates. `delete` writes a row that deletes
/// each entity, by entity key, on the ref. `unset` removes the ref's own row
/// of each entity, so the ref reads the entity through its base again.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct KindEdits {
    pub upsert: Vec<Value>,
    pub delete: Vec<String>,
    pub unset: Vec<String>,
}

/// A save's edits by kind. Save applies every kind's upserts, then every
/// kind's deletes, then every kind's unsets, each in descriptor order.
pub type Edits = BTreeMap<String, KindEdits>;

/// A commit's message (`""` for none) and whether it is tagged: a tagged
/// commit takes the root's next sequence.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct CommitOptions {
    pub message: String,
    pub tag: bool,
}

/// A saved ref at its new version and the rows Save upserted, as stored, in
/// edit order per kind.
#[derive(Debug, Clone, PartialEq)]
pub struct SaveResult {
    pub ref_: Ref,
    pub saved: Tree,
}

/// A ref at its new version and the commit written, `None` when there was
/// nothing to commit.
#[derive(Debug, Clone, PartialEq)]
pub struct CommitResult {
    pub ref_: Ref,
    pub commit: Option<Commit>,
}

/// The target of a merge, or the change set a rebase moved, and the commit
/// written. When `conflicts` is not empty nothing was written and `ref_` is
/// the ref as it was.
#[derive(Debug, Clone, PartialEq)]
pub struct MergeResult {
    pub ref_: Ref,
    pub commit: Option<Commit>,
    pub conflicts: Vec<Conflict>,
}

/// A tree in the core's order (rows by their order column, then by entity
/// key), its content hash, and, for a composed ref, the problems compose
/// found.
#[derive(Debug, Clone, PartialEq)]
pub struct TreeResult {
    pub tree: Tree,
    pub content_hash: String,
    pub findings: Vec<Finding>,
}

/// A root's release pointer and the tree of the commit it names.
#[derive(Debug, Clone, PartialEq)]
pub struct ReleasedResult {
    pub release: Release,
    pub tree: TreeResult,
}

/// What the engine reads of one row.
struct RowRoles {
    key: String,
    id: String,
    version: i64,
    tombstone: bool,
}

/// An entity of a tree: its kind and entity key.
type Entity = (String, String);

/// A commit's tree as the row versions it holds, by entity, in kind and
/// entity key order.
type PinSet = BTreeMap<Entity, SnapshotEntry>;

/// Each kind's rows by entity key.
type Index = HashMap<String, HashMap<String, Value>>;

/// Normalizes a UUID argument to its canonical form.
pub(crate) fn id(what: &str, value: &str) -> Result<String, Error> {
    canonical::uuid(value).map_err(|mut error| {
        error.message = format!("{what}: {}", error.message);
        Error::Canonical(error)
    })
}

/// Normalizes a write's actor; an empty one is [`Error::NoActor`].
pub(crate) fn actor_id(actor: &str) -> Result<String, Error> {
    if actor.is_empty() {
        return Err(Error::NoActor);
    }
    id("actor", actor)
}

/// Commits the transaction when the operation succeeded and rolls it back
/// otherwise, returning the operation's error.
pub(crate) async fn finish<T>(tx: Box<dyn Tx + '_>, result: Result<T, Error>) -> Result<T, Error> {
    match result {
        Ok(value) => {
            tx.commit().await?;
            Ok(value)
        }
        Err(error) => {
            // The operation's error is what the caller needs; a failed
            // rollback ends the transaction as surely.
            let _ = tx.rollback().await;
            Err(error)
        }
    }
}

impl Engine {
    /// The engine of the graph `descriptor` describes (version 2), over
    /// `storage`. The core checks the descriptor.
    pub fn new(
        descriptor: &str,
        storage: Arc<dyn Storage>,
        options: Options,
    ) -> Result<Self, Error> {
        let descriptor: Value = serde_json::from_str(descriptor)
            .map_err(|e| Error::Invalid(format!("read the descriptor: {e}")))?;
        let core = Core { descriptor };
        core.validate(&Tree::new())?;
        #[derive(Deserialize)]
        struct Kinds {
            kinds: Vec<KindRoles>,
        }
        let kinds: Kinds = serde_json::from_value(core.descriptor.clone())
            .map_err(|e| Error::Invalid(format!("read the descriptor: {e}")))?;
        Ok(Engine {
            core,
            kinds: Arc::new(kinds.kinds),
            storage,
            schema_epoch: options.schema_epoch,
            walk_ceiling: if options.walk_ceiling == 0 {
                DEFAULT_WALK_CEILING
            } else {
                options.walk_ceiling
            },
            snapshot_every: if options.snapshot_every == 0 {
                DEFAULT_SNAPSHOT_EVERY
            } else {
                options.snapshot_every
            },
        })
    }

    /// A copy of the engine over another storage.
    pub fn with_storage(&self, storage: Arc<dyn Storage>) -> Self {
        Engine {
            storage,
            ..self.clone()
        }
    }

    /// A copy of the engine that walks at most `n` commits to read a
    /// commit's tree, and fails with [`Error::WalkCeiling`] past them. 0 is
    /// [`DEFAULT_WALK_CEILING`].
    pub fn with_walk_ceiling(&self, n: usize) -> Self {
        Engine {
            walk_ceiling: if n == 0 { DEFAULT_WALK_CEILING } else { n },
            ..self.clone()
        }
    }

    /// The graph's descriptor.
    pub fn descriptor(&self) -> &Value {
        &self.core.descriptor
    }

    /// The engine's storage.
    pub fn storage(&self) -> &Arc<dyn Storage> {
        &self.storage
    }

    /// The canonical row of a row of `kind` whose values are the schema
    /// runtime's JSON for each column's field type, as a typed value
    /// serializes: what a typed facade hands the engine. A column the kind
    /// lacks is refused.
    pub fn canonical_row(&self, kind: &str, row: &Value) -> Result<Value, Error> {
        let text = canonical::row_value(&self.kind(kind)?.columns, row)?;
        serde_json::from_str(&text)
            .map_err(|e| Error::Invalid(format!("reread a canonical row: {e}")))
    }

    fn kind(&self, name: &str) -> Result<&KindRoles, Error> {
        self.kinds
            .iter()
            .find(|k| k.name == name)
            .ok_or_else(|| Error::Invalid(format!("unknown kind {name:?}")))
    }

    /// Creates a primary line of `root`: a ref with no parent.
    pub async fn create_primary(&self, actor: &str, root: &str, name: &str) -> Result<Ref, Error> {
        let actor = actor_id(actor)?;
        let root = id("root", root)?;
        let mut tx = self.storage.begin().await?;
        let result = tx
            .create_ref(NewRef {
                root,
                parent: None,
                base: None,
                name: name.to_owned(),
                actor,
            })
            .await;
        finish(tx, result).await
    }

    /// Creates a change set of `from_ref` whose base is `from_ref`'s head.
    pub async fn branch(&self, actor: &str, from_ref: &str, name: &str) -> Result<Ref, Error> {
        let actor = actor_id(actor)?;
        let from_ref = id("ref", from_ref)?;
        let mut tx = self.storage.begin().await?;
        let result = async {
            let from = self.read_ref(&mut *tx, &from_ref, None, false).await?;
            tx.create_ref(NewRef {
                root: from.root,
                parent: Some(from.id),
                base: from.head,
                name: name.to_owned(),
                actor,
            })
            .await
        }
        .await;
        finish(tx, result).await
    }

    /// Applies edits to a change set at `version`. It refuses a sealed ref,
    /// and a primary line with [`Error::PrimaryMergeOnly`].
    pub async fn save(
        &self,
        actor: &str,
        ref_id: &str,
        version: i64,
        edits: &Edits,
    ) -> Result<SaveResult, Error> {
        let actor = actor_id(actor)?;
        let ref_id = id("ref", ref_id)?;
        for name in edits.keys() {
            self.kind(name)?;
        }
        let mut tx = self.storage.begin().await?;
        let result = self
            .save_in(&mut *tx, &actor, &ref_id, version, edits)
            .await;
        finish(tx, result).await
    }

    async fn save_in(
        &self,
        tx: &mut dyn Tx,
        actor: &str,
        ref_id: &str,
        version: i64,
        edits: &Edits,
    ) -> Result<SaveResult, Error> {
        let r = self.read_draft(tx, ref_id, version).await?;
        let mut saved = Tree::new();
        let mut deletes = false;
        for k in self.kinds.iter() {
            let Some(kind_edits) = edits.get(&k.name) else {
                continue;
            };
            for row in &kind_edits.upsert {
                let stored = self
                    .write_row(tx, &k.name, &r, row.clone(), false, actor)
                    .await?;
                saved.entry(k.name.clone()).or_default().push(stored);
            }
            deletes = deletes || !kind_edits.delete.is_empty();
        }
        if deletes {
            let (composed, _, _) = self.compose_ref(tx, &r).await?;
            let by_key = self.index(&composed)?;
            for k in self.kinds.iter() {
                for key in edits.get(&k.name).map_or(&[][..], |e| &e.delete[..]) {
                    let key = id("entity key", key)?;
                    self.delete_entity(tx, &r, &by_key, &k.name, &key, actor)
                        .await?;
                }
            }
        }
        for k in self.kinds.iter() {
            for key in edits.get(&k.name).map_or(&[][..], |e| &e.unset[..]) {
                let key = id("entity key", key)?;
                if !tx.remove_row(&k.name, &r.id, &key, actor).await? {
                    return Err(Error::EntityNotFound(format!(
                        "no {} override of {key}",
                        k.name
                    )));
                }
            }
        }
        let ref_ = tx
            .update_ref(RefUpdate {
                id: r.id.clone(),
                version: r.version,
                actor: actor.to_owned(),
                ..RefUpdate::default()
            })
            .await?;
        Ok(SaveResult { ref_, saved })
    }

    /// Composes a change set at `version`, diffs it against its last commit
    /// (or its base), writes a commit with a patch per changed entity and
    /// moves the ref's head. It returns [`Error::NothingToCommit`] when
    /// nothing changed, [`Error::InvalidTree`] when the composed tree breaks
    /// the graph's rules, and [`Error::PrimaryMergeOnly`] for a primary line,
    /// whose commits Merge writes.
    pub async fn commit(
        &self,
        actor: &str,
        ref_id: &str,
        version: i64,
        options: &CommitOptions,
    ) -> Result<CommitResult, Error> {
        self.commit_ref(actor, ref_id, version, options, false, false)
            .await
    }

    /// Commits a change set at `version` when it has changes, and seals it:
    /// the ref then refuses writes. A primary line is
    /// [`Error::PrimaryMergeOnly`].
    pub async fn seal(
        &self,
        actor: &str,
        ref_id: &str,
        version: i64,
    ) -> Result<CommitResult, Error> {
        self.commit_ref(
            actor,
            ref_id,
            version,
            &CommitOptions::default(),
            true,
            true,
        )
        .await
    }

    async fn commit_ref(
        &self,
        actor: &str,
        ref_id: &str,
        version: i64,
        options: &CommitOptions,
        allow_empty: bool,
        seal: bool,
    ) -> Result<CommitResult, Error> {
        let actor = actor_id(actor)?;
        let ref_id = id("ref", ref_id)?;
        let mut tx = self.storage.begin().await?;
        let result = async {
            let r = self.read_draft(&mut *tx, &ref_id, version).await?;
            let (ref_, commit) = self
                .commit_and_move(&mut *tx, &r, options, allow_empty, seal, &actor)
                .await?;
            Ok(CommitResult { ref_, commit })
        }
        .await;
        finish(tx, result).await
    }

    /// Merges `source`'s head into `target` at `target_version`, against
    /// `source`'s base. Without conflicts it writes the result onto `target`
    /// and commits it in the same transaction, with `options`' message and
    /// tag. With conflicts left after `resolutions` it returns them and
    /// writes nothing. Merge is the only write a primary line takes.
    pub async fn merge(
        &self,
        actor: &str,
        source: &str,
        target: &str,
        target_version: i64,
        resolutions: &[Resolution],
        options: &CommitOptions,
    ) -> Result<MergeResult, Error> {
        let actor = actor_id(actor)?;
        let source = id("source", source)?;
        let target = id("target", target)?;
        let mut tx = self.storage.begin().await?;
        let result = async {
            let tx = &mut *tx;
            let t = self
                .read_ref(tx, &target, Some(target_version), true)
                .await?;
            let s = self.read_ref(tx, &source, None, false).await?;
            if s.root != t.root {
                return Err(Error::RootMismatch);
            }
            if s.id == t.id {
                return Err(Error::MergeIntoItself);
            }
            let conflicts = self.merge_in(tx, &s, &t, resolutions, &actor).await?;
            if !conflicts.is_empty() {
                return Ok(MergeResult {
                    ref_: t,
                    commit: None,
                    conflicts,
                });
            }
            let (ref_, commit) = self
                .commit_and_move(tx, &t, options, true, false, &actor)
                .await?;
            Ok(MergeResult {
                ref_,
                commit,
                conflicts: Vec::new(),
            })
        }
        .await;
        finish(tx, result).await
    }

    /// Moves a change set at `version` onto its parent's head. It merges the
    /// parent's head into the change set, with the change set's base as the
    /// merge base and its composed tree, uncommitted work included, as ours.
    /// With conflicts left after `resolutions` it returns them and writes
    /// nothing. Otherwise it writes the change set's rows so it composes to
    /// the merged tree over the parent's head, makes that head its base, and
    /// commits on it with its previous head (or, with none, its new base) as
    /// the parent, so its history keeps its commits. A primary line has no
    /// parent to rebase onto: [`Error::NoParent`].
    pub async fn rebase(
        &self,
        actor: &str,
        draft: &str,
        version: i64,
        resolutions: &[Resolution],
    ) -> Result<MergeResult, Error> {
        let actor = actor_id(actor)?;
        let draft = id("draft", draft)?;
        let mut tx = self.storage.begin().await?;
        let result = self
            .rebase_in(&mut *tx, &actor, &draft, version, resolutions)
            .await;
        finish(tx, result).await
    }

    async fn rebase_in(
        &self,
        tx: &mut dyn Tx,
        actor: &str,
        draft: &str,
        version: i64,
        resolutions: &[Resolution],
    ) -> Result<MergeResult, Error> {
        let d = self.read_ref(tx, draft, Some(version), true).await?;
        let Some(parent_id) = d.parent.clone() else {
            return Err(Error::NoParent);
        };
        let parent = self.read_ref(tx, &parent_id, None, false).await?;
        if parent.head == d.base {
            // Already on the parent's head: nothing to merge.
            let ref_ = tx
                .update_ref(RefUpdate {
                    id: d.id.clone(),
                    version: d.version,
                    actor: actor.to_owned(),
                    ..RefUpdate::default()
                })
                .await?;
            return Ok(MergeResult {
                ref_,
                commit: None,
                conflicts: Vec::new(),
            });
        }
        let base = self.materialize_tree(tx, d.base.as_deref()).await?;
        let theirs = self.materialize_tree(tx, parent.head.as_deref()).await?;
        let (ours, _, own) = self.compose_ref(tx, &d).await?;
        let merged = self.core_merge(&base, &ours, &theirs, resolutions)?;
        if !merged.conflicts.is_empty() {
            return Ok(MergeResult {
                ref_: d,
                commit: None,
                conflicts: merged.conflicts,
            });
        }
        let mut moved = d.clone();
        moved.base = parent.head.clone();
        self.overlay(tx, &moved, &theirs, &merged.merged, &ours, &own, actor)
            .await?;
        let written = match self
            .write_commit(tx, &moved, &CommitOptions::default(), actor)
            .await
        {
            Ok(commit) => Some(commit),
            Err(Error::NothingToCommit) => None,
            Err(error) => return Err(error),
        };
        let ref_ = tx
            .update_ref(RefUpdate {
                id: d.id.clone(),
                version: d.version,
                head: written.as_ref().map(|c| c.id.clone()),
                base: parent.head.clone(),
                seal: false,
                actor: actor.to_owned(),
            })
            .await?;
        Ok(MergeResult {
            ref_,
            commit: written,
            conflicts: Vec::new(),
        })
    }

    /// Writes the rows that make a change set at `version` compose to the
    /// tree of `to_commit`, and commits them. History is never rewritten. A
    /// primary line is [`Error::PrimaryMergeOnly`]: revert a change set of it
    /// and merge that.
    pub async fn revert(
        &self,
        actor: &str,
        ref_id: &str,
        version: i64,
        to_commit: &str,
    ) -> Result<CommitResult, Error> {
        let actor = actor_id(actor)?;
        let ref_id = id("ref", ref_id)?;
        let to_commit = id("commit", to_commit)?;
        let mut tx = self.storage.begin().await?;
        let result = async {
            let tx = &mut *tx;
            let r = self.read_draft(tx, &ref_id, version).await?;
            let commit = tx.read_commit(&to_commit).await?;
            if commit.root != r.root {
                return Err(Error::RootMismatch);
            }
            let tree = self.materialize_tree(tx, Some(&to_commit)).await?;
            self.revert_rows(tx, &r, &tree, &actor).await?;
            let (ref_, commit) = self
                .commit_and_move(tx, &r, &CommitOptions::default(), true, false, &actor)
                .await?;
            Ok(CommitResult { ref_, commit })
        }
        .await;
        finish(tx, result).await
    }

    /// Points `root`'s release at `commit`, a tagged commit of `root`, fenced
    /// by the pointer's version: 0 for the root's first release. It
    /// snapshots the commit and writes no member rows, so a rollback is a
    /// release of an earlier tagged commit, and the pointer's history is the
    /// release log. An untagged commit is [`Error::NotTagged`]; another
    /// root's is [`Error::RootMismatch`].
    pub async fn release(
        &self,
        actor: &str,
        root: &str,
        commit: &str,
        version: i64,
    ) -> Result<Release, Error> {
        let actor = actor_id(actor)?;
        let root = id("root", root)?;
        let commit = id("commit", commit)?;
        let mut tx = self.storage.begin().await?;
        let result = async {
            let tx = &mut *tx;
            let c = tx.read_commit(&commit).await?;
            if c.root != root {
                return Err(Error::RootMismatch);
            }
            if c.sequence.is_none() {
                return Err(Error::NotTagged);
            }
            self.ensure_snapshot(tx, &c.id, c.snapshot).await?;
            tx.write_release(ReleaseWrite {
                root,
                commit: c.id,
                version,
                actor,
            })
            .await
        }
        .await;
        finish(tx, result).await
    }

    /// Reads `root`'s release pointer and the tree of the commit it names. A
    /// root that has never been released is [`Error::NotFound`].
    pub async fn released(&self, root: &str) -> Result<ReleasedResult, Error> {
        let root = id("root", root)?;
        let mut tx = self.storage.begin().await?;
        let result = async {
            let tx = &mut *tx;
            let release = tx.read_release(&root).await?;
            let tree = self.materialize_tree(tx, Some(&release.commit)).await?;
            let tree = self.order(&tree)?;
            let tree = self.tree_result(tree, Vec::new())?;
            Ok(ReleasedResult { release, tree })
        }
        .await;
        finish(tx, result).await
    }

    /// Reads a commit's tree: the nearest snapshot on its chain with each
    /// later commit's patches laid over it, the nearest winning and a
    /// `DELETE` removing the entity.
    pub async fn materialize(&self, commit: &str) -> Result<TreeResult, Error> {
        let commit = id("commit", commit)?;
        let mut tx = self.storage.begin().await?;
        let result = async {
            let tree = self.materialize_tree(&mut *tx, Some(&commit)).await?;
            let tree = self.order(&tree)?;
            self.tree_result(tree, Vec::new())
        }
        .await;
        finish(tx, result).await
    }

    /// Reads a ref's tree: its base commit's tree with the ref's own rows
    /// laid over it.
    pub async fn compose(&self, ref_id: &str) -> Result<TreeResult, Error> {
        let ref_id = id("ref", ref_id)?;
        let mut tx = self.storage.begin().await?;
        let result = async {
            let tx = &mut *tx;
            let r = self.read_ref(tx, &ref_id, None, false).await?;
            let (tree, findings, _) = self.compose_ref(tx, &r).await?;
            self.tree_result(tree, findings)
        }
        .await;
        finish(tx, result).await
    }

    /// Lists the entities the trees of two commits differ on.
    pub async fn diff(&self, from: &str, to: &str) -> Result<Vec<Change>, Error> {
        let from = id("commit", from)?;
        let to = id("commit", to)?;
        let mut tx = self.storage.begin().await?;
        let result = async {
            let tx = &mut *tx;
            let from_tree = self.materialize_tree(tx, Some(&from)).await?;
            let to_tree = self.materialize_tree(tx, Some(&to)).await?;
            self.core.diff(&from_tree, &to_tree)
        }
        .await;
        finish(tx, result).await
    }

    /// Lists the commits a ref wrote, newest first.
    pub async fn history(&self, ref_id: &str) -> Result<Vec<Commit>, Error> {
        let ref_id = id("ref", ref_id)?;
        let mut tx = self.storage.begin().await?;
        let result = async {
            let tx = &mut *tx;
            let r = self.read_ref(tx, &ref_id, None, false).await?;
            match &r.head {
                None => Ok(Vec::new()),
                Some(head) => tx.ref_commits(&r.id, head, self.walk_ceiling).await,
            }
        }
        .await;
        finish(tx, result).await
    }

    /// Soft-deletes a ref at `version`, which frees its name.
    pub async fn discard(&self, actor: &str, ref_id: &str, version: i64) -> Result<(), Error> {
        let actor = actor_id(actor)?;
        let ref_id = id("ref", ref_id)?;
        let mut tx = self.storage.begin().await?;
        let result = async {
            let tx = &mut *tx;
            self.read_ref(tx, &ref_id, Some(version), false).await?;
            tx.discard_ref(&ref_id, version, &actor).await
        }
        .await;
        finish(tx, result).await
    }

    /// Reads a ref. With `expected` set it locks the ref's row and refuses a
    /// ref at another version, and with `write` set a sealed ref.
    async fn read_ref(
        &self,
        tx: &mut dyn Tx,
        id: &str,
        expected: Option<i64>,
        write: bool,
    ) -> Result<Ref, Error> {
        let r = match expected {
            Some(_) => tx.lock_ref(id).await?,
            None => tx.read_ref(id).await?,
        };
        if r.discarded {
            return Err(Error::NotFound);
        }
        if expected.is_some_and(|v| v != r.version) {
            return Err(Error::VersionConflict);
        }
        if write && r.sealed {
            return Err(Error::RefSealed);
        }
        Ok(r)
    }

    /// Reads a ref to write through at `version`: a live, unsealed change
    /// set. A primary line takes writes only from Merge.
    async fn read_draft(&self, tx: &mut dyn Tx, id: &str, version: i64) -> Result<Ref, Error> {
        let r = self.read_ref(tx, id, Some(version), true).await?;
        if r.parent.is_none() {
            return Err(Error::PrimaryMergeOnly);
        }
        Ok(r)
    }

    fn read_row(&self, kind: &KindRoles, row: &Value) -> Result<RowRoles, Error> {
        let column = |name: &str| row.get(name).unwrap_or(&Value::Null);
        let text = |name: &str| -> Result<String, Error> {
            column(name).as_str().map(str::to_owned).ok_or_else(|| {
                Error::Invalid(format!(
                    "{} row {name}: {} is not a string",
                    kind.name,
                    column(name)
                ))
            })
        };
        let version = column(&kind.version).as_i64().ok_or_else(|| {
            Error::Invalid(format!(
                "{} row {}: {} is not an integer",
                kind.name,
                kind.version,
                column(&kind.version)
            ))
        })?;
        let tombstone = match column(&kind.tombstone) {
            Value::Null => false,
            Value::Bool(b) => *b,
            other => {
                return Err(Error::Invalid(format!(
                    "{} row {}: {other} is not a boolean",
                    kind.name, kind.tombstone
                )))
            }
        };
        Ok(RowRoles {
            key: text(&kind.key)?,
            id: text(&kind.id)?,
            version,
            tombstone,
        })
    }

    /// Maps each kind's rows by entity key.
    fn index(&self, tree: &Tree) -> Result<Index, Error> {
        let mut by_key = Index::new();
        for (name, rows) in tree {
            let kind = self.kind(name)?;
            let rows_by_key = by_key.entry(name.clone()).or_default();
            for row in rows {
                let r = self.read_row(kind, row)?;
                rows_by_key.insert(r.key, row.clone());
            }
        }
        Ok(by_key)
    }

    /// Reads every row a ref holds, tombstones included.
    async fn own_rows(&self, tx: &mut dyn Tx, ref_id: &str) -> Result<Tree, Error> {
        let mut tree = Tree::new();
        for k in self.kinds.iter() {
            let rows = tx.rows(&k.name, ref_id).await?;
            if !rows.is_empty() {
                tree.insert(k.name.clone(), rows);
            }
        }
        Ok(tree)
    }

    /// Reads the pin set of a commit's tree: it walks the commit's parents to
    /// the nearest snapshot, takes that snapshot's pins, and lays each nearer
    /// commit's patches over them, the nearest winning and a `DELETE`
    /// removing the entity. It also returns the commit's distance from the
    /// nearest snapshot on its chain: 0 for a snapshotted commit, else how
    /// many commits separate them, the snapshot excluded, counting a chain
    /// with no snapshot from before its first commit. No commit is the empty
    /// set at distance 0.
    pub(crate) async fn resolve(
        &self,
        tx: &mut dyn Tx,
        commit: Option<&str>,
    ) -> Result<(PinSet, usize), Error> {
        let mut pins = PinSet::new();
        let Some(commit) = commit else {
            return Ok((pins, 0));
        };
        let chain = tx.walk(commit, self.walk_ceiling).await?;
        let Some(last) = chain.last() else {
            return Err(Error::NotFound);
        };
        for c in &chain {
            if c.schema_epoch > self.schema_epoch {
                return Err(Error::SchemaEpoch(format!(
                    "commit {} has epoch {}, this graph {}",
                    c.id, c.schema_epoch, self.schema_epoch
                )));
            }
        }
        let mut distance = chain.len();
        let mut patched = &chain[..];
        if last.snapshot {
            for entry in tx.snapshot(&last.id).await? {
                pins.insert((entry.kind.clone(), entry.entity_key.clone()), entry);
            }
            distance = chain.len() - 1;
            patched = &chain[..chain.len() - 1];
        } else if last.parent.is_some() {
            return Err(Error::WalkCeiling(format!("{} commits", self.walk_ceiling)));
        }
        if patched.is_empty() {
            return Ok((pins, distance));
        }
        let depth: HashMap<&str, usize> = patched
            .iter()
            .enumerate()
            .map(|(i, c)| (c.id.as_str(), i))
            .collect();
        let ids: Vec<String> = patched.iter().map(|c| c.id.clone()).collect();
        let mut nearest: HashMap<Entity, Patch> = HashMap::new();
        for p in tx.patches(&ids).await? {
            let at = (p.kind.clone(), p.entity_key.clone());
            let closer = match nearest.get(&at) {
                None => true,
                Some(seen) => depth.get(p.commit.as_str()) < depth.get(seen.commit.as_str()),
            };
            if closer {
                nearest.insert(at, p);
            }
        }
        apply(&mut pins, nearest.into_values());
        Ok((pins, distance))
    }

    /// Reads the history image of every row version a pin set holds.
    async fn images(&self, tx: &mut dyn Tx, pins: &PinSet) -> Result<Tree, Error> {
        let mut by_kind: HashMap<&str, Vec<Pin>> = HashMap::new();
        for entry in pins.values() {
            by_kind.entry(&entry.kind).or_default().push(Pin {
                id: entry.entity_id.clone(),
                version: entry.entity_version,
            });
        }
        let mut tree = Tree::new();
        for k in self.kinds.iter() {
            let Some(want) = by_kind.get(k.name.as_str()) else {
                continue;
            };
            let images = tx.images(&k.name, want).await?;
            if images.len() != want.len() {
                return Err(Error::HistoryMissing(format!(
                    "history returned {} images for {} pinned {} rows",
                    images.len(),
                    want.len(),
                    k.name
                )));
            }
            if !images.is_empty() {
                tree.insert(k.name.clone(), images);
            }
        }
        Ok(tree)
    }

    /// Reads a commit's tree: the row versions its pin set holds, read from
    /// history. No commit is the empty tree.
    async fn materialize_tree(&self, tx: &mut dyn Tx, commit: Option<&str>) -> Result<Tree, Error> {
        let (tree, _, _) = self.materialize_pins(tx, commit).await?;
        Ok(tree)
    }

    /// `materialize_tree`, with the commit's pin set and its distance from
    /// the nearest snapshot (see `resolve`).
    async fn materialize_pins(
        &self,
        tx: &mut dyn Tx,
        commit: Option<&str>,
    ) -> Result<(Tree, PinSet, usize), Error> {
        let (pins, distance) = self.resolve(tx, commit).await?;
        let tree = self.images(tx, &pins).await?;
        Ok((tree, pins, distance))
    }

    /// Snapshots a commit that has no snapshot yet. It reports whether it
    /// wrote one: a commit whose tree is empty has no entries to write.
    pub(crate) async fn ensure_snapshot(
        &self,
        tx: &mut dyn Tx,
        commit: &str,
        has_snapshot: bool,
    ) -> Result<bool, Error> {
        if has_snapshot {
            return Ok(false);
        }
        let (pins, _) = self.resolve(tx, Some(commit)).await?;
        if pins.is_empty() {
            return Ok(false);
        }
        let entries: Vec<SnapshotEntry> = pins.into_values().collect();
        tx.insert_snapshot(commit, &entries).await?;
        Ok(true)
    }

    /// The core's compose of the ref's base commit's tree (or the empty
    /// tree) and the ref's own rows. It returns the composed tree, the core's
    /// findings and the own rows.
    async fn compose_ref(
        &self,
        tx: &mut dyn Tx,
        r: &Ref,
    ) -> Result<(Tree, Vec<Finding>, Tree), Error> {
        let base = self.materialize_tree(tx, r.base.as_deref()).await?;
        let own = self.own_rows(tx, &r.id).await?;
        let (tree, findings) = self.core.compose(&base, &own)?;
        Ok((tree, findings, own))
    }

    /// The tree in the core's order: rows by their order column, then by
    /// entity key. A materialized tree comes out of history in no order.
    fn order(&self, tree: &Tree) -> Result<Tree, Error> {
        let (tree, _) = self.core.compose(tree, &Tree::new())?;
        Ok(tree)
    }

    fn tree_result(&self, tree: Tree, findings: Vec<Finding>) -> Result<TreeResult, Error> {
        let content_hash = self.core.content_hash(&tree)?;
        Ok(TreeResult {
            tree,
            content_hash,
            findings,
        })
    }

    /// Writes a row onto a ref as the ref's row of its entity: live, or with
    /// `tombstone` set, the row that deletes the entity on the ref. It
    /// returns the row as stored.
    async fn write_row(
        &self,
        tx: &mut dyn Tx,
        kind: &str,
        r: &Ref,
        row: Value,
        tombstone: bool,
        actor: &str,
    ) -> Result<Value, Error> {
        tx.upsert_row(
            kind,
            RowWrite {
                ref_id: r.id.clone(),
                root: r.root.clone(),
                row,
                tombstone,
                actor: actor.to_owned(),
            },
        )
        .await
    }

    /// Writes the row that deletes an entity on a ref: a copy of the
    /// entity's effective row, so every required column holds, with its
    /// tombstone set.
    async fn delete_entity(
        &self,
        tx: &mut dyn Tx,
        r: &Ref,
        composed: &Index,
        kind: &str,
        key: &str,
        actor: &str,
    ) -> Result<(), Error> {
        let Some(row) = composed.get(kind).and_then(|rows| rows.get(key)) else {
            return Err(Error::EntityNotFound(format!("{kind} {key}")));
        };
        self.write_row(tx, kind, r, row.clone(), true, actor)
            .await?;
        Ok(())
    }

    /// Composes the ref, diffs it against its last commit (or its base) and
    /// writes a commit with a patch per changed entity. It returns the new
    /// commit, or [`Error::NothingToCommit`]. The caller moves the ref's head.
    async fn write_commit(
        &self,
        tx: &mut dyn Tx,
        r: &Ref,
        options: &CommitOptions,
        actor: &str,
    ) -> Result<Commit, Error> {
        let (composed, _, own) = self.compose_ref(tx, r).await?;
        let findings = self.core.validate(&composed)?;
        if !findings.is_empty() {
            return Err(Error::InvalidTree(findings));
        }
        let parent = r.head.clone().or_else(|| r.base.clone());
        let (previous, mut pins, distance) = self.materialize_pins(tx, parent.as_deref()).await?;
        let changes = self.core.diff(&previous, &composed)?;
        if changes.is_empty() {
            return Err(Error::NothingToCommit);
        }
        let own_by_key = self.index(&own)?;
        let previous_by_key = self.index(&previous)?;
        let mut patches = Vec::with_capacity(changes.len());
        for change in &changes {
            let kind = self.kind(&change.kind)?;
            // An ADD or UPDATE pins the winning row. A DELETE pins the ref's
            // tombstone, or the entity's last committed row when a removed
            // parent took it with it.
            let mut pinned = change.row.as_ref();
            if change.operation == "DELETE" {
                pinned = previous_by_key
                    .get(&change.kind)
                    .and_then(|rows| rows.get(&change.entity_key));
                if let Some(own_row) = own_by_key
                    .get(&change.kind)
                    .and_then(|rows| rows.get(&change.entity_key))
                {
                    if self.read_row(kind, own_row).is_ok_and(|r| r.tombstone) {
                        pinned = Some(own_row);
                    }
                }
            }
            let pinned = pinned.ok_or_else(|| {
                Error::Invalid(format!(
                    "no {} row to pin for {}",
                    change.kind, change.entity_key
                ))
            })?;
            let row = self.read_row(kind, pinned)?;
            patches.push(Patch {
                commit: String::new(),
                kind: change.kind.clone(),
                entity_key: change.entity_key.clone(),
                entity_id: row.id,
                entity_version: row.version,
                operation: change.operation.clone(),
            });
        }
        let content_hash = self.core.content_hash(&composed)?;
        let sequence = if options.tag {
            Some(tx.next_sequence(&r.root).await?)
        } else {
            None
        };
        let mut written = tx
            .insert_commit(NewCommit {
                root: r.root.clone(),
                ref_id: r.id.clone(),
                parent,
                message: options.message.clone(),
                schema_epoch: self.schema_epoch,
                content_hash,
                sequence,
                actor: actor.to_owned(),
            })
            .await?;
        tx.insert_patches(&written.id, &patches).await?;
        // A tagged commit is snapshotted, and so is one snapshot_every
        // commits past the nearest snapshot on its chain: its parent's pins
        // with its own patches laid over them.
        if options.tag || distance + 1 >= self.snapshot_every {
            apply(&mut pins, patches.into_iter());
            if !pins.is_empty() {
                let entries: Vec<SnapshotEntry> = pins.into_values().collect();
                tx.insert_snapshot(&written.id, &entries).await?;
                written.snapshot = true;
            }
        }
        Ok(written)
    }

    /// Commits the ref and moves its head, sealing it when asked. Nothing to
    /// commit is not an error when `allow_empty` is set: the ref's version
    /// still moves and the returned commit is `None`.
    async fn commit_and_move(
        &self,
        tx: &mut dyn Tx,
        r: &Ref,
        options: &CommitOptions,
        allow_empty: bool,
        seal: bool,
        actor: &str,
    ) -> Result<(Ref, Option<Commit>), Error> {
        let written = match self.write_commit(tx, r, options, actor).await {
            Ok(commit) => Some(commit),
            Err(Error::NothingToCommit) if allow_empty => None,
            Err(error) => return Err(error),
        };
        let moved = tx
            .update_ref(RefUpdate {
                id: r.id.clone(),
                version: r.version,
                head: written.as_ref().map(|c| c.id.clone()),
                base: None,
                seal,
                actor: actor.to_owned(),
            })
            .await?;
        Ok((moved, written))
    }

    /// Merges `source`'s head into `target` against `source`'s base. With no
    /// conflicts it writes every entity the target does not already hold as
    /// the merge left it: the merged row, or the row that deletes the entity.
    async fn merge_in(
        &self,
        tx: &mut dyn Tx,
        source: &Ref,
        target: &Ref,
        resolutions: &[Resolution],
        actor: &str,
    ) -> Result<Vec<Conflict>, Error> {
        let base = self.materialize_tree(tx, source.base.as_deref()).await?;
        let theirs = match &source.head {
            None => base.clone(),
            Some(head) => self.materialize_tree(tx, Some(head)).await?,
        };
        let (ours, _, _) = self.compose_ref(tx, target).await?;
        let result = self.core_merge(&base, &ours, &theirs, resolutions)?;
        if !result.conflicts.is_empty() {
            return Ok(result.conflicts);
        }
        let merged_by_key = self.index(&result.merged)?;
        let ours_by_key = self.index(&ours)?;
        for outcome in &result.entities {
            // A result equal to ours is ours' side, a delete on both sides
            // included, so a delete from another side deletes a live entity
            // of ours.
            if outcome.side == "ours" {
                continue;
            }
            if outcome.deleted {
                self.delete_entity(
                    tx,
                    target,
                    &ours_by_key,
                    &outcome.kind,
                    &outcome.entity_key,
                    actor,
                )
                .await?;
                continue;
            }
            let Some(row) = merged_by_key
                .get(&outcome.kind)
                .and_then(|rows| rows.get(&outcome.entity_key))
            else {
                return Err(Error::Invalid(format!(
                    "the merge left no {} row for {}",
                    outcome.kind, outcome.entity_key
                )));
            };
            self.write_row(tx, &outcome.kind, target, row.clone(), false, actor)
                .await?;
        }
        Ok(Vec::new())
    }

    /// The core's three-way merge of three trees, with the resolutions'
    /// entity keys in their canonical form.
    fn core_merge(
        &self,
        base: &Tree,
        ours: &Tree,
        theirs: &Tree,
        resolutions: &[Resolution],
    ) -> Result<crate::core::MergeOutput, Error> {
        let resolutions = resolutions
            .iter()
            .map(|r| {
                Ok(Resolution {
                    entity_key: id("resolution entity key", &r.entity_key)?,
                    ..r.clone()
                })
            })
            .collect::<Result<Vec<_>, Error>>()?;
        self.core.merge(base, ours, theirs, &resolutions)
    }

    /// Writes a ref's own rows so that, over the tree of its base (`base`),
    /// it composes to `want`. `current` is what the ref composes to now and
    /// `own` its rows. Each entity `want` holds differently from `base` gets
    /// the ref's row of it (a tombstone where `want` lacks it) unless the
    /// ref's row already gives it; every other row the ref holds is removed,
    /// so the entity reads through the base.
    #[allow(clippy::too_many_arguments)]
    async fn overlay(
        &self,
        tx: &mut dyn Tx,
        r: &Ref,
        base: &Tree,
        want: &Tree,
        current: &Tree,
        own: &Tree,
        actor: &str,
    ) -> Result<(), Error> {
        let changes = self.core.diff(base, want)?;
        let moved = self.core.diff(current, want)?;
        let differs: HashSet<Entity> = moved.into_iter().map(|c| (c.kind, c.entity_key)).collect();
        let own_by_key = self.index(own)?;
        let base_by_key = self.index(base)?;
        let mut kept: HashSet<Entity> = HashSet::with_capacity(changes.len());
        for change in &changes {
            let at = (change.kind.clone(), change.entity_key.clone());
            kept.insert(at.clone());
            let kind = self.kind(&change.kind)?;
            let own_row = own_by_key
                .get(&change.kind)
                .and_then(|rows| rows.get(&change.entity_key));
            let tombstone = match own_row {
                Some(row) => self.read_row(kind, row)?.tombstone,
                None => false,
            };
            if change.operation == "DELETE" {
                if own_row.is_some() && tombstone {
                    continue;
                }
                self.delete_entity(tx, r, &base_by_key, &change.kind, &change.entity_key, actor)
                    .await?;
                continue;
            }
            if own_row.is_some() && !tombstone && !differs.contains(&at) {
                continue;
            }
            let row = change.row.clone().unwrap_or(Value::Null);
            self.write_row(tx, &change.kind, r, row, false, actor)
                .await?;
        }
        for k in self.kinds.iter() {
            let Some(rows) = own_by_key.get(&k.name) else {
                continue;
            };
            let mut keys: Vec<&String> = rows.keys().collect();
            keys.sort();
            for key in keys {
                if kept.contains(&(k.name.clone(), key.clone())) {
                    continue;
                }
                tx.remove_row(&k.name, &r.id, key, actor).await?;
            }
        }
        Ok(())
    }

    /// Writes the rows that make the ref compose to `tree`: each entity
    /// `tree` holds that the ref composes differently is written from `tree`,
    /// and each entity only the ref holds is deleted on it.
    async fn revert_rows(
        &self,
        tx: &mut dyn Tx,
        r: &Ref,
        tree: &Tree,
        actor: &str,
    ) -> Result<(), Error> {
        let (current, _, _) = self.compose_ref(tx, r).await?;
        let changes = self.core.diff(&current, tree)?;
        let current_by_key = self.index(&current)?;
        for change in changes {
            if change.operation == "DELETE" {
                self.delete_entity(
                    tx,
                    r,
                    &current_by_key,
                    &change.kind,
                    &change.entity_key,
                    actor,
                )
                .await?;
                continue;
            }
            let row = change.row.unwrap_or(Value::Null);
            self.write_row(tx, &change.kind, r, row, false, actor)
                .await?;
        }
        Ok(())
    }
}

/// Lays patches over a pin set: a `DELETE` removes its entity, and any other
/// patch pins its row version.
fn apply(pins: &mut PinSet, patches: impl Iterator<Item = Patch>) {
    for p in patches {
        let at = (p.kind.clone(), p.entity_key.clone());
        if p.operation == "DELETE" {
            pins.remove(&at);
            continue;
        }
        pins.insert(
            at,
            SnapshotEntry {
                kind: p.kind,
                entity_key: p.entity_key,
                entity_id: p.entity_id,
                entity_version: p.entity_version,
            },
        );
    }
}
