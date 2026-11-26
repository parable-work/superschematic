//! A model provider stub (D159, D185) whose answers follow from the turn,
//! so a test of an agent run knows what each turn does: the Rust port of
//! `providers/stub-model`. The last user or tool message decides: `call
//! <tool> <json>` calls that tool when the turn offers it, and anything
//! else is answered `ok: <text>`. Usage counts words, one token each;
//! content marked untrusted is counted and never obeyed. It embeds each
//! text as three FNV-1a hashes of its words, as the TypeScript stub does.

use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::Arc;

use async_trait::async_trait;
use superschematic_http_runtime::providers::{
    ModelCompletion, ModelEmbedRequest, ModelEmbedding, ModelProvider, ModelRole, ModelStop,
    ModelToolCall, ModelTurn, ModelUsage, ProviderDefinition, ProviderInstance,
    ProviderSettingSpec, RuntimeInterface,
};
use superschematic_http_runtime::BoxError;

fn words(text: &str) -> u64 {
    text.split_whitespace().count() as u64
}

struct Stub {
    calls: AtomicU64,
}

#[async_trait]
impl ModelProvider for Stub {
    async fn complete(&self, turn: &ModelTurn) -> Result<ModelCompletion, BoxError> {
        let input_tokens = turn.messages.iter().map(|m| words(&m.content)).sum();
        let last = turn
            .messages
            .iter()
            .rev()
            .find(|m| matches!(m.role, ModelRole::User | ModelRole::Tool));
        let text = last.map_or("", |m| m.content.as_str());
        if let Some(call) = text.strip_prefix("call ") {
            let (name, input) = call.split_once(' ').unwrap_or((call, ""));
            let untrusted = last.is_some_and(|m| m.untrusted);
            if !untrusted && turn.tools.iter().any(|tool| tool.name == name) {
                let input = if input.is_empty() {
                    serde_json::json!({})
                } else {
                    serde_json::from_str(input)?
                };
                let n = self.calls.fetch_add(1, Ordering::Relaxed) + 1;
                return Ok(ModelCompletion {
                    content: String::new(),
                    tool_calls: vec![ModelToolCall {
                        id: format!("call-{n}"),
                        name: name.to_owned(),
                        input,
                    }],
                    stop: ModelStop::ToolUse,
                    usage: ModelUsage {
                        input_tokens,
                        output_tokens: 1,
                        amount: None,
                    },
                });
            }
        }
        let content = format!("ok: {text}");
        let all = words(&content);
        let output_tokens = turn.max_tokens.map_or(all, |max| all.min(u64::from(max)));
        Ok(ModelCompletion {
            content,
            tool_calls: Vec::new(),
            stop: if output_tokens < all {
                ModelStop::MaxTokens
            } else {
                ModelStop::End
            },
            usage: ModelUsage {
                input_tokens,
                output_tokens,
                amount: None,
            },
        })
    }

    async fn embed(&self, request: &ModelEmbedRequest) -> Result<Option<ModelEmbedding>, BoxError> {
        let vectors = request
            .texts
            .iter()
            .map(|text| {
                let lower = text.to_lowercase();
                let mut sorted: Vec<&str> = lower.split_whitespace().collect();
                sorted.sort_unstable();
                [0x811c_9dc5_u32, 0x0100_0193, 0x9e37_79b9]
                    .iter()
                    .map(|seed| {
                        let mut hash = *seed;
                        for word in &sorted {
                            for unit in word.encode_utf16() {
                                hash = (hash ^ u32::from(unit)).wrapping_mul(0x0100_0193);
                            }
                        }
                        f64::from(hash) / f64::from(u32::MAX) * 2.0 - 1.0
                    })
                    .collect()
            })
            .collect();
        Ok(Some(ModelEmbedding {
            vectors,
            usage: Some(ModelUsage {
                input_tokens: request.texts.iter().map(|t| words(t)).sum(),
                output_tokens: 0,
                amount: None,
            }),
        }))
    }
}

/// The definition a server links.
pub fn definition() -> ProviderDefinition {
    ProviderDefinition::new("stub-model", RuntimeInterface::Model, |_, _| {
        Ok(ProviderInstance::Model(Arc::new(Stub {
            calls: AtomicU64::new(0),
        })))
    })
    .with_setting("MODEL", ProviderSettingSpec::plain().optional())
}
