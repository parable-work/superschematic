//! What a generated Rust router's graph operations (D138) share: the routes
//! the loader adds for an API's `@graphOperations` sets, which run each
//! operation of a version graph through the graph's generated facade in
//! the database's Rust types, with no implementation method. The
//! generated `src/graphs.rs` of an API reads the root, the ref and the
//! commit a route names through the database's repositories, holds an
//! operation that writes a change set to the set's row rule, calls the
//! facade as the request's caller, and answers what it returns as the
//! API's types hold it. This module gives it, as the TypeScript runtime's
//! `graph-routes.ts` does:
//!
//! - the problems a refusal is answered with, by its code: `not_found`
//!   404; `no_actor` 403; `version_conflict`, `ref_sealed`,
//!   `primary_merge_only`, `schema_epoch`, `name_taken`,
//!   `nothing_to_commit`, `not_tagged`, `no_parent`, `merge_into_itself`
//!   and `root_mismatch` 409; and an input the engine or the core refuses
//!   422 (`invalid_tree` with the core's findings as details,
//!   `invalid_row`, `entity_not_found`, `invalid_arrangement`,
//!   `order_not_a_key`, `invalid_resolution`, `unmatched_resolution` and
//!   the rest). Any other error is the server's own;
//! - the members of each operation's body, read from the API's input type
//!   ([`BranchInput`] and the rest);
//! - a save's edits as the facade takes them, and the rows of a tree the
//!   facade returns as the API's row types hold them: each row's entity key
//!   and its content members, a to-one relation as its key.
//!
//! It names neither the database's types nor the version-graph engine, so
//! the runtime stays free of them: the generated code passes what it read
//! of them as JSON, and names a refusal by the engine's code of it.

use axum::http::StatusCode;
use serde::Deserialize;
use serde_json::{json, Map, Value};

use crate::auth::{covers, Principal};
use crate::error::ApiError;

/// The name of the primary line branch creates for a root that has none.
pub const PRIMARY_NAME: &str = "main";

/// A field of a member kind that holds a to-one relation: the row's member
/// that holds its key (`authorId`), the facade's field (`author`) and the
/// target's key (`id`).
#[derive(Clone, Debug, Deserialize)]
pub struct Relation {
    pub member: String,
    pub field: String,
    pub key: String,
}

/// One member kind of a served graph.
#[derive(Clone, Debug, Deserialize)]
pub struct Kind {
    /// The kind as the graph's descriptor names it (`line_item`).
    pub kind: String,
    /// The member of a tree and of the edits that holds its rows, as the
    /// API's types name it (`lineItem`).
    pub member: String,
    /// The members of its row beside `entityKey`: the content fields a
    /// typed upsert writes.
    pub fields: Vec<String>,
    /// The fields among them that hold a to-one relation's key.
    #[serde(default)]
    pub relations: Vec<Relation>,
}

impl Kind {
    fn relation(&self, member: &str) -> Option<&Relation> {
        self.relations.iter().find(|r| r.member == member)
    }
}

/// A version graph as a generated router serves it (D138).
#[derive(Clone, Debug, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct GraphSet {
    /// The graph's name, which a refusal names, and the root's type.
    pub graph: String,
    pub root: String,
    /// The graph's member kinds, in descriptor order.
    pub kinds: Vec<Kind>,
}

impl GraphSet {
    /// The set its generated literal holds, as JSON.
    pub fn parse(json: &str) -> Result<Self, serde_json::Error> {
        serde_json::from_str(json)
    }

    /// The member kind the descriptor names `kind`.
    pub fn kind(&self, kind: &str) -> Option<&Kind> {
        self.kinds.iter().find(|k| k.kind == kind)
    }
}

/// The status a refusal of `code` is answered with, and `None` for a code
/// no refusal has.
pub fn refusal_status(code: &str) -> Option<StatusCode> {
    Some(match code {
        "no_actor" => StatusCode::FORBIDDEN,
        "not_found" => StatusCode::NOT_FOUND,
        "version_conflict" | "ref_sealed" | "primary_merge_only" | "schema_epoch" | "name_taken"
        | "nothing_to_commit" | "not_tagged" | "no_parent" | "merge_into_itself"
        | "root_mismatch" => StatusCode::CONFLICT,
        "invalid_tree" | "entity_not_found" | "invalid_row" | "unknown_kind"
        | "duplicate_entity_key" | "order_out_of_range" | "invalid_resolution"
        | "unmatched_resolution" | "no_order_key" | "order_not_a_key"
        | "invalid_arrangement" => StatusCode::UNPROCESSABLE_ENTITY,
        _ => return None,
    })
}

/// The problem of the refusal `code`, with `message` as its detail.
pub fn problem(code: &str, message: impl Into<String>) -> ApiError {
    ApiError::new(
        refusal_status(code).unwrap_or(StatusCode::INTERNAL_SERVER_ERROR),
        code,
        message,
    )
}

/// The 404 `not_found` of what the request named and the root holds none
/// of: "Recipe not found", "ref not found".
pub fn not_found(message: impl Into<String>) -> ApiError {
    problem("not_found", message)
}

/// The 403 `no_actor` of a write whose caller has no subject that is a
/// UUID, which the graph records as the write's actor.
pub fn no_actor() -> ApiError {
    problem(
        "no_actor",
        "a write to a version graph records its caller, and the request's caller has no subject that is a UUID",
    )
}

/// The 409 `primary_merge_only` of a discard of a primary line: discarding
/// it would leave the root a line the next branch makes again, empty.
pub fn primary_not_discarded() -> ApiError {
    problem(
        "primary_merge_only",
        "the ref is a primary line, which is never discarded: discard a change set of it",
    )
}

/// The 409 `no_parent` of a merge of a primary line that names no ref to
/// merge into.
pub fn no_parent() -> ApiError {
    problem(
        "no_parent",
        "the ref is a primary line, which has no parent to merge into: name the ref to merge into",
    )
}

/// The 422 `unknown_kind` of an arrange of a kind the graph has none of.
pub fn unknown_kind(graph: &str, kind: &str) -> ApiError {
    problem("unknown_kind", format!("the {graph} graph has no kind {kind}"))
}

/// A refusal of the graph's engine or core as the problem of its code:
/// `code` is the engine's code of it, `message` what it says, and
/// `findings`, when set, the core's findings of an invalid tree, answered
/// as `details.findings`. Any other error, with no code or one no refusal
/// has, is the route's internal error, with none of its words.
pub fn refusal(code: Option<&str>, message: impl Into<String>, findings: Option<Value>) -> ApiError {
    match code.filter(|code| refusal_status(code).is_some()) {
        Some(code) => {
            let error = problem(code, message);
            match findings {
                Some(findings) => error.with_details(json!({ "findings": findings })),
                None => error,
            }
        }
        None => ApiError::internal("The graph operation failed"),
    }
}

/// Whether `principal` holds `permission`, by a grant that covers it.
pub fn holds(principal: Option<&Principal>, permission: &str) -> bool {
    principal.is_some_and(|p| p.permissions.iter().any(|granted| covers(granted, permission)))
}

/// The body of branch.
#[derive(Debug, Default, Deserialize)]
pub struct BranchInput {
    #[serde(default)]
    pub from: Option<String>,
    pub name: String,
}

/// The body of seal and discard.
#[derive(Debug, Default, Deserialize)]
pub struct VersionInput {
    pub version: i64,
}

/// The body of save: the edits stay as the API's edits type holds them,
/// for [`facade_edits`].
#[derive(Debug, Default, Deserialize)]
pub struct SaveInput {
    pub version: i64,
    #[serde(default)]
    pub edits: Value,
}

/// The body of arrange.
#[derive(Debug, Default, Deserialize)]
pub struct ArrangeInput {
    pub version: i64,
    pub kind: String,
    #[serde(default)]
    pub parent: Option<String>,
    #[serde(default)]
    pub keys: Vec<String>,
}

/// The body of commit.
#[derive(Debug, Default, Deserialize)]
pub struct CommitInput {
    pub version: i64,
    #[serde(default)]
    pub message: Option<String>,
    #[serde(default)]
    pub tag: Option<bool>,
}

/// One resolution of a merge's or a rebase's body.
#[derive(Debug, Default, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Resolution {
    pub kind: String,
    pub entity_key: String,
    pub path: String,
    #[serde(default)]
    pub take: Option<String>,
    /// The unit's value as JSON text, `"null"` among them.
    #[serde(default)]
    pub value: Option<String>,
}

impl Resolution {
    /// The resolution's value as the facade takes it, `None` for none; a
    /// text that is no JSON is the problem of an invalid resolution.
    pub fn value_json(&self) -> Result<Option<Value>, ApiError> {
        self.value
            .as_deref()
            .map(|text| {
                serde_json::from_str(text).map_err(|err| {
                    problem("invalid_resolution", format!("a resolution's value is no JSON: {err}"))
                })
            })
            .transpose()
    }
}

/// The body of merge.
#[derive(Debug, Default, Deserialize)]
pub struct MergeInput {
    #[serde(default)]
    pub into: Option<String>,
    pub version: i64,
    #[serde(default)]
    pub resolutions: Vec<Resolution>,
    #[serde(default)]
    pub message: Option<String>,
    #[serde(default)]
    pub tag: Option<bool>,
}

/// The body of rebase.
#[derive(Debug, Default, Deserialize)]
pub struct RebaseInput {
    pub version: i64,
    #[serde(default)]
    pub resolutions: Vec<Resolution>,
}

/// The body of revert.
#[derive(Debug, Default, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct RevertInput {
    pub version: i64,
    pub to_commit: String,
}

/// The body of release.
#[derive(Debug, Default, Deserialize)]
pub struct ReleaseInput {
    pub commit: String,
    pub version: i64,
}

/// The members of `input`, the API's input type, as `T`, a body of this
/// module's.
pub fn body<T: serde::de::DeserializeOwned>(input: &impl serde::Serialize) -> Result<T, ApiError> {
    serde_json::to_value(input)
        .and_then(serde_json::from_value)
        .map_err(|err| ApiError::internal(format!("graph operation: the body: {err}")))
}

/// A value of one generated type as another that holds the same members,
/// through their serde JSON.
pub fn recode<T: serde::de::DeserializeOwned>(value: Value) -> Result<T, ApiError> {
    serde_json::from_value(value)
        .map_err(|err| ApiError::internal(format!("graph operation: a value of the API's types: {err}")))
}

/// The edits of one kind as the facade takes them: each upsert a JSON
/// object of the kind's fields, with each relation's key as the object
/// that holds it, and the deletes and unsets by key.
#[derive(Debug, Default)]
pub struct KindEdits {
    pub upsert: Vec<Value>,
    pub delete: Vec<String>,
    pub unset: Vec<String>,
}

/// A save's edits, as the API's edits type holds them, as the graph's
/// facade takes them, by kind (`line_item`), each kind's upserts with each
/// relation's key as the object that holds it under the target's key.
pub fn facade_edits(set: &GraphSet, edits: &Value) -> Vec<(String, KindEdits)> {
    let mut out = Vec::new();
    for kind in &set.kinds {
        let Some(own) = edits.get(&kind.member).filter(|own| !own.is_null()) else {
            continue;
        };
        let keys = |name: &str| -> Vec<String> {
            own.get(name)
                .and_then(Value::as_array)
                .map(|keys| keys.iter().filter_map(|key| key.as_str().map(str::to_owned)).collect())
                .unwrap_or_default()
        };
        let upsert = own
            .get("upsert")
            .and_then(Value::as_array)
            .map(|rows| rows.iter().map(|row| facade_value(kind, row)).collect())
            .unwrap_or_default();
        out.push((
            kind.kind.clone(),
            KindEdits {
                upsert,
                delete: keys("delete"),
                unset: keys("unset"),
            },
        ));
    }
    out
}

/// The typed value the facade takes for a row the API's types hold: its
/// members, each relation's key as the object that holds it.
fn facade_value(kind: &Kind, row: &Value) -> Value {
    let mut out = Map::new();
    if let Some(key) = row.get("entityKey").filter(|key| !key.is_null()) {
        out.insert("entityKey".to_owned(), key.clone());
    }
    for field in &kind.fields {
        let value = row.get(field);
        if let Some(relation) = kind.relation(field) {
            let target = match value {
                None | Some(Value::Null) => Value::Null,
                Some(key) => json!({ relation.key.clone(): key }),
            };
            out.insert(relation.field.clone(), target);
        } else if let Some(value) = value {
            out.insert(field.clone(), value.clone());
        }
    }
    Value::Object(out)
}

/// The row the API's types hold of a typed value of the facade's, as its
/// serde JSON: its entity key and its content members.
fn api_row(kind: &Kind, value: &Value) -> Value {
    let mut out = Map::new();
    out.insert(
        "entityKey".to_owned(),
        value.get("entityKey").cloned().unwrap_or(Value::Null),
    );
    for field in &kind.fields {
        let member = match kind.relation(field) {
            Some(relation) => value
                .get(&relation.field)
                .and_then(|target| target.get(&relation.key))
                .cloned()
                .unwrap_or(Value::Null),
            None => value.get(field).cloned().unwrap_or(Value::Null),
        };
        out.insert(field.clone(), member);
    }
    Value::Object(out)
}

/// Each kind's rows of a tree the facade returned, as the API's row types
/// hold them: `rows` holds each kind's rows, as their serde JSON, under the
/// kind's member.
pub fn rows(set: &GraphSet, rows: &Map<String, Value>) -> Value {
    let mut out = Map::new();
    for kind in &set.kinds {
        let converted = rows
            .get(&kind.member)
            .and_then(Value::as_array)
            .map(|rows| rows.iter().map(|row| api_row(kind, row)).collect())
            .unwrap_or_default();
        out.insert(kind.member.clone(), Value::Array(converted));
    }
    Value::Object(out)
}

/// A tree the facade returned as the API's tree type holds it: each kind's
/// rows, its content hash and the core's findings, as their serde JSON.
pub fn tree(set: &GraphSet, kinds: &Map<String, Value>, content_hash: &str, findings: Value) -> Value {
    let mut out = rows(set, kinds);
    if let Value::Object(members) = &mut out {
        members.insert("contentHash".to_owned(), Value::String(content_hash.to_owned()));
        members.insert("findings".to_owned(), findings);
    }
    out
}

/// A value the engine returned as JSON, as the API holds it: its JSON
/// text, or `None` for none.
pub fn json_text(value: Option<&Value>) -> Option<String> {
    value.map(Value::to_string)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn set() -> GraphSet {
        GraphSet::parse(
            r#"{"graph":"Doc","root":"Doc","kinds":[
              {"kind":"line_item","member":"lineItem","fields":["text","authorId"],"relations":[{"member":"authorId","field":"author","key":"id"}]},
              {"kind":"note","member":"note","fields":["body"]}]}"#,
        )
        .unwrap()
    }

    #[test]
    fn a_refusal_is_the_problem_of_its_code() {
        for (code, status) in [
            ("not_found", 404),
            ("no_actor", 403),
            ("version_conflict", 409),
            ("root_mismatch", 409),
            ("invalid_tree", 422),
            ("unmatched_resolution", 422),
        ] {
            let error = refusal(Some(code), "words", None);
            assert_eq!(error.status.as_u16(), status, "{code}");
            assert_eq!(error.code, code);
            assert_eq!(error.message, "words");
        }
        let error = refusal(Some("invalid_tree"), "bad", Some(json!([{"code": "missing_parent"}])));
        assert_eq!(error.details.as_deref(), Some(&json!({"findings": [{"code": "missing_parent"}]})));
        assert_eq!(refusal(None, "storage", None).status.as_u16(), 500);
        assert_eq!(refusal(Some("walk_ceiling"), "deep", None).status.as_u16(), 500);
    }

    #[test]
    fn edits_and_rows_hold_a_relation_as_its_key() {
        let edits = facade_edits(
            &set(),
            &json!({"lineItem": {"upsert": [{"entityKey": "k1", "text": "a", "authorId": "u1"}, {"text": "b", "authorId": null}], "delete": ["k2"]}}),
        );
        assert_eq!(edits.len(), 1);
        let (kind, own) = &edits[0];
        assert_eq!(kind, "line_item");
        assert_eq!(
            own.upsert,
            vec![
                json!({"entityKey": "k1", "text": "a", "author": {"id": "u1"}}),
                json!({"text": "b", "author": null})
            ]
        );
        assert_eq!(own.delete, vec!["k2".to_owned()]);
        let mut kinds = Map::new();
        kinds.insert(
            "lineItem".to_owned(),
            json!([{"entityKey": "k1", "text": "a", "author": {"id": "u1"}, "_version": 3}]),
        );
        assert_eq!(
            tree(&set(), &kinds, "h", json!([])),
            json!({"lineItem": [{"entityKey": "k1", "text": "a", "authorId": "u1"}], "note": [], "contentHash": "h", "findings": []})
        );
    }
}
