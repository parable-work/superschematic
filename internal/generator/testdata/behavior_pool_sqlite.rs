//! fixture-pool-api over a SQLite file (D104, D116): its behavior worker
//! AccountRechecks as a worker its host drains, with no async runtime under
//! it. A recheck's deadline runs through the core, and the recheck event it
//! emits is handed to the Workers method of the test's own, which reports
//! the account's room with reportRoom through the event's run, in the unit
//! that spends the event.

use std::path::PathBuf;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex};
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use api::behaviors::{account_rechecks_worker, EventRun, RULES_LAYOUT};
use api::runtime::orm;
use api::runtime::orm::sqlite::{rusqlite, Database};
use api::runtime::worker::drain;
use api::{types, BoxError, Workers};
use async_trait::async_trait;
use serde_json::{json, Value};
use superschematic_rules_adapter::{sqlite, Caller, CreateOptions, Host, Layout};

/// The project's handler of Account's recheck events, as the test writes
/// it: it records each event it is handed and reports the account's room
/// through the event's run, or, while `failing`, fails after reporting.
#[derive(Default)]
struct Provider {
    heard: Mutex<Vec<Value>>,
    reports: Mutex<Vec<Value>>,
    failing: AtomicBool,
}

#[async_trait]
impl Workers for Provider {
    async fn account_rechecks_pool_recheck(
        &self,
        event: types::AccountPoolRecheckEvent,
        run: &EventRun<'_>,
    ) -> Result<(), BoxError> {
        let event = serde_json::to_value(&event)?;
        let key = event["key"].as_str().unwrap_or_default().to_owned();
        self.heard.lock().unwrap().push(event);
        let report: Value = run.operation("Account", &key, "Pool", "reportRoom", &json!({"room": true}))?;
        if self.failing.load(Ordering::SeqCst) {
            return Err("the provider is down".into());
        }
        self.reports.lock().unwrap().push(report);
        Ok(())
    }
}

/// A SQLite file of the test's own, holding the tables of the build's
/// SQLite DDL, with the server's handle on it and a connection that stands
/// for another process.
struct File {
    dir: PathBuf,
    path: PathBuf,
    db: Arc<Database>,
    other: rusqlite::Connection,
}

impl File {
    fn create(name: &str) -> File {
        let dir = std::env::temp_dir().join(format!("pool-sqlite-{name}-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&dir);
        std::fs::create_dir_all(&dir).expect("create the directory");
        let path = dir.join("pool.sqlite");
        rusqlite::Connection::open(&path)
            .and_then(|connection| connection.execute_batch(include_str!("../create.sql")))
            .expect("create the tables");
        let db = Arc::new(Database::open(&path).expect("open the file"));
        let other = rusqlite::Connection::open(&path).expect("open the file again");
        other.busy_timeout(Duration::from_secs(5)).expect("a busy timeout");
        File { dir, path, db, other }
    }

    /// Runs `work` with the core and the layout on the file's writing
    /// connection, as a caller holding accounts.pool.
    fn core<T>(
        &self,
        work: impl FnOnce(&Host<'_>, &rusqlite::Connection, Caller) -> Result<T, superschematic_rules_adapter::Error>,
    ) -> T {
        self.db
            .write(|connection| {
                let core = superschematic_rules_behaviors::core();
                let layout = Layout::from_json(RULES_LAYOUT).unwrap();
                let host = Host::new(&core, &layout);
                let caller = Caller::new("tester", ["accounts.pool"]);
                work(&host, connection, caller).map_err(|err| orm::Error::Database(err.to_string()))
            })
            .expect("the command runs")
    }

    /// Creates an account through the core, and answers its key in its
    /// wire form.
    fn create_account(&self, name: &str) -> String {
        let own = json!({
            "owner": "5f0c3a52-8a5e-4c1b-9d1e-2f6f1b7c8d90",
            "name": name,
            "vendor": "anthropic",
            "kind": "account",
            "rank": 1,
        });
        self.core(|host, connection, caller| {
            sqlite::run_create(host, connection, "Account", own.as_object().cloned().unwrap(), caller, &CreateOptions::default())
        })
    }

    /// Puts the account out until `until`, in epoch milliseconds, which
    /// arms its recheck there.
    fn mark_out(&self, key: &str, until: i64) -> Value {
        self.core(|host, connection, caller| {
            sqlite::run_operation(
                host,
                connection,
                "Account",
                key,
                "Pool",
                "markOut",
                json!({"until": until, "reason": "rate limited"}),
                caller,
            )
        })
    }

    fn text(&self, sql: &str) -> Option<String> {
        self.other.query_row(sql, [], |row| row.get(0)).expect(sql)
    }

    fn count(&self, table: &str, condition: &str) -> i64 {
        self.other
            .query_row(&format!("SELECT COUNT(*) FROM {table} WHERE {condition}"), [], |row| row.get(0))
            .expect("a count")
    }
}

impl Drop for File {
    fn drop(&mut self) {
        let _ = std::fs::remove_dir_all(&self.dir);
    }
}

fn millis(time: SystemTime) -> i64 {
    time.duration_since(UNIX_EPOCH).unwrap().as_millis() as i64
}

#[test]
fn a_recheck_event_is_handed_to_the_workers_method_on_sqlite() {
    let file = File::create("event");
    let provider = Arc::new(Provider::default());
    let worker = account_rechecks_worker(provider.clone()).expect("the worker builds from its queues' descriptors");
    assert_eq!(worker.name(), "AccountDeadline, AccountEvent");
    let mut connection = drain::open(&file.path).expect("the worker's own connection");

    // markOut puts the account out for ten minutes and arms its recheck
    // then; a drain now runs nothing.
    let key = file.create_account("a1");
    let now = SystemTime::now();
    let until = millis(now) + 10 * 60 * 1000;
    let out = file.mark_out(&key, until);
    assert_eq!(out["outUntil"], until, "{out}");
    assert_eq!(file.count("account_deadline_queue", "name = 'recheck' AND state = 'ready'"), 1);
    let drained = worker.drain(&mut connection, now, 10).expect("a drain");
    assert_eq!(drained.ran, 0);
    assert!(provider.heard.lock().unwrap().is_empty());

    // A drain given a time past outUntil runs the recheck through the core,
    // whose event is written in the deadline's unit, and hands the event to
    // the Workers method in a unit of its own: its reportRoom opens the
    // account, and commits with the event's spending.
    let later = now + Duration::from_secs(11 * 60);
    let drained = worker.drain(&mut connection, later, 10).expect("a drain");
    assert_eq!(drained.ran, 2, "the recheck, and the event it emitted");
    let heard = provider.heard.lock().unwrap().clone();
    assert_eq!(heard.len(), 1, "{heard:?}");
    assert_eq!(heard[0]["key"], key);
    assert_eq!(heard[0]["payload"]["dueAt"], until, "{heard:?}");
    assert_eq!(heard[0]["payload"]["outReason"], "rate limited", "{heard:?}");
    let reports = provider.reports.lock().unwrap().clone();
    assert_eq!(reports[0]["availability"], "open", "{reports:?}");
    assert_eq!(file.text("SELECT pool_availability FROM account").as_deref(), Some("open"));
    assert_eq!(file.count("account_event_queue", "1"), 0, "the event is spent");
    assert_eq!(file.count("account_deadline_queue", "1"), 0, "a check that opens clears the recheck");

    // A Workers method that fails rolls its unit back, its reportRoom with
    // it, and its event is failed, to come again after its backoff.
    provider.failing.store(true, Ordering::SeqCst);
    let until = millis(later) + 10 * 60 * 1000;
    file.mark_out(&key, until);
    let again = later + Duration::from_secs(11 * 60);
    let drained = worker.drain(&mut connection, again, 10).expect("a drain");
    assert_eq!(drained.ran, 2);
    assert_eq!(provider.heard.lock().unwrap().len(), 2);
    assert_eq!(provider.reports.lock().unwrap().len(), 1);
    assert_eq!(file.text("SELECT pool_availability FROM account").as_deref(), Some("out"));
    assert_eq!(
        file.count("account_event_queue", "state = 'ready' AND attempts = 1 AND last_error LIKE '%the provider is down%'"),
        1
    );

    // An hour on, the drain runs the event again, beside the recheck the
    // account's being out still arms, and the event of that: once the
    // method handles one, the account is open, and every event is spent.
    provider.failing.store(false, Ordering::SeqCst);
    let retried = again + Duration::from_secs(3600);
    let drained = worker.drain(&mut connection, retried, 10).expect("a drain");
    assert!(drained.ran >= 1, "{drained:?}");
    assert!(provider.reports.lock().unwrap().len() >= 2);
    assert_eq!(file.text("SELECT pool_availability FROM account").as_deref(), Some("open"));
    assert_eq!(file.count("account_event_queue", "1"), 0);
    assert_eq!(file.count("account_deadline_queue", "1"), 0);
}
