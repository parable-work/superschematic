//! What the engine client generator narrows (D49), against the Rust
//! engine's describe document (D58): `runtime/engine/testdata/
//! client_codegen_parity.json`, which `internal/generator/engineclientgen`
//! writes. For each schema the generator's create parameters must be the
//! describe document's, descriptions aside, and its behavior fields must
//! be the ones the describe document names, by behavior — the first two
//! cases of `test/client-codegen.test.ts`, on an engine with the core's
//! behaviors ported so far, as `openTestEngine` opens one. A document that
//! composes a behavior the engine does not run yet (milestones 9 to 11) is
//! not published and its schema is counted as skipped: the vectors'
//! `notes` waits for Revisions, `Spec` for Revisions and Dependencies,
//! `Task` for Links, Numbering and Dependencies, `Project` for Rollups.
//! The test fails if the vectors stop covering the shapes it checks.

mod support;

use serde_json::{Map, Value};

use superschematic_engine::ToolTarget;
use support::{alice, publish, repository, targets};

fn corpus() -> Value {
    let path = repository().join("runtime/engine/testdata/client_codegen_parity.json");
    let text = std::fs::read_to_string(&path)
        .unwrap_or_else(|error| panic!("{}: {error}", path.display()));
    serde_json::from_str(&text).expect("the parity file is JSON")
}

// without_descriptions copies a JSON value without its schemas'
// descriptions, which each side words itself.
fn without_descriptions(value: &Value) -> Value {
    match value {
        Value::Array(items) => Value::Array(items.iter().map(without_descriptions).collect()),
        Value::Object(members) => Value::Object(
            members
                .iter()
                .filter(|(key, _)| *key != "description")
                .map(|(key, member)| (key.clone(), without_descriptions(member)))
                .collect::<Map<String, Value>>(),
        ),
        other => other.clone(),
    }
}

// composed lists the behaviors a document's types compose.
fn composed(document: &Value) -> Vec<String> {
    let mut names = Vec::new();
    for type_def in document["types"]
        .as_object()
        .into_iter()
        .flat_map(|types| types.values())
    {
        for behavior in type_def["behaviors"].as_array().into_iter().flatten() {
            if let Some(name) = behavior["name"].as_str() {
                if !names.iter().any(|known| known == name) {
                    names.push(name.to_owned());
                }
            }
        }
    }
    names
}

#[test]
fn the_engines_describe_document_narrows_create_parameters_and_names_fields_as_the_generator_does()
{
    let corpus = corpus();
    let documents = corpus["documents"].as_array().expect("documents");
    let schemas = corpus["schemas"].as_object().expect("schemas");
    assert!(!documents.is_empty() && !schemas.is_empty());
    assert_eq!(
        schemas["Task"]["createParams"]
            .as_object()
            .map(|params| {
                let mut keys: Vec<&String> = params.keys().collect();
                keys.sort();
                keys
            })
            .unwrap_or_default(),
        vec!["Dependencies", "Links"]
    );
    for name in schemas.keys() {
        assert!(
            documents
                .iter()
                .any(|document| document["name"].as_str() == Some(name.as_str())),
            "{name} names no document of the vectors"
        );
    }
    for target in targets("client_codegen_parity") {
        let engine = target.open_core();
        // The documents in order, each whose behaviors the engine runs.
        let mut skipped: Vec<(String, Vec<String>)> = Vec::new();
        for document in documents {
            let missing: Vec<String> = composed(document)
                .into_iter()
                .filter(|name| !engine.behaviors().has(name))
                .collect();
            if missing.is_empty() {
                publish(&engine, document, None);
            } else {
                let name = document["name"].as_str().expect("a document's name");
                skipped.push((name.to_owned(), missing));
            }
        }
        eprintln!(
            "client_codegen_parity on {:?}: {} of {} schemas checked",
            target.dialect,
            schemas.len() - skipped.len(),
            schemas.len()
        );
        for (name, missing) in &skipped {
            eprintln!(
                "    skipped {name}: composes behaviors this engine does not run yet ({})",
                missing.join(", ")
            );
        }
        for (name, expected) in schemas {
            if skipped.iter().any(|(skip, _)| skip == name) {
                continue;
            }
            let described = engine
                .tools()
                .describe(&alice(), name, ToolTarget::default())
                .expect("describe");
            let create = described["operations"]
                .as_array()
                .unwrap()
                .iter()
                .find(|operation| operation["name"] == "create")
                .unwrap()
                .clone();
            let mut behaviors = create["params"]["properties"]["behaviors"]["properties"].clone();
            if behaviors.is_null() {
                behaviors = Value::Object(Map::new());
            }
            assert_eq!(
                without_descriptions(&behaviors),
                without_descriptions(&expected["createParams"]),
                "{name}"
            );
            let named = |schema: &Value| -> Vec<(String, Vec<String>)> {
                let mut out: Vec<(String, Vec<String>)> = schema
                    .as_object()
                    .into_iter()
                    .flatten()
                    .map(|(behavior, fields)| {
                        let fields = fields.get("properties").unwrap_or(fields);
                        let mut keys: Vec<String> = fields
                            .as_object()
                            .into_iter()
                            .flatten()
                            .map(|(key, _)| key.clone())
                            .collect();
                        keys.sort();
                        (behavior.clone(), keys)
                    })
                    .collect();
                out.sort();
                out
            };
            assert_eq!(
                named(&described["instanceBehaviors"]["properties"]),
                named(&expected["fields"]),
                "{name}"
            );
        }
    }
}
