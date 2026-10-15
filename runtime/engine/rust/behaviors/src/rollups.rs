//! Rollups, the core's values derived from linked instances (D16, amended),
//! the port of `src/behaviors/core/rollups.ts`. The config names each
//! rollup: the schema whose instances point at this one, the link of that
//! schema's Links config they point through, and a function from a closed
//! set:
//!
//! - `count`: how many instances point here;
//! - `countBy`: how many hold each value of a field, by value: a string,
//!   enum or boolean field of their type, or `Workflow.status`, their
//!   Workflow's. An instance with no value is not counted;
//! - `sum`, `min` and `max`: of a number or integer field, over the
//!   instances that hold a value. `min` and `max` of none have no value;
//! - `latest`: the value of a field of their type, or `Workflow.status`, on
//!   the instance created last (`created_at`, then the greater id on a
//!   tie), and no value when it holds none or there is none;
//! - `all` and `any`: whether every one, or some one, is in a terminal
//!   state of its schema's Workflow ([`crate::is_terminal_state`]) and,
//!   when the rollup lists outcomes, one whose outcome
//!   ([`crate::state_outcome`]) it lists. `all` of none holds; `any` of
//!   none does not.
//!
//! The set is closed so that each function is one pass over the records it
//! reads, has one JSON type (`latest`'s is its field's), and has a rule
//! `parse_config` can check against the linked schema when the schema is
//! defined.
//!
//! One read-only field, `values`, holds every rollup's value, since a
//! declaration's fields are fixed and the config's names are not; a read
//! returns it as `Rollups.values`, and it is volatile (D56): it is computed
//! when the instance is read, as the caller, so a linked instance's change
//! shows at this instance's next read with no event on it, and nothing is
//! stored. For each schema and link its rollups name, a computation reads
//! the schema's Links config (a link that no longer points at this schema
//! holds no instance of it), one page of Links' read-only `listLinked`,
//! and, for every function but `count`, the instances in one `get_many`,
//! each asking the access policy for read on that schema. It never reads
//! Links' tables.
//!
//! The bound: a rollup reads at most [`MAX_ROLLUP_READ`] (500) linked
//! instances per computation. Past it the rollup has no value, and its
//! entry is `{"over": true}`, which no function returns.
//!
//! The guard: an `all` or `any` rollup may name `gatedStates`, states of
//! the type's own Workflow. A transition into one is refused (vetoed,
//! `not_held`, with the rollup, the state and the counts in its details),
//! whoever asks, unless every rollup that gates it holds; one past the
//! bound does not. It reads the state from transition's `to`, the only
//! parameter Workflow's closed paramsSchema takes.
//!
//! `parse_config` holds `gatedStates` to the states of the type's Workflow
//! and, when the schema is defined or published, each linked schema as the
//! caller may read it ([`ConfigTarget::schemas`]): it has a live version,
//! composes Links with the link pointing at this schema, and has the field
//! each function takes, of a fitting type.
//!
//! Nothing is stored and no SQL is run, so both databases run the same
//! code (D58). configChange: rollups may be added, removed and changed,
//! and Rollups added to or removed from a schema with instances.

use std::collections::HashMap;

use serde_json::{Map, Value};

use superschematic_engine::behaviors::{ConfigSchema, InstanceView};
use superschematic_engine::errors::Veto;
use superschematic_engine::json::{compare_utf16, js_keys, js_number, number_value, stringify};
use superschematic_engine::{
    Behavior, ConfigTarget, Error, GuardRequest, Hooks, InstanceRecord, Result,
};

use crate::workflow::{state_outcome, WorkflowOutcome};

/// The most linked instances one rollup reads per computation: one
/// `listLinked` page, one `get_many`.
pub const MAX_ROLLUP_READ: usize = 500;

/// Workflow's status by qualified name, as a rollup's field names it.
const WORKFLOW_STATUS: &str = "Workflow.status";

/// The functions a rollup computes.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum RollupFunction {
    Count,
    CountBy,
    Sum,
    Min,
    Max,
    Latest,
    All,
    Any,
}

impl RollupFunction {
    /// The function as a config writes it.
    pub fn as_str(self) -> &'static str {
        match self {
            RollupFunction::Count => "count",
            RollupFunction::CountBy => "countBy",
            RollupFunction::Sum => "sum",
            RollupFunction::Min => "min",
            RollupFunction::Max => "max",
            RollupFunction::Latest => "latest",
            RollupFunction::All => "all",
            RollupFunction::Any => "any",
        }
    }

    /// The function a config's text names; `None` for another text.
    pub fn parse(text: &str) -> Option<Self> {
        Some(match text {
            "count" => RollupFunction::Count,
            "countBy" => RollupFunction::CountBy,
            "sum" => RollupFunction::Sum,
            "min" => RollupFunction::Min,
            "max" => RollupFunction::Max,
            "latest" => RollupFunction::Latest,
            "all" => RollupFunction::All,
            "any" => RollupFunction::Any,
            _ => return None,
        })
    }

    // Whether it reads whether each instance is in a terminal state.
    fn terminal(self) -> bool {
        matches!(self, RollupFunction::All | RollupFunction::Any)
    }
}

/// One rollup of a Rollups config.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct RollupSpec {
    /// The schema whose instances point at this one.
    pub schema: String,
    /// The link of that schema's Links config they point through.
    pub link: String,
    pub function: RollupFunction,
    /// countBy's, sum's, min's, max's and latest's field of the linked
    /// instances.
    pub field: Option<String>,
    /// For all and any, the states of the type's Workflow a transition into
    /// waits for the rollup to hold; none when the config gives none.
    pub gated_states: Vec<String>,
    /// For all and any, the outcomes a linked instance's terminal state must
    /// have to count; every outcome when `None`.
    pub outcomes: Option<Vec<WorkflowOutcome>>,
}

/// Rollups' config, parsed: each rollup by name, in the config's order.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct RollupsConfig {
    pub rollups: Vec<(String, RollupSpec)>,
}

impl RollupsConfig {
    /// A rollup by name; `None` for a name the config does not give.
    pub fn get(&self, name: &str) -> Option<&RollupSpec> {
        self.rollups
            .iter()
            .find(|(known, _)| known == name)
            .map(|(_, spec)| spec)
    }
}

/// Whether a terminal state's outcome counts for a list of outcomes: every
/// one when there is no list (`outcomes.includes`).
pub(crate) fn outcome_counts(
    outcomes: Option<&[WorkflowOutcome]>,
    outcome: Option<WorkflowOutcome>,
) -> bool {
    outcome.is_some_and(|outcome| outcomes.is_none_or(|listed| listed.contains(&outcome)))
}

/// A number as JavaScript holds it, as JSON: its shortest text, null for
/// one JSON cannot write.
pub(crate) fn number_json(value: f64) -> Value {
    if !value.is_finite() {
        return Value::Null;
    }
    serde_json::from_str(&js_number(value)).unwrap_or(Value::Null)
}

// The instances that point at one instance through one link, as read.
enum Linked {
    Over,
    Read {
        ids: Vec<String>,
        records: HashMap<String, InstanceRecord>,
        /// The linked schema's Workflow config, for all and any; `None`
        /// when it composes none.
        flow: Option<Value>,
    },
}

// What a rollup computed: its value (none for min and max of no value), or
// over the bound; for all and any, how many are in a terminal state with
// an outcome the rollup counts.
enum Computed {
    Over,
    Value {
        value: Option<Value>,
        total: usize,
        terminal: Option<usize>,
    },
}

// What one schema and link must read for the rollups that name it.
struct Group {
    schema: String,
    link: String,
    records: bool,
    status: bool,
    flow: bool,
}

// linked reads the instances of the group's schema that point at the
// view's instance through its link, as the caller: none when the link no
// longer points at this schema, over when there are more than
// MAX_ROLLUP_READ.
fn linked(view: &InstanceView<'_, RollupsConfig>, group: &Group) -> Result<Linked> {
    let links = view.schemas().config(&group.schema, "Links")?;
    let link = links
        .as_ref()
        .and_then(|links| links.get("links"))
        .and_then(Value::as_object)
        .and_then(|links| links.get(&group.link));
    if link
        .and_then(|link| link.get("schema"))
        .and_then(Value::as_str)
        != Some(view.schema())
    {
        return Ok(Linked::Read {
            ids: Vec::new(),
            records: HashMap::new(),
            flow: None,
        });
    }
    let mut params = Map::new();
    params.insert("name".into(), Value::from(group.link.as_str()));
    params.insert("id".into(), Value::from(view.id()));
    params.insert("limit".into(), Value::from(MAX_ROLLUP_READ));
    let listed =
        view.instances()
            .invoke_schema(&group.schema, "listLinked", &Value::Object(params))?;
    if !listed.get("next").is_none_or(Value::is_null) {
        return Ok(Linked::Over);
    }
    let ids: Vec<String> = listed
        .get("items")
        .and_then(Value::as_array)
        .into_iter()
        .flatten()
        .filter_map(|item| item.get("id").and_then(Value::as_str))
        .map(str::to_owned)
        .collect();
    let records = if group.records && !ids.is_empty() {
        let fields: &[&str] = if group.status {
            &[WORKFLOW_STATUS]
        } else {
            &[]
        };
        let wanted: Vec<&str> = ids.iter().map(String::as_str).collect();
        view.instances()
            .get_many(&group.schema, &wanted, Some(fields))?
            .into_iter()
            .collect()
    } else {
        HashMap::new()
    };
    let flow = if group.flow {
        view.schemas().config(&group.schema, "Workflow")?
    } else {
        None
    };
    Ok(Linked::Read { ids, records, flow })
}

// field_of reads a rollup's field of a linked instance: Workflow.status,
// its Workflow's, or one of its own fields.
fn field_of<'r>(record: Option<&'r InstanceRecord>, field: &str) -> Option<&'r Value> {
    let record = record?;
    if field == WORKFLOW_STATUS {
        record.behavior_field("Workflow", "status")
    } else {
        record.data.get(field)
    }
}

// compute computes one rollup over the instances read for its link. An id
// listLinked gave whose instance is gone, which only happens while its
// delete runs, holds no value and is not in a terminal state.
fn compute(spec: &RollupSpec, set: &Linked) -> Computed {
    let Linked::Read { ids, records, flow } = set else {
        return Computed::Over;
    };
    let total = ids.len();
    let read: Vec<Option<&InstanceRecord>> = ids.iter().map(|id| records.get(id)).collect();
    let field = spec.field.as_deref().unwrap_or_default();
    let done = |value: Option<Value>| Computed::Value {
        value,
        total,
        terminal: None,
    };
    match spec.function {
        RollupFunction::Count => done(Some(Value::from(total))),
        RollupFunction::CountBy => {
            let mut counts: Vec<(String, i64)> = Vec::new();
            for value in read.iter().map(|record| field_of(*record, field)) {
                let key = match value {
                    Some(Value::String(text)) => text.clone(),
                    Some(Value::Bool(flag)) => flag.to_string(),
                    _ => continue,
                };
                match counts.iter_mut().find(|(known, _)| *known == key) {
                    Some((_, count)) => *count += 1,
                    None => counts.push((key, 1)),
                }
            }
            counts.sort_by(|(a, _), (b, _)| compare_utf16(a, b));
            done(Some(Value::Object(
                counts
                    .into_iter()
                    .map(|(key, count)| (key, Value::from(count)))
                    .collect(),
            )))
        }
        RollupFunction::Sum | RollupFunction::Min | RollupFunction::Max => {
            let numbers: Vec<f64> = read
                .iter()
                .filter_map(|record| match field_of(*record, field) {
                    Some(Value::Number(number)) => Some(number_value(number)),
                    _ => None,
                })
                .collect();
            if spec.function == RollupFunction::Sum {
                return done(Some(number_json(
                    numbers.iter().fold(0.0, |sum, value| sum + value),
                )));
            }
            if numbers.is_empty() {
                return done(None);
            }
            let pick = if spec.function == RollupFunction::Min {
                numbers.iter().copied().fold(f64::INFINITY, f64::min)
            } else {
                numbers.iter().copied().fold(f64::NEG_INFINITY, f64::max)
            };
            done(Some(number_json(pick)))
        }
        RollupFunction::Latest => {
            // The instance created last, the greater id on a tie; one
            // listLinked gave whose instance is gone is passed over.
            let mut last: Option<&InstanceRecord> = None;
            for id in ids {
                let Some(record) = records.get(id) else {
                    continue;
                };
                let later = match last {
                    None => true,
                    Some(last) => {
                        record.created_at > last.created_at
                            || (record.created_at == last.created_at
                                && compare_utf16(&record.id, &last.id).is_gt())
                    }
                };
                if later {
                    last = Some(record);
                }
            }
            done(
                field_of(last, field)
                    .filter(|value| !value.is_null())
                    .cloned(),
            )
        }
        RollupFunction::All | RollupFunction::Any => {
            let terminal = read
                .iter()
                .filter(|record| {
                    let status = record
                        .and_then(|record| record.behavior_field("Workflow", "status"))
                        .and_then(Value::as_str);
                    let outcome = match (flow, status) {
                        (Some(flow), Some(status)) => state_outcome(flow, status),
                        _ => None,
                    };
                    outcome_counts(spec.outcomes.as_deref(), outcome)
                })
                .count();
            Computed::Value {
                value: Some(Value::Bool(if spec.function == RollupFunction::All {
                    terminal == total
                } else {
                    terminal > 0
                })),
                total,
                terminal: Some(terminal),
            }
        }
    }
}

// evaluate computes the named rollups for one instance, reading each schema
// and link they name once, with the fields every one of them needs.
fn evaluate(
    view: &InstanceView<'_, RollupsConfig>,
    names: &[&str],
) -> Result<Vec<(String, Computed)>> {
    let config = view.config();
    let mut groups: Vec<Group> = Vec::new();
    for name in names {
        let Some(spec) = config.get(name) else {
            continue;
        };
        let at = match groups
            .iter()
            .position(|group| group.schema == spec.schema && group.link == spec.link)
        {
            Some(at) => at,
            None => {
                groups.push(Group {
                    schema: spec.schema.clone(),
                    link: spec.link.clone(),
                    records: false,
                    status: false,
                    flow: false,
                });
                groups.len() - 1
            }
        };
        let group = &mut groups[at];
        group.records |= spec.function != RollupFunction::Count;
        group.status |= spec.function.terminal() || spec.field.as_deref() == Some(WORKFLOW_STATUS);
        group.flow |= spec.function.terminal();
    }
    let mut sets = Vec::with_capacity(groups.len());
    for group in &groups {
        sets.push(linked(view, group)?);
    }
    Ok(names
        .iter()
        .filter_map(|name| {
            let spec = config.get(name)?;
            let at = groups
                .iter()
                .position(|group| group.schema == spec.schema && group.link == spec.link)?;
            Some(((*name).to_owned(), compute(spec, &sets[at])))
        })
        .collect())
}

// counted names the states an all or any rollup counts, in a sentence.
fn counted(spec: &RollupSpec) -> String {
    match &spec.outcomes {
        None => "a terminal state".to_owned(),
        Some(outcomes) => format!(
            "a terminal state whose outcome is {}",
            outcomes
                .iter()
                .map(|outcome| outcome.as_str())
                .collect::<Vec<_>>()
                .join(" or ")
        ),
    }
}

// refusal says why a gating rollup does not hold, as a veto with its code:
// the rollup, the state, and how many linked instances it read and
// counted, or that it read past its bound.
fn refusal(
    view: &InstanceView<'_, RollupsConfig>,
    to: &str,
    name: &str,
    result: &Computed,
) -> Veto {
    let spec = view
        .config()
        .get(name)
        .expect("a gating rollup of the config");
    let through = format!(
        "instances of {} that point at it through {}",
        spec.schema, spec.link
    );
    let mut details = Map::new();
    details.insert("rollup".into(), Value::from(name));
    details.insert("to".into(), Value::from(to));
    let Computed::Value {
        total, terminal, ..
    } = result
    else {
        details.insert("over".into(), Value::Bool(true));
        return Veto {
            reason: format!(
                "{} {} cannot move to {to}: rollup {name} has no value, since more than {MAX_ROLLUP_READ} {through} exist",
                view.schema(),
                view.id()
            ),
            code: Some("not_held".to_owned()),
            details: Some(details),
        };
    };
    let terminal = terminal.unwrap_or(0);
    let detail = if spec.function == RollupFunction::All {
        format!(
            "{} of the {total} {through} are not in {}",
            total - terminal,
            counted(spec)
        )
    } else if *total == 0 {
        format!(
            "no instance of {} points at it through {}",
            spec.schema, spec.link
        )
    } else {
        format!("none of the {total} {through} is in {}", counted(spec))
    };
    details.insert("over".into(), Value::Bool(false));
    details.insert("linked".into(), Value::from(*total));
    details.insert("counted".into(), Value::from(terminal));
    Veto {
        reason: format!(
            "{} {} cannot move to {to} until rollup {name} holds: {detail}",
            view.schema(),
            view.id()
        ),
        code: Some("not_held".to_owned()),
        details: Some(details),
    }
}

// article names a JSON type in a sentence.
fn article(kind: &str) -> String {
    match kind {
        "integer" | "object" => format!("an {kind}"),
        "array" => "a list".to_owned(),
        "any" => "any JSON value".to_owned(),
        _ => format!("a {kind}"),
    }
}

// check_linked holds a rollup to the live version of the schema it names,
// as the caller who defines or publishes reads it.
fn check_linked(
    name: &str,
    spec: &RollupSpec,
    target: &ConfigTarget<'_>,
    source: Option<&ConfigSchema>,
) -> Result<()> {
    let at = format!("rollup {name}");
    let schema = &spec.schema;
    let Some(source) = source else {
        return Err(Error::config(format!(
            "{at}: schema {schema} has no live version; publish it first"
        )));
    };
    if !source.behaviors.iter().any(|behavior| behavior == "Links") {
        return Err(Error::config(format!(
            "{at}: {schema} does not compose Links, so none of its instances points at {}",
            target.schema()
        )));
    }
    let links = source
        .configs
        .get("Links")
        .and_then(|links| links.get("links"))
        .and_then(Value::as_object);
    let Some(link) = links.and_then(|links| links.get(&spec.link)) else {
        let known = links.map(js_keys).unwrap_or_default().join(", ");
        return Err(Error::config(format!(
            "{at}: {schema} has no link {} (its links: {known})",
            spec.link
        )));
    };
    match link.get("schema") {
        Some(Value::String(linked)) if linked == target.schema() => {}
        other => {
            let shown = match other {
                Some(Value::String(linked)) => linked.clone(),
                Some(other) => stringify(other),
                None => "undefined".to_owned(),
            };
            return Err(Error::config(format!(
                "{at}: {schema}'s link {} points at {shown}, not {}",
                spec.link,
                target.schema()
            )));
        }
    }
    let workflow = source
        .behaviors
        .iter()
        .any(|behavior| behavior == "Workflow");
    let field = spec.field.as_deref().unwrap_or("undefined");
    let own = source.fields.get(field).and_then(Value::as_str);
    match spec.function {
        RollupFunction::All | RollupFunction::Any if !workflow => Err(Error::config(format!(
            "{at}: {} reads the terminal states of {schema}'s Workflow, which it does not compose",
            spec.function.as_str()
        ))),
        RollupFunction::CountBy => {
            let kind = own.or((field == WORKFLOW_STATUS && workflow).then_some("string"));
            match kind {
                None => Err(Error::config(format!(
                    "{at}: {schema} has no field {field}; countBy takes a string, enum or boolean field of its type, or {WORKFLOW_STATUS} when it composes Workflow"
                ))),
                Some(kind) if kind != "string" && kind != "boolean" => Err(Error::config(format!(
                    "{at}: countBy takes a string, enum or boolean field, and {schema}'s {field} holds {}",
                    article(kind)
                ))),
                Some(_) => Ok(()),
            }
        }
        RollupFunction::Sum | RollupFunction::Min | RollupFunction::Max => {
            let function = spec.function.as_str();
            match own {
                None => Err(Error::config(format!(
                    "{at}: {schema} has no field {field}; {function} takes a number or integer field of its type"
                ))),
                Some(kind) if kind != "number" && kind != "integer" => {
                    Err(Error::config(format!(
                        "{at}: {function} takes a number or integer field, and {schema}'s {field} holds {}",
                        article(kind)
                    )))
                }
                Some(_) => Ok(()),
            }
        }
        RollupFunction::Latest
            if !source.fields.contains_key(field) && !(field == WORKFLOW_STATUS && workflow) =>
        {
            Err(Error::config(format!(
                "{at}: {schema} has no field {field}; latest takes a field of its type, or {WORKFLOW_STATUS} when it composes Workflow"
            )))
        }
        _ => Ok(()),
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

/// Rollups: values derived from the instances that point at this one.
#[derive(Clone, Copy, Debug, Default)]
pub struct Rollups;

impl Behavior for Rollups {
    type Config = RollupsConfig;

    fn declaration(&self) -> &str {
        include_str!("../declarations/Rollups.behavior.json")
    }

    // The configSchema holds the shape and which functions take a field or
    // gate; this holds gated states to the type's Workflow and, when the
    // schema is defined or published, each rollup to the schema it names.
    fn parse_config(&self, raw: &Value, target: &ConfigTarget<'_>) -> Result<RollupsConfig> {
        let empty = Map::new();
        let rollups = raw
            .get("rollups")
            .and_then(Value::as_object)
            .unwrap_or(&empty);
        let flow = target.configs().get("Workflow");
        let mut parsed = Vec::new();
        for name in js_keys(rollups) {
            let entry = &rollups[name];
            let text = |key: &str| {
                entry
                    .get(key)
                    .and_then(Value::as_str)
                    .unwrap_or_default()
                    .to_owned()
            };
            let spec = RollupSpec {
                schema: text("schema"),
                link: text("link"),
                function: entry
                    .get("function")
                    .and_then(Value::as_str)
                    .and_then(RollupFunction::parse)
                    .unwrap_or(RollupFunction::Count),
                field: entry
                    .get("field")
                    .and_then(Value::as_str)
                    .map(str::to_owned),
                gated_states: strings(entry.get("gatedStates")),
                outcomes: entry.get("outcomes").map(|outcomes| {
                    strings(Some(outcomes))
                        .iter()
                        .filter_map(|outcome| WorkflowOutcome::parse(outcome))
                        .collect()
                }),
            };
            if !spec.gated_states.is_empty() {
                if !target
                    .behaviors()
                    .iter()
                    .any(|behavior| behavior == "Workflow")
                {
                    return Err(Error::config(format!(
                        "rollup {name} gates states of the type's Workflow, which the type does not compose"
                    )));
                }
                // A Workflow config of the wrong shape is Workflow's to refuse.
                if let Some(states) = flow
                    .and_then(|flow| flow.get("states"))
                    .and_then(Value::as_array)
                {
                    for state in &spec.gated_states {
                        if !states.iter().any(|known| known.as_str() == Some(state)) {
                            let all: Vec<String> = states
                                .iter()
                                .map(|state| match state {
                                    Value::String(state) => state.clone(),
                                    other => other.to_string(),
                                })
                                .collect();
                            return Err(Error::config(format!(
                                "rollup {name}: gated state \"{state}\" is not a state of the type's Workflow ({})",
                                all.join(", ")
                            )));
                        }
                    }
                }
            }
            if let Some(schemas) = target.schemas() {
                check_linked(name, &spec, target, schemas.get(&spec.schema)?.as_ref())?;
            }
            parsed.push((name.to_owned(), spec));
        }
        Ok(RollupsConfig { rollups: parsed })
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            // Nothing is stored: any change is allowed.
            config_change: Some(|_, _, _, _| Ok(None)),
            guidance: Some(|_, config, _| crate::guidance::rollups::guidance(config)),
            guard: Some(guard),
            fields: vec![("values", |_, view| {
                let names: Vec<&str> = view
                    .config()
                    .rollups
                    .iter()
                    .map(|(name, _)| name.as_str())
                    .collect();
                let mut out = Map::new();
                for (name, result) in evaluate(view, &names)? {
                    match result {
                        Computed::Over => {
                            let mut over = Map::new();
                            over.insert("over".into(), Value::Bool(true));
                            out.insert(name, Value::Object(over));
                        }
                        Computed::Value {
                            value: Some(value), ..
                        } => {
                            out.insert(name, value);
                        }
                        Computed::Value { value: None, .. } => {}
                    }
                }
                Ok(Some(Value::Object(out)))
            })],
            ..Hooks::default()
        }
    }
}

// guard is the gate: a transition of the type's Workflow into a gated state
// waits for every rollup that gates it to hold.
fn guard(
    _: &Rollups,
    view: &InstanceView<'_, RollupsConfig>,
    request: &GuardRequest<'_>,
) -> Result<Option<Veto>> {
    let GuardRequest::Operation {
        behavior: "Workflow",
        operation: "transition",
        params,
        ..
    } = request
    else {
        return Ok(None);
    };
    let to = params.get("to").and_then(Value::as_str).unwrap_or_default();
    let gating: Vec<&str> = view
        .config()
        .rollups
        .iter()
        .filter(|(_, spec)| spec.gated_states.iter().any(|state| state == to))
        .map(|(name, _)| name.as_str())
        .collect();
    if gating.is_empty() {
        return Ok(None);
    }
    for (name, result) in evaluate(view, &gating)? {
        let holds = matches!(
            result,
            Computed::Value {
                value: Some(Value::Bool(true)),
                ..
            }
        );
        if !holds {
            return Ok(Some(refusal(view, to, &name, &result)));
        }
    }
    Ok(None)
}
