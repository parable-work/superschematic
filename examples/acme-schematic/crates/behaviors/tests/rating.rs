//! acme.Rating in the Rust engine, the port of
//! `packages/behaviors/test/rating.test.ts` (the smoke's step 20): an
//! engine with the core's behaviors and acme's defines and publishes
//! shop-ratings' Product, creates one, rates it and reads the rating fields
//! and events; its tools carry acme's invocation policy; a new version may
//! raise maxStars and not lower it; an engine without acme's behaviors
//! refuses the schema. Each case runs on SQLite, and on Postgres when
//! `SUPERSCHEMATIC_ENGINE_TEST_DATABASE_URL` names one, each engine in a
//! schema of its own that the case drops.
//!
//! `ACME_META_SCHEMA` names the `acme-schematic json-schema` output, which
//! declares acme.Rating and acme's invocation policy, as a deployment's
//! binary would; `scripts/smoke.sh` and `make acme-engine-rust` set it.

use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicU64, Ordering};
use std::time::{SystemTime, UNIX_EPOCH};

use serde_json::{json, Map, Value};

use superschematic_engine::storage::{Db, Lock, PostgresOptions, SqliteOptions};
use superschematic_engine::{
    CreateOptions, DefineOptions, Engine, EngineError, EngineOptions, Error, MetaSchema, Policy,
    Principal, ReadEvents, SchemaTarget, Storage, ToolTarget, ValidateOptions, WriteOptions,
};

/// Names the Postgres the cases run against, as the engine's tests do.
const DATABASE_VARIABLE: &str = "SUPERSCHEMATIC_ENGINE_TEST_DATABASE_URL";

fn unique_name(prefix: &str) -> String {
    let nanos = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .expect("clock")
        .as_nanos();
    static NEXT: AtomicU64 = AtomicU64::new(0);
    let n = NEXT.fetch_add(1, Ordering::Relaxed);
    format!("{prefix}_{nanos}_{}_{n}", std::process::id())
}

/// A database a case runs on: a SQLite file in a directory of its own, or
/// a Postgres schema of its own, removed when the case is done.
enum Target {
    Sqlite { dir: PathBuf },
    Postgres { url: String, schema: String },
}

impl Target {
    fn storage(&self) -> Storage {
        match self {
            Target::Sqlite { dir } => Storage::Sqlite {
                path: dir.join("shop.db"),
                options: SqliteOptions::default(),
            },
            Target::Postgres { url, schema } => Storage::Postgres {
                url: url.clone(),
                options: postgres_options(schema),
            },
        }
    }

    /// An engine as the TypeScript test's `open` opens one: every write
    /// allowed, acme's meta-schema and tool options, and the core's
    /// behaviors, with acme's when `with_acme`.
    fn open(&self, with_acme: bool) -> Engine {
        let mut options = EngineOptions::new(self.storage());
        options.policy = Some(Policy::allow_all());
        options.meta_schema = Some(MetaSchema::Text(meta_schema()));
        let mut behaviors = superschematic_engine_behaviors::core();
        if with_acme {
            behaviors = behaviors.extend(acme_behaviors::behaviors());
        }
        options.behaviors = behaviors;
        options.tools = acme_behaviors::tools();
        Engine::open(options).expect("open the engine")
    }
}

fn postgres_options(schema: &str) -> PostgresOptions {
    PostgresOptions {
        schema: schema.to_owned(),
        listen: false,
        ..PostgresOptions::default()
    }
}

impl Drop for Target {
    fn drop(&mut self) {
        match self {
            Target::Sqlite { dir } => {
                let _ = std::fs::remove_dir_all(dir);
            }
            Target::Postgres { url, schema } => {
                if let Ok(db) = Db::open_postgres(url, postgres_options(schema)) {
                    let _ = db.write(Lock::Catalog, |tx| {
                        tx.exec(format!("DROP SCHEMA {schema} CASCADE"))
                    });
                }
            }
        }
    }
}

/// SQLite, and Postgres when the variable names one.
fn targets(case: &str) -> Vec<Target> {
    let dir = std::env::temp_dir().join(unique_name("acme_engine"));
    std::fs::create_dir_all(&dir).expect("a directory");
    let mut out = vec![Target::Sqlite { dir }];
    match std::env::var(DATABASE_VARIABLE) {
        Ok(url) if !url.is_empty() => out.push(Target::Postgres {
            url,
            schema: unique_name("acme_engine"),
        }),
        _ => eprintln!("{case}: Postgres pass skipped; set {DATABASE_VARIABLE} to run it"),
    }
    out
}

/// The meta-schema acme's binary writes.
fn meta_schema() -> String {
    let path = std::env::var("ACME_META_SCHEMA").unwrap_or_default();
    if path.is_empty() {
        panic!("ACME_META_SCHEMA must name the acme-schematic json-schema output; scripts/smoke.sh sets it");
    }
    std::fs::read_to_string(&path).unwrap_or_else(|error| panic!("{path}: {error}"))
}

/// shop-ratings' Product, the file ext's tests load.
fn product() -> Value {
    let path = Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../../ext/testdata/services/shop-ratings/src/product.schema.json");
    let text = std::fs::read_to_string(&path).unwrap_or_else(|error| panic!("{path:?}: {error}"));
    serde_json::from_str(&text).expect("the product schema")
}

/// Product as the engine takes a schema: one General document with a name.
fn schema() -> Value {
    let mut out = Map::new();
    out.insert("kind".into(), json!("General"));
    out.insert("name".into(), json!("Product"));
    if let Value::Object(members) = product() {
        out.extend(members);
    }
    Value::Object(out)
}

/// Product with maxStars at `max`.
fn with_max(max: i64) -> Value {
    let mut document = schema();
    document["types"]["Product"]["behaviors"] =
        json!([{"name": "acme.Rating", "config": {"maxStars": max}}]);
    document
}

fn shopper() -> Principal {
    Principal::new("shopper", Vec::<String>::new())
}

fn define(engine: &Engine, document: &Value) -> Result<(), Error> {
    engine
        .schemas()
        .define(&shopper(), document, DefineOptions::default())
        .map(|_| ())
}

fn publish(engine: &Engine) -> superschematic_engine::PublishResult {
    engine
        .schemas()
        .publish(&shopper(), "Product", SchemaTarget::default())
        .expect("publish")
}

fn invoke(engine: &Engine, operation: &str, params: Value) -> Result<Value, Error> {
    engine.instances().invoke(
        &shopper(),
        "Product",
        "p1",
        operation,
        &params,
        WriteOptions::default(),
    )
}

/// p1's behavior fields, as a read returns them.
fn rating_of(engine: &Engine) -> Value {
    Value::Object(
        engine
            .instances()
            .get(&shopper(), "Product", "p1", Default::default())
            .expect("get")
            .expect("p1")
            .behaviors,
    )
}

/// The refusal a call returned.
fn thrown<T: std::fmt::Debug>(result: Result<T, Error>) -> EngineError {
    match result {
        Err(Error::Engine(error)) => error,
        other => panic!("expected an EngineError, got {other:?}"),
    }
}

#[test]
fn an_engine_with_acme_rating_publishes_shop_ratings_and_rates_a_product() {
    for target in targets("publishes_and_rates") {
        let engine = target.open(true);
        assert!(engine.behaviors().has(acme_behaviors::NAME));
        define(&engine, &schema()).expect("define");
        let published = publish(&engine);
        assert_eq!(
            (
                published.namespace.as_str(),
                published.name.as_str(),
                published.version,
                published.published
            ),
            ("default", "Product", 1, true)
        );
        let composed: Vec<Value> = engine
            .schemas()
            .behaviors(&shopper(), "Product", ValidateOptions::default())
            .expect("behaviors")
            .into_iter()
            .map(|behavior| json!({"name": behavior.name, "config": behavior.config}))
            .collect();
        assert_eq!(
            composed,
            [json!({"name": "acme.Rating", "config": {"maxStars": 5}})]
        );

        let created = engine
            .instances()
            .create(
                &shopper(),
                "Product",
                &json!({"sku": "walnut-desk", "name": "Walnut desk"}),
                CreateOptions {
                    id: Some("p1"),
                    ..Default::default()
                },
            )
            .expect("create");
        assert_eq!(
            Value::Object(created.data),
            json!({"sku": "walnut-desk", "name": "Walnut desk"})
        );
        assert_eq!(
            Value::Object(created.behaviors),
            json!({"acme.Rating": {"ratingCount": 0, "ratingAverage": 0}})
        );

        assert_eq!(
            invoke(&engine, "rate", json!({"stars": 4})).expect("rate 4"),
            json!({"ratingCount": 1, "ratingAverage": 4})
        );
        assert_eq!(
            invoke(&engine, "rate", json!({"stars": 5})).expect("rate 5"),
            json!({"ratingCount": 2, "ratingAverage": 4.5})
        );
        assert_eq!(
            rating_of(&engine),
            json!({"acme.Rating": {"ratingCount": 2, "ratingAverage": 4.5}})
        );
        assert_eq!(
            invoke(&engine, "ratingSummary", json!({})).expect("ratingSummary"),
            json!({"ratingCount": 2, "ratingAverage": 4.5})
        );

        // Above the type's maxStars, below the declaration's minimum, and
        // the fields themselves, which an update's own fields cannot name:
        // each is refused and changes nothing.
        let above = thrown(invoke(&engine, "rate", json!({"stars": 6})));
        assert_eq!(above.code().as_str(), "invalid_argument");
        assert_eq!(
            above.message(),
            "a rating of Product is 1 to 5 stars, not 6"
        );
        assert_eq!(
            thrown(invoke(&engine, "rate", json!({"stars": 0}))).name(),
            "OperationParamsError"
        );
        let update = engine.instances().update(
            &shopper(),
            "Product",
            "p1",
            &json!({"ratingCount": 9}),
            WriteOptions::default(),
        );
        assert_eq!(thrown(update).code().as_str(), "invalid_instance");
        assert_eq!(rating_of(&engine)["acme.Rating"]["ratingCount"], json!(2));

        let events: Vec<Value> = engine
            .events()
            .read(
                &shopper(),
                &ReadEvents {
                    schema: Some("Product"),
                    instance_id: Some("p1"),
                    ..Default::default()
                },
            )
            .expect("events")
            .events
            .into_iter()
            .map(|event| json!([event.kind, event.change]))
            .collect();
        assert_eq!(
            events,
            [
                json!(["create", {"data": {"sku": "walnut-desk", "name": "Walnut desk"}, "behaviors": {"acme.Rating": {"ratingCount": 0, "ratingAverage": 0}}}]),
                json!(["operation", {"behavior": "acme.Rating", "operation": "rate", "params": {"stars": 4}, "patch": {"behaviors": {"acme.Rating": {"ratingCount": 1, "ratingAverage": 4}}}}]),
                json!(["operation", {"behavior": "acme.Rating", "operation": "rate", "params": {"stars": 5}, "patch": {"behaviors": {"acme.Rating": {"ratingCount": 2, "ratingAverage": 4.5}}}}]),
            ]
        );
    }
}

#[test]
fn the_engines_tools_carry_the_invocation_policy_acmes_binary_registers() {
    // The meta-schema's mcp record has one key beside its own: the
    // policy's, with its values and default.
    let meta: Value = serde_json::from_str(&meta_schema()).expect("the meta-schema");
    let record = meta["$defs"]["OperationMCP"]["properties"]
        .as_object()
        .expect("the OperationMCP record");
    let own = [
        "_meta",
        "description",
        "handle",
        "hidden",
        "hiddenReason",
        "icon",
        "name",
    ];
    let keys: Vec<&String> = record
        .keys()
        .filter(|name| !own.contains(&name.as_str()))
        .collect();
    assert_eq!(keys.len(), 1, "{keys:?}");
    let key = keys[0];
    let policy = acme_behaviors::tools()
        .invocation_policy
        .expect("acme's policy");
    assert_eq!(
        json!({"key": policy.key, "values": policy.values, "default": policy.default}),
        json!({"key": key, "values": record[key]["enum"], "default": record[key]["default"]})
    );

    for target in targets("tools") {
        let engine = target.open(true);
        define(&engine, &schema()).expect("define");
        publish(&engine);
        let manifest = engine
            .tools()
            .manifest(&shopper(), ToolTarget::default())
            .expect("the manifest");
        let tools = manifest["tools"].as_array().expect("tools");
        let policies: Vec<Value> = tools
            .iter()
            .filter(|tool| {
                tool["name"]
                    .as_str()
                    .is_some_and(|name| name.starts_with("product."))
            })
            .map(|tool| {
                let confirm = if tool["mcp"]["hidden"] == json!(true) {
                    Value::Null
                } else {
                    tool["mcp"]["confirm"].clone()
                };
                json!([tool["name"], confirm])
            })
            .collect();
        assert_eq!(
            policies,
            [
                json!(["product.create", "never"]),
                json!(["product.get", "never"]),
                json!(["product.list", "never"]),
                json!(["product.update", "never"]),
                json!(["product.delete", "never"]),
                json!(["product.rate", "always"]),
                json!(["product.ratingSummary", "never"]),
            ]
        );
        let rate = tools
            .iter()
            .find(|tool| tool["name"] == json!("product.rate"))
            .expect("product.rate");
        assert_eq!(rate["parameters"]["x-acme-arguments"], json!(1));
    }
}

#[test]
fn a_new_version_may_raise_max_stars_and_may_not_lower_it() {
    for target in targets("max_stars") {
        let engine = target.open(true);
        define(&engine, &schema()).expect("define");
        publish(&engine);
        define(&engine, &with_max(10)).expect("define maxStars 10");
        assert_eq!(publish(&engine).version, 2);
        let lowered = thrown(define(&engine, &with_max(3)));
        assert_eq!(lowered.code().as_str(), "incompatible_change");
        assert!(
            lowered
                .message()
                .contains("maxStars cannot fall from 10 to 3"),
            "{}",
            lowered.message()
        );
    }
}

#[test]
fn an_engine_without_acme_rating_refuses_the_schema() {
    for target in targets("without_acme") {
        let engine = target.open(false);
        assert!(!engine.behaviors().has(acme_behaviors::NAME));
        let refused = thrown(define(&engine, &schema()));
        assert_eq!(refused.name(), "SchemaDocumentError");
        let issues: Vec<(String, String)> = refused
            .issues()
            .iter()
            .map(|issue| (issue.path.clone(), issue.message.clone()))
            .collect();
        assert_eq!(
            issues,
            [(
                "/types/Product/behaviors/0".to_owned(),
                "behavior acme.Rating on type Product: no implementation registered".to_owned()
            )]
        );
        assert!(engine
            .schemas()
            .list(&shopper(), SchemaTarget::default())
            .expect("list")
            .is_empty());
    }
}
