//! Reactions, the core's rules that move Workflow statuses after a change
//! commits (D16, amended; D56), the port of
//! `src/behaviors/core/reactions.ts`. It has no fields, operations or
//! storage: the engine's runner (`run_due`) hands it each committed event
//! of its schema and of the schemas its rules name, as the runner's
//! principal, and it changes statuses only through Workflow's `transition`,
//! whose guards run and whose events record the cause.
//!
//! A rule is one `when` and one `then`:
//!
//! - `enters`: the instance's status became the state, by a create or a
//!   transition, as the event's change records it.
//! - `changes { field, to?, from? }` (D56): a create, an update or an
//!   operation event of the instance moved the value of `field`, to `to` and
//!   from `from` when they are given. `field` is an own field by its JSON
//!   key, or a field of a behavior the type lists by qualified name, a
//!   member path after it allowed (`Timers.deadlines.nudge.fired`); the
//!   behavior is the longest of the type's behavior names that prefixes it.
//!   A create moves a field from absent, and a delete moves nothing. The
//!   event's change says what moved: an operation's patch and an update's
//!   `behaviors` part are diffs, so a field they leave out has not moved,
//!   and a value they replace is the value now. That is the fast path: a
//!   rule with no `from` on a whole field the diff replaces reads nothing
//!   more. The slow path folds the instance before the event (`before()`),
//!   once per event whichever rules ask, only for `from`, for a value whose
//!   patch is an object (a diff member by member), and for an own field of
//!   an update, whose data is the caller's patch rather than a diff. A
//!   value the event keeps in the value store is loaded before it is
//!   compared.
//! - `allTerminal { schema, link, outcomes? }`: an instance of `schema` that
//!   links here through `link` changed, linked, unlinked or went, and every
//!   instance that links here through it is now in a terminal state of its
//!   own schema's Workflow, at least one, with an outcome the list holds
//!   when there is one. Both the instance it links to now and the one it
//!   linked to before the event are looked at; the linking instances are
//!   found with Links' `listLinked`.
//! - `anyTerminal { schema, link, outcomes }`: an instance of `schema` that
//!   links here through `link` entered a terminal state whose outcome the
//!   list holds, by a create or a transition, or was linked here while in
//!   one.
//! - `holds <rollup>`: an all or any rollup of the type's Rollups config came
//!   to hold over at least one linked instance, on the edge from not
//!   holding to holding: it holds after the event, and would not with the
//!   event's instance as it was before the event, the other linked
//!   instances as they are when the rule runs; read as Rollups reads it,
//!   over at most [`crate::MAX_ROLLUP_READ`] instances.
//! - `revised { link }`: the instance this instance's link points to gained
//!   a revision of Revisions or a release of Branches ([`crate::revised`]);
//!   for a link pinned to what the target gained, only the instances the
//!   target has moved past.
//! - `then { transition, link? }`: move the instance, or the one its link
//!   points to, to the state.
//!
//! A rule acts only where it can: a target already in the state, with no
//! transition to it from where it is, or whose guards veto the transition
//! is left as it is, and so is an enters rule on the instance itself once
//! the instance has moved on from the state, and a changes rule with `to`
//! on the instance itself once the field holds another value. A target
//! without Workflow, a state its Workflow lacks, a schema that does not link
//! here through the link, a link of `then`, `allTerminal`, `anyTerminal` or
//! a holds rule's rollup that holds a list of targets (D56), and a revised
//! link whose schema composes neither Revisions nor Branches are failures
//! (BehaviorError), which retry and then halt the subscription.
//!
//! `parse_config` checks what the type's own configs show: every state of
//! an enters rule, of a changes rule on `Workflow.status` and of a rule on
//! the instance itself against the type's Workflow; every field a changes
//! rule names against the type's own fields and the fields its behaviors
//! declare ([`ConfigTarget::behavior_fields`]), refusing a volatile one,
//! which moves at a read with no event to hear, and `Workflow.enteredAt`;
//! every link a `then` or a `revised` names against its Links; every rollup
//! a holds rule names against its Rollups (an all or an any); an
//! allTerminal or anyTerminal on the type's own schema against its own
//! link; a link a rule reads one target of that holds a list, the type's
//! own or a linked schema's live one when the schema is defined or
//! published; a rule on the instance itself that no transition allows; and
//! the cycles of enters rules and changes rules on `Workflow.status` on the
//! instance itself (with `to` a changes rule moves on from that state,
//! without it from each state a transition to its own state leaves).
//!
//! Reactions runs no SQL, so both databases run the same code (D58).
//! configChange: rules hold no state, so any change is allowed, and
//! Reactions may be added to and removed from a schema with instances.

use std::collections::HashSet;

use serde_json::{Map, Value};

use superschematic_engine::behaviors::{ConfigSchemas, ReactionCx};
use superschematic_engine::instances::patch::merge_patch;
use superschematic_engine::json::{js_equal, js_keys, pointer, stringify};
use superschematic_engine::{Behavior, ConfigTarget, EngineEvent, Error, Hooks, Result};

use crate::links::{link_many, link_pin, LinkPin};
use crate::revised::{moved_on, target_move};
use crate::rollups::{outcome_counts, MAX_ROLLUP_READ};
use crate::workflow::{state_outcome, WorkflowOutcome};

const NAME: &str = "Reactions";

/// Links' largest page of `listLinked`.
const PAGE: usize = 500;

const WORKFLOW_STATUS: &str = "Workflow.status";
const LINKS_TARGETS: &str = "Links.targets";

/// The instances an allTerminal or anyTerminal rule hears: those of
/// `schema` that link here through `link`.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ReactionsTerminal {
    pub schema: String,
    pub link: String,
    /// The outcomes a terminal state must have to count; every outcome when
    /// `None`, which only allTerminal allows.
    pub outcomes: Option<Vec<WorkflowOutcome>>,
}

/// A move of a field a changes rule hears: to `to` and from `from`, when
/// they are given.
#[derive(Clone, Debug, PartialEq)]
pub struct ReactionsChanges {
    /// An own field's JSON key, or a behavior's field by qualified name,
    /// with a member path after it.
    pub field: String,
    pub to: Option<Value>,
    pub from: Option<Value>,
}

/// A field a changes rule names, as `parse_config` resolved it.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ReactionsField {
    /// The behavior whose field it is; `None` for an own field.
    pub behavior: Option<String>,
    /// The own field's JSON key, or the name the behavior's declaration
    /// gives the field.
    pub field: String,
    /// The members below the field's value the rule reads, outermost first;
    /// none for the value itself.
    pub members: Vec<String>,
}

/// What sets a rule off.
#[derive(Clone, Debug, PartialEq)]
pub enum ReactionsWhen {
    Enters(String),
    Changes(ReactionsChanges),
    AllTerminal(ReactionsTerminal),
    AnyTerminal(ReactionsTerminal),
    Holds(String),
    Revised { link: String },
}

impl ReactionsWhen {
    // terminal reads the instances an allTerminal or anyTerminal rule hears,
    // with the rule's form; None for the other forms.
    fn terminal(&self) -> Option<(&'static str, &ReactionsTerminal)> {
        match self {
            ReactionsWhen::AllTerminal(terminal) => Some(("allTerminal", terminal)),
            ReactionsWhen::AnyTerminal(terminal) => Some(("anyTerminal", terminal)),
            _ => None,
        }
    }
}

/// What a rule does: move the instance, or the one its link points to, to a
/// state.
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

/// An all or any rollup of the type's Rollups config a holds rule names, as
/// the config gives it.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ReactionsRollup {
    pub schema: String,
    pub link: String,
    /// true for an all rollup, false for an any one.
    pub all: bool,
    pub outcomes: Option<Vec<WorkflowOutcome>>,
}

/// A link of the type's Links config a revised rule names, as the config
/// gives it.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ReactionsRevisedLink {
    pub schema: String,
    /// What the link pins, a revision or a release; `None` when it pins
    /// nothing.
    pub pin: Option<LinkPin>,
}

/// Reactions' config, parsed: the rules, and what the type's other configs
/// say of the fields, rollups and links they name.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct ReactionsConfig {
    pub rules: Vec<ReactionsRule>,
    /// Each field a changes rule names, by the name the rule gives it.
    pub fields: Vec<(String, ReactionsField)>,
    /// Each rollup a holds rule names, by name.
    pub rollups: Vec<(String, ReactionsRollup)>,
    /// Each link a revised rule names, by name.
    pub revised: Vec<(String, ReactionsRevisedLink)>,
}

fn entry<'c, T>(list: &'c [(String, T)], name: &str) -> Option<&'c T> {
    list.iter()
        .find(|(known, _)| known == name)
        .map(|(_, item)| item)
}

fn put<T>(list: &mut Vec<(String, T)>, name: &str, item: T) {
    match list.iter_mut().find(|(known, _)| known == name) {
        Some((_, held)) => *held = item,
        None => list.push((name.to_owned(), item)),
    }
}

impl ReactionsConfig {
    /// The field a changes rule names, as `parse_config` resolved it.
    pub fn field(&self, name: &str) -> Option<&ReactionsField> {
        entry(&self.fields, name)
    }

    /// The rollup a holds rule names.
    pub fn rollup(&self, name: &str) -> Option<&ReactionsRollup> {
        entry(&self.rollups, name)
    }

    /// The link a revised rule names.
    pub fn revised_link(&self, name: &str) -> Option<&ReactionsRevisedLink> {
        entry(&self.revised, name)
    }
}

// A link's target, as a rule reads it.
#[derive(Clone, Debug, PartialEq, Eq)]
struct Target {
    schema: String,
    id: String,
}

// An instance as a read or before() returns it, or a change of one as the
// log records it: { data, behaviors }.
fn fields_value(data: Map<String, Value>, behaviors: Map<String, Value>) -> Value {
    let mut out = Map::new();
    out.insert("data".into(), Value::Object(data));
    out.insert("behaviors".into(), Value::Object(behaviors));
    Value::Object(out)
}

// get reads an instance of the namespace with the behavior fields named, as
// the runner, as { data, behaviors }.
fn get(
    cx: &ReactionCx<'_, ReactionsConfig>,
    schema: &str,
    id: &str,
    fields: &[&str],
) -> Result<Option<Value>> {
    Ok(cx
        .instances()
        .get(schema, id, Some(fields))?
        .map(|record| fields_value(record.data, record.behaviors)))
}

// before reads the event's instance as the log had it just before the
// event, as { data, behaviors }.
fn before(cx: &ReactionCx<'_, ReactionsConfig>, event: &EngineEvent) -> Result<Option<Value>> {
    Ok(cx
        .before(event)?
        .map(|was| fields_value(was.data, was.behaviors)))
}

fn instance_id(event: &EngineEvent) -> &str {
    event.instance_id.as_deref().unwrap_or_default()
}

// behavior_field reads one behavior field of an instance or a change of one
// (behaviorField): None when the behavior or the field has no value there.
fn behavior_field<'v>(record: Option<&'v Value>, behavior: &str, field: &str) -> Option<&'v Value> {
    record?
        .get("behaviors")?
        .get(behavior)?
        .as_object()?
        .get(field)
}

// patch_of is the merge patch an event records of its instance: a create's
// whole instance, an update's change and an operation's patch; none for a
// delete.
fn patch_of(event: &EngineEvent) -> Option<&Value> {
    match event.kind.as_str() {
        "delete" => None,
        "operation" => event.change.get("patch"),
        _ => Some(&event.change),
    }
}

// entered reads the state an event moved its instance's status into.
fn entered(event: &EngineEvent) -> Option<&str> {
    behavior_field(patch_of(event), "Workflow", "status").and_then(Value::as_str)
}

// moved_links reports whether an operation's patch moved the instance's
// links.
fn moved_links(event: &EngineEvent) -> bool {
    event.kind == "operation"
        && patch_of(event)
            .and_then(|patch| patch.get("behaviors"))
            .and_then(|behaviors| behaviors.get("Links"))
            .and_then(Value::as_object)
            .is_some_and(|links| links.contains_key("targets"))
}

// link_of reads one link of an instance's Links targets field. A rule reads
// the one target of a link, and a link that holds a list of them, which a
// linked schema's new version may make it, is a failure.
fn link_of(record: Option<&Value>, link: &str) -> Result<Option<Target>> {
    let held = behavior_field(record, "Links", "targets").and_then(|targets| targets.get(link));
    if held.is_some_and(Value::is_array) {
        return Err(Error::behavior(
            NAME,
            format!("link {link} holds a list of targets, and a rule reads the one instance a link points at"),
        ));
    }
    Ok(held.and_then(|held| {
        match (
            held.get("schema").and_then(Value::as_str),
            held.get("id").and_then(Value::as_str),
        ) {
            (Some(schema), Some(id)) => Some(Target {
                schema: schema.to_owned(),
                id: id.to_owned(),
            }),
            _ => None,
        }
    }))
}

// status_of reads an instance's Workflow status.
fn status_of(record: Option<&Value>) -> Option<&Value> {
    behavior_field(record, "Workflow", "status")
}

// parents lists the instances of the home schema that an event's instance
// links to through link: now, and before the event when the event moved the
// link or deleted the instance.
fn parents(
    cx: &ReactionCx<'_, ReactionsConfig>,
    event: &EngineEvent,
    link: &str,
) -> Result<Vec<String>> {
    let mut found: Vec<String> = Vec::new();
    let id = instance_id(event);
    if event.kind != "delete" {
        let record = get(cx, &event.schema, id, &[LINKS_TARGETS])?;
        if let Some(now) = link_of(record.as_ref(), link)? {
            if now.schema == cx.schema() {
                found.push(now.id);
            }
        }
    }
    if event.kind == "delete" || moved_links(event) {
        let was = before(cx, event)?;
        if let Some(was) = link_of(was.as_ref(), link)? {
            if was.schema == cx.schema() && !found.contains(&was.id) {
                found.push(was.id);
            }
        }
    }
    Ok(found)
}

// flow_of reads the Workflow config of the schema an allTerminal or
// anyTerminal rule hears.
fn flow_of(cx: &ReactionCx<'_, ReactionsConfig>, form: &str, schema: &str) -> Result<Value> {
    cx.schemas().config(schema, "Workflow")?.ok_or_else(|| {
        Error::behavior(
            NAME,
            format!("{form} names {schema}, which does not compose Workflow, so its instances have no terminal state"),
        )
    })
}

// counts reports whether a status is a terminal state whose outcome the list
// holds; every terminal state counts when there is no list.
fn counts(flow: &Value, status: Option<&Value>, outcomes: Option<&[WorkflowOutcome]>) -> bool {
    let outcome = status
        .and_then(Value::as_str)
        .and_then(|status| state_outcome(flow, status));
    outcome_counts(outcomes, outcome)
}

// list_linked reads one page of the instances of schema whose link points
// at id, through Links' listLinked: their ids, and the next page's cursor.
fn list_linked(
    cx: &ReactionCx<'_, ReactionsConfig>,
    schema: &str,
    link: &str,
    id: &str,
    limit: usize,
    cursor: Option<&str>,
) -> Result<(Vec<String>, Option<String>)> {
    let mut params = Map::new();
    params.insert("name".into(), Value::from(link));
    params.insert("id".into(), Value::from(id));
    params.insert("limit".into(), Value::from(limit));
    if let Some(cursor) = cursor {
        params.insert("cursor".into(), Value::from(cursor));
    }
    let page = cx
        .instances()
        .invoke_schema(schema, "listLinked", &Value::Object(params))?;
    let ids = page
        .get("items")
        .and_then(Value::as_array)
        .into_iter()
        .flatten()
        .filter_map(|item| item.get("id").and_then(Value::as_str))
        .map(str::to_owned)
        .collect();
    Ok((
        ids,
        page.get("next").and_then(Value::as_str).map(str::to_owned),
    ))
}

// statuses reads the Workflow status of instances of a schema in one
// get_many, by id.
fn statuses(
    cx: &ReactionCx<'_, ReactionsConfig>,
    schema: &str,
    ids: &[String],
) -> Result<Vec<(String, Option<Value>)>> {
    let wanted: Vec<&str> = ids.iter().map(String::as_str).collect();
    let found = cx
        .instances()
        .get_many(schema, &wanted, Some(&[WORKFLOW_STATUS]))?;
    Ok(ids
        .iter()
        .map(|id| {
            let status = found
                .iter()
                .find(|(known, _)| known == id)
                .and_then(|(_, record)| record.behavior_field("Workflow", "status"))
                .cloned();
            (id.clone(), status)
        })
        .collect())
}

// all_terminal reports whether every instance of the rule's schema that
// links to parent through its link is in a terminal state of its schema's
// Workflow with an outcome the rule counts, and there is at least one.
fn all_terminal(
    cx: &ReactionCx<'_, ReactionsConfig>,
    terminal: &ReactionsTerminal,
    parent: &str,
) -> Result<bool> {
    let flow = flow_of(cx, "allTerminal", &terminal.schema)?;
    let mut cursor: Option<String> = None;
    let mut any = false;
    loop {
        let (ids, next) = list_linked(
            cx,
            &terminal.schema,
            &terminal.link,
            parent,
            PAGE,
            cursor.as_deref(),
        )?;
        for (_, status) in statuses(cx, &terminal.schema, &ids)? {
            if !counts(&flow, status.as_ref(), terminal.outcomes.as_deref()) {
                return Ok(false);
            }
            any = true;
        }
        cursor = next;
        if cursor.is_none() {
            return Ok(any);
        }
    }
}

// any_terminal lists the instance of the home schema that an event's
// instance links to through the rule's link, when the event moved its
// status into a terminal state whose outcome the rule lists, or moved the
// link here while its status is one; none otherwise. A later event that
// leaves both alone, a comment say, does not set the rule off again.
fn any_terminal(
    cx: &ReactionCx<'_, ReactionsConfig>,
    event: &EngineEvent,
    terminal: &ReactionsTerminal,
) -> Result<Vec<String>> {
    if event.kind == "delete" {
        return Ok(Vec::new());
    }
    let flow = flow_of(cx, "anyTerminal", &terminal.schema)?;
    let record = get(
        cx,
        &event.schema,
        instance_id(event),
        &[LINKS_TARGETS, WORKFLOW_STATUS],
    )?;
    let Some(now) = link_of(record.as_ref(), &terminal.link)? else {
        return Ok(Vec::new());
    };
    if now.schema != cx.schema() {
        return Ok(Vec::new());
    }
    let outcomes = terminal.outcomes.as_deref();
    if let Some(state) = entered(event) {
        let state = Value::from(state);
        return Ok(if counts(&flow, Some(&state), outcomes) {
            vec![now.id]
        } else {
            Vec::new()
        });
    }
    if !moved_links(event) {
        return Ok(Vec::new());
    }
    let was = before(cx, event)?;
    if link_of(was.as_ref(), &terminal.link)?.as_ref() == Some(&now) {
        return Ok(Vec::new());
    }
    Ok(if counts(&flow, status_of(record.as_ref()), outcomes) {
        vec![now.id]
    } else {
        Vec::new()
    })
}

// holds_over is whether an all or any rollup holds, for a rule, over the
// counted flags of its linked instances: at least one of them, as an all of
// none sets nothing off, and at most MAX_ROLLUP_READ, past which it has no
// value, as Rollups has it.
fn holds_over(rollup: &ReactionsRollup, flags: &[bool]) -> bool {
    if flags.is_empty() || flags.len() > MAX_ROLLUP_READ {
        return false;
    }
    if rollup.all {
        flags.iter().all(|flag| *flag)
    } else {
        flags.iter().any(|flag| *flag)
    }
}

// left_by is the event's instance as the event left it, { data, behaviors }:
// the create's, the instance before an update or an operation with the
// change's patch applied, nothing after a delete.
fn left_by(event: &EngineEvent, was: Option<&Value>) -> Option<Value> {
    let empty = Value::Object(Map::new());
    let was = was.unwrap_or(&empty);
    match event.kind.as_str() {
        "delete" => None,
        "create" => Some(if event.change.is_null() {
            empty.clone()
        } else {
            event.change.clone()
        }),
        "operation" => Some(merge_patch(
            was,
            event
                .change
                .get("patch")
                .filter(|patch| !patch.is_null())
                .unwrap_or(&empty),
        )),
        _ => Some(merge_patch(
            was,
            if event.change.is_null() {
                &empty
            } else {
                &event.change
            },
        )),
    }
}

// made_hold reports whether an event of the rollup's schema made the rollup
// hold on parent: it holds now, over the instances that link to parent as
// they are, and with the event's instance as the event left it, and would
// not with that instance as it was before the event. It reads as Rollups
// does: the schema's Links config, one listLinked page, their statuses in
// one get_many, and the schema's Workflow config.
fn made_hold(
    cx: &ReactionCx<'_, ReactionsConfig>,
    rollup: &ReactionsRollup,
    parent: &str,
    event: &EngineEvent,
) -> Result<bool> {
    let links = cx.schemas().config(&rollup.schema, "Links")?;
    let points = links
        .as_ref()
        .and_then(|links| links.get("links"))
        .and_then(Value::as_object)
        .and_then(|links| links.get(&rollup.link))
        .and_then(|link| link.get("schema"))
        .and_then(Value::as_str);
    if points != Some(cx.schema()) {
        // Rollups reads no instance there: the value does not move.
        return Ok(false);
    }
    let (ids, next) = list_linked(
        cx,
        &rollup.schema,
        &rollup.link,
        parent,
        MAX_ROLLUP_READ,
        None,
    )?;
    if next.is_some() {
        return Ok(false);
    }
    let read = if ids.is_empty() {
        Vec::new()
    } else {
        statuses(cx, &rollup.schema, &ids)?
    };
    let flow = cx.schemas().config(&rollup.schema, "Workflow")?;
    let counted = |status: Option<&Value>| {
        flow.as_ref()
            .is_some_and(|flow| counts(flow, status, rollup.outcomes.as_deref()))
    };
    let now: Vec<bool> = read
        .iter()
        .map(|(_, status)| counted(status.as_ref()))
        .collect();
    if !holds_over(rollup, &now) {
        return Ok(false);
    }
    let id = instance_id(event);
    let was = before(cx, event)?;
    let with = |record: Option<&Value>| -> Result<Vec<bool>> {
        let mut flags: Vec<bool> = read
            .iter()
            .filter(|(known, _)| known != id)
            .map(|(_, status)| counted(status.as_ref()))
            .collect();
        if link_of(record, &rollup.link)?
            .is_some_and(|linked| linked.schema == cx.schema() && linked.id == parent)
        {
            flags.push(counted(status_of(record)));
        }
        Ok(flags)
    };
    let left = left_by(event, was.as_ref());
    Ok(holds_over(rollup, &with(left.as_ref())?) && !holds_over(rollup, &with(was.as_ref())?))
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

// part_of is the object of an instance, or of a change of one, that holds a
// changes rule's field: its own fields, or its behavior's.
fn part_of<'v>(record: Option<&'v Value>, field: &ReactionsField) -> Option<&'v Value> {
    match &field.behavior {
        None => record?.get("data"),
        Some(behavior) => record?.get("behaviors")?.get(behavior),
    }
}

// field_of reads a changes rule's field of an instance as a read or before()
// returns it: None when it has no value.
fn field_of<'v>(record: Option<&'v Value>, field: &ReactionsField) -> Option<&'v Value> {
    part_of(record, field)?.as_object()?.get(&field.field)
}

// patched_field is what an event's change records of a changes rule's
// field: None when the change leaves it out, else its value there, a
// create's value or a patch's, with a value the event keeps in the value
// store loaded.
fn patched_field(
    cx: &ReactionCx<'_, ReactionsConfig>,
    event: &EngineEvent,
    field: &ReactionsField,
) -> Result<Option<Value>> {
    let Some(value) = part_of(patch_of(event), field)
        .and_then(Value::as_object)
        .and_then(|part| part.get(&field.field))
    else {
        return Ok(None);
    };
    let mut tokens: Vec<&str> = Vec::new();
    if event.kind == "operation" {
        tokens.push("patch");
    }
    match &field.behavior {
        None => tokens.push("data"),
        Some(behavior) => {
            tokens.push("behaviors");
            tokens.push(behavior);
        }
    }
    tokens.push(&field.field);
    let at = pointer(&tokens);
    if !event
        .value_refs
        .as_ref()
        .is_some_and(|refs| refs.contains(&at))
    {
        return Ok(Some(value.clone()));
    }
    let mut stowed = Map::new();
    stowed.insert("value".into(), value.clone());
    let loaded = cx.values().load(
        &stringify(&Value::Object(stowed)),
        Some(&stringify(&Value::from(vec![Value::from("/value")]))),
    )?;
    Ok(Some(loaded.get("value").cloned().unwrap_or(Value::Null)))
}

// equal compares two values that may be absent as jsonEqual does: absent
// equals only absent.
fn equal(a: Option<&Value>, b: Option<&Value>) -> bool {
    match (a, b) {
        (None, None) => true,
        (Some(a), Some(b)) => js_equal(a, b),
        _ => false,
    }
}

// wanted reports whether a value is the one a rule names, any value when
// it names none.
fn wanted(value: Option<&Value>, wanted: Option<&Value>) -> bool {
    wanted.is_none() || equal(value, wanted)
}

// The instance before an event, folded at most once for the event,
// whichever changes rules ask.
struct Was<'e> {
    event: &'e EngineEvent,
    folded: Option<Option<Value>>,
}

impl Was<'_> {
    fn get(&mut self, cx: &ReactionCx<'_, ReactionsConfig>) -> Result<Option<&Value>> {
        if self.folded.is_none() {
            self.folded = Some(before(cx, self.event)?);
        }
        Ok(self.folded.as_ref().and_then(Option::as_ref))
    }
}

// changes_fired reports whether an event of the type's own schema moved a
// changes rule's field: to `to` and from `from`, when the rule gives them.
// The fast path reads the move from the event alone: a create, and a diff
// (an operation's patch, an update's behaviors part) that replaces the
// whole value with no from to check. The slow path folds the instance
// before the event, once per event: for from, for a patch whose value is
// an object (a diff member by member), and for an own field of an update
// (the caller's patch, not a diff).
fn changes_fired(
    cx: &ReactionCx<'_, ReactionsConfig>,
    event: &EngineEvent,
    changes: &ReactionsChanges,
    field: &ReactionsField,
    was: &mut Was<'_>,
) -> Result<bool> {
    if !matches!(event.kind.as_str(), "create" | "update" | "operation") {
        return Ok(false);
    }
    let Some(patched) = patched_field(cx, event, field)? else {
        return Ok(false);
    };
    let present = (!patched.is_null()).then_some(&patched);
    if event.kind == "create" {
        let after = member_of(present, &field.members);
        return Ok(after.is_some() && changes.from.is_none() && wanted(after, changes.to.as_ref()));
    }
    let diff = event.kind == "operation" || field.behavior.is_some();
    if diff && !patched.is_object() {
        // The diff replaced the value, or removed it with null: the patch
        // holds what it is now.
        let after = member_of(present, &field.members);
        if !wanted(after, changes.to.as_ref()) {
            return Ok(false);
        }
        if changes.from.is_none() && field.members.is_empty() {
            return Ok(true);
        }
    }
    let before = field_of(was.get(cx)?, field).cloned();
    let merged = merge_patch(before.as_ref().unwrap_or(&Value::Null), &patched);
    let from = member_of(before.as_ref(), &field.members);
    let to = member_of((!merged.is_null()).then_some(&merged), &field.members);
    Ok(!equal(from, to) && wanted(to, changes.to.as_ref()) && wanted(from, changes.from.as_ref()))
}

// still_holds reports whether the instance's field holds the value a
// changes rule moved it to, as it is when the rule runs.
fn still_holds(
    cx: &ReactionCx<'_, ReactionsConfig>,
    field: &ReactionsField,
    id: &str,
    to: &Value,
) -> Result<bool> {
    let path = field
        .behavior
        .as_ref()
        .map(|behavior| format!("{behavior}.{}", field.field));
    let fields: Vec<&str> = path.iter().map(String::as_str).collect();
    let record = get(cx, cx.schema(), id, &fields)?;
    Ok(equal(
        member_of(field_of(record.as_ref(), field), &field.members),
        Some(to),
    ))
}

// apply moves a rule's target to its state, where it can. from, for an
// enters rule on the instance itself, is the state the instance must still
// be in.
fn apply(
    cx: &ReactionCx<'_, ReactionsConfig>,
    then: &ReactionsThen,
    id: &str,
    from: Option<&str>,
) -> Result<()> {
    let target = match &then.link {
        None => Target {
            schema: cx.schema().to_owned(),
            id: id.to_owned(),
        },
        Some(link) => {
            let record = get(cx, cx.schema(), id, &[LINKS_TARGETS])?;
            match link_of(record.as_ref(), link)? {
                Some(target) => target,
                None => return Ok(()),
            }
        }
    };
    let Some(flow) = cx.schemas().config(&target.schema, "Workflow")? else {
        return Err(Error::behavior(
            NAME,
            format!(
                "a rule moves {} {} to {}, but {} does not compose Workflow",
                target.schema, target.id, then.transition, target.schema
            ),
        ));
    };
    let states: Vec<String> = flow
        .get("states")
        .and_then(Value::as_array)
        .into_iter()
        .flatten()
        .map(text_of)
        .collect();
    if !states.contains(&then.transition) {
        return Err(Error::behavior(
            NAME,
            format!(
                "a rule moves {} {} to {}, which is not a state of {}'s Workflow ({})",
                target.schema,
                target.id,
                then.transition,
                target.schema,
                states.join(", ")
            ),
        ));
    }
    let record = get(cx, &target.schema, &target.id, &[WORKFLOW_STATUS])?;
    let Some(status) = status_of(record.as_ref()).and_then(Value::as_str) else {
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
    let mut params = Map::new();
    params.insert("to".into(), Value::from(then.transition.as_str()));
    match cx.instances().invoke(
        &target.schema,
        &target.id,
        "transition",
        &Value::Object(params),
        None,
    ) {
        // A guard's refusal, an open blocker's say, leaves the target as
        // it is.
        Err(error) if error.is_veto() => Ok(()),
        other => other.map(|_| ()),
    }
}

// text_of reads a config's state as JavaScript's join writes it.
fn text_of(value: &Value) -> String {
    match value {
        Value::String(text) => text.clone(),
        other => stringify(other),
    }
}

// cycle finds a cycle among the moves of rules on the instance itself: from
// the state they fire on to the state they move to.
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

fn text(value: &Value, key: &str) -> String {
    value
        .get(key)
        .and_then(Value::as_str)
        .unwrap_or_default()
        .to_owned()
}

// rule_of reads one rule the configSchema let through.
fn rule_of(raw: &Value) -> ReactionsRule {
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
        ReactionsWhen::Holds(text_of(rollup))
    } else {
        ReactionsWhen::Revised {
            link: when
                .get("revised")
                .map(|revised| text(revised, "link"))
                .unwrap_or_default(),
        }
    };
    let then = raw.get("then").cloned().unwrap_or(Value::Null);
    ReactionsRule {
        when,
        then: ReactionsThen {
            transition: text(&then, "transition"),
            link: then.get("link").and_then(Value::as_str).map(str::to_owned),
        },
    }
}

// resolve holds the rollups holds rules name to the type's Rollups and the
// links revised rules name to its Links, and returns what the rules read
// of them when they run.
#[allow(clippy::type_complexity)]
fn resolve(
    rules: &[ReactionsRule],
    target: &ConfigTarget<'_>,
) -> Result<(
    Vec<(String, ReactionsRollup)>,
    Vec<(String, ReactionsRevisedLink)>,
)> {
    let lists = |behavior: &str| target.behaviors().iter().any(|known| known == behavior);
    let specs = target
        .configs()
        .get("Rollups")
        .and_then(|config| config.get("rollups"));
    let links = target
        .configs()
        .get("Links")
        .and_then(|config| config.get("links"));
    let mut rollups = Vec::new();
    let mut revised = Vec::new();
    for (index, rule) in rules.iter().enumerate() {
        let at = format!("rule {}", index + 1);
        match &rule.when {
            ReactionsWhen::Holds(name) => {
                if !lists("Rollups") {
                    return Err(Error::config(format!(
                        "{at}: when.holds names rollup {name} of Rollups, which the type does not list"
                    )));
                }
                // A Rollups config of the wrong shape is Rollups' to refuse.
                let Some(specs) = specs.and_then(Value::as_object) else {
                    continue;
                };
                let Some(spec) = specs.get(name) else {
                    return Err(Error::config(format!(
                        "{at}: when.holds names rollup {name}, which is not a rollup of the type's Rollups ({})",
                        js_keys(specs).join(", ")
                    )));
                };
                let function = spec.get("function");
                let all = match function.and_then(Value::as_str) {
                    Some("all") => true,
                    Some("any") => false,
                    other => {
                        let shown = match (other, function) {
                            (Some(text), _) => text.to_owned(),
                            (None, Some(value)) => stringify(value),
                            (None, None) => "undefined".to_owned(),
                        };
                        return Err(Error::config(format!(
                            "{at}: when.holds names rollup {name}, a {shown} rollup; holds takes an all or any rollup, which holds or does not"
                        )));
                    }
                };
                put(
                    &mut rollups,
                    name,
                    ReactionsRollup {
                        schema: spec.get("schema").map(text_of).unwrap_or_default(),
                        link: spec.get("link").map(text_of).unwrap_or_default(),
                        all,
                        outcomes: outcomes_of(spec.get("outcomes")),
                    },
                );
            }
            ReactionsWhen::Revised { link: name } => {
                if !lists("Links") {
                    return Err(Error::config(format!(
                        "{at}: when.revised names link {name} of Links, which the type does not list"
                    )));
                }
                let Some(links) = links.and_then(Value::as_object) else {
                    continue;
                };
                let Some(link) = links.get(name) else {
                    return Err(Error::config(format!(
                        "{at}: when.revised names link {name}, which is not a link of the type's Links ({})",
                        js_keys(links).join(", ")
                    )));
                };
                put(
                    &mut revised,
                    name,
                    ReactionsRevisedLink {
                        schema: link.get("schema").map(text_of).unwrap_or_default(),
                        pin: link_pin(link),
                    },
                );
            }
            _ => {}
        }
    }
    Ok((rollups, revised))
}

// check_one_target refuses a link a rule reads the one target of that holds
// a list of them: then.link, and the link of an allTerminal, an anyTerminal
// or a holds rule's rollup, the type's own or, when the schema is defined
// or published, a linked schema's live one. A linked schema with no live
// version is checked when the rule runs.
fn check_one_target(
    rules: &[ReactionsRule],
    rollups: &[(String, ReactionsRollup)],
    target: &ConfigTarget<'_>,
) -> Result<()> {
    let schemas: Option<&dyn ConfigSchemas> = target.schemas();
    let many = |schema: &str, link: &str| -> Result<bool> {
        let config = if schema == target.schema() {
            target.configs().get("Links").cloned()
        } else {
            match schemas {
                Some(schemas) => schemas
                    .get(schema)?
                    .and_then(|source| source.configs.get("Links").cloned()),
                None => None,
            }
        };
        Ok(config
            .as_ref()
            .and_then(|config| config.get("links"))
            .and_then(Value::as_object)
            .and_then(|links| links.get(link))
            .is_some_and(link_many))
    };
    let reads = "and the rule reads the one instance it points at";
    for (index, rule) in rules.iter().enumerate() {
        let at = format!("rule {}", index + 1);
        if let Some(link) = &rule.then.link {
            if many(target.schema(), link)? {
                return Err(Error::config(format!(
                    "{at}: then.link {link} holds a list of targets, {reads}"
                )));
            }
        }
        if let Some((form, terminal)) = rule.when.terminal() {
            if many(&terminal.schema, &terminal.link)? {
                return Err(Error::config(format!(
                    "{at}: when.{form} names link {} of {}, which holds a list of targets, {reads}",
                    terminal.link, terminal.schema
                )));
            }
        }
        if let ReactionsWhen::Holds(name) = &rule.when {
            if let Some(rollup) = entry(rollups, name) {
                if many(&rollup.schema, &rollup.link)? {
                    return Err(Error::config(format!(
                        "{at}: when.holds names rollup {name}, whose link {} of {} holds a list of targets, {reads}",
                        rollup.link, rollup.schema
                    )));
                }
            }
        }
    }
    Ok(())
}

// fields_of holds the fields changes rules name to the type's own fields and
// the ones its behaviors declare, and returns each resolved: an own field
// has no dot; a behavior's field is the longest of the type's behavior
// names that prefixes it, a dot, a field it declares, and the members
// below. It refuses a volatile field, which moves at a read with no event
// of the instance, and Workflow.enteredAt, which every move of the status
// moves.
fn fields_of(
    rules: &[ReactionsRule],
    target: &ConfigTarget<'_>,
) -> Result<Vec<(String, ReactionsField)>> {
    let mut fields = Vec::new();
    for (index, rule) in rules.iter().enumerate() {
        let ReactionsWhen::Changes(changes) = &rule.when else {
            continue;
        };
        let at = format!("rule {}: when.changes", index + 1);
        let name = changes.field.as_str();
        if !name.contains('.') {
            if !target.fields().iter().any(|field| field == name) {
                let own = target.fields().join(", ");
                return Err(Error::config(format!(
                    "{at} names {name}, which is not a field of {} ({})",
                    target.type_name(),
                    if own.is_empty() { "none" } else { own.as_str() }
                )));
            }
            put(
                &mut fields,
                name,
                ReactionsField {
                    behavior: None,
                    field: name.to_owned(),
                    members: Vec::new(),
                },
            );
            continue;
        }
        let behavior = target
            .behaviors()
            .iter()
            .filter(|candidate| target.behavior_fields(candidate).is_some())
            .filter(|candidate| {
                name.len() > candidate.len()
                    && name.starts_with(candidate.as_str())
                    && name.as_bytes()[candidate.len()] == b'.'
            })
            .max_by_key(|candidate| candidate.len());
        let Some(behavior) = behavior else {
            return Err(Error::config(format!(
                "{at} names {name}, which is not a field of a behavior the type lists ({})",
                target.behaviors().join(", ")
            )));
        };
        let mut path = name[behavior.len() + 1..].split('.');
        let field = path.next().unwrap_or_default().to_owned();
        let members: Vec<String> = path.map(str::to_owned).collect();
        let declared = target.behavior_fields(behavior).unwrap_or_default();
        let Some(found) = declared.iter().find(|candidate| candidate.name == field) else {
            let names: Vec<&str> = declared.iter().map(|field| field.name.as_str()).collect();
            let names = names.join(", ");
            return Err(Error::config(format!(
                "{at} names {name}, but {behavior} declares no field {field} ({})",
                if names.is_empty() {
                    "none"
                } else {
                    names.as_str()
                }
            )));
        };
        if found.volatile {
            return Err(Error::config(format!(
                "{at} names {name}, a volatile field: it moves at a read, with no event of the instance to hear; a rule hears what moves it with holds, allTerminal, anyTerminal or revised"
            )));
        }
        if behavior == "Workflow" && field == "enteredAt" {
            return Err(Error::config(format!(
                "{at} names {name}, which every move of the status moves; a rule on {WORKFLOW_STATUS} hears those"
            )));
        }
        put(
            &mut fields,
            name,
            ReactionsField {
                behavior: Some(behavior.clone()),
                field,
                members,
            },
        );
    }
    Ok(fields)
}

// check_rules holds the rules to the type's Workflow and Links, and refuses
// the cycles of the rules on the instance itself.
fn check_rules(rules: &[ReactionsRule], target: &ConfigTarget<'_>, flow: &Value) -> Result<()> {
    let states: Vec<String> = flow
        .get("states")
        .and_then(Value::as_array)
        .into_iter()
        .flatten()
        .map(text_of)
        .collect();
    let transitions: Vec<(Option<&str>, Option<&str>)> = flow
        .get("transitions")
        .and_then(Value::as_array)
        .into_iter()
        .flatten()
        .map(|edge| {
            (
                edge.get("from").and_then(Value::as_str),
                edge.get("to").and_then(Value::as_str),
            )
        })
        .collect();
    let empty = Map::new();
    let links = target
        .behaviors()
        .iter()
        .any(|behavior| behavior == "Links")
        .then(|| {
            target
                .configs()
                .get("Links")
                .and_then(|config| config.get("links"))
                .and_then(Value::as_object)
                .unwrap_or(&empty)
        });
    let all = states.join(", ");
    let not_state =
        |state: &str| format!("\"{state}\" is not a state of the type's Workflow ({all})");
    let mut moves: Vec<(String, String)> = Vec::new();
    for (index, rule) in rules.iter().enumerate() {
        let at = format!("rule {}", index + 1);
        if let ReactionsWhen::Enters(state) = &rule.when {
            if !states.contains(state) {
                return Err(Error::config(format!(
                    "{at}: when.enters {}",
                    not_state(state)
                )));
            }
        }
        // A changes rule on the status names states, as an enters rule does.
        let status = match &rule.when {
            ReactionsWhen::Changes(changes) if changes.field == WORKFLOW_STATUS => Some(changes),
            _ => None,
        };
        for (key, state) in [
            ("to", status.and_then(|status| status.to.as_ref())),
            ("from", status.and_then(|status| status.from.as_ref())),
        ] {
            match state {
                Some(Value::String(state)) if !states.contains(state) => {
                    return Err(Error::config(format!(
                        "{at}: when.changes.{key} {}",
                        not_state(state)
                    )));
                }
                Some(Value::String(_)) | None => {}
                Some(other) => {
                    return Err(Error::config(format!(
                        "{at}: when.changes.{key} {} is not a state of the type's Workflow ({all})",
                        stringify(other)
                    )));
                }
            }
        }
        if let Some((form, terminal)) = rule.when.terminal() {
            if terminal.schema == target.schema() {
                let points = links
                    .and_then(|links| links.get(&terminal.link))
                    .and_then(|link| link.get("schema"))
                    .and_then(Value::as_str);
                if points != Some(target.schema()) {
                    return Err(Error::config(format!(
                        "{at}: when.{form} names link {} of {}, which has no such link to {}",
                        terminal.link,
                        target.schema(),
                        target.schema()
                    )));
                }
            }
        }
        if let Some(link) = &rule.then.link {
            let Some(links) = links else {
                return Err(Error::config(format!(
                    "{at}: then.link {link} needs Links on the type, which does not list it"
                )));
            };
            if !links.contains_key(link) {
                return Err(Error::config(format!(
                    "{at}: then.link {link} is not a link of the type's Links ({})",
                    js_keys(links).join(", ")
                )));
            }
            continue;
        }
        let to = rule.then.transition.as_str();
        if !states.iter().any(|state| state == to) {
            return Err(Error::config(format!(
                "{at}: then.transition {}",
                not_state(to)
            )));
        }
        // An enters rule, and a changes rule on the status with to, acts only
        // while the instance is still in that state.
        let from = match &rule.when {
            ReactionsWhen::Enters(state) => Some(state.as_str()),
            _ => status
                .and_then(|status| status.to.as_ref())
                .and_then(Value::as_str),
        };
        if let Some(from) = from {
            if !transitions.contains(&(Some(from), Some(to))) {
                return Err(Error::config(format!(
                    "{at}: no transition of the type's Workflow leads from {from} to {to}, so the rule could never move the instance"
                )));
            }
            moves.push((from.to_owned(), to.to_owned()));
        } else if !transitions.iter().any(|(_, into)| *into == Some(to)) {
            return Err(Error::config(format!(
                "{at}: no transition of the type's Workflow leads to {to}, so the rule could never move the instance"
            )));
        } else if status.is_some() {
            // Without to it hears every move of the status, so it moves the
            // instance on from each state a transition to its state leaves.
            for (source, into) in &transitions {
                if *into == Some(to) {
                    moves.push((source.unwrap_or_default().to_owned(), to.to_owned()));
                }
            }
        }
    }
    if let Some(found) = cycle(&moves) {
        return Err(Error::config(format!(
            "its rules on the instance itself move it round the states {} for ever",
            found.join(", ")
        )));
    }
    Ok(())
}

/// Reactions: rules that move Workflow statuses after a change commits.
#[derive(Clone, Copy, Debug, Default)]
pub struct Reactions;

impl Behavior for Reactions {
    type Config = ReactionsConfig;

    fn declaration(&self) -> &str {
        include_str!("../declarations/Reactions.behavior.json")
    }

    // The configSchema holds the shape; this holds the rules to the type's
    // Workflow, Links and Rollups.
    fn parse_config(&self, raw: &Value, target: &ConfigTarget<'_>) -> Result<ReactionsConfig> {
        let rules: Vec<ReactionsRule> = raw
            .get("rules")
            .and_then(Value::as_array)
            .into_iter()
            .flatten()
            .map(rule_of)
            .collect();
        let fields = fields_of(&rules, target)?;
        let (rollups, revised) = resolve(&rules, target)?;
        check_one_target(&rules, &rollups, target)?;
        // Without Workflow, or with a Workflow config its own checks refuse,
        // the composition reports that; there is nothing to check against.
        let flow = target.configs().get("Workflow").filter(|flow| {
            flow.get("states").is_some_and(Value::is_array)
                && flow.get("transitions").is_some_and(Value::is_array)
        });
        if let Some(flow) = flow {
            check_rules(&rules, target, flow)?;
        }
        Ok(ReactionsConfig {
            rules,
            fields,
            rollups,
            revised,
        })
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            config_change: Some(|_, _, _, _| Ok(None)),
            guidance: Some(|_, config, target| {
                crate::guidance::reactions::guidance(config, target)
            }),
            reactions: Some(superschematic_engine::behaviors::Reactions {
                watches: Some(|_, config, _| {
                    let mut schemas: Vec<String> = Vec::new();
                    let heard = config
                        .rules
                        .iter()
                        .filter_map(|rule| rule.when.terminal())
                        .map(|(_, terminal)| &terminal.schema)
                        .chain(config.rollups.iter().map(|(_, rollup)| &rollup.schema))
                        .chain(config.revised.iter().map(|(_, link)| &link.schema));
                    for schema in heard {
                        if !schemas.contains(schema) {
                            schemas.push(schema.clone());
                        }
                    }
                    Ok(Some(schemas))
                }),
                react,
            }),
            ..Hooks::default()
        }
    }
}

fn react(_: &Reactions, cx: &ReactionCx<'_, ReactionsConfig>, event: &EngineEvent) -> Result<()> {
    // A beside operation's event moves no field (D77), so no rule hears
    // it.
    if event.beside {
        return Ok(());
    }
    let config = cx.config();
    let id = instance_id(event);
    // The instance before the event, for the changes rules that need it:
    // folded once for the event, whichever rules ask.
    let mut was = Was {
        event,
        folded: None,
    };
    for rule in &config.rules {
        match &rule.when {
            ReactionsWhen::Enters(state) => {
                if event.schema == cx.schema() && entered(event) == Some(state.as_str()) {
                    let from = rule.then.link.is_none().then_some(state.as_str());
                    apply(cx, &rule.then, id, from)?;
                }
            }
            ReactionsWhen::Changes(changes) => {
                let Some(field) = config.field(&changes.field) else {
                    continue;
                };
                if event.schema != cx.schema()
                    || !changes_fired(cx, event, changes, field, &mut was)?
                {
                    continue;
                }
                // As an enters rule does, one on the instance itself acts only
                // while the field still holds what it moved to.
                if let (None, Some(to)) = (&rule.then.link, &changes.to) {
                    if !still_holds(cx, field, id, to)? {
                        continue;
                    }
                }
                apply(cx, &rule.then, id, None)?;
            }
            ReactionsWhen::Holds(name) => {
                let Some(rollup) = config.rollup(name) else {
                    return Err(Error::behavior(
                        NAME,
                        format!("when.holds names rollup {name}, which the type's Rollups config does not give"),
                    ));
                };
                if event.schema != rollup.schema {
                    continue;
                }
                for parent in parents(cx, event, &rollup.link)? {
                    if made_hold(cx, rollup, &parent, event)? {
                        apply(cx, &rule.then, &parent, None)?;
                    }
                }
            }
            ReactionsWhen::Revised { link: name } => {
                let Some(link) = config.revised_link(name) else {
                    return Err(Error::behavior(
                        NAME,
                        format!("when.revised names link {name}, which the type's Links config does not give"),
                    ));
                };
                let Some(moved) = target_move(&**cx, NAME, "revised", name, &link.schema, event)?
                else {
                    continue;
                };
                for referrer in moved_on(&**cx, name, link.pin, id, &moved)? {
                    apply(cx, &rule.then, &referrer, None)?;
                }
            }
            ReactionsWhen::AllTerminal(terminal) | ReactionsWhen::AnyTerminal(terminal) => {
                let form = if matches!(rule.when, ReactionsWhen::AnyTerminal(_)) {
                    "anyTerminal"
                } else {
                    "allTerminal"
                };
                if event.schema != terminal.schema {
                    continue;
                }
                let links = cx.schemas().config(&terminal.schema, "Links")?;
                let spec = links
                    .as_ref()
                    .and_then(|links| links.get("links"))
                    .and_then(|links| links.get(&terminal.link));
                if spec
                    .and_then(|spec| spec.get("schema"))
                    .and_then(Value::as_str)
                    != Some(cx.schema())
                {
                    return Err(Error::behavior(
                        NAME,
                        format!(
                            "{form} names link {} of {}, which has no such link to {}",
                            terminal.link,
                            terminal.schema,
                            cx.schema()
                        ),
                    ));
                }
                if spec.is_some_and(link_many) {
                    return Err(Error::behavior(
                        NAME,
                        format!(
                            "{form} names link {} of {}, which holds a list of targets, and the rule reads the one instance it points at",
                            terminal.link, terminal.schema
                        ),
                    ));
                }
                if form == "anyTerminal" {
                    for parent in any_terminal(cx, event, terminal)? {
                        apply(cx, &rule.then, &parent, None)?;
                    }
                    continue;
                }
                for parent in parents(cx, event, &terminal.link)? {
                    if all_terminal(cx, terminal, &parent)? {
                        apply(cx, &rule.then, &parent, None)?;
                    }
                }
            }
        }
    }
    Ok(())
}
