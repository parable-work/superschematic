//! The engine's calls by name, with JSON arguments and a JSON result, as
//! the TypeScript engine's methods take and return them (D58): what a
//! binding (the Node-API addon, the C ABI's `eng_call`) and the scenario
//! replayer call. `instances.create` with `["Order", {"title": "Desk"},
//! {"id": "o1"}]` is `engine.instances.create(principal, 'Order', { title:
//! 'Desk' }, { id: 'o1' })`.
//!
//! The typed API takes Rust's types, so what JSON can say and they cannot
//! is checked here, in the TypeScript engine's words: a page size, a
//! sequence or a cursor that is no integer (`1.5`, `"tail"`), a filter
//! that is no list. A value is named as JavaScript's `String()` writes it.
//! A call returns `None` where the TypeScript method returns `undefined`.
//!
//! The behaviors' calls are here (`instances.invoke`, `operate`,
//! `invokeSchema`, and `behaviors.has`, `names` and `declaration`);
//! `behaviors.register` takes an implementation, which JSON cannot carry,
//! so a host registers through [`crate::Behaviors::register`]. Milestone 6
//! adds the runner's and the tools'.

use std::borrow::Cow;

use serde_json::{Map, Value};

use crate::engine::Engine;
use crate::errors::{EngineError, Error, Result};
use crate::events::{check_filters, cursor_refusal, EventsAfter, ReadEvents};
use crate::instances::{
    expected_seq_refusal, CreateOptions, GetOptions, ListOptions, WriteOptions,
};
use crate::json;
use crate::namespaces::name_refusal;
use crate::paging::page_size_refusal;
use crate::principal::{check_principal, Principal};
use crate::registry::{DefineOptions, SchemaInput, SchemaTarget, ValidateOptions};

/// The calls [`call`] answers, by name.
pub const CALLS: [&str; 33] = [
    "schemas.define",
    "schemas.publish",
    "schemas.live",
    "schemas.draft",
    "schemas.version",
    "schemas.list",
    "schemas.capabilities",
    "schemas.validate",
    "schemas.behaviors",
    "instances.create",
    "instances.get",
    "instances.list",
    "instances.lookup",
    "instances.update",
    "instances.delete",
    "instances.invoke",
    "instances.operate",
    "instances.invokeSchema",
    "events.read",
    "events.head",
    "events.floor",
    "namespaces.create",
    "namespaces.archive",
    "namespaces.unarchive",
    "namespaces.get",
    "namespaces.list",
    "namespaces.names",
    "values.get",
    "values.sweep",
    "values.thresholdBytes",
    "behaviors.has",
    "behaviors.names",
    "behaviors.declaration",
];

/// Calls a method of the engine by name, as `principal` for a method that
/// takes one, with its arguments after the principal. An unknown method is
/// refused as an argument the engine cannot take.
pub fn call(
    engine: &Engine,
    method: &str,
    principal: Option<&Principal>,
    args: &[Value],
) -> Result<Option<Value>> {
    let who = || {
        principal.ok_or_else(|| {
            Error::from(EngineError::invalid_argument(
                "an engine call needs a principal with a subject",
            ))
        })
    };
    let arg = |index: usize| args.get(index).unwrap_or(&Value::Null);
    let options = |index: usize| options_of(args.get(index));
    let result = match method {
        "schemas.define" => {
            let options = options(1);
            let namespace = text_member(options, "namespace");
            let source = text_member(options, "source");
            let input = match arg(0) {
                Value::String(text) => SchemaInput::Text(Cow::Borrowed(text)),
                document => SchemaInput::Document(Cow::Borrowed(document)),
            };
            to_json(engine.schemas().define(
                who()?,
                input,
                DefineOptions {
                    namespace: namespace.as_deref(),
                    source: source.as_deref(),
                },
            )?)
        }
        "schemas.publish" => {
            let namespace = text_member(options(1), "namespace");
            to_json(engine.schemas().publish(
                who()?,
                &text(arg(0)),
                SchemaTarget {
                    namespace: namespace.as_deref(),
                },
            )?)
        }
        "schemas.live" | "schemas.draft" => {
            let namespace = text_member(options(1), "namespace");
            let target = SchemaTarget {
                namespace: namespace.as_deref(),
            };
            let schemas = engine.schemas();
            let found = if method == "schemas.live" {
                schemas.live(who()?, &text(arg(0)), target)?
            } else {
                schemas.draft(who()?, &text(arg(0)), target)?
            };
            return found.map(to_json).transpose();
        }
        "schemas.version" => {
            let principal = who()?;
            let version = integer(arg(1)).ok_or_else(|| {
                EngineError::invalid_argument(format!(
                    "a schema version is a positive integer, got {}",
                    js_string(arg(1))
                ))
            })?;
            let namespace = text_member(options(2), "namespace");
            return engine
                .schemas()
                .version(
                    principal,
                    &text(arg(0)),
                    version,
                    SchemaTarget {
                        namespace: namespace.as_deref(),
                    },
                )?
                .map(to_json)
                .transpose();
        }
        "schemas.list" | "schemas.capabilities" => {
            let namespace = text_member(options(0), "namespace");
            let target = SchemaTarget {
                namespace: namespace.as_deref(),
            };
            if method == "schemas.list" {
                to_json(engine.schemas().list(who()?, target)?)
            } else {
                to_json(engine.schemas().capabilities(who()?, target)?)
            }
        }
        "schemas.validate" | "schemas.behaviors" => {
            let at = if method == "schemas.validate" { 2 } else { 1 };
            let principal = who()?;
            let options = options(at);
            let namespace = text_member(options, "namespace");
            let version = match options.and_then(|options| options.get("version")) {
                None => None,
                Some(given) => Some(integer(given).ok_or_else(|| {
                    EngineError::invalid_argument(format!(
                        "a schema version is a positive integer, got {}",
                        js_string(given)
                    ))
                })?),
            };
            let target = ValidateOptions {
                namespace: namespace.as_deref(),
                version,
            };
            if method == "schemas.validate" {
                to_json(
                    engine
                        .schemas()
                        .validate(principal, &text(arg(0)), arg(1), target)?,
                )
            } else {
                to_json(
                    engine
                        .schemas()
                        .behaviors(principal, &text(arg(0)), target)?,
                )
            }
        }
        "instances.create" => {
            let options = options(2);
            let namespace = text_member(options, "namespace");
            let id = options.and_then(|options| options.get("id")).map(js_string);
            let behaviors = options.and_then(|options| options.get("behaviors"));
            to_json(engine.instances().create(
                who()?,
                &text(arg(0)),
                arg(1),
                CreateOptions {
                    namespace: namespace.as_deref(),
                    id: id.as_deref(),
                    behaviors,
                },
            )?)
        }
        "instances.get" | "instances.lookup" => {
            let options = options(2);
            let namespace = text_member(options, "namespace");
            let get = GetOptions {
                namespace: namespace.as_deref(),
                value_refs: flag_member(options, "valueRefs"),
            };
            let found = if method == "instances.get" {
                engine
                    .instances()
                    .get(who()?, &text(arg(0)), &text(arg(1)), get)?
            } else {
                engine
                    .instances()
                    .lookup(who()?, &text(arg(0)), arg(1), get)?
            };
            return found.map(to_json).transpose();
        }
        "instances.list" => {
            let principal = who()?;
            let options = options(1);
            let namespace = text_member(options, "namespace");
            let limit = match options.and_then(|options| options.get("limit")) {
                None => None,
                Some(given) => {
                    Some(integer(given).ok_or_else(|| page_size_refusal(&js_string(given)))?)
                }
            };
            let cursor = options
                .and_then(|options| options.get("cursor"))
                .map(js_string);
            to_json(engine.instances().list(
                principal,
                &text(arg(0)),
                ListOptions {
                    namespace: namespace.as_deref(),
                    limit,
                    cursor: cursor.as_deref(),
                    value_refs: flag_member(options, "valueRefs"),
                    filter: options.and_then(|options| options.get("where")),
                },
            )?)
        }
        "instances.update" | "instances.delete" => {
            let principal = who()?;
            let at = if method == "instances.update" { 3 } else { 2 };
            let options = options(at);
            let namespace = text_member(options, "namespace");
            let expected_seq = match options.and_then(|options| options.get("expectedSeq")) {
                None => None,
                Some(given) => Some(
                    integer(given)
                        .filter(|seq| *seq >= 0)
                        .ok_or_else(|| expected_seq_refusal(&js_string(given)))?,
                ),
            };
            let write = WriteOptions {
                namespace: namespace.as_deref(),
                expected_seq,
                preconditions: options.and_then(|options| options.get("preconditions")),
            };
            if method == "instances.update" {
                to_json(engine.instances().update(
                    principal,
                    &text(arg(0)),
                    &text(arg(1)),
                    arg(2),
                    write,
                )?)
            } else {
                Ok(Value::Bool(engine.instances().delete(
                    principal,
                    &text(arg(0)),
                    &text(arg(1)),
                    write,
                )?))
            }
        }
        "instances.invoke" | "instances.operate" => {
            let principal = who()?;
            let options = options(4);
            let namespace = text_member(options, "namespace");
            let expected_seq = match options.and_then(|options| options.get("expectedSeq")) {
                None => None,
                Some(given) => Some(
                    integer(given)
                        .filter(|seq| *seq >= 0)
                        .ok_or_else(|| expected_seq_refusal(&js_string(given)))?,
                ),
            };
            let write = WriteOptions {
                namespace: namespace.as_deref(),
                expected_seq,
                preconditions: options.and_then(|options| options.get("preconditions")),
            };
            let empty = Value::Object(Map::new());
            let outcome = engine.instances().operate(
                principal,
                &text(arg(0)),
                &text(arg(1)),
                &text(arg(2)),
                args.get(3).unwrap_or(&empty),
                write,
            )?;
            if method == "instances.invoke" {
                Ok(outcome.result)
            } else {
                to_json(outcome)
            }
        }
        "instances.invokeSchema" => {
            let principal = who()?;
            let namespace = text_member(options(3), "namespace");
            let empty = Value::Object(Map::new());
            engine.instances().invoke_schema(
                principal,
                &text(arg(0)),
                &text(arg(1)),
                args.get(2).unwrap_or(&empty),
                namespace.as_deref(),
            )
        }
        "behaviors.has" => Ok(Value::Bool(engine.behaviors().has(&text(arg(0))))),
        "behaviors.names" => to_json(engine.behaviors().names()),
        "behaviors.declaration" => return Ok(engine.behaviors().declaration(&text(arg(0)))),
        "events.read" => {
            let principal = who()?;
            let options = options(0);
            let member = |key: &str| options.and_then(|options| options.get(key));
            let after = match member("after") {
                None => None,
                Some(Value::String(head)) if head == "head" => Some(EventsAfter::Head),
                Some(given) => Some(EventsAfter::Cursor(
                    integer(given).ok_or_else(|| cursor_refusal(&js_string(given)))?,
                )),
            };
            let limit = match member("limit") {
                None => None,
                Some(given) => {
                    Some(integer(given).ok_or_else(|| page_size_refusal(&js_string(given)))?)
                }
            };
            let namespace = text_member(options, "namespace");
            let schema = text_member(options, "schema");
            let instance_id = text_member(options, "instanceId");
            // The filters are checked in the TypeScript engine's order,
            // after the cursor and the page size, before the schema.
            let [kinds, behaviors, exclude] =
                check_filters([member("kinds"), member("behaviors"), member("exclude")])?;
            to_json(engine.events().read(
                principal,
                &ReadEvents {
                    namespace: namespace.as_deref(),
                    schema: schema.as_deref(),
                    instance_id: instance_id.as_deref(),
                    after,
                    limit,
                    kinds,
                    behaviors,
                    exclude,
                },
            )?)
        }
        "events.head" => Ok(Value::from(engine.events().head()?)),
        "events.floor" => {
            let namespace = args.first().filter(|given| !given.is_null()).map(js_string);
            Ok(Value::from(engine.events().floor(namespace.as_deref())?))
        }
        "namespaces.create" => {
            let principal = who()?;
            let Value::String(name) = arg(0) else {
                check_principal(principal)?;
                return Err(name_refusal(&json::stringify(arg(0))).into());
            };
            to_json(engine.namespaces().create(principal, name)?)
        }
        "namespaces.archive" => to_json(engine.namespaces().archive(who()?, &text(arg(0)))?),
        "namespaces.unarchive" => to_json(engine.namespaces().unarchive(who()?, &text(arg(0)))?),
        "namespaces.get" => to_json(engine.namespaces().get(who()?, &text(arg(0)))?),
        "namespaces.list" => to_json(engine.namespaces().list(who()?)?),
        "namespaces.names" => to_json(engine.namespaces().names()?),
        "values.get" => {
            let namespace = text_member(options(1), "namespace");
            to_json(
                engine
                    .values()
                    .get(who()?, &text(arg(0)), namespace.as_deref())?,
            )
        }
        "values.sweep" => to_json(engine.values().sweep()?),
        "values.thresholdBytes" => Ok(Value::from(engine.values().threshold_bytes())),
        other => {
            return Err(Error::usage(format!(
                "the engine has no call {}",
                json::quote(other)
            )))
        }
    };
    Ok(Some(result?))
}

/// A principal a call passes as JSON, checked as the TypeScript engine's
/// `checkPrincipal` checks it: an object naming its subject, and a service
/// that is `{ deployable, serves, subject, standsIn }` and, standing in for
/// an end user, holds no permissions (D37).
pub fn principal_from_json(given: &Value) -> Result<Principal, EngineError> {
    let no_subject =
        || EngineError::invalid_argument("an engine call needs a principal with a subject");
    let Value::Object(members) = given else {
        return Err(no_subject());
    };
    match members.get("subject") {
        Some(Value::String(subject)) if !subject.is_empty() => {}
        _ => return Err(no_subject()),
    }
    if let Some(service) = members.get("service") {
        let empty = Map::new();
        let fields = service.as_object().unwrap_or(&empty);
        let deployable = fields
            .get("deployable")
            .and_then(Value::as_str)
            .unwrap_or_default();
        let serves = fields
            .get("serves")
            .and_then(Value::as_array)
            .is_some_and(|serves| serves.iter().all(Value::is_string));
        let well_formed = !deployable.is_empty()
            && serves
            && fields.get("subject").is_some_and(Value::is_string)
            && fields.get("standsIn").is_some_and(Value::is_boolean);
        if !well_formed {
            return Err(EngineError::invalid_argument(
                "a principal's service is { deployable, serves, subject, standsIn }",
            ));
        }
        let holds = members
            .get("permissions")
            .is_none_or(|permissions| permissions.as_array().is_none_or(|list| !list.is_empty()));
        if fields.get("standsIn") == Some(&Value::Bool(true)) && holds {
            return Err(EngineError::invalid_argument(format!(
                "service {deployable} stands in for an end user and holds no permissions (D37)"
            )));
        }
    }
    serde_json::from_value(given.clone()).map_err(|error| {
        EngineError::invalid_argument(format!(
            "a principal is {{ subject, permissions, claims?, service? }}: {error}"
        ))
    })
}

fn to_json(value: impl serde::Serialize) -> Result<Value> {
    serde_json::to_value(value)
        .map_err(|error| Error::internal(format!("a result does not write as JSON: {error}")))
}

// options_of is a call's options: an object, or none.
fn options_of(given: Option<&Value>) -> Option<&Map<String, Value>> {
    given.and_then(Value::as_object)
}

fn text_member(options: Option<&Map<String, Value>>, key: &str) -> Option<String> {
    options
        .and_then(|options| options.get(key))
        .filter(|value| !value.is_null())
        .map(js_string)
}

fn flag_member(options: Option<&Map<String, Value>>, key: &str) -> bool {
    options
        .and_then(|options| options.get(key))
        .is_some_and(|value| value == &Value::Bool(true))
}

// text is an argument the TypeScript engine reads as a string, as
// JavaScript's String() writes it.
fn text(value: &Value) -> String {
    js_string(value)
}

// integer is a JSON number that is an integer (Number.isInteger), within
// the range a JavaScript number holds exactly; None for anything else.
fn integer(value: &Value) -> Option<i64> {
    let Value::Number(number) = value else {
        return None;
    };
    let read = json::number_value(number);
    (read.is_finite() && read.fract() == 0.0 && read.abs() <= 9_007_199_254_740_991.0)
        .then_some(read as i64)
}

/// A JSON value as JavaScript's `String()` writes it: a string as itself,
/// a number as `Number#toString`, an array's items joined with commas, an
/// object as `[object Object]`.
pub fn js_string(value: &Value) -> String {
    match value {
        Value::Null => "null".to_owned(),
        Value::Bool(flag) => flag.to_string(),
        Value::Number(number) => json::js_number(json::number_value(number)),
        Value::String(text) => text.clone(),
        Value::Array(items) => items
            .iter()
            .map(|item| match item {
                Value::Null => String::new(),
                other => js_string(other),
            })
            .collect::<Vec<_>>()
            .join(","),
        Value::Object(_) => "[object Object]".to_owned(),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn values_are_named_as_javascript_names_them() {
        assert_eq!(js_string(&json!(1.5)), "1.5");
        assert_eq!(js_string(&json!(2.0)), "2");
        assert_eq!(js_string(&json!("tail")), "tail");
        assert_eq!(js_string(&json!(["a", 1, null])), "a,1,");
        assert_eq!(js_string(&json!({})), "[object Object]");
        assert_eq!(integer(&json!(3.0)), Some(3));
        assert_eq!(integer(&json!(1.5)), None);
        assert_eq!(integer(&json!("3")), None);
    }
}
