//! The moves between modes (D117) runtime mode makes, on SQLite and on
//! Postgres alike: development mode's General documents published as the
//! tables `format --to=ts` writes them as, a development store's schemas
//! and rows moved to an empty runtime database, and a runtime database
//! exported to a compiled project whose first plan from the recorded model
//! is empty.

use std::path::{Path, PathBuf};

use serde_json::{json, Value};
use superschematic_rules::{Caller, Code};
use superschematic_rules_dev::Dev;
use superschematic_rules_handler::{Request, Store};
use superschematic_rules_runtime::{Binaries, Publish};

use super::{create, host, ok, run, Clock, Open};

/// Development mode's General documents of the engine examples, copied
/// into the planner's testdata (`internal/migrateplan/testdata/development`).
pub fn development(names: &[&str]) -> Vec<Value> {
    let dir = PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("../../../../internal/migrateplan/testdata/development");
    names
        .iter()
        .map(|name| {
            let path = dir.join(format!("{name}.schema.json"));
            let text = std::fs::read_to_string(&path)
                .unwrap_or_else(|error| panic!("{}: {error}", path.display()));
            serde_json::from_str(&text).expect("a schema document")
        })
        .collect()
}

fn operation(type_name: &str, id: &Value, operation: &str, params: Value) -> Value {
    json!({"kind": "operation", "type": type_name, "id": id, "use": "", "operation": operation, "params": params})
}

/// Development mode's General documents publish as their tables: notes as
/// the service of its one name, its Workflow the field `status` with its
/// operations, and the jobs example's three documents once their service
/// is named (decision 3).
pub fn general_documents_publish_as_their_tables(open: Open<'_>, binaries: &Binaries) {
    let mut runtime = host(open, binaries, &Clock::now());
    let refused = runtime
        .publish(&Publish::of(development(&["batches", "jobs", "workers"])))
        .expect_err("documents of three names name no service");
    assert_eq!(refused.code, Code::InvalidArgument, "{refused:?}");
    assert!(
        refused
            .message
            .contains("General schemas Batch, Job and Worker")
            && refused.message.contains("name the service"),
        "{}",
        refused.message
    );

    let published = runtime
        .publish(&Publish::of(development(&["notes"])))
        .expect("the notes publish");
    assert_eq!(published["service"], "notes");
    let note = create(&mut runtime, "Note", json!({"title": "Plan the launch"}));
    assert_eq!(note["status"], "draft", "{note}");
    assert!(note.get("statusEnteredAt").is_some(), "{note}");
    assert!(note.get("workflowStatus").is_none(), "{note}");
    ok(
        &mut runtime,
        operation("Note", &note["id"], "transition", json!({"to": "review"})),
    );
    let moved = ok(
        &mut runtime,
        json!({"kind": "get", "type": "Note", "id": note["id"]}),
    );
    assert_eq!(moved["status"], "review", "{moved}");
    let listed = ok(
        &mut runtime,
        json!({"kind": "list", "type": "Note", "filter": {"status": {"eq": "review"}}}),
    );
    assert_eq!(
        listed["items"].as_array().map(Vec::len),
        Some(1),
        "{listed}"
    );
}

/// The jobs example's three documents publish as one service's tables
/// when it is named.
pub fn named_general_documents_publish(open: Open<'_>, binaries: &Binaries) {
    let mut runtime = host(open, binaries, &Clock::now());
    let published = runtime
        .publish(&Publish::of(development(&["batches", "jobs", "workers"])).service("jobs-db"))
        .expect("the jobs publish as jobs-db");
    assert_eq!(published["service"], "jobs-db");
    let worker = create(
        &mut runtime,
        "Worker",
        json!({"id": "worker-1", "subject": "worker-1"}),
    );
    assert_eq!(worker["status"], "active", "{worker}");
    let job = create(
        &mut runtime,
        "Job",
        json!({"title": "Crawl", "topic": "crawl"}),
    );
    assert_eq!(job["status"], "queued", "{job}");
}

/// Order, keyed by a code, whose Workflow is on the type and which may
/// point at a parent Order, as a General document of development mode.
pub fn order(max_title: Option<u64>) -> Value {
    let mut title = json!({"name": "title", "typeRef": {"name": "string"}, "required": true});
    if let Some(max) = max_title {
        title["validateMaxLength"] = json!(max);
    }
    json!({"kind": "General", "name": "Order", "types": {"Order": {"name": "Order", "role": "EmbeddedStruct",
    "behaviors": [{"name": "Workflow", "config": {
        "states": ["draft", "paid"], "transitions": [{"from": "draft", "to": "paid"}]}}],
    "fields": [
        {"name": "code", "typeRef": {"name": "string"}, "required": true, "key": true},
        title,
        {"name": "parent", "typeRef": {"name": "Order"}, "relation": {"type": "Order"}}
    ]}}})
}

/// Line, which points at its Order; its name sorts before Order's, so a
/// replay in name order would create it first.
pub fn line() -> Value {
    json!({"kind": "General", "name": "Line", "types": {"Line": {"name": "Line", "role": "EmbeddedStruct",
    "fields": [
        {"name": "code", "typeRef": {"name": "string"}, "required": true, "key": true},
        {"name": "sku", "typeRef": {"name": "string"}, "required": true},
        {"name": "order", "typeRef": {"name": "Order"}, "required": true, "relation": {"type": "Order"}}
    ]}}})
}

fn dev_ok<S: Store>(dev: &mut Dev<S>, request: Request) -> Value {
    let me = Caller::new("developer", Vec::<String>::new());
    match dev.request(&me, &request) {
        Ok(answer) => answer,
        Err(refusal) => panic!("{request:?} was refused: {refusal:?}"),
    }
}

/// Fills a development store with Orders and Lines: b, then a whose parent
/// is b and which is paid, then c, whose title the runtime's schema holds
/// too long, and Line l1 of a.
pub fn sample_rows<S: Store>(dev: &mut Dev<S>) {
    for document in [order(None), line()] {
        let name = document["name"].as_str().expect("a name").to_owned();
        dev_ok(dev, Request::Define { document });
        dev_ok(
            dev,
            Request::Publish {
                type_name: name,
                discard: false,
            },
        );
    }
    let create = |type_name: &str, data: Value| Request::Create {
        type_name: type_name.into(),
        data: data.as_object().cloned().expect("an object"),
        params: Default::default(),
    };
    dev_ok(
        dev,
        create("Order", json!({"code": "b", "title": "Chairs"})),
    );
    dev_ok(
        dev,
        create(
            "Order",
            json!({"code": "a", "title": "Desk", "parentId": "b"}),
        ),
    );
    dev_ok(
        dev,
        Request::Operation {
            type_name: "Order".into(),
            id: "a".into(),
            use_id: String::new(),
            operation: "transition".into(),
            params: json!({"to": "paid"}),
            preconditions: Default::default(),
        },
    );
    let paid = dev_ok(
        dev,
        Request::Get {
            type_name: "Order".into(),
            id: "a".into(),
        },
    );
    assert_eq!(
        paid["status"], "paid",
        "development mode's Workflow is the field status: {paid}"
    );
    dev_ok(
        dev,
        create("Order", json!({"code": "c", "title": "A title too long"})),
    );
    dev_ok(
        dev,
        create(
            "Line",
            json!({"code": "l1", "sku": "DESK-1", "orderId": "a"}),
        ),
    );
}

/// A development store moves to an empty runtime database with its rows
/// (D117, decision 5): its schemas publish, and each row replays as a
/// create through its type's rules, after the rows it points at, with its
/// own fields and its id and its behaviors' state fresh; a row the runtime
/// refuses is listed, and the rest move. A second move is refused, since
/// the database is no longer empty.
pub fn development_moves_with_its_rows<S: Store>(
    open: Open<'_>,
    binaries: &Binaries,
    dev: &mut Dev<S>,
) {
    sample_rows(dev);
    let mut runtime = host(open, binaries, &Clock::now());
    // The runtime's Order holds a title to 10 characters, which c's fails.
    let publish = Publish::of(vec![order(Some(10)), line()]).service("shop-db");
    let moved = runtime
        .publish_from(dev.store(), &publish)
        .expect("the store moves");
    assert_eq!(moved["applied"], true, "{moved}");
    assert_eq!(moved["rows"]["replayed"], 3, "{moved}");
    let refused = moved["rows"]["refused"].as_array().expect("refused rows");
    assert_eq!(refused.len(), 1, "{moved}");
    assert_eq!(refused[0]["type"], "Order");
    assert_eq!(refused[0]["id"], "c");
    assert_eq!(refused[0]["code"], "invalid_instance", "{moved}");

    let a = ok(
        &mut runtime,
        json!({"kind": "get", "type": "Order", "id": "a"}),
    );
    assert_eq!(a["title"], "Desk");
    assert_eq!(a["parentId"], "b", "{a}");
    assert_eq!(a["status"], "draft", "a behavior's state starts fresh: {a}");
    let line = ok(
        &mut runtime,
        json!({"kind": "get", "type": "Line", "id": "l1"}),
    );
    assert_eq!(line["orderId"], "a", "{line}");
    let c = super::ask(
        &mut runtime,
        json!({"kind": "get", "type": "Order", "id": "c"}),
    )
    .expect_err("c stayed behind");
    assert_eq!(c.code, Code::NotFound);

    // The development store is as it was.
    let held = dev_ok(
        dev,
        Request::Get {
            type_name: "Order".into(),
            id: "a".into(),
        },
    );
    assert_eq!(held["status"], "paid");

    let again = runtime
        .publish_from(dev.store(), &publish)
        .expect_err("the database is no longer empty");
    assert_eq!(again.code, Code::Conflict);
    assert!(
        again
            .message
            .contains("replay into an empty runtime database alone"),
        "{}",
        again.message
    );
}

/// A development store's schemas, the ones its types are live at, publish
/// with no rows when a move is asked for without them, as
/// `superschematic-dev move` does without --rows.
pub fn development_documents_publish<S: Store>(
    open: Open<'_>,
    binaries: &Binaries,
    dev: &mut Dev<S>,
) {
    sample_rows(dev);
    let documents =
        superschematic_rules_runtime::development_documents(dev.store()).expect("the documents");
    assert_eq!(documents, vec![line(), order(None)], "in type order");
    let mut runtime = host(open, binaries, &Clock::now());
    runtime
        .publish(&Publish::of(documents).service("shop-db"))
        .expect("the schemas publish");
    let listed = ok(&mut runtime, json!({"kind": "list", "type": "Order"}));
    assert_eq!(listed["items"], json!([]), "sample rows stay behind");
}

/// The runtime database exports as a compiled project (D117): `migrate
/// export` writes the service's config, its schema files in TypeScript and
/// the model the database recorded, and the build's first plan from that
/// model is empty, so the generated servers take the tables over as they
/// are.
pub fn exports_to_compiled(open: Open<'_>, binaries: &Binaries, database_url: &str, dialect: &str) {
    let mut runtime = host(open, binaries, &Clock::now());
    runtime
        .publish(&Publish::of(development(&["batches", "jobs", "workers"])).service("jobs-db"))
        .expect("the jobs publish");
    create(
        &mut runtime,
        "Job",
        json!({"title": "Crawl", "topic": "crawl"}),
    );
    drop(runtime);

    // A schemas root of its own, whose one service the export writes, so
    // the plan resolves no other service of its name.
    let root = PathBuf::from(env!("CARGO_TARGET_TMPDIR"))
        .join("rules-runtime")
        .join(format!("export-{dialect}-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&root);
    let out = root.join("services").join("jobs-db");
    let printed = run_with_env(
        &binaries.superschematic,
        &["migrate", "export", "--database-url", database_url, "--out"],
        &out,
        binaries,
    );
    assert!(printed.contains("model.json"), "{printed}");
    for written in ["schema.config.ts", "model.json"] {
        assert!(out.join(written).exists(), "{written} written");
    }
    let schemas: Vec<String> = std::fs::read_dir(out.join("src"))
        .expect("src")
        .filter_map(|entry| Some(entry.ok()?.file_name().to_string_lossy().into_owned()))
        .collect();
    assert!(
        schemas.iter().any(|name| name.ends_with(".schema.ts")),
        "{schemas:?}"
    );
    let job = std::fs::read_to_string(out.join("src/job.schema.ts")).expect("job.schema.ts");
    assert!(job.contains("status: Workflow<"), "{job}");
    assert!(!job.contains("@behavior"), "{job}");

    // The build reads the TypeScript through the repository's packages.
    write_tsconfig(&out);
    let plan = run(
        &binaries.superschematic,
        &[
            "migrate",
            "plan",
            out.to_str().expect("a path"),
            "--dialect",
            dialect,
            "--from",
            out.join("model.json").to_str().expect("a path"),
            "--format",
            "json",
        ],
        b"",
    );
    let plan: Value = serde_json::from_slice(&plan).expect("a plan");
    assert_eq!(plan["steps"], json!([]), "{plan}");
    assert_eq!(plan["from"], plan["to"], "{plan}");
}

/// Runs a binary with its last argument a path, the migration runner on
/// the export's PATH, and returns its stdout and stderr.
fn run_with_env(binary: &Path, args: &[&str], path: &Path, binaries: &Binaries) -> String {
    let output = std::process::Command::new(binary)
        .args(args)
        .arg(path)
        .env("SUPERSCHEMATIC_MIGRATE", &binaries.migrate)
        .output()
        .expect("the binary runs");
    let text = format!(
        "{}{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );
    assert!(output.status.success(), "{binary:?} {args:?}: {text}");
    text
}

/// A tsconfig.json beside an exported service that resolves the authoring
/// packages to the repository's, as a schemas root's base config does.
fn write_tsconfig(dir: &Path) {
    let repo = PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../../../..");
    let repo = repo.canonicalize().expect("the repository");
    let package = |name: &str| {
        repo.join("packages")
            .join(name)
            .join("src/index.ts")
            .to_string_lossy()
            .into_owned()
    };
    let config = json!({
        "compilerOptions": {
            "target": "ES2022", "module": "ESNext", "moduleResolution": "Bundler", "lib": ["ES2022"],
            "strict": true, "strictPropertyInitialization": false, "experimentalDecorators": true,
            "noEmit": true, "skipLibCheck": true, "baseUrl": ".",
            "paths": {
                "@superschematic/db": [package("db")],
                "@superschematic/schema": [package("schema")],
                "@superschematic/schema-config": [package("schema-config")],
                "superscalar": [repo.join("third_party/superscalar/bindings/typescript/src/index.ts").to_string_lossy()]
            }
        },
        "include": ["schema.config.ts", "src/**/*.ts"]
    });
    std::fs::write(
        dir.join("tsconfig.json"),
        serde_json::to_vec_pretty(&config).expect("JSON"),
    )
    .expect("tsconfig.json");
}
