//! What the engine's tools are written with (D11, D16, D58), the port of
//! `src/tools/options.ts`: the invocation policy and the vendor-extension
//! keys. Both are the deployment's binary's registrations in the compiler,
//! which the engine cannot read, so they are engine config
//! (`EngineOptions.tools`), with the core's values as defaults, held to
//! the Go defaults by `runtime/engine/testdata/tool_parameters_parity.json`.
//!
//! - `invocation_policy`: the key a tool's policy is written under, its
//!   values and its default (`apigen.ToolInvocationPolicy`, section 3.15 of
//!   `docs/extension-model.md`). A behavior operation takes its
//!   declaration's `invocationPolicy`, which must be one of the values, or
//!   the default.
//! - `invocation`: the policy of the operations every schema has and of the
//!   engine's own tools, by operation name, and of a behavior's operation,
//!   by `<Behavior>.<operation>` (`Payments.capturePayment`,
//!   `acme.Rating.rate`), which takes the place of its declaration's
//!   `invocationPolicy` (D70); the default when absent. A deployment marks
//!   an operation whose declaration names no policy, as the core's leave
//!   money-moving ones, through it.
//! - `keys`: the vendor keys of the tool documents (`apigen.ToolKeys`,
//!   section 3.14): the scalar key, the `_meta` key of a tool's guidance,
//!   and keys written at the root of every argument schema.

use serde_json::{Map, Value};

use crate::errors::{Error, Result};
use crate::json;

/// An invocation policy: `apigen.ToolInvocationPolicy` without its
/// extension.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct InvocationPolicy {
    /// The key the policy is written under.
    pub key: String,
    /// The allowed values, in order.
    pub values: Vec<String>,
    /// The value an operation gets when nothing sets one.
    pub default: String,
}

impl Default for InvocationPolicy {
    /// The core's (`apigen.DefaultToolInvocationPolicy`): `invocationPolicy`,
    /// auto or ask, auto.
    fn default() -> Self {
        InvocationPolicy {
            key: "invocationPolicy".to_owned(),
            values: vec!["auto".to_owned(), "ask".to_owned()],
            default: "auto".to_owned(),
        }
    }
}

/// The vendor keys of the tool documents: `apigen.ToolKeys`.
#[derive(Clone, Debug, PartialEq)]
pub struct ToolKeys {
    /// The key an argument property names its scalar under; empty leaves
    /// the name out.
    pub scalar: String,
    /// The `_meta` key of a visible tool's guidance; empty leaves it out.
    pub guidance: String,
    /// Written at the root of every argument schema, in order.
    pub parameters: Vec<(String, Value)>,
}

impl Default for ToolKeys {
    /// The core's (`apigen.DefaultToolKeys`).
    fn default() -> Self {
        ToolKeys {
            scalar: "x-superschematic-scalar".to_owned(),
            guidance: "superschematic/operation-guidance".to_owned(),
            parameters: Vec::new(),
        }
    }
}

impl ToolKeys {
    /// The keys as the parity vectors write them.
    pub fn to_json(&self) -> Value {
        serde_json::json!({
            "scalar": self.scalar,
            "guidance": self.guidance,
            "parameters": self.parameters.iter().map(|(key, value)| serde_json::json!({"key": key, "value": value})).collect::<Vec<_>>(),
        })
    }
}

/// The operations every schema has, lookup, which a schema with a unique
/// field has, the engine's tools for writing schemas and reading its
/// behaviors, its search across them, the read of a value by its hash, and
/// its tools for namespaces.
pub const BUILTIN_TOOLS: [&str; 17] = [
    "create",
    "get",
    "list",
    "update",
    "delete",
    "lookup",
    "listSchemas",
    "describeSchema",
    "defineSchema",
    "listBehaviors",
    "describeBehavior",
    "search",
    "getValue",
    "listNamespaces",
    "createNamespace",
    "archiveNamespace",
    "unarchiveNamespace",
];

/// `EngineOptions.tools`: each absent part is the core's.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct ToolOptions {
    pub invocation_policy: Option<InvocationPolicy>,
    /// The policy of a built-in operation or engine tool, by its name, or
    /// of a behavior's operation, by `<Behavior>.<operation>`.
    pub invocation: Vec<(String, String)>,
    pub scalar_key: Option<String>,
    pub guidance_key: Option<String>,
    pub parameter_keys: Option<Vec<(String, Value)>>,
}

impl ToolOptions {
    /// Checks the options as the compiler's registry checks them, as
    /// opening an engine with them does: every problem named.
    pub fn check(&self) -> Result<()> {
        resolve_tool_options(self).map(|_| ())
    }

    /// Reads the options from the JSON the TypeScript engine takes:
    /// `{ invocationPolicy?: { key, values, default }, invocation?: {...},
    /// keys?: { scalar?, guidance?, parameters?: [{ key, value }] } }`.
    pub fn from_json(value: &Value) -> Result<Self> {
        let Value::Object(members) = value else {
            return Err(Error::usage(
                "engine tool options: tools is { invocationPolicy?, invocation?, keys? }",
            ));
        };
        let mut options = ToolOptions::default();
        if let Some(policy) = members.get("invocationPolicy") {
            let text = |key: &str| policy.get(key).and_then(Value::as_str).unwrap_or_default();
            options.invocation_policy = Some(InvocationPolicy {
                key: text("key").to_owned(),
                values: policy
                    .get("values")
                    .and_then(Value::as_array)
                    .map(|values| {
                        values
                            .iter()
                            .map(|value| match value {
                                Value::String(text) => text.clone(),
                                other => json::stringify(other),
                            })
                            .collect()
                    })
                    .unwrap_or_default(),
                default: text("default").to_owned(),
            });
        }
        if let Some(Value::Object(invocation)) = members.get("invocation") {
            for (name, value) in invocation {
                if let Some(value) = value.as_str() {
                    options.invocation.push((name.clone(), value.to_owned()));
                }
            }
        }
        if let Some(Value::Object(keys)) = members.get("keys") {
            options.scalar_key = keys
                .get("scalar")
                .and_then(Value::as_str)
                .map(str::to_owned);
            options.guidance_key = keys
                .get("guidance")
                .and_then(Value::as_str)
                .map(str::to_owned);
            if let Some(Value::Array(parameters)) = keys.get("parameters") {
                options.parameter_keys = Some(
                    parameters
                        .iter()
                        .map(|entry| {
                            (
                                entry
                                    .get("key")
                                    .and_then(Value::as_str)
                                    .unwrap_or_default()
                                    .to_owned(),
                                entry.get("value").cloned().unwrap_or(Value::Null),
                            )
                        })
                        .collect(),
                );
            }
        }
        Ok(options)
    }
}

/// Tool options with every default filled in and checked.
#[derive(Clone, Debug, PartialEq)]
pub(crate) struct ResolvedToolOptions {
    pub invocation_policy: InvocationPolicy,
    /// Each built-in tool's policy, in [`BUILTIN_TOOLS`]' order.
    pub invocation: Vec<(String, String)>,
    /// The behavior operations the options set a policy for, each as
    /// (behavior, operation, policy), in the order given.
    pub operations: Vec<(String, String, String)>,
    pub keys: ToolKeys,
}

impl ResolvedToolOptions {
    /// The policy of a built-in operation or engine tool.
    pub fn invocation_of(&self, tool: &str) -> &str {
        self.invocation
            .iter()
            .find(|(name, _)| name == tool)
            .map_or(self.invocation_policy.default.as_str(), |(_, value)| {
                value.as_str()
            })
    }

    /// The policy of a behavior's operation: the options' for it, else
    /// its declaration's, else the default.
    pub fn operation_invocation<'a>(
        &'a self,
        behavior: &str,
        operation: &str,
        declared: Option<&'a str>,
    ) -> &'a str {
        self.operations
            .iter()
            .find(|(b, o, _)| b == behavior && o == operation)
            .map(|(_, _, value)| value.as_str())
            .or(declared)
            .unwrap_or(self.invocation_policy.default.as_str())
    }
}

/// A behavior operation's key in `invocation`, `<Behavior>.<operation>`,
/// split at its last dot: the behavior's registered name, bare or
/// qualified, and an operation name. `None` for a key with no dot, a
/// built-in tool's.
pub(crate) fn behavior_operation(key: &str) -> Option<(&str, &str)> {
    key.rsplit_once('.')
}

// ^[A-Za-z][A-Za-z0-9_]*$, an operation's name.
fn is_operation_name(name: &str) -> bool {
    let mut chars = name.chars();
    chars.next().is_some_and(|c| c.is_ascii_alphabetic())
        && chars.all(|c| c.is_ascii_alphanumeric() || c == '_')
}

// The keys the mcp record and @mcp already use (ir.ValidateMCPInvocationKey).
const RESERVED_POLICY_KEYS: [&str; 8] = [
    "handle",
    "hidden",
    "hiddenReason",
    "_meta",
    "name",
    "description",
    "icon",
    "reason",
];
// The keys the core writes at the root of an argument schema.
const RESERVED_PARAMETER_KEYS: [&str; 4] =
    ["type", "additionalProperties", "properties", "required"];

// ^[A-Za-z][A-Za-z0-9_]*$
fn is_policy_key(key: &str) -> bool {
    let mut chars = key.chars();
    chars.next().is_some_and(|c| c.is_ascii_alphabetic())
        && chars.all(|c| c.is_ascii_alphanumeric() || c == '_')
}

// ^[a-z][a-z0-9_-]*$
fn is_policy_value(value: &str) -> bool {
    let mut chars = value.chars();
    chars.next().is_some_and(|c| c.is_ascii_lowercase())
        && chars.all(|c| c.is_ascii_lowercase() || c.is_ascii_digit() || c == '_' || c == '-')
}

/// Fills in the defaults and refuses, naming every problem, options the
/// compiler's registry would refuse (the TypeScript engine's TypeError).
pub(crate) fn resolve_tool_options(options: &ToolOptions) -> Result<ResolvedToolOptions> {
    let mut problems: Vec<String> = Vec::new();
    let policy = options.invocation_policy.clone().unwrap_or_default();
    if !is_policy_key(&policy.key) {
        problems.push(format!(
            "invocation policy key {} must be a letter followed by letters, digits or underscores",
            json::quote(&policy.key)
        ));
    } else if RESERVED_POLICY_KEYS.contains(&policy.key.as_str()) {
        problems.push(format!(
            "invocation policy key {} is a key @mcp already uses",
            json::quote(&policy.key)
        ));
    }
    if policy.values.is_empty() {
        problems.push(format!("invocation policy {} lists no values", policy.key));
    }
    for (index, value) in policy.values.iter().enumerate() {
        if !is_policy_value(value) {
            problems.push(format!(
                "invocation policy value {} must be lowercase letters, digits, underscores or hyphens, starting with a letter",
                json::quote(value)
            ));
        } else if policy.values.iter().position(|each| each == value) != Some(index) {
            problems.push(format!("invocation policy value {value} is listed twice"));
        }
    }
    if !policy.values.contains(&policy.default) {
        problems.push(format!(
            "invocation policy default {} is not one of {}",
            json::quote(&policy.default),
            policy.values.join(", ")
        ));
    }
    let mut operations = Vec::new();
    for (name, value) in &options.invocation {
        if let Some((behavior, operation)) = behavior_operation(name) {
            if !crate::behaviors::declaration::is_behavior_name(behavior)
                || !is_operation_name(operation)
            {
                problems.push(format!(
                    "invocation names {}, which is no behavior's operation (<Behavior>.<operation>)",
                    json::quote(name)
                ));
            } else if !policy.values.contains(value) {
                problems.push(format!(
                    "the invocation policy of {name}, {}, is not one of {}",
                    json::quote(value),
                    policy.values.join(", ")
                ));
            } else {
                operations.push((behavior.to_owned(), operation.to_owned(), value.clone()));
            }
        } else if !BUILTIN_TOOLS.contains(&name.as_str()) {
            problems.push(format!(
                "invocation names {name}, which is not one of {}, nor a behavior's operation (<Behavior>.<operation>)",
                BUILTIN_TOOLS.join(", ")
            ));
        } else if !policy.values.contains(value) {
            problems.push(format!(
                "the invocation policy of {name}, {}, is not one of {}",
                json::quote(value),
                policy.values.join(", ")
            ));
        }
    }
    let invocation = BUILTIN_TOOLS
        .iter()
        .map(|name| {
            let value = options
                .invocation
                .iter()
                .find(|(given, _)| given == name)
                .map_or(policy.default.clone(), |(_, value)| value.clone());
            ((*name).to_owned(), value)
        })
        .collect();
    let defaults = ToolKeys::default();
    let keys = ToolKeys {
        scalar: options.scalar_key.clone().unwrap_or(defaults.scalar),
        guidance: options.guidance_key.clone().unwrap_or(defaults.guidance),
        parameters: options
            .parameter_keys
            .clone()
            .unwrap_or(defaults.parameters),
    };
    let mut seen: Vec<&str> = Vec::new();
    for (key, _) in &keys.parameters {
        if key.is_empty() {
            problems.push("a tool parameter key is empty".to_owned());
        } else if RESERVED_PARAMETER_KEYS.contains(&key.as_str()) {
            problems.push(format!(
                "tool parameter key {} is written by the core",
                json::quote(key)
            ));
        } else if *key == keys.scalar {
            problems.push(format!(
                "tool parameter key {} is the scalar key",
                json::quote(key)
            ));
        } else if seen.contains(&key.as_str()) {
            problems.push(format!(
                "tool parameter key {} is listed twice",
                json::quote(key)
            ));
        }
        seen.push(key);
    }
    if !problems.is_empty() {
        return Err(Error::usage(format!(
            "engine tool options: {}",
            problems.join("; ")
        )));
    }
    Ok(ResolvedToolOptions {
        invocation_policy: policy,
        invocation,
        operations,
        keys,
    })
}

/// A policy as a describe or tools document writes it: under its key.
pub(crate) fn policy_entry(policy: &InvocationPolicy, value: &str) -> Map<String, Value> {
    let mut entry = Map::new();
    entry.insert(policy.key.clone(), Value::from(value));
    entry
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn the_defaults_resolve_and_a_bad_option_names_every_problem() {
        let resolved = resolve_tool_options(&ToolOptions::default()).unwrap();
        assert_eq!(resolved.invocation_of("create"), "auto");
        assert_eq!(resolved.keys, ToolKeys::default());
        let refused = resolve_tool_options(&ToolOptions {
            invocation_policy: Some(InvocationPolicy {
                key: "handle".into(),
                values: vec!["Auto".into()],
                default: "manual".into(),
            }),
            invocation: vec![("publish".into(), "auto".into())],
            ..ToolOptions::default()
        });
        let Err(Error::Usage(message)) = refused else {
            panic!("refused")
        };
        assert!(message.starts_with(
            "engine tool options: invocation policy key \"handle\" is a key @mcp already uses; "
        ));
        assert!(message.contains("invocation names publish"));
    }

    #[test]
    fn a_behavior_operation_takes_the_policy_the_options_give_it() {
        let resolved = resolve_tool_options(&ToolOptions {
            invocation: vec![
                ("Payments.capturePayment".into(), "ask".into()),
                ("acme.Rating.rate".into(), "ask".into()),
                ("delete".into(), "ask".into()),
            ],
            ..ToolOptions::default()
        })
        .unwrap();
        assert_eq!(
            resolved.operations,
            vec![
                ("Payments".into(), "capturePayment".into(), "ask".into()),
                ("acme.Rating".into(), "rate".into(), "ask".into()),
            ]
        );
        assert_eq!(resolved.invocation_of("delete"), "ask");
        assert_eq!(
            resolved.operation_invocation("Payments", "capturePayment", None),
            "ask"
        );
        assert_eq!(
            resolved.operation_invocation("Payments", "openPayment", None),
            "auto"
        );
        assert_eq!(
            resolved.operation_invocation("acme.Rating", "rate", Some("auto")),
            "ask"
        );
        assert_eq!(
            resolved.operation_invocation("acme.Rating", "clear", Some("ask")),
            "ask"
        );

        let refused = resolve_tool_options(&ToolOptions {
            invocation: vec![
                ("Payments.refundPayment".into(), "maybe".into()),
                (".capturePayment".into(), "ask".into()),
                ("Payments.".into(), "ask".into()),
            ],
            ..ToolOptions::default()
        });
        let Err(Error::Usage(message)) = refused else {
            panic!("refused")
        };
        assert!(message.contains(
            "the invocation policy of Payments.refundPayment, \"maybe\", is not one of auto, ask"
        ));
        assert!(message
            .contains("invocation names \".capturePayment\", which is no behavior's operation"));
        assert!(
            message.contains("invocation names \"Payments.\", which is no behavior's operation")
        );
    }
}
