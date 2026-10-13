//! The strict schema-file loader against the Go data-form reader, over
//! runtime/schema/testdata/schema_file_parity.json, through the
//! `schemafile` feature. internal/loader/schemafile
//! (TestSchemaFileParityCorpus) writes it: each payload with the Go
//! reader's verdict and, for an accepted one, the decoded document as
//! ir.CanonicalJSON writes it, and JSON texts with ir.CanonicalJSON's
//! output. The loader must reach the same verdict and write the same
//! bytes. The "core" vectors load against the default meta-schema; the
//! "extended" ones against the meta-schema of a registry that adds a kind,
//! extension decorators, documents and its own invocation policy,
//! schema_file_parity.meta-schema.json. Each test is the TypeScript
//! runner's (runtime/schema/typescript/test/schema-file-parity.cjs).

use std::path::{Path, PathBuf};

use serde_json::Value;
use superschematic_schema_ir::schemafile::{
    canonical_json, load_schema_file, SchemaFileLoader, CORE_META_SCHEMA,
};

fn testdata(name: &str) -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../testdata")
        .join(name)
}

fn corpus() -> Value {
    let text = std::fs::read_to_string(testdata("schema_file_parity.json"))
        .expect("read schema_file_parity.json");
    serde_json::from_str(&text).expect("schema_file_parity.json is JSON")
}

fn extended_meta_schema_text() -> String {
    std::fs::read_to_string(testdata("schema_file_parity.meta-schema.json"))
        .expect("read schema_file_parity.meta-schema.json")
}

fn text<'a>(vector: &'a Value, key: &str) -> &'a str {
    vector[key]
        .as_str()
        .unwrap_or_else(|| panic!("{key} is a string"))
}

#[test]
fn canonical_vectors_write_what_go_writes() {
    let corpus = corpus();
    let vectors = corpus["canonical"].as_array().expect("canonical vectors");
    assert!(!vectors.is_empty());
    let mut failures = Vec::new();
    for vector in vectors {
        let name = text(vector, "name");
        let got = canonical_json(text(vector, "input"));
        match (&vector["error"], got) {
            (Value::String(_), Ok(output)) => {
                failures.push(format!("{name}: Go refuses it, got {output}"));
            }
            (Value::String(_), Err(_)) => {}
            (_, Ok(output)) if output == text(vector, "output") => {}
            (_, got) => failures.push(format!("{name}: got {got:?}")),
        }
    }
    assert!(failures.is_empty(), "{}", failures.join("\n"));
}

#[test]
fn every_vector_has_the_go_readers_verdict_and_bytes() {
    let corpus = corpus();
    let core = SchemaFileLoader::new();
    let extended = SchemaFileLoader::with_meta_schema_text(&extended_meta_schema_text())
        .expect("the extended meta-schema compiles");
    let vectors = corpus["vectors"].as_array().expect("vectors");
    assert!(!vectors.is_empty());
    let mut failures = Vec::new();
    for vector in vectors {
        let name = text(vector, "name");
        let registry = text(vector, "registry");
        let loader = match registry {
            "core" => &core,
            "extended" => &extended,
            other => panic!("unknown registry {other}"),
        };
        let loaded = loader.load(text(vector, "input"), name);
        let accept = vector["accept"].as_bool().expect("accept is a boolean");
        match (accept, loaded) {
            (false, Err(error)) if error.source() == name => {}
            (false, Err(error)) => {
                failures.push(format!("{registry}: {name}: refused as {}", error.source()));
            }
            (false, Ok(loaded)) => failures.push(format!(
                "{registry}: {name}: Go refuses it ({}), got {}",
                text(vector, "error"),
                loaded.canonical
            )),
            (true, Err(error)) => failures.push(format!("{registry}: {name}: {error}")),
            (true, Ok(loaded)) => {
                let canonical = text(vector, "canonical");
                if loaded.canonical != canonical {
                    failures.push(format!(
                        "{registry}: {name}:\n  got  {}\n  want {canonical}",
                        loaded.canonical
                    ));
                }
                let document: Value =
                    serde_json::from_str(canonical).expect("the canonical text is JSON");
                if loaded.document != document {
                    failures.push(format!("{registry}: {name}: the document differs"));
                }
            }
        }
    }
    assert!(
        failures.is_empty(),
        "{} of {} vectors failed:\n{}",
        failures.len(),
        vectors.len(),
        failures.join("\n")
    );
}

#[test]
fn load_schema_file_uses_the_core_meta_schema() {
    let corpus = corpus();
    let vectors = corpus["vectors"].as_array().expect("vectors");
    let vector = vectors
        .iter()
        .find(|v| v["registry"] == "core" && v["accept"] == true)
        .expect("an accepted core vector");
    assert_eq!(
        load_schema_file(text(vector, "input"), "payload")
            .unwrap()
            .canonical,
        text(vector, "canonical")
    );
    let extended = vectors
        .iter()
        .find(|v| v["registry"] == "extended" && v["name"] == "the registry's policy value")
        .expect("the registry's policy value");
    assert!(load_schema_file(text(extended, "input"), "payload").is_err());
    let loader = SchemaFileLoader::with_meta_schema_text(&extended_meta_schema_text()).unwrap();
    assert_eq!(
        loader
            .load(text(extended, "input"), "payload")
            .unwrap()
            .canonical,
        text(extended, "canonical")
    );
}

#[test]
fn a_repeated_key_names_its_json_pointer() {
    let loader = SchemaFileLoader::new();
    let cases = [
        (
            r#"{"name": "Item", "role": "EmbeddedStruct", "fields": [], "fields": []}"#,
            "/fields",
            "fields",
        ),
        (
            r#"{"types": {"a/b~c": {"name": "a/b~c", "role": "EmbeddedStruct", "fields": [{"name": "x", "typeRef": {"name": "String"}, "extensions": {}, "extensions": {}}]}}}"#,
            "/types/a~1b~0c/fields/0/extensions",
            "extensions",
        ),
        (
            r#"{"documents": {"d": [{"k": [1, {"a": 1, "b": 2, "a": 3}]}]}}"#,
            "/documents/d/0/k/1/a",
            "a",
        ),
        (
            r#"{"name": "Item", "role": "EmbeddedStruct", "description": "x", "description": "x"}"#,
            "/description",
            "description",
        ),
        // Go reads each half of a surrogate pair alone as U+FFFD.
        (
            r#"{"name": "Item", "role": "EmbeddedStruct", "implements": [{"name": "Named", "configArgs": {"\ud800": 1, "\udc00": 2}}]}"#,
            "/implements/0/configArgs/\u{FFFD}",
            "\u{FFFD}",
        ),
    ];
    for (input, path, key) in cases {
        let error = loader.load(input, "payload").unwrap_err();
        assert_eq!(error.issues().len(), 1, "{input}");
        assert_eq!(error.issues()[0].path, path, "{input}");
        assert_eq!(
            error.issues()[0].message,
            format!(
                "repeated object key {}",
                serde_json::to_string(key).unwrap()
            ),
            "{input}"
        );
    }
    // A string that holds a repeated key, and the same key in sibling
    // objects and array elements.
    let loaded = loader
        .load(
            r#"{"types": {"A": {"name": "A", "role": "EmbeddedStruct", "description": "{\"name\": 1, \"name\": 2}", "implements": [{"name": "N", "configArgs": {"k": ["k", "k"]}}, {"name": "M", "configArgs": {"k": 1}}]}, "B": {"name": "B", "role": "EmbeddedStruct"}}}"#,
            "payload",
        )
        .unwrap();
    let types: Vec<&String> = loaded.document["types"]
        .as_object()
        .unwrap()
        .keys()
        .collect();
    assert_eq!(types, ["A", "B"]);
}

#[test]
fn a_meta_schema_without_the_invocation_policy_default_is_refused() {
    let mut meta_schema: Value = serde_json::from_str(&extended_meta_schema_text()).unwrap();
    meta_schema["$defs"]["OperationMCP"]["properties"]["review"]
        .as_object_mut()
        .expect("the registry's policy")
        .remove("default");
    let error = SchemaFileLoader::with_meta_schema(&meta_schema).unwrap_err();
    assert!(error.to_string().contains("invocation policy"), "{error}");
}

#[test]
fn the_core_meta_schema_is_the_schema_ir_packages() {
    let package = std::fs::read_to_string(
        Path::new(env!("CARGO_MANIFEST_DIR")).join("../../../ir/typescript/schema-file.json"),
    );
    // A published crate has no checkout beside it to compare with.
    if let Ok(package) = package {
        assert_eq!(
            CORE_META_SCHEMA, package,
            "run go run ./internal/tools/schemafiletypes"
        );
    }
}
