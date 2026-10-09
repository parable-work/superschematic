//! The user model's operations over a [`Store`], a [`Config`] and a clock:
//! signing users in and out, resolving a request's session to its
//! principal, and managing users, roles and grants. Each returns the
//! problem the Go runtime answers with, as an [`ApiError`].

use std::collections::{BTreeMap, HashSet};
use std::fmt;
use std::sync::Arc;
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use http::header::SET_COOKIE;
use http::request::Parts;
use http::{HeaderMap, HeaderValue, Method, StatusCode};
use serde::Serialize;
use serde_json::{json, Map, Value};

use crate::{has_any_permission, ApiError, Principal};

use super::config::{Config, ConfigError};
use super::cross_origin::{check_cross_origin, request_host};
use super::errors::{
    cross_origin, field_refusal, forbidden, internal, invalid_credentials, invalid_permissions,
    not_held, store_error, unauthenticated, FieldErrors, CODE_UNAUTHORIZED,
};
use super::json;
use super::password::{
    check_password, dummy_hash, hash_password, verify_password, Argon2Params, Verified,
};
use super::permissions::{
    capabilities_of, effective_permissions, uncovered, valid_permission, Route,
};
use super::store::{NewSession, NewUser, Role, Store, StoreError, User};
use super::time::format_rfc3339;
use super::token::{extract_credential, hash_token, new_token, CredentialOutcome, Transport};

/// The administration routes' permissions, under the naming key
/// `identity_permission_prefix` (`identity` by default): the prefix, a
/// dot, and one of these.
pub const PERMISSION_USERS_READ: &str = "users.read";
pub const PERMISSION_USERS_WRITE: &str = "users.write";
pub const PERMISSION_ROLES_READ: &str = "roles.read";
pub const PERMISSION_ROLES_WRITE: &str = "roles.write";

/// `identity_permission_prefix`'s default.
pub const DEFAULT_PERMISSION_PREFIX: &str = "identity";

/// The latest a session ends, in seconds after the Unix epoch:
/// 9999-12-31T23:59:59Z, the last time every store and the wire's RFC 3339
/// write, however long the config's `sessionTtlSeconds`.
const LATEST_EXPIRY_SECONDS: u64 = 253_402_300_799;

/// The clock sessions are created, expired and touched by.
pub type Clock = Arc<dyn Fn() -> SystemTime + Send + Sync>;

/// The permission rule: whether permissions `held` satisfy a route's
/// `required` list, the router's `Authenticator::permits`.
pub type Permits = Arc<dyn Fn(&[String], &[String]) -> bool + Send + Sync>;

/// The user model's logic. Build one with [`Service::new`], share it in an
/// `Arc`, and mount its handlers (`Service::handler`) and its
/// [`super::IdentityAuthenticator`].
pub struct Service {
    store: Arc<dyn Store>,
    config: Config,
    clock: Clock,
    permits: Permits,
    prefix: String,
    routes: Vec<Route>,
    params: Argon2Params,
    dummy: String,
}

impl fmt::Debug for Service {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("Service")
            .field("config", &self.config)
            .field("prefix", &self.prefix)
            .field("routes", &self.routes.len())
            .finish_non_exhaustive()
    }
}

/// An authenticated caller: the user, their roles and permissions, and the
/// session the request carried.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct IdentityPrincipal {
    pub id: String,
    pub login: String,
    pub name: String,
    /// The user's roles, by name.
    pub roles: Vec<Role>,
    /// The roles' permissions, without repeats ([`effective_permissions`]).
    pub permissions: Vec<String>,
    pub session_id: String,
    pub transport: Transport,
}

impl IdentityPrincipal {
    /// The router's [`Principal`]: the user's id as its subject, the roles'
    /// permissions, and claims carrying the login, the name, the session
    /// id, the transport and the roles.
    pub fn to_principal(&self) -> Principal {
        let mut claims = Map::new();
        claims.insert("login".to_owned(), Value::from(self.login.clone()));
        claims.insert("name".to_owned(), Value::from(self.name.clone()));
        claims.insert("sessionId".to_owned(), Value::from(self.session_id.clone()));
        claims.insert("transport".to_owned(), Value::from(self.transport.as_str()));
        let roles: Vec<Value> = self
            .roles
            .iter()
            .map(|role| json!({"id": role.id, "name": role.name, "permissions": role.permissions}))
            .collect();
        claims.insert("roles".to_owned(), Value::Array(roles));
        Principal {
            subject: self.id.clone(),
            permissions: self.permissions.clone(),
            claims,
        }
    }

    /// The caller a [`Principal`] the identity runtime made carries, or
    /// `None` for one another authenticator made.
    pub fn from_principal(principal: &Principal) -> Option<IdentityPrincipal> {
        let claims = &principal.claims;
        let text = |key: &str| claims.get(key)?.as_str().map(str::to_owned);
        let transport = match claims.get("transport")?.as_str()? {
            "bearer" => Transport::Bearer,
            "cookie" => Transport::Cookie,
            _ => return None,
        };
        let roles = claims
            .get("roles")?
            .as_array()?
            .iter()
            .map(|role| {
                Some(Role {
                    id: role.get("id")?.as_str()?.to_owned(),
                    name: role.get("name")?.as_str()?.to_owned(),
                    permissions: role
                        .get("permissions")?
                        .as_array()?
                        .iter()
                        .map(|p| p.as_str().map(str::to_owned))
                        .collect::<Option<Vec<_>>>()?,
                })
            })
            .collect::<Option<Vec<_>>>()?;
        Some(IdentityPrincipal {
            id: principal.subject.clone(),
            login: text("login")?,
            name: text("name")?,
            roles,
            permissions: principal.permissions.clone(),
            session_id: text("sessionId").filter(|id| !id.is_empty())?,
            transport,
        })
    }
}

/// `login`'s input. `session` is `bearer` (also when empty) or `cookie`.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct LoginInput {
    pub login: String,
    pub password: String,
    pub session: String,
}

/// `register`'s input: `login`'s, and the display name (the login when
/// empty).
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct RegisterInput {
    pub login: String,
    pub name: String,
    pub password: String,
    pub session: String,
}

/// `changePassword`'s input.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct ChangePasswordInput {
    pub current: String,
    pub password: String,
}

/// `createUser`'s input.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct CreateUserInput {
    pub login: String,
    pub name: String,
    pub password: String,
}

/// `setUserPassword`'s input.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct SetPasswordInput {
    pub password: String,
}

/// `createRole`'s and `updateRole`'s input.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct RoleInput {
    pub name: String,
    pub permissions: Vec<String>,
}

/// Reads an input from its JSON body as every identity runtime reads one:
/// an object, members matched by name exactly, any other member refused,
/// null as absent.
pub trait Input: Sized {
    fn from_json(value: &Value) -> Result<Self, String>;
}

fn input_members<'a, const N: usize>(
    value: &'a Value,
    names: [&str; N],
) -> Result<[Option<&'a Value>; N], String> {
    let object = json::object(value, "the body")?;
    let values = json::members(object, &names)?;
    let mut out = [None; N];
    out.copy_from_slice(&values);
    Ok(out)
}

fn string_member(value: Option<&Value>, member: &str) -> Result<String, String> {
    value.map_or(Ok(String::new()), |v| json::string(v, member))
}

impl Input for LoginInput {
    fn from_json(value: &Value) -> Result<Self, String> {
        let [login, password, session] = input_members(value, ["login", "password", "session"])?;
        Ok(LoginInput {
            login: string_member(login, "login")?,
            password: string_member(password, "password")?,
            session: string_member(session, "session")?,
        })
    }
}

impl Input for RegisterInput {
    fn from_json(value: &Value) -> Result<Self, String> {
        let [login, name, password, session] =
            input_members(value, ["login", "name", "password", "session"])?;
        Ok(RegisterInput {
            login: string_member(login, "login")?,
            name: string_member(name, "name")?,
            password: string_member(password, "password")?,
            session: string_member(session, "session")?,
        })
    }
}

impl Input for ChangePasswordInput {
    fn from_json(value: &Value) -> Result<Self, String> {
        let [current, password] = input_members(value, ["current", "password"])?;
        Ok(ChangePasswordInput {
            current: string_member(current, "current")?,
            password: string_member(password, "password")?,
        })
    }
}

impl Input for CreateUserInput {
    fn from_json(value: &Value) -> Result<Self, String> {
        let [login, name, password] = input_members(value, ["login", "name", "password"])?;
        Ok(CreateUserInput {
            login: string_member(login, "login")?,
            name: string_member(name, "name")?,
            password: string_member(password, "password")?,
        })
    }
}

impl Input for SetPasswordInput {
    fn from_json(value: &Value) -> Result<Self, String> {
        let [password] = input_members(value, ["password"])?;
        Ok(SetPasswordInput {
            password: string_member(password, "password")?,
        })
    }
}

impl Input for RoleInput {
    fn from_json(value: &Value) -> Result<Self, String> {
        let [name, permissions] = input_members(value, ["name", "permissions"])?;
        Ok(RoleInput {
            name: string_member(name, "name")?,
            permissions: permissions
                .map(|v| json::strings(v, "permissions"))
                .transpose()?
                .unwrap_or_default(),
        })
    }
}

/// `login`'s and `register`'s result: the user, when the session ends, and
/// the token, which a cookie session does not answer.
#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct LoginResult {
    pub user: SessionUser,
    /// RFC 3339, in UTC.
    pub expires_at: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub token: Option<String>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
pub struct SessionUser {
    pub id: String,
    pub login: String,
    pub name: String,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
pub struct RoleRef {
    pub id: String,
    pub name: String,
}

/// `me`'s result.
#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
pub struct CurrentUser {
    pub user: SessionUser,
    pub roles: Vec<RoleRef>,
    pub permissions: Vec<String>,
}

/// `capabilities`' result: for each operation an end user may call, by its
/// OpenAPI operation id, whether the route admits the caller.
#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
pub struct Capabilities {
    pub operations: BTreeMap<String, bool>,
}

/// A user as the administration routes answer one.
#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
pub struct IdentityUser {
    pub id: String,
    pub login: String,
    pub name: String,
    pub disabled: bool,
    pub roles: Vec<RoleRef>,
}

/// A role as the administration routes answer one.
#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
pub struct IdentityRole {
    pub id: String,
    pub name: String,
    pub permissions: Vec<String>,
}

/// A session `login` or `register` created: the result the route answers,
/// and the token and transport the cookie is set from.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct IssuedSession {
    pub result: LoginResult,
    pub token: String,
    pub transport: Transport,
    pub session_id: String,
    pub expires_at: SystemTime,
}

fn role_refs(roles: &[Role]) -> Vec<RoleRef> {
    roles
        .iter()
        .map(|role| RoleRef {
            id: role.id.clone(),
            name: role.name.clone(),
        })
        .collect()
}

fn identity_user(user: User) -> IdentityUser {
    IdentityUser {
        roles: role_refs(&user.roles),
        id: user.id,
        login: user.login,
        name: user.name,
        disabled: user.disabled,
    }
}

fn identity_role(role: Role) -> IdentityRole {
    IdentityRole {
        id: role.id,
        name: role.name,
        permissions: role.permissions,
    }
}

/// A login's transport: bearer when empty.
fn transport_of(session: &str, errors: &mut FieldErrors) -> Transport {
    match session {
        "" | "bearer" => Transport::Bearer,
        "cookie" => Transport::Cookie,
        _ => {
            errors.add("session", "enum", r#"session must be "bearer" or "cookie""#);
            Transport::Bearer
        }
    }
}

fn check_password_field(errors: &mut FieldErrors, field: &str, password: &str) {
    if password.is_empty() {
        errors.add(field, "required", format!("{field} is required"));
    } else if let Err(reason) = check_password(password) {
        errors.add(field, "length", reason);
    }
}

fn check_login_field(errors: &mut FieldErrors, login: &str) {
    if login.is_empty() {
        errors.add("login", "required", "login is required");
    }
}

/// How long ago `then` was, zero when it is not before `now`.
fn since(now: SystemTime, then: SystemTime) -> Duration {
    now.duration_since(then).unwrap_or(Duration::ZERO)
}

/// Runs a password hash or verify on the blocking pool, so its cost does
/// not hold an executor thread.
async fn blocking<T, F>(work: F) -> Result<T, ApiError>
where
    T: Send + 'static,
    F: FnOnce() -> T + Send + 'static,
{
    tokio::task::spawn_blocking(work)
        .await
        .map_err(|err| internal(&err))
}

impl Service {
    /// A service over `store` with `config`, which it validates. It hashes
    /// the dummy password an unknown login verifies against, at the
    /// config's cost.
    pub fn new(store: Arc<dyn Store>, config: Config) -> Result<Service, ConfigError> {
        config.validate()?;
        let params = config.argon2_params();
        let dummy = dummy_hash(params).map_err(|err| ConfigError(err.to_string()))?;
        Ok(Service {
            store,
            config,
            clock: Arc::new(SystemTime::now),
            permits: Arc::new(|held: &[String], required: &[String]| {
                has_any_permission(held, required)
            }),
            prefix: DEFAULT_PERMISSION_PREFIX.to_owned(),
            routes: Vec::new(),
            params,
            dummy,
        })
    }

    /// The service with the clock sessions are created, expired and touched
    /// by.
    #[must_use]
    pub fn with_clock(mut self, clock: Clock) -> Service {
        self.clock = clock;
        self
    }

    /// The service with the permission rule capabilities and the
    /// administration routes admit callers by, for a project whose router
    /// runs another than [`has_any_permission`]. [`super::IdentityAuthenticator`]
    /// answers the router's `permits` with it too.
    #[must_use]
    pub fn with_permits(mut self, permits: Permits) -> Service {
        self.permits = permits;
        self
    }

    /// The service with `identity_permission_prefix`, the prefix of the
    /// administration routes' permissions.
    pub fn with_permission_prefix(mut self, prefix: &str) -> Result<Service, ConfigError> {
        if !valid_permission(prefix) {
            return Err(ConfigError(format!(
                "the permission prefix {prefix:?} is not dotted segments of letters, digits, '_' and '-'"
            )));
        }
        prefix.clone_into(&mut self.prefix);
        Ok(self)
    }

    /// The service with the route requirements of every operation of the
    /// API, which `capabilities` answers for.
    #[must_use]
    pub fn with_routes(mut self, routes: Vec<Route>) -> Service {
        self.routes = routes;
        self
    }

    /// The config the service runs with.
    pub fn config(&self) -> &Config {
        &self.config
    }

    /// The store the service runs over.
    pub fn store(&self) -> &Arc<dyn Store> {
        &self.store
    }

    /// The full name of an administration permission
    /// ([`PERMISSION_USERS_READ`], ...) under the service's prefix.
    pub fn permission(&self, name: &str) -> String {
        format!("{}.{name}", self.prefix)
    }

    /// Whether permissions `held` satisfy `required`, by the service's rule.
    pub fn permits(&self, held: &[String], required: &[String]) -> bool {
        (self.permits)(held, required)
    }

    fn now(&self) -> SystemTime {
        (self.clock)()
    }

    async fn verify(&self, phc: String, password: &str) -> Result<Option<Verified>, ApiError> {
        let password = password.to_owned();
        let params = self.params;
        blocking(move || verify_password(&phc, &password, params).ok()).await
    }

    async fn hash(&self, password: &str) -> Result<String, ApiError> {
        let password = password.to_owned();
        let params = self.params;
        blocking(move || hash_password(&password, params))
            .await?
            .map_err(|err| internal(&err))
    }

    /// Signs a user in: verifies the password and creates a session. Every
    /// failure (an unknown login, a login the scalar refuses, a user with
    /// no password, a wrong one, a disabled user) is 401
    /// `invalid_credentials` after a verify of the same cost, so neither
    /// the answer nor its timing tells which. A hash at another cost is
    /// written again at the config's.
    pub async fn login(&self, input: &LoginInput) -> Result<IssuedSession, ApiError> {
        let mut errors = FieldErrors::default();
        let transport = transport_of(&input.session, &mut errors);
        check_login_field(&mut errors, &input.login);
        check_password_field(&mut errors, "password", &input.password);
        errors.check()?;

        let record = match self.store.find_login(&input.login).await {
            Ok(record) => record,
            Err(StoreError::NotFound | StoreError::InvalidLogin { .. }) => {
                self.verify(self.dummy.clone(), &input.password).await?;
                return Err(invalid_credentials());
            }
            Err(err) => return Err(internal(&err)),
        };
        if record.password_hash.is_empty() {
            self.verify(self.dummy.clone(), &input.password).await?;
            return Err(invalid_credentials());
        }
        let verified = self
            .verify(record.password_hash.clone(), &input.password)
            .await?;
        let Some(Verified { ok: true, rehash }) = verified else {
            return Err(invalid_credentials());
        };
        if record.user.disabled {
            return Err(invalid_credentials());
        }
        if rehash {
            let hash = self.hash(&input.password).await?;
            self.store
                .rehash_password(&record.user.id, &record.password_hash, &hash)
                .await
                .map_err(|err| internal(&err))?;
        }
        self.issue(&record.user, transport).await
    }

    /// Creates a user with a password and signs them in, as `login` does. A
    /// taken login is 409 `conflict`.
    pub async fn register(&self, input: &RegisterInput) -> Result<IssuedSession, ApiError> {
        let mut errors = FieldErrors::default();
        let transport = transport_of(&input.session, &mut errors);
        check_login_field(&mut errors, &input.login);
        check_password_field(&mut errors, "password", &input.password);
        errors.check()?;
        let user = self
            .create(&input.login, &input.name, &input.password)
            .await?;
        self.issue(&user, transport).await
    }

    async fn create(&self, login: &str, name: &str, password: &str) -> Result<User, ApiError> {
        let hash = self.hash(password).await?;
        let created = self
            .store
            .create_user(NewUser {
                login: login.to_owned(),
                name: name.to_owned(),
                password_hash: hash,
                at: self.now(),
            })
            .await;
        match created {
            Ok(user) => Ok(user),
            Err(StoreError::InvalidLogin { reason, .. }) => {
                Err(field_refusal("login", "parse", reason))
            }
            Err(StoreError::InvalidName { reason, .. }) => {
                Err(field_refusal("name", "parse", reason))
            }
            Err(err) => Err(store_error(err, "User")),
        }
    }

    /// Creates a session for `user`.
    async fn issue(&self, user: &User, transport: Transport) -> Result<IssuedSession, ApiError> {
        let token = new_token().map_err(|err| internal(&err))?;
        let now = self.now();
        let latest = UNIX_EPOCH + Duration::from_secs(LATEST_EXPIRY_SECONDS);
        let expires_at = now
            .checked_add(self.config.session_ttl())
            .map_or(latest, |t| t.min(latest));
        let session = self
            .store
            .create_session(NewSession {
                user_id: user.id.clone(),
                token_hash: hash_token(&token),
                created_at: now,
                expires_at,
            })
            .await
            .map_err(|err| internal(&err))?;
        let result = LoginResult {
            user: SessionUser {
                id: user.id.clone(),
                login: user.login.clone(),
                name: user.name.clone(),
            },
            expires_at: format_rfc3339(expires_at),
            token: (transport == Transport::Bearer).then(|| token.clone()),
        };
        Ok(IssuedSession {
            result,
            token,
            transport,
            session_id: session.id,
            expires_at,
        })
    }

    /// The cross-origin check of a login or register asking for a cookie
    /// session, since its cookie would sign the browser in: 403
    /// `cross_origin` when it refuses.
    pub fn check_cookie_login(&self, parts: &Parts, session: &str) -> Result<(), ApiError> {
        if session != Transport::Cookie.as_str() {
            return Ok(());
        }
        check_cross_origin(
            &parts.method,
            &request_host(parts),
            &parts.headers,
            &self.config.trusted_origins,
        )
        .map_err(|_| cross_origin())
    }

    /// The caller of a request, from its head: `None` when it carries no
    /// credential. The credential is the `Authorization` bearer token, or
    /// the session cookie without an `Authorization` header. A cookie
    /// request with a method other than `GET`, `HEAD` or `OPTIONS` passes
    /// the cross-origin check first (403 `cross_origin`). Then the session
    /// must exist, be neither revoked, expired nor idle, and belong to a
    /// user who is not disabled; every other outcome is 401 `unauthorized`,
    /// which for the session cookie carries the `Set-Cookie` that clears
    /// it.
    pub async fn authenticate(&self, parts: &Parts) -> Result<Option<IdentityPrincipal>, ApiError> {
        self.authenticate_head(&parts.method, &request_host(parts), &parts.headers)
            .await
    }

    /// [`Service::authenticate`] of a request's method, host (the `Host`
    /// header, or the URI's authority) and headers, for a caller with a
    /// request type of its own, such as a page's.
    pub async fn authenticate_head(
        &self,
        method: &Method,
        host: &[u8],
        headers: &HeaderMap,
    ) -> Result<Option<IdentityPrincipal>, ApiError> {
        let credential = match extract_credential(headers, self.config.cookie_name()) {
            CredentialOutcome::None => return Ok(None),
            CredentialOutcome::Invalid(transport) => {
                return Err(self.clearing(unauthenticated(), transport))
            }
            CredentialOutcome::Usable(credential) => credential,
        };
        if credential.transport == Transport::Cookie {
            check_cross_origin(method, host, headers, &self.config.trusted_origins)
                .map_err(|_| cross_origin())?;
        }
        self.authenticate_token(&credential.token, credential.transport)
            .await
            .map(Some)
            .map_err(|err| self.clearing(err, credential.transport))
    }

    /// A refusal of a request's credential: a 401 `unauthorized` of the
    /// session cookie also clears the cookie.
    fn clearing(&self, err: ApiError, transport: Transport) -> ApiError {
        if transport != Transport::Cookie
            || err.status != StatusCode::UNAUTHORIZED
            || err.code != CODE_UNAUTHORIZED
        {
            return err;
        }
        match HeaderValue::from_str(&self.config.clear_cookie()) {
            Ok(clear) => err.with_header(SET_COOKIE, clear),
            Err(_) => err,
        }
    }

    /// The caller a session token signs in, as `authenticate` resolves one
    /// once it has read the token from a request.
    pub async fn authenticate_token(
        &self,
        token: &str,
        transport: Transport,
    ) -> Result<IdentityPrincipal, ApiError> {
        let record = match self.store.find_session(&hash_token(token)).await {
            Ok(record) => record,
            Err(StoreError::NotFound) => return Err(unauthenticated()),
            Err(err) => return Err(internal(&err)),
        };
        let now = self.now();
        let session = &record.session;
        if session.revoked_at.is_some() || now >= session.expires_at || record.user.disabled {
            return Err(unauthenticated());
        }
        let last_seen = session.last_seen_at.unwrap_or(session.created_at);
        let idle = self.config.idle_timeout();
        if !idle.is_zero() && since(now, last_seen) >= idle {
            return Err(unauthenticated());
        }
        let interval = self.config.touch_interval();
        if session
            .last_seen_at
            .is_none_or(|seen| since(now, seen) >= interval)
        {
            let stale_before = now.checked_sub(interval).unwrap_or(UNIX_EPOCH);
            self.store
                .touch_session(&session.id, now, stale_before)
                .await
                .map_err(|err| internal(&err))?;
        }
        let roles = self
            .store
            .user_roles(&record.user.id)
            .await
            .map_err(|err| internal(&err))?;
        let permissions = effective_permissions(roles.iter().map(|r| r.permissions.as_slice()));
        Ok(IdentityPrincipal {
            id: record.user.id,
            login: record.user.login,
            name: record.user.name,
            roles,
            permissions,
            session_id: session.id.clone(),
            transport,
        })
    }

    /// Revokes the caller's session.
    pub async fn logout(&self, caller: &IdentityPrincipal) -> Result<(), ApiError> {
        if caller.session_id.is_empty() {
            return Err(unauthenticated());
        }
        self.store
            .revoke_session(&caller.session_id, self.now())
            .await
            .map_err(|err| internal(&err))
    }

    /// The caller's user, roles and permissions.
    pub fn me(&self, caller: &IdentityPrincipal) -> CurrentUser {
        CurrentUser {
            user: SessionUser {
                id: caller.id.clone(),
                login: caller.login.clone(),
                name: caller.name.clone(),
            },
            roles: role_refs(&caller.roles),
            permissions: caller.permissions.clone(),
        }
    }

    /// For each operation of the API an end user may call, whether its route
    /// admits the caller ([`capabilities_of`] over the routes
    /// [`Service::with_routes`] set).
    pub fn capabilities(&self, caller: &IdentityPrincipal) -> Capabilities {
        let permits = |held: &[String], required: &[String]| self.permits(held, required);
        Capabilities {
            operations: capabilities_of(&self.routes, &caller.permissions, &permits),
        }
    }

    /// Sets the caller's password once `current` verifies (401
    /// `invalid_credentials` otherwise), and revokes the user's other
    /// sessions.
    pub async fn change_password(
        &self,
        caller: &IdentityPrincipal,
        input: &ChangePasswordInput,
    ) -> Result<(), ApiError> {
        let mut errors = FieldErrors::default();
        check_password_field(&mut errors, "current", &input.current);
        check_password_field(&mut errors, "password", &input.password);
        errors.check()?;
        let record = match self.store.find_credential(&caller.id).await {
            Ok(record) => record,
            Err(StoreError::NotFound) => return Err(unauthenticated()),
            Err(err) => return Err(internal(&err)),
        };
        let stored = if record.password_hash.is_empty() {
            self.dummy.clone()
        } else {
            record.password_hash.clone()
        };
        let verified = self.verify(stored, &input.current).await?;
        if !verified.is_some_and(|v| v.ok) || record.password_hash.is_empty() {
            return Err(invalid_credentials());
        }
        let hash = self.hash(&input.password).await?;
        self.store
            .set_password(&caller.id, &hash, self.now(), Some(&caller.session_id))
            .await
            .map_err(|err| internal(&err))
    }

    /// Refuses a caller the rule does not give the administration
    /// permission `name`.
    fn require(&self, caller: &IdentityPrincipal, name: &str) -> Result<(), ApiError> {
        if caller.id.is_empty() {
            return Err(unauthenticated());
        }
        if !self.permits(&caller.permissions, &[self.permission(name)]) {
            return Err(forbidden("Insufficient permissions"));
        }
        Ok(())
    }

    /// Creates a user with a password (`users.write`).
    pub async fn create_user(
        &self,
        caller: &IdentityPrincipal,
        input: &CreateUserInput,
    ) -> Result<IdentityUser, ApiError> {
        self.require(caller, PERMISSION_USERS_WRITE)?;
        let mut errors = FieldErrors::default();
        check_login_field(&mut errors, &input.login);
        check_password_field(&mut errors, "password", &input.password);
        errors.check()?;
        self.create(&input.login, &input.name, &input.password)
            .await
            .map(identity_user)
    }

    /// Every user, by login (`users.read`).
    pub async fn list_users(
        &self,
        caller: &IdentityPrincipal,
    ) -> Result<Vec<IdentityUser>, ApiError> {
        self.require(caller, PERMISSION_USERS_READ)?;
        let users = self
            .store
            .list_users()
            .await
            .map_err(|err| internal(&err))?;
        Ok(users.into_iter().map(identity_user).collect())
    }

    /// One user (`users.read`).
    pub async fn get_user(
        &self,
        caller: &IdentityPrincipal,
        id: &str,
    ) -> Result<IdentityUser, ApiError> {
        self.require(caller, PERMISSION_USERS_READ)?;
        self.user(id).await
    }

    async fn user(&self, id: &str) -> Result<IdentityUser, ApiError> {
        self.store
            .get_user(id)
            .await
            .map(identity_user)
            .map_err(|err| store_error(err, "User"))
    }

    /// Disables a user and revokes their sessions (`users.write`).
    pub async fn disable_user(
        &self,
        caller: &IdentityPrincipal,
        id: &str,
    ) -> Result<IdentityUser, ApiError> {
        self.set_disabled(caller, id, true).await
    }

    /// Enables a disabled user (`users.write`).
    pub async fn enable_user(
        &self,
        caller: &IdentityPrincipal,
        id: &str,
    ) -> Result<IdentityUser, ApiError> {
        self.set_disabled(caller, id, false).await
    }

    async fn set_disabled(
        &self,
        caller: &IdentityPrincipal,
        id: &str,
        disabled: bool,
    ) -> Result<IdentityUser, ApiError> {
        self.require(caller, PERMISSION_USERS_WRITE)?;
        self.store
            .set_disabled(id, disabled, self.now())
            .await
            .map_err(|err| store_error(err, "User"))?;
        self.user(id).await
    }

    /// Sets a user's password and revokes their sessions (`users.write`).
    pub async fn set_user_password(
        &self,
        caller: &IdentityPrincipal,
        id: &str,
        input: &SetPasswordInput,
    ) -> Result<(), ApiError> {
        self.require(caller, PERMISSION_USERS_WRITE)?;
        let mut errors = FieldErrors::default();
        check_password_field(&mut errors, "password", &input.password);
        errors.check()?;
        let hash = self.hash(&input.password).await?;
        self.store
            .set_password(id, &hash, self.now(), None)
            .await
            .map_err(|err| store_error(err, "User"))
    }

    /// Every role, by name (`roles.read`).
    pub async fn list_roles(
        &self,
        caller: &IdentityPrincipal,
    ) -> Result<Vec<IdentityRole>, ApiError> {
        self.require(caller, PERMISSION_ROLES_READ)?;
        let roles = self
            .store
            .list_roles()
            .await
            .map_err(|err| store_error(err, "Role"))?;
        Ok(roles.into_iter().map(identity_role).collect())
    }

    /// A role's input checked, and its permissions without repeats: a name
    /// is required (400), every permission has a permission's form (422),
    /// and the caller covers each (403: no one grants what they do not
    /// hold).
    fn check_role(
        &self,
        caller: &IdentityPrincipal,
        input: &RoleInput,
    ) -> Result<Vec<String>, ApiError> {
        let mut errors = FieldErrors::default();
        if input.name.is_empty() {
            errors.add("name", "required", "name is required");
        }
        errors.check()?;
        let invalid: Vec<String> = input
            .permissions
            .iter()
            .filter(|p| !valid_permission(p))
            .cloned()
            .collect();
        if !invalid.is_empty() {
            return Err(invalid_permissions(invalid));
        }
        let mut seen = HashSet::new();
        let permissions: Vec<String> = input
            .permissions
            .iter()
            .filter(|p| seen.insert(p.as_str()))
            .cloned()
            .collect();
        let missing = uncovered(&caller.permissions, &permissions);
        if !missing.is_empty() {
            return Err(not_held(missing));
        }
        Ok(permissions)
    }

    /// Creates a role (`roles.write`). The caller's permissions must cover
    /// each of the role's.
    pub async fn create_role(
        &self,
        caller: &IdentityPrincipal,
        input: &RoleInput,
    ) -> Result<IdentityRole, ApiError> {
        self.require(caller, PERMISSION_ROLES_WRITE)?;
        let permissions = self.check_role(caller, input)?;
        self.store
            .create_role(&input.name, &permissions)
            .await
            .map(identity_role)
            .map_err(|err| store_error(err, "Role"))
    }

    /// Rewrites a role's name and permissions (`roles.write`). The caller's
    /// permissions must cover each of the new ones.
    pub async fn update_role(
        &self,
        caller: &IdentityPrincipal,
        id: &str,
        input: &RoleInput,
    ) -> Result<IdentityRole, ApiError> {
        self.require(caller, PERMISSION_ROLES_WRITE)?;
        let permissions = self.check_role(caller, input)?;
        self.store
            .update_role(id, &input.name, &permissions)
            .await
            .map(identity_role)
            .map_err(|err| store_error(err, "Role"))
    }

    /// Deletes a role and its grants (`roles.write`).
    pub async fn delete_role(&self, caller: &IdentityPrincipal, id: &str) -> Result<(), ApiError> {
        self.require(caller, PERMISSION_ROLES_WRITE)?;
        self.store
            .delete_role(id)
            .await
            .map_err(|err| store_error(err, "Role"))
    }

    /// Grants a user a role (`roles.write`). The caller's permissions must
    /// cover each of the role's.
    pub async fn grant_role(
        &self,
        caller: &IdentityPrincipal,
        user_id: &str,
        role_id: &str,
    ) -> Result<IdentityUser, ApiError> {
        self.require(caller, PERMISSION_ROLES_WRITE)?;
        let role = self
            .store
            .get_role(role_id)
            .await
            .map_err(|err| store_error(err, "Role"))?;
        let missing = uncovered(&caller.permissions, &role.permissions);
        if !missing.is_empty() {
            return Err(not_held(missing));
        }
        self.store
            .grant_role(user_id, role_id, self.now())
            .await
            .map_err(|err| store_error(err, "User or role"))?;
        self.user(user_id).await
    }

    /// Revokes a role from a user (`roles.write`).
    pub async fn revoke_role(
        &self,
        caller: &IdentityPrincipal,
        user_id: &str,
        role_id: &str,
    ) -> Result<IdentityUser, ApiError> {
        self.require(caller, PERMISSION_ROLES_WRITE)?;
        self.store
            .revoke_role(user_id, role_id)
            .await
            .map_err(|err| store_error(err, "User or role"))?;
        self.user(user_id).await
    }
}
