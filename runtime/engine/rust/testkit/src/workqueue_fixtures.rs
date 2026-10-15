//! The behaviors the work queue's tests register beside the package's
//! (`runtime/engine-workqueue/typescript/test/fixtures.ts`), ported to Rust
//! with the same declarations and semantics (D58): [`Signal`] sends a
//! directive from its own operation, as a budget does on an overrun;
//! [`Gate`] refuses changes of an instance by its title; [`Lock`] forbids
//! some principals to claim an instance by its title; and
//! [`BudgetStandIn`] holds Queue's claim to the contract Budget implements.
//! The core meta-schema admits only the core's behaviors, so an engine that
//! composes them loads against the open one ([`crate::open_meta_schema`]).

use std::sync::{Arc, LazyLock, Mutex, PoisonError};

use serde_json::{json, Value};

use superschematic_engine::errors::Veto;
use superschematic_engine::{
    Behavior, ConfigTarget, EngineError, EngineErrorCode, Error, GuardRequest, Hooks, Result,
};

fn said(reason: &str) -> Option<Veto> {
    Some(Veto {
        reason: reason.to_owned(),
        code: None,
        details: None,
    })
}

/// test.Signal: sends the holder a directive from its own operation,
/// through `call()`.
pub struct Signal;

static SIGNAL_DECLARATION: LazyLock<String> = LazyLock::new(|| {
    json!({
        "name": "test.Signal",
        "operations": [
            {
                "name": "signal",
                "paramsSchema": {"type": "object", "additionalProperties": false, "required": ["name"], "properties": {"name": {"type": "string"}}},
                "resultSchema": true,
                "writes": true
            }
        ]
    })
    .to_string()
});

impl Behavior for Signal {
    type Config = Value;

    fn declaration(&self) -> &str {
        &SIGNAL_DECLARATION
    }

    fn parse_config(&self, raw: &Value, _target: &ConfigTarget<'_>) -> Result<Value> {
        Ok(raw.clone())
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            operations: vec![("signal", |_, cx, params| {
                cx.call("Lease", "direct", &json!({ "name": params.get("name") }))
            })],
            ..Hooks::default()
        }
    }
}

/// test.Gate: refuses Workflow's transition of an instance titled held,
/// and Lease's expire of one titled stuck.
pub struct Gate;

impl Behavior for Gate {
    type Config = Value;

    fn declaration(&self) -> &str {
        r#"{"name": "test.Gate"}"#
    }

    fn parse_config(&self, raw: &Value, _target: &ConfigTarget<'_>) -> Result<Value> {
        Ok(raw.clone())
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            guard: Some(|_, view, request| {
                let GuardRequest::Operation {
                    behavior,
                    operation,
                    ..
                } = request
                else {
                    return Ok(None);
                };
                let data = view.data()?;
                let title = data.get("title").and_then(Value::as_str);
                Ok(match (title, *behavior, *operation) {
                    (Some("held"), "Workflow", "transition") => said("it is held"),
                    (Some("stuck"), "Lease", "expire") => said("it is stuck"),
                    _ => None,
                })
            }),
            ..Hooks::default()
        }
    }
}

/// test.Lock: forbids Queue's claim of an instance titled locked to a
/// principal without jobs.unlock, a refusal of one caller that another may
/// not get.
pub struct Lock;

impl Behavior for Lock {
    type Config = Value;

    fn declaration(&self) -> &str {
        r#"{"name": "test.Lock"}"#
    }

    fn parse_config(&self, raw: &Value, _target: &ConfigTarget<'_>) -> Result<Value> {
        Ok(raw.clone())
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            guard: Some(|_, view, request| {
                let claim = matches!(
                    request,
                    GuardRequest::Operation {
                        behavior: "Queue",
                        operation: "claim",
                        ..
                    }
                );
                if claim
                    && view.data()?.get("title").and_then(Value::as_str) == Some("locked")
                    && !view.can("jobs.unlock")?
                {
                    return Err(EngineError::new(
                        EngineErrorCode::Forbidden,
                        format!(
                            "{} may not claim {} {}: it needs permission jobs.unlock",
                            view.principal().subject,
                            view.schema(),
                            view.id()
                        ),
                    )
                    .into());
                }
                Ok(None)
            }),
            ..Hooks::default()
        }
    }
}

/// A stand-in for Budget, under its name: `reserve({ meter?, amount? })`,
/// as Budget declares it, records the call with the lease and the status
/// it sees (`reserved`), and refuses an instance titled broke, as a
/// reservation that does not fit is refused; `checkReserve` says so of
/// broke.
#[derive(Clone, Default)]
pub struct BudgetStandIn {
    /// What reserve heard, in order: `{ id, params, holder, status }`.
    pub reserved: Arc<Mutex<Vec<Value>>>,
}

impl BudgetStandIn {
    /// What reserve heard since the last take.
    pub fn take(&self) -> Vec<Value> {
        std::mem::take(&mut self.reserved.lock().unwrap_or_else(PoisonError::into_inner))
    }
}

static BUDGET_DECLARATION: LazyLock<String> = LazyLock::new(|| {
    let params = json!({
        "type": "object",
        "additionalProperties": false,
        "properties": {"meter": {"type": "string"}, "amount": {"type": "integer"}}
    });
    json!({
        "name": "Budget",
        "operations": [
            {"name": "reserve", "paramsSchema": params, "resultSchema": true, "writes": true},
            {"name": "checkReserve", "paramsSchema": params, "resultSchema": true}
        ]
    })
    .to_string()
});

impl Behavior for BudgetStandIn {
    type Config = Value;

    fn declaration(&self) -> &str {
        &BUDGET_DECLARATION
    }

    fn parse_config(&self, raw: &Value, _target: &ConfigTarget<'_>) -> Result<Value> {
        Ok(raw.clone())
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            operations: vec![
                ("checkReserve", |_, cx, _| {
                    let fits = cx.data()?.get("title").and_then(Value::as_str) != Some("broke");
                    Ok(json!({ "fits": fits, "until": null, "scopes": [] }))
                }),
                ("reserve", |budget, cx, params| {
                    let seen = cx.instances().get(
                        cx.schema(),
                        cx.id(),
                        Some(&["Lease.holder", "Workflow.status"]),
                    )?;
                    let field = |behavior: &str, name: &str| {
                        seen.as_ref()
                            .and_then(|record| record.behavior_field(behavior, name))
                            .cloned()
                            .unwrap_or(Value::Null)
                    };
                    let mut heard = json!({
                        "id": cx.id(),
                        "params": params,
                        "holder": field("Lease", "holder"),
                        "status": field("Workflow", "status"),
                    });
                    // A member undefined in the TypeScript record is absent.
                    if let Some(heard) = heard.as_object_mut() {
                        heard.retain(|_, value| !value.is_null());
                    }
                    budget
                        .reserved
                        .lock()
                        .unwrap_or_else(PoisonError::into_inner)
                        .push(heard);
                    if cx.data()?.get("title").and_then(Value::as_str) == Some("broke") {
                        return Err(Error::veto(
                            "Budget",
                            "reserve",
                            cx.schema(),
                            cx.id(),
                            Veto {
                                reason: "the reservation does not fit".to_owned(),
                                code: None,
                                details: None,
                            },
                        ));
                    }
                    Ok(json!({}))
                }),
            ],
            ..Hooks::default()
        }
    }
}
