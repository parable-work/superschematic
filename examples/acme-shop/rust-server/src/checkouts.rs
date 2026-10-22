//! shop-orders' checkouts and acmepay's webhook, which this server does not
//! serve: a checkout holds stock and a pickup slot in shop-db's engine and
//! takes payment through acmepay (D70), which only the shop's stack runs,
//! with shop-orders' Go server. Here each answers 501, and the webhook's
//! verifier refuses every request, as no signing secret is set.

use acme_shop_orders_api::{
    types, CheckoutsGetCheckoutArgs, CheckoutsImplementation, CheckoutsPayCheckoutArgs,
    CheckoutsRequestRefundArgs, CheckoutsStartCheckoutArgs, PaymentWebhooksImplementation,
    PaymentWebhooksPaymentEventsArgs, WebhookVerifier,
};
use async_trait::async_trait;
use axum::extract::Request;
use axum::http::StatusCode;
use axum::middleware::Next;
use axum::response::{IntoResponse, Response};
use superschematic_http_runtime::{ApiError, RequestContext};

/// The checkouts and the webhook, unserved.
pub struct NoCheckouts;

fn unserved() -> ApiError {
    ApiError::not_implemented(
        "checkouts are served by shop-orders' Go server in the shop's stack, over shop-db's engine",
    )
}

#[async_trait]
impl CheckoutsImplementation for NoCheckouts {
    async fn get_checkout(
        &self,
        _ctx: RequestContext,
        _args: CheckoutsGetCheckoutArgs,
    ) -> Result<types::CheckoutView, ApiError> {
        Err(unserved())
    }

    async fn pay_checkout(
        &self,
        _ctx: RequestContext,
        _args: CheckoutsPayCheckoutArgs,
    ) -> Result<types::CheckoutPayment, ApiError> {
        Err(unserved())
    }

    async fn request_refund(
        &self,
        _ctx: RequestContext,
        _args: CheckoutsRequestRefundArgs,
    ) -> Result<types::RefundView, ApiError> {
        Err(unserved())
    }

    async fn start_checkout(
        &self,
        _ctx: RequestContext,
        _args: CheckoutsStartCheckoutArgs,
    ) -> Result<types::CheckoutView, ApiError> {
        Err(unserved())
    }
}

#[async_trait]
impl PaymentWebhooksImplementation for NoCheckouts {
    async fn payment_events(
        &self,
        _ctx: RequestContext,
        _args: PaymentWebhooksPaymentEventsArgs,
    ) -> Result<types::PaymentEventReceipt, ApiError> {
        Err(unserved())
    }
}

#[async_trait]
impl WebhookVerifier for NoCheckouts {
    /// No signing secret is set here, so no signature verifies.
    async fn verify(&self, _request: Request, _next: Next) -> Response {
        (
            StatusCode::UNAUTHORIZED,
            "acmepay's webhook is not served here",
        )
            .into_response()
    }
}
