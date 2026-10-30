//! A behavior's declaration, the port of `src/behaviors/declaration.ts`:
//! the JSON document the compiler registers (registry.BehaviorSpec, section
//! 3.16 of docs/extension-model.md) and an implementation carries, so the
//! engine and the compiler read one file. The engine checks the shape again
//! when an implementation registers, since the file it is given need not
//! be the one a binary embedded.
//!
//! An operation's paramsSchema sets `additionalProperties: false`, so its
//! parameters are exactly the ones it declares: a guard and the handler
//! read the same validated object. A createParamsSchema is an object
//! schema whose additionalProperties is false or a schema; a
//! preconditionSchema an object schema with `additionalProperties: false`.
//! Each veto code is lowercase snake case, listed once. A field may say it
//! is volatile (D56): it can move without an event of its own instance.
//! A volatile field may say it is per reader (D77): its value is the
//! caller's, so it is left out of every event and reference move, and
//! only a read computes it.
//! An instance operation that writes may say it is beside the instance
//! (D77): it writes only its behavior's own storage, so it leaves the
//! instance's seq alone, and with `"log": false` appends no event. A
//! behavior that declares one declares every field volatile.
//! An operation may say it runs on the namespace (D77, `"scope":
//! "namespace"`): on its behavior's tables across every schema of the
//! namespace that composes it, with no instance and no schema; it is
//! never beside an instance and always appends no event, so it sets
//! neither `beside` nor `log`. A beside operation with `"log": false` may
//! say `"asks": "read"`: it writes only the caller's own state, so the
//! policy is asked `read` for it, and an archived namespace lets it run.
//! A behavior may declare signals (D77): messages about an instance the
//! engine delivers to the event streams open on it and keeps nowhere, each
//! with a camelCase name unique among its signals, a JSON Schema of its
//! data, and who may send it: a caller who may read the schema (`read`),
//! one who may write it (`write`), or the behavior's own operations alone
//! (`operation`).
//! A declaration may name its value field (`value`) and give each field the
//! type of the column it takes on a table (`type`), which the pure core and
//! a build-time table read (D103) and the engine does not: it checks that
//! each is a string, the value one of the fields, and leaves both out of
//! the declaration's JSON it answers with ([`BehaviorDeclaration::json`]),
//! so its answers read as the recordings hold them.
//! The compiler's registry refuses the same declarations, with the same
//! wording.
//!
//! The engine adds one rule of its own: a name is `<extension>.<Name>`
//! with an extension name of a letter, then letters, digits, `_` and `-`,
//! or a bare `<Name>` for a core behavior, since the engine keys a
//! behavior's migrations by its name.

use serde_json::{Map, Value};

use crate::json;

/// A behavior name the engine takes, as a regular expression.
pub const BEHAVIOR_NAME: &str = "^(?:[A-Za-z][A-Za-z0-9_-]*\\.)?[A-Z][A-Za-z0-9]*$";

/// The operations the engine serves on a schema itself, which no behavior
/// may declare.
pub const BUILTIN_OPERATIONS: [&str; 6] = ["create", "get", "list", "update", "delete", "lookup"];

/// A veto code, as a regular expression: lowercase snake case, at most 64
/// characters.
pub const VETO_CODE: &str = "^[a-z][a-z0-9]*(?:_[a-z0-9]+)*$";

const DECLARATION_KEYS: [&str; 12] = [
    "name",
    "description",
    "configSchema",
    "createParamsSchema",
    "requires",
    "conflicts",
    "fields",
    "value",
    "operations",
    "signals",
    "preconditionSchema",
    "vetoes",
];
const SIGNAL_KEYS: [&str; 4] = ["name", "description", "paramsSchema", "access"];
const FIELD_KEYS: [&str; 5] = ["name", "description", "volatile", "perReader", "type"];
const VETO_KEYS: [&str; 2] = ["code", "description"];
const OPERATION_KEYS: [&str; 10] = [
    "name",
    "description",
    "paramsSchema",
    "resultSchema",
    "writes",
    "scope",
    "beside",
    "log",
    "asks",
    "invocationPolicy",
];

/// Whether a name is one a behavior may take ([`BEHAVIOR_NAME`]):
/// `Workflow`, `acme.Rating`.
pub fn is_behavior_name(name: &str) -> bool {
    let (namespace, local) = match name.split_once('.') {
        Some((namespace, local)) => (Some(namespace), local),
        None => (None, name),
    };
    let namespaced = namespace.is_none_or(|namespace| {
        let bytes = namespace.as_bytes();
        !bytes.is_empty()
            && bytes[0].is_ascii_alphabetic()
            && bytes
                .iter()
                .all(|b| b.is_ascii_alphanumeric() || *b == b'_' || *b == b'-')
    });
    let bytes = local.as_bytes();
    namespaced
        && !bytes.is_empty()
        && bytes[0].is_ascii_uppercase()
        && bytes.iter().all(u8::is_ascii_alphanumeric)
}

/// Whether a code is one a veto may carry ([`VETO_CODE`]).
pub fn is_veto_code(code: &str) -> bool {
    code.len() <= 64
        && code.split('_').enumerate().all(|(index, part)| {
            let bytes = part.as_bytes();
            !bytes.is_empty()
                && (index > 0 || bytes[0].is_ascii_lowercase())
                && bytes
                    .iter()
                    .all(|b| b.is_ascii_lowercase() || b.is_ascii_digit())
        })
}

// An operation's name: camelCase.
fn is_operation_name(name: &str) -> bool {
    let bytes = name.as_bytes();
    !bytes.is_empty()
        && bytes[0].is_ascii_lowercase()
        && bytes.iter().all(u8::is_ascii_alphanumeric)
}

// A field's name: an identifier.
fn is_field_name(name: &str) -> bool {
    let bytes = name.as_bytes();
    !bytes.is_empty()
        && (bytes[0].is_ascii_alphabetic() || bytes[0] == b'_')
        && bytes
            .iter()
            .all(|b| b.is_ascii_alphanumeric() || *b == b'_')
}

/// What an operation runs on.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub enum OperationScope {
    /// One instance a call names by id.
    Instance,
    /// The schema as a whole, with no instance.
    Schema,
    /// The behavior's tables across every schema of a namespace that
    /// composes it (D77), with no instance and no schema.
    Namespace,
}

impl OperationScope {
    pub fn as_str(self) -> &'static str {
        match self {
            OperationScope::Instance => "instance",
            OperationScope::Schema => "schema",
            OperationScope::Namespace => "namespace",
        }
    }

    /// The scope a declaration names; `None` for any other value.
    pub fn parse(scope: &str) -> Option<Self> {
        match scope {
            "instance" => Some(OperationScope::Instance),
            "schema" => Some(OperationScope::Schema),
            "namespace" => Some(OperationScope::Namespace),
            _ => None,
        }
    }
}

/// Who may send a signal (D77).
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub enum SignalAccess {
    /// A caller the policy lets read the schema.
    Read,
    /// A caller the policy lets write the schema.
    Write,
    /// The behavior's own operations alone, through their context.
    Operation,
}

impl SignalAccess {
    pub fn as_str(self) -> &'static str {
        match self {
            SignalAccess::Read => "read",
            SignalAccess::Write => "write",
            SignalAccess::Operation => "operation",
        }
    }

    fn parse(text: &str) -> Option<Self> {
        match text {
            "read" => Some(SignalAccess::Read),
            "write" => Some(SignalAccess::Write),
            "operation" => Some(SignalAccess::Operation),
            _ => None,
        }
    }
}

/// A behavior's declaration, checked: its JSON as given and what the
/// engine reads of it.
#[derive(Clone, Debug, PartialEq)]
pub struct BehaviorDeclaration {
    /// The declaration's JSON, as given, without the members only a table's
    /// layout reads (D103): `value` and each field's `type`.
    pub json: Value,
    /// Bare (`Workflow`) for a core behavior, `<extension>.<Name>` for an
    /// extension's.
    pub name: String,
    pub description: Option<String>,
    /// The JSON Schema of the config a type gives it; absent, it takes none.
    pub config_schema: Option<Value>,
    /// The JSON Schema of the parameters a create gives it.
    pub create_params_schema: Option<Value>,
    /// Behaviors a type that lists this one must also list.
    pub requires: Vec<String>,
    /// Behaviors a type that lists this one may not list.
    pub conflicts: Vec<String>,
    /// The fields it adds to the instance type, in order.
    pub fields: Vec<FieldDeclaration>,
    /// The operations it adds beside the built-in ones, in order.
    pub operations: Vec<OperationDeclaration>,
    /// The signals it declares (D77), in order.
    pub signals: Vec<SignalDeclaration>,
    /// The JSON Schema of the entry a caller sends for it in a write's
    /// preconditions.
    pub precondition_schema: Option<Value>,
    /// The codes its vetoes carry.
    pub vetoes: Vec<VetoDeclaration>,
}

/// One field a behavior adds.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct FieldDeclaration {
    pub name: String,
    pub description: Option<String>,
    /// Whether it can move without an event of its own instance: computed
    /// at a read from other instances, the clock or the config (D56).
    pub volatile: bool,
    /// Whether its value is the caller's, not the instance's (D77): a read
    /// computes it, and no event patch, create event or reference move
    /// carries it. Only a volatile field is.
    pub per_reader: bool,
}

/// One operation a behavior adds.
#[derive(Clone, Debug, PartialEq)]
pub struct OperationDeclaration {
    pub name: String,
    pub description: Option<String>,
    pub params_schema: Value,
    pub result_schema: Value,
    /// Whether it changes stored state.
    pub writes: bool,
    pub scope: OperationScope,
    /// Whether it runs beside the instance (D77): it writes only its
    /// behavior's own tables and columns, takes no sequence of the
    /// instance and runs no other behavior's guard.
    pub beside: bool,
    /// Whether it appends an event; false only for a beside operation that
    /// declares `"log": false`.
    pub log: bool,
    /// Whether the policy is asked `read` for it though it writes (D77,
    /// `"asks": "read"`): a beside operation with `"log": false` that
    /// writes only the caller's own state, a read marker say.
    pub asks_read: bool,
    /// The MCP invocation policy of its tool (D11); absent for the policy's
    /// default.
    pub invocation_policy: Option<String>,
}

/// One signal a behavior declares (D77).
#[derive(Clone, Debug, PartialEq)]
pub struct SignalDeclaration {
    pub name: String,
    pub description: Option<String>,
    /// The JSON Schema of its data.
    pub params_schema: Value,
    pub access: SignalAccess,
}

/// One code a behavior's vetoes carry.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct VetoDeclaration {
    pub code: String,
    pub description: Option<String>,
}

impl BehaviorDeclaration {
    /// Reads a declaration from its JSON text: every problem the engine
    /// finds with it, or the declaration.
    pub fn parse(text: &str) -> Result<Self, Vec<String>> {
        let value: Value = serde_json::from_str(text)
            .map_err(|error| vec![format!("a behavior declaration is JSON: {error}")])?;
        Self::from_json(value)
    }

    /// Reads a declaration from its JSON value: every problem
    /// ([`check_declaration`]), or the declaration.
    pub fn from_json(value: Value) -> Result<Self, Vec<String>> {
        let problems = check_declaration(&value);
        if !problems.is_empty() {
            return Err(problems);
        }
        let text = |object: &Value, key: &str| -> Option<String> {
            object.get(key).and_then(Value::as_str).map(str::to_owned)
        };
        let names = |key: &str| -> Vec<String> {
            value
                .get(key)
                .and_then(Value::as_array)
                .map(|list| {
                    list.iter()
                        .filter_map(Value::as_str)
                        .map(str::to_owned)
                        .collect()
                })
                .unwrap_or_default()
        };
        let list = |key: &str| -> Vec<Value> {
            value
                .get(key)
                .and_then(Value::as_array)
                .cloned()
                .unwrap_or_default()
        };
        Ok(BehaviorDeclaration {
            name: text(&value, "name").unwrap_or_default(),
            description: text(&value, "description"),
            config_schema: value.get("configSchema").cloned(),
            create_params_schema: value.get("createParamsSchema").cloned(),
            requires: names("requires"),
            conflicts: names("conflicts"),
            fields: list("fields")
                .iter()
                .map(|field| FieldDeclaration {
                    name: text(field, "name").unwrap_or_default(),
                    description: text(field, "description"),
                    volatile: field.get("volatile") == Some(&Value::Bool(true)),
                    per_reader: field.get("perReader") == Some(&Value::Bool(true)),
                })
                .collect(),
            operations: list("operations")
                .iter()
                .map(|operation| OperationDeclaration {
                    name: text(operation, "name").unwrap_or_default(),
                    description: text(operation, "description"),
                    params_schema: operation
                        .get("paramsSchema")
                        .cloned()
                        .unwrap_or(Value::Null),
                    result_schema: operation
                        .get("resultSchema")
                        .cloned()
                        .unwrap_or(Value::Null),
                    writes: operation.get("writes") == Some(&Value::Bool(true)),
                    scope: operation
                        .get("scope")
                        .and_then(Value::as_str)
                        .and_then(OperationScope::parse)
                        .unwrap_or(OperationScope::Instance),
                    beside: operation.get("beside") == Some(&Value::Bool(true)),
                    log: operation.get("log") != Some(&Value::Bool(false)),
                    asks_read: operation.get("asks") == Some(&Value::from("read")),
                    invocation_policy: text(operation, "invocationPolicy"),
                })
                .collect(),
            signals: list("signals")
                .iter()
                .map(|signal| SignalDeclaration {
                    name: text(signal, "name").unwrap_or_default(),
                    description: text(signal, "description"),
                    params_schema: signal.get("paramsSchema").cloned().unwrap_or(Value::Null),
                    access: signal
                        .get("access")
                        .and_then(Value::as_str)
                        .and_then(SignalAccess::parse)
                        .unwrap_or(SignalAccess::Operation),
                })
                .collect(),
            precondition_schema: value.get("preconditionSchema").cloned(),
            vetoes: list("vetoes")
                .iter()
                .map(|veto| VetoDeclaration {
                    code: text(veto, "code").unwrap_or_default(),
                    description: text(veto, "description"),
                })
                .collect(),
            json: without_layout_members(value),
        })
    }

    /// The operation of a name.
    pub fn operation(&self, name: &str) -> Option<&OperationDeclaration> {
        self.operations
            .iter()
            .find(|operation| operation.name == name)
    }

    /// The signal of a name.
    pub fn signal(&self, name: &str) -> Option<&SignalDeclaration> {
        self.signals.iter().find(|signal| signal.name == name)
    }
}

/// A declaration without the members only a table's layout reads (D103):
/// its `value` and each field's `type`, which the engine, keeping no
/// table, never answers with.
fn without_layout_members(mut value: Value) -> Value {
    if let Value::Object(members) = &mut value {
        members.remove("value");
        if let Some(Value::Array(fields)) = members.get_mut("fields") {
            for field in fields {
                if let Value::Object(field) = field {
                    field.remove("type");
                }
            }
        }
    }
    value
}

/// A value as `JSON.stringify` writes it in a message; `undefined` for an
/// absent one.
fn shown(value: Option<&Value>) -> String {
    value.map_or_else(|| "undefined".to_owned(), json::stringify)
}

/// Every problem with a declaration, in the TypeScript engine's order and
/// words; empty means the engine takes it. It does not compile the
/// schemas: the registry does.
pub fn check_declaration(value: &Value) -> Vec<String> {
    let Value::Object(members) = value else {
        return vec!["a behavior declaration is a JSON object".to_owned()];
    };
    let mut problems = Vec::new();
    unknown_keys(members, &DECLARATION_KEYS, "the declaration", &mut problems);
    let name = members.get("name");
    let name_text = name.and_then(Value::as_str);
    if !name_text.is_some_and(is_behavior_name) {
        problems.push(format!("name {} must match {BEHAVIOR_NAME}", shown(name)));
    }
    optional_string(members.get("description"), "description", &mut problems);
    if members
        .get("configSchema")
        .is_some_and(|schema| !is_schema(schema))
    {
        problems.push("configSchema is a JSON Schema: an object, or true or false".to_owned());
    }
    if let Some(schema) = members.get("createParamsSchema") {
        if let Some(problem) = create_params_schema_problem(schema) {
            problems.push(problem.to_owned());
        }
    }
    let requires = names(members.get("requires"), "requires", &mut problems);
    let conflicts = names(members.get("conflicts"), "conflicts", &mut problems);
    for other in requires.iter().chain(&conflicts) {
        if Some(other.as_str()) == name_text {
            problems.push(format!(
                "{} names the behavior itself",
                if requires.contains(other) {
                    "requires"
                } else {
                    "conflicts"
                }
            ));
        }
    }
    for other in &requires {
        if conflicts.contains(other) {
            problems.push(format!("it both requires and conflicts with {other}"));
        }
    }
    check_fields(members.get("fields"), &mut problems);
    check_value(members, &mut problems);
    check_operations(members.get("operations"), &mut problems);
    check_beside_fields(members, &mut problems);
    check_signals(members.get("signals"), &mut problems);
    if let Some(precondition) = members.get("preconditionSchema") {
        match precondition {
            Value::Object(schema) if schema.get("type") == Some(&Value::from("object")) => {
                if schema.get("additionalProperties") != Some(&Value::Bool(false)) {
                    problems.push("preconditionSchema must set \"additionalProperties\": false, so its members are exactly the ones it declares".to_owned());
                }
            }
            _ => problems.push(
                "preconditionSchema must be an object schema (\"type\": \"object\")".to_owned(),
            ),
        }
    }
    check_vetoes(members.get("vetoes"), &mut problems);
    problems
}

/// Why a schema cannot be a behavior's create parameters: it is not an
/// object schema whose additionalProperties is false or a schema. `None`
/// when it can. A declaration's createParamsSchema, and what an
/// implementation's createParamsSchema returns for a config, are held to
/// it.
pub fn create_params_schema_problem(schema: &Value) -> Option<&'static str> {
    let Value::Object(members) = schema else {
        return Some("createParamsSchema must be an object schema (\"type\": \"object\")");
    };
    if members.get("type") != Some(&Value::from("object")) {
        return Some("createParamsSchema must be an object schema (\"type\": \"object\")");
    }
    match members.get("additionalProperties") {
        Some(Value::Bool(false)) | Some(Value::Object(_)) => None,
        _ => Some("createParamsSchema must set \"additionalProperties\": false or a schema, so no create parameter goes unchecked"),
    }
}

fn check_vetoes(vetoes: Option<&Value>, problems: &mut Vec<String>) {
    let Some(vetoes) = vetoes else {
        return;
    };
    let Value::Array(vetoes) = vetoes else {
        problems.push("vetoes is a list".to_owned());
        return;
    };
    let mut seen: Vec<&str> = Vec::new();
    for (index, veto) in vetoes.iter().enumerate() {
        let at = format!("vetoes[{index}]");
        let Value::Object(members) = veto else {
            problems.push(format!("{at} is an object"));
            continue;
        };
        unknown_keys(members, &VETO_KEYS, &at, problems);
        optional_string(
            members.get("description"),
            &format!("{at}.description"),
            problems,
        );
        let code = members.get("code");
        let Some(text) = code
            .and_then(Value::as_str)
            .filter(|code| is_veto_code(code))
        else {
            problems.push(format!(
                "veto code {} is not lowercase snake case of at most 64 characters",
                shown(code)
            ));
            continue;
        };
        if seen.contains(&text) {
            problems.push(format!("veto code {text} is declared twice"));
        }
        seen.push(text);
    }
}

/// The value field, when the declaration names one, is one of its fields
/// (D103).
fn check_value(members: &Map<String, Value>, problems: &mut Vec<String>) {
    let Some(value) = members.get("value") else {
        return;
    };
    let Some(name) = value.as_str() else {
        problems.push("value is a string".to_owned());
        return;
    };
    let declared = members
        .get("fields")
        .and_then(Value::as_array)
        .into_iter()
        .flatten()
        .any(|field| field.get("name").and_then(Value::as_str) == Some(name));
    if !declared {
        problems.push(format!(
            "value {} is not one of its fields",
            json::quote(name)
        ));
    }
}

fn check_fields(fields: Option<&Value>, problems: &mut Vec<String>) {
    let Some(fields) = fields else {
        return;
    };
    let Value::Array(fields) = fields else {
        problems.push("fields is a list".to_owned());
        return;
    };
    let mut seen: Vec<&str> = Vec::new();
    for (index, field) in fields.iter().enumerate() {
        let at = format!("fields[{index}]");
        let Value::Object(members) = field else {
            problems.push(format!("{at} is an object"));
            continue;
        };
        unknown_keys(members, &FIELD_KEYS, &at, problems);
        optional_string(
            members.get("description"),
            &format!("{at}.description"),
            problems,
        );
        optional_string(members.get("type"), &format!("{at}.type"), problems);
        if members
            .get("volatile")
            .is_some_and(|volatile| !volatile.is_boolean())
        {
            problems.push(format!("{at}.volatile is a boolean"));
        }
        match members.get("perReader") {
            None => {}
            Some(Value::Bool(per_reader)) => {
                if *per_reader && members.get("volatile") != Some(&Value::Bool(true)) {
                    problems.push(format!(
                        "{at}.perReader needs \"volatile\": true: a value each reader reads its own of is computed at a read"
                    ));
                }
            }
            Some(_) => problems.push(format!("{at}.perReader is a boolean")),
        }
        let name = members.get("name");
        let Some(text) = name
            .and_then(Value::as_str)
            .filter(|name| is_field_name(name))
        else {
            problems.push(format!("{at}.name {} is not an identifier", shown(name)));
            continue;
        };
        if seen.contains(&text) {
            problems.push(format!("field {text} is declared twice"));
        }
        seen.push(text);
    }
}

fn check_operations(operations: Option<&Value>, problems: &mut Vec<String>) {
    let Some(operations) = operations else {
        return;
    };
    let Value::Array(operations) = operations else {
        problems.push("operations is a list".to_owned());
        return;
    };
    let mut seen: Vec<&str> = Vec::new();
    for (index, operation) in operations.iter().enumerate() {
        let mut at = format!("operations[{index}]");
        let Value::Object(members) = operation else {
            problems.push(format!("{at} is an object"));
            continue;
        };
        let name = members.get("name");
        match name
            .and_then(Value::as_str)
            .filter(|name| is_operation_name(name))
        {
            None => problems.push(format!("{at}.name {} is not camelCase", shown(name))),
            Some(text) => {
                at = format!("operation {text}");
                if BUILTIN_OPERATIONS.contains(&text) {
                    problems.push(format!(
                        "{at} has the name of an operation the engine serves on a schema ({})",
                        BUILTIN_OPERATIONS.join(", ")
                    ));
                }
                if seen.contains(&text) {
                    problems.push(format!("{at} is declared twice"));
                }
                seen.push(text);
            }
        }
        unknown_keys(members, &OPERATION_KEYS, &at, problems);
        optional_string(
            members.get("description"),
            &format!("{at} description"),
            problems,
        );
        optional_string(
            members.get("invocationPolicy"),
            &format!("{at} invocationPolicy"),
            problems,
        );
        if members
            .get("writes")
            .is_some_and(|writes| !writes.is_boolean())
        {
            problems.push(format!("{at} writes is a boolean"));
        }
        if let Some(scope) = members.get("scope") {
            if scope.as_str().and_then(OperationScope::parse).is_none() {
                problems.push(format!(
                    "{at} scope {} is not \"instance\", \"schema\" or \"namespace\"",
                    json::stringify(scope)
                ));
            }
        }
        if members.get("scope").and_then(Value::as_str) == Some("namespace") {
            check_namespace_operation(members, &at, problems);
        } else {
            check_beside(members, &at, problems);
        }
        check_asks(members, &at, problems);
        if !members.get("resultSchema").is_some_and(is_schema) {
            problems.push(format!("{at} has no resultSchema"));
        }
        match members.get("paramsSchema") {
            Some(Value::Object(params)) if params.get("type") == Some(&Value::from("object")) => {
                if params.get("additionalProperties") != Some(&Value::Bool(false)) {
                    problems.push(format!("{at} paramsSchema must set \"additionalProperties\": false, so its parameters are exactly the ones it declares"));
                }
            }
            _ => problems.push(format!(
                "{at} paramsSchema must be an object schema (\"type\": \"object\")"
            )),
        }
    }
}

// check_signals holds each signal to its rule (D77): a camelCase name,
// declared once, a JSON Schema of its data, and an access of read, write or
// operation.
fn check_signals(signals: Option<&Value>, problems: &mut Vec<String>) {
    let Some(signals) = signals else {
        return;
    };
    let Value::Array(signals) = signals else {
        problems.push("signals is a list".to_owned());
        return;
    };
    let mut seen: Vec<&str> = Vec::new();
    for (index, signal) in signals.iter().enumerate() {
        let mut at = format!("signals[{index}]");
        let Value::Object(members) = signal else {
            problems.push(format!("{at} is an object"));
            continue;
        };
        let name = members.get("name");
        match name
            .and_then(Value::as_str)
            .filter(|name| is_operation_name(name))
        {
            None => problems.push(format!("{at}.name {} is not camelCase", shown(name))),
            Some(text) => {
                at = format!("signal {text}");
                if seen.contains(&text) {
                    problems.push(format!("{at} is declared twice"));
                }
                seen.push(text);
            }
        }
        unknown_keys(members, &SIGNAL_KEYS, &at, problems);
        optional_string(
            members.get("description"),
            &format!("{at} description"),
            problems,
        );
        if !members.get("paramsSchema").is_some_and(is_schema) {
            problems.push(format!(
                "{at} has no paramsSchema: a JSON Schema of its data, an object, or true or false"
            ));
        }
        let access = members.get("access");
        if access
            .and_then(Value::as_str)
            .and_then(SignalAccess::parse)
            .is_none()
        {
            problems.push(format!(
                "{at} access {} is not \"read\", \"write\" or \"operation\"",
                shown(access)
            ));
        }
    }
}

// check_beside holds an operation's beside and log to their rule: each a
// boolean; a beside operation writes and runs on an instance; only a beside
// one appends no event.
fn check_beside(members: &Map<String, Value>, at: &str, problems: &mut Vec<String>) {
    for key in ["beside", "log"] {
        if members.get(key).is_some_and(|flag| !flag.is_boolean()) {
            problems.push(format!("{at} {key} is a boolean"));
        }
    }
    let beside = members.get("beside") == Some(&Value::Bool(true));
    if beside {
        if members.get("writes") != Some(&Value::Bool(true)) {
            problems.push(format!(
                "{at} is beside the instance and does not write: a beside operation declares \"writes\": true"
            ));
        }
        if members.get("scope").and_then(Value::as_str) == Some("schema") {
            problems.push(format!(
                "{at} is beside the instance and has scope \"schema\": a beside operation runs on an instance"
            ));
        }
    } else if members.get("log") == Some(&Value::Bool(false)) {
        problems.push(format!(
            "{at} sets \"log\": false and is not beside the instance: only a beside operation appends no event"
        ));
    }
}

// check_asks holds an operation's asks to its rule (D77): only "read", and
// only on a beside operation that appends no event, which writes nothing
// anyone but its caller reads.
fn check_asks(members: &Map<String, Value>, at: &str, problems: &mut Vec<String>) {
    let Some(asks) = members.get("asks") else {
        return;
    };
    if asks != &Value::from("read") {
        problems.push(format!(
            "{at} asks {}: the only value is \"read\"",
            json::stringify(asks)
        ));
        return;
    }
    if members.get("beside") != Some(&Value::Bool(true))
        || members.get("log") != Some(&Value::Bool(false))
    {
        problems.push(format!(
            "{at} asks \"read\" and is not beside the instance with \"log\": false: only an operation that writes its caller's own state, and appends no event, asks read"
        ));
    }
}

// check_namespace_operation holds a namespace operation to its rule (D77):
// it runs on no instance, so it is never beside one, and it appends no
// event, so it says nothing of one.
fn check_namespace_operation(members: &Map<String, Value>, at: &str, problems: &mut Vec<String>) {
    if members.get("beside") == Some(&Value::Bool(true)) {
        problems.push(format!(
            "{at} has scope \"namespace\" and is beside the instance: a namespace operation runs on no instance"
        ));
    }
    if members.contains_key("log") {
        problems.push(format!(
            "{at} has scope \"namespace\" and sets \"log\": a namespace operation appends no event"
        ));
    }
}

// check_beside_fields holds a behavior that declares a beside operation to
// volatile fields: a beside operation appends no move of a field, so every
// field the behavior declares is computed at the read.
fn check_beside_fields(members: &Map<String, Value>, problems: &mut Vec<String>) {
    let Some(Value::Array(operations)) = members.get("operations") else {
        return;
    };
    let Some(beside) = operations.iter().find_map(|operation| {
        (operation.get("beside") == Some(&Value::Bool(true)))
            .then(|| operation.get("name").and_then(Value::as_str))
            .flatten()
    }) else {
        return;
    };
    let Some(Value::Array(fields)) = members.get("fields") else {
        return;
    };
    for field in fields {
        if field.get("volatile") == Some(&Value::Bool(true)) {
            continue;
        }
        if let Some(name) = field.get("name").and_then(Value::as_str) {
            problems.push(format!(
                "field {name} is not volatile, and operation {beside} is beside the instance: a behavior with a beside operation declares every field volatile, since no event records its moves"
            ));
        }
    }
}

fn names(value: Option<&Value>, key: &str, problems: &mut Vec<String>) -> Vec<String> {
    let Some(value) = value else {
        return Vec::new();
    };
    match value {
        Value::Array(items) if items.iter().all(Value::is_string) => items
            .iter()
            .filter_map(Value::as_str)
            .map(str::to_owned)
            .collect(),
        _ => {
            problems.push(format!("{key} is a list of behavior names"));
            Vec::new()
        }
    }
}

fn unknown_keys(
    members: &Map<String, Value>,
    known: &[&str],
    at: &str,
    problems: &mut Vec<String>,
) {
    for key in json::js_keys(members) {
        if !known.contains(&key) {
            problems.push(format!("{at} has the unknown key {}", json::quote(key)));
        }
    }
}

fn optional_string(value: Option<&Value>, at: &str, problems: &mut Vec<String>) {
    if value.is_some_and(|value| !value.is_string()) {
        problems.push(format!("{at} is a string"));
    }
}

fn is_schema(value: &Value) -> bool {
    value.is_boolean() || value.is_object()
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn names_and_codes_are_checked() {
        for name in ["Workflow", "acme.Rating", "work-queue_2.Lease"] {
            assert!(is_behavior_name(name), "{name}");
        }
        for name in [
            "workflow",
            "acme.rating",
            ".Rating",
            "a.b.C",
            "1x.Rating",
            "",
        ] {
            assert!(!is_behavior_name(name), "{name}");
        }
        for code in ["stale", "not_allowed", "a1_b2"] {
            assert!(is_veto_code(code), "{code}");
        }
        for code in ["Stale", "_x", "x_", "x__y", "1x", ""] {
            assert!(!is_veto_code(code), "{code}");
        }
        assert!(!is_veto_code(&"x".repeat(65)));
    }

    /// A declaration may name its value field and type its fields for a
    /// table (D103): the engine checks both and answers with neither.
    #[test]
    fn the_layout_members_are_checked_and_left_out_of_its_json() {
        let declared = BehaviorDeclaration::from_json(json!({
            "name": "test.Gauge",
            "fields": [{"name": "level", "type": "number"}, {"name": "readAt", "description": "when", "type": "Temporal.DateTime"}],
            "value": "level"
        }))
        .expect("the declaration is sound");
        assert_eq!(
            declared.json,
            json!({"name": "test.Gauge", "fields": [{"name": "level"}, {"name": "readAt", "description": "when"}]})
        );
        assert_eq!(declared.fields.len(), 2);
        assert_eq!(
            check_declaration(&json!({
                "name": "test.Gauge",
                "fields": [{"name": "level", "type": 1}],
                "value": "reading"
            })),
            [
                "fields[0].type is a string",
                "value \"reading\" is not one of its fields"
            ]
        );
        assert_eq!(
            check_declaration(&json!({"name": "test.Gauge", "value": true})),
            ["value is a string"]
        );
        // The engine's Workflow reads its file, which names both.
        let workflow = BehaviorDeclaration::parse(include_str!(
            "../../../behaviors/declarations/Workflow.behavior.json"
        ))
        .expect("Workflow's declaration is sound");
        assert!(workflow.json.get("value").is_none());
        assert!(workflow.json["fields"][1].get("type").is_none());
    }

    #[test]
    fn every_problem_is_named_in_the_typescript_engines_words() {
        let problems = check_declaration(&json!({
            "name": "test.bad",
            "colour": 1,
            "requires": ["test.bad"],
            "conflicts": ["test.bad"],
            "fields": [{"name": "1x"}, {"name": "a", "volatile": "yes"}, {"name": "a"}, {"name": "b", "perReader": true}, {"name": "c", "volatile": true, "perReader": 1}],
            "operations": [
                {"name": "create", "paramsSchema": {"type": "object"}, "resultSchema": true, "scope": "row"},
                {"name": "Go", "paramsSchema": {}, "resultSchema": 3}
            ],
            "preconditionSchema": {"type": "object"},
            "vetoes": [{"code": "Bad"}, {"code": "ok"}, {"code": "ok"}]
        }));
        assert_eq!(
            problems,
            [
                "the declaration has the unknown key \"colour\"",
                "name \"test.bad\" must match ^(?:[A-Za-z][A-Za-z0-9_-]*\\.)?[A-Z][A-Za-z0-9]*$",
                "requires names the behavior itself",
                "requires names the behavior itself",
                "it both requires and conflicts with test.bad",
                "fields[0].name \"1x\" is not an identifier",
                "fields[1].volatile is a boolean",
                "field a is declared twice",
                "fields[3].perReader needs \"volatile\": true: a value each reader reads its own of is computed at a read",
                "fields[4].perReader is a boolean",
                "operation create has the name of an operation the engine serves on a schema (create, get, list, update, delete, lookup)",
                "operation create scope \"row\" is not \"instance\", \"schema\" or \"namespace\"",
                "operation create paramsSchema must set \"additionalProperties\": false, so its parameters are exactly the ones it declares",
                "operations[1].name \"Go\" is not camelCase",
                "operations[1] has no resultSchema",
                "operations[1] paramsSchema must be an object schema (\"type\": \"object\")",
                "preconditionSchema must set \"additionalProperties\": false, so its members are exactly the ones it declares",
                "veto code \"Bad\" is not lowercase snake case of at most 64 characters",
                "veto code ok is declared twice",
            ]
        );
        assert_eq!(
            check_declaration(&json!([])),
            ["a behavior declaration is a JSON object"]
        );
        assert_eq!(
            check_declaration(&json!({})),
            ["name undefined must match ^(?:[A-Za-z][A-Za-z0-9_-]*\\.)?[A-Z][A-Za-z0-9]*$"]
        );
    }

    #[test]
    fn a_beside_operation_writes_on_an_instance_beside_volatile_fields() {
        let none = json!({"type": "object", "additionalProperties": false});
        let problems = check_declaration(&json!({
            "name": "test.Chat",
            "fields": [{"name": "count", "volatile": true}, {"name": "last"}],
            "operations": [
                {"name": "post", "paramsSchema": none, "resultSchema": true, "writes": true, "beside": true},
                {"name": "peek", "paramsSchema": none, "resultSchema": true, "beside": true},
                {"name": "sweep", "paramsSchema": none, "resultSchema": true, "writes": true, "beside": true, "scope": "schema"},
                {"name": "mark", "paramsSchema": none, "resultSchema": true, "writes": true, "log": false},
                {"name": "odd", "paramsSchema": none, "resultSchema": true, "writes": true, "beside": 1, "log": "no"}
            ]
        }));
        assert_eq!(
            problems,
            [
                "operation peek is beside the instance and does not write: a beside operation declares \"writes\": true",
                "operation sweep is beside the instance and has scope \"schema\": a beside operation runs on an instance",
                "operation mark sets \"log\": false and is not beside the instance: only a beside operation appends no event",
                "operation odd beside is a boolean",
                "operation odd log is a boolean",
                "field last is not volatile, and operation post is beside the instance: a behavior with a beside operation declares every field volatile, since no event records its moves",
            ]
        );
        let declaration = BehaviorDeclaration::parse(
            r#"{"name": "test.Chat", "fields": [{"name": "count", "volatile": true}],
                "operations": [
                    {"name": "post", "paramsSchema": {"type": "object", "additionalProperties": false}, "resultSchema": true, "writes": true, "beside": true},
                    {"name": "markRead", "paramsSchema": {"type": "object", "additionalProperties": false}, "resultSchema": true, "writes": true, "beside": true, "log": false},
                    {"name": "rename", "paramsSchema": {"type": "object", "additionalProperties": false}, "resultSchema": true, "writes": true}
                ]}"#,
        )
        .unwrap();
        let flags = |name: &str| {
            let operation = declaration.operation(name).unwrap();
            (operation.beside, operation.log)
        };
        assert_eq!(flags("post"), (true, true));
        assert_eq!(flags("markRead"), (true, false));
        assert_eq!(flags("rename"), (false, true));
    }

    #[test]
    fn signals_are_named_once_with_a_schema_and_an_access() {
        let problems = check_declaration(&json!({
            "name": "test.Chat",
            "signals": [
                {"name": "typing", "paramsSchema": {"type": "object"}, "access": "read"},
                {"name": "typing", "paramsSchema": true, "access": "write"},
                {"name": "Draft", "paramsSchema": {}, "access": "operation"},
                {"name": "seen", "paramsSchema": 3, "access": "everyone", "colour": 1},
                "loud"
            ]
        }));
        assert_eq!(
            problems,
            [
                "signal typing is declared twice",
                "signals[2].name \"Draft\" is not camelCase",
                "signal seen has the unknown key \"colour\"",
                "signal seen has no paramsSchema: a JSON Schema of its data, an object, or true or false",
                "signal seen access \"everyone\" is not \"read\", \"write\" or \"operation\"",
                "signals[4] is an object",
            ]
        );
        assert_eq!(
            check_declaration(&json!({"name": "test.Chat", "signals": {}})),
            ["signals is a list"]
        );
        let declaration = BehaviorDeclaration::parse(
            r#"{"name": "test.Chat", "signals": [
                {"name": "typing", "description": "Who is typing.", "paramsSchema": {"type": "object"}, "access": "read"},
                {"name": "draft", "paramsSchema": true, "access": "operation"}
            ]}"#,
        )
        .unwrap();
        let typing = declaration.signal("typing").unwrap();
        assert_eq!(
            (typing.access, typing.description.as_deref()),
            (SignalAccess::Read, Some("Who is typing."))
        );
        assert_eq!(
            declaration.signal("draft").map(|signal| signal.access),
            Some(SignalAccess::Operation)
        );
        assert!(declaration.signal("viewing").is_none());
    }

    #[test]
    fn a_declaration_reads_what_the_engine_needs() {
        let declaration = BehaviorDeclaration::parse(
            r#"{"name": "test.Counter", "fields": [{"name": "count", "volatile": true}, {"name": "unseen", "volatile": true, "perReader": true}],
                "operations": [{"name": "count", "scope": "schema", "paramsSchema": {"type": "object", "additionalProperties": false}, "resultSchema": true, "writes": true}],
                "vetoes": [{"code": "stale"}]}"#,
        )
        .unwrap();
        assert_eq!(declaration.name, "test.Counter");
        assert!(declaration.fields[0].volatile);
        assert!(!declaration.fields[0].per_reader);
        assert!(declaration.fields[1].volatile && declaration.fields[1].per_reader);
        let operation = declaration.operation("count").unwrap();
        assert_eq!(
            (operation.scope, operation.writes),
            (OperationScope::Schema, true)
        );
        assert_eq!(declaration.vetoes[0].code, "stale");
        assert!(BehaviorDeclaration::parse("{").is_err());
    }
}
