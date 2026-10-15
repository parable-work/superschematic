//! The document the core binary builds in the CLI smoke
//! (`internal/loader/testdata/services/fixture-workqueue-json`), on the Rust
//! engine (D58): the cases of the work queue's `test/package.test.ts` and
//! `test/guidance.test.ts` over it, whose jobs compose Workflow, Lease,
//! Assignment, Queue, Budget and Retries, whose workers compose Presence,
//! and whose batches stamp steps with Blueprint. Workers claim jobs in
//! order, each claim reserving its budget, and the runner puts a job whose
//! time ran out back in the queue, settling what it reserved; each
//! behavior's guidance on it says what its config means. On SQLite and,
//! with the variable set, on Postgres.

mod support;

use serde_json::{json, Value};

use superschematic_engine::{
    Clock, CreateOptions, DefineOptions, Engine, Principal, RunnerOptions, SchemaTarget,
    ToolTarget, WriteOptions,
};
use support::{alice, repository, targets, Target};

fn principal(subject: &str, permissions: &[&str]) -> Principal {
    Principal::new(subject, permissions.iter().map(|one| (*one).to_owned()))
}

fn worker() -> Principal {
    principal("wren", &[])
}

fn other() -> Principal {
    principal("otto", &[])
}

fn lead() -> Principal {
    principal("lena", &["jobs.assign"])
}

fn fixture_file(file: &str) -> Value {
    let path = repository().join(format!(
        "internal/loader/testdata/services/fixture-workqueue-json/src/{file}.schema.json"
    ));
    serde_json::from_str(&std::fs::read_to_string(&path).expect("the fixture")).expect("JSON")
}

/// The fixture's jobs document, as the core binary loads it.
fn jobs_fixture() -> Value {
    fixture_file("job")
}

/// One type's file of the fixture as a schema of its own.
fn fixture_type(name: &str, file: &str) -> Value {
    let found = fixture_file(file);
    let type_name = found["name"].as_str().expect("a type name").to_owned();
    json!({"kind": "General", "name": name, "types": {type_name: found}})
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

fn describe(engine: &Engine, schema: &str) -> Value {
    engine
        .tools()
        .describe(&alice(), schema, ToolTarget::default())
        .expect("describe")
}

fn guidance_of(engine: &Engine, schema: &str, operation: &str) -> Value {
    describe(engine, schema)["operations"]
        .as_array()
        .expect("operations")
        .iter()
        .find(|candidate| candidate["name"] == operation)
        .unwrap_or_else(|| panic!("{schema} has {operation}"))["guidance"]
        .clone()
}

fn summary_of(engine: &Engine, schema: &str, behavior: &str) -> String {
    describe(engine, schema)["behaviors"]
        .as_array()
        .expect("behaviors")
        .iter()
        .find(|candidate| candidate["name"] == behavior)
        .and_then(|candidate| candidate["summary"].as_str())
        .unwrap_or_else(|| panic!("{schema} {behavior} has a summary"))
        .to_owned()
}

fn text_of(guidance: &Value, key: &str) -> String {
    guidance[key].as_str().unwrap_or_default().to_owned()
}

// codes are a guidance's errors as `<behavior>.<code>`.
fn codes(guidance: &Value) -> Vec<String> {
    guidance["errors"]
        .as_array()
        .into_iter()
        .flatten()
        .map(|error| {
            let description = error["description"].as_str().unwrap_or_default();
            format!(
                "{}.{}",
                description.split(':').next().unwrap_or_default(),
                error["code"].as_str().unwrap_or_default()
            )
        })
        .collect()
}

fn assert_contains(text: &str, part: &str) {
    assert!(text.contains(part), "{text}\nwants: {part}");
}

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

fn open(target: &Target) -> Engine {
    target.open_workqueue()
}

// The work-queue behaviors say what their config means.

#[test]
fn lease_on_the_fixture_gives_its_lengths_what_an_expiry_does_and_fences_every_write() {
    for target in targets("fixture_lease_guidance") {
        let engine = open(&target);
        publish(&engine, &jobs_fixture());
        let summary = summary_of(&engine, "jobs", "Lease");
        assert_contains(&summary, "for 30 seconds at a time, renewed by heartbeat at least every 10 seconds, and held at most the instance's timeLimitMs milliseconds.");
        assert_contains(&summary, "The runner expires a lapsed lease within 5 seconds; an expiry moves status from running to queued.");
        assert_contains(&summary, "After 3 expiries it is not leased again, and that expiry moves status from running to failed instead.");
        assert!(
            text_of(&guidance_of(&engine, "jobs", "heartbeat"), "useWhen")
                .starts_with("Use as the holder at least every 10 seconds, presenting the token")
        );
        assert_contains(
            &text_of(&guidance_of(&engine, "jobs", "acquire"), "success"),
            "present the token as preconditions {\"Lease\": {\"token\": n}} on each write",
        );
        // Lease's guard holds the type's writes: Workflow's transition and
        // Lease's own update rule among them.
        assert_eq!(
            codes(&guidance_of(&engine, "jobs", "update")),
            [
                "Lease.held_by_another",
                "Lease.token_stale",
                "Lease.lapsed",
                "Lease.hold_limit_fixed"
            ]
        );
        assert!(codes(&guidance_of(&engine, "jobs", "transition"))
            .contains(&"Lease.held_by_another".to_owned()));
        // Queue's refresh is let through, a read is, and a create holds no
        // lease.
        for operation in ["refresh", "countClaimable", "create"] {
            assert!(
                !codes(&guidance_of(&engine, "jobs", operation))
                    .contains(&"Lease.held_by_another".to_owned()),
                "{operation}"
            );
        }
    }
}

#[test]
fn queue_names_its_claim_states_its_order_its_match_fields_and_what_keeps_work_out() {
    for target in targets("fixture_queue_guidance") {
        let engine = open(&target);
        publish(&engine, &jobs_fixture());
        let summary = summary_of(&engine, "jobs", "Queue");
        assert!(
            summary.starts_with("A claim takes an instance in queued: its lease, its budget reservation, and a move to running, in one write."),
            "{summary}"
        );
        assert_contains(&summary, "claimNext claims the first the caller can, highest priority first, then oldest first, matching topic, trying at most 100.");
        assert_contains(&summary, "not claimed while it is exhausted (Retries), over its budget, here or in an enclosing scope (Budget), at 3 lease expiries or assigned to another principal.");
        assert_contains(
            &text_of(&guidance_of(&engine, "jobs", "claimNext"), "useWhen"),
            "match filters on topic, each a value or a list of values. assignedOnly takes only work assigned to you.",
        );
        // A claim meets its own refusals and those of what it runs: the
        // lease, the assignment, the budget, the retries.
        assert_eq!(
            codes(&guidance_of(&engine, "jobs", "claim")),
            [
                "Queue.not_claimable",
                "Lease.held_by_another",
                "Lease.token_stale",
                "Lease.lapsed",
                "Assignment.assigned_to_another",
                "Budget.over_limit",
                "Budget.scope_moved",
                "Retries.exhausted"
            ]
        );
        let acquire = guidance_of(&engine, "jobs", "acquire");
        assert!(codes(&acquire).contains(&"Queue.claim_required".to_owned()));
        assert_contains(
            &text_of(&acquire, "doNotUseWhen"),
            "Do not use: on Job the lease is taken by claim or claimNext.",
        );

        publish(
            &engine,
            &jobs_document(
                json!([
                    {"name": "Workflow", "config": job_flow()},
                    {"name": "Dependencies"},
                    {"name": "Lease"},
                    {"name": "Queue", "config": {"claim": {"from": ["queued"], "to": "running"}}}
                ]),
                "Job",
            ),
        );
        assert_contains(
            &summary_of(&engine, "Job", "Queue"),
            "claimNext claims the first the caller can, oldest first, trying at most 100. An instance is not claimed while it is blocked.",
        );
        assert_eq!(
            codes(&guidance_of(&engine, "Job", "claim"))[..2],
            ["Queue.not_claimable", "Queue.blocked"]
        );
    }
}

#[test]
fn retries_gives_its_classes_and_caps_budget_its_meters_and_scopes_assignment_who_may_assign() {
    for target in targets("fixture_budget_guidance") {
        let engine = open(&target);
        publish(&engine, &jobs_fixture());
        let retries = summary_of(&engine, "jobs", "Retries");
        assert!(
            retries.starts_with("Failure classes: invalidOutput (2 attempts), rejected (terminal: one failure exhausts) and timeout (3 attempts); at most 4 counted failures in all."),
            "{retries}"
        );
        assert_contains(
            &retries,
            "Once exhausted, status moves from queued or running to failed",
        );
        assert!(codes(&guidance_of(&engine, "jobs", "transition"))
            .contains(&"Retries.exhausted".to_owned()));
        let budget = summary_of(&engine, "jobs", "Budget");
        assert!(
            budget.starts_with(
                "Meters: cpuSeconds (limit 3600; reserving 600 at a claim; counted per UTC day)."
            ),
            "{budget}"
        );
        assert_contains(
            &text_of(&guidance_of(&engine, "jobs", "setLimit"), "useWhen"),
            "needs permission jobs.budget",
        );
        assert_eq!(
            codes(&guidance_of(&engine, "jobs", "reserve"))[..3],
            [
                "Budget.over_limit",
                "Budget.scope_moved",
                "Budget.not_leased"
            ]
        );
        assert!(summary_of(&engine, "jobs", "Assignment").ends_with("need permission jobs.assign."));
        assert!(codes(&guidance_of(&engine, "jobs", "acquire"))
            .contains(&"Assignment.assigned_to_another".to_owned()));

        // A scope link: Budget holds Links' link and unlink of it while a
        // reservation is held through it.
        publish(
            &engine,
            &jobs_document(
                json!([{"name": "Budget", "config": {"meters": {"cpu": {"limit": 100}}}}]),
                "Pool",
            ),
        );
        publish(
            &engine,
            &jobs_document(
                json!([
                    {"name": "Links", "config": {"links": {"pool": {"schema": "Pool"}}}},
                    {"name": "Budget", "config": {"meters": {"cpu": {"reserveField": "timeLimitMs", "scope": "pool"}}}}
                ]),
                "Job",
            ),
        );
        assert_contains(
            &summary_of(&engine, "Job", "Budget"),
            "cpu (no limit; reserving the instance's timeLimitMs at a claim; drawn from the scope its pool link points at)",
        );
        assert_eq!(
            codes(&guidance_of(&engine, "Job", "link")),
            ["Budget.scope_reserved"]
        );
        assert_contains(
            &text_of(&guidance_of(&engine, "Job", "setLimit"), "doNotUseWhen"),
            "names no limitPermission",
        );
    }
}

fn publish_fixture(engine: &Engine) {
    publish(engine, &jobs_fixture());
    for (name, file) in [
        ("workers", "worker"),
        ("steps", "step"),
        ("batches", "batch"),
    ] {
        publish(engine, &fixture_type(name, file));
    }
}

#[test]
fn presence_names_who_beats_an_instance_and_blueprint_what_a_stamp_creates() {
    for target in targets("fixture_presence_blueprint_guidance") {
        let engine = open(&target);
        publish_fixture(&engine);
        let presence = summary_of(&engine, "workers", "Presence");
        assert_contains(
            &presence,
            "stands for the principal subject names, which beats it at least every 30 seconds",
        );
        assert_contains(&presence, "A miss moves status from idle or busy to missing. A miss expires the principal's leases on jobs, but those renewed since its last beat.");
        assert_eq!(
            codes(&guidance_of(&engine, "workers", "beat")),
            ["Presence.not_principal", "Presence.no_principal"]
        );
        assert_eq!(
            codes(&guidance_of(&engine, "workers", "update")),
            ["Presence.principal_fixed"]
        );

        let blueprint = summary_of(&engine, "batches", "Blueprint");
        assert!(
            blueprint.starts_with("The create of a Batch stamps a child per step, "),
            "{blueprint}"
        );
        assert_contains(
            &blueprint,
            "Each child is a steps instance whose step holds its step's key and whose batch links to its parent, copying topic;",
        );
        let create = guidance_of(&engine, "batches", "create");
        // The create's own sentence comes first; Blueprint's says what it
        // stamps.
        let success = text_of(&create, "success");
        let stamps = success
            .find("The create stamps the children (")
            .map(|at| &success[at..]);
        assert!(
            stamps.is_some_and(|stamps| stamps
                .ends_with(") in its own write; behaviors.Blueprint.children lists them.")),
            "{success}"
        );
        assert_eq!(
            codes(&create),
            ["Blueprint.no_dependencies", "Blueprint.not_constant"]
        );
    }
}

#[test]
fn every_operation_of_the_fixture_says_when_to_use_it_in_plain_ascii_and_the_tools_document_and_meta_carry_it(
) {
    for target in targets("fixture_guidance_ascii") {
        let engine = open(&target);
        publish_fixture(&engine);
        let manifest = engine
            .tools()
            .manifest(&alice(), ToolTarget::default())
            .expect("manifest");
        let tools = manifest["tools"].as_array().expect("tools");
        for schema in ["jobs", "workers", "steps", "batches"] {
            let document = describe(&engine, schema);
            for operation in document["operations"].as_array().expect("operations") {
                let name = format!(
                    "{schema}.{}",
                    operation["name"].as_str().unwrap_or_default()
                );
                let guidance = &operation["guidance"];
                assert!(
                    !text_of(guidance, "useWhen").is_empty()
                        || !text_of(guidance, "doNotUseWhen").is_empty(),
                    "{name} says when to use it"
                );
                let mut texts = vec![
                    text_of(guidance, "useWhen"),
                    text_of(guidance, "doNotUseWhen"),
                    text_of(guidance, "success"),
                ];
                for error in guidance["errors"].as_array().into_iter().flatten() {
                    for key in ["description", "commonCorrection"] {
                        texts.push(error[key].as_str().unwrap_or_default().to_owned());
                    }
                }
                for text in texts {
                    assert!(
                        text.bytes().all(|byte| (0x20..=0x7e).contains(&byte)),
                        "{name}: plain ASCII, {text:?}"
                    );
                }
                let entry = tools
                    .iter()
                    .find(|tool| tool["name"] == operation["tool"])
                    .unwrap_or_else(|| panic!("{name}: its tool"));
                assert_ne!(entry["mcp"]["hidden"], json!(true), "{name}");
                assert_eq!(
                    [
                        &entry["guidance"],
                        &entry["mcp"]["_meta"]["superschematic/operation-guidance"]
                    ],
                    [guidance, guidance],
                    "{name}"
                );
            }
        }
    }
}

// The work-queue package runs the document the core binary builds.

fn get(engine: &Engine, schema: &str, id: &str) -> Value {
    let found = engine
        .instances()
        .get(&alice(), schema, id, Default::default())
        .expect("get")
        .expect("the instance");
    json!({"data": found.data, "behaviors": found.behaviors})
}

fn create(engine: &Engine, schema: &str, id: &str, data: Value) {
    engine
        .instances()
        .create(
            &alice(),
            schema,
            &data,
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
) -> Value {
    engine
        .instances()
        .invoke(who, schema, id, operation, &params, WriteOptions::default())
        .expect(operation)
}

fn fenced(engine: &Engine, who: &Principal, id: &str, operation: &str, token: i64) -> Value {
    let preconditions = json!({"Lease": {"token": token}});
    engine
        .instances()
        .invoke(
            who,
            "jobs",
            id,
            operation,
            &json!({}),
            WriteOptions {
                preconditions: Some(&preconditions),
                ..Default::default()
            },
        )
        .expect(operation)
}

fn claim_next(engine: &Engine, who: &Principal, topic: &str) -> Value {
    engine
        .instances()
        .invoke_schema(
            who,
            "jobs",
            "claimNext",
            &json!({"match": {"topic": topic}}),
            None,
        )
        .expect("claimNext")["claimed"]
        .clone()
}

#[test]
fn it_runs_the_document_the_core_binary_builds_claimed_in_order_held_and_put_back_when_its_time_runs_out(
) {
    for target in targets("fixture_jobs_run") {
        let engine = target.open_workqueue_with(|options| {
            options.clock = Clock::Manual(1_000_000);
            options.runner = Some(RunnerOptions::new(principal("runner", &[])));
        });
        publish(&engine, &jobs_fixture());
        let described = describe(&engine, "jobs");
        let behaviors: Vec<Value> = described["behaviors"]
            .as_array()
            .expect("behaviors")
            .iter()
            .map(|behavior| json!([behavior["name"], behavior["operations"]]))
            .collect();
        assert_eq!(
            behaviors,
            [
                json!(["Workflow", ["transition"]]),
                json!([
                    "Lease",
                    [
                        "acquire",
                        "heartbeat",
                        "release",
                        "expire",
                        "direct",
                        "acknowledge",
                        "expireHolder",
                        "resetExpiries"
                    ]
                ]),
                json!(["Assignment", ["assign", "unassign"]]),
                json!(["Queue", ["claim", "claimNext", "countClaimable", "refresh"]]),
                json!([
                    "Budget",
                    [
                        "reserve",
                        "checkReserve",
                        "recordUsage",
                        "settle",
                        "setLimit",
                        "reserveFor",
                        "settleFor",
                        "recordUsageFor"
                    ]
                ]),
                json!(["Retries", ["recordAttempt"]])
            ]
        );
        // The config a client reads its lease length from.
        assert_eq!(described["behaviors"][1]["config"]["ttlMs"], json!(30000));
        engine.runner().run_due().expect("run");

        for (id, data) in [
            (
                "index",
                json!({"topic": "search", "priority": 5, "timeLimitMs": 45000}),
            ),
            ("reindex", json!({"topic": "search", "priority": 9})),
            ("digest", json!({"topic": "mail", "priority": 7})),
            ("mine", json!({"topic": "search", "priority": 10})),
        ] {
            let mut fields = json!({"title": id});
            if let (Some(fields), Value::Object(data)) = (fields.as_object_mut(), data) {
                fields.extend(data);
            }
            create(&engine, "jobs", id, fields);
        }
        invoke(
            &engine,
            &lead(),
            "jobs",
            "mine",
            "assign",
            json!({"to": "otto"}),
        );

        assert_eq!(
            claim_next(&engine, &worker(), "search"),
            json!({"id": "reindex", "token": 1, "expiresAt": 1_030_000, "heartbeatMs": 10000})
        );
        assert_eq!(
            claim_next(&engine, &worker(), "search")["id"],
            json!("index")
        );
        assert_eq!(claim_next(&engine, &worker(), "search"), Value::Null);
        assert_eq!(claim_next(&engine, &other(), "search")["id"], json!("mine"));
        // Each claim reserved the job's claim amount of its daily CPU budget.
        assert_eq!(
            get(&engine, "jobs", "index")["behaviors"]["Budget"]["meters"],
            json!({"cpuSeconds": {"used": 0, "reserved": 600, "limit": 3600, "remaining": 3000}})
        );
        invoke(
            &engine,
            &worker(),
            "jobs",
            "index",
            "recordUsage",
            json!({"meter": "cpuSeconds", "amount": 100}),
        );
        invoke(
            &engine,
            &worker(),
            "jobs",
            "index",
            "recordAttempt",
            json!({"failure": "timeout"}),
        );

        engine.clock().advance(20000).expect("advance");
        assert_eq!(
            fenced(&engine, &worker(), "reindex", "heartbeat", 1)["expiresAt"],
            json!(1_050_000)
        );
        assert_eq!(
            fenced(&engine, &worker(), "index", "heartbeat", 1)["expiresAt"],
            json!(1_045_000)
        );
        invoke(
            &engine,
            &worker(),
            "jobs",
            "reindex",
            "transition",
            json!({"to": "done"}),
        );
        fenced(&engine, &worker(), "reindex", "release", 1);

        // index's time limit passes, and mine's lease, never renewed, has
        // expired: the runner's sweep puts both back in the queue.
        let now = engine.clock().advance(25000).expect("advance");
        assert_eq!(engine.runner().run_due().expect("run").scheduled, 1);
        let index = get(&engine, "jobs", "index");
        assert_eq!(
            index["data"],
            json!({"title": "index", "topic": "search", "priority": 5, "timeLimitMs": 45000})
        );
        // Each behavior that declares a field has an entry, an unassigned
        // job's Assignment an empty one; the sweep's move entered queued now.
        assert_eq!(
            index["behaviors"],
            json!({
                "Workflow": {"status": "queued", "enteredAt": now},
                "Lease": {"token": 2, "active": false, "expiries": 1, "ended": {"reason": "maxHold", "at": 1_045_000}},
                "Assignment": {},
                "Budget": {"meters": {"cpuSeconds": {"used": 100, "reserved": 0, "limit": 3600, "remaining": 3500}}},
                "Retries": {"total": 1, "classAttempts": {"timeout": 1, "invalidOutput": 0, "rejected": 0}, "exhausted": false, "stuck": false}
            })
        );
        assert_eq!(
            [
                claim_next(&engine, &other(), "search")["id"].clone(),
                claim_next(&engine, &other(), "search")["id"].clone()
            ],
            [json!("mine"), json!("index")]
        );
        assert_eq!(
            get(&engine, "jobs", "reindex")["behaviors"]["Workflow"]["status"],
            json!("done")
        );
    }
}

#[test]
fn it_runs_the_fixtures_worker_type_a_missed_workers_live_lease_is_spared_and_goes_when_it_runs_out(
) {
    for target in targets("fixture_worker_run") {
        let engine = target.open_workqueue_with(|options| {
            options.clock = Clock::Manual(1_000_000);
            options.runner = Some(RunnerOptions::new(principal("runner", &["jobs.override"])));
        });
        publish(&engine, &jobs_fixture());
        publish(&engine, &fixture_type("workers", "worker"));
        engine.runner().run_due().expect("run");

        create(
            &engine,
            "workers",
            "w1",
            json!({"subject": "wren", "name": "Wren"}),
        );
        create(
            &engine,
            "jobs",
            "index",
            json!({"title": "index", "topic": "search", "priority": 5}),
        );
        engine.clock().advance(10000).expect("advance");
        assert_eq!(
            invoke(&engine, &worker(), "workers", "w1", "beat", json!({})),
            json!({"deadline": 1_040_000})
        );
        assert_eq!(
            claim_next(&engine, &worker(), "search")["id"],
            json!("index")
        );
        engine.clock().advance(20000).expect("advance");
        fenced(&engine, &worker(), "index", "heartbeat", 1);

        // wren stops beating its worker but renews its job's lease: the
        // runner misses the worker, and the miss spares the lease, renewed
        // after the worker's last beat, so live work is not lost.
        engine.clock().advance(10000).expect("advance");
        engine.runner().run_due().expect("run");
        let w1 = get(&engine, "workers", "w1");
        assert_eq!(w1["behaviors"]["Workflow"]["status"], json!("missing"));
        assert_eq!(w1["behaviors"]["Presence"]["released"], json!({"jobs": []}));
        assert_eq!(
            get(&engine, "jobs", "index")["behaviors"]["Lease"]["holder"],
            json!("wren")
        );
        // Its heartbeats stop too: the lease runs out on its own, at
        // 1_060_000, and the runner's sweep puts the job back.
        engine.clock().advance(20000).expect("advance");
        engine.runner().run_due().expect("run");
        let index = get(&engine, "jobs", "index");
        assert_eq!(
            index["behaviors"]["Lease"],
            json!({"token": 2, "active": false, "expiries": 1, "ended": {"reason": "ttl", "at": 1_060_000}})
        );
        assert_eq!(index["behaviors"]["Workflow"]["status"], json!("queued"));
        // The expiry settled the claim's reservation.
        assert_eq!(
            index["behaviors"]["Budget"]["meters"],
            json!({"cpuSeconds": {"used": 0, "reserved": 0, "limit": 3600, "remaining": 3600}})
        );
        // Queue's copies followed the expiry, so claimNext finds the job
        // again.
        assert_eq!(
            claim_next(&engine, &other(), "search")["id"],
            json!("index")
        );
    }
}

#[test]
fn it_runs_the_fixtures_batch_and_step_types_each_as_a_schema_of_its_own_a_batch_stamps_its_steps()
{
    for target in targets("fixture_batch_run") {
        let engine = open(&target);
        for (name, file) in [("steps", "step"), ("batches", "batch")] {
            publish(&engine, &fixture_type(name, file));
        }
        create(
            &engine,
            "batches",
            "b1",
            json!({"title": "Search the archive", "topic": "search"}),
        );
        create(
            &engine,
            "batches",
            "b2",
            json!({"title": "Copy the archive", "topic": "copy"}),
        );
        let stamped = |id: &str| -> Vec<Value> {
            get(&engine, "batches", id)["behaviors"]["Blueprint"]["children"]
                .as_array()
                .expect("children")
                .iter()
                .map(|child| {
                    let step = get(&engine, "steps", child["id"].as_str().unwrap_or_default());
                    json!([
                        step["data"]["step"],
                        step["data"]["topic"],
                        step["behaviors"]["Dependencies"]["blocked"]
                    ])
                })
                .collect()
        };
        assert_eq!(
            stamped("b1"),
            [
                json!(["fetch", "search", false]),
                json!(["check", "search", true]),
                json!(["index", "search", true])
            ]
        );
        assert_eq!(
            stamped("b2"),
            [
                json!(["fetch", "copy", false]),
                json!(["index", "copy", true])
            ]
        );
    }
}
