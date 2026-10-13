//! The parts of the value store (D16, amended: a large value is stored once;
//! `src/values/store.ts`) the registry needs: reading the refs an instance's
//! row keeps, and putting a value back inline when an index comes to cover
//! its field. A top-level member stored by hash is kept in its row as a ref,
//!
//! ```json
//! { "$value": "<64 hex digits>", "bytes": <its canonical JSON's length> }
//! ```
//!
//! beside the JSON pointers of the members that hold one (`value_refs`).
//! The values live in `engine_payloads`, the default driver, and who holds
//! each is `engine_payload_holders`. Milestone 4b brings the rest: storing
//! a value by hash, the configured driver (D58), the cache, the reads by
//! hash and the sweep.

use std::collections::BTreeSet;

use serde_json::Value;

use superschematic_engine_storage::{Sql, SqlValue, Tx};

use crate::errors::{Error, Result};
use crate::json;

/// The member of a ref that names its value's hash.
pub(crate) const VALUE_REF_KEY: &str = "$value";

/// The hash a ref names, or `None` for a value that is no ref.
pub(crate) fn ref_hash(value: &Value) -> Option<&str> {
    let hash = value.as_object()?.get(VALUE_REF_KEY)?.as_str()?;
    let valid = hash.len() == 64
        && hash
            .bytes()
            .all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b));
    valid.then_some(hash)
}

/// The stored form of a list of pointers: JSON text, or `None` for none.
pub(crate) fn refs_text(refs: &[String]) -> Option<String> {
    if refs.is_empty() {
        return None;
    }
    let list: Vec<Value> = refs.iter().map(|at| Value::from(at.as_str())).collect();
    Some(json::stringify(&Value::Array(list)))
}

/// A `value_refs` column read: the pointers, or none.
pub(crate) fn refs_of(text: Option<&str>) -> Result<Vec<String>> {
    let Some(text) = text else {
        return Ok(Vec::new());
    };
    let value: Value = serde_json::from_str(text)
        .map_err(|error| Error::internal(format!("value_refs is not JSON: {error}")))?;
    Ok(value
        .as_array()
        .map(|refs| {
            refs.iter()
                .filter_map(Value::as_str)
                .map(str::to_owned)
                .collect()
        })
        .unwrap_or_default())
}

/// Who holds values: an instance's row, one of its events, or a row of a
/// behavior's tables.
pub(crate) struct ValueHolder<'a> {
    pub namespace: &'a str,
    pub schema: &'a str,
    /// `instance`, `event`, or the name of the behavior whose table holds
    /// it.
    pub holder: &'a str,
    pub id: &'a str,
    pub key: &'a str,
}

/// The value stored under a hash, parsed; `None` when the store has none.
fn read(tx: &Tx<'_>, hash: &str) -> Result<Option<Value>> {
    let row = tx.get(
        "SELECT value FROM engine_payloads WHERE hash = ?",
        &[SqlValue::from(hash)],
    )?;
    row.map(|row| {
        serde_json::from_str(row.text("value")?)
            .map_err(|error| Error::internal(format!("stored value {hash} is not JSON: {error}")))
    })
    .transpose()
}

/// A copy of a stored object with each ref its pointers name replaced by
/// its value.
pub(crate) fn fill(tx: &Tx<'_>, stored: &Value, refs: &[String]) -> Result<Value> {
    let mut filled = stored.clone();
    for at in refs {
        let tokens = json::pointer_tokens(at);
        let mut member = &mut filled;
        for token in &tokens {
            member = member
                .as_object_mut()
                .and_then(|object| object.get_mut(token))
                .ok_or_else(|| {
                    Error::internal(format!(
                        "value store: {at} does not name a member of the stored object"
                    ))
                })?;
        }
        let hash = ref_hash(member)
            .ok_or_else(|| Error::internal(format!("value store: {at} holds no ref")))?
            .to_owned();
        *member = read(tx, &hash)?.ok_or_else(|| {
            Error::internal(format!(
                "value store: no value {hash}, which {at} of a stored object refers to"
            ))
        })?;
    }
    Ok(filled)
}

/// Records that a holder references exactly these values: adds the new
/// ones, drops the ones it no longer holds, and removes a dropped value
/// nothing else holds, in the write's transaction.
pub(crate) fn hold(tx: &Tx<'_>, holder: &ValueHolder<'_>, hashes: &BTreeSet<String>) -> Result<()> {
    let key = [
        SqlValue::from(holder.namespace),
        SqlValue::from(holder.schema),
        SqlValue::from(holder.holder),
        SqlValue::from(holder.id),
        SqlValue::from(holder.key),
    ];
    let held: Vec<String> = tx
        .all(
            Sql::per(
                "SELECT hash FROM engine_payload_holders INDEXED BY engine_payload_holders_holder WHERE namespace = ? AND schema = ? AND holder = ? AND id = ? AND key = ?",
                "SELECT hash FROM engine_payload_holders WHERE namespace = ? AND schema = ? AND holder = ? AND id = ? AND key = ?",
            ),
            &key,
        )?
        .iter()
        .map(|row| row.text("hash").map(str::to_owned))
        .collect::<Result<_, _>>()?;
    for hash in hashes {
        if !held.contains(hash) {
            let mut params = vec![SqlValue::from(hash.as_str())];
            params.extend(key.iter().cloned());
            tx.run(
                "INSERT INTO engine_payload_holders (hash, namespace, schema, holder, id, key) VALUES (?, ?, ?, ?, ?, ?)",
                &params,
            )?;
        }
    }
    let dropped: Vec<&String> = held.iter().filter(|hash| !hashes.contains(*hash)).collect();
    for hash in &dropped {
        let mut params = vec![SqlValue::from(hash.as_str())];
        params.extend(key.iter().cloned());
        tx.run(
            "DELETE FROM engine_payload_holders WHERE hash = ? AND namespace = ? AND schema = ? AND holder = ? AND id = ? AND key = ?",
            &params,
        )?;
    }
    for hash in dropped.into_iter().collect::<BTreeSet<_>>() {
        let still_held = tx.get(
            "SELECT 1 AS held FROM engine_payload_holders WHERE hash = ? LIMIT 1",
            &[SqlValue::from(hash.as_str())],
        )?;
        if still_held.is_none() {
            tx.run(
                "DELETE FROM engine_payloads WHERE hash = ?",
                &[SqlValue::from(hash.as_str())],
            )?;
        }
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_ref_names_a_hash() {
        let hash = "a".repeat(64);
        assert_eq!(
            ref_hash(&serde_json::json!({"$value": hash, "bytes": 3})),
            Some(hash.as_str())
        );
        assert_eq!(ref_hash(&serde_json::json!({"$value": "short"})), None);
        assert_eq!(ref_hash(&serde_json::json!("text")), None);
        assert_eq!(refs_text(&[]), None);
        assert_eq!(refs_text(&["/slug".into()]).as_deref(), Some("[\"/slug\"]"));
        assert_eq!(refs_of(Some("[\"/slug\"]")).unwrap(), ["/slug"]);
        assert!(refs_of(None).unwrap().is_empty());
    }
}
