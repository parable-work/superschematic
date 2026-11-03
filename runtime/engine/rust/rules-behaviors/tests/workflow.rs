//! Workflow on the pure core (D103), through the core's public API over a
//! source in memory: the cases `runtime/engine/testdata/scenarios/
//! core-workflow.json` records (its initial state and when it was entered,
//! the transition operation and its guard, which no caller and no other
//! behavior gets past, its permission gates, the change set a transition's
//! event is written from, its config at define, and its rule for a new
//! version, with rows and without), each refusal worded as the engine
//! words it. Where the engine's message names a namespace or a version,
//! the core's leaves them out and a host writes them (the core's README).

use std::collections::BTreeMap;

use serde_json::{json, Map, Value};
use superschematic_rules::{
    merge_patch, Action, Answer, Behavior, Caller, ChangeSet, Code, Command, ConfigError,
    ConfigTarget, Core, Cx, Declaration, Hook, Outcome, PermissionMatcher, ReadRequest, Refusal,
    Row, RowWrite, Source, TypeSpec, UseSpec,
};
use superschematic_rules_behaviors::{core, Workflow};

const START: i64 = 1_000_000;

// ---- the host ----

// Memory answers the core's reads from rows in memory and records each
// read it is asked.
#[derive(Default)]
struct Memory {
    rows: BTreeMap<(String, String), Row>,
    reads: Vec<ReadRequest>,
}

impl Source for Memory {
    fn answer(&mut self, request: &ReadRequest) -> Result<Answer, Refusal> {
        self.reads.push(request.clone());
        match request {
            ReadRequest::Row { type_name, key } => Ok(Answer::Row(
                self.rows.get(&(type_name.clone(), key.clone())).cloned(),
            )),
            ReadRequest::HasRows { type_name } => Ok(Answer::HasRows(
                self.rows.keys().any(|(name, _)| name == type_name),
            )),
            other => Err(Refusal::internal(format!(
                "this host answers no read of this kind: {other:?}"
            ))),
        }
    }
}

impl Memory {
    // apply writes a change set's rows as an adapter does: a create at
    // version 1; an update held to its version, which patches own fields,
    // sets owned ones and moves the version on; a delete.
    fn apply(&mut self, changes: &ChangeSet) {
        for write in &changes.rows {
            let at = (
                write.type_name().to_owned(),
                write.key().expect("a keyed row").to_owned(),
            );
            match write {
                RowWrite::Create { own, owned, .. } => {
                    self.rows.insert(
                        at,
                        Row {
                            own: own.clone(),
                            owned: owned.clone(),
                            version: 1,
                        },
                    );
                }
                RowWrite::Update {
                    expected_version,
                    own,
                    owned,
                    ..
                } => {
                    let row = self.rows.get_mut(&at).expect("the row");
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
                    self.rows.remove(&at);
                }
                other => panic!("a write this host does not take: {other:?}"),
            }
        }
    }

    fn row(&self, key: &str) -> &Row {
        self.rows
            .get(&("Order".to_owned(), key.to_owned()))
            .expect("the row")
    }

    fn asked_has_rows(&self) -> bool {
        self.reads
            .iter()
            .any(|read| matches!(read, ReadRequest::HasRows { .. }))
    }
}

struct Host {
    core: Core,
    spec: TypeSpec,
    memory: Memory,
}

impl Host {
    fn new(config: Value) -> Self {
        Host::of(core(), order(config))
    }

    fn of(core: Core, spec: TypeSpec) -> Self {
        Host {
            core,
            spec,
            memory: Memory::default(),
        }
    }

    // run drives a command to its outcome and applies its change set.
    fn run(&mut self, caller: &Caller, now: i64, action: Action) -> Outcome {
        let command = Command::new(caller.clone(), now, action);
        let outcome = self.core.drive(&self.spec, &command, &mut self.memory);
        if let Outcome::Changed(changes) = &outcome {
            self.memory.apply(changes);
        }
        outcome
    }

    fn create(&mut self, key: &str, now: i64) -> ChangeSet {
        changed(self.run(
            &alice(),
            now,
            Action::Create {
                id: Some(key.to_owned()),
                own: object(json!({"title": "Desk"})),
                params: Map::new(),
            },
        ))
    }

    fn invoke(&mut self, caller: &Caller, operation: &str, params: Value, now: i64) -> Outcome {
        let use_id = match operation {
            "transition" => "Workflow",
            _ => "test.Dispatch",
        };
        self.run(
            caller,
            now,
            Action::Operation {
                key: "o1".to_owned(),
                use_id: use_id.to_owned(),
                operation: operation.to_owned(),
                params,
            },
        )
    }

    fn transition(&mut self, caller: &Caller, to: &str) -> Outcome {
        self.invoke(caller, "transition", json!({ "to": to }), START)
    }

    // publish checks the host's type as a new version of an earlier one.
    fn publish(&mut self, from: TypeSpec) -> Outcome {
        self.run(&alice(), START, Action::Publish { from: Some(from) })
    }
}

// ---- the fixtures ----

fn caller(subject: &str, permissions: &[&str]) -> Caller {
    Caller::new(subject, permissions.iter().copied())
}

fn alice() -> Caller {
    caller("alice", &[])
}

// An order moves from draft to open, then ships (which needs orders.ship)
// or is cancelled; shipped and cancelled are terminal.
fn order_flow() -> Value {
    json!({
        "states": ["draft", "open", "shipped", "cancelled"],
        "transitions": [
            {"from": "draft", "to": "open"},
            {"from": "draft", "to": "cancelled"},
            {"from": "open", "to": "shipped", "permission": "orders.ship"},
            {"from": "open", "to": "cancelled"}
        ]
    })
}

// order_flow with members changed.
fn flow_with(members: Value) -> Value {
    let mut flow = order_flow();
    for (key, value) in members.as_object().expect("an object") {
        flow[key] = value.clone();
    }
    flow
}

fn order(config: Value) -> TypeSpec {
    TypeSpec::new("Order").with(UseSpec::on_type("Workflow", config))
}

fn object(value: Value) -> Map<String, Value> {
    value.as_object().cloned().expect("an object")
}

// owned reads `{ use id: { field: value } }` as a change set's owned map.
fn owned(value: Value) -> BTreeMap<String, Map<String, Value>> {
    object(value)
        .into_iter()
        .map(|(use_id, fields)| (use_id, object(fields)))
        .collect()
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

// The veto of a move draft → shipped, which no transition makes.
fn not_allowed() -> Value {
    json!({"class": "BehaviorVetoError", "code": "vetoed", "message": "behavior Workflow vetoes transition of Order o1: no transition leads from draft to shipped; from draft it can move to open, cancelled", "details": {"behavior": "Workflow", "action": "transition", "reason": "no transition leads from draft to shipped; from draft it can move to open, cancelled", "vetoCode": "transition_not_allowed", "vetoDetails": {"from": "draft", "to": "shipped", "allowed": ["open", "cancelled"]}}})
}

fn forbidden(subject: &str) -> Value {
    json!({"class": "EngineError", "code": "forbidden", "message": format!("{subject} may not move Order o1 from open to shipped: the transition needs permission orders.ship")})
}

// ---- initialize ----

#[test]
fn a_new_row_starts_in_the_initial_state_entered_at_its_create() {
    let mut host = Host::new(order_flow());
    let created = host.create("o1", START);
    assert_eq!(
        created.rows,
        [RowWrite::Create {
            type_name: "Order".to_owned(),
            key: Some("o1".to_owned()),
            own: object(json!({"title": "Desk"})),
            owned: owned(json!({"Workflow": {"status": "draft", "enteredAt": START}})),
        }]
    );
    assert!(created.events.is_empty());
    assert_eq!(created.result, Value::Null);

    // The config's initial state, where it names one.
    let mut host = Host::new(flow_with(json!({"initial": "open"})));
    let created = host.create("o1", START);
    assert_eq!(created.owned("Workflow", "status"), Some(&json!("open")));
    assert_eq!(created.owned("Workflow", "enteredAt"), Some(&json!(START)));
}

#[test]
fn a_create_gives_workflow_no_parameters() {
    let mut host = Host::new(order_flow());
    let outcome = host.run(
        &alice(),
        START,
        Action::Create {
            id: Some("o1".to_owned()),
            own: object(json!({"title": "Desk"})),
            params: object(json!({"Workflow": {"status": "open"}})),
        },
    );
    let refusal = json!({"class": "CreateParamsError", "code": "invalid_argument", "message": "create of Order: /Workflow: behavior Workflow takes no create parameters", "details": {"issues": [{"path": "/Workflow", "message": "behavior Workflow takes no create parameters"}]}});
    assert_eq!(refused(outcome), refusal);
    // An entry of {} is an entry, as the engine refused it.
    let outcome = host.run(
        &alice(),
        START,
        Action::Create {
            id: Some("o1".to_owned()),
            own: object(json!({"title": "Desk"})),
            params: object(json!({"Workflow": {}})),
        },
    );
    assert_eq!(refused(outcome), refusal);
}

// ---- transition and the change set its event is written from ----

#[test]
fn transition_moves_the_status_and_its_change_set_carries_the_new_status_and_when_it_was_entered() {
    let mut host = Host::new(order_flow());
    host.create("o1", START);
    let moved = changed(host.invoke(&alice(), "transition", json!({"to": "open"}), START + 5_000));
    // The row's update the host writes, and records as the operation's
    // event: the use's status and when it was entered, no own patch.
    assert_eq!(
        moved.rows,
        [RowWrite::Update {
            type_name: "Order".to_owned(),
            key: "o1".to_owned(),
            expected_version: 1,
            own: Map::new(),
            owned: owned(json!({"Workflow": {"status": "open", "enteredAt": START + 5_000}})),
        }]
    );
    assert_eq!(moved.result, json!({"from": "draft", "to": "open"}));
    // Workflow emits no outbox event of its own: the host's event for the
    // command is the row write's.
    assert!(moved.events.is_empty());
    assert!(moved.sub_rows.is_empty() && moved.deadlines.is_empty());

    // An update of own fields leaves the status and when it was entered.
    let updated = changed(host.run(
        &alice(),
        START + 10_000,
        Action::Update {
            key: "o1".to_owned(),
            patch: object(json!({"title": "Oak desk"})),
        },
    ));
    assert!(
        matches!(updated.rows.as_slice(), [RowWrite::Update { owned, .. }] if owned.is_empty())
    );
    let row = host.memory.row("o1");
    assert_eq!(row.own, object(json!({"title": "Oak desk"})));
    assert_eq!(
        row.owned,
        owned(json!({"Workflow": {"status": "open", "enteredAt": START + 5_000}}))
    );
}

#[test]
fn a_row_from_before_workflow_kept_entered_at_has_none_until_its_next_transition() {
    let mut host = Host::new(order_flow());
    host.memory.rows.insert(
        ("Order".to_owned(), "o1".to_owned()),
        Row {
            own: object(json!({"title": "Desk"})),
            owned: owned(json!({"Workflow": {"status": "draft"}})),
            version: 1,
        },
    );
    let moved = changed(host.invoke(&alice(), "transition", json!({"to": "open"}), START + 1_000));
    assert_eq!(moved.result, json!({"from": "draft", "to": "open"}));
    assert_eq!(
        host.memory.row("o1").owned,
        owned(json!({"Workflow": {"status": "open", "enteredAt": START + 1_000}}))
    );
}

// ---- the guard ----

#[test]
fn its_guard_refuses_a_state_the_config_lacks_the_state_it_is_in_a_transition_it_does_not_list_and_a_move_out_of_a_terminal_state(
) {
    let mut host = Host::new(order_flow());
    host.create("o1", START);
    assert_eq!(
        refused(host.transition(&alice(), "lost")),
        json!({"class": "OperationParamsError", "code": "invalid_argument", "message": "operation transition of behavior Workflow: /to: \"lost\" is not a state of Order (draft, open, shipped, cancelled)", "details": {"issues": [{"path": "/to", "message": "\"lost\" is not a state of Order (draft, open, shipped, cancelled)"}]}})
    );
    assert_eq!(
        refused(host.transition(&alice(), "draft")),
        json!({"class": "BehaviorVetoError", "code": "vetoed", "message": "behavior Workflow vetoes transition of Order o1: Order o1 is already draft", "details": {"behavior": "Workflow", "action": "transition", "reason": "Order o1 is already draft", "vetoCode": "already_in_state", "vetoDetails": {"from": "draft", "to": "draft"}}})
    );
    assert_eq!(refused(host.transition(&alice(), "shipped")), not_allowed());
    assert_eq!(
        changed(host.transition(&alice(), "cancelled")).result,
        json!({"from": "draft", "to": "cancelled"})
    );
    assert_eq!(
        refused(host.transition(&alice(), "open")),
        json!({"class": "BehaviorVetoError", "code": "vetoed", "message": "behavior Workflow vetoes transition of Order o1: cancelled is a terminal state: no transition leaves it", "details": {"behavior": "Workflow", "action": "transition", "reason": "cancelled is a terminal state: no transition leaves it", "vetoCode": "terminal_state", "vetoDetails": {"from": "cancelled", "to": "open"}}})
    );
    assert_eq!(host.memory.row("o1").version, 2);
}

#[test]
fn a_row_with_no_status_has_nothing_to_move_from() {
    let mut host = Host::new(order_flow());
    host.memory.rows.insert(
        ("Order".to_owned(), "o1".to_owned()),
        Row {
            own: object(json!({"title": "Desk"})),
            owned: BTreeMap::new(),
            version: 4,
        },
    );
    assert_eq!(
        refused(host.transition(&alice(), "open")),
        json!({"class": "BehaviorVetoError", "code": "vetoed", "message": "behavior Workflow vetoes transition of Order o1: Order o1 has no status", "details": {"behavior": "Workflow", "action": "transition", "reason": "Order o1 has no status", "vetoCode": "no_status"}})
    );
}

#[test]
fn transition_takes_to_and_nothing_else_so_no_alias_reaches_the_guard_or_the_handler() {
    let mut host = Host::new(order_flow());
    host.create("o1", START);
    assert_eq!(
        refused(host.invoke(&alice(), "transition", json!({"status": "open"}), START)),
        json!({"class": "OperationParamsError", "code": "invalid_argument", "message": "operation transition of behavior Workflow: must NOT have additional properties: status; must have required property 'to'", "details": {"issues": [{"path": "", "message": "must NOT have additional properties: status"}, {"path": "", "message": "must have required property 'to'"}]}})
    );
    assert_eq!(
        refused(host.invoke(
            &alice(),
            "transition",
            json!({"to": "open", "status": "shipped"}),
            START
        )),
        json!({"class": "OperationParamsError", "code": "invalid_argument", "message": "operation transition of behavior Workflow: must NOT have additional properties: status", "details": {"issues": [{"path": "", "message": "must NOT have additional properties: status"}]}})
    );
    assert_eq!(
        refused(host.invoke(&alice(), "transition", json!({"to": ""}), START)),
        json!({"class": "OperationParamsError", "code": "invalid_argument", "message": "operation transition of behavior Workflow: /to: must NOT have fewer than 1 characters", "details": {"issues": [{"path": "/to", "message": "must NOT have fewer than 1 characters"}]}})
    );
    let row = host.memory.row("o1");
    assert_eq!(row.version, 1);
    assert_eq!(
        row.owned,
        owned(json!({"Workflow": {"status": "draft", "enteredAt": START}}))
    );
}

// ---- the permission gate ----

// Rooted is a deployment's matcher: only root holds any permission.
struct Rooted;

impl PermissionMatcher for Rooted {
    fn matches(&self, held: &[String], _required: &[String]) -> bool {
        held.iter().any(|permission| permission == "root")
    }
}

#[test]
fn a_transition_that_names_a_permission_needs_a_caller_who_holds_it() {
    let mut host = Host::new(order_flow());
    host.create("o1", START);
    changed(host.transition(&alice(), "open"));
    assert_eq!(
        refused(host.transition(&alice(), "shipped")),
        forbidden("alice")
    );
    // A caller standing in for an end user holds no permission.
    assert_eq!(
        refused(host.transition(&Caller::standing_in("svc"), "shipped")),
        forbidden("svc")
    );
    // A permission covers every permission nested under it.
    assert_eq!(
        changed(host.transition(&caller("olga", &["orders"]), "shipped")).result,
        json!({"from": "open", "to": "shipped"})
    );

    let mut host = Host::new(order_flow());
    host.create("o1", START);
    changed(host.transition(&alice(), "open"));
    changed(host.transition(&caller("sam", &["orders.ship"]), "shipped"));

    // A deployment's matcher decides instead of the default rule.
    let mut host = Host::of(core().with_matcher(Rooted), order(order_flow()));
    host.create("o1", START);
    changed(host.transition(&alice(), "open"));
    assert_eq!(
        refused(host.transition(&caller("sam", &["orders.ship"]), "shipped")),
        forbidden("sam")
    );
    changed(host.transition(&caller("rita", &["root"]), "shipped"));
}

// ---- another behavior ----

// Dispatch moves the status only through Workflow's transition, which is
// the only way: a write of Workflow's field is not a field it declares.
struct Dispatch;

impl Behavior for Dispatch {
    type Config = ();

    fn declaration(&self) -> Declaration {
        Declaration::new("test.Dispatch")
            .operation("dispatch", true)
            .operation("forceStatus", true)
    }

    fn parse_config(&self, _raw: &Value, _target: &ConfigTarget<'_>) -> Result<(), ConfigError> {
        Ok(())
    }

    fn operation(&self, cx: &Cx<'_, ()>, name: &str, params: &Value) -> Hook<Value> {
        match name {
            "dispatch" => cx.call(
                "Workflow",
                "transition",
                &json!({ "to": params.get("to").cloned() }),
            ),
            _ => {
                cx.set("status", "shipped")?;
                Ok(Value::Null)
            }
        }
    }
}

#[test]
fn another_behavior_moves_the_status_only_through_transition_whose_guard_and_permission_gate_run_for_it_too(
) {
    let spec = order(order_flow()).with(UseSpec::on_type("test.Dispatch", json!({})));
    let mut host = Host::of(core().with(Dispatch), spec);
    host.create("o1", START);
    assert_eq!(
        refused(host.invoke(&alice(), "dispatch", json!({"to": "shipped"}), START)),
        not_allowed()
    );
    let moved = changed(host.invoke(&alice(), "dispatch", json!({"to": "open"}), START + 1_000));
    assert_eq!(moved.result, json!({"from": "draft", "to": "open"}));
    // The patch the engine's event recorded under the outer operation.
    assert_eq!(
        moved.rows,
        [RowWrite::Update {
            type_name: "Order".to_owned(),
            key: "o1".to_owned(),
            expected_version: 1,
            own: Map::new(),
            owned: owned(json!({"Workflow": {"status": "open", "enteredAt": START + 1_000}})),
        }]
    );
    assert_eq!(
        refused(host.invoke(&alice(), "dispatch", json!({"to": "shipped"}), START)),
        forbidden("alice")
    );
    let shipped = changed(host.invoke(
        &caller("sam", &["orders.ship"]),
        "dispatch",
        json!({"to": "shipped"}),
        START + 2_000,
    ));
    assert_eq!(shipped.result, json!({"from": "open", "to": "shipped"}));
    // A write of Workflow's field from another behavior is a defect.
    let forced = host
        .invoke(&alice(), "forceStatus", json!({}), START)
        .into_result()
        .expect_err("refused");
    assert_eq!(forced.code, Code::BehaviorError);
    assert!(
        forced.message.starts_with("behavior test.Dispatch: "),
        "{}",
        forced.message
    );
    assert_eq!(
        host.memory.row("o1").owned,
        owned(json!({"Workflow": {"status": "shipped", "enteredAt": START + 2_000}}))
    );
}

// ---- two fields ----

#[test]
fn two_fields_carry_workflow_and_each_guards_and_moves_only_its_own_status() {
    let spec = TypeSpec::new("Order")
        .with(UseSpec::on_field("status", "Workflow", order_flow()))
        .with(UseSpec::on_field(
            "fulfilment",
            "Workflow",
            json!({
                "states": ["pending", "packed", "sent"],
                "transitions": [{"from": "pending", "to": "packed"}, {"from": "packed", "to": "sent"}]
            }),
        ));
    let mut host = Host::of(core(), spec);
    let created = host.create("o1", START);
    assert_eq!(
        created.rows.first().and_then(|row| match row {
            RowWrite::Create { owned, .. } => Some(owned.clone()),
            _ => None,
        }),
        Some(owned(json!({
            "fulfilment": {"status": "pending", "enteredAt": START},
            "status": {"status": "draft", "enteredAt": START}
        })))
    );
    let transition = |host: &mut Host, caller: &Caller, use_id: &str, to: &str| {
        host.run(
            caller,
            START + 1_000,
            Action::Operation {
                key: "o1".to_owned(),
                use_id: use_id.to_owned(),
                operation: "transition".to_owned(),
                params: json!({ "to": to }),
            },
        )
    };
    let packed = changed(transition(&mut host, &alice(), "fulfilment", "packed"));
    assert_eq!(
        packed.rows,
        [RowWrite::Update {
            type_name: "Order".to_owned(),
            key: "o1".to_owned(),
            expected_version: 1,
            own: Map::new(),
            owned: owned(json!({"fulfilment": {"status": "packed", "enteredAt": START + 1_000}})),
        }]
    );
    // The veto names the field in its reason, and the use beside its
    // behavior.
    assert_eq!(
        refused(transition(&mut host, &alice(), "fulfilment", "packed")),
        json!({"class": "BehaviorVetoError", "code": "vetoed", "message": "behavior Workflow vetoes transition of Order o1: Order o1's fulfilment is already packed", "details": {"behavior": "Workflow", "use": "fulfilment", "action": "transition", "reason": "Order o1's fulfilment is already packed", "vetoCode": "already_in_state", "vetoDetails": {"from": "packed", "to": "packed"}}})
    );
    // The status's states are its own, and the refusal of a state it lacks
    // names the field.
    assert_eq!(
        refused(transition(&mut host, &alice(), "status", "packed")),
        json!({"class": "OperationParamsError", "code": "invalid_argument", "message": "operation transition of behavior Workflow on field status: /to: \"packed\" is not a state of Order.status (draft, open, shipped, cancelled)", "details": {"use": "status", "issues": [{"path": "/to", "message": "\"packed\" is not a state of Order.status (draft, open, shipped, cancelled)"}]}})
    );
    // So does a permission's.
    changed(transition(&mut host, &alice(), "status", "open"));
    assert_eq!(
        refused(transition(&mut host, &alice(), "status", "shipped")),
        json!({"class": "EngineError", "code": "forbidden", "message": "alice may not move Order o1's status from open to shipped: the transition needs permission orders.ship"})
    );
    assert_eq!(
        host.memory.row("o1").owned["status"],
        object(json!({"status": "open", "enteredAt": START + 1_000}))
    );

    // Each field is Workflow's: a create or an update that names one, to
    // set it or clear it, is refused, and nothing moves.
    let owned_issue = |field: &str| json!({"path": field, "rule": "owned", "message": format!("{field} is behavior Workflow's field: only its operations change it")});
    let create = host.run(
        &alice(),
        START,
        Action::Create {
            id: Some("o2".to_owned()),
            own: object(json!({"title": "Lamp", "status": "shipped"})),
            params: Map::new(),
        },
    );
    assert_eq!(
        refused(create),
        json!({"class": "InstanceValidationError", "code": "invalid_instance", "message": "Order: status: status is behavior Workflow's field: only its operations change it", "details": {"issues": [owned_issue("status")]}})
    );
    for patch in [
        json!({"status": "shipped"}),
        json!({"fulfilment": "sent", "status": null}),
    ] {
        let update = host.run(
            &alice(),
            START + 2_000,
            Action::Update {
                key: "o1".to_owned(),
                patch: object(patch.clone()),
            },
        );
        let issues: Vec<Value> = ["status", "fulfilment"]
            .into_iter()
            .filter(|field| patch.get(field).is_some())
            .map(owned_issue)
            .collect();
        assert_eq!(
            refused(update)["details"]["issues"],
            json!(issues),
            "{patch}"
        );
    }
    let row = host.memory.row("o1");
    assert_eq!(row.version, 3);
    assert_eq!(row.own, object(json!({"title": "Desk"})));
    assert_eq!(row.owned["status"]["status"], json!("open"));
    assert_eq!(row.owned["fulfilment"]["status"], json!("packed"));
}

#[test]
fn a_row_with_no_value_on_a_field_has_nothing_to_move_from() {
    let spec =
        TypeSpec::new("Order").with(UseSpec::on_field("fulfilment", "Workflow", order_flow()));
    let mut host = Host::of(core(), spec);
    host.memory.rows.insert(
        ("Order".to_owned(), "o1".to_owned()),
        Row {
            own: object(json!({"title": "Desk"})),
            owned: BTreeMap::new(),
            version: 1,
        },
    );
    let outcome = host.run(
        &alice(),
        START,
        Action::Operation {
            key: "o1".to_owned(),
            use_id: "fulfilment".to_owned(),
            operation: "transition".to_owned(),
            params: json!({"to": "open"}),
        },
    );
    assert_eq!(
        refused(outcome)["details"]["reason"],
        json!("Order o1 has no fulfilment")
    );
}

// ---- define ----

// define_refusal is the define check's refusal of a config: its message,
// after checking it is the one issue of the one use.
fn define_refusal(config: Value) -> String {
    let command = Command::new(alice(), START, Action::Define);
    let refusal = core()
        .drive(&order(config), &command, &mut Memory::default())
        .into_result()
        .expect_err("refused");
    let json = refusal.error_json();
    assert_eq!(
        (json["class"].clone(), json["code"].clone()),
        (json!("SchemaDocumentError"), json!("invalid_schema"))
    );
    let issues = json["details"]["issues"].as_array().expect("issues");
    assert_eq!(issues.len(), 1);
    assert_eq!(issues[0]["use"], json!("Workflow"));
    assert_eq!(issues[0]["index"], json!(0));
    assert_eq!(issues[0]["message"], json!(refusal.message));
    refusal.message
}

#[test]
fn define_takes_a_state_machine() {
    let command = Command::new(alice(), START, Action::Define);
    for config in [
        order_flow(),
        flow_with(json!({"initial": "open", "outcomes": {"cancelled": "failure"}})),
        json!({"states": ["only"], "transitions": []}),
    ] {
        let outcome = core().drive(&order(config), &command, &mut Memory::default());
        assert_eq!(changed(outcome), ChangeSet::default());
    }
}

#[test]
fn define_refuses_a_config_its_schema_lets_through_that_is_not_a_state_machine() {
    let prefix = "type Order: behavior Workflow config: ";
    let all = "(draft, open, shipped, cancelled)";
    let with_transition = |transition: Value| {
        let mut flow = order_flow();
        flow["transitions"]
            .as_array_mut()
            .expect("transitions")
            .push(transition);
        flow
    };
    let cases = [
        (
            flow_with(json!({"initial": "lost"})),
            format!("the initial state \"lost\" is not one of its states {all}"),
        ),
        (
            with_transition(json!({"from": "open", "to": "held"})),
            format!(
                "the transition from \"open\" to \"held\" names \"held\", which is not one of its states {all}"
            ),
        ),
        (
            with_transition(json!({"from": "open", "to": "open"})),
            "a transition from \"open\" to itself changes nothing".to_owned(),
        ),
        (
            with_transition(json!({"from": "draft", "to": "open"})),
            "the transition from \"draft\" to \"open\" is listed twice".to_owned(),
        ),
        (
            flow_with(json!({"outcomes": {"lost": "failure"}})),
            format!("outcomes names \"lost\", which is not one of its states {all}"),
        ),
        (
            flow_with(json!({"outcomes": {"shipped": "success", "open": "failure"}})),
            "outcomes names \"open\", which is not a terminal state: a transition leaves it, and only a terminal state has an outcome".to_owned(),
        ),
    ];
    for (config, reason) in cases {
        assert_eq!(define_refusal(config), format!("{prefix}{reason}"));
    }
}

// define_issues is the define check's issues with a type's uses: each
// one's kind, its path into the use's entry and its message.
fn define_issues(spec: TypeSpec) -> Vec<(String, String, String)> {
    let command = Command::new(alice(), START, Action::Define);
    let refusal = core()
        .drive(&spec, &command, &mut Memory::default())
        .into_result()
        .expect_err("refused");
    assert_eq!(refusal.class, "SchemaDocumentError");
    refusal.details.expect("details")["issues"]
        .as_array()
        .expect("issues")
        .iter()
        .map(|issue| {
            let text = |key: &str| issue[key].as_str().unwrap_or_default().to_owned();
            (text("kind"), text("path"), text("message"))
        })
        .collect()
}

#[test]
fn define_refuses_a_config_the_declarations_config_schema_refuses() {
    let pattern = "\"^[A-Za-z][A-Za-z0-9_-]*$\"";
    let cases: Vec<(Value, Vec<(&str, String)>)> = vec![
        (
            json!({"states": [], "transitions": []}),
            vec![(
                "/config/states",
                "must NOT have fewer than 1 items".to_owned(),
            )],
        ),
        (
            json!({"states": ["in review"], "transitions": []}),
            vec![("/config/states/0", format!("must match pattern {pattern}"))],
        ),
        (
            flow_with(json!({"terminal": ["shipped"]})),
            vec![("/config", "unknown key \"terminal\"".to_owned())],
        ),
        (
            flow_with(json!({"outcomes": {"cancelled": "aborted"}})),
            vec![(
                "/config/outcomes/cancelled",
                "must be one of \"success\", \"failure\", \"neutral\"".to_owned(),
            )],
        ),
        (
            flow_with(json!({"outcomes": {}})),
            vec![(
                "/config/outcomes",
                "must NOT have fewer than 1 properties".to_owned(),
            )],
        ),
        (
            json!({}),
            vec![
                ("/config", "must have required property 'states'".to_owned()),
                (
                    "/config",
                    "must have required property 'transitions'".to_owned(),
                ),
            ],
        ),
        (json!(null), vec![("/config", "must be object".to_owned())]),
        (
            json!({"states": ["a", "a"], "transitions": []}),
            vec![(
                "/config/states",
                "must NOT have duplicate items (items ## 1 and 0 are identical)".to_owned(),
            )],
        ),
        (
            json!({"states": ["a", "b"], "transitions": [{"from": "a", "when": "now"}]}),
            vec![
                ("/config/transitions/0", "unknown key \"when\"".to_owned()),
                (
                    "/config/transitions/0",
                    "must have required property 'to'".to_owned(),
                ),
            ],
        ),
        (
            json!({"states": ["a", "b"], "transitions": [{"from": "a", "to": "b", "permission": ""}]}),
            vec![(
                "/config/transitions/0/permission",
                "must NOT have fewer than 1 characters".to_owned(),
            )],
        ),
        (
            json!({"states": ["a", "b"], "transitions": [{"from": "a", "to": "b"}], "outcomes": {"in review": "failure"}}),
            vec![
                ("/config/outcomes", format!("must match pattern {pattern}")),
                ("/config/outcomes", "property name must be valid".to_owned()),
            ],
        ),
        (
            json!({"states": "a", "transitions": {}, "initial": 1}),
            vec![
                ("/config/initial", "must be string".to_owned()),
                ("/config/states", "must be array".to_owned()),
                ("/config/transitions", "must be array".to_owned()),
            ],
        ),
    ];
    for (config, expected) in cases {
        let expected: Vec<(String, String, String)> = expected
            .into_iter()
            .map(|(path, message)| ("schema".to_owned(), path.to_owned(), message))
            .collect();
        assert_eq!(define_issues(order(config.clone())), expected, "{config}");
    }
    // The message names the type, the behavior and the path in the config.
    let command = Command::new(alice(), START, Action::Define);
    let refusal = core()
        .drive(
            &order(json!({"states": [], "transitions": []})),
            &command,
            &mut Memory::default(),
        )
        .into_result()
        .expect_err("refused");
    assert_eq!(
        refusal.message,
        "type Order: behavior Workflow config: /states: must NOT have fewer than 1 items"
    );
    // A use that gives no config is refused for the config it lacks, at
    // its entry: {} is not the shape Workflow's schema gives.
    assert_eq!(
        define_issues(TypeSpec::new("Order").with(UseSpec::on_type("Workflow", None))),
        vec![(
            "schema".to_owned(),
            String::new(),
            "must have required property 'config'".to_owned()
        )]
    );
}

// ---- a new version ----

#[test]
fn a_new_version_keeps_every_state_while_transitions_permissions_and_the_initial_state_may_change()
{
    let mut host = Host::new(order_flow());
    host.create("o1", START);
    let v2 = json!({
        "states": ["draft", "open", "shipped", "cancelled", "held"],
        "initial": "open",
        "transitions": [
            {"from": "draft", "to": "open"},
            {"from": "draft", "to": "cancelled"},
            {"from": "open", "to": "shipped"},
            {"from": "open", "to": "held"},
            {"from": "held", "to": "open"}
        ]
    });
    host.spec = order(v2.clone());
    host.memory.reads.clear();
    changed(host.publish(order(order_flow())));
    // Nothing dropped, so nothing to read.
    assert!(!host.memory.asked_has_rows());
    changed(host.transition(&alice(), "open"));
    // The permission the new version drops no longer gates the move.
    changed(host.transition(&alice(), "shipped"));
    assert_eq!(
        host.create("o2", START).owned("Workflow", "status"),
        Some(&json!("open"))
    );

    let v3 = json!({
        "states": ["draft", "open", "shipped"],
        "transitions": [{"from": "draft", "to": "open"}, {"from": "open", "to": "shipped"}]
    });
    host.spec = order(v3.clone());
    // Each config prints with its members sorted, as the engine printed
    // its canonical document's.
    let message = r#"behavior Workflow on type Order cannot change its config from {"initial":"open","states":["draft","open","shipped","cancelled","held"],"transitions":[{"from":"draft","to":"open"},{"from":"draft","to":"cancelled"},{"from":"open","to":"shipped"},{"from":"open","to":"held"},{"from":"held","to":"open"}]} to {"states":["draft","open","shipped"],"transitions":[{"from":"draft","to":"open"},{"from":"open","to":"shipped"}]}: an instance may be in "cancelled", "held", which the new config drops"#;
    assert_eq!(
        refused(host.publish(order(v2.clone()))),
        json!({"class": "IncompatibleChangeError", "code": "incompatible_change", "message": format!("type Order: {message}"), "details": {"changes": [{"path": "Order.behaviors.Workflow", "message": message}]}})
    );

    host.spec = TypeSpec::new("Order");
    assert_eq!(
        refused(host.publish(order(v2)))["details"],
        json!({"changes": [{"path": "Order.behaviors.Workflow", "message": "behavior Workflow cannot be removed from type Order, which has instances: the instances would lose their status"}]})
    );
}

#[test]
fn with_no_rows_a_new_version_may_drop_a_state_and_once_the_last_one_goes_it_may_again() {
    let dropped = json!({
        "states": ["draft", "open", "shipped"],
        "transitions": [{"from": "draft", "to": "open"}, {"from": "open", "to": "shipped"}]
    });
    let mut host = Host::new(order_flow());
    host.create("o1", START);
    host.spec = order(dropped.clone());
    assert_eq!(
        refused(host.publish(order(order_flow())))["details"]["changes"][0]["message"],
        json!(
            r#"behavior Workflow on type Order cannot change its config from {"states":["draft","open","shipped","cancelled"],"transitions":[{"from":"draft","to":"open"},{"from":"draft","to":"cancelled"},{"from":"open","permission":"orders.ship","to":"shipped"},{"from":"open","to":"cancelled"}]} to {"states":["draft","open","shipped"],"transitions":[{"from":"draft","to":"open"},{"from":"open","to":"shipped"}]}: an instance may be in "cancelled", which the new config drops"#
        )
    );
    host.memory.rows.clear();
    host.memory.reads.clear();
    changed(host.publish(order(order_flow())));
    // Dropping a state reads whether the type has rows.
    assert!(host.memory.asked_has_rows());
    host.spec = order(
        json!({"states": ["draft", "open"], "transitions": [{"from": "draft", "to": "open"}]}),
    );
    changed(host.publish(order(dropped)));
}

#[test]
fn workflow_joins_a_type_with_no_rows_and_not_one_that_has_them() {
    let mut host = Host::new(order_flow());
    changed(host.publish(TypeSpec::new("Order")));

    host.memory.rows.insert(
        ("Order".to_owned(), "o1".to_owned()),
        Row {
            own: object(json!({"title": "Desk"})),
            owned: BTreeMap::new(),
            version: 1,
        },
    );
    assert_eq!(
        refused(host.publish(TypeSpec::new("Order"))),
        json!({"class": "IncompatibleChangeError", "code": "incompatible_change", "message": "type Order: behavior Workflow cannot be added to type Order, which has instances: the instances that exist have no status to start from", "details": {"changes": [{"path": "Order.behaviors.Workflow", "message": "behavior Workflow cannot be added to type Order, which has instances: the instances that exist have no status to start from"}]}})
    );
}

#[test]
fn a_new_version_may_change_outcomes_as_it_may_change_transitions() {
    let failure = flow_with(json!({"outcomes": {"cancelled": "failure"}}));
    let mut host = Host::new(failure.clone());
    host.create("o1", START);
    changed(host.transition(&alice(), "cancelled"));
    let steps = [
        failure,
        flow_with(json!({"outcomes": {"cancelled": "neutral"}})),
        flow_with(json!({"outcomes": {"cancelled": "neutral", "shipped": "success"}})),
        order_flow(),
    ];
    for pair in steps.windows(2) {
        host.spec = order(pair[1].clone());
        changed(host.publish(order(pair[0].clone())));
    }
    // An outcome on a state a new transition leaves is no terminal state's.
    let mut leaves = flow_with(json!({"outcomes": {"cancelled": "failure"}}));
    leaves["transitions"]
        .as_array_mut()
        .expect("transitions")
        .push(json!({"from": "cancelled", "to": "draft"}));
    host.spec = order(leaves);
    let refusal = host
        .publish(order(order_flow()))
        .into_result()
        .expect_err("refused");
    assert_eq!(refusal.code, Code::InvalidSchema);
    assert_eq!(
        refusal.message,
        "type Order: behavior Workflow config: outcomes names \"cancelled\", which is not a terminal state: a transition leaves it, and only a terminal state has an outcome"
    );
}

// ---- the declaration ----

#[test]
fn the_core_holds_workflow_to_its_declaration() {
    let core = core();
    let declaration = core.declaration("Workflow").expect("registered");
    assert_eq!(declaration, &Workflow.declaration());
    assert!(declaration.has_field("status") && declaration.has_field("enteredAt"));
    // Its status is the value a field anchor's field is.
    assert_eq!(declaration.value.as_deref(), Some("status"));
    assert_eq!(
        declaration.find_operation("transition").map(|op| op.writes),
        Some(true)
    );
}
