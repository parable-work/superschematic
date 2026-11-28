//! Development mode's store over SQLite (D117): the development layout of
//! `superschematic-rules-handler` in a SQLite file, or in a database in
//! memory that goes with its process, and a host that serves the handler's
//! requests over it in one process.
//!
//! The layout is fixed, so a publish writes rows and never a table: the
//! store creates its tables when it opens, and none after.
//!
//! | Table | What it holds |
//! |-------|---------------|
//! | `dev_schema_versions` | each type's published versions: the type (`schema`), the number, the schema document |
//! | `dev_schema_drafts` | each type's version defined and not published yet |
//! | `dev_instances` | the rows: the type (`schema`), the id, the version, the own fields as one JSON column (`data`) and the owned fields as another (`owned`) |
//! | `dev_sub_<behavior>` | one shared sub-table per behavior that declares sub-tables kept per row (`dev_sub_timers`), keyed by type, id, use, table and key, each row's values a JSON column |
//! | `dev_shared` | the rows of the uses' shared tables, kept for a type's table as a whole (D103's step 4: Numbering's `counts`), keyed by type, use, table and key, each row's values a JSON column and its version |
//! | `dev_deadlines` | the armed deadlines: the row, the use, the name, the time armed and when due |
//! | `dev_events` | the event queue (D104), in the order written |
//! | `dev_changes` | the change queue (D103): each heard write, run after the commit |
//!
//! Each request runs in one transaction begun IMMEDIATE, so it holds the
//! database's one writer from its start: every lock a behavior asks for is
//! held from the request's start, and a request reads what the one before
//! it committed. A unit applies in a savepoint, whole or not at all.
//!
//! [`Dev::http`] serves the handler's routes over HTTP with no transport
//! under them, and with the `server` feature [`server`] is their axum
//! binding, the event stream and the MCP endpoint among them, which the
//! `superschematic-dev` binary serves.
//!
//! With the `postgres` feature, `PgStore` keeps the same tables in a schema
//! of a Postgres database. Its request's transaction takes the schema's
//! advisory lock first, so one request at a time writes the store, as on
//! SQLite.

use std::path::Path;
use std::time::{SystemTime, UNIX_EPOCH};

use rusqlite::types::{ToSqlOutput, ValueRef};
use rusqlite::{params_from_iter, Connection, ToSql};
use serde_json::Value;
use superschematic_rules::{Caller, Code, Core, Refusal, SharedRow, SubRow};
use superschematic_rules_handler::{
    run_due, Declarations, DueDeadline, Handler, Policy, QueuedChange, QueuedEvent, RanDue,
    Request, Store, StoreAnswer, StoreRead, Unit,
};

use layout::{Arg, Cell, Cells, Db, Layout};

mod http;
pub mod hub;
mod layout;
#[cfg(feature = "postgres")]
mod pg;
#[cfg(feature = "server")]
pub mod server;

pub use http::Served;
#[cfg(feature = "postgres")]
pub use pg::PgStore;

/// The version of the layout this crate writes, kept in `dev_meta`.
pub const LAYOUT_VERSION: &str = "1";

/// The development layout in a SQLite database.
pub struct SqliteStore {
    conn: Connection,
    layout: Layout,
}

fn failed(error: rusqlite::Error) -> Refusal {
    if let rusqlite::Error::SqliteFailure(failure, _) = &error {
        if failure.code == rusqlite::ErrorCode::DatabaseBusy {
            return Refusal::new(
                Code::Busy,
                format!("the development store is busy with another writer: {error}"),
            );
        }
    }
    Refusal::internal(format!("the development store failed: {error}"))
}

/// A statement of the layout in SQLite's words: its `$n` placeholders as
/// `?n`, which SQLite numbers alike. No statement of the layout holds a `$`
/// but in a placeholder; every value is bound.
fn sqlite_sql(sql: &str) -> String {
    sql.replace('$', "?")
}

impl ToSql for Arg<'_> {
    fn to_sql(&self) -> rusqlite::Result<ToSqlOutput<'_>> {
        Ok(match self {
            Arg::Text(text) => ToSqlOutput::Borrowed(ValueRef::Text(text.as_bytes())),
            Arg::Owned(text) => ToSqlOutput::Borrowed(ValueRef::Text(text.as_bytes())),
            Arg::Int(n) => ToSqlOutput::Owned(rusqlite::types::Value::Integer(*n)),
        })
    }
}

/// The connection a store's statements run over.
struct SqliteDb<'a>(&'a Connection);

impl Db for SqliteDb<'_> {
    fn execute(&mut self, sql: &str, args: &[Arg<'_>]) -> Result<u64, Refusal> {
        let changed = self
            .0
            .execute(&sqlite_sql(sql), params_from_iter(args))
            .map_err(failed)?;
        Ok(u64::try_from(changed).unwrap_or(u64::MAX))
    }

    fn query(&mut self, sql: &str, args: &[Arg<'_>]) -> Result<Vec<Cells>, Refusal> {
        let mut statement = self.0.prepare_cached(&sqlite_sql(sql)).map_err(failed)?;
        let width = statement.column_count();
        let mut rows = statement.query(params_from_iter(args)).map_err(failed)?;
        let mut out = Vec::new();
        while let Some(row) = rows.next().map_err(failed)? {
            let mut cells = Vec::with_capacity(width);
            for at in 0..width {
                cells.push(match row.get_ref(at).map_err(failed)? {
                    ValueRef::Null => Cell::Null,
                    ValueRef::Integer(n) => Cell::Int(n),
                    ValueRef::Text(bytes) => Cell::Text(
                        std::str::from_utf8(bytes)
                            .map_err(|error| {
                                Refusal::internal(format!(
                                    "the development store holds text that is no UTF-8: {error}"
                                ))
                            })?
                            .to_owned(),
                    ),
                    ValueRef::Real(_) | ValueRef::Blob(_) => {
                        return Err(Refusal::internal(format!(
                            "the development store holds neither text nor a whole number in column {at}"
                        )))
                    }
                });
            }
            out.push(Cells(cells));
        }
        Ok(out)
    }

    fn batch(&mut self, sql: &str) -> Result<(), Refusal> {
        self.0.execute_batch(sql).map_err(failed)
    }
}

impl SqliteStore {
    /// The store in a SQLite file, its tables created when they are not
    /// there, for the behaviors `declarations` declares.
    pub fn open(path: impl AsRef<Path>, declarations: &Declarations) -> rusqlite::Result<Self> {
        Self::over(Connection::open(path)?, declarations)
    }

    /// The store in a SQLite database in memory, which goes with it.
    pub fn memory(declarations: &Declarations) -> rusqlite::Result<Self> {
        Self::over(Connection::open_in_memory()?, declarations)
    }

    /// The store over a connection, its tables created when they are not
    /// there.
    pub fn over(conn: Connection, declarations: &Declarations) -> rusqlite::Result<Self> {
        conn.busy_timeout(std::time::Duration::from_secs(5))?;
        let layout = Layout::sqlite(declarations);
        conn.execute_batch(&layout.create_tables())?;
        conn.execute(&sqlite_sql(&layout.record_version()), [LAYOUT_VERSION])?;
        for table in layout.create_sub_tables() {
            conn.execute_batch(&table)?;
        }
        Ok(SqliteStore { conn, layout })
    }

    /// The connection, for a test's or an operator's own SQL.
    pub fn connection(&self) -> &Connection {
        &self.conn
    }

    /// A row's rows of one use's sub-table, in key order.
    pub fn sub_rows(
        &self,
        type_name: &str,
        id: &str,
        use_id: &str,
        table: &str,
    ) -> Result<Vec<SubRow>, Refusal> {
        self.layout
            .sub_rows(&mut SqliteDb(&self.conn), type_name, id, use_id, table)
    }

    /// The row of a key of a use's shared table, with its version.
    pub fn shared_row(
        &self,
        type_name: &str,
        use_id: &str,
        table: &str,
        key: &str,
    ) -> Result<Option<SharedRow>, Refusal> {
        self.layout
            .shared_row(&mut SqliteDb(&self.conn), type_name, use_id, table, key)
    }

    /// A row's armed deadlines: each its use, name, time and payload, by
    /// use and name.
    pub fn armed(
        &self,
        type_name: &str,
        id: &str,
    ) -> Result<Vec<(String, String, i64, Value)>, Refusal> {
        self.layout.armed(&mut SqliteDb(&self.conn), type_name, id)
    }
}

impl Store for SqliteStore {
    fn due(&mut self, now: i64) -> Result<Vec<DueDeadline>, Refusal> {
        self.layout.due(&mut SqliteDb(&self.conn), now)
    }

    fn queued(&mut self) -> Result<Vec<QueuedChange>, Refusal> {
        self.layout.queued(&mut SqliteDb(&self.conn))
    }

    fn events(&mut self, after: i64) -> Result<Vec<QueuedEvent>, Refusal> {
        self.layout.events(&mut SqliteDb(&self.conn), after)
    }

    fn begin(&mut self) -> Result<(), Refusal> {
        self.conn.execute_batch("BEGIN IMMEDIATE").map_err(failed)
    }

    fn commit(&mut self) -> Result<(), Refusal> {
        self.conn.execute_batch("COMMIT").map_err(failed)
    }

    fn rollback(&mut self) -> Result<(), Refusal> {
        if self.conn.is_autocommit() {
            return Ok(());
        }
        self.conn.execute_batch("ROLLBACK").map_err(failed)
    }

    fn read(&mut self, read: &StoreRead) -> Result<StoreAnswer, Refusal> {
        self.layout.read(&mut SqliteDb(&self.conn), read)
    }

    fn apply(&mut self, unit: &Unit) -> Result<bool, Refusal> {
        self.layout.apply(&mut SqliteDb(&self.conn), unit)
    }
}

/// When the host runs its queues: after each request, as a worker that
/// claims a message the moment it is written would, or only when asked.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Default)]
pub enum Drain {
    #[default]
    AfterEachRequest,
    WhenAsked,
}

/// The development host: the handler over a store in one process (SQLite
/// by default, or a Postgres schema with the `postgres` feature), with the
/// core, the behaviors' declarations, a clock, and the caller the runner
/// runs the queues as.
pub struct Dev<S = SqliteStore> {
    core: Core,
    declarations: Declarations,
    store: S,
    clock: Box<dyn Fn() -> i64 + Send>,
    runner: Caller,
    drain: Drain,
    policy: Policy,
    /// The streams open with signals (D77's primitive 2), which each
    /// request's committed signals reach.
    hub: hub::SharedHub,
}

/// The system's time in epoch milliseconds.
fn system_millis() -> i64 {
    match SystemTime::now().duration_since(UNIX_EPOCH) {
        Ok(since) => i64::try_from(since.as_millis()).unwrap_or(i64::MAX),
        Err(before) => -i64::try_from(before.duration().as_millis()).unwrap_or(i64::MAX),
    }
}

impl Dev<SqliteStore> {
    /// The host over a SQLite file, running the behaviors ported onto the
    /// core.
    pub fn open(path: impl AsRef<Path>) -> rusqlite::Result<Self> {
        let declarations = Declarations::ported();
        let store = SqliteStore::open(path, &declarations)?;
        Ok(Self::over(
            superschematic_rules_behaviors::core(),
            declarations,
            store,
        ))
    }

    /// The host over a SQLite database in memory, which goes with it.
    pub fn memory() -> rusqlite::Result<Self> {
        let declarations = Declarations::ported();
        let store = SqliteStore::memory(&declarations)?;
        Ok(Self::over(
            superschematic_rules_behaviors::core(),
            declarations,
            store,
        ))
    }
}

#[cfg(feature = "postgres")]
impl Dev<PgStore> {
    /// The host over a schema of the Postgres database a URL names,
    /// running the behaviors ported onto the core.
    pub fn postgres(url: &str, schema: &str) -> Result<Self, Refusal> {
        let declarations = Declarations::ported();
        let store = PgStore::open(url, schema, &declarations)?;
        Ok(Self::over(
            superschematic_rules_behaviors::core(),
            declarations,
            store,
        ))
    }
}

impl<S: Store> Dev<S> {
    /// The host over a core, the declarations of its behaviors and a store
    /// opened with them.
    pub fn over(core: Core, declarations: Declarations, store: S) -> Self {
        Dev {
            core,
            declarations,
            store,
            clock: Box::new(system_millis),
            runner: Caller::new("development-runner", Vec::<String>::new()),
            drain: Drain::default(),
            policy: Policy::default(),
            hub: hub::SharedHub::default(),
        }
    }

    /// The streams open with signals, which a transport subscribes to
    /// ([`hub::Subscription`]).
    pub fn hub(&self) -> &hub::SharedHub {
        &self.hub
    }

    /// Hands signals a committed request sent to the streams open with
    /// signals that deliver them: whose filters keep them and whose caller
    /// may read their type.
    pub fn publish(&self, signals: &[superschematic_rules_handler::HostSignal]) {
        if signals.is_empty() {
            return;
        }
        let handler = Handler::new(&self.core, &self.declarations).with_policy(&self.policy);
        self.hub
            .lock()
            .unwrap_or_else(std::sync::PoisonError::into_inner)
            .publish(signals, |caller, query, signal| {
                handler.delivers(caller, query, signal)
            });
    }

    /// The host with a clock of its own, in epoch milliseconds.
    pub fn with_clock(mut self, clock: impl Fn() -> i64 + Send + 'static) -> Self {
        self.clock = Box::new(clock);
        self
    }

    /// The host with the caller its runner runs the queues as.
    pub fn with_runner(mut self, runner: Caller) -> Self {
        self.runner = runner;
        self
    }

    /// The host with the access policy its handler asks before each
    /// request (D58's form); `"allowAll"` unless given.
    pub fn with_policy(mut self, policy: Policy) -> Self {
        self.policy = policy;
        self
    }

    /// The access policy the host's handler asks.
    pub fn policy(&self) -> &Policy {
        &self.policy
    }

    /// The host with when it runs its queues.
    pub fn with_drain(mut self, drain: Drain) -> Self {
        self.drain = drain;
        self
    }

    pub fn store(&mut self) -> &mut S {
        &mut self.store
    }

    pub fn core(&self) -> &Core {
        &self.core
    }

    pub fn declarations(&self) -> &Declarations {
        &self.declarations
    }

    /// Serves one request as a caller, then, unless the host drains when
    /// asked, runs the queues.
    pub fn request(&mut self, caller: &Caller, request: &Request) -> Result<Value, Refusal> {
        let request_id = uuid::Uuid::new_v4().to_string();
        self.request_as(caller, request, &request_id)
    }

    /// Runs the change queue and the deadlines due by the host's clock, as
    /// its runner.
    pub fn run_due(&mut self) -> Result<Vec<RanDue>, Refusal> {
        let handler = Handler::new(&self.core, &self.declarations);
        let mut fresh = || {
            (
                uuid::Uuid::new_v4().to_string(),
                uuid::Uuid::new_v4().as_u128(),
            )
        };
        let ran = run_due(
            &handler,
            &mut self.store,
            (self.clock)(),
            &self.runner,
            &mut fresh,
        )?;
        for one in &ran {
            self.publish(&one.signals);
        }
        Ok(ran)
    }
}
