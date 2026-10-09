//! The identity runtime over HTTP: the router's authenticator, an axum
//! handler for every operation of `ir/identity_routes.go`, and the layer
//! that answers the trusted origins' CORS.

use std::collections::HashMap;
use std::convert::Infallible;
use std::future::Future;
use std::pin::Pin;
use std::sync::Arc;
use std::task::{Context, Poll};

use async_trait::async_trait;
use axum::body::Body;
use axum::extract::{FromRequestParts, Path, Request};
use axum::response::{IntoResponse, Response};
use axum::routing::{on, MethodFilter, MethodRouter};
use axum::Router;
use http::header::{
    ACCESS_CONTROL_ALLOW_CREDENTIALS, ACCESS_CONTROL_ALLOW_HEADERS, ACCESS_CONTROL_ALLOW_METHODS,
    ACCESS_CONTROL_ALLOW_ORIGIN, ACCESS_CONTROL_MAX_AGE, ACCESS_CONTROL_REQUEST_HEADERS,
    ACCESS_CONTROL_REQUEST_METHOD, ORIGIN, SET_COOKIE, VARY,
};
use http::request::Parts;
use http::{HeaderValue, Method, StatusCode};
use serde::Serialize;
use serde_json::Value;

use crate::{
    error_response, operation_response, path_is_percent_encoded, request_id_from_headers, ApiError,
    Authenticator, Principal,
};

use super::cross_origin::first;
use super::errors::unauthenticated;
use super::service::{
    ChangePasswordInput, CreateUserInput, IdentityPrincipal, Input, IssuedSession, LoginInput,
    RegisterInput, RoleInput, Service, SetPasswordInput,
};
use super::token::Transport;

/// The largest body an identity route reads.
const MAX_BODY_BYTES: usize = 64 * 1024;

/// The operations of `@userSessions`, as `ir/identity_routes.go` names
/// them.
pub const OP_LOGIN: &str = "login";
pub const OP_LOGOUT: &str = "logout";
pub const OP_ME: &str = "me";
pub const OP_CAPABILITIES: &str = "capabilities";
pub const OP_CHANGE_PASSWORD: &str = "changePassword";
pub const OP_REGISTER: &str = "register";
/// The operations of `@userAdministration`.
pub const OP_CREATE_USER: &str = "createUser";
pub const OP_LIST_USERS: &str = "listUsers";
pub const OP_GET_USER: &str = "getUser";
pub const OP_DISABLE_USER: &str = "disableUser";
pub const OP_ENABLE_USER: &str = "enableUser";
pub const OP_SET_USER_PASSWORD: &str = "setUserPassword";
pub const OP_LIST_ROLES: &str = "listRoles";
pub const OP_CREATE_ROLE: &str = "createRole";
pub const OP_UPDATE_ROLE: &str = "updateRole";
pub const OP_DELETE_ROLE: &str = "deleteRole";
pub const OP_GRANT_ROLE: &str = "grantRole";
pub const OP_REVOKE_ROLE: &str = "revokeRole";

/// The default route prefixes of the two sets.
pub const DEFAULT_USER_SESSIONS_PATH: &str = "auth";
pub const DEFAULT_USER_ADMINISTRATION_PATH: &str = "auth/admin";

/// One of the user model's routes: its operation's name, the set it belongs
/// to, its method and its path under the set's path, with `{id}` and
/// `{roleId}` for its path parameters.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Operation {
    pub name: &'static str,
    /// The route is `@userAdministration`'s; otherwise `@userSessions`'.
    pub administration: bool,
    pub method: &'static str,
    pub path: &'static str,
}

const fn op(
    name: &'static str,
    administration: bool,
    method: &'static str,
    path: &'static str,
) -> Operation {
    Operation {
        name,
        administration,
        method,
        path,
    }
}

/// Every route of the user model, in `ir/identity_routes.go`'s order.
pub const OPERATIONS: &[Operation] = &[
    op(OP_LOGIN, false, "POST", "login"),
    op(OP_LOGOUT, false, "POST", "logout"),
    op(OP_ME, false, "GET", "me"),
    op(OP_CAPABILITIES, false, "GET", "capabilities"),
    op(OP_CHANGE_PASSWORD, false, "POST", "password"),
    op(OP_REGISTER, false, "POST", "register"),
    op(OP_CREATE_USER, true, "POST", "users"),
    op(OP_LIST_USERS, true, "GET", "users"),
    op(OP_GET_USER, true, "GET", "users/{id}"),
    op(OP_DISABLE_USER, true, "POST", "users/{id}/disable"),
    op(OP_ENABLE_USER, true, "POST", "users/{id}/enable"),
    op(OP_SET_USER_PASSWORD, true, "PUT", "users/{id}/password"),
    op(OP_LIST_ROLES, true, "GET", "roles"),
    op(OP_CREATE_ROLE, true, "POST", "roles"),
    op(OP_UPDATE_ROLE, true, "PUT", "roles/{id}"),
    op(OP_DELETE_ROLE, true, "DELETE", "roles/{id}"),
    op(OP_GRANT_ROLE, true, "PUT", "users/{id}/roles/{roleId}"),
    op(OP_REVOKE_ROLE, true, "DELETE", "users/{id}/roles/{roleId}"),
];

/// `@userSessions` on an operation set, as `ir.UserSessionsConfig` holds it.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct UserSessions {
    /// The set's route prefix; [`DEFAULT_USER_SESSIONS_PATH`] when empty.
    pub path: String,
    /// `login: false`: `me` and `capabilities` alone.
    pub no_login: bool,
    /// `register: true`: also `register`.
    pub register: bool,
}

/// `@userAdministration` on an operation set.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct UserAdministration {
    /// The set's route prefix; [`DEFAULT_USER_ADMINISTRATION_PATH`] when
    /// empty.
    pub path: String,
}

/// The router's [`Authenticator`] over the identity service: the caller of
/// a request is its session's user, with the permissions of the user's
/// roles, so `admit`, `RouteControls::authorize` and an in-process caller's
/// `OperationInfo::admit` work unchanged. A request with no credential has
/// no caller (401 from the router); one whose credential is refused is the
/// service's 401 `unauthorized`, whose problem clears a refused session
/// cookie, or 403 `cross_origin`.
#[derive(Clone, Debug)]
pub struct IdentityAuthenticator {
    service: Arc<Service>,
}

impl IdentityAuthenticator {
    pub fn new(service: Arc<Service>) -> Self {
        IdentityAuthenticator { service }
    }

    pub fn service(&self) -> &Arc<Service> {
        &self.service
    }
}

#[async_trait]
impl Authenticator for IdentityAuthenticator {
    async fn authenticate(&self, request: &Parts) -> Result<Option<Principal>, ApiError> {
        Ok(self
            .service
            .authenticate(request)
            .await?
            .map(|caller| caller.to_principal()))
    }

    fn permits(&self, held: &[String], required: &[String]) -> bool {
        self.service.permits(held, required)
    }
}

fn method_filter(method: &str) -> MethodFilter {
    match method {
        "GET" => MethodFilter::GET,
        "PUT" => MethodFilter::PUT,
        "DELETE" => MethodFilter::DELETE,
        _ => MethodFilter::POST,
    }
}

impl Service {
    /// The handler of the operation named `op` (an `OP_` constant), or
    /// `None` for a name that is none, to mount at the operation's route. A
    /// route that needs a caller reads the [`Principal`] the router's
    /// [`IdentityAuthenticator`] put on the request, or authenticates the
    /// request itself without one. Success is 200 with the success envelope:
    /// the operation's result, or `true` for `logout`, `changePassword`,
    /// `setUserPassword` and `deleteRole`, which the contract types as a
    /// boolean since the schema language has no void result. A refusal is
    /// the problem.
    pub fn handler<S>(self: &Arc<Self>, op: &str) -> Option<MethodRouter<S>>
    where
        S: Clone + Send + Sync + 'static,
    {
        let operation = OPERATIONS.iter().find(|o| o.name == op)?;
        let service = Arc::clone(self);
        let name = operation.name;
        Some(on(
            method_filter(operation.method),
            move |request: Request| {
                let service = Arc::clone(&service);
                async move { service.serve(name, request).await }
            },
        ))
    }

    /// The routes of the sets an API declares, as `(method, path,
    /// handler)`: `@userSessions`' under its path when `sessions` is given
    /// (`login`, `logout` and `changePassword` unless `no_login`,
    /// `register` only with `register`), and `@userAdministration`'s under
    /// its path when `administration` is. Paths start with `/`; the caller
    /// adds its server's own prefix.
    pub fn routes<S>(
        self: &Arc<Self>,
        sessions: Option<&UserSessions>,
        administration: Option<&UserAdministration>,
    ) -> Vec<(&'static str, String, MethodRouter<S>)>
    where
        S: Clone + Send + Sync + 'static,
    {
        let mut routes = Vec::new();
        for operation in OPERATIONS {
            let base = if operation.administration {
                match administration {
                    Some(set) if set.path.is_empty() => DEFAULT_USER_ADMINISTRATION_PATH,
                    Some(set) => set.path.as_str(),
                    None => continue,
                }
            } else {
                match sessions {
                    Some(set)
                        if set.no_login && !matches!(operation.name, OP_ME | OP_CAPABILITIES) =>
                    {
                        continue
                    }
                    Some(set) if !set.register && operation.name == OP_REGISTER => continue,
                    Some(set) if set.path.is_empty() => DEFAULT_USER_SESSIONS_PATH,
                    Some(set) => set.path.as_str(),
                    None => continue,
                }
            };
            if let Some(handler) = self.handler(operation.name) {
                let path = format!("/{}/{}", base.trim_matches('/'), operation.path);
                routes.push((operation.method, path, handler));
            }
        }
        routes
    }

    /// A router of [`Service::routes`], two operations on one path merged.
    pub fn router<S>(
        self: &Arc<Self>,
        sessions: Option<&UserSessions>,
        administration: Option<&UserAdministration>,
    ) -> Router<S>
    where
        S: Clone + Send + Sync + 'static,
    {
        let mut merged: Vec<(String, MethodRouter<S>)> = Vec::new();
        for (_, path, handler) in self.routes(sessions, administration) {
            match merged.iter_mut().find(|(p, _)| *p == path) {
                Some((_, existing)) => {
                    let current = std::mem::take(existing);
                    *existing = current.merge(handler);
                }
                None => merged.push((path, handler)),
            }
        }
        merged
            .into_iter()
            .fold(Router::new(), |router, (path, handler)| {
                router.route(&path, handler)
            })
    }

    /// The credentialed CORS layer of the config's trusted origins, to wrap
    /// the API's router in.
    pub fn cors(&self) -> CorsLayer {
        CorsLayer::new(self.config().trusted_origins.clone())
    }

    async fn serve(self: Arc<Self>, op: &'static str, request: Request) -> Response {
        let (mut parts, body) = request.into_parts();
        let request_id = request_id_from_headers(&parts.headers);
        match self
            .dispatch(op, &mut parts, body, request_id.as_deref())
            .await
        {
            Ok(response) => response,
            Err(err) => error_response(err),
        }
    }

    async fn dispatch(
        &self,
        op: &str,
        parts: &mut Parts,
        body: Body,
        request_id: Option<&str>,
    ) -> Result<Response, ApiError> {
        if op == OP_LOGIN {
            let input: LoginInput = decode(body).await?;
            self.check_cookie_login(parts, &input.session)?;
            let issued = self.login(&input).await?;
            return Ok(self.issued(issued, request_id));
        }
        if op == OP_REGISTER {
            let input: RegisterInput = decode(body).await?;
            self.check_cookie_login(parts, &input.session)?;
            let issued = self.register(&input).await?;
            return Ok(self.issued(issued, request_id));
        }
        let caller = self.caller(parts).await?;
        match op {
            OP_LOGOUT | OP_ME | OP_CAPABILITIES | OP_CHANGE_PASSWORD => {
                self.serve_session(op, &caller, body, request_id).await
            }
            OP_CREATE_USER | OP_LIST_USERS | OP_GET_USER | OP_DISABLE_USER | OP_ENABLE_USER
            | OP_SET_USER_PASSWORD => self.serve_users(op, &caller, parts, body, request_id).await,
            _ => self.serve_roles(op, &caller, parts, body, request_id).await,
        }
    }

    /// The session routes a caller calls.
    async fn serve_session(
        &self,
        op: &str,
        caller: &IdentityPrincipal,
        body: Body,
        request_id: Option<&str>,
    ) -> Result<Response, ApiError> {
        match op {
            OP_LOGOUT => {
                self.logout(caller).await?;
                let mut response = done(request_id);
                if caller.transport == Transport::Cookie {
                    append_cookie(&mut response, &self.config().clear_cookie());
                }
                Ok(response)
            }
            OP_ME => Ok(respond(&self.me(caller), request_id)),
            OP_CAPABILITIES => Ok(respond(&self.capabilities(caller), request_id)),
            _ => {
                let input: ChangePasswordInput = decode(body).await?;
                self.change_password(caller, &input).await?;
                Ok(done(request_id))
            }
        }
    }

    /// The administration routes of users.
    async fn serve_users(
        &self,
        op: &str,
        caller: &IdentityPrincipal,
        parts: &mut Parts,
        body: Body,
        request_id: Option<&str>,
    ) -> Result<Response, ApiError> {
        let ok = |data: &dyn ErasedSerialize| respond(data, request_id);
        match op {
            OP_CREATE_USER => {
                let input: CreateUserInput = decode(body).await?;
                Ok(ok(&self.create_user(caller, &input).await?))
            }
            OP_LIST_USERS => Ok(ok(&self.list_users(caller).await?)),
            _ => {
                let id = path_param(parts, "id").await?;
                match op {
                    OP_GET_USER => Ok(ok(&self.get_user(caller, &id).await?)),
                    OP_DISABLE_USER => Ok(ok(&self.disable_user(caller, &id).await?)),
                    OP_ENABLE_USER => Ok(ok(&self.enable_user(caller, &id).await?)),
                    _ => {
                        let input: SetPasswordInput = decode(body).await?;
                        self.set_user_password(caller, &id, &input).await?;
                        Ok(done(request_id))
                    }
                }
            }
        }
    }

    /// The administration routes of roles and grants.
    async fn serve_roles(
        &self,
        op: &str,
        caller: &IdentityPrincipal,
        parts: &mut Parts,
        body: Body,
        request_id: Option<&str>,
    ) -> Result<Response, ApiError> {
        let ok = |data: &dyn ErasedSerialize| respond(data, request_id);
        match op {
            OP_LIST_ROLES => Ok(ok(&self.list_roles(caller).await?)),
            OP_CREATE_ROLE => {
                let input: RoleInput = decode(body).await?;
                Ok(ok(&self.create_role(caller, &input).await?))
            }
            OP_UPDATE_ROLE | OP_DELETE_ROLE => {
                let id = path_param(parts, "id").await?;
                if op == OP_DELETE_ROLE {
                    self.delete_role(caller, &id).await?;
                    return Ok(done(request_id));
                }
                let input: RoleInput = decode(body).await?;
                Ok(ok(&self.update_role(caller, &id, &input).await?))
            }
            OP_GRANT_ROLE | OP_REVOKE_ROLE => {
                let id = path_param(parts, "id").await?;
                let role_id = path_param(parts, "roleId").await?;
                let user = if op == OP_GRANT_ROLE {
                    self.grant_role(caller, &id, &role_id).await?
                } else {
                    self.revoke_role(caller, &id, &role_id).await?
                };
                Ok(ok(&user))
            }
            _ => Err(ApiError::not_found("No such identity operation")),
        }
    }

    /// The request's caller: the one the router's authenticator put on it,
    /// or one authenticated now.
    async fn caller(&self, parts: &Parts) -> Result<IdentityPrincipal, ApiError> {
        if let Some(caller) = parts
            .extensions
            .get::<Principal>()
            .and_then(IdentityPrincipal::from_principal)
        {
            return Ok(caller);
        }
        self.authenticate(parts).await?.ok_or_else(unauthenticated)
    }

    /// A login's or register's answer, with the session cookie for a cookie
    /// session.
    fn issued(&self, issued: IssuedSession, request_id: Option<&str>) -> Response {
        let mut response = respond(&issued.result, request_id);
        if issued.transport == Transport::Cookie {
            let cookie = self
                .config()
                .session_cookie(&issued.token, self.config().session_ttl());
            append_cookie(&mut response, &cookie);
        }
        response
    }
}

/// A result serialized into the success envelope.
trait ErasedSerialize {
    fn to_json(&self) -> Result<Value, serde_json::Error>;
}

impl<T: Serialize> ErasedSerialize for T {
    fn to_json(&self) -> Result<Value, serde_json::Error> {
        serde_json::to_value(self)
    }
}

fn respond(data: &dyn ErasedSerialize, request_id: Option<&str>) -> Response {
    operation_response(
        data.to_json().map_err(|err| {
            ApiError::internal(format!("The response could not be serialized: {err}"))
        }),
        request_id,
    )
}

/// The answer of an operation whose result is `true`: `logout`,
/// `changePassword`, `setUserPassword` and `deleteRole`.
fn done(request_id: Option<&str>) -> Response {
    respond(&true, request_id)
}

fn append_cookie(response: &mut Response, cookie: &str) {
    if let Ok(value) = HeaderValue::from_str(cookie) {
        response.headers_mut().append(SET_COOKIE, value);
    }
}

/// A route's JSON body read into its input, refusing a body over 64 KiB,
/// one that is not the input's JSON, and an unknown member, with 400
/// `bad_request`.
async fn decode<T: Input>(body: Body) -> Result<T, ApiError> {
    let refuse = |reason: &str| {
        ApiError::bad_request(format!(
            "The body is not the operation's JSON input: {reason}"
        ))
    };
    let bytes = axum::body::to_bytes(body, MAX_BODY_BYTES)
        .await
        .map_err(|_| refuse("the body is larger than 64 KiB or could not be read"))?;
    let value: Value = serde_json::from_slice(&bytes).map_err(|err| refuse(&err.to_string()))?;
    T::from_json(&value).map_err(|err| refuse(&err))
}

/// A path parameter, decoded once: 400 when the path's escapes do not
/// decode to UTF-8, or the parameter is empty.
async fn path_param(parts: &mut Parts, name: &str) -> Result<String, ApiError> {
    if !path_is_percent_encoded(parts.uri.path()) {
        return Err(ApiError::bad_request(format!(
            "{name} must be percent-encoded UTF-8"
        )));
    }
    let params = Path::<HashMap<String, String>>::from_request_parts(parts, &())
        .await
        .map(|Path(params)| params)
        .unwrap_or_default();
    params
        .get(name)
        .filter(|value| !value.is_empty())
        .cloned()
        .ok_or_else(|| ApiError::bad_request(format!("{name} is required")))
}

/// The credentialed CORS of the trusted origins, as the Go runtime's `CORS`
/// middleware answers it, around an API's router. A request whose `Origin`
/// is trusted gets `Access-Control-Allow-Origin` (the origin) and
/// `Access-Control-Allow-Credentials: true`, and its preflight (`OPTIONS`
/// with `Access-Control-Request-Method`) is answered here with 204, the
/// method and headers it asks for, and a ten-minute `Max-Age`. Any other
/// request passes through without CORS headers, so a browser keeps a page
/// from another origin from reading the answer. Every request with an
/// `Origin` gets `Vary: Origin`, since the answer depends on it.
#[derive(Clone, Debug)]
pub struct CorsLayer {
    trusted: Arc<[String]>,
}

impl CorsLayer {
    /// The layer of `trusted_origins`, the config's.
    pub fn new(trusted_origins: Vec<String>) -> Self {
        CorsLayer {
            trusted: trusted_origins.into(),
        }
    }
}

impl<S> tower_layer::Layer<S> for CorsLayer {
    type Service = Cors<S>;

    fn layer(&self, inner: S) -> Self::Service {
        Cors {
            inner,
            trusted: Arc::clone(&self.trusted),
        }
    }
}

/// The service [`CorsLayer`] wraps a router's in.
#[derive(Clone, Debug)]
pub struct Cors<S> {
    inner: S,
    trusted: Arc<[String]>,
}

impl<S> tower_service::Service<Request> for Cors<S>
where
    S: tower_service::Service<Request, Response = Response, Error = Infallible>
        + Clone
        + Send
        + 'static,
    S::Future: Send + 'static,
{
    type Response = Response;
    type Error = Infallible;
    type Future = Pin<Box<dyn Future<Output = Result<Response, Infallible>> + Send>>;

    fn poll_ready(&mut self, cx: &mut Context<'_>) -> Poll<Result<(), Self::Error>> {
        self.inner.poll_ready(cx)
    }

    fn call(&mut self, request: Request) -> Self::Future {
        let clone = self.inner.clone();
        let mut inner = std::mem::replace(&mut self.inner, clone);
        let origin = first(request.headers(), ORIGIN.as_str()).to_vec();
        let trusted = !origin.is_empty() && self.trusted.iter().any(|t| t.as_bytes() == origin);
        let asked_method = first(request.headers(), ACCESS_CONTROL_REQUEST_METHOD.as_str());
        if trusted && request.method() == Method::OPTIONS && !asked_method.is_empty() {
            let asked_headers = first(request.headers(), ACCESS_CONTROL_REQUEST_HEADERS.as_str());
            let response = preflight(&origin, asked_method, asked_headers);
            return Box::pin(async move { Ok(response) });
        }
        Box::pin(async move {
            let mut response = inner.call(request).await?;
            if !origin.is_empty() {
                let headers = response.headers_mut();
                headers.append(VARY, HeaderValue::from_static("Origin"));
                if trusted {
                    if let Ok(value) = HeaderValue::from_bytes(&origin) {
                        headers.entry(ACCESS_CONTROL_ALLOW_ORIGIN).or_insert(value);
                    }
                    headers
                        .entry(ACCESS_CONTROL_ALLOW_CREDENTIALS)
                        .or_insert(HeaderValue::from_static("true"));
                }
            }
            Ok(response)
        })
    }
}

/// The answer to a trusted origin's preflight.
fn preflight(origin: &[u8], method: &[u8], asked_headers: &[u8]) -> Response {
    let mut response = StatusCode::NO_CONTENT.into_response();
    let headers = response.headers_mut();
    for vary in [
        "Origin",
        "Access-Control-Request-Method",
        "Access-Control-Request-Headers",
    ] {
        headers.append(VARY, HeaderValue::from_static(vary));
    }
    let value = |bytes: &[u8]| HeaderValue::from_bytes(bytes).ok();
    if let Some(origin) = value(origin) {
        headers.insert(ACCESS_CONTROL_ALLOW_ORIGIN, origin);
    }
    headers.insert(
        ACCESS_CONTROL_ALLOW_CREDENTIALS,
        HeaderValue::from_static("true"),
    );
    if let Some(method) = value(method) {
        headers.insert(ACCESS_CONTROL_ALLOW_METHODS, method);
    }
    if !asked_headers.is_empty() {
        if let Some(asked) = value(asked_headers) {
            headers.insert(ACCESS_CONTROL_ALLOW_HEADERS, asked);
        }
    }
    headers.insert(ACCESS_CONTROL_MAX_AGE, HeaderValue::from_static("600"));
    response
}
