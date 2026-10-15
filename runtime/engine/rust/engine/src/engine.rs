//! Opening an engine, the part of `src/engine.ts` built so far (D16, D58):
//! its storage, brought up to the engine's migrations, the namespaces the
//! deployment configures, the access policy, the meta-schema schema
//! documents load against, the value store, and the ids and the clock.
//!
//! What the TypeScript engine took as functions is configuration here
//! (D58), so a Go or Rust host passes the same options as data: the policy
//! is rules ([`crate::PolicyConfig`]), the value store's driver one a
//! deployment's engine crate compiles in ([`crate::ValueDriver`]), the ids
//! random UUIDs or a sequence ([`Ids`]), the clock the system's or a manual
//! one a test moves ([`Clock`], [`EngineClock::advance`]).
//!
//! The API mirrors the TypeScript `Engine` so the Node-API facade can keep
//! its synchronous, principal-first signatures: `engine.schemas().define(
//! &principal, document, options)` is `engine.schemas.define(principal,
//! document, options)`. An [`Engine`] is `Send + Sync`; a call runs on the
//! thread that makes it.

use std::collections::VecDeque;
use std::panic::{catch_unwind, AssertUnwindSafe};
use std::sync::atomic::{AtomicBool, AtomicI64, AtomicU64, Ordering};
use std::sync::{Arc, Mutex, OnceLock, PoisonError, Weak};
use std::time::{SystemTime, UNIX_EPOCH};

use serde_json::Value;

use superschematic_engine_storage::{engine_migrations, Db, Dialect, Lock, Sql, Tx};
use superschematic_schema_ir::schemafile::SchemaFileLoader;

use crate::access::{Access, DottedPermissions, PermissionMatcher, Policy};
use crate::behaviors::{BehaviorSet, Behaviors};
use crate::errors::{Error, Result};
use crate::events::Events;
use crate::indexes::{clash_message, own_indexes, sync_own_indexes};
use crate::instances::Instances;
use crate::namespaces::{NamespaceConfig, NamespaceOptions, Namespaces};
use crate::registry::document::model_of;
use crate::registry::{Catalog, Schemas};
use crate::retention::RetentionOptions;
use crate::runner::{Runner, RunnerCore, RunnerOptions};
use crate::tools::options::resolve_tool_options;
use crate::tools::{ToolOptions, Tools, ToolsCore};
use crate::values::{ValueOptions, ValueStore, Values};

/// Where the engine keeps its data.
#[derive(Debug)]
#[non_exhaustive]
pub enum Storage {
    /// A storage the host opened (`superschematic-engine-storage`), which
    /// the engine migrates.
    Db(Db),
    /// A SQLite file, created if absent. One process writes it.
    #[cfg(feature = "sqlite")]
    Sqlite {
        path: std::path::PathBuf,
        options: superschematic_engine_storage::SqliteOptions,
    },
    /// A schema of a Postgres database (`engine` unless configured), which
    /// every instance of a server shares.
    #[cfg(feature = "postgres")]
    Postgres {
        url: String,
        options: superschematic_engine_storage::PostgresOptions,
    },
}

/// How the engine makes the id of an instance created without one.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub enum Ids {
    /// Random version 4 UUIDs, lowercase and hyphenated, as the TypeScript
    /// engine's `randomUUID` makes them.
    #[default]
    Uuid4,
    /// `<prefix>1`, `<prefix>2`, ...: one count per engine, for tests and
    /// recorded scenarios, which need ids they can name in advance. Two
    /// processes on one database count apart, so a deployment uses UUIDs.
    /// An id is checked as a caller's is when a create takes it.
    Sequence { prefix: String },
    /// The version 4 UUIDs the scenario recorder draws (milestone 1's
    /// `seededUUID`): the n-th, from 1, is the first 16 bytes of
    /// SHA-256(`<prefix><n>`), with the version nibble set to 4 and the
    /// variant bits to `10`, hyphenated in lowercase ([`seeded_uuid`]).
    /// Engines given one counter draw one sequence, as the recorder's one
    /// generator serves every engine a test opens. The recorder's prefix is
    /// [`SCENARIO_SEED`]. For replay, and tests that need ids they can
    /// compute.
    Seeded { prefix: String, drawn: IdCounter },
    /// The ids a host queues before a call ([`IdQueue::push`]), drawn in
    /// order: a replay of a recording whose test gave the engine ids of
    /// its own. Drawing from an empty queue is refused.
    Queue(IdQueue),
}

/// The prefix of the scenario recorder's seed: its n-th id is
/// [`seeded_uuid`]`(SCENARIO_SEED, n)`.
pub const SCENARIO_SEED: &str = "superschematic-engine-scenario/";

/// The n-th seeded id: the version 4 UUID made of the first 16 bytes of
/// SHA-256(`<prefix><n>`), its version and variant bits set.
pub fn seeded_uuid(prefix: &str, n: u64) -> String {
    uuid::Uuid::from_bytes(seeded_bytes(prefix, n)).to_string()
}

// seeded_bytes is the n-th seeded id's 16 bytes.
fn seeded_bytes(prefix: &str, n: u64) -> [u8; 16] {
    use sha2::{Digest, Sha256};
    let digest = Sha256::digest(format!("{prefix}{n}").as_bytes());
    let mut bytes = [0u8; 16];
    bytes.copy_from_slice(&digest[..16]);
    bytes[6] = (bytes[6] & 0x0f) | 0x40;
    bytes[8] = (bytes[8] & 0x3f) | 0x80;
    bytes
}

/// Where the version 4 UUIDs a behavior makes of its own come from (the
/// refs, commits and rows of Branches' version graph): the engine's seeded
/// generator when it has one ([`Ids::Seeded`]), so a replay draws them in
/// the sequence the scenario recorder's one generator gave the TypeScript
/// engine's version graph, and random ones otherwise (D58). A behavior
/// reads it from its context (`uuids()`); it is cheap to clone and lives on
/// its own.
#[derive(Clone, Debug, Default)]
pub struct UuidSource {
    seeded: Option<(String, IdCounter)>,
}

impl UuidSource {
    /// Random version 4 UUIDs.
    pub fn random() -> Self {
        Self::default()
    }

    /// The next UUID's 16 bytes, its version and variant set.
    pub fn next_bytes(&self) -> [u8; 16] {
        match &self.seeded {
            Some((prefix, drawn)) => seeded_bytes(prefix, drawn.next()),
            None => uuid::Uuid::new_v4().into_bytes(),
        }
    }
}

/// A count of the seeded ids drawn, which engines given one share.
#[derive(Clone, Debug, Default)]
pub struct IdCounter(Arc<AtomicU64>);

impl IdCounter {
    /// A counter at 0: the first id drawn is the first.
    pub fn new() -> Self {
        Self::default()
    }

    /// How many ids have been drawn.
    pub fn drawn(&self) -> u64 {
        self.0.load(Ordering::SeqCst)
    }

    /// Draws the next number, from 1: what a host that shares the counter
    /// with an engine (a replay's request ids) takes its own ids from.
    pub fn next(&self) -> u64 {
        self.0.fetch_add(1, Ordering::SeqCst) + 1
    }
}

impl PartialEq for IdCounter {
    fn eq(&self, other: &Self) -> bool {
        Arc::ptr_eq(&self.0, &other.0)
    }
}

impl Eq for IdCounter {}

/// Ids a host queues for the engine to draw, in order.
#[derive(Clone, Debug, Default)]
pub struct IdQueue(Arc<Mutex<VecDeque<String>>>);

impl IdQueue {
    pub fn new() -> Self {
        Self::default()
    }

    /// Queues an id; the engine draws the first queued first.
    pub fn push(&self, id: impl Into<String>) {
        self.ids().push_back(id.into());
    }

    /// How many ids wait.
    pub fn len(&self) -> usize {
        self.ids().len()
    }

    /// Whether none waits.
    pub fn is_empty(&self) -> bool {
        self.ids().is_empty()
    }

    /// Drops every id that waits.
    pub fn clear(&self) {
        self.ids().clear();
    }

    fn pop(&self) -> Option<String> {
        self.ids().pop_front()
    }

    fn ids(&self) -> std::sync::MutexGuard<'_, VecDeque<String>> {
        self.0.lock().unwrap_or_else(PoisonError::into_inner)
    }
}

impl PartialEq for IdQueue {
    fn eq(&self, other: &Self) -> bool {
        Arc::ptr_eq(&self.0, &other.0)
    }
}

impl Eq for IdQueue {}

/// What time it is to the engine, in epoch milliseconds.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub enum Clock {
    /// The system's clock.
    #[default]
    System,
    /// A clock that stands at the given time until a test moves it
    /// ([`EngineClock::advance`], [`EngineClock::set`],
    /// [`EngineClock::script`]).
    Manual(i64),
    /// The system's clock, but never the same millisecond twice: each
    /// reading is one past the last when the system's has not moved past
    /// it, so two readings are two instants. The scenario recorder reads
    /// the wall clock so, and its replay does.
    Strict,
}

/// The meta-schema schema documents load against: the `superschematic
/// json-schema` output of the deployment's binary, parsed or as JSON text.
#[derive(Clone, Debug, PartialEq)]
pub enum MetaSchema {
    Document(Value),
    Text(String),
}

/// How an engine opens.
pub struct EngineOptions {
    pub storage: Storage,
    /// Who may read, write, define and publish (D58: rules a deployment
    /// configures, or a Rust policy of its own). There is no default: an
    /// engine opened without one is refused, as the TypeScript engine
    /// refuses it; `Policy::allow_all()` is for tests and local use.
    pub policy: Option<Policy>,
    /// Decides whether a principal holds a permission a rule requires, or
    /// one a behavior's `can()` asks about; the HTTP runtime's dotted-path
    /// `hasAnyPermission` when absent.
    pub permission_matcher: Option<Arc<dyn PermissionMatcher>>,
    /// The configured namespaces besides `default`, and the shared one. A
    /// create makes more while the engine runs.
    pub namespaces: NamespaceOptions,
    pub ids: Ids,
    pub clock: Clock,
    /// The core registry's meta-schema when absent.
    pub meta_schema: Option<MetaSchema>,
    /// The value store: its threshold, its driver, its cache and the
    /// longest value it keeps (D58: the driver is config).
    pub values: ValueOptions,
    /// The behavior implementations this engine runs, registered when it
    /// opens in order (D58: a value the project's build assembles);
    /// `engine.behaviors().register` adds more later. A schema that
    /// composes a behavior without one is refused.
    pub behaviors: BehaviorSet,
    /// The runner of behaviors' reactions and schedules: the principal
    /// they act as and its limits. Without it the runner refuses to run.
    pub runner: Option<RunnerOptions>,
    /// How long the event log keeps events, pruned on the runner. Without
    /// it the log keeps every event.
    pub retention: Option<RetentionOptions>,
    /// The invocation policy and the vendor keys the describe and tools
    /// documents and the MCP tools are written with: the deployment's
    /// binary's registrations, the core's by default (D11, D58: config).
    pub tools: ToolOptions,
}

impl std::fmt::Debug for EngineOptions {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("EngineOptions")
            .field("storage", &self.storage)
            .field("policy", &self.policy)
            .field("namespaces", &self.namespaces)
            .field("ids", &self.ids)
            .field("clock", &self.clock)
            .field("values", &self.values)
            .field("behaviors", &self.behaviors)
            .field("runner", &self.runner)
            .field("retention", &self.retention)
            .field("tools", &self.tools)
            .finish_non_exhaustive()
    }
}

impl EngineOptions {
    /// Options with every default: no policy yet, one namespace, random
    /// ids, the system clock, the core's meta-schema.
    pub fn new(storage: Storage) -> Self {
        EngineOptions {
            storage,
            policy: None,
            permission_matcher: None,
            namespaces: NamespaceOptions::default(),
            ids: Ids::default(),
            clock: Clock::default(),
            meta_schema: None,
            values: ValueOptions::default(),
            behaviors: BehaviorSet::default(),
            runner: None,
            retention: None,
            tools: ToolOptions::default(),
        }
    }

    /// The defaults on a SQLite file.
    #[cfg(feature = "sqlite")]
    pub fn sqlite(path: impl Into<std::path::PathBuf>) -> Self {
        Self::new(Storage::Sqlite {
            path: path.into(),
            options: superschematic_engine_storage::SqliteOptions::default(),
        })
    }

    /// The defaults on a Postgres database.
    #[cfg(feature = "postgres")]
    pub fn postgres(
        url: impl Into<String>,
        options: superschematic_engine_storage::PostgresOptions,
    ) -> Self {
        Self::new(Storage::Postgres {
            url: url.into(),
            options,
        })
    }
}

/// The engine's clock: the system's, or a manual one a test moves.
#[derive(Debug)]
pub struct EngineClock {
    manual: Option<AtomicI64>,
    /// The times a scripted manual clock reads next, in order.
    script: Mutex<VecDeque<i64>>,
    /// For a strict clock, its last reading.
    strict: Option<AtomicI64>,
}

impl EngineClock {
    fn new(clock: Clock) -> Self {
        EngineClock {
            manual: match clock {
                Clock::Manual(at) => Some(AtomicI64::new(at)),
                Clock::System | Clock::Strict => None,
            },
            script: Mutex::new(VecDeque::new()),
            strict: (clock == Clock::Strict).then(|| AtomicI64::new(0)),
        }
    }

    /// The time, in epoch milliseconds.
    pub fn now(&self) -> i64 {
        if let Some(at) = &self.manual {
            let next = self
                .script
                .lock()
                .unwrap_or_else(PoisonError::into_inner)
                .pop_front();
            if let Some(next) = next {
                at.store(next, Ordering::SeqCst);
            }
            return at.load(Ordering::SeqCst);
        }
        let system = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .map_or(0, |since| {
                i64::try_from(since.as_millis()).unwrap_or(i64::MAX)
            });
        match &self.strict {
            None => system,
            Some(last) => {
                let mut seen = last.load(Ordering::SeqCst);
                loop {
                    let next = system.max(seen + 1);
                    match last.compare_exchange(seen, next, Ordering::SeqCst, Ordering::SeqCst) {
                        Ok(_) => return next,
                        Err(moved) => seen = moved,
                    }
                }
            }
        }
    }

    /// Has a manual clock read these times next, in order, then stand at
    /// the last: a recorded step's readings, replayed. A set or an advance
    /// drops what is left.
    pub fn script(&self, times: impl IntoIterator<Item = i64>) -> Result<()> {
        self.manual_clock()?;
        *self.script.lock().unwrap_or_else(PoisonError::into_inner) = times.into_iter().collect();
        Ok(())
    }

    fn drop_script(&self) {
        self.script
            .lock()
            .unwrap_or_else(PoisonError::into_inner)
            .clear();
    }

    /// Whether a test moves the clock.
    pub fn is_manual(&self) -> bool {
        self.manual.is_some()
    }

    /// Moves a manual clock on by `ms` and returns the new time. The
    /// system's clock cannot be moved.
    pub fn advance(&self, ms: i64) -> Result<i64> {
        let at = self.manual_clock()?;
        self.drop_script();
        Ok(at.fetch_add(ms, Ordering::SeqCst) + ms)
    }

    /// Sets a manual clock to `at`.
    pub fn set(&self, at: i64) -> Result<()> {
        self.manual_clock()?.store(at, Ordering::SeqCst);
        self.drop_script();
        Ok(())
    }

    fn manual_clock(&self) -> Result<&AtomicI64> {
        self.manual.as_ref().ok_or_else(|| {
            Error::usage(
                "the engine runs on the system clock; open it with Clock::Manual to move it",
            )
        })
    }
}

// The ids a create without one takes.
#[derive(Debug)]
struct IdSource {
    ids: Ids,
    next: AtomicU64,
}

/// The engine: its storage, namespaces, access policy, schemas, instances,
/// event log, value store, behaviors and runner.
pub struct Engine {
    pub(crate) db: Db,
    pub(crate) namespaces: NamespaceConfig,
    pub(crate) access: Access,
    /// Answers a behavior's `can()` and a rule's required permission.
    pub(crate) matcher: Arc<dyn PermissionMatcher>,
    pub(crate) clock: EngineClock,
    pub(crate) catalog: Catalog,
    pub(crate) values: Arc<ValueStore>,
    ids: IdSource,
    /// Set when a panic escaped the engine's own code while a transaction
    /// was held: the engine refuses every later call, so its host fails
    /// its readiness (D58).
    poisoned: AtomicBool,
    /// Set by [`Engine::shutdown`]: every later call is refused, and every
    /// event stream ends.
    closed: AtomicBool,
    /// The runner's options, state, driver and lease.
    pub(crate) runner: RunnerCore,
    /// The tools' options and what each published version gives them.
    pub(crate) tools: ToolsCore,
    /// The engine itself, once [`Engine::shared`] put it in an `Arc`: what
    /// the runner's driver thread and an event stream hold.
    this: OnceLock<Weak<Engine>>,
    /// The event streams the JSON calls hold open, by id.
    pub(crate) streams: crate::api::stream::OpenStreams,
}

impl std::fmt::Debug for Engine {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Engine")
            .field("db", &self.db)
            .field("namespaces", &self.namespaces)
            .finish_non_exhaustive()
    }
}

impl Engine {
    /// Opens the engine's storage, creating it if absent, and applies the
    /// engine's migrations. A bad option is refused before the storage
    /// opens.
    pub fn open(options: EngineOptions) -> Result<Engine> {
        let Some(policy) = &options.policy else {
            return Err(Error::usage(
                "an engine needs an access policy (policy); pass allowAll for tests and local use",
            ));
        };
        let matcher: Arc<dyn PermissionMatcher> = options
            .permission_matcher
            .clone()
            .unwrap_or_else(|| Arc::new(DottedPermissions));
        let access = Access::of(policy, &matcher);
        let namespaces = NamespaceConfig::new(&options.namespaces)?;
        let loader = match &options.meta_schema {
            None => SchemaFileLoader::new(),
            Some(MetaSchema::Document(document)) => SchemaFileLoader::with_meta_schema(document)
                .map_err(|error| Error::usage(error.to_string()))?,
            Some(MetaSchema::Text(text)) => SchemaFileLoader::with_meta_schema_text(text)
                .map_err(|error| Error::usage(error.to_string()))?,
        };
        let clock = EngineClock::new(options.clock);
        let values = Arc::new(ValueStore::new(&options.values)?);
        // Checked before the storage opens, so a bad option leaves nothing
        // open.
        let runner = RunnerCore::new(options.runner.as_ref(), options.retention.as_ref())?;
        let tools = resolve_tool_options(&options.tools)?;
        let invocation = crate::behaviors::registry::InvocationPolicy {
            key: tools.invocation_policy.key.clone(),
            values: tools.invocation_policy.values.clone(),
        };
        let db = open_storage(options.storage)?;
        let migrating = Arc::clone(&values);
        db.migrate(
            &engine_migrations(move |tx| unique_indexes(tx, &migrating)),
            clock.now(),
        )?;
        values.settle(&db);
        let engine = Engine {
            db,
            namespaces,
            access,
            matcher,
            clock,
            catalog: Catalog::new(loader, Arc::clone(&values), invocation),
            values,
            ids: IdSource {
                ids: options.ids,
                next: AtomicU64::new(0),
            },
            poisoned: AtomicBool::new(false),
            closed: AtomicBool::new(false),
            runner,
            tools: ToolsCore::new(tools),
            this: OnceLock::new(),
            streams: crate::api::stream::OpenStreams::default(),
        };
        engine.behaviors().register_set(options.behaviors)?;
        Ok(engine)
    }

    /// The engine in an `Arc`, as a host keeps it: what the runner's
    /// driver thread ([`crate::Runner::start`]) and an event stream
    /// ([`crate::api`]) need, since they outlive the call that starts them.
    pub fn shared(self) -> Arc<Engine> {
        let shared = Arc::new(self);
        let _ = shared.this.set(Arc::downgrade(&shared));
        shared
    }

    /// The engine opened and shared ([`Engine::shared`]).
    pub fn open_shared(options: EngineOptions) -> Result<Arc<Engine>> {
        Ok(Engine::open(options)?.shared())
    }

    /// The shared engine, when [`Engine::shared`] made it one.
    pub fn handle(&self) -> Option<Arc<Engine>> {
        self.weak()?.upgrade()
    }

    pub(crate) fn weak(&self) -> Option<Weak<Engine>> {
        self.this.get().cloned()
    }

    /// The runner of reactions and schedules, and of retention.
    pub fn runner(&self) -> Runner<'_> {
        Runner { engine: self }
    }

    /// The describe and tools documents, the tool calls and the behaviors
    /// the engine runs.
    pub fn tools(&self) -> Tools<'_> {
        Tools { engine: self }
    }

    /// The search across the schemas a namespace reaches that compose
    /// Search (`engine.search`, [`crate::search`]): every one the principal
    /// may read, each ranked by its own search as the principal, the
    /// rankings fused by reciprocal rank. It runs on the core's Search
    /// behavior, which the engine reaches by name; an engine that does not
    /// run it answers `unavailable`.
    pub fn search(
        &self,
        principal: &crate::principal::Principal,
        params: &Value,
        namespace: Option<&str>,
    ) -> Result<Value> {
        crate::principal::check_principal(principal)?;
        let namespace = self.namespaces().resolve(namespace)?;
        crate::search::search_schemas(self, principal, params, &namespace)
    }

    /// Stops the runner and ends every event stream, as the TypeScript
    /// engine's `close` does; the storage closes once nothing holds the
    /// engine. Every later call by name ([`crate::calls`]) and request
    /// ([`crate::api::handle`]: 503 `unavailable`) is refused, and so is
    /// every instance read and write. A host calls it on SIGTERM.
    pub fn shutdown(&self) {
        self.runner.close(self.db.notifier());
        self.closed.store(true, Ordering::SeqCst);
        self.db.notifier().bump();
    }

    /// Whether [`Engine::shutdown`] closed the engine.
    pub fn is_closed(&self) -> bool {
        self.closed.load(Ordering::SeqCst)
    }

    /// The behaviors the engine runs: registered, and registered later.
    pub fn behaviors(&self) -> Behaviors<'_> {
        Behaviors { engine: self }
    }

    /// Whether a panic escaped the engine's code while it held a
    /// transaction: every later call is refused.
    pub fn poisoned(&self) -> bool {
        self.poisoned.load(Ordering::SeqCst)
    }

    /// The schema registry.
    pub fn schemas(&self) -> Schemas<'_> {
        Schemas { engine: self }
    }

    /// The namespaces: resolved by every call, and created, archived and
    /// listed through the access policy's `manage`.
    pub fn namespaces(&self) -> Namespaces<'_> {
        Namespaces { engine: self }
    }

    /// The instance store.
    pub fn instances(&self) -> Instances<'_> {
        Instances { engine: self }
    }

    /// The event log.
    pub fn events(&self) -> Events<'_> {
        Events { engine: self }
    }

    /// The value store: a value by its hash, and the sweep.
    pub fn values(&self) -> Values<'_> {
        Values { engine: self }
    }

    /// Runs `f` in a write transaction that excludes the writers `lock`
    /// names, then settles what a value driver's writes left (a value a
    /// rolled-back write wrote, one a committed write let go): every write
    /// of the engine's goes through it.
    pub(crate) fn write<T>(
        &self,
        lock: Lock<'_>,
        f: impl FnOnce(&Tx<'_>) -> Result<T>,
    ) -> Result<T> {
        self.usable()?;
        let result = self.guarded(|| self.db.write(lock, f));
        self.values.settle(&self.db);
        result
    }

    /// Runs `f` in a read-only transaction: the reads that run behaviors'
    /// field readers go through it.
    pub(crate) fn read<T>(&self, f: impl FnOnce(&Tx<'_>) -> Result<T>) -> Result<T> {
        self.usable()?;
        self.guarded(|| self.db.read(f))
    }

    fn usable(&self) -> Result<()> {
        if self.is_closed() {
            return Err(Error::internal("the engine is closed"));
        }
        if self.poisoned() {
            return Err(Error::internal(
                "the engine panicked inside a transaction and refuses every call; restart it",
            ));
        }
        Ok(())
    }

    // guarded runs a transaction, and poisons the engine when a panic
    // escapes it: a hook's own panic is caught where the engine calls it,
    // so one that reaches here is the engine's.
    fn guarded<T>(&self, run: impl FnOnce() -> Result<T>) -> Result<T> {
        match catch_unwind(AssertUnwindSafe(run)) {
            Ok(result) => result,
            Err(payload) => {
                self.poisoned.store(true, Ordering::SeqCst);
                let message = payload
                    .downcast_ref::<&str>()
                    .map(|text| (*text).to_owned())
                    .or_else(|| payload.downcast_ref::<String>().cloned())
                    .unwrap_or_else(|| "a panic".to_owned());
                Err(Error::internal(format!(
                    "the engine panicked inside a transaction: {message}"
                )))
            }
        }
    }

    /// The engine's storage.
    pub fn db(&self) -> &Db {
        &self.db
    }

    /// Which database the engine runs on.
    pub fn dialect(&self) -> Dialect {
        self.db.dialect()
    }

    /// The engine's clock.
    pub fn clock(&self) -> &EngineClock {
        &self.clock
    }

    /// Where a behavior's own version 4 UUIDs come from: the engine's
    /// seeded generator, or random ones.
    pub fn uuids(&self) -> UuidSource {
        match &self.ids.ids {
            Ids::Seeded { prefix, drawn } => UuidSource {
                seeded: Some((prefix.clone(), drawn.clone())),
            },
            Ids::Uuid4 | Ids::Sequence { .. } | Ids::Queue(_) => UuidSource::random(),
        }
    }

    /// The id of an instance created without one.
    pub fn new_id(&self) -> Result<String> {
        Ok(match &self.ids.ids {
            Ids::Uuid4 => uuid::Uuid::new_v4().to_string(),
            Ids::Sequence { prefix } => {
                let next = self.ids.next.fetch_add(1, Ordering::SeqCst) + 1;
                format!("{prefix}{next}")
            }
            Ids::Seeded { prefix, drawn } => seeded_uuid(prefix, drawn.next()),
            Ids::Queue(queue) => queue.pop().ok_or_else(|| {
                Error::usage(
                    "the engine's id queue is empty: queue the id a create draws (IdQueue::push)",
                )
            })?,
        })
    }

    /// Closes the engine: its runner stops, and its storage closes once
    /// nothing else holds it.
    pub fn close(self) {
        self.shutdown();
    }
}

impl Drop for Engine {
    fn drop(&mut self) {
        // The driver holds the engine only during a pass; a driver that
        // dropped the last hold of it ends on its own thread.
        self.runner.close(self.db.notifier());
    }
}

fn open_storage(storage: Storage) -> Result<Db> {
    match storage {
        Storage::Db(db) => Ok(db),
        #[cfg(feature = "sqlite")]
        Storage::Sqlite { path, options } => Ok(Db::open_sqlite(path, options)?),
        #[cfg(feature = "postgres")]
        Storage::Postgres { url, options } => Ok(Db::open_postgres(&url, options)?),
    }
}

/// Engine migration 10, unique fields and indexes. The engine ignored
/// @unique, @key and @index before. Each live version gets the indexes
/// publish now creates, with the values under their keys put back inline
/// first. A unique index the stored instances break stops the storage from
/// opening: an operator makes the values distinct, or drops the field's
/// @unique in a new version, with the engine before this one. A field the
/// engine refuses @unique on now stays unenforced in the version that has
/// it, as the define of its next version refuses it.
fn unique_indexes(tx: &Tx<'_>, values: &ValueStore) -> superschematic_engine_storage::Result<()> {
    let live = tx.all(
        Sql::per(
            "SELECT namespace, name, document FROM engine_schemas AS s
           WHERE version = (SELECT MAX(version) FROM engine_schemas AS t WHERE t.namespace = s.namespace AND t.name = s.name) AND version > 0
           ORDER BY namespace, name",
            "SELECT namespace, name, document FROM engine_schemas AS s
           WHERE version = (SELECT MAX(version) FROM engine_schemas AS t WHERE t.namespace = s.namespace AND t.name = s.name) AND version > 0
           ORDER BY namespace COLLATE \"C\", name COLLATE \"C\"",
        ),
        &[],
    )?;
    for row in live {
        let holder = row.text("namespace")?;
        let model = model_of(row.text("document")?)?;
        let clashes = sync_own_indexes(
            tx,
            values,
            holder,
            &model.name,
            &[],
            &own_indexes(&model, holder),
        )?;
        if !clashes.is_empty() {
            let listed: Vec<String> = clashes
                .iter()
                .map(|clash| clash_message(clash, &clash.namespace, &model.instance_type))
                .collect();
            return Err(superschematic_engine_storage::Error::Migration {
                message: format!(
                    "engine migration 10: schema {} in namespace {holder} has unique fields its stored instances break: {}. Make the values distinct, or publish a version without the unique field, with the engine before this one",
                    model.name,
                    listed.join("; ")
                ),
            });
        }
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn an_engine_is_shared_across_threads() {
        fn shared<T: Send + Sync>() {}
        shared::<Engine>();
    }

    #[test]
    fn a_manual_clock_moves_and_the_systems_does_not() {
        let manual = EngineClock::new(Clock::Manual(1_000));
        assert_eq!(manual.now(), 1_000);
        assert_eq!(manual.advance(500).unwrap(), 1_500);
        manual.set(10).unwrap();
        assert_eq!(manual.now(), 10);
        let system = EngineClock::new(Clock::System);
        assert!(system.now() > 1_700_000_000_000);
        assert!(system.advance(1).is_err());
        assert!(system.script([1]).is_err());
    }

    #[test]
    fn a_scripted_clock_reads_its_times_then_stands_at_the_last() {
        let clock = EngineClock::new(Clock::Manual(100));
        clock.script([101, 102, 103]).unwrap();
        assert_eq!(
            (clock.now(), clock.now(), clock.now(), clock.now()),
            (101, 102, 103, 103)
        );
        clock.script([200, 201]).unwrap();
        clock.set(5).unwrap();
        assert_eq!(clock.now(), 5, "a set drops the script");
    }

    #[test]
    fn a_strict_clock_never_reads_one_millisecond_twice() {
        let clock = EngineClock::new(Clock::Strict);
        let readings: Vec<i64> = (0..1000).map(|_| clock.now()).collect();
        assert!(readings.windows(2).all(|pair| pair[0] < pair[1]));
    }

    #[test]
    fn seeded_ids_are_the_recorders() {
        // The recorder's seededUUID (milestone 1, test/recorder.ts) drew
        // these, run under Node.js; the corpus writes them as {{id:N}}.
        for (n, id) in [
            (1, "81af85d2-eaea-4aa9-9131-9573fd47ad9a"),
            (2, "adb623a0-36b0-44f2-8f7d-79ba3d38cb0c"),
            (3, "0eefa6f1-884c-4bf1-9193-b9fe654aae6d"),
            (100, "09b4dd39-1e1c-4b2a-a3c2-6fcb3b4d8a15"),
        ] {
            assert_eq!(seeded_uuid(SCENARIO_SEED, n), id);
        }
    }
}
