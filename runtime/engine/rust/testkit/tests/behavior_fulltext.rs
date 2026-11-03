//! A behavior's full-text match kept to the schemas a namespace operation
//! may read (D58, D77): `FullTextQuery::readable` names the column of the
//! keys that holds a schema, and the match joins `sql.readable()` on it,
//! so one ranked match runs across every schema the caller may read, a
//! schema the policy refuses never in its hits, paged by offset as any
//! match. A match with `readable` outside a namespace operation is the
//! behavior's defect, and one with `None` filters nothing more. On SQLite
//! and, with the variable set, on Postgres.

mod support;

use std::sync::LazyLock;

use serde_json::{json, Value};

use superschematic_engine::behaviors::{BehaviorMigration, ColumnSpec, TableSpec};
use superschematic_engine::storage::fulltext::{FullTextQuery, Snippets};
use superschematic_engine::storage::SqlValue;
use superschematic_engine::{
    AccessRequest, Behavior, BehaviorSet, ConfigTarget, CreateOptions, Engine, Hooks,
    NamespaceCall, Policy, Principal, Result, WriteOptions,
};
use support::{alice, defect, named, schema_document, targets, RecordingPolicy, Target};

static DECLARATION: LazyLock<String> = LazyLock::new(|| {
    json!({
        "name": "test.Words",
        "description": "Words on instances, found across a namespace.",
        "configSchema": {"type": "object"},
        "operations": [
            {"name": "index", "paramsSchema": {"type": "object", "additionalProperties": false, "required": ["body"], "properties": {"body": {"type": "string"}}}, "resultSchema": true, "writes": true, "beside": true},
            {"name": "here", "paramsSchema": {"type": "object", "additionalProperties": false}, "resultSchema": true},
            {"name": "find", "paramsSchema": {"type": "object", "additionalProperties": false, "required": ["query"], "properties": {
                "query": {"type": "string"}, "readable": {"type": "boolean"}, "limit": {"type": "integer"}, "offset": {"type": "integer"}}},
             "resultSchema": true, "scope": "namespace"}
        ],
        "vetoes": []
    })
    .to_string()
});

struct Words;

const COLUMNS: [&str; 1] = ["body"];

fn query<'a>(
    expression: &'a str,
    filters: &'a [(&'a str, SqlValue)],
    readable: Option<&'a str>,
    limit: i64,
    offset: i64,
) -> FullTextQuery<'a> {
    FullTextQuery {
        index: "text",
        columns: &COLUMNS,
        weights: &[1.0],
        snippets: Snippets {
            columns: 1,
            open: "[",
            close: "]",
            ellipsis: "...",
            words: 8,
        },
        keys: "rows",
        key: "row",
        select: &["schema", "id"],
        filters,
        readable,
        exclude: None,
        expression,
        caller: false,
        limit,
        offset,
    }
}

impl Behavior for Words {
    type Config = Value;

    fn declaration(&self) -> &str {
        &DECLARATION
    }

    fn parse_config(&self, raw: &Value, _: &ConfigTarget<'_>) -> Result<Value> {
        Ok(raw.clone())
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            migrations: vec![BehaviorMigration::new(1, "words")
                .table(
                    TableSpec::new("rows")
                        .row_id("row")
                        .column("namespace", ColumnSpec::text().not_null())
                        .column("schema", ColumnSpec::text().not_null())
                        .column("id", ColumnSpec::text().not_null())
                        .unique(&["namespace", "schema", "id"]),
                )
                .up(|sql| sql.create_fulltext("text", &COLUMNS))],
            operations: vec![
                ("index", |_, cx, params| {
                    let (rows, text) = (cx.sql().table("rows")?, cx.sql().table("text")?);
                    let key = [
                        SqlValue::from(cx.namespace()),
                        SqlValue::from(cx.schema()),
                        SqlValue::from(cx.id()),
                    ];
                    cx.sql().run(
                        format!("INSERT INTO {rows} (namespace, schema, id) VALUES (?, ?, ?)"),
                        &key,
                    )?;
                    let row = cx
                        .sql()
                        .get(
                            format!("SELECT row FROM {rows} WHERE namespace = ? AND schema = ? AND id = ?"),
                            &key,
                        )?
                        .expect("its row")
                        .i64("row")?;
                    cx.sql().run(
                        format!("INSERT INTO {text} (rowid, body) VALUES (?, ?)"),
                        &[
                            SqlValue::Integer(row),
                            params["body"].as_str().unwrap_or_default().into(),
                        ],
                    )?;
                    Ok(Value::Null)
                }),
                ("here", |_, cx, _| {
                    let filters = [("namespace", SqlValue::from(cx.namespace()))];
                    cx.sql()
                        .fulltext(&query("\"walnut\"", &filters, Some("schema"), 10, 0))?
                        .expect("taken");
                    Ok(Value::Null)
                }),
            ],
            namespace_operations: vec![("find", |_, cx, params| {
                let filters = [("namespace", SqlValue::from(cx.namespace()))];
                let readable = params["readable"].as_bool().unwrap_or(true);
                let hits = cx
                    .sql()
                    .fulltext(&query(
                        params["query"].as_str().unwrap_or_default(),
                        &filters,
                        readable.then_some("schema"),
                        params["limit"].as_i64().unwrap_or(10),
                        params["offset"].as_i64().unwrap_or(0),
                    ))?
                    .expect("taken");
                Ok(Value::Array(
                    hits.iter()
                        .map(|hit| {
                            json!([
                                hit.row.text("schema").unwrap(),
                                hit.row.text("id").unwrap(),
                                hit.snippets[0]
                            ])
                        })
                        .collect(),
                ))
            })],
            ..Hooks::default()
        }
    }
}

/// An engine whose A, B and C compose test.Words, each with instances
/// holding text, and a policy that lets rita read no B and outsider no
/// schema.
fn world(target: &Target) -> Engine {
    let policy = RecordingPolicy::new(|request: &AccessRequest<'_>| {
        match request.principal().subject.as_str() {
            "rita" => request.schema() != Some("B"),
            "outsider" => request.schema().is_none(),
            _ => true,
        }
    });
    let engine = target.open_with(BehaviorSet::new().with(Words), |options| {
        options.policy = Some(Policy::custom(policy));
    });
    for name in ["A", "B", "C"] {
        let mut document = schema_document(
            name,
            json!([{"name": "title", "typeRef": {"name": "string"}}]),
            json!({}),
        );
        document["types"][name]["behaviors"] = json!([{"name": "test.Words", "config": {}}]);
        support::publish(&engine, &document, None);
    }
    for (schema, id, body) in [
        ("A", "a1", "walnut desk"),
        ("B", "b1", "walnut walnut walnut chair"),
        ("C", "c1", "a walnut"),
        ("A", "a2", "oak table"),
        ("C", "c2", "walnut walnut stool"),
    ] {
        engine
            .instances()
            .create(
                &alice(),
                schema,
                &json!({"title": id}),
                CreateOptions {
                    id: Some(id),
                    ..Default::default()
                },
            )
            .expect("create");
        engine
            .instances()
            .invoke(
                &alice(),
                schema,
                id,
                "index",
                &json!({ "body": body }),
                WriteOptions::default(),
            )
            .expect("index");
    }
    engine
}

fn find(engine: &Engine, who: &Principal, params: Value) -> Vec<Value> {
    let found = engine
        .behaviors()
        .invoke(
            who,
            &NamespaceCall {
                namespace: None,
                behavior: "test.Words",
                operation: "find",
                params: Some(&params),
            },
        )
        .expect("find");
    found.as_array().cloned().unwrap_or_default()
}

fn ids(hits: &[Value]) -> Vec<String> {
    hits.iter()
        .map(|hit| format!("{}/{}", hit[0].as_str().unwrap(), hit[1].as_str().unwrap()))
        .collect()
}

#[test]
fn a_match_kept_to_the_readable_schemas_ranks_across_them_and_leaves_the_others_out() {
    for target in targets("fulltext_readable") {
        let engine = world(&target);
        let everyone = ids(&find(&engine, &alice(), json!({"query": "\"walnut\""})));
        let mut sorted = everyone.clone();
        sorted.sort();
        assert_eq!(
            sorted,
            ["A/a1", "B/b1", "C/c1", "C/c2"],
            "{:?}",
            target.dialect
        );
        // B holds walnut three times, c2 twice: they lead on both databases.
        assert_eq!(everyone[..2], ["B/b1".to_owned(), "C/c2".to_owned()]);

        // rita may read no B: one match across A and C, in the same order,
        // B never in it.
        let rita = ids(&find(
            &engine,
            &named("rita"),
            json!({"query": "\"walnut\""}),
        ));
        let without_b: Vec<String> = everyone
            .iter()
            .filter(|id| !id.starts_with("B/"))
            .cloned()
            .collect();
        assert_eq!(rita, without_b);

        // Paged by offset over the kept hits alone.
        let second = ids(&find(
            &engine,
            &named("rita"),
            json!({"query": "\"walnut\"", "limit": 1, "offset": 1}),
        ));
        assert_eq!(second, [without_b[1].clone()]);

        // The snippet is the match's own.
        let hit = &find(&engine, &named("rita"), json!({"query": "\"oak\""}))[0];
        assert_eq!(hit[1], json!("a2"));
        assert_eq!(hit[2], json!("[oak] table"));

        // A principal that may read no schema matches nothing.
        assert!(find(&engine, &named("outsider"), json!({"query": "\"walnut\""})).is_empty());

        // With None the match filters nothing more: B is in it.
        let unfiltered = ids(&find(
            &engine,
            &named("rita"),
            json!({"query": "\"walnut\"", "readable": false}),
        ));
        assert_eq!(unfiltered, everyone);
    }
}

#[test]
fn a_match_kept_to_the_readable_schemas_is_a_namespace_operations() {
    for target in targets("fulltext_readable_outside") {
        let engine = world(&target);
        let (behavior, message) = defect(engine.instances().invoke(
            &alice(),
            "A",
            "a1",
            "here",
            &json!({}),
            WriteOptions::default(),
        ));
        assert_eq!(behavior, "test.Words");
        assert!(
            message.contains("sql.readable() is a namespace operation's"),
            "{message}"
        );
    }
}
