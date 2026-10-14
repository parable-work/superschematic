//! Search's vectors and the search across schemas: the cases of
//! `test/core-search-vectors.test.ts` (an outside embedder pulls the
//! instances whose text has no vector and settles their vectors, which
//! skips an instance whose text moved since and refuses a vector the
//! config's dimensions or a 32-bit float cannot hold, with no event; a
//! search by vector ranks by cosine and one by a query and a vector fuses
//! the two rankings by reciprocal rank; similar leaves the instance out and
//! ranks a draft's text and vector the same way; a version that changes
//! the model, the dimensions or the fields makes every vector stale; the
//! search across schemas, and its HTTP route and MCP tool), on SQLite and,
//! with the variable set, on Postgres (D58).
//!
//! Vectors are bytes and Rust over them, the same on both databases, and
//! these cases' text rankings agree on both (the more matches first, then
//! the row), so each holds Postgres to the same ids, places and scores;
//! the snippets are SQLite's alone.

mod support;

use serde_json::{json, Map, Value};

use superschematic_engine::api::{handle, ApiOptions, Caller, Request, Response};
use superschematic_engine::storage::Dialect;
use superschematic_engine::{
    CreateOptions, DefineOptions, Engine, EngineErrorCode, Error, NamespaceOptions, Policy,
    Principal, ReadEvents, SchemaTarget, WriteOptions,
};
use superschematic_engine_behaviors::RRF_K;
use support::{
    alice, code, issue, issues_of, named, refusal, schema_document, targets, RecordingPolicy,
    Target,
};

fn embedder() -> Principal {
    Principal::new("embedder", vec!["notes.embed".to_owned()])
}

fn bob() -> Principal {
    named("bob")
}

fn vectors() -> Value {
    json!({"dimensions": 3, "model": "test-3d", "permission": "notes.embed"})
}

fn search_config() -> Value {
    json!({"name": "Search", "config": {"fields": ["title", "body"], "weights": {"title": 3}, "vectors": vectors()}})
}

// notes is a schema of notes: a title, a body, and an author Search does
// not index.
fn notes_named(behaviors: Value, name: &str) -> Value {
    let mut document = schema_document(
        name,
        json!([
            {"name": "title", "typeRef": {"name": "string"}, "required": true},
            {"name": "body", "typeRef": {"name": "string"}},
            {"name": "author", "typeRef": {"name": "Contact.Email"}}
        ]),
        json!({}),
    );
    document["types"][name]["behaviors"] = behaviors;
    document
}

fn notes(behaviors: Value) -> Value {
    notes_named(behaviors, "Note")
}

fn publish(engine: &Engine, document: &Value) -> i64 {
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
        .expect("publish")
        .version
}

fn create_in(engine: &Engine, schema: &str, data: Value, id: &str, namespace: Option<&str>) {
    engine
        .instances()
        .create(
            &alice(),
            schema,
            &data,
            CreateOptions {
                id: Some(id),
                namespace,
                ..Default::default()
            },
        )
        .expect("create");
}

fn create(engine: &Engine, data: Value, id: &str) {
    create_in(engine, "Note", data, id, None);
}

fn update(engine: &Engine, id: &str, patch: Value) {
    engine
        .instances()
        .update(&alice(), "Note", id, &patch, WriteOptions::default())
        .expect("update");
}

fn call_as(
    engine: &Engine,
    principal: &Principal,
    schema: &str,
    operation: &str,
    params: Value,
) -> Result<Value, Error> {
    engine
        .instances()
        .invoke_schema(principal, schema, operation, &params, None)
}

fn call(engine: &Engine, operation: &str, params: Value) -> Value {
    call_as(engine, &alice(), "Note", operation, params).expect(operation)
}

fn stale(engine: &Engine, params: Value) -> Value {
    call(engine, "staleEmbeddings", params)
}

fn stale_ids(engine: &Engine) -> Vec<String> {
    ids(&stale(engine, json!({})))
}

fn settle(engine: &Engine, items: Value, principal: &Principal) -> Result<Value, Error> {
    call_as(
        engine,
        principal,
        "Note",
        "settleEmbeddings",
        json!({ "items": items }),
    )
}

// settle_all settles a vector for every stale instance the map names.
fn settle_all(engine: &Engine, vectors: &[(&str, Value)], schema: &str) -> Result<Value, Error> {
    let pulled = call_as(
        engine,
        &alice(),
        schema,
        "staleEmbeddings",
        json!({"limit": 100}),
    )?;
    let items: Vec<Value> = pulled["items"]
        .as_array()
        .expect("items")
        .iter()
        .filter_map(|item| {
            let id = item["id"].as_str()?;
            let vector = vectors.iter().find(|(named, _)| *named == id)?.1.clone();
            Some(json!({"id": id, "sourceHash": item["sourceHash"], "vector": vector}))
        })
        .collect();
    call_as(
        engine,
        &embedder(),
        schema,
        "settleEmbeddings",
        json!({ "items": items }),
    )
}

fn search(engine: &Engine, params: Value) -> Value {
    call(engine, "search", params)
}

fn ids(page: &Value) -> Vec<String> {
    page["items"]
        .as_array()
        .expect("items")
        .iter()
        .map(|hit| hit["id"].as_str().expect("id").to_owned())
        .collect()
}

fn rrf(places: &[i64]) -> f64 {
    places
        .iter()
        .map(|place| 1.0 / (RRF_K + place) as f64)
        .sum()
}

fn number(value: &Value) -> Option<f64> {
    value.as_f64()
}

// rows_of reads Search's own record of each instance's vector: its id,
// whether it is hashed, whether it is embedded, and who settled it.
fn rows_of(engine: &Engine) -> Vec<(String, bool, bool, Option<String>)> {
    engine
        .db()
        .read(|tx| {
            tx.all(
                "SELECT id, source_hash, vector, settled_by FROM bhv_search__rows WHERE schema = 'Note' ORDER BY row",
                &[],
            )?
            .iter()
            .map(|row| {
                Ok((
                    row.text("id")?.to_owned(),
                    row.opt_text("source_hash")?.is_some(),
                    row.opt_blob("vector")?.is_some(),
                    row.opt_text("settled_by")?.map(str::to_owned),
                ))
            })
            .collect::<Result<Vec<_>, superschematic_engine::storage::Error>>()
        })
        .expect("rows")
}

fn last_cursor(engine: &Engine) -> i64 {
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

fn desks(target: &Target) -> Engine {
    let engine = target.open_core();
    publish(&engine, &notes(json!([search_config()])));
    create(
        &engine,
        json!({"title": "Walnut desk", "body": "A desk of solid wood."}),
        "n1",
    );
    create(&engine, json!({"title": "Oak chair"}), "n2");
    create(&engine, json!({"title": "  ", "body": "\n"}), "blank");
    engine
}

#[test]
fn an_embedder_pulls_the_text_of_each_instance_without_a_vector_and_settles_the_vectors_with_no_event(
) {
    for target in targets("vectors_pull") {
        let engine = desks(&target);
        let pulled = stale(&engine, json!({}));
        assert_eq!(
            (&pulled["model"], &pulled["dimensions"], &pulled["next"]),
            (&json!("test-3d"), &json!(3), &Value::Null)
        );
        let texts: Vec<(String, String)> = pulled["items"]
            .as_array()
            .expect("items")
            .iter()
            .map(|item| {
                let hash = item["sourceHash"].as_str().expect("a hash");
                assert!(
                    hash.len() == 64 && hash.bytes().all(|b| b.is_ascii_hexdigit()),
                    "{hash}"
                );
                (
                    item["id"].as_str().expect("id").to_owned(),
                    item["text"].as_str().expect("text").to_owned(),
                )
            })
            .collect();
        // Text that holds nothing wants no vector.
        assert_eq!(
            texts,
            [
                (
                    "n1".to_owned(),
                    "Walnut desk\n\nA desk of solid wood.".to_owned()
                ),
                ("n2".to_owned(), "Oak chair".to_owned())
            ]
        );
        let first = stale(&engine, json!({"limit": 1}));
        assert_eq!(ids(&first), ["n1"]);
        assert_eq!(
            ids(&stale(
                &engine,
                json!({"limit": 1, "cursor": first["next"]})
            )),
            ["n2"]
        );

        let cursor = last_cursor(&engine);
        let seq = engine
            .instances()
            .get(&alice(), "Note", "n1", Default::default())
            .expect("get")
            .expect("n1")
            .seq;
        assert_eq!(
            settle_all(
                &engine,
                &[("n1", json!([1, 0, 0])), ("n2", json!([0, 2, 0]))],
                "Note"
            )
            .expect("settle"),
            json!({"settled": 2, "skipped": []})
        );
        assert!(stale_ids(&engine).is_empty());
        assert_eq!(last_cursor(&engine), cursor, "a settle appends no event");
        let n1 = engine
            .instances()
            .get(&alice(), "Note", "n1", Default::default())
            .expect("get")
            .expect("n1");
        assert_eq!(n1.seq, seq, "and moves no instance's sequence");
        assert_eq!(
            Value::Object(n1.data),
            json!({"title": "Walnut desk", "body": "A desk of solid wood."})
        );
        let embedder = Some("embedder".to_owned());
        assert_eq!(
            rows_of(&engine),
            [
                ("n1".to_owned(), true, true, embedder.clone()),
                ("n2".to_owned(), true, true, embedder),
                ("blank".to_owned(), false, false, None)
            ]
        );
        // A vector is stored at unit length: n2's ranks with similarity 1.
        let found = search(&engine, json!({"vector": [0, 1, 0]}));
        assert_eq!(found["items"][0]["id"], json!("n2"));
        assert_eq!(found["items"][0]["rank"], json!(1));
        assert_eq!(number(&found["items"][0]["score"]), Some(rrf(&[1])));
        assert_eq!(
            number(&found["items"][0]["vector"]["similarity"]),
            Some(1.0)
        );
    }
}

#[test]
fn a_settle_refuses_a_wrong_dimension_a_value_a_float_cannot_hold_all_zeros_and_an_id_twice() {
    for target in targets("vectors_refused") {
        let engine = desks(&target);
        let pulled = stale(&engine, json!({}));
        let n1 = &pulled["items"][0];
        let n2 = &pulled["items"][1];
        let item = |pulled: &Value, vector: Value| json!({"id": pulled["id"], "sourceHash": pulled["sourceHash"], "vector": vector});
        let refused = |items: Value| issues_of(&refusal(settle(&engine, items, &embedder())));
        assert_eq!(
            refused(json!([item(n2, json!([0, 1, 0])), item(n1, json!([1, 0]))])),
            [issue(
                "/items/1/vector",
                "holds 2 numbers, and the config's vectors hold 3"
            )]
        );
        assert_eq!(
            refused(json!([item(n1, json!([1, 1e39, 0]))])),
            [issue(
                "/items/0/vector",
                "holds 1e+39 at 1, which a 32-bit float cannot hold"
            )]
        );
        assert_eq!(
            refused(json!([item(n1, json!([0, 0, 0]))])),
            [issue(
                "/items/0/vector",
                "is all zeros, which has no direction to compare"
            )]
        );
        assert_eq!(
            refused(json!([
                item(n1, json!([1, 0, 0])),
                item(n1, json!([0, 1, 0]))
            ])),
            [issue("/items/1/id", "settles n1 a second time in one call")]
        );
        assert_eq!(
            code(settle(&engine, json!([]), &embedder())),
            EngineErrorCode::InvalidArgument
        );
        assert_eq!(
            stale_ids(&engine),
            ["n1", "n2"],
            "a refused batch stores none of its vectors"
        );
        // It needs the permission the config names, and read on the schema.
        let forbidden = refusal(settle(
            &engine,
            json!([item(n1, json!([1, 0, 0]))]),
            &alice(),
        ));
        assert_eq!(
            (forbidden.code(), forbidden.message()),
            (
                EngineErrorCode::Forbidden,
                "alice may not settle the vectors of Note in namespace default: it needs notes.embed"
            )
        );
    }
}

#[test]
fn a_settle_skips_an_instance_whose_text_moved_since_it_was_pulled_and_one_that_is_gone() {
    for target in targets("vectors_moved") {
        let engine = desks(&target);
        let pulled = stale(&engine, json!({}));
        let n1 = pulled["items"][0].clone();
        let n2 = pulled["items"][1].clone();
        let presented = |pulled: &Value, vector: Value| json!({"id": pulled["id"], "sourceHash": pulled["sourceHash"], "vector": vector});
        update(&engine, "n1", json!({"title": "Pine desk"}));
        engine
            .instances()
            .delete(&alice(), "Note", "n2", WriteOptions::default())
            .expect("delete");
        assert_eq!(
            settle(
                &engine,
                json!([
                    presented(&n1, json!([1, 0, 0])),
                    presented(&n2, json!([0, 1, 0]))
                ]),
                &embedder()
            )
            .expect("settle"),
            json!({"settled": 0, "skipped": [{"id": "n1", "reason": "moved"}, {"id": "n2", "reason": "not_found"}]})
        );
        let moved = stale(&engine, json!({}))["items"][0].clone();
        assert_eq!(
            (&moved["id"], &moved["text"]),
            (&json!("n1"), &json!("Pine desk\n\nA desk of solid wood."))
        );
        assert_ne!(moved["sourceHash"], n1["sourceHash"]);
        assert_eq!(
            settle(
                &engine,
                json!([presented(&moved, json!([1, 0, 0]))]),
                &embedder()
            )
            .expect("settle"),
            json!({"settled": 1, "skipped": []})
        );

        // A field Search does not index leaves the vector as it is; the
        // text back as it was pulled takes the old hash again.
        update(&engine, "n1", json!({"author": "ann@example.com"}));
        assert!(stale_ids(&engine).is_empty());
        update(&engine, "n1", json!({"title": "Walnut desk"}));
        assert_eq!(stale(&engine, json!({}))["items"], json!([n1]));
        // Text that comes to hold nothing wants no vector, and has none.
        update(&engine, "n1", json!({"title": " ", "body": null}));
        assert!(stale_ids(&engine).is_empty());
        assert!(rows_of(&engine).contains(&("n1".to_owned(), false, false, None)));
        assert_eq!(
            settle(
                &engine,
                json!([presented(&n1, json!([1, 0, 0]))]),
                &embedder()
            )
            .expect("settle")["skipped"],
            json!([{"id": "n1", "reason": "moved"}])
        );
    }
}

// A hit's id, score, place in the text ranking and in the vector ranking.
type Placed = (String, f64, Option<i64>, Option<i64>);

// An expected hit: its id, the places its score sums, and its places.
type Expected<'a> = (&'a str, &'a [i64], Option<i64>, Option<i64>);

// hit_places is each hit's id, score, text rank and vector rank.
fn hit_places(page: &Value) -> Vec<Placed> {
    page["items"]
        .as_array()
        .expect("items")
        .iter()
        .map(|hit| {
            (
                hit["id"].as_str().expect("id").to_owned(),
                number(&hit["score"]).expect("a score"),
                hit["text"]["rank"].as_i64(),
                hit["vector"]["rank"].as_i64(),
            )
        })
        .collect()
}

fn places(expected: &[Expected<'_>]) -> Vec<Placed> {
    expected
        .iter()
        .map(|(id, ranks, text, vector)| ((*id).to_owned(), rrf(ranks), *text, *vector))
        .collect()
}

#[test]
fn a_vector_ranks_by_cosine_similarity_and_a_query_and_a_vector_fuse_their_rankings() {
    for target in targets("vectors_rank") {
        let engine = target.open_core();
        publish(
            &engine,
            &notes(
                json!([{"name": "Search", "config": {"fields": ["title"], "vectors": vectors()}}]),
            ),
        );
        for (id, title) in [
            ("a", "walnut desk"),
            ("b", "walnut chair"),
            ("c", "oak table"),
            ("d", "walnut walnut walnut lamp"),
            ("e", "walnut stool"),
        ] {
            create(&engine, json!({ "title": title }), id);
        }
        // e is not embedded yet: it matches by its text alone.
        settle_all(
            &engine,
            &[
                ("a", json!([1, 0, 0])),
                ("b", json!([0, 1, 0])),
                ("c", json!([0.8, 0.6, 0])),
                ("d", json!([-1, 0, 0])),
            ],
            "Note",
        )
        .expect("settle");

        let by_vector = search(&engine, json!({"vector": [2, 0, 0], "model": "test-3d"}));
        assert_eq!(ids(&by_vector), ["a", "c", "b", "d"]);
        let similarity = |at: usize| number(&by_vector["items"][at]["vector"]["similarity"]);
        assert_eq!(similarity(0), Some(1.0));
        assert!((similarity(1).expect("c") - 0.8).abs() < 1e-6);
        assert_eq!((similarity(2), similarity(3)), (Some(0.0), Some(-1.0)));
        assert_eq!(
            hit_places(&by_vector),
            places(&[
                ("a", &[1], None, Some(1)),
                ("c", &[2], None, Some(2)),
                ("b", &[3], None, Some(3)),
                ("d", &[4], None, Some(4))
            ])
        );

        // By text alone: d, then a, b and e in the order they were indexed.
        let by_text = search(&engine, json!({"query": "walnut"}));
        assert_eq!(ids(&by_text), ["d", "a", "b", "e"]);
        if target.dialect == Dialect::Sqlite {
            assert_eq!(
                by_text["items"][1],
                json!({"id": "a", "rank": 2, "field": "title", "snippet": [{"text": "walnut", "match": true}, {"text": " desk", "match": false}]})
            );
        }

        // Fused: a is second by text and first by vector, so it passes d.
        let fused = search(&engine, json!({"query": "walnut", "vector": [1, 0, 0]}));
        assert_eq!(
            hit_places(&fused),
            places(&[
                ("a", &[2, 1], Some(2), Some(1)),
                ("d", &[1, 4], Some(1), Some(4)),
                ("b", &[3, 3], Some(3), Some(3)),
                ("c", &[2], None, Some(2)),
                ("e", &[4], Some(4), None)
            ])
        );
        assert_eq!(fused["items"][0]["field"], json!("title"));
        assert!(
            fused["items"][3].get("snippet").is_none(),
            "a hit by vector alone has no snippet"
        );

        // Pages go on through the fused ranking.
        let first = search(
            &engine,
            json!({"query": "walnut", "vector": [1, 0, 0], "limit": 3}),
        );
        assert_eq!(ids(&first), ["a", "d", "b"]);
        let second = search(
            &engine,
            json!({"query": "walnut", "vector": [1, 0, 0], "limit": 3, "cursor": first["next"]}),
        );
        assert_eq!(
            (
                second["items"]
                    .as_array()
                    .expect("items")
                    .iter()
                    .map(|hit| (hit["id"].clone(), hit["rank"].clone()))
                    .collect::<Vec<_>>(),
                &second["next"]
            ),
            (
                vec![(json!("c"), json!(4)), (json!("e"), json!(5))],
                &Value::Null
            )
        );
    }
}

#[test]
fn a_vector_the_config_does_not_take_is_invalid_argument_and_so_is_one_on_a_schema_without_vectors()
{
    for target in targets("vectors_invalid") {
        let engine = desks(&target);
        let issues = |params: Value| {
            issues_of(&refusal(call_as(
                &engine,
                &alice(),
                "Note",
                "search",
                params,
            )))
        };
        assert_eq!(
            issues(json!({"vector": [1, 0]})),
            [issue(
                "/vector",
                "holds 2 numbers, and the config's vectors hold 3"
            )]
        );
        assert_eq!(
            issues(json!({"vector": [1, 0, 0], "model": "other"})),
            [issue(
                "/model",
                "is \"other\", and Note's vectors come from \"test-3d\""
            )]
        );
        assert_eq!(
            issues(json!({"vector": [0, 0, 0]})),
            [issue(
                "/vector",
                "is all zeros, which has no direction to compare"
            )]
        );
        for params in [
            json!({}),
            json!({"model": "test-3d", "query": "walnut"}),
            json!({"vector": []}),
        ] {
            assert_eq!(
                code(call_as(&engine, &alice(), "Note", "search", params.clone())),
                EngineErrorCode::InvalidArgument,
                "{params}"
            );
        }
        assert_eq!(
            issues(json!({"query": "walnut", "model": "test-3d"})),
            [issue(
                "",
                "must have property vector when property model is present"
            )]
        );

        let plain = target.open_core();
        publish(
            &plain,
            &notes(json!([{"name": "Search", "config": {"fields": ["title"]}}])),
        );
        let none = "Note's Search keeps no vectors: its config has none";
        assert_eq!(
            issues_of(&refusal(call_as(
                &plain,
                &alice(),
                "Note",
                "search",
                json!({"vector": [1, 0, 0]})
            ))),
            [issue("/vector", none)]
        );
        for (operation, params) in [
            ("staleEmbeddings", json!({})),
            (
                "settleEmbeddings",
                json!({"items": [{"id": "n1", "sourceHash": "x", "vector": [1, 0, 0]}]}),
            ),
        ] {
            assert_eq!(
                issues_of(&refusal(call_as(
                    &plain,
                    &embedder(),
                    "Note",
                    operation,
                    params
                ))),
                [issue("", none)]
            );
        }
    }
}

// similar_rooms publishes Note and creates n1 to n4, which share words
// with n1 in turn less.
fn similar_rooms(target: &Target) -> Engine {
    let engine = target.open_core();
    publish(&engine, &notes(json!([search_config()])));
    for (id, title, body) in [
        ("n1", "Walnut desk", "Solid walnut with brass legs."),
        ("n2", "Walnut chair", "Pairs with the desk."),
        ("n3", "Oak shelf", "Brass brackets."),
        ("n4", "Pine", "Nothing alike."),
    ] {
        create(&engine, json!({"title": title, "body": body}), id);
    }
    engine
}

fn room_vectors() -> Vec<(&'static str, Value)> {
    vec![
        ("n1", json!([1, 0, 0])),
        ("n2", json!([0.9, 0.1, 0])),
        ("n3", json!([0, 1, 0])),
        ("n4", json!([0.95, 0, 0.05])),
    ]
}

#[test]
fn similar_lists_the_nearest_instances_and_leaves_the_instance_out() {
    for target in targets("vectors_similar") {
        let engine = similar_rooms(&target);
        let similar = |id: &str| call(&engine, "similar", json!({ "id": id }));

        let by_text = similar("n1");
        assert_eq!(ids(&by_text), ["n2", "n3"]);
        assert_eq!(
            (&by_text["embedded"], &by_text["next"]),
            (&json!(false), &Value::Null)
        );
        let keys: Vec<&String> = by_text["items"][0]
            .as_object()
            .expect("a hit")
            .keys()
            .collect();
        assert_eq!(keys, ["id", "rank", "field", "snippet"]);

        settle_all(&engine, &room_vectors(), "Note").expect("settle");
        let fused = similar("n1");
        assert_eq!(fused["embedded"], json!(true));
        // n4 shares no word with n1 and is nearest by vector.
        assert_eq!(
            hit_places(&fused),
            places(&[
                ("n2", &[1, 2], Some(1), Some(2)),
                ("n3", &[2, 3], Some(2), Some(3)),
                ("n4", &[1], None, Some(1))
            ])
        );

        // An instance without a vector is ranked by its text alone, and one
        // whose words match nothing has no neighbours.
        update(&engine, "n4", json!({"body": "Nothing comparable."}));
        assert_eq!(
            similar("n4"),
            json!({"items": [], "next": null, "embedded": false})
        );
        let missing = refusal(call_as(
            &engine,
            &alice(),
            "Note",
            "similar",
            json!({"id": "n9"}),
        ));
        assert_eq!(
            (missing.code(), missing.message()),
            (
                EngineErrorCode::NotFound,
                "Note n9 does not exist in namespace default"
            )
        );
    }
}

#[test]
fn similar_takes_a_drafts_text_in_place_of_an_id_and_ranks_as_similar_of_an_instance_does() {
    for target in targets("vectors_draft") {
        let engine = similar_rooms(&target);
        let similar = |params: Value| call_as(&engine, &alice(), "Note", "similar", params);
        let draft = "Walnut desk\n\nSolid walnut with brass legs.";
        let by_text = similar(json!({ "text": draft })).expect("similar");
        assert_eq!(ids(&by_text), ["n1", "n2", "n3"]);
        assert_eq!(by_text["embedded"], json!(false));
        // The text n1 holds ranks every instance but n1 as similar of n1
        // does, and n1 itself first.
        let of_n1 = similar(json!({"id": "n1"})).expect("similar");
        let shifted: Vec<Value> = of_n1["items"]
            .as_array()
            .expect("items")
            .iter()
            .enumerate()
            .map(|(index, hit)| {
                let mut hit = hit.clone();
                hit["rank"] = json!(index + 2);
                hit
            })
            .collect();
        assert_eq!(by_text["items"].as_array().expect("items")[1..], shifted);

        // With a vector, the two rankings fuse, as they do for a settled
        // instance.
        settle_all(&engine, &room_vectors(), "Note").expect("settle");
        let fused = similar(json!({"text": draft, "vector": [1, 0, 0], "model": "test-3d"}))
            .expect("similar");
        assert_eq!(fused["embedded"], json!(true));
        assert_eq!(
            hit_places(&fused),
            places(&[
                ("n1", &[1, 1], Some(1), Some(1)),
                ("n2", &[2, 3], Some(2), Some(3)),
                ("n3", &[3, 4], Some(3), Some(4)),
                ("n4", &[2], None, Some(2))
            ])
        );
        // A vector alone ranks when the text holds no word to search for.
        assert_eq!(
            ids(&similar(json!({"text": "a b", "vector": [0, 1, 0]})).expect("similar")),
            ["n3", "n2", "n1", "n4"]
        );
        assert_eq!(
            similar(json!({"text": "a b"})).expect("similar"),
            json!({"items": [], "next": null, "embedded": false})
        );
        // A page at a time.
        let first = similar(json!({"text": draft, "limit": 2})).expect("similar");
        assert_eq!(ids(&first), ["n1", "n2"]);
        assert_eq!(
            ids(
                &similar(json!({"text": draft, "limit": 2, "cursor": first["next"]}))
                    .expect("similar")
            ),
            ["n3"]
        );
        // Its parameters: one of id and text, a vector only with text, the
        // config's model and dimensions.
        let issues = |params: Value| issues_of(&refusal(similar(params)));
        assert_eq!(issues(json!({"id": "n1", "text": draft}))[0].0, "");
        assert_eq!(issues(json!({}))[0].0, "");
        assert!(!issues(json!({"id": "n1", "vector": [1, 0, 0]})).is_empty());
        assert_eq!(
            issues(json!({"text": draft, "vector": [1, 0]})),
            [issue(
                "/vector",
                "holds 2 numbers, and the config's vectors hold 3"
            )]
        );
        assert_eq!(
            issues(json!({"text": draft, "vector": [1, 0, 0], "model": "other"})),
            [issue(
                "/model",
                "is \"other\", and Note's vectors come from \"test-3d\""
            )]
        );
        let plain = target.open_core();
        publish(
            &plain,
            &notes(json!([{"name": "Search", "config": {"fields": ["title"]}}])),
        );
        assert_eq!(
            issues_of(&refusal(call_as(
                &plain,
                &alice(),
                "Note",
                "similar",
                json!({"text": "walnut", "vector": [1, 0, 0]})
            ))),
            [issue(
                "/vector",
                "Note's Search keeps no vectors: its config has none"
            )]
        );
        // A caller who may not read the schema is refused, as for every
        // search.
        let closed = target.open_core_with(|options| {
            options.policy = Some(Policy::custom(RecordingPolicy::new(|request| {
                request.principal().subject == "alice"
                    || request.action() != "read"
                    || request.operation().is_some()
            })));
        });
        publish(&closed, &notes(json!([search_config()])));
        assert_eq!(
            code(call_as(
                &closed,
                &bob(),
                "Note",
                "similar",
                json!({ "text": draft })
            )),
            EngineErrorCode::Forbidden
        );
    }
}

#[test]
fn a_version_that_changes_the_model_the_dimensions_or_the_fields_makes_every_vector_stale() {
    for target in targets("vectors_versions") {
        let engine = desks(&target);
        let pairs = [("n1", json!([1, 0, 0])), ("n2", json!([0, 1, 0]))];
        settle_all(&engine, &pairs, "Note").expect("settle");
        let config = |change: Value| -> Value {
            let mut config = search_config()["config"].clone();
            if let (Value::Object(config), Value::Object(change)) = (&mut config, change) {
                config.extend(change);
            }
            notes(json!([{ "name": "Search", "config": config }]))
        };

        // Weights and the permission change nothing a vector is computed
        // from.
        publish(&engine, &config(json!({"weights": {"body": 2}})));
        publish(
            &engine,
            &config(
                json!({"vectors": {"dimensions": 3, "model": "test-3d", "permission": "notes.write"}}),
            ),
        );
        assert!(stale_ids(&engine).is_empty());

        // A new model: every vector goes stale under a new hash, and a
        // settle under the old one is skipped.
        let old: String = engine
            .db()
            .read(|tx| {
                Ok::<_, superschematic_engine::storage::Error>(
                    tx.get(
                        "SELECT source_hash FROM bhv_search__rows WHERE schema = 'Note' AND id = 'n1'",
                        &[],
                    )?
                    .expect("a row")
                    .text("source_hash")?
                    .to_owned(),
                )
            })
            .expect("the hash");
        let v2 = json!({"dimensions": 3, "model": "test-3d-v2", "permission": "notes.embed"});
        publish(&engine, &config(json!({ "vectors": v2 })));
        assert_eq!(stale_ids(&engine), ["n1", "n2"]);
        assert_eq!(stale(&engine, json!({}))["model"], json!("test-3d-v2"));
        assert_eq!(
            search(&engine, json!({"vector": [1, 0, 0]}))["items"],
            json!([]),
            "no vector ranks until it is settled again"
        );
        assert_eq!(
            settle(
                &engine,
                json!([{"id": "n1", "sourceHash": old, "vector": [1, 0, 0]}]),
                &embedder()
            )
            .expect("settle")["skipped"],
            json!([{"id": "n1", "reason": "moved"}])
        );
        settle_all(&engine, &pairs, "Note").expect("settle");

        // New dimensions: stale again, and a vector of the old ones is
        // refused.
        publish(
            &engine,
            &config(
                json!({"vectors": {"dimensions": 2, "model": "test-3d-v2", "permission": "notes.embed"}}),
            ),
        );
        assert_eq!(stale_ids(&engine), ["n1", "n2"]);
        assert_eq!(
            code(settle_all(&engine, &pairs, "Note")),
            EngineErrorCode::InvalidArgument
        );
        settle_all(
            &engine,
            &[("n1", json!([1, 0])), ("n2", json!([0, 1]))],
            "Note",
        )
        .expect("settle");

        // Removed: the hashes and vectors go; added back, every instance is
        // stale.
        publish(
            &engine,
            &notes(json!([{"name": "Search", "config": {"fields": ["title", "body"]}}])),
        );
        assert!(rows_of(&engine)
            .iter()
            .all(|(_, hashed, embedded, _)| !hashed && !embedded));
        publish(&engine, &config(json!({})));
        assert_eq!(stale_ids(&engine), ["n1", "n2"]);
        settle_all(&engine, &pairs, "Note").expect("settle");

        // New fields rebuild the index, with every vector stale.
        publish(
            &engine,
            &config(json!({"fields": ["title"], "weights": {}})),
        );
        let texts: Vec<(Value, Value)> = stale(&engine, json!({}))["items"]
            .as_array()
            .expect("items")
            .iter()
            .map(|item| (item["id"].clone(), item["text"].clone()))
            .collect();
        assert_eq!(
            texts,
            [
                (json!("n1"), json!("Walnut desk")),
                (json!("n2"), json!("Oak chair"))
            ]
        );

        // Search removed and added: the index and its vectors start again.
        publish(&engine, &notes(json!([])));
        assert!(rows_of(&engine).is_empty());
        publish(&engine, &config(json!({})));
        assert_eq!(stale_ids(&engine), ["n1", "n2"]);
    }
}

#[test]
fn each_namespaces_vectors_are_its_own() {
    for target in targets("vectors_namespaces") {
        let engine = target.open_core_with(|options| {
            options.namespaces = NamespaceOptions {
                names: vec!["shared".to_owned(), "east".to_owned(), "west".to_owned()],
                shared: Some("shared".to_owned()),
            };
        });
        let shared = Some("shared");
        engine
            .schemas()
            .define(
                &alice(),
                notes(json!([search_config()])),
                DefineOptions {
                    namespace: shared,
                    source: None,
                },
            )
            .expect("define");
        engine
            .schemas()
            .publish(&alice(), "Note", SchemaTarget { namespace: shared })
            .expect("publish");
        for namespace in ["east", "west"] {
            create_in(
                &engine,
                "Note",
                json!({"title": format!("Walnut {namespace}")}),
                "n1",
                Some(namespace),
            );
        }
        let in_namespace =
            |principal: &Principal, operation: &str, params: Value, namespace: &str| {
                engine
                    .instances()
                    .invoke_schema(principal, "Note", operation, &params, Some(namespace))
                    .expect(operation)
            };
        let east = in_namespace(&alice(), "staleEmbeddings", json!({}), "east")["items"][0].clone();
        let presented =
            json!([{"id": east["id"], "sourceHash": east["sourceHash"], "vector": [1, 0, 0]}]);
        // west's n1 has text of its own.
        assert_eq!(
            in_namespace(
                &embedder(),
                "settleEmbeddings",
                json!({ "items": presented }),
                "west"
            ),
            json!({"settled": 0, "skipped": [{"id": "n1", "reason": "moved"}]})
        );
        in_namespace(
            &embedder(),
            "settleEmbeddings",
            json!({ "items": presented }),
            "east",
        );
        assert_eq!(
            in_namespace(&alice(), "staleEmbeddings", json!({}), "east")["items"],
            json!([])
        );
        assert_eq!(
            ids(&in_namespace(
                &alice(),
                "staleEmbeddings",
                json!({}),
                "west"
            )),
            ["n1"]
        );
        assert_eq!(
            in_namespace(&alice(), "search", json!({"vector": [1, 0, 0]}), "west")["items"],
            json!([])
        );
    }
}

// shelves publishes Note with vectors of test-3d, Memo of other-3d, Task
// with none, Secret, which bob may not read, and Plain, which does not
// compose Search.
fn shelves(target: &Target, policy: Option<RecordingPolicy>) -> Engine {
    let engine = target.open_core_with(|options| {
        if let Some(policy) = policy {
            options.policy = Some(Policy::custom(policy));
        }
    });
    let titled = |vectors: Option<Value>| {
        let mut config = json!({"fields": ["title"]});
        if let Some(vectors) = vectors {
            config["vectors"] = vectors;
        }
        json!([{ "name": "Search", "config": config }])
    };
    publish(&engine, &notes_named(titled(Some(vectors())), "Note"));
    publish(
        &engine,
        &notes_named(
            titled(Some(
                json!({"dimensions": 3, "model": "other-3d", "permission": "notes.embed"}),
            )),
            "Memo",
        ),
    );
    publish(&engine, &notes_named(titled(None), "Task"));
    publish(&engine, &notes_named(titled(None), "Secret"));
    publish(&engine, &notes_named(json!([]), "Plain"));
    for (schema, id, title) in [
        ("Note", "n1", "Walnut desk"),
        ("Note", "n2", "Oak chair"),
        ("Memo", "m1", "Walnut memo"),
        ("Task", "t1", "Buy walnut oil"),
        ("Task", "t2", "Sand the walnut desk"),
        ("Secret", "s1", "Walnut secret"),
        ("Plain", "p1", "Walnut plain"),
    ] {
        create_in(&engine, schema, json!({ "title": title }), id, None);
    }
    settle_all(
        &engine,
        &[("n1", json!([1, 0, 0])), ("n2", json!([0, 1, 0]))],
        "Note",
    )
    .expect("settle");
    settle_all(&engine, &[("m1", json!([1, 0, 0]))], "Memo").expect("settle");
    engine
}

fn not_secret(request: &superschematic_engine::AccessRequest<'_>) -> bool {
    let subject = request.principal().subject.as_str();
    subject == "alice" || subject == "embedder" || request.schema() != Some("Secret")
}

fn across(engine: &Engine, params: Value, principal: &Principal) -> Result<Value, Error> {
    engine.search(principal, &params, None)
}

fn schema_places(page: &Value) -> Vec<String> {
    page["items"]
        .as_array()
        .expect("items")
        .iter()
        .map(|hit| {
            format!(
                "{}/{}",
                hit["schema"].as_str().expect("schema"),
                hit["id"].as_str().expect("id")
            )
        })
        .collect()
}

#[test]
fn the_search_across_schemas_fuses_each_readable_schemas_ranking_and_skips_one_the_caller_may_not_read(
) {
    for target in targets("across_fuse") {
        let engine = shelves(&target, Some(RecordingPolicy::new(not_secret)));
        let by_text = across(&engine, json!({"query": "walnut"}), &bob()).expect("search");
        assert_eq!(
            schema_places(&by_text),
            ["Memo/m1", "Note/n1", "Task/t1", "Task/t2"]
        );
        let first = by_text["items"][0].clone();
        assert_eq!(
            (
                &first["schema"],
                &first["id"],
                &first["rank"],
                number(&first["score"]),
                &first["field"],
                &first["text"]
            ),
            (
                &json!("Memo"),
                &json!("m1"),
                &json!(1),
                Some(rrf(&[1])),
                &json!("title"),
                &json!({"rank": 1})
            )
        );
        if target.dialect == Dialect::Sqlite {
            assert_eq!(
                first["snippet"],
                json!([{"text": "Walnut", "match": true}, {"text": " memo", "match": false}])
            );
        }
        assert_eq!(
            schema_places(&across(&engine, json!({"query": "walnut"}), &alice()).expect("search")),
            ["Memo/m1", "Note/n1", "Secret/s1", "Task/t1", "Task/t2"]
        );

        // A vector ranks the schemas of its model; the query alone the
        // others.
        let fused = across(
            &engine,
            json!({"query": "walnut", "vector": [1, 0, 0], "model": "test-3d"}),
            &bob(),
        )
        .expect("search");
        let ranked: Vec<(i64, Placed)> = fused["items"]
            .as_array()
            .expect("items")
            .iter()
            .map(|hit| {
                (
                    hit["rank"].as_i64().expect("rank"),
                    (
                        format!(
                            "{}/{}",
                            hit["schema"].as_str().expect("schema"),
                            hit["id"].as_str().expect("id")
                        ),
                        number(&hit["score"]).expect("score"),
                        hit["text"]["rank"].as_i64(),
                        hit["vector"]["rank"].as_i64(),
                    ),
                )
            })
            .collect();
        let expected: Vec<(i64, Placed)> = (1..)
            .zip(places(&[
                ("Note/n1", &[1, 1], Some(1), Some(1)),
                ("Memo/m1", &[1], Some(1), None),
                ("Task/t1", &[1], Some(1), None),
                ("Note/n2", &[2], None, Some(2)),
                ("Task/t2", &[2], Some(2), None),
            ]))
            .collect();
        assert_eq!(ranked, expected);
        assert_eq!(
            schema_places(
                &across(
                    &engine,
                    json!({"vector": [1, 0, 0], "model": "other-3d"}),
                    &bob()
                )
                .expect("search")
            ),
            ["Memo/m1"]
        );
        assert_eq!(
            across(
                &engine,
                json!({"vector": [1, 0, 0], "model": "none"}),
                &bob()
            )
            .expect("search"),
            json!({"items": [], "next": null})
        );

        // Pages go on through the merged ranking.
        let first =
            across(&engine, json!({"query": "walnut", "limit": 3}), &bob()).expect("search");
        assert_eq!(
            schema_places(
                &across(
                    &engine,
                    json!({"query": "walnut", "limit": 3, "cursor": first["next"]}),
                    &bob()
                )
                .expect("search")
            ),
            ["Task/t2"]
        );
    }
}

#[test]
fn the_search_across_schemas_skips_a_schema_whose_search_the_policy_refuses_and_refuses_parameters_it_does_not_take(
) {
    for target in targets("across_refused") {
        let policy = RecordingPolicy::new(|request| {
            not_secret(request)
                && !(request.schema() == Some("Task") && request.operation() == Some("search"))
        });
        let engine = shelves(&target, Some(policy));
        assert_eq!(
            schema_places(&across(&engine, json!({"query": "walnut"}), &bob()).expect("search")),
            ["Memo/m1", "Note/n1"]
        );
        assert_eq!(
            issues_of(&refusal(across(&engine, json!({}), &bob()))),
            [
                issue("", "must match a schema in anyOf"),
                issue("", "must have required property 'query'"),
                issue("", "must have required property 'vector'")
            ]
        );
        for (params, expected) in [
            (
                json!({"vector": [1, 0, 0]}),
                issue(
                    "",
                    "must have property model when property vector is present",
                ),
            ),
            (
                json!({"query": "walnut", "model": "test-3d"}),
                issue(
                    "",
                    "must have property vector when property model is present",
                ),
            ),
            (
                json!({"query": "walnut", "schema": "Note"}),
                issue("", "must NOT have additional properties: schema"),
            ),
            (
                json!({"query": "walnut", "limit": 0}),
                issue("/limit", "must be >= 1"),
            ),
        ] {
            let error = refusal(across(&engine, params.clone(), &bob()));
            assert_eq!(
                (error.name(), issues_of(&error)),
                ("OperationParamsError", vec![expected]),
                "{params}"
            );
        }
        let fts5 = issues_of(&refusal(across(
            &engine,
            json!({"query": "walnut AND", "syntax": "fts5"}),
            &bob(),
        )));
        assert_eq!(fts5[0].0, "/query");
        assert_eq!(
            code(engine.search(&bob(), &json!({"query": "walnut"}), Some("nowhere"))),
            EngineErrorCode::UnknownNamespace
        );
    }
}

fn request(method: &str, path: &str, principal: Principal, body: Option<&Value>) -> Request {
    let mut request = Request {
        method: method.to_owned(),
        prefix: "/api".to_owned(),
        path: format!("/api{path}"),
        caller: Caller {
            principal: Some(principal),
            ..Caller::default()
        },
        request_id: "r1".to_owned(),
        ..Request::default()
    };
    if let Some(body) = body {
        request
            .headers
            .push(("content-type".to_owned(), "application/json".to_owned()));
        request.body = Some(body.to_string().into_bytes());
    }
    request
}

fn json_of(response: &Response) -> Value {
    serde_json::from_slice(response.bytes()).expect("a JSON body")
}

fn engine_with_notes(target: &Target) -> Engine {
    let engine = target.open_core();
    publish(&engine, &notes(json!([search_config()])));
    create(&engine, json!({"title": "Walnut desk"}), "n1");
    create(&engine, json!({"title": "Oak chair"}), "n2");
    engine
}

#[test]
fn post_search_answers_the_merged_ranking_and_the_embedder_pulls_and_settles_on_the_schema_level_routes(
) {
    for target in targets("across_http") {
        let engine = engine_with_notes(&target);
        let options = ApiOptions::default();
        let post = |path: &str, body: Value, principal: Principal| {
            handle(
                &engine,
                &options,
                &request("POST", path, principal, Some(&body)),
            )
        };
        let pulled = post(
            "/namespaces/default/schemas/Note/operations/staleEmbeddings",
            json!({}),
            embedder(),
        );
        assert_eq!(pulled.status, 200);
        let pulled = json_of(&pulled);
        assert_eq!(ids(&pulled["data"]), ["n1", "n2"]);
        let items: Vec<Value> = pulled["data"]["items"]
            .as_array()
            .expect("items")
            .iter()
            .enumerate()
            .map(|(index, item)| {
                json!({
                    "id": item["id"],
                    "sourceHash": item["sourceHash"],
                    "vector": if index == 0 { json!([1, 0, 0]) } else { json!([0, 1, 0]) },
                })
            })
            .collect();
        let settled = post(
            "/namespaces/default/schemas/Note/operations/settleEmbeddings",
            json!({ "items": items }),
            embedder(),
        );
        assert_eq!(
            (settled.status, json_of(&settled)["data"].clone()),
            (200, json!({"settled": 2, "skipped": []}))
        );
        assert_eq!(
            post(
                "/namespaces/default/schemas/Note/operations/settleEmbeddings",
                json!({ "items": items }),
                alice()
            )
            .status,
            403
        );

        let found = post(
            "/namespaces/default/search",
            json!({"query": "walnut", "vector": [1, 0, 0], "model": "test-3d"}),
            alice(),
        );
        assert_eq!(found.status, 200);
        let hits: Vec<(Value, Value, Option<i64>, Option<i64>)> = json_of(&found)["data"]["items"]
            .as_array()
            .expect("items")
            .iter()
            .map(|hit| {
                (
                    hit["schema"].clone(),
                    hit["id"].clone(),
                    hit["text"]["rank"].as_i64(),
                    hit["vector"]["rank"].as_i64(),
                )
            })
            .collect();
        assert_eq!(
            hits,
            [
                (json!("Note"), json!("n1"), Some(1), Some(1)),
                (json!("Note"), json!("n2"), None, Some(2))
            ]
        );
        let empty = post("/namespaces/default/search", json!({}), alice());
        assert_eq!(
            (empty.status, json_of(&empty)["code"].clone()),
            (400, json!("invalid_argument"))
        );
        let mut plain = request(
            "POST",
            "/namespaces/default/search",
            alice(),
            Some(&json!({"query": "walnut"})),
        );
        plain.headers = vec![("content-type".to_owned(), "text/plain".to_owned())];
        assert_eq!(handle(&engine, &options, &plain).status, 415);
        assert_eq!(
            post(
                "/namespaces/nowhere/search",
                json!({"query": "walnut"}),
                alice()
            )
            .status,
            404
        );
    }
}

// mcp_messages reads the JSON-RPC messages of an MCP event stream.
fn mcp_messages(response: &Response) -> Vec<Value> {
    let text = String::from_utf8(response.bytes().to_vec()).expect("text");
    text.split("\n\n")
        .filter_map(|block| {
            block
                .lines()
                .find_map(|line| line.strip_prefix("data: "))
                .map(|data| serde_json::from_str(data).expect("JSON"))
        })
        .collect()
}

fn mcp(engine: &Engine, message: Value) -> Value {
    let mut post = request("POST", "/namespaces/default/mcp", alice(), Some(&message));
    post.headers.push((
        "accept".to_owned(),
        "application/json, text/event-stream".to_owned(),
    ));
    post.headers
        .push(("mcp-protocol-version".to_owned(), "2025-06-18".to_owned()));
    let response = handle(engine, &ApiOptions::default(), &post);
    assert_eq!(response.status, 200);
    mcp_messages(&response).remove(0)
}

#[test]
fn the_mcp_tool_search_is_listed_where_a_schema_composes_search_and_runs_the_search_across_schemas()
{
    for target in targets("across_mcp") {
        let bare = target.open_core();
        let manifest = bare
            .tools()
            .manifest(
                &alice(),
                superschematic_engine::tools::ToolTarget::default(),
            )
            .expect("manifest");
        assert!(
            !manifest["tools"]
                .as_array()
                .expect("tools")
                .iter()
                .any(|tool| tool["name"] == json!("engine.search")),
            "no schema to search, no tool"
        );

        let engine = engine_with_notes(&target);
        let listed = mcp(
            &engine,
            json!({"jsonrpc": "2.0", "id": 1, "method": "tools/list"}),
        );
        let tools = listed["result"]["tools"].as_array().expect("tools");
        let names: Vec<&str> = tools
            .iter()
            .map(|tool| tool["name"].as_str().expect("a name"))
            .collect();
        for name in [
            "search",
            "note_search",
            "note_similar",
            "note_stale_embeddings",
            "note_settle_embeddings",
        ] {
            assert!(names.contains(&name), "{name}: {names:?}");
        }
        let tool = tools
            .iter()
            .find(|tool| tool["name"] == json!("search"))
            .expect("search");
        let mut keys: Vec<&str> = tool["inputSchema"]["properties"]
            .as_object()
            .map(Map::keys)
            .expect("properties")
            .map(String::as_str)
            .collect();
        keys.sort();
        assert_eq!(
            (&tool["annotations"]["readOnlyHint"], keys),
            (
                &json!(true),
                vec!["cursor", "limit", "model", "query", "syntax", "vector"]
            )
        );
        let found = mcp(
            &engine,
            json!({"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": {"name": "search", "arguments": {"query": "walnut"}}}),
        );
        let content = &found["result"]["structuredContent"];
        assert_eq!(schema_places(content), ["Note/n1"]);
        assert_eq!(number(&content["items"][0]["score"]), Some(rrf(&[1])));
        assert_eq!(content["items"][0]["text"], json!({"rank": 1}));
        let refused = mcp(
            &engine,
            json!({"jsonrpc": "2.0", "id": 3, "method": "tools/call", "params": {"name": "search", "arguments": {"vector": [1, 0, 0]}}}),
        );
        assert_eq!(refused["result"]["isError"], json!(true));
        assert_eq!(
            refused["result"]["structuredContent"]["code"],
            json!("invalid_argument")
        );
    }
}
