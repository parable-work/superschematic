//! The program a sandbox instance runs for an agent run (D158, D193), the
//! other side of the dispatcher's agent routes: the TypeScript runtime's
//! `runAgentInstance`. It reads what the dispatcher gave the instance:
//! `AGENT_ENDPOINT`, `AGENT_RUN_ID`, `AGENT_CREDENTIALS` (references
//! alone) and the secret `AGENT_RUN_TOKEN`. Each turn it posts the tool
//! results it has to `<endpoint>/turn`, where the app's model provider
//! completes the turn with the run's tools, meters it against the budget
//! and writes the transcript, so the instance holds no model key and no
//! budget of its own. Each tool call it runs through the run's MCP endpoint
//! (`<endpoint>/mcp`) as the run's token, or, for a tool the sandbox runs
//! itself, in process. A call an approval holds (428 `approval_required`)
//! is tried again until it runs, is denied, or the wait passes. A
//! credential a tool needs is resolved here: its value goes to the runner
//! that uses it and never to the endpoint, the model or a log; the
//! endpoint is told the reference, the provider, the target and the time.
//! When the model ends its turns, the runner reports the outcome at
//! `<endpoint>/finish`; the dispatcher's watch ends the run.

use std::collections::BTreeMap;
use std::sync::Arc;
use std::time::{Duration, Instant, SystemTime};

use async_trait::async_trait;
use serde_json::{json, Value};

use super::{
    time, use_credential, CredentialProvider, CredentialUse, InjectionRunner, InjectionTarget,
    ModelToolCall, UseCredential,
};
use crate::BoxError;

/// What a tool the sandbox runs is given: the run's credential
/// references, and the use of one.
pub struct SandboxToolContext<'a> {
    runner: &'a Runner,
    /// The run's credential references, as `AGENT_CREDENTIALS` gave them:
    /// each `{ ref, site?, header?, selector? }`.
    pub credentials: &'a [Value],
}

impl SandboxToolContext<'_> {
    /// Resolves `reference`, one the run holds, into `target` and hands the
    /// value to `runner`, recording the use with the run.
    pub async fn use_credential(
        &self,
        reference: &str,
        target: &InjectionTarget,
        runner: &dyn InjectionRunner,
    ) -> Result<CredentialUse, BoxError> {
        let Some((provider, name)) = &self.runner.credentials else {
            return Err("the instance has no credential provider".into());
        };
        if !self
            .credentials
            .iter()
            .any(|c| c.get("ref").and_then(Value::as_str) == Some(reference))
        {
            return Err(format!("the run holds no credential {reference}").into());
        }
        let used = use_credential(UseCredential {
            provider: provider.as_ref(),
            provider_name: name,
            reference,
            target,
            runner,
            record: None,
            now: Some(SystemTime::now()),
        })
        .await?;
        self.runner
            .post(
                "credential",
                &json!({
                    "ref": used.reference,
                    "provider": used.provider,
                    "target": used.target,
                    "usedAt": time::text(used.used_at),
                }),
            )
            .await?;
        Ok(used)
    }
}

/// A tool the sandbox runs in process.
#[async_trait]
pub trait SandboxToolHandler: Send + Sync {
    async fn run(&self, input: &Value, ctx: &SandboxToolContext<'_>) -> Result<Value, BoxError>;
}

/// What [`run_agent_instance`] takes.
#[derive(Clone)]
pub struct AgentInstanceOptions {
    /// The instance's environment: what the dispatcher gave it.
    pub env: BTreeMap<String, String>,
    /// The credential provider the instance resolves references through,
    /// and its name for the record.
    pub credentials: Option<(Arc<dyn CredentialProvider>, String)>,
    /// The tools the sandbox runs itself, by name.
    pub tools: BTreeMap<String, Arc<dyn SandboxToolHandler>>,
    /// How long a call waits for its approval; a minute unless set.
    pub approval_wait: Duration,
    /// How often a waiting call is tried again; a second unless set.
    pub poll: Duration,
    /// The most turns it takes; 20 unless set.
    pub max_turns: u32,
}

impl AgentInstanceOptions {
    /// The options of an instance whose environment is `env`.
    pub fn new(env: BTreeMap<String, String>) -> Self {
        Self {
            env,
            credentials: None,
            tools: BTreeMap::new(),
            approval_wait: Duration::from_secs(60),
            poll: Duration::from_secs(1),
            max_turns: 20,
        }
    }
}

/// What a run of the instance came to: `done`, `failed` or `stopped`.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct AgentInstanceResult {
    pub outcome: String,
    pub turns: u32,
    pub summary: String,
}

struct Runner {
    endpoint: String,
    token: String,
    client: reqwest::Client,
    credentials: Option<(Arc<dyn CredentialProvider>, String)>,
}

/// The JSON-RPC answer of an MCP endpoint: its body, or the data of the
/// one message event it streams.
fn rpc_body(text: &str) -> Value {
    let data: String = text
        .lines()
        .filter_map(|line| line.strip_prefix("data:"))
        .map(str::trim)
        .collect();
    let text = if data.is_empty() { text } else { &data };
    serde_json::from_str(text).unwrap_or(Value::Null)
}

impl Runner {
    async fn post(&self, path: &str, body: &Value) -> Result<(u16, String), BoxError> {
        let response = self
            .client
            .post(format!("{}/{path}", self.endpoint))
            .header("authorization", format!("Bearer {}", self.token))
            .header("content-type", "application/json")
            .header("accept", "application/json, text/event-stream")
            .body(body.to_string())
            .send()
            .await?;
        let status = response.status().as_u16();
        Ok((status, response.text().await?))
    }

    /// A call of the run's MCP tool `name`: its text, and the status of
    /// the problem it answered, if any.
    async fn mcp(
        &self,
        id: u64,
        name: &str,
        args: &Value,
    ) -> Result<(String, Option<u16>), BoxError> {
        let (status, text) = self
            .post(
                "mcp",
                &json!({ "jsonrpc": "2.0", "id": id, "method": "tools/call", "params": { "name": name, "arguments": args } }),
            )
            .await?;
        if status == 401 {
            return Ok(("the run has ended".to_owned(), Some(401)));
        }
        let body = rpc_body(&text);
        if let Some(error) = body.get("error") {
            let message = error
                .get("message")
                .and_then(Value::as_str)
                .unwrap_or("tool error");
            return Ok((message.to_owned(), Some(400)));
        }
        let result = &body["result"];
        let text: String = result["content"]
            .as_array()
            .into_iter()
            .flatten()
            .filter_map(|c| c.get("text").and_then(Value::as_str))
            .collect();
        if result["isError"] != json!(true) {
            return Ok((text, None));
        }
        let status = result["structuredContent"]["status"]
            .as_u64()
            .or_else(|| {
                serde_json::from_str::<Value>(&text)
                    .ok()
                    .and_then(|p| p["status"].as_u64())
            })
            .and_then(|s| u16::try_from(s).ok());
        Ok((text, status.or(Some(500))))
    }
}

/// Runs an agent run's turns inside its sandbox instance until the model
/// ends them or the run is stopped.
pub async fn run_agent_instance(
    options: AgentInstanceOptions,
) -> Result<AgentInstanceResult, BoxError> {
    let env = &options.env;
    let endpoint = env
        .get("AGENT_ENDPOINT")
        .map(|e| e.trim_end_matches('/').to_owned())
        .unwrap_or_default();
    let token = env.get("AGENT_RUN_TOKEN").cloned().unwrap_or_default();
    if endpoint.is_empty() || token.is_empty() {
        return Err(
            "run_agent_instance: AGENT_ENDPOINT and AGENT_RUN_TOKEN are the instance's".into(),
        );
    }
    let credentials: Vec<Value> = env
        .get("AGENT_CREDENTIALS")
        .and_then(|c| serde_json::from_str(c).ok())
        .unwrap_or_default();
    let runner = Runner {
        endpoint,
        token,
        client: reqwest::Client::new(),
        credentials: options.credentials.clone(),
    };
    let ctx = SandboxToolContext {
        runner: &runner,
        credentials: &credentials,
    };
    let mut rpc = 0u64;
    let mut messages: Vec<Value> = Vec::new();
    let mut summary = String::new();
    for turn in 1..=options.max_turns {
        let (status, text) = runner
            .post("turn", &json!({ "messages": messages }))
            .await?;
        messages.clear();
        if status == 409 || status == 401 {
            return Ok(AgentInstanceResult {
                outcome: "stopped".to_owned(),
                turns: turn - 1,
                summary,
            });
        }
        if !(200..300).contains(&status) {
            runner
                .post(
                    "finish",
                    &json!({ "outcome": "failed", "summary": format!("the turn answered {status}") }),
                )
                .await?;
            return Ok(AgentInstanceResult {
                outcome: "failed".to_owned(),
                turns: turn,
                summary,
            });
        }
        let completion: Value = serde_json::from_str(&text).unwrap_or(Value::Null);
        summary = completion["content"]
            .as_str()
            .unwrap_or_default()
            .to_owned();
        let calls: Vec<ModelToolCall> =
            serde_json::from_value(completion["toolCalls"].clone()).unwrap_or_default();
        if calls.is_empty() {
            runner
                .post("finish", &json!({ "outcome": "done", "summary": summary }))
                .await?;
            return Ok(AgentInstanceResult {
                outcome: "done".to_owned(),
                turns: turn,
                summary,
            });
        }
        for call in calls {
            let content = match options.tools.get(&call.name) {
                Some(local) => match local.run(&call.input, &ctx).await {
                    Ok(result) => result.to_string(),
                    Err(err) => json!({ "error": err.to_string() }).to_string(),
                },
                None => {
                    let until = Instant::now() + options.approval_wait;
                    loop {
                        rpc += 1;
                        let (text, problem) = runner.mcp(rpc, &call.name, &call.input).await?;
                        if problem != Some(428) || Instant::now() > until {
                            break text;
                        }
                        tokio::time::sleep(options.poll).await;
                    }
                }
            };
            messages.push(json!({ "role": "tool", "toolCallId": call.id, "content": content }));
        }
    }
    runner
        .post("finish", &json!({ "outcome": "done", "summary": summary }))
        .await?;
    Ok(AgentInstanceResult {
        outcome: "done".to_owned(),
        turns: options.max_turns,
        summary,
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn an_rpc_answer_is_its_body_or_its_event() {
        assert_eq!(rpc_body("{\"id\":1}"), json!({ "id": 1 }));
        assert_eq!(
            rpc_body("event: message\ndata: {\"id\":2}\n\n"),
            json!({ "id": 2 })
        );
        assert_eq!(rpc_body("not json"), Value::Null);
    }
}
