#[allow(unused_imports)]
use serde::Serialize;


use crate::client::HttpClient;
use crate::errors::SDKError;
use crate::runtime;
use crate::types;

#[derive(Clone)]
pub struct EventNamespace {
    client: HttpClient,
}

impl EventNamespace {
    pub(crate) fn new(
        client: HttpClient,
    ) -> Self {
        Self {
            client,
        }
    }
    /// A received event. Not a webhook, so no verifier runs.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn get_event(
        &self,
        id: String,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<types::EventReceipt, SDKError> {
        let path = &format!("/api/events/{}", runtime::path_segment(&id));
        let query_params: Vec<(String, String)> = Vec::new();
        self.client
            .request_json("GET", path, &query_params, None, options)
            .await
    }
}
