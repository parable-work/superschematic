//! Who may do what (D58's access policy as data, D103, D117): the question
//! the handler asks before each request a caller makes, and the policy that
//! answers it, in the engine's form, so a policy written for the engine
//! reads here unchanged.
//!
//! - `define` for a type's draft, `publish` for its next version, `read`
//!   for its describe, a get and a list, and `write` for a create, an
//!   update and a delete; a behavior's operation asks `write` or `read`, as
//!   its declaration says it writes, and names itself (`operation`).
//! - The question is asked before anything is read: a type that does not
//!   exist is refused to a caller who may not read it as one that does. A
//!   define asks once its document reads, about the type it defines, and an
//!   operation once the type's live version names the operation.
//! - A deadline and a write the change queue holds are the host's runner's,
//!   which no caller makes, and ask nothing.
//! - A request needs a caller with a subject, and a service standing in for
//!   an end user holds no permissions (D37): each refused
//!   `invalid_argument` before the policy is asked.
//!
//! The policy is `"allowAll"`, `"permission"` (an action on a type to a
//! caller holding `<type>.<action>`), or `{"rules": [...]}`: the first rule
//! that matches a question decides it, and a question no rule matches is
//! refused. A rule matches on the question's `action` (one or a list of
//! `read`, `write`, `define`, `publish` and `manage`), `operation`,
//! `schema` and `namespace`, and the caller's `subject`; it decides with
//! `allow` (true or false) or `require`, a permission or a list of them,
//! any one of which the caller must hold, matched with the core's matcher.
//! A permission is a template: `{schema}`, `{action}`, `{namespace}` and
//! `{operation}` stand for the question's, and one whose placeholder the
//! question lacks is held by no one. Development mode keeps one namespace,
//! `default`.
//!
//! The engine's members for what development mode has none of read too, so
//! a policy moves between them: `behavior` (a behavior's namespace
//! operation; no question here is about one), `service` (a calling
//! service, D37; a caller standing in for one is a service whose deployable
//! the handler does not know), `standsIn`, `actingFor` (D77; no caller here
//! acts for another) and `listing` (no question here is a listing one). A
//! rule that names one of them matches as the engine's matches a question
//! with none of it.

use serde::{Deserialize, Serialize};
use serde_json::{Map, Value};
use superschematic_rules::{Action as CoreAction, Caller, PermissionMatcher, Refusal};

use crate::document::Model;
use crate::handler::{Context, Handler, Request};

/// The one namespace development mode keeps, which every question names.
pub const NAMESPACE: &str = "default";

/// What a question asks of a type.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub enum Action {
    Read,
    Write,
    Define,
    Publish,
}

impl Action {
    pub fn as_str(self) -> &'static str {
        match self {
            Action::Read => "read",
            Action::Write => "write",
            Action::Define => "define",
            Action::Publish => "publish",
        }
    }
}

/// One question the handler asks the policy: who asks, what of which type,
/// and for a behavior's operation, its name; or, for an operation of the
/// namespace as a whole (D77's primitive 3), its behavior in place of a
/// type (`schema` empty).
#[derive(Clone, Copy, Debug)]
pub struct Question<'a> {
    pub caller: &'a Caller,
    pub action: Action,
    pub schema: &'a str,
    pub operation: Option<&'a str>,
    pub behavior: Option<&'a str>,
}

impl Question<'_> {
    /// The question as the engine passes it to a policy: `{ principal,
    /// action, namespace, schema, operation? }`.
    pub fn to_json(&self) -> Value {
        let mut out = Map::new();
        out.insert(
            "principal".into(),
            Value::from(self.caller.subject.as_str()),
        );
        out.insert("action".into(), Value::from(self.action.as_str()));
        out.insert("namespace".into(), Value::from(NAMESPACE));
        match self.behavior {
            Some(behavior) => {
                out.insert("behavior".into(), Value::from(behavior));
            }
            None => {
                out.insert("schema".into(), Value::from(self.schema));
            }
        }
        if let Some(operation) = self.operation {
            out.insert("operation".into(), Value::from(operation));
        }
        Value::Object(out)
    }

    /// The refusal of a question the policy did not allow, in the engine's
    /// words: `<subject> may not <action> <schema> in namespace default`,
    /// or `call <operation> (<action>) on <schema>` for an operation.
    pub fn refusal(&self) -> Refusal {
        let what = match (self.behavior, self.operation) {
            (Some(behavior), operation) => format!(
                "call {} ({}) of behavior {behavior}",
                operation.unwrap_or_default(),
                self.action.as_str()
            ),
            (None, None) => format!("{} {}", self.action.as_str(), self.schema),
            (None, Some(operation)) => format!(
                "call {operation} ({}) on {}",
                self.action.as_str(),
                self.schema
            ),
        };
        Refusal::forbidden(format!(
            "{} may not {what} in namespace {NAMESPACE}",
            self.caller.subject
        ))
    }
}

/// What a rule says about a member the engine's principal has and a caller
/// here has not: any, none (`true`, `false`), or one of these names.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Named {
    Any(bool),
    Names(Vec<String>),
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum Placeholder {
    Schema,
    Action,
    Namespace,
    Operation,
    Behavior,
}

impl Placeholder {
    fn of(name: &str) -> Option<Placeholder> {
        Some(match name {
            "schema" => Placeholder::Schema,
            "action" => Placeholder::Action,
            "namespace" => Placeholder::Namespace,
            "operation" => Placeholder::Operation,
            "behavior" => Placeholder::Behavior,
            _ => return None,
        })
    }

    fn value<'q>(self, question: &'q Question<'_>) -> Option<&'q str> {
        match self {
            Placeholder::Schema => Some(question.schema),
            Placeholder::Action => Some(question.action.as_str()),
            Placeholder::Namespace => Some(NAMESPACE),
            Placeholder::Operation => question.operation,
            Placeholder::Behavior => question.behavior,
        }
    }
}

#[derive(Clone, Debug, PartialEq, Eq)]
enum Segment {
    Text(String),
    Value(Placeholder),
}

/// A permission a rule requires, its placeholders standing for the
/// question's.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Template {
    text: String,
    segments: Vec<Segment>,
}

impl Template {
    /// Reads a template; a placeholder other than the five, or a brace
    /// left open, is refused.
    pub fn parse(text: &str) -> Result<Self, String> {
        let mut segments = Vec::new();
        let mut rest = text;
        while let Some(open) = rest.find('{') {
            if open > 0 {
                segments.push(Segment::Text(rest[..open].to_owned()));
            }
            let after = &rest[open + 1..];
            let close = after
                .find('}')
                .ok_or_else(|| format!("{} leaves a brace open", quote(text)))?;
            let name = &after[..close];
            let placeholder = Placeholder::of(name).ok_or_else(|| {
                format!(
                    "{} names {{{name}}}; a permission's placeholders are {{schema}}, {{action}}, {{namespace}}, {{operation}} and {{behavior}}",
                    quote(text)
                )
            })?;
            segments.push(Segment::Value(placeholder));
            rest = &after[close + 1..];
        }
        if rest.contains('}') {
            return Err(format!("{} closes a brace it never opened", quote(text)));
        }
        if !rest.is_empty() {
            segments.push(Segment::Text(rest.to_owned()));
        }
        Ok(Template {
            text: text.to_owned(),
            segments,
        })
    }

    pub fn as_str(&self) -> &str {
        &self.text
    }

    /// The permission a question requires; `None` when the question has no
    /// value for one of its placeholders.
    pub fn render(&self, question: &Question<'_>) -> Option<String> {
        let mut out = String::new();
        for segment in &self.segments {
            match segment {
                Segment::Text(text) => out.push_str(text),
                Segment::Value(placeholder) => out.push_str(placeholder.value(question)?),
            }
        }
        Some(out)
    }
}

/// How a rule decides a question it matches.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Decision {
    Allow(bool),
    Require(Vec<Template>),
}

/// One rule: what it matches, each member left out matching anything (but
/// `behavior` and `actingFor`, as the engine's), and how it decides.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Rule {
    /// `read`, `write`, `define`, `publish` or `manage`; `manage` names a
    /// namespace's own operations, which development mode has none of.
    pub action: Option<Vec<String>>,
    pub operation: Option<Vec<String>>,
    pub schema: Option<Vec<String>>,
    pub namespace: Option<Vec<String>>,
    pub behavior: Option<Named>,
    pub subject: Option<Vec<String>>,
    pub service: Option<Named>,
    pub stands_in: Option<bool>,
    pub acting_for: Option<Named>,
    pub listing: Option<bool>,
    pub decision: Decision,
}

impl Rule {
    /// Whether the rule matches a question.
    pub fn matches(&self, question: &Question<'_>) -> bool {
        let named = |list: &Option<Vec<String>>, value: Option<&str>| match list {
            None => true,
            Some(list) => value.is_some_and(|value| list.iter().any(|item| item == value)),
        };
        let caller = question.caller;
        // A question about an operation of the namespace (D77's primitive
        // 3) matches only a rule that names its behavior, as the engine's
        // does, and one that names no schema; any other matches a rule that
        // names none, or says false.
        let behavior = match question.behavior {
            Some(behavior) => {
                self.schema.is_none()
                    && match &self.behavior {
                        Some(Named::Any(any)) => *any,
                        Some(Named::Names(names)) => names.iter().any(|name| name == behavior),
                        None => false,
                    }
            }
            None => {
                named(&self.schema, Some(question.schema))
                    && matches!(&self.behavior, None | Some(Named::Any(false)))
            }
        };
        named(&self.action, Some(question.action.as_str()))
            && named(&self.operation, question.operation)
            && behavior
            && named(&self.namespace, Some(NAMESPACE))
            && named(&self.subject, Some(&caller.subject))
            // A caller standing in is a service's own principal, whose
            // deployable the handler does not know.
            && match &self.service {
                None => true,
                Some(Named::Any(any)) => caller.stands_in == *any,
                Some(Named::Names(_)) => false,
            }
            && self.stands_in.is_none_or(|stands_in| caller.stands_in == stands_in)
            // No caller here acts for another (D77).
            && matches!(&self.acting_for, None | Some(Named::Any(false)))
            // No question here is a listing one.
            && self.listing.is_none_or(|listing| !listing)
    }

    /// The rule's answer to a question it matches.
    pub fn decide(&self, question: &Question<'_>, matcher: &dyn PermissionMatcher) -> bool {
        match &self.decision {
            Decision::Allow(allow) => *allow,
            Decision::Require(templates) => {
                let required: Vec<String> = templates
                    .iter()
                    .filter_map(|template| template.render(question))
                    .collect();
                !required.is_empty() && matcher.matches(&question.caller.permissions, &required)
            }
        }
    }
}

/// The access policy: a shorthand or rules. It reads from and writes to
/// JSON as the engine's does ([`Policy::from_json`], or serde).
#[derive(Clone, Debug, Default, PartialEq, Eq, Deserialize, Serialize)]
#[serde(try_from = "Value", into = "Value")]
pub enum Policy {
    /// `"allowAll"`: every question allowed, for local use and tests.
    #[default]
    AllowAll,
    /// `"permission"`: an action on a type to a caller holding
    /// `<type>.<action>`.
    Permission,
    /// `{"rules": [...]}`.
    Rules(Vec<Rule>),
}

/// The policy a handler answers with when its host gives none.
pub static ALLOW_ALL: Policy = Policy::AllowAll;

impl Policy {
    /// Whether the policy allows a question, its `require` rules matched
    /// with `matcher`.
    pub fn allows(&self, question: &Question<'_>, matcher: &dyn PermissionMatcher) -> bool {
        match self {
            Policy::AllowAll => true,
            // An operation of the namespace that reads keeps the types the
            // caller may read, so anyone may call it; one that writes needs
            // <behavior>.<operation>, as the engine's permission policy.
            Policy::Permission => match question.behavior {
                Some(_) if question.action == Action::Read => true,
                Some(behavior) => {
                    let required = format!("{behavior}.{}", question.operation.unwrap_or_default());
                    matcher.matches(&question.caller.permissions, &[required])
                }
                None => {
                    let required = format!("{}.{}", question.schema, question.action.as_str());
                    matcher.matches(&question.caller.permissions, &[required])
                }
            },
            Policy::Rules(rules) => rules
                .iter()
                .find(|rule| rule.matches(question))
                .is_some_and(|rule| rule.decide(question, matcher)),
        }
    }

    /// Refuses a question the policy does not allow with `forbidden`.
    pub fn require(
        &self,
        question: &Question<'_>,
        matcher: &dyn PermissionMatcher,
    ) -> Result<(), Refusal> {
        if self.allows(question, matcher) {
            Ok(())
        } else {
            Err(question.refusal())
        }
    }

    /// Reads a policy: `"allowAll"`, `"permission"`, or `{ "rules": [...]
    /// }`, the engine's form; the message names what is wrong and where.
    pub fn from_json(value: &Value) -> Result<Self, String> {
        match value {
            Value::String(name) => match name.as_str() {
                "allowAll" => Ok(Policy::AllowAll),
                "permission" => Ok(Policy::Permission),
                other => Err(format!(
                    "policy {} is not a shorthand: \"allowAll\" or \"permission\", or {{ \"rules\": [...] }}",
                    quote(other)
                )),
            },
            Value::Object(members) => {
                if let Some(key) = members.keys().find(|key| *key != "rules") {
                    return Err(format!(
                        "policy: {} is not a member of a policy, which is {{ \"rules\": [...] }}",
                        quote(key)
                    ));
                }
                let rules = members
                    .get("rules")
                    .and_then(Value::as_array)
                    .ok_or("policy: rules is a list of rules")?;
                rules
                    .iter()
                    .enumerate()
                    .map(|(index, entry)| {
                        rule_of(entry).map_err(|message| format!("policy: rules[{index}]{message}"))
                    })
                    .collect::<Result<_, _>>()
                    .map(Policy::Rules)
            }
            _ => Err(
                "a policy is \"allowAll\", \"permission\" or { \"rules\": [...] }".to_owned(),
            ),
        }
    }

    /// The policy as JSON, as [`Policy::from_json`] reads it.
    pub fn to_json(&self) -> Value {
        match self {
            Policy::AllowAll => Value::from("allowAll"),
            Policy::Permission => Value::from("permission"),
            Policy::Rules(rules) => {
                let mut out = Map::new();
                out.insert(
                    "rules".into(),
                    Value::Array(rules.iter().map(rule_json).collect()),
                );
                Value::Object(out)
            }
        }
    }
}

impl TryFrom<Value> for Policy {
    type Error = String;

    fn try_from(value: Value) -> Result<Self, String> {
        Policy::from_json(&value)
    }
}

impl From<Policy> for Value {
    fn from(policy: Policy) -> Value {
        policy.to_json()
    }
}

fn quote(text: &str) -> String {
    Value::from(text).to_string()
}

const ACTIONS: [&str; 5] = ["read", "write", "define", "publish", "manage"];

const RULE_MEMBERS: [&str; 12] = [
    "action",
    "operation",
    "schema",
    "namespace",
    "behavior",
    "subject",
    "service",
    "standsIn",
    "actingFor",
    "listing",
    "allow",
    "require",
];

// rule_of reads one rule; an error is the member's path and what is wrong,
// in the engine's words.
fn rule_of(entry: &Value) -> Result<Rule, String> {
    let Value::Object(members) = entry else {
        return Err(": a rule is a JSON object".to_owned());
    };
    if let Some(key) = members
        .keys()
        .find(|key| !RULE_MEMBERS.contains(&key.as_str()))
    {
        return Err(format!(
            ": {} is not a member of a rule, which matches on {} and decides with allow or require",
            quote(key),
            RULE_MEMBERS[..10].join(", ")
        ));
    }
    let names = |key: &str| -> Result<Option<Vec<String>>, String> {
        match members.get(key) {
            None => Ok(None),
            Some(Value::String(one)) => Ok(Some(vec![one.clone()])),
            Some(Value::Array(list)) if !list.is_empty() => list
                .iter()
                .map(|item| {
                    item.as_str()
                        .map(str::to_owned)
                        .ok_or_else(|| format!(".{key}: a name or a list of names"))
                })
                .collect::<Result<Vec<_>, _>>()
                .map(Some),
            Some(_) => Err(format!(".{key}: a name or a non-empty list of names")),
        }
    };
    let flag = |key: &str| -> Result<Option<bool>, String> {
        match members.get(key) {
            None => Ok(None),
            Some(Value::Bool(flag)) => Ok(Some(*flag)),
            Some(_) => Err(format!(".{key}: true or false")),
        }
    };
    let named = |key: &str, what: &str| -> Result<Option<Named>, String> {
        match members.get(key) {
            None => Ok(None),
            Some(Value::Bool(any)) => Ok(Some(Named::Any(*any))),
            Some(_) => names(key)
                .map_err(|_| format!(".{key}: true, false, or {what} or a list of them"))
                .map(|list| Some(Named::Names(list.unwrap_or_default()))),
        }
    };
    let action = names("action")?;
    if let Some(unknown) = action
        .iter()
        .flatten()
        .find(|name| !ACTIONS.contains(&name.as_str()))
    {
        return Err(format!(
            ".action: {} is not one of read, write, define, publish, manage",
            quote(unknown)
        ));
    }
    let decision = match (members.get("allow"), members.get("require")) {
        (Some(Value::Bool(allow)), None) => Decision::Allow(*allow),
        (Some(_), None) => return Err(".allow: true or false".to_owned()),
        (None, Some(_)) => Decision::Require(
            names("require")
                .map_err(|_| ".require: a permission or a non-empty list of them".to_owned())?
                .unwrap_or_default()
                .iter()
                .map(|text| Template::parse(text).map_err(|message| format!(".require: {message}")))
                .collect::<Result<_, _>>()?,
        ),
        (Some(_), Some(_)) => {
            return Err(": a rule decides with allow or require, not both".to_owned())
        }
        (None, None) => {
            return Err(
                ": a rule decides with allow (true or false) or require (a permission)".to_owned(),
            )
        }
    };
    Ok(Rule {
        action,
        operation: names("operation")?,
        schema: names("schema")?,
        namespace: names("namespace")?,
        behavior: named("behavior", "a behavior")?,
        subject: names("subject")?,
        service: named("service", "a deployable")?,
        stands_in: flag("standsIn")?,
        acting_for: named("actingFor", "a subject")?,
        listing: flag("listing")?,
        decision,
    })
}

fn rule_json(rule: &Rule) -> Value {
    let list = |items: &[String]| -> Value {
        match items {
            [one] => Value::from(one.as_str()),
            many => Value::Array(many.iter().map(|item| Value::from(item.as_str())).collect()),
        }
    };
    let named = |named: &Named| -> Value {
        match named {
            Named::Any(any) => Value::Bool(*any),
            Named::Names(names) => list(names),
        }
    };
    let mut out = Map::new();
    for (key, value) in [
        ("action", &rule.action),
        ("operation", &rule.operation),
        ("schema", &rule.schema),
        ("namespace", &rule.namespace),
    ] {
        if let Some(items) = value {
            out.insert(key.into(), list(items));
        }
    }
    if let Some(behavior) = &rule.behavior {
        out.insert("behavior".into(), named(behavior));
    }
    if let Some(subject) = &rule.subject {
        out.insert("subject".into(), list(subject));
    }
    if let Some(service) = &rule.service {
        out.insert("service".into(), named(service));
    }
    if let Some(stands_in) = rule.stands_in {
        out.insert("standsIn".into(), Value::Bool(stands_in));
    }
    if let Some(acting_for) = &rule.acting_for {
        out.insert("actingFor".into(), named(acting_for));
    }
    if let Some(listing) = rule.listing {
        out.insert("listing".into(), Value::Bool(listing));
    }
    match &rule.decision {
        Decision::Allow(allow) => {
            out.insert("allow".into(), Value::Bool(*allow));
        }
        Decision::Require(templates) => {
            let texts: Vec<String> = templates
                .iter()
                .map(|template| template.as_str().to_owned())
                .collect();
            out.insert("require".into(), list(&texts));
        }
    }
    Value::Object(out)
}

impl Handler<'_> {
    /// Asks the policy one question of a caller, refusing with `forbidden`.
    pub(crate) fn ask(
        &self,
        caller: &Caller,
        action: Action,
        schema: &str,
        operation: Option<&str>,
    ) -> Result<(), Refusal> {
        self.policy().require(
            &Question {
                caller,
                action,
                schema,
                operation,
                behavior: None,
            },
            self.core().matcher(),
        )
    }

    /// Asks the policy about an operation of the namespace as a whole (D77's
    /// primitive 3): its behavior and name, `read` or `write`.
    pub(crate) fn ask_behavior(
        &self,
        caller: &Caller,
        action: Action,
        behavior: &str,
        operation: &str,
    ) -> Result<(), Refusal> {
        self.policy().require(
            &Question {
                caller,
                action,
                schema: "",
                operation: Some(operation),
                behavior: Some(behavior),
            },
            self.core().matcher(),
        )
    }

    /// Admits a request a caller makes before anything is read: a caller
    /// with a subject, and the policy's answer to the question the request
    /// asks of its type. A define asks once its document reads, and an
    /// operation once its type is read ([`Handler::ask_operation`]); the
    /// runner's requests ask nothing.
    pub(crate) fn admit(&self, request: &Request, context: &Context) -> Result<(), Refusal> {
        let (action, type_name) = match request {
            Request::Deadline { .. } | Request::Change { .. } => return Ok(()),
            Request::Define { .. }
            | Request::Operation { .. }
            | Request::TableOperation { .. }
            | Request::Signal { .. }
            | Request::NamespaceOperation { .. }
            | Request::Types
            | Request::Tools => (None, ""),
            Request::Publish { type_name, .. } => (Some(Action::Publish), type_name.as_str()),
            Request::Describe { type_name }
            | Request::Get { type_name, .. }
            | Request::List { type_name, .. } => (Some(Action::Read), type_name.as_str()),
            Request::Create { type_name, .. }
            | Request::Update { type_name, .. }
            | Request::Delete { type_name, .. } => (Some(Action::Write), type_name.as_str()),
        };
        let caller = &context.caller;
        if caller.subject.is_empty() {
            return Err(Refusal::invalid_argument(
                "a request needs a caller with a subject",
            ));
        }
        if caller.stands_in && !caller.permissions.is_empty() {
            return Err(Refusal::invalid_argument(format!(
                "{} stands in for an end user and holds no permissions (D37)",
                caller.subject
            )));
        }
        match action {
            Some(action) => self.ask(&context.caller, action, type_name, None),
            None => Ok(()),
        }
    }

    /// Asks the policy about a behavior's operation of a row or of the
    /// table: `write` or `read` as its declaration says it writes, naming
    /// the operation. One the type's uses do not declare asks nothing here,
    /// and the core refuses it.
    pub(crate) fn ask_operation(
        &self,
        model: &Model,
        action: &CoreAction,
        caller: &Caller,
    ) -> Result<(), Refusal> {
        let (CoreAction::Operation {
            use_id, operation, ..
        }
        | CoreAction::TableOperation {
            use_id, operation, ..
        }) = action
        else {
            return Ok(());
        };
        let declared = model
            .use_of(use_id)
            .and_then(|used| self.declarations().get(&used.spec.behavior))
            .and_then(|declared| {
                declared
                    .operations
                    .iter()
                    .find(|declared| declared.name == *operation)
            });
        let Some(declared) = declared else {
            return Ok(());
        };
        // A write of the caller's own state alone says it asks read
        // (`"asks": "read"`, D77): a reader may mark a row read.
        let asked = if declared.writes && !declared.asks_read {
            Action::Write
        } else {
            Action::Read
        };
        self.ask(caller, asked, &model.name, Some(operation))
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;
    use superschematic_rules::DottedPermissions;

    fn ask(policy: &Policy, caller: &Caller, action: Action, schema: &str) -> bool {
        policy.allows(
            &Question {
                caller,
                action,
                schema,
                operation: None,
                behavior: None,
            },
            &DottedPermissions,
        )
    }

    #[test]
    fn a_policy_reads_and_writes_the_engines_form() {
        let written = json!({"rules": [
            {"action": "manage", "allow": false},
            {"action": ["define", "publish"], "require": "schemas.{action}"},
            {"action": "write", "operation": ["comment", "propose"], "require": "{schema}.read"},
            {"subject": "ops", "service": true, "allow": true},
            {"require": "{schema}.{action}"}
        ]});
        let policy = Policy::from_json(&written).expect("it reads");
        assert_eq!(policy.to_json(), written);
        assert_eq!(
            serde_json::from_value::<Policy>(json!("permission")).expect("it reads"),
            Policy::Permission
        );
        for (bad, says) in [
            (json!("everyone"), "is not a shorthand"),
            (
                json!({"rules": [{"action": "eat", "allow": true}]}),
                "rules[0].action",
            ),
            (
                json!({"rules": [{"allow": true, "require": "x"}]}),
                "not both",
            ),
            (
                json!({"rules": [{"require": "{who}.read"}]}),
                "placeholders",
            ),
            (
                json!({"rules": [{"who": "me", "allow": true}]}),
                "not a member of a rule",
            ),
            (json!({"rule": []}), "not a member of a policy"),
        ] {
            let message = Policy::from_json(&bad).expect_err("refused");
            assert!(message.contains(says), "{bad}: {message}");
        }
    }

    #[test]
    fn the_first_rule_that_matches_decides_and_none_refuses() {
        let policy = Policy::from_json(&json!({"rules": [
            {"subject": "mallory", "allow": false},
            {"action": ["define", "publish"], "require": "schemas.{action}"},
            {"action": "read", "allow": true},
            {"action": "write", "schema": "Order", "require": "{schema}.{action}"}
        ]}))
        .expect("it reads");
        let ada = Caller::new("ada", ["schemas", "Order.write"]);
        let bob = Caller::new("bob", Vec::<String>::new());
        let mallory = Caller::new("mallory", ["schemas"]);
        assert!(ask(&policy, &ada, Action::Define, "Order"));
        assert!(ask(&policy, &ada, Action::Write, "Order"));
        assert!(
            !ask(&policy, &ada, Action::Write, "Note"),
            "no rule matches"
        );
        assert!(!ask(&policy, &bob, Action::Publish, "Order"));
        assert!(ask(&policy, &bob, Action::Read, "Order"));
        assert!(!ask(&policy, &mallory, Action::Read, "Order"));
        assert!(ask(&Policy::AllowAll, &bob, Action::Define, "Order"));
        assert!(ask(&Policy::Permission, &ada, Action::Write, "Order"));
        assert!(!ask(&Policy::Permission, &ada, Action::Read, "Order"));
        // What development mode has none of: a namespace operation, a
        // listing, a principal acting for another.
        let none = Policy::from_json(&json!({"rules": [
            {"behavior": true, "allow": true},
            {"listing": true, "allow": true},
            {"actingFor": true, "allow": true},
            {"service": ["api"], "allow": true}
        ]}))
        .expect("it reads");
        assert!(!ask(&none, &ada, Action::Read, "Order"));
        let refusal = Question {
            caller: &bob,
            action: Action::Write,
            schema: "Order",
            operation: Some("transition"),
            behavior: None,
        }
        .refusal();
        assert_eq!(
            refusal.message,
            "bob may not call transition (write) on Order in namespace default"
        );
    }
}
