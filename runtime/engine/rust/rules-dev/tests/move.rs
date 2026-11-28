//! `superschematic-dev move` (D117): a development file's schemas publish
//! to an empty runtime database, its rows with `--rows` replayed there as
//! creates with their ids, and the file is left as it was. It runs the
//! `superschematic` and `superschematic-migrate` binaries a runtime
//! environment carries: `$SUPERSCHEMATIC_BIN` and
//! `$SUPERSCHEMATIC_MIGRATE_BIN`, else the repository's `bin/`, which `make
//! cli` builds. Without them the test says so and passes.

#![cfg(feature = "server")]

use std::path::{Path, PathBuf};
use std::process::{Command, Output};

use serde_json::{json, Value};
use superschematic_rules::Caller;
use superschematic_rules_dev::Dev;
use superschematic_rules_handler::Request;
use superschematic_rules_runtime::{Binaries, Runtime};

fn binaries() -> Option<Binaries> {
    let repo = PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../../../../bin");
    let find = |var: &str, name: &str| {
        std::env::var_os(var)
            .filter(|value| !value.is_empty())
            .map(PathBuf::from)
            .or_else(|| Some(repo.join(name)).filter(|path| path.exists()))
    };
    Some(Binaries {
        superschematic: find("SUPERSCHEMATIC_BIN", "superschematic")?,
        migrate: find("SUPERSCHEMATIC_MIGRATE_BIN", "superschematic-migrate")?,
    })
}

fn dir() -> PathBuf {
    let dir = Path::new(env!("CARGO_TARGET_TMPDIR"))
        .join("rules-dev-move")
        .join(std::process::id().to_string());
    let _ = std::fs::remove_dir_all(&dir);
    std::fs::create_dir_all(&dir).expect("the test's directory");
    dir
}

fn notes() -> Value {
    let path = PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("../../../../internal/migrateplan/testdata/development/notes.schema.json");
    serde_json::from_str(&std::fs::read_to_string(path).expect("the notes schema"))
        .expect("a schema document")
}

fn dev_move(binaries: &Binaries, args: &[&str]) -> Output {
    Command::new(env!("CARGO_BIN_EXE_superschematic-dev"))
        .arg("move")
        .args(args)
        .env("SUPERSCHEMATIC_BIN", &binaries.superschematic)
        .env("SUPERSCHEMATIC_MIGRATE_BIN", &binaries.migrate)
        .output()
        .expect("superschematic-dev runs")
}

fn ids(runtime: &mut Runtime) -> Vec<String> {
    let me = Caller::new("alice", Vec::<String>::new());
    let listed = runtime
        .answer(
            &me,
            &json!({"kind": "list", "type": "Note", "sort": [{"field": "title"}]}),
        )
        .expect("a list");
    listed["items"]
        .as_array()
        .expect("items")
        .iter()
        .filter_map(|item| item["id"].as_str().map(str::to_owned))
        .collect()
}

#[test]
fn a_development_file_moves_to_runtime_mode() {
    let Some(binaries) = binaries() else {
        println!("set SUPERSCHEMATIC_BIN and SUPERSCHEMATIC_MIGRATE_BIN, or run make cli, to run the move");
        return;
    };
    let dir = dir();
    let file = dir.join("notes.dev.db");
    let mut made = Vec::new();
    {
        let mut dev = Dev::open(&file).expect("a development file");
        let me = Caller::new("developer", Vec::<String>::new());
        dev.request(&me, &Request::Define { document: notes() })
            .expect("defined");
        dev.request(
            &me,
            &Request::Publish {
                type_name: "Note".into(),
                discard: false,
            },
        )
        .expect("published");
        for title in ["Alpha", "Beta"] {
            let note = dev
                .request(
                    &me,
                    &Request::Create {
                        type_name: "Note".into(),
                        data: json!({"title": title}).as_object().cloned().expect("data"),
                        params: Default::default(),
                    },
                )
                .expect("a note");
            made.push(note["id"].as_str().expect("an id").to_owned());
        }
    }

    let runtime_file = dir.join("notes.runtime.db");
    let moved = dev_move(
        &binaries,
        &[
            "--db",
            file.to_str().expect("a path"),
            "--to",
            runtime_file.to_str().expect("a path"),
            "--rows",
        ],
    );
    assert!(
        moved.status.success(),
        "{}",
        String::from_utf8_lossy(&moved.stderr)
    );
    let answer: Value = serde_json::from_slice(&moved.stdout).expect("the answer");
    assert_eq!(answer["service"], "notes");
    assert_eq!(answer["rows"], json!({"replayed": 2, "refused": []}));
    let mut runtime = Runtime::open(&runtime_file)
        .expect("the runtime file")
        .with_binaries(binaries.clone());
    assert_eq!(ids(&mut runtime), made, "each row keeps its id");

    // The database is no longer empty.
    let again = dev_move(
        &binaries,
        &[
            "--db",
            file.to_str().expect("a path"),
            "--to",
            runtime_file.to_str().expect("a path"),
            "--rows",
        ],
    );
    assert!(!again.status.success());
    assert!(
        String::from_utf8_lossy(&again.stderr).contains("replay into an empty runtime database"),
        "{}",
        String::from_utf8_lossy(&again.stderr)
    );

    // Without --rows the schemas move and the rows stay behind.
    let schemas_file = dir.join("notes.schemas.db");
    let schemas = dev_move(
        &binaries,
        &[
            "--db",
            file.to_str().expect("a path"),
            "--to",
            schemas_file.to_str().expect("a path"),
        ],
    );
    assert!(
        schemas.status.success(),
        "{}",
        String::from_utf8_lossy(&schemas.stderr)
    );
    let answer: Value = serde_json::from_slice(&schemas.stdout).expect("the answer");
    assert!(answer.get("rows").is_none(), "{answer}");
    let mut runtime = Runtime::open(&schemas_file)
        .expect("the runtime file")
        .with_binaries(binaries.clone());
    assert!(ids(&mut runtime).is_empty());

    // A store that does not exist is refused, and no runtime file is made;
    // a move with no runtime database is a usage error.
    let nowhere = dir.join("nowhere.db");
    let missing = dev_move(
        &binaries,
        &[
            "--db",
            dir.join("missing.db").to_str().expect("a path"),
            "--to",
            nowhere.to_str().expect("a path"),
        ],
    );
    assert!(!missing.status.success());
    assert!(String::from_utf8_lossy(&missing.stderr).contains("no such development store"));
    assert!(!nowhere.exists());
    let usage = dev_move(&binaries, &["--db", file.to_str().expect("a path")]);
    assert_eq!(usage.status.code(), Some(2));
}
