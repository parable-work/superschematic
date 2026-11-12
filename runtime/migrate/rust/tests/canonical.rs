//! Canonical JSON and the hashes over it, against the vectors the
//! compiler's function wrote (`runtime/migrate/go/testdata/canonical.json`),
//! as the Go runner's `canonical_test.go` runs them.

use std::path::PathBuf;

use serde_json::Value;
use superschematic_migrate::{canonical_json, hash, plan_hash};

/// The runner's canonical JSON agrees with the compiler's on every vector:
/// key order at every depth, whitespace, `<`, `>` and `&` escaped,
/// non-ASCII as UTF-8, U+2028 and U+2029 escaped, number literals kept, the
/// last of two equal keys, and the inputs it refuses.
#[test]
fn canonical_json_vectors() {
    let path = PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../go/testdata/canonical.json");
    let raw = std::fs::read(&path).unwrap_or_else(|err| panic!("read {}: {err}", path.display()));
    let vectors: Vec<Value> = serde_json::from_slice(&raw).expect("the vectors are JSON");
    assert!(vectors.len() >= 20, "read {} vectors", vectors.len());
    for vector in &vectors {
        let name = vector["name"].as_str().expect("a vector has a name");
        let input = vector["input"].as_str().expect("a vector has an input");
        let canonical = canonical_json(input.as_bytes());
        if vector["error"] == Value::Bool(true) {
            assert!(
                canonical.is_err(),
                "{name}: canonical_json({input:?}) = {canonical:?}, want an error"
            );
            continue;
        }
        let canonical = canonical.unwrap_or_else(|err| panic!("{name}: {err}"));
        assert_eq!(
            canonical,
            vector["canonical"].as_str().unwrap_or_default(),
            "{name}"
        );
        assert_eq!(
            hash(canonical.as_bytes()),
            vector["sha256"].as_str().unwrap_or_default(),
            "{name}: the hash"
        );
        let again = canonical_json(canonical.as_bytes());
        assert_eq!(
            again.as_deref(),
            Ok(canonical.as_str()),
            "{name}: canonical JSON is not a fixed point"
        );
    }
}

/// A byte that is not UTF-8 becomes U+FFFD, written as UTF-8, as Go's
/// `encoding/json` writes it.
#[test]
fn canonical_json_invalid_utf8() {
    let canonical = canonical_json(b"\"a\xffb\"").expect("the document reads");
    assert_eq!(canonical.as_bytes(), b"\"a\xef\xbf\xbdb\"");
}

/// Only whitespace may follow the value, and the refusal says so.
#[test]
fn canonical_json_refuses_trailing_data() {
    for input in ["{} x", "{}]", "{} {}", "0 1", "01"] {
        let err = canonical_json(input.as_bytes()).expect_err("trailing data");
        assert_eq!(
            err.to_string(),
            "trailing data after JSON value",
            "{input:?}"
        );
    }
}

/// A plan's hash leaves out its `hash` member and nothing else, and does
/// not depend on the plan's encoding.
#[test]
fn plan_hash_leaves_out_the_hash() {
    let compact = r#"{"version":1,"hash":"anything","steps":[],"to":"b","from":"a"}"#;
    let indented =
        "{\n  \"from\": \"a\",\n  \"steps\": [ ],\n  \"to\": \"b\",\n  \"version\": 1\n}\n";
    let want = hash(br#"{"from":"a","steps":[],"to":"b","version":1}"#);
    for doc in [compact, indented] {
        assert_eq!(
            plan_hash(doc.as_bytes()).as_deref(),
            Ok(want.as_str()),
            "{doc:?}"
        );
    }
    assert!(plan_hash(b"[1]").is_err(), "plan_hash of an array");
}
