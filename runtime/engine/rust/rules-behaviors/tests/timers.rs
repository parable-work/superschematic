//! Timers on the pure core (D103), through the core's public API over a
//! source in memory that keeps rows, their deadlines' sub-table rows and
//! the deadlines the core arms, as a table's adapter keeps them: each
//! start (the create, an entry, a field as epoch milliseconds and as text),
//! then's four outcomes, until both ways, reset and snooze and their
//! permission, the Workflow a config reads (implied, named, several, none),
//! a config change that reconciles at the next write, the computed
//! deadlines a read shows, a write heard from the table's change queue (the
//! core's own, which changes nothing, a raw update of a field start and a
//! raw insert, which arm), a deadline that reconciles its row before it
//! fires, and the config and the operations' parameters held to the
//! declaration file's schemas. Where the engine's message names
//! a namespace or a version, the core's leaves them out and a host writes
//! them (the core's README).

use std::collections::BTreeMap;

use serde_json::{json, Map, Value};
use superschematic_rules::{
    merge_patch, Action, Answer, Behavior, Caller, ChangeSet, Command, ConfigError, ConfigTarget,
    Core, DeadlineChange, Declaration, FieldSpec, GuardRequest, Hook, Outcome, ReadRequest,
    Refusal, Row, RowChange, RowImage, RowWrite, Source, SubRow, SubRowChange, TableDecl, TypeSpec,
    UseSpec, Veto, View, WriteKind,
};
use superschematic_rules_behaviors::{core, Timers, TimersConfig};

const START: i64 = 1_791_536_400_000;
const HOUR: i64 = 3_600_000;

// ---- the host ----

// Memory answers the core's reads from rows, their sub-table rows and the
// deadlines armed on them, and applies a change set as an adapter does: a
// deadline command spends the arming it ran for before the change set's
// own deadline changes, and a delete takes the row's sub-table rows and
// deadlines with it.
#[derive(Default)]
struct Memory {
    rows: BTreeMap<String, Row>,
    /// By row key, use id, table and key.
    sub_rows: BTreeMap<(String, String, String, String), Map<String, Value>>,
    /// By row key, use id and name: the time it falls due.
    armed: BTreeMap<(String, String, String), i64>,
}

impl Source for Memory {
    fn answer(&mut self, request: &ReadRequest) -> Result<Answer, Refusal> {
        match request {
            ReadRequest::Row { key, .. } => Ok(Answer::Row(self.rows.get(key).cloned())),
            ReadRequest::HasRows { .. } => Ok(Answer::HasRows(!self.rows.is_empty())),
            ReadRequest::SubRows {
                key, use_id, table, ..
            } => Ok(Answer::SubRows(self.table(key, use_id, table))),
            other => Err(Refusal::internal(format!(
                "this host answers no read of this kind: {other:?}"
            ))),
        }
    }
}

impl Memory {
    fn table(&self, key: &str, use_id: &str, table: &str) -> Vec<SubRow> {
        self.sub_rows
            .iter()
            .filter(|((row, used, named, _), _)| row == key && used == use_id && named == table)
            .map(|((_, _, _, sub_key), values)| SubRow::new(sub_key.clone(), values.clone()))
            .collect()
    }

    fn apply(&mut self, command: &Command, changes: &ChangeSet) {
        let mut deleted = Vec::new();
        for write in &changes.rows {
            let key = write.key().expect("a keyed row").to_owned();
            match write {
                RowWrite::Create { own, owned, .. } => {
                    let row = Row {
                        own: own.clone(),
                        owned: owned.clone(),
                        version: 1,
                    };
                    self.rows.insert(key, row);
                }
                RowWrite::Update {
                    expected_version,
                    own,
                    owned,
                    ..
                } => {
                    let row = self.rows.get_mut(&key).expect("the row");
                    assert_eq!(row.version, *expected_version);
                    row.own = merge_patch(&row.own, own);
                    for (use_id, fields) in owned {
                        let held = row.owned.entry(use_id.clone()).or_default();
                        for (field, value) in fields {
                            held.insert(field.clone(), value.clone());
                        }
                    }
                    row.version += 1;
                }
                RowWrite::Delete { .. } => {
                    self.rows.remove(&key);
                    self.sub_rows.retain(|(row, ..), _| *row != key);
                    self.armed.retain(|(row, ..), _| *row != key);
                    deleted.push(key);
                }
                other => panic!("a write this host does not take: {other:?}"),
            }
        }
        if let Action::Deadline {
            key,
            use_id,
            name,
            at,
            ..
        } = &command.action
        {
            let arming = (key.clone(), use_id.clone(), name.clone());
            assert_eq!(
                self.armed.remove(&arming),
                Some(*at),
                "the arming it ran for"
            );
        }
        for write in &changes.sub_rows {
            let key = write.row.key.clone().expect("a keyed row");
            if deleted.contains(&key) {
                continue;
            }
            let at = (
                key,
                write.use_id.clone(),
                write.table.clone(),
                write.key.clone(),
            );
            match &write.change {
                SubRowChange::Insert { values } => {
                    assert!(!self.sub_rows.contains_key(&at), "a new row: {at:?}");
                    self.sub_rows.insert(at, present(values));
                }
                SubRowChange::Update { values } => {
                    let row = self.sub_rows.get_mut(&at).expect("the row it updates");
                    for (name, value) in values {
                        if value.is_null() {
                            row.remove(name);
                        } else {
                            row.insert(name.clone(), value.clone());
                        }
                    }
                }
                SubRowChange::Delete => {
                    assert!(self.sub_rows.remove(&at).is_some(), "the row it deletes");
                }
                other => panic!("a sub-table change this host does not take: {other:?}"),
            }
        }
        for change in &changes.deadlines {
            let key = change.row().key.clone().expect("a keyed row");
            if deleted.contains(&key) {
                continue;
            }
            let arming = (key, change.use_id().to_owned(), change.name().to_owned());
            match change {
                DeadlineChange::Arm { at, .. } => {
                    self.armed.insert(arming, *at);
                }
                _ => {
                    self.armed.remove(&arming);
                }
            }
        }
    }
}

fn present(values: &Map<String, Value>) -> Map<String, Value> {
    values
        .iter()
        .filter(|(_, value)| !value.is_null())
        .map(|(name, value)| (name.clone(), value.clone()))
        .collect()
}

struct Host {
    core: Core,
    spec: TypeSpec,
    memory: Memory,
}

impl Host {
    fn new(timers: Value) -> Self {
        Host::of(ticket(timers))
    }

    fn of(spec: TypeSpec) -> Self {
        Host {
            core: core().with(Freeze),
            spec,
            memory: Memory::default(),
        }
    }

    // run drives a command to its outcome and applies its change set.
    fn run(&mut self, caller: &Caller, now: i64, action: Action) -> Outcome {
        let command = Command::new(caller.clone(), now, action).with_request("r1");
        let outcome = self.core.drive(&self.spec, &command, &mut self.memory);
        if let Outcome::Changed(changes) = &outcome {
            self.memory.apply(&command, changes);
        }
        outcome
    }

    fn create(&mut self, key: &str, own: Value, now: i64) -> ChangeSet {
        changed(self.run(
            &alice(),
            now,
            Action::Create {
                id: Some(key.to_owned()),
                own: object(own),
                params: Map::new(),
            },
        ))
    }

    fn update(&mut self, key: &str, patch: Value, now: i64) -> ChangeSet {
        changed(self.run(
            &alice(),
            now,
            Action::Update {
                key: key.to_owned(),
                patch: object(patch),
            },
        ))
    }

    fn invoke(
        &mut self,
        caller: &Caller,
        use_id: &str,
        op: &str,
        params: Value,
        now: i64,
    ) -> Outcome {
        self.run(
            caller,
            now,
            Action::Operation {
                key: "t1".to_owned(),
                use_id: use_id.to_owned(),
                operation: op.to_owned(),
                params,
            },
        )
    }

    fn transition(&mut self, to: &str, now: i64) -> Outcome {
        self.invoke(&alice(), "status", "transition", json!({ "to": to }), now)
    }

    // fall_due runs every deadline of t1 armed and due at a time, as the
    // API's worker does, as the runner: what each came to.
    fn fall_due(&mut self, now: i64, runner: &Caller) -> Vec<(String, Outcome)> {
        let due: Vec<((String, String, String), i64)> = self
            .memory
            .armed
            .iter()
            .filter(|(_, at)| **at <= now)
            .map(|(arming, at)| (arming.clone(), *at))
            .collect();
        due.into_iter()
            .map(|((key, use_id, name), at)| {
                let outcome = self.run(
                    runner,
                    now.max(at),
                    Action::Deadline {
                        key,
                        use_id,
                        name: name.clone(),
                        at,
                        payload: Value::Null,
                    },
                );
                (name, outcome)
            })
            .collect()
    }

    // deadlines is t1's Timers.deadlines, as a read shows them.
    fn deadlines(&self) -> Value {
        let row = self.memory.rows.get("t1").expect("t1");
        let mut tables = BTreeMap::new();
        tables.insert(
            "deadlines".to_owned(),
            self.memory.table("t1", "Timers", "deadlines"),
        );
        let fields = self
            .core
            .read_fields(&self.spec, "Timers", row, &tables)
            .expect("its fields");
        fields["deadlines"].clone()
    }

    fn armed(&self) -> Vec<(String, i64)> {
        self.memory
            .armed
            .iter()
            .map(|((_, _, name), at)| (name.clone(), *at))
            .collect()
    }

    fn status(&self) -> Value {
        self.memory.rows["t1"].owned["status"]["status"].clone()
    }

    // image is t1's fields as a write's image holds them.
    fn image(&self) -> RowImage {
        let row = &self.memory.rows["t1"];
        RowImage {
            own: row.own.clone(),
            owned: row.owned.clone(),
        }
    }

    // hear runs a write of t1 its table's change queue heard, as the worker
    // runs it, and applies what it writes.
    fn hear(
        &mut self,
        write: WriteKind,
        before: Option<RowImage>,
        after: Option<RowImage>,
        at: i64,
    ) -> ChangeSet {
        let change = RowChange {
            write,
            at,
            before,
            after,
        };
        changed(self.run(
            &runner(),
            at,
            Action::Change {
                key: "t1".to_owned(),
                change,
            },
        ))
    }
}

// ---- the fixtures ----

fn caller(subject: &str, permissions: &[&str]) -> Caller {
    Caller::new(subject, permissions.iter().copied())
}

fn alice() -> Caller {
    caller("alice", &[])
}

fn runner() -> Caller {
    caller("runner", &[])
}

// A ticket's status: open escalates (with tickets.escalate), waits or
// closes; waiting reopens or closes; escalated closes.
fn flow() -> Value {
    json!({
        "states": ["open", "waiting", "escalated", "closed"],
        "transitions": [
            {"from": "open", "to": "waiting"},
            {"from": "waiting", "to": "open"},
            {"from": "open", "to": "escalated", "permission": "tickets.escalate"},
            {"from": "open", "to": "closed"},
            {"from": "waiting", "to": "closed"},
            {"from": "escalated", "to": "closed"}
        ]
    })
}

// A ticket as a table holds it: its status a Workflow field, Timers on the
// type, Freeze after them, a title and a date-time visitAt.
fn ticket(timers: Value) -> TypeSpec {
    TypeSpec::new("Ticket")
        .with(UseSpec::on_field("status", "Workflow", flow()))
        .with(UseSpec::on_type("Timers", timers))
        .with(UseSpec::on_type("Freeze", None))
        .with_field(FieldSpec::new("title", "string"))
        .with_field(FieldSpec::date_time("visitAt", "Temporal.DateTime"))
}

fn object(value: Value) -> Map<String, Value> {
    value.as_object().cloned().expect("an object")
}

fn changed(outcome: Outcome) -> ChangeSet {
    match outcome.into_result() {
        Ok(changes) => changes,
        Err(refusal) => panic!("refused: {}", refusal.error_json()),
    }
}

// refused is a refusal as the engine's error JSON.
fn refused(outcome: Outcome) -> Value {
    match outcome.into_result() {
        Ok(changes) => panic!("not refused: {changes:?}"),
        Err(refusal) => refusal.error_json(),
    }
}

// Freeze: vetoes a move of the status while the ticket's own frozen is
// true, as a lease another principal holds does.
struct Freeze;

impl Behavior for Freeze {
    type Config = ();

    fn declaration(&self) -> Declaration {
        Declaration::new("Freeze").veto("frozen")
    }

    fn parse_config(&self, _raw: &Value, _target: &ConfigTarget<'_>) -> Result<(), ConfigError> {
        Ok(())
    }

    fn guard(&self, view: &View<'_, ()>, request: &GuardRequest<'_>) -> Hook<Option<Veto>> {
        let frozen = view.own_field("frozen") == Some(Value::Bool(true));
        Ok(
            (request.operation_of("status", "transition").is_some() && frozen)
                .then(|| Veto::new("it is frozen").with_code("frozen")),
        )
    }
}

// define runs the core's define check of a type.
fn define(spec: &TypeSpec) -> Result<(), Refusal> {
    let command = Command::new(alice(), START, Action::Define);
    core()
        .with(Freeze)
        .drive(spec, &command, &mut Memory::default())
        .into_result()
        .map(|_| ())
}

// rule is the reason a define refuses a Timers config for, after the
// core's prefix.
fn rule(spec: &TypeSpec) -> String {
    let refusal = define(spec).expect_err("refused");
    refusal
        .message
        .strip_prefix(&format!("type {}: behavior Timers config: ", spec.name))
        .unwrap_or(&refusal.message)
        .to_owned()
}

// shape is what a define refuses a Timers config's shape for: each issue
// as `<path into the use's entry>: <message>`.
fn shape(timers: Value) -> Vec<String> {
    let refusal = define(&ticket(timers)).expect_err("refused");
    refusal.details.expect("details")["issues"]
        .as_array()
        .expect("issues")
        .iter()
        .map(|issue| {
            format!(
                "{}: {}",
                issue["path"].as_str().unwrap(),
                issue["message"].as_str().unwrap()
            )
        })
        .collect()
}

// undescribed is a schema with every description left out, at any depth:
// what the checks implement, and nothing else.
fn undescribed(schema: &Value) -> Value {
    match schema {
        Value::Object(members) => Value::Object(
            members
                .iter()
                .filter(|(key, _)| key.as_str() != "description")
                .map(|(key, member)| (key.clone(), undescribed(member)))
                .collect(),
        ),
        Value::Array(items) => Value::Array(items.iter().map(undescribed).collect()),
        other => other.clone(),
    }
}

// ---- the declaration and its schemas ----

#[test]
fn the_declaration_is_the_files() {
    assert_eq!(
        Timers.declaration(),
        Declaration::new("Timers")
            .computed("deadlines")
            .sub_table(TableDecl::new(
                "deadlines",
                "name",
                [
                    "name",
                    "baseAt",
                    "armedFrom",
                    "dueAt",
                    "fired",
                    "firedAt",
                    "outcome"
                ]
            ))
            .arms_deadlines()
            .hears_changes()
            .operation("fireTimer", true)
            .operation("resetTimer", true)
            .operation("snoozeTimer", true)
            .veto("no_deadline")
            .veto("not_armed")
    );
}

/// The checks Timers writes by hand are the declaration's configSchema and
/// paramsSchemas, keyword for keyword: each schema, its descriptions left
/// out, is exactly the one they implement, so a keyword the file gains
/// fails this test until the checks take it. The shape refusals the
/// recorded config scenario holds (`core-timers.json`, its steps 19 to 22)
/// come out in its words.
#[test]
fn the_config_checks_are_the_config_schema() {
    let file: Value = serde_json::from_str(superschematic_rules_behaviors::timers::DECLARATION)
        .expect("the declaration file parses");
    let text = json!({"minLength": 1, "type": "string"});
    let closed = |key: &str, member: Value| json!({"additionalProperties": false, "properties": {key: member}, "required": [key], "type": "object"});
    let states = json!({"items": text, "minItems": 1, "type": "array", "uniqueItems": true});
    assert_eq!(
        undescribed(&file["configSchema"]),
        json!({
            "additionalProperties": false,
            "properties": {
                "deadlines": {
                    "additionalProperties": {
                        "additionalProperties": false,
                        "properties": {
                            "afterMs": {"maximum": 3_153_600_000_000_i64, "minimum": -3_153_600_000_000_i64, "type": "integer"},
                            "start": {"oneOf": [
                                closed("create", json!({"const": true})),
                                closed("enters", text.clone()),
                                closed("field", text.clone())
                            ]},
                            "then": {
                                "additionalProperties": false,
                                "properties": {"from": states, "transition": text},
                                "required": ["transition"],
                                "type": "object"
                            },
                            "until": {"oneOf": [
                                closed("leaves", text.clone()),
                                closed("enters", states.clone())
                            ]}
                        },
                        "required": ["start"],
                        "type": "object"
                    },
                    "minProperties": 1,
                    "propertyNames": {"maxLength": 64, "pattern": "^[A-Za-z][A-Za-z0-9_-]*$"},
                    "type": "object"
                },
                "permission": text,
                "status": text,
                "sweepMs": {"minimum": 1000, "type": "integer"}
            },
            "required": ["deadlines"],
            "type": "object"
        })
    );
    let operations = file["operations"].as_array().expect("operations");
    let names: Vec<&str> = operations
        .iter()
        .map(|operation| operation["name"].as_str().expect("a name"))
        .collect();
    assert_eq!(names, ["fireTimer", "resetTimer", "snoozeTimer"]);
    for operation in operations {
        let mut properties = json!({"deadline": text});
        let mut required = json!(["deadline"]);
        if operation["name"] == "snoozeTimer" {
            properties["dueAt"] = json!({"minimum": 0, "type": "integer"});
            required = json!(["deadline", "dueAt"]);
        }
        assert_eq!(
            undescribed(&operation["paramsSchema"]),
            json!({"additionalProperties": false, "properties": properties, "required": required, "type": "object"})
        );
    }
    assert_eq!(file.get("createParamsSchema"), None);

    // The recorded config scenario's shape refusals, each at its path
    // into the use's entry.
    let config = |deadline: Value| json!({"deadlines": {"sla": deadline}});
    assert_eq!(
        shape(json!({"deadlines": {}})),
        ["/config/deadlines: must NOT have fewer than 1 properties"]
    );
    assert_eq!(
        shape(config(json!({"start": {"create": true, "enters": "open"}}))),
        [
            "/config/deadlines/sla/start: unknown key \"create\"",
            "/config/deadlines/sla/start: unknown key \"create\"",
            "/config/deadlines/sla/start: unknown key \"enters\"",
            "/config/deadlines/sla/start: unknown key \"enters\"",
            "/config/deadlines/sla/start: must match exactly one schema in oneOf",
            "/config/deadlines/sla/start: must have required property 'field'"
        ]
    );
    assert_eq!(
        shape(json!({"deadlines": {"sla": {"start": {"create": true}}}, "sweepMs": 999})),
        ["/config/sweepMs: must be >= 1000"]
    );
    assert_eq!(
        shape(config(json!({"start": {"create": true}, "afterMs": 1.5}))),
        ["/config/deadlines/sla/afterMs: must be integer"]
    );
    // And the rest of the schema's keywords, in ajv's words.
    assert_eq!(
        shape(json!({"deadlines": {"2nd": {"start": {"create": false}, "then": {"from": [], "x": 1}, "until": {"enters": ["a", "a"]}}}, "status": ""})),
        [
            "/config/deadlines: must match pattern \"^[A-Za-z][A-Za-z0-9_-]*$\"",
            "/config/deadlines: property name must be valid",
            "/config/deadlines/2nd/start: unknown key \"create\"",
            "/config/deadlines/2nd/start: unknown key \"create\"",
            "/config/deadlines/2nd/start: must match exactly one schema in oneOf",
            "/config/deadlines/2nd/start: must have required property 'enters'",
            "/config/deadlines/2nd/start: must have required property 'field'",
            "/config/deadlines/2nd/start/create: must be equal to constant",
            "/config/deadlines/2nd/then: unknown key \"x\"",
            "/config/deadlines/2nd/then: must have required property 'transition'",
            "/config/deadlines/2nd/then/from: must NOT have fewer than 1 items",
            "/config/deadlines/2nd/until: unknown key \"enters\"",
            "/config/deadlines/2nd/until: must match exactly one schema in oneOf",
            "/config/deadlines/2nd/until: must have required property 'leaves'",
            "/config/deadlines/2nd/until/enters: must NOT have duplicate items (items ## 1 and 0 are identical)",
            "/config/status: must NOT have fewer than 1 characters"
        ]
    );
}

#[test]
fn the_operations_parameters_are_held_to_their_params_schemas() {
    let mut host = Host::new(json!({"deadlines": {"respond": {"start": {"create": true}}}}));
    host.create("t1", json!({"title": "t1"}), START);
    assert_eq!(
        refused(host.invoke(
            &alice(),
            "Timers",
            "snoozeTimer",
            json!({"deadline": "", "dueAt": -1, "x": 1}),
            START
        )),
        json!({"class": "OperationParamsError", "code": "invalid_argument", "message": "operation snoozeTimer of behavior Timers: must NOT have additional properties: x; /deadline: must NOT have fewer than 1 characters; /dueAt: must be >= 0", "details": {"issues": [
            {"path": "", "message": "must NOT have additional properties: x"},
            {"path": "/deadline", "message": "must NOT have fewer than 1 characters"},
            {"path": "/dueAt", "message": "must be >= 0"}
        ]}})
    );
    assert_eq!(
        refused(host.invoke(&alice(), "Timers", "fireTimer", json!({}), START))["details"]
            ["issues"],
        json!([{"path": "", "message": "must have required property 'deadline'"}])
    );
}

// ---- starts and fires ----

/// A create start is due afterMs after the create; a fire before it is
/// due, or after it fired, changes nothing; a due one fires once, through
/// the API's worker, moving the status from the state it starts in.
#[test]
fn a_create_start_fires_once_when_it_falls_due() {
    let mut host = Host::new(json!({"deadlines": {
        "respond": {"start": {"create": true}, "afterMs": HOUR, "then": {"transition": "closed"}}
    }}));
    let created = host.create("t1", json!({"title": "t1"}), START);
    assert_eq!(
        created.sub_rows[0].change,
        SubRowChange::Insert {
            values: object(
                json!({"baseAt": START, "armedFrom": START.to_string(), "dueAt": START + HOUR, "fired": false})
            )
        }
    );
    assert_eq!(host.armed(), [("respond".to_owned(), START + HOUR)]);
    assert_eq!(
        host.deadlines(),
        json!({"respond": {"dueAt": START + HOUR, "fired": false}})
    );

    // Not due: unchanged, no write at all.
    let early = changed(host.invoke(
        &alice(),
        "Timers",
        "fireTimer",
        json!({"deadline": "respond"}),
        START,
    ));
    assert_eq!(early.result, json!({"fired": false}));
    assert!(early.rows.is_empty() && early.sub_rows.is_empty() && early.deadlines.is_empty());

    // Due: the worker's deadline command fires it through fireTimer.
    let ran = host.fall_due(START + HOUR, &runner());
    assert_eq!(ran.len(), 1);
    let fired = changed(ran.into_iter().next().unwrap().1);
    assert!(matches!(fired.rows.as_slice(), [RowWrite::Update { .. }]));
    assert_eq!(host.status(), json!("closed"));
    assert_eq!(
        host.deadlines(),
        json!({"respond": {"dueAt": START + HOUR, "fired": true, "firedAt": START + HOUR, "outcome": "moved"}})
    );
    assert!(host.armed().is_empty());

    // Fired already: unchanged.
    let again = changed(host.invoke(
        &alice(),
        "Timers",
        "fireTimer",
        json!({"deadline": "respond"}),
        START + 2 * HOUR,
    ));
    assert_eq!(again.result, json!({"fired": false}));
    assert!(again.rows.is_empty());
    assert_eq!(
        refused(host.invoke(
            &alice(),
            "Timers",
            "fireTimer",
            json!({"deadline": "answer"}),
            START
        )),
        json!({"class": "BehaviorVetoError", "code": "vetoed", "message": "behavior Timers vetoes fireTimer of Ticket t1: answer is not a deadline of Ticket's Timers (its deadlines: respond)", "details": {"behavior": "Timers", "action": "fireTimer", "reason": "answer is not a deadline of Ticket's Timers (its deadlines: respond)", "vetoCode": "no_deadline"}})
    );
}

/// An enters start arms at each entry of the status into its state, a new
/// entry arming it again from its new time, and a caller who may write the
/// row fires a due one before the worker, which then finds it spent.
#[test]
fn an_enters_start_arms_at_each_entry() {
    let mut host = Host::new(
        json!({"deadlines": {"idle": {"start": {"enters": "waiting"}, "afterMs": HOUR}}}),
    );
    host.create("t1", json!({"title": "t1"}), START);
    assert_eq!(host.deadlines(), json!({}));
    changed(host.transition("waiting", START + 10));
    assert_eq!(host.armed(), [("idle".to_owned(), START + 10 + HOUR)]);
    changed(host.transition("open", START + 20));
    // Left its state: the deadline stays, as the engine's did with no until.
    assert_eq!(host.armed(), [("idle".to_owned(), START + 10 + HOUR)]);
    changed(host.transition("waiting", START + 30));
    assert_eq!(host.armed(), [("idle".to_owned(), START + 30 + HOUR)]);

    let fired = changed(host.invoke(
        &alice(),
        "Timers",
        "fireTimer",
        json!({"deadline": "idle"}),
        START + 30 + HOUR,
    ));
    assert_eq!(fired.result, json!({"fired": true}));
    assert!(host.armed().is_empty(), "the caller's fire clears it");
    assert!(host.fall_due(START + 2 * HOUR + 30, &runner()).is_empty());
}

/// A field start is due afterMs from the time an own field holds, as epoch
/// milliseconds (a table's column) or as text (an engine schema's); a new
/// value moves it and clears fired, and clearing the field deletes it.
#[test]
fn a_field_start_reads_a_number_or_text() {
    let mut host = Host::new(
        json!({"deadlines": {"remind": {"start": {"field": "visitAt"}, "afterMs": -HOUR}}}),
    );
    let visit = START + 24 * HOUR;
    host.create("t1", json!({"title": "t1", "visitAt": visit}), START);
    assert_eq!(host.armed(), [("remind".to_owned(), visit - HOUR)]);
    assert_eq!(
        host.memory.sub_rows.values().next().unwrap()["armedFrom"],
        json!(visit.to_string())
    );
    // Text is a fact of its own: it arms again, from the time it holds.
    host.update("t1", json!({"visitAt": "2026-10-11T08:00:00Z"}), START);
    let text = superschematic_rules_behaviors::parse_date_time("2026-10-11T08:00:00Z").unwrap();
    assert_eq!(host.armed(), [("remind".to_owned(), text - HOUR)]);
    // A write that leaves the field as it is arms nothing.
    let untouched = host.update("t1", json!({"title": "renamed"}), START);
    assert!(untouched.sub_rows.is_empty() && untouched.deadlines.is_empty());
    // Text that holds no time, or a field cleared, deletes the deadline.
    let cleared = host.update("t1", json!({"visitAt": null}), START);
    assert!(matches!(cleared.sub_rows[0].change, SubRowChange::Delete));
    assert!(host.armed().is_empty() && host.memory.sub_rows.is_empty());
}

// ---- then's outcomes and until ----

/// then moves the status from the states it moves from (the state it
/// starts in by default), skipped in any other; a guard's veto and a
/// permission the worker lacks leave the status as it is; the deadline is
/// fired whatever its move met, and the outcome records which.
#[test]
fn then_records_moved_skipped_vetoed_and_forbidden() {
    let timers = json!({"deadlines": {"respond": {"start": {"create": true}, "afterMs": HOUR, "then": {"transition": "escalated"}}}});
    let outcome_of = |own: Value, moved_on: bool, runner: &Caller| -> (Value, Value) {
        let mut host = Host::new(timers.clone());
        host.create("t1", own, START);
        if moved_on {
            changed(host.transition("waiting", START));
        }
        let ran = host.fall_due(START + HOUR, runner);
        changed(ran.into_iter().next().expect("a deadline").1);
        (
            host.deadlines()["respond"]["outcome"].clone(),
            host.status(),
        )
    };
    let lead = caller("runner", &["tickets.escalate"]);
    assert_eq!(
        outcome_of(json!({"title": "t1"}), false, &lead),
        (json!("moved"), json!("escalated"))
    );
    assert_eq!(
        outcome_of(json!({"title": "t1"}), true, &lead),
        (json!("skipped"), json!("waiting"))
    );
    assert_eq!(
        outcome_of(json!({"title": "t1", "frozen": true}), false, &lead),
        (json!("vetoed"), json!("open"))
    );
    assert_eq!(
        outcome_of(json!({"title": "t1"}), false, &runner()),
        (json!("forbidden"), json!("open"))
    );
}

/// until cancels a pending deadline once a change leaves the status out of
/// the state it starts in (leaves) or in a state it names (enters), and a
/// deadline whose until holds when it falls due is cancelled, not fired.
#[test]
fn until_cancels_a_pending_deadline_by_leaves_and_by_enters() {
    let mut host = Host::new(json!({"deadlines": {
        "respond": {"start": {"enters": "open"}, "afterMs": HOUR, "until": {"leaves": "open"}},
        "answer": {"start": {"create": true}, "afterMs": 2 * HOUR, "until": {"enters": ["closed"]}}
    }}));
    host.create("t1", json!({"title": "t1"}), START);
    assert_eq!(host.armed().len(), 2);
    changed(host.transition("waiting", START + 1));
    assert_eq!(host.armed(), [("answer".to_owned(), START + 2 * HOUR)]);
    assert_eq!(
        host.deadlines(),
        json!({"answer": {"dueAt": START + 2 * HOUR, "fired": false}})
    );
    changed(host.transition("closed", START + 2));
    assert!(host.armed().is_empty());
    assert_eq!(host.deadlines(), json!({}));
    // A return to an earlier state does not start a create deadline again.
    let mut host = Host::new(
        json!({"deadlines": {"answer": {"start": {"create": true}, "afterMs": HOUR, "until": {"enters": ["waiting"]}}}}),
    );
    host.create("t1", json!({"title": "t1"}), START);
    changed(host.transition("waiting", START + 1));
    changed(host.transition("open", START + 2));
    assert!(host.armed().is_empty());
}

// ---- reset and snooze ----

/// resetTimer and snoozeTimer need the config's permission, nobody's when
/// it names none; a snooze moves the deadline and a later write keeps it,
/// a reset starts it again from what it starts from, and either is
/// refused a deadline with no due time to give.
#[test]
fn reset_and_snooze_need_the_permission_and_move_the_deadline() {
    let mut host = Host::new(json!({"deadlines": {
        "respond": {"start": {"enters": "open"}, "afterMs": HOUR},
        "chase": {"start": {"enters": "waiting"}, "afterMs": HOUR}
    }, "permission": "tickets.timers"}));
    host.create("t1", json!({"title": "t1"}), START);
    let lee = caller("lee", &["tickets.timers"]);
    assert_eq!(
        refused(host.invoke(
            &alice(),
            "Timers",
            "snoozeTimer",
            json!({"deadline": "respond", "dueAt": START + 5 * HOUR}),
            START
        )),
        json!({"class": "EngineError", "code": "forbidden", "message": "alice may not snooze deadline respond of Ticket t1: it needs permission tickets.timers"})
    );
    let snoozed = changed(host.invoke(
        &lee,
        "Timers",
        "snoozeTimer",
        json!({"deadline": "respond", "dueAt": START + 5 * HOUR}),
        START,
    ));
    assert_eq!(snoozed.result, json!({"dueAt": START + 5 * HOUR}));
    assert_eq!(host.armed(), [("respond".to_owned(), START + 5 * HOUR)]);
    // The same snooze again changes nothing; a later write keeps it.
    let again = changed(host.invoke(
        &lee,
        "Timers",
        "snoozeTimer",
        json!({"deadline": "respond", "dueAt": START + 5 * HOUR}),
        START,
    ));
    assert!(again.rows.is_empty());
    host.update("t1", json!({"title": "renamed"}), START + 1);
    assert_eq!(host.armed(), [("respond".to_owned(), START + 5 * HOUR)]);
    // A reset starts it again from the entry.
    let reset = changed(host.invoke(
        &lee,
        "Timers",
        "resetTimer",
        json!({"deadline": "respond"}),
        START + 2,
    ));
    assert_eq!(reset.result, json!({"dueAt": START + HOUR}));
    assert_eq!(host.armed(), [("respond".to_owned(), START + HOUR)]);
    assert_eq!(
        refused(host.invoke(&lee, "Timers", "resetTimer", json!({"deadline": "chase"}), START))["message"],
        json!("behavior Timers vetoes resetTimer of Ticket t1: deadline chase starts when the status enters waiting, and Ticket t1 is open; it starts at the next entry")
    );
    assert_eq!(
        refused(host.invoke(
            &lee,
            "Timers",
            "snoozeTimer",
            json!({"deadline": "chase", "dueAt": START}),
            START
        ))["details"],
        json!({"behavior": "Timers", "action": "snoozeTimer", "reason": "deadline chase of Ticket t1 has not started or was cancelled, so it has no due time to move", "vetoCode": "not_armed"})
    );

    let mut host = Host::new(json!({"deadlines": {"respond": {"start": {"create": true}}}}));
    host.create("t1", json!({"title": "t1"}), START);
    assert_eq!(
        refused(host.invoke(&lee, "Timers", "resetTimer", json!({"deadline": "respond"}), START))["message"],
        json!("lee may not reset deadline respond of Ticket t1: Ticket's Timers config names no permission, so nobody may")
    );
}

/// A row created before Timers joined its table has no create time: it
/// gets no create deadline, and a reset of one is refused.
#[test]
fn a_row_created_before_timers_has_no_create_time() {
    let mut host =
        Host::of(TypeSpec::new("Ticket").with(UseSpec::on_field("status", "Workflow", flow())));
    host.create("t1", json!({"title": "t1"}), START);
    host.spec = ticket(
        json!({"deadlines": {"respond": {"start": {"create": true}}}, "permission": "tickets.timers"}),
    );
    host.update("t1", json!({"title": "renamed"}), START + 1);
    assert!(host.armed().is_empty());
    assert_eq!(
        refused(host.invoke(&caller("lee", &["tickets.timers"]), "Timers", "resetTimer", json!({"deadline": "respond"}), START))["details"]["reason"],
        json!("deadline respond starts from the create, and Ticket t1 was created before Timers kept its deadlines, so it has no create time to start from")
    );
}

/// A config change reconciles each row's deadlines at its next write (D18):
/// one the config no longer names is deleted and cleared, and one that
/// falls due before that deletes its row and changes nothing else.
#[test]
fn a_config_change_reconciles_at_the_next_write() {
    let mut host = Host::new(json!({"deadlines": {
        "respond": {"start": {"create": true}, "afterMs": HOUR},
        "ping": {"start": {"create": true}, "afterMs": 2 * HOUR}
    }}));
    host.create("t1", json!({"title": "t1"}), START);
    host.spec =
        ticket(json!({"deadlines": {"ping": {"start": {"create": true}, "afterMs": 2 * HOUR}}}));
    // respond falls due under the new config: its row goes.
    let ran = host.fall_due(START + HOUR, &runner());
    changed(ran.into_iter().next().unwrap().1);
    assert_eq!(host.memory.table("t1", "Timers", "deadlines").len(), 1);
    host.spec = ticket(json!({"deadlines": {"later": {"start": {"create": true}}}}));
    let written = host.update("t1", json!({"title": "renamed"}), START + 1);
    assert!(matches!(written.sub_rows[0].change, SubRowChange::Delete));
    assert!(host.armed().is_empty());
    assert_eq!(host.deadlines(), json!({}));
}

// ---- the Workflow a config reads ----

#[test]
fn the_workflow_a_config_reads_is_implied_named_or_refused() {
    let reads = json!({"deadlines": {"late": {"start": {"create": true}, "then": {"transition": "closed"}}}});
    // Implied: the type's one Workflow.
    assert!(define(&ticket(reads.clone())).is_ok());
    let two = |timers: Value| {
        TypeSpec::new("Invoice")
            .with(UseSpec::on_field("status", "Workflow", flow()))
            .with(UseSpec::on_field("payment", "Workflow", flow()))
            .with(UseSpec::on_type("Timers", timers))
    };
    assert_eq!(
        rule(&two(reads.clone())),
        "deadlines.late.then reads the status of Workflow, and the type has several (status, payment): status names the field whose Workflow it reads"
    );
    let mut named = reads.clone();
    named["status"] = json!("payment");
    assert!(define(&two(named.clone())).is_ok());
    let parsed = config_of(&two(named.clone()));
    assert_eq!(parsed.status.as_deref(), Some("payment"));
    named["status"] = json!("phase");
    assert_eq!(
        rule(&two(named)),
        "status \"phase\" is not a Workflow of the type (its Workflows: status, payment)"
    );
    let none = TypeSpec::new("Ticket").with(UseSpec::on_type("Timers", reads));
    assert_eq!(
        rule(&none),
        "deadlines.late.then reads the status of Workflow, which the type does not list"
    );
    let mut named = json!({"deadlines": {"ping": {"start": {"create": true}}}, "status": "status"});
    assert_eq!(
        rule(&TypeSpec::new("Ticket").with(UseSpec::on_type("Timers", named.clone()))),
        "status \"status\" is not a Workflow of the type (its Workflows: none)"
    );
    // A config that reads no status needs no Workflow.
    named.as_object_mut().unwrap().remove("status");
    assert!(define(&TypeSpec::new("Ticket").with(UseSpec::on_type("Timers", named))).is_ok());
}

#[test]
fn a_field_start_is_held_to_the_types_date_time_fields() {
    let starts = |field: &str| json!({"deadlines": {"remind": {"start": {"field": field}}}});
    assert_eq!(
        rule(&ticket(starts("when"))),
        "deadlines.remind.start.field \"when\" is not a field of Ticket (its fields: title, visitAt)"
    );
    assert_eq!(
        rule(&ticket(starts("title"))),
        "deadlines.remind.start.field \"title\" is not a date-time field of Ticket: a deadline starts from the time a value holds"
    );
    assert_eq!(
        rule(&TypeSpec::new("Ticket").with(UseSpec::on_type("Timers", starts("title")))),
        "deadlines.remind.start.field \"title\" is not a field of Ticket (its fields: none)"
    );
    assert!(define(&ticket(starts("visitAt"))).is_ok());
}

// config_of is the Timers config the core parses for a type.
fn config_of(spec: &TypeSpec) -> TimersConfig {
    let index = spec
        .uses
        .iter()
        .position(|entry| entry.behavior == "Timers")
        .expect("a Timers use");
    let target = ConfigTarget::new(spec, index);
    Timers
        .parse_config(&spec.uses[index].config_or_empty(), &target)
        .expect("a config")
}

#[test]
fn a_config_lists_its_deadlines_in_name_order() {
    let parsed = config_of(&ticket(json!({"deadlines": {
        "respond": {"start": {"create": true}},
        "chase": {"start": {"create": true}}
    }})));
    let names: Vec<&str> = parsed
        .deadlines
        .iter()
        .map(|(name, _)| name.as_str())
        .collect();
    assert_eq!(names, ["chase", "respond"]);
    assert_eq!(parsed.status, None, "no deadline reads the status");
}

// ---- writes heard from the table's change queue ----

/// A write the core made, heard from the table's change queue after the
/// commit, changes nothing: the core's own write left the row reconciled.
#[test]
fn a_heard_write_the_core_made_changes_nothing() {
    let mut host = Host::new(json!({"deadlines": {
        "respond": {"start": {"create": true}, "afterMs": HOUR},
        "idle": {"start": {"enters": "waiting"}, "afterMs": HOUR},
        "remind": {"start": {"field": "visitAt"}, "afterMs": -HOUR}
    }}));
    host.create(
        "t1",
        json!({"title": "t1", "visitAt": START + 24 * HOUR}),
        START,
    );
    let inserted = host.image();
    let quiet = |changes: ChangeSet| {
        assert!(
            changes.rows.is_empty() && changes.sub_rows.is_empty() && changes.deadlines.is_empty(),
            "{changes:?}"
        );
    };
    quiet(host.hear(WriteKind::Insert, None, Some(inserted.clone()), START));
    changed(host.transition("waiting", START + 10));
    let moved = host.image();
    quiet(host.hear(WriteKind::Update, Some(inserted), Some(moved), START + 10));
    assert_eq!(
        host.armed(),
        [
            ("idle".to_owned(), START + 10 + HOUR),
            ("remind".to_owned(), START + 23 * HOUR),
            ("respond".to_owned(), START + HOUR)
        ]
    );
}

/// A raw write of a field start, past the core, heard from the table's
/// change queue, arms the deadline again from the time the field holds now,
/// held to the version the row is at.
#[test]
fn a_raw_write_of_a_field_start_heard_arms_it_again() {
    let mut host = Host::new(
        json!({"deadlines": {"remind": {"start": {"field": "visitAt"}, "afterMs": -HOUR}}}),
    );
    host.create(
        "t1",
        json!({"title": "t1", "visitAt": START + 24 * HOUR}),
        START,
    );
    let before = host.image();
    host.memory
        .rows
        .get_mut("t1")
        .expect("t1")
        .own
        .insert("visitAt".into(), json!(START + 48 * HOUR));
    let after = host.image();
    let heard = host.hear(WriteKind::Update, Some(before), Some(after), START + 5);
    assert!(matches!(
        heard.rows.as_slice(),
        [RowWrite::Update {
            expected_version: 1,
            ..
        }]
    ));
    assert_eq!(host.armed(), [("remind".to_owned(), START + 47 * HOUR)]);
    assert_eq!(
        host.deadlines(),
        json!({"remind": {"dueAt": START + 47 * HOUR, "fired": false}})
    );
}

/// A raw insert, heard from the table's change queue, arms a create
/// deadline from the insert's time, which the row's sub-table keeps from
/// then on; heard again, it arms nothing new.
#[test]
fn a_raw_insert_heard_arms_a_create_deadline_from_its_time() {
    let mut host =
        Host::new(json!({"deadlines": {"respond": {"start": {"create": true}, "afterMs": HOUR}}}));
    host.memory.rows.insert(
        "t1".to_owned(),
        Row {
            own: object(json!({"title": "t1"})),
            owned: [(
                "status".to_owned(),
                object(json!({"status": "open", "enteredAt": START})),
            )]
            .into_iter()
            .collect(),
            version: 1,
        },
    );
    let inserted = host.image();
    host.hear(WriteKind::Insert, None, Some(inserted.clone()), START + 7);
    assert_eq!(host.armed(), [("respond".to_owned(), START + 7 + HOUR)]);
    let again = host.hear(WriteKind::Insert, None, Some(inserted), START + 9);
    assert!(again.sub_rows.is_empty() && again.deadlines.is_empty());
    assert_eq!(host.armed(), [("respond".to_owned(), START + 7 + HOUR)]);
}

/// A deadline that falls due reconciles its row first: one whose field a
/// raw write moved, before that write's change ran, is armed again at its
/// new time rather than fired on its old arming.
#[test]
fn a_deadline_reconciles_its_row_before_it_fires() {
    let mut host = Host::new(
        json!({"deadlines": {"remind": {"start": {"field": "visitAt"}, "afterMs": -HOUR}}}),
    );
    host.create(
        "t1",
        json!({"title": "t1", "visitAt": START + 2 * HOUR}),
        START,
    );
    assert_eq!(host.armed(), [("remind".to_owned(), START + HOUR)]);
    host.memory
        .rows
        .get_mut("t1")
        .expect("t1")
        .own
        .insert("visitAt".into(), json!(START + 5 * HOUR));
    let ran = host.fall_due(START + HOUR, &runner());
    assert_eq!(ran.len(), 1);
    changed(ran.into_iter().next().expect("a run").1);
    assert_eq!(host.armed(), [("remind".to_owned(), START + 4 * HOUR)]);
    assert_eq!(
        host.deadlines(),
        json!({"remind": {"dueAt": START + 4 * HOUR, "fired": false}})
    );
}
