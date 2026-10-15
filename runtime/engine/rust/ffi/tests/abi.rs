//! The C ABI through its exports, as a C host calls them: every export's
//! documents and codes, the binding's own calls, an event stream pulled
//! and woken, a panic caught, and the header held to the exports.

#![cfg(feature = "sqlite")]

use std::path::PathBuf;
use std::sync::atomic::{AtomicU64, Ordering};
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use serde_json::{json, Value};

use superschematic_engine_ffi::{
    abi, eng_abi_version, eng_call, eng_close, eng_free, eng_http, eng_open, eng_runner,
    eng_stream_close, eng_stream_poll, eng_wait, Project, ABI_VERSION, ENG_BAD_ARGUMENTS,
    ENG_CLOSED, ENG_ERROR, ENG_OK,
};

static NEXT: AtomicU64 = AtomicU64::new(0);

fn temp_dir() -> PathBuf {
    let nanos = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map_or(0, |since| since.as_nanos());
    let dir = std::env::temp_dir().join(format!(
        "engine-ffi-{nanos}-{}-{}",
        std::process::id(),
        NEXT.fetch_add(1, Ordering::Relaxed)
    ));
    std::fs::create_dir_all(&dir).expect("temp dir");
    dir
}

// taken copies an output and releases it.
unsafe fn taken(ptr: *mut u8, len: usize) -> Vec<u8> {
    if ptr.is_null() {
        assert_eq!(len, 0, "a null output has no length");
        return Vec::new();
    }
    let bytes = std::slice::from_raw_parts(ptr, len).to_vec();
    eng_free(ptr, len);
    bytes
}

fn document(bytes: &[u8]) -> Value {
    serde_json::from_slice(bytes)
        .unwrap_or_else(|error| panic!("{error}: {}", String::from_utf8_lossy(bytes)))
}

fn open(config: &Value) -> Result<(u64, Value), (i32, Value)> {
    let text = config.to_string();
    let mut engine = 0u64;
    let mut out = std::ptr::null_mut();
    let mut out_len = 0usize;
    let code = unsafe {
        eng_open(
            text.as_ptr(),
            text.len(),
            &mut engine,
            &mut out,
            &mut out_len,
        )
    };
    let bytes = unsafe { taken(out, out_len) };
    if code == ENG_OK {
        assert_ne!(engine, 0);
        Ok((engine, document(&bytes)))
    } else {
        Err((
            code,
            if bytes.is_empty() {
                Value::Null
            } else {
                document(&bytes)
            },
        ))
    }
}

fn opened() -> (u64, PathBuf) {
    let dir = temp_dir();
    let (engine, _) = open(&json!({
        "path": dir.join("engine.db").to_string_lossy(),
        "policy": "allowAll",
        "runner": {"principal": {"subject": "runner", "permissions": []}}
    }))
    .expect("open");
    (engine, dir)
}

// call makes a call and reads its output: (code, document or null).
fn call(engine: u64, request: &Value) -> (i32, Value) {
    let text = request.to_string();
    let mut out = std::ptr::null_mut();
    let mut out_len = 0usize;
    let code = unsafe { eng_call(engine, text.as_ptr(), text.len(), &mut out, &mut out_len) };
    let bytes = unsafe { taken(out, out_len) };
    (
        code,
        if bytes.is_empty() {
            Value::Null
        } else {
            document(&bytes)
        },
    )
}

fn ok(engine: u64, request: Value) -> Value {
    let (code, out) = call(engine, &request);
    assert_eq!(code, ENG_OK, "{request}: {out}");
    out
}

fn alice() -> Value {
    json!({"subject": "alice", "permissions": []})
}

fn note_schema() -> Value {
    json!({
        "kind": "General",
        "name": "Note",
        "types": {"Note": {"name": "Note", "role": "EmbeddedStruct", "fields": [
            {"name": "body", "typeRef": {"name": "string"}, "required": true}
        ]}}
    })
}

struct Response {
    code: i32,
    head: Value,
    body: Vec<u8>,
}

fn http(engine: u64, head: &Value, body: &[u8]) -> Response {
    let text = head.to_string();
    let (mut head_out, mut head_len) = (std::ptr::null_mut(), 0usize);
    let (mut body_out, mut body_len) = (std::ptr::null_mut(), 0usize);
    let code = unsafe {
        eng_http(
            engine,
            text.as_ptr(),
            text.len(),
            body.as_ptr(),
            body.len(),
            &mut head_out,
            &mut head_len,
            &mut body_out,
            &mut body_len,
        )
    };
    let head = unsafe { taken(head_out, head_len) };
    let body = unsafe { taken(body_out, body_len) };
    Response {
        code,
        head: if head.is_empty() {
            Value::Null
        } else {
            document(&head)
        },
        body,
    }
}

fn header<'h>(head: &'h Value, name: &str) -> Option<&'h str> {
    head["headers"]
        .as_array()?
        .iter()
        .find(|pair| pair[0] == name)
        .and_then(|pair| pair[1].as_str())
}

fn poll(engine: u64, stream: u64) -> (i32, String) {
    let (mut out, mut out_len) = (std::ptr::null_mut(), 0usize);
    let code = unsafe { eng_stream_poll(engine, stream, &mut out, &mut out_len) };
    let bytes = unsafe { taken(out, out_len) };
    (code, String::from_utf8(bytes).expect("UTF-8"))
}

#[test]
fn the_abi_version_is_the_headers() {
    assert_eq!(eng_abi_version(), ABI_VERSION);
    assert_eq!(ABI_VERSION, 1);
}

#[test]
fn an_engine_opens_with_the_cores_behaviors_and_closes() {
    let dir = temp_dir();
    let (engine, info) = open(&json!({
        "path": dir.join("engine.db").to_string_lossy(),
        "policy": "allowAll"
    }))
    .expect("open");
    assert_eq!(info["abi"], 1);
    assert_eq!(info["project"], "superschematic-engine-ffi");
    assert_eq!(info["dialect"], "sqlite");
    let names: Vec<&str> = info["behaviors"]
        .as_array()
        .expect("behaviors")
        .iter()
        .filter_map(Value::as_str)
        .collect();
    for name in superschematic_engine_behaviors::NAMES {
        assert!(names.contains(&name), "{name} is registered: {names:?}");
    }
    assert_eq!(ok(engine, json!({"op": "abi"})), info);
    assert_eq!(eng_close(engine), ENG_OK);
    assert_eq!(eng_close(engine), ENG_CLOSED, "a handle closes once");
    assert_eq!(call(engine, &json!({"op": "abi"})).0, ENG_CLOSED);
    assert_eq!(
        call(0, &json!({"op": "abi"})).0,
        ENG_CLOSED,
        "0 is no engine"
    );
    let _ = std::fs::remove_dir_all(dir);
}

#[test]
fn a_config_the_engine_cannot_take_is_a_type_error() {
    let (code, error) = open(&json!({"path": "x.db", "polcy": "allowAll"})).expect_err("refused");
    assert_eq!(code, ENG_ERROR);
    assert_eq!(error["error"]["class"], "TypeError");
    assert!(error["error"]["message"]
        .as_str()
        .unwrap()
        .contains("\"polcy\""));
    let (_, error) = open(&json!({"path": "x.db"})).expect_err("no policy");
    assert_eq!(error["error"]["class"], "TypeError");
    assert!(error["error"]["message"]
        .as_str()
        .unwrap()
        .contains("access policy"));
    let (_, error) = open(&json!({"path": "x.db", "policy": "allowAll", "project": {}}))
        .expect_err("the core's archive takes no project config");
    assert!(error["error"]["message"]
        .as_str()
        .unwrap()
        .contains("no project config"));
    let text = b"{not json";
    let mut engine = 0u64;
    let (mut out, mut out_len) = (std::ptr::null_mut(), 0usize);
    let code = unsafe {
        eng_open(
            text.as_ptr(),
            text.len(),
            &mut engine,
            &mut out,
            &mut out_len,
        )
    };
    assert_eq!(code, ENG_ERROR);
    assert_eq!(engine, 0, "no handle is written on a refusal");
    let error = document(&unsafe { taken(out, out_len) });
    assert_eq!(error["error"]["class"], "TypeError");
}

#[test]
fn a_call_returns_its_result_and_a_refusal_its_class_code_and_details() {
    let (engine, dir) = opened();
    let defined = ok(
        engine,
        json!({"op": "schemas.define", "principal": alice(), "args": [note_schema()]}),
    );
    assert_eq!(defined["name"], "Note");
    let published = ok(
        engine,
        json!({"op": "schemas.publish", "principal": alice(), "args": ["Note"]}),
    );
    assert_eq!(published["version"], 1);
    let created = ok(
        engine,
        json!({"op": "instances.create", "principal": alice(), "args": ["Note", {"body": "hi"}, {"id": "n1"}]}),
    );
    assert_eq!(created["id"], "n1");
    // A call that returns nothing (undefined) is an empty output.
    let (code, missing) = call(
        engine,
        &json!({"op": "instances.get", "principal": alice(), "args": ["Note", "n9"]}),
    );
    assert_eq!((code, missing), (ENG_OK, Value::Null));
    let (code, refused) = call(
        engine,
        &json!({"op": "instances.create", "principal": alice(), "args": ["Note", {"body": 7}]}),
    );
    assert_eq!(code, ENG_ERROR);
    let error = &refused["error"];
    assert_eq!(error["class"], "InstanceValidationError");
    assert_eq!(error["code"], "invalid_instance");
    assert!(error["details"]["issues"].is_array(), "{refused}");
    let (code, refused) = call(
        engine,
        &json!({"op": "instances.create", "args": ["Note", {"body": "x"}]}),
    );
    assert_eq!(code, ENG_ERROR);
    assert_eq!(refused["error"]["code"], "invalid_argument", "no principal");
    let (code, refused) = call(engine, &json!({"op": "instances.frobnicate"}));
    assert_eq!(code, ENG_ERROR);
    assert_eq!(refused["error"]["class"], "TypeError");
    let (code, refused) = call(engine, &json!({"args": []}));
    assert_eq!(
        (code, refused["error"]["class"].as_str()),
        (ENG_ERROR, Some("TypeError"))
    );
    let (code, refused) = call(engine, &json!({"op": "abi", "now": 5}));
    assert_eq!(code, ENG_ERROR, "the system clock is not moved: {refused}");
    eng_close(engine);
    let _ = std::fs::remove_dir_all(dir);
}

#[test]
fn deploy_publishes_each_document_once_and_leaves_a_live_one_as_it_is() {
    let (engine, dir) = opened();
    let first = ok(engine, json!({"op": "deploy", "args": [[note_schema()]]}));
    assert_eq!(
        first,
        json!([{"name": "Note", "namespace": "default", "version": 1, "defined": true, "published": true}])
    );
    let head = ok(engine, json!({"op": "events.head"}));
    let again = ok(
        engine,
        json!({"op": "deploy", "args": [[note_schema().to_string()]]}),
    );
    assert_eq!(
        again,
        json!([{"name": "Note", "namespace": "default", "version": 1, "defined": false, "published": false}])
    );
    assert_eq!(
        ok(engine, json!({"op": "events.head"})),
        head,
        "a deploy of the live version writes nothing"
    );
    let mut changed = note_schema();
    changed["types"]["Note"]["fields"]
        .as_array_mut()
        .unwrap()
        .push(json!({"name": "title", "typeRef": {"name": "string"}}));
    let next = ok(engine, json!({"op": "deploy", "args": [[changed]]}));
    assert_eq!(next[0]["version"], 2);
    assert_eq!(next[0]["published"], true);
    let (code, refused) = call(engine, &json!({"op": "deploy", "args": [{}]}));
    assert_eq!(
        (code, refused["error"]["class"].as_str()),
        (ENG_ERROR, Some("TypeError"))
    );
    let (code, refused) = call(
        engine,
        &json!({"op": "deploy", "args": [[{"kind": "General", "name": "Bad", "types": {}}]]}),
    );
    assert_eq!(code, ENG_ERROR);
    assert_eq!(refused["error"]["code"], "invalid_schema", "{refused}");
    eng_close(engine);
    let _ = std::fs::remove_dir_all(dir);
}

#[test]
fn health_is_ready_while_the_engine_is_open() {
    let (engine, dir) = opened();
    let health = ok(engine, json!({"op": "health"}));
    assert_eq!(
        health,
        json!({"ready": true, "closed": false, "poisoned": false, "dialect": "sqlite", "listening": false})
    );
    eng_close(engine);
    let _ = std::fs::remove_dir_all(dir);
}

#[test]
fn a_request_is_served_with_its_envelope_or_problem() {
    let (engine, dir) = opened();
    ok(engine, json!({"op": "deploy", "args": [[note_schema()]]}));
    let caller = json!({"principal": alice()});
    let created = http(
        engine,
        &json!({
            "method": "POST",
            "prefix": "/api",
            "path": "/api/namespaces/default/schemas/Note/instances",
            "headers": [["content-type", "application/json"]],
            "caller": caller,
            "requestId": "r1"
        }),
        br#"{"id": "n1", "data": {"body": "hello"}}"#,
    );
    assert_eq!(created.code, ENG_OK, "{}", created.head);
    assert_eq!(created.head["status"], 201);
    assert_eq!(created.head["stream"], Value::Null);
    assert_eq!(header(&created.head, "etag"), Some("\"1\""));
    assert_eq!(
        header(&created.head, "location"),
        Some("/api/namespaces/default/schemas/Note/instances/n1")
    );
    let body = document(&created.body);
    assert_eq!(body["data"]["id"], "n1");
    assert_eq!(body["meta"]["requestId"], "r1");
    let refused = http(
        engine,
        &json!({"method": "GET", "path": "/namespaces", "requestId": "r2"}),
        &[],
    );
    assert_eq!(refused.head["status"], 401);
    assert_eq!(document(&refused.body)["code"], "unauthorized");
    let host = http(
        engine,
        &json!({"method": "GET", "path": "/namespaces", "caller": {"refused": {"status": 401, "code": "service_unauthorized"}}}),
        &[],
    );
    assert_eq!(host.head["status"], 401);
    assert_eq!(document(&host.body)["code"], "service_unauthorized");
    let nothing = http(
        engine,
        &json!({"method": "GET", "path": "/nothing", "caller": caller}),
        &[],
    );
    assert_eq!(nothing.head["status"], 404);
    let bad = http(engine, &json!({"path": "/namespaces"}), &[]);
    assert_eq!(bad.code, ENG_ERROR);
    assert!(bad.body.is_empty());
    eng_close(engine);
    let _ = std::fs::remove_dir_all(dir);
}

#[test]
fn an_event_stream_is_pulled_without_waiting_and_woken_by_a_commit() {
    let (engine, dir) = opened();
    ok(engine, json!({"op": "deploy", "args": [[note_schema()]]}));
    let caller = json!({"principal": alice()});
    let opened = http(
        engine,
        &json!({
            "method": "GET",
            "path": "/namespaces/default/events",
            "headers": [["accept", "text/event-stream"]],
            "caller": caller,
            "requestId": "s1",
            "options": {"stream": {"heartbeatMs": 60000}}
        }),
        &[],
    );
    assert_eq!(opened.code, ENG_OK, "{}", opened.head);
    assert_eq!(opened.head["status"], 200);
    assert_eq!(
        header(&opened.head, "content-type"),
        Some("text/event-stream")
    );
    assert!(opened.body.is_empty());
    let stream = opened.head["stream"].as_u64().expect("a stream");
    assert_eq!(poll(engine, stream), (ENG_OK, ": open\n\n".to_owned()));
    let (code, replay) = poll(engine, stream);
    assert_eq!(code, ENG_OK);
    assert!(replay.contains("event: ready"), "{replay}");
    assert_eq!(poll(engine, stream), (ENG_OK, String::new()), "nothing yet");
    // One waiter, woken by a commit.
    let mut epoch = 0u64;
    assert_eq!(unsafe { eng_wait(engine, 0, 0, &mut epoch) }, ENG_OK);
    let mut same = 0u64;
    assert_eq!(unsafe { eng_wait(engine, epoch, 10, &mut same) }, ENG_OK);
    assert_eq!(same, epoch, "a wait that times out keeps the epoch");
    let waiter = std::thread::spawn(move || {
        let mut moved = 0u64;
        let code = unsafe { eng_wait(engine, epoch, 10_000, &mut moved) };
        (code, moved)
    });
    std::thread::sleep(Duration::from_millis(50));
    ok(
        engine,
        json!({"op": "instances.create", "principal": alice(), "args": ["Note", {"body": "live"}, {"id": "n2"}]}),
    );
    let (code, moved) = waiter.join().expect("waiter");
    assert_eq!(code, ENG_OK);
    assert!(moved > epoch, "the commit moved the epoch");
    let (code, live) = poll(engine, stream);
    assert_eq!(code, ENG_OK);
    assert!(live.contains("\"instanceId\":\"n2\""), "{live}");
    assert_eq!(eng_stream_close(engine, stream), ENG_OK);
    assert_eq!(
        poll(engine, stream).0,
        ENG_CLOSED,
        "a closed stream is over"
    );
    assert_eq!(eng_stream_close(engine, stream), ENG_CLOSED);
    // A close ends every stream and wakes every waiter.
    let again = http(
        engine,
        &json!({
            "method": "GET",
            "path": "/namespaces/default/events",
            "headers": [["accept", "text/event-stream"]],
            "caller": caller
        }),
        &[],
    );
    let stream = again.head["stream"].as_u64().expect("a stream");
    let mut current = 0u64;
    unsafe { eng_wait(engine, 0, 0, &mut current) };
    let waiter = std::thread::spawn(move || {
        let mut moved = 0u64;
        unsafe { eng_wait(engine, current, 10_000, &mut moved) }
    });
    std::thread::sleep(Duration::from_millis(50));
    assert_eq!(eng_close(engine), ENG_OK);
    assert_eq!(waiter.join().expect("waiter"), ENG_CLOSED);
    assert_eq!(poll(engine, stream).0, ENG_CLOSED);
    let _ = std::fs::remove_dir_all(dir);
}

#[test]
fn the_runner_starts_stops_and_runs_what_is_due() {
    let (engine, dir) = opened();
    let runner = |action: &str| {
        let text = json!({"action": action}).to_string();
        let (mut out, mut out_len) = (std::ptr::null_mut(), 0usize);
        let code = unsafe { eng_runner(engine, text.as_ptr(), text.len(), &mut out, &mut out_len) };
        let bytes = unsafe { taken(out, out_len) };
        (
            code,
            if bytes.is_empty() {
                Value::Null
            } else {
                document(&bytes)
            },
        )
    };
    let (code, pass) = runner("run_due");
    assert_eq!(code, ENG_OK, "{pass}");
    assert!(pass.is_object(), "{pass}");
    assert_eq!(runner("start"), (ENG_OK, Value::Null));
    assert_eq!(
        ok(engine, json!({"op": "runner.running"})),
        Value::Bool(true)
    );
    let (code, status) = runner("status");
    assert_eq!(code, ENG_OK);
    assert_eq!(status["running"], true, "{status}");
    assert_eq!(runner("stop"), (ENG_OK, Value::Null));
    let (code, refused) = runner("sprint");
    assert_eq!(
        (code, refused["error"]["class"].as_str()),
        (ENG_ERROR, Some("TypeError"))
    );
    eng_close(engine);
    let _ = std::fs::remove_dir_all(dir);
}

#[test]
fn null_pointers_write_nothing() {
    let (engine, dir) = opened();
    let text = b"{\"op\":\"abi\"}";
    let mut out_len = 0usize;
    let code = unsafe {
        eng_call(
            engine,
            text.as_ptr(),
            text.len(),
            std::ptr::null_mut(),
            &mut out_len,
        )
    };
    assert_eq!(code, ENG_BAD_ARGUMENTS);
    let mut out = std::ptr::null_mut();
    let code = unsafe { eng_call(engine, std::ptr::null(), 4, &mut out, &mut out_len) };
    assert_eq!(code, ENG_BAD_ARGUMENTS);
    assert!(out.is_null());
    let mut engine_out = 0u64;
    let code = unsafe {
        eng_open(
            text.as_ptr(),
            text.len(),
            std::ptr::null_mut(),
            &mut out,
            &mut out_len,
        )
    };
    assert_eq!(code, ENG_BAD_ARGUMENTS);
    let code = unsafe { eng_open(std::ptr::null(), 3, &mut engine_out, &mut out, &mut out_len) };
    assert_eq!(code, ENG_BAD_ARGUMENTS);
    assert_eq!(
        unsafe { eng_wait(engine, 0, 0, std::ptr::null_mut()) },
        ENG_BAD_ARGUMENTS
    );
    unsafe { eng_free(std::ptr::null_mut(), 0) };
    eng_close(engine);
    let _ = std::fs::remove_dir_all(dir);
}

fn panicking(
    _options: &mut superschematic_engine_abi::EngineOptions,
    _project: Option<&Value>,
) -> Result<(), String> {
    panic!("a project's configure gave up")
}

#[test]
fn a_panic_is_an_internal_error_not_an_abort() {
    let project = Project {
        name: "panicking",
        configure: panicking,
    };
    let text = json!({"path": "x.db", "policy": "allowAll"}).to_string();
    let mut engine = 0u64;
    let (mut out, mut out_len) = (std::ptr::null_mut(), 0usize);
    let code = unsafe {
        abi::open(
            &project,
            text.as_ptr(),
            text.len(),
            &mut engine,
            &mut out,
            &mut out_len,
        )
    };
    assert_eq!(code, ENG_ERROR);
    let error = document(&unsafe { taken(out, out_len) });
    assert_eq!(error["error"]["class"], "Error");
    assert_eq!(error["error"]["code"], "internal");
    assert_eq!(
        error["error"]["message"],
        "the engine panicked: a project's configure gave up"
    );
}

// The header's prototypes, as the exports' Rust signatures give them: a
// change to an export's signature fails to compile here, and a change to
// the header fails the comparison.
const PROTOTYPES: [(&str, &str); 10] = [
    ("eng_abi_version", "uint32_t eng_abi_version(void);"),
    (
        "eng_open",
        "int32_t eng_open(const uint8_t *config, size_t config_len, eng_engine *engine, uint8_t **out_ptr, size_t *out_len);",
    ),
    ("eng_close", "int32_t eng_close(eng_engine engine);"),
    (
        "eng_call",
        "int32_t eng_call(eng_engine engine, const uint8_t *request, size_t request_len, uint8_t **out_ptr, size_t *out_len);",
    ),
    (
        "eng_http",
        "int32_t eng_http(eng_engine engine, const uint8_t *head, size_t head_len, const uint8_t *body, size_t body_len, uint8_t **head_out_ptr, size_t *head_out_len, uint8_t **body_out_ptr, size_t *body_out_len);",
    ),
    (
        "eng_stream_poll",
        "int32_t eng_stream_poll(eng_engine engine, eng_stream stream, uint8_t **out_ptr, size_t *out_len);",
    ),
    (
        "eng_stream_close",
        "int32_t eng_stream_close(eng_engine engine, eng_stream stream);",
    ),
    (
        "eng_wait",
        "int32_t eng_wait(eng_engine engine, uint64_t after_epoch, uint32_t timeout_ms, uint64_t *epoch);",
    ),
    (
        "eng_runner",
        "int32_t eng_runner(eng_engine engine, const uint8_t *request, size_t request_len, uint8_t **out_ptr, size_t *out_len);",
    ),
    ("eng_free", "void eng_free(uint8_t *ptr, size_t len);"),
];

type Open = unsafe extern "C" fn(*const u8, usize, *mut u64, *mut *mut u8, *mut usize) -> i32;
type Json = unsafe extern "C" fn(u64, *const u8, usize, *mut *mut u8, *mut usize) -> i32;
type Http = unsafe extern "C" fn(
    u64,
    *const u8,
    usize,
    *const u8,
    usize,
    *mut *mut u8,
    *mut usize,
    *mut *mut u8,
    *mut usize,
) -> i32;
type Poll = unsafe extern "C" fn(u64, u64, *mut *mut u8, *mut usize) -> i32;
type Wait = unsafe extern "C" fn(u64, u64, u32, *mut u64) -> i32;

#[test]
fn the_header_declares_the_exports() {
    // Each export has the C type its prototype says.
    let _: extern "C" fn() -> u32 = eng_abi_version;
    let _: Open = eng_open;
    let _: extern "C" fn(u64) -> i32 = eng_close;
    let _: Json = eng_call;
    let _: Http = eng_http;
    let _: Poll = eng_stream_poll;
    let _: extern "C" fn(u64, u64) -> i32 = eng_stream_close;
    let _: Wait = eng_wait;
    let _: Json = eng_runner;
    let _: unsafe extern "C" fn(*mut u8, usize) = eng_free;
    let path =
        PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../../go/include/superschematic_engine.h");
    let header = std::fs::read_to_string(&path).expect("the header");
    // Prototypes, each joined onto one line with its whitespace collapsed.
    let mut declared = Vec::new();
    let mut current = String::new();
    for line in header.lines() {
        let line = line.trim();
        if current.is_empty()
            && !(line.starts_with("int32_t eng_")
                || line.starts_with("uint32_t eng_")
                || line.starts_with("void eng_"))
        {
            continue;
        }
        if !current.is_empty() {
            current.push(' ');
        }
        current.push_str(line);
        if line.ends_with(';') {
            declared.push(current.split_whitespace().collect::<Vec<_>>().join(" "));
            current.clear();
        }
    }
    let expected: Vec<String> = PROTOTYPES
        .iter()
        .map(|(_, text)| (*text).to_owned())
        .collect();
    assert_eq!(
        declared,
        expected,
        "{} declares other exports",
        path.display()
    );
    for (name, value) in [
        ("ENG_ABI_VERSION", ABI_VERSION as i32),
        ("ENG_OK", ENG_OK),
        ("ENG_ERROR", ENG_ERROR),
        ("ENG_BAD_ARGUMENTS", ENG_BAD_ARGUMENTS),
        ("ENG_CLOSED", ENG_CLOSED),
    ] {
        assert!(
            header.contains(&format!("#define {name} {value}\n")),
            "{} defines {name} as {value}",
            path.display()
        );
    }
    assert!(header.contains("typedef uint64_t eng_engine;"));
    assert!(header.contains("typedef uint64_t eng_stream;"));
}
