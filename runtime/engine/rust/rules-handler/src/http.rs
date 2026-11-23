//! Development mode's routes over HTTP (D58's paths, D117), with no
//! transport under them: [`route`] reads an HTTP request (its method, its
//! path after the host's prefix, its query, its headers and its body) as a
//! handler [`Request`], the event log's read, or the MCP endpoint, and
//! [`respond`] writes the handler's answer as the engine writes its own:
//! the envelope `{data, meta: {requestId}}`, an `ETag` of a row's version
//! and a `Location` of what a create made, or an RFC 9457 problem whose
//! `code` is the refusal's and whose `details` are what it carries.
//!
//! | Method | Path (after the prefix) | Request |
//! |--------|-------------------------|---------|
//! | GET | `/namespaces/default/schemas` | the live types the caller may read |
//! | POST | `/namespaces/default/schemas` | define (a schema document) |
//! | GET | `/namespaces/default/schemas/{type}` | the live version's document |
//! | GET | `/namespaces/default/schemas/{type}/describe` | describe |
//! | POST | `/namespaces/default/schemas/{type}/publish` | publish (`?discard=true` deletes the rows it fails) |
//! | GET | `/namespaces/default/schemas/{type}/instances` | list |
//! | POST | `/namespaces/default/schemas/{type}/instances` | create (`{id?, data, behaviors?}`) |
//! | GET, PATCH, DELETE | `/namespaces/default/schemas/{type}/instances/{id}` | get, update (a merge patch), delete |
//! | POST | `/namespaces/default/schemas/{type}/instances/{id}/operations/{operation}` | a use's operation of a row |
//! | POST | `/namespaces/default/schemas/{type}/operations/{operation}` | a use's operation of the table |
//! | POST | `/namespaces/default/schemas/{type}/instances/{id}/signals/{behavior}/{name}` | a caller's signal about a row (D77's primitive 2, [`crate::signals`]): its data the body, 204 |
//! | POST | `/namespaces/default/behaviors/{behavior}/operations/{operation}` | an operation of the namespace as a whole (D77's primitive 3), over the types that compose the behavior and that the caller may read |
//! | GET | `/namespaces/default/events` | the event queue: a page, or server-sent events ([`crate::events`]), with signals beside them on `signals=1` |
//! | POST, GET, DELETE | `/namespaces/default/mcp` | the MCP endpoint, which the host serves |
//!
//! Development mode keeps one namespace, `default`; a path that names
//! another is 404. An operation is named as the engine names it, by its
//! name alone, which the type's one use that declares it answers, or as
//! `<use>.<operation>` where two uses declare it. A list takes the engine's
//! `where` (a JSON filter), the generated servers' bracket filters
//! (`filter[field]=value`, `filter[field][op]=value`, D106's `FilterBy`),
//! `sort` (`field` or `-field`, separated by commas), `limit` and `cursor`.
//! An update, a delete and an operation take the `Preconditions` header, a
//! JSON object of each use's entry by its id.
//!
//! The host keeps authentication (it hands the handler the caller), request
//! ids, body limits, timeouts and CORS, as an engine's host does.

use serde_json::{json, Map, Value};
use superschematic_rules::{Code, Refusal};

use crate::filter::{Sort, QUERY};
use crate::handler::Request;

/// The media type of a JSON body.
pub const JSON: &str = "application/json";
/// The media type of an update's body, RFC 7396.
pub const MERGE_PATCH: &str = "application/merge-patch+json";
/// The media type of a problem document, RFC 9457.
pub const PROBLEM: &str = "application/problem+json";

/// One HTTP request as the host received it.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct HttpRequest {
    /// `GET`, `POST`, ...
    pub method: String,
    /// The path after the prefix the host mounts the routes at, still
    /// percent-encoded.
    pub path: String,
    /// The query string without `?`; empty for none.
    pub query: String,
    /// The headers, names in any case.
    pub headers: Vec<(String, String)>,
    /// The body's bytes; `None` for none.
    pub body: Option<Vec<u8>>,
}

impl HttpRequest {
    /// A header's value, by its name in any case.
    pub fn header(&self, name: &str) -> Option<&str> {
        self.headers
            .iter()
            .find(|(key, _)| key.eq_ignore_ascii_case(name))
            .map(|(_, value)| value.as_str())
    }
}

/// One HTTP response: its status, its headers (lowercase names) and its
/// body's bytes.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct HttpResponse {
    pub status: u16,
    pub headers: Vec<(String, String)>,
    pub body: Vec<u8>,
}

impl HttpResponse {
    /// A header's value, by its lowercase name.
    pub fn header(&self, name: &str) -> Option<&str> {
        self.headers
            .iter()
            .find(|(key, _)| key == name)
            .map(|(_, value)| value.as_str())
    }

    /// The body as JSON; `Value::Null` for none or one that is not.
    pub fn json(&self) -> Value {
        serde_json::from_slice(&self.body).unwrap_or(Value::Null)
    }
}

/// How a handler's answer to a routed request is written.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Answer {
    /// The response as the envelope's data, 200.
    Data,
    /// A row, 200, with its version as its entity tag.
    Row,
    /// A row a create made, 201, with its entity tag and its `Location`:
    /// the request's path and the row's key.
    Created { path: String },
    /// A describe answered as the live version's record: its namespace,
    /// name, version and document.
    Schema,
    /// No content, 204: a signal sent.
    NoContent,
}

/// What a request routes to.
#[derive(Clone, Debug, PartialEq)]
pub enum Routed {
    /// A request of the handler, and how its answer is written.
    Handle { request: Request, answer: Answer },
    /// The event queue's read ([`crate::events::EventsQuery`]).
    Events(crate::events::EventsQuery),
    /// The MCP endpoint, which the host serves.
    Mcp,
    /// The router's own answer: a problem, for a path no route serves or
    /// a request no route takes.
    Answer(HttpResponse),
}

/// The reason phrase Go's `http.StatusText` gives a status, as the
/// engine's problems carry it.
pub fn status_text(status: u16) -> &'static str {
    match status {
        400 => "Bad Request",
        401 => "Unauthorized",
        403 => "Forbidden",
        404 => "Not Found",
        405 => "Method Not Allowed",
        409 => "Conflict",
        412 => "Precondition Failed",
        413 => "Request Entity Too Large",
        415 => "Unsupported Media Type",
        422 => "Unprocessable Entity",
        500 => "Internal Server Error",
        503 => "Service Unavailable",
        _ => "Error",
    }
}

fn json_bytes(value: &Value) -> Vec<u8> {
    serde_json::to_vec(value).unwrap_or_default()
}

/// A problem document as a response: `type`, `title`, `status`, `detail`,
/// `code`, `requestId` and `details`, as the engine writes them.
pub fn problem(
    status: u16,
    detail: &str,
    code: &str,
    details: Option<Value>,
    request_id: &str,
) -> HttpResponse {
    let mut body = Map::new();
    body.insert("type".into(), Value::from("about:blank"));
    body.insert("title".into(), Value::from(status_text(status)));
    body.insert("status".into(), Value::from(status));
    body.insert("detail".into(), Value::from(detail));
    body.insert("code".into(), Value::from(code));
    body.insert("requestId".into(), Value::from(request_id));
    if let Some(details) = details {
        body.insert("details".into(), details);
    }
    let mut headers = vec![
        ("content-type".to_owned(), PROBLEM.to_owned()),
        ("cache-control".to_owned(), "no-store".to_owned()),
        ("x-request-id".to_owned(), request_id.to_owned()),
    ];
    // A busy store clears once the writer ahead lets go.
    if code == Code::Busy.as_str() {
        headers.insert(0, ("retry-after".to_owned(), "1".to_owned()));
    }
    HttpResponse {
        status,
        headers,
        body: json_bytes(&Value::Object(body)),
    }
}

/// A refusal as its problem: its code's status, its message as the
/// detail, and what it carries as the details. A failure that is no
/// refusal of the request's (`internal`, a behavior's defect) keeps its
/// message off the wire.
pub fn refusal_problem(refusal: &Refusal, request_id: &str) -> HttpResponse {
    let status = refusal.status;
    let detail = if status >= 500 && refusal.code != Code::Busy {
        "An unexpected error occurred"
    } else {
        refusal.message.as_str()
    };
    let details = refusal
        .details
        .as_ref()
        .map(|details| Value::Object((**details).clone()));
    problem(status, detail, refusal.code.as_str(), details, request_id)
}

/// The success envelope `{data, meta: {requestId}}` as a response.
pub fn envelope(
    data: Value,
    status: u16,
    extra: Vec<(String, String)>,
    request_id: &str,
) -> HttpResponse {
    let body = json!({"data": data, "meta": {"requestId": request_id}});
    let mut headers = vec![
        ("content-type".to_owned(), JSON.to_owned()),
        ("cache-control".to_owned(), "no-store".to_owned()),
    ];
    headers.extend(extra);
    headers.push(("x-request-id".to_owned(), request_id.to_owned()));
    HttpResponse {
        status,
        headers,
        body: json_bytes(&body),
    }
}

/// 401, when the host established no caller.
pub fn unauthorized(request_id: &str) -> HttpResponse {
    problem(
        401,
        "Authentication required",
        "unauthorized",
        None,
        request_id,
    )
}

fn not_found(detail: &str, request_id: &str) -> HttpResponse {
    problem(404, detail, "not_found", None, request_id)
}

fn bad_request(detail: impl Into<String>, request_id: &str) -> HttpResponse {
    problem(400, &detail.into(), "bad_request", None, request_id)
}

/// A path segment with its percent-encoding decoded; `None` for one that
/// does not decode to UTF-8.
pub fn decode_segment(segment: &str) -> Option<String> {
    let bytes = segment.as_bytes();
    let mut out = Vec::with_capacity(bytes.len());
    let mut i = 0;
    while i < bytes.len() {
        match bytes[i] {
            b'%' => {
                let hex = segment.get(i + 1..i + 3)?;
                out.push(u8::from_str_radix(hex, 16).ok()?);
                i += 3;
            }
            byte => {
                out.push(byte);
                i += 1;
            }
        }
    }
    String::from_utf8(out).ok()
}

/// A query string's pairs, each name and value decoded, `+` a space.
pub fn query_pairs(query: &str) -> Vec<(String, String)> {
    query
        .split('&')
        .filter(|pair| !pair.is_empty())
        .map(|pair| {
            let (name, value) = pair.split_once('=').unwrap_or((pair, ""));
            let decode = |text: &str| {
                decode_segment(&text.replace('+', " ")).unwrap_or_else(|| text.to_owned())
            };
            (decode(name), decode(value))
        })
        .collect()
}

/// A path segment percent-encoded, as a `Location` writes a key.
pub fn encode_segment(text: &str) -> String {
    let mut out = String::new();
    for byte in text.bytes() {
        if byte.is_ascii_alphanumeric() || matches!(byte, b'-' | b'_' | b'.' | b'~') {
            out.push(char::from(byte));
        } else {
            out.push_str(&format!("%{byte:02X}"));
        }
    }
    out
}

fn media_type(request: &HttpRequest, expected: &str, request_id: &str) -> Result<(), HttpResponse> {
    let given = request
        .header("content-type")
        .and_then(|value| value.split(';').next())
        .map(|value| value.trim().to_lowercase())
        .unwrap_or_default();
    if given == expected {
        return Ok(());
    }
    let mut response = problem(
        415,
        &format!(
            "The body of {} {} is {expected}, not {}",
            request.method,
            request.path,
            if given.is_empty() { "untyped" } else { &given }
        ),
        "unsupported_media_type",
        None,
        request_id,
    );
    if request.method.eq_ignore_ascii_case("PATCH") {
        response
            .headers
            .insert(0, ("accept-patch".to_owned(), expected.to_owned()));
    }
    Err(response)
}

// body is a request's JSON body: `None` for none, a problem for one that is
// not JSON.
fn body(request: &HttpRequest, request_id: &str) -> Result<Option<Value>, HttpResponse> {
    match &request.body {
        None => Ok(None),
        Some(bytes) if bytes.is_empty() => Ok(None),
        Some(bytes) => serde_json::from_slice(bytes)
            .map(Some)
            .map_err(|_| bad_request("Request body is not valid JSON", request_id)),
    }
}

fn required_body(
    request: &HttpRequest,
    media: &str,
    request_id: &str,
) -> Result<Value, HttpResponse> {
    let value = body(request, request_id)?
        .ok_or_else(|| bad_request("Request body is required", request_id))?;
    media_type(request, media, request_id)?;
    Ok(value)
}

fn optional_body(request: &HttpRequest, request_id: &str) -> Result<Value, HttpResponse> {
    match body(request, request_id)? {
        Some(value) => {
            media_type(request, JSON, request_id)?;
            Ok(value)
        }
        None => Ok(Value::Object(Map::new())),
    }
}

fn preconditions(
    request: &HttpRequest,
    request_id: &str,
) -> Result<Map<String, Value>, HttpResponse> {
    let Some(header) = request.header("preconditions") else {
        return Ok(Map::new());
    };
    match serde_json::from_str::<Value>(header) {
        Ok(Value::Object(object)) => Ok(object),
        _ => Err(bad_request(
            "Preconditions is a JSON object of each use's entry by its id",
            request_id,
        )),
    }
}

// operation splits `<use>.<operation>`, or names the operation alone, its
// use left to the handler.
fn operation(segment: &str) -> (String, String) {
    match segment.rsplit_once('.') {
        Some((use_id, operation)) => (use_id.to_owned(), operation.to_owned()),
        None => (String::new(), segment.to_owned()),
    }
}

// bracket reads `filter[field]` and `filter[field][op]`.
fn bracket(name: &str) -> Option<(String, String)> {
    let rest = name.strip_prefix("filter[")?;
    let (field, rest) = rest.split_once(']')?;
    if field.is_empty() || field.contains('[') {
        return None;
    }
    if rest.is_empty() {
        return Some((field.to_owned(), "eq".to_owned()));
    }
    let op = rest.strip_prefix('[')?.strip_suffix(']')?;
    if op.is_empty() || op.contains(['[', ']']) {
        return None;
    }
    Some((field.to_owned(), op.to_owned()))
}

fn list_request(
    type_name: &str,
    query: &[(String, String)],
    request_id: &str,
) -> Result<Request, HttpResponse> {
    let mut filter = Map::new();
    let mut brackets = Vec::new();
    let mut sort = Vec::new();
    let mut limit = None;
    let mut cursor = None;
    for (name, value) in query {
        match name.as_str() {
            "where" => match serde_json::from_str::<Value>(value) {
                Ok(Value::Object(object)) => {
                    filter.insert("allOf".into(), json!([Value::Object(object)]));
                }
                _ => {
                    return Err(bad_request(
                        "Invalid query parameter where: a JSON object",
                        request_id,
                    ))
                }
            },
            "sort" => {
                for field in value.split(',').map(str::trim).filter(|f| !f.is_empty()) {
                    sort.push(Sort::parse(field));
                }
            }
            "limit" => {
                limit = Some(value.parse::<usize>().map_err(|_| {
                    bad_request("Invalid query parameter limit: a whole number", request_id)
                })?);
            }
            "cursor" => cursor = Some(value.clone()),
            other => {
                let Some((field, op)) = bracket(other) else {
                    return Err(bad_request(
                        format!("Unknown query parameter {other}"),
                        request_id,
                    ));
                };
                brackets.push(json!([field, op, value]));
            }
        }
    }
    if !brackets.is_empty() {
        filter.insert(QUERY.into(), Value::Array(brackets));
    }
    Ok(Request::List {
        type_name: type_name.to_owned(),
        filter: if filter.is_empty() {
            Value::Null
        } else {
            Value::Object(filter)
        },
        sort,
        limit,
        cursor,
    })
}

/// Routes one request. `request_id` is the host's id for it, which a
/// problem the router answers carries.
pub fn route(request: &HttpRequest, request_id: &str) -> Routed {
    match route_inner(request, request_id) {
        Ok(routed) => routed,
        Err(response) => Routed::Answer(response),
    }
}

fn route_inner(request: &HttpRequest, request_id: &str) -> Result<Routed, HttpResponse> {
    let method = request.method.to_uppercase();
    let raw: Vec<&str> = request.path.split('/').skip(1).collect();
    let mut segments = Vec::with_capacity(raw.len());
    for segment in &raw {
        segments.push(decode_segment(segment).ok_or_else(|| {
            bad_request("The request path is not valid percent-encoding", request_id)
        })?);
    }
    let segments: Vec<&str> = segments.iter().map(String::as_str).collect();
    let Some(["namespaces", namespace, rest @ ..]) = Some(segments.as_slice()) else {
        return Err(not_found("Not Found", request_id));
    };
    if *namespace != crate::access::NAMESPACE {
        return Err(not_found(
            &format!("namespace {namespace} does not exist: development mode keeps one, default"),
            request_id,
        ));
    }
    let query = query_pairs(&request.query);
    let handle = |request: Request, answer: Answer| Ok(Routed::Handle { request, answer });
    match (method.as_str(), rest) {
        ("GET", ["schemas"]) => handle(Request::Types, Answer::Data),
        ("POST", ["schemas"]) => {
            let document = required_body(request, JSON, request_id)?;
            if !document.is_object() {
                return Err(bad_request(
                    "A schema document is a JSON object",
                    request_id,
                ));
            }
            handle(Request::Define { document }, Answer::Data)
        }
        ("GET", ["schemas", name]) => handle(
            Request::Describe {
                type_name: (*name).to_owned(),
            },
            Answer::Schema,
        ),
        ("GET", ["schemas", name, "describe"]) => handle(
            Request::Describe {
                type_name: (*name).to_owned(),
            },
            Answer::Data,
        ),
        ("POST", ["schemas", name, "publish"]) => {
            let given = optional_body(request, request_id)?;
            let discard = query
                .iter()
                .any(|(key, value)| key == "discard" && value == "true")
                || given.get("discard") == Some(&Value::Bool(true));
            handle(
                Request::Publish {
                    type_name: (*name).to_owned(),
                    discard,
                },
                Answer::Data,
            )
        }
        ("GET", ["schemas", name, "instances"]) => {
            handle(list_request(name, &query, request_id)?, Answer::Data)
        }
        ("POST", ["schemas", name, "instances"]) => {
            let given = required_body(request, JSON, request_id)?;
            let Value::Object(members) = given else {
                return Err(bad_request(
                    "A create body is a JSON object with the row as \"data\"",
                    request_id,
                ));
            };
            if let Some(key) = members
                .keys()
                .find(|key| !matches!(key.as_str(), "id" | "data" | "behaviors"))
            {
                return Err(bad_request(
                    format!(
                        "A create body has only \"id\", \"data\" and \"behaviors\", not \"{key}\""
                    ),
                    request_id,
                ));
            }
            let Some(Value::Object(mut data)) = members.get("data").cloned() else {
                return Err(bad_request(
                    "A create body needs the row as \"data\", an object",
                    request_id,
                ));
            };
            match members.get("id") {
                None | Some(Value::Null) => {}
                Some(Value::String(id)) => {
                    data.entry("id")
                        .or_insert_with(|| Value::String(id.clone()));
                }
                Some(_) => return Err(bad_request("\"id\" is a string", request_id)),
            }
            let params = match members.get("behaviors") {
                None | Some(Value::Null) => Map::new(),
                Some(Value::Object(params)) => params.clone(),
                Some(_) => {
                    return Err(bad_request(
                        "\"behaviors\" is an object of each use's create parameters",
                        request_id,
                    ))
                }
            };
            handle(
                Request::Create {
                    type_name: (*name).to_owned(),
                    data,
                    params,
                },
                Answer::Created {
                    path: request.path.trim_end_matches('/').to_owned(),
                },
            )
        }
        ("GET", ["schemas", name, "instances", id]) => handle(
            Request::Get {
                type_name: (*name).to_owned(),
                id: (*id).to_owned(),
            },
            Answer::Row,
        ),
        ("PATCH", ["schemas", name, "instances", id]) => {
            let patch = required_body(request, MERGE_PATCH, request_id)?;
            let Value::Object(patch) = patch else {
                return Err(bad_request(
                    "A merge patch here is a JSON object",
                    request_id,
                ));
            };
            handle(
                Request::Update {
                    type_name: (*name).to_owned(),
                    id: (*id).to_owned(),
                    patch,
                    preconditions: preconditions(request, request_id)?,
                },
                Answer::Row,
            )
        }
        ("DELETE", ["schemas", name, "instances", id]) => handle(
            Request::Delete {
                type_name: (*name).to_owned(),
                id: (*id).to_owned(),
                preconditions: preconditions(request, request_id)?,
            },
            Answer::Data,
        ),
        ("POST", ["schemas", name, "instances", id, "operations", op]) => {
            let params = optional_body(request, request_id)?;
            let (use_id, operation) = operation(op);
            handle(
                Request::Operation {
                    type_name: (*name).to_owned(),
                    id: (*id).to_owned(),
                    use_id,
                    operation,
                    params,
                    preconditions: preconditions(request, request_id)?,
                },
                Answer::Data,
            )
        }
        ("POST", ["schemas", name, "instances", id, "signals", behavior, signal]) => {
            let data = optional_body(request, request_id)?;
            handle(
                Request::Signal {
                    type_name: (*name).to_owned(),
                    id: (*id).to_owned(),
                    behavior: (*behavior).to_owned(),
                    name: (*signal).to_owned(),
                    data,
                },
                Answer::NoContent,
            )
        }
        ("POST", ["schemas", name, "operations", op]) => {
            let params = optional_body(request, request_id)?;
            let (use_id, operation) = operation(op);
            handle(
                Request::TableOperation {
                    type_name: (*name).to_owned(),
                    use_id,
                    operation,
                    params,
                },
                Answer::Data,
            )
        }
        ("POST", ["behaviors", behavior, "operations", op]) => {
            let params = optional_body(request, request_id)?;
            handle(
                Request::NamespaceOperation {
                    behavior: (*behavior).to_owned(),
                    operation: (*op).to_owned(),
                    params,
                },
                Answer::Data,
            )
        }
        ("GET", ["events"]) => crate::events::EventsQuery::read(request, &query)
            .map(Routed::Events)
            .map_err(|detail| bad_request(detail, request_id)),
        ("POST" | "GET" | "DELETE", ["mcp"]) => Ok(Routed::Mcp),
        _ => Err(not_found("Not Found", request_id)),
    }
}

/// Writes the handler's answer to a routed request.
pub fn respond(answer: &Answer, outcome: Result<Value, Refusal>, request_id: &str) -> HttpResponse {
    let value = match outcome {
        Ok(value) => value,
        Err(refusal) => return refusal_problem(&refusal, request_id),
    };
    let etag = |value: &Value| {
        value
            .get("_version")
            .and_then(Value::as_i64)
            .map(|version| ("etag".to_owned(), format!("\"{version}\"")))
    };
    match answer {
        Answer::Data => envelope(value, 200, Vec::new(), request_id),
        Answer::Row => {
            let extra = etag(&value).into_iter().collect();
            envelope(value, 200, extra, request_id)
        }
        Answer::Created { path } => {
            let mut extra: Vec<(String, String)> = etag(&value).into_iter().collect();
            let key = value
                .as_object()
                .and_then(|row| row.values().next())
                .and_then(Value::as_str)
                .unwrap_or_default();
            extra.push((
                "location".to_owned(),
                format!("{path}/{}", encode_segment(key)),
            ));
            envelope(value, 201, extra, request_id)
        }
        Answer::Schema => {
            let record = json!({
                "namespace": crate::access::NAMESPACE,
                "name": value["type"],
                "version": value["version"],
                "document": value["document"],
            });
            envelope(record, 200, Vec::new(), request_id)
        }
        Answer::NoContent => HttpResponse {
            status: 204,
            headers: vec![
                ("cache-control".to_owned(), "no-store".to_owned()),
                ("x-request-id".to_owned(), request_id.to_owned()),
            ],
            body: Vec::new(),
        },
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn get(path: &str, query: &str) -> Routed {
        route(
            &HttpRequest {
                method: "GET".into(),
                path: path.into(),
                query: query.into(),
                ..HttpRequest::default()
            },
            "r1",
        )
    }

    #[test]
    fn a_path_routes_to_its_request_and_another_namespace_is_404() {
        assert!(matches!(
            get("/namespaces/default/schemas/Order/instances/o%3A1", ""),
            Routed::Handle { request: Request::Get { id, .. }, answer: Answer::Row } if id == "o:1"
        ));
        let Routed::Answer(missing) = get("/namespaces/east/schemas", "") else {
            panic!("east is no namespace here");
        };
        assert_eq!(missing.status, 404);
        assert_eq!(missing.json()["code"], "not_found");
        let Routed::Answer(none) = get("/nothing", "") else {
            panic!("no route");
        };
        assert_eq!(none.status, 404);
        assert!(matches!(get("/namespaces/default/mcp", ""), Routed::Mcp));
    }

    #[test]
    fn a_lists_query_carries_where_brackets_sort_limit_and_cursor() {
        let Routed::Handle {
            request:
                Request::List {
                    filter,
                    sort,
                    limit,
                    cursor,
                    ..
                },
            ..
        } = get(
            "/namespaces/default/schemas/Task/instances",
            "where=%7B%22title%22%3A%7B%22eq%22%3A%22a%22%7D%7D&filter[estimate][gte]=2&filter[status]=todo&filter[kind][in]=a,%20b&sort=-estimate,title&limit=5&cursor=c1",
        )
        else {
            panic!("a list");
        };
        assert_eq!(
            filter,
            json!({
                "allOf": [{"title": {"eq": "a"}}],
                "$query": [["estimate", "gte", "2"], ["status", "eq", "todo"], ["kind", "in", "a, b"]]
            })
        );
        assert_eq!(sort.len(), 2);
        assert_eq!((limit, cursor.as_deref()), (Some(5), Some("c1")));
        let Routed::Answer(refused) = get(
            "/namespaces/default/schemas/Task/instances",
            "filter[a][b][c]=1",
        ) else {
            panic!("refused");
        };
        assert_eq!(refused.status, 400);
    }

    #[test]
    fn an_answer_is_the_envelope_or_a_problem() {
        let created = respond(
            &Answer::Created {
                path: "/namespaces/default/schemas/Order/instances".into(),
            },
            Ok(json!({"id": "o 1", "title": "Desk", "_version": 1})),
            "r1",
        );
        assert_eq!(created.status, 201);
        assert_eq!(created.header("etag"), Some("\"1\""));
        assert_eq!(
            created.header("location"),
            Some("/namespaces/default/schemas/Order/instances/o%201")
        );
        assert_eq!(created.json()["meta"]["requestId"], "r1");
        let refused = respond(
            &Answer::Data,
            Err(Refusal::forbidden(
                "bob may not read Order in namespace default",
            )),
            "r2",
        );
        assert_eq!(refused.status, 403);
        assert_eq!(refused.header("content-type"), Some(PROBLEM));
        assert_eq!(
            refused.json(),
            json!({"type": "about:blank", "title": "Forbidden", "status": 403,
                "detail": "bob may not read Order in namespace default", "code": "forbidden", "requestId": "r2"})
        );
        let internal = respond(&Answer::Data, Err(Refusal::internal("a secret")), "r3");
        assert_eq!(internal.json()["detail"], "An unexpected error occurred");
    }
}
