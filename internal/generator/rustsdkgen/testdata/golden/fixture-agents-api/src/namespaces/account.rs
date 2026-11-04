#[allow(unused_imports)]
use serde::Serialize;


use crate::client::HttpClient;
use crate::errors::SDKError;
use crate::runtime;
use crate::types;

#[derive(Clone)]
pub struct AccountNamespace {
    /// The operations of the Budget Account implements (D103).
    pub budget: AccountBudgetOperations,
    /// The operations of Account's status, a Workflow (D103).
    pub status: AccountStatusOperations,
}

impl AccountNamespace {
    pub(crate) fn new(
        client: HttpClient,
    ) -> Self {
        Self {
            budget: AccountBudgetOperations::new(client.clone()),
            status: AccountStatusOperations::new(client),
        }
    }
}

/// The operations of the Budget Account implements (D103).
#[derive(Clone)]
pub struct AccountBudgetOperations {
    client: HttpClient,
}

impl AccountBudgetOperations {
    pub(crate) fn new(client: HttpClient) -> Self {
        Self { client }
    }
    /// Whether reserve with the same parameters would fit now, here and in every enclosing scope, after the reservations of a lease that is no longer active are settled, as reserve settles them; it changes nothing and does not ask for a lease. Queue copies its answer so that a claim that cannot fit is not tried.
    ///
    /// Requires authentication.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn check_reserve(
        &self,
        id: types::IdentityUUID,
        input: types::AccountBudgetCheckReserveInput,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<types::AccountBudgetCheckReserveResult, SDKError> {
        let path = &format!("/api/account/{}/budget/check-reserve", runtime::path_segment(&id));
        let input_value = serde_json::to_value(&input)?;
        runtime::validate_input("AccountBudgetCheckReserveInput", &input_value)?;
        let query_params: Vec<(String, String)> = Vec::new();
        let body = Some(input_value);
        self.client
            .request_json("POST", path, &query_params, body, options)
            .await
    }
    /// Records the usage of a meter a provider reports, in place of what the meter counts until resetsAt (until the meter's next reset without it, and until the next observe for a meter with no reset): the meter's used is the figure given, usage recorded after it adds to it, and limit, when given, is the meter's limit while it holds. Nothing passes up to an enclosing scope. Needs observePermission. A table's alone: an engine type refuses it.
    ///
    /// Requires authentication.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn observe(
        &self,
        id: types::IdentityUUID,
        input: types::AccountBudgetObserveInput,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<types::AccountBudgetObserveResult, SDKError> {
        let path = &format!("/api/account/{}/budget/observe", runtime::path_segment(&id));
        let input_value = serde_json::to_value(&input)?;
        runtime::validate_input("AccountBudgetObserveInput", &input_value)?;
        let query_params: Vec<(String, String)> = Vec::new();
        let body = Some(input_value);
        self.client
            .request_json("POST", path, &query_params, body, options)
            .await
    }
    /// Records usage of a meter, which is never refused. The part of it the instance's own reservation covers is released, here and in every enclosing scope, and the usage counts in each of them. Reports every instance of the chain whose usage is over its limit after it, moves the status of each whose config gives escalate, and sends onExceeded's directive to the holder of the active lease once per meter per lease.
    ///
    /// Requires authentication.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn record_usage(
        &self,
        id: types::IdentityUUID,
        input: types::AccountBudgetRecordUsageInput,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<types::AccountBudgetRecordUsageResult, SDKError> {
        let path = &format!("/api/account/{}/budget/record-usage", runtime::path_segment(&id));
        let input_value = serde_json::to_value(&input)?;
        runtime::validate_input("AccountBudgetRecordUsageInput", &input_value)?;
        let query_params: Vec<(String, String)> = Vec::new();
        let body = Some(input_value);
        self.client
            .request_json("POST", path, &query_params, body, options)
            .await
    }
    /// Counts usage of a meter by an instance whose scope link points here, releases the part of what this instance holds for it that its own reservation covered, never more than that one no longer holds, and passes both on up this instance's own scope. Never refused for the amount.
    ///
    /// Requires authentication.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn record_usage_for(
        &self,
        id: types::IdentityUUID,
        input: types::AccountBudgetRecordUsageForInput,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<types::AccountBudgetRecordUsageForResult, SDKError> {
        let path = &format!("/api/account/{}/budget/record-usage-for", runtime::path_segment(&id));
        let input_value = serde_json::to_value(&input)?;
        runtime::validate_input("AccountBudgetRecordUsageForInput", &input_value)?;
        let query_params: Vec<(String, String)> = Vec::new();
        let body = Some(input_value);
        self.client
            .request_json("POST", path, &query_params, body, options)
            .await
    }
    /// Reserves an amount of one meter, or of every meter the config gives a reservation, for the instance and every enclosing scope. Refused when the instance or an enclosing scope cannot fit it, and then nothing is reserved anywhere. On a type that composes Lease it needs an active lease, and the reservation is settled when that lease ends.
    ///
    /// Requires authentication.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn reserve(
        &self,
        id: types::IdentityUUID,
        input: types::AccountBudgetReserveInput,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<types::AccountBudgetReserveResult, SDKError> {
        let path = &format!("/api/account/{}/budget/reserve", runtime::path_segment(&id));
        let input_value = serde_json::to_value(&input)?;
        runtime::validate_input("AccountBudgetReserveInput", &input_value)?;
        let query_params: Vec<(String, String)> = Vec::new();
        let body = Some(input_value);
        self.client
            .request_json("POST", path, &query_params, body, options)
            .await
    }
    /// Holds an amount of a meter for an instance whose scope link points here, as that instance's reserve does, and passes it on up this instance's own scope. Refused when the amount does not fit, when that instance does not draw the meter from here, and beyond what it has reserved and this instance does not hold for it yet.
    ///
    /// Requires authentication.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn reserve_for(
        &self,
        id: types::IdentityUUID,
        input: types::AccountBudgetReserveForInput,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<types::AccountBudgetReserveForResult, SDKError> {
        let path = &format!("/api/account/{}/budget/reserve-for", runtime::path_segment(&id));
        let input_value = serde_json::to_value(&input)?;
        runtime::validate_input("AccountBudgetReserveForInput", &input_value)?;
        let query_params: Vec<(String, String)> = Vec::new();
        let body = Some(input_value);
        self.client
            .request_json("POST", path, &query_params, body, options)
            .await
    }
    /// Sets a meter's limit on the instance, raising or lowering it; through limitField when the meter has one. Needs limitPermission, and is refused when the config names none, and below what the instance has used and reserved.
    ///
    /// Requires authentication.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn set_limit(
        &self,
        id: types::IdentityUUID,
        input: types::AccountBudgetSetLimitInput,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<types::AccountBudgetSetLimitResult, SDKError> {
        let path = &format!("/api/account/{}/budget/set-limit", runtime::path_segment(&id));
        let input_value = serde_json::to_value(&input)?;
        runtime::validate_input("AccountBudgetSetLimitInput", &input_value)?;
        let query_params: Vec<(String, String)> = Vec::new();
        let body = Some(input_value);
        self.client
            .request_json("POST", path, &query_params, body, options)
            .await
    }
    /// Releases the instance's own reservation of one meter, or of every meter, here and in every enclosing scope.
    ///
    /// Requires authentication.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn settle(
        &self,
        id: types::IdentityUUID,
        input: types::AccountBudgetSettleInput,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<types::AccountBudgetSettleResult, SDKError> {
        let path = &format!("/api/account/{}/budget/settle", runtime::path_segment(&id));
        let input_value = serde_json::to_value(&input)?;
        runtime::validate_input("AccountBudgetSettleInput", &input_value)?;
        let query_params: Vec<(String, String)> = Vec::new();
        let body = Some(input_value);
        self.client
            .request_json("POST", path, &query_params, body, options)
            .await
    }
    /// Releases up to an amount of what this instance holds of a meter for another, never more than that one no longer holds itself, and passes the release on up this instance's own scope.
    ///
    /// Requires authentication.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn settle_for(
        &self,
        id: types::IdentityUUID,
        input: types::AccountBudgetSettleForInput,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<types::AccountBudgetSettleForResult, SDKError> {
        let path = &format!("/api/account/{}/budget/settle-for", runtime::path_segment(&id));
        let input_value = serde_json::to_value(&input)?;
        runtime::validate_input("AccountBudgetSettleForInput", &input_value)?;
        let query_params: Vec<(String, String)> = Vec::new();
        let body = Some(input_value);
        self.client
            .request_json("POST", path, &query_params, body, options)
            .await
    }
}

/// The operations of Account's status, a Workflow (D103).
#[derive(Clone)]
pub struct AccountStatusOperations {
    client: HttpClient,
}

impl AccountStatusOperations {
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
        input: types::AccountStatusTransitionInput,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<types::AccountStatusTransitionResult, SDKError> {
        let path = &format!("/api/account/{}/status/transition", runtime::path_segment(&id));
        let input_value = serde_json::to_value(&input)?;
        runtime::validate_input("AccountStatusTransitionInput", &input_value)?;
        let query_params: Vec<(String, String)> = Vec::new();
        let body = Some(input_value);
        self.client
            .request_json("POST", path, &query_params, body, options)
            .await
    }
}
