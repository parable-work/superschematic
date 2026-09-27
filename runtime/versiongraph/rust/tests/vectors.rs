//! Runs every vector in runtime/versiongraph/testdata/vectors. Each file is
//! `{"name", "op", "input", "expect"}`; `expect` is the output document, or
//! `{"error": {...}}` for a refused input. `UPDATE_VECTORS=1 cargo test`
//! rewrites every `expect` from the core; review the diff.

use std::fs;
use std::path::{Path, PathBuf};

use serde_json::Value;
use superschematic_versiongraph::{run_json, Op};

fn vector_files() -> Vec<PathBuf> {
    let dir = Path::new(env!("CARGO_MANIFEST_DIR")).join("../testdata/vectors");
    let mut files: Vec<PathBuf> = fs::read_dir(&dir)
        .unwrap_or_else(|e| panic!("read {}: {e}", dir.display()))
        .map(|entry| entry.expect("directory entry").path())
        .filter(|path| path.extension().is_some_and(|ext| ext == "json"))
        .collect();
    files.sort();
    files
}

#[test]
fn vectors() {
    let update = std::env::var("UPDATE_VECTORS").is_ok_and(|v| v == "1");
    let files = vector_files();
    assert!(!files.is_empty(), "no vectors found");
    let mut failures = Vec::new();
    for path in files {
        let text = fs::read_to_string(&path).expect("read vector");
        let mut vector: Value = serde_json::from_str(&text).expect("vector is JSON");
        let name = path
            .file_name()
            .unwrap_or_default()
            .to_string_lossy()
            .into_owned();
        assert_eq!(
            vector["name"].as_str(),
            Some(name.trim_end_matches(".json")),
            "{name}: the name member matches the file name"
        );
        let op = vector["op"]
            .as_str()
            .and_then(Op::from_name)
            .unwrap_or_else(|| panic!("{name}: unknown op"));
        let input = vector["input"].to_string();
        let (output, _) = run_json(op, input.as_bytes());
        let output: Value = serde_json::from_str(&output).expect("output is JSON");
        if update {
            vector["expect"] = output;
            let rendered = render(&vector);
            if rendered != text {
                fs::write(&path, rendered).expect("write vector");
            }
        } else if vector["expect"] != output {
            failures.push(format!(
                "{name}\n  expect: {}\n  got:    {output}",
                vector["expect"]
            ));
        }
    }
    assert!(
        failures.is_empty(),
        "{} vector(s) differ:\n{}",
        failures.len(),
        failures.join("\n")
    );
}

/// A vector file with its members in reading order: name, op, input, expect.
fn render(vector: &Value) -> String {
    let member = |key: &str| {
        serde_json::to_string_pretty(&vector[key])
            .expect("render member")
            .replace('\n', "\n  ")
    };
    format!(
        "{{\n  \"name\": {},\n  \"op\": {},\n  \"input\": {},\n  \"expect\": {}\n}}\n",
        member("name"),
        member("op"),
        member("input"),
        member("expect")
    )
}
