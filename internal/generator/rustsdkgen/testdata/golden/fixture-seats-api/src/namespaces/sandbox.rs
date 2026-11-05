#[allow(unused_imports)]
use serde::Serialize;


use crate::client::HttpClient;
use crate::errors::SDKError;
use crate::runtime;
use crate::types;

#[derive(Clone)]
pub struct SandboxNamespace {
    /// The operations of Sandbox's observed, a Reconcile (D103).
    pub observed: SandboxObservedOperations,
    /// The operations of Sandbox's status, a Workflow (D103).
    pub status: SandboxStatusOperations,
}

impl SandboxNamespace {
    pub(crate) fn new(
        client: HttpClient,
    ) -> Self {
        Self {
            observed: SandboxObservedOperations::new(client.clone()),
            status: SandboxStatusOperations::new(client),
        }
    }
}

/// The operations of Sandbox's observed, a Reconcile (D103).
#[derive(Clone)]
pub struct SandboxObservedOperations {
    client: HttpClient,
}

impl SandboxObservedOperations {
    pub(crate) fn new(client: HttpClient) -> Self {
        Self { client }
    }
    /// Records the state the provider reports, when at is newer than the last observation's, and moves the Workflow there along the fewest transitions its config allows, each as the caller, so every guard and permission holds each step; a step refused stops there and records drift. An observation no newer is answered unchanged. Needs observePermission.
    ///
    /// Requires authentication.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn observe(
        &self,
        id: types::IdentityUUID,
        input: types::SandboxObservedObserveInput,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<types::SandboxObservedObserveResult, SDKError> {
        let path = &format!("/api/sandbox/{}/observed/observe", runtime::path_segment(&id));
        let input_value = serde_json::to_value(&input)?;
        runtime::validate_input("SandboxObservedObserveInput", &input_value)?;
        let query_params: Vec<(String, String)> = Vec::new();
        let body = Some(input_value);
        self.client
            .request_json("POST", path, &query_params, body, options)
            .await
    }
}

/// The operations of Sandbox's status, a Workflow (D103).
#[derive(Clone)]
pub struct SandboxStatusOperations {
    client: HttpClient,
}

impl SandboxStatusOperations {
    pub(crate) fn new(client: HttpClient) -> Self {
        Self { client }
    }
    /// Moves the instance to another state along a transition the config lists. A transition that names a permission needs a caller who holds it.
    ///
    /// Requires authentication.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn transition(
        &self,
        id: types::IdentityUUID,
        input: types::SandboxStatusTransitionInput,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<types::SandboxStatusTransitionResult, SDKError> {
        let path = &format!("/api/sandbox/{}/status/transition", runtime::path_segment(&id));
        let input_value = serde_json::to_value(&input)?;
        runtime::validate_input("SandboxStatusTransitionInput", &input_value)?;
        let query_params: Vec<(String, String)> = Vec::new();
        let body = Some(input_value);
        self.client
            .request_json("POST", path, &query_params, body, options)
            .await
    }
}
