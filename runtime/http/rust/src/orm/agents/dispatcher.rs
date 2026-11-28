//! The agent dispatcher over a database's repositories on either dialect
//! (D158, D183, D193, #618), the TypeScript runtime's `createAgents`: run requests,
//! runs, their tokens and principals, autonomy and approvals, budgets,
//! stop switches, transcripts and administration.

use std::collections::BTreeMap;
use std::fmt;
use std::sync::{Arc, OnceLock, Weak};
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use async_trait::async_trait;
use serde::{Deserialize, Serialize};
use serde_json::{json, Map, Value};

use super::super::inbound::{
    Inbound, InboundError, RunRequest, SendApproval, SendDecision, SendInput,
};
use super::super::notify::{Notifier, NotifierError, RaiseInput, ReplyAction, ReplyContext};
use super::super::parties::{party_principal, role_named, PartyRef};
#[cfg(feature = "postgres")]
use super::super::postgres::deadpool_postgres;
use super::super::rowfeatures::RowRef;
use super::super::search_tables::SearchOptions;
use super::super::store::{
    Db, PartQueue, PgClient, PoolClient, PoolTransaction, Store, StoreGrants, StoreRepositories,
    TxRepositories,
};
use super::super::{
    rfc3339_millis, AccessCaller, Caller, Definition, Error, FindOptions, OrderBy, Values,
    WriteOptions, ACCESS_ROLES,
};
use super::{
    best_policy, budget_crossing, default_autonomy, effective_autonomy, narrow_permissions,
    narrow_reaches, params_hash, permissions, redact_text, trust_of, Autonomy, OperationClass,
    PolicyRow, RunTrust, DEFAULT_TRANSCRIPT_RETENTION_DAYS,
};
use crate::access::{reaches_of, ActiveGrantsFilter, IssueGrant, GRANT_TOKEN_PREFIX};
use crate::auth::{covers, ActingFor, Authenticator, GrantReach, Principal, PrincipalKind};
use crate::civil::parse_rfc3339;
use crate::mcp::canonical_json;
use crate::orm::routes::acting;
use crate::providers::{
    ModelUsage, Providers, RuntimeInterface, SandboxCost, SandboxLimits, SandboxProvider,
    SandboxSpec, SandboxState, SecretValue,
};
use crate::queue::{QueueDefinition, QueueError};
use crate::uuid62::parse_uuid;
use crate::BoxError;

/// A refusal of an agents call, or a failure: its status, its code and its
/// message. The TypeScript runtime's `AgentsError`.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct AgentsError {
    pub status: u16,
    pub code: String,
    pub message: String,
}

impl AgentsError {
    /// A refusal of `status` and `code` that says `message`.
    pub fn new(status: u16, code: impl Into<String>, message: impl Into<String>) -> Self {
        Self {
            status,
            code: code.into(),
            message: message.into(),
        }
    }

    /// A failure of the database, a provider or a channel: 500 `failed`.
    pub fn failed(message: impl fmt::Display) -> Self {
        Self::new(500, "failed", message.to_string())
    }
}

impl fmt::Display for AgentsError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(&self.message)
    }
}

impl std::error::Error for AgentsError {}

impl From<Error> for AgentsError {
    fn from(err: Error) -> Self {
        match &err {
            Error::NotFound(_) => Self::new(404, "not_found", err.to_string()),
            Error::AccessRefused(_) => Self::new(403, "forbidden", err.to_string()),
            Error::Refused(refusal) => Self::new(
                refusal.status,
                refusal.code.clone(),
                refusal.message.clone(),
            ),
            Error::Invalid(_) => Self::new(400, "invalid_argument", err.to_string()),
            _ => Self::failed(err),
        }
    }
}

impl From<QueueError> for AgentsError {
    fn from(err: QueueError) -> Self {
        Self::failed(err)
    }
}

impl From<NotifierError> for AgentsError {
    fn from(err: NotifierError) -> Self {
        Self::failed(err)
    }
}

impl From<InboundError> for AgentsError {
    fn from(err: InboundError) -> Self {
        match err {
            InboundError::Refused(message) => Self::new(403, "refused", message),
            InboundError::Orm(err) => err.into(),
            other => Self::failed(other),
        }
    }
}

impl From<BoxError> for AgentsError {
    fn from(err: BoxError) -> Self {
        match err.downcast::<AgentsError>() {
            Ok(agents) => *agents,
            Err(err) => match err.downcast::<Error>() {
                Ok(orm) => (*orm).into(),
                Err(err) => Self::failed(err),
            },
        }
    }
}

#[cfg(feature = "postgres")]
impl From<deadpool_postgres::tokio_postgres::Error> for AgentsError {
    fn from(err: deadpool_postgres::tokio_postgres::Error) -> Self {
        Self::failed(crate::error::chain(&err))
    }
}

#[cfg(feature = "postgres")]
impl From<deadpool_postgres::PoolError> for AgentsError {
    fn from(err: deadpool_postgres::PoolError) -> Self {
        Self::failed(format!("the database is unreachable: {err}"))
    }
}

type Result<T, E = AgentsError> = std::result::Result<T, E>;

/// Who asked for a run, or whom it acts for.
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct PrincipalRef {
    /// `user`, `party`, `agent` or `route`.
    pub kind: String,
    pub subject: String,
    /// A party's table.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub table: Option<String>,
}

impl PrincipalRef {
    /// The user whose id is `subject`.
    pub fn user(subject: impl Into<String>) -> Self {
        Self {
            kind: "user".to_owned(),
            subject: subject.into(),
            table: None,
        }
    }
}

/// An ask's deadline: the row it was asked on, the message of its thread
/// and the question.
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct AskDeadline {
    pub table: String,
    pub key: String,
    pub message: String,
    pub question: String,
}

/// A message of the dispatcher's queue (D158): a run request, whose step is
/// `start` (or none), and the dispatcher's own `watch` and `followUp`
/// steps. Section 7's inbound message (D157) is `{ trigger: "inbound",
/// party, message, grant }`.
#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct AgentRunRequest {
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub step: Option<String>,
    /// The agent, by name or key; none for an inbound message, which the
    /// definition whose `inbound` is set takes.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub agent: Option<String>,
    /// `route`, `assigned`, `schedule`, `principal` or `inbound`.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub trigger: Option<String>,
    /// The row the run is about, which its timeline shows.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub subject_table: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub subject_key: Option<String>,
    /// More rows of the run's scope.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub targets: Option<Vec<RowRef>>,
    /// A route's event.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub event: Option<Value>,
    /// What the run is told: `{ task }`, the first message of its turns.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub input: Option<Values>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub requested_by: Option<PrincipalRef>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub acting_for: Option<PrincipalRef>,
    /// An inbound message's party, `{ table, key }` (D157).
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub party: Option<RowRef>,
    /// An inbound message: its row's key as section 7's queue carries it,
    /// its row as `{ table, key }`, or its text as `{ body }`.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub message: Option<Value>,
    /// The party's active grant, whose target is the run's scope.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub grant: Option<String>,
    /// The run a watch, a retry, a follow-up or an approved start is of,
    /// its key in base62 as the repositories write it, whatever form the
    /// queue's column read in.
    #[serde(
        default,
        skip_serializing_if = "Option::is_none",
        deserialize_with = "wire_id"
    )]
    pub run: Option<String>,
    /// An ask's deadline.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub ask: Option<AskDeadline>,
}

/// A key read from a queue's `UUID` column, hyphenated there, in the form
/// the repositories write it: base62.
fn wire_id<'de, D: serde::Deserializer<'de>>(deserializer: D) -> Result<Option<String>, D::Error> {
    let text = Option::<String>::deserialize(deserializer)?;
    Ok(text.map(|id| super::super::notify::same_id(&id)))
}

impl From<RunRequest> for AgentRunRequest {
    fn from(request: RunRequest) -> Self {
        Self {
            trigger: Some(request.trigger),
            party: Some(RowRef::new(request.party.table, request.party.key)),
            message: Some(Value::String(request.message)),
            grant: Some(request.grant),
            ..Self::default()
        }
    }
}

/// One entry of a run's transcript, redacted as it is written.
#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct TranscriptEntry {
    #[serde(default)]
    pub at: String,
    /// `task`, `message`, `completion`, `call`, `credential` or `note`.
    pub kind: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub role: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub content: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub tool_call_id: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub tool_calls: Option<Vec<Value>>,
    /// A completion's provider state, which the turns after it pass back.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub provider_state: Option<Value>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub tool: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub args: Option<Value>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub outcome: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub untrusted: Option<bool>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub r#ref: Option<String>,
}

/// Where a run's transcript is kept once it ends: a bucket's objects.
#[async_trait]
pub trait TranscriptStore: Send + Sync {
    async fn put(&self, name: &str, body: String) -> Result<(), BoxError>;
    async fn get(&self, name: &str) -> Result<Option<String>, BoxError>;
    async fn delete(&self, name: &str) -> Result<(), BoxError>;
}

/// What a tool of the app is given: the repositories of a transaction
/// acting as the run's principal, whose reads and writes keep to its reach
/// and whose behaviors' commands run with its permissions, the principal,
/// the run's row and the arguments.
pub struct AgentToolContext<'a, 't> {
    pub repositories: &'a TxRepositories<'a, 't>,
    pub principal: &'a Principal,
    pub run: &'a Values,
    pub args: &'a Map<String, Value>,
}

/// What a tool of the app runs.
#[async_trait]
pub trait AgentToolRun: Send + Sync {
    /// Runs the tool; an [`AgentsError`] answers its status, an
    /// [`Error::NotFound`] 404 and [`Error::AccessRefused`] 403.
    async fn run(&self, ctx: &AgentToolContext<'_, '_>) -> Result<Value, BoxError>;
}

/// A tool of the app an agent may call through the run's MCP endpoint.
#[derive(Clone)]
pub struct AgentTool {
    pub description: String,
    pub input_schema: Value,
    /// What the tool is to the autonomy policy.
    pub class: OperationClass,
    /// The autonomy when no policy row names it; the class's default when
    /// none.
    pub mode: Option<Autonomy>,
    /// A permission the run's principal holds for the tool to be offered.
    pub permission: Option<String>,
    /// Its results carry outside content, which the run marks untrusted.
    pub untrusted: bool,
    pub run: Arc<dyn AgentToolRun>,
}

impl AgentTool {
    /// A tool of `class` described by `description`, taking `input_schema`.
    pub fn new(
        description: impl Into<String>,
        input_schema: Value,
        class: OperationClass,
        run: Arc<dyn AgentToolRun>,
    ) -> Self {
        Self {
            description: description.into(),
            input_schema,
            class,
            mode: None,
            permission: None,
            untrusted: false,
            run,
        }
    }
}

/// A tool the sandbox runs itself (a browser, a fetch with a credential):
/// offered to the model, run inside the instance.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct AgentSandboxTool {
    pub name: String,
    pub description: String,
    pub input_schema: Value,
    #[serde(default)]
    pub untrusted: bool,
}

/// How a principal is asked in a row's thread; the row's Comments unless
/// the app gives one.
#[async_trait]
pub trait AskThread: Send + Sync {
    /// Posts `question` on `row` as the run, mentioning `principal`;
    /// answers the message's id.
    async fn post(
        &self,
        repositories: &TxRepositories<'_, '_>,
        row: &RowRef,
        question: &str,
        principal: Option<&str>,
    ) -> Result<String, BoxError>;
    /// Whether someone other than the run answered the message.
    async fn answered(
        &self,
        repositories: &TxRepositories<'_, '_>,
        row: &RowRef,
        message: &str,
        run: &str,
    ) -> Result<bool, BoxError>;
}

/// Makes the follow-up row of an ask nobody answered by its deadline.
#[async_trait]
pub trait FollowUp: Send + Sync {
    async fn follow_up(
        &self,
        repositories: &TxRepositories<'_, '_>,
        run: &Values,
        row: &RowRef,
        question: &str,
    ) -> Result<Option<RowRef>, BoxError>;
}

/// Returns a row assigned to an agent to its queue when the agent is
/// paused and its run cancelled.
#[async_trait]
pub trait OnReturn: Send + Sync {
    async fn on_return(
        &self,
        repositories: &TxRepositories<'_, '_>,
        run: &Values,
        row: &RowRef,
    ) -> Result<(), BoxError>;
}

/// Plain settings and secrets of the sandbox beside the run's own.
#[derive(Clone, Debug, Default)]
pub struct SandboxExtra {
    pub env: BTreeMap<String, String>,
    pub secrets: BTreeMap<String, SecretValue>,
}

/// What a run's sandbox is given beside the run's own, of its run and its
/// definition.
pub type SandboxSpecOf = Arc<dyn Fn(&Values, &Values) -> SandboxExtra + Send + Sync>;

/// The clock.
pub type Clock = Arc<dyn Fn() -> SystemTime + Send + Sync>;

/// What [`Agents::gate`] answers: run the call, or the refusal the call
/// answers instead.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum GateOutcome {
    Run {
        mode: Autonomy,
        approval: Option<String>,
    },
    Refused {
        status: u16,
        code: String,
        detail: String,
        approval: Option<String>,
    },
}

impl GateOutcome {
    /// Whether the call runs.
    pub fn runs(&self) -> bool {
        matches!(self, GateOutcome::Run { .. })
    }

    /// The refusal as a problem document.
    pub fn problem(&self) -> Option<Value> {
        let GateOutcome::Refused {
            status,
            code,
            detail,
            approval,
        } = self
        else {
            return None;
        };
        let mut problem = json!({ "type": "about:blank", "title": code, "status": status, "detail": detail, "code": code });
        if let Some(approval) = approval {
            problem["approval"] = json!(approval);
        }
        Some(problem)
    }
}

#[derive(Clone, Debug)]
pub(super) struct Parts {
    pub definitions: String,
    pub runs: String,
    pub policies: String,
    pub approvals: String,
    pub controls: String,
}

pub(super) struct Inner {
    pub tables: Store,
    pub queue: PartQueue<AgentRunRequest>,
    pub definition: &'static Definition,
    pub requests: QueueDefinition,
    pub endpoint: String,
    pub base: String,
    pub providers: Arc<Providers>,
    pub sandbox_slot: Option<String>,
    pub model_slot: Option<String>,
    pub notifier: OnceLock<Notifier>,
    pub inbound: OnceLock<Inbound>,
    pub transcripts: Option<Arc<dyn TranscriptStore>>,
    pub tools: BTreeMap<String, AgentTool>,
    pub sandbox_tools: Vec<AgentSandboxTool>,
    pub search: Option<SearchOptions>,
    pub thread: Option<Arc<dyn AskThread>>,
    pub follow_up: Option<Arc<dyn FollowUp>>,
    pub on_return: Option<Arc<dyn OnReturn>>,
    pub sandbox_spec: Option<SandboxSpecOf>,
    pub dispatcher: Caller,
    pub cost_of: Arc<dyn Fn(&ModelUsage) -> i64 + Send + Sync>,
    pub sandbox_cost_of: Arc<dyn Fn(&SandboxCost) -> i64 + Send + Sync>,
    pub meter: String,
    pub watch_seconds: u64,
    pub ask_seconds: u64,
    pub approval_ttl_seconds: u64,
    pub retention_days: u64,
    pub redact: Vec<String>,
    pub now: Option<Clock>,
    pub parts: Parts,
    /// The controls' relation to the table of every agent's budget: its
    /// member and target.
    pub total: Option<(String, String)>,
    pub messages: Option<String>,
}

/// The agent orchestration (D158). Build one with [`Agents::new`] and its
/// `with_` methods, share it (it is cheap to clone), give the notifier and
/// the inbound path once they are built ([`Agents::set_notifier`],
/// [`Agents::set_inbound`]), and run its request queue's worker with
/// [`Agents::worker_queue`] and [`Agents::handle`].
#[derive(Clone)]
pub struct Agents {
    pub(super) inner: Arc<Inner>,
}

impl fmt::Debug for Agents {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("Agents")
            .field("parts", &self.inner.parts)
            .field("requests", &self.inner.requests.name)
            .field("endpoint", &self.inner.endpoint)
            .finish_non_exhaustive()
    }
}

pub(super) fn obj(value: Value) -> Values {
    match value {
        Value::Object(map) => map,
        _ => Values::new(),
    }
}

pub(super) fn eq(value: impl Into<Value>) -> Value {
    json!({ "eq": value.into() })
}

pub(super) fn string(row: &Values, member: &str) -> String {
    match row.get(member) {
        Some(Value::String(text)) => text.clone(),
        Some(Value::Null) | None => String::new(),
        Some(other) => other.to_string(),
    }
}

pub(super) fn opt(row: &Values, member: &str) -> Option<String> {
    Some(string(row, member)).filter(|text| !text.is_empty())
}

pub(super) fn number(row: &Values, member: &str) -> i64 {
    as_number(row.get(member))
}

pub(super) fn as_number(value: Option<&Value>) -> i64 {
    match value {
        Some(Value::Number(n)) => n
            .as_i64()
            .or_else(|| n.as_f64().map(|f| f as i64))
            .unwrap_or(0),
        Some(Value::String(s)) => s.parse().unwrap_or(0),
        _ => 0,
    }
}

pub(super) fn at_text(time: SystemTime) -> Value {
    Value::String(rfc3339_millis(time))
}

pub(super) fn many(limit: u32) -> FindOptions {
    FindOptions {
        limit: Some(limit),
        skip_count: true,
        ..FindOptions::default()
    }
}

fn key(id: &str) -> Value {
    Value::String(id.to_owned())
}

fn ended(status: &str) -> bool {
    matches!(status, "done" | "failed" | "cancelled")
}

fn live(status: &str) -> bool {
    matches!(status, "running" | "waiting")
}

fn millis_of(value: Option<&Value>) -> Option<i64> {
    match value? {
        Value::Number(n) => n.as_i64(),
        Value::String(s) => parse_rfc3339(s)
            .and_then(|t| t.duration_since(UNIX_EPOCH).ok())
            .map(|d| i64::try_from(d.as_millis()).unwrap_or(i64::MAX))
            .or_else(|| s.parse().ok()),
        _ => None,
    }
}

fn millis(time: SystemTime) -> i64 {
    time.duration_since(UNIX_EPOCH)
        .map_or(0, |d| i64::try_from(d.as_millis()).unwrap_or(i64::MAX))
}

pub(super) fn targets_of(run: &Values) -> Vec<RowRef> {
    run.get("targets")
        .and_then(Value::as_array)
        .map(|items| {
            items
                .iter()
                .filter_map(|item| {
                    Some(RowRef::new(
                        item.get("table")?.as_str()?,
                        match item.get("key")? {
                            Value::String(s) => s.clone(),
                            other => other.to_string(),
                        },
                    ))
                })
                .collect()
        })
        .unwrap_or_default()
}

impl Agents {
    /// The orchestration over `tables`, the database's repositories, with
    /// `requests`, its `@agents("requests")` queue's descriptor, and
    /// `endpoint`, the URL the sandbox reaches the agent routes at. It
    /// refuses a database without every part, and an endpoint that is no
    /// URL.
    pub fn new(
        tables: impl Into<Store>,
        requests: QueueDefinition,
        endpoint: &str,
    ) -> Result<Self> {
        let tables: Store = tables.into();
        let definition = tables.definition()?;
        let queue = PartQueue::of(&tables, requests.clone())?;
        let part = |name: &str| {
            definition
                .tables
                .iter()
                .find(|table| table.agents.as_deref() == Some(name))
                .map(|table| table.name.clone())
                .ok_or_else(|| {
                    AgentsError::failed(format!(
                        "the agents: the database has no @agents(\"{name}\") table"
                    ))
                })
        };
        let parts = Parts {
            definitions: part("definitions")?,
            runs: part("runs")?,
            policies: part("policies")?,
            approvals: part("approvals")?,
            controls: part("controls")?,
        };
        let controls = definition.table(&parts.controls)?;
        let total = controls
            .relations
            .iter()
            .find(|relation| relation.target != parts.controls)
            .map(|relation| (format!("{}Id", relation.field), relation.target.clone()));
        let messages = definition
            .tables
            .iter()
            .find(|table| table.conversations.as_deref() == Some("messages"))
            .map(|table| table.name.clone());
        let endpoint = endpoint.trim_end_matches('/').to_owned();
        let base = endpoint
            .split_once("://")
            .map(|(_, rest)| rest)
            .and_then(|rest| rest.find('/').map(|at| rest[at..].to_owned()))
            .ok_or_else(|| {
                AgentsError::failed(format!(
                    "the agents: the endpoint {endpoint} is no URL with a path"
                ))
            })?;
        Ok(Self {
            inner: Arc::new(Inner {
                tables,
                queue,
                definition,
                requests,
                endpoint,
                base,
                providers: Arc::new(Providers::none()),
                sandbox_slot: None,
                model_slot: None,
                notifier: OnceLock::new(),
                inbound: OnceLock::new(),
                transcripts: None,
                tools: BTreeMap::new(),
                sandbox_tools: Vec::new(),
                search: None,
                thread: None,
                follow_up: None,
                on_return: None,
                sandbox_spec: None,
                dispatcher: Caller {
                    subject: "agent-dispatcher".to_owned(),
                    ..Caller::default()
                },
                cost_of: Arc::new(|usage: &ModelUsage| {
                    i64::try_from(usage.input_tokens + usage.output_tokens).unwrap_or(i64::MAX)
                }),
                sandbox_cost_of: Arc::new(|cost: &SandboxCost| cost.wall_seconds.ceil() as i64),
                meter: "cost".to_owned(),
                watch_seconds: 5,
                ask_seconds: 86_400,
                approval_ttl_seconds: 86_400,
                retention_days: DEFAULT_TRANSCRIPT_RETENTION_DAYS,
                redact: Vec::new(),
                now: None,
                parts,
                total,
                messages,
            }),
        })
    }

    fn edit(mut self, change: impl FnOnce(&mut Inner)) -> Self {
        if let Some(inner) = Arc::get_mut(&mut self.inner) {
            change(inner);
        }
        self
    }

    /// The orchestration with the environment's providers (D159): the
    /// sandbox and model slots the definitions name, or the defaults. Each
    /// `with_` method is for an orchestration not yet cloned.
    #[must_use]
    pub fn with_providers(self, providers: Arc<Providers>) -> Self {
        self.edit(|inner| inner.providers = providers)
    }

    /// The sandbox slot of a definition that names none; the first sandbox
    /// slot configured unless set.
    #[must_use]
    pub fn with_sandbox_slot(self, slot: impl Into<String>) -> Self {
        let slot = slot.into();
        self.edit(|inner| inner.sandbox_slot = Some(slot))
    }

    /// The model slot of a definition that names none; the first model
    /// slot configured unless set.
    #[must_use]
    pub fn with_model_slot(self, slot: impl Into<String>) -> Self {
        let slot = slot.into();
        self.edit(|inner| inner.model_slot = Some(slot))
    }

    /// Where a run's transcript goes when it ends; on the run row alone
    /// without one.
    #[must_use]
    pub fn with_transcripts(self, transcripts: Arc<dyn TranscriptStore>) -> Self {
        self.edit(|inner| inner.transcripts = Some(transcripts))
    }

    /// The app's tool `name`.
    #[must_use]
    pub fn with_tool(self, name: impl Into<String>, tool: AgentTool) -> Self {
        let name = name.into();
        self.edit(|inner| {
            inner.tools.insert(name, tool);
        })
    }

    /// A tool the sandbox runs itself.
    #[must_use]
    pub fn with_sandbox_tool(self, tool: AgentSandboxTool) -> Self {
        self.edit(|inner| inner.sandbox_tools.push(tool))
    }

    /// Search across tables (D155) as the tool `search`, run as the run's
    /// principal.
    #[must_use]
    pub fn with_search(self, options: SearchOptions) -> Self {
        self.edit(|inner| inner.search = Some(options))
    }

    /// How a principal is asked in a row's thread; the row's Comments
    /// unless set.
    #[must_use]
    pub fn with_thread(self, thread: Arc<dyn AskThread>) -> Self {
        self.edit(|inner| inner.thread = Some(thread))
    }

    /// What makes the follow-up row of an ask nobody answered.
    #[must_use]
    pub fn with_follow_up(self, follow_up: Arc<dyn FollowUp>) -> Self {
        self.edit(|inner| inner.follow_up = Some(follow_up))
    }

    /// What returns an assigned row to its queue when its run is cancelled.
    #[must_use]
    pub fn with_on_return(self, on_return: Arc<dyn OnReturn>) -> Self {
        self.edit(|inner| inner.on_return = Some(on_return))
    }

    /// Plain settings and secrets of the sandbox beside the run's own: a
    /// credential provider's slot settings, say.
    #[must_use]
    pub fn with_sandbox_spec(self, spec: SandboxSpecOf) -> Self {
        self.edit(|inner| inner.sandbox_spec = Some(spec))
    }

    /// The behaviors' caller the dispatcher writes runs and controls as;
    /// `agent-dispatcher`, holding no permission, unless set.
    #[must_use]
    pub fn with_dispatcher(self, caller: Caller) -> Self {
        self.edit(|inner| inner.dispatcher = caller)
    }

    /// The units a turn's usage costs; its tokens unless set.
    #[must_use]
    pub fn with_cost_of(self, cost_of: Arc<dyn Fn(&ModelUsage) -> i64 + Send + Sync>) -> Self {
        self.edit(|inner| inner.cost_of = cost_of)
    }

    /// The units an instance costs; its wall seconds unless set.
    #[must_use]
    pub fn with_sandbox_cost_of(
        self,
        cost_of: Arc<dyn Fn(&SandboxCost) -> i64 + Send + Sync>,
    ) -> Self {
        self.edit(|inner| inner.sandbox_cost_of = cost_of)
    }

    /// The Budget meter of the controls; `cost` unless set.
    #[must_use]
    pub fn with_meter(self, meter: impl Into<String>) -> Self {
        let meter = meter.into();
        self.edit(|inner| inner.meter = meter)
    }

    /// How often a running instance is watched, in seconds; 5 unless set.
    #[must_use]
    pub fn with_watch_seconds(self, seconds: u64) -> Self {
        self.edit(|inner| inner.watch_seconds = seconds)
    }

    /// How long a run waits for an answer to its question; a day unless
    /// set.
    #[must_use]
    pub fn with_ask_seconds(self, seconds: u64) -> Self {
        self.edit(|inner| inner.ask_seconds = seconds)
    }

    /// How long a pending or approved approval holds; a day unless set.
    #[must_use]
    pub fn with_approval_ttl_seconds(self, seconds: u64) -> Self {
        self.edit(|inner| inner.approval_ttl_seconds = seconds)
    }

    /// How long a transcript is kept after its run ends, in days; 30 unless
    /// set.
    #[must_use]
    pub fn with_transcript_retention_days(self, days: u64) -> Self {
        self.edit(|inner| inner.retention_days = days)
    }

    /// More text a transcript entry is redacted of.
    #[must_use]
    pub fn with_redacted(self, text: impl Into<String>) -> Self {
        let text = text.into();
        self.edit(|inner| inner.redact.push(text))
    }

    /// The clock; the system's unless set, for a test.
    #[must_use]
    pub fn with_clock(self, now: Clock) -> Self {
        self.edit(|inner| inner.now = Some(now))
    }

    /// Gives the orchestration the notifier an approval's request, a budget
    /// warning and a tell are raised through, once: the notifier is built
    /// with the orchestration's reply actions, so it comes after it. A
    /// second call changes nothing, and answers false.
    pub fn set_notifier(&self, notifier: Notifier) -> bool {
        self.inner.notifier.set(notifier).is_ok()
    }

    /// Gives the orchestration the inbound path (D157), once: an inbound
    /// message's run reads its message through it, and the `message_party`
    /// tool sends through it, held to its permission, identity, rate limit
    /// and the send's approval policy, [`Agents::send_approval`].
    pub fn set_inbound(&self, inbound: Inbound) -> bool {
        self.inner.inbound.set(inbound).is_ok()
    }

    /// The `@agents("requests")` queue's descriptor.
    pub fn requests(&self) -> &QueueDefinition {
        &self.inner.requests
    }

    /// The URL the sandbox reaches the agent routes at.
    pub fn endpoint(&self) -> &str {
        &self.inner.endpoint
    }

    /// The path the agent routes are served under: the endpoint's.
    pub fn base_path(&self) -> &str {
        &self.inner.base
    }

    pub(super) fn now(&self) -> SystemTime {
        self.inner
            .now
            .as_ref()
            .map_or_else(SystemTime::now, |now| now())
    }

    pub(super) fn store(&self) -> &Store {
        &self.inner.tables
    }

    pub(super) fn tables(&self) -> &Store {
        &self.inner.tables
    }

    pub(super) fn definition(&self) -> &'static Definition {
        self.inner.definition
    }

    pub(super) fn parts(&self) -> &Parts {
        &self.inner.parts
    }

    pub(super) fn inbound(&self) -> Option<&Inbound> {
        self.inner.inbound.get()
    }

    // ---- the repositories ----

    /// The repositories over `client` as the dispatcher: the system, whose
    /// behaviors' commands run as the dispatcher's caller.
    pub(super) fn sys<'c, C: PgClient>(
        &'c self,
        client: Db<'c, C>,
    ) -> Result<StoreRepositories<'c, C>> {
        let mut repositories = client
            .repositories(self.inner.definition)?
            .as_system()
            .with_caller(self.inner.dispatcher.clone());
        if let Some(keys) = self.inner.tables.keys() {
            repositories = repositories.with_keys(keys.as_ref());
        }
        Ok(repositories)
    }

    /// The repositories of `tx` as the system, whose behaviors' commands
    /// run as `caller` (the dispatcher's unless given), through the core.
    pub(super) fn sys_tx<'c, 't>(
        &'c self,
        tx: Db<'c, PoolTransaction<'t>>,
        caller: Option<Caller>,
    ) -> Result<StoreRepositories<'c, PoolTransaction<'t>>> {
        let mut repositories = tx
            .repositories(self.inner.definition)?
            .as_system()
            .with_caller(caller.unwrap_or_else(|| self.inner.dispatcher.clone()));
        if let Some(keys) = self.inner.tables.keys() {
            repositories = repositories.with_keys(keys.as_ref());
        }
        repositories = repositories.with_rules_of(&self.inner.tables);
        Ok(repositories)
    }

    /// The repositories of `tx` acting as `principal`: its reads and writes
    /// keep to its reach, and its behaviors' commands run with its
    /// permissions.
    pub(super) fn as_principal<'c, 't>(
        &'c self,
        tx: Db<'c, PoolTransaction<'t>>,
        principal: &Principal,
    ) -> Result<StoreRepositories<'c, PoolTransaction<'t>>> {
        let mut repositories = tx
            .repositories(self.inner.definition)?
            .acting(acting(Some(principal)))
            .with_access(AccessCaller::of(Some(principal)))
            .with_caller(Caller::of(Some(principal)));
        if let Some(keys) = self.inner.tables.keys() {
            repositories = repositories.with_keys(keys.as_ref());
        }
        repositories = repositories.with_rules_of(&self.inner.tables);
        Ok(repositories)
    }

    pub(super) async fn find_one(&self, table: &str, filter: Value) -> Result<Option<Values>> {
        let client = self.store().client().await?;
        Ok(self
            .sys(client.db())?
            .table(table)?
            .find_one(Some(&obj(filter)), &FindOptions::default())
            .await?)
    }

    pub(super) async fn find_many(
        &self,
        table: &str,
        filter: Value,
        options: &FindOptions,
    ) -> Result<Vec<Values>> {
        let client = self.store().client().await?;
        Ok(self
            .sys(client.db())?
            .table(table)?
            .find_many(Some(&obj(filter)), options)
            .await?
            .rows)
    }

    pub(super) async fn create(&self, table: &str, values: Value) -> Result<Values> {
        let mut client = self.store().client().await?;
        let tx = client.transaction().await?;
        let row = self
            .sys_tx(tx.db(), None)?
            .table(table)?
            .create_one(&obj(values))
            .await?;
        tx.commit().await?;
        Ok(row)
    }

    pub(super) async fn update(&self, table: &str, id: &str, values: Value) -> Result<Values> {
        self.update_as(table, id, values, None).await
    }

    async fn update_as(
        &self,
        table: &str,
        id: &str,
        values: Value,
        caller: Option<Caller>,
    ) -> Result<Values> {
        let mut client = self.store().client().await?;
        let tx = client.transaction().await?;
        let row = self
            .sys_tx(tx.db(), caller)?
            .table(table)?
            .update_one(&key(id), &obj(values), &WriteOptions::default())
            .await?;
        tx.commit().await?;
        Ok(row)
    }

    /// Runs `operation` of the behavior under `member` on the row `id` of
    /// `table` as the dispatcher, in a transaction of its own.
    async fn operation(
        &self,
        table: &str,
        member: &str,
        operation: &str,
        id: &str,
        params: Value,
        options: WriteOptions,
    ) -> Result<Value> {
        let mut client = self.store().client().await?;
        let tx = client.transaction().await?;
        let answer = self
            .sys_tx(tx.db(), None)?
            .table(table)?
            .operation(member, operation, &key(id), &params, &options)
            .await?;
        tx.commit().await?;
        Ok(answer)
    }

    fn owns(&self, table: &str, column: &str) -> bool {
        self.inner
            .definition
            .table(table)
            .ok()
            .and_then(|t| t.behaviors.as_ref())
            .is_some_and(|b| b.owned.iter().any(|c| c == column))
    }

    fn composes(&self, table: &str) -> bool {
        self.inner
            .definition
            .table(table)
            .ok()
            .is_some_and(|t| t.behaviors.is_some())
    }

    pub(super) async fn run_row(&self, id: &str) -> Result<Option<Values>> {
        if id.is_empty() {
            return Ok(None);
        }
        self.find_one(&self.inner.parts.runs, json!({ "id": { "eq": id } }))
            .await
    }

    pub(super) async fn definition_row(&self, id: &str) -> Result<Option<Values>> {
        if id.is_empty() {
            return Ok(None);
        }
        self.find_one(&self.inner.parts.definitions, json!({ "id": { "eq": id } }))
            .await
    }

    // ---- redaction and the transcript ----

    pub(super) fn redact_text(&self, text: &str) -> String {
        redact_text(text, &self.inner.redact)
    }

    pub(super) fn redact(&self, value: &Value) -> Value {
        match value {
            Value::String(text) => Value::String(self.redact_text(text)),
            Value::Array(_) | Value::Object(_) => {
                let text = serde_json::to_string(value).unwrap_or_default();
                serde_json::from_str(&self.redact_text(&text)).unwrap_or(Value::Null)
            }
            other => other.clone(),
        }
    }

    /// Appends `entries` to the run's transcript, each redacted as it is
    /// written but a completion's `providerState`, which the turns after it
    /// pass back as it was.
    pub(super) async fn append(&self, run: &str, entries: Vec<Value>) -> Result<()> {
        let mut client = self.store().client().await?;
        let tx = client.transaction().await?;
        {
            let repositories = self.sys_tx(tx.db(), None)?;
            let runs = repositories.table(&self.inner.parts.runs)?;
            let mut by_id = Values::new();
            by_id.insert("id".to_owned(), eq(run));
            let Some(row) = runs.find_one(Some(&by_id), &FindOptions::default()).await? else {
                return Ok(());
            };
            let mut next: Vec<Value> = row
                .get("transcript")
                .and_then(Value::as_array)
                .cloned()
                .unwrap_or_default();
            let at = rfc3339_millis(self.now());
            for entry in entries {
                let mut entry = obj(entry);
                let state = entry.remove("providerState");
                let mut written = Values::new();
                written.insert("at".to_owned(), json!(at));
                written.extend(entry);
                let mut written = obj(self.redact(&Value::Object(written)));
                if let Some(state) = state.filter(|s| !s.is_null()) {
                    written.insert("providerState".to_owned(), state);
                }
                next.push(Value::Object(written));
            }
            runs.update_one(
                &key(run),
                &obj(json!({ "transcript": next })),
                &WriteOptions::default(),
            )
            .await?;
        }
        tx.commit().await?;
        Ok(())
    }

    // ---- definitions, controls and budgets ----

    pub(super) async fn find_definition(&self, agent: &str) -> Result<Option<Values>> {
        let defs = &self.inner.parts.definitions;
        if let Some(found) = self
            .find_one(defs, json!({ "name": { "eq": agent } }))
            .await?
        {
            return Ok(Some(found));
        }
        if parse_uuid(agent).is_none() {
            return Ok(None);
        }
        Ok(self
            .find_one(defs, json!({ "id": { "eq": agent } }))
            .await
            .ok()
            .flatten())
    }

    /// The control row of `agent`, or of every agent for none, made when
    /// there is none: an agent's spends within every agent's budget.
    pub(super) async fn control_of(&self, agent: Option<&str>) -> Result<Values> {
        let controls = &self.inner.parts.controls;
        let filter = match agent {
            None => json!({ "agent": { "isNull": true } }),
            Some(agent) => json!({ "agent": { "eq": agent } }),
        };
        if let Some(found) = self.find_one(controls, filter).await? {
            return Ok(found);
        }
        let mut values = obj(json!({ "agent": agent }));
        if let (Some(agent), Some((column, _))) = (agent, &self.inner.total) {
            let _ = agent;
            let every = Box::pin(self.control_of(None)).await?;
            if let Some(total) = every.get(column).filter(|v| !v.is_null()) {
                values.insert(column.clone(), total.clone());
            }
        }
        self.create(controls, Value::Object(values)).await
    }

    /// Whether a Budget's meter on the row `id` of `table` fits one more
    /// unit; true for a table with no Budget.
    async fn fits(&self, table: &str, id: Option<&str>) -> bool {
        let Some(id) = id.filter(|id| !id.is_empty()) else {
            return true;
        };
        if !self.composes(table) {
            return true;
        }
        let params = json!({ "meter": self.inner.meter, "amount": 1 });
        match self
            .operation(
                table,
                "budget",
                "checkReserve",
                id,
                params,
                WriteOptions::default(),
            )
            .await
        {
            Ok(answer) => answer.get("fits").and_then(Value::as_bool).unwrap_or(false),
            Err(err) => {
                tracing::warn!(table = %table, detail = %err, "a budget's check did not answer");
                true
            }
        }
    }

    /// Where the agent stands: paused (itself or every agent), and whether
    /// it or every agent is over its cap.
    pub(super) async fn standing(&self, agent: &str) -> Result<Standing> {
        let every = self.control_of(None).await?;
        let own = self.control_of(Some(agent)).await?;
        let total_capped = match &self.inner.total {
            Some((column, target)) => {
                let from = if own.get(column).is_some_and(|v| !v.is_null()) {
                    &own
                } else {
                    &every
                };
                !self.fits(target, opt(from, column).as_deref()).await
            }
            None => false,
        };
        // The agent's own meter answers for every scope it spends within:
        // past every agent's cap it fits nowhere, which is not its own cap.
        let agent_capped = !total_capped
            && !self
                .fits(&self.inner.parts.controls, Some(&string(&own, "id")))
                .await;
        let paused_of = |row: &Values| {
            row.get("paused")
                .and_then(Value::as_bool)
                .unwrap_or(false)
                .then(|| opt(row, "pausedReason").unwrap_or_else(|| "stopped".to_owned()))
        };
        Ok(Standing {
            paused: paused_of(&every).or_else(|| paused_of(&own)),
            agent_capped,
            total_capped,
        })
    }

    pub(super) async fn raise_in<C: PgClient>(
        &self,
        client: Db<'_, C>,
        table: &str,
        key: &str,
        kind: &str,
        payload: Value,
        urgency: Option<&str>,
    ) -> Result<()> {
        let Some(notifier) = self.inner.notifier.get() else {
            return Ok(());
        };
        let declared = self
            .inner
            .definition
            .table(table)
            .is_ok_and(|t| t.events.iter().any(|e| e.kind == kind));
        if !declared {
            return Ok(());
        }
        let mut input = RaiseInput::of(table, key, kind);
        input.payload = obj(payload);
        input.urgency = urgency.map(str::to_owned);
        notifier.raise_in(client, input, false).await?;
        Ok(())
    }

    async fn raise(&self, table: &str, key: &str, kind: &str, payload: Value) -> Result<()> {
        let mut client = self.store().client().await?;
        let tx = client.transaction().await?;
        self.raise_in(tx.db(), table, key, kind, payload, None)
            .await?;
        tx.commit().await?;
        Ok(())
    }

    /// Records usage on the agent's control row: warns at its threshold
    /// and pauses the agent at its cap.
    pub(super) async fn meter_usage(&self, agent: &str, amount: i64) -> Result<()> {
        if amount <= 0 || !self.composes(&self.inner.parts.controls) {
            return Ok(());
        }
        let own = self.control_of(Some(agent)).await?;
        let own_id = string(&own, "id");
        let used = match self
            .operation(
                &self.inner.parts.controls,
                "budget",
                "recordUsage",
                &own_id,
                json!({ "meter": self.inner.meter, "amount": amount }),
                WriteOptions::default(),
            )
            .await
        {
            Ok(used) => used,
            Err(err) => {
                tracing::warn!(agent = %agent, detail = %err, "a run's usage was not metered");
                return Ok(());
            }
        };
        let limit = own
            .get("budgetLimit")
            .filter(|v| !v.is_null())
            .map(|v| as_number(Some(v)));
        let after = as_number(used.get("used"));
        let warn_at = own
            .get("warnAt")
            .filter(|v| !v.is_null())
            .map_or(80, |v| as_number(Some(v)));
        let crossing = budget_crossing(after - amount, after, limit, warn_at);
        if crossing.warns {
            self.raise(
                &self.inner.parts.controls,
                &own_id,
                "agent.budgetWarning",
                json!({ "used": after, "limit": limit, "warnAt": warn_at }),
            )
            .await?;
        }
        let overrun = used
            .get("overruns")
            .and_then(Value::as_array)
            .is_some_and(|overruns| {
                overruns.iter().any(|o| {
                    o.get("schema").and_then(Value::as_str) == Some(&self.inner.parts.controls)
                        && o.get("id").map(|id| match id {
                            Value::String(s) => s.clone(),
                            other => other.to_string(),
                        }) == Some(own_id.clone())
                })
            });
        if overrun || crossing.capped {
            self.set_paused(Some(agent), true, Some("budget"), Some("budget"))
                .await?;
        }
        Ok(())
    }

    // ---- runs ----

    fn sandbox_of(&self, definition: &Values) -> Result<(String, Arc<dyn SandboxProvider>)> {
        let slot = opt(definition, "sandboxSlot")
            .or_else(|| self.inner.sandbox_slot.clone())
            .or_else(|| {
                self.inner
                    .providers
                    .configured(Some(RuntimeInterface::Sandbox))
                    .first()
                    .map(|c| c.slot.clone())
            })
            .ok_or_else(|| AgentsError::failed("the agents: no sandbox slot is configured"))?;
        let provider = self.inner.providers.sandbox(&slot)?;
        Ok((slot, provider))
    }

    pub(super) fn model_slot_of(&self, definition: &Values) -> Result<String> {
        opt(definition, "modelSlot")
            .or_else(|| self.inner.model_slot.clone())
            .or_else(|| {
                self.inner
                    .providers
                    .configured(Some(RuntimeInterface::Model))
                    .first()
                    .map(|c| c.slot.clone())
            })
            .ok_or_else(|| AgentsError::failed("the agents: no model slot is configured"))
    }

    pub(super) fn providers(&self) -> &Providers {
        &self.inner.providers
    }

    /// Calls an operation of the run's Lease as the dispatcher, presenting
    /// the row's current token; nothing for a runs table with no Lease.
    async fn lease_call(&self, run: &str, operation: &str) {
        let runs = &self.inner.parts.runs;
        if !self.owns(runs, "leaseToken") {
            return;
        }
        let mut options = WriteOptions::default();
        if operation != "acquire" {
            let token = match self.run_row(run).await {
                Ok(Some(row)) => row.get("leaseToken").filter(|t| !t.is_null()).cloned(),
                _ => None,
            };
            let Some(token) = token else {
                return;
            };
            options.preconditions = Some(obj(
                json!({ "Lease": { "token": as_number(Some(&token)) } }),
            ));
        }
        if let Err(err) = self
            .operation(runs, "lease", operation, run, json!({}), options)
            .await
        {
            // A lease that lapsed is Lease's to expire; one never taken has
            // nothing to give back.
            tracing::warn!(run = %run, operation = %operation, detail = %err, "a run's lease did not hold");
        }
    }

    /// Moves a run's status, through its Workflow's transition when the
    /// runs table composes one, and writes `extra` before it: a reader that
    /// sees a run ended also sees its outcome, cost and transcript. A move
    /// its transitions lack is logged and left.
    pub(super) async fn move_run(&self, run: &str, status: &str, extra: Value) -> Result<()> {
        let runs = &self.inner.parts.runs;
        let mut extra = obj(extra);
        if !self.owns(runs, "status") {
            extra.insert("status".to_owned(), json!(status));
            self.update(runs, run, Value::Object(extra)).await?;
            return Ok(());
        }
        if !extra.is_empty() {
            self.update(runs, run, Value::Object(extra)).await?;
        }
        let Some(current) = self.run_row(run).await? else {
            return Ok(());
        };
        let from = string(&current, "status");
        if from != status {
            if let Err(err) = self
                .operation(
                    runs,
                    "status",
                    "transition",
                    run,
                    json!({ "to": status }),
                    WriteOptions::default(),
                )
                .await
            {
                tracing::warn!(run = %run, from = %from, to = %status, detail = %err, "a run did not move");
            }
        }
        Ok(())
    }

    /// What a run's instance cost, in the budget's units; nothing for a run
    /// with no instance, or one whose cost does not read.
    async fn instance_cost(&self, run: &Values) -> i64 {
        let (Some(instance), Some(slot)) = (opt(run, "instance"), opt(run, "sandboxSlot")) else {
            return 0;
        };
        let cost = match self.inner.providers.sandbox(&slot) {
            Ok(provider) => provider.cost(&instance).await,
            Err(err) => Err(err),
        };
        match cost {
            Ok(answer) => (self.inner.sandbox_cost_of)(&answer),
            Err(err) => {
                tracing::warn!(run = %string(run, "id"), detail = %err, "a sandbox cost was not read");
                0
            }
        }
    }

    /// The transcript a run leaves, redacted whole, its completions'
    /// `providerState` with the rest, since no later turn passes it back:
    /// in the bucket, when there is one, or on the run row. Answers the
    /// row's transcript and its object.
    async fn leave_transcript(&self, run_id: &str, run: &Values) -> Result<(Value, Value)> {
        let entries = self.redact(&run.get("transcript").cloned().unwrap_or(json!([])));
        let entries = if entries.is_null() {
            json!([])
        } else {
            entries
        };
        let Some(store) = &self.inner.transcripts else {
            let object = run.get("transcriptObject").cloned().unwrap_or(Value::Null);
            return Ok((entries, object));
        };
        let name = format!("agent-runs/{run_id}.json");
        store
            .put(&name, serde_json::to_string(&entries).unwrap_or_default())
            .await?;
        Ok((Value::Null, json!(name)))
    }

    /// Ends a run: its token revoked, its instance's cost metered, its
    /// transcript written, its outcome recorded and its lease released.
    pub(super) async fn finish(
        &self,
        run_id: &str,
        outcome: &str,
        detail: Option<&str>,
    ) -> Result<()> {
        let Some(run) = self.run_row(run_id).await? else {
            return Ok(());
        };
        let run_id = &string(&run, "id");
        if ended(&string(&run, "status")) {
            return Ok(());
        }
        self.revoke_grants_of(run_id).await?;
        let spent = self.instance_cost(&run).await;
        let cost = number(&run, "cost") + spent;
        let (transcript, transcript_object) = self.leave_transcript(run_id, &run).await?;
        let status = match outcome {
            "done" => "done",
            "failed" => "failed",
            _ => "cancelled",
        };
        // A run that asked and then ended runs again on its way to done or
        // failed.
        if string(&run, "status") == "waiting" && status != "cancelled" {
            self.move_run(run_id, "running", json!({})).await?;
        }
        self.move_run(
            run_id,
            status,
            json!({
                "outcome": outcome,
                "detail": detail,
                "cost": cost,
                "transcript": transcript,
                "transcriptObject": transcript_object,
                "endedAt": at_text(self.now()),
            }),
        )
        .await?;
        // The lease is given back once the run has ended, so its end moves
        // no state of a run in flight.
        self.lease_call(run_id, "release").await;
        // The instance's cost counts once the run has ended, so a cap it
        // reaches pauses the agent without ending this run again.
        self.meter_usage(&string(&run, "agent"), spent).await?;
        if outcome != "done" && string(&run, "trigger") == "assigned" {
            if let (Some(table), Some(key)) = (opt(&run, "triggerTable"), opt(&run, "triggerKey")) {
                self.give_back(&run, &RowRef::new(table, key)).await?;
            }
        }
        Ok(())
    }

    async fn revoke_grants_of(&self, run: &str) -> Result<()> {
        let client = self.store().client().await?;
        for grant in client
            .db()
            .active_grants(
                self.inner.definition,
                &ActiveGrantsFilter {
                    holder: Some(run.to_owned()),
                    now: Some(self.now()),
                    ..ActiveGrantsFilter::default()
                },
            )
            .await?
        {
            client
                .db()
                .revoke_grant(self.inner.definition, &string(&grant, "id"), self.now())
                .await?;
        }
        Ok(())
    }

    async fn give_back(&self, run: &Values, row: &RowRef) -> Result<()> {
        let Some(on_return) = &self.inner.on_return else {
            return Ok(());
        };
        let mut client = self.store().client().await?;
        let tx = client.transaction().await?;
        {
            let repositories = self.sys_tx(tx.db(), None)?;
            on_return.on_return(&repositories, run, row).await?;
        }
        tx.commit().await?;
        Ok(())
    }

    /// Ends a run in flight: cancels its instance and revokes its token.
    pub async fn cancel(
        &self,
        run_id: &str,
        outcome: Option<&str>,
        detail: Option<&str>,
    ) -> Result<()> {
        let Some(run) = self.run_row(run_id).await? else {
            return Ok(());
        };
        if ended(&string(&run, "status")) {
            return Ok(());
        }
        if let (Some(instance), Some(slot)) = (opt(&run, "instance"), opt(&run, "sandboxSlot")) {
            let cancelled = match self.inner.providers.sandbox(&slot) {
                Ok(provider) => provider.cancel(&instance).await,
                Err(err) => Err(err),
            };
            if let Err(err) = cancelled {
                tracing::warn!(run = %run_id, detail = %err, "an instance was not cancelled");
            }
        }
        self.finish(run_id, outcome.unwrap_or("cancelled"), detail)
            .await
    }

    fn run_fields(
        &self,
        request: &AgentRunRequest,
        definition: &Values,
        targets: &[RowRef],
    ) -> Values {
        let mut input = request.input.clone().unwrap_or_default();
        if let Some(message) = request.message.as_ref().filter(|m| !m.is_null()) {
            input.insert("message".to_owned(), message.clone());
        }
        let party = request.party.as_ref();
        obj(json!({
            "agent": definition.get("id").cloned().unwrap_or(Value::Null),
            "agentName": definition.get("name").cloned().unwrap_or(Value::Null),
            "definitionVersion": number(definition, "_version"),
            "trigger": request.trigger.as_deref().unwrap_or("principal"),
            "triggerTable": request.subject_table.clone().or_else(|| party.map(|p| p.table.clone())),
            "triggerKey": request.subject_key.clone().or_else(|| party.map(|p| p.key.clone())),
            "event": request.event,
            "input": input,
            "requestedBy": request.requested_by.as_ref().map(|r| r.subject.clone()),
            "requestedByKind": request.requested_by.as_ref().map(|r| r.kind.clone()),
            "actingForKind": request.acting_for.as_ref().map(|r| r.kind.clone()).or_else(|| party.map(|_| "party".to_owned())),
            "actingFor": request.acting_for.as_ref().map(|r| r.subject.clone()).or_else(|| party.map(|p| p.key.clone())),
            "actingForTable": request.acting_for.as_ref().and_then(|r| r.table.clone()).or_else(|| party.map(|p| p.table.clone())),
            "targets": targets,
        }))
    }

    async fn refuse_run(
        &self,
        request: &AgentRunRequest,
        definition: Option<&Values>,
        outcome: &str,
        detail: &str,
    ) -> Result<()> {
        let Some(definition) = definition else {
            tracing::warn!(agent = ?request.agent, trigger = ?request.trigger, "a run request names no agent");
            return Ok(());
        };
        let refused = self
            .create(
                &self.inner.parts.runs,
                Value::Object(self.run_fields(request, definition, &[])),
            )
            .await?;
        self.move_run(
            &string(&refused, "id"),
            "cancelled",
            json!({ "outcome": outcome, "detail": detail, "endedAt": at_text(self.now()) }),
        )
        .await?;
        if request.trigger.as_deref() == Some("assigned") {
            if let (Some(table), Some(key)) = (&request.subject_table, &request.subject_key) {
                let run = obj(json!({ "agent": definition.get("id"), "trigger": "assigned" }));
                self.give_back(&run, &RowRef::new(table.clone(), key.clone()))
                    .await?;
            }
        }
        Ok(())
    }

    /// An inbound message by its key: the inbound path's message row, its
    /// text and its recorded trust.
    async fn read_inbound(
        &self,
        message: &str,
    ) -> Result<Option<(String, Option<RowRef>, Option<String>)>> {
        let Some(inbound) = self.inner.inbound.get() else {
            return Ok(None);
        };
        let Some(row) = inbound.message(message).await? else {
            return Ok(None);
        };
        let row_ref = self
            .inner
            .messages
            .as_ref()
            .map(|table| RowRef::new(table.clone(), string(&row, "id")));
        Ok(Some((string(&row, "body"), row_ref, opt(&row, "trust"))))
    }

    /// The run's scope: the row it is about, the rows the request adds, and
    /// an inbound message's grant's target, the party's row and the
    /// message's; or why it has none.
    async fn scope_of(
        &self,
        request: &AgentRunRequest,
    ) -> Result<std::result::Result<Vec<RowRef>, String>> {
        let mut out: Vec<RowRef> = Vec::new();
        let mut add = |row: RowRef| {
            if !out.contains(&row) {
                out.push(row);
            }
        };
        if request.trigger.as_deref() == Some("inbound") {
            let (Some(grant_id), Some(party)) = (&request.grant, &request.party) else {
                return Ok(Err(
                    "an inbound message names its party and the party's grant".to_owned(),
                ));
            };
            let client = self.store().client().await?;
            let grants = client
                .db()
                .active_grants(
                    self.inner.definition,
                    &ActiveGrantsFilter {
                        holder: Some(party.key.clone()),
                        now: Some(self.now()),
                        ..ActiveGrantsFilter::default()
                    },
                )
                .await?;
            drop(client);
            let Some(grant) = grants
                .iter()
                .find(|g| super::super::parties::same_uuid(&string(g, "id"), grant_id))
            else {
                return Ok(Err("the party holds no such active grant".to_owned()));
            };
            add(RowRef::new(
                string(grant, "targetType"),
                string(grant, "targetId"),
            ));
            // The party's own row, which a message to it reads, and the
            // message's.
            add(party.clone());
            if let Some(Value::String(message)) = &request.message {
                if let Some((_, Some(row), _)) = self.read_inbound(message).await? {
                    add(row);
                }
            }
            return Ok(Ok(out));
        }
        if let (Some(table), Some(key)) = (&request.subject_table, &request.subject_key) {
            add(RowRef::new(table.clone(), key.clone()));
        }
        for target in request.targets.iter().flatten() {
            add(target.clone());
        }
        Ok(Ok(out))
    }

    fn task_of(run: &Values, definition: &Values) -> String {
        let input = run
            .get("input")
            .and_then(Value::as_object)
            .cloned()
            .unwrap_or_default();
        if let Some(task) = input.get("task").and_then(Value::as_str) {
            return task.to_owned();
        }
        if let Some(text) = input.get("inboundText").and_then(Value::as_str) {
            return text.to_owned();
        }
        match input.get("message") {
            Some(Value::String(message)) => return format!("An inbound message, {message}"),
            Some(Value::Object(message)) => {
                if let Some(body) = message.get("body").and_then(Value::as_str) {
                    return body.to_owned();
                }
            }
            _ => {}
        }
        opt(definition, "task")
            .or_else(|| opt(definition, "purpose"))
            .unwrap_or_else(|| {
                format!(
                    "Work on {} {}",
                    opt(run, "triggerTable").unwrap_or_else(|| "the task".to_owned()),
                    string(run, "triggerKey")
                )
                .trim()
                .to_owned()
            })
    }

    async fn enqueue(&self, request: &AgentRunRequest, delay: Duration) -> Result<()> {
        let mut client = self.store().client().await?;
        let tx = client.transaction().await?;
        tx.db()
            .enqueue(&self.inner.requests, request, delay)
            .await?;
        tx.commit().await?;
        Ok(())
    }

    /// The run a request starts again, and the definition it runs; or that
    /// the request is spent (a run that is no longer queued or waiting).
    async fn definition_for(
        &self,
        request: &AgentRunRequest,
    ) -> Result<Option<(Option<Values>, Option<Values>)>> {
        if let Some(run) = &request.run {
            let Some(row) = self.run_row(run).await? else {
                return Ok(None);
            };
            if !matches!(string(&row, "status").as_str(), "queued" | "waiting") {
                return Ok(None);
            }
            let definition = self.definition_row(&string(&row, "agent")).await?;
            return Ok(Some((Some(row), definition)));
        }
        if let Some(agent) = &request.agent {
            return Ok(Some((None, self.find_definition(agent).await?)));
        }
        if request.trigger.as_deref() == Some("inbound") {
            let found = self
                .find_many(
                    &self.inner.parts.definitions,
                    json!({ "inbound": { "eq": true } }),
                    &FindOptions {
                        order_by: vec![OrderBy::asc("name")],
                        ..many(1)
                    },
                )
                .await?
                .into_iter()
                .next();
            return Ok(Some((None, found)));
        }
        Ok(Some((None, None)))
    }

    /// The run row of a new request, with its scope, and for an inbound
    /// message its grant, its text and its trust; none when the request is
    /// refused, as a run row that says why.
    async fn new_run(
        &self,
        request: &AgentRunRequest,
        definition: &Values,
    ) -> Result<Option<Values>> {
        let targets = match self.scope_of(request).await? {
            Ok(targets) => targets,
            Err(detail) => {
                self.refuse_run(request, Some(definition), "refused", &detail)
                    .await?;
                return Ok(None);
            }
        };
        let mut fields = self.run_fields(request, definition, &targets);
        if request.trigger.as_deref() == Some("inbound") {
            // The grant the run is under, which narrows its permissions and
            // its paths (D183).
            let mut input = fields
                .get("input")
                .and_then(Value::as_object)
                .cloned()
                .unwrap_or_default();
            input.insert("grant".to_owned(), json!(request.grant));
            let read = match &request.message {
                Some(Value::String(message)) => self.read_inbound(message).await?,
                _ => None,
            };
            if let Some((text, _, _)) = &read {
                input.insert("inboundText".to_owned(), json!(text));
            }
            fields.insert("input".to_owned(), Value::Object(input));
            // The message's trust, as its row records it (D157): a sender
            // entered by hand, and a message the dispatcher cannot read or
            // that the request carries as text, is entered, whose run asks
            // before anything but a read (D183).
            let recorded = read.as_ref().and_then(|(_, _, trust)| trust.clone());
            fields.insert(
                "trust".to_owned(),
                json!(trust_of(recorded.as_deref()).as_str()),
            );
        }
        let run = self
            .create(&self.inner.parts.runs, Value::Object(fields))
            .await?;
        // The run on the timeline of the row that started it (D154): a
        // reference from the run, which the row lists among its backlinks.
        if let (Some(table), Some(key)) = (opt(&run, "triggerTable"), opt(&run, "triggerKey")) {
            self.refer_run(&string(&run, "id"), &RowRef::new(table, key))
                .await;
        }
        Ok(Some(run))
    }

    /// Whether a principal's run past every agent's cap waits for its
    /// approval: it asks, once, and waits until the approval is given, which
    /// it then spends.
    async fn waits_past_budget(&self, run: &Values, definition: &Values) -> Result<bool> {
        let approvals = &self.inner.parts.approvals;
        let run_id = string(run, "id");
        let pending = self
            .find_one(
                approvals,
                json!({
                    "run": { "eq": run_id },
                    "operation": { "eq": "agents.overBudget" },
                    "state": { "in": ["pending", "approved"] },
                }),
            )
            .await?;
        if let Some(approved) = pending
            .as_ref()
            .filter(|p| string(p, "state") == "approved")
        {
            self.update(
                approvals,
                &string(approved, "id"),
                json!({ "state": "used", "usedAt": at_text(self.now()) }),
            )
            .await?;
            return Ok(false);
        }
        if pending.is_none() {
            self.ask_approval(
                run,
                "agents.overBudget",
                OperationClass::MoneyMovement,
                &json!({ "agent": definition.get("name"), "run": run_id }),
                permissions::BUDGETS,
                &format!(
                    "Run {} past every agent's budget",
                    string(definition, "name")
                ),
            )
            .await?;
        }
        if string(run, "status") == "queued" {
            self.move_run(&run_id, "running", json!({})).await?;
        }
        self.move_run(&run_id, "waiting", json!({})).await?;
        Ok(true)
    }

    /// The run's token: a grant per target row, held by the run, ending at
    /// `ends_at`; answers the token and the grants' ids.
    async fn mint(
        &self,
        run_id: &str,
        role: &str,
        targets: &[RowRef],
        ends_at: SystemTime,
    ) -> Result<(String, Vec<String>)> {
        let mut token = String::new();
        let mut grants: Vec<String> = Vec::new();
        let client = self.store().client().await?;
        let system = self.sys(client.db())?;
        for target in targets {
            let issued = system
                .issue_grant(&IssueGrant {
                    holder: run_id.to_owned(),
                    holder_kind: PrincipalKind::Agent,
                    role: role.to_owned(),
                    target_type: target.table.clone(),
                    target_id: target.key.clone(),
                    starts_at: None,
                    ends_at: Some(ends_at),
                    ends_when: None,
                })
                .await?;
            grants.push(string(&issued.grant, "id"));
            if token.is_empty() {
                token = issued.token;
            }
        }
        Ok((token, grants))
    }

    async fn start(&self, request: AgentRunRequest) -> Result<()> {
        let Some((existing, definition)) = self.definition_for(&request).await? else {
            return Ok(());
        };
        let Some(definition) = definition else {
            return self
                .refuse_run(&request, None, "refused", "no such agent")
                .await;
        };
        let agent_id = string(&definition, "id");
        let stand = self.standing(&agent_id).await?;
        let fail = async |outcome: &str, detail: &str| -> Result<()> {
            match &existing {
                Some(run) => self.finish(&string(run, "id"), outcome, Some(detail)).await,
                None => {
                    self.refuse_run(&request, Some(&definition), outcome, detail)
                        .await
                }
            }
        };
        if let Some(reason) = &stand.paused {
            return fail("paused", &format!("the agent is paused ({reason})")).await;
        }
        if stand.agent_capped {
            self.set_paused(Some(&agent_id), true, Some("budget"), Some("budget"))
                .await?;
            return fail("budget", "the agent reached its budget's cap").await;
        }
        let trigger = existing
            .as_ref()
            .and_then(|run| opt(run, "trigger"))
            .or_else(|| request.trigger.clone())
            .unwrap_or_else(|| "principal".to_owned());
        if stand.total_capped && trigger != "principal" {
            return fail(
                "budget",
                "every agent's budget reached its cap, and scheduled runs stop",
            )
            .await;
        }
        let run = match existing.clone() {
            Some(run) => run,
            None => match self.new_run(&request, &definition).await? {
                Some(run) => run,
                None => return Ok(()),
            },
        };
        // Every agent over its cap: a principal's run asks before it starts.
        if stand.total_capped && self.waits_past_budget(&run, &definition).await? {
            return Ok(());
        }
        self.launch(&run, &definition, &request).await
    }

    /// Starts a run's instance: the run moves to running, holds its lease,
    /// is given its token and its task, and its sandbox instance starts, or
    /// Retries records the failure.
    async fn launch(
        &self,
        run: &Values,
        definition: &Values,
        request: &AgentRunRequest,
    ) -> Result<()> {
        let runs = self.inner.parts.runs.clone();
        let run_id = string(run, "id");
        if matches!(string(run, "status").as_str(), "waiting" | "queued") {
            self.move_run(&run_id, "running", json!({})).await?;
        }
        self.lease_call(&run_id, "acquire").await;
        let limits = definition
            .get("limits")
            .and_then(Value::as_object)
            .cloned()
            .unwrap_or_default();
        let max_seconds = limits
            .get("maxSeconds")
            .map_or(3600, |v| as_number(Some(v)).max(0) as u64);
        let ends_at = self.now() + Duration::from_secs(max_seconds);
        let targets = targets_of(run);
        if targets.is_empty() {
            return self
                .finish(
                    &run_id,
                    "refused",
                    Some("the run has no target rows, so its token would reach nothing"),
                )
                .await;
        }
        let (token, grants) = self
            .mint(&run_id, &string(definition, "role"), &targets, ends_at)
            .await?;
        // The task is the transcript's first entry before the instance can
        // ask for its first turn.
        let has_transcript = run
            .get("transcript")
            .and_then(Value::as_array)
            .is_some_and(|t| !t.is_empty());
        if !has_transcript {
            let mut entry = json!({ "kind": "task", "role": "user", "content": Self::task_of(run, definition) });
            if string(run, "trigger") == "inbound" {
                entry["untrusted"] = json!(true);
            }
            self.append(&run_id, vec![entry]).await?;
        }
        let attempts = number(run, "attempts") + 1;
        match self
            .start_instance(run, definition, &limits, max_seconds, &token)
            .await
        {
            Ok((slot, instance)) => {
                self.update(
                    &runs,
                    &run_id,
                    json!({
                        "grants": grants,
                        "instance": instance.id,
                        "sandboxSlot": slot,
                        "startedAt": at_text(instance.started_at),
                        "attempts": attempts,
                    }),
                )
                .await?;
            }
            Err(detail) => {
                tracing::warn!(run = %run_id, detail = %detail, "a sandbox instance did not start");
                let client = self.store().client().await?;
                for id in &grants {
                    client
                        .db()
                        .revoke_grant(self.inner.definition, id, self.now())
                        .await?;
                }
                drop(client);
                self.update(
                    &runs,
                    &run_id,
                    json!({ "grants": grants, "attempts": attempts }),
                )
                .await?;
                return self.retry(&run_id, request, &detail).await;
            }
        }
        let watch = AgentRunRequest {
            step: Some("watch".to_owned()),
            run: Some(run_id),
            ..AgentRunRequest::default()
        };
        self.enqueue(&watch, Duration::from_secs(self.inner.watch_seconds))
            .await
    }

    async fn start_instance(
        &self,
        run: &Values,
        definition: &Values,
        limits: &Values,
        max_seconds: u64,
        token: &str,
    ) -> std::result::Result<(String, crate::providers::SandboxInstance), String> {
        let (slot, provider) = self.sandbox_of(definition).map_err(|e| e.message)?;
        let extra = self
            .inner
            .sandbox_spec
            .as_ref()
            .map(|of| of(run, definition))
            .unwrap_or_default();
        let credentials: Vec<Value> = definition
            .get("credentials")
            .and_then(Value::as_array)
            .map(|items| {
                items
                    .iter()
                    .map(|c| match c {
                        Value::String(reference) => json!({ "ref": reference }),
                        other => other.clone(),
                    })
                    .collect()
            })
            .unwrap_or_default();
        let mut env = extra.env;
        let run_id = string(run, "id");
        env.insert("AGENT_RUN_ID".to_owned(), run_id.clone());
        env.insert("AGENT_ENDPOINT".to_owned(), self.inner.endpoint.clone());
        env.insert("AGENT_NAME".to_owned(), string(definition, "name"));
        env.insert(
            "AGENT_DEFINITION_VERSION".to_owned(),
            number(definition, "_version").to_string(),
        );
        env.insert(
            "AGENT_CREDENTIALS".to_owned(),
            serde_json::to_string(&credentials).unwrap_or_else(|_| "[]".to_owned()),
        );
        let mut secrets = extra.secrets;
        secrets.insert("AGENT_RUN_TOKEN".to_owned(), SecretValue::new(token));
        let seconds = |member: &str| limits.get(member).map(|v| as_number(Some(v)).max(0) as u64);
        let spec = SandboxSpec {
            run_id,
            image: opt(definition, "image"),
            command: definition
                .get("command")
                .and_then(Value::as_array)
                .map(|items| {
                    items
                        .iter()
                        .map(|c| match c {
                            Value::String(s) => s.clone(),
                            other => other.to_string(),
                        })
                        .collect()
                })
                .unwrap_or_default(),
            env,
            secrets,
            limits: SandboxLimits {
                wall_seconds: Some(seconds("wallSeconds").unwrap_or(max_seconds)),
                cpu_seconds: seconds("cpuSeconds"),
                memory_mb: seconds("memoryMb"),
            },
        };
        let instance = provider.start(&spec).await.map_err(|err| err.to_string())?;
        Ok((slot, instance))
    }

    async fn refer_run(&self, run: &str, to: &RowRef) {
        if self.inner.definition.references.is_none() {
            return;
        }
        let added: std::result::Result<(), BoxError> = async {
            let mut client = self.store().client().await?;
            let tx = client.transaction().await?;
            self.sys_tx(tx.db(), None)?
                .references()?
                .add(
                    &RowRef::new(self.inner.parts.runs.clone(), run),
                    to,
                    "agentRun",
                )
                .await?;
            tx.commit().await?;
            Ok(())
        }
        .await;
        // A runs table without @references keeps no timeline entry.
        if let Err(err) = added {
            tracing::debug!(run = %run, detail = %err, "a run's timeline entry was not added");
        }
    }

    /// A failed start: Retries records it and its backoff says when the
    /// next starts; exhausted, the run fails.
    async fn retry(&self, run_id: &str, request: &AgentRunRequest, detail: &str) -> Result<()> {
        let runs = self.inner.parts.runs.clone();
        if !self.owns(&runs, "retriesTotal") {
            return self.finish(run_id, "failed", Some(detail)).await;
        }
        let message: String = detail.chars().take(500).collect();
        let attempt = match self
            .operation(
                &runs,
                "retries",
                "recordAttempt",
                run_id,
                json!({ "failure": "sandbox", "detail": { "message": message } }),
                WriteOptions::default(),
            )
            .await
        {
            Ok(attempt) => attempt,
            Err(err) => {
                return self
                    .finish(run_id, "failed", Some(&format!("{detail}; {err}")))
                    .await
            }
        };
        if attempt
            .get("exhausted")
            .and_then(Value::as_bool)
            .unwrap_or(false)
        {
            return self.finish(run_id, "failed", Some(detail)).await;
        }
        self.move_run(run_id, "queued", json!({})).await?;
        self.lease_call(run_id, "release").await;
        let now = millis(self.now());
        let at = millis_of(attempt.get("retryAt")).unwrap_or(now);
        let delay = Duration::from_millis(u64::try_from((at - now).max(0)).unwrap_or(0));
        let delay = Duration::from_secs(delay.as_secs() + u64::from(delay.subsec_nanos() > 0));
        let mut again = request.clone();
        again.step = Some("start".to_owned());
        again.run = Some(run_id.to_owned());
        self.enqueue(&again, delay).await
    }

    async fn watch(&self, run_id: &str) -> Result<()> {
        let Some(run) = self.run_row(run_id).await? else {
            return Ok(());
        };
        let (Some(instance), Some(slot)) = (opt(&run, "instance"), opt(&run, "sandboxSlot")) else {
            return Ok(());
        };
        if !live(&string(&run, "status")) {
            return Ok(());
        }
        let provider = self.inner.providers.sandbox(&slot)?;
        let status = provider.status(&instance).await?;
        match status.state {
            SandboxState::Running | SandboxState::Starting => {
                self.lease_call(run_id, "heartbeat").await;
                let watch = AgentRunRequest {
                    step: Some("watch".to_owned()),
                    run: Some(run_id.to_owned()),
                    ..AgentRunRequest::default()
                };
                self.enqueue(&watch, Duration::from_secs(self.inner.watch_seconds))
                    .await
            }
            SandboxState::Succeeded => {
                let reported = opt(&run, "outcome").unwrap_or_else(|| "done".to_owned());
                self.finish(run_id, &reported, None).await
            }
            SandboxState::Cancelled => {
                self.finish(run_id, "cancelled", Some("the instance was cancelled"))
                    .await
            }
            SandboxState::Failed => {
                let detail = status.detail.unwrap_or_else(|| {
                    format!(
                        "the instance exited {}",
                        status.exit_code.map(|c| c.to_string()).unwrap_or_default()
                    )
                    .trim()
                    .to_owned()
                });
                self.finish(run_id, "failed", Some(&detail)).await
            }
        }
    }

    pub(super) fn thread(&self) -> Arc<dyn AskThread> {
        self.inner.thread.clone().unwrap_or_else(|| {
            Arc::new(CommentsThread {
                dispatcher: self.inner.dispatcher.clone(),
            })
        })
    }

    async fn follow_up(&self, request: &AgentRunRequest) -> Result<()> {
        let (Some(ask), Some(run_id)) = (&request.ask, &request.run) else {
            return Ok(());
        };
        let Some(run) = self.run_row(run_id).await? else {
            return Ok(());
        };
        let row = RowRef::new(ask.table.clone(), ask.key.clone());
        let answered = {
            let mut client = self.store().client().await?;
            let tx = client.transaction().await?;
            let answered = {
                let repositories = self.sys_tx(tx.db(), None)?;
                self.thread()
                    .answered(&repositories, &row, &ask.message, &string(&run, "id"))
                    .await?
            };
            tx.commit().await?;
            answered
        };
        if answered {
            return Ok(());
        }
        if let Some(follow_up) = &self.inner.follow_up {
            let mut client = self.store().client().await?;
            let tx = client.transaction().await?;
            {
                let repositories = self.sys_tx(tx.db(), None)?;
                follow_up
                    .follow_up(&repositories, &run, &row, &ask.question)
                    .await?;
            }
            tx.commit().await?;
        }
        self.append(
            &string(&run, "id"),
            vec![json!({ "kind": "note", "content": format!("nobody answered by the deadline: {}", ask.question) })],
        )
        .await
    }

    /// Handles one message of the request queue: the dispatcher's worker
    /// handler. A `@runRequests` queue's message (D157) is handed as
    /// [`AgentRunRequest::from`] it.
    pub async fn handle(&self, message: AgentRunRequest) -> Result<()> {
        match message.step.as_deref().unwrap_or("start") {
            "watch" => self.watch(message.run.as_deref().unwrap_or_default()).await,
            "followUp" => self.follow_up(&message).await,
            _ => self.start(message).await,
        }
    }

    /// The request queue over the tables' pool, as a worker claims it. A
    /// worker's prune of it is [`Agents::prune`]'s too.
    pub fn worker_queue(&self) -> PartQueue<AgentRunRequest> {
        self.inner.queue.clone()
    }

    /// Puts a run request on the dispatcher's queue in the caller's
    /// transaction, due after `delay`; a principal of kind agent may not
    /// request a run, and an inbound message's request names its party and
    /// its grant.
    pub async fn request<'c>(
        &self,
        client: impl super::super::store::IntoDb<'c>,
        input: AgentRunRequest,
        delay: Duration,
    ) -> Result<()> {
        let input = self.requested(input)?;
        client
            .into_db()
            .enqueue(&self.inner.requests, &input, delay)
            .await?;
        Ok(())
    }

    /// [`Agents::request`] in the caller's own transaction on a SQLite
    /// file (#618): `connection` is its rusqlite transaction, which the
    /// request commits with.
    #[cfg(feature = "sqlite")]
    pub fn request_sqlite(
        &self,
        connection: &super::super::sqlite::rusqlite::Connection,
        input: AgentRunRequest,
        delay: Duration,
    ) -> Result<()> {
        let input = self.requested(input)?;
        crate::queue::sqlite::enqueue_sqlite_in(connection, &self.inner.requests, &input, delay)?;
        Ok(())
    }

    /// A run request as the queue takes it: one an agent run made, and an
    /// inbound message's that names no party and grant, are refused.
    fn requested(&self, input: AgentRunRequest) -> Result<AgentRunRequest> {
        if input
            .requested_by
            .as_ref()
            .is_some_and(|r| r.kind == "agent")
        {
            return Err(AgentsError::new(
                403,
                "agents_cannot_request",
                "an agent run may not start a run: only the dispatcher starts one (D158)",
            ));
        }
        if input.trigger.as_deref() == Some("inbound")
            && (input.party.is_none() || input.grant.is_none())
        {
            return Err(AgentsError::new(
                400,
                "invalid_request",
                "an inbound message's request names its party and its grant",
            ));
        }
        let mut input = input;
        input.step = Some("start".to_owned());
        Ok(input)
    }

    // ---- the run's principal ----

    async fn permissions_of(
        &self,
        kind: &str,
        subject: &str,
        table: Option<&str>,
        grant: Option<&str>,
    ) -> Result<Vec<String>> {
        let definition = self.inner.definition;
        let Some(roles) = definition.access_table(ACCESS_ROLES) else {
            return Ok(Vec::new());
        };
        let mut out: Vec<String> = Vec::new();
        let mut add = |role: Option<Values>| {
            for p in role
                .and_then(|r| r.get("permissions").cloned())
                .and_then(|p| p.as_array().cloned())
                .unwrap_or_default()
                .iter()
                .filter_map(Value::as_str)
            {
                if !out.iter().any(|q| q == p) {
                    out.push(p.to_owned());
                }
            }
        };
        let client = self.store().client().await?;
        if kind == "user" {
            let Ok(grants) = definition.table("UserRoleGrant") else {
                return Ok(Vec::new());
            };
            let rows = self
                .sys(client.db())?
                .table(&grants.name)?
                .find_many(
                    Some(&obj(json!({ "userId": { "eq": subject } }))),
                    &many(1000),
                )
                .await?
                .rows;
            for grant in rows {
                let role = self
                    .sys(client.db())?
                    .table(&roles.name)?
                    .find_one(
                        Some(&obj(json!({ "id": { "eq": grant.get("roleId").cloned().unwrap_or(Value::Null) } }))),
                        &FindOptions::default(),
                    )
                    .await?;
                add(role);
            }
        } else if let Some(table) = table {
            // A party is what D157's party principal says it is: its active
            // grants' roles, or the one grant the run is under.
            return Ok(party_principal(
                client.db(),
                definition,
                &PartyRef::new(table, subject),
                grant,
                self.now(),
            )
            .await?
            .permissions);
        } else {
            for grant in client
                .db()
                .active_grants(
                    definition,
                    &ActiveGrantsFilter {
                        holder: Some(subject.to_owned()),
                        now: Some(self.now()),
                        ..ActiveGrantsFilter::default()
                    },
                )
                .await?
            {
                add(role_named(client.db(), definition, &string(&grant, "role")).await?);
            }
        }
        Ok(out)
    }

    async fn role_reaches(&self, name: &str) -> Result<BTreeMap<String, Vec<String>>> {
        let client = self.store().client().await?;
        let role = role_named(client.db(), self.inner.definition, name).await?;
        let entries: Vec<String> = role
            .and_then(|r| r.get("reaches").cloned())
            .and_then(|r| r.as_array().cloned())
            .unwrap_or_default()
            .iter()
            .filter_map(Value::as_str)
            .map(str::to_owned)
            .collect();
        Ok(reaches_of(entries.iter().map(String::as_str)))
    }

    /// The principal a run's token resolves to (D158, D183), or none for a
    /// token that is no running run's: kind agent, acting for whom the run
    /// acts for, its permissions the role's narrowed to that principal's,
    /// its grants the run's, each reaching through only the paths the
    /// party's grant of the same target reaches too when it acts for a
    /// party, and its claims the run, the agent, the definition's version
    /// and the run's trust.
    pub async fn principal_of(&self, token: &str) -> Result<Option<Principal>> {
        if !token.starts_with(GRANT_TOKEN_PREFIX) {
            return Ok(None);
        }
        let mut authenticator = StoreGrants::new(self.inner.tables.clone())?.admitting_runs();
        if let Some(now) = self.inner.now.clone() {
            authenticator = authenticator.with_clock(now);
        }
        let (parts, ()) = http::Request::builder()
            .header(http::header::AUTHORIZATION, format!("Bearer {token}"))
            .body(())
            .map_err(AgentsError::failed)?
            .into_parts();
        // An unknown, revoked or ended grant: no running run holds it.
        let Ok(Some(base)) = authenticator.authenticate(&parts).await else {
            return Ok(None);
        };
        if base.kind != PrincipalKind::Agent {
            return Ok(None);
        }
        let Some(run) = self.run_row(&base.subject).await? else {
            return Ok(None);
        };
        if !live(&string(&run, "status")) {
            return Ok(None);
        }
        let acting_for = match (opt(&run, "actingForKind"), opt(&run, "actingFor")) {
            (Some(kind), Some(subject)) => Some((kind, subject)),
            _ => None,
        };
        let input = run
            .get("input")
            .and_then(Value::as_object)
            .cloned()
            .unwrap_or_default();
        let run_grant = input
            .get("grant")
            .and_then(Value::as_str)
            .map(str::to_owned);
        // A run acting for a party reaches through no path the party's
        // grant does not (D183): the grant it runs under, or each active
        // grant of the party, read on every call, so a grant that ended
        // narrows it to nothing.
        let mut party_grants: Option<Vec<PartyReach>> = None;
        if let Some((kind, subject)) = &acting_for {
            if kind == "party" {
                let client = self.store().client().await?;
                let grants = client
                    .db()
                    .active_grants(
                        self.inner.definition,
                        &ActiveGrantsFilter {
                            holder: Some(subject.clone()),
                            now: Some(self.now()),
                            ..ActiveGrantsFilter::default()
                        },
                    )
                    .await?;
                drop(client);
                let mut held = Vec::new();
                for grant in grants {
                    if run_grant.as_deref().is_some_and(|g| {
                        !super::super::parties::same_uuid(&string(&grant, "id"), g)
                    }) {
                        continue;
                    }
                    held.push((
                        string(&grant, "targetType"),
                        string(&grant, "targetId"),
                        self.role_reaches(&string(&grant, "role")).await?,
                    ));
                }
                party_grants = Some(held);
            }
        }
        let own_grants = {
            let client = self.store().client().await?;
            client
                .db()
                .active_grants(
                    self.inner.definition,
                    &ActiveGrantsFilter {
                        holder: Some(base.subject.clone()),
                        now: Some(self.now()),
                        ..ActiveGrantsFilter::default()
                    },
                )
                .await?
        };
        let mut reaches = Vec::new();
        for grant in own_grants {
            let target_type = string(&grant, "targetType");
            let target_id = string(&grant, "targetId");
            let own = self.role_reaches(&string(&grant, "role")).await?;
            let narrowed = match &party_grants {
                Some(party) => {
                    let of_target: Vec<BTreeMap<String, Vec<String>>> = party
                        .iter()
                        .filter(|(ty, id, _)| {
                            *ty == target_type && super::super::parties::same_uuid(id, &target_id)
                        })
                        .map(|(_, _, r)| r.clone())
                        .collect();
                    narrow_reaches(&own, &of_target)
                }
                None => own,
            };
            reaches.push(GrantReach {
                id: string(&grant, "id"),
                target_type,
                target_id,
                reaches: narrowed,
            });
        }
        let permissions = match &acting_for {
            Some((kind, subject)) => {
                let actor = self
                    .permissions_of(
                        kind,
                        subject,
                        opt(&run, "actingForTable").as_deref(),
                        run_grant.as_deref(),
                    )
                    .await?;
                narrow_permissions(&base.permissions, &actor)
            }
            None => base.permissions.clone(),
        };
        let mut principal = Principal::new(base.subject.clone(), permissions)
            .of_kind(PrincipalKind::Agent)
            .with_grants(reaches);
        if let Some((kind, subject)) = acting_for {
            principal = principal.acting_for(ActingFor {
                kind: Some(match kind.as_str() {
                    "party" => PrincipalKind::Party,
                    "agent" => PrincipalKind::Agent,
                    _ => PrincipalKind::User,
                }),
                subject,
            });
        }
        if let Some(credential) = base.credential {
            principal = principal.with_credential(credential);
        }
        principal
            .claims
            .insert("run".to_owned(), json!(string(&run, "id")));
        principal
            .claims
            .insert("agent".to_owned(), json!(string(&run, "agent")));
        principal.claims.insert(
            "definitionVersion".to_owned(),
            json!(number(&run, "definitionVersion")),
        );
        if let Some(trust) = opt(&run, "trust") {
            principal.claims.insert("trust".to_owned(), json!(trust));
        }
        Ok(Some(principal))
    }

    /// The authenticator of a run's token (D158): a request whose bearer is
    /// a running run's token is its principal; any other grant's token is
    /// the grant authenticator's; every other caller is `inner`'s.
    pub fn authenticator(&self, inner: Option<Arc<dyn Authenticator>>) -> Result<RunAuthenticator> {
        let mut grants = StoreGrants::new(self.inner.tables.clone())?;
        if let Some(now) = self.inner.now.clone() {
            grants = grants.with_clock(now);
        }
        Ok(RunAuthenticator {
            agents: self.clone(),
            grants,
            inner,
        })
    }

    pub(super) async fn run_of(&self, principal: &Principal) -> Result<Values> {
        let id = principal
            .claims
            .get("run")
            .and_then(Value::as_str)
            .unwrap_or(&principal.subject)
            .to_owned();
        self.run_row(&id)
            .await?
            .ok_or_else(|| AgentsError::new(401, "unauthorized", "no such run"))
    }

    // ---- autonomy ----

    async fn policy_mode(
        &self,
        agent: &str,
        operation: &str,
        class: OperationClass,
    ) -> Result<Option<PolicyRow>> {
        let policies = &self.inner.parts.policies;
        let mut rows = self
            .find_many(policies, json!({ "agent": { "eq": agent } }), &many(500))
            .await?;
        rows.extend(
            self.find_many(policies, json!({ "agent": { "isNull": true } }), &many(500))
                .await?,
        );
        let rows: Vec<PolicyRow> = rows
            .iter()
            .map(|row| PolicyRow {
                agent: opt(row, "agent"),
                operation: opt(row, "operation"),
                operation_class: opt(row, "operationClass"),
                mode: string(row, "mode"),
                permission: opt(row, "permission"),
            })
            .collect();
        Ok(best_policy(&rows, operation, class).cloned())
    }

    pub(super) async fn ask_approval(
        &self,
        run: &Values,
        operation: &str,
        class: OperationClass,
        params: &Value,
        permission: &str,
        summary: &str,
    ) -> Result<Values> {
        let approvals = &self.inner.parts.approvals;
        let hash = params_hash(operation, params);
        let expires = self.now() + Duration::from_secs(self.inner.approval_ttl_seconds);
        let mut client = self.store().client().await?;
        let tx = client.transaction().await?;
        let created = {
            let repositories = self.sys_tx(tx.db(), None)?;
            let created = repositories
                .table(approvals)?
                .create_one(&obj(json!({
                    "agent": run.get("agent").cloned().unwrap_or(Value::Null),
                    "run": run.get("id").cloned().unwrap_or(Value::Null),
                    "operation": operation,
                    "operationClass": class.as_str(),
                    "params": self.redact(params),
                    "paramsHash": hash,
                    "summary": self.redact_text(summary),
                    "permission": permission,
                    "state": "pending",
                    "expiresAt": at_text(expires),
                })))
                .await?;
            self.raise_in(
                tx.db(),
                approvals,
                &string(&created, "id"),
                "approval.requested",
                json!({ "operation": operation, "summary": summary }),
                Some("urgent"),
            )
            .await?;
            let current = repositories
                .table(&self.inner.parts.runs)?
                .find_one(
                    Some(&obj(json!({ "id": { "eq": string(run, "id") } }))),
                    &FindOptions::default(),
                )
                .await?
                .unwrap_or_default();
            repositories
                .table(&self.inner.parts.runs)?
                .update_one(
                    &key(&string(run, "id")),
                    &obj(json!({ "approvals": number(&current, "approvals") + 1 })),
                    &WriteOptions::default(),
                )
                .await?;
            created
        };
        tx.commit().await?;
        Ok(created)
    }

    /// Holds a call of `operation` with `args` to the run's autonomy
    /// policy (D158, D183): the most specific policy row, else `mode`, else
    /// `class`'s default, but a money movement asks, and so does every
    /// operation but a read of a run whose sender was entered by hand. An
    /// ask runs once an approval of the same parameters was approved, and
    /// spends it; another call of the operation supersedes the run's open
    /// approvals of it.
    pub async fn gate(
        &self,
        principal: &Principal,
        operation: &str,
        args: &Value,
        class: Option<OperationClass>,
        mode: Option<Autonomy>,
    ) -> Result<GateOutcome> {
        let run = self.run_of(principal).await?;
        let run_id = string(&run, "id");
        let class = class.unwrap_or(OperationClass::InternalWrite);
        let policy = self
            .policy_mode(&string(&run, "agent"), operation, class)
            .await?;
        let trust: Option<RunTrust> = opt(&run, "trust").map(|t| trust_of(Some(&t)));
        let chosen = policy
            .as_ref()
            .and_then(|p| Autonomy::parse(&p.mode))
            .or(mode)
            .unwrap_or_else(|| default_autonomy(class));
        let mode = effective_autonomy(chosen, class, trust);
        if mode != Autonomy::Ask {
            return Ok(GateOutcome::Run {
                mode,
                approval: None,
            });
        }
        let approvals = &self.inner.parts.approvals;
        let hash = params_hash(operation, args);
        let open = self
            .find_many(
                approvals,
                json!({
                    "run": { "eq": run_id },
                    "operation": { "eq": operation },
                    "state": { "in": ["pending", "approved"] },
                }),
                &many(100),
            )
            .await?;
        let now = millis(self.now());
        let live: Vec<Values> = open
            .into_iter()
            .filter(|a| millis_of(a.get("expiresAt")).is_none_or(|at| at > now))
            .collect();
        // A changed parameter: every open approval of another hash no
        // longer holds.
        for other in live.iter().filter(|a| string(a, "paramsHash") != hash) {
            self.update(
                approvals,
                &string(other, "id"),
                json!({ "state": "superseded" }),
            )
            .await?;
        }
        let same = live.into_iter().find(|a| string(a, "paramsHash") == hash);
        if let Some(same) = same.as_ref().filter(|a| string(a, "state") == "approved") {
            self.update(
                approvals,
                &string(same, "id"),
                json!({ "state": "used", "usedAt": at_text(self.now()) }),
            )
            .await?;
            if string(&run, "status") == "waiting" {
                self.move_run(&run_id, "running", json!({})).await?;
            }
            return Ok(GateOutcome::Run {
                mode,
                approval: Some(string(same, "id")),
            });
        }
        let denied = self
            .find_many(
                approvals,
                json!({
                    "run": { "eq": run_id },
                    "operation": { "eq": operation },
                    "paramsHash": { "eq": hash },
                    "state": { "eq": "denied" },
                }),
                &many(1),
            )
            .await?;
        if let Some(denied) = denied.first() {
            return Ok(GateOutcome::Refused {
                status: 403,
                code: "approval_denied".to_owned(),
                detail: format!("{operation} was denied"),
                approval: Some(string(denied, "id")),
            });
        }
        let approval = match same {
            Some(same) => same,
            None => {
                let sender = if trust == Some(RunTrust::Entered) {
                    ", for a sender entered by hand and not verified"
                } else {
                    ""
                };
                let permission = policy
                    .as_ref()
                    .and_then(|p| p.permission.clone())
                    .unwrap_or_else(|| permissions::APPROVE.to_owned());
                self.ask_approval(
                    &run,
                    operation,
                    class,
                    args,
                    &permission,
                    &format!(
                        "{} asks to run {operation} with {}{sender}",
                        string(&run, "agentName"),
                        canonical_json(args)
                    ),
                )
                .await?
            }
        };
        if string(&run, "status") == "running" {
            self.move_run(&run_id, "waiting", json!({})).await?;
        }
        Ok(GateOutcome::Refused {
            status: 428,
            code: "approval_required".to_owned(),
            detail: format!(
                "{operation} runs once someone holding {} approves it: approval {}",
                string(&approval, "permission"),
                string(&approval, "id")
            ),
            approval: Some(string(&approval, "id")),
        })
    }

    async fn decide_with<C: PgClient>(
        &self,
        client: Option<Db<'_, C>>,
        principal: &Principal,
        id: &str,
        state: &str,
        via: &str,
    ) -> Result<()> {
        let approvals = self.inner.parts.approvals.clone();
        let approval = self
            .find_one(&approvals, json!({ "id": { "eq": id } }))
            .await?
            .ok_or_else(|| AgentsError::new(404, "not_found", "no such approval"))?;
        let needs = string(&approval, "permission");
        if !principal.permissions.iter().any(|p| covers(p, &needs)) {
            return Err(AgentsError::new(
                403,
                "forbidden",
                format!("approving needs {needs}"),
            ));
        }
        if principal.kind == PrincipalKind::Agent {
            return Err(AgentsError::new(
                403,
                "forbidden",
                "an agent run may not decide an approval",
            ));
        }
        let current = string(&approval, "state");
        if current != "pending" {
            return Err(AgentsError::new(
                409,
                "not_pending",
                format!("the approval is {current}"),
            ));
        }
        let decided = json!({
            "state": state,
            "decidedBy": principal.subject,
            "decidedVia": via,
            "decidedAt": at_text(self.now()),
        });
        match client {
            Some(client) => {
                self.sys(client)?
                    .table(&approvals)?
                    .update_one(&key(id), &obj(decided), &WriteOptions::default())
                    .await?;
            }
            None => {
                self.update(&approvals, id, decided).await?;
            }
        }
        // A message to a party held for this approval goes, or is refused,
        // with it (D157).
        if let (Some(holds), Some(inbound)) = (opt(&approval, "holds"), self.inner.inbound.get()) {
            if state == "approved" {
                let used = json!({ "state": "used", "usedAt": at_text(self.now()) });
                match client {
                    Some(client) => {
                        self.sys(client)?
                            .table(&approvals)?
                            .update_one(&key(id), &obj(used), &WriteOptions::default())
                            .await?;
                    }
                    None => {
                        self.update(&approvals, id, used).await?;
                    }
                }
                inbound.release(&holds).await?;
            } else {
                inbound
                    .reject(&holds, Some("its approval was denied"))
                    .await?;
            }
            if let Some(run) = opt(&approval, "run") {
                if self
                    .run_row(&run)
                    .await?
                    .is_some_and(|r| string(&r, "status") == "waiting")
                {
                    self.move_run(&run, "running", json!({})).await?;
                }
            }
        }
        if string(&approval, "operation") == "agents.overBudget" {
            if let Some(run) = opt(&approval, "run") {
                let step = if state == "approved" {
                    "start"
                } else {
                    "watch"
                };
                let request = AgentRunRequest {
                    step: Some(step.to_owned()),
                    run: Some(run.clone()),
                    ..AgentRunRequest::default()
                };
                match client {
                    Some(client) => {
                        client
                            .enqueue(&self.inner.requests, &request, Duration::ZERO)
                            .await?
                    }
                    None => self.enqueue(&request, Duration::ZERO).await?,
                }
                if state == "denied" {
                    self.finish(
                        &run,
                        "refused",
                        Some("its approval to run past the budget was denied"),
                    )
                    .await?;
                }
            }
        }
        Ok(())
    }

    /// Approves an approval as `principal`, which holds its permission and
    /// is no agent run, on `via` (`api` unless given).
    pub async fn approve(&self, principal: &Principal, id: &str, via: Option<&str>) -> Result<()> {
        self.decide_with::<PoolClient>(None, principal, id, "approved", via.unwrap_or("api"))
            .await
    }

    /// Denies an approval as `principal`.
    pub async fn deny(&self, principal: &Principal, id: &str, via: Option<&str>) -> Result<()> {
        self.decide_with::<PoolClient>(None, principal, id, "denied", via.unwrap_or("api"))
            .await
    }

    /// The reply actions `approve` and `deny` of `approval.requested`, for
    /// the notifier's `with_reply` (D153): each decides the approval its
    /// notice is about, as the replier, on the reply's channel, releasing
    /// or refusing a message to a party it holds.
    pub fn reply_actions(&self) -> [(&'static str, Arc<dyn ReplyAction>); 2] {
        let weak = Arc::downgrade(&self.inner);
        [
            (
                "approve",
                Arc::new(Decide {
                    agents: weak.clone(),
                    state: "approved",
                    describe: "Approved",
                }),
            ),
            (
                "deny",
                Arc::new(Decide {
                    agents: weak,
                    state: "denied",
                    describe: "Denied",
                }),
            ),
        ]
    }

    // ---- administration ----

    fn need(principal: &Principal, permission: &str) -> Result<()> {
        if principal.kind == PrincipalKind::Agent {
            return Err(AgentsError::new(
                403,
                "forbidden",
                "an agent run may not administer agents",
            ));
        }
        if !principal.permissions.iter().any(|p| covers(p, permission)) {
            return Err(AgentsError::new(
                403,
                "forbidden",
                format!("this needs {permission}"),
            ));
        }
        Ok(())
    }

    pub(super) async fn set_paused(
        &self,
        agent: Option<&str>,
        paused: bool,
        reason: Option<&str>,
        by: Option<&str>,
    ) -> Result<()> {
        let control = self.control_of(agent).await?;
        self.update(
            &self.inner.parts.controls,
            &string(&control, "id"),
            json!({
                "paused": paused,
                "pausedReason": if paused { reason } else { None },
                "pausedBy": if paused { by } else { None },
                "pausedAt": if paused { at_text(self.now()) } else { Value::Null },
            }),
        )
        .await?;
        if !paused {
            return Ok(());
        }
        let mut filter = json!({ "status": { "in": ["queued", "running", "waiting"] } });
        if let Some(agent) = agent {
            filter["agent"] = json!({ "eq": agent });
        }
        let runs = self
            .find_many(&self.inner.parts.runs, filter, &many(10_000))
            .await?;
        let budget = reason == Some("budget");
        let detail = if budget {
            "the agent reached its budget's cap".to_owned()
        } else {
            format!(
                "the {} paused",
                if agent.is_none() {
                    "agents are"
                } else {
                    "agent is"
                }
            )
        };
        for run in runs {
            Box::pin(self.cancel(
                &string(&run, "id"),
                Some(if budget { "budget" } else { "paused" }),
                Some(&detail),
            ))
            .await?;
        }
        Ok(())
    }

    async fn agent_id(&self, agent: Option<&str>) -> Result<Option<String>> {
        let Some(agent) = agent else {
            return Ok(None);
        };
        Ok(Some(
            self.find_definition(agent)
                .await?
                .map_or_else(|| agent.to_owned(), |d| string(&d, "id")),
        ))
    }

    /// Pauses an agent, or every agent for none, cancelling its runs in
    /// flight; `principal` holds `agents.pause`.
    pub async fn pause(
        &self,
        principal: &Principal,
        agent: Option<&str>,
        reason: Option<&str>,
    ) -> Result<()> {
        Self::need(principal, permissions::PAUSE)?;
        let id = self.agent_id(agent).await?;
        self.set_paused(
            id.as_deref(),
            true,
            Some(reason.unwrap_or("stopped")),
            Some(&principal.subject),
        )
        .await
    }

    /// Resumes an agent, or every agent for none.
    pub async fn resume(&self, principal: &Principal, agent: Option<&str>) -> Result<()> {
        Self::need(principal, permissions::PAUSE)?;
        let id = self.agent_id(agent).await?;
        self.set_paused(id.as_deref(), false, None, None).await
    }

    /// Writes an autonomy policy row for an agent (every agent for none) and
    /// an operation or a class, one of them; refuses one that lets an agent
    /// move money without asking.
    pub async fn set_policy(
        &self,
        principal: &Principal,
        agent: Option<&str>,
        operation: Option<&str>,
        class: Option<OperationClass>,
        mode: Autonomy,
        permission: Option<&str>,
    ) -> Result<Values> {
        Self::need(principal, permissions::AUTONOMY)?;
        if operation.is_none() == class.is_none() {
            return Err(AgentsError::new(
                400,
                "invalid_policy",
                "a policy names an operation or a class, one of them",
            ));
        }
        let class_of =
            class.or_else(|| operation.and_then(|op| self.inner.tools.get(op).map(|t| t.class)));
        if class_of == Some(OperationClass::MoneyMovement) && mode != Autonomy::Ask {
            return Err(AgentsError::new(
                400,
                "money_asks",
                "an operation that moves money is ask and nothing else: an agent may only ask to move money (D158)",
            ));
        }
        let agent = self.agent_id(agent).await?;
        self.create(
            &self.inner.parts.policies,
            json!({
                "agent": agent,
                "operation": operation,
                "operationClass": class.map(OperationClass::as_str),
                "mode": mode.as_str(),
                "permission": permission,
            }),
        )
        .await
    }

    /// Sets an agent's budget, or every agent's for none: its limit (none
    /// for no limit) and the percent it warns at. A limit changes only as a
    /// principal holding the Budget's `limitPermission`.
    pub async fn set_budget(
        &self,
        principal: &Principal,
        agent: Option<&str>,
        limit: Option<i64>,
        warn_at: Option<i64>,
    ) -> Result<()> {
        Self::need(principal, permissions::BUDGETS)?;
        let id = self.agent_id(agent).await?;
        let control = self.control_of(id.as_deref()).await?;
        let mut update = json!({ "budgetLimit": limit });
        if let Some(warn_at) = warn_at {
            update["warnAt"] = json!(warn_at);
        }
        let caller = Some(Caller::of(Some(principal)));
        self.update_as(
            &self.inner.parts.controls,
            &string(&control, "id"),
            update,
            caller.clone(),
        )
        .await?;
        // Every agent's limit is the limit of the budget their meters spend
        // within.
        if id.is_none() {
            if let Some((column, target)) = &self.inner.total {
                if let Some(total) = opt(&control, column) {
                    self.update_as(target, &total, json!({ "budgetLimit": limit }), caller)
                        .await?;
                }
            }
        }
        Ok(())
    }

    /// Writes or edits an agent's definition, by its name; its credential
    /// references need `agents.credentials` too, and each is a reference
    /// alone.
    pub async fn define(&self, principal: &Principal, input: Values) -> Result<Values> {
        Self::need(principal, permissions::DEFINE)?;
        let defs = self.inner.parts.definitions.clone();
        let existing = match input.get("name").and_then(Value::as_str) {
            Some(name) => {
                self.find_one(&defs, json!({ "name": { "eq": name } }))
                    .await?
            }
            None => None,
        };
        if let Some(credentials) = input.get("credentials") {
            let before = existing
                .as_ref()
                .and_then(|e| e.get("credentials").cloned())
                .unwrap_or(Value::Null);
            if canonical_json(credentials) != canonical_json(&before) {
                Self::need(principal, permissions::CREDENTIALS)?;
            }
            for reference in credentials.as_array().into_iter().flatten() {
                let text = match reference {
                    Value::String(s) => Some(s.as_str()),
                    Value::Object(o) => o.get("ref").and_then(Value::as_str),
                    _ => None,
                };
                if text.is_none_or(str::is_empty) {
                    return Err(AgentsError::new(
                        400,
                        "invalid_credentials",
                        "a credential is named by its reference alone",
                    ));
                }
            }
        }
        let written = match existing {
            Some(existing) => {
                self.update(&defs, &string(&existing, "id"), Value::Object(input))
                    .await?
            }
            None => self.create(&defs, Value::Object(input)).await?,
        };
        self.control_of(Some(&string(&written, "id"))).await?;
        Ok(written)
    }

    /// A run's redacted transcript: the bucket's once the run ended, else
    /// the run row's, its completions' provider state redacted with the
    /// rest.
    pub async fn transcript(&self, run_id: &str) -> Result<Vec<TranscriptEntry>> {
        let Some(run) = self.run_row(run_id).await? else {
            return Ok(Vec::new());
        };
        let entries = match (&self.inner.transcripts, opt(&run, "transcriptObject")) {
            (Some(store), Some(object)) if opt(&run, "transcriptPrunedAt").is_none() => {
                let text = store.get(&object).await?.unwrap_or_else(|| "[]".to_owned());
                serde_json::from_str(&text).unwrap_or(json!([]))
            }
            _ => self.redact(&run.get("transcript").cloned().unwrap_or(json!([]))),
        };
        Ok(serde_json::from_value(entries).unwrap_or_default())
    }

    /// Deletes up to `limit` transcripts past their retention, stamping
    /// each run; answers how many.
    pub async fn prune(&self, limit: u32) -> Result<u64> {
        let Some(store) = &self.inner.transcripts else {
            return Ok(0);
        };
        let before = self.now() - Duration::from_secs(self.inner.retention_days * 86_400);
        let old = self
            .find_many(
                &self.inner.parts.runs,
                json!({
                    "endedAt": { "lt": at_text(before) },
                    "transcriptObject": { "isNull": false },
                    "transcriptPrunedAt": { "isNull": true },
                }),
                &many(limit),
            )
            .await?;
        for run in &old {
            store.delete(&string(run, "transcriptObject")).await?;
            self.update(
                &self.inner.parts.runs,
                &string(run, "id"),
                json!({ "transcriptPrunedAt": at_text(self.now()) }),
            )
            .await?;
        }
        Ok(old.len() as u64)
    }

    /// The same policy as the gate's for any send (D158): `allow` for a
    /// principal that is no agent run, `allow` when the run's call runs,
    /// `ask` while its approval waits, `deny` when it was denied.
    pub async fn send_policy(
        &self,
        principal: Option<&Principal>,
        operation: &str,
        params: &Value,
    ) -> Result<SendDecision> {
        let Some(principal) =
            principal.filter(|p| p.kind == PrincipalKind::Agent && p.claims.contains_key("run"))
        else {
            return Ok(SendDecision::Allow);
        };
        let gated = self
            .gate(
                principal,
                operation,
                params,
                Some(OperationClass::MessageParty),
                None,
            )
            .await?;
        Ok(match gated {
            GateOutcome::Run { .. } => SendDecision::Allow,
            GateOutcome::Refused { status: 428, .. } => SendDecision::Ask,
            GateOutcome::Refused { .. } => SendDecision::Deny,
        })
    }

    /// The parameters of a send an approval is bound to: its party,
    /// channel, subject and body.
    pub(super) fn send_params(
        party: &PartyRef,
        channel: &str,
        subject: Option<&str>,
        body: &str,
    ) -> Value {
        let mut params = json!({ "party": { "table": party.table, "key": party.key }, "channel": channel, "body": body });
        if let Some(subject) = subject {
            params["subject"] = json!(subject);
        }
        params
    }

    /// The inbound path's send approval (D157's `with_approval`): a send as
    /// an agent run is `send_message`, class messageParty, of its party,
    /// channel, subject and body.
    pub fn send_approval(&self) -> Arc<dyn SendApproval> {
        Arc::new(SendPolicy {
            agents: Arc::downgrade(&self.inner),
        })
    }
}

/// A grant of the party a run acts for: its target's type and key, and the
/// paths its role reaches.
type PartyReach = (String, String, BTreeMap<String, Vec<String>>);

/// Where an agent stands.
pub(super) struct Standing {
    pub paused: Option<String>,
    pub agent_capped: bool,
    pub total_capped: bool,
}

/// The authenticator of a run's token ([`Agents::authenticator`]).
pub struct RunAuthenticator {
    agents: Agents,
    grants: StoreGrants,
    inner: Option<Arc<dyn Authenticator>>,
}

#[async_trait]
impl Authenticator for RunAuthenticator {
    async fn authenticate(
        &self,
        request: &http::request::Parts,
    ) -> Result<Option<Principal>, crate::ApiError> {
        match crate::auth::bearer_token(&request.headers) {
            Some(token) if token.starts_with(GRANT_TOKEN_PREFIX) => {
                match self.agents.principal_of(token).await {
                    Ok(Some(principal)) => Ok(Some(principal)),
                    // A grant's token that is no running run's is any other
                    // grant's.
                    _ => self.grants.authenticate(request).await,
                }
            }
            _ => match &self.inner {
                Some(inner) => inner.authenticate(request).await,
                None => Ok(None),
            },
        }
    }

    fn permits(&self, held: &[String], required: &[String]) -> bool {
        match &self.inner {
            Some(inner) => inner.permits(held, required),
            None => crate::auth::has_any_permission(held, required),
        }
    }

    async fn called(
        &self,
        principal: Option<&Principal>,
        operation: &str,
        method: &http::Method,
    ) -> Result<(), crate::ApiError> {
        match &self.inner {
            Some(inner) => inner.called(principal, operation, method).await,
            None => Ok(()),
        }
    }
}

struct Decide {
    agents: Weak<Inner>,
    state: &'static str,
    describe: &'static str,
}

#[async_trait]
impl ReplyAction for Decide {
    fn permission(&self) -> Option<&str> {
        Some(permissions::APPROVE)
    }

    fn describe(&self) -> Option<&str> {
        Some(self.describe)
    }

    async fn run(&self, ctx: &ReplyContext<'_, '_>) -> Result<Value, BoxError> {
        let inner = self
            .agents
            .upgrade()
            .ok_or("the agent dispatcher is gone")?;
        let agents = Agents { inner };
        let id = ctx
            .subject_key()
            .ok_or("the notice is about no approval")?
            .to_owned();
        agents
            .decide_with(
                Some(ctx.repositories.db()),
                ctx.principal,
                &id,
                self.state,
                ctx.channel,
            )
            .await
            .map_err(|err| -> BoxError { Box::new(err) })?;
        Ok(json!({ "approval": id }))
    }
}

struct SendPolicy {
    agents: Weak<Inner>,
}

#[async_trait]
impl SendApproval for SendPolicy {
    async fn approve(&self, input: &SendInput) -> Result<SendDecision, BoxError> {
        let inner = self
            .agents
            .upgrade()
            .ok_or("the agent dispatcher is gone")?;
        let agents = Agents { inner };
        let params = Agents::send_params(
            &input.party,
            &input.channel,
            input.subject.as_deref(),
            &input.body,
        );
        Ok(agents
            .send_policy(Some(&input.as_), "send_message", &params)
            .await?)
    }
}

/// The row's Comments, as an ask's thread (D158).
struct CommentsThread {
    dispatcher: Caller,
}

#[async_trait]
impl AskThread for CommentsThread {
    async fn post(
        &self,
        repositories: &TxRepositories<'_, '_>,
        row: &RowRef,
        question: &str,
        principal: Option<&str>,
    ) -> Result<String, BoxError> {
        let mut params = json!({ "body": question });
        if let Some(principal) = principal {
            params["mentions"] = json!([principal]);
        }
        let posted = repositories
            .table(&row.table)?
            .operation(
                "comments",
                "post",
                &key(&row.key),
                &params,
                &WriteOptions::default(),
            )
            .await
            .map_err(|err| -> BoxError {
                Box::new(AgentsError::new(
                    400,
                    "no_thread",
                    format!("{} keeps no Comments to ask in: {err}", row.table),
                ))
            })?;
        let id = posted
            .get("id")
            .or_else(|| posted.get("message").and_then(|m| m.get("id")))
            .or_else(|| posted.get("message"))
            .cloned()
            .unwrap_or(Value::Null);
        Ok(match id {
            Value::String(s) => s,
            Value::Null => String::new(),
            other => other.to_string(),
        })
    }

    async fn answered(
        &self,
        repositories: &TxRepositories<'_, '_>,
        row: &RowRef,
        message: &str,
        run: &str,
    ) -> Result<bool, BoxError> {
        let _ = &self.dispatcher;
        let thread: i64 = message.parse().unwrap_or(0);
        let listed = repositories
            .table(&row.table)?
            .operation(
                "comments",
                "listMessages",
                &key(&row.key),
                &json!({ "thread": thread, "limit": 100 }),
                &WriteOptions::default(),
            )
            .await;
        let Ok(listed) = listed else {
            return Ok(false);
        };
        let items = listed
            .get("items")
            .and_then(Value::as_array)
            .cloned()
            .unwrap_or_default();
        Ok(items.iter().any(|m| {
            as_number(m.get("id")) != thread
                && m.get("author").map(|a| match a {
                    Value::String(s) => s.clone(),
                    other => other.to_string(),
                }) != Some(run.to_owned())
        }))
    }
}
