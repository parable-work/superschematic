use crate::{Principal, ServiceCaller};
use http::Method;
use std::collections::HashMap;

/// RequestContext is passed to generated implementation handlers.
///
/// Headers are forwarded so route-impl code can extract request metadata
/// (caller identity, request id, idempotency keys, etc.) without bypassing
/// the generated router plumbing. Keys are lowercased per HTTP semantics.
#[derive(Clone, Debug, Default)]
pub struct RequestContext {
    pub method: Method,
    pub route: String,
    pub path_params: HashMap<String, String>,
    pub query_params: HashMap<String, String>,
    pub headers: HashMap<String, String>,
    /// The caller the route's `Authenticator` established, on a route that
    /// needs one (`@auth`, `@requirePermission`, `@requireOwnership`, an
    /// `Authenticated` set); `None` elsewhere. An `@requireOwnership`
    /// implementation checks that this caller owns the resource.
    pub principal: Option<Principal>,
    /// The calling service the server's `ServiceAuthenticator` verified,
    /// when the request carries a service credential; `None` otherwise
    /// (D37). On an `@allowService` route that admitted it, no end user was
    /// authenticated and `principal` is `None`.
    pub service_caller: Option<ServiceCaller>,
}

impl RequestContext {
    pub fn new(method: Method, route: String) -> Self {
        Self {
            method,
            route,
            path_params: HashMap::new(),
            query_params: HashMap::new(),
            headers: HashMap::new(),
            principal: None,
            service_caller: None,
        }
    }

    /// Look up a header value by name. Header names are compared
    /// case-insensitively per RFC 7230.
    pub fn header(&self, name: &str) -> Option<&str> {
        let lower = name.to_ascii_lowercase();
        self.headers.get(&lower).map(String::as_str)
    }

    /// The end user's token from `Authorization: Bearer <token>`, which a
    /// service forwards, unchanged, on a call it makes for that user (D37).
    pub fn bearer_token(&self) -> Option<&str> {
        self.header("authorization")
            .and_then(crate::auth::parse_bearer)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn the_bearer_token_is_read_from_the_authorization_header() {
        let mut ctx = RequestContext::new(Method::GET, "/op".to_string());
        assert_eq!(ctx.bearer_token(), None);
        ctx.headers
            .insert("authorization".to_string(), "Bearer user-1".to_string());
        assert_eq!(ctx.bearer_token(), Some("user-1"));
        ctx.headers
            .insert("authorization".to_string(), "Basic dXNlcg==".to_string());
        assert_eq!(ctx.bearer_token(), None);
    }
}
