//! The graph's maintenance: one sweep pass, and a sweeper that repeats it.

use std::collections::{BTreeMap, HashMap};
use std::future::Future;
use std::time::Duration;

use serde::Serialize;

use crate::engine::{actor_id, finish, Engine};
use crate::storage::{CommitNode, Tx};
use crate::Error;

/// How long after a ref is discarded a sweep keeps its member rows, unless
/// [`SweepOptions::discard_grace`] says otherwise.
pub const DEFAULT_DISCARD_GRACE: Duration = Duration::from_secs(7 * 24 * 3600);

/// Configures a maintenance pass.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct SweepOptions {
    /// Who the pass writes as: the discards it makes and the deletes of
    /// discarded refs' rows record it.
    pub actor: String,
    /// How long after a ref is discarded its member rows are kept; zero is
    /// [`DEFAULT_DISCARD_GRACE`].
    pub discard_grace: Duration,
    /// When not zero, discards every live change set with no write for that
    /// long. Zero discards none.
    pub abandon_after: Duration,
    /// Caps how many history images of each kind the pass prunes; 0 is no
    /// cap.
    pub prune_batch: i64,
}

/// What one pass did. Each count is keyed by kind and holds only kinds with
/// a nonzero count. It serializes as the scenario files' `report`.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize)]
pub struct SweepReport {
    /// True when another pass held the graph's sweep lock, and this one did
    /// nothing.
    pub skipped: bool,
    /// How many idle change sets the pass discarded.
    pub abandoned: i64,
    /// How many discarded refs had member rows the pass deleted.
    #[serde(rename = "collectedRefs")]
    pub collected_refs: i64,
    /// How many rows of each kind the pass deleted from discarded refs.
    #[serde(rename = "collectedRows")]
    pub collected_rows: BTreeMap<String, i64>,
    /// How many history images of each kind the pass pruned.
    pub pruned: BTreeMap<String, i64>,
    /// How many missing snapshots the pass wrote.
    pub snapshots: i64,
}

impl Engine {
    /// Runs one maintenance pass in one transaction, under the graph's sweep
    /// lock; when another pass holds it, the pass does nothing and reports
    /// `skipped`. In order, the pass discards the change sets idle past
    /// `abandon_after`, leaving one a write reaches after the pass read it;
    /// deletes the member rows of refs discarded longer ago than
    /// `discard_grace`, keeping their ref rows and commits as the audit
    /// trail; prunes each kind's history past its declared retention,
    /// keeping every row version a patch or a snapshot pins; and writes each
    /// snapshot the graph's rules call for and it lacks. Nothing calls it
    /// unless a service does.
    pub async fn sweep(&self, options: &SweepOptions) -> Result<SweepReport, Error> {
        let actor = actor_id(&options.actor)?;
        let grace = if options.discard_grace.is_zero() {
            DEFAULT_DISCARD_GRACE
        } else {
            options.discard_grace
        };
        let mut tx = self.storage.begin().await?;
        let result = self.sweep_in(&mut *tx, options, grace, &actor).await;
        finish(tx, result).await
    }

    async fn sweep_in(
        &self,
        tx: &mut dyn Tx,
        options: &SweepOptions,
        grace: Duration,
        actor: &str,
    ) -> Result<SweepReport, Error> {
        let mut report = SweepReport::default();
        if !tx.sweep_lock().await? {
            report.skipped = true;
            return Ok(report);
        }
        if !options.abandon_after.is_zero() {
            for r in tx.idle_drafts(options.abandon_after).await? {
                // A write that reached the ref after idle_drafts read it moved
                // its version, so the ref is no longer idle: leave it.
                match tx.discard_ref(&r.id, r.version, actor).await {
                    Ok(()) => report.abandoned += 1,
                    Err(Error::VersionConflict) => {}
                    Err(error) => return Err(error),
                }
            }
        }
        for r in tx.discarded_refs(grace).await? {
            let mut rows = 0;
            for k in self.kinds.iter() {
                let n = tx.remove_ref_rows(&k.name, &r.id, actor).await?;
                if n > 0 {
                    *report.collected_rows.entry(k.name.clone()).or_default() += n;
                    rows += n;
                }
            }
            if rows > 0 {
                report.collected_refs += 1;
            }
        }
        for k in self.kinds.iter() {
            let n = tx.prune(&k.name, 0, options.prune_batch).await?;
            if n > 0 {
                report.pruned.insert(k.name.clone(), n);
            }
        }
        report.snapshots = self.backfill(tx).await?;
        Ok(report)
    }

    /// Writes every snapshot the graph's rules call for and it lacks: of a
    /// tagged commit (a release takes only tagged ones, so this covers a
    /// released commit), and of a commit `snapshot_every` commits past the
    /// nearest snapshot on its chain. It visits each commit after its
    /// parent, so every walk it takes stops at a snapshot at most
    /// `snapshot_every` commits away. It returns how many snapshots it
    /// wrote.
    async fn backfill(&self, tx: &mut dyn Tx) -> Result<i64, Error> {
        let nodes = tx.commits().await?;
        let by_id: HashMap<&str, &CommitNode> = nodes.iter().map(|n| (n.id.as_str(), n)).collect();
        // Each visited commit's distance from the nearest snapshot on its
        // chain, 0 for a snapshotted commit.
        let mut distance: HashMap<String, usize> = HashMap::with_capacity(nodes.len());
        let mut written = 0;
        for start in &nodes {
            // Visit start's unvisited ancestors from the oldest down.
            let mut path: Vec<&CommitNode> = Vec::new();
            let mut at = Some(start);
            while let Some(node) = at {
                if distance.contains_key(&node.id) {
                    break;
                }
                path.push(node);
                at = node
                    .parent
                    .as_deref()
                    .and_then(|parent| by_id.get(parent).copied());
            }
            for node in path.into_iter().rev() {
                if node.snapshot {
                    distance.insert(node.id.clone(), 0);
                    continue;
                }
                let mut d = node
                    .parent
                    .as_ref()
                    .and_then(|parent| distance.get(parent))
                    .copied()
                    .unwrap_or(0)
                    + 1;
                if node.tagged || d >= self.snapshot_every {
                    if self.ensure_snapshot(tx, &node.id, false).await? {
                        written += 1;
                    }
                    d = 0;
                }
                distance.insert(node.id.clone(), d);
            }
        }
        Ok(written)
    }

    /// Runs a sweep pass now and then once every `interval` until `shutdown`
    /// completes; a pass under way finishes first, so no transaction is cut
    /// off. Each pass takes the graph's sweep lock, so one replica sweeps at
    /// a time and a pass that finds it held is skipped. `on_pass` receives
    /// each pass's report or error; an error does not stop the sweeper. An
    /// interval of zero is refused before any pass.
    pub async fn run_sweeper<F>(
        &self,
        interval: Duration,
        options: &SweepOptions,
        mut on_pass: F,
        shutdown: impl Future<Output = ()>,
    ) -> Result<(), Error>
    where
        F: FnMut(Result<SweepReport, Error>),
    {
        if interval.is_zero() {
            return Err(Error::Invalid(
                "a sweeper's interval must be positive".to_owned(),
            ));
        }
        let mut ticker = tokio::time::interval_at(tokio::time::Instant::now() + interval, interval);
        ticker.set_missed_tick_behavior(tokio::time::MissedTickBehavior::Delay);
        let mut shutdown = std::pin::pin!(shutdown);
        loop {
            on_pass(self.sweep(options).await);
            tokio::select! {
                biased;
                () = &mut shutdown => return Ok(()),
                _ = ticker.tick() => {}
            }
        }
    }
}
