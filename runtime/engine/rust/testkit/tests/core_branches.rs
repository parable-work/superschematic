//! Branches, the core's version graph on each instance (D32): the cases of
//! `test/core-branches.test.ts` (each instance a root with a primary line,
//! which drafts merge into; rows of the config's kinds, each held to its
//! type when saved; commits, merges, rebases, reverts and releases, with the
//! engine's refusals as vetoes; actors and roots as version-5 UUIDs, with
//! reads returning subjects; the graph in the behavior's own tables, by
//! namespace and schema; the access policy asked per operation, and an
//! event per write; the rule for a new version; and the delete of an
//! instance's graph with it), on SQLite and, with the variable set, on
//! Postgres, where the version graph's fixed layout runs without a trigger
//! inside the call's transaction (D58). A file the TypeScript engine wrote
//! (`runtime/engine/testdata/sqlite`) keeps working: its recipe reads as
//! recorded and takes new writes. The descriptor and the layout's pinned
//! statements are the behaviors crate's own tests.

mod support;

use std::sync::{Arc, Mutex};

use serde_json::{json, Map, Value};

use superschematic_engine::storage::{Dialect, SqlValue};
use superschematic_engine::{
    AccessRequest, BehaviorSet, Clock, CreateOptions, DefineOptions, Engine, EngineErrorCode,
    EngineErrorKind, EngineOptions, Error, GetOptions, NamespaceOptions, Policy, Principal,
    ReadEvents, RunnerOptions, SchemaTarget, WriteOptions,
};
use superschematic_engine_behaviors::{
    uuid_v5, Branches, Revisions, ACTOR_NAMESPACE, ROOT_NAMESPACE,
};
use support::{alice, refusal, schema_document, targets, veto, Target};

fn bob() -> Principal {
    Principal::new("bob@example.com", Vec::<String>::new())
}

fn boil() -> Value {
    json!({"instruction": "Boil the water", "position": 1})
}

fn stir() -> Value {
    json!({"instruction": "Stir", "position": 2, "timings": {"stir": 30}})
}

/// The Branches config of the Recipe document.
fn recipe_config() -> Value {
    json!({
        "kinds": {
            "step": {"type": "Step", "order": "position", "units": {"timings": "keyed"}},
            "ingredient": {"type": "Ingredient", "parent": {"key": "stepKey", "of": "step"}},
            "cover": {"type": "Cover", "singleton": true}
        }
    })
}

/// A schema, Recipe unless `name` says otherwise, whose instance type
/// composes Branches with `config`, beside the kinds' types; with no config
/// it composes nothing.
fn recipe_document(config: Option<Value>, name: &str) -> Value {
    let mut document = schema_document(
        name,
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
    if let Some(config) = config {
        document["types"][name]["behaviors"] = json!([{"name": "Branches", "config": config}]);
    }
    document
}

fn recipes(config: Value) -> Value {
    recipe_document(Some(config), "Recipe")
}

fn behaviors() -> BehaviorSet {
    BehaviorSet::new().with(Revisions).with(Branches)
}

fn open_with(target: &Target, configure: impl FnOnce(&mut EngineOptions)) -> Engine {
    target.open_core_with(|options| {
        options.behaviors = behaviors();
        configure(options);
    })
}

fn define(engine: &Engine, document: &Value) -> Result<(), Error> {
    engine
        .schemas()
        .define(&alice(), document, DefineOptions::default())
        .map(|_| ())
}

/// Defines and publishes a document as alice.
fn publish(engine: &Engine, document: &Value) {
    define(engine, document).expect("define");
    engine
        .schemas()
        .publish(
            &alice(),
            document["name"].as_str().expect("a name"),
            SchemaTarget::default(),
        )
        .expect("publish");
}

fn create(engine: &Engine, principal: &Principal, schema: &str, title: &str, id: &str) {
    engine
        .instances()
        .create(
            principal,
            schema,
            &json!({ "title": title }),
            CreateOptions {
                id: Some(id),
                ..Default::default()
            },
        )
        .expect("create");
}

/// The calls a test makes on one instance of a schema, as a principal.
struct Calls<'e> {
    engine: &'e Engine,
    id: String,
    principal: Principal,
    schema: String,
}

impl<'e> Calls<'e> {
    fn new(engine: &'e Engine, id: &str) -> Self {
        Calls {
            engine,
            id: id.to_owned(),
            principal: alice(),
            schema: "Recipe".to_owned(),
        }
    }

    fn of(engine: &'e Engine, id: &str, principal: Principal, schema: &str) -> Self {
        Calls {
            engine,
            id: id.to_owned(),
            principal,
            schema: schema.to_owned(),
        }
    }

    fn as_(&self, principal: Principal) -> Calls<'e> {
        Calls::of(self.engine, &self.id, principal, &self.schema)
    }

    fn try_invoke(&self, operation: &str, params: Value) -> Result<Value, Error> {
        self.engine.instances().invoke(
            &self.principal,
            &self.schema,
            &self.id,
            operation,
            &params,
            WriteOptions::default(),
        )
    }

    fn invoke(&self, operation: &str, params: Value) -> Value {
        self.try_invoke(operation, params)
            .unwrap_or_else(|error| panic!("{operation}: {error}"))
    }

    fn refs(&self) -> Vec<Value> {
        self.invoke("refs", json!({}))["items"]
            .as_array()
            .expect("items")
            .clone()
    }

    fn main(&self) -> Value {
        self.refs()
            .into_iter()
            .find(|r| r["parent"].is_null())
            .expect("the primary line")
    }

    fn named(&self, name: &str) -> Value {
        self.refs()
            .into_iter()
            .find(|r| r["name"] == name)
            .expect("the ref")
    }

    fn branch_from(&self, name: &str, from: &Value) -> Value {
        self.invoke("branch", json!({"fromRef": from["id"], "name": name}))
    }

    fn branch(&self, name: &str) -> Value {
        let main = self.main();
        self.branch_from(name, &main)
    }

    fn try_save(&self, r: &Value, edits: Value) -> Result<Value, Error> {
        self.try_invoke(
            "save",
            json!({"ref": r["id"], "version": r["version"], "edits": edits}),
        )
    }

    fn save(&self, r: &Value, edits: Value) -> Value {
        self.try_save(r, edits).expect("save")
    }

    fn try_commit(&self, r: &Value, options: Value) -> Result<Value, Error> {
        let mut params = json!({"ref": r["id"], "version": r["version"]});
        extend(&mut params, options);
        self.try_invoke("commit", params)
    }

    fn commit(&self, r: &Value, options: Value) -> Value {
        self.try_commit(r, options).expect("commit")
    }

    fn try_merge(&self, source: &Value, target: &Value, options: Value) -> Result<Value, Error> {
        let mut params = json!({"source": source["id"], "target": target["id"], "targetVersion": target["version"]});
        extend(&mut params, options);
        self.try_invoke("merge", params)
    }

    fn merge(&self, source: &Value, target: &Value, options: Value) -> Value {
        self.try_merge(source, target, options).expect("merge")
    }

    fn compose(&self, r: &Value) -> Value {
        self.invoke("compose", json!({"ref": r["id"]}))
    }

    /// Writes rows on a new draft of the primary line, commits it and
    /// merges it in, tagged; returns the merge's commit.
    fn change(&self, name: &str, edits: Value) -> Value {
        let draft = self.branch(name);
        let saved = self.save(&draft, edits);
        let committed = self.commit(&saved["ref"], json!({}));
        let main = self.main();
        self.merge(&committed["ref"], &main, json!({"tag": true}))["commit"].clone()
    }
}

fn extend(params: &mut Value, more: Value) {
    if let Value::Object(more) = more {
        for (key, value) in more {
            params[key] = value;
        }
    }
}

/// Each row of a tree's kind with only the columns named.
fn content_of(tree: &Value, kind: &str, columns: &[&str]) -> Value {
    Value::Array(
        tree[kind]
            .as_array()
            .map(|rows| {
                rows.iter()
                    .map(|row| {
                        let mut out = Map::new();
                        for column in columns {
                            out.insert((*column).to_owned(), row[*column].clone());
                        }
                        Value::Object(out)
                    })
                    .collect()
            })
            .unwrap_or_default(),
    )
}

/// The code of the veto a call returned, of Branches.
fn veto_of<T: std::fmt::Debug>(result: Result<T, Error>) -> String {
    let (behavior, _, _, code, _) = veto(result);
    assert_eq!(behavior, "Branches");
    code.expect("a veto code")
}

/// Each issue of the operation's refusal, as (path, message).
fn params_of<T: std::fmt::Debug>(result: Result<T, Error>) -> Vec<(String, String)> {
    let error = refusal(result);
    assert!(
        matches!(error.kind(), EngineErrorKind::OperationParams { .. }),
        "{error}"
    );
    support::issues_of(&error)
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
                        let mut out = Map::new();
                        for (column, value) in row.iter() {
                            out.insert(
                                column.to_owned(),
                                match value {
                                    SqlValue::Null => Value::Null,
                                    SqlValue::Integer(n) => Value::from(*n),
                                    SqlValue::Real(x) => Value::from(*x),
                                    SqlValue::Text(text) => Value::from(text.as_str()),
                                    SqlValue::Blob(bytes) => Value::from(bytes.len()),
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

fn count(engine: &Engine, sql: &str, params: &[SqlValue]) -> i64 {
    rows(engine, sql, params)[0]["n"].as_i64().expect("a count")
}

/// An engine with the Recipe schema published under `config` and soup
/// created by alice.
fn opened(target: &Target, config: Value) -> Engine {
    let engine = open_with(target, |_| {});
    publish(&engine, &recipes(config));
    create(&engine, &alice(), "Recipe", "Soup", "soup");
    engine
}

fn read(engine: &Engine, schema: &str, id: &str) -> Value {
    let record = engine
        .instances()
        .get(&alice(), schema, id, GetOptions::default())
        .expect("get")
        .expect("the instance");
    json!({"data": record.data, "behaviors": record.behaviors})
}

fn release_field(engine: &Engine) -> Value {
    read(engine, "Recipe", "soup")["behaviors"]["Branches"]["release"].clone()
}

fn events_of(engine: &Engine, schema: &str, id: &str) -> Vec<superschematic_engine::EngineEvent> {
    engine
        .events()
        .read(
            &alice(),
            &ReadEvents {
                schema: Some(schema),
                instance_id: Some(id),
                limit: Some(500),
                ..ReadEvents::default()
            },
        )
        .expect("events")
        .events
}

#[test]
fn a_create_makes_the_instance_a_root_with_its_primary_line_and_its_own_fields_stay_outside_the_graph(
) {
    for target in targets("branches_create") {
        let engine = opened(&target, recipe_config());
        let soup = Calls::new(&engine, "soup");
        assert!(engine.behaviors().has("Branches"));
        let refs = soup.refs();
        assert_eq!(refs.len(), 1);
        let main = &refs[0];
        for column in ["id", "createdAt", "updatedAt"] {
            assert!(main[column].is_string(), "{column}");
        }
        let mut shape = main.clone();
        for column in ["id", "createdAt", "updatedAt"] {
            shape[column] = json!("string");
        }
        assert_eq!(
            shape,
            json!({
                "id": "string", "name": "main", "parent": null, "base": null, "head": null,
                "sealed": false, "discarded": false, "version": 1,
                "createdAt": "string", "createdBy": "alice", "updatedAt": "string", "updatedBy": "alice"
            })
        );
        // Branches' entry is empty until a release gives its release field
        // a value.
        assert_eq!(
            read(&engine, "Recipe", "soup"),
            json!({"data": {"title": "Soup"}, "behaviors": {"Branches": {}}})
        );
        // The config names the primary line.
        let mut trunk = recipe_config();
        trunk["primary"] = json!("trunk");
        publish(&engine, &recipe_document(Some(trunk), "Menu"));
        create(&engine, &alice(), "Menu", "Lunch", "lunch");
        let names: Vec<Value> = Calls::of(&engine, "lunch", alice(), "Menu")
            .refs()
            .iter()
            .map(|r| r["name"].clone())
            .collect();
        assert_eq!(names, [json!("trunk")]);
    }
}

#[test]
fn the_graph_is_in_the_behaviors_own_tables_named_by_namespace_and_schema() {
    for target in targets("branches_tables") {
        let engine = opened(&target, recipe_config());
        let soup = Calls::new(&engine, "soup");
        publish(&engine, &recipe_document(Some(recipe_config()), "Menu"));
        create(&engine, &bob(), "Menu", "Lunch", "soup");
        let menu = Calls::of(&engine, "soup", bob(), "Menu");
        let sql = match target.dialect {
            Dialect::Sqlite => "SELECT name FROM sqlite_master WHERE type = 'table' AND substr(name, 1, 14) = 'bhv_branches__' ORDER BY name",
            Dialect::Postgres => "SELECT relname::text AS name FROM pg_catalog.pg_class WHERE relnamespace = current_schema()::regnamespace AND relkind = 'r' AND substr(relname::text, 1, 14) = 'bhv_branches__' ORDER BY relname COLLATE \"C\"",
        };
        let tables: Vec<Value> = rows(&engine, sql, &[])
            .into_iter()
            .map(|row| row["name"].clone())
            .collect();
        assert_eq!(
            tables,
            [
                "bhv_branches__actors",
                "bhv_branches__commit",
                "bhv_branches__member",
                "bhv_branches__member_history",
                "bhv_branches__patch",
                "bhv_branches__ref",
                "bhv_branches__ref_history",
                "bhv_branches__release",
                "bhv_branches__release_history",
                "bhv_branches__roots",
                "bhv_branches__snapshot_entry",
            ]
            .map(Value::from)
        );
        let root = uuid_v5(ROOT_NAMESPACE, "soup");
        assert_eq!(
            rows(
                &engine,
                "SELECT graph, root_id, id FROM bhv_branches__roots ORDER BY graph",
                &[]
            ),
            [
                json!({"graph": "default/Menu", "root_id": root, "id": "soup"}),
                json!({"graph": "default/Recipe", "root_id": root, "id": "soup"}),
            ]
        );
        assert_eq!(
            rows(
                &engine,
                "SELECT graph, root_id, name, created_by FROM bhv_branches__ref ORDER BY graph",
                &[]
            ),
            [
                json!({"graph": "default/Menu", "root_id": root, "name": "main", "created_by": uuid_v5(ACTOR_NAMESPACE, "bob@example.com")}),
                json!({"graph": "default/Recipe", "root_id": root, "name": "main", "created_by": uuid_v5(ACTOR_NAMESPACE, "alice")}),
            ]
        );
        assert_eq!(
            rows(
                &engine,
                "SELECT actor, subject FROM bhv_branches__actors ORDER BY subject",
                &[]
            ),
            [
                json!({"actor": uuid_v5(ACTOR_NAMESPACE, "alice"), "subject": "alice"}),
                json!({"actor": uuid_v5(ACTOR_NAMESPACE, "bob@example.com"), "subject": "bob@example.com"}),
            ]
        );
        // The same instance id in another schema is another graph: its refs
        // are no ref of this one.
        let menu_main = menu.main();
        assert_ne!(menu_main["id"], soup.main()["id"]);
        let id = menu_main["id"].as_str().expect("an id").to_owned();
        assert_eq!(
            params_of(soup.try_invoke("compose", json!({"ref": id}))),
            [("/ref".to_owned(), format!("Recipe soup has no ref {id}"))]
        );
        assert_eq!(
            params_of(soup.try_invoke("compose", json!({"ref": "not-a-uuid"}))),
            [("/ref".to_owned(), "\"not-a-uuid\" is not a UUID".to_owned())]
        );
    }
}

#[test]
fn a_draft_saves_commits_and_merges_into_the_primary_line_and_a_release_writes_the_release_log() {
    for target in targets("branches_release") {
        let engine = opened(&target, recipe_config());
        let soup = Calls::new(&engine, "soup");
        let main = soup.main();
        let draft = soup.branch("edit");
        assert_eq!(
            [
                &draft["parent"],
                &draft["base"],
                &draft["version"],
                &draft["createdBy"]
            ],
            [&main["id"], &Value::Null, &json!(1), &json!("alice")]
        );
        let saved = soup.save(&draft, json!({"step": {"upsert": [boil(), stir()]}}));
        assert_eq!(saved["ref"]["version"], 2);
        let mut boiled = boil();
        extend(
            &mut boiled,
            json!({"timings": null, "created_by": "alice", "updated_by": "alice", "_version": 1}),
        );
        let mut stirred = stir();
        extend(
            &mut stirred,
            json!({"created_by": "alice", "updated_by": "alice", "_version": 1}),
        );
        assert_eq!(
            content_of(
                &saved["saved"],
                "step",
                &[
                    "instruction",
                    "position",
                    "timings",
                    "created_by",
                    "updated_by",
                    "_version"
                ]
            ),
            json!([boiled, stirred])
        );
        assert_eq!(
            veto_of(soup.try_save(&main, json!({"step": {"upsert": [boil()]}}))),
            "primary_merge_only"
        );
        let committed = soup.commit(&saved["ref"], json!({"message": "two steps"}));
        assert_eq!(
            [
                &committed["commit"]["message"],
                &committed["commit"]["sequence"],
                &committed["commit"]["createdBy"]
            ],
            [&json!("two steps"), &Value::Null, &json!("alice")]
        );
        let merged = soup.merge(
            &committed["ref"],
            &soup.main(),
            json!({"tag": true, "message": "first"}),
        );
        assert_eq!(merged["conflicts"], json!([]));
        assert_eq!(merged["commit"]["sequence"], 1);
        assert_eq!(
            content_of(
                &soup.compose(&soup.main())["tree"],
                "step",
                &["instruction", "position"]
            ),
            json!([{"instruction": "Boil the water", "position": 1}, {"instruction": "Stir", "position": 2}])
        );
        // Only a tagged commit is released, at the pointer's version: 0
        // first.
        assert_eq!(
            veto_of(soup.try_invoke(
                "releaseCommit",
                json!({"commit": committed["commit"]["id"], "version": 0})
            )),
            "not_tagged"
        );
        let unreleased = refusal(soup.try_invoke("released", json!({})));
        assert_eq!(
            (unreleased.code(), unreleased.message()),
            (
                EngineErrorCode::NotFound,
                "Recipe soup has not been released"
            )
        );
        let first = merged["commit"].clone();
        // The release field is the release pointer's version, absent before
        // the first release.
        assert_eq!(release_field(&engine), Value::Null);
        assert_eq!(
            soup.invoke(
                "releaseCommit",
                json!({"commit": first["id"], "version": 0})
            ),
            json!({"commit": first["id"], "version": 1})
        );
        assert_eq!(release_field(&engine), 1);
        let last = events_of(&engine, "Recipe", "soup")
            .pop()
            .expect("an event");
        assert_eq!(
            last.change,
            json!({
                "behavior": "Branches",
                "operation": "releaseCommit",
                "params": {"commit": first["id"], "version": 0},
                "patch": {"behaviors": {"Branches": {"release": 1}}}
            })
        );
        assert_eq!(
            veto_of(soup.try_invoke(
                "releaseCommit",
                json!({"commit": first["id"], "version": 0})
            )),
            "version_conflict"
        );
        let second = soup.as_(bob()).change(
            "more",
            json!({"cover": {"upsert": [{"photoUrl": "soup.jpg"}]}}),
        );
        assert_eq!(
            soup.as_(bob()).invoke(
                "releaseCommit",
                json!({"commit": second["id"], "version": 1})
            ),
            json!({"commit": second["id"], "version": 2})
        );
        let released = soup.invoke("released", json!({}));
        let kinds: Vec<&String> = released["tree"]
            .as_object()
            .expect("a tree")
            .keys()
            .collect();
        assert_eq!(kinds, ["cover", "step"]);
        // A rollback is a releaseCommit of the earlier commit.
        soup.invoke(
            "releaseCommit",
            json!({"commit": first["id"], "version": 2}),
        );
        let released = soup.invoke("released", json!({}));
        let kinds: Vec<&String> = released["tree"]
            .as_object()
            .expect("a tree")
            .keys()
            .collect();
        assert_eq!(
            released["release"],
            json!({"commit": first["id"], "version": 3})
        );
        assert_eq!(kinds, ["step"]);
        assert_eq!(release_field(&engine), 3);
        let log = soup.invoke("releases", json!({"limit": 2}));
        let items: Vec<Value> = log["items"]
            .as_array()
            .expect("items")
            .iter()
            .map(|item| json!([item["version"], item["commit"], item["releasedBy"]]))
            .collect();
        assert_eq!(
            items,
            [
                json!([1, first["id"], "alice"]),
                json!([2, second["id"], "bob@example.com"])
            ]
        );
        let rest = soup.invoke("releases", json!({"cursor": log["next"]}));
        let versions: Vec<Value> = rest["items"]
            .as_array()
            .expect("items")
            .iter()
            .map(|item| item["version"].clone())
            .collect();
        assert_eq!((versions, &rest["next"]), (vec![json!(3)], &Value::Null));
        let history: Vec<Value> = soup.invoke("history", json!({"ref": soup.main()["id"]}))
            ["commits"]
            .as_array()
            .expect("commits")
            .iter()
            .map(|commit| json!([commit["sequence"], commit["createdBy"]]))
            .collect();
        assert_eq!(
            history,
            [json!([2, "bob@example.com"]), json!([1, "alice"])]
        );
    }
}

#[test]
fn save_holds_each_rows_content_to_its_kinds_type_before_the_engine_sees_it() {
    for target in targets("branches_save_types") {
        let engine = opened(&target, recipe_config());
        let soup = Calls::new(&engine, "soup");
        let draft = soup.branch("edit");
        let mut mallory = boil();
        mallory["updated_by"] = json!("mallory");
        let issues = params_of(soup.try_save(
            &draft,
            json!({
                "step": {"upsert": [{"instruction": "Boil"}, {"instruction": "Stir", "position": "two", "colour": "red"}, mallory], "delete": ["no pe"]},
                "ingredient": {"upsert": [{"entity_key": "no pe", "stepKey": "also-nope!", "quantity": "1", "unit": "spoon"}]},
                "garnish": {"upsert": [{}]}
            }),
        ));
        let mut paths: Vec<&str> = issues.iter().map(|(path, _)| path.as_str()).collect();
        paths.sort_unstable();
        assert_eq!(
            paths,
            [
                "/edits/garnish",
                "/edits/ingredient/upsert/0/entity_key",
                "/edits/ingredient/upsert/0/stepKey",
                "/edits/ingredient/upsert/0/unit",
                "/edits/step/delete/0",
                "/edits/step/upsert/0/position",
                "/edits/step/upsert/1/colour",
                "/edits/step/upsert/1/position",
                "/edits/step/upsert/2/updated_by",
            ]
        );
        let message = |path: &str| {
            issues
                .iter()
                .find(|(at, _)| at == path)
                .map(|(_, message)| message.as_str())
        };
        assert_eq!(
            message("/edits/garnish"),
            Some("garnish is not a kind of the graph (its kinds: cover, ingredient, step)")
        );
        assert_eq!(
            message("/edits/ingredient/upsert/0/entity_key"),
            Some("\"no pe\" is not a UUID")
        );
        for (path, text) in &issues {
            assert!(!text.is_empty(), "{path}");
        }
        // Nothing was written: the draft is at its version and reads empty.
        assert_eq!(soup.refs()[1]["version"], 1);
        assert_eq!(soup.compose(&draft)["tree"], json!({}));
        // A row is its entity's whole content: a field it leaves out is
        // null.
        let saved = soup.save(&draft, json!({"step": {"upsert": [stir()]}}));
        let key = saved["saved"]["step"][0]["entity_key"].clone();
        let again = soup.save(
            &saved["ref"],
            json!({"step": {"upsert": [{"entity_key": key, "instruction": "Stir well", "position": 2}]}}),
        );
        assert_eq!(
            content_of(
                &again["saved"],
                "step",
                &["entity_key", "instruction", "timings", "_version"]
            ),
            json!([{"entity_key": key, "instruction": "Stir well", "timings": null, "_version": 2}])
        );
    }
}

#[test]
fn a_merges_conflicts_name_each_sides_author_and_resolutions_settle_them_with_rebase_revert_seal_diff_and_materialize(
) {
    for target in targets("branches_merge") {
        let engine = opened(&target, recipe_config());
        let soup = Calls::new(&engine, "soup");
        let base = soup.change("base", json!({"step": {"upsert": [stir()]}}));
        let key = soup.compose(&soup.main())["tree"]["step"][0]["entity_key"].clone();
        let ours = soup.branch("ours");
        let theirs = soup.as_(bob()).branch("theirs");
        let ours_saved = soup.save(
            &ours,
            json!({"step": {"upsert": [{"entity_key": key, "instruction": "Stir slowly", "position": 2, "timings": {"stir": 60}}]}}),
        );
        let ours_committed = soup.commit(&ours_saved["ref"], json!({}));
        soup.merge(&ours_committed["ref"], &soup.main(), json!({}));
        let theirs_saved = soup.as_(bob()).save(
            &theirs,
            json!({"step": {"upsert": [{"entity_key": key, "instruction": "Stir fast", "position": 2, "timings": {"stir": 30, "rest": 5}}]}}),
        );
        // theirs is behind main: rebase meets the conflict on instruction,
        // and the keyed timings merge by key, each side's change kept.
        let conflicted = soup.as_(bob()).invoke(
            "rebase",
            json!({"draft": theirs["id"], "version": theirs_saved["ref"]["version"]}),
        );
        assert_eq!(
            [
                &conflicted["commit"],
                &conflicted["ref"]["version"],
                &conflicted["conflicts"]
            ],
            [
                &Value::Null,
                &theirs_saved["ref"]["version"],
                &json!([{"kind": "step", "entityKey": key, "path": "/instruction", "base": "Stir", "ours": "Stir fast", "theirs": "Stir slowly", "oursAuthor": "bob@example.com", "theirsAuthor": "alice"}])
            ]
        );
        let rebased = soup.as_(bob()).invoke(
            "rebase",
            json!({
                "draft": theirs["id"],
                "version": theirs_saved["ref"]["version"],
                "resolutions": [{"kind": "step", "entityKey": key, "path": "/instruction", "value": "Stir fast, then slowly"}]
            }),
        );
        assert_eq!(
            [&rebased["conflicts"], &rebased["ref"]["base"]],
            [&json!([]), &soup.main()["head"]]
        );
        assert_eq!(
            content_of(
                &soup.compose(&rebased["ref"])["tree"],
                "step",
                &["instruction", "timings"]
            ),
            json!([{"instruction": "Stir fast, then slowly", "timings": {"rest": 5, "stir": 60}}])
        );
        assert_eq!(
            params_of(soup.try_invoke(
                "rebase",
                json!({"draft": theirs["id"], "version": rebased["ref"]["version"], "resolutions": [{"kind": "garnish", "entityKey": key, "path": ""}]})
            )),
            [("/resolutions/0/kind".to_owned(), "garnish is not a kind of the graph".to_owned())]
        );
        // diff and materialize read commits of the instance.
        let changes: Vec<Value> = soup.invoke(
            "diff",
            json!({"from": base["id"], "to": rebased["commit"]["id"]}),
        )["changes"]
            .as_array()
            .expect("changes")
            .iter()
            .map(|change| {
                json!([
                    change["kind"],
                    change["operation"],
                    change["row"]["instruction"],
                    change["row"]["updated_by"]
                ])
            })
            .collect();
        assert_eq!(
            changes,
            [json!([
                "step",
                "UPDATE",
                "Stir fast, then slowly",
                "bob@example.com"
            ])]
        );
        assert_eq!(
            content_of(
                &soup.invoke("materialize", json!({"commit": base["id"]}))["tree"],
                "step",
                &["instruction"]
            ),
            json!([{"instruction": "Stir"}])
        );
        // revert makes the draft compose to an earlier commit's tree, and
        // commits it; seal commits what is left and takes no more writes.
        let reverted = soup.as_(bob()).invoke(
            "revert",
            json!({"ref": theirs["id"], "version": rebased["ref"]["version"], "toCommit": base["id"]}),
        );
        assert_eq!(
            content_of(
                &soup.compose(&reverted["ref"])["tree"],
                "step",
                &["instruction"]
            ),
            json!([{"instruction": "Stir"}])
        );
        let sealed = soup.as_(bob()).invoke(
            "seal",
            json!({"ref": theirs["id"], "version": reverted["ref"]["version"]}),
        );
        assert_eq!(
            [&sealed["ref"]["sealed"], &sealed["commit"]],
            [&json!(true), &Value::Null]
        );
        assert_eq!(
            veto_of(soup.try_save(&sealed["ref"], json!({"step": {"upsert": [boil()]}}))),
            "ref_sealed"
        );
    }
}

#[test]
fn save_holds_each_fields_value_to_its_columns_value_class() {
    for target in targets("branches_value_class") {
        let engine = open_with(&target, |_| {});
        let mut document = recipes(recipe_config());
        document["scalars"] = json!({"Grams": {"name": "Grams", "languagePrimitive": "number", "typeMappings": {"json_schema": "integer"}}});
        document["types"]["Cover"]["fields"]
            .as_array_mut()
            .expect("fields")
            .push(json!({"name": "weight", "typeRef": {"name": "Grams"}}));
        publish(&engine, &document);
        create(&engine, &alice(), "Recipe", "Soup", "soup");
        let soup = Calls::new(&engine, "soup");
        let draft = soup.branch("edit");
        assert_eq!(
            params_of(soup.try_save(
                &draft,
                json!({"cover": {"upsert": [{"photoUrl": "a.jpg", "weight": 1.5}]}})
            )),
            [(
                "/edits/cover/upsert/0/weight".to_owned(),
                "is no integer value: the number 1.5 is not an integer".to_owned()
            )]
        );
        assert_eq!(
            soup.save(
                &draft,
                json!({"cover": {"upsert": [{"photoUrl": "a.jpg", "weight": 2}]}})
            )["saved"]["cover"][0]["weight"],
            2
        );
    }
}

#[test]
fn a_resolution_value_is_held_to_its_fields_class_and_the_row_it_leaves_to_its_kinds_type() {
    for target in targets("branches_resolutions") {
        let engine = opened(&target, recipe_config());
        let soup = Calls::new(&engine, "soup");
        soup.change("base", json!({"step": {"upsert": [boil()]}}));
        let step = soup.compose(&soup.main())["tree"]["step"][0]["entity_key"].clone();
        soup.change(
            "ingredient",
            json!({"ingredient": {"upsert": [{"stepKey": step, "quantity": "1 l", "unit": "g"}]}}),
        );
        let key = soup.compose(&soup.main())["tree"]["ingredient"][0]["entity_key"].clone();
        let ours = soup.branch("ours");
        let theirs = soup.branch("theirs");
        let ours_saved = soup.save(
            &ours,
            json!({
                "step": {"upsert": [{"entity_key": step, "instruction": "Boil it", "position": 1}]},
                "ingredient": {"upsert": [{"entity_key": key, "stepKey": step, "quantity": "1 l", "unit": "cup"}]}
            }),
        );
        let ours_committed = soup.commit(&ours_saved["ref"], json!({}))["ref"].clone();
        let theirs_saved = soup.save(
            &theirs,
            json!({
                "step": {"upsert": [{"entity_key": step, "instruction": "Boil slowly", "position": 1}]},
                "ingredient": {"upsert": [{"entity_key": key, "stepKey": step, "quantity": "1 l"}]}
            }),
        );
        let theirs_committed = soup.commit(&theirs_saved["ref"], json!({}))["ref"].clone();
        soup.merge(&ours_committed, &soup.main(), json!({}));
        let main = soup.main();
        let before = soup.compose(&main)["contentHash"].clone();
        let resolve = |resolutions: Value| {
            soup.try_merge(
                &theirs_committed,
                &soup.main(),
                json!({"resolutions": resolutions}),
            )
        };
        let unit =
            json!({"kind": "ingredient", "entityKey": key, "path": "/unit", "take": "theirs"});
        // A value its field's class refuses, before the engine merges.
        assert_eq!(
            params_of(resolve(
                json!([{"kind": "step", "entityKey": step, "path": "/position", "value": 1.5}, unit])
            )),
            [(
                "/resolutions/0/value".to_owned(),
                "is no integer value: the number 1.5 is not an integer".to_owned()
            )]
        );
        // A value that leaves the row one its type refuses, once the engine
        // has merged.
        let refused = params_of(resolve(
            json!([{"kind": "step", "entityKey": step, "path": "/instruction", "value": null}, unit]),
        ));
        assert_eq!(refused.len(), 1);
        assert_eq!(refused[0].0, "/resolutions/0/value");
        let prefix = format!("step {} instruction: ", step.as_str().expect("a key"));
        assert!(refused[0].1.starts_with(&prefix), "{}", refused[0].1);
        let now = soup.main();
        assert_eq!(
            [
                &now["version"],
                &now["head"],
                &soup.compose(&now)["contentHash"]
            ],
            [&main["version"], &main["head"], &before]
        );
        // The same merge with values its type accepts commits.
        let mut ours_unit = unit.clone();
        ours_unit["take"] = json!("ours");
        let merged = soup.merge(
            &theirs_committed,
            &soup.main(),
            json!({"resolutions": [{"kind": "step", "entityKey": step, "path": "/instruction", "value": "Boil, then simmer"}, ours_unit]}),
        );
        assert_eq!(merged["conflicts"], json!([]));
        assert_eq!(
            content_of(
                &soup.compose(&soup.main())["tree"],
                "step",
                &["instruction"]
            ),
            json!([{"instruction": "Boil, then simmer"}])
        );
        // A rebase is held to the same: an enum member the type lacks is
        // refused.
        let draft = soup.named("theirs");
        let enum_refused = params_of(soup.try_invoke(
            "rebase",
            json!({
                "draft": draft["id"],
                "version": draft["version"],
                "resolutions": [
                    {"kind": "step", "entityKey": step, "path": "/instruction", "take": "ours"},
                    {"kind": "ingredient", "entityKey": key, "path": "/unit", "value": "spoon"}
                ]
            }),
        ));
        assert_eq!(enum_refused.len(), 1);
        assert_eq!(enum_refused[0].0, "/resolutions/1/value");
        let prefix = format!("ingredient {} unit: ", key.as_str().expect("a key"));
        assert!(
            enum_refused[0].1.starts_with(&prefix),
            "{}",
            enum_refused[0].1
        );
        assert_eq!(soup.named("theirs")["version"], draft["version"]);
        let rebased = soup.invoke(
            "rebase",
            json!({
                "draft": draft["id"],
                "version": draft["version"],
                "resolutions": [
                    {"kind": "step", "entityKey": step, "path": "/instruction", "take": "ours"},
                    {"kind": "ingredient", "entityKey": key, "path": "/unit", "value": "g"}
                ]
            }),
        );
        assert_eq!(rebased["conflicts"], json!([]));
    }
}

#[test]
fn refs_pages_the_live_refs_the_primary_line_first_then_the_drafts_as_they_were_created() {
    for target in targets("branches_refs") {
        let engine = open_with(&target, |options| options.clock = Clock::Manual(1_000_000));
        publish(&engine, &recipes(recipe_config()));
        create(&engine, &alice(), "Recipe", "Soup", "soup");
        let soup = Calls::new(&engine, "soup");
        let main = soup.main();
        // Drafts made in one millisecond order by id; later ones after them.
        let mut same = [
            soup.branch("a")["id"].clone(),
            soup.branch("b")["id"].clone(),
        ];
        same.sort_by(|a, b| a.as_str().cmp(&b.as_str()));
        engine.clock().advance(1).expect("advance");
        let later = soup.branch("c");
        let discarded = soup.branch("d");
        soup.invoke(
            "discard",
            json!({"ref": discarded["id"], "version": discarded["version"]}),
        );
        let first = soup.invoke("refs", json!({"limit": 2}));
        let second = soup.invoke("refs", json!({"limit": 2, "cursor": first["next"]}));
        let ids: Vec<Value> = first["items"]
            .as_array()
            .expect("items")
            .iter()
            .chain(second["items"].as_array().expect("items"))
            .map(|r| r["id"].clone())
            .collect();
        assert_eq!(
            ids,
            [
                main["id"].clone(),
                same[0].clone(),
                same[1].clone(),
                later["id"].clone()
            ]
        );
        assert_eq!(second["next"], Value::Null);
        assert_eq!(
            params_of(soup.try_invoke("refs", json!({"cursor": "nope"}))),
            [(
                "/cursor".to_owned(),
                "is not a cursor this operation returned".to_owned()
            )]
        );
    }
}

#[test]
fn the_engines_refusals_are_vetoes_with_its_codes() {
    for target in targets("branches_vetoes") {
        let engine = opened(&target, recipe_config());
        let soup = Calls::new(&engine, "soup");
        let draft = soup.branch("edit");
        assert_eq!(
            veto_of(soup.try_invoke(
                "branch",
                json!({"fromRef": soup.main()["id"], "name": "edit"})
            )),
            "name_taken"
        );
        assert_eq!(
            veto_of(soup.try_commit(&draft, json!({}))),
            "nothing_to_commit"
        );
        let saved = soup.save(
            &draft,
            json!({"cover": {"upsert": [{"photoUrl": "a.jpg"}, {"photoUrl": "b.jpg"}]}}),
        );
        assert_eq!(
            veto_of(soup.try_save(&draft, json!({"step": {"upsert": [boil()]}}))),
            "version_conflict"
        );
        let (_, _, _, code, details) = veto(soup.try_commit(&saved["ref"], json!({})));
        let findings: Vec<Value> = details.expect("details")["findings"]
            .as_array()
            .expect("findings")
            .iter()
            .map(|finding| json!([finding["code"], finding["kind"]]))
            .collect();
        assert_eq!(
            (code.as_deref(), findings),
            (Some("invalid_tree"), vec![json!(["singleton", "cover"])])
        );
        assert_eq!(
            veto_of(soup.try_save(
                &saved["ref"],
                json!({"step": {"unset": [uuid_v5(ROOT_NAMESPACE, "missing")]}})
            )),
            "entity_not_found"
        );
        assert_eq!(
            veto_of(soup.try_merge(&saved["ref"], &saved["ref"], json!({}))),
            "merge_into_itself"
        );
        assert_eq!(
            veto_of(soup.try_invoke("rebase", json!({"draft": soup.main()["id"], "version": 1}))),
            "no_parent"
        );
        // The primary line is not discarded: every draft branches from it.
        let main = soup.main();
        assert_eq!(
            veto_of(soup.try_invoke(
                "discard",
                json!({"ref": main["id"], "version": main["version"]})
            )),
            "primary_line"
        );
        assert_eq!(soup.main(), main);
        // A discarded ref is no ref of the instance any more, and frees its
        // name.
        let discarded = soup.invoke(
            "discard",
            json!({"ref": draft["id"], "version": saved["ref"]["version"]}),
        );
        assert_eq!(discarded["discarded"], true);
        let id = draft["id"].as_str().expect("an id").to_owned();
        assert_eq!(
            params_of(soup.try_invoke("compose", json!({"ref": id}))),
            [("/ref".to_owned(), format!("Recipe soup has no ref {id}"))]
        );
        assert_eq!(soup.branch("edit")["name"], "edit");
    }
}

#[test]
fn every_operation_asks_the_access_policy_write_or_read_with_its_name_and_each_write_appends_its_event(
) {
    for target in targets("branches_policy") {
        let asked: Arc<Mutex<Vec<String>>> = Arc::default();
        let recorded = Arc::clone(&asked);
        let policy = support::RecordingPolicy::new(move |request: &AccessRequest<'_>| {
            if let Some(operation) = request.operation() {
                recorded
                    .lock()
                    .expect("asked")
                    .push(format!("{} {operation}", request.action()));
            }
            request.principal().subject != "reader" || request.action() == "read"
        });
        let engine = open_with(&target, |options| {
            options.policy = Some(Policy::custom(policy))
        });
        publish(&engine, &recipes(recipe_config()));
        create(&engine, &alice(), "Recipe", "Soup", "soup");
        let soup = Calls::new(&engine, "soup");
        let before = events_of(&engine, "Recipe", "soup").len();
        asked.lock().expect("asked").clear();
        let main = soup.main();
        let draft = soup.branch_from("edit", &main);
        let saved = soup.save(&draft, json!({"step": {"upsert": [boil()]}}));
        let committed = soup.commit(&saved["ref"], json!({"tag": true}));
        let commit = committed["commit"].clone();
        soup.compose(&committed["ref"]);
        soup.invoke("materialize", json!({"commit": commit["id"]}));
        soup.invoke("diff", json!({"from": commit["id"], "to": commit["id"]}));
        soup.invoke("history", json!({"ref": draft["id"]}));
        soup.invoke(
            "releaseCommit",
            json!({"commit": commit["id"], "version": 0}),
        );
        soup.invoke("released", json!({}));
        soup.invoke("releases", json!({}));
        let merged = soup.merge(&committed["ref"], &main, json!({}));
        let other = soup.branch_from("other", &merged["ref"]);
        let other_saved = soup.save(&other, json!({"step": {"upsert": [stir()]}}));
        let rebased = soup.invoke(
            "rebase",
            json!({"draft": other["id"], "version": other_saved["ref"]["version"]}),
        );
        let reverted = soup.invoke(
            "revert",
            json!({"ref": other["id"], "version": rebased["ref"]["version"], "toCommit": commit["id"]}),
        );
        let sealed = soup.invoke(
            "seal",
            json!({"ref": other["id"], "version": reverted["ref"]["version"]}),
        );
        soup.invoke(
            "discard",
            json!({"ref": other["id"], "version": sealed["ref"]["version"]}),
        );
        let mut questions: Vec<String> = asked.lock().expect("asked").clone();
        questions.sort();
        questions.dedup();
        let mut expected = vec![
            "read refs",
            "write branch",
            "write save",
            "write commit",
            "read compose",
            "read materialize",
            "read diff",
            "read history",
            "write releaseCommit",
            "read released",
            "read releases",
            "write merge",
            "write rebase",
            "write revert",
            "write seal",
            "write discard",
        ];
        expected.sort_unstable();
        assert_eq!(questions, expected);
        // One operation event for each write.
        let written: Vec<Value> = events_of(&engine, "Recipe", "soup")[before..]
            .iter()
            .map(|event| json!([event.kind, event.change["operation"]]))
            .collect();
        assert_eq!(
            written,
            [
                "branch",
                "save",
                "commit",
                "releaseCommit",
                "merge",
                "branch",
                "save",
                "rebase",
                "revert",
                "seal",
                "discard",
            ]
            .map(|operation| json!(["operation", operation]))
        );
        // A principal the policy lets only read may read the graph and not
        // write it.
        let reader = soup.as_(Principal::new("reader", Vec::<String>::new()));
        assert_eq!(reader.refs().len(), 2);
        assert_eq!(
            refusal(reader.try_invoke("branch", json!({"fromRef": main["id"], "name": "mine"})))
                .code(),
            EngineErrorCode::Forbidden
        );
    }
}

#[test]
fn a_role_named_field_a_kind_whose_type_is_not_one_and_a_parent_order_or_unit_of_the_wrong_class_are_refused_at_define(
) {
    for target in targets("branches_define") {
        let engine = open_with(&target, |_| {});
        let refusal_of = |change: &dyn Fn(&mut Value), config: Value| -> String {
            let mut document = recipes(config);
            change(&mut document);
            let error = refusal(define(&engine, &document));
            assert!(
                matches!(error.kind(), EngineErrorKind::SchemaDocument { .. }),
                "{error}"
            );
            let issues = support::issues_of(&error);
            assert_eq!(
                issues
                    .iter()
                    .map(|(path, _)| path.as_str())
                    .collect::<Vec<_>>(),
                ["/types/Recipe/behaviors/0/config"]
            );
            issues[0]
                .1
                .trim_start_matches("type Recipe: behavior Branches config: ")
                .to_owned()
        };
        let none = |_: &mut Value| {};
        for column in [
            "id",
            "entity_key",
            "ref_id",
            "root_id",
            "deleted_on_ref",
            "_version",
            "created_at",
            "created_by",
            "updated_at",
            "updated_by",
        ] {
            let add = |document: &mut Value| {
                document["types"]["Cover"]["fields"]
                    .as_array_mut()
                    .expect("fields")
                    .push(json!({"name": "x", "jsonTag": column, "typeRef": {"name": "string"}}));
            };
            assert_eq!(
                refusal_of(&add, recipe_config()),
                format!("kind cover: Cover.{column} takes the name of a column every row of a kind has (id, entity_key, ref_id, root_id, deleted_on_ref, _version, created_at, created_by, updated_at, updated_by)")
            );
        }
        assert_eq!(
            refusal_of(&none, json!({"kinds": {"step": {"type": "Recipe"}}})),
            "kind step: Recipe is not a type of Recipe besides Recipe (its types: Cover, Ingredient, Step)"
        );
        assert_eq!(
            refusal_of(&none, json!({"kinds": {"ingredient": {"type": "Ingredient", "parent": {"key": "quantity", "of": "ingredient"}}}})),
            "kind ingredient: its parent key quantity holds a parent row's entity key, a UUID, not a value of class string"
        );
        assert_eq!(
            refusal_of(
                &none,
                json!({"kinds": {"ingredient": {"type": "Ingredient", "parent": {"key": "stepKey", "of": "step"}}}})
            ),
            "kind ingredient: its parent step is not a kind of the config (its kinds: ingredient)"
        );
        assert_eq!(
            refusal_of(
                &none,
                json!({"kinds": {"step": {"type": "Step", "order": "instruction"}}})
            ),
            "kind step: its order instruction holds an integer, not a value of class string"
        );
        assert_eq!(
            refusal_of(&none, json!({"kinds": {"step": {"type": "Step", "units": {"instruction": "keyed"}}}})),
            "kind step: a keyed unit merges a JSON object, and instruction holds a value of class string"
        );
        assert_eq!(
            refusal_of(
                &none,
                json!({"kinds": {"step": {"type": "Step", "units": {"garnish": "atomic"}}}})
            ),
            "kind step: units names garnish, which is not a field of Step"
        );
        // A catalog scalar no value class reads cannot be a kind's field.
        let located = |document: &mut Value| {
            document["types"]["Cover"]["fields"]
                .as_array_mut()
                .expect("fields")
                .push(json!({"name": "where", "typeRef": {"name": "Geo.Location"}}));
        };
        assert_eq!(
            refusal_of(&located, recipe_config()),
            "kind cover: Cover.where is of Geo.Location, which has no value class a graph's row can hold"
        );
    }
}

#[test]
fn an_instance_created_before_its_schema_composed_branches_gets_its_primary_line_at_its_first_write(
) {
    for target in targets("branches_older") {
        let engine = open_with(&target, |_| {});
        publish(&engine, &recipe_document(None, "Recipe"));
        create(&engine, &alice(), "Recipe", "Old soup", "old");
        // Branches can be added to a schema that has instances.
        publish(&engine, &recipes(recipe_config()));
        let old = Calls::of(&engine, "old", bob(), "Recipe");
        assert_eq!(old.refs(), Vec::<Value>::new());
        // A write that fails leaves no primary line behind.
        let issues = params_of(old.try_invoke(
            "branch",
            json!({"fromRef": uuid_v5(ROOT_NAMESPACE, "x"), "name": "edit"}),
        ));
        assert_eq!(
            issues
                .iter()
                .map(|(path, _)| path.as_str())
                .collect::<Vec<_>>(),
            ["/fromRef"]
        );
        assert_eq!(old.refs(), Vec::<Value>::new());
        engine
            .instances()
            .update(
                &bob(),
                "Recipe",
                "old",
                &json!({"title": "Old soup, again"}),
                WriteOptions::default(),
            )
            .expect("update");
        let refs: Vec<Value> = old
            .refs()
            .iter()
            .map(|r| json!([r["name"], r["createdBy"]]))
            .collect();
        assert_eq!(refs, [json!(["main", "bob@example.com"])]);
        // A new instance has its line from its create.
        create(&engine, &alice(), "Recipe", "New soup", "new");
        let created: Vec<Value> = Calls::new(&engine, "new")
            .refs()
            .iter()
            .map(|r| r["createdBy"].clone())
            .collect();
        assert_eq!(created, [json!("alice")]);
    }
}

#[test]
fn an_older_instances_first_branch_without_from_ref_makes_its_primary_line_and_a_draft_and_a_refused_one_neither(
) {
    for target in targets("branches_implicit") {
        let engine = open_with(&target, |options| {
            let policy = support::RecordingPolicy::new(|request: &AccessRequest<'_>| {
                request.principal().subject != "reader" || request.action() == "read"
            });
            options.policy = Some(Policy::custom(policy));
        });
        publish(&engine, &recipe_document(None, "Recipe"));
        create(&engine, &alice(), "Recipe", "Old soup", "old");
        publish(&engine, &recipes(recipe_config()));
        let old = Calls::of(&engine, "old", bob(), "Recipe");
        // A refused branch rolls the primary line it made back with it: a
        // draft named like the primary line, and a caller the policy
        // refuses.
        assert_eq!(
            veto_of(old.try_invoke("branch", json!({"name": "main"}))),
            "name_taken"
        );
        assert_eq!(old.refs(), Vec::<Value>::new());
        assert_eq!(
            refusal(
                old.as_(Principal::new("reader", Vec::<String>::new()))
                    .try_invoke("branch", json!({"name": "edit"}))
            )
            .code(),
            EngineErrorCode::Forbidden
        );
        assert_eq!(old.refs(), Vec::<Value>::new());
        let draft = old.invoke("branch", json!({"name": "edit"}));
        let refs = old.refs();
        let shown: Vec<Value> = refs
            .iter()
            .map(|r| {
                json!([
                    r["name"],
                    if r["parent"].is_null() {
                        Value::Null
                    } else {
                        json!("main")
                    },
                    r["createdBy"]
                ])
            })
            .collect();
        assert_eq!(
            shown,
            [
                json!(["main", null, "bob@example.com"]),
                json!(["edit", "main", "bob@example.com"])
            ]
        );
        assert_eq!(
            [&draft["id"], &draft["parent"], &draft["base"]],
            [&refs[1]["id"], &refs[0]["id"], &Value::Null]
        );
        // On an instance with its line, branch without fromRef is branch
        // from it.
        create(&engine, &alice(), "Recipe", "New soup", "new");
        let soup = Calls::new(&engine, "new");
        let main = soup.main();
        let implicit = soup.invoke("branch", json!({"name": "implicit"}));
        let explicit = soup.invoke("branch", json!({"fromRef": main["id"], "name": "explicit"}));
        assert_eq!(
            [&implicit["parent"], &implicit["base"]],
            [&explicit["parent"], &explicit["base"]]
        );
        assert_eq!(implicit["parent"], main["id"]);
        // With fromRef it branches from that ref, a draft included.
        assert_eq!(
            soup.invoke(
                "branch",
                json!({"fromRef": implicit["id"], "name": "nested"})
            )["parent"],
            implicit["id"]
        );
    }
}

#[test]
fn another_behaviors_writing_operation_is_a_first_write_too() {
    for target in targets("branches_other_write") {
        let engine = open_with(&target, |_| {});
        let mut before = recipe_document(None, "Recipe");
        let revisions =
            json!({"name": "Revisions", "config": {"review": {"permission": "recipes.review"}}});
        before["types"]["Recipe"]["behaviors"] = json!([revisions]);
        publish(&engine, &before);
        create(&engine, &alice(), "Recipe", "Old soup", "old");
        let mut after = recipes(recipe_config());
        after["types"]["Recipe"]["behaviors"] =
            json!([revisions, {"name": "Branches", "config": recipe_config()}]);
        publish(&engine, &after);
        let old = Calls::of(&engine, "old", bob(), "Recipe");
        assert_eq!(old.refs(), Vec::<Value>::new());
        old.invoke("propose", json!({"patch": {"title": "Salted soup"}}));
        let refs: Vec<Value> = old
            .refs()
            .iter()
            .map(|r| json!([r["name"], r["createdBy"]]))
            .collect();
        assert_eq!(refs, [json!(["main", "bob@example.com"])]);
    }
}

#[test]
fn two_namespaces_that_share_a_schema_keep_a_graph_each_and_the_sweep_in_each_discards_only_its_own(
) {
    for target in targets("branches_namespaces") {
        let engine = open_with(&target, |options| {
            options.clock = Clock::Manual(1_800_000_000_000);
            options.runner = Some(RunnerOptions::new(Principal::new(
                "sweeper",
                Vec::<String>::new(),
            )));
            options.namespaces = NamespaceOptions {
                names: vec!["east".into(), "west".into(), "lib".into()],
                shared: Some("lib".into()),
            };
        });
        let mut config = recipe_config();
        config["sweep"] = json!({"intervalMs": 1000, "abandonAfter": 60_000});
        engine
            .schemas()
            .define(
                &alice(),
                recipes(config),
                DefineOptions {
                    namespace: Some("lib"),
                    source: None,
                },
            )
            .expect("define");
        engine
            .schemas()
            .publish(
                &alice(),
                "Recipe",
                SchemaTarget {
                    namespace: Some("lib"),
                },
            )
            .expect("publish");
        let invoke = |namespace: &str, operation: &str, params: Value| {
            engine.instances().invoke(
                &alice(),
                "Recipe",
                "soup",
                operation,
                &params,
                WriteOptions {
                    namespace: Some(namespace),
                    ..Default::default()
                },
            )
        };
        let mut drafts = Map::new();
        for namespace in ["east", "west"] {
            engine
                .instances()
                .create(
                    &alice(),
                    "Recipe",
                    &json!({"title": namespace}),
                    CreateOptions {
                        namespace: Some(namespace),
                        id: Some("soup"),
                        ..Default::default()
                    },
                )
                .expect("create");
            let main = invoke(namespace, "refs", json!({})).expect("refs")["items"][0].clone();
            let draft = invoke(
                namespace,
                "branch",
                json!({"fromRef": main["id"], "name": "idle"}),
            )
            .expect("branch");
            drafts.insert(namespace.to_owned(), draft["id"].clone());
        }
        let root = uuid_v5(ROOT_NAMESPACE, "soup");
        assert_eq!(
            rows(
                &engine,
                "SELECT graph, root_id FROM bhv_branches__roots ORDER BY graph",
                &[]
            ),
            [
                json!({"graph": "east/Recipe", "root_id": root}),
                json!({"graph": "west/Recipe", "root_id": root}),
            ]
        );
        // One namespace's ref is no ref of the other's instance.
        let issues = params_of(invoke("west", "compose", json!({"ref": drafts["east"]})));
        assert_eq!(issues[0].0, "/ref");
        engine.runner().run_due().expect("run");
        // A write keeps west's draft from going idle; east's goes.
        engine.clock().advance(50_000).expect("advance");
        let west = invoke("west", "refs", json!({})).expect("refs")["items"][1].clone();
        invoke(
            "west",
            "save",
            json!({"ref": west["id"], "version": west["version"], "edits": {"step": {"upsert": [boil()]}}}),
        )
        .expect("save");
        engine.clock().advance(20_000).expect("advance");
        engine.runner().run_due().expect("run");
        let names = |namespace: &str| -> Vec<Value> {
            invoke(namespace, "refs", json!({})).expect("refs")["items"]
                .as_array()
                .expect("items")
                .iter()
                .map(|r| r["name"].clone())
                .collect()
        };
        assert_eq!(names("east"), [json!("main")]);
        assert_eq!(names("west"), [json!("main"), json!("idle")]);
        // The schedule runs in each namespace the shared schema serves, each
        // run on that namespace's graph.
        let mut schedules: Vec<Value> = engine
            .runner()
            .status()
            .expect("status")
            .schedules
            .iter()
            .filter(|schedule| schedule.behavior == "Branches")
            .map(|schedule| json!([schedule.namespace, schedule.state]))
            .collect();
        schedules.sort_by_key(|schedule| schedule.to_string());
        assert_eq!(
            schedules,
            [
                json!(["default", "active"]),
                json!(["east", "active"]),
                json!(["lib", "active"]),
                json!(["west", "active"]),
            ]
        );
    }
}

#[test]
fn a_delete_image_of_a_row_names_its_actor_and_history_images_exclude_nothing() {
    for target in targets("branches_images") {
        let engine = opened(&target, recipe_config());
        let soup = Calls::new(&engine, "soup");
        let draft = soup.branch("edit");
        let saved = soup.save(&draft, json!({"step": {"upsert": [stir()]}}));
        let key = saved["saved"]["step"][0]["entity_key"].clone();
        soup.as_(bob())
            .save(&saved["ref"], json!({"step": {"unset": [key]}}));
        let images: Vec<Value> = rows(
            &engine,
            "SELECT operation, _version, data FROM bhv_branches__member_history ORDER BY _version",
            &[],
        )
        .into_iter()
        .map(|row| {
            let image: Value =
                serde_json::from_str(row["data"].as_str().expect("data")).expect("JSON");
            let mut keys: Vec<&String> = image.as_object().expect("an image").keys().collect();
            keys.sort();
            json!([row["operation"], row["_version"], image["updated_by"], keys])
        })
        .collect();
        let columns = json!([
            "_version",
            "created_at",
            "created_by",
            "deleted_on_ref",
            "entity_key",
            "id",
            "instruction",
            "position",
            "ref_id",
            "root_id",
            "timings",
            "updated_at",
            "updated_by"
        ]);
        assert_eq!(
            images,
            [
                json!(["INSERT", 1, uuid_v5(ACTOR_NAMESPACE, "alice"), columns]),
                json!([
                    "DELETE",
                    2,
                    uuid_v5(ACTOR_NAMESPACE, "bob@example.com"),
                    columns
                ]),
            ]
        );
        // Every commit records schema epoch 0.
        soup.change("more", json!({"step": {"upsert": [boil()]}}));
        assert_eq!(
            rows(
                &engine,
                "SELECT DISTINCT schema_epoch FROM bhv_branches__commit",
                &[]
            ),
            [json!({"schema_epoch": 0})]
        );
    }
}

#[test]
fn deleting_an_instance_deletes_its_graph_and_only_its_own() {
    for target in targets("branches_delete") {
        let engine = opened(&target, recipe_config());
        create(&engine, &alice(), "Recipe", "Stew", "stew");
        let soup = Calls::new(&engine, "soup");
        let stew = Calls::new(&engine, "stew");
        for calls in [&soup, &stew] {
            let first = calls.change("first", json!({"step": {"upsert": [boil()]}}));
            calls.invoke(
                "releaseCommit",
                json!({"commit": first["id"], "version": 0}),
            );
            let draft = calls.branch("draft");
            calls.save(&draft, json!({"step": {"upsert": [stir()]}}));
        }
        let tables = [
            "ref",
            "ref_history",
            "commit",
            "patch",
            "snapshot_entry",
            "release",
            "release_history",
            "member",
            "member_history",
            "roots",
        ];
        let counts = || -> Vec<i64> {
            tables
                .iter()
                .map(|table| {
                    count(
                        &engine,
                        &format!("SELECT COUNT(*) AS n FROM bhv_branches__{table}"),
                        &[],
                    )
                })
                .collect()
        };
        let both = counts();
        engine
            .instances()
            .delete(&alice(), "Recipe", "soup", WriteOptions::default())
            .expect("delete");
        let left = counts();
        for (index, table) in tables.iter().enumerate() {
            assert!(both[index] > 0, "{table}");
            assert_eq!(left[index] * 2, both[index], "{table}");
        }
        let root = uuid_v5(ROOT_NAMESPACE, "soup");
        for table in ["ref", "commit", "release", "member", "roots"] {
            assert_eq!(
                count(
                    &engine,
                    &format!("SELECT COUNT(*) AS n FROM bhv_branches__{table} WHERE root_id = ?"),
                    &[SqlValue::from(root.as_str())]
                ),
                0,
                "{table}"
            );
        }
        let mut names: Vec<String> = stew
            .refs()
            .iter()
            .map(|r| r["name"].as_str().expect("a name").to_owned())
            .collect();
        names.sort();
        assert_eq!(names, ["draft", "first", "main"]);
    }
}

#[test]
fn a_new_version_may_add_a_kind_and_change_fields_and_a_retention_but_not_a_kinds_shape_or_drop_branches(
) {
    for target in targets("branches_versions") {
        let engine = opened(&target, recipe_config());
        let soup = Calls::new(&engine, "soup");
        soup.change("first", json!({"step": {"upsert": [boil()]}}));
        let next = |change: &dyn Fn(&mut Value, &mut Value)| -> Result<(), Error> {
            let mut config = recipe_config();
            let mut document = recipes(config.clone());
            change(&mut config, &mut document);
            document["types"]["Recipe"]["behaviors"] =
                json!([{"name": "Branches", "config": config}]);
            define(&engine, &document)
        };
        let refused = |result: Result<(), Error>| -> String {
            let error = refusal(result);
            error
                .changes()
                .iter()
                .map(|change| change.message.clone())
                .collect::<Vec<_>>()
                .join("; ")
        };
        // Allowed: a kind, an optional field, a retention, the sweep and the
        // primary line's name.
        next(&|config, document| {
            config["kinds"]["note"] = json!({"type": "Note"});
            document["types"]["Note"] = json!({"name": "Note", "role": "EmbeddedStruct", "fields": [{"name": "body", "typeRef": {"name": "string"}}]});
            document["types"]["Step"]["fields"]
                .as_array_mut()
                .expect("fields")
                .push(json!({"name": "minutes", "typeRef": {"name": "Int"}}));
            config["kinds"]["step"]["retentionDays"] = json!(30);
            config["kinds"]["step"]["units"] = json!({"timings": "keyed", "minutes": "excluded"});
            config["sweep"] = json!({"intervalMs": 60_000});
            config["primary"] = json!("trunk");
        })
        .expect("an allowed version");
        let removed = refused(next(&|config, _| {
            config["kinds"]
                .as_object_mut()
                .expect("kinds")
                .shift_remove("cover");
        }));
        assert!(
            removed.contains("it removes kind cover, whose rows the graphs hold"),
            "{removed}"
        );
        let parent = refused(next(&|config, _| {
            config["kinds"]["ingredient"]["parent"] = json!({"key": "stepKey", "of": "ingredient"});
        }));
        assert!(
            parent.contains("kind ingredient: its parent changes"),
            "{parent}"
        );
        let order = refused(next(&|config, _| {
            config["kinds"]["step"]
                .as_object_mut()
                .expect("step")
                .shift_remove("order");
        }));
        assert!(
            order.contains("kind step: its order changes from \"position\" to null"),
            "{order}"
        );
        let singleton = refused(next(&|config, _| {
            config["kinds"]["cover"]["singleton"] = json!(false);
        }));
        assert!(
            singleton.contains("kind cover: its singleton changes from true to false"),
            "{singleton}"
        );
        let unit = refused(next(&|config, _| {
            config["kinds"]["step"]["units"] = json!({});
        }));
        assert!(
            unit.contains("kind step: its unit of timings changes from \"keyed\" to \"atomic\""),
            "{unit}"
        );
        let retyped = refused(next(&|config, document| {
            let mut dish = document["types"]["Cover"].clone();
            dish["name"] = json!("Dish");
            document["types"]["Dish"] = dish;
            config["kinds"]["cover"]["type"] = json!("Dish");
        }));
        assert!(
            retyped.contains("kind cover: its type changes from \"Cover\" to \"Dish\""),
            "{retyped}"
        );
        // The compatibility rule keeps a kind's type as it keeps the
        // instance type.
        let required = refused(next(&|_, document| {
            document["types"]["Step"]["fields"]
                .as_array_mut()
                .expect("fields")
                .push(json!({"name": "serves", "typeRef": {"name": "Int"}, "required": true}));
        }));
        assert!(!required.is_empty());
        let dropped = refused(define(&engine, &recipe_document(None, "Recipe")));
        assert!(
            dropped.contains(
                "the graphs its instances root would stay behind with nothing to delete them"
            ),
            "{dropped}"
        );
        // Deleting the last instance deletes its graph: with no instance, a
        // kind may go and a unit change.
        engine
            .instances()
            .delete(&alice(), "Recipe", "soup", WriteOptions::default())
            .expect("delete");
        next(&|config, _| {
            config["kinds"]
                .as_object_mut()
                .expect("kinds")
                .shift_remove("cover");
            config["kinds"]["step"]["units"] = json!({});
        })
        .expect("a version with no instance");
    }
}

#[test]
fn after_a_new_version_adds_a_field_materialize_of_an_earlier_commit_returns_the_stored_content_hash(
) {
    for target in targets("branches_added_field") {
        let engine = opened(&target, recipe_config());
        let soup = Calls::new(&engine, "soup");
        let first = soup.change("first", json!({"step": {"upsert": [boil(), stir()]}}));
        soup.invoke(
            "releaseCommit",
            json!({"commit": first["id"], "version": 0}),
        );
        let draft = soup.branch("draft");
        let saved = soup.save(
            &draft,
            json!({"cover": {"upsert": [{"photoUrl": "soup.jpg"}]}}),
        );
        let drafted = soup.commit(&saved["ref"], json!({}))["commit"].clone();
        let stored = |r: &Value| -> Vec<Value> {
            soup.invoke("history", json!({"ref": r["id"]}))["commits"]
                .as_array()
                .expect("commits")
                .iter()
                .map(|commit| json!([commit["id"], commit["contentHash"]]))
                .collect()
        };
        let before: Vec<Value> = stored(&soup.main())
            .into_iter()
            .chain(stored(&draft))
            .collect();
        assert_eq!(
            before
                .iter()
                .map(|pair| pair[0].clone())
                .collect::<Vec<_>>(),
            [first["id"].clone(), drafted["id"].clone()]
        );
        // The next version: Step gains minutes, which every stored step
        // reads as null.
        let mut document = recipes(recipe_config());
        document["types"]["Step"]["fields"]
            .as_array_mut()
            .expect("fields")
            .push(json!({"name": "minutes", "typeRef": {"name": "Int"}}));
        define(&engine, &document).expect("define");
        assert_eq!(
            engine
                .schemas()
                .publish(&alice(), "Recipe", SchemaTarget::default())
                .expect("publish")
                .version,
            2
        );
        let main = soup.main();
        assert_eq!(
            content_of(
                &soup.compose(&main)["tree"],
                "step",
                &["instruction", "minutes"]
            ),
            json!([{"instruction": "Boil the water", "minutes": null}, {"instruction": "Stir", "minutes": null}])
        );
        let after: Vec<Value> = stored(&main).into_iter().chain(stored(&draft)).collect();
        assert_eq!(after, before);
        for pair in &before {
            assert_eq!(
                soup.invoke("materialize", json!({"commit": pair[0]}))["contentHash"],
                pair[1],
                "{}",
                pair[0]
            );
        }
        assert_eq!(soup.compose(&main)["contentHash"], first["contentHash"]);
        assert_eq!(
            soup.invoke("released", json!({}))["contentHash"],
            first["contentHash"]
        );
        // A value in the new field is content, and moves the hash.
        let key = soup.compose(&main)["tree"]["step"][0]["entity_key"].clone();
        let mut timed_row = boil();
        extend(&mut timed_row, json!({"entity_key": key, "minutes": 5}));
        let timed = soup.change("timed", json!({"step": {"upsert": [timed_row]}}));
        assert_ne!(timed["contentHash"], first["contentHash"]);
        assert_eq!(
            soup.invoke("materialize", json!({"commit": timed["id"]}))["contentHash"],
            timed["contentHash"]
        );
    }
}

#[cfg(feature = "sqlite")]
/// Loads the file the TypeScript engine wrote (`typescript.sql`) into a new
/// SQLite file, with foreign keys deferred, since a row may name one
/// inserted after it.
fn load_typescript_file(path: &std::path::Path) {
    let text = std::fs::read_to_string(
        support::repository().join("runtime/engine/testdata/sqlite/typescript.sql"),
    )
    .expect("the SQLite corpus");
    let db = superschematic_engine::storage::Db::open_sqlite(
        path,
        superschematic_engine::storage::SqliteOptions::default(),
    )
    .expect("a file");
    db.write(superschematic_engine::storage::Lock::Catalog, |tx| {
        tx.exec("PRAGMA defer_foreign_keys = ON")?;
        for statement in text.lines().filter(|line| !line.is_empty()) {
            tx.exec(statement)?;
        }
        Ok::<_, superschematic_engine::storage::Error>(())
    })
    .expect("load the file");
}

#[cfg(feature = "sqlite")]
#[test]
fn a_file_the_typescript_engine_wrote_reads_its_recipe_as_recorded_and_takes_new_writes() {
    let expected: Value = serde_json::from_str(
        &std::fs::read_to_string(
            support::repository().join("runtime/engine/testdata/sqlite/expected.json"),
        )
        .expect("the expected reads"),
    )
    .expect("JSON");
    let dir = std::env::temp_dir().join(format!(
        "branches_typescript_{}_{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .expect("clock")
            .as_nanos()
    ));
    std::fs::create_dir_all(&dir).expect("a directory");
    let path = dir.join("engine.db");
    load_typescript_file(&path);
    let mut options = EngineOptions::sqlite(&path);
    options.policy = Some(Policy::allow_all());
    // The file's other schemas compose behaviors the reads below never run.
    options.behaviors = BehaviorSet::new().with(Branches);
    options.clock = Clock::Manual(expected["clock"].as_i64().expect("a clock"));
    options.namespaces = NamespaceOptions {
        names: vec!["tree".into(), "billing".into()],
        shared: None,
    };
    let engine = Engine::open(options).expect("open the file");
    // The recipe's reads give what the TypeScript engine read.
    let mut replayed = 0;
    for step in expected["reads"].as_array().expect("reads") {
        let args = &step["args"];
        if step["call"] != "instances.invoke" || args[0] != "Recipe" {
            continue;
        }
        let operation = args[2].as_str().expect("an operation");
        let got = engine
            .instances()
            .invoke(
                &alice(),
                "Recipe",
                args[1].as_str().expect("an id"),
                operation,
                &args[3],
                WriteOptions::default(),
            )
            .expect("a read");
        assert!(
            superschematic_engine::json::js_equal(&got, &step["expect"]),
            "{operation}: {got} is not {}",
            step["expect"]
        );
        replayed += 1;
    }
    assert_eq!(replayed, 3);
    // And it takes writes: a draft saved, committed, merged tagged and
    // released, the pointer at its next version.
    let soup = Calls::new(&engine, "soup");
    let released = release_field(&engine).as_i64().expect("a release");
    let cover = soup.compose(&soup.main())["tree"]["cover"][0]["entity_key"].clone();
    let commit = soup.change(
        "again",
        json!({"cover": {"upsert": [{"entity_key": cover, "photoUrl": "stew.jpg"}]}}),
    );
    assert_eq!(
        soup.invoke(
            "releaseCommit",
            json!({"commit": commit["id"], "version": released})
        ),
        json!({"commit": commit["id"], "version": released + 1})
    );
    assert_eq!(
        content_of(
            &soup.invoke("released", json!({}))["tree"],
            "cover",
            &["photoUrl"]
        ),
        json!([{"photoUrl": "stew.jpg"}])
    );
    drop(engine);
    let _ = std::fs::remove_dir_all(&dir);
}
