#[allow(unused_imports)]
use serde::Serialize;


use crate::client::HttpClient;
use crate::errors::SDKError;
use crate::runtime;
use crate::types;

#[derive(Clone)]
pub struct TicketNamespace {
    /// The operations of Ticket's status, a Workflow (D103).
    pub status: TicketStatusOperations,
    /// The operations of the Timers Ticket implements (D103).
    pub timers: TicketTimersOperations,
}

impl TicketNamespace {
    pub(crate) fn new(
        client: HttpClient,
    ) -> Self {
        Self {
            status: TicketStatusOperations::new(client.clone()),
            timers: TicketTimersOperations::new(client),
        }
    }
}

/// The operations of Ticket's status, a Workflow (D103).
#[derive(Clone)]
pub struct TicketStatusOperations {
    client: HttpClient,
}

impl TicketStatusOperations {
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
        input: types::TicketStatusTransitionInput,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<types::TicketStatusTransitionResult, SDKError> {
        let path = &format!("/api/ticket/{}/status/transition", runtime::path_segment(&id));
        let input_value = serde_json::to_value(&input)?;
        runtime::validate_input("TicketStatusTransitionInput", &input_value)?;
        let query_params: Vec<(String, String)> = Vec::new();
        let body = Some(input_value);
        self.client
            .request_json("POST", path, &query_params, body, options)
            .await
    }
}

/// The operations of the Timers Ticket implements (D103).
#[derive(Clone)]
pub struct TicketTimersOperations {
    client: HttpClient,
}

impl TicketTimersOperations {
    pub(crate) fn new(client: HttpClient) -> Self {
        Self { client }
    }
    /// Fires a deadline that is due and has not fired: marks it fired and moves the status by its then, recording the outcome; a veto or a missing permission the move meets leaves the status as it is and the deadline fired. A deadline whose until holds is cancelled instead. On a deadline not due, fired already or not started it changes nothing. Any principal who may write the instance may call it; the engine's runner calls it on every due deadline, every sweepMs.
    ///
    /// Requires authentication.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn fire_timer(
        &self,
        id: types::IdentityUUID,
        input: types::TicketTimersFireTimerInput,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<types::TicketTimersFireTimerResult, SDKError> {
        let path = &format!("/api/ticket/{}/timers/fire-timer", runtime::path_segment(&id));
        let input_value = serde_json::to_value(&input)?;
        runtime::validate_input("TicketTimersFireTimerInput", &input_value)?;
        let query_params: Vec<(String, String)> = Vec::new();
        let body = Some(input_value);
        self.client
            .request_json("POST", path, &query_params, body, options)
            .await
    }
    /// Starts a deadline again from what it starts from now, the create, the status's entry into its state or its field's value, so it is due afterMs after it and not fired: a snooze or a fire is undone. Needs the config's permission; refused for a deadline with nothing to start from now, or whose until holds.
    ///
    /// Requires authentication.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn reset_timer(
        &self,
        id: types::IdentityUUID,
        input: types::TicketTimersResetTimerInput,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<types::TicketTimersResetTimerResult, SDKError> {
        let path = &format!("/api/ticket/{}/timers/reset-timer", runtime::path_segment(&id));
        let input_value = serde_json::to_value(&input)?;
        runtime::validate_input("TicketTimersResetTimerInput", &input_value)?;
        let query_params: Vec<(String, String)> = Vec::new();
        let body = Some(input_value);
        self.client
            .request_json("POST", path, &query_params, body, options)
            .await
    }
    /// Moves a started deadline's due time to dueAt, and a fired one fires again then. A later write keeps it, until what the deadline starts from changes. Needs the config's permission; refused for a deadline not started, cancelled, or whose until holds.
    ///
    /// Requires authentication.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn snooze_timer(
        &self,
        id: types::IdentityUUID,
        input: types::TicketTimersSnoozeTimerInput,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<types::TicketTimersSnoozeTimerResult, SDKError> {
        let path = &format!("/api/ticket/{}/timers/snooze-timer", runtime::path_segment(&id));
        let input_value = serde_json::to_value(&input)?;
        runtime::validate_input("TicketTimersSnoozeTimerInput", &input_value)?;
        let query_params: Vec<(String, String)> = Vec::new();
        let body = Some(input_value);
        self.client
            .request_json("POST", path, &query_params, body, options)
            .await
    }
}
