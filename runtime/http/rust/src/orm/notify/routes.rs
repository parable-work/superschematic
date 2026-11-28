//! The notifier's routes (D153, D191, #620): the in-app inbox a caller
//! reads and acknowledges its own notices in, and the routes a channel
//! adapter's relay posts what people reply and how each message fared to.
//! The routes the TypeScript example server (`examples/notifications`)
//! writes by hand, which a generated Rust server mounts.
//!
//! - `GET {inbox}?state&limit`: the caller's notices, newest first, as
//!   `{ data: [...] }`; `state` is a comma list of states, every one when
//!   unset.
//! - `POST {inbox}/{id}/read`, `POST {inbox}/{id}/ack`: the caller's own
//!   notice read or acknowledged; 204, 404 for one that is not the
//!   caller's.
//! - `POST {webhooks}/{slot}/inbound`: the replies an adapter's request
//!   carries, each run as its sender (`Notifier::inbound`), or, with the
//!   inbound path, each down D157's path (`Inbound::inbound`), which hands
//!   a user's reply to the notifier: `{ data: [{ outcome, ... }] }`.
//! - `POST {webhooks}/{slot}/status`: the delivery statuses it carries
//!   (`Notifier::statuses`): `{ data: { moved } }`.
//!
//! `{slot}` is the channel slot in lower case, `-` for `_`: `sms` is the
//! slot `SMS`. The adapter checks the relay's signature; a request it
//! refuses is answered 400, and a slot that is no channel of the
//! environment 404.

use std::collections::BTreeMap;
use std::sync::Arc;

use axum::body::to_bytes;
use axum::extract::{Request, State};
use axum::http::{header, Method, StatusCode};
use axum::response::{IntoResponse, Response};
use axum::routing::any;
use axum::Router;
use serde_json::{json, Value};

use super::notifier::{Notifier, NotifierError, RecipientRef, ReplyOutcome};
use crate::auth::{Authenticator, Principal, PrincipalKind};
use crate::orm::inbound::{Inbound, InboundError, InboundOutcome};
use crate::providers::WebhookRequest;
use crate::response::error_response;
use crate::ApiError;

/// The most bytes a relay's request may carry.
const MAX_WEBHOOK_BODY_BYTES: usize = 1 << 20;

/// What [`Notifier::routes`] serves from.
#[derive(Clone, Default)]
pub struct NotifierRoutes {
    /// Establishes the inbox's caller: the server's authenticator. Without
    /// one there is no inbox route, only the relays'.
    pub authenticator: Option<Arc<dyn Authenticator>>,
    /// D157's inbound path, which a relay's messages take when set: a
    /// party's message becomes a row of its conversation, and a user's
    /// reply goes to the notifier.
    pub inbound: Option<Inbound>,
    /// The inbox's path; `/api/inbox` unless set.
    pub inbox: Option<String>,
    /// The path the relays' routes are under; `/webhooks` unless set.
    pub webhooks: Option<String>,
}

struct Served {
    notifier: Notifier,
    routes: NotifierRoutes,
}

fn base(path: Option<&str>, default: &str) -> String {
    path.unwrap_or(default).trim_end_matches('/').to_owned()
}

impl Notifier {
    /// The notifier's routes, as a router the app merges into its own: the
    /// inbox with an authenticator, and the relays' routes.
    pub fn routes(&self, routes: NotifierRoutes) -> Router {
        let inbox = base(routes.inbox.as_deref(), "/api/inbox");
        let webhooks = base(routes.webhooks.as_deref(), "/webhooks");
        let has_inbox = routes.authenticator.is_some();
        let state = Arc::new(Served {
            notifier: self.clone(),
            routes,
        });
        let mut router = Router::new()
            .route(&format!("{webhooks}/{{slot}}/inbound"), any(relay_inbound))
            .route(&format!("{webhooks}/{{slot}}/status"), any(relay_status));
        if has_inbox {
            router = router
                .route(&inbox, any(list))
                .route(&format!("{inbox}/{{id}}/{{verb}}"), any(decide));
        }
        router.with_state(state)
    }
}

fn data(status: StatusCode, body: Value) -> Response {
    (
        status,
        [(header::CONTENT_TYPE, "application/json")],
        json!({ "data": body }).to_string(),
    )
        .into_response()
}

fn notifier_error(err: NotifierError) -> ApiError {
    match err {
        NotifierError::Refused(message) => ApiError::not_found(message),
        other => ApiError::internal(other.to_string()),
    }
}

fn inbound_error(err: InboundError) -> ApiError {
    match err {
        InboundError::Refused(message) => ApiError::not_found(message),
        InboundError::Notifier(err) => notifier_error(err),
        other => ApiError::internal(other.to_string()),
    }
}

fn recipient_of(principal: &Principal) -> RecipientRef {
    let kind = match principal.kind {
        PrincipalKind::Party => "party",
        PrincipalKind::Agent => "agent",
        _ => "user",
    };
    RecipientRef {
        kind: kind.to_owned(),
        id: principal.subject.clone(),
        table: None,
    }
}

async fn caller(served: &Served, parts: &http::request::Parts) -> Result<Principal, ApiError> {
    let Some(authenticator) = &served.routes.authenticator else {
        return Err(ApiError::not_found("no inbox"));
    };
    authenticator
        .authenticate(parts)
        .await?
        .ok_or_else(|| ApiError::unauthorized("Authentication required"))
}

async fn list(State(served): State<Arc<Served>>, request: Request) -> Response {
    let answered = async {
        let (parts, _) = request.into_parts();
        if parts.method != Method::GET {
            return Err(ApiError::method_not_allowed("the inbox is a GET"));
        }
        let principal = caller(&served, &parts).await?;
        let mut states = Vec::new();
        let mut limit = None;
        for (name, value) in parts
            .uri
            .query()
            .unwrap_or_default()
            .split('&')
            .filter_map(|pair| pair.split_once('='))
        {
            match name {
                "state" => states.extend(value.split(',').filter(|s| !s.is_empty())),
                "limit" => {
                    limit = Some(value.parse::<u32>().map_err(|_| {
                        ApiError::bad_request(format!("limit is {value:?}, which is no count"))
                    })?);
                }
                _ => {}
            }
        }
        let notices = served
            .notifier
            .inbox(&recipient_of(&principal), &states, limit)
            .await
            .map_err(notifier_error)?;
        Ok(data(StatusCode::OK, json!(notices)))
    };
    answered.await.unwrap_or_else(error_response)
}

async fn decide(State(served): State<Arc<Served>>, request: Request) -> Response {
    let answered = async {
        let (parts, _) = request.into_parts();
        if parts.method != Method::POST {
            return Err(ApiError::method_not_allowed(
                "a notice is read or acknowledged by a POST",
            ));
        }
        let principal = caller(&served, &parts).await?;
        let path = parts.uri.path().trim_end_matches('/');
        let mut segments = path.rsplit('/');
        let (verb, id) = (
            segments.next().unwrap_or_default(),
            segments.next().unwrap_or_default(),
        );
        let recipient = recipient_of(&principal);
        match verb {
            "read" => served.notifier.mark_read(&recipient, id).await,
            "ack" => served.notifier.acknowledge(&recipient, id, None).await,
            _ => {
                return Err(ApiError::not_found(format!(
                    "a notice is read or acknowledged, not {verb:?}"
                )))
            }
        }
        .map_err(notifier_error)?;
        Ok(StatusCode::NO_CONTENT.into_response())
    };
    answered.await.unwrap_or_else(error_response)
}

/// The slot a route's `{slot}` names: `sms` is `SMS`, `chat-bridge`
/// `CHAT_BRIDGE`.
fn slot_of(path: &str, rest: &str) -> String {
    path.trim_end_matches(rest)
        .rsplit('/')
        .next()
        .unwrap_or_default()
        .to_uppercase()
        .replace('-', "_")
}

async fn webhook(request: Request, rest: &str) -> Result<(String, WebhookRequest), ApiError> {
    let (parts, body) = request.into_parts();
    if parts.method != Method::POST {
        return Err(ApiError::method_not_allowed("a relay posts"));
    }
    let slot = slot_of(parts.uri.path().trim_end_matches('/'), rest);
    let body = to_bytes(body, MAX_WEBHOOK_BODY_BYTES)
        .await
        .map_err(|_| ApiError::payload_too_large("the relay's request is too large"))?;
    let mut headers = BTreeMap::new();
    for (name, value) in &parts.headers {
        if let Ok(value) = value.to_str() {
            headers.insert(name.as_str().to_ascii_lowercase(), value.to_owned());
        }
    }
    Ok((
        slot,
        WebhookRequest {
            headers,
            body: String::from_utf8_lossy(&body).into_owned(),
        },
    ))
}

fn reply_json(reply: &ReplyOutcome) -> Value {
    json!({
        "outcome": reply.outcome,
        "notice": reply.notice,
        "verb": reply.verb,
        "detail": reply.detail,
    })
}

fn inbound_json(outcome: &InboundOutcome) -> Value {
    json!({
        "outcome": outcome.outcome,
        "message": outcome.message,
        "conversation": outcome.conversation,
        "reply": outcome.reply.as_ref().map(reply_json),
        "detail": outcome.detail,
    })
}

async fn relay_inbound(State(served): State<Arc<Served>>, request: Request) -> Response {
    let answered = async {
        let (slot, request) = webhook(request, "/inbound").await?;
        let outcomes: Vec<Value> = match &served.routes.inbound {
            Some(inbound) => inbound
                .inbound(&slot, &request)
                .await
                .map_err(inbound_error)?
                .iter()
                .map(inbound_json)
                .collect(),
            None => served
                .notifier
                .inbound(&slot, &request)
                .await
                .map_err(notifier_error)?
                .iter()
                .map(reply_json)
                .collect(),
        };
        Ok(data(StatusCode::OK, json!(outcomes)))
    };
    answered.await.unwrap_or_else(error_response)
}

async fn relay_status(State(served): State<Arc<Served>>, request: Request) -> Response {
    let answered = async {
        let (slot, request) = webhook(request, "/status").await?;
        let moved = served
            .notifier
            .statuses(&slot, &request)
            .await
            .map_err(notifier_error)?;
        Ok(data(StatusCode::OK, json!({ "moved": moved })))
    };
    answered.await.unwrap_or_else(error_response)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_slot_is_its_path_segment_in_upper_case() {
        assert_eq!(slot_of("/webhooks/sms/inbound", "/inbound"), "SMS");
        assert_eq!(
            slot_of("/webhooks/chat-bridge/status", "/status"),
            "CHAT_BRIDGE"
        );
    }

    #[test]
    fn a_principal_is_the_recipient_of_its_kind() {
        let user = Principal::new("u1", Vec::<String>::new());
        assert_eq!(recipient_of(&user), RecipientRef::user("u1"));
        let party = Principal::new("p1", Vec::<String>::new()).of_kind(PrincipalKind::Party);
        assert_eq!(recipient_of(&party).kind, "party");
    }
}
