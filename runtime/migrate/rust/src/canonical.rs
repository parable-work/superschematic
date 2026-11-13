//! Canonical JSON and the hashes taken over it (`runtime/migrate/README.md`,
//! "Canonical JSON and hashes"), byte for byte the Go runner's
//! `canonical.go`. `runtime/migrate/go/testdata/canonical.json` holds
//! vectors the compiler's function wrote, and `tests/canonical.rs` runs
//! them.

use std::collections::BTreeMap;

use sha2::{Digest, Sha256};

use crate::json::{self, JsonError, Value};

/// Re-encodes `raw` in canonical form: compact, object members sorted by
/// key, array order and number literals kept as written, and `<`, `>` and
/// `&` in strings escaped. It is what Go's `encoding/json` writes for a
/// value decoded with `UseNumber` into `any`. Only whitespace may follow
/// the value.
///
/// It is a copy of `ir.CanonicalJSON` in the compiler, which writes the
/// hashes this crate checks; the two must not drift.
pub fn canonical_json(raw: &[u8]) -> Result<String, JsonError> {
    Ok(canonical(&json::parse(raw)?))
}

/// The canonical form of a value the decoder read.
pub(crate) fn canonical(value: &Value) -> String {
    let mut out = String::new();
    json::write(value, &mut out);
    out
}

/// Returns the lowercase hex SHA-256 of `canonical`, which the caller has
/// already put in canonical form.
pub fn hash(canonical: &[u8]) -> String {
    let digest = Sha256::digest(canonical);
    let mut out = String::with_capacity(digest.len() * 2);
    for byte in digest {
        out.push_str(&format!("{byte:02x}"));
    }
    out
}

/// Returns the hash of a plan document: the SHA-256 of the canonical JSON
/// of the plan object without its `hash` member.
pub fn plan_hash(doc: &[u8]) -> Result<String, JsonError> {
    match json::parse(doc)? {
        Value::Object(plan) => Ok(object_hash(&plan)),
        _ => Err(JsonError::new("a plan is a JSON object", 0)),
    }
}

/// The hash of a plan object the decoder read.
pub(crate) fn object_hash(plan: &BTreeMap<String, Value>) -> String {
    let mut canonical = String::new();
    json::write_object(
        plan.iter().filter(|(key, _)| *key != "hash"),
        &mut canonical,
    );
    hash(canonical.as_bytes())
}

/// The hash a log row records for a step's statements: the SHA-256 of the
/// statements joined by `"\n;\n"`.
pub(crate) fn sql_hash(statements: &[String]) -> String {
    hash(statements.join("\n;\n").as_bytes())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn the_hash_of_no_sql_is_the_hash_of_nothing() {
        assert_eq!(
            sql_hash(&[]),
            "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
        );
        assert_eq!(
            sql_hash(&["a".to_string(), "b".to_string()]),
            hash(b"a\n;\nb")
        );
    }
}
