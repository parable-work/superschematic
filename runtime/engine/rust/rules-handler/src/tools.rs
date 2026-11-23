//! What development mode serves about its types besides their rows (D58's
//! tools, D117): the list of live types, and the tools an MCP endpoint
//! offers a caller, made as the engine makes its own. A tool is one
//! request: create, get, list, update and delete of each live type, each
//! operation its uses declare (of a row, or of the table as a whole), and
//! the types' own: `list_schemas`, `describe_schema`, `define_schema` and
//! `publish_schema`. Development mode is where a schema is worked out, so
//! unlike the engine it offers the publish.
//!
//! A tool's handle follows the SDK generators: the type's name in snake
//! case, then the method's (`order_create`, `task_add_blocker`); an
//! operation two uses of a type declare is qualified with the use
//! (`task_review_approve`). A tool the access policy refuses the caller is
//! left out of the caller's list; called by its handle, it is refused as
//! its request is.
//!
//! Each tool comes with its call (`{"kind", "type", "use", "operation"}`),
//! which [`request_of`] turns into the request a call with arguments makes.

use serde_json::{json, Map, Value};
use superschematic_rules::{Action as CoreAction, Refusal};

use crate::access::{Action, Question};
use crate::document::{Kind, Model};
use crate::handler::{done, take, Context, Handler, HandlerStep, Reads, Request};

/// The JSON Schema dialect a tool's input schema names.
const TOOL_SCHEMA: &str = "https://json-schema.org/draft/2020-12/schema";

/// A name in snake case: `TaskList` is `task_list`, `addBlocker`
/// `add_blocker`, each run of other characters one `_`.
pub fn snake_case(name: &str) -> String {
    let mut out = String::new();
    let mut previous_lower = false;
    for c in name.chars() {
        if c.is_ascii_alphanumeric() {
            if c.is_ascii_uppercase() && previous_lower && !out.ends_with('_') {
                out.push('_');
            }
            previous_lower = c.is_ascii_lowercase() || c.is_ascii_digit();
            out.push(c.to_ascii_lowercase());
        } else {
            if !out.is_empty() && !out.ends_with('_') {
                out.push('_');
            }
            previous_lower = false;
        }
    }
    out.trim_end_matches('_').to_owned()
}

fn json_type(kind: &Kind) -> Value {
    match kind {
        Kind::Text | Kind::Uuid | Kind::Date => json!({"type": "string"}),
        Kind::DateTime => json!({"type": "string", "format": "date-time"}),
        Kind::Number => json!({"type": "number"}),
        Kind::Integer => json!({"type": "integer"}),
        Kind::Bool => json!({"type": "boolean"}),
        Kind::Enum(values) => json!({"type": "string", "enum": values}),
        Kind::Object(_) => json!({"type": "object"}),
        Kind::Json => json!({}),
    }
}

/// The JSON Schema of a type's own fields as a write gives them: each
/// field's type, a list's items, and, for a create, the required ones.
fn fields_schema(model: &Model, create: bool) -> Value {
    let mut properties = Map::new();
    let mut required = Vec::new();
    let mut key = json_type(&model.key.kind);
    if let Some(entry) = key.as_object_mut() {
        entry.insert(
            "description".into(),
            Value::from(format!("The row's key, {}", model.key.name)),
        );
    }
    if create {
        properties.insert(model.key.name.clone(), key);
        if !model.key.auto {
            required.push(Value::from(model.key.name.as_str()));
        }
    }
    for field in &model.fields {
        let mut schema = json_type(&field.kind);
        if field.list {
            schema = json!({"type": "array", "items": schema});
        }
        if !field.description.is_empty() {
            if let Some(entry) = schema.as_object_mut() {
                entry.insert(
                    "description".into(),
                    Value::from(field.description.as_str()),
                );
            }
        }
        if create && field.required && field.default.is_none() {
            required.push(Value::from(field.name.as_str()));
        }
        properties.insert(field.name.clone(), schema);
    }
    let mut schema = json!({"type": "object", "properties": properties});
    if !required.is_empty() {
        schema["required"] = Value::Array(required);
    }
    schema
}

fn input(properties: Value, required: &[&str]) -> Value {
    let mut schema = json!({
        "$schema": TOOL_SCHEMA,
        "type": "object",
        "properties": properties,
        "additionalProperties": false,
    });
    if !required.is_empty() {
        schema["required"] = json!(required);
    }
    schema
}

fn tool(
    name: String,
    title: String,
    description: String,
    input_schema: Value,
    read_only: bool,
    call: Value,
) -> Value {
    json!({
        "tool": {
            "name": name,
            "title": title,
            "description": description,
            "inputSchema": input_schema,
            "annotations": {"readOnlyHint": read_only, "destructiveHint": call["kind"] == "delete"},
        },
        "call": call,
    })
}

impl Handler<'_> {
    /// A use for an operation named without one: the one use of the type
    /// whose declaration declares the operation, of a row or of the table
    /// as the action is.
    pub(crate) fn resolve_use(
        &self,
        model: &Model,
        action: CoreAction,
    ) -> Result<CoreAction, Refusal> {
        let (table, operation) = match &action {
            CoreAction::Operation {
                use_id, operation, ..
            } if use_id.is_empty() => (false, operation.clone()),
            CoreAction::TableOperation {
                use_id, operation, ..
            } if use_id.is_empty() => (true, operation.clone()),
            _ => return Ok(action),
        };
        let declaring: Vec<&str> = model
            .uses
            .iter()
            .filter(|used| {
                self.declarations()
                    .get(&used.spec.behavior)
                    .is_some_and(|declared| {
                        declared
                            .operations
                            .iter()
                            .any(|op| op.name == operation && op.table == table)
                    })
            })
            .map(crate::document::Use::id)
            .collect();
        let found = match declaring.as_slice() {
            [one] => (*one).to_owned(),
            [] => {
                return Err(Refusal::not_found(format!(
                    "{} has no operation {operation}{}",
                    model.name,
                    if table { " of its table" } else { "" }
                )))
            }
            many => {
                return Err(Refusal::invalid_argument(format!(
                    "{} has {operation} on uses {}; name the use as <use>.{operation}",
                    model.name,
                    many.join(", ")
                )))
            }
        };
        Ok(match action {
            CoreAction::Operation {
                key,
                operation,
                params,
                ..
            } => CoreAction::Operation {
                key,
                use_id: found,
                operation,
                params,
            },
            CoreAction::TableOperation {
                operation, params, ..
            } => CoreAction::TableOperation {
                use_id: found,
                operation,
                params,
            },
            other => other,
        })
    }

    fn may(
        &self,
        context: &Context,
        action: Action,
        schema: &str,
        operation: Option<&str>,
    ) -> bool {
        self.policy().allows(
            &Question {
                caller: &context.caller,
                action,
                schema,
                operation,
            },
            self.core().matcher(),
        )
    }

    pub(crate) fn types(
        &self,
        context: &Context,
        reads: &mut Reads<'_>,
    ) -> Result<HandlerStep, Refusal> {
        let Some(live) = self.live(reads)? else {
            return Ok(take(reads).wait());
        };
        let types: Vec<Value> = live
            .models
            .keys()
            .filter(|name| self.may(context, Action::Read, name, None))
            .map(|name| {
                json!({
                    "namespace": crate::access::NAMESPACE,
                    "name": name,
                    "liveVersion": live.versions.get(name).copied().unwrap_or_default(),
                })
            })
            .collect();
        Ok(done(Value::Array(types), Vec::new()))
    }

    pub(crate) fn tools(
        &self,
        context: &Context,
        reads: &mut Reads<'_>,
    ) -> Result<HandlerStep, Refusal> {
        let Some(live) = self.live(reads)? else {
            return Ok(take(reads).wait());
        };
        let mut tools = vec![
            tool(
                "list_schemas".into(),
                "List schemas".into(),
                "The types with a live version, each with its version.".into(),
                input(json!({}), &[]),
                true,
                json!({"kind": "types"}),
            ),
            tool(
                "describe_schema".into(),
                "Describe schema".into(),
                "A type's live version: its key, fields with their filters, owned and computed fields, and each use with its operations.".into(),
                input(json!({"type": {"type": "string"}}), &["type"]),
                true,
                json!({"kind": "describe"}),
            ),
            tool(
                "define_schema".into(),
                "Define schema".into(),
                "Keeps a schema document as its type's draft; publish_schema makes it live.".into(),
                input(json!({"document": {"type": "object"}}), &["document"]),
                false,
                json!({"kind": "define"}),
            ),
            tool(
                "publish_schema".into(),
                "Publish schema".into(),
                "Makes a type's draft its next version. With no compatibility rule: stored rows the version no longer describes are refused unless discard deletes them.".into(),
                input(
                    json!({"type": {"type": "string"}, "discard": {"type": "boolean"}}),
                    &["type"],
                ),
                false,
                json!({"kind": "publish"}),
            ),
        ];
        let id = json!({"type": "string", "description": "The row's key"});
        for (name, model) in &live.models {
            let snake = snake_case(name);
            let call = |kind: &str| json!({"kind": kind, "type": name});
            if self.may(context, Action::Write, name, None) {
                tools.push(tool(
                    format!("{snake}_create"),
                    format!("Create {name}"),
                    format!("Creates a {name}: its own fields, and each use's create parameters by its id."),
                    input(
                        json!({"data": fields_schema(model, true), "behaviors": {"type": "object"}}),
                        &["data"],
                    ),
                    false,
                    call("create"),
                ));
            }
            if self.may(context, Action::Read, name, None) {
                tools.push(tool(
                    format!("{snake}_get"),
                    format!("Get {name}"),
                    format!("Reads one {name} by its key, with its owned and computed fields."),
                    input(json!({"id": id}), &["id"]),
                    true,
                    call("get"),
                ));
                tools.push(tool(
                    format!("{snake}_list"),
                    format!("List {name}"),
                    format!("A page of {name} rows a filter keeps, in a sort's order: describe_schema gives each field's filters."),
                    input(
                        json!({
                            "filter": {"type": "object"},
                            "sort": {"type": "array", "items": {"type": "object"}},
                            "limit": {"type": "integer", "minimum": 1, "maximum": crate::handler::MAX_LIMIT},
                            "cursor": {"type": "string"},
                        }),
                        &[],
                    ),
                    true,
                    call("list"),
                ));
            }
            if self.may(context, Action::Write, name, None) {
                tools.push(tool(
                    format!("{snake}_update"),
                    format!("Update {name}"),
                    format!("A merge patch of a {name}'s own fields."),
                    input(
                        json!({"id": id, "patch": fields_schema(model, false)}),
                        &["id", "patch"],
                    ),
                    false,
                    call("update"),
                ));
                tools.push(tool(
                    format!("{snake}_delete"),
                    format!("Delete {name}"),
                    format!("Deletes a {name}, and what its relations' deletes reach."),
                    input(json!({"id": id}), &["id"]),
                    false,
                    call("delete"),
                ));
            }
            let mut declared_ops: Vec<(&str, &str, &crate::declarations::DeclaredOperation)> =
                Vec::new();
            for used in &model.uses {
                let Some(declared) = self.declarations().get(&used.spec.behavior) else {
                    continue;
                };
                for operation in &declared.operations {
                    declared_ops.push((used.id(), &used.spec.behavior, operation));
                }
            }
            for (use_id, behavior, operation) in &declared_ops {
                let action = if operation.writes {
                    Action::Write
                } else {
                    Action::Read
                };
                if !self.may(context, action, name, Some(&operation.name)) {
                    continue;
                }
                let shared = declared_ops
                    .iter()
                    .filter(|(_, _, other)| other.name == operation.name)
                    .count()
                    > 1;
                let handle = if shared {
                    format!(
                        "{snake}_{}_{}",
                        snake_case(use_id),
                        snake_case(&operation.name)
                    )
                } else {
                    format!("{snake}_{}", snake_case(&operation.name))
                };
                let params = if operation.params.is_object() {
                    operation.params.clone()
                } else {
                    json!({"type": "object"})
                };
                let (properties, required): (Value, &[&str]) = if operation.table {
                    (json!({"params": params}), &[])
                } else {
                    (json!({"id": id, "params": params}), &["id"])
                };
                let description = if operation.description.is_empty() {
                    format!("{behavior}'s {} on {name}.", operation.name)
                } else {
                    operation.description.clone()
                };
                tools.push(tool(
                    handle,
                    format!("{name}: {}", operation.name),
                    description,
                    input(properties, required),
                    !operation.writes,
                    json!({
                        "kind": if operation.table { "tableOperation" } else { "operation" },
                        "type": name,
                        "use": use_id,
                        "operation": operation.name,
                    }),
                ));
            }
        }
        Ok(done(Value::Array(tools), Vec::new()))
    }
}

fn argument<'a>(arguments: &'a Map<String, Value>, name: &str) -> Option<&'a Value> {
    arguments.get(name).filter(|value| !value.is_null())
}

fn text_argument(arguments: &Map<String, Value>, name: &str) -> Result<String, Refusal> {
    match argument(arguments, name) {
        Some(Value::String(text)) => Ok(text.clone()),
        Some(_) => Err(Refusal::invalid_argument(format!("{name} is a string"))),
        None => Err(Refusal::invalid_argument(format!("{name} is required"))),
    }
}

fn object_argument(
    arguments: &Map<String, Value>,
    name: &str,
) -> Result<Map<String, Value>, Refusal> {
    match argument(arguments, name) {
        Some(Value::Object(object)) => Ok(object.clone()),
        Some(_) => Err(Refusal::invalid_argument(format!("{name} is an object"))),
        None => Ok(Map::new()),
    }
}

/// The request a call of a tool makes with its arguments, from the call
/// [`Request::Tools`] gave the tool.
pub fn request_of(call: &Value, arguments: &Map<String, Value>) -> Result<Request, Refusal> {
    let kind = call.get("kind").and_then(Value::as_str).unwrap_or_default();
    let type_of = || {
        call.get("type")
            .and_then(Value::as_str)
            .map(str::to_owned)
            .map_or_else(|| text_argument(arguments, "type"), Ok)
    };
    let member = |name: &str| {
        call.get(name)
            .and_then(Value::as_str)
            .unwrap_or_default()
            .to_owned()
    };
    Ok(match kind {
        "types" => Request::Types,
        "describe" => Request::Describe {
            type_name: type_of()?,
        },
        "define" => Request::Define {
            document: argument(arguments, "document")
                .cloned()
                .ok_or_else(|| Refusal::invalid_argument("document is required"))?,
        },
        "publish" => Request::Publish {
            type_name: type_of()?,
            discard: argument(arguments, "discard").and_then(Value::as_bool) == Some(true),
        },
        "create" => Request::Create {
            type_name: type_of()?,
            data: object_argument(arguments, "data")?,
            params: object_argument(arguments, "behaviors")?,
        },
        "get" => Request::Get {
            type_name: type_of()?,
            id: text_argument(arguments, "id")?,
        },
        "list" => Request::List {
            type_name: type_of()?,
            filter: argument(arguments, "filter")
                .cloned()
                .unwrap_or(Value::Null),
            sort: match argument(arguments, "sort") {
                Some(sort) => serde_json::from_value(sort.clone()).map_err(|error| {
                    Refusal::invalid_argument(format!("sort is a list of fields: {error}"))
                })?,
                None => Vec::new(),
            },
            limit: match argument(arguments, "limit") {
                Some(limit) => Some(
                    limit
                        .as_u64()
                        .and_then(|limit| usize::try_from(limit).ok())
                        .ok_or_else(|| Refusal::invalid_argument("limit is a whole number"))?,
                ),
                None => None,
            },
            cursor: match argument(arguments, "cursor") {
                Some(_) => Some(text_argument(arguments, "cursor")?),
                None => None,
            },
        },
        "update" => Request::Update {
            type_name: type_of()?,
            id: text_argument(arguments, "id")?,
            patch: object_argument(arguments, "patch")?,
            preconditions: Map::new(),
        },
        "delete" => Request::Delete {
            type_name: type_of()?,
            id: text_argument(arguments, "id")?,
            preconditions: Map::new(),
        },
        "operation" => Request::Operation {
            type_name: type_of()?,
            id: text_argument(arguments, "id")?,
            use_id: member("use"),
            operation: member("operation"),
            params: argument(arguments, "params")
                .cloned()
                .unwrap_or(Value::Null),
            preconditions: Map::new(),
        },
        "tableOperation" => Request::TableOperation {
            type_name: type_of()?,
            use_id: member("use"),
            operation: member("operation"),
            params: argument(arguments, "params")
                .cloned()
                .unwrap_or(Value::Null),
        },
        other => {
            return Err(Refusal::internal(format!(
                "a tool's call names no request of kind {other}"
            )))
        }
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_handle_is_snake_case() {
        assert_eq!(snake_case("Order"), "order");
        assert_eq!(snake_case("TaskList"), "task_list");
        assert_eq!(snake_case("addBlocker"), "add_blocker");
        assert_eq!(snake_case("test.Gate"), "test_gate");
        assert_eq!(snake_case("v2Items"), "v2_items");
    }
}
