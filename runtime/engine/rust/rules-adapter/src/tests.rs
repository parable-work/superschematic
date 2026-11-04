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
use superschematic_rules::{
    Behavior, ConfigError, ConfigTarget, Cx, Declaration, FieldSpec, GuardRequest, Heard, Hook,
    RowWrite, SchemaIssue, UseSpec, Veto, View,
};

use crate::convert::{iso_of_millis, Bind, Cell};
use crate::run::{self, Dialect, Statement, Store};
use crate::{
    Action, Caller, ChangeRun, Changes, Claim, Code, Column, Command, Convert, Core, CreateOptions,
    Deadline, DeadlineRun, Deadlines, Error, ExecuteOptions, Host, Layout, Live, Operation,
    OwnedField, Refusal, SubTable, Table, TableChange, TypeSpec, Use, MAX_ATTEMPTS,
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
        audit: Vec::new(),
        uses: vec![Use {
            id: "status".into(),
            behavior: "Workflow".into(),
            member: "status".into(),
            anchor: Some("status".into()),
            fields: vec![status, entered],
            operations: vec![Operation {
                name: "transition".into(),
                writes: true,
            }],
            tables: Vec::new(),
        }],
        deadlines: None,
        changes: None,
        spec: TypeSpec::new("Order").with(UseSpec::on_field("status", "Workflow", flow())),
    }
}

fn layout() -> Layout {
    Layout::new([order_table()])
}

/// Order with the toy behavior on the whole type, its one field `seen` in
/// `toy_seen`, its sub-table `notes` in `order_toy_notes` (each row keyed
/// by `key`, its `body` and its `at`), and the queue its deadlines are
/// armed in, `order_deadline_queue`.
pub(crate) fn toy_table() -> Table {
    let mut at = owned_field("at", "at", "at", "Temporal.DateTime");
    at.column = at.column.converted(Convert::EpochMillis);
    Table {
        uses: vec![Use {
            id: "Toy".into(),
            behavior: "Toy".into(),
            member: "toy".into(),
            anchor: None,
            fields: vec![owned_field("seen", "toySeen", "toy_seen", "string")],
            operations: Vec::new(),
            tables: vec![SubTable {
                name: "notes".into(),
                table: "order_toy_notes".into(),
                row: "order_id".into(),
                key: owned_field("key", "key", "key", "string"),
                fields: vec![owned_field("body", "body", "body", "string"), at],
            }],
        }],
        deadlines: Some(Deadlines {
            queue: "OrderDeadline".into(),
            table: "order_deadline_queue".into(),
            row_key: "row_key".into(),
            use_id: "use_id".into(),
            name: "name".into(),
            at: "at".into(),
            payload: "payload".into(),
        }),
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
            .arms_deadlines()
            .hears_changes()
            .operation("linked", false)
            .operation("reserve", true)
            .operation("fence", true)
            .operation("ask", false)
            .operation("emit", true)
            .operation("note", true)
            .operation("notes", false)
            .operation("lockedNotes", false)
            .operation("arm", true)
            .operation("clear", true)
            .operation("walk", false)
            .operation("mint", true)
            .operation("holding", false)
            .preconditions()
    }

    // A caller presents it a precondition of `{"stop": <boolean>}`, an
    // object with no other member.
    fn check_precondition(&self, _config: &Value, entry: &Value) -> Vec<SchemaIssue> {
        match entry {
            Value::Object(members) => members
                .keys()
                .filter(|name| *name != "stop")
                .map(|name| {
                    SchemaIssue::new("", format!("must NOT have additional properties: {name}"))
                })
                .collect(),
            _ => vec![SchemaIssue::new("", "must be object")],
        }
    }

    // A precondition of `{"stop": true}` vetoes the command's own write.
    fn guard(&self, view: &View<'_, Value>, _request: &GuardRequest<'_>) -> Hook<Option<Veto>> {
        Ok(view
            .precondition()
            .filter(|entry| entry.get("stop") == Some(&Value::Bool(true)))
            .map(|_| Veto::new("its caller's precondition stops it")))
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
            // A sub-table row: inserted with the values params give, or
            // updated with params' update, or deleted.
            "note" => {
                let values = |name: &str| {
                    params
                        .get(name)
                        .and_then(Value::as_object)
                        .cloned()
                        .unwrap_or_default()
                };
                match params.get("change").and_then(Value::as_str) {
                    Some("update") => cx.update_row("notes", "n1", values("values"))?,
                    Some("delete") => cx.delete_row("notes", "n1")?,
                    _ => cx.insert_row("notes", "n1", values("values"))?,
                }
                Ok(Value::Null)
            }
            // The row's notes, as the core reads them, and under a lock.
            "notes" => Ok(serde_json::to_value(cx.sub_rows("notes")?).unwrap_or_default()),
            "lockedNotes" => {
                cx.lock("Stock/s1")?;
                Ok(serde_json::to_value(cx.sub_rows("notes")?).unwrap_or_default())
            }
            "arm" => {
                let payload = params.get("payload").cloned().unwrap_or(Value::Null);
                cx.arm("expire", cx.now() + 1000, payload)?;
                Ok(Value::Null)
            }
            "clear" => {
                cx.clear("expire")?;
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
            // A page of the rows of params' type whose use's field holds
            // its value, after its after, under the lock Stock/s1 with its
            // lock.
            "holding" => {
                if params.get("lock") == Some(&Value::Bool(true)) {
                    cx.lock("Stock/s1")?;
                }
                let param = |name: &str| params.get(name).and_then(Value::as_str);
                let value = params.get("value").cloned().unwrap_or(Value::Null);
                let page = cx.read_holding(
                    param("type").unwrap_or_default(),
                    param("use").unwrap_or_default(),
                    param("field").unwrap_or_default(),
                    &value,
                    param("after"),
                )?;
                Ok(serde_json::to_value(page).unwrap_or_default())
            }
            // A page of the rows of params' type whose field holds its
            // key, after its after, under the lock Stock/s1 with its lock.
            "linked" => {
                if params.get("lock") == Some(&Value::Bool(true)) {
                    cx.lock("Stock/s1")?;
                }
                let param = |name: &str| params.get(name).and_then(Value::as_str);
                let page = cx.read_linked(
                    param("type").unwrap_or_default(),
                    param("field").unwrap_or_default(),
                    param("key").unwrap_or_default(),
                    param("after"),
                )?;
                Ok(serde_json::to_value(page).unwrap_or_default())
            }
            _ => Err(cx.defect(format!("no operation {name}"))),
        }
    }

    // A write of its own row it hears marks the row with the change as the
    // core got it, as JSON.
    fn heard(&self, cx: &Cx<'_, Value>, heard: &Heard<'_>) -> Hook<()> {
        if let Heard::Own { change } = heard {
            cx.set("seen", serde_json::to_string(change).unwrap_or_default())?;
        }
        Ok(())
    }

    // A deadline marks the row with its name, and one armed with
    // {"again": true} arms itself again, a second later; one armed with
    // {"reach": true} is refused not_found, as a rule that reached a row
    // that is gone would be, while its own row and use are there.
    fn deadline(&self, cx: &Cx<'_, Value>, name: &str, payload: &Value) -> Hook<()> {
        if payload.get("reach") == Some(&Value::Bool(true)) {
            return Err(Refusal::not_found("Shelf 7 not found").into());
        }
        cx.set("seen", format!("fired {name}"))?;
        if payload.get("again") == Some(&Value::Bool(true)) {
            cx.arm(name, cx.now() + 1000, Value::Null)?;
        }
        Ok(())
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
/// it holds one, a read that begins with one of `responds`' texts its rows,
/// and apply what `applies` says for each attempt, true unless it says
/// otherwise.
struct MemoryStore {
    dialect: Dialect,
    fenced: bool,
    row: Option<Vec<Cell>>,
    absent: Option<Bind>,
    applies: Vec<bool>,
    responds: Vec<(String, Vec<Vec<Cell>>)>,
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
            responds: Vec::new(),
            recorded: RefCell::default(),
        }
    }

    fn applying(mut self, applies: &[bool]) -> Self {
        self.applies = applies.to_vec();
        self
    }

    /// The store answering a read whose text begins with `text` with
    /// `rows`.
    fn responding(mut self, text: &str, rows: Vec<Vec<Cell>>) -> Self {
        self.responds.push((text.to_owned(), rows));
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
        if let Some((_, rows)) = self
            .responds
            .iter()
            .find(|(prefix, _)| text.starts_with(prefix.as_str()))
        {
            return Ok(rows.clone());
        }
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
            ..ExecuteOptions::default()
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
        ..ExecuteOptions::default()
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
        &ExecuteOptions::default(),
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

/// Order with `createdBy` among its audit fields (D104), a UUID in
/// `created_by`, its status a Workflow whose move to paid is made by the
/// row's creator and whose repeat answers unchanged.
fn audited_table() -> Table {
    let flow = json!({
        "states": ["draft", "paid", "shipped"],
        "initial": "draft",
        "transitions": [
            {"from": "draft", "to": "paid", "by": ["createdBy"]},
            {"from": "paid", "to": "shipped"}
        ],
        "repeat": "unchanged"
    });
    Table {
        audit: vec![Column::new("createdBy", "created_by", "Identity.UUID")],
        spec: TypeSpec::new("Order")
            .with(UseSpec::on_field("status", "Workflow", flow))
            .with_field(FieldSpec::new("title", "string"))
            .with_field(FieldSpec::new("createdBy", "Identity.UUID")),
        ..order_table()
    }
}

/// The audited Order's draft row as Postgres reads it, created by the
/// subject whose UUID is the row's key's.
fn audited_row() -> Vec<Cell> {
    vec![
        Cell::Json(json!("Desk")),
        Cell::Json(json!(KEY_COLUMN)),
        Cell::Json(json!("draft")),
        Cell::Json(json!("1970-01-01T00:00:01+00:00")),
        Cell::Int(1),
    ]
}

#[test]
fn a_row_read_selects_the_audit_columns_after_the_own_ones_into_its_own_fields_and_a_write_of_one_is_internal(
) {
    let core = workflow();
    let layout = Layout::new([audited_table()]);
    let host = Host::new(&core, &layout);
    let store = MemoryStore::new(Dialect::Postgres, Some(audited_row()));
    let row = block(run::read_row(&host, &store, "Order", KEY))
        .expect("read")
        .expect("a row");
    assert_eq!(
        serde_json::to_value(&row).expect("json"),
        json!({"own": {"title": "Desk", "createdBy": KEY}, "owned": {"status": {"status": "draft", "enteredAt": 1000}}, "version": 1})
    );
    assert_eq!(
        store.queries(),
        [(
            r#"SELECT to_jsonb("title") AS c0, to_jsonb("created_by") AS c1, to_jsonb("status") AS c2, to_jsonb("status_entered_at") AS c3, "_version" AS v FROM "order" WHERE "id" = $1"#.to_owned(),
            vec![text(KEY_COLUMN)]
        )]
    );
    // A null created_by is absent, as a NULL own column is.
    let unstamped = MemoryStore::new(
        Dialect::Sqlite,
        Some(vec![
            Cell::Text("Desk".into()),
            Cell::Null,
            Cell::Text("draft".into()),
            Cell::Text("1970-01-01T00:00:01.000Z".into()),
            Cell::Int(1),
        ]),
    );
    let row = block(run::read_row(&host, &unstamped, "Order", KEY))
        .expect("read")
        .expect("a row");
    assert_eq!(
        serde_json::to_value(&row.own).expect("json"),
        json!({"title": "Desk"})
    );
    // No command writes one: an update that names it is the layout's
    // internal refusal of a field it has no own column for.
    let refused = refusal(block(run::run_update(
        &host,
        &store,
        "Order",
        KEY,
        own(json!({"createdBy": KEY})),
        alice(),
        &ExecuteOptions::default(),
    )));
    assert_eq!(
        (refused.code, refused.message.as_str()),
        (
            Code::Internal,
            "rules: Order has no own field createdBy in the behaviors' layout"
        )
    );
    assert!(store.applied().is_empty());
}

#[test]
fn a_transition_by_the_subject_an_audit_field_holds_moves_the_row_and_a_repeat_applies_an_empty_unit(
) {
    let core = workflow();
    let layout = Layout::new([audited_table()]);
    let host = Host::new(&core, &layout);
    let move_as = |subject: &str, to: &str, row: Vec<Cell>| {
        let store = MemoryStore::new(Dialect::Postgres, Some(row));
        let command = Command::new(
            Caller::new(subject, Vec::<String>::new()),
            2000,
            invoke("transition", json!({ "to": to })),
        )
        .with_request("r2");
        let executed = block(run::execute(
            &host,
            &store,
            "Order",
            command,
            &ExecuteOptions::default(),
        ));
        (executed, store.applied())
    };
    // The creator, in the subject's base62 form, makes the move by; anyone
    // else is not_principal and nothing is applied.
    let (moved, applied) = move_as(KEY, "paid", audited_row());
    assert_eq!(
        moved.expect("applied").result,
        json!({"from": "draft", "to": "paid"})
    );
    assert_eq!(applied.len(), 1);
    let (refused, applied) = move_as("bob", "paid", audited_row());
    assert_eq!(
        refusal(refused)
            .details
            .as_ref()
            .map(|details| details["vetoCode"].clone()),
        Some(json!("not_principal"))
    );
    assert!(applied.is_empty());
    // A move to the state the row is in answers unchanged through an empty
    // unit: no row write, so its version and enteredAt stay as they are.
    let (repeated, applied) = move_as("bob", "draft", audited_row());
    let repeated = repeated.expect("answered");
    assert_eq!(
        repeated.result,
        json!({"from": "draft", "to": "draft", "unchanged": true})
    );
    assert!(repeated.changes.rows.is_empty() && repeated.changes.sub_rows.is_empty());
    assert_eq!(applied, [Vec::<Statement>::new()]);
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
fn an_outbox_event_the_adapter_does_not_take_yet_and_a_run_that_never_settles_refuse_the_command_as_internal(
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
        Refusal::internal("rules: emit of Order writes outbox events of a behavior's own, which the adapter does not take yet: a table's change queue holds its writes, and the event stream comes later (D103)")
    );
    assert_eq!(
        refused_by("walk").message,
        "rules: walk of Order: the run still asked for reads or locks after 256 rounds"
    );
}

/// The statements a toy operation's unit applies, on a store of `dialect`
/// holding the toy's row.
fn unit_of(dialect: Dialect, operation: &str, params: Value) -> Vec<Statement> {
    let core = toy();
    let layout = Layout::new([toy_table()]);
    let host = Host::new(&core, &layout).with_clock(|| 2000);
    let store = MemoryStore::new(dialect, Some(draft_row_of_toy()));
    block(run::execute(
        &host,
        &store,
        "Order",
        toy_op(operation, params),
        &ExecuteOptions::default(),
    ))
    .expect("applied");
    let mut applied = store.applied();
    assert_eq!(applied.len(), 1, "one unit");
    applied.remove(0)
}

/// The toy row's write, held to its version: the first statement of every
/// unit of a toy operation that writes.
fn toy_row_write(dialect: Dialect) -> Statement {
    let (key, version) = match dialect {
        Dialect::Postgres => ("$1", "$2"),
        Dialect::Sqlite => ("?1", "?2"),
    };
    write(
        &format!(
            r#"UPDATE "order" SET "_version" = "_version" + 1 WHERE "id" = {key} AND "_version" = {version}"#
        ),
        vec![text(KEY_COLUMN), Bind::Int(1)],
    )
}

#[test]
fn a_sub_table_row_write_is_one_statement_after_the_rows_held_to_one_row_its_values_in_the_layouts_order(
) {
    let insert = unit_of(
        Dialect::Postgres,
        "note",
        json!({"values": {"at": 5000, "body": "hi"}}),
    );
    assert_eq!(
        insert,
        [
            toy_row_write(Dialect::Postgres),
            write(
                r#"INSERT INTO "order_toy_notes" ("order_id", "key", "body", "at") VALUES ($1, $2, $3, $4) ON CONFLICT ("order_id", "key") DO NOTHING"#,
                vec![text(KEY_COLUMN), text("n1"), text("hi"), iso(5000)]
            )
        ]
    );
    let update = unit_of(
        Dialect::Sqlite,
        "note",
        json!({"change": "update", "values": {"body": "bye", "at": null}}),
    );
    assert_eq!(
        update[1..],
        [write(
            r#"UPDATE "order_toy_notes" SET "body" = ?3, "at" = ?4 WHERE "order_id" = ?1 AND "key" = ?2"#,
            vec![text(KEY_COLUMN), text("n1"), text("bye"), Bind::Null]
        )]
    );
    // An update that sets nothing writes nothing.
    assert_eq!(
        unit_of(Dialect::Postgres, "note", json!({"change": "update"})),
        [toy_row_write(Dialect::Postgres)]
    );
    assert_eq!(
        unit_of(Dialect::Postgres, "note", json!({"change": "delete"}))[1..],
        [write(
            r#"DELETE FROM "order_toy_notes" WHERE "order_id" = $1 AND "key" = $2"#,
            vec![text(KEY_COLUMN), text("n1")]
        )]
    );

    // A value the layout's table does not hold, the key's among them, is
    // the host's defect.
    let core = toy();
    let layout = Layout::new([toy_table()]);
    let host = Host::new(&core, &layout);
    for values in [json!({"colour": "red"}), json!({"key": "n2"})] {
        let store = MemoryStore::new(Dialect::Postgres, Some(draft_row_of_toy()));
        let refused = refusal(block(run::execute(
            &host,
            &store,
            "Order",
            toy_op("note", json!({ "values": values })),
            &ExecuteOptions::default(),
        )));
        assert_eq!(refused.code, Code::Internal);
        assert!(
            refused
                .message
                .starts_with("rules: sub-table order_toy_notes has no field "),
            "{}",
            refused.message
        );
        assert!(store.applied().is_empty());
    }
}

#[test]
fn a_read_of_sub_table_rows_selects_the_rows_of_the_row_in_key_order_converted_as_owned_fields_are()
{
    let core = toy();
    let layout = Layout::new([toy_table()]);
    let host = Host::new(&core, &layout);
    let read = r#"SELECT to_jsonb("key") AS c0, to_jsonb("body") AS c1, to_jsonb("at") AS c2 FROM "order_toy_notes" WHERE "order_id" = $1 ORDER BY "key" COLLATE "C""#;
    let store = MemoryStore::new(Dialect::Postgres, Some(draft_row_of_toy())).responding(
        r#"SELECT to_jsonb("key")"#,
        vec![
            vec![
                Cell::Json(json!("n1")),
                Cell::Json(json!("hi")),
                Cell::Json(json!("1970-01-01T00:00:05+00:00")),
            ],
            vec![Cell::Json(json!("n2")), Cell::Json(Value::Null), Cell::Null],
        ],
    );
    let executed = block(run::execute(
        &host,
        &store,
        "Order",
        toy_op("notes", json!({})),
        &ExecuteOptions::default(),
    ))
    .expect("read");
    assert_eq!(
        executed.result,
        json!([{"key": "n1", "values": {"body": "hi", "at": 5000}}, {"key": "n2", "values": {}}])
    );
    assert!(store
        .queries()
        .contains(&(read.to_owned(), vec![text(KEY_COLUMN)])));

    // SQLite reads each column as it is stored, in its own key order.
    let store = MemoryStore::new(Dialect::Sqlite, Some(draft_row_of_toy())).responding(
        r#"SELECT "key""#,
        vec![vec![
            Cell::Text("n1".into()),
            Cell::Null,
            Cell::Text("1970-01-01T00:00:05.000Z".into()),
        ]],
    );
    let executed = block(run::execute(
        &host,
        &store,
        "Order",
        toy_op("notes", json!({})),
        &ExecuteOptions::default(),
    ))
    .expect("read");
    assert_eq!(
        executed.result,
        json!([{"key": "n1", "values": {"at": 5000}}])
    );
    assert!(store.queries().iter().any(|(text, _)| text
        == r#"SELECT "key" AS c0, "body" AS c1, "at" AS c2 FROM "order_toy_notes" WHERE "order_id" = ?1 ORDER BY "key""#));

    // A store whose locks are fenced has no check of one under a lock yet.
    let fenced = MemoryStore::new(Dialect::Sqlite, Some(draft_row_of_toy())).fenced();
    let refused = refusal(block(run::execute(
        &host,
        &fenced,
        "Order",
        toy_op("lockedNotes", json!({})),
        &ExecuteOptions::default(),
    )));
    assert_eq!(
        refused,
        Refusal::internal(
            "rules: a read of sub-table rows under a lock has no check on SQLite or D1 yet (D103)"
        )
    );
}

#[test]
fn a_deadline_armed_or_cleared_deletes_its_messages_and_an_arm_inserts_one_due_when_it_falls_due() {
    let key = || vec![text(KEY_COLUMN), text("Toy"), text("expire")];
    let clear = |p: [&str; 3]| Statement {
        text: format!(
            r#"DELETE FROM "order_deadline_queue" WHERE "row_key" = {} AND "use_id" = {} AND "name" = {}"#,
            p[0], p[1], p[2]
        ),
        values: key(),
        expect: None,
        check: false,
    };
    let armed = |text: &str, payload: Bind| Statement {
        text: text.into(),
        values: [key(), vec![Bind::Int(3000), payload]].concat(),
        expect: None,
        check: false,
    };
    assert_eq!(
        unit_of(Dialect::Postgres, "arm", json!({"payload": {"n": 1}})),
        [
            toy_row_write(Dialect::Postgres),
            clear(["$1", "$2", "$3"]),
            armed(
                r#"INSERT INTO "order_deadline_queue" ("row_key", "use_id", "name", "at", "payload", "due_at") VALUES ($1, $2, $3, $4, $5::jsonb, to_timestamp($4::bigint / 1000.0))"#,
                text(r#"{"n":1}"#)
            )
        ]
    );
    assert_eq!(
        unit_of(Dialect::Sqlite, "arm", json!({}))[1..],
        [
            clear(["?1", "?2", "?3"]),
            armed(
                r#"INSERT INTO "order_deadline_queue" ("row_key", "use_id", "name", "at", "payload", "due_at") VALUES (?1, ?2, ?3, ?4, ?5, strftime('%Y-%m-%dT%H:%M:%fZ', ?4 / 1000.0, 'unixepoch'))"#,
                Bind::Null
            )
        ]
    );
    // A string payload binds as its JSON text.
    assert_eq!(
        unit_of(Dialect::Postgres, "arm", json!({"payload": "soon"}))[2].values[4],
        text(r#""soon""#)
    );
    assert_eq!(
        unit_of(Dialect::Postgres, "clear", json!({})),
        [toy_row_write(Dialect::Postgres), clear(["$1", "$2", "$3"])]
    );
}

#[test]
fn a_delete_deletes_the_rows_messages_and_a_hard_one_its_sub_table_rows_after_its_fenced_write() {
    let core = toy();
    let layout = Layout::new([toy_table()]);
    let host = Host::new(&core, &layout);
    let store = MemoryStore::new(Dialect::Postgres, Some(draft_row_of_toy()));
    block(run::run_delete(
        &host,
        &store,
        "Order",
        KEY,
        alice(),
        &ExecuteOptions::default(),
    ))
    .expect("deleted");
    let any = |text: &str| Statement {
        text: text.into(),
        values: vec![self::text(KEY_COLUMN)],
        expect: None,
        check: false,
    };
    assert_eq!(
        store.applied(),
        [[
            write(
                r#"DELETE FROM "order" WHERE "id" = $1 AND "_version" = $2"#,
                vec![text(KEY_COLUMN), Bind::Int(1)]
            ),
            any(r#"DELETE FROM "order_deadline_queue" WHERE "row_key" = $1"#),
            any(r#"DELETE FROM "order_toy_notes" WHERE "order_id" = $1"#),
        ]]
    );

    // A soft delete keeps the row's sub-table rows, its state for a
    // restore, and deletes its messages.
    let store = MemoryStore::new(Dialect::Postgres, Some(draft_row_of_toy()));
    let options = ExecuteOptions {
        own_columns: None,
        soft_delete: Some(own(json!({"deleted_at": "1970-01-01T00:00:09.000Z"}))),
        ..ExecuteOptions::default()
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
    let applied = store.applied();
    assert_eq!(applied[0].len(), 2, "{applied:?}");
    assert_eq!(
        applied[0][1],
        any(r#"DELETE FROM "order_deadline_queue" WHERE "row_key" = $1"#)
    );
}

/// The toy's deadline `expire` of the row, armed at 3000 with `payload`.
fn expire(payload: Value) -> Deadline {
    Deadline {
        key: KEY.into(),
        use_id: "Toy".into(),
        name: "expire".into(),
        at: 3000,
        payload,
    }
}

fn claim() -> Claim {
    Claim {
        id: "6c1f6f1e-6d3a-4f39-9a2b-1d2e3f4a5b6c".into(),
        token: "0b7c5e1a-2f3d-4c5b-8a9e-7d6c5b4a3f2e".into(),
    }
}

/// The spend of a claim, by its id and token, held to one row.
fn claim_spend() -> Statement {
    write(
        r#"DELETE FROM "order_deadline_queue" WHERE "id" = $1::uuid AND "claim_token" = $2::uuid AND "state" = 'claimed'"#,
        vec![text(&claim().id), text(&claim().token)],
    )
}

#[test]
fn a_claimed_deadline_runs_at_its_time_and_spends_its_claim_after_the_row_write_and_before_the_deadline_it_arms_again(
) {
    let core = toy();
    let layout = Layout::new([toy_table()]);
    // The host's clock lags the time the deadline fell due at.
    let host = Host::new(&core, &layout).with_clock(|| 2500);
    let store = MemoryStore::new(Dialect::Postgres, Some(draft_row_of_toy()));
    let run = block(run::run_deadline(
        &host,
        &store,
        "Order",
        &expire(json!({"again": true})),
        Caller::new("api/Expiry", Vec::<String>::new()),
        Some(&claim()),
    ))
    .expect("ran");
    let DeadlineRun::Ran(executed) = &run else {
        panic!("not run: {run:?}");
    };
    assert_eq!((run.ran(), run.why()), (true, ""));
    // It ran at the time it fell due, not the host's.
    assert_eq!(executed.changes.deadlines.len(), 1);
    let applied = store.applied();
    assert_eq!(applied.len(), 1);
    let unit = &applied[0];
    assert_eq!(
        unit[0],
        write(
            r#"UPDATE "order" SET "toy_seen" = $1, "_version" = "_version" + 1 WHERE "id" = $2 AND "_version" = $3"#,
            vec![text("fired expire"), text(KEY_COLUMN), Bind::Int(1)]
        )
    );
    assert_eq!(unit[1], claim_spend());
    assert!(unit[2]
        .text
        .starts_with(r#"DELETE FROM "order_deadline_queue" WHERE "row_key""#));
    assert!(unit[3]
        .text
        .starts_with(r#"INSERT INTO "order_deadline_queue""#));
    assert_eq!(
        unit[3].values[3],
        Bind::Int(4000),
        "armed a second after 3000"
    );
    assert_eq!(unit.len(), 4);
    // The claim needs no read of the arming: the spend in the unit is the
    // check.
    assert!(!store
        .queries()
        .iter()
        .any(|(text, _)| text.contains("order_deadline_queue")));
}

#[test]
fn a_claimed_deadline_whose_unit_did_not_apply_reads_its_claim_and_runs_nothing_once_it_is_gone() {
    let core = toy();
    let layout = Layout::new([toy_table()]);
    let host = Host::new(&core, &layout).with_clock(|| 9000);
    let held = r#"SELECT 1 FROM "order_deadline_queue" WHERE "id" = $1::uuid AND "claim_token" = $2::uuid AND "state" = 'claimed'"#;
    let lost = MemoryStore::new(Dialect::Postgres, Some(draft_row_of_toy()))
        .applying(&[false])
        .responding(held, Vec::new());
    let run = block(run::run_deadline(
        &host,
        &lost,
        "Order",
        &expire(Value::Null),
        Caller::default(),
        Some(&claim()),
    ))
    .expect("ran nothing");
    assert!(matches!(run, DeadlineRun::ClaimLost), "{run:?}");
    assert_eq!(run.why(), "claim lost");
    assert_eq!(lost.applied().len(), 1);
    assert!(lost.queries().contains(&(
        held.to_owned(),
        vec![text(&claim().id), text(&claim().token)]
    )));

    // A claim still held when its row moved runs again from fresh state.
    let moved = MemoryStore::new(Dialect::Postgres, Some(draft_row_of_toy()))
        .applying(&[false, true])
        .responding(held, vec![vec![Cell::Int(1)]]);
    let run = block(run::run_deadline(
        &host,
        &moved,
        "Order",
        &expire(Value::Null),
        Caller::default(),
        Some(&claim()),
    ))
    .expect("ran");
    let DeadlineRun::Ran(executed) = run else {
        panic!("not run");
    };
    assert_eq!(executed.attempts, 2);
}

#[test]
fn a_deadline_whose_row_is_gone_spends_its_arming_in_a_unit_of_its_own_and_runs_nothing() {
    let core = toy();
    let layout = Layout::new([toy_table()]);
    let host = Host::new(&core, &layout).with_clock(|| 9000);
    let gone = MemoryStore::new(Dialect::Postgres, None);
    let run = block(run::run_deadline(
        &host,
        &gone,
        "Order",
        &expire(Value::Null),
        Caller::default(),
        Some(&claim()),
    ))
    .expect("ran nothing");
    let DeadlineRun::Gone(refusal) = &run else {
        panic!("not gone: {run:?}");
    };
    assert_eq!(refusal.code, Code::NotFound);
    assert_eq!(run.why(), "gone");
    assert_eq!(gone.applied(), [[claim_spend()]]);

    // A use the layout no longer lists is gone with its row there.
    let lapsed = MemoryStore::new(Dialect::Postgres, Some(draft_row_of_toy()));
    let deadline = Deadline {
        use_id: "Lapsed".into(),
        ..expire(Value::Null)
    };
    let run = block(run::run_deadline(
        &host,
        &lapsed,
        "Order",
        &deadline,
        Caller::default(),
        Some(&claim()),
    ))
    .expect("ran nothing");
    assert!(matches!(run, DeadlineRun::Gone(_)), "{run:?}");
    assert_eq!(lapsed.applied(), [[claim_spend()]]);

    // A not_found while the row and the use are there is of something a
    // rule reached: the run's refusal, nothing spent, so the worker fails
    // the message and it falls due again.
    let there = MemoryStore::new(Dialect::Postgres, Some(draft_row_of_toy()));
    let refused = block(run::run_deadline(
        &host,
        &there,
        "Order",
        &expire(json!({"reach": true})),
        Caller::default(),
        Some(&claim()),
    ));
    let Err(Error::Refused(refusal)) = &refused else {
        panic!("not refused: {refused:?}");
    };
    assert_eq!(
        (refusal.code, refusal.message.as_str()),
        (Code::NotFound, "Shelf 7 not found")
    );
    assert!(there.applied().is_empty());

    // A spend that finds the claim gone ran nothing either.
    let lost = MemoryStore::new(Dialect::Postgres, None).applying(&[false]);
    let run = block(run::run_deadline(
        &host,
        &lost,
        "Order",
        &expire(Value::Null),
        Caller::default(),
        Some(&claim()),
    ))
    .expect("ran nothing");
    assert!(matches!(run, DeadlineRun::ClaimLost), "{run:?}");
}

#[test]
fn a_deadline_with_no_claim_runs_only_while_it_is_armed_at_its_time_and_spends_that_arming() {
    let core = toy();
    let layout = Layout::new([toy_table()]);
    let host = Host::new(&core, &layout).with_clock(|| 9000);
    let armed = r#"SELECT "at", "payload" FROM "order_deadline_queue" WHERE "row_key" = ?1 AND "use_id" = ?2 AND "name" = ?3 AND "state" IN ('ready', 'claimed')"#;
    let deadline = |at: i64, now: i64| {
        Command::new(
            alice(),
            now,
            Action::Deadline {
                key: KEY.into(),
                use_id: "Toy".into(),
                name: "expire".into(),
                at,
                payload: Value::Null,
            },
        )
        .with_request("r9")
    };
    let store = |rows: Vec<Vec<Cell>>| {
        MemoryStore::new(Dialect::Sqlite, Some(draft_row_of_toy())).responding(armed, rows)
    };

    let none = store(Vec::new());
    let refused = refusal(block(run::execute(
        &host,
        &none,
        "Order",
        deadline(3000, 9000),
        &ExecuteOptions::default(),
    )));
    assert_eq!(
        refused,
        Refusal::not_found(format!(
            "Order {KEY} has no deadline Toy.expire armed at 3000"
        ))
    );
    assert!(none.applied().is_empty());
    assert_eq!(
        none.queries()[0],
        (
            armed.to_owned(),
            vec![text(KEY_COLUMN), text("Toy"), text("expire")]
        )
    );
    // Armed at another time, it is not armed at this one.
    let moved = store(vec![vec![Cell::Int(4000), Cell::Null]]);
    let refused = refusal(block(run::execute(
        &host,
        &moved,
        "Order",
        deadline(3000, 9000),
        &ExecuteOptions::default(),
    )));
    assert_eq!(refused.code, Code::NotFound);
    // Armed at its time, and not yet due.
    let early = store(vec![vec![Cell::Int(3000), Cell::Null]]);
    let refused = refusal(block(run::execute(
        &host,
        &early,
        "Order",
        deadline(3000, 2000),
        &ExecuteOptions::default(),
    )));
    assert_eq!(
        refused,
        Refusal::invalid_argument(format!(
            "deadline Toy.expire of Order {KEY} falls due at 3000, after 2000"
        ))
    );

    let due = store(vec![vec![Cell::Int(3000), Cell::Text("null".into())]]);
    let executed = block(run::execute(
        &host,
        &due,
        "Order",
        deadline(3000, 9000),
        &ExecuteOptions::default(),
    ))
    .expect("ran");
    assert_eq!(executed.key.as_deref(), Some(KEY));
    let unit = &due.applied()[0];
    assert_eq!(
        unit[1],
        write(
            r#"DELETE FROM "order_deadline_queue" WHERE "row_key" = ?1 AND "use_id" = ?2 AND "name" = ?3 AND "at" = ?4 AND "state" IN ('ready', 'claimed')"#,
            vec![
                text(KEY_COLUMN),
                text("Toy"),
                text("expire"),
                Bind::Int(3000)
            ]
        )
    );

    // A table the layout gives no deadline queue runs none.
    let bare = Layout::new([Table {
        deadlines: None,
        ..toy_table()
    }]);
    let refused = refusal(block(run::execute(
        &Host::new(&core, &bare),
        &store(Vec::new()),
        "Order",
        deadline(3000, 9000),
        &ExecuteOptions::default(),
    )));
    assert_eq!(
        refused,
        Refusal::internal("rules: a deadline Toy.expire of Order ran, and the behaviors' layout gives Order no deadline queue")
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
        ..ExecuteOptions::default()
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
/// and [`toy_table`]'s as `toy`, with its sub-table `order_toy_notes` and
/// its deadline queue `order_deadline_queue` as sqlgen writes a queue's,
/// its live messages unique by row, use and name (D103), each table with
/// sqlgen's `@optimistic` trigger.
#[cfg(any(feature = "postgres", feature = "sqlite"))]
pub(crate) fn ddl(postgres: bool) -> Vec<String> {
    let (timestamp, version, uuid, now, json) = if postgres {
        (
            "TIMESTAMPTZ",
            "BIGINT DEFAULT 1 NOT NULL",
            "UUID DEFAULT gen_random_uuid()",
            "CURRENT_TIMESTAMP",
            "JSONB",
        )
    } else {
        (
            "TEXT",
            "INTEGER DEFAULT 1 NOT NULL",
            "TEXT DEFAULT (lower(hex(randomblob(16))))",
            "(strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))",
            "TEXT",
        )
    };
    let mut ddl = vec![
        format!("CREATE TABLE \"order\" (id TEXT PRIMARY KEY, title TEXT, status TEXT, status_entered_at {timestamp}, _version {version})"),
        format!("CREATE TABLE ticket (id TEXT PRIMARY KEY, title TEXT, status TEXT, status_entered_at {timestamp}, deleted_at {timestamp}, _version {version})"),
        format!("CREATE TABLE toy (id TEXT PRIMARY KEY, title TEXT, toy_seen TEXT, _version {version})"),
        format!("CREATE TABLE order_toy_notes (order_id TEXT NOT NULL, key TEXT NOT NULL, body TEXT, at {timestamp}, PRIMARY KEY (order_id, key))"),
        format!("CREATE TABLE order_deadline_queue (id {uuid} NOT NULL PRIMARY KEY, row_key TEXT NOT NULL, use_id TEXT NOT NULL, name TEXT NOT NULL, at BIGINT NOT NULL, payload {json}, state TEXT DEFAULT 'ready' NOT NULL, attempts INTEGER DEFAULT 0 NOT NULL, due_at {timestamp} DEFAULT {now} NOT NULL, claim_expires_at {timestamp}, claim_token {}, last_error TEXT)", if postgres { "UUID" } else { "TEXT" }),
        "CREATE UNIQUE INDEX order_deadline_queue_live ON order_deadline_queue (row_key, use_id, name) WHERE state IN ('ready', 'claimed')".to_owned(),
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

// ---- the change command (D103) ----

/// Probe: a soft-deletable table of a column of each kind the adapter
/// converts (a title, a timestamp, a UUID, a boolean and JSON) under the
/// toy, its sub-table `probe_toy_notes`, its deadline queue and its change
/// queue, as the generator writes a table its behaviors hear.
fn probe_table() -> Table {
    Table {
        type_name: "Probe".into(),
        table: "probe".into(),
        key: Column::new("id", "id", "Identity.UUID"),
        version: "_version".into(),
        live: Some(Live {
            column: "deleted_at".into(),
        }),
        own: vec![
            Column::new("title", "title", "string"),
            Column::new("at", "at", "Temporal.DateTime").converted(Convert::EpochMillis),
            Column::new("ownerId", "owner_id", "Identity.UUID"),
            Column::new("done", "done", "boolean"),
            Column::new("data", "data", "Generic.JSON"),
        ],
        audit: Vec::new(),
        uses: vec![Use {
            id: "Toy".into(),
            behavior: "Toy".into(),
            member: "toy".into(),
            anchor: None,
            fields: vec![owned_field("seen", "toySeen", "toy_seen", "string")],
            operations: Vec::new(),
            tables: vec![SubTable {
                name: "notes".into(),
                table: "probe_toy_notes".into(),
                row: "probe_id".into(),
                key: owned_field("key", "key", "key", "string"),
                fields: Vec::new(),
            }],
        }],
        deadlines: Some(Deadlines {
            queue: "ProbeDeadline".into(),
            table: "probe_deadline_queue".into(),
            row_key: "row_key".into(),
            use_id: "use_id".into(),
            name: "name".into(),
            at: "at".into(),
            payload: "payload".into(),
        }),
        changes: Some(Changes {
            queue: "ProbeChange".into(),
            table: "probe_change_queue".into(),
            row_key: "row_key".into(),
            write: "write".into(),
            at: "at".into(),
            before: "before".into(),
            after: "after".into(),
        }),
        spec: TypeSpec::new("Probe").with(UseSpec::on_type("Toy", None)),
    }
}

/// Probe's row as Postgres reads it: Desk, no time, no owner, not done,
/// no data, no mark, at version 1.
fn probe_row() -> Vec<Cell> {
    vec![
        Cell::Json(json!("Desk")),
        Cell::Null,
        Cell::Null,
        Cell::Json(json!(false)),
        Cell::Null,
        Cell::Null,
        Cell::Int(1),
    ]
}

/// A change of Probe's row, as its trigger wrote it.
fn probe_change(write: &str, before: Value, after: Value) -> TableChange {
    TableChange {
        key: KEY_COLUMN.into(),
        write: write.into(),
        at: 5000,
        before,
        after,
    }
}

/// Runs a change of Probe through the loop on a store and returns what it
/// came to.
fn run_probe_change(
    store: &MemoryStore,
    change: &TableChange,
    claim: Option<&Claim>,
) -> Result<ChangeRun, Error> {
    let core = toy();
    let layout = Layout::new([toy_table(), probe_table()]);
    let host = Host::new(&core, &layout).with_clock(|| 6000);
    block(run::run_change(
        &host,
        store,
        "Probe",
        change,
        Caller::new("api/ProbeWorker", Vec::<String>::new()),
        claim,
    ))
}

/// The change the toy heard, from the mark its unit set on the row (its
/// first statement's first value).
fn heard_of(store: &MemoryStore) -> Value {
    let applied = store.applied();
    let Some(Bind::Text(mark)) = applied[0][0].values.first() else {
        panic!("no mark: {applied:?}");
    };
    serde_json::from_str(mark).expect("the mark is JSON")
}

#[test]
fn a_change_maps_its_images_by_the_layout_converting_each_column_as_its_dialect_reads_it() {
    // Postgres: each value as jsonb_build_object wrote it, a timestamp with
    // the session's offset and a UUID hyphenated; a null, and a column the
    // layout does not map, left out.
    let store = MemoryStore::new(Dialect::Postgres, Some(probe_row()));
    let change = probe_change(
        "update",
        json!({"title": "Desk", "at": "1970-01-01T05:30:01.234+05:30", "owner_id": KEY_COLUMN, "done": false, "data": {"a": [1]}, "toy_seen": null, "_version": 1}),
        json!({"title": "Lamp", "at": null, "owner_id": KEY_COLUMN, "done": true, "data": null, "toy_seen": "x"}),
    );
    let run = run_probe_change(&store, &change, None).expect("ran");
    assert_eq!((run.ran(), run.why()), (true, ""));
    let ChangeRun::Ran(executed) = run else {
        panic!("not run");
    };
    assert_eq!(executed.key.as_deref(), Some(KEY));
    assert_eq!(
        heard_of(&store),
        json!({
            "write": "update",
            "at": 5000,
            "before": {"own": {"title": "Desk", "at": 1234, "ownerId": KEY, "done": false, "data": {"a": [1]}}, "owned": {}},
            "after": {"own": {"title": "Lamp", "ownerId": KEY, "done": true}, "owned": {"Toy": {"seen": "x"}}}
        })
    );
    // The row's write is held to its version, and nothing is spent.
    let applied = store.applied();
    assert_eq!(applied.len(), 1);
    assert_eq!(
        applied[0][0].text,
        r#"UPDATE "probe" SET "toy_seen" = $1, "_version" = "_version" + 1 WHERE "id" = $2 AND "_version" = $3 AND "deleted_at" IS NULL"#
    );
    assert_eq!(applied[0].len(), 1);

    // SQLite: an image is the text json_object wrote, each value its
    // storage class's: a boolean 0 or 1, a JSON column its text; an insert
    // has no before.
    let store = MemoryStore::new(
        Dialect::Sqlite,
        Some(vec![
            Cell::Text("Desk".into()),
            Cell::Null,
            Cell::Null,
            Cell::Int(1),
            Cell::Null,
            Cell::Null,
            Cell::Int(1),
        ]),
    );
    let after = json!({"title": "Desk", "at": "1970-01-01T00:00:01.234Z", "owner_id": KEY_COLUMN, "done": 1, "data": "{\"a\":1}", "toy_seen": null});
    let change = probe_change("insert", Value::Null, Value::String(after.to_string()));
    run_probe_change(&store, &change, None).expect("ran");
    assert_eq!(
        heard_of(&store),
        json!({
            "write": "insert",
            "at": 5000,
            "after": {"own": {"title": "Desk", "at": 1234, "ownerId": KEY, "done": true, "data": {"a": 1}}, "owned": {}}
        })
    );

    // A write the trigger never writes, and an image that is no object,
    // are the adapter's internal refusals, nothing applied.
    let store = MemoryStore::new(Dialect::Postgres, Some(probe_row()));
    let refused = refusal(run_probe_change(
        &store,
        &probe_change("upsert", Value::Null, json!({})),
        None,
    ));
    assert_eq!(
        refused,
        Refusal::internal(format!("rules: Probe's change of {KEY} is a write of upsert, which is no insert, update or delete"))
    );
    let refused = refusal(run_probe_change(
        &store,
        &probe_change("update", json!([1]), json!({})),
        None,
    ));
    assert_eq!(
        refused,
        Refusal::internal("rules: Probe's change holds an image that is no object: [1]")
    );
    assert!(store.applied().is_empty());
}

/// The spend of Probe's claimed change, by its id and token, held to one
/// row.
fn change_spend() -> Statement {
    write(
        r#"DELETE FROM "probe_change_queue" WHERE "id" = $1::uuid AND "claim_token" = $2::uuid AND "state" = 'claimed'"#,
        vec![text(&claim().id), text(&claim().token)],
    )
}

#[test]
fn a_claimed_change_spends_its_message_after_the_row_writes_and_one_whose_claim_is_lost_runs_nothing(
) {
    let change = probe_change("update", json!({"title": "Desk"}), json!({"title": "Lamp"}));
    let store = MemoryStore::new(Dialect::Postgres, Some(probe_row()));
    let run = run_probe_change(&store, &change, Some(&claim())).expect("ran");
    assert!(run.ran(), "{run:?}");
    let applied = store.applied();
    assert_eq!(applied.len(), 1);
    assert!(applied[0][0].text.starts_with(r#"UPDATE "probe""#));
    assert_eq!(applied[0][1], change_spend());
    assert_eq!(applied[0].len(), 2);

    // A unit that did not apply reads the claim, and one that is gone ran
    // nothing.
    let held = r#"SELECT 1 FROM "probe_change_queue" WHERE "id" = $1::uuid AND "claim_token" = $2::uuid AND "state" = 'claimed'"#;
    let lost = MemoryStore::new(Dialect::Postgres, Some(probe_row()))
        .applying(&[false])
        .responding(held, Vec::new());
    let run = run_probe_change(&lost, &change, Some(&claim())).expect("ran nothing");
    assert!(matches!(run, ChangeRun::ClaimLost), "{run:?}");
    assert_eq!(run.why(), "claim lost");
    assert!(lost.queries().contains(&(
        held.to_owned(),
        vec![text(&claim().id), text(&claim().token)]
    )));

    // One still held when its row moved runs again from fresh state.
    let moved = MemoryStore::new(Dialect::Postgres, Some(probe_row()))
        .applying(&[false, true])
        .responding(held, vec![vec![Cell::Int(1)]]);
    let ChangeRun::Ran(executed) = run_probe_change(&moved, &change, Some(&claim())).expect("ran")
    else {
        panic!("not run");
    };
    assert_eq!(executed.attempts, 2);

    // A change whose hooks write nothing spends its message alone; on
    // SQLite by its id and token as they are.
    let core = workflow();
    let layout = Layout::new([Table {
        changes: probe_table().changes,
        ..order_table()
    }]);
    let host = Host::new(&core, &layout);
    let store = MemoryStore::new(Dialect::Sqlite, Some(draft_row()));
    let run = block(run::run_change(
        &host,
        &store,
        "Order",
        &probe_change("update", json!({}), json!({})),
        alice(),
        Some(&claim()),
    ))
    .expect("ran");
    let ChangeRun::Ran(executed) = run else {
        panic!("not run");
    };
    assert!(executed.changes.rows.is_empty());
    assert_eq!(
        store.applied(),
        [[write(
            r#"DELETE FROM "probe_change_queue" WHERE "id" = ?1 AND "claim_token" = ?2 AND "state" = 'claimed'"#,
            vec![text(&claim().id), text(&claim().token)]
        )]]
    );

    // A claimed change of a table the layout gives no change queue is
    // internal.
    let bare = Layout::new([order_table()]);
    let refused = refusal(block(run::run_change(
        &Host::new(&core, &bare),
        &MemoryStore::new(Dialect::Postgres, Some(draft_row())),
        "Order",
        &probe_change("update", json!({}), json!({})),
        alice(),
        Some(&claim()),
    )));
    assert_eq!(
        refused,
        Refusal::internal("rules: a change of Order ran with a claim of its message, and the behaviors' layout gives Order no change queue")
    );
}

#[test]
fn a_row_a_change_finds_gone_has_its_deadlines_and_sub_table_rows_deleted_unless_a_row_of_its_key_is_there(
) {
    let store = MemoryStore::new(Dialect::Postgres, None);
    let change = probe_change("delete", json!({"title": "Desk"}), Value::Null);
    let ChangeRun::Ran(executed) = run_probe_change(&store, &change, Some(&claim())).expect("ran")
    else {
        panic!("not run");
    };
    assert_eq!(
        executed.changes.rows,
        [RowWrite::Gone {
            type_name: "Probe".into(),
            key: KEY.into()
        }]
    );
    let any = |text: &str| Statement {
        text: text.into(),
        values: vec![self::text(KEY_COLUMN)],
        expect: None,
        check: false,
    };
    assert_eq!(
        store.applied(),
        [[
            any(
                r#"DELETE FROM "probe_deadline_queue" WHERE "row_key" = $1 AND NOT EXISTS (SELECT 1 FROM "probe" WHERE "id" = $1 AND "deleted_at" IS NULL)"#
            ),
            any(
                r#"DELETE FROM "probe_toy_notes" WHERE "probe_id" = $1 AND NOT EXISTS (SELECT 1 FROM "probe" WHERE "id" = $1)"#
            ),
            change_spend(),
        ]]
    );

    // On SQLite, by ?1.
    let store = MemoryStore::new(Dialect::Sqlite, None);
    run_probe_change(&store, &change, None).expect("ran");
    assert_eq!(
        store.applied()[0][0].text,
        r#"DELETE FROM "probe_deadline_queue" WHERE "row_key" = ?1 AND NOT EXISTS (SELECT 1 FROM "probe" WHERE "id" = ?1 AND "deleted_at" IS NULL)"#
    );
}

/// The Postgres read of a page of Probe's rows whose owner is the key.
const LINKED: &str = r#"SELECT to_jsonb("id") AS k, to_jsonb("title") AS c0, to_jsonb("at") AS c1, to_jsonb("owner_id") AS c2, to_jsonb("done") AS c3, to_jsonb("data") AS c4, to_jsonb("toy_seen") AS c5, "_version" AS v FROM "probe" WHERE "owner_id" = $1 AND "deleted_at" IS NULL"#;

/// A row of Probe's as the linked read reads it: its key hyphenated, then
/// its row.
fn linked_row(n: u128) -> Vec<Cell> {
    let mut cells = vec![Cell::Json(json!(crate::convert::hyphenate(n)))];
    cells.extend(probe_row());
    cells
}

/// Runs the toy's linked read of Probe from Order's row with `params`.
fn linked(store: &MemoryStore, params: Value) -> Result<Value, Error> {
    let core = toy();
    let layout = Layout::new([toy_table(), probe_table()]);
    let host = Host::new(&core, &layout);
    block(run::execute(
        &host,
        store,
        "Order",
        toy_op("linked", params),
        &ExecuteOptions::default(),
    ))
    .map(|executed| executed.result)
}

#[test]
fn a_linked_read_selects_a_page_of_the_live_rows_whose_field_holds_the_key_in_key_order_and_whether_more_follow(
) {
    let store = MemoryStore::new(Dialect::Postgres, Some(draft_row_of_toy()))
        .responding(LINKED, vec![linked_row(1), linked_row(2)]);
    let page = linked(
        &store,
        json!({"type": "Probe", "field": "ownerId", "key": KEY}),
    )
    .expect("read");
    let row = json!({"own": {"title": "Desk", "done": false}, "owned": {}, "version": 1});
    assert_eq!(
        page,
        json!({"rows": [{"key": "1", "row": row}, {"key": "2", "row": row}], "more": false})
    );
    assert!(store.queries().contains(&(
        format!(r#"{LINKED} ORDER BY "id" LIMIT 501"#),
        vec![text(KEY_COLUMN)]
    )));

    // After a key, bound as the key column binds one; a page of 500 of the
    // 501 rows the read gives, and more follow.
    let rows = (1..=501).map(linked_row).collect();
    let store =
        MemoryStore::new(Dialect::Postgres, Some(draft_row_of_toy())).responding(LINKED, rows);
    let page = linked(
        &store,
        json!({"type": "Probe", "field": "ownerId", "key": KEY, "after": "1"}),
    )
    .expect("read");
    assert_eq!(page["rows"].as_array().map(Vec::len), Some(500));
    assert_eq!(page["more"], json!(true));
    assert!(store.queries().contains(&(
        format!(r#"{LINKED} AND "id" > $2 ORDER BY "id" LIMIT 501"#),
        vec![
            text(KEY_COLUMN),
            text("00000000-0000-0000-0000-000000000001")
        ]
    )));

    // SQLite selects each column as it is stored.
    let store = MemoryStore::new(Dialect::Sqlite, Some(draft_row_of_toy()))
        .responding(r#"SELECT "id" AS k"#, Vec::new());
    linked(
        &store,
        json!({"type": "Probe", "field": "ownerId", "key": KEY}),
    )
    .expect("read");
    assert!(store.queries().iter().any(|(text, _)| text
        == r#"SELECT "id" AS k, "title" AS c0, "at" AS c1, "owner_id" AS c2, "done" AS c3, "data" AS c4, "toy_seen" AS c5, "_version" AS v FROM "probe" WHERE "owner_id" = ?1 AND "deleted_at" IS NULL ORDER BY "id" LIMIT 501"#));

    // A type the layout lacks has no rows; a field it has no own column for
    // is internal, and so is a linked read under a lock a store fences.
    let store = MemoryStore::new(Dialect::Postgres, Some(draft_row_of_toy()));
    assert_eq!(
        linked(
            &store,
            json!({"type": "Ghost", "field": "ownerId", "key": KEY})
        )
        .expect("read"),
        json!({"rows": [], "more": false})
    );
    assert_eq!(
        refusal(linked(
            &store,
            json!({"type": "Probe", "field": "nope", "key": KEY})
        )),
        Refusal::internal("rules: Probe has no own field nope in the behaviors' layout")
    );
    let fenced = MemoryStore::new(Dialect::Sqlite, Some(draft_row_of_toy())).fenced();
    assert_eq!(
        refusal(linked(
            &fenced,
            json!({"type": "Probe", "field": "ownerId", "key": KEY, "lock": true})
        )),
        Refusal::internal("rules: a read of the rows that link to a row under a lock has no check on SQLite or D1 yet (D103)")
    );
}

/// The Postgres read of a page of Probe's rows whose Toy's seen holds a
/// value (D104).
const HOLDING: &str = r#"SELECT to_jsonb("id") AS k, to_jsonb("title") AS c0, to_jsonb("at") AS c1, to_jsonb("owner_id") AS c2, to_jsonb("done") AS c3, to_jsonb("data") AS c4, to_jsonb("toy_seen") AS c5, "_version" AS v FROM "probe" WHERE "toy_seen" = $1 AND "deleted_at" IS NULL"#;

/// Runs the toy's holding read of Probe from Order's row with `params`.
fn holding(store: &MemoryStore, params: Value) -> Result<Value, Error> {
    let core = toy();
    let layout = Layout::new([toy_table(), probe_table()]);
    let host = Host::new(&core, &layout);
    block(run::execute(
        &host,
        store,
        "Order",
        toy_op("holding", params),
        &ExecuteOptions::default(),
    ))
    .map(|executed| executed.result)
}

#[test]
fn a_holding_read_selects_a_page_of_the_live_rows_whose_use_field_holds_the_value_by_its_column() {
    let store = MemoryStore::new(Dialect::Postgres, Some(draft_row_of_toy()))
        .responding(HOLDING, vec![linked_row(1), linked_row(2)]);
    let page = holding(
        &store,
        json!({"type": "Probe", "use": "Toy", "field": "seen", "value": "wren"}),
    )
    .expect("read");
    let row = json!({"own": {"title": "Desk", "done": false}, "owned": {}, "version": 1});
    assert_eq!(
        page,
        json!({"rows": [{"key": "1", "row": row}, {"key": "2", "row": row}], "more": false})
    );
    assert!(store.queries().contains(&(
        format!(r#"{HOLDING} ORDER BY "id" LIMIT 501"#),
        vec![text("wren")]
    )));

    // After a key, bound as the key column binds one; a page of 500 of the
    // 501 rows the read gives, and more follow.
    let rows = (1..=501).map(linked_row).collect();
    let store =
        MemoryStore::new(Dialect::Postgres, Some(draft_row_of_toy())).responding(HOLDING, rows);
    let page = holding(
        &store,
        json!({"type": "Probe", "use": "Toy", "field": "seen", "value": "wren", "after": "1"}),
    )
    .expect("read");
    assert_eq!(page["rows"].as_array().map(Vec::len), Some(500));
    assert_eq!(page["more"], json!(true));
    assert!(store.queries().contains(&(
        format!(r#"{HOLDING} AND "id" > $2 ORDER BY "id" LIMIT 501"#),
        vec![text("wren"), text("00000000-0000-0000-0000-000000000001")]
    )));

    // SQLite selects each column as it is stored.
    let store = MemoryStore::new(Dialect::Sqlite, Some(draft_row_of_toy()))
        .responding(r#"SELECT "id" AS k"#, Vec::new());
    holding(
        &store,
        json!({"type": "Probe", "use": "Toy", "field": "seen", "value": "wren"}),
    )
    .expect("read");
    assert!(store.queries().iter().any(|(text, _)| text
        == r#"SELECT "id" AS k, "title" AS c0, "at" AS c1, "owner_id" AS c2, "done" AS c3, "data" AS c4, "toy_seen" AS c5, "_version" AS v FROM "probe" WHERE "toy_seen" = ?1 AND "deleted_at" IS NULL ORDER BY "id" LIMIT 501"#));

    // A type the layout lacks has no rows; a use or a field the layout
    // lacks is internal, and so is a holding read under a lock a store
    // fences.
    let store = MemoryStore::new(Dialect::Postgres, Some(draft_row_of_toy()));
    assert_eq!(
        holding(
            &store,
            json!({"type": "Ghost", "use": "Toy", "field": "seen", "value": "wren"})
        )
        .expect("read"),
        json!({"rows": [], "more": false})
    );
    assert_eq!(
        refusal(holding(
            &store,
            json!({"type": "Probe", "use": "Lease", "field": "holder", "value": "wren"})
        )),
        Refusal::internal("rules: Probe has no use Lease in the behaviors' layout")
    );
    assert_eq!(
        refusal(holding(
            &store,
            json!({"type": "Probe", "use": "Toy", "field": "holder", "value": "wren"})
        )),
        Refusal::internal(
            "rules: Probe's use Toy (Toy) has no field holder in the behaviors' layout"
        )
    );
    let fenced = MemoryStore::new(Dialect::Sqlite, Some(draft_row_of_toy())).fenced();
    assert_eq!(
        refusal(holding(
            &fenced,
            json!({"type": "Probe", "use": "Toy", "field": "seen", "value": "wren", "lock": true})
        )),
        Refusal::internal("rules: a read of the rows whose use's field holds a value under a lock has no check on SQLite or D1 yet (D103)")
    );
}

/// Preconditions of `entries`, by use id.
fn preconditions(entries: Value) -> ExecuteOptions {
    ExecuteOptions {
        preconditions: own(entries),
        ..ExecuteOptions::default()
    }
}

#[test]
fn the_callers_preconditions_reach_the_command_and_its_uses_guard_and_a_malformed_one_is_refused_before_any_read(
) {
    let (core, layout) = (toy(), Layout::new([toy_table()]));
    let host = Host::new(&core, &layout);
    let update = |store: &MemoryStore, options: &ExecuteOptions| {
        block(run::run_update(
            &host,
            store,
            "Order",
            KEY,
            own(json!({"title": "Lamp"})),
            alice(),
            options,
        ))
    };

    // An update presenting the toy's entry: its guard reads it.
    let store = MemoryStore::new(Dialect::Postgres, Some(draft_row_of_toy()));
    update(&store, &preconditions(json!({"Toy": {"stop": false}}))).expect("updated");
    assert_eq!(store.applied().len(), 1);
    let store = MemoryStore::new(Dialect::Postgres, Some(draft_row_of_toy()));
    let refused = refusal(update(
        &store,
        &preconditions(json!({"Toy": {"stop": true}})),
    ));
    assert_eq!(
        (refused.code, refused.class.as_str()),
        (Code::Vetoed, "BehaviorVetoError")
    );
    assert!(store.applied().is_empty());

    // An operation and a delete present them alike.
    let store = MemoryStore::new(Dialect::Postgres, Some(draft_row_of_toy()));
    let stop = preconditions(json!({"Toy": {"stop": true}}));
    let refused = refusal(block(run::run_operation(
        &host,
        &store,
        "Order",
        KEY,
        "Toy",
        "mint",
        json!({}),
        alice(),
        &stop,
    )));
    assert_eq!(refused.code, Code::Vetoed);
    let refused = refusal(block(run::run_delete(
        &host,
        &store,
        "Order",
        KEY,
        alice(),
        &stop,
    )));
    assert_eq!(refused.code, Code::Vetoed);
    assert!(store.applied().is_empty());

    // The options' entry stands in for the command's own of the same use,
    // and the command's other entries stay, as TypeScript's and Go's
    // adapters merge them.
    let store = MemoryStore::new(Dialect::Postgres, Some(draft_row_of_toy()));
    let fenced = command(Action::Update {
        key: KEY.into(),
        patch: own(json!({"title": "Lamp"})),
    })
    .with_preconditions(own(json!({"Toy": {"stop": true}})));
    block(run::execute(
        &host,
        &store,
        "Order",
        fenced,
        &preconditions(json!({"Toy": {"stop": false}})),
    ))
    .expect("the options' entry stands in for the command's");
    assert_eq!(store.applied().len(), 1);

    // A malformed entry is the core's PreconditionsError, refused before
    // the row is read.
    let store = MemoryStore::new(Dialect::Postgres, Some(draft_row_of_toy()));
    let refused = refusal(update(
        &store,
        &preconditions(json!({"Toy": {"stop": true, "at": 2}})),
    ));
    assert_eq!(
        (
            refused.code,
            refused.class.as_str(),
            refused.message.as_str()
        ),
        (
            Code::InvalidArgument,
            "PreconditionsError",
            "preconditions of Order: /Toy: must NOT have additional properties: at"
        )
    );
    assert!(store.queries().is_empty(), "{:?}", store.queries());

    // A create takes none: run, or prepared for an upsert's insert, it is
    // refused, never ignored.
    let options = CreateOptions {
        id: Some(KEY.into()),
        execute: preconditions(json!({"Toy": {}})),
        ..CreateOptions::default()
    };
    let store = MemoryStore::new(Dialect::Postgres, None);
    let created = block(run::run_create(
        &host,
        &store,
        "Order",
        own(json!({"title": "Desk"})),
        alice(),
        &options,
    ));
    let refused = refusal(created);
    assert_eq!(
        (refused.code, refused.class.as_str()),
        (Code::InvalidArgument, "PreconditionsError")
    );
    assert_eq!(
        refused.message,
        "preconditions of Order: a create takes no preconditions: an update, a delete or an operation of a row presents them"
    );
    let prepared = block(run::prepare_create(
        &host,
        &store,
        "Order",
        own(json!({"title": "Desk"})),
        alice(),
        &options,
    ));
    assert_eq!(refusal(prepared).class, "PreconditionsError");
    assert!(store.applied().is_empty());
}
