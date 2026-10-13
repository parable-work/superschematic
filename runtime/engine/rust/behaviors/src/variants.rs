//! Variants, a field typed by another field's value (D16), the port of
//! `src/behaviors/core/variants.ts`. A step's result has a different shape
//! for each kind of step, and the engine refuses a union field, which the
//! schema runtime does not check. Without this the field is Generic.JSON,
//! and a result in the wrong shape is accepted without a word.
//!
//! The config names `field`, an own field of the type whose values are
//! open JSON objects (Generic.JSON, or a scalar whose values are JSON
//! objects), `by`, an own string or enum field, and `types`: by a value of
//! `by`, a type of the schema document besides the instance type. validate
//! holds `field`, when it holds a value, to the type `by`'s value picks, by
//! the rules the live version holds a field of that type to (`check_type`):
//! a JSON object, each field's value, and no key the type does not
//! declare, at any depth. While `by` holds a value `types` does not list,
//! or none, `field` holds none: a value there is refused with the rule
//! `variant`. So a value of `by` gains a type in a later version without a
//! stored instance breaking. Absent and null are one value, none, as a
//! merge patch has it.
//!
//! parse_config checks that `field` and `by` are distinct own fields of
//! those kinds, that each type is a type of the document besides the
//! instance type, and, when `by` is an enum, that each value is one of its
//! values. checked_types names the types, so the compatibility rule holds a
//! new version to each as to a type a field reaches. instance_schema writes
//! the rule as JSON Schema for the describe document and the create and
//! update tools: an if/then per listed value, and one more, which holds
//! `field` to null when `by` holds another value or none.
//!
//! configChange: while the schema has instances, `field` and `by` stay;
//! each value keeps its type, and a value may be added, since no stored
//! instance holds a value for it. With none, any change is allowed.
//! Removing the behavior is allowed, which leaves `field` open JSON again;
//! adding it to a schema with instances is not, since their values were
//! never checked.

use serde::Deserialize;
use serde_json::{json, Map, Value};

use superschematic_engine::behaviors::{ConfigChange, InstanceSchemaForm};
use superschematic_engine::errors::ValidationIssue;
use superschematic_engine::json::{js_keys, quote, stringify};
use superschematic_engine::{
    json_config, Behavior, ConfigTarget, Error, Hooks, Result, ValidationRequest,
};

/// Variants' config, parsed.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct VariantsConfig {
    /// The type's own field the variants type, by JSON key.
    pub field: String,
    /// The type's own string or enum field whose value picks the variant,
    /// by JSON key.
    pub by: String,
    /// By a value of `by`, the type of the schema document `field` holds
    /// while `by` holds it, in the order the config's object holds them.
    pub types: Vec<(String, String)>,
    /// Whether `field` is required, so that an instance always holds a
    /// value of it.
    pub required: bool,
}

impl VariantsConfig {
    /// The type a value of `by` picks; `None` for a value `types` does not
    /// list.
    pub fn type_of(&self, value: &str) -> Option<&str> {
        self.types
            .iter()
            .find(|(listed, _)| listed == value)
            .map(|(_, type_name)| type_name.as_str())
    }

    /// The values of `by` that pick a type, in order.
    pub fn values(&self) -> Vec<&str> {
        self.types.iter().map(|(value, _)| value.as_str()).collect()
    }
}

// What the configSchema lets through.
#[derive(Deserialize)]
struct RawConfig {
    field: String,
    by: String,
    types: Map<String, Value>,
}

// json_types reads the JSON types a field's JSON Schema takes, null aside.
fn json_types(schema: Option<&Value>) -> Vec<&str> {
    match schema.and_then(|schema| schema.get("type")) {
        Some(Value::String(one)) if one != "null" => vec![one.as_str()],
        Some(Value::Array(many)) => many
            .iter()
            .filter_map(Value::as_str)
            .filter(|one| *one != "null")
            .collect(),
        _ => Vec::new(),
    }
}

fn nullable(schema: Option<&Value>) -> bool {
    matches!(
        schema.and_then(|schema| schema.get("type")),
        Some(Value::Array(many)) if many.iter().any(|one| one == "null")
    )
}

// value_of is a field's value, None for none: absent and null are one.
fn value_of<'d>(data: &'d Map<String, Value>, field: &str) -> Option<&'d Value> {
    data.get(field).filter(|value| !value.is_null())
}

/// Variants: a field whose type another field's value picks.
#[derive(Clone, Copy, Debug, Default)]
pub struct Variants;

impl Behavior for Variants {
    type Config = VariantsConfig;

    fn declaration(&self) -> &str {
        include_str!("../declarations/Variants.behavior.json")
    }

    // The configSchema holds the shape; this holds the config to the type
    // and the document.
    fn parse_config(&self, raw: &Value, target: &ConfigTarget<'_>) -> Result<VariantsConfig> {
        let raw: RawConfig = json_config(raw)?;
        let type_name = target.type_name();
        for (role, name) in [("field", &raw.field), ("by", &raw.by)] {
            if !target.fields().contains(name) {
                return Err(Error::config(format!(
                    "{role}: {name} is not a field of {type_name} (its fields: {})",
                    target.fields().join(", ")
                )));
            }
        }
        if raw.field == raw.by {
            return Err(Error::config(format!(
                "field and by both name {}: a field cannot be typed by its own value",
                raw.field
            )));
        }
        let field_schema = target.field_schemas().get(&raw.field);
        let member = |schema: Option<&Value>, key: &str| -> Option<Value> {
            schema.and_then(|schema| schema.get(key)).cloned()
        };
        if !json_types(field_schema).contains(&"object")
            || member(field_schema, "items").is_some()
            || member(field_schema, "additionalProperties") == Some(Value::Bool(false))
        {
            return Err(Error::config(format!(
                "field: {type_name}.{} is not an open JSON object; Variants types a Generic.JSON field, or one of a scalar whose values are JSON objects",
                raw.field
            )));
        }
        let by_schema = target.field_schemas().get(&raw.by);
        if json_types(by_schema) != ["string"] || member(by_schema, "items").is_some() {
            return Err(Error::config(format!(
                "by: {type_name}.{} is not a string or enum field",
                raw.by
            )));
        }
        let values: Option<Vec<String>> = match member(by_schema, "enum") {
            Some(Value::Array(values)) => Some(
                values
                    .iter()
                    .filter_map(Value::as_str)
                    .map(str::to_owned)
                    .collect(),
            ),
            _ => None,
        };
        let names = target.types().names();
        let mut types = Vec::new();
        for value in js_keys(&raw.types) {
            let named = raw.types.get(value).and_then(Value::as_str).unwrap_or("");
            if !names.iter().any(|name| name == named) {
                return Err(Error::config(format!(
                    "types: {} names {named}, which is not a type of the schema document besides {type_name} (its types: {})",
                    quote(value),
                    if names.is_empty() {
                        "none".to_owned()
                    } else {
                        names.join(", ")
                    }
                )));
            }
            if let Some(values) = &values {
                if !values.iter().any(|known| known == value) {
                    return Err(Error::config(format!(
                        "types: {} is not a value of {} (its values: {})",
                        quote(value),
                        raw.by,
                        values.join(", ")
                    )));
                }
            }
            types.push((value.to_owned(), named.to_owned()));
        }
        Ok(VariantsConfig {
            field: raw.field,
            by: raw.by,
            types,
            required: !nullable(field_schema),
        })
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            config_change: Some(config_change),
            guidance: Some(|_, config, target| crate::guidance::variants::guidance(config, target)),
            checked_types: Some(|_, config| {
                let mut out: Vec<String> = Vec::new();
                for (_, type_name) in &config.types {
                    if !out.contains(type_name) {
                        out.push(type_name.clone());
                    }
                }
                out
            }),
            validate: Some(|_, cx, request| {
                let config = cx.config();
                let data = match request {
                    ValidationRequest::Create { data } => *data,
                    ValidationRequest::Update { after, .. } => *after,
                };
                let Some(value) = value_of(data, &config.field) else {
                    return Ok(Vec::new());
                };
                let key = value_of(data, &config.by);
                if let Some(type_name) = key
                    .and_then(Value::as_str)
                    .and_then(|key| config.type_of(key))
                {
                    return cx.check_type(type_name, value, &config.field);
                }
                let holds = key.map_or("holds none".to_owned(), |key| {
                    format!("is {}", stringify(key))
                });
                let listed: Vec<String> = config.values().into_iter().map(quote).collect();
                Ok(vec![ValidationIssue::new(
                    config.field.as_str(),
                    "variant",
                    format!(
                        "{field} holds a value while {by} {holds}; {field} has a type only while {by} is {}, and no value otherwise",
                        listed.join(", "),
                        field = config.field,
                        by = config.by
                    ),
                )])
            }),
            instance_schema: Some(|_, config, form, type_schema| {
                let VariantsConfig {
                    field,
                    by,
                    required,
                    ..
                } = config;
                let listed = config.values();
                let mut rules: Vec<Value> = config
                    .types
                    .iter()
                    .map(|(value, type_name)| {
                        json!({
                            "description": format!("While {by} is {}, {field} is a {type_name}", quote(value)),
                            "if": {"properties": {by.as_str(): {"const": value}}, "required": [by]},
                            "then": {"properties": {field.as_str(): type_schema(type_name, !required)}},
                        })
                    })
                    .collect();
                rules.push(json!({
                    "description": format!("While {by} holds another value or none, {field} holds none"),
                    "if": match form {
                        InstanceSchemaForm::Instance => json!({"not": {"properties": {by.as_str(): {"enum": listed}}, "required": [by]}}),
                        InstanceSchemaForm::Patch => json!({"properties": {by.as_str(): {"not": {"enum": listed}}}, "required": [by]}),
                    },
                    "then": {"properties": {field.as_str(): {"type": "null"}}},
                }));
                rules
            }),
            ..Hooks::default()
        }
    }
}

fn config_change(
    _: &Variants,
    before: Option<&VariantsConfig>,
    after: Option<&VariantsConfig>,
    change: &ConfigChange<'_>,
) -> Result<Option<String>> {
    let Some(before) = before else {
        return Ok(Some(format!(
            "the instances already hold values of {} that no type checked",
            after.map_or("", |after| after.field.as_str())
        )));
    };
    let Some(after) = after else {
        return Ok(None);
    };
    // No instance holds a value a type checked.
    if !change.instances()? {
        return Ok(None);
    }
    if before.field != after.field || before.by != after.by {
        return Ok(Some(format!(
            "field and by stay {} and {}: the instances hold values of {} typed by {}",
            before.field, before.by, before.field, before.by
        )));
    }
    for (value, type_name) in &before.types {
        match after.type_of(value) {
            None => {
                return Ok(Some(format!(
                    "types keeps {}: an instance whose {} is {} may hold a {type_name}, which a version without it refuses",
                    quote(value),
                    before.by,
                    quote(value)
                )))
            }
            Some(other) if other != type_name => {
                return Ok(Some(format!(
                    "types: {} stays {type_name}: an instance's {} was checked against it, not against {other}",
                    quote(value),
                    before.field
                )))
            }
            Some(_) => {}
        }
    }
    Ok(None)
}
