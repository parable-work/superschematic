//! Per-record access (D156): the grants that let a caller who is no user
//! reach rows, and the access log, as the TypeScript runtime's `access.ts`
//! has them.
//!
//! A grant is a row of the database's `@accessGrants` table: its holder (a
//! party's or an agent run's id) and the holder's kind, the role it acts
//! with, its target row, when it starts and when it ends (a date, or the
//! target's field reaching a state), when it was revoked, and the SHA-256
//! of the token its holder presents. The role is a row of the UserRole
//! table: its permissions are what the holder may do, and its reaches,
//! `Type:path` entries, which rows beside the target the holder reaches:
//! a row of `Type` whose path leads to the target. [`GrantAuthenticator`]
//! establishes the holder of a grant's token as the principal of its
//! kind, with the role's permissions and the grant's reach, on every call,
//! so a grant that ended or was revoked is refused at the very next one.
//! The repositories keep every read of such a principal to the rows its
//! grants reach (`orm`, `AccessCaller`), through the same relation paths a
//! row rule follows.
//!
//! The access log is a row of the database's `@accessLog` table for each
//! sensitive access: a reveal of an `Encrypted<T>` field, which a table
//! operation records itself, a read an agent run made, and every call made
//! with an API key (D114) or a grant's token, which [`Logged`] records
//! through an [`AccessLog`] once the router has the call's caller.
//! [`TableAccessLog`] writes them as the system actor, so a caller's own
//! access never hides its log row.
//!
//! The grants and the table access log run on Postgres, with the
//! `postgres` feature; the log's entries and [`Logged`] need none.

use async_trait::async_trait;
use http::request::Parts;
use http::Method;

use crate::auth::{Authenticator, CredentialKind, Principal, PrincipalKind};
use crate::ApiError;

/// What an access-log entry records (D156).
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum AccessLogKind {
    /// A reveal of an `Encrypted<T>` field.
    Reveal,
    /// A read an agent run made.
    AgentRead,
    /// A call made with an API key (D114).
    KeyCall,
    /// A call made with a grant's token.
    GrantCall,
}

impl AccessLogKind {
    /// The kind's name as the log's `kind` column holds it.
    pub fn as_str(self) -> &'static str {
        match self {
            AccessLogKind::Reveal => "reveal",
            AccessLogKind::AgentRead => "agentRead",
            AccessLogKind::KeyCall => "keyCall",
            AccessLogKind::GrantCall => "grantCall",
        }
    }
}

/// One entry of the access log (D156), the TypeScript `AccessLogEntry`.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct AccessLogEntry {
    /// What happened.
    pub kind: AccessLogKind,
    /// The subject of the principal that acted.
    pub principal: Option<String>,
    /// Its kind.
    pub principal_kind: Option<PrincipalKind>,
    /// The subject the principal acted for.
    pub acting_for: Option<String>,
    /// The API key's or the grant's id the call came with.
    pub credential: Option<String>,
    /// The operation the call ran.
    pub operation: Option<String>,
    /// The table of the row read.
    pub target_type: Option<String>,
    /// The key of the row read.
    pub target_id: Option<String>,
    /// The field revealed.
    pub field: Option<String>,
}

/// Where the access log is written: [`TableAccessLog`], or the project's
/// own.
#[async_trait]
pub trait AccessLog: Send + Sync + 'static {
    /// Records `entry`. An `Err` answers the call it records with it.
    async fn record(&self, entry: AccessLogEntry) -> Result<(), ApiError>;
}

/// The access log entry of a call (D156): an agent run's `GET` or `HEAD`,
/// and a call made with an API key's or a grant's credential; none for any
/// other, nor for a call with no caller.
pub fn call_entry(
    principal: Option<&Principal>,
    operation: &str,
    method: &Method,
) -> Option<AccessLogEntry> {
    let principal = principal?;
    let credential = principal.credential.as_ref();
    let kind = if principal.kind == PrincipalKind::Agent
        && (method == Method::GET || method == Method::HEAD)
    {
        AccessLogKind::AgentRead
    } else {
        match credential?.kind {
            CredentialKind::ApiKey => AccessLogKind::KeyCall,
            CredentialKind::Grant => AccessLogKind::GrantCall,
        }
    };
    Some(AccessLogEntry {
        kind,
        principal: Some(principal.subject.clone()),
        principal_kind: Some(principal.kind),
        acting_for: principal.acting_for.as_ref().map(|a| a.subject.clone()),
        credential: credential.map(|c| c.id.clone()),
        operation: Some(operation.to_owned()),
        target_type: None,
        target_id: None,
        field: None,
    })
}

/// An [`Authenticator`] whose calls the access log records (D156): it
/// establishes the caller as `inner` does, and once the router has the
/// caller of a call, before its permissions are checked, records the
/// call's entry ([`call_entry`]) in `log`, so a refused call is recorded
/// too. The TypeScript router's `accessLog` option.
pub struct Logged<A> {
    inner: A,
    log: std::sync::Arc<dyn AccessLog>,
}

impl<A: Authenticator> Logged<A> {
    /// `inner`, whose calls `log` records.
    pub fn new(inner: A, log: std::sync::Arc<dyn AccessLog>) -> Self {
        Self { inner, log }
    }
}

#[async_trait]
impl<A: Authenticator> Authenticator for Logged<A> {
    async fn authenticate(&self, request: &Parts) -> Result<Option<Principal>, ApiError> {
        self.inner.authenticate(request).await
    }

    fn permits(&self, held: &[String], required: &[String]) -> bool {
        self.inner.permits(held, required)
    }

    async fn called(
        &self,
        principal: Option<&Principal>,
        operation: &str,
        method: &Method,
    ) -> Result<(), ApiError> {
        self.inner.called(principal, operation, method).await?;
        match call_entry(principal, operation, method) {
            Some(entry) => self.log.record(entry).await,
            None => Ok(()),
        }
    }
}

#[cfg(feature = "postgres")]
pub use self::grants::*;

#[cfg(feature = "postgres")]
mod grants {
    use std::collections::BTreeMap;
    use std::sync::Arc;
    use std::time::SystemTime;

    use async_trait::async_trait;
    use base64::engine::general_purpose::URL_SAFE_NO_PAD;
    use base64::Engine as _;
    use deadpool_postgres::GenericClient;
    use http::request::Parts;
    use http::Method;
    use ring::rand::SecureRandom as _;
    use serde_json::{Map, Value};

    use super::{AccessLog, AccessLogEntry};
    use crate::auth::{
        bearer_token, Authenticator, Credential, CredentialKind, GrantReach, Principal,
        PrincipalKind,
    };
    use crate::civil::parse_rfc3339;
    use crate::orm::postgres::{Repositories, Tables};
    use crate::orm::{
        rfc3339_millis, Definition, Error, FindOptions, Values, ACCESS_GRANTS, ACCESS_LOG,
        ACCESS_ROLES,
    };
    use crate::ApiError;

    /// The prefix of a grant's token, which tells a person and a secret
    /// scanner what it is.
    pub const GRANT_TOKEN_PREFIX: &str = "grant_";

    /// The SHA-256 of a token's text, as the grant's `tokenHash` holds it:
    /// lowercase hex.
    pub fn grant_token_hash(token: &str) -> String {
        let digest = ring::digest::digest(&ring::digest::SHA256, token.as_bytes());
        digest
            .as_ref()
            .iter()
            .map(|byte| format!("{byte:02x}"))
            .collect()
    }

    /// A new grant's token: `grant_` and 43 characters of base64url, 32
    /// random bytes.
    fn new_token() -> Result<String, Error> {
        let mut random = [0u8; 32];
        ring::rand::SystemRandom::new()
            .fill(&mut random)
            .map_err(|_| Error::Database("issue_grant: no random bytes".to_owned()))?;
        Ok(format!(
            "{GRANT_TOKEN_PREFIX}{}",
            URL_SAFE_NO_PAD.encode(random)
        ))
    }

    /// A role's reaches, `Type:path` each, by type: the paths a grant of
    /// the role reaches rows of the type by. An entry with no type or no
    /// path is none.
    pub fn reaches_of<'a>(
        entries: impl IntoIterator<Item = &'a str>,
    ) -> BTreeMap<String, Vec<String>> {
        let mut out: BTreeMap<String, Vec<String>> = BTreeMap::new();
        for entry in entries {
            let Some((ty, path)) = entry.split_once(':') else {
                continue;
            };
            if ty.is_empty() || path.is_empty() {
                continue;
            }
            out.entry(ty.to_owned()).or_default().push(path.to_owned());
        }
        out
    }

    /// When a grant ends by its target's state (D156): the target's field
    /// and the state that ends it.
    #[derive(Clone, Debug, PartialEq, Eq)]
    pub struct EndsWhen {
        /// The target's field.
        pub field: String,
        /// The state of the field that ends the grant.
        pub state: String,
    }

    /// What [`issue_grant`] takes: the grant's row, but its key, its
    /// token's hash and its stamps.
    #[derive(Clone, Debug, PartialEq, Eq)]
    pub struct IssueGrant {
        /// The principal the grant lets reach rows.
        pub holder: String,
        /// party or agent; user for a calendar feed (D152), whose token
        /// reads its feed alone.
        pub holder_kind: PrincipalKind,
        /// The role's name, a row of the UserRole table.
        pub role: String,
        /// The target's table type.
        pub target_type: String,
        /// The target's key.
        pub target_id: String,
        /// When the grant starts; now unless set.
        pub starts_at: Option<SystemTime>,
        /// When it ends; never unless set.
        pub ends_at: Option<SystemTime>,
        /// The target's state that ends it.
        pub ends_when: Option<EndsWhen>,
    }

    /// A grant [`issue_grant`] made: its row and the token only its holder
    /// is given.
    #[derive(Clone, Debug)]
    pub struct IssuedGrant {
        /// The grant's row.
        pub grant: Values,
        /// The token, `grant_...`, which nothing stores.
        pub token: String,
    }

    fn grants_of(definition: &Definition) -> Result<&str, Error> {
        definition
            .access_table(ACCESS_GRANTS)
            .map(|table| table.name.as_str())
            .ok_or_else(|| {
                Error::Definition("the ORM has no @accessGrants table (D156)".to_owned())
            })
    }

    fn eq(value: Value) -> Value {
        let mut held = Map::new();
        held.insert("eq".to_owned(), value);
        Value::Object(held)
    }

    fn op(op: &str, value: Value) -> Value {
        let mut held = Map::new();
        held.insert(op.to_owned(), value);
        Value::Object(held)
    }

    fn text(value: Option<&Value>) -> Option<&str> {
        value.and_then(Value::as_str)
    }

    fn time(value: Option<&Value>) -> Option<SystemTime> {
        text(value).and_then(parse_rfc3339)
    }

    /// Makes a grant (D156) and its token, which only the holder is given:
    /// `grant_` and 43 characters of base64url, 32 random bytes, stored as
    /// the SHA-256 of the whole text. It writes through `repositories` as
    /// they are, so the grant records who made it, and a caller the target
    /// is no row of reaches none.
    pub async fn issue_grant<C: GenericClient + Sync>(
        repositories: &Repositories<'_, C>,
        input: &IssueGrant,
    ) -> Result<IssuedGrant, Error> {
        let definition = repositories.definition();
        let grants = repositories.table(grants_of(definition)?)?;
        let token = new_token()?;
        let now = repositories.session().now.unwrap_or_else(SystemTime::now);
        let stamp =
            |at: Option<SystemTime>| at.map_or(Value::Null, |at| Value::String(rfc3339_millis(at)));
        let mut row = Values::new();
        row.insert("holder".to_owned(), Value::String(input.holder.clone()));
        row.insert(
            "holderKind".to_owned(),
            Value::from(input.holder_kind.as_str()),
        );
        row.insert("role".to_owned(), Value::String(input.role.clone()));
        row.insert(
            "targetType".to_owned(),
            Value::String(input.target_type.clone()),
        );
        row.insert(
            "targetId".to_owned(),
            Value::String(input.target_id.clone()),
        );
        row.insert(
            "startsAt".to_owned(),
            stamp(Some(input.starts_at.unwrap_or(now))),
        );
        row.insert("endsAt".to_owned(), stamp(input.ends_at));
        let (field, state) = match &input.ends_when {
            Some(ends) => (
                Value::String(ends.field.clone()),
                Value::String(ends.state.clone()),
            ),
            None => (Value::Null, Value::Null),
        };
        row.insert("endsWhenField".to_owned(), field);
        row.insert("endsWhenState".to_owned(), state);
        row.insert(
            "tokenHash".to_owned(),
            Value::String(grant_token_hash(&token)),
        );
        row.insert("createdAt".to_owned(), stamp(Some(now)));
        let grant = grants.create_one(&row).await?;
        Ok(IssuedGrant { grant, token })
    }

    /// Revokes the grant whose key is `id` at `at`, as the system actor:
    /// the next call its token makes is refused.
    pub async fn revoke_grant<C: GenericClient + Sync>(
        client: &C,
        definition: &Definition,
        id: &str,
        at: SystemTime,
    ) -> Result<(), Error> {
        let system = Repositories::new(client, definition)?.as_system();
        let grants = system.table(grants_of(definition)?)?;
        let mut update = Values::new();
        update.insert("revokedAt".to_owned(), Value::String(rfc3339_millis(at)));
        grants
            .update_one(&Value::String(id.to_owned()), &update, &Default::default())
            .await?;
        Ok(())
    }

    /// Why `grant` is not active at `now`, or none when it is (D156): it
    /// was revoked, it has not started, it has ended by its date, its
    /// target's field holds the state that ends it, or its target is gone.
    /// The target is counted as the system actor, with no value of its
    /// read, so an encrypted field of it needs no key.
    pub async fn grant_ended<C: GenericClient + Sync>(
        client: &C,
        definition: &Definition,
        grant: &Values,
        now: SystemTime,
    ) -> Result<Option<&'static str>, Error> {
        if grant.get("revokedAt").is_some_and(|at| !at.is_null()) {
            return Ok(Some("revoked"));
        }
        if time(grant.get("startsAt")).is_some_and(|starts| starts > now) {
            return Ok(Some("not started"));
        }
        if time(grant.get("endsAt")).is_some_and(|ends| ends <= now) {
            return Ok(Some("expired"));
        }
        let Some(field) = text(grant.get("endsWhenField")).filter(|field| !field.is_empty()) else {
            return Ok(None);
        };
        let target_type = text(grant.get("targetType")).unwrap_or_default();
        let Ok(target) = definition.table(target_type) else {
            return Ok(Some("target gone"));
        };
        let system = Repositories::new(client, definition)?.as_system();
        let rows = system.table(&target.name)?;
        let mut filter = Values::new();
        filter.insert(
            target.key.clone(),
            eq(grant.get("targetId").cloned().unwrap_or(Value::Null)),
        );
        if rows.count(Some(&filter)).await? == 0 {
            return Ok(Some("target gone"));
        }
        let state = grant.get("endsWhenState").cloned().unwrap_or(Value::Null);
        filter.insert(field.to_owned(), eq(state));
        Ok((rows.count(Some(&filter)).await? > 0).then_some("target ended"))
    }

    /// What [`active_grants`] keeps: a holder's grants, a target's, or
    /// every one.
    #[derive(Clone, Debug, Default, PartialEq, Eq)]
    pub struct ActiveGrantsFilter {
        /// The holder's id.
        pub holder: Option<String>,
        /// The target's table type.
        pub target_type: Option<String>,
        /// The target's key.
        pub target_id: Option<String>,
        /// The time the grants are held to; the clock's unless set.
        pub now: Option<SystemTime>,
    }

    /// The read of the active grants (D156): each grant that has started,
    /// has not ended by its date or its target's state, and was not
    /// revoked, of a holder, of a target, or every one, read as the system
    /// actor.
    pub async fn active_grants<C: GenericClient + Sync>(
        client: &C,
        definition: &Definition,
        filter: &ActiveGrantsFilter,
    ) -> Result<Vec<Values>, Error> {
        let system = Repositories::new(client, definition)?.as_system();
        let grants = system.table(grants_of(definition)?)?;
        let now = filter.now.unwrap_or_else(SystemTime::now);
        let at = Value::String(rfc3339_millis(now));
        let mut wanted = Values::new();
        wanted.insert("revokedAt".to_owned(), op("isNull", Value::Bool(true)));
        wanted.insert("startsAt".to_owned(), op("lte", at.clone()));
        for (member, value) in [
            ("holder", &filter.holder),
            ("targetType", &filter.target_type),
            ("targetId", &filter.target_id),
        ] {
            if let Some(value) = value {
                wanted.insert(member.to_owned(), eq(Value::String(value.clone())));
            }
        }
        let mut no_end = Values::new();
        no_end.insert("endsAt".to_owned(), op("isNull", Value::Bool(true)));
        let mut later = Values::new();
        later.insert("endsAt".to_owned(), op("gt", at));
        wanted.insert(
            "anyOf".to_owned(),
            Value::Array(vec![Value::Object(no_end), Value::Object(later)]),
        );
        let options = FindOptions {
            limit: Some(10_000),
            skip_count: true,
            ..FindOptions::default()
        };
        let found = grants.find_many(Some(&wanted), &options).await?;
        let mut active = Vec::new();
        for row in found.rows {
            if grant_ended(client, definition, &row, now).await?.is_none() {
                active.push(row);
            }
        }
        Ok(active)
    }

    /// The router's [`Authenticator`] for grants (D156): a request whose
    /// Authorization is `Bearer grant_...` runs as the grant's holder, a
    /// party or an agent run, with its role's permissions and the grant's
    /// reach; every other request is `inner`'s to establish. The grant is
    /// read on every call, so a grant that ended, by its date or its
    /// target's state, or was revoked is refused 401 at the very next call,
    /// as an unknown token is.
    pub struct GrantAuthenticator {
        tables: Tables,
        inner: Option<Arc<dyn Authenticator>>,
        now: Option<Arc<dyn Fn() -> SystemTime + Send + Sync>>,
        runs: bool,
    }

    impl GrantAuthenticator {
        /// The grants of the database `tables` reaches, which holds the
        /// `@accessGrants` table and the UserRole table its roles are rows
        /// of; with no inner authenticator, a request that presents no
        /// grant's token has no principal. Refuses a database with neither.
        pub fn new(tables: Tables) -> Result<Self, Error> {
            let definition = tables.definition()?;
            grants_of(definition)?;
            if definition.access_table(ACCESS_ROLES).is_none() {
                return Err(Error::Definition(
                    "the ORM has no UserRole table, whose rows a grant's role names (D156)"
                        .to_owned(),
                ));
            }
            Ok(Self {
                tables,
                inner: None,
                now: None,
                runs: false,
            })
        }

        /// The authenticator taking an agent run's token too, as its run:
        /// the agent dispatcher's alone (D158, D193), which narrows the
        /// run's permissions and paths and holds its calls to its autonomy.
        pub(crate) fn admitting_runs(mut self) -> Self {
            self.runs = true;
            self
        }

        /// The authenticator establishing every caller that presents no
        /// grant's token as `inner` does: the identity service's, say.
        #[must_use]
        pub fn with_inner(mut self, inner: Arc<dyn Authenticator>) -> Self {
            self.inner = Some(inner);
            self
        }

        /// The time a grant is held to, the clock's unless set.
        #[must_use]
        pub fn with_clock(mut self, now: Arc<dyn Fn() -> SystemTime + Send + Sync>) -> Self {
            self.now = Some(now);
            self
        }

        async fn grant(&self, token: &str) -> Result<Principal, ApiError> {
            let unreachable = |err: &dyn std::error::Error| {
                ApiError::service_unavailable(format!("the database is unreachable: {err}"))
            };
            let definition = self.tables.definition()?;
            let client = self
                .tables
                .pool()
                .get()
                .await
                .map_err(|err| unreachable(&err))?;
            let system = Repositories::new(&client, definition)?.as_system();
            let mut filter = Values::new();
            filter.insert(
                "tokenHash".to_owned(),
                eq(Value::String(grant_token_hash(token))),
            );
            let grant = system
                .table(grants_of(definition)?)?
                .find_one(Some(&filter), &FindOptions::default())
                .await?
                .ok_or_else(|| ApiError::unauthorized("Authentication required"))?;
            let target_type = text(grant.get("targetType")).unwrap_or_default().to_owned();
            // A calendar feed's token reads its feed alone (D152): a leaked
            // feed URL is no credential of any route.
            if target_type == "CalendarFeed" || target_type.starts_with("CalendarFeed;") {
                return Err(ApiError::unauthorized(
                    "A calendar feed's token reads its feed alone",
                ));
            }
            // An agent run's token is the dispatcher's to resolve
            // (`Agents::authenticator`), which narrows its permissions to
            // whom it acts for and its paths to its party's grant, and holds
            // its calls to its autonomy: as its grant's bare holder it would
            // run with its role's permissions, neither narrowed nor gated
            // (D193).
            if !self.runs && text(grant.get("holderKind")) == Some("agent") {
                return Err(ApiError::unauthorized(
                    "An agent run's token is resolved by the agent dispatcher's authenticator alone",
                ));
            }
            let now = self.now.as_ref().map_or_else(SystemTime::now, |now| now());
            if let Some(ended) = grant_ended(&client, definition, &grant, now).await? {
                return Err(ApiError::unauthorized(if ended == "not started" {
                    "The grant has not started"
                } else {
                    "The grant has ended"
                }));
            }
            let roles = definition
                .access_table(ACCESS_ROLES)
                .map(|table| table.name.clone())
                .unwrap_or_default();
            let mut by_name = Values::new();
            by_name.insert(
                "name".to_owned(),
                eq(grant.get("role").cloned().unwrap_or(Value::Null)),
            );
            let role = system
                .table(&roles)?
                .find_one(Some(&by_name), &FindOptions::default())
                .await?;
            let list = |member: &str| -> Vec<String> {
                role.as_ref()
                    .and_then(|role| role.get(member))
                    .and_then(Value::as_array)
                    .map(|items| {
                        items
                            .iter()
                            .filter_map(Value::as_str)
                            .map(str::to_owned)
                            .collect()
                    })
                    .unwrap_or_default()
            };
            let id = text(grant.get("id"))
                .map(str::to_owned)
                .or_else(|| grant.get("id").map(Value::to_string))
                .unwrap_or_default();
            let reaches = list("reaches");
            let reach = GrantReach {
                id: id.clone(),
                target_type,
                target_id: text(grant.get("targetId")).unwrap_or_default().to_owned(),
                reaches: reaches_of(reaches.iter().map(String::as_str)),
            };
            let kind = if text(grant.get("holderKind")) == Some("agent") {
                PrincipalKind::Agent
            } else {
                PrincipalKind::Party
            };
            Ok(Principal::new(
                text(grant.get("holder")).unwrap_or_default(),
                list("permissions"),
            )
            .of_kind(kind)
            .with_grants(vec![reach])
            .with_credential(Credential {
                kind: CredentialKind::Grant,
                id,
            }))
        }
    }

    #[async_trait]
    impl Authenticator for GrantAuthenticator {
        async fn authenticate(&self, request: &Parts) -> Result<Option<Principal>, ApiError> {
            match bearer_token(&request.headers) {
                Some(token) if token.starts_with(GRANT_TOKEN_PREFIX) => {
                    self.grant(token).await.map(Some)
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
            method: &Method,
        ) -> Result<(), ApiError> {
            match &self.inner {
                Some(inner) => inner.called(principal, operation, method).await,
                None => Ok(()),
            }
        }
    }

    /// The access log over a database's `@accessLog` table (D156), written
    /// as the system actor, each entry on a connection of its own.
    pub struct TableAccessLog {
        tables: Tables,
    }

    /// The access log of the database `tables` reaches, or none for one
    /// with no `@accessLog` table, so a router that takes it logs nothing.
    pub fn access_log_of(tables: &Tables) -> Result<Option<TableAccessLog>, Error> {
        Ok(tables
            .definition()?
            .access_table(ACCESS_LOG)
            .map(|_| TableAccessLog {
                tables: tables.clone(),
            }))
    }

    /// The row of the access log `entry` is, stamped `at`.
    pub fn access_log_row(entry: &AccessLogEntry, at: SystemTime) -> Values {
        let text = |value: &Option<String>| value.clone().map_or(Value::Null, Value::String);
        let mut row = Values::new();
        row.insert("at".to_owned(), Value::String(rfc3339_millis(at)));
        row.insert("kind".to_owned(), Value::from(entry.kind.as_str()));
        row.insert("principal".to_owned(), text(&entry.principal));
        row.insert(
            "principalKind".to_owned(),
            entry
                .principal_kind
                .map_or(Value::Null, |kind| Value::from(kind.as_str())),
        );
        row.insert("actingFor".to_owned(), text(&entry.acting_for));
        row.insert("credential".to_owned(), text(&entry.credential));
        row.insert("operation".to_owned(), text(&entry.operation));
        row.insert("targetType".to_owned(), text(&entry.target_type));
        row.insert("targetId".to_owned(), text(&entry.target_id));
        row.insert("field".to_owned(), text(&entry.field));
        row
    }

    #[async_trait]
    impl AccessLog for TableAccessLog {
        async fn record(&self, entry: AccessLogEntry) -> Result<(), ApiError> {
            let definition = self.tables.definition()?;
            let log = definition
                .access_table(ACCESS_LOG)
                .map(|table| table.name.clone())
                .ok_or_else(|| ApiError::internal("the ORM has no @accessLog table (D156)"))?;
            let client = self.tables.pool().get().await.map_err(|err| {
                ApiError::service_unavailable(format!("the database is unreachable: {err}"))
            })?;
            let system = Repositories::new(&client, definition)?.as_system();
            system
                .table(&log)?
                .create_one(&access_log_row(&entry, SystemTime::now()))
                .await?;
            Ok(())
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::auth::{ActingFor, Credential};

    #[test]
    fn a_call_is_logged_by_its_credential_or_an_agent_runs_read() {
        let user = Principal::new("u", Vec::<String>::new());
        assert_eq!(call_entry(Some(&user), "listThings", &Method::GET), None);
        assert_eq!(call_entry(None, "listThings", &Method::GET), None);
        let key = user.clone().with_credential(Credential {
            kind: CredentialKind::ApiKey,
            id: "k1".to_owned(),
        });
        let entry = call_entry(Some(&key), "listThings", &Method::POST).unwrap();
        assert_eq!(entry.kind, AccessLogKind::KeyCall);
        assert_eq!(entry.credential.as_deref(), Some("k1"));
        assert_eq!(entry.principal_kind, Some(PrincipalKind::User));
        let run = Principal::new("r", Vec::<String>::new())
            .of_kind(PrincipalKind::Agent)
            .acting_for(ActingFor::user("u"));
        let read = call_entry(Some(&run), "getThing", &Method::GET).unwrap();
        assert_eq!(read.kind, AccessLogKind::AgentRead);
        assert_eq!(read.acting_for.as_deref(), Some("u"));
        assert_eq!(read.operation.as_deref(), Some("getThing"));
        assert_eq!(call_entry(Some(&run), "editThing", &Method::PATCH), None);
    }

    #[cfg(feature = "postgres")]
    #[test]
    fn a_roles_reaches_are_read_by_type_and_a_token_is_hashed_in_hex() {
        let reaches = reaches_of([
            "Unit:contract",
            "Request:unit.contract",
            "bad",
            ":x",
            "Y:",
            "Unit:*",
        ]);
        assert_eq!(reaches.len(), 2);
        assert_eq!(reaches["Unit"], vec!["contract", "*"]);
        assert_eq!(reaches["Request"], vec!["unit.contract"]);
        // The SHA-256 of "abc" in lowercase hex, as the TypeScript
        // runtime's grantTokenHash writes a token's.
        assert_eq!(
            grant_token_hash("abc"),
            "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
        );
    }
}
