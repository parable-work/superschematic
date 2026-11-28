//! The implementation of fixture-wired-api in the generated wiring's
//! end-to-end test (#620, #622, #617, #610): its server, its workers and
//! its runtime parts are generated, and what is left to it is what only
//! an app knows. Its authenticator takes the caller from two headers,
//! `x-user` and `x-permissions`, which the generated server wraps in the
//! grants' authenticator. Its Wire watches a run's instance every second
//! and takes the agent routes' URL from WIRED_AGENT_ENDPOINT, and its
//! reviewer agent's instance writes its run token to WIRED_TOKEN_FILE, a
//! file of the test's, and exits.

use std::sync::Arc;
use std::time::Duration;

use async_trait::async_trait;
use http::request::Parts;
use schemas_fixture_wired_api_api::behaviors::EventRun;
use schemas_fixture_wired_api_api::database_types::orm::postgres::Repositories;
use schemas_fixture_wired_api_api::runtime::orm::agents::{AgentRunRequest, Agents, PrincipalRef};
use schemas_fixture_wired_api_api::runtime::orm::notify::RaiseInput;
use schemas_fixture_wired_api_api::runtime::orm::{FindOptions, OrderBy, Value, Values};
use schemas_fixture_wired_api_api::types;
use schemas_fixture_wired_api_api::{
    BoxError, Deps, Handlers, Wire, WiringDeps, WorkImplementation, WorkReviewArgs, Workers,
};
use superschematic_http_runtime::{ApiError, Authenticator, Principal, RequestContext};

fn internal(err: impl std::fmt::Display) -> ApiError {
    ApiError::internal(err.to_string())
}

fn text(value: &str) -> Value {
    Value::String(value.to_owned())
}

fn texts(values: &[&str]) -> Value {
    Value::Array(values.iter().map(|value| text(value)).collect())
}

fn row(members: Vec<(&str, Value)>) -> Values {
    members
        .into_iter()
        .map(|(name, value)| (name.to_owned(), value))
        .collect()
}

fn id_of(row: &Values) -> String {
    match row.get("id") {
        Some(Value::String(id)) => id.clone(),
        other => other.map(Value::to_string).unwrap_or_default(),
    }
}

/// Builds the implementation.
pub async fn create(deps: Deps) -> Result<Handlers, Box<dyn std::error::Error + Send + Sync>> {
    let deps = Arc::new(deps);
    Ok(Handlers {
        work: Arc::new(Work {
            deps: Arc::clone(&deps),
        }),
        authenticator: Arc::new(HeaderCaller),
    })
}

/// Implements WorkImplementation.
pub struct Work {
    pub deps: Arc<Deps>,
}

impl Work {
    fn principal(ctx: &RequestContext) -> Result<Principal, ApiError> {
        ctx.principal
            .clone()
            .ok_or_else(|| ApiError::unauthorized("Authentication required"))
    }
}

#[async_trait]
impl WorkImplementation for Work {
    async fn seed(&self, _ctx: RequestContext) -> Result<types::Seeded, ApiError> {
        let mut client = self.deps.db.get().await.map_err(internal)?;
        let tx = client.transaction().await.map_err(internal)?;
        let (user, task) = {
            let repositories = Repositories::new(&tx).map_err(internal)?.as_system();
            let repositories = repositories.untyped();
            let create = async |table: &str, values: Values| -> Result<String, ApiError> {
                let created = repositories
                    .table(table)
                    .map_err(internal)?
                    .create_one(&values)
                    .await
                    .map_err(internal)?;
                Ok(id_of(&created))
            };
            let user = create("Account", row(vec![("email", text("approver@example.test"))])).await?;
            let approver = create(
                "Role",
                row(vec![("name", text("approver")), ("permissions", texts(&["agents.approve"]))]),
            )
            .await?;
            create(
                "Role",
                row(vec![
                    ("name", text("worker-agent")),
                    ("permissions", texts(&["tasks.read"])),
                    ("reaches", texts(&[])),
                ]),
            )
            .await?;
            create(
                "UserRoleGrant",
                row(vec![
                    ("userId", text(&user)),
                    ("roleId", text(&approver)),
                    ("grantedAt", text("2026-01-01T00:00:00Z")),
                ]),
            )
            .await?;
            create(
                "NoticeRoute",
                row(vec![
                    ("name", text("Budget warnings go to the approvers")),
                    ("eventKind", text("agent.budgetWarning")),
                    ("recipients", approvers()),
                ]),
            )
            .await?;
            let total = create(
                "AgentBudget",
                row(vec![("name", text("every agent")), ("budgetLimit", Value::from(100_000))]),
            )
            .await?;
            create("AgentControl", row(vec![("agent", Value::Null), ("totalId", text(&total))])).await?;
            let task = create(
                "Task",
                row(vec![
                    ("title", text("Pump overhaul")),
                    ("details", text("replace the pump seal")),
                    ("state", text("open")),
                    ("createdAt", text("2026-11-26T12:00:00Z")),
                ]),
            )
            .await?;
            (user, task)
        };
        tx.commit().await.map_err(internal)?;
        Ok(types::Seeded { user, task })
    }

    async fn status(&self, ctx: RequestContext) -> Result<types::Done, ApiError> {
        Self::principal(&ctx)?;
        let client = self.deps.db.get().await.map_err(internal)?;
        let repositories = Repositories::new(&client).map_err(internal)?.as_system();
        let options = FindOptions {
            order_by: vec![OrderBy::desc("createdAt")],
            limit: Some(1),
            ..FindOptions::default()
        };
        let runs = repositories
            .untyped()
            .table("AgentRun")
            .map_err(internal)?
            .find_many(None, &options)
            .await
            .map_err(internal)?
            .rows;
        let ok = runs
            .first()
            .is_some_and(|run| run.get("status") == Some(&text("done")));
        Ok(types::Done { ok })
    }

    async fn review(&self, ctx: RequestContext, args: WorkReviewArgs) -> Result<types::Done, ApiError> {
        let principal = Self::principal(&ctx)?;
        let token_file = std::env::var("WIRED_TOKEN_FILE").map_err(internal)?;
        let agents: &Agents = &self.deps.agents;
        let command = format!("printf %s \"$AGENT_RUN_TOKEN\" > '{token_file}'");
        agents
            .define(
                &principal,
                row(vec![
                    ("name", text("reviewer")),
                    ("role", text("worker-agent")),
                    ("tools", texts(&["work_status"])),
                    ("command", texts(&["/bin/sh", "-c", &command])),
                ]),
            )
            .await
            .map_err(internal)?;
        let mut client = self.deps.db.get().await.map_err(internal)?;
        let tx = client.transaction().await.map_err(internal)?;
        let request = AgentRunRequest {
            agent: Some("reviewer".to_owned()),
            trigger: Some("principal".to_owned()),
            subject_table: Some("Task".to_owned()),
            subject_key: Some(args.task),
            requested_by: Some(PrincipalRef::user(principal.subject.clone())),
            ..AgentRunRequest::default()
        };
        agents
            .request(&tx, request, Duration::ZERO)
            .await
            .map_err(internal)?;
        tx.commit().await.map_err(internal)?;
        Ok(types::Done { ok: true })
    }

    async fn warn(&self, ctx: RequestContext) -> Result<types::Done, ApiError> {
        Self::principal(&ctx)?;
        let mut client = self.deps.db.get().await.map_err(internal)?;
        let tx = client.transaction().await.map_err(internal)?;
        {
            let repositories = Repositories::new(&tx).map_err(internal)?.as_system();
            let every_agent = row(vec![("agent", Value::Object(row(vec![("isNull", Value::Bool(true))])))]);
            let control = repositories
                .untyped()
                .table("AgentControl")
                .map_err(internal)?
                .find_one(Some(&every_agent), &FindOptions::default())
                .await
                .map_err(internal)?
                .ok_or_else(|| ApiError::not_found("no control of every agent"))?;
            self.deps
                .notifier
                .raise(&tx, RaiseInput::of("AgentControl", id_of(&control), "agent.budgetWarning"))
                .await
                .map_err(internal)?;
        }
        tx.commit().await.map_err(internal)?;
        Ok(types::Done { ok: true })
    }
}

/// A route's recipients: the principals who hold the approver role.
fn approvers() -> Value {
    Value::Array(vec![Value::Object(row(vec![("role", text("approver"))]))])
}

/// The caller the headers name, with the permissions they list.
pub struct HeaderCaller;

#[async_trait]
impl Authenticator for HeaderCaller {
    async fn authenticate(&self, request: &Parts) -> Result<Option<Principal>, ApiError> {
        let header = |name: &str| {
            request
                .headers
                .get(name)
                .and_then(|value| value.to_str().ok())
                .unwrap_or_default()
                .to_owned()
        };
        let user = header("x-user");
        if user.is_empty() {
            return Ok(None);
        }
        let permissions: Vec<String> = header("x-permissions")
            .split(',')
            .filter(|p| !p.is_empty())
            .map(str::to_owned)
            .collect();
        Ok(Some(Principal::new(user, permissions)))
    }
}

/// Builds the workers: the tasks' schedules' run event alone, which the
/// test raises none of.
pub async fn create_workers(deps: Deps) -> Result<Arc<dyn Workers>, BoxError> {
    Ok(Arc::new(WiredWorkers { _deps: Arc::new(deps) }))
}

/// Implements Workers.
pub struct WiredWorkers {
    _deps: Arc<Deps>,
}

#[async_trait]
impl Workers for WiredWorkers {
    async fn task_behaviors_schedules_run(
        &self,
        _event: types::TaskSchedulesRunEvent,
        _run: &EventRun<'_>,
    ) -> Result<(), BoxError> {
        Ok(())
    }
}

/// How the runtime parts are shaped: the dispatcher watches a run's
/// instance every second, at the agent routes WIRED_AGENT_ENDPOINT names.
pub async fn create_wiring(_deps: &WiringDeps) -> Result<Arc<dyn Wire>, BoxError> {
    Ok(Arc::new(Wiring))
}

/// Implements Wire.
pub struct Wiring;

impl Wire for Wiring {
    fn agent_endpoint(&self) -> String {
        std::env::var("WIRED_AGENT_ENDPOINT")
            .unwrap_or_else(|_| schemas_fixture_wired_api_api::wiring::default_agent_endpoint())
    }

    fn agents(&self, agents: Agents) -> Result<Agents, BoxError> {
        Ok(agents.with_watch_seconds(1))
    }
}
