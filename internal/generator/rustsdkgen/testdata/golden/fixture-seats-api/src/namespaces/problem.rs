#[allow(unused_imports)]
use serde::Serialize;


use crate::client::HttpClient;
use crate::errors::SDKError;
use crate::runtime;
use crate::types;

#[derive(Clone)]
pub struct ProblemNamespace {
    /// The operations of the Seats Problem implements (D103).
    pub seats: ProblemSeatsOperations,
    /// The operations of Problem's status, a Workflow (D103).
    pub status: ProblemStatusOperations,
}

impl ProblemNamespace {
    pub(crate) fn new(
        client: HttpClient,
    ) -> Self {
        Self {
            seats: ProblemSeatsOperations::new(client.clone()),
            status: ProblemStatusOperations::new(client),
        }
    }
}

/// The operations of the Seats Problem implements (D103).
#[derive(Clone)]
pub struct ProblemSeatsOperations {
    client: HttpClient,
}

impl ProblemSeatsOperations {
    pub(crate) fn new(client: HttpClient) -> Self {
        Self { client }
    }
    /// Unseats a subject from a seat: the caller, which needs no permission, or another, with the seat's assign permission. The subject the seat's fromField holds cannot leave while it holds them. A subject not seated is answered unchanged.
    ///
    /// Requires authentication.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn leave_seat(
        &self,
        id: types::IdentityUUID,
        input: types::ProblemSeatsLeaveSeatInput,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<types::ProblemSeatsLeaveSeatResult, SDKError> {
        let path = &format!("/api/problem/{}/seats/leave-seat", runtime::path_segment(&id));
        let input_value = serde_json::to_value(&input)?;
        runtime::validate_input("ProblemSeatsLeaveSeatInput", &input_value)?;
        let query_params: Vec<(String, String)> = Vec::new();
        let body = Some(input_value);
        self.client
            .request_json("POST", path, &query_params, body, options)
            .await
    }
    /// Lists the subjects seated on the row, by seat in code point order, each seat's fromField subject first and then the others in the order they were seated; with seat, only its subjects.
    ///
    /// Requires authentication.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn list_seats(
        &self,
        id: types::IdentityUUID,
        input: types::ProblemSeatsListSeatsInput,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<types::ProblemSeatsListSeatsResult, SDKError> {
        let path = &format!("/api/problem/{}/seats/list-seats", runtime::path_segment(&id));
        let input_value = serde_json::to_value(&input)?;
        runtime::validate_input("ProblemSeatsListSeatsInput", &input_value)?;
        let query_params: Vec<(String, String)> = Vec::new();
        let body = Some(input_value);
        self.client
            .request_json("POST", path, &query_params, body, options)
            .await
    }
    /// Seats a subject in a seat: the caller, with the seat's take permission when it names one, or another, with its assign permission. A full seat refuses it, or unseats the subject seated longest when its onFull is displace. A subject seated already is answered unchanged.
    ///
    /// Requires authentication.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn take_seat(
        &self,
        id: types::IdentityUUID,
        input: types::ProblemSeatsTakeSeatInput,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<types::ProblemSeatsTakeSeatResult, SDKError> {
        let path = &format!("/api/problem/{}/seats/take-seat", runtime::path_segment(&id));
        let input_value = serde_json::to_value(&input)?;
        runtime::validate_input("ProblemSeatsTakeSeatInput", &input_value)?;
        let query_params: Vec<(String, String)> = Vec::new();
        let body = Some(input_value);
        self.client
            .request_json("POST", path, &query_params, body, options)
            .await
    }
}

/// The operations of Problem's status, a Workflow (D103).
#[derive(Clone)]
pub struct ProblemStatusOperations {
    client: HttpClient,
}

impl ProblemStatusOperations {
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
        input: types::ProblemStatusTransitionInput,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<types::ProblemStatusTransitionResult, SDKError> {
        let path = &format!("/api/problem/{}/status/transition", runtime::path_segment(&id));
        let input_value = serde_json::to_value(&input)?;
        runtime::validate_input("ProblemStatusTransitionInput", &input_value)?;
        let query_params: Vec<(String, String)> = Vec::new();
        let body = Some(input_value);
        self.client
            .request_json("POST", path, &query_params, body, options)
            .await
    }
}
