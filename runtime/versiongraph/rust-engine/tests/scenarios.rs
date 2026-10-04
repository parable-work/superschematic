//! Runs every scenario in runtime/versiongraph/testdata/scenarios through the
//! engine and the Postgres adapter, each in a schema of its own that holds
//! the fixture's DDL. The format is runtime/versiongraph/README.md
//! ("Scenarios"). It needs the Postgres
//! SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL names.

mod support;

use std::collections::{BTreeMap, HashMap};
use std::fs;
use std::sync::Arc;
use std::time::Duration;

use serde::{Deserialize, Deserializer};
use serde_json::{json, Map, Value};
use superschematic_versiongraph_engine::postgres::{self, PostgresStorage, TokioPostgres};
use superschematic_versiongraph_engine::storage::{Storage, Tx};
use superschematic_versiongraph_engine::{
    Change, Commit, CommitOptions, Conflict, Edits, Engine, Error, KindEdits, Options, Ref,
    Release, Resolution, SweepOptions, SweepReport, TreeResult,
};

/// The backend this runner runs the scenarios on. A step that lists its
/// backends runs here only when it lists this one, and an sql step runs its
/// statement for this backend.
const BACKEND: &str = "postgres";

/// The backends a scenario may name.
const KNOWN_BACKENDS: [&str; 2] = ["postgres", "sqlite"];

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Scenario {
    name: String,
    #[allow(dead_code)]
    description: String,
    #[serde(default)]
    roots: Option<Vec<String>>,
    steps: Vec<Step>,
}

#[derive(Deserialize, Default)]
#[serde(deny_unknown_fields, default)]
struct Step {
    op: String,
    backends: Option<Vec<String>>,
    #[serde(rename = "as")]
    as_: String,
    actor: Option<String>,
    root: String,
    name: String,
    #[serde(rename = "ref")]
    ref_: String,
    from: String,
    to: String,
    source: String,
    target: String,
    commit: String,
    #[serde(rename = "toCommit")]
    to_commit: String,
    version: Option<i64>,
    edits: BTreeMap<String, StepEdits>,
    message: String,
    tag: bool,
    resolutions: Vec<Resolution>,
    #[serde(rename = "walkCeiling")]
    walk_ceiling: usize,
    #[serde(rename = "schemaEpoch")]
    schema_epoch: Option<i64>,
    #[serde(rename = "snapshotEvery")]
    snapshot_every: usize,
    sweep: Option<StepSweep>,
    kind: String,
    #[serde(deserialize_with = "statements")]
    statement: Option<BTreeMap<String, String>>,
    args: Vec<SqlArg>,
    expect: Expect,
}

#[derive(Deserialize, Default)]
#[serde(deny_unknown_fields, default)]
struct StepEdits {
    upsert: Vec<Value>,
    delete: Vec<String>,
    unset: Vec<String>,
}

/// A sweep step's options; durations are in seconds.
#[derive(Deserialize, Default)]
#[serde(deny_unknown_fields, default)]
struct StepSweep {
    #[serde(rename = "discardGraceSeconds")]
    discard_grace_seconds: u64,
    #[serde(rename = "abandonAfterSeconds")]
    abandon_after_seconds: u64,
    #[serde(rename = "pruneBatch")]
    prune_batch: i64,
}

#[derive(Deserialize, Default)]
#[serde(deny_unknown_fields, default)]
struct SqlArg {
    uuid: String,
    #[serde(rename = "ref")]
    ref_: String,
    commit: String,
}

type PartialRow = Map<String, Value>;

#[derive(Deserialize, Default)]
#[serde(deny_unknown_fields, default)]
struct Expect {
    error: String,
    #[serde(rename = "ref")]
    ref_: Option<RefExpect>,
    #[serde(deserialize_with = "present")]
    commit: Option<Value>,
    tree: Option<BTreeMap<String, Vec<PartialRow>>>,
    saved: Option<BTreeMap<String, Vec<PartialRow>>>,
    #[serde(rename = "contentHash")]
    content_hash: String,
    #[serde(rename = "contentHashOf")]
    content_hash_of: String,
    findings: Option<Vec<PartialRow>>,
    conflicts: Option<Vec<PartialRow>>,
    changes: Option<Vec<PartialRow>>,
    commits: Option<Vec<String>>,
    rows: Option<Vec<PartialRow>>,
    patches: Option<Vec<PartialRow>>,
    snapshot: Option<Vec<PartialRow>>,
    release: Option<ReleaseExpect>,
    report: Option<PartialRow>,
}

#[derive(Deserialize, Default)]
#[serde(deny_unknown_fields, default)]
struct ReleaseExpect {
    commit: String,
    version: Option<i64>,
}

#[derive(Deserialize, Default)]
#[serde(deny_unknown_fields, default)]
struct RefExpect {
    version: Option<i64>,
    sealed: Option<bool>,
    name: Option<String>,
    #[serde(deserialize_with = "present")]
    parent: Option<Value>,
    #[serde(deserialize_with = "present")]
    base: Option<Value>,
    #[serde(deserialize_with = "present")]
    head: Option<Value>,
}

#[derive(Deserialize, Default)]
#[serde(deny_unknown_fields, default)]
struct CommitExpect {
    #[serde(rename = "ref")]
    ref_: Option<String>,
    #[serde(deserialize_with = "present")]
    parent: Option<Value>,
    message: Option<String>,
    #[serde(deserialize_with = "present")]
    sequence: Option<Value>,
    #[serde(rename = "schemaEpoch")]
    schema_epoch: Option<i64>,
    #[serde(rename = "contentHash")]
    content_hash: String,
    #[serde(rename = "contentHashOf")]
    content_hash_of: String,
}

impl Step {
    /// Whether the step runs on the backend: a step runs on every backend
    /// unless it lists the ones it runs on.
    fn runs_on(&self, backend: &str) -> bool {
        self.backends
            .as_ref()
            .is_none_or(|backends| backends.iter().any(|name| name == backend))
    }
}

/// Reads an sql step's statement as an object of one statement per
/// backend, and refuses any other form, a plain string included. A null
/// statement is none.
fn statements<'de, D: Deserializer<'de>>(
    deserializer: D,
) -> Result<Option<BTreeMap<String, String>>, D::Error> {
    let value = Value::deserialize(deserializer)?;
    let refused = || {
        serde::de::Error::custom(format!(
            "a statement is an object of one statement per backend, not {value}"
        ))
    };
    match &value {
        Value::Null => Ok(None),
        Value::Object(members) => members
            .iter()
            .map(|(name, text)| match text {
                Value::String(text) => Ok((name.clone(), text.clone())),
                _ => Err(refused()),
            })
            .collect::<Result<_, _>>()
            .map(Some),
        _ => Err(refused()),
    }
}

/// A member given as `null` is `Some(Value::Null)`; only a missing one is
/// `None`.
fn present<'de, D: Deserializer<'de>>(deserializer: D) -> Result<Option<Value>, D::Error> {
    Value::deserialize(deserializer).map(Some)
}

/// The holder of the graph's sweep lock between `holdSweepLock` and
/// `releaseSweepLock`: a transaction of another connection.
type Holder = Box<dyn Tx + 'static>;

/// One scenario's database, engine and named results.
struct Runner {
    descriptor: String,
    adapter: Arc<postgres::Adapter>,
    store: Arc<PostgresStorage<TokioPostgres>>,
    engine: Engine,
    refs: HashMap<String, Ref>,
    commits: HashMap<String, Commit>,
    releases: HashMap<String, Release>,
    step: String,
    // A Mutex, so the runner is Sync while a step awaits.
    holder: std::sync::Mutex<Option<Holder>>,
    // Last, so the connections above close before the schema drops.
    schema: support::Schema,
}

/// What a step returned, for its expectations.
#[derive(Default)]
struct Returned {
    ref_: Option<Ref>,
    commit: Option<Commit>,
    commit_aware: bool,
    tree: Option<TreeResult>,
    saved: Option<superschematic_versiongraph_engine::Tree>,
    conflicts: Vec<Conflict>,
    changes: Vec<Change>,
    history: Vec<Commit>,
    rows: Vec<Value>,
    patches: Vec<Value>,
    snapshot: Vec<Value>,
    release: Option<Release>,
    report: Option<SweepReport>,
}

macro_rules! fail {
    ($runner:expr, $($arg:tt)*) => {
        panic!("{}: {}", $runner.step, format!($($arg)*))
    };
}

impl Runner {
    async fn new(dsn: &str) -> Runner {
        let schema = support::Schema::create(dsn, "vg_rust_scenario").await;
        let descriptor = support::descriptor();
        let adapter = support::adapter();
        let store = schema.storage(&adapter).await;
        let engine = Engine::new(
            &descriptor,
            store.clone(),
            Options {
                schema_epoch: support::FIXTURE_SCHEMA_EPOCH,
                snapshot_every: support::FIXTURE_SNAPSHOT_EVERY,
                ..Options::default()
            },
        )
        .expect("the fixture's engine");
        Runner {
            descriptor,
            schema,
            adapter,
            store,
            engine,
            refs: HashMap::new(),
            commits: HashMap::new(),
            releases: HashMap::new(),
            step: String::new(),
            holder: std::sync::Mutex::new(None),
        }
    }

    /// A ref named by an earlier step's `as`, or a literal id written
    /// `id:<uuid>`.
    /// Gives each root the row a root has on Postgres: a recipe whose id is
    /// the root, whose title is the root's name and whose creator is the
    /// default actor, all in one statement.
    async fn seed(&self, roots: &[String]) {
        let mut args = vec![support::hyphenated(support::DEFAULT_ACTOR)];
        let mut values = Vec::new();
        for root in roots {
            args.push(support::hyphenated(root));
            args.push(root.clone());
            values.push(format!(
                "(${}::uuid, ${}, $1::uuid)",
                args.len() - 1,
                args.len()
            ));
        }
        let statement = format!(
            "INSERT INTO recipe (id, title, created_by) VALUES {}",
            values.join(", ")
        );
        let params: Vec<&(dyn tokio_postgres::types::ToSql + Sync)> = args
            .iter()
            .map(|arg| arg as &(dyn tokio_postgres::types::ToSql + Sync))
            .collect();
        let types = vec![tokio_postgres::types::Type::TEXT; args.len()];
        let client = self.store.client().client().await;
        let prepared = client
            .prepare_typed(&statement, &types)
            .await
            .unwrap_or_else(|e| panic!("prepare {statement}: {e}"));
        client
            .execute(&prepared, &params)
            .await
            .unwrap_or_else(|e| panic!("seed the roots {roots:?}: {e}"));
    }

    fn ref_id(&self, name: &str) -> String {
        if let Some(literal) = name.strip_prefix("id:") {
            return literal.to_owned();
        }
        match self.refs.get(name) {
            Some(r) => r.id.clone(),
            None => fail!(self, "no ref is named {name:?}"),
        }
    }

    /// A commit named by an earlier step's `as`, or a literal id written
    /// `id:<uuid>`.
    fn commit_id(&self, name: &str) -> String {
        if let Some(literal) = name.strip_prefix("id:") {
            return literal.to_owned();
        }
        match self.commits.get(name) {
            Some(c) => c.id.clone(),
            None => fail!(self, "no commit is named {name:?}"),
        }
    }

    /// The step's version, else the named ref's current one.
    fn version(&self, st: &Step, name: &str) -> i64 {
        if let Some(version) = st.version {
            return version;
        }
        match self.refs.get(name) {
            Some(r) => r.version,
            None => fail!(self, "no ref is named {name:?}"),
        }
    }

    /// Records a ref's new state under every name bound to it.
    fn track_ref(&mut self, r: &Ref) {
        for known in self.refs.values_mut() {
            if known.id == r.id {
                *known = r.clone();
            }
        }
    }

    /// The scenario's engine, at the step's walk ceiling, schema epoch and
    /// snapshot interval when it names them.
    fn engine_for(&self, st: &Step) -> Engine {
        let mut engine = self.engine.clone();
        if st.schema_epoch.is_some() || st.snapshot_every != 0 {
            engine = Engine::new(
                &self.descriptor,
                self.store.clone(),
                Options {
                    schema_epoch: st.schema_epoch.unwrap_or(support::FIXTURE_SCHEMA_EPOCH),
                    snapshot_every: if st.snapshot_every != 0 {
                        st.snapshot_every
                    } else {
                        support::FIXTURE_SNAPSHOT_EVERY
                    },
                    ..Options::default()
                },
            )
            .expect("the step's engine");
        }
        if st.walk_ceiling != 0 {
            engine = engine.with_walk_ceiling(st.walk_ceiling);
        }
        engine
    }

    fn commit_name(&self, id: &str) -> String {
        self.commits
            .iter()
            .find(|(_, c)| c.id == id)
            .map_or_else(|| format!("id:{id}"), |(name, _)| name.clone())
    }

    fn ref_name(&self, id: &str) -> String {
        self.refs
            .iter()
            .find(|(_, r)| r.id == id)
            .map_or_else(|| format!("id:{id}"), |(name, _)| name.clone())
    }

    async fn run(&mut self, st: &Step) {
        let engine = self.engine_for(st);
        let actor = st
            .actor
            .clone()
            .unwrap_or_else(|| support::DEFAULT_ACTOR.to_owned());
        let mut out = Returned::default();
        let result = match st.op.as_str() {
            "holdSweepLock" | "releaseSweepLock" | "sql" => self.control(st, &mut out).await,
            "released" | "snapshot" | "materialize" | "compose" | "diff" | "history" | "rows"
            | "patches" => self.read(st, &engine, &mut out).await,
            _ => self.write(st, &engine, &actor, &mut out).await,
        };

        let x = &st.expect;
        if !x.error.is_empty() {
            match result {
                Ok(()) => fail!(self, "succeeded, want error {}", x.error),
                Err(error) if error.code() != Some(x.error.as_str()) => {
                    fail!(self, "error {:?} ({error}), want {}", error.code(), x.error)
                }
                Err(_) => return,
            }
        }
        if let Err(error) = result {
            fail!(self, "{error}");
        }
        if let Some(r) = &out.ref_ {
            self.track_ref(r);
        }
        if let Some(commit) = &out.commit {
            if !st.as_.is_empty() && st.op != "createPrimary" && st.op != "branch" {
                self.commits.insert(st.as_.clone(), commit.clone());
            }
        }
        self.check(st, &out);
    }

    /// Runs a step that writes through the engine.
    async fn write(
        &mut self,
        st: &Step,
        engine: &Engine,
        actor: &str,
        out: &mut Returned,
    ) -> Result<(), Error> {
        let actor = actor.to_owned();
        match st.op.as_str() {
            "createPrimary" | "branch" => {
                let created = if st.op == "createPrimary" {
                    engine.create_primary(&actor, &st.root, &st.name).await
                } else {
                    engine
                        .branch(&actor, &self.ref_id(&st.from), &st.name)
                        .await
                };
                created.map(|created| {
                    if !st.as_.is_empty() {
                        self.refs.insert(st.as_.clone(), created.clone());
                    }
                    out.ref_ = Some(created);
                })
            }
            "save" => {
                let edits: Edits = st
                    .edits
                    .iter()
                    .map(|(kind, e)| {
                        let edits = KindEdits {
                            upsert: e.upsert.clone(),
                            delete: e.delete.clone(),
                            unset: e.unset.clone(),
                        };
                        (kind.clone(), edits)
                    })
                    .collect();
                engine
                    .save(
                        &actor,
                        &self.ref_id(&st.ref_),
                        self.version(st, &st.ref_),
                        &edits,
                    )
                    .await
                    .map(|saved| {
                        out.ref_ = Some(saved.ref_);
                        out.saved = Some(saved.saved);
                    })
            }
            "commit" | "seal" | "revert" => {
                let (ref_id, version) = (self.ref_id(&st.ref_), self.version(st, &st.ref_));
                let result = match st.op.as_str() {
                    "commit" => {
                        let options = CommitOptions {
                            message: st.message.clone(),
                            tag: st.tag,
                        };
                        engine.commit(&actor, &ref_id, version, &options).await
                    }
                    "seal" => engine.seal(&actor, &ref_id, version).await,
                    _ => {
                        let to = self.commit_id(&st.to_commit);
                        engine.revert(&actor, &ref_id, version, &to).await
                    }
                };
                result.map(|r| {
                    out.ref_ = Some(r.ref_);
                    out.commit = r.commit;
                    out.commit_aware = true;
                })
            }
            "merge" | "rebase" => {
                let result = if st.op == "merge" {
                    let options = CommitOptions {
                        message: st.message.clone(),
                        tag: st.tag,
                    };
                    let (source, target) = (self.ref_id(&st.source), self.ref_id(&st.target));
                    let version = self.version(st, &st.target);
                    engine
                        .merge(&actor, &source, &target, version, &st.resolutions, &options)
                        .await
                } else {
                    let (draft, version) = (self.ref_id(&st.ref_), self.version(st, &st.ref_));
                    engine
                        .rebase(&actor, &draft, version, &st.resolutions)
                        .await
                };
                result.map(|r| {
                    out.ref_ = Some(r.ref_);
                    out.commit = r.commit;
                    out.conflicts = r.conflicts;
                    out.commit_aware = true;
                })
            }
            "release" => {
                let version = st
                    .version
                    .unwrap_or_else(|| self.releases.get(&st.root).map_or(0, |r| r.version));
                let commit = self.commit_id(&st.commit);
                engine
                    .release(&actor, &st.root, &commit, version)
                    .await
                    .map(|released| {
                        self.releases.insert(st.root.clone(), released.clone());
                        out.release = Some(released);
                    })
            }
            "sweep" => {
                let mut options = SweepOptions {
                    actor: actor.clone(),
                    ..SweepOptions::default()
                };
                if let Some(o) = &st.sweep {
                    options.discard_grace = Duration::from_secs(o.discard_grace_seconds);
                    options.abandon_after = Duration::from_secs(o.abandon_after_seconds);
                    options.prune_batch = o.prune_batch;
                }
                engine
                    .sweep(&options)
                    .await
                    .map(|report| out.report = Some(report))
            }
            "discard" => {
                let (ref_id, version) = (self.ref_id(&st.ref_), self.version(st, &st.ref_));
                engine.discard(&actor, &ref_id, version).await
            }
            op => fail!(self, "unknown op {op:?}"),
        }
    }

    /// Runs a step that reads through the engine or the adapter.
    async fn read(&self, st: &Step, engine: &Engine, out: &mut Returned) -> Result<(), Error> {
        match st.op.as_str() {
            "released" => engine.released(&st.root).await.map(|released| {
                out.release = Some(released.release);
                out.tree = Some(released.tree);
            }),
            "snapshot" => {
                let commit = self.commit_id(&st.commit);
                self.in_tx(|tx| Box::pin(async move { tx.snapshot(&commit).await }))
                    .await
                    .map(|mut entries| {
                        entries.sort_by(|a, b| (&a.kind, &a.entity_key).cmp(&(&b.kind, &b.entity_key)));
                        out.snapshot = entries
                            .into_iter()
                            .map(|e| json!({"kind": e.kind, "entityKey": e.entity_key, "entityVersion": e.entity_version}))
                            .collect();
                    })
            }
            "materialize" => engine
                .materialize(&self.commit_id(&st.commit))
                .await
                .map(|tree| out.tree = Some(tree)),
            "compose" => engine
                .compose(&self.ref_id(&st.ref_))
                .await
                .map(|tree| out.tree = Some(tree)),
            "diff" => engine
                .diff(&self.commit_id(&st.from), &self.commit_id(&st.to))
                .await
                .map(|changes| out.changes = changes),
            "history" => engine
                .history(&self.ref_id(&st.ref_))
                .await
                .map(|history| out.history = history),
            "rows" => {
                let (kind, ref_id) = (st.kind.clone(), self.ref_id(&st.ref_));
                self.in_tx(|tx| Box::pin(async move { tx.rows(&kind, &ref_id).await }))
                    .await
                    .map(|mut rows| {
                        rows.sort_by_key(|row| row["entity_key"].to_string());
                        out.rows = rows;
                    })
            }
            "patches" => {
                let commit = self.commit_id(&st.commit);
                self.in_tx(|tx| Box::pin(async move { tx.patches(&[commit]).await }))
                    .await
                    .map(|mut patches| {
                        patches.sort_by(|a, b| (&a.kind, &a.entity_key).cmp(&(&b.kind, &b.entity_key)));
                        out.patches = patches
                            .into_iter()
                            .map(|p| {
                                json!({"kind": p.kind, "entityKey": p.entity_key, "operation": p.operation, "entityVersion": p.entity_version})
                            })
                            .collect();
                    })
            }
            op => fail!(self, "unknown op {op:?}"),
        }
    }

    /// Runs a step that takes or releases the sweep lock, or runs SQL. An
    /// `sql` step that expects rows returns the statement's rows, in the
    /// order it returns them, each column read as text.
    async fn control(&mut self, st: &Step, out: &mut Returned) -> Result<(), Error> {
        match st.op.as_str() {
            "holdSweepLock" => {
                self.hold_sweep_lock().await;
                Ok(())
            }
            "releaseSweepLock" => match self.take_holder() {
                Some(holder) => holder.rollback().await,
                None => fail!(self, "no sweep lock is held"),
            },
            "sql" => {
                let args: Vec<String> = st.args.iter().map(|arg| self.sql_arg(arg)).collect();
                let params: Vec<&(dyn tokio_postgres::types::ToSql + Sync)> = args
                    .iter()
                    .map(|arg| arg as &(dyn tokio_postgres::types::ToSql + Sync))
                    .collect();
                let types = vec![tokio_postgres::types::Type::TEXT; args.len()];
                let text = st
                    .statement
                    .as_ref()
                    .and_then(|statements| statements.get(BACKEND))
                    .unwrap_or_else(|| fail!(self, "the sql step has no {BACKEND} statement"));
                let client = self.store.client().client().await;
                let statement = client
                    .prepare_typed(text, &types)
                    .await
                    .unwrap_or_else(|e| fail!(self, "prepare {text}: {e}"));
                if st.expect.rows.is_none() {
                    return client
                        .execute(&statement, &params)
                        .await
                        .map(|_| ())
                        .map_err(Error::storage);
                }
                let rows = client
                    .query(&statement, &params)
                    .await
                    .map_err(Error::storage)?;
                out.rows = rows
                    .iter()
                    .map(|row| {
                        let columns = row.columns().iter().enumerate().map(|(i, column)| {
                            let text: Option<String> = row.try_get(i).unwrap_or_else(|e| {
                                fail!(
                                    self,
                                    "read {} as text (cast it in the statement): {e}",
                                    column.name()
                                )
                            });
                            (
                                column.name().to_owned(),
                                text.map_or(Value::Null, Value::String),
                            )
                        });
                        Value::Object(columns.collect())
                    })
                    .collect();
                Ok(())
            }
            op => fail!(self, "unknown op {op:?}"),
        }
    }

    fn check(&self, st: &Step, out: &Returned) {
        let x = &st.expect;
        if let Some(want) = &x.ref_ {
            let Some(got) = &out.ref_ else {
                fail!(self, "expects a ref, and {} returns none", st.op);
            };
            self.check_ref(got, want);
        }
        if let Some(want) = &x.commit {
            if !out.commit_aware {
                fail!(self, "expects a commit, and {} returns none", st.op);
            }
            self.check_commit(out.commit.as_ref(), want);
        }
        if let Some(want) = &x.saved {
            let empty = superschematic_versiongraph_engine::Tree::new();
            self.check_tree("saved", out.saved.as_ref().unwrap_or(&empty), want);
        }
        if x.tree.is_some()
            || !x.content_hash.is_empty()
            || !x.content_hash_of.is_empty()
            || x.findings.is_some()
        {
            let Some(tree) = &out.tree else {
                fail!(self, "expects a tree, and {} returns none", st.op);
            };
            if let Some(want) = &x.tree {
                self.check_tree("tree", &tree.tree, want);
            }
            self.check_hash(&tree.content_hash, &x.content_hash, &x.content_hash_of);
            if let Some(want) = &x.findings {
                self.check_list("findings", &to_rows(&tree.findings), want);
            }
        }
        match &x.conflicts {
            Some(want) => self.check_list("conflicts", &to_rows(&out.conflicts), want),
            None if !out.conflicts.is_empty() => fail!(
                self,
                "merge left conflicts {}, and the step expects none",
                serde_json::to_string(&out.conflicts).expect("conflicts")
            ),
            None => {}
        }
        if let Some(want) = &x.changes {
            self.check_list("changes", &to_rows(&out.changes), want);
        }
        if let Some(want) = &x.commits {
            let got: Vec<String> = out
                .history
                .iter()
                .map(|c| self.commit_name(&c.id))
                .collect();
            if got != *want {
                fail!(self, "history {got:?}, want {want:?}");
            }
        }
        if let Some(want) = &x.rows {
            self.check_list("rows", &out.rows, want);
        }
        if let Some(want) = &x.snapshot {
            self.check_list("snapshot", &out.snapshot, want);
        }
        if let Some(want) = &x.release {
            let Some(got) = &out.release else {
                fail!(self, "expects a release, and {} returns none", st.op);
            };
            let name = self.commit_name(&got.commit);
            if name != want.commit {
                fail!(
                    self,
                    "the release names commit {name:?}, want {:?}",
                    want.commit
                );
            }
            if want.version.is_some_and(|v| v != got.version) {
                fail!(
                    self,
                    "release version {}, want {:?}",
                    got.version,
                    want.version
                );
            }
        }
        if let Some(want) = &x.report {
            let Some(report) = &out.report else {
                fail!(self, "expects a report, and {} returns none", st.op);
            };
            let got = serde_json::to_value(report).expect("report");
            self.check_list("report", &[got], std::slice::from_ref(want));
        }
        if let Some(want) = &x.patches {
            self.check_list("patches", &out.patches, want);
        }
    }

    /// Takes the graph's sweep lock through the adapter in a transaction of
    /// another connection, and keeps it open until `releaseSweepLock`.
    async fn hold_sweep_lock(&mut self) {
        if self.holder.lock().expect("holder").is_some() {
            fail!(self, "the sweep lock is already held");
        }
        // The holder's storage lives as long as the test process, so its
        // transaction outlives this step.
        let store: &'static PostgresStorage<TokioPostgres> = Box::leak(Box::new(
            self.adapter
                .storage(TokioPostgres::new(self.schema.connect().await)),
        ));
        let mut holder = store
            .begin()
            .await
            .unwrap_or_else(|e| fail!(self, "begin: {e}"));
        match holder.sweep_lock().await {
            Ok(true) => *self.holder.lock().expect("holder") = Some(holder),
            other => fail!(self, "take the sweep lock: {other:?}"),
        }
    }

    fn take_holder(&self) -> Option<Holder> {
        self.holder.lock().expect("holder").take()
    }

    /// Runs `f` in a transaction of the scenario's storage.
    async fn in_tx<T>(
        &self,
        f: impl for<'t> FnOnce(
            &'t mut (dyn Tx + '_),
        ) -> std::pin::Pin<
            Box<dyn std::future::Future<Output = Result<T, Error>> + Send + 't>,
        >,
    ) -> Result<T, Error> {
        let mut tx = self.store.begin().await?;
        let result = f(&mut *tx).await;
        match result {
            Ok(value) => {
                tx.commit().await?;
                Ok(value)
            }
            Err(error) => {
                let _ = tx.rollback().await;
                Err(error)
            }
        }
    }

    fn sql_arg(&self, arg: &SqlArg) -> String {
        let id = if !arg.uuid.is_empty() {
            arg.uuid.clone()
        } else if !arg.ref_.is_empty() {
            self.ref_id(&arg.ref_)
        } else if !arg.commit.is_empty() {
            self.commit_id(&arg.commit)
        } else {
            fail!(self, "an sql argument names a uuid, a ref or a commit")
        };
        support::hyphenated(&id)
    }

    /// Compares an id with an expectation that names a ref or a commit, or
    /// is null for none.
    fn check_name(
        &self,
        what: &str,
        id: Option<&str>,
        want: Option<&Value>,
        name: impl Fn(&str) -> String,
    ) {
        let Some(want) = want else {
            return;
        };
        match (want, id) {
            (Value::Null, None) => {}
            (Value::Null, Some(id)) => fail!(self, "{what} is {}, want none", name(id)),
            (Value::String(want), Some(id)) if name(id) == *want => {}
            (Value::String(want), id) => {
                fail!(self, "{what} is {:?}, want {want:?}", id.map(&name))
            }
            (other, _) => fail!(self, "{what} expectation {other}"),
        }
    }

    fn check_ref(&self, got: &Ref, want: &RefExpect) {
        if want.version.is_some_and(|v| v != got.version) {
            fail!(self, "ref version {}, want {:?}", got.version, want.version);
        }
        if want.sealed.is_some_and(|s| s != got.sealed) {
            fail!(self, "ref sealed {}, want {:?}", got.sealed, want.sealed);
        }
        if want.name.as_ref().is_some_and(|n| *n != got.name) {
            fail!(self, "ref name {:?}, want {:?}", got.name, want.name);
        }
        self.check_name(
            "the ref's parent",
            got.parent.as_deref(),
            want.parent.as_ref(),
            |id| self.ref_name(id),
        );
        self.check_name(
            "the ref's base",
            got.base.as_deref(),
            want.base.as_ref(),
            |id| self.commit_name(id),
        );
        self.check_name(
            "the ref's head",
            got.head.as_deref(),
            want.head.as_ref(),
            |id| self.commit_name(id),
        );
    }

    fn check_commit(&self, got: Option<&Commit>, want: &Value) {
        if want.is_null() {
            if let Some(got) = got {
                fail!(self, "wrote commit {}, want none", got.id);
            }
            return;
        }
        let Some(got) = got else {
            fail!(self, "wrote no commit, want one");
        };
        let want: CommitExpect = serde_json::from_value(want.clone())
            .unwrap_or_else(|e| fail!(self, "commit expectation: {e}"));
        if want
            .ref_
            .as_ref()
            .is_some_and(|r| *r != self.ref_name(&got.ref_id))
        {
            fail!(
                self,
                "commit ref {:?}, want {:?}",
                self.ref_name(&got.ref_id),
                want.ref_
            );
        }
        self.check_name(
            "the commit's parent",
            got.parent.as_deref(),
            want.parent.as_ref(),
            |id| self.commit_name(id),
        );
        if want.message.as_ref().is_some_and(|m| *m != got.message) {
            fail!(
                self,
                "commit message {:?}, want {:?}",
                got.message,
                want.message
            );
        }
        if let Some(sequence) = &want.sequence {
            let got_sequence = got.sequence.map_or(Value::Null, Value::from);
            if got_sequence != *sequence {
                fail!(self, "commit sequence {got_sequence}, want {sequence}");
            }
        }
        if want.schema_epoch.is_some_and(|e| e != got.schema_epoch) {
            fail!(
                self,
                "commit schema epoch {}, want {:?}",
                got.schema_epoch,
                want.schema_epoch
            );
        }
        self.check_hash(&got.content_hash, &want.content_hash, &want.content_hash_of);
    }

    fn check_hash(&self, got: &str, want: &str, want_of: &str) {
        if !want.is_empty() && got != want {
            fail!(self, "content hash {got}, want {want}");
        }
        if !want_of.is_empty() {
            let Some(commit) = self.commits.get(want_of) else {
                fail!(self, "no commit is named {want_of:?}");
            };
            if got != commit.content_hash {
                fail!(
                    self,
                    "content hash {got}, want {want_of}'s, {}",
                    commit.content_hash
                );
            }
        }
    }

    /// Compares every kind of a tree with the expected rows: the same kinds,
    /// and per kind the same number of rows in the same order, each with the
    /// listed columns' values.
    fn check_tree(
        &self,
        what: &str,
        got: &superschematic_versiongraph_engine::Tree,
        want: &BTreeMap<String, Vec<PartialRow>>,
    ) {
        for (kind, rows) in got {
            if !want.contains_key(kind) {
                fail!(
                    self,
                    "{what} has {kind} rows {}, and the step expects none",
                    Value::Array(rows.clone())
                );
            }
        }
        for (kind, rows) in want {
            let empty = Vec::new();
            self.check_list(
                &format!("{what} {kind}"),
                got.get(kind).unwrap_or(&empty),
                rows,
            );
        }
    }

    /// Compares a list of JSON objects with expected ones: the same length,
    /// and each object with the listed members' values. A listed null also
    /// matches an absent member.
    fn check_list(&self, what: &str, got: &[Value], want: &[PartialRow]) {
        if got.len() != want.len() {
            fail!(
                self,
                "{what}: {}, want {}: {}",
                got.len(),
                want.len(),
                Value::Array(got.to_vec())
            );
        }
        for (i, (got, want)) in got.iter().zip(want).enumerate() {
            for (column, value) in want {
                match got.get(column) {
                    None if value.is_null() => {}
                    Some(have) if have == value => {}
                    have => fail!(
                        self,
                        "{what}[{i}].{column} is {have:?}, want {value} ({got})"
                    ),
                }
            }
        }
    }
}

fn to_rows<T: serde::Serialize>(values: &[T]) -> Vec<Value> {
    values
        .iter()
        .map(|v| serde_json::to_value(v).expect("serialize"))
        .collect()
}

fn scenario_files() -> Vec<std::path::PathBuf> {
    let dir = support::testdata().join("scenarios");
    let mut files: Vec<_> = fs::read_dir(&dir)
        .unwrap_or_else(|e| panic!("read {}: {e}", dir.display()))
        .map(|entry| entry.expect("directory entry").path())
        .filter(|path| path.extension().is_some_and(|ext| ext == "json"))
        .collect();
    files.sort();
    files
}

fn read_scenario(path: &std::path::Path) -> Scenario {
    let text = fs::read_to_string(path).expect("read a scenario");
    let scenario =
        parse_scenario(&text, BACKEND).unwrap_or_else(|e| panic!("{}: {e}", path.display()));
    let stem = path
        .file_stem()
        .and_then(|s| s.to_str())
        .expect("a file name");
    assert_eq!(
        scenario.name,
        stem,
        "{}: the scenario's name is its file's",
        path.display()
    );
    scenario
}

/// Reads a scenario as the format says (runtime/versiongraph/README.md,
/// "Scenarios") for a runner of `backend`. It refuses an unknown member, a
/// scenario with no steps or no roots or one that names a root twice, a
/// backends list that is empty, names a backend twice or names one no
/// runner knows, a statement for an unknown backend, and an sql step that
/// runs on `backend` with no statement for it.
fn parse_scenario(text: &str, backend: &str) -> Result<Scenario, String> {
    let scenario: Scenario = serde_json::from_str(text).map_err(|e| e.to_string())?;
    if scenario.steps.is_empty() {
        return Err("a scenario has steps".to_owned());
    }
    let Some(roots) = &scenario.roots else {
        return Err("a scenario names its roots".to_owned());
    };
    if roots.is_empty() {
        return Err("a scenario names at least one root".to_owned());
    }
    if let Some(root) = repeated(roots) {
        return Err(format!("the scenario names root {root:?} twice"));
    }
    for (i, st) in scenario.steps.iter().enumerate() {
        check_step(st, backend).map_err(|e| format!("step {i} ({}): {e}", st.op))?;
    }
    Ok(scenario)
}

/// Checks a step's backends and statement.
fn check_step(st: &Step, backend: &str) -> Result<(), String> {
    if let Some(backends) = &st.backends {
        if backends.is_empty() {
            return Err("backends lists no backend".to_owned());
        }
        if let Some(name) = backends
            .iter()
            .find(|name| !KNOWN_BACKENDS.contains(&name.as_str()))
        {
            return Err(format!("backends lists unknown backend {name:?}"));
        }
        if let Some(name) = repeated(backends) {
            return Err(format!("backends lists {name:?} twice"));
        }
    }
    if let Some(name) = st
        .statement
        .iter()
        .flatten()
        .map(|(name, _)| name)
        .find(|name| !KNOWN_BACKENDS.contains(&name.as_str()))
    {
        return Err(format!("a statement for unknown backend {name:?}"));
    }
    let has_statement = st
        .statement
        .as_ref()
        .is_some_and(|statements| statements.contains_key(backend));
    if st.op == "sql" && st.runs_on(backend) && !has_statement {
        return Err(format!("the sql step has no {backend} statement"));
    }
    Ok(())
}

/// A name the list holds twice.
fn repeated(names: &[String]) -> Option<&String> {
    names
        .iter()
        .enumerate()
        .find(|(i, name)| names[..*i].contains(name))
        .map(|(_, name)| name)
}

/// Seeds the scenario's roots and runs each step that runs on the runner's
/// backend, in order.
async fn run_scenario(runner: &mut Runner, scenario: &Scenario) {
    runner
        .seed(scenario.roots.as_deref().unwrap_or_default())
        .await;
    for (i, step) in scenario.steps.iter().enumerate() {
        if !step.runs_on(BACKEND) {
            continue;
        }
        runner.step = format!("{} step {i} ({})", scenario.name, step.op);
        runner.run(step).await;
    }
}

/// Every scenario file reads as the format says, whether or not a database
/// is there to run it on.
#[test]
fn scenarios_read() {
    let files = scenario_files();
    assert!(!files.is_empty(), "no scenarios found");
    for path in files {
        read_scenario(&path);
    }
}

/// Every scenario through the engine and the Postgres adapter. Each runs in
/// a task of its own, so one that fails reports its step and the rest still
/// run; each schema is dropped whatever happened.
#[tokio::test(flavor = "multi_thread")]
async fn scenarios() {
    let Some(dsn) = support::database("scenarios") else {
        return;
    };
    let mut failures = Vec::new();
    let files = scenario_files();
    for path in &files {
        let scenario = read_scenario(path);
        let dsn = dsn.clone();
        let name = scenario.name.clone();
        let outcome = tokio::spawn(async move {
            let mut runner = Runner::new(&dsn).await;
            run_scenario(&mut runner, &scenario).await;
            if let Some(holder) = runner.take_holder() {
                let _ = holder.rollback().await;
            }
            scenario.steps.len()
        })
        .await;
        match outcome {
            Ok(steps) => eprintln!("scenario {name}: ok ({steps} steps)"),
            Err(error) => {
                let message = match error.try_into_panic() {
                    Ok(panic) => panic
                        .downcast_ref::<String>()
                        .cloned()
                        .or_else(|| panic.downcast_ref::<&str>().map(|s| (*s).to_owned()))
                        .unwrap_or_else(|| "a panic".to_owned()),
                    Err(error) => error.to_string(),
                };
                eprintln!("scenario {name}: FAILED: {message}");
                failures.push(format!("{name}: {message}"));
            }
        }
    }
    assert!(
        failures.is_empty(),
        "{} of {} scenarios failed:\n{}",
        failures.len(),
        files.len(),
        failures.join("\n")
    );
}

/// A scenario of the given roots (raw JSON, or "" for none) and steps.
fn format_scenario(roots: &str, steps: &[&str]) -> String {
    let member = if roots.is_empty() {
        String::new()
    } else {
        format!(r#""roots": {roots}, "#)
    };
    format!(
        r#"{{"name": "format", "description": "", {member}"steps": [{}]}}"#,
        steps.join(", ")
    )
}

/// Scenarios that each break one rule of the format are refused, with an
/// error that says which, and ones that keep them read.
#[test]
fn scenario_format() {
    const CREATE_PRIMARY: &str = r#"{"op": "createPrimary", "root": "Bread", "name": "main"}"#;
    let unknown_member = format!(
        r#"{{"name": "format", "description": "", "roots": ["Bread"], "backend": "postgres", "steps": [{CREATE_PRIMARY}]}}"#
    );
    // Each case's name, its text, and a part of the error it is refused
    // with ("" when it reads).
    let cases: Vec<(&str, String, &str)> = vec![
        (
            "a statement per backend",
            format_scenario(
                r#"["Bread"]"#,
                &[r#"{"op": "sql", "statement": {"postgres": "SELECT 1", "sqlite": "SELECT 1"}}"#],
            ),
            "",
        ),
        (
            "a plain string statement",
            format_scenario(
                r#"["Bread"]"#,
                &[r#"{"op": "sql", "statement": "SELECT 1"}"#],
            ),
            "a statement is an object of one statement per backend",
        ),
        (
            "a statement that is not text",
            format_scenario(
                r#"["Bread"]"#,
                &[r#"{"op": "sql", "statement": {"postgres": 1}}"#],
            ),
            "a statement is an object of one statement per backend",
        ),
        (
            "an sql step without the runner's statement",
            format_scenario(
                r#"["Bread"]"#,
                &[r#"{"op": "sql", "statement": {"sqlite": "SELECT 1"}}"#],
            ),
            "the sql step has no postgres statement",
        ),
        (
            "an sql step with no statement",
            format_scenario(r#"["Bread"]"#, &[r#"{"op": "sql"}"#]),
            "the sql step has no postgres statement",
        ),
        (
            "a statement for an unknown backend",
            format_scenario(
                r#"["Bread"]"#,
                &[r#"{"op": "sql", "statement": {"postgres": "SELECT 1", "mysql": "SELECT 1"}}"#],
            ),
            r#"a statement for unknown backend "mysql""#,
        ),
        (
            "an sql step for another backend, without the runner's statement",
            format_scenario(
                r#"["Bread"]"#,
                &[r#"{"op": "sql", "backends": ["sqlite"], "statement": {"sqlite": "SELECT 1"}}"#],
            ),
            "",
        ),
        (
            "backends listing the runner's",
            format_scenario(
                r#"["Bread"]"#,
                &[
                    r#"{"op": "createPrimary", "root": "Bread", "name": "main", "backends": ["sqlite", "postgres"]}"#,
                ],
            ),
            "",
        ),
        (
            "backends listing an unknown backend",
            format_scenario(
                r#"["Bread"]"#,
                &[
                    r#"{"op": "createPrimary", "root": "Bread", "name": "main", "backends": ["postgres", "mysql"]}"#,
                ],
            ),
            r#"backends lists unknown backend "mysql""#,
        ),
        (
            "an empty backends",
            format_scenario(
                r#"["Bread"]"#,
                &[r#"{"op": "createPrimary", "root": "Bread", "name": "main", "backends": []}"#],
            ),
            "backends lists no backend",
        ),
        (
            "backends listing a backend twice",
            format_scenario(
                r#"["Bread"]"#,
                &[
                    r#"{"op": "createPrimary", "root": "Bread", "name": "main", "backends": ["postgres", "postgres"]}"#,
                ],
            ),
            r#"backends lists "postgres" twice"#,
        ),
        (
            "no roots",
            format_scenario("", &[CREATE_PRIMARY]),
            "a scenario names its roots",
        ),
        (
            "null roots",
            format_scenario("null", &[CREATE_PRIMARY]),
            "a scenario names its roots",
        ),
        (
            "empty roots",
            format_scenario("[]", &[CREATE_PRIMARY]),
            "a scenario names at least one root",
        ),
        (
            "a root named twice",
            format_scenario(r#"["Bread", "Soup", "Bread"]"#, &[CREATE_PRIMARY]),
            r#"the scenario names root "Bread" twice"#,
        ),
        (
            "no steps",
            format_scenario(r#"["Bread"]"#, &[]),
            "a scenario has steps",
        ),
        (
            "an unknown scenario member",
            unknown_member,
            "unknown field `backend`",
        ),
        (
            "an unknown step member",
            format_scenario(
                r#"["Bread"]"#,
                &[
                    r#"{"op": "createPrimary", "root": "Bread", "name": "main", "backend": "postgres"}"#,
                ],
            ),
            "unknown field `backend`",
        ),
    ];
    for (name, text, refused) in cases {
        match parse_scenario(&text, BACKEND) {
            Ok(_) if refused.is_empty() => {}
            Ok(_) => panic!("{name}: read, want it refused with {refused:?}"),
            Err(error) if refused.is_empty() => panic!("{name}: refused: {error}"),
            Err(error) => assert!(
                error.contains(refused),
                "{name}: refused with {error:?}, want {refused:?}"
            ),
        }
    }
}

/// A scenario whose steps list their backends: a save listed for SQLite
/// alone is skipped and leaves no row, and a save listed for Postgres too
/// writes its row.
#[tokio::test(flavor = "multi_thread")]
async fn scenario_backends() {
    let Some(dsn) = support::database("scenario_backends") else {
        return;
    };
    let scenario = parse_scenario(
        &format_scenario(
            r#"["Bread"]"#,
            &[
                r#"{"op": "createPrimary", "root": "Bread", "name": "main", "as": "main"}"#,
                r#"{"op": "branch", "from": "main", "name": "mix", "as": "mix"}"#,
                r#"{"op": "save", "ref": "mix", "backends": ["sqlite"], "edits": {"step": {"upsert": [{"entity_key": "Mix", "position": 1, "instruction": "Mix", "timings": {}}]}}}"#,
                r#"{"op": "rows", "ref": "mix", "kind": "step", "expect": {"rows": []}}"#,
                r#"{"op": "save", "ref": "mix", "backends": ["sqlite", "postgres"], "edits": {"step": {"upsert": [{"entity_key": "Rest", "position": 2, "instruction": "Rest", "timings": {}}]}}}"#,
                r#"{"op": "rows", "ref": "mix", "kind": "step", "expect": {"rows": [{"entity_key": "Rest"}]}}"#,
            ],
        ),
        BACKEND,
    )
    .expect("the scenario reads");
    let mut runner = Runner::new(&dsn).await;
    run_scenario(&mut runner, &scenario).await;
}

/// A scenario of two roots: each has the recipe row the seeding sql steps
/// gave it (its id, its name as the title and the default actor as its
/// creator) and no other root has one, so a primary line of a root the
/// scenario does not name fails on the foreign key from
/// recipe_ref.root_id.
#[tokio::test(flavor = "multi_thread")]
async fn scenario_roots() {
    let Some(dsn) = support::database("scenario_roots") else {
        return;
    };
    let scenario = parse_scenario(
        &format_scenario(
            r#"["Soup", "Pie"]"#,
            &[
                r#"{"op": "sql", "statement": {"postgres": "SELECT title, CASE id WHEN $1::uuid THEN 'Soup' WHEN $2::uuid THEN 'Pie' ELSE id::text END AS id, CASE created_by WHEN $3::uuid THEN 'Cook' ELSE created_by::text END AS created_by FROM recipe ORDER BY title"},
                  "args": [{"uuid": "Soup"}, {"uuid": "Pie"}, {"uuid": "Cook"}],
                  "expect": {"rows": [{"title": "Pie", "id": "Pie", "created_by": "Cook"}, {"title": "Soup", "id": "Soup", "created_by": "Cook"}]}}"#,
                r#"{"op": "createPrimary", "root": "Soup", "name": "main"}"#,
                r#"{"op": "createPrimary", "root": "Pie", "name": "main"}"#,
            ],
        ),
        BACKEND,
    )
    .expect("the scenario reads");
    let mut runner = Runner::new(&dsn).await;
    run_scenario(&mut runner, &scenario).await;
    match runner
        .engine
        .create_primary(support::DEFAULT_ACTOR, "Bread", "main")
        .await
    {
        Err(error) if error.to_string().contains("SQLSTATE 23503") => {}
        other => panic!(
            "a primary line of a root the scenario does not name: {:?}, want a foreign key violation",
            other.map(|r| r.id)
        ),
    }
}
