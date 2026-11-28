//! A run's tools (D158): the MCP tool set a run's principal is served, its
//! calls held to the run's autonomy, and the model turn the instance posts
//! its tool results to.

use std::collections::BTreeSet;
use std::time::Duration;

use async_trait::async_trait;
use serde_json::{json, Map, Value};

use super::super::inbound::{SendInput, SendOutcome};
use super::super::parties::PartyRef;
use super::super::rowfeatures::RowRef;
use super::super::search_tables::search_mcp_tool;
use super::super::{AccessCaller, Error, FindOptions, Values, WriteOptions};
use super::dispatcher::{
    as_number, number, obj, opt, string, targets_of, AgentRunRequest, AgentToolContext, Agents,
    AgentsError, GateOutcome,
};
use super::{params_hash, Autonomy, OperationClass};
use crate::auth::{covers, Principal};
use crate::mcp::{CallOutcome, Tool, ToolError, ToolSet};
use crate::orm::routes::acting;
use crate::providers::{ModelMessage, ModelRole, ModelTool, ModelToolCall, ModelTurn};

/// What a tool of the run's set is.
#[derive(Clone, Debug)]
enum Kind {
    App(String),
    Search,
    MessageParty,
    AskPrincipal,
}

struct Offered {
    tool: Tool,
    kind: Kind,
}

/// The MCP tool set a run's principal is served: the app's tools its
/// definition and role allow, search, `message_party` and `ask_principal`.
pub(super) struct RunTools {
    agents: Agents,
    principal: Principal,
    run: Values,
    offered: Vec<Offered>,
}

fn problem(status: u16, code: &str, detail: &str, approval: Option<&str>) -> CallOutcome {
    let mut problem = json!({ "type": "about:blank", "title": code, "status": status, "detail": detail, "code": code });
    if let Some(approval) = approval {
        problem["approval"] = json!(approval);
    }
    CallOutcome::Problem(problem)
}

fn allowed(definition: &Values, name: &str) -> bool {
    match definition.get("tools").and_then(Value::as_array) {
        None => true,
        Some(list) => list.iter().any(|t| t.as_str() == Some(name)),
    }
}

fn counter(class: OperationClass) -> &'static str {
    match class {
        OperationClass::Read => "reads",
        OperationClass::MessageParty => "messagesSent",
        OperationClass::InternalWrite | OperationClass::MoneyMovement => "writes",
    }
}

fn status_of(err: &AgentsError) -> u16 {
    err.status
}

fn tool_of(name: &str, description: &str, input_schema: Value, untrusted: bool) -> Tool {
    let mut meta = Map::new();
    if untrusted {
        meta.insert("untrusted".to_owned(), json!(true));
    }
    Tool {
        name: name.to_owned(),
        title: None,
        description: Some(description.to_owned()),
        input_schema,
        annotations: None,
        meta: (!meta.is_empty()).then_some(meta),
    }
}

impl Agents {
    /// The MCP tool set a run's principal is served.
    pub(super) async fn mcp_tools(&self, principal: &Principal) -> Result<RunTools, AgentsError> {
        let run = self.run_of(principal).await?;
        let definition = self
            .definition_row(&string(&run, "agent"))
            .await?
            .unwrap_or_default();
        let mut offered = Vec::new();
        for (name, tool) in &self.inner.tools {
            if !allowed(&definition, name) {
                continue;
            }
            if tool
                .permission
                .as_deref()
                .is_some_and(|need| !principal.permissions.iter().any(|p| covers(p, need)))
            {
                continue;
            }
            offered.push(Offered {
                tool: tool_of(
                    name,
                    &tool.description,
                    tool.input_schema.clone(),
                    tool.untrusted,
                ),
                kind: Kind::App(name.clone()),
            });
        }
        if self.inner.search.is_some() && allowed(&definition, "search") {
            if let Ok(tool) = serde_json::from_value::<Tool>(search_mcp_tool()) {
                offered.push(Offered {
                    tool,
                    kind: Kind::Search,
                });
            }
        }
        if self.inner.inbound.get().is_some() && allowed(&definition, "message_party") {
            offered.push(Offered {
                tool: tool_of(
                    "message_party",
                    "Sends a message to a party on a channel: the party the run acts for unless another is named. It is held for approval as the autonomy policy says.",
                    json!({
                        "type": "object",
                        "additionalProperties": false,
                        "required": ["channel", "body"],
                        "properties": {
                            "channel": { "type": "string" },
                            "body": { "type": "string", "minLength": 1 },
                            "subject": { "type": "string" },
                            "party": { "type": "object", "properties": { "table": { "type": "string" }, "key": { "type": "string" } } }
                        }
                    }),
                    false,
                ),
                kind: Kind::MessageParty,
            });
        }
        if allowed(&definition, "ask_principal") {
            offered.push(Offered {
                tool: tool_of(
                    "ask_principal",
                    "Asks a principal a question in a target row's thread; if nobody answers by the deadline, a follow-up row is made.",
                    json!({
                        "type": "object",
                        "additionalProperties": false,
                        "required": ["table", "key", "question"],
                        "properties": {
                            "table": { "type": "string" },
                            "key": { "type": "string" },
                            "question": { "type": "string", "minLength": 1 },
                            "principal": { "type": "string" },
                            "deadlineSeconds": { "type": "integer", "minimum": 1 }
                        }
                    }),
                    false,
                ),
                kind: Kind::AskPrincipal,
            });
        }
        Ok(RunTools {
            agents: self.clone(),
            principal: principal.clone(),
            run,
            offered,
        })
    }

    /// Records a call on the run: its counters, and the call in the
    /// transcript.
    async fn record(
        &self,
        run: &str,
        name: &str,
        args: &Map<String, Value>,
        outcome: &str,
        counts: &[(&str, i64)],
    ) -> Result<(), AgentsError> {
        if !counts.is_empty() {
            let mut client = self.store().client().await?;
            let tx = client.transaction().await?;
            {
                let repositories = self.sys_tx(tx.db(), None)?;
                let runs = repositories.table(&self.parts().runs)?;
                let current = runs
                    .find_one(
                        Some(&obj(json!({ "id": { "eq": run } }))),
                        &FindOptions::default(),
                    )
                    .await?
                    .unwrap_or_default();
                let mut update = Values::new();
                for (field, by) in counts {
                    update.insert((*field).to_owned(), json!(number(&current, field) + by));
                }
                runs.update_one(&json!(run), &update, &WriteOptions::default())
                    .await?;
            }
            tx.commit().await?;
        }
        self.append(
            run,
            vec![json!({ "kind": "call", "tool": name, "args": args, "outcome": outcome })],
        )
        .await
    }

    /// The tools offered to the model: the run's MCP tools and the
    /// sandbox's own, and the names of those whose results are untrusted.
    async fn model_tools(
        &self,
        principal: &Principal,
        definition: &Values,
    ) -> Result<(Vec<ModelTool>, BTreeSet<String>), AgentsError> {
        let set = self.mcp_tools(principal).await?;
        let mut untrusted = BTreeSet::new();
        let mut list = Vec::new();
        for offered in &set.offered {
            let tool = &offered.tool;
            if tool
                .meta
                .as_ref()
                .and_then(|m| m.get("untrusted"))
                .and_then(Value::as_bool)
                .unwrap_or(false)
            {
                untrusted.insert(tool.name.clone());
            }
            list.push(ModelTool {
                name: tool.name.clone(),
                description: tool
                    .description
                    .clone()
                    .unwrap_or_else(|| tool.name.clone()),
                input_schema: tool.input_schema.as_object().cloned().unwrap_or_default(),
            });
        }
        for tool in &self.inner.sandbox_tools {
            if !allowed(definition, &tool.name) {
                continue;
            }
            if tool.untrusted {
                untrusted.insert(tool.name.clone());
            }
            list.push(ModelTool {
                name: tool.name.clone(),
                description: tool.description.clone(),
                input_schema: tool.input_schema.as_object().cloned().unwrap_or_default(),
            });
        }
        Ok((list, untrusted))
    }

    /// A turn the instance posts its tool results to (D158): the model
    /// provider completes it with the run's tools, its usage is metered on
    /// the run and the budget, and the transcript written. Answers the
    /// status and body of the route.
    pub(super) async fn turn(&self, principal: &Principal, body: &Value) -> (u16, Value) {
        match self.turn_of(principal, body).await {
            Ok(answer) => answer,
            Err(err) => (
                err.status,
                refusal_body(err.status, &err.code, &self.redact_text(&err.message)),
            ),
        }
    }

    async fn turn_of(
        &self,
        principal: &Principal,
        body: &Value,
    ) -> Result<(u16, Value), AgentsError> {
        let run = self.run_of(principal).await?;
        let run_id = string(&run, "id");
        if !matches!(string(&run, "status").as_str(), "running" | "waiting") {
            return Ok((409, refusal_body(409, "run_ended", "the run has ended")));
        }
        let agent = string(&run, "agent");
        let stand = self.standing(&agent).await?;
        if let Some(reason) = &stand.paused {
            self.cancel(
                &run_id,
                Some("paused"),
                Some(&format!("the agent is paused ({reason})")),
            )
            .await?;
            return Ok((
                409,
                refusal_body(409, "agent_paused", "the agent is paused"),
            ));
        }
        if stand.agent_capped {
            self.set_paused(Some(&agent), true, Some("budget"), Some("budget"))
                .await?;
            return Ok((
                409,
                refusal_body(409, "agent_paused", "the agent reached its budget's cap"),
            ));
        }
        let definition = self.definition_row(&agent).await?.unwrap_or_default();
        let limits = definition
            .get("limits")
            .and_then(Value::as_object)
            .cloned()
            .unwrap_or_default();
        if let Some(max) = limits.get("maxTurns") {
            if number(&run, "turns") >= as_number(Some(max)) {
                return Ok((
                    409,
                    refusal_body(409, "max_turns", "the run took its turns"),
                ));
            }
        }
        let (tools, untrusted) = self.model_tools(principal, &definition).await?;
        let recorded: Vec<Value> = run
            .get("transcript")
            .and_then(Value::as_array)
            .cloned()
            .unwrap_or_default();
        let mut untrusted_calls: BTreeSet<String> = run
            .get("untrustedCalls")
            .and_then(Value::as_array)
            .map(|items| {
                items
                    .iter()
                    .map(|i| match i {
                        Value::String(s) => s.clone(),
                        other => other.to_string(),
                    })
                    .collect()
            })
            .unwrap_or_default();
        let incoming = incoming_of(body, &untrusted_calls);
        self.append(&run_id, incoming.clone()).await?;
        let outside = incoming
            .iter()
            .filter(|e| e["untrusted"] == json!(true) && e["role"] == json!("tool"))
            .count() as i64;
        if outside > 0 {
            self.update(
                &self.parts().runs,
                &run_id,
                json!({ "untrusted": number(&run, "untrusted") + outside }),
            )
            .await?;
        }
        let history: Vec<Value> = recorded
            .into_iter()
            .chain(incoming.iter().map(|e| self.redact(e)))
            .collect();
        let messages = messages_of(opt(&definition, "instructions"), &history);
        let slot = self.model_slot_of(&definition)?;
        let provider = self.providers().model(&slot)?;
        let turn = ModelTurn {
            model: opt(&definition, "model"),
            messages,
            tools,
            max_tokens: None,
        };
        let completion = match provider.complete(&turn).await {
            Ok(completion) => completion,
            Err(err) => {
                return Ok((
                    502,
                    refusal_body(502, "model_failed", &self.redact_text(&err.to_string())),
                ));
            }
        };
        for call in &completion.tool_calls {
            if untrusted.contains(&call.name) {
                untrusted_calls.insert(call.id.clone());
            }
        }
        let spent = (self.inner.cost_of)(&completion.usage);
        {
            let mut client = self.store().client().await?;
            let tx = client.transaction().await?;
            {
                let repositories = self.sys_tx(tx.db(), None)?;
                let runs = repositories.table(&self.parts().runs)?;
                let current = runs
                    .find_one(
                        Some(&obj(json!({ "id": { "eq": run_id } }))),
                        &FindOptions::default(),
                    )
                    .await?
                    .unwrap_or_default();
                runs.update_one(
                    &json!(run_id),
                    &obj(json!({
                        "turns": number(&current, "turns") + 1,
                        "inputTokens": number(&current, "inputTokens") + i64::try_from(completion.usage.input_tokens).unwrap_or(0),
                        "outputTokens": number(&current, "outputTokens") + i64::try_from(completion.usage.output_tokens).unwrap_or(0),
                        "cost": number(&current, "cost") + spent,
                        "untrustedCalls": untrusted_calls.iter().collect::<Vec<_>>(),
                        "modelSlot": slot,
                    })),
                    &WriteOptions::default(),
                )
                .await?;
            }
            tx.commit().await?;
        }
        let mut entry = json!({
            "kind": "completion",
            "role": "assistant",
            "content": completion.content,
            "toolCalls": completion.tool_calls,
        });
        if let Some(state) = &completion.provider_state {
            entry["providerState"] = state.clone();
        }
        self.append(&run_id, vec![entry]).await?;
        self.meter_usage(&agent, spent).await?;
        Ok((
            200,
            json!({
                "content": self.redact_text(&completion.content),
                "toolCalls": self.redact(&json!(completion.tool_calls)),
                "stop": completion.stop,
                "usage": completion.usage,
            }),
        ))
    }
}

/// The messages a runner posts with a turn, as transcript entries: its
/// tool results and user messages, each a tool result of a call marked
/// untrusted, and anything it sends as a user, marked untrusted: data.
fn incoming_of(body: &Value, untrusted_calls: &BTreeSet<String>) -> Vec<Value> {
    let mut incoming = Vec::new();
    for message in body
        .get("messages")
        .and_then(Value::as_array)
        .into_iter()
        .flatten()
    {
        let role = message
            .get("role")
            .and_then(Value::as_str)
            .unwrap_or_default();
        if role != "tool" && role != "user" {
            continue;
        }
        let mut entry = json!({
            "kind": "message",
            "role": role,
            "content": message.get("content").and_then(Value::as_str).unwrap_or_default(),
        });
        let call = message.get("toolCallId").and_then(Value::as_str);
        if let Some(call) = call {
            entry["toolCallId"] = json!(call);
        }
        if call.is_some_and(|c| untrusted_calls.contains(c)) || role == "user" {
            entry["untrusted"] = json!(true);
        }
        incoming.push(entry);
    }
    incoming
}

/// A turn's messages: the definition's instructions, then the run's
/// transcript, its task, its messages and its earlier completions, each
/// with the tool calls the tool messages after it answer and the provider's
/// state of it, as the provider returned it (D158).
fn messages_of(instructions: Option<String>, history: &[Value]) -> Vec<ModelMessage> {
    let mut messages = Vec::new();
    if let Some(instructions) = instructions {
        messages.push(message_of(ModelRole::System, instructions));
    }
    for entry in history {
        let untrusted = entry
            .get("untrusted")
            .and_then(Value::as_bool)
            .unwrap_or(false);
        let content = entry
            .get("content")
            .and_then(Value::as_str)
            .unwrap_or_default()
            .to_owned();
        let mut message = match entry.get("kind").and_then(Value::as_str) {
            Some("task") => message_of(ModelRole::User, content),
            Some("message") => {
                let role = match entry.get("role").and_then(Value::as_str) {
                    Some("user") => ModelRole::User,
                    Some("assistant") => ModelRole::Assistant,
                    Some("system") => ModelRole::System,
                    _ => ModelRole::Tool,
                };
                let mut m = message_of(role, content);
                m.tool_call_id = entry
                    .get("toolCallId")
                    .and_then(Value::as_str)
                    .map(str::to_owned);
                m
            }
            Some("completion") => {
                let mut m = message_of(ModelRole::Assistant, content);
                m.tool_calls = entry
                    .get("toolCalls")
                    .and_then(Value::as_array)
                    .map(|calls| {
                        calls
                            .iter()
                            .filter_map(|c| serde_json::from_value::<ModelToolCall>(c.clone()).ok())
                            .collect()
                    })
                    .unwrap_or_default();
                m.provider_state = entry.get("providerState").filter(|s| !s.is_null()).cloned();
                messages.push(m);
                continue;
            }
            _ => continue,
        };
        message.untrusted = untrusted;
        messages.push(message);
    }
    messages
}

fn message_of(role: ModelRole, content: String) -> ModelMessage {
    ModelMessage {
        role,
        content,
        tool_call_id: None,
        tool_calls: Vec::new(),
        provider_state: None,
        untrusted: false,
    }
}

pub(super) fn refusal_body(status: u16, code: &str, detail: &str) -> Value {
    json!({ "type": "about:blank", "title": code, "status": status, "code": code, "detail": detail })
}

impl RunTools {
    async fn call_app(
        &self,
        name: &str,
        args: &Map<String, Value>,
    ) -> Result<CallOutcome, AgentsError> {
        let agents = &self.agents;
        let run_id = string(&self.run, "id");
        let Some(tool) = agents.inner.tools.get(name).cloned() else {
            return Ok(problem(404, "not_found", &format!("no tool {name}"), None));
        };
        let gated = agents
            .gate(
                &self.principal,
                name,
                &Value::Object(args.clone()),
                Some(tool.class),
                tool.mode,
            )
            .await?;
        let mode = match &gated {
            GateOutcome::Run { mode, .. } => *mode,
            GateOutcome::Refused {
                status,
                code,
                detail,
                approval,
            } => {
                agents.record(&run_id, name, args, code, &[]).await?;
                return Ok(problem(*status, code, detail, approval.as_deref()));
            }
        };
        let ran: Result<Value, AgentsError> = async {
            let mut client = agents.store().client().await?;
            let tx = client.transaction().await?;
            let result = {
                let repositories = agents.as_principal(tx.db(), &self.principal)?;
                let ctx = AgentToolContext {
                    repositories: &repositories,
                    principal: &self.principal,
                    run: &self.run,
                    args,
                };
                tool.run.run(&ctx).await.map_err(AgentsError::from)?
            };
            tx.commit().await?;
            Ok(result)
        }
        .await;
        match ran {
            Ok(result) => {
                let mut counts = vec![(counter(tool.class), 1)];
                if tool.untrusted {
                    counts.push(("untrusted", 1));
                }
                agents.record(&run_id, name, args, "done", &counts).await?;
                if mode == Autonomy::Tell {
                    let mut client = agents.store().client().await?;
                    let tx = client.transaction().await?;
                    agents
                        .raise_in(
                            tx.db(),
                            &agents.parts().runs,
                            &run_id,
                            "agent.acted",
                            json!({ "tool": name }),
                            None,
                        )
                        .await?;
                    tx.commit().await?;
                }
                Ok(CallOutcome::Result(if tool.untrusted {
                    json!({ "untrusted": true, "content": result })
                } else {
                    result
                }))
            }
            Err(err) => {
                let status = status_of(&err);
                agents
                    .record(&run_id, name, args, &format!("failed {status}"), &[])
                    .await?;
                let code = match status {
                    404 => "not_found",
                    403 => "forbidden",
                    _ => "failed",
                };
                Ok(problem(
                    status,
                    code,
                    &agents.redact_text(&err.message),
                    None,
                ))
            }
        }
    }

    async fn call_search(&self, args: &Map<String, Value>) -> Result<CallOutcome, AgentsError> {
        let agents = &self.agents;
        let run_id = string(&self.run, "id");
        let options = agents.inner.search.clone().unwrap_or_default();
        let client = agents.store().client().await?;
        let mut repositories = client
            .db()
            .repositories(agents.definition())?
            .acting(acting(Some(&self.principal)))
            .with_access(AccessCaller::of(Some(&self.principal)));
        if let Some(keys) = agents.tables().keys() {
            repositories = repositories.with_keys(keys.as_ref());
        }
        let answer = repositories
            .call_search_tool(Some(&self.principal), args, &options)
            .await;
        drop(repositories);
        drop(client);
        match answer {
            Ok(result) => {
                agents
                    .record(&run_id, "search", args, "done", &[("reads", 1)])
                    .await?;
                Ok(CallOutcome::Result(
                    serde_json::to_value(result).unwrap_or(Value::Null),
                ))
            }
            Err(err) => {
                agents
                    .record(&run_id, "search", args, "refused", &[])
                    .await?;
                let status = match &err {
                    Error::Invalid(_) => 400,
                    _ => 500,
                };
                Ok(problem(
                    status,
                    if status == 400 {
                        "invalid_argument"
                    } else {
                        "failed"
                    },
                    &err.to_string(),
                    None,
                ))
            }
        }
    }

    async fn call_message_party(
        &self,
        args: &Map<String, Value>,
    ) -> Result<CallOutcome, AgentsError> {
        let agents = &self.agents;
        let run_id = string(&self.run, "id");
        let Some(inbound) = agents.inbound() else {
            return Ok(problem(404, "not_found", "no tool message_party", None));
        };
        let named = args.get("party").and_then(Value::as_object);
        let party =
            match named.and_then(|p| Some((p.get("table")?.as_str()?, p.get("key")?.as_str()?))) {
                Some((table, key)) => Some(PartyRef::new(table, key)),
                None => (string(&self.run, "actingForKind") == "party")
                    .then(|| {
                        opt(&self.run, "actingForTable")
                            .map(|t| PartyRef::new(t, string(&self.run, "actingFor")))
                    })
                    .flatten(),
            };
        let Some(party) = party else {
            return Ok(problem(
                400,
                "invalid_argument",
                "the run acts for no party; name one",
                None,
            ));
        };
        let channel = args
            .get("channel")
            .and_then(Value::as_str)
            .unwrap_or_default()
            .to_owned();
        let body = args
            .get("body")
            .and_then(Value::as_str)
            .unwrap_or_default()
            .to_owned();
        let subject = args
            .get("subject")
            .and_then(Value::as_str)
            .map(str::to_owned);
        // The send's own approval hook is this run's policy (the
        // dispatcher's send approval), so the call is not gated twice.
        let mut input = SendInput::new(
            self.principal.clone(),
            party.clone(),
            channel.clone(),
            body.clone(),
        );
        input.subject = subject.clone();
        input.about = targets_of(&self.run);
        let sent: SendOutcome = match inbound.send(input).await {
            Ok(sent) => sent,
            Err(err) => {
                let err = AgentsError::from(err);
                agents
                    .record(&run_id, "message_party", args, "refused", &[])
                    .await?;
                return Ok(problem(403, "refused", &err.message, None));
            }
        };
        if sent.outcome == "awaitingApproval" {
            if let Some(message) = &sent.message {
                let hash = params_hash(
                    "send_message",
                    &Agents::send_params(&party, &channel, subject.as_deref(), &body),
                );
                let open = agents
                    .find_many(
                        &agents.parts().approvals,
                        json!({
                            "run": { "eq": run_id },
                            "operation": { "eq": "send_message" },
                            "paramsHash": { "eq": hash },
                            "state": { "eq": "pending" },
                        }),
                        &super::dispatcher::many(1),
                    )
                    .await?;
                if let Some(open) = open.first() {
                    agents
                        .update(
                            &agents.parts().approvals,
                            &string(open, "id"),
                            json!({ "holds": message }),
                        )
                        .await?;
                }
            }
        }
        let counts: &[(&str, i64)] = if sent.outcome == "sent" {
            &[("messagesSent", 1)]
        } else {
            &[]
        };
        agents
            .record(&run_id, "message_party", args, &sent.outcome, counts)
            .await?;
        Ok(match sent.outcome.as_str() {
            "refused" => problem(
                403,
                "refused",
                sent.detail.as_deref().unwrap_or("refused"),
                None,
            ),
            "failed" => problem(
                502,
                "failed",
                sent.detail.as_deref().unwrap_or("failed"),
                None,
            ),
            _ => CallOutcome::Result(serde_json::to_value(&sent).unwrap_or(Value::Null)),
        })
    }

    async fn call_ask_principal(
        &self,
        args: &Map<String, Value>,
    ) -> Result<CallOutcome, AgentsError> {
        let agents = &self.agents;
        let run_id = string(&self.run, "id");
        let text = |member: &str| {
            args.get(member)
                .and_then(Value::as_str)
                .unwrap_or_default()
                .to_owned()
        };
        let row = RowRef::new(text("table"), text("key"));
        if !targets_of(&self.run).contains(&row) {
            agents
                .record(&run_id, "ask_principal", args, "forbidden", &[])
                .await?;
            return Ok(problem(
                403,
                "forbidden",
                "a run asks only in the thread of one of its target rows",
                None,
            ));
        }
        // Posting in a thread is an internal write: an entered sender's run
        // asks first (D183).
        let gated = agents
            .gate(
                &self.principal,
                "ask_principal",
                &Value::Object(args.clone()),
                Some(OperationClass::InternalWrite),
                None,
            )
            .await?;
        if let GateOutcome::Refused {
            status,
            code,
            detail,
            approval,
        } = &gated
        {
            agents
                .record(&run_id, "ask_principal", args, code, &[])
                .await?;
            return Ok(problem(*status, code, detail, approval.as_deref()));
        }
        let question = text("question");
        let principal = args.get("principal").and_then(Value::as_str);
        let message = {
            let mut client = agents.store().client().await?;
            let tx = client.transaction().await?;
            let message = {
                let repositories = agents.as_principal(tx.db(), &self.principal)?;
                agents
                    .thread()
                    .post(&repositories, &row, &question, principal)
                    .await
                    .map_err(AgentsError::from)?
            };
            tx.commit().await?;
            message
        };
        let delay = args
            .get("deadlineSeconds")
            .and_then(Value::as_u64)
            .unwrap_or(agents.inner.ask_seconds);
        let follow = AgentRunRequest {
            step: Some("followUp".to_owned()),
            run: Some(run_id.clone()),
            ask: Some(super::dispatcher::AskDeadline {
                table: row.table.clone(),
                key: row.key.clone(),
                message: message.clone(),
                question,
            }),
            ..AgentRunRequest::default()
        };
        {
            let mut client = agents.store().client().await?;
            let tx = client.transaction().await?;
            tx.db()
                .enqueue(agents.requests(), &follow, Duration::from_secs(delay))
                .await?;
            tx.commit().await?;
        }
        agents
            .record(
                &run_id,
                "ask_principal",
                args,
                "asked",
                &[("messagesSent", 1)],
            )
            .await?;
        Ok(CallOutcome::Result(
            json!({ "asked": true, "message": message, "deadlineSeconds": delay }),
        ))
    }
}

#[async_trait]
impl ToolSet for RunTools {
    async fn list(&self) -> Result<Vec<Tool>, ToolError> {
        Ok(self.offered.iter().map(|o| o.tool.clone()).collect())
    }

    async fn call(
        &self,
        name: &str,
        arguments: &Map<String, Value>,
    ) -> Result<CallOutcome, ToolError> {
        let Some(offered) = self.offered.iter().find(|o| o.tool.name == name) else {
            return Ok(problem(404, "not_found", &format!("no tool {name}"), None));
        };
        let answer = match &offered.kind {
            Kind::App(tool) => self.call_app(tool, arguments).await,
            Kind::Search => self.call_search(arguments).await,
            Kind::MessageParty => self.call_message_party(arguments).await,
            Kind::AskPrincipal => self.call_ask_principal(arguments).await,
        };
        answer.map_err(|err| ToolError::Failed(Box::new(err)))
    }
}
