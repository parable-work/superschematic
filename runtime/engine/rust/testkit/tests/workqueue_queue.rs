//! Queue, claimable work (D16): the cases of the work queue's
//! `test/queue.test.ts` (claim takes the lease and moves the status in one
//! transaction, after checks it reads from the instance itself; claimNext
//! picks candidates from Queue's own copies, in priority order, filtered by
//! the caller's assignment and the match values, and skips the ones whose
//! claim is refused, vetoed or forbidden; the copies follow the instance,
//! its blockers and its enclosing budget scopes, so work over its budget is
//! not tried and does not starve the work behind it; countClaimable counts
//! the same candidates; and the config rules), on SQLite and, with the
//! variable set, on Postgres. Queue requires Lease, so every case whose
//! type composes Queue waits for it (milestone 12a). The guidance is held
//! to the TypeScript text in the work-queue crate's `tests/guidance_queue.rs`.

mod support;

use serde_json::{json, Map, Value};

use superschematic_engine::storage::{Lock, SqlValue};
use superschematic_engine::{
    BehaviorSet, Clock, CreateOptions, DefineOptions, Engine, EngineErrorCode, EngineOptions,
    InstanceRecord, Principal, ReadEvents, Result, RunnerOptions, SchemaTarget, WriteOptions,
};
use superschematic_engine_testkit::workqueue_fixtures::{BudgetStandIn, Gate, Lock as LockFixture};
use support::{alice, issue, issues_of, refusal, targets, veto, Target};

const T0: i64 = 1_000_000;
const DAY: i64 = 86_400_000;

fn principal(subject: &str, permissions: &[&str]) -> Principal {
    Principal::new(subject, permissions.iter().map(|one| (*one).to_owned()))
}

fn worker() -> Principal {
    principal("wren", &[])
}

fn other() -> Principal {
    principal("otto", &[])
}

fn operator() -> Principal {
    principal("opal", &["jobs.override"])
}

fn lead() -> Principal {
    principal("lena", &["jobs.assign"])
}

fn pam() -> Principal {
    principal("pam", &["pools.limit"])
}

fn requeue() -> Value {
    json!({"onExpiry": {"transition": "queued", "from": ["running"]}})
}

fn claimable() -> Value {
    json!({"claim": {"from": ["queued"], "to": "running"}, "priorityField": "priority", "match": ["topic", "urgent"]})
}

fn with(base: Value, extra: Value) -> Value {
    let mut out = base;
    if let (Some(out), Value::Object(extra)) = (out.as_object_mut(), extra) {
        out.extend(extra);
    }
    out
}

// A step is a piece of work another schema's jobs can wait on.
fn step_document() -> Value {
    json!({"kind": "General", "name": "Step", "types": {"Step": {
        "name": "Step",
        "role": "EmbeddedStruct",
        "behaviors": [{"name": "Workflow", "config": {"states": ["todo", "done"], "transitions": [{"from": "todo", "to": "done"}]}}],
        "fields": [{"name": "title", "typeRef": {"name": "string"}, "required": true}]
    }}})
}

fn job_flow() -> Value {
    json!({
        "states": ["queued", "running", "done", "failed"],
        "transitions": [
            {"from": "queued", "to": "running"},
            {"from": "running", "to": "queued"},
            {"from": "running", "to": "done"},
            {"from": "running", "to": "failed"},
            {"from": "queued", "to": "failed"}
        ]
    })
}

/// A General schema named Job whose instance type has a title, a priority,
/// a time limit, a topic and an urgent flag, and composes the behaviors
/// given.
fn jobs_document(behaviors: Value, name: &str) -> Value {
    json!({"kind": "General", "name": name, "types": {name: {
        "name": name,
        "role": "EmbeddedStruct",
        "behaviors": behaviors,
        "fields": [
            {"name": "title", "typeRef": {"name": "string"}, "required": true},
            {"name": "priority", "typeRef": {"name": "Generic.Int64"}},
            {"name": "timeLimitMs", "typeRef": {"name": "Generic.Int64"}},
            {"name": "topic", "typeRef": {"name": "string"}},
            {"name": "urgent", "typeRef": {"name": "boolean"}}
        ]
    }}})
}

#[derive(Default)]
struct World {
    lease: Option<Value>,
    queue: Option<Value>,
    extra: Vec<Value>,
    /// The behaviors the engine runs in place of the work queue's.
    behaviors: Option<BehaviorSet>,
    runner: Option<Principal>,
}

// jobs is the Job schema: Workflow, Lease, Queue and the extra behaviors,
// with the configs given.
fn jobs(spec: &World) -> Value {
    let mut behaviors = vec![
        json!({"name": "Workflow", "config": job_flow()}),
        json!({"name": "Lease", "config": spec.lease.clone().unwrap_or_else(requeue)}),
        json!({"name": "Queue", "config": spec.queue.clone().unwrap_or_else(claimable)}),
    ];
    behaviors.extend(spec.extra.iter().cloned());
    jobs_document(Value::Array(behaviors), "Job")
}

// work_queue is the work queue's behaviors by name, as the TypeScript test
// lists the implementations it registers.
fn work_queue(names: &[&str]) -> BehaviorSet {
    names.iter().fold(BehaviorSet::new(), |set, name| {
        set.extend(superschematic_engine_workqueue::behavior(name).expect("a ported behavior"))
    })
}

fn open_with(
    target: &Target,
    behaviors: Option<BehaviorSet>,
    configure: impl FnOnce(&mut EngineOptions),
) -> Engine {
    match behaviors {
        // The test behaviors load against the open meta-schema, beside the
        // core's.
        Some(set) => target.open_with(
            superschematic_engine_behaviors::core().extend(set),
            |options| {
                options.clock = Clock::Manual(T0);
                configure(options);
            },
        ),
        None => target.open_workqueue_with(|options| {
            options.clock = Clock::Manual(T0);
            configure(options);
        }),
    }
}

// world opens an engine whose Job composes Workflow, Lease, Queue and the
// extra behaviors, with the configs given.
fn world(target: &Target, spec: World) -> Engine {
    let runner = spec.runner.clone();
    let behaviors = spec.behaviors.clone();
    let engine = open_with(target, behaviors, |options| {
        if let Some(runner) = runner {
            options.runner = Some(RunnerOptions::new(runner));
        }
    });
    publish(&engine, &jobs(&spec));
    engine
}

fn define(engine: &Engine, document: &Value) -> Result<()> {
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

fn create_in(
    engine: &Engine,
    schema: &str,
    id: &str,
    data: Value,
    behaviors: Option<Value>,
) -> InstanceRecord {
    let fields = with(json!({"title": id}), data);
    engine
        .instances()
        .create(
            &alice(),
            schema,
            &fields,
            CreateOptions {
                id: Some(id),
                behaviors: behaviors.as_ref(),
                ..Default::default()
            },
        )
        .expect("create")
}

fn create(engine: &Engine, id: &str, data: Value) -> InstanceRecord {
    create_in(engine, "Job", id, data, None)
}

fn invoke(
    engine: &Engine,
    who: &Principal,
    schema: &str,
    id: &str,
    operation: &str,
    params: Value,
) -> Result<Value> {
    engine
        .instances()
        .invoke(who, schema, id, operation, &params, WriteOptions::default())
}

fn job(engine: &Engine, who: &Principal, id: &str, operation: &str, params: Value) -> Value {
    invoke(engine, who, "Job", id, operation, params).expect(operation)
}

fn fenced(
    engine: &Engine,
    who: &Principal,
    id: &str,
    operation: &str,
    params: Value,
    token: i64,
) -> Value {
    let preconditions = json!({"Lease": {"token": token}});
    engine
        .instances()
        .invoke(
            who,
            "Job",
            id,
            operation,
            &params,
            WriteOptions {
                preconditions: Some(&preconditions),
                ..Default::default()
            },
        )
        .expect(operation)
}

fn claim(engine: &Engine, who: &Principal, id: &str, params: Value) -> Result<Value> {
    invoke(engine, who, "Job", id, "claim", params)
}

fn claim_next(engine: &Engine, who: &Principal, params: Value) -> Result<Value> {
    engine
        .instances()
        .invoke_schema(who, "Job", "claimNext", &params, None)
        .map(|answer| answer["claimed"].clone())
}

fn next(engine: &Engine, who: &Principal, params: Value) -> Option<String> {
    claim_next(engine, who, params)
        .expect("claimNext")
        .get("id")
        .and_then(Value::as_str)
        .map(str::to_owned)
}

fn count(engine: &Engine, who: &Principal, params: Value) -> i64 {
    engine
        .instances()
        .invoke_schema(who, "Job", "countClaimable", &params, None)
        .expect("countClaimable")["count"]
        .as_i64()
        .expect("a count")
}

fn get(engine: &Engine, schema: &str, id: &str) -> InstanceRecord {
    engine
        .instances()
        .get(&alice(), schema, id, Default::default())
        .expect("get")
        .expect("the instance")
}

fn status_of(engine: &Engine, id: &str) -> Value {
    get(engine, "Job", id)
        .behavior_field("Workflow", "status")
        .cloned()
        .unwrap_or(Value::Null)
}

fn lease_of(engine: &Engine, id: &str) -> Value {
    get(engine, "Job", id)
        .behaviors
        .get("Lease")
        .cloned()
        .unwrap_or(Value::Null)
}

fn events(
    engine: &Engine,
    schema: &str,
    id: Option<&str>,
    after: Option<i64>,
) -> Vec<superschematic_engine::EngineEvent> {
    engine
        .events()
        .read(
            &alice(),
            &ReadEvents {
                schema: Some(schema),
                instance_id: id,
                after: after.map(superschematic_engine::EventsAfter::Cursor),
                limit: Some(500),
                ..ReadEvents::default()
            },
        )
        .expect("events")
        .events
}

// The next event's cursor is past this one: the last of the log.
fn cursor_of(engine: &Engine) -> i64 {
    engine
        .events()
        .read(
            &alice(),
            &ReadEvents {
                limit: Some(500),
                ..ReadEvents::default()
            },
        )
        .expect("events")
        .events
        .last()
        .map_or(0, |event| event.cursor)
}

// refreshes lists the refresh events of Job after a cursor, [id, actor].
fn refreshes(engine: &Engine, after: i64) -> Vec<(String, String)> {
    events(engine, "Job", None, Some(after))
        .into_iter()
        .filter(|event| event.change.get("operation") == Some(&json!("refresh")))
        .map(|event| (event.instance_id.unwrap_or_default(), event.actor))
        .collect()
}

fn pairs(list: &[(&str, &str)]) -> Vec<(String, String)> {
    list.iter()
        .map(|(id, actor)| ((*id).to_owned(), (*actor).to_owned()))
        .collect()
}

// row reads the first row a statement returns, as JSON.
fn row(engine: &Engine, sql: &str, params: &[SqlValue]) -> Value {
    let found = engine
        .db()
        .read(|tx| tx.get(sql, params))
        .expect("read")
        .expect("a row");
    let mut out = Map::new();
    for (name, value) in found.iter() {
        out.insert(
            name.to_owned(),
            match value {
                SqlValue::Null => Value::Null,
                SqlValue::Integer(n) => Value::from(*n),
                SqlValue::Real(x) => Value::from(*x),
                SqlValue::Text(text) => Value::from(text.as_str()),
                SqlValue::Blob(_) => Value::from("<blob>"),
            },
        );
    }
    Value::Object(out)
}

fn run_sql(engine: &Engine, sql: &str, params: &[SqlValue]) {
    engine
        .db()
        .write(Lock::Catalog, |tx| tx.run(sql, params))
        .expect("run");
}

// Queue: claim.

#[test]
#[ignore = "needs Lease (12a)"]
fn claim_takes_the_lease_and_moves_the_status_in_one_call() {
    for target in targets("claim_takes_the_lease") {
        let engine = world(&target, World::default());
        create(&engine, "j1", json!({}));
        assert_eq!(
            claim(&engine, &worker(), "j1", json!({})).expect("claim"),
            json!({"id": "j1", "token": 1, "expiresAt": T0 + 60000, "heartbeatMs": 20000})
        );
        assert_eq!(status_of(&engine, "j1"), json!("running"));
        assert_eq!(lease_of(&engine, "j1")["holder"], json!("wren"));
        // One event records both.
        let all = events(&engine, "Job", Some("j1"), None);
        let last = all.last().expect("an event");
        assert_eq!(
            (last.actor.as_str(), &last.change["operation"]),
            ("wren", &json!("claim"))
        );
        assert_eq!(
            last.change["patch"]["behaviors"]["Workflow"]["status"],
            json!("running")
        );
        // A claimed instance is another principal's lease.
        assert_eq!(veto(claim(&engine, &other(), "j1", json!({}))).0, "Lease");
        // The lease's length is Lease's acquire's.
        create(&engine, "j2", json!({}));
        assert_eq!(
            claim(&engine, &worker(), "j2", json!({"ttlMs": 30000})).expect("claim"),
            json!({"id": "j2", "token": 1, "expiresAt": T0 + 30000, "heartbeatMs": 10000})
        );
    }
}

#[test]
#[ignore = "needs Lease (12a)"]
fn claim_checks_the_instance_itself_a_state_it_is_not_claimed_from_and_a_blocker_refuse_it() {
    for target in targets("claim_checks_the_instance_itself") {
        let engine = world(
            &target,
            World {
                extra: vec![json!({"name": "Dependencies"})],
                ..World::default()
            },
        );
        for id in ["j1", "j2", "j3"] {
            create(&engine, id, json!({}));
        }
        job(
            &engine,
            &alice(),
            "j1",
            "transition",
            json!({"to": "failed"}),
        );
        let failed = veto(claim(&engine, &worker(), "j1", json!({})));
        assert_eq!(
            (failed.2.as_str(), failed.3.as_deref(), failed.4),
            (
                "it is failed, and it is claimed from queued",
                Some("not_claimable"),
                Some(json!({"status": "failed", "from": ["queued"]}))
            )
        );
        job(&engine, &alice(), "j2", "addBlocker", json!({"id": "j3"}));
        let blocked = veto(claim(&engine, &worker(), "j2", json!({})));
        assert_eq!(
            (blocked.0.as_str(), blocked.2.as_str(), blocked.3.as_deref()),
            ("Queue", "a blocker holds it up", Some("blocked"))
        );
        assert_eq!(
            [
                lease_of(&engine, "j2")["token"].clone(),
                status_of(&engine, "j2")
            ],
            [json!(0), json!("queued")]
        );
    }
}

#[test]
#[ignore = "needs Lease (12a)"]
fn a_lease_on_a_type_that_composes_queue_is_taken_only_by_claiming_it() {
    for target in targets("taken_only_by_claiming") {
        let engine = world(&target, World::default());
        create(&engine, "j1", json!({}));
        let refused = veto(invoke(
            &engine,
            &worker(),
            "Job",
            "j1",
            "acquire",
            json!({}),
        ));
        assert_eq!(
            (refused.0.as_str(), refused.2.as_str(), refused.3.as_deref()),
            (
                "Queue",
                "its lease is taken by claiming it (Queue's claim), which checks that it can be claimed",
                Some("claim_required")
            )
        );
    }
}

#[test]
#[ignore = "needs Lease (12a)"]
fn a_refusal_after_the_lease_is_taken_undoes_the_lease() {
    for target in targets("a_refusal_undoes_the_lease") {
        let engine = world(
            &target,
            World {
                behaviors: Some(work_queue(&["Lease", "Queue"]).with(Gate)),
                extra: vec![json!({"name": "test.Gate"})],
                ..World::default()
            },
        );
        create(&engine, "held", json!({}));
        let refused = veto(claim(&engine, &worker(), "held", json!({})));
        assert_eq!(
            (refused.0.as_str(), refused.1.as_str(), refused.2.as_str()),
            ("test.Gate", "transition", "it is held")
        );
        let lease = lease_of(&engine, "held");
        assert_eq!(
            [
                lease.get("holder").cloned().unwrap_or(Value::Null),
                lease["token"].clone(),
                status_of(&engine, "held")
            ],
            [Value::Null, json!(0), json!("queued")]
        );
    }
}

#[test]
#[ignore = "needs Lease (12a)"]
fn claim_expires_a_lapsed_lease_first_and_checks_the_status_its_expiry_leaves() {
    for target in targets("claim_expires_a_lapsed_lease") {
        let engine = world(&target, World::default());
        create(&engine, "j1", json!({}));
        claim(&engine, &worker(), "j1", json!({})).expect("claim");
        engine.clock().advance(60000).expect("advance");
        assert_eq!(
            claim(&engine, &other(), "j1", json!({})).expect("claim"),
            json!({"id": "j1", "token": 3, "expiresAt": T0 + 120000, "heartbeatMs": 20000})
        );
        let lease = lease_of(&engine, "j1");
        assert_eq!(
            [
                status_of(&engine, "j1"),
                lease["holder"].clone(),
                lease["expiries"].clone()
            ],
            [json!("running"), json!("otto"), json!(1)]
        );
    }
}

#[test]
#[ignore = "needs Lease (12a)"]
fn with_budget_on_the_type_claim_reserves_after_the_lease_and_before_the_transition() {
    for target in targets("claim_reserves_after_the_lease") {
        let budget = BudgetStandIn::default();
        let engine = world(
            &target,
            World {
                behaviors: Some(work_queue(&["Lease", "Queue"]).with(budget.clone())),
                extra: vec![json!({"name": "Budget"})],
                ..World::default()
            },
        );
        create(&engine, "j1", json!({}));
        create(&engine, "broke", json!({}));
        claim(&engine, &worker(), "j1", json!({})).expect("claim");
        assert_eq!(
            budget.take(),
            vec![json!({"id": "j1", "params": {}, "holder": "wren", "status": "queued"})]
        );
        assert_eq!(
            veto(claim(&engine, &worker(), "broke", json!({}))).2,
            "the reservation does not fit"
        );
        let lease = lease_of(&engine, "broke");
        assert_eq!(
            [
                lease.get("holder").cloned().unwrap_or(Value::Null),
                lease["token"].clone(),
                status_of(&engine, "broke")
            ],
            [Value::Null, json!(0), json!("queued")]
        );
        // claimNext does not try it, since Budget's checkReserve says it does
        // not fit.
        budget.take();
        assert_eq!(next(&engine, &other(), json!({})), None);
        assert!(budget.take().is_empty());
    }
}

// Queue: claimNext.

#[test]
#[ignore = "needs Lease (12a)"]
fn claim_next_claims_highest_priority_first_then_oldest_then_by_id() {
    for target in targets("claim_next_claims_highest_priority_first") {
        let engine = world(&target, World::default());
        create(&engine, "low", json!({"priority": 1}));
        engine.clock().advance(1).expect("advance");
        create(&engine, "high", json!({"priority": 9}));
        engine.clock().advance(1).expect("advance");
        create(&engine, "none", json!({}));
        engine.clock().advance(1).expect("advance");
        create(&engine, "tie-c", json!({"priority": 9}));
        create(&engine, "tie-b", json!({"priority": 9}));
        let order: Vec<Option<String>> = (0..6)
            .map(|_| next(&engine, &worker(), json!({})))
            .collect();
        assert_eq!(
            order,
            [
                Some("high"),
                Some("tie-b"),
                Some("tie-c"),
                Some("low"),
                Some("none"),
                None
            ]
            .map(|id| id.map(str::to_owned))
        );
        assert_eq!(
            claim_next(&engine, &worker(), json!({})).expect("claimNext"),
            Value::Null
        );
    }
}

#[test]
#[ignore = "needs Lease (12a)"]
fn a_change_of_priority_reorders_the_queue() {
    for target in targets("a_change_of_priority") {
        let engine = world(&target, World::default());
        create(&engine, "a", json!({"priority": 1}));
        create(&engine, "b", json!({"priority": 2}));
        engine
            .instances()
            .update(
                &alice(),
                "Job",
                "a",
                &json!({"priority": 5}),
                WriteOptions::default(),
            )
            .expect("update");
        assert_eq!(next(&engine, &worker(), json!({})).as_deref(), Some("a"));
        engine
            .instances()
            .update(
                &alice(),
                "Job",
                "b",
                &json!({"priority": null}),
                WriteOptions::default(),
            )
            .expect("update");
        create(&engine, "c", json!({}));
        assert_eq!(next(&engine, &worker(), json!({})).as_deref(), Some("b"));
    }
}

#[test]
#[ignore = "needs Lease (12a)"]
fn match_takes_a_list_of_values_any_of_which_an_instance_holds() {
    for target in targets("match_takes_a_list") {
        let engine = world(&target, World::default());
        create(&engine, "search", json!({"topic": "search", "priority": 5}));
        create(&engine, "mail", json!({"topic": "mail", "priority": 9}));
        create(&engine, "misc", json!({"topic": "misc", "priority": 10}));
        create(
            &engine,
            "urgent",
            json!({"topic": "mail", "urgent": true, "priority": 1}),
        );
        let both = json!({"match": {"topic": ["search", "mail"]}});
        assert_eq!(
            next(&engine, &worker(), both.clone()).as_deref(),
            Some("mail")
        );
        assert_eq!(
            next(
                &engine,
                &worker(),
                json!({"match": {"topic": ["search", "mail"], "urgent": [true]}})
            )
            .as_deref(),
            Some("urgent")
        );
        assert_eq!(
            next(&engine, &worker(), both.clone()).as_deref(),
            Some("search")
        );
        assert_eq!(next(&engine, &worker(), both), None);
        assert_eq!(
            refusal(claim_next(
                &engine,
                &worker(),
                json!({"match": {"topic": []}})
            ))
            .code(),
            EngineErrorCode::InvalidArgument
        );
    }
}

#[test]
#[ignore = "needs Lease (12a)"]
fn claim_next_passes_ttl_ms_to_the_claim() {
    for target in targets("claim_next_passes_ttl_ms") {
        let engine = world(&target, World::default());
        create(&engine, "j1", json!({}));
        assert_eq!(
            claim_next(&engine, &worker(), json!({"ttlMs": 30000})).expect("claimNext"),
            json!({"id": "j1", "token": 1, "expiresAt": T0 + 30000, "heartbeatMs": 10000})
        );
    }
}

#[test]
#[ignore = "needs Lease (12a)"]
fn a_claim_forbidden_to_the_caller_is_skipped_and_when_every_one_is_claim_next_says_why() {
    for target in targets("a_claim_forbidden_to_the_caller") {
        let engine = world(
            &target,
            World {
                behaviors: Some(work_queue(&["Lease", "Queue"]).with(LockFixture)),
                extra: vec![json!({"name": "test.Lock"})],
                ..World::default()
            },
        );
        create(&engine, "locked", json!({"priority": 9}));
        create(&engine, "open", json!({"priority": 1}));
        assert_eq!(next(&engine, &worker(), json!({})).as_deref(), Some("open"));
        let refused = refusal(claim_next(&engine, &worker(), json!({})));
        assert_eq!(
            (refused.code(), refused.message()),
            (
                EngineErrorCode::Forbidden,
                "wren may not claim Job locked: it needs permission jobs.unlock"
            )
        );
        assert_eq!(
            next(&engine, &principal("kim", &["jobs.unlock"]), json!({})).as_deref(),
            Some("locked")
        );
    }
}

#[test]
#[ignore = "needs Lease (12a)"]
fn match_filters_on_the_configs_fields_by_equality_and_refuses_another_field() {
    for target in targets("match_filters_on_the_configs_fields") {
        let engine = world(&target, World::default());
        create(&engine, "search", json!({"topic": "search", "priority": 9}));
        create(&engine, "mail", json!({"topic": "mail"}));
        create(
            &engine,
            "urgent-mail",
            json!({"topic": "mail", "urgent": true}),
        );
        assert_eq!(
            next(
                &engine,
                &worker(),
                json!({"match": {"topic": "mail", "urgent": true}})
            )
            .as_deref(),
            Some("urgent-mail")
        );
        assert_eq!(
            next(&engine, &worker(), json!({"match": {"topic": "mail"}})).as_deref(),
            Some("mail")
        );
        assert_eq!(
            next(&engine, &worker(), json!({"match": {"topic": "mail"}})),
            None
        );
        assert_eq!(
            next(&engine, &worker(), json!({"match": {"topic": "search"}})).as_deref(),
            Some("search")
        );
        assert_eq!(
            issues_of(&refusal(claim_next(
                &engine,
                &worker(),
                json!({"match": {"title": "x"}})
            ))),
            vec![issue(
                "/match/title",
                "claimNext on Job matches topic, urgent, not title"
            )]
        );
    }
}

fn assigned() -> Value {
    json!({"name": "Assignment", "config": {"permission": "jobs.assign"}})
}

#[test]
#[ignore = "needs Lease and Assignment (12a)"]
fn an_instance_assigned_to_a_principal_is_a_candidate_for_that_principal_only() {
    for target in targets("an_instance_assigned_to_a_principal") {
        let engine = world(
            &target,
            World {
                extra: vec![assigned()],
                ..World::default()
            },
        );
        create(&engine, "mine", json!({"priority": 9}));
        create(&engine, "open", json!({"priority": 1}));
        job(&engine, &lead(), "mine", "assign", json!({"to": "wren"}));
        assert_eq!(next(&engine, &other(), json!({})).as_deref(), Some("open"));
        assert_eq!(next(&engine, &other(), json!({})), None);
        assert_eq!(next(&engine, &worker(), json!({})).as_deref(), Some("mine"));
        // The claim itself is refused to anyone but the assignee, whatever
        // the copies say.
        create(&engine, "later", json!({}));
        job(&engine, &lead(), "later", "assign", json!({"to": "wren"}));
        let refused = veto(claim(&engine, &other(), "later", json!({})));
        assert_eq!(
            (refused.0.as_str(), refused.3.as_deref()),
            ("Assignment", Some("assigned_to_another"))
        );
    }
}

#[test]
#[ignore = "needs Lease and Assignment (12a)"]
fn assigned_only_takes_only_the_work_assigned_to_the_caller_and_needs_assignment() {
    for target in targets("assigned_only") {
        let engine = world(
            &target,
            World {
                extra: vec![assigned()],
                ..World::default()
            },
        );
        create(&engine, "open", json!({"priority": 9}));
        create(&engine, "mine", json!({"priority": 1}));
        job(&engine, &lead(), "mine", "assign", json!({"to": "wren"}));
        assert_eq!(
            [
                count(&engine, &worker(), json!({})),
                count(&engine, &worker(), json!({"assignedOnly": true})),
                count(&engine, &other(), json!({"assignedOnly": true}))
            ],
            [2, 1, 0]
        );
        assert_eq!(
            next(&engine, &worker(), json!({"assignedOnly": true})).as_deref(),
            Some("mine")
        );
        assert_eq!(
            next(&engine, &worker(), json!({"assignedOnly": true})),
            None
        );
        assert_eq!(next(&engine, &worker(), json!({})).as_deref(), Some("open"));
    }
    for target in targets("assigned_only_needs_assignment") {
        let plain = world(&target, World::default());
        assert_eq!(
            issues_of(&refusal(claim_next(
                &plain,
                &worker(),
                json!({"assignedOnly": true})
            ))),
            vec![issue(
                "/assignedOnly",
                "Job does not compose Assignment, so none of its instances is assigned"
            )]
        );
    }
}

#[test]
#[ignore = "needs Lease and Assignment (12a)"]
fn count_claimable_counts_what_claim_next_would_try_without_max_candidates_and_changes_nothing() {
    for target in targets("count_claimable_counts") {
        let engine = world(
            &target,
            World {
                queue: Some(with(claimable(), json!({"maxCandidates": 1}))),
                extra: vec![json!({"name": "Dependencies"}), assigned()],
                ..World::default()
            },
        );
        for (id, topic) in [
            ("a", "search"),
            ("b", "search"),
            ("c", "mail"),
            ("blocked", "search"),
            ("theirs", "search"),
            ("failed", "search"),
        ] {
            create(&engine, id, json!({"topic": topic}));
        }
        job(
            &engine,
            &alice(),
            "blocked",
            "addBlocker",
            json!({"id": "a"}),
        );
        job(&engine, &lead(), "theirs", "assign", json!({"to": "otto"}));
        job(
            &engine,
            &alice(),
            "failed",
            "transition",
            json!({"to": "failed"}),
        );
        let cursor = cursor_of(&engine);
        assert_eq!(
            [
                count(&engine, &worker(), json!({})),
                count(&engine, &worker(), json!({"match": {"topic": "search"}})),
                count(
                    &engine,
                    &worker(),
                    json!({"match": {"topic": ["search", "mail"]}})
                )
            ],
            [3, 2, 3]
        );
        // The next event is the claim's: counting appended none.
        assert_eq!(cursor_of(&engine), cursor);
        let refused = refusal(engine.instances().invoke_schema(
            &worker(),
            "Job",
            "countClaimable",
            &json!({"match": {"title": "a"}}),
            None,
        ));
        assert_eq!(
            issues_of(&refused),
            vec![issue(
                "/match/title",
                "countClaimable on Job matches topic, urgent, not title"
            )]
        );
        assert_eq!(next(&engine, &worker(), json!({})).as_deref(), Some("a"));
        assert_eq!(count(&engine, &worker(), json!({})), 2);
    }
}

fn dependencies_across() -> Value {
    json!({"name": "Dependencies", "config": {"schemas": ["Job", "Step"]}})
}

#[test]
#[ignore = "needs Lease (12a)"]
fn an_instance_waits_while_a_blocker_in_another_schema_is_open() {
    for target in targets("waits_while_a_blocker_is_open") {
        let engine = world(
            &target,
            World {
                extra: vec![dependencies_across()],
                ..World::default()
            },
        );
        publish(&engine, &step_document());
        create_in(&engine, "Step", "s1", json!({"title": "Approve"}), None);
        create(&engine, "j1", json!({}));
        job(
            &engine,
            &alice(),
            "j1",
            "addBlocker",
            json!({"schema": "Step", "id": "s1"}),
        );
        assert_eq!(next(&engine, &worker(), json!({})), None);
        // The step's change, by another principal, refreshes the job's copies
        // through its reference.
        invoke(
            &engine,
            &other(),
            "Step",
            "s1",
            "transition",
            json!({"to": "done"}),
        )
        .expect("transition");
        assert_eq!(next(&engine, &worker(), json!({})).as_deref(), Some("j1"));
        let refreshed = events(&engine, "Job", Some("j1"), None)
            .into_iter()
            .find(|event| event.change.get("operation") == Some(&json!("refresh")))
            .expect("a refresh");
        assert_eq!(refreshed.actor, "otto");
    }
}

#[test]
#[ignore = "needs Lease (12a)"]
fn an_instance_a_create_gives_its_blockers_is_no_candidate_from_its_create() {
    for target in targets("a_create_gives_its_blockers") {
        let engine = world(
            &target,
            World {
                extra: vec![dependencies_across()],
                ..World::default()
            },
        );
        publish(&engine, &step_document());
        create_in(&engine, "Step", "s1", json!({"title": "Approve"}), None);
        create_in(
            &engine,
            "Job",
            "j1",
            json!({}),
            Some(json!({"Dependencies": {"blockers": [{"schema": "Step", "id": "s1"}]}})),
        );
        assert_eq!(
            row(&engine, "SELECT bhv_queue__status AS status, bhv_queue__blocked AS blocked FROM engine_instances WHERE schema = 'Job' AND id = 'j1'", &[]),
            json!({"status": "queued", "blocked": 1})
        );
        assert_eq!(next(&engine, &worker(), json!({})), None);
        assert_eq!(count(&engine, &worker(), json!({})), 0);
        // Its create event is its only one: the copies were set in it.
        let kinds: Vec<String> = events(&engine, "Job", Some("j1"), None)
            .into_iter()
            .map(|event| event.kind)
            .collect();
        assert_eq!(kinds, ["create"]);
        invoke(
            &engine,
            &other(),
            "Step",
            "s1",
            "transition",
            json!({"to": "done"}),
        )
        .expect("transition");
        assert_eq!(next(&engine, &worker(), json!({})).as_deref(), Some("j1"));
    }
}

#[test]
#[ignore = "needs Lease (12a)"]
fn a_refresh_that_finds_the_copies_right_changes_nothing_and_one_that_finds_them_wrong_writes() {
    for target in targets("a_refresh_that_finds_the_copies_right") {
        let engine = world(
            &target,
            World {
                extra: vec![dependencies_across()],
                ..World::default()
            },
        );
        publish(&engine, &step_document());
        create_in(&engine, "Step", "s1", json!({"title": "Approve"}), None);
        create_in(
            &engine,
            "Job",
            "j1",
            json!({}),
            Some(json!({"Dependencies": {"blockers": [{"schema": "Step", "id": "s1"}]}})),
        );
        let seq = get(&engine, "Job", "j1").seq;
        let operated = engine
            .instances()
            .operate(
                &alice(),
                "Job",
                "j1",
                "refresh",
                &json!({}),
                WriteOptions::default(),
            )
            .expect("refresh");
        assert_eq!((operated.result, operated.seq), (json!({}), seq));
        let kinds: Vec<String> = events(&engine, "Job", Some("j1"), None)
            .into_iter()
            .map(|event| event.kind)
            .collect();
        assert_eq!(kinds, ["create"]);
        // A copy out of step, as one a version before the copies kept would
        // be, is set right by a refresh with its event.
        run_sql(
            &engine,
            "UPDATE engine_instances SET bhv_queue__blocked = 0 WHERE schema = 'Job' AND id = 'j1'",
            &[],
        );
        let operated = engine
            .instances()
            .operate(
                &alice(),
                "Job",
                "j1",
                "refresh",
                &json!({}),
                WriteOptions::default(),
            )
            .expect("refresh");
        assert_eq!((operated.result, operated.seq), (json!({}), seq + 1));
        assert_eq!(
            row(&engine, "SELECT bhv_queue__blocked AS blocked FROM engine_instances WHERE schema = 'Job' AND id = 'j1'", &[]),
            json!({"blocked": 1})
        );
    }
}

#[test]
#[ignore = "needs Lease (12a)"]
fn a_blockers_change_refreshes_a_dependent_another_principal_holds_the_lease_of() {
    for target in targets("leases_guard_lets_refresh_through") {
        let engine = world(
            &target,
            World {
                extra: vec![dependencies_across()],
                ..World::default()
            },
        );
        publish(&engine, &step_document());
        create_in(&engine, "Step", "s1", json!({"title": "Review"}), None);
        create(&engine, "j1", json!({}));
        claim(&engine, &worker(), "j1", json!({})).expect("claim");
        job(
            &engine,
            &worker(),
            "j1",
            "addBlocker",
            json!({"schema": "Step", "id": "s1"}),
        );
        // otto may not change j1 itself while wren holds it...
        assert_eq!(
            veto(engine.instances().update(
                &other(),
                "Job",
                "j1",
                &json!({"title": "Mine"}),
                WriteOptions::default()
            ))
            .0,
            "Lease"
        );
        // ...but finishing the step, which refreshes j1 as otto, goes
        // through.
        invoke(
            &engine,
            &other(),
            "Step",
            "s1",
            "transition",
            json!({"to": "done"}),
        )
        .expect("transition");
        assert_eq!(
            get(&engine, "Step", "s1").behavior_field("Workflow", "status"),
            Some(&json!("done"))
        );
    }
}

#[test]
#[ignore = "needs Lease (12a)"]
fn a_stale_copy_costs_a_skipped_candidate_never_a_wrong_claim_and_max_candidates_caps_the_tries() {
    for target in targets("a_stale_copy_costs_a_skipped_candidate") {
        let engine = world(
            &target,
            World {
                queue: Some(with(claimable(), json!({"maxCandidates": 2}))),
                ..World::default()
            },
        );
        for (id, priority) in [("s1", 9), ("s2", 8), ("s3", 7), ("good", 1)] {
            create(&engine, id, json!({"priority": priority}));
        }
        // The three first are failed, but their copies are made to say
        // queued, as copies a change failed to reach would.
        for id in ["s1", "s2", "s3"] {
            job(&engine, &alice(), id, "transition", json!({"to": "failed"}));
            run_sql(
                &engine,
                "UPDATE engine_instances SET bhv_queue__status = 'queued' WHERE schema = 'Job' AND id = ?",
                &[SqlValue::from(id)],
            );
        }
        // Two tries, both stale: nothing claimed, nothing changed.
        assert_eq!(next(&engine, &worker(), json!({})), None);
        for id in ["s1", "s2"] {
            assert_eq!(
                [
                    status_of(&engine, id),
                    lease_of(&engine, id)
                        .get("holder")
                        .cloned()
                        .unwrap_or(Value::Null)
                ],
                [json!("failed"), Value::Null]
            );
        }
        publish(
            &engine,
            &jobs(&World {
                queue: Some(with(claimable(), json!({"maxCandidates": 4}))),
                ..World::default()
            }),
        );
        assert_eq!(next(&engine, &worker(), json!({})).as_deref(), Some("good"));
        assert_eq!(status_of(&engine, "good"), json!("running"));
    }
}

#[test]
#[ignore = "needs Lease (12a)"]
fn an_instance_at_max_expiries_is_no_candidate_until_its_expiries_are_reset() {
    for target in targets("an_instance_at_max_expiries") {
        let engine = world(
            &target,
            World {
                lease: Some(with(
                    requeue(),
                    json!({"maxExpiries": 1, "overridePermission": "jobs.override"}),
                )),
                ..World::default()
            },
        );
        create(&engine, "j1", json!({}));
        claim(&engine, &worker(), "j1", json!({})).expect("claim");
        engine.clock().advance(60000).expect("advance");
        job(&engine, &other(), "j1", "expire", json!({}));
        assert_eq!(
            [
                status_of(&engine, "j1"),
                lease_of(&engine, "j1")["expiries"].clone()
            ],
            [json!("queued"), json!(1)]
        );
        assert_eq!(next(&engine, &other(), json!({})), None);
        let reason = veto(claim(&engine, &other(), "j1", json!({}))).2;
        assert!(reason.contains("expired 1 times"), "{reason}");
        job(&engine, &operator(), "j1", "resetExpiries", json!({}));
        assert_eq!(next(&engine, &other(), json!({})).as_deref(), Some("j1"));
    }
}

#[test]
#[ignore = "needs Lease (12a)"]
fn the_runners_lease_sweep_puts_a_claim_whose_worker_went_quiet_back_in_the_queue() {
    for target in targets("the_runners_lease_sweep") {
        let engine = world(
            &target,
            World {
                lease: Some(with(requeue(), json!({"sweepMs": 2000}))),
                runner: Some(principal("runner", &[])),
                ..World::default()
            },
        );
        engine.runner().run_due().expect("run");
        create(&engine, "j1", json!({}));
        claim(&engine, &worker(), "j1", json!({})).expect("claim");
        assert_eq!(next(&engine, &other(), json!({})), None);
        engine.clock().advance(60000).expect("advance");
        assert_eq!(engine.runner().run_due().expect("run").scheduled, 1);
        assert_eq!(
            [
                status_of(&engine, "j1"),
                lease_of(&engine, "j1")
                    .get("holder")
                    .cloned()
                    .unwrap_or(Value::Null)
            ],
            [json!("queued"), Value::Null]
        );
        assert_eq!(next(&engine, &other(), json!({})).as_deref(), Some("j1"));
    }
}

// Queue: work over its budget is not tried.

fn pool_document() -> Value {
    json!({"kind": "General", "name": "Pool", "types": {"Pool": {
        "name": "Pool",
        "role": "EmbeddedStruct",
        "behaviors": [{"name": "Budget", "config": {"meters": {"cpu": {"limit": 20}}, "limitPermission": "pools.limit"}}],
        "fields": [{"name": "title", "typeRef": {"name": "string"}, "required": true}]
    }}})
}

// pooled opens an engine with Pool, whose cpu meter has a limit of 20,
// pools p1 and p2, and Job, whose claim reserves 10 cpu of the pool its
// pool link points at, with claimNext trying 2 candidates at most.
fn pooled(target: &Target) -> Engine {
    let engine = open_with(target, None, |_| {});
    publish(&engine, &pool_document());
    publish(
        &engine,
        &jobs_document(
            json!([
                {"name": "Workflow", "config": job_flow()},
                {"name": "Lease", "config": requeue()},
                {"name": "Links", "config": {"links": {"pool": {"schema": "Pool"}}}},
                {"name": "Queue", "config": with(claimable(), json!({"maxCandidates": 2}))},
                {"name": "Budget", "config": {"meters": {"cpu": {"scope": "pool", "reserve": 10}}}}
            ]),
            "Job",
        ),
    );
    for id in ["p1", "p2"] {
        create_in(&engine, "Pool", id, json!({}), None);
    }
    engine
}

fn pooled_job(engine: &Engine, id: &str, pool: &str, priority: i64) {
    create(engine, id, json!({"priority": priority}));
    job(
        engine,
        &alice(),
        id,
        "link",
        json!({"name": "pool", "id": pool}),
    );
}

fn excluded_until(engine: &Engine, id: &str) -> i64 {
    row(
        engine,
        "SELECT bhv_queue__excluded_until AS until FROM engine_instances WHERE schema = 'Job' AND id = ?",
        &[SqlValue::from(id)],
    )["until"]
        .as_i64()
        .expect("until")
}

#[test]
#[ignore = "needs Lease (12a)"]
fn more_over_budget_candidates_than_max_candidates_ahead_of_a_claimable_one_do_not_starve_it() {
    for target in targets("over_budget_candidates_do_not_starve") {
        let engine = pooled(&target);
        for (id, priority) in [("a1", 9), ("a2", 8), ("a3", 7), ("a4", 6), ("a5", 5)] {
            pooled_job(&engine, id, "p1", priority);
        }
        pooled_job(&engine, "b1", "p2", 1);
        assert_eq!(count(&engine, &worker(), json!({})), 6);
        // Two claims take p1's whole limit; the second's reservation leaves
        // a3, a4 and a5, more than maxCandidates, out of the candidates.
        assert_eq!(next(&engine, &worker(), json!({})).as_deref(), Some("a1"));
        let before = cursor_of(&engine);
        let second = claim_next(&engine, &worker(), json!({})).expect("claimNext");
        assert_eq!(second["id"], json!("a2"));
        assert_eq!(
            refreshes(&engine, before),
            pairs(&[("a3", "wren"), ("a4", "wren"), ("a5", "wren")])
        );
        assert_eq!(count(&engine, &worker(), json!({})), 1);
        // Copies that let them in, as before they kept the budget, make
        // claimNext try a3 and a4 alone, both refused, and claim nothing.
        let exclusion = |value: i64| {
            run_sql(
                &engine,
                "UPDATE engine_instances SET bhv_queue__excluded_until = ? WHERE schema = 'Job' AND id IN ('a3', 'a4', 'a5')",
                &[SqlValue::from(value)],
            );
        };
        exclusion(0);
        assert_eq!(next(&engine, &worker(), json!({})), None);
        exclusion(9_007_199_254_740_991);
        // So claimNext reaches b1 behind them.
        assert_eq!(next(&engine, &worker(), json!({})).as_deref(), Some("b1"));
        assert_eq!(next(&engine, &worker(), json!({})), None);

        // a2's release settles its reservation in p1, which lets a3, a4 and
        // a5 back in; a2's own copies follow its release.
        let released = cursor_of(&engine);
        let token = second["token"].as_i64().expect("a token");
        fenced(&engine, &worker(), "a2", "release", json!({}), token);
        assert_eq!(
            refreshes(&engine, released),
            pairs(&[("a3", "wren"), ("a4", "wren"), ("a5", "wren")])
        );
        assert_eq!(count(&engine, &worker(), json!({})), 4);
        assert_eq!(next(&engine, &worker(), json!({})).as_deref(), Some("a2"));
        assert_eq!(count(&engine, &worker(), json!({})), 0);
        // A change of p1 that moves no one's fit refreshes no one.
        let quiet = cursor_of(&engine);
        fenced(
            &engine,
            &worker(),
            "a1",
            "recordUsage",
            json!({"meter": "cpu", "amount": 1}),
            1,
        );
        assert!(refreshes(&engine, quiet).is_empty());
    }
}

#[test]
#[ignore = "needs Lease (12a)"]
fn a_scopes_new_limit_lets_its_waiting_work_in_refreshed_as_the_principal_that_set_it() {
    for target in targets("a_scopes_new_limit") {
        let engine = pooled(&target);
        for (id, priority) in [("a1", 9), ("a2", 8), ("a3", 7)] {
            pooled_job(&engine, id, "p1", priority);
        }
        invoke(
            &engine,
            &pam(),
            "Pool",
            "p1",
            "setLimit",
            json!({"meter": "cpu", "limit": 5}),
        )
        .expect("setLimit");
        assert_eq!(count(&engine, &worker(), json!({})), 0);
        assert_eq!(next(&engine, &worker(), json!({})), None);
        let cursor = cursor_of(&engine);
        invoke(
            &engine,
            &pam(),
            "Pool",
            "p1",
            "setLimit",
            json!({"meter": "cpu", "limit": 30}),
        )
        .expect("setLimit");
        assert_eq!(
            refreshes(&engine, cursor),
            pairs(&[("a1", "pam"), ("a2", "pam"), ("a3", "pam")])
        );
        let order: Vec<Option<String>> = (0..4)
            .map(|_| next(&engine, &worker(), json!({})))
            .collect();
        assert_eq!(
            order,
            [Some("a1"), Some("a2"), Some("a3"), None].map(|id| id.map(str::to_owned))
        );
    }
}

#[test]
#[ignore = "needs Lease (12a)"]
fn an_instance_a_create_links_to_a_scope_without_room_is_excluded_from_its_create() {
    for target in targets("excluded_from_its_create") {
        let engine = pooled(&target);
        invoke(
            &engine,
            &pam(),
            "Pool",
            "p1",
            "setLimit",
            json!({"meter": "cpu", "limit": 5}),
        )
        .expect("setLimit");
        create_in(
            &engine,
            "Job",
            "a1",
            json!({"priority": 1}),
            Some(json!({"Links": {"pool": "p1"}})),
        );
        create_in(
            &engine,
            "Job",
            "b1",
            json!({"priority": 1}),
            Some(json!({"Links": {"pool": "p2"}})),
        );
        assert_eq!(
            [excluded_until(&engine, "a1"), excluded_until(&engine, "b1")],
            [9_007_199_254_740_991, 0]
        );
        assert_eq!(count(&engine, &worker(), json!({})), 1);
        assert_eq!(
            [
                next(&engine, &worker(), json!({})),
                next(&engine, &worker(), json!({}))
            ],
            [Some("b1".to_owned()), None]
        );
        // p1's new limit refreshes a1, which heard p1 from its create.
        let cursor = cursor_of(&engine);
        invoke(
            &engine,
            &pam(),
            "Pool",
            "p1",
            "setLimit",
            json!({"meter": "cpu", "limit": 30}),
        )
        .expect("setLimit");
        assert_eq!(refreshes(&engine, cursor), pairs(&[("a1", "pam")]));
        assert_eq!(next(&engine, &worker(), json!({})).as_deref(), Some("a1"));
    }
}

#[test]
#[ignore = "needs Lease (12a)"]
fn an_instance_over_its_own_daily_limit_waits_for_the_next_day_which_lets_it_in_with_no_change() {
    for target in targets("over_its_own_daily_limit") {
        let engine = open_with(&target, None, |_| {});
        publish(
            &engine,
            &jobs_document(
                json!([
                    {"name": "Workflow", "config": job_flow()},
                    {"name": "Lease", "config": requeue()},
                    {"name": "Queue", "config": with(claimable(), json!({"maxCandidates": 1}))},
                    {"name": "Budget", "config": {"meters": {"cpu": {"limit": 25, "reserve": 10, "reset": "daily"}}}}
                ]),
                "Job",
            ),
        );
        create(&engine, "spent", json!({"priority": 9}));
        create(&engine, "fresh", json!({"priority": 1}));
        // 20 used leaves 5 of 25, less than a claim's 10.
        job(
            &engine,
            &alice(),
            "spent",
            "recordUsage",
            json!({"meter": "cpu", "amount": 20}),
        );
        assert_eq!(count(&engine, &worker(), json!({})), 1);
        assert_eq!(
            next(&engine, &worker(), json!({})).as_deref(),
            Some("fresh")
        );
        assert_eq!(next(&engine, &worker(), json!({})), None);
        let seq = get(&engine, "Job", "spent").seq;
        engine.clock().set(DAY).expect("set");
        assert_eq!(count(&engine, &worker(), json!({})), 1);
        assert_eq!(
            next(&engine, &worker(), json!({})).as_deref(),
            Some("spent")
        );
        assert_eq!(get(&engine, "Job", "spent").seq, seq + 1);
    }
}

// Queue: its config.

#[test]
#[ignore = "needs Lease (12a)"]
fn parse_config_holds_the_claim_to_the_types_workflow_and_the_fields_to_the_types() {
    for target in targets("parse_config_holds_the_claim") {
        let engine = open_with(&target, None, |_| {});
        let refused = |queue: Value| {
            refusal(define(
                &engine,
                &jobs(&World {
                    queue: Some(queue),
                    ..World::default()
                }),
            ))
            .message()
            .to_owned()
        };
        for (queue, expected) in [
            (
                json!({"claim": {"from": ["queued"], "to": "working"}}),
                "behavior Queue config: claim.to \"working\" is not a state of the type's Workflow (queued, running, done, failed)",
            ),
            (
                json!({"claim": {"from": ["waiting"], "to": "running"}}),
                "claim.from names \"waiting\", which is not a state of the type's Workflow",
            ),
            (
                json!({"claim": {"from": ["running"], "to": "running"}}),
                "claim.from names \"running\", the state a claim moves the instance to",
            ),
            (
                json!({"claim": {"from": ["done"], "to": "running"}}),
                "claim: no transition of the type's Workflow leads from \"done\" to \"running\"",
            ),
            (
                with(claimable(), json!({"priorityField": "title"})),
                "priorityField names \"title\", which is not an integer field of Job",
            ),
            (
                with(claimable(), json!({"priorityField": "rank"})),
                "priorityField names \"rank\", which is not a field of Job",
            ),
            (
                with(claimable(), json!({"match": ["owner"]})),
                "match names \"owner\", which is not a field of Job",
            ),
        ] {
            let message = refused(queue);
            assert!(message.contains(expected), "{message}\nwants: {expected}");
        }
    }
}

#[test]
fn queue_requires_lease_on_the_type() {
    for target in targets("queue_requires_lease") {
        let engine = open_with(&target, None, |_| {});
        let message = refusal(define(
            &engine,
            &jobs_document(
                json!([{"name": "Workflow", "config": job_flow()}, {"name": "Queue", "config": claimable()}]),
                "Job",
            ),
        ))
        .message()
        .to_owned();
        assert!(
            message
                .contains("behavior Queue requires behavior Lease, which the type does not list"),
            "{message}"
        );
    }
}

#[test]
#[ignore = "needs Lease (12a)"]
fn it_goes_on_a_schema_before_it_has_instances_and_is_not_removed_from_one_that_has_them() {
    // without is the Job schema with Workflow and Lease and no Queue.
    let without = || {
        jobs_document(
            json!([{"name": "Workflow", "config": job_flow()}, {"name": "Lease", "config": requeue()}]),
            "Job",
        )
    };
    for target in targets("it_goes_on_before_instances") {
        let engine = open_with(&target, None, |_| {});
        publish(&engine, &without());
        create(&engine, "j1", json!({"title": "Before"}));
        let added = refusal(define(&engine, &jobs(&World::default())));
        assert_eq!(added.code(), EngineErrorCode::IncompatibleChange);
        assert!(
            added
                .message()
                .contains("compose Queue before a schema has instances"),
            "{}",
            added.message()
        );
    }
    for target in targets("it_is_not_removed") {
        let fresh = open_with(&target, None, |_| {});
        publish(&fresh, &jobs(&World::default()));
        create(&fresh, "j1", json!({"title": "One"}));
        publish(
            &fresh,
            &jobs(&World {
                queue: Some(
                    json!({"claim": {"from": ["queued"], "to": "running"}, "maxCandidates": 5}),
                ),
                ..World::default()
            }),
        );
        let removed = refusal(define(&fresh, &without()));
        assert_eq!(removed.code(), EngineErrorCode::IncompatibleChange);
        assert!(
            removed.message().contains(
                "the claim facts and blocker references its instances hold would stay behind"
            ),
            "{}",
            removed.message()
        );
    }
}
