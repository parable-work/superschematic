//! Registering behavior implementations, the cases of
//! `test/behavior-registration.test.ts`: when the engine opens or later,
//! and what registration refuses, on SQLite and, with the variable set, on
//! Postgres. A hook that is not a function, which TypeScript refuses, is a
//! type Rust's compiler refuses.

mod support;

use serde_json::{json, Value};

use superschematic_engine::behaviors::{
    BehaviorFilter, BehaviorMigration, ColumnSpec, Every, FieldFn, OperationFn, Schedule,
    SchemaOperationFn,
};
use superschematic_engine::{Behavior, BehaviorSet, ConfigTarget, Engine, Hooks, Result};
use superschematic_engine_testkit::behaviors::{counter_declaration, Counter, Flag, Tally};
use support::{targets, usage, Target};

// A behavior shaped as a test needs: test.Counter's declaration, or one a
// test changes, with handlers and readers of the names it lists.
#[derive(Clone)]
struct Shaped {
    declaration: String,
    operations: Vec<&'static str>,
    schema_operations: Vec<&'static str>,
    fields: Vec<&'static str>,
    migrations: Vec<BehaviorMigration>,
    filters: Vec<(&'static str, BehaviorFilter)>,
    create_params_schema: bool,
    schedules: Vec<(&'static str, i64)>,
}

impl Shaped {
    fn counter() -> Self {
        Shaped {
            declaration: counter_declaration(),
            operations: vec!["increment", "history"],
            schema_operations: Vec::new(),
            fields: vec!["count"],
            migrations: vec![BehaviorMigration::new(1, "count")
                .column("count", ColumnSpec::integer().not_null().default(0))],
            filters: Vec::new(),
            create_params_schema: false,
            schedules: Vec::new(),
        }
    }

    fn declared(change: impl FnOnce(&mut Value)) -> Self {
        let mut declaration: Value = serde_json::from_str(&counter_declaration()).unwrap();
        change(&mut declaration);
        Shaped {
            declaration: declaration.to_string(),
            ..Shaped::counter()
        }
    }
}

impl Behavior for Shaped {
    type Config = Value;

    fn declaration(&self) -> &str {
        &self.declaration
    }

    fn parse_config(&self, raw: &Value, _: &ConfigTarget<'_>) -> Result<Value> {
        Ok(raw.clone())
    }

    fn hooks(&self) -> Hooks<Self> {
        let handler: OperationFn<Self> = |_, _, _| Ok(Value::Null);
        let schema_handler: SchemaOperationFn<Self> = |_, _, _| Ok(Value::Null);
        let reader: FieldFn<Self> = |_, _| Ok(None);
        Hooks {
            operations: self
                .operations
                .iter()
                .map(|name| (*name, handler))
                .collect(),
            schema_operations: self
                .schema_operations
                .iter()
                .map(|name| (*name, schema_handler))
                .collect(),
            fields: self.fields.iter().map(|name| (*name, reader)).collect(),
            migrations: self.migrations.clone(),
            filters: self.filters.clone(),
            create_params_schema: self
                .create_params_schema
                .then_some(|_, _, _| json!({"type": "object", "additionalProperties": false})),
            schedules: self
                .schedules
                .iter()
                .map(|(name, every)| {
                    (
                        *name,
                        Schedule {
                            every: Every::Ms(*every),
                            run: |_, _| Ok(()),
                        },
                    )
                })
                .collect(),
            ..Hooks::default()
        }
    }
}

// refusal registers an implementation with a fresh engine and returns the
// message it is refused with; the engine's behaviors stay as they were.
fn refusal(target: &Target, behavior: Shaped) -> String {
    let engine = target.open(BehaviorSet::new());
    let message = usage(engine.behaviors().register(behavior));
    assert!(engine.behaviors().names().is_empty());
    message
}

fn names(engine: &Engine) -> Vec<String> {
    engine.behaviors().names()
}

#[test]
fn behaviors_register_when_the_engine_opens_and_later() {
    for target in targets("behaviors_register_when_the_engine_opens_and_later") {
        let engine = target.open(BehaviorSet::new().with(Counter::default()));
        assert_eq!(names(&engine), ["test.Counter"]);
        engine.behaviors().register(Flag).unwrap();
        assert_eq!(names(&engine), ["test.Counter", "test.Flag"]);
        assert!(engine.behaviors().has("test.Flag"));
        assert_eq!(
            engine.behaviors().declaration("test.Counter").unwrap(),
            serde_json::from_str::<Value>(&counter_declaration()).unwrap()
        );
        assert_eq!(engine.behaviors().declaration("test.Missing"), None);
    }
}

#[test]
fn a_name_registers_once() {
    for target in targets("a_name_registers_once") {
        let engine = target.open_fixtures();
        assert!(usage(engine.behaviors().register(Counter::default()))
            .contains("behavior test.Counter is already registered with this engine"));
        drop(engine);
        let twice = Engine::open(
            target.options(
                BehaviorSet::new()
                    .with(Counter::default())
                    .with(Counter::default()),
            ),
        );
        assert!(usage(twice).contains("already registered"));
    }
}

#[test]
fn registering_stores_nothing_and_storage_comes_with_a_published_schema() {
    for target in targets("registering_stores_nothing") {
        let engine = target.open_fixtures();
        assert_eq!(
            support::count(&engine, "SELECT COUNT(*) AS n FROM engine_behaviors"),
            0
        );
        assert_eq!(
            support::count(
                &engine,
                "SELECT COUNT(*) AS n FROM engine_migrations WHERE owner <> 'engine'"
            ),
            0
        );
    }
}

#[test]
fn registration_refuses_operations_that_are_not_exactly_the_declared_ones() {
    for target in targets("registration_refuses_operations") {
        assert_eq!(
            refusal(
                &target,
                Shaped {
                    operations: vec!["increment"],
                    ..Shaped::counter()
                }
            ),
            "behavior test.Counter cannot register: its declaration names operations it does not implement: history"
        );
        assert!(refusal(
            &target,
            Shaped {
                operations: vec!["increment", "history", "decrement"],
                ..Shaped::counter()
            }
        )
        .contains("it implements operations its declaration does not name: decrement"));
    }
}

#[test]
fn registration_refuses_schema_level_operations_that_are_not_exactly_the_declared_ones() {
    for target in targets("registration_refuses_schema_level_operations") {
        // history declared schema-level: its handler belongs in
        // schema_operations.
        let moved = Shaped::declared(|d| d["operations"][1]["scope"] = json!("schema"));
        let message = refusal(&target, moved.clone());
        assert!(message.contains("it implements operations its declaration does not name: history"));
        assert!(message
            .contains("its declaration names schemaOperations it does not implement: history"));
        let engine = target.open(BehaviorSet::new().with(Shaped {
            operations: vec!["increment"],
            schema_operations: vec!["history"],
            ..moved
        }));
        assert!(engine.behaviors().has("test.Counter"));
        drop(engine);
        assert!(refusal(
            &target,
            Shaped {
                schema_operations: vec!["tally"],
                ..Shaped::counter()
            }
        )
        .contains("it implements schemaOperations its declaration does not name: tally"));
    }
}

#[test]
fn registration_refuses_fields_that_are_not_exactly_the_declared_ones() {
    for target in targets("registration_refuses_fields") {
        assert!(refusal(
            &target,
            Shaped {
                fields: Vec::new(),
                ..Shaped::counter()
            }
        )
        .contains("its declaration names fields it does not implement: count"));
        assert!(refusal(
            &target,
            Shaped {
                fields: vec!["count", "total"],
                ..Shaped::counter()
            }
        )
        .contains("it implements fields its declaration does not name: total"));
    }
}

/// A change a test makes to a declaration.
type Change = Box<dyn Fn(&mut Value)>;

#[test]
fn registration_refuses_a_declaration_the_engine_cannot_take() {
    for target in targets("registration_refuses_a_declaration") {
        let cases: Vec<(Change, &str)> = vec![
            (Box::new(|d| d["name"] = json!("acme.shop.Counter")), "name \"acme.shop.Counter\" must match"),
            (Box::new(|d| d["name"] = json!("test.counter")), "name \"test.counter\" must match"),
            (Box::new(|d| d["colour"] = json!("red")), "the declaration has the unknown key \"colour\""),
            (
                Box::new(|d| d["operations"][0]["name"] = json!("create")),
                "operation create has the name of an operation the engine serves on a schema (create, get, list, update, delete, lookup)",
            ),
            (Box::new(|d| d["operations"][1]["name"] = json!("increment")), "operation increment is declared twice"),
            (Box::new(|d| d["operations"][0]["name"] = json!("Increment")), "\"Increment\" is not camelCase"),
            (
                Box::new(|d| {
                    d["operations"][0]["paramsSchema"]
                        .as_object_mut()
                        .unwrap()
                        .shift_remove("additionalProperties");
                }),
                "operation increment paramsSchema must set \"additionalProperties\": false",
            ),
            (Box::new(|d| d["operations"][0]["paramsSchema"] = json!({"type": "array"})), "paramsSchema must be an object schema"),
            (
                Box::new(|d| {
                    d["operations"][0].as_object_mut().unwrap().shift_remove("resultSchema");
                }),
                "operation increment has no resultSchema",
            ),
            (Box::new(|d| d["operations"][0]["writes"] = json!("yes")), "operation increment writes is a boolean"),
            (Box::new(|d| d["operations"][0]["scope"] = json!("type")), "operation increment scope \"type\" is not \"instance\", \"schema\" or \"namespace\""),
            (Box::new(|d| d["fields"][0]["name"] = json!("the count")), "fields[0].name \"the count\" is not an identifier"),
            (Box::new(|d| d["fields"] = json!([{"name": "count"}, {"name": "count"}])), "field count is declared twice"),
            (Box::new(|d| d["fields"][0]["volatile"] = json!("yes")), "fields[0].volatile is a boolean"),
            (Box::new(|d| d["requires"] = json!(["test.Counter"])), "requires names the behavior itself"),
            (
                Box::new(|d| {
                    d["requires"] = json!(["test.Flag"]);
                    d["conflicts"] = json!(["test.Flag"]);
                }),
                "it both requires and conflicts with test.Flag",
            ),
            (
                Box::new(|d| d["configSchema"] = json!({"type": "object", "properties": {"start": {"type": "nonsense"}}})),
                "configSchema does not compile",
            ),
            (Box::new(|d| d["operations"][0]["invocationPolicy"] = json!("never")), "operation increment invocationPolicy \"never\" is not one of the invocationPolicy values (auto, ask)"),
        ];
        for (change, expected) in cases {
            let message = refusal(&target, Shaped::declared(change));
            assert!(message.contains(expected), "{expected}: {message}");
        }
        let not_an_object = Shaped {
            declaration: "\"nope\"".to_owned(),
            ..Shaped::counter()
        };
        assert!(refusal(&target, not_an_object).contains("a behavior declaration is a JSON object"));
    }
}

#[test]
fn registration_refuses_malformed_migrations_and_columns() {
    for target in targets("registration_refuses_malformed_migrations") {
        let count = || BehaviorMigration::new(1, "count");
        for (migrations, expected) in [
            (
                vec![BehaviorMigration::new(2, "history"), count()],
                "position 1 holds version 2",
            ),
            (vec![BehaviorMigration::new(1, "")], "migration 1 needs a name"),
            (
                vec![count().column("count", ColumnSpec::integer().not_null())],
                "column count is NOT NULL, so it needs a default for the instances that exist",
            ),
            (
                vec![count().column("Count", ColumnSpec::integer())],
                "a column name matches",
            ),
            (
                vec![count().column("count", ColumnSpec::integer().default("zero"))],
                "a default of \"zero\" does not fit type integer",
            ),
            (
                vec![
                    count().column("count", ColumnSpec::integer()),
                    BehaviorMigration::new(2, "again").column("count", ColumnSpec::integer()),
                ],
                "migration 2 adds column count, which an earlier migration added",
            ),
            (
                vec![count()
                    .column("count", ColumnSpec::integer())
                    .index("by_total", &["total"])],
                "migration 1 index by_total names total, which no migration up to this one adds; an index lists the behavior's own columns by its own names (count)",
            ),
            (
                vec![count()
                    .column("count", ColumnSpec::integer())
                    .index("Bad", &["count"])],
                "migration 1 index Bad: an index name matches",
            ),
        ] {
            let message = refusal(
                &target,
                Shaped {
                    migrations,
                    fields: vec!["count"],
                    ..Shaped::counter()
                },
            );
            assert!(message.contains(expected), "{expected}: {message}");
        }
    }
}

#[test]
fn registration_refuses_filters_on_what_it_does_not_declare_or_add() {
    for target in targets("registration_refuses_filters") {
        for (filters, expected) in [
            (
                vec![("total", BehaviorFilter::new("count", "integer"))],
                "filter total names a field its declaration does not (count)",
            ),
            (
                vec![("total.value", BehaviorFilter::new("count", "integer"))],
                "filter total.value names a field its declaration does not",
            ),
            (
                vec![("count.Value", BehaviorFilter::new("count", "integer"))],
                "the member camelCase",
            ),
            (
                vec![("count.a.b", BehaviorFilter::new("count", "integer"))],
                "filter count.a.b names a field",
            ),
            (
                vec![(
                    "count.value",
                    BehaviorFilter::new("count", "integer").described(" "),
                )],
                "filter count.value: description is text when it is given",
            ),
            (
                vec![("count", BehaviorFilter::new("total", "integer"))],
                "filter count: column is one of its migrations' columns (count)",
            ),
            (
                vec![("count", BehaviorFilter::new("count", "date"))],
                "filter count: type is one of string, number, integer, boolean",
            ),
        ] {
            let message = refusal(
                &target,
                Shaped {
                    filters,
                    ..Shaped::counter()
                },
            );
            assert!(message.contains(expected), "{expected}: {message}");
        }
    }
}

#[test]
fn registration_refuses_misnamed_or_too_frequent_schedules_and_unasked_create_parameters() {
    for target in targets("registration_refuses_schedules") {
        let message = refusal(
            &target,
            Shaped {
                schedules: vec![("Sweep", 60_000), ("often", 999)],
                ..Shaped::counter()
            },
        );
        assert!(
            message.contains("schedule Sweep: a schedule name is camelCase (^[a-z][A-Za-z0-9]*$)"),
            "{message}"
        );
        assert!(message.contains("schedule often: everyMs is an integer of at least 1000, or a function of the config that returns one, got 999"), "{message}");
        assert!(refusal(
            &target,
            Shaped {
                create_params_schema: true,
                ..Shaped::counter()
            }
        )
        .contains("createParamsSchema narrows the create parameters its declaration takes, and its declaration takes none (no createParamsSchema)"));
    }
}

#[test]
fn every_problem_is_named_at_once() {
    for target in targets("every_problem_is_named_at_once") {
        let message = refusal(
            &target,
            Shaped {
                fields: Vec::new(),
                operations: Vec::new(),
                ..Shaped::counter()
            },
        );
        assert!(message.contains("fields it does not implement: count"));
        assert!(message.contains("operations it does not implement: increment, history"));
    }
}

#[test]
fn a_set_registers_in_order_and_names_come_sorted() {
    for target in targets("a_set_registers_in_order") {
        struct Nothing;
        impl Behavior for Nothing {
            type Config = Value;
            fn declaration(&self) -> &str {
                r#"{"name": "test.Nothing"}"#
            }
            fn parse_config(&self, raw: &Value, _: &ConfigTarget<'_>) -> Result<Value> {
                Ok(raw.clone())
            }
            fn hooks(&self) -> Hooks<Self> {
                Hooks::default()
            }
        }
        let engine = target.open(
            BehaviorSet::new()
                .with(Counter::default())
                .with(Flag)
                .with(Tally)
                .with(Nothing),
        );
        assert_eq!(
            names(&engine),
            ["test.Counter", "test.Flag", "test.Nothing", "test.Tally"]
        );
    }
}
