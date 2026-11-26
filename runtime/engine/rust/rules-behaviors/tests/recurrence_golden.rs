//! Recurrence's golden expansions (D152): each case of
//! `runtime/engine/testdata/rules/recurrence/expand.golden.json` expanded
//! by the core and held to the output the file writes, a refused input to
//! `{"refused": reason}`. TypeScript and Go hold their bindings to the same
//! file, so the three return the same dates. With
//! `RECURRENCE_GOLDEN_UPDATE=1` the test writes the outputs instead.

use std::path::PathBuf;

use serde_json::{json, Map, Value};
use superschematic_rules_behaviors::recurrence::expand_json;

fn golden_path() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("../../testdata/rules/recurrence/expand.golden.json")
}

/// What a case's input expands to, as the file writes it.
fn answer(input: &Value) -> Value {
    match expand_json(input) {
        Ok(output) => output,
        Err(reason) => json!({ "refused": reason }),
    }
}

#[test]
fn every_case_expands_as_the_golden_file_writes() {
    let path = golden_path();
    let text = std::fs::read_to_string(&path)
        .unwrap_or_else(|error| panic!("{}: {error}", path.display()));
    let mut file: Value = serde_json::from_str(&text).unwrap();
    let update = std::env::var_os("RECURRENCE_GOLDEN_UPDATE").is_some();
    let mut failures = Vec::new();
    let cases = file["cases"].as_array_mut().expect("cases");
    assert!(cases.len() >= 10, "{} cases", cases.len());
    for case in cases.iter_mut() {
        let got = answer(&case["input"]);
        if update {
            let mut out = Map::new();
            for (key, value) in case.as_object().unwrap() {
                if key != "output" {
                    out.insert(key.clone(), value.clone());
                }
            }
            out.insert("output".into(), got);
            *case = Value::Object(out);
        } else if case.get("output") != Some(&got) {
            failures.push(format!(
                "{}:\n  file: {}\n  core: {}",
                case["name"],
                case.get("output")
                    .map(Value::to_string)
                    .unwrap_or_else(|| "none".into()),
                got
            ));
        }
    }
    if update {
        std::fs::write(&path, serde_json::to_string_pretty(&file).unwrap() + "\n").unwrap();
        return;
    }
    assert!(failures.is_empty(), "{}", failures.join("\n"));
}

#[test]
fn the_golden_file_covers_what_d152_asks() {
    let file: Value =
        serde_json::from_str(&std::fs::read_to_string(golden_path()).unwrap()).unwrap();
    let cases = file["cases"].as_array().unwrap();
    for anchor in ["calendar", "afterDone", "relative"] {
        // Each anchor runs two years out: a window of two years with its
        // occurrences across it, the last in the window's final quarter.
        let spans = cases
            .iter()
            .filter(|case| case["input"]["anchor"] == anchor)
            .any(|case| {
                let (Some(from), Some(to)) =
                    (case["input"]["from"].as_i64(), case["input"]["to"].as_i64())
                else {
                    return false;
                };
                let dues: Vec<i64> = case["output"]["occurrences"]
                    .as_array()
                    .map(|all| all.iter().filter_map(|o| o["dueAt"].as_i64()).collect())
                    .unwrap_or_default();
                to - from >= 730 * 86_400_000
                    && dues.len() > 1
                    && dues[dues.len() - 1] >= to - (to - from) / 4
            });
        assert!(spans, "no case expands {anchor} two years out");
    }
    let shifted_by_holiday = cases.iter().any(|case| {
        case["output"]["occurrences"]
            .as_array()
            .is_some_and(|all| all.iter().any(|o| o["shifted"] == "holiday"))
    });
    assert!(shifted_by_holiday, "no case moves a due date for a holiday");
}
