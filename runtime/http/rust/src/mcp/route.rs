//! A generated router's tools (D81, D116): a tool of the table the build
//! writes, the request a call of it makes to its operation's route, and
//! what the route's response comes to, the port of the TypeScript
//! runtime's `McpRouteTool`, `toolRequest`, `filterQuery`, `outcomeOf` and
//! `problemOf`. Nothing here holds a router: the generated one makes the
//! request, answers it through its own `handle`, and reads the response.

use super::confirm::CONFIRMATION_ARGUMENT;
use super::json::number_text;
use super::{CallOutcome, Content, Problem, Tool};
use crate::filter::MAX_IN_VALUES;
use crate::response::{problem_body, status_title, REQUEST_ID_HEADER};
use crate::ApiError;
use base64::Engine as _;
use bytes::Bytes;
use http::header::{ACCEPT, CONTENT_TYPE};
use http::{HeaderMap, HeaderValue, Method, Request, Response, Uri};
use serde::{Deserialize, Serialize};
use serde_json::{json, Map, Value};
use std::collections::HashSet;

/// Where one tool argument goes in the request to its operation's route.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum ArgumentLocation {
    /// A path parameter.
    Path,
    /// A query parameter, once per element of a list.
    Query,
    /// A member of the JSON body object.
    Body,
    /// The operation's filter (FilterBy, D106), an object with a member
    /// per field, which goes to the query string as the bracket form.
    Filter,
}

/// One argument of a tool: its key in the tool's arguments, and where the
/// route reads it.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct ToolArgument {
    /// The argument's key in the tool's arguments: its property in the
    /// input schema.
    pub key: String,
    /// Where it goes in the route's request.
    #[serde(rename = "in")]
    pub location: ArgumentLocation,
    /// The parameter's or the member's wire name; a filter's is the
    /// parameter its brackets follow, `filter`.
    pub name: String,
}

/// One tool of a generated router: what `tools/list` answers, and the
/// operation whose route a call runs through. It reads the JSON the build
/// writes a tool as, the TypeScript runtime's `McpRouteTool`.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
pub struct RouteTool {
    /// The tool as `tools/list` answers it, but for the confirmation
    /// argument of a tool that asks ([`RouteTool::listed`]).
    #[serde(flatten)]
    pub tool: Tool,
    /// The operation's key in the router's operation table.
    pub operation: String,
    /// Where each argument goes in the route's request.
    #[serde(default)]
    pub arguments: Vec<ToolArgument>,
    /// Whether a call runs only once the person confirms it: true for a
    /// tool whose invocation policy is one the build's policy confirms,
    /// ask in the core's (D93).
    #[serde(default, skip_serializing_if = "is_false")]
    pub confirm: bool,
}

// serde's skip_serializing_if hands the field by reference.
fn is_false(flag: &bool) -> bool {
    !*flag
}

impl RouteTool {
    /// The tool as `tools/list` answers it: a tool that asks with the
    /// argument its confirmation comes back in
    /// ([`with_confirmation_argument`]).
    pub fn listed(&self) -> Tool {
        let mut tool = self.tool.clone();
        if self.confirm {
            tool.input_schema = with_confirmation_argument(&tool.input_schema);
        }
        tool
    }
}

/// What a router checks of its tools before it serves them, as the
/// TypeScript runtime's `mountMcp` does: no two share a name, and none
/// that asks takes an argument named [`CONFIRMATION_ARGUMENT`], which
/// carries its confirmation (D93). That each names an operation the router
/// has is the router's to check.
pub fn check_route_tools(tools: &[RouteTool]) -> Result<(), String> {
    let mut names = HashSet::new();
    for tool in tools {
        let name = tool.tool.name.as_str();
        if !names.insert(name) {
            return Err(format!("two MCP tools are named {name}"));
        }
        if tool.confirm
            && tool
                .arguments
                .iter()
                .any(|argument| argument.key == CONFIRMATION_ARGUMENT)
        {
            return Err(format!(
                "MCP tool {name} asks the person before it runs, and takes an argument {CONFIRMATION_ARGUMENT}, which carries its confirmation"
            ));
        }
    }
    Ok(())
}

/// Who is calling, which a confirmation is bound to (D93): the principal's
/// subject and the calling service's deployable the endpoint's request
/// established, as JSON, null for each it did not. The text is the
/// TypeScript runtime's `callerOf`.
pub fn caller_of(subject: Option<&str>, service_deployable: Option<&str>) -> String {
    let part = |text: Option<&str>| text.map_or(Value::Null, Value::from);
    Value::Array(vec![part(subject), part(service_deployable)]).to_string()
}

/// The input schema of a tool that confirms (D93), with the optional
/// argument a confirmation token comes back in, so a client that checks
/// arguments against it sends the token, and a model reads where it goes.
/// A schema that is no object is returned as it is.
pub fn with_confirmation_argument(input_schema: &Value) -> Value {
    let Some(schema) = input_schema.as_object() else {
        return input_schema.clone();
    };
    let mut properties = schema
        .get("properties")
        .and_then(Value::as_object)
        .cloned()
        .unwrap_or_default();
    properties.insert(
        CONFIRMATION_ARGUMENT.to_owned(),
        json!({
            "type": "string",
            "description": "The confirmation token a call of this tool answered with confirmation_required, sent once the person confirms the call; leave it out to ask for one.",
        }),
    );
    let mut schema = schema.clone();
    schema.insert("properties".to_owned(), Value::Object(properties));
    Value::Object(schema)
}

// Headers of the endpoint's request a call's request does not carry: its
// own body's, its conditions, the MCP transport's and the connection's.
// Every other one goes on, so an authenticator that reads its own header
// (an API key, say) authenticates the call as it did the endpoint. The
// TypeScript runtime's list also holds host, which there is part of the
// URL the call's request keeps; here it is a header, and goes on.
const UNFORWARDED: [&str; 22] = [
    "accept",
    "connection",
    "content-encoding",
    "content-length",
    "content-type",
    "expect",
    "if-match",
    "if-modified-since",
    "if-none-match",
    "if-range",
    "if-unmodified-since",
    "keep-alive",
    "last-event-id",
    "mcp-method",
    "mcp-name",
    "mcp-protocol-version",
    "mcp-session-id",
    "range",
    "te",
    "trailer",
    "transfer-encoding",
    "upgrade",
];

/// The request to the tool's route that a call with `arguments` makes:
/// `method` on `path_template`, the route's path with a `{name}` for each
/// path parameter, each argument where the route reads it. It carries the
/// headers of `incoming`, the endpoint's own request, but the transport's,
/// so the credentials that authenticated the endpoint authenticate the
/// call; its scheme and authority, when its URI has them; and
/// `request_id`. A body is a JSON object of the body arguments.
///
/// The `Err` is the refusal, a 400 problem, of arguments that name an
/// argument the tool does not take, leave out a path parameter, or hold a
/// filter with no bracket form.
pub fn tool_request<B>(
    tool: &RouteTool,
    method: &Method,
    path_template: &str,
    arguments: &Map<String, Value>,
    incoming: &Request<B>,
    request_id: &str,
) -> Result<Request<Bytes>, Problem> {
    let name = tool.tool.name.as_str();
    let refuse = |detail: String| problem_body(&ApiError::bad_request(detail), Some(request_id));
    if let Some(unknown) = arguments
        .keys()
        .find(|key| !tool.arguments.iter().any(|argument| argument.key == **key))
    {
        let mut keys: Vec<&str> = Vec::new();
        for argument in &tool.arguments {
            if !keys.contains(&argument.key.as_str()) {
                keys.push(&argument.key);
            }
        }
        let takes = if keys.is_empty() {
            "takes no arguments".to_owned()
        } else {
            format!("takes {}", keys.join(", "))
        };
        return Err(refuse(format!(
            "{name} {takes}, not {}",
            Value::from(unknown.as_str())
        )));
    }
    let mut path = path_template.to_owned();
    let mut query: Vec<(String, String)> = Vec::new();
    let mut body = Map::new();
    let mut has_body = false;
    for argument in &tool.arguments {
        let value = arguments.get(&argument.key);
        match argument.location {
            ArgumentLocation::Path => {
                let Some(value) = value.filter(|value| !value.is_null()) else {
                    return Err(refuse(format!("{name}: {} is required", argument.key)));
                };
                path = path.replace(
                    &format!("{{{}}}", argument.name),
                    &encode(&wire_text(value), PATH_KEPT, false),
                );
            }
            ArgumentLocation::Query => match value {
                None | Some(Value::Null) => {}
                Some(Value::Array(items)) => {
                    for item in items {
                        query.push((argument.name.clone(), wire_text(item)));
                    }
                }
                Some(single) => query.push((argument.name.clone(), wire_text(single))),
            },
            ArgumentLocation::Filter => {
                if let Some(filter) = value.filter(|value| !value.is_null()) {
                    filter_query(&mut query, &argument.name, filter)
                        .map_err(|wrong| refuse(format!("{name}: {}{wrong}", argument.key)))?;
                }
            }
            ArgumentLocation::Body => {
                if let Some(value) = value {
                    body.insert(argument.name.clone(), value.clone());
                    has_body = true;
                }
            }
        }
    }
    if !query.is_empty() {
        path.push('?');
        for (index, (key, value)) in query.iter().enumerate() {
            if index > 0 {
                path.push('&');
            }
            path.push_str(&encode(key, QUERY_KEPT, true));
            path.push('=');
            path.push_str(&encode(value, QUERY_KEPT, true));
        }
    }
    let mut uri = Uri::builder().path_and_query(path);
    if let (Some(scheme), Some(authority)) = (incoming.uri().scheme(), incoming.uri().authority()) {
        uri = uri.scheme(scheme.clone()).authority(authority.clone());
    }
    let mut request = Request::new(if has_body {
        Bytes::from(Value::Object(body).to_string())
    } else {
        Bytes::new()
    });
    *request.method_mut() = method.clone();
    *request.uri_mut() = uri.build().map_err(|error| {
        problem_body(
            &ApiError::internal(format!(
                "{name}: the request to {path_template} is no URI: {error}"
            )),
            Some(request_id),
        )
    })?;
    *request.headers_mut() = forwarded(incoming.headers());
    let headers = request.headers_mut();
    headers.insert(ACCEPT, HeaderValue::from_static("application/json"));
    match HeaderValue::from_bytes(request_id.as_bytes()) {
        Ok(id) => headers.insert(REQUEST_ID_HEADER, id),
        Err(_) => headers.remove(REQUEST_ID_HEADER),
    };
    if has_body {
        headers.insert(CONTENT_TYPE, HeaderValue::from_static("application/json"));
    }
    Ok(request)
}

// forwarded is the endpoint request's headers a call's request carries.
fn forwarded(incoming: &HeaderMap) -> HeaderMap {
    let mut headers = HeaderMap::new();
    for (name, value) in incoming {
        if !UNFORWARDED.contains(&name.as_str()) {
            headers.append(name.clone(), value.clone());
        }
    }
    headers
}

// A path or query value as the route reads it: a string as it is, a number
// or a boolean as its text, and anything else as JSON.
fn wire_text(value: &Value) -> String {
    wire_value(value).unwrap_or_else(|| value.to_string())
}

// What a filter's member or operand may be beside an object or a list: a
// value the query string carries as its text.
fn wire_value(value: &Value) -> Option<String> {
    match value {
        Value::String(text) => Some(text.clone()),
        Value::Number(number) => Some(number_text(number)),
        Value::Bool(flag) => Some(flag.to_string()),
        _ => None,
    }
}

// The space the routes' reader trims an in's values of, at either end of
// one: what JavaScript's \s matches, and U+0085, which Rust's trim takes.
fn is_outer_space(character: char) -> bool {
    character.is_whitespace() || character == '\u{feff}'
}

// unsendable is whether the route would split, drop or trim a value of an
// in or a notIn: one that is blank, or holds a comma or a space at either
// end.
fn unsendable(item: &str) -> bool {
    item.is_empty()
        || item.contains(',')
        || item.starts_with(is_outer_space)
        || item.ends_with(is_outer_space)
}

// filter_query writes a tool's filter argument (FilterBy, D106) into the
// query of its route's request as the bracket form the route's reader
// reads, under name, the parameter the brackets follow: a member that
// holds a value is equality, name[field]=value, and one that holds an
// object of operators is name[field][op]=value for each, an in's and a
// notIn's list joined by commas, as the wire lists it (D112). A field or
// an operator the operation does not take goes to the route as it is,
// whose reader refuses it with the 400 it answers any caller. The Err is
// what is wrong with a filter that has no bracket form, after the
// argument's name: a filter that is no object, a member that is null or a
// list, an operand that is null, an object, or a list for any operator
// but in and notIn, and what the form would read as another filter than
// the caller sent: an in or a notIn of more than MAX_IN_VALUES values, a
// value of one that is blank or holds a comma or a space at either end,
// and a contains of no text.
fn filter_query(
    query: &mut Vec<(String, String)>,
    name: &str,
    filter: &Value,
) -> Result<(), String> {
    let Some(fields) = filter.as_object() else {
        return Err(" is an object with a member per field".to_owned());
    };
    for (field, member) in fields {
        if let Some(text) = wire_value(member) {
            query.push((format!("{name}[{field}]"), text));
            continue;
        }
        let Some(operators) = member.as_object() else {
            return Err(format!(
                ".{field} is a value, for equality, or an object of operators"
            ));
        };
        for (operator, operand) in operators {
            let key = format!("{name}[{field}][{operator}]");
            let at = format!(".{field}.{operator}");
            if matches!(operator.as_str(), "in" | "notIn") {
                let items: Option<Vec<String>> = match operand {
                    Value::Array(items) => items.iter().map(wire_value).collect(),
                    single => wire_value(single).map(|text| vec![text]),
                };
                let Some(items) = items else {
                    return Err(format!("{at} is a list of values"));
                };
                if items.len() > MAX_IN_VALUES {
                    return Err(format!("{at} holds more than {MAX_IN_VALUES} values"));
                }
                if let Some(index) = items.iter().position(|item| unsendable(item)) {
                    return Err(format!(
                        "{at}[{index}] must not be blank or hold a comma or a space at either end"
                    ));
                }
                query.push((key, items.join(",")));
            } else if operator == "contains" && operand.as_str() == Some("") {
                return Err(format!("{at} must not be empty"));
            } else if let Some(text) = wire_value(operand) {
                query.push((key, text));
            } else {
                return Err(format!("{at} is a value"));
            }
        }
    }
    Ok(())
}

// The characters JavaScript's encodeURIComponent keeps beside letters and
// digits, for a path segment, and those a URLSearchParams keeps, for a
// query string, where a space is a plus sign.
const PATH_KEPT: &[u8] = b"-_.!~*'()";
const QUERY_KEPT: &[u8] = b"*-._";

// encode percent-encodes text as UTF-8, but for letters, digits and the
// bytes kept.
fn encode(text: &str, kept: &[u8], plus_for_space: bool) -> String {
    const HEX: &[u8; 16] = b"0123456789ABCDEF";
    let mut out = String::with_capacity(text.len());
    for byte in text.bytes() {
        if byte.is_ascii_alphanumeric() || kept.contains(&byte) {
            out.push(char::from(byte));
        } else if byte == b' ' && plus_for_space {
            out.push('+');
        } else {
            out.push('%');
            out.push(char::from(HEX[usize::from(byte >> 4)]));
            out.push(char::from(HEX[usize::from(byte & 15)]));
        }
    }
    out
}

fn media_type(response: &Response<Bytes>) -> String {
    response
        .headers()
        .get(CONTENT_TYPE)
        .and_then(|value| value.to_str().ok())
        .and_then(|value| value.split(';').next())
        .unwrap_or_default()
        .trim()
        .to_ascii_lowercase()
}

fn is_json_media(media: &str) -> bool {
    media == "application/json" || media.ends_with("+json")
}

fn is_text_media(media: &str) -> bool {
    media.is_empty()
        || media.starts_with("text/")
        || media == "application/xml"
        || media.ends_with("+xml")
}

// body_text is a response's body as text, a byte order mark aside, as a
// fetch Response reads it.
fn body_text(response: &Response<Bytes>) -> String {
    let text = String::from_utf8_lossy(response.body());
    text.strip_prefix('\u{feff}').unwrap_or(&text).to_owned()
}

/// A route's refusal (4xx or 5xx) as its problem document, or one made of
/// its status and text, under `request_id`, when the route answered
/// another body.
pub fn problem_of(response: &Response<Bytes>, request_id: &str) -> Problem {
    let text = body_text(response);
    if is_json_media(&media_type(response)) {
        if let Ok(problem @ Value::Object(_)) = serde_json::from_str::<Value>(&text) {
            return problem;
        }
    }
    let detail = match text.trim() {
        "" => "Request failed",
        detail => detail,
    };
    json!({
        "type": "about:blank",
        "title": status_title(response.status()),
        "status": response.status().as_u16(),
        "detail": detail,
        "requestId": request_id,
    })
}

/// What the route's response comes to: a refusal as its problem document
/// ([`problem_of`]); a success's envelope data, a JSON body that is no
/// envelope as it is, text as text content, and any other body as a
/// resource at `uri`, the route's path.
pub fn outcome_of(response: &Response<Bytes>, request_id: &str, uri: &str) -> CallOutcome {
    if response.status().as_u16() >= 400 {
        return CallOutcome::Problem(problem_of(response, request_id));
    }
    if response.body().is_empty() {
        return CallOutcome::Result(Value::Null);
    }
    let media = media_type(response);
    if is_json_media(&media) {
        let text = body_text(response);
        return match serde_json::from_str::<Value>(&text) {
            Ok(Value::Object(mut envelope))
                if envelope.contains_key("data")
                    && envelope.get("meta").is_some_and(Value::is_object) =>
            {
                CallOutcome::Result(envelope.remove("data").unwrap_or_default())
            }
            Ok(value) => CallOutcome::Result(value),
            Err(_) => CallOutcome::Content(vec![Content::Text { text }]),
        };
    }
    if is_text_media(&media) {
        return CallOutcome::Content(vec![Content::Text {
            text: body_text(response),
        }]);
    }
    CallOutcome::Content(vec![Content::Resource {
        uri: uri.to_owned(),
        mime_type: media,
        blob: base64::engine::general_purpose::STANDARD.encode(response.body()),
    }])
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_path_segment_and_a_query_value_are_encoded_as_javascript_encodes_them() {
        assert_eq!(
            encode("a b/c?d&e=f", PATH_KEPT, false),
            "a%20b%2Fc%3Fd%26e%3Df"
        );
        assert_eq!(encode("-_.!~*'()", PATH_KEPT, false), "-_.!~*'()");
        assert_eq!(encode("\u{e9}{x}", PATH_KEPT, false), "%C3%A9%7Bx%7D");
        assert_eq!(
            encode("a b+c&d=e~f", QUERY_KEPT, true),
            "a+b%2Bc%26d%3De%7Ef"
        );
        assert_eq!(
            encode("filter[name][in]", QUERY_KEPT, true),
            "filter%5Bname%5D%5Bin%5D"
        );
        assert_eq!(encode("*-._!'()", QUERY_KEPT, true), "*-._%21%27%28%29");
    }

    #[test]
    fn an_outer_space_is_any_the_routes_reader_trims() {
        for item in [
            " a",
            "a ",
            "a\t",
            "\u{85}a",
            "a\u{a0}",
            "\u{feff}a",
            "",
            "a,b",
        ] {
            assert!(unsendable(item), "{item:?}");
        }
        for item in ["a", "a b", "a\u{85}b"] {
            assert!(!unsendable(item), "{item:?}");
        }
    }
}
