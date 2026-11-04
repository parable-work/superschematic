//! Constants, fields a create sets for good (D16), ported onto the pure core
//! (D103) from the engine's (`../behaviors/src/constants.rs`), with the
//! fields that move one way (D104). Work is routed by what a row is: a
//! step's kind, the key a blueprint stamped it with, the owner its create
//! named. When any writer can change them, a worker can turn the step it
//! holds into another one. The config lists the type's own fields, by
//! name, that keep the value their create gives them (`fields`), and the
//! ones that only move one way after it (`oneWay`).
//!
//! Constants is held to D103's test of a behavior and fails it: it keeps
//! no state of its own, and its rules hold the table's own fields, a field
//! kept as its create gave it or moved one way. It is ported because D104
//! orders it, with `oneWay`; whether a field's constancy and direction
//! become a declarative rule (D103, step 5, beside the fields a state
//! freezes) is step 5's question, which D104's status records.
//!
//! An update that changes a listed field is refused at the field, with the
//! rule `constant` (`invalid_instance`), unless the caller holds the
//! config's permission. A field the create leaves absent stays absent: its
//! absence is the value the create set, and setting it later is a change.
//! Absent and null are one value, none, since a merge patch removes a
//! member with null. A value is compared as JSON, so a change inside an
//! object or a list is a change. A create is never refused.
//!
//! A field that moves one way (D104) holds a boolean, a number or a
//! date-time: `up`, a boolean that only turns true, a number or a
//! date-time that only grows; `down`, one that only turns false or
//! shrinks. An update that moves one the other way is the veto `one_way`,
//! details `{ field, direction, current, attempted }` (null for no value,
//! and a date-time held as epoch milliseconds, a table's column, as the
//! ISO 8601 text `toISOString` writes), unless the caller holds the
//! config's permission; the first such field,
//! in the order the type lists its fields, is the one refused. A create
//! gives any value. No value is below every value: clearing a field is a
//! move down, refused on an up field, and giving a field with none a value
//! a move up, refused on a down field. It is the one order in which up
//! means "never lost once given", a requirement turned on stays on, and
//! down means "only ever less", and the absence a create left is the value
//! it set, as for a constant. An equal value is no move, and a value the
//! core cannot read as the field's type is no move it can order, and
//! passes. A boolean reads as JSON's, or 0 and 1 as an adapter may read
//! one; a number as JSON's, or a string that holds a decimal number, as an
//! adapter may read a BIGINT; a date-time as epoch milliseconds or ISO 8601
//! text as Timers reads it. The constant rule is a validation and the
//! direction a guard, so an update that changes a constant and moves a
//! one-way field back is `invalid_instance`, which runs first.
//!
//! The core has no write of own fields from an operation (the engine's
//! `update()`), so the update a Constants use holds is every own-field
//! write through the core: a caller's update, a repository's update of one
//! row by its key and an upsert's update of the row its key holds. A write
//! around the core (a bulk update, raw SQL) is held to none of it, the
//! escape hatch D103 keeps.
//!
//! The config is checked at define as the engine checked it: first as the
//! declaration's configSchema holds it, each issue at its path in the
//! schema loader's words ([`ConfigError::Schema`]), then each field held to
//! the type's own, in the engine's words ([`ConfigError::Rule`]): a field
//! in both `fields` and `oneWay`, and a `oneWay` field of another type, are
//! refused. The core runs no JSON Schema, so the schema's checks are
//! written here, held to the declaration file by this crate's tests.
//! Messages name the row `{type} {key}`, where the engine's named `{schema}
//! {id}`.
//!
//! configChange: any config may change, and the behavior can be added to
//! and removed from a type with rows: it keeps no state, and a stored row
//! satisfies any list, since the rules only compare an update with what the
//! row holds.

use std::cmp::Ordering;

use serde_json::{Map, Value};
use superschematic_rules::{
    Behavior, ConfigCx, ConfigError, ConfigTarget, Declaration, GuardRequest, Hook,
    ValidationIssue, ValidationRequest, Veto, View,
};

use crate::declared;
use crate::schema::{self, Issues};
use crate::values::{date_time_text, equal, one_way_order};

/// The behavior's name, as its declaration has it.
pub const NAME: &str = "Constants";

/// Constants' declaration, the file the compiler registers and the engine
/// embeds, read in place: what a host describes the behavior with.
pub const DECLARATION: &str = include_str!("../../behaviors/declarations/Constants.behavior.json");

/// The IR types a oneWay field may have that hold a boolean (D104).
pub const ONE_WAY_BOOLEANS: [&str; 1] = ["boolean"];

/// The IR types a oneWay field may have that hold a number (D104).
pub const ONE_WAY_NUMBERS: [&str; 3] = ["number", "Generic.Int64", "Generic.Float64"];

// The veto an update that moves a oneWay field the other way meets.
const ONE_WAY: &str = "one_way";

// The members the configSchema's closed object takes.
const CONFIG_KEYS: [&str; 3] = ["fields", "oneWay", "permission"];

// The bounds the configSchema holds fields and oneWay to.
const MAX_FIELDS: usize = 64;

/// The way a oneWay field moves.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum OneWayDirection {
    /// A boolean that only turns true, a number or a date-time that only
    /// grows.
    Up,
    /// A boolean that only turns false, a number or a date-time that only
    /// shrinks.
    Down,
}

impl OneWayDirection {
    /// The direction as a config writes it.
    pub fn as_str(self) -> &'static str {
        match self {
            OneWayDirection::Up => "up",
            OneWayDirection::Down => "down",
        }
    }

    /// The direction a config's text names; `None` for another text.
    pub fn parse(text: &str) -> Option<Self> {
        match text {
            "up" => Some(OneWayDirection::Up),
            "down" => Some(OneWayDirection::Down),
            _ => None,
        }
    }
}

/// What a oneWay field holds, which decides how its values are ordered.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum OneWayKind {
    Boolean,
    Number,
    DateTime,
}

/// One field that moves one way, with its direction and its kind.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ConstantsOneWay {
    pub field: String,
    pub direction: OneWayDirection,
    pub kind: OneWayKind,
}

/// Constants' config, parsed.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ConstantsConfig {
    /// The type's own fields, by name, that keep the value their create
    /// gives them.
    pub fields: Vec<String>,
    /// The permission a caller needs to change them after the create, or to
    /// move a oneWay field back; nobody may when `None`.
    pub permission: Option<String>,
    /// The fields that move one way, in the order the type lists its fields.
    pub one_way: Vec<ConstantsOneWay>,
}

impl ConstantsConfig {
    /// Parses and checks a config as a type gives it: as the declaration's
    /// configSchema holds it, each issue at its path in the schema loader's
    /// words, then each field held to the type's own, a rule's reason in the
    /// engine's words, which the core prefixes with the type and the
    /// behavior.
    pub fn parse(raw: &Value, target: &ConfigTarget<'_>) -> Result<ConstantsConfig, ConfigError> {
        let issues = config_issues(raw);
        if !issues.is_empty() {
            return Err(ConfigError::Schema(issues.into_issues()));
        }
        let type_name = target.type_name();
        let fields: Vec<String> = strings(raw.get("fields"));
        let names = || -> String {
            let names: Vec<&str> = target
                .fields()
                .iter()
                .map(|field| field.name.as_str())
                .collect();
            if names.is_empty() {
                "none".to_owned()
            } else {
                names.join(", ")
            }
        };
        for field in &fields {
            if target.fields().iter().all(|known| known.name != *field) {
                return Err(format!(
                    "fields: {field} is not a field of {type_name} (its fields: {})",
                    names()
                )
                .into());
            }
        }
        let empty = Map::new();
        let moves = raw
            .get("oneWay")
            .and_then(Value::as_object)
            .unwrap_or(&empty);
        let mut one_way: Vec<ConstantsOneWay> = Vec::new();
        for (field, direction) in moves {
            let Some(found) = target.fields().iter().find(|known| known.name == *field) else {
                return Err(format!(
                    "oneWay: {field} is not a field of {type_name} (its fields: {})",
                    names()
                )
                .into());
            };
            if fields.contains(field) {
                return Err(format!(
                    "oneWay: {field} is in fields, which keep the value their create gives them: a field moves one way or stays, not both"
                )
                .into());
            }
            let kind = if ONE_WAY_BOOLEANS.contains(&found.type_name.as_str()) {
                OneWayKind::Boolean
            } else if ONE_WAY_NUMBERS.contains(&found.type_name.as_str()) {
                OneWayKind::Number
            } else if found.date_time {
                OneWayKind::DateTime
            } else {
                return Err(format!(
                    "oneWay: {field} is a {} field of {type_name}: a field that moves one way holds a boolean, a number or a date-time",
                    found.type_name
                )
                .into());
            };
            one_way.push(ConstantsOneWay {
                field: field.clone(),
                direction: direction
                    .as_str()
                    .and_then(OneWayDirection::parse)
                    .unwrap_or(OneWayDirection::Up),
                kind,
            });
        }
        // In the order the type lists its fields, so the first refused is
        // the type's first, however the config orders its members.
        let order = |field: &str| {
            target
                .fields()
                .iter()
                .position(|known| known.name == field)
                .unwrap_or(usize::MAX)
        };
        one_way.sort_by_key(|moving| order(&moving.field));
        Ok(ConstantsConfig {
            fields,
            permission: raw
                .get("permission")
                .and_then(Value::as_str)
                .map(str::to_owned),
            one_way,
        })
    }
}

/// Constants: fields their create sets for good, and fields that move one
/// way.
#[derive(Clone, Copy, Debug, Default)]
pub struct Constants;

impl Behavior for Constants {
    type Config = ConstantsConfig;

    // The declaration file names the one_way veto (D104).
    fn declaration(&self) -> Declaration {
        declared::declaration(DECLARATION)
    }

    fn parse_config(
        &self,
        raw: &Value,
        target: &ConfigTarget<'_>,
    ) -> Result<ConstantsConfig, ConfigError> {
        ConstantsConfig::parse(raw, target)
    }

    // Any change: Constants keeps no state, and joins and leaves a type
    // with rows.
    fn config_change(
        &self,
        _cx: &ConfigCx<'_>,
        _before: Option<&ConstantsConfig>,
        _after: Option<&ConstantsConfig>,
    ) -> Hook<Option<String>> {
        Ok(None)
    }

    // validate refuses an update that changes a listed field, at the field,
    // unless the caller holds the config's permission. A create is never
    // refused.
    fn validate(
        &self,
        view: &View<'_, ConstantsConfig>,
        request: &ValidationRequest<'_>,
    ) -> Hook<Vec<ValidationIssue>> {
        let ValidationRequest::Update { before, after } = request else {
            return Ok(Vec::new());
        };
        let config = view.config();
        let changed: Vec<&String> = config
            .fields
            .iter()
            .filter(|field| !equal(before.get(field.as_str()), after.get(field.as_str())))
            .collect();
        if changed.is_empty() {
            return Ok(Vec::new());
        }
        if let Some(permission) = &config.permission {
            if view.can(permission)? {
                return Ok(Vec::new());
            }
        }
        let unless = config
            .permission
            .as_ref()
            .map_or(String::new(), |permission| {
                format!("; only a caller with {permission} may change it")
            });
        Ok(changed
            .into_iter()
            .map(|field| {
                ValidationIssue::new(
                    field.as_str(),
                    "constant",
                    format!(
                        "{field} is a constant of {}: its create sets it and nothing changes it after{unless}",
                        view.type_name()
                    ),
                )
            })
            .collect())
    }

    // guard vetoes an update that moves a oneWay field the other way, the
    // first in the type's order, unless the caller holds the config's
    // permission, asked only when one moved so (D104). The row before the
    // update is the view's.
    fn guard(
        &self,
        view: &View<'_, ConstantsConfig>,
        request: &GuardRequest<'_>,
    ) -> Hook<Option<Veto>> {
        let GuardRequest::Update { after, .. } = request else {
            return Ok(None);
        };
        let config = view.config();
        if config.one_way.is_empty() {
            return Ok(None);
        }
        let before = view.own();
        let back = config.one_way.iter().find(|moving| {
            let order = one_way_order(
                moving.kind,
                before.get(&moving.field),
                after.get(&moving.field),
            );
            matches!(
                (moving.direction, order),
                (OneWayDirection::Up, Some(Ordering::Greater))
                    | (OneWayDirection::Down, Some(Ordering::Less))
            )
        });
        let Some(moving) = back else {
            return Ok(None);
        };
        if let Some(permission) = &config.permission {
            if view.can(permission)? {
                return Ok(None);
            }
        }
        let current = shown(moving.kind, before.get(&moving.field));
        let attempted = shown(moving.kind, after.get(&moving.field));
        let verb = match (moving.kind, moving.direction) {
            (OneWayKind::Boolean, OneWayDirection::Up) => "turns true",
            (OneWayKind::Boolean, OneWayDirection::Down) => "turns false",
            (_, OneWayDirection::Up) => "grows",
            (_, OneWayDirection::Down) => "shrinks",
        };
        let unless = config
            .permission
            .as_ref()
            .map_or(String::new(), |permission| {
                format!("; only a caller with {permission} may move it back")
            });
        let mut details = Map::new();
        details.insert("field".into(), Value::from(moving.field.as_str()));
        details.insert("direction".into(), Value::from(moving.direction.as_str()));
        details.insert("current".into(), current.clone().unwrap_or(Value::Null));
        details.insert("attempted".into(), attempted.clone().unwrap_or(Value::Null));
        Ok(Some(
            Veto::new(format!(
                "{}'s {} only {verb}: an update may not move it from {} to {}{unless}",
                row(view),
                moving.field,
                text(current.as_ref()),
                text(attempted.as_ref())
            ))
            .with_code(ONE_WAY)
            .with_details(details),
        ))
    }
}

// shown is a value of a oneWay field as its refusal names it: none for an
// absent value and a null; a date-time a table's adapter gives as epoch
// milliseconds as ISO 8601 text, as JavaScript's toISOString writes it,
// the form the table's API types a date-time in, so a refusal names one
// date-time alike whether its row holds a number or text; any other value
// as it is.
fn shown(kind: OneWayKind, value: Option<&Value>) -> Option<Value> {
    let value = value.filter(|value| !value.is_null())?;
    if kind == OneWayKind::DateTime {
        if let Some(text) = value.as_i64().and_then(date_time_text) {
            return Some(Value::String(text));
        }
    }
    Some(value.clone())
}

// text writes a value as a refusal names it: its JSON, or none.
fn text(value: Option<&Value>) -> String {
    value.map_or_else(
        || "none".to_owned(),
        |value| serde_json::to_string(value).unwrap_or_default(),
    )
}

// row names the row a refusal is about: its type and its key.
fn row<C>(view: &View<'_, C>) -> String {
    match view.key() {
        Some(key) => format!("{} {key}", view.type_name()),
        None => view.type_name().to_owned(),
    }
}

// strings reads a list of strings the configSchema let through.
fn strings(value: Option<&Value>) -> Vec<String> {
    value
        .and_then(Value::as_array)
        .into_iter()
        .flatten()
        .filter_map(Value::as_str)
        .map(str::to_owned)
        .collect()
}

// config_issues lists what a config breaks of the declaration's
// configSchema, in the schema loader's words.
fn config_issues(raw: &Value) -> Issues {
    let mut issues = Issues::default();
    let Some(config) = raw.as_object() else {
        issues.push("", "type", "must be object");
        return issues;
    };
    for key in config
        .keys()
        .filter(|key| !CONFIG_KEYS.contains(&key.as_str()))
    {
        issues.push(
            "",
            "additionalProperties",
            format!("unknown key {}", schema::quoted(key)),
        );
    }
    match config.get("fields") {
        None => issues.push("", "required", "must have required property 'fields'"),
        Some(fields) => fields_issues(fields, &mut issues),
    }
    if let Some(permission) = config.get("permission") {
        schema::text_issues("/permission", permission, &mut issues);
    }
    if let Some(moves) = config.get("oneWay") {
        one_way_issues(moves, &mut issues);
    }
    issues
}

fn fields_issues(fields: &Value, issues: &mut Issues) {
    let at = "/fields";
    let Some(items) = fields.as_array() else {
        issues.push(at, "type", "must be array");
        return;
    };
    if items.is_empty() {
        issues.push(at, "minItems", "must NOT have fewer than 1 items");
    }
    if items.len() > MAX_FIELDS {
        issues.push(
            at,
            "maxItems",
            format!("must NOT have more than {MAX_FIELDS} items"),
        );
    }
    for (i, item) in items.iter().enumerate() {
        schema::text_issues(&format!("{at}/{i}"), item, issues);
    }
    if let Some((j, i)) = schema::duplicate(items) {
        issues.push(
            at,
            "uniqueItems",
            format!("must NOT have duplicate items (items ## {j} and {i} are identical)"),
        );
    }
}

fn one_way_issues(moves: &Value, issues: &mut Issues) {
    let at = "/oneWay";
    let Some(members) = moves.as_object() else {
        issues.push(at, "type", "must be object");
        return;
    };
    if members.is_empty() {
        issues.push(at, "minProperties", "must NOT have fewer than 1 properties");
    }
    if members.len() > MAX_FIELDS {
        issues.push(
            at,
            "maxProperties",
            format!("must NOT have more than {MAX_FIELDS} properties"),
        );
    }
    for (field, direction) in members {
        let known = direction
            .as_str()
            .and_then(OneWayDirection::parse)
            .is_some();
        if !known {
            issues.push(
                schema::member(at, field),
                "enum",
                format!(
                    "must be one of {}",
                    ["up", "down"].map(schema::quoted).join(", ")
                ),
            );
        }
    }
}
