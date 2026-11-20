//! Development mode's store over SQLite (D117), in memory and in a file:
//! the same requests answer as they do over memory, a file keeps its
//! versions and rows across opens, a unit whose fence does not hold
//! applies nothing, a shared table's row is written held to its version,
//! and the host runs its queues after each request.

use std::sync::atomic::{AtomicI64, Ordering};
use std::sync::Arc;

use serde_json::{json, Map, Value};
use superschematic_rules::{Caller, Code, Refusal};
use superschematic_rules_dev::{Dev, Drain, SqliteStore};
use superschematic_rules_handler::filter::Sort;
use superschematic_rules_handler::{
    serve, Context, Declarations, Handler, MemoryStore, Request, Store, StoreRead, StoreWrite, Unit,
};

const HOUR: i64 = 3_600_000;

fn object(value: Value) -> Map<String, Value> {
    value.as_object().cloned().expect("an object")
}

fn file(name: &str) -> std::path::PathBuf {
    let dir = std::path::Path::new(env!("CARGO_TARGET_TMPDIR")).join("rules-dev");
    std::fs::create_dir_all(&dir).expect("the test's directory");
    let path = dir.join(format!("{name}-{}.db", std::process::id()));
    let _ = std::fs::remove_file(&path);
    path
}

fn caller() -> Caller {
    Caller::new("alice", ["orders"])
}

/// The requests of a session that works out two types: defines,
/// publishes, writes, a behavior's operation, lists, a relation's
/// cascade, a refused tightening and its discard; then a type whose rows
/// Numbering counts in a shared table, and one whose changes Revisions
/// reviews, an approval writing the row's own fields (D103's step 4).
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
    vec![
        Request::Define { document: project },
        Request::Publish {
            type_name: "Project".into(),
            discard: false,
        },
        Request::Define {
            document: task(None),
        },
        Request::Publish {
            type_name: "Task".into(),
            discard: false,
        },
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
        Request::Operation {
            type_name: "Task".into(),
            id: "t1".into(),
            use_id: "status".into(),
            operation: "transition".into(),
            params: json!({"to": "doing"}),
            preconditions: Map::new(),
        },
        Request::Operation {
            type_name: "Task".into(),
            id: "t1".into(),
            use_id: "status".into(),
            operation: "transition".into(),
            params: json!({"to": "done"}),
            preconditions: Map::new(),
        },
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
        Request::Publish {
            type_name: "Task".into(),
            discard: false,
        },
        Request::Publish {
            type_name: "Task".into(),
            discard: true,
        },
        list(Value::Null, Vec::new()),
        Request::TableOperation {
            type_name: "Task".into(),
            use_id: "Timers".into(),
            operation: "nothing".into(),
            params: Value::Null,
        },
        Request::Define { document: invoice },
        Request::Publish {
            type_name: "Invoice".into(),
            discard: false,
        },
        create("Invoice", json!({"id": "i1", "title": "a"})),
        create("Invoice", json!({"id": "i2", "title": "b"})),
        Request::Delete {
            type_name: "Invoice".into(),
            id: "i2".into(),
            preconditions: Map::new(),
        },
        create("Invoice", json!({"id": "i3", "title": "c"})),
        Request::Define { document: policy },
        Request::Publish {
            type_name: "Policy".into(),
            discard: false,
        },
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

fn run_script<S: Store>(store: &mut S) -> Vec<Value> {
    let core = superschematic_rules_behaviors::core();
    let declarations = Declarations::ported();
    let handler = Handler::new(&core, &declarations);
    script()
        .iter()
        .enumerate()
        .map(|(n, request)| {
            let context = Context {
                caller: caller(),
                now: 1_000_000,
                request: format!("r{n}"),
                id: (n as u128 + 1) << 64,
            };
            match serve(&handler, store, request, &context) {
                Ok(value) => json!({"ok": value}),
                Err(Refusal { code, message, .. }) => {
                    json!({"refused": code.as_str(), "message": message})
                }
            }
        })
        .collect()
}

#[test]
fn the_same_requests_answer_alike_over_memory_and_over_sqlite() {
    let declarations = Declarations::ported();
    let over_memory = run_script(&mut MemoryStore::new());
    let mut in_memory = SqliteStore::memory(&declarations).expect("SQLite opens");
    let over_sqlite = run_script(&mut in_memory);
    let path = file("parity");
    let mut in_a_file = SqliteStore::open(&path, &declarations).expect("the file opens");
    let over_a_file = run_script(&mut in_a_file);
    for (n, ((memory, sqlite), file)) in over_memory
        .iter()
        .zip(&over_sqlite)
        .zip(&over_a_file)
        .enumerate()
    {
        assert_eq!(memory, sqlite, "request {n} over SQLite in memory");
        assert_eq!(memory, file, "request {n} over a SQLite file");
    }
    // What the session came to, step by step.
    let refused: Vec<(usize, &str)> = over_memory
        .iter()
        .enumerate()
        .filter_map(|(n, answer)| answer["refused"].as_str().map(|code| (n, code)))
        .collect();
    assert_eq!(
        refused,
        [
            (5, "conflict"),          // a second holder of a unique name
            (9, "invalid_instance"),  // a relation to p9, which does not exist
            (13, "forbidden"),        // done needs projects.finish
            (20, "not_found"),        // p2's delete cascaded to t2
            (22, "conflict"),         // the tightened title fails t1
            (25, "not_found"),        // Timers has no operation nothing
            (38, "invalid_instance"), // the approval clears a required title
        ]
    );
    // Numbering's count is the table's: a delete leaves a gap.
    let numbers: Vec<&Value> = [28, 29, 31]
        .iter()
        .map(|n| &over_memory[*n]["ok"]["number"])
        .collect();
    assert_eq!(numbers, ["INV-01", "INV-02", "INV-03"]);
    // The approval wrote the row's own fields; the refused one, nothing.
    assert_eq!(over_memory[39]["ok"]["title"], "Trips");
    assert_eq!(over_memory[39]["ok"]["revisionsRevision"], 2);
    let ids = |answer: &Value| -> Vec<String> {
        answer["ok"]["items"]
            .as_array()
            .expect("items")
            .iter()
            .map(|item| item["id"].as_str().unwrap_or_default().to_owned())
            .collect()
    };
    assert_eq!(ids(&over_memory[14]), ["t3", "t1"], "points descending");
    assert_eq!(ids(&over_memory[15]), ["t1"]);
    assert_eq!(
        ids(&over_memory[16]),
        ["t1"],
        "a date-time compares by its time"
    );
    assert_eq!(
        over_memory[19]["ok"]["deleted"][1],
        json!({"type": "Task", "id": "t2"})
    );
    assert_eq!(over_memory[20]["message"], "Task t2 does not exist");
    assert_eq!(
        over_memory[23]["ok"]["discarded"],
        json!([{"type": "Task", "id": "t1"}])
    );
    assert_eq!(ids(&over_memory[24]), ["t3"]);
    drop(in_a_file);
    let _ = std::fs::remove_file(path);
}

/// The same session over a Postgres schema answers as it does over memory.
/// Skipped without `SUPERSCHEMATIC_RULES_TEST_DATABASE_URL`.
#[cfg(feature = "postgres")]
#[test]
fn the_same_requests_answer_alike_over_memory_and_over_postgres() {
    let Ok(url) = std::env::var("SUPERSCHEMATIC_RULES_TEST_DATABASE_URL") else {
        println!("set SUPERSCHEMATIC_RULES_TEST_DATABASE_URL to run the session on Postgres");
        return;
    };
    let declarations = Declarations::ported();
    let schema = format!("rules_dev_{}_parity", std::process::id());
    let mut store = superschematic_rules_dev::PgStore::open(&url, &schema, &declarations)
        .expect("the Postgres store opens");
    store.clear().expect("the store empties");
    let over_postgres = run_script(&mut store);
    let over_memory = run_script(&mut MemoryStore::new());
    for (n, (memory, postgres)) in over_memory.iter().zip(&over_postgres).enumerate() {
        assert_eq!(memory, postgres, "request {n} over Postgres");
    }
    store.drop_schema().expect("the schema drops");
}

#[test]
fn a_file_keeps_its_versions_and_rows_across_opens() {
    let path = file("reopen");
    let now = Arc::new(AtomicI64::new(1_000_000));
    let clock = {
        let now = Arc::clone(&now);
        move || now.load(Ordering::SeqCst)
    };
    {
        let mut dev = Dev::open(&path)
            .expect("the file opens")
            .with_clock(clock.clone());
        dev.request(&caller(), &Request::Define { document: json!({"name": "Note", "fields": [{"name": "title", "typeRef": {"name": "string"}}]}) })
            .expect("defined");
        dev.request(
            &caller(),
            &Request::Publish {
                type_name: "Note".into(),
                discard: false,
            },
        )
        .expect("published");
        dev.request(
            &caller(),
            &Request::Create {
                type_name: "Note".into(),
                data: object(
                    json!({"id": "0190f0e2-7b1c-7d4e-8f00-000000000001", "title": "Kept"}),
                ),
                params: Map::new(),
            },
        )
        .expect("created");
    }
    let mut dev = Dev::open(&path)
        .expect("the file opens again")
        .with_clock(clock);
    let page = dev
        .request(
            &caller(),
            &Request::List {
                type_name: "Note".into(),
                filter: Value::Null,
                sort: Vec::new(),
                limit: None,
                cursor: None,
            },
        )
        .expect("listed");
    assert_eq!(page["items"][0]["title"], "Kept");
    assert_eq!(
        page["items"][0]["id"],
        json!(superschematic_rules_handler::values::base62(
            0x0190_f0e2_7b1c_7d4e_8f00_0000_0000_0001
        ))
    );
    let described = dev
        .request(
            &caller(),
            &Request::Describe {
                type_name: "Note".into(),
            },
        )
        .expect("described");
    assert_eq!(described["version"], 1);
    drop(dev);
    let _ = std::fs::remove_file(path);
}

#[test]
fn a_unit_whose_fence_does_not_hold_applies_nothing() {
    let declarations = Declarations::ported();
    let mut store = SqliteStore::memory(&declarations).expect("SQLite opens");
    store.begin().expect("begun");
    let insert = Unit {
        writes: vec![StoreWrite::Insert {
            type_name: "Note".into(),
            id: "n1".into(),
            data: object(json!({"title": "a"})),
            owned: Default::default(),
        }],
    };
    assert!(store.apply(&insert).expect("applied"));
    let again = store.apply(&insert).expect_err("a second insert of the id");
    assert_eq!(again.code, Code::Conflict);
    let stale = Unit {
        writes: vec![
            StoreWrite::Arm {
                type_name: "Note".into(),
                id: "n1".into(),
                use_id: "Timers".into(),
                name: "x".into(),
                at: 5,
                payload: Value::Null,
            },
            StoreWrite::Update {
                type_name: "Note".into(),
                id: "n1".into(),
                expected: 7,
                data: object(json!({"title": "b"})),
                owned: Default::default(),
            },
        ],
    };
    assert!(!store.apply(&stale).expect("moved, not failed"));
    let row = store
        .read(&StoreRead::Row {
            type_name: "Note".into(),
            id: "n1".into(),
        })
        .expect("read");
    assert_eq!(
        serde_json::to_value(&row).expect("json")["row"]["data"]["title"],
        "a"
    );
    assert!(
        store.armed("Note", "n1").expect("read").is_empty(),
        "the arm went with the unit"
    );
    store.commit().expect("committed");
}

/// A shared table's row is written held to one row (D103's step 4): an
/// insert of a key the table holds and an update at another version move
/// the unit, which applies nothing, and a publish's drop of the use takes
/// its rows.
fn shared_rows_are_held_to_their_version<S: Store>(store: &mut S) {
    let write = |change: Value| StoreWrite::SharedRow {
        type_name: "Invoice".into(),
        use_id: "number".into(),
        table: "counts".into(),
        key: "*".into(),
        change: serde_json::from_value(change).expect("a change"),
    };
    let read = StoreRead::SharedRow {
        type_name: "Invoice".into(),
        use_id: "number".into(),
        table: "counts".into(),
        key: "*".into(),
    };
    let held = |store: &mut S| -> Value {
        serde_json::to_value(store.read(&read).expect("read")).expect("json")["sharedRow"].clone()
    };
    store.begin().expect("begun");
    assert_eq!(held(store), Value::Null);
    let insert = Unit {
        writes: vec![write(
            json!({"kind": "insert", "values": {"last": 1, "counting": null}}),
        )],
    };
    assert!(store.apply(&insert).expect("applied"));
    assert_eq!(
        held(store),
        json!({"key": "*", "values": {"last": 1}, "version": 1})
    );
    assert!(!store.apply(&insert).expect("a racing first insert moves"));
    let update = |expected: i64, last: i64| Unit {
        writes: vec![write(
            json!({"kind": "update", "expectedVersion": expected, "values": {"last": last}}),
        )],
    };
    assert!(store.apply(&update(1, 2)).expect("applied"));
    // A unit whose shared row moved applies none of its writes.
    let stale = Unit {
        writes: [
            update(1, 9).writes,
            vec![StoreWrite::Insert {
                type_name: "Invoice".into(),
                id: "i9".into(),
                data: object(json!({"title": "late"})),
                owned: Default::default(),
            }],
        ]
        .concat(),
    };
    assert!(!store.apply(&stale).expect("moved, not failed"));
    assert_eq!(
        held(store),
        json!({"key": "*", "values": {"last": 2}, "version": 2})
    );
    let row = store
        .read(&StoreRead::Row {
            type_name: "Invoice".into(),
            id: "i9".into(),
        })
        .expect("read");
    assert_eq!(
        serde_json::to_value(&row).expect("json")["row"],
        Value::Null
    );
    let dropped = Unit {
        writes: vec![StoreWrite::DropUse {
            type_name: "Invoice".into(),
            behavior: "Numbering".into(),
            use_id: "number".into(),
        }],
    };
    assert!(store.apply(&dropped).expect("applied"));
    assert_eq!(held(store), Value::Null);
    store.commit().expect("committed");
}

#[test]
fn a_shared_tables_row_is_held_to_its_version_over_memory_and_sqlite() {
    let declarations = Declarations::ported();
    shared_rows_are_held_to_their_version(&mut MemoryStore::new());
    shared_rows_are_held_to_their_version(
        &mut SqliteStore::memory(&declarations).expect("SQLite opens"),
    );
}

/// The same over a Postgres schema. Skipped without
/// `SUPERSCHEMATIC_RULES_TEST_DATABASE_URL`.
#[cfg(feature = "postgres")]
#[test]
fn a_shared_tables_row_is_held_to_its_version_over_postgres() {
    let Ok(url) = std::env::var("SUPERSCHEMATIC_RULES_TEST_DATABASE_URL") else {
        println!("set SUPERSCHEMATIC_RULES_TEST_DATABASE_URL to run it on Postgres");
        return;
    };
    let declarations = Declarations::ported();
    let schema = format!("rules_dev_{}_shared", std::process::id());
    let mut store = superschematic_rules_dev::PgStore::open(&url, &schema, &declarations)
        .expect("the Postgres store opens");
    store.clear().expect("the store empties");
    shared_rows_are_held_to_their_version(&mut store);
    store.drop_schema().expect("the schema drops");
}

fn ticket() -> Value {
    json!({"name": "Ticket",
        "behaviors": [
            {"name": "Workflow", "field": "status", "config": {"states": ["open", "closed"], "transitions": [{"from": "open", "to": "closed"}]}},
            {"name": "Timers", "config": {"deadlines": {"respond": {"start": {"create": true}, "afterMs": HOUR, "then": {"transition": "closed"}}}}}
        ],
        "fields": [{"name": "title", "typeRef": {"name": "string"}}]})
}

#[test]
fn the_host_runs_its_queues_after_each_request_and_a_deadline_when_it_falls_due() {
    let now = Arc::new(AtomicI64::new(1_000_000));
    let clock = {
        let now = Arc::clone(&now);
        move || now.load(Ordering::SeqCst)
    };
    let mut dev = Dev::memory().expect("SQLite opens").with_clock(clock);
    dev.request(&caller(), &Request::Define { document: ticket() })
        .expect("defined");
    dev.request(
        &caller(),
        &Request::Publish {
            type_name: "Ticket".into(),
            discard: false,
        },
    )
    .expect("published");
    let id = dev
        .request(
            &caller(),
            &Request::Create {
                type_name: "Ticket".into(),
                data: object(json!({"title": "t"})),
                params: Map::new(),
            },
        )
        .expect("created")["id"]
        .as_str()
        .expect("an id")
        .to_owned();
    assert!(
        dev.store().queued().expect("read").is_empty(),
        "the create's change ran"
    );
    assert_eq!(dev.store().armed("Ticket", &id).expect("read").len(), 1);
    now.fetch_add(HOUR, Ordering::SeqCst);
    // The next request runs the deadline that fell due.
    let read = dev
        .request(
            &caller(),
            &Request::Get {
                type_name: "Ticket".into(),
                id: id.clone(),
            },
        )
        .expect("read");
    assert_eq!(read["status"], "open", "read before the queues ran");
    let read = dev
        .request(
            &caller(),
            &Request::Get {
                type_name: "Ticket".into(),
                id: id.clone(),
            },
        )
        .expect("read");
    assert_eq!(read["status"], "closed");
    assert!(dev.store().armed("Ticket", &id).expect("read").is_empty());
    let rows = dev
        .store()
        .sub_rows("Ticket", &id, "Timers", "deadlines")
        .expect("read");
    assert_eq!(rows[0].values["fired"], json!(true));

    let mut manual = Dev::memory()
        .expect("SQLite opens")
        .with_drain(Drain::WhenAsked);
    manual
        .request(&caller(), &Request::Define { document: ticket() })
        .expect("defined");
    manual
        .request(
            &caller(),
            &Request::Publish {
                type_name: "Ticket".into(),
                discard: false,
            },
        )
        .expect("published");
    manual
        .request(
            &caller(),
            &Request::Create {
                type_name: "Ticket".into(),
                data: object(json!({"title": "t"})),
                params: Map::new(),
            },
        )
        .expect("created");
    assert_eq!(
        manual.store().queued().expect("read").len(),
        1,
        "the change waits"
    );
    assert_eq!(manual.run_due().expect("ran").len(), 1);
    assert!(manual.store().queued().expect("read").is_empty());
}
