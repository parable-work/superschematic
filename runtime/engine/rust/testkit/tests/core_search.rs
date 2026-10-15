//! Search, the core's full-text search: the cases of
//! `test/core-search.test.ts` (the index kept in the transaction of each
//! create, update, delete and operation that changes an indexed field,
//! ranking by the weights, paging, plain words and the opt-in FTS5
//! syntax, answers only for a caller who may read the schema and only from
//! the caller's namespace, the config's rules, and the index rebuilt or
//! dropped when a version changes the fields or adds or removes Search),
//! on SQLite and, with the variable set, on Postgres (D58).
//!
//! SQLite gives the TypeScript engine's answers, snippets and all. On
//! Postgres the matches are the same but for the tokens its parser keeps
//! whole (an email address here), the order is `ts_rank_cd`'s and the
//! snippets `ts_headline`'s: each case holds Postgres to the ids where the
//! order is not in doubt, and to a snippet that marks a match.

mod support;

use std::time::Instant;

use serde_json::{json, Value};

use superschematic_engine::storage::{Dialect, SqlValue};
use superschematic_engine::{
    CreateOptions, DefineOptions, Engine, EngineErrorCode, Error, MetaSchema, NamespaceOptions,
    Policy, Principal, SchemaTarget, WriteOptions,
};
use superschematic_engine_testkit::core_fixtures::Tripwire;
use superschematic_engine_testkit::open_meta_schema;
use support::{
    alice, code, issue, issues_of, named, refusal, repository, schema_document, targets,
    RecordingPolicy, Target,
};

fn search_config() -> Value {
    json!({"name": "Search", "config": {"fields": ["title", "body"], "weights": {"title": 3}}})
}

// notes is a Note with fields of every kind Search's config is checked
// against.
fn notes(behaviors: Value) -> Value {
    let mut document = schema_document(
        "Note",
        json!([
            {"name": "title", "typeRef": {"name": "string"}, "required": true},
            {"name": "body", "typeRef": {"name": "string"}},
            {"name": "author", "typeRef": {"name": "Contact.Email"}},
            {"name": "rating", "typeRef": {"name": "Generic.Int64"}},
            {"name": "mood", "typeRef": {"name": "Mood"}},
            {"name": "tags", "typeRef": {"name": "string", "isArray": true}},
            {"name": "meta", "typeRef": {"name": "Meta"}}
        ]),
        json!({
            "enums": {"Mood": {"name": "Mood", "values": [{"name": "CALM"}, {"name": "LOUD"}]}},
            "types": {"Meta": {"name": "Meta", "role": "EmbeddedStruct", "fields": [{"name": "source", "typeRef": {"name": "string"}}]}}
        }),
    );
    document["types"]["Note"]["behaviors"] = behaviors;
    document
}

fn publish(engine: &Engine, document: &Value, namespace: Option<&str>) -> Result<i64, Error> {
    engine.schemas().define(
        &alice(),
        document,
        DefineOptions {
            namespace,
            source: None,
        },
    )?;
    Ok(engine
        .schemas()
        .publish(&alice(), "Note", SchemaTarget { namespace })?
        .version)
}

fn create(engine: &Engine, data: Value, id: &str, namespace: Option<&str>) {
    engine
        .instances()
        .create(
            &alice(),
            "Note",
            &data,
            CreateOptions {
                id: Some(id),
                namespace,
                ..Default::default()
            },
        )
        .expect("create");
}

fn update(engine: &Engine, id: &str, patch: Value) -> Result<(), Error> {
    engine
        .instances()
        .update(&alice(), "Note", id, &patch, WriteOptions::default())
        .map(|_| ())
}

fn search_as(
    engine: &Engine,
    principal: &Principal,
    params: Value,
    namespace: Option<&str>,
) -> Result<Value, Error> {
    engine
        .instances()
        .invoke_schema(principal, "Note", "search", &params, namespace)
}

fn search(engine: &Engine, params: Value) -> Value {
    search_as(engine, &alice(), params, None).expect("search")
}

// ids searches with plain words and returns the matching ids in rank
// order.
fn ids_in(engine: &Engine, query: &str, namespace: Option<&str>) -> Vec<String> {
    let found = search_as(
        engine,
        &alice(),
        json!({"query": query, "limit": 500}),
        namespace,
    )
    .expect("search");
    ids_of(&found)
}

fn ids(engine: &Engine, query: &str) -> Vec<String> {
    ids_in(engine, query, None)
}

fn ids_of(page: &Value) -> Vec<String> {
    page["items"]
        .as_array()
        .expect("items")
        .iter()
        .map(|hit| hit["id"].as_str().expect("id").to_owned())
        .collect()
}

// rows_of counts the index rows a namespace holds for Note.
fn rows_of(engine: &Engine, namespace: &str) -> i64 {
    engine
        .db()
        .read(|tx| {
            tx.get(
                "SELECT count(*) AS n FROM bhv_search__rows WHERE namespace = ? AND schema = 'Note'",
                &[SqlValue::from(namespace)],
            )?
            .expect("a row")
            .i64("n")
        })
        .expect("count")
}

// marked holds every hit of a page to a field and a snippet with a match
// in it, the snippet Postgres writes.
fn marked(page: &Value) {
    for hit in page["items"].as_array().expect("items") {
        assert!(hit["field"].is_string(), "{hit}");
        assert!(
            hit["snippet"]
                .as_array()
                .expect("a snippet")
                .iter()
                .any(|part| part["match"] == json!(true)),
            "{hit}"
        );
    }
}

// desks publishes Note and creates n1 and n2, which mention a walnut desk,
// n1 in its title.
fn desks(target: &Target) -> Engine {
    let engine = target.open_core();
    publish(&engine, &notes(json!([search_config()])), None).expect("publish");
    create(
        &engine,
        json!({"title": "Walnut desk", "body": "A desk of solid wood."}),
        "n1",
        None,
    );
    create(
        &engine,
        json!({"title": "Oak chair", "body": "Pairs with the walnut desk."}),
        "n2",
        None,
    );
    engine
}

// walnut_ranks_the_title_first: n1 holds walnut in its title, n2 in its
// body, and the title weighs three times the body.
fn walnut_ranks_the_title_first(target: &Target, engine: &Engine) {
    let found = search(engine, json!({"query": "walnut"}));
    if target.dialect == Dialect::Sqlite {
        assert_eq!(
            found,
            json!({"items": [
                {"id": "n1", "rank": 1, "field": "title", "snippet": [{"text": "Walnut", "match": true}, {"text": " desk", "match": false}]},
                {"id": "n2", "rank": 2, "field": "body", "snippet": [{"text": "Pairs with the ", "match": false}, {"text": "walnut", "match": true}, {"text": " desk.", "match": false}]}
            ], "next": null})
        );
    } else {
        // The title weighs three times the body, so n1 comes first.
        assert_eq!(ids_of(&found), ["n1", "n2"]);
        marked(&found);
        assert_eq!(found["items"][0]["field"], json!("title"));
        assert_eq!(found["items"][1]["field"], json!("body"));
    }
}

// a_failed_write_leaves_the_index_as_it_was: test.Tripwire fails a write
// of the title Boom after Search's afterChange has run, and the write
// rolls back, the index with it.
fn a_failed_write_leaves_the_index_as_it_was(engine: &Engine) {
    let created = engine.instances().create(
        &alice(),
        "Note",
        &json!({"title": "Boom"}),
        CreateOptions {
            id: Some("n3"),
            ..Default::default()
        },
    );
    assert!(
        matches!(&created, Err(Error::Internal(message)) if message == "tripped"),
        "{created:?}"
    );
    let updated = update(engine, "n1", json!({"title": "Boom"}));
    assert!(
        matches!(&updated, Err(Error::Internal(message)) if message == "tripped"),
        "{updated:?}"
    );
    assert!(ids(engine, "boom").is_empty());
    assert_eq!(ids(engine, "pine"), ["n1"]);
    assert_eq!(rows_of(engine, "default"), 2);
}

#[test]
fn a_create_an_update_and_a_delete_keep_the_index_in_their_own_transaction() {
    for target in targets("search_index") {
        let engine = target.open_core_with(|options| {
            options.meta_schema = Some(MetaSchema::Document(
                open_meta_schema(&repository()).expect("the open meta-schema"),
            ));
            options.behaviors = std::mem::take(&mut options.behaviors)
                .extend(superschematic_engine::BehaviorSet::new().with(Tripwire));
        });
        publish(
            &engine,
            &notes(json!([search_config(), {"name": "test.Tripwire"}])),
            None,
        )
        .expect("publish");
        create(
            &engine,
            json!({"title": "Walnut desk", "body": "A desk of solid wood."}),
            "n1",
            None,
        );
        create(
            &engine,
            json!({"title": "Oak chair", "body": "Pairs with the walnut desk."}),
            "n2",
            None,
        );
        walnut_ranks_the_title_first(&target, &engine);

        update(
            &engine,
            "n1",
            json!({"title": "Pine desk", "body": "Light and cheap."}),
        )
        .expect("update");
        assert_eq!(ids(&engine, "walnut"), ["n2"]);
        assert_eq!(ids(&engine, "pine"), ["n1"]);
        // A field Search does not index changes nothing it holds.
        update(
            &engine,
            "n1",
            json!({"author": "ann@example.com", "rating": 4}),
        )
        .expect("update");
        assert_eq!(ids(&engine, "pine"), ["n1"]);
        assert!(ids(&engine, "ann").is_empty());
        // A field cleared leaves the others searchable.
        update(&engine, "n1", json!({"body": null})).expect("update");
        assert!(ids(&engine, "cheap").is_empty());
        assert_eq!(ids(&engine, "pine"), ["n1"]);

        a_failed_write_leaves_the_index_as_it_was(&engine);

        assert!(engine
            .instances()
            .delete(&alice(), "Note", "n2", WriteOptions::default())
            .expect("delete"));
        assert!(ids(&engine, "walnut").is_empty());
        assert_eq!(ids(&engine, "desk"), ["n1"]);
        assert_eq!(rows_of(&engine, "default"), 1);
    }
}

#[test]
fn an_operation_that_changes_the_own_fields_with_update_reindexes_them() {
    for target in targets("search_operation") {
        let engine = target.open_core();
        publish(
            &engine,
            &notes(json!([
                search_config(),
                {"name": "Revisions", "config": {"review": {"permission": "notes.review"}}}
            ])),
            None,
        )
        .expect("publish");
        create(&engine, json!({"title": "Draft plan"}), "n1", None);
        let proposal = engine
            .instances()
            .invoke(
                &alice(),
                "Note",
                "n1",
                "propose",
                &json!({"patch": {"title": "Final plan"}}),
                WriteOptions::default(),
            )
            .expect("propose");
        // A proposal changes nothing a search sees.
        assert!(ids(&engine, "final").is_empty());
        assert_eq!(ids(&engine, "draft"), ["n1"]);
        let reviewer = Principal::new("rae", vec!["notes.review".to_owned()]);
        engine
            .instances()
            .invoke(
                &reviewer,
                "Note",
                "n1",
                "approve",
                &json!({"proposal": proposal["id"]}),
                WriteOptions::default(),
            )
            .expect("approve");
        assert_eq!(ids(&engine, "final"), ["n1"]);
        assert!(ids(&engine, "draft").is_empty());
    }
}

#[test]
fn ranks_by_the_weights_then_by_when_an_instance_was_indexed_and_a_change_of_weights_alone_applies_at_once(
) {
    for target in targets("search_rank") {
        let engine = target.open_core();
        publish(&engine, &notes(json!([search_config()])), None).expect("publish");
        create(
            &engine,
            json!({"title": "Other", "body": "A fox ran by."}),
            "body",
            None,
        );
        create(
            &engine,
            json!({"title": "A fox", "body": "Nothing else."}),
            "title",
            None,
        );
        create(
            &engine,
            json!({"title": "Fox fox fox", "body": "Nothing else."}),
            "often",
            None,
        );
        // bm25 on SQLite and ts_rank_cd on Postgres agree here: the title
        // weighs more, and more matches count more.
        assert_eq!(ids(&engine, "fox"), ["often", "title", "body"]);
        // The body now weighs more than the title. The index stays as it
        // was.
        let rows = |engine: &Engine| -> Vec<(i64, String)> {
            engine
                .db()
                .read(|tx| {
                    tx.all("SELECT row, id FROM bhv_search__rows ORDER BY row", &[])?
                        .iter()
                        .map(|row| Ok((row.i64("row")?, row.text("id")?.to_owned())))
                        .collect::<Result<Vec<_>, superschematic_engine::storage::Error>>()
                })
                .expect("rows")
        };
        let before = rows(&engine);
        assert_eq!(
            publish(
                &engine,
                &notes(
                    json!([{"name": "Search", "config": {"fields": ["title", "body"], "weights": {"body": 20}}}])
                ),
                None
            )
            .expect("publish"),
            2
        );
        assert_eq!(ids(&engine, "fox"), ["body", "often", "title"]);
        assert_eq!(rows(&engine), before);

        // Equal matches keep the order they were first indexed in, an
        // update included.
        create(&engine, json!({"title": "Twin lamp"}), "t1", None);
        create(&engine, json!({"title": "Twin lamp"}), "t2", None);
        update(&engine, "t1", json!({"title": "Twin lamp "})).expect("update");
        assert_eq!(ids(&engine, "lamp"), ["t1", "t2"]);
    }
}

#[test]
fn the_match_drives_the_query_so_a_prefix_searchs_cost_follows_its_matches() {
    for target in targets("search_prefix") {
        let engine = target.open_core();
        publish(&engine, &notes(json!([search_config()])), None).expect("publish");
        let words = ["alpha", "bravo", "charlie", "delta", "echo"];
        for n in 0..1500 {
            let body: Vec<String> = (0..20)
                .map(|k| format!("{}{}", words[(n + k) % 5], (n * 7 + k) % 300))
                .collect();
            create(
                &engine,
                json!({"title": format!("lamp {n}"), "body": body.join(" ")}),
                &format!("n{n}"),
                None,
            );
        }
        let started = Instant::now();
        for word in words {
            let found = search(
                &engine,
                json!({"query": format!("{word}*"), "syntax": "fts5", "limit": 10}),
            );
            assert_eq!(found["items"].as_array().expect("items").len(), 10);
        }
        // With the schema's rows as the outer loop, the match runs once per
        // instance, and these five searches take many seconds; a debug
        // build gets more room than the TypeScript engine's 750 ms.
        let elapsed = started.elapsed();
        assert!(
            elapsed.as_millis() < 5000,
            "five prefix searches took {elapsed:?} on {:?}",
            target.dialect
        );
    }
}

#[test]
fn pages_through_the_ranking_with_limit_and_cursor_rank_continuing_across_pages() {
    for target in targets("search_pages") {
        let engine = target.open_core();
        publish(&engine, &notes(json!([search_config()])), None).expect("publish");
        for n in 1..=5 {
            let title = format!("lamp {}", "lamp ".repeat(5 - n));
            create(
                &engine,
                json!({"title": title.trim()}),
                &format!("n{n}"),
                None,
            );
        }
        let ranks = |page: &Value| -> Vec<(String, i64)> {
            page["items"]
                .as_array()
                .expect("items")
                .iter()
                .map(|hit| {
                    (
                        hit["id"].as_str().expect("id").to_owned(),
                        hit["rank"].as_i64().expect("rank"),
                    )
                })
                .collect()
        };
        let all = ranks(&search(&engine, json!({"query": "lamp"})));
        let expected: Vec<(String, i64)> = (1..=5).map(|n| (format!("n{n}"), n)).collect();
        assert_eq!(all, expected);
        let mut pages = Vec::new();
        let mut cursor: Option<String> = None;
        loop {
            let mut params = json!({"query": "lamp", "limit": 2});
            if let Some(cursor) = &cursor {
                params["cursor"] = json!(cursor);
            }
            let page = search(&engine, params);
            pages.push(ranks(&page));
            match page["next"].as_str() {
                Some(next) => cursor = Some(next.to_owned()),
                None => break,
            }
        }
        assert_eq!(
            pages,
            [
                expected[0..2].to_vec(),
                expected[2..4].to_vec(),
                expected[4..].to_vec()
            ]
        );
        let bad = refusal(search_as(
            &engine,
            &alice(),
            json!({"query": "lamp", "cursor": "nope"}),
            None,
        ));
        assert_eq!(
            issues_of(&bad),
            [issue("/cursor", "is not a cursor this operation returned")]
        );
        for params in [
            json!({"query": "lamp", "limit": 0}),
            json!({"query": "lamp", "limit": 501}),
            json!({"query": ""}),
        ] {
            assert_eq!(
                code(search_as(&engine, &alice(), params, None)),
                EngineErrorCode::InvalidArgument
            );
        }
    }
}

#[test]
fn a_query_is_plain_words_and_quotes_operators_and_fts5_syntax_in_it_are_text() {
    for target in targets("search_words") {
        let engine = target.open_core();
        publish(&engine, &notes(json!([search_config()])), None).expect("publish");
        create(
            &engine,
            json!({"title": "C++ tips", "body": "Use \"const\" and NOT macros: a OR b, near(x) * star. Caf\u{e9} cr\u{e8}me."}),
            "n1",
            None,
        );
        create(
            &engine,
            json!({"title": "Plain", "body": "Nothing to see."}),
            "n2",
            None,
        );
        // Each word must be in the instance, operators included.
        for (query, expected) in [
            ("macros NOT", vec!["n1"]),
            ("a OR b", vec!["n1"]),
            ("nothing OR macros", vec![]),
            ("NEAR(x", vec!["n1"]),
            ("\"const\"", vec!["n1"]),
            ("\"const", vec!["n1"]),
            // A column filter is a word, and so is a prefix.
            ("body: macros", vec![]),
            ("tip*", vec![]),
            ("tips*", vec!["n1"]),
            // Diacritics and case fold.
            ("CAFE creme", vec!["n1"]),
            // A dangling operator is a word too; one of no letters counts
            // for nothing, and one split in parts is a phrase of them.
            ("macros AND", vec!["n1"]),
            ("macros -", vec!["n1"]),
            ("a-or-b", vec!["n1"]),
            ("b-or-a", vec![]),
        ] {
            assert_eq!(ids(&engine, query), expected, "{query}");
        }
        // What holds no word matches nothing, without an error.
        for query in ["\"", "(", ")", "*", "-", " ", "\u{0}", "{}", ":"] {
            assert_eq!(
                search(&engine, json!({"query": query})),
                json!({"items": [], "next": null}),
                "{query:?}"
            );
        }
        let found = search(&engine, json!({"query": "CAFE creme"}));
        if target.dialect == Dialect::Sqlite {
            assert_eq!(
                found["items"][0]["snippet"],
                json!([{"text": "...", "match": false}, {"text": "Caf\u{e9}", "match": true}, {"text": " ", "match": false}, {"text": "cr\u{e8}me", "match": true}, {"text": ".", "match": false}])
            );
        } else {
            // ts_headline marks the words as they are written.
            marked(&found);
            let matched: Vec<&str> = found["items"][0]["snippet"]
                .as_array()
                .expect("snippet")
                .iter()
                .filter(|part| part["match"] == json!(true))
                .map(|part| part["text"].as_str().expect("text"))
                .collect();
            assert_eq!(matched, ["Caf\u{e9}", "cr\u{e8}me"]);
        }
    }
}

#[test]
fn syntax_fts5_takes_an_fts5_expression_and_refuses_one_it_cannot_parse_or_a_column_filter() {
    for target in targets("search_fts5") {
        let engine = desks(&target);
        let fts5 = |query: &str| {
            search_as(
                &engine,
                &alice(),
                json!({"query": query, "syntax": "fts5"}),
                None,
            )
        };
        for (query, expected) in [
            ("wood OR chair", vec!["n2", "n1"]),
            ("\"walnut desk\"", vec!["n1", "n2"]),
            ("\"solid wood\" OR \"oak chair\"", vec!["n2", "n1"]),
            ("wal*", vec!["n1", "n2"]),
            ("desk NOT oak", vec!["n1"]),
            // A colon in a string is text.
            ("\"title: walnut\"", vec![]),
        ] {
            assert_eq!(ids_of(&fts5(query).expect(query)), expected, "{query}");
        }
        // Parentheses group; both match, in an order each database ranks.
        let mut grouped = ids_of(&fts5("(walnut OR oak) AND desk").expect("grouped"));
        grouped.sort();
        assert_eq!(grouped, ["n1", "n2"]);
        // A NEAR group is SQLite's alone.
        let near = fts5("NEAR(pairs desk, 3)");
        if target.dialect == Dialect::Sqlite {
            assert_eq!(ids_of(&near.expect("NEAR")), ["n2"]);
        } else {
            let issues = issues_of(&refusal(near));
            assert_eq!(issues[0].0, "/query");
            assert!(
                issues[0].1.starts_with(
                    "is not an FTS5 expression this index takes: NEAR groups are SQLite's alone"
                ),
                "{issues:?}"
            );
        }

        let deep = format!("{}walnut{}", "(".repeat(200), ")".repeat(200));
        for query in [
            "walnut AND",
            "*",
            "walnut)",
            "\"walnut",
            "-wood",
            "NEAR(walnut desk, x)",
            deep.as_str(),
        ] {
            let error = refusal(fts5(query));
            assert_eq!(error.code(), EngineErrorCode::InvalidArgument, "{query}");
            let issues = issues_of(&error);
            assert_eq!(issues[0].0, "/query", "{query}");
            assert!(
                issues[0]
                    .1
                    .starts_with("is not an FTS5 expression this index takes: "),
                "{query}: {issues:?}"
            );
        }
        // The call goes on after a refusal: a Postgres transaction an error
        // ended would refuse this.
        assert_eq!(ids_of(&fts5("walnut").expect("walnut")), ["n1", "n2"]);
        for query in ["title: walnut", "{title body}: walnut", "- title : oak"] {
            assert_eq!(
                issues_of(&refusal(fts5(query))),
                [issue(
                    "/query",
                    "holds a column filter, which search does not take: a query searches every indexed field"
                )]
            );
        }
        assert_eq!(
            code(search_as(
                &engine,
                &alice(),
                json!({"query": "walnut", "syntax": "regex"}),
                None
            )),
            EngineErrorCode::InvalidArgument
        );
    }
}

#[test]
fn it_answers_only_a_caller_who_may_read_the_schema_and_only_from_the_callers_namespace() {
    for target in targets("search_access") {
        let policy = RecordingPolicy::new(|request| {
            let subject = request.principal().subject.as_str();
            subject == "alice"
                || (subject == "bob" && request.namespace() == "east" && request.action() == "read")
                || (subject == "carl" && request.operation() == Some("search"))
        });
        let engine = target.open_core_with(|options| {
            options.policy = Some(Policy::custom(policy));
            options.namespaces = NamespaceOptions {
                names: vec!["east".to_owned(), "west".to_owned()],
                shared: None,
            };
        });
        for namespace in ["east", "west"] {
            publish(&engine, &notes(json!([search_config()])), Some(namespace)).expect("publish");
        }
        create(&engine, json!({"title": "Walnut desk"}), "n1", Some("east"));
        create(
            &engine,
            json!({"title": "Walnut chair"}),
            "n1",
            Some("west"),
        );
        let found = search_as(
            &engine,
            &named("bob"),
            json!({"query": "walnut"}),
            Some("east"),
        )
        .expect("search");
        assert_eq!(ids_of(&found), ["n1"]);
        if target.dialect == Dialect::Sqlite {
            assert_eq!(
                found["items"],
                json!([{"id": "n1", "rank": 1, "field": "title", "snippet": [{"text": "Walnut", "match": true}, {"text": " desk", "match": false}]}])
            );
        }
        assert!(ids_in(&engine, "chair", Some("east")).is_empty());
        assert_eq!(ids_in(&engine, "chair", Some("west")), ["n1"]);

        // bob may not read west's notes; carl may call search, but may not
        // read a note.
        for (principal, namespace, message) in [
            (
                named("bob"),
                "west",
                "bob may not call search (read) on Note in namespace west",
            ),
            (
                named("carl"),
                "east",
                "carl may not read Note in namespace east",
            ),
        ] {
            let error = refusal(search_as(
                &engine,
                &principal,
                json!({"query": "walnut"}),
                Some(namespace),
            ));
            assert_eq!(
                (error.code(), error.message()),
                (EngineErrorCode::Forbidden, message)
            );
        }
    }
}

#[test]
fn a_schema_of_the_shared_namespace_keeps_each_namespaces_index_to_its_own_instances() {
    for target in targets("search_shared") {
        let engine = target.open_core_with(|options| {
            options.namespaces = NamespaceOptions {
                names: vec!["shared".to_owned(), "east".to_owned(), "west".to_owned()],
                shared: Some("shared".to_owned()),
            };
        });
        publish(&engine, &notes(json!([])), Some("shared")).expect("publish");
        create(&engine, json!({"title": "Walnut desk"}), "n1", Some("east"));
        create(
            &engine,
            json!({"title": "Walnut chair"}),
            "n1",
            Some("west"),
        );
        create(
            &engine,
            json!({"title": "Walnut stool"}),
            "s1",
            Some("shared"),
        );
        // Search added in the shared namespace indexes every namespace's
        // instances, each in its own.
        assert_eq!(
            publish(&engine, &notes(json!([search_config()])), Some("shared")).expect("publish"),
            2
        );
        for (namespace, id) in [("shared", "s1"), ("east", "n1"), ("west", "n1")] {
            assert_eq!(ids_in(&engine, "walnut", Some(namespace)), [id]);
            assert_eq!(rows_of(&engine, namespace), 1);
        }
        assert!(ids_in(&engine, "chair", Some("east")).is_empty());
        assert_eq!(ids_in(&engine, "chair", Some("west")), ["n1"]);
        create(
            &engine,
            json!({"title": "Walnut shelf"}),
            "n2",
            Some("east"),
        );
        assert_eq!(ids_in(&engine, "walnut", Some("east")), ["n1", "n2"]);
    }
}

#[test]
fn a_schema_without_search_has_no_search_operation() {
    for target in targets("search_none") {
        let engine = target.open_core();
        publish(&engine, &notes(json!([])), None).expect("publish");
        let error = refusal(search_as(
            &engine,
            &alice(),
            json!({"query": "walnut"}),
            None,
        ));
        assert_eq!(
            (error.code(), error.message()),
            (
                EngineErrorCode::NotFound,
                "schema Note has no schema-level operation search (its behaviors' schema-level operations: none)"
            )
        );
    }
}

#[test]
fn its_config_names_the_types_own_text_fields_strings_and_string_scalars() {
    for target in targets("search_config") {
        let engine = target.open_core();
        let refused = |config: Value| -> Vec<String> {
            let error = refusal(engine.schemas().define(
                &alice(),
                notes(json!([{"name": "Search", "config": config}])),
                DefineOptions::default(),
            ));
            assert_eq!(error.name(), "SchemaDocumentError");
            issues_of(&error)
                .into_iter()
                .map(|(_, message)| message)
                .collect()
        };
        assert_eq!(
            refused(json!({"fields": ["title", "subject"]})),
            ["type Note: behavior Search config: fields: Note has no field subject"]
        );
        for field in ["rating", "mood", "tags", "meta"] {
            assert_eq!(
                refused(json!({"fields": [field]})),
                [format!("type Note: behavior Search config: fields: Note.{field} is not a string, or a scalar whose values are strings")]
            );
        }
        assert_eq!(
            refused(json!({"fields": ["title"], "weights": {"body": 2}})),
            ["type Note: behavior Search config: weights: body is not an indexed field (fields: title)"]
        );
        // The configSchema holds the rest: at least one field and at most
        // 16, each once, weights above 0.
        for config in [
            json!({"fields": []}),
            json!({"fields": ["title", "title"]}),
            json!({"fields": ["title"], "weights": {"title": 0}}),
            json!({}),
        ] {
            refused(config);
        }
        // A string scalar is text.
        assert_eq!(
            publish(
                &engine,
                &notes(json!([{"name": "Search", "config": {"fields": ["author"]}}])),
                None
            )
            .expect("publish"),
            1
        );
        create(
            &engine,
            json!({"title": "x", "author": "ann@example.com"}),
            "n1",
            None,
        );
        if target.dialect == Dialect::Sqlite {
            assert_eq!(
                search(&engine, json!({"query": "ann"}))["items"],
                json!([{"id": "n1", "rank": 1, "field": "author", "snippet": [{"text": "ann", "match": true}, {"text": "@example.com", "match": false}]}])
            );
        } else {
            // Postgres's parser keeps an email address whole: it is found
            // whole, and not by its parts.
            assert!(ids(&engine, "ann").is_empty());
            let found = search(&engine, json!({"query": "ann@example.com"}));
            assert_eq!(ids_of(&found), ["n1"]);
            marked(&found);
        }
    }
}

#[test]
fn a_version_that_changes_the_indexed_fields_rebuilds_the_index_of_the_instances_that_exist() {
    for target in targets("search_fields") {
        let engine = target.open_core();
        let only = |fields: Value| notes(json!([{"name": "Search", "config": {"fields": fields}}]));
        publish(&engine, &only(json!(["title"])), None).expect("publish");
        create(
            &engine,
            json!({"title": "Walnut desk", "body": "Made of oak."}),
            "n1",
            None,
        );
        assert!(ids(&engine, "oak").is_empty());
        assert_eq!(
            publish(&engine, &only(json!(["title", "body"])), None).expect("publish"),
            2
        );
        assert_eq!(ids(&engine, "oak"), ["n1"]);
        assert_eq!(
            publish(&engine, &only(json!(["body"])), None).expect("publish"),
            3
        );
        assert!(ids(&engine, "walnut").is_empty());
        let found = search(&engine, json!({"query": "oak"}));
        if target.dialect == Dialect::Sqlite {
            assert_eq!(
                found["items"],
                json!([{"id": "n1", "rank": 1, "field": "body", "snippet": [{"text": "Made of ", "match": false}, {"text": "oak", "match": true}, {"text": ".", "match": false}]}])
            );
        } else {
            assert_eq!(ids_of(&found), ["n1"]);
            assert_eq!(found["items"][0]["field"], json!("body"));
            marked(&found);
        }
        assert_eq!(rows_of(&engine, "default"), 1);
    }
}

#[test]
fn search_can_be_added_to_and_removed_from_a_schema_with_instances() {
    for target in targets("search_added") {
        let engine = target.open_core();
        publish(&engine, &notes(json!([])), None).expect("publish");
        create(&engine, json!({"title": "Walnut desk"}), "n1", None);
        create(&engine, json!({"title": "Walnut chair"}), "n2", None);
        assert_eq!(
            publish(&engine, &notes(json!([search_config()])), None).expect("publish"),
            2
        );
        assert_eq!(ids(&engine, "walnut"), ["n1", "n2"]);

        // Removed: the index goes, and writes keep none.
        assert_eq!(
            publish(&engine, &notes(json!([])), None).expect("publish"),
            3
        );
        assert_eq!(rows_of(&engine, "default"), 0);
        assert_eq!(
            code(search_as(
                &engine,
                &alice(),
                json!({"query": "walnut"}),
                None
            )),
            EngineErrorCode::NotFound
        );
        create(&engine, json!({"title": "Walnut stool"}), "n3", None);
        update(&engine, "n1", json!({"title": "Pine desk"})).expect("update");
        assert_eq!(rows_of(&engine, "default"), 0);

        // Added again: every instance is indexed as it is now.
        assert_eq!(
            publish(&engine, &notes(json!([search_config()])), None).expect("publish"),
            4
        );
        assert_eq!(ids(&engine, "walnut"), ["n2", "n3"]);
        assert_eq!(ids(&engine, "pine"), ["n1"]);
        assert_eq!(rows_of(&engine, "default"), 3);
    }
}

#[test]
fn it_runs_the_notes_document_its_title_weighing_more_and_its_embedder_pulls_their_text() {
    // core-behaviors.test.ts: fixture-search-json, whose notes are
    // searched.
    for target in targets("search_notes_document") {
        let engine = target.open_core();
        let path = repository()
            .join("internal/loader/testdata/services/fixture-search-json/src/note.schema.json");
        let document: Value =
            serde_json::from_str(&std::fs::read_to_string(&path).expect("the notes document"))
                .expect("JSON");
        engine
            .schemas()
            .define(&alice(), &document, DefineOptions::default())
            .expect("define");
        engine
            .schemas()
            .publish(&alice(), "notes", SchemaTarget::default())
            .expect("publish");
        let behaviors = engine
            .schemas()
            .behaviors(&alice(), "notes", Default::default())
            .expect("behaviors");
        assert_eq!(
            behaviors
                .iter()
                .map(|behavior| (behavior.name.as_str(), behavior.config.clone()))
                .collect::<Vec<_>>(),
            [(
                "Search",
                json!({"fields": ["title", "body"], "weights": {"title": 3}, "vectors": {"dimensions": 384, "model": "minilm-l6", "permission": "notes.embed"}})
            )]
        );
        let writer = named("wes");
        for (id, title, body) in [
            ("n1", "Standup", "The release slips a week."),
            ("n2", "Release plan", "Dates and owners."),
            ("n3", "Lunch", "Tacos."),
        ] {
            engine
                .instances()
                .create(
                    &writer,
                    "notes",
                    &json!({"title": title, "body": body}),
                    CreateOptions {
                        id: Some(id),
                        ..Default::default()
                    },
                )
                .expect("create");
        }
        let found = engine
            .instances()
            .invoke_schema(
                &writer,
                "notes",
                "search",
                &json!({"query": "release"}),
                None,
            )
            .expect("search");
        let hits: Vec<(String, String)> = found["items"]
            .as_array()
            .expect("items")
            .iter()
            .map(|hit| {
                (
                    hit["id"].as_str().expect("id").to_owned(),
                    hit["field"].as_str().expect("field").to_owned(),
                )
            })
            .collect();
        assert_eq!(
            hits,
            [
                ("n2".to_owned(), "title".to_owned()),
                ("n1".to_owned(), "body".to_owned())
            ]
        );
        let pulled = engine
            .instances()
            .invoke_schema(
                &writer,
                "notes",
                "staleEmbeddings",
                &json!({"limit": 1}),
                None,
            )
            .expect("staleEmbeddings");
        assert_eq!(pulled["model"], json!("minilm-l6"));
        assert_eq!(
            (
                pulled["items"][0]["id"].clone(),
                pulled["items"][0]["text"].clone()
            ),
            (json!("n1"), json!("Standup\n\nThe release slips a week."))
        );
        engine
            .instances()
            .delete(&writer, "notes", "n2", WriteOptions::default())
            .expect("delete");
        assert_eq!(
            engine
                .instances()
                .invoke_schema(
                    &writer,
                    "notes",
                    "search",
                    &json!({"query": "release plan"}),
                    None
                )
                .expect("search"),
            json!({"items": [], "next": null})
        );
    }
}
