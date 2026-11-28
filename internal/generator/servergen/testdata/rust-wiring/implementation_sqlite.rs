//! The implementation of fixture-wired-api in the generated wiring's
//! end-to-end test on a SQLite file (#725), testdata/rust-wiring/
//! implementation.rs over the file: its server, its workers and its
//! runtime parts are generated, and what is left to it is what only an
//! app knows. Its authenticator takes the caller from two headers,
//! `x-user` and `x-permissions`, which the generated server wraps in the
//! grants' authenticator. Its seed applies the build's SQLite DDL, which
//! WIRED_SQLITE_DDL names (the test's stand-in for the environment's
//! migrations), writes the rows an app writes, and writes a customer's
//! grant token to WIRED_GRANT_FILE. Its Wire watches a run's instance
//! every second and takes the agent routes' URL from WIRED_AGENT_ENDPOINT,
//! and its reviewer agent's instance writes its run token to
//! WIRED_TOKEN_FILE and exits.

use std::sync::Arc;
use std::time::Duration;

use async_trait::async_trait;
use http::request::Parts;
use schemas_fixture_wired_api_api::behaviors::EventRun;
use schemas_fixture_wired_api_api::database_types;
use schemas_fixture_wired_api_api::runtime::access::IssueGrant;
use schemas_fixture_wired_api_api::runtime::orm::agents::{AgentRunRequest, Agents, PrincipalRef};
use schemas_fixture_wired_api_api::runtime::orm::notify::RaiseInput;
use schemas_fixture_wired_api_api::runtime::orm::sqlite::failure;
use schemas_fixture_wired_api_api::runtime::orm::store::{Store, TxRepositories};
use schemas_fixture_wired_api_api::runtime::orm::{FindOptions, OrderBy, Value, Values};
use schemas_fixture_wired_api_api::types;
use schemas_fixture_wired_api_api::{
    BoxError, Deps, Handlers, Wire, WiringDeps, WorkImplementation, WorkReviewArgs, Workers,
};
use superschematic_http_runtime::{ApiError, Authenticator, Principal, PrincipalKind, RequestContext};

/// The number Ann verified, which the notifier texts and her replies come
/// from.
const PHONE_ANN: &str = "+15550100001";

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

/// Builds the implementation over the API's Deps, whose db is the file.
pub async fn create(deps: Deps) -> Result<Handlers, Box<dyn std::error::Error + Send + Sync>> {
    let store: Store = database_types::orm::sqlite::tables(Arc::clone(&deps.db)).into();
    let deps = Arc::new(deps);
    Ok(Handlers {
        work: Arc::new(Work {
            deps: Arc::clone(&deps),
            store,
        }),
        authenticator: Arc::new(HeaderCaller),
    })
}

/// Implements WorkImplementation.
pub struct Work {
    pub deps: Arc<Deps>,
    store: Store,
}

impl Work {
    fn principal(ctx: &RequestContext) -> Result<Principal, ApiError> {
        ctx.principal
            .clone()
            .ok_or_else(|| ApiError::unauthorized("Authentication required"))
    }
}

/// Creates a row of `table` as the system, answering its id.
async fn create_row(
    repositories: &TxRepositories<'_, '_>,
    table: &str,
    values: Values,
) -> Result<String, ApiError> {
    let created = repositories
        .table(table)
        .map_err(internal)?
        .create_one(&values)
        .await
        .map_err(internal)?;
    Ok(id_of(&created))
}

#[async_trait]
impl WorkImplementation for Work {
    async fn seed(&self, _ctx: RequestContext) -> Result<types::Seeded, ApiError> {
        let ddl = std::env::var("WIRED_SQLITE_DDL").map_err(internal)?;
        let ddl = std::fs::read_to_string(ddl).map_err(internal)?;
        self.deps
            .db
            .write_async(move |connection| {
                connection
                    .execute_batch(&ddl)
                    .map_err(|err| failure("the build's SQLite DDL", &err))
            })
            .await
            .map_err(internal)?;
        let definition = self.store.definition().map_err(internal)?;
        let mut client = self.store.client().await.map_err(internal)?;
        let tx = client.transaction().await.map_err(internal)?;
        let (user, task, token) = {
            let repositories = tx
                .db()
                .repositories(definition)
                .map_err(internal)?
                .as_system()
                .with_rules_of(&self.store);
            let create = async |table: &str, values: Values| create_row(&repositories, table, values).await;
            let user = create("Account", row(vec![("email", text("ann@example.test"))])).await?;
            let approver = create(
                "Role",
                row(vec![("name", text("approver")), ("permissions", texts(&["agents.approve", "agents"]))]),
            )
            .await?;
            for (name, reaches) in [("worker-agent", texts(&["TaskNote:task"])), ("customer", texts(&[]))] {
                create(
                    "Role",
                    row(vec![("name", text(name)), ("permissions", texts(&["tasks.read"])), ("reaches", reaches)]),
                )
                .await?;
            }
            create(
                "UserRoleGrant",
                row(vec![
                    ("userId", text(&user)),
                    ("roleId", text(&approver)),
                    ("grantedAt", text("2026-01-01T00:00:00Z")),
                ]),
            )
            .await?;
            // Ann's phone, which the notifier texts, and her every notice by
            // SMS: her own preference, over the schema's default for
            // everyone.
            create(
                "ChannelIdentity",
                row(vec![
                    ("kind", text("phone")),
                    ("address", text(PHONE_ANN)),
                    ("principalKind", text("user")),
                    ("principal", text(&user)),
                    ("trust", text("verified")),
                    ("verifiedAt", text("2026-01-01T00:00:00Z")),
                ]),
            )
            .await?;
            create(
                "NoticePreference",
                row(vec![
                    ("principal", text(&user)),
                    ("eventKind", text("*")),
                    ("urgency", text("*")),
                    ("channels", texts(&["sms"])),
                ]),
            )
            .await?;
            create(
                "ChannelTrust",
                row(vec![
                    ("channelKind", text("sms")),
                    ("level", text("standard")),
                    ("permissions", texts(&["agents.approve"])),
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
            // A task, which its write puts on the search index's queue, and
            // a customer whose grant reaches it.
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
            let customer = create("Customer", row(vec![("name", text("Cy"))])).await?;
            let issued = repositories
                .issue_grant(&IssueGrant {
                    holder: customer,
                    holder_kind: PrincipalKind::Party,
                    role: "customer".to_owned(),
                    target_type: "Task".to_owned(),
                    target_id: task.clone(),
                    starts_at: None,
                    ends_at: None,
                    ends_when: None,
                })
                .await
                .map_err(internal)?;
            (user, task, issued.token)
        };
        tx.commit().await.map_err(internal)?;
        let grant_file = std::env::var("WIRED_GRANT_FILE").map_err(internal)?;
        std::fs::write(grant_file, token).map_err(internal)?;
        Ok(types::Seeded { user, task })
    }

    async fn status(&self, ctx: RequestContext) -> Result<types::Done, ApiError> {
        Self::principal(&ctx)?;
        let definition = self.store.definition().map_err(internal)?;
        let client = self.store.client().await.map_err(internal)?;
        let repositories = client.db().repositories(definition).map_err(internal)?.as_system();
        let options = FindOptions {
            order_by: vec![OrderBy::desc("createdAt")],
            limit: Some(1),
            ..FindOptions::default()
        };
        let runs = repositories
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
        let mut client = self.store.client().await.map_err(internal)?;
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
            .request(tx.db(), request, Duration::ZERO)
            .await
            .map_err(internal)?;
        tx.commit().await.map_err(internal)?;
        Ok(types::Done { ok: true })
    }

    async fn warn(&self, ctx: RequestContext) -> Result<types::Done, ApiError> {
        Self::principal(&ctx)?;
        let definition = self.store.definition().map_err(internal)?;
        let mut client = self.store.client().await.map_err(internal)?;
        let tx = client.transaction().await.map_err(internal)?;
        {
            let repositories = tx.db().repositories(definition).map_err(internal)?.as_system();
            let every_agent = row(vec![("agent", Value::Object(row(vec![("isNull", Value::Bool(true))])))]);
            let control = repositories
                .table("AgentControl")
                .map_err(internal)?
                .find_one(Some(&every_agent), &FindOptions::default())
                .await
                .map_err(internal)?
                .ok_or_else(|| ApiError::not_found("no control of every agent"))?;
            self.deps
                .notifier
                .raise(tx.db(), RaiseInput::of("AgentControl", id_of(&control), "agent.budgetWarning"))
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
