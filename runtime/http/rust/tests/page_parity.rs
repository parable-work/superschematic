//! The page parity vectors (D60): `runtime/http/testdata/page_parity.json`,
//! which the Go runtime writes (`go test ./paging -run
//! TestWriteParityVectors -update` in `runtime/http/go`) and the Go,
//! TypeScript and Rust runtimes each run through their page reader and
//! builder. This reads each vector's query as the router does, through
//! axum's `Query` into `QueryValues`, then `PageSpec::query`; encodes and
//! decodes the cursors with `Cursor`; and builds the pages with
//! `Page::from_rows`. A member the harness does not know fails it.

use std::collections::BTreeSet;

use axum::extract::Query;
use http::Uri;
use serde::Deserialize;
use serde_json::{json, Value};
use superschematic_http_runtime::page::{Cursor, Page, PageSpec};
use superschematic_http_runtime::{ApiError, QueryValues};

const VECTORS: &str = "../testdata/page_parity.json";

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct ParityFile {
    #[allow(dead_code)]
    comment: String,
    reads: Vec<Read>,
    cursors: Vec<CursorVector>,
    refused: Vec<Refused>,
    #[serde(rename = "fromRows")]
    from_rows: Vec<FromRows>,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields, rename_all = "camelCase")]
struct Spec {
    default_limit: i64,
    max_limit: i64,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Request {
    cursor: Option<String>,
    limit: i64,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Read {
    name: String,
    spec: Spec,
    query: String,
    request: Option<Request>,
    problem: Option<Value>,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields, rename_all = "camelCase")]
struct CursorVector {
    name: String,
    #[serde(rename = "keyJSON")]
    key_json: String,
    cursor: String,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Refused {
    name: String,
    cursor: String,
    problem: Value,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct FromRows {
    name: String,
    rows: Vec<Value>,
    limit: i64,
    page: Value,
}

fn corpus() -> ParityFile {
    let path = std::path::Path::new(env!("CARGO_MANIFEST_DIR")).join(VECTORS);
    serde_json::from_str(&std::fs::read_to_string(path).unwrap()).unwrap()
}

/// The vector's view of a refusal: the problem the router writes for it.
fn problem(err: &ApiError) -> Value {
    json!({
        "status": err.status.as_u16(),
        "code": err.code,
        "detail": err.message,
        "details": err.details.as_deref().cloned().unwrap_or(Value::Null),
    })
}

/// A query string as the router reads one.
fn query_of(query: &str) -> QueryValues {
    let uri: Uri = format!("/items?{query}").parse().unwrap();
    let Query(pairs) = Query::<Vec<(String, String)>>::try_from_uri(&uri).unwrap();
    QueryValues::new(pairs)
}

#[test]
fn every_section_has_vectors_with_distinct_names() {
    let file = corpus();
    let sections: [Vec<&str>; 4] = [
        file.reads.iter().map(|v| v.name.as_str()).collect(),
        file.cursors.iter().map(|v| v.name.as_str()).collect(),
        file.refused.iter().map(|v| v.name.as_str()).collect(),
        file.from_rows.iter().map(|v| v.name.as_str()).collect(),
    ];
    for names in sections {
        assert!(!names.is_empty());
        assert_eq!(names.iter().collect::<BTreeSet<_>>().len(), names.len());
    }
}

#[test]
fn reads() {
    for vector in corpus().reads {
        let spec = PageSpec::new(vector.spec.default_limit, vector.spec.max_limit);
        let got = spec.query(&query_of(&vector.query));
        match (&vector.request, &vector.problem) {
            (Some(want), None) => {
                let request = got.unwrap_or_else(|err| panic!("{}: refused: {err:?}", vector.name));
                assert_eq!(
                    request.cursor.as_ref().map(|c| c.as_str().to_owned()),
                    want.cursor,
                    "{}",
                    vector.name
                );
                assert_eq!(request.limit, want.limit, "{}", vector.name);
                // The same request built in process passes the route's check.
                assert!(spec.check(&request).is_ok(), "{}", vector.name);
            }
            (None, Some(want)) => {
                let err = got
                    .err()
                    .unwrap_or_else(|| panic!("{}: accepted", vector.name));
                assert_eq!(&problem(&err), want, "{}", vector.name);
            }
            _ => panic!("{}: a read has a request or a problem", vector.name),
        }
    }
}

#[test]
fn cursors() {
    for vector in corpus().cursors {
        let key: Value = serde_json::from_str(&vector.key_json).unwrap();
        let cursor = Cursor::encode(&key);
        assert_eq!(cursor.as_str(), vector.cursor, "{}", vector.name);
        assert_eq!(cursor.decode::<Value>().unwrap(), key, "{}", vector.name);
    }
}

#[test]
fn refused() {
    for vector in corpus().refused {
        // A cursor the router would refuse never reaches an implementation
        // as a Cursor, but one built from its text is refused the same.
        let cursor: Cursor = serde_json::from_value(Value::String(vector.cursor.clone())).unwrap();
        let err = cursor
            .decode::<Value>()
            .err()
            .unwrap_or_else(|| panic!("{}: accepted", vector.name));
        assert_eq!(problem(&err), vector.problem, "{}", vector.name);
    }
}

#[test]
fn from_rows() {
    for vector in corpus().from_rows {
        let page = Page::from_rows(vector.rows, vector.limit, Clone::clone);
        assert_eq!(
            serde_json::to_value(&page).unwrap(),
            vector.page,
            "{}",
            vector.name
        );
    }
}
