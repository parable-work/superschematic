//! Reactions' changes (D56): the cases of
//! `test/core-reactions-changes.test.ts` (a rule fires when a create, an
//! update or an operation of the instance moves a field's value, an own
//! field or a behavior's by qualified name with a member path after it, to
//! `to` and from `from` when they are given; a create moves a field from
//! absent and a delete moves nothing; a value the event keeps in the value
//! store is compared once loaded; a rule on the instance itself acts only
//! while the field still holds `to`; changes rules on Workflow.status
//! chain, to the runner's depth limit, and join enters rules' cycle check;
//! the guidance the describe document shows; parseConfig's refusals of an
//! unknown field, a volatile one and Workflow.enteredAt), on SQLite and,
//! with the variable set, on Postgres. test.Score and Workflow.Tally are
//! the test kit's ports of the file's behaviors.

mod support;

use serde_json::{json, Value};

use superschematic_engine::{
    BehaviorSet, Clock, CreateOptions, DefineOptions, Engine, EngineEvent, EngineOptions, Error,
    GetOptions, Principal, ReadEvents, RunnerOptions, SchemaTarget, ToolTarget, ValueOptions,
    WriteOptions,
};
use superschematic_engine_behaviors::{
    Approvals, Comments, Constants, Dependencies, Links, Numbering, Reactions, Revisions, Rollups,
    StateRules, Timers, Variants, Workflow,
};
use superschematic_engine_testkit::behaviors::{Score, WorkflowTally};
use support::{alice, issues_of, refusal, schema_document, targets, Target};

/// The core's behaviors with Rollups and Reactions, in the TypeScript
/// engine's order.
fn behaviors() -> BehaviorSet {
    BehaviorSet::new()
        .with(Workflow)
        .with(Comments)
        .with(Revisions)
        .with(Dependencies)
        .with(Links)
        .with(Rollups)
        .with(Reactions)
        .with(Constants)
        .with(Variants)
        .with(StateRules)
        .with(Approvals)
        .with(Numbering)
        .with(Timers)
}

fn runner() -> Principal {
    Principal::new("runner", Vec::<String>::new())
}

// open opens an engine with the core's meta-schema, the runner and a clock
// that stands still, its options changed by `configure`.
fn open_with(target: &Target, configure: impl FnOnce(&mut EngineOptions)) -> Engine {
    target.open_core_with(|options| {
        options.behaviors = behaviors();
        options.clock = Clock::Manual(1_000_000);
        options.runner = Some(RunnerOptions::new(runner()));
        configure(options);
    })
}

fn open(target: &Target) -> Engine {
    open_with(target, |_| {})
}

// open_extended opens an engine with the open meta-schema, which admits
// the extension behaviors test.Score and Workflow.Tally, besides the
// core's.
fn open_extended(target: &Target, extra: BehaviorSet) -> Engine {
    target.open_with(behaviors().extend(extra), |options| {
        options.clock = Clock::Manual(1_000_000);
        options.runner = Some(RunnerOptions::new(runner()));
    })
}

// A ticket is triaged, escalated or closed; an escalated one goes back to
// triage. Its priority and body are its own.
fn ticket_flow() -> Value {
    json!({
        "name": "Workflow",
        "config": {
            "states": ["new", "triaged", "escalated", "closed"],
            "transitions": [
                {"from": "new", "to": "triaged"},
                {"from": "new", "to": "escalated"},
                {"from": "triaged", "to": "escalated"},
                {"from": "escalated", "to": "triaged"},
                {"from": "triaged", "to": "closed"},
                {"from": "escalated", "to": "closed"}
            ]
        }
    })
}

fn links_to_project() -> Value {
    json!({"name": "Links", "config": {"links": {"project": {"schema": "Project"}}}})
}

fn ticket_schema(behaviors: Value) -> Value {
    let mut document = schema_document(
        "Ticket",
        json!([
            {"name": "title", "typeRef": {"name": "string"}, "required": true},
            {"name": "priority", "typeRef": {"name": "string"}},
            {"name": "body", "typeRef": {"name": "string"}}
        ]),
        json!({}),
    );
    document["types"]["Ticket"]["behaviors"] = behaviors;
    document
}

fn tickets(rules: Value, extra: &[Value]) -> Value {
    let mut behaviors = vec![ticket_flow()];
    behaviors.extend(extra.iter().cloned());
    behaviors.push(json!({"name": "Reactions", "config": {"rules": rules}}));
    ticket_schema(Value::Array(behaviors))
}

// review is Revisions with a review step, whose propose moves
// pendingProposals.
fn review() -> Value {
    json!({"name": "Revisions", "config": {"review": {"permission": "tickets.review"}}})
}

fn projects() -> Value {
    let mut document = schema_document(
        "Project",
        json!([{"name": "title", "typeRef": {"name": "string"}, "required": true}]),
        json!({}),
    );
    document["types"]["Project"]["behaviors"] = json!([{
        "name": "Workflow",
        "config": {"states": ["planned", "active", "done"], "transitions": [{"from": "planned", "to": "active"}, {"from": "active", "to": "done"}]}
    }]);
    document
}

fn define(engine: &Engine, document: &Value) -> Result<(), Error> {
    engine
        .schemas()
        .define(&alice(), document, DefineOptions::default())
        .map(|_| ())
}

fn publish(engine: &Engine, document: &Value) {
    define(engine, document).expect("define");
    engine
        .schemas()
        .publish(
            &alice(),
            document["name"].as_str().expect("a name"),
            SchemaTarget::default(),
        )
        .expect("publish");
}

fn create(engine: &Engine, schema: &str, id: &str, data: Value, behaviors: Option<Value>) {
    engine
        .instances()
        .create(
            &alice(),
            schema,
            &data,
            CreateOptions {
                id: Some(id),
                behaviors: behaviors.as_ref(),
                ..Default::default()
            },
        )
        .expect("create");
}

fn update(engine: &Engine, schema: &str, id: &str, patch: Value) {
    engine
        .instances()
        .update(&alice(), schema, id, &patch, WriteOptions::default())
        .expect("update");
}

fn invoke(engine: &Engine, schema: &str, id: &str, operation: &str, params: Value) -> Value {
    engine
        .instances()
        .invoke(
            &alice(),
            schema,
            id,
            operation,
            &params,
            WriteOptions::default(),
        )
        .expect("invoke")
}

fn move_to(engine: &Engine, schema: &str, id: &str, to: &str) {
    invoke(engine, schema, id, "transition", json!({ "to": to }));
}

fn status(engine: &Engine, schema: &str, id: &str) -> Value {
    engine
        .instances()
        .get(&alice(), schema, id, GetOptions::default())
        .expect("get")
        .expect("the instance")
        .behaviors["Workflow"]["status"]
        .clone()
}

fn events(engine: &Engine, schema: &str, id: &str) -> Vec<EngineEvent> {
    engine
        .events()
        .read(
            &alice(),
            &ReadEvents {
                schema: Some(schema),
                instance_id: Some(id),
                limit: Some(500),
                ..ReadEvents::default()
            },
        )
        .expect("events")
        .events
}

// moves lists the Reactions moves of an instance: the state each moved it
// to, and its cause's depth.
fn moves(engine: &Engine, schema: &str, id: &str) -> Vec<(Value, i64)> {
    events(engine, schema, id)
        .into_iter()
        .filter_map(|event| {
            let cause = event.cause?;
            (cause.behavior == "Reactions")
                .then(|| (event.change["params"]["to"].clone(), cause.depth))
        })
        .collect()
}

fn moved(states: &[(&str, i64)]) -> Vec<(Value, i64)> {
    states
        .iter()
        .map(|(state, depth)| (json!(state), *depth))
        .collect()
}

fn run_due(engine: &Engine) {
    engine.runner().run_due().expect("run due");
}

#[test]
fn an_own_field_a_create_moves_it_from_absent_and_an_update_that_moves_it_fires() {
    for target in targets("changes_own") {
        let engine = open(&target);
        publish(
            &engine,
            &tickets(
                json!([{"when": {"changes": {"field": "priority", "to": "urgent"}}, "then": {"transition": "escalated"}}]),
                &[],
            ),
        );
        create(
            &engine,
            "Ticket",
            "a",
            json!({"title": "a", "priority": "urgent"}),
            None,
        );
        create(
            &engine,
            "Ticket",
            "b",
            json!({"title": "b", "priority": "low"}),
            None,
        );
        run_due(&engine);
        assert_eq!(
            [
                status(&engine, "Ticket", "a"),
                status(&engine, "Ticket", "b")
            ],
            [json!("escalated"), json!("new")]
        );
        let a = events(&engine, "Ticket", "a");
        assert_eq!(
            serde_json::to_value(&a.last().expect("an event").cause).expect("the cause"),
            json!({"behavior": "Reactions", "event": a[0].cursor, "depth": 1})
        );

        // A patch that leaves priority out, or gives the value it holds,
        // moves nothing.
        update(&engine, "Ticket", "b", json!({"title": "b, again"}));
        run_due(&engine);
        assert_eq!(status(&engine, "Ticket", "b"), "new");
        update(&engine, "Ticket", "b", json!({"priority": "urgent"}));
        run_due(&engine);
        assert_eq!(status(&engine, "Ticket", "b"), "escalated");
        move_to(&engine, "Ticket", "b", "triaged");
        update(
            &engine,
            "Ticket",
            "b",
            json!({"priority": "urgent", "title": "b, the third"}),
        );
        run_due(&engine);
        assert_eq!(status(&engine, "Ticket", "b"), "triaged");
        assert_eq!(moves(&engine, "Ticket", "b"), moved(&[("escalated", 1)]));
    }
}

#[test]
fn from_and_to_only_a_move_from_the_one_value_to_the_other_fires() {
    for target in targets("changes_from_to") {
        let engine = open(&target);
        publish(
            &engine,
            &tickets(
                json!([{"when": {"changes": {"field": "priority", "from": "urgent", "to": "low"}}, "then": {"transition": "triaged"}}]),
                &[],
            ),
        );
        create(
            &engine,
            "Ticket",
            "d",
            json!({"title": "d", "priority": "low"}),
            None,
        );
        move_to(&engine, "Ticket", "d", "escalated");
        for priority in ["urgent", "normal", "urgent"] {
            update(&engine, "Ticket", "d", json!({ "priority": priority }));
        }
        run_due(&engine);
        assert_eq!(status(&engine, "Ticket", "d"), "escalated");
        update(&engine, "Ticket", "d", json!({"priority": "low"}));
        run_due(&engine);
        assert_eq!(status(&engine, "Ticket", "d"), "triaged");
        // A patch that removes the field moves it to no value, which no to
        // names.
        update(&engine, "Ticket", "d", json!({"priority": null}));
        run_due(&engine);
        assert_eq!(moves(&engine, "Ticket", "d"), moved(&[("triaged", 1)]));
    }
}

#[test]
fn a_behaviors_field_workflow_status_as_enters_hears_it_and_a_proposal_count_read_as_a_diff() {
    for target in targets("changes_behavior_field") {
        let engine = open(&target);
        publish(&engine, &projects());
        publish(
            &engine,
            &tickets(
                json!([
                    {"when": {"changes": {"field": "Workflow.status", "to": "escalated"}}, "then": {"link": "project", "transition": "active"}},
                    {"when": {"changes": {"field": "Revisions.pendingProposals", "from": 0}}, "then": {"transition": "triaged"}}
                ]),
                &[review(), links_to_project()],
            ),
        );
        create(&engine, "Project", "p1", json!({"title": "Launch"}), None);
        create(
            &engine,
            "Ticket",
            "t1",
            json!({"title": "t1"}),
            Some(json!({"Links": {"project": "p1"}})),
        );
        // The first proposal moves the count from 0 and triages the ticket;
        // a proposal's patch holds no status.
        invoke(
            &engine,
            "Ticket",
            "t1",
            "propose",
            json!({"patch": {"title": "t1, seen"}}),
        );
        run_due(&engine);
        assert_eq!(
            [
                status(&engine, "Ticket", "t1"),
                status(&engine, "Project", "p1")
            ],
            [json!("triaged"), json!("planned")]
        );
        move_to(&engine, "Ticket", "t1", "escalated");
        run_due(&engine);
        assert_eq!(status(&engine, "Project", "p1"), "active");
        // A second proposal moves the count from 1: the ticket stays
        // escalated.
        invoke(
            &engine,
            "Ticket",
            "t1",
            "propose",
            json!({"patch": {"title": "t1, still there"}}),
        );
        run_due(&engine);
        assert_eq!(status(&engine, "Ticket", "t1"), "escalated");
        assert_eq!(moves(&engine, "Ticket", "t1"), moved(&[("triaged", 1)]));
    }
}

#[test]
fn a_delete_moves_nothing() {
    for target in targets("changes_delete") {
        let engine = open(&target);
        publish(&engine, &projects());
        publish(
            &engine,
            &tickets(
                json!([{"when": {"changes": {"field": "priority"}}, "then": {"link": "project", "transition": "active"}}]),
                &[links_to_project()],
            ),
        );
        for id in ["p1", "p2"] {
            create(&engine, "Project", id, json!({ "title": id }), None);
        }
        // t2's create sets its priority before it links anywhere, so it
        // moves no project.
        create(
            &engine,
            "Ticket",
            "t1",
            json!({"title": "t1"}),
            Some(json!({"Links": {"project": "p1"}})),
        );
        create(
            &engine,
            "Ticket",
            "t2",
            json!({"title": "t2", "priority": "low"}),
            None,
        );
        run_due(&engine);
        // A link's patch holds no priority.
        invoke(
            &engine,
            "Ticket",
            "t2",
            "link",
            json!({"name": "project", "id": "p2"}),
        );
        run_due(&engine);
        engine
            .instances()
            .delete(&alice(), "Ticket", "t2", WriteOptions::default())
            .expect("delete");
        update(&engine, "Ticket", "t1", json!({"priority": "high"}));
        run_due(&engine);
        assert_eq!(
            [
                status(&engine, "Project", "p1"),
                status(&engine, "Project", "p2")
            ],
            [json!("active"), json!("planned")]
        );
    }
}

#[test]
fn a_member_path_below_a_behavior_field_whose_value_is_an_object_the_longest_behavior_name() {
    for target in targets("changes_member_path") {
        let engine = open_extended(&target, BehaviorSet::new().with(Score).with(WorkflowTally));
        publish(
            &engine,
            &tickets(
                json!([
                    {"when": {"changes": {"field": "test.Score.score.level", "to": "gold"}}, "then": {"transition": "escalated"}},
                    {"when": {"changes": {"field": "Workflow.Tally.count", "to": 2}}, "then": {"transition": "triaged"}}
                ]),
                &[
                    json!({"name": "test.Score"}),
                    json!({"name": "Workflow.Tally"}),
                ],
            ),
        );
        create(&engine, "Ticket", "t1", json!({"title": "t1"}), None);
        // 4 points leave the level silver: the patch moves points alone.
        invoke(&engine, "Ticket", "t1", "award", json!({"points": 4}));
        assert_eq!(
            events(&engine, "Ticket", "t1")
                .last()
                .expect("an event")
                .change["patch"],
            json!({"behaviors": {"test.Score": {"score": {"points": 4}}}})
        );
        run_due(&engine);
        assert_eq!(status(&engine, "Ticket", "t1"), "new");
        invoke(&engine, "Ticket", "t1", "award", json!({"points": 6}));
        run_due(&engine);
        assert_eq!(status(&engine, "Ticket", "t1"), "escalated");
        for _ in 0..2 {
            invoke(&engine, "Ticket", "t1", "bump", json!({}));
        }
        run_due(&engine);
        assert_eq!(
            moves(&engine, "Ticket", "t1"),
            moved(&[("escalated", 1), ("triaged", 1)])
        );
    }
}

#[test]
fn a_value_the_event_keeps_in_the_value_store_is_compared_once_loaded_never_as_its_ref() {
    for target in targets("changes_value_store") {
        let engine = open_with(&target, |options| {
            options.values = ValueOptions {
                threshold_bytes: Some(1024),
                ..ValueOptions::default()
            };
        });
        let long = "x".repeat(2048);
        publish(
            &engine,
            &tickets(
                json!([
                    {"when": {"changes": {"field": "body", "to": long}}, "then": {"transition": "escalated"}},
                    {"when": {"changes": {"field": "body", "from": long}}, "then": {"transition": "closed"}}
                ]),
                &[],
            ),
        );
        create(
            &engine,
            "Ticket",
            "t1",
            json!({"title": "t1", "body": long}),
            None,
        );
        assert_eq!(
            events(&engine, "Ticket", "t1")[0].value_refs,
            Some(vec!["/data/body".to_owned()])
        );
        run_due(&engine);
        assert_eq!(status(&engine, "Ticket", "t1"), "escalated");
        // The same long body again, beside a new title: the patch's ref is
        // the value the ticket holds.
        move_to(&engine, "Ticket", "t1", "triaged");
        update(
            &engine,
            "Ticket",
            "t1",
            json!({"title": "Renamed", "body": long}),
        );
        assert_eq!(
            events(&engine, "Ticket", "t1")
                .last()
                .expect("an event")
                .value_refs,
            Some(vec!["/data/body".to_owned()])
        );
        run_due(&engine);
        assert_eq!(status(&engine, "Ticket", "t1"), "triaged");
        update(&engine, "Ticket", "t1", json!({"body": "Short now."}));
        run_due(&engine);
        assert_eq!(status(&engine, "Ticket", "t1"), "closed");
    }
}

#[test]
fn a_rule_with_to_on_the_instance_itself_acts_only_while_the_field_still_holds_it() {
    for target in targets("changes_still_holds") {
        let engine = open(&target);
        publish(&engine, &projects());
        publish(
            &engine,
            &tickets(
                json!([
                    {"when": {"changes": {"field": "priority", "to": "urgent"}}, "then": {"transition": "escalated"}},
                    {"when": {"changes": {"field": "priority", "to": "urgent"}}, "then": {"link": "project", "transition": "active"}}
                ]),
                &[links_to_project()],
            ),
        );
        create(&engine, "Project", "p1", json!({"title": "Launch"}), None);
        create(
            &engine,
            "Ticket",
            "t1",
            json!({"title": "t1"}),
            Some(json!({"Links": {"project": "p1"}})),
        );
        update(&engine, "Ticket", "t1", json!({"priority": "urgent"}));
        update(&engine, "Ticket", "t1", json!({"priority": "low"}));
        run_due(&engine);
        assert_eq!(
            [
                status(&engine, "Ticket", "t1"),
                status(&engine, "Project", "p1")
            ],
            [json!("new"), json!("active")]
        );
    }
}

#[test]
fn rules_on_workflow_status_chain_on_the_instance_itself_and_stop_at_the_depth_limit() {
    let rules = json!([
        {"when": {"changes": {"field": "Workflow.status", "to": "triaged"}}, "then": {"transition": "escalated"}},
        {"when": {"changes": {"field": "Workflow.status", "from": "triaged"}}, "then": {"transition": "closed"}}
    ]);
    for (max_depth, last, chain) in [
        (8, "closed", moved(&[("escalated", 1), ("closed", 2)])),
        (1, "escalated", moved(&[("escalated", 1)])),
    ] {
        for target in targets("changes_chain") {
            let engine = open_with(&target, |options| {
                options.runner = Some(RunnerOptions {
                    max_depth: Some(max_depth),
                    ..RunnerOptions::new(runner())
                });
            });
            publish(&engine, &tickets(rules.clone(), &[]));
            create(&engine, "Ticket", "t1", json!({"title": "t1"}), None);
            move_to(&engine, "Ticket", "t1", "triaged");
            run_due(&engine);
            assert_eq!(
                (
                    status(&engine, "Ticket", "t1"),
                    moves(&engine, "Ticket", "t1")
                ),
                (json!(last), chain.clone()),
                "maxDepth {max_depth}"
            );
        }
    }
}

#[test]
fn guidance_names_each_changes_rule_and_the_writes_that_can_set_one_off_say_so() {
    for target in targets("changes_guidance") {
        let engine = open(&target);
        publish(
            &engine,
            &tickets(
                json!([
                    {"when": {"changes": {"field": "priority", "to": "urgent"}}, "then": {"transition": "escalated"}},
                    {"when": {"changes": {"field": "Revisions.pendingProposals", "from": 0}}, "then": {"transition": "triaged"}}
                ]),
                &[review()],
            ),
        );
        let described = engine
            .tools()
            .describe(&alice(), "Ticket", ToolTarget::default())
            .expect("describe");
        let summary = described["behaviors"]
            .as_array()
            .expect("behaviors")
            .iter()
            .find(|behavior| behavior["name"] == "Reactions")
            .map(|behavior| behavior["summary"].clone());
        assert_eq!(
            summary,
            Some(json!("After a change commits, the runner moves statuses by rules, through transition and its guards: when priority changes to \"urgent\", it moves to escalated; when Revisions.pendingProposals changes from 0, it moves to triaged. A move a guard vetoes, or that no transition allows, is left undone."))
        );
        let success = |name: &str| -> String {
            described["operations"]
                .as_array()
                .expect("operations")
                .iter()
                .find(|operation| operation["name"] == name)
                .and_then(|operation| operation["guidance"]["success"].as_str())
                .unwrap_or_default()
                .to_owned()
        };
        let said =
            "Reactions' rules on Ticket may move it, or the instances it links to, again after the commit.";
        for name in ["transition", "create", "update", "propose", "approve"] {
            assert!(success(name).contains(said), "{name}: {}", success(name));
        }
        assert!(!success("listProposals").contains("Reactions"));
    }
}

#[test]
fn parse_config_refuses_an_unknown_field_a_volatile_one_and_entered_at_and_cycles_on_the_status() {
    for target in targets("changes_config") {
        let engine = open_extended(&target, BehaviorSet::new().with(WorkflowTally));
        let refusal_of =
            |rules: Value, extra: &[Value]| refusal(define(&engine, &tickets(rules, extra)));
        let refused = |rules: Value, extra: &[Value]| -> String {
            issues_of(&refusal_of(rules, extra))
                .into_iter()
                .map(|(_, message)| message)
                .collect::<Vec<_>>()
                .join("; ")
        };
        let changes = |field: &str, more: Value| {
            let mut changes = json!({ "field": field });
            if let Value::Object(more) = more {
                for (key, value) in more {
                    changes[key] = value;
                }
            }
            json!({"when": {"changes": changes}, "then": {"transition": "escalated"}})
        };
        let prefix = "type Ticket: behavior Reactions config: rule 1: ";
        let cases = [
            (
                changes("severity", json!({})),
                "when.changes names severity, which is not a field of Ticket (title, priority, body)",
            ),
            (
                changes("Lease.holder", json!({})),
                "when.changes names Lease.holder, which is not a field of a behavior the type lists (Workflow, Links, Reactions)",
            ),
            (
                changes("Workflow.state", json!({})),
                "when.changes names Workflow.state, but Workflow declares no field state (status, enteredAt)",
            ),
            (
                changes("Reactions.rules", json!({})),
                "when.changes names Reactions.rules, but Reactions declares no field rules (none)",
            ),
            (
                changes("Links.targets.project", json!({})),
                "when.changes names Links.targets.project, a volatile field: it moves at a read, with no event of the instance to hear; a rule hears what moves it with holds, allTerminal, anyTerminal or revised",
            ),
            (
                changes("Workflow.enteredAt", json!({})),
                "when.changes names Workflow.enteredAt, which every move of the status moves; a rule on Workflow.status hears those",
            ),
            (
                changes("Workflow.status", json!({"to": "lost"})),
                "when.changes.to \"lost\" is not a state of the type's Workflow (new, triaged, escalated, closed)",
            ),
            (
                changes("Workflow.status", json!({"from": 3})),
                "when.changes.from 3 is not a state of the type's Workflow (new, triaged, escalated, closed)",
            ),
            (
                changes("Workflow.status", json!({"to": "closed"})),
                "no transition of the type's Workflow leads from closed to escalated, so the rule could never move the instance",
            ),
        ];
        for (rule, message) in cases {
            assert_eq!(
                refused(json!([rule]), &[links_to_project()]),
                format!("{prefix}{message}")
            );
        }
        // Dependencies' blocked moves with the blockers: volatile too.
        assert!(refused(
            json!([changes("Dependencies.blocked", json!({}))]),
            &[json!({"name": "Dependencies"})]
        )
        .contains("names Dependencies.blocked, a volatile field"));
        // A changes rule on the status with to moves on from that state;
        // without it, from each state a transition to its state leaves.
        let back = json!({"when": {"enters": "escalated"}, "then": {"transition": "triaged"}});
        assert_eq!(
            refused(
                json!([changes("Workflow.status", json!({"to": "triaged"})), back]),
                &[links_to_project()]
            ),
            "type Ticket: behavior Reactions config: its rules on the instance itself move it round the states triaged, escalated, triaged for ever"
        );
        assert_eq!(
            refused(
                json!([changes("Workflow.status", json!({})), back]),
                &[links_to_project()]
            ),
            "type Ticket: behavior Reactions config: its rules on the instance itself move it round the states escalated, triaged, escalated for ever"
        );
        // The configSchema holds changes to a field and its two values.
        for (rule, wanted) in [
            (
                json!({"when": {"changes": {"to": "urgent"}}, "then": {"transition": "escalated"}}),
                "must have required property 'field'",
            ),
            (
                json!({"when": {"changes": {"field": "priority", "into": "urgent"}}, "then": {"transition": "escalated"}}),
                "must NOT have additional properties",
            ),
            (changes("Workflow..status", json!({})), "must match pattern"),
        ] {
            let message = refused(json!([rule]), &[links_to_project()]);
            assert!(message.contains(wanted), "{message}");
        }
        // These pass: a field of the longer behavior name, a rule through a
        // link, and a rule from a state.
        define(
            &engine,
            &tickets(
                json!([
                    changes("Workflow.Tally.count", json!({"to": 1})),
                    {"when": {"changes": {"field": "Workflow.status", "to": "closed"}}, "then": {"link": "project", "transition": "done"}},
                    {"when": {"changes": {"field": "Workflow.status", "from": "new"}}, "then": {"transition": "closed"}}
                ]),
                &[links_to_project(), json!({"name": "Workflow.Tally"})],
            ),
        )
        .expect("define");
    }
}
