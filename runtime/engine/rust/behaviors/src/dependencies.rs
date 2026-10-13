//! Dependencies, the core's blockers between instances (D16, amended), the
//! port of `src/behaviors/core/dependencies.ts`. An edge says a blocker
//! holds up its dependent. A blocker is an instance of the dependent's own
//! schema, or of one the config lists, in the same namespace, and its
//! schema composes Workflow. It is finished once its status is a terminal
//! state of its own schema's Workflow config whose outcome
//! ([`crate::state_outcome`]) the dependent's `satisfiedBy` lists, success
//! by default, and open until then: a blocker that failed stays open, so it
//! does not let its dependents through, until it is removed. The dependent
//! is blocked while any blocker is open. One function, `open_blockers`,
//! answers that for the `blocked` field and the guard alike, over every
//! blocker, whatever its schema.
//!
//! The guard gates the type's own Workflow: a transition into a gated state
//! (every terminal state of the type's Workflow, or the states the config
//! lists, terminal or not) is refused while the instance is blocked. A gate
//! on a state that is not terminal holds up the start of work, a move from
//! todo to doing say, as a gate on a terminal one holds up its end. It
//! reads the state from transition's `to`, which Workflow's closed
//! paramsSchema makes the only parameter, and it runs for every transition
//! request, a caller's, another behavior's `call()` and another instance's
//! invoke alike, before any handler. An instance in a gated state that is
//! terminal takes no open blocker: no transition leaves it, and its gate
//! let it in with every blocker finished, so it is never blocked. One in a
//! gated state that is not terminal takes one, which holds up its next
//! move into a gated state; it is how a dependency found during the work is
//! recorded.
//!
//! `addBlocker` refuses an edge that would close a cycle, in any schema:
//! the new blocker must not be blocked by the dependent, directly or
//! through others. A create may give blockers too, in its parameters
//! (initialize), each held to addBlocker's checks in the create's
//! transaction, so an instance is blocked from its first event and never
//! claimable before its edges exist. Its status then is its Workflow's
//! initial state, so the same finished/open rule, satisfiedBy and the
//! terminal gated state rule apply: an initial state that is gated and that
//! no transition leaves takes no open blocker. Each edge is also a
//! reference the engine records, which hears the blocker's delete alone
//! (blocked is computed at each read, so nothing else of the blocker needs
//! hearing, and the field is volatile), so deleting a blocker runs
//! `after_reference_change` on each dependent, which removes the edge with
//! removeBlocker, as the caller: its own event records it. Deleting a
//! dependent deletes its edges. Blockers are read as the caller, so a
//! caller who may not read a blocker's schema cannot read whether its
//! dependents are blocked.
//!
//! Every veto carries a code the declaration lists: the gate's `blocked`,
//! with the open blockers in its details, and an edge's
//! `already_blocking`, `cycle` and `gated`, which a create's blocker gets
//! as addBlocker's would, with the pointer of its entry.
//!
//! Storage (D58): an edge's number, which orders the lists' pages, is a
//! row number the database assigns (SQLite's rowid, a Postgres identity
//! column); every statement is the same on both databases.
//!
//! configChange: schemas, gatedStates and satisfiedBy may change (existing
//! edges stay, and `parse_config` holds gatedStates to the new Workflow's
//! states). Dependencies can be added to a schema that has instances, which
//! start with no blockers, and cannot be removed from one: its edges and
//! references would stay behind.

use std::collections::HashMap;

use serde_json::{json, Map, Value};

use superschematic_engine::behaviors::{
    page, page_request, BehaviorMigration, ColumnSpec, Hears, IndexSpec, InstanceCx, InstanceView,
    Page, TableSpec, MAX_BATCH_READ,
};
use superschematic_engine::errors::{SchemaIssue, Veto};
use superschematic_engine::storage::{Row, SqlValue};
use superschematic_engine::{
    Behavior, ConfigTarget, EngineError, Error, GuardRequest, Hooks, InstanceChange, OperationCx,
    Result,
};

use crate::workflow::{is_terminal_state, state_outcome, WorkflowOutcome};

const NAME: &str = "Dependencies";

/// Dependencies' config, parsed: the defaults filled in.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct DependenciesConfig {
    /// The schemas a blocker may be an instance of.
    pub schemas: Vec<String>,
    /// The states of the type's Workflow a transition into waits for every
    /// blocker to finish.
    pub gated_states: Vec<String>,
    /// The outcomes of a blocker's terminal state that finish it.
    pub satisfied_by: Vec<WorkflowOutcome>,
    /// The gated states no transition of the type's Workflow leaves: an
    /// instance in one takes no open blocker.
    pub terminal_gated_states: Vec<String>,
}

/// A blocker, as addBlocker and listBlockers return it.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct BlockerRecord {
    pub schema: String,
    pub id: String,
    /// Its Workflow status; `None` when it has none.
    pub status: Option<String>,
    /// Whether it is not finished: its status is not a terminal state of
    /// its schema's Workflow whose outcome satisfiedBy lists.
    pub open: bool,
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

    // described names the blocker in a message: its schema, its id and its
    // status.
    fn described(&self) -> String {
        match &self.status {
            Some(status) => format!("{} {} ({status})", self.schema, self.id),
            None => format!("{} {}", self.schema, self.id),
        }
    }
}

struct Edge {
    edge: i64,
    schema: String,
    id: String,
}

// The behavior's rows of one instance, as its SQL names them.
fn key<C>(view: &InstanceView<'_, C>) -> Vec<SqlValue> {
    vec![
        SqlValue::from(view.namespace()),
        SqlValue::from(view.schema()),
        SqlValue::from(view.id()),
    ]
}

// edges reads the instance's blockers, in the order they were added, after
// an edge's number and up to a limit (every one when None).
fn edges<C>(view: &InstanceView<'_, C>, after: i64, limit: Option<i64>) -> Result<Vec<Edge>> {
    let table = view.sql().table("edges")?;
    let mut values = key(view);
    values.push(SqlValue::Integer(after));
    if let Some(limit) = limit {
        values.push(SqlValue::Integer(limit));
    }
    let rows = view.sql().all(
        format!(
            "SELECT edge, blocker_schema, blocker_id FROM {table}
             WHERE namespace = ? AND schema = ? AND id = ? AND edge > ? ORDER BY edge{}",
            if limit.is_some() { " LIMIT ?" } else { "" }
        ),
        &values,
    )?;
    rows.iter()
        .map(|row| {
            Ok(Edge {
                edge: row.i64("edge")?,
                schema: row.text("blocker_schema")?.to_owned(),
                id: row.text("blocker_id")?.to_owned(),
            })
        })
        .collect()
}

// blockers reads each blocker's status as the caller: its schema's
// Workflow config and the instance's status field. A blocker is open unless
// its status is a terminal state of that config whose outcome the
// dependent's satisfiedBy lists. One whose row is gone while its edge
// remains, which only happens while its delete runs the hook that removes
// the edge, stays open until the edge goes, so the dependent's
// removeBlocker event records blocked changing.
fn blockers(
    view: &InstanceView<'_, DependenciesConfig>,
    list: &[Edge],
) -> Result<Vec<BlockerRecord>> {
    let mut by_schema: Vec<(&str, Vec<&str>)> = Vec::new();
    for edge in list {
        match by_schema
            .iter_mut()
            .find(|(schema, _)| *schema == edge.schema)
        {
            Some((_, ids)) => ids.push(&edge.id),
            None => by_schema.push((&edge.schema, vec![&edge.id])),
        }
    }
    let mut found: HashMap<(String, String), BlockerRecord> = HashMap::new();
    for (schema, ids) in &by_schema {
        let flow = view.schemas().config(schema, "Workflow")?;
        for chunk in ids.chunks(MAX_BATCH_READ) {
            for (id, record) in
                view.instances()
                    .get_many(schema, chunk, Some(&["Workflow.status"]))?
            {
                let status = record
                    .behavior_field("Workflow", "status")
                    .and_then(Value::as_str)
                    .map(str::to_owned);
                let outcome = match (&flow, &status) {
                    (Some(flow), Some(status)) => state_outcome(flow, status),
                    _ => None,
                };
                let finished =
                    outcome.is_some_and(|outcome| view.config().satisfied_by.contains(&outcome));
                found.insert(
                    ((*schema).to_owned(), id.clone()),
                    BlockerRecord {
                        schema: (*schema).to_owned(),
                        id,
                        status,
                        open: !finished,
                    },
                );
            }
        }
    }
    Ok(list
        .iter()
        .map(|edge| {
            found
                .get(&(edge.schema.clone(), edge.id.clone()))
                .cloned()
                .unwrap_or_else(|| BlockerRecord {
                    schema: edge.schema.clone(),
                    id: edge.id.clone(),
                    status: None,
                    open: true,
                })
        })
        .collect())
}

/// The one rule the blocked field and the guard share: the blockers that
/// are open.
fn open_blockers(view: &InstanceView<'_, DependenciesConfig>) -> Result<Vec<BlockerRecord>> {
    Ok(blockers(view, &edges(view, 0, None)?)?
        .into_iter()
        .filter(|blocker| blocker.open)
        .collect())
}

// reaches reports whether an instance is blocked by goal, directly or
// through other blockers, in any schema of the namespace.
fn reaches<C>(view: &InstanceView<'_, C>, from: (&str, &str), goal: (&str, &str)) -> Result<bool> {
    let table = view.sql().table("edges")?;
    let mut seen: Vec<(String, String)> = vec![(from.0.to_owned(), from.1.to_owned())];
    let mut queue: std::collections::VecDeque<(String, String)> =
        std::collections::VecDeque::from([(from.0.to_owned(), from.1.to_owned())]);
    while let Some((schema, id)) = queue.pop_front() {
        let rows = view.sql().all(
            format!(
                "SELECT blocker_schema, blocker_id FROM {table} WHERE namespace = ? AND schema = ? AND id = ?"
            ),
            &[
                SqlValue::from(view.namespace()),
                SqlValue::from(schema.as_str()),
                SqlValue::from(id.as_str()),
            ],
        )?;
        for row in rows {
            let next = (
                row.text("blocker_schema")?.to_owned(),
                row.text("blocker_id")?.to_owned(),
            );
            if next.0 == goal.0 && next.1 == goal.1 {
                return Ok(true);
            }
            if !seen.contains(&next) {
                seen.push(next.clone());
                queue.push_back(next);
            }
        }
    }
    Ok(false)
}

/// Where an edge's checks point when they refuse: at the parameter they
/// name (the blocker's schema or id) of an operation, or of a create's
/// blocker at its index; a veto with its declared code, the create's with
/// the pointer of its entry.
#[derive(Clone, Copy)]
enum Refusals<'a> {
    Operation(&'a str),
    Create(usize),
}

impl Refusals<'_> {
    fn param(self, cx: &InstanceCx<'_, DependenciesConfig>, at: &str, message: String) -> Error {
        match self {
            Refusals::Operation(operation) => EngineError::operation_params(
                NAME,
                operation,
                vec![SchemaIssue::new(format!("/{at}"), message)],
            )
            .into(),
            Refusals::Create(index) => EngineError::create_params(
                cx.schema(),
                vec![SchemaIssue::new(
                    format!("/behaviors/{NAME}/blockers/{index}/{at}"),
                    message,
                )],
            )
            .into(),
        }
    }

    fn veto(self, cx: &InstanceCx<'_, DependenciesConfig>, reason: String, code: &str) -> Error {
        let (action, details) = match self {
            Refusals::Operation(operation) => (operation, None),
            Refusals::Create(index) => {
                let mut details = Map::new();
                details.insert(
                    "path".into(),
                    Value::from(format!("/behaviors/{NAME}/blockers/{index}")),
                );
                ("create", Some(details))
            }
        };
        Error::veto(
            NAME,
            action,
            cx.schema(),
            cx.id(),
            Veto {
                reason,
                code: Some(code.to_owned()),
                details,
            },
        )
    }
}

// blocker_params reads a blocker's schema (the instance's own when absent)
// and id, refusing the instance itself.
fn blocker_params<'p>(
    cx: &InstanceCx<'_, DependenciesConfig>,
    params: &'p Value,
    refuse: Refusals<'_>,
) -> Result<(String, &'p str)> {
    let schema = params
        .get("schema")
        .and_then(Value::as_str)
        .unwrap_or(cx.schema())
        .to_owned();
    let id = params.get("id").and_then(Value::as_str).unwrap_or_default();
    if schema == cx.schema() && id == cx.id() {
        return Err(refuse.param(
            cx,
            "id",
            format!("{} {} cannot block itself", cx.schema(), cx.id()),
        ));
    }
    Ok((schema, id))
}

// status_of reads the instance's Workflow status as the caller; at its
// create, before Workflow's initialize when the type lists it later, the
// initial state its Workflow config gives.
fn status_of(cx: &InstanceCx<'_, DependenciesConfig>) -> Result<Option<String>> {
    let record = cx
        .instances()
        .get(cx.schema(), cx.id(), Some(&["Workflow.status"]))?;
    if let Some(status) = record
        .as_ref()
        .and_then(|record| record.behavior_field("Workflow", "status"))
        .and_then(Value::as_str)
    {
        return Ok(Some(status.to_owned()));
    }
    let flow = cx.schemas().config(cx.schema(), "Workflow")?;
    let Some(flow) = flow else {
        return Ok(None);
    };
    if let Some(initial) = flow.get("initial").and_then(Value::as_str) {
        return Ok(Some(initial.to_owned()));
    }
    Ok(flow
        .get("states")
        .and_then(Value::as_array)
        .and_then(|states| states.first())
        .and_then(Value::as_str)
        .map(str::to_owned))
}

// add_edge makes a blocker block the instance, with addBlocker's checks: a
// schema the config lists that composes Workflow, a blocker that exists,
// read as the caller, not one already added, no cycle, and no open blocker
// of an instance in a gated state no transition leaves. It records the
// edge and its reference, and returns the blocker.
fn add_edge(
    cx: &InstanceCx<'_, DependenciesConfig>,
    params: &Value,
    refuse: Refusals<'_>,
) -> Result<BlockerRecord> {
    let (schema, id) = blocker_params(cx, params, refuse)?;
    let config = cx.config();
    if !config.schemas.contains(&schema) {
        return Err(refuse.param(
            cx,
            "schema",
            format!(
                "a blocker of {} is an instance of {}, not {schema}",
                cx.schema(),
                config.schemas.join(", ")
            ),
        ));
    }
    if cx.schemas().config(&schema, "Workflow")?.is_none() {
        return Err(refuse.param(
            cx,
            "schema",
            format!("{schema} does not compose Workflow, so its instances cannot block"),
        ));
    }
    if cx.instances().get(&schema, id, Some(&[]))?.is_none() {
        return Err(refuse.param(cx, "id", format!("{schema} {id} does not exist")));
    }
    let table = cx.sql().table("edges")?;
    let mut values = key(cx);
    values.extend([SqlValue::from(schema.as_str()), SqlValue::from(id)]);
    if cx
        .sql()
        .get(
            format!("SELECT 1 AS found FROM {table} WHERE namespace = ? AND schema = ? AND id = ? AND blocker_schema = ? AND blocker_id = ?"),
            &values,
        )?
        .is_some()
    {
        return Err(refuse.veto(
            cx,
            format!("{schema} {id} already blocks it"),
            "already_blocking",
        ));
    }
    if reaches(cx, (&schema, id), (cx.schema(), cx.id()))? {
        return Err(refuse.veto(
            cx,
            format!(
                "{schema} {id} is blocked by {} {}, directly or through others: the edge would close a cycle",
                cx.schema(),
                cx.id()
            ),
            "cycle",
        ));
    }
    // An instance in a gated state no transition leaves has passed its last
    // gate: a blocker could hold up nothing, and blocked would say it
    // waits. At a create that is its Workflow's initial state, when no
    // transition leaves it.
    let blocker = blockers(
        cx,
        &[Edge {
            edge: 0,
            schema: schema.clone(),
            id: id.to_owned(),
        }],
    )?
    .remove(0);
    if blocker.open {
        if let Some(status) =
            status_of(cx)?.filter(|status| config.terminal_gated_states.contains(status))
        {
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
    values.extend([
        SqlValue::from(cx.principal().subject.as_str()),
        SqlValue::Integer(cx.now()),
    ]);
    cx.sql().run(
        format!("INSERT INTO {table} (namespace, schema, id, blocker_schema, blocker_id, created_by, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)"),
        &values,
    )?;
    cx.references().add(&schema, id, "", Some(Hears::Delete))?;
    Ok(blocker)
}

/// Dependencies: blockers that hold up the type's Workflow.
#[derive(Clone, Copy, Debug, Default)]
pub struct Dependencies;

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

impl Behavior for Dependencies {
    type Config = DependenciesConfig;

    fn declaration(&self) -> &str {
        include_str!("../declarations/Dependencies.behavior.json")
    }

    // The configSchema holds the shape; this holds gatedStates to the states
    // of the type's Workflow, whose config it is given, and fills in every
    // terminal state when the config names none.
    fn parse_config(&self, raw: &Value, target: &ConfigTarget<'_>) -> Result<DependenciesConfig> {
        let schemas =
            strings(raw.get("schemas")).unwrap_or_else(|| vec![target.schema().to_owned()]);
        let satisfied_by = match strings(raw.get("satisfiedBy")) {
            Some(outcomes) => outcomes
                .iter()
                .filter_map(|outcome| WorkflowOutcome::parse(outcome))
                .collect(),
            None => vec![WorkflowOutcome::Success],
        };
        let given = strings(raw.get("gatedStates"));
        let flow = target.configs().get("Workflow");
        let states = flow
            .and_then(|flow| flow.get("states"))
            .and_then(Value::as_array);
        let transitions = flow
            .and_then(|flow| flow.get("transitions"))
            .and_then(Value::as_array);
        let (Some(flow), Some(states), Some(_)) = (flow, states, transitions) else {
            // The type does not list Workflow, or gives it a config it
            // refuses: the requires check or Workflow's own config check
            // refuses it.
            return Ok(DependenciesConfig {
                schemas,
                gated_states: given.unwrap_or_default(),
                satisfied_by,
                terminal_gated_states: Vec::new(),
            });
        };
        let states: Vec<String> = states
            .iter()
            .filter_map(Value::as_str)
            .map(str::to_owned)
            .collect();
        for state in given.iter().flatten() {
            if !states.contains(state) {
                return Err(Error::config(format!(
                    "gated state \"{state}\" is not a state of the type's Workflow ({})",
                    states.join(", ")
                )));
            }
        }
        let gated_states = given.unwrap_or_else(|| {
            states
                .iter()
                .filter(|state| is_terminal_state(flow, state))
                .cloned()
                .collect()
        });
        let terminal_gated_states = gated_states
            .iter()
            .filter(|state| is_terminal_state(flow, state))
            .cloned()
            .collect();
        Ok(DependenciesConfig {
            schemas,
            gated_states,
            satisfied_by,
            terminal_gated_states,
        })
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            config_change: Some(|_, before, after, _| {
                Ok((before.is_some() && after.is_none()).then(|| {
                    "the edges and references its instances hold would stay behind".to_owned()
                }))
            }),
            guidance: Some(|_, config, target| {
                crate::guidance::dependencies::guidance(config, target)
            }),
            create_params_schema: Some(|_, config, target| {
                crate::guidance::dependencies::create_params(config, target)
            }),
            migrations: vec![BehaviorMigration::new(1, "edges").table(
                TableSpec::new("edges")
                    .row_id("edge")
                    .column("namespace", ColumnSpec::text().not_null())
                    .column("schema", ColumnSpec::text().not_null())
                    .column("id", ColumnSpec::text().not_null())
                    .column("blocker_schema", ColumnSpec::text().not_null())
                    .column("blocker_id", ColumnSpec::text().not_null())
                    .column("created_by", ColumnSpec::text().not_null())
                    .column("created_at", ColumnSpec::integer().not_null())
                    .unique(&["namespace", "schema", "id", "blocker_schema", "blocker_id"])
                    .index(IndexSpec::new(
                        "edges_by_blocker",
                        &["namespace", "blocker_schema", "blocker_id", "edge"],
                    )),
            )],
            guard: Some(guard),
            // A create's blockers, each added with addBlocker's checks.
            initialize: Some(|_, cx, params| {
                let given = params
                    .get("blockers")
                    .and_then(Value::as_array)
                    .cloned()
                    .unwrap_or_default();
                for (index, blocker) in given.iter().enumerate() {
                    add_edge(cx, blocker, Refusals::Create(index))?;
                }
                Ok(())
            }),
            operations: vec![
                ("addBlocker", |_, cx, params| {
                    Ok(add_edge(cx, params, Refusals::Operation("addBlocker"))?.to_json())
                }),
                ("removeBlocker", remove_blocker),
                ("listBlockers", |_, cx, params| {
                    let request = page_request(NAME, "listBlockers", params)?;
                    let rows = edges(cx, request.after, Some(request.limit + 1))?;
                    let found = page(rows, request.limit, |edge| edge.edge);
                    Ok(Page {
                        items: blockers(cx, &found.items)?
                            .iter()
                            .map(BlockerRecord::to_json)
                            .collect(),
                        next: found.next,
                    }
                    .to_json())
                }),
                ("listDependents", list_dependents),
            ],
            fields: vec![("blocked", |_, view| {
                Ok(Some(Value::Bool(!open_blockers(view)?.is_empty())))
            })],
            // A blocker's delete removes its edge, through removeBlocker,
            // as the caller.
            after_reference_change: Some(|_, cx, reference, change| {
                if matches!(change, InstanceChange::Delete) {
                    cx.instances().invoke(
                        cx.schema(),
                        cx.id(),
                        "removeBlocker",
                        &json!({"schema": reference.schema, "id": reference.id}),
                        None,
                    )?;
                }
                Ok(())
            }),
            after_change: Some(|_, cx, change| {
                if matches!(change, InstanceChange::Delete) {
                    let table = cx.sql().table("edges")?;
                    cx.sql().run(
                        format!(
                            "DELETE FROM {table} WHERE namespace = ? AND schema = ? AND id = ?"
                        ),
                        &key(cx),
                    )?;
                }
                Ok(())
            }),
            ..Hooks::default()
        }
    }
}

// guard is the gate: a transition of the type's Workflow into a gated
// state waits for every blocker.
fn guard(
    _: &Dependencies,
    view: &InstanceView<'_, DependenciesConfig>,
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
    if !view.config().gated_states.iter().any(|state| state == to) {
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
    Ok(Some(Veto {
        reason: format!(
            "{} {} cannot move to {to} while it is blocked by {}",
            view.schema(),
            view.id(),
            open.iter()
                .map(BlockerRecord::described)
                .collect::<Vec<_>>()
                .join(", ")
        ),
        code: Some("blocked".to_owned()),
        details: Some(details),
    }))
}

// removeBlocker reads nothing of the blocker, which may be the instance
// whose delete is removing its edges.
fn remove_blocker(
    _: &Dependencies,
    cx: &OperationCx<'_, DependenciesConfig>,
    params: &Value,
) -> Result<Value> {
    let (schema, id) = blocker_params(cx, params, Refusals::Operation("removeBlocker"))?;
    let table = cx.sql().table("edges")?;
    let mut values = key(cx);
    values.extend([SqlValue::from(schema.as_str()), SqlValue::from(id)]);
    let removed = cx.sql().run(
        format!("DELETE FROM {table} WHERE namespace = ? AND schema = ? AND id = ? AND blocker_schema = ? AND blocker_id = ?"),
        &values,
    )?;
    if removed.changes == 0 {
        return Err(EngineError::operation_params(
            NAME,
            "removeBlocker",
            vec![SchemaIssue::new(
                "/id",
                format!("{schema} {id} does not block {} {}", cx.schema(), cx.id()),
            )],
        )
        .into());
    }
    cx.references().remove(&schema, id, "")?;
    Ok(json!({"schema": schema, "id": id}))
}

// listDependents pages the instances this one blocks, in schemas the
// caller may read: a page holds fewer when it may not read some.
fn list_dependents(
    _: &Dependencies,
    cx: &OperationCx<'_, DependenciesConfig>,
    params: &Value,
) -> Result<Value> {
    let request = page_request(NAME, "listDependents", params)?;
    let table = cx.sql().table("edges")?;
    let rows = cx.sql().all(
        format!(
            "SELECT edge, schema, id FROM {table}
             WHERE namespace = ? AND blocker_schema = ? AND blocker_id = ? AND edge > ? ORDER BY edge LIMIT ?"
        ),
        &[
            SqlValue::from(cx.namespace()),
            SqlValue::from(cx.schema()),
            SqlValue::from(cx.id()),
            SqlValue::Integer(request.after),
            SqlValue::Integer(request.limit + 1),
        ],
    )?;
    let dependents = rows
        .iter()
        .map(|row: &Row| {
            Ok(Edge {
                edge: row.i64("edge")?,
                schema: row.text("schema")?.to_owned(),
                id: row.text("id")?.to_owned(),
            })
        })
        .collect::<Result<Vec<_>>>()?;
    let found = page(dependents, request.limit, |edge| edge.edge);
    let mut readable: HashMap<String, bool> = HashMap::new();
    let mut items = Vec::new();
    for edge in &found.items {
        let allowed = match readable.get(&edge.schema) {
            Some(answer) => *answer,
            None => {
                let answer = cx.schemas().readable(&edge.schema)?;
                readable.insert(edge.schema.clone(), answer);
                answer
            }
        };
        if allowed {
            items.push(json!({"schema": edge.schema, "id": edge.id}));
        }
    }
    Ok(Page {
        items,
        next: found.next,
    }
    .to_json())
}
