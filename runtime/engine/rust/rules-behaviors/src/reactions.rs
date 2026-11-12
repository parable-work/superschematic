//! Reactions, rules that move Workflow statuses after a write commits (D16,
//! amended; D56), ported onto the pure core (D103) from the engine's
//! (`../behaviors/src/reactions.rs`): a task that starts moves its project
//! to active, a project whose tasks are all done is done, one whose task
//! fails is failed, a ticket marked urgent escalates.
//!
//! Reactions is a behavior by D103's test. It has no state of its own: no
//! field, no sub-table, no deadline and no operation; what it moves is
//! Workflow's, and only through Workflow's `transition` ([`Cx::call`] on
//! the row, [`Cx::call_on`] on another), whose guards run. It has rules over
//! how that state changes: each rule is one `when` and one `then`, and acts
//! only where it can. And it recurs: a project done when its tasks are, a
//! run failed when a step fails, an escalation when a field moves.
//!
//! A rule is one `when` and one `then`:
//!
//! - `enters S`: the write moved the row's status into `S`: a create or an
//!   insert that leaves it there, or an update that left it in another
//!   state, or in `S` entered at another time (`enteredAt` moved).
//! - `changes { field, to?, from? }`: the write moved the value of `field`,
//!   to `to` and from `from` when they are given. An insert moves a field
//!   from absent, which `from` never names; a delete moves nothing. On a
//!   table `field` names a field of the table by its name: an own field, a
//!   to-one relation (`project`, which hears the key its column holds), or
//!   a field a behavior is anchored on (`status`, which hears the behavior's
//!   value field). The engine's qualified form is taken too, a use's id, a
//!   dot and a field its behavior declares, with a member path after it
//!   (`Workflow.status`, `Lease.ended.reason`), the use the longest id that
//!   prefixes it, which the recorded scenarios write.
//! - `allTerminal { schema, link, outcomes? }`: a write of a row of
//!   `schema` whose to-one relation `link` points at this row, now, after
//!   the write or before it, left every row of `schema` that points here in
//!   a terminal state of its own Workflow, at least one, with an outcome
//!   the list holds when there is one. The rows are read a page at a time
//!   ([`View::read_linked`]) as the host has them.
//! - `anyTerminal { schema, link, outcomes }`: a write of a row of `schema`
//!   that points here through `link` entered a terminal state whose outcome
//!   the list holds, or moved the link here while the row is in one.
//! - `then { transition, link? }`: move the row, or the row its to-one
//!   relation `link` points at, to the state.
//!
//! For `allTerminal` and `anyTerminal`, a state the linking type's Workflow
//! names in `finished` counts as a terminal state does, with the outcome
//! `finished` names (D113): a task that is done and may be reopened closes
//! its project. A row that leaves such a state moves nothing by itself, and
//! one that enters it again is heard again.
//!
//! A rule acts only where it can: a target already in the state, with no
//! transition to it from where it is, or whose guards veto the transition
//! is left as it is, and so is a target that is gone, an enters rule on the
//! row itself once the row has moved on from the state, and a changes rule
//! with `to` on the row itself once the field holds another value. None of
//! them refuses the write it heard. A move refused for anything else (a
//! transition's permission the worker lacks, `forbidden`) refuses the change
//! command, which fails its message (D68), and a target without Workflow, a
//! linking type without one, or a state its Workflow lacks is a defect.
//!
//! What changed from the engine, and why. The engine's runner handed it
//! each committed event of the log after the commit; on a table every
//! write of a row, whoever makes it, reaches it from the table's change
//! queue, which the API's worker runs after the commit (D103), so it reads
//! what moved from the write's images, the row before and after it, never
//! an event's patch, and a raw SQL write sets a rule off as any write does.
//! It hears its own row's writes ([`Behavior::heard`], `Heard::Own`) and,
//! for allTerminal and anyTerminal, the writes of the rows that point at it
//! ([`Behavior::listens`], `Heard::Linked`), which the core reaches through
//! the linking table's to-one relation: the engine's Links link is that
//! relation on a table. The runner's principal is the worker's caller. A
//! chain of moves has no depth limit: the config's cycle check bounds a
//! row's own rules, and each move is a write its table's queue hears. A
//! failure fails one message, where the engine halted the subscription.
//! `holds` (Rollups), `revised` (Revisions and Branches), a link that holds
//! a list of targets, the value store, and a field a behavior computes at a
//! read (`Timers.deadlines`), which no column holds and no write carries,
//! wait for later steps, and the core refuses a rule that names them.
//!
//! The config is checked at define, first as the declaration's configSchema
//! holds it, each issue at its path in the schema loader's words
//! ([`ConfigError::Schema`]), then each rule held to the type, its own
//! fields, its relations and its Workflow, in the engine's words where they
//! apply ([`ConfigError::Rule`]): the Workflow `status` names, each field a
//! changes rule names, the Workflow each rule that reads the status reads,
//! its states and transitions, a terminal rule on the type itself against
//! its own relation, a then's link against the type's to-one relations, and
//! the cycles of its rules on the row itself. The core runs no JSON Schema,
//! so the schema's checks are written here, held to the declaration file by
//! this crate's tests. What reads another type (the linking type's relation
//! and Workflow, a then's target's Workflow) is the build's to check and the
//! run's, since a type's define check sees its own spec alone.
//!
//! configChange: rules hold no state, so any change is allowed, and
//! Reactions may be added to and removed from a type with rows.

use std::collections::HashSet;

use serde_json::{json, Map, Value};
use superschematic_rules::{
    key_of, Anchor, Behavior, Code, ConfigCx, ConfigError, ConfigTarget, Cx, Declaration,
    FieldSpec, Heard, Hook, Listen, RowChange, RowImage, TypeSpec, UseSpec, WriteKind,
};

use crate::declared;
use crate::schema::{self, Issues};
use crate::workflow::{self, state_outcome, WorkflowOutcome};

/// The behavior's name, as its declaration has it.
pub const NAME: &str = "Reactions";

/// Reactions' declaration, the file the compiler registers and the engine
/// embeds, read in place: what a host describes the behavior with.
pub const DECLARATION: &str = include_str!("../../behaviors/declarations/Reactions.behavior.json");

// The members the configSchema's closed objects take.
const CONFIG_KEYS: [&str; 2] = ["rules", "status"];
const RULE_KEYS: [&str; 2] = ["then", "when"];
const THEN_KEYS: [&str; 2] = ["link", "transition"];
const WHEN_KEYS: [&str; 6] = [
    "allTerminal",
    "anyTerminal",
    "changes",
    "enters",
    "holds",
    "revised",
];
const TERMINAL_KEYS: [&str; 3] = ["link", "outcomes", "schema"];
const CHANGES_KEYS: [&str; 3] = ["field", "from", "to"];
const REVISED_KEYS: [&str; 1] = ["link"];

// The patterns and bounds the configSchema holds names to.
const LINK_PATTERN: &str = "^[a-z][A-Za-z0-9]{0,63}$";
const SCHEMA_PATTERN: &str = "^[A-Za-z][A-Za-z0-9_-]{0,127}$";
const FIELD_PATTERN: &str = r"^[^.]+(\.[^.]+)*$";
const FIELD_MAX_LENGTH: usize = 512;
const RULES_MAX_ITEMS: usize = 64;

// The outcomes, in the order the configSchema's enum lists them.
const OUTCOMES: [&str; 3] = ["success", "failure", "neutral"];

/// The rows an allTerminal or anyTerminal rule hears: those of `schema`
/// whose to-one relation `link` points at this row.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ReactionsTerminal {
    pub schema: String,
    pub link: String,
    /// The outcomes a terminal state, or a state its Workflow's finished
    /// names (D113), must have to count; every outcome when `None`, which
    /// only allTerminal allows.
    pub outcomes: Option<Vec<WorkflowOutcome>>,
}

/// A move of a field a changes rule hears: to `to` and from `from`, when
/// they are given.
#[derive(Clone, Debug, PartialEq)]
pub struct ReactionsChanges {
    /// The field as the rule names it: a field of the table by its name, or
    /// the engine's qualified form.
    pub field: String,
    pub to: Option<Value>,
    pub from: Option<Value>,
}

/// What sets a rule off.
#[derive(Clone, Debug, PartialEq)]
pub enum ReactionsWhen {
    Enters(String),
    Changes(ReactionsChanges),
    AllTerminal(ReactionsTerminal),
    AnyTerminal(ReactionsTerminal),
}

impl ReactionsWhen {
    /// The rows an allTerminal or anyTerminal rule hears, with the rule's
    /// form; `None` for the other forms.
    pub fn terminal(&self) -> Option<(&'static str, &ReactionsTerminal)> {
        match self {
            ReactionsWhen::AllTerminal(terminal) => Some(("allTerminal", terminal)),
            ReactionsWhen::AnyTerminal(terminal) => Some(("anyTerminal", terminal)),
            _ => None,
        }
    }
}

/// What a rule does: move the row, or the row its to-one relation `link`
/// points at, to a state.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ReactionsThen {
    pub transition: String,
    pub link: Option<String>,
}

/// One rule of a Reactions config.
#[derive(Clone, Debug, PartialEq)]
pub struct ReactionsRule {
    pub when: ReactionsWhen,
    pub then: ReactionsThen,
}

/// A field a changes rule names, resolved: an own field (`use_id` `None`,
/// `field` its spec name: `projectId` for the relation `project`) or a
/// use's declared field, with a member path below it.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ReactionsField {
    pub use_id: Option<String>,
    pub field: String,
    /// The members below the field's value the rule reads, outermost first;
    /// none for the value itself.
    pub members: Vec<String>,
}

/// Reactions' config, parsed: the rules, the Workflow they read, and each
/// field a changes rule names, resolved.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct ReactionsConfig {
    pub rules: Vec<ReactionsRule>,
    /// The id of the Workflow use the rules read and move on the type: the
    /// config's status, or the type's one Workflow; `None` when no rule
    /// reads one.
    pub status: Option<String>,
    /// Each field a changes rule names, by the name the rule gives it.
    pub fields: Vec<(String, ReactionsField)>,
}

impl ReactionsConfig {
    /// Parses and checks a config as a type gives it: as the declaration's
    /// configSchema holds it, each issue at its path in the schema loader's
    /// words, then each rule held to the type, its fields, its relations
    /// and its Workflow, a rule's reason in the engine's words where they
    /// apply, which the core prefixes with the type and the behavior.
    pub fn parse(raw: &Value, target: &ConfigTarget<'_>) -> Result<ReactionsConfig, ConfigError> {
        let issues = config_issues(raw);
        if !issues.is_empty() {
            return Err(ConfigError::Schema(issues.into_issues()));
        }
        let spec = Spec::of(target);
        let status = spec.status(raw.get("status").and_then(Value::as_str))?;
        let read: Vec<Read> = raw
            .get("rules")
            .and_then(Value::as_array)
            .into_iter()
            .flatten()
            .map(rule_of)
            .collect();
        let rules: Vec<(usize, &ReactionsRule)> = read
            .iter()
            .enumerate()
            .filter_map(|(index, read)| match read {
                Read::Rule(rule) => Some((index + 1, rule)),
                _ => None,
            })
            .collect();
        let fields = fields_of(&rules, &spec)?;
        for (index, read) in read.iter().enumerate() {
            let n = index + 1;
            match read {
                Read::Holds(name) => return Err(format!(
                    "rule {n}: when.holds names rollup {name}: a rule that hears a rollup waits for Rollups on the core (D103's order of the work, step 4)"
                ).into()),
                Read::Revised(link) => return Err(format!(
                    "rule {n}: when.revised names link {link}: a rule that hears a revision or a release waits for Revisions and Branches on the core (D103's order of the work, step 4)"
                ).into()),
                Read::Rule(_) => {}
            }
        }
        for (n, rule) in &rules {
            for place in places(rule) {
                match &status {
                    Status::Of(_) => {}
                    Status::None => {
                        return Err(format!(
                            "rule {n}: {place} reads the status of Workflow, which the type does not list"
                        )
                        .into())
                    }
                    Status::Several(ids) => return Err(format!(
                        "rule {n}: {place} reads the status of Workflow, and the type has several ({ids}): status names the field whose Workflow it reads"
                    )
                    .into()),
                }
            }
        }
        check_rules(&rules, &fields, &spec, &status)?;
        let reads_status = rules.iter().any(|(_, rule)| !places(rule).is_empty());
        Ok(ReactionsConfig {
            rules: rules.into_iter().map(|(_, rule)| rule.clone()).collect(),
            status: match status {
                Status::Of(entry) if reads_status => Some(entry.id().to_owned()),
                _ => None,
            },
            fields,
        })
    }

    /// The field a changes rule names, as the config resolved it.
    pub fn field(&self, name: &str) -> Option<&ReactionsField> {
        self.fields
            .iter()
            .find(|(known, _)| known == name)
            .map(|(_, field)| field)
    }
}

/// Reactions: rules that move Workflow statuses after a write commits.
#[derive(Clone, Copy, Debug, Default)]
pub struct Reactions;

impl Behavior for Reactions {
    type Config = ReactionsConfig;

    // The declaration file says it hears its row's writes (D103).
    fn declaration(&self) -> Declaration {
        declared::declaration(DECLARATION)
    }

    fn parse_config(
        &self,
        raw: &Value,
        target: &ConfigTarget<'_>,
    ) -> Result<ReactionsConfig, ConfigError> {
        ReactionsConfig::parse(raw, target)
    }

    // Rules hold no state: any change is allowed, and Reactions joins and
    // leaves a type with rows.
    fn config_change(
        &self,
        _cx: &ConfigCx<'_>,
        _before: Option<&ReactionsConfig>,
        _after: Option<&ReactionsConfig>,
    ) -> Hook<Option<String>> {
        Ok(None)
    }

    // An allTerminal or anyTerminal rule hears the writes of the rows of
    // its schema through its link, each once, in rule order.
    fn listens(&self, config: &ReactionsConfig) -> Vec<Listen> {
        let mut listens: Vec<Listen> = Vec::new();
        for (_, terminal) in config.rules.iter().filter_map(|rule| rule.when.terminal()) {
            let listen = Listen::new(&terminal.schema, &terminal.link);
            if !listens.contains(&listen) {
                listens.push(listen);
            }
        }
        listens
    }

    // A write of the row runs its enters and changes rules; a write of a
    // row that points here runs the allTerminal and anyTerminal rules over
    // its type and link, each in config order.
    fn heard(&self, cx: &Cx<'_, ReactionsConfig>, heard: &Heard<'_>) -> Hook<()> {
        match heard {
            Heard::Own { change } => own(cx, change),
            Heard::Linked {
                type_name,
                link,
                change,
                ..
            } => linked(cx, type_name, link, change),
            _ => Ok(()),
        }
    }
}

// own runs the rules a write of the row sets off: an enters rule whose state
// the write moved the status into, and a changes rule whose field it moved.
fn own(cx: &Cx<'_, ReactionsConfig>, change: &RowChange) -> Hook<()> {
    let config = cx.config();
    for rule in &config.rules {
        match &rule.when {
            ReactionsWhen::Enters(state) => {
                let Some(status) = &config.status else {
                    continue;
                };
                if entered(change, status) != Some(state.as_str()) {
                    continue;
                }
                // On the row itself, it acts only while the row is still in
                // the state it entered.
                let from = rule.then.link.is_none().then_some(state.as_str());
                apply(cx, &rule.then, from)?;
            }
            ReactionsWhen::Changes(changes) => {
                let Some(field) = config.field(&changes.field) else {
                    continue;
                };
                if !changes_fired(change, changes, field) {
                    continue;
                }
                // As an enters rule does, one on the row itself acts only
                // while the field still holds what it moved to.
                if let (None, Some(to)) = (&rule.then.link, &changes.to) {
                    if !equal(now_of(cx, field).as_ref(), Some(to)) {
                        continue;
                    }
                }
                apply(cx, &rule.then, None)?;
            }
            _ => {}
        }
    }
    Ok(())
}

// linked runs the allTerminal and anyTerminal rules over a type and link a
// write of one of its rows that points, or pointed, at the row sets off.
fn linked(
    cx: &Cx<'_, ReactionsConfig>,
    type_name: &str,
    link: &str,
    change: &RowChange,
) -> Hook<()> {
    let config = cx.config();
    for rule in &config.rules {
        let Some((form, terminal)) = rule.when.terminal() else {
            continue;
        };
        if terminal.schema != type_name || terminal.link != link {
            continue;
        }
        let linking = Linking::of(cx, form, terminal)?;
        let Some(linking) = linking else {
            continue;
        };
        let fired = if form == "anyTerminal" {
            any_terminal(cx, &linking, terminal, change)
        } else {
            all_terminal(cx, &linking, terminal)?
        };
        if fired {
            apply(cx, &rule.then, None)?;
        }
    }
    Ok(())
}

// The type of the rows a terminal rule hears, as the run reads it: the own
// field its link's key is held in, and its one Workflow's use id and config.
struct Linking {
    field: String,
    use_id: String,
    flow: Value,
}

impl Linking {
    // of reads the linking type's spec: None for a type no spec names or
    // one with no such relation, which the core reaches no row through. A
    // type with no Workflow, or several, is a defect.
    fn of(
        cx: &Cx<'_, ReactionsConfig>,
        form: &str,
        terminal: &ReactionsTerminal,
    ) -> Hook<Option<Linking>> {
        let Some(spec) = cx.read_spec(&terminal.schema)? else {
            return Ok(None);
        };
        let Some(field) = spec.relation(&terminal.link) else {
            return Ok(None);
        };
        let workflows = workflows_of(spec);
        let entry = match workflows.as_slice() {
            [entry] => *entry,
            [] => {
                return Err(cx.defect(format!(
                    "{form} names {}, which does not compose Workflow, so its rows have no terminal state",
                    terminal.schema
                )))
            }
            _ => {
                return Err(cx.defect(format!(
                    "{form} names {}, which has several Workflows ({}); a rule reads a linking type's one Workflow",
                    terminal.schema,
                    ids_of(&workflows)
                )))
            }
        };
        Ok(Some(Linking {
            field: field.name.clone(),
            use_id: entry.id().to_owned(),
            flow: entry.config_or_empty(),
        }))
    }
}

// all_terminal reports whether every row of the rule's schema that points at
// the row through its link is in a terminal state of its Workflow, or one
// its finished names (D113), with an outcome the rule counts, and there is
// at least one: read a page at a time as the host has them.
fn all_terminal(
    cx: &Cx<'_, ReactionsConfig>,
    linking: &Linking,
    terminal: &ReactionsTerminal,
) -> Hook<bool> {
    let key = cx.key().unwrap_or_default().to_owned();
    let mut after: Option<String> = None;
    let mut any = false;
    loop {
        let page = cx.read_linked(&terminal.schema, &linking.field, &key, after.as_deref())?;
        for row in &page.rows {
            let status = workflow::status_of(&row.row, &linking.use_id);
            if !counts(&linking.flow, status, terminal.outcomes.as_deref()) {
                return Ok(false);
            }
            any = true;
        }
        if !page.more {
            return Ok(any);
        }
        after = page.rows.last().map(|row| row.key.clone());
    }
}

// any_terminal reports whether a write of a row of the rule's schema that
// points at the row now entered a terminal state, or one its Workflow's
// finished names (D113), whose outcome the rule lists, or moved the link
// here while its status is one. A link's key is read as a command names a
// row (key_of), so a number key matches its text.
fn any_terminal(
    cx: &Cx<'_, ReactionsConfig>,
    linking: &Linking,
    terminal: &ReactionsTerminal,
    change: &RowChange,
) -> bool {
    let Some(after) = &change.after else {
        return false;
    };
    let key = cx.key().unwrap_or_default();
    if after.own_field(&linking.field).and_then(key_of).as_deref() != Some(key) {
        return false;
    }
    let outcomes = terminal.outcomes.as_deref();
    if let Some(state) = entered(change, &linking.use_id) {
        return counts(&linking.flow, Some(state), outcomes);
    }
    let was = change
        .before
        .as_ref()
        .and_then(|before| before.own_field(&linking.field))
        .and_then(key_of);
    if was.as_deref() == Some(key) {
        return false;
    }
    let status = after
        .owned(&linking.use_id, workflow::STATUS)
        .and_then(Value::as_str);
    counts(&linking.flow, status, outcomes)
}

// counts reports whether a status is a terminal state, or a state finished
// names (D113), whose outcome the list holds; every such state counts when
// there is no list.
fn counts(flow: &Value, status: Option<&str>, outcomes: Option<&[WorkflowOutcome]>) -> bool {
    status
        .and_then(|status| state_outcome(flow, status))
        .is_some_and(|outcome| outcomes.is_none_or(|listed| listed.contains(&outcome)))
}

// entered reads the state a write moved a Workflow's status into: the
// status after it, when there was none before, another one, or the same one
// entered at another time.
fn entered<'c>(change: &'c RowChange, use_id: &str) -> Option<&'c str> {
    let after = change.after.as_ref()?;
    let status = after.owned(use_id, workflow::STATUS)?.as_str()?;
    match &change.before {
        None => Some(status),
        Some(before) => {
            let moved = before
                .owned(use_id, workflow::STATUS)
                .and_then(Value::as_str)
                != Some(status)
                || before.owned(use_id, workflow::ENTERED_AT)
                    != after.owned(use_id, workflow::ENTERED_AT);
            moved.then_some(status)
        }
    }
}

// changes_fired reports whether a write moved a changes rule's field: to
// `to` and from `from`, when the rule gives them. An insert moves it from
// absent, which from never names; a delete moves nothing.
fn changes_fired(change: &RowChange, changes: &ReactionsChanges, field: &ReactionsField) -> bool {
    let after = value_of(change.after.as_ref(), field);
    match change.write {
        WriteKind::Insert => {
            after.is_some() && changes.from.is_none() && wanted(after, changes.to.as_ref())
        }
        WriteKind::Update => {
            let before = value_of(change.before.as_ref(), field);
            !equal(before, after)
                && wanted(after, changes.to.as_ref())
                && wanted(before, changes.from.as_ref())
        }
        _ => false,
    }
}

// value_of reads a changes rule's field of an image, and the member path
// below it: None where it has no value.
fn value_of<'i>(image: Option<&'i RowImage>, field: &ReactionsField) -> Option<&'i Value> {
    let image = image?;
    let value = match &field.use_id {
        None => image.own_field(&field.field),
        Some(use_id) => image.owned(use_id, &field.field),
    };
    member_of(value, &field.members)
}

// now_of reads a changes rule's field of the row as the run has it.
fn now_of(cx: &Cx<'_, ReactionsConfig>, field: &ReactionsField) -> Option<Value> {
    let value = match &field.use_id {
        None => cx.own_field(&field.field),
        Some(use_id) => cx.get_of(use_id, &field.field),
    };
    member_of(value.as_ref(), &field.members).cloned()
}

// member_of reads a member path below a value: the value itself for none,
// None where a step is not an object or lacks the member.
fn member_of<'v>(value: Option<&'v Value>, members: &[String]) -> Option<&'v Value> {
    let mut at = value?;
    for member in members {
        at = at.as_object()?.get(member)?;
    }
    Some(at)
}

// equal compares two values that may be absent as the engine's jsonEqual
// does: objects by member, arrays in order, numbers by value; absent equals
// only absent.
fn equal(a: Option<&Value>, b: Option<&Value>) -> bool {
    match (a, b) {
        (None, None) => true,
        (Some(a), Some(b)) => json_equal(a, b),
        _ => false,
    }
}

fn json_equal(a: &Value, b: &Value) -> bool {
    match (a, b) {
        (Value::Number(a), Value::Number(b)) => match (a.as_f64(), b.as_f64()) {
            (Some(a), Some(b)) => a == b,
            _ => a == b,
        },
        (Value::Array(a), Value::Array(b)) => {
            a.len() == b.len() && a.iter().zip(b).all(|(a, b)| json_equal(a, b))
        }
        (Value::Object(a), Value::Object(b)) => {
            a.len() == b.len()
                && a.iter()
                    .all(|(key, a)| b.get(key).is_some_and(|b| json_equal(a, b)))
        }
        _ => a == b,
    }
}

// wanted reports whether a value is the one a rule names, any value when it
// names none.
fn wanted(value: Option<&Value>, wanted: Option<&Value>) -> bool {
    wanted.is_none() || equal(value, wanted)
}

// apply moves a rule's target to its state, where it can: the row, through
// the Workflow the config reads, or the row its to-one relation points at,
// through that type's one Workflow. `from`, for an enters rule on the row
// itself, is the state the row must still be in.
fn apply(cx: &Cx<'_, ReactionsConfig>, then: &ReactionsThen, from: Option<&str>) -> Hook<()> {
    let (type_name, key, use_id, flow) = match &then.link {
        None => {
            let Some(use_id) = &cx.config().status else {
                return Err(cx.defect("a then reached its move with no Workflow to move"));
            };
            let spec = cx.read_spec(cx.type_name())?;
            let flow = spec
                .and_then(|spec| spec.use_of(use_id))
                .map_or(Value::Null, |(_, entry)| entry.config_or_empty());
            (
                cx.type_name().to_owned(),
                cx.key().unwrap_or_default().to_owned(),
                use_id.clone(),
                flow,
            )
        }
        Some(link) => {
            let Some((field, target)) = relation_of(cx, link)? else {
                return Ok(());
            };
            let Some(key) = cx.own_field(&field).as_ref().and_then(key_of) else {
                return Ok(());
            };
            let Some(spec) = cx.read_spec(&target)? else {
                return Ok(());
            };
            let workflows = workflows_of(spec);
            let entry = match workflows.as_slice() {
                [entry] => *entry,
                [] => {
                    return Err(cx.defect(format!(
                        "a rule moves {target} {key} to {}, but {target} does not compose Workflow",
                        then.transition
                    )))
                }
                _ => {
                    return Err(cx.defect(format!(
                        "a rule moves {target} {key} to {}, and {target} has several Workflows ({}); a rule moves a type's one Workflow",
                        then.transition,
                        ids_of(&workflows)
                    )))
                }
            };
            (target, key, entry.id().to_owned(), entry.config_or_empty())
        }
    };
    let states = strings(flow.get("states"));
    if !states.contains(&then.transition) {
        return Err(cx.defect(format!(
            "a rule moves {type_name} {key} to {}, which is not a state of {type_name}'s Workflow ({})",
            then.transition,
            states.join(", ")
        )));
    }
    let Some(row) = cx.row_of(&type_name, &key)? else {
        return Ok(());
    };
    let Some(status) = workflow::status_of(&row, &use_id) else {
        return Ok(());
    };
    if status == then.transition || from.is_some_and(|from| status != from) {
        return Ok(());
    }
    let allowed = flow
        .get("transitions")
        .and_then(Value::as_array)
        .into_iter()
        .flatten()
        .any(|edge| {
            edge.get("from").and_then(Value::as_str) == Some(status)
                && edge.get("to").and_then(Value::as_str) == Some(then.transition.as_str())
        });
    if !allowed {
        return Ok(());
    }
    let params = json!({ "to": then.transition });
    let moved = if then.link.is_none() {
        cx.call(&use_id, workflow::TRANSITION, &params)
    } else {
        cx.call_on(&type_name, &key, &use_id, workflow::TRANSITION, &params)
    };
    match moved {
        Ok(_) => Ok(()),
        // A guard's refusal leaves the target as it is.
        Err(stop)
            if stop
                .refusal()
                .is_some_and(|refusal| refusal.code == Code::Vetoed) =>
        {
            Ok(())
        }
        Err(stop) => Err(stop),
    }
}

// relation_of reads a to-one relation of the row's type: the own field that
// holds its target's key, and the type it points at.
fn relation_of(cx: &Cx<'_, ReactionsConfig>, link: &str) -> Hook<Option<(String, String)>> {
    let Some(spec) = cx.read_spec(cx.type_name())? else {
        return Ok(None);
    };
    Ok(spec.relation(link).and_then(|field| {
        field
            .relation
            .as_ref()
            .map(|relation| (field.name.clone(), relation.type_name.clone()))
    }))
}

// workflows_of lists a type's Workflow uses, in list order.
fn workflows_of(spec: &TypeSpec) -> Vec<&UseSpec> {
    spec.uses
        .iter()
        .filter(|entry| entry.behavior == workflow::NAME)
        .collect()
}

fn ids_of(uses: &[&UseSpec]) -> String {
    let ids: Vec<&str> = uses.iter().map(|entry| entry.id()).collect();
    listed(&ids)
}

// listed joins names for a message, `none` for none.
fn listed(names: &[&str]) -> String {
    if names.is_empty() {
        "none".to_owned()
    } else {
        names.join(", ")
    }
}

// strings reads a list of strings, as JavaScript's join writes each.
fn strings(value: Option<&Value>) -> Vec<String> {
    value
        .and_then(Value::as_array)
        .into_iter()
        .flatten()
        .map(text_of)
        .collect()
}

// text_of reads a config's value as JavaScript's join writes it.
fn text_of(value: &Value) -> String {
    match value {
        Value::String(text) => text.clone(),
        other => serde_json::to_string(other).unwrap_or_default(),
    }
}

// ---- the config's rules ----

// A rule as the configSchema let it through: one the core runs, or a holds
// or revised rule, which waits for a later step.
enum Read {
    Rule(ReactionsRule),
    Holds(String),
    Revised(String),
}

fn text(value: &Value, key: &str) -> String {
    value
        .get(key)
        .and_then(Value::as_str)
        .unwrap_or_default()
        .to_owned()
}

// outcomes_of reads a list of outcomes the configSchema let through.
fn outcomes_of(value: Option<&Value>) -> Option<Vec<WorkflowOutcome>> {
    value.map(|outcomes| {
        outcomes
            .as_array()
            .into_iter()
            .flatten()
            .filter_map(Value::as_str)
            .filter_map(WorkflowOutcome::parse)
            .collect()
    })
}

// rule_of reads one rule the configSchema let through.
fn rule_of(raw: &Value) -> Read {
    let when = raw.get("when").cloned().unwrap_or(Value::Null);
    let terminal = |value: &Value| ReactionsTerminal {
        schema: text(value, "schema"),
        link: text(value, "link"),
        outcomes: outcomes_of(value.get("outcomes")),
    };
    let when = if let Some(state) = when.get("enters") {
        ReactionsWhen::Enters(text_of(state))
    } else if let Some(changes) = when.get("changes") {
        ReactionsWhen::Changes(ReactionsChanges {
            field: text(changes, "field"),
            to: changes.get("to").cloned(),
            from: changes.get("from").cloned(),
        })
    } else if let Some(all) = when.get("allTerminal") {
        ReactionsWhen::AllTerminal(terminal(all))
    } else if let Some(any) = when.get("anyTerminal") {
        ReactionsWhen::AnyTerminal(terminal(any))
    } else if let Some(rollup) = when.get("holds") {
        return Read::Holds(text_of(rollup));
    } else {
        return Read::Revised(
            when.get("revised")
                .map(|revised| text(revised, "link"))
                .unwrap_or_default(),
        );
    };
    let then = raw.get("then").cloned().unwrap_or(Value::Null);
    Read::Rule(ReactionsRule {
        when,
        then: ReactionsThen {
            transition: text(&then, "transition"),
            link: then.get("link").and_then(Value::as_str).map(str::to_owned),
        },
    })
}

// places lists where a rule reads the status of the Workflow the config
// reads: an enters rule, and a then with no link.
fn places(rule: &ReactionsRule) -> Vec<&'static str> {
    let mut places = Vec::new();
    if matches!(rule.when, ReactionsWhen::Enters(_)) {
        places.push("when.enters");
    }
    if rule.then.link.is_none() {
        places.push("then");
    }
    places
}

/// The Workflow the rules read, as the config's `status` and the type's uses
/// resolve it.
enum Status<'a> {
    /// The type lists no Workflow.
    None,
    /// The Workflow use the config names, or the type's one.
    Of(&'a UseSpec),
    /// The type lists several, by id, and the config names none.
    Several(String),
}

// The type a config is held to: its name, its own fields, its relations, its
// uses and the declarations of their behaviors.
struct Spec<'a> {
    name: &'a str,
    fields: &'a [FieldSpec],
    uses: &'a [UseSpec],
    target: ConfigTarget<'a>,
}

impl<'a> Spec<'a> {
    fn of(target: &ConfigTarget<'a>) -> Self {
        Spec {
            name: target.type_name(),
            fields: target.fields(),
            uses: target.uses(),
            target: *target,
        }
    }

    // status resolves the Workflow the rules read: the one status names, or
    // the type's one.
    fn status(&self, named: Option<&str>) -> Result<Status<'a>, ConfigError> {
        let workflows: Vec<&'a UseSpec> = self
            .uses
            .iter()
            .filter(|entry| entry.behavior == workflow::NAME)
            .collect();
        match named {
            Some(named) => match workflows.iter().find(|entry| entry.id() == named) {
                Some(entry) => Ok(Status::Of(entry)),
                None => Err(format!(
                    "status \"{named}\" is not a Workflow of the type (its Workflows: {})",
                    ids_of(&workflows)
                )
                .into()),
            },
            None => Ok(match workflows.as_slice() {
                [] => Status::None,
                [entry] => Status::Of(entry),
                _ => Status::Several(ids_of(&workflows)),
            }),
        }
    }

    // names lists the fields a changes rule names on the type: its own as a
    // rule names them, a relation by its name, then the field anchors.
    fn names(&self) -> Vec<&'a str> {
        let mut names: Vec<&str> = self
            .fields
            .iter()
            .map(|field| {
                field
                    .relation
                    .as_ref()
                    .map_or(field.name.as_str(), |relation| relation.name.as_str())
            })
            .collect();
        names.extend(self.uses.iter().filter_map(UseSpec::field));
        names
    }

    // relation is the type a to-one relation of the type points at, by the
    // relation's name.
    fn relation(&self, name: &str) -> Option<&'a str> {
        self.fields.iter().find_map(|field| {
            field
                .relation
                .as_ref()
                .filter(|relation| relation.name == name)
                .map(|relation| relation.type_name.as_str())
        })
    }

    // relation_names lists the names of the type's to-one relations, in
    // field order.
    fn relation_names(&self) -> Vec<&'a str> {
        self.fields
            .iter()
            .filter_map(|field| field.relation.as_ref())
            .map(|relation| relation.name.as_str())
            .collect()
    }
}

// fields_of holds the fields changes rules name to the type and returns each
// resolved: a field of the table by its name (an own field, a relation, a
// field anchor), or the engine's qualified form, the longest of the type's
// use ids that prefixes it, a dot, a field its behavior declares, and the
// members below. It refuses a field the behavior computes at a read, which
// no column holds and no write carries, and Workflow's enteredAt, which
// every move of the status moves.
fn fields_of(
    rules: &[(usize, &ReactionsRule)],
    spec: &Spec<'_>,
) -> Result<Vec<(String, ReactionsField)>, ConfigError> {
    let mut fields: Vec<(String, ReactionsField)> = Vec::new();
    for (n, rule) in rules {
        let ReactionsWhen::Changes(changes) = &rule.when else {
            continue;
        };
        let at = format!("rule {n}: when.changes");
        let name = changes.field.as_str();
        let resolved = if name.contains('.') {
            qualified(&at, name, spec)?
        } else if let Some(field) = spec
            .fields
            .iter()
            .find(|field| field.relation.is_none() && field.name == name)
        {
            ReactionsField {
                use_id: None,
                field: field.name.clone(),
                members: Vec::new(),
            }
        } else if let Some(field) = spec.fields.iter().find(|field| {
            field
                .relation
                .as_ref()
                .is_some_and(|relation| relation.name == name)
        }) {
            ReactionsField {
                use_id: None,
                field: field.name.clone(),
                members: Vec::new(),
            }
        } else if let Some(entry) = spec.uses.iter().find(|entry| entry.field() == Some(name)) {
            let value = spec
                .target
                .declaration(&entry.behavior)
                .and_then(|declaration| declaration.value.clone())
                .unwrap_or_default();
            ReactionsField {
                use_id: Some(entry.id().to_owned()),
                field: value,
                members: Vec::new(),
            }
        } else {
            return Err(format!(
                "{at} names {name}, which is not a field of {} ({})",
                spec.name,
                listed(&spec.names())
            )
            .into());
        };
        if !fields.iter().any(|(known, _)| known == name) {
            fields.push((name.to_owned(), resolved));
        }
    }
    Ok(fields)
}

// qualified resolves the engine's qualified form of a changes rule's field:
// a use's id, a dot, a field its behavior declares, and members below it.
fn qualified(at: &str, name: &str, spec: &Spec<'_>) -> Result<ReactionsField, ConfigError> {
    let entry = spec
        .uses
        .iter()
        .filter(|entry| {
            let id = entry.id();
            name.len() > id.len() && name.starts_with(id) && name.as_bytes()[id.len()] == b'.'
        })
        .max_by_key(|entry| entry.id().len());
    let Some(entry) = entry else {
        let ids: Vec<&str> = spec.uses.iter().map(UseSpec::id).collect();
        return Err(format!(
            "{at} names {name}, which is not a field of a behavior the type lists ({})",
            listed(&ids)
        )
        .into());
    };
    let behavior = entry.behavior.as_str();
    let mut path = name[entry.id().len() + 1..].split('.');
    let field = path.next().unwrap_or_default().to_owned();
    let members: Vec<String> = path.map(str::to_owned).collect();
    let declared: Vec<&str> = spec
        .target
        .declaration(behavior)
        .map(|declaration| declaration.fields.iter().map(String::as_str).collect())
        .unwrap_or_default();
    if !declared.contains(&field.as_str()) {
        return Err(format!(
            "{at} names {name}, but {behavior} declares no field {field} ({})",
            listed(&declared)
        )
        .into());
    }
    if spec
        .target
        .declaration(behavior)
        .is_some_and(|declaration| declaration.is_computed(&field))
    {
        return Err(format!(
            "{at} names {name}, which {behavior} computes at a read and no column holds, so a write of the row does not carry it: a rule on it waits for a later step (D103)"
        )
        .into());
    }
    if behavior == workflow::NAME && field == workflow::ENTERED_AT {
        let status = match &entry.anchor {
            Anchor::Type => format!("{}.{}", entry.id(), workflow::STATUS),
            Anchor::Field(field) => field.clone(),
        };
        return Err(format!(
            "{at} names {name}, which every move of the status moves; a rule on {status} hears those"
        )
        .into());
    }
    Ok(ReactionsField {
        use_id: Some(entry.id().to_owned()),
        field,
        members,
    })
}

// A Workflow config as the type holds it, when it has the shape Workflow
// takes; one of another shape is Workflow's to refuse.
struct Flow {
    states: Vec<String>,
    transitions: Vec<(Option<String>, Option<String>)>,
}

fn flow_of(entry: &UseSpec) -> Option<Flow> {
    let config = entry.config_or_empty();
    let states = config.get("states").filter(|states| states.is_array())?;
    let transitions = config.get("transitions")?.as_array()?;
    Some(Flow {
        states: strings(Some(states)),
        transitions: transitions
            .iter()
            .map(|edge| {
                (
                    edge.get("from").and_then(Value::as_str).map(str::to_owned),
                    edge.get("to").and_then(Value::as_str).map(str::to_owned),
                )
            })
            .collect(),
    })
}

// check_rules holds the rules to the type's Workflow and relations, in the
// engine's words, and refuses the cycles of the rules on the row itself.
fn check_rules(
    rules: &[(usize, &ReactionsRule)],
    fields: &[(String, ReactionsField)],
    spec: &Spec<'_>,
    status: &Status<'_>,
) -> Result<(), ConfigError> {
    let status_use = match status {
        Status::Of(entry) => Some(*entry),
        _ => None,
    };
    let flow = status_use.and_then(flow_of);
    let not_state = |flow: &Flow, state: &str| {
        format!(
            "\"{state}\" is not a state of the type's Workflow ({})",
            flow.states.join(", ")
        )
    };
    let mut moves: Vec<(String, String)> = Vec::new();
    for (n, rule) in rules {
        let at = format!("rule {n}");
        if let (ReactionsWhen::Enters(state), Some(flow)) = (&rule.when, &flow) {
            if !flow.states.contains(state) {
                return Err(format!("{at}: when.enters {}", not_state(flow, state)).into());
            }
        }
        // A changes rule on a Workflow's status names states, as an enters
        // rule does, of the Workflow whose status it is.
        let on_status = match &rule.when {
            ReactionsWhen::Changes(changes) => fields
                .iter()
                .find(|(known, _)| *known == changes.field)
                .and_then(|(_, field)| status_field(spec, field))
                .map(|entry| (changes, entry)),
            _ => None,
        };
        if let Some((changes, entry)) = on_status {
            if let Some(own) = flow_of(entry) {
                for (key, state) in [("to", changes.to.as_ref()), ("from", changes.from.as_ref())] {
                    match state {
                        Some(Value::String(state)) if !own.states.contains(state) => {
                            return Err(format!(
                                "{at}: when.changes.{key} {}",
                                not_state(&own, state)
                            )
                            .into());
                        }
                        Some(Value::String(_)) | None => {}
                        Some(other) => {
                            return Err(format!(
                                "{at}: when.changes.{key} {} is not a state of the type's Workflow ({})",
                                serde_json::to_string(other).unwrap_or_default(),
                                own.states.join(", ")
                            )
                            .into());
                        }
                    }
                }
            }
        }
        if let Some((form, terminal)) = rule.when.terminal() {
            if terminal.schema == spec.name && spec.relation(&terminal.link) != Some(spec.name) {
                return Err(format!(
                    "{at}: when.{form} names link {} of {}, which has no such link to {}",
                    terminal.link, spec.name, spec.name
                )
                .into());
            }
        }
        if let Some(link) = &rule.then.link {
            if spec.relation(link).is_none() {
                return Err(format!(
                    "{at}: then.link {link} is not a to-one relation of {} ({})",
                    spec.name,
                    listed(&spec.relation_names())
                )
                .into());
            }
            continue;
        }
        let Some(flow) = &flow else {
            continue;
        };
        let to = rule.then.transition.as_str();
        if !flow.states.iter().any(|state| state == to) {
            return Err(format!("{at}: then.transition {}", not_state(flow, to)).into());
        }
        // An enters rule, and a changes rule on the status with to, acts
        // only while the row is still in that state.
        let on_own_status = on_status
            .filter(|(_, entry)| status_use.is_some_and(|status| status.id() == entry.id()));
        let from = match &rule.when {
            ReactionsWhen::Enters(state) => Some(state.as_str()),
            _ => on_own_status
                .and_then(|(changes, _)| changes.to.as_ref())
                .and_then(Value::as_str),
        };
        let leads = |from: Option<&str>, into: &str| {
            flow.transitions.iter().any(|(source, target)| {
                from.is_none_or(|from| source.as_deref() == Some(from))
                    && target.as_deref() == Some(into)
            })
        };
        if let Some(from) = from {
            if !leads(Some(from), to) {
                return Err(format!(
                    "{at}: no transition of the type's Workflow leads from {from} to {to}, so the rule could never move the instance"
                )
                .into());
            }
            moves.push((from.to_owned(), to.to_owned()));
        } else if !leads(None, to) {
            return Err(format!(
                "{at}: no transition of the type's Workflow leads to {to}, so the rule could never move the instance"
            )
            .into());
        } else if on_own_status.is_some() {
            // Without to it hears every move of the status, so it moves the
            // row on from each state a transition to its state leaves.
            for (source, target) in &flow.transitions {
                if target.as_deref() == Some(to) {
                    moves.push((source.clone().unwrap_or_default(), to.to_owned()));
                }
            }
        }
    }
    if let Some(found) = cycle(&moves) {
        return Err(format!(
            "its rules on the instance itself move it round the states {} for ever",
            found.join(", ")
        )
        .into());
    }
    Ok(())
}

// status_field is the Workflow use whose status a changes rule's field is:
// a Workflow use's value field, with no member below it.
fn status_field<'a>(spec: &Spec<'a>, field: &ReactionsField) -> Option<&'a UseSpec> {
    let use_id = field.use_id.as_deref()?;
    if !field.members.is_empty() || field.field != workflow::STATUS {
        return None;
    }
    spec.uses
        .iter()
        .find(|entry| entry.id() == use_id && entry.behavior == workflow::NAME)
}

// cycle finds a cycle among the moves of rules on the row itself: from the
// state they fire on to the state they move to.
fn cycle(moves: &[(String, String)]) -> Option<Vec<String>> {
    let mut next: Vec<(&str, Vec<&str>)> = Vec::new();
    for (from, to) in moves {
        match next.iter_mut().find(|(state, _)| state == from) {
            Some((_, targets)) => targets.push(to),
            None => next.push((from, vec![to])),
        }
    }
    fn visit<'s>(
        state: &'s str,
        path: &mut Vec<&'s str>,
        next: &[(&'s str, Vec<&'s str>)],
        done: &mut HashSet<&'s str>,
    ) -> Option<Vec<String>> {
        if let Some(at) = path.iter().position(|known| *known == state) {
            let mut found: Vec<String> =
                path[at..].iter().map(|state| (*state).to_owned()).collect();
            found.push(state.to_owned());
            return Some(found);
        }
        if done.contains(state) {
            return None;
        }
        let targets = next
            .iter()
            .find(|(known, _)| *known == state)
            .map(|(_, targets)| targets.clone())
            .unwrap_or_default();
        for to in targets {
            path.push(state);
            let found = visit(to, path, next, done);
            path.pop();
            if found.is_some() {
                return found;
            }
        }
        done.insert(state);
        None
    }
    let mut done = HashSet::new();
    for (state, _) in &next {
        if let Some(found) = visit(state, &mut Vec::new(), &next, &mut done) {
            return Some(found);
        }
    }
    None
}

// ---- the configSchema, in ajv's words ----

// config_issues lists what a config breaks of the declaration's
// configSchema, in the schema loader's words.
fn config_issues(raw: &Value) -> Issues {
    let mut issues = Issues::default();
    let Some(config) = raw.as_object() else {
        issues.push("", "type", "must be object");
        return issues;
    };
    unknown_keys("", config, &CONFIG_KEYS, &mut issues);
    match config.get("rules") {
        None => issues.push("", "required", "must have required property 'rules'"),
        Some(rules) => rules_issues(rules, &mut issues),
    }
    if let Some(status) = config.get("status") {
        schema::text_issues("/status", status, &mut issues);
    }
    issues
}

fn rules_issues(rules: &Value, issues: &mut Issues) {
    let at = "/rules";
    let Some(items) = rules.as_array() else {
        issues.push(at, "type", "must be array");
        return;
    };
    if items.is_empty() {
        issues.push(at, "minItems", "must NOT have fewer than 1 items");
    }
    if items.len() > RULES_MAX_ITEMS {
        issues.push(
            at,
            "maxItems",
            format!("must NOT have more than {RULES_MAX_ITEMS} items"),
        );
    }
    for (i, rule) in items.iter().enumerate() {
        rule_issues(&format!("{at}/{i}"), rule, issues);
    }
}

fn rule_issues(at: &str, rule: &Value, issues: &mut Issues) {
    let Some(members) = rule.as_object() else {
        issues.push(at, "type", "must be object");
        return;
    };
    unknown_keys(at, members, &RULE_KEYS, issues);
    for key in ["then", "when"] {
        if !members.contains_key(key) {
            issues.push(
                at,
                "required",
                format!("must have required property '{key}'"),
            );
        }
    }
    if let Some(then) = members.get("then") {
        then_issues(&schema::member(at, "then"), then, issues);
    }
    if let Some(when) = members.get("when") {
        when_issues(&schema::member(at, "when"), when, issues);
    }
}

fn then_issues(at: &str, then: &Value, issues: &mut Issues) {
    let Some(members) = then.as_object() else {
        issues.push(at, "type", "must be object");
        return;
    };
    unknown_keys(at, members, &THEN_KEYS, issues);
    match members.get("transition") {
        None => issues.push(at, "required", "must have required property 'transition'"),
        Some(transition) => {
            schema::text_issues(&schema::member(at, "transition"), transition, issues);
        }
    }
    if let Some(link) = members.get("link") {
        pattern_issues(
            &schema::member(at, "link"),
            link,
            LINK_PATTERN,
            None,
            issues,
        );
    }
}

fn when_issues(at: &str, when: &Value, issues: &mut Issues) {
    let Some(members) = when.as_object() else {
        issues.push(at, "type", "must be object");
        return;
    };
    unknown_keys(at, members, &WHEN_KEYS, issues);
    if members.len() > 1 {
        issues.push(at, "maxProperties", "must NOT have more than 1 properties");
    }
    if members.is_empty() {
        issues.push(at, "minProperties", "must NOT have fewer than 1 properties");
    }
    for (key, value) in members {
        let here = schema::member(at, key);
        match key.as_str() {
            "allTerminal" => terminal_issues(&here, value, false, issues),
            "anyTerminal" => terminal_issues(&here, value, true, issues),
            "changes" => changes_issues(&here, value, issues),
            "enters" => schema::text_issues(&here, value, issues),
            "holds" => pattern_issues(&here, value, LINK_PATTERN, None, issues),
            "revised" => revised_issues(&here, value, issues),
            _ => {}
        }
    }
}

fn terminal_issues(at: &str, terminal: &Value, outcomes_required: bool, issues: &mut Issues) {
    let Some(members) = terminal.as_object() else {
        issues.push(at, "type", "must be object");
        return;
    };
    unknown_keys(at, members, &TERMINAL_KEYS, issues);
    let required: &[&str] = if outcomes_required {
        &["schema", "link", "outcomes"]
    } else {
        &["schema", "link"]
    };
    for key in required {
        if !members.contains_key(*key) {
            issues.push(
                at,
                "required",
                format!("must have required property '{key}'"),
            );
        }
    }
    if let Some(link) = members.get("link") {
        pattern_issues(
            &schema::member(at, "link"),
            link,
            LINK_PATTERN,
            None,
            issues,
        );
    }
    if let Some(name) = members.get("schema") {
        pattern_issues(
            &schema::member(at, "schema"),
            name,
            SCHEMA_PATTERN,
            None,
            issues,
        );
    }
    if let Some(outcomes) = members.get("outcomes") {
        outcome_list_issues(&schema::member(at, "outcomes"), outcomes, issues);
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
    if let Some((j, i)) = duplicate(items) {
        issues.push(
            at,
            "uniqueItems",
            format!("must NOT have duplicate items (items ## {j} and {i} are identical)"),
        );
    }
}

// duplicate names two equal items of a list as ajv's uniqueItems does: the
// later and the earlier, from the first pair it meets reading from the end.
fn duplicate(items: &[Value]) -> Option<(usize, usize)> {
    for (i, item) in items.iter().enumerate().rev() {
        if let Some(j) = items[i + 1..].iter().position(|later| later == item) {
            return Some((i + 1 + j, i));
        }
    }
    None
}

fn changes_issues(at: &str, changes: &Value, issues: &mut Issues) {
    let Some(members) = changes.as_object() else {
        issues.push(at, "type", "must be object");
        return;
    };
    unknown_keys(at, members, &CHANGES_KEYS, issues);
    match members.get("field") {
        None => issues.push(at, "required", "must have required property 'field'"),
        Some(field) => pattern_issues(
            &schema::member(at, "field"),
            field,
            FIELD_PATTERN,
            Some(FIELD_MAX_LENGTH),
            issues,
        ),
    }
}

fn revised_issues(at: &str, revised: &Value, issues: &mut Issues) {
    let Some(members) = revised.as_object() else {
        issues.push(at, "type", "must be object");
        return;
    };
    unknown_keys(at, members, &REVISED_KEYS, issues);
    match members.get("link") {
        None => issues.push(at, "required", "must have required property 'link'"),
        Some(link) => pattern_issues(
            &schema::member(at, "link"),
            link,
            LINK_PATTERN,
            None,
            issues,
        ),
    }
}

// pattern_issues lists what a value breaks of `{ type: string, pattern }`
// with a maximum length when one is given.
fn pattern_issues(
    at: &str,
    value: &Value,
    pattern: &str,
    max_length: Option<usize>,
    issues: &mut Issues,
) {
    let Some(text) = value.as_str() else {
        issues.push(at, "type", "must be string");
        return;
    };
    if let Some(max) = max_length.filter(|max| schema::length(text) > *max) {
        issues.push(
            at,
            "maxLength",
            format!("must NOT have more than {max} characters"),
        );
    }
    let matches = match pattern {
        LINK_PATTERN => is_link(text),
        SCHEMA_PATTERN => is_schema(text),
        _ => is_field(text),
    };
    if !matches {
        issues.push(at, "pattern", format!("must match pattern \"{pattern}\""));
    }
}

// is_link matches LINK_PATTERN: a lowercase letter, then at most 63
// letters and digits, ASCII alone.
fn is_link(text: &str) -> bool {
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

// is_field matches FIELD_PATTERN: dotted names, none of them empty.
fn is_field(text: &str) -> bool {
    !text.is_empty() && text.split('.').all(|part| !part.is_empty())
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

    #[test]
    fn the_patterns_match_as_the_schemas_do() {
        for (text, link, name, field) in [
            ("project", true, true, true),
            ("Project", false, true, true),
            ("a-b", false, true, true),
            ("2nd", false, false, true),
            ("", false, false, false),
            ("Workflow.status", false, false, true),
            ("a..b", false, false, false),
            (".a", false, false, false),
        ] {
            assert_eq!(is_link(text), link, "{text}");
            assert_eq!(is_schema(text), name, "{text}");
            assert_eq!(is_field(text), field, "{text}");
        }
        assert!(!is_link(&format!("a{}", "b".repeat(64))));
        assert!(is_link(&format!("a{}", "b".repeat(63))));
    }

    #[test]
    fn a_cycle_is_found_from_its_first_state() {
        let moves = |pairs: &[(&str, &str)]| -> Vec<(String, String)> {
            pairs
                .iter()
                .map(|(a, b)| ((*a).to_owned(), (*b).to_owned()))
                .collect()
        };
        assert_eq!(
            cycle(&moves(&[("open", "held"), ("held", "open")])),
            Some(vec![
                "open".to_owned(),
                "held".to_owned(),
                "open".to_owned()
            ])
        );
        assert_eq!(cycle(&moves(&[("a", "b"), ("b", "c")])), None);
    }

    #[test]
    fn values_compare_as_json_with_numbers_by_value() {
        assert!(json_equal(&json!(1), &json!(1.0)));
        assert!(json_equal(&json!({"a": [1, 2]}), &json!({"a": [1.0, 2]})));
        assert!(!json_equal(&json!([1, 2]), &json!([2, 1])));
        assert!(equal(None, None));
        assert!(!equal(None, Some(&Value::Null)));
        assert_eq!(
            duplicate(&[json!("a"), json!("b"), json!("a")]),
            Some((2, 0))
        );
    }
}
