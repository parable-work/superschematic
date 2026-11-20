//! Rollups, values derived from the rows that point at a row (D16,
//! amended), ported onto the pure core (D103) from the engine's
//! (`../behaviors/src/rollups.rs`): a project's count of tasks, its tasks by
//! status, the sum of their estimates, whether every one is done. A table
//! says `implements Rollups<Project, { rollups: { ... } }>`. Each rollup
//! names the rows it reads by the table that points here (`schema`) and its
//! to-one relation to this table (`link`): the rows a `HasMany` of the table
//! lists (D104's Barrier, `agents: HasMany<Agent>` beside `barrier:
//! Relation<Barrier>` on Agent, reads `{ schema: "Agent", link: "barrier"
//! }`), as the engine's Links link is that relation on a table. A function
//! from a closed set computes each:
//!
//! - `count`: how many rows point here;
//! - `countBy`: how many hold each value of a field, by value: a string,
//!   enum or boolean field of their table, or `Workflow.status`, their
//!   Workflow's; a row with no value is not counted;
//! - `sum`, `min` and `max`: of a number or integer field, over the rows
//!   that hold a value; `min` and `max` of none have no value;
//! - `all` and `any`: whether every one, or some one, is in a terminal
//!   state of its table's Workflow, or a state its `finished` names (D113),
//!   with an outcome the rollup lists when it lists outcomes. `all` of none
//!   holds; `any` of none does not.
//!
//! A bare field name reads an own field of the linking row, or the status
//! of the Workflow anchored on a field of that name; `Workflow.status` reads
//! its one Workflow's. `latest`, the field's value on the row created last,
//! waits: a table's row carries no time of its creation the core reads
//! (D104's step 4), so the core refuses it at define.
//!
//! Rollups is held to D103's test of a behavior and fails it, as Constants
//! and StateRules did. It keeps no state of its own: its values are
//! computed from the related rows at each read ([`Behavior::read_view`],
//! which a read of the row runs, [`superschematic_rules::Action::Read`]) and
//! stored nowhere, a query over a relation; and its one rule, the gate,
//! holds another behavior's state, Workflow's transitions, to that query.
//! It recurs (a parent's counts, a batch done when its steps are, D104's
//! Barrier), and it is ported because D104 orders it for Barrier. The fold
//! it points to is step 5's: the values a declared query or fields derived
//! over a relation in the declarative vocabulary, and the gate a declarative
//! condition on a Workflow transition over the related rows.
//!
//! The gate: an `all` or `any` rollup may name `gatedStates`, states of the
//! table's one Workflow. A transition into one is refused (vetoed,
//! `not_held`, with the rollup, the state and the counts in its details),
//! whoever asks, unless every rollup that gates it holds; one past the read
//! bound does not. It reads the state from transition's `to`, the only
//! state Workflow's closed parameters name.
//!
//! The bound: a rollup reads one linked page, [`MAX_ROLLUP_READ`] rows
//! (500). Past it the rollup has no value, its entry is `{"over": true}`,
//! which no function returns, and its gate does not hold.
//!
//! A value is computed as the host has the rows when the read or the
//! transition runs, through the core's linked read (rows of a table whose
//! own field holds the row's key), the linking table's spec read for its
//! relation and its Workflow: a table no spec names, or one whose relation
//! no longer points at this table, holds no row of it. On a table the
//! engine's access policy is the route's (D103), so no read is refused.
//!
//! The config is checked at define, first as the declaration's
//! configSchema holds it, each issue at its path in the schema loader's
//! words ([`ConfigError::Schema`]), then each gate held to the table's one
//! Workflow and a rollup over the table itself to its relation, in the
//! engine's words where they apply ([`ConfigError::Rule`]). What reads
//! another table (it exists, its relation points here, the field each
//! function takes is one of it of a fitting type) is the build's to check
//! (`verify/behaviors_rollups.go`) and the run's, since a type's define
//! check sees its own spec alone.
//!
//! configChange: nothing is stored, so any change is allowed, and Rollups
//! joins and leaves a table with rows.

use serde_json::{Map, Number, Value};
use superschematic_rules::{
    Behavior, ConfigCx, ConfigError, ConfigTarget, Declaration, GuardRequest, Hook, KeyedRow,
    TypeSpec, UseSpec, Veto, View,
};

use crate::declared;
use crate::schema::{self, Issues};
use crate::workflow::{self, state_outcome, WorkflowOutcome};

/// The behavior's name, as its declaration has it.
pub const NAME: &str = "Rollups";

/// Rollups' declaration, the file the compiler registers and the engine
/// embeds, read in place.
pub const DECLARATION: &str = include_str!("../../behaviors/declarations/Rollups.behavior.json");

/// The field a read shows every rollup's value in.
pub const VALUES: &str = "values";

/// The veto of a gated transition whose rollup does not hold.
pub const NOT_HELD: &str = "not_held";

/// The most linked rows one rollup reads: one linked page.
pub const MAX_ROLLUP_READ: usize = superschematic_rules::LINKED_PAGE;

/// Workflow's status by qualified name, as a rollup's field names it.
const WORKFLOW_STATUS: &str = "Workflow.status";

// The members the configSchema's closed objects take.
const CONFIG_KEYS: [&str; 1] = ["rollups"];
const ROLLUP_KEYS: [&str; 6] = [
    "field",
    "function",
    "gatedStates",
    "link",
    "outcomes",
    "schema",
];

// The patterns the configSchema holds names to.
const NAME_PATTERN: &str = "^[a-z][A-Za-z0-9]{0,63}$";
const SCHEMA_PATTERN: &str = "^[A-Za-z][A-Za-z0-9_-]{0,127}$";

// The functions, in the order the configSchema's enum lists them, and the
// ones that take a field.
const FUNCTIONS: [&str; 8] = [
    "count", "countBy", "sum", "min", "max", "latest", "all", "any",
];
const FIELD_FUNCTIONS: [&str; 5] = ["countBy", "sum", "min", "max", "latest"];
const TERMINAL_FUNCTIONS: [&str; 2] = ["all", "any"];

// The outcomes, in the order the configSchema's enum lists them.
const OUTCOMES: [&str; 3] = ["success", "failure", "neutral"];

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
}

/// One rollup of a Rollups config.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct RollupSpec {
    /// The table whose rows point at this one.
    pub schema: String,
    /// Its to-one relation to this table.
    pub link: String,
    pub function: RollupFunction,
    /// countBy's, sum's, min's and max's field of the linked rows.
    pub field: Option<String>,
    /// For all and any, the states of the table's Workflow a transition
    /// into waits for the rollup to hold; none when the config gives none.
    pub gated_states: Vec<String>,
    /// For all and any, the outcomes a linked row's terminal state must
    /// have to count; every outcome when `None`.
    pub outcomes: Option<Vec<WorkflowOutcome>>,
}

/// Rollups' config, parsed: each rollup by name, in the config's order,
/// and the Workflow use whose transitions the gates hold.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct RollupsConfig {
    pub rollups: Vec<(String, RollupSpec)>,
    /// The id of the table's one Workflow use, when a rollup gates it.
    pub gated: Option<String>,
}

impl RollupsConfig {
    /// A rollup by name; `None` for a name the config does not give.
    pub fn get(&self, name: &str) -> Option<&RollupSpec> {
        self.rollups
            .iter()
            .find(|(known, _)| known == name)
            .map(|(_, spec)| spec)
    }

    /// Parses and checks a config as a type gives it: as the declaration's
    /// configSchema holds it, each issue at its path in the schema loader's
    /// words, then each gate against the table's Workflow and a rollup over
    /// the table itself against its relations, a rule's reason which the
    /// core prefixes with the type and the behavior.
    pub fn parse(raw: &Value, target: &ConfigTarget<'_>) -> Result<RollupsConfig, ConfigError> {
        let issues = config_issues(raw);
        if !issues.is_empty() {
            return Err(ConfigError::Schema(issues.into_issues()));
        }
        let empty = Map::new();
        let rollups = raw
            .get("rollups")
            .and_then(Value::as_object)
            .unwrap_or(&empty);
        let workflows: Vec<&UseSpec> = target
            .uses()
            .iter()
            .filter(|entry| entry.behavior == workflow::NAME)
            .collect();
        let mut parsed = Vec::new();
        let mut gated = None;
        for (name, entry) in rollups {
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
            if spec.function == RollupFunction::Latest {
                return Err(format!(
                    "rollup {name}: latest reads the linked row created last, and a table's row carries no time of its creation the core reads: latest waits for a later step (D104's step 4)"
                )
                .into());
            }
            if !spec.gated_states.is_empty() {
                let flow = match workflows.as_slice() {
                    [] => {
                        return Err(format!(
                            "rollup {name} gates states of the type's Workflow, which the type does not compose"
                        )
                        .into())
                    }
                    [entry] => *entry,
                    several => {
                        let ids: Vec<&str> = several.iter().map(|entry| entry.id()).collect();
                        return Err(format!(
                            "rollup {name} gates states of the type's Workflow, and the type has several ({}): Rollups gates a type's one Workflow",
                            ids.join(", ")
                        )
                        .into());
                    }
                };
                // A Workflow config of the wrong shape is Workflow's to refuse.
                if let Some(states) = flow
                    .config
                    .as_ref()
                    .and_then(|config| config.get("states"))
                    .and_then(Value::as_array)
                {
                    let all: Vec<String> = states.iter().map(state_text).collect();
                    for state in &spec.gated_states {
                        if !all.contains(state) {
                            return Err(format!(
                                "rollup {name}: gated state \"{state}\" is not a state of the type's Workflow ({})",
                                all.join(", ")
                            )
                            .into());
                        }
                    }
                }
                gated = Some(flow.id().to_owned());
            }
            // A rollup over the table itself is held to its own relation,
            // the one part of the linked table its define check sees.
            if spec.schema == target.type_name() {
                let pointing: Vec<&str> = target
                    .fields()
                    .iter()
                    .filter_map(|field| field.relation.as_ref())
                    .filter(|relation| relation.type_name == spec.schema)
                    .map(|relation| relation.name.as_str())
                    .collect();
                if !pointing.contains(&spec.link.as_str()) {
                    return Err(format!(
                        "rollup {name}: {} has no to-one relation {} to itself (its relations to itself: {})",
                        spec.schema,
                        spec.link,
                        if pointing.is_empty() {
                            "none".to_owned()
                        } else {
                            pointing.join(", ")
                        }
                    )
                    .into());
                }
            }
            parsed.push((name.clone(), spec));
        }
        Ok(RollupsConfig {
            rollups: parsed,
            gated,
        })
    }
}

/// Whether a terminal state's outcome counts for a list of outcomes: every
/// one when there is no list.
fn outcome_counts(outcomes: Option<&[WorkflowOutcome]>, outcome: Option<WorkflowOutcome>) -> bool {
    outcome.is_some_and(|outcome| outcomes.is_none_or(|listed| listed.contains(&outcome)))
}

/// A number as JavaScript holds it, as JSON: a whole one under 10^21 as
/// its digits, any other its shortest text, null for one JSON cannot
/// write.
pub(crate) fn number_json(value: f64) -> Value {
    if !value.is_finite() {
        return Value::Null;
    }
    // JavaScript writes -0 as 0.
    let value = if value == 0.0 { 0.0 } else { value };
    let text = if value.fract() == 0.0 && value.abs() < 1e21 {
        format!("{value:.0}")
    } else {
        format!("{value}")
    };
    serde_json::from_str::<Number>(&text)
        .map(Value::Number)
        .unwrap_or(Value::Null)
}

// A number a row holds, as JavaScript reads it.
fn number_of(value: &Value) -> Option<f64> {
    match value {
        Value::Number(number) => number
            .as_f64()
            .or_else(|| number.to_string().parse::<f64>().ok()),
        _ => None,
    }
}

// The rows that point at the row through one link, as read: their Workflow
// use's id and config when their table composes one.
enum Linked {
    Over,
    Read {
        rows: Vec<KeyedRow>,
        flow: Option<(String, Value)>,
    },
}

// What a rollup computed: its value (none for min and max of no value), or
// over the bound; for all and any, how many it read and how many are in a
// state it counts.
enum Computed {
    Over,
    Value {
        value: Option<Value>,
        total: usize,
        counted: Option<usize>,
    },
}

// linked reads the rows of a rollup's table that point at the view's row
// through its link: none when no spec names the table or its relation does
// not point at this table, over when one page does not hold them all.
fn linked<C>(view: &View<'_, C>, spec: &RollupSpec) -> Hook<Linked> {
    let none = Linked::Read {
        rows: Vec::new(),
        flow: None,
    };
    let Some(linking) = view.read_spec(&spec.schema)? else {
        return Ok(none);
    };
    let Some(field) = linking.relation(&spec.link) else {
        return Ok(none);
    };
    if field
        .relation
        .as_ref()
        .is_none_or(|relation| relation.type_name != view.type_name())
    {
        return Ok(none);
    }
    let flow = one_workflow(linking);
    let field = field.name.clone();
    let key = view.key().unwrap_or_default().to_owned();
    let page = view.read_linked(&spec.schema, &field, &key, None)?;
    if page.more {
        return Ok(Linked::Over);
    }
    Ok(Linked::Read {
        rows: page.rows.clone(),
        flow,
    })
}

// one_workflow is a table's one Workflow use, by id with its config; none
// for a table with none, or several, whose terminal states a rollup does
// not read.
fn one_workflow(spec: &TypeSpec) -> Option<(String, Value)> {
    let mut workflows = spec
        .uses
        .iter()
        .filter(|entry| entry.behavior == workflow::NAME);
    let entry = workflows.next()?;
    if workflows.next().is_some() {
        return None;
    }
    Some((entry.id().to_owned(), entry.config_or_empty()))
}

// field_of reads a rollup's field of a linked row: Workflow.status, its
// one Workflow's; an own field; or the status of the Workflow anchored on
// a field of that name.
fn field_of<'r>(
    row: &'r KeyedRow,
    field: &str,
    flow: Option<&(String, Value)>,
) -> Option<&'r Value> {
    if field == WORKFLOW_STATUS {
        let (use_id, _) = flow?;
        return row.row.owned(use_id, workflow::STATUS);
    }
    match row.row.own.get(field).filter(|value| !value.is_null()) {
        Some(value) => Some(value),
        None => row.row.owned(field, workflow::STATUS),
    }
}

// compute computes one rollup over the rows read for it.
fn compute(spec: &RollupSpec, set: &Linked) -> Computed {
    let Linked::Read { rows, flow } = set else {
        return Computed::Over;
    };
    let total = rows.len();
    let field = spec.field.as_deref().unwrap_or_default();
    let done = |value: Option<Value>| Computed::Value {
        value,
        total,
        counted: None,
    };
    match spec.function {
        RollupFunction::Count => done(Some(Value::from(total))),
        RollupFunction::CountBy => {
            let mut counts: Vec<(String, i64)> = Vec::new();
            for value in rows.iter().map(|row| field_of(row, field, flow.as_ref())) {
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
            counts.sort_by_key(|(a, _)| utf16(a));
            done(Some(Value::Object(
                counts
                    .into_iter()
                    .map(|(key, count)| (key, Value::from(count)))
                    .collect(),
            )))
        }
        RollupFunction::Sum | RollupFunction::Min | RollupFunction::Max => {
            let numbers: Vec<f64> = rows
                .iter()
                .filter_map(|row| field_of(row, field, flow.as_ref()).and_then(number_of))
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
        // The define check refuses latest; a config read past it has none.
        RollupFunction::Latest => done(None),
        RollupFunction::All | RollupFunction::Any => {
            let counted = rows
                .iter()
                .filter(|row| {
                    let outcome = flow.as_ref().and_then(|(use_id, config)| {
                        workflow::status_of(&row.row, use_id)
                            .and_then(|status| state_outcome(config, status))
                    });
                    outcome_counts(spec.outcomes.as_deref(), outcome)
                })
                .count();
            Computed::Value {
                value: Some(Value::Bool(if spec.function == RollupFunction::All {
                    counted == total
                } else {
                    counted > 0
                })),
                total,
                counted: Some(counted),
            }
        }
    }
}

// utf16 is a text as JavaScript orders it, by UTF-16 code unit.
fn utf16(text: &str) -> Vec<u16> {
    text.encode_utf16().collect()
}

// evaluate computes the named rollups of the view's row, reading each table
// and link they name once.
fn evaluate(view: &View<'_, RollupsConfig>, names: &[&str]) -> Hook<Vec<(String, Computed)>> {
    let config = view.config();
    let mut groups: Vec<((String, String), Linked)> = Vec::new();
    let mut out = Vec::new();
    for name in names {
        let Some(spec) = config.get(name) else {
            continue;
        };
        let at = (spec.schema.clone(), spec.link.clone());
        let index = match groups.iter().position(|(known, _)| *known == at) {
            Some(index) => index,
            None => {
                groups.push((at, linked(view, spec)?));
                groups.len() - 1
            }
        };
        out.push(((*name).to_owned(), compute(spec, &groups[index].1)));
    }
    Ok(out)
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
// the rollup, the state, and how many linked rows it read and counted, or
// that it read past its bound.
fn refusal(view: &View<'_, RollupsConfig>, to: &str, name: &str, result: &Computed) -> Veto {
    let Some(spec) = view.config().get(name) else {
        return Veto::new(format!("rollup {name} does not hold")).with_code(NOT_HELD);
    };
    let through = format!(
        "instances of {} that point at it through {}",
        spec.schema, spec.link
    );
    let row = format!("{} {}", view.type_name(), view.key().unwrap_or_default());
    let mut details = Map::new();
    details.insert("rollup".into(), Value::from(name));
    details.insert("to".into(), Value::from(to));
    let Computed::Value {
        total, counted: n, ..
    } = result
    else {
        details.insert("over".into(), Value::Bool(true));
        return Veto::new(format!(
            "{row} cannot move to {to}: rollup {name} has no value, since more than {MAX_ROLLUP_READ} {through} exist"
        ))
        .with_code(NOT_HELD)
        .with_details(details);
    };
    let n = n.unwrap_or(0);
    let detail = if spec.function == RollupFunction::All {
        format!(
            "{} of the {total} {through} are not in {}",
            total - n,
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
    details.insert("counted".into(), Value::from(n));
    Veto::new(format!(
        "{row} cannot move to {to} until rollup {name} holds: {detail}"
    ))
    .with_code(NOT_HELD)
    .with_details(details)
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

// state_text is a Workflow state as a refusal lists it.
fn state_text(state: &Value) -> String {
    match state {
        Value::String(state) => state.clone(),
        other => other.to_string(),
    }
}

/// Rollups: values derived from the rows that point at this one.
#[derive(Clone, Copy, Debug, Default)]
pub struct Rollups;

impl Behavior for Rollups {
    type Config = RollupsConfig;

    // The declaration file declares values, computed at a read.
    fn declaration(&self) -> Declaration {
        declared::declaration(DECLARATION)
    }

    fn parse_config(
        &self,
        raw: &Value,
        target: &ConfigTarget<'_>,
    ) -> Result<RollupsConfig, ConfigError> {
        RollupsConfig::parse(raw, target)
    }

    // Nothing is stored: any change is allowed, and Rollups joins and
    // leaves a table with rows.
    fn config_change(
        &self,
        _cx: &ConfigCx<'_>,
        _before: Option<&RollupsConfig>,
        _after: Option<&RollupsConfig>,
    ) -> Hook<Option<String>> {
        Ok(None)
    }

    // The gate: a transition of the table's Workflow into a gated state
    // waits for every rollup that gates it to hold.
    fn guard(
        &self,
        view: &View<'_, RollupsConfig>,
        request: &GuardRequest<'_>,
    ) -> Hook<Option<Veto>> {
        let config = view.config();
        let Some(gated) = &config.gated else {
            return Ok(None);
        };
        let Some(params) = request.operation_of(gated, workflow::TRANSITION) else {
            return Ok(None);
        };
        let to = params.get("to").and_then(Value::as_str).unwrap_or_default();
        let gating: Vec<&str> = config
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

    // A read shows every rollup's value: {"over": true} past the bound, and
    // none for a min or max of no value.
    fn read_view(&self, view: &View<'_, RollupsConfig>) -> Hook<Map<String, Value>> {
        let names: Vec<&str> = view
            .config()
            .rollups
            .iter()
            .map(|(name, _)| name.as_str())
            .collect();
        let mut values = Map::new();
        for (name, result) in evaluate(view, &names)? {
            match result {
                Computed::Over => {
                    let mut over = Map::new();
                    over.insert("over".into(), Value::Bool(true));
                    values.insert(name, Value::Object(over));
                }
                Computed::Value {
                    value: Some(value), ..
                } => {
                    values.insert(name, value);
                }
                Computed::Value { value: None, .. } => {}
            }
        }
        let mut fields = Map::new();
        fields.insert(VALUES.into(), Value::Object(values));
        Ok(fields)
    }
}

// ---- the configSchema's checks ----

// config_issues lists what a config breaks of the declaration's
// configSchema, in the schema loader's words: a closed object of rollups by
// name, each a closed object that names its table, link and function,
// takes a field exactly for the functions that read one, and gates and
// counts outcomes only for all and any.
fn config_issues(raw: &Value) -> Issues {
    let mut issues = Issues::default();
    let Some(config) = raw.as_object() else {
        issues.push("", "type", "must be object");
        return issues;
    };
    unknown_keys("", config, &CONFIG_KEYS, &mut issues);
    let Some(rollups) = config.get("rollups") else {
        issues.push("", "required", "must have required property 'rollups'");
        return issues;
    };
    let Some(rollups) = rollups.as_object() else {
        issues.push("/rollups", "type", "must be object");
        return issues;
    };
    if rollups.is_empty() {
        issues.push(
            "/rollups",
            "minProperties",
            "must NOT have fewer than 1 properties",
        );
    }
    for (name, rollup) in rollups {
        if !is_name(name) {
            issues.push(
                "/rollups",
                "pattern",
                format!("must match pattern \"{NAME_PATTERN}\""),
            );
            issues.push("/rollups", "propertyNames", "property name must be valid");
        }
        rollup_issues(&schema::member("/rollups", name), rollup, &mut issues);
    }
    issues
}

// rollup_issues lists what one rollup breaks of its closed object.
fn rollup_issues(at: &str, rollup: &Value, issues: &mut Issues) {
    let Some(members) = rollup.as_object() else {
        issues.push(at, "type", "must be object");
        return;
    };
    unknown_keys(at, members, &ROLLUP_KEYS, issues);
    for key in ["schema", "link", "function"] {
        if !members.contains_key(key) {
            issues.push(
                at,
                "required",
                format!("must have required property '{key}'"),
            );
        }
    }
    let function = members.get("function");
    let named = |listed: &[&str]| {
        function.is_none_or(|function| {
            function
                .as_str()
                .is_some_and(|function| listed.contains(&function))
        })
    };
    // allOf[0]: a function that reads a field takes one; any other none. A
    // function the rollup does not give holds the if, as ajv reads it.
    if named(&FIELD_FUNCTIONS) {
        if !members.contains_key("field") {
            issues.push(at, "if", "must match \"then\" schema");
            issues.push(at, "required", "must have required property 'field'");
        }
    } else if members.contains_key("field") {
        issues.push(at, "if", "must match \"else\" schema");
        issues.push(at, "not", "must NOT be valid");
    }
    // allOf[1] and allOf[2]: gatedStates and outcomes are all's and any's.
    for key in ["gatedStates", "outcomes"] {
        if !members.contains_key(key) {
            continue;
        }
        if let Some(given) = function.filter(|_| !named(&TERMINAL_FUNCTIONS)) {
            issues.push(at, "if", "must match \"then\" schema");
            let _ = given;
            issues.push(
                schema::member(at, "function"),
                "enum",
                format!(
                    "must be one of {}",
                    TERMINAL_FUNCTIONS.map(schema::quoted).join(", ")
                ),
            );
        }
    }
    if let Some(field) = members.get("field") {
        schema::text_issues(&schema::member(at, "field"), field, issues);
    }
    if let Some(function) = function {
        if !function
            .as_str()
            .is_some_and(|function| FUNCTIONS.contains(&function))
        {
            issues.push(
                schema::member(at, "function"),
                "enum",
                format!(
                    "must be one of {}",
                    FUNCTIONS.map(schema::quoted).join(", ")
                ),
            );
        }
    }
    if let Some(states) = members.get("gatedStates") {
        gated_issues(&schema::member(at, "gatedStates"), states, issues);
    }
    if let Some(link) = members.get("link") {
        pattern_issues(&schema::member(at, "link"), link, NAME_PATTERN, issues);
    }
    if let Some(outcomes) = members.get("outcomes") {
        outcome_list_issues(&schema::member(at, "outcomes"), outcomes, issues);
    }
    if let Some(name) = members.get("schema") {
        pattern_issues(&schema::member(at, "schema"), name, SCHEMA_PATTERN, issues);
    }
}

// gated_issues lists what gatedStates breaks of `{ type: array, minItems:
// 1, uniqueItems: true, items: { type: string, minLength: 1 } }`.
fn gated_issues(at: &str, value: &Value, issues: &mut Issues) {
    let Some(items) = value.as_array() else {
        issues.push(at, "type", "must be array");
        return;
    };
    if items.is_empty() {
        issues.push(at, "minItems", "must NOT have fewer than 1 items");
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

// outcome_list_issues lists what a list of outcomes breaks of `{ type:
// array, minItems: 1, uniqueItems: true, items: { enum: [...] } }`.
fn outcome_list_issues(at: &str, value: &Value, issues: &mut Issues) {
    let Some(items) = value.as_array() else {
        issues.push(at, "type", "must be array");
        return;
    };
    if items.is_empty() {
        issues.push(at, "minItems", "must NOT have fewer than 1 items");
    }
    for (i, item) in items.iter().enumerate() {
        if !item.as_str().is_some_and(|text| OUTCOMES.contains(&text)) {
            issues.push(
                format!("{at}/{i}"),
                "enum",
                format!("must be one of {}", OUTCOMES.map(schema::quoted).join(", ")),
            );
        }
    }
    // Its items name no type, so ajv compares them pair by pair.
    if let Some((j, i)) = untyped_duplicate(items) {
        issues.push(
            at,
            "uniqueItems",
            format!("must NOT have duplicate items (items ## {j} and {i} are identical)"),
        );
    }
}

/// Two equal items of a list whose items name no type, as ajv's
/// `uniqueItems` names them when it compares pair by pair: `(j, i)`, the
/// earlier and the later, from the first later item, reading from the end,
/// that equals an earlier one.
pub(crate) fn untyped_duplicate(items: &[Value]) -> Option<(usize, usize)> {
    for i in (0..items.len()).rev() {
        if let Some(j) = (0..i)
            .rev()
            .find(|j| crate::values::equal(Some(&items[i]), Some(&items[*j])))
        {
            return Some((j, i));
        }
    }
    None
}

// pattern_issues lists what a value breaks of `{ type: string, pattern }`.
fn pattern_issues(at: &str, value: &Value, pattern: &str, issues: &mut Issues) {
    let Some(text) = value.as_str() else {
        issues.push(at, "type", "must be string");
        return;
    };
    let matches = if pattern == SCHEMA_PATTERN {
        is_schema(text)
    } else {
        is_name(text)
    };
    if !matches {
        issues.push(at, "pattern", format!("must match pattern \"{pattern}\""));
    }
}

// is_name matches NAME_PATTERN: a lowercase letter, then at most 63
// letters and digits, ASCII alone.
fn is_name(text: &str) -> bool {
    let mut chars = text.chars();
    chars.next().is_some_and(|first| first.is_ascii_lowercase())
        && text.chars().count() <= 64
        && chars.all(|c| c.is_ascii_alphanumeric())
}

// is_schema matches SCHEMA_PATTERN: a letter, then at most 127 letters,
// digits, underscores and hyphens, ASCII alone.
fn is_schema(text: &str) -> bool {
    let mut chars = text.chars();
    chars
        .next()
        .is_some_and(|first| first.is_ascii_alphabetic())
        && text.chars().count() <= 128
        && chars.all(|c| c.is_ascii_alphanumeric() || c == '_' || c == '-')
}

// unknown_keys lists the members a closed object does not take, in the
// schema loader's words.
fn unknown_keys(at: &str, members: &Map<String, Value>, known: &[&str], issues: &mut Issues) {
    for key in members.keys().filter(|key| !known.contains(&key.as_str())) {
        issues.push(
            at,
            "additionalProperties",
            format!("unknown key {}", schema::quoted(key)),
        );
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn a_number_is_written_as_javascript_writes_it() {
        assert_eq!(number_json(8.5).to_string(), "8.5");
        assert_eq!(number_json(8.0).to_string(), "8");
        assert_eq!(number_json(-3.0).to_string(), "-3");
        assert_eq!(number_json(-0.0).to_string(), "0");
        assert_eq!(number_json(f64::NAN), Value::Null);
        assert_eq!(number_json(0.1 + 0.2).to_string(), "0.30000000000000004");
    }

    #[test]
    fn an_untyped_duplicate_is_named_as_ajv_names_it() {
        assert_eq!(untyped_duplicate(&[json!("a"), json!("a")]), Some((0, 1)));
        assert_eq!(
            untyped_duplicate(&[json!("a"), json!("b"), json!("a"), json!("b")]),
            Some((1, 3))
        );
        assert_eq!(untyped_duplicate(&[json!("a"), json!("b")]), None);
    }

    #[test]
    fn the_patterns_match_as_the_schemas_do() {
        assert!(is_name("byStatus") && !is_name("ByStatus") && !is_name("by-status"));
        assert!(is_schema("Task") && is_schema("work_items-2") && !is_schema("2tasks"));
    }
}
