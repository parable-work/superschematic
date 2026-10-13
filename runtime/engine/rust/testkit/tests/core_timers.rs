//! Timers, named deadlines on an instance (D56): the cases of
//! `test/core-timers.test.ts` (a deadline that starts at the create, at
//! each entry of the status into a state, or at a date-time field, before
//! it with a negative afterMs; the fire schedule, which fires each due
//! deadline once through fireTimer, a keyset past the ones whose fire is
//! refused, per schema and namespace; then's move, from the state the
//! deadline starts in by default, recorded as moved, skipped, vetoed or
//! forbidden; until, which cancels a pending deadline; re-entry, a move
//! away and back within one call included; a snooze and a reset, which a
//! later write keeps, and their permission; the Timers.deadlines field and
//! its events; its config, its rule for a new version and deletes), and
//! `test/core-behaviors.test.ts`'s requests document, on SQLite and, with
//! the variable set, on Postgres.
//!
//! Each engine opens with the runner, as the TypeScript file's do, which
//! finds the fire schedule once the schema is published; a sweep moves the
//! clock at least the schedule's interval and runs what is due
//! (`run_due`), and `status()` shows the schedule, its state and its
//! failures. The case a Reactions rule hears a fire in waits for Reactions
//! (wave 3). The guidance is held to the TypeScript text in
//! `behaviors/tests/guidance.rs`, and the describe document that shows it
//! to the recording (`core-timers.json`).

mod support;

use serde_json::{json, Value};

use superschematic_engine::{
    BehaviorSet, Clock, CreateOptions, DefineOptions, Engine, EngineErrorCode, EngineEvent, Error,
    GetOptions, InstanceRecord, NamespaceOptions, Policy, Principal, ReadEvents, RunnerOptions,
    RunnerPass, ScheduleState, ScheduleStatus, SchemaTarget, WriteOptions,
};
use superschematic_engine_testkit::core_fixtures::{Bounce, FireHold};
use support::{alice, refusal, repository, schema_document, targets, veto, Target};

const MINUTE: i64 = 60_000;
const HOUR: i64 = 60 * MINUTE;
const DAY: i64 = 24 * HOUR;

/// When every test's clock starts: `Date.UTC(2026, 9, 9, 9)`.
const T0: i64 = 1_791_536_400_000;

fn runner() -> Principal {
    Principal::new("runner", Vec::<String>::new())
}

fn lead() -> Principal {
    Principal::new("lee", ["tickets.timers", "tickets.escalate"])
}

/// The core's behaviors, as the TypeScript engine registers them.
fn core() -> BehaviorSet {
    superschematic_engine_behaviors::core()
}

// A ticket is open, waits on its customer, is escalated, resolved, or
// closed; most moves go back and forth.
fn flow() -> Value {
    json!({
        "states": ["open", "waiting", "escalated", "resolved", "closed"],
        "transitions": [
            {"from": "open", "to": "waiting"},
            {"from": "waiting", "to": "open"},
            {"from": "open", "to": "escalated"},
            {"from": "waiting", "to": "escalated"},
            {"from": "escalated", "to": "open"},
            {"from": "open", "to": "resolved"},
            {"from": "waiting", "to": "resolved"},
            {"from": "escalated", "to": "resolved"},
            {"from": "resolved", "to": "open"},
            {"from": "open", "to": "closed"},
            {"from": "resolved", "to": "closed"}
        ]
    })
}

/// A schema named `name` whose Workflow is `workflow` (none for null),
/// then Timers with its config (none for null), then `extra`.
fn ticket_schema_named(timers: Value, extra: &[Value], workflow: Value, name: &str) -> Value {
    let mut document = schema_document(
        name,
        json!([
            {"name": "title", "typeRef": {"name": "string"}, "required": true},
            {"name": "body", "typeRef": {"name": "string"}},
            {"name": "visitAt", "typeRef": {"name": "Temporal.DateTime"}},
            {"name": "visitDay", "typeRef": {"name": "Temporal.Date"}}
        ]),
        json!({}),
    );
    let mut behaviors = Vec::new();
    if !workflow.is_null() {
        behaviors.push(json!({"name": "Workflow", "config": workflow}));
    }
    if !timers.is_null() {
        behaviors.push(json!({"name": "Timers", "config": timers}));
    }
    behaviors.extend(extra.iter().cloned());
    document["types"][name]["behaviors"] = Value::Array(behaviors);
    document
}

fn ticket_schema(timers: Value, extra: &[Value]) -> Value {
    ticket_schema_named(timers, extra, flow(), "Ticket")
}

fn publish_in(engine: &Engine, document: &Value, namespace: Option<&str>) {
    engine
        .schemas()
        .define(
            &alice(),
            document,
            DefineOptions {
                namespace,
                ..Default::default()
            },
        )
        .expect("define");
    engine
        .schemas()
        .publish(
            &alice(),
            document["name"].as_str().expect("a name"),
            SchemaTarget { namespace },
        )
        .expect("publish");
}

fn publish(engine: &Engine, document: &Value) {
    publish_in(engine, document, None);
}

fn at(ms: i64) -> String {
    superschematic_engine_testkit::iso_of(ms)
}

/// An engine on a clock at T0, the Ticket schema published.
struct Opened {
    engine: Engine,
}

impl Opened {
    fn create(&self, id: &str, data: Value) -> InstanceRecord {
        let mut data = data;
        data["title"] = json!(id);
        self.engine
            .instances()
            .create(
                &alice(),
                "Ticket",
                &data,
                CreateOptions {
                    id: Some(id),
                    ..Default::default()
                },
            )
            .expect("create")
    }

    fn invoke(
        &self,
        id: &str,
        operation: &str,
        params: Value,
        who: &Principal,
    ) -> Result<Value, Error> {
        self.engine.instances().invoke(
            who,
            "Ticket",
            id,
            operation,
            &params,
            WriteOptions::default(),
        )
    }

    fn move_to(&self, id: &str, to: &str) -> Value {
        self.invoke(id, "transition", json!({ "to": to }), &alice())
            .expect("transition")
    }

    fn read(&self, id: &str) -> InstanceRecord {
        self.engine
            .instances()
            .get(&alice(), "Ticket", id, GetOptions::default())
            .expect("get")
            .expect("the instance")
    }

    fn deadlines(&self, id: &str) -> Value {
        self.read(id).behaviors["Timers"]["deadlines"].clone()
    }

    fn status(&self, id: &str) -> Value {
        self.read(id).behaviors["Workflow"]["status"].clone()
    }

    fn seq(&self, id: &str) -> i64 {
        self.read(id).seq
    }

    fn events(&self, id: &str) -> Vec<EngineEvent> {
        self.engine
            .events()
            .read(
                &alice(),
                &ReadEvents {
                    schema: Some("Ticket"),
                    instance_id: Some(id),
                    limit: Some(500),
                    ..ReadEvents::default()
                },
            )
            .expect("events")
            .events
    }

    fn now(&self) -> i64 {
        self.engine.clock().now()
    }

    fn set(&self, at: i64) {
        self.engine.clock().set(at).expect("set the clock");
    }

    fn advance(&self, ms: i64) -> i64 {
        self.engine.clock().advance(ms).expect("advance the clock")
    }

    // sweep moves the clock on by ms, at least the sweep's interval, and
    // runs what is due.
    fn sweep(&self, ms: i64) -> Result<RunnerPass, Error> {
        self.advance(ms);
        self.engine.runner().run_due()
    }

    fn timers_schedule(&self) -> ScheduleStatus {
        timers_schedules(&self.engine)
            .into_iter()
            .next()
            .expect("Timers' schedule")
    }
}

// timers_schedules is what the runner's status lists of Timers' schedules.
fn timers_schedules(engine: &Engine) -> Vec<ScheduleStatus> {
    engine
        .runner()
        .status()
        .expect("the runner's status")
        .schedules
        .into_iter()
        .filter(|schedule| schedule.behavior == "Timers")
        .collect()
}

// open_with opens an engine on a clock at T0 with the runner, test.Bounce
// and test.Hold, its options changed by `configure`.
fn open_with(
    target: &Target,
    configure: impl FnOnce(&mut superschematic_engine::EngineOptions),
) -> Engine {
    target.open_with(core().with(Bounce).with(FireHold), |options| {
        options.clock = Clock::Manual(T0);
        options.runner = Some(RunnerOptions::new(runner()));
        configure(options);
    })
}

// opened opens an engine on a clock at T0, with the open meta-schema, the
// runner, test.Bounce and test.Hold, publishes the Ticket schema with its
// Timers config, extra behaviors after it, and a Workflow config, and lets
// the runner find the fire schedule, which runs an interval later.
fn opened_with(
    target: &Target,
    timers: Value,
    extra: &[Value],
    workflow: Value,
    configure: impl FnOnce(&mut superschematic_engine::EngineOptions),
) -> Opened {
    let engine = open_with(target, configure);
    publish(
        &engine,
        &ticket_schema_named(timers, extra, workflow, "Ticket"),
    );
    engine.runner().run_due().expect("the runner's first pass");
    Opened { engine }
}

fn opened(target: &Target, timers: Value, extra: &[Value]) -> Opened {
    opened_with(target, timers, extra, flow(), |_| {})
}

fn change(event: &EngineEvent) -> &Value {
    &event.change
}

#[test]
fn an_enters_deadline_arms_as_the_status_enters_its_state_and_the_sweep_fires_it_once_due() {
    for target in targets("timers_enters") {
        let t = opened(
            &target,
            json!({"deadlines": {"respond": {"start": {"enters": "open"}, "afterMs": 4 * HOUR, "then": {"transition": "escalated"}}}}),
            &[],
        );
        let created = t.create("t1", json!({}));
        assert_eq!(
            created.behaviors["Timers"],
            json!({"deadlines": {"respond": {"dueAt": T0 + 4 * HOUR, "fired": false}}})
        );
        assert_eq!(
            change(&t.events("t1")[0])["behaviors"]["Timers"],
            created.behaviors["Timers"]
        );

        // Not due: the sweep and a caller's fireTimer change nothing.
        assert_eq!(t.sweep(4 * HOUR - MINUTE).expect("sweep").scheduled, 1);
        assert_eq!(t.status("t1"), "open");
        assert_eq!(
            t.invoke("t1", "fireTimer", json!({"deadline": "respond"}), &alice())
                .expect("fireTimer"),
            json!({"fired": false})
        );
        assert_eq!((t.seq("t1"), t.events("t1").len()), (1, 1));

        t.sweep(MINUTE).expect("sweep");
        let fired_at = t.now();
        assert_eq!(t.status("t1"), "escalated");
        assert_eq!(
            t.deadlines("t1"),
            json!({"respond": {"dueAt": T0 + 4 * HOUR, "fired": true, "firedAt": fired_at, "outcome": "moved"}})
        );
        let events = t.events("t1");
        let fire = events.last().expect("the fire");
        assert_eq!(
            (
                fire.actor.as_str(),
                serde_json::to_value(&fire.cause).expect("JSON"),
                change(fire)["behavior"].clone(),
                change(fire)["operation"].clone(),
                change(fire)["params"].clone()
            ),
            (
                "runner",
                json!({"behavior": "Timers", "schedule": "fire", "depth": 1}),
                json!("Timers"),
                json!("fireTimer"),
                json!({"deadline": "respond"})
            )
        );
        assert_eq!(
            change(fire)["patch"],
            json!({"behaviors": {"Workflow": {"status": "escalated", "enteredAt": fired_at}, "Timers": {"deadlines": {"respond": {"fired": true, "firedAt": fired_at, "outcome": "moved"}}}}})
        );

        // It fired once: a caller's fireTimer and the next sweeps change
        // nothing.
        assert_eq!(
            t.invoke("t1", "fireTimer", json!({"deadline": "respond"}), &alice())
                .expect("fireTimer"),
            json!({"fired": false})
        );
        t.sweep(HOUR).expect("sweep");
        assert_eq!(t.events("t1").len(), 2);
        assert_eq!(
            timers_schedules(&t.engine)
                .iter()
                .map(|schedule| (
                    schedule.schedule.as_str(),
                    schedule.schema.as_str(),
                    schedule.state,
                    schedule.every_ms
                ))
                .collect::<Vec<_>>(),
            [("fire", "Ticket", ScheduleState::Active, Some(5000))]
        );
        let (behavior, _, reason, code, _) =
            veto(t.invoke("t1", "fireTimer", json!({"deadline": "answer"}), &alice()));
        assert_eq!(
            (behavior.as_str(), code.as_deref(), reason.as_str()),
            (
                "Timers",
                Some("no_deadline"),
                "answer is not a deadline of Ticket's Timers (its deadlines: respond)"
            )
        );
    }
}

#[test]
fn a_caller_who_may_write_fires_a_due_deadline_itself_before_the_sweep() {
    for target in targets("timers_caller") {
        let t = opened(
            &target,
            json!({"deadlines": {"respond": {"start": {"enters": "open"}, "afterMs": HOUR, "then": {"transition": "escalated"}}}, "sweepMs": 60_000}),
            &[],
        );
        t.create("t1", json!({}));
        t.advance(HOUR);
        assert_eq!(
            t.invoke("t1", "fireTimer", json!({"deadline": "respond"}), &alice())
                .expect("fireTimer"),
            json!({"fired": true, "outcome": "moved"})
        );
        assert_eq!(
            (
                t.status("t1"),
                t.events("t1").last().expect("an event").actor.clone()
            ),
            (json!("escalated"), "alice".to_owned())
        );
        t.sweep(MINUTE).expect("sweep");
        assert_eq!(
            (
                t.events("t1").len(),
                t.deadlines("t1")["respond"]["firedAt"].clone()
            ),
            (2, json!(T0 + HOUR))
        );
    }
}

#[test]
fn a_create_deadline_moves_from_the_initial_state_alone_and_one_without_then_only_fires() {
    for target in targets("timers_create") {
        let t = opened(
            &target,
            json!({"deadlines": {
                "close": {"start": {"create": true}, "afterMs": 30 * MINUTE, "then": {"transition": "closed"}},
                "ping": {"start": {"create": true}, "afterMs": 10 * MINUTE}
            }}),
            &[],
        );
        t.create("t1", json!({}));
        t.create("t2", json!({}));
        t.move_to("t2", "waiting");
        assert_eq!(
            t.deadlines("t2"),
            json!({"close": {"dueAt": T0 + 30 * MINUTE, "fired": false}, "ping": {"dueAt": T0 + 10 * MINUTE, "fired": false}})
        );

        t.sweep(10 * MINUTE).expect("sweep");
        for id in ["t1", "t2"] {
            assert_eq!(
                t.deadlines(id)["ping"],
                json!({"dueAt": T0 + 10 * MINUTE, "fired": true, "firedAt": T0 + 10 * MINUTE})
            );
        }
        t.sweep(20 * MINUTE).expect("sweep");
        assert_eq!(
            (
                t.status("t1"),
                t.deadlines("t1")["close"]["outcome"].clone()
            ),
            (json!("closed"), json!("moved"))
        );
        // t2 is waiting, which close does not move from.
        assert_eq!(
            (t.status("t2"), t.deadlines("t2")["close"].clone()),
            (
                json!("waiting"),
                json!({"dueAt": T0 + 30 * MINUTE, "fired": true, "firedAt": T0 + 30 * MINUTE, "outcome": "skipped"})
            )
        );
    }
}

#[test]
fn a_field_deadline_is_due_after_or_before_the_time_it_holds_and_moves_with_it() {
    for target in targets("timers_field") {
        let t = opened(
            &target,
            json!({"deadlines": {
                "remind": {"start": {"field": "visitAt"}, "afterMs": -DAY},
                "visit": {"start": {"field": "visitAt"}, "then": {"transition": "waiting", "from": ["open"]}}
            }}),
            &[],
        );
        let visit = T0 + 3 * DAY;
        t.create("t1", json!({"visitAt": at(visit)}));
        t.create("t2", json!({}));
        assert_eq!(
            t.deadlines("t1"),
            json!({"remind": {"dueAt": visit - DAY, "fired": false}, "visit": {"dueAt": visit, "fired": false}})
        );
        assert_eq!(t.deadlines("t2"), json!({}));
        let update = |id: &str, patch: Value| {
            t.engine
                .instances()
                .update(&alice(), "Ticket", id, &patch, WriteOptions::default())
                .expect("update")
        };
        // A value in another offset is the same time.
        update("t2", json!({"visitAt": "2026-10-12T11:00:00+02:00"}));
        assert_eq!(
            t.deadlines("t2")["visit"],
            json!({"dueAt": visit, "fired": false})
        );

        t.sweep(2 * DAY).expect("sweep");
        assert_eq!(
            t.deadlines("t1")["remind"],
            json!({"dueAt": visit - DAY, "fired": true, "firedAt": T0 + 2 * DAY})
        );
        // A write that leaves the field as it is keeps the fire.
        update("t1", json!({"body": "Bring the part."}));
        assert_eq!(t.deadlines("t1")["remind"]["fired"], true);
        // A new time starts both again, fired cleared.
        update("t1", json!({"visitAt": at(visit + DAY)}));
        assert_eq!(
            t.deadlines("t1"),
            json!({"remind": {"dueAt": visit, "fired": false}, "visit": {"dueAt": visit + DAY, "fired": false}})
        );
        assert_eq!(
            change(t.events("t1").last().expect("an event"))["behaviors"],
            json!({"Timers": {"deadlines": {"remind": {"dueAt": visit, "fired": false, "firedAt": null}, "visit": {"dueAt": visit + DAY}}}})
        );
        // Clearing it cancels them.
        update("t1", json!({"visitAt": null}));
        assert_eq!(t.deadlines("t1"), json!({}));

        t.sweep(DAY).expect("sweep");
        assert_eq!(
            (
                t.status("t2"),
                t.deadlines("t2")["visit"]["outcome"].clone()
            ),
            (json!("waiting"), json!("moved"))
        );
        assert_eq!(
            t.events("t1")
                .iter()
                .filter(|event| event
                    .cause
                    .as_ref()
                    .and_then(|cause| cause.schedule.as_deref())
                    == Some("fire"))
                .count(),
            1
        );
    }
}

#[test]
fn each_entry_starts_an_enters_deadline_again_and_until_cancels_it_while_it_is_pending() {
    for target in targets("timers_reentry") {
        let t = opened(
            &target,
            json!({"deadlines": {
                "respond": {"start": {"enters": "open"}, "afterMs": 4 * HOUR, "then": {"transition": "escalated"}, "until": {"leaves": "open"}},
                "idle": {"start": {"enters": "open"}, "afterMs": 2 * HOUR}
            }}),
            &[json!({"name": "test.Bounce"})],
        );
        t.create("t1", json!({}));
        t.set(T0 + HOUR);
        t.move_to("t1", "waiting");
        // respond is cancelled on leaving open; idle, with no until, waits.
        assert_eq!(
            t.deadlines("t1"),
            json!({"idle": {"dueAt": T0 + 2 * HOUR, "fired": false}})
        );
        t.set(T0 + 2 * HOUR);
        t.move_to("t1", "open");
        assert_eq!(
            t.deadlines("t1"),
            json!({"respond": {"dueAt": T0 + 6 * HOUR, "fired": false}, "idle": {"dueAt": T0 + 4 * HOUR, "fired": false}})
        );

        // Away and back in one operation: one event, a new enteredAt, and
        // both start again.
        t.set(T0 + 3 * HOUR);
        let seen = t.events("t1").len();
        assert_eq!(
            t.invoke(
                "t1",
                "bounce",
                json!({"via": "waiting", "back": "open"}),
                &alice()
            )
            .expect("bounce"),
            json!(T0 + 3 * HOUR)
        );
        assert_eq!(t.events("t1").len(), seen + 1);
        assert_eq!(
            t.deadlines("t1"),
            json!({"respond": {"dueAt": T0 + 7 * HOUR, "fired": false}, "idle": {"dueAt": T0 + 5 * HOUR, "fired": false}})
        );

        t.sweep(2 * HOUR).expect("sweep");
        assert_eq!(
            t.deadlines("t1")["idle"],
            json!({"dueAt": T0 + 5 * HOUR, "fired": true, "firedAt": T0 + 5 * HOUR})
        );
        // A fired deadline is not cancelled; an entry starts it again.
        t.set(T0 + 6 * HOUR);
        t.move_to("t1", "waiting");
        assert_eq!(
            t.deadlines("t1"),
            json!({"idle": {"dueAt": T0 + 5 * HOUR, "fired": true, "firedAt": T0 + 5 * HOUR}})
        );
        t.advance(MINUTE);
        t.move_to("t1", "open");
        assert_eq!(
            t.deadlines("t1"),
            json!({
                "respond": {"dueAt": T0 + 10 * HOUR + MINUTE, "fired": false},
                "idle": {"dueAt": T0 + 8 * HOUR + MINUTE, "fired": false}
            })
        );
    }
}

#[test]
fn until_enters_cancels_a_create_deadline_and_a_return_does_not_start_it_again() {
    for target in targets("timers_until_enters") {
        let t = opened(
            &target,
            json!({"deadlines": {"answer": {"start": {"create": true}, "afterMs": HOUR, "until": {"enters": ["resolved", "closed"]}}}}),
            &[],
        );
        t.create("t1", json!({}));
        t.move_to("t1", "resolved");
        assert_eq!(t.deadlines("t1"), json!({}));
        t.move_to("t1", "open");
        assert_eq!(t.deadlines("t1"), json!({}));
        t.sweep(2 * HOUR).expect("sweep");
        assert_eq!(t.events("t1").len(), 3);
    }
}

#[test]
fn a_deadline_whose_until_holds_when_it_comes_due_is_cancelled_rather_than_fired() {
    for target in targets("timers_until_later") {
        let t = opened(
            &target,
            json!({"deadlines": {"answer": {"start": {"create": true}, "afterMs": HOUR}}}),
            &[],
        );
        t.create("t1", json!({}));
        t.move_to("t1", "resolved");
        publish(
            &t.engine,
            &ticket_schema(
                json!({"deadlines": {"answer": {"start": {"create": true}, "afterMs": HOUR, "until": {"enters": ["resolved"]}}}}),
                &[],
            ),
        );
        assert_eq!(
            t.deadlines("t1"),
            json!({"answer": {"dueAt": T0 + HOUR, "fired": false}})
        );
        t.sweep(2 * HOUR).expect("sweep");
        assert_eq!(t.deadlines("t1"), json!({}));
        let events = t.events("t1");
        let last = change(events.last().expect("an event"));
        assert_eq!(
            (last["operation"].clone(), last["patch"].clone()),
            (
                json!("fireTimer"),
                json!({"behaviors": {"Timers": {"deadlines": {"answer": null}}}})
            )
        );
    }
}

#[test]
fn a_create_arms_its_deadlines_whatever_the_order_the_type_lists_its_behaviors_in() {
    for target in targets("timers_order") {
        let engine = target.open_core_with(|options| {
            options.behaviors = core();
            options.clock = Clock::Manual(T0);
        });
        let mut document = ticket_schema(
            json!({"deadlines": {"respond": {"start": {"enters": "open"}, "afterMs": HOUR}, "ping": {"start": {"create": true}}}}),
            &[],
        );
        document["types"]["Ticket"]["behaviors"]
            .as_array_mut()
            .expect("behaviors")
            .reverse();
        publish(&engine, &document);
        let created = engine
            .instances()
            .create(
                &alice(),
                "Ticket",
                &json!({"title": "First"}),
                CreateOptions {
                    id: Some("t1"),
                    ..Default::default()
                },
            )
            .expect("create");
        assert_eq!(
            created.behaviors["Timers"],
            json!({"deadlines": {"ping": {"dueAt": T0, "fired": false}, "respond": {"dueAt": T0 + HOUR, "fired": false}}})
        );
    }
}

#[test]
fn a_snooze_outlives_a_later_write_and_reset_starts_it_again_both_need_the_permission() {
    for target in targets("timers_snooze") {
        let t = opened(
            &target,
            json!({
                "deadlines": {
                    "respond": {"start": {"enters": "open"}, "afterMs": 4 * HOUR, "then": {"transition": "escalated"}},
                    "chase": {"start": {"enters": "waiting"}, "afterMs": DAY}
                },
                "permission": "tickets.timers"
            }),
            &[],
        );
        t.create("t1", json!({}));
        let forbidden = refusal(t.invoke(
            "t1",
            "snoozeTimer",
            json!({"deadline": "respond", "dueAt": T0 + 8 * HOUR}),
            &alice(),
        ));
        assert_eq!(
            (forbidden.code(), forbidden.message()),
            (
                EngineErrorCode::Forbidden,
                "alice may not snooze deadline respond of Ticket t1: it needs permission tickets.timers"
            )
        );
        assert_eq!(
            refusal(t.invoke("t1", "resetTimer", json!({"deadline": "respond"}), &alice())).code(),
            EngineErrorCode::Forbidden
        );

        assert_eq!(
            t.invoke(
                "t1",
                "snoozeTimer",
                json!({"deadline": "respond", "dueAt": T0 + 8 * HOUR}),
                &lead()
            )
            .expect("snooze"),
            json!({"dueAt": T0 + 8 * HOUR})
        );
        t.set(T0 + HOUR);
        // A write that leaves the status where it was keeps the snooze.
        t.engine
            .instances()
            .update(
                &alice(),
                "Ticket",
                "t1",
                &json!({"body": "Still broken."}),
                WriteOptions::default(),
            )
            .expect("update");
        assert_eq!(
            t.deadlines("t1"),
            json!({"respond": {"dueAt": T0 + 8 * HOUR, "fired": false}})
        );
        t.sweep(4 * HOUR).expect("sweep");
        assert_eq!(t.status("t1"), "open");
        // The same dueAt again changes nothing.
        let before = t.seq("t1");
        t.invoke(
            "t1",
            "snoozeTimer",
            json!({"deadline": "respond", "dueAt": T0 + 8 * HOUR}),
            &lead(),
        )
        .expect("snooze");
        assert_eq!(t.seq("t1"), before);

        // A reset starts it from the entry into open again: due already.
        assert_eq!(
            t.invoke("t1", "resetTimer", json!({"deadline": "respond"}), &lead())
                .expect("reset"),
            json!({"dueAt": T0 + 4 * HOUR})
        );
        t.sweep(MINUTE).expect("sweep");
        assert_eq!(
            (
                t.status("t1"),
                t.deadlines("t1")["respond"]["outcome"].clone()
            ),
            (json!("escalated"), json!("moved"))
        );
        // A fired one snoozed fires again then; out of open, its move is
        // skipped.
        let later = t.now() + HOUR;
        t.invoke(
            "t1",
            "snoozeTimer",
            json!({"deadline": "respond", "dueAt": later}),
            &lead(),
        )
        .expect("snooze");
        assert_eq!(
            t.deadlines("t1")["respond"],
            json!({"dueAt": later, "fired": false})
        );
        t.sweep(HOUR).expect("sweep");
        assert_eq!(
            (
                t.status("t1"),
                t.deadlines("t1")["respond"]["outcome"].clone()
            ),
            (json!("escalated"), json!("skipped"))
        );

        // Out of open, respond has nothing to start from now; chase has
        // never started.
        let (_, _, reason, code, _) =
            veto(t.invoke("t1", "resetTimer", json!({"deadline": "respond"}), &lead()));
        assert_eq!(
            (code.as_deref(), reason.as_str()),
            (
                Some("not_armed"),
                "deadline respond starts when the status enters open, and Ticket t1 is escalated; it starts at the next entry"
            )
        );
        let (_, _, reason, code, _) = veto(t.invoke(
            "t1",
            "snoozeTimer",
            json!({"deadline": "chase", "dueAt": T0}),
            &lead(),
        ));
        assert_eq!(
            (code.as_deref(), reason.as_str()),
            (
                Some("not_armed"),
                "deadline chase of Ticket t1 has not started or was cancelled, so it has no due time to move"
            )
        );
        assert_eq!(
            veto(t.invoke(
                "t1",
                "snoozeTimer",
                json!({"deadline": "answer", "dueAt": T0}),
                &lead()
            ))
            .3
            .as_deref(),
            Some("no_deadline")
        );
        // Back in open, the entry starts respond again and the snooze is
        // gone.
        t.move_to("t1", "open");
        assert_eq!(
            t.deadlines("t1")["respond"],
            json!({"dueAt": t.now() + 4 * HOUR, "fired": false})
        );
    }
    for target in targets("timers_snooze_closed") {
        // With no permission in the config, nobody resets or snoozes.
        let closed = opened(
            &target,
            json!({"deadlines": {"respond": {"start": {"enters": "open"}, "afterMs": HOUR}}}),
            &[],
        );
        closed.create("t1", json!({}));
        let refused = refusal(closed.invoke(
            "t1",
            "snoozeTimer",
            json!({"deadline": "respond", "dueAt": T0}),
            &lead(),
        ));
        assert_eq!(
            (refused.code(), refused.message()),
            (
                EngineErrorCode::Forbidden,
                "lee may not snooze deadline respond of Ticket t1: Ticket's Timers config names no permission, so nobody may"
            )
        );
    }
}

#[test]
fn a_fire_whose_move_is_vetoed_or_forbidden_is_fired_all_the_same_and_records_which() {
    let respond = json!({"deadlines": {"respond": {"start": {"enters": "open"}, "afterMs": HOUR, "then": {"transition": "escalated"}}}});
    for target in targets("timers_vetoed") {
        // StateRules keeps a ticket out of escalated until it has a body.
        let vetoing = opened(
            &target,
            respond.clone(),
            &[
                json!({"name": "StateRules", "config": {"states": {"escalated": {"required": ["body"]}}}}),
            ],
        );
        vetoing.create("t1", json!({}));
        vetoing.sweep(HOUR).expect("sweep");
        assert_eq!(
            (
                vetoing.status("t1"),
                vetoing.deadlines("t1")["respond"]["outcome"].clone()
            ),
            (json!("open"), json!("vetoed"))
        );
        vetoing.sweep(HOUR).expect("sweep");
        assert_eq!(vetoing.events("t1").len(), 2);
    }
    for target in targets("timers_forbidden") {
        let mut guarded = flow();
        for edge in guarded["transitions"].as_array_mut().expect("transitions") {
            if edge["from"] == "open" && edge["to"] == "escalated" {
                edge["permission"] = json!("tickets.escalate");
            }
        }
        let forbidding = opened_with(&target, respond.clone(), &[], guarded, |_| {});
        forbidding.create("t1", json!({}));
        forbidding.create("t2", json!({}));
        forbidding.advance(HOUR);
        // A caller with the transition's permission fires t2 itself.
        assert_eq!(
            forbidding
                .invoke("t2", "fireTimer", json!({"deadline": "respond"}), &lead())
                .expect("fireTimer"),
            json!({"fired": true, "outcome": "moved"})
        );
        forbidding.sweep(MINUTE).expect("sweep");
        assert_eq!(
            (
                forbidding.status("t1"),
                forbidding.deadlines("t1")["respond"]["outcome"].clone()
            ),
            (json!("open"), json!("forbidden"))
        );
        assert_eq!(
            (forbidding.status("t2"), forbidding.events("t2").len()),
            (json!("escalated"), 2)
        );
    }
}

#[test]
fn a_runner_the_policy_refuses_write_on_the_schema_fails_the_sweep_and_fires_nothing() {
    for target in targets("timers_refused") {
        // The runner may read the schema, which the sweep reads, and not
        // write it.
        let t = opened_with(
            &target,
            json!({"deadlines": {"respond": {"start": {"enters": "open"}, "afterMs": HOUR, "then": {"transition": "escalated"}}}}),
            &[],
            flow(),
            |options| {
                options.policy = Some(
                    Policy::from_json(&json!({"rules": [
                        {"subject": "runner", "action": "write", "allow": false},
                        {"allow": true}
                    ]}))
                    .expect("the policy"),
                );
            },
        );
        t.create("t1", json!({}));
        assert_eq!(t.sweep(HOUR).expect("sweep").failed, 1);
        let status = t.timers_schedule();
        assert_eq!(status.state, ScheduleState::Retrying);
        let error = status.error.unwrap_or_default();
        assert!(
            error.contains("forbidden") || error.contains("may not"),
            "{error}"
        );
        assert_eq!(t.deadlines("t1")["respond"]["fired"], false);
    }
}

#[test]
fn the_sweep_pages_by_due_time_past_the_deadlines_whose_fire_is_refused() {
    for target in targets("timers_paging") {
        let t = opened(
            &target,
            json!({"deadlines": {"ping": {"start": {"create": true}, "afterMs": HOUR}}}),
            &[json!({"name": "test.Hold"})],
        );
        // A full page of held deadlines due first, then three that fire.
        for index in 0..100 {
            t.engine
                .instances()
                .create(
                    &alice(),
                    "Ticket",
                    &json!({"title": "stuck"}),
                    CreateOptions {
                        id: Some(&format!("s{index:03}")),
                        ..Default::default()
                    },
                )
                .expect("create");
        }
        t.advance(MINUTE);
        for id in ["f1", "f2", "f3"] {
            t.create(id, json!({}));
        }
        for _ in 0..2 {
            let pass = t.sweep(2 * HOUR).expect("sweep");
            assert_eq!((pass.scheduled, pass.failed), (1, 0));
            for id in ["f1", "f2", "f3"] {
                assert_eq!(t.deadlines(id)["ping"]["fired"], true, "{id}");
                assert_eq!(t.events(id).len(), 2, "{id}");
            }
            assert_eq!(
                (
                    t.deadlines("s000")["ping"]["fired"].clone(),
                    t.deadlines("s099")["ping"]["fired"].clone(),
                    t.events("s050").len()
                ),
                (json!(false), json!(false), 1)
            );
        }
        assert_eq!(t.timers_schedule().state, ScheduleState::Active);
    }
}

#[test]
fn the_sweep_runs_per_schema_and_per_namespace_each_firing_its_own_deadlines() {
    for target in targets("timers_namespaces") {
        let engine = target.open_core_with(|options| {
            options.behaviors = core();
            options.clock = Clock::Manual(T0);
            options.runner = Some(RunnerOptions::new(runner()));
            options.namespaces = NamespaceOptions {
                names: vec!["east".to_owned(), "west".to_owned()],
                shared: None,
            };
        });
        let timers = json!({"deadlines": {"ping": {"start": {"create": true}, "afterMs": HOUR}}});
        for namespace in ["east", "west"] {
            publish_in(
                &engine,
                &ticket_schema(timers.clone(), &[]),
                Some(namespace),
            );
            publish_in(
                &engine,
                &ticket_schema_named(timers.clone(), &[], flow(), "Task"),
                Some(namespace),
            );
        }
        let create = |namespace: &str, schema: &str| {
            engine
                .instances()
                .create(
                    &alice(),
                    schema,
                    &json!({"title": "One"}),
                    CreateOptions {
                        id: Some("x1"),
                        namespace: Some(namespace),
                        ..Default::default()
                    },
                )
                .expect("create")
        };
        let fired = |namespace: &str, schema: &str| {
            engine
                .instances()
                .get(
                    &alice(),
                    schema,
                    "x1",
                    GetOptions {
                        namespace: Some(namespace),
                        ..Default::default()
                    },
                )
                .expect("get")
                .expect("x1")
                .behaviors["Timers"]["deadlines"]["ping"]["fired"]
                .clone()
        };
        engine.runner().run_due().expect("the runner's first pass");
        let run_due = || engine.runner().run_due().expect("run due");
        create("east", "Ticket");
        create("east", "Task");
        engine.clock().set(T0 + 2 * HOUR).expect("set");
        create("west", "Ticket");
        create("west", "Task");
        assert_eq!(run_due().scheduled, 4);
        assert_eq!(
            [
                fired("east", "Ticket"),
                fired("east", "Task"),
                fired("west", "Ticket"),
                fired("west", "Task")
            ],
            [json!(true), json!(true), json!(false), json!(false)]
        );
        engine.clock().set(T0 + 4 * HOUR).expect("set");
        run_due();
        assert_eq!(
            [fired("west", "Ticket"), fired("west", "Task")],
            [json!(true), json!(true)]
        );
        let mut schedules = timers_schedules(&engine)
            .iter()
            .map(|schedule| format!("{} {}", schedule.namespace, schedule.schema))
            .collect::<Vec<_>>();
        schedules.sort();
        assert_eq!(
            schedules,
            ["east Task", "east Ticket", "west Task", "west Ticket"]
        );
        let west = engine
            .events()
            .read(
                &alice(),
                &ReadEvents {
                    namespace: Some("west"),
                    schema: Some("Ticket"),
                    instance_id: Some("x1"),
                    ..ReadEvents::default()
                },
            )
            .expect("events")
            .events;
        assert_eq!(
            west.iter()
                .map(|event| if event.kind == "operation" {
                    event.change["operation"].clone()
                } else {
                    json!(event.kind)
                })
                .collect::<Vec<_>>(),
            [json!("create"), json!("fireTimer")]
        );
    }
}

#[test]
fn a_new_version_moves_removes_and_arms_deadlines_and_timers_joins_and_leaves_a_schema_with_instances(
) {
    for target in targets("timers_versions") {
        let t = Opened {
            engine: target.open_core_with(|options| {
                options.behaviors = core();
                options.clock = Clock::Manual(T0);
            }),
        };
        let version = |timers: Value| publish(&t.engine, &ticket_schema(timers, &[]));
        version(Value::Null);
        t.create("t1", json!({}));
        t.create("t2", json!({"visitAt": at(T0 + DAY)}));

        // Joining: the field deadline arms at the publish; the enters one at
        // each instance's next write.
        t.set(T0 + HOUR);
        version(json!({
            "deadlines": {
                "respond": {"start": {"enters": "open"}, "afterMs": 4 * HOUR},
                "remind": {"start": {"field": "visitAt"}, "afterMs": -HOUR}
            },
            "permission": "tickets.timers"
        }));
        assert_eq!(
            (t.deadlines("t1"), t.deadlines("t2")),
            (
                json!({}),
                json!({"remind": {"dueAt": T0 + 23 * HOUR, "fired": false}})
            )
        );
        assert_eq!(t.events("t2").len(), 1);
        t.engine
            .instances()
            .update(
                &alice(),
                "Ticket",
                "t1",
                &json!({"body": "First write."}),
                WriteOptions::default(),
            )
            .expect("update");
        assert_eq!(
            t.deadlines("t1"),
            json!({"respond": {"dueAt": T0 + 4 * HOUR, "fired": false}})
        );
        t.create("t3", json!({}));
        t.invoke(
            "t3",
            "snoozeTimer",
            json!({"deadline": "respond", "dueAt": T0 + 9 * HOUR}),
            &lead(),
        )
        .expect("snooze");

        // A new afterMs: t1's pending respond moves; t3's snooze stays.
        version(json!({
            "deadlines": {
                "respond": {"start": {"enters": "open"}, "afterMs": 6 * HOUR},
                "remind": {"start": {"field": "visitAt"}, "afterMs": -HOUR}
            },
            "permission": "tickets.timers"
        }));
        assert_eq!(
            (
                t.deadlines("t1")["respond"]["dueAt"].clone(),
                t.deadlines("t3")["respond"]["dueAt"].clone()
            ),
            (json!(T0 + 6 * HOUR), json!(T0 + 9 * HOUR))
        );

        // A changed start drops its rows, which the next write arms again;
        // a removed deadline goes.
        version(
            json!({"deadlines": {"respond": {"start": {"create": true}, "afterMs": 2 * HOUR}}, "permission": "tickets.timers"}),
        );
        assert_eq!(
            (t.deadlines("t1"), t.deadlines("t2"), t.deadlines("t3")),
            (json!({}), json!({}), json!({}))
        );
        t.engine
            .instances()
            .update(
                &alice(),
                "Ticket",
                "t3",
                &json!({"body": "Next write."}),
                WriteOptions::default(),
            )
            .expect("update");
        assert_eq!(
            t.deadlines("t3"),
            json!({"respond": {"dueAt": T0 + HOUR + 2 * HOUR, "fired": false}})
        );

        // Leaving drops every deadline; joining again arms the field ones.
        version(Value::Null);
        assert_eq!(t.read("t3").behaviors.get("Timers"), None);
        version(json!({"deadlines": {"remind": {"start": {"field": "visitAt"}}}}));
        assert_eq!(
            (t.deadlines("t2"), t.deadlines("t3")),
            (
                json!({"remind": {"dueAt": T0 + DAY, "fired": false}}),
                json!({})
            )
        );
    }
}

#[test]
fn deleting_the_instance_deletes_its_deadlines() {
    for target in targets("timers_delete") {
        let t = opened(
            &target,
            json!({"deadlines": {"ping": {"start": {"create": true}, "afterMs": HOUR}}}),
            &[],
        );
        t.create("t1", json!({}));
        t.engine
            .instances()
            .delete(&alice(), "Ticket", "t1", WriteOptions::default())
            .expect("delete");
        t.sweep(2 * HOUR).expect("sweep");
        t.create("t1", json!({}));
        assert_eq!(
            t.deadlines("t1"),
            json!({"ping": {"dueAt": T0 + 2 * HOUR + HOUR, "fired": false}})
        );
    }
}

#[test]
fn its_config_holds_each_start_then_and_until_to_the_type() {
    for target in targets("timers_config") {
        let engine = target.open_core_with(|options| options.behaviors = core());
        let define = |timers: Value, workflow: Value| {
            engine.schemas().define(
                &alice(),
                ticket_schema_named(timers, &[], workflow, "Ticket"),
                DefineOptions::default(),
            )
        };
        let one = |deadline: Value| json!({"deadlines": {"sla": deadline}});
        let cases: Vec<(Value, &str, Value)> = vec![
            (one(json!({"start": {"enters": "open"}})), "deadlines.sla.start.enters reads the status of Workflow, which the type does not list", Value::Null),
            (one(json!({"start": {"create": true}, "then": {"transition": "closed"}})), "deadlines.sla.then reads the status of Workflow, which the type does not list", Value::Null),
            (one(json!({"start": {"enters": "gone"}})), "deadlines.sla.start.enters \"gone\" is not a state of the type's Workflow (open, waiting, escalated, resolved, closed)", flow()),
            (one(json!({"start": {"field": "when"}})), "deadlines.sla.start.field \"when\" is not a field of Ticket (its fields: title, body, visitAt, visitDay)", flow()),
            (one(json!({"start": {"field": "visitDay"}})), "deadlines.sla.start.field \"visitDay\" is not a date-time field of Ticket", flow()),
            (one(json!({"start": {"enters": "open"}, "afterMs": -1})), "deadlines.sla.afterMs is negative, which only a field start takes", flow()),
            (one(json!({"start": {"enters": "open"}, "then": {"transition": "gone"}})), "deadlines.sla.then.transition \"gone\" is not a state", flow()),
            (one(json!({"start": {"enters": "waiting"}, "then": {"transition": "closed"}})), "deadlines.sla.then: no transition of the type's Workflow leads from \"waiting\" to \"closed\"", flow()),
            (one(json!({"start": {"enters": "open"}, "then": {"transition": "open"}})), "deadlines.sla.then moves the status from \"open\" to itself", flow()),
            (one(json!({"start": {"field": "visitAt"}, "then": {"transition": "closed"}})), "deadlines.sla.then names no from: a deadline that starts at a field is in no state of its own", flow()),
            (one(json!({"start": {"field": "visitAt"}, "until": {"leaves": "open"}})), "deadlines.sla.until.leaves: a deadline that starts at a field is in no state of its own to leave", flow()),
            (one(json!({"start": {"enters": "waiting"}, "until": {"leaves": "open"}})), "deadlines.sla.until.leaves \"open\" is not the state the deadline starts in, \"waiting\"", flow()),
            (one(json!({"start": {"create": true}, "until": {"leaves": "waiting"}})), "deadlines.sla.until.leaves \"waiting\" is not the state the deadline starts in, \"open\"", flow()),
            (one(json!({"start": {"create": true}, "until": {"enters": ["closed", "open"]}})), "deadlines.sla.until.enters names \"open\", the state the deadline starts in", flow()),
            (one(json!({"start": {"field": "visitAt"}, "until": {"enters": ["gone"]}})), "deadlines.sla.until.enters \"gone\" is not a state", flow()),
        ];
        for (timers, message, workflow) in cases {
            let refused = refusal(define(timers, workflow));
            assert!(
                refused.message().contains(message),
                "{message}: {}",
                refused.message()
            );
        }
        // A field deadline needs no Workflow, and its then names from.
        define(
            json!({"deadlines": {"sla": {"start": {"field": "visitAt"}, "afterMs": -HOUR}}}),
            Value::Null,
        )
        .expect("a field deadline without Workflow");
        define(
            one(json!({"start": {"field": "visitAt"}, "then": {"transition": "closed", "from": ["open", "resolved"]}, "until": {"enters": ["closed"]}})),
            flow(),
        )
        .expect("a field deadline that names from");
        // The core meta-schema holds the config to the declaration's
        // configSchema first.
        let paths = |timers: Value| -> Vec<String> {
            refusal(define(timers, flow()))
                .issues()
                .iter()
                .map(|issue| issue.path.clone())
                .collect()
        };
        assert!(paths(json!({"deadlines": {}}))
            .contains(&"/types/Ticket/behaviors/1/config/deadlines".to_owned()));
        assert!(
            paths(one(json!({"start": {"create": true, "enters": "open"}})))
                .iter()
                .any(
                    |path| path.starts_with("/types/Ticket/behaviors/1/config/deadlines/sla/start")
                )
        );
        let mut slow = one(json!({"start": {"create": true}}));
        slow["sweepMs"] = json!(999);
        assert!(paths(slow).contains(&"/types/Ticket/behaviors/1/config/sweepMs".to_owned()));
        assert!(
            paths(one(json!({"start": {"create": true}, "afterMs": 1.5})))
                .contains(&"/types/Ticket/behaviors/1/config/deadlines/sla/afterMs".to_owned())
        );
    }
}

/// The requests document the core binary builds (fixture-timers-json).
fn requests_document() -> Value {
    let path = repository()
        .join("internal/loader/testdata/services/fixture-timers-json/src/request.schema.json");
    serde_json::from_str(&std::fs::read_to_string(&path).expect("the requests document"))
        .expect("JSON")
}

/// The case of `test/core-behaviors.test.ts` that runs the requests
/// document on an engine with the runner.
#[test]
fn it_runs_the_requests_document_a_request_open_four_hours_is_escalated_and_a_visit_reminded() {
    for target in targets("timers_requests_document") {
        let writer = Principal::new("wes", Vec::<String>::new());
        let lead = Principal::new("lee", ["requests.timers"]);
        let engine = target.open_core_with(|options| {
            options.behaviors = core();
            options.clock = Clock::Manual(T0);
            options.runner = Some(RunnerOptions::new(runner()));
        });
        publish(&engine, &requests_document());
        assert_eq!(
            engine
                .schemas()
                .behaviors(&alice(), "requests", Default::default())
                .expect("behaviors")
                .iter()
                .map(|behavior| behavior.name.as_str())
                .collect::<Vec<_>>(),
            ["Workflow", "Timers"]
        );
        engine.runner().run_due().expect("the runner's first pass");
        let start = T0;
        let created = engine
            .instances()
            .create(
                &writer,
                "requests",
                &json!({"title": "Printer jams", "visitAt": at(start + 3 * DAY)}),
                CreateOptions {
                    id: Some("jam"),
                    ..Default::default()
                },
            )
            .expect("create");
        assert_eq!(
            created.behaviors["Timers"],
            json!({"deadlines": {"remind": {"dueAt": start + 2 * DAY, "fired": false}, "respond": {"dueAt": start + 4 * HOUR, "fired": false}}})
        );
        engine
            .instances()
            .create(
                &writer,
                "requests",
                &json!({"title": "Toner"}),
                CreateOptions {
                    id: Some("toner"),
                    ..Default::default()
                },
            )
            .expect("create");
        let invoke = |who: &Principal, id: &str, operation: &str, params: Value| {
            engine
                .instances()
                .invoke(
                    who,
                    "requests",
                    id,
                    operation,
                    &params,
                    WriteOptions::default(),
                )
                .expect(operation)
        };
        invoke(&writer, "toner", "transition", json!({"to": "waiting"}));
        // The waiting request's lapse is snoozed a day, by a lead.
        invoke(
            &lead,
            "toner",
            "snoozeTimer",
            json!({"deadline": "lapse", "dueAt": start + 8 * DAY}),
        );
        let read = |id: &str| {
            engine
                .instances()
                .get(&alice(), "requests", id, GetOptions::default())
                .expect("get")
                .expect("the request")
        };
        let run = |at: i64| {
            engine.clock().set(at).expect("set");
            engine.runner().run_due().expect("run due");
        };
        run(start + 5 * HOUR);
        assert_eq!(
            (
                read("jam").behaviors["Workflow"]["status"].clone(),
                read("toner").behaviors["Workflow"]["status"].clone()
            ),
            (json!("escalated"), json!("waiting"))
        );
        assert_eq!(
            read("jam").behaviors["Timers"]["deadlines"]["respond"],
            json!({"dueAt": start + 4 * HOUR, "fired": true, "firedAt": start + 5 * HOUR, "outcome": "moved"})
        );
        run(start + 2 * DAY);
        assert_eq!(
            read("jam").behaviors["Timers"]["deadlines"]["remind"]["fired"],
            true
        );
        // The customer answers: leaving waiting cancels lapse.
        invoke(&writer, "toner", "transition", json!({"to": "open"}));
        assert_eq!(
            read("toner").behaviors["Timers"]["deadlines"]
                .as_object()
                .expect("deadlines")
                .keys()
                .collect::<Vec<_>>(),
            ["respond"]
        );
    }
}
