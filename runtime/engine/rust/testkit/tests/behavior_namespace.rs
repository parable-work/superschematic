//! A behavior's namespace operations (D77, primitive 3): an operation with
//! `"scope": "namespace"` runs on its behavior's tables across every schema
//! of the namespace that composes it. `sql.readable()` holds the composing
//! schemas the principal may read, the policy asked `read` once each a
//! call, a refused one left out; a writing one writes the behavior's
//! tables and appends no event; a read-only one's `run` is refused; it
//! reads instances and invokes their read-only operations, and nothing
//! that writes. The policy is asked `{ principal, action, namespace,
//! behavior, operation }`, which `permissionPolicy` answers read to anyone
//! and write to a holder of `<behavior>.<operation>`. Also an operation
//! that asks `read` (`"asks": "read"`), a beside one that writes only its
//! caller's state. On SQLite and, with the variable set, on Postgres.

mod support;

use std::sync::LazyLock;

use serde_json::{json, Value};

use superschematic_engine::api::{handle, ApiOptions, Caller, Request, Response};
use superschematic_engine::behaviors::{BehaviorMigration, ColumnSpec, NamespaceCx, TableSpec};
use superschematic_engine::storage::SqlValue;
use superschematic_engine::tools::ToolTarget;
use superschematic_engine::{
    Behavior, BehaviorSet, ConfigTarget, CreateOptions, Engine, EngineErrorCode, Hooks,
    NamespaceCall, Policy, Principal, ReadEvents, Result, WriteOptions,
};
use support::{alice, code, defect, named, refusal, targets, RecordingPolicy, Target};

const NONE: &str = r#"{"type": "object", "additionalProperties": false}"#;

static DECLARATION: LazyLock<String> = LazyLock::new(|| {
    let none: Value = serde_json::from_str(NONE).unwrap();
    json!({
        "name": "test.Inbox",
        "description": "Notes on instances, read across a namespace.",
        "configSchema": {"type": "object", "properties": {"label": {"type": "string"}}},
        "operations": [
            {"name": "add", "paramsSchema": {"type": "object", "additionalProperties": false, "required": ["body"], "properties": {"body": {"type": "string"}}}, "resultSchema": true, "writes": true, "beside": true},
            {"name": "mark", "paramsSchema": none, "resultSchema": true, "writes": true, "beside": true, "log": false, "asks": "read"},
            {"name": "markAll", "paramsSchema": none, "resultSchema": true, "writes": true, "beside": true, "log": false},
            {"name": "stamp", "paramsSchema": none, "resultSchema": true, "writes": true, "beside": true},
            {"name": "peek", "paramsSchema": none, "resultSchema": {"type": "integer"}},
            {"name": "bump", "paramsSchema": none, "resultSchema": true, "writes": true},
            {"name": "tally", "paramsSchema": none, "resultSchema": {"type": "integer"}, "scope": "schema"},
            {"name": "notes", "description": "Every note the caller may read.", "paramsSchema": none, "resultSchema": {"type": "object"}, "scope": "namespace"},
            {"name": "clear", "paramsSchema": none, "resultSchema": {"type": "object"}, "writes": true, "scope": "namespace"},
            {"name": "hold", "paramsSchema": none, "resultSchema": true, "writes": true, "scope": "namespace"},
            {"name": "probe", "paramsSchema": {"type": "object", "additionalProperties": false, "required": ["mode"], "properties": {"mode": {"enum": ["run", "get", "peek", "tally", "bump", "create", "configs", "engine", "instances"]}}}, "resultSchema": true, "scope": "namespace"}
        ],
        "vetoes": []
    })
    .to_string()
});

struct Inbox;

// What the writer test's operations did, in order.
static ORDER: std::sync::Mutex<Vec<&'static str>> = std::sync::Mutex::new(Vec::new());
static HOLDING: std::sync::atomic::AtomicBool = std::sync::atomic::AtomicBool::new(false);

fn note(cx_sql: &superschematic_engine::behaviors::SqlWriter<'_>, args: [&str; 4]) -> Result<()> {
    let table = cx_sql.table("note")?;
    cx_sql.run(
        format!(
            "INSERT INTO {table} (namespace, schema, instance, body, seen) VALUES (?, ?, ?, ?, 0)"
        ),
        &[
            args[0].into(),
            args[1].into(),
            args[2].into(),
            args[3].into(),
        ],
    )?;
    Ok(())
}

fn list(cx: &NamespaceCx<'_, Value>) -> Result<Value> {
    let sql = cx.sql();
    let (table, readable) = (sql.table("note")?, sql.readable()?);
    let rows = sql.all(
        format!(
            "SELECT n.schema, n.instance, n.body, n.seen FROM {table} AS n JOIN {readable} AS r ON r.schema = n.schema WHERE n.namespace = ? ORDER BY n.schema, n.instance, n.body"
        ),
        &[cx.namespace().into()],
    )?;
    // A second statement on the relation asks the policy nothing more.
    let schemas = sql.all(
        format!("SELECT schema FROM {readable} ORDER BY schema"),
        &[],
    )?;
    Ok(json!({
        "items": rows.iter().map(|row| json!([
            row.text("schema").unwrap(),
            row.text("instance").unwrap(),
            row.text("body").unwrap(),
            row.i64("seen").unwrap(),
        ])).collect::<Vec<_>>(),
        "readable": schemas.iter().map(|row| row.text("schema").unwrap().to_owned()).collect::<Vec<_>>(),
    }))
}

impl Behavior for Inbox {
    type Config = Value;

    fn declaration(&self) -> &str {
        &DECLARATION
    }

    fn parse_config(&self, raw: &Value, _: &ConfigTarget<'_>) -> Result<Value> {
        Ok(raw.clone())
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            migrations: vec![BehaviorMigration::new(1, "notes").table(
                TableSpec::new("note")
                    .column("namespace", ColumnSpec::text().not_null())
                    .column("schema", ColumnSpec::text().not_null())
                    .column("instance", ColumnSpec::text().not_null())
                    .column("body", ColumnSpec::text().not_null())
                    .column("seen", ColumnSpec::integer().not_null().default(0))
                    .primary_key(&["namespace", "schema", "instance", "body"]),
            )],
            operations: vec![
                ("add", |_, cx, params| {
                    note(
                        &cx.sql(),
                        [
                            cx.namespace(),
                            cx.schema(),
                            cx.id(),
                            params["body"].as_str().unwrap_or_default(),
                        ],
                    )?;
                    Ok(Value::Null)
                }),
                ("mark", |_, cx, _| {
                    let table = cx.sql().table("note")?;
                    cx.sql().run(
                        format!("UPDATE {table} SET seen = 1 WHERE namespace = ? AND schema = ? AND instance = ?"),
                        &[cx.namespace().into(), cx.schema().into(), cx.id().into()],
                    )?;
                    Ok(Value::Null)
                }),
                ("markAll", |_, _, _| Ok(Value::Null)),
                ("stamp", |_, cx, _| {
                    note(&cx.sql(), [cx.namespace(), cx.schema(), cx.id(), "stamp"])?;
                    ORDER.lock().unwrap().push("stamp");
                    Ok(Value::Null)
                }),
                ("peek", |_, cx, _| {
                    let table = cx.sql().table("note")?;
                    let row = cx.sql().get(
                        format!("SELECT COUNT(*) AS c FROM {table} WHERE namespace = ? AND schema = ? AND instance = ?"),
                        &[cx.namespace().into(), cx.schema().into(), cx.id().into()],
                    )?;
                    Ok(json!(row.map_or(0, |row| row.i64("c").unwrap())))
                }),
                ("bump", |_, cx, _| {
                    cx.update(&json!({"title": "Bumped"}))?;
                    Ok(Value::Null)
                }),
            ],
            schema_operations: vec![("tally", |_, _, _| Ok(json!(0)))],
            namespace_operations: vec![
                ("notes", |_, cx, _| list(cx)),
                ("clear", |_, cx, _| {
                    let sql = cx.sql();
                    let (table, readable) = (sql.table("note")?, sql.readable()?);
                    let changed = sql.run(
                        format!("UPDATE {table} SET seen = 1 WHERE namespace = ? AND schema IN (SELECT schema FROM {readable})"),
                        &[cx.namespace().into()],
                    )?;
                    Ok(json!({"changed": changed.changes}))
                }),
                ("hold", |_, cx, _| {
                    // Writes, then holds its transaction a while.
                    let table = cx.sql().table("note")?;
                    cx.sql().run(
                        format!("UPDATE {table} SET seen = 2 WHERE namespace = ?"),
                        &[cx.namespace().into()],
                    )?;
                    ORDER.lock().unwrap().push("hold");
                    HOLDING.store(true, std::sync::atomic::Ordering::SeqCst);
                    std::thread::sleep(std::time::Duration::from_millis(400));
                    ORDER.lock().unwrap().push("released");
                    Ok(Value::Null)
                }),
                ("probe", |_, cx, params| {
                    let sql = cx.sql();
                    match params["mode"].as_str().unwrap_or_default() {
                        "run" => {
                            let table = sql.table("note")?;
                            sql.run(format!("DELETE FROM {table}"), &[])?;
                            Ok(Value::Null)
                        }
                        "get" => Ok(cx
                            .instances()
                            .get("Note", "n1", Some(&[]))?
                            .map_or(Value::Null, |record| json!(record.data["title"]))),
                        "peek" => cx
                            .instances()
                            .invoke("Note", "n1", "peek", &json!({}), None),
                        "tally" => cx.instances().invoke_schema("Note", "tally", &json!({})),
                        "bump" => cx
                            .instances()
                            .invoke("Note", "n1", "bump", &json!({}), None),
                        "create" => cx
                            .instances()
                            .create("Note", &json!({"title": "New"}), None, None)
                            .map(|_| Value::Null),
                        "configs" => Ok(Value::Array(
                            cx.configs()?
                                .into_iter()
                                .map(|(schema, config)| json!([schema, config["label"]]))
                                .collect(),
                        )),
                        "engine" => {
                            sql.all("SELECT id FROM engine_instances", &[])?;
                            Ok(Value::Null)
                        }
                        _ => {
                            sql.instances()?;
                            Ok(Value::Null)
                        }
                    }
                }),
            ],
            ..Hooks::default()
        }
    }
}

fn document(name: &str, label: Option<&str>) -> Value {
    let mut document = support::schema_document(
        name,
        json!([{"name": "title", "typeRef": {"name": "string"}, "required": true}]),
        json!({}),
    );
    if let Some(label) = label {
        document["types"][name]["behaviors"] =
            json!([{"name": "test.Inbox", "config": {"label": label}}]);
    }
    document
}

// An engine whose Note and Task compose test.Inbox, and whose Plain does
// not, each with an instance and a note on it.
fn opened(target: &Target, policy: Policy) -> Engine {
    let engine = target.open_with(BehaviorSet::new().with(Inbox), |options| {
        options.policy = Some(policy);
    });
    for (schema, label) in [
        ("Note", Some("notes")),
        ("Task", Some("tasks")),
        ("Plain", None),
    ] {
        publish_in(&engine, &document(schema, label), "default");
    }
    // The system principal sets up, whatever the policy.
    let system = Principal::system();
    for (schema, id) in [("Note", "n1"), ("Task", "t1"), ("Plain", "p1")] {
        engine
            .instances()
            .create(
                &system,
                schema,
                &json!({"title": id}),
                CreateOptions {
                    id: Some(id),
                    ..CreateOptions::default()
                },
            )
            .unwrap();
        if schema != "Plain" {
            engine
                .instances()
                .invoke(
                    &system,
                    schema,
                    id,
                    "add",
                    &json!({"body": format!("on {id}")}),
                    WriteOptions::default(),
                )
                .unwrap();
        }
    }
    engine
}

// bob may not read Task; everyone else may do anything.
fn no_task_for_bob() -> RecordingPolicy {
    RecordingPolicy::new(|request| {
        !(request.principal().subject == "bob" && request.schema() == Some("Task"))
    })
}

fn call(engine: &Engine, who: &Principal, operation: &str, params: Value) -> Result<Value> {
    engine.behaviors().invoke(
        who,
        &NamespaceCall {
            namespace: None,
            behavior: "test.Inbox",
            operation,
            params: Some(&params),
        },
    )
}

#[test]
fn readable_holds_the_composing_schemas_the_principal_may_read_and_skips_a_refused_one() {
    for target in targets("readable_skips_a_refused_schema") {
        let policy = no_task_for_bob();
        let engine = opened(&target, Policy::custom(policy.clone()));
        let everything = call(&engine, &alice(), "notes", json!({})).unwrap();
        assert_eq!(
            everything,
            json!({"items": [["Note", "n1", "on n1", 0], ["Task", "t1", "on t1", 0]], "readable": ["Note", "Task"]})
        );
        policy.take();
        let bobs = call(&engine, &named("bob"), "notes", json!({})).unwrap();
        assert_eq!(
            bobs,
            json!({"items": [["Note", "n1", "on n1", 0]], "readable": ["Note"]})
        );
        // The call's own question, then read once per composing schema,
        // though two statements named the relation; Plain composes none.
        assert_eq!(
            policy.take(),
            [
                json!({"principal": "bob", "action": "read", "namespace": "default", "behavior": "test.Inbox", "operation": "notes"}),
                json!({"principal": "bob", "action": "read", "namespace": "default", "schema": "Note"}),
                json!({"principal": "bob", "action": "read", "namespace": "default", "schema": "Task"}),
            ]
        );
    }
}

#[test]
fn a_writing_operation_writes_its_tables_and_appends_no_event_and_changes_no_instance() {
    for target in targets("a_writing_namespace_operation") {
        let policy = no_task_for_bob();
        let engine = opened(&target, Policy::custom(policy.clone()));
        let events = |engine: &Engine| {
            engine
                .events()
                .read(&alice(), &ReadEvents::default())
                .unwrap()
                .events
                .len()
        };
        let before_events = events(&engine);
        let before = engine
            .instances()
            .get(&alice(), "Note", "n1", Default::default())
            .unwrap()
            .unwrap();
        policy.take();
        assert_eq!(
            call(&engine, &named("bob"), "clear", json!({})).unwrap(),
            json!({"changed": 1}),
            "bob's readable schemas alone"
        );
        assert_eq!(
            policy.take()[0],
            json!({"principal": "bob", "action": "write", "namespace": "default", "behavior": "test.Inbox", "operation": "clear"})
        );
        assert_eq!(
            call(&engine, &alice(), "notes", json!({})).unwrap()["items"],
            json!([["Note", "n1", "on n1", 1], ["Task", "t1", "on t1", 0]])
        );
        assert_eq!(events(&engine), before_events, "no event");
        let after = engine
            .instances()
            .get(&alice(), "Note", "n1", Default::default())
            .unwrap()
            .unwrap();
        assert_eq!(
            (after.seq, after.updated_at, after.updated_by),
            (before.seq, before.updated_at, before.updated_by)
        );
    }
}

#[test]
fn a_read_only_operations_run_and_any_writing_invoke_or_create_is_a_behavior_error() {
    for target in targets("a_read_only_namespace_operation") {
        let engine = opened(&target, Policy::allow_all());
        let probe = |mode: &str| call(&engine, &alice(), "probe", json!({"mode": mode}));
        let (behavior, message) = defect(probe("run"));
        assert_eq!(behavior, "test.Inbox");
        assert!(
            message.contains("does not write cannot run a statement"),
            "{message}"
        );
        let (_, message) = defect(probe("bump"));
        assert!(
            message.contains("cannot invoke bump of Note, which writes"),
            "{message}"
        );
        let (_, message) = defect(probe("create"));
        assert!(
            message.contains("cannot create an instance of Note"),
            "{message}"
        );
        let (_, message) = defect(probe("engine"));
        assert!(message.contains("SQL refused"), "{message}");
        let (_, message) = defect(probe("instances"));
        assert!(message.contains("no sql.instances()"), "{message}");
        // Reads and read-only operations of instances are the principal's.
        assert_eq!(probe("get").unwrap(), json!("n1"));
        assert_eq!(probe("peek").unwrap(), json!(1));
        assert_eq!(probe("tally").unwrap(), json!(0));
        // Nothing the refused calls tried stayed.
        assert_eq!(
            call(&engine, &alice(), "notes", json!({})).unwrap()["items"]
                .as_array()
                .unwrap()
                .len(),
            2
        );
        assert_eq!(
            engine
                .instances()
                .get(&alice(), "Note", "n1", Default::default())
                .unwrap()
                .unwrap()
                .data["title"],
            json!("n1")
        );
    }
}

#[test]
fn configs_gives_each_composing_schemas_config_whatever_the_principal_reads() {
    for target in targets("namespace_configs") {
        let engine = opened(&target, Policy::custom(no_task_for_bob()));
        assert_eq!(
            call(&engine, &named("bob"), "probe", json!({"mode": "configs"})).unwrap(),
            json!([["Note", "notes"], ["Task", "tasks"]])
        );
    }
}

#[test]
fn an_operation_of_another_scope_is_not_found_and_so_is_a_namespace_one_on_a_schema() {
    for target in targets("namespace_not_found") {
        let engine = opened(&target, Policy::allow_all());
        for (operation, said) in [
            ("peek", "test.Inbox's peek is an instance operation"),
            ("tally", "test.Inbox's tally is a schema-level operation"),
            ("nothing", "behavior test.Inbox has no namespace operation nothing (its namespace operations: notes, clear, hold, probe)"),
        ] {
            let refused = refusal(call(&engine, &alice(), operation, json!({})));
            assert_eq!(refused.code(), EngineErrorCode::NotFound);
            assert!(refused.message().starts_with(said), "{}", refused.message());
        }
        let refused = refusal(engine.behaviors().invoke(
            &alice(),
            &NamespaceCall {
                namespace: None,
                behavior: "test.Nothing",
                operation: "notes",
                params: None,
            },
        ));
        assert_eq!(
            (refused.code(), refused.message()),
            (
                EngineErrorCode::NotFound,
                "this engine runs no behavior test.Nothing"
            )
        );
        let as_instance = refusal(engine.instances().invoke(
            &alice(),
            "Note",
            "n1",
            "notes",
            &json!({}),
            WriteOptions::default(),
        ));
        assert_eq!(as_instance.code(), EngineErrorCode::NotFound);
        assert!(as_instance
            .message()
            .contains("is a namespace operation of test.Inbox"));
        assert_eq!(
            code(
                engine
                    .instances()
                    .invoke_schema(&alice(), "Note", "clear", &json!({}), None)
            ),
            EngineErrorCode::NotFound
        );
        assert_eq!(
            code(call(&engine, &alice(), "probe", json!({"mode": "nope"}))),
            EngineErrorCode::InvalidArgument
        );
    }
}

#[test]
fn permission_policy_allows_a_read_to_anyone_and_a_write_to_a_holder_of_the_operation() {
    for target in targets("namespace_permission_policy") {
        let engine = opened(&target, Policy::permission());
        let carol = named("carol");
        // carol may call list, and reads nothing: she may read no schema.
        assert_eq!(
            call(&engine, &carol, "notes", json!({})).unwrap(),
            json!({"items": [], "readable": []})
        );
        let note_reader = Principal::new("nora", ["Note.read"]);
        assert_eq!(
            call(&engine, &note_reader, "notes", json!({})).unwrap()["readable"],
            json!(["Note"])
        );
        let refused = refusal(call(&engine, &carol, "clear", json!({})));
        assert_eq!(
            (refused.code(), refused.message()),
            (
                EngineErrorCode::Forbidden,
                "carol may not call test.Inbox's clear (write) in namespace default"
            )
        );
        let clearer = Principal::new("dave", ["test.Inbox.clear", "Note.read"]);
        assert_eq!(
            call(&engine, &clearer, "clear", json!({})).unwrap(),
            json!({"changed": 1})
        );
        // The listing question answers the same way.
        let tools = |who: &Principal| -> Value {
            let manifest = engine.tools().manifest(who, ToolTarget::default()).unwrap();
            manifest["tools"]
                .as_array()
                .unwrap()
                .iter()
                .filter(|tool| tool["namespace"] == json!("test-inbox"))
                .map(|tool| {
                    json!([
                        tool["name"],
                        tool["mcp"]["hidden"],
                        tool["mcp"].get("hiddenBy").cloned().unwrap_or(json!([]))
                    ])
                })
                .collect()
        };
        // A tool the behavior listing question refuses is hidden by policy.
        assert_eq!(
            tools(&clearer),
            json!([
                ["test-inbox.notes", false, []],
                ["test-inbox.clear", false, []],
                ["test-inbox.hold", true, ["policy"]],
                ["test-inbox.probe", false, []]
            ])
        );
        assert_eq!(
            tools(&note_reader),
            json!([
                ["test-inbox.notes", false, []],
                ["test-inbox.clear", true, ["policy"]],
                ["test-inbox.hold", true, ["policy"]],
                ["test-inbox.probe", false, []]
            ])
        );
        // carol reads no schema that composes test.Inbox: no tools at all.
        assert_eq!(tools(&carol), json!([]));
    }
}

#[test]
fn a_behavior_no_schema_composes_still_runs_with_an_empty_readable() {
    for target in targets("namespace_no_storage") {
        let engine = target.open(BehaviorSet::new().with(Inbox));
        assert_eq!(
            call(&engine, &alice(), "notes", json!({})).unwrap(),
            json!({"items": [], "readable": []})
        );
        assert_eq!(
            call(&engine, &alice(), "clear", json!({})).unwrap(),
            json!({"changed": 0})
        );
    }
}

#[test]
fn an_archived_namespace_refuses_a_writing_one_and_runs_a_read() {
    for target in targets("namespace_archived") {
        let engine = opened(&target, Policy::allow_all());
        engine.namespaces().create(&alice(), "acme").unwrap();
        // A schema there composes test.Inbox, so its tools are listed.
        publish_in(&engine, &document("Note", Some("notes")), "acme");
        engine.namespaces().archive(&alice(), "acme").unwrap();
        let at = |operation: &str| {
            engine.behaviors().invoke(
                &alice(),
                &NamespaceCall {
                    namespace: Some("acme"),
                    behavior: "test.Inbox",
                    operation,
                    params: None,
                },
            )
        };
        assert_eq!(code(at("clear")), EngineErrorCode::NamespaceArchived);
        assert_eq!(
            at("notes").unwrap(),
            json!({"items": [], "readable": ["Note"]})
        );
        // Its tools say so: a writing one is hidden by archived, a read
        // one is listed.
        let hidden: Vec<Value> = engine
            .tools()
            .manifest(
                &alice(),
                ToolTarget {
                    namespace: Some("acme"),
                    filter: None,
                },
            )
            .unwrap()["tools"]
            .as_array()
            .unwrap()
            .iter()
            .filter(|tool| tool["namespace"] == json!("test-inbox"))
            .map(|tool| {
                json!([
                    tool["name"],
                    tool["mcp"].get("hiddenBy").cloned().unwrap_or(json!([]))
                ])
            })
            .collect();
        assert_eq!(
            hidden,
            [
                json!(["test-inbox.notes", []]),
                json!(["test-inbox.clear", ["archived"]]),
                json!(["test-inbox.hold", ["archived"]]),
                json!(["test-inbox.probe", []])
            ]
        );
    }
}

fn request(path: &str, who: Principal, body: Option<&Value>) -> Request {
    let mut request = Request {
        method: "POST".to_owned(),
        prefix: "/api".to_owned(),
        path: format!("/api{path}"),
        caller: Caller {
            principal: Some(who),
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

fn body_of(response: &Response) -> Value {
    serde_json::from_slice(response.bytes()).expect("a JSON body")
}

#[test]
fn the_http_route_and_the_mcp_tool_call_it() {
    for target in targets("namespace_http_and_mcp") {
        let engine = opened(&target, Policy::custom(no_task_for_bob()));
        let options = ApiOptions::default();
        let path = "/namespaces/default/behaviors/test.Inbox/operations/notes";
        let response = handle(&engine, &options, &request(path, named("bob"), None));
        assert_eq!(response.status, 200);
        assert_eq!(
            body_of(&response)["data"],
            json!({"items": [["Note", "n1", "on n1", 0]], "readable": ["Note"]})
        );
        let response = handle(
            &engine,
            &options,
            &request(
                "/namespaces/default/behaviors/test.Inbox/operations/probe",
                alice(),
                Some(&json!({"mode": "configs"})),
            ),
        );
        assert_eq!(
            body_of(&response)["data"],
            json!([["Note", "notes"], ["Task", "tasks"]])
        );
        for (path, status) in [
            (
                "/namespaces/default/behaviors/test.Inbox/operations/peek",
                404,
            ),
            (
                "/namespaces/default/behaviors/test.Nothing/operations/notes",
                404,
            ),
            (
                "/namespaces/nowhere/behaviors/test.Inbox/operations/notes",
                404,
            ),
        ] {
            let response = handle(&engine, &options, &request(path, alice(), None));
            assert_eq!(response.status, status, "{path}");
        }
        let response = handle(
            &engine,
            &options,
            &request(
                "/namespaces/default/behaviors/test.Inbox/operations/probe",
                alice(),
                Some(&json!({"mode": 1})),
            ),
        );
        assert_eq!(
            (response.status, body_of(&response)["code"].clone()),
            (400, json!("invalid_argument"))
        );
        // MCP: the tool is listed, and its call runs the operation.
        let mcp = |message: Value| -> Value {
            let mut post = request("/namespaces/default/mcp", named("bob"), Some(&message));
            post.headers.push((
                "accept".to_owned(),
                "application/json, text/event-stream".to_owned(),
            ));
            post.headers
                .push(("mcp-protocol-version".to_owned(), "2025-06-18".to_owned()));
            let response = handle(&engine, &options, &post);
            assert_eq!(response.status, 200);
            let text = String::from_utf8(response.bytes().to_vec()).unwrap();
            text.lines()
                .find_map(|line| line.strip_prefix("data: "))
                .map(|data| serde_json::from_str(data).unwrap())
                .unwrap()
        };
        let listed = mcp(json!({"jsonrpc": "2.0", "id": 1, "method": "tools/list"}));
        let tool = listed["result"]["tools"]
            .as_array()
            .unwrap()
            .iter()
            .find(|tool| tool["name"] == json!("test_inbox_notes"))
            .cloned()
            .expect("test_inbox_notes");
        assert_eq!(
            (&tool["title"], &tool["annotations"]["readOnlyHint"]),
            (&json!("test.Inbox: notes"), &json!(true))
        );
        let called = mcp(
            json!({"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": {"name": "test_inbox_notes", "arguments": {}}}),
        );
        assert_eq!(
            called["result"]["structuredContent"],
            json!({"items": [["Note", "n1", "on n1", 0]], "readable": ["Note"]})
        );
    }
}

#[test]
fn the_documents_name_namespace_operations_under_the_behavior() {
    for target in targets("namespace_documents") {
        let engine = opened(&target, Policy::allow_all());
        let described = engine
            .tools()
            .describe(&alice(), "Note", ToolTarget::default())
            .unwrap();
        let inbox = &described["behaviors"][0];
        assert_eq!(
            (&inbox["operations"], &inbox["namespaceOperations"]),
            (
                &json!(["add", "mark", "markAll", "stamp", "peek", "bump", "tally"]),
                &json!(["notes", "clear", "hold", "probe"])
            )
        );
        let operations: Vec<&Value> = described["operations"]
            .as_array()
            .unwrap()
            .iter()
            .map(|operation| &operation["name"])
            .collect();
        assert!(!operations.contains(&&json!("notes")), "{operations:?}");
        let document = engine
            .tools()
            .describe_behavior(&alice(), "test.Inbox")
            .unwrap();
        let clear = document["operations"]
            .as_array()
            .unwrap()
            .iter()
            .find(|operation| operation["name"] == json!("clear"))
            .unwrap();
        assert_eq!(
            (&clear["scope"], &clear["writes"]),
            (&json!("namespace"), &json!(true))
        );
        let mark = document["operations"]
            .as_array()
            .unwrap()
            .iter()
            .find(|operation| operation["name"] == json!("mark"))
            .unwrap();
        assert_eq!(
            (&mark["beside"], &mark["log"], &mark["asks"]),
            (&json!(true), &json!(false), &json!("read"))
        );
    }
}

#[test]
fn an_operation_that_asks_read_runs_for_a_reader_and_one_that_does_not_is_refused() {
    for target in targets("asks_read") {
        // A reader: read allowed, write refused.
        let policy = RecordingPolicy::new(|request| request.action() == "read");
        let engine = target.open_with(BehaviorSet::new().with(Inbox), |options| {
            options.policy = Some(Policy::custom(policy.clone()));
        });
        let admin = Principal::system();
        publish_in(&engine, &document("Note", Some("notes")), "default");
        engine
            .instances()
            .create(
                &admin,
                "Note",
                &json!({"title": "n1"}),
                CreateOptions {
                    id: Some("n1"),
                    ..CreateOptions::default()
                },
            )
            .unwrap();
        engine
            .instances()
            .invoke(
                &admin,
                "Note",
                "n1",
                "add",
                &json!({"body": "hi"}),
                WriteOptions::default(),
            )
            .unwrap();
        policy.take();
        let reader = named("rita");
        engine
            .instances()
            .invoke(
                &reader,
                "Note",
                "n1",
                "mark",
                &json!({}),
                WriteOptions::default(),
            )
            .unwrap();
        assert_eq!(
            policy.take(),
            [
                json!({"principal": "rita", "action": "read", "namespace": "default", "schema": "Note", "operation": "mark"})
            ]
        );
        assert_eq!(
            call(&engine, &admin, "notes", json!({})).unwrap()["items"],
            json!([["Note", "n1", "hi", 1]])
        );
        assert_eq!(
            code(engine.instances().invoke(
                &reader,
                "Note",
                "n1",
                "markAll",
                &json!({}),
                WriteOptions::default()
            )),
            EngineErrorCode::Forbidden
        );
        // An archived namespace lets it run, as a read.
        engine.namespaces().create(&admin, "acme").unwrap();
        publish_in(&engine, &document("Note", Some("notes")), "acme");
        engine
            .instances()
            .create(
                &admin,
                "Note",
                &json!({"title": "a"}),
                CreateOptions {
                    namespace: Some("acme"),
                    id: Some("a1"),
                    ..CreateOptions::default()
                },
            )
            .unwrap();
        engine.namespaces().archive(&admin, "acme").unwrap();
        let in_acme = WriteOptions {
            namespace: Some("acme"),
            ..WriteOptions::default()
        };
        engine
            .instances()
            .invoke(&reader, "Note", "a1", "mark", &json!({}), in_acme)
            .unwrap();
        assert_eq!(
            code(
                engine
                    .instances()
                    .invoke(&admin, "Note", "a1", "markAll", &json!({}), in_acme)
            ),
            EngineErrorCode::NamespaceArchived
        );
    }
}

fn publish_in(engine: &Engine, document: &Value, namespace: &str) {
    let record = engine
        .schemas()
        .define(
            &Principal::system(),
            document,
            superschematic_engine::DefineOptions {
                namespace: Some(namespace),
                source: None,
            },
        )
        .unwrap();
    engine
        .schemas()
        .publish(
            &Principal::system(),
            &record.name,
            superschematic_engine::SchemaTarget {
                namespace: Some(namespace),
            },
        )
        .unwrap();
}

#[test]
fn asks_read_is_refused_unless_beside_with_no_log_and_namespace_scope_refuses_beside_and_log() {
    for (operation, problem) in [
        (
            json!({"name": "look", "paramsSchema": {"type": "object", "additionalProperties": false}, "resultSchema": true, "writes": true, "beside": true, "asks": "read"}),
            "operation look asks \"read\" and is not beside the instance with \"log\": false",
        ),
        (
            json!({"name": "look", "paramsSchema": {"type": "object", "additionalProperties": false}, "resultSchema": true, "writes": true, "beside": true, "log": false, "asks": "write"}),
            "operation look asks \"write\": the only value is \"read\"",
        ),
        (
            json!({"name": "look", "paramsSchema": {"type": "object", "additionalProperties": false}, "resultSchema": true, "scope": "namespace", "beside": true}),
            "operation look has scope \"namespace\" and is beside the instance",
        ),
        (
            json!({"name": "look", "paramsSchema": {"type": "object", "additionalProperties": false}, "resultSchema": true, "scope": "namespace", "log": true}),
            "operation look has scope \"namespace\" and sets \"log\"",
        ),
    ] {
        let declaration = json!({"name": "test.Odd", "operations": [operation]});
        let problems = superschematic_engine::behaviors::check_declaration(&declaration);
        assert!(
            problems.iter().any(|each| each.starts_with(problem)),
            "{problems:?}"
        );
    }
}

// The engine's registration holds an implementation's namespace operations
// to the ones its declaration names.
#[test]
fn registration_holds_namespace_operations_to_the_declaration() {
    struct Missing;
    impl Behavior for Missing {
        type Config = Value;
        fn declaration(&self) -> &str {
            r#"{"name": "test.Missing", "operations": [{"name": "notes", "paramsSchema": {"type": "object", "additionalProperties": false}, "resultSchema": true, "scope": "namespace"}]}"#
        }
        fn parse_config(&self, raw: &Value, _: &ConfigTarget<'_>) -> Result<Value> {
            Ok(raw.clone())
        }
        fn hooks(&self) -> Hooks<Self> {
            Hooks::default()
        }
    }
    let target = &targets("namespace_registration")[0];
    let engine = target.open(BehaviorSet::new());
    let message = support::usage(engine.behaviors().register(Missing));
    assert!(
        message.contains("its declaration names namespaceOperations it does not implement: notes"),
        "{message}"
    );
    let _ = SqlValue::Null;
}

// A writing namespace operation holds its namespace's writer, as an
// instance write does: a write of the same namespace waits for it.
#[test]
fn a_writing_operation_holds_the_namespaces_writer() {
    for target in targets("namespace_writer") {
        let engine = opened(&target, Policy::allow_all());
        ORDER.lock().unwrap().clear();
        HOLDING.store(false, std::sync::atomic::Ordering::SeqCst);
        std::thread::scope(|scope| {
            let holder = scope.spawn(|| call(&engine, &alice(), "hold", json!({})).unwrap());
            while !HOLDING.load(std::sync::atomic::Ordering::SeqCst) {
                std::thread::sleep(std::time::Duration::from_millis(5));
            }
            engine
                .instances()
                .invoke(
                    &alice(),
                    "Note",
                    "n1",
                    "stamp",
                    &json!({}),
                    WriteOptions::default(),
                )
                .unwrap();
            holder.join().unwrap();
        });
        assert_eq!(*ORDER.lock().unwrap(), ["hold", "released", "stamp"]);
    }
}
