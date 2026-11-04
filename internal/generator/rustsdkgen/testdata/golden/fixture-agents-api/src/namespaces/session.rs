#[allow(unused_imports)]
use serde::Serialize;


use crate::client::HttpClient;
use crate::errors::SDKError;
use crate::runtime;
use crate::types;

#[derive(Clone)]
pub struct SessionNamespace {
    /// The operations of the Presence Session implements (D103).
    pub presence: SessionPresenceOperations,
    /// The operations of Session's status, a Workflow (D103).
    pub status: SessionStatusOperations,
}

impl SessionNamespace {
    pub(crate) fn new(
        client: HttpClient,
    ) -> Self {
        Self {
            presence: SessionPresenceOperations::new(client.clone()),
            status: SessionStatusOperations::new(client),
        }
    }
}

/// The operations of the Presence Session implements (D103).
#[derive(Clone)]
pub struct SessionPresenceOperations {
    client: HttpClient,
}

impl SessionPresenceOperations {
    pub(crate) fn new(client: HttpClient) -> Self {
        Self { client }
    }
    /// Moves the deadline to ttlMs from now and clears missed, and onBeat moves the status when it is one of its from states. Only the principal whose subject the instance's principalField holds.
    ///
    /// Requires authentication.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn beat(
        &self,
        id: types::IdentityUUID,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<types::SessionPresenceBeatResult, SDKError> {
        let path = &format!("/api/session/{}/presence/beat", runtime::path_segment(&id));
        let query_params: Vec<(String, String)> = Vec::new();
        self.client
            .request_json("POST", path, &query_params, None, options)
            .await
    }
    /// Applies the miss of an instance past its deadline and not missed yet: sets missed, moves the status with onMissed when it is one of its from states, and expires the principal's leases on the releaseLeases schemas that were not renewed after its last beat, recording which in released. Any principal who may write the instance may call it; on any other instance it changes nothing. The engine's runner calls it every sweepMs.
    ///
    /// Requires authentication.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn miss(
        &self,
        id: types::IdentityUUID,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<types::SessionPresenceMissResult, SDKError> {
        let path = &format!("/api/session/{}/presence/miss", runtime::path_segment(&id));
        let query_params: Vec<(String, String)> = Vec::new();
        self.client
            .request_json("POST", path, &query_params, None, options)
            .await
    }
}

/// The operations of Session's status, a Workflow (D103).
#[derive(Clone)]
pub struct SessionStatusOperations {
    client: HttpClient,
}

impl SessionStatusOperations {
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
        input: types::SessionStatusTransitionInput,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<types::SessionStatusTransitionResult, SDKError> {
        let path = &format!("/api/session/{}/status/transition", runtime::path_segment(&id));
        let input_value = serde_json::to_value(&input)?;
        runtime::validate_input("SessionStatusTransitionInput", &input_value)?;
        let query_params: Vec<(String, String)> = Vec::new();
        let body = Some(input_value);
        self.client
            .request_json("POST", path, &query_params, body, options)
            .await
    }
}
