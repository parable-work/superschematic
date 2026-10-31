#[allow(unused_imports)]
use serde::Serialize;


use crate::client::HttpClient;
use crate::errors::SDKError;
use crate::runtime;
use crate::types;

#[derive(Clone)]
pub struct OrderNamespace {
    client: HttpClient,
    /// The operations of Order's status, a Workflow (D103).
    pub status: OrderStatusOperations,
}

impl OrderNamespace {
    pub(crate) fn new(
        client: HttpClient,
    ) -> Self {
        Self {
            status: OrderStatusOperations::new(client.clone()),
            client,
        }
    }
    /// get_order endpoint.
    ///
    /// Requires authentication.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn get_order(
        &self,
        id: types::IdentityUUID,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<types::Order, SDKError> {
        let path = &format!("/api/orders/{}", runtime::path_segment(&id));
        let query_params: Vec<(String, String)> = Vec::new();
        self.client
            .request_json("GET", path, &query_params, None, options)
            .await
    }
}

/// The operations of Order's status, a Workflow (D103).
#[derive(Clone)]
pub struct OrderStatusOperations {
    client: HttpClient,
}

impl OrderStatusOperations {
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
        input: types::OrderStatusTransitionInput,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<types::OrderStatusTransitionResult, SDKError> {
        let path = &format!("/api/order/{}/status/transition", runtime::path_segment(&id));
        let input_value = serde_json::to_value(&input)?;
        runtime::validate_input("OrderStatusTransitionInput", &input_value)?;
        let query_params: Vec<(String, String)> = Vec::new();
        let body = Some(input_value);
        self.client
            .request_json("POST", path, &query_params, body, options)
            .await
    }
}
