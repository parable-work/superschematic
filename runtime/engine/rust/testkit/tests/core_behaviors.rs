//! The core's behaviors with no extension: the cases of
//! `test/core-behaviors.test.ts` that compose only the behaviors ported so
//! far (an engine opened with them registers them and no one else can take
//! their names; the steps document the core binary builds, and the
//! documents one, with Workflow, Comments and Revisions on one type, and
//! the recipes one, whose recipes are version graphs of Branches), on
//! SQLite and, with the variable set, on Postgres. The other documents
//! compose behaviors of later waves.

mod support;

use serde_json::{json, Value};

use superschematic_engine::{
    Behavior, BehaviorSet, ConfigTarget, CreateOptions, DefineOptions, Engine, Error, Hooks,
    Principal, Result, SchemaTarget, WriteOptions,
};
use support::{alice, refusal, repository, targets, usage};

fn writer() -> Principal {
    Principal::new("wes", Vec::<String>::new())
}

// An implementation that takes Workflow's name.
struct Impostor;

impl Behavior for Impostor {
    type Config = Value;

    fn declaration(&self) -> &str {
        r#"{"name": "Workflow"}"#
    }

    fn parse_config(&self, raw: &Value, _target: &ConfigTarget<'_>) -> Result<Value> {
        Ok(raw.clone())
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks::default()
    }
}

#[test]
fn an_engine_opened_with_the_core_registers_it_and_no_one_else_can_take_its_names() {
    for target in targets("core_names") {
        let engine = target.open_core();
        let mut ported: Vec<&str> = superschematic_engine_behaviors::NAMES.to_vec();
        ported.sort_unstable();
        assert_eq!(engine.behaviors().names(), ported);
        let fields: Vec<Value> = engine
            .behaviors()
            .declaration("Workflow")
            .expect("Workflow's declaration")["fields"]
            .as_array()
            .expect("fields")
            .iter()
            .map(|field| field["name"].clone())
            .collect();
        assert_eq!(fields, [json!("status"), json!("enteredAt")]);
        assert_eq!(
            usage(engine.behaviors().register(Impostor)),
            "behavior Workflow is already registered with this engine"
        );
        let opened = Engine::open({
            let mut options = target.core_options();
            options.behaviors =
                std::mem::take(&mut options.behaviors).extend(BehaviorSet::new().with(Impostor));
            options
        });
        assert!(
            usage(opened).contains("already registered"),
            "an open that names a core behavior again"
        );
    }
}

/// The steps document the core binary loads with no extension linked
/// (`fixture-variants-json`).
fn steps() -> Value {
    let path = repository()
        .join("internal/loader/testdata/services/fixture-variants-json/src/step.schema.json");
    serde_json::from_str(&std::fs::read_to_string(&path).expect("the steps document"))
        .expect("JSON")
}

#[test]
fn it_runs_the_steps_document_a_steps_kind_stays_and_its_result_has_the_kinds_shape() {
    for target in targets("core_steps") {
        let engine = target.open_core();
        engine
            .schemas()
            .define(&alice(), steps(), DefineOptions::default())
            .expect("define");
        engine
            .schemas()
            .publish(&alice(), "Step", SchemaTarget::default())
            .expect("publish");
        let check = engine
            .instances()
            .create(
                &writer(),
                "Step",
                &json!({"title": "Lint", "kind": "verify", "result": {"passed": true, "checks": [{"name": "eslint", "ok": true}]}}),
                CreateOptions::default(),
            )
            .expect("create");
        assert_eq!(check.seq, 1);
        let update = |patch: Value| -> Result<Value, Error> {
            engine
                .instances()
                .update(
                    &writer(),
                    "Step",
                    &check.id,
                    &patch,
                    WriteOptions::default(),
                )
                .map(|record| Value::Object(record.data))
        };
        assert_eq!(
            refusal(update(json!({"kind": "note", "result": null}))).message(),
            "Step in namespace default (version 1): kind: kind is a constant of Step: its create sets it and nothing changes it after"
        );
        assert_eq!(
            refusal(update(json!({"result": {"passed": "yes"}})))
                .code()
                .as_str(),
            "invalid_instance"
        );
        assert_eq!(
            refusal(engine.instances().create(
                &writer(),
                "Step",
                &json!({"title": "Idea", "kind": "note", "result": {"passed": true}}),
                CreateOptions::default(),
            ))
            .code()
            .as_str(),
            "invalid_instance"
        );
        assert_eq!(
            update(json!({"result": {"passed": false}})).expect("update"),
            json!({"title": "Lint", "kind": "verify", "result": {"passed": false, "checks": [{"name": "eslint", "ok": true}]}})
        );
    }
}

/// The documents document the core binary loads with no extension linked
/// (`fixture-behaviors-json`): Workflow, Comments and Revisions on one
/// type.
fn documents() -> Value {
    let path = repository()
        .join("internal/loader/testdata/services/fixture-behaviors-json/src/document.schema.json");
    serde_json::from_str(&std::fs::read_to_string(&path).expect("the documents document"))
        .expect("JSON")
}

#[test]
fn it_runs_the_document_the_core_binary_builds_with_all_three_composed_on_one_type() {
    use superschematic_engine::instances::patch::merge_patch;
    use superschematic_engine::{ReadEvents, ValidateOptions};

    let reviewer = Principal::new("rae", ["documents.review"]);
    let publisher = Principal::new("pat", ["documents"]);
    for target in targets("core_documents") {
        let engine = target.open_core_with(|options| {
            options.behaviors = BehaviorSet::new()
                .with(superschematic_engine_behaviors::Workflow)
                .with(superschematic_engine_behaviors::Comments)
                .with(superschematic_engine_behaviors::Revisions);
        });
        engine
            .schemas()
            .define(&alice(), documents(), DefineOptions::default())
            .expect("define");
        let published = engine
            .schemas()
            .publish(&alice(), "documents", SchemaTarget::default())
            .expect("publish");
        assert_eq!((published.version, published.published), (1, true));
        let composed: Vec<String> = engine
            .schemas()
            .behaviors(&alice(), "documents", ValidateOptions::default())
            .expect("behaviors")
            .into_iter()
            .map(|behavior| behavior.name)
            .collect();
        assert_eq!(composed, ["Workflow", "Comments", "Revisions"]);

        let created = engine
            .instances()
            .create(
                &writer(),
                "documents",
                &json!({"title": "Launch plan"}),
                CreateOptions {
                    id: Some("doc-1"),
                    ..Default::default()
                },
            )
            .expect("create");
        assert_eq!(
            Value::Object(created.data.clone()),
            json!({"title": "Launch plan"})
        );
        assert_eq!(
            Value::Object(created.behaviors.clone()),
            json!({
                "Workflow": {"status": "draft", "enteredAt": created.created_at},
                "Comments": {"commentCount": 0},
                "Revisions": {"revision": 1, "pendingProposals": 0}
            })
        );

        let invoke = |principal: &Principal, operation: &str, params: Value| {
            engine.instances().invoke(
                principal,
                "documents",
                "doc-1",
                operation,
                &params,
                WriteOptions::default(),
            )
        };
        invoke(&writer(), "comment", json!({"body": "First pass is up."})).expect("comment");
        invoke(&writer(), "transition", json!({"to": "review"})).expect("transition");
        invoke(
            &writer(),
            "propose",
            json!({"patch": {"body": "The plan, in full."}, "note": "Adds the body."}),
        )
        .expect("propose");
        assert_eq!(
            refusal(invoke(&writer(), "approve", json!({"proposal": 1})))
                .code()
                .as_str(),
            "forbidden"
        );
        invoke(&reviewer, "approve", json!({"proposal": 1})).expect("approve");
        invoke(
            &reviewer,
            "comment",
            json!({"body": "Approved the body.", "replyTo": 1}),
        )
        .expect("reply");
        assert_eq!(
            refusal(invoke(&reviewer, "transition", json!({"to": "published"})))
                .code()
                .as_str(),
            "forbidden"
        );
        invoke(&publisher, "transition", json!({"to": "published"})).expect("publish");

        let read = |id: &str| {
            engine
                .instances()
                .get(&alice(), "documents", id, Default::default())
                .expect("get")
                .expect("the document")
        };
        let events = |id: &str| {
            engine
                .events()
                .read(
                    &alice(),
                    &ReadEvents {
                        schema: Some("documents"),
                        instance_id: Some(id),
                        ..ReadEvents::default()
                    },
                )
                .expect("events")
                .events
        };
        let record = read("doc-1");
        let log = events("doc-1");
        assert_eq!(
            Value::Object(record.data.clone()),
            json!({"title": "Launch plan", "body": "The plan, in full."})
        );
        // The status was last entered by the last transition, the seventh
        // event.
        assert_eq!(
            Value::Object(record.behaviors.clone()),
            json!({
                "Workflow": {"status": "published", "enteredAt": log[6].at},
                "Comments": {"commentCount": 2},
                "Revisions": {"revision": 2, "pendingProposals": 0}
            })
        );
        assert_eq!(record.seq, 7);
        let changes: Vec<Value> = log
            .iter()
            .map(|event| {
                json!([
                    event.kind,
                    event.actor,
                    if event.kind == "operation" {
                        event.change["patch"].clone()
                    } else {
                        event.change.clone()
                    }
                ])
            })
            .collect();
        assert_eq!(
            changes,
            [
                json!(["create", "wes", {
                    "data": {"title": "Launch plan"},
                    "behaviors": {"Workflow": {"status": "draft", "enteredAt": log[0].at}, "Comments": {"commentCount": 0}, "Revisions": {"revision": 1, "pendingProposals": 0}}
                }]),
                json!(["operation", "wes", {"behaviors": {"Comments": {"commentCount": 1}}}]),
                json!(["operation", "wes", {"behaviors": {"Workflow": {"status": "review", "enteredAt": log[2].at}}}]),
                json!(["operation", "wes", {"behaviors": {"Revisions": {"pendingProposals": 1}}}]),
                json!(["operation", "rae", {"data": {"body": "The plan, in full."}, "behaviors": {"Revisions": {"revision": 2, "pendingProposals": 0}}}]),
                json!(["operation", "rae", {"behaviors": {"Comments": {"commentCount": 2}}}]),
                json!(["operation", "pat", {"behaviors": {"Workflow": {"status": "published", "enteredAt": log[6].at}}}]),
            ]
        );
        // The log replays, a merge patch at a time, to the instance a read
        // returns.
        let mut replayed = json!({});
        for event in &log {
            let patch = if event.kind == "create" {
                &event.change
            } else {
                &event.change["patch"]
            };
            replayed = merge_patch(&replayed, patch);
        }
        assert_eq!(
            replayed,
            json!({"data": record.data, "behaviors": record.behaviors})
        );

        let revisions = invoke(&alice(), "listRevisions", json!({})).expect("listRevisions");
        let summary: Vec<Value> = revisions["items"]
            .as_array()
            .expect("items")
            .iter()
            .map(|item| json!([item["revision"], item["data"], item.get("proposal")]))
            .collect();
        assert_eq!(
            summary,
            [
                json!([1, {"title": "Launch plan"}, null]),
                json!([2, {"title": "Launch plan", "body": "The plan, in full."}, 1]),
            ]
        );
        let comments = invoke(&alice(), "listComments", json!({})).expect("listComments");
        let threads: Vec<Value> = comments["items"]
            .as_array()
            .expect("items")
            .iter()
            .map(|item| json!([item.get("replyTo"), item["createdBy"]]))
            .collect();
        assert_eq!(threads, [json!([null, "wes"]), json!([1, "rae"])]);

        // A new version adds an optional field and a state; the instance
        // keeps its history.
        let mut next = documents();
        next["types"]["Document"]["fields"]
            .as_array_mut()
            .expect("fields")
            .push(json!({"name": "summary", "typeRef": {"name": "string"}}));
        let flow = &mut next["types"]["Document"]["behaviors"][0]["config"];
        flow["states"]
            .as_array_mut()
            .expect("states")
            .push(json!("retracted"));
        flow["transitions"]
            .as_array_mut()
            .expect("transitions")
            .push(json!({"from": "published", "to": "retracted"}));
        engine
            .schemas()
            .define(&alice(), &next, DefineOptions::default())
            .expect("define");
        assert_eq!(
            engine
                .schemas()
                .publish(&alice(), "documents", SchemaTarget::default())
                .expect("publish")
                .version,
            2
        );
        invoke(&writer(), "transition", json!({"to": "retracted"})).expect("retract");
        engine
            .instances()
            .update(
                &writer(),
                "documents",
                "doc-1",
                &json!({"summary": "Withdrawn."}),
                WriteOptions::default(),
            )
            .expect("update");
        let retracted = read("doc-1");
        assert_eq!(
            Value::Object(retracted.data.clone()),
            json!({"title": "Launch plan", "body": "The plan, in full.", "summary": "Withdrawn."})
        );
        assert_eq!(
            Value::Object(retracted.behaviors),
            json!({
                "Workflow": {"status": "retracted", "enteredAt": events("doc-1")[7].at},
                "Comments": {"commentCount": 2},
                "Revisions": {"revision": 3, "pendingProposals": 0}
            })
        );
    }
}

/// The recipes document the core binary builds (`fixture-branches-json`):
/// each recipe a version graph's root.
fn recipes() -> Value {
    let path = repository()
        .join("internal/loader/testdata/services/fixture-branches-json/src/recipe.schema.json");
    serde_json::from_str(&std::fs::read_to_string(&path).expect("the recipes document"))
        .expect("JSON")
}

#[test]
fn it_runs_the_recipes_document_whose_drafts_merge_into_the_primary_line_and_a_tagged_commit_is_released(
) {
    use superschematic_engine::RunnerOptions;

    for target in targets("core_recipes") {
        let engine = target.open_core_with(|options| {
            options.behaviors = BehaviorSet::new().with(superschematic_engine_behaviors::Branches);
            options.runner = Some(RunnerOptions::new(Principal::new(
                "runner",
                Vec::<String>::new(),
            )));
        });
        engine
            .schemas()
            .define(&alice(), recipes(), DefineOptions::default())
            .expect("define");
        engine
            .schemas()
            .publish(&alice(), "Recipe", SchemaTarget::default())
            .expect("publish");
        engine
            .instances()
            .create(
                &writer(),
                "Recipe",
                &json!({"title": "Soup"}),
                CreateOptions {
                    id: Some("soup"),
                    ..Default::default()
                },
            )
            .expect("create");
        let invoke = |operation: &str, params: Value| {
            engine
                .instances()
                .invoke(
                    &writer(),
                    "Recipe",
                    "soup",
                    operation,
                    &params,
                    WriteOptions::default(),
                )
                .unwrap_or_else(|error| panic!("{operation}: {error}"))
        };
        let main = invoke("refs", json!({}))["items"][0].clone();
        let draft = invoke("branch", json!({"fromRef": main["id"], "name": "first"}));
        let saved = invoke(
            "save",
            json!({
                "ref": draft["id"],
                "version": draft["version"],
                "edits": {"step": {"upsert": [{"instruction": "Boil", "position": 1}]}, "cover": {"upsert": [{"photoUrl": "soup.jpg"}]}}
            }),
        );
        let version = saved["ref"]["version"].as_i64().expect("a version");
        invoke(
            "save",
            json!({
                "ref": draft["id"],
                "version": version,
                "edits": {"ingredient": {"upsert": [{"stepKey": saved["saved"]["step"][0]["entity_key"], "quantity": "1 l"}]}}
            }),
        );
        let committed = invoke(
            "commit",
            json!({"ref": draft["id"], "version": version + 1}),
        );
        let merged = invoke(
            "merge",
            json!({"source": draft["id"], "target": main["id"], "targetVersion": main["version"], "tag": true}),
        );
        assert_eq!(merged["commit"]["sequence"], 1);
        assert_eq!(
            invoke(
                "releaseCommit",
                json!({"commit": merged["commit"]["id"], "version": 0})
            ),
            json!({"commit": merged["commit"]["id"], "version": 1})
        );
        let released = invoke("released", json!({}));
        let counts: Vec<(String, usize)> = released["tree"]
            .as_object()
            .expect("a tree")
            .iter()
            .map(|(kind, rows)| (kind.clone(), rows.as_array().map_or(0, Vec::len)))
            .collect();
        assert_eq!(
            counts,
            [
                ("cover".to_owned(), 1),
                ("ingredient".to_owned(), 1),
                ("step".to_owned(), 1)
            ]
        );
        assert_eq!(committed["ref"]["parent"], main["id"]);
        // The sweep is on for the schema, at the interval its config gives.
        engine.runner().run_due().expect("run");
        let schedules: Vec<(String, Option<i64>)> = engine
            .runner()
            .status()
            .expect("status")
            .schedules
            .into_iter()
            .filter(|schedule| schedule.behavior == "Branches")
            .map(|schedule| {
                (
                    serde_json::to_value(schedule.state)
                        .expect("a state")
                        .as_str()
                        .unwrap_or_default()
                        .to_owned(),
                    schedule.every_ms,
                )
            })
            .collect();
        assert_eq!(schedules, [("active".to_owned(), Some(3_600_000))]);
    }
}
