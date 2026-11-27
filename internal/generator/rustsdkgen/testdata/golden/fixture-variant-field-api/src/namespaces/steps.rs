#[allow(unused_imports)]
use serde::Serialize;


use crate::client::HttpClient;
use crate::errors::SDKError;
use crate::runtime;
use crate::types;

#[derive(Clone)]
pub struct StepsNamespace {
    client: HttpClient,
}

impl StepsNamespace {
    pub(crate) fn new(
        client: HttpClient,
    ) -> Self {
        Self {
            client,
        }
    }
    /// Records a step.
    ///
    /// Requires authentication.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn create_step(
        &self,
        input: types::CreateStepInput,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<types::StepView, SDKError> {
        let path = "/api/steps";
        let input_value = serde_json::to_value(&input)?;
        runtime::validate_input("CreateStepInput", &input_value)?;
        let query_params: Vec<(String, String)> = Vec::new();
        let body = Some(input_value);
        self.client
            .request_json("POST", path, &query_params, body, options)
            .await
    }
    /// One step.
    ///
    /// Requires authentication.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn get_step(
        &self,
        id: types::IdentityUUID,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<types::StepView, SDKError> {
        let path = &format!("/api/steps/{}", runtime::path_segment(&id));
        let query_params: Vec<(String, String)> = Vec::new();
        self.client
            .request_json("GET", path, &query_params, None, options)
            .await
    }
}
