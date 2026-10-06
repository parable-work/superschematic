//! The permission gate, with the semantics of the Go runtime's session
//! package and the TypeScript runtime's `authorize`: no caller on a route
//! that needs one is 401 "Authentication required"; a caller whose
//! permissions cover none of the route's is 403 "Insufficient permissions".
//! Permissions are dotted paths. A granted permission covers a required one
//! when they are equal or the required one is nested under it (`reports`
//! covers `reports.export`; `report` does not). There is no root permission
//! that covers everything.
//!
//! How a caller is established is the service's business: the generated
//! `Implementations` takes an [`Authenticator`]. A project with its own
//! permission vocabulary (a root permission, roles resolved elsewhere)
//! overrides [`Authenticator::permits`].

use crate::ApiError;
use async_trait::async_trait;
use http::header::AUTHORIZATION;
use http::request::Parts;
use http::HeaderMap;
use serde_json::{Map, Value};
use std::fmt;

/// The caller an [`Authenticator`] established for a request.
#[derive(Clone, Default, PartialEq)]
pub struct Principal {
    /// Stable subject identifier: a user id, a service identity, an API key's owner.
    pub subject: String,
    pub permissions: Vec<String>,
    /// Verified claims for the implementation's own checks; never echoed.
    pub claims: Map<String, Value>,
}

impl Principal {
    pub fn new(
        subject: impl Into<String>,
        permissions: impl IntoIterator<Item = impl Into<String>>,
    ) -> Self {
        Self {
            subject: subject.into(),
            permissions: permissions.into_iter().map(Into::into).collect(),
            claims: Map::new(),
        }
    }
}

// Debug leaves the claims' values out, so a logged context does not carry
// a token's contents.
impl fmt::Debug for Principal {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("Principal")
            .field("subject", &self.subject)
            .field("permissions", &self.permissions)
            .field("claims", &self.claims.keys().collect::<Vec<_>>())
            .finish()
    }
}

/// Establishes the caller of a request to a route that needs one: a route
/// declared `@auth`, `@requirePermission` or `@requireOwnership`, or in an
/// `Authenticated` operation set. The router calls it after the webhook
/// verifier, the rate limit and the body limit, and before the timeout and
/// the handler; it sees the request's head, not its body.
#[async_trait]
pub trait Authenticator: Send + Sync + 'static {
    /// The caller, or `Ok(None)` when the request carries none (401). An
    /// `Err` answers with that error, for a failure that is not the caller's
    /// (an identity service that is down, say).
    async fn authenticate(&self, request: &Parts) -> Result<Option<Principal>, ApiError>;

    /// Whether the caller's permissions satisfy a route's
    /// `@requirePermission` list (403 otherwise). The router calls it only
    /// for a route that lists permissions. The default is
    /// [`has_any_permission`]: dotted-path coverage, no root permission.
    fn permits(&self, held: &[String], required: &[String]) -> bool {
        has_any_permission(held, required)
    }
}

/// Whether a granted permission covers a required one: equal, or the
/// required one nested under it.
pub fn covers(granted: &str, required: &str) -> bool {
    granted == required
        || required
            .strip_prefix(granted)
            .is_some_and(|rest| rest.starts_with('.'))
}

/// True when `required` is empty or any held permission covers any
/// required one.
pub fn has_any_permission(held: &[String], required: &[String]) -> bool {
    required.is_empty()
        || required
            .iter()
            .any(|need| held.iter().any(|have| covers(have, need)))
}

/// The permission gate for a caller already established: the rule the
/// router applies to a request's caller (`RouteControls::authorize`), and
/// an in-process caller to its own (`OperationInfo::admit`). No caller is
/// 401 "Authentication required"; a caller whose permissions
/// `authenticator.permits` finds do not satisfy a non-empty `required` is
/// 403 "Insufficient permissions".
pub fn admit(
    authenticator: &dyn Authenticator,
    principal: Option<Principal>,
    required: &[String],
) -> Result<Principal, ApiError> {
    let principal = principal.ok_or_else(|| ApiError::unauthorized("Authentication required"))?;
    if !required.is_empty() && !authenticator.permits(&principal.permissions, required) {
        return Err(ApiError::forbidden("Insufficient permissions"));
    }
    Ok(principal)
}

/// The token of an `Authorization: Bearer <token>` header, for an
/// authenticator that reads one; `None` without the header or another scheme.
pub fn bearer_token(headers: &HeaderMap) -> Option<&str> {
    let value = headers.get(AUTHORIZATION)?.to_str().ok()?;
    let (scheme, token) = value.split_once(' ')?;
    let token = token.trim();
    (scheme.eq_ignore_ascii_case("bearer") && !token.is_empty()).then_some(token)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn strings(values: &[&str]) -> Vec<String> {
        values.iter().map(ToString::to_string).collect()
    }

    #[test]
    fn a_permission_covers_itself_and_what_is_nested_under_it() {
        assert!(covers("reports", "reports"));
        assert!(covers("reports", "reports.export"));
        assert!(!covers("report", "reports.export"));
        assert!(!covers("reports.export", "reports"));
        assert!(!covers("", "reports"));
    }

    #[test]
    fn any_held_permission_covering_any_required_one_permits() {
        assert!(has_any_permission(&[], &[]));
        assert!(has_any_permission(
            &strings(&["orders"]),
            &strings(&["orders.read"])
        ));
        assert!(has_any_permission(
            &strings(&["a", "orders.write"]),
            &strings(&["orders.read", "orders.write"])
        ));
        assert!(!has_any_permission(
            &strings(&["orders.read"]),
            &strings(&["orders.write"])
        ));
        assert!(!has_any_permission(&[], &strings(&["orders.read"])));
    }

    #[test]
    fn a_bearer_token_is_read_from_the_authorization_header() {
        let mut headers = HeaderMap::new();
        assert_eq!(bearer_token(&headers), None);
        headers.insert(AUTHORIZATION, "Bearer abc.def".parse().unwrap());
        assert_eq!(bearer_token(&headers), Some("abc.def"));
        headers.insert(AUTHORIZATION, "bearer xyz".parse().unwrap());
        assert_eq!(bearer_token(&headers), Some("xyz"));
        headers.insert(AUTHORIZATION, "Basic dXNlcjpwdw==".parse().unwrap());
        assert_eq!(bearer_token(&headers), None);
        headers.insert(AUTHORIZATION, "Bearer ".parse().unwrap());
        assert_eq!(bearer_token(&headers), None);
    }

    #[test]
    fn debug_leaves_out_the_claims_values() {
        let mut principal = Principal::new("user-1", ["orders.read"]);
        principal
            .claims
            .insert("token".to_string(), Value::String("secret".to_string()));
        let printed = format!("{principal:?}");
        assert!(
            printed.contains("user-1") && printed.contains("token"),
            "{printed}"
        );
        assert!(!printed.contains("secret"), "{printed}");
    }
}
