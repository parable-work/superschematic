//! The engine's routes, the handlers of `src/http/app.ts`: each decodes
//! its parameters and body as the HTTP runtime does, calls the engine as
//! the request's principal, and answers with the envelope (an ETag and a
//! Location where the TypeScript app sends them) or the problem an engine
//! refusal answers with.
//!
//! An instance's sequence is its entity tag: a response that carries an
//! instance, or a behavior operation's result, sends `ETag: "<seq>"`, and
//! PATCH, DELETE and an operation honour `If-Match` inside the write
//! (expectedSeq), so a lost update answers 412. They take the
//! `Preconditions` header, a JSON object of each behavior's entry by its
//! name. A list's `where` and a lookup's `key` are JSON objects in a query
//! parameter, so a value travels whole.

use serde_json::{Map, Value};

use crate::errors::Error;
use crate::events::{EventsAfter, ReadEvents};
use crate::instances::{CreateOptions, GetOptions, ListOptions, WriteOptions};
use crate::principal::Principal;
use crate::registry::{DefineOptions, SchemaTarget};
use crate::tools::{encode_component, schema_view, ToolTarget};
use crate::Engine;

use super::params::{self, Kind, Location, Param};
use super::problems::{envelope, Problem};
use super::{json_body, ApiOptions, Matched, Request, Response};

/// The media type of a JSON body.
pub const JSON_MEDIA_TYPE: &str = "application/json";
/// The media type of an instance update, RFC 7386.
pub const MERGE_PATCH_MEDIA_TYPE: &str = "application/merge-patch+json";

type Served = Result<Response, Problem>;

fn to_json(value: &impl serde::Serialize) -> Result<Value, Problem> {
    serde_json::to_value(value).map_err(|_| Problem::internal())
}

// engine_problem maps an engine error to its problem.
fn failed(error: Error) -> Problem {
    Problem::of(&error)
}

fn ok(request: &Request, data: Value) -> Response {
    envelope(data, &request.request_id, 200, Vec::new())
}

fn tagged(request: &Request, data: Value, status: u16, extra: Vec<(String, String)>) -> Response {
    envelope(data, &request.request_id, status, extra)
}

fn etag(seq: i64) -> (String, String) {
    ("etag".to_owned(), format!("\"{seq}\""))
}

// location is where a created resource is: the request's path, prefix and
// all, without trailing slashes, and the name, encoded.
fn location(request: &Request, name: &str) -> (String, String) {
    (
        "location".to_owned(),
        format!(
            "{}/{}",
            request.path.trim_end_matches('/'),
            encode_component(name)
        ),
    )
}

// media_type refuses a body that is not of the media type the route
// takes: 415, with Accept-Patch on PATCH.
fn media_type(request: &Request, matched: &Matched, expected: &str) -> Result<(), Problem> {
    let given = request
        .header("content-type")
        .and_then(|value| value.split(';').next())
        .map(|value| value.trim().to_lowercase())
        .unwrap_or_default();
    if given == expected {
        return Ok(());
    }
    let mut problem = Problem::new(
        415,
        format!(
            "The body of {} {} is {expected}, not {}",
            request.method,
            matched.route.path,
            if given.is_empty() { "untyped" } else { &given }
        ),
        "unsupported_media_type",
    );
    if request.method == "PATCH" {
        problem = problem.with_header("accept-patch", expected);
    }
    Err(problem)
}

// required_body is a route's JSON body, which it must have.
fn required_body(request: &Request) -> Result<Value, Problem> {
    json_body(request)?.ok_or_else(|| Problem::bad_request("Request body is required"))
}

fn query_param(
    query: &[(String, String)],
    name: &str,
    kind: Kind,
    required: bool,
) -> Result<Option<Param>, Problem> {
    params::decode(
        Location::Query,
        name,
        kind,
        required,
        false,
        &params::all(query, name),
    )
}

fn text_param(param: Option<Param>) -> Option<String> {
    match param {
        Some(Param::Text(text)) => Some(text),
        _ => None,
    }
}

fn flag_param(param: Option<Param>) -> bool {
    matches!(param, Some(Param::Flag(true)))
}

/// Serves one of the 26 routes.
pub(crate) fn serve(
    engine: &Engine,
    options: &ApiOptions,
    request: &Request,
    matched: &Matched,
    principal: &Principal,
) -> Served {
    let namespace = matched.param("namespace");
    let name = matched.param("name");
    let query = params::parse_query(&request.query);
    let ns = Some(namespace);
    match matched.route.name {
        "listNamespaces" => Ok(ok(
            request,
            to_json(&engine.namespaces().list(principal).map_err(failed)?)?,
        )),
        "createNamespace" => {
            let input = required_body(request)?;
            media_type(request, matched, JSON_MEDIA_TYPE)?;
            let name = match &input {
                Value::Object(members)
                    if members.keys().all(|key| key == "name")
                        && members.get("name").is_some_and(Value::is_string) =>
                {
                    members["name"].as_str().unwrap_or_default().to_owned()
                }
                _ => {
                    return Err(Problem::bad_request(
                        "A namespace create body is a JSON object with the namespace's name as \"name\", a string, and nothing else",
                    ))
                }
            };
            let record = engine
                .namespaces()
                .create(principal, &name)
                .map_err(failed)?;
            Ok(tagged(
                request,
                to_json(&record)?,
                201,
                vec![location(request, &record.name)],
            ))
        }
        "getNamespace" => Ok(ok(
            request,
            to_json(
                &engine
                    .namespaces()
                    .get(principal, namespace)
                    .map_err(failed)?,
            )?,
        )),
        "archiveNamespace" => Ok(ok(
            request,
            to_json(
                &engine
                    .namespaces()
                    .archive(principal, namespace)
                    .map_err(failed)?,
            )?,
        )),
        "unarchiveNamespace" => Ok(ok(
            request,
            to_json(
                &engine
                    .namespaces()
                    .unarchive(principal, namespace)
                    .map_err(failed)?,
            )?,
        )),
        "listSchemas" => Ok(ok(
            request,
            to_json(
                &engine
                    .schemas()
                    .list(principal, SchemaTarget { namespace: ns })
                    .map_err(failed)?,
            )?,
        )),
        "defineSchema" => {
            let input = required_body(request)?;
            media_type(request, matched, JSON_MEDIA_TYPE)?;
            if !input.is_object() {
                return Err(Problem::bad_request("A schema document is a JSON object"));
            }
            let record = engine
                .schemas()
                .define(
                    principal,
                    &input,
                    DefineOptions {
                        namespace: ns,
                        source: None,
                    },
                )
                .map_err(failed)?;
            Ok(ok(request, schema_view(&record).map_err(failed)?))
        }
        "getSchema" | "getSchemaDraft" => {
            let schemas = engine.schemas();
            let target = SchemaTarget { namespace: ns };
            let found = if matched.route.name == "getSchema" {
                schemas.live(principal, name, target)
            } else {
                schemas.draft(principal, name, target)
            }
            .map_err(failed)?;
            match found {
                Some(record) => Ok(ok(request, schema_view(&record).map_err(failed)?)),
                None => Err(Problem::not_found(format!(
                    "schema {name} has no {} in namespace {namespace}",
                    if matched.route.name == "getSchema" {
                        "live version"
                    } else {
                        "draft"
                    }
                ))),
            }
        }
        "getSchemaVersion" => {
            let version = match params::decode(
                Location::Path,
                "version",
                Kind::Integer,
                true,
                false,
                &[matched.param("version").to_owned()],
            )? {
                Some(Param::Integer(version)) => version,
                _ => {
                    return Err(Problem::bad_request(
                        "Invalid path parameter version: required",
                    ))
                }
            };
            match engine
                .schemas()
                .version(principal, name, version, SchemaTarget { namespace: ns })
                .map_err(failed)?
            {
                Some(record) => Ok(ok(request, schema_view(&record).map_err(failed)?)),
                None => Err(Problem::not_found(format!(
                    "schema {name} has no version {version} in namespace {namespace}"
                ))),
            }
        }
        "describeSchema" => Ok(ok(
            request,
            engine
                .tools()
                .describe(
                    principal,
                    name,
                    ToolTarget {
                        namespace: ns,
                        filter: None,
                    },
                )
                .map_err(failed)?,
        )),
        "publishSchema" => Ok(ok(
            request,
            to_json(
                &engine
                    .schemas()
                    .publish(principal, name, SchemaTarget { namespace: ns })
                    .map_err(failed)?,
            )?,
        )),
        "listInstances" => {
            let limit = match query_param(&query, "limit", Kind::Integer, false)? {
                Some(Param::Integer(limit)) => Some(limit),
                _ => None,
            };
            let cursor = text_param(query_param(&query, "cursor", Kind::String, false)?);
            let filter = text_param(query_param(&query, "where", Kind::String, false)?);
            let value_refs = flag_param(query_param(&query, "valueRefs", Kind::Boolean, false)?);
            let filter = filter
                .map(|text| params::json_object("where", &text))
                .transpose()?;
            Ok(ok(
                request,
                to_json(
                    &engine
                        .instances()
                        .list(
                            principal,
                            name,
                            ListOptions {
                                namespace: ns,
                                limit,
                                cursor: cursor.as_deref(),
                                value_refs,
                                filter: filter.as_ref(),
                            },
                        )
                        .map_err(failed)?,
                )?,
            ))
        }
        "lookupInstance" => {
            let key =
                text_param(query_param(&query, "key", Kind::String, true)?).unwrap_or_default();
            let value_refs = flag_param(query_param(&query, "valueRefs", Kind::Boolean, false)?);
            let object = params::json_object("key", &key)?;
            match engine
                .instances()
                .lookup(
                    principal,
                    name,
                    &object,
                    GetOptions {
                        namespace: ns,
                        value_refs,
                    },
                )
                .map_err(failed)?
            {
                Some(record) => Ok(tagged(
                    request,
                    to_json(&record)?,
                    200,
                    vec![etag(record.seq)],
                )),
                None => Err(Problem::not_found(format!(
                    "no {name} in namespace {namespace} holds {key}"
                ))),
            }
        }
        "createInstance" => {
            let input = required_body(request)?;
            media_type(request, matched, JSON_MEDIA_TYPE)?;
            let Value::Object(members) = &input else {
                return Err(Problem::bad_request(
                    "A create body is a JSON object with the instance as \"data\"",
                ));
            };
            let unknown: Vec<String> = members
                .keys()
                .filter(|key| !matches!(key.as_str(), "id" | "data" | "behaviors"))
                .map(|key| crate::json::quote(key))
                .collect();
            if !unknown.is_empty() {
                return Err(Problem::bad_request(format!(
                    "A create body has only \"id\", \"data\" and \"behaviors\", not {}",
                    unknown.join(", ")
                )));
            }
            let Some(data) = members.get("data") else {
                return Err(Problem::bad_request(
                    "A create body needs the instance as \"data\"",
                ));
            };
            let id = match members.get("id") {
                None => None,
                Some(Value::String(id)) => Some(id.as_str()),
                Some(_) => return Err(Problem::bad_request("\"id\" is a string")),
            };
            let behaviors = members.get("behaviors").filter(|value| !value.is_null());
            let record = engine
                .instances()
                .create(
                    principal,
                    name,
                    data,
                    CreateOptions {
                        namespace: ns,
                        id,
                        behaviors,
                    },
                )
                .map_err(failed)?;
            let extra = vec![etag(record.seq), location(request, &record.id)];
            Ok(tagged(request, to_json(&record)?, 201, extra))
        }
        "getInstance" => {
            let id = matched.param("id");
            let value_refs = flag_param(query_param(&query, "valueRefs", Kind::Boolean, false)?);
            match engine
                .instances()
                .get(
                    principal,
                    name,
                    id,
                    GetOptions {
                        namespace: ns,
                        value_refs,
                    },
                )
                .map_err(failed)?
            {
                Some(record) => Ok(tagged(
                    request,
                    to_json(&record)?,
                    200,
                    vec![etag(record.seq)],
                )),
                None => Err(Problem::not_found(format!(
                    "{name} {id} does not exist in namespace {namespace}"
                ))),
            }
        }
        "updateInstance" => {
            let input = required_body(request)?;
            media_type(request, matched, MERGE_PATCH_MEDIA_TYPE)?;
            let id = matched.param("id");
            let expected_seq = expected_seq_of(engine, request, principal, name, id, ns)?;
            let preconditions = preconditions_of(request)?;
            let record = engine
                .instances()
                .update(
                    principal,
                    name,
                    id,
                    &input,
                    WriteOptions {
                        namespace: ns,
                        expected_seq,
                        preconditions: preconditions.as_ref(),
                    },
                )
                .map_err(failed)?;
            Ok(tagged(
                request,
                to_json(&record)?,
                200,
                vec![etag(record.seq)],
            ))
        }
        "deleteInstance" => {
            let id = matched.param("id");
            let expected_seq = expected_seq_of(engine, request, principal, name, id, ns)?;
            let preconditions = preconditions_of(request)?;
            let deleted = engine
                .instances()
                .delete(
                    principal,
                    name,
                    id,
                    WriteOptions {
                        namespace: ns,
                        expected_seq,
                        preconditions: preconditions.as_ref(),
                    },
                )
                .map_err(failed)?;
            if !deleted {
                return Err(Problem::not_found(format!(
                    "{name} {id} does not exist in namespace {namespace}"
                )));
            }
            Ok(ok(request, Value::Null))
        }
        "invokeOperation" => {
            let input = json_body(request)?;
            if input.is_some() {
                media_type(request, matched, JSON_MEDIA_TYPE)?;
            }
            let id = matched.param("id");
            let expected_seq = expected_seq_of(engine, request, principal, name, id, ns)?;
            let preconditions = preconditions_of(request)?;
            let params = input.unwrap_or_else(|| Value::Object(Map::new()));
            let outcome = engine
                .instances()
                .operate(
                    principal,
                    name,
                    id,
                    matched.param("operation"),
                    &params,
                    WriteOptions {
                        namespace: ns,
                        expected_seq,
                        preconditions: preconditions.as_ref(),
                    },
                )
                .map_err(failed)?;
            Ok(tagged(
                request,
                outcome.result,
                200,
                vec![etag(outcome.seq)],
            ))
        }
        "invokeSchemaOperation" => {
            let input = json_body(request)?;
            if input.is_some() {
                media_type(request, matched, JSON_MEDIA_TYPE)?;
            }
            let params = input.unwrap_or_else(|| Value::Object(Map::new()));
            Ok(ok(
                request,
                engine
                    .instances()
                    .invoke_schema(principal, name, matched.param("operation"), &params, ns)
                    .map_err(failed)?,
            ))
        }
        "listTools" => Ok(ok(
            request,
            engine
                .tools()
                .manifest(
                    principal,
                    ToolTarget {
                        namespace: ns,
                        filter: options.tools.as_deref(),
                    },
                )
                .map_err(failed)?,
        )),
        "listBehaviors" => Ok(ok(
            request,
            engine.tools().list_behaviors(principal).map_err(failed)?,
        )),
        "describeBehavior" => Ok(ok(
            request,
            engine
                .tools()
                .describe_behavior(principal, name)
                .map_err(failed)?,
        )),
        "searchSchemas" => {
            let input = required_body(request)?;
            media_type(request, matched, JSON_MEDIA_TYPE)?;
            Ok(ok(
                request,
                engine.search(principal, &input, ns).map_err(failed)?,
            ))
        }
        "getValue" => Ok(ok(
            request,
            to_json(
                &engine
                    .values()
                    .get(principal, matched.param("hash"), ns)
                    .map_err(failed)?,
            )?,
        )),
        "readEvents" => read_events(engine, options, request, &query, namespace, principal),
        _ => Err(Problem::not_found("Not Found")),
    }
}

// read_events is the event log: a JSON page, or with Accept:
// text/event-stream the stream.
fn read_events(
    engine: &Engine,
    options: &ApiOptions,
    request: &Request,
    query: &[(String, String)],
    namespace: &str,
    principal: &Principal,
) -> Served {
    let after = text_param(query_param(query, "after", Kind::String, false)?);
    let limit = match query_param(query, "limit", Kind::Integer, false)? {
        Some(Param::Integer(limit)) => Some(limit),
        _ => None,
    };
    let schema = text_param(query_param(query, "schema", Kind::String, false)?);
    let instance_id = text_param(query_param(query, "instanceId", Kind::String, false)?);
    let list = |name: &str| -> Result<Option<Vec<String>>, Problem> {
        match params::decode(
            Location::Query,
            name,
            Kind::String,
            false,
            true,
            &params::all(query, name),
        )? {
            Some(Param::List(items)) => Ok(Some(items)),
            _ => Ok(None),
        }
    };
    let kinds = list("kind")?;
    let behaviors = list("behavior")?;
    let exclude = list("exclude")?;
    let resume = last_event_id_of(request)?;
    let after = match resume {
        Some(cursor) => Some(EventsAfter::Cursor(cursor)),
        None => after_of(after.as_deref())?,
    };
    let read = ReadEvents {
        namespace: Some(namespace),
        schema: schema.as_deref(),
        instance_id: instance_id.as_deref(),
        after,
        limit: None,
        kinds,
        behaviors,
        exclude,
    };
    if !wants_event_stream(request.header("accept")) {
        let page = engine
            .events()
            .read(principal, &ReadEvents { limit, ..read })
            .map_err(failed)?;
        return Ok(ok(request, to_json(&page)?));
    }
    super::stream::open(engine, options, request, principal, &read)
}

/// The cursor a reconnecting EventSource sends; it wins over `after`.
fn last_event_id_of(request: &Request) -> Result<Option<i64>, Problem> {
    let Some(header) = request.header("last-event-id") else {
        return Ok(None);
    };
    let header = header.trim();
    if header.is_empty() {
        return Ok(None);
    }
    cursor_of(header).map(Some).ok_or_else(|| {
        Problem::bad_request("Last-Event-ID is an event cursor, a non-negative integer")
    })
}

// cursor_of reads ^[0-9]{1,15}$.
fn cursor_of(text: &str) -> Option<i64> {
    (!text.is_empty() && text.len() <= 15 && text.bytes().all(|b| b.is_ascii_digit()))
        .then(|| text.parse().ok())
        .flatten()
}

/// The `after` query parameter: an event cursor, or `head`.
fn after_of(value: Option<&str>) -> Result<Option<EventsAfter>, Problem> {
    match value {
        None => Ok(None),
        Some("head") => Ok(Some(EventsAfter::Head)),
        Some(text) => cursor_of(text)
            .map(|cursor| Some(EventsAfter::Cursor(cursor)))
            .ok_or_else(|| {
                Problem::bad_request("after is an event cursor, a non-negative integer, or head")
            }),
    }
}

/// Whether Accept asks for text/event-stream with a quality above zero.
pub(crate) fn wants_event_stream(accept: Option<&str>) -> bool {
    let Some(accept) = accept.filter(|accept| !accept.is_empty()) else {
        return false;
    };
    accept.split(',').any(|range| {
        let mut parts = range.split(';').map(|part| part.trim().to_lowercase());
        if parts.next().as_deref() != Some("text/event-stream") {
            return false;
        }
        match parts.find(|part| part.starts_with("q=")) {
            None => true,
            Some(quality) => quality[2..].trim().parse::<f64>().is_ok_and(|q| q > 0.0),
        }
    })
}

/// The sequence If-Match names, for expectedSeq: `*` and no header name
/// none; a tag that is no sequence never matches (0); from several, the one
/// the instance is at, if any.
fn expected_seq_of(
    engine: &Engine,
    request: &Request,
    principal: &Principal,
    name: &str,
    id: &str,
    namespace: Option<&str>,
) -> Result<Option<i64>, Problem> {
    let Some(header) = request.header("if-match") else {
        return Ok(None);
    };
    if header.trim() == "*" {
        return Ok(None);
    }
    let mut seqs: Vec<i64> = Vec::new();
    for tag in header.split(',') {
        let tag = tag.trim();
        if let Some(inner) = tag
            .strip_prefix('"')
            .and_then(|rest| rest.strip_suffix('"'))
        {
            if let Some(seq) = cursor_of(inner) {
                if !seqs.contains(&seq) {
                    seqs.push(seq);
                }
            }
        }
    }
    if seqs.len() <= 1 {
        return Ok(Some(seqs.first().copied().unwrap_or(0)));
    }
    let current = engine
        .instances()
        .get(
            principal,
            name,
            id,
            GetOptions {
                namespace,
                value_refs: false,
            },
        )
        .map_err(failed)?;
    Ok(Some(match current {
        Some(record) if seqs.contains(&record.seq) => record.seq,
        _ => 0,
    }))
}

/// The preconditions the Preconditions header carries: a JSON object, or
/// 400.
fn preconditions_of(request: &Request) -> Result<Option<Value>, Problem> {
    let Some(header) = request.header("preconditions") else {
        return Ok(None);
    };
    match serde_json::from_str::<Value>(header) {
        Ok(value @ Value::Object(_)) => Ok(Some(value)),
        _ => Err(Problem::bad_request(
            "Preconditions is a JSON object of each behavior's entry by its name",
        )),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn accept_asks_for_the_stream_with_a_quality_above_zero() {
        assert!(wants_event_stream(Some("text/event-stream")));
        assert!(wants_event_stream(Some(
            "application/json, text/event-stream;q=0.5"
        )));
        assert!(!wants_event_stream(Some("text/event-stream;q=0")));
        assert!(!wants_event_stream(Some("application/json")));
        assert!(!wants_event_stream(None));
        assert_eq!(cursor_of("0012"), Some(12));
        assert_eq!(cursor_of("-1"), None);
        assert_eq!(cursor_of("1.5"), None);
    }
}
