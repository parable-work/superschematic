//! The notifier over a database's repositories on either dialect (D153,
//! D191, #618), the TypeScript runtime's `createNotifier`: raising, routing, delivering,
//! escalating and replies, over the notifier's tables, the channel
//! identities and the dispatch queue its database declares.

use std::collections::{BTreeMap, BTreeSet, HashMap};
use std::fmt;
use std::sync::{Arc, Mutex, PoisonError};
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use async_trait::async_trait;
use serde::{Deserialize, Serialize};
use serde_json::{json, Value};

use super::super::parties::identity_lapsed;
#[cfg(feature = "postgres")]
use super::super::postgres::deadpool_postgres;
use super::super::store::{
    Db, DbFuture, IntoDb, PartQueue, PgClient, PoolTransaction, Store, StoreRepositories,
};
use super::super::{
    AccessCaller, Caller, Definition, Error, EventDefinition, FindOptions, OrderBy, Values,
    WriteOptions, ACCESS_ROLES,
};
use super::replies::{ReplyAction, ReplyContext};
use super::{
    code_hash, default_address_kind, looks_like_code, narrowed_permissions, normalise_address,
    quiet_delay_seconds, read_reply, reply_allows, same_id, CODE_ATTEMPTS, CODE_TTL_SECONDS,
    DEFAULT_DELIVERY_RETENTION_DAYS, DEFAULT_DELIVERY_TIMEOUT_SECONDS, DIGEST, HEALTH_TTL_MS,
    INAPP, NORMAL, RECORD, UNDO_VERB, URGENT,
};
use crate::access::{reaches_of, ActiveGrantsFilter};
use crate::auth::{covers, GrantReach, Principal, PrincipalKind};
use crate::providers::{
    time as wire_time, ChannelAdapter, OutboundMessage, Providers, RuntimeInterface, WebhookRequest,
};
use crate::queue::{Claim, QueueDefinition, QueueError, Slot};
use crate::worker::WorkerQueue;
use crate::BoxError;

/// The refusal or failure of a notifier call.
#[derive(Debug)]
pub enum NotifierError {
    /// A call the notifier refuses: an unknown event kind, a table the
    /// database lacks, a notice that is not the caller's.
    Refused(String),
    /// A repository's read or write failed.
    Orm(Error),
    /// The dispatch queue's write failed.
    Queue(QueueError),
    /// The database or a channel failed.
    Failed(String),
}

impl fmt::Display for NotifierError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            NotifierError::Refused(message) | NotifierError::Failed(message) => {
                f.write_str(message)
            }
            NotifierError::Orm(err) => write!(f, "{err}"),
            NotifierError::Queue(err) => write!(f, "{err}"),
        }
    }
}

impl std::error::Error for NotifierError {}

impl From<Error> for NotifierError {
    fn from(err: Error) -> Self {
        NotifierError::Orm(err)
    }
}

impl From<QueueError> for NotifierError {
    fn from(err: QueueError) -> Self {
        NotifierError::Queue(err)
    }
}

#[cfg(feature = "postgres")]
impl From<deadpool_postgres::tokio_postgres::Error> for NotifierError {
    fn from(err: deadpool_postgres::tokio_postgres::Error) -> Self {
        NotifierError::Failed(crate::error::chain(&err))
    }
}

#[cfg(feature = "postgres")]
impl From<deadpool_postgres::PoolError> for NotifierError {
    fn from(err: deadpool_postgres::PoolError) -> Self {
        NotifierError::Failed(format!("the database is unreachable: {err}"))
    }
}

fn refused(message: impl Into<String>) -> NotifierError {
    NotifierError::Refused(message.into())
}

fn failed(err: impl fmt::Display) -> NotifierError {
    NotifierError::Failed(err.to_string())
}

type Result<T, E = NotifierError> = std::result::Result<T, E>;

/// A principal a notice is for, as the notifier names it.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct RecipientRef {
    /// `user`, `party` or `agent`.
    pub kind: String,
    pub id: String,
    /// A party's table.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub table: Option<String>,
}

impl RecipientRef {
    /// A user, by its id.
    pub fn user(id: impl Into<String>) -> Self {
        Self {
            kind: "user".to_owned(),
            id: id.into(),
            table: None,
        }
    }

    fn principal_kind(&self) -> PrincipalKind {
        match self.kind.as_str() {
            "party" => PrincipalKind::Party,
            "agent" => PrincipalKind::Agent,
            _ => PrincipalKind::User,
        }
    }
}

/// An event as [`Notifier::raise`] takes it.
#[derive(Clone, Debug, Default)]
pub struct RaiseInput {
    /// The table whose row raised it, by its type's name; none for an
    /// event of no record (`channel.down`).
    pub table: Option<String>,
    /// The row's key.
    pub key: Option<String>,
    pub kind: String,
    /// What it says beyond the payload fields its `@event` names, which
    /// raise reads from the row.
    pub payload: Values,
    /// An urgency in place of the event's default.
    pub urgency: Option<String>,
    /// The event's id, so a retried handler raises it once; a new one
    /// unless set.
    pub id: Option<String>,
}

impl RaiseInput {
    /// The event `kind` of the row `key` of `table`.
    pub fn of(table: impl Into<String>, key: impl Into<String>, kind: impl Into<String>) -> Self {
        Self {
            table: Some(table.into()),
            key: Some(key.into()),
            kind: kind.into(),
            ..Self::default()
        }
    }
}

/// An event as it travels on the dispatch queue.
#[derive(Clone, Debug, Default, Serialize, Deserialize)]
struct RaisedEvent {
    id: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    table: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    key: Option<String>,
    kind: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    urgency: Option<String>,
    #[serde(default)]
    payload: Values,
    #[serde(default)]
    at: i64,
}

/// A message of the dispatch queue: a step and what it is of.
#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct NoticeWork {
    /// `route`, `deliver`, `timeout`, `escalate`, `digest` or `brief`.
    pub step: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub event: Option<Value>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub notice: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub delivery: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub recipient: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub subscription: Option<String>,
}

impl NoticeWork {
    fn step(step: &str) -> Self {
        Self {
            step: step.to_owned(),
            ..Self::default()
        }
    }
}

/// The input of [`Notifier::reply`]: a message an inbound adapter
/// received.
#[derive(Clone, Debug, Default)]
pub struct ReplyInput {
    /// The channel kind it came on: `sms`, `email`, `chat`.
    pub channel: String,
    /// The slot whose adapter received it, which the echo goes back
    /// through; the first of its kind unless set.
    pub slot: Option<String>,
    pub from: String,
    pub body: String,
    pub received_at: Option<SystemTime>,
    pub provider_message_id: Option<String>,
}

impl ReplyInput {
    /// A reply of `body` from `from` on the channel kind `channel`.
    pub fn of(
        channel: impl Into<String>,
        from: impl Into<String>,
        body: impl Into<String>,
    ) -> Self {
        Self {
            channel: channel.into(),
            from: from.into(),
            body: body.into(),
            ..Self::default()
        }
    }
}

/// What a reply came to.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct ReplyOutcome {
    /// `done`, `refused`, `unknown`, `ambiguous`, `failed` or `verified`.
    pub outcome: String,
    /// The reply's row in the replies table.
    pub reply: Option<String>,
    pub notice: Option<String>,
    pub verb: Option<String>,
    /// What was echoed back to the sender.
    pub echo: Option<String>,
    pub detail: Option<String>,
    /// The principal it ran as; none for an unknown sender, which D157's
    /// triage takes.
    pub principal: Option<RecipientRef>,
}

/// What [`Notifier::add_identity`] takes.
#[derive(Clone, Debug)]
pub struct AddIdentity {
    /// `email`, `phone` or `handle`.
    pub kind: String,
    pub address: String,
    pub principal: RecipientRef,
    /// `entered` marks it entered by hand, which resolves at the lowest
    /// trust; `unverified` (none) waits for a code.
    pub trust: Option<String>,
}

/// What [`Notifier::send_direct`] takes: a message to an address on a
/// channel kind with no notice.
#[derive(Clone, Debug, Default)]
pub struct SendDirect {
    pub channel: String,
    pub to: String,
    pub subject: Option<String>,
    pub body: String,
    pub recipient: Option<String>,
    pub purpose: Option<String>,
}

/// The health of one channel adapter, as [`Notifier::check_health`]
/// answers it.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ChannelHealthOf {
    pub slot: String,
    pub kind: String,
    pub ok: bool,
    pub detail: Option<String>,
}

/// The permissions a user holds, when the app reads them itself; the
/// notifier reads the `UserRoleGrant` and UserRole tables unless given one.
#[async_trait]
pub trait PermissionsOf: Send + Sync {
    async fn permissions_of(&self, recipient: &RecipientRef) -> Result<Vec<String>, BoxError>;
}

/// The core's expansion of a recurrence rule (RulesCore.expand, D152),
/// which digests and briefs are dated by: given `{ anchor, rule, start,
/// timeZone, from, limit }`, the due instants of its occurrences, in
/// milliseconds since the epoch.
pub type Expand = Arc<dyn Fn(&Value) -> Vec<i64> + Send + Sync>;

/// What a notice says on a channel.
pub type Render = Arc<dyn Fn(&Values, &str) -> (String, String) + Send + Sync>;

/// The clock.
pub type Clock = Arc<dyn Fn() -> SystemTime + Send + Sync>;

/// The notifier's tables, by their types' names.
#[derive(Clone, Debug)]
struct Parts {
    inbox: String,
    deliveries: String,
    routes: String,
    preferences: Option<String>,
    subscriptions: Option<String>,
    trust: Option<String>,
    replies: Option<String>,
    identities: Option<String>,
}

#[derive(Clone)]
struct Channel {
    slot: String,
    kind: String,
    adapter: Arc<dyn ChannelAdapter>,
    status: bool,
    inbound: bool,
}

#[derive(Clone, Debug)]
struct Health {
    ok: bool,
    detail: Option<String>,
    at: SystemTime,
}

struct Preference {
    channels: Vec<String>,
    quiet_start: Option<String>,
    quiet_end: Option<String>,
    time_zone: String,
    digest: Option<String>,
}

struct Inner {
    tables: Store,
    queue: PartQueue<NoticeWork>,
    definition: &'static Definition,
    dispatch: QueueDefinition,
    parts: Parts,
    providers: Arc<Providers>,
    replies: BTreeMap<String, BTreeMap<String, Arc<dyn ReplyAction>>>,
    default_channels: Vec<String>,
    address_kinds: BTreeMap<String, String>,
    render: Option<Render>,
    permissions_of: Option<Arc<dyn PermissionsOf>>,
    expand: Option<Expand>,
    timeout_seconds: u64,
    retention_days: u64,
    now: Option<Clock>,
    health: Mutex<HashMap<String, Health>>,
    agents: Option<QueueDefinition>,
}

/// The notifier (D153). Build one with [`Notifier::new`] and its `with_`
/// methods, share it (it is cheap to clone), and run its dispatch queue's
/// worker with [`Notifier::worker_queue`] and [`Notifier::handle`].
#[derive(Clone)]
pub struct Notifier {
    inner: Arc<Inner>,
}

impl fmt::Debug for Notifier {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("Notifier")
            .field("parts", &self.inner.parts)
            .field("dispatch", &self.inner.dispatch.name)
            .finish_non_exhaustive()
    }
}

fn obj(value: Value) -> Values {
    match value {
        Value::Object(map) => map,
        _ => Values::new(),
    }
}

fn eq(value: impl Into<Value>) -> Value {
    json!({ "eq": value.into() })
}

fn text<'v>(row: &'v Values, member: &str) -> Option<&'v str> {
    row.get(member).and_then(Value::as_str)
}

fn string(row: &Values, member: &str) -> String {
    match row.get(member) {
        Some(Value::String(text)) => text.clone(),
        Some(Value::Null) | None => String::new(),
        Some(other) => other.to_string(),
    }
}

fn strings(value: Option<&Value>) -> Option<Vec<String>> {
    value.and_then(Value::as_array).map(|items| {
        items
            .iter()
            .filter_map(Value::as_str)
            .map(str::to_owned)
            .collect()
    })
}

fn number(row: &Values, member: &str) -> i64 {
    match row.get(member) {
        Some(Value::Number(n)) => n.as_i64().unwrap_or(0),
        Some(Value::String(s)) => s.parse().unwrap_or(0),
        _ => 0,
    }
}

fn at_text(time: SystemTime) -> Value {
    Value::String(super::super::rfc3339_millis(time))
}

fn millis(time: SystemTime) -> i64 {
    time.duration_since(UNIX_EPOCH)
        .map_or(0, |d| i64::try_from(d.as_millis()).unwrap_or(i64::MAX))
}

fn many(limit: u32) -> FindOptions {
    FindOptions {
        limit: Some(limit),
        skip_count: true,
        ..FindOptions::default()
    }
}

fn ordered(limit: u32, order: OrderBy) -> FindOptions {
    FindOptions {
        order_by: vec![order],
        ..many(limit)
    }
}

fn none() -> WriteOptions {
    WriteOptions::default()
}

fn key(id: &str) -> Value {
    Value::String(id.to_owned())
}

fn ended(state: &str) -> bool {
    matches!(state, "failed" | "acknowledged" | "read")
}

fn state_order(state: &str) -> u8 {
    match state {
        "queued" => 0,
        "sent" => 1,
        "delivered" => 2,
        "read" => 3,
        "failed" => 4,
        _ => 5,
    }
}

fn wire_state(state: crate::providers::DeliveryState) -> &'static str {
    use crate::providers::DeliveryState as S;
    match state {
        S::Queued | S::Sent => "sent",
        S::Delivered => "delivered",
        S::Read => "read",
        S::Failed => "failed",
    }
}

fn random_code() -> String {
    use ring::rand::SecureRandom as _;
    let mut bytes = [0u8; 4];
    let _ = ring::rand::SystemRandom::new().fill(&mut bytes);
    format!("{:06}", u32::from_be_bytes(bytes) % 1_000_000)
}

/// The id of a new event, hyphenated, as TypeScript's crypto.randomUUID.
fn new_id() -> String {
    uuid::Uuid::new_v4().hyphenated().to_string()
}

/// A behavior's event (D104): its kind is the use's member, a dot and the
/// behavior's kind.
fn behavior_event(
    table: &str,
    member: &str,
    kind: &str,
    key: &str,
    payload: Option<&Value>,
) -> RaiseInput {
    RaiseInput {
        table: Some(table.to_owned()),
        key: Some(key.to_owned()),
        kind: format!("{member}.{kind}"),
        payload: payload.cloned().map(obj).unwrap_or_default(),
        ..RaiseInput::default()
    }
}

/// `reference.cleared` on the row that held a cleared reference (D154).
fn cleared_event(message: &Value) -> RaiseInput {
    let member = |name: &str| {
        message
            .get(name)
            .and_then(Value::as_str)
            .unwrap_or_default()
            .to_owned()
    };
    let mut payload = Values::new();
    payload.insert("targetTable".to_owned(), json!(member("targetTable")));
    payload.insert("targetKey".to_owned(), json!(member("targetKey")));
    payload.insert("role".to_owned(), json!(member("role")));
    RaiseInput {
        table: Some(member("holderTable")),
        key: Some(member("holderKey")),
        kind: "reference.cleared".to_owned(),
        payload,
        ..RaiseInput::default()
    }
}

impl Notifier {
    /// The notifier over `tables`, the database's repositories, with
    /// `dispatch`, its `@notifier("dispatch")` queue's descriptor. It
    /// refuses a database without the inbox, the deliveries or the routes.
    pub fn new(tables: impl Into<Store>, dispatch: QueueDefinition) -> Result<Self> {
        let tables: Store = tables.into();
        let definition = tables.definition()?;
        let queue = PartQueue::of(&tables, dispatch.clone())?;
        let part = |name: &str| {
            definition
                .tables
                .iter()
                .find(|table| table.notifier.as_deref() == Some(name))
                .map(|table| table.name.clone())
        };
        let required = |name: &str| {
            part(name).ok_or_else(|| {
                refused(format!(
                    "the notifier: the database has no @notifier(\"{name}\") table"
                ))
            })
        };
        let parts = Parts {
            inbox: required("inbox")?,
            deliveries: required("deliveries")?,
            routes: required("routes")?,
            preferences: part("preferences"),
            subscriptions: part("subscriptions"),
            trust: part("channelTrust"),
            replies: part("replies"),
            identities: part("channelIdentities"),
        };
        Ok(Self {
            inner: Arc::new(Inner {
                tables,
                queue,
                definition,
                dispatch,
                parts,
                providers: Arc::new(Providers::none()),
                replies: BTreeMap::new(),
                default_channels: vec![INAPP.to_owned()],
                address_kinds: BTreeMap::new(),
                render: None,
                permissions_of: None,
                expand: None,
                timeout_seconds: DEFAULT_DELIVERY_TIMEOUT_SECONDS,
                retention_days: DEFAULT_DELIVERY_RETENTION_DAYS,
                now: None,
                health: Mutex::new(HashMap::new()),
                agents: None,
            }),
        })
    }

    fn edit(mut self, change: impl FnOnce(&mut Inner)) -> Self {
        if let Some(inner) = Arc::get_mut(&mut self.inner) {
            change(inner);
        }
        self
    }

    /// The notifier sending on the channel slots `providers` configures
    /// (D159). Each `with_` method is for a notifier not yet cloned.
    #[must_use]
    pub fn with_providers(self, providers: Arc<Providers>) -> Self {
        self.edit(|inner| inner.providers = providers)
    }

    /// The notifier running `action` for a reply of `verb` to a notice of
    /// the event `kind` (or `*` for every kind).
    #[must_use]
    pub fn with_reply(
        self,
        kind: impl Into<String>,
        verb: impl Into<String>,
        action: Arc<dyn ReplyAction>,
    ) -> Self {
        let (kind, verb) = (kind.into(), verb.into());
        self.edit(|inner| {
            inner.replies.entry(kind).or_default().insert(verb, action);
        })
    }

    /// The channels a recipient with no preference row of any kind is
    /// sent on; `inapp` alone unless set.
    #[must_use]
    pub fn with_default_channels(self, channels: Vec<String>) -> Self {
        self.edit(|inner| inner.default_channels = channels)
    }

    /// The kind of address a channel kind sends to, beside email to email
    /// and sms to phone.
    #[must_use]
    pub fn with_address_kind(self, channel: impl Into<String>, kind: impl Into<String>) -> Self {
        let (channel, kind) = (channel.into(), kind.into());
        self.edit(|inner| {
            inner.address_kinds.insert(channel, kind);
        })
    }

    /// What a notice says on a channel: its subject and body.
    #[must_use]
    pub fn with_render(self, render: Render) -> Self {
        self.edit(|inner| inner.render = Some(render))
    }

    /// The permissions a user holds, read by the app.
    #[must_use]
    pub fn with_permissions_of(self, permissions_of: Arc<dyn PermissionsOf>) -> Self {
        self.edit(|inner| inner.permissions_of = Some(permissions_of))
    }

    /// The core's expansion of a recurrence rule, which digests and briefs
    /// are dated by.
    #[must_use]
    pub fn with_expand(self, expand: Expand) -> Self {
        self.edit(|inner| inner.expand = Some(expand))
    }

    /// How long a delivery an adapter reports on may go unreported.
    #[must_use]
    pub fn with_delivery_timeout(self, seconds: u64) -> Self {
        self.edit(|inner| inner.timeout_seconds = seconds)
    }

    /// How many days a delivery that ended is kept.
    #[must_use]
    pub fn with_delivery_retention_days(self, days: u64) -> Self {
        self.edit(|inner| inner.retention_days = days)
    }

    /// The agent dispatcher's request queue (D158, D193), its
    /// `@agents("requests")` queue's descriptor: a route's `{ agent }`
    /// recipient puts a run request about the event's record on it, in the
    /// routing transaction. Without it, such a recipient is logged and
    /// reaches no one.
    #[must_use]
    pub fn with_agents(self, requests: QueueDefinition) -> Self {
        self.edit(|inner| inner.agents = Some(requests))
    }

    /// The clock; the system's unless set, for a test.
    #[must_use]
    pub fn with_clock(self, now: Clock) -> Self {
        self.edit(|inner| inner.now = Some(now))
    }

    fn now(&self) -> SystemTime {
        self.inner
            .now
            .as_ref()
            .map_or_else(SystemTime::now, |now| now())
    }

    fn store(&self) -> &Store {
        &self.inner.tables
    }

    /// The dispatch queue's descriptor.
    pub fn dispatch(&self) -> &QueueDefinition {
        &self.inner.dispatch
    }

    /// The repositories over `client` as the system, which reads every
    /// row, with the keys of the encrypted fields.
    fn sys<'c, C: PgClient>(&'c self, client: Db<'c, C>) -> Result<StoreRepositories<'c, C>> {
        let mut repositories = client.repositories(self.inner.definition)?.as_system();
        if let Some(keys) = self.inner.tables.keys() {
            repositories = repositories.with_keys(keys.as_ref());
        }
        Ok(repositories)
    }

    /// The repositories over `client` as `reader`, keeping to what it
    /// reads (D156).
    fn as_reader<'c, C: PgClient>(
        &'c self,
        client: Db<'c, C>,
        reader: &Principal,
    ) -> Result<StoreRepositories<'c, C>> {
        let mut repositories = client
            .repositories(self.inner.definition)?
            .acting(Some(reader.subject.clone()))
            .with_access(AccessCaller::of(Some(reader)));
        if let Some(keys) = self.inner.tables.keys() {
            repositories = repositories.with_keys(keys.as_ref());
        }
        Ok(repositories)
    }

    fn definition_of(&self, table: Option<&str>) -> Option<&'static super::super::Table> {
        let table = table?;
        self.inner
            .definition
            .tables
            .iter()
            .find(|t| t.name == table)
    }

    fn event_of(&self, table: Option<&str>, kind: &str) -> Option<&'static EventDefinition> {
        self.definition_of(table)?
            .events
            .iter()
            .find(|event| event.kind == kind)
    }

    fn address_kind_of(&self, channel: &str) -> String {
        self.inner
            .address_kinds
            .get(channel)
            .cloned()
            .unwrap_or_else(|| default_address_kind(channel).to_owned())
    }

    fn channel_kind_of_address(&self, kind: &str) -> String {
        if let Some((channel, _)) = self.inner.address_kinds.iter().find(|(_, a)| *a == kind) {
            return channel.clone();
        }
        match kind {
            "email" => "email".to_owned(),
            "phone" => "sms".to_owned(),
            other => other.to_owned(),
        }
    }

    /// The row `key` of `table`, read as the system.
    async fn row_of<C: PgClient>(
        &self,
        client: Db<'_, C>,
        table: &str,
        key: &str,
    ) -> Result<Option<Values>> {
        let def = self.inner.definition.table(table)?;
        let mut filter = Values::new();
        filter.insert(def.key.clone(), eq(key));
        Ok(self
            .sys(client)?
            .table(table)?
            .find_one(Some(&filter), &FindOptions::default())
            .await?)
    }

    // ---- channels ----

    fn channels(&self) -> Vec<Channel> {
        let mut out = Vec::new();
        for configured in self
            .inner
            .providers
            .configured(Some(RuntimeInterface::Channel))
        {
            match self.inner.providers.channel(&configured.slot) {
                Ok(adapter) => {
                    let operations = self
                        .inner
                        .providers
                        .operations(&configured.slot)
                        .map(<[String]>::to_vec)
                        .unwrap_or_default();
                    out.push(Channel {
                        kind: adapter.kind().to_owned(),
                        slot: configured.slot.clone(),
                        adapter,
                        status: operations.iter().any(|op| op == "status"),
                        inbound: operations.iter().any(|op| op == "inbound"),
                    });
                }
                Err(err) => {
                    tracing::warn!(slot = %configured.slot, detail = %err, "a channel adapter could not be made");
                }
            }
        }
        out
    }

    async fn health_of(&self, channel: &Channel) -> Result<(bool, Option<String>)> {
        let now = self.now();
        let cached = self
            .inner
            .health
            .lock()
            .unwrap_or_else(PoisonError::into_inner)
            .get(&channel.slot)
            .cloned();
        if let Some(cached) = &cached {
            let age = now.duration_since(cached.at).unwrap_or_default();
            if age < Duration::from_millis(HEALTH_TTL_MS) {
                return Ok((cached.ok, cached.detail.clone()));
            }
        }
        let (ok, detail) = match channel.adapter.health().await {
            Ok(answer) => (answer.ok, answer.detail),
            Err(err) => (false, Some(err.to_string())),
        };
        self.inner
            .health
            .lock()
            .unwrap_or_else(PoisonError::into_inner)
            .insert(
                channel.slot.clone(),
                Health {
                    ok,
                    detail: detail.clone(),
                    at: now,
                },
            );
        if !ok && cached.as_ref().is_none_or(|c| c.ok) {
            self.alert_down(channel, detail.as_deref()).await?;
        }
        Ok((ok, detail))
    }

    /// A healthy channel of `kind`, preferring `slot`.
    async fn channel_of_kind(&self, kind: &str, slot: Option<&str>) -> Result<Option<Channel>> {
        let mut list: Vec<Channel> = self
            .channels()
            .into_iter()
            .filter(|c| c.kind == kind)
            .collect();
        list.sort_by_key(|c| Some(c.slot.as_str()) != slot);
        for channel in list {
            if self.health_of(&channel).await?.0 {
                return Ok(Some(channel));
            }
        }
        Ok(None)
    }

    async fn alert_down(&self, channel: &Channel, detail: Option<&str>) -> Result<()> {
        tracing::warn!(slot = %channel.slot, kind = %channel.kind, detail = detail.unwrap_or_default(), "channel down");
        // One alert while it is down: an alert of the slot not within the
        // hour stands.
        let client = self.store().client().await?;
        let hour_ago = self.now() - Duration::from_secs(3600);
        let mut filter = Values::new();
        filter.insert("eventKind".to_owned(), eq("channel.down"));
        filter.insert("subjectKey".to_owned(), eq(channel.slot.as_str()));
        filter.insert("createdAt".to_owned(), json!({ "gt": at_text(hour_ago) }));
        let recent = self
            .sys(client.db())?
            .table(&self.inner.parts.inbox)?
            .find_one(Some(&filter), &FindOptions::default())
            .await?;
        if recent.is_some() {
            return Ok(());
        }
        drop(client);
        let mut payload = Values::new();
        payload.insert("slot".to_owned(), json!(channel.slot));
        payload.insert("channel".to_owned(), json!(channel.kind));
        if let Some(detail) = detail {
            payload.insert("detail".to_owned(), json!(detail));
        }
        let mut client = self.store().client().await?;
        let tx = client.transaction().await?;
        self.raise_in(
            tx.db(),
            RaiseInput {
                kind: "channel.down".to_owned(),
                key: Some(channel.slot.clone()),
                urgency: Some(URGENT.to_owned()),
                payload,
                ..RaiseInput::default()
            },
            false,
        )
        .await?;
        tx.commit().await?;
        Ok(())
    }

    // ---- raising ----

    /// Raises `input` in `client`'s transaction, the transaction of the
    /// change that caused it, and answers its id. A table's own event is one
    /// its `@event` declares; a kind it does not declare is refused, and the
    /// caller's transaction with it. `client` is a Postgres client or a
    /// [`Db`]; in a caller's own rusqlite transaction, use
    /// [`Notifier::raise_sqlite`].
    pub fn raise<'a, 'c: 'a>(
        &'a self,
        client: impl IntoDb<'c>,
        input: RaiseInput,
    ) -> DbFuture<'a, Result<String>> {
        let client = client.into_db();
        Box::pin(async move { self.raise_in(client, input, false).await })
    }

    /// Raises a behavior's event (D104), as its behavior worker's Workers
    /// method hands it: its kind is the use's member, a dot and the
    /// behavior's kind, which needs no declaration.
    pub fn from_behavior_event<'a, 'c: 'a>(
        &'a self,
        client: impl IntoDb<'c>,
        table: &str,
        member: &str,
        kind: &str,
        key: &str,
        payload: Option<&Value>,
    ) -> DbFuture<'a, Result<String>> {
        let client = client.into_db();
        let input = behavior_event(table, member, kind, key, payload);
        Box::pin(async move { self.raise_in(client, input, true).await })
    }

    /// Raises `reference.cleared` on the row that held a reference a
    /// target's delete cleared (D154), from the cleared queue's message:
    /// its `holderTable`, `holderKey`, `targetTable`, `targetKey` and
    /// `role`.
    pub fn cleared<'a, 'c: 'a>(
        &'a self,
        client: impl IntoDb<'c>,
        message: &Value,
    ) -> DbFuture<'a, Result<String>> {
        let client = client.into_db();
        let input = cleared_event(message);
        Box::pin(async move { self.raise_in(client, input, true).await })
    }

    /// [`Notifier::raise`] in the caller's own transaction on a SQLite
    /// file (#618): `connection` is the rusqlite transaction of the change
    /// that caused it, which the event's message commits with.
    #[cfg(feature = "sqlite")]
    pub fn raise_sqlite(
        &self,
        connection: &super::super::sqlite::rusqlite::Connection,
        input: RaiseInput,
    ) -> Result<String> {
        self.raise_on(connection, input, false)
    }

    /// [`Notifier::from_behavior_event`] in the caller's own transaction
    /// on a SQLite file: the behavior worker's, on its connection (D116).
    #[cfg(feature = "sqlite")]
    pub fn from_behavior_event_sqlite(
        &self,
        connection: &super::super::sqlite::rusqlite::Connection,
        table: &str,
        member: &str,
        kind: &str,
        key: &str,
        payload: Option<&Value>,
    ) -> Result<String> {
        self.raise_on(
            connection,
            behavior_event(table, member, kind, key, payload),
            true,
        )
    }

    /// [`Notifier::cleared`] in the caller's own transaction on a SQLite
    /// file.
    #[cfg(feature = "sqlite")]
    pub fn cleared_sqlite(
        &self,
        connection: &super::super::sqlite::rusqlite::Connection,
        message: &Value,
    ) -> Result<String> {
        self.raise_on(connection, cleared_event(message), true)
    }

    #[cfg(feature = "sqlite")]
    fn raise_on(
        &self,
        connection: &super::super::sqlite::rusqlite::Connection,
        input: RaiseInput,
        undeclared: bool,
    ) -> Result<String> {
        let row = match self.raise_row(&input, undeclared)? {
            Some((table, key)) => {
                let def = self.inner.definition.table(&table)?;
                let mut filter = Values::new();
                filter.insert(def.key.clone(), eq(key.as_str()));
                let mut repositories =
                    super::super::sqlite::Repositories::new(connection, self.inner.definition)?
                        .as_system();
                if let Some(keys) = self.inner.tables.keys() {
                    repositories = repositories.with_keys(keys.as_ref());
                }
                repositories
                    .table(&table)?
                    .find_one(Some(&filter), &FindOptions::default())?
            }
            None => None,
        };
        let (work, id) = self.raise_work(input, row)?;
        crate::queue::sqlite::enqueue_sqlite_in(
            connection,
            &self.inner.dispatch,
            &work,
            Duration::ZERO,
        )?;
        Ok(id)
    }

    pub(crate) async fn raise_in<C: PgClient>(
        &self,
        client: Db<'_, C>,
        input: RaiseInput,
        undeclared: bool,
    ) -> Result<String> {
        let row = match self.raise_row(&input, undeclared)? {
            Some((table, key)) => self.row_of(client, &table, &key).await?,
            None => None,
        };
        let (work, id) = self.raise_work(input, row)?;
        client
            .enqueue(&self.inner.dispatch, &work, Duration::ZERO)
            .await?;
        Ok(id)
    }

    /// Refuses an event of no table of the database, or one its table
    /// does not declare (unless `undeclared`), and answers the row whose
    /// members its payload reads, when it reads any.
    fn raise_row(&self, input: &RaiseInput, undeclared: bool) -> Result<Option<(String, String)>> {
        let definition = self.definition_of(input.table.as_deref());
        if let Some(table) = &input.table {
            if definition.is_none() {
                return Err(refused(format!(
                    "raise: {table} is no table of the database"
                )));
            }
        }
        let declared = self.event_of(input.table.as_deref(), &input.kind);
        if let Some(table) = &input.table {
            if declared.is_none() && !undeclared {
                return Err(refused(format!(
                    "raise: {table} declares no event {}; declare it with @event",
                    input.kind
                )));
            }
        }
        Ok(match (declared, &input.key, definition) {
            (Some(declared), Some(key), Some(def)) if !declared.payload.is_empty() => {
                Some((def.name.clone(), key.clone()))
            }
            _ => None,
        })
    }

    /// The route step of `input`, its payload's declared members read
    /// from `row`, and the event's id.
    fn raise_work(&self, input: RaiseInput, row: Option<Values>) -> Result<(NoticeWork, String)> {
        let mut payload = Values::new();
        if let Some(declared) = self.event_of(input.table.as_deref(), &input.kind) {
            if input.key.is_some() {
                for field in &declared.payload {
                    payload.insert(
                        field.clone(),
                        row.as_ref()
                            .and_then(|row| row.get(field))
                            .cloned()
                            .unwrap_or(Value::Null),
                    );
                }
            }
        }
        payload.extend(input.payload);
        let event = RaisedEvent {
            id: input.id.unwrap_or_else(new_id),
            table: input.table,
            key: input.key,
            kind: input.kind,
            urgency: input.urgency,
            payload,
            at: millis(self.now()),
        };
        let id = same_id(&event.id);
        let mut work = NoticeWork::step("route");
        work.event = Some(serde_json::to_value(&event).map_err(failed)?);
        Ok((work, id))
    }

    // ---- recipients ----

    fn roles_table(&self) -> Option<String> {
        let definition = self.inner.definition;
        definition
            .tables
            .iter()
            .find(|t| t.name == "UserRoleGrant")
            .and_then(|grants| grants.relation_target("role"))
            .map(str::to_owned)
            .or_else(|| {
                definition
                    .access_table(ACCESS_ROLES)
                    .map(|t| t.name.clone())
            })
    }

    fn has_table(&self, name: &str) -> bool {
        self.inner.definition.tables.iter().any(|t| t.name == name)
    }

    /// The permissions a user holds: its roles', through the
    /// `UserRoleGrant` table.
    async fn permissions_of(&self, recipient: &RecipientRef) -> Result<Vec<String>> {
        if let Some(of) = &self.inner.permissions_of {
            return of.permissions_of(recipient).await.map_err(failed);
        }
        if recipient.kind != "user" {
            return Ok(Vec::new());
        }
        let Some(roles) = self.roles_table() else {
            return Ok(Vec::new());
        };
        if !self.has_table("UserRoleGrant") {
            return Ok(Vec::new());
        }
        let client = self.store().client().await?;
        let sys = self.sys(client.db())?;
        let mut filter = Values::new();
        filter.insert("userId".to_owned(), eq(recipient.id.as_str()));
        let grants = sys
            .table("UserRoleGrant")?
            .find_many(Some(&filter), &many(1000))
            .await?;
        let mut out: Vec<String> = Vec::new();
        for grant in grants.rows {
            let mut by_id = Values::new();
            by_id.insert(
                "id".to_owned(),
                eq(grant.get("roleId").cloned().unwrap_or(Value::Null)),
            );
            let role = sys
                .table(&roles)?
                .find_one(Some(&by_id), &FindOptions::default())
                .await?;
            for permission in role
                .as_ref()
                .and_then(|role| strings(role.get("permissions")))
                .unwrap_or_default()
            {
                if !out.contains(&permission) {
                    out.push(permission);
                }
            }
        }
        Ok(out)
    }

    /// The principal a recipient reads as (D156): a user by its id, a
    /// party or an agent with its active grants.
    async fn reader_of(&self, recipient: &RecipientRef) -> Result<Principal> {
        let kind = recipient.principal_kind();
        if kind == PrincipalKind::User {
            return Ok(Principal::new(recipient.id.as_str(), Vec::<String>::new()));
        }
        let client = self.store().client().await?;
        let roles = self.roles_table();
        let filter = ActiveGrantsFilter {
            holder: Some(recipient.id.clone()),
            now: Some(self.now()),
            ..ActiveGrantsFilter::default()
        };
        let mut grants = Vec::new();
        for grant in client
            .db()
            .active_grants(self.inner.definition, &filter)
            .await?
        {
            let mut reaches: Vec<String> = Vec::new();
            if let Some(roles) = &roles {
                let mut by_name = Values::new();
                by_name.insert(
                    "name".to_owned(),
                    eq(grant.get("role").cloned().unwrap_or(Value::Null)),
                );
                let role = self
                    .sys(client.db())?
                    .table(roles)?
                    .find_one(Some(&by_name), &FindOptions::default())
                    .await?;
                reaches = role
                    .as_ref()
                    .and_then(|role| strings(role.get("reaches")))
                    .unwrap_or_default();
            }
            grants.push(GrantReach {
                id: string(&grant, "id"),
                target_type: string(&grant, "targetType"),
                target_id: string(&grant, "targetId"),
                reaches: reaches_of(reaches.iter().map(String::as_str)),
            });
        }
        Ok(Principal::new(recipient.id.as_str(), Vec::<String>::new())
            .of_kind(kind)
            .with_grants(grants))
    }

    /// Whether `recipient` reads the event's record, through its table's
    /// repository as the recipient.
    async fn reads<C: PgClient>(
        &self,
        client: Db<'_, C>,
        recipient: &RecipientRef,
        table: Option<&str>,
        key: Option<&str>,
    ) -> Result<bool> {
        let (Some(table), Some(key)) = (table, key) else {
            return Ok(true);
        };
        let Some(def) = self.definition_of(Some(table)) else {
            return Ok(false);
        };
        let reader = self.reader_of(recipient).await?;
        let repositories = self.as_reader(client, &reader)?;
        let mut filter = Values::new();
        filter.insert(def.key.clone(), eq(key));
        let found = match repositories.table(&def.name) {
            Ok(repository) => repository
                .find_one(Some(&filter), &FindOptions::default())
                .await
                .ok()
                .flatten(),
            Err(_) => None,
        };
        Ok(found.is_some())
    }

    /// The principals a path of the record names: each step a to-one
    /// relation, the last a field of ids.
    async fn follow_path<C: PgClient>(
        &self,
        client: Db<'_, C>,
        event: &RaisedEvent,
        path: &str,
    ) -> Result<Vec<String>> {
        let (Some(table), Some(key)) = (&event.table, &event.key) else {
            return Ok(Vec::new());
        };
        let Some(mut def) = self.definition_of(Some(table)) else {
            return Ok(Vec::new());
        };
        let mut row = self.row_of(client, &def.name, key).await?;
        let steps: Vec<&str> = path.split('.').collect();
        for (i, step) in steps.iter().enumerate() {
            let Some(current) = row.take() else {
                return Ok(Vec::new());
            };
            if i == steps.len() - 1 {
                return Ok(match current.get(*step) {
                    None | Some(Value::Null) => Vec::new(),
                    Some(Value::Array(items)) => items
                        .iter()
                        .map(|v| v.as_str().map_or_else(|| v.to_string(), str::to_owned))
                        .collect(),
                    Some(Value::String(s)) => vec![s.clone()],
                    Some(other) => vec![other.to_string()],
                });
            }
            let Some(relation) = def.relations.iter().find(|r| r.field == *step) else {
                return Ok(Vec::new());
            };
            let Some(target) = self.definition_of(Some(&relation.target)) else {
                return Ok(Vec::new());
            };
            def = target;
            row = match current.get(&format!("{step}Id")) {
                Some(Value::String(id)) => self.row_of(client, &def.name, id).await?,
                _ => None,
            };
        }
        Ok(Vec::new())
    }

    async fn recipients_of<C: PgClient>(
        &self,
        client: Db<'_, C>,
        event: &RaisedEvent,
        specs: &[Value],
    ) -> Result<Vec<RecipientRef>> {
        let mut out = Vec::new();
        for spec in specs {
            if let Some(path) = spec.get("path").and_then(Value::as_str) {
                for id in self.follow_path(client, event, path).await? {
                    out.push(RecipientRef::user(id));
                }
            } else if let Some(principal) = spec.get("principal").and_then(Value::as_str) {
                out.push(RecipientRef {
                    kind: spec
                        .get("kind")
                        .and_then(Value::as_str)
                        .unwrap_or("user")
                        .to_owned(),
                    id: principal.to_owned(),
                    table: None,
                });
            } else if let Some(role) = spec.get("role").and_then(Value::as_str) {
                let Some(roles) = self.roles_table() else {
                    continue;
                };
                if !self.has_table("UserRoleGrant") {
                    continue;
                }
                let sys = self.sys(client)?;
                let mut by_name = Values::new();
                by_name.insert("name".to_owned(), eq(role));
                let Some(row) = sys
                    .table(&roles)?
                    .find_one(Some(&by_name), &FindOptions::default())
                    .await?
                else {
                    continue;
                };
                let mut filter = Values::new();
                filter.insert(
                    "roleId".to_owned(),
                    eq(row.get("id").cloned().unwrap_or(Value::Null)),
                );
                let grants = sys
                    .table("UserRoleGrant")?
                    .find_many(Some(&filter), &many(10_000))
                    .await?;
                for grant in grants.rows {
                    out.push(RecipientRef::user(string(&grant, "userId")));
                }
            } else if let Some(party) = spec.get("party") {
                out.push(RecipientRef {
                    kind: "party".to_owned(),
                    id: party
                        .get("key")
                        .and_then(Value::as_str)
                        .unwrap_or_default()
                        .to_owned(),
                    table: party
                        .get("table")
                        .and_then(Value::as_str)
                        .map(str::to_owned),
                });
            }
        }
        let mut seen = BTreeSet::new();
        out.retain(|r| seen.insert(format!("{}:{}", r.kind, same_id(&r.id))));
        Ok(out)
    }

    // ---- preferences ----

    async fn preference_of<C: PgClient>(
        &self,
        client: Db<'_, C>,
        recipient: &RecipientRef,
        kind: &str,
        urgency: &str,
    ) -> Result<Preference> {
        let fallback = Preference {
            channels: self.inner.default_channels.clone(),
            quiet_start: None,
            quiet_end: None,
            time_zone: "UTC".to_owned(),
            digest: None,
        };
        let Some(preferences) = &self.inner.parts.preferences else {
            return Ok(fallback);
        };
        let sys = self.sys(client)?;
        let prefs = sys.table(preferences)?;
        let mut own = Values::new();
        own.insert("principal".to_owned(), eq(recipient.id.as_str()));
        let mut everyone = Values::new();
        everyone.insert("principal".to_owned(), json!({ "isNull": true }));
        let mut rows = prefs.find_many(Some(&own), &many(100)).await?.rows;
        rows.extend(prefs.find_many(Some(&everyone), &many(100)).await?.rows);
        let score = |row: &Values| -> i64 {
            let principal = if row.get("principal").is_some_and(|p| !p.is_null()) {
                4
            } else {
                0
            };
            let event = match text(row, "eventKind") {
                Some(k) if k == kind => 2,
                Some("*") => 0,
                _ => -100,
            };
            let urgent = match text(row, "urgency") {
                Some(u) if u == urgency => 1,
                Some("*") => 0,
                _ => -100,
            };
            principal + event + urgent
        };
        // The most specific row wins; of rows alike, the first read.
        let mut best: Option<(i64, &Values)> = None;
        for row in &rows {
            let s = score(row);
            if s >= 0 && best.is_none_or(|(b, _)| s > b) {
                best = Some((s, row));
            }
        }
        let Some((_, best)) = best else {
            return Ok(fallback);
        };
        Ok(Preference {
            channels: strings(best.get("channels")).unwrap_or(fallback.channels),
            quiet_start: text(best, "quietStart").map(str::to_owned),
            quiet_end: text(best, "quietEnd").map(str::to_owned),
            time_zone: text(best, "timeZone").unwrap_or("UTC").to_owned(),
            digest: text(best, "digest").map(str::to_owned),
        })
    }

    /// The wall time of `at` in `zone`: its minutes of the day and its
    /// second, and its date, from Postgres's zone database, or on SQLite
    /// from the runtime's bundled one.
    async fn wall_of<C: PgClient>(
        &self,
        client: Db<'_, C>,
        at: SystemTime,
        zone: &str,
    ) -> Result<(u32, u32, String)> {
        Ok(client.wall_of(at, zone).await?)
    }

    /// The channels a notice goes out on: its preference's, the in-app
    /// inbox left out, since every notice reaches it.
    fn outward(list: &[String]) -> Vec<String> {
        list.iter().filter(|c| *c != INAPP).cloned().collect()
    }

    // ---- notices and deliveries ----

    fn render(&self, notice: &Values, channel: &str) -> (String, String) {
        if let Some(render) = &self.inner.render {
            return render(notice, channel);
        }
        let reference = number(notice, "reference");
        let title = string(notice, "title");
        let ask = if text(notice, "urgency") == Some(URGENT) {
            format!(" Reply ACK {reference} to acknowledge.")
        } else {
            String::new()
        };
        let body = match text(notice, "body") {
            Some(body) if !body.is_empty() => format!(" {body}"),
            _ => String::new(),
        };
        let text = if channel == "email" {
            format!("{title}{body}\n\nReference {reference}.{ask}")
        } else {
            format!("#{reference} {title}{body}{ask}")
        };
        (title, text)
    }

    async fn create_notice<C: PgClient>(
        &self,
        client: Db<'_, C>,
        recipient: &RecipientRef,
        event: &RaisedEvent,
        urgency: &str,
        route: Option<&str>,
    ) -> Result<Option<Values>> {
        let inbox = &self.inner.parts.inbox;
        let mut existing = Values::new();
        existing.insert("eventId".to_owned(), eq(event.id.as_str()));
        existing.insert("recipient".to_owned(), eq(recipient.id.as_str()));
        if self
            .sys(client)?
            .table(inbox)?
            .find_one(Some(&existing), &FindOptions::default())
            .await?
            .is_some()
        {
            return Ok(None);
        }
        let mut title = match &event.key {
            Some(key) => format!("{} {key}", event.kind),
            None => event.kind.clone(),
        };
        if let (Some(declared), Some(table), Some(key)) = (
            self.event_of(event.table.as_deref(), &event.kind),
            &event.table,
            &event.key,
        ) {
            if let Some(field) = &declared.title {
                if let Some(row) = self.row_of(client, table, key).await? {
                    match row.get(field) {
                        None | Some(Value::Null) => {}
                        Some(Value::String(s)) => title = s.clone(),
                        Some(other) => title = other.to_string(),
                    }
                }
            }
        }
        let mut attempt = 0;
        loop {
            // The reference is the recipient's next: a concurrent notice of
            // theirs that took it first is met by the unique index, and the
            // notice tries the next one, in a savepoint so the caller's
            // transaction holds.
            client.batch_execute("SAVEPOINT notifier_notice").await?;
            let created = async {
                let sys = self.sys(client)?;
                let box_ = sys.table(inbox)?;
                let mut mine = Values::new();
                mine.insert("recipient".to_owned(), eq(recipient.id.as_str()));
                let last = box_
                    .find_one(Some(&mine), &ordered(1, OrderBy::desc("reference")))
                    .await?;
                let reference = last.as_ref().map_or(0, |l| number(l, "reference")) + 1;
                let input = obj(json!({
                    "recipient": recipient.id,
                    "recipientKind": recipient.kind,
                    "reference": reference,
                    "eventId": event.id,
                    "eventKind": event.kind,
                    "urgency": urgency,
                    "subjectTable": event.table,
                    "subjectKey": event.key,
                    "title": title,
                    "payload": Value::Object(event.payload.clone()),
                    "route": route,
                    "state": if urgency == RECORD { "read" } else { "unread" },
                }));
                Ok::<_, NotifierError>(box_.create_one(&input).await?)
            }
            .await;
            match created {
                Ok(row) => {
                    client
                        .batch_execute("RELEASE SAVEPOINT notifier_notice")
                        .await?;
                    return Ok(Some(row));
                }
                Err(err) => {
                    client
                        .batch_execute("ROLLBACK TO SAVEPOINT notifier_notice")
                        .await?;
                    attempt += 1;
                    if attempt >= 5 {
                        return Err(err);
                    }
                }
            }
        }
    }

    async fn queue_delivery<C: PgClient>(
        &self,
        client: Db<'_, C>,
        notice: &Values,
        recipient: &RecipientRef,
        channel: &str,
        attempt: i64,
        delay: u64,
    ) -> Result<()> {
        let due = self.now() + Duration::from_secs(delay);
        let input = obj(json!({
            "notice": notice.get("id"),
            "purpose": "notice",
            "recipient": recipient.id,
            "channelKind": channel,
            "attempt": attempt,
            "state": "queued",
            "dueAt": at_text(due),
        }));
        let delivery = self
            .sys(client)?
            .table(&self.inner.parts.deliveries)?
            .create_one(&input)
            .await?;
        let mut work = NoticeWork::step("deliver");
        work.delivery = Some(string(&delivery, "id"));
        client
            .enqueue(&self.inner.dispatch, &work, Duration::from_secs(delay))
            .await?;
        Ok(())
    }

    async fn next_digest_at<C: PgClient>(
        &self,
        client: Db<'_, C>,
        preference: &Preference,
    ) -> Result<Option<i64>> {
        let (Some(rule), Some(expand)) = (&preference.digest, &self.inner.expand) else {
            return Ok(None);
        };
        let at = self.now();
        let (_, _, date) = self.wall_of(client, at, &preference.time_zone).await?;
        let input = json!({
            "anchor": "calendar",
            "rule": rule,
            "start": format!("{date}T00:00"),
            "timeZone": preference.time_zone,
            "from": millis(at),
            "limit": 1,
        });
        Ok(expand(&input).first().copied())
    }

    /// Sends `notice` to `recipient` as its urgency says: now, after quiet
    /// hours, in the next digest, or not at all.
    async fn dispatch_notice<C: PgClient>(
        &self,
        client: Db<'_, C>,
        notice: &Values,
        recipient: &RecipientRef,
        urgency: &str,
    ) -> Result<()> {
        if urgency == RECORD {
            return Ok(());
        }
        let preference = self
            .preference_of(
                client,
                recipient,
                text(notice, "eventKind").unwrap_or_default(),
                urgency,
            )
            .await?;
        let list = Self::outward(&preference.channels);
        if urgency == DIGEST {
            if let Some(at) = self.next_digest_at(client, &preference).await? {
                let mut waiting = Values::new();
                waiting.insert("recipient".to_owned(), eq(recipient.id.as_str()));
                waiting.insert("urgency".to_owned(), eq(DIGEST));
                waiting.insert("digestedAt".to_owned(), json!({ "isNull": true }));
                waiting.insert("id".to_owned(), json!({ "ne": notice.get("id") }));
                let others = self
                    .sys(client)?
                    .table(&self.inner.parts.inbox)?
                    .find_many(Some(&waiting), &many(1))
                    .await?;
                if others.rows.is_empty() {
                    let delay = u64::try_from((at - millis(self.now())).max(0)).unwrap_or(0);
                    let mut work = NoticeWork::step("digest");
                    work.recipient = Some(recipient.id.clone());
                    client
                        .enqueue(
                            &self.inner.dispatch,
                            &work,
                            Duration::from_secs(delay.div_ceil(1000)),
                        )
                        .await?;
                }
                return Ok(());
            }
        }
        let Some(first) = list.first() else {
            return Ok(());
        };
        let delay = if urgency == URGENT {
            0
        } else {
            let (minutes, second, _) = self
                .wall_of(client, self.now(), &preference.time_zone)
                .await?;
            quiet_delay_seconds(
                minutes,
                second,
                preference.quiet_start.as_deref(),
                preference.quiet_end.as_deref(),
            )
        };
        self.queue_delivery(client, notice, recipient, first, 1, delay)
            .await
    }

    async fn route(&self, event: RaisedEvent) -> Result<()> {
        let mut client = self.store().client().await?;
        let tx = client.transaction().await?;
        let mut filter = Values::new();
        filter.insert("enabled".to_owned(), eq(true));
        filter.insert("eventKind".to_owned(), json!({ "in": [event.kind, "*"] }));
        let candidates = self
            .sys(tx.db())?
            .table(&self.inner.parts.routes)?
            .find_many(Some(&filter), &many(1000))
            .await?
            .rows;
        for row in candidates {
            if self.route_matches(tx.db(), &event, &row).await? {
                self.route_by(tx.db(), &event, &row).await?;
            }
        }
        tx.commit().await?;
        Ok(())
    }

    /// Whether a route's table and filter match the event's record.
    async fn route_matches<C: PgClient>(
        &self,
        client: Db<'_, C>,
        event: &RaisedEvent,
        row: &Values,
    ) -> Result<bool> {
        if let Some(table) = text(row, "subjectTable") {
            if Some(table) != event.table.as_deref() {
                return Ok(false);
            }
        }
        let (Some(Value::Object(route_filter)), Some(table), Some(key)) =
            (row.get("filter"), &event.table, &event.key)
        else {
            return Ok(true);
        };
        let def = self.inner.definition.table(table)?;
        let mut matching = route_filter.clone();
        matching.insert(def.key.clone(), eq(key.as_str()));
        Ok(self
            .sys(client)?
            .table(table)?
            .find_one(Some(&matching), &FindOptions::default())
            .await?
            .is_some())
    }

    /// Routes the event by one route: a notice for each recipient who reads
    /// the record, sent as its urgency says, and the first step of the
    /// route's escalation of an urgent one.
    async fn route_by<C: PgClient>(
        &self,
        client: Db<'_, C>,
        event: &RaisedEvent,
        row: &Values,
    ) -> Result<()> {
        let declared = self.event_of(event.table.as_deref(), &event.kind);
        let urgency = text(row, "urgency")
            .map(str::to_owned)
            .or_else(|| event.urgency.clone())
            .or_else(|| declared.map(|d| d.urgency.clone()))
            .unwrap_or_else(|| NORMAL.to_owned());
        let route_id = string(row, "id");
        let specs: Vec<Value> = row
            .get("recipients")
            .and_then(Value::as_array)
            .cloned()
            .unwrap_or_default();
        // A route that names an agent starts its run about the record
        // (D158): a request on the dispatcher's queue, in the routing
        // transaction.
        for spec in &specs {
            let Some(agent) = spec.get("agent").and_then(Value::as_str) else {
                continue;
            };
            let Some(requests) = &self.inner.agents else {
                tracing::warn!(route = %route_id, agent = %agent, "a route names an agent, and the notifier has no agent dispatcher");
                continue;
            };
            let mut request = obj(json!({
                "step": "start",
                "trigger": "route",
                "agent": agent,
                "subjectTable": event.table,
                "subjectKey": event.key,
                "event": { "id": event.id, "kind": event.kind, "payload": event.payload },
                "requestedBy": { "kind": "route", "subject": route_id },
            }));
            if let Some(task) = spec.get("task").and_then(Value::as_str) {
                request.insert("input".to_owned(), json!({ "task": task }));
            }
            client
                .enqueue(requests, &Value::Object(request), Duration::ZERO)
                .await?;
        }
        let recipients = self.recipients_of(client, event, &specs).await?;
        for recipient in &recipients {
            if !self
                .reads(
                    client,
                    recipient,
                    event.table.as_deref(),
                    event.key.as_deref(),
                )
                .await?
            {
                continue;
            }
            if let Some(notice) = self
                .create_notice(client, recipient, event, &urgency, Some(&route_id))
                .await?
            {
                self.dispatch_notice(client, &notice, recipient, &urgency)
                    .await?;
            }
        }
        let escalation: Vec<Value> = row
            .get("escalation")
            .and_then(Value::as_array)
            .cloned()
            .unwrap_or_default();
        if urgency != URGENT || escalation.is_empty() || recipients.is_empty() {
            return Ok(());
        }
        let mut of_route = Values::new();
        of_route.insert("eventId".to_owned(), eq(event.id.as_str()));
        of_route.insert("route".to_owned(), eq(route_id.as_str()));
        let notices = self
            .sys(client)?
            .table(&self.inner.parts.inbox)?
            .find_many(Some(&of_route), &many(1000))
            .await?
            .rows;
        let after = escalation[0]
            .get("afterSeconds")
            .and_then(Value::as_u64)
            .unwrap_or(0);
        for notice in notices {
            let mut work = NoticeWork::step("escalate");
            work.notice = Some(string(&notice, "id"));
            client
                .enqueue(&self.inner.dispatch, &work, Duration::from_secs(after))
                .await?;
        }
        Ok(())
    }

    fn recipient_of_notice(notice: &Values) -> RecipientRef {
        RecipientRef {
            kind: text(notice, "recipientKind").unwrap_or("user").to_owned(),
            id: string(notice, "recipient"),
            table: None,
        }
    }

    async fn address_of<C: PgClient>(
        &self,
        client: Db<'_, C>,
        recipient: &RecipientRef,
        channel: &str,
    ) -> Result<Option<String>> {
        let Some(identities) = &self.inner.parts.identities else {
            return Ok(None);
        };
        // An address that opted out is sent nothing, and a party's identity
        // whose grants have all ended since it was verified may be someone
        // else's now (D157).
        let filter = obj(json!({
            "principal": { "eq": recipient.id },
            "kind": { "eq": self.address_kind_of(channel) },
            "trust": { "in": ["verified", "entered"] },
            "revokedAt": { "isNull": true },
            "optedOutAt": { "isNull": true },
        }));
        let rows = self
            .sys(client)?
            .table(identities)?
            .find_many(Some(&filter), &many(10))
            .await?
            .rows;
        for row in rows {
            if !identity_lapsed(client, self.inner.definition, &row, self.now()).await? {
                return Ok(Some(string(&row, "address")));
            }
        }
        Ok(None)
    }

    /// Falls through from a delivery that failed to the recipient's next
    /// channel.
    async fn fall_through<C: PgClient>(
        &self,
        client: Db<'_, C>,
        delivery: &Values,
        detail: &str,
    ) -> Result<()> {
        let sys = self.sys(client)?;
        let deliveries = sys.table(&self.inner.parts.deliveries)?;
        let now = at_text(self.now());
        let mut update =
            obj(json!({ "state": "failed", "detail": detail, "failedAt": now, "endedAt": now }));
        if let Some(slot) = delivery.get("slot").filter(|s| !s.is_null()) {
            update.insert("slot".to_owned(), slot.clone());
        }
        deliveries
            .update_one(&key(&string(delivery, "id")), &update, &none())
            .await?;
        let Some(notice_id) = text(delivery, "notice") else {
            return Ok(());
        };
        if text(delivery, "purpose") != Some("notice") {
            return Ok(());
        }
        let Some(notice) = self.notice_by_id(client, notice_id).await? else {
            return Ok(());
        };
        if matches!(text(&notice, "state"), Some("acknowledged" | "done")) {
            return Ok(());
        }
        let recipient = Self::recipient_of_notice(&notice);
        let preference = self
            .preference_of(
                client,
                &recipient,
                text(&notice, "eventKind").unwrap_or_default(),
                text(&notice, "urgency").unwrap_or(NORMAL),
            )
            .await?;
        let list = Self::outward(&preference.channels);
        let mut of_notice = Values::new();
        of_notice.insert("notice".to_owned(), eq(notice_id));
        let tried: BTreeSet<String> = deliveries
            .find_many(Some(&of_notice), &many(100))
            .await?
            .rows
            .iter()
            .map(|d| string(d, "channelKind"))
            .collect();
        if let Some(next) = list.iter().find(|c| !tried.contains(*c)) {
            let attempt = number(delivery, "attempt").max(1) + 1;
            self.queue_delivery(client, &notice, &recipient, next, attempt, 0)
                .await?;
        }
        Ok(())
    }

    async fn notice_by_id<C: PgClient>(
        &self,
        client: Db<'_, C>,
        id: &str,
    ) -> Result<Option<Values>> {
        let mut filter = Values::new();
        filter.insert("id".to_owned(), eq(id));
        Ok(self
            .sys(client)?
            .table(&self.inner.parts.inbox)?
            .find_one(Some(&filter), &FindOptions::default())
            .await?)
    }

    async fn delivery_by_id<C: PgClient>(
        &self,
        client: Db<'_, C>,
        id: &str,
    ) -> Result<Option<Values>> {
        let mut filter = Values::new();
        filter.insert("id".to_owned(), eq(id));
        Ok(self
            .sys(client)?
            .table(&self.inner.parts.deliveries)?
            .find_one(Some(&filter), &FindOptions::default())
            .await?)
    }

    async fn update_delivery(&self, id: &str, update: Values) -> Result<()> {
        let client = self.store().client().await?;
        self.sys(client.db())?
            .table(&self.inner.parts.deliveries)?
            .update_one(&key(id), &update, &none())
            .await?;
        Ok(())
    }

    async fn fall_through_alone(&self, delivery: &Values, detail: &str) -> Result<()> {
        let mut client = self.store().client().await?;
        let tx = client.transaction().await?;
        self.fall_through(tx.db(), delivery, detail).await?;
        tx.commit().await?;
        Ok(())
    }

    async fn deliver(&self, id: &str) -> Result<()> {
        let client = self.store().client().await?;
        let Some(delivery) = self.delivery_by_id(client.db(), id).await? else {
            return Ok(());
        };
        if text(&delivery, "state") != Some("queued") {
            return Ok(());
        }
        let notice = match text(&delivery, "notice") {
            Some(notice) => self.notice_by_id(client.db(), notice).await?,
            None => None,
        };
        drop(client);
        let now = at_text(self.now());
        if let Some(notice) = &notice {
            if matches!(text(notice, "state"), Some("acknowledged" | "done")) {
                return self
                    .update_delivery(id, obj(json!({ "state": "acknowledged", "acknowledgedAt": now, "endedAt": now })))
                    .await;
            }
        }
        let kind = string(&delivery, "channelKind");
        let Some(channel) = self.channel_of_kind(&kind, text(&delivery, "slot")).await? else {
            return self
                .fall_through_alone(&delivery, &format!("no {kind} channel is up"))
                .await;
        };
        let recipient = notice.as_ref().map(Self::recipient_of_notice);
        let address = match (text(&delivery, "address"), &recipient) {
            (Some(address), _) => Some(address.to_owned()),
            (None, Some(recipient)) => {
                let client = self.store().client().await?;
                self.address_of(client.db(), recipient, &kind).await?
            }
            (None, None) => None,
        };
        let Some(address) = address else {
            let detail = format!(
                "the recipient has no verified {} address",
                self.address_kind_of(&kind)
            );
            return self.fall_through_alone(&delivery, &detail).await;
        };
        // A notice's recipient is held to its record again at send time:
        // one who lost the record since the event reaches nothing.
        if let (Some(notice), Some(recipient)) = (&notice, &recipient) {
            if let (Some(table), Some(subject)) =
                (text(notice, "subjectTable"), text(notice, "subjectKey"))
            {
                let client = self.store().client().await?;
                if !self
                    .reads(client.db(), recipient, Some(table), Some(subject))
                    .await?
                {
                    drop(client);
                    return self
                        .update_delivery(id, obj(json!({ "state": "failed", "detail": "withheld", "failedAt": now, "endedAt": now })))
                        .await;
                }
            }
        }
        self.send_notice(&delivery, notice.as_ref(), &channel, &address)
            .await
    }

    /// Sends a delivery of a notice through `channel` to `address`, and
    /// waits on its report from an adapter that reports, or falls through
    /// when the send fails.
    async fn send_notice(
        &self,
        delivery: &Values,
        notice: Option<&Values>,
        channel: &Channel,
        address: &str,
    ) -> Result<()> {
        let id = string(delivery, "id");
        let id = id.as_str();
        let kind = string(delivery, "channelKind");
        let (subject, body) = match notice {
            Some(notice) => self.render(notice, &kind),
            None => {
                let detail = string(delivery, "detail");
                (detail.clone(), detail)
            }
        };
        let message = OutboundMessage {
            to: address.to_owned(),
            subject: Some(subject),
            body,
            reference: notice.map(|n| number(n, "reference").to_string()),
            idempotency_key: Some(id.to_owned()),
            ..OutboundMessage::default()
        };
        match channel.adapter.send(&message).await {
            Ok(sent) => {
                let state = wire_state(sent.state);
                let at = at_text(self.now());
                let mut update = obj(json!({
                    "state": state,
                    "slot": channel.slot,
                    "address": address,
                    "providerMessageId": sent.provider_message_id,
                    "sentAt": at,
                }));
                if state == "delivered" {
                    update.insert("deliveredAt".to_owned(), at.clone());
                }
                if !channel.status {
                    update.insert("endedAt".to_owned(), at);
                }
                self.update_delivery(id, update).await?;
                if channel.status && state != "delivered" && state != "read" {
                    let client = self.store().client().await?;
                    let mut work = NoticeWork::step("timeout");
                    work.delivery = Some(id.to_owned());
                    client
                        .db()
                        .enqueue(
                            &self.inner.dispatch,
                            &work,
                            Duration::from_secs(self.inner.timeout_seconds),
                        )
                        .await?;
                }
                Ok(())
            }
            Err(err) => {
                let detail = err.to_string();
                tracing::warn!(slot = %channel.slot, delivery = %id, detail = %detail, "send failed");
                let mut failed_one = delivery.clone();
                failed_one.insert("slot".to_owned(), json!(channel.slot));
                self.fall_through_alone(&failed_one, &detail).await
            }
        }
    }

    /// Delivers a notice's delivery, or one of no notice (a digest, an
    /// echo, a code) to the address it holds or its recipient's.
    async fn deliver_any(&self, id: &str) -> Result<()> {
        let client = self.store().client().await?;
        let Some(delivery) = self.delivery_by_id(client.db(), id).await? else {
            return Ok(());
        };
        drop(client);
        if text(&delivery, "notice").is_some() {
            return self.deliver(id).await;
        }
        if text(&delivery, "state") != Some("queued") {
            return Ok(());
        }
        let kind = string(&delivery, "channelKind");
        let channel = self.channel_of_kind(&kind, text(&delivery, "slot")).await?;
        let address = match (text(&delivery, "address"), text(&delivery, "recipient")) {
            (Some(address), _) => Some(address.to_owned()),
            (None, Some(recipient)) => {
                let client = self.store().client().await?;
                self.address_of(client.db(), &RecipientRef::user(recipient), &kind)
                    .await?
            }
            (None, None) => None,
        };
        let now = at_text(self.now());
        let (Some(channel), Some(address)) = (channel.clone(), address) else {
            let detail = if channel.is_none() {
                format!("no {kind} channel is up")
            } else {
                "no address".to_owned()
            };
            return self
                .update_delivery(id, obj(json!({ "state": "failed", "detail": detail, "failedAt": now, "endedAt": now })))
                .await;
        };
        let detail = string(&delivery, "detail");
        let (subject, body) = match detail.split_once('\n') {
            Some((subject, rest)) => (subject.to_owned(), rest.to_owned()),
            None => (detail.clone(), detail.clone()),
        };
        let message = OutboundMessage {
            to: address.clone(),
            subject: Some(subject),
            body,
            idempotency_key: Some(id.to_owned()),
            ..OutboundMessage::default()
        };
        let update = match channel.adapter.send(&message).await {
            Ok(sent) => obj(json!({
                "state": wire_state(sent.state),
                "slot": channel.slot,
                "address": address,
                "providerMessageId": sent.provider_message_id,
                "sentAt": now,
                "endedAt": now,
            })),
            Err(err) => obj(json!({
                "state": "failed",
                "slot": channel.slot,
                "address": address,
                "detail": err.to_string(),
                "failedAt": now,
                "endedAt": now,
            })),
        };
        self.update_delivery(id, update).await
    }

    async fn timeout(&self, id: &str) -> Result<()> {
        let client = self.store().client().await?;
        let Some(delivery) = self.delivery_by_id(client.db(), id).await? else {
            return Ok(());
        };
        drop(client);
        if text(&delivery, "state") != Some("sent") {
            return Ok(());
        }
        let detail = format!("not delivered within {} s", self.inner.timeout_seconds);
        self.fall_through_alone(&delivery, &detail).await
    }

    async fn escalate(&self, notice_id: &str) -> Result<()> {
        let mut client = self.store().client().await?;
        let tx = client.transaction().await?;
        let Some(notice) = self.notice_by_id(tx.db(), notice_id).await? else {
            return Ok(());
        };
        if matches!(text(&notice, "state"), Some("acknowledged" | "done")) {
            return Ok(());
        }
        let Some(route_id) = text(&notice, "route").map(str::to_owned) else {
            return Ok(());
        };
        let mut by_id = Values::new();
        by_id.insert("id".to_owned(), eq(route_id.as_str()));
        let route = self
            .sys(tx.db())?
            .table(&self.inner.parts.routes)?
            .find_one(Some(&by_id), &FindOptions::default())
            .await?;
        let steps: Vec<Value> = route
            .as_ref()
            .and_then(|r| r.get("escalation"))
            .and_then(Value::as_array)
            .cloned()
            .unwrap_or_default();
        let index = usize::try_from(number(&notice, "escalation")).unwrap_or(0);
        let Some(step) = steps.get(index) else {
            return Ok(());
        };
        let notice_key = string(&notice, "id");
        self.sys(tx.db())?
            .table(&self.inner.parts.inbox)?
            .update_one(
                &key(&notice_key),
                &obj(json!({ "escalation": index + 1 })),
                &none(),
            )
            .await?;
        let recipient = Self::recipient_of_notice(&notice);
        let mut of_notice = Values::new();
        of_notice.insert("notice".to_owned(), eq(notice_key.as_str()));
        let tried = self
            .sys(tx.db())?
            .table(&self.inner.parts.deliveries)?
            .find_many(Some(&of_notice), &many(100))
            .await?
            .rows
            .len();
        let tried = i64::try_from(tried).unwrap_or(0);
        for channel in strings(step.get("channels")).unwrap_or_default() {
            self.queue_delivery(tx.db(), &notice, &recipient, &channel, tried + 1, 0)
                .await?;
        }
        if let Some(more) = step.get("recipients").and_then(Value::as_array) {
            if !more.is_empty() {
                let event = RaisedEvent {
                    id: string(&notice, "eventId"),
                    table: text(&notice, "subjectTable").map(str::to_owned),
                    key: text(&notice, "subjectKey").map(str::to_owned),
                    kind: string(&notice, "eventKind"),
                    urgency: None,
                    payload: notice.get("payload").cloned().map(obj).unwrap_or_default(),
                    at: millis(self.now()),
                };
                for added in self.recipients_of(tx.db(), &event, more).await? {
                    if !self
                        .reads(
                            tx.db(),
                            &added,
                            event.table.as_deref(),
                            event.key.as_deref(),
                        )
                        .await?
                    {
                        continue;
                    }
                    if let Some(created) = self
                        .create_notice(tx.db(), &added, &event, URGENT, Some(&route_id))
                        .await?
                    {
                        self.dispatch_notice(tx.db(), &created, &added, URGENT)
                            .await?;
                    }
                }
            }
        }
        if let Some(next) = steps.get(index + 1) {
            let after = next
                .get("afterSeconds")
                .and_then(Value::as_u64)
                .unwrap_or(0);
            let mut work = NoticeWork::step("escalate");
            work.notice = Some(notice_key);
            tx.db()
                .enqueue(&self.inner.dispatch, &work, Duration::from_secs(after))
                .await?;
        }
        tx.commit().await?;
        Ok(())
    }

    async fn digest(&self, recipient_id: &str) -> Result<()> {
        let mut client = self.store().client().await?;
        let waiting = {
            let filter = obj(json!({
                "recipient": { "eq": recipient_id },
                "urgency": { "eq": DIGEST },
                "digestedAt": { "isNull": true },
            }));
            self.sys(client.db())?
                .table(&self.inner.parts.inbox)?
                .find_many(Some(&filter), &ordered(200, OrderBy::asc("reference")))
                .await?
                .rows
        };
        let Some(first) = waiting.first() else {
            return Ok(());
        };
        let recipient = Self::recipient_of_notice(first);
        let preference = self
            .preference_of(client.db(), &recipient, "*", DIGEST)
            .await?;
        let channel = Self::outward(&preference.channels).into_iter().next();
        let tx = client.transaction().await?;
        let now = at_text(self.now());
        for notice in &waiting {
            self.sys(tx.db())?
                .table(&self.inner.parts.inbox)?
                .update_one(
                    &key(&string(notice, "id")),
                    &obj(json!({ "digestedAt": now })),
                    &none(),
                )
                .await?;
        }
        if let Some(channel) = channel {
            let lines: Vec<String> = waiting
                .iter()
                .map(|n| format!("#{} {}", number(n, "reference"), string(n, "title")))
                .collect();
            let input = obj(json!({
                "purpose": "digest",
                "recipient": recipient.id,
                "channelKind": channel,
                "attempt": 1,
                "state": "queued",
                "detail": format!("Your digest\n{}", lines.join("\n")),
            }));
            let delivery = self
                .sys(tx.db())?
                .table(&self.inner.parts.deliveries)?
                .create_one(&input)
                .await?;
            let mut work = NoticeWork::step("deliver");
            work.delivery = Some(string(&delivery, "id"));
            tx.db()
                .enqueue(&self.inner.dispatch, &work, Duration::ZERO)
                .await?;
        }
        tx.commit().await?;
        Ok(())
    }

    /// Starts a subscription's brief at its next time, in `client`'s
    /// transaction.
    pub async fn schedule_subscription<C: PgClient>(
        &self,
        client: Db<'_, C>,
        id: &str,
    ) -> Result<()> {
        let (Some(subscriptions), Some(expand)) =
            (&self.inner.parts.subscriptions, &self.inner.expand)
        else {
            return Err(refused(
                "schedule_subscription: the database declares no @notifier(\"subscriptions\"), or the notifier has no expand",
            ));
        };
        let mut by_id = Values::new();
        by_id.insert("id".to_owned(), eq(id));
        let sys = self.sys(client)?;
        let Some(row) = sys
            .table(subscriptions)?
            .find_one(Some(&by_id), &FindOptions::default())
            .await?
        else {
            return Ok(());
        };
        if row.get("enabled") != Some(&Value::Bool(true)) {
            return Ok(());
        }
        let input = json!({
            "anchor": "calendar",
            "rule": row.get("rule"),
            "start": row.get("start"),
            "timeZone": row.get("timeZone"),
            "from": millis(self.now()) + 1000,
            "limit": 1,
        });
        let Some(at) = expand(&input).first().copied() else {
            return Ok(());
        };
        let next = UNIX_EPOCH + Duration::from_millis(u64::try_from(at).unwrap_or(0));
        sys.table(subscriptions)?
            .update_one(&key(id), &obj(json!({ "nextAt": at_text(next) })), &none())
            .await?;
        let delay = u64::try_from((at - millis(self.now())).max(0)).unwrap_or(0);
        let mut work = NoticeWork::step("brief");
        work.subscription = Some(id.to_owned());
        client
            .enqueue(
                &self.inner.dispatch,
                &work,
                Duration::from_secs(delay.div_ceil(1000)),
            )
            .await?;
        Ok(())
    }

    async fn brief(&self, id: &str) -> Result<()> {
        let Some(subscriptions) = self.inner.parts.subscriptions.clone() else {
            return Ok(());
        };
        let mut client = self.store().client().await?;
        let mut by_id = Values::new();
        by_id.insert("id".to_owned(), eq(id));
        let Some(row) = self
            .sys(client.db())?
            .table(&subscriptions)?
            .find_one(Some(&by_id), &FindOptions::default())
            .await?
        else {
            return Ok(());
        };
        if row.get("enabled") != Some(&Value::Bool(true)) {
            return Ok(());
        }
        let recipient = RecipientRef::user(string(&row, "principal"));
        let mut lines: Vec<String> = Vec::new();
        if let Some(def) = self.definition_of(text(&row, "subjectTable")) {
            // The saved query runs as its recipient: a row they cannot read
            // is not in their brief.
            let reader = self.reader_of(&recipient).await?;
            let filter = row.get("filter").cloned().map(obj).unwrap_or_default();
            let rows = self
                .as_reader(client.db(), &reader)?
                .table(&def.name)?
                .find_many(Some(&filter), &many(50))
                .await?
                .rows;
            let field = text(&row, "titleField").unwrap_or(&def.key).to_owned();
            lines = rows
                .iter()
                .map(|r| match r.get(&field) {
                    Some(Value::String(s)) => s.clone(),
                    Some(v) if !v.is_null() => v.to_string(),
                    _ => string(r, &def.key),
                })
                .collect();
        }
        let tx = client.transaction().await?;
        for channel in Self::outward(&strings(row.get("channels")).unwrap_or_default()) {
            let address = self.address_of(tx.db(), &recipient, &channel).await?;
            let body = if lines.is_empty() {
                "Nothing to report.".to_owned()
            } else {
                lines.join("\n")
            };
            let input = obj(json!({
                "purpose": "digest",
                "recipient": recipient.id,
                "channelKind": channel,
                "address": address,
                "attempt": 1,
                "state": "queued",
                "detail": format!("{}\n{body}", string(&row, "name")),
            }));
            let delivery = self
                .sys(tx.db())?
                .table(&self.inner.parts.deliveries)?
                .create_one(&input)
                .await?;
            let mut work = NoticeWork::step("deliver");
            work.delivery = Some(string(&delivery, "id"));
            tx.db()
                .enqueue(&self.inner.dispatch, &work, Duration::ZERO)
                .await?;
        }
        self.sys(tx.db())?
            .table(&subscriptions)?
            .update_one(
                &key(id),
                &obj(json!({ "lastSentAt": at_text(self.now()) })),
                &none(),
            )
            .await?;
        self.schedule_subscription(tx.db(), id).await?;
        tx.commit().await?;
        Ok(())
    }

    /// Handles a message of the dispatch queue: the queue's worker calls
    /// it with each message it claims.
    pub async fn handle(&self, message: NoticeWork) -> Result<()> {
        let of = |value: &Option<String>| value.clone().unwrap_or_default();
        match message.step.as_str() {
            "route" => {
                let event: RaisedEvent =
                    serde_json::from_value(message.event.unwrap_or(Value::Null))
                        .map_err(|err| failed(format!("the route step's event: {err}")))?;
                self.route(event).await
            }
            "deliver" => self.deliver_any(&of(&message.delivery)).await,
            "timeout" => self.timeout(&of(&message.delivery)).await,
            "escalate" => self.escalate(&of(&message.notice)).await,
            "digest" => self.digest(&of(&message.recipient)).await,
            "brief" => self.brief(&of(&message.subscription)).await,
            other => Err(refused(format!(
                "the dispatch queue holds a step the notifier does not take: {other}"
            ))),
        }
    }

    /// The dispatch queue as a worker claims it, whose prune deletes the
    /// deliveries past their retention with the queue's done messages:
    /// what `run_worker` runs with [`Notifier::handle`].
    pub fn worker_queue(&self) -> NotifierQueue {
        NotifierQueue {
            queue: self.inner.queue.clone(),
            notifier: self.clone(),
        }
    }

    /// Deletes up to `limit` deliveries that ended past their retention,
    /// and answers how many.
    pub async fn prune(&self, limit: u32) -> Result<u64> {
        let client = self.store().client().await?;
        let before = self.now() - Duration::from_secs(self.inner.retention_days * 86_400);
        let filter = obj(json!({ "endedAt": { "lt": at_text(before) } }));
        let sys = self.sys(client.db())?;
        let deliveries = sys.table(&self.inner.parts.deliveries)?;
        let old = deliveries
            .find_many(Some(&filter), &many(limit))
            .await?
            .rows;
        for row in &old {
            deliveries
                .delete_one(&key(&string(row, "id")), &none())
                .await?;
        }
        Ok(old.len() as u64)
    }

    // ---- the inbox ----

    /// The recipient's notices, newest first, of `states` (every one when
    /// empty), at most `limit` (50 unless set).
    pub async fn inbox(
        &self,
        recipient: &RecipientRef,
        states: &[&str],
        limit: Option<u32>,
    ) -> Result<Vec<Values>> {
        let client = self.store().client().await?;
        let mut filter = Values::new();
        filter.insert("recipient".to_owned(), eq(recipient.id.as_str()));
        if !states.is_empty() {
            filter.insert("state".to_owned(), json!({ "in": states }));
        }
        Ok(self
            .sys(client.db())?
            .table(&self.inner.parts.inbox)?
            .find_many(
                Some(&filter),
                &ordered(limit.unwrap_or(50), OrderBy::desc("reference")),
            )
            .await?
            .rows)
    }

    async fn own_notice(&self, recipient: &RecipientRef, id: &str) -> Result<Values> {
        let client = self.store().client().await?;
        match self.notice_by_id(client.db(), id).await? {
            Some(notice) if same_id(&string(&notice, "recipient")) == same_id(&recipient.id) => {
                Ok(notice)
            }
            _ => Err(refused("no such notice of the caller")),
        }
    }

    /// Marks a notice of the recipient's read.
    pub async fn mark_read(&self, recipient: &RecipientRef, id: &str) -> Result<()> {
        let notice = self.own_notice(recipient, id).await?;
        if text(&notice, "state") == Some("unread") {
            let client = self.store().client().await?;
            self.sys(client.db())?
                .table(&self.inner.parts.inbox)?
                .update_one(
                    &key(&string(&notice, "id")),
                    &obj(json!({ "state": "read", "readAt": at_text(self.now()) })),
                    &none(),
                )
                .await?;
        }
        Ok(())
    }

    /// Acknowledges a notice of the recipient's, `via` a channel kind
    /// (`inapp` unless set), which stops its escalation.
    pub async fn acknowledge(
        &self,
        recipient: &RecipientRef,
        id: &str,
        via: Option<&str>,
    ) -> Result<()> {
        let notice = self.own_notice(recipient, id).await?;
        if matches!(text(&notice, "state"), Some("acknowledged" | "done")) {
            return Ok(());
        }
        let mut client = self.store().client().await?;
        let tx = client.transaction().await?;
        self.acknowledge_in(tx.db(), &notice, via.unwrap_or(INAPP))
            .await?;
        tx.commit().await?;
        Ok(())
    }

    async fn acknowledge_in<C: PgClient>(
        &self,
        client: Db<'_, C>,
        notice: &Values,
        via: &str,
    ) -> Result<()> {
        let now = at_text(self.now());
        let notice_key = string(notice, "id");
        let sys = self.sys(client)?;
        sys.table(&self.inner.parts.inbox)?
            .update_one(&key(&notice_key), &obj(json!({ "state": "acknowledged", "acknowledgedAt": now, "acknowledgedVia": via })), &none())
            .await?;
        self.end_deliveries(client, &notice_key, |state| {
            matches!(state, "queued" | "sent" | "delivered")
        })
        .await
    }

    async fn end_deliveries<C: PgClient>(
        &self,
        client: Db<'_, C>,
        notice: &str,
        open: impl Fn(&str) -> bool,
    ) -> Result<()> {
        let now = at_text(self.now());
        let sys = self.sys(client)?;
        let deliveries = sys.table(&self.inner.parts.deliveries)?;
        let mut of_notice = Values::new();
        of_notice.insert("notice".to_owned(), eq(notice));
        for d in deliveries
            .find_many(Some(&of_notice), &many(100))
            .await?
            .rows
        {
            if open(text(&d, "state").unwrap_or_default()) {
                deliveries
                    .update_one(&key(&string(&d, "id")), &obj(json!({ "state": "acknowledged", "acknowledgedAt": now, "endedAt": now })), &none())
                    .await?;
            }
        }
        Ok(())
    }

    // ---- identities ----

    fn identities(&self) -> Result<&str> {
        self.inner
            .parts
            .identities
            .as_deref()
            .ok_or_else(|| refused("the database declares no @channelIdentities table"))
    }

    /// Adds an address of a principal; unverified until it replies with the
    /// code [`Notifier::start_verification`] sends, or entered by hand.
    pub async fn add_identity(&self, input: AddIdentity) -> Result<Values> {
        let identities = self.identities()?;
        let client = self.store().client().await?;
        let trust = input.trust.unwrap_or_else(|| "unverified".to_owned());
        let mut row = obj(json!({
            "kind": input.kind,
            "address": normalise_address(&input.kind, &input.address),
            "principalKind": input.principal.kind,
            "principalTable": input.principal.table,
            "principal": input.principal.id,
            "trust": trust,
        }));
        if trust == "entered" {
            row.insert("verifiedAt".to_owned(), at_text(self.now()));
        }
        Ok(self
            .sys(client.db())?
            .table(identities)?
            .create_one(&row)
            .await?)
    }

    /// The identities a sender's address resolves through: verified or
    /// entered, not revoked.
    pub async fn resolve_sender(&self, kind: &str, address: &str) -> Result<Vec<Values>> {
        let identities = self.identities()?;
        let client = self.store().client().await?;
        let filter = obj(json!({
            "kind": { "eq": kind },
            "address": { "eq": normalise_address(kind, address) },
            "trust": { "in": ["verified", "entered"] },
            "revokedAt": { "isNull": true },
        }));
        Ok(self
            .sys(client.db())?
            .table(identities)?
            .find_many(Some(&filter), &many(10))
            .await?
            .rows)
    }

    /// Sends a message to an address on a channel kind with no notice: a
    /// code, a reset link. It answers the delivery's id, and refuses when no
    /// channel of the kind is up, or the send fails.
    pub async fn send_direct(&self, input: SendDirect) -> Result<String> {
        let channel = self.channel_of_kind(&input.channel, None).await?;
        let client = self.store().client().await?;
        let row = self
            .sys(client.db())?
            .table(&self.inner.parts.deliveries)?
            .create_one(&obj(json!({
                "purpose": input.purpose.as_deref().unwrap_or("code"),
                "recipient": input.recipient,
                "channelKind": input.channel,
                "address": input.to,
                "attempt": 1,
                "state": "queued",
            })))
            .await?;
        drop(client);
        let id = string(&row, "id");
        let now = at_text(self.now());
        let Some(channel) = channel else {
            let detail = format!("no {} channel is up", input.channel);
            self.update_delivery(
                &id,
                obj(
                    json!({ "state": "failed", "detail": detail, "failedAt": now, "endedAt": now }),
                ),
            )
            .await?;
            return Err(refused(detail));
        };
        let message = OutboundMessage {
            to: input.to.clone(),
            subject: Some(input.subject.unwrap_or_default()),
            body: input.body,
            idempotency_key: Some(id.clone()),
            ..OutboundMessage::default()
        };
        match channel.adapter.send(&message).await {
            Ok(sent) => {
                self.update_delivery(
                    &id,
                    obj(json!({
                        "state": wire_state(sent.state),
                        "slot": channel.slot,
                        "providerMessageId": sent.provider_message_id,
                        "sentAt": now,
                        "endedAt": now,
                    })),
                )
                .await?;
                Ok(id)
            }
            Err(err) => {
                let detail = err.to_string();
                self.update_delivery(&id, obj(json!({ "state": "failed", "slot": channel.slot, "detail": detail, "failedAt": now, "endedAt": now })))
                    .await?;
                Err(failed(detail))
            }
        }
    }

    /// Sends a code to an identity's address, which a reply on that
    /// channel, or [`Notifier::confirm_identity`], answers: `verify`, or
    /// `reset` for a password reset (D81).
    pub async fn start_verification(&self, id: &str, purpose: &str) -> Result<()> {
        let identities = self.identities()?;
        let client = self.store().client().await?;
        let mut by_id = Values::new();
        by_id.insert("id".to_owned(), eq(id));
        let sys = self.sys(client.db())?;
        let identity = sys
            .table(identities)?
            .find_one(Some(&by_id), &FindOptions::default())
            .await?
            .ok_or_else(|| refused("no such channel identity"))?;
        let code = random_code();
        let expires = self.now() + Duration::from_secs(CODE_TTL_SECONDS);
        sys.table(identities)?
            .update_one(
                &key(id),
                &obj(json!({
                    "codeHash": code_hash(&code),
                    "codePurpose": purpose,
                    "codeExpiresAt": at_text(expires),
                    "codeAttempts": 0,
                })),
                &none(),
            )
            .await?;
        drop(client);
        let reset = purpose == "reset";
        self.send_direct(SendDirect {
            channel: self.channel_kind_of_address(&string(&identity, "kind")),
            to: string(&identity, "address"),
            subject: Some(
                if reset {
                    "Your password reset code"
                } else {
                    "Your verification code"
                }
                .to_owned(),
            ),
            body: if reset {
                format!(
                    "Your password reset code is {code}. It expires in {} minutes.",
                    CODE_TTL_SECONDS / 60
                )
            } else {
                format!("Your code is {code}. Reply with it to confirm this address.")
            },
            recipient: Some(string(&identity, "principal")),
            purpose: Some("code".to_owned()),
        })
        .await?;
        Ok(())
    }

    /// Checks `code` against an identity's pending code of `purpose`; a
    /// wrong one counts, and the last attempt spends it. A code answered
    /// verifies the address.
    async fn check_code(&self, identity: &Values, code: &str, purpose: &str) -> Result<bool> {
        let identities = self.identities()?;
        if text(identity, "codePurpose") != Some(purpose) {
            return Ok(false);
        }
        let (Some(hash), Some(expires)) =
            (text(identity, "codeHash"), text(identity, "codeExpiresAt"))
        else {
            return Ok(false);
        };
        if wire_time::parse(expires).is_none_or(|at| at < self.now()) {
            return Ok(false);
        }
        let client = self.store().client().await?;
        let sys = self.sys(client.db())?;
        let id = key(&string(identity, "id"));
        if hash != code_hash(code) {
            let attempts = number(identity, "codeAttempts") + 1;
            let update = if attempts >= CODE_ATTEMPTS {
                obj(
                    json!({ "codeHash": null, "codePurpose": null, "codeExpiresAt": null, "codeAttempts": attempts }),
                )
            } else {
                obj(json!({ "codeAttempts": attempts }))
            };
            sys.table(identities)?
                .update_one(&id, &update, &none())
                .await?;
            return Ok(false);
        }
        sys.table(identities)?
            .update_one(
                &id,
                &obj(json!({
                    "codeHash": null, "codePurpose": null, "codeExpiresAt": null, "codeAttempts": 0,
                    "trust": "verified", "verifiedAt": at_text(self.now()),
                })),
                &none(),
            )
            .await?;
        Ok(true)
    }

    /// Checks a code given in the app; answers whether it verified.
    pub async fn confirm_identity(&self, id: &str, code: &str) -> Result<bool> {
        let identities = self.identities()?;
        let client = self.store().client().await?;
        let mut by_id = Values::new();
        by_id.insert("id".to_owned(), eq(id));
        let identity = self
            .sys(client.db())?
            .table(identities)?
            .find_one(Some(&by_id), &FindOptions::default())
            .await?;
        drop(client);
        match identity {
            Some(identity) => self.check_code(&identity, code, "verify").await,
            None => Ok(false),
        }
    }

    // ---- replies ----

    async fn trust_of(&self, channel: &str) -> Result<(String, Option<Vec<String>>)> {
        let Some(trust) = &self.inner.parts.trust else {
            return Ok(("low".to_owned(), Some(Vec::new())));
        };
        let client = self.store().client().await?;
        let mut filter = Values::new();
        filter.insert("channelKind".to_owned(), eq(channel));
        let row = self
            .sys(client.db())?
            .table(trust)?
            .find_one(Some(&filter), &FindOptions::default())
            .await?;
        // A channel kind with no row of its own is low: a reply on it
        // acknowledges and runs nothing else.
        Ok(match row {
            None => ("low".to_owned(), Some(Vec::new())),
            Some(row) => (string(&row, "level"), strings(row.get("permissions"))),
        })
    }

    async fn record<C: PgClient>(
        &self,
        client: Db<'_, C>,
        values: Values,
    ) -> Result<Option<String>> {
        let Some(replies) = &self.inner.parts.replies else {
            return Ok(None);
        };
        let row = self
            .sys(client)?
            .table(replies)?
            .create_one(&values)
            .await?;
        Ok(Some(string(&row, "id")))
    }

    async fn record_alone(&self, values: Values) -> Result<Option<String>> {
        let client = self.store().client().await?;
        self.record(client.db(), values).await
    }

    async fn echo(&self, input: &ReplyInput, address: &str, text: &str, recipient: Option<&str>) {
        let sent = async {
            let Some(channel) = self
                .channel_of_kind(&input.channel, input.slot.as_deref())
                .await?
            else {
                return Ok::<_, NotifierError>(());
            };
            let row = {
                let client = self.store().client().await?;
                self.sys(client.db())?
                    .table(&self.inner.parts.deliveries)?
                    .create_one(&obj(json!({
                        "purpose": "echo",
                        "recipient": recipient,
                        "channelKind": input.channel,
                        "slot": channel.slot,
                        "address": address,
                        "attempt": 1,
                        "state": "queued",
                    })))
                    .await?
            };
            let id = string(&row, "id");
            let message = OutboundMessage {
                to: address.to_owned(),
                body: text.to_owned(),
                idempotency_key: Some(id.clone()),
                ..OutboundMessage::default()
            };
            let answer = channel.adapter.send(&message).await.map_err(failed)?;
            let now = at_text(self.now());
            self.update_delivery(
                &id,
                obj(json!({
                    "state": wire_state(answer.state),
                    "providerMessageId": answer.provider_message_id,
                    "sentAt": now,
                    "endedAt": now,
                })),
            )
            .await
        }
        .await;
        if let Err(err) = sent {
            tracing::warn!(detail = %err, "echo failed");
        }
    }

    fn action_of(&self, kind: &str, verb: &str) -> Option<Arc<dyn ReplyAction>> {
        self.inner
            .replies
            .get(kind)
            .and_then(|verbs| verbs.get(verb))
            .or_else(|| {
                self.inner
                    .replies
                    .get("*")
                    .and_then(|verbs| verbs.get(verb))
            })
            .cloned()
    }

    /// The repositories of `tx` acting as `principal`, for a reply's
    /// action: its reads and writes keep to what it sees, and its
    /// behaviors' commands run with its permissions.
    fn acting<'c, 't>(
        &'c self,
        tx: Db<'c, PoolTransaction<'t>>,
        principal: &Principal,
    ) -> Result<StoreRepositories<'c, PoolTransaction<'t>>> {
        let mut repositories = tx
            .repositories(self.inner.definition)?
            .acting(Some(principal.subject.clone()))
            .with_access(AccessCaller::of(Some(principal)))
            .with_caller(Caller::of(Some(principal)));
        if let Some(keys) = self.inner.tables.keys() {
            repositories = repositories.with_keys(keys.as_ref());
        }
        repositories = repositories.with_rules_of(&self.inner.tables);
        Ok(repositories)
    }

    /// The entry point of an inbound message (D153, D157): a reply, a
    /// verification code, or a sender the notifier does not know.
    pub async fn reply(&self, input: ReplyInput) -> Result<ReplyOutcome> {
        let address_kind = self.address_kind_of(&input.channel);
        let address = normalise_address(&address_kind, &input.from);
        let body_text = input.body.trim().to_owned();
        let mut base = obj(json!({
            "receivedAt": at_text(input.received_at.unwrap_or_else(|| self.now())),
            "channelKind": input.channel,
            "slot": input.slot,
            "address": address,
            "providerMessageId": input.provider_message_id,
            "body": input.body,
            "interpreter": "keyword",
        }));
        let identities = if self.inner.parts.identities.is_some() {
            self.resolve_sender(&address_kind, &address).await?
        } else {
            Vec::new()
        };
        if identities.is_empty() {
            return self
                .reply_of_no_one(&input, &address, &address_kind, &body_text, &base)
                .await;
        }
        let principals: BTreeSet<String> = identities
            .iter()
            .map(|i| {
                format!(
                    "{}:{}",
                    string(i, "principalKind"),
                    same_id(&string(i, "principal"))
                )
            })
            .collect();
        if principals.len() != 1 {
            let detail = "the address resolves to more than one principal";
            let mut values = base.clone();
            values.insert("outcome".to_owned(), json!("ambiguous"));
            values.insert("detail".to_owned(), json!(detail));
            let reply = self.record_alone(values).await?;
            return Ok(ReplyOutcome {
                outcome: "ambiguous".to_owned(),
                reply,
                detail: Some(detail.to_owned()),
                ..ReplyOutcome::default()
            });
        }
        let identity = &identities[0];
        let recipient = RecipientRef {
            kind: string(identity, "principalKind"),
            id: string(identity, "principal"),
            table: text(identity, "principalTable").map(str::to_owned),
        };
        base.insert(
            "identity".to_owned(),
            identity.get("id").cloned().unwrap_or(Value::Null),
        );
        base.insert(
            "principal".to_owned(),
            identity.get("principal").cloned().unwrap_or(Value::Null),
        );
        let read = read_reply(&body_text);
        let verb = read.verb.clone();
        let reference = read.reference;
        let ctx = Replying {
            notifier: self,
            input: &input,
            address: &address,
            recipient: &recipient,
            base: &base,
            verb: &verb,
            reference,
        };

        if read.undoes() {
            return self.reply_undo(&ctx).await;
        }
        let notice = match self.answered_notice(&ctx).await? {
            Ok(notice) => notice,
            Err(refusal) => return Ok(refusal),
        };
        let notice_key = string(&notice, "id");
        let notice_ref = number(&notice, "reference");
        let title = string(&notice, "title");

        let (level, permissions) = self.trust_of(&input.channel).await?;
        // An identity entered by hand runs at the lowest trust: it
        // acknowledges and runs nothing else.
        let entered = text(identity, "trust") == Some("entered");
        let cap = if entered {
            Some(Vec::new())
        } else {
            permissions
        };
        let held = self.permissions_of(&recipient).await?;
        let principal = Principal::new(
            recipient.id.as_str(),
            narrowed_permissions(&held, cap.as_deref()),
        )
        .of_kind(recipient.principal_kind());

        if read.acknowledges() {
            return self.reply_ack(&ctx, &notice).await;
        }

        let kind = string(&notice, "eventKind");
        let Some(action) = self.action_of(&kind, &verb) else {
            return ctx
                .refuse(
                    "refused",
                    &format!("\"{verb}\" is no reply {notice_ref} takes."),
                    Some(&notice_key),
                )
                .await;
        };
        if let Some(need) = action.permission() {
            if !reply_allows(&held, cap.as_deref(), need) {
                let detail = if held.iter().any(|h| covers(h, need)) {
                    format!(
                        "A reply by {} may not {verb}: its trust is {}. Use the app.",
                        input.channel,
                        if entered { "entered by hand" } else { &level }
                    )
                } else {
                    format!("You may not {verb} {notice_ref}.")
                };
                return ctx.refuse("refused", &detail, Some(&notice_key)).await;
            }
        }
        let ran: Result<Option<String>> = async {
            let mut client = self.store().client().await?;
            let tx = client.transaction().await?;
            let reply = {
                let acting = self.acting(tx.db(), &principal)?;
                let context = ReplyContext {
                    repositories: &acting,
                    notice: &notice,
                    principal: &principal,
                    args: &read.args,
                    channel: &input.channel,
                    now: self.now(),
                };
                let data = action.run(&context).await.map_err(failed)?;
                let now = at_text(self.now());
                let acknowledged_at = notice
                    .get("acknowledgedAt")
                    .filter(|v| !v.is_null())
                    .cloned()
                    .unwrap_or_else(|| now.clone());
                self.sys(tx.db())?
                    .table(&self.inner.parts.inbox)?
                    .update_one(&key(&notice_key), &obj(json!({ "state": "done", "acknowledgedAt": acknowledged_at, "acknowledgedVia": input.channel })), &none())
                    .await?;
                self.end_deliveries(tx.db(), &notice_key, |state| !ended(state)).await?;
                let mut values = base.clone();
                values.extend(obj(json!({
                    "verb": verb,
                    "notice": notice_key,
                    "reference": notice_ref,
                    "outcome": "done",
                    "undo": if action.undoable() { json!({ "data": data, "previous": notice.get("state") }) } else { Value::Null },
                })));
                self.record(tx.db(), values).await?
            };
            tx.commit().await?;
            Ok(reply)
        }
        .await;
        let reply = match ran {
            Ok(reply) => reply,
            Err(err) => {
                return ctx
                    .refuse(
                        "failed",
                        &format!("{verb} {notice_ref} failed: {err}"),
                        Some(&notice_key),
                    )
                    .await;
            }
        };
        let describe = action
            .describe()
            .map_or_else(|| format!("Done: {verb}"), str::to_owned);
        let undo = if action.undoable() {
            format!(" Reply UNDO {notice_ref} to undo.")
        } else {
            " This cannot be undone here.".to_owned()
        };
        let said = format!("{describe} {notice_ref}: {title}.{undo}");
        self.echo(&input, &address, &said, Some(&recipient.id))
            .await;
        Ok(ReplyOutcome {
            outcome: "done".to_owned(),
            reply,
            notice: Some(notice_key),
            verb: Some(verb),
            echo: Some(said),
            principal: Some(recipient.clone()),
            ..ReplyOutcome::default()
        })
    }

    /// A reply from an address no verified identity holds: the code an
    /// address with one pending answers, or `unknown`, which D157's triage
    /// takes.
    async fn reply_of_no_one(
        &self,
        input: &ReplyInput,
        address: &str,
        address_kind: &str,
        body: &str,
        base: &Values,
    ) -> Result<ReplyOutcome> {
        // An address with a code pending verifies by replying with it.
        if let (Some(table), true) = (&self.inner.parts.identities, looks_like_code(body)) {
            let pending = {
                let client = self.store().client().await?;
                let filter = obj(json!({
                    "kind": { "eq": address_kind },
                    "address": { "eq": address },
                    "codePurpose": { "eq": "verify" },
                    "revokedAt": { "isNull": true },
                }));
                self.sys(client.db())?
                    .table(table)?
                    .find_many(Some(&filter), &many(10))
                    .await?
                    .rows
            };
            for identity in pending {
                if !self.check_code(&identity, body, "verify").await? {
                    continue;
                }
                let mut values = base.clone();
                values.insert(
                    "identity".to_owned(),
                    identity.get("id").cloned().unwrap_or(Value::Null),
                );
                values.insert(
                    "principal".to_owned(),
                    identity.get("principal").cloned().unwrap_or(Value::Null),
                );
                values.insert("verb".to_owned(), json!("verify"));
                values.insert("outcome".to_owned(), json!("done"));
                let reply = self.record_alone(values).await?;
                let principal = string(&identity, "principal");
                self.echo(
                    input,
                    address,
                    "This address is confirmed.",
                    Some(&principal),
                )
                .await;
                return Ok(ReplyOutcome {
                    outcome: "verified".to_owned(),
                    reply,
                    verb: Some("verify".to_owned()),
                    principal: Some(RecipientRef {
                        kind: string(&identity, "principalKind"),
                        id: principal,
                        table: None,
                    }),
                    ..ReplyOutcome::default()
                });
            }
        }
        let mut values = base.clone();
        values.insert("outcome".to_owned(), json!("unknown"));
        values.insert(
            "detail".to_owned(),
            json!("no verified identity holds the address"),
        );
        let reply = self.record_alone(values).await?;
        Ok(ReplyOutcome {
            outcome: "unknown".to_owned(),
            reply,
            ..ReplyOutcome::default()
        })
    }

    /// `undo <reference>`: undoes the latest action of the sender's on the
    /// notice.
    async fn reply_undo(&self, ctx: &Replying<'_>) -> Result<ReplyOutcome> {
        let (Some(reference), Some(replies)) = (ctx.reference, self.inner.parts.replies.clone())
        else {
            return ctx
                .refuse("ambiguous", "Reply UNDO and the reference number.", None)
                .await;
        };
        let Some(notice) = self.notice_of(ctx.recipient, reference).await? else {
            return ctx
                .refuse("refused", &format!("No notice {reference} is yours."), None)
                .await;
        };
        let notice_key = string(&notice, "id");
        let done = {
            let client = self.store().client().await?;
            let filter = obj(json!({
                "notice": { "eq": notice_key },
                "principal": { "eq": ctx.recipient.id },
                "outcome": { "eq": "done" },
                "undoneAt": { "isNull": true },
            }));
            self.sys(client.db())?
                .table(&replies)?
                .find_many(Some(&filter), &ordered(1, OrderBy::desc("receivedAt")))
                .await?
                .rows
                .into_iter()
                .next()
        };
        let Some(done) = done else {
            return ctx
                .refuse(
                    "refused",
                    &format!("Nothing to undo on {reference}."),
                    Some(&notice_key),
                )
                .await;
        };
        self.run_undo(ctx, &notice, &done).await
    }

    /// Which notice a reply answers: the one its reference names, or the
    /// one notice of the sender's that waits on the channel and address; a
    /// refusal for none, or for more than one.
    async fn answered_notice(
        &self,
        ctx: &Replying<'_>,
    ) -> Result<std::result::Result<Values, ReplyOutcome>> {
        if let Some(reference) = ctx.reference {
            return Ok(match self.notice_of(ctx.recipient, reference).await? {
                Some(notice) => Ok(notice),
                None => Err(ctx
                    .refuse("refused", &format!("No notice {reference} is yours."), None)
                    .await?),
            });
        }
        let mut waiting = Vec::new();
        {
            let client = self.store().client().await?;
            let sys = self.sys(client.db())?;
            let filter = obj(
                json!({ "recipient": { "eq": ctx.recipient.id }, "state": { "in": ["unread", "read"] } }),
            );
            let open = sys
                .table(&self.inner.parts.inbox)?
                .find_many(Some(&filter), &ordered(50, OrderBy::desc("reference")))
                .await?
                .rows;
            for candidate in open {
                let sent = obj(json!({
                    "notice": { "eq": candidate.get("id") },
                    "channelKind": { "eq": ctx.input.channel },
                    "address": { "eq": ctx.address },
                }));
                if sys
                    .table(&self.inner.parts.deliveries)?
                    .find_one(Some(&sent), &FindOptions::default())
                    .await?
                    .is_some()
                {
                    waiting.push(candidate);
                }
            }
        }
        if waiting.len() == 1 {
            return Ok(Ok(waiting.remove(0)));
        }
        let detail = if waiting.is_empty() {
            "Nothing is waiting on you here.".to_owned()
        } else {
            let names: Vec<String> = waiting
                .iter()
                .map(|n| format!("#{}", number(n, "reference")))
                .collect();
            format!("Name the reference: {}.", names.join(", "))
        };
        Ok(Err(ctx.refuse("ambiguous", &detail, None).await?))
    }

    /// `ack`: acknowledges the notice, which stops its escalation.
    async fn reply_ack(&self, ctx: &Replying<'_>, notice: &Values) -> Result<ReplyOutcome> {
        let notice_key = string(notice, "id");
        let notice_ref = number(notice, "reference");
        if matches!(text(notice, "state"), Some("acknowledged" | "done")) {
            return ctx
                .refuse(
                    "refused",
                    &format!("{notice_ref} is acknowledged already."),
                    Some(&notice_key),
                )
                .await;
        }
        let mut client = self.store().client().await?;
        let tx = client.transaction().await?;
        self.acknowledge_in(tx.db(), notice, &ctx.input.channel)
            .await?;
        let mut values = ctx.base.clone();
        values.extend(obj(json!({
            "verb": "ack",
            "notice": notice_key,
            "reference": notice_ref,
            "outcome": "done",
            "undo": { "previous": notice.get("state") },
        })));
        let reply = self.record(tx.db(), values).await?;
        tx.commit().await?;
        drop(client);
        let title = string(notice, "title");
        let said = format!("Acknowledged {notice_ref}: {title}. Reply UNDO {notice_ref} to undo.");
        self.echo(ctx.input, ctx.address, &said, Some(&ctx.recipient.id))
            .await;
        Ok(ReplyOutcome {
            outcome: "done".to_owned(),
            reply,
            notice: Some(notice_key),
            verb: Some("ack".to_owned()),
            echo: Some(said),
            principal: Some(ctx.recipient.clone()),
            ..ReplyOutcome::default()
        })
    }

    async fn notice_of(&self, recipient: &RecipientRef, reference: i64) -> Result<Option<Values>> {
        let client = self.store().client().await?;
        let filter =
            obj(json!({ "recipient": { "eq": recipient.id }, "reference": { "eq": reference } }));
        Ok(self
            .sys(client.db())?
            .table(&self.inner.parts.inbox)?
            .find_one(Some(&filter), &FindOptions::default())
            .await?)
    }

    async fn run_undo(
        &self,
        ctx: &Replying<'_>,
        notice: &Values,
        done: &Values,
    ) -> Result<ReplyOutcome> {
        let undo = done.get("undo").filter(|u| !u.is_null()).cloned();
        let held = self.permissions_of(ctx.recipient).await?;
        let (_, cap) = self.trust_of(&ctx.input.channel).await?;
        let principal = Principal::new(
            ctx.recipient.id.as_str(),
            narrowed_permissions(&held, cap.as_deref()),
        )
        .of_kind(ctx.recipient.principal_kind());
        let verb = string(done, "verb");
        let notice_key = string(notice, "id");
        let notice_ref = number(notice, "reference");
        let action = if verb == "ack" {
            None
        } else {
            self.action_of(&string(notice, "eventKind"), &verb)
        };
        let Some(undo) =
            undo.filter(|_| verb == "ack" || action.as_ref().is_some_and(|a| a.undoable()))
        else {
            let mut values = ctx.base.clone();
            values.extend(obj(json!({
                "verb": UNDO_VERB,
                "notice": notice_key,
                "reference": notice_ref,
                "outcome": "refused",
                "detail": format!("{verb} cannot be undone here"),
            })));
            let reply = self.record_alone(values).await?;
            return Ok(ReplyOutcome {
                outcome: "refused".to_owned(),
                reply,
                notice: Some(notice_key),
                verb: Some(UNDO_VERB.to_owned()),
                principal: Some(ctx.recipient.clone()),
                ..ReplyOutcome::default()
            });
        };
        let mut client = self.store().client().await?;
        let tx = client.transaction().await?;
        if let Some(action) = &action {
            let acting = self.acting(tx.db(), &principal)?;
            let no_args: Vec<String> = Vec::new();
            let context = ReplyContext {
                repositories: &acting,
                notice,
                principal: &principal,
                args: &no_args,
                channel: &ctx.input.channel,
                now: self.now(),
            };
            action
                .undo(&context, undo.get("data").unwrap_or(&Value::Null))
                .await
                .map_err(failed)?;
        }
        let previous = undo
            .get("previous")
            .and_then(Value::as_str)
            .unwrap_or("read");
        self.sys(tx.db())?
            .table(&self.inner.parts.inbox)?
            .update_one(
                &key(&notice_key),
                &obj(json!({ "state": previous, "acknowledgedAt": null, "acknowledgedVia": null })),
                &none(),
            )
            .await?;
        if let Some(replies) = &self.inner.parts.replies {
            self.sys(tx.db())?
                .table(replies)?
                .update_one(
                    &key(&string(done, "id")),
                    &obj(json!({ "undoneAt": at_text(self.now()) })),
                    &none(),
                )
                .await?;
        }
        let mut values = ctx.base.clone();
        values.extend(obj(json!({ "verb": UNDO_VERB, "notice": notice_key, "reference": notice_ref, "outcome": "done" })));
        let reply = self.record(tx.db(), values).await?;
        tx.commit().await?;
        drop(client);
        let said = format!("Undone: {verb} {notice_ref}.");
        self.echo(ctx.input, ctx.address, &said, Some(&ctx.recipient.id))
            .await;
        Ok(ReplyOutcome {
            outcome: "done".to_owned(),
            reply,
            notice: Some(notice_key),
            verb: Some(UNDO_VERB.to_owned()),
            echo: Some(said),
            principal: Some(ctx.recipient.clone()),
            ..ReplyOutcome::default()
        })
    }

    fn slot_channel(&self, slot: &str) -> Result<Channel> {
        self.channels()
            .into_iter()
            .find(|c| c.slot == slot)
            .ok_or_else(|| refused(format!("slot {slot} is no channel of this environment")))
    }

    /// The replies an adapter's `@webhook` request carries, each through
    /// [`Notifier::reply`]; none for an adapter that reads no inbound
    /// messages.
    pub async fn inbound(&self, slot: &str, request: &WebhookRequest) -> Result<Vec<ReplyOutcome>> {
        let channel = self.slot_channel(slot)?;
        if !channel.inbound {
            return Ok(Vec::new());
        }
        let messages = channel.adapter.inbound(request).await.map_err(failed)?;
        let mut out = Vec::new();
        for message in messages {
            out.push(
                self.reply(ReplyInput {
                    channel: channel.kind.clone(),
                    slot: Some(slot.to_owned()),
                    from: message.from,
                    body: message.body,
                    received_at: Some(message.received_at),
                    provider_message_id: Some(message.provider_message_id),
                })
                .await?,
            );
        }
        Ok(out)
    }

    /// The delivery statuses an adapter's `@webhook` request carries,
    /// applied to the deliveries they name; answers how many moved.
    pub async fn statuses(&self, slot: &str, request: &WebhookRequest) -> Result<u64> {
        let channel = self.slot_channel(slot)?;
        if !channel.status {
            return Ok(0);
        }
        let mut moved = 0;
        for status in channel.adapter.status(request).await.map_err(failed)? {
            let delivery = {
                let client = self.store().client().await?;
                let mut filter = Values::new();
                filter.insert(
                    "providerMessageId".to_owned(),
                    eq(status.provider_message_id.as_str()),
                );
                self.sys(client.db())?
                    .table(&self.inner.parts.deliveries)?
                    .find_one(Some(&filter), &FindOptions::default())
                    .await?
            };
            let Some(delivery) = delivery else { continue };
            let current = string(&delivery, "state");
            let state = match status.state {
                crate::providers::DeliveryState::Queued => "queued",
                crate::providers::DeliveryState::Sent => "sent",
                crate::providers::DeliveryState::Delivered => "delivered",
                crate::providers::DeliveryState::Read => "read",
                crate::providers::DeliveryState::Failed => "failed",
            };
            if ended(&current) || state_order(state) <= state_order(&current) {
                continue;
            }
            if state == "failed" {
                let detail = status
                    .detail
                    .clone()
                    .unwrap_or_else(|| "the service reported a failure".to_owned());
                self.fall_through_alone(&delivery, &detail).await?;
            } else {
                let at = at_text(status.at);
                let mut update = obj(json!({ "state": state }));
                if state == "delivered" {
                    update.insert("deliveredAt".to_owned(), at.clone());
                }
                if state == "read" {
                    update.insert("readAt".to_owned(), at.clone());
                    update.insert("endedAt".to_owned(), at);
                }
                self.update_delivery(&string(&delivery, "id"), update)
                    .await?;
            }
            moved += 1;
        }
        Ok(moved)
    }

    /// The health of every channel adapter, each asked afresh; one that
    /// fails raises `channel.down` once while it stays down.
    pub async fn check_health(&self) -> Result<Vec<ChannelHealthOf>> {
        let mut out = Vec::new();
        for channel in self.channels() {
            self.inner
                .health
                .lock()
                .unwrap_or_else(PoisonError::into_inner)
                .remove(&channel.slot);
            let (ok, detail) = self.health_of(&channel).await?;
            out.push(ChannelHealthOf {
                slot: channel.slot.clone(),
                kind: channel.kind.clone(),
                ok,
                detail,
            });
        }
        Ok(out)
    }
}

/// What a reply's refusals share.
struct Replying<'a> {
    notifier: &'a Notifier,
    input: &'a ReplyInput,
    address: &'a str,
    recipient: &'a RecipientRef,
    base: &'a Values,
    verb: &'a str,
    reference: Option<i64>,
}

impl Replying<'_> {
    async fn refuse(
        &self,
        outcome: &str,
        detail: &str,
        notice: Option<&str>,
    ) -> Result<ReplyOutcome> {
        let mut values = self.base.clone();
        values.insert("verb".to_owned(), json!(self.verb));
        if let Some(reference) = self.reference {
            values.insert("reference".to_owned(), json!(reference));
        }
        values.insert("outcome".to_owned(), json!(outcome));
        values.insert("detail".to_owned(), json!(detail));
        if let Some(notice) = notice {
            values.insert("notice".to_owned(), json!(notice));
        }
        let reply = self.notifier.record_alone(values).await?;
        self.notifier
            .echo(self.input, self.address, detail, Some(&self.recipient.id))
            .await;
        Ok(ReplyOutcome {
            outcome: outcome.to_owned(),
            reply,
            notice: notice.map(str::to_owned),
            verb: Some(self.verb.to_owned()),
            echo: Some(detail.to_owned()),
            detail: Some(detail.to_owned()),
            principal: Some(self.recipient.clone()),
        })
    }
}

/// The dispatch queue as a worker claims it (D80): the queue's claims,
/// whose prune deletes the deliveries past their retention too.
pub struct NotifierQueue {
    queue: PartQueue<NoticeWork>,
    notifier: Notifier,
}

impl NotifierQueue {
    /// The queue's claims.
    pub fn queue(&self) -> &PartQueue<NoticeWork> {
        &self.queue
    }
}

#[async_trait]
impl WorkerQueue for NotifierQueue {
    type Message = NoticeWork;

    fn name(&self) -> &str {
        self.queue.name()
    }

    fn lease(&self) -> Duration {
        self.queue.lease()
    }

    async fn claim(&self, n: usize, lease: Duration) -> Result<Vec<Claim<NoticeWork>>, QueueError> {
        self.queue.claim(n, lease).await
    }

    async fn extend(&self, id: &str, token: &str, lease: Duration) -> Result<(), QueueError> {
        self.queue.extend(id, token, lease).await
    }

    async fn complete(&self, id: &str, token: &str) -> Result<(), QueueError> {
        self.queue.complete(id, token).await
    }

    async fn fail(&self, id: &str, token: &str, cause: &str) -> Result<bool, QueueError> {
        self.queue.fail(id, token, cause).await
    }

    async fn release(&self, id: &str, token: &str) -> Result<(), QueueError> {
        self.queue.release(id, token).await
    }

    async fn prune(&self, limit: usize) -> Result<u64, QueueError> {
        let done = self.queue.prune(limit).await?;
        let deliveries = self
            .notifier
            .prune(u32::try_from(limit).unwrap_or(u32::MAX))
            .await
            .map_err(|err| QueueError::Failed(err.to_string()))?;
        Ok(done + deliveries)
    }

    async fn slot(&self, slots: usize) -> Result<Option<Slot>, QueueError> {
        self.queue.slot(slots).await
    }

    async fn next_due(&self) -> Result<Option<Duration>, QueueError> {
        self.queue.next_due().await
    }
}

/// What [`PasswordReset`] needs of the identity service (D81): the user a
/// login names, and the write of a new password, which ends every session.
#[async_trait]
pub trait ResetIdentity: Send + Sync {
    /// The id of the user `login` names, or none.
    async fn user_of_login(&self, login: &str) -> Option<String>;
    /// Sets the user's password.
    async fn reset_password(&self, user: &str, password: &str) -> Result<(), BoxError>;
}

#[cfg(feature = "identity")]
#[async_trait]
impl ResetIdentity for crate::identity::Service {
    async fn user_of_login(&self, login: &str) -> Option<String> {
        self.store()
            .find_login(login)
            .await
            .ok()
            .map(|record| record.user.id)
    }

    async fn reset_password(&self, user: &str, password: &str) -> Result<(), BoxError> {
        crate::identity::Service::reset_password(self, user, password)
            .await
            .map_err(|err| -> BoxError { err.to_string().into() })
    }
}

/// Password reset over a channel (D81, D153): `request` sends a code to
/// the email address the login names, through the email adapter, held on
/// the user's email channel identity; `complete` checks it and writes the
/// new password, which ends every session of the user, and marks the
/// address verified, since its owner proved they read it. `request`
/// answers alike for a login that does not exist. The TypeScript runtime's
/// `passwordReset`.
pub struct PasswordReset {
    notifier: Notifier,
    identity: Arc<dyn ResetIdentity>,
}

impl PasswordReset {
    /// Password reset through `notifier` over `identity`.
    pub fn new(notifier: Notifier, identity: Arc<dyn ResetIdentity>) -> Self {
        Self { notifier, identity }
    }

    async fn identity_of(&self, login: &str) -> Result<Option<Values>> {
        let Some(user) = self.identity.user_of_login(login).await else {
            return Ok(None);
        };
        let identities = self.notifier.identities()?.to_owned();
        let address = normalise_address("email", login);
        let client = self.notifier.store().client().await?;
        let sys = self.notifier.sys(client.db())?;
        let filter = obj(json!({
            "kind": { "eq": "email" },
            "address": { "eq": address },
            "principal": { "eq": user },
            "revokedAt": { "isNull": true },
        }));
        if let Some(existing) = sys
            .table(&identities)?
            .find_one(Some(&filter), &FindOptions::default())
            .await?
        {
            return Ok(Some(existing));
        }
        if !address.contains('@') {
            return Ok(None);
        }
        Ok(Some(
            sys.table(&identities)?
                .create_one(&obj(json!({
                    "kind": "email",
                    "address": address,
                    "principalKind": "user",
                    "principal": user,
                    "trust": "unverified",
                })))
                .await?,
        ))
    }

    /// Sends a reset code to the login's address; nothing for a login that
    /// does not exist, alike.
    pub async fn request(&self, login: &str) -> Result<()> {
        let Some(identity) = self.identity_of(login).await? else {
            return Ok(());
        };
        self.notifier
            .start_verification(&string(&identity, "id"), "reset")
            .await
    }

    /// Checks the code and sets the password; answers whether it did.
    pub async fn complete(&self, login: &str, code: &str, password: &str) -> Result<bool> {
        let Some(identity) = self.identity_of(login).await? else {
            return Ok(false);
        };
        let identities = self.notifier.identities()?.to_owned();
        if text(&identity, "codePurpose") != Some("reset") {
            return Ok(false);
        }
        let (Some(hash), Some(expires)) = (
            text(&identity, "codeHash"),
            text(&identity, "codeExpiresAt"),
        ) else {
            return Ok(false);
        };
        if wire_time::parse(expires).is_none_or(|at| at < SystemTime::now()) {
            return Ok(false);
        }
        let id = key(&string(&identity, "id"));
        if hash != code_hash(code) {
            let attempts = number(&identity, "codeAttempts") + 1;
            let update = if attempts >= CODE_ATTEMPTS {
                obj(
                    json!({ "codeHash": null, "codePurpose": null, "codeExpiresAt": null, "codeAttempts": attempts }),
                )
            } else {
                obj(json!({ "codeAttempts": attempts }))
            };
            let client = self.notifier.store().client().await?;
            self.notifier
                .sys(client.db())?
                .table(&identities)?
                .update_one(&id, &update, &none())
                .await?;
            return Ok(false);
        }
        self.identity
            .reset_password(&string(&identity, "principal"), password)
            .await
            .map_err(failed)?;
        let client = self.notifier.store().client().await?;
        self.notifier
            .sys(client.db())?
            .table(&identities)?
            .update_one(
                &id,
                &obj(json!({
                    "codeHash": null, "codePurpose": null, "codeExpiresAt": null, "codeAttempts": 0,
                    "trust": "verified", "verifiedAt": at_text(SystemTime::now()),
                })),
                &none(),
            )
            .await?;
        Ok(true)
    }
}
