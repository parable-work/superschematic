//! Dependencies, blockers between rows (D16, amended), ported onto the pure
//! core (D103) from the engine's, which is gone (D103, #587). An
//! edge says a blocker holds up its dependent. A table says `implements
//! Dependencies<Task, { schemas?, gatedStates?, satisfiedBy? }>` beside its
//! Workflow. A blocker is a row of the dependent's own table, or of one the
//! config lists (`schemas`, by table), and that table composes Workflow. It
//! is finished once its status is a terminal state of its own table's
//! Workflow, or a state its `finished` names (D113), whose outcome the
//! dependent's `satisfiedBy` lists, success by default, and open until
//! then: a blocker that failed stays open until it is removed. The
//! dependent is blocked while any blocker is open. One function,
//! `open_blockers`, answers that for the `blocked` field
//! ([`Behavior::read_view`], which a read of the row runs) and the guard
//! alike, over every blocker, whatever its table.
//!
//! Dependencies is held to D103's test of a behavior and passes it. Its
//! state is its own: the edges, a sub-table of the dependent's
//! (`blockers`), each the blocker's table and key and who added it when.
//! Its rules say which edges may be added (a listed table that composes
//! Workflow, a row that exists, not one already there, no cycle, no open
//! blocker on a row past its last gate), when a blocker finishes, and which
//! transitions wait for every blocker. It recurs: a task blocked by
//! others, a build step after the steps before it, a release held by its
//! checks, a batch's jobs in order (D104's engine-jobs).
//!
//! The guard gates the table's one Workflow: a transition into a gated
//! state (every terminal state of the Workflow, or the states the config
//! lists, terminal or not) is refused while the row is blocked (`blocked`,
//! the open blockers in its details). It reads the state from transition's
//! `to`, and runs for every transition request, a caller's, another
//! behavior's `call()` and another row's `call_on()` alike. A row in a
//! gated state that is terminal takes no open blocker (`gated`): no
//! transition leaves it and its gate let it in with every blocker finished.
//! One in a gated state a transition leaves takes one, which holds up its
//! next move into a gated state.
//!
//! `addBlocker` refuses an edge that would close a cycle, through any
//! table: the new blocker must not be blocked by the dependent, directly or
//! through others, which it reads breadth first, a level of the graph at a
//! time, through each row's own `blockers`. A create may give blockers too,
//! in its parameters, each held to addBlocker's checks in the create's run,
//! so a row is blocked from its create. Its status then is its Workflow's:
//! the one Workflow's initialize set, or its initial state.
//!
//! What changed from the engine, and why. An edge's number, which orders a
//! row's blockers and a page's cursor, is the row's own (one past its
//! greatest, in ten digits, the sub-table row's key), not the engine
//! database's row number across every row. The engine heard a blocker's
//! delete and removed its edges; a table's core hears no delete of another
//! table's row through a sub-table, so a blocker that is gone neither
//! blocks nor is listed, and its edge stays until `removeBlocker` takes it
//! or the dependent goes, a dependent's delete taking its sub-table rows
//! with it. A blocker that was finished and is reopened (D113: a state a
//! transition leaves that `finished` names) blocks again; a dependent that
//! went through its gate meanwhile stays where it is, as no rule moves a
//! row back (D113). `listDependents` stays the engine's: the rows a row
//! blocks are a query over every table's blockers, which a type's core
//! cannot enumerate, so the core declares no such operation and the build
//! leaves it out of a table's operations. On a table the access policy is
//! the route's (D103), so no blocker read is refused.
//!
//! Every veto carries a code the declaration lists: the gate's `blocked`
//! and an edge's `already_blocking`, `cycle` and `gated`, which a create's
//! blocker gets as addBlocker's would, with the pointer of its entry.
//!
//! The config is checked at define, first as the declaration's
//! configSchema holds it, each issue at its path in the schema loader's
//! words ([`ConfigError::Schema`]), then against the table's one Workflow
//! ([`ConfigError::Rule`]). What reads another table (each table `schemas`
//! lists exists and composes Workflow) is the build's to check and the
//! run's, since a type's define check sees its own spec alone.
//!
//! configChange: `schemas`, `gatedStates` and `satisfiedBy` may change
//! (existing edges stay). Dependencies joins a table that has rows, each
//! with no blockers, and cannot leave one: its edges would stay behind.

use serde_json::{json, Map, Value};
use superschematic_rules::{
    Behavior, ConfigCx, ConfigError, ConfigTarget, Cx, Declaration, GuardRequest, Hook,
    ReadRequest, Refusal, SchemaIssue, Stop, SubRow, TypeSpec, UseSpec, Veto, View,
};

use crate::declared;
use crate::paging::{page, page_request};
use crate::schema::{self, Issues};
use crate::subjects::canonical_subject;
use crate::workflow::{self, is_terminal_state, state_outcome, WorkflowOutcome};

/// The behavior's name, as its declaration has it.
pub const NAME: &str = "Dependencies";

/// Dependencies' declaration, read in place: the copy `make behaviors` writes
/// of the one the compiler registers.
pub const DECLARATION: &str = include_str!("../declarations/Dependencies.behavior.json");

/// The sub-table that holds a row's edges, keyed by each one's number.
pub const BLOCKERS: &str = "blockers";

/// The field a read shows whether the row is blocked in.
pub const BLOCKED: &str = "blocked";

/// The operation the engine serves and a table leaves out: the rows a row
/// blocks, across every table, which the core cannot enumerate.
pub const LIST_DEPENDENTS: &str = "listDependents";

// The operations.
const ADD_BLOCKER: &str = "addBlocker";
const REMOVE_BLOCKER: &str = "removeBlocker";
const LIST_BLOCKERS: &str = "listBlockers";

// A blocker's values in the sub-table.
const BLOCKER_TYPE: &str = "blockerType";
const BLOCKER_KEY: &str = "blockerKey";
const ADDED_BY: &str = "addedBy";
const ADDED_AT: &str = "addedAt";

// The width of an edge's number in its key.
const NUMBER_WIDTH: usize = 10;

// The members the configSchema's, the parameters' and the create
// parameters' closed objects take.
const CONFIG_KEYS: [&str; 3] = ["gatedStates", "satisfiedBy", "schemas"];
const BLOCKER_KEYS: [&str; 2] = ["id", "schema"];
const PAGE_KEYS: [&str; 2] = ["cursor", "limit"];
const CREATE_KEYS: [&str; 1] = ["blockers"];

// The patterns the schemas hold names to.
const SCHEMA_PATTERN: &str = "^[A-Za-z][A-Za-z0-9_-]{0,127}$";
const ID_PATTERN: &str = "^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$";

// The most blockers a create gives.
const MAX_CREATE_BLOCKERS: usize = 500;

// The outcomes, in the order the configSchema's enum lists them.
const OUTCOMES: [&str; 3] = ["success", "failure", "neutral"];

/// Dependencies' config, parsed: the defaults filled in.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct DependenciesConfig {
    /// The tables a blocker may be a row of.
    pub schemas: Vec<String>,
    /// The states of the table's Workflow a transition into waits for every
    /// blocker to finish.
    pub gated_states: Vec<String>,
    /// The outcomes of a blocker's terminal state that finish it.
    pub satisfied_by: Vec<WorkflowOutcome>,
    /// The gated states no transition of the table's Workflow leaves: a row
    /// in one takes no open blocker.
    pub terminal_gated_states: Vec<String>,
    /// The table's one Workflow use, by its id.
    pub status: String,
    /// That Workflow's initial state, a create's status before Workflow's
    /// initialize runs.
    pub initial: String,
}

impl DependenciesConfig {
    /// Parses and checks a config as a type gives it: as the declaration's
    /// configSchema holds it, each issue at its path in the schema loader's
    /// words, then its gated states against the table's one Workflow, a
    /// rule's reason which the core prefixes with the type and the
    /// behavior.
    pub fn parse(
        raw: &Value,
        target: &ConfigTarget<'_>,
    ) -> Result<DependenciesConfig, ConfigError> {
        let issues = config_issues(raw);
        if !issues.is_empty() {
            return Err(ConfigError::Schema(issues.into_issues()));
        }
        let schemas =
            strings(raw.get("schemas")).unwrap_or_else(|| vec![target.type_name().to_owned()]);
        let satisfied_by = match strings(raw.get("satisfiedBy")) {
            Some(outcomes) => outcomes
                .iter()
                .filter_map(|outcome| WorkflowOutcome::parse(outcome))
                .collect(),
            None => vec![WorkflowOutcome::Success],
        };
        let workflows: Vec<&UseSpec> = target
            .uses()
            .iter()
            .filter(|entry| entry.behavior == workflow::NAME)
            .collect();
        let flow = match workflows.as_slice() {
            [] => return Err("it gates the type's Workflow, which the type does not list".into()),
            [entry] => *entry,
            several => {
                let ids: Vec<&str> = several.iter().map(|entry| entry.id()).collect();
                return Err(format!(
                    "it gates the type's Workflow, and the type has several ({}): Dependencies gates a type's one Workflow",
                    ids.join(", ")
                )
                .into());
            }
        };
        let config = flow.config_or_empty();
        let states: Vec<String> = config
            .get("states")
            .and_then(Value::as_array)
            .into_iter()
            .flatten()
            .filter_map(Value::as_str)
            .map(str::to_owned)
            .collect();
        let initial = config
            .get("initial")
            .and_then(Value::as_str)
            .map(str::to_owned)
            .or_else(|| states.first().cloned())
            .unwrap_or_default();
        let given = strings(raw.get("gatedStates"));
        // A Workflow config of a shape Workflow refuses reads as no states.
        if !states.is_empty() {
            for state in given.iter().flatten() {
                if !states.contains(state) {
                    return Err(format!(
                        "gated state \"{state}\" is not a state of the type's Workflow ({})",
                        states.join(", ")
                    )
                    .into());
                }
            }
        }
        let gated_states = given.unwrap_or_else(|| {
            states
                .iter()
                .filter(|state| is_terminal_state(&config, state))
                .cloned()
                .collect()
        });
        let terminal_gated_states = gated_states
            .iter()
            .filter(|state| is_terminal_state(&config, state))
            .cloned()
            .collect();
        Ok(DependenciesConfig {
            schemas,
            gated_states,
            satisfied_by,
            terminal_gated_states,
            status: flow.id().to_owned(),
            initial,
        })
    }
}

/// A blocker, as addBlocker and listBlockers return it.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct BlockerRecord {
    pub schema: String,
    pub id: String,
    /// Its Workflow status; `None` when it has none.
    pub status: Option<String>,
    /// Whether it is not finished.
    pub open: bool,
    /// Whether its row is gone, which no list shows and no gate counts.
    gone: bool,
}

impl BlockerRecord {
    /// The blocker as the operations return it: `schema`, `id`, `status`
    /// when it has one, `open`.
    pub fn to_json(&self) -> Value {
        let mut out = Map::new();
        out.insert("schema".into(), Value::from(self.schema.as_str()));
        out.insert("id".into(), Value::from(self.id.as_str()));
        if let Some(status) = &self.status {
            out.insert("status".into(), Value::from(status.as_str()));
        }
        out.insert("open".into(), Value::Bool(self.open));
        Value::Object(out)
    }

    // described names the blocker in a message: its table, its key and its
    // status.
    fn described(&self) -> String {
        match &self.status {
            Some(status) => format!("{} {} ({status})", self.schema, self.id),
            None => format!("{} {}", self.schema, self.id),
        }
    }
}

// An edge as its sub-table row holds it: its number, the blocker's table
// and key.
struct Edge {
    number: i64,
    schema: String,
    key: String,
}

impl Edge {
    fn of(row: &SubRow) -> Option<Edge> {
        Some(Edge {
            number: row.key.parse().ok()?,
            schema: row.value(BLOCKER_TYPE)?.as_str()?.to_owned(),
            key: row.value(BLOCKER_KEY)?.as_str()?.to_owned(),
        })
    }
}

// edges are a row's edges, in the order they were added, from its
// sub-table rows.
fn edges(rows: &[SubRow]) -> Vec<Edge> {
    let mut edges: Vec<Edge> = rows.iter().filter_map(Edge::of).collect();
    edges.sort_by_key(|edge| edge.number);
    edges
}

// key_of is an edge's key: its number in ten digits, zero-padded.
fn key_of(number: i64) -> String {
    format!("{number:0NUMBER_WIDTH$}")
}

// one_workflow is a table's one Workflow use, by id with its config.
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

// blockers reads each edge's blocker as the host has it: its table's spec
// for its Workflow and its row for its status, each table's spec and each
// row read together. A blocker is open unless its status is a terminal
// state of that Workflow, or one its finished names, whose outcome the
// dependent's satisfiedBy lists; one whose row is gone is gone.
fn blockers<C>(
    view: &View<'_, C>,
    satisfied_by: &[WorkflowOutcome],
    list: &[Edge],
) -> Hook<Vec<BlockerRecord>> {
    let mut types: Vec<&str> = Vec::new();
    for edge in list {
        if !types.contains(&edge.schema.as_str()) {
            types.push(&edge.schema);
        }
    }
    let mut requests: Vec<ReadRequest> =
        types.iter().map(|name| ReadRequest::spec(*name)).collect();
    requests.extend(
        list.iter()
            .map(|edge| ReadRequest::row(&edge.schema, &edge.key)),
    );
    view.read_many(&requests)?;
    let mut flows = Vec::new();
    for name in &types {
        flows.push((*name, view.read_spec(name)?.and_then(one_workflow)));
    }
    let mut out = Vec::new();
    for edge in list {
        let flow = flows
            .iter()
            .find(|(name, _)| *name == edge.schema)
            .and_then(|(_, flow)| flow.as_ref());
        let row = view.read_row(&edge.schema, &edge.key)?;
        let status = match (row, flow) {
            (Some(row), Some((use_id, _))) => workflow::status_of(row, use_id).map(str::to_owned),
            _ => None,
        };
        let finished = match (flow, &status) {
            (Some((_, config)), Some(status)) => {
                state_outcome(config, status).is_some_and(|outcome| satisfied_by.contains(&outcome))
            }
            _ => false,
        };
        out.push(BlockerRecord {
            schema: edge.schema.clone(),
            id: edge.key.clone(),
            status,
            open: !finished,
            gone: row.is_none(),
        });
    }
    Ok(out)
}

/// The one rule the blocked field and the guard share: the blockers that
/// are open, of the row's every edge.
fn open_blockers(view: &View<'_, DependenciesConfig>) -> Hook<Vec<BlockerRecord>> {
    let rows = view.sub_rows(BLOCKERS)?;
    Ok(blockers(view, &view.config().satisfied_by, &edges(&rows))?
        .into_iter()
        .filter(|blocker| blocker.open && !blocker.gone)
        .collect())
}

// reaches reports whether a row is blocked by the goal, directly or through
// other blockers, through any table: breadth first, each level's specs and
// sub-table rows read together.
fn reaches<C>(view: &View<'_, C>, from: (&str, &str), goal: (&str, &str)) -> Hook<bool> {
    let mut seen: Vec<(String, String)> = vec![(from.0.to_owned(), from.1.to_owned())];
    let mut level: Vec<(String, String)> = seen.clone();
    while !level.is_empty() {
        let mut types: Vec<&str> = Vec::new();
        for (name, _) in &level {
            if !types.contains(&name.as_str()) {
                types.push(name);
            }
        }
        let specs: Vec<ReadRequest> = types.iter().map(|name| ReadRequest::spec(*name)).collect();
        view.read_many(&specs)?;
        // A table with no Dependencies holds no edges.
        let mut reads = Vec::new();
        for (name, key) in &level {
            if let Some(use_id) = view.read_spec(name)?.and_then(use_of) {
                reads.push((name.clone(), key.clone(), use_id));
            }
        }
        let requests: Vec<ReadRequest> = reads
            .iter()
            .map(|(name, key, use_id)| ReadRequest::sub_rows(name, key, use_id, BLOCKERS))
            .collect();
        view.read_many(&requests)?;
        let mut next = Vec::new();
        for (name, key, use_id) in &reads {
            for edge in edges(&view.sub_rows_of(name, key, use_id, BLOCKERS)?) {
                let node = (edge.schema, edge.key);
                if node.0 == goal.0 && node.1 == goal.1 {
                    return Ok(true);
                }
                if !seen.contains(&node) {
                    seen.push(node.clone());
                    next.push(node);
                }
            }
        }
        level = next;
    }
    Ok(false)
}

// use_of is the id of a table's Dependencies use: its behavior's name, as a
// use the table implements is.
fn use_of(spec: &TypeSpec) -> Option<String> {
    spec.uses
        .iter()
        .find(|entry| entry.behavior == NAME)
        .map(|entry| entry.id().to_owned())
}

/// Where an edge's checks point when they refuse: at the parameter they
/// name (the blocker's table or key) of an operation, or of a create's
/// blocker at its index; a veto with its declared code, the create's with
/// the pointer of its entry.
#[derive(Clone, Copy)]
enum Refusals<'a> {
    Operation(&'a str),
    Create(usize),
}

impl Refusals<'_> {
    fn param<C>(self, view: &View<'_, C>, at: &str, message: String) -> Stop {
        match self {
            Refusals::Operation(operation) => Refusal::operation_params(
                view.behavior(),
                view.use_id(),
                operation,
                vec![SchemaIssue::new(format!("/{at}"), message)],
            )
            .into(),
            Refusals::Create(index) => Refusal::create_params(
                view.type_name(),
                vec![SchemaIssue::new(
                    format!("/{}/blockers/{index}/{at}", view.use_id()),
                    message,
                )],
            )
            .into(),
        }
    }

    fn veto<C>(self, view: &View<'_, C>, reason: String, code: &str) -> Stop {
        let mut veto = Veto::new(reason).with_code(code);
        if let Refusals::Create(index) = self {
            let mut details = Map::new();
            details.insert(
                "path".into(),
                Value::from(format!("/behaviors/{}/blockers/{index}", view.use_id())),
            );
            veto = veto.with_details(details);
        }
        view.veto(veto)
    }
}

// blocker_params reads a blocker's table (the row's own when absent) and
// key, in its canonical form, refusing the row itself.
fn blocker_params<C>(
    view: &View<'_, C>,
    params: &Value,
    refuse: Refusals<'_>,
) -> Hook<(String, String)> {
    let schema = params
        .get("schema")
        .and_then(Value::as_str)
        .unwrap_or(view.type_name())
        .to_owned();
    let id = canonical_subject(params.get("id").and_then(Value::as_str).unwrap_or_default());
    let own = view.key().map(canonical_subject).unwrap_or_default();
    if schema == view.type_name() && id == own {
        return Err(refuse.param(
            view,
            "id",
            format!(
                "{} {} cannot block itself",
                view.type_name(),
                view.key().unwrap_or_default()
            ),
        ));
    }
    Ok((schema, id))
}

// status_now is the row's Workflow status: the one its Workflow holds, or,
// at its create before Workflow's initialize ran, its initial state.
fn status_now(view: &View<'_, DependenciesConfig>) -> String {
    let config = view.config();
    view.get_of(&config.status, workflow::STATUS)
        .and_then(|status| status.as_str().map(str::to_owned))
        .unwrap_or_else(|| config.initial.clone())
}

// add_edge makes a blocker block the row, with addBlocker's checks: a
// table the config lists that composes Workflow, a row that exists, not
// one already there, no cycle, and no open blocker of a row in a gated
// state no transition leaves. It inserts the edge and returns the blocker.
fn add_edge(
    cx: &Cx<'_, DependenciesConfig>,
    params: &Value,
    refuse: Refusals<'_>,
) -> Hook<BlockerRecord> {
    let (schema, id) = blocker_params(cx, params, refuse)?;
    let config = cx.config();
    if !config.schemas.contains(&schema) {
        return Err(refuse.param(
            cx,
            "schema",
            format!(
                "a blocker of {} is an instance of {}, not {schema}",
                cx.type_name(),
                config.schemas.join(", ")
            ),
        ));
    }
    if cx.read_spec(&schema)?.and_then(one_workflow).is_none() {
        return Err(refuse.param(
            cx,
            "schema",
            format!("{schema} does not compose Workflow, so its instances cannot block"),
        ));
    }
    // A row the command creates is one too: a sibling Blueprint stamped
    // before this one, in the same change set (D149).
    if cx.row_of(&schema, &id)?.is_none() {
        return Err(refuse.param(cx, "id", format!("{schema} {id} does not exist")));
    }
    let rows = cx.sub_rows(BLOCKERS)?;
    let held = edges(&rows);
    if held
        .iter()
        .any(|edge| edge.schema == schema && canonical_subject(&edge.key) == id)
    {
        return Err(refuse.veto(
            cx,
            format!("{schema} {id} already blocks it"),
            "already_blocking",
        ));
    }
    let own = cx.key().unwrap_or_default().to_owned();
    if reaches(cx, (&schema, &id), (cx.type_name(), &own))? {
        return Err(refuse.veto(
            cx,
            format!(
                "{schema} {id} is blocked by {} {own}, directly or through others: the edge would close a cycle",
                cx.type_name()
            ),
            "cycle",
        ));
    }
    let edge = Edge {
        number: held.iter().map(|edge| edge.number).max().unwrap_or(0) + 1,
        schema: schema.clone(),
        key: id.clone(),
    };
    let blocker = blockers(cx, &config.satisfied_by, std::slice::from_ref(&edge))?.remove(0);
    // A row in a gated state no transition leaves has passed its last gate:
    // a blocker could hold up nothing, and blocked would say it waits.
    if blocker.open {
        let status = status_now(cx);
        if config.terminal_gated_states.contains(&status) {
            return Err(refuse.veto(
                cx,
                format!(
                    "it is {status}, a gated state no transition leaves, so it takes no blocker that is not finished: {}",
                    blocker.described()
                ),
                "gated",
            ));
        }
    }
    let by = canonical_subject(&cx.caller().subject);
    let mut values = Map::new();
    values.insert(BLOCKER_TYPE.into(), Value::from(schema));
    values.insert(BLOCKER_KEY.into(), Value::from(id));
    values.insert(
        ADDED_BY.into(),
        if by.is_empty() {
            Value::Null
        } else {
            Value::from(by)
        },
    );
    values.insert(ADDED_AT.into(), Value::from(cx.now()));
    cx.insert_row(BLOCKERS, &key_of(edge.number), values)?;
    Ok(blocker)
}

// remove_blocker deletes the edge to a blocker, which may be gone.
fn remove_blocker(cx: &Cx<'_, DependenciesConfig>, params: &Value) -> Hook<Value> {
    let (schema, id) = blocker_params(cx, params, Refusals::Operation(REMOVE_BLOCKER))?;
    let rows = cx.sub_rows(BLOCKERS)?;
    let found: Vec<String> = rows
        .iter()
        .filter(|row| {
            Edge::of(row)
                .is_some_and(|edge| edge.schema == schema && canonical_subject(&edge.key) == id)
        })
        .map(|row| row.key.clone())
        .collect();
    if found.is_empty() {
        return Err(Refusal::operation_params(
            cx.behavior(),
            cx.use_id(),
            REMOVE_BLOCKER,
            vec![SchemaIssue::new(
                "/id",
                format!(
                    "{schema} {id} does not block {} {}",
                    cx.type_name(),
                    cx.key().unwrap_or_default()
                ),
            )],
        )
        .into());
    }
    for key in found {
        cx.delete_row(BLOCKERS, &key)?;
    }
    Ok(json!({ "schema": schema, "id": id }))
}

// list_blockers pages the row's blockers in the order they were added,
// with each one's status and whether it blocks; a gone one is left out.
fn list_blockers(cx: &Cx<'_, DependenciesConfig>, params: &Value) -> Hook<Value> {
    let request = page_request(params).map_err(|issue| -> Stop {
        Refusal::operation_params(cx.behavior(), cx.use_id(), LIST_BLOCKERS, vec![issue]).into()
    })?;
    let rows = cx.sub_rows(BLOCKERS)?;
    let after: Vec<Edge> = edges(&rows)
        .into_iter()
        .filter(|edge| edge.number > request.after)
        .collect();
    let read = blockers(cx, &cx.config().satisfied_by, &after)?;
    let listed: Vec<(i64, BlockerRecord)> = after
        .iter()
        .zip(read)
        .filter(|(_, blocker)| !blocker.gone)
        .map(|(edge, blocker)| (edge.number, blocker))
        .collect();
    let found = page(listed, request.limit, |(number, _)| *number);
    let mut out = Map::new();
    out.insert(
        "items".into(),
        Value::Array(
            found
                .items
                .iter()
                .map(|(_, blocker)| blocker.to_json())
                .collect(),
        ),
    );
    out.insert("next".into(), found.next.map_or(Value::Null, Value::String));
    Ok(Value::Object(out))
}

// strings reads a config's list of strings.
fn strings(value: Option<&Value>) -> Option<Vec<String>> {
    value.and_then(Value::as_array).map(|items| {
        items
            .iter()
            .filter_map(Value::as_str)
            .map(str::to_owned)
            .collect()
    })
}

/// Dependencies: blockers that hold up the table's Workflow.
#[derive(Clone, Copy, Debug, Default)]
pub struct Dependencies;

impl Behavior for Dependencies {
    type Config = DependenciesConfig;

    // The declaration file declares the blockers sub-table and blocked,
    // computed at a read; listDependents, the engine's alone, is left out.
    fn declaration(&self) -> Declaration {
        let mut declared = declared::declaration(DECLARATION);
        declared
            .operations
            .retain(|operation| operation.name != LIST_DEPENDENTS);
        declared
    }

    fn parse_config(
        &self,
        raw: &Value,
        target: &ConfigTarget<'_>,
    ) -> Result<DependenciesConfig, ConfigError> {
        DependenciesConfig::parse(raw, target)
    }

    // Any change of a use that stays; it joins a table with rows, which
    // start with no blockers, and cannot leave one.
    fn config_change(
        &self,
        _cx: &ConfigCx<'_>,
        before: Option<&DependenciesConfig>,
        after: Option<&DependenciesConfig>,
    ) -> Hook<Option<String>> {
        Ok((before.is_some() && after.is_none())
            .then(|| "the edges and references its instances hold would stay behind".to_owned()))
    }

    fn check_create_params(
        &self,
        _config: &DependenciesConfig,
        params: &Value,
    ) -> Vec<SchemaIssue> {
        create_issues(params).into_issues()
    }

    fn check_params(
        &self,
        _config: &DependenciesConfig,
        operation: &str,
        params: &Value,
    ) -> Vec<SchemaIssue> {
        let mut issues = Issues::default();
        match operation {
            ADD_BLOCKER | REMOVE_BLOCKER => blocker_issues("", params, &mut issues),
            LIST_BLOCKERS => page_issues(params, &mut issues),
            _ => {}
        }
        issues.into_issues()
    }

    // A create's blockers, each added with addBlocker's checks.
    fn initialize(&self, cx: &Cx<'_, DependenciesConfig>, params: &Value) -> Hook<()> {
        let given = params
            .get("blockers")
            .and_then(Value::as_array)
            .cloned()
            .unwrap_or_default();
        for (index, blocker) in given.iter().enumerate() {
            add_edge(cx, blocker, Refusals::Create(index))?;
        }
        Ok(())
    }

    // The gate: a transition of the table's Workflow into a gated state
    // waits for every blocker.
    fn guard(
        &self,
        view: &View<'_, DependenciesConfig>,
        request: &GuardRequest<'_>,
    ) -> Hook<Option<Veto>> {
        let config = view.config();
        let Some(params) = request.operation_of(&config.status, workflow::TRANSITION) else {
            return Ok(None);
        };
        let to = params.get("to").and_then(Value::as_str).unwrap_or_default();
        if !config.gated_states.iter().any(|state| state == to) {
            return Ok(None);
        }
        let open = open_blockers(view)?;
        if open.is_empty() {
            return Ok(None);
        }
        let mut details = Map::new();
        details.insert(
            "blockers".into(),
            Value::Array(
                open.iter()
                    .map(|blocker| {
                        let mut out = Map::new();
                        out.insert("schema".into(), Value::from(blocker.schema.as_str()));
                        out.insert("id".into(), Value::from(blocker.id.as_str()));
                        if let Some(status) = &blocker.status {
                            out.insert("status".into(), Value::from(status.as_str()));
                        }
                        Value::Object(out)
                    })
                    .collect(),
            ),
        );
        Ok(Some(
            Veto::new(format!(
                "{} {} cannot move to {to} while it is blocked by {}",
                view.type_name(),
                view.key().unwrap_or_default(),
                open.iter()
                    .map(BlockerRecord::described)
                    .collect::<Vec<_>>()
                    .join(", ")
            ))
            .with_code("blocked")
            .with_details(details),
        ))
    }

    fn operation(
        &self,
        cx: &Cx<'_, DependenciesConfig>,
        name: &str,
        params: &Value,
    ) -> Hook<Value> {
        match name {
            ADD_BLOCKER => Ok(add_edge(cx, params, Refusals::Operation(ADD_BLOCKER))?.to_json()),
            REMOVE_BLOCKER => remove_blocker(cx, params),
            LIST_BLOCKERS => list_blockers(cx, params),
            other => Err(cx.defect(format!(
                "it declares operation {other} and has no handler for it"
            ))),
        }
    }

    // A read shows whether a blocker is open.
    fn read_view(&self, view: &View<'_, DependenciesConfig>) -> Hook<Map<String, Value>> {
        let mut fields = Map::new();
        fields.insert(
            BLOCKED.into(),
            Value::Bool(!open_blockers(view)?.is_empty()),
        );
        Ok(fields)
    }
}

// ---- the schemas' checks ----

// config_issues lists what a config breaks of the declaration's
// configSchema, in the schema loader's words.
fn config_issues(raw: &Value) -> Issues {
    let mut issues = Issues::default();
    let Some(config) = raw.as_object() else {
        issues.push("", "type", "must be object");
        return issues;
    };
    for key in config
        .keys()
        .filter(|key| !CONFIG_KEYS.contains(&key.as_str()))
    {
        issues.push(
            "",
            "additionalProperties",
            format!("unknown key {}", schema::quoted(key)),
        );
    }
    if let Some(schemas) = config.get("schemas") {
        list_issues("/schemas", schemas, &mut issues, |at, item, issues| {
            pattern_issues(at, item, SCHEMA_PATTERN, issues)
        });
    }
    if let Some(states) = config.get("gatedStates") {
        list_issues("/gatedStates", states, &mut issues, schema::text_issues);
    }
    if let Some(outcomes) = config.get("satisfiedBy") {
        // Its items name no type, so ajv compares them pair by pair.
        untyped_list_issues("/satisfiedBy", outcomes, &mut issues, |at, item, issues| {
            if !item.as_str().is_some_and(|text| OUTCOMES.contains(&text)) {
                issues.push(
                    at,
                    "enum",
                    format!("must be one of {}", OUTCOMES.map(schema::quoted).join(", ")),
                );
            }
        });
    }
    issues
}

// list_issues lists what a value breaks of `{ type: array, minItems: 1,
// uniqueItems: true, items }`, each item held to its own check.
fn list_issues(
    at: &str,
    value: &Value,
    issues: &mut Issues,
    item_issues: impl Fn(&str, &Value, &mut Issues),
) {
    let Some(items) = value.as_array() else {
        issues.push(at, "type", "must be array");
        return;
    };
    if items.is_empty() {
        issues.push(at, "minItems", "must NOT have fewer than 1 items");
    }
    for (i, item) in items.iter().enumerate() {
        item_issues(&format!("{at}/{i}"), item, issues);
    }
    if let Some((j, i)) = schema::duplicate_value(items) {
        issues.push(
            at,
            "uniqueItems",
            format!("must NOT have duplicate items (items ## {j} and {i} are identical)"),
        );
    }
}

// untyped_list_issues is list_issues for a list whose items name no type,
// whose duplicate ajv finds pair by pair (`rollups::untyped_duplicate`).
fn untyped_list_issues(
    at: &str,
    value: &Value,
    issues: &mut Issues,
    item_issues: impl Fn(&str, &Value, &mut Issues),
) {
    let Some(items) = value.as_array() else {
        issues.push(at, "type", "must be array");
        return;
    };
    if items.is_empty() {
        issues.push(at, "minItems", "must NOT have fewer than 1 items");
    }
    for (i, item) in items.iter().enumerate() {
        item_issues(&format!("{at}/{i}"), item, issues);
    }
    if let Some((j, i)) = crate::rollups::untyped_duplicate(items) {
        issues.push(
            at,
            "uniqueItems",
            format!("must NOT have duplicate items (items ## {j} and {i} are identical)"),
        );
    }
}

// blocker_issues lists what a blocker breaks of its closed object, `{ schema?,
// id }`, in the behavior registry's words: addBlocker's and removeBlocker's
// parameters, and each blocker of a create.
fn blocker_issues(at: &str, value: &Value, issues: &mut Issues) {
    let Some(given) = value.as_object() else {
        issues.push(at, "type", "must be object");
        return;
    };
    for key in given
        .keys()
        .filter(|key| !BLOCKER_KEYS.contains(&key.as_str()))
    {
        issues.push(
            at,
            "additionalProperties",
            format!("must NOT have additional properties: {key}"),
        );
    }
    if !given.contains_key("id") {
        issues.push(at, "required", "must have required property 'id'");
    }
    if let Some(name) = given.get("schema") {
        pattern_issues(&schema::member(at, "schema"), name, SCHEMA_PATTERN, issues);
    }
    if let Some(id) = given.get("id") {
        pattern_issues(&schema::member(at, "id"), id, ID_PATTERN, issues);
    }
}

// page_issues lists what listBlockers' parameters break of `{ limit?: 1 to
// 500, cursor?: string }`.
fn page_issues(params: &Value, issues: &mut Issues) {
    let Some(given) = params.as_object() else {
        issues.push("", "type", "must be object");
        return;
    };
    for key in given
        .keys()
        .filter(|key| !PAGE_KEYS.contains(&key.as_str()))
    {
        issues.push(
            "",
            "additionalProperties",
            format!("must NOT have additional properties: {key}"),
        );
    }
    if let Some(limit) = given.get("limit") {
        schema::integer_issues("/limit", limit, Some(1), Some(500), issues);
    }
    if let Some(cursor) = given.get("cursor") {
        if !cursor.is_string() {
            issues.push("/cursor", "type", "must be string");
        }
    }
}

// create_issues lists what a create's parameters break of the
// createParamsSchema, `{ blockers?: [blocker] }` of at most 500.
fn create_issues(params: &Value) -> Issues {
    let mut issues = Issues::default();
    let Some(given) = params.as_object() else {
        issues.push("", "type", "must be object");
        return issues;
    };
    for key in given
        .keys()
        .filter(|key| !CREATE_KEYS.contains(&key.as_str()))
    {
        issues.push(
            "",
            "additionalProperties",
            format!("must NOT have additional properties: {key}"),
        );
    }
    if let Some(blockers) = given.get("blockers") {
        let Some(items) = blockers.as_array() else {
            issues.push("/blockers", "type", "must be array");
            return issues;
        };
        if items.len() > MAX_CREATE_BLOCKERS {
            issues.push(
                "/blockers",
                "maxItems",
                format!("must NOT have more than {MAX_CREATE_BLOCKERS} items"),
            );
        }
        for (i, item) in items.iter().enumerate() {
            blocker_issues(&format!("/blockers/{i}"), item, &mut issues);
        }
    }
    issues
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
        is_id(text)
    };
    if !matches {
        issues.push(at, "pattern", format!("must match pattern \"{pattern}\""));
    }
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

// is_id matches ID_PATTERN: a letter or digit, then at most 255 letters,
// digits, dots, underscores, colons and hyphens, ASCII alone.
fn is_id(text: &str) -> bool {
    let mut chars = text.chars();
    chars
        .next()
        .is_some_and(|first| first.is_ascii_alphanumeric())
        && text.chars().count() <= 256
        && chars.all(|c| c.is_ascii_alphanumeric() || matches!(c, '.' | '_' | ':' | '-'))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn an_edges_key_is_its_number_in_ten_digits() {
        assert_eq!(key_of(42), "0000000042");
        let row = SubRow::new(
            "0000000042",
            json!({"blockerType": "Task", "blockerKey": "t1"})
                .as_object()
                .cloned()
                .unwrap_or_default(),
        );
        let edge = Edge::of(&row).expect("an edge");
        assert_eq!(
            (edge.number, edge.schema.as_str(), edge.key.as_str()),
            (42, "Task", "t1")
        );
        assert!(Edge::of(&SubRow::new("x", Map::new())).is_none());
    }

    #[test]
    fn the_patterns_match_as_the_schemas_do() {
        assert!(is_id("t1") && is_id("a.b:c-d_e") && !is_id("bad id") && !is_id("-x"));
        assert!(is_schema("Task") && !is_schema("_Task"));
    }
}
