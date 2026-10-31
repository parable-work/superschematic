#[allow(unused_imports)]
use serde::Serialize;


use crate::client::HttpClient;
use crate::errors::SDKError;
use crate::runtime;
use crate::types;

#[derive(Clone)]
pub struct ShipmentNamespace {
    /// The operations of Shipment's paymentStatus, a Workflow (D103).
    pub payment_status: ShipmentPaymentStatusOperations,
    /// The operations of Shipment's status, a Workflow (D103).
    pub status: ShipmentStatusOperations,
}

impl ShipmentNamespace {
    pub(crate) fn new(
        client: HttpClient,
    ) -> Self {
        Self {
            payment_status: ShipmentPaymentStatusOperations::new(client.clone()),
            status: ShipmentStatusOperations::new(client),
        }
    }
}

/// The operations of Shipment's paymentStatus, a Workflow (D103).
#[derive(Clone)]
pub struct ShipmentPaymentStatusOperations {
    client: HttpClient,
}

impl ShipmentPaymentStatusOperations {
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
        input: types::ShipmentPaymentStatusTransitionInput,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<types::ShipmentPaymentStatusTransitionResult, SDKError> {
        let path = &format!("/api/shipment/{}/payment-status/transition", runtime::path_segment(&id));
        let input_value = serde_json::to_value(&input)?;
        runtime::validate_input("ShipmentPaymentStatusTransitionInput", &input_value)?;
        let query_params: Vec<(String, String)> = Vec::new();
        let body = Some(input_value);
        self.client
            .request_json("POST", path, &query_params, body, options)
            .await
    }
}

/// The operations of Shipment's status, a Workflow (D103).
#[derive(Clone)]
pub struct ShipmentStatusOperations {
    client: HttpClient,
}

impl ShipmentStatusOperations {
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
        input: types::ShipmentStatusTransitionInput,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<types::ShipmentStatusTransitionResult, SDKError> {
        let path = &format!("/api/shipment/{}/status/transition", runtime::path_segment(&id));
        let input_value = serde_json::to_value(&input)?;
        runtime::validate_input("ShipmentStatusTransitionInput", &input_value)?;
        let query_params: Vec<(String, String)> = Vec::new();
        let body = Some(input_value);
        self.client
            .request_json("POST", path, &query_params, body, options)
            .await
    }
}
