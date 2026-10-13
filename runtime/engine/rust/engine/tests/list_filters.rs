//! A list's where, the cases of `test/list-filters.test.ts` on the instance
//! type's own fields: equality by key, a list of values, null for no
//! value, paging with the cursor in creation order, a page read through an
//! index, a page that scans a bounded number of instances when none serves
//! it, and what where refuses; on SQLite and, with the variable set, on
//! Postgres, where every comparison repeats the index's expression.
//!
//! milestone 5: a behavior's filters (Workflow.status, a behavior's index
//! on its column, which serves null as one more range).

mod support;

use serde_json::{json, Value};

use superschematic_engine::storage::{Lock, SqlValue};
use superschematic_engine::{
    CreateOptions, DefineOptions, Engine, EngineErrorCode, ListOptions, SchemaTarget,
    FILTER_SCAN_ROWS,
};
use support::{alice, refusal, schema_document, targets};

fn job_document() -> Value {
    schema_document(
        "Job",
        json!([
            {"name": "slug", "typeRef": {"name": "string"}, "unique": true},
            {"name": "kind", "typeRef": {"name": "string"}},
            {"name": "rank", "typeRef": {"name": "Generic.Int64"}},
            {"name": "urgent", "typeRef": {"name": "boolean"}},
            {"name": "tags", "typeRef": {"name": "string", "isArray": true}},
            {"name": "owner", "typeRef": {"name": "Owner"}}
        ]),
        json!({"types": {"Owner": {"name": "Owner", "role": "EmbeddedStruct", "fields": [{"name": "name", "typeRef": {"name": "string"}}]}}}),
    )
}

fn open(target: &support::Target) -> Engine {
    let engine = target.open();
    engine
        .schemas()
        .define(&alice(), job_document(), DefineOptions::default())
        .unwrap();
    engine
        .schemas()
        .publish(&alice(), "Job", SchemaTarget::default())
        .unwrap();
    engine
}

fn job_id(index: usize) -> String {
    format!("j{index:05}")
}

// many creates count jobs through the engine, each from make.
fn many(engine: &Engine, count: usize, make: impl Fn(usize) -> Value) {
    for index in 0..count {
        engine
            .instances()
            .create(
                &alice(),
                "Job",
                &make(index),
                CreateOptions {
                    id: Some(&job_id(index)),
                    ..CreateOptions::default()
                },
            )
            .unwrap();
    }
}

// bulk writes count jobs' rows in one transaction, as a create stores
// them, for the tests that need thousands: a filter reads rows alone.
fn bulk(engine: &Engine, count: usize, make: impl Fn(usize) -> Value) {
    engine
        .db()
        .write(Lock::Catalog, |tx| {
            for index in 0..count {
                tx.run(
                    "INSERT INTO engine_instances (namespace, schema, id, schema_namespace, version, seq, data, created_at, created_by, updated_at, updated_by)
                     VALUES ('default', 'Job', ?, 'default', 1, 1, ?, 1, 'alice', 1, 'alice')",
                    &[
                        SqlValue::from(job_id(index)),
                        SqlValue::from(superschematic_engine::json::stringify(&make(index))),
                    ],
                )?;
            }
            Ok::<_, superschematic_engine::storage::Error>(())
        })
        .unwrap();
}

/// Every page of a filter from the start: the ids in order, and how many
/// items each page held.
fn every(engine: &Engine, filter: &Value, limit: Option<i64>) -> (Vec<String>, Vec<usize>) {
    let mut ids = Vec::new();
    let mut pages = Vec::new();
    let mut cursor: Option<String> = None;
    loop {
        let page = engine
            .instances()
            .list(
                &alice(),
                "Job",
                ListOptions {
                    filter: Some(filter),
                    limit,
                    cursor: cursor.as_deref(),
                    ..ListOptions::default()
                },
            )
            .unwrap();
        pages.push(page.items.len());
        ids.extend(page.items.into_iter().map(|item| item.id));
        match page.next {
            Some(next) => cursor = Some(next),
            None => return (ids, pages),
        }
    }
}

fn ids_of(list: &[usize]) -> Vec<String> {
    list.iter().map(|index| job_id(*index)).collect()
}

#[test]
fn where_keeps_the_instances_whose_fields_hold_its_values_a_list_meaning_any_every_member_together()
{
    for target in targets("where_keeps_the_instances_whose_fields_hold_its_values") {
        let engine = open(&target);
        many(
            &engine,
            8,
            |index| json!({"slug": format!("org/job-{index}"), "kind": if index % 2 == 0 { "build" } else { "test" }, "rank": index % 3, "urgent": index < 2}),
        );
        let ids = |filter: Value| every(&engine, &filter, None).0;
        assert_eq!(ids(json!({"kind": "build"})), ids_of(&[0, 2, 4, 6]));
        assert_eq!(ids(json!({"kind": ["test"], "rank": 0})), ids_of(&[3]));
        assert_eq!(ids(json!({"rank": [2, 0]})), ids_of(&[0, 2, 3, 5, 6]));
        assert_eq!(ids(json!({"rank": 1.0})), ids_of(&[1, 4, 7]));
        assert_eq!(ids(json!({"urgent": true})), ids_of(&[0, 1]));
        assert_eq!(
            ids(json!({"urgent": false, "kind": "build"})),
            ids_of(&[2, 4, 6])
        );
        assert_eq!(
            ids(json!({"slug": ["org/job-6", "org/job-1", "org/missing"]})),
            ids_of(&[1, 6])
        );
        assert_eq!(
            ids(json!({"slug": "org/job-1", "kind": "build"})),
            Vec::<String>::new()
        );
        assert_eq!(ids(json!({"kind": "deploy"})), Vec::<String>::new());
        // An empty where, or none, lists every instance.
        assert_eq!(ids(json!({})).len(), 8);
        // valueRefs and the rest of a read work as an unfiltered list's.
        let filter = json!({"kind": "test", "urgent": true});
        let page = engine
            .instances()
            .list(
                &alice(),
                "Job",
                ListOptions {
                    filter: Some(&filter),
                    value_refs: true,
                    ..ListOptions::default()
                },
            )
            .unwrap();
        assert_eq!(
            (
                Value::Object(page.items[0].data.clone()),
                Value::Object(page.items[0].behaviors.clone())
            ),
            (
                json!({"slug": "org/job-1", "kind": "test", "rank": 1, "urgent": true}),
                json!({})
            )
        );
    }
}

#[test]
fn pages_keep_creation_order_across_the_cursor_a_list_of_values_merged() {
    for target in targets("pages_keep_creation_order_across_the_cursor") {
        let engine = open(&target);
        many(
            &engine,
            30,
            |index| json!({"slug": format!("s{index}"), "kind": (["a", "b", "c"][index % 3])}),
        );
        let expected: Vec<usize> = (0..30).filter(|index| index % 3 != 1).collect();
        let (ids, pages) = every(&engine, &json!({"kind": ["c", "a"]}), Some(4));
        assert_eq!(ids, ids_of(&expected));
        assert_eq!(pages, [4, 4, 4, 4, 4]);
        // Through the unique index on slug, a range per value.
        let slugs: Vec<String> = expected.iter().map(|index| format!("s{index}")).collect();
        let (ids, pages) = every(&engine, &json!({"slug": slugs}), Some(4));
        assert_eq!(ids, ids_of(&expected));
        assert_eq!(pages, [4, 4, 4, 4, 4]);
        // An instance created behind the cursor moves nothing between pages.
        let filter = json!({"kind": "a"});
        let first = engine
            .instances()
            .list(
                &alice(),
                "Job",
                ListOptions {
                    filter: Some(&filter),
                    limit: Some(2),
                    ..ListOptions::default()
                },
            )
            .unwrap();
        engine
            .instances()
            .create(
                &alice(),
                "Job",
                &json!({"slug": "late", "kind": "a"}),
                CreateOptions {
                    id: Some("late"),
                    ..CreateOptions::default()
                },
            )
            .unwrap();
        let mut rest = Vec::new();
        let mut cursor = first.next.clone();
        while let Some(next) = cursor {
            let page = engine
                .instances()
                .list(
                    &alice(),
                    "Job",
                    ListOptions {
                        filter: Some(&filter),
                        cursor: Some(&next),
                        ..ListOptions::default()
                    },
                )
                .unwrap();
            rest.extend(page.items.into_iter().map(|item| item.id));
            cursor = page.next;
        }
        assert_eq!(
            &rest[rest.len() - 2..],
            ["j00027".to_owned(), "late".to_owned()]
        );
    }
}

#[test]
fn a_field_an_index_serves_reads_only_the_instances_that_hold_its_values_another_fields_page_scans_a_bounded_number(
) {
    for target in targets("a_field_an_index_serves_reads_only_the_instances_that_hold_its_values") {
        let engine = open(&target);
        let total = FILTER_SCAN_ROWS * 2 + 500;
        let rare = [10, FILTER_SCAN_ROWS + 500, FILTER_SCAN_ROWS * 2 + 400];
        bulk(
            &engine,
            total,
            |index| json!({"slug": format!("s{index}"), "kind": if rare.contains(&index) { "rare" } else { "common" }}),
        );
        let ids = ids_of(&rare);
        // The unique index on slug reads the three in one page, a
        // combination at a time.
        let slugs: Vec<String> = rare.iter().map(|index| format!("s{index}")).collect();
        assert_eq!(
            every(&engine, &json!({"slug": slugs}), None),
            (ids.clone(), vec![3])
        );
        // No index serves kind: each page reads FILTER_SCAN_ROWS instances
        // past its cursor, and keeps the ones that match.
        assert_eq!(
            every(&engine, &json!({"kind": "rare"}), None),
            (ids.clone(), vec![1, 1, 1])
        );
        assert_eq!(
            every(&engine, &json!({"kind": "none"}), None),
            (Vec::new(), vec![0, 0, 0])
        );
        // A member no index serves beside one an index does is tested on
        // the rows the index reads.
        assert_eq!(
            every(
                &engine,
                &json!({"kind": "rare", "slug": format!("s{}", FILTER_SCAN_ROWS + 500)}),
                None
            )
            .0,
            [ids[1].clone()]
        );
    }
}

#[test]
fn null_keeps_the_instances_whose_field_holds_no_value_and_an_own_index_partial_on_a_value_serves_none(
) {
    for target in targets("null_keeps_the_instances_whose_field_holds_no_value") {
        let engine = open(&target);
        for (id, kind) in [
            ("a", Some("build")),
            ("b", None),
            ("c", Some("test")),
            ("d", None),
            ("e", Some("build")),
        ] {
            let mut data = json!({"slug": id});
            if let Some(kind) = kind {
                data["kind"] = json!(kind);
            }
            engine
                .instances()
                .create(
                    &alice(),
                    "Job",
                    &data,
                    CreateOptions {
                        id: Some(id),
                        ..CreateOptions::default()
                    },
                )
                .unwrap();
        }
        let ids = |filter: Value| every(&engine, &filter, None).0;
        assert_eq!(ids(json!({"kind": null})), ["b", "d"]);
        assert_eq!(ids(json!({"kind": [null, "test"]})), ["b", "c", "d"]);

        let bulky = target.fresh();
        let engine = open(&bulky);
        let total = FILTER_SCAN_ROWS * 2 + 500;
        let bare = [10, FILTER_SCAN_ROWS + 500, FILTER_SCAN_ROWS * 2 + 400];
        bulk(&engine, total, |index| {
            if bare.contains(&index) {
                json!({"kind": "x"})
            } else {
                json!({"slug": job_id(index), "kind": "x"})
            }
        });
        let ids = ids_of(&bare);
        assert_eq!(
            every(&engine, &json!({"slug": null}), None),
            (ids.clone(), vec![1, 1, 1])
        );
        // With a value beside null, the unique index still serves no member.
        let mut with_first = vec![job_id(0)];
        with_first.extend(ids);
        assert_eq!(
            every(&engine, &json!({"slug": [null, job_id(0)]}), None).0,
            with_first
        );
    }
}

#[test]
fn where_refuses_a_field_it_does_not_filter_on_a_value_of_another_type_and_an_empty_or_long_list() {
    for target in targets("where_refuses_a_field_it_does_not_filter_on") {
        let engine = open(&target);
        let refused = |filter: Value, message: &str| {
            let error = refusal(engine.instances().list(
                &alice(),
                "Job",
                ListOptions {
                    filter: Some(&filter),
                    ..ListOptions::default()
                },
            ));
            assert_eq!(error.code(), EngineErrorCode::InvalidArgument);
            assert!(error.message().contains(message), "{error}");
        };
        refused(json!({"tags": "a"}), "where names tags, which is not a field Job filters on; it filters on slug, kind, rank, urgent: an own field");
        refused(
            json!({"status": "todo"}),
            "where names status, which is not a field Job filters on",
        );
        refused(json!({"owner": {"name": "x"}}), "where names owner");
        refused(json!({"title": "x"}), "where names title");
        refused(
            json!({"kind": 1}),
            "where.kind is a string or null, or a list of them, not 1",
        );
        refused(
            json!({"rank": 1.5}),
            "where.rank is an integer or null, or a list of them, not 1.5",
        );
        refused(json!({"urgent": "yes"}), "where.urgent is a boolean");
        refused(
            json!({"kind": []}),
            "where.kind lists 1 to 100 values, got 0",
        );
        let many: Vec<String> = (0..101).map(|index| format!("k{index}")).collect();
        refused(json!({"kind": many}), "lists 1 to 100 values, got 101");
        refused(json!({"kind": ["a", 3]}), "where.kind is a string");
        refused(json!(["kind"]), "where is a JSON object");
    }
}
