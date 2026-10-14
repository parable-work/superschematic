//! Queue's exclusion, and what keeps it current (D16): the cases of the
//! work queue's `test/queue-exclusion.test.ts` (a write to an enclosing
//! budget scope reaches only the waiting instances whose fit it can flip,
//! counted by the checks and hooks it runs; a daily scope whose
//! reservations settle lets its waiting work in at the next day; a scope
//! whose own scope link moves wakes what waits under it; a blocker is heard
//! only when its status moves; and excludeStale keeps work pinned to a
//! superseded revision or release out of claimNext and claim), on SQLite
//! and, with the variable set, on Postgres. Every Job composes Lease, so the
//! cases wait for it (milestone 12a), and the release's for Branches.

mod support;

use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::Arc;

use serde_json::{json, Value};

use superschematic_engine::behaviors::Hooks;
use superschematic_engine::json::js_equal;
use superschematic_engine::storage::{Dialect, SqlValue};
use superschematic_engine::{
    Behavior, BehaviorSet, Clock, ConfigTarget, CreateOptions, DefineOptions, Engine,
    EngineOptions, Principal, ReadEvents, Result, SchemaTarget, WriteOptions,
};
use superschematic_engine_workqueue::{Budget, BudgetConfig, Queue, QueueConfig};
use support::{alice, targets, veto, Target};

const T0: i64 = 1_000_000;
const DAY: i64 = 86_400_000;
const EXCLUDED: i64 = 9_007_199_254_740_991;

fn worker() -> Principal {
    Principal::new("wren", Vec::<String>::new())
}

fn pam() -> Principal {
    Principal::new("pam", ["pools.limit".to_owned()])
}

fn requeue() -> Value {
    json!({"onExpiry": {"transition": "queued", "from": ["running"]}})
}

fn claimable(extra: Value) -> Value {
    let mut out = json!({"claim": {"from": ["queued"], "to": "running"}, "priorityField": "priority", "match": ["topic", "urgent"]});
    if let (Some(out), Value::Object(extra)) = (out.as_object_mut(), extra) {
        out.extend(extra);
    }
    out
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

fn jobs_document(behaviors: Value) -> Value {
    json!({"kind": "General", "name": "Job", "types": {"Job": {
        "name": "Job",
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

// A pool or a run: a title, and the behaviors given.
fn scope_document(name: &str, behaviors: Value) -> Value {
    json!({"kind": "General", "name": name, "types": {name: {
        "name": name,
        "role": "EmbeddedStruct",
        "behaviors": behaviors,
        "fields": [{"name": "title", "typeRef": {"name": "string"}, "required": true}]
    }}})
}

static QUEUE: Queue = Queue;
static BUDGET: Budget = Budget;

/// Queue, its afterReferenceChange counted.
#[derive(Clone, Default)]
struct CountedQueue {
    hooks: Arc<AtomicUsize>,
}

fn queue_operation(name: &str) -> superschematic_engine::behaviors::OperationFn<Queue> {
    QUEUE
        .hooks()
        .operations
        .into_iter()
        .find(|(known, _)| *known == name)
        .map(|(_, operation)| operation)
        .expect("an operation")
}

fn queue_schema_operation(
    name: &str,
) -> superschematic_engine::behaviors::SchemaOperationFn<Queue> {
    QUEUE
        .hooks()
        .schema_operations
        .into_iter()
        .find(|(known, _)| *known == name)
        .map(|(_, operation)| operation)
        .expect("a schema operation")
}

impl Behavior for CountedQueue {
    type Config = QueueConfig;

    fn declaration(&self) -> &str {
        QUEUE.declaration()
    }

    fn parse_config(&self, raw: &Value, target: &ConfigTarget<'_>) -> Result<QueueConfig> {
        QUEUE.parse_config(raw, target)
    }

    fn hooks(&self) -> Hooks<Self> {
        let inner = QUEUE.hooks();
        Hooks {
            config_change: Some(|_, before, after, change| {
                (QUEUE.hooks().config_change.expect("configChange"))(&QUEUE, before, after, change)
            }),
            migrations: inner.migrations,
            guard: Some(|_, view, request| {
                (QUEUE.hooks().guard.expect("guard"))(&QUEUE, view, request)
            }),
            guidance: Some(|_, config, target| {
                (QUEUE.hooks().guidance.expect("guidance"))(&QUEUE, config, target)
            }),
            operations: vec![
                ("claim", |_, cx, params| {
                    queue_operation("claim")(&QUEUE, cx, params)
                }),
                ("refresh", |_, cx, params| {
                    queue_operation("refresh")(&QUEUE, cx, params)
                }),
            ],
            schema_operations: vec![
                ("claimNext", |_, cx, params| {
                    queue_schema_operation("claimNext")(&QUEUE, cx, params)
                }),
                ("countClaimable", |_, cx, params| {
                    queue_schema_operation("countClaimable")(&QUEUE, cx, params)
                }),
            ],
            after_change: Some(|_, cx, change| {
                (QUEUE.hooks().after_change.expect("afterChange"))(&QUEUE, cx, change)
            }),
            after_reference_change: Some(|counted, cx, reference, change| {
                counted.hooks.fetch_add(1, Ordering::SeqCst);
                (QUEUE
                    .hooks()
                    .after_reference_change
                    .expect("afterReferenceChange"))(&QUEUE, cx, reference, change)
            }),
            ..Hooks::default()
        }
    }
}

/// Budget, its checkReserve counted.
#[derive(Clone, Default)]
struct CountedBudget {
    checks: Arc<AtomicUsize>,
}

fn budget_operation(name: &str) -> superschematic_engine::behaviors::OperationFn<Budget> {
    BUDGET
        .hooks()
        .operations
        .into_iter()
        .find(|(known, _)| *known == name)
        .map(|(_, operation)| operation)
        .expect("an operation")
}

impl Behavior for CountedBudget {
    type Config = BudgetConfig;

    fn declaration(&self) -> &str {
        BUDGET.declaration()
    }

    fn parse_config(&self, raw: &Value, target: &ConfigTarget<'_>) -> Result<BudgetConfig> {
        BUDGET.parse_config(raw, target)
    }

    fn hooks(&self) -> Hooks<Self> {
        let inner = BUDGET.hooks();
        Hooks {
            config_change: Some(|_, before, after, change| {
                (BUDGET.hooks().config_change.expect("configChange"))(
                    &BUDGET, before, after, change,
                )
            }),
            migrations: inner.migrations,
            guard: Some(|_, view, request| {
                (BUDGET.hooks().guard.expect("guard"))(&BUDGET, view, request)
            }),
            guidance: Some(|_, config, target| {
                (BUDGET.hooks().guidance.expect("guidance"))(&BUDGET, config, target)
            }),
            operations: vec![
                ("reserve", |_, cx, params| {
                    budget_operation("reserve")(&BUDGET, cx, params)
                }),
                ("checkReserve", |counted, cx, params| {
                    counted.checks.fetch_add(1, Ordering::SeqCst);
                    budget_operation("checkReserve")(&BUDGET, cx, params)
                }),
                ("recordUsage", |_, cx, params| {
                    budget_operation("recordUsage")(&BUDGET, cx, params)
                }),
                ("settle", |_, cx, params| {
                    budget_operation("settle")(&BUDGET, cx, params)
                }),
                ("setLimit", |_, cx, params| {
                    budget_operation("setLimit")(&BUDGET, cx, params)
                }),
                ("reserveFor", |_, cx, params| {
                    budget_operation("reserveFor")(&BUDGET, cx, params)
                }),
                ("settleFor", |_, cx, params| {
                    budget_operation("settleFor")(&BUDGET, cx, params)
                }),
                ("recordUsageFor", |_, cx, params| {
                    budget_operation("recordUsageFor")(&BUDGET, cx, params)
                }),
            ],
            fields: vec![("meters", |_, view| {
                let field = BUDGET
                    .hooks()
                    .fields
                    .into_iter()
                    .find(|(name, _)| *name == "meters")
                    .map(|(_, field)| field)
                    .expect("the meters field");
                field(&BUDGET, view)
            })],
            after_change: Some(|_, cx, change| {
                (BUDGET.hooks().after_change.expect("afterChange"))(&BUDGET, cx, change)
            }),
            ..Hooks::default()
        }
    }
}

/// The work-queue behaviors, with Budget's checkReserve and Queue's
/// afterReferenceChange counted.
struct Counting {
    checks: Arc<AtomicUsize>,
    hooks: Arc<AtomicUsize>,
    behaviors: BehaviorSet,
}

impl Counting {
    fn calls(&self) -> [usize; 2] {
        [
            self.checks.load(Ordering::SeqCst),
            self.hooks.load(Ordering::SeqCst),
        ]
    }

    fn reset(&self) {
        self.checks.store(0, Ordering::SeqCst);
        self.hooks.store(0, Ordering::SeqCst);
    }
}

fn workqueue_behavior(name: &str) -> BehaviorSet {
    superschematic_engine_workqueue::behavior(name).expect("a ported behavior")
}

fn counting() -> Counting {
    let queue = CountedQueue::default();
    let budget = CountedBudget::default();
    let behaviors = superschematic_engine_behaviors::core()
        .extend(workqueue_behavior("Lease"))
        .extend(workqueue_behavior("Assignment"))
        .with(queue.clone())
        .extend(workqueue_behavior("Presence"))
        .extend(workqueue_behavior("Blueprint"))
        .with(budget.clone())
        .extend(workqueue_behavior("Retries"));
    Counting {
        checks: budget.checks,
        hooks: queue.hooks,
        behaviors,
    }
}

fn open(target: &Target, behaviors: Option<BehaviorSet>) -> Engine {
    let configure = |options: &mut EngineOptions| options.clock = Clock::Manual(T0);
    match behaviors {
        Some(behaviors) => target.open_core_with(|options| {
            options.behaviors = behaviors;
            configure(options);
        }),
        None => target.open_workqueue_with(configure),
    }
}

fn publish(engine: &Engine, document: &Value) {
    engine
        .schemas()
        .define(&alice(), document, DefineOptions::default())
        .expect("define");
    engine
        .schemas()
        .publish(
            &alice(),
            document["name"].as_str().expect("a name"),
            SchemaTarget::default(),
        )
        .expect("publish");
}

fn create_in(engine: &Engine, schema: &str, id: &str, data: Value, behaviors: Option<Value>) {
    let mut fields = json!({"title": id});
    if let (Some(fields), Value::Object(data)) = (fields.as_object_mut(), data) {
        fields.extend(data);
    }
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
        .expect("create");
}

fn create(engine: &Engine, id: &str, data: Value, behaviors: Option<Value>) {
    create_in(engine, "Job", id, data, behaviors);
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

fn fenced(engine: &Engine, id: &str, operation: &str, params: Value, token: i64) {
    let preconditions = json!({"Lease": {"token": token}});
    engine
        .instances()
        .invoke(
            &worker(),
            "Job",
            id,
            operation,
            &params,
            WriteOptions {
                preconditions: Some(&preconditions),
                ..Default::default()
            },
        )
        .expect(operation);
}

fn update(engine: &Engine, schema: &str, id: &str, patch: Value) {
    engine
        .instances()
        .update(&alice(), schema, id, &patch, WriteOptions::default())
        .expect("update");
}

fn claim_next(engine: &Engine) -> Value {
    engine
        .instances()
        .invoke_schema(&worker(), "Job", "claimNext", &json!({}), None)
        .expect("claimNext")["claimed"]
        .clone()
}

fn next_id(engine: &Engine) -> Option<String> {
    claim_next(engine)["id"].as_str().map(str::to_owned)
}

fn count(engine: &Engine) -> i64 {
    engine
        .instances()
        .invoke_schema(&worker(), "Job", "countClaimable", &json!({}), None)
        .expect("countClaimable")["count"]
        .as_i64()
        .expect("a count")
}

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

// refreshes lists the instances of Job refreshed after a cursor.
fn refreshes(engine: &Engine, after: i64) -> Vec<String> {
    engine
        .events()
        .read(
            &alice(),
            &ReadEvents {
                schema: Some("Job"),
                after: Some(superschematic_engine::EventsAfter::Cursor(after)),
                limit: Some(500),
                ..ReadEvents::default()
            },
        )
        .expect("events")
        .events
        .into_iter()
        .filter(|event| event.change.get("operation") == Some(&json!("refresh")))
        .map(|event| event.instance_id.unwrap_or_default())
        .collect()
}

fn sorted(mut list: Vec<String>) -> Vec<String> {
    list.sort();
    list
}

fn rows(engine: &Engine, sql: &str, params: &[SqlValue]) -> Vec<Value> {
    engine
        .db()
        .read(|tx| tx.all(sql, params))
        .expect("read")
        .iter()
        .map(|row| {
            let mut out = serde_json::Map::new();
            for (name, value) in row.iter() {
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
        })
        .collect()
}

// The order references were recorded in: SQLite's rowid, Postgres's
// position identity.
fn recorded(engine: &Engine) -> &'static str {
    match engine.dialect() {
        Dialect::Sqlite => "rowid",
        Dialect::Postgres => "position",
    }
}

fn excluded_until(engine: &Engine, id: &str) -> i64 {
    rows(
        engine,
        "SELECT bhv_queue__excluded_until AS until FROM engine_instances WHERE schema = 'Job' AND id = ?",
        &[SqlValue::from(id)],
    )[0]["until"]
        .as_i64()
        .expect("until")
}

fn assert_rows(found: Vec<Value>, expected: Value) {
    let expected = expected.as_array().cloned().unwrap_or_default();
    assert!(
        found.len() == expected.len() && found.iter().zip(&expected).all(|(a, b)| js_equal(a, b)),
        "{found:?} != {expected:?}"
    );
}

// Queue: a write to a scope reaches only what it can let in or keep out.

// pooled opens an engine whose Job draws cpu from the Pool its pool link
// points at: 10 a claim, or its timeLimitMs when it holds one; p1's limit
// is 20. Two jobs at the head of the order, and 20 that claim 10 and 20
// that claim 5 behind them.
fn pooled(target: &Target, behaviors: Option<BehaviorSet>) -> Engine {
    let engine = open(target, behaviors);
    publish(
        &engine,
        &scope_document(
            "Pool",
            json!([{"name": "Budget", "config": {"meters": {"cpu": {"limit": 20}}, "limitPermission": "pools.limit"}}]),
        ),
    );
    publish(
        &engine,
        &jobs_document(json!([
            {"name": "Workflow", "config": job_flow()},
            {"name": "Lease", "config": requeue()},
            {"name": "Links", "config": {"links": {"pool": {"schema": "Pool"}}}},
            {"name": "Queue", "config": claimable(json!({"maxCandidates": 2}))},
            {"name": "Budget", "config": {"meters": {"cpu": {"scope": "pool", "reserve": 10, "reserveField": "timeLimitMs"}}}}
        ])),
    );
    create_in(&engine, "Pool", "p1", json!({"title": "Shared"}), None);
    let pool = json!({"Links": {"pool": "p1"}});
    create(&engine, "a1", json!({"priority": 9}), Some(pool.clone()));
    create(&engine, "a2", json!({"priority": 9}), Some(pool.clone()));
    for at in 1..=20 {
        create(
            &engine,
            &format!("big{at}"),
            json!({"priority": 5}),
            Some(pool.clone()),
        );
        create(
            &engine,
            &format!("small{at}"),
            json!({"priority": 1, "timeLimitMs": 5}),
            Some(pool.clone()),
        );
    }
    engine
}

#[test]
#[ignore = "needs Lease, Assignment, Presence and Retries (12a)"]
fn a_write_that_crosses_no_amount_runs_no_check_and_no_hook_and_one_that_does_reaches_only_those_it_crosses(
) {
    for target in targets("a_write_that_crosses_no_amount") {
        let counting = counting();
        let engine = pooled(&target, Some(counting.behaviors.clone()));
        assert_eq!(next_id(&engine).as_deref(), Some("a1"));
        let second = claim_next(&engine);
        assert_eq!(second["id"], json!("a2"));
        // p1's 20 are reserved: every one of the 40 waits.
        assert_eq!(count(&engine), 0);

        let smalls: Vec<String> = (1..=20).map(|at| format!("small{at}")).collect();
        let bigs: Vec<String> = (1..=20).map(|at| format!("big{at}")).collect();
        // Usage inside a1's reservation moves p1's used and reserved, not
        // its remaining: no waiting instance is asked anything.
        counting.reset();
        let from = cursor_of(&engine);
        fenced(
            &engine,
            "a1",
            "recordUsage",
            json!({"meter": "cpu", "amount": 4}),
            1,
        );
        assert_eq!(counting.calls(), [0, 0]);
        assert!(refreshes(&engine, from).is_empty());
        // Usage past it takes the remaining from 0 to -6, and a2's release to
        // 4: below 5, so across no amount of the others. a2, back in the
        // queue, checks itself in its own write and waits too; its own
        // reference hears its own settlement, which its write settles.
        fenced(
            &engine,
            "a1",
            "recordUsage",
            json!({"meter": "cpu", "amount": 12}),
            1,
        );
        let token = second["token"].as_i64().expect("a token");
        fenced(&engine, "a2", "release", json!({}), token);
        assert_eq!(counting.calls(), [1, 1]);
        assert!(refreshes(&engine, from).is_empty());
        assert_eq!(count(&engine), 0);

        // A limit of 25 leaves 9: across 5, not 10, so the 20 that claim 5
        // hear it and the rest do not. Each that hears it checks once to see
        // its fit moved, and once more in the refresh that lets it in.
        counting.reset();
        let from = cursor_of(&engine);
        invoke(
            &engine,
            &pam(),
            "Pool",
            "p1",
            "setLimit",
            json!({"meter": "cpu", "limit": 25}),
        )
        .expect("setLimit");
        assert_eq!(counting.calls(), [40, 20]);
        assert_eq!(sorted(refreshes(&engine, from)), sorted(smalls.clone()));
        assert_eq!(count(&engine), 20);
        // 30 leaves 14: across 10, so the 20 that claim 10 and a2 come in.
        counting.reset();
        let from = cursor_of(&engine);
        invoke(
            &engine,
            &pam(),
            "Pool",
            "p1",
            "setLimit",
            json!({"meter": "cpu", "limit": 30}),
        )
        .expect("setLimit");
        assert_eq!(counting.calls(), [42, 21]);
        let mut expected = bigs.clone();
        expected.push("a2".to_owned());
        assert_eq!(sorted(refreshes(&engine, from)), sorted(expected));
        assert_eq!(count(&engine), 41);
        // With every one of the 41 waiting on it, a write that crosses no
        // amount still asks none of them.
        counting.reset();
        let from = cursor_of(&engine);
        fenced(
            &engine,
            "a1",
            "recordUsage",
            json!({"meter": "cpu", "amount": 1}),
            1,
        );
        assert_eq!(counting.calls(), [0, 0]);
        assert!(refreshes(&engine, from).is_empty());
    }
}

#[test]
#[ignore = "needs Lease (12a)"]
fn each_waiting_instance_hears_the_values_of_its_scopes_the_answer_turns_on_at_its_own_amount() {
    for target in targets("each_waiting_instance_hears") {
        let engine = pooled(&target, None);
        let heard = |id: &str| {
            rows(
                &engine,
                &format!(
                    "SELECT target_id, key, hears, crosses FROM engine_references WHERE source_id = ? AND behavior = 'Queue' ORDER BY {}",
                    recorded(&engine)
                ),
                &[SqlValue::from(id)],
            )
        };
        assert_rows(
            heard("big1"),
            json!([{"target_id": "p1", "key": "budget /behaviors/Budget/meters/cpu/remaining 10", "hears": "/behaviors/Budget/meters/cpu/remaining", "crosses": 10}]),
        );
        assert_rows(
            heard("small1"),
            json!([{"target_id": "p1", "key": "budget /behaviors/Budget/meters/cpu/remaining 5", "hears": "/behaviors/Budget/meters/cpu/remaining", "crosses": 5}]),
        );
        // A claimed instance is no candidate, and hears nothing.
        claim_next(&engine);
        assert!(heard("a1").is_empty());
    }
}

// Queue: a daily scope and a scope's own scope.

#[test]
#[ignore = "needs Lease (12a)"]
fn a_daily_scopes_settled_reservations_let_its_waiting_work_in_at_the_next_day() {
    for target in targets("a_daily_scopes_settled_reservations") {
        let engine = open(&target, None);
        publish(
            &engine,
            &scope_document(
                "Pool",
                json!([{"name": "Budget", "config": {"meters": {"cpu": {"limit": 30, "reset": "daily"}}}}]),
            ),
        );
        publish(
            &engine,
            &jobs_document(json!([
                {"name": "Workflow", "config": job_flow()},
                {"name": "Lease", "config": requeue()},
                {"name": "Links", "config": {"links": {"pool": {"schema": "Pool"}}}},
                {"name": "Queue", "config": claimable(json!({"maxCandidates": 1}))},
                {"name": "Budget", "config": {"meters": {"cpu": {"scope": "pool", "reserve": 10}}}}
            ])),
        );
        create_in(&engine, "Pool", "p1", json!({"title": "Shared"}), None);
        for (id, priority) in [("a1", 9), ("a2", 8), ("a3", 7), ("w", 1)] {
            create(
                &engine,
                id,
                json!({"priority": priority}),
                Some(json!({"Links": {"pool": "p1"}})),
            );
        }
        let tokens: Vec<Value> = (0..3)
            .map(|_| claim_next(&engine)["token"].clone())
            .collect();
        assert_eq!(tokens, [json!(1), json!(1), json!(1)]);
        // 30 reserved: w fits neither today nor tomorrow, so only a change
        // lets it in.
        assert_eq!(excluded_until(&engine, "w"), EXCLUDED);
        // a1 uses 15: the pool has used 15 and holds 20. w still does not fit
        // today, but 20 held and its 10 fit a fresh day: it waits for the
        // day.
        let from = cursor_of(&engine);
        fenced(
            &engine,
            "a1",
            "recordUsage",
            json!({"meter": "cpu", "amount": 15}),
            1,
        );
        assert_eq!(refreshes(&engine, from), ["w"]);
        assert_eq!(excluded_until(&engine, "w"), DAY);
        assert_eq!(claim_next(&engine), Value::Null);
        engine.clock().set(DAY).expect("set");
        assert_eq!(next_id(&engine).as_deref(), Some("w"));
    }
}

#[test]
#[ignore = "needs Lease (12a)"]
fn a_scope_whose_own_scope_link_moves_wakes_what_waits_under_it() {
    for target in targets("a_scope_whose_own_scope_link_moves") {
        let engine = open(&target, None);
        publish(
            &engine,
            &scope_document(
                "Pool",
                json!([{"name": "Budget", "config": {"meters": {"cpu": {"limit": 5}}, "limitPermission": "pools.limit"}}]),
            ),
        );
        publish(
            &engine,
            &scope_document(
                "Run",
                json!([
                    {"name": "Links", "config": {"links": {"pool": {"schema": "Pool"}}}},
                    {"name": "Budget", "config": {"meters": {"cpu": {"scope": "pool"}}}}
                ]),
            ),
        );
        publish(
            &engine,
            &jobs_document(json!([
                {"name": "Workflow", "config": job_flow()},
                {"name": "Lease", "config": requeue()},
                {"name": "Links", "config": {"links": {"run": {"schema": "Run"}}}},
                {"name": "Queue", "config": claimable(json!({}))},
                {"name": "Budget", "config": {"meters": {"cpu": {"scope": "run", "reserve": 10}}}}
            ])),
        );
        create_in(&engine, "Pool", "p1", json!({"title": "Small"}), None);
        create_in(&engine, "Pool", "p2", json!({"title": "Large"}), None);
        invoke(
            &engine,
            &pam(),
            "Pool",
            "p2",
            "setLimit",
            json!({"meter": "cpu", "limit": 100}),
        )
        .expect("setLimit");
        create_in(
            &engine,
            "Run",
            "r1",
            json!({"title": "Nightly"}),
            Some(json!({"Links": {"pool": "p1"}})),
        );
        create(
            &engine,
            "j1",
            json!({}),
            Some(json!({"Links": {"run": "r1"}})),
        );
        assert_eq!(count(&engine), 0);
        // r1 has no limit: its remaining never moves, but its pool link does.
        let from = cursor_of(&engine);
        invoke(
            &engine,
            &alice(),
            "Run",
            "r1",
            "link",
            json!({"name": "pool", "id": "p2"}),
        )
        .expect("link");
        assert_eq!(refreshes(&engine, from), ["j1"]);
        assert_eq!(next_id(&engine).as_deref(), Some("j1"));
    }
}

// Queue: a blocker is heard when its status moves.

#[test]
#[ignore = "needs Lease (12a)"]
fn a_blockers_change_that_leaves_its_status_refreshes_no_dependent_and_one_that_moves_it_does() {
    for target in targets("a_blockers_change") {
        let engine = open(&target, None);
        publish(
            &engine,
            &scope_document(
                "Step",
                json!([{"name": "Workflow", "config": {"states": ["todo", "done"], "transitions": [{"from": "todo", "to": "done"}]}}]),
            ),
        );
        publish(
            &engine,
            &jobs_document(json!([
                {"name": "Workflow", "config": job_flow()},
                {"name": "Lease", "config": requeue()},
                {"name": "Queue", "config": claimable(json!({}))},
                {"name": "Dependencies", "config": {"schemas": ["Job", "Step"]}}
            ])),
        );
        create_in(&engine, "Step", "s1", json!({"title": "Approve"}), None);
        for id in ["j1", "j2"] {
            create(
                &engine,
                id,
                json!({}),
                Some(json!({"Dependencies": {"blockers": [{"schema": "Step", "id": "s1"}]}})),
            );
        }
        let from = cursor_of(&engine);
        update(&engine, "Step", "s1", json!({"title": "Approve the plan"}));
        assert!(refreshes(&engine, from).is_empty());
        let from = cursor_of(&engine);
        invoke(
            &engine,
            &alice(),
            "Step",
            "s1",
            "transition",
            json!({"to": "done"}),
        )
        .expect("transition");
        assert_eq!(refreshes(&engine, from), ["j1", "j2"]);
        assert_eq!(count(&engine), 2);
    }
}

// Queue: excludeStale.

// specced opens an engine whose Job pins its spec link to a Spec's revision
// and is kept out while the spec has moved past it.
fn specced(target: &Target, extra: Vec<Value>, behaviors: Option<BehaviorSet>) -> Engine {
    let engine = open(target, behaviors);
    publish(
        &engine,
        &scope_document("Spec", json!([{"name": "Revisions"}])),
    );
    let mut list = vec![
        json!({"name": "Workflow", "config": job_flow()}),
        json!({"name": "Lease", "config": requeue()}),
        json!({"name": "Links", "config": {"links": {"spec": {"schema": "Spec", "pinned": true}, "design": {"schema": "Spec"}}}}),
        json!({"name": "Queue", "config": claimable(json!({"excludeStale": ["spec"]}))}),
    ];
    list.extend(extra);
    publish(&engine, &jobs_document(Value::Array(list)));
    create_in(&engine, "Spec", "sp1", json!({"title": "v1"}), None);
    engine
}

#[test]
#[ignore = "needs Lease (12a)"]
fn an_instance_whose_pinned_link_the_target_has_moved_past_is_no_candidate_until_it_is_linked_again(
) {
    for target in targets("an_instance_whose_pinned_link_moved") {
        let engine = specced(&target, Vec::new(), None);
        create(
            &engine,
            "j1",
            json!({"priority": 9}),
            Some(json!({"Links": {"spec": "sp1"}})),
        );
        create(&engine, "j2", json!({"priority": 1}), None);
        assert_eq!(count(&engine), 2);
        // The spec's next revision crosses the one j1 pins past it: j1 is
        // refreshed out, as the writer.
        let from = cursor_of(&engine);
        update(&engine, "Spec", "sp1", json!({"title": "v2"}));
        assert_eq!(refreshes(&engine, from), ["j1"]);
        assert_eq!(excluded_until(&engine, "j1"), EXCLUDED);
        assert_eq!(count(&engine), 1);
        let refused = veto(invoke(&engine, &worker(), "Job", "j1", "claim", json!({})));
        assert_eq!(
            (
                refused.0.as_str(),
                refused.2.as_str(),
                refused.3.as_deref(),
                refused.4
            ),
            (
                "Queue",
                "its link spec is pinned to a revision its target has moved past",
                Some("stale_link"),
                Some(json!({"links": ["spec"]}))
            )
        );
        // Stale, it hears the spec no more: another revision refreshes
        // nothing.
        let from = cursor_of(&engine);
        update(&engine, "Spec", "sp1", json!({"title": "v3"}));
        assert!(refreshes(&engine, from).is_empty());
        // Pinned again, to revision 3, it is back, and hears the next one.
        invoke(
            &engine,
            &alice(),
            "Job",
            "j1",
            "link",
            json!({"name": "spec", "id": "sp1"}),
        )
        .expect("link");
        assert_rows(
            rows(&engine, "SELECT key, hears, crosses FROM engine_references WHERE source_id = 'j1' AND behavior = 'Queue'", &[]),
            json!([{"key": "stale spec", "hears": "/behaviors/Revisions/revision", "crosses": 4}]),
        );
        assert_eq!(next_id(&engine).as_deref(), Some("j1"));
    }
}

#[test]
#[ignore = "needs Lease (12a)"]
fn the_delete_of_a_pinned_links_target_lets_go_of_it_whichever_reference_hears_the_delete_first() {
    for target in targets("the_delete_of_a_pinned_links_target") {
        let engine = specced(&target, Vec::new(), None);
        create(
            &engine,
            "j1",
            json!({}),
            Some(json!({"Links": {"spec": "sp1"}})),
        );
        // Linked again to the same revision: Links records its reference
        // anew, after Queue's, so Queue's hears the delete first.
        invoke(
            &engine,
            &alice(),
            "Job",
            "j1",
            "link",
            json!({"name": "spec", "id": "sp1"}),
        )
        .expect("link");
        let behaviors: Vec<Value> = rows(
            &engine,
            &format!(
                "SELECT behavior FROM engine_references WHERE source_id = 'j1' ORDER BY {}",
                recorded(&engine)
            ),
            &[],
        )
        .into_iter()
        .map(|row| row["behavior"].clone())
        .collect();
        assert_eq!(behaviors, [json!("Queue"), json!("Links")]);
        assert!(engine
            .instances()
            .delete(&alice(), "Spec", "sp1", WriteOptions::default())
            .expect("delete"));
        let found = engine
            .instances()
            .get(&alice(), "Job", "j1", Default::default())
            .expect("get")
            .expect("j1");
        assert_eq!(found.behaviors.get("Links"), Some(&json!({})));
        assert!(rows(
            &engine,
            "SELECT key FROM engine_references WHERE source_id = 'j1'",
            &[]
        )
        .is_empty());
        assert_eq!(next_id(&engine).as_deref(), Some("j1"));
    }
}

#[test]
#[ignore = "needs Lease (12a)"]
fn a_pin_to_an_earlier_revision_is_stale_from_its_create_and_an_unpinned_link_is_never_stale() {
    for target in targets("a_pin_to_an_earlier_revision") {
        let engine = specced(&target, Vec::new(), None);
        update(&engine, "Spec", "sp1", json!({"title": "v2"}));
        create(
            &engine,
            "old",
            json!({"priority": 9}),
            Some(json!({"Links": {"spec": {"id": "sp1", "revision": 1}}})),
        );
        create(
            &engine,
            "loose",
            json!({"priority": 5}),
            Some(json!({"Links": {"design": "sp1"}})),
        );
        assert_eq!(excluded_until(&engine, "old"), EXCLUDED);
        assert_eq!(next_id(&engine).as_deref(), Some("loose"));
        assert_eq!(claim_next(&engine), Value::Null);
    }
}

#[test]
#[ignore = "needs Lease, Assignment, Presence and Retries (12a)"]
fn with_budget_beside_it_a_stale_link_keeps_the_instance_out_without_a_budget_check() {
    for target in targets("a_stale_link_without_a_budget_check") {
        let counting = counting();
        let engine = specced(
            &target,
            vec![
                json!({"name": "Budget", "config": {"meters": {"cpu": {"limit": 100, "reserve": 10}}}}),
            ],
            Some(counting.behaviors.clone()),
        );
        create(
            &engine,
            "j1",
            json!({}),
            Some(json!({"Links": {"spec": "sp1"}})),
        );
        assert_eq!(counting.calls()[0], 1);
        update(&engine, "Spec", "sp1", json!({"title": "v2"}));
        assert_eq!(counting.calls(), [1, 1]);
        assert_eq!(excluded_until(&engine, "j1"), EXCLUDED);
        assert!(rows(
            &engine,
            "SELECT key FROM engine_references WHERE source_id = 'j1' AND behavior = 'Queue'",
            &[]
        )
        .is_empty());
    }
}

#[test]
#[ignore = "needs Lease (12a)"]
fn parse_config_holds_exclude_stale_to_pinned_links_of_the_type_that_hold_one_target() {
    for target in targets("parse_config_holds_exclude_stale") {
        let engine = open(&target, None);
        publish(
            &engine,
            &scope_document("Spec", json!([{"name": "Revisions"}])),
        );
        let document = |behaviors: Vec<Value>| {
            let mut list = vec![
                json!({"name": "Workflow", "config": job_flow()}),
                json!({"name": "Lease", "config": requeue()}),
            ];
            list.extend(behaviors);
            jobs_document(Value::Array(list))
        };
        let refused = |behaviors: Vec<Value>| {
            support::refusal(engine.schemas().define(
                &alice(),
                document(behaviors),
                DefineOptions::default(),
            ))
            .message()
            .to_owned()
        };
        let queue_on = |names: &[&str]| json!({"name": "Queue", "config": claimable(json!({"excludeStale": names}))});
        let linked = json!({"name": "Links", "config": {"links": {"spec": {"schema": "Spec", "pinned": true}, "design": {"schema": "Spec"}}}});
        for (behaviors, expected) in [
            (vec![queue_on(&["spec"])], "behavior Queue config: excludeStale names links of Links, which the type does not list"),
            (
                vec![linked.clone(), queue_on(&["plan"])],
                "excludeStale names \"plan\", which is not a link of the type's Links config (its links: spec, design)",
            ),
            (
                vec![linked.clone(), queue_on(&["design"])],
                "excludeStale names \"design\", a link that is not pinned, so it is never stale",
            ),
            (
                vec![
                    json!({"name": "Links", "config": {"links": {"specs": {"schema": "Spec", "pinned": true, "many": true, "max": 3}}}}),
                    queue_on(&["specs"]),
                ],
                "excludeStale names \"specs\", a link that holds a list of targets; it reads links that hold one",
            ),
        ] {
            let message = refused(behaviors);
            assert!(message.contains(expected), "{message}\nwants: {expected}");
        }
        engine
            .schemas()
            .define(
                &alice(),
                document(vec![linked, queue_on(&["spec"])]),
                DefineOptions::default(),
            )
            .expect("define");
        let released = json!({"name": "Links", "config": {"links": {"spec": {"schema": "Spec", "pinned": "release"}}}});
        engine
            .schemas()
            .define(
                &alice(),
                document(vec![released, queue_on(&["spec"])]),
                DefineOptions::default(),
            )
            .expect("define");
    }
}

/// A schema Recipe whose instances are version graphs of steps (Branches).
fn recipes_document() -> Value {
    json!({
        "kind": "General",
        "name": "Recipe",
        "types": {
            "Recipe": {
                "name": "Recipe",
                "role": "EmbeddedStruct",
                "behaviors": [{"name": "Branches", "config": {"kinds": {"step": {"type": "Step", "order": "position"}}}}],
                "fields": [{"name": "title", "typeRef": {"name": "string"}, "required": true}]
            },
            "Step": {
                "name": "Step",
                "role": "EmbeddedStruct",
                "fields": [
                    {"name": "instruction", "typeRef": {"name": "string"}, "required": true},
                    {"name": "position", "typeRef": {"name": "Int"}, "required": true}
                ]
            }
        }
    })
}

// release_recipe releases a new tagged commit of a recipe as alice: a step
// saved on a draft, committed and merged into its primary line. It returns
// the recipe's Branches release field, the new release's number.
fn release_recipe(engine: &Engine, id: &str) -> i64 {
    let call = |operation: &str, params: Value| {
        invoke(engine, &alice(), "Recipe", id, operation, params).expect(operation)
    };
    let version = engine
        .instances()
        .get(&alice(), "Recipe", id, Default::default())
        .expect("get")
        .and_then(|record| {
            record
                .behavior_field("Branches", "release")
                .and_then(Value::as_i64)
        })
        .unwrap_or(0);
    let draft = call("branch", json!({"name": format!("release{}", version + 1)}));
    let saved = call(
        "save",
        json!({
            "ref": draft["id"],
            "version": draft["version"],
            "edits": {"step": {"upsert": [{"instruction": format!("Step {}", version + 1), "position": version + 1}]}}
        }),
    );
    let committed = call(
        "commit",
        json!({"ref": draft["id"], "version": saved["ref"]["version"]}),
    );
    let refs = call("refs", json!({}));
    let main = &refs["items"][0];
    let merged = call(
        "merge",
        json!({"source": committed["ref"]["id"], "target": main["id"], "targetVersion": main["version"], "tag": true}),
    );
    call(
        "releaseCommit",
        json!({"commit": merged["commit"]["id"], "version": version}),
    )["version"]
        .as_i64()
        .expect("a release")
}

#[test]
#[ignore = "needs Lease (12a) and Branches (PR #377)"]
fn a_link_pinned_to_a_release_keeps_its_instance_out_once_the_target_is_released_again() {
    for target in targets("a_link_pinned_to_a_release") {
        let engine = open(&target, None);
        publish(&engine, &recipes_document());
        publish(
            &engine,
            &jobs_document(json!([
                {"name": "Workflow", "config": job_flow()},
                {"name": "Lease", "config": requeue()},
                {"name": "Links", "config": {"links": {"recipe": {"schema": "Recipe", "pinned": "release"}}}},
                {"name": "Queue", "config": claimable(json!({"excludeStale": ["recipe"]}))}
            ])),
        );
        create_in(&engine, "Recipe", "soup", json!({"title": "Soup"}), None);
        release_recipe(&engine, "soup");
        create(
            &engine,
            "j1",
            json!({"priority": 9}),
            Some(json!({"Links": {"recipe": "soup"}})),
        );
        create(&engine, "j2", json!({"priority": 1}), None);
        assert_rows(
            rows(&engine, "SELECT key, hears, crosses FROM engine_references WHERE source_id = 'j1' AND behavior = 'Queue'", &[]),
            json!([{"key": "stale recipe", "hears": "/behaviors/Branches/release", "crosses": 2}]),
        );
        // The recipe's next release crosses the one j1 pins past it: j1 is
        // refreshed out.
        let from = cursor_of(&engine);
        assert_eq!(release_recipe(&engine, "soup"), 2);
        assert_eq!(refreshes(&engine, from), ["j1"]);
        assert_eq!(excluded_until(&engine, "j1"), EXCLUDED);
        let refused = veto(invoke(&engine, &worker(), "Job", "j1", "claim", json!({})));
        assert_eq!(
            (refused.2.as_str(), refused.3.as_deref(), refused.4),
            (
                "its link recipe is pinned to a release its target has moved past",
                Some("stale_link"),
                Some(json!({"links": ["recipe"]}))
            )
        );
        assert_eq!(next_id(&engine).as_deref(), Some("j2"));
        // Pinned again, to release 2, it is back.
        invoke(
            &engine,
            &alice(),
            "Job",
            "j1",
            "link",
            json!({"name": "recipe", "id": "soup"}),
        )
        .expect("link");
        assert_eq!(next_id(&engine).as_deref(), Some("j1"));
    }
}
