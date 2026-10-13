//! A behavior's storage through the engine, the cases of
//! `test/behavior-sql.test.ts` (its SQL reaches its own tables only, a read
//! cannot write, its migrations create only tables and indexes of its own)
//! and `test/behavior-own-columns.test.ts` (the read-only relation
//! `sql.instances()` names over the call's schema's instances with the
//! behavior's columns, each statement that names it asking read, and the
//! indexes a migration lists on those columns); on SQLite and, with the
//! variable set, on Postgres. The storage crate's tests hold the checker
//! to every statement form; these hold the engine to what it hands the
//! checker: the call's mode, namespace, schema, columns and policy. The
//! query plan the TypeScript test reads by wrapping the storage is not
//! portable; a migration that returns a promise has no Rust counterpart.

mod support;

use serde_json::{json, Map, Value};

use superschematic_engine::behaviors::{
    BehaviorMigration, ColumnSpec, SqlReader, TableSpec, RELATION_COLUMNS,
};
use superschematic_engine::storage::{Dialect, Row, SqlValue};
use superschematic_engine::{
    Behavior, BehaviorSet, ConfigTarget, CreateOptions, Engine, EngineErrorCode, EngineOptions,
    Error, Hooks, NamespaceOptions, Policy, Principal, Result, SchemaTarget, WriteOptions,
};
use support::{
    alice, defect, fields_of, fixtures, named, publish, publish_item, refusal, schema_document,
    tables_of, targets, texts, usage, RecordingPolicy, Target,
};

fn sql_json(value: &SqlValue) -> Value {
    match value {
        SqlValue::Null => Value::Null,
        SqlValue::Integer(integer) => json!(integer),
        SqlValue::Real(real) => json!(real),
        SqlValue::Text(text) => json!(text),
        SqlValue::Blob(bytes) => json!(bytes),
    }
}

fn rows_json(rows: &[Row]) -> Value {
    Value::Array(
        rows.iter()
            .map(|row| {
                Value::Object(
                    row.iter()
                        .map(|(column, value)| (column.to_owned(), sql_json(value)))
                        .collect::<Map<_, _>>(),
                )
            })
            .collect(),
    )
}

fn values_of(params: &Value) -> Vec<SqlValue> {
    params
        .get("values")
        .and_then(Value::as_array)
        .map(|values| {
            values
                .iter()
                .map(|value| match value {
                    Value::Null => SqlValue::Null,
                    Value::Number(number) => number.as_i64().map_or_else(
                        || SqlValue::Real(number.as_f64().unwrap_or_default()),
                        SqlValue::Integer,
                    ),
                    Value::String(text) => SqlValue::Text(text.clone()),
                    other => SqlValue::Text(other.to_string()),
                })
                .collect()
        })
        .unwrap_or_default()
}

fn sql_of(params: &Value) -> &str {
    params
        .get("sql")
        .and_then(Value::as_str)
        .unwrap_or_default()
}

const SQL_PARAMS: &str = r#"{"type": "object", "additionalProperties": false, "required": ["sql"], "properties": {"sql": {"type": "string"}, "values": {"type": "array"}}}"#;

fn sql_params() -> Value {
    serde_json::from_str(SQL_PARAMS).unwrap()
}

// test.Sneaky runs the SQL it is given, in a read and in a write; its first
// migration makes a notes table and, on SQLite, an fts5 search table.
struct Sneaky {
    more: Vec<BehaviorMigration>,
}

static SNEAKY: std::sync::LazyLock<String> = std::sync::LazyLock::new(|| {
    json!({"name": "test.Sneaky", "operations": [
        {"name": "read", "paramsSchema": sql_params(), "resultSchema": true},
        {"name": "write", "paramsSchema": sql_params(), "resultSchema": true, "writes": true},
        {"name": "name", "paramsSchema": sql_params(), "resultSchema": true}
    ]})
    .to_string()
});

fn notes_migration() -> BehaviorMigration {
    BehaviorMigration::new(1, "notes")
        .table(
            TableSpec::new("notes")
                .column("id", ColumnSpec::text().not_null())
                .column("body", ColumnSpec::text())
                .primary_key(&["id"]),
        )
        .up(|sql| {
            if sql.dialect() == Dialect::Sqlite {
                sql.run(
                    format!(
                        "CREATE VIRTUAL TABLE {} USING fts5(body)",
                        sql.table("search")?
                    ),
                    &[],
                )?;
            }
            Ok(())
        })
}

impl Behavior for Sneaky {
    type Config = Value;

    fn declaration(&self) -> &str {
        &SNEAKY
    }

    fn parse_config(&self, raw: &Value, _: &ConfigTarget<'_>) -> Result<Value> {
        Ok(raw.clone())
    }

    fn hooks(&self) -> Hooks<Self> {
        let mut migrations = vec![notes_migration()];
        migrations.extend(self.more.iter().cloned());
        Hooks {
            migrations,
            operations: vec![
                ("read", |_, cx, params| {
                    Ok(rows_json(
                        &cx.sql().all(sql_of(params), &values_of(params))?,
                    ))
                }),
                ("write", |_, cx, params| {
                    Ok(json!(
                        cx.sql().run(sql_of(params), &values_of(params))?.changes
                    ))
                }),
                ("name", |_, cx, params| {
                    Ok(json!(cx.sql().table(sql_of(params))?))
                }),
            ],
            ..Hooks::default()
        }
    }
}

fn sneaky(target: &Target) -> Engine {
    let engine = target.open(fixtures().with(Sneaky { more: Vec::new() }));
    publish_item(
        &engine,
        json!([{"name": "test.Counter"}, {"name": "test.Sneaky"}]),
    );
    engine
        .instances()
        .create(
            &alice(),
            "Item",
            &json!({"title": "Desk"}),
            CreateOptions {
                id: Some("i1"),
                ..Default::default()
            },
        )
        .unwrap();
    engine
}

fn run(engine: &Engine, operation: &str, sql: &str, values: Value) -> Result<Value> {
    engine.instances().invoke(
        &alice(),
        "Item",
        "i1",
        operation,
        &json!({"sql": sql, "values": values}),
        WriteOptions::default(),
    )
}

#[test]
fn a_behavior_reads_and_writes_its_own_tables_through_its_operations() {
    for target in targets("a_behavior_reads_and_writes_its_own_tables") {
        let engine = sneaky(&target);
        assert_eq!(
            run(
                &engine,
                "write",
                "INSERT INTO bhv_test_sneaky__notes (id, body) VALUES ('n1', 'a desk'); ",
                json!([])
            )
            .unwrap(),
            json!(1)
        );
        assert_eq!(
            run(
                &engine,
                "read",
                "SELECT body FROM bhv_test_sneaky__notes -- engine_instances is only a comment",
                json!([])
            )
            .unwrap(),
            json!([{"body": "a desk"}])
        );
        assert_eq!(
            run(&engine, "read", "WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < 3) SELECT i FROM n", json!([])).unwrap(),
            json!([{"i": 1}, {"i": 2}, {"i": 3}])
        );
        // Data that looks like a reserved name goes in as a parameter.
        assert_eq!(
            run(
                &engine,
                "write",
                "UPDATE bhv_test_sneaky__notes SET body = ?",
                json!(["engine_instances"])
            )
            .unwrap(),
            json!(1)
        );
        if target.dialect == Dialect::Sqlite {
            run(
                &engine,
                "write",
                "INSERT INTO \"BHV_TEST_SNEAKY__SEARCH\" (body) VALUES (?)",
                json!(["a walnut desk"]),
            )
            .unwrap();
            assert_eq!(
                run(&engine, "read", "/* engine_events */ SELECT body FROM bhv_test_sneaky__search WHERE bhv_test_sneaky__search MATCH ?", json!(["walnut"])).unwrap(),
                json!([{"body": "a walnut desk"}])
            );
        }
    }
}

#[test]
fn sql_naming_anything_outside_the_behaviors_storage_is_a_behavior_error_before_it_runs() {
    for target in targets("sql_naming_anything_outside") {
        let engine = sneaky(&target);
        let mut outside = vec![
            ("SELECT data FROM engine_instances", "engine_instances"),
            (
                "UPDATE engine_instances SET bhv_test_counter__count = 99",
                "engine_instances",
            ),
            ("SELECT * FROM \"ENGINE_EVENTS\"", "ENGINE_EVENTS"),
            (
                "DELETE FROM bhv_test_counter__history",
                "bhv_test_counter__history",
            ),
            (
                "SELECT body FROM bhv_test_sneaky__notes WHERE body = 'engine_like'",
                "engine_like",
            ),
        ];
        if target.dialect == Dialect::Sqlite {
            outside.push(("SELECT name FROM sqlite_master", "sqlite_master"));
        }
        for (sql, name) in outside {
            assert_eq!(
                defect(run(&engine, "write", sql, json!([]))),
                (
                    "test.Sneaky".to_owned(),
                    format!("behavior test.Sneaky: SQL refused: it names {name}, which is not the behavior's own storage (bhv_test_sneaky__*): {sql}")
                ),
                "{sql}"
            );
        }
        for (sql, message) in [
            ("BEGIN", "not BEGIN"),
            ("COMMIT", "not COMMIT"),
            ("CREATE TABLE bhv_test_sneaky__more (x TEXT)", "not CREATE"),
            ("DROP TABLE bhv_test_sneaky__notes", "not DROP"),
            (
                "INSERT INTO bhv_test_sneaky__notes (id) VALUES ('n2'); DELETE FROM bhv_test_sneaky__notes",
                "the SQL holds more than one statement; run one at a time",
            ),
            ("", "the SQL holds no statement"),
        ] {
            let (_, refused) = defect(run(&engine, "write", sql, json!([])));
            assert!(refused.contains(message), "{sql}: {refused}");
        }
        assert_eq!(
            fields_of(
                &engine
                    .instances()
                    .get(&alice(), "Item", "i1", Default::default())
                    .unwrap()
                    .unwrap()
            ),
            json!({"data": {"title": "Desk"}, "behaviors": {"test.Counter": {"count": 0}}})
        );
    }
}

#[test]
fn a_read_only_operation_runs_select_values_and_with_select_only() {
    for target in targets("a_read_only_operation_runs_select") {
        let engine = sneaky(&target);
        for (sql, message) in [
            ("INSERT INTO bhv_test_sneaky__notes (id) VALUES ('n9') RETURNING id", "a read runs SELECT, VALUES or WITH ... SELECT, not INSERT"),
            ("DELETE FROM bhv_test_sneaky__notes RETURNING id", "not DELETE"),
            (
                "WITH gone AS (SELECT id FROM bhv_test_sneaky__notes) DELETE FROM bhv_test_sneaky__notes WHERE id IN (SELECT id FROM gone) RETURNING id",
                "not WITH ... DELETE",
            ),
        ] {
            let (_, refused) = defect(run(&engine, "read", sql, json!([])));
            assert!(refused.contains(message), "{sql}: {refused}");
        }
        assert_eq!(
            run(&engine, "read", "VALUES (1)", json!([])).unwrap(),
            json!([{"column1": 1}])
        );
        assert_eq!(
            run(
                &engine,
                "read",
                "SELECT id FROM bhv_test_sneaky__notes",
                json!([])
            )
            .unwrap(),
            json!([])
        );
    }
}

#[test]
fn a_table_name_is_the_behaviors_own_name_for_it() {
    for target in targets("a_table_name_is_the_behaviors_own") {
        let engine = sneaky(&target);
        assert_eq!(
            run(&engine, "name", "notes", json!([])).unwrap(),
            json!("bhv_test_sneaky__notes")
        );
        for name in ["Notes", "../x", "a b", ""] {
            let (_, refused) = defect(run(&engine, "name", name, json!([])));
            assert!(refused.contains("must match"), "{name}: {refused}");
        }
    }
}

#[test]
fn a_migration_that_reaches_past_its_own_storage_refuses_the_publish_which_leaves_nothing() {
    for target in targets("a_migration_that_reaches_past") {
        let cases: Vec<(BehaviorMigration, &str)> = vec![
            (
                BehaviorMigration::new(2, "view").up(|sql| {
                    sql.run(format!("CREATE VIEW {} AS SELECT 1", sql.table("v")?), &[])
                        .map(|_| ())
                }),
                "not CREATE VIEW",
            ),
            (
                BehaviorMigration::new(2, "engine").up(|sql| {
                    sql.run("ALTER TABLE engine_instances ADD COLUMN x TEXT", &[])
                        .map(|_| ())
                }),
                "it names engine_instances",
            ),
            (
                BehaviorMigration::new(2, "rename").up(|sql| {
                    sql.run(
                        format!("ALTER TABLE {} RENAME TO notes", sql.table("notes")?),
                        &[],
                    )
                    .map(|_| ())
                }),
                "migration 2 \"rename\" created table notes",
            ),
        ];
        for (migration, message) in cases {
            let name = migration.name.clone();
            let engine = target.open(BehaviorSet::new().with(Sneaky {
                more: vec![migration],
            }));
            let mut document = schema_document("Item", json!([]), json!({}));
            document["types"]["Item"]["behaviors"] = json!([{"name": "test.Sneaky"}]);
            engine
                .schemas()
                .define(&alice(), &document, Default::default())
                .unwrap();
            let (behavior, refused) = defect(engine.schemas().publish(
                &alice(),
                "Item",
                SchemaTarget::default(),
            ));
            assert_eq!(behavior, "test.Sneaky", "{name}");
            assert!(refused.contains(message), "{name}: {refused}");
            assert_eq!(tables_of(&engine, "bhv_"), Vec::<String>::new(), "{name}");
            assert_eq!(tables_of(&engine, "notes"), Vec::<String>::new(), "{name}");
            assert!(engine
                .schemas()
                .live(&alice(), "Item", SchemaTarget::default())
                .unwrap()
                .is_none());
        }
    }
}

#[test]
fn a_migration_creates_an_fts5_table_of_its_own_if_not_exists_and_quoted_alike() {
    for target in targets("a_migration_creates_an_fts5_table") {
        if target.dialect != Dialect::Sqlite {
            continue;
        }
        let more = BehaviorMigration::new(2, "more").up(|sql| {
            sql.run(format!("CREATE VIRTUAL TABLE IF NOT EXISTS {} USING fts5(title)", sql.table("titles")?), &[])?;
            sql.run(
                format!("CREATE VIRTUAL TABLE \"{}\" USING FTS5(body, tokenize = 'unicode61 remove_diacritics 2')", sql.table("bodies")?),
                &[],
            )?;
            Ok(())
        });
        let engine = target.open(fixtures().with(Sneaky { more: vec![more] }));
        publish_item(&engine, json!([{"name": "test.Sneaky"}]));
        let shadow = ["_data", "_idx", "_content", "_docsize", "_config"];
        assert_eq!(
            tables_of(&engine, "bhv_test_sneaky__")
                .into_iter()
                .filter(|name| !shadow.iter().any(|suffix| name.ends_with(suffix))
                    && !name.starts_with("sqlite_autoindex"))
                .filter(|name| !name.contains("pkey") && !name.contains("autoindex"))
                .collect::<Vec<_>>(),
            [
                "bhv_test_sneaky__bodies",
                "bhv_test_sneaky__notes",
                "bhv_test_sneaky__search",
                "bhv_test_sneaky__titles"
            ]
        );
    }
}

const RELATION: &str = "bhv_test_queue___instances";

// fill puts the relation's name and the tags table's in a statement the
// test writes with {instances} and {tags}.
fn fill(sql: &SqlReader<'_>, text: &str) -> Result<String> {
    let mut out = text.to_owned();
    if out.contains("{instances}") {
        out = out.replace("{instances}", &sql.instances()?);
    }
    Ok(out.replace("{tags}", &sql.table("tags")?))
}

fn queue_migrations() -> Vec<BehaviorMigration> {
    vec![
        BehaviorMigration::new(1, "rank")
            .column("rank", ColumnSpec::integer().not_null().default(0))
            .column("holder", ColumnSpec::text())
            .table(
                TableSpec::new("tags")
                    .column("namespace", ColumnSpec::text().not_null())
                    .column("schema", ColumnSpec::text().not_null())
                    .column("id", ColumnSpec::text().not_null())
                    .column("tag", ColumnSpec::text().not_null())
                    .primary_key(&["namespace", "schema", "id", "tag"]),
            ),
        BehaviorMigration::new(2, "indexes")
            .index("by_rank", &["rank"])
            .index("by_holder", &["holder", "rank"]),
    ]
}

/// What a test has test.Queue's afterConfigChange run.
type PublishHook = fn(&superschematic_engine::behaviors::PublishCx<'_, Value>) -> Result<()>;

// test.Queue ranks instances, tags them in a table of its own, and runs
// the SQL a test gives it over its columns across the schema.
struct Queue {
    migrations: Vec<BehaviorMigration>,
    // What afterConfigChange runs, when a test gives it something.
    publishing: Option<PublishHook>,
}

impl Queue {
    fn new() -> Self {
        Queue {
            migrations: queue_migrations(),
            publishing: None,
        }
    }

    fn with(migrations: Vec<BehaviorMigration>) -> Self {
        Queue {
            migrations,
            publishing: None,
        }
    }
}

static QUEUE: std::sync::LazyLock<String> = std::sync::LazyLock::new(|| {
    json!({
        "name": "test.Queue",
        "description": "Ranks instances and reads the ranks across the schema.",
        "fields": [{"name": "ahead", "description": "How many instances of the schema rank above this one."}],
        "operations": [
            {"name": "rank", "paramsSchema": {"type": "object", "additionalProperties": false, "required": ["rank"], "properties": {"rank": {"type": "integer"}, "holder": {"type": ["string", "null"]}}}, "resultSchema": true, "writes": true},
            {"name": "tag", "paramsSchema": {"type": "object", "additionalProperties": false, "required": ["tag"], "properties": {"tag": {"type": "string"}}}, "resultSchema": true, "writes": true},
            {"name": "relation", "paramsSchema": {"type": "object", "additionalProperties": false}, "resultSchema": {"type": "string"}},
            {"name": "read", "paramsSchema": sql_params(), "resultSchema": true},
            {"name": "write", "paramsSchema": sql_params(), "resultSchema": true, "writes": true},
            {"name": "select", "scope": "schema", "paramsSchema": sql_params(), "resultSchema": true}
        ]
    })
    .to_string()
});

impl Behavior for Queue {
    type Config = Value;

    fn declaration(&self) -> &str {
        &QUEUE
    }

    fn parse_config(&self, raw: &Value, _: &ConfigTarget<'_>) -> Result<Value> {
        Ok(raw.clone())
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            migrations: self.migrations.clone(),
            config_change: self.publishing.map(|_| {
                (|_, _, _, _| Ok(None)) as superschematic_engine::behaviors::ConfigChangeFn<Self>
            }),
            after_config_change: self.publishing.map(|_| {
                (|queue: &Queue, cx: &superschematic_engine::behaviors::PublishCx<'_, Value>| {
                    (queue.publishing.expect("a publishing hook"))(cx)
                }) as superschematic_engine::behaviors::AfterConfigChangeFn<Self>
            }),
            operations: vec![
                ("rank", |_, cx, params| {
                    let mut set = vec![(
                        "rank",
                        SqlValue::Integer(params["rank"].as_i64().unwrap_or_default()),
                    )];
                    if let Some(holder) = params.get("holder") {
                        set.push((
                            "holder",
                            holder.as_str().map_or(SqlValue::Null, SqlValue::from),
                        ));
                    }
                    cx.columns().set(&set)?;
                    Ok(Value::Null)
                }),
                ("tag", |_, cx, params| {
                    let table = cx.sql().table("tags")?;
                    cx.sql().run(
                        format!(
                            "INSERT INTO {table} (namespace, schema, id, tag) VALUES (?, ?, ?, ?)"
                        ),
                        &[
                            cx.namespace().into(),
                            cx.schema().into(),
                            cx.id().into(),
                            params["tag"].as_str().unwrap_or_default().into(),
                        ],
                    )?;
                    Ok(Value::Null)
                }),
                ("relation", |_, cx, _| Ok(json!(cx.sql().instances()?))),
                ("read", |_, cx, params| {
                    let sql = fill(&cx.sql(), sql_of(params))?;
                    Ok(rows_json(&cx.sql().all(sql, &values_of(params))?))
                }),
                ("write", |_, cx, params| {
                    let sql = fill(&cx.sql(), sql_of(params))?;
                    Ok(json!(cx.sql().run(sql, &values_of(params))?.changes))
                }),
            ],
            schema_operations: vec![("select", |_, cx, params| {
                let sql = fill(&cx.sql(), sql_of(params))?;
                Ok(rows_json(&cx.sql().all(sql, &values_of(params))?))
            })],
            fields: vec![("ahead", |_, view| {
                let relation = view.sql().instances()?;
                let row = view.sql().get(
                    format!("SELECT COUNT(*) AS n FROM {relation} WHERE rank > (SELECT rank FROM {relation} WHERE id = ?)"),
                    &[view.id().into()],
                )?;
                Ok(row.map(|row| sql_json(&row.values()[0])))
            })],
            ..Hooks::default()
        }
    }
}

// test.Other keeps a column of its own on the same instances.
struct Other;

impl Behavior for Other {
    type Config = Value;

    fn declaration(&self) -> &str {
        r#"{"name": "test.Other", "description": "Keeps a secret on each instance."}"#
    }

    fn parse_config(&self, raw: &Value, _: &ConfigTarget<'_>) -> Result<Value> {
        Ok(raw.clone())
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            migrations: vec![
                BehaviorMigration::new(1, "secret").column("secret", ColumnSpec::text())
            ],
            initialize: Some(|_, cx, _| cx.columns().set(&[("secret", SqlValue::from("hidden"))])),
            ..Hooks::default()
        }
    }
}

fn queue_document(name: &str) -> Value {
    let mut document = schema_document(
        name,
        json!([{"name": "title", "typeRef": {"name": "string"}, "required": true}]),
        json!({}),
    );
    document["types"][name]["behaviors"] = json!([{"name": "test.Queue"}, {"name": "test.Other"}]);
    document
}

fn open_queue(target: &Target, configure: impl FnOnce(&mut EngineOptions)) -> Engine {
    target.open_with(BehaviorSet::new().with(Queue::new()).with(Other), configure)
}

fn invoke(engine: &Engine, id: &str, operation: &str, params: Value) -> Result<Value> {
    engine.instances().invoke(
        &alice(),
        "Item",
        id,
        operation,
        &params,
        WriteOptions::default(),
    )
}

// Item i1 ranks 3 with holder w1, i2 ranks 1, i3 ranks 2 with holder w2.
fn ranked(target: &Target, configure: impl FnOnce(&mut EngineOptions)) -> Engine {
    let engine = open_queue(target, configure);
    publish(&engine, &queue_document("Item"), None);
    for (id, title, rank, holder) in [
        ("i1", "Desk", 3, json!("w1")),
        ("i2", "Lamp", 1, Value::Null),
        ("i3", "Rug", 2, json!("w2")),
    ] {
        engine
            .instances()
            .create(
                &alice(),
                "Item",
                &json!({ "title": title }),
                CreateOptions {
                    id: Some(id),
                    ..Default::default()
                },
            )
            .unwrap();
        invoke(&engine, id, "rank", json!({"rank": rank, "holder": holder})).unwrap();
    }
    engine
}

fn select_as(engine: &Engine, who: &Principal, sql: &str, values: Value) -> Result<Value> {
    engine.instances().invoke_schema(
        who,
        "Item",
        "select",
        &json!({"sql": sql, "values": values}),
        None,
    )
}

fn select(engine: &Engine, sql: &str, values: Value) -> Value {
    select_as(engine, &alice(), sql, values).unwrap()
}

// The SQL that reads an instance's title out of its data.
fn title(engine: &Engine) -> &'static str {
    match engine.dialect() {
        Dialect::Sqlite => "json_extract(data, '$.title')",
        Dialect::Postgres => "CAST(data AS jsonb) ->> 'title'",
    }
}

#[test]
fn the_relation_holds_the_calls_schemas_instances_with_their_metadata_own_fields_and_the_behaviors_columns(
) {
    for target in targets("the_relation_holds_the_calls_schemas_instances") {
        let engine = ranked(&target, |_| {});
        assert_eq!(
            invoke(&engine, "i1", "relation", json!({})).unwrap(),
            json!(RELATION)
        );
        let rows = select(&engine, "SELECT * FROM {instances} ORDER BY id", json!([]));
        let rows = rows.as_array().unwrap();
        let mut columns: Vec<&str> = RELATION_COLUMNS.to_vec();
        columns.extend(["rank", "holder"]);
        for row in rows {
            assert_eq!(
                row.as_object()
                    .unwrap()
                    .keys()
                    .map(String::as_str)
                    .collect::<Vec<_>>(),
                columns
            );
        }
        let desk = engine
            .instances()
            .get(&alice(), "Item", "i1", Default::default())
            .unwrap()
            .unwrap();
        assert_eq!(
            rows[0],
            json!({
                "id": "i1",
                "seq": desk.seq,
                "version": 1,
                "created_at": desk.created_at,
                "created_by": "alice",
                "updated_at": desk.updated_at,
                "updated_by": "alice",
                "data": "{\"title\":\"Desk\"}",
                "rank": 3,
                "holder": "w1"
            })
        );
        assert_eq!(
            rows.iter()
                .map(|row| json!([row["id"], row["rank"], row["holder"]]))
                .collect::<Vec<_>>(),
            [
                json!(["i1", 3, "w1"]),
                json!(["i2", 1, null]),
                json!(["i3", 2, "w2"])
            ]
        );
        // A view reads it too: a field reader counts the instances ranked above.
        assert_eq!(
            fields_of(
                &engine
                    .instances()
                    .get(&alice(), "Item", "i2", Default::default())
                    .unwrap()
                    .unwrap()
            ),
            json!({"data": {"title": "Lamp"}, "behaviors": {"test.Queue": {"ahead": 2}}})
        );
        assert_eq!(
            invoke(&engine, "i3", "read", json!({"sql": format!("SELECT {} AS title FROM {{instances}} WHERE rank >= ? ORDER BY rank", title(&engine)), "values": [2]})).unwrap(),
            json!([{"title": "Rug"}, {"title": "Desk"}])
        );
    }
}

#[test]
fn another_behaviors_columns_are_not_in_it_and_the_checker_still_refuses_what_is_not_the_behaviors()
{
    for target in targets("another_behaviors_columns_are_not_in_it") {
        let engine = ranked(&target, |_| {});
        for column in ["secret", "bhv_test_queue__rank"] {
            let error = select_as(
                &engine,
                &alice(),
                &format!("SELECT {column} FROM {{instances}}"),
                json!([]),
            )
            .unwrap_err();
            assert!(error.to_string().contains(column), "{column}: {error}");
            assert!(
                !matches!(error, Error::Behavior(_)),
                "{column}: the database's error, {error:?}"
            );
        }
        let mut refused = vec![
            (
                "SELECT bhv_test_other__secret FROM {instances}",
                "bhv_test_other__secret",
            ),
            (
                "SELECT i.id FROM {instances} i JOIN engine_instances e ON e.id = i.id",
                "engine_instances",
            ),
            (
                "SELECT id FROM {instances} WHERE id IN (SELECT id FROM \"ENGINE_INSTANCES\")",
                "ENGINE_INSTANCES",
            ),
        ];
        if target.dialect == Dialect::Sqlite {
            refused.push((
                "SELECT id FROM {instances} UNION SELECT name FROM sqlite_master",
                "sqlite_master",
            ));
        }
        for (sql, name) in refused {
            assert_eq!(
                defect(select_as(&engine, &alice(), sql, json!([]))).1,
                format!(
                    "behavior test.Queue: SQL refused: it names {name}, which is not the behavior's own storage (bhv_test_queue__*): {}",
                    sql.replace("{instances}", RELATION)
                ),
                "{sql}"
            );
        }
    }
}

#[test]
fn it_shows_the_namespace_its_call_runs_in_and_the_schema() {
    for target in targets("it_shows_the_namespace_its_call_runs_in") {
        let engine = open_queue(&target, |options| {
            options.namespaces = NamespaceOptions {
                names: vec!["east".into(), "west".into(), "library".into()],
                shared: Some("library".into()),
            }
        });
        publish(&engine, &queue_document("Item"), Some("library"));
        publish(&engine, &queue_document("Thing"), Some("east"));
        for (schema, id, title, namespace) in [
            ("Item", "i1", "East desk", "east"),
            ("Item", "i2", "East lamp", "east"),
            ("Item", "i1", "West desk", "west"),
            ("Thing", "t1", "A thing", "east"),
        ] {
            engine
                .instances()
                .create(
                    &alice(),
                    schema,
                    &json!({ "title": title }),
                    CreateOptions {
                        id: Some(id),
                        namespace: Some(namespace),
                        ..Default::default()
                    },
                )
                .unwrap();
        }
        let titles = |schema: &str, namespace: &str| -> Vec<String> {
            let sql = format!(
                "SELECT id, {} AS title FROM {{instances}} ORDER BY id",
                title(&engine)
            );
            engine
                .instances()
                .invoke_schema(
                    &alice(),
                    schema,
                    "select",
                    &json!({ "sql": sql }),
                    Some(namespace),
                )
                .unwrap()
                .as_array()
                .unwrap()
                .iter()
                .map(|row| {
                    format!(
                        "{} {}",
                        row["id"].as_str().unwrap(),
                        row["title"].as_str().unwrap()
                    )
                })
                .collect()
        };
        assert_eq!(titles("Item", "east"), ["i1 East desk", "i2 East lamp"]);
        assert_eq!(titles("Item", "west"), ["i1 West desk"]);
        assert!(titles("Item", "library").is_empty());
        assert_eq!(titles("Thing", "east"), ["t1 A thing"]);
    }
}

#[test]
fn it_joins_the_behaviors_own_tables_merges_into_with_and_the_statements_parameters_bind_as_written(
) {
    for target in targets("it_joins_the_behaviors_own_tables") {
        let engine = ranked(&target, |_| {});
        for (id, tag) in [("i1", "red"), ("i2", "blue"), ("i3", "red")] {
            invoke(&engine, id, "tag", json!({ "tag": tag })).unwrap();
        }
        assert_eq!(
            select(
                &engine,
                "SELECT i.id, t.tag FROM {instances} i JOIN {tags} t ON t.namespace = ? AND t.schema = ? AND t.id = i.id WHERE t.tag = ? AND i.rank > ? ORDER BY i.rank",
                json!(["default", "Item", "red", 1])
            ),
            json!([{"id": "i3", "tag": "red"}, {"id": "i1", "tag": "red"}])
        );
        assert_eq!(
            select(&engine, "WITH top AS (SELECT id, rank FROM {instances} WHERE rank >= ?) SELECT id FROM top ORDER BY rank DESC", json!([2])),
            json!([{"id": "i1"}, {"id": "i3"}])
        );
        // A quoted name folds case on SQLite alone.
        let quoted = match target.dialect {
            Dialect::Sqlite => "\"BHV_TEST_QUEUE___INSTANCES\"",
            Dialect::Postgres => "\"bhv_test_queue___instances\"",
        };
        assert_eq!(
            select(
                &engine,
                &format!("/* ranks one to n */ with recursive n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < ?) SELECT n.i, x.id FROM n JOIN {quoted} x ON x.rank = n.i ORDER BY n.i"),
                json!([2])
            ),
            json!([{"i": 1, "id": "i2"}, {"i": 2, "id": "i3"}])
        );
        // The relation named twice.
        assert_eq!(
            select(&engine, "SELECT a.id FROM {instances} a JOIN {instances} b ON b.rank = a.rank - ? WHERE a.id <> ? ORDER BY a.id", json!([1, "i3"])),
            json!([{"id": "i1"}])
        );
        if target.dialect == Dialect::Sqlite {
            // Numbered parameters.
            assert_eq!(
                select(&engine, "SELECT a.id FROM {instances} a JOIN {instances} b ON b.rank = a.rank - ?2 WHERE a.id <> ?1 ORDER BY a.id", json!(["i3", 1])),
                json!([{"id": "i1"}])
            );
        }
        // Postgres types a parameter nothing else types as text.
        let seven = match target.dialect {
            Dialect::Sqlite => json!(7),
            Dialect::Postgres => json!("7"),
        };
        assert_eq!(
            select(&engine, "VALUES (?)", json!([7])),
            json!([{ "column1": seven }])
        );
        // A write reads it: its own table takes a row per instance it selects.
        assert_eq!(
            invoke(&engine, "i1", "write", json!({
                "sql": "INSERT INTO {tags} (namespace, schema, id, tag) SELECT ?, ?, id, 'held' FROM {instances} WHERE holder IS NOT NULL AND rank > ?",
                "values": ["default", "Item", 2]
            }))
            .unwrap(),
            json!(1)
        );
        assert_eq!(
            select(
                &engine,
                "SELECT id FROM {tags} WHERE tag = 'held'",
                json!([])
            ),
            json!([{"id": "i1"}])
        );
    }
}

#[test]
fn each_statement_that_names_it_asks_read_on_the_schema_as_the_caller_once_and_a_refusal_is_forbidden(
) {
    for target in targets("each_statement_that_names_it_asks_read") {
        let policy = RecordingPolicy::new(|_| true);
        let recorded = policy.clone();
        let engine = ranked(&target, move |options| {
            options.policy = Some(Policy::custom(policy))
        });
        recorded.take();
        let bob = named("bob");
        select_as(
            &engine,
            &bob,
            "SELECT a.id FROM {instances} a JOIN {instances} b ON a.id = b.id",
            json!([]),
        )
        .unwrap();
        assert_eq!(
            recorded.take(),
            [
                json!({"principal": "bob", "action": "read", "namespace": "default", "schema": "Item", "operation": "select"}),
                json!({"principal": "bob", "action": "read", "namespace": "default", "schema": "Item"}),
            ]
        );
        select_as(&engine, &bob, "SELECT COUNT(*) AS n FROM {tags}", json!([])).unwrap();
        assert_eq!(
            recorded.take(),
            [
                json!({"principal": "bob", "action": "read", "namespace": "default", "schema": "Item", "operation": "select"})
            ],
            "a statement on its own tables asks nothing more"
        );
    }
    for target in targets("a_refusal_of_the_relation_is_forbidden") {
        // bob may call the operation, and not read the schema.
        let policy = RecordingPolicy::new(|request| {
            request.principal().subject == "alice" || request.operation().is_some()
        });
        let recorded = policy.clone();
        let engine = ranked(&target, move |options| {
            options.policy = Some(Policy::custom(policy))
        });
        let bob = named("bob");
        let refused = refusal(select_as(
            &engine,
            &bob,
            "SELECT id FROM {instances}",
            json!([]),
        ));
        assert_eq!(
            (refused.code(), refused.message()),
            (
                EngineErrorCode::Forbidden,
                "bob may not read Item in namespace default"
            )
        );
        assert_eq!(
            recorded.take().last().unwrap(),
            &json!({"principal": "bob", "action": "read", "namespace": "default", "schema": "Item"})
        );
        assert_eq!(
            select_as(&engine, &bob, "SELECT COUNT(*) AS n FROM {tags}", json!([])).unwrap(),
            json!([{"n": 0}])
        );
    }
}

#[test]
fn nothing_writes_through_it() {
    for target in targets("nothing_writes_through_it") {
        let engine = ranked(&target, |_| {});
        for sql in [
            "UPDATE {instances} SET rank = 99",
            "DELETE FROM {instances} WHERE id = 'i1'",
            "INSERT INTO {instances} (id, rank) VALUES ('x', 1)",
            "WITH t AS (SELECT 1) UPDATE {instances} SET holder = NULL",
        ] {
            let error = invoke(&engine, "i1", "write", json!({ "sql": sql })).unwrap_err();
            assert!(error.to_string().contains(RELATION), "{sql}: {error}");
        }
        assert!(defect(invoke(
            &engine,
            "i1",
            "read",
            json!({"sql": "UPDATE {instances} SET rank = 99"})
        ))
        .1
        .contains("a read runs SELECT, VALUES or WITH ... SELECT, not UPDATE"));
        assert_eq!(
            select(
                &engine,
                "SELECT id, rank, holder FROM {instances} ORDER BY id",
                json!([])
            )
            .as_array()
            .unwrap()
            .len(),
            3
        );
        assert_eq!(
            select(
                &engine,
                "SELECT rank FROM {instances} WHERE id = ?",
                json!(["i1"])
            ),
            json!([{"rank": 3}])
        );
    }
}

#[test]
fn a_migration_and_after_config_change_have_no_relation_and_a_migration_may_not_name_what_the_engine_keeps(
) {
    for target in targets("a_migration_and_after_config_change_have_no_relation") {
        let cases: Vec<(BehaviorMigration, &str)> = vec![
            (
                BehaviorMigration::new(3, "reads").up(|sql| sql.instances().map(|_| ())),
                "sql.instances() reads as a principal, and a migration and afterConfigChange act for none",
            ),
            (
                BehaviorMigration::new(3, "shadow").up(|sql| {
                    sql.run(format!("CREATE TABLE {RELATION} (id TEXT)"), &[]).map(|_| ())
                }),
                "it names bhv_test_queue___instances, which is under bhv_test_queue___, the names the engine keeps",
            ),
            (
                BehaviorMigration::new(3, "drop")
                    .up(|sql| sql.run("DROP INDEX bhv_test_queue___index_by_rank", &[]).map(|_| ())),
                "it names bhv_test_queue___index_by_rank, which is under bhv_test_queue___",
            ),
        ];
        for (migration, message) in cases {
            let name = migration.name.clone();
            let mut migrations = queue_migrations();
            migrations.push(migration);
            let engine = target.open(BehaviorSet::new().with(Queue::with(migrations)).with(Other));
            engine
                .schemas()
                .define(&alice(), queue_document("Item"), Default::default())
                .unwrap();
            let (_, refused) = defect(engine.schemas().publish(
                &alice(),
                "Item",
                SchemaTarget::default(),
            ));
            assert!(refused.contains(message), "{name}: {refused}");
            assert!(engine
                .schemas()
                .live(&alice(), "Item", SchemaTarget::default())
                .unwrap()
                .is_none());
        }
        let publishing = Queue {
            migrations: queue_migrations(),
            publishing: Some(|cx| cx.sql().instances().map(|_| ())),
        };
        let engine = target.open(BehaviorSet::new().with(publishing).with(Other));
        engine
            .schemas()
            .define(&alice(), queue_document("Item"), Default::default())
            .unwrap();
        assert!(defect(
            engine
                .schemas()
                .publish(&alice(), "Item", SchemaTarget::default())
        )
        .1
        .contains("sql.instances() reads as a principal"));
    }
}

// test.Clash keeps a column named like one of the relation's.
struct Clash;

impl Behavior for Clash {
    type Config = Value;

    fn declaration(&self) -> &str {
        static DECLARATION: std::sync::LazyLock<String> = std::sync::LazyLock::new(|| {
            json!({"name": "test.Clash", "operations": [{"name": "select", "scope": "schema", "paramsSchema": sql_params(), "resultSchema": true}]}).to_string()
        });
        &DECLARATION
    }

    fn parse_config(&self, raw: &Value, _: &ConfigTarget<'_>) -> Result<Value> {
        Ok(raw.clone())
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            migrations: vec![
                BehaviorMigration::new(1, "version").column("version", ColumnSpec::integer()),
                BehaviorMigration::new(2, "notes")
                    .table(TableSpec::new("notes").column("id", ColumnSpec::text())),
            ],
            schema_operations: vec![("select", |_, cx, params| {
                let sql = sql_of(params).replace("{notes}", &cx.sql().table("notes")?);
                Ok(rows_json(&cx.sql().all(sql, &[])?))
            })],
            ..Hooks::default()
        }
    }
}

#[test]
fn a_column_named_like_one_of_the_relations_makes_the_relation_a_behavior_error_and_nothing_else() {
    for target in targets("a_column_named_like_one_of_the_relations") {
        let engine = target.open(BehaviorSet::new().with(Clash));
        let mut document = schema_document(
            "Item",
            json!([{"name": "title", "typeRef": {"name": "string"}, "required": true}]),
            json!({}),
        );
        document["types"]["Item"]["behaviors"] = json!([{"name": "test.Clash"}]);
        publish(&engine, &document, None);
        let select = |sql: &str| {
            engine.instances().invoke_schema(
                &alice(),
                "Item",
                "select",
                &json!({ "sql": sql }),
                None,
            )
        };
        assert_eq!(
            select("SELECT COUNT(*) AS n FROM {notes}").unwrap(),
            json!([{"n": 0}])
        );
        assert!(defect(select("SELECT COUNT(*) AS n FROM bhv_test_clash___instances"))
            .1
            .contains("behavior test.Clash: sql.instances(): its column version shares a name with the relation's own columns"));
    }
}

fn queue_indexes(engine: &Engine) -> Vec<String> {
    let sql = match engine.dialect() {
        Dialect::Sqlite => "SELECT name FROM sqlite_master WHERE type = 'index' AND tbl_name = 'engine_instances' AND substr(name, 1, length(?)) = ? ORDER BY name",
        Dialect::Postgres => "SELECT indexname::text FROM pg_catalog.pg_indexes WHERE schemaname = current_schema() AND tablename = 'engine_instances' AND substr(indexname::text, 1, length(?)) = ? ORDER BY indexname COLLATE \"C\"",
    };
    let prefix = SqlValue::from("bhv_test_queue___index");
    texts(engine, sql, &[prefix.clone(), prefix])
}

fn index_columns(engine: &Engine, index: &str) -> Vec<String> {
    let sql = match engine.dialect() {
        Dialect::Sqlite => "SELECT name FROM pragma_index_info(?) ORDER BY seqno",
        Dialect::Postgres => {
            "SELECT a.attname::text FROM pg_catalog.pg_index i JOIN pg_catalog.pg_class c ON c.oid = i.indexrelid JOIN pg_catalog.pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = ANY(i.indkey) WHERE c.relname = ? AND c.relnamespace = current_schema()::regnamespace ORDER BY array_position(i.indkey::int2[], a.attnum)"
        }
    };
    texts(engine, sql, &[SqlValue::from(index)])
}

#[test]
fn a_migrations_index_leads_with_the_namespace_and_schema() {
    for target in targets("a_migrations_index_leads_with_the_namespace") {
        let engine = ranked(&target, |_| {});
        assert_eq!(
            queue_indexes(&engine),
            [
                "bhv_test_queue___index_by_holder",
                "bhv_test_queue___index_by_rank"
            ]
        );
        assert_eq!(
            index_columns(&engine, "bhv_test_queue___index_by_holder"),
            [
                "namespace",
                "schema",
                "bhv_test_queue__holder",
                "bhv_test_queue__rank"
            ]
        );
    }
}

#[test]
fn registration_refuses_an_index_that_is_not_over_columns_of_its_own_added_by_then() {
    for target in targets("registration_refuses_an_index") {
        let v1 = || queue_migrations().remove(0);
        let second =
            |name: &str, columns: &[&str]| BehaviorMigration::new(2, "x").index(name, columns);
        let cases: Vec<(Vec<BehaviorMigration>, &str)> = vec![
            (
                vec![v1(), second("by_size", &["size"])],
                "migration 2 index by_size names size, which no migration up to this one adds; an index lists the behavior's own columns by its own names (rank, holder)",
            ),
            (vec![v1(), second("by_secret", &["bhv_test_other__secret"])], "index by_secret names bhv_test_other__secret, which no migration up to this one adds"),
            (vec![v1(), second("by_data", &["rank", "data"])], "index by_data names data, which no migration up to this one adds"),
            (
                vec![
                    v1().index("by_later", &["later"]),
                    BehaviorMigration::new(2, "later").column("later", ColumnSpec::integer()),
                ],
                "migration 1 index by_later names later, which no migration up to this one adds",
            ),
            (vec![v1(), second("by_none", &[])], "migration 2 index by_none is a non-empty list of the behavior's column names"),
            (vec![v1(), second("ByRank", &["rank"])], "migration 2 index ByRank: an index name matches"),
            (vec![v1(), second("by_rank", &["rank", "rank"])], "index by_rank lists a column twice"),
            (
                vec![v1().index("by_rank", &["rank"]), second("by_rank", &["holder"])],
                "migration 2 adds index by_rank, which an earlier migration added",
            ),
        ];
        for (migrations, problem) in cases {
            let refused = usage(Engine::open(
                target.options(BehaviorSet::new().with(Queue::with(migrations)).with(Other)),
            ));
            assert!(refused.contains(problem), "{problem}: {refused}");
        }
    }
}

#[test]
fn an_index_a_later_migration_lists_is_created_when_the_implementation_registers_on_storage_that_holds_its_own(
) {
    for target in targets("an_index_a_later_migration_lists") {
        let first = target.open(
            BehaviorSet::new()
                .with(Queue::with(vec![queue_migrations().remove(0)]))
                .with(Other),
        );
        publish(&first, &queue_document("Item"), None);
        assert!(queue_indexes(&first).is_empty());
        drop(first);
        let second = open_queue(&target, |_| {});
        assert_eq!(
            queue_indexes(&second),
            [
                "bhv_test_queue___index_by_holder",
                "bhv_test_queue___index_by_rank"
            ]
        );
    }
}
