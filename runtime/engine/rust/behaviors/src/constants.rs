//! Constants, fields set at create and never changed after (D16), the
//! port of `src/behaviors/core/constants.ts`. Work is routed by what an
//! instance is: a step's kind, the key a blueprint stamped it with, the
//! fields it copied from its parent. When any writer can change them, the
//! holder of a lease included, a worker can turn the step it holds into
//! another one. The config lists the type's own top-level fields, by JSON
//! key, that keep the value their create gives them.
//!
//! validate refuses an update that changes a listed field, whoever makes it
//! (a caller's update, or an operation's `update()` on a caller's behalf),
//! with the rule `constant` at the field, unless the caller holds the
//! config's permission. A field the create leaves absent stays absent: its
//! absence is the value the create set, and setting it later is a change.
//! Absent and null are one value, none, since a merge patch removes a
//! member with null. A value is compared as JSON, so a change inside an
//! object or a list is a change. A create is never refused.
//!
//! configChange: any config may change, and the behavior can be added to
//! and removed from a schema with instances: it keeps no state, and a
//! stored instance satisfies any list, since validate only compares an
//! update with what the instance holds.

use serde::Deserialize;
use serde_json::{Map, Value};

use superschematic_engine::errors::ValidationIssue;
use superschematic_engine::json::js_equal;
use superschematic_engine::{
    json_config, Behavior, ConfigTarget, Error, Hooks, Result, ValidationRequest,
};

/// Constants' config, parsed.
#[derive(Clone, Debug, PartialEq, Eq, Deserialize)]
pub struct ConstantsConfig {
    /// The type's own top-level fields, by JSON key, that keep the value
    /// their create gives them.
    pub fields: Vec<String>,
    /// The permission a caller needs to change them after the create.
    #[serde(default)]
    pub permission: Option<String>,
}

// value_of is a field's value, null for none: absent and null are one.
fn value_of<'d>(data: &'d Map<String, Value>, field: &str) -> &'d Value {
    data.get(field).unwrap_or(&Value::Null)
}

/// Constants: fields their create sets for good.
#[derive(Clone, Copy, Debug, Default)]
pub struct Constants;

impl Behavior for Constants {
    type Config = ConstantsConfig;

    fn declaration(&self) -> &str {
        include_str!("../declarations/Constants.behavior.json")
    }

    // The configSchema holds the shape; this holds the fields to the
    // type's own.
    fn parse_config(&self, raw: &Value, target: &ConfigTarget<'_>) -> Result<ConstantsConfig> {
        let config: ConstantsConfig = json_config(raw)?;
        for field in &config.fields {
            if !target.fields().contains(field) {
                return Err(Error::config(format!(
                    "fields: {field} is not a field of {} (its fields: {})",
                    target.type_name(),
                    target.fields().join(", ")
                )));
            }
        }
        Ok(config)
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            config_change: Some(|_, _, _, _| Ok(None)),
            guidance: Some(|_, config, target| {
                crate::guidance::constants::guidance(config, target)
            }),
            validate: Some(|_, cx, request| {
                let ValidationRequest::Update { before, after, .. } = request else {
                    return Ok(Vec::new());
                };
                let config = cx.config();
                let changed: Vec<&String> = config
                    .fields
                    .iter()
                    .filter(|field| !js_equal(value_of(before, field), value_of(after, field)))
                    .collect();
                if changed.is_empty() {
                    return Ok(Vec::new());
                }
                if let Some(permission) = &config.permission {
                    if cx.can(permission)? {
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
                                cx.schema()
                            ),
                        )
                    })
                    .collect())
            }),
            ..Hooks::default()
        }
    }
}
