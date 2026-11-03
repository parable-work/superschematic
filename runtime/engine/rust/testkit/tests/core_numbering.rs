//! Numbering, a number a person reads on each instance (D56): the cases of
//! `test/core-numbering.test.ts` (its format's tokens and padding, the
//! configs it refuses at define, a count with no gap a refused create
//! leaves, counts per scope target and per year, a list's filters on the
//! number and the label through their indexes, assignNumber's backfill,
//! the rule that nothing changes a number, the rule for a new version, and
//! Revisions beside it), on SQLite and, with the variable set, on Postgres.
//! Its guidance, which the TypeScript file reads through the describe
//! document (milestone 6), is read through the behavior's hook.

mod support;

use serde_json::{json, Value};

use superschematic_engine::behaviors::{DescribeTarget, DescribedTypeOperation, OperationScope};
use superschematic_engine::{
    Behavior, BehaviorSet, Clock, CreateOptions, DefineOptions, Engine, EngineErrorCode,
    EngineErrorKind, EngineOptions, ListOptions, Principal, ReadEvents, Result, SchemaTarget,
    WriteOptions, FILTER_SCAN_ROWS,
};
use superschematic_engine_behaviors::{Links, Numbering, NumberingConfig, Revisions};
use superschematic_engine_testkit::behaviors::Refuser;
use support::{alice, refusal, schema_document, targets, veto, Target};

const MARCH_2026: i64 = 1_772_323_200_000;

fn behaviors() -> BehaviorSet {
    BehaviorSet::new()
        .with(Revisions)
        .with(Links)
        .with(Numbering)
}

// customers is the schema an invoice's customer link points at.
fn customers() -> Value {
    schema_document(
        "Customer",
        json!([{"name": "name", "typeRef": {"name": "string"}, "required": true}]),
        json!({}),
    )
}

// invoice is an Invoice schema whose type composes Links, with a required
// customer and an optional referrer, then the behaviors given.
fn invoice(behaviors: &[Value], links: bool) -> Value {
    let mut document = schema_document(
        "Invoice",
        json!([
            {"name": "title", "typeRef": {"name": "string"}, "required": true},
            {"name": "note", "typeRef": {"name": "string"}}
        ]),
        json!({}),
    );
    let mut all = Vec::new();
    if links {
        all.push(json!({"name": "Links", "config": {"links": {"customer": {"schema": "Customer", "required": true}, "referrer": {"schema": "Customer"}}}}));
    }
    all.extend(behaviors.iter().cloned());
    document["types"]["Invoice"]["behaviors"] = Value::Array(all);
    document
}

fn numbered(config: Value, more: &[Value]) -> Value {
    let mut behaviors = vec![json!({"name": "Numbering", "config": config})];
    behaviors.extend(more.iter().cloned());
    invoice(&behaviors, true)
}

// open opens an engine at a time, with the customers c1 and c2.
fn open_with(
    target: &Target,
    now: i64,
    behaviors: BehaviorSet,
    configure: impl FnOnce(&mut EngineOptions),
) -> Engine {
    let engine = target.open_core_with(|options| {
        options.behaviors = behaviors;
        options.clock = Clock::Manual(now);
        configure(options);
    });
    publish(&engine, &customers());
    for id in ["c1", "c2"] {
        engine
            .instances()
            .create(
                &alice(),
                "Customer",
                &json!({ "name": id }),
                CreateOptions {
                    id: Some(id),
                    ..Default::default()
                },
            )
            .expect("customer");
    }
    engine
}

fn open(target: &Target, now: i64) -> Engine {
    open_with(target, now, behaviors(), |_| {})
}

fn define(engine: &Engine, document: &Value) -> Result<()> {
    engine
        .schemas()
        .define(&alice(), document, DefineOptions::default())
        .map(|_| ())
}

fn publish(engine: &Engine, document: &Value) -> i64 {
    try_publish(engine, document).expect("publish")
}

fn try_publish(engine: &Engine, document: &Value) -> Result<i64> {
    define(engine, document)?;
    Ok(engine
        .schemas()
        .publish(
            &alice(),
            document["name"].as_str().expect("a name"),
            SchemaTarget::default(),
        )?
        .version)
}

fn try_create(engine: &Engine, id: &str, customer: &str, title: &str) -> Result<Value> {
    engine
        .instances()
        .create(
            &alice(),
            "Invoice",
            &json!({ "title": title }),
            CreateOptions {
                id: Some(id),
                behaviors: Some(&json!({"Links": {"customer": customer}})),
                ..Default::default()
            },
        )
        .map(|record| {
            record
                .behaviors
                .get("Numbering")
                .cloned()
                .unwrap_or(Value::Null)
        })
}

fn create(engine: &Engine, id: &str) -> Value {
    try_create(engine, id, "c1", id).expect("create")
}

fn create_for(engine: &Engine, id: &str, customer: &str) -> Value {
    try_create(engine, id, customer, id).expect("create")
}

fn number_of(engine: &Engine, id: &str) -> Value {
    engine
        .instances()
        .get(&alice(), "Invoice", id, Default::default())
        .expect("get")
        .expect("the invoice")
        .behaviors
        .get("Numbering")
        .cloned()
        .unwrap_or(Value::Null)
}

fn invoke(
    engine: &Engine,
    principal: &Principal,
    id: &str,
    operation: &str,
    params: Value,
) -> Result<Value> {
    engine.instances().invoke(
        principal,
        "Invoice",
        id,
        operation,
        &params,
        WriteOptions::default(),
    )
}

// every lists the invoices a filter finds, page by page: their ids and
// each page's size.
fn every(engine: &Engine, filter: Value) -> (Vec<String>, Vec<usize>) {
    let mut ids = Vec::new();
    let mut pages = Vec::new();
    let mut cursor: Option<String> = None;
    loop {
        let page = engine
            .instances()
            .list(
                &alice(),
                "Invoice",
                ListOptions {
                    filter: Some(&filter),
                    cursor: cursor.as_deref(),
                    ..Default::default()
                },
            )
            .expect("list");
        ids.extend(page.items.iter().map(|item| item.id.clone()));
        pages.push(page.items.len());
        cursor = page.next;
        if cursor.is_none() {
            return (ids, pages);
        }
    }
}

fn config_issues(engine: &Engine, document: &Value) -> Vec<(String, String)> {
    refusal(define(engine, document))
        .issues()
        .iter()
        .map(|issue| (issue.path.clone(), issue.message.clone()))
        .collect()
}

#[test]
fn a_create_numbers_the_instance_and_the_format_pads_and_prints_the_year() {
    for target in targets("numbering_create") {
        let engine = open(&target, MARCH_2026);
        publish(
            &engine,
            &numbered(json!({"format": "INV-{yyyy}-{seq:4}"}), &[]),
        );
        assert_eq!(
            create(&engine, "i1"),
            json!({"seq": 1, "label": "INV-2026-0001"})
        );
        assert_eq!(
            create(&engine, "i2"),
            json!({"seq": 2, "label": "INV-2026-0002"})
        );
        assert_eq!(
            number_of(&engine, "i2"),
            json!({"seq": 2, "label": "INV-2026-0002"})
        );
        let events = engine
            .events()
            .read(
                &alice(),
                &ReadEvents {
                    schema: Some("Invoice"),
                    instance_id: Some("i1"),
                    ..ReadEvents::default()
                },
            )
            .expect("events")
            .events;
        let kinds: Vec<&str> = events.iter().map(|event| event.kind.as_str()).collect();
        assert_eq!(kinds, ["create"]);
        assert_eq!(
            events[0].change["behaviors"]["Numbering"],
            json!({"seq": 1, "label": "INV-2026-0001"})
        );
    }
    // {yy} is the year's last two digits, {seq:1} pads to one digit and
    // prints 10 in full, and {seq} pads nothing.
    for target in targets("numbering_padded") {
        let padded = open(&target, 1_120_435_200_000);
        publish(
            &padded,
            &numbered(json!({"format": "{yy}/{seq:1}:{yyyy}"}), &[]),
        );
        let labels: Vec<Value> = (1..=11)
            .map(|index| create(&padded, &format!("i{index}"))["label"].clone())
            .collect();
        assert_eq!(
            json!([labels[0], labels[8], labels[9], labels[10]]),
            json!(["05/1:2005", "05/9:2005", "05/10:2005", "05/11:2005"])
        );
    }
    for target in targets("numbering_plain") {
        let plain = open(&target, 1_120_435_200_000);
        publish(&plain, &numbered(json!({"format": "No. {seq}"}), &[]));
        assert_eq!(create(&plain, "i1"), json!({"seq": 1, "label": "No. 1"}));
    }
}

#[test]
fn a_define_refuses_a_stray_brace_a_seq_not_there_once_a_yearly_reset_without_the_year_and_a_bad_scope(
) {
    for target in targets("numbering_refusals") {
        let engine = open(&target, MARCH_2026);
        let tokens = "{seq}, {seq:N} (N from 1 to 10), {yyyy} and {yy}";
        let refusals = [
            (json!({"format": "INV-{n}"}), format!("format \"INV-{{n}}\" has {{n}}, which is not one of its tokens: {tokens}")),
            (json!({"format": "{seq:0}"}), format!("format \"{{seq:0}}\" has {{seq:0}}, which is not one of its tokens: {tokens}")),
            (json!({"format": "{seq:04}"}), format!("format \"{{seq:04}}\" has {{seq:04}}, which is not one of its tokens: {tokens}")),
            (json!({"format": "{seq:11}"}), format!("format \"{{seq:11}}\" has {{seq:11}}, which is not one of its tokens: {tokens}")),
            (json!({"format": "{YYYY}-{seq}"}), format!("format \"{{YYYY}}-{{seq}}\" has {{YYYY}}, which is not one of its tokens: {tokens}")),
            (json!({"format": "INV-{seq"}), format!("format \"INV-{{seq\" has a \"{{\" that is not part of a token; its tokens are {tokens}")),
            (json!({"format": "{{seq}}"}), format!("format \"{{{{seq}}}}\" has a \"{{\" that is not part of a token; its tokens are {tokens}")),
            (json!({"format": "INV}{seq}"}), format!("format \"INV}}{{seq}}\" has a \"}}\" that is not part of a token; its tokens are {tokens}")),
            (json!({"format": "INV-{yyyy}"}), "format \"INV-{yyyy}\" has no {seq}: it prints the number with {seq}, or {seq:N} padded to N digits, once".to_owned()),
            (json!({"format": "{seq}-{seq:3}"}), "format \"{seq}-{seq:3}\" has the number 2 times: it prints it with {seq} or {seq:N} once, so two numbers never print one label".to_owned()),
            (
                json!({"format": "INV-{seq}", "reset": "yearly"}),
                "reset yearly counts each year from 1, so format \"INV-{seq}\" prints the year, {yyyy} or {yy}, or two years would print one label".to_owned(),
            ),
            (json!({"format": "{seq}", "scope": {"link": "ghost"}}), "scope.link ghost is not a link of the type's Links config (its links: customer, referrer)".to_owned()),
            (json!({"format": "{seq}", "scope": {"link": "referrer"}}), "scope.link referrer is not required, so an instance may hold no target to count by; make it required".to_owned()),
        ];
        for (config, message) in refusals {
            assert_eq!(
                config_issues(&engine, &numbered(config, &[])),
                [(
                    "/types/Invoice/behaviors/1/config".to_owned(),
                    format!("type Invoice: behavior Numbering config: {message}")
                )],
                "{message}"
            );
        }
        let unlinked = invoice(
            &[
                json!({"name": "Numbering", "config": {"format": "{seq}", "scope": {"link": "customer"}}}),
            ],
            false,
        );
        assert_eq!(
            config_issues(&engine, &unlinked),
            [(
                "/types/Invoice/behaviors/0/config".to_owned(),
                "type Invoice: behavior Numbering config: scope.link customer is a link of Links, which the type does not list".to_owned()
            )]
        );
        // What the declaration's config schema refuses, the loader refuses
        // first.
        for config in [
            json!({}),
            json!({"format": ""}),
            json!({"format": "{seq}", "reset": "monthly"}),
            json!({"format": "{seq}", "scope": {}}),
            json!({"format": "{seq}", "start": 100}),
        ] {
            let issues = config_issues(&engine, &numbered(config.clone(), &[]));
            assert!(
                issues[0].0.starts_with("/types/Invoice/behaviors/1"),
                "{config}: {issues:?}"
            );
        }
        let names: Vec<String> = engine
            .schemas()
            .list(&alice(), Default::default())
            .expect("list")
            .into_iter()
            .map(|schema| schema.name)
            .collect();
        assert_eq!(names, ["Customer"]);
    }
}

#[test]
fn a_scope_link_that_holds_many_targets_is_refused_read_from_the_links_config() {
    for target in targets("numbering_many_scope") {
        // An open meta-schema lets a Links config through that names many,
        // so Numbering's own check is what refuses it.
        let engine = target.open_with(behaviors(), |_| {});
        let mut document = invoice(
            &[
                json!({"name": "Numbering", "config": {"format": "{seq}", "scope": {"link": "customer"}}}),
            ],
            true,
        );
        document["types"]["Invoice"]["behaviors"][0]["config"]["links"]["customer"]["many"] =
            json!(true);
        let issues = config_issues(&engine, &document);
        assert!(
            issues.iter().any(|(path, message)| path == "/types/Invoice/behaviors/1/config"
                && message == "type Invoice: behavior Numbering config: scope.link customer holds many targets (many: true), so an instance has no one target to count by; name a link that holds one"),
            "{issues:?}"
        );
    }
}

#[test]
fn the_count_has_no_gap_a_refused_create_leaves_and_a_delete_leaves_one() {
    for target in targets("numbering_no_gap") {
        let engine = target.open_with(behaviors().with(Refuser), |options| {
            options.clock = Clock::Manual(MARCH_2026);
        });
        publish(&engine, &customers());
        for id in ["c1", "c2"] {
            engine
                .instances()
                .create(
                    &alice(),
                    "Customer",
                    &json!({ "name": id }),
                    CreateOptions {
                        id: Some(id),
                        ..Default::default()
                    },
                )
                .expect("customer");
        }
        publish(
            &engine,
            &numbered(
                json!({"format": "INV-{seq:3}"}),
                &[json!({"name": "test.Refuser"})],
            ),
        );
        assert_eq!(create(&engine, "i1"), json!({"seq": 1, "label": "INV-001"}));
        let (behavior, _, _, code, _) = veto(try_create(&engine, "gone", "c1", "refuse"));
        assert_eq!(
            (behavior.as_str(), code.as_deref()),
            ("test.Refuser", Some("refused"))
        );
        // An invalid create and a create Links refuses take none either.
        let invalid = refusal(engine.instances().create(
            &alice(),
            "Invoice",
            &json!({"note": "no title"}),
            CreateOptions {
                behaviors: Some(&json!({"Links": {"customer": "c1"}})),
                ..Default::default()
            },
        ));
        assert!(matches!(
            invalid.kind(),
            EngineErrorKind::InstanceValidation { .. }
        ));
        assert_eq!(
            refusal(try_create(&engine, "lost", "ghost", "lost")).code(),
            EngineErrorCode::InvalidArgument
        );
        assert_eq!(create(&engine, "i2"), json!({"seq": 2, "label": "INV-002"}));
        assert!(engine
            .instances()
            .delete(&alice(), "Invoice", "i2", WriteOptions::default())
            .expect("delete"));
        assert_eq!(create(&engine, "i3"), json!({"seq": 3, "label": "INV-003"}));
        // The deleted instance's label went with it; the count did not.
        assert!(every(&engine, json!({"Numbering.label": "INV-002"}))
            .0
            .is_empty());
    }
}

#[test]
fn a_scope_counts_per_target_of_its_link_and_moving_the_link_keeps_the_number() {
    for target in targets("numbering_scope") {
        let engine = open(&target, MARCH_2026);
        publish(
            &engine,
            &numbered(
                json!({"format": "C-{seq}", "scope": {"link": "customer"}}),
                &[],
            ),
        );
        assert_eq!(
            create_for(&engine, "a1", "c1"),
            json!({"seq": 1, "label": "C-1"})
        );
        assert_eq!(
            create_for(&engine, "a2", "c1"),
            json!({"seq": 2, "label": "C-2"})
        );
        assert_eq!(
            create_for(&engine, "b1", "c2"),
            json!({"seq": 1, "label": "C-1"})
        );
        assert_eq!(
            every(&engine, json!({"Numbering.label": "C-1"})).0,
            ["a1", "b1"]
        );
        invoke(
            &engine,
            &alice(),
            "a2",
            "link",
            json!({"name": "customer", "id": "c2"}),
        )
        .expect("link");
        assert_eq!(number_of(&engine, "a2"), json!({"seq": 2, "label": "C-2"}));
        // c2's count is its own: a2 was numbered in c1's.
        assert_eq!(
            create_for(&engine, "b2", "c2"),
            json!({"seq": 2, "label": "C-2"})
        );
        assert_eq!(
            create_for(&engine, "a3", "c1"),
            json!({"seq": 3, "label": "C-3"})
        );
    }
}

#[test]
fn a_yearly_count_starts_again_at_1_each_utc_year_per_scope_target_too() {
    for target in targets("numbering_yearly") {
        let engine = open(&target, 1_798_761_599_999);
        publish(
            &engine,
            &numbered(
                json!({"format": "INV-{yyyy}-{seq:4}", "scope": {"link": "customer"}, "reset": "yearly"}),
                &[],
            ),
        );
        assert_eq!(
            create_for(&engine, "i1", "c1")["label"],
            json!("INV-2026-0001")
        );
        assert_eq!(
            create_for(&engine, "i2", "c1")["label"],
            json!("INV-2026-0002")
        );
        assert_eq!(
            create_for(&engine, "i3", "c2")["label"],
            json!("INV-2026-0001")
        );
        engine.clock().set(1_798_761_600_000).expect("clock");
        assert_eq!(
            create_for(&engine, "i4", "c1"),
            json!({"seq": 1, "label": "INV-2027-0001"})
        );
        assert_eq!(
            create_for(&engine, "i5", "c1"),
            json!({"seq": 2, "label": "INV-2027-0002"})
        );
        assert_eq!(
            create_for(&engine, "i6", "c2"),
            json!({"seq": 1, "label": "INV-2027-0001"})
        );
    }
    // Without a reset the count runs on across the year; the label shows
    // the year.
    for target in targets("numbering_running") {
        let other = open(&target, 1_798_675_200_000);
        publish(&other, &numbered(json!({"format": "{yy}-{seq}"}), &[]));
        assert_eq!(create(&other, "j1")["label"], json!("26-1"));
        other.clock().set(1_798_761_600_000).expect("clock");
        assert_eq!(create(&other, "j2")["label"], json!("27-2"));
    }
}

#[test]
fn a_list_finds_an_instance_by_its_label_and_its_seq_each_through_its_index() {
    for target in targets("numbering_filters") {
        let engine = open(&target, MARCH_2026);
        publish(
            &engine,
            &numbered(json!({"format": "INV-{yyyy}-{seq:4}"}), &[]),
        );
        let total = FILTER_SCAN_ROWS + 10;
        for index in 1..=total {
            create(&engine, &format!("i{index:05}"));
        }
        // Without an index, the page would scan FILTER_SCAN_ROWS instances
        // and find none; through it, one page holds each match.
        assert_eq!(
            every(
                &engine,
                json!({"Numbering.label": format!("INV-2026-{}", FILTER_SCAN_ROWS + 5)})
            ),
            (vec!["i01005".to_owned()], vec![1])
        );
        assert_eq!(
            every(&engine, json!({"Numbering.seq": [FILTER_SCAN_ROWS + 7, 3]})),
            (vec!["i00003".to_owned(), "i01007".to_owned()], vec![2])
        );
        assert_eq!(
            every(&engine, json!({"Numbering.label": "INV-2026-9999"})),
            (Vec::new(), vec![0])
        );
        let wrong = refusal(engine.instances().list(
            &alice(),
            "Invoice",
            ListOptions {
                filter: Some(&json!({"Numbering.seq": "3"})),
                ..Default::default()
            },
        ));
        assert!(
            wrong
                .message()
                .contains("where.Numbering.seq is an integer"),
            "{wrong}"
        );
    }
}

#[test]
fn assign_number_numbers_an_instance_made_before_numbering_in_the_year_it_was_created() {
    for target in targets("numbering_assign") {
        let engine = open(&target, 1_748_736_000_000);
        publish(&engine, &invoice(&[], true));
        create(&engine, "o1");
        create(&engine, "o2");
        engine.clock().set(MARCH_2026).expect("clock");
        create(&engine, "o3");
        // Numbering joins a schema with instances, which hold no number yet.
        assert_eq!(
            publish(
                &engine,
                &numbered(
                    json!({"format": "INV-{yyyy}-{seq:3}", "reset": "yearly"}),
                    &[]
                )
            ),
            2
        );
        assert_eq!(number_of(&engine, "o1"), json!({}));
        assert_eq!(
            every(&engine, json!({"Numbering.seq": null})).0,
            ["o1", "o2", "o3"]
        );
        assert_eq!(
            create(&engine, "o4"),
            json!({"seq": 1, "label": "INV-2026-001"})
        );

        // The backfill, in creation order.
        let assign = |id: &str| invoke(&engine, &alice(), id, "assignNumber", json!({}));
        assert_eq!(
            assign("o1").expect("assign"),
            json!({"seq": 1, "label": "INV-2025-001"})
        );
        assert_eq!(
            assign("o2").expect("assign"),
            json!({"seq": 2, "label": "INV-2025-002"})
        );
        assert_eq!(
            assign("o3").expect("assign"),
            json!({"seq": 2, "label": "INV-2026-002"})
        );
        let events = |id: &str| {
            engine
                .events()
                .read(
                    &alice(),
                    &ReadEvents {
                        schema: Some("Invoice"),
                        instance_id: Some(id),
                        ..ReadEvents::default()
                    },
                )
                .expect("events")
                .events
        };
        assert_eq!(
            events("o1").last().expect("an event").change,
            json!({
                "behavior": "Numbering",
                "operation": "assignNumber",
                "params": {},
                "patch": {"behaviors": {"Numbering": {"seq": 1, "label": "INV-2025-001"}}}
            })
        );
        assert!(every(&engine, json!({"Numbering.seq": null})).0.is_empty());

        // On one numbered already it changes nothing: no event, the same
        // seq.
        let read = || {
            engine
                .instances()
                .get(&alice(), "Invoice", "o4", Default::default())
                .expect("get")
                .expect("o4")
        };
        let before = read();
        assert_eq!(
            assign("o4").expect("assign"),
            json!({"seq": 1, "label": "INV-2026-001"})
        );
        let after = read();
        assert_eq!(
            (after.seq, after.updated_at),
            (before.seq, before.updated_at)
        );
        assert_eq!(events("o4").len(), 1);
    }
}

#[test]
fn nothing_changes_a_number_once_set() {
    for target in targets("numbering_fixed") {
        let engine = open(&target, MARCH_2026);
        publish(&engine, &numbered(json!({"format": "INV-{seq}"}), &[]));
        create(&engine, "i1");
        let refused = refusal(engine.instances().update(
            &alice(),
            "Invoice",
            "i1",
            &json!({"Numbering": {"seq": 9}}),
            WriteOptions::default(),
        ));
        assert!(refused.message().contains("Numbering"), "{refused}");
        engine
            .instances()
            .update(
                &alice(),
                "Invoice",
                "i1",
                &json!({"note": "paid"}),
                WriteOptions::default(),
            )
            .expect("update");
        invoke(
            &engine,
            &alice(),
            "i1",
            "link",
            json!({"name": "referrer", "id": "c2"}),
        )
        .expect("link");
        invoke(&engine, &alice(), "i1", "assignNumber", json!({})).expect("assign");
        assert_eq!(
            number_of(&engine, "i1"),
            json!({"seq": 1, "label": "INV-1"})
        );
        let events = engine
            .events()
            .read(
                &alice(),
                &ReadEvents {
                    schema: Some("Invoice"),
                    instance_id: Some("i1"),
                    ..ReadEvents::default()
                },
            )
            .expect("events")
            .events;
        for event in events.iter().skip(1) {
            if event.kind == "operation" {
                assert_eq!(
                    event.change["patch"]["behaviors"].get("Numbering"),
                    None,
                    "{}",
                    event.change
                );
            }
        }
    }
}

#[test]
fn while_the_schema_has_instances_the_format_scope_and_reset_stay_and_with_none_anything_may_change(
) {
    for target in targets("numbering_versions") {
        let engine = open(&target, MARCH_2026);
        publish(
            &engine,
            &numbered(
                json!({"format": "INV-{yyyy}-{seq}", "scope": {"link": "customer"}}),
                &[],
            ),
        );
        create(&engine, "i1");
        for (config, message) in [
            (
                json!({"format": "INV-{seq}", "scope": {"link": "customer"}}),
                "the new config changes the format from \"INV-{yyyy}-{seq}\" to \"INV-{seq}\"",
            ),
            (
                json!({"format": "INV-{yyyy}-{seq}"}),
                "the new config changes the scope from link customer to none",
            ),
            (
                json!({"format": "INV-{yyyy}-{seq}", "scope": {"link": "customer"}, "reset": "yearly"}),
                "the new config changes the reset from none to yearly",
            ),
        ] {
            let error = refusal(try_publish(&engine, &numbered(config, &[])));
            let paths: Vec<&str> = error
                .changes()
                .iter()
                .map(|change| change.path.as_str())
                .collect();
            assert_eq!(paths, ["Invoice.behaviors.Numbering"]);
            assert!(
                error.changes()[0].message.contains(message),
                "{}",
                error.changes()[0].message
            );
        }
        let removed = refusal(try_publish(&engine, &invoice(&[], true)));
        assert!(removed.changes()[0].message.contains(
            "the instances would lose their numbers, and Numbering added again would count from 1"
        ));

        // A new version that keeps the config keeps the counts.
        let mut next = numbered(
            json!({"format": "INV-{yyyy}-{seq}", "scope": {"link": "customer"}}),
            &[],
        );
        next["types"]["Invoice"]["description"] = json!("An invoice to a customer.");
        assert_eq!(publish(&engine, &next), 2);
        assert_eq!(create(&engine, "i2")["label"], json!("INV-2026-2"));

        // With none, the format may change, and the count starts at 1.
        for id in ["i1", "i2"] {
            engine
                .instances()
                .delete(&alice(), "Invoice", id, WriteOptions::default())
                .expect("delete");
        }
        assert_eq!(
            publish(&engine, &numbered(json!({"format": "N-{seq:2}"}), &[])),
            3
        );
        assert_eq!(create(&engine, "i3"), json!({"seq": 1, "label": "N-01"}));
        engine
            .instances()
            .delete(&alice(), "Invoice", "i3", WriteOptions::default())
            .expect("delete");
        // Numbering may go, and comes back counting from 1.
        assert_eq!(publish(&engine, &invoice(&[], true)), 4);
        create(&engine, "i4");
        assert_eq!(
            publish(&engine, &numbered(json!({"format": "N-{seq:2}"}), &[])),
            5
        );
        assert_eq!(number_of(&engine, "i4"), json!({}));
        assert_eq!(create(&engine, "i5"), json!({"seq": 1, "label": "N-01"}));
        assert_eq!(
            invoke(&engine, &alice(), "i4", "assignNumber", json!({})).expect("assign"),
            json!({"seq": 2, "label": "N-02"})
        );
    }
}

fn invoice_target(operations: &[&str]) -> DescribeTarget {
    DescribeTarget {
        schema: "Invoice".to_owned(),
        type_name: "Invoice".to_owned(),
        behaviors: vec!["Links".to_owned(), "Numbering".to_owned()],
        configs: serde_json::Map::new(),
        operations: operations
            .iter()
            .map(|name| DescribedTypeOperation {
                name: (*name).to_owned(),
                behavior: None,
                writes: true,
                beside: false,
                scope: OperationScope::Instance,
            })
            .collect(),
    }
}

#[test]
fn its_guidance_prints_an_example_of_the_format_and_says_what_it_counts_by() {
    let guidance = Numbering.hooks().guidance.expect("guidance");
    let scoped = NumberingConfig {
        format: "INV-{yyyy}-{seq:4}".to_owned(),
        scope: Some("customer".to_owned()),
        yearly: true,
    };
    let said = guidance(
        &Numbering,
        &scoped,
        &invoice_target(&["link", "assignNumber"]),
    );
    assert_eq!(
        said.summary,
        "Each Invoice gets a number at its create, in a count per target of its customer link, starting again at 1 each UTC year, printed as INV-{yyyy}-{seq:4}: number 42, made in 2026, prints INV-2026-0042. A label is unique among one customer target's instances. A refused create takes no number, a delete leaves a gap, and nothing changes a number once set."
    );
    let operation = |name: &str| {
        said.operations
            .iter()
            .find(|(operation, _)| operation == name)
            .map(|(_, guidance)| guidance.clone())
            .expect("the operation's guidance")
    };
    assert!(operation("list").use_when.unwrap_or_default().contains("where: { \"Numbering.label\": \"INV-2026-0042\" } finds an instance by the number a person reads, and \"Numbering.seq\" by its number. Each customer target counts from 1"));
    assert!(operation("create").success.unwrap_or_default().contains("holds behaviors.Numbering.seq, the next number of its count, and label, the number as INV-{yyyy}-{seq:4} prints it. It is counted in the count of the customer target the create gives."));
    assert!(operation("assignNumber")
        .use_when
        .unwrap_or_default()
        .contains("Use on an instance created before Invoice composed Numbering"));
    assert!(operation("assignNumber").success.unwrap_or_default().contains(
        "counted as its create would have counted them, by its customer target, in the year it was created"
    ));
    assert!(operation("update")
        .do_not_use_when
        .unwrap_or_default()
        .contains("Do not use to change a number"));
    assert!(operation("link")
        .success
        .unwrap_or_default()
        .contains("Moving customer keeps the instance's number"));

    let plain = NumberingConfig {
        format: "No. {seq}".to_owned(),
        scope: None,
        yearly: false,
    };
    assert_eq!(
        guidance(&Numbering, &plain, &invoice_target(&[])).summary,
        "Each Invoice gets a number at its create, in one count for Invoice, printed as No. {seq}: number 42 prints No. 42. A label is unique. A refused create takes no number, a delete leaves a gap, and nothing changes a number once set."
    );
}

#[test]
fn beside_revisions_an_approved_proposal_keeps_the_number() {
    for target in targets("numbering_revisions") {
        let engine = open(&target, MARCH_2026);
        publish(
            &engine,
            &numbered(
                json!({"format": "INV-{seq}"}),
                &[
                    json!({"name": "Revisions", "config": {"review": {"permission": "invoices.review"}}}),
                ],
            ),
        );
        create(&engine, "i1");
        invoke(
            &engine,
            &alice(),
            "i1",
            "propose",
            json!({"patch": {"note": "net 30"}}),
        )
        .expect("propose");
        invoke(
            &engine,
            &Principal::new("rae", ["invoices.review"]),
            "i1",
            "approve",
            json!({"proposal": 1}),
        )
        .expect("approve");
        let read = engine
            .instances()
            .get(&alice(), "Invoice", "i1", Default::default())
            .expect("get")
            .expect("i1");
        assert_eq!(
            json!([
                read.data.get("note"),
                read.behaviors["Numbering"],
                read.behaviors["Revisions"]["revision"]
            ]),
            json!(["net 30", {"seq": 1, "label": "INV-1"}, 2])
        );
        let revisions =
            invoke(&engine, &alice(), "i1", "listRevisions", json!({})).expect("listRevisions");
        let data: Vec<Value> = revisions["items"]
            .as_array()
            .expect("items")
            .iter()
            .map(|item| item["data"].clone())
            .collect();
        assert_eq!(
            data,
            [
                json!({"title": "i1"}),
                json!({"title": "i1", "note": "net 30"})
            ]
        );
        assert_eq!(create(&engine, "i2"), json!({"seq": 2, "label": "INV-2"}));
    }
}
