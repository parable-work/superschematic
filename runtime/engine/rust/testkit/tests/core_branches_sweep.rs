//! Branches' sweep schedule (D32): the cases of
//! `test/core-branches-sweep.test.ts`, on SQLite and, with the variable
//! set, on Postgres. The sweep is off on a schema whose config gives none;
//! with one, each run discards the drafts idle past `abandonAfter` by
//! invoking discard on each instance, as the runner's principal, so each
//! runs its guards and appends its event, then runs the version graph's
//! sweep with abandoning off, which writes only the behavior's own tables:
//! the rows of refs discarded past the grace, history past each kind's
//! retention, and missing snapshots. test.Keep, the test kit's, vetoes a
//! discard of the ref the test keeps; the recording's scenario reads that
//! ref through a recorder hook and replays only on the TypeScript engine.

mod support;

use serde_json::{json, Value};

use superschematic_engine::storage::{Dialect, SqlValue};
use superschematic_engine::{
    BehaviorSet, Clock, CreateOptions, DefineOptions, Engine, Principal, ReadEvents, RunnerOptions,
    SchemaTarget, WriteOptions,
};
use superschematic_engine_behaviors::{uuid_v5, Branches, ACTOR_NAMESPACE};
use superschematic_engine_testkit::behaviors::Keep;
use support::{alice, schema_document, targets, Target};

const DAY: i64 = 24 * 60 * 60 * 1000;

fn sweeper() -> Principal {
    Principal::new("sweeper", Vec::<String>::new())
}

fn recipe_config() -> Value {
    json!({
        "kinds": {
            "step": {"type": "Step", "order": "position", "units": {"timings": "keyed"}},
            "ingredient": {"type": "Ingredient", "parent": {"key": "stepKey", "of": "step"}},
            "cover": {"type": "Cover", "singleton": true}
        }
    })
}

fn recipes(config: Value, more: Value) -> Value {
    let mut document = schema_document(
        "Recipe",
        json!([{"name": "title", "typeRef": {"name": "string"}, "required": true}]),
        json!({
            "enums": {"Unit": {"name": "Unit", "values": [{"name": "GRAM", "serializedAs": "g"}, {"name": "CUP", "serializedAs": "cup"}]}},
            "types": {
                "Step": {"name": "Step", "role": "EmbeddedStruct", "fields": [
                    {"name": "instruction", "typeRef": {"name": "string"}, "required": true},
                    {"name": "position", "typeRef": {"name": "Int"}, "required": true},
                    {"name": "timings", "typeRef": {"name": "Generic.JSON"}}
                ]},
                "Ingredient": {"name": "Ingredient", "role": "EmbeddedStruct", "fields": [
                    {"name": "stepKey", "typeRef": {"name": "Identity.UUID"}, "required": true},
                    {"name": "quantity", "typeRef": {"name": "string"}, "required": true},
                    {"name": "unit", "typeRef": {"name": "Unit"}}
                ]},
                "Cover": {"name": "Cover", "role": "EmbeddedStruct", "fields": [
                    {"name": "photoUrl", "typeRef": {"name": "string"}, "required": true}
                ]}
            }
        }),
    );
    let mut composed = vec![json!({"name": "Branches", "config": config})];
    composed.extend(more.as_array().cloned().unwrap_or_default());
    document["types"]["Recipe"]["behaviors"] = Value::Array(composed);
    document
}

fn publish(engine: &Engine, document: &Value) {
    engine
        .schemas()
        .define(&alice(), document, DefineOptions::default())
        .expect("define");
    engine
        .schemas()
        .publish(&alice(), "Recipe", SchemaTarget::default())
        .expect("publish");
}

/// An engine on a clock at 1_800_000_000_000 with the runner, Branches and
/// test.Keep, the Recipe schema published under `config` with `more`
/// behaviors beside Branches, and soup created; the runner has found the
/// schedule, which runs an interval later.
fn opened(target: &Target, config: Value, more: Value, keep: &Keep) -> Engine {
    let engine = target.open_with(
        BehaviorSet::new().with(Branches).with(keep.clone()),
        |options| {
            options.clock = Clock::Manual(1_800_000_000_000);
            options.runner = Some(RunnerOptions::new(sweeper()));
        },
    );
    publish(&engine, &recipes(config, more));
    engine
        .instances()
        .create(
            &alice(),
            "Recipe",
            &json!({"title": "Soup"}),
            CreateOptions {
                id: Some("soup"),
                ..Default::default()
            },
        )
        .expect("create");
    engine.runner().run_due().expect("the runner's first pass");
    engine
}

fn invoke(engine: &Engine, operation: &str, params: Value) -> Value {
    engine
        .instances()
        .invoke(
            &alice(),
            "Recipe",
            "soup",
            operation,
            &params,
            WriteOptions::default(),
        )
        .unwrap_or_else(|error| panic!("{operation}: {error}"))
}

fn main_line(engine: &Engine) -> Value {
    invoke(engine, "refs", json!({}))["items"][0].clone()
}

fn branch(engine: &Engine, name: &str) -> Value {
    let main = main_line(engine);
    invoke(
        engine,
        "branch",
        json!({"fromRef": main["id"], "name": name}),
    )
}

fn save(engine: &Engine, r: &Value, edits: Value) -> Value {
    invoke(
        engine,
        "save",
        json!({"ref": r["id"], "version": r["version"], "edits": edits}),
    )
}

fn ref_names(engine: &Engine) -> Vec<String> {
    invoke(engine, "refs", json!({}))["items"]
        .as_array()
        .expect("items")
        .iter()
        .map(|r| r["name"].as_str().expect("a name").to_owned())
        .collect()
}

fn statuses(engine: &Engine) -> Vec<Value> {
    engine
        .runner()
        .status()
        .expect("status")
        .schedules
        .iter()
        .filter(|schedule| schedule.behavior == "Branches")
        .map(|schedule| {
            json!({"schedule": schedule.schedule, "schema": schedule.schema, "state": schedule.state, "everyMs": schedule.every_ms})
        })
        .collect()
}

fn events(engine: &Engine, instance: Option<&str>) -> Vec<superschematic_engine::EngineEvent> {
    engine
        .events()
        .read(
            &alice(),
            &ReadEvents {
                schema: Some("Recipe"),
                instance_id: instance,
                limit: Some(500),
                ..ReadEvents::default()
            },
        )
        .expect("events")
        .events
}

/// Every row a statement returns, run on the engine's storage, as JSON.
fn rows(engine: &Engine, sql: &str, params: &[SqlValue]) -> Vec<Value> {
    engine
        .db()
        .read(|tx| {
            Ok::<_, superschematic_engine::storage::Error>(
                tx.all(sql, params)?
                    .into_iter()
                    .map(|row| {
                        let mut out = serde_json::Map::new();
                        for (column, value) in row.iter() {
                            out.insert(
                                column.to_owned(),
                                match value {
                                    SqlValue::Integer(n) => Value::from(*n),
                                    SqlValue::Text(text) => Value::from(text.as_str()),
                                    _ => Value::Null,
                                },
                            );
                        }
                        Value::Object(out)
                    })
                    .collect(),
            )
        })
        .expect("read the engine's tables")
}

#[test]
fn the_sweep_is_off_on_a_schema_whose_config_gives_none_until_a_publish_gives_it_one() {
    for target in targets("branches_sweep_off") {
        let keep = Keep::default();
        let engine = opened(&target, recipe_config(), json!([]), &keep);
        assert_eq!(
            statuses(&engine),
            [json!({"schedule": "sweep", "schema": "Recipe", "state": "off", "everyMs": null})]
        );
        let mut config = recipe_config();
        config["sweep"] = json!({"intervalMs": 5000});
        publish(&engine, &recipes(config, json!([])));
        engine.runner().run_due().expect("run");
        assert_eq!(
            statuses(&engine),
            [json!({"schedule": "sweep", "schema": "Recipe", "state": "active", "everyMs": 5000})]
        );
    }
}

#[test]
fn a_run_discards_each_idle_draft_through_its_instances_discard_as_the_runner_and_one_a_guard_vetoes_stays(
) {
    for target in targets("branches_sweep_idle") {
        let keep = Keep::default();
        let mut config = recipe_config();
        config["sweep"] = json!({"intervalMs": 1000, "abandonAfter": 60_000});
        let engine = opened(&target, config, json!([{"name": "test.Keep"}]), &keep);
        let draft = branch(&engine, "idle");
        let idle = save(
            &engine,
            &draft,
            json!({"step": {"upsert": [{"instruction": "Boil", "position": 1}]}}),
        )["ref"]
            .clone();
        let kept = branch(&engine, "kept");
        keep.keep(kept["id"].as_str().expect("an id"));
        let busy = branch(&engine, "busy");
        engine.clock().advance(50_000).expect("advance");
        save(
            &engine,
            &busy,
            json!({"step": {"upsert": [{"instruction": "Stir", "position": 1}]}}),
        );
        let seen = events(&engine, Some("soup")).len();
        engine.clock().advance(20_000).expect("advance");
        assert_eq!(engine.runner().run_due().expect("run").scheduled, 1);
        // Refs made in one millisecond list in the order of their ids.
        let mut names = ref_names(&engine);
        names.sort();
        assert_eq!(names, ["busy", "kept", "main"]);
        let written: Vec<Value> = events(&engine, Some("soup"))[seen..]
            .iter()
            .map(|event| {
                json!([
                    event.kind,
                    event.change["operation"],
                    event.actor,
                    serde_json::to_value(&event.cause).expect("a cause")
                ])
            })
            .collect();
        assert_eq!(
            written,
            [
                json!(["operation", "discard", "sweeper", {"behavior": "Branches", "schedule": "sweep", "depth": 1}])
            ]
        );
        assert_eq!(
            events(&engine, Some("soup"))[seen].change["params"],
            json!({"ref": idle["id"], "version": idle["version"]})
        );
        // The draft written since goes once it has been idle as long; the
        // main line never does.
        engine.clock().advance(41_000).expect("advance");
        engine.runner().run_due().expect("run");
        assert_eq!(ref_names(&engine), ["main", "kept"]);
    }
}

#[test]
fn a_run_then_deletes_discarded_rows_past_the_grace_prunes_history_past_retention_and_writes_missing_snapshots(
) {
    for target in targets("branches_sweep_prune") {
        let keep = Keep::default();
        let mut config = recipe_config();
        config["kinds"]["step"]["retentionDays"] = json!(1);
        config["sweep"] = json!({"intervalMs": 1000, "discardGrace": 10_000});
        let engine = opened(&target, config.clone(), json!([]), &keep);
        // A row saved twice on a draft: its first image is pinned by no
        // commit.
        let draft = branch(&engine, "edit");
        let first = save(
            &engine,
            &draft,
            json!({"step": {"upsert": [{"instruction": "Boil", "position": 1}]}}),
        );
        let key = first["saved"]["step"][0]["entity_key"].clone();
        let second = save(
            &engine,
            &first["ref"],
            json!({"step": {"upsert": [{"entity_key": key, "instruction": "Boil hard", "position": 1}]}}),
        );
        let committed = invoke(
            &engine,
            "commit",
            json!({"ref": second["ref"]["id"], "version": second["ref"]["version"]}),
        )["commit"]
            .clone();
        assert_eq!(committed["snapshot"], false);
        // A discarded draft's rows wait out the grace.
        let scrap = branch(&engine, "scrap");
        let scrap_saved = save(
            &engine,
            &scrap,
            json!({"step": {"upsert": [{"instruction": "Burn it", "position": 1}]}}),
        );
        invoke(
            &engine,
            "discard",
            json!({"ref": scrap["id"], "version": scrap_saved["ref"]["version"]}),
        );
        let rows_of = |r: &Value| {
            rows(
                &engine,
                "SELECT COUNT(*) AS n FROM bhv_branches__member WHERE ref_id = ?",
                &[SqlValue::from(r["id"].as_str().expect("an id"))],
            )[0]["n"]
                .clone()
        };
        let member = |path: &str| match target.dialect {
            Dialect::Sqlite => format!("json_extract(data, '$.{path}')"),
            Dialect::Postgres => format!("CAST(data AS JSONB) ->> '{path}'"),
        };
        let images_of = |r: &Value| -> Vec<Value> {
            rows(
                &engine,
                &format!(
                    "SELECT operation, _version, {} AS actor FROM bhv_branches__member_history WHERE {} = ? ORDER BY _version",
                    member("updated_by"),
                    member("ref_id")
                ),
                &[SqlValue::from(r["id"].as_str().expect("an id"))],
            )
            .into_iter()
            .map(|row| json!([row["operation"], row["_version"], row["actor"]]))
            .collect()
        };
        let operations = |images: Vec<Value>| -> Vec<Value> {
            images
                .into_iter()
                .map(|image| json!([image[0], image[1]]))
                .collect()
        };
        assert_eq!(rows_of(&scrap), 1);
        assert_eq!(
            operations(images_of(&draft)),
            [json!(["INSERT", 1]), json!(["UPDATE", 2])]
        );
        // A new version snapshots every commit; the sweep writes those
        // missing.
        let mut every = config.clone();
        every["snapshotEvery"] = json!(1);
        publish(&engine, &recipes(every, json!([])));
        engine.runner().run_due().expect("run");
        let seen = events(&engine, None).len();
        engine.clock().advance(2 * DAY).expect("advance");
        assert_eq!(engine.runner().run_due().expect("run").scheduled, 1);
        assert_eq!(rows_of(&scrap), 0);
        // The delete's image names the runner, and outlives the row's older
        // image.
        assert_eq!(
            images_of(&scrap),
            [json!(["DELETE", 2, uuid_v5(ACTOR_NAMESPACE, "sweeper")])]
        );
        assert_eq!(operations(images_of(&draft)), [json!(["UPDATE", 2])]);
        let history: Vec<Value> = invoke(&engine, "history", json!({"ref": draft["id"]}))
            ["commits"]
            .as_array()
            .expect("commits")
            .iter()
            .map(|commit| json!([commit["id"], commit["snapshot"]]))
            .collect();
        assert_eq!(history, [json!([committed["id"], true])]);
        // The discarded ref and its commits stay, as the audit trail; the
        // sweep appended no event.
        assert_eq!(
            rows(
                &engine,
                "SELECT name, CASE WHEN deleted_at IS NULL THEN 0 ELSE 1 END AS discarded FROM bhv_branches__ref WHERE id = ?",
                &[SqlValue::from(scrap["id"].as_str().expect("an id"))]
            ),
            [json!({"name": "scrap", "discarded": 1})]
        );
        assert_eq!(events(&engine, None).len(), seen);
        assert_eq!(
            rows(
                &engine,
                "SELECT subject FROM bhv_branches__actors WHERE actor = ?",
                &[SqlValue::from(uuid_v5(ACTOR_NAMESPACE, "sweeper"))]
            ),
            [json!({"subject": "sweeper"})]
        );
    }
}
