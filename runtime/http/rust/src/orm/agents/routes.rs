//! The agent routes (D158): what a run's sandbox instance calls, under the
//! endpoint's path. `POST <endpoint>/turn` completes a model turn, `/mcp`
//! serves the run's tools, `/credential` records a credential the sandbox
//! used, and `/finish` the outcome it reports; with an authenticator,
//! `/approvals/<id>/approve` and `/deny` decide an approval as its caller.
//! Each but the approvals' runs as the run's token resolves.

use std::sync::Arc;

use async_trait::async_trait;
use axum::body::{to_bytes, Body};
use axum::extract::{Request, State};
use axum::http::{header, Method, StatusCode};
use axum::response::{IntoResponse, Response};
use axum::routing::any;
use axum::Router;
use serde_json::{json, Map, Value};

use super::dispatcher::{at_text, obj, string, Agents};
use super::tools::refusal_body;
use super::OperationClass;
use crate::auth::{bearer_token, Authenticator, Principal, PrincipalKind};
use crate::mcp::{serve, CallGate, Options, Problem, RouteTool, ServerInfo, MCP_MAX_BODY_BYTES};
use crate::BoxError;

/// What [`Agents::routes`] serves from: the orchestration, and the
/// authenticator of an approval's decider.
#[derive(Clone)]
pub struct AgentRoutes {
    agents: Agents,
    authenticate: Option<Arc<dyn Authenticator>>,
}

fn json_response(status: u16, body: &Value) -> Response {
    let status = StatusCode::from_u16(status).unwrap_or(StatusCode::INTERNAL_SERVER_ERROR);
    (
        status,
        [(header::CONTENT_TYPE, "application/json")],
        body.to_string(),
    )
        .into_response()
}

fn refusal(status: u16, code: &str, detail: &str) -> Response {
    json_response(status, &refusal_body(status, code, detail))
}

impl Agents {
    /// The agent routes, as a router the app merges into its own ahead of
    /// the API's: `POST <endpoint>/turn`, `/mcp`, `/credential`,
    /// `/finish`, and with `authenticate`, `/approvals/<id>/approve` and
    /// `/deny`.
    pub fn routes(&self, authenticate: Option<Arc<dyn Authenticator>>) -> Router {
        let base = self.base_path().trim_end_matches('/').to_owned();
        let state = Arc::new(AgentRoutes {
            agents: self.clone(),
            authenticate,
        });
        let mut router = Router::new();
        for rest in ["turn", "mcp", "credential", "finish"] {
            router = router.route(&format!("{base}/{rest}"), any(run_route));
        }
        router
            .route(
                &format!("{base}/approvals/{{id}}/{{decision}}"),
                any(approval_route),
            )
            .with_state(state)
    }

    /// The call gate of a generated API's MCP endpoint (D158, D183): a call
    /// an agent run makes with its token is held to the run's autonomy by
    /// its tool's `@autonomy` (an internal write when it declares none);
    /// every other caller's runs.
    pub fn mcp_gate(&self) -> Arc<dyn CallGate> {
        Arc::new(McpGate {
            agents: self.clone(),
        })
    }
}

/// Runs a route's work to its end in a task of its own: an instance that
/// is cancelled while its turn runs (a pause, a budget's cap) closes its
/// connection, and the work it asked for, which cancels it, must not stop
/// half done with it.
async fn detached<F>(work: F) -> Response
where
    F: std::future::Future<Output = Response> + Send + 'static,
{
    match tokio::spawn(work).await {
        Ok(response) => response,
        Err(err) => refusal(500, "failed", &format!("the agent route failed: {err}")),
    }
}

async fn run_route(State(routes): State<Arc<AgentRoutes>>, request: Request) -> Response {
    detached(serve_run(routes, request)).await
}

async fn serve_run(routes: Arc<AgentRoutes>, request: Request) -> Response {
    let agents = &routes.agents;
    let rest = request
        .uri()
        .path()
        .rsplit('/')
        .next()
        .unwrap_or_default()
        .to_owned();
    if request.method() != Method::POST {
        return refusal(405, "method_not_allowed", &format!("{rest} is a POST"));
    }
    let token = bearer_token(request.headers())
        .unwrap_or_default()
        .to_owned();
    let principal = match agents.principal_of(&token).await {
        Ok(Some(principal)) => principal,
        Ok(None) => return refusal(401, "unauthorized", "no running run holds this token"),
        Err(err) => return refusal(err.status, &err.code, &err.message),
    };
    let (parts, body) = request.into_parts();
    let Ok(bytes) = to_bytes(body, MCP_MAX_BODY_BYTES + 1).await else {
        return refusal(400, "bad_request", "the request body could not be read");
    };
    if rest == "mcp" {
        let tools = match agents.mcp_tools(&principal).await {
            Ok(tools) => tools,
            Err(err) => return refusal(err.status, &err.code, &err.message),
        };
        let request = http::Request::from_parts(parts, bytes);
        let options =
            Options::new(ServerInfo::new("agent-run", "1")).with_caller(principal.subject.clone());
        return serve(&request, &tools, &options).await.map(Body::from);
    }
    let body: Value = serde_json::from_slice(&bytes).unwrap_or_else(|_| json!({}));
    if rest == "turn" {
        let (status, answer) = agents.turn(&principal, &body).await;
        return json_response(status, &answer);
    }
    match recorded(agents, &principal, &rest, &body).await {
        Ok(()) => json_response(200, &json!({ "recorded": true })),
        Err(err) => refusal(err.status, &err.code, &err.message),
    }
}

/// What the sandbox reports: a credential it used, its reference and
/// target, never its value; or the outcome of the run, kept for the watch
/// that ends it.
async fn recorded(
    agents: &Agents,
    principal: &Principal,
    rest: &str,
    body: &Value,
) -> Result<(), super::AgentsError> {
    let run = agents.run_of(principal).await?;
    let run_id = string(&run, "id");
    let text = |member: &str| {
        body.get(member)
            .and_then(Value::as_str)
            .unwrap_or_default()
            .to_owned()
    };
    if rest == "credential" {
        let used_at = Some(text("usedAt"))
            .filter(|at| !at.is_empty())
            .map_or_else(|| at_text(agents.now()), Value::String);
        let use_ = json!({
            "ref": text("ref"),
            "provider": text("provider"),
            "target": agents.redact(body.get("target").unwrap_or(&Value::Null)),
            "usedAt": used_at,
        });
        let mut client = agents.store().client().await?;
        let tx = client.transaction().await?;
        {
            let repositories = agents.sys_tx(tx.db(), None)?;
            let runs = repositories.table(&agents.parts().runs)?;
            let current = runs
                .find_one(
                    Some(&obj(json!({ "id": { "eq": run_id } }))),
                    &crate::orm::FindOptions::default(),
                )
                .await?
                .unwrap_or_default();
            let mut uses = current
                .get("credentialUses")
                .and_then(Value::as_array)
                .cloned()
                .unwrap_or_default();
            uses.push(use_.clone());
            runs.update_one(
                &json!(run_id),
                &obj(json!({ "credentialUses": uses })),
                &crate::orm::WriteOptions::default(),
            )
            .await?;
        }
        tx.commit().await?;
        agents
            .append(
                &run_id,
                vec![json!({
                    "kind": "credential",
                    "ref": use_["ref"],
                    "content": format!("used {} on {}", use_["ref"].as_str().unwrap_or_default(), use_["target"]),
                })],
            )
            .await?;
        return Ok(());
    }
    // finish: the outcome the instance reports, kept for the watch that ends
    // the run.
    let outcome = match text("outcome").as_str() {
        "failed" => "failed",
        _ => "done",
    };
    let summary = text("summary");
    let mut update = json!({ "outcome": outcome });
    if !summary.is_empty() {
        let detail: String = agents.redact_text(&summary).chars().take(2000).collect();
        update["detail"] = json!(detail);
    }
    agents.update(&agents.parts().runs, &run_id, update).await?;
    if !summary.is_empty() {
        agents
            .append(&run_id, vec![json!({ "kind": "note", "content": summary })])
            .await?;
    }
    Ok(())
}

async fn approval_route(State(routes): State<Arc<AgentRoutes>>, request: Request) -> Response {
    detached(serve_approval(routes, request)).await
}

async fn serve_approval(routes: Arc<AgentRoutes>, request: Request) -> Response {
    if request.method() != Method::POST {
        return refusal(405, "method_not_allowed", "a decision is a POST");
    }
    let path = request.uri().path().to_owned();
    let mut segments = path.rsplit('/');
    let decision = segments.next().unwrap_or_default().to_owned();
    let id = segments.next().unwrap_or_default().to_owned();
    if decision != "approve" && decision != "deny" {
        return refusal(404, "not_found", "no such route");
    }
    let (parts, _) = request.into_parts();
    let who = match &routes.authenticate {
        Some(authenticate) => authenticate.authenticate(&parts).await.ok().flatten(),
        None => None,
    };
    let Some(who) = who else {
        return refusal(401, "unauthorized", "Authentication required");
    };
    let decided = if decision == "approve" {
        routes.agents.approve(&who, &id, Some("api")).await
    } else {
        routes.agents.deny(&who, &id, Some("api")).await
    };
    match decided {
        Ok(()) => json_response(
            200,
            &json!({ "state": if decision == "approve" { "approved" } else { "denied" } }),
        ),
        Err(err) => refusal(err.status, &err.code, &err.message),
    }
}

struct McpGate {
    agents: Agents,
}

#[async_trait]
impl CallGate for McpGate {
    async fn check(
        &self,
        principal: Option<&Principal>,
        tool: &RouteTool,
        arguments: &Map<String, Value>,
    ) -> Result<Option<Problem>, BoxError> {
        let Some(principal) =
            principal.filter(|p| p.kind == PrincipalKind::Agent && p.claims.contains_key("run"))
        else {
            return Ok(None);
        };
        let autonomy = tool.autonomy.as_ref();
        let class = autonomy
            .and_then(|a| OperationClass::parse(&a.class))
            .unwrap_or(OperationClass::InternalWrite);
        let mode = autonomy
            .and_then(|a| a.mode.as_deref())
            .and_then(super::Autonomy::parse);
        let gated = self
            .agents
            .gate(
                principal,
                &tool.tool.name,
                &Value::Object(arguments.clone()),
                Some(class),
                mode,
            )
            .await?;
        Ok(gated.problem())
    }
}
