//! The search parity vectors (D62): `runtime/http/testdata/search_parity.json`,
//! which the Go runtime writes (`go test ./search -run
//! TestWriteParityVectors -update` in `runtime/http/go`) and the Go,
//! TypeScript and Rust runtimes each answer with their search helpers:
//! here `search::terms`, `search::like_pattern`, `search::fold` and
//! `search::matches`. A member the harness does not know fails it.

use serde::Deserialize;
use std::collections::BTreeSet;
use std::path::Path;
use superschematic_http_runtime::search;

const VECTORS: &str = "../testdata/search_parity.json";

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct ParityFile {
    #[allow(dead_code)]
    comment: String,
    terms: Vec<TermsVector>,
    patterns: Vec<PatternVector>,
    folds: Vec<FoldVector>,
    matches: Vec<MatchVector>,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct TermsVector {
    name: String,
    text: String,
    terms: Vec<String>,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct PatternVector {
    name: String,
    term: String,
    pattern: String,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct FoldVector {
    name: String,
    text: String,
    fold: String,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct MatchVector {
    name: String,
    text: String,
    query: String,
    #[serde(rename = "match")]
    matched: bool,
}

fn corpus() -> ParityFile {
    let path = Path::new(env!("CARGO_MANIFEST_DIR")).join(VECTORS);
    let data =
        std::fs::read_to_string(&path).unwrap_or_else(|e| panic!("read {}: {e}", path.display()));
    serde_json::from_str(&data).expect("decode the search vectors")
}

fn distinct<'a>(names: impl Iterator<Item = &'a String>) -> bool {
    let names: Vec<_> = names.collect();
    !names.is_empty() && names.iter().collect::<BTreeSet<_>>().len() == names.len()
}

#[test]
fn every_section_has_vectors_with_distinct_names() {
    let file = corpus();
    assert!(distinct(file.terms.iter().map(|v| &v.name)));
    assert!(distinct(file.patterns.iter().map(|v| &v.name)));
    assert!(distinct(file.folds.iter().map(|v| &v.name)));
    assert!(distinct(file.matches.iter().map(|v| &v.name)));
}

#[test]
fn terms() {
    for v in corpus().terms {
        assert_eq!(search::terms(&v.text), v.terms, "terms: {}", v.name);
    }
}

#[test]
fn patterns() {
    for v in corpus().patterns {
        assert_eq!(
            search::like_pattern(&v.term),
            v.pattern,
            "patterns: {}",
            v.name
        );
    }
}

#[test]
fn folds() {
    for v in corpus().folds {
        assert_eq!(search::fold(&v.text), v.fold, "folds: {}", v.name);
    }
}

#[test]
fn matches() {
    for v in corpus().matches {
        assert_eq!(
            search::matches(&v.text, &search::terms(&v.query)),
            v.matched,
            "matches: {}",
            v.name
        );
    }
}
