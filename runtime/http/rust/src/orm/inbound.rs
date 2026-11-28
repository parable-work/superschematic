//! Outside parties and inbound messages (D157, D193): one path every
//! inbound message takes, conversations whose messages are rows, and
//! sending to a party as a permission. The TypeScript runtime's
//! `inbound.ts`, with its tables, statements, states and refusals.
//!
//! The path. An adapter's `@webhook` request ([`Inbound::inbound`]) gives
//! its messages, each of which [`Inbound::receive`] takes:
//!
//! 1. The sender's address is normalised as its channel's kind of address.
//! 2. An email to another address than the deployment's inbound address is
//!    not the deployment's, and is dropped (`ignored`). An email whose
//!    adapter does not report DMARC pass, or reports SPF or DKIM failing,
//!    is from an unknown sender ([`email_authenticated`]): the adapter
//!    reports, the core decides.
//! 3. An opt-out or opt-in keyword the adapter read is recorded on every
//!    identity of the address, and nothing is sent to an address that
//!    opted out.
//! 4. The address resolves through the channel identities (D153): a
//!    user's is a reply, the notifier's, as is; a party's whose grants are
//!    active is a row of the party's conversation on the channel, its files
//!    filed to it, and a run request `{ trigger: "inbound", party, message,
//!    grant }` on the `@runRequests` queue, which an agent run (D158)
//!    takes; a party's with no active grant is on the party's timeline and
//!    held in triage; a party's whose grants have all ended since it was
//!    verified (it lapsed) is held in triage as an unknown sender's until
//!    its identity is verified again; no one's, an address two parties
//!    share that no thread settles, or a user's and a party's at once, is
//!    held in triage, given to no one.
//!
//! Sending to a party ([`Inbound::send`]) is a permission (`parties.message`
//! unless set), held to the sender's read of the party's row, to the
//! party's identity on the channel (verified or entered, not opted out, not
//! lapsed), to a rate limit per party, and to an approval policy the app or
//! the agent dispatcher plugs in ([`SendApproval`]): allow sends, deny
//! refuses, ask holds the message awaiting approval until
//! [`Inbound::release`] sends it.
//!
//! On Postgres and on a SQLite file (#618), as the notifier is, over
//! [`Store`](super::store::Store). Rust has no ambient caller (D181), so
//! a read as a principal opens the repositories for it (`with_access`).

use std::collections::BTreeMap;
use std::fmt;
use std::sync::{Arc, OnceLock};
use std::time::{Duration, SystemTime};

use async_trait::async_trait;
use serde::{Deserialize, Serialize};
use serde_json::{json, Value};

use super::notify::{
    default_address_kind, normalise_address, same_id, Notifier, NotifierError, ReplyInput,
    ReplyOutcome,
};
use super::parties::{identity_lapsed, party_principal, party_tables_of, same_uuid, PartyRef};
#[cfg(feature = "postgres")]
use super::postgres::deadpool_postgres;
use super::rowfeatures::{BeginFile, FileOnto, FileRef, RowRef};
use super::store::{Db, PartQueue, PgClient, Store, StoreRepositories};
use super::{
    rfc3339_millis, AccessCaller, Definition, Error, FindOptions, OrderBy, Values, WriteOptions,
};
use crate::access::ActiveGrantsFilter;
use crate::auth::{covers, Principal};
use crate::bucket::{Body, Bucket, PutOptions};
use crate::civil::parse_rfc3339;
use crate::providers::{
    ChannelAdapter, InboundAttachment, InboundAuthentication, InboundMessage, OptOut,
    OutboundMessage, Providers, RuntimeInterface, WebhookRequest,
};
use crate::queue::{QueueDefinition, QueueError};
use crate::BoxError;

/// The refusal or failure of an inbound call.
#[derive(Debug)]
pub enum InboundError {
    /// A call the path refuses: no permission, no such party, a message
    /// that awaits no approval. The TypeScript runtime's `InboundError`.
    Refused(String),
    /// A repository's read or write failed.
    Orm(Error),
    /// The notifier refused or failed.
    Notifier(NotifierError),
    /// The run requests queue's write failed.
    Queue(QueueError),
    /// The database or a channel failed.
    Failed(String),
}

impl fmt::Display for InboundError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            InboundError::Refused(message) | InboundError::Failed(message) => f.write_str(message),
            InboundError::Orm(err) => write!(f, "{err}"),
            InboundError::Notifier(err) => write!(f, "{err}"),
            InboundError::Queue(err) => write!(f, "{err}"),
        }
    }
}

impl std::error::Error for InboundError {}

impl From<Error> for InboundError {
    fn from(err: Error) -> Self {
        InboundError::Orm(err)
    }
}

impl From<NotifierError> for InboundError {
    fn from(err: NotifierError) -> Self {
        InboundError::Notifier(err)
    }
}

impl From<QueueError> for InboundError {
    fn from(err: QueueError) -> Self {
        InboundError::Queue(err)
    }
}

#[cfg(feature = "postgres")]
impl From<deadpool_postgres::tokio_postgres::Error> for InboundError {
    fn from(err: deadpool_postgres::tokio_postgres::Error) -> Self {
        InboundError::Failed(crate::error::chain(&err))
    }
}

#[cfg(feature = "postgres")]
impl From<deadpool_postgres::PoolError> for InboundError {
    fn from(err: deadpool_postgres::PoolError) -> Self {
        InboundError::Failed(format!("the database is unreachable: {err}"))
    }
}

fn refused(message: impl Into<String>) -> InboundError {
    InboundError::Refused(message.into())
}

type Result<T, E = InboundError> = std::result::Result<T, E>;

/// The message of the `@runRequests` queue an inbound message from a party
/// with an active grant puts on it (D157).
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct RunRequest {
    /// `inbound`.
    pub trigger: String,
    /// The party the run acts for.
    pub party: PartyRef,
    /// The message row that asked for it, its key in base62 as the
    /// repositories write it, whatever form the queue's column read in.
    #[serde(deserialize_with = "wire_id")]
    pub message: String,
    /// The party's grant the run acts under: the one the message's thread
    /// ran under, while it is active, else the party's newest active grant.
    #[serde(deserialize_with = "wire_id")]
    pub grant: String,
}

/// A key read from a queue's `UUID` column, hyphenated there, in the form
/// the repositories write it: base62.
fn wire_id<'de, D: serde::Deserializer<'de>>(deserializer: D) -> Result<String, D::Error> {
    let text = String::deserialize(deserializer)?;
    Ok(same_id(&text))
}

/// What [`Inbound::receive`] takes: a message an adapter read, and the
/// channel kind and slot it came on.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct ReceivedMessage {
    /// The channel kind it came on: `sms`, `email`.
    pub channel: String,
    /// The slot whose adapter read it.
    pub slot: Option<String>,
    pub from: String,
    pub body: String,
    pub received_at: Option<SystemTime>,
    pub provider_message_id: Option<String>,
    pub in_reply_to: Option<String>,
    pub message_id: Option<String>,
    pub references: Vec<String>,
    pub to: Vec<String>,
    pub subject: Option<String>,
    pub authentication: Option<InboundAuthentication>,
    pub attachments: Vec<InboundAttachment>,
    pub opt_out: Option<OptOut>,
}

impl ReceivedMessage {
    /// A message of `body` from `from` on the channel kind `channel`.
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

    /// The message an adapter of the channel kind `channel` read on `slot`.
    pub fn read(channel: &str, slot: &str, message: InboundMessage) -> Self {
        Self {
            channel: channel.to_owned(),
            slot: Some(slot.to_owned()),
            from: message.from,
            body: message.body,
            received_at: Some(message.received_at),
            provider_message_id: Some(message.provider_message_id).filter(|id| !id.is_empty()),
            in_reply_to: message.in_reply_to,
            message_id: message.message_id,
            references: message.references,
            to: message.to,
            subject: message.subject,
            authentication: message.authentication,
            attachments: message.attachments,
            opt_out: message.opt_out,
        }
    }
}

/// What [`Inbound::receive`] came to.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct InboundOutcome {
    /// `reply`, `run`, `timeline`, `unknown`, `lapsed`, `optOut`,
    /// `verified` or `ignored`.
    pub outcome: String,
    /// The message's row; none for a reply (the notifier's) and an email
    /// not to the deployment.
    pub message: Option<String>,
    pub conversation: Option<String>,
    /// The party it came from, when one was settled.
    pub party: Option<PartyRef>,
    /// The grant its run request names.
    pub grant: Option<String>,
    /// A user's reply, as the notifier answered it.
    pub reply: Option<ReplyOutcome>,
    pub detail: Option<String>,
}

impl InboundOutcome {
    fn of(outcome: &str) -> Self {
        Self {
            outcome: outcome.to_owned(),
            ..Self::default()
        }
    }
}

/// What [`Inbound::send`] takes.
#[derive(Clone, Debug)]
pub struct SendInput {
    /// Who sends: a user, an agent run; it holds the send permission, and
    /// reads the party's row.
    pub as_: Principal,
    pub party: PartyRef,
    /// The channel kind it goes on: `sms`, `email`.
    pub channel: String,
    pub body: String,
    pub subject: Option<String>,
    /// The rows it is about, which list it in their backlinks.
    pub about: Vec<RowRef>,
}

impl SendInput {
    /// A message of `body` to `party` on `channel`, sent as `as_`.
    pub fn new(
        as_: Principal,
        party: PartyRef,
        channel: impl Into<String>,
        body: impl Into<String>,
    ) -> Self {
        Self {
            as_,
            party,
            channel: channel.into(),
            body: body.into(),
            subject: None,
            about: Vec::new(),
        }
    }
}

/// What [`Inbound::send`] and [`Inbound::release`] came to.
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize)]
pub struct SendOutcome {
    /// `sent`, `failed`, `refused` or `awaitingApproval`.
    pub outcome: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub message: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub detail: Option<String>,
}

/// What an approval policy decides of a send (D158 sets ask policies).
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum SendDecision {
    /// Send it.
    Allow,
    /// Refuse it.
    Deny,
    /// Hold it awaiting approval.
    Ask,
}

/// The approval policy of a send (D157): the app's, or the agent
/// dispatcher's, which holds an agent run's send to its autonomy policy
/// (D158). Every send is allowed without one.
#[async_trait]
pub trait SendApproval: Send + Sync {
    async fn approve(&self, input: &SendInput) -> Result<SendDecision, BoxError>;
}

/// The clock.
pub type Clock = Arc<dyn Fn() -> SystemTime + Send + Sync>;

/// The emails' authentication results that fail a check.
const FAILING: [&str; 3] = ["fail", "softfail", "permerror"];

/// Whether an email's authentication, as its adapter reported it, lets its
/// sender be trusted (D157): DMARC passed, and neither SPF nor DKIM failed.
/// An email that reports none is from an unknown sender.
pub fn email_authenticated(authentication: Option<&InboundAuthentication>) -> bool {
    let Some(authentication) = authentication else {
        return false;
    };
    let result = |value: &Option<String>| value.as_deref().unwrap_or("none").trim().to_lowercase();
    if result(&authentication.dmarc) != "pass" {
        return false;
    }
    !FAILING.contains(&result(&authentication.spf).as_str())
        && !FAILING.contains(&result(&authentication.dkim).as_str())
}

/// Why an email's authentication does not pass, in a line: `dmarc=fail
/// spf=pass dkim=none`.
fn authentication_text(authentication: Option<&InboundAuthentication>) -> String {
    let Some(a) = authentication else {
        return "the adapter reported no authentication".to_owned();
    };
    let of = |value: &Option<String>| value.clone().unwrap_or_else(|| "none".to_owned());
    format!(
        "dmarc={} spf={} dkim={}",
        of(&a.dmarc),
        of(&a.spf),
        of(&a.dkim)
    )
}

/// Whether a message's body is shaped as a code an identity is verified by:
/// four to twelve letters and digits.
fn is_code(text: &str) -> bool {
    let text = text.trim();
    (4..=12).contains(&text.len()) && text.chars().all(|c| c.is_ascii_alphanumeric())
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

fn string(row: &Values, member: &str) -> String {
    match row.get(member) {
        Some(Value::String(text)) => text.clone(),
        Some(Value::Null) | None => String::new(),
        Some(other) => other.to_string(),
    }
}

fn opt(row: &Values, member: &str) -> Option<String> {
    row.get(member)
        .filter(|value| !value.is_null())
        .map(|_| string(row, member))
        .filter(|text| !text.is_empty())
}

fn at_text(time: SystemTime) -> Value {
    Value::String(rfc3339_millis(time))
}

fn many(limit: u32) -> FindOptions {
    FindOptions {
        limit: Some(limit),
        skip_count: true,
        ..FindOptions::default()
    }
}

fn newest(limit: u32, field: &str) -> FindOptions {
    FindOptions {
        order_by: vec![OrderBy::desc(field)],
        ..many(limit)
    }
}

fn none() -> WriteOptions {
    WriteOptions::default()
}

fn key(id: &str) -> Value {
    Value::String(id.to_owned())
}

fn hex(bytes: &[u8]) -> String {
    bytes.iter().map(|b| format!("{b:02x}")).collect()
}

struct Inner {
    tables: Store,
    queue: PartQueue<RunRequest>,
    definition: &'static Definition,
    notifier: Notifier,
    runs: QueueDefinition,
    providers: Arc<Providers>,
    bucket: Option<Arc<dyn Bucket>>,
    inbound_address: Option<String>,
    send_permission: String,
    triage_permission: String,
    approve: OnceLock<Arc<dyn SendApproval>>,
    rate_messages: u32,
    rate_seconds: u64,
    address_kinds: BTreeMap<String, String>,
    now: Option<Clock>,
    conversations: String,
    messages: String,
    identities: String,
    parties: Vec<String>,
}

/// The inbound path, conversations and sends (D157). Build one with
/// [`Inbound::new`] and its `with_` methods, and share it: it is cheap to
/// clone.
#[derive(Clone)]
pub struct Inbound {
    inner: Arc<Inner>,
}

impl fmt::Debug for Inbound {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("Inbound")
            .field("messages", &self.inner.messages)
            .field("runs", &self.inner.runs.name)
            .finish_non_exhaustive()
    }
}

impl Inbound {
    /// The inbound path over `tables`, the database's repositories, with
    /// `notifier`, whose channel identities senders resolve through and to
    /// which a user's reply goes, and `runs`, the `@runRequests` queue's
    /// descriptor. It refuses a database without the conversations, the
    /// messages and the channel identities.
    pub fn new(
        tables: impl Into<Store>,
        notifier: Notifier,
        runs: QueueDefinition,
    ) -> Result<Self> {
        let tables: Store = tables.into();
        let definition = tables.definition()?;
        let queue = PartQueue::of(&tables, runs.clone())?;
        let part = |of: &dyn Fn(&super::Table) -> bool| {
            definition
                .tables
                .iter()
                .find(|table| of(table))
                .map(|table| table.name.clone())
        };
        let conversations = part(&|t| t.conversations.as_deref() == Some("conversations"));
        let messages = part(&|t| t.conversations.as_deref() == Some("messages"));
        let (Some(conversations), Some(messages)) = (conversations, messages) else {
            return Err(refused(
                "the inbound path: the database has no @conversations(\"conversations\") and @conversations(\"messages\") tables",
            ));
        };
        let identities =
            part(&|t| t.notifier.as_deref() == Some("channelIdentities")).ok_or_else(|| {
                refused("the inbound path: the database has no @channelIdentities table")
            })?;
        let parties = party_tables_of(definition)
            .into_iter()
            .map(str::to_owned)
            .collect();
        Ok(Self {
            inner: Arc::new(Inner {
                tables,
                queue,
                definition,
                notifier,
                runs,
                providers: Arc::new(Providers::none()),
                bucket: None,
                inbound_address: None,
                send_permission: "parties.message".to_owned(),
                triage_permission: "parties.triage".to_owned(),
                approve: OnceLock::new(),
                rate_messages: 20,
                rate_seconds: 3600,
                address_kinds: BTreeMap::new(),
                now: None,
                conversations,
                messages,
                identities,
                parties,
            }),
        })
    }

    fn edit(mut self, change: impl FnOnce(&mut Inner)) -> Self {
        if let Some(inner) = Arc::get_mut(&mut self.inner) {
            change(inner);
        }
        self
    }

    /// The path sending and receiving on the channel slots `providers`
    /// configures (D159). Each `with_` method is for a path not yet cloned.
    #[must_use]
    pub fn with_providers(self, providers: Arc<Providers>) -> Self {
        self.edit(|inner| inner.providers = providers)
    }

    /// The bucket an inbound message's files go to, through the messages
    /// table's `@attachments`; files are dropped, and the message says so,
    /// without it.
    #[must_use]
    pub fn with_bucket(self, bucket: Arc<dyn Bucket>) -> Self {
        self.edit(|inner| inner.bucket = Some(bucket))
    }

    /// The deployment's inbound email address (D157): an email to another
    /// is not the deployment's, and an outbound email's replies go to it.
    #[must_use]
    pub fn with_inbound_address(self, address: &str) -> Self {
        let address = normalise_address("email", address);
        self.edit(|inner| inner.inbound_address = Some(address))
    }

    /// The permission sending to a party takes; `parties.message` unless
    /// set.
    #[must_use]
    pub fn with_send_permission(self, permission: impl Into<String>) -> Self {
        let permission = permission.into();
        self.edit(|inner| inner.send_permission = permission)
    }

    /// The permission reading and settling triage takes; `parties.triage`
    /// unless set.
    #[must_use]
    pub fn with_triage_permission(self, permission: impl Into<String>) -> Self {
        let permission = permission.into();
        self.edit(|inner| inner.triage_permission = permission)
    }

    /// How many messages a party may be sent within `seconds`; 20 an hour
    /// unless set.
    #[must_use]
    pub fn with_rate_limit(self, messages: u32, seconds: u64) -> Self {
        self.edit(|inner| {
            inner.rate_messages = messages;
            inner.rate_seconds = seconds;
        })
    }

    /// The kind of address a channel kind uses, beside email to email and
    /// sms to phone.
    #[must_use]
    pub fn with_address_kind(self, channel: impl Into<String>, kind: impl Into<String>) -> Self {
        let (channel, kind) = (channel.into(), kind.into());
        self.edit(|inner| {
            inner.address_kinds.insert(channel, kind);
        })
    }

    /// The clock; the system's unless set, for a test.
    #[must_use]
    pub fn with_clock(self, now: Clock) -> Self {
        self.edit(|inner| inner.now = Some(now))
    }

    /// The approval policy of a send; every send is allowed unless set.
    #[must_use]
    pub fn with_approval(self, approve: Arc<dyn SendApproval>) -> Self {
        let _ = self.inner.approve.set(approve);
        self
    }

    /// Sets the approval policy of a path already shared, once: the agent
    /// dispatcher's (D158), which is built with the path. A second call
    /// changes nothing, and answers false.
    pub fn set_approval(&self, approve: Arc<dyn SendApproval>) -> bool {
        self.inner.approve.set(approve).is_ok()
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

    /// The tables the path reads and writes.
    pub fn tables(&self) -> &Store {
        &self.inner.tables
    }

    /// The `@runRequests` queue's descriptor.
    pub fn runs(&self) -> &QueueDefinition {
        &self.inner.runs
    }

    /// The messages table's type name.
    pub fn messages_table(&self) -> &str {
        &self.inner.messages
    }

    fn address_kind_of(&self, channel: &str) -> String {
        self.inner
            .address_kinds
            .get(channel)
            .cloned()
            .unwrap_or_else(|| default_address_kind(channel).to_owned())
    }

    fn sys<'c, C: PgClient>(&'c self, client: Db<'c, C>) -> Result<StoreRepositories<'c, C>> {
        let mut repositories = client.repositories(self.inner.definition)?.as_system();
        if let Some(keys) = self.inner.tables.keys() {
            repositories = repositories.with_keys(keys.as_ref());
        }
        Ok(repositories)
    }

    fn as_principal<'c, C: PgClient>(
        &'c self,
        client: Db<'c, C>,
        principal: &Principal,
    ) -> Result<StoreRepositories<'c, C>> {
        let mut repositories = client
            .repositories(self.inner.definition)?
            .acting(Some(principal.subject.clone()))
            .with_access(AccessCaller::of(Some(principal)));
        if let Some(keys) = self.inner.tables.keys() {
            repositories = repositories.with_keys(keys.as_ref());
        }
        Ok(repositories)
    }

    fn require_permission(&self, as_: &Principal, permission: &str, what: &str) -> Result<()> {
        if as_.permissions.iter().any(|held| covers(held, permission)) {
            return Ok(());
        }
        Err(refused(format!(
            "{} may not {what}: it takes {permission}",
            as_.subject
        )))
    }

    fn party_table(&self, table: &str) -> Result<&str> {
        self.inner
            .parties
            .iter()
            .find(|t| *t == table)
            .map(String::as_str)
            .ok_or_else(|| refused(format!("{table} is no @party table")))
    }

    /// Whether `principal` reads the row `key` of `table`.
    async fn reads(&self, principal: &Principal, table: &str, key: &str) -> Result<bool> {
        let def = self.inner.definition.table(table)?;
        let client = self.store().client().await?;
        let mut filter = Values::new();
        filter.insert(def.key.clone(), eq(key));
        Ok(self
            .as_principal(client.db(), principal)?
            .table(table)?
            .find_one(Some(&filter), &FindOptions::default())
            .await?
            .is_some())
    }

    // ---- channels ----

    fn adapter_of(&self, slot: &str) -> Result<Arc<dyn ChannelAdapter>> {
        self.inner
            .providers
            .channel(slot)
            .map_err(|err| refused(err.to_string()))
    }

    /// The first slot whose adapter sends on `kind` and answers its health.
    async fn slot_of_kind(&self, kind: &str) -> Option<(String, Arc<dyn ChannelAdapter>)> {
        for configured in self
            .inner
            .providers
            .configured(Some(RuntimeInterface::Channel))
        {
            let Ok(adapter) = self.inner.providers.channel(&configured.slot) else {
                continue;
            };
            if adapter.kind() != kind {
                continue;
            }
            match adapter.health().await {
                Ok(health) if health.ok => return Some((configured.slot, adapter)),
                Ok(_) => {}
                Err(err) => {
                    tracing::warn!(slot = %configured.slot, detail = %err, "channel health failed");
                }
            }
        }
        None
    }

    // ---- conversations and messages ----

    /// The conversation of `party` (or of `address`, for no party) on
    /// `channel`, made when there is none.
    async fn conversation_of<C: PgClient>(
        &self,
        client: Db<'_, C>,
        channel: &str,
        address: &str,
        party: Option<&PartyRef>,
        subject: Option<&str>,
    ) -> Result<Values> {
        let conversations = self.sys(client)?;
        let conversations = conversations.table(&self.inner.conversations)?;
        let filter = match party {
            Some(party) => obj(json!({
                "partyTable": { "eq": party.table },
                "party": { "eq": party.key },
                "channelKind": { "eq": channel },
            })),
            None => obj(json!({
                "party": { "isNull": true },
                "channelKind": { "eq": channel },
                "address": { "eq": address },
            })),
        };
        if let Some(found) = conversations
            .find_one(Some(&filter), &FindOptions::default())
            .await?
        {
            return Ok(found);
        }
        Ok(conversations
            .create_one(&obj(json!({
                "channelKind": channel,
                "address": address,
                "partyTable": party.map(|p| p.table.clone()),
                "party": party.map(|p| p.key.clone()),
                "subject": subject,
            })))
            .await?)
    }

    async fn message_where(&self, member: &str, value: &str) -> Result<Option<Values>> {
        let client = self.store().client().await?;
        let mut filter = Values::new();
        filter.insert(member.to_owned(), eq(value));
        Ok(self
            .sys(client.db())?
            .table(&self.inner.messages)?
            .find_one(Some(&filter), &FindOptions::default())
            .await?)
    }

    /// The message an inbound message's thread names: its In-Reply-To, its
    /// References, newest first, or the service's id of the text it
    /// answers.
    async fn thread_of(&self, input: &ReceivedMessage) -> Result<Option<Values>> {
        let mut ids: Vec<&str> = Vec::new();
        if let Some(id) = input.in_reply_to.as_deref() {
            ids.push(id);
        }
        ids.extend(input.references.iter().rev().map(String::as_str));
        for id in ids.into_iter().filter(|id| !id.is_empty()) {
            if let Some(found) = self.message_where("messageId", id).await? {
                return Ok(Some(found));
            }
            if let Some(found) = self.message_where("providerMessageId", id).await? {
                return Ok(Some(found));
            }
        }
        Ok(None)
    }

    /// Stores an inbound message's files on its row through the messages
    /// table's `@attachments`, and answers what could not be.
    async fn store_files(&self, message: &str, files: &[InboundAttachment]) -> Option<String> {
        if files.is_empty() {
            return None;
        }
        let (Some(bucket), true) = (
            self.inner.bucket.as_ref(),
            self.inner
                .definition
                .attachments
                .as_ref()
                .is_some_and(|attachments| attachments.holders.contains_key(&self.inner.messages)),
        ) else {
            return Some(format!(
                "{} file(s) dropped: the inbound path has no bucket or the messages table no @attachments",
                files.len()
            ));
        };
        let mut dropped = Vec::new();
        for file in files {
            if let Err(err) = self.store_one(message, file, bucket.as_ref()).await {
                tracing::warn!(message = %message, name = %file.name, detail = %err, "inbound file dropped");
                dropped.push(format!("{}: {err}", file.name));
            }
        }
        (!dropped.is_empty()).then(|| format!("files dropped: {}", dropped.join("; ")))
    }

    async fn store_one(
        &self,
        message: &str,
        file: &InboundAttachment,
        bucket: &dyn Bucket,
    ) -> Result<(), BoxError> {
        let digest = ring::digest::digest(&ring::digest::SHA256, &file.bytes);
        let begun = {
            let mut client = self.store().client().await?;
            let tx = client.transaction().await?;
            let (attachment, _) = self
                .sys(tx.db())?
                .attachments()?
                .begin(
                    &BeginFile {
                        to: RowRef::new(self.inner.messages.clone(), message),
                        name: file.name.clone(),
                        media_type: file.media_type.clone(),
                        size: file.bytes.len() as u64,
                        sha256: hex(digest.as_ref()),
                        caption: None,
                    },
                    bucket,
                    None,
                )
                .await?;
            tx.commit().await?;
            attachment
        };
        bucket
            .put(
                &begun.object_key,
                Body::full(file.bytes.clone()),
                PutOptions::content_type(file.media_type.clone()),
            )
            .await?;
        let mut client = self.store().client().await?;
        let tx = client.transaction().await?;
        self.sys(tx.db())?
            .attachments()?
            .complete(
                &FileRef::new(self.inner.messages.clone(), begun.id.clone()),
                bucket,
            )
            .await?;
        tx.commit().await?;
        Ok(())
    }

    /// Points `message` at `row`, when the messages table keeps references
    /// that may point at it.
    async fn refer(&self, message: &str, row: &RowRef, role: &str) {
        if self.inner.definition.references.is_none() {
            return;
        }
        let added: Result<(), BoxError> = async {
            let mut client = self.store().client().await?;
            let tx = client.transaction().await?;
            self.sys(tx.db())?
                .references()?
                .add(
                    &RowRef::new(self.inner.messages.clone(), message),
                    row,
                    role,
                )
                .await?;
            tx.commit().await?;
            Ok(())
        }
        .await;
        if let Err(err) = added {
            tracing::warn!(message = %message, table = %row.table, detail = %err, "reference not added");
        }
    }

    /// Records an inbound message in its conversation, its files filed to
    /// it; answers the row and its conversation.
    async fn record_inbound(
        &self,
        input: &ReceivedMessage,
        address: &str,
        values: Values,
        party: Option<&PartyRef>,
        conversation: Option<Values>,
    ) -> Result<(Values, Values)> {
        let at = input.received_at.unwrap_or_else(|| self.now());
        let (mut row, held) = {
            let mut client = self.store().client().await?;
            let tx = client.transaction().await?;
            let held = match conversation {
                Some(conversation) => conversation,
                None => {
                    self.conversation_of(
                        tx.db(),
                        &input.channel,
                        address,
                        party,
                        input.subject.as_deref(),
                    )
                    .await?
                }
            };
            let mut message = obj(json!({
                "conversation": held.get("id").cloned().unwrap_or(Value::Null),
                "direction": "inbound",
                "via": input.channel,
                "slot": input.slot,
                "fromAddress": address,
                "toAddress": input.to.first(),
                "subject": input.subject,
                "body": input.body,
                "messageId": input.message_id,
                "inReplyTo": input.in_reply_to,
                "threadRefs": if input.references.is_empty() { Value::Null } else { json!(input.references) },
                "providerMessageId": input.provider_message_id,
                "authentication": input.authentication.as_ref().map(|a| json!(a)),
                "partyTable": party.map(|p| p.table.clone()),
                "party": party.map(|p| p.key.clone()),
                "at": at_text(at),
            }));
            for (member, value) in values {
                message.insert(member, value);
            }
            let sys = self.sys(tx.db())?;
            let row = sys
                .table(&self.inner.messages)?
                .create_one(&message)
                .await?;
            sys.table(&self.inner.conversations)?
                .update_one(
                    held.get("id").unwrap_or(&Value::Null),
                    &obj(json!({ "lastMessageAt": at_text(at) })),
                    &none(),
                )
                .await?;
            drop(sys);
            tx.commit().await?;
            (row, held)
        };
        if let Some(dropped) = self
            .store_files(&string(&row, "id"), &input.attachments)
            .await
        {
            let detail = match opt(&row, "detail") {
                Some(detail) => format!("{detail}; {dropped}"),
                None => dropped,
            };
            let client = self.store().client().await?;
            row = self
                .sys(client.db())?
                .table(&self.inner.messages)?
                .update_one(
                    &key(&string(&row, "id")),
                    &obj(json!({ "detail": detail })),
                    &none(),
                )
                .await?;
        }
        Ok((row, held))
    }

    async fn held(
        &self,
        input: &ReceivedMessage,
        address: &str,
        disposition: &str,
        detail: &str,
        identity: Option<&Values>,
    ) -> Result<InboundOutcome> {
        let values = obj(json!({
            "disposition": disposition,
            "triage": "open",
            "detail": detail,
            "identity": identity.and_then(|i| i.get("id").cloned()),
            "trust": identity.and_then(|i| i.get("trust").cloned()),
        }));
        let (message, conversation) = self
            .record_inbound(input, address, values, None, None)
            .await?;
        Ok(InboundOutcome {
            message: Some(string(&message, "id")),
            conversation: Some(string(&conversation, "id")),
            detail: Some(detail.to_owned()),
            ..InboundOutcome::of(disposition)
        })
    }

    /// A party's message: a run request when its grants are active, its
    /// timeline and triage when not.
    async fn party_message(
        &self,
        input: &ReceivedMessage,
        address: &str,
        identity: &Values,
        party: PartyRef,
        thread: Option<Values>,
    ) -> Result<InboundOutcome> {
        let mut grants = {
            let client = self.store().client().await?;
            client
                .db()
                .active_grants(
                    self.inner.definition,
                    &ActiveGrantsFilter {
                        holder: Some(party.key.clone()),
                        now: Some(self.now()),
                        ..ActiveGrantsFilter::default()
                    },
                )
                .await?
        };
        let started = |g: &Values| {
            g.get("startsAt")
                .and_then(Value::as_str)
                .and_then(parse_rfc3339)
                .unwrap_or(SystemTime::UNIX_EPOCH)
        };
        grants.sort_by_key(|g| std::cmp::Reverse(started(g)));
        let thread_grant = thread
            .as_ref()
            .and_then(|t| opt(t, "grant"))
            .and_then(|grant| {
                grants
                    .iter()
                    .find(|g| same_uuid(&string(g, "id"), &grant))
                    .cloned()
            });
        let grant = thread_grant.or_else(|| grants.first().cloned());
        // The thread's conversation, when it is this party's on this
        // channel.
        let mut conversation = None;
        if let Some(thread) = &thread {
            let client = self.store().client().await?;
            let mut by_id = Values::new();
            by_id.insert(
                "id".to_owned(),
                eq(thread.get("conversation").cloned().unwrap_or(Value::Null)),
            );
            let named = self
                .sys(client.db())?
                .table(&self.inner.conversations)?
                .find_one(Some(&by_id), &FindOptions::default())
                .await?;
            if let Some(named) = named {
                if opt(&named, "party").is_some_and(|p| same_uuid(&p, &party.key))
                    && string(&named, "channelKind") == input.channel
                {
                    conversation = Some(named);
                }
            }
        }
        let mut base = obj(json!({
            "identity": identity.get("id").cloned().unwrap_or(Value::Null),
            "trust": identity.get("trust").cloned().unwrap_or(Value::Null),
        }));
        let party_row = RowRef::new(party.table.clone(), party.key.clone());
        let Some(grant) = grant else {
            let detail = "the party holds no active grant";
            base.insert("disposition".to_owned(), json!("timeline"));
            base.insert("triage".to_owned(), json!("open"));
            base.insert("detail".to_owned(), json!(detail));
            let (message, held) = self
                .record_inbound(input, address, base, Some(&party), conversation)
                .await?;
            let id = string(&message, "id");
            self.refer(&id, &party_row, "from").await;
            return Ok(InboundOutcome {
                message: Some(id),
                conversation: Some(string(&held, "id")),
                party: Some(party),
                detail: Some(detail.to_owned()),
                ..InboundOutcome::of("timeline")
            });
        };
        let grant_id = same_id(&string(&grant, "id"));
        base.insert("disposition".to_owned(), json!("run"));
        base.insert("grant".to_owned(), json!(grant_id));
        let (message, held) = self
            .record_inbound(input, address, base, Some(&party), conversation)
            .await?;
        let id = string(&message, "id");
        self.refer(&id, &party_row, "from").await;
        let request = RunRequest {
            trigger: "inbound".to_owned(),
            party: party.clone(),
            message: same_id(&id),
            grant: grant_id.clone(),
        };
        let mut client = self.store().client().await?;
        let tx = client.transaction().await?;
        tx.db()
            .enqueue(&self.inner.runs, &request, Duration::ZERO)
            .await?;
        tx.commit().await?;
        Ok(InboundOutcome {
            message: Some(id),
            conversation: Some(string(&held, "id")),
            party: Some(party),
            grant: Some(grant_id),
            ..InboundOutcome::of("run")
        })
    }

    async fn opt_out(
        &self,
        input: &ReceivedMessage,
        address: &str,
        kind: &str,
        out: bool,
    ) -> Result<InboundOutcome> {
        {
            let client = self.store().client().await?;
            let filter = obj(json!({
                "kind": { "eq": kind },
                "address": { "eq": address },
                "revokedAt": { "isNull": true },
            }));
            let stamp = if out {
                at_text(self.now())
            } else {
                Value::Null
            };
            self.sys(client.db())?
                .table(&self.inner.identities)?
                .update_many(Some(&filter), &obj(json!({ "optedOutAt": stamp })))
                .await?;
        }
        let detail = if out {
            "the address opted out"
        } else {
            "the address opted in"
        };
        let values = obj(json!({ "disposition": "optOut", "detail": detail }));
        let (message, conversation) = self
            .record_inbound(input, address, values, None, None)
            .await?;
        Ok(InboundOutcome {
            message: Some(string(&message, "id")),
            conversation: Some(string(&conversation, "id")),
            detail: Some(if out { "opted out" } else { "opted in" }.to_owned()),
            ..InboundOutcome::of("optOut")
        })
    }

    fn reply_input(input: &ReceivedMessage, address: &str, full: bool) -> ReplyInput {
        let mut reply = ReplyInput::of(input.channel.clone(), address, input.body.clone());
        reply.slot = input.slot.clone();
        if full {
            reply.received_at = input.received_at;
            reply.provider_message_id = input.provider_message_id.clone();
        }
        reply
    }

    /// The messages an adapter's `@webhook` request carries, each through
    /// [`Inbound::receive`]: none for an adapter that reads no inbound
    /// messages.
    pub async fn inbound(
        &self,
        slot: &str,
        request: &WebhookRequest,
    ) -> Result<Vec<InboundOutcome>> {
        let adapter = self.adapter_of(slot)?;
        let reads = self
            .inner
            .providers
            .operations(slot)
            .is_ok_and(|ops| ops.iter().any(|op| op == "inbound"));
        if !reads {
            return Ok(Vec::new());
        }
        let messages = adapter
            .inbound(request)
            .await
            .map_err(|err| InboundError::Failed(err.to_string()))?;
        let mut out = Vec::new();
        for message in messages {
            out.push(
                self.receive(ReceivedMessage::read(adapter.kind(), slot, message))
                    .await?,
            );
        }
        Ok(out)
    }

    /// Takes one inbound message down the path.
    pub async fn receive(&self, input: ReceivedMessage) -> Result<InboundOutcome> {
        let kind = self.address_kind_of(&input.channel);
        let address = normalise_address(&kind, &input.from);
        if kind == "email" {
            if let Some(inbound) = &self.inner.inbound_address {
                if !input.to.is_empty()
                    && !input
                        .to
                        .iter()
                        .any(|to| &normalise_address("email", to) == inbound)
                {
                    return Ok(InboundOutcome {
                        detail: Some("not sent to the deployment's inbound address".to_owned()),
                        ..InboundOutcome::of("ignored")
                    });
                }
            }
        }
        if let Some(opt_out) = input.opt_out {
            return self
                .opt_out(&input, &address, &kind, opt_out == OptOut::Out)
                .await;
        }
        if kind == "email" && !email_authenticated(input.authentication.as_ref()) {
            // The adapter reports, the core decides: a sender an email
            // cannot prove is no one's.
            let detail = format!(
                "the email's authentication did not pass: {}",
                authentication_text(input.authentication.as_ref())
            );
            return self.held(&input, &address, "unknown", &detail, None).await;
        }
        let resolved = self.inner.notifier.resolve_sender(&kind, &address).await?;
        let users = resolved
            .iter()
            .filter(|i| string(i, "principalKind") == "user" || opt(i, "principalKind").is_none())
            .count();
        let party_identities: Vec<&Values> = resolved
            .iter()
            .filter(|i| string(i, "principalKind") == "party")
            .collect();
        if users > 0 && party_identities.is_empty() {
            let reply = self
                .inner
                .notifier
                .reply(Self::reply_input(&input, &address, true))
                .await?;
            return Ok(InboundOutcome {
                reply: Some(reply),
                ..InboundOutcome::of("reply")
            });
        }
        if users > 0 {
            return self
                .held(
                    &input,
                    &address,
                    "unknown",
                    "the address is a user's and a party's: it resolves to no one",
                    None,
                )
                .await;
        }
        self.of_parties(&input, &address, &party_identities).await
    }

    /// A message from an address only parties hold: the one party whose
    /// identity stands, or the one its thread names of the parties that
    /// share it; a lapsed identity's, verified again by a code; or held in
    /// triage.
    async fn of_parties(
        &self,
        input: &ReceivedMessage,
        address: &str,
        party_identities: &[&Values],
    ) -> Result<InboundOutcome> {
        let mut current: Vec<&Values> = Vec::new();
        let mut lapsed: Vec<&Values> = Vec::new();
        {
            let client = self.store().client().await?;
            for identity in party_identities {
                if identity_lapsed(client.db(), self.inner.definition, identity, self.now()).await?
                {
                    lapsed.push(identity);
                } else {
                    current.push(identity);
                }
            }
        }
        let mut by_party: Vec<(String, &Values)> = Vec::new();
        for identity in current {
            let id = format!(
                "{}:{}",
                string(identity, "principalTable"),
                same_id(&string(identity, "principal"))
            );
            match by_party.iter_mut().find(|(k, _)| *k == id) {
                Some(entry) => entry.1 = identity,
                None => by_party.push((id, identity)),
            }
        }
        let ref_of = |identity: &Values| {
            PartyRef::new(
                string(identity, "principalTable"),
                same_id(&string(identity, "principal")),
            )
        };
        if by_party.len() == 1 {
            let identity = by_party[0].1;
            let thread = self.thread_of(input).await?;
            return self
                .party_message(input, address, identity, ref_of(identity), thread)
                .await;
        }
        if by_party.len() > 1 {
            let thread = self.thread_of(input).await?;
            let named = thread.as_ref().and_then(|thread| {
                let party = opt(thread, "party")?;
                let table = string(thread, "partyTable");
                by_party
                    .iter()
                    .find(|(_, c)| {
                        same_uuid(&string(c, "principal"), &party)
                            && string(c, "principalTable") == table
                    })
                    .map(|(_, c)| *c)
            });
            if let Some(identity) = named {
                return self
                    .party_message(input, address, identity, ref_of(identity), thread)
                    .await;
            }
            let detail = format!(
                "the address is shared by {} parties, and no thread names one",
                by_party.len()
            );
            return self.held(input, address, "unknown", &detail, None).await;
        }
        if let Some(first) = lapsed.first() {
            // A code sent to verify the lapsed identity again, replied with.
            let body = input.body.trim();
            if is_code(body) {
                for identity in &lapsed {
                    if self
                        .inner
                        .notifier
                        .confirm_identity(&string(identity, "id"), body)
                        .await?
                    {
                        let party = ref_of(identity);
                        let values = obj(json!({
                            "disposition": "verified",
                            "identity": identity.get("id").cloned().unwrap_or(Value::Null),
                            "trust": "verified",
                            "detail": "the identity is verified again",
                        }));
                        let (message, conversation) = self
                            .record_inbound(input, address, values, Some(&party), None)
                            .await?;
                        return Ok(InboundOutcome {
                            message: Some(string(&message, "id")),
                            conversation: Some(string(&conversation, "id")),
                            party: Some(party),
                            ..InboundOutcome::of("verified")
                        });
                    }
                }
            }
            return self
                .held(
                    input,
                    address,
                    "lapsed",
                    "the identity's grants have all ended since it was verified: verify it again before it is trusted",
                    Some(first),
                )
                .await;
        }
        // No identity: a code that verifies a new one is the notifier's.
        if is_code(&input.body) {
            let reply = self
                .inner
                .notifier
                .reply(Self::reply_input(input, address, false))
                .await?;
            if reply.outcome == "verified" {
                let party = reply
                    .principal
                    .as_ref()
                    .filter(|p| p.kind == "party")
                    .and_then(|p| Some(PartyRef::new(p.table.clone()?, same_id(&p.id))));
                let values =
                    obj(json!({ "disposition": "verified", "detail": "the address is verified" }));
                let (message, conversation) = self
                    .record_inbound(input, address, values, party.as_ref(), None)
                    .await?;
                return Ok(InboundOutcome {
                    message: Some(string(&message, "id")),
                    conversation: Some(string(&conversation, "id")),
                    party,
                    ..InboundOutcome::of("verified")
                });
            }
        }
        self.held(
            input,
            address,
            "unknown",
            "no verified identity holds the address",
            None,
        )
        .await
    }

    // ---- sending ----

    /// The party's identity a send on `channel` goes to, or why there is
    /// none.
    async fn destination_of(
        &self,
        party: &PartyRef,
        channel: &str,
    ) -> Result<std::result::Result<Values, String>> {
        let kind = self.address_kind_of(channel);
        let client = self.store().client().await?;
        let filter = obj(json!({
            "principalKind": { "eq": "party" },
            "principal": { "eq": party.key },
            "kind": { "eq": kind },
            "trust": { "in": ["verified", "entered"] },
            "revokedAt": { "isNull": true },
        }));
        let rows = self
            .sys(client.db())?
            .table(&self.inner.identities)?
            .find_many(Some(&filter), &many(10))
            .await?
            .rows;
        if rows.is_empty() {
            return Ok(Err(format!("the party has no verified {kind} address")));
        }
        let open: Vec<&Values> = rows
            .iter()
            .filter(|r| r.get("optedOutAt").is_none_or(Value::is_null))
            .collect();
        if open.is_empty() {
            return Ok(Err("the address opted out".to_owned()));
        }
        for identity in open {
            if !identity_lapsed(client.db(), self.inner.definition, identity, self.now()).await? {
                return Ok(Ok(identity.clone()));
            }
        }
        Ok(Err(
            "the party's address lapsed when its grants ended: verify it again".to_owned(),
        ))
    }

    fn domain_of(&self) -> String {
        self.inner
            .inbound_address
            .as_deref()
            .and_then(|a| a.split_once('@'))
            .map_or_else(|| "inbound.invalid".to_owned(), |(_, d)| d.to_owned())
    }

    async fn update_message(&self, id: &str, update: Value) -> Result<Values> {
        let client = self.store().client().await?;
        Ok(self
            .sys(client.db())?
            .table(&self.inner.messages)?
            .update_one(&key(id), &obj(update), &none())
            .await?)
    }

    /// Sends an outbound message's row through a channel of its kind.
    async fn deliver(&self, row: &Values) -> Result<SendOutcome> {
        let id = string(row, "id");
        let channel = string(row, "via");
        let Some((slot, adapter)) = self.slot_of_kind(&channel).await else {
            let detail = format!("no {channel} channel is up");
            self.update_message(&id, json!({ "state": "failed", "detail": detail }))
                .await?;
            return Ok(SendOutcome {
                outcome: "failed".to_owned(),
                message: Some(id),
                detail: Some(detail),
            });
        };
        let mut message = OutboundMessage {
            to: string(row, "toAddress"),
            body: string(row, "body"),
            subject: opt(row, "subject"),
            idempotency_key: Some(id.clone()),
            ..OutboundMessage::default()
        };
        if self.address_kind_of(&channel) == "email" {
            let client = self.store().client().await?;
            let filter = obj(json!({
                "conversation": { "eq": row.get("conversation").cloned().unwrap_or(Value::Null) },
                "direction": { "eq": "inbound" },
                "messageId": { "isNull": false },
            }));
            let last = self
                .sys(client.db())?
                .table(&self.inner.messages)?
                .find_many(Some(&filter), &newest(1, "at"))
                .await?
                .rows
                .into_iter()
                .next();
            message.message_id = Some(format!(
                "<{}@{}>",
                uuid::Uuid::new_v4().hyphenated(),
                self.domain_of()
            ));
            if let Some(last) = last {
                let last_id = string(&last, "messageId");
                let mut references: Vec<String> = last
                    .get("threadRefs")
                    .and_then(Value::as_array)
                    .map(|refs| {
                        refs.iter()
                            .filter_map(Value::as_str)
                            .map(str::to_owned)
                            .collect()
                    })
                    .unwrap_or_default();
                references.push(last_id.clone());
                message.in_reply_to = Some(last_id);
                message.references = references;
            }
            message.reply_to = self.inner.inbound_address.clone();
        }
        match adapter.send(&message).await {
            Ok(sent) => {
                let now = at_text(self.now());
                let mut update = obj(json!({
                    "state": "sent",
                    "slot": slot,
                    "providerMessageId": sent.provider_message_id,
                    "at": now,
                }));
                if let Some(message_id) = &message.message_id {
                    update.insert("messageId".to_owned(), json!(message_id));
                    update.insert("inReplyTo".to_owned(), json!(message.in_reply_to));
                    update.insert(
                        "threadRefs".to_owned(),
                        if message.references.is_empty() {
                            Value::Null
                        } else {
                            json!(message.references)
                        },
                    );
                }
                let client = self.store().client().await?;
                let sys = self.sys(client.db())?;
                sys.table(&self.inner.messages)?
                    .update_one(&key(&id), &update, &none())
                    .await?;
                sys.table(&self.inner.conversations)?
                    .update_one(
                        row.get("conversation").unwrap_or(&Value::Null),
                        &obj(json!({ "lastMessageAt": now })),
                        &none(),
                    )
                    .await?;
                Ok(SendOutcome {
                    outcome: "sent".to_owned(),
                    message: Some(id),
                    detail: None,
                })
            }
            Err(err) => {
                let detail = err.to_string();
                self.update_message(
                    &id,
                    json!({ "state": "failed", "slot": slot, "detail": detail }),
                )
                .await?;
                Ok(SendOutcome {
                    outcome: "failed".to_owned(),
                    message: Some(id),
                    detail: Some(detail),
                })
            }
        }
    }

    /// Sends a message to a party, held to the permission, the sender's
    /// read of the party's row, its identity, the rate limit and the
    /// approval policy. A refusal but the permission's and the read's is a
    /// message row with its reason.
    pub async fn send(&self, input: SendInput) -> Result<SendOutcome> {
        self.require_permission(&input.as_, &self.inner.send_permission, "message a party")?;
        // The sender reads the party's row: a party it cannot see it cannot
        // message, and nothing says the party exists.
        self.party_table(&input.party.table)?;
        if !self
            .reads(&input.as_, &input.party.table, &input.party.key)
            .await?
        {
            return Err(refused(format!(
                "no party {} {}",
                input.party.table, input.party.key
            )));
        }
        let destination = self.destination_of(&input.party, &input.channel).await?;
        let address = destination
            .as_ref()
            .map(|identity| string(identity, "address"))
            .unwrap_or_default();
        let mut conversation = {
            let mut client = self.store().client().await?;
            let tx = client.transaction().await?;
            let conversation = self
                .conversation_of(
                    tx.db(),
                    &input.channel,
                    &address,
                    Some(&input.party),
                    input.subject.as_deref(),
                )
                .await?;
            tx.commit().await?;
            conversation
        };
        // The party's address on the channel is the one it is reached at
        // now.
        if destination.is_ok() && string(&conversation, "address") != address {
            let client = self.store().client().await?;
            conversation = self
                .sys(client.db())?
                .table(&self.inner.conversations)?
                .update_one(
                    conversation.get("id").unwrap_or(&Value::Null),
                    &obj(json!({ "address": address })),
                    &none(),
                )
                .await?;
        }
        let email = self.address_kind_of(&input.channel) == "email";
        let from = match (&self.inner.inbound_address, email) {
            (Some(inbound), true) => inbound.clone(),
            _ => input.channel.clone(),
        };
        let subject = input
            .subject
            .clone()
            .or_else(|| opt(&conversation, "subject").map(|s| format!("Re: {s}")));
        let for_subject = input
            .as_
            .acting_for
            .as_ref()
            .filter(|f| f.subject != input.as_.subject)
            .map(|f| f.subject.clone());
        let base = obj(json!({
            "conversation": conversation.get("id").cloned().unwrap_or(Value::Null),
            "direction": "outbound",
            "via": input.channel,
            "fromAddress": from,
            "toAddress": destination.as_ref().ok().map(|i| string(i, "address")),
            "subject": subject,
            "body": input.body,
            "identity": destination.as_ref().ok().and_then(|i| i.get("id").cloned()),
            "partyTable": input.party.table,
            "party": input.party.key,
            "author": input.as_.subject,
            "authorKind": input.as_.kind.as_str(),
            "forSubject": for_subject,
            "at": at_text(self.now()),
        }));
        let create = |state: &str, detail: Option<&str>| {
            let mut row = base.clone();
            row.insert("state".to_owned(), json!(state));
            if let Some(detail) = detail {
                row.insert("detail".to_owned(), json!(detail));
            }
            row
        };
        let refuse = async |detail: &str| -> Result<SendOutcome> {
            let client = self.store().client().await?;
            let row = self
                .sys(client.db())?
                .table(&self.inner.messages)?
                .create_one(&create("refused", Some(detail)))
                .await?;
            Ok(SendOutcome {
                outcome: "refused".to_owned(),
                message: Some(string(&row, "id")),
                detail: Some(detail.to_owned()),
            })
        };
        if let Err(detail) = &destination {
            return refuse(detail).await;
        }
        let since = self.now() - Duration::from_secs(self.inner.rate_seconds);
        let recent = {
            let client = self.store().client().await?;
            let filter = obj(json!({
                "partyTable": { "eq": input.party.table },
                "party": { "eq": input.party.key },
                "direction": { "eq": "outbound" },
                "state": { "in": ["sent", "queued", "awaitingApproval"] },
                "at": { "gt": at_text(since) },
            }));
            self.sys(client.db())?
                .table(&self.inner.messages)?
                .find_many(Some(&filter), &many(self.inner.rate_messages))
                .await?
                .rows
                .len()
        };
        if recent >= self.inner.rate_messages as usize {
            return refuse(&format!(
                "the party was sent {recent} messages within {} s, the most it may be",
                self.inner.rate_seconds
            ))
            .await;
        }
        let decision = match self.inner.approve.get() {
            Some(approve) => approve
                .approve(&input)
                .await
                .map_err(|err| InboundError::Failed(err.to_string()))?,
            None => SendDecision::Allow,
        };
        if decision == SendDecision::Deny {
            return refuse("the approval policy refuses it").await;
        }
        let state = if decision == SendDecision::Ask {
            "awaitingApproval"
        } else {
            "queued"
        };
        let row = {
            let client = self.store().client().await?;
            self.sys(client.db())?
                .table(&self.inner.messages)?
                .create_one(&create(state, None))
                .await?
        };
        let id = string(&row, "id");
        for about in &input.about {
            self.refer(&id, about, "about").await;
        }
        if decision == SendDecision::Ask {
            return Ok(SendOutcome {
                outcome: "awaitingApproval".to_owned(),
                message: Some(id),
                detail: None,
            });
        }
        self.deliver(&row).await
    }

    async fn awaiting(&self, id: &str) -> Result<Values> {
        self.message(id)
            .await?
            .filter(|row| string(row, "state") == "awaitingApproval")
            .ok_or_else(|| refused(format!("message {id} awaits no approval")))
    }

    /// Sends a message held awaiting approval, once it was approved; its
    /// party's identity is checked again.
    pub async fn release(&self, id: &str) -> Result<SendOutcome> {
        let row = self.awaiting(id).await?;
        let party = PartyRef::new(string(&row, "partyTable"), same_id(&string(&row, "party")));
        let identity = match self.destination_of(&party, &string(&row, "via")).await? {
            Ok(identity) => identity,
            Err(detail) => {
                self.update_message(id, json!({ "state": "refused", "detail": detail }))
                    .await?;
                return Ok(SendOutcome {
                    outcome: "refused".to_owned(),
                    message: Some(id.to_owned()),
                    detail: Some(detail),
                });
            }
        };
        let queued = self
            .update_message(
                id,
                json!({
                    "state": "queued",
                    "toAddress": string(&identity, "address"),
                    "identity": identity.get("id").cloned().unwrap_or(Value::Null),
                }),
            )
            .await?;
        self.deliver(&queued).await
    }

    /// Refuses a message held awaiting approval, with `detail` (`not
    /// approved` unless set).
    pub async fn reject(&self, id: &str, detail: Option<&str>) -> Result<()> {
        self.awaiting(id).await?;
        self.update_message(
            id,
            json!({ "state": "refused", "detail": detail.unwrap_or("not approved") }),
        )
        .await?;
        Ok(())
    }

    /// The principal a party is (D156, D157): its active grants' reach and
    /// their roles' permissions, or one grant's.
    pub async fn principal_of(&self, party: &PartyRef, grant: Option<&str>) -> Result<Principal> {
        let client = self.store().client().await?;
        Ok(party_principal(client.db(), self.inner.definition, party, grant, self.now()).await?)
    }

    /// Files a message onto a row as `as_`, which must read it: the row
    /// points back at the message, with `role` (`about` unless set), and
    /// holds the message's files with no second upload.
    pub async fn file(
        &self,
        message: &str,
        to: &RowRef,
        as_: &Principal,
        role: Option<&str>,
    ) -> Result<()> {
        if self.message(message).await?.is_none() {
            return Err(refused(format!("no message {message}")));
        }
        // Whoever files it must read the row it goes on: a party's run,
        // held to its grants, reaches the rows its role's paths reach.
        if self.inner.definition.table(&to.table).is_err() {
            return Err(refused(format!("{} is no table of the database", to.table)));
        }
        if !self.reads(as_, &to.table, &to.key).await? {
            return Err(refused(format!("{} {} is not there", to.table, to.key)));
        }
        let role = role.unwrap_or("about");
        self.refer(message, to, role).await;
        // Only a messages table with @attachments has files to file, and
        // only a row of a table with them takes one: any other row gets the
        // reference alone.
        let holds = |table: &str| {
            self.inner
                .definition
                .attachments
                .as_ref()
                .is_some_and(|attachments| attachments.holders.contains_key(table))
        };
        if !holds(&self.inner.messages) || !holds(&to.table) {
            return Ok(());
        }
        // Filing is idempotent, as the reference is: a run request whose
        // handler failed after filing is run again, and the row takes no
        // file twice.
        let (filed, files) = {
            let client = self.store().client().await?;
            let sys = self.sys(client.db())?;
            let attachments = sys.attachments()?;
            let filed: Vec<String> = attachments
                .list(to)
                .await?
                .into_iter()
                .filter_map(|file| file.filed_from)
                .collect();
            let files = attachments
                .list(&RowRef::new(self.inner.messages.clone(), message))
                .await?;
            (filed, files)
        };
        for attachment in files {
            let from = format!(
                "{}:{}:{}",
                self.inner.messages, attachment.key, attachment.id
            );
            if filed.contains(&from) {
                continue;
            }
            let mut client = self.store().client().await?;
            let tx = client.transaction().await?;
            self.sys(tx.db())?
                .attachments()?
                .file(&FileOnto {
                    file: FileRef::new(self.inner.messages.clone(), attachment.id.clone()),
                    to: to.clone(),
                    r#move: false,
                    role: Some(role.to_owned()),
                })
                .await?;
            tx.commit().await?;
        }
        Ok(())
    }

    /// The messages held in triage, newest first; `as_` holds the triage
    /// permission.
    pub async fn triage(&self, as_: &Principal, limit: Option<u32>) -> Result<Vec<Values>> {
        self.require_permission(as_, &self.inner.triage_permission, "read triage")?;
        let client = self.store().client().await?;
        Ok(self
            .sys(client.db())?
            .table(&self.inner.messages)?
            .find_many(
                Some(&obj(json!({ "triage": { "eq": "open" } }))),
                &newest(limit.unwrap_or(50), "at"),
            )
            .await?
            .rows)
    }

    /// Closes a message's triage; `as_` holds the triage permission.
    pub async fn settle(&self, as_: &Principal, id: &str) -> Result<()> {
        self.require_permission(as_, &self.inner.triage_permission, "settle triage")?;
        let held = self
            .message(id)
            .await?
            .filter(|row| string(row, "triage") == "open")
            .ok_or_else(|| refused(format!("message {id} is not held in triage")))?;
        self.update_message(
            &string(&held, "id"),
            json!({
                "triage": "closed",
                "triageClosedBy": as_.subject,
                "triageClosedAt": at_text(self.now()),
            }),
        )
        .await?;
        Ok(())
    }

    /// A party's messages on every channel, newest first; `as_` reads the
    /// party's row.
    pub async fn timeline(
        &self,
        as_: &Principal,
        party: &PartyRef,
        limit: Option<u32>,
    ) -> Result<Vec<Values>> {
        self.party_table(&party.table)?;
        if !self.reads(as_, &party.table, &party.key).await? {
            return Err(refused(format!("no party {} {}", party.table, party.key)));
        }
        let client = self.store().client().await?;
        Ok(self
            .sys(client.db())?
            .table(&self.inner.messages)?
            .find_many(
                Some(&obj(json!({
                    "partyTable": { "eq": party.table },
                    "party": { "eq": party.key },
                }))),
                &newest(limit.unwrap_or(50), "at"),
            )
            .await?
            .rows)
    }

    /// A message's row, or none.
    pub async fn message(&self, id: &str) -> Result<Option<Values>> {
        if id.is_empty() {
            return Ok(None);
        }
        self.message_where("id", id).await
    }

    /// The `@runRequests` queue over the tables' pool, as a worker claims
    /// it: each message's handler is the agent dispatcher's (D158,
    /// `Agents::handle` of `AgentRunRequest::from`), or an app's own.
    pub fn run_queue(&self) -> PartQueue<RunRequest> {
        self.inner.queue.clone()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn auth(spf: Option<&str>, dkim: Option<&str>, dmarc: Option<&str>) -> InboundAuthentication {
        InboundAuthentication {
            spf: spf.map(str::to_owned),
            dkim: dkim.map(str::to_owned),
            dmarc: dmarc.map(str::to_owned),
        }
    }

    /// The vectors the TypeScript runtime's parity test writes and checks
    /// (`AGENTS_PARITY_WRITE=1 bun test src/agents-parity.test.ts`).
    #[test]
    fn the_parity_vectors_hold() {
        let text = include_str!("../../../testdata/agents_parity.json");
        let vectors: Value = serde_json::from_str(text).unwrap();
        for case in vectors["emailAuthenticated"].as_array().unwrap() {
            let given = &case["authentication"];
            let authentication = (!given.is_null()).then(|| {
                auth(
                    given["spf"].as_str(),
                    given["dkim"].as_str(),
                    given["dmarc"].as_str(),
                )
            });
            assert_eq!(
                email_authenticated(authentication.as_ref()),
                case["want"].as_bool().unwrap(),
                "email {case}"
            );
        }
    }

    #[test]
    fn a_code_is_four_to_twelve_letters_and_digits() {
        assert!(is_code(" 123456 "));
        assert!(is_code("AB12"));
        assert!(!is_code("123"));
        assert!(!is_code("Gate fixed"));
    }
}
