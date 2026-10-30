//! The host loop over a transaction in memory (a store that records what it
//! is asked and answers from a row it holds), with Workflow and with a toy
//! behavior of the tests' own, so no database is needed: the cases of the
//! HTTP runtime's `src/rules.test.ts`, the statements and values each makes
//! held exactly. `tests/sqlite.rs` and `tests/postgres.rs` run the loop on
//! real databases, and `tests/transcript.rs` replays the recorded
//! scenarios.

use std::cell::RefCell;
use std::future::Future;
use std::pin::pin;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::task::{Context, Poll, Waker};

use serde_json::{json, Map, Value};
use superschematic_rules::{Behavior, ConfigError, ConfigTarget, Cx, Declaration, Hook, UseSpec};

use crate::convert::{iso_of_millis, Bind, Cell};
use crate::run::{self, Dialect, Statement, Store};
use crate::{
    Action, Caller, Code, Column, Command, Convert, Core, CreateOptions, Error, ExecuteOptions,
    Host, Layout, Operation, OwnedField, Refusal, Table, TypeSpec, Use, MAX_ATTEMPTS,
};

/// A UUID key in its two forms: base62 on the wire and in the core,
/// hyphenated in the column.
pub(crate) const KEY: &str = "6fe1XoZ0UJpMNNdUOSMC2X";
pub(crate) const KEY_COLUMN: &str = "db360f43-5530-c407-56da-9cd8e0c971dd";

/// A key no row of a memory store holds.
const ABSENT: &str = "1fe1XoZ0UJpMNNdUOSMC2X";

pub(crate) fn flow() -> Value {
    json!({
        "states": ["draft", "paid", "shipped"],
        "initial": "draft",
        "transitions": [
            {"from": "draft", "to": "paid"},
            {"from": "paid", "to": "shipped", "permission": "orders.ship"}
        ]
    })
}

fn owned_field(name: &str, field: &str, column: &str, type_name: &str) -> OwnedField {
    OwnedField {
        name: name.into(),
        column: Column::new(field, column, type_name),
        value: false,
    }
}

/// Order, as the generator writes its layout: status a Workflow, a UUID key.
pub(crate) fn order_table() -> Table {
    let mut status = owned_field("status", "status", "status", "string");
    status.value = true;
    let mut entered = owned_field(
        "enteredAt",
        "statusEnteredAt",
        "status_entered_at",
        "Temporal.DateTime",
    );
    entered.column = entered.column.converted(Convert::EpochMillis);
    Table {
        type_name: "Order".into(),
        table: "order".into(),
        key: Column::new("id", "id", "Identity.UUID"),
        version: "_version".into(),
        live: None,
        own: vec![Column::new("title", "title", "string")],
        uses: vec![Use {
            id: "status".into(),
            behavior: "Workflow".into(),
            anchor: Some("status".into()),
            fields: vec![status, entered],
            operations: vec![Operation {
                name: "transition".into(),
                writes: true,
            }],
        }],
        spec: TypeSpec::new("Order").with(UseSpec::on_field("status", "Workflow", flow())),
    }
}

fn layout() -> Layout {
    Layout::new([order_table()])
}

/// Order with the toy behavior on the whole type, its one field `seen` in
/// `toy_seen`.
pub(crate) fn toy_table() -> Table {
    Table {
        uses: vec![Use {
            id: "Toy".into(),
            behavior: "Toy".into(),
            anchor: None,
            fields: vec![owned_field("seen", "toySeen", "toy_seen", "string")],
            operations: Vec::new(),
        }],
        spec: TypeSpec::new("Order").with(UseSpec::on_type("Toy", None)),
        ..order_table()
    }
}

/// A behavior of the tests' own, each operation one thing a behavior may
/// ask of a host.
pub(crate) struct Toy;

impl Behavior for Toy {
    type Config = Value;

    fn declaration(&self) -> Declaration {
        Declaration::new("Toy")
            .field("seen")
            .table("notes")
            .operation("reserve", true)
            .operation("fence", true)
            .operation("ask", false)
            .operation("emit", true)
            .operation("note", true)
            .operation("arm", true)
            .operation("walk", false)
            .operation("mint", true)
    }

    fn parse_config(&self, raw: &Value, _target: &ConfigTarget<'_>) -> Result<Value, ConfigError> {
        Ok(raw.clone())
    }

    // A config of {"emit": true} makes a create emit an event beside its
    // row; one of {"lock": true}, decide under the lock Stock/s1 whether the
    // type has rows, and one of {"lock": "<key>"} under that key.
    fn initialize(&self, cx: &Cx<'_, Value>, _params: &Value) -> Hook<()> {
        if cx.config().get("emit") == Some(&Value::Bool(true)) {
            cx.emit("created", json!({}))?;
        }
        let lock = match cx.config().get("lock") {
            Some(Value::Bool(true)) => Some("Stock/s1"),
            Some(Value::String(key)) => Some(key.as_str()),
            _ => None,
        };
        if let Some(lock) = lock {
            cx.lock(lock)?;
            let any = cx.has_rows(cx.type_name())?;
            cx.set("seen", if any { "after" } else { "alone" })?;
        }
        Ok(())
    }

    fn operation(&self, cx: &Cx<'_, Value>, name: &str, params: &Value) -> Hook<Value> {
        match name {
            // A rule across rows: its lock, asked for twice, before what it
            // decides.
            "reserve" => {
                cx.lock("Stock/s1")?;
                cx.lock("Stock/s1")?;
                cx.set("seen", "reserved")?;
                Ok(json!("ok"))
            }
            // Reads under a lock: a row that is there, one that is not, and
            // whether the type has rows.
            "fence" => {
                cx.lock("Stock/s1")?;
                let absent = params
                    .get("absent")
                    .and_then(Value::as_str)
                    .unwrap_or_default();
                let found = cx.read_row(cx.type_name(), absent)?.is_some();
                let any = cx.has_rows(cx.type_name())?;
                cx.set("seen", format!("{found} {any}"))?;
                Ok(Value::Null)
            }
            "ask" => Ok(json!({
                "hasRows": cx.has_rows("Order")?,
                "ghost": cx.read_spec("Ghost")?.is_none(),
                "invoice": cx.read_spec("Invoice")?.map(|spec| spec.name.clone()),
            })),
            "emit" => {
                cx.emit("paid", json!({}))?;
                Ok(Value::Null)
            }
            "note" => {
                cx.insert_row("notes", "n1", Map::new())?;
                Ok(Value::Null)
            }
            "arm" => {
                cx.arm("expire", cx.now() + 1000, Value::Null)?;
                Ok(Value::Null)
            }
            // Reads a row it has not read on every run, so it never
            // settles.
            "walk" => {
                for n in 0u64.. {
                    cx.read_row("Order", &n.to_string())?;
                }
                Ok(Value::Null)
            }
            "mint" => {
                let key = cx.new_key("thing")?;
                cx.set("seen", key.clone())?;
                Ok(Value::from(key))
            }
            _ => Err(cx.defect(format!("no operation {name}"))),
        }
    }
}

pub(crate) fn workflow() -> Core {
    superschematic_rules_behaviors::core()
}

pub(crate) fn toy() -> Core {
    Core::new().with(Toy)
}

/// What a memory store was asked.
#[derive(Default)]
struct Recorded {
    queries: Vec<(String, Vec<Bind>)>,
    applied: Vec<Vec<Statement>>,
    locks: Vec<String>,
    /// Every call in order: `query`, `lock <key>`, `apply`.
    log: Vec<String>,
}

/// A transaction in memory: a read of a row answers the row it holds (none
/// for the key `absent`, or when it holds none), an existence probe whether
/// it holds one, and apply what `applies` says for each attempt, true
/// unless it says otherwise.
struct MemoryStore {
    dialect: Dialect,
    fenced: bool,
    row: Option<Vec<Cell>>,
    absent: Option<Bind>,
    applies: Vec<bool>,
    recorded: RefCell<Recorded>,
}

impl MemoryStore {
    fn new(dialect: Dialect, row: Option<Vec<Cell>>) -> Self {
        MemoryStore {
            dialect,
            fenced: false,
            row,
            absent: None,
            applies: Vec::new(),
            recorded: RefCell::default(),
        }
    }

    fn applying(mut self, applies: &[bool]) -> Self {
        self.applies = applies.to_vec();
        self
    }

    fn fenced(mut self) -> Self {
        self.fenced = true;
        self
    }

    fn queries(&self) -> Vec<(String, Vec<Bind>)> {
        self.recorded.borrow().queries.clone()
    }

    fn applied(&self) -> Vec<Vec<Statement>> {
        self.recorded.borrow().applied.clone()
    }
}

impl Store for MemoryStore {
    fn dialect(&self) -> Dialect {
        self.dialect
    }

    fn fenced(&self) -> bool {
        self.fenced
    }

    async fn query(&self, text: &str, values: &[Bind]) -> Result<Vec<Vec<Cell>>, Error> {
        let mut recorded = self.recorded.borrow_mut();
        recorded.queries.push((text.to_owned(), values.to_vec()));
        recorded.log.push("query".into());
        let row = match (&self.absent, values.first()) {
            (Some(absent), Some(first)) if absent == first => None,
            _ => self.row.clone(),
        };
        if text.contains("LIMIT 1") || text.starts_with("SELECT 1 AS one") {
            return Ok(row.map(|_| vec![vec![Cell::Int(1)]]).unwrap_or_default());
        }
        Ok(row.into_iter().collect())
    }

    async fn apply(&self, statements: &[Statement]) -> Result<bool, Error> {
        let mut recorded = self.recorded.borrow_mut();
        recorded.applied.push(statements.to_vec());
        recorded.log.push("apply".into());
        Ok(self
            .applies
            .get(recorded.applied.len() - 1)
            .copied()
            .unwrap_or(true))
    }

    async fn lock(&self, key: &str) -> Result<(), Error> {
        let mut recorded = self.recorded.borrow_mut();
        recorded.locks.push(key.to_owned());
        recorded.log.push(format!("lock {key}"));
        Ok(())
    }
}

/// Runs the loop on a memory store, whose every step returns at once.
fn block<T>(work: impl Future<Output = T>) -> T {
    match pin!(work).poll(&mut Context::from_waker(Waker::noop())) {
        Poll::Ready(out) => out,
        Poll::Pending => panic!("a memory store never waits"),
    }
}

/// Order's draft row as Postgres reads it: each column `to_jsonb`'s JSON,
/// the version an integer.
fn draft_row() -> Vec<Cell> {
    vec![
        Cell::Json(json!("Desk")),
        Cell::Json(json!("draft")),
        Cell::Json(json!("1970-01-01T00:00:01+00:00")),
        Cell::Int(1),
    ]
}

/// The toy's row: the title, no `seen`, version 1.
fn toy_row() -> Vec<Cell> {
    vec![Cell::Text("Desk".into()), Cell::Null, Cell::Int(1)]
}

pub(crate) fn alice() -> Caller {
    Caller::new("alice", Vec::<String>::new())
}

pub(crate) fn command(action: Action) -> Command {
    Command::new(alice(), 2000, action).with_request("r2")
}

pub(crate) fn invoke(operation: &str, params: Value) -> Action {
    Action::Operation {
        key: KEY.into(),
        use_id: "status".into(),
        operation: operation.into(),
        params,
    }
}

pub(crate) fn transition(to: &str) -> Command {
    command(invoke("transition", json!({ "to": to })))
}

fn toy_op(operation: &str, params: Value) -> Command {
    command(Action::Operation {
        key: KEY.into(),
        use_id: "Toy".into(),
        operation: operation.into(),
        params,
    })
}

fn iso(millis: i64) -> Bind {
    Bind::Text(iso_of_millis(millis).expect("in range"))
}

fn text(value: &str) -> Bind {
    Bind::Text(value.into())
}

fn write(text: &str, values: Vec<Bind>) -> Statement {
    Statement {
        text: text.into(),
        values,
        expect: Some(1),
        check: false,
    }
}

fn check(text: &str, values: Vec<Bind>) -> Statement {
    Statement {
        text: text.into(),
        values,
        expect: None,
        check: true,
    }
}

pub(crate) fn refusal(result: Result<impl std::fmt::Debug, Error>) -> Refusal {
    match result {
        Err(Error::Refused(refusal)) => *refusal,
        other => panic!("not refused: {other:?}"),
    }
}

pub(crate) fn own(value: Value) -> Map<String, Value> {
    value.as_object().cloned().unwrap_or_default()
}

// ---- the host loop on the core ----

#[test]
fn a_create_inserts_the_key_the_own_fields_and_the_owned_columns_workflow_initializes_fenced_to_one_row(
) {
    let (core, layout) = (workflow(), layout());
    let host = Host::new(&core, &layout)
        .with_clock(|| 5000)
        .with_requests(|| "req".into());
    let store = MemoryStore::new(Dialect::Postgres, None);
    let options = CreateOptions {
        id: Some(KEY.into()),
        ..CreateOptions::default()
    };
    let created = block(run::run_create(
        &host,
        &store,
        "Order",
        own(json!({"title": "Desk"})),
        alice(),
        &options,
    ));
    assert_eq!(created.expect("created"), KEY);
    assert_eq!(
        store.queries(),
        [(
            r#"SELECT 1 AS one FROM "order" WHERE "id" = $1"#.to_owned(),
            vec![text(KEY_COLUMN)]
        )]
    );
    assert_eq!(
        store.applied(),
        [[write(
            r#"INSERT INTO "order" ("id", "title", "status", "status_entered_at") VALUES ($1, $2, $3, $4)"#,
            vec![text(KEY_COLUMN), text("Desk"), text("draft"), iso(5000)]
        )]]
    );
}

#[test]
fn a_create_with_no_id_gets_a_uuid_key_in_its_wire_form_and_its_own_columns_as_the_caller_encoded_them(
) {
    let (core, layout) = (workflow(), layout());
    let host = Host::new(&core, &layout).with_clock(|| 5000);
    let store = MemoryStore::new(Dialect::Sqlite, None);
    let options = CreateOptions {
        execute: ExecuteOptions {
            own_columns: Some(own(json!({"title": "Desk", "created_by": "u1"}))),
            soft_delete: None,
        },
        ..CreateOptions::default()
    };
    let created = block(run::run_create(
        &host,
        &store,
        "Order",
        own(json!({"title": "Desk"})),
        alice(),
        &options,
    ))
    .expect("created");
    assert!(
        (1..=22).contains(&created.len()) && created.chars().all(|c| c.is_ascii_alphanumeric()),
        "{created}"
    );
    let insert = &store.applied()[0][0];
    assert_eq!(
        insert.text,
        r#"INSERT INTO "order" ("id", "title", "created_by", "status", "status_entered_at") VALUES (?1, ?2, ?3, ?4, ?5)"#
    );
    assert_eq!(
        insert.values[1..],
        [text("Desk"), text("u1"), text("draft"), iso(5000)]
    );
}

#[test]
fn an_operation_reads_its_row_by_its_key_converting_each_column_to_the_core_and_writes_it_held_to_its_version(
) {
    let (core, layout) = (workflow(), layout());
    let host = Host::new(&core, &layout);
    let store = MemoryStore::new(Dialect::Postgres, Some(draft_row()));
    let row = block(run::read_row(&host, &store, "Order", KEY))
        .expect("read")
        .expect("a row");
    assert_eq!(
        serde_json::to_value(&row).expect("json"),
        json!({"own": {"title": "Desk"}, "owned": {"status": {"status": "draft", "enteredAt": 1000}}, "version": 1})
    );
    store.recorded.borrow_mut().queries.clear();
    let options = ExecuteOptions {
        own_columns: Some(own(json!({"updated_at": "1970-01-01T00:00:00.007Z"}))),
        soft_delete: None,
    };
    let executed = block(run::execute(
        &host,
        &store,
        "Order",
        transition("paid"),
        &options,
    ))
    .expect("applied");
    assert_eq!(
        serde_json::to_value(&executed.changes).expect("json"),
        json!({
            "rows": [{"kind": "update", "type": "Order", "key": KEY, "expectedVersion": 1, "own": {}, "owned": {"status": {"status": "paid", "enteredAt": 2000}}}],
            "subRows": [],
            "events": [],
            "deadlines": [],
            "result": {"from": "draft", "to": "paid"}
        })
    );
    assert_eq!(executed.result, json!({"from": "draft", "to": "paid"}));
    assert_eq!(executed.key.as_deref(), Some(KEY));
    assert_eq!(executed.attempts, 1);
    assert_eq!(
        store.queries(),
        [(
            r#"SELECT to_jsonb("title") AS c0, to_jsonb("status") AS c1, to_jsonb("status_entered_at") AS c2, "_version" AS v FROM "order" WHERE "id" = $1"#.to_owned(),
            vec![text(KEY_COLUMN)]
        )]
    );
    assert_eq!(
        store.applied(),
        [[write(
            r#"UPDATE "order" SET "updated_at" = $1, "status" = $2, "status_entered_at" = $3, "_version" = "_version" + 1 WHERE "id" = $4 AND "_version" = $5"#,
            vec![
                text("1970-01-01T00:00:00.007Z"),
                text("paid"),
                iso(2000),
                text(KEY_COLUMN),
                Bind::Int(1)
            ]
        )]]
    );
}

#[test]
fn a_row_that_moved_runs_the_command_again_from_fresh_state_and_one_that_keeps_moving_is_refused_as_busy(
) {
    let (core, layout) = (workflow(), layout());
    let host = Host::new(&core, &layout);
    let once = MemoryStore::new(Dialect::Postgres, Some(draft_row())).applying(&[false]);
    let executed = block(run::execute(
        &host,
        &once,
        "Order",
        transition("paid"),
        &ExecuteOptions::default(),
    ))
    .expect("applied");
    assert_eq!(executed.attempts, 2);
    // Each attempt read the row afresh.
    assert_eq!(once.queries().len(), 2);

    let always =
        MemoryStore::new(Dialect::Postgres, Some(draft_row())).applying(&[false; MAX_ATTEMPTS]);
    let refused = refusal(block(run::execute(
        &host,
        &always,
        "Order",
        transition("paid"),
        &ExecuteOptions::default(),
    )));
    assert_eq!(
        refused,
        crate::busy_refusal("Order", &transition("paid"), MAX_ATTEMPTS)
    );
    assert_eq!(
        (refused.code, refused.status, refused.class.as_str()),
        (Code::Busy, 503, "EngineError")
    );
    assert_eq!(
        refused.message,
        format!("transition of Order {KEY}: the row kept moving while the command ran; it ran 8 times and nothing was written, so it may be tried again")
    );
    assert_eq!(always.applied().len(), MAX_ATTEMPTS);
}

#[test]
fn a_refusal_is_the_cores_whole_and_writes_nothing() {
    let (core, layout) = (workflow(), layout());
    let host = Host::new(&core, &layout);
    let store = MemoryStore::new(Dialect::Postgres, Some(draft_row()));
    let result = block(run::run_operation(
        &host,
        &store,
        "Order",
        KEY,
        "status",
        "transition",
        json!({"to": "shipped"}),
        alice(),
    ));
    let refused = refusal(result);
    assert_eq!(
        (refused.status, refused.code, refused.class.as_str()),
        (409, Code::Vetoed, "BehaviorVetoError")
    );
    assert_eq!(
        refused.message,
        format!("behavior Workflow vetoes transition of Order {KEY}: no transition leads from draft to shipped; from draft it can move to paid")
    );
    assert_eq!(
        refused
            .details
            .as_ref()
            .and_then(|details| details.get("vetoCode")),
        Some(&json!("transition_not_allowed"))
    );
    assert!(store.applied().is_empty());
}

#[test]
fn an_update_that_names_an_owned_field_is_refused_the_anchor_in_the_cores_words_another_owned_field_in_the_same_shape(
) {
    let (core, layout) = (workflow(), layout());
    let host = Host::new(&core, &layout);
    let store = MemoryStore::new(Dialect::Postgres, Some(draft_row()));
    let refused_with = |patch: Value| {
        let result = block(run::run_update(
            &host,
            &store,
            "Order",
            KEY,
            own(patch),
            alice(),
            &ExecuteOptions::default(),
        ));
        serde_json::to_value(refusal(result)).expect("json")
    };
    assert_eq!(
        refused_with(json!({"status": "paid"})),
        json!({
            "code": "invalid_instance",
            "status": 422,
            "class": "InstanceValidationError",
            "message": "Order: status: status is behavior Workflow's field: only its operations change it",
            "details": {"issues": [{"path": "status", "rule": "owned", "message": "status is behavior Workflow's field: only its operations change it"}]}
        })
    );
    assert_eq!(
        refused_with(json!({"statusEnteredAt": null})),
        json!({
            "code": "invalid_instance",
            "status": 422,
            "class": "InstanceValidationError",
            "message": "Order: statusEnteredAt: statusEnteredAt is behavior Workflow's field: only its operations change it",
            "details": {"issues": [{"path": "statusEnteredAt", "rule": "owned", "message": "statusEnteredAt is behavior Workflow's field: only its operations change it"}]}
        })
    );
    assert!(store.applied().is_empty());
}

#[test]
fn a_create_of_a_key_the_table_holds_is_refused_as_conflict_before_anything_is_applied() {
    let (core, layout) = (workflow(), layout());
    let host = Host::new(&core, &layout);
    let store = MemoryStore::new(Dialect::Postgres, Some(draft_row()));
    let options = CreateOptions {
        id: Some(KEY.into()),
        ..CreateOptions::default()
    };
    let refused = refusal(block(run::run_create(
        &host,
        &store,
        "Order",
        own(json!({"title": "Desk"})),
        alice(),
        &options,
    )));
    assert_eq!(
        refused,
        Refusal::new(Code::Conflict, format!("Order {KEY} already exists"))
    );
    assert_eq!(
        (refused.status, refused.class.as_str()),
        (409, "EngineError")
    );
    assert!(store.applied().is_empty());
}

#[test]
fn a_type_the_layout_lacks_is_refused_as_internal() {
    let (core, layout) = (workflow(), layout());
    let host = Host::new(&core, &layout);
    let store = MemoryStore::new(Dialect::Postgres, Some(draft_row()));
    let refused = refusal(block(run::execute(
        &host,
        &store,
        "Invoice",
        transition("paid"),
        &ExecuteOptions::default(),
    )));
    assert_eq!(
        (refused.code, refused.status, refused.class.as_str()),
        (Code::Internal, 500, "Error")
    );
    assert_eq!(
        refused.message,
        "rules: the behaviors' layout has no type Invoice; it holds Order"
    );
    let empty = Layout::default();
    let refused = refusal(block(run::read_row(
        &Host::new(&core, &empty),
        &store,
        "Order",
        KEY,
    )));
    assert_eq!(
        refused.message,
        "rules: the behaviors' layout has no type Order; it holds none"
    );
}

#[test]
fn a_sqlite_row_reads_its_iso_text_as_epoch_milliseconds_a_null_column_as_absent_and_its_version_as_a_number(
) {
    let (core, layout) = (workflow(), layout());
    let host = Host::new(&core, &layout);
    let paid = MemoryStore::new(
        Dialect::Sqlite,
        Some(vec![
            Cell::Text("Desk".into()),
            Cell::Text("paid".into()),
            Cell::Null,
            Cell::Int(3),
        ]),
    );
    let row = block(run::read_row(&host, &paid, "Order", KEY)).expect("read");
    assert_eq!(
        serde_json::to_value(row).expect("json"),
        json!({"own": {"title": "Desk"}, "owned": {"status": {"status": "paid"}}, "version": 3})
    );
    let iso_only = MemoryStore::new(
        Dialect::Sqlite,
        Some(vec![
            Cell::Null,
            Cell::Null,
            Cell::Text("1970-01-01T00:00:01.234Z".into()),
            Cell::Int(1),
        ]),
    );
    let row = block(run::read_row(&host, &iso_only, "Order", KEY)).expect("read");
    assert_eq!(
        serde_json::to_value(row).expect("json"),
        json!({"own": {}, "owned": {"status": {"enteredAt": 1234}}, "version": 1})
    );
    assert_eq!(
        paid.queries()[0].0,
        r#"SELECT "title" AS c0, "status" AS c1, "status_entered_at" AS c2, "_version" AS v FROM "order" WHERE "id" = ?1"#
    );
}

/// A behavior whose every config is refused, counting how often the core
/// parses one.
struct Strict;

static STRICT_PARSES: AtomicUsize = AtomicUsize::new(0);

impl Behavior for Strict {
    type Config = ();

    fn declaration(&self) -> Declaration {
        Declaration::new("Strict")
    }

    fn parse_config(&self, _raw: &Value, _target: &ConfigTarget<'_>) -> Result<(), ConfigError> {
        STRICT_PARSES.fetch_add(1, Ordering::SeqCst);
        Err("it takes no config at all".into())
    }
}

/// A behavior of Strict's name that takes every config.
struct Lenient;

impl Behavior for Lenient {
    type Config = ();

    fn declaration(&self) -> Declaration {
        Declaration::new("Strict")
    }

    fn parse_config(&self, _raw: &Value, _target: &ConfigTarget<'_>) -> Result<(), ConfigError> {
        Ok(())
    }
}

/// A spec the core refuses refuses every command on its type, checked once
/// per core and layout, as `rules.ts` keeps the check: another core over
/// the same layout makes its own, and the first's stays its own.
#[test]
fn a_layouts_spec_the_core_refuses_refuses_every_command_on_its_type_checked_once_per_core() {
    let core = Core::new().with(Strict);
    let layout = Layout::new([Table {
        uses: Vec::new(),
        spec: TypeSpec::new("Order").with(UseSpec::on_type("Strict", None)),
        ..order_table()
    }]);
    let host = Host::new(&core, &layout);
    let store = MemoryStore::new(Dialect::Postgres, Some(draft_row()));
    for _ in 0..2 {
        let refused = refusal(block(run::execute(
            &host,
            &store,
            "Order",
            transition("paid"),
            &ExecuteOptions::default(),
        )));
        assert_eq!((refused.code, refused.status), (Code::InvalidSchema, 422));
        assert_eq!(
            refused.message,
            "type Order: behavior Strict config: it takes no config at all"
        );
    }
    assert_eq!(STRICT_PARSES.load(Ordering::SeqCst), 1);
    assert!(store.queries().is_empty());

    let lenient = Core::new().with(Lenient);
    let taken = block(run::execute(
        &Host::new(&lenient, &layout),
        &store,
        "Order",
        transition("paid"),
        &ExecuteOptions::default(),
    ));
    assert!(
        taken
            .as_ref()
            .err()
            .and_then(Error::refusal)
            .is_none_or(|refused| refused.code != Code::InvalidSchema),
        "{taken:?}"
    );
    let refused = refusal(block(run::execute(
        &host,
        &store,
        "Order",
        transition("paid"),
        &ExecuteOptions::default(),
    )));
    assert_eq!(refused.code, Code::InvalidSchema);
    assert_eq!(STRICT_PARSES.load(Ordering::SeqCst), 1);
}

/// A behavior of Strict's name that refuses every config, without
/// counting, for a test that runs beside Strict's.
struct Refusing;

impl Behavior for Refusing {
    type Config = ();

    fn declaration(&self) -> Declaration {
        Declaration::new("Strict")
    }

    fn parse_config(&self, _raw: &Value, _target: &ConfigTarget<'_>) -> Result<(), ConfigError> {
        Err("it takes no config at all".into())
    }
}

/// A core made where a dropped one was, composing a behavior of the same
/// name, makes its own define check: a layout keeps a check by the core's
/// id, not its address.
#[test]
fn a_core_made_where_a_dropped_one_was_makes_its_own_define_check() {
    let layout = Layout::new([Table {
        uses: Vec::new(),
        spec: TypeSpec::new("Order").with(UseSpec::on_type("Strict", None)),
        ..order_table()
    }]);
    let store = MemoryStore::new(Dialect::Postgres, Some(draft_row()));
    let mut core = Core::new().with(Lenient);
    let at = std::ptr::from_ref(&core) as usize;
    let taken = block(run::execute(
        &Host::new(&core, &layout),
        &store,
        "Order",
        transition("paid"),
        &ExecuteOptions::default(),
    ));
    assert!(
        taken
            .as_ref()
            .err()
            .and_then(Error::refusal)
            .is_none_or(|refused| refused.code != Code::InvalidSchema),
        "{taken:?}"
    );

    core = Core::new().with(Refusing);
    assert_eq!(std::ptr::from_ref(&core) as usize, at);
    let refused = refusal(block(run::execute(
        &Host::new(&core, &layout),
        &store,
        "Order",
        transition("paid"),
        &ExecuteOptions::default(),
    )));
    assert_eq!((refused.code, refused.status), (Code::InvalidSchema, 422));
}

// ---- the host loop on a toy behavior ----

#[test]
fn a_lock_is_taken_once_and_the_read_made_before_it_is_made_again_under_it() {
    let core = toy();
    let layout = Layout::new([toy_table()]);
    let host = Host::new(&core, &layout);
    let store = MemoryStore::new(Dialect::Postgres, Some(toy_row()));
    let executed = block(run::execute(
        &host,
        &store,
        "Order",
        toy_op("reserve", json!({})),
        &ExecuteOptions::default(),
    ))
    .expect("applied");
    assert_eq!(executed.result, json!("ok"));
    let recorded = store.recorded.borrow();
    assert_eq!(recorded.locks, ["Stock/s1"]);
    // The row, read before the lock, is read again under it.
    assert_eq!(recorded.log, ["query", "lock Stock/s1", "query", "apply"]);
}

#[test]
fn a_store_whose_locks_are_fenced_gets_first_in_the_unit_a_check_of_each_read_made_under_a_lock_a_soft_deletable_tables_live_rows_alone(
) {
    let core = toy();
    let layout = Layout::new([Table {
        live: Some(crate::Live {
            column: "deleted_at".into(),
        }),
        ..toy_table()
    }]);
    let host = Host::new(&core, &layout);
    let absent_column = "36ed48cd-b2e1-b6ce-3b26-89443d6971dd";
    assert_eq!(
        crate::convert::uuid_value(ABSENT)
            .map(crate::convert::hyphenate)
            .as_deref(),
        Some(absent_column)
    );
    let mut fenced = MemoryStore::new(Dialect::Sqlite, Some(toy_row())).fenced();
    fenced.absent = Some(text(absent_column));
    block(run::execute(
        &host,
        &fenced,
        "Order",
        toy_op("fence", json!({"absent": ABSENT})),
        &ExecuteOptions::default(),
    ))
    .expect("applied");
    assert_eq!(fenced.recorded.borrow().locks, ["Stock/s1"]);
    let unit = &fenced.applied()[0];
    assert_eq!(
        unit[..3],
        [
            check(
                r#"EXISTS (SELECT 1 FROM "order" WHERE "id" = ?1 AND "_version" = ?2 AND "deleted_at" IS NULL)"#,
                vec![text(KEY_COLUMN), Bind::Int(1)]
            ),
            check(
                r#"NOT EXISTS (SELECT 1 FROM "order" WHERE "id" = ?1 AND "deleted_at" IS NULL)"#,
                vec![text(absent_column)]
            ),
            check(
                r#"EXISTS (SELECT 1 FROM "order" WHERE "deleted_at" IS NULL)"#,
                Vec::new()
            ),
        ]
    );
    assert_eq!(
        unit[3],
        write(
            r#"UPDATE "order" SET "toy_seen" = ?1, "_version" = "_version" + 1 WHERE "id" = ?2 AND "_version" = ?3 AND "deleted_at" IS NULL"#,
            vec![text("false true"), text(KEY_COLUMN), Bind::Int(1)]
        )
    );
    assert_eq!(unit.len(), 4);

    // A store that holds its locks gets no check.
    let held = MemoryStore::new(Dialect::Postgres, Some(draft_row_of_toy()));
    block(run::execute(
        &host,
        &held,
        "Order",
        toy_op("fence", json!({"absent": ABSENT})),
        &ExecuteOptions::default(),
    ))
    .expect("applied");
    assert!(held.applied()[0].iter().all(|statement| !statement.check));
}

fn draft_row_of_toy() -> Vec<Cell> {
    vec![Cell::Json(json!("Desk")), Cell::Null, Cell::Int(1)]
}

#[test]
fn a_read_of_whether_a_type_has_rows_and_of_its_spec_answer_from_the_store_and_the_layout() {
    let core = toy();
    let invoice = Table {
        type_name: "Invoice".into(),
        table: "invoice".into(),
        uses: Vec::new(),
        spec: TypeSpec::new("Invoice"),
        ..order_table()
    };
    let layout = Layout::new([toy_table(), invoice]);
    let host = Host::new(&core, &layout);
    let store = MemoryStore::new(Dialect::Postgres, Some(draft_row_of_toy()));
    let executed = block(run::execute(
        &host,
        &store,
        "Order",
        toy_op("ask", json!({})),
        &ExecuteOptions::default(),
    ))
    .expect("applied");
    assert_eq!(
        executed.result,
        json!({"hasRows": true, "ghost": true, "invoice": "Invoice"})
    );
    assert!(store
        .queries()
        .iter()
        .any(|(text, _)| text == r#"SELECT 1 AS one FROM "order" LIMIT 1"#));
    // A read-only operation writes no row: its unit is empty.
    assert_eq!(store.applied(), [Vec::<Statement>::new()]);
}

#[test]
fn a_write_the_adapter_does_not_take_yet_and_a_run_that_never_settles_refuse_the_command_as_internal(
) {
    let core = toy();
    let layout = Layout::new([toy_table()]);
    let host = Host::new(&core, &layout);
    let refused_by = |operation: &str| {
        let store = MemoryStore::new(Dialect::Postgres, Some(draft_row_of_toy()));
        let refused = refusal(block(run::execute(
            &host,
            &store,
            "Order",
            toy_op(operation, json!({})),
            &ExecuteOptions::default(),
        )));
        assert!(store.applied().is_empty(), "{operation} applied");
        refused
    };
    assert_eq!(
        refused_by("emit"),
        Refusal::internal("rules: emit of Order writes outbox events, which the adapter takes once each has a table of its own (D103's order of the work, step 3)")
    );
    assert!(refused_by("note").message.contains("writes sub-table rows"));
    assert!(refused_by("arm").message.contains("writes deadlines"));
    assert_eq!(
        refused_by("walk").message,
        "rules: walk of Order: the run still asked for reads or locks after 256 rounds"
    );
}

#[test]
fn a_command_gets_the_hosts_request_id_once_for_every_attempt() {
    let core = toy();
    let layout = Layout::new([toy_table()]);
    let requests = AtomicUsize::new(0);
    let host = Host::new(&core, &layout)
        .with_requests(|| format!("r{}", requests.fetch_add(1, Ordering::SeqCst) + 1));
    let store =
        MemoryStore::new(Dialect::Postgres, Some(draft_row_of_toy())).applying(&[false, true]);
    let mut unnamed = toy_op("mint", json!({}));
    unnamed.request = String::new();
    let executed = block(run::execute(
        &host,
        &store,
        "Order",
        unnamed,
        &ExecuteOptions::default(),
    ))
    .expect("applied");
    assert_eq!(executed.attempts, 2);
    assert_eq!(requests.load(Ordering::SeqCst), 1);
    // The key the behavior made is derived from the one request id, and
    // both attempts wrote the same.
    let minted = executed.result.as_str().expect("a key").to_owned();
    assert!(minted.starts_with("r1:"), "{minted}");
    let applied = store.applied();
    assert_eq!(applied[0][0].values[0], text(&minted));
    assert_eq!(applied[1][0].values[0], text(&minted));
}

#[test]
fn a_prepared_create_applies_nothing_and_gives_the_owned_fields_of_the_row_alone() {
    let (core, layout) = (workflow(), layout());
    let host = Host::new(&core, &layout).with_clock(|| 9000);
    let store = MemoryStore::new(Dialect::Postgres, None);
    let owned = block(run::prepare_create(
        &host,
        &store,
        "Order",
        own(json!({"title": "Desk"})),
        alice(),
        &CreateOptions::default(),
    ))
    .expect("prepared");
    assert_eq!(
        serde_json::to_value(owned).expect("json"),
        json!({"status": {"status": "draft", "enteredAt": 9000}})
    );
    assert!(store.applied().is_empty());
    assert!(store.queries().is_empty());
    // An owned field among the own fields is refused, as a create's is.
    let refused = refusal(block(run::prepare_create(
        &host,
        &store,
        "Order",
        own(json!({"statusEnteredAt": 1})),
        alice(),
        &CreateOptions::default(),
    )));
    assert_eq!(refused.code, Code::InvalidInstance);
}

#[test]
fn a_prepared_create_that_writes_more_than_the_row_or_reads_under_a_fenced_lock_is_refused_as_internal(
) {
    let core = toy();
    let toy_with = |config: Value| {
        Layout::new([Table {
            spec: TypeSpec::new("Order").with(UseSpec::on_type("Toy", config)),
            ..toy_table()
        }])
    };
    let loud = toy_with(json!({"emit": true}));
    let store = MemoryStore::new(Dialect::Postgres, None);
    let refused = refusal(block(run::prepare_create(
        &Host::new(&core, &loud),
        &store,
        "Order",
        Map::new(),
        alice(),
        &CreateOptions::default(),
    )));
    assert_eq!(
        refused,
        Refusal::internal("rules: an upsert of Order inserts the row a create of the core gives it, and that create writes more than the row; create the row with a create of its own")
    );

    let locking = toy_with(json!({"lock": true}));
    let host = Host::new(&core, &locking);
    let fenced = MemoryStore::new(Dialect::Sqlite, None).fenced();
    let refused = refusal(block(run::prepare_create(
        &host,
        &fenced,
        "Order",
        Map::new(),
        alice(),
        &CreateOptions::default(),
    )));
    assert_eq!(
        refused,
        Refusal::internal("rules: an upsert of Order binds what a create of the core gives the row, and that create reads under a lock this store holds only in a unit of its own; create the row with a create of its own")
    );
    // A store that holds its locks holds them for the upsert that follows.
    let held = MemoryStore::new(Dialect::Postgres, None);
    let owned = block(run::prepare_create(
        &host,
        &held,
        "Order",
        Map::new(),
        alice(),
        &CreateOptions::default(),
    ))
    .expect("prepared");
    assert_eq!(
        serde_json::to_value(owned).expect("json"),
        json!({"Toy": {"seen": "alone"}})
    );
    assert_eq!(held.recorded.borrow().locks, ["Stock/s1"]);
    assert!(held.applied().is_empty());
}

#[test]
fn a_soft_delete_stamps_the_live_row_held_to_its_version_in_place_of_deleting_it() {
    let core = workflow();
    let layout = Layout::new([Table {
        live: Some(crate::Live {
            column: "deleted_at".into(),
        }),
        ..order_table()
    }]);
    let host = Host::new(&core, &layout);
    let store = MemoryStore::new(Dialect::Postgres, Some(draft_row()));
    let options = ExecuteOptions {
        own_columns: None,
        soft_delete: Some(own(
            json!({"deleted_at": "1970-01-01T00:00:09.000Z", "deleted_by": "u1"}),
        )),
    };
    block(run::run_delete(
        &host,
        &store,
        "Order",
        KEY,
        alice(),
        &options,
    ))
    .expect("deleted");
    assert_eq!(
        store.applied(),
        [[write(
            r#"UPDATE "order" SET "deleted_at" = $1, "deleted_by" = $2, "_version" = "_version" + 1 WHERE "id" = $3 AND "_version" = $4 AND "deleted_at" IS NULL"#,
            vec![
                text("1970-01-01T00:00:09.000Z"),
                text("u1"),
                text(KEY_COLUMN),
                Bind::Int(1)
            ]
        )]]
    );
    assert!(store.queries()[0]
        .0
        .ends_with(r#"WHERE "id" = $1 AND "deleted_at" IS NULL"#));

    let plain = MemoryStore::new(Dialect::Postgres, Some(draft_row()));
    block(run::run_delete(
        &host,
        &plain,
        "Order",
        KEY,
        alice(),
        &ExecuteOptions::default(),
    ))
    .expect("deleted");
    assert_eq!(
        plain.applied(),
        [[write(
            r#"DELETE FROM "order" WHERE "id" = $1 AND "_version" = $2 AND "deleted_at" IS NULL"#,
            vec![text(KEY_COLUMN), Bind::Int(1)]
        )]]
    );
}

#[test]
fn a_create_of_a_type_whose_key_the_adapter_makes_none_for_names_its_key() {
    let core = workflow();
    let layout = Layout::new([Table {
        key: Column::new("id", "id", "number"),
        ..order_table()
    }]);
    let host = Host::new(&core, &layout);
    let store = MemoryStore::new(Dialect::Postgres, None);
    let refused = refusal(block(run::run_create(
        &host,
        &store,
        "Order",
        Map::new(),
        alice(),
        &CreateOptions::default(),
    )));
    assert_eq!(
        refused,
        Refusal::invalid_argument("a create of Order names no key, and the adapter makes none for a key of type number: pass the id")
    );
    // A string key gets a UUID's text.
    let layout = Layout::new([Table {
        key: Column::new("id", "id", "string"),
        ..order_table()
    }]);
    let host = Host::new(&core, &layout);
    let created = block(run::run_create(
        &host,
        &store,
        "Order",
        own(json!({"title": "Desk"})),
        alice(),
        &CreateOptions::default(),
    ))
    .expect("created");
    assert!(
        crate::convert::uuid_value(&created).is_some() && created.len() == 36,
        "{created}"
    );
}

/// The tables the store tests on real databases run on, in a dialect: the
/// layout of [`order_table`] as `order` and as the soft-deletable `ticket`,
/// and [`toy_table`]'s as `toy`, each with sqlgen's `@optimistic` trigger.
#[cfg(any(feature = "postgres", feature = "sqlite"))]
pub(crate) fn ddl(postgres: bool) -> Vec<String> {
    let (timestamp, version) = if postgres {
        ("TIMESTAMPTZ", "BIGINT DEFAULT 1 NOT NULL")
    } else {
        ("TEXT", "INTEGER DEFAULT 1 NOT NULL")
    };
    let mut ddl = vec![
        format!("CREATE TABLE \"order\" (id TEXT PRIMARY KEY, title TEXT, status TEXT, status_entered_at {timestamp}, _version {version})"),
        format!("CREATE TABLE ticket (id TEXT PRIMARY KEY, title TEXT, status TEXT, status_entered_at {timestamp}, deleted_at {timestamp}, _version {version})"),
        format!("CREATE TABLE toy (id TEXT PRIMARY KEY, title TEXT, toy_seen TEXT, _version {version})"),
    ];
    for table in ["order", "ticket", "toy"] {
        if postgres {
            ddl.push(format!(
                "CREATE FUNCTION \"{table}_bump\"() RETURNS trigger AS $$ BEGIN NEW._version := OLD._version + 1; RETURN NEW; END; $$ LANGUAGE plpgsql"
            ));
            ddl.push(format!(
                "CREATE TRIGGER \"{table}_bump\" BEFORE UPDATE ON \"{table}\" FOR EACH ROW EXECUTE FUNCTION \"{table}_bump\"()"
            ));
        } else {
            ddl.push(format!(
                "CREATE TRIGGER \"{table}_bump\" AFTER UPDATE ON \"{table}\" FOR EACH ROW WHEN NEW._version = OLD._version BEGIN UPDATE \"{table}\" SET _version = OLD._version + 1 WHERE rowid = NEW.rowid; END"
            ));
        }
    }
    ddl
}

/// The layout of the tables [`ddl`] makes.
#[cfg(any(feature = "postgres", feature = "sqlite"))]
pub(crate) fn db_layout() -> Layout {
    Layout::new([
        order_table(),
        Table {
            type_name: "Ticket".into(),
            table: "ticket".into(),
            live: Some(crate::Live {
                column: "deleted_at".into(),
            }),
            spec: TypeSpec::new("Ticket").with(UseSpec::on_field("status", "Workflow", flow())),
            ..order_table()
        },
        Table {
            type_name: "Toy".into(),
            table: "toy".into(),
            spec: TypeSpec::new("Toy").with(UseSpec::on_type("Toy", json!({"lock": true}))),
            ..toy_table()
        },
    ])
}

/// Workflow, and the toy.
#[cfg(any(feature = "postgres", feature = "sqlite"))]
pub(crate) fn both() -> Core {
    workflow().with(Toy)
}
