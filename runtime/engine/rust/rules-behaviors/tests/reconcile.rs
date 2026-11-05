//! Reconcile on the pure core (D104, step 3), through the core's public
//! API over a source in memory that keeps rows and the deadlines the core
//! arms: the config held to the declaration file's schemas and to the
//! Workflow it moves, reachability along transitions; an observation and
//! the fewest transitions it moves the status along, its tie broken by the
//! config's order; a repeat, an older and an equal `at` answered before any
//! guard; a future `at`, a state the Workflow lacks and observePermission;
//! drift on `forbidden`, on `not_principal` (a `by` of fields and of a
//! seat), `missing_fields`, `approvals_missing`, `backing_off` and
//! `held_by_another`, each step's guards told it is the observer's own
//! request; `no_path` from a terminal state; drift cleared by an
//! observation the path reaches; the stale deadline armed, moved, cleared
//! at a terminal state, fired, armed again past a raw write of
//! `observedAt`, a no-op on a terminal row and its vetoed move recording
//! drift; a Workflow anchored on the type; and two Workflow fields.

mod support;

use serde_json::{json, Value};
use superschematic_rules::{
    Behavior, Caller, ConfigError, ConfigTarget, Declaration, FieldSpec, GuardRequest, Hook,
    Outcome, TypeSpec, UseSpec, Veto, View,
};
use superschematic_rules_behaviors::{core, reconcile, Reconcile};
use support::{caller, changed, define, refused, result, rule, shape, undescribed, Host, START};

const STATES: [&str; 7] = [
    "pending", "starting", "running", "stopping", "stopped", "failed", "lost",
];

// A sandbox's states: it starts, runs, stops, fails, and is lost when its
// provider stops answering, from which it may be found in any state.
fn flow() -> Value {
    let mut transitions = vec![
        json!({"from": "pending", "to": "starting"}),
        json!({"from": "starting", "to": "running"}),
        json!({"from": "running", "to": "stopping", "permission": "sandboxes.stop"}),
        json!({"from": "stopping", "to": "stopped"}),
        json!({"from": "stopped", "to": "starting"}),
    ];
    for from in ["pending", "starting", "running", "stopping"] {
        transitions.push(json!({"from": from, "to": "failed"}));
    }
    for from in ["pending", "starting", "running", "stopping", "stopped"] {
        transitions.push(json!({"from": from, "to": "lost"}));
    }
    for to in [
        "pending", "starting", "running", "stopping", "stopped", "failed",
    ] {
        transitions.push(json!({"from": "lost", "to": to}));
    }
    json!({"states": STATES, "transitions": transitions})
}

fn watch() -> Value {
    json!({"status": "status", "unreachable": "lost", "staleAfterMs": 60_000, "observePermission": "sandboxes.observe"})
}

// A sandbox as a table holds it: the uses it implements first, then its
// Workflow field and the Reconcile field beside it, with its name, an
// endpoint and the operator it names.
fn sandbox(implemented: Vec<UseSpec>, flow: Value, reconcile: Value) -> TypeSpec {
    let mut spec = TypeSpec::new("Sandbox");
    for entry in implemented {
        spec = spec.with(entry);
    }
    spec.with(UseSpec::on_field("status", "Workflow", flow))
        .with(UseSpec::on_field("observed", "Reconcile", reconcile))
        .with_field(FieldSpec::new("name", "string"))
        .with_field(FieldSpec::new("endpoint", "string"))
        .with_field(FieldSpec::new("operator", "string"))
}

fn hosting(spec: TypeSpec) -> Host {
    let mut host = Host::new(core(), spec);
    changed(host.create(&poller(), "s1", json!({"name": "box"})));
    host
}

fn poller() -> Caller {
    caller("poller", &["sandboxes.observe"])
}

fn operator() -> Caller {
    caller("operator", &["sandboxes.observe", "sandboxes.stop"])
}

fn runner() -> Caller {
    caller("runner", &[])
}

fn observe(host: &mut Host, who: &Caller, now: i64, state: &str, at: i64) -> Outcome {
    host.invoke_at(
        who,
        now,
        "s1",
        "observed",
        "observe",
        json!({ "state": state, "at": at }),
    )
}

fn status(host: &Host) -> Value {
    host.row("s1").owned["status"]["status"].clone()
}

// observed is Reconcile's owned fields on s1, a field with no value left
// out.
fn observed(host: &Host) -> Value {
    Value::Object(
        host.row("s1")
            .owned
            .get("observed")
            .into_iter()
            .flatten()
            .filter(|(_, value)| !value.is_null())
            .map(|(field, value)| (field.clone(), value.clone()))
            .collect(),
    )
}

// stale is when the row's stale deadline falls due; None when unarmed.
fn stale(host: &Host) -> Option<i64> {
    host.memory
        .armed
        .get(&("s1".to_owned(), "observed".to_owned(), "stale".to_owned()))
        .copied()
}

fn moves(pairs: &[(&str, &str)]) -> Value {
    Value::Array(
        pairs
            .iter()
            .map(|(from, to)| json!({"from": from, "to": to}))
            .collect(),
    )
}

// ---- the declaration and its schemas ----

#[test]
fn the_declaration_is_the_files() {
    assert_eq!(
        Reconcile.declaration(),
        Declaration::new("Reconcile")
            .value("observed")
            .field("observedAt")
            .field("drift")
            .field("reconciledAt")
            .arms_deadlines()
            .operation("observe", true)
    );
    let file: Value = serde_json::from_str(reconcile::DECLARATION).expect("it parses");
    assert_eq!(file["value"], "observed");
    assert_eq!(file["fields"][0]["optional"], true);
    assert_eq!(file["requires"], json!(["Workflow"]));
    assert_eq!(file.get("createParamsSchema"), None);
    assert_eq!(file.get("vetoes"), None);
}

/// The checks Reconcile writes by hand are the declaration's configSchema
/// and observe's paramsSchema, keyword for keyword, each issue in the
/// schema loader's or the behavior registry's words, and what observe
/// answers is its resultSchema.
#[test]
fn the_config_and_parameter_checks_are_the_declarations_schemas() {
    let file: Value = serde_json::from_str(reconcile::DECLARATION).expect("it parses");
    let text = json!({"minLength": 1, "type": "string"});
    assert_eq!(
        undescribed(&file["configSchema"]),
        json!({
            "additionalProperties": false,
            "properties": {
                "observePermission": text,
                "staleAfterMs": {"minimum": 1000, "type": "integer"},
                "status": text,
                "unreachable": text
            },
            "required": ["status", "observePermission"],
            "type": "object"
        })
    );
    let operation = &file["operations"][0];
    assert_eq!(operation["name"], "observe");
    assert_eq!(
        undescribed(&operation["paramsSchema"]),
        json!({
            "additionalProperties": false,
            "properties": {
                "at": {"minimum": 0, "type": "integer"},
                "state": text
            },
            "required": ["state", "at"],
            "type": "object"
        })
    );
    let state = json!({"type": "string"});
    assert_eq!(
        undescribed(&operation["resultSchema"]),
        json!({
            "additionalProperties": false,
            "properties": {
                "drift": {
                    "additionalProperties": false,
                    "properties": {"code": state, "observed": state, "status": state},
                    "required": ["observed", "code"],
                    "type": "object"
                },
                "moves": {"items": {
                    "additionalProperties": false,
                    "properties": {"from": state, "to": state},
                    "required": ["from", "to"],
                    "type": "object"
                }, "type": "array"},
                "observed": state,
                "observedAt": {"type": "integer"},
                "status": {"type": ["string", "null"]},
                "unchanged": {"type": "boolean"}
            },
            "required": ["observed", "observedAt", "status", "moves"],
            "type": "object"
        })
    );
    let shaped = |config: Value| shape(&core(), &sandbox(Vec::new(), flow(), config));
    assert_eq!(shaped(json!("x")), ["/config: must be object"]);
    assert_eq!(
        shaped(json!({"x": 1})),
        [
            "/config: unknown key \"x\"",
            "/config: must have required property 'observePermission'",
            "/config: must have required property 'status'"
        ]
    );
    assert_eq!(
        shaped(
            json!({"status": "", "unreachable": 5, "staleAfterMs": 99.5, "observePermission": ""})
        ),
        [
            "/config/observePermission: must NOT have fewer than 1 characters",
            "/config/staleAfterMs: must be >= 1000",
            "/config/staleAfterMs: must be integer",
            "/config/status: must NOT have fewer than 1 characters",
            "/config/unreachable: must be string"
        ]
    );
    let mut host = hosting(sandbox(Vec::new(), flow(), watch()));
    let issues = |outcome| refused(outcome)["details"].clone();
    assert_eq!(
        issues(host.invoke(&poller(), "s1", "observed", "observe", json!({"x": 1}))),
        json!({"use": "observed", "issues": [
            {"path": "", "message": "must NOT have additional properties: x"},
            {"path": "", "message": "must have required property 'at'"},
            {"path": "", "message": "must have required property 'state'"}
        ]})
    );
    assert_eq!(
        issues(host.invoke(
            &poller(),
            "s1",
            "observed",
            "observe",
            json!({"state": "", "at": 1.5})
        )),
        json!({"use": "observed", "issues": [
            {"path": "/at", "message": "must be integer"},
            {"path": "/state", "message": "must NOT have fewer than 1 characters"}
        ]})
    );
}

/// Each rule of the config, the first refused: status names a Workflow;
/// unreachable and staleAfterMs come together; unreachable is a state,
/// which every state but a terminal one reaches along transitions, and
/// from which every state is reached.
#[test]
fn the_config_rules_hold_unreachable_to_the_workflow_along_transitions() {
    let ruled = |flow: Value, config: Value| {
        let spec = sandbox(Vec::new(), flow, config);
        let refusal = define(&core(), &spec).expect_err("refused");
        refusal
            .message
            .strip_prefix("type Sandbox: field observed: behavior Reconcile config: ")
            .unwrap_or(&refusal.message)
            .to_owned()
    };
    let with = |members: Value| {
        let mut config = watch();
        for (key, value) in members.as_object().expect("an object") {
            if value.is_null() {
                config.as_object_mut().expect("an object").remove(key);
            } else {
                config[key] = value.clone();
            }
        }
        config
    };
    assert_eq!(
        ruled(flow(), with(json!({"status": "phase"}))),
        "status \"phase\" is not a Workflow of the type (its Workflows: status)"
    );
    assert_eq!(
        ruled(flow(), with(json!({"staleAfterMs": null}))),
        "unreachable names the state the stale deadline moves a row to, and the config gives no staleAfterMs to arm it"
    );
    assert_eq!(
        ruled(flow(), with(json!({"unreachable": null}))),
        "staleAfterMs arms a deadline that moves a row to unreachable, and the config names no unreachable state"
    );
    assert_eq!(
        ruled(flow(), with(json!({"unreachable": "gone"}))),
        "unreachable \"gone\" is not a state of the type's Workflow (pending, starting, running, stopping, stopped, failed, lost)"
    );
    // stopped reaches lost through starting, so a flow without
    // stopped→lost is taken; one in which running reaches no lost is not.
    let mut through = flow();
    let edges = through["transitions"].as_array_mut().expect("transitions");
    edges.retain(|edge| edge != &json!({"from": "stopped", "to": "lost"}));
    assert!(define(&core(), &sandbox(Vec::new(), through, watch())).is_ok());
    let paused = json!({"states": ["pending", "running", "paused", "failed", "lost"], "transitions": [
        {"from": "pending", "to": "running"}, {"from": "running", "to": "paused"},
        {"from": "paused", "to": "running"}, {"from": "running", "to": "failed"},
        {"from": "pending", "to": "lost"}, {"from": "lost", "to": "pending"},
        {"from": "lost", "to": "running"}, {"from": "lost", "to": "paused"}, {"from": "lost", "to": "failed"}
    ]});
    assert_eq!(
        ruled(paused, watch()),
        "unreachable \"lost\" cannot be reached from \"running\", which is not a terminal state: the stale deadline moves a row there from any state but a terminal one"
    );
    // From lost, every state; pending reached only along lost→pending.
    let mut no_return = flow();
    let edges = no_return["transitions"]
        .as_array_mut()
        .expect("transitions");
    edges.retain(|edge| edge != &json!({"from": "lost", "to": "pending"}));
    assert_eq!(
        ruled(no_return, watch()),
        "\"pending\" cannot be reached from unreachable \"lost\": an observation moves a row from it to any state"
    );
    // With no Workflow on the type, its Workflows are none.
    let alone = TypeSpec::new("Sandbox").with(UseSpec::on_field("observed", "Reconcile", watch()));
    assert_eq!(
        rule(&core(), &alone, "Reconcile"),
        "type Sandbox: field observed: behavior Reconcile config: status \"status\" is not a Workflow of the type (its Workflows: none)"
    );
    // Neither unreachable nor staleAfterMs is taken.
    assert!(define(
        &core(),
        &sandbox(
            Vec::new(),
            flow(),
            json!({"status": "status", "observePermission": "x"})
        )
    )
    .is_ok());
}

// ---- observations ----

/// An observation records observed and observedAt and moves the status
/// along the fewest transitions, each through Workflow's transition, then
/// sets reconciledAt; the tie between two shortest paths goes to the one
/// whose first step the config lists first.
#[test]
fn an_observation_moves_the_status_along_the_fewest_transitions() {
    let mut host = hosting(sandbox(Vec::new(), flow(), watch()));
    assert_eq!(observed(&host), json!({}));
    assert_eq!(
        result(observe(
            &mut host,
            &poller(),
            1_002_000,
            "running",
            1_001_000
        )),
        json!({"observed": "running", "observedAt": 1_001_000, "status": "running",
            "moves": moves(&[("pending", "starting"), ("starting", "running")])})
    );
    assert_eq!(status(&host), "running");
    assert_eq!(
        observed(&host),
        json!({"observed": "running", "observedAt": 1_001_000, "reconciledAt": 1_002_000})
    );
    assert_eq!(host.row("s1").owned["status"]["enteredAt"], 1_002_000);
    // Of two shortest paths, the config's first.
    let diamond = json!({"states": ["draft", "left", "right", "done"], "transitions": [
        {"from": "draft", "to": "right"}, {"from": "draft", "to": "left"},
        {"from": "left", "to": "done"}, {"from": "right", "to": "done"}
    ]});
    let mut host = hosting(sandbox(
        Vec::new(),
        diamond,
        json!({"status": "status", "observePermission": "sandboxes.observe"}),
    ));
    assert_eq!(
        result(observe(&mut host, &poller(), START, "done", START))["moves"],
        moves(&[("draft", "right"), ("right", "done")])
    );
    // No stale deadline without staleAfterMs.
    assert_eq!(stale(&host), None);
}

// Wall: a test behavior whose guard vetoes every write of its row, so a
// call that reaches the guards is refused.
struct Wall;

impl Behavior for Wall {
    type Config = ();

    fn declaration(&self) -> Declaration {
        Declaration::new("test.Wall").veto("walled")
    }

    fn parse_config(&self, _raw: &Value, _target: &ConfigTarget<'_>) -> Result<(), ConfigError> {
        Ok(())
    }

    fn guard(&self, _view: &View<'_, ()>, request: &GuardRequest<'_>) -> Hook<Option<Veto>> {
        Ok(match request {
            GuardRequest::Operation { writes: true, .. } => {
                Some(Veto::new("the row is walled").with_code("walled"))
            }
            _ => None,
        })
    }
}

/// An observation no newer than observedAt, an equal one and a repeat
/// included, answers unchanged and writes nothing, before any guard: a
/// guard that vetoes every write does not refuse it, and holds a newer one.
#[test]
fn an_older_and_an_equal_observation_answer_unchanged_before_any_guard() {
    let mut host = hosting(sandbox(Vec::new(), flow(), watch()));
    changed(observe(
        &mut host,
        &poller(),
        1_000_500,
        "running",
        1_000_500,
    ));
    // From running the path to stopped goes through stopping, which needs
    // sandboxes.stop.
    changed(observe(
        &mut host,
        &poller(),
        1_001_000,
        "stopped",
        1_001_000,
    ));
    let drifted = observed(&host);
    host.core = core().with(Wall);
    host.spec = sandbox(vec![UseSpec::on_type("test.Wall", None)], flow(), watch());
    let version = host.row("s1").version;
    for (state, at) in [
        ("stopped", 1_001_000),
        ("pending", 1_000_500),
        ("running", 1_001_000),
    ] {
        let outcome = observe(&mut host, &poller(), 1_003_000, state, at);
        assert_eq!(changed(outcome.clone()).rows, Vec::new());
        assert_eq!(
            result(outcome),
            json!({"observed": "stopped", "observedAt": 1_001_000, "status": "running", "moves": [],
                "drift": {"status": "running", "observed": "stopped", "code": "forbidden"}, "unchanged": true})
        );
    }
    assert_eq!(host.row("s1").version, version);
    assert_eq!(observed(&host), drifted);
    assert_eq!(
        refused(observe(
            &mut host,
            &poller(),
            1_003_000,
            "running",
            1_002_000
        ))["details"]["vetoCode"],
        "walled"
    );
}

/// An at after the command's time and a state the Workflow lacks are bad
/// parameters, together; a caller without observePermission is forbidden,
/// before the repeat, so it learns nothing of the row.
#[test]
fn a_future_at_a_state_the_workflow_lacks_and_observe_permission() {
    let mut host = hosting(sandbox(Vec::new(), flow(), watch()));
    assert_eq!(
        refused(observe(
            &mut host,
            &poller(),
            1_006_000,
            "hibernating",
            1_007_000
        )),
        json!({"class": "OperationParamsError", "code": "invalid_argument",
        "message": "operation observe of behavior Reconcile on field observed: /at: 1970-01-01T00:16:47.000Z is after the command's time, 1970-01-01T00:16:46.000Z: an observation reports a state already seen; /state: \"hibernating\" is not a state of Sandbox.status (pending, starting, running, stopping, stopped, failed, lost)",
        "details": {"use": "observed", "issues": [
            {"path": "/at", "message": "1970-01-01T00:16:47.000Z is after the command's time, 1970-01-01T00:16:46.000Z: an observation reports a state already seen"},
            {"path": "/state", "message": "\"hibernating\" is not a state of Sandbox.status (pending, starting, running, stopping, stopped, failed, lost)"}
        ]}})
    );
    changed(observe(
        &mut host,
        &poller(),
        1_006_000,
        "running",
        1_006_000,
    ));
    // A repeat by a caller who may not observe is forbidden, not unchanged.
    assert_eq!(
        refused(observe(
            &mut host,
            &caller("carol", &[]),
            1_006_000,
            "running",
            1_006_000
        )),
        json!({"class": "EngineError", "code": "forbidden",
            "message": "carol may not observe Sandbox s1's observed: observe needs permission sandboxes.observe"})
    );
}

// Told: a test behavior that vetoes every transition of the status made
// as the caller's own request, so a step an observation makes is held and
// one the stale deadline makes, the worker's, is not.
struct Told;

impl Behavior for Told {
    type Config = ();

    fn declaration(&self) -> Declaration {
        Declaration::new("test.Told").veto("told")
    }

    fn parse_config(&self, _raw: &Value, _target: &ConfigTarget<'_>) -> Result<(), ConfigError> {
        Ok(())
    }

    fn guard(&self, _view: &View<'_, ()>, request: &GuardRequest<'_>) -> Hook<Option<Veto>> {
        let told = matches!(
            request,
            GuardRequest::Operation {
                operation: "transition",
                as_caller: true,
                fired: false,
                ..
            }
        );
        Ok(told.then(|| Veto::new("a step as the caller").with_code("told")))
    }
}

/// A step the observer may not make stops the path and records drift with
/// the refusal's code: forbidden for a transition's permission, and every
/// other use's veto (Workflow's by of a field and of a seat, StateRules,
/// Approvals, Retries, another principal's lease with observe exempt);
/// each step is told it is the observer's own request; and an observation
/// the path reaches clears drift.
#[test]
fn a_refused_step_records_drift_with_its_code() {
    // forbidden, then an observer who may completes it.
    let mut host = hosting(sandbox(Vec::new(), flow(), watch()));
    changed(observe(
        &mut host,
        &poller(),
        1_001_000,
        "running",
        1_001_000,
    ));
    assert_eq!(
        result(observe(
            &mut host,
            &poller(),
            1_002_000,
            "stopped",
            1_002_000
        )),
        json!({"observed": "stopped", "observedAt": 1_002_000, "status": "running", "moves": [],
            "drift": {"status": "running", "observed": "stopped", "code": "forbidden"}})
    );
    assert_eq!(
        observed(&host),
        json!({"observed": "stopped", "observedAt": 1_002_000, "reconciledAt": 1_001_000,
            "drift": {"status": "running", "observed": "stopped", "code": "forbidden"}})
    );
    changed(observe(
        &mut host,
        &operator(),
        1_003_000,
        "stopped",
        1_003_000,
    ));
    assert_eq!(
        observed(&host),
        json!({"observed": "stopped", "observedAt": 1_003_000, "reconciledAt": 1_003_000})
    );
    // drift's code for each use that holds a step.
    let by_field = {
        let mut flow = flow();
        flow["transitions"][1]["by"] = json!(["operator"]);
        flow
    };
    let by_seat = {
        let mut flow = flow();
        flow["transitions"][1]["by"] = json!(["seat:ops"]);
        flow
    };
    let retries = json!({
        "classes": {"timeout": {"attempts": 9, "backoff": {"firstMs": 60_000, "maxMs": 60_000}}},
        "totalAttempts": 9, "exhaustedState": "failed", "retryInto": ["running"]
    });
    let cases: Vec<(TypeSpec, &str, &str)> = vec![
        (
            sandbox(Vec::new(), by_field, watch()),
            "starting",
            "not_principal",
        ),
        (
            sandbox(
                vec![UseSpec::on_type("Seats", json!({"seats": {"ops": {}}}))],
                by_seat,
                watch(),
            ),
            "starting",
            "not_principal",
        ),
        (
            sandbox(
                vec![UseSpec::on_type(
                    "StateRules",
                    json!({"states": {"running": {"required": ["endpoint"]}}}),
                )],
                flow(),
                watch(),
            ),
            "starting",
            "missing_fields",
        ),
        (
            sandbox(
                vec![UseSpec::on_type(
                    "Approvals",
                    json!({"gates": {"running": {"count": 1, "permission": "sandboxes.approve", "excludeCreator": false}}}),
                )],
                flow(),
                watch(),
            ),
            "starting",
            "approvals_missing",
        ),
        (
            sandbox(vec![UseSpec::on_type("test.Told", None)], flow(), watch()),
            "pending",
            "told",
        ),
    ];
    for (spec, stopped_at, code) in cases {
        let mut host = Host::new(core().with(Told), spec);
        changed(host.create(&poller(), "s1", json!({"name": "box"})));
        assert_eq!(
            result(observe(
                &mut host,
                &poller(),
                1_001_000,
                "running",
                1_001_000
            ))["drift"],
            json!({"status": stopped_at, "observed": "running", "code": code}),
            "{code}"
        );
        assert_eq!(status(&host), stopped_at, "{code}");
    }
    // Retries' backoff holds the move into running.
    let mut host = Host::new(
        core(),
        sandbox(vec![UseSpec::on_type("Retries", retries)], flow(), watch()),
    );
    changed(host.create(&poller(), "s1", json!({"name": "box"})));
    changed(host.invoke(
        &poller(),
        "s1",
        "Retries",
        "recordAttempt",
        json!({"failure": "timeout"}),
    ));
    assert_eq!(
        result(observe(
            &mut host,
            &poller(),
            1_001_000,
            "running",
            1_001_000
        ))["drift"],
        json!({"status": "starting", "observed": "running", "code": "backing_off"})
    );
    // Another principal's active lease holds each step, observe exempt.
    let mut host = Host::new(
        core(),
        sandbox(
            vec![UseSpec::on_type(
                "Lease",
                json!({"exempt": ["Reconcile.observe"]}),
            )],
            flow(),
            watch(),
        ),
    );
    changed(host.create(&poller(), "s1", json!({"name": "box"})));
    changed(host.invoke(&caller("wren", &[]), "s1", "Lease", "acquire", json!({})));
    assert_eq!(
        result(observe(
            &mut host,
            &poller(),
            1_001_000,
            "running",
            1_001_000
        ))["drift"],
        json!({"status": "pending", "observed": "running", "code": "held_by_another"})
    );
    // Without the exempt, the observe itself is held, and records nothing.
    let mut host = Host::new(
        core(),
        sandbox(vec![UseSpec::on_type("Lease", json!({}))], flow(), watch()),
    );
    changed(host.create(&poller(), "s1", json!({"name": "box"})));
    changed(host.invoke(&caller("wren", &[]), "s1", "Lease", "acquire", json!({})));
    assert_eq!(
        refused(observe(
            &mut host,
            &poller(),
            1_001_000,
            "running",
            1_001_000
        ))["details"]["vetoCode"],
        "held_by_another"
    );
    assert_eq!(observed(&host), json!({}));
}

/// With no path from a terminal state the drift is no_path; a row with no
/// status, which only a raw write leaves, drifts as no_status.
#[test]
fn no_path_from_a_terminal_state_and_no_status() {
    let mut host = hosting(sandbox(Vec::new(), flow(), watch()));
    assert_eq!(
        result(observe(
            &mut host,
            &poller(),
            1_001_000,
            "failed",
            1_001_000
        ))["moves"],
        moves(&[("pending", "failed")])
    );
    // A terminal status clears the stale deadline.
    assert_eq!(stale(&host), None);
    assert_eq!(
        result(observe(
            &mut host,
            &poller(),
            1_002_000,
            "running",
            1_002_000
        )),
        json!({"observed": "running", "observedAt": 1_002_000, "status": "failed", "moves": [],
            "drift": {"status": "failed", "observed": "running", "code": "no_path"}})
    );
    host.memory
        .rows
        .get_mut("s1")
        .expect("the row")
        .owned
        .get_mut("status")
        .expect("its status")
        .insert("status".into(), Value::Null);
    assert_eq!(
        result(observe(
            &mut host,
            &poller(),
            1_003_000,
            "running",
            1_003_000
        )),
        json!({"observed": "running", "observedAt": 1_003_000, "status": null, "moves": [],
            "drift": {"observed": "running", "code": "no_status"}})
    );
}

// ---- the stale deadline ----

/// staleAfterMs arms stale from each observation's at; a newer one moves
/// it; its fire, as the runner, sets observed to unreachable and moves the
/// row there along the fewest transitions; a fire whose observedAt a raw
/// write moved past it arms again; a terminal row is left as it is; and a
/// vetoed move records drift and is spent.
#[test]
fn the_stale_deadline_moves_a_row_no_observation_reached_to_unreachable() {
    let mut host = hosting(sandbox(Vec::new(), flow(), watch()));
    changed(observe(&mut host, &poller(), START, "running", START));
    assert_eq!(stale(&host), Some(1_060_000));
    changed(observe(
        &mut host,
        &poller(),
        1_030_000,
        "running",
        1_030_000,
    ));
    assert_eq!(stale(&host), Some(1_090_000));
    assert!(host.fall_due(1_060_000, &runner()).is_empty());
    // A raw write moves observedAt past the deadline: its fire arms again.
    host.memory
        .rows
        .get_mut("s1")
        .expect("the row")
        .owned
        .get_mut("observed")
        .expect("its observed")
        .insert("observedAt".into(), json!(1_050_000));
    let fired = host.fall_due(1_090_000, &runner());
    assert_eq!(fired.len(), 1);
    changed(fired[0].1.clone());
    assert_eq!(stale(&host), Some(1_110_000));
    assert_eq!(status(&host), "running");
    let fired = host.fall_due(1_110_000, &runner());
    changed(fired[0].1.clone());
    assert_eq!(status(&host), "lost");
    assert_eq!(
        observed(&host),
        json!({"observed": "lost", "observedAt": 1_050_000, "reconciledAt": 1_110_000})
    );
    assert_eq!(stale(&host), None);
    // A newer observation moves it back and arms it again.
    assert_eq!(
        result(observe(
            &mut host,
            &poller(),
            1_120_000,
            "running",
            1_120_000
        ))["moves"],
        moves(&[("lost", "running")])
    );
    assert_eq!(stale(&host), Some(1_180_000));
    // A row a raw write left terminal is left as it is.
    host.memory
        .rows
        .get_mut("s1")
        .expect("the row")
        .owned
        .get_mut("status")
        .expect("its status")
        .insert("status".into(), json!("failed"));
    let fired = host.fall_due(1_180_000, &runner());
    changed(fired[0].1.clone());
    assert_eq!(status(&host), "failed");
    assert_eq!(observed(&host)["observed"], "running");
    // A vetoed stale move records drift, and the deadline is spent.
    let mut host = hosting(sandbox(
        vec![UseSpec::on_type(
            "StateRules",
            json!({"states": {"lost": {"required": ["endpoint"]}}}),
        )],
        flow(),
        watch(),
    ));
    changed(observe(&mut host, &poller(), START, "running", START));
    let fired = host.fall_due(1_060_000, &runner());
    changed(fired[0].1.clone());
    assert_eq!(status(&host), "running");
    assert_eq!(
        observed(&host),
        json!({"observed": "lost", "observedAt": START, "reconciledAt": START,
            "drift": {"status": "running", "observed": "lost", "code": "missing_fields"}})
    );
    assert_eq!(stale(&host), None);
}

/// The stale deadline's moves are the worker's (`fired`), not a caller's:
/// a guard that holds every step made as the caller's own request lets
/// them through.
#[test]
fn the_stale_deadlines_moves_are_the_workers() {
    let mut host = Host::new(
        core().with(Told),
        sandbox(vec![UseSpec::on_type("test.Told", None)], flow(), watch()),
    );
    changed(host.create(&poller(), "s1", json!({"name": "box"})));
    changed(observe(&mut host, &poller(), START, "running", START));
    assert_eq!(status(&host), "pending");
    let fired = host.fall_due(1_060_000, &runner());
    changed(fired[0].1.clone());
    assert_eq!(status(&host), "lost");
}

// ---- where the Workflow sits ----

/// A Workflow anchored on the type is named Workflow, and a refusal names
/// its states by the type; with two Workflow fields, status names the one
/// observations move, and the other stays where it is.
#[test]
fn a_workflow_on_the_type_and_one_of_two_fields() {
    let spec = TypeSpec::new("Sandbox")
        .with(UseSpec::on_type("Workflow", flow()))
        .with(UseSpec::on_field(
            "observed",
            "Reconcile",
            json!({"status": "Workflow", "observePermission": "sandboxes.observe"}),
        ))
        .with_field(FieldSpec::new("name", "string"));
    let mut host = hosting(spec);
    assert_eq!(
        refused(observe(&mut host, &poller(), START, "gone", START))["details"]["issues"][0]["message"],
        "\"gone\" is not a state of Sandbox (pending, starting, running, stopping, stopped, failed, lost)"
    );
    changed(observe(&mut host, &poller(), START, "running", START));
    assert_eq!(host.row("s1").owned["Workflow"]["status"], "running");
    // Two Workflow fields.
    let phase =
        json!({"states": ["draft", "live"], "transitions": [{"from": "draft", "to": "live"}]});
    let spec = TypeSpec::new("Sandbox")
        .with(UseSpec::on_field("phase", "Workflow", phase))
        .with(UseSpec::on_field("status", "Workflow", flow()))
        .with(UseSpec::on_field(
            "observed",
            "Reconcile",
            json!({"status": "status", "observePermission": "sandboxes.observe"}),
        ))
        .with_field(FieldSpec::new("name", "string"));
    let mut host = hosting(spec);
    changed(observe(&mut host, &poller(), START, "running", START));
    assert_eq!(status(&host), "running");
    assert_eq!(host.row("s1").owned["phase"]["status"], "draft");
    assert_eq!(
        refused(observe(&mut host, &poller(), START + 1, "live", START + 1))["details"]["issues"][0]["message"],
        "\"live\" is not a state of Sandbox.status (pending, starting, running, stopping, stopped, failed, lost)"
    );
}

/// Reconcile allows any config change, and joins and leaves a type with
/// rows.
#[test]
fn any_config_change_is_allowed() {
    let mut host = hosting(sandbox(Vec::new(), flow(), watch()));
    changed(observe(&mut host, &poller(), START, "running", START));
    let publish = |before: TypeSpec, after: TypeSpec, host: &mut Host| {
        let command = superschematic_rules::Command::new(
            poller(),
            START,
            superschematic_rules::Action::Publish { from: Some(before) },
        );
        host.core
            .drive(&after, &command, &mut host.memory)
            .into_result()
            .map(|_| ())
    };
    let shorter = sandbox(
        Vec::new(),
        flow(),
        json!({"status": "status", "unreachable": "lost", "staleAfterMs": 1000, "observePermission": "x"}),
    );
    let without = TypeSpec {
        uses: vec![UseSpec::on_field("status", "Workflow", flow())],
        ..sandbox(Vec::new(), flow(), watch())
    };
    assert!(publish(sandbox(Vec::new(), flow(), watch()), shorter, &mut host).is_ok());
    assert!(publish(
        sandbox(Vec::new(), flow(), watch()),
        without.clone(),
        &mut host
    )
    .is_ok());
    assert!(publish(without, sandbox(Vec::new(), flow(), watch()), &mut host).is_ok());
}
