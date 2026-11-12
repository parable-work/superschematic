//! The engine's MCP corpus (`runtime/engine/testdata/http/mcp.json`), which
//! the engine's tests record through the official MCP client, replayed
//! through this runtime's transport (D81, D116), as the TypeScript
//! runtime's `mcp-parity.test.ts` replays it through `serveMcp`. The
//! transport is the engine's, copied: the tools differ, so each exchange's
//! tools answer what the engine's answered, and the transport must frame
//! them as the engine did: the statuses, the headers, the event stream of
//! the 2025 revisions and the single JSON body of 2026-07-28, the ids,
//! `initialize` and `server/discover`, the 202 of a notification and the
//! 405 of a GET, a result's text and structured content, a tool error
//! carrying its problem, and an unknown tool's invalid-params error.
//! Exchanges the transport does not answer, the gate's 401 and the engine's
//! 404 for a namespace it has not got, are left out.
//!
//! A call's text is the engine's byte for byte where this build's
//! serde_json keeps an object's members as they were written (its
//! `preserve_order` feature, which a generated crate may turn on). Without
//! it serde_json sorts them, in a route's response as in a tool's text, so
//! the text is held to the engine's with its members in that order, and to
//! the same value.

use async_trait::async_trait;
use bytes::Bytes;
use http::{Request, Response};
use serde_json::{json, Map, Value};
use std::collections::HashMap;
use std::future::Future;
use std::path::Path;
use std::task::{Context, Poll, Waker};
use superschematic_http_runtime::mcp::{
    serve, CallOutcome, Content, Options, ServerInfo, Tool, ToolError, ToolSet,
};

const CORPUS: &str = "../../engine/testdata/http/mcp.json";

/// The headers of an answer the corpus records.
const RECORDED_HEADERS: [&str; 3] = ["cache-control", "content-type", "mcp-session-id"];

/// Polls a future to its end on this thread. `serve` awaits its tools and
/// nothing else, so with tools that are ready it never waits, and needs no
/// runtime.
fn block_on<F: Future>(future: F) -> F::Output {
    let mut future = std::pin::pin!(future);
    match future
        .as_mut()
        .poll(&mut Context::from_waker(Waker::noop()))
    {
        Poll::Ready(output) => output,
        Poll::Pending => panic!("serve waited on something its tools did not hand it"),
    }
}

fn corpus() -> Value {
    let path = Path::new(env!("CARGO_MANIFEST_DIR")).join(CORPUS);
    let text = std::fs::read_to_string(&path)
        .unwrap_or_else(|err| panic!("read {}: {err}", path.display()));
    serde_json::from_str(&text).unwrap_or_else(|err| panic!("parse {}: {err}", path.display()))
}

/// `value` with each shared value it names, at any depth, in place.
fn resolve(value: &Value, shared: &Value) -> Value {
    match value {
        Value::Array(items) => items.iter().map(|item| resolve(item, shared)).collect(),
        Value::Object(members) => match members.get("$shared").and_then(Value::as_str) {
            Some(name) => resolve(&shared[name], shared),
            None => Value::Object(
                members
                    .iter()
                    .map(|(key, item)| (key.clone(), resolve(item, shared)))
                    .collect(),
            ),
        },
        other => other.clone(),
    }
}

/// The frames a step's stream sent: every frames step for it after
/// `index`, until it ends.
fn frames_of(steps: &[Value], index: usize, stream: &str, shared: &Value) -> Vec<Value> {
    let mut frames = Vec::new();
    for step in &steps[index + 1..] {
        if step["stream"] != stream || step.get("request").is_some() {
            continue;
        }
        if step["end"] == true {
            break;
        }
        if let Value::Array(sent) = resolve(&step["frames"], shared) {
            frames.extend(sent);
        }
    }
    frames
}

/// The engine's server info, which the corpus's engine package reports,
/// unless the mount gave one.
fn engine_server() -> Options {
    Options::new(ServerInfo::new("@superschematic/engine", "0.0.0"))
}

/// The server an app step builds: its MCP mount's server info and
/// instructions.
fn server_of(step: &Value) -> Options {
    let own = [json!({"kind": step["kind"], "options": step["options"]})];
    let mounts = step["mounts"].as_array().map_or(&own[..], Vec::as_slice);
    let Some(mcp) = mounts.iter().find(|mount| mount["kind"] == "mcp") else {
        return engine_server();
    };
    let options = &mcp["options"];
    let mut server = match serde_json::from_value::<ServerInfo>(options["serverInfo"].clone()) {
        Ok(info) => Options::new(info),
        Err(_) => engine_server(),
    };
    if let Some(instructions) = options["instructions"].as_str() {
        server = server.with_instructions(instructions);
    }
    server
}

/// Tools that answer what the engine answered the recorded reply.
struct ReplayTools {
    reply: Value,
}

#[async_trait]
impl ToolSet for ReplayTools {
    async fn list(&self) -> Result<Vec<Tool>, ToolError> {
        match self.reply.pointer("/result/tools") {
            Some(tools) => serde_json::from_value(tools.clone())
                .map_err(|err| ToolError::Failed(Box::new(err))),
            None => Ok(Vec::new()),
        }
    }

    async fn call(
        &self,
        _name: &str,
        _arguments: &Map<String, Value>,
    ) -> Result<CallOutcome, ToolError> {
        if let Some(error) = self.reply.get("error") {
            let message = error["message"].as_str().unwrap_or_default().to_owned();
            return Err(if error["code"] == -32602 {
                ToolError::unknown(message)
            } else {
                ToolError::Failed(message.into())
            });
        }
        let result = &self.reply["result"];
        if result["isError"] == true {
            return Ok(CallOutcome::Problem(result["structuredContent"].clone()));
        }
        let content = result["content"].as_array().cloned().unwrap_or_default();
        if let [only] = content.as_slice() {
            if let (Some("text"), Some(text)) = (only["type"].as_str(), only["text"].as_str()) {
                // A value JSON cannot hold, a time, is a symbol in the
                // text, and a string in the structured content.
                return Ok(CallOutcome::Result(
                    serde_json::from_str(text).unwrap_or_else(|_| {
                        result
                            .get("structuredContent")
                            .cloned()
                            .unwrap_or_else(|| Value::from(text))
                    }),
                ));
            }
        }
        Ok(CallOutcome::Content(
            content.iter().map(recorded_content).collect(),
        ))
    }
}

fn recorded_content(item: &Value) -> Content {
    let text = |value: &Value| value.as_str().unwrap_or_default().to_owned();
    match item["type"].as_str() {
        Some("text") => Content::Text {
            text: text(&item["text"]),
        },
        Some("resource") => Content::Resource {
            uri: text(&item["resource"]["uri"]),
            mime_type: text(&item["resource"]["mimeType"]),
            blob: text(&item["resource"]["blob"]),
        },
        other => panic!("the corpus holds content of type {other:?}"),
    }
}

/// The frames of an event-stream body, as the corpus records them.
fn parse_frames(text: &str) -> Vec<Value> {
    text.split("\n\n")
        .filter(|block| !block.trim().is_empty())
        .map(|block| {
            let mut frame = Map::new();
            for line in block.split('\n') {
                let (field, value) = line
                    .split_once(": ")
                    .unwrap_or_else(|| panic!("a frame line with no field: {line:?}"));
                let value = if field == "data" {
                    serde_json::from_str(value)
                        .unwrap_or_else(|err| panic!("a frame's data is not JSON: {err}"))
                } else {
                    Value::from(value)
                };
                frame.insert(field.to_owned(), value);
            }
            Value::Object(frame)
        })
        .collect()
}

/// `json` with the text of each text content item made comparable: one
/// that holds a symbol (`{{time:1}}` where the engine wrote a time)
/// replaced by one mark, since the corpus writes the symbol bare in the
/// text and quoted in the structured content, which is compared instead;
/// and one that is JSON replaced by its value, whose bytes `held_texts`
/// compares.
fn comparable(json: &Value) -> Value {
    match json {
        Value::Array(items) => items.iter().map(comparable).collect(),
        Value::Object(members) => {
            let text_item = members.get("type").and_then(Value::as_str) == Some("text");
            Value::Object(
                members
                    .iter()
                    .map(|(key, value)| {
                        let value = match value.as_str() {
                            Some(text) if key == "text" && text_item && text.contains("{{") => {
                                Value::from("<symbol>")
                            }
                            Some(text) if key == "text" && text_item => {
                                match serde_json::from_str::<Value>(text) {
                                    Ok(parsed) => json!({"<json>": parsed}),
                                    Err(_) => Value::from(text),
                                }
                            }
                            _ => comparable(value),
                        };
                        (key.clone(), value)
                    })
                    .collect(),
            )
        }
        other => other.clone(),
    }
}

/// The text of each tool result's content in a reply.
fn texts(reply: &Value) -> Vec<String> {
    reply
        .pointer("/result/content")
        .and_then(Value::as_array)
        .into_iter()
        .flatten()
        .filter(|item| item["type"] == "text")
        .map(|item| item["text"].as_str().unwrap_or_default().to_owned())
        .collect()
}

/// Whether this build's serde_json keeps an object's members in the order
/// they were written.
fn keeps_order() -> bool {
    let value: Value = serde_json::from_str(r#"{"b":0,"a":0}"#).unwrap_or_default();
    value
        .as_object()
        .and_then(|members| members.keys().next())
        .is_some_and(|first| first == "b")
}

/// Holds the texts the transport wrote to the engine's, byte for byte: as
/// recorded when this build keeps members in order, else as recorded with
/// its members in this build's order. A text that holds a symbol is left
/// out, as in `comparable`.
fn held_texts(name: &str, answered: &[Value], recorded: &[Value]) -> Result<(), String> {
    let answered: Vec<String> = answered.iter().flat_map(texts).collect();
    let recorded: Vec<String> = recorded.iter().flat_map(texts).collect();
    if answered.len() != recorded.len() {
        return Err(format!(
            "{name}: {} texts, recorded {}",
            answered.len(),
            recorded.len()
        ));
    }
    for (answered, recorded) in answered.iter().zip(&recorded) {
        if recorded.contains("{{") {
            continue;
        }
        let expected = match serde_json::from_str::<Value>(recorded) {
            Ok(value) => value.to_string(),
            Err(_) => recorded.clone(),
        };
        if keeps_order() && expected != *recorded {
            return Err(format!(
                "{name}: the recorded text is not what serde_json writes: {recorded}"
            ));
        }
        if *answered != expected {
            return Err(format!("{name}: text {answered}, recorded {expected}"));
        }
    }
    Ok(())
}

/// One exchange the transport answers.
struct Replay {
    name: String,
    exchange: Value,
    frames: Vec<Value>,
    server: Options,
}

impl Replay {
    fn method(&self) -> Option<&str> {
        self.exchange["request"]["body"]["method"].as_str()
    }

    fn streams(&self) -> bool {
        self.exchange.get("stream").is_some()
    }

    /// The recorded reply the request's tools answer from: its one frame,
    /// or its JSON body.
    fn reply(&self) -> Value {
        if self.streams() {
            return self
                .frames
                .first()
                .map_or(Value::Null, |frame| frame["data"].clone());
        }
        self.exchange["response"]["body"].clone()
    }

    fn request(&self) -> Request<Bytes> {
        let request = &self.exchange["request"];
        let mut uri = request["path"].as_str().unwrap_or_default().to_owned();
        if let Some(query) = request["query"].as_str().filter(|query| !query.is_empty()) {
            uri = format!("{uri}?{query}");
        }
        let mut builder = Request::builder()
            .method(request["method"].as_str().unwrap_or_default())
            .uri(uri);
        for (name, value) in request["headers"].as_object().into_iter().flatten() {
            builder = builder.header(name, value.as_str().unwrap_or_default());
        }
        let body = match request.get("body") {
            Some(body) => Bytes::from(body.to_string()),
            None => Bytes::new(),
        };
        builder
            .body(body)
            .unwrap_or_else(|err| panic!("{}: {err}", self.name))
    }

    /// What differs between the transport's answer and the recorded one.
    fn check(&self, answer: &Response<Bytes>) -> Result<(), String> {
        let name = &self.name;
        let response = &self.exchange["response"];
        if Some(u64::from(answer.status().as_u16())) != response["status"].as_u64() {
            return Err(format!(
                "{name}: status {}, recorded {}",
                answer.status(),
                response["status"]
            ));
        }
        for header in RECORDED_HEADERS {
            let answered = answer
                .headers()
                .get(header)
                .and_then(|value| value.to_str().ok());
            let recorded = response["headers"][header].as_str();
            if answered != recorded {
                return Err(format!(
                    "{name}: {header} {answered:?}, recorded {recorded:?}"
                ));
            }
        }
        let text = String::from_utf8_lossy(answer.body());
        if self.streams() {
            let frames = parse_frames(&text);
            let same = frames.len() == self.frames.len()
                && frames
                    .iter()
                    .zip(&self.frames)
                    .all(|(answered, recorded)| comparable(answered) == comparable(recorded));
            if !same {
                return Err(format!(
                    "{name}: frames {}, recorded {}",
                    Value::from(frames),
                    Value::from(self.frames.clone())
                ));
            }
            let data = |frames: &[Value]| -> Vec<Value> {
                frames.iter().map(|frame| frame["data"].clone()).collect()
            };
            held_texts(name, &data(&frames), &data(&self.frames))
        } else if let Some(recorded) = response.get("body") {
            let body: Value = serde_json::from_str(&text)
                .map_err(|err| format!("{name}: the body is not JSON: {err}"))?;
            if comparable(&body) != comparable(recorded) {
                return Err(format!("{name}: body {body}, recorded {recorded}"));
            }
            held_texts(name, &[body], std::slice::from_ref(recorded))
        } else if text.is_empty() {
            Ok(())
        } else {
            Err(format!("{name}: a body, {text}, where none was recorded"))
        }
    }
}

/// Every exchange of the corpus the transport answers, each with the
/// server its app was built as.
fn replays() -> Vec<Replay> {
    let corpus = corpus();
    let shared = &corpus["shared"];
    let mut replays = Vec::new();
    for case in corpus["cases"].as_array().into_iter().flatten() {
        let case_name = case["name"].as_str().unwrap_or_default();
        let steps = case["steps"].as_array().map_or(&[][..], Vec::as_slice);
        // The server of each app the case built, and of the one it built
        // last, which an exchange that names no app was sent to.
        let mut servers: HashMap<String, Options> = HashMap::new();
        let mut latest = engine_server();
        for (index, step) in steps.iter().enumerate() {
            if step.get("request").is_none() {
                if let Some(app) = step["app"].as_str() {
                    latest = server_of(step);
                    servers.insert(app.to_owned(), latest.clone());
                }
                continue;
            }
            let exchange = resolve(step, shared);
            let path = exchange["request"]["path"].as_str().unwrap_or_default();
            let status = exchange["response"]["status"].as_u64();
            if !path.ends_with("/mcp") || status == Some(401) || status == Some(404) {
                continue;
            }
            let frames = match exchange["stream"].as_str() {
                Some(stream) => frames_of(steps, index, stream, shared),
                None => Vec::new(),
            };
            let server = exchange["app"]
                .as_str()
                .and_then(|app| servers.get(app))
                .unwrap_or(&latest)
                .clone();
            replays.push(Replay {
                name: format!(
                    "{case_name} > exchange {index}: {} {}",
                    exchange["request"]["method"].as_str().unwrap_or_default(),
                    exchange["request"]["body"]["method"]
                        .as_str()
                        .unwrap_or_default()
                ),
                exchange,
                frames,
                server,
            });
        }
    }
    replays
}

#[test]
fn the_corpus_has_exchanges_the_transport_answers() {
    let replays = replays();
    assert!(replays.len() > 50, "{} exchanges", replays.len());
    for kind in [
        "initialize",
        "notifications/initialized",
        "tools/list",
        "tools/call",
        "server/discover",
    ] {
        assert!(
            replays.iter().any(|replay| replay.method() == Some(kind)),
            "the corpus has no {kind}"
        );
    }
    // Both framings, a tool error, an unknown tool and a refused method.
    assert!(replays.iter().any(Replay::streams));
    assert!(replays
        .iter()
        .any(|replay| !replay.streams() && replay.method() == Some("tools/call")));
    assert!(replays
        .iter()
        .any(|replay| replay.reply().pointer("/result/isError") == Some(&Value::Bool(true))));
    assert!(replays
        .iter()
        .any(|replay| replay.reply().pointer("/error/code") == Some(&json!(-32602))));
    assert!(replays
        .iter()
        .any(|replay| replay.exchange["response"]["status"] == 405));
}

#[test]
fn the_engine_mcp_corpus_replays_through_serve() {
    let replays = replays();
    let mut failures = Vec::new();
    for replay in &replays {
        let tools = ReplayTools {
            reply: replay.reply(),
        };
        let answer = block_on(serve(&replay.request(), &tools, &replay.server));
        if let Err(failure) = replay.check(&answer) {
            failures.push(failure);
        }
    }
    assert!(
        failures.is_empty(),
        "{} of {} exchanges differ:\n{}",
        failures.len(),
        replays.len(),
        failures.join("\n")
    );
    println!(
        "replayed {} exchanges of the engine's MCP corpus",
        replays.len()
    );
}
