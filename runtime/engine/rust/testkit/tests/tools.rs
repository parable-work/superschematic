//! The describe document, the tools document and the calls the tools make
//! (D16, D58), the port of `test/tools.test.ts`'s cases that need no core
//! behavior: names and MCP handles, hidden tools and why, invocation
//! policies (the default, configured ones, a behavior operation's and a
//! distribution's), the options' checks, what each tool call does and
//! refuses, that no tool publishes, and the behavior catalog; on SQLite
//! and, with the variable set, on Postgres. The core's behaviors' tools
//! (Workflow, Comments, Revisions, Links, Constants, Variants) are
//! milestones 8 to 12.

mod support;

use serde_json::{json, Value};

use superschematic_engine::tools::{InvocationPolicy, ToolOptions};
use superschematic_engine::{
    AccessRequest, Behavior, BehaviorSet, ConfigTarget, CreateOptions, EngineErrorCode,
    EngineErrorKind, Error, Hooks, NamespaceOptions, Policy, Principal, Result, SchemaTarget,
    ToolFilter, ToolSummary, ToolTarget,
};
use superschematic_engine_testkit::behaviors::{counter_declaration, Counter, Holder};
use support::{
    alice, code, fixtures, publish, publish_item, refusal, schema_document, targets, usage, Target,
};

fn reader() -> Principal {
    Principal::new("reader", ["read"])
}

// alice may do everything; reader may read; a principal holding an
// action's name may do it.
fn policy() -> Policy {
    Policy::custom(support::RecordingPolicy::new(
        |request: &AccessRequest<'_>| {
            let principal = request.principal();
            principal.subject == "alice"
                || principal
                    .permissions
                    .iter()
                    .any(|permission| permission == request.action())
        },
    ))
}

fn order_document() -> Value {
    schema_document(
        "Order",
        json!([
            {"name": "title", "typeRef": {"name": "string"}, "required": true},
            {"name": "quantity", "typeRef": {"name": "Generic.Int64"}},
            {"name": "status", "typeRef": {"name": "OrderStatus"}},
            {"name": "lines", "typeRef": {"name": "OrderLine", "isArray": true}}
        ]),
        json!({
            "enums": {"OrderStatus": {"name": "OrderStatus", "values": [{"name": "OPEN", "serializedAs": "open"}, {"name": "SHIPPED", "serializedAs": "shipped"}]}},
            "types": {"OrderLine": {"name": "OrderLine", "role": "EmbeddedStruct", "fields": [
                {"name": "sku", "typeRef": {"name": "string"}, "required": true, "validateMaxLength": 12},
                {"name": "count", "typeRef": {"name": "number"}, "validateMin": 1}
            ]}}
        }),
    )
}

fn describe(engine: &superschematic_engine::Engine, schema: &str) -> Value {
    engine
        .tools()
        .describe(&alice(), schema, ToolTarget::default())
        .expect("describe")
}

fn operation(engine: &superschematic_engine::Engine, schema: &str, name: &str) -> Value {
    describe(engine, schema)["operations"]
        .as_array()
        .unwrap()
        .iter()
        .find(|operation| operation["name"] == name)
        .unwrap_or_else(|| panic!("no operation {name}"))
        .clone()
}

fn manifest(engine: &superschematic_engine::Engine, principal: &Principal) -> Vec<Value> {
    engine
        .tools()
        .manifest(principal, ToolTarget::default())
        .expect("manifest")["tools"]
        .as_array()
        .unwrap()
        .clone()
}

fn tool(engine: &superschematic_engine::Engine, name: &str, principal: &Principal) -> Value {
    manifest(engine, principal)
        .into_iter()
        .find(|tool| tool["name"] == name)
        .unwrap_or_else(|| panic!("no tool {name}"))
}

fn keys(value: &Value) -> Vec<String> {
    value
        .as_object()
        .map(|members| members.keys().cloned().collect())
        .unwrap_or_default()
}

fn call(
    engine: &superschematic_engine::Engine,
    principal: &Principal,
    handle: &str,
    args: Value,
) -> Result<Value> {
    engine
        .tools()
        .call(principal, handle, &args, ToolTarget::default())
}

fn unknown_tool(result: Result<Value>) -> bool {
    matches!(result, Err(Error::Engine(ref error)) if matches!(error.kind(), EngineErrorKind::UnknownTool { .. }))
}

fn open(target: &Target, behaviors: BehaviorSet) -> superschematic_engine::Engine {
    target.open_with(behaviors, |options| options.policy = Some(policy()))
}

#[test]
fn describes_a_schema_without_behaviors_its_instance_its_five_operations_and_their_tools() {
    for target in targets("describes_a_schema_without_behaviors") {
        let engine = open(&target, BehaviorSet::new());
        publish(&engine, &order_document(), None);
        let described = describe(&engine, "Order");
        assert_eq!(
            json!([
                described["namespace"],
                described["name"],
                described["schemaNamespace"],
                described["version"],
                described["instanceType"],
                described["behaviors"]
            ]),
            json!(["default", "Order", "default", 1, "Order", []])
        );
        assert_eq!(
            described["hash"],
            json!(
                engine
                    .schemas()
                    .live(&alice(), "Order", SchemaTarget::default())
                    .unwrap()
                    .unwrap()
                    .hash
            )
        );
        assert_eq!(described["instance"]["required"], json!(["title"]));
        assert_eq!(described["instance"]["additionalProperties"], json!(false));
        assert_eq!(
            keys(&described["instance"]["properties"]),
            vec!["lines", "quantity", "status", "title"]
        );
        assert_eq!(
            described["instance"]["properties"]["status"],
            json!({"description": "A OrderStatus value", "enum": ["open", "shipped", null], "type": ["string", "null"]})
        );
        let operations: Vec<Value> = described["operations"]
            .as_array()
            .unwrap()
            .iter()
            .map(|operation| {
                json!([
                    operation["name"],
                    operation["writes"],
                    operation["invocationPolicy"],
                    operation["tool"],
                    operation.get("behavior")
                ])
            })
            .collect();
        assert_eq!(
            operations,
            vec![
                json!(["create", true, "auto", "order.create", null]),
                json!(["get", false, "auto", "order.get", null]),
                json!(["list", false, "auto", "order.list", null]),
                json!(["update", true, "auto", "order.update", null]),
                json!(["delete", true, "auto", "order.delete", null]),
            ]
        );
        let params: Vec<Value> = described["operations"]
            .as_array()
            .unwrap()
            .iter()
            .map(|operation| {
                json!([
                    operation["name"],
                    keys(&operation["params"]["properties"]),
                    operation["params"]
                        .get("required")
                        .cloned()
                        .unwrap_or(json!([]))
                ])
            })
            .collect();
        assert_eq!(
            params,
            vec![
                json!(["create", ["data", "id"], ["data"]]),
                json!(["get", ["id", "valueRefs"], ["id"]]),
                json!(["list", ["cursor", "limit", "valueRefs", "where"], []]),
                json!(["update", ["expectedSeq", "id", "patch"], ["id", "patch"]]),
                json!(["delete", ["expectedSeq", "id"], ["id"]]),
            ]
        );
        let filter = &operation(&engine, "Order", "list")["params"]["properties"]["where"];
        assert_eq!(
            keys(&filter["properties"]),
            vec!["title", "quantity", "status"]
        );
        assert_eq!(
            filter["properties"]["status"]["anyOf"][0],
            json!({"description": "A OrderStatus value", "enum": ["open", "shipped", null], "type": ["string", "null"]})
        );
        assert_eq!(
            filter["properties"]["status"]["anyOf"][1],
            json!({"type": "array", "items": filter["properties"]["status"]["anyOf"][0], "minItems": 1, "maxItems": 100})
        );
        let patch = &operation(&engine, "Order", "update")["params"]["properties"]["patch"];
        assert_eq!(patch.get("required"), None);
        assert_eq!(
            patch["properties"]["lines"]["items"]["required"][0],
            json!("sku"),
            "a list is replaced whole, so its items keep what they require"
        );
        let result = &operation(&engine, "Order", "get")["result"];
        assert_eq!(result["properties"]["data"], described["instance"]);
        assert_eq!(
            result["properties"]["behaviors"],
            described["instanceBehaviors"]
        );
        assert_eq!(
            json!([
                described["instanceBehaviors"]["properties"],
                described["instanceBehaviors"]["required"]
            ]),
            json!([{}, []])
        );
        assert_eq!(
            operation(&engine, "Order", "delete")["result"],
            json!({"type": "null"})
        );
    }
}

#[test]
fn describes_a_schema_with_behaviors_their_fields_their_config_and_their_operations() {
    for target in targets("describes_a_schema_with_behaviors") {
        let engine = open(&target, fixtures());
        publish_item(
            &engine,
            json!([{"name": "test.Counter", "config": {"start": 2, "limit": 9}}, {"name": "test.Flag"}]),
        );
        let described = describe(&engine, "Item");
        let counter: Value = serde_json::from_str(&counter_declaration()).unwrap();
        assert_eq!(
            described["behaviors"],
            json!([
                {"name": "test.Counter", "description": counter["description"], "config": {"start": 2, "limit": 9}, "fields": [{"name": "count", "description": "The count."}], "operations": ["increment", "history"], "vetoes": []},
                {"name": "test.Flag", "description": "Holds an instance still while it is flagged.", "config": {}, "fields": [
                    {"name": "flagged", "description": "Whether the instance is flagged."},
                    {"name": "flagReason", "description": "Why; absent when it is not flagged."}
                ], "operations": ["flag", "unflag"], "vetoes": []}
            ])
        );
        assert_eq!(keys(&described["instance"]["properties"]), vec!["title"]);
        assert_eq!(
            described["instanceBehaviors"],
            json!({
                "type": "object",
                "description": "The instance's behaviors' fields, by behavior name, apart from its own fields",
                "additionalProperties": false,
                "properties": {
                    "test.Counter": {"type": "object", "description": "The fields of behavior test.Counter", "additionalProperties": false, "readOnly": true,
                        "properties": {"count": {"description": "The count.", "readOnly": true}}},
                    "test.Flag": {"type": "object", "description": "The fields of behavior test.Flag", "additionalProperties": false, "readOnly": true,
                        "properties": {
                            "flagged": {"description": "Whether the instance is flagged.", "readOnly": true},
                            "flagReason": {"description": "Why; absent when it is not flagged.", "readOnly": true}
                        }}
                },
                "required": ["test.Counter", "test.Flag"]
            })
        );
        let behaviors: Vec<Value> = described["operations"].as_array().unwrap()[5..]
            .iter()
            .map(|operation| {
                json!([
                    operation["name"],
                    operation["behavior"],
                    operation["writes"],
                    operation["invocationPolicy"],
                    operation["tool"]
                ])
            })
            .collect();
        assert_eq!(
            behaviors,
            vec![
                json!(["increment", "test.Counter", true, "auto", "item.increment"]),
                json!(["history", "test.Counter", false, "auto", "item.history"]),
                json!(["flag", "test.Flag", true, "ask", "item.flag"]),
                json!(["unflag", "test.Flag", true, "auto", "item.unflag"]),
            ]
        );
        let increment = operation(&engine, "Item", "increment");
        assert_eq!(
            increment["params"]["properties"]["params"],
            counter["operations"][0]["paramsSchema"]
        );
        assert_eq!(increment["params"]["required"], json!(["id"]));
        assert_eq!(
            increment["result"],
            counter["operations"][0]["resultSchema"]
        );
        assert_eq!(
            operation(&engine, "Item", "flag")["params"]["required"],
            json!(["id", "params"])
        );
    }
}

#[test]
fn a_schema_level_operation_takes_params_and_no_id_and_says_where_it_is_served() {
    for target in targets("a_schema_level_operation_describes") {
        let engine = open(&target, fixtures().with(Holder));
        publish_item(&engine, json!([{"name": "test.Holder"}]));
        let holders = operation(&engine, "Item", "holders");
        assert_eq!(
            json!([
                holders["behavior"],
                holders["scope"],
                holders["writes"],
                holders["tool"]
            ]),
            json!(["test.Holder", "schema", false, "item.holders"])
        );
        assert_eq!(keys(&holders["params"]["properties"]), vec!["params"]);
        assert_eq!(holders["params"]["required"], json!(["params"]));
        assert_eq!(
            operation(&engine, "Item", "hold")["scope"],
            json!("instance")
        );
        assert_eq!(operation(&engine, "Item", "create").get("scope"), None);
        assert_eq!(
            operation(&engine, "Item", "scribble")["params"].get("required"),
            None
        );
        let read = tool(&engine, "item.holders", &alice());
        assert_eq!(
            json!([
                read["httpMethod"],
                read["httpPath"],
                read["mcp"]["handle"],
                read["replay"]["mode"],
                keys(&read["parameters"]["properties"])
            ]),
            json!([
                "POST",
                "/namespaces/default/schemas/Item/operations/holders",
                "item_holders",
                "read_only",
                ["params"]
            ])
        );
        let write = tool(&engine, "item.releaseAll", &alice());
        assert_eq!(
            json!([write["httpPath"], write["replay"], write["mcp"]["handle"]]),
            json!([
                "/namespaces/default/schemas/Item/operations/releaseAll",
                null,
                "item_release_all"
            ])
        );
        assert_eq!(
            tool(&engine, "item.hold", &alice())["httpPath"],
            json!("/namespaces/default/schemas/Item/instances/{id}/operations/hold")
        );
        assert_eq!(
            tool(&engine, "item.releaseAll", &reader())["mcp"],
            json!({"hidden": true, "hiddenReason": "the access policy refuses reader write on Item (releaseAll)", "hiddenBy": ["policy"]})
        );
    }
}

#[test]
fn describes_a_schema_the_namespace_reaches_in_the_shared_one_and_refuses_what_it_cannot() {
    for target in targets("describes_a_shared_schema") {
        let engine = target.open_with(BehaviorSet::new(), |options| {
            options.policy = Some(policy());
            options.namespaces = NamespaceOptions {
                names: vec!["east".into(), "common".into()],
                shared: Some("common".into()),
            };
        });
        publish(
            &engine,
            &schema_document(
                "Note",
                json!([{"name": "body", "typeRef": {"name": "string"}, "required": true}]),
                json!({}),
            ),
            Some("common"),
        );
        let east = ToolTarget {
            namespace: Some("east"),
            filter: None,
        };
        let note = engine.tools().describe(&alice(), "Note", east).unwrap();
        assert_eq!(
            json!([note["namespace"], note["schemaNamespace"]]),
            json!(["east", "common"])
        );
        assert_eq!(note["operations"][0]["tool"], json!("note.create"));
        engine
            .schemas()
            .define(&alice(), order_document(), Default::default())
            .unwrap();
        assert_eq!(
            code(
                engine
                    .tools()
                    .describe(&alice(), "Order", ToolTarget::default())
            ),
            EngineErrorCode::NotFound
        );
        assert_eq!(
            code(engine.tools().describe(
                &Principal::new("nobody", Vec::<String>::new()),
                "Note",
                east
            )),
            EngineErrorCode::Forbidden
        );
        assert_eq!(
            code(engine.tools().describe(
                &alice(),
                "Note",
                ToolTarget {
                    namespace: Some("nowhere"),
                    filter: None
                }
            )),
            EngineErrorCode::UnknownNamespace
        );
    }
}

#[test]
fn the_tools_document_names_each_tool_as_the_sdk_generators_do_and_no_tool_publishes() {
    for target in targets("the_tools_document_names_each_tool") {
        let engine = open(&target, fixtures());
        publish_item(&engine, json!([{"name": "test.Counter"}]));
        publish(
            &engine,
            &schema_document(
                "LineItem",
                json!([{"name": "sku", "typeRef": {"name": "string"}}]),
                json!({}),
            ),
            None,
        );
        let document = engine
            .tools()
            .manifest(&alice(), ToolTarget::default())
            .unwrap();
        assert_eq!(
            json!([document["$schema"], document["title"]]),
            json!([
                "https://json-schema.org/draft/2020-12/schema",
                "Namespace default Tool Definitions"
            ])
        );
        let listed: Vec<Value> = document["tools"]
            .as_array()
            .unwrap()
            .iter()
            .map(|entry| {
                json!([
                    entry["name"],
                    entry["mcp"]["handle"],
                    entry["namespace"],
                    entry["methodName"]
                ])
            })
            .collect();
        assert_eq!(
            listed,
            vec![
                json!([
                    "engine.listSchemas",
                    "list_schemas",
                    "engine",
                    "listSchemas"
                ]),
                json!([
                    "engine.describeSchema",
                    "describe_schema",
                    "engine",
                    "describeSchema"
                ]),
                json!([
                    "engine.defineSchema",
                    "define_schema",
                    "engine",
                    "defineSchema"
                ]),
                json!([
                    "engine.listBehaviors",
                    "list_behaviors",
                    "engine",
                    "listBehaviors"
                ]),
                json!([
                    "engine.describeBehavior",
                    "describe_behavior",
                    "engine",
                    "describeBehavior"
                ]),
                json!(["engine.getValue", "get_value", "engine", "getValue"]),
                json!([
                    "engine.listNamespaces",
                    "list_namespaces",
                    "engine",
                    "listNamespaces"
                ]),
                json!([
                    "engine.createNamespace",
                    "create_namespace",
                    "engine",
                    "createNamespace"
                ]),
                json!([
                    "engine.archiveNamespace",
                    "archive_namespace",
                    "engine",
                    "archiveNamespace"
                ]),
                json!([
                    "engine.unarchiveNamespace",
                    "unarchive_namespace",
                    "engine",
                    "unarchiveNamespace"
                ]),
                json!(["item.create", "item_create", "item", "create"]),
                json!(["item.get", "item_get", "item", "get"]),
                json!(["item.list", "item_list", "item", "list"]),
                json!(["item.update", "item_update", "item", "update"]),
                json!(["item.delete", "item_delete", "item", "delete"]),
                json!(["item.increment", "item_increment", "item", "increment"]),
                json!(["item.history", "item_history", "item", "history"]),
                json!([
                    "line-item.create",
                    "line_item_create",
                    "line-item",
                    "create"
                ]),
                json!(["line-item.get", "line_item_get", "line-item", "get"]),
                json!(["line-item.list", "line_item_list", "line-item", "list"]),
                json!([
                    "line-item.update",
                    "line_item_update",
                    "line-item",
                    "update"
                ]),
                json!([
                    "line-item.delete",
                    "line_item_delete",
                    "line-item",
                    "delete"
                ]),
            ]
        );
        for entry in document["tools"].as_array().unwrap() {
            let text = format!(
                "{} {} {}",
                entry["name"], entry["mcp"]["handle"], entry["httpPath"]
            )
            .to_lowercase();
            assert!(
                !text.contains("publish"),
                "{} does not publish",
                entry["name"]
            );
        }
        assert!(unknown_tool(call(
            &engine,
            &alice(),
            "publish_schema",
            json!({"name": "Item"})
        )));
        let increment = tool(&engine, "item.increment", &alice());
        assert_eq!(
            increment["mcp"],
            json!({"hidden": false, "name": "Item: increment", "handle": "item_increment", "description": "Adds to the count.", "invocationPolicy": "auto",
                "_meta": {"superschematic/operation-guidance": {"useWhen": "", "doNotUseWhen": "", "success": "", "errors": []}}})
        );
        assert_eq!(
            json!([
                increment["httpMethod"],
                increment["httpPath"],
                increment["requiresAuth"],
                increment["replay"],
                increment["bindingStatus"]
            ]),
            json!([
                "POST",
                "/namespaces/default/schemas/Item/instances/{id}/operations/increment",
                true,
                null,
                "ready"
            ])
        );
        assert_eq!(
            increment["returns"],
            json!({"type": "object", "description": "The result of increment"})
        );
        let history = tool(&engine, "item.history", &alice());
        assert_eq!(
            history["replay"],
            json!({"mode": "read_only", "idempotencyKeyPointers": [], "expectedRevisionPointers": []})
        );
        assert_eq!(
            history["returns"],
            json!({"type": "array", "description": "The result of history", "items": {"type": "integer"}})
        );
        let digest = increment["inputSchemaDigest"].as_str().unwrap();
        assert!(digest.starts_with("sha256:") && digest.len() == 71);
        assert_ne!(increment["inputSchemaDigest"], history["inputSchemaDigest"]);
    }
}

// test.Lister: an operation whose handle is an engine tool's.
struct Lister;

impl Behavior for Lister {
    type Config = Value;

    fn declaration(&self) -> &str {
        r#"{"name": "test.Lister", "operations": [{"name": "schemas", "paramsSchema": {"type": "object", "additionalProperties": false}, "resultSchema": true}]}"#
    }

    fn parse_config(&self, raw: &Value, _target: &ConfigTarget<'_>) -> Result<Value> {
        Ok(raw.clone())
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            operations: vec![("schemas", |_, _, _| Ok(json!([])))],
            ..Hooks::default()
        }
    }
}

#[test]
fn hides_a_tool_whose_handle_mcp_refuses_or_another_tool_derives_too_with_the_reason() {
    for target in targets("hides_a_tool_whose_handle") {
        let engine = target.open_with(BehaviorSet::new().with(Lister), |_| {});
        let sku = json!([{"name": "sku", "typeRef": {"name": "string"}}]);
        let mut listed = schema_document(
            "List",
            json!([{"name": "title", "typeRef": {"name": "string"}}]),
            json!({}),
        );
        listed["types"]["List"]["behaviors"] = json!([{"name": "test.Lister"}]);
        for document in [
            schema_document("LineItem", sku.clone(), json!({})),
            schema_document("Line_Item", sku.clone(), json!({})),
            schema_document("Tail-", sku.clone(), json!({})),
            schema_document(&format!("A{}", "b".repeat(45)), sku.clone(), json!({})),
            listed,
        ] {
            publish(&engine, &document, None);
        }
        let hidden = |name: &str| {
            let entry = tool(&engine, name, &alice());
            assert_eq!(entry["mcp"]["hidden"], json!(true), "{name}");
            assert_eq!(entry["mcp"]["hiddenBy"], json!(["handle"]), "{name}");
            entry["mcp"]["hiddenReason"].as_str().unwrap().to_owned()
        };
        assert_eq!(
            hidden("line-item.create"),
            "its MCP handle line_item_create is also the handle of line_item.create"
        );
        assert_eq!(
            hidden("line_item.create"),
            "its MCP handle line_item_create is also the handle of line-item.create"
        );
        assert_eq!(
            hidden("tail-.get"),
            "its MCP handle tail__get is not lowercase snake case starting with a letter"
        );
        let long = format!("a{}", "b".repeat(45));
        assert_eq!(
            hidden(&format!("{long}.delete")),
            format!("its MCP handle {long}_delete is longer than 48 characters")
        );
        assert_eq!(
            hidden("list.schemas"),
            "its MCP handle list_schemas is the handle of the engine's tool engine.listSchemas"
        );
        assert_eq!(
            tool(&engine, "list.create", &alice())["mcp"]["hidden"],
            json!(false)
        );
        assert_eq!(
            call(&engine, &alice(), "list_schemas", json!({})).unwrap(),
            serde_json::to_value(
                engine
                    .schemas()
                    .list(&alice(), SchemaTarget::default())
                    .unwrap()
            )
            .unwrap()
        );
        assert!(unknown_tool(call(
            &engine,
            &alice(),
            "line_item_create",
            json!({"data": {}})
        )));
        assert!(unknown_tool(call(
            &engine,
            &alice(),
            "tail__get",
            json!({"id": "x"})
        )));
    }
}

#[test]
fn hides_a_tool_the_policy_refuses_and_lists_no_tool_of_a_schema_the_caller_may_not_read() {
    for target in targets("hides_a_tool_the_policy_refuses") {
        let engine = target.open_with(fixtures(), |options| {
            options.policy = Some(Policy::custom(support::RecordingPolicy::new(
                |request: &AccessRequest<'_>| {
                    let principal = request.principal();
                    (principal.subject == "alice"
                        || principal
                            .permissions
                            .iter()
                            .any(|permission| permission == request.action()))
                        && !(principal.subject == "reader" && request.schema() == Some("Secret"))
                },
            )));
        });
        publish_item(&engine, json!([{"name": "test.Counter"}]));
        publish(
            &engine,
            &schema_document(
                "Secret",
                json!([{"name": "code", "typeRef": {"name": "string"}}]),
                json!({}),
            ),
            None,
        );
        let tools = manifest(&engine, &reader());
        let visible: Vec<&Value> = tools
            .iter()
            .filter(|entry| entry["mcp"]["hidden"] == json!(false))
            .map(|entry| &entry["name"])
            .collect();
        assert_eq!(
            visible,
            vec![
                "engine.listSchemas",
                "engine.describeSchema",
                "engine.listBehaviors",
                "engine.describeBehavior",
                "engine.getValue",
                "item.get",
                "item.list",
                "item.history"
            ]
        );
        let reason = |name: &str| tool(&engine, name, &reader())["mcp"]["hiddenReason"].clone();
        assert_eq!(
            reason("item.increment"),
            json!("the access policy refuses reader write on Item (increment)")
        );
        assert_eq!(
            reason("engine.defineSchema"),
            json!("the access policy refuses reader define in namespace default")
        );
        assert_eq!(
            reason("engine.archiveNamespace"),
            json!("the access policy refuses reader manage (archive) of namespaces")
        );
        assert_eq!(
            code(call(
                &engine,
                &reader(),
                "define_schema",
                json!({"document": schema_document("Other", json!([]), json!({}))})
            )),
            EngineErrorCode::Forbidden
        );
        assert_eq!(
            reason("item.create"),
            json!("the access policy refuses reader write on Item")
        );
        assert!(!tools
            .iter()
            .any(|entry| entry["name"].as_str().unwrap().starts_with("secret.")));
        tool(&engine, "secret.create", &alice());
    }
}

// An agent's session: Item's operations alone, and nothing that writes but
// increment.
struct AgentFilter {
    seen: std::sync::Mutex<Vec<ToolSummary>>,
}

impl ToolFilter for AgentFilter {
    fn keeps(&self, principal: &Principal, tool: &ToolSummary, _namespace: &str) -> bool {
        self.seen.lock().unwrap().push(tool.clone());
        principal.subject != "agent"
            || (tool.schema.as_deref() == Some("Item")
                && (!tool.writes || tool.operation == "increment"))
    }
}

#[test]
fn a_mounts_filter_narrows_a_callers_tools_and_its_call_is_a_tool_the_namespace_does_not_have() {
    for target in targets("a_mounts_filter_narrows") {
        let engine = open(&target, fixtures());
        publish_item(&engine, json!([{"name": "test.Counter"}]));
        publish(
            &engine,
            &schema_document(
                "Other",
                json!([{"name": "code", "typeRef": {"name": "string"}}]),
                json!({}),
            ),
            None,
        );
        let filter = AgentFilter {
            seen: std::sync::Mutex::new(Vec::new()),
        };
        let agent = Principal::new("agent", ["read", "write"]);
        let through = ToolTarget {
            namespace: None,
            filter: Some(&filter),
        };
        let tools = engine.tools().manifest(&agent, through).unwrap()["tools"]
            .as_array()
            .unwrap()
            .clone();
        let visible: Vec<&Value> = tools
            .iter()
            .filter(|entry| entry["mcp"]["hidden"] == json!(false))
            .map(|entry| &entry["name"])
            .collect();
        assert_eq!(
            visible,
            vec!["item.get", "item.list", "item.increment", "item.history"]
        );
        assert_eq!(
            tools
                .iter()
                .find(|entry| entry["name"] == "other.get")
                .unwrap()["mcp"],
            json!({
                "hidden": true,
                "hiddenReason": "this mount's tool filter leaves it out of agent's tools",
                "hiddenBy": ["filter"]
            })
        );
        let seen = filter.seen.lock().unwrap().clone();
        assert_eq!(
            seen.iter()
                .find(|summary| summary.name == "item.increment")
                .cloned(),
            Some(ToolSummary {
                handle: "item_increment".into(),
                name: "item.increment".into(),
                schema: Some("Item".into()),
                operation: "increment".into(),
                behavior: Some("test.Counter".into()),
                writes: true
            })
        );
        engine
            .instances()
            .create(
                &alice(),
                "Item",
                &json!({"title": "Lamp"}),
                CreateOptions {
                    id: Some("i1"),
                    ..Default::default()
                },
            )
            .unwrap();
        assert_eq!(
            engine
                .tools()
                .call(&agent, "item_increment", &json!({"id": "i1"}), through)
                .unwrap(),
            json!({"count": 1})
        );
        assert!(unknown_tool(engine.tools().call(
            &agent,
            "item_create",
            &json!({"data": {"title": "Desk"}}),
            through
        )));
        assert!(unknown_tool(engine.tools().call(
            &agent,
            "list_schemas",
            &json!({}),
            through
        )));
        assert!(engine.tools().manifest(&alice(), through).unwrap()["tools"]
            .as_array()
            .unwrap()
            .iter()
            .all(|entry| entry["mcp"]["hidden"] == json!(false)));
        assert_eq!(
            call(&engine, &agent, "list_schemas", json!({}))
                .unwrap()
                .as_array()
                .unwrap()
                .len(),
            2
        );
    }
}

#[test]
fn invocation_policies_are_the_default_unless_configured_and_a_behavior_operations_its_declarations(
) {
    for target in targets("invocation_policies") {
        let engine = target.open_with(fixtures(), |options| {
            options.tools = ToolOptions {
                invocation: vec![
                    ("delete".into(), "ask".into()),
                    ("defineSchema".into(), "ask".into()),
                ],
                ..ToolOptions::default()
            };
        });
        publish_item(
            &engine,
            json!([{"name": "test.Counter"}, {"name": "test.Flag"}]),
        );
        let policies: Vec<Value> = manifest(&engine, &alice())
            .iter()
            .map(|entry| json!([entry["name"], entry["mcp"]["invocationPolicy"]]))
            .collect();
        let ask = ["engine.defineSchema", "item.delete", "item.flag"];
        for entry in &policies {
            let want = if ask.contains(&entry[0].as_str().unwrap()) {
                "ask"
            } else {
                "auto"
            };
            assert_eq!(entry[1], json!(want), "{}", entry[0]);
        }
        assert_eq!(policies.len(), 19);
        assert_eq!(
            operation(&engine, "Item", "delete")["invocationPolicy"],
            json!("ask")
        );
        assert_eq!(
            operation(&engine, "Item", "flag")["invocationPolicy"],
            json!("ask")
        );

        // A distribution's policy: its key everywhere, its default, its
        // values; test.Flag's own declaration ("ask") does not register
        // under it.
        let review = InvocationPolicy {
            key: "review".into(),
            values: vec!["never".into(), "on-write".into(), "always".into()],
            default: "on-write".into(),
        };
        let refused = Engine::open({
            let mut options = target.options(fixtures());
            options.tools = ToolOptions {
                invocation_policy: Some(review.clone()),
                ..ToolOptions::default()
            };
            options
        });
        assert!(usage(refused).contains("operation flag invocationPolicy \"ask\" is not one of the review values (never, on-write, always)"));
        drop(engine);
        let reviewed = target.open_with(BehaviorSet::new().with(Counter::default()), |options| {
            options.tools = ToolOptions {
                invocation_policy: Some(review),
                invocation: vec![
                    ("get".into(), "never".into()),
                    ("list".into(), "never".into()),
                ],
                ..ToolOptions::default()
            };
        });
        publish_item(&reviewed, json!([{"name": "test.Counter"}]));
        let increment = tool(&reviewed, "item.increment", &alice());
        assert_eq!(
            keys(&increment["mcp"]),
            vec!["hidden", "name", "handle", "description", "review", "_meta"]
        );
        assert_eq!(increment["mcp"]["review"], json!("on-write"));
        assert_eq!(
            tool(&reviewed, "item.get", &alice())["mcp"]["review"],
            json!("never")
        );
        let described = operation(&reviewed, "Item", "increment");
        assert_eq!(
            json!([described["review"], described.get("invocationPolicy")]),
            json!(["on-write", null])
        );
    }
}

use superschematic_engine::Engine;

// A deployment marks a behavior's operation through the tool options
// (D70): `<Behavior>.<operation>` takes the place of the declaration's
// policy in the tool, the describe document and the behavior's own
// document, and an engine refuses one that names a behavior it does not
// run, or an operation the behavior does not declare.
#[test]
fn a_behavior_operation_takes_the_invocation_policy_the_tool_options_give_it() {
    for target in targets("operation_invocation") {
        let engine = target.open_with(fixtures(), |options| {
            options.tools = ToolOptions {
                invocation: vec![
                    ("test.Counter.increment".into(), "ask".into()),
                    ("test.Flag.flag".into(), "auto".into()),
                ],
                ..ToolOptions::default()
            };
        });
        publish_item(
            &engine,
            json!([{"name": "test.Counter"}, {"name": "test.Flag"}]),
        );
        assert_eq!(
            tool(&engine, "item.increment", &alice())["mcp"]["invocationPolicy"],
            json!("ask")
        );
        assert_eq!(
            tool(&engine, "item.history", &alice())["mcp"]["invocationPolicy"],
            json!("auto")
        );
        assert_eq!(
            tool(&engine, "item.flag", &alice())["mcp"]["invocationPolicy"],
            json!("auto")
        );
        assert_eq!(
            operation(&engine, "Item", "increment")["invocationPolicy"],
            json!("ask")
        );
        let described = engine
            .tools()
            .describe_behavior(&alice(), "test.Counter")
            .unwrap();
        let increment = described["operations"]
            .as_array()
            .unwrap()
            .iter()
            .find(|operation| operation["name"] == "increment")
            .unwrap();
        assert_eq!(increment["invocationPolicy"], json!("ask"));
        drop(engine);

        let refused = Engine::open({
            let mut options = target.options(fixtures());
            options.tools = ToolOptions {
                invocation: vec![
                    ("Payments.capturePayment".into(), "ask".into()),
                    ("test.Counter.decrement".into(), "ask".into()),
                ],
                ..ToolOptions::default()
            };
            options
        });
        let message = usage(refused);
        assert!(
            message.contains(
                "invocation names Payments.capturePayment, and this engine runs no behavior Payments"
            ),
            "{message}"
        );
        assert!(
            message.contains(
                "invocation names test.Counter.decrement, which is no operation of test.Counter"
            ),
            "{message}"
        );
    }
}

// A behavior operation the tool options hide is no agent's tool: the
// tools document reads it hidden with its reason, and a call by its handle
// is a tool the namespace does not have, while the operation itself still
// runs through the API. An engine refuses to hide an operation it does not
// run.
#[test]
fn a_behavior_operation_the_tool_options_hide_is_offered_to_no_agent() {
    for target in targets("operation_hidden") {
        let engine = target.open_with(fixtures(), |options| {
            options.tools = ToolOptions {
                hidden: vec!["test.Counter.increment".into()],
                ..ToolOptions::default()
            };
        });
        publish_item(&engine, json!([{"name": "test.Counter"}]));
        let hidden = tool(&engine, "item.increment", &alice());
        assert_eq!(hidden["mcp"]["hidden"], json!(true));
        assert!(hidden["mcp"]["hiddenReason"]
            .as_str()
            .unwrap()
            .contains("tools.hidden"));
        assert_eq!(
            tool(&engine, "item.history", &alice())["mcp"]["hidden"],
            json!(false)
        );
        engine
            .instances()
            .create(
                &alice(),
                "Item",
                &json!({"title": "Lamp"}),
                CreateOptions {
                    id: Some("i1"),
                    ..Default::default()
                },
            )
            .unwrap();
        assert!(unknown_tool(call(
            &engine,
            &alice(),
            "item_increment",
            json!({"id": "i1"})
        )));
        engine
            .instances()
            .invoke(
                &alice(),
                "Item",
                "i1",
                "increment",
                &json!({}),
                superschematic_engine::WriteOptions::default(),
            )
            .expect("the API still runs it");
        drop(engine);

        let refused = Engine::open({
            let mut options = target.options(fixtures());
            options.tools = ToolOptions {
                hidden: vec![
                    "Payments.recordPaymentEvent".into(),
                    "test.Counter.decrement".into(),
                ],
                ..ToolOptions::default()
            };
            options
        });
        let message = usage(refused);
        assert!(
            message.contains(
                "hidden names Payments.recordPaymentEvent, and this engine runs no behavior Payments"
            ),
            "{message}"
        );
        assert!(
            message.contains(
                "hidden names test.Counter.decrement, which is no operation of test.Counter"
            ),
            "{message}"
        );
    }
}

#[test]
fn the_tool_options_are_checked_as_the_compilers_registry_checks_them() {
    let refusal_of = |options: ToolOptions| match options.check() {
        Err(Error::Usage(message)) => message,
        other => panic!("expected a refusal, got {other:?}"),
    };
    let policy = |key: &str, values: &[&str], default: &str| ToolOptions {
        invocation_policy: Some(InvocationPolicy {
            key: key.into(),
            values: values.iter().map(|value| (*value).to_owned()).collect(),
            default: default.into(),
        }),
        ..ToolOptions::default()
    };
    assert!(refusal_of(policy("handle", &["auto"], "auto"))
        .contains("key \"handle\" is a key @mcp already uses"));
    assert!(
        refusal_of(policy("9lives", &["auto"], "auto")).contains("must be a letter followed by")
    );
    assert!(refusal_of(policy("confirm", &[], "auto")).contains("lists no values"));
    let several = refusal_of(policy("confirm", &["Never", "never", "never"], "always"));
    assert!(
        several.contains("\"Never\" must be lowercase")
            && several.contains("never is listed twice")
            && several.contains("default \"always\" is not one of")
    );
    assert!(refusal_of(ToolOptions {
        invocation: vec![("delete".into(), "maybe".into())],
        ..ToolOptions::default()
    })
    .contains("the invocation policy of delete, \"maybe\", is not one of auto, ask"));
    assert!(refusal_of(ToolOptions {
        invocation: vec![("publish".into(), "ask".into())],
        ..ToolOptions::default()
    })
    .contains("invocation names publish, which is not one of create"));
    assert!(refusal_of(ToolOptions {
        parameter_keys: Some(vec![("type".into(), json!(1))]),
        ..ToolOptions::default()
    })
    .contains("\"type\" is written by the core"));
    assert!(refusal_of(ToolOptions {
        scalar_key: Some("x-s".into()),
        parameter_keys: Some(vec![("x-s".into(), json!(1))]),
        ..ToolOptions::default()
    })
    .contains("\"x-s\" is the scalar key"));
    assert!(refusal_of(ToolOptions {
        parameter_keys: Some(vec![("x-a".into(), json!(1)), ("x-a".into(), json!(2))]),
        ..ToolOptions::default()
    })
    .contains("\"x-a\" is listed twice"));
}

#[test]
fn tool_calls_run_the_operation_their_handle_names_through_the_engine() {
    for target in targets("tool_calls_run") {
        let engine = open(&target, fixtures());
        publish_item(&engine, json!([{"name": "test.Counter"}]));
        let created = call(
            &engine,
            &alice(),
            "item_create",
            json!({"id": "i1", "data": {"title": "Desk"}}),
        )
        .unwrap();
        assert_eq!(
            json!([
                created["id"],
                created["seq"],
                created["data"],
                created["behaviors"]
            ]),
            json!(["i1", 1, {"title": "Desk"}, {"test.Counter": {"count": 0}}])
        );
        assert_eq!(
            call(
                &engine,
                &alice(),
                "item_create",
                json!({"data": {"title": "Lamp"}})
            )
            .unwrap()["id"]
                .as_str()
                .unwrap()
                .len(),
            36
        );
        assert_eq!(
            call(&engine, &alice(), "item_get", json!({"id": "i1"})).unwrap(),
            serde_json::to_value(
                engine
                    .instances()
                    .get(&alice(), "Item", "i1", Default::default())
                    .unwrap()
                    .unwrap()
            )
            .unwrap()
        );
        assert_eq!(
            call(&engine, &alice(), "item_list", json!({"limit": 1})).unwrap()["items"]
                .as_array()
                .unwrap()
                .len(),
            1
        );
        assert_eq!(
            call(
                &engine,
                &alice(),
                "item_increment",
                json!({"id": "i1", "params": {"by": 3}, "expectedSeq": 1})
            )
            .unwrap(),
            json!({"count": 3})
        );
        assert_eq!(
            call(&engine, &alice(), "item_history", json!({"id": "i1"})).unwrap(),
            json!([3])
        );
        let updated = call(
            &engine,
            &alice(),
            "item_update",
            json!({"id": "i1", "patch": {"title": "Table"}, "expectedSeq": 2}),
        )
        .unwrap();
        assert_eq!(
            json!([updated["seq"], updated["data"], updated["behaviors"]]),
            json!([3, {"title": "Table"}, {"test.Counter": {"count": 3}}])
        );
        assert_eq!(
            code(call(
                &engine,
                &alice(),
                "item_delete",
                json!({"id": "i1", "expectedSeq": 2})
            )),
            EngineErrorCode::SeqMismatch
        );
        assert_eq!(
            call(
                &engine,
                &alice(),
                "item_delete",
                json!({"id": "i1", "expectedSeq": 3})
            )
            .unwrap(),
            Value::Null
        );
        assert_eq!(
            code(call(&engine, &alice(), "item_get", json!({"id": "i1"}))),
            EngineErrorCode::NotFound
        );
        assert_eq!(
            code(call(&engine, &alice(), "item_delete", json!({"id": "i1"}))),
            EngineErrorCode::NotFound
        );
    }
}

#[test]
fn a_schema_level_tool_runs_its_operation_with_params_and_no_instance() {
    for target in targets("a_schema_level_tool_runs") {
        let engine = open(&target, fixtures().with(Holder));
        publish_item(&engine, json!([{"name": "test.Holder"}]));
        for (id, title) in [("i1", "Desk"), ("i2", "Lamp")] {
            engine
                .instances()
                .create(
                    &alice(),
                    "Item",
                    &json!({"title": title}),
                    CreateOptions {
                        id: Some(id),
                        ..Default::default()
                    },
                )
                .unwrap();
        }
        call(
            &engine,
            &alice(),
            "item_hold",
            json!({"id": "i2", "params": {"schema": "Item", "id": "i1"}}),
        )
        .unwrap();
        assert_eq!(
            call(
                &engine,
                &reader(),
                "item_holders",
                json!({"params": {"schema": "Item", "id": "i1"}})
            )
            .unwrap(),
            json!(["i2"])
        );
        assert_eq!(
            code(call(
                &engine,
                &alice(),
                "item_holders",
                json!({"id": "i2", "params": {"schema": "Item", "id": "i1"}})
            )),
            EngineErrorCode::InvalidArgument
        );
        assert_eq!(
            code(call(&engine, &alice(), "item_holders", json!({}))),
            EngineErrorCode::InvalidArgument
        );
        assert_eq!(
            code(call(
                &engine,
                &reader(),
                "item_release_all",
                json!({"params": {"schema": "Item", "id": "i1"}})
            )),
            EngineErrorCode::Forbidden
        );
        assert_eq!(
            call(
                &engine,
                &alice(),
                "item_release_all",
                json!({"params": {"schema": "Item", "id": "i1"}})
            )
            .unwrap(),
            json!(1)
        );
        assert_eq!(
            call(
                &engine,
                &alice(),
                "item_holders",
                json!({"params": {"schema": "Item", "id": "i1"}})
            )
            .unwrap(),
            json!([])
        );
    }
}

#[test]
fn the_schema_tools_list_describe_and_define_a_draft_which_stays_a_draft() {
    for target in targets("the_schema_tools") {
        let engine = open(&target, BehaviorSet::new());
        assert_eq!(
            call(&engine, &alice(), "list_schemas", Value::Null).unwrap(),
            json!([])
        );
        let draft = call(
            &engine,
            &alice(),
            "define_schema",
            json!({"document": order_document()}),
        )
        .unwrap();
        assert_eq!(
            json!([
                draft["name"],
                draft["version"],
                draft.get("canonical").is_some()
            ]),
            json!(["Order", null, false])
        );
        assert_eq!(
            call(&engine, &alice(), "list_schemas", json!({})).unwrap(),
            json!([{"namespace": "default", "name": "Order", "liveVersion": null, "hasDraft": true}])
        );
        assert_eq!(
            code(call(
                &engine,
                &alice(),
                "describe_schema",
                json!({"name": "Order"})
            )),
            EngineErrorCode::NotFound
        );
        engine
            .schemas()
            .publish(&alice(), "Order", SchemaTarget::default())
            .unwrap();
        assert_eq!(
            call(
                &engine,
                &alice(),
                "describe_schema",
                json!({"name": "Order"})
            )
            .unwrap(),
            describe(&engine, "Order")
        );
        assert_eq!(
            code(call(
                &engine,
                &reader(),
                "define_schema",
                json!({"document": order_document()})
            )),
            EngineErrorCode::Forbidden
        );
        assert_eq!(
            code(call(
                &engine,
                &alice(),
                "define_schema",
                json!({"document": {"kind": "General"}})
            )),
            EngineErrorCode::InvalidSchema
        );
    }
}

#[test]
fn tool_calls_refuse_arguments_of_the_wrong_shape_a_refused_write_and_a_tool_the_namespace_lacks() {
    for target in targets("tool_calls_refuse") {
        let engine = open(&target, fixtures());
        publish_item(&engine, json!([{"name": "test.Counter"}]));
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
        for (handle, args) in [
            ("item_get", json!({})),
            ("item_get", json!({"id": 7})),
            ("item_get", json!({"id": "i1", "extra": true})),
            ("item_get", json!(["i1"])),
            ("item_create", json!({"id": "i2"})),
            ("item_create", json!({"id": 3, "data": {"title": "Lamp"}})),
            ("item_update", json!({"id": "i1"})),
            (
                "item_update",
                json!({"id": "i1", "patch": {}, "expectedSeq": "one"}),
            ),
            ("item_list", json!({"limit": "ten"})),
            ("item_list", json!({"limit": 0})),
            (
                "item_increment",
                json!({"id": "i1", "params": {"by": "two"}}),
            ),
            ("describe_schema", json!({"name": 3})),
            ("define_schema", json!({"document": "text"})),
            ("list_schemas", json!({"all": true})),
            ("list_behaviors", json!({"name": "Workflow"})),
            ("describe_behavior", json!({})),
            ("describe_behavior", json!({"name": "not a name"})),
        ] {
            assert_eq!(
                code(call(&engine, &alice(), handle, args.clone())),
                EngineErrorCode::InvalidArgument,
                "{handle} {args}"
            );
        }
        assert_eq!(
            code(call(
                &engine,
                &alice(),
                "item_create",
                json!({"data": {"title": 3}})
            )),
            EngineErrorCode::InvalidInstance
        );
        assert_eq!(
            code(call(
                &engine,
                &reader(),
                "item_increment",
                json!({"id": "i1"})
            )),
            EngineErrorCode::Forbidden
        );
        assert_eq!(
            code(call(
                &engine,
                &reader(),
                "item_create",
                json!({"data": {"title": "Lamp"}})
            )),
            EngineErrorCode::Forbidden
        );
        assert_eq!(
            call(&engine, &reader(), "item_history", json!({"id": "i1"})).unwrap(),
            json!([])
        );
        for handle in ["item_publish", "order_create", "", "Item_get"] {
            let error = refusal(call(&engine, &alice(), handle, json!({})));
            assert_eq!(error.code(), EngineErrorCode::NotFound);
            assert_eq!(error.name(), "UnknownToolError");
        }
        let nobody = Principal::new("nobody", Vec::<String>::new());
        assert!(unknown_tool(call(
            &engine,
            &nobody,
            "item_get",
            json!({"id": "i1"})
        )));
        assert_eq!(
            code(engine.tools().call(
                &alice(),
                "item_get",
                &json!({"id": "i1"}),
                ToolTarget {
                    namespace: Some("nowhere"),
                    filter: None
                }
            )),
            EngineErrorCode::UnknownNamespace
        );
    }
}

#[test]
fn the_behavior_catalog_lists_and_describes_every_behavior_asking_the_policy_nothing() {
    for target in targets("the_behavior_catalog") {
        let asked = support::RecordingPolicy::new(|_: &AccessRequest<'_>| false);
        let recorded = asked.clone();
        let engine = target.open_with(fixtures().with(Holder), |options| {
            options.policy = Some(Policy::custom(asked))
        });
        let nobody = Principal::new("nobody", Vec::<String>::new());
        let summaries = engine.tools().list_behaviors(&nobody).unwrap();
        let named: Vec<String> = summaries
            .as_array()
            .unwrap()
            .iter()
            .map(|summary| summary["name"].as_str().unwrap().to_owned())
            .collect();
        assert_eq!(named, engine.behaviors().names());
        let counter: Value = serde_json::from_str(&counter_declaration()).unwrap();
        assert_eq!(
            summaries
                .as_array()
                .unwrap()
                .iter()
                .find(|summary| summary["name"] == "test.Counter")
                .unwrap(),
            &json!({"name": "test.Counter", "description": counter["description"], "requires": [], "conflicts": [], "fields": ["count"], "operations": ["increment", "history"]})
        );
        assert_eq!(
            engine
                .tools()
                .describe_behavior(&nobody, "test.Counter")
                .unwrap(),
            json!({
                "name": "test.Counter",
                "description": counter["description"],
                "configSchema": counter["configSchema"],
                "requires": [],
                "conflicts": [],
                "fields": [{"name": "count", "description": "The count."}],
                "operations": [
                    {"name": "increment", "description": "Adds to the count.", "scope": "instance", "writes": true, "invocationPolicy": "auto",
                        "paramsSchema": counter["operations"][0]["paramsSchema"], "resultSchema": counter["operations"][0]["resultSchema"]},
                    {"name": "history", "description": "Lists what each increment added.", "scope": "instance", "writes": false, "invocationPolicy": "auto",
                        "paramsSchema": counter["operations"][1]["paramsSchema"], "resultSchema": counter["operations"][1]["resultSchema"]}
                ],
                "vetoes": []
            })
        );
        let holders = engine
            .tools()
            .describe_behavior(&nobody, "test.Holder")
            .unwrap()["operations"]
            .as_array()
            .unwrap()
            .iter()
            .find(|operation| operation["name"] == "holders")
            .unwrap()
            .clone();
        assert_eq!(
            keys(&holders),
            vec![
                "name",
                "description",
                "scope",
                "writes",
                "invocationPolicy",
                "paramsSchema",
                "resultSchema"
            ]
        );
        assert_eq!(
            call(&engine, &nobody, "list_behaviors", json!({})).unwrap(),
            summaries
        );
        assert_eq!(
            call(
                &engine,
                &nobody,
                "describe_behavior",
                json!({"name": "test.Flag"})
            )
            .unwrap(),
            engine
                .tools()
                .describe_behavior(&nobody, "test.Flag")
                .unwrap()
        );
        assert_eq!(
            code(engine.tools().describe_behavior(&nobody, "test.Missing")),
            EngineErrorCode::NotFound
        );
        let questions = recorded.take();
        assert!(
            questions.is_empty(),
            "the catalog asks nothing: {questions:?}"
        );
    }
}

// test.Notes says what its config's mode says (`behavior-guidance.test.ts`):
// guidance the engine merges, or one of the defects it refuses.
struct Notes;

impl Behavior for Notes {
    type Config = String;

    fn declaration(&self) -> &str {
        r#"{"name": "test.Notes",
            "configSchema": {"type": "object", "additionalProperties": false, "properties": {"mode": {"type": "string"}}},
            "createParamsSchema": {"type": "object", "additionalProperties": {"type": "string"}},
            "operations": [{"name": "note", "paramsSchema": {"type": "object", "additionalProperties": false}, "resultSchema": true, "writes": true}],
            "vetoes": [{"code": "full", "description": "The instance holds as many notes as it may."}]}"#
    }

    fn parse_config(&self, raw: &Value, _target: &ConfigTarget<'_>) -> Result<String> {
        Ok(raw
            .get("mode")
            .and_then(Value::as_str)
            .unwrap_or("good")
            .to_owned())
    }

    fn hooks(&self) -> Hooks<Self> {
        use superschematic_engine::behaviors::{
            BehaviorGuidance, GuidanceError, OperationGuidance,
        };
        Hooks {
            config_change: Some(|_, _, _, _| Ok(None)),
            operations: vec![("note", |_, _, _| Ok(Value::Null))],
            guidance: Some(|_, mode, _| {
                let error =
                    |code: &str, description: Option<&str>, correction: &str| GuidanceError {
                        code: code.into(),
                        description: description.map(str::to_owned),
                        common_correction: correction.into(),
                    };
                match mode.as_str() {
                    "undeclared" => BehaviorGuidance {
                        summary: "Notes.".into(),
                        operations: vec![(
                            "note".into(),
                            OperationGuidance {
                                errors: vec![error("nope", None, "None.")],
                                ..Default::default()
                            },
                        )],
                    },
                    "unknownOperation" => BehaviorGuidance {
                        summary: "Notes.".into(),
                        operations: vec![(
                            "publish".into(),
                            OperationGuidance {
                                use_when: Some("Use it.".into()),
                                ..Default::default()
                            },
                        )],
                    },
                    "padded" => BehaviorGuidance {
                        summary: "Notes.".into(),
                        operations: vec![(
                            "note".into(),
                            OperationGuidance {
                                use_when: Some(" Use it.".into()),
                                ..Default::default()
                            },
                        )],
                    },
                    "noSummary" => BehaviorGuidance::default(),
                    _ => BehaviorGuidance {
                        summary: "Each instance keeps notes.".into(),
                        operations: vec![
                            (
                                "note".into(),
                                OperationGuidance {
                                    use_when: Some("Use to add a note.".into()),
                                    errors: vec![
                                        error("full", None, "Delete a note first."),
                                        error("full", None, "Twice."),
                                    ],
                                    ..Default::default()
                                },
                            ),
                            (
                                "create".into(),
                                OperationGuidance {
                                    success: Some("It starts with no notes.".into()),
                                    ..Default::default()
                                },
                            ),
                            (
                                "update".into(),
                                OperationGuidance {
                                    do_not_use_when: Some("Do not move a noted instance.".into()),
                                    errors: vec![error(
                                        "full",
                                        Some("The notes are full."),
                                        "Wait.",
                                    )],
                                    ..Default::default()
                                },
                            ),
                        ],
                    },
                }
            }),
            create_params_schema: Some(|_, mode, _| {
                if mode == "badParams" {
                    json!({"type": "array"})
                } else {
                    json!({"type": "object", "additionalProperties": false, "properties": {"title": {"type": "string"}}})
                }
            }),
            ..Hooks::default()
        }
    }
}

#[test]
fn the_engine_merges_what_a_behavior_says_and_refuses_what_its_declaration_does_not_hold() {
    for target in targets("the_engine_merges_guidance") {
        let open_mode = |mode: &str| {
            let engine = target.open_with(BehaviorSet::new().with(Notes), |_| {});
            let mut document = schema_document(
                "Item",
                json!([{"name": "title", "typeRef": {"name": "string"}, "required": true}]),
                json!({}),
            );
            document["types"]["Item"]["behaviors"] =
                json!([{"name": "test.Notes", "config": {"mode": mode}}]);
            publish(&engine, &document, None);
            engine
        };
        let engine = open_mode("good");
        let described = describe(&engine, "Item");
        assert_eq!(
            described["behaviors"][0]["summary"],
            json!("Each instance keeps notes.")
        );
        let guidance = |name: &str| operation(&engine, "Item", name)["guidance"].clone();
        assert_eq!(
            guidance("note"),
            json!({"useWhen": "Use to add a note.", "doNotUseWhen": "", "success": "",
                "errors": [{"code": "full", "description": "test.Notes: The instance holds as many notes as it may.", "commonCorrection": "Delete a note first."}]})
        );
        let create = guidance("create");
        assert!(create["success"]
            .as_str()
            .unwrap()
            .starts_with("Returns the new instance "));
        assert!(create["success"]
            .as_str()
            .unwrap()
            .ends_with(". It starts with no notes."));
        assert!(create["useWhen"]
            .as_str()
            .unwrap()
            .contains(" behaviors gives test.Notes their create parameters"));
        let update = guidance("update");
        assert!(update["doNotUseWhen"]
            .as_str()
            .unwrap()
            .ends_with(" Do not move a noted instance."));
        assert_eq!(
            update["errors"].as_array().unwrap().last().unwrap(),
            &json!({"code": "full", "description": "test.Notes: The notes are full.", "commonCorrection": "Wait."})
        );
        assert_eq!(
            operation(&engine, "Item", "create")["params"]["properties"]["behaviors"]["properties"]
                ["test.Notes"],
            json!({"type": "object", "additionalProperties": false, "properties": {"title": {"type": "string"}}})
        );
        // The tools document and MCP's _meta carry the same guidance.
        let note = tool(&engine, "item.note", &alice());
        assert_eq!(note["guidance"], guidance("note"));
        assert_eq!(
            note["mcp"]["_meta"]["superschematic/operation-guidance"],
            guidance("note")
        );
        for (mode, message) in [
            (
                "undeclared",
                "an error's code is a veto code its declaration lists (full), not \"nope\"",
            ),
            (
                "unknownOperation",
                "guidance names operation publish, which Item does not have",
            ),
            (
                "padded",
                "useWhen is a string without surrounding whitespace",
            ),
            ("noSummary", "guidance returns a summary"),
            ("badParams", "createParamsSchema must be an object schema"),
        ] {
            let engine = open_mode(mode);
            for result in [
                engine
                    .tools()
                    .describe(&alice(), "Item", ToolTarget::default()),
                engine.tools().manifest(&alice(), ToolTarget::default()),
            ] {
                match result {
                    Err(Error::Behavior(defect)) => {
                        assert!(defect.to_string().contains(message), "{mode}: {defect}")
                    }
                    other => panic!("{mode}: expected a BehaviorError, got {other:?}"),
                }
            }
        }
    }
}

// A hidden tool names each cause in hiddenBy, in a fixed order, and gives
// the first one's reason: the deployment's before the handle's, and the
// policy's before the handle's, since the policy is asked of a tool MCP
// refuses the handle of, whose operation the API still offers. A tool the
// deployment hides is asked of no policy.
#[test]
fn a_hidden_tool_names_each_cause_in_hidden_by_and_the_first_ones_reason() {
    for target in targets("hidden_by") {
        let engine = target.open_with(fixtures(), |options| {
            options.policy = Some(policy());
            options.tools = ToolOptions {
                hidden: vec!["test.Counter.increment".into()],
                ..ToolOptions::default()
            };
        });
        publish_item(&engine, json!([{"name": "test.Counter"}]));
        let long = format!("A{}", "b".repeat(42));
        let mut document = schema_document(
            &long,
            json!([{"name": "title", "typeRef": {"name": "string"}}]),
            json!({}),
        );
        document["types"][long.as_str()]["behaviors"] = json!([{"name": "test.Counter"}]);
        publish(&engine, &document, None);
        let kebab = long.to_lowercase();
        let snake = long.to_lowercase();
        let hidden = |name: &str, principal: &Principal| {
            let mcp = tool(&engine, name, principal)["mcp"].clone();
            assert_eq!(mcp["hidden"], json!(true), "{name}");
            assert_eq!(
                keys(&mcp),
                vec!["hidden", "hiddenReason", "hiddenBy"],
                "{name}"
            );
            (mcp["hiddenBy"].clone(), mcp["hiddenReason"].clone())
        };
        let deployment = json!("the deployment's tool options hide test.Counter.increment (tools.hidden): a service calls it through the API, and no agent is offered it");
        assert_eq!(
            hidden("item.increment", &alice()),
            (json!(["deployment"]), deployment.clone())
        );
        assert_eq!(
            hidden("item.increment", &reader()),
            (json!(["deployment"]), deployment.clone())
        );
        assert_eq!(
            hidden(&format!("{kebab}.increment"), &alice()),
            (json!(["deployment", "handle"]), deployment.clone())
        );
        assert_eq!(
            hidden(&format!("{kebab}.history"), &alice()),
            (
                json!(["handle"]),
                json!(format!(
                    "its MCP handle {snake}_history is longer than 48 characters"
                ))
            )
        );
        assert_eq!(
            hidden(&format!("{kebab}.history"), &reader()),
            (
                json!(["handle"]),
                json!(format!(
                    "its MCP handle {snake}_history is longer than 48 characters"
                ))
            )
        );
        assert_eq!(
            hidden(&format!("{kebab}.delete"), &reader()),
            (
                json!(["policy", "handle"]),
                json!(format!("the access policy refuses reader write on {long}"))
            )
        );
        assert_eq!(
            hidden("item.create", &reader()),
            (
                json!(["policy"]),
                json!("the access policy refuses reader write on Item")
            )
        );
        assert_eq!(
            tool(&engine, &format!("{kebab}.get"), &reader())["mcp"]["hidden"],
            json!(false)
        );
    }
}

// The describe document gives each own field's display as the schema
// holds it, a currency's field by its key in an instance's data.
#[test]
fn describes_each_fields_display_its_currency_field_by_its_key() {
    for target in targets("describes_each_fields_display") {
        let engine = open(&target, BehaviorSet::new());
        publish(
            &engine,
            &schema_document(
                "Invoice",
                json!([
                    {"name": "note", "typeRef": {"name": "string"}, "display": {"multiline": true}},
                    {"name": "total", "typeRef": {"name": "Generic.Int64"}, "title": "Total", "display": {"currency": "GBP"}},
                    {"name": "code", "jsonTag": "currencyCode", "typeRef": {"name": "string"}},
                    {"name": "due", "typeRef": {"name": "Generic.Int64"}, "display": {"currency": {"field": "code"}}},
                    {"name": "paid", "typeRef": {"name": "Generic.Int64"}, "display": {"currency": {"field": "currencyCode"}}}
                ]),
                json!({}),
            ),
            None,
        );
        assert_eq!(
            describe(&engine, "Invoice")["fields"],
            json!([
                {"name": "note", "display": {"multiline": true}},
                {"name": "total", "title": "Total", "display": {"currency": "GBP"}},
                {"name": "currencyCode"},
                {"name": "due", "display": {"currency": {"field": "currencyCode"}}},
                {"name": "paid", "display": {"currency": {"field": "currencyCode"}}}
            ])
        );
    }
}
