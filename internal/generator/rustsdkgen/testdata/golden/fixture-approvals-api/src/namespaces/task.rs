#[allow(unused_imports)]
use serde::Serialize;


use crate::client::HttpClient;
use crate::errors::SDKError;
use crate::runtime;
use crate::types;

#[derive(Clone)]
pub struct TaskNamespace {
    /// The operations of the Approvals Task implements (D103).
    pub approvals: TaskApprovalsOperations,
    /// The operations of Task's status, a Workflow (D103).
    pub status: TaskStatusOperations,
}

impl TaskNamespace {
    pub(crate) fn new(
        client: HttpClient,
    ) -> Self {
        Self {
            approvals: TaskApprovalsOperations::new(client.clone()),
            status: TaskStatusOperations::new(client),
        }
    }
}

/// The operations of the Approvals Task implements (D103).
#[derive(Clone)]
pub struct TaskApprovalsOperations {
    client: HttpClient,
}

impl TaskApprovalsOperations {
    pub(crate) fn new(client: HttpClient) -> Self {
        Self { client }
    }
    /// Lists the instance's sign-offs, oldest first, one page at a time, the consumed ones included; with state, only those for it.
    ///
    /// Requires authentication.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn list_sign_offs(
        &self,
        id: types::IdentityUUID,
        input: types::TaskApprovalsListSignOffsInput,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<types::TaskApprovalsListSignOffsResult, SDKError> {
        let path = &format!("/api/task/{}/approvals/list-sign-offs", runtime::path_segment(&id));
        let input_value = serde_json::to_value(&input)?;
        runtime::validate_input("TaskApprovalsListSignOffsInput", &input_value)?;
        let query_params: Vec<(String, String)> = Vec::new();
        let body = Some(input_value);
        self.client
            .request_json("POST", path, &query_params, body, options)
            .await
    }
    /// Signs off on the instance for a gated state, as the caller. Needs the gate's permission; refused for the instance's creator unless the gate allows it, while the instance is in the state, and for a caller whose sign-off for the state is not yet consumed.
    ///
    /// Requires authentication.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn sign_off(
        &self,
        id: types::IdentityUUID,
        input: types::TaskApprovalsSignOffInput,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<types::TaskApprovalsSignOffResult, SDKError> {
        let path = &format!("/api/task/{}/approvals/sign-off", runtime::path_segment(&id));
        let input_value = serde_json::to_value(&input)?;
        runtime::validate_input("TaskApprovalsSignOffInput", &input_value)?;
        let query_params: Vec<(String, String)> = Vec::new();
        let body = Some(input_value);
        self.client
            .request_json("POST", path, &query_params, body, options)
            .await
    }
    /// Withdraws the caller's sign-off for a gated state, one not yet consumed.
    ///
    /// Requires authentication.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn withdraw_sign_off(
        &self,
        id: types::IdentityUUID,
        input: types::TaskApprovalsWithdrawSignOffInput,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<types::TaskApprovalsWithdrawSignOffResult, SDKError> {
        let path = &format!("/api/task/{}/approvals/withdraw-sign-off", runtime::path_segment(&id));
        let input_value = serde_json::to_value(&input)?;
        runtime::validate_input("TaskApprovalsWithdrawSignOffInput", &input_value)?;
        let query_params: Vec<(String, String)> = Vec::new();
        let body = Some(input_value);
        self.client
            .request_json("POST", path, &query_params, body, options)
            .await
    }
}

/// The operations of Task's status, a Workflow (D103).
#[derive(Clone)]
pub struct TaskStatusOperations {
    client: HttpClient,
}

impl TaskStatusOperations {
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
        input: types::TaskStatusTransitionInput,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<types::TaskStatusTransitionResult, SDKError> {
        let path = &format!("/api/task/{}/status/transition", runtime::path_segment(&id));
        let input_value = serde_json::to_value(&input)?;
        runtime::validate_input("TaskStatusTransitionInput", &input_value)?;
        let query_params: Vec<(String, String)> = Vec::new();
        let body = Some(input_value);
        self.client
            .request_json("POST", path, &query_params, body, options)
            .await
    }
}
