//! The engine's JSON Schema of a schema's fields against the Go side's,
//! the port of `test/tools-parity.test.ts` (D58):
//! `runtime/engine/testdata/tool_parameters_parity.json`, which
//! `internal/generator/toolsutil` writes from what the SDK generators build
//! for the same schema-file document (every scalar of the pinned catalog,
//! enums, nested and recursive types, lists and lists of lists, field rules
//! and the document's own scalars), under the core's vendor keys, a
//! distribution's and none; the defaults, names, handles and option checks
//! the Go side gives verdicts on; and a published schema's describe and
//! tools documents, on SQLite and Postgres.

mod support;

use serde_json::Value;

use superschematic_engine::tools::{
    is_mcp_handle, kebab_case, snake_case, type_arguments, InvocationPolicy, ToolKeys, ToolOptions,
};
use superschematic_engine::{BehaviorSet, ToolTarget};
use support::{alice, publish, repository, targets};

fn corpus() -> Value {
    let path = repository().join("runtime/engine/testdata/tool_parameters_parity.json");
    let text = std::fs::read_to_string(&path)
        .unwrap_or_else(|error| panic!("{}: {error}", path.display()));
    serde_json::from_str(&text).expect("the parity file is JSON")
}

fn keys_of(case: &Value) -> ToolKeys {
    let keys = &case["keys"];
    ToolKeys {
        scalar: keys["scalar"].as_str().unwrap_or_default().to_owned(),
        guidance: keys["guidance"].as_str().unwrap_or_default().to_owned(),
        parameters: keys["parameters"]
            .as_array()
            .map(|parameters| {
                parameters
                    .iter()
                    .map(|entry| {
                        (
                            entry["key"].as_str().unwrap_or_default().to_owned(),
                            entry["value"].clone(),
                        )
                    })
                    .collect()
            })
            .unwrap_or_default(),
    }
}

fn options_of(keys: &ToolKeys) -> ToolOptions {
    ToolOptions {
        scalar_key: Some(keys.scalar.clone()),
        guidance_key: Some(keys.guidance.clone()),
        parameter_keys: Some(keys.parameters.clone()),
        ..ToolOptions::default()
    }
}

fn names(value: &Value) -> Vec<String> {
    value
        .as_array()
        .map(|items| {
            items
                .iter()
                .filter_map(Value::as_str)
                .map(str::to_owned)
                .collect()
        })
        .unwrap_or_default()
}

fn object_keys(value: &Value) -> Vec<String> {
    value
        .as_object()
        .map(|members| members.keys().cloned().collect())
        .unwrap_or_default()
}

#[test]
fn the_go_vectors_cover_what_the_engine_must_match() {
    let corpus = corpus();
    let cases = corpus["cases"].as_array().expect("cases");
    assert_eq!(cases.len(), 3);
    let properties: Vec<&Value> = cases[0]["parameters"]["properties"]
        .as_object()
        .expect("properties")
        .values()
        .collect();
    assert!(properties.len() > 70, "a field per catalog scalar");
    let any = |types: Value| properties.iter().any(|property| property["type"] == types);
    assert!(any(serde_json::json!([
        "object", "array", "string", "number", "boolean"
    ])));
    assert!(any(serde_json::json!([
        "object", "array", "string", "number", "boolean", "null"
    ])));
    assert!(properties.iter().any(|property| property
        .get("items")
        .and_then(|items| items.get("items"))
        .is_some()));
}

#[test]
fn the_engines_defaults_are_the_go_defaults() {
    let corpus = corpus();
    assert_eq!(ToolKeys::default().to_json(), corpus["defaults"]["keys"]);
    let policy = InvocationPolicy::default();
    assert_eq!(
        serde_json::json!({"key": policy.key, "values": policy.values, "default": policy.default}),
        corpus["defaults"]["invocationPolicy"]
    );
}

#[test]
fn a_tools_name_and_handle_take_the_go_case_conversions_and_the_registrys_checks() {
    let corpus = corpus();
    let cases = corpus["names"].as_array().expect("names");
    assert!(!cases.is_empty());
    for case in cases {
        let input = case["input"].as_str().unwrap();
        assert_eq!(
            (kebab_case(input), snake_case(input)),
            (
                case["kebab"].as_str().unwrap().to_owned(),
                case["snake"].as_str().unwrap().to_owned()
            ),
            "{input}"
        );
    }
    for verdict in corpus["handles"].as_array().expect("handles") {
        let value = verdict["value"].as_str().unwrap();
        assert_eq!(
            is_mcp_handle(value),
            verdict["valid"] == Value::Bool(true),
            "{value:?}"
        );
    }
    let accepts = |policy: InvocationPolicy| {
        ToolOptions {
            invocation_policy: Some(policy),
            ..ToolOptions::default()
        }
        .check()
        .is_ok()
    };
    for verdict in corpus["policyKeys"].as_array().expect("policyKeys") {
        let value = verdict["value"].as_str().unwrap();
        assert_eq!(
            accepts(InvocationPolicy {
                key: value.to_owned(),
                values: vec!["auto".into()],
                default: "auto".into(),
            }),
            verdict["valid"] == Value::Bool(true),
            "{value:?}"
        );
    }
    for verdict in corpus["policyValues"].as_array().expect("policyValues") {
        let value = verdict["value"].as_str().unwrap();
        assert_eq!(
            accepts(InvocationPolicy {
                key: "review".into(),
                values: vec![value.to_owned()],
                default: value.to_owned(),
            }),
            verdict["valid"] == Value::Bool(true),
            "{value:?}"
        );
    }
}

#[test]
fn type_arguments_writes_the_sdk_generators_arguments_and_digest() {
    let corpus = corpus();
    let instance_type = corpus["instanceType"].as_str().unwrap();
    for case in corpus["cases"].as_array().unwrap() {
        let (parameters, digest) =
            type_arguments(&corpus["document"], instance_type, &keys_of(case)).unwrap();
        assert_eq!(parameters, case["parameters"], "{}", case["name"]);
        assert_eq!(
            Value::from(digest),
            case["inputSchemaDigest"],
            "{}",
            case["name"]
        );
    }
}

#[test]
fn a_published_schema_describes_its_instance_and_takes_creates_data_as_the_go_vectors_do() {
    let corpus = corpus();
    for target in targets("tools_parity_published") {
        for case in corpus["cases"].as_array().unwrap() {
            let keys = keys_of(case);
            let engine = target.open_with(BehaviorSet::new(), |options| {
                options.tools = options_of(&keys);
            });
            publish(&engine, &corpus["document"], None);
            let mut want = case["parameters"].as_object().unwrap().clone();
            for (key, _) in &keys.parameters {
                want.shift_remove(key);
            }
            let want = Value::Object(want);
            let described = engine
                .tools()
                .describe(&alice(), "Shelf", ToolTarget::default())
                .unwrap();
            assert_eq!(described["instance"], want, "{}", case["name"]);
            let create = described["operations"]
                .as_array()
                .unwrap()
                .iter()
                .find(|operation| operation["name"] == "create")
                .unwrap()
                .clone();
            let mut data = create["params"]["properties"]["data"]
                .as_object()
                .unwrap()
                .clone();
            assert_eq!(
                data.shift_remove("description"),
                Some(Value::from("Shelf object"))
            );
            assert_eq!(Value::Object(data), want);
            for (key, value) in &keys.parameters {
                assert_eq!(&create["params"][key.as_str()], value);
            }
            let manifest = engine
                .tools()
                .manifest(&alice(), ToolTarget::default())
                .unwrap();
            assert_eq!(
                object_keys(&manifest),
                names(&corpus["keys"]["ToolManifest"])
            );
            let tools = manifest["tools"].as_array().unwrap();
            let shelf = tools
                .iter()
                .find(|tool| tool["name"] == "shelf.create")
                .unwrap();
            assert_eq!(shelf["parameters"], create["params"]);
            for entry in tools {
                assert_eq!(
                    object_keys(entry),
                    names(&corpus["keys"]["ToolManifestTool"]),
                    "{}",
                    entry["name"]
                );
                assert_eq!(
                    object_keys(&entry["guidance"]),
                    names(&corpus["keys"]["ToolOperationGuidance"])
                );
                if entry["replay"].is_object() {
                    assert_eq!(
                        object_keys(&entry["replay"]),
                        names(&corpus["keys"]["ToolReplayContract"])
                    );
                }
                let returned = object_keys(&entry["returns"]);
                let order: Vec<String> = names(&corpus["keys"]["ToolReturnSchema"])
                    .into_iter()
                    .filter(|key| returned.contains(key))
                    .collect();
                assert_eq!(returned, order, "{}", entry["name"]);
            }
        }
    }
}
