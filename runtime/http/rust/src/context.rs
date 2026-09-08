use http::Method;
use std::collections::HashMap;

/// RequestContext is passed to generated implementation handlers.
///
/// Headers are forwarded so route-impl code can extract request metadata
/// (tenant identity, request id, idempotency keys, etc.) without bypassing
/// the generated router plumbing. Keys are lowercased per HTTP semantics.
#[derive(Clone, Debug, Default)]
pub struct RequestContext {
    pub method: Method,
    pub route: String,
    pub path_params: HashMap<String, String>,
    pub query_params: HashMap<String, String>,
    pub headers: HashMap<String, String>,
}

impl RequestContext {
    pub fn new(method: Method, route: String) -> Self {
        Self {
            method,
            route,
            path_params: HashMap::new(),
            query_params: HashMap::new(),
            headers: HashMap::new(),
        }
    }

    /// Look up a header value by name. Header names are compared
    /// case-insensitively per RFC 7230.
    pub fn header(&self, name: &str) -> Option<&str> {
        let lower = name.to_ascii_lowercase();
        self.headers.get(&lower).map(String::as_str)
    }
}
