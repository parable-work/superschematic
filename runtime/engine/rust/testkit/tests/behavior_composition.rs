//! Composition, the cases of `test/behavior-composition.test.ts`: what
//! define and publish check about a type's behaviors, the storage a publish
//! creates, a version whose behavior the engine lacks, and the
//! compatibility rule for behavior configs; on SQLite and, with the
//! variable set, on Postgres. The case that counts the store's reads
//! wraps the TypeScript storage, which the Rust engine does not expose.

mod support;

use std::sync::{Arc, Mutex};

use serde_json::{json, Value};

use superschematic_engine::behaviors::{BehaviorMigration, ColumnSpec};
use superschematic_engine::storage::SqlValue;
use superschematic_engine::{
    Behavior, BehaviorSet, Clock, ConfigTarget, DefineOptions, Engine, EngineErrorCode, Error,
    Hooks, MetaSchema, NamespaceOptions, Result, SchemaTarget, ValidateOptions, WriteOptions,
};
use superschematic_engine_testkit::behaviors::{counter_declaration, Counter, CounterConfig};
use superschematic_engine_testkit::iso_of;
use support::{
    alice, code, fields_of, issue, issues_of, item_document, publish_item, publish_item_with,
    refusal, schema_document, tables_of, targets, texts, usage,
};

// A behavior with a field and an operation named like the counter's.
struct Shadow;

impl Behavior for Shadow {
    type Config = Value;

    fn declaration(&self) -> &str {
        r#"{"name": "test.Shadow", "fields": [{"name": "count"}], "operations": [{"name": "history", "paramsSchema": {"type": "object", "additionalProperties": false}, "resultSchema": true}]}"#
    }

    fn parse_config(&self, raw: &Value, _: &ConfigTarget<'_>) -> Result<Value> {
        Ok(raw.clone())
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            fields: vec![("count", |_, _| Ok(Some(json!(1))))],
            operations: vec![("history", |_, _, _| Ok(json!([])))],
            ..Hooks::default()
        }
    }
}

// A behavior with a field and a column of the names a test gives.
struct Twin {
    declaration: String,
}

impl Twin {
    fn named(name: &str, field: &str) -> Self {
        Twin {
            declaration: json!({"name": name, "fields": [{"name": field}]}).to_string(),
        }
    }
}

impl Behavior for Twin {
    type Config = Value;

    fn declaration(&self) -> &str {
        &self.declaration
    }

    fn parse_config(&self, raw: &Value, _: &ConfigTarget<'_>) -> Result<Value> {
        Ok(raw.clone())
    }

    fn hooks(&self) -> Hooks<Self> {
        let field: &'static str = if self.declaration.contains("\"count\"") {
            "count"
        } else if self.declaration.contains("\"f9\"") {
            "f9"
        } else {
            "f10"
        };
        Hooks {
            fields: vec![(field, |_, _| Ok(Some(json!(1))))],
            migrations: vec![BehaviorMigration::new(1, "c").column("c", ColumnSpec::integer())],
            ..Hooks::default()
        }
    }
}

// test.Broken: a migration that creates a table that is not its own.
struct Broken;

impl Behavior for Broken {
    type Config = Value;

    fn declaration(&self) -> &str {
        r#"{"name": "test.Broken"}"#
    }

    fn parse_config(&self, raw: &Value, _: &ConfigTarget<'_>) -> Result<Value> {
        Ok(raw.clone())
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            migrations: vec![
                BehaviorMigration::new(1, "count")
                    .column("count", ColumnSpec::integer().not_null().default(0)),
                BehaviorMigration::new(2, "escape").up(|sql| {
                    sql.run("CREATE TABLE loose (id TEXT)", &[])?;
                    Ok(())
                }),
            ],
            ..Hooks::default()
        }
    }
}

fn define(engine: &Engine, document: &Value) -> Result<superschematic_engine::SchemaRecord> {
    engine
        .schemas()
        .define(&alice(), document, DefineOptions::default())
}

fn document_issues(result: Result<superschematic_engine::SchemaRecord>) -> Vec<(String, String)> {
    let error = refusal(result);
    assert_eq!(error.name(), "SchemaDocumentError", "{error}");
    issues_of(&error)
}

fn bhv_columns(engine: &Engine) -> Vec<String> {
    support::instance_columns(engine)
        .into_iter()
        .filter(|column| column.starts_with("bhv_"))
        .collect()
}

fn applied(engine: &Engine, owner: &str) -> Vec<(i64, String)> {
    engine
        .db()
        .applied_migrations(owner)
        .unwrap()
        .into_iter()
        .map(|row| (i64::from(row.version), row.name))
        .collect()
}

#[test]
fn define_and_publish_refuse_what_the_compiler_loader_refuses_at_the_behavior() {
    for target in targets("define_and_publish_refuse_what_the_compiler_loader_refuses") {
        let engine = target.open(support::fixtures().with(Shadow));
        let cases = [
            (json!([{"name": "test.Counter"}, {"name": "test.Counter"}]), "/types/Item/behaviors/1", "type Item lists behavior test.Counter twice"),
            (json!([{"name": "test.Missing"}]), "/types/Item/behaviors/0", "behavior test.Missing on type Item: no implementation registered"),
            (json!([{"name": "test.Counter", "config": {"start": -1}}]), "/types/Item/behaviors/0/config", "type Item: behavior test.Counter config: /start must be >= 0"),
            (json!([{"name": "test.Counter", "config": {"start": 5, "limit": 2}}]), "/types/Item/behaviors/0/config", "type Item: behavior test.Counter config: start 5 is past limit 2"),
            (json!([{"name": "test.Flag", "config": {"strict": true}}]), "/types/Item/behaviors/0/config", "type Item: behavior test.Flag takes no config"),
            (json!([{"name": "test.Tally"}]), "/types/Item/behaviors/0", "type Item: behavior test.Tally requires behavior test.Counter, which the type does not list"),
            (json!([{"name": "test.Counter"}, {"name": "test.Tally"}, {"name": "test.Flag"}]), "/types/Item/behaviors/2", "type Item: behavior test.Flag conflicts with behavior test.Tally, which the type also lists"),
            (json!([{"name": "test.Counter"}, {"name": "test.Shadow"}]), "/types/Item/behaviors/1", "type Item: behaviors test.Counter and test.Shadow both add operation history"),
        ];
        for (behaviors, path, message) in cases {
            let found = document_issues(define(&engine, &item_document(behaviors, json!([]))));
            assert_eq!(found[0], issue(path, message), "{message}");
        }
        // The operation is the one issue: two behaviors' fields of one name
        // are none.
        assert_eq!(
            document_issues(define(
                &engine,
                &item_document(
                    json!([{"name": "test.Counter"}, {"name": "test.Shadow"}]),
                    json!([])
                )
            ))
            .into_iter()
            .map(|(_, message)| message)
            .collect::<Vec<_>>(),
            ["type Item: behaviors test.Counter and test.Shadow both add operation history"]
        );
        assert!(engine
            .schemas()
            .list(&alice(), SchemaTarget::default())
            .unwrap()
            .is_empty());
    }
}

#[test]
fn a_types_field_and_two_behaviors_fields_of_one_name_each_sit_apart() {
    for field in [
        json!({"name": "count", "typeRef": {"name": "number"}}),
        json!({"name": "total", "jsonTag": "count", "typeRef": {"name": "number"}}),
    ] {
        for target in targets("a_types_field_and_two_behaviors_fields_of_one_name") {
            let engine = target.open(
                BehaviorSet::new()
                    .with(Counter::default())
                    .with(Twin::named("test.Twin", "count")),
            );
            publish_item_with(
                &engine,
                json!([{"name": "test.Counter", "config": {"start": 3}}, {"name": "test.Twin"}]),
                json!([field]),
            );
            engine
                .instances()
                .create(
                    &alice(),
                    "Item",
                    &json!({"title": "Desk", "count": 7}),
                    superschematic_engine::CreateOptions {
                        id: Some("i1"),
                        ..Default::default()
                    },
                )
                .unwrap();
            assert_eq!(
                fields_of(
                    &engine
                        .instances()
                        .get(&alice(), "Item", "i1", Default::default())
                        .unwrap()
                        .unwrap()
                ),
                json!({"data": {"title": "Desk", "count": 7}, "behaviors": {"test.Counter": {"count": 3}, "test.Twin": {"count": 1}}})
            );
        }
    }
}

#[test]
fn a_behavior_composes_on_the_instance_type_only() {
    for target in targets("a_behavior_composes_on_the_instance_type_only") {
        let engine = target.open_fixtures();
        let document = schema_document(
            "Item",
            json!([{"name": "part", "typeRef": {"name": "Part"}}]),
            json!({"types": {"Part": {"name": "Part", "role": "EmbeddedStruct", "fields": [], "behaviors": [{"name": "test.Counter"}]}}}),
        );
        assert_eq!(
            document_issues(define(&engine, &document)),
            [issue(
                "/types/Part/behaviors/0",
                "type Part: behavior test.Counter composes on the instance type, Item; Part is a nested type, which has no instances"
            )]
        );
    }
}

#[test]
fn publish_checks_the_draft_again_against_the_implementations_registered_then() {
    for target in targets("publish_checks_the_draft_again") {
        let first = target.open(BehaviorSet::new().with(Counter::default()));
        define(
            &first,
            &item_document(json!([{"name": "test.Counter"}]), json!([])),
        )
        .unwrap();
        drop(first);
        let second = target.open(BehaviorSet::new());
        let error = refusal(
            second
                .schemas()
                .publish(&alice(), "Item", SchemaTarget::default()),
        );
        assert_eq!(
            issues_of(&error),
            [issue(
                "/types/Item/behaviors/0",
                "behavior test.Counter on type Item: no implementation registered"
            )]
        );
        assert_eq!(
            second
                .schemas()
                .live(&alice(), "Item", SchemaTarget::default())
                .unwrap(),
            None
        );
    }
}

#[test]
fn storage_is_created_when_a_schema_that_composes_the_behavior_is_published_and_not_before() {
    for target in targets("storage_is_created_when_a_schema_is_published") {
        let engine = target.open_with(support::fixtures(), |options| {
            options.clock = Clock::Manual(7);
        });
        define(
            &engine,
            &item_document(
                json!([{"name": "test.Counter"}, {"name": "test.Flag"}]),
                json!([]),
            ),
        )
        .unwrap();
        assert!(bhv_columns(&engine).is_empty());
        assert!(tables_of(&engine, "bhv_").is_empty());

        engine
            .schemas()
            .publish(&alice(), "Item", SchemaTarget::default())
            .unwrap();
        assert_eq!(
            bhv_columns(&engine),
            [
                "bhv_test_counter__count",
                "bhv_test_flag__flagged",
                "bhv_test_flag__reason"
            ]
        );
        let tables = tables_of(&engine, "bhv_");
        assert!(
            tables.contains(&"bhv_test_counter__history".to_owned()),
            "{tables:?}"
        );
        assert!(
            tables.contains(&"bhv_test_counter__history_by_instance".to_owned()),
            "{tables:?}"
        );
        assert_eq!(
            texts(
                &engine,
                "SELECT name || ' ' || key || ' ' || created_at FROM engine_behaviors ORDER BY name",
                &[]
            ),
            ["test.Counter test_counter 7", "test.Flag test_flag 7"]
        );
        assert_eq!(
            applied(&engine, "test.Counter"),
            [(1, "count".to_owned()), (2, "history".to_owned())]
        );
        // The tally is registered but no published schema composes it.
        assert!(applied(&engine, "test.Tally").is_empty());
    }
}

#[test]
fn schemas_behaviors_lists_what_a_version_composes_with_configs_and_declarations() {
    for target in targets("schemas_behaviors_lists_what_a_version_composes") {
        let engine = target.open_fixtures();
        publish_item(
            &engine,
            json!([{"name": "test.Counter", "config": {"limit": 4}}, {"name": "test.Flag"}]),
        );
        let composed = engine
            .schemas()
            .behaviors(&alice(), "Item", ValidateOptions::default())
            .unwrap();
        assert_eq!(
            serde_json::to_value(&composed).unwrap(),
            json!([
                {"name": "test.Counter", "config": {"limit": 4}, "declaration": serde_json::from_str::<Value>(&counter_declaration()).unwrap()},
                {"name": "test.Flag", "config": {}, "declaration": engine.behaviors().declaration("test.Flag").unwrap()},
            ])
        );
        assert_eq!(
            code(engine.schemas().behaviors(
                &alice(),
                "Item",
                ValidateOptions {
                    version: Some(2),
                    ..Default::default()
                }
            )),
            EngineErrorCode::NotFound
        );
    }
}

#[test]
fn a_failed_publish_leaves_no_storage_behind() {
    for target in targets("a_failed_publish_leaves_no_storage_behind") {
        let engine = target.open(BehaviorSet::new().with(Broken));
        define(
            &engine,
            &item_document(json!([{"name": "test.Broken"}]), json!([])),
        )
        .unwrap();
        let failed = engine
            .schemas()
            .publish(&alice(), "Item", SchemaTarget::default());
        match failed {
            Err(Error::Behavior(error)) => assert!(
                error.to_string().contains(
                    "behavior test.Broken: migration 2 \"escape\" created table loose; a behavior creates only tables and indexes named bhv_test_broken__*"
                ),
                "{error}"
            ),
            other => panic!("expected the migration's defect, got {other:?}"),
        }
        assert!(bhv_columns(&engine).is_empty());
        assert!(tables_of(&engine, "bhv_").is_empty());
        assert!(tables_of(&engine, "loose").is_empty());
        assert_eq!(
            support::count(&engine, "SELECT COUNT(*) AS n FROM engine_behaviors"),
            0
        );
        assert_eq!(
            engine
                .schemas()
                .live(&alice(), "Item", SchemaTarget::default())
                .unwrap(),
            None
        );
        assert_eq!(
            engine
                .schemas()
                .draft(&alice(), "Item", SchemaTarget::default())
                .unwrap()
                .unwrap()
                .name,
            "Item"
        );
    }
}

#[test]
fn names_that_lower_case_alike_get_distinct_keys() {
    for target in targets("names_that_lower_case_alike_get_distinct_keys") {
        let engine = target.open(
            BehaviorSet::new()
                .with(Twin::named("test.Twin", "f9"))
                .with(Twin::named("test_.Twin", "f10")),
        );
        for (schema, behavior) in [("A", "test.Twin"), ("B", "test_.Twin")] {
            let document = schema_document(
                schema,
                json!([]),
                json!({"types": {schema: {"name": schema, "role": "EmbeddedStruct", "fields": [], "behaviors": [{"name": behavior}]}}}),
            );
            support::publish(&engine, &document, None);
        }
        assert_eq!(
            texts(
                &engine,
                "SELECT name || ' ' || key FROM engine_behaviors ORDER BY key",
                &[]
            ),
            ["test.Twin test_twin", "test_.Twin test_twin_2"]
        );
    }
}

#[test]
fn an_engine_without_the_implementation_refuses_a_live_version_that_composes_it_until_one_registers(
) {
    for target in targets("an_engine_without_the_implementation_refuses") {
        let first = target.open(BehaviorSet::new().with(Counter::default()));
        publish_item(
            &first,
            json!([{"name": "test.Counter", "config": {"start": 2}}]),
        );
        first
            .instances()
            .create(
                &alice(),
                "Item",
                &json!({"title": "Desk"}),
                superschematic_engine::CreateOptions {
                    id: Some("i1"),
                    ..Default::default()
                },
            )
            .unwrap();
        drop(first);
        let second = target.open(BehaviorSet::new());
        let expected = "schema Item version 1 in namespace default composes behaviors this engine cannot run: behavior test.Counter on type Item: no implementation registered";
        let outcomes: Vec<Result<Value>> = vec![
            second
                .instances()
                .get(&alice(), "Item", "i1", Default::default())
                .map(|_| Value::Null),
            second
                .instances()
                .list(&alice(), "Item", Default::default())
                .map(|_| Value::Null),
            second
                .instances()
                .create(
                    &alice(),
                    "Item",
                    &json!({"title": "Lamp"}),
                    Default::default(),
                )
                .map(|_| Value::Null),
            second
                .instances()
                .update(
                    &alice(),
                    "Item",
                    "i1",
                    &json!({"title": "Lamp"}),
                    WriteOptions::default(),
                )
                .map(|_| Value::Null),
            second
                .instances()
                .delete(&alice(), "Item", "i1", WriteOptions::default())
                .map(|_| Value::Null),
            second.instances().invoke(
                &alice(),
                "Item",
                "i1",
                "increment",
                &json!({}),
                WriteOptions::default(),
            ),
            second
                .schemas()
                .validate(
                    &alice(),
                    "Item",
                    &json!({"title": "Lamp"}),
                    Default::default(),
                )
                .map(|_| Value::Null),
        ];
        for outcome in outcomes {
            let error = refusal(outcome);
            assert_eq!(error.code(), EngineErrorCode::Unavailable);
            assert_eq!(error.message(), expected);
        }
        second.behaviors().register(Counter::default()).unwrap();
        assert_eq!(
            fields_of(
                &second
                    .instances()
                    .get(&alice(), "Item", "i1", Default::default())
                    .unwrap()
                    .unwrap()
            ),
            json!({"data": {"title": "Desk"}, "behaviors": {"test.Counter": {"count": 2}}})
        );
    }
}

// test.Counter with its first migration alone: the build before history.
struct Early;

impl Behavior for Early {
    type Config = Value;

    fn declaration(&self) -> &str {
        r#"{"name": "test.Counter", "fields": [{"name": "count"}]}"#
    }

    fn parse_config(&self, raw: &Value, _: &ConfigTarget<'_>) -> Result<Value> {
        Ok(raw.clone())
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            migrations: vec![BehaviorMigration::new(1, "count")
                .column("count", ColumnSpec::integer().not_null().default(0))],
            fields: vec![("count", |_, cx| {
                Ok(Some(Value::from(cx.columns().get()?.i64("count")?)))
            })],
            ..Hooks::default()
        }
    }
}

#[test]
fn registering_brings_existing_storage_up_to_the_implementations_last_migration() {
    for target in targets("registering_brings_existing_storage_up") {
        let first = target.open(BehaviorSet::new().with(Early));
        publish_item(&first, json!([{"name": "test.Counter"}]));
        assert_eq!(applied(&first, "test.Counter").len(), 1);
        drop(first);

        let second = target.open(BehaviorSet::new().with(Counter::default()));
        assert_eq!(
            applied(&second, "test.Counter")
                .into_iter()
                .map(|(version, _)| version)
                .collect::<Vec<_>>(),
            [1, 2]
        );
        let tables = tables_of(&second, "bhv_test_counter__history");
        assert!(
            tables.contains(&"bhv_test_counter__history".to_owned()),
            "{tables:?}"
        );
        drop(second);

        let refused = Engine::open(target.options(BehaviorSet::new().with(Early)));
        let message = match refused {
            Err(error) => error.to_string(),
            Ok(_) => panic!("an older build opened"),
        };
        assert!(
            message.contains("test.Counter storage is at migration 2, newer than this build's 1; migrations do not run backwards"),
            "{message}"
        );
    }
}

fn published(target: &support::Target, behaviors: Value, instance: bool) -> Engine {
    let engine = target.open_fixtures();
    publish_item(&engine, behaviors);
    if instance {
        engine
            .instances()
            .create(
                &alice(),
                "Item",
                &json!({"title": "Desk"}),
                superschematic_engine::CreateOptions {
                    id: Some("i1"),
                    ..Default::default()
                },
            )
            .unwrap();
    }
    engine
}

fn changes_of(engine: &Engine, behaviors: Value) -> Vec<String> {
    let error = refusal(define(engine, &item_document(behaviors, json!([]))));
    assert_eq!(error.name(), "IncompatibleChangeError", "{error}");
    error
        .changes()
        .iter()
        .map(|change| change.message.clone())
        .collect()
}

fn publish_version(engine: &Engine) -> i64 {
    engine
        .schemas()
        .publish(&alice(), "Item", SchemaTarget::default())
        .unwrap()
        .version
}

#[test]
fn without_a_config_change_only_an_identical_config_is_a_compatible_version() {
    for target in targets("without_a_config_change_only_an_identical_config") {
        let engine = published(
            &target,
            json!([{"name": "test.Counter"}, {"name": "test.Flag"}]),
            true,
        );
        let mut document = item_document(
            json!([{"name": "test.Counter"}, {"name": "test.Flag"}]),
            json!([]),
        );
        document["description"] = json!("the same behaviors");
        define(&engine, &document).unwrap();
        assert_eq!(publish_version(&engine), 2);
    }
}

#[test]
fn an_implementation_decides_which_config_changes_a_version_may_make() {
    for target in targets("an_implementation_decides_which_config_changes") {
        let engine = published(
            &target,
            json!([{"name": "test.Counter", "config": {"limit": 5}}]),
            true,
        );
        define(
            &engine,
            &item_document(
                json!([{"name": "test.Counter", "config": {"limit": 9}}]),
                json!([]),
            ),
        )
        .unwrap();
        assert_eq!(publish_version(&engine), 2);
        assert_eq!(
            changes_of(&engine, json!([{"name": "test.Counter", "config": {"limit": 3}}])),
            ["behavior test.Counter on type Item cannot change its config from {\"limit\":9} to {\"limit\":3}: a limit can only rise or go"]
        );
        assert_eq!(
            changes_of(&engine, json!([{"name": "test.Counter", "config": {"limit": 9, "start": 1}}])),
            ["behavior test.Counter on type Item cannot change its config from {\"limit\":9} to {\"limit\":9,\"start\":1}: start cannot change"]
        );
        let error = refusal(define(
            &engine,
            &item_document(
                json!([{"name": "test.Counter", "config": {"limit": 3}}]),
                json!([]),
            ),
        ));
        assert_eq!(error.code(), EngineErrorCode::IncompatibleChange);
        assert_eq!(error.changes()[0].path, "Item.behaviors.test.Counter");
    }
}

#[test]
fn a_behavior_is_added_to_or_removed_from_a_schema_with_instances_only_when_it_opts_in() {
    for target in targets("a_behavior_is_added_or_removed_only_when_it_opts_in") {
        let engine = published(&target, json!([{"name": "test.Flag"}]), true);
        assert_eq!(
            changes_of(&engine, json!([{"name": "test.Counter"}])),
            ["behavior test.Flag cannot be removed from type Item, which has instances: it cannot be removed from a schema that has instances"]
        );
        // The counter opts in to being added: the instance that exists
        // counts from 0.
        define(
            &engine,
            &item_document(
                json!([{"name": "test.Flag"}, {"name": "test.Counter"}]),
                json!([]),
            ),
        )
        .unwrap();
        assert_eq!(publish_version(&engine), 2);
        assert_eq!(
            fields_of(
                &engine
                    .instances()
                    .get(&alice(), "Item", "i1", Default::default())
                    .unwrap()
                    .unwrap()
            ),
            json!({"data": {"title": "Desk"}, "behaviors": {"test.Flag": {"flagged": false}, "test.Counter": {"count": 0}}})
        );
        // ... and refuses to be removed.
        assert_eq!(
            changes_of(&engine, json!([{"name": "test.Flag"}])),
            ["behavior test.Counter cannot be removed from type Item, which has instances: removing it loses every count"]
        );
        drop(engine);
        let counted_target = targets("counted")
            .into_iter()
            .find(|candidate| candidate.dialect == target.dialect)
            .unwrap();
        let counted = published(&counted_target, json!([{"name": "test.Counter"}]), true);
        assert_eq!(
            changes_of(&counted, json!([{"name": "test.Counter"}, {"name": "test.Tally"}])),
            ["behavior test.Tally cannot be added to type Item, which has instances: it cannot be added to a schema that has instances"]
        );
    }
}

#[test]
fn with_no_instances_behaviors_come_and_go_freely() {
    for target in targets("with_no_instances_behaviors_come_and_go_freely") {
        let engine = published(&target, json!([{"name": "test.Flag"}]), false);
        define(
            &engine,
            &item_document(
                json!([{"name": "test.Counter"}, {"name": "test.Tally"}]),
                json!([]),
            ),
        )
        .unwrap();
        assert_eq!(publish_version(&engine), 2);
        define(&engine, &item_document(json!([]), json!([]))).unwrap();
        assert_eq!(publish_version(&engine), 3);
    }
}

fn shared_namespaces(options: &mut superschematic_engine::EngineOptions) {
    options.namespaces = NamespaceOptions {
        names: vec!["shared".into(), "east".into()],
        shared: Some("shared".into()),
    };
}

#[test]
fn instances_in_any_namespace_count_for_a_schema_the_shared_namespace_holds() {
    for target in targets("instances_in_any_namespace_count") {
        let engine = target.open_with(support::fixtures(), shared_namespaces);
        let shared = DefineOptions {
            namespace: Some("shared"),
            source: None,
        };
        engine
            .schemas()
            .define(
                &alice(),
                item_document(json!([{"name": "test.Flag"}]), json!([])),
                shared,
            )
            .unwrap();
        engine
            .schemas()
            .publish(
                &alice(),
                "Item",
                SchemaTarget {
                    namespace: Some("shared"),
                },
            )
            .unwrap();
        engine
            .instances()
            .create(
                &alice(),
                "Item",
                &json!({"title": "Desk"}),
                superschematic_engine::CreateOptions {
                    namespace: Some("east"),
                    ..Default::default()
                },
            )
            .unwrap();
        let error = refusal(engine.schemas().define(
            &alice(),
            item_document(json!([]), json!([])),
            shared,
        ));
        assert_eq!(
            error
                .changes()
                .iter()
                .map(|change| change.path.as_str())
                .collect::<Vec<_>>(),
            ["Item.behaviors.test.Flag"]
        );
    }
}

// test.Counter whose configChange reads whether the schema has instances.
struct Counted {
    asked: Arc<Mutex<Vec<bool>>>,
}

impl Behavior for Counted {
    type Config = CounterConfig;

    fn declaration(&self) -> &str {
        r#"{"name": "test.Counter", "configSchema": {"type": "object", "additionalProperties": false, "properties": {"start": {"type": "integer", "minimum": 0}, "limit": {"type": "integer", "minimum": 1}}}}"#
    }

    fn parse_config(&self, raw: &Value, target: &ConfigTarget<'_>) -> Result<CounterConfig> {
        Counter::default().parse_config(raw, target)
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            config_change: Some(|counted, before, after, change| {
                let (Some(before), Some(after)) = (before, after) else {
                    return Ok(None);
                };
                let instances = change.instances()?;
                counted.asked.lock().unwrap().push(instances);
                Ok((before.start != after.start && instances)
                    .then(|| "start cannot change while an instance counts from it".to_owned()))
            }),
            ..Hooks::default()
        }
    }
}

#[test]
fn config_change_sees_whether_the_schema_has_instances_in_any_namespace_and_publish_asks_again() {
    for target in targets("config_change_sees_whether_the_schema_has_instances") {
        let asked = Arc::new(Mutex::new(Vec::new()));
        let engine = target.open_with(
            BehaviorSet::new().with(Counted {
                asked: Arc::clone(&asked),
            }),
            shared_namespaces,
        );
        let shared = DefineOptions {
            namespace: Some("shared"),
            source: None,
        };
        let define_at = |start: i64| {
            engine.schemas().define(
                &alice(),
                item_document(
                    json!([{"name": "test.Counter", "config": {"start": start}}]),
                    json!([]),
                ),
                shared,
            )
        };
        define_at(1).unwrap();
        engine
            .schemas()
            .publish(
                &alice(),
                "Item",
                SchemaTarget {
                    namespace: Some("shared"),
                },
            )
            .unwrap();
        // No namespace holds an instance: start may change.
        define_at(2).unwrap();
        // An instance in a namespace that reads the shared schema: the
        // publish asks again, in its transaction.
        engine
            .instances()
            .create(
                &alice(),
                "Item",
                &json!({"title": "Desk"}),
                superschematic_engine::CreateOptions {
                    namespace: Some("east"),
                    ..Default::default()
                },
            )
            .unwrap();
        let error = refusal(engine.schemas().publish(
            &alice(),
            "Item",
            SchemaTarget {
                namespace: Some("shared"),
            },
        ));
        assert_eq!(
            error
                .changes()
                .iter()
                .map(|change| change.message.as_str())
                .collect::<Vec<_>>(),
            ["behavior test.Counter on type Item cannot change its config from {\"start\":1} to {\"start\":2}: start cannot change while an instance counts from it"]
        );
        assert_eq!(*asked.lock().unwrap(), [false, true]);
    }
}

// acme.Stock and acme.Audited: the Go registry's test extension's
// behaviors, from the declaration files the compiler embeds.
struct Stock {
    declaration: String,
}

impl Behavior for Stock {
    type Config = Value;

    fn declaration(&self) -> &str {
        &self.declaration
    }

    fn parse_config(&self, raw: &Value, _: &ConfigTarget<'_>) -> Result<Value> {
        Ok(raw.clone())
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            migrations: vec![BehaviorMigration::new(1, "on hand")
                .column("on_hand", ColumnSpec::integer().not_null().default(0))],
            operations: vec![
                ("restock", |_, cx, params| {
                    let on_hand = cx.columns().get()?.i64("on_hand")?
                        + params["quantity"].as_i64().unwrap_or(0);
                    cx.columns()
                        .set(&[("on_hand", SqlValue::Integer(on_hand))])?;
                    Ok(json!({ "onHand": on_hand }))
                }),
                ("countStock", |_, cx, _| {
                    Ok(json!({ "onHand": cx.columns().get()?.i64("on_hand")? }))
                }),
            ],
            fields: vec![("onHand", |_, cx| {
                Ok(Some(Value::from(cx.columns().get()?.i64("on_hand")?)))
            })],
            ..Hooks::default()
        }
    }
}

struct Audited {
    declaration: String,
}

impl Behavior for Audited {
    type Config = Value;

    fn declaration(&self) -> &str {
        &self.declaration
    }

    fn parse_config(&self, raw: &Value, _: &ConfigTarget<'_>) -> Result<Value> {
        Ok(raw.clone())
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            migrations: vec![
                BehaviorMigration::new(1, "audited at").column("audited_at", ColumnSpec::text())
            ],
            operations: vec![("audit", |_, cx, _| {
                let audited_at = iso_of(cx.now());
                cx.columns()
                    .set(&[("audited_at", SqlValue::from(audited_at.as_str()))])?;
                Ok(json!({ "auditedAt": audited_at }))
            })],
            fields: vec![("auditedAt", |_, cx| {
                Ok(cx.columns().get()?.opt_text("audited_at")?.map(Value::from))
            })],
            ..Hooks::default()
        }
    }
}

fn extension(target: &support::Target, more: BehaviorSet) -> Engine {
    let read = |path: &str| {
        std::fs::read_to_string(support::repository().join(path)).expect("a repository file")
    };
    let behaviors = BehaviorSet::new()
        .with(Stock {
            declaration: read("internal/registry/registrytest/stock.behavior.json"),
        })
        .with(Audited {
            declaration: read("internal/registry/registrytest/audited.behavior.json"),
        })
        .extend(more);
    let meta_schema = read("runtime/schema/testdata/schema_file_parity.meta-schema.json");
    target.open_with(behaviors, |options| {
        options.meta_schema = Some(MetaSchema::Text(meta_schema));
        options.clock = Clock::Manual(0);
    })
}

#[test]
fn a_binarys_meta_schema_and_its_behaviors_declarations_load_publish_and_run() {
    for target in targets("a_binarys_meta_schema_and_its_behaviors") {
        let engine = extension(&target, BehaviorSet::new());
        let mut document = schema_document(
            "Item",
            json!([{"name": "sku", "typeRef": {"name": "string"}, "required": true}]),
            json!({}),
        );
        document["types"]["Item"]["behaviors"] =
            json!([{"name": "acme.Stock", "config": {"aisles": 2}}, {"name": "acme.Audited"}]);
        support::publish(&engine, &document, None);
        engine
            .instances()
            .create(
                &alice(),
                "Item",
                &json!({"sku": "D-1"}),
                superschematic_engine::CreateOptions {
                    id: Some("i1"),
                    ..Default::default()
                },
            )
            .unwrap();
        let invoke = |operation: &str, params: Value| {
            engine
                .instances()
                .invoke(
                    &alice(),
                    "Item",
                    "i1",
                    operation,
                    &params,
                    WriteOptions::default(),
                )
                .unwrap()
        };
        assert_eq!(
            invoke("restock", json!({"quantity": 4})),
            json!({"onHand": 4})
        );
        assert_eq!(
            invoke("audit", json!({})),
            json!({"auditedAt": "1970-01-01T00:00:00.000Z"})
        );
        assert_eq!(
            fields_of(
                &engine
                    .instances()
                    .get(&alice(), "Item", "i1", Default::default())
                    .unwrap()
                    .unwrap()
            ),
            json!({"data": {"sku": "D-1"}, "behaviors": {"acme.Stock": {"onHand": 4}, "acme.Audited": {"auditedAt": "1970-01-01T00:00:00.000Z"}}})
        );
    }
}

#[test]
fn the_meta_schema_refuses_a_name_the_binary_does_not_declare_before_the_engine_looks() {
    for target in targets("the_meta_schema_refuses_a_name") {
        let engine = extension(&target, BehaviorSet::new().with(Counter::default()));
        let mut document = schema_document("Item", json!([]), json!({}));
        document["types"]["Item"]["behaviors"] = json!([{"name": "test.Counter"}]);
        assert_eq!(
            document_issues(define(&engine, &document))[0].0,
            "/types/Item/behaviors/0/name"
        );
        let _ = usage::<()>(Err(Error::Usage(String::new())));
    }
}
