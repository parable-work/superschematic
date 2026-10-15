//! The work-queue behaviors' filters on the Rust engine (D58): the cases of
//! the work queue's `test/filters.test.ts`. A list keeps the instances
//! assigned to a principal or to none (`Assignment.assignee`), whose lease
//! a principal holds or that are free (`Lease.holder`), and whose retries
//! are exhausted (`Retries.exhausted`), each by its qualified name, as its
//! field reads and through an index on its column; the list tool says so.
//! On SQLite and, with the variable set, on Postgres.

mod support;
mod workqueue_support;

use serde_json::{json, Value};

use superschematic_engine::{Clock, Engine, ListOptions, Principal, FILTER_SCAN_ROWS};
use support::{alice, targets, Target};
use workqueue_support::{advance, job_flow, jobs_document, principal, publish, Instances, T0};

fn wren() -> Principal {
    principal("wren", &[])
}

fn otto() -> Principal {
    principal("otto", &[])
}

fn jobs(engine: &Engine) -> Instances<'_> {
    Instances {
        engine,
        schema: "Job",
    }
}

// world opens an engine whose Job composes Workflow, Lease, Assignment
// and Retries, with jobs j1 to jN.
fn world(target: &Target, count: usize) -> Engine {
    let engine = target.open_workqueue_with(|options| options.clock = Clock::Manual(T0));
    publish(
        &engine,
        &jobs_document(json!([
            {"name": "Workflow", "config": job_flow()},
            {"name": "Lease", "config": {"ttlMs": 60_000}},
            {"name": "Assignment"},
            {"name": "Retries", "config": {"classes": {"flaky": {"attempts": 1}}, "totalAttempts": 3, "exhaustedState": "failed"}}
        ])),
    );
    for index in 1..=count {
        let id = format!("j{index}");
        jobs(&engine).create(&id, json!({ "title": id }));
    }
    engine
}

fn page(engine: &Engine, filter: Value) -> superschematic_engine::InstancePage {
    engine
        .instances()
        .list(
            &alice(),
            "Job",
            ListOptions {
                filter: Some(&filter),
                ..ListOptions::default()
            },
        )
        .expect("list")
}

fn ids(engine: &Engine, filter: Value) -> Vec<String> {
    page(engine, filter)
        .items
        .into_iter()
        .map(|item| item.id)
        .collect()
}

fn invoke(engine: &Engine, who: &Principal, id: &str, operation: &str, params: Value) -> Value {
    jobs(engine)
        .invoke_on(who, id, operation, params, None)
        .expect("invoke")
}

#[test]
fn a_list_keeps_the_instances_assigned_to_a_principal_or_to_none() {
    for target in targets("filters_assignee") {
        let engine = world(&target, 4);
        invoke(&engine, &wren(), "j1", "assign", json!({"to": "wren"}));
        invoke(&engine, &otto(), "j2", "assign", json!({"to": "otto"}));
        invoke(&engine, &wren(), "j3", "assign", json!({"to": "wren"}));
        invoke(&engine, &wren(), "j3", "unassign", json!({}));
        assert_eq!(ids(&engine, json!({"Assignment.assignee": "wren"})), ["j1"]);
        assert_eq!(
            ids(&engine, json!({"Assignment.assignee": null})),
            ["j3", "j4"]
        );
        // My work, or work no one has: what a principal picks from.
        assert_eq!(
            ids(&engine, json!({"Assignment.assignee": [null, "wren"]})),
            ["j1", "j3", "j4"]
        );
        for item in page(&engine, json!({"Assignment.assignee": null})).items {
            assert_eq!(item.behaviors["Assignment"], json!({}));
        }
    }
}

#[test]
fn a_list_keeps_the_instances_whose_lease_a_principal_holds_a_lapsed_one_until_its_expiry_or_the_free_ones(
) {
    for target in targets("filters_holder") {
        let engine = world(&target, 4);
        invoke(&engine, &wren(), "j1", "acquire", json!({}));
        invoke(&engine, &otto(), "j2", "acquire", json!({}));
        let token = invoke(&engine, &wren(), "j3", "acquire", json!({}))["token"]
            .as_i64()
            .unwrap();
        jobs(&engine)
            .invoke_on(&wren(), "j3", "release", json!({}), Some(token))
            .unwrap();
        assert_eq!(ids(&engine, json!({"Lease.holder": "wren"})), ["j1"]);
        assert_eq!(ids(&engine, json!({"Lease.holder": null})), ["j3", "j4"]);
        // Lapsed, a lease is still its holder's, in the field as in the
        // filter, until the runner's sweep or an acquire applies its expiry.
        advance(&engine, 60_001);
        assert_eq!(
            ids(&engine, json!({"Lease.holder": ["wren", "otto"]})),
            ["j1", "j2"]
        );
        let lapsed = jobs(&engine).behavior("j1", "Lease");
        assert_eq!(
            (lapsed["holder"].clone(), lapsed["active"].clone()),
            (json!("wren"), json!(false))
        );
        invoke(&engine, &alice(), "j1", "expire", json!({}));
        assert_eq!(
            ids(&engine, json!({"Lease.holder": null})),
            ["j1", "j3", "j4"]
        );
        // A free instance's holder field is absent, as the null filter keeps
        // it.
        assert_eq!(jobs(&engine).behavior("j1", "Lease").get("holder"), None);
    }
}

#[test]
fn a_list_keeps_the_instances_whose_retries_are_exhausted_or_the_others() {
    for target in targets("filters_exhausted") {
        let engine = world(&target, 4);
        let attempt = invoke(
            &engine,
            &alice(),
            "j2",
            "recordAttempt",
            json!({"failure": "flaky"}),
        );
        assert_eq!(attempt["exhausted"], true);
        assert_eq!(ids(&engine, json!({"Retries.exhausted": true})), ["j2"]);
        assert_eq!(
            ids(
                &engine,
                json!({"Retries.exhausted": false, "Workflow.status": "queued"})
            ),
            ["j1", "j3", "j4"]
        );
        assert_eq!(jobs(&engine).behavior("j2", "Retries")["exhausted"], true);
    }
}

#[test]
fn each_reads_through_an_index_on_its_column_so_a_page_holds_every_match_among_more_instances_than_one_scan_reads(
) {
    for target in targets("filters_index") {
        let total = FILTER_SCAN_ROWS + 200;
        let engine = world(&target, total);
        let late = |back: usize| format!("j{}", total - back);
        invoke(&engine, &wren(), "j1", "assign", json!({"to": "wren"}));
        invoke(&engine, &wren(), &late(0), "assign", json!({"to": "wren"}));
        invoke(&engine, &otto(), "j2", "acquire", json!({}));
        invoke(&engine, &otto(), &late(1), "acquire", json!({}));
        invoke(
            &engine,
            &alice(),
            "j3",
            "recordAttempt",
            json!({"failure": "flaky"}),
        );
        invoke(
            &engine,
            &alice(),
            &late(2),
            "recordAttempt",
            json!({"failure": "flaky"}),
        );
        for (filter, expected) in [
            (
                json!({"Assignment.assignee": "wren"}),
                ["j1".to_owned(), late(0)],
            ),
            (json!({"Lease.holder": "otto"}), ["j2".to_owned(), late(1)]),
            (
                json!({"Retries.exhausted": true}),
                ["j3".to_owned(), late(2)],
            ),
        ] {
            let found = page(&engine, filter.clone());
            let ids: Vec<String> = found.items.into_iter().map(|item| item.id).collect();
            assert_eq!((ids, found.next), (expected.to_vec(), None), "{filter}");
        }
    }
}

#[test]
fn the_list_tools_where_takes_each_null_among_its_values_and_each_behavior_says_so() {
    for target in targets("filters_tool") {
        let engine = world(&target, 4);
        let described = engine
            .tools()
            .describe(&alice(), "Job", Default::default())
            .expect("describe");
        let list = described["operations"]
            .as_array()
            .unwrap()
            .iter()
            .find(|operation| operation["name"] == "list")
            .expect("list")
            .clone();
        let filter = &list["params"]["properties"]["where"]["properties"];
        let keys: Vec<&str> = filter
            .as_object()
            .unwrap()
            .keys()
            .map(String::as_str)
            .collect();
        assert_eq!(
            keys,
            [
                "title",
                "priority",
                "timeLimitMs",
                "topic",
                "urgent",
                "Workflow.status",
                "Lease.holder",
                "Assignment.assignee",
                "Retries.exhausted"
            ]
        );
        assert_eq!(
            filter["Lease.holder"]["anyOf"][0],
            json!({"type": ["string", "null"], "description": "The principal that holds its lease, a lapsed one included until its expiry is applied; null for a free instance."})
        );
        assert_eq!(
            filter["Assignment.assignee"]["anyOf"][0],
            json!({"type": ["string", "null"], "description": "The principal the instance is assigned to; absent when it is unassigned."})
        );
        assert_eq!(
            filter["Retries.exhausted"]["anyOf"][0],
            json!({"type": ["boolean", "null"], "description": "Whether its retries are exhausted."})
        );
        let use_when = list["guidance"]["useWhen"].as_str().unwrap_or_default();
        for sentence in [
            "where: { \"Lease.holder\": <subject> } lists the Job instances whose lease a principal holds, a lapsed one until the runner expires it, and \"Lease.holder\": null the free ones.",
            "where: { \"Assignment.assignee\": <subject> } lists the Job instances assigned to a principal, \"Assignment.assignee\": null the unassigned ones, and [null, <subject>] both.",
            "where: { \"Retries.exhausted\": true } lists the Job instances whose retries are exhausted, and false the ones that may run again.",
        ] {
            assert!(use_when.contains(sentence), "{use_when}");
        }
    }
}
