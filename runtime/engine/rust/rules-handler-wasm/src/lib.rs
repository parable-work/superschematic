//! Development mode's request handler (D117; D103's step 6, "a library in
//! each language") for a host that reaches it through a C ABI:
//! `superschematic-rules-handler` over the core with the behaviors ported
//! onto it, every value of the ABI JSON ([`ffi`]).
//! `@superschematic/rules-handler` (`runtime/rules-handler/typescript`)
//! loads the `wasm32-unknown-unknown` build and runs the host loop over its
//! store, as `superschematic-rules-dev` runs it in Rust: it answers the
//! reads a run asks, applies the unit a run returns in the request's
//! transaction, runs a request again from fresh state when a fence did not
//! hold, up to [`MAX_ATTEMPTS`] times, and runs the change queue and the
//! deadlines due.
//!
//! The module imports nothing. It reads no clock and draws no random
//! number: a request's context carries its time, the host's id for it and
//! a fresh UUID, the key of a create that names none.
//!
//! Each operation is a JSON document in and one out:
//!
//! - `run`: `{"request": Request, "context": {"caller", "now", "request",
//!   "id"}, "answers"?: {"reads": [[StoreRead, StoreAnswer]]}, "policy"?:
//!   Policy}` to one step: `{"read": [StoreRead]}`, the reads to answer
//!   before the request runs again with them, or `{"done": {"response",
//!   "unit"}}` or `{"done": {"refused", "unit"}}`, the request's answer and
//!   the [`Unit`] to apply. With `"respond": {"answer", "requestId"}`, the
//!   answer a `route` gave, a done step carries the HTTP response it is
//!   written as too (`"http"`), so the response's body is the handler's own
//!   JSON. A refusal is an answer, not an error. `now` is
//!   epoch milliseconds, `request` the host's id for the request (kept for
//!   every run of it), and `id` a version 4 UUID the host drew, hyphenated.
//!   The request, the reads, the answers and the unit are the handler's
//!   serde forms ([`Request`], [`StoreRead`], [`StoreAnswer`], [`Unit`]),
//!   and the policy is D58's JSON (`"allowAll"` when absent).
//! - `route`: `{"request": {"method", "path", "query"?, "headers"?:
//!   [[name, value]], "body"?: text}, "requestId"}` to what the request
//!   routes to: `{"handle": {"request": Request, "answer": {"kind": "data" |
//!   "row" | "created" | "schema", "path"?}}}`, `{"events": EventsQuery}`,
//!   `{"mcp": {}}`, or `{"response": HttpResponse}`, the router's own
//!   answer. The path is the one after the host's prefix, still
//!   percent-encoded. An `EventsQuery` is `{"after": cursor | "head",
//!   "schema"?, "instanceId"?, "behaviors", "kinds", "limit", "stream"}`; an
//!   `HttpResponse` is `{"status", "headers": [[name, value]], "body":
//!   text}`.
//! - `respond`: `{"answer", "response" | "refused", "requestId"}` to the
//!   `HttpResponse` the handler's answer is written as; with no `answer`, a
//!   refusal's problem; `{"problem": {"status", "detail", "code",
//!   "details"?}, "requestId"}` to a problem of the host's own; and
//!   `{"unauthorized": true, "requestId"}` to the 401 a request with no
//!   caller answers.
//! - `events`: `{"mode", "caller", "query": EventsQuery, "events":
//!   [QueuedEvent], "head"?, "cursor"?, "policy"?}`. `admit` to
//!   `{"admitted": true}` or `{"refused"}`; `page`, the events after the
//!   query's start, to `{"page": {"events", "next", "more"}}` or
//!   `{"refused"}`; `since`, the events after `cursor`, to what a stream
//!   sends: `{"frames": text, "next", "ready": text}`, the server-sent
//!   events of the events the caller may read, the cursor to read on from
//!   and the `ready` event at it, or `{"refused"}`.
//! - `tool`: `{"call", "arguments"}`, a tool's call as `tools` lists it, to
//!   `{"request": Request}` or `{"refused"}`.
//! - `bridge`: `{"specs": {name: TypeSpec}, "spec", "command", "answers"?,
//!   "hearing"?: [type]}`, one command of the core over the layout's rows
//!   ([`Bridge`]), to `{"read": [StoreRead]}`, `{"refused"}` or
//!   `{"changed": {"changes": ChangeSet, "writes": [StoreWrite]}}`: what a
//!   replay of the recorded scenarios runs.
//! - `core`: `{"spec", "command", "answers"?}` to the core's step, as
//!   `@superschematic/rules`' `run` returns it: what a replay's define and
//!   publish checks run.
//! - `declare`: `{"declarations": [declaration file]}`, the declaration
//!   files of declarative behaviors (D103's step 5), each whole, to
//!   `{"declared": [name]}`, every declarative behavior the handler runs
//!   since: every operation runs them after the core's own, and `layout`
//!   lists them. A file the vocabulary does not read, one that names a
//!   behavior the core implements, and a second file of a declared name
//!   that differs are the error document `invalid_input` with the reason.
//! - `layout`: no input, to what a store's tables are made from and the
//!   bounds of its host loop: `{"namespace", "behaviors": [{"name",
//!   "storageKey", "rowTables", "tables": [{"name", "shared"}],
//!   "armsDeadlines", "hearsChanges"}], "maxAttempts", "maxReadRounds",
//!   "maxRuns", "heartbeat"}`. A behavior with `rowTables` keeps sub-table
//!   rows per row, in its shared sub-table.
//!
//! An input the operation cannot read is an error document, `{"error":
//! {"code", "message"}}` with the code `invalid_json` or `invalid_input`,
//! and a panic `internal`; [`run_json`] says which by its second value.
//!
//! The core and the declarations are built once per process (on wasm, once
//! per instance), the first time an operation runs, and kept; a `declare`
//! that adds a behavior builds both again.
//!
//! [`StoreRead`]: superschematic_rules_handler::StoreRead
//! [`StoreAnswer`]: superschematic_rules_handler::StoreAnswer
//! [`Unit`]: superschematic_rules_handler::Unit

pub mod ffi;

use std::collections::{BTreeMap, BTreeSet};
use std::sync::{Arc, LazyLock, PoisonError, RwLock};

use serde::de::DeserializeOwned;
use serde::{Deserialize, Serialize};
use serde_json::{json, Map, Value};
use superschematic_rules::{Answers, Caller, Command, Core, Refusal, TypeSpec};
use superschematic_rules_handler::access::NAMESPACE;
use superschematic_rules_handler::document::storage_key;
use superschematic_rules_handler::events::{self, After, EventsQuery};
use superschematic_rules_handler::http::{self, Answer, HttpRequest, HttpResponse, Routed};
use superschematic_rules_handler::values::uuid_of;
use superschematic_rules_handler::{
    tools, Bridge, Context, Declarations, Done, Handler, HandlerStep, Policy, QueuedEvent, Ran,
    Request, StoreAnswers, MAX_ATTEMPTS, MAX_READ_ROUNDS, MAX_RUNS,
};

// The core every operation runs on, each behavior ported so far and each
// declarative one a declare added, and the declarations of those
// behaviors, built the first time an operation runs.
static STATE: LazyLock<RwLock<Arc<State>>> = LazyLock::new(|| {
    RwLock::new(Arc::new(State {
        core: superschematic_rules_behaviors::core(),
        declarations: Declarations::ported(),
        files: Vec::new(),
    }))
});

// The core, its behaviors' declarations, and the declaration files of the
// declarative behaviors among them.
struct State {
    core: Core,
    declarations: Declarations,
    files: Vec<Value>,
}

// The state as declare last left it.
fn current() -> Arc<State> {
    STATE.read().unwrap_or_else(PoisonError::into_inner).clone()
}

// declare adds the declarative behaviors of declaration files, building
// the core and the declarations again when one is new.
fn declare(state: &RwLock<Arc<State>>, input: DeclareInput) -> Result<Value, Failure> {
    let mut held = state.write().unwrap_or_else(PoisonError::into_inner);
    let mut files = held.files.clone();
    for file in input.declarations {
        if !files.contains(&file) {
            files.push(file);
        }
    }
    let declared = superschematic_rules_behaviors::declaratives(&files)
        .map_err(|reason| ("invalid_input", format!("declare: {reason}")))?;
    let names: Vec<Value> = declared
        .iter()
        .map(|behavior| Value::from(behavior.name()))
        .collect();
    let files: Vec<Value> = declared
        .iter()
        .map(|behavior| behavior.document().clone())
        .collect();
    if files != held.files {
        let declarations = Declarations::ported_with(&files)
            .map_err(|reason| ("invalid_input", format!("declare: {reason}")))?;
        let mut core = superschematic_rules_behaviors::core();
        for behavior in declared {
            core.push(behavior);
        }
        *held = Arc::new(State {
            core,
            declarations,
            files,
        });
    }
    Ok(json!({ "declared": names }))
}

/// `declare`'s input.
#[derive(Deserialize)]
struct DeclareInput {
    declarations: Vec<Value>,
}

/// An operation of the ABI.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Op {
    Run,
    Route,
    Respond,
    Events,
    Tool,
    Bridge,
    Core,
    Layout,
    Declare,
}

impl Op {
    /// The operation's name, as an error names it.
    pub fn name(self) -> &'static str {
        match self {
            Op::Run => "run",
            Op::Route => "route",
            Op::Respond => "respond",
            Op::Events => "events",
            Op::Tool => "tool",
            Op::Bridge => "bridge",
            Op::Core => "core",
            Op::Layout => "layout",
            Op::Declare => "declare",
        }
    }
}

/// Why an input was refused: its code and message.
type Failure = (&'static str, String);

/// Runs an operation on its JSON input and returns its output document,
/// with true for the operation's output and false for an error document.
pub fn run_json(op: Op, input: &[u8]) -> (String, bool) {
    match output(op, input) {
        Ok(value) => (value.to_string(), true),
        Err((code, message)) => (error_document(code, &message), false),
    }
}

/// The error document of a refused input: `{"error": {"code", "message"}}`.
pub fn error_document(code: &str, message: &str) -> String {
    json!({ "error": { "code": code, "message": message } }).to_string()
}

fn output(op: Op, input: &[u8]) -> Result<Value, Failure> {
    match op {
        Op::Run => run(op, parse(op, input)?),
        Op::Route => route(parse(op, input)?),
        Op::Respond => respond(op, &parse::<Value>(op, input)?),
        Op::Events => events_of(op, parse(op, input)?),
        Op::Tool => tool(parse(op, input)?),
        Op::Bridge => bridge(parse(op, input)?),
        Op::Core => {
            let input: CoreInput = parse(op, input)?;
            to_json(
                &current()
                    .core
                    .run(&input.spec, &input.command, &input.answers),
            )
        }
        Op::Layout => Ok(layout()),
        Op::Declare => declare(&STATE, parse(op, input)?),
    }
}

// parse reads an operation's input: `invalid_json` for text that is no
// JSON, `invalid_input` for JSON of another shape.
fn parse<T: DeserializeOwned>(op: Op, input: &[u8]) -> Result<T, Failure> {
    let document: Value = serde_json::from_slice(input)
        .map_err(|error| ("invalid_json", format!("{}: {error}", op.name())))?;
    serde_json::from_value(document)
        .map_err(|error| ("invalid_input", format!("{}: {error}", op.name())))
}

fn to_json(value: &impl Serialize) -> Result<Value, Failure> {
    serde_json::to_value(value).map_err(|error| ("internal", error.to_string()))
}

/// A request's context as a host sends it: its caller, its time in epoch
/// milliseconds, the host's id for it, and a fresh UUID.
#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
struct ContextInput {
    caller: Caller,
    now: i64,
    #[serde(default)]
    request: String,
    id: String,
}

/// `run`'s input.
#[derive(Deserialize)]
struct RunInput {
    request: Request,
    context: ContextInput,
    #[serde(default)]
    answers: StoreAnswers,
    #[serde(default)]
    policy: Policy,
    /// How a routed request's answer is written, when the host wants its
    /// HTTP response with the answer.
    #[serde(default)]
    respond: Option<RespondWith>,
}

/// A routed request's answer and the host's id for it.
#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
struct RespondWith {
    answer: Value,
    #[serde(default)]
    request_id: String,
}

fn run(op: Op, input: RunInput) -> Result<Value, Failure> {
    let ContextInput {
        caller,
        now,
        request,
        id,
    } = input.context;
    let id = uuid_of(&id).ok_or_else(|| {
        (
            "invalid_input",
            format!(
                "{}: context.id is a version 4 UUID the host drew, not {id:?}",
                op.name()
            ),
        )
    })?;
    let context = Context {
        caller,
        now,
        request,
        id,
    };
    let state = current();
    let handler = Handler::new(&state.core, &state.declarations).with_policy(&input.policy);
    let (response, unit) = match handler.run(&input.request, &context, &input.answers) {
        HandlerStep::Read(reads) => return Ok(json!({ "read": to_json(&reads)? })),
        HandlerStep::Done(Done { response, unit }) => (response, unit),
    };
    let mut done = Map::new();
    if let Some(respond) = &input.respond {
        let answer = answer_of(op, &respond.answer)?;
        let written = http::respond(&answer, response.clone(), &respond.request_id);
        done.insert("http".into(), response_json(&written));
    }
    match response {
        Ok(response) => done.insert("response".into(), response),
        Err(refusal) => done.insert("refused".into(), to_json(&refusal)?),
    };
    done.insert("unit".into(), to_json(&unit)?);
    Ok(json!({ "done": done }))
}

/// An HTTP request as a host sends it, its body as text.
#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
struct HttpInput {
    #[serde(default)]
    method: String,
    #[serde(default)]
    path: String,
    #[serde(default)]
    query: String,
    #[serde(default)]
    headers: Vec<(String, String)>,
    #[serde(default)]
    body: Option<String>,
}

/// `route`'s input.
#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
struct RouteInput {
    request: HttpInput,
    #[serde(default)]
    request_id: String,
}

fn route(input: RouteInput) -> Result<Value, Failure> {
    let HttpInput {
        method,
        path,
        query,
        headers,
        body,
    } = input.request;
    let request = HttpRequest {
        method,
        path,
        query,
        headers,
        body: body.map(String::into_bytes),
    };
    Ok(match http::route(&request, &input.request_id) {
        Routed::Handle { request, answer } => json!({
            "handle": { "request": to_json(&request)?, "answer": answer_json(&answer) }
        }),
        Routed::Events(query) => json!({ "events": query_json(&query) }),
        Routed::Mcp => json!({ "mcp": {} }),
        Routed::Answer(response) => json!({ "response": response_json(&response) }),
    })
}

fn answer_json(answer: &Answer) -> Value {
    match answer {
        Answer::Data => json!({ "kind": "data" }),
        Answer::Row => json!({ "kind": "row" }),
        Answer::Created { path } => json!({ "kind": "created", "path": path }),
        Answer::Schema => json!({ "kind": "schema" }),
        Answer::NoContent => json!({ "kind": "noContent" }),
    }
}

fn answer_of(op: Op, value: &Value) -> Result<Answer, Failure> {
    match value.get("kind").and_then(Value::as_str) {
        Some("data") => Ok(Answer::Data),
        Some("row") => Ok(Answer::Row),
        Some("created") => Ok(Answer::Created {
            path: value
                .get("path")
                .and_then(Value::as_str)
                .unwrap_or_default()
                .to_owned(),
        }),
        Some("schema") => Ok(Answer::Schema),
        Some("noContent") => Ok(Answer::NoContent),
        _ => Err((
            "invalid_input",
            format!(
                "{}: an answer is {{\"kind\": \"data\" | \"row\" | \"created\" | \"schema\" | \"noContent\"}}, not {value}",
                op.name()
            ),
        )),
    }
}

/// An HTTP response as a host reads it: its status, its headers and its
/// body as text (every body the router writes is JSON).
fn response_json(response: &HttpResponse) -> Value {
    json!({
        "status": response.status,
        "headers": response.headers.iter().map(|(name, value)| json!([name, value])).collect::<Vec<_>>(),
        "body": String::from_utf8_lossy(&response.body),
    })
}

fn query_json(query: &EventsQuery) -> Value {
    json!({
        "after": match query.after {
            After::Cursor(cursor) => Value::from(cursor),
            After::Head => Value::from("head"),
        },
        "schema": query.schema,
        "instanceId": query.instance_id,
        "behaviors": query.behaviors,
        "kinds": query.kinds,
        "limit": query.limit,
        "stream": query.stream,
        "signals": query.signals,
    })
}

fn query_of(op: Op, value: &Value) -> Result<EventsQuery, Failure> {
    let refused = |what: &str| {
        (
            "invalid_input",
            format!("{}: an events query's {what}", op.name()),
        )
    };
    let after = match value.get("after") {
        None | Some(Value::Null) => After::Cursor(0),
        Some(Value::String(head)) if head == "head" => After::Head,
        Some(cursor) => After::Cursor(
            cursor
                .as_i64()
                .ok_or_else(|| refused("after is a cursor or \"head\""))?,
        ),
    };
    let text = |name: &str| value.get(name).and_then(Value::as_str).map(str::to_owned);
    let list = |name: &str| -> Vec<String> {
        value
            .get(name)
            .and_then(Value::as_array)
            .map(|items| {
                items
                    .iter()
                    .filter_map(Value::as_str)
                    .map(str::to_owned)
                    .collect()
            })
            .unwrap_or_default()
    };
    let limit = match value.get("limit") {
        None | Some(Value::Null) => events::DEFAULT_PAGE,
        Some(limit) => limit
            .as_u64()
            .and_then(|limit| usize::try_from(limit).ok())
            .ok_or_else(|| refused("limit is a whole number"))?,
    };
    Ok(EventsQuery {
        after,
        schema: text("schema"),
        instance_id: text("instanceId"),
        behaviors: list("behaviors"),
        kinds: list("kinds"),
        limit,
        stream: value.get("stream") == Some(&Value::Bool(true)),
        signals: value.get("signals") == Some(&Value::Bool(true)),
    })
}

fn respond(op: Op, input: &Value) -> Result<Value, Failure> {
    let request_id = input
        .get("requestId")
        .and_then(Value::as_str)
        .unwrap_or_default();
    if input.get("unauthorized") == Some(&Value::Bool(true)) {
        return Ok(response_json(&http::unauthorized(request_id)));
    }
    if let Some(problem) = input.get("problem") {
        let status = problem
            .get("status")
            .and_then(Value::as_u64)
            .and_then(|status| u16::try_from(status).ok())
            .ok_or((
                "invalid_input",
                format!("{}: a problem's status is an HTTP status", op.name()),
            ))?;
        let text = |name: &str| {
            problem
                .get(name)
                .and_then(Value::as_str)
                .unwrap_or_default()
        };
        return Ok(response_json(&http::problem(
            status,
            text("detail"),
            text("code"),
            problem.get("details").filter(|d| !d.is_null()).cloned(),
            request_id,
        )));
    }
    let outcome = match input.get("refused") {
        Some(refused) => Err(serde_json::from_value::<Refusal>(refused.clone())
            .map_err(|error| ("invalid_input", format!("{}: refused: {error}", op.name())))?),
        None => Ok(input.get("response").cloned().unwrap_or(Value::Null)),
    };
    let response = match input.get("answer") {
        Some(answer) => http::respond(&answer_of(op, answer)?, outcome, request_id),
        None => match outcome {
            Err(refusal) => http::refusal_problem(&refusal, request_id),
            Ok(data) => http::envelope(data, 200, Vec::new(), request_id),
        },
    };
    Ok(response_json(&response))
}

/// `events`' input.
#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
struct EventsInput {
    mode: String,
    caller: Caller,
    #[serde(default)]
    query: Value,
    #[serde(default)]
    events: Vec<QueuedEvent>,
    #[serde(default)]
    head: i64,
    #[serde(default)]
    cursor: i64,
    #[serde(default)]
    policy: Policy,
}

fn events_of(op: Op, input: EventsInput) -> Result<Value, Failure> {
    let query = query_of(op, &input.query)?;
    let state = current();
    let handler = Handler::new(&state.core, &state.declarations).with_policy(&input.policy);
    let refused = |refusal: Refusal| -> Result<Value, Failure> {
        Ok(json!({ "refused": to_json(&refusal)? }))
    };
    match input.mode.as_str() {
        "admit" => match handler.admit_events(&input.caller, &query) {
            Ok(()) => Ok(json!({ "admitted": true })),
            Err(refusal) => refused(refusal),
        },
        "page" => match handler.events_page(&input.caller, &query, &input.events, input.head) {
            Ok(page) => Ok(json!({ "page": page })),
            Err(refusal) => refused(refusal),
        },
        "since" => {
            if let Err(refusal) = handler.admit_events(&input.caller, &query) {
                return refused(refusal);
            }
            let (kept, next, _) =
                handler.events_after(&input.caller, &query, &input.events, usize::MAX);
            let next = next.max(input.cursor);
            let frames: String = kept.into_iter().map(events::frame).collect();
            Ok(json!({ "frames": frames, "next": next, "ready": events::ready_frame(next) }))
        }
        other => Err((
            "invalid_input",
            format!(
                "{}: mode is \"admit\", \"page\" or \"since\", not {other:?}",
                op.name()
            ),
        )),
    }
}

/// `tool`'s input.
#[derive(Deserialize)]
struct ToolInput {
    call: Value,
    #[serde(default)]
    arguments: Map<String, Value>,
}

fn tool(input: ToolInput) -> Result<Value, Failure> {
    Ok(match tools::request_of(&input.call, &input.arguments) {
        Ok(request) => json!({ "request": to_json(&request)? }),
        Err(refusal) => json!({ "refused": to_json(&refusal)? }),
    })
}

/// `bridge`'s input.
#[derive(Deserialize)]
struct BridgeInput {
    #[serde(default)]
    specs: BTreeMap<String, TypeSpec>,
    spec: TypeSpec,
    command: Command,
    #[serde(default)]
    answers: StoreAnswers,
    #[serde(default)]
    hearing: BTreeSet<String>,
}

fn bridge(input: BridgeInput) -> Result<Value, Failure> {
    let state = current();
    let bridge = Bridge {
        core: &state.core,
        specs: &input.specs,
        hearing: &input.hearing,
        own: None,
        readable: None,
    };
    Ok(
        match bridge.run(&input.spec, &input.command, &input.answers) {
            Ran::Read(reads) => json!({ "read": to_json(&reads)? }),
            Ran::Refused(refusal) => json!({ "refused": to_json(&refusal)? }),
            Ran::Changed(changed) => json!({
                "changed": { "changes": to_json(&changed.changes)?, "writes": to_json(&changed.writes)? }
            }),
        },
    )
}

/// `core`'s input.
#[derive(Deserialize)]
struct CoreInput {
    spec: TypeSpec,
    command: Command,
    #[serde(default)]
    answers: Answers,
}

fn layout() -> Value {
    let state = current();
    let behaviors: Vec<Value> = state
        .declarations
        .iter()
        .map(|declared| {
            json!({
                "name": declared.name,
                "storageKey": storage_key(&declared.name),
                "rowTables": declared.has_row_tables(),
                "tables": declared.tables.iter().map(|table| json!({"name": table.name, "shared": table.shared})).collect::<Vec<_>>(),
                "armsDeadlines": declared.arms_deadlines,
                "hearsChanges": declared.hears_changes,
            })
        })
        .collect();
    json!({
        "namespace": NAMESPACE,
        "behaviors": behaviors,
        "maxAttempts": MAX_ATTEMPTS,
        "maxReadRounds": MAX_READ_ROUNDS,
        "maxRuns": MAX_RUNS,
        "heartbeat": events::HEARTBEAT,
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    fn call(op: Op, input: Value) -> Value {
        let (out, ok) = run_json(op, input.to_string().as_bytes());
        assert!(ok, "{out}");
        serde_json::from_str(&out).expect("the output is JSON")
    }

    fn context() -> Value {
        json!({"caller": {"subject": "alice"}, "now": 1_000, "request": "r1", "id": "00000000-0000-4000-8000-000000000007"})
    }

    /// A declare adds a declarative behavior (D103's step 5) to the core
    /// and to the declarations a store's tables are made from; the same
    /// file again is one behavior, and one the vocabulary does not read is
    /// refused, leaving the state as it was. A state of the test's own keeps
    /// the process's as it is for the other tests.
    #[test]
    fn declare_adds_a_declarative_behavior_to_the_core_and_the_layout() {
        let votes: Value = serde_json::from_str(include_str!(
            "../../../testdata/rules/declarations/test.Votes.behavior.json"
        ))
        .unwrap();
        let state = RwLock::new(Arc::new(State {
            core: superschematic_rules_behaviors::core(),
            declarations: Declarations::ported(),
            files: Vec::new(),
        }));
        let input = || DeclareInput {
            declarations: vec![votes.clone()],
        };
        assert_eq!(
            declare(&state, input()),
            Ok(json!({"declared": ["test.Votes"]}))
        );
        let first = state.read().unwrap().clone();
        assert_eq!(
            declare(&state, input()),
            Ok(json!({"declared": ["test.Votes"]}))
        );
        // The same file again builds nothing.
        assert!(Arc::ptr_eq(&first, &state.read().unwrap()));
        assert!(first.core.declaration("test.Votes").is_some());
        let declared = first.declarations.get("test.Votes").expect("declared");
        assert!(declared.has_row_tables());
        let refused = declare(
            &state,
            DeclareInput {
                declarations: vec![json!({"name": "test.Plain"})],
            },
        );
        assert!(matches!(refused, Err(("invalid_input", _))), "{refused:?}");
        assert_eq!(state.read().unwrap().files, [votes]);
    }

    #[test]
    fn a_run_asks_its_reads_then_answers_with_its_unit() {
        let order =
            json!({"name": "Order", "fields": [{"name": "title", "typeRef": {"name": "string"}}]});
        // A define reads nothing: its unit keeps the draft.
        let step = call(
            Op::Run,
            json!({"request": {"kind": "define", "document": order}, "context": context()}),
        );
        assert_eq!(
            step["done"]["response"],
            json!({"type": "Order", "defined": true})
        );
        assert_eq!(step["done"]["unit"]["writes"][0]["write"], "putDraft");
        // A get asks the live versions and the row, then answers.
        let request = json!({"kind": "get", "type": "Order", "id": "o1"});
        let step = call(Op::Run, json!({"request": request, "context": context()}));
        assert_eq!(
            step,
            json!({"read": [{"kind": "live"}, {"kind": "row", "type": "Order", "id": "o1"}]})
        );
        let answered = json!([
            [{"kind": "live"}, {"live": [{"type": "Order", "version": 1, "document": order}]}],
            [{"kind": "row", "type": "Order", "id": "o1"}, {"row": null}]
        ]);
        let step = call(
            Op::Run,
            json!({"request": request, "context": context(), "answers": {"reads": answered}}),
        );
        assert_eq!(step["done"]["refused"]["code"], "not_found");
        assert_eq!(step["done"]["unit"], json!({"writes": []}));
        // With the answer a route gave, the step carries its HTTP response.
        let step = call(
            Op::Run,
            json!({"request": request, "context": context(), "answers": {"reads": answered},
                "respond": {"answer": {"kind": "row"}, "requestId": "q9"}}),
        );
        assert_eq!(step["done"]["http"]["status"], 404);
        assert!(step["done"]["http"]["body"]
            .as_str()
            .is_some_and(|body| body.contains(r#""requestId":"q9""#)));
    }

    #[test]
    fn a_run_refused_by_the_policy_is_a_refusal_with_an_empty_unit() {
        let request = json!({"kind": "describe", "type": "Order"});
        let policy = json!({"rules": [{"subject": "alice", "allow": false}]});
        let step = call(
            Op::Run,
            json!({"request": request, "context": context(), "policy": policy}),
        );
        assert_eq!(step["done"]["refused"]["code"], "forbidden");
        assert_eq!(step["done"]["unit"], json!({"writes": []}));
    }

    #[test]
    fn a_context_with_no_uuid_is_refused_as_an_input() {
        let mut context = context();
        context["id"] = json!("not a uuid at all, far too long");
        let (out, ok) = run_json(
            Op::Run,
            json!({"request": {"kind": "types"}, "context": context})
                .to_string()
                .as_bytes(),
        );
        assert!(!ok);
        assert!(out.contains("invalid_input"), "{out}");
    }

    #[test]
    fn route_and_respond_carry_the_routers_answers() {
        let routed = call(
            Op::Route,
            json!({"request": {"method": "POST", "path": "/namespaces/default/schemas/Task/instances",
                "headers": [["Content-Type", "application/json"]], "body": "{\"data\": {\"title\": \"a\"}}"}, "requestId": "q1"}),
        );
        assert_eq!(routed["handle"]["request"]["kind"], "create");
        assert_eq!(
            routed["handle"]["answer"],
            json!({"kind": "created", "path": "/namespaces/default/schemas/Task/instances"})
        );
        let written = call(
            Op::Respond,
            json!({"answer": routed["handle"]["answer"], "response": {"id": "t 1", "_version": 1}, "requestId": "q1"}),
        );
        assert_eq!(written["status"], 201);
        assert!(written["headers"]
            .as_array()
            .expect("headers")
            .contains(&json!([
                "location",
                "/namespaces/default/schemas/Task/instances/t%201"
            ])));
        let missing = call(
            Op::Route,
            json!({"request": {"method": "GET", "path": "/namespaces/east/schemas"}, "requestId": "q2"}),
        );
        assert_eq!(missing["response"]["status"], 404);
        let events = call(
            Op::Route,
            json!({"request": {"method": "GET", "path": "/namespaces/default/events", "query": "after=head&kind=run",
                "headers": [["Accept", "text/event-stream"]]}}),
        );
        assert_eq!(
            events["events"],
            json!({"after": "head", "schema": null, "instanceId": null, "behaviors": [], "kinds": ["run"], "limit": 100, "stream": true, "signals": false})
        );
        let mcp = call(
            Op::Route,
            json!({"request": {"method": "POST", "path": "/namespaces/default/mcp"}}),
        );
        assert_eq!(mcp, json!({"mcp": {}}));
        let unauthorized = call(
            Op::Respond,
            json!({"unauthorized": true, "requestId": "q3"}),
        );
        assert_eq!(unauthorized["status"], 401);
        let refused = call(
            Op::Respond,
            json!({"refused": {"code": "busy", "status": 503, "class": "EngineError", "message": "m"}, "requestId": "q4"}),
        );
        assert_eq!(refused["headers"][0], json!(["retry-after", "1"]));
        let problem = call(
            Op::Respond,
            json!({"problem": {"status": 413, "detail": "too big", "code": "payload_too_large"}, "requestId": "q5"}),
        );
        assert_eq!(problem["status"], 413);
    }

    #[test]
    fn events_admit_page_and_frame_what_the_caller_may_read() {
        let event = |seq: i64, type_name: &str| json!({"seq": seq, "type": type_name, "id": format!("r{seq}"), "useId": "Schedules", "kind": "run", "at": 1_767_225_600_000_i64, "payload": {"n": seq}});
        let query =
            json!({"after": 0, "behaviors": [], "kinds": [], "limit": 100, "stream": false});
        let page = call(
            Op::Events,
            json!({"mode": "page", "caller": {"subject": "a"}, "query": query, "events": [event(1, "Job"), event(2, "Task")], "head": 2,
                "policy": {"rules": [{"schema": "Task", "allow": false}, {"allow": true}]}}),
        );
        assert_eq!(page["page"]["events"].as_array().map(Vec::len), Some(1));
        assert_eq!(page["page"]["next"], 2);
        let since = call(
            Op::Events,
            json!({"mode": "since", "caller": {"subject": "a"}, "query": query, "events": [event(3, "Job")], "cursor": 2}),
        );
        assert_eq!(since["next"], 3);
        assert!(since["frames"]
            .as_str()
            .is_some_and(|frames| frames.starts_with("id: 3\ndata: {\"cursor\":3,")));
        assert_eq!(
            since["ready"],
            "id: 3\nevent: ready\ndata: {\"cursor\":3}\n\n"
        );
        let refused = call(
            Op::Events,
            json!({"mode": "admit", "caller": {"subject": ""}, "query": query}),
        );
        assert_eq!(refused["refused"]["code"], "invalid_argument");
    }

    #[test]
    fn a_tools_call_is_its_request() {
        let request = call(
            Op::Tool,
            json!({"call": {"kind": "get", "type": "Order"}, "arguments": {"id": "o1"}}),
        );
        assert_eq!(
            request["request"],
            json!({"kind": "get", "type": "Order", "id": "o1"})
        );
    }

    #[test]
    fn the_layout_names_each_behaviors_tables() {
        let layout = call(Op::Layout, Value::Null);
        let timers = layout["behaviors"]
            .as_array()
            .and_then(|all| all.iter().find(|b| b["name"] == "Timers"))
            .expect("Timers");
        assert_eq!(timers["storageKey"], "timers");
        assert_eq!(timers["rowTables"], true);
        let numbering = layout["behaviors"]
            .as_array()
            .and_then(|all| all.iter().find(|b| b["name"] == "Numbering"))
            .expect("Numbering");
        assert_eq!(numbering["tables"][0]["shared"], true);
        assert_eq!(layout["maxAttempts"], 8);
    }

    #[test]
    fn the_core_and_the_bridge_run_one_command() {
        let spec = json!({"name": "Note"});
        let command = json!({"caller": {"subject": "a"}, "now": 1, "action": {"kind": "delete", "key": "n1"}});
        let step = call(Op::Core, json!({"spec": spec, "command": command}));
        assert_eq!(
            step,
            json!({"read": [{"kind": "row", "type": "Note", "key": "n1"}]})
        );
        let ran = call(
            Op::Bridge,
            json!({"specs": {"Note": spec}, "spec": spec, "command": command}),
        );
        assert_eq!(
            ran,
            json!({"read": [{"kind": "row", "type": "Note", "id": "n1"}]})
        );
    }

    #[test]
    fn an_input_it_cannot_read_is_an_error_document() {
        let (out, ok) = run_json(Op::Run, b"{not json");
        assert!(!ok);
        assert!(out.contains("invalid_json"), "{out}");
        let (out, ok) = run_json(
            Op::Events,
            br#"{"mode": "later", "caller": {"subject": "a"}}"#,
        );
        assert!(!ok);
        assert!(out.contains("invalid_input"), "{out}");
    }
}
