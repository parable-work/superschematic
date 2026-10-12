#[allow(unused_imports)]
use serde::Serialize;


use crate::client::HttpClient;
use crate::errors::SDKError;
use crate::runtime;
use crate::types;

#[derive(Clone)]
pub struct GreetingNamespace {
    client: HttpClient,
}

impl GreetingNamespace {
    pub(crate) fn new(
        client: HttpClient,
    ) -> Self {
        Self {
            client,
        }
    }
    /// greet endpoint.
    ///
    /// Requires authentication.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn greet(
        &self,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<types::Greeting, SDKError> {
        let path = "/api/greeting";
        let query_params: Vec<(String, String)> = Vec::new();
        self.client
            .request_json("GET", path, &query_params, None, options)
            .await
    }
}
