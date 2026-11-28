//! What a scenario file's tests wrote in TypeScript, ported onto the core so
//! the replayer can run the scenarios that use it, as the engine's test kit
//! ported the test behaviors onto the engine until the engine went (D103,
//! #587): the test behaviors a file composes
//! (`testBehaviors`) or registers when it opens an engine, each
//! acting as its TypeScript fixture does with none of its hooks set, and
//! the permission matchers a test opens an engine with
//! (`permissionMatcher`, which the recording names `js`), and the test
//! behaviors a hand-written file composes (D104), which no TypeScript test
//! wrote and this kit defines for it. What a fixture
//! does that the core has no notion of (SQL of its own) is not run: the
//! replayer skips the step with [`unported_operation`]'s reason.

use serde_json::{json, Value};

use superschematic_rules::{
    Behavior, Change, ConfigError, ConfigTarget, Core, Cx, Declaration, GuardRequest, Hook,
    PermissionMatcher, SchemaIssue, Veto, View,
};

/// Registers the test behavior a scenario file names on a core; false for
/// one this kit has not ported.
pub fn register(file: &str, name: &str, core: &mut Core) -> bool {
    match (file, name) {
        ("core-workflow", "test.Dispatch") => core.push(Dispatch),
        ("core-timers", "test.Bounce") => core.push(Bounce),
        ("core-timers", "test.Hold") => core.push(FireHold),
        ("core-workflow-principals", "test.Gate") => core.push(Gate),
        ("workqueue-lease", "test.Gate") => core.push(LeaseGate),
        ("workqueue-lease", "test.Signal") => core.push(Signal),
        ("core-numbering", "test.Refuser") => core.push(Refuser),
        ("core-revisions", "test.Hold") => core.push(UpdateHold),
        ("core-staterules", "test.Recorder") => core.push(Recorder),
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

// closed_params lists what parameters break of a closed object of string
// members, each required, in the behavior registry's words.
fn closed_params(params: &Value, members: &[&str]) -> Vec<SchemaIssue> {
    let Some(given) = params.as_object() else {
        return vec![SchemaIssue::new("", "must be object")];
    };
    let mut issues: Vec<SchemaIssue> = given
        .keys()
        .filter(|key| !members.contains(&key.as_str()))
        .map(|key| SchemaIssue::new("", format!("must NOT have additional properties: {key}")))
        .collect();
    for member in members {
        match given.get(*member) {
            None => issues.push(SchemaIssue::new(
                "",
                format!("must have required property '{member}'"),
            )),
            Some(value) if !value.is_string() => {
                issues.push(SchemaIssue::new(format!("/{member}"), "must be string"));
            }
            Some(_) => {}
        }
    }
    issues
}

/// test.Bounce (`test/core-timers.test.ts`): moves the status to `via` and
/// back to `back` in one call, as one operation that calls Workflow's
/// transition twice, and returns `Workflow.enteredAt` after it: a status
/// that left a state and came back within one call has a new entry time,
/// which arms an enters deadline again.
pub struct Bounce;

impl Behavior for Bounce {
    type Config = ();

    fn declaration(&self) -> Declaration {
        Declaration::new("test.Bounce").operation("bounce", true)
    }

    fn parse_config(&self, _raw: &Value, _target: &ConfigTarget<'_>) -> Result<(), ConfigError> {
        Ok(())
    }

    // Its paramsSchema is a closed object of via and back, both strings.
    fn check_params(&self, _config: &(), _operation: &str, params: &Value) -> Vec<SchemaIssue> {
        closed_params(params, &["via", "back"])
    }

    fn operation(&self, cx: &Cx<'_, ()>, name: &str, params: &Value) -> Hook<Value> {
        if name != "bounce" {
            return Err(cx.defect(format!("it has no operation {name}")));
        }
        for leg in ["via", "back"] {
            cx.call(
                "Workflow",
                "transition",
                &json!({ "to": params.get(leg).cloned().unwrap_or(Value::Null) }),
            )?;
        }
        Ok(cx.get_of("Workflow", "enteredAt").unwrap_or(Value::Null))
    }
}

/// test.Hold of `test/core-timers.test.ts`: vetoes a fire of a row whose
/// own title starts with `stuck` (`held`), as a lease another principal
/// holds does, a caller's or a deadline's alike. The engine's test kit
/// called it `FireHold`, beside the other test.Hold of
/// `test/behavior-fixtures.ts`.
pub struct FireHold;

impl Behavior for FireHold {
    type Config = ();

    fn declaration(&self) -> Declaration {
        Declaration::new("test.Hold").veto("held")
    }

    fn parse_config(&self, _raw: &Value, _target: &ConfigTarget<'_>) -> Result<(), ConfigError> {
        Ok(())
    }

    fn guard(&self, view: &View<'_, ()>, request: &GuardRequest<'_>) -> Hook<Option<Veto>> {
        let GuardRequest::Operation {
            behavior: "Timers",
            operation: "fireTimer",
            ..
        } = request
        else {
            return Ok(None);
        };
        // String(view.data.title): a missing title is "undefined".
        let held = matches!(
            view.own_field("title"),
            Some(Value::String(title)) if title.starts_with("stuck")
        );
        Ok(held.then(|| Veto::new("the test holds it").with_code("held")))
    }
}

/// test.Refuser (`test/core-numbering.test.ts`): refuses a create whose
/// title is `refuse` after every use listed before it has run, as the
/// fixture's afterChange vetoes it, so a scenario shows a refused create
/// takes no number (`refused`).
pub struct Refuser;

impl Behavior for Refuser {
    type Config = ();

    fn declaration(&self) -> Declaration {
        Declaration::new("test.Refuser").veto("refused")
    }

    fn parse_config(&self, _raw: &Value, _target: &ConfigTarget<'_>) -> Result<(), ConfigError> {
        Ok(())
    }

    fn after_change(&self, cx: &Cx<'_, ()>, change: &Change<'_>) -> Hook<()> {
        let refused = matches!(change, Change::Create)
            && cx.own_field("title") == Some(Value::from("refuse"));
        if refused {
            return Err(cx.veto(Veto::new("refused").with_code("refused")));
        }
        Ok(())
    }
}

/// test.Hold of `test/core-revisions.test.ts`: once its `hold` has run,
/// vetoes every update of its row, whoever makes it, a caller's or an
/// approval's through `update()`, so a scenario shows an approval meets
/// every guard. Its `held` reads false until then. The engine's test kit
/// had no port of it; this kit calls it `UpdateHold`, beside the other
/// test.Holds.
pub struct UpdateHold;

impl Behavior for UpdateHold {
    type Config = ();

    fn declaration(&self) -> Declaration {
        Declaration::new("test.Hold")
            .field("held")
            .operation("hold", true)
    }

    fn parse_config(&self, _raw: &Value, _target: &ConfigTarget<'_>) -> Result<(), ConfigError> {
        Ok(())
    }

    fn initialize(&self, cx: &Cx<'_, ()>, _params: &Value) -> Hook<()> {
        cx.set("held", false)
    }

    fn guard(&self, view: &View<'_, ()>, request: &GuardRequest<'_>) -> Hook<Option<Veto>> {
        let held = view.get("held") == Some(Value::Bool(true));
        Ok((held && matches!(request, GuardRequest::Update { .. }))
            .then(|| Veto::new("it is held")))
    }

    fn operation(&self, cx: &Cx<'_, ()>, _name: &str, _params: &Value) -> Hook<Value> {
        cx.set("held", true)?;
        Ok(Value::Bool(true))
    }
}

/// test.Recorder of `test/core-staterules.test.ts`: `record` writes the
/// own fields its `patch` gives with `update()`, as a behavior applies a
/// result on a caller's behalf, then moves Workflow to `to` when given, in
/// the same call, so a scenario shows a transition reads the fields as the
/// update left them and a veto undoes both.
pub struct Recorder;

impl Behavior for Recorder {
    type Config = ();

    fn declaration(&self) -> Declaration {
        Declaration::new("test.Recorder").operation("record", true)
    }

    fn parse_config(&self, _raw: &Value, _target: &ConfigTarget<'_>) -> Result<(), ConfigError> {
        Ok(())
    }

    fn operation(&self, cx: &Cx<'_, ()>, _name: &str, params: &Value) -> Hook<Value> {
        cx.update(params.get("patch").unwrap_or(&Value::Null))?;
        if let Some(to) = params.get("to").filter(|to| to.is_string()) {
            cx.call("Workflow", "transition", &json!({ "to": to }))?;
        }
        Ok(Value::Bool(true))
    }
}

/// test.Gate of the hand-written `core-workflow-principals.json` (D104):
/// vetoes every Workflow transition of its row (`gated`), as a gate another
/// behavior keeps would, so a scenario shows which moves reach the guards.
/// A repeat Workflow answers as no change before any guard is asked passes
/// it; one that is the veto already_in_state, and any other move, meet it.
/// Its config is none, or `{}`.
pub struct Gate;

impl Behavior for Gate {
    type Config = ();

    fn declaration(&self) -> Declaration {
        Declaration::new("test.Gate").veto("gated")
    }

    fn parse_config(&self, raw: &Value, _target: &ConfigTarget<'_>) -> Result<(), ConfigError> {
        match raw.as_object() {
            Some(members) if members.is_empty() => Ok(()),
            _ => Err(ConfigError::Schema(vec![SchemaIssue::new(
                "",
                "must NOT have additional properties",
            )])),
        }
    }

    fn guard(&self, _view: &View<'_, ()>, request: &GuardRequest<'_>) -> Hook<Option<Veto>> {
        let gated = matches!(
            request,
            GuardRequest::Operation {
                behavior: "Workflow",
                operation: "transition",
                ..
            }
        );
        Ok(gated.then(|| Veto::new("the gate is closed").with_code("gated")))
    }
}

/// test.Gate, as the work queue's TypeScript tests defined it until the
/// engine went (D103, #587): it
/// refuses Workflow's transition of a row titled held, and Lease's expire
/// of one titled stuck, with no code, whoever calls.
pub struct LeaseGate;

impl Behavior for LeaseGate {
    type Config = ();

    fn declaration(&self) -> Declaration {
        Declaration::new("test.Gate")
    }

    fn parse_config(&self, _raw: &Value, _target: &ConfigTarget<'_>) -> Result<(), ConfigError> {
        Ok(())
    }

    fn guard(&self, view: &View<'_, ()>, request: &GuardRequest<'_>) -> Hook<Option<Veto>> {
        let GuardRequest::Operation {
            behavior,
            operation,
            ..
        } = request
        else {
            return Ok(None);
        };
        let title = view.own_field("title");
        let titled = |name: &str| title.as_ref().and_then(Value::as_str) == Some(name);
        if titled("held") && (*behavior, *operation) == ("Workflow", "transition") {
            return Ok(Some(Veto::new("it is held")));
        }
        if titled("stuck") && (*behavior, *operation) == ("Lease", "expire") {
            return Ok(Some(Veto::new("it is stuck")));
        }
        Ok(None)
    }
}

/// test.Signal, as the work queue's TypeScript tests defined it until the
/// engine went (D103, #587):
/// its own operation, `signal({ name })`, sends the holder a directive
/// through Lease's direct (`Cx::call`), as a budget does on an overrun.
pub struct Signal;

impl Behavior for Signal {
    type Config = ();

    fn declaration(&self) -> Declaration {
        Declaration::new("test.Signal").operation("signal", true)
    }

    fn parse_config(&self, _raw: &Value, _target: &ConfigTarget<'_>) -> Result<(), ConfigError> {
        Ok(())
    }

    // The paramsSchema is a closed object: signal takes name, a string.
    fn check_params(&self, _config: &(), _operation: &str, params: &Value) -> Vec<SchemaIssue> {
        let Some(members) = params.as_object() else {
            return vec![SchemaIssue::new("", "must be object")];
        };
        let mut issues: Vec<SchemaIssue> = members
            .keys()
            .filter(|key| key.as_str() != "name")
            .map(|key| SchemaIssue::new("", format!("must NOT have additional properties: {key}")))
            .collect();
        match members.get("name") {
            None => issues.push(SchemaIssue::new("", "must have required property 'name'")),
            Some(name) if !name.is_string() => {
                issues.push(SchemaIssue::new("/name", "must be string"));
            }
            Some(_) => {}
        }
        issues
    }

    fn operation(&self, cx: &Cx<'_, ()>, _name: &str, params: &Value) -> Hook<Value> {
        cx.call("Lease", "direct", &json!({ "name": params.get("name") }))
    }
}
