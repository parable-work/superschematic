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
//!
//! Every caller is one principal, of a kind (D156): a user, signed in by a
//! session or an API key; a party, a row of an app table that holds a
//! grant; an agent run; or a runtime provider calling as itself (D159). A
//! principal says whom it acts for, the grants it calls through and the
//! credential it called with, as the TypeScript runtime's `Principal` does.

use crate::ApiError;
use async_trait::async_trait;
use http::header::AUTHORIZATION;
use http::request::Parts;
use http::{HeaderMap, Method};
use serde::{Deserialize, Serialize};
use serde_json::{Map, Value};
use std::collections::BTreeMap;
use std::fmt;

/// The caller an [`Authenticator`] established for a request.
#[derive(Clone, Default, PartialEq)]
pub struct Principal {
    /// Stable subject identifier: a user id, an API key's owner, a party's
    /// or an agent run's id. A calling service is the request's
    /// `ServiceCaller`, not a principal (D37).
    pub subject: String,
    pub permissions: Vec<String>,
    /// Verified claims for the implementation's own checks; never echoed.
    pub claims: Map<String, Value>,
    /// What the principal is (D156): a user unless an authenticator says
    /// otherwise, as every authenticator's was before D156.
    pub kind: PrincipalKind,
    /// Whom the principal acts for (D156): the user an agent run serves,
    /// the party it answers. An audience admits a row an agent run or whom
    /// it acts for may see, and the access log records both. `None` for a
    /// principal that acts for itself.
    pub acting_for: Option<ActingFor>,
    /// The grants a party or an agent run calls through (D156): each one's
    /// target and the relation paths its role reaches from it. A principal
    /// with grants reaches, of a table, only the rows one of them reaches.
    /// `None` for a user, who reaches rows through roles and row rules.
    pub grants: Option<Vec<GrantReach>>,
    /// The credential the call came with, for the access log (D114, D156):
    /// an API key's or a grant's id; `None` for a session.
    pub credential: Option<Credential>,
}

impl Principal {
    pub fn new(
        subject: impl Into<String>,
        permissions: impl IntoIterator<Item = impl Into<String>>,
    ) -> Self {
        Self {
            subject: subject.into(),
            permissions: permissions.into_iter().map(Into::into).collect(),
            ..Self::default()
        }
    }

    /// The principal as one of `kind` (D156).
    #[must_use]
    pub fn of_kind(mut self, kind: PrincipalKind) -> Self {
        self.kind = kind;
        self
    }

    /// The principal acting for `acting_for` (D156).
    #[must_use]
    pub fn acting_for(mut self, acting_for: ActingFor) -> Self {
        self.acting_for = Some(acting_for);
        self
    }

    /// The principal calling through `grants` (D156).
    #[must_use]
    pub fn with_grants(mut self, grants: Vec<GrantReach>) -> Self {
        self.grants = Some(grants);
        self
    }

    /// The principal that called with `credential` (D114, D156).
    #[must_use]
    pub fn with_credential(mut self, credential: Credential) -> Self {
        self.credential = Some(credential);
        self
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
            .field("kind", &self.kind)
            .field("acting_for", &self.acting_for)
            .field("grants", &self.grants)
            .field("credential", &self.credential)
            .finish()
    }
}

/// The kinds of principal (D156): a user, a party, an agent run, or a
/// runtime provider (D159), which acts as itself with the narrow role its
/// package declares. Their wire names are the TypeScript runtime's.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Hash, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum PrincipalKind {
    /// A user, signed in by a session or an API key.
    #[default]
    User,
    /// A row of an app table that holds a grant.
    Party,
    /// An agent run.
    Agent,
    /// A runtime provider calling as itself (D159).
    Extension,
}

impl PrincipalKind {
    /// The kind's wire name: `user`, `party`, `agent` or `extension`.
    pub fn as_str(self) -> &'static str {
        match self {
            PrincipalKind::User => "user",
            PrincipalKind::Party => "party",
            PrincipalKind::Agent => "agent",
            PrincipalKind::Extension => "extension",
        }
    }
}

impl fmt::Display for PrincipalKind {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(self.as_str())
    }
}

/// Whom a principal acts for (D156): its kind and its subject. The
/// engine's principal names the subject alone (D77), and passes as this
/// one, so an absent kind reads as user, as the principal's own does.
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct ActingFor {
    /// What it is, a user unless set.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub kind: Option<PrincipalKind>,
    /// Its subject.
    pub subject: String,
}

impl ActingFor {
    /// The user whose id is `subject`.
    pub fn user(subject: impl Into<String>) -> Self {
        Self {
            kind: Some(PrincipalKind::User),
            subject: subject.into(),
        }
    }
}

/// What one grant reaches (D156): its target row, by its table's type and
/// its key, and the relation paths its role reaches other rows by, by
/// table: `{"Request": ["unit.contract"]}` reaches a request whose unit's
/// contract is the target. The target's own table reaches the target, and
/// a path `*` every row of its table.
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct GrantReach {
    /// The grant's id.
    pub id: String,
    /// The target's table type.
    pub target_type: String,
    /// The target's key, in its wire form.
    pub target_id: String,
    /// The paths the grant's role reaches rows by, by table type.
    #[serde(default)]
    pub reaches: BTreeMap<String, Vec<String>>,
}

/// The credential a call came with (D114, D156), which the access log
/// records: an API key's id or a grant's.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct Credential {
    /// What it is.
    pub kind: CredentialKind,
    /// Its id.
    pub id: String,
}

/// What a credential is (D156).
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub enum CredentialKind {
    /// An API key (D114).
    ApiKey,
    /// A grant's token (D156).
    Grant,
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

    /// Told of each call of the operation `operation` once its caller is
    /// established, and before its permissions are checked, so a refused
    /// call is told too (D156): what records a call in the access log
    /// (`access::Logged`). The router tells it on a route that needs a
    /// caller whose controls name the operation; `principal` is `None` for
    /// a request that carries none. An `Err` answers the request with it.
    /// The default does nothing.
    async fn called(
        &self,
        principal: Option<&Principal>,
        operation: &str,
        method: &Method,
    ) -> Result<(), ApiError> {
        let _ = (principal, operation, method);
        Ok(())
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
    parse_bearer(headers.get(AUTHORIZATION)?.to_str().ok()?)
}

/// The token of a `Bearer <token>` header value.
pub(crate) fn parse_bearer(value: &str) -> Option<&str> {
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
