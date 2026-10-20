//! The page parity vectors (D60): `runtime/http/testdata/page_parity.json`,
//! which the Go runtime writes (`go test ./paging -run
//! TestWriteParityVectors -update` in `runtime/http/go`) and the Go,
//! TypeScript and Rust runtimes each run through their page reader and
//! builder. This reads each vector's query as the router does, through
//! axum's `Query` into `QueryValues`, then `PageSpec::query` and
//! `PageSpec::unbind`; encodes and decodes the cursors with `Cursor`;
//! builds the pages with `Page::from_rows`; and binds and unbinds sorted
//! cursors (D60, amended) with `Cursor::bind`, `Cursor::unbind` and
//! `Page::bind`. A member the harness does not know fails it.

use std::collections::BTreeSet;

use axum::extract::Query;
use http::Uri;
use serde::Deserialize;
use serde_json::{json, Value};
use superschematic_http_runtime::page::{Cursor, Page, PageRequest, PageSpec, Sort, SortSpec};
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
    unencodable: Vec<Unencodable>,
    #[serde(rename = "fromRows")]
    from_rows: Vec<FromRows>,
    bindings: Vec<Binding>,
    #[serde(rename = "refusedBindings")]
    refused_bindings: Vec<RefusedBinding>,
    unbindable: Vec<Unbindable>,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields, rename_all = "camelCase")]
struct Spec {
    default_limit: i64,
    max_limit: i64,
    sort: Option<SortVector>,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct SortVector {
    param: String,
    fields: Vec<String>,
    default: String,
}

impl Spec {
    /// The vector's spec as `Args::PAGE` carries one, its strings leaked
    /// for the `'static` a generated spec's literals have.
    fn page_spec(&self) -> PageSpec {
        let spec = PageSpec::new(self.default_limit, self.max_limit);
        let Some(sort) = &self.sort else {
            return spec;
        };
        let leak = |text: &str| -> &'static str { Box::leak(text.to_owned().into_boxed_str()) };
        let fields: Vec<&'static str> = sort.fields.iter().map(|field| leak(field)).collect();
        spec.sorted(SortSpec::new(
            leak(&sort.param),
            Box::leak(fields.into_boxed_slice()),
            leak(&sort.default),
        ))
    }
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Request {
    cursor: Option<String>,
    limit: i64,
    sort: Option<String>,
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
struct Unencodable {
    name: String,
    #[serde(rename = "keyJSON")]
    key_json: String,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct FromRows {
    name: String,
    rows: Vec<Value>,
    limit: i64,
    page: Value,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Binding {
    name: String,
    sort: String,
    cursor: String,
    bound: String,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct RefusedBinding {
    name: String,
    sort: String,
    cursor: String,
    problem: Value,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Unbindable {
    name: String,
    sort: String,
    cursor: String,
}

/// A cursor of any text, as a request's JSON may carry one.
fn cursor_of(text: &str) -> Cursor {
    serde_json::from_value(Value::String(text.to_owned())).unwrap()
}

fn sort_of(text: &str) -> Sort {
    Sort::parse(text).unwrap_or_else(|| panic!("{text} is no sort"))
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
    let sections: [Vec<&str>; 8] = [
        file.reads.iter().map(|v| v.name.as_str()).collect(),
        file.cursors.iter().map(|v| v.name.as_str()).collect(),
        file.refused.iter().map(|v| v.name.as_str()).collect(),
        file.unencodable.iter().map(|v| v.name.as_str()).collect(),
        file.from_rows.iter().map(|v| v.name.as_str()).collect(),
        file.bindings.iter().map(|v| v.name.as_str()).collect(),
        file.refused_bindings
            .iter()
            .map(|v| v.name.as_str())
            .collect(),
        file.unbindable.iter().map(|v| v.name.as_str()).collect(),
    ];
    for names in sections {
        assert!(!names.is_empty());
        assert_eq!(names.iter().collect::<BTreeSet<_>>().len(), names.len());
    }
}

#[test]
fn reads() {
    for vector in corpus().reads {
        let spec = vector.spec.page_spec();
        // The router's decode: the request as sent, then unbound.
        let got = spec
            .query(&query_of(&vector.query))
            .and_then(|request| spec.unbind(request));
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
                assert_eq!(
                    request.sort.as_ref().map(Sort::to_string),
                    want.sort,
                    "{}",
                    vector.name
                );
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
        let cursor = Cursor::encode(&key).unwrap_or_else(|err| panic!("{}: {err:?}", vector.name));
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

/// A key as a JSON value. serde_json reads no value nested 128 deep, which
/// an implementation can still build, so lists that deep are built.
fn key_of(json: &str) -> Value {
    serde_json::from_str(json).unwrap_or_else(|err| {
        let depth = json.len() / 2;
        assert_eq!(json, "[".repeat(depth) + &"]".repeat(depth), "{err}");
        (1..depth).fold(json!([]), |inner, _| json!([inner]))
    })
}

#[test]
fn unencodable() {
    for vector in corpus().unencodable {
        let key = key_of(&vector.key_json);
        let err = Cursor::encode(&key)
            .err()
            .unwrap_or_else(|| panic!("{}: encoded", vector.name));
        assert_eq!(err.status.as_u16(), 500, "{}", vector.name);
        let rows = vec![json!("first"), key, json!("after")];
        assert!(
            Page::from_rows(rows, 2, Clone::clone).is_err(),
            "{}",
            vector.name
        );
    }
}

#[test]
fn from_rows() {
    for vector in corpus().from_rows {
        let page = Page::from_rows(vector.rows, vector.limit, Clone::clone).unwrap();
        assert_eq!(
            serde_json::to_value(&page).unwrap(),
            vector.page,
            "{}",
            vector.name
        );
    }
}

#[test]
fn bindings() {
    for vector in corpus().bindings {
        let sort = sort_of(&vector.sort);
        let cursor = Cursor::parse(&vector.cursor).unwrap();
        let bound = cursor
            .bind(&sort)
            .unwrap_or_else(|err| panic!("{}: {err:?}", vector.name));
        assert_eq!(bound.as_str(), vector.bound, "{}", vector.name);
        assert_eq!(bound.unbind(&sort).unwrap(), cursor, "{}", vector.name);
        let request = PageRequest::first(1).sorted(sort);
        let page = Page::more(vec![1], cursor).bind(&request).unwrap();
        assert_eq!(page.next.unwrap().as_str(), vector.bound, "{}", vector.name);
    }
}

#[test]
fn refused_bindings() {
    for vector in corpus().refused_bindings {
        let err = cursor_of(&vector.cursor)
            .unbind(&sort_of(&vector.sort))
            .err()
            .unwrap_or_else(|| panic!("{}: accepted", vector.name));
        assert_eq!(problem(&err), vector.problem, "{}", vector.name);
    }
}

#[test]
fn unbindable() {
    for vector in corpus().unbindable {
        let sort = sort_of(&vector.sort);
        let cursor = cursor_of(&vector.cursor);
        let err = cursor
            .bind(&sort)
            .err()
            .unwrap_or_else(|| panic!("{}: bound", vector.name));
        assert_eq!(err.status.as_u16(), 500, "{}", vector.name);
        let request = PageRequest::first(1).sorted(sort);
        assert!(
            Page::more(vec![1], cursor).bind(&request).is_err(),
            "{}",
            vector.name
        );
    }
}
