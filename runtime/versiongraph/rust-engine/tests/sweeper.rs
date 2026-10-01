//! The sweeper against Postgres: it skips while another transaction holds
//! the graph's sweep lock, and a pass leaves an idle change set that a write
//! reaches while the pass runs.

mod support;

use std::sync::Arc;
use std::time::Duration;

use async_trait::async_trait;
use serde_json::{json, Value};
use superschematic_versiongraph_engine::storage::{
    Commit, CommitNode, NewCommit, NewRef, Patch, Pin, Ref, RefUpdate, Release, ReleaseWrite,
    RowWrite, SnapshotEntry, Storage, Tx,
};
use superschematic_versiongraph_engine::{
    Edits, Engine, Error, KindEdits, Options, SweepOptions, SweepReport,
};
use tokio::sync::mpsc;

fn engine(storage: Arc<dyn Storage>) -> Engine {
    Engine::new(
        &support::descriptor(),
        storage,
        Options {
            schema_epoch: support::FIXTURE_SCHEMA_EPOCH,
            snapshot_every: support::FIXTURE_SNAPSHOT_EVERY,
            ..Options::default()
        },
    )
    .expect("the fixture's engine")
}

fn sweep_options() -> SweepOptions {
    SweepOptions {
        actor: support::DEFAULT_ACTOR.to_owned(),
        ..SweepOptions::default()
    }
}

/// The sweeper runs while another transaction holds the graph's sweep lock:
/// its passes are skipped until the lock is released, the next pass sweeps,
/// and its shutdown stops it.
#[tokio::test(flavor = "multi_thread")]
async fn run_sweeper_skips_while_the_lock_is_held() {
    let Some(dsn) = support::database("run_sweeper_skips_while_the_lock_is_held") else {
        return;
    };
    let schema = support::Schema::create(&dsn, "vg_rust_sweeper").await;
    let adapter = support::adapter();
    let holder_store = schema.storage(&adapter).await;
    let mut holder = holder_store.begin().await.expect("begin");
    assert!(
        holder.sweep_lock().await.expect("take the lock"),
        "the lock is free"
    );

    let engine = engine(schema.storage(&adapter).await);
    let (passes, mut received) = mpsc::unbounded_channel::<Result<SweepReport, Error>>();
    let (stop, stopped) = tokio::sync::oneshot::channel::<()>();
    let sweeper = tokio::spawn(async move {
        engine
            .run_sweeper(
                Duration::from_millis(5),
                &sweep_options(),
                move |pass| {
                    let _ = passes.send(pass);
                },
                async move {
                    let _ = stopped.await;
                },
            )
            .await
    });
    for i in 0..3 {
        let report = next_pass(&mut received).await;
        assert!(
            report.skipped,
            "pass {i} swept while another transaction held the lock: {report:?}"
        );
    }
    holder.rollback().await.expect("release the lock");
    while next_pass(&mut received).await.skipped {}
    stop.send(()).expect("stop the sweeper");
    let result = tokio::time::timeout(Duration::from_secs(10), sweeper)
        .await
        .expect("the sweeper stops after its shutdown")
        .expect("the sweeper's task");
    assert!(result.is_ok(), "the sweeper returned {result:?}");
}

async fn next_pass(
    received: &mut mpsc::UnboundedReceiver<Result<SweepReport, Error>>,
) -> SweepReport {
    match tokio::time::timeout(Duration::from_secs(10), received.recv()).await {
        Ok(Some(Ok(report))) => report,
        Ok(Some(Err(error))) => panic!("a pass failed: {error}"),
        _ => panic!("no pass within 10s"),
    }
}

/// An interval of zero is refused before any pass.
#[tokio::test]
async fn run_sweeper_refuses_an_interval_that_is_not_positive() {
    let engine = engine(Arc::new(NoStorage));
    let mut called = false;
    let result = engine
        .run_sweeper(Duration::ZERO, &sweep_options(), |_| called = true, async {
        })
        .await;
    assert!(result.is_err(), "run_sweeper took a zero interval");
    assert!(!called, "run_sweeper ran a pass with a zero interval");
}

/// A write lands on an idle change set after the pass read it as idle and
/// before the pass discards it. The pass leaves that change set live, since
/// it is no longer idle, and the rest of the pass lands: it discards the
/// other idle change set and collects a discarded ref's rows.
#[tokio::test(flavor = "multi_thread")]
async fn sweep_skips_an_idle_draft_written_during_the_pass() {
    let Some(dsn) = support::database("sweep_skips_an_idle_draft_written_during_the_pass") else {
        return;
    };
    let schema = support::Schema::create(&dsn, "vg_rust_sweeper").await;
    let adapter = support::adapter();
    let store = schema.storage(&adapter).await;
    let engine = engine(store.clone());
    let sql = schema.connect().await;
    sql.execute(
        "INSERT INTO recipe (id, title, created_by) VALUES ($1::text::uuid, 'Bread', $2::text::uuid)",
        &[&support::hyphenated("Bread"), &support::hyphenated(support::DEFAULT_ACTOR)],
    )
    .await
    .expect("insert the root");
    let actor = support::DEFAULT_ACTOR;
    let main = engine
        .create_primary(actor, "Bread", "main")
        .await
        .expect("create the primary line");
    let idle = engine
        .branch(actor, &main.id, "idle")
        .await
        .expect("branch idle");
    let busy = engine
        .branch(actor, &main.id, "busy")
        .await
        .expect("branch busy");
    let dropped = engine
        .branch(actor, &main.id, "dropped")
        .await
        .expect("branch dropped");
    let mix: Edits = [(
        "step".to_owned(),
        KindEdits {
            upsert: vec![
                json!({"entity_key": "Mix", "position": 1, "instruction": "Mix", "timings": {}}),
            ],
            ..KindEdits::default()
        },
    )]
    .into();
    let saved = engine
        .save(actor, &dropped.id, dropped.version, &mix)
        .await
        .expect("save on dropped");
    engine
        .discard(actor, &dropped.id, saved.ref_.version)
        .await
        .expect("discard dropped");
    sql.execute(
        "UPDATE recipe_ref SET updated_at = now() - interval '3 days' WHERE id IN ($1::text::uuid, $2::text::uuid)",
        &[&support::hyphenated(&idle.id), &support::hyphenated(&busy.id)],
    )
    .await
    .expect("age the change sets");
    sql.execute(
        "UPDATE recipe_ref SET deleted_at = now() - interval '8 days' WHERE id = $1::text::uuid",
        &[&support::hyphenated(&dropped.id)],
    )
    .await
    .expect("age the discard");

    // The write goes through another connection, in a transaction of its own
    // that commits while the pass's is open.
    let writer = engine.with_storage(schema.storage(&adapter).await);
    let busy_id = busy.id.clone();
    let (wrote, mut written) = mpsc::unbounded_channel();
    let racing = Arc::new(Racing {
        inner: store,
        after_idle_drafts: Arc::new(move || {
            let writer = writer.clone();
            let (busy, mix, wrote) = (busy.id.clone(), mix.clone(), wrote.clone());
            Box::pin(async move {
                let r = read_ref(&writer, &busy).await?;
                let saved = writer
                    .save(support::DEFAULT_ACTOR, &busy, r.version, &mix)
                    .await?;
                let _ = wrote.send(saved);
                Ok(())
            })
        }),
    });
    let report = engine
        .with_storage(racing)
        .sweep(&SweepOptions {
            abandon_after: Duration::from_secs(48 * 3600),
            ..sweep_options()
        })
        .await
        .expect("sweep");
    assert!(
        written.try_recv().is_ok(),
        "the pass did not read the idle change sets"
    );
    assert_eq!(report.abandoned, 1, "{report:?}");
    assert_eq!(report.collected_refs, 1, "{report:?}");
    assert_eq!(report.collected_rows.get("step"), Some(&1), "{report:?}");
    let composed = engine.compose(&idle.id).await;
    assert!(
        matches!(composed, Err(Error::NotFound)),
        "compose the idle change set: {composed:?}"
    );
    let tree = engine
        .compose(&busy_id)
        .await
        .expect("compose the written change set");
    assert_eq!(
        tree.tree.get("step").map(Vec::len),
        Some(1),
        "{:?}",
        tree.tree
    );
}

async fn read_ref(engine: &Engine, id: &str) -> Result<Ref, Error> {
    let mut tx = engine.storage().begin().await?;
    let r = tx.read_ref(id).await;
    tx.rollback().await?;
    r
}

type AfterIdleDrafts = Arc<
    dyn Fn() -> std::pin::Pin<Box<dyn std::future::Future<Output = Result<(), Error>> + Send>>
        + Send
        + Sync,
>;

/// Runs `after_idle_drafts` in a pass's transaction as soon as the pass has
/// read the idle change sets.
struct Racing<S> {
    inner: Arc<S>,
    after_idle_drafts: AfterIdleDrafts,
}

#[async_trait]
impl<S: Storage> Storage for Racing<S> {
    async fn begin(&self) -> Result<Box<dyn Tx + '_>, Error> {
        Ok(Box::new(RacingTx {
            inner: self.inner.begin().await?,
            after: self.after_idle_drafts.clone(),
        }))
    }
}

struct RacingTx<'a> {
    inner: Box<dyn Tx + 'a>,
    after: AfterIdleDrafts,
}

#[async_trait]
impl Tx for RacingTx<'_> {
    async fn commit(self: Box<Self>) -> Result<(), Error> {
        self.inner.commit().await
    }
    async fn rollback(self: Box<Self>) -> Result<(), Error> {
        self.inner.rollback().await
    }
    async fn idle_drafts(&mut self, idle: Duration) -> Result<Vec<Ref>, Error> {
        let refs = self.inner.idle_drafts(idle).await?;
        (self.after)().await?;
        Ok(refs)
    }
    async fn create_ref(&mut self, new: NewRef) -> Result<Ref, Error> {
        self.inner.create_ref(new).await
    }
    async fn read_ref(&mut self, id: &str) -> Result<Ref, Error> {
        self.inner.read_ref(id).await
    }
    async fn lock_ref(&mut self, id: &str) -> Result<Ref, Error> {
        self.inner.lock_ref(id).await
    }
    async fn update_ref(&mut self, update: RefUpdate) -> Result<Ref, Error> {
        self.inner.update_ref(update).await
    }
    async fn discard_ref(&mut self, id: &str, version: i64, actor: &str) -> Result<(), Error> {
        self.inner.discard_ref(id, version, actor).await
    }
    async fn rows(&mut self, kind: &str, ref_id: &str) -> Result<Vec<Value>, Error> {
        self.inner.rows(kind, ref_id).await
    }
    async fn upsert_row(&mut self, kind: &str, write: RowWrite) -> Result<Value, Error> {
        self.inner.upsert_row(kind, write).await
    }
    async fn remove_row(
        &mut self,
        kind: &str,
        ref_id: &str,
        key: &str,
        actor: &str,
    ) -> Result<bool, Error> {
        self.inner.remove_row(kind, ref_id, key, actor).await
    }
    async fn images(&mut self, kind: &str, pins: &[Pin]) -> Result<Vec<Value>, Error> {
        self.inner.images(kind, pins).await
    }
    async fn read_commit(&mut self, id: &str) -> Result<Commit, Error> {
        self.inner.read_commit(id).await
    }
    async fn insert_commit(&mut self, commit: NewCommit) -> Result<Commit, Error> {
        self.inner.insert_commit(commit).await
    }
    async fn insert_patches(&mut self, commit: &str, patches: &[Patch]) -> Result<(), Error> {
        self.inner.insert_patches(commit, patches).await
    }
    async fn walk(&mut self, commit: &str, limit: usize) -> Result<Vec<Commit>, Error> {
        self.inner.walk(commit, limit).await
    }
    async fn ref_commits(
        &mut self,
        ref_id: &str,
        head: &str,
        limit: usize,
    ) -> Result<Vec<Commit>, Error> {
        self.inner.ref_commits(ref_id, head, limit).await
    }
    async fn patches(&mut self, commits: &[String]) -> Result<Vec<Patch>, Error> {
        self.inner.patches(commits).await
    }
    async fn next_sequence(&mut self, root: &str) -> Result<i64, Error> {
        self.inner.next_sequence(root).await
    }
    async fn snapshot(&mut self, commit: &str) -> Result<Vec<SnapshotEntry>, Error> {
        self.inner.snapshot(commit).await
    }
    async fn insert_snapshot(
        &mut self,
        commit: &str,
        entries: &[SnapshotEntry],
    ) -> Result<(), Error> {
        self.inner.insert_snapshot(commit, entries).await
    }
    async fn commits(&mut self) -> Result<Vec<CommitNode>, Error> {
        self.inner.commits().await
    }
    async fn read_release(&mut self, root: &str) -> Result<Release, Error> {
        self.inner.read_release(root).await
    }
    async fn write_release(&mut self, write: ReleaseWrite) -> Result<Release, Error> {
        self.inner.write_release(write).await
    }
    async fn prune(
        &mut self,
        kind: &str,
        retention_days: i64,
        batch_size: i64,
    ) -> Result<i64, Error> {
        self.inner.prune(kind, retention_days, batch_size).await
    }
    async fn discarded_refs(&mut self, grace: Duration) -> Result<Vec<Ref>, Error> {
        self.inner.discarded_refs(grace).await
    }
    async fn remove_ref_rows(
        &mut self,
        kind: &str,
        ref_id: &str,
        actor: &str,
    ) -> Result<i64, Error> {
        self.inner.remove_ref_rows(kind, ref_id, actor).await
    }
    async fn sweep_lock(&mut self) -> Result<bool, Error> {
        self.inner.sweep_lock().await
    }
}

/// A storage the refused sweeper never reaches.
struct NoStorage;

#[async_trait]
impl Storage for NoStorage {
    async fn begin(&self) -> Result<Box<dyn Tx + '_>, Error> {
        Err(Error::Invalid("no storage".to_owned()))
    }
}
