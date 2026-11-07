#[allow(unused_imports)]
use serde::Serialize;


use crate::client::HttpClient;
use crate::errors::SDKError;
use crate::runtime;
use crate::types;

#[derive(Clone)]
pub struct JobNamespace {
    /// The operations of the Lease Job implements (D103).
    pub lease: JobLeaseOperations,
    /// The operations of the Retries Job implements (D103).
    pub retries: JobRetriesOperations,
    /// The operations of Job's status, a Workflow (D103).
    pub status: JobStatusOperations,
}

impl JobNamespace {
    pub(crate) fn new(
        client: HttpClient,
    ) -> Self {
        Self {
            lease: JobLeaseOperations::new(client.clone()),
            retries: JobRetriesOperations::new(client.clone()),
            status: JobStatusOperations::new(client),
        }
    }
}

/// The operations of the Lease Job implements (D103).
#[derive(Clone)]
pub struct JobLeaseOperations {
    client: HttpClient,
}

impl JobLeaseOperations {
    pub(crate) fn new(client: HttpClient) -> Self {
        Self { client }
    }
    /// Expires every lease one principal holds on the schema's instances, active or not, as expire with that holder does on each: for a principal that is gone. With notRenewedAfter, an active lease renewed after that time is spared. Needs overridePermission; refused when the config names none.
    ///
    /// Requires authentication.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn expire_holder(
        &self,
        input: types::JobLeaseExpireHolderInput,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<types::JobLeaseExpireHolderResult, SDKError> {
        let path = "/api/job/lease/expire-holder";
        let input_value = serde_json::to_value(&input)?;
        runtime::validate_input("JobLeaseExpireHolderInput", &input_value)?;
        let query_params: Vec<(String, String)> = Vec::new();
        let body = Some(input_value);
        self.client
            .request_json("POST", path, &query_params, body, options)
            .await
    }
    /// Marks directives of the lease as handled, so heartbeats stop delivering them. Only the holder, presenting the current token as Lease's precondition.
    ///
    /// Requires authentication.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn acknowledge(
        &self,
        id: types::IdentityUUID,
        input: types::JobLeaseAcknowledgeInput,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<types::GenericJSON, SDKError> {
        let path = &format!("/api/job/{}/lease/acknowledge", runtime::path_segment(&id));
        let input_value = serde_json::to_value(&input)?;
        runtime::validate_input("JobLeaseAcknowledgeInput", &input_value)?;
        let query_params: Vec<(String, String)> = Vec::new();
        let body = Some(input_value);
        self.client
            .request_json("POST", path, &query_params, body, options)
            .await
    }
    /// Takes the lease for the caller and returns its fencing token. Refused while a lease is active, the caller's own included (heartbeat renews it), without acquirePermission when the config names one, and once the instance has had maxExpiries expiries. Over a lease that expired, it applies that expiry first.
    ///
    /// Requires authentication.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn acquire(
        &self,
        id: types::IdentityUUID,
        input: types::JobLeaseAcquireInput,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<types::JobLeaseAcquireResult, SDKError> {
        let path = &format!("/api/job/{}/lease/acquire", runtime::path_segment(&id));
        let input_value = serde_json::to_value(&input)?;
        runtime::validate_input("JobLeaseAcquireInput", &input_value)?;
        let query_params: Vec<(String, String)> = Vec::new();
        let body = Some(input_value);
        self.client
            .request_json("POST", path, &query_params, body, options)
            .await
    }
    /// Sends the holder of the active lease a directive, which its heartbeats deliver until it acknowledges it. With dedupeKey, a directive already sent under the lease with the key is not sent again. A principal needs directPermission, or overridePermission when that is absent, and is refused when the config names neither; another behavior of the type sends one with call(), as the principal it runs for, without either.
    ///
    /// Requires authentication.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn direct(
        &self,
        id: types::IdentityUUID,
        input: types::JobLeaseDirectInput,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<types::JobLeaseDirectResult, SDKError> {
        let path = &format!("/api/job/{}/lease/direct", runtime::path_segment(&id));
        let input_value = serde_json::to_value(&input)?;
        runtime::validate_input("JobLeaseDirectInput", &input_value)?;
        let query_params: Vec<(String, String)> = Vec::new();
        let body = Some(input_value);
        self.client
            .request_json("POST", path, &query_params, body, options)
            .await
    }
    /// Applies the expiry of a lease past its expiry time or its longest hold: the holder is cleared, the token advances, the expiry is counted unless the instance is in a terminal state of its Workflow, and onExpiry or escalate moves the status. Any principal who may write the instance may call it; on any other instance it changes nothing. With holder, a principal with overridePermission expires that holder's lease at once, active or not, unless notRenewedAfter spares it. The engine's runner calls it on every lapsed lease, every sweepMs.
    ///
    /// Requires authentication.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn expire(
        &self,
        id: types::IdentityUUID,
        input: types::JobLeaseExpireInput,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<types::JobLeaseExpireResult, SDKError> {
        let path = &format!("/api/job/{}/lease/expire", runtime::path_segment(&id));
        let input_value = serde_json::to_value(&input)?;
        runtime::validate_input("JobLeaseExpireInput", &input_value)?;
        let query_params: Vec<(String, String)> = Vec::new();
        let body = Some(input_value);
        self.client
            .request_json("POST", path, &query_params, body, options)
            .await
    }
    /// Renews the lease, never past its longest hold, acknowledges the directives acknowledge names, and returns the directives sent under it that are not acknowledged. Only the holder, presenting the current token as Lease's precondition, while the lease is active: an expired lease cannot be renewed.
    ///
    /// Requires authentication.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn heartbeat(
        &self,
        id: types::IdentityUUID,
        input: types::JobLeaseHeartbeatInput,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<types::JobLeaseHeartbeatResult, SDKError> {
        let path = &format!("/api/job/{}/lease/heartbeat", runtime::path_segment(&id));
        let input_value = serde_json::to_value(&input)?;
        runtime::validate_input("JobLeaseHeartbeatInput", &input_value)?;
        let query_params: Vec<(String, String)> = Vec::new();
        let body = Some(input_value);
        self.client
            .request_json("POST", path, &query_params, body, options)
            .await
    }
    /// Ends an active lease: the holder, presenting the current token as Lease's precondition, or a principal with overridePermission, who need not. The token advances, so the directives sent under the lease are not delivered again, and onExpiry moves the status when it is still one of its from states. With abandon, the holder gives the work up as failed: the release counts as an expiry, and escalate applies at the one that reaches maxExpiries.
    ///
    /// Requires authentication.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn release(
        &self,
        id: types::IdentityUUID,
        input: types::JobLeaseReleaseInput,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<types::GenericJSON, SDKError> {
        let path = &format!("/api/job/{}/lease/release", runtime::path_segment(&id));
        let input_value = serde_json::to_value(&input)?;
        runtime::validate_input("JobLeaseReleaseInput", &input_value)?;
        let query_params: Vec<(String, String)> = Vec::new();
        let body = Some(input_value);
        self.client
            .request_json("POST", path, &query_params, body, options)
            .await
    }
    /// Sets the instance's count of expiries back to 0, so a lease can be acquired again after maxExpiries. Needs overridePermission; refused when the config names none.
    ///
    /// Requires authentication.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn reset_expiries(
        &self,
        id: types::IdentityUUID,
        input: types::JobLeaseResetExpiriesInput,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<types::JobLeaseResetExpiriesResult, SDKError> {
        let path = &format!("/api/job/{}/lease/reset-expiries", runtime::path_segment(&id));
        let input_value = serde_json::to_value(&input)?;
        runtime::validate_input("JobLeaseResetExpiriesInput", &input_value)?;
        let query_params: Vec<(String, String)> = Vec::new();
        let body = Some(input_value);
        self.client
            .request_json("POST", path, &query_params, body, options)
            .await
    }
}

/// The operations of the Retries Job implements (D103).
#[derive(Clone)]
pub struct JobRetriesOperations {
    client: HttpClient,
}

impl JobRetriesOperations {
    pub(crate) fn new(client: HttpClient) -> Self {
        Self { client }
    }
    /// Records an attempt: a success without failure, or a failure of one of the config's classes, counted against its caps, and returns the class's hint. Decides whether its result is kept, and writes a kept result to resultField. Exhausts the instance at a terminal class, a failure past a cap or a stuck signature, moving its status to exhaustedState from the from states. Refused once the instance is exhausted, and without the config's permission.
    ///
    /// Requires authentication.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn record_attempt(
        &self,
        id: types::IdentityUUID,
        input: types::JobRetriesRecordAttemptInput,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<types::JobRetriesRecordAttemptResult, SDKError> {
        let path = &format!("/api/job/{}/retries/record-attempt", runtime::path_segment(&id));
        let input_value = serde_json::to_value(&input)?;
        runtime::validate_input("JobRetriesRecordAttemptInput", &input_value)?;
        let query_params: Vec<(String, String)> = Vec::new();
        let body = Some(input_value);
        self.client
            .request_json("POST", path, &query_params, body, options)
            .await
    }
}

/// The operations of Job's status, a Workflow (D103).
#[derive(Clone)]
pub struct JobStatusOperations {
    client: HttpClient,
}

impl JobStatusOperations {
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
        input: types::JobStatusTransitionInput,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<types::JobStatusTransitionResult, SDKError> {
        let path = &format!("/api/job/{}/status/transition", runtime::path_segment(&id));
        let input_value = serde_json::to_value(&input)?;
        runtime::validate_input("JobStatusTransitionInput", &input_value)?;
        let query_params: Vec<(String, String)> = Vec::new();
        let body = Some(input_value);
        self.client
            .request_json("POST", path, &query_params, body, options)
            .await
    }
}
