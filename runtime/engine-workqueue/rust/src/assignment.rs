//! Assignment, who an instance is assigned to (D16), the port of
//! `typescript/src/assignment.ts`: one principal at a time, or none. A
//! principal may assign an unassigned instance to itself, and an assignee
//! may unassign itself. Assigning another principal, reassigning, and
//! unassigning another principal need the config's permission; without
//! one in the config, only those two self-service moves exist. The engine
//! knows no principals, so it cannot check that another principal exists:
//! only a principal trusted with the permission names one.
//!
//! While the instance is assigned, the guard refuses Lease's acquire and
//! Queue's claim by every principal but the assignee, whoever made the
//! call (a claim calls acquire for its caller), so an assigned instance is
//! claimed only by its assignee.
//!
//! A list filters on the assignee (`where: { "Assignment.assignee":
//! "alice" }`), null keeping the unassigned instances, through an index on
//! its column.
//!
//! Every refusal is a veto with a code the declaration lists
//! (`assigned_to_another`, `already_assigned`, `not_assigned`,
//! `not_configured`), but a move the config's permission allows and the
//! caller lacks, which is forbidden. Every statement is the behavior's
//! columns, the same on SQLite and Postgres (D58).
//!
//! configChange: the permission may change. Assignment can be added to a
//! schema that has instances, which start unassigned, and cannot be
//! removed from one: the assignments they hold would stay behind and come
//! back if it were added again.

use serde::Deserialize;
use serde_json::{json, Value};

use superschematic_engine::behaviors::{
    BehaviorFilter, BehaviorMigration, ColumnSpec, InstanceView, OperationCx, Scope,
};
use superschematic_engine::errors::Veto;
use superschematic_engine::storage::SqlValue;
use superschematic_engine::{
    json_config, Behavior, ConfigTarget, EngineError, EngineErrorCode, Error, GuardRequest, Hooks,
    Result,
};

const NAME: &str = "Assignment";

/// Assignment's config.
#[derive(Clone, Debug, Default, PartialEq, Eq, Deserialize)]
pub struct AssignmentConfig {
    /// The permission that assigns another principal, reassigns, and
    /// unassigns another principal.
    #[serde(default)]
    pub permission: Option<String>,
}

fn vetoed(
    view: &InstanceView<'_, AssignmentConfig>,
    operation: &str,
    reason: &str,
    code: &str,
) -> Error {
    Error::veto(
        NAME,
        operation,
        view.schema(),
        view.id(),
        Veto {
            reason: reason.to_owned(),
            code: Some(code.to_owned()),
            details: None,
        },
    )
}

/// The operations an assignment keeps to its assignee: taking the lease,
/// and claiming the instance.
const GATED: [(&str, &str); 2] = [("Lease", "acquire"), ("Queue", "claim")];

fn assignee_of(view: &InstanceView<'_, AssignmentConfig>) -> Result<Option<String>> {
    Ok(view
        .columns()
        .get()?
        .opt_text("assignee")?
        .map(str::to_owned))
}

fn privileged(scope: &Scope<'_, AssignmentConfig>) -> Result<bool> {
    match &scope.config().permission {
        Some(permission) => scope.can(permission),
        None => Ok(false),
    }
}

// refused is the refusal of a move only the permission allows: forbidden
// when the config names one the caller lacks, vetoed when it names none.
fn refused(view: &InstanceView<'_, AssignmentConfig>, operation: &str, what: &str) -> Error {
    match &view.config().permission {
        None => vetoed(
            view,
            operation,
            &format!("{what} needs a permission, and its config names none"),
            "not_configured",
        ),
        Some(permission) => EngineError::new(
            EngineErrorCode::Forbidden,
            format!(
                "{} may not {operation} {} {}: {what} needs permission {permission}",
                view.principal().subject,
                view.schema(),
                view.id()
            ),
        )
        .into(),
    }
}

/// Assignment: the principal each instance is assigned to, or none.
#[derive(Clone, Copy, Debug, Default)]
pub struct Assignment;

impl Behavior for Assignment {
    type Config = AssignmentConfig;

    fn declaration(&self) -> &str {
        include_str!("../declarations/Assignment.behavior.json")
    }

    fn parse_config(&self, raw: &Value, _target: &ConfigTarget<'_>) -> Result<AssignmentConfig> {
        json_config(raw)
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            config_change: Some(|_, before, after, _| {
                Ok((before.is_some() && after.is_none())
                    .then(|| "the assignments its instances hold would stay behind".to_owned()))
            }),
            guidance: Some(|_, config, target| {
                crate::guidance::assignment::guidance(config, target)
            }),
            migrations: vec![
                BehaviorMigration::new(1, "assignment")
                    .column("assignee", ColumnSpec::text())
                    .column("assigned_at", ColumnSpec::integer())
                    .column("assigned_by", ColumnSpec::text()),
                // The index lets a list that filters on the assignee read
                // only one principal's instances, or the unassigned ones, in
                // creation order.
                BehaviorMigration::new(2, "assignee index").index("assignee", &["assignee"]),
            ],
            filters: vec![("assignee", BehaviorFilter::new("assignee", "string"))],
            guard: Some(guard),
            operations: vec![("assign", assign), ("unassign", unassign)],
            fields: vec![(
                "assignee",
                |_, view| Ok(assignee_of(view)?.map(Value::from)),
            )],
            ..Hooks::default()
        }
    }
}

// guard keeps Lease's acquire and Queue's claim of an assigned instance to
// its assignee, whoever made the call.
fn guard(
    _: &Assignment,
    view: &InstanceView<'_, AssignmentConfig>,
    request: &GuardRequest<'_>,
) -> Result<Option<Veto>> {
    let GuardRequest::Operation {
        behavior,
        operation,
        ..
    } = request
    else {
        return Ok(None);
    };
    if !GATED.contains(&(*behavior, *operation)) {
        return Ok(None);
    }
    match assignee_of(view)? {
        Some(assignee) if assignee != view.principal().subject => Ok(Some(Veto {
            reason: "it is assigned to another principal, who alone may take it".to_owned(),
            code: Some("assigned_to_another".to_owned()),
            details: None,
        })),
        _ => Ok(None),
    }
}

fn assign(_: &Assignment, cx: &OperationCx<'_, AssignmentConfig>, params: &Value) -> Result<Value> {
    let to = params.get("to").and_then(Value::as_str).unwrap_or_default();
    let subject = cx.principal().subject.as_str();
    let current = assignee_of(cx)?;
    if current.as_deref() == Some(to) {
        return Err(vetoed(
            cx,
            "assign",
            if to == subject {
                "it is already assigned to the caller"
            } else {
                "it is already assigned to that principal"
            },
            "already_assigned",
        ));
    }
    if !privileged(cx)? {
        if to != subject {
            return Err(refused(cx, "assign", "assigning another principal"));
        }
        if current.is_some() {
            return Err(refused(
                cx,
                "assign",
                "reassigning an instance assigned to another principal",
            ));
        }
    }
    cx.columns().set(&[
        ("assignee", SqlValue::from(to)),
        ("assigned_at", SqlValue::Integer(cx.now())),
        ("assigned_by", SqlValue::from(subject)),
    ])?;
    Ok(json!({ "assignee": to, "assignedAt": cx.now(), "assignedBy": subject }))
}

fn unassign(_: &Assignment, cx: &OperationCx<'_, AssignmentConfig>, _: &Value) -> Result<Value> {
    let Some(current) = assignee_of(cx)? else {
        return Err(vetoed(cx, "unassign", "it is not assigned", "not_assigned"));
    };
    if current != cx.principal().subject && !privileged(cx)? {
        return Err(refused(cx, "unassign", "unassigning another principal"));
    }
    cx.columns().set(&[
        ("assignee", SqlValue::Null),
        ("assigned_at", SqlValue::Null),
        ("assigned_by", SqlValue::Null),
    ])?;
    Ok(json!({ "assignee": current }))
}
