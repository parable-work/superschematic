//! Declarative behaviors (D103's step 5): a behavior defined with no Rust.
//! Its declaration lists its fields, sub-tables and operations, as every
//! behavior's does, and its `rules` member states its rules in a fixed
//! vocabulary the core interprets. There are no loops and no expressions:
//! a rule is a condition, a list of fields, or an effect from a fixed list.
//!
//! The vocabulary, each member of `rules` optional:
//!
//! - `constant` and `oneWay`: the type's own fields a create sets for good,
//!   and the ones that move one way after it, as Constants has them, with
//!   `permission` the one a caller needs to change them.
//! - `states` and `status`: the own fields a state of a Workflow requires
//!   or freezes and who may change them (`by`), as StateRules has them,
//!   with `status` the Workflow use the states are of and `permission` the
//!   one a caller needs to change a frozen field.
//! - `guards`: conditions a create, an update, a delete or an operation,
//!   the behavior's own or another use's (`Lease.acquire`), must meet; one
//!   that does not is vetoed with the guard's code.
//! - `operations`: each declared operation's rules: a condition its
//!   parameters meet (`params`), the effects it has (`effects`) and what it
//!   returns (`result`).
//! - `reactions`: `{ when, then }`: when a write of the row, whoever makes
//!   it, leaves it meeting `when` and the row before the write did not,
//!   `then`'s effects run, each where it can.
//! - `derived`: owned fields that hold a count, a sum, a minimum, a maximum
//!   or an average over a sub-table's rows, kept as each operation writes
//!   the sub-table.
//! - `configChange`: how a new version may change a use's config.
//!
//! A condition is the ORM's filter (D90, D99), each member a field and its
//! operators (`eq`, `ne`, `gt`, `gte`, `lt`, `lte`, `between`, `in`,
//! `notIn`, `isNull`, `ilike`), ANDed, with `anyOf` and `allOf`, as
//! `@enqueueOnChange`'s `when` is a list of such tests. An operand is a
//! literal or one of `{ "param": name }`, `{ "config": name }`, `{
//! "caller": true }` (D104: the caller's subject, compared with a field
//! that holds a subject as Workflow's `by` compares one), `{ "now": true
//! }`, `{ "field": name }`, `{ "newKey": true }` and `{ "literal": value }`.
//! A rule's list or name may come from the use's config instead (`{
//! "config": "fields" }`), so a declaration states the rule and the config
//! its fields.
//!
//! The effects: `set` an owned field, `adjust` a counter by a number,
//! `upsert` a sub-table row (add one, or update the one its key holds),
//! `move` a Workflow status, and `update` the row's own fields, an
//! operation's alone. A declarative behavior's state is what any behavior's
//! is: owned fields and sub-tables the layout maps (D103), written only by
//! its operations. Its config and parameters are held to the declaration's
//! configSchema and paramsSchema ([`crate::ajv`]), so a declaration and the
//! check of it are one text.
//!
//! Constants' and StateRules' rules are each behavior's own code, which the
//! vocabulary hands the part of its config each reads
//! ([`superschematic_rules::View::with_config`]): a declaration whose rules
//! are `{ "constant": { "config": "fields" }, "oneWay": { "config":
//! "oneWay" }, "permission": { "config": "permission" } }` beside
//! Constants' configSchema is Constants, word for word.

use std::cmp::Ordering;
use std::collections::{BTreeMap, BTreeSet};
use std::sync::Arc;

use serde_json::{json, Map, Value};
use superschematic_rules::{
    Behavior, Code, ConfigCx, ConfigError, ConfigTarget, Cx, Declaration, GuardRequest, Heard,
    Hook, Refusal, RowImage, SchemaIssue, SubRow, ValidationIssue, ValidationRequest, Veto, View,
    WriteKind,
};

use crate::ajv;
use crate::constants::{Constants, ConstantsConfig};
use crate::declared;
use crate::flow::{self, flow_of};
use crate::rollups::number_json;
use crate::schema::{self, Issues};
use crate::staterules::{StateRules, StateRulesConfig};
use crate::subjects::{holds_subject, names_subject};
use crate::values::equal;
use crate::workflow;

/// The member of a declaration that makes it declarative.
pub const RULES: &str = "rules";

// The members `rules` takes.
const RULE_KEYS: [&str; 10] = [
    "configChange",
    "constant",
    "derived",
    "guards",
    "oneWay",
    "operations",
    "permission",
    "reactions",
    "states",
    "status",
];

// The operators of a condition's field, as the ORM's filter names them.
const OPERATORS: [&str; 11] = [
    "eq", "ne", "gt", "gte", "lt", "lte", "between", "in", "notIn", "isNull", "ilike",
];

// The kinds of an operand object, each its one member.
const OPERANDS: [&str; 7] = [
    "caller", "config", "field", "literal", "newKey", "now", "param",
];

// The functions a derived field computes over a sub-table.
const FUNCTIONS: [&str; 5] = ["count", "sum", "min", "max", "average"];

// The effects an operation or a reaction has.
const EFFECTS: [&str; 5] = ["adjust", "move", "set", "update", "upsert"];

// The veto a oneWay rule refuses a move back with, as Constants does.
const ONE_WAY: &str = "one_way";

// The vetoes a states rule refuses with, as StateRules does.
const STATE_VETOES: [&str; 3] = ["missing_fields", "frozen_fields", "required_fields"];

/// Whether a declaration file states its rules (a `rules` member): one the
/// core runs as a [`Declarative`] behavior.
pub fn is_declarative(document: &Value) -> bool {
    document.get(RULES).is_some()
}

/// A behavior whose declaration states its rules in the vocabulary: its
/// declaration file, as the core reads it, and the rules parsed from it.
#[derive(Clone, Debug)]
pub struct Declarative {
    file: Arc<Value>,
    declaration: Declaration,
    rules: Arc<Rules>,
}

impl Declarative {
    /// The behavior a declaration file declares, its rules checked against
    /// its own members: each operation, field, sub-table and veto a rule
    /// names is one the file declares, and each declared operation has
    /// rules. A file with no `rules`, or rules the vocabulary does not
    /// read, is refused with the reason.
    pub fn from_document(document: &Value) -> Result<Declarative, String> {
        let name = document
            .get("name")
            .and_then(Value::as_str)
            .filter(|name| !name.is_empty())
            .ok_or("a declaration names its behavior (name)")?;
        let fail = |reason: String| format!("behavior {name}: {reason}");
        let Some(raw) = document.get(RULES) else {
            return Err(fail(
                "its declaration states no rules: a declarative behavior's has a rules member"
                    .to_owned(),
            ));
        };
        let declaration = declared::declaration_of(document);
        let rules = Rules::parse(raw, document, &declaration).map_err(fail)?;
        Ok(Declarative {
            file: Arc::new(document.clone()),
            declaration,
            rules: Arc::new(rules),
        })
    }

    /// The behavior a declaration file's text declares
    /// ([`Declarative::from_document`]).
    pub fn from_text(text: &str) -> Result<Declarative, String> {
        let document: Value =
            serde_json::from_str(text).map_err(|error| format!("a declaration file: {error}"))?;
        Declarative::from_document(&document)
    }

    /// The behavior's name.
    pub fn name(&self) -> &str {
        &self.declaration.name
    }

    /// The declaration file, as given: what a host makes its layout from.
    pub fn document(&self) -> &Value {
        &self.file
    }
}

/// A use's config of a declarative behavior, parsed: the config as given,
/// and what its rules read of it and of the type.
#[derive(Debug)]
pub struct DeclarativeConfig {
    raw: Value,
    constants: Option<ConstantsConfig>,
    states: Option<StateRulesConfig>,
    /// The Workflow use each `move` moves, by the status it names (none
    /// for the type's one), with its transitions.
    moves: BTreeMap<Option<String>, Moved>,
    /// What each field a condition or an operand names reads.
    members: BTreeMap<String, Member>,
}

impl DeclarativeConfig {
    /// The config as the use gives it (`{}` when it gives none).
    pub fn raw(&self) -> &Value {
        &self.raw
    }
}

#[derive(Clone, Debug)]
struct Moved {
    use_id: String,
    transitions: Vec<(String, String)>,
}

/// What a field a rule names reads, and the IR type it holds.
#[derive(Clone, Debug, PartialEq, Eq)]
struct Member {
    read: Read,
    type_name: String,
}

#[derive(Clone, Debug, PartialEq, Eq)]
enum Read {
    /// One of the behavior's own owned fields.
    Owned(String),
    /// An owned field of another use of the row, by its use id.
    OwnedOf(String, String),
    /// An own field of the row.
    Own(String),
}

// ---- the rules ----

#[derive(Debug, Default)]
struct Rules {
    constant: Option<Given>,
    one_way: Option<Given>,
    permission: Option<Given>,
    states: Option<Given>,
    status: Option<Given>,
    guards: Vec<Guard>,
    operations: BTreeMap<String, OperationRules>,
    reactions: Vec<Reaction>,
    derived: Vec<(String, Aggregate)>,
    config_change: ConfigChange,
}

/// A rule's list or name: the declaration's own, or a member of the use's
/// config.
#[derive(Clone, Debug, PartialEq)]
enum Given {
    Literal(Value),
    Config(String),
}

#[derive(Clone, Debug)]
struct Guard {
    on: Vec<String>,
    require: Condition,
    veto: String,
    message: Option<String>,
}

#[derive(Clone, Debug, Default)]
struct OperationRules {
    params: Option<Condition>,
    effects: Vec<Effect>,
    result: Option<Returned>,
}

#[derive(Clone, Debug)]
enum Returned {
    Members(Vec<(String, Operand)>),
    One(Operand),
}

#[derive(Clone, Debug)]
struct Reaction {
    when: Condition,
    then: Vec<Effect>,
}

#[derive(Clone, Debug)]
struct Aggregate {
    function: Function,
    table: String,
    field: Option<String>,
    filter: Option<Condition>,
    empty: Option<Value>,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum Function {
    Count,
    Sum,
    Min,
    Max,
    Average,
}

#[derive(Clone, Debug, Default)]
enum ConfigChange {
    /// Only an identical config, as the core's default.
    #[default]
    Identical,
    /// Any config.
    Any,
    /// Members that move one way, every other member as it was.
    OneWay(Vec<(String, Direction)>),
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum Direction {
    Up,
    Down,
}

#[derive(Clone, Debug)]
enum Effect {
    Set(Vec<(String, Operand)>),
    Adjust(Vec<(String, Operand)>),
    Upsert {
        table: String,
        key: Operand,
        values: Vec<(String, Operand)>,
    },
    Move {
        status: Option<String>,
        to: String,
    },
    Update(Vec<(String, Operand)>),
}

#[derive(Clone, Debug, PartialEq)]
enum Operand {
    Literal(Value),
    Param(String),
    Config(String),
    Caller,
    Now,
    Field(String),
    NewKey,
}

/// A condition in the ORM's filter grammar: each field's tests, ANDed,
/// then each of `allOf`, then one of `anyOf`.
#[derive(Clone, Debug, Default)]
struct Condition {
    tests: Vec<(String, Vec<Test>)>,
    all_of: Vec<Condition>,
    any_of: Vec<Condition>,
}

#[derive(Clone, Debug)]
enum Test {
    Compare(Op, Operand),
    Between(Operand, Operand),
    In(Vec<Operand>, bool),
    InGiven(Operand, bool),
    IsNull(bool),
    Ilike(Operand),
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum Op {
    Eq,
    Ne,
    Gt,
    Gte,
    Lt,
    Lte,
}

impl Op {
    fn parse(text: &str) -> Option<Op> {
        Some(match text {
            "eq" => Op::Eq,
            "ne" => Op::Ne,
            "gt" => Op::Gt,
            "gte" => Op::Gte,
            "lt" => Op::Lt,
            "lte" => Op::Lte,
            _ => return None,
        })
    }
}

// What a rule may name, for the checks of a declaration's rules.
struct Declared<'d> {
    file: &'d Value,
    declaration: &'d Declaration,
}

impl Declared<'_> {
    // field_type is a declared field's type; None for a field with none.
    fn field_type(&self, name: &str) -> Option<&str> {
        listed(self.file, "fields")
            .find(|field| field.get("name").and_then(Value::as_str) == Some(name))
            .and_then(|field| field.get("type"))
            .and_then(Value::as_str)
    }

    fn table_field_type(&self, table: &str, field: &str) -> Option<String> {
        let found = listed(self.file, "tables")
            .find(|declared| declared.get("name").and_then(Value::as_str) == Some(table))?;
        if found.get("key").and_then(Value::as_str) == Some(field) {
            return Some("string".to_owned());
        }
        listed(found, "fields")
            .find(|declared| declared.get("name").and_then(Value::as_str) == Some(field))
            .and_then(|declared| declared.get("type"))
            .and_then(Value::as_str)
            .map(str::to_owned)
    }
}

impl Rules {
    fn parse(raw: &Value, file: &Value, declaration: &Declaration) -> Result<Rules, String> {
        let Some(members) = raw.as_object() else {
            return Err("rules is an object".to_owned());
        };
        for key in members.keys() {
            if !RULE_KEYS.contains(&key.as_str()) {
                return Err(format!(
                    "rules has no member {key} (its members: {})",
                    sorted(&RULE_KEYS)
                ));
            }
        }
        let declared = Declared { file, declaration };
        if file
            .get("createParamsSchema")
            .is_some_and(|schema| !schema.is_null())
        {
            return Err(
                "a declarative behavior takes no create parameters (createParamsSchema)".to_owned(),
            );
        }
        if file
            .get("preconditionSchema")
            .is_some_and(|schema| !schema.is_null())
        {
            return Err(
                "a declarative behavior takes no precondition (preconditionSchema)".to_owned(),
            );
        }
        if declaration.arms_deadlines {
            return Err("a declarative behavior arms no deadlines (armsDeadlines)".to_owned());
        }
        if !declaration.events.is_empty() || !declaration.signals.is_empty() {
            return Err("a declarative behavior emits no events and sends no signals".to_owned());
        }
        if !declaration.contracts.is_empty() || !declaration.asks.is_empty() {
            return Err("a declarative behavior implements and asks no contract".to_owned());
        }
        let mut rules = Rules {
            constant: given(members.get("constant"), "rules.constant", Shape::List)?,
            one_way: given(members.get("oneWay"), "rules.oneWay", Shape::Object)?,
            permission: given(members.get("permission"), "rules.permission", Shape::Text)?,
            states: given(members.get("states"), "rules.states", Shape::Object)?,
            status: given(members.get("status"), "rules.status", Shape::Text)?,
            ..Rules::default()
        };
        // The codes Constants' and StateRules' rules veto with are the
        // declaration's to list, as any veto it refuses with is.
        let vetoes: &[(&str, &[&str])] = &[("oneWay", &[ONE_WAY]), ("states", &STATE_VETOES)];
        for (rule, codes) in vetoes {
            if !members.contains_key(*rule) {
                continue;
            }
            for code in *codes {
                if !declaration.vetoes.iter().any(|known| known == code) {
                    return Err(format!(
                        "rules.{rule} vetoes with {code}, a code its declaration does not list (vetoes)"
                    ));
                }
            }
        }
        for (i, guard) in listed(raw, "guards").enumerate() {
            rules.guards.push(Guard::parse(
                guard,
                &format!("rules.guards[{i}]"),
                &declared,
            )?);
        }
        if members
            .get("guards")
            .is_some_and(|guards| !guards.is_array())
        {
            return Err("rules.guards is a list".to_owned());
        }
        let empty = Map::new();
        let operations = match members.get("operations") {
            None => &empty,
            Some(Value::Object(operations)) => operations,
            Some(_) => return Err("rules.operations is an object, by operation".to_owned()),
        };
        for operation in &declaration.operations {
            if operation.table || operation.namespace {
                return Err(format!(
                    "operation {} is of the {}: a declarative behavior's operations are of a row",
                    operation.name,
                    if operation.table {
                        "table as a whole"
                    } else {
                        "namespace"
                    }
                ));
            }
            if !operations.contains_key(&operation.name) {
                return Err(format!(
                    "it declares operation {} and rules.operations states none for it",
                    operation.name
                ));
            }
        }
        for (name, stated) in operations {
            let Some(operation) = declaration.find_operation(name) else {
                return Err(format!(
                    "rules.operations.{name} names no operation it declares ({})",
                    listed_names(&declaration.operation_names(false))
                ));
            };
            let parsed =
                OperationRules::parse(stated, &format!("rules.operations.{name}"), &declared)?;
            if !operation.writes && !parsed.effects.is_empty() {
                return Err(format!(
                    "rules.operations.{name} has effects, and {name} does not write (writes)"
                ));
            }
            rules.operations.insert(name.clone(), parsed);
        }
        if members
            .get("reactions")
            .is_some_and(|reactions| !reactions.is_array())
        {
            return Err("rules.reactions is a list".to_owned());
        }
        for (i, reaction) in listed(raw, "reactions").enumerate() {
            let at = format!("rules.reactions[{i}]");
            let Some(stated) = reaction.as_object() else {
                return Err(format!("{at} is an object: {{ when, then }}"));
            };
            unknown(&at, stated, &["then", "when"])?;
            let when = Condition::parse(
                stated.get("when").ok_or(format!("{at} has a when"))?,
                &format!("{at}.when"),
            )?;
            let then = effects(stated.get("then"), &format!("{at}.then"), &declared)?;
            if then.is_empty() {
                return Err(format!("{at}.then lists at least one effect"));
            }
            if then
                .iter()
                .any(|effect| matches!(effect, Effect::Update(_)))
            {
                return Err(format!(
                    "{at}.then updates the row's own fields, which an operation alone does"
                ));
            }
            rules.reactions.push(Reaction { when, then });
        }
        if !rules.reactions.is_empty() && !declaration.hears_changes {
            return Err(
                "rules.reactions hear the row's writes, and its declaration does not (hearsChanges)"
                    .to_owned(),
            );
        }
        match members.get("derived") {
            None => {}
            Some(Value::Object(derived)) => {
                for (field, stated) in derived {
                    let at = format!("rules.derived.{field}");
                    rules.derived.push((
                        field.clone(),
                        Aggregate::parse(field, stated, &at, &declared)?,
                    ));
                }
            }
            Some(_) => return Err("rules.derived is an object, by field".to_owned()),
        }
        rules.config_change = match members.get("configChange") {
            None => ConfigChange::Identical,
            Some(Value::String(any)) if any == "any" => ConfigChange::Any,
            Some(Value::Object(stated)) => {
                unknown("rules.configChange", stated, &["oneWay"])?;
                let mut moves = Vec::new();
                for (member, direction) in stated
                    .get("oneWay")
                    .and_then(Value::as_object)
                    .into_iter()
                    .flatten()
                {
                    let direction = match direction.as_str() {
                        Some("up") => Direction::Up,
                        Some("down") => Direction::Down,
                        _ => {
                            return Err(format!("rules.configChange.oneWay.{member} is up or down"))
                        }
                    };
                    moves.push((member.clone(), direction));
                }
                ConfigChange::OneWay(moves)
            }
            Some(_) => {
                return Err(
                    "rules.configChange is \"any\" or { oneWay: { <member>: up | down } }"
                        .to_owned(),
                )
            }
        };
        Ok(rules)
    }

    // keeps_state says whether the behavior keeps state of its own: an
    // owned field or a sub-table.
    fn keeps_state(declaration: &Declaration) -> bool {
        !declaration.fields.is_empty() || !declaration.tables.is_empty()
    }

    // members is every row field a condition or an operand of the rules
    // names, once, in the order the rules name them.
    fn members(&self) -> Vec<String> {
        let mut out: Vec<String> = Vec::new();
        let mut add = |name: &str| {
            if !out.iter().any(|known| known == name) {
                out.push(name.to_owned());
            }
        };
        for guard in &self.guards {
            guard.require.members(&mut add, true);
        }
        for reaction in &self.reactions {
            reaction.when.members(&mut add, true);
            for effect in &reaction.then {
                effect.members(&mut add);
            }
        }
        for rules in self.operations.values() {
            if let Some(params) = &rules.params {
                params.members(&mut add, false);
            }
            for effect in &rules.effects {
                effect.members(&mut add);
            }
            match &rules.result {
                Some(Returned::Members(members)) => {
                    for (_, operand) in members {
                        operand.members(&mut add);
                    }
                }
                Some(Returned::One(operand)) => operand.members(&mut add),
                None => {}
            }
        }
        for (_, aggregate) in &self.derived {
            if let Some(filter) = &aggregate.filter {
                filter.members(&mut add, false);
            }
        }
        out
    }

    // moves is every status a move effect names (none for the type's one
    // Workflow) with the states it moves to.
    fn moves(&self) -> Vec<(Option<String>, Vec<String>, String)> {
        let mut out: Vec<(Option<String>, Vec<String>, String)> = Vec::new();
        let mut add = |effects: &[Effect], at: String| {
            for effect in effects {
                if let Effect::Move { status, to } = effect {
                    match out.iter_mut().find(|(known, _, _)| known == status) {
                        Some((_, states, _)) => states.push(to.clone()),
                        None => out.push((status.clone(), vec![to.clone()], at.clone())),
                    }
                }
            }
        };
        for (name, rules) in &self.operations {
            add(&rules.effects, format!("rules.operations.{name}"));
        }
        for (i, reaction) in self.reactions.iter().enumerate() {
            add(&reaction.then, format!("rules.reactions[{i}]"));
        }
        out
    }
}

#[derive(Clone, Copy)]
enum Shape {
    List,
    Object,
    Text,
}

// given reads a rule's list or name: the declaration's own, of its shape,
// or `{ "config": name }`.
fn given(value: Option<&Value>, at: &str, shape: Shape) -> Result<Option<Given>, String> {
    let Some(value) = value else {
        return Ok(None);
    };
    if let Some(member) = value.as_object().and_then(|object| {
        (object.len() == 1)
            .then(|| object.get("config"))
            .flatten()
            .and_then(Value::as_str)
    }) {
        return Ok(Some(Given::Config(member.to_owned())));
    }
    let fits = match shape {
        Shape::List => value.is_array(),
        Shape::Object => value.is_object(),
        Shape::Text => value.is_string(),
    };
    if !fits {
        let what = match shape {
            Shape::List => "a list",
            Shape::Object => "an object",
            Shape::Text => "a string",
        };
        return Err(format!(
            "{at} is {what}, or {{ \"config\": <member> }} for the use's config's"
        ));
    }
    Ok(Some(Given::Literal(value.clone())))
}

impl Given {
    // of is the value the rule reads: its own, or the config's member,
    // none when the config lacks it.
    fn of(&self, config: &Value) -> Option<Value> {
        match self {
            Given::Literal(value) => Some(value.clone()),
            Given::Config(member) => config_member(config, member).cloned(),
        }
        .filter(|value| !value.is_null())
    }
}

// config_member reads a member of a use's config: a JSON pointer when it
// starts with `/`, else the member of that name.
fn config_member<'c>(config: &'c Value, member: &str) -> Option<&'c Value> {
    if member.starts_with('/') {
        config.pointer(member)
    } else {
        config.get(member)
    }
}

impl Guard {
    fn parse(raw: &Value, at: &str, declared: &Declared<'_>) -> Result<Guard, String> {
        let Some(stated) = raw.as_object() else {
            return Err(format!(
                "{at} is an object: {{ on, require, veto, message? }}"
            ));
        };
        unknown(at, stated, &["message", "on", "require", "veto"])?;
        let on: Vec<String> = match stated.get("on") {
            Some(Value::String(one)) => vec![one.clone()],
            Some(Value::Array(many)) if !many.is_empty() => many
                .iter()
                .map(|one| {
                    one.as_str()
                        .map(str::to_owned)
                        .ok_or(format!("{at}.on lists names"))
                })
                .collect::<Result<_, _>>()?,
            _ => {
                return Err(format!(
                    "{at}.on names what it guards: create, update, delete, an operation of its own, or <use>.<operation>"
                ))
            }
        };
        for action in &on {
            let own = !action.contains('.');
            let known = ["create", "update", "delete"].contains(&action.as_str())
                || declared.declaration.find_operation(action).is_some();
            if own && !known {
                return Err(format!(
                    "{at}.on names {action}, which is neither create, update, delete nor an operation it declares"
                ));
            }
        }
        let veto = stated
            .get("veto")
            .and_then(Value::as_str)
            .ok_or(format!("{at}.veto names the code it refuses with"))?
            .to_owned();
        if !declared.declaration.vetoes.contains(&veto) {
            return Err(format!(
                "{at}.veto is {veto}, a code its declaration does not list (vetoes)"
            ));
        }
        let require = Condition::parse(
            stated.get("require").ok_or(format!("{at} has a require"))?,
            &format!("{at}.require"),
        )?;
        let message = match stated.get("message") {
            None => None,
            Some(Value::String(text)) => Some(text.clone()),
            Some(_) => return Err(format!("{at}.message is text")),
        };
        Ok(Guard {
            on,
            require,
            veto,
            message,
        })
    }

    // guards says whether the guard is asked about a request.
    fn guards(&self, use_id: &str, request: &GuardRequest<'_>) -> bool {
        self.on.iter().any(|action| match request {
            GuardRequest::Create { .. } => action == "create",
            GuardRequest::Update { .. } => action == "update",
            GuardRequest::Delete => action == "delete",
            GuardRequest::Operation {
                use_id: asked,
                operation,
                ..
            } => match action.split_once('.') {
                Some((of, name)) => of == *asked && name == *operation,
                None => *asked == use_id && action == operation,
            },
            _ => false,
        })
    }
}

impl OperationRules {
    fn parse(raw: &Value, at: &str, declared: &Declared<'_>) -> Result<OperationRules, String> {
        let Some(stated) = raw.as_object() else {
            return Err(format!(
                "{at} is an object: {{ params?, effects?, result? }}"
            ));
        };
        unknown(at, stated, &["effects", "params", "result"])?;
        let params = stated
            .get("params")
            .map(|params| Condition::parse(params, &format!("{at}.params")))
            .transpose()?;
        if let Some(params) = &params {
            params.only_given(&format!("{at}.params"))?;
        }
        let effects = effects(stated.get("effects"), &format!("{at}.effects"), declared)?;
        let result = match stated.get("result") {
            None => None,
            Some(Value::Object(members)) if operand_kind(members).is_none() => {
                let mut out = Vec::new();
                for (name, operand) in members {
                    out.push((
                        name.clone(),
                        Operand::parse(operand, &format!("{at}.result.{name}"))?,
                    ));
                }
                Some(Returned::Members(out))
            }
            Some(one) => Some(Returned::One(Operand::parse(one, &format!("{at}.result"))?)),
        };
        Ok(OperationRules {
            params,
            effects,
            result,
        })
    }
}

fn effects(raw: Option<&Value>, at: &str, declared: &Declared<'_>) -> Result<Vec<Effect>, String> {
    let Some(raw) = raw else {
        return Ok(Vec::new());
    };
    let Some(list) = raw.as_array() else {
        return Err(format!("{at} is a list of effects"));
    };
    list.iter()
        .enumerate()
        .map(|(i, effect)| Effect::parse(effect, &format!("{at}[{i}]"), declared))
        .collect()
}

impl Effect {
    fn parse(raw: &Value, at: &str, declared: &Declared<'_>) -> Result<Effect, String> {
        let Some((kind, body)) = raw
            .as_object()
            .filter(|object| object.len() == 1)
            .and_then(|object| object.iter().next())
        else {
            return Err(format!(
                "{at} is one effect: an object of one of {}",
                sorted(&EFFECTS)
            ));
        };
        let at = format!("{at}.{kind}");
        let fields = |body: &Value, owned: bool| -> Result<Vec<(String, Operand)>, String> {
            let Some(members) = body.as_object().filter(|members| !members.is_empty()) else {
                return Err(format!("{at} is an object of fields and their values"));
            };
            let mut out = Vec::new();
            for (field, operand) in members {
                if owned {
                    if !declared.declaration.has_field(field) {
                        return Err(format!(
                            "{at} sets {field}, which is not a field it declares ({})",
                            listed_names(&declared.declaration.fields)
                        ));
                    }
                    if declared.declaration.is_computed(field) {
                        return Err(format!(
                            "{at} sets {field}, a field with no type, which no column holds"
                        ));
                    }
                }
                out.push((
                    field.clone(),
                    Operand::parse(operand, &format!("{at}.{field}"))?,
                ));
            }
            Ok(out)
        };
        match kind.as_str() {
            "set" => Ok(Effect::Set(fields(body, true)?)),
            "adjust" => {
                let adjusted = fields(body, true)?;
                for (field, _) in &adjusted {
                    let numeric = declared
                        .field_type(field)
                        .is_some_and(|type_name| NUMBERS.contains(&type_name));
                    if !numeric {
                        return Err(format!(
                            "{at} adjusts {field}, which holds no number ({})",
                            NUMBERS.join(", ")
                        ));
                    }
                }
                Ok(Effect::Adjust(adjusted))
            }
            "update" => Ok(Effect::Update(fields(body, false)?)),
            "move" => {
                let Some(stated) = body.as_object() else {
                    return Err(format!("{at} is an object: {{ to, status? }}"));
                };
                unknown(&at, stated, &["status", "to"])?;
                let to = stated
                    .get("to")
                    .and_then(Value::as_str)
                    .ok_or(format!("{at}.to names a state"))?
                    .to_owned();
                let status = match stated.get("status") {
                    None => None,
                    Some(Value::String(status)) => Some(status.clone()),
                    Some(_) => return Err(format!("{at}.status names a Workflow use")),
                };
                Ok(Effect::Move { status, to })
            }
            "upsert" => {
                let Some(stated) = body.as_object() else {
                    return Err(format!("{at} is an object: {{ table, key, values }}"));
                };
                unknown(&at, stated, &["key", "table", "values"])?;
                let table = stated
                    .get("table")
                    .and_then(Value::as_str)
                    .ok_or(format!("{at}.table names a sub-table"))?;
                let Some(found) = declared.declaration.find_table(table) else {
                    return Err(format!(
                        "{at}.table is {table}, which is not a sub-table it declares ({})",
                        listed_names(&declared.declaration.table_names())
                    ));
                };
                if found.shared {
                    return Err(format!(
                        "{at}.table is {table}, a table kept for the table as a whole, which no upsert writes"
                    ));
                }
                let key = Operand::parse(
                    stated.get("key").ok_or(format!("{at} has a key"))?,
                    &format!("{at}.key"),
                )?;
                let values = match stated.get("values") {
                    None => Vec::new(),
                    Some(values) => {
                        let Some(members) = values.as_object() else {
                            return Err(format!("{at}.values is an object of the row's fields"));
                        };
                        let mut out = Vec::new();
                        for (field, operand) in members {
                            if found.key.as_deref() == Some(field.as_str()) {
                                return Err(format!(
                                    "{at}.values names {field}, the row's key, which key gives"
                                ));
                            }
                            if found.is_checked() && !found.fields.contains(field) {
                                return Err(format!(
                                    "{at}.values names {field}, which {table} has no field of (its fields: {})",
                                    listed_names(&found.fields)
                                ));
                            }
                            out.push((
                                field.clone(),
                                Operand::parse(operand, &format!("{at}.values.{field}"))?,
                            ));
                        }
                        out
                    }
                };
                Ok(Effect::Upsert {
                    table: table.to_owned(),
                    key,
                    values,
                })
            }
            other => Err(format!(
                "{at}: {other} is no effect (the effects: {})",
                sorted(&EFFECTS)
            )),
        }
    }

    fn members(&self, add: &mut dyn FnMut(&str)) {
        match self {
            Effect::Set(fields) | Effect::Adjust(fields) | Effect::Update(fields) => {
                for (_, operand) in fields {
                    operand.members(add);
                }
            }
            Effect::Upsert { key, values, .. } => {
                key.members(add);
                for (_, operand) in values {
                    operand.members(add);
                }
            }
            Effect::Move { .. } => {}
        }
    }
}

// The IR types of an owned field that holds a number, which adjust and the
// sums and averages read.
const NUMBERS: [&str; 4] = ["number", "integer", "Generic.Int64", "Generic.Float64"];

impl Aggregate {
    fn parse(
        field: &str,
        raw: &Value,
        at: &str,
        declared: &Declared<'_>,
    ) -> Result<Aggregate, String> {
        if !declared.declaration.has_field(field) || declared.field_type(field).is_none() {
            return Err(format!(
                "{at}: {field} is not a field it declares with a type, which a column holds"
            ));
        }
        if declared.declaration.value.as_deref() == Some(field) {
            return Err(format!(
                "{at}: {field} is its value field, which no rule derives"
            ));
        }
        let Some((name, body)) = raw
            .as_object()
            .filter(|object| object.len() == 1)
            .and_then(|object| object.iter().next())
        else {
            return Err(format!("{at} is one function: {}", sorted(&FUNCTIONS)));
        };
        let function = match name.as_str() {
            "count" => Function::Count,
            "sum" => Function::Sum,
            "min" => Function::Min,
            "max" => Function::Max,
            "average" => Function::Average,
            other => {
                return Err(format!(
                    "{at}: {other} is no function (the functions: {})",
                    sorted(&FUNCTIONS)
                ))
            }
        };
        let at = format!("{at}.{name}");
        let Some(stated) = body.as_object() else {
            return Err(format!(
                "{at} is an object: {{ table, field?, where?, empty? }}"
            ));
        };
        unknown(&at, stated, &["empty", "field", "table", "where"])?;
        let table = stated
            .get("table")
            .and_then(Value::as_str)
            .ok_or(format!("{at}.table names a sub-table"))?;
        let Some(found) = declared.declaration.find_table(table) else {
            return Err(format!(
                "{at}.table is {table}, which is not a sub-table it declares ({})",
                listed_names(&declared.declaration.table_names())
            ));
        };
        if found.shared {
            return Err(format!(
                "{at}.table is {table}, a table kept for the table as a whole: a derived field reads its row's own rows"
            ));
        }
        let of = stated.get("field").and_then(Value::as_str);
        let of = match (function, of) {
            (Function::Count, None) => None,
            (Function::Count, Some(_)) => {
                return Err(format!("{at} counts rows, and names no field"));
            }
            (_, None) => return Err(format!("{at}.field names a field of {table}")),
            (_, Some(of)) => {
                let numeric = declared
                    .table_field_type(table, of)
                    .is_some_and(|type_name| NUMBERS.contains(&type_name.as_str()));
                if !numeric {
                    return Err(format!(
                        "{at}.field is {of}, which is not a field of {table} that holds a number ({})",
                        NUMBERS.join(", ")
                    ));
                }
                Some(of.to_owned())
            }
        };
        let filter = stated
            .get("where")
            .map(|filter| Condition::parse(filter, &format!("{at}.where")))
            .transpose()?;
        if let Some(filter) = &filter {
            filter.only_given(&format!("{at}.where"))?;
            let mut missing = None;
            filter.members(
                &mut |name: &str| {
                    if missing.is_none() && declared.table_field_type(table, name).is_none() {
                        missing = Some(name.to_owned());
                    }
                },
                true,
            );
            if let Some(name) = missing {
                return Err(format!(
                    "{at}.where names {name}, which {table} has no field of (its fields: {})",
                    listed_names(&found.fields)
                ));
            }
        }
        let empty = match (function, stated.get("empty")) {
            (Function::Count | Function::Sum, Some(_)) => {
                return Err(format!("{at}.empty: a count and a sum of no rows are 0"));
            }
            (_, empty) => empty.cloned(),
        };
        Ok(Aggregate {
            function,
            table: table.to_owned(),
            field: of,
            filter,
            empty,
        })
    }
}

fn operand_kind(members: &Map<String, Value>) -> Option<&str> {
    if members.len() != 1 {
        return None;
    }
    members
        .keys()
        .next()
        .map(String::as_str)
        .filter(|key| OPERANDS.contains(key))
}

impl Operand {
    fn parse(raw: &Value, at: &str) -> Result<Operand, String> {
        let Some(members) = raw.as_object() else {
            return Ok(Operand::Literal(raw.clone()));
        };
        let Some(kind) = operand_kind(members) else {
            return Err(format!(
                "{at} is an object, which an operand is as one of {} (a literal object is {{ \"literal\": ... }})",
                sorted(&OPERANDS)
            ));
        };
        let value = &members[kind];
        let name = || -> Result<String, String> {
            value
                .as_str()
                .filter(|name| !name.is_empty())
                .map(str::to_owned)
                .ok_or(format!("{at}.{kind} names a member"))
        };
        Ok(match kind {
            "literal" => Operand::Literal(value.clone()),
            "param" => Operand::Param(name()?),
            "config" => Operand::Config(name()?),
            "field" => Operand::Field(name()?),
            "caller" | "now" | "newKey" if value == &Value::Bool(true) => match kind {
                "caller" => Operand::Caller,
                "now" => Operand::Now,
                _ => Operand::NewKey,
            },
            _ => return Err(format!("{at}.{kind} is true")),
        })
    }

    fn members(&self, add: &mut dyn FnMut(&str)) {
        if let Operand::Field(name) = self {
            add(name);
        }
    }
}

impl Condition {
    fn parse(raw: &Value, at: &str) -> Result<Condition, String> {
        let Some(members) = raw.as_object() else {
            return Err(format!(
                "{at} is a condition: an object of fields and their tests, with anyOf and allOf"
            ));
        };
        let mut condition = Condition::default();
        for (member, tests) in members {
            match member.as_str() {
                "anyOf" | "allOf" => {
                    let Some(list) = tests.as_array() else {
                        return Err(format!("{at}.{member} is a list of conditions"));
                    };
                    let parsed = list
                        .iter()
                        .enumerate()
                        .map(|(i, one)| Condition::parse(one, &format!("{at}.{member}[{i}]")))
                        .collect::<Result<Vec<_>, _>>()?;
                    if member == "anyOf" {
                        condition.any_of = parsed;
                    } else {
                        condition.all_of = parsed;
                    }
                }
                field => {
                    let at = format!("{at}.{field}");
                    let Some(ops) = tests.as_object() else {
                        return Err(format!(
                            "{at} is an object of tests ({})",
                            OPERATORS.join(", ")
                        ));
                    };
                    let mut parsed = Vec::new();
                    for (op, value) in ops {
                        let at = format!("{at}.{op}");
                        parsed.push(match op.as_str() {
                            "between" => match value.as_array().map(Vec::as_slice) {
                                Some([low, high]) => Test::Between(
                                    Operand::parse(low, &format!("{at}[0]"))?,
                                    Operand::parse(high, &format!("{at}[1]"))?,
                                ),
                                _ => return Err(format!("{at} is a list of two: [low, high]")),
                            },
                            "in" | "notIn" => {
                                let keep = op == "in";
                                match value {
                                    Value::Array(list) => Test::In(
                                        list.iter()
                                            .enumerate()
                                            .map(|(i, one)| {
                                                Operand::parse(one, &format!("{at}[{i}]"))
                                            })
                                            .collect::<Result<_, _>>()?,
                                        keep,
                                    ),
                                    other => {
                                        let operand = Operand::parse(other, &at)?;
                                        if matches!(operand, Operand::Literal(_)) {
                                            return Err(format!(
                                                "{at} is a list, or an operand that holds one"
                                            ));
                                        }
                                        Test::InGiven(operand, keep)
                                    }
                                }
                            }
                            "isNull" => match value {
                                Value::Bool(null) => Test::IsNull(*null),
                                _ => return Err(format!("{at} is true or false")),
                            },
                            "ilike" => Test::Ilike(Operand::parse(value, &at)?),
                            other => match Op::parse(other) {
                                Some(op) => Test::Compare(op, Operand::parse(value, &at)?),
                                None => {
                                    return Err(format!(
                                        "{at}: {other} is no test (the tests: {})",
                                        OPERATORS.join(", ")
                                    ))
                                }
                            },
                        });
                    }
                    condition.tests.push((field.to_owned(), parsed));
                }
            }
        }
        Ok(condition)
    }

    // members adds each field the condition tests, when `subjects` is set,
    // and each field an operand of it reads.
    fn members(&self, add: &mut dyn FnMut(&str), subjects: bool) {
        for (field, tests) in &self.tests {
            if subjects {
                add(field);
            }
            for test in tests {
                for operand in test.operands() {
                    if subjects {
                        operand.members(add);
                    }
                }
            }
        }
        for one in self.all_of.iter().chain(&self.any_of) {
            one.members(add, subjects);
        }
    }

    // only_given refuses an operand that is neither a literal nor a
    // member of the use's config: what a parameter's bound and a derived
    // field's where compare with, which read no row and no caller.
    fn only_given(&self, at: &str) -> Result<(), String> {
        for (field, tests) in &self.tests {
            for test in tests {
                for operand in test.operands() {
                    if !matches!(operand, Operand::Literal(_) | Operand::Config(_)) {
                        return Err(format!(
                            "{at}.{field}: a literal or a member of the config is what it compares with here"
                        ));
                    }
                }
            }
        }
        for one in self.all_of.iter().chain(&self.any_of) {
            one.only_given(at)?;
        }
        Ok(())
    }
}

impl Test {
    fn operands(&self) -> Vec<&Operand> {
        match self {
            Test::Compare(_, operand) | Test::Ilike(operand) | Test::InGiven(operand, _) => {
                vec![operand]
            }
            Test::Between(low, high) => vec![low, high],
            Test::In(list, _) => list.iter().collect(),
            Test::IsNull(_) => Vec::new(),
        }
    }
}

// ---- evaluation ----

/// What an operand and a condition read: the use's config, an operation's
/// parameters, the caller's subject, the command's time, and the row's
/// fields.
struct Env<'e> {
    config: &'e Value,
    params: Option<&'e Value>,
    caller: Option<&'e str>,
    now: Option<i64>,
    row: &'e dyn Fn(&str) -> Option<Value>,
}

impl Env<'_> {
    fn value(&self, operand: &Operand) -> Option<Value> {
        match operand {
            Operand::Literal(value) => Some(value.clone()).filter(|value| !value.is_null()),
            Operand::Param(name) => self
                .params
                .and_then(|params| params.get(name))
                .filter(|value| !value.is_null())
                .cloned(),
            Operand::Config(member) => config_member(self.config, member)
                .filter(|value| !value.is_null())
                .cloned(),
            Operand::Caller => self
                .caller
                .filter(|subject| !subject.is_empty())
                .map(Value::from),
            Operand::Now => self.now.map(Value::from),
            Operand::Field(name) => (self.row)(name),
            // A new key is made where an upsert names it, never compared.
            Operand::NewKey => None,
        }
    }

    // holds says whether a condition holds, each field it tests read by
    // `subject` with its IR type, which a comparison with the caller reads.
    fn holds(
        &self,
        condition: &Condition,
        subject: &dyn Fn(&str) -> (Option<Value>, String),
    ) -> bool {
        for (field, tests) in &condition.tests {
            let (value, type_name) = subject(field);
            for test in tests {
                if !self.test(test, value.as_ref(), &type_name) {
                    return false;
                }
            }
        }
        if !condition.all_of.iter().all(|one| self.holds(one, subject)) {
            return false;
        }
        // An empty anyOf filters nothing (D90).
        condition.any_of.is_empty() || condition.any_of.iter().any(|one| self.holds(one, subject))
    }

    fn test(&self, test: &Test, value: Option<&Value>, type_name: &str) -> bool {
        match test {
            Test::Compare(op, operand) => {
                // The caller is compared with a field that holds a subject
                // as Workflow's by compares one (D104).
                if *operand == Operand::Caller && matches!(op, Op::Eq | Op::Ne) {
                    let subject = self.caller.unwrap_or_default();
                    let named = names_subject(subject_type(type_name), value, subject);
                    return if *op == Op::Eq { named } else { !named };
                }
                let other = self.value(operand);
                compare(*op, value, other.as_ref())
            }
            Test::Between(low, high) => {
                compare(Op::Gte, value, self.value(low).as_ref())
                    && compare(Op::Lte, value, self.value(high).as_ref())
            }
            Test::In(list, keep) => {
                // An empty list filters nothing, as `in` with no values (D90).
                if list.is_empty() {
                    return true;
                }
                let found = list
                    .iter()
                    .any(|operand| self.is(operand, value, type_name));
                if *keep {
                    found
                } else {
                    !found
                }
            }
            Test::InGiven(operand, keep) => {
                let list = match self.value(operand) {
                    Some(Value::Array(list)) => list,
                    _ => Vec::new(),
                };
                if list.is_empty() {
                    return true;
                }
                let found = list
                    .iter()
                    .any(|one| value.is_some_and(|value| equal(Some(value), Some(one))));
                if *keep {
                    found
                } else {
                    !found
                }
            }
            Test::IsNull(null) => value.is_none() == *null,
            Test::Ilike(operand) => match (value, self.value(operand)) {
                (Some(Value::String(text)), Some(Value::String(part))) => {
                    text.to_lowercase().contains(&part.to_lowercase())
                }
                _ => false,
            },
        }
    }

    // is says whether a value is an operand's, the caller compared as a
    // subject.
    fn is(&self, operand: &Operand, value: Option<&Value>, type_name: &str) -> bool {
        if *operand == Operand::Caller {
            return names_subject(
                subject_type(type_name),
                value,
                self.caller.unwrap_or_default(),
            );
        }
        compare(Op::Eq, value, self.value(operand).as_ref())
    }
}

// subject_type is the type a subject comparison reads a field as: its own
// when it holds a subject, else text.
fn subject_type(type_name: &str) -> &str {
    if holds_subject(type_name) {
        type_name
    } else {
        "string"
    }
}

// compare says whether a field's value meets a comparison with another, as
// the ORM's filter compares them: eq holds for a value that is the other,
// ne for one that is not (none among them, as IS DISTINCT FROM), and an
// order for two numbers, or two strings by code point, that are so.
fn compare(op: Op, value: Option<&Value>, other: Option<&Value>) -> bool {
    match op {
        Op::Eq => value.is_some() && other.is_some() && equal(value, other),
        Op::Ne => {
            !(value.is_some() && other.is_some() && equal(value, other))
                && !(value.is_none() && other.is_none())
        }
        _ => {
            let (Some(value), Some(other)) = (value, other) else {
                return false;
            };
            let Some(order) = order(value, other) else {
                return false;
            };
            match op {
                Op::Gt => order == Ordering::Greater,
                Op::Gte => order != Ordering::Less,
                Op::Lt => order == Ordering::Less,
                Op::Lte => order != Ordering::Greater,
                Op::Eq | Op::Ne => false,
            }
        }
    }
}

// order compares two numbers, or two strings by code point; None for
// values that do not compare.
fn order(a: &Value, b: &Value) -> Option<Ordering> {
    match (number_of(a), number_of(b)) {
        (Some(a), Some(b)) => a.partial_cmp(&b),
        _ => match (a, b) {
            (Value::String(a), Value::String(b)) => Some(a.cmp(b)),
            _ => None,
        },
    }
}

// number_of reads a number, or a string that holds a decimal one, as an
// adapter may read a BIGINT.
fn number_of(value: &Value) -> Option<f64> {
    match value {
        Value::Number(number) => number
            .as_f64()
            .or_else(|| number.to_string().parse::<f64>().ok()),
        Value::String(text) => {
            let text = text.trim();
            let decimal = !text.is_empty()
                && text
                    .trim_start_matches('-')
                    .chars()
                    .all(|c| c.is_ascii_digit() || c == '.');
            decimal.then(|| text.parse::<f64>().ok()).flatten()
        }
        _ => None,
    }
}

// whole reads an integer exactly: a JSON integer, or a string of one.
fn whole(value: &Value) -> Option<i64> {
    match value {
        Value::Number(number) => number.as_i64(),
        Value::String(text) => text.trim().parse::<i64>().ok(),
        _ => None,
    }
}

// added is the sum of two numbers: exact for two integers, JavaScript's
// double otherwise.
fn added(a: &Value, b: &Value) -> Option<Value> {
    if let (Some(a), Some(b)) = (whole(a), whole(b)) {
        if let Some(sum) = a.checked_add(b) {
            return Some(Value::from(sum));
        }
    }
    Some(number_json(number_of(a)? + number_of(b)?))
}

// ---- the behavior ----

impl Behavior for Declarative {
    type Config = DeclarativeConfig;

    fn declaration(&self) -> Declaration {
        self.declaration.clone()
    }

    fn parse_config(
        &self,
        raw: &Value,
        target: &ConfigTarget<'_>,
    ) -> Result<DeclarativeConfig, ConfigError> {
        let issues = match self.file.get("configSchema") {
            Some(_) => ajv::config_issues(&self.file, raw),
            None if raw.is_object() => Vec::new(),
            None => vec![SchemaIssue::new("", "must be object")],
        };
        if !issues.is_empty() {
            return Err(ConfigError::Schema(issues));
        }
        let rules = &self.rules;
        let permission = rules.permission.as_ref().and_then(|given| given.of(raw));
        // Constants' rules, over the lists the declaration or the config
        // gives.
        let fields = rules.constant.as_ref().and_then(|given| given.of(raw));
        let one_way = rules.one_way.as_ref().and_then(|given| given.of(raw));
        // A declaration that states a constant or oneWay rule keeps or
        // moves one field at least, as Constants does: a use whose config
        // gives neither is refused, not run with no rule.
        let constants = if rules.constant.is_some() || rules.one_way.is_some() {
            let mut stated = Map::new();
            if let Some(fields) = fields {
                stated.insert("fields".into(), fields);
            }
            if let Some(one_way) = one_way {
                stated.insert("oneWay".into(), one_way);
            }
            if let Some(permission) = &permission {
                stated.insert("permission".into(), permission.clone());
            }
            let parsed = ConstantsConfig::of(&Value::Object(stated), target).map_err(|error| {
                renamed(
                    renamed(error, "fields", rules.constant.as_ref(), "rules.constant"),
                    "oneWay",
                    rules.one_way.as_ref(),
                    "rules.oneWay",
                )
            })?;
            Some(parsed)
        } else {
            None
        };
        // StateRules' rules, over the states the declaration or the config
        // gives.
        let status = rules.status.as_ref().and_then(|given| given.of(raw));
        let states = match rules.states.as_ref().and_then(|given| given.of(raw)) {
            Some(states) => {
                let mut stated = Map::new();
                stated.insert("states".into(), states);
                if let Some(status) = &status {
                    stated.insert("status".into(), status.clone());
                }
                if let Some(permission) = &permission {
                    stated.insert("permission".into(), permission.clone());
                }
                let parsed =
                    StateRulesConfig::of(&Value::Object(stated), target).map_err(|error| {
                        renamed(error, "states", rules.states.as_ref(), "rules.states")
                    })?;
                Some(parsed)
            }
            None => None,
        };
        // The Workflow each move moves, and its states.
        let mut moves = BTreeMap::new();
        for (named, states, at) in rules.moves() {
            let name = named
                .clone()
                .or_else(|| status.as_ref().and_then(Value::as_str).map(str::to_owned));
            let flow = flow_of(target, name.as_deref())?;
            for state in &states {
                if flow.lacks(state) {
                    return Err(format!(
                        "{at} moves to {state}, which is not a state of the type's Workflow ({})",
                        flow.listed()
                    )
                    .into());
                }
            }
            moves.insert(
                named,
                Moved {
                    use_id: flow.use_id,
                    transitions: flow.transitions,
                },
            );
        }
        let mut members = BTreeMap::new();
        for name in rules.members() {
            members.insert(name.clone(), self.member(&name, target)?);
        }
        Ok(DeclarativeConfig {
            raw: raw.clone(),
            constants,
            states,
            moves,
            members,
        })
    }

    fn config_change(
        &self,
        _cx: &ConfigCx<'_>,
        before: Option<&DeclarativeConfig>,
        after: Option<&DeclarativeConfig>,
    ) -> Hook<Option<String>> {
        // One that keeps no state of its own holds no row to its config.
        if !Rules::keeps_state(&self.declaration) {
            return Ok(None);
        }
        let (Some(before), Some(after)) = (before, after) else {
            return Ok(Some(
                match before {
                    None => "it cannot be added to a schema that has instances",
                    Some(_) => "it cannot be removed from a schema that has instances",
                }
                .to_owned(),
            ));
        };
        Ok(match &self.rules.config_change {
            ConfigChange::Any => None,
            ConfigChange::Identical => (!equal(Some(&before.raw), Some(&after.raw)))
                .then(|| "it allows no config change".to_owned()),
            ConfigChange::OneWay(moves) => {
                let empty = Map::new();
                let was = before.raw.as_object().unwrap_or(&empty);
                let is = after.raw.as_object().unwrap_or(&empty);
                let mut reason = None;
                for name in was.keys().chain(is.keys()) {
                    let (a, b) = (was.get(name), is.get(name));
                    if equal(a, b) {
                        continue;
                    }
                    let Some((_, direction)) = moves.iter().find(|(member, _)| member == name)
                    else {
                        reason = Some(format!("{name} cannot change"));
                        break;
                    };
                    let shown = |value: Option<&Value>| {
                        value.map_or_else(|| "none".to_owned(), Value::to_string)
                    };
                    let back = match (a, b) {
                        (Some(a), Some(b)) => order(a, b).is_none_or(|order| match direction {
                            Direction::Up => order == Ordering::Greater,
                            Direction::Down => order == Ordering::Less,
                        }),
                        _ => true,
                    };
                    if back {
                        let verb = match direction {
                            Direction::Up => "fall",
                            Direction::Down => "rise",
                        };
                        reason = Some(format!(
                            "{name} cannot {verb} from {} to {}",
                            shown(a),
                            shown(b)
                        ));
                        break;
                    }
                }
                reason
            }
        })
    }

    fn check_params(
        &self,
        config: &DeclarativeConfig,
        operation: &str,
        params: &Value,
    ) -> Vec<SchemaIssue> {
        let issues = ajv::params_issues(&self.file, operation, params);
        if !issues.is_empty() {
            return issues;
        }
        let Some(condition) = self
            .rules
            .operations
            .get(operation)
            .and_then(|rules| rules.params.as_ref())
        else {
            return Vec::new();
        };
        let none = |_: &str| None;
        let env = Env {
            config: &config.raw,
            params: Some(params),
            caller: None,
            now: None,
            row: &none,
        };
        let mut found = Issues::default();
        param_issues(&env, condition, params, "", &mut found);
        found.into_issues()
    }

    fn initialize(&self, cx: &Cx<'_, DeclarativeConfig>, _params: &Value) -> Hook<()> {
        // A new row's derived fields over no rows.
        for (field, aggregate) in &self.rules.derived {
            if let Some(value) = aggregate.over(&[], &cx.config().raw) {
                cx.set(field, value)?;
            }
        }
        Ok(())
    }

    fn validate(
        &self,
        view: &View<'_, DeclarativeConfig>,
        request: &ValidationRequest<'_>,
    ) -> Hook<Vec<ValidationIssue>> {
        let config = view.config();
        let mut issues = Vec::new();
        if let Some(constants) = &config.constants {
            issues.extend(Constants.validate(&view.with_config(constants), request)?);
        }
        if let Some(states) = &config.states {
            issues.extend(StateRules.validate(&view.with_config(states), request)?);
        }
        Ok(issues)
    }

    fn guard(
        &self,
        view: &View<'_, DeclarativeConfig>,
        request: &GuardRequest<'_>,
    ) -> Hook<Option<Veto>> {
        let config = view.config();
        if let Some(constants) = &config.constants {
            if let Some(veto) = Constants.guard(&view.with_config(constants), request)? {
                return Ok(Some(veto));
            }
        }
        if let Some(states) = &config.states {
            if let Some(veto) = StateRules.guard(&view.with_config(states), request)? {
                return Ok(Some(veto));
            }
        }
        for guard in &self.rules.guards {
            if !guard.guards(view.use_id(), request) {
                continue;
            }
            let params = match request {
                GuardRequest::Operation { params, .. } => Some(*params),
                _ => None,
            };
            // A create's guard reads the new row's own fields; every other
            // request's, the row as it is before it.
            let created = match request {
                GuardRequest::Create { own, .. } => Some(*own),
                _ => None,
            };
            let read = |name: &str| match created {
                Some(own) => match config.members.get(name).map(|member| &member.read) {
                    Some(Read::Own(field)) => {
                        own.get(field).filter(|value| !value.is_null()).cloned()
                    }
                    _ => None,
                },
                None => read_view(view, config, name),
            };
            let env = Env {
                config: &config.raw,
                params,
                caller: Some(view.caller().subject.as_str()),
                now: Some(view.now()),
                row: &read,
            };
            let subject = |name: &str| (read(name), member_type(config, name));
            if env.holds(&guard.require, &subject) {
                continue;
            }
            let action = request.action();
            let mut details = Map::new();
            details.insert("action".into(), Value::from(action));
            let message = guard
                .message
                .clone()
                .unwrap_or_else(|| format!("{} refuses {action}: {}", row_name(view), guard.veto));
            return Ok(Some(
                Veto::new(message)
                    .with_code(guard.veto.as_str())
                    .with_details(details),
            ));
        }
        Ok(None)
    }

    fn operation(&self, cx: &Cx<'_, DeclarativeConfig>, name: &str, params: &Value) -> Hook<Value> {
        let Some(rules) = self.rules.operations.get(name) else {
            return Err(cx.defect(format!(
                "it declares operation {name} and states no rules for it"
            )));
        };
        let mut written = BTreeSet::new();
        for effect in &rules.effects {
            self.apply(cx, effect, Some(params), Strictness::Strict, &mut written)?;
        }
        self.derive(cx, &written)?;
        let config = cx.config();
        let read = |field: &str| read_view(cx, config, field);
        let env = Env {
            config: &config.raw,
            params: Some(params),
            caller: Some(cx.caller().subject.as_str()),
            now: Some(cx.now()),
            row: &read,
        };
        Ok(match &rules.result {
            None => Value::Null,
            Some(Returned::One(operand)) => env.value(operand).unwrap_or(Value::Null),
            // A member whose operand gives no value is absent, as a row's
            // field with none is.
            Some(Returned::Members(members)) => {
                let mut out = Map::new();
                for (member, operand) in members {
                    if let Some(value) = env.value(operand) {
                        out.insert(member.clone(), value);
                    }
                }
                Value::Object(out)
            }
        })
    }

    fn heard(&self, cx: &Cx<'_, DeclarativeConfig>, heard: &Heard<'_>) -> Hook<()> {
        let Heard::Own { change } = heard else {
            return Ok(());
        };
        if change.write == WriteKind::Delete || change.after.is_none() {
            return Ok(());
        }
        let config = cx.config();
        let mut written = BTreeSet::new();
        // A reaction runs for the write that brought the row to meet its
        // when: the image after it meets it and the one before it does not.
        // It acts on the row as it is now, and not at all once the row has
        // moved on from meeting it.
        let met = |image: Option<&RowImage>, when: &Condition| {
            let Some(image) = image else {
                return false;
            };
            let read = |field: &str| read_image(image, cx.use_id(), config, field);
            let env = Env {
                config: &config.raw,
                params: None,
                caller: Some(cx.caller().subject.as_str()),
                now: Some(cx.now()),
                row: &read,
            };
            env.holds(when, &|name: &str| (read(name), member_type(config, name)))
        };
        for reaction in &self.rules.reactions {
            if !met(change.after.as_ref(), &reaction.when)
                || met(change.before.as_ref(), &reaction.when)
            {
                continue;
            }
            let now_read = |field: &str| read_view(cx, config, field);
            let env = Env {
                config: &config.raw,
                params: None,
                caller: Some(cx.caller().subject.as_str()),
                now: Some(cx.now()),
                row: &now_read,
            };
            let holds_now = env.holds(&reaction.when, &|name: &str| {
                (now_read(name), member_type(config, name))
            });
            if !holds_now {
                continue;
            }
            for effect in &reaction.then {
                self.apply(cx, effect, None, Strictness::WhereItCan, &mut written)?;
            }
        }
        self.derive(cx, &written)
    }
}

// How an effect that cannot act answers: an operation's refuses, a
// reaction's acts where it can.
#[derive(Clone, Copy, PartialEq, Eq)]
enum Strictness {
    Strict,
    WhereItCan,
}

impl Declarative {
    // member resolves a field a rule names: one of the behavior's owned
    // fields, the value of the use anchored on a field of that name, an own
    // field of the type, or a `<use>.<field>` of another use.
    fn member(&self, name: &str, target: &ConfigTarget<'_>) -> Result<Member, ConfigError> {
        let declared = Declared {
            file: &self.file,
            declaration: &self.declaration,
        };
        if let Some((use_id, field)) = name.split_once('.') {
            let Some(entry) = target.uses().iter().find(|entry| entry.id() == use_id) else {
                return Err(format!(
                    "rules name {name}, and the type has no use {use_id} ({})",
                    target
                        .uses()
                        .iter()
                        .map(|entry| entry.id())
                        .collect::<Vec<_>>()
                        .join(", ")
                )
                .into());
            };
            let known = target
                .declaration(&entry.behavior)
                .is_none_or(|declaration| declaration.has_field(field));
            if !known {
                return Err(format!(
                    "rules name {name}, and {} declares no field {field}",
                    entry.behavior
                )
                .into());
            }
            return Ok(Member {
                read: Read::OwnedOf(use_id.to_owned(), field.to_owned()),
                type_name: "string".to_owned(),
            });
        }
        if self.declaration.has_field(name) {
            return Ok(Member {
                read: Read::Owned(name.to_owned()),
                type_name: declared.field_type(name).unwrap_or("string").to_owned(),
            });
        }
        if let Some(entry) = target
            .uses()
            .iter()
            .find(|entry| entry.field() == Some(name) && entry.id() != target.use_id())
        {
            let value = target
                .declaration(&entry.behavior)
                .and_then(|declaration| declaration.value.clone())
                .unwrap_or_else(|| workflow::STATUS.to_owned());
            return Ok(Member {
                read: Read::OwnedOf(entry.id().to_owned(), value),
                type_name: "string".to_owned(),
            });
        }
        if let Some(field) = target.fields().iter().find(|field| field.name == name) {
            return Ok(Member {
                read: Read::Own(name.to_owned()),
                type_name: field.type_name.clone(),
            });
        }
        let fields: Vec<&str> = target
            .fields()
            .iter()
            .map(|field| field.name.as_str())
            .collect();
        Err(format!(
            "rules name {name}, which is neither a field it declares nor a field of {} (its fields: {})",
            target.type_name(),
            if fields.is_empty() { "none".to_owned() } else { fields.join(", ") }
        )
        .into())
    }

    // apply runs one effect on the row.
    fn apply(
        &self,
        cx: &Cx<'_, DeclarativeConfig>,
        effect: &Effect,
        params: Option<&Value>,
        strictness: Strictness,
        written: &mut BTreeSet<String>,
    ) -> Hook<()> {
        let config = cx.config();
        let read = |field: &str| read_view(cx, config, field);
        let env = Env {
            config: &config.raw,
            params,
            caller: Some(cx.caller().subject.as_str()),
            now: Some(cx.now()),
            row: &read,
        };
        match effect {
            Effect::Set(fields) => {
                for (field, operand) in fields {
                    cx.set(field, env.value(operand).unwrap_or(Value::Null))?;
                }
            }
            Effect::Adjust(fields) => {
                for (field, operand) in fields {
                    let Some(by) = env.value(operand).filter(|by| number_of(by).is_some()) else {
                        if strictness == Strictness::WhereItCan {
                            continue;
                        }
                        return Err(Refusal::invalid_argument(format!(
                            "{}: {} adjusts {field} by a number, and its operand gives none",
                            row_name(cx),
                            cx.action()
                        ))
                        .into());
                    };
                    let current = cx.get(field).unwrap_or_else(|| Value::from(0));
                    let next = added(&current, &by).unwrap_or(Value::Null);
                    cx.set(field, next)?;
                }
            }
            Effect::Update(fields) => {
                let mut patch = Map::new();
                for (field, operand) in fields {
                    patch.insert(field.clone(), env.value(operand).unwrap_or(Value::Null));
                }
                cx.update(&Value::Object(patch))?;
            }
            Effect::Upsert { table, key, values } => {
                let key = match key {
                    Operand::NewKey => cx.new_key(table)?,
                    operand => match env.value(operand) {
                        Some(Value::String(key)) if !key.is_empty() => key,
                        Some(Value::Number(number)) => number.to_string(),
                        _ => {
                            if strictness == Strictness::WhereItCan {
                                return Ok(());
                            }
                            if *operand == Operand::Caller {
                                return Err(cx.forbidden(format!(
                                    "{}: {} keys a row of {table} by its caller, and the call names none",
                                    row_name(cx),
                                    cx.action()
                                )));
                            }
                            return Err(Refusal::invalid_argument(format!(
                                "{}: {} keys a row of {table}, and its key gives none",
                                row_name(cx),
                                cx.action()
                            ))
                            .into());
                        }
                    },
                };
                let mut row = Map::new();
                for (field, operand) in values {
                    row.insert(field.clone(), env.value(operand).unwrap_or(Value::Null));
                }
                let held = cx.sub_rows(table)?.iter().any(|known| known.key == key);
                if held {
                    cx.update_row(table, &key, row)?;
                } else {
                    row.retain(|_, value| !value.is_null());
                    cx.insert_row(table, &key, row)?;
                }
                written.insert(table.clone());
            }
            Effect::Move { status, to } => {
                let Some(moved) = config.moves.get(status) else {
                    return Err(cx.defect(format!(
                        "it moves {status:?}, which its config does not resolve"
                    )));
                };
                let params = json!({ "to": to });
                if strictness == Strictness::Strict {
                    cx.call(&moved.use_id, workflow::TRANSITION, &params)?;
                    return Ok(());
                }
                // A reaction's move acts only where it can: a row already
                // there, with no transition to it, or whose guards veto it
                // is left as it is.
                let Some(current) = cx
                    .get_of(&moved.use_id, workflow::STATUS)
                    .and_then(|status| status.as_str().map(str::to_owned))
                else {
                    return Ok(());
                };
                if current == *to || !flow::leads(&moved.transitions, &current, to) {
                    return Ok(());
                }
                match cx.call(&moved.use_id, workflow::TRANSITION, &params) {
                    Ok(_) => {}
                    Err(stop)
                        if stop
                            .refusal()
                            .is_some_and(|refusal| refusal.code == Code::Vetoed) => {}
                    Err(stop) => return Err(stop),
                }
            }
        }
        Ok(())
    }

    // derive sets each derived field over a sub-table the run wrote.
    fn derive(&self, cx: &Cx<'_, DeclarativeConfig>, written: &BTreeSet<String>) -> Hook<()> {
        for (field, aggregate) in &self.rules.derived {
            if !written.contains(&aggregate.table) {
                continue;
            }
            let rows = cx.sub_rows(&aggregate.table)?;
            let value = aggregate.over(&rows, &cx.config().raw);
            cx.set(field, value.unwrap_or(Value::Null))?;
        }
        Ok(())
    }
}

impl Aggregate {
    // over is the aggregate of a sub-table's rows, those `where` keeps;
    // none for a minimum, a maximum or an average of no value, unless the
    // rule gives one for it (`empty`).
    fn over(&self, rows: &[SubRow], config: &Value) -> Option<Value> {
        let kept: Vec<&SubRow> = rows
            .iter()
            .filter(|row| match &self.filter {
                None => true,
                Some(filter) => {
                    let none = |_: &str| None;
                    let env = Env {
                        config,
                        params: None,
                        caller: None,
                        now: None,
                        row: &none,
                    };
                    let field = |name: &str| (sub_value(row, name), "string".to_owned());
                    env.holds(filter, &field)
                }
            })
            .collect();
        if self.function == Function::Count {
            return Some(Value::from(kept.len() as i64));
        }
        let field = self.field.as_deref().unwrap_or_default();
        let values: Vec<Value> = kept
            .iter()
            .filter_map(|row| sub_value(row, field))
            .filter(|value| number_of(value).is_some())
            .collect();
        let empty = || self.empty.clone().filter(|value| !value.is_null());
        match self.function {
            Function::Count => None,
            Function::Sum => values
                .iter()
                .try_fold(Value::from(0), |sum, value| added(&sum, value))
                .or(Some(Value::from(0))),
            Function::Min | Function::Max => {
                let picked = values
                    .iter()
                    .fold(None::<&Value>, |best, value| match best {
                        None => Some(value),
                        Some(best) => {
                            let order = order(value, best).unwrap_or(Ordering::Equal);
                            let better = match self.function {
                                Function::Min => order == Ordering::Less,
                                _ => order == Ordering::Greater,
                            };
                            Some(if better { value } else { best })
                        }
                    });
                picked.map(normal_number).or_else(empty)
            }
            Function::Average => {
                if values.is_empty() {
                    return empty();
                }
                let sum: f64 = values.iter().filter_map(number_of).sum();
                Some(number_json(sum / values.len() as f64))
            }
        }
    }
}

// normal_number is a number a row holds as JSON writes it: a string of a
// decimal read as the number.
fn normal_number(value: &Value) -> Value {
    match value {
        Value::String(_) => match whole(value) {
            Some(whole) => Value::from(whole),
            None => number_of(value).map_or(Value::Null, number_json),
        },
        other => other.clone(),
    }
}

// sub_value is a sub-table row's field: its key's field is its key.
fn sub_value(row: &SubRow, field: &str) -> Option<Value> {
    row.values
        .get(field)
        .filter(|value| !value.is_null())
        .cloned()
        .or_else(|| (field == "key").then(|| Value::from(row.key.as_str())))
}

// read_view reads a field a rule names from the row as the run has it.
fn read_view<C>(view: &View<'_, C>, config: &DeclarativeConfig, name: &str) -> Option<Value> {
    match config.members.get(name).map(|member| &member.read) {
        Some(Read::Owned(field)) => view.get(field),
        Some(Read::OwnedOf(use_id, field)) => view.get_of(use_id, field),
        Some(Read::Own(field)) => view.own_field(field),
        None => None,
    }
}

// read_image reads a field a rule names from a write's image of the row.
fn read_image(
    image: &RowImage,
    use_id: &str,
    config: &DeclarativeConfig,
    name: &str,
) -> Option<Value> {
    match config.members.get(name).map(|member| &member.read) {
        Some(Read::Owned(field)) => image.owned(use_id, field).cloned(),
        Some(Read::OwnedOf(other, field)) => image.owned(other, field).cloned(),
        Some(Read::Own(field)) => image.own_field(field).cloned(),
        None => None,
    }
}

fn member_type(config: &DeclarativeConfig, name: &str) -> String {
    config
        .members
        .get(name)
        .map_or_else(|| "string".to_owned(), |member| member.type_name.clone())
}

// param_issues lists what the parameters break of an operation's params
// condition, each at its parameter in ajv's words for the bound it breaks.
fn param_issues(
    env: &Env<'_>,
    condition: &Condition,
    params: &Value,
    at: &str,
    found: &mut Issues,
) {
    for (name, tests) in &condition.tests {
        let path = schema::member(at, name);
        let value = params.get(name).filter(|value| !value.is_null());
        for test in tests {
            if env.test(test, value, "string") {
                continue;
            }
            let (keyword, message) = match test {
                Test::Compare(op, operand) => {
                    let bound = env
                        .value(operand)
                        .map_or_else(|| "null".to_owned(), |v| v.to_string());
                    match op {
                        Op::Eq => ("const", "must be equal to constant".to_owned()),
                        Op::Ne => ("not", "must NOT be valid".to_owned()),
                        Op::Gt => ("exclusiveMinimum", format!("must be > {bound}")),
                        Op::Gte => ("minimum", format!("must be >= {bound}")),
                        Op::Lt => ("exclusiveMaximum", format!("must be < {bound}")),
                        Op::Lte => ("maximum", format!("must be <= {bound}")),
                    }
                }
                Test::Between(low, high) => {
                    let below = !compare(Op::Gte, value, env.value(low).as_ref());
                    let shown = |operand: &Operand| {
                        env.value(operand)
                            .map_or_else(|| "null".to_owned(), |v| v.to_string())
                    };
                    if below {
                        ("minimum", format!("must be >= {}", shown(low)))
                    } else {
                        ("maximum", format!("must be <= {}", shown(high)))
                    }
                }
                Test::In(_, true) | Test::InGiven(_, true) => (
                    "enum",
                    "must be equal to one of the allowed values".to_owned(),
                ),
                Test::In(_, false) | Test::InGiven(_, false) => {
                    ("not", "must NOT be valid".to_owned())
                }
                Test::IsNull(true) => ("type", "must be null".to_owned()),
                Test::IsNull(false) => {
                    ("required", format!("must have required property '{name}'"))
                }
                Test::Ilike(operand) => {
                    let part = env
                        .value(operand)
                        .map_or_else(String::new, |v| v.to_string());
                    ("pattern", format!("must contain {part}"))
                }
            };
            let path = if matches!(test, Test::IsNull(false)) {
                at.to_owned()
            } else {
                path.clone()
            };
            found.push(path, keyword, message);
        }
    }
    for one in &condition.all_of {
        param_issues(env, one, params, at, found);
    }
    if !condition.any_of.is_empty() {
        let any = condition.any_of.iter().any(|one| {
            let mut inner = Issues::default();
            param_issues(env, one, params, at, &mut inner);
            inner.is_empty()
        });
        if !any {
            found.push(at, "anyOf", "must match a schema in anyOf");
        }
    }
}

// renamed names the member a rule's reason starts with as the rule reads
// it: the config's member it comes from, or the declaration's rule. A
// reason Constants or StateRules gives names its own config's member
// (`fields: owner is not a field of ...`), which a declaration's own list
// is not.
fn renamed(error: ConfigError, member: &str, given: Option<&Given>, rule: &str) -> ConfigError {
    let ConfigError::Rule(reason) = error else {
        return error;
    };
    let named = match given {
        Some(Given::Config(of)) => of.as_str(),
        _ => rule,
    };
    for separator in [": ", "."] {
        if let Some(rest) = reason.strip_prefix(&format!("{member}{separator}")) {
            return ConfigError::Rule(format!("{named}{separator}{rest}"));
        }
    }
    ConfigError::Rule(reason)
}

// row_name names the row a refusal is about: its type and its key.
fn row_name<C>(view: &View<'_, C>) -> String {
    match view.key() {
        Some(key) => format!("{} {key}", view.type_name()),
        None => view.type_name().to_owned(),
    }
}

fn unknown(at: &str, members: &Map<String, Value>, known: &[&str]) -> Result<(), String> {
    match members.keys().find(|key| !known.contains(&key.as_str())) {
        Some(key) => Err(format!(
            "{at} has no member {key} (its members: {})",
            known.join(", ")
        )),
        None => Ok(()),
    }
}

fn sorted(names: &[&str]) -> String {
    let mut names: Vec<&str> = names.to_vec();
    names.sort_unstable();
    names.dedup();
    names.join(", ")
}

fn listed_names(names: &[String]) -> String {
    if names.is_empty() {
        "none".to_owned()
    } else {
        names.join(", ")
    }
}

fn listed<'f>(file: &'f Value, key: &str) -> impl Iterator<Item = &'f Value> {
    file.get(key)
        .and_then(Value::as_array)
        .into_iter()
        .flatten()
}

#[cfg(test)]
mod tests {
    use super::*;

    // A declaration of a counter with a sub-table of marks, and its rules.
    fn tally(rules: Value) -> Value {
        json!({
            "name": "test.Tally",
            "fields": [
                {"name": "count", "type": "Generic.Int64"},
                {"name": "label", "type": "string"},
                {"name": "note"}
            ],
            "tables": [{"name": "marks", "key": "by", "fields": [
                {"name": "by", "type": "string"},
                {"name": "weight", "type": "Generic.Int64"},
                {"name": "tag", "type": "string"}
            ]}],
            "operations": [
                {"name": "add", "paramsSchema": {"type": "object", "additionalProperties": false}, "writes": true},
                {"name": "peek", "paramsSchema": {"type": "object", "additionalProperties": false}}
            ],
            "vetoes": [{"code": "too_many"}],
            "rules": rules
        })
    }

    fn refused(rules: Value) -> String {
        Declarative::from_document(&tally(rules)).expect_err("refused")
    }

    /// A declaration's rules are held to what it declares, each refusal
    /// naming the rule and what it lacks.
    #[test]
    fn a_declarations_rules_are_held_to_what_it_declares() {
        let both = json!({"add": {}, "peek": {}});
        assert!(Declarative::from_document(&tally(json!({"operations": both}))).is_ok());
        let cases = [
            (json!([]), "rules is an object"),
            (json!({"operations": both, "loops": 1}), "rules has no member loops"),
            (json!({"operations": {"add": {}}}), "it declares operation peek and rules.operations states none for it"),
            (json!({"operations": {"add": {}, "peek": {}, "drop": {}}}), "rules.operations.drop names no operation it declares (add, peek)"),
            (json!({"operations": {"add": {"effects": [{"set": {"total": 1}}]}, "peek": {}}}), "sets total, which is not a field it declares (count, label, note)"),
            (json!({"operations": {"add": {"effects": [{"set": {"note": 1}}]}, "peek": {}}}), "sets note, a field with no type, which no column holds"),
            (json!({"operations": {"add": {"effects": [{"adjust": {"label": 1}}]}, "peek": {}}}), "adjusts label, which holds no number"),
            (json!({"operations": {"add": {"effects": [{"upsert": {"table": "notes", "key": "k"}}]}, "peek": {}}}), "rules.operations.add.effects[0].upsert.table is notes, which is not a sub-table it declares (marks)"),
            (json!({"operations": {"add": {"effects": [{"upsert": {"table": "marks", "key": "k", "values": {"by": "x"}}}]}, "peek": {}}}), "names by, the row's key, which key gives"),
            (json!({"operations": {"add": {}, "peek": {"effects": [{"set": {"count": 1}}]}}}), "rules.operations.peek has effects, and peek does not write"),
            (json!({"operations": {"add": {"effects": [{"loop": {}}]}, "peek": {}}}), "loop is no effect"),
            (json!({"operations": {"add": {"params": {"n": {"lte": {"caller": true}}}}, "peek": {}}}), "a literal or a member of the config is what it compares with here"),
            (json!({"operations": both, "guards": [{"on": ["add"], "require": {}, "veto": "nope"}]}), "rules.guards[0].veto is nope, a code its declaration does not list"),
            (json!({"operations": both, "guards": [{"on": ["drop"], "require": {}, "veto": "too_many"}]}), "rules.guards[0].on names drop"),
            (json!({"operations": both, "reactions": [{"when": {}, "then": [{"set": {"count": 0}}]}]}), "rules.reactions hear the row's writes, and its declaration does not (hearsChanges)"),
            (json!({"operations": both, "derived": {"note": {"count": {"table": "marks"}}}}), "rules.derived.note: note is not a field it declares with a type"),
            (json!({"operations": both, "derived": {"count": {"sum": {"table": "marks", "field": "tag"}}}}), "rules.derived.count.sum.field is tag, which is not a field of marks that holds a number"),
            (json!({"operations": both, "derived": {"count": {"count": {"table": "marks", "where": {"color": {"eq": "red"}}}}}}), "rules.derived.count.count.where names color, which marks has no field of"),
            (json!({"operations": both, "oneWay": {"count": "up"}}), "rules.oneWay vetoes with one_way, a code its declaration does not list"),
            (json!({"operations": both, "constant": "count"}), "rules.constant is a list, or { \"config\": <member> }"),
            (json!({"operations": {"add": {"result": {"n": {"field": "count", "extra": 1}}}, "peek": {}}}), "rules.operations.add.result.n is an object, which an operand is as one of"),
        ];
        for (rules, reason) in cases {
            let error = refused(rules.clone());
            assert!(error.starts_with("behavior test.Tally: "), "{error}");
            assert!(error.contains(reason), "{rules}: {error}");
        }
        let mut created = tally(json!({"operations": both}));
        created["createParamsSchema"] = json!({"type": "object"});
        assert!(Declarative::from_document(&created)
            .expect_err("refused")
            .contains("takes no create parameters"));
        assert!(Declarative::from_document(&json!({"name": "test.Plain"}))
            .expect_err("refused")
            .contains("states no rules"));
    }

    fn holds(condition: Value, row: &Value, caller: &str) -> bool {
        let condition = Condition::parse(&condition, "when").expect("a condition");
        let config = json!({"least": 2, "names": ["ann", "bo"]});
        let read = |name: &str| row.get(name).filter(|value| !value.is_null()).cloned();
        let env = Env {
            config: &config,
            params: None,
            caller: Some(caller),
            now: Some(1_000),
            row: &read,
        };
        let types = |name: &str| match name {
            "owner" => "Identity.UUID".to_owned(),
            _ => "string".to_owned(),
        };
        env.holds(&condition, &|name: &str| (read(name), types(name)))
    }

    // row is the row the condition tests read.
    fn row() -> Value {
        json!({
            "count": 3, "label": "Big Box", "name": "bo",
            "owner": "0f5c4a3e-2b7d-4c1a-9e8f-6a5b4c3d2e1f", "none": null
        })
    }

    /// A condition reads as the ORM's filter: each field's tests ANDed,
    /// allOf's every one and anyOf's one, an empty list filtering nothing,
    /// and ne holding for a field with no value.
    #[test]
    fn a_condition_reads_as_the_orms_filter() {
        let row = row();
        let at = |condition: Value| holds(condition, &row, "alice");
        assert!(at(json!({"count": {"gte": 3, "lt": 4}})));
        assert!(!at(json!({"count": {"gt": 3}})));
        assert!(at(json!({"count": {"gte": {"config": "least"}}})));
        assert!(at(json!({"count": {"between": [1, 3]}})));
        assert!(!at(json!({"count": {"between": [4, 9]}})));
        assert!(at(json!({"count": {"eq": 3.0}})), "numbers by value");
        assert!(at(json!({"name": {"in": {"config": "names"}}})));
        assert!(
            at(json!({"name": {"in": []}})),
            "an empty in filters nothing"
        );
        assert!(!at(json!({"name": {"notIn": ["bo"]}})));
        assert!(at(
            json!({"none": {"isNull": true}, "name": {"isNull": false}})
        ));
        assert!(at(json!({"none": {"ne": "x"}})), "ne holds for no value");
        assert!(!at(json!({"none": {"eq": "x"}})));
        assert!(at(json!({"label": {"ilike": "big"}})));
        assert!(at(
            json!({"anyOf": [{"count": {"eq": 9}}, {"name": {"eq": "bo"}}]})
        ));
        assert!(!at(
            json!({"anyOf": [{"count": {"eq": 9}}, {"name": {"eq": "cy"}}]})
        ));
        assert!(at(json!({"anyOf": []})), "an empty anyOf filters nothing");
        assert!(!at(
            json!({"allOf": [{"count": {"eq": 3}}, {"name": {"eq": "cy"}}]})
        ));
        assert!(
            !at(json!({"label": {"gt": 3}})),
            "a string and a number do not order"
        );
    }

    /// The caller operand (D104) compares with a field that holds a subject
    /// as Workflow's by does: a UUID field in either case of its hyphenated
    /// form, a string field by its text, and a field with no value naming no
    /// one.
    #[test]
    fn the_caller_is_compared_with_a_field_that_holds_a_subject() {
        let row = row();
        let owner = |caller: &str| holds(json!({"owner": {"eq": {"caller": true}}}), &row, caller);
        assert!(owner("0f5c4a3e-2b7d-4c1a-9e8f-6a5b4c3d2e1f"));
        assert!(owner("0F5C4A3E-2B7D-4C1A-9E8F-6A5B4C3D2E1F"));
        assert!(!owner("alice"));
        assert!(holds(json!({"name": {"eq": {"caller": true}}}), &row, "bo"));
        assert!(holds(
            json!({"name": {"ne": {"caller": true}}}),
            &row,
            "ann"
        ));
        assert!(holds(
            json!({"name": {"in": [{"caller": true}, "cy"]}}),
            &row,
            "bo"
        ));
        assert!(
            !holds(json!({"none": {"eq": {"caller": true}}}), &row, ""),
            "no value names no one"
        );
    }

    /// A derived field over a sub-table: a count, a sum exact over integers,
    /// a mean as JavaScript divides it, a highest and a lowest, a where, and
    /// none for no rows unless the rule gives a value for it.
    #[test]
    fn a_derived_field_is_an_aggregate_of_the_rows() {
        let row = |key: &str, weight: Value, tag: &str| {
            SubRow::new(
                key,
                json!({"weight": weight, "tag": tag})
                    .as_object()
                    .cloned()
                    .unwrap(),
            )
        };
        let rows = [
            row("a", json!(4), "x"),
            row("b", json!("5"), "y"),
            row("c", json!(1), "x"),
        ];
        let aggregate = |rule: Value| {
            let declared = Declarative::from_document(&tally(json!({
                "operations": {"add": {}, "peek": {}},
                "derived": {"count": rule}
            })))
            .expect("declared");
            let (_, aggregate) = declared.rules.derived[0].clone();
            aggregate
        };
        let config = json!({});
        let of = |rule: Value, rows: &[SubRow]| aggregate(rule).over(rows, &config);
        let weight = |function: &str| json!({ function: {"table": "marks", "field": "weight"} });
        assert_eq!(
            of(json!({"count": {"table": "marks"}}), &rows),
            Some(json!(3))
        );
        assert_eq!(of(weight("sum"), &rows), Some(json!(10)));
        assert_eq!(
            of(weight("average"), &rows),
            Some(serde_json::from_str("3.3333333333333335").unwrap())
        );
        assert_eq!(of(weight("max"), &rows), Some(json!(5)));
        assert_eq!(of(weight("min"), &rows), Some(json!(1)));
        assert_eq!(
            of(
                json!({"count": {"table": "marks", "where": {"tag": {"eq": "x"}}}}),
                &rows
            ),
            Some(json!(2))
        );
        assert_eq!(of(weight("sum"), &[]), Some(json!(0)));
        assert_eq!(of(weight("average"), &[]), None);
        assert_eq!(
            of(
                json!({"average": {"table": "marks", "field": "weight", "empty": 0}}),
                &[]
            ),
            Some(json!(0))
        );
        assert_eq!(added(&json!(2), &json!(1)), Some(json!(3)));
        assert_eq!(
            added(&json!(0.5), &json!(1)),
            Some(serde_json::from_str("1.5").unwrap())
        );
    }
}
