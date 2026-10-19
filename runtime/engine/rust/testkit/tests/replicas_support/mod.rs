//! What the tests of two engines on one database share (D58): replicas,
//! each an engine with a pool of its own, as two instances of a server
//! open it, their connections named so a test finds them among the
//! database's sessions; the database's administrator, who ends a session
//! as a crash, a failover or a proxy would (`pg_terminate_backend`); a call
//! tried again while the storage is busy, as a client of a 503 would; and
//! test.Pause, whose operation runs a test's closure inside the caller's
//! transaction, so a test holds a writer's lock where it chooses.

#![allow(dead_code)]

use std::sync::mpsc::{self, Receiver, RecvTimeoutError, Sender};
use std::sync::{Arc, Mutex, PoisonError};
use std::time::{Duration, Instant};

use serde_json::{json, Value};

use superschematic_engine::storage::{Db, SqlValue};
use superschematic_engine::{
    Behavior, ConfigTarget, EngineErrorCode, EngineEvent, EngineOptions, Error, Hooks, OperationCx,
    Principal, ReadEvents, Result,
};

use crate::support::{unique_name, Target};

/// How long a test waits for what another replica, the listener or the
/// runner does before it fails: long, since a loaded machine is slow, and
/// a pass is quicker.
pub const PATIENCE: Duration = Duration::from_secs(30);

/// `url` with the application name every session it opens carries, so
/// `pg_stat_activity` tells one replica's sessions from another's.
pub fn named_url(url: &str, application: &str) -> String {
    let joiner = if url.contains('?') { '&' } else { '?' };
    format!("{url}{joiner}application_name={application}")
}

/// A name for a replica's sessions that no other test's has.
pub fn replica_name(test: &str) -> String {
    unique_name(test)
}

/// Names a replica's sessions on Postgres and says whether it keeps a
/// listener; nothing on SQLite, where a replica is a connection to the
/// file.
pub fn as_replica(options: &mut EngineOptions, name: &str, listen: bool) {
    #[cfg(feature = "postgres")]
    if let superschematic_engine::Storage::Postgres { url, options } = &mut options.storage {
        *url = named_url(url, name);
        options.listen = listen;
    }
    #[cfg(not(feature = "postgres"))]
    let _ = (options, name, listen);
}

/// Sets how long a replica's writes wait for another writer's lock.
pub fn set_busy_timeout(options: &mut EngineOptions, timeout: Duration) {
    match &mut options.storage {
        #[cfg(feature = "postgres")]
        superschematic_engine::Storage::Postgres { options, .. } => options.busy_timeout = timeout,
        #[cfg(feature = "sqlite")]
        superschematic_engine::Storage::Sqlite { options, .. } => options.busy_timeout = timeout,
        _ => {
            let _ = timeout;
        }
    }
}

/// Runs `call` until it is not refused as `busy`, at most `tries` times,
/// as a client of a 503 would: the answer, and how many times it was
/// busy.
pub fn retrying<T>(tries: usize, mut call: impl FnMut() -> Result<T>) -> (Result<T>, usize) {
    let mut busy = 0;
    loop {
        match call() {
            Err(Error::Engine(refused))
                if refused.code() == EngineErrorCode::Busy && busy + 1 < tries =>
            {
                busy += 1;
                std::thread::sleep(Duration::from_millis(20));
            }
            other => return (other, busy),
        }
    }
}

/// Every event of a namespace from the start, page by page, as `reader`
/// reads them.
pub fn all_events(
    engine: &superschematic_engine::Engine,
    namespace: Option<&str>,
) -> Vec<EngineEvent> {
    let mut out = Vec::new();
    let mut after = 0;
    loop {
        let page = engine
            .events()
            .read(
                &crate::support::alice(),
                &ReadEvents {
                    namespace,
                    after: Some(superschematic_engine::EventsAfter::Cursor(after)),
                    limit: Some(500),
                    ..ReadEvents::default()
                },
            )
            .expect("read the log");
        out.extend(page.events);
        after = page.next;
        if !page.more {
            return out;
        }
    }
}

/// Waits for `done`, failing with `what` past [`PATIENCE`].
pub fn wait_for(what: &str, mut done: impl FnMut() -> bool) {
    let deadline = Instant::now() + PATIENCE;
    while !done() {
        assert!(Instant::now() < deadline, "{what}: still not done");
        std::thread::sleep(Duration::from_millis(10));
    }
}

/// The database's administrator: a storage of its own on the target's
/// schema, outside every replica's pool, that reads the database's
/// sessions and ends them. It holds no writer's lock: it only reads.
pub struct Admin {
    db: Db,
}

impl Admin {
    /// The administrator of the target's database; `None` on SQLite.
    pub fn new(target: &Target) -> Option<Admin> {
        #[cfg(feature = "postgres")]
        {
            let (url, schema) = (target.url()?, target.schema()?);
            let db = Db::open_postgres(
                url,
                superschematic_engine::storage::PostgresOptions {
                    schema: schema.to_owned(),
                    listen: false,
                    pool_max: 1,
                    ..Default::default()
                },
            )
            .expect("the administrator's storage");
            Some(Admin { db })
        }
        #[cfg(not(feature = "postgres"))]
        {
            let _ = target;
            None
        }
    }

    /// The pids, states and last statements of the sessions of the
    /// database a condition on `pg_stat_activity` keeps.
    pub fn sessions(&self, condition: &str, params: &[SqlValue]) -> Vec<(i64, String, String)> {
        self.db
            .read(|tx| {
                tx.all(
                    format!(
                        "SELECT pid, coalesce(state, '') AS state, coalesce(query, '') AS query FROM pg_stat_activity
                         WHERE datname = current_database() AND pid <> pg_backend_pid() AND ({condition})"
                    ),
                    params,
                )
            })
            .expect("read the sessions")
            .iter()
            .map(|row| {
                (
                    row.i64("pid").expect("a pid"),
                    row.text("state").expect("a state").to_owned(),
                    row.text("query").expect("a query").to_owned(),
                )
            })
            .collect()
    }

    /// Ends the sessions a condition keeps, waiting up to five seconds for
    /// each to go, and says how many it ended.
    pub fn terminate(&self, condition: &str, params: &[SqlValue]) -> usize {
        self.db
            .read(|tx| {
                tx.all(
                    format!(
                        "SELECT pg_terminate_backend(pid, 5000) AS ended FROM pg_stat_activity
                         WHERE datname = current_database() AND pid <> pg_backend_pid() AND ({condition})"
                    ),
                    params,
                )
            })
            .expect("end the sessions")
            .len()
    }

    /// Ends every session of a replica.
    pub fn terminate_replica(&self, name: &str) -> usize {
        self.terminate("application_name = ?", &[SqlValue::from(name)])
    }

    /// Ends a replica's listener, the session that LISTENs on the engine's
    /// channel.
    pub fn terminate_listener(&self, name: &str) -> usize {
        self.terminate(
            "application_name = ? AND query LIKE 'LISTEN %'",
            &[SqlValue::from(name)],
        )
    }

    /// Ends a replica's sessions that are inside a transaction: the one a
    /// paused call holds.
    pub fn terminate_in_transaction(&self, name: &str) -> usize {
        self.terminate(
            "application_name = ? AND state LIKE 'idle in transaction%'",
            &[SqlValue::from(name)],
        )
    }

    /// The pid of the session that holds the advisory lock `key` as a
    /// lock of its session (a lease), if one does.
    pub fn lock_holder(&self, key: i64) -> Option<i64> {
        let (high, low) = (key >> 32 & 0xffff_ffff, key & 0xffff_ffff);
        self.db
            .read(|tx| {
                tx.get(
                    "SELECT pid FROM pg_locks WHERE locktype = 'advisory' AND granted AND objsubid = 1
                     AND classid::bigint = ? AND objid::bigint = ?",
                    &[SqlValue::Integer(high), SqlValue::Integer(low)],
                )
            })
            .expect("read the locks")
            .map(|row| row.i64("pid").expect("a pid"))
    }

    /// The application name of the session that holds the advisory lock
    /// `key` as a lock of its session: which replica holds a lease.
    pub fn lock_holder_name(&self, key: i64) -> Option<String> {
        let pid = self.lock_holder(key)?;
        self.db
            .read(|tx| {
                tx.get(
                    "SELECT coalesce(application_name, '') AS name FROM pg_stat_activity WHERE pid = ?",
                    &[SqlValue::Integer(pid)],
                )
            })
            .expect("read the session")
            .map(|row| row.text("name").expect("a name").to_owned())
    }

    /// Whether a replica has a session waiting for a lock (an advisory
    /// lock, a row's).
    pub fn waiting(&self, name: &str) -> bool {
        !self
            .sessions(
                "application_name = ? AND wait_event_type = 'Lock'",
                &[SqlValue::from(name)],
            )
            .is_empty()
    }

    /// Ends the session that holds the advisory lock `key`, as its
    /// process's crash would; whether one did.
    pub fn terminate_lock_holder(&self, key: i64) -> bool {
        match self.lock_holder(key) {
            Some(pid) => self.terminate("pid = ?", &[SqlValue::Integer(pid)]) == 1,
            None => false,
        }
    }

    /// The administrator's storage, for statements of its own.
    pub fn db(&self) -> &Db {
        &self.db
    }
}

/// What a test makes test.Pause's operation do, with the label the call
/// passed.
pub type PauseProbe = Arc<dyn Fn(&OperationCx<'_, ()>, &str) -> Result<()> + Send + Sync>;

/// test.Pause: its operation `pause({ label })` runs the closure its probe
/// holds, inside the call's transaction, so a test can hold a writer's
/// lock (a namespace's, and the catalog's shared) for as long as it likes,
/// write through the call's context first, or fail. With no closure it
/// does nothing.
#[derive(Clone, Default)]
pub struct Pause {
    pub probe: Arc<Mutex<Option<PauseProbe>>>,
}

impl Pause {
    /// Has the operation run `probe`.
    pub fn on_pause(
        &self,
        probe: impl Fn(&OperationCx<'_, ()>, &str) -> Result<()> + Send + Sync + 'static,
    ) {
        *self.probe.lock().unwrap_or_else(PoisonError::into_inner) = Some(Arc::new(probe));
    }

    /// Has the operation hold the call's transaction until the hold lets
    /// go: each call says it is inside (with its label), then waits.
    pub fn hold(&self) -> Hold {
        let (inside, entered) = mpsc::channel::<String>();
        let (release, released) = mpsc::channel::<()>();
        let inside = Mutex::new(inside);
        let released = Mutex::new(released);
        self.on_pause(move |_, label| {
            let _ = inside
                .lock()
                .unwrap_or_else(PoisonError::into_inner)
                .send(label.to_owned());
            match released
                .lock()
                .unwrap_or_else(PoisonError::into_inner)
                .recv_timeout(PATIENCE)
            {
                Ok(()) | Err(RecvTimeoutError::Disconnected) => Ok(()),
                Err(RecvTimeoutError::Timeout) => Err(Error::behavior(
                    "test.Pause",
                    "no one let the paused call go",
                )),
            }
        });
        Hold { entered, release }
    }
}

/// A transaction test.Pause holds: [`Hold::entered`] waits until a call is
/// inside it, [`Hold::release`] lets one go.
pub struct Hold {
    entered: Receiver<String>,
    release: Sender<()>,
}

impl Hold {
    /// Waits until a paused call is inside its transaction, and returns
    /// its label.
    pub fn entered(&self) -> String {
        self.entered
            .recv_timeout(PATIENCE)
            .expect("a paused call came inside its transaction")
    }

    /// Lets one paused call go on to its commit.
    pub fn release(&self) {
        let _ = self.release.send(());
    }
}

static PAUSE_DECLARATION: &str = r#"{
  "name": "test.Pause",
  "description": "Runs a test's closure inside the call's transaction.",
  "operations": [{
    "name": "pause",
    "description": "Runs the closure the test gave.",
    "paramsSchema": {"type": "object", "additionalProperties": false, "properties": {"label": {"type": "string"}}},
    "resultSchema": {"type": "object"},
    "writes": true
  }]
}"#;

impl Behavior for Pause {
    type Config = ();

    fn declaration(&self) -> &str {
        PAUSE_DECLARATION
    }

    fn parse_config(&self, _raw: &Value, _target: &ConfigTarget<'_>) -> Result<()> {
        Ok(())
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            config_change: Some(|_, _, _, _| Ok(None)),
            operations: vec![("pause", |pause, cx, params| {
                let probe = pause
                    .probe
                    .lock()
                    .unwrap_or_else(PoisonError::into_inner)
                    .clone();
                if let Some(probe) = probe {
                    let label = params.get("label").and_then(Value::as_str).unwrap_or("");
                    probe(cx, label)?;
                }
                Ok(json!({}))
            })],
            ..Hooks::default()
        }
    }
}

/// Invokes test.Pause's operation on an instance, as `who`, in a
/// namespace.
pub fn pause(
    engine: &superschematic_engine::Engine,
    who: &Principal,
    namespace: Option<&str>,
    schema: &str,
    id: &str,
    label: &str,
) -> Result<Value> {
    engine.instances().invoke(
        who,
        schema,
        id,
        "pause",
        &json!({ "label": label }),
        superschematic_engine::WriteOptions {
            namespace,
            ..Default::default()
        },
    )
}
