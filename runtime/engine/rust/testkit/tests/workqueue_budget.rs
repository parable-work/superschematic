//! Budget, reserve-then-settle budgets (D16): the cases of the work queue's
//! `test/budget.test.ts` (reservations held against a meter's limit, here
//! and up a chain of enclosing scopes that Links links point at; usage that
//! is never refused, draws the instance's own reservation down and releases
//! exactly that part at every scope; settlement when the lease a
//! reservation was made under ends and when the instance is deleted;
//! overruns reported, directed once per lease and escalated by each level's
//! own config; limits only limitPermission changes; daily meters that a
//! read never writes; scope operations that cannot free what an instance
//! still holds; checkReserve, the read-only answer Queue copies; and the
//! config rules), on SQLite and, with the variable set, on Postgres. The
//! guidance is held to the TypeScript
//! text in the work-queue crate's `tests/guidance_budget.rs`.

mod support;

use serde_json::{json, Value};

use superschematic_engine::{
    Clock, CreateOptions, DefineOptions, Engine, EngineErrorCode, EngineEvent, Principal,
    ReadEvents, Result, RunnerOptions, SchemaTarget, WriteOptions,
};
use support::{alice, count, issue, issues_of, refusal, targets, veto, Target};

const T0: i64 = 1_000_000;
const DAY: i64 = 86_400_000;

fn principal(subject: &str, permissions: &[&str]) -> Principal {
    Principal::new(subject, permissions.iter().map(|one| (*one).to_owned()))
}

// No principal holds a permission that sends Lease's directives: Budget's
// overrun directive goes through call(), which Lease's guard lets through.
fn worker() -> Principal {
    principal("wren", &[])
}

fn other() -> Principal {
    principal("otto", &[])
}

fn operator() -> Principal {
    principal("opal", &["budget.limit"])
}

fn lead() -> Principal {
    principal("lena", &["budget.limit"])
}

fn sweeper() -> Principal {
    principal("sweeper", &[])
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

fn lease() -> Value {
    json!({"name": "Lease", "config": {"onExpiry": {"transition": "queued", "from": ["running"]}}})
}

fn workflow() -> Value {
    json!({"name": "Workflow", "config": job_flow()})
}

// budget_document is a General schema whose instance type, named like the
// schema, has a title and two integer fields, cap and estimate, a meter's
// limitField and reserveField, and composes the behaviors given.
fn budget_document(name: &str, behaviors: Value) -> Value {
    json!({
        "kind": "General",
        "name": name,
        "types": {
            name: {
                "name": name,
                "role": "EmbeddedStruct",
                "behaviors": behaviors,
                "fields": [
                    {"name": "title", "typeRef": {"name": "string"}, "required": true},
                    {"name": "cap", "typeRef": {"name": "Generic.Int64"}},
                    {"name": "estimate", "typeRef": {"name": "Generic.Int64"}}
                ]
            }
        }
    })
}

fn meter(used: i64, reserved: i64, limit: Option<i64>) -> Value {
    json!({
        "used": used,
        "reserved": reserved,
        "limit": limit,
        "remaining": limit.map(|limit| limit - used - reserved),
    })
}

fn open(target: &Target) -> Engine {
    target.open_workqueue_with(|options| options.clock = Clock::Manual(T0))
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

fn create(engine: &Engine, schema: &str, id: &str, data: Value) {
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
                ..Default::default()
            },
        )
        .expect("create");
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

// fenced invokes with a lease's token as Lease's precondition.
fn fenced(
    engine: &Engine,
    who: &Principal,
    id: &str,
    operation: &str,
    token: i64,
) -> Result<Value> {
    let preconditions = json!({"Lease": {"token": token}});
    engine.instances().invoke(
        who,
        "Step",
        id,
        operation,
        &json!({}),
        WriteOptions {
            preconditions: Some(&preconditions),
            ..Default::default()
        },
    )
}

fn step(engine: &Engine, who: &Principal, id: &str, operation: &str, params: Value) -> Value {
    invoke(engine, who, "Step", id, operation, params).expect(operation)
}

fn record(engine: &Engine, schema: &str, id: &str) -> Value {
    let found = engine
        .instances()
        .get(&alice(), schema, id, Default::default())
        .expect("get")
        .expect("the instance");
    json!({"data": found.data, "behaviors": found.behaviors, "seq": found.seq})
}

fn meter_of(engine: &Engine, schema: &str, id: &str, name: &str) -> Value {
    record(engine, schema, id)["behaviors"]["Budget"]["meters"][name].clone()
}

fn cpu(engine: &Engine, schema: &str, id: &str) -> Value {
    meter_of(engine, schema, id, "cpu")
}

fn seq_of(engine: &Engine, schema: &str, id: &str) -> i64 {
    record(engine, schema, id)["seq"].as_i64().expect("a seq")
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

fn status_of(engine: &Engine, schema: &str, id: &str) -> Value {
    record(engine, schema, id)["behaviors"]["Workflow"]["status"].clone()
}

// single opens an engine whose Step composes Workflow, Lease and Budget
// with the config given (or the behaviors given before Budget), with step
// s1 queued.
fn single(target: &Target, config: Value, behaviors: Option<Value>) -> Engine {
    let engine = open(target);
    let mut list = behaviors
        .and_then(|given| given.as_array().cloned())
        .unwrap_or_else(|| vec![workflow(), lease()]);
    list.push(json!({"name": "Budget", "config": config}));
    publish(&engine, &budget_document("Step", Value::Array(list)));
    create(&engine, "Step", "s1", json!({"title": "Build"}));
    engine
}

// The meters of a chain: a pool's, a run's and a step's cpu meter, and the
// rest of a step's Budget config.
#[derive(Default)]
struct Chain {
    pool: Option<Value>,
    run: Option<Value>,
    step: Option<Value>,
    step_config: Option<Value>,
}

fn merged(base: Value, extra: Option<Value>) -> Value {
    let mut out = base;
    if let (Some(out), Some(Value::Object(extra))) = (out.as_object_mut(), extra) {
        out.extend(extra);
    }
    out
}

// chain opens an engine with three levels: Pool p1, Run r1 whose pool link
// points at it, and Steps s1 and s2 whose run link points at r1, each with
// a cpu meter of the config given; a run's and a step's meter draw on
// their link.
fn chain(target: &Target, meters: Chain) -> Engine {
    let engine = open(target);
    publish(
        &engine,
        &budget_document(
            "Pool",
            json!([{"name": "Budget", "config": {
                "meters": {"cpu": meters.pool.unwrap_or_else(|| json!({"limit": 100}))},
                "limitPermission": "budget.limit"
            }}]),
        ),
    );
    publish(
        &engine,
        &budget_document(
            "Run",
            json!([
                {"name": "Links", "config": {"links": {"pool": {"schema": "Pool"}}}},
                {"name": "Budget", "config": {"meters": {"cpu": merged(json!({"scope": "pool"}), meters.run)}}}
            ]),
        ),
    );
    let step_budget = merged(
        json!({"meters": {"cpu": merged(json!({"scope": "run"}), Some(meters.step.unwrap_or_else(|| json!({"reserve": 60}))))}}),
        meters.step_config,
    );
    publish(
        &engine,
        &budget_document(
            "Step",
            json!([
                workflow(),
                lease(),
                {"name": "Links", "config": {"links": {"run": {"schema": "Run"}}}},
                {"name": "Budget", "config": step_budget}
            ]),
        ),
    );
    create(&engine, "Pool", "p1", json!({"title": "Shared"}));
    create(&engine, "Run", "r1", json!({"title": "Nightly"}));
    invoke(
        &engine,
        &alice(),
        "Run",
        "r1",
        "link",
        json!({"name": "pool", "id": "p1"}),
    )
    .expect("link");
    for id in ["s1", "s2"] {
        create(&engine, "Step", id, json!({}));
        step(
            &engine,
            &alice(),
            id,
            "link",
            json!({"name": "run", "id": "r1"}),
        );
    }
    engine
}

// claim takes a step's lease and reserves what the config gives, as
// Queue's claim does; it returns the lease's token.
fn claim(engine: &Engine, id: &str, who: &Principal) -> i64 {
    let taken = step(engine, who, id, "acquire", json!({}));
    step(engine, who, id, "reserve", json!({}));
    taken["token"].as_i64().expect("a token")
}

fn release(engine: &Engine, who: &Principal, id: &str, token: i64) {
    fenced(engine, who, id, "release", token).expect("release");
}

fn three(engine: &Engine) -> Vec<Value> {
    vec![
        cpu(engine, "Step", "s1"),
        cpu(engine, "Run", "r1"),
        cpu(engine, "Pool", "p1"),
    ]
}

// Budget: one instance.

#[test]
fn a_claim_reserves_usage_draws_the_reservation_down_and_the_release_settles_the_rest() {
    for target in targets("a_claim_reserves") {
        let engine = single(
            &target,
            json!({"meters": {"cpu": {"limit": 80, "reserve": 60}}}),
            None,
        );
        assert_eq!(cpu(&engine, "Step", "s1"), meter(0, 0, Some(80)));
        step(&engine, &worker(), "s1", "acquire", json!({}));
        assert_eq!(
            step(&engine, &worker(), "s1", "reserve", json!({})),
            json!({"reserved": {"cpu": 60}})
        );
        assert_eq!(
            cpu(&engine, "Step", "s1"),
            json!({"used": 0, "reserved": 60, "limit": 80, "remaining": 20})
        );
        assert_eq!(
            step(
                &engine,
                &worker(),
                "s1",
                "recordUsage",
                json!({"meter": "cpu", "amount": 25})
            ),
            json!({"meter": "cpu", "used": 25, "released": 25, "overruns": [], "directed": false})
        );
        assert_eq!(
            cpu(&engine, "Step", "s1"),
            json!({"used": 25, "reserved": 35, "limit": 80, "remaining": 20})
        );
        release(&engine, &worker(), "s1", 1);
        assert_eq!(
            cpu(&engine, "Step", "s1"),
            json!({"used": 25, "reserved": 0, "limit": 80, "remaining": 55})
        );
    }
}

#[test]
fn a_reservation_that_does_not_fit_is_refused_and_one_with_a_meter_takes_an_amount() {
    for target in targets("a_reservation_that_does_not_fit") {
        let engine = single(
            &target,
            json!({"meters": {"cpu": {"limit": 80, "reserve": 60}, "calls": {}}}),
            None,
        );
        claim(&engine, "s1", &worker());
        let seq = seq_of(&engine, "Step", "s1");
        let refused = veto(invoke(
            &engine,
            &worker(),
            "Step",
            "s1",
            "reserve",
            json!({"meter": "cpu", "amount": 30}),
        ));
        assert_eq!(
            (refused.0.as_str(), refused.1.as_str(), refused.2.as_str()),
            (
                "Budget",
                "reserve",
                "meter cpu has 20 of its limit 80 left, not 30"
            )
        );
        assert_eq!(
            (refused.3, refused.4),
            (
                Some("over_limit".to_owned()),
                Some(
                    json!({"meter": "cpu", "amount": 30, "remaining": 20, "limit": 80, "scope": {"schema": "Step", "id": "s1"}})
                )
            )
        );
        assert_eq!(cpu(&engine, "Step", "s1"), meter(0, 60, Some(80)));
        assert_eq!(seq_of(&engine, "Step", "s1"), seq);
        // A meter with no limit takes any amount; reserve with no meter
        // takes only the configured reservations.
        assert_eq!(
            step(
                &engine,
                &worker(),
                "s1",
                "reserve",
                json!({"meter": "calls", "amount": 1000})
            ),
            json!({"reserved": {"calls": 1000}})
        );
        assert_eq!(
            meter_of(&engine, "Step", "s1", "calls"),
            meter(0, 1000, None)
        );
        assert_eq!(
            step(
                &engine,
                &worker(),
                "s1",
                "reserve",
                json!({"meter": "cpu", "amount": 20})
            ),
            json!({"reserved": {"cpu": 20}})
        );
        assert_eq!(
            issues_of(&refusal(invoke(
                &engine,
                &worker(),
                "Step",
                "s1",
                "reserve",
                json!({"meter": "calls"})
            ))),
            vec![issue(
                "/amount",
                "meter calls has no configured reservation, so reserve takes an amount"
            )]
        );
        assert_eq!(
            issues_of(&refusal(invoke(
                &engine,
                &worker(),
                "Step",
                "s1",
                "reserve",
                json!({"amount": 5})
            ))),
            vec![issue(
                "/amount",
                "an amount is reserved of one meter, which meter names"
            )]
        );
        assert_eq!(
            issues_of(&refusal(invoke(
                &engine,
                &worker(),
                "Step",
                "s1",
                "recordUsage",
                json!({"meter": "gpu", "amount": 5})
            ))),
            vec![issue(
                "/meter",
                "Step has no meter gpu (its meters: calls, cpu)"
            )]
        );
    }
}

#[test]
fn on_a_type_that_composes_lease_reserve_needs_an_active_lease_and_only_its_holder_reserves() {
    for target in targets("reserve_needs_an_active_lease") {
        let engine = single(
            &target,
            json!({"meters": {"cpu": {"limit": 80, "reserve": 60}}}),
            None,
        );
        let reason = "no lease is active, and on a type that composes Lease a reservation is made under the active lease";
        let unleased = veto(invoke(
            &engine,
            &worker(),
            "Step",
            "s1",
            "reserve",
            json!({}),
        ));
        assert_eq!(
            (unleased.2.as_str(), unleased.3.as_deref()),
            (reason, Some("not_leased"))
        );
        step(&engine, &worker(), "s1", "acquire", json!({}));
        assert_eq!(
            veto(invoke(
                &engine,
                &other(),
                "Step",
                "s1",
                "reserve",
                json!({})
            ))
            .0,
            "Lease"
        );
        engine.clock().advance(60000).expect("advance");
        assert_eq!(
            veto(invoke(
                &engine,
                &other(),
                "Step",
                "s1",
                "reserve",
                json!({})
            ))
            .2,
            reason
        );
    }
}

#[test]
fn without_lease_on_the_type_a_reservation_needs_no_lease_and_lasts_until_it_is_settled() {
    for target in targets("without_lease") {
        let plain = single(
            &target,
            json!({"meters": {"cpu": {"limit": 80, "reserve": 60}}}),
            Some(json!([])),
        );
        assert_eq!(
            step(&plain, &other(), "s1", "reserve", json!({})),
            json!({"reserved": {"cpu": 60}})
        );
        assert_eq!(
            step(&plain, &other(), "s1", "settle", json!({})),
            json!({"released": {"cpu": 60}})
        );
        assert_eq!(cpu(&plain, "Step", "s1"), meter(0, 0, Some(80)));
    }
}

#[test]
fn usage_is_never_refused_and_the_holder_gets_one_directive_per_lease() {
    for target in targets("usage_is_never_refused") {
        let engine = single(
            &target,
            json!({"meters": {"cpu": {"limit": 80, "reserve": 60}}, "onExceeded": {"direct": "meterExceeded"}}),
            None,
        );
        claim(&engine, "s1", &worker());
        let over =
            json!({"schema": "Step", "id": "s1", "used": 100, "limit": 80, "escalated": false});
        assert_eq!(
            step(
                &engine,
                &worker(),
                "s1",
                "recordUsage",
                json!({"meter": "cpu", "amount": 100})
            ),
            json!({"meter": "cpu", "used": 100, "released": 60, "overruns": [over], "directed": true})
        );
        assert_eq!(
            cpu(&engine, "Step", "s1"),
            json!({"used": 100, "reserved": 0, "limit": 80, "remaining": -20})
        );
        let beat = fenced(&engine, &worker(), "s1", "heartbeat", 1).expect("heartbeat");
        assert_eq!(
            beat["directives"],
            json!([{"id": 1, "name": "meterExceeded", "data": {"meter": "cpu", "used": 100, "limit": 80, "scope": {"schema": "Step", "id": "s1"}}, "createdAt": T0, "createdBy": "wren"}])
        );
        // The worker could not send it itself; Budget's call() needs no
        // permission.
        assert_eq!(
            veto(invoke(&engine, &worker(), "Step", "s1", "direct", json!({"name": "meterExceeded"}))).2,
            "its config names no permission that sends directives (directPermission or overridePermission)"
        );
        // Once per meter per lease: the next overrun under the same token
        // sends none.
        assert_eq!(
            step(
                &engine,
                &worker(),
                "s1",
                "recordUsage",
                json!({"meter": "cpu", "amount": 5})
            )["directed"],
            json!(false)
        );
        assert_eq!(
            fenced(&engine, &worker(), "s1", "heartbeat", 1).expect("heartbeat")["directives"]
                .as_array()
                .map(Vec::len),
            Some(1)
        );
        release(&engine, &worker(), "s1", 1);
        // A caller without a lease learns of the overrun from the result
        // alone.
        let mut later = over.clone();
        later["used"] = json!(106);
        assert_eq!(
            step(
                &engine,
                &other(),
                "s1",
                "recordUsage",
                json!({"meter": "cpu", "amount": 1})
            ),
            json!({"meter": "cpu", "used": 106, "released": 0, "overruns": [later], "directed": false})
        );
        // A new lease is a new token, which gets its own directive.
        step(&engine, &worker(), "s1", "acquire", json!({}));
        assert_eq!(
            step(
                &engine,
                &worker(),
                "s1",
                "recordUsage",
                json!({"meter": "cpu", "amount": 1})
            )["directed"],
            json!(true)
        );
        assert_eq!(
            fenced(&engine, &worker(), "s1", "heartbeat", 3).expect("heartbeat")["directives"][0]
                ["data"],
            json!({"meter": "cpu", "used": 107, "limit": 80, "scope": {"schema": "Step", "id": "s1"}})
        );
    }
}

#[test]
fn settle_releases_the_own_reservation_of_one_meter_or_of_every_meter() {
    for target in targets("settle_releases") {
        let engine = single(
            &target,
            json!({"meters": {"cpu": {"limit": 80, "reserve": 60}, "calls": {"reserve": 3}}}),
            None,
        );
        claim(&engine, "s1", &worker());
        assert_eq!(
            step(
                &engine,
                &worker(),
                "s1",
                "settle",
                json!({"meter": "calls"})
            ),
            json!({"released": {"calls": 3}})
        );
        assert_eq!(
            step(&engine, &worker(), "s1", "settle", json!({})),
            json!({"released": {"cpu": 60, "calls": 0}})
        );
        assert_eq!(
            [
                cpu(&engine, "Step", "s1"),
                meter_of(&engine, "Step", "s1", "calls")
            ],
            [meter(0, 0, Some(80)), meter(0, 0, None)]
        );
    }
}

// Budget: settlement when a lease ends.

#[test]
fn a_release_settles_the_reservation_here_and_in_every_enclosing_scope() {
    for target in targets("a_release_settles") {
        let engine = chain(&target, Chain::default());
        claim(&engine, "s1", &worker());
        assert_eq!(
            three(&engine),
            [
                meter(0, 60, None),
                meter(0, 60, None),
                meter(0, 60, Some(100))
            ]
        );
        release(&engine, &worker(), "s1", 1);
        assert_eq!(
            three(&engine),
            [meter(0, 0, None), meter(0, 0, None), meter(0, 0, Some(100))]
        );
    }
}

#[test]
fn an_expiry_settles_it_and_an_expire_that_finds_the_lease_active_settles_nothing() {
    for target in targets("an_expiry_settles") {
        let engine = chain(&target, Chain::default());
        claim(&engine, "s1", &worker());
        assert_eq!(
            step(&engine, &sweeper(), "s1", "expire", json!({})),
            json!({"expired": false})
        );
        assert_eq!(cpu(&engine, "Pool", "p1"), meter(0, 60, Some(100)));
        engine.clock().advance(60000).expect("advance");
        assert_eq!(
            step(&engine, &sweeper(), "s1", "expire", json!({})),
            json!({"expired": true, "reason": "ttl"})
        );
        assert_eq!(
            three(&engine),
            [meter(0, 0, None), meter(0, 0, None), meter(0, 0, Some(100))]
        );
    }
}

#[test]
fn an_acquire_over_a_lapsed_lease_settles_the_reservation_of_the_lease_it_expires() {
    for target in targets("an_acquire_over_a_lapsed_lease") {
        let engine = chain(&target, Chain::default());
        claim(&engine, "s1", &worker());
        engine.clock().advance(60000).expect("advance");
        assert_eq!(
            step(&engine, &other(), "s1", "acquire", json!({}))["token"],
            json!(3)
        );
        assert_eq!(
            [cpu(&engine, "Step", "s1"), cpu(&engine, "Pool", "p1")],
            [meter(0, 0, None), meter(0, 0, Some(100))]
        );
        // The new holder's own reservation is made under its token, and
        // lasts.
        step(&engine, &other(), "s1", "reserve", json!({}));
        fenced(&engine, &other(), "s1", "heartbeat", 3).expect("heartbeat");
        assert_eq!(cpu(&engine, "Pool", "p1"), meter(0, 60, Some(100)));
    }
}

#[test]
fn deleting_an_instance_settles_everything_it_reserved_up_the_chain() {
    for target in targets("deleting_an_instance_settles") {
        let engine = chain(
            &target,
            Chain {
                pool: Some(json!({"limit": 200})),
                ..Chain::default()
            },
        );
        claim(&engine, "s1", &worker());
        claim(&engine, "s2", &worker());
        assert!(engine
            .instances()
            .delete(&worker(), "Step", "s1", WriteOptions::default())
            .expect("delete"));
        assert_eq!(
            [cpu(&engine, "Run", "r1"), cpu(&engine, "Pool", "p1")],
            [meter(0, 60, None), meter(0, 60, Some(200))]
        );
        assert_eq!(
            count(
                &engine,
                "SELECT COUNT(*) AS n FROM bhv_budget__meters WHERE id = 's1'"
            ),
            0
        );
        assert_eq!(
            count(
                &engine,
                "SELECT COUNT(*) AS n FROM bhv_budget__holds WHERE inner_id = 's1'"
            ),
            0
        );
    }
}

// Budget: enclosing scopes.

#[test]
fn a_scope_that_cannot_fit_a_reservation_refuses_it_and_leaves_nothing_in_the_chain() {
    for target in targets("a_scope_that_cannot_fit") {
        let engine = chain(&target, Chain::default());
        claim(&engine, "s1", &worker());
        step(&engine, &worker(), "s2", "acquire", json!({}));
        let seqs = |engine: &Engine| {
            [
                seq_of(engine, "Step", "s2"),
                seq_of(engine, "Run", "r1"),
                seq_of(engine, "Pool", "p1"),
            ]
        };
        let before = seqs(&engine);
        let result = invoke(&engine, &worker(), "Step", "s2", "reserve", json!({}));
        let message = refusal(invoke(
            &engine,
            &worker(),
            "Step",
            "s2",
            "reserve",
            json!({}),
        ))
        .message()
        .to_owned();
        let refused = veto(result);
        assert_eq!(
            (refused.0.as_str(), refused.1.as_str(), refused.2.as_str()),
            (
                "Budget",
                "reserveFor",
                "meter cpu has 40 of its limit 100 left, not 60"
            )
        );
        assert!(message.contains("of Pool p1:"), "{message}");
        assert_eq!(
            [
                cpu(&engine, "Step", "s2"),
                cpu(&engine, "Run", "r1"),
                cpu(&engine, "Pool", "p1")
            ],
            [
                meter(0, 0, None),
                meter(0, 60, None),
                meter(0, 60, Some(100))
            ]
        );
        assert_eq!(seqs(&engine), before);
        assert_eq!(
            count(
                &engine,
                "SELECT COUNT(*) AS n FROM bhv_budget__holds WHERE inner_id = 's2'"
            ),
            0
        );
        // Once the first claim's lease ends, the second fits.
        release(&engine, &worker(), "s1", 1);
        assert_eq!(
            step(&engine, &worker(), "s2", "reserve", json!({})),
            json!({"reserved": {"cpu": 60}})
        );
    }
}

#[test]
fn a_reservation_reaches_the_outermost_scope_through_every_level_each_with_its_own_event() {
    for target in targets("a_reservation_reaches_the_outermost_scope") {
        let engine = chain(
            &target,
            Chain {
                run: Some(json!({"limit": 150})),
                ..Chain::default()
            },
        );
        claim(&engine, "s1", &worker());
        assert_eq!(
            three(&engine),
            [
                meter(0, 60, None),
                meter(0, 60, Some(150)),
                meter(0, 60, Some(100))
            ]
        );
        for (schema, id, holds) in [
            ("Run", "r1", json!({"schema": "Step", "id": "s1"})),
            ("Pool", "p1", json!({"schema": "Run", "id": "r1"})),
        ] {
            let all = events(&engine, schema, id);
            let last = all.last().expect("an event");
            assert_eq!(last.actor, "wren");
            assert_eq!(last.change["operation"], json!("reserveFor"));
            let mut params = json!({"meter": "cpu"});
            params["schema"] = holds["schema"].clone();
            params["id"] = holds["id"].clone();
            params["amount"] = json!(60);
            assert_eq!(last.change["params"], params);
        }
        // The pool, held through one run, refuses a claim through another
        // of its runs.
        create(&engine, "Run", "r2", json!({"title": "Weekly"}));
        invoke(
            &engine,
            &alice(),
            "Run",
            "r2",
            "link",
            json!({"name": "pool", "id": "p1"}),
        )
        .expect("link");
        create(&engine, "Step", "s3", json!({}));
        step(
            &engine,
            &alice(),
            "s3",
            "link",
            json!({"name": "run", "id": "r2"}),
        );
        step(&engine, &other(), "s3", "acquire", json!({}));
        let message = refusal(invoke(
            &engine,
            &other(),
            "Step",
            "s3",
            "reserve",
            json!({}),
        ))
        .message()
        .to_owned();
        assert!(
            message.ends_with("of Pool p1: meter cpu has 40 of its limit 100 left, not 60"),
            "{message}"
        );
        assert_eq!(cpu(&engine, "Run", "r2"), meter(0, 0, Some(150)));
    }
    // A run's own limit refuses before the pool is asked.
    for target in targets("a_runs_own_limit_refuses_first") {
        let tight = chain(
            &target,
            Chain {
                run: Some(json!({"limit": 50})),
                ..Chain::default()
            },
        );
        step(&tight, &worker(), "s1", "acquire", json!({}));
        let message = refusal(invoke(
            &tight,
            &worker(),
            "Step",
            "s1",
            "reserve",
            json!({}),
        ))
        .message()
        .to_owned();
        assert!(
            message.ends_with("of Run r1: meter cpu has 50 of its limit 50 left, not 60"),
            "{message}"
        );
    }
}

#[test]
fn usage_beyond_a_reservation_releases_only_the_reservations_part_at_each_scope() {
    for target in targets("usage_beyond_a_reservation") {
        let engine = chain(
            &target,
            Chain {
                step: Some(json!({"reserve": 30})),
                ..Chain::default()
            },
        );
        claim(&engine, "s1", &worker());
        claim(&engine, "s2", &other());
        assert_eq!(cpu(&engine, "Pool", "p1"), meter(0, 60, Some(100)));
        assert_eq!(
            step(
                &engine,
                &worker(),
                "s1",
                "recordUsage",
                json!({"meter": "cpu", "amount": 50})
            ),
            json!({"meter": "cpu", "used": 50, "released": 30, "overruns": [], "directed": false})
        );
        // s2's 30 is still held at the run and the pool.
        assert_eq!(
            three(&engine),
            [
                meter(50, 0, None),
                meter(50, 30, None),
                meter(50, 30, Some(100))
            ]
        );
        assert_eq!(
            veto(invoke(
                &engine,
                &other(),
                "Step",
                "s2",
                "reserve",
                json!({"meter": "cpu", "amount": 21})
            ))
            .2,
            "meter cpu has 20 of its limit 100 left, not 21"
        );
    }
}

#[test]
fn usage_rolls_up_and_stays_when_the_reservation_is_settled() {
    for target in targets("usage_rolls_up") {
        let engine = chain(&target, Chain::default());
        claim(&engine, "s1", &worker());
        step(
            &engine,
            &worker(),
            "s1",
            "recordUsage",
            json!({"meter": "cpu", "amount": 30}),
        );
        release(&engine, &worker(), "s1", 1);
        assert_eq!(
            three(&engine),
            [
                meter(30, 0, None),
                meter(30, 0, None),
                meter(30, 0, Some(100))
            ]
        );
    }
}

#[test]
fn each_instances_reserve_field_reserves_its_own_amount_and_the_scopes_add_them_up() {
    for target in targets("each_instances_reserve_field") {
        let engine = chain(
            &target,
            Chain {
                pool: Some(json!({"limit": 200})),
                step: Some(json!({"reserveField": "estimate"})),
                ..Chain::default()
            },
        );
        update(&engine, &alice(), "s1", json!({"estimate": 30})).expect("update");
        update(&engine, &alice(), "s2", json!({"estimate": 80})).expect("update");
        claim(&engine, "s1", &worker());
        claim(&engine, "s2", &other());
        assert_eq!(cpu(&engine, "Run", "r1"), meter(0, 110, None));
        step(
            &engine,
            &worker(),
            "s1",
            "recordUsage",
            json!({"meter": "cpu", "amount": 10}),
        );
        release(&engine, &worker(), "s1", 1);
        assert_eq!(
            [cpu(&engine, "Run", "r1"), cpu(&engine, "Pool", "p1")],
            [meter(10, 80, None), meter(10, 80, Some(200))]
        );
        // An instance without a positive estimate reserves nothing of the
        // meter.
        create(&engine, "Step", "s3", json!({"title": "Unsized"}));
        step(&engine, &worker(), "s3", "acquire", json!({}));
        assert_eq!(
            step(&engine, &worker(), "s3", "reserve", json!({})),
            json!({"reserved": {}})
        );
    }
}

#[test]
fn with_reserve_beside_reserve_field_an_instance_without_a_positive_integer_reserves_the_config_amount(
) {
    for target in targets("reserve_beside_reserve_field") {
        let engine = chain(
            &target,
            Chain {
                pool: Some(json!({"limit": 200})),
                step: Some(json!({"reserve": 20, "reserveField": "estimate"})),
                ..Chain::default()
            },
        );
        update(&engine, &alice(), "s1", json!({"estimate": 30})).expect("update");
        update(&engine, &alice(), "s2", json!({"estimate": 0})).expect("update");
        claim(&engine, "s1", &worker());
        claim(&engine, "s2", &other());
        assert_eq!(
            [
                cpu(&engine, "Step", "s1"),
                cpu(&engine, "Step", "s2"),
                cpu(&engine, "Pool", "p1")
            ],
            [
                meter(0, 30, None),
                meter(0, 20, None),
                meter(0, 50, Some(200))
            ]
        );
    }
}

#[test]
fn usage_that_takes_a_scope_over_its_limit_is_reported_and_directs_the_holder() {
    for target in targets("usage_that_takes_a_scope_over") {
        let engine = chain(
            &target,
            Chain {
                step_config: Some(json!({"onExceeded": {"direct": "meterExceeded"}})),
                ..Chain::default()
            },
        );
        claim(&engine, "s1", &worker());
        assert_eq!(
            step(
                &engine,
                &worker(),
                "s1",
                "recordUsage",
                json!({"meter": "cpu", "amount": 120})
            ),
            json!({
                "meter": "cpu",
                "used": 120,
                "released": 60,
                "overruns": [{"schema": "Pool", "id": "p1", "used": 120, "limit": 100, "escalated": false}],
                "directed": true
            })
        );
        let beat = fenced(&engine, &worker(), "s1", "heartbeat", 1).expect("heartbeat");
        let directives: Vec<Value> = beat["directives"]
            .as_array()
            .expect("directives")
            .iter()
            .map(|one| json!([one["name"], one["data"]]))
            .collect();
        assert_eq!(
            directives,
            vec![
                json!(["meterExceeded", {"meter": "cpu", "used": 120, "limit": 100, "scope": {"schema": "Pool", "id": "p1"}}])
            ]
        );
    }
}

#[test]
fn the_scope_operations_cannot_free_what_an_instance_still_holds_nor_hold_more_than_it_reserved() {
    for target in targets("the_scope_operations") {
        let engine = chain(&target, Chain::default());
        claim(&engine, "s1", &worker());
        // Anyone who may write the scope can call them; they release
        // nothing the step still holds.
        assert_eq!(
            invoke(
                &engine,
                &other(),
                "Run",
                "r1",
                "settleFor",
                json!({"meter": "cpu", "schema": "Step", "id": "s1", "amount": 60})
            )
            .expect("settleFor"),
            json!({"released": 0})
        );
        assert_eq!(
            invoke(
                &engine,
                &other(),
                "Pool",
                "p1",
                "settleFor",
                json!({"meter": "cpu", "schema": "Run", "id": "r1", "amount": 60})
            )
            .expect("settleFor"),
            json!({"released": 0})
        );
        assert_eq!(
            invoke(
                &engine,
                &other(),
                "Pool",
                "p1",
                "recordUsageFor",
                json!({"meter": "cpu", "schema": "Run", "id": "r1", "amount": 1, "released": 60})
            )
            .expect("recordUsageFor"),
            json!({"released": 0, "overruns": []})
        );
        assert_eq!(
            [cpu(&engine, "Run", "r1"), cpu(&engine, "Pool", "p1")],
            [meter(0, 60, None), meter(1, 60, Some(100))]
        );
        let beyond = veto(invoke(
            &engine,
            &other(),
            "Pool",
            "p1",
            "reserveFor",
            json!({"meter": "cpu", "schema": "Run", "id": "r1", "amount": 10}),
        ));
        assert_eq!(
            (beyond.2.as_str(), beyond.3.as_deref()),
            (
                "Run r1 has 60 of meter cpu reserved and 60 of it is held here, so 10 more is not its to hold",
                Some("exceeds_reservation")
            )
        );
        // An instance that does not draw the meter from the scope is no one
        // the scope holds for.
        assert_eq!(
            issues_of(&refusal(invoke(
                &engine,
                &other(),
                "Pool",
                "p1",
                "reserveFor",
                json!({"meter": "cpu", "schema": "Step", "id": "s1", "amount": 10})
            ))),
            vec![issue("/id", "Step s1 does not draw meter cpu from Pool p1")]
        );
        assert_eq!(cpu(&engine, "Pool", "p1"), meter(1, 60, Some(100)));
    }
}

#[test]
fn a_scope_link_does_not_move_while_a_reservation_is_held_through_it() {
    for target in targets("a_scope_link_does_not_move") {
        let engine = chain(&target, Chain::default());
        create(&engine, "Run", "r2", json!({"title": "Other"}));
        claim(&engine, "s1", &worker());
        let reason =
            "meter cpu has 60 reserved through link run; settle it before the link changes";
        let moved = veto(invoke(
            &engine,
            &worker(),
            "Step",
            "s1",
            "link",
            json!({"name": "run", "id": "r2"}),
        ));
        assert_eq!(
            (moved.2.as_str(), moved.3.as_deref(), moved.4),
            (
                reason,
                Some("scope_reserved"),
                Some(json!({"meter": "cpu", "reserved": 60}))
            )
        );
        assert_eq!(
            veto(invoke(
                &engine,
                &worker(),
                "Step",
                "s1",
                "unlink",
                json!({"name": "run"})
            ))
            .2,
            reason
        );
        // A scope holding for an instance is held to it too.
        assert_eq!(
            veto(invoke(
                &engine,
                &alice(),
                "Run",
                "r1",
                "unlink",
                json!({"name": "pool"})
            ))
            .2,
            "meter cpu has 60 reserved through link pool; settle it before the link changes"
        );
        step(&engine, &worker(), "s1", "settle", json!({}));
        step(
            &engine,
            &worker(),
            "s1",
            "link",
            json!({"name": "run", "id": "r2"}),
        );
        step(
            &engine,
            &worker(),
            "s1",
            "recordUsage",
            json!({"meter": "cpu", "amount": 7}),
        );
        assert_eq!(
            [
                cpu(&engine, "Run", "r1")["used"].clone(),
                cpu(&engine, "Run", "r2")["used"].clone()
            ],
            [json!(0), json!(7)]
        );
    }
}

fn update(engine: &Engine, who: &Principal, id: &str, patch: Value) -> Result<Value> {
    engine
        .instances()
        .update(who, "Step", id, &patch, WriteOptions::default())
        .map(|record| Value::Object(record.data))
}

// queued opens an engine with Pool p1 (cpu limit 100), Runs r1 and r2
// under it, whose cap is their own limit, and Steps that compose Workflow,
// Lease, Links, Queue and Budget, with each step of steps ([id, run])
// linked to its run, and a runner whose schedules have had their first
// pass.
fn queued(
    target: &Target,
    steps: &[(&str, &str)],
    step_meters: Option<Value>,
    caps: &[(&str, i64)],
) -> Engine {
    let engine = target.open_workqueue_with(|options| {
        options.clock = Clock::Manual(T0);
        options.runner = Some(RunnerOptions::new(sweeper()));
    });
    publish(
        &engine,
        &budget_document(
            "Pool",
            json!([{"name": "Budget", "config": {"meters": {"cpu": {"limit": 100}}}}]),
        ),
    );
    publish(
        &engine,
        &budget_document(
            "Run",
            json!([
                {"name": "Links", "config": {"links": {"pool": {"schema": "Pool"}}}},
                {"name": "Budget", "config": {"meters": {"cpu": {"scope": "pool", "limitField": "cap"}}}}
            ]),
        ),
    );
    publish(
        &engine,
        &budget_document(
            "Step",
            json!([
                workflow(),
                lease(),
                {"name": "Links", "config": {"links": {"run": {"schema": "Run"}}}},
                {"name": "Queue", "config": {"claim": {"from": ["queued"], "to": "running"}}},
                {"name": "Budget", "config": {"meters": step_meters.unwrap_or_else(|| json!({"cpu": {"scope": "run", "reserve": 60}}))}}
            ]),
        ),
    );
    create(&engine, "Pool", "p1", json!({"title": "Shared"}));
    for run in ["r1", "r2"] {
        let cap = caps
            .iter()
            .find(|(name, _)| *name == run)
            .map(|(_, cap)| *cap);
        create(
            &engine,
            "Run",
            run,
            cap.map_or_else(|| json!({}), |cap| json!({"cap": cap})),
        );
        invoke(
            &engine,
            &alice(),
            "Run",
            run,
            "link",
            json!({"name": "pool", "id": "p1"}),
        )
        .expect("link");
    }
    for (id, run) in steps {
        create(&engine, "Step", id, json!({}));
        step(
            &engine,
            &alice(),
            id,
            "link",
            json!({"name": "run", "id": run}),
        );
    }
    engine.runner().run_due().expect("run");
    engine
}

fn claim_next(engine: &Engine, who: &Principal) -> Value {
    engine
        .instances()
        .invoke_schema(who, "Step", "claimNext", &json!({}), Default::default())
        .expect("claimNext")["claimed"]
        .clone()
}

fn step_of(engine: &Engine, id: &str) -> Value {
    let behaviors = record(engine, "Step", id)["behaviors"].clone();
    json!([
        behaviors["Workflow"]["status"],
        behaviors["Lease"]["holder"],
        behaviors["Lease"]["token"]
    ])
}

// Budget: claimed through Queue.

#[test]
fn a_claim_reserves_every_meters_claim_amount_here_and_in_every_scope() {
    for target in targets("a_claim_reserves_every_meter") {
        let engine = queued(
            &target,
            &[("s1", "r1")],
            Some(
                json!({"cpu": {"scope": "run", "reserve": 60}, "calls": {"reserve": 3}, "gpu": {"limit": 10}}),
            ),
            &[],
        );
        assert_eq!(
            step(&engine, &worker(), "s1", "claim", json!({})),
            json!({"id": "s1", "token": 1, "expiresAt": T0 + 60000, "heartbeatMs": 20000})
        );
        assert_eq!(step_of(&engine, "s1"), json!(["running", "wren", 1]));
        assert_eq!(
            [
                cpu(&engine, "Step", "s1"),
                meter_of(&engine, "Step", "s1", "calls"),
                meter_of(&engine, "Step", "s1", "gpu")
            ],
            [meter(0, 60, None), meter(0, 3, None), meter(0, 0, Some(10))]
        );
        assert_eq!(
            [cpu(&engine, "Run", "r1"), cpu(&engine, "Pool", "p1")],
            [meter(0, 60, None), meter(0, 60, Some(100))]
        );
    }
}

#[test]
fn a_reservation_that_does_not_fit_refuses_the_claim_which_leaves_nothing() {
    for target in targets("a_reservation_refuses_the_claim") {
        let engine = queued(&target, &[("s1", "r1"), ("s2", "r2")], None, &[]);
        step(&engine, &worker(), "s1", "claim", json!({}));
        let seq = seq_of(&engine, "Step", "s2");
        let message = refusal(invoke(&engine, &other(), "Step", "s2", "claim", json!({})))
            .message()
            .to_owned();
        let refused = veto(invoke(&engine, &other(), "Step", "s2", "claim", json!({})));
        assert_eq!(
            (refused.1.as_str(), refused.2.as_str()),
            (
                "reserveFor",
                "meter cpu has 40 of its limit 100 left, not 60"
            )
        );
        assert!(message.contains("of Pool p1:"), "{message}");
        assert_eq!(step_of(&engine, "s2"), json!(["queued", null, 0]));
        assert_eq!(seq_of(&engine, "Step", "s2"), seq);
        assert_eq!(
            [
                cpu(&engine, "Step", "s2"),
                cpu(&engine, "Run", "r2"),
                cpu(&engine, "Pool", "p1")
            ],
            [
                meter(0, 0, None),
                meter(0, 0, None),
                meter(0, 60, Some(100))
            ]
        );
    }
}

#[test]
fn claim_next_passes_over_a_candidate_whose_budget_does_not_fit_and_claims_the_next() {
    for target in targets("claim_next_passes_over") {
        let engine = queued(&target, &[("s1", "r1"), ("s2", "r2")], None, &[("r1", 50)]);
        assert_eq!(claim_next(&engine, &worker())["id"], json!("s2"));
        assert_eq!(
            [step_of(&engine, "s1"), step_of(&engine, "s2")],
            [json!(["queued", null, 0]), json!(["running", "wren", 1])]
        );
        assert_eq!(
            [cpu(&engine, "Run", "r1"), cpu(&engine, "Run", "r2")],
            [meter(0, 0, Some(50)), meter(0, 60, None)]
        );
        assert_eq!(claim_next(&engine, &other()), Value::Null);
    }
}

#[test]
fn a_release_and_the_runners_expiry_settle_the_claims_reservation_and_queues_copies_follow() {
    for target in targets("a_release_and_the_runners_expiry") {
        let engine = queued(&target, &[("s1", "r1")], None, &[]);
        assert_eq!(claim_next(&engine, &worker())["token"], json!(1));
        step(
            &engine,
            &worker(),
            "s1",
            "recordUsage",
            json!({"meter": "cpu", "amount": 10}),
        );
        release(&engine, &worker(), "s1", 1);
        assert_eq!(step_of(&engine, "s1"), json!(["queued", null, 2]));
        assert_eq!(
            [cpu(&engine, "Step", "s1"), cpu(&engine, "Pool", "p1")],
            [meter(10, 0, None), meter(10, 0, Some(100))]
        );
        assert_eq!(claim_next(&engine, &worker())["token"], json!(3));
        assert_eq!(cpu(&engine, "Pool", "p1"), meter(10, 60, Some(100)));
        engine.clock().advance(60000).expect("advance");
        engine.runner().run_due().expect("run");
        assert_eq!(step_of(&engine, "s1"), json!(["queued", null, 4]));
        assert_eq!(
            three(&engine),
            [
                meter(10, 0, None),
                meter(10, 0, None),
                meter(10, 0, Some(100))
            ]
        );
        assert_eq!(
            claim_next(&engine, &other()),
            json!({"id": "s1", "token": 5, "expiresAt": T0 + 120000, "heartbeatMs": 20000})
        );
    }
}

#[test]
fn through_three_levels_claim_next_claims_what_the_pool_can_fit() {
    for target in targets("through_three_levels") {
        let engine = queued(
            &target,
            &[("s1", "r1"), ("s2", "r2"), ("s3", "r1")],
            None,
            &[],
        );
        assert_eq!(claim_next(&engine, &worker())["id"], json!("s1"));
        assert_eq!(
            [cpu(&engine, "Run", "r1"), cpu(&engine, "Pool", "p1")],
            [meter(0, 60, None), meter(0, 60, Some(100))]
        );
        // s2 through r2 and s3 through r1 both find the pool short.
        assert_eq!(claim_next(&engine, &other()), Value::Null);
        step(
            &engine,
            &worker(),
            "s1",
            "recordUsage",
            json!({"meter": "cpu", "amount": 30}),
        );
        step(
            &engine,
            &worker(),
            "s1",
            "transition",
            json!({"to": "done"}),
        );
        release(&engine, &worker(), "s1", 1);
        assert_eq!(cpu(&engine, "Pool", "p1"), meter(30, 0, Some(100)));
        assert_eq!(claim_next(&engine, &other())["id"], json!("s2"));
        assert_eq!(
            [
                cpu(&engine, "Run", "r1"),
                cpu(&engine, "Run", "r2"),
                cpu(&engine, "Pool", "p1")
            ],
            [
                meter(30, 0, None),
                meter(0, 60, None),
                meter(30, 60, Some(100))
            ]
        );
        assert_eq!(claim_next(&engine, &worker()), Value::Null);
        assert_eq!(step_of(&engine, "s3"), json!(["queued", null, 0]));
    }
}

// Budget: a scope a new version moves.

#[test]
fn what_is_held_at_the_old_scope_is_released_there_and_a_reservation_through_the_new_one_waits() {
    for target in targets("a_scope_a_new_version_moves") {
        let engine = chain(&target, Chain::default());
        let step_document = |scope: &str| {
            budget_document(
                "Step",
                json!([
                    workflow(),
                    lease(),
                    {"name": "Links", "config": {"links": {"run": {"schema": "Run"}, "alt": {"schema": "Run"}}}},
                    {"name": "Budget", "config": {"meters": {"cpu": {"scope": scope, "reserve": 60}}}}
                ]),
            )
        };
        publish(&engine, &step_document("run"));
        create(&engine, "Run", "r2", json!({"title": "Other"}));
        step(
            &engine,
            &alice(),
            "s1",
            "link",
            json!({"name": "alt", "id": "r2"}),
        );
        claim(&engine, "s1", &worker());
        publish(&engine, &step_document("alt"));
        let moved = veto(invoke(
            &engine,
            &worker(),
            "Step",
            "s1",
            "reserve",
            json!({"meter": "cpu", "amount": 1}),
        ));
        assert_eq!(
            (moved.2.as_str(), moved.3.as_deref()),
            (
                "meter cpu has 60 reserved through Run r1; settle it before reserving through Run r2",
                Some("scope_moved")
            )
        );
        // checkReserve says so too: no reservation fits until the old one is
        // settled.
        assert_eq!(
            step(&engine, &worker(), "s1", "checkReserve", json!({})),
            json!({
                "fits": false,
                "until": null,
                "scopes": [{"schema": "Run", "id": "r2", "hears": [
                    {"path": "/behaviors/Budget/meters/cpu/remaining", "crosses": 60},
                    {"path": "/behaviors/Links/targets/pool"}
                ]}]
            })
        );
        // The usage counts at the new scope; the part it draws is released
        // at the old one, which holds it.
        step(
            &engine,
            &worker(),
            "s1",
            "recordUsage",
            json!({"meter": "cpu", "amount": 10}),
        );
        assert_eq!(
            [
                cpu(&engine, "Run", "r1"),
                cpu(&engine, "Run", "r2"),
                cpu(&engine, "Pool", "p1")
            ],
            [
                meter(0, 50, None),
                meter(10, 0, None),
                meter(0, 50, Some(100))
            ]
        );
        step(&engine, &worker(), "s1", "settle", json!({}));
        assert_eq!(
            [cpu(&engine, "Run", "r1"), cpu(&engine, "Pool", "p1")],
            [meter(0, 0, None), meter(0, 0, Some(100))]
        );
        step(&engine, &worker(), "s1", "reserve", json!({}));
        assert_eq!(cpu(&engine, "Run", "r2"), meter(10, 60, None));
    }
}

// Budget: checkReserve.

fn scopes(amount: i64) -> Value {
    json!([
        {"schema": "Run", "id": "r1", "hears": [
            {"path": "/behaviors/Budget/meters/cpu/remaining", "crosses": amount},
            {"path": "/behaviors/Links/targets/pool"}
        ]},
        {"schema": "Pool", "id": "p1", "hears": [
            {"path": "/behaviors/Budget/meters/cpu/remaining", "crosses": amount}
        ]}
    ])
}

#[test]
fn check_reserve_says_whether_reserve_would_fit_lists_the_scopes_it_read_and_changes_nothing() {
    for target in targets("check_reserve_says") {
        let engine = chain(
            &target,
            Chain {
                pool: Some(json!({"limit": 100})),
                ..Chain::default()
            },
        );
        let check = |id: &str, params: Value| {
            invoke(&engine, &worker(), "Step", id, "checkReserve", params)
        };
        assert_eq!(
            check("s1", json!({})).expect("check"),
            json!({"fits": true, "until": null, "scopes": scopes(60)})
        );
        claim(&engine, "s1", &worker());
        let seq = seq_of(&engine, "Step", "s2");
        // The pool has 40 of 100 left: s2's 60 does not fit, and only a
        // change makes it.
        assert_eq!(
            check("s2", json!({})).expect("check"),
            json!({"fits": false, "until": null, "scopes": scopes(60)})
        );
        assert_eq!(
            check("s2", json!({"meter": "cpu", "amount": 40})).expect("check"),
            json!({"fits": true, "until": null, "scopes": scopes(40)})
        );
        assert_eq!(seq_of(&engine, "Step", "s2"), seq);
        assert_eq!(
            issues_of(&refusal(check("s2", json!({"amount": 5})))),
            vec![issue(
                "/amount",
                "an amount is reserved of one meter, which meter names"
            )]
        );
        // An instance with no scope link reads none.
        create(&engine, "Step", "s3", json!({"title": "Loose"}));
        assert_eq!(
            check("s3", json!({})).expect("check"),
            json!({"fits": true, "until": null, "scopes": []})
        );
    }
}

#[test]
fn check_reserve_counts_a_leases_reservation_as_settled_once_the_lease_is_no_longer_active() {
    for target in targets("check_reserve_counts_a_lapsed_lease") {
        let engine = chain(
            &target,
            Chain {
                pool: Some(json!({"limit": 100})),
                ..Chain::default()
            },
        );
        claim(&engine, "s1", &worker());
        assert_eq!(
            step(&engine, &other(), "s1", "checkReserve", json!({})),
            json!({"fits": false, "until": null, "scopes": scopes(60)})
        );
        // Lapsed, not yet expired: the 60 still shows, and is counted as
        // gone. Until it is settled the remaining it turns on is the amount
        // less the 60, and after, the amount: the answer gives both.
        engine.clock().advance(60000).expect("advance");
        assert_eq!(cpu(&engine, "Pool", "p1"), meter(0, 60, Some(100)));
        let lapsed = step(&engine, &other(), "s1", "checkReserve", json!({}));
        assert_eq!(lapsed["fits"], json!(true));
        assert_eq!(
            lapsed["scopes"][1]["hears"],
            json!([
                {"path": "/behaviors/Budget/meters/cpu/remaining", "crosses": 0},
                {"path": "/behaviors/Budget/meters/cpu/remaining", "crosses": 60}
            ])
        );
        assert_eq!(
            step(&engine, &other(), "s2", "checkReserve", json!({}))["fits"],
            json!(false)
        );
    }
}

#[test]
fn a_reservation_daily_meters_keep_out_fits_at_the_start_of_the_next_utc_day() {
    for target in targets("daily_meters_keep_out") {
        let engine = chain(
            &target,
            Chain {
                pool: Some(json!({"limit": 100, "reset": "daily"})),
                step: Some(json!({"reserve": 60, "limit": 70})),
                ..Chain::default()
            },
        );
        claim(&engine, "s1", &worker());
        step(
            &engine,
            &worker(),
            "s1",
            "recordUsage",
            json!({"meter": "cpu", "amount": 60}),
        );
        release(&engine, &worker(), "s1", 1);
        // The pool, daily, has 40 left until the day turns; s2's own meter
        // is not daily but empty. Where the daily meter keeps it out, its
        // reserved and its limit decide whether the day lets it in:
        // reserved at 41 or more, 100 less 60 and 1, keeps it out tomorrow
        // too.
        assert_eq!(
            step(&engine, &other(), "s2", "checkReserve", json!({})),
            json!({
                "fits": false,
                "until": DAY,
                "scopes": [
                    {"schema": "Run", "id": "r1", "hears": [
                        {"path": "/behaviors/Budget/meters/cpu/remaining", "crosses": 60},
                        {"path": "/behaviors/Links/targets/pool"}
                    ]},
                    {"schema": "Pool", "id": "p1", "hears": [
                        {"path": "/behaviors/Budget/meters/cpu/remaining", "crosses": 60},
                        {"path": "/behaviors/Budget/meters/cpu/reserved", "crosses": 41},
                        {"path": "/behaviors/Budget/meters/cpu/limit"}
                    ]}
                ]
            })
        );
        // s1's own meter, not daily, has 10 of 70 left: no day makes 60
        // fit.
        assert_eq!(
            step(&engine, &other(), "s1", "checkReserve", json!({}))["until"],
            Value::Null
        );
        engine.clock().set(DAY).expect("set");
        assert_eq!(
            step(&engine, &other(), "s2", "checkReserve", json!({}))["fits"],
            json!(true)
        );
    }
}

// Budget: escalate.

fn pool_flow(permission: Option<&str>) -> Value {
    let mut transition = json!({"from": "open", "to": "paused"});
    if let Some(permission) = permission {
        transition["permission"] = json!(permission);
    }
    json!({"states": ["open", "paused"], "transitions": [transition]})
}

#[test]
fn usage_that_leaves_the_instance_over_its_limit_moves_its_status_in_the_same_operation() {
    for target in targets("usage_moves_its_status") {
        let engine = single(
            &target,
            json!({"meters": {"cpu": {"limit": 80, "reserve": 60}}, "escalate": {"transition": "failed", "from": ["running"]}}),
            None,
        );
        claim(&engine, "s1", &worker());
        step(
            &engine,
            &worker(),
            "s1",
            "transition",
            json!({"to": "running"}),
        );
        assert_eq!(
            step(
                &engine,
                &worker(),
                "s1",
                "recordUsage",
                json!({"meter": "cpu", "amount": 50})
            )["overruns"],
            json!([])
        );
        assert_eq!(status_of(&engine, "Step", "s1"), json!("running"));
        assert_eq!(
            step(
                &engine,
                &worker(),
                "s1",
                "recordUsage",
                json!({"meter": "cpu", "amount": 40})
            ),
            json!({
                "meter": "cpu",
                "used": 90,
                "released": 10,
                "overruns": [{"schema": "Step", "id": "s1", "used": 90, "limit": 80, "escalated": true}],
                "directed": false
            })
        );
        let all = events(&engine, "Step", "s1");
        let last = all.last().expect("an event");
        assert_eq!(
            [
                last.change["operation"].clone(),
                last.change["patch"]["behaviors"]["Workflow"]["status"].clone()
            ],
            [json!("recordUsage"), json!("failed")]
        );
        // Out of its from states, a later overrun leaves the status.
        assert_eq!(
            step(
                &engine,
                &worker(),
                "s1",
                "recordUsage",
                json!({"meter": "cpu", "amount": 1})
            )["overruns"][0]["escalated"],
            json!(false)
        );
    }
}

#[test]
fn a_scope_over_its_limit_moves_by_its_own_config_as_the_caller() {
    for target in targets("a_scope_over_its_limit_moves") {
        let engine = open(&target);
        let pool_of = |permission: Option<&str>| {
            budget_document(
                "Pool",
                json!([
                    {"name": "Workflow", "config": pool_flow(permission)},
                    {"name": "Budget", "config": {"meters": {"cpu": {"limit": 100}}, "escalate": {"transition": "paused", "from": ["open"]}}}
                ]),
            )
        };
        publish(&engine, &pool_of(Some("pools.pause")));
        publish(
            &engine,
            &budget_document(
                "Step",
                json!([
                    workflow(),
                    lease(),
                    {"name": "Links", "config": {"links": {"pool": {"schema": "Pool"}}}},
                    {"name": "Budget", "config": {"meters": {"cpu": {"scope": "pool", "reserve": 60}}}}
                ]),
            ),
        );
        create(&engine, "Pool", "p1", json!({"title": "Shared"}));
        create(&engine, "Step", "s1", json!({}));
        step(
            &engine,
            &alice(),
            "s1",
            "link",
            json!({"name": "pool", "id": "p1"}),
        );
        claim(&engine, "s1", &worker());
        let over = step(
            &engine,
            &worker(),
            "s1",
            "recordUsage",
            json!({"meter": "cpu", "amount": 110}),
        );
        assert_eq!(
            over["overruns"],
            json!([{"schema": "Pool", "id": "p1", "used": 110, "limit": 100, "escalated": false}])
        );
        assert_eq!(
            [
                status_of(&engine, "Pool", "p1"),
                cpu(&engine, "Pool", "p1")["used"].clone()
            ],
            [json!("open"), json!(110)]
        );
        // Without the permission on the transition, the caller moves it.
        publish(&engine, &pool_of(None));
        let moved = step(
            &engine,
            &worker(),
            "s1",
            "recordUsage",
            json!({"meter": "cpu", "amount": 1}),
        );
        assert_eq!(
            moved["overruns"],
            json!([{"schema": "Pool", "id": "p1", "used": 111, "limit": 100, "escalated": true}])
        );
        let all = events(&engine, "Pool", "p1");
        let last = all.last().expect("an event");
        assert_eq!(
            (
                last.actor.as_str(),
                last.change["operation"].clone(),
                last.change["patch"]["behaviors"]["Workflow"]["status"].clone()
            ),
            ("wren", json!("recordUsageFor"), json!("paused"))
        );
    }
}

#[test]
fn escalate_needs_workflow_its_states_and_a_transition_from_each_from_state() {
    for target in targets("escalate_needs_workflow") {
        let engine = open(&target);
        let budget_with = |escalate: Value| json!({"name": "Budget", "config": {"meters": {"cpu": {"limit": 10}}, "escalate": escalate}});
        let refused = |behaviors: Value| {
            refusal(define(&engine, &budget_document("Run", behaviors)))
                .message()
                .to_owned()
        };
        let message = refused(json!([budget_with(
            json!({"transition": "paused", "from": ["open"]})
        )]));
        assert!(
            message.contains(
                "escalate moves the status through Workflow, which the type does not list"
            ),
            "{message}"
        );
        let flow = json!({"name": "Workflow", "config": pool_flow(None)});
        let message = refused(json!([
            flow,
            budget_with(json!({"transition": "stopped", "from": ["open"]}))
        ]));
        assert!(
            message.contains("escalate.transition \"stopped\" is not a state of the type's Workflow (open, paused)"),
            "{message}"
        );
        let message = refused(json!([
            flow,
            budget_with(json!({"transition": "open", "from": ["paused"]}))
        ]));
        assert!(
            message.contains(
                "escalate: no transition of the type's Workflow leads from \"paused\" to \"open\""
            ),
            "{message}"
        );
        define(
            &engine,
            &budget_document(
                "Run",
                json!([
                    flow,
                    budget_with(json!({"transition": "paused", "from": ["open"]}))
                ]),
            ),
        )
        .expect("define");
    }
}

// Budget: limits.

#[test]
fn set_limit_needs_limit_permission_and_never_goes_below_what_is_used_and_reserved() {
    for target in targets("set_limit_needs_limit_permission") {
        let engine = single(
            &target,
            json!({"meters": {"cpu": {"limit": 80, "reserve": 60}}, "limitPermission": "budget.limit"}),
            None,
        );
        claim(&engine, "s1", &worker());
        step(
            &engine,
            &worker(),
            "s1",
            "recordUsage",
            json!({"meter": "cpu", "amount": 10}),
        );
        release(&engine, &worker(), "s1", 1);
        let refused = refusal(invoke(
            &engine,
            &other(),
            "Step",
            "s1",
            "setLimit",
            json!({"meter": "cpu", "limit": 200}),
        ));
        assert_eq!(
            (refused.code(), refused.message()),
            (
                EngineErrorCode::Forbidden,
                "otto may not change a limit of Step s1: it needs permission budget.limit"
            )
        );
        let below = veto(invoke(
            &engine,
            &operator(),
            "Step",
            "s1",
            "setLimit",
            json!({"meter": "cpu", "limit": 9}),
        ));
        assert_eq!(
            (below.2.as_str(), below.3.as_deref(), below.4),
            (
                "meter cpu has 10 used and reserved, more than the limit 9",
                Some("below_committed"),
                Some(json!({"meter": "cpu", "committed": 10}))
            )
        );
        // It lowers a limit too, down to what is committed.
        assert_eq!(
            step(
                &engine,
                &operator(),
                "s1",
                "setLimit",
                json!({"meter": "cpu", "limit": 10})
            ),
            json!({"meter": "cpu", "limit": 10, "previous": 80})
        );
        assert_eq!(
            step(
                &engine,
                &operator(),
                "s1",
                "setLimit",
                json!({"meter": "cpu", "limit": 200})
            ),
            json!({"meter": "cpu", "limit": 200, "previous": 10})
        );
        // The next reservation fits the new limit; while it is held, the
        // limit does not drop below it.
        claim(&engine, "s1", &lead());
        assert_eq!(cpu(&engine, "Step", "s1"), meter(10, 60, Some(200)));
        assert_eq!(
            veto(invoke(
                &engine,
                &lead(),
                "Step",
                "s1",
                "setLimit",
                json!({"meter": "cpu", "limit": 69})
            ))
            .2,
            "meter cpu has 70 used and reserved, more than the limit 69"
        );
        // While a worker holds the lease, the operator's change goes through
        // when Lease exempts it.
        assert_eq!(
            veto(invoke(
                &engine,
                &operator(),
                "Step",
                "s1",
                "setLimit",
                json!({"meter": "cpu", "limit": 70})
            ))
            .0,
            "Lease"
        );
    }
    for target in targets("set_limit_exempt") {
        let exempt = single(
            &target,
            json!({"meters": {"cpu": {"limit": 80, "reserve": 60}}, "limitPermission": "budget.limit"}),
            Some(json!([workflow(), {"name": "Lease", "config": {"exempt": ["Budget.setLimit"]}}])),
        );
        claim(&exempt, "s1", &worker());
        assert_eq!(
            step(
                &exempt,
                &operator(),
                "s1",
                "setLimit",
                json!({"meter": "cpu", "limit": 60})
            ),
            json!({"meter": "cpu", "limit": 60, "previous": 80})
        );
        assert_eq!(
            veto(invoke(
                &exempt,
                &worker(),
                "Step",
                "s1",
                "reserve",
                json!({"meter": "cpu", "amount": 1})
            ))
            .2,
            "meter cpu has 0 of its limit 60 left, not 1"
        );
    }
    for target in targets("set_limit_unset") {
        let unset = single(&target, json!({"meters": {"cpu": {"limit": 80}}}), None);
        let unnamed = veto(invoke(
            &unset,
            &operator(),
            "Step",
            "s1",
            "setLimit",
            json!({"meter": "cpu", "limit": 100}),
        ));
        assert_eq!(
            (unnamed.2.as_str(), unnamed.3.as_deref()),
            (
                "its config names no limitPermission, which changing a limit needs",
                Some("not_configured")
            )
        );
    }
}

#[test]
fn limit_field_holds_the_instances_limit_which_only_limit_permission_changes() {
    for target in targets("limit_field_holds_the_limit") {
        let engine = single(
            &target,
            json!({"meters": {"cpu": {"limitField": "cap", "reserve": 60}}, "limitPermission": "budget.limit"}),
            None,
        );
        assert_eq!(cpu(&engine, "Step", "s1"), meter(0, 0, None));
        create(&engine, "Step", "s2", json!({"title": "Capped", "cap": 80}));
        assert_eq!(cpu(&engine, "Step", "s2"), meter(0, 0, Some(80)));
        let refused = refusal(update(&engine, &other(), "s2", json!({"cap": 1000})));
        assert_eq!(
            (refused.code(), refused.message()),
            (
                EngineErrorCode::Forbidden,
                "otto may not change cap, the limit of meter cpu, of Step s2: it needs permission budget.limit"
            )
        );
        update(&engine, &operator(), "s2", json!({"cap": 100})).expect("update");
        assert_eq!(cpu(&engine, "Step", "s2"), meter(0, 0, Some(100)));
        assert_eq!(
            step(
                &engine,
                &operator(),
                "s2",
                "setLimit",
                json!({"meter": "cpu", "limit": 120})
            ),
            json!({"meter": "cpu", "limit": 120, "previous": 100})
        );
        assert_eq!(record(&engine, "Step", "s2")["data"]["cap"], json!(120));
        claim(&engine, "s2", &lead());
        step(
            &engine,
            &lead(),
            "s2",
            "recordUsage",
            json!({"meter": "cpu", "amount": 10}),
        );
        let below = veto(update(&engine, &lead(), "s2", json!({"cap": 50})));
        assert_eq!(
            (below.2.as_str(), below.3.as_deref()),
            (
                "meter cpu has 60 used and reserved, more than the limit 50",
                Some("below_committed")
            )
        );
    }
    for target in targets("limit_field_unset") {
        let unset = single(
            &target,
            json!({"meters": {"cpu": {"limitField": "cap"}}}),
            None,
        );
        let unnamed = veto(update(&unset, &alice(), "s1", json!({"cap": 10})));
        assert_eq!(
            (unnamed.2.as_str(), unnamed.3.as_deref()),
            (
                "cap holds the limit of meter cpu, which changes only with limitPermission, and the config names none",
                Some("not_configured")
            )
        );
        update(&unset, &alice(), "s1", json!({"title": "Renamed"})).expect("update");
    }
}

#[test]
fn limit_beside_limit_field_is_the_limit_while_the_field_holds_no_count() {
    for target in targets("limit_beside_limit_field") {
        let engine = single(
            &target,
            json!({"meters": {"cpu": {"limit": 70, "limitField": "cap", "reserve": 60}}, "limitPermission": "budget.limit"}),
            None,
        );
        assert_eq!(cpu(&engine, "Step", "s1"), meter(0, 0, Some(70)));
        create(
            &engine,
            "Step",
            "s2",
            json!({"title": "Capped", "cap": 100}),
        );
        assert_eq!(cpu(&engine, "Step", "s2"), meter(0, 0, Some(100)));
        // setLimit writes the field, which then holds the limit; emptying
        // it brings the fallback back.
        assert_eq!(
            step(
                &engine,
                &operator(),
                "s1",
                "setLimit",
                json!({"meter": "cpu", "limit": 90})
            ),
            json!({"meter": "cpu", "limit": 90, "previous": 70})
        );
        assert_eq!(
            [
                record(&engine, "Step", "s1")["data"]["cap"].clone(),
                cpu(&engine, "Step", "s1")
            ],
            [json!(90), meter(0, 0, Some(90))]
        );
        update(&engine, &operator(), "s1", json!({"cap": null})).expect("update");
        assert_eq!(cpu(&engine, "Step", "s1"), meter(0, 0, Some(70)));
        // The fallback is a limit a reservation is held to.
        claim(&engine, "s1", &worker());
        assert_eq!(cpu(&engine, "Step", "s1"), meter(0, 60, Some(70)));
        // Emptying the field is never below what is used and reserved, the
        // fallback being the limit.
        claim(&engine, "s2", &lead());
        step(
            &engine,
            &lead(),
            "s2",
            "recordUsage",
            json!({"meter": "cpu", "amount": 75}),
        );
        let below = veto(update(&engine, &lead(), "s2", json!({"cap": null})));
        assert_eq!(
            (below.2.as_str(), below.3.as_deref()),
            (
                "meter cpu has 75 used and reserved, more than the limit 70",
                Some("below_committed")
            )
        );
        assert_eq!(cpu(&engine, "Step", "s2"), meter(75, 0, Some(100)));
    }
}

// Budget: daily meters.

#[test]
fn a_daily_meters_usage_starts_again_each_utc_day_and_a_read_writes_nothing() {
    for target in targets("a_daily_meters_usage") {
        let engine = chain(
            &target,
            Chain {
                pool: Some(json!({"limit": 100, "reset": "daily"})),
                ..Chain::default()
            },
        );
        let stored = |engine: &Engine| {
            let used = count(
                engine,
                "SELECT used FROM bhv_budget__meters WHERE schema = 'Pool' AND id = 'p1'",
            );
            let start = count(
                engine,
                "SELECT period_start FROM bhv_budget__meters WHERE schema = 'Pool' AND id = 'p1'",
            );
            [used, start]
        };
        claim(&engine, "s1", &worker());
        step(
            &engine,
            &worker(),
            "s1",
            "recordUsage",
            json!({"meter": "cpu", "amount": 90}),
        );
        release(&engine, &worker(), "s1", 1);
        step(&engine, &other(), "s2", "acquire", json!({}));
        assert_eq!(
            veto(invoke(
                &engine,
                &other(),
                "Step",
                "s2",
                "reserve",
                json!({})
            ))
            .2,
            "meter cpu has 10 of its limit 100 left, not 60"
        );
        assert_eq!(stored(&engine), [90, 0]);

        engine.clock().set(DAY + 5000).expect("set");
        assert_eq!(cpu(&engine, "Pool", "p1"), meter(0, 0, Some(100)));
        // The read wrote nothing: back on the first day the pool still shows
        // its usage.
        assert_eq!(stored(&engine), [90, 0]);
        engine.clock().set(DAY - 1).expect("set");
        assert_eq!(cpu(&engine, "Pool", "p1"), meter(90, 0, Some(100)));

        engine.clock().set(DAY + 5000).expect("set");
        claim(&engine, "s2", &other());
        assert_eq!(stored(&engine), [0, DAY]);
        assert_eq!(cpu(&engine, "Pool", "p1"), meter(0, 60, Some(100)));
        // Reservations are kept across a day.
        engine.clock().set(2 * DAY).expect("set");
        assert_eq!(cpu(&engine, "Pool", "p1"), meter(0, 60, Some(100)));
    }
}

// Budget: its config.

#[test]
fn parse_config_holds_the_config_to_the_type_its_fields_links_and_the_schema_each_scope_points_at()
{
    for target in targets("parse_config_holds_the_config") {
        let engine = open(&target);
        let links = |schema: &str| json!({"name": "Links", "config": {"links": {"pool": {"schema": schema}}}});
        let budget_on = |cpu: Value, rest: Value| {
            let mut config = json!({"meters": {"cpu": cpu}});
            if let (Some(config), Value::Object(rest)) = (config.as_object_mut(), rest) {
                config.extend(rest);
            }
            json!({"name": "Budget", "config": config})
        };
        let refused = |behaviors: Value| {
            refusal(define(&engine, &budget_document("Run", behaviors)))
                .message()
                .to_owned()
        };
        for (behaviors, expected) in [
            (
                json!([budget_on(json!({"limitField": "title"}), json!({}))]),
                "behavior Budget config: meter cpu: limitField \"title\" is not an integer field of Run",
            ),
            (
                json!([budget_on(json!({"reserveField": "size"}), json!({}))]),
                "meter cpu: reserveField \"size\" is not a field of Run (its fields: title, cap, estimate)",
            ),
            (
                json!([budget_on(json!({"scope": "pool"}), json!({}))]),
                "meter cpu: scope pool is a link of Links, which the type does not list",
            ),
            (
                json!([links("Pool"), budget_on(json!({"scope": "parent"}), json!({}))]),
                "meter cpu: scope parent is not a link of the type's Links config (its links: pool)",
            ),
            (
                json!([
                    {"name": "Links", "config": {"links": {"pool": {"schema": "Pool", "many": true, "max": 2}}}},
                    budget_on(json!({"scope": "pool"}), json!({}))
                ]),
                "meter cpu: scope pool holds a list of targets, and a meter draws on one scope",
            ),
            (
                json!([links("Pool"), budget_on(json!({"scope": "pool"}), json!({}))]),
                "meter cpu: scope pool: schema Pool, which the link points at, has no live version; publish it first",
            ),
        ] {
            let message = refused(behaviors);
            assert!(message.contains(expected), "{message}");
        }
        publish(
            &engine,
            &budget_document("Pool", json!([{"name": "Comments"}])),
        );
        let message = refused(json!([
            links("Pool"),
            budget_on(json!({"scope": "pool"}), json!({}))
        ]));
        assert!(
            message.contains("scope pool: Pool, which the link points at, does not compose Budget"),
            "{message}"
        );
        publish(
            &engine,
            &budget_document(
                "Pool",
                json!([{"name": "Comments"}, {"name": "Budget", "config": {"meters": {"gpu": {"limit": 10}}}}]),
            ),
        );
        let message = refused(json!([
            links("Pool"),
            budget_on(json!({"scope": "pool"}), json!({}))
        ]));
        assert!(
            message.contains(
                "scope pool: Pool, which the link points at, has no meter cpu (its meters: gpu)"
            ),
            "{message}"
        );
        let message = refused(json!([budget_on(
            json!({}),
            json!({"onExceeded": {"direct": "stop"}})
        )]));
        assert!(
            message.contains(
                "onExceeded sends a directive through Lease's direct, which the type does not list"
            ),
            "{message}"
        );
        // The core meta-schema holds the shape first.
        let shape = refusal(define(
            &engine,
            &budget_document(
                "Run",
                json!([budget_on(
                    json!({"limit": 0, "limitField": "cap"}),
                    json!({})
                )]),
            ),
        ));
        assert!(
            issues_of(&shape)
                .iter()
                .any(|(path, _)| path == "/types/Run/behaviors/0/config/meters/cpu/limit"),
            "{shape:?}"
        );
        // A scope on the type's own schema: a run nested in a run.
        publish(
            &engine,
            &budget_document(
                "Run",
                json!([
                    {"name": "Links", "config": {"links": {"pool": {"schema": "Run"}}}},
                    budget_on(json!({"limit": 10, "scope": "pool"}), json!({}))
                ]),
            ),
        );
    }
}

#[test]
fn it_can_be_added_to_a_schema_that_has_instances_keeps_its_meters_and_cannot_be_removed_from_one()
{
    for target in targets("it_can_be_added") {
        let engine = open(&target);
        let pool = |meters: Option<Value>| {
            let mut behaviors = vec![json!({"name": "Comments"})];
            if let Some(meters) = meters {
                behaviors.push(json!({"name": "Budget", "config": {"meters": meters}}));
            }
            budget_document("Pool", Value::Array(behaviors))
        };
        publish(&engine, &pool(None));
        create(&engine, "Pool", "p1", json!({"title": "Before"}));
        publish(&engine, &pool(Some(json!({"cpu": {"limit": 100}}))));
        assert_eq!(cpu(&engine, "Pool", "p1"), meter(0, 0, Some(100)));
        invoke(
            &engine,
            &alice(),
            "Pool",
            "p1",
            "reserve",
            json!({"meter": "cpu", "amount": 40}),
        )
        .expect("reserve");
        // A limit may change, and a meter may be added; a meter may not go.
        publish(
            &engine,
            &pool(Some(json!({"cpu": {"limit": 120}, "gpu": {}}))),
        );
        assert_eq!(cpu(&engine, "Pool", "p1"), meter(0, 40, Some(120)));
        let dropped = refusal(define(&engine, &pool(Some(json!({"cpu": {"limit": 120}})))));
        assert_eq!(dropped.code(), EngineErrorCode::IncompatibleChange);
        assert!(
            dropped.message().contains(
                "meter gpu is gone, and its instances and the scopes they draw on may hold it"
            ),
            "{}",
            dropped.message()
        );
        let removed = refusal(define(&engine, &pool(None)));
        assert_eq!(removed.code(), EngineErrorCode::IncompatibleChange);
        assert!(
            removed.message().contains("behavior Budget cannot be removed from type Pool, which has instances: the budgets its instances hold, and what enclosing scopes hold for them, would stay behind"),
            "{}",
            removed.message()
        );
        // With no instance, nothing holds a meter: one may go.
        invoke(
            &engine,
            &alice(),
            "Pool",
            "p1",
            "settle",
            json!({"meter": "cpu"}),
        )
        .expect("settle");
        engine
            .instances()
            .delete(&alice(), "Pool", "p1", WriteOptions::default())
            .expect("delete");
        publish(&engine, &pool(Some(json!({"cpu": {"limit": 120}}))));
    }
}
