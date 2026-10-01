//! Runs every vector in runtime/versiongraph/testdata/canonical through the
//! canonical rules, and checks each vector's Postgres rendering against a
//! real Postgres when SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL names
//! one.

mod support;

use std::collections::{BTreeMap, BTreeSet, HashMap};
use std::fs;

use serde::Deserialize;
use serde_json::value::RawValue;
use superschematic_versiongraph_engine::canonical;

/// One value vector: a value of a class as Postgres renders it (`postgres`,
/// from the SQL in `sql` under `time_zone`), and its canonical JSON, or the
/// reason its rule refuses it.
#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
struct ValueCase {
    name: String,
    class: String,
    sql: String,
    #[serde(default, rename = "timeZone")]
    time_zone: String,
    postgres: String,
    #[serde(default)]
    canonical: String,
    #[serde(default)]
    error: String,
}

/// One row vector: a row as `to_jsonb` renders it, the classes of its
/// columns, and its canonical row.
#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
struct RowCase {
    name: String,
    columns: BTreeMap<String, String>,
    sql: String,
    #[serde(default, rename = "timeZone")]
    time_zone: String,
    postgres: String,
    #[serde(default)]
    canonical: String,
    #[serde(default)]
    error: String,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Document {
    #[serde(default)]
    cases: Vec<ValueCase>,
    #[serde(default)]
    rows: Vec<RowCase>,
}

fn vectors() -> (Vec<ValueCase>, Vec<RowCase>) {
    let dir = support::testdata().join("canonical");
    let mut files: Vec<_> = fs::read_dir(&dir)
        .unwrap_or_else(|e| panic!("read {}: {e}", dir.display()))
        .map(|entry| entry.expect("directory entry").path())
        .filter(|path| path.extension().is_some_and(|ext| ext == "json"))
        .collect();
    files.sort();
    assert!(!files.is_empty(), "no canonical vectors found");
    let (mut values, mut rows) = (Vec::new(), Vec::new());
    for file in files {
        let text = fs::read_to_string(&file).expect("read a vector file");
        let document: Document =
            serde_json::from_str(&text).unwrap_or_else(|e| panic!("{}: {e}", file.display()));
        assert!(
            !document.cases.is_empty() || !document.rows.is_empty(),
            "{} has no cases",
            file.display()
        );
        let stem = file
            .file_stem()
            .and_then(|s| s.to_str())
            .expect("a file name");
        for c in &document.cases {
            let element = c.class.trim_end_matches("[]");
            assert_eq!(
                element,
                stem,
                "{}: case {:?} has class {}, which belongs in {element}.json",
                file.display(),
                c.name,
                c.class
            );
        }
        values.extend(document.cases);
        rows.extend(document.rows);
    }
    (values, rows)
}

/// Every value vector: the canonical JSON matches byte for byte, a refused
/// value is refused, and a canonical value comes back unchanged.
#[test]
fn value_vectors() {
    let (values, _) = vectors();
    let mut failures = Vec::new();
    for c in &values {
        let got = canonical::postgres(&c.class, &c.postgres);
        match (&got, c.error.is_empty()) {
            (Err(_), false) => {}
            (Ok(got), false) => failures.push(format!(
                "{}/{}: {} gave {got}, want it refused ({})",
                c.class, c.name, c.postgres, c.error
            )),
            (Err(error), true) => {
                failures.push(format!("{}/{}: {}: {error}", c.class, c.name, c.postgres))
            }
            (Ok(got), true) if *got != c.canonical => failures.push(format!(
                "{}/{}: {}\n got: {got}\nwant: {}",
                c.class, c.name, c.postgres, c.canonical
            )),
            (Ok(got), true) => match canonical::postgres(&c.class, got) {
                Ok(again) if again == c.canonical => {}
                other => failures.push(format!(
                    "{}/{}: the canonical form is not a fixed point: {got} gives {other:?}",
                    c.class, c.name
                )),
            },
        }
    }
    assert!(
        failures.is_empty(),
        "{} of {} value vectors fail:\n{}",
        failures.len(),
        values.len(),
        failures.join("\n")
    );
}

/// Fails when an element class, its list or null, has no vector, or a class
/// has no refused value; and when a class a list of lists holds has no
/// vector of one.
#[test]
fn every_class_has_vectors() {
    let (values, _) = vectors();
    let mut seen = BTreeSet::new();
    let mut nulls = BTreeSet::new();
    let mut refused = BTreeSet::new();
    for c in &values {
        seen.insert(c.class.as_str());
        let element = c.class.trim_end_matches("[]");
        if c.postgres == "null" {
            nulls.insert(element);
        }
        if !c.error.is_empty() {
            refused.insert(element);
        }
    }
    for element in canonical::CLASSES {
        assert!(seen.contains(element), "class {element} needs a vector");
        assert!(
            seen.contains(format!("{element}[]").as_str()),
            "class {element}[] needs a vector"
        );
        assert!(
            nulls.contains(element),
            "class {element} needs a null vector"
        );
        assert!(
            refused.contains(element),
            "class {element} needs a refused vector"
        );
    }
    for element in [
        canonical::STRING,
        canonical::INTEGER,
        canonical::NUMBER,
        canonical::UUID,
        canonical::DATE_TIME,
        canonical::DURATION,
        canonical::JSON,
    ] {
        assert!(
            seen.contains(format!("{element}[][]").as_str()),
            "class {element}[][] needs a vector"
        );
    }
}

/// Every row vector through the row rule.
#[test]
fn row_vectors() {
    let (_, rows) = vectors();
    assert!(!rows.is_empty(), "no row vectors");
    for c in &rows {
        let got = canonical::postgres_row(&c.columns, &c.postgres);
        if !c.error.is_empty() {
            assert!(
                got.is_err(),
                "{}: gave {got:?}, want it refused ({})",
                c.name,
                c.error
            );
            continue;
        }
        assert_eq!(got.as_deref(), Ok(c.canonical.as_str()), "{}", c.name);
    }
}

/// Every vector's `postgres` member against a real Postgres: each value's
/// SQL, in a row, under the vector's session time zone (UTC when it names
/// none), renders as the vector says, and each row's `to_jsonb` does. The
/// rendering then goes through the row rule, so the rules run on what
/// Postgres returned, not only on text written into a file.
#[tokio::test]
async fn vectors_render_as_postgres_renders_them() {
    let Some(dsn) = support::database("vectors_render_as_postgres_renders_them") else {
        return;
    };
    let client = support::connect(&dsn, None).await;
    let render = |time_zone: String, query: String| {
        let client = &client;
        async move {
            let time_zone = if time_zone.is_empty() {
                "UTC".to_owned()
            } else {
                time_zone
            };
            client
                .execute("SELECT set_config('TimeZone', $1, false)", &[&time_zone])
                .await
                .unwrap_or_else(|e| panic!("set the time zone to {time_zone}: {e}"));
            let row = client
                .query_one(query.as_str(), &[])
                .await
                .unwrap_or_else(|e| panic!("{query}: {e}"));
            row.get::<_, String>(0)
        }
    };
    let (values, rows) = vectors();
    let mut checked = 0;
    for c in &values {
        let got = render(
            c.time_zone.clone(),
            format!("SELECT to_jsonb(t)::text FROM (SELECT {} AS v) AS t", c.sql),
        )
        .await;
        let members: HashMap<String, Box<RawValue>> =
            serde_json::from_str(&got).unwrap_or_else(|e| panic!("{got}: {e}"));
        assert_eq!(
            members["v"].get(),
            c.postgres,
            "{}/{}: Postgres renders {} as {}, the vector says {}",
            c.class,
            c.name,
            c.sql,
            members["v"].get(),
            c.postgres
        );
        let columns: BTreeMap<String, String> = [("v".to_owned(), c.class.clone())].into();
        let row = canonical::postgres_row(&columns, &got);
        if c.error.is_empty() {
            assert_eq!(
                row,
                Ok(format!("{{\"v\":{}}}", c.canonical)),
                "{}/{}",
                c.class,
                c.name
            );
        } else {
            assert!(
                row.is_err(),
                "{}/{}: accepted {got}, which the vector refuses",
                c.class,
                c.name
            );
        }
        checked += 1;
    }
    for c in &rows {
        let got = render(
            c.time_zone.clone(),
            format!("SELECT to_jsonb(t)::text FROM (SELECT {}) AS t", c.sql),
        )
        .await;
        assert_eq!(
            got, c.postgres,
            "row {}: Postgres renders it otherwise",
            c.name
        );
        checked += 1;
    }
    eprintln!(
        "vectors_render_as_postgres_renders_them: {checked} renderings checked against Postgres"
    );
}
