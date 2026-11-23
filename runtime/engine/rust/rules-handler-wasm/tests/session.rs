//! The session `superschematic-rules-dev`'s store test runs
//! (`the_same_requests_answer_alike_over_memory_and_over_sqlite`, in
//! `rules-dev/tests/store.rs`), run through the JSON ABI: a host loop over
//! `run`'s documents, its reads answered and its units applied over
//! memory, answers as `serve` answers over memory, and both are the answers
//! `runtime/engine/testdata/rules-dev/session.json` records. The
//! TypeScript library (`runtime/rules-handler/typescript`) runs the same
//! requests from that file over bun:sqlite and is held to the same
//! answers.
//!
//! `UPDATE_SESSION=1 cargo test -p superschematic-rules-handler-wasm --test
//! session` writes the file again.

use serde_json::{json, Map, Value};
use superschematic_rules::{Caller, Refusal};
use superschematic_rules_handler::filter::Sort;
use superschematic_rules_handler::{
    serve, Context, Declarations, Handler, MemoryStore, Request, Store, StoreAnswer, StoreRead,
    Unit,
};
use superschematic_rules_handler_wasm::{run_json, Op};

const HOUR: i64 = 3_600_000;
const NOW: i64 = 1_000_000;

fn object(value: Value) -> Map<String, Value> {
    value.as_object().cloned().expect("an object")
}

fn caller() -> Caller {
    Caller::new("alice", ["orders"])
}

/// rules-dev's store test's session, request for request: defines,
/// publishes, writes, a behavior's operation, lists, a relation's cascade,
/// a refused tightening and its discard; then a type whose rows Numbering
/// counts in a shared table, and one whose changes Revisions reviews, an
/// approval writing the row's own fields.
fn script() -> Vec<Request> {
    let project = json!({"name": "Project", "fields": [
        {"name": "id", "typeRef": {"name": "string"}, "key": true},
        {"name": "name", "typeRef": {"name": "string"}, "required": true, "unique": true}
    ]});
    let task = |max: Option<u64>| {
        let mut title = json!({"name": "title", "typeRef": {"name": "string"}, "required": true});
        if let Some(max) = max {
            title["validateMaxLength"] = json!(max);
        }
        json!({"name": "Task",
        "behaviors": [
            {"name": "Workflow", "field": "status", "config": {"states": ["todo", "doing", "done"], "transitions": [{"from": "todo", "to": "doing"}, {"from": "doing", "to": "done", "permission": "projects.finish"}]}},
            {"name": "Timers", "config": {"deadlines": {"nudge": {"start": {"create": true}, "afterMs": HOUR, "then": {"transition": "doing"}}}}}
        ],
        "fields": [
            {"name": "id", "typeRef": {"name": "string"}, "key": true},
            title,
            {"name": "points", "typeRef": {"name": "Generic.Int64"}},
            {"name": "due", "typeRef": {"name": "Temporal.DateTime"}},
            {"name": "project", "typeRef": {"name": "Project"}, "relation": {"type": "Project"}}
        ]})
    };
    let create = |type_name: &str, data: Value| Request::Create {
        type_name: type_name.into(),
        data: object(data),
        params: Map::new(),
    };
    let invoice = json!({"name": "Invoice",
    "behaviors": [{"name": "Numbering", "field": "number", "config": {"format": "INV-{seq:2}"}}],
    "fields": [
        {"name": "id", "typeRef": {"name": "string"}, "key": true},
        {"name": "title", "typeRef": {"name": "string"}}
    ]});
    let policy = json!({"name": "Policy",
    "behaviors": [{"name": "Revisions", "config": {"review": {"permission": "orders.review"}}}],
    "fields": [
        {"name": "id", "typeRef": {"name": "string"}, "key": true},
        {"name": "title", "typeRef": {"name": "string"}, "required": true}
    ]});
    let revisions = |operation: &str, params: Value| Request::Operation {
        type_name: "Policy".into(),
        id: "y1".into(),
        use_id: "Revisions".into(),
        operation: operation.into(),
        params,
        preconditions: Map::new(),
    };
    let list = |filter: Value, sort: Vec<Sort>| Request::List {
        type_name: "Task".into(),
        filter,
        sort,
        limit: Some(2),
        cursor: None,
    };
    let publish = |type_name: &str, discard: bool| Request::Publish {
        type_name: type_name.into(),
        discard,
    };
    let transition = |to: &str| Request::Operation {
        type_name: "Task".into(),
        id: "t1".into(),
        use_id: "status".into(),
        operation: "transition".into(),
        params: json!({ "to": to }),
        preconditions: Map::new(),
    };
    vec![
        Request::Define { document: project },
        publish("Project", false),
        Request::Define {
            document: task(None),
        },
        publish("Task", false),
        create("Project", json!({"id": "p1", "name": "Launch"})),
        create("Project", json!({"id": "p2", "name": "Launch"})),
        create("Project", json!({"id": "p2", "name": "Ship"})),
        create(
            "Task",
            json!({"id": "t1", "title": "Write the notes", "points": 3, "projectId": "p1", "due": "2026-03-01T09:00:00+01:00"}),
        ),
        create(
            "Task",
            json!({"id": "t2", "title": "Review", "points": 5, "projectId": "p2"}),
        ),
        create(
            "Task",
            json!({"id": "t3", "title": "Ship", "projectId": "p9"}),
        ),
        create(
            "Task",
            json!({"id": "t3", "title": "Ship", "projectId": "p1"}),
        ),
        Request::Update {
            type_name: "Task".into(),
            id: "t3".into(),
            patch: object(json!({"points": 8})),
            preconditions: Map::new(),
        },
        transition("doing"),
        transition("done"),
        list(
            json!({"project": {"name": {"eq": "Launch"}}}),
            vec![Sort {
                field: "points".into(),
                desc: true,
            }],
        ),
        list(json!({"status": {"in": ["doing", "done"]}}), Vec::new()),
        list(json!({"due": {"gte": "2026-03-01T08:00:00Z"}}), Vec::new()),
        Request::Get {
            type_name: "Task".into(),
            id: "t1".into(),
        },
        Request::Describe {
            type_name: "Task".into(),
        },
        Request::Delete {
            type_name: "Project".into(),
            id: "p2".into(),
            preconditions: Map::new(),
        },
        Request::Get {
            type_name: "Task".into(),
            id: "t2".into(),
        },
        Request::Define {
            document: task(Some(5)),
        },
        publish("Task", false),
        publish("Task", true),
        list(Value::Null, Vec::new()),
        Request::TableOperation {
            type_name: "Task".into(),
            use_id: "Timers".into(),
            operation: "nothing".into(),
            params: Value::Null,
        },
        Request::Define { document: invoice },
        publish("Invoice", false),
        create("Invoice", json!({"id": "i1", "title": "a"})),
        create("Invoice", json!({"id": "i2", "title": "b"})),
        Request::Delete {
            type_name: "Invoice".into(),
            id: "i2".into(),
            preconditions: Map::new(),
        },
        create("Invoice", json!({"id": "i3", "title": "c"})),
        Request::Define { document: policy },
        publish("Policy", false),
        create("Policy", json!({"id": "y1", "title": "Travel"})),
        revisions("propose", json!({"patch": {"title": "Trips"}})),
        revisions("approve", json!({"proposal": 1})),
        revisions("propose", json!({"patch": {"title": null}})),
        revisions("approve", json!({"proposal": 2})),
        Request::Get {
            type_name: "Policy".into(),
            id: "y1".into(),
        },
    ]
}

/// The n-th request's id and UUID, as rules-dev's store test gives them:
/// `r<n>`, and `(n + 1) << 64`, hyphenated.
fn ids(n: usize) -> (String, u128, String) {
    let id = (n as u128 + 1) << 64;
    let hex = format!("{id:032x}");
    let uuid = format!(
        "{}-{}-{}-{}-{}",
        &hex[0..8],
        &hex[8..12],
        &hex[12..16],
        &hex[16..20],
        &hex[20..32]
    );
    (format!("r{n}"), id, uuid)
}

fn answer(outcome: Result<Value, Refusal>) -> Value {
    match outcome {
        Ok(value) => json!({ "ok": value }),
        Err(refusal) => json!({ "refused": refusal }),
    }
}

/// The host loop over the ABI's documents: a request's run, its reads
/// answered from the store, its unit applied, run again from fresh state
/// when a fence did not hold.
fn serve_json<S: Store>(store: &mut S, request: &Value, context: &Value) -> Value {
    'attempt: for _ in 0..8 {
        store.begin().expect("begun");
        let mut answers: Vec<Value> = Vec::new();
        loop {
            let input =
                json!({"request": request, "context": context, "answers": {"reads": answers}});
            let (out, ok) = run_json(Op::Run, input.to_string().as_bytes());
            assert!(ok, "{out}");
            let step: Value = serde_json::from_str(&out).expect("a step of JSON");
            if let Some(reads) = step.get("read").and_then(Value::as_array) {
                for read in reads {
                    let typed: StoreRead =
                        serde_json::from_value(read.clone()).expect("a store read");
                    let answered: StoreAnswer = match store.read(&typed) {
                        Ok(answered) => answered,
                        Err(refusal) => {
                            store.rollback().expect("rolled back");
                            return json!({ "refused": refusal });
                        }
                    };
                    answers.push(json!([read, answered]));
                }
                continue;
            }
            let done = &step["done"];
            let unit: Unit = serde_json::from_value(done["unit"].clone()).expect("a unit");
            let applied = unit.is_empty() || store.apply(&unit).expect("the unit applies");
            if !applied {
                store.rollback().expect("rolled back");
                continue 'attempt;
            }
            store.commit().expect("committed");
            return match done.get("refused") {
                Some(refused) => json!({ "refused": refused }),
                None => json!({ "ok": done["response"] }),
            };
        }
    }
    panic!("the request kept moving");
}

fn session_path() -> std::path::PathBuf {
    std::path::Path::new(env!("CARGO_MANIFEST_DIR")).join("../../testdata/rules-dev/session.json")
}

#[test]
fn the_session_answers_alike_through_the_abi_and_as_recorded() {
    let core = superschematic_rules_behaviors::core();
    let declarations = Declarations::ported();
    let handler = Handler::new(&core, &declarations);
    let mut direct = MemoryStore::new();
    let mut through = MemoryStore::new();
    let caller_json = serde_json::to_value(caller()).expect("a caller of JSON");
    let mut steps = Vec::new();
    for (n, request) in script().iter().enumerate() {
        let (request_id, id, uuid) = ids(n);
        let context = Context {
            caller: caller(),
            now: NOW,
            request: request_id.clone(),
            id,
        };
        let served = answer(serve(&handler, &mut direct, request, &context));
        let request_json = serde_json::to_value(request).expect("a request of JSON");
        let context_json =
            json!({"caller": caller_json, "now": NOW, "request": request_id, "id": uuid});
        let ran = serve_json(&mut through, &request_json, &context_json);
        assert_eq!(served, ran, "request {n} through the ABI");
        steps.push(json!({"request": request_json, "context": {"request": request_id, "id": uuid}, "answer": served}));
    }
    // What rules-dev's store test holds the session to.
    let refused: Vec<(usize, &str)> = steps
        .iter()
        .enumerate()
        .filter_map(|(n, step)| step["answer"]["refused"]["code"].as_str().map(|c| (n, c)))
        .collect();
    assert_eq!(
        refused,
        [
            (5, "conflict"),
            (9, "invalid_instance"),
            (13, "forbidden"),
            (20, "not_found"),
            (22, "conflict"),
            (25, "not_found"),
            (38, "invalid_instance"),
        ]
    );
    let session = json!({
        "about": "rules-dev's store session (rules-dev/tests/store.rs), each request with its context and its answer over memory; written by UPDATE_SESSION=1 cargo test -p superschematic-rules-handler-wasm --test session",
        "caller": caller_json,
        "now": NOW,
        "steps": steps,
    });
    let path = session_path();
    if std::env::var_os("UPDATE_SESSION").is_some() {
        std::fs::create_dir_all(path.parent().expect("a directory")).expect("the directory");
        let text = serde_json::to_string_pretty(&session).expect("JSON") + "\n";
        std::fs::write(&path, text).expect("the session is written");
        return;
    }
    let text = std::fs::read_to_string(&path).unwrap_or_else(|error| {
        panic!(
            "{}: {error}; write it with UPDATE_SESSION=1",
            path.display()
        )
    });
    let recorded: Value = serde_json::from_str(&text).expect("the session reads");
    for (n, (recorded, step)) in recorded["steps"]
        .as_array()
        .expect("steps")
        .iter()
        .zip(session["steps"].as_array().expect("steps"))
        .enumerate()
    {
        assert_eq!(
            recorded, step,
            "request {n} answers otherwise than {}; write it again with UPDATE_SESSION=1 if the change is meant",
            path.display()
        );
    }
    assert_eq!(
        recorded,
        session,
        "{} is not the session; write it again with UPDATE_SESSION=1",
        path.display()
    );
}
