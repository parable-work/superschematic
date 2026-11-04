#[allow(unused_imports)]
use serde::Serialize;


use crate::client::HttpClient;
use crate::errors::SDKError;
use crate::runtime;
use crate::types;

#[derive(Clone)]
pub struct TaskAdminNamespace {
    client: HttpClient,
}

impl TaskAdminNamespace {
    pub(crate) fn new(
        client: HttpClient,
    ) -> Self {
        Self {
            client,
        }
    }
    /// reassign_task endpoint.
    ///
    /// Requires authentication.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn reassign_task(
        &self,
        id: types::IdentityUUID,
        input: types::TaskAssignment,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<types::Task, SDKError> {
        let path = &format!("/api/tasks/{}/assignment", runtime::path_segment(&id));
        let input_value = serde_json::to_value(&input)?;
        runtime::validate_input("TaskAssignment", &input_value)?;
        let query_params: Vec<(String, String)> = Vec::new();
        let body = Some(input_value);
        self.client
            .request_json("PATCH", path, &query_params, body, options)
            .await
    }
}
