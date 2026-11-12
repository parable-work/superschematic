//! The filter parity vectors (D106):
//! `runtime/http/testdata/filter_parity.json`, which the Go runtime writes
//! (`go test ./filterparse -run TestWriteParityVectors -update` in
//! `runtime/http/go`) and the Go, TypeScript and Rust runtimes each run
//! through their filter reader. This reads each `reads` vector's query as
//! the router does, through axum's `Query` into `QueryValues`, then
//! `FilterSpec::query`, and compares the filters it answers, or the
//! problem it refuses with, as JSON values. The `queries` vectors are a
//! writer's, an SDK's; this reads each one's pairs back under a spec that
//! names their fields, which gives the filters. A member the harness does
//! not know fails it.

use std::collections::BTreeSet;

use axum::extract::Query;
use http::Uri;
use serde::Deserialize;
use serde_json::{json, Value};
use superschematic_http_runtime::filter::{FieldSpec, Filter, FilterSpec, Operator};
use superschematic_http_runtime::{ApiError, QueryValues};

const VECTORS: &str = "../testdata/filter_parity.json";

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct ParityFile {
    #[allow(dead_code)]
    comment: String,
    reads: Vec<Read>,
    queries: Vec<Written>,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Spec {
    fields: Vec<Field>,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Field {
    name: String,
    operators: Vec<Operator>,
}

impl Spec {
    /// The vector's spec as `Args::FILTER` carries one, its strings and
    /// lists leaked for the `'static` a generated spec's literals have.
    fn filter_spec(&self) -> FilterSpec {
        let fields: Vec<FieldSpec> = self
            .fields
            .iter()
            .map(|field| {
                let name: &'static str = Box::leak(field.name.clone().into_boxed_str());
                let operators: &'static [Operator] =
                    Box::leak(field.operators.clone().into_boxed_slice());
                FieldSpec::new(name, operators)
            })
            .collect();
        FilterSpec::new(Box::leak(fields.into_boxed_slice()))
    }
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Read {
    name: String,
    spec: Spec,
    query: String,
    filters: Option<Vec<Filter>>,
    problem: Option<Value>,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Written {
    name: String,
    filters: Vec<Filter>,
    pairs: Vec<(String, String)>,
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
    let sections: [Vec<&str>; 2] = [
        file.reads.iter().map(|v| v.name.as_str()).collect(),
        file.queries.iter().map(|v| v.name.as_str()).collect(),
    ];
    for names in sections {
        assert!(!names.is_empty());
        assert_eq!(names.iter().collect::<BTreeSet<_>>().len(), names.len());
    }
}

#[test]
fn reads() {
    let mut refused = 0;
    for vector in corpus().reads {
        let got = vector.spec.filter_spec().query(&query_of(&vector.query));
        match (&vector.filters, &vector.problem) {
            (Some(_), Some(_)) => panic!("{}: a read has filters or a problem", vector.name),
            (want, None) => {
                // A vector of no filter leaves the member out.
                let filters = got.unwrap_or_else(|err| panic!("{}: refused: {err:?}", vector.name));
                assert_eq!(&filters, want.as_deref().unwrap_or(&[]), "{}", vector.name);
            }
            (None, Some(want)) => {
                let err = got
                    .err()
                    .unwrap_or_else(|| panic!("{}: accepted", vector.name));
                assert_eq!(&problem(&err), want, "{}", vector.name);
                refused += 1;
            }
        }
    }
    assert!(refused > 0, "no vector is refused");
}

#[test]
fn written_pairs_read_back() {
    for vector in corpus().queries {
        // A spec that names the filters' fields, each with every operator.
        let mut names: Vec<&str> = Vec::new();
        for filter in &vector.filters {
            if !names.contains(&filter.field.as_str()) {
                names.push(&filter.field);
            }
        }
        let spec = Spec {
            fields: names
                .iter()
                .map(|name| Field {
                    name: (*name).to_owned(),
                    operators: Operator::ALL.to_vec(),
                })
                .collect(),
        };
        let read = spec
            .filter_spec()
            .query(&QueryValues::new(vector.pairs.clone()))
            .unwrap_or_else(|err| panic!("{}: refused: {err:?}", vector.name));
        // The reader answers them in the spec's field order, then the
        // operators'; the vector's are in the order they were written.
        let mut want = vector.filters.clone();
        want.retain(|filter| !filter.values.is_empty());
        want.sort_by_key(|filter| {
            let field = names.iter().position(|name| *name == filter.field);
            (field, filter.operator)
        });
        assert_eq!(read, want, "{}", vector.name);
    }
}
