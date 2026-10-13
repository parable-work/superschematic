//! StateRules, the own fields a state of the type's Workflow requires or
//! freezes (D56), the port of `src/behaviors/core/staterules.ts`. A
//! record's fields mean more as it moves on: an invoice is drafted with
//! any amount, sent only with a customer and an amount that then stay,
//! and paid with nothing left to change. The config gives, by state of the
//! type's Workflow, the type's own top-level fields, by JSON key, an
//! instance must hold to enter the state and keeps while it is in it
//! (`required`), and the ones that do not change while it is in it
//! (`frozen`: a list, or `"all"` for every own field). A field is held
//! when it is neither absent nor null, since a merge patch removes a member
//! with null: `""` and `[]` are held.
//!
//! validate sees the fields and not the status, so each rule sits where it
//! reads what it needs:
//!
//! - A transition into a state is a request of Workflow's `transition`,
//!   whoever makes it: a caller, another behavior's `call()`, another
//!   instance's invoke, the runner. The guard reads the state from its
//!   `to` and the fields from the view, as an operation's `update()` calls
//!   leave them, and vetoes a move into a state that requires a field the
//!   instance does not hold (`missing_fields`).
//! - A create starts in the Workflow's initial state, which `parse_config`
//!   reads from the type's Workflow config, so validate refuses a create
//!   whose initial state requires a field it does not give, with the rule
//!   `required` at the field (`invalid_instance`), before anything is
//!   stored.
//! - An update, a caller's or an operation's `update()` on a caller's
//!   behalf, is an update request. The guard reads the instance's status as
//!   the caller, through Workflow's field, only when a field a rule names
//!   changes, and compares the fields before with the ones after. One that
//!   clears a field the state requires is vetoed whoever asks
//!   (`required_fields`), and checked first, since no permission lifts it;
//!   one that changes a field the state freezes is vetoed unless the caller
//!   holds the config's permission (`frozen_fields`), a behavior's write
//!   included.
//!
//! Each veto names the state and the fields in its details, in the order
//! the type lists its fields for `"all"` and the config's otherwise. A
//! value is compared as JSON, as Constants compares one, so a change
//! inside an object or a list is a change.
//!
//! configChange: any change of the rules is allowed, and the behavior can
//! be added to and removed from a schema with instances: it keeps no
//! state. An instance that predates a requirement keeps working until it
//! would lose a field the rule requires or enter the state. Revisions'
//! propose checks its patch with `validate_update()`, which asks no guard,
//! so a proposal that changes a frozen field is refused at approve.

use serde_json::{Map, Value};

use superschematic_engine::behaviors::InstanceView;
use superschematic_engine::errors::{ValidationIssue, Veto};
use superschematic_engine::json::{js_equal, js_keys};
use superschematic_engine::{
    Behavior, ConfigTarget, Error, GuardRequest, Hooks, Result, ValidationRequest,
};

use crate::guidance::list;

/// One state's rules, parsed.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct StateRule {
    /// The own fields an instance must hold to enter the state, and keeps
    /// while it is in it.
    pub required: Vec<String>,
    /// The own fields that do not change while the instance is in the
    /// state: every one of the type's when `frozen_all`.
    pub frozen: Vec<String>,
    /// Whether the config freezes every own field (`"all"`).
    pub frozen_all: bool,
}

/// StateRules' config, parsed.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct StateRulesConfig {
    /// The rules, by a state of the type's Workflow, in the order its
    /// config lists the states.
    pub states: Vec<(String, StateRule)>,
    /// The permission a caller needs to change a frozen field.
    pub permission: Option<String>,
    /// The state a new instance starts in, the type's Workflow's initial
    /// state; `None` when its config is not one Workflow takes.
    pub initial: Option<String>,
}

impl StateRulesConfig {
    /// A state's rules; `None` when the config gives it none.
    pub fn rule(&self, state: &str) -> Option<&StateRule> {
        self.states
            .iter()
            .find(|(known, _)| known == state)
            .map(|(_, rule)| rule)
    }
}

// held reports whether a field holds a value: neither absent nor null.
fn held(data: &Map<String, Value>, field: &str) -> bool {
    data.get(field).is_some_and(|value| !value.is_null())
}

// value_of is a field's value, null for none: absent and null are one.
fn value_of<'d>(data: &'d Map<String, Value>, field: &str) -> &'d Value {
    data.get(field).unwrap_or(&Value::Null)
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

/// The instance's Workflow status, read through Workflow's field as the
/// call's principal reads it (`statusOf`), as the call has left it so far;
/// `None` when it has none.
pub(crate) fn status_of<C>(view: &InstanceView<'_, C>) -> Result<Option<String>> {
    let record = view
        .instances()
        .get(view.schema(), view.id(), Some(&["Workflow.status"]))?;
    Ok(record.and_then(|record| {
        record
            .behaviors
            .get("Workflow")
            .and_then(|fields| fields.get("status"))
            .and_then(Value::as_str)
            .map(str::to_owned)
    }))
}

fn veto(code: &str, reason: String, state: &str, fields: &[&str]) -> Veto {
    let mut details = Map::new();
    details.insert("state".into(), Value::from(state));
    details.insert(
        "fields".into(),
        Value::Array(fields.iter().map(|field| Value::from(*field)).collect()),
    );
    Veto {
        reason,
        code: Some(code.to_owned()),
        details: Some(details),
    }
}

/// StateRules: what each state of the Workflow requires and freezes.
#[derive(Clone, Copy, Debug, Default)]
pub struct StateRules;

impl Behavior for StateRules {
    type Config = StateRulesConfig;

    fn declaration(&self) -> &str {
        include_str!("../declarations/StateRules.behavior.json")
    }

    // The configSchema holds the shape; this holds the states to the type's
    // Workflow, whose config it is given, and the fields to the type's own,
    // and reads the state a create starts in.
    fn parse_config(&self, raw: &Value, target: &ConfigTarget<'_>) -> Result<StateRulesConfig> {
        let empty = Map::new();
        let rules = raw
            .get("states")
            .and_then(Value::as_object)
            .unwrap_or(&empty);
        let flow = target.configs().get("Workflow");
        // A Workflow config of the wrong shape, or none, is the requires
        // check's or Workflow's own to refuse.
        let states: Option<Vec<String>> = flow
            .and_then(|flow| flow.get("states"))
            .and_then(Value::as_array)
            .filter(|states| states.iter().all(Value::is_string))
            .map(|states| {
                states
                    .iter()
                    .filter_map(Value::as_str)
                    .map(str::to_owned)
                    .collect()
            });
        let fields = target.fields();
        let field = |state: &str, rule: &str, name: &str| -> Result<()> {
            if fields.iter().any(|known| known == name) {
                return Ok(());
            }
            Err(Error::config(format!(
                "states.{state}.{rule}: {name} is not a field of {} (its fields: {})",
                target.type_name(),
                fields.join(", ")
            )))
        };
        for state in js_keys(rules) {
            if let Some(states) = &states {
                if !states.iter().any(|known| known == state) {
                    return Err(Error::config(format!(
                        "states: \"{state}\" is not a state of the type's Workflow ({})",
                        states.join(", ")
                    )));
                }
            }
            let rule = &rules[state];
            for name in strings(rule.get("required")) {
                field(state, "required", &name)?;
            }
            if rule.get("frozen").is_some_and(Value::is_array) {
                for name in strings(rule.get("frozen")) {
                    field(state, "frozen", &name)?;
                }
            }
        }
        // The rules in the order the Workflow lists its states.
        let order: Vec<String> = match &states {
            Some(states) => states.clone(),
            None => js_keys(rules).into_iter().map(str::to_owned).collect(),
        };
        let mut parsed: Vec<(String, StateRule)> = Vec::new();
        for state in order {
            let Some(rule) = rules.get(&state) else {
                continue;
            };
            if parsed.iter().any(|(known, _)| *known == state) {
                continue;
            }
            let frozen_all = rule.get("frozen").and_then(Value::as_str) == Some("all");
            parsed.push((
                state,
                StateRule {
                    required: strings(rule.get("required")),
                    frozen: if frozen_all {
                        fields.to_vec()
                    } else {
                        strings(rule.get("frozen"))
                    },
                    frozen_all,
                },
            ));
        }
        let initial = match flow.and_then(|flow| flow.get("initial")) {
            Some(Value::String(initial)) => Some(initial.clone()),
            _ => states.as_ref().and_then(|states| states.first().cloned()),
        };
        Ok(StateRulesConfig {
            states: parsed,
            permission: raw
                .get("permission")
                .and_then(Value::as_str)
                .map(str::to_owned),
            initial,
        })
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            config_change: Some(|_, _, _, _| Ok(None)),
            guidance: Some(|_, config, target| {
                crate::guidance::staterules::guidance(config, target)
            }),
            // A create starts in the initial state: what it requires, the
            // create gives. An update's rules read the status, which the
            // guard reads.
            validate: Some(|_, cx, request| {
                let ValidationRequest::Create { data } = request else {
                    return Ok(Vec::new());
                };
                let config = cx.config();
                let Some(initial) = &config.initial else {
                    return Ok(Vec::new());
                };
                let Some(rule) = config.rule(initial) else {
                    return Ok(Vec::new());
                };
                Ok(rule
                    .required
                    .iter()
                    .filter(|field| !held(data, field))
                    .map(|field| {
                        ValidationIssue::new(
                            field.as_str(),
                            "required",
                            format!(
                                "{field} is required in {initial}, the state an instance of {} is created in",
                                cx.schema()
                            ),
                        )
                    })
                    .collect())
            }),
            guard: Some(guard),
            ..Hooks::default()
        }
    }
}

fn guard(
    _: &StateRules,
    view: &InstanceView<'_, StateRulesConfig>,
    request: &GuardRequest<'_>,
) -> Result<Option<Veto>> {
    match request {
        GuardRequest::Update { after, .. } => update_guard(view, after),
        GuardRequest::Operation {
            behavior: "Workflow",
            operation: "transition",
            params,
            ..
        } => {
            let to = params.get("to").and_then(Value::as_str).unwrap_or_default();
            let Some(rule) = view.config().rule(to) else {
                return Ok(None);
            };
            let data = view.data()?;
            let missing: Vec<&str> = rule
                .required
                .iter()
                .map(String::as_str)
                .filter(|field| !held(&data, field))
                .collect();
            if missing.is_empty() {
                return Ok(None);
            }
            Ok(Some(veto(
                "missing_fields",
                format!(
                    "{} {} cannot move to {to} without {}, which {to} requires",
                    view.schema(),
                    view.id(),
                    list(&missing, "and")
                ),
                to,
                &missing,
            )))
        }
        _ => Ok(None),
    }
}

// update_guard holds an update to the rules of the state the instance is
// in: no required field it held cleared, then no frozen field changed but
// by a caller with the permission.
fn update_guard(
    view: &InstanceView<'_, StateRulesConfig>,
    after: &Map<String, Value>,
) -> Result<Option<Veto>> {
    let config = view.config();
    let before = view.data()?;
    let changes = |field: &str| !js_equal(value_of(&before, field), value_of(after, field));
    // The status is read only when a field some rule names changes.
    let named = config.states.iter().any(|(_, rule)| {
        rule.frozen.iter().any(|field| changes(field))
            || rule.required.iter().any(|field| changes(field))
    });
    if !named {
        return Ok(None);
    }
    let Some(state) = status_of(view)? else {
        return Ok(None);
    };
    let Some(rule) = config.rule(&state) else {
        return Ok(None);
    };
    let cleared: Vec<&str> = rule
        .required
        .iter()
        .map(String::as_str)
        .filter(|field| held(&before, field) && !held(after, field))
        .collect();
    if !cleared.is_empty() {
        let keeps = if cleared.len() == 1 {
            "it keeps its value"
        } else {
            "they keep their values"
        };
        return Ok(Some(veto(
            "required_fields",
            format!(
                "{} {} is {state}, which requires {}: {keeps} while it is {state}",
                view.schema(),
                view.id(),
                list(&cleared, "and")
            ),
            &state,
            &cleared,
        )));
    }
    let frozen: Vec<&str> = rule
        .frozen
        .iter()
        .map(String::as_str)
        .filter(|field| changes(field))
        .collect();
    if frozen.is_empty() {
        return Ok(None);
    }
    if let Some(permission) = &config.permission {
        if view.can(permission)? {
            return Ok(None);
        }
    }
    let one = frozen.len() == 1;
    let unless = config
        .permission
        .as_ref()
        .map_or(String::new(), |permission| {
            format!(
                "; only a caller with {permission} may change {}",
                if one { "it" } else { "them" }
            )
        });
    Ok(Some(veto(
        "frozen_fields",
        format!(
            "{} {} is {state}, which freezes {}: {} not change while it is {state}{unless}",
            view.schema(),
            view.id(),
            list(&frozen, "and"),
            if one { "it does" } else { "they do" }
        ),
        &state,
        &frozen,
    )))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_field_is_held_when_it_is_neither_absent_nor_null() {
        let data = serde_json::json!({"a": "", "b": [], "c": 0, "d": null});
        let data = data.as_object().expect("an object");
        for field in ["a", "b", "c"] {
            assert!(held(data, field), "{field}");
        }
        assert!(!held(data, "d"));
        assert!(!held(data, "e"));
        assert_eq!(value_of(data, "e"), &Value::Null);
    }
}
