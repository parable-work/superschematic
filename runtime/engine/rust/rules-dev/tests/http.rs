//! The routes over HTTP with no transport under them (D58's paths, D117):
//! each request through `Dev::http` as the engine's host would hand it
//! over, answered with the envelope or a problem, a list's bracket filters
//! typed by their fields, an operation named by its name alone, the event
//! queue as a page, and the tools MCP lists.

use serde_json::{json, Map, Value};
use superschematic_rules::Caller;
use superschematic_rules_dev::{Dev, Served};
use superschematic_rules_handler::http::{HttpRequest, HttpResponse, JSON, MERGE_PATCH};
use superschematic_rules_handler::{Policy, Store, StoreWrite, Unit};

const PREFIX: &str = "/namespaces/default";

fn dev() -> Dev {
    Dev::memory()
        .expect("SQLite opens")
        .with_clock(|| 1_767_225_600_000)
}

fn call(
    dev: &mut Dev,
    caller: Option<&Caller>,
    method: &str,
    path: &str,
    body: Option<(&str, Value)>,
) -> HttpResponse {
    let (path, query) = path.split_once('?').unwrap_or((path, ""));
    let mut headers = Vec::new();
    let body = body.map(|(media, value)| {
        headers.push(("Content-Type".to_owned(), media.to_owned()));
        serde_json::to_vec(&value).expect("JSON")
    });
    let request = HttpRequest {
        method: method.into(),
        path: format!("{PREFIX}{path}"),
        query: query.into(),
        headers,
        body,
    };
    match dev.http(caller, &request, "req-1") {
        Served::Response(response) => response,
        other => panic!("{method} {path} served {other:?}"),
    }
}

fn alice() -> Caller {
    Caller::new("alice", ["Task"])
}

fn task() -> Value {
    json!({"name": "Task",
        "fields": [
            {"name": "title", "typeRef": {"name": "string"}, "required": true},
            {"name": "estimate", "typeRef": {"name": "number"}},
            {"name": "urgent", "typeRef": {"name": "boolean"}}
        ],
        "behaviors": [{"name": "Workflow", "field": "status", "config": {
            "states": ["todo", "doing", "done"],
            "transitions": [{"from": "todo", "to": "doing"}, {"from": "doing", "to": "done"}]}}]})
}

fn published(dev: &mut Dev) {
    let me = alice();
    let defined = call(dev, Some(&me), "POST", "/schemas", Some((JSON, task())));
    assert_eq!(defined.status, 200, "{:?}", defined.json());
    let published = call(dev, Some(&me), "POST", "/schemas/Task/publish", None);
    assert_eq!(published.status, 200, "{:?}", published.json());
}

#[test]
fn a_row_is_written_read_listed_and_deleted_over_the_engines_paths() {
    let mut dev = dev();
    let me = alice();
    published(&mut dev);
    let created = call(
        &mut dev,
        Some(&me),
        "POST",
        "/schemas/Task/instances",
        Some((
            JSON,
            json!({"id": "t1", "data": {"title": "Desk", "estimate": 3}}),
        )),
    );
    assert_eq!(created.status, 201, "{:?}", created.json());
    assert_eq!(created.header("etag"), Some("\"1\""));
    assert_eq!(
        created.header("location"),
        Some("/namespaces/default/schemas/Task/instances/t1")
    );
    assert_eq!(created.json()["data"]["status"], "todo");
    assert_eq!(created.json()["meta"]["requestId"], "req-1");
    for (id, estimate, urgent) in [("t2", 5, true), ("t3", 8, false)] {
        call(
            &mut dev,
            Some(&me),
            "POST",
            "/schemas/Task/instances",
            Some((
                JSON,
                json!({"id": id, "data": {"title": id, "estimate": estimate, "urgent": urgent}}),
            )),
        );
    }
    let got = call(
        &mut dev,
        Some(&me),
        "GET",
        "/schemas/Task/instances/t1",
        None,
    );
    assert_eq!(got.json()["data"]["title"], "Desk");

    lists(&mut dev, &me);

    // An update is a merge patch, and needs its media type.
    let wrong = call(
        &mut dev,
        Some(&me),
        "PATCH",
        "/schemas/Task/instances/t1",
        Some((JSON, json!({"title": "Lamp"}))),
    );
    assert_eq!(wrong.status, 415);
    assert_eq!(wrong.header("accept-patch"), Some(MERGE_PATCH));
    let updated = call(
        &mut dev,
        Some(&me),
        "PATCH",
        "/schemas/Task/instances/t1",
        Some((MERGE_PATCH, json!({"title": "Lamp"}))),
    );
    assert_eq!(updated.header("etag"), Some("\"2\""));

    // An operation is named by its name, which the one use that declares
    // it answers, or as <use>.<operation>.
    let moved = call(
        &mut dev,
        Some(&me),
        "POST",
        "/schemas/Task/instances/t1/operations/transition",
        Some((JSON, json!({"to": "doing"}))),
    );
    assert_eq!(moved.status, 200, "{:?}", moved.json());
    let moved = call(
        &mut dev,
        Some(&me),
        "POST",
        "/schemas/Task/instances/t1/operations/status.transition",
        Some((JSON, json!({"to": "done"}))),
    );
    assert_eq!(moved.status, 200, "{:?}", moved.json());
    let none = call(
        &mut dev,
        Some(&me),
        "POST",
        "/schemas/Task/instances/t1/operations/fly",
        None,
    );
    assert_eq!(none.status, 404);

    let deleted = call(
        &mut dev,
        Some(&me),
        "DELETE",
        "/schemas/Task/instances/t1",
        None,
    );
    assert_eq!(deleted.status, 200);
    let gone = call(
        &mut dev,
        Some(&me),
        "GET",
        "/schemas/Task/instances/t1",
        None,
    );
    assert_eq!(gone.status, 404);
    assert_eq!(gone.json()["title"], "Not Found");

    // The types, a live version's record and its describe.
    let types = call(&mut dev, Some(&me), "GET", "/schemas", None);
    assert_eq!(
        types.json()["data"],
        json!([{"namespace": "default", "name": "Task", "liveVersion": 1}])
    );
    let schema = call(&mut dev, Some(&me), "GET", "/schemas/Task", None);
    assert_eq!(schema.json()["data"]["version"], 1);
    assert_eq!(schema.json()["data"]["document"]["name"], "Task");
    let described = call(&mut dev, Some(&me), "GET", "/schemas/Task/describe", None);
    assert_eq!(described.json()["data"]["mode"], "development");
}

// lists holds a list to the bracket filters, typed by their fields, and a
// sort.
fn lists(dev: &mut Dev, me: &Caller) {
    let listed = call(
        dev,
        Some(me),
        "GET",
        "/schemas/Task/instances?filter[estimate][gte]=4&filter[status]=todo&sort=-estimate",
        None,
    );
    assert_eq!(ids_of(&listed, "id"), ["t3", "t2"], "{:?}", listed.json());
    let urgent = call(
        dev,
        Some(me),
        "GET",
        "/schemas/Task/instances?filter[urgent]=true&filter[title][in]=t1,%20t2",
        None,
    );
    assert_eq!(urgent.json()["data"]["items"][0]["id"], "t2");
    assert_eq!(
        urgent.json()["data"]["items"].as_array().map(Vec::len),
        Some(1)
    );
    let refused = call(
        dev,
        Some(me),
        "GET",
        "/schemas/Task/instances?filter[estimate]=many",
        None,
    );
    assert_eq!(refused.status, 400);
    assert_eq!(refused.json()["code"], "invalid_argument");
}

#[test]
fn a_request_with_no_caller_is_401_and_the_policy_answers_403() {
    let mut dev = dev().with_policy(Policy::Permission);
    let unauthorized = call(&mut dev, None, "GET", "/schemas", None);
    assert_eq!(unauthorized.status, 401);
    assert_eq!(unauthorized.json()["code"], "unauthorized");
    let reader = Caller::new("reader", ["Task.read"]);
    let refused = call(
        &mut dev,
        Some(&reader),
        "POST",
        "/schemas",
        Some((JSON, task())),
    );
    assert_eq!(refused.status, 403);
    assert_eq!(
        refused.json()["detail"],
        "reader may not define Task in namespace default"
    );
    let other = call(
        &mut dev,
        Some(&reader),
        "GET",
        "/../../namespaces/east/schemas",
        None,
    );
    assert_eq!(other.status, 404);
}

fn event(dev: &mut Dev, type_name: &str, id: &str, kind: &str) {
    let store = dev.store();
    store.begin().expect("begun");
    let applied = store
        .apply(&Unit {
            writes: vec![StoreWrite::Event {
                type_name: type_name.into(),
                id: id.into(),
                use_id: "Schedules".into(),
                kind: kind.into(),
                at: 1_767_225_600_000,
                payload: json!({"name": "nudge"}),
            }],
        })
        .expect("the event applies");
    assert!(applied);
    store.commit().expect("committed");
}

#[test]
fn the_event_queue_reads_as_a_page_from_a_cursor_kept_by_type() {
    let mut dev = dev().with_policy(
        Policy::from_json(&json!({"rules": [
            {"subject": "alice", "allow": true},
            {"action": "read", "schema": "Task", "allow": true}
        ]}))
        .expect("the policy reads"),
    );
    let me = alice();
    published(&mut dev);
    event(&mut dev, "Task", "t1", "run");
    event(&mut dev, "Secret", "s1", "run");
    event(&mut dev, "Task", "t2", "skip");
    let page = call(&mut dev, Some(&me), "GET", "/events", None);
    assert_eq!(
        page.json()["data"]["events"].as_array().map(Vec::len),
        Some(3)
    );
    assert_eq!(page.json()["data"]["next"], 3);
    let after = call(
        &mut dev,
        Some(&me),
        "GET",
        "/events?after=1&kind=skip",
        None,
    );
    let events = after.json()["data"]["events"].clone();
    assert_eq!(events.as_array().map(Vec::len), Some(1));
    assert_eq!(events[0]["instanceId"], "t2");
    assert_eq!(events[0]["schema"], "Task");
    assert_eq!(events[0]["at"], "2026-01-01T00:00:00.000Z");
    let head = call(&mut dev, Some(&me), "GET", "/events?after=head", None);
    assert_eq!(head.json()["data"]["events"], json!([]));
    assert_eq!(head.json()["data"]["next"], 3);

    // Another caller reads only the types it may read, and is refused a
    // read of one it may not.
    let bob = Caller::new("bob", Vec::<String>::new());
    let kept = call(&mut dev, Some(&bob), "GET", "/events", None);
    assert_eq!(ids_of(&kept, "schema"), ["Task", "Task"]);
    let refused = call(&mut dev, Some(&bob), "GET", "/events?schema=Secret", None);
    assert_eq!(refused.status, 403);
    let bad = call(&mut dev, Some(&bob), "GET", "/events?after=soon", None);
    assert_eq!(bad.status, 400);

    // A stream is the transport's to serve.
    let request = HttpRequest {
        method: "GET".into(),
        path: format!("{PREFIX}/events"),
        query: String::new(),
        headers: vec![("Accept".into(), "text/event-stream".into())],
        body: None,
    };
    assert!(matches!(
        dev.http(Some(&me), &request, "req-2"),
        Served::Stream { .. }
    ));
}

#[test]
fn the_tools_are_made_from_the_live_types_and_called_by_their_handles() {
    let mut dev = dev().with_policy(Policy::Permission);
    let me = Caller::new("alice", ["Task", "schemas"]);
    published(&mut dev);
    let tools = dev.tools(&me).expect("the tools");
    let names: Vec<&str> = tools
        .iter()
        .filter_map(|tool| tool["tool"]["name"].as_str())
        .collect();
    for name in [
        "list_schemas",
        "describe_schema",
        "define_schema",
        "publish_schema",
        "task_create",
        "task_get",
        "task_list",
        "task_update",
        "task_delete",
        "task_transition",
    ] {
        assert!(names.contains(&name), "{name} in {names:?}");
    }
    let created = dev
        .call_tool(
            &me,
            "task_create",
            &object(json!({"data": {"id": "t1", "title": "Desk"}})),
        )
        .expect("called")
        .expect("a tool")
        .expect("created");
    assert_eq!(created["status"], "todo");
    let moved = dev
        .call_tool(
            &me,
            "task_transition",
            &object(json!({"id": "t1", "params": {"to": "doing"}})),
        )
        .expect("called")
        .expect("a tool")
        .expect("moved");
    assert_eq!(moved["to"], "doing");
    assert!(dev
        .call_tool(&me, "task_fly", &Map::new())
        .expect("called")
        .is_none());
    // A caller the policy refuses writes is offered no tool that writes.
    let reader = Caller::new("reader", ["Task.read"]);
    let offered = dev.tools(&reader).expect("the tools");
    let names: Vec<&str> = offered
        .iter()
        .filter_map(|tool| tool["tool"]["name"].as_str())
        .collect();
    assert!(
        names.contains(&"task_get") && !names.contains(&"task_create"),
        "{names:?}"
    );
}

// ids_of is a member of each item of a page (`items`) or of the event
// queue's (`events`).
fn ids_of(response: &HttpResponse, member: &str) -> Vec<String> {
    let data = &response.json()["data"];
    let items = data
        .get("items")
        .or_else(|| data.get("events"))
        .and_then(Value::as_array)
        .cloned()
        .unwrap_or_default();
    items
        .iter()
        .filter_map(|item| item[member].as_str().map(str::to_owned))
        .collect()
}

fn object(value: Value) -> Map<String, Value> {
    value.as_object().cloned().expect("an object")
}
