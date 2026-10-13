//! The cross-language validation corpus,
//! runtime/schema/testdata/validation_parity.json, through the IR reader.
//! The generated-validator parity harness
//! (internal/generator/parity) writes it: the matrix schema's IR and one
//! vector table with the expected verdicts. Every vector is read as the
//! TypeScript runner (runtime/schema/typescript/test/validation-parity.cjs)
//! reads it through the IR reader: the schema read with parse_schema_ir,
//! each payload validated as its type, and the errors flattened to dotted
//! paths with sorted validator names, which must be `want` exactly. A path
//! the scalar core alone refuses is named as the core names it, as the
//! TypeScript runtime reports it. This crate reads IR, not the schema JSON
//! form, so the runner's JSON-reader pass has no counterpart here.

use std::collections::BTreeMap;
use std::path::Path;

use serde_json::Value;
use superschematic_schema_ir::{parse_schema_ir, validate_schema_type, FieldErrors, Runtime};

fn corpus() -> Value {
    let path = Path::new(env!("CARGO_MANIFEST_DIR")).join("../testdata/validation_parity.json");
    let text = std::fs::read_to_string(&path).expect("read validation_parity.json");
    serde_json::from_str(&text).expect("validation_parity.json is JSON")
}

/// The errors as path -> sorted validator names, nested object errors at
/// dotted paths.
fn verdicts(errors: &FieldErrors) -> BTreeMap<String, Vec<String>> {
    let mut out: BTreeMap<String, Vec<String>> = BTreeMap::new();
    for (path, list) in errors.flatten() {
        let validators = out.entry(path).or_default();
        validators.extend(list.into_iter().map(|error| error.validator));
        validators.sort();
    }
    out
}

fn want(vector: &Value) -> BTreeMap<String, Vec<String>> {
    vector["want"]
        .as_object()
        .expect("want is an object")
        .iter()
        .map(|(path, validators)| {
            let names = validators
                .as_array()
                .expect("want lists validator names")
                .iter()
                .map(|name| name.as_str().expect("a validator name").to_owned())
                .collect();
            (path.clone(), names)
        })
        .collect()
}

#[test]
fn every_vector_has_the_corpus_verdict() {
    let corpus = corpus();
    let schema = parse_schema_ir(&corpus["schema"]).expect("the corpus schema reads");
    let runtime = Runtime::new(schema.clone());
    let vectors = corpus["vectors"].as_array().expect("vectors");
    assert!(!vectors.is_empty());

    let mut failures = Vec::new();
    for vector in vectors {
        let name = vector["name"].as_str().expect("a vector name");
        let type_name = vector["type"].as_str().expect("a type name");
        let want = want(vector);
        let got = verdicts(&runtime.validate_type(type_name, &vector["payload"]));
        if got != want {
            failures.push(format!("{name}: got {got:?}, want {want:?}"));
        }
        // The free function compiles its patterns each call and must agree.
        let again = verdicts(&validate_schema_type(
            &schema,
            type_name,
            &vector["payload"],
        ));
        if again != want {
            failures.push(format!("{name} (validate_schema_type): got {again:?}"));
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
