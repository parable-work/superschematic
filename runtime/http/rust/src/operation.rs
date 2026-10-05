//! An operation's input and result, as the generated handlers take and
//! answer them: the input type parsed by its generated `parse_<type>` with
//! undeclared top-level keys refused, the body object scalar arguments are
//! read from, and the result in the success envelope. Also what an
//! in-process caller needs to run an operation by the route's rules: the
//! route's facts (`OperationInfo`) and the input check.

use axum::response::{IntoResponse, Response};
use axum::Json;
use http::Method;
use serde::Serialize;
use serde_json::{Map, Value};

use crate::schema::{ParseError, UnknownFields, ValidationErrors};
use crate::{
    admit, error_response, wrap_envelope, ApiError, Authenticator, ObjectPrepare, Principal,
    RequestContext,
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
}

impl OperationInfo {
    /// The caller the route would hand the implementation, admitted as the
    /// route admits a request's: for an operation that needs one,
    /// `principal` through [`admit`] (401 without one, 403 without a
    /// permission the operation lists); for one that does not, none, as the
    /// route establishes none.
    pub fn admit(
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
