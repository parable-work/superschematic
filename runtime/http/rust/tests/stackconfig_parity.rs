//! The stackconfig parity vectors (D51, D57, sections 3.4 and 8.6 of
//! `docs/stack-model.md`): `runtime/http/testdata/stackconfig_parity.json`,
//! which the Go runtime writes (`go test ./stackconfig -run
//! TestWriteParityVectors -update` in `runtime/http/go`) and the Go,
//! TypeScript and Rust runtimes each read with their own readers. Each
//! vector holds an ir derived value, the variables `ir.DerivedVariables`
//! makes of it, edits to them, and the loaded value or the refusal's
//! messages. This reads each with the `_from` reader the vector names and
//! compares the loaded value in the vector's JSON shape, or each line of
//! the refusal by its beginning, as the file's comment describes. A
//! member, a reader or a shape the harness does not know fails it.

use serde::Deserialize;
use serde_json::{json, Map, Value};
use std::collections::{BTreeSet, HashMap};
use std::path::Path;
use superschematic_http_runtime::stackconfig::{
    load_bucket_from, load_callers_from, load_cors_from, load_database_from, load_service_from,
    Database, StackConfigError,
};

const VECTORS: &str = "../testdata/stackconfig_parity.json";

/// The readers a vector names, all of which the corpus must hold.
const READERS: [&str; 5] = ["database", "service", "bucket", "callers", "cors"];

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct ParityFile {
    #[allow(dead_code)]
    comment: String,
    vectors: Vec<Vector>,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Vector {
    name: String,
    reader: String,
    field: String,
    /// The ir derived value, which the variables already encode.
    #[allow(dead_code)]
    value: Value,
    variables: HashMap<String, String>,
    #[serde(default)]
    edits: HashMap<String, Option<String>>,
    want: Value,
    refusals: Option<Vec<String>>,
    /// Go's and Rust's refusal of a member only a Worker reads (D64), in
    /// place of `want` and `refusals`, which are TypeScript's.
    #[serde(default, rename = "workerOnly")]
    worker_only: Option<Vec<String>>,
}

/// The environment a vector's reader reads: its variables with its edits
/// applied, `null` unsetting one.
fn env_of(vector: &Vector) -> HashMap<String, String> {
    let mut env = vector.variables.clone();
    for (name, value) in &vector.edits {
        match value {
            Some(value) => env.insert(name.clone(), value.clone()),
            None => env.remove(name),
        };
    }
    env
}

/// A member of `object` named `name` when `value` is set.
fn put(object: &mut Map<String, Value>, name: &str, value: Option<&String>) {
    if let Some(value) = value {
        object.insert(name.to_string(), json!(value));
    }
}

/// Reads a vector's field with the reader it names, and returns the loaded
/// value in the vector's JSON shape (the TypeScript runtime's), or the
/// refusal. `Err(None)` is a reader the harness does not know.
fn read(vector: &Vector, env: &HashMap<String, String>) -> Result<Value, Option<StackConfigError>> {
    let field = vector.field.as_str();
    Ok(match vector.reader.as_str() {
        "database" => match load_database_from(field, env)? {
            Database::Url(url) => json!({ "url": url }),
            Database::CloudSql(c) => json!({
                "cloudSql": { "instance": c.instance, "database": c.database, "user": c.user }
            }),
        },
        "service" => {
            let service = load_service_from(field, env)?;
            let mut out = Map::new();
            out.insert("url".to_string(), json!(service.url));
            if let Some(c) = service.credential {
                let mut credential = Map::new();
                credential.insert("source".to_string(), json!(c.source.as_str()));
                put(&mut credential, "audience", c.audience.as_ref());
                put(&mut credential, "tokenFile", c.token_file.as_ref());
                put(&mut credential, "issuer", c.issuer.as_ref());
                put(&mut credential, "key", c.key.as_ref());
                if !c.headers.is_empty() {
                    credential.insert("headers".to_string(), json!(c.headers));
                }
                out.insert("credential".to_string(), Value::Object(credential));
            }
            Value::Object(out)
        }
        "bucket" => {
            let bucket = load_bucket_from(field, env)?;
            let mut out = Map::new();
            out.insert("name".to_string(), json!(bucket.name));
            put(&mut out, "endpoint", bucket.endpoint.as_ref());
            Value::Object(out)
        }
        "callers" => {
            // The config as the shared JSON writes it, without the leeway,
            // which the field does not set and the Rust config writes.
            let mut config = serde_json::to_value(load_callers_from(field, env)?).unwrap();
            let leeway = config.as_object_mut().unwrap().remove("leewaySeconds");
            assert_eq!(leeway, Some(json!(60)), "{}: the leeway", vector.name);
            without_default_subject_claim(config)
        }
        "cors" => json!({ "origins": load_cors_from(field, env)?.origins }),
        _ => return Err(None),
    })
}

/// A callers config without each issuer's subject claim of `sub`, the
/// claim an empty one means: the Rust config writes it, and Go's leaves it
/// out.
fn without_default_subject_claim(mut config: Value) -> Value {
    if let Some(issuers) = config["issuers"].as_array_mut() {
        for issuer in issuers.iter_mut().filter_map(Value::as_object_mut) {
            if issuer.get("subjectClaim") == Some(&json!("sub")) {
                issuer.remove("subjectClaim");
            }
        }
    }
    config
}

#[test]
fn the_shared_stackconfig_vectors() {
    let path = Path::new(env!("CARGO_MANIFEST_DIR")).join(VECTORS);
    let data = std::fs::read(&path).unwrap_or_else(|err| {
        panic!(
            "{}: {err}; the Go runtime writes it \
             (cd runtime/http/go && go test ./stackconfig -run TestWriteParityVectors -update)",
            path.display()
        )
    });
    let file: ParityFile = serde_json::from_slice(&data).expect("stackconfig_parity.json");
    assert!(!file.vectors.is_empty(), "no vectors");
    let names: BTreeSet<&str> = file.vectors.iter().map(|v| v.name.as_str()).collect();
    assert_eq!(names.len(), file.vectors.len(), "a vector name repeats");
    let mut failures = Vec::new();
    for vector in &file.vectors {
        let env = env_of(vector);
        let got = read(vector, &env);
        // Rust reads no member only a Worker reads (D64): such a vector's
        // refusal is its workerOnly.
        let refusals = vector.worker_only.as_ref().or(vector.refusals.as_ref());
        let failure = match (refusals, got) {
            (_, Err(None)) => Some(format!(
                "names the reader {:?}, which the harness does not know",
                vector.reader
            )),
            (None, Ok(loaded)) => {
                let want = if vector.reader == "callers" {
                    without_default_subject_claim(vector.want.clone())
                } else {
                    vector.want.clone()
                };
                (loaded != want).then(|| format!("loaded {loaded}\n  want   {want}"))
            }
            (None, Err(Some(err))) => Some(format!("refused: {err}")),
            (Some(_), Ok(loaded)) => Some(format!("loaded {loaded}; want a refusal")),
            (Some(refusals), Err(Some(err))) => {
                let text = err.to_string();
                let lines: Vec<&str> = text.split('\n').collect();
                if err.problems().len() != lines.len() {
                    Some(format!("its problems are not its lines: {err:?}"))
                } else if lines.len() != refusals.len()
                    || lines
                        .iter()
                        .zip(refusals)
                        .any(|(line, want)| !line.starts_with(want.as_str()))
                {
                    Some(format!(
                        "refusal:\n    {}\n  want lines beginning:\n    {}",
                        lines.join("\n    "),
                        refusals.join("\n    ")
                    ))
                } else if vector.worker_only.is_none() && !vector.want.is_null() {
                    Some("a refusal's want is not null".to_string())
                } else {
                    None
                }
            }
        };
        if let Some(failure) = failure {
            failures.push(format!("{}: {failure}", vector.name));
        }
    }
    let readers: BTreeSet<&str> = file.vectors.iter().map(|v| v.reader.as_str()).collect();
    let known: BTreeSet<&str> = READERS.into_iter().collect();
    if readers != known {
        failures.push(format!(
            "the vectors' readers are {readers:?}; want {known:?}"
        ));
    }
    assert!(
        failures.is_empty(),
        "{} of {} vectors differ:\n{}",
        failures.len(),
        file.vectors.len(),
        failures.join("\n")
    );
}
