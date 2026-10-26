//! Who may do what (D16, D58): the questions the engine asks before every
//! read and write, the port of `src/access.ts`, and the policy that answers
//! them, which D58 makes data.
//!
//! `read` for a schema or its instances and events, `write` to create,
//! update or delete an instance, `define` for a draft, `publish` for a new
//! version; a behavior operation asks `write` or `read` and names itself.
//! `manage` names no schema: it is asked about a namespace itself, with what
//! is done to it (`create`, `archive`, `unarchive`, or `list`). The tools
//! document asks one more kind while it lists a caller's tools, a listing
//! question ([`AccessRequest::Listing`]): whether the caller may do what
//! `define_schema` or a namespace tool does at all. A namespace operation
//! of a behavior (D77) asks a fourth kind ([`AccessRequest::Behavior`]):
//! `read` or `write`, with the behavior and the operation, and no schema;
//! the tools document asks it as a listing question too.
//!
//! The TypeScript engine took the policy as a function it called before
//! every check. A Go or Rust host would answer such a function through a
//! callback across its binding, so D58 makes the policy data a deployment
//! configures ([`PolicyConfig`]), which every binding passes as JSON:
//!
//! - `"allowAll"` allows everything (tests and local use);
//! - `"permission"` is the TypeScript engine's `permissionPolicy()`: an
//!   action on a schema to a caller holding `<schema>.<action>`; a
//!   behavior's namespace operation that reads to anyone, and one that
//!   writes to a caller holding `<behavior>.<operation>`; and no other
//!   question that names no schema;
//! - `{"rules": [...]}` is a list of rules, the first that matches a
//!   question deciding it, a question no rule matches refused. A rule
//!   matches on the question's action (one or a list of `read`, `write`,
//!   `define`, `publish` and `manage`), its operation, schema and
//!   namespace, its behavior (`true` for any question about a behavior's
//!   namespace operation, `false` for any other, or a name or a list of
//!   them), the principal's subject, its calling service (D37) and
//!   whether that service stands in for an end user, and whether the
//!   question is a listing one; a member a rule leaves out matches
//!   anything. It decides with `allow` (`true` or `false`) or with
//!   `require`, a permission or a list of them, any one of which the
//!   principal must hold, matched with the engine's permission matcher
//!   (the HTTP runtime's `hasAnyPermission` unless configured). A
//!   permission is a template: `{schema}`, `{action}`, `{namespace}`,
//!   `{operation}` and `{behavior}` stand for the question's; one whose
//!   placeholder the question lacks (`{schema}` in a `manage` question) is
//!   held by no one.
//!
//! ```json
//! {"rules": [
//!   {"action": "manage", "allow": false},
//!   {"action": ["define", "publish"], "require": "schemas.{action}"},
//!   {"action": "write", "operation": ["comment", "propose"], "require": "{schema}.read"},
//!   {"require": "{schema}.{action}"}
//! ]}
//! ```
//!
//! A deployment that needs more compiles a policy of its own into its
//! engine: any [`AccessPolicy`] ([`Policy::custom`]). Whatever the policy,
//! the engine's own principal ([`Principal::system`]) is allowed
//! everything.

use std::fmt;
use std::sync::Arc;

use serde::{Deserialize, Serialize};
use serde_json::{Map, Value};

use crate::errors::{EngineError, EngineErrorCode, Error};
use crate::json;
use crate::principal::Principal;

/// What a question about one schema asks.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub enum SchemaAction {
    Read,
    Write,
    Define,
    Publish,
}

impl SchemaAction {
    pub fn as_str(self) -> &'static str {
        match self {
            SchemaAction::Read => "read",
            SchemaAction::Write => "write",
            SchemaAction::Define => "define",
            SchemaAction::Publish => "publish",
        }
    }
}

/// What `manage` asks to do with a namespace.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub enum NamespaceOperation {
    Create,
    Archive,
    Unarchive,
    List,
}

impl NamespaceOperation {
    pub fn as_str(self) -> &'static str {
        match self {
            NamespaceOperation::Create => "create",
            NamespaceOperation::Archive => "archive",
            NamespaceOperation::Unarchive => "unarchive",
            NamespaceOperation::List => "list",
        }
    }
}

/// What a listing question asks: whether the caller may define some schema
/// of the namespace (`define_schema`), or do what a namespace tool does to
/// some namespace (`manage`, with the operation).
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub enum ListingAction {
    Define,
    Manage,
}

impl ListingAction {
    pub fn as_str(self) -> &'static str {
        match self {
            ListingAction::Define => "define",
            ListingAction::Manage => "manage",
        }
    }
}

/// One question the engine asks the policy.
#[derive(Clone, Copy, Debug)]
pub enum AccessRequest<'a> {
    /// About a schema of a namespace: its instances, events, drafts and
    /// versions. `operation` names a behavior operation.
    Schema {
        principal: &'a Principal,
        action: SchemaAction,
        namespace: &'a str,
        schema: &'a str,
        operation: Option<&'a str>,
    },
    /// About a namespace itself (`manage`), which names no schema.
    Namespace {
        principal: &'a Principal,
        namespace: &'a str,
        operation: NamespaceOperation,
    },
    /// While the tools document lists a caller's tools: whether the caller
    /// may do what an engine tool that names its schema or namespace only
    /// when called does at all. It names no schema.
    Listing {
        principal: &'a Principal,
        action: ListingAction,
        namespace: &'a str,
        operation: Option<NamespaceOperation>,
    },
    /// About a behavior's namespace operation (D77), which runs on the
    /// behavior's tables across the namespace's schemas and names no
    /// schema: `read` or `write`, as its declaration says. The tools
    /// document asks it with `listing` while it lists the operation's tool.
    Behavior {
        principal: &'a Principal,
        /// `Read` or `Write`.
        action: SchemaAction,
        namespace: &'a str,
        behavior: &'a str,
        operation: &'a str,
        listing: bool,
    },
}

impl AccessRequest<'_> {
    /// Who asks.
    pub fn principal(&self) -> &Principal {
        match self {
            AccessRequest::Schema { principal, .. }
            | AccessRequest::Namespace { principal, .. }
            | AccessRequest::Listing { principal, .. }
            | AccessRequest::Behavior { principal, .. } => principal,
        }
    }

    /// The action, as the TypeScript engine's `AccessRequest.action`.
    pub fn action(&self) -> &'static str {
        match self {
            AccessRequest::Schema { action, .. } => action.as_str(),
            AccessRequest::Namespace { .. } => "manage",
            AccessRequest::Listing { action, .. } => action.as_str(),
            AccessRequest::Behavior { action, .. } => action.as_str(),
        }
    }

    /// The namespace the question is about.
    pub fn namespace(&self) -> &str {
        match self {
            AccessRequest::Schema { namespace, .. }
            | AccessRequest::Namespace { namespace, .. }
            | AccessRequest::Listing { namespace, .. }
            | AccessRequest::Behavior { namespace, .. } => namespace,
        }
    }

    /// The schema, for a question about one.
    pub fn schema(&self) -> Option<&str> {
        match self {
            AccessRequest::Schema { schema, .. } => Some(schema),
            _ => None,
        }
    }

    /// The operation: a behavior operation's name, or what `manage` does
    /// to a namespace.
    pub fn operation(&self) -> Option<&str> {
        match self {
            AccessRequest::Schema { operation, .. } => *operation,
            AccessRequest::Namespace { operation, .. } => Some(operation.as_str()),
            AccessRequest::Listing { operation, .. } => operation.map(NamespaceOperation::as_str),
            AccessRequest::Behavior { operation, .. } => Some(operation),
        }
    }

    /// The behavior, for a question about one of its namespace operations.
    pub fn behavior(&self) -> Option<&str> {
        match self {
            AccessRequest::Behavior { behavior, .. } => Some(behavior),
            _ => None,
        }
    }

    /// Whether it is a listing question.
    pub fn listing(&self) -> bool {
        match self {
            AccessRequest::Listing { .. } => true,
            AccessRequest::Behavior { listing, .. } => *listing,
            _ => false,
        }
    }

    /// The question as the TypeScript engine passes it to a policy, with
    /// the principal as `principal`: `{ principal, action, namespace,
    /// schema?, behavior?, operation?, listing? }`.
    pub fn to_json(&self, principal: Value) -> Value {
        let mut question = Map::new();
        question.insert("principal".into(), principal);
        question.insert("action".into(), Value::from(self.action()));
        question.insert("namespace".into(), Value::from(self.namespace()));
        if let Some(schema) = self.schema() {
            question.insert("schema".into(), Value::from(schema));
        }
        if let Some(behavior) = self.behavior() {
            question.insert("behavior".into(), Value::from(behavior));
        }
        if let Some(operation) = self.operation() {
            question.insert("operation".into(), Value::from(operation));
        }
        if self.listing() {
            question.insert("listing".into(), Value::Bool(true));
        }
        Value::Object(question)
    }
}

/// Answers the engine's questions: true allows. A deployment's engine crate
/// implements it for a policy rules cannot say ([`Policy::custom`]). It
/// runs inside the engine's transaction, on the call's thread, and must not
/// call the engine.
pub trait AccessPolicy: Send + Sync {
    fn allows(&self, request: &AccessRequest<'_>) -> bool;
}

/// Decides whether a principal holds a permission: a rule's `require`, and
/// a behavior's `can()`, ask it. The default is
/// [`DottedPermissions`], the HTTP runtime's `hasAnyPermission`; a
/// deployment with its own vocabulary implements it in its engine crate.
pub trait PermissionMatcher: Send + Sync {
    /// Whether `held` satisfies `required`; an empty `required` is
    /// satisfied.
    fn matches(&self, held: &[String], required: &[String]) -> bool;
}

/// The default permission matcher: dotted paths, where a granted permission
/// covers itself and every permission nested under it, and no root
/// permission ([`has_any_permission`]).
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct DottedPermissions;

impl PermissionMatcher for DottedPermissions {
    fn matches(&self, held: &[String], required: &[String]) -> bool {
        has_any_permission(held, required)
    }
}

/// Whether a granted permission covers a required one: equal, or the
/// required one nested under it (`covers` of `@superschematic/http-runtime`
/// and the Rust HTTP runtime).
pub fn covers(granted: &str, required: &str) -> bool {
    granted == required
        || required
            .strip_prefix(granted)
            .is_some_and(|rest| rest.starts_with('.'))
}

/// True when `required` is empty or any held permission covers any
/// required one: the HTTP runtime's `hasAnyPermission`.
pub fn has_any_permission(held: &[String], required: &[String]) -> bool {
    required.is_empty()
        || required
            .iter()
            .any(|need| held.iter().any(|have| covers(have, need)))
}

/// An action a rule matches: a schema's, or `manage`.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub enum PolicyAction {
    Read,
    Write,
    Define,
    Publish,
    Manage,
}

impl PolicyAction {
    pub const ALL: [PolicyAction; 5] = [
        PolicyAction::Read,
        PolicyAction::Write,
        PolicyAction::Define,
        PolicyAction::Publish,
        PolicyAction::Manage,
    ];

    pub fn as_str(self) -> &'static str {
        match self {
            PolicyAction::Read => "read",
            PolicyAction::Write => "write",
            PolicyAction::Define => "define",
            PolicyAction::Publish => "publish",
            PolicyAction::Manage => "manage",
        }
    }

    fn of(name: &str) -> Option<PolicyAction> {
        PolicyAction::ALL
            .into_iter()
            .find(|action| action.as_str() == name)
    }
}

/// What a rule says about the calling service (D37): any service or none
/// (`true`, `false`), or one of these deployables.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum ServiceMatch {
    Any(bool),
    Deployables(Vec<String>),
}

/// What a rule says about a behavior's namespace operation (D77): any
/// question about one or none (`true`, `false`), or one about one of these
/// behaviors.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum BehaviorMatch {
    Any(bool),
    Names(Vec<String>),
}

/// A placeholder a permission template fills from the question.
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
        match name {
            "schema" => Some(Placeholder::Schema),
            "action" => Some(Placeholder::Action),
            "namespace" => Some(Placeholder::Namespace),
            "operation" => Some(Placeholder::Operation),
            "behavior" => Some(Placeholder::Behavior),
            _ => None,
        }
    }

    fn value<'r>(self, request: &'r AccessRequest<'_>) -> Option<&'r str> {
        match self {
            Placeholder::Schema => request.schema(),
            Placeholder::Action => Some(request.action()),
            Placeholder::Namespace => Some(request.namespace()),
            Placeholder::Operation => request.operation(),
            Placeholder::Behavior => request.behavior(),
        }
    }
}

#[derive(Clone, Debug, PartialEq, Eq)]
enum Segment {
    Text(String),
    Value(Placeholder),
}

/// A permission a rule requires, with `{schema}`, `{action}`,
/// `{namespace}`, `{operation}` and `{behavior}` standing for the
/// question's.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct PermissionTemplate {
    text: String,
    segments: Vec<Segment>,
}

impl PermissionTemplate {
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
                .ok_or_else(|| format!("{} leaves a brace open", json::quote(text)))?;
            let name = &after[..close];
            let placeholder = Placeholder::of(name).ok_or_else(|| {
                format!(
                    "{} names {{{name}}}; a permission's placeholders are {{schema}}, {{action}}, {{namespace}}, {{operation}} and {{behavior}}",
                    json::quote(text)
                )
            })?;
            segments.push(Segment::Value(placeholder));
            rest = &after[close + 1..];
        }
        if rest.contains('}') {
            return Err(format!(
                "{} closes a brace it never opened",
                json::quote(text)
            ));
        }
        if !rest.is_empty() {
            segments.push(Segment::Text(rest.to_owned()));
        }
        Ok(PermissionTemplate {
            text: text.to_owned(),
            segments,
        })
    }

    /// The template as written.
    pub fn as_str(&self) -> &str {
        &self.text
    }

    /// The permission a question requires, or `None` when the question has
    /// no value for one of its placeholders.
    pub fn render(&self, request: &AccessRequest<'_>) -> Option<String> {
        let mut out = String::new();
        for segment in &self.segments {
            match segment {
                Segment::Text(text) => out.push_str(text),
                Segment::Value(placeholder) => out.push_str(placeholder.value(request)?),
            }
        }
        Some(out)
    }
}

/// How a rule decides a question it matches.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Decision {
    /// `allow`: true allows, false refuses.
    Allow(bool),
    /// `require`: the principal must hold one of these permissions.
    Require(Vec<PermissionTemplate>),
}

/// One rule of a policy: what it matches, each member left out matching
/// anything, and how it decides.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct PolicyRule {
    pub action: Option<Vec<PolicyAction>>,
    pub operation: Option<Vec<String>>,
    pub schema: Option<Vec<String>>,
    pub namespace: Option<Vec<String>>,
    pub behavior: Option<BehaviorMatch>,
    pub subject: Option<Vec<String>>,
    pub service: Option<ServiceMatch>,
    pub stands_in: Option<bool>,
    pub listing: Option<bool>,
    pub decision: Decision,
}

impl PolicyRule {
    /// Whether the rule matches a question.
    pub fn matches(&self, request: &AccessRequest<'_>) -> bool {
        let principal = request.principal();
        let named = |list: &Option<Vec<String>>, value: Option<&str>| match list {
            None => true,
            Some(list) => value.is_some_and(|value| list.iter().any(|item| item == value)),
        };
        self.action.as_ref().is_none_or(|actions| {
            actions
                .iter()
                .any(|action| action.as_str() == request.action())
        }) && named(&self.operation, request.operation())
            && named(&self.schema, request.schema())
            && named(&self.namespace, Some(request.namespace()))
            && match &self.behavior {
                None => true,
                Some(BehaviorMatch::Any(any)) => request.behavior().is_some() == *any,
                Some(BehaviorMatch::Names(list)) => request
                    .behavior()
                    .is_some_and(|behavior| list.iter().any(|item| item == behavior)),
            }
            && named(&self.subject, Some(&principal.subject))
            && match &self.service {
                None => true,
                Some(ServiceMatch::Any(any)) => principal.service.is_some() == *any,
                Some(ServiceMatch::Deployables(list)) => principal
                    .service
                    .as_ref()
                    .is_some_and(|service| list.contains(&service.deployable)),
            }
            && self
                .stands_in
                .is_none_or(|stands_in| principal.stands_in() == stands_in)
            && self
                .listing
                .is_none_or(|listing| request.listing() == listing)
    }

    /// The rule's answer to a question it matches.
    pub fn decide(&self, request: &AccessRequest<'_>, matcher: &dyn PermissionMatcher) -> bool {
        match &self.decision {
            Decision::Allow(allow) => *allow,
            Decision::Require(templates) => {
                let required: Vec<String> = templates
                    .iter()
                    .filter_map(|template| template.render(request))
                    .collect();
                !required.is_empty() && matcher.matches(&request.principal().permissions, &required)
            }
        }
    }
}

/// A policy's shorthand.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum PolicyShorthand {
    /// `"allowAll"`: allows everything, for tests and local use.
    AllowAll,
    /// `"permission"`: the TypeScript engine's `permissionPolicy()`, an
    /// action on a schema to a caller holding `<schema>.<action>`; a
    /// behavior's namespace operation (D77) that reads to anyone, since its
    /// `readable()` keeps the rows of the schemas the caller may read, and
    /// one that writes to a caller holding `<behavior>.<operation>`; any
    /// other question that names no schema is refused.
    Permission,
}

/// The access policy as a deployment configures it: a shorthand or rules.
/// It reads from JSON ([`PolicyConfig::from_json`], or serde), which is
/// how every binding passes it.
#[derive(Clone, Debug, PartialEq, Eq, Deserialize, Serialize)]
#[serde(try_from = "Value", into = "Value")]
pub enum PolicyConfig {
    Shorthand(PolicyShorthand),
    Rules(Vec<PolicyRule>),
}

impl PolicyConfig {
    /// The rules a configuration answers with: a shorthand's, or its own.
    pub fn rules(&self) -> Vec<PolicyRule> {
        match self {
            PolicyConfig::Shorthand(PolicyShorthand::AllowAll) => vec![rule(Decision::Allow(true))],
            PolicyConfig::Shorthand(PolicyShorthand::Permission) => {
                let template = |text: &str| {
                    PermissionTemplate::parse(text).unwrap_or_else(|_| {
                        unreachable!("the permission shorthand's templates read")
                    })
                };
                vec![
                    PolicyRule {
                        action: Some(vec![PolicyAction::Read]),
                        behavior: Some(BehaviorMatch::Any(true)),
                        ..rule(Decision::Allow(true))
                    },
                    PolicyRule {
                        action: Some(vec![PolicyAction::Write]),
                        behavior: Some(BehaviorMatch::Any(true)),
                        ..rule(Decision::Require(vec![template("{behavior}.{operation}")]))
                    },
                    rule(Decision::Require(vec![template("{schema}.{action}")])),
                ]
            }
            PolicyConfig::Rules(rules) => rules.clone(),
        }
    }

    /// Reads a policy from JSON: `"allowAll"`, `"permission"`, or `{
    /// "rules": [...] }`; the message names what is wrong and where.
    pub fn from_json(value: &Value) -> Result<Self, String> {
        match value {
            Value::String(name) => match name.as_str() {
                "allowAll" => Ok(PolicyConfig::Shorthand(PolicyShorthand::AllowAll)),
                "permission" => Ok(PolicyConfig::Shorthand(PolicyShorthand::Permission)),
                other => Err(format!(
                    "policy {} is not a shorthand: \"allowAll\" or \"permission\", or {{ \"rules\": [...] }}",
                    json::quote(other)
                )),
            },
            Value::Object(members) => {
                if let Some(key) = members.keys().find(|key| *key != "rules") {
                    return Err(format!(
                        "policy: {} is not a member of a policy, which is {{ \"rules\": [...] }}",
                        json::quote(key)
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
                    .map(PolicyConfig::Rules)
            }
            _ => Err(
                "a policy is \"allowAll\", \"permission\" or { \"rules\": [...] }".to_owned(),
            ),
        }
    }

    /// The policy as JSON, as [`PolicyConfig::from_json`] reads it.
    pub fn to_json(&self) -> Value {
        match self {
            PolicyConfig::Shorthand(PolicyShorthand::AllowAll) => Value::from("allowAll"),
            PolicyConfig::Shorthand(PolicyShorthand::Permission) => Value::from("permission"),
            PolicyConfig::Rules(rules) => {
                let mut policy = Map::new();
                policy.insert(
                    "rules".into(),
                    Value::Array(rules.iter().map(rule_json).collect()),
                );
                Value::Object(policy)
            }
        }
    }
}

impl TryFrom<Value> for PolicyConfig {
    type Error = String;

    fn try_from(value: Value) -> Result<Self, String> {
        PolicyConfig::from_json(&value)
    }
}

impl From<PolicyConfig> for Value {
    fn from(config: PolicyConfig) -> Value {
        config.to_json()
    }
}

fn rule(decision: Decision) -> PolicyRule {
    PolicyRule {
        action: None,
        operation: None,
        schema: None,
        namespace: None,
        behavior: None,
        subject: None,
        service: None,
        stands_in: None,
        listing: None,
        decision,
    }
}

const RULE_MEMBERS: [&str; 11] = [
    "action",
    "operation",
    "schema",
    "namespace",
    "behavior",
    "subject",
    "service",
    "standsIn",
    "listing",
    "allow",
    "require",
];

// rule_of reads one rule; an error is the member's path and what is wrong.
fn rule_of(entry: &Value) -> Result<PolicyRule, String> {
    let Value::Object(members) = entry else {
        return Err(": a rule is a JSON object".to_owned());
    };
    if let Some(key) = members
        .keys()
        .find(|key| !RULE_MEMBERS.contains(&key.as_str()))
    {
        return Err(format!(
            ": {} is not a member of a rule, which matches on {} and decides with allow or require",
            json::quote(key),
            RULE_MEMBERS[..9].join(", ")
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
    let action = names("action")?
        .map(|list| {
            list.iter()
                .map(|name| {
                    PolicyAction::of(name).ok_or_else(|| {
                        format!(
                            ".action: {} is not one of read, write, define, publish, manage",
                            json::quote(name)
                        )
                    })
                })
                .collect::<Result<Vec<_>, _>>()
        })
        .transpose()?;
    let service = match members.get("service") {
        None => None,
        Some(Value::Bool(any)) => Some(ServiceMatch::Any(*any)),
        Some(_) => Some(ServiceMatch::Deployables(
            names("service")
                .map_err(|_| ".service: true, false, or a deployable or a list of them".to_owned())?
                .unwrap_or_default(),
        )),
    };
    let behavior = match members.get("behavior") {
        None => None,
        Some(Value::Bool(any)) => Some(BehaviorMatch::Any(*any)),
        Some(_) => Some(BehaviorMatch::Names(
            names("behavior")
                .map_err(|_| ".behavior: true, false, or a behavior or a list of them".to_owned())?
                .unwrap_or_default(),
        )),
    };
    let decision = match (members.get("allow"), members.get("require")) {
        (Some(Value::Bool(allow)), None) => Decision::Allow(*allow),
        (Some(_), None) => return Err(".allow: true or false".to_owned()),
        (None, Some(_)) => Decision::Require(
            names("require")
                .map_err(|_| ".require: a permission or a non-empty list of them".to_owned())?
                .unwrap_or_default()
                .iter()
                .map(|text| {
                    PermissionTemplate::parse(text)
                        .map_err(|message| format!(".require: {message}"))
                })
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
    Ok(PolicyRule {
        action,
        operation: names("operation")?,
        schema: names("schema")?,
        namespace: names("namespace")?,
        behavior,
        subject: names("subject")?,
        service,
        stands_in: flag("standsIn")?,
        listing: flag("listing")?,
        decision,
    })
}

fn rule_json(rule: &PolicyRule) -> Value {
    let mut out = Map::new();
    let list = |items: &[String]| -> Value {
        match items {
            [one] => Value::from(one.as_str()),
            many => Value::Array(many.iter().map(|item| Value::from(item.as_str())).collect()),
        }
    };
    if let Some(actions) = &rule.action {
        let names: Vec<String> = actions
            .iter()
            .map(|action| action.as_str().to_owned())
            .collect();
        out.insert("action".into(), list(&names));
    }
    for (key, value) in [
        ("operation", &rule.operation),
        ("schema", &rule.schema),
        ("namespace", &rule.namespace),
        ("subject", &rule.subject),
    ] {
        if let Some(items) = value {
            out.insert(key.into(), list(items));
        }
    }
    match &rule.behavior {
        None => {}
        Some(BehaviorMatch::Any(any)) => {
            out.insert("behavior".into(), Value::Bool(*any));
        }
        Some(BehaviorMatch::Names(names)) => {
            out.insert("behavior".into(), list(names));
        }
    }
    match &rule.service {
        None => {}
        Some(ServiceMatch::Any(any)) => {
            out.insert("service".into(), Value::Bool(*any));
        }
        Some(ServiceMatch::Deployables(names)) => {
            out.insert("service".into(), list(names));
        }
    }
    if let Some(stands_in) = rule.stands_in {
        out.insert("standsIn".into(), Value::Bool(stands_in));
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

/// The policy an engine answers with: configured rules, or a Rust policy
/// compiled into the deployment's engine.
#[derive(Clone)]
pub enum Policy {
    Config(PolicyConfig),
    Custom(Arc<dyn AccessPolicy>),
}

impl Policy {
    /// `"allowAll"`.
    pub fn allow_all() -> Self {
        Policy::Config(PolicyConfig::Shorthand(PolicyShorthand::AllowAll))
    }

    /// `"permission"`.
    pub fn permission() -> Self {
        Policy::Config(PolicyConfig::Shorthand(PolicyShorthand::Permission))
    }

    /// A policy read from JSON ([`PolicyConfig::from_json`]); a policy the
    /// engine cannot read is refused as an option ([`Error::Usage`]).
    pub fn from_json(value: &Value) -> Result<Self, Error> {
        PolicyConfig::from_json(value)
            .map(Policy::Config)
            .map_err(Error::Usage)
    }

    /// A policy of the deployment's own.
    pub fn custom(policy: impl AccessPolicy + 'static) -> Self {
        Policy::Custom(Arc::new(policy))
    }
}

impl From<PolicyConfig> for Policy {
    fn from(config: PolicyConfig) -> Self {
        Policy::Config(config)
    }
}

impl fmt::Debug for Policy {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Policy::Config(config) => f.debug_tuple("Config").field(config).finish(),
            Policy::Custom(_) => f.write_str("Custom(..)"),
        }
    }
}

/// Configured rules, answering with the engine's permission matcher.
pub struct RulePolicy {
    rules: Vec<PolicyRule>,
    matcher: Arc<dyn PermissionMatcher>,
}

impl RulePolicy {
    pub fn new(config: &PolicyConfig, matcher: Arc<dyn PermissionMatcher>) -> Self {
        RulePolicy {
            rules: config.rules(),
            matcher,
        }
    }
}

impl AccessPolicy for RulePolicy {
    fn allows(&self, request: &AccessRequest<'_>) -> bool {
        self.rules
            .iter()
            .find(|rule| rule.matches(request))
            .is_some_and(|rule| rule.decide(request, self.matcher.as_ref()))
    }
}

/// Asks the policy and refuses with `forbidden`.
pub(crate) struct Access {
    policy: Arc<dyn AccessPolicy>,
}

impl Access {
    pub fn new(policy: Arc<dyn AccessPolicy>) -> Self {
        Access { policy }
    }

    /// The access of an engine: its policy, configured rules answering with
    /// its matcher.
    pub fn of(policy: &Policy, matcher: &Arc<dyn PermissionMatcher>) -> Self {
        match policy {
            Policy::Config(config) => {
                Access::new(Arc::new(RulePolicy::new(config, Arc::clone(matcher))))
            }
            Policy::Custom(custom) => Access::new(Arc::clone(custom)),
        }
    }

    fn ask(&self, request: &AccessRequest<'_>) -> bool {
        request.principal().is_system() || self.policy.allows(request)
    }

    /// Whether the policy allows the action on the schema.
    pub fn allows(
        &self,
        principal: &Principal,
        action: SchemaAction,
        namespace: &str,
        schema: &str,
    ) -> bool {
        self.allows_operation(principal, action, namespace, schema, None)
    }

    /// Whether the policy allows the action on the schema, for a behavior
    /// operation when `operation` names one.
    pub fn allows_operation(
        &self,
        principal: &Principal,
        action: SchemaAction,
        namespace: &str,
        schema: &str,
        operation: Option<&str>,
    ) -> bool {
        self.ask(&AccessRequest::Schema {
            principal,
            action,
            namespace,
            schema,
            operation,
        })
    }

    /// Refuses with `forbidden` unless the policy allows the action on the
    /// schema.
    pub fn require(
        &self,
        principal: &Principal,
        action: SchemaAction,
        namespace: &str,
        schema: &str,
    ) -> Result<(), EngineError> {
        self.require_operation(principal, action, namespace, schema, None)
    }

    /// Refuses with `forbidden` unless the policy allows the action on the
    /// schema, for a behavior operation when `operation` names one.
    pub fn require_operation(
        &self,
        principal: &Principal,
        action: SchemaAction,
        namespace: &str,
        schema: &str,
        operation: Option<&str>,
    ) -> Result<(), EngineError> {
        if self.allows_operation(principal, action, namespace, schema, operation) {
            return Ok(());
        }
        let what = match operation {
            None => format!("{} {schema}", action.as_str()),
            Some(operation) => format!("call {operation} ({}) on {schema}", action.as_str()),
        };
        Err(EngineError::new(
            EngineErrorCode::Forbidden,
            format!(
                "{} may not {what} in namespace {namespace}",
                principal.subject
            ),
        ))
    }

    /// Whether the policy allows the operation on the namespace.
    pub fn allows_manage(
        &self,
        principal: &Principal,
        namespace: &str,
        operation: NamespaceOperation,
    ) -> bool {
        self.ask(&AccessRequest::Namespace {
            principal,
            namespace,
            operation,
        })
    }

    /// Whether the policy lets the principal do what an engine tool does at
    /// all, for the tools document (milestone 6).
    #[allow(dead_code)] // the tools document asks it (milestone 6)
    pub fn allows_listing(
        &self,
        principal: &Principal,
        action: ListingAction,
        namespace: &str,
        operation: Option<NamespaceOperation>,
    ) -> bool {
        self.ask(&AccessRequest::Listing {
            principal,
            action,
            namespace,
            operation,
        })
    }

    /// Whether the policy allows a behavior's namespace operation (D77),
    /// `listing` while the tools document lists its tool.
    pub fn allows_behavior(
        &self,
        principal: &Principal,
        action: SchemaAction,
        namespace: &str,
        behavior: &str,
        operation: &str,
        listing: bool,
    ) -> bool {
        self.ask(&AccessRequest::Behavior {
            principal,
            action,
            namespace,
            behavior,
            operation,
            listing,
        })
    }

    /// Refuses with `forbidden` unless the policy allows a behavior's
    /// namespace operation (D77).
    pub fn require_behavior(
        &self,
        principal: &Principal,
        action: SchemaAction,
        namespace: &str,
        behavior: &str,
        operation: &str,
    ) -> Result<(), EngineError> {
        if self.allows_behavior(principal, action, namespace, behavior, operation, false) {
            return Ok(());
        }
        Err(EngineError::new(
            EngineErrorCode::Forbidden,
            format!(
                "{} may not call {behavior}'s {operation} ({}) in namespace {namespace}",
                principal.subject,
                action.as_str()
            ),
        ))
    }

    /// Refuses with `forbidden` unless the policy allows the operation on
    /// the namespace.
    pub fn require_manage(
        &self,
        principal: &Principal,
        namespace: &str,
        operation: NamespaceOperation,
    ) -> Result<(), EngineError> {
        if self.allows_manage(principal, namespace, operation) {
            return Ok(());
        }
        Err(EngineError::new(
            EngineErrorCode::Forbidden,
            format!(
                "{} may not {} namespace {namespace}",
                principal.subject,
                operation.as_str()
            ),
        ))
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::principal::PrincipalService;
    use serde_json::json;

    struct Nothing;

    impl AccessPolicy for Nothing {
        fn allows(&self, _request: &AccessRequest<'_>) -> bool {
            false
        }
    }

    fn strings(values: &[&str]) -> Vec<String> {
        values.iter().map(|value| (*value).to_owned()).collect()
    }

    fn rules(policy: Value) -> RulePolicy {
        RulePolicy::new(
            &PolicyConfig::from_json(&policy).unwrap(),
            Arc::new(DottedPermissions),
        )
    }

    fn schema<'a>(
        principal: &'a Principal,
        action: SchemaAction,
        name: &'a str,
        operation: Option<&'a str>,
    ) -> AccessRequest<'a> {
        AccessRequest::Schema {
            principal,
            action,
            namespace: "default",
            schema: name,
            operation,
        }
    }

    #[test]
    fn a_refusal_is_forbidden_and_the_system_principal_is_never_refused() {
        let access = Access::new(Arc::new(Nothing));
        let alice = Principal::new("alice", Vec::<String>::new());
        let refused = access
            .require(&alice, SchemaAction::Define, "default", "Order")
            .unwrap_err();
        assert_eq!(refused.code(), EngineErrorCode::Forbidden);
        assert_eq!(
            refused.message(),
            "alice may not define Order in namespace default"
        );
        assert_eq!(
            access
                .require_operation(
                    &alice,
                    SchemaAction::Write,
                    "default",
                    "Order",
                    Some("transition")
                )
                .unwrap_err()
                .message(),
            "alice may not call transition (write) on Order in namespace default"
        );
        assert_eq!(
            access
                .require_manage(&alice, "acme", NamespaceOperation::Archive)
                .unwrap_err()
                .message(),
            "alice may not archive namespace acme"
        );
        assert!(access
            .require(
                &Principal::system(),
                SchemaAction::Publish,
                "default",
                "Order"
            )
            .is_ok());
        assert!(Access::of(
            &Policy::allow_all(),
            &(Arc::new(DottedPermissions) as Arc<dyn PermissionMatcher>)
        )
        .allows(&alice, SchemaAction::Write, "default", "Order"));
    }

    #[test]
    fn a_permission_covers_itself_and_what_is_nested_under_it() {
        assert!(covers("reports", "reports"));
        assert!(covers("reports", "reports.export"));
        assert!(!covers("report", "reports.export"));
        assert!(!covers("reports.export", "reports"));
        assert!(!covers("", "reports"));
        assert!(covers("", ".x"));
        assert!(has_any_permission(&[], &[]));
        assert!(has_any_permission(
            &strings(&["orders"]),
            &strings(&["orders.read"])
        ));
        assert!(has_any_permission(
            &strings(&["billing", "orders.read"]),
            &strings(&["x", "orders.read.one"])
        ));
        assert!(!has_any_permission(
            &strings(&["orders.read"]),
            &strings(&["orders"])
        ));
        assert!(!has_any_permission(&[], &strings(&["orders"])));
    }

    #[test]
    fn the_permission_shorthand_is_permission_policy() {
        let policy = rules(json!("permission"));
        let holder = Principal::new("bob", ["Task"]);
        let reader = Principal::new("carol", ["Task.read"]);
        assert!(policy.allows(&schema(&holder, SchemaAction::Publish, "Task", None)));
        assert!(policy.allows(&schema(
            &holder,
            SchemaAction::Write,
            "Task",
            Some("transition")
        )));
        assert!(policy.allows(&schema(&reader, SchemaAction::Read, "Task", None)));
        assert!(!policy.allows(&schema(&reader, SchemaAction::Write, "Task", None)));
        assert!(!policy.allows(&schema(&holder, SchemaAction::Read, "Note", None)));
        // A question that names no schema is refused, whatever is held.
        let everything = Principal::new("root", ["", "Task", "manage", "default"]);
        assert!(!policy.allows(&AccessRequest::Namespace {
            principal: &everything,
            namespace: "acme",
            operation: NamespaceOperation::Create,
        }));
        assert!(!policy.allows(&AccessRequest::Listing {
            principal: &everything,
            action: ListingAction::Define,
            namespace: "default",
            operation: None,
        }));
    }

    fn behavior_question<'a>(
        principal: &'a Principal,
        action: SchemaAction,
        operation: &'a str,
        listing: bool,
    ) -> AccessRequest<'a> {
        AccessRequest::Behavior {
            principal,
            action,
            namespace: "default",
            behavior: "Comments",
            operation,
            listing,
        }
    }

    #[test]
    fn a_behavior_question_names_no_schema_and_the_permission_shorthand_answers_it() {
        let policy = rules(json!("permission"));
        let nobody = Principal::new("ann", Vec::<String>::new());
        let claimer = Principal::new("nate", ["Comments.claimDeliveries"]);
        let question = behavior_question(&nobody, SchemaAction::Read, "inbox", false);
        assert_eq!(
            question.to_json(Value::from("ann")),
            json!({"principal": "ann", "action": "read", "namespace": "default", "behavior": "Comments", "operation": "inbox"})
        );
        assert_eq!(question.schema(), None);
        assert!(policy.allows(&question));
        for listing in [false, true] {
            assert!(policy.allows(&behavior_question(
                &nobody,
                SchemaAction::Read,
                "inbox",
                listing
            )));
            assert!(!policy.allows(&behavior_question(
                &nobody,
                SchemaAction::Write,
                "claimDeliveries",
                listing
            )));
            assert!(policy.allows(&behavior_question(
                &claimer,
                SchemaAction::Write,
                "claimDeliveries",
                listing
            )));
            assert!(!policy.allows(&behavior_question(
                &claimer,
                SchemaAction::Write,
                "settleDeliveries",
                listing
            )));
        }
        assert_eq!(
            behavior_question(&nobody, SchemaAction::Read, "inbox", true).to_json(Value::Null)
                ["listing"],
            json!(true)
        );
        // The rules match a behavior question by its behavior, and a rule
        // written for schemas, {schema}.{action}, refuses it.
        let rules = rules(json!({"rules": [
            {"behavior": ["Other"], "allow": true},
            {"behavior": true, "action": "write", "require": "{behavior}.{operation}"},
            {"behavior": false, "action": "read", "allow": true},
            {"require": "{schema}.{action}"}
        ]}));
        let everything = Principal::new("root", ["Comments"]);
        assert!(rules.allows(&behavior_question(
            &everything,
            SchemaAction::Write,
            "claimDeliveries",
            false
        )));
        assert!(!rules.allows(&behavior_question(
            &everything,
            SchemaAction::Read,
            "inbox",
            false
        )));
        assert!(rules.allows(&schema(&nobody, SchemaAction::Read, "Task", None)));
        assert!(Access::of(
            &Policy::allow_all(),
            &(Arc::new(DottedPermissions) as Arc<dyn PermissionMatcher>)
        )
        .allows_behavior(
            &nobody,
            SchemaAction::Write,
            "default",
            "Comments",
            "claimDeliveries",
            false
        ));
        assert_eq!(
            Access::new(Arc::new(Nothing))
                .require_behavior(
                    &nobody,
                    SchemaAction::Write,
                    "default",
                    "Comments",
                    "claimDeliveries"
                )
                .unwrap_err()
                .message(),
            "ann may not call Comments's claimDeliveries (write) in namespace default"
        );
    }

    #[test]
    fn the_first_rule_that_matches_decides_and_none_refuses() {
        let policy = rules(json!({"rules": [
            {"subject": "mallory", "allow": false},
            {"action": "read", "schema": ["Order", "Note"], "allow": true},
            {"action": "write", "service": "worker", "standsIn": true, "schema": "Order", "allow": true},
            {"action": "manage", "operation": "list", "namespace": "tom", "allow": true},
            {"action": "manage", "require": "namespaces"},
            {"listing": true, "action": "define", "require": "schemas.define"},
            {"action": ["define", "publish"], "require": ["schemas.{action}", "{schema}.{action}"]}
        ]}));
        let anyone = Principal::new("ann", Vec::<String>::new());
        let mallory = Principal::new("mallory", ["Order"]);
        assert!(policy.allows(&schema(&anyone, SchemaAction::Read, "Note", None)));
        assert!(!policy.allows(&schema(&mallory, SchemaAction::Read, "Note", None)));
        assert!(!policy.allows(&schema(&anyone, SchemaAction::Read, "Other", None)));
        let worker = Principal::service("worker", &["jobs".into()], "sa-1");
        let for_bob = Principal::new("bob", Vec::<String>::new()).with_service(PrincipalService {
            deployable: "worker".into(),
            serves: vec![],
            subject: "sa-1".into(),
            stands_in: false,
        });
        assert!(policy.allows(&schema(&worker, SchemaAction::Write, "Order", None)));
        assert!(!policy.allows(&schema(&for_bob, SchemaAction::Write, "Order", None)));
        let manage = |principal, namespace, operation| AccessRequest::Namespace {
            principal,
            namespace,
            operation,
        };
        assert!(policy.allows(&manage(&anyone, "tom", NamespaceOperation::List)));
        assert!(!policy.allows(&manage(&anyone, "acme", NamespaceOperation::List)));
        let ada = Principal::new("ada", ["namespaces"]);
        assert!(policy.allows(&manage(&ada, "acme", NamespaceOperation::Archive)));
        let owner = Principal::new("owen", ["Order.publish"]);
        assert!(policy.allows(&schema(&owner, SchemaAction::Publish, "Order", None)));
        assert!(!policy.allows(&schema(&owner, SchemaAction::Define, "Order", None)));
        // A listing question names no schema: {schema} renders nothing, and
        // the rule written for listings answers it.
        let definer = Principal::new("dee", ["schemas.define"]);
        assert!(policy.allows(&AccessRequest::Listing {
            principal: &definer,
            action: ListingAction::Define,
            namespace: "default",
            operation: None,
        }));
        assert!(!policy.allows(&AccessRequest::Listing {
            principal: &owner,
            action: ListingAction::Define,
            namespace: "default",
            operation: None,
        }));
    }

    #[test]
    fn a_policy_reads_from_json_and_writes_back() {
        for policy in [
            json!("allowAll"),
            json!("permission"),
            json!({"rules": [
                {"action": ["define", "publish"], "require": "schemas.{action}"},
                {"action": "write", "operation": ["comment", "propose"], "require": ["{schema}.read", "x.{namespace}.{operation}"]},
                {"service": false, "listing": false, "subject": ["a", "b"], "allow": true},
                {"service": ["worker", "crawler"], "standsIn": true, "allow": false},
                {"behavior": true, "action": "read", "allow": true},
                {"behavior": ["Comments", "acme.Rating"], "require": "{behavior}.{operation}"},
                {"behavior": false, "allow": false}
            ]}),
        ] {
            let config: PolicyConfig = serde_json::from_value(policy.clone()).unwrap();
            assert_eq!(serde_json::to_value(&config).unwrap(), policy);
        }
        let refused = |policy: Value| PolicyConfig::from_json(&policy).unwrap_err();
        assert_eq!(
            refused(json!("allowNone")),
            "policy \"allowNone\" is not a shorthand: \"allowAll\" or \"permission\", or { \"rules\": [...] }"
        );
        assert_eq!(
            refused(json!({"rules": [{"allow": true}, {"action": "manag", "allow": true}]})),
            "policy: rules[1].action: \"manag\" is not one of read, write, define, publish, manage"
        );
        assert_eq!(
            refused(json!({"rules": [{"require": "{schema}.{verb}"}]})),
            "policy: rules[0].require: \"{schema}.{verb}\" names {verb}; a permission's placeholders are {schema}, {action}, {namespace}, {operation} and {behavior}"
        );
        assert_eq!(
            refused(json!({"rules": [{"action": "read"}]})),
            "policy: rules[0]: a rule decides with allow (true or false) or require (a permission)"
        );
        assert_eq!(
            refused(json!({"rules": [{"allow": true, "require": "x"}]})),
            "policy: rules[0]: a rule decides with allow or require, not both"
        );
        assert!(refused(json!({"rules": [{"verb": "read", "allow": true}]}))
            .starts_with("policy: rules[0]: \"verb\" is not a member of a rule"));
        assert_eq!(
            refused(json!({"rule": []})),
            "policy: \"rule\" is not a member of a policy, which is { \"rules\": [...] }"
        );
        assert_eq!(
            refused(json!(true)),
            "a policy is \"allowAll\", \"permission\" or { \"rules\": [...] }"
        );
        assert!(serde_json::from_value::<PolicyConfig>(json!({"rules": [{}]})).is_err());
    }
}
