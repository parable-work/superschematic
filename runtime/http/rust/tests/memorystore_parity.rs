//! The memory store parity vectors (D75):
//! `runtime/http/testdata/memorystore_parity.json`, which ir writes
//! (`cd ir && go test -run TestWriteMemoryStoreParityVectors
//! -update-memorystore-parity`) and the Go, TypeScript and Rust runtimes
//! each run: `names`, a declaration and its parts' wire strings joined into
//! a name, with the store's prefix when the vector names a store, or
//! refused; and `rateLimit`, the shared rate limit's sliding window
//! (`windows`) and its counters' names (`keys`). A section the harness
//! does not know fails it.

use serde::Deserialize;
use serde_json::Value;
use std::time::Duration;
use superschematic_http_runtime::memorystore::{name, prefix, rate_limit_name, window_decision};

const VECTORS: &str = "../testdata/memorystore_parity.json";

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct ParityFile {
    #[allow(dead_code)]
    comment: String,
    names: Vec<NameVector>,
    #[serde(rename = "rateLimit")]
    rate_limit: RateLimit,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct NameVector {
    name: String,
    declaration: String,
    parts: Vec<String>,
    store: Option<String>,
    member: Option<String>,
    want: Option<String>,
    #[serde(rename = "fullName")]
    full_name: Option<String>,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct RateLimit {
    windows: Vec<WindowVector>,
    keys: Vec<KeyVector>,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields, rename_all = "camelCase")]
struct WindowVector {
    name: String,
    limit: i64,
    window_ms: i64,
    now_ms: i64,
    prev_count: i64,
    curr_count: i64,
    want: WindowWant,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields, rename_all = "camelCase")]
struct WindowWant {
    allowed: bool,
    estimate: f64,
    curr_window_start: i64,
    prev_window_start: i64,
    increment_ttl_ms: u64,
    retry_after_seconds: u64,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields, rename_all = "camelCase")]
struct KeyVector {
    name: String,
    service: String,
    operation: String,
    window_start: i64,
    client_key: String,
    want: Option<String>,
}

fn vectors() -> ParityFile {
    let path = std::path::Path::new(env!("CARGO_MANIFEST_DIR")).join(VECTORS);
    let data = std::fs::read_to_string(&path)
        .unwrap_or_else(|err| panic!("read {}: {err}", path.display()));
    serde_json::from_str(&data).unwrap_or_else(|err| panic!("parse {}: {err}", path.display()))
}

#[test]
fn names() {
    let file = vectors();
    assert!(!file.names.is_empty());
    for v in &file.names {
        let parts: Vec<&str> = v.parts.iter().map(String::as_str).collect();
        let joined = name(&v.declaration, &parts).ok();
        match &v.store {
            None => {
                assert_eq!(joined, v.want, "{}", v.name);
                assert!(
                    v.full_name.is_none(),
                    "{}: a fullName with no store",
                    v.name
                );
            }
            Some(store) => match prefix(store, v.member.as_deref()) {
                Err(_) => {
                    assert!(
                        v.want.is_none() && v.full_name.is_none(),
                        "{}: the prefix is refused",
                        v.name
                    );
                }
                Ok(prefix) => {
                    assert_eq!(joined, v.want, "{}", v.name);
                    let full = joined.map(|name| format!("{prefix}:{name}"));
                    assert_eq!(full, v.full_name, "{}", v.name);
                }
            },
        }
    }
}

#[test]
fn rate_limit_windows() {
    let file = vectors();
    assert!(!file.rate_limit.windows.is_empty());
    for v in &file.rate_limit.windows {
        let got = window_decision(v.limit, v.window_ms, v.now_ms, v.prev_count, v.curr_count);
        let want = &v.want;
        assert_eq!(got.allowed, want.allowed, "{}: allowed", v.name);
        assert!(
            (got.estimate - want.estimate).abs() < 1e-9,
            "{}: estimate {} != {}",
            v.name,
            got.estimate,
            want.estimate
        );
        assert_eq!(got.curr_window_start, want.curr_window_start, "{}", v.name);
        assert_eq!(got.prev_window_start, want.prev_window_start, "{}", v.name);
        assert_eq!(
            got.increment_ttl.unwrap_or(Duration::ZERO),
            Duration::from_millis(want.increment_ttl_ms),
            "{}: increment TTL",
            v.name
        );
        assert_eq!(
            got.retry_after_seconds.unwrap_or(0),
            want.retry_after_seconds,
            "{}: Retry-After",
            v.name
        );
    }
}

#[test]
fn rate_limit_keys() {
    let file = vectors();
    assert!(!file.rate_limit.keys.is_empty());
    for v in &file.rate_limit.keys {
        let got = rate_limit_name(&v.service, &v.operation, v.window_start, &v.client_key).ok();
        assert_eq!(got, v.want, "{}", v.name);
    }
}

/// The file holds no section beside the three the harness reads.
#[test]
fn the_file_holds_only_known_sections() {
    let path = std::path::Path::new(env!("CARGO_MANIFEST_DIR")).join(VECTORS);
    let raw: Value = serde_json::from_str(&std::fs::read_to_string(path).unwrap()).unwrap();
    let keys: Vec<&str> = raw
        .as_object()
        .unwrap()
        .keys()
        .map(String::as_str)
        .collect();
    assert_eq!(keys.len(), 3, "{keys:?}");
}
