//! What a scenario file's tests wrote in TypeScript, ported onto the core so
//! the replayer can run the scenarios that use it, as the engine's test kit
//! ports the test behaviors onto the engine (`superschematic-engine-testkit`'s
//! `behaviors`): the test behaviors a file composes (`testBehaviors`), each
//! acting as its TypeScript fixture does with none of its hooks set, and
//! the permission matchers a test opens an engine with
//! (`permissionMatcher`, which the recording names `js`). What a fixture
//! does that the core has no notion of (SQL of its own) is not run: the
//! replayer skips the step with [`unported_operation`]'s reason.

use serde_json::{json, Value};

use superschematic_rules::{
    Behavior, ConfigError, ConfigTarget, Core, Cx, Declaration, Hook, PermissionMatcher,
    SchemaIssue,
};

/// Registers the test behavior a scenario file names on a core; false for
/// one this kit has not ported.
pub fn register(file: &str, name: &str, core: &mut Core) -> bool {
    match (file, name) {
        ("core-workflow", "test.Dispatch") => core.push(Dispatch),
        _ => return false,
    }
    true
}

/// Whether this kit has ported the test behavior a scenario file names.
pub fn is_ported(file: &str, name: &str) -> bool {
    register(file, name, &mut Core::new())
}

/// Why the replayer does not run an operation of a test behavior a
/// scenario file names: the fixture does what only the engine's store
/// answers. `None` for an operation it runs.
pub fn unported_operation(file: &str, behavior: &str, operation: &str) -> Option<&'static str> {
    match (file, behavior, operation) {
        ("core-workflow", "test.Dispatch", "forceStatus") => Some(
            "test.Dispatch's forceStatus writes Workflow's column with SQL of its own, and the engine's store refuses SQL outside the behavior's storage: the step tests the store's SQL guard. The core runs no SQL; a write of a field a behavior does not declare is its defect, which superschematic-rules-behaviors' tests hold",
        ),
        _ => None,
    }
}

/// A core with the permission matcher a scenario's test opened an engine
/// with (`permissionMatcher: "custom"`), ported from the test's function;
/// `None` for a matcher this kit has not ported.
pub fn with_matcher(file: &str, scenario: &str, core: Core) -> Option<Core> {
    match (file, scenario) {
        (
            "core-workflow",
            "Workflow (node) > a transition that names a permission needs a caller who holds it",
        ) => Some(core.with_matcher(Rooted)),
        _ => None,
    }
}

/// The matcher of `test/core-workflow.test.ts`'s permission test, a
/// deployment's own: `(held) => held.includes('root')`. A caller who holds
/// `root` holds every permission and no one else holds any, the dotted rule
/// set aside.
pub struct Rooted;

impl PermissionMatcher for Rooted {
    fn matches(&self, held: &[String], _required: &[String]) -> bool {
        held.iter().any(|permission| permission == "root")
    }
}

/// test.Dispatch (`test/core-workflow.test.ts`): moves an order through
/// Workflow's `transition`, as a caller would, and tries to write the
/// status itself (`forceStatus`). On the core that write is of a field it
/// does not declare, which the core refuses as a defect; the replayer does
/// not run it ([`unported_operation`]), since the engine's answer is its
/// store's refusal of the fixture's SQL.
pub struct Dispatch;

impl Behavior for Dispatch {
    type Config = ();

    fn declaration(&self) -> Declaration {
        Declaration::new("test.Dispatch")
            .operation("dispatch", true)
            .operation("forceStatus", true)
    }

    fn parse_config(&self, _raw: &Value, _target: &ConfigTarget<'_>) -> Result<(), ConfigError> {
        Ok(())
    }

    // The paramsSchemas are closed objects: dispatch takes `to`, a string,
    // and forceStatus nothing.
    fn check_params(&self, _config: &(), operation: &str, params: &Value) -> Vec<SchemaIssue> {
        let Some(members) = params.as_object() else {
            return vec![SchemaIssue::new("", "must be object")];
        };
        let allowed: &[&str] = if operation == "dispatch" {
            &["to"]
        } else {
            &[]
        };
        let mut issues: Vec<SchemaIssue> = members
            .keys()
            .filter(|key| !allowed.contains(&key.as_str()))
            .map(|key| SchemaIssue::new("", format!("must NOT have additional properties: {key}")))
            .collect();
        if operation == "dispatch" {
            match members.get("to") {
                None => issues.push(SchemaIssue::new("", "must have required property 'to'")),
                Some(to) if !to.is_string() => {
                    issues.push(SchemaIssue::new("/to", "must be string"));
                }
                Some(_) => {}
            }
        }
        issues
    }

    fn operation(&self, cx: &Cx<'_, ()>, name: &str, params: &Value) -> Hook<Value> {
        match name {
            "dispatch" => cx.call("Workflow", "transition", &json!({ "to": params.get("to") })),
            "forceStatus" => {
                cx.set("status", "shipped")?;
                Ok(Value::from(1))
            }
            _ => Err(cx.defect(format!("it has no operation {name}"))),
        }
    }
}
