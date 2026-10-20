//! An operation's input and result, as the generated handlers take and
//! answer them: the input type parsed by its generated `parse_<type>` with
//! undeclared top-level keys refused, the body object scalar arguments are
//! read from, and the result in the success envelope. Also what an
//! in-process caller needs to run an operation by the route's rules: the
//! route's facts (`OperationInfo`), its admission, and the input check.

use axum::response::{IntoResponse, Response};
use axum::Json;
use http::Method;
use serde::Serialize;
use serde_json::{Map, Value};

use crate::schema::{ParseError, UnknownFields, ValidationErrors};
use crate::service_auth::service_step;
use crate::{
    admit, error_response, wrap_envelope, ApiError, Authenticator, ObjectPrepare, Principal,
    RequestContext, ServiceCaller,
};

/// One operation as the service's router serves it. The generated crate's
/// `operations` module declares one for each operation
/// (`operations::ORDERS_GET_ORDER`), so a caller that runs an operation
/// in-process (a page, a job) applies the route's own rules instead of
/// restating them.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct OperationInfo {
    /// The operation set the operation belongs to and the operation's name,
    /// as the schema declares them.
    pub namespace: &'static str,
    pub name: &'static str,
    /// The route's method, upper case, and its path with `{param}`
    /// captures.
    pub method: &'static str,
    pub path: &'static str,
    /// Whether the route needs a caller: `@auth`, `@requirePermission`,
    /// `@requireOwnership`, or an `Authenticated` operation set.
    pub requires_auth: bool,
    /// The route's `@requirePermission` list, of which the caller must
    /// satisfy one; empty without one.
    pub permissions: &'static [&'static str],
    /// `@requireOwnership`: the implementation checks that the caller owns
    /// the resource.
    pub require_ownership: bool,
    /// `@manualRouteRegistration`: the service mounts the route itself; the
    /// router does not, and the namespace trait has no method for it.
    pub manual: bool,
    /// The route's `@rateLimit`, in requests per minute; `None` without one
    /// (or below 1). Its limiter is the one a [`crate::RateLimits`] holds
    /// for the operation, which every way into it that shares the registry
    /// counts against (D44, amended).
    pub rate_limit: Option<u32>,
    /// The operation's effective `@requireService` or `@allowService`
    /// clause, as its route applies it (D37); `None` without one.
    pub service: Option<ServiceClause>,
}

/// An operation's effective `@requireService` or `@allowService` clause
/// (D37), as its route applies it.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct ServiceClause {
    pub mode: ServiceMode,
    /// The APIs a caller must serve one of; empty lists every caller.
    pub from: &'static [&'static str],
}

/// Which service clause an operation declares.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum ServiceMode {
    /// `@requireService`: only a listed service caller, which forwards an
    /// end user who meets the user clause when the route has one.
    Require,
    /// `@allowService`: a listed service caller stands in for the end user;
    /// any other call goes on to the end-user step.
    Allow,
}

impl OperationInfo {
    /// The caller the route would hand the implementation, for a call that
    /// names no service caller (D43), admitted as the route admits a
    /// request without a service credential: first the service step
    /// ([`OperationInfo::admit_service`] with none), which refuses a
    /// `@requireService` operation, with or without a user clause, 401
    /// `service_unauthorized` (D37, amended); then the end-user step
    /// ([`OperationInfo::admit_end_user`]).
    pub fn admit(
        &self,
        authenticator: &dyn Authenticator,
        principal: Option<Principal>,
    ) -> Result<Option<Principal>, ApiError> {
        self.admit_service(None)?;
        self.admit_end_user(authenticator, principal)
    }

    /// The route's service step, by the router's own rule, for an
    /// in-process call (D37, amended). `caller` is the service the process
    /// acts for, as [`ServiceCaller::in_process`] names it, or none.
    ///
    /// `Ok(true)` says the caller alone is admitted, so the end-user step is
    /// skipped: a caller the clause's `from` lists, of an `@allowService`
    /// operation. `Ok(false)` says the end-user step still applies
    /// ([`OperationInfo::admit_end_user`]): for an operation without a
    /// clause, for any other call of an `@allowService` operation with a
    /// user clause, and for a listed caller of a `@requireService` one. A
    /// `@requireService` operation refuses a call without a caller 401
    /// `service_unauthorized` and a caller its `from` does not list 403
    /// `service_forbidden`, as its route refuses them.
    pub fn admit_service(&self, caller: Option<&ServiceCaller>) -> Result<bool, ApiError> {
        match self.service {
            Some(clause) => service_step(clause.mode, clause.from, caller, self.requires_auth),
            None => Ok(false),
        }
    }

    /// The route's end-user step alone: for an operation that needs a
    /// caller, `principal` through [`admit`] (401 without one, 403 without
    /// a permission the operation lists); for one that does not, none, as
    /// the route establishes none.
    pub fn admit_end_user(
        &self,
        authenticator: &dyn Authenticator,
        principal: Option<Principal>,
    ) -> Result<Option<Principal>, ApiError> {
        if !self.requires_auth {
            return Ok(None);
        }
        let required: Vec<String> = self
            .permissions
            .iter()
            .map(|permission| (*permission).to_owned())
            .collect();
        admit(authenticator, principal, &required).map(Some)
    }

    /// The `RequestContext` the route would build for the implementation,
    /// for a call that is not a request: the route's method and path and the
    /// admitted caller. It has no headers and no path or query parameters;
    /// the operation's `Args` carry its arguments.
    pub fn context(&self, principal: Option<Principal>) -> RequestContext {
        let method = Method::from_bytes(self.method.as_bytes()).unwrap_or(Method::POST);
        let mut ctx = RequestContext::new(method, self.path.to_owned());
        ctx.principal = principal;
        ctx
    }

    /// [`OperationInfo::context`] of a call `caller` makes, with the
    /// service caller the route would hand the implementation in
    /// `ctx.service_caller` (D37, amended).
    pub fn service_context(
        &self,
        caller: ServiceCaller,
        principal: Option<Principal>,
    ) -> RequestContext {
        let mut ctx = self.context(principal);
        ctx.service_caller = Some(caller);
        ctx
    }
}

/// Checks an input a caller built, not a request's body, as the router
/// checks a body: its JSON goes through the input type's generated
/// `prepare_<type>` with undeclared keys refused, so an input the router
/// would refuse is refused with the same 400 ([`input_refusal`]).
pub fn check_input<T: Serialize>(input: &T, prepare: ObjectPrepare) -> Result<(), ApiError> {
    let value =
        serde_json::to_value(input).map_err(|err| input_refusal(ParseError::Decode(err)))?;
    prepare(value, UnknownFields::Refuse)
        .map(drop)
        .map_err(input_refusal)
}

/// An input type's generated `parse_<type>`.
pub type InputParse<T> = fn(Value, UnknownFields) -> Result<T, ParseError>;

/// The operation's optional input: `None` without a body, else the body
/// parsed as the input type.
pub fn input<T>(body: Option<Value>, parse: InputParse<T>) -> Result<Option<T>, ApiError> {
    body.map(|body| parse(body, UnknownFields::Refuse).map_err(input_refusal))
        .transpose()
}

/// The operation's required input: 400 without a body, else the body parsed
/// as the input type.
pub fn required_input<T>(body: Option<Value>, parse: InputParse<T>) -> Result<T, ApiError> {
    let body = body.ok_or_else(|| ApiError::bad_request("Request body is required"))?;
    parse(body, UnknownFields::Refuse).map_err(input_refusal)
}

/// The 400 of a body the input type refuses, with the same detail as the
/// TypeScript server's. `details.reason` says which step refused it; a key
/// the type does not declare and a field that breaks a rule are also in the
/// top-level `errors` member the Go server writes and every SDK reads,
/// keyed by path.
pub fn input_refusal(err: ParseError) -> ApiError {
    let (reason, errors) = match err {
        ParseError::NotAnObject => ("expected an object".to_owned(), None),
        ParseError::UnknownFields(keys) => {
            let mut errors = ValidationErrors::new();
            for key in &keys {
                errors.add(key.as_str(), "unknown", "unknown field");
            }
            (format!("unknown fields: {}", keys.join(", ")), Some(errors))
        }
        ParseError::Invalid(errors) => ("validation failed".to_owned(), Some(errors)),
        ParseError::Decode(_) => ("does not match the declared type".to_owned(), None),
    };
    let mut details = Map::new();
    details.insert("location".to_owned(), Value::from("body"));
    details.insert("reason".to_owned(), Value::from(reason));
    let refusal = ApiError::bad_request("Request body does not match the declared input")
        .with_details(Value::Object(details));
    match errors.map(|errors| serde_json::to_value(&errors)) {
        Some(Ok(errors)) => refusal.with_errors(errors),
        _ => refusal,
    }
}

/// The body object a non-GET operation's scalar arguments are read from:
/// `None` without a body, which is 400 when an argument is required, and
/// 400 for a body that is not a JSON object.
pub fn body_fields(
    body: Option<&Value>,
    required: bool,
) -> Result<Option<&Map<String, Value>>, ApiError> {
    match body {
        None if required => Err(ApiError::bad_request("Request body is required")),
        None => Ok(None),
        Some(Value::Object(fields)) => Ok(Some(fields)),
        Some(_) => Err(ApiError::bad_request("Request body must be a JSON object")),
    }
}

/// What a handler answers for its implementation's result: the result's
/// JSON in the success envelope, or the refusal's problem. A result that
/// does not serialize is 500.
pub fn operation_response<T: Serialize>(
    result: Result<T, ApiError>,
    request_id: Option<&str>,
) -> Response {
    match result.and_then(|data| {
        serde_json::to_value(data).map_err(|err| {
            ApiError::internal(format!("The response could not be serialized: {err}"))
        })
    }) {
        Ok(data) => Json(wrap_envelope(data, request_id)).into_response(),
        Err(err) => error_response(err),
    }
}

#[cfg(test)]
mod tests {
    use axum::http::StatusCode;
    use serde_json::json;

    use super::*;

    fn parse_named(value: Value, unknown: UnknownFields) -> Result<String, ParseError> {
        let object = value.as_object().ok_or(ParseError::NotAnObject)?;
        if unknown == UnknownFields::Refuse {
            let extra: Vec<String> = object
                .keys()
                .filter(|key| *key != "name")
                .cloned()
                .collect();
            if !extra.is_empty() {
                return Err(ParseError::UnknownFields(extra));
            }
        }
        match object.get("name").and_then(Value::as_str) {
            Some(name) => Ok(name.to_owned()),
            None => {
                let mut errors = ValidationErrors::new();
                errors.add("name", "required", "required field");
                Err(ParseError::Invalid(errors))
            }
        }
    }

    #[test]
    fn an_input_is_parsed_with_unknown_keys_refused() {
        assert_eq!(
            required_input(Some(json!({"name": "a"})), parse_named).unwrap(),
            "a"
        );
        assert_eq!(input(None, parse_named).unwrap(), None);

        let err = required_input(None, parse_named).unwrap_err();
        assert_eq!(
            (err.status, err.message.as_str()),
            (StatusCode::BAD_REQUEST, "Request body is required")
        );

        let err = required_input(Some(json!({"name": "a", "x": 1})), parse_named).unwrap_err();
        assert_eq!(
            err.message,
            "Request body does not match the declared input"
        );
        assert_eq!(
            err.details.as_deref(),
            Some(&json!({"location": "body", "reason": "unknown fields: x"}))
        );
        assert_eq!(
            err.errors.as_deref(),
            Some(&json!({"x": [{"validator": "unknown", "message": "unknown field"}]}))
        );

        let err = input(Some(json!({})), parse_named).unwrap_err();
        assert_eq!(err.details.unwrap()["reason"], "validation failed");
        assert_eq!(
            err.errors.as_deref(),
            Some(&json!({"name": [{"validator": "required", "message": "required field"}]}))
        );

        let err = input(Some(json!([])), parse_named).unwrap_err();
        assert!(err.errors.is_none());
    }

    fn prepare_named(value: Value, unknown: UnknownFields) -> Result<Value, ParseError> {
        parse_named(value.clone(), unknown).map(|_| value)
    }

    #[derive(Serialize)]
    struct Named {
        #[serde(skip_serializing_if = "Option::is_none")]
        name: Option<String>,
    }

    #[test]
    fn a_built_input_is_checked_as_a_body_is() {
        check_input(
            &Named {
                name: Some("a".to_owned()),
            },
            prepare_named,
        )
        .unwrap();
        let err = check_input(&Named { name: None }, prepare_named).unwrap_err();
        let refused = input(Some(json!({})), parse_named).unwrap_err();
        assert_eq!(
            (err.status, &err.message, &err.details, &err.errors),
            (
                refused.status,
                &refused.message,
                &refused.details,
                &refused.errors
            )
        );
    }

    struct Tokens;

    #[async_trait::async_trait]
    impl Authenticator for Tokens {
        async fn authenticate(
            &self,
            _request: &http::request::Parts,
        ) -> Result<Option<Principal>, ApiError> {
            Ok(None)
        }
    }

    const AUDIT: OperationInfo = OperationInfo {
        namespace: "orders",
        name: "audit",
        method: "GET",
        path: "/api/orders/{id}/audit",
        requires_auth: true,
        permissions: &["orders.audit"],
        require_ownership: false,
        manual: false,
        rate_limit: None,
        service: None,
    };

    #[test]
    fn an_operation_admits_a_caller_as_its_route_does() {
        let err = AUDIT.admit(&Tokens, None).unwrap_err();
        assert_eq!(
            (err.status, err.message.as_str()),
            (StatusCode::UNAUTHORIZED, "Authentication required")
        );
        let err = AUDIT
            .admit(&Tokens, Some(Principal::new("u", ["orders.read"])))
            .unwrap_err();
        assert_eq!(
            (err.status, err.message.as_str()),
            (StatusCode::FORBIDDEN, "Insufficient permissions")
        );
        let caller = AUDIT
            .admit(&Tokens, Some(Principal::new("u", ["orders"])))
            .unwrap();
        assert_eq!(caller.map(|caller| caller.subject), Some("u".to_owned()));

        let open = OperationInfo {
            requires_auth: false,
            permissions: &[],
            ..AUDIT
        };
        assert_eq!(
            open.admit(&Tokens, Some(Principal::new("u", ["orders"])))
                .unwrap(),
            None
        );

        let ctx = AUDIT.context(Some(Principal::new("u", ["orders"])));
        assert_eq!(
            (ctx.method, ctx.route.as_str()),
            (Method::GET, "/api/orders/{id}/audit")
        );
        assert_eq!(
            ctx.principal.map(|caller| caller.subject),
            Some("u".to_owned())
        );
    }

    /// Reads `Authorization: Bearer <subject>:<permission>,...`.
    struct Users;

    #[async_trait::async_trait]
    impl Authenticator for Users {
        async fn authenticate(
            &self,
            request: &http::request::Parts,
        ) -> Result<Option<Principal>, ApiError> {
            Ok(crate::bearer_token(&request.headers).map(principal_of))
        }
    }

    /// Reads `Service-Authorization: Bearer <deployable>:<api>,...`.
    struct Services;

    #[async_trait::async_trait]
    impl crate::ServiceAuthenticator for Services {
        async fn authenticate(
            &self,
            request: &http::request::Parts,
        ) -> Result<Option<ServiceCaller>, ApiError> {
            Ok(request
                .headers
                .get(crate::SERVICE_AUTHORIZATION)
                .and_then(|value| crate::auth::parse_bearer(value.to_str().ok()?))
                .map(caller_of))
        }
    }

    fn principal_of(token: &str) -> Principal {
        let (subject, permissions) = token.split_once(':').unwrap_or((token, ""));
        Principal::new(subject, permissions.split(',').filter(|p| !p.is_empty()))
    }

    fn caller_of(token: &str) -> ServiceCaller {
        let (deployable, serves) = token.split_once(':').unwrap_or((token, ""));
        ServiceCaller::in_process(deployable, serves.split(',').filter(|api| !api.is_empty()))
    }

    fn refusal<T: std::fmt::Debug>(result: Result<T, ApiError>) -> (StatusCode, String, String) {
        let err = result.unwrap_err();
        (err.status, err.code, err.message)
    }

    fn refused(status: StatusCode, code: &str, message: &str) -> (StatusCode, String, String) {
        (status, code.to_owned(), message.to_owned())
    }

    /// A `@requireService({ from: [shop-orders] })` operation with a user
    /// clause.
    const RESERVE: OperationInfo = OperationInfo {
        namespace: "stock",
        name: "reserveStock",
        method: "POST",
        path: "/api/stock/{id}/reserve",
        requires_auth: true,
        permissions: &["stock.write"],
        require_ownership: false,
        manual: false,
        rate_limit: None,
        service: Some(ServiceClause {
            mode: ServiceMode::Require,
            from: &["shop-orders"],
        }),
    };

    /// `RESERVE` as `@allowService`.
    const RELEASE: OperationInfo = OperationInfo {
        name: "releaseReservation",
        service: Some(ServiceClause {
            mode: ServiceMode::Allow,
            from: &["shop-orders"],
        }),
        ..RESERVE
    };

    fn ana() -> Option<Principal> {
        Some(Principal::new("ana", ["stock.write"]))
    }

    #[test]
    fn an_operation_only_services_call_refuses_a_call_without_one() {
        let without_a_user_clause = OperationInfo {
            requires_auth: false,
            permissions: &[],
            ..RESERVE
        };
        for operation in [RESERVE, without_a_user_clause] {
            let want = refused(
                StatusCode::UNAUTHORIZED,
                "service_unauthorized",
                "Service credential required",
            );
            assert_eq!(refusal(operation.admit(&Users, ana())), want);
            assert_eq!(refusal(operation.admit(&Users, None)), want);
            assert_eq!(refusal(operation.admit_service(None)), want);
        }
    }

    #[test]
    fn a_service_caller_is_held_to_from() {
        let listed = ServiceCaller::in_process("app", ["shop-orders"]);
        let unlisted = ServiceCaller::in_process("billing", ["shop-billing"]);
        assert!(!RESERVE.admit_service(Some(&listed)).unwrap());
        assert_eq!(
            refusal(RESERVE.admit_service(Some(&unlisted))),
            refused(
                StatusCode::FORBIDDEN,
                "service_forbidden",
                "Service not permitted"
            )
        );
        let any = OperationInfo {
            service: Some(ServiceClause {
                mode: ServiceMode::Require,
                from: &[],
            }),
            ..RESERVE
        };
        assert!(!any.admit_service(Some(&unlisted)).unwrap());
        // A listed caller still forwards an end user who meets the clause.
        assert_eq!(
            refusal(RESERVE.admit_end_user(&Users, None)),
            refused(
                StatusCode::UNAUTHORIZED,
                "unauthorized",
                "Authentication required"
            )
        );
        let caller = RESERVE.admit_end_user(&Users, ana()).unwrap();
        assert_eq!(caller.map(|caller| caller.subject), Some("ana".to_owned()));
    }

    #[test]
    fn a_listed_caller_of_an_allow_service_operation_stands_in_for_the_end_user() {
        let listed = ServiceCaller::in_process("app", ["shop-orders"]);
        let unlisted = ServiceCaller::in_process("billing", ["shop-billing"]);
        assert!(RELEASE.admit_service(Some(&listed)).unwrap());
        // Anyone else goes on to the end-user step.
        assert!(!RELEASE.admit_service(Some(&unlisted)).unwrap());
        assert!(!RELEASE.admit_service(None).unwrap());
        assert_eq!(
            refusal(RELEASE.admit_end_user(&Users, None)),
            refused(
                StatusCode::UNAUTHORIZED,
                "unauthorized",
                "Authentication required"
            )
        );
        // admit, which names no service caller, is that step.
        let caller = RELEASE.admit(&Users, ana()).unwrap();
        assert_eq!(caller.map(|caller| caller.subject), Some("ana".to_owned()));
        // An operation without a clause has no service step.
        let open = OperationInfo {
            service: None,
            ..RESERVE
        };
        assert!(!open.admit_service(Some(&unlisted)).unwrap());
        assert!(!open.admit_service(None).unwrap());
    }

    #[test]
    fn a_service_callers_context_names_it() {
        let caller = ServiceCaller::in_process("app", ["shop-orders"]);
        let ctx = RESERVE.service_context(caller.clone(), ana());
        assert_eq!(ctx.service_caller, Some(caller));
        assert_eq!(
            ctx.principal.map(|principal| principal.subject),
            Some("ana".to_owned())
        );
        assert_eq!(
            (ctx.method, ctx.route.as_str()),
            (Method::POST, "/api/stock/{id}/reserve")
        );
        assert_eq!(RESERVE.context(None).service_caller, None);
    }

    /// What an in-process call gets: the service step, then, unless the
    /// caller alone is admitted, the end-user step.
    fn in_process(
        operation: &OperationInfo,
        caller: Option<&str>,
        user: Option<&str>,
    ) -> (StatusCode, String) {
        let caller = caller.map(caller_of);
        let admitted = operation
            .admit_service(caller.as_ref())
            .and_then(|alone| match alone {
                true => Ok(()),
                false => operation
                    .admit_end_user(&Users, user.map(principal_of))
                    .map(drop),
            });
        match admitted {
            Ok(()) => (StatusCode::OK, String::new()),
            Err(err) => (err.status, err.code),
        }
    }

    /// What a request to the route gets.
    async fn routed(
        router: &axum::Router,
        caller: Option<&str>,
        user: Option<&str>,
    ) -> (StatusCode, String) {
        use tower::ServiceExt;

        let mut request = http::Request::post("/op");
        if let Some(token) = caller {
            request = request.header(crate::SERVICE_AUTHORIZATION, format!("Bearer {token}"));
        }
        if let Some(token) = user {
            request = request.header("authorization", format!("Bearer {token}"));
        }
        let response = router
            .clone()
            .oneshot(request.body(axum::body::Body::empty()).unwrap())
            .await
            .unwrap();
        let status = response.status();
        let bytes = axum::body::to_bytes(response.into_body(), usize::MAX)
            .await
            .unwrap();
        let code = serde_json::from_slice::<Value>(&bytes)
            .ok()
            .and_then(|body| body["code"].as_str().map(str::to_owned))
            .unwrap_or_default();
        (status, code)
    }

    // admit_service and the router apply one rule: over each mode, from,
    // caller (none, listed, not listed), user clause and end user (none,
    // with the permission, without it), an in-process call gets the
    // status and code a request to a RouteControls route gets.
    #[tokio::test]
    async fn the_in_process_service_step_is_the_routes() {
        use crate::RouteControls;
        use std::sync::Arc;

        let froms: [&'static [&'static str]; 2] = [&[], &["shop-orders"]];
        let callers = [
            None,
            Some("orders:shop-orders"),
            Some("billing:shop-billing"),
        ];
        let users = [None, Some("ana:stock.write"), Some("bob:stock.read")];
        let mut checked = 0;
        let mut answers = std::collections::BTreeSet::new();
        for mode in [ServiceMode::Require, ServiceMode::Allow] {
            for from in froms {
                for user_clause in [false, true] {
                    let operation = OperationInfo {
                        requires_auth: user_clause,
                        permissions: if user_clause { &["stock.write"] } else { &[] },
                        service: Some(ServiceClause { mode, from }),
                        ..RESERVE
                    };
                    let controls = match mode {
                        ServiceMode::Require => {
                            RouteControls::new().require_service(Arc::new(Services), from)
                        }
                        ServiceMode::Allow => {
                            RouteControls::new().allow_service(Arc::new(Services), from)
                        }
                    };
                    let controls = match user_clause {
                        true => controls.authorize(Arc::new(Users), operation.permissions),
                        false => controls,
                    };
                    let router = axum::Router::new()
                        .route("/op", controls.apply(axum::routing::post(|| async { "" })));
                    for caller in callers {
                        for user in users {
                            let answer = in_process(&operation, caller, user);
                            answers.insert((answer.0.as_u16(), answer.1.clone()));
                            assert_eq!(
                                answer,
                                routed(&router, caller, user).await,
                                "{mode:?} from {from:?}, user clause {user_clause}, \
                                 caller {caller:?}, end user {user:?}"
                            );
                            checked += 1;
                        }
                    }
                }
            }
        }
        assert_eq!(checked, 72);
        let answers: Vec<(u16, &str)> = answers
            .iter()
            .map(|(status, code)| (*status, code.as_str()))
            .collect();
        assert_eq!(
            answers,
            [
                (200, ""),
                (401, "service_unauthorized"),
                (401, "unauthorized"),
                (403, "forbidden"),
                (403, "service_forbidden"),
            ]
        );
    }

    #[test]
    fn scalar_arguments_need_an_object_body() {
        assert_eq!(body_fields(None, false).unwrap(), None);
        assert_eq!(
            body_fields(None, true).unwrap_err().message,
            "Request body is required"
        );
        assert_eq!(
            body_fields(Some(&json!([1])), false).unwrap_err().message,
            "Request body must be a JSON object"
        );
        assert!(body_fields(Some(&json!({"a": 1})), true).unwrap().is_some());
    }
}
