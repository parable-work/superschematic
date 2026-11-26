//! The model provider (D159): completes one turn of an agent run (D158)
//! with the tools the run's role allows, and reports what the turn used.
//! Content from outside is marked untrusted, and a provider passes the
//! mark on in the way its API takes it. No credential value is ever in a
//! message: a credential reaches only the runner.

use async_trait::async_trait;
use serde::{Deserialize, Serialize};
use serde_json::{Map, Value};

use super::Amount;
use crate::BoxError;

/// Who a message of a turn is from.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum ModelRole {
    System,
    User,
    Assistant,
    Tool,
}

/// One message of a turn.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ModelMessage {
    pub role: ModelRole,
    pub content: String,
    /// For a tool message: the call it answers.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub tool_call_id: Option<String>,
    /// Content from outside the app, which is data and never instructions.
    #[serde(default, skip_serializing_if = "std::ops::Not::not")]
    pub untrusted: bool,
}

/// A tool the model may call.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ModelTool {
    pub name: String,
    pub description: String,
    /// The JSON Schema of its input.
    pub input_schema: Map<String, Value>,
}

/// A turn to complete.
#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ModelTurn {
    /// The model, by the provider's name for it; the provider's default
    /// when left out.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub model: Option<String>,
    pub messages: Vec<ModelMessage>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub tools: Vec<ModelTool>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub max_tokens: Option<u32>,
}

/// A tool call the model made.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
pub struct ModelToolCall {
    pub id: String,
    pub name: String,
    pub input: Value,
}

/// What a turn used, for the Budget behaviour (D104).
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ModelUsage {
    pub input_tokens: u64,
    pub output_tokens: u64,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub amount: Option<Amount>,
}

/// Why a turn stopped.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub enum ModelStop {
    End,
    ToolUse,
    MaxTokens,
}

/// A completed turn.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ModelCompletion {
    pub content: String,
    pub tool_calls: Vec<ModelToolCall>,
    pub stop: ModelStop,
    pub usage: ModelUsage,
}

/// Texts to embed (D155): each becomes one vector, under `model` or the
/// provider's default.
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct ModelEmbedRequest {
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub model: Option<String>,
    pub texts: Vec<String>,
}

/// The vectors of an embed request, one per text in order, and what it
/// used.
#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize)]
pub struct ModelEmbedding {
    pub vectors: Vec<Vec<f64>>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub usage: Option<ModelUsage>,
}

/// A model provider.
#[async_trait]
pub trait ModelProvider: Send + Sync {
    async fn complete(&self, turn: &ModelTurn) -> Result<ModelCompletion, BoxError>;

    /// Embeds texts, for search's re-ranking (D155). A provider that
    /// embeds nothing leaves it out, and answers `None`: search runs on
    /// full text alone. It is no operation of the contract, which a
    /// manifest lists, since search asks whether the provider embeds.
    async fn embed(&self, request: &ModelEmbedRequest) -> Result<Option<ModelEmbedding>, BoxError> {
        let _ = request;
        Ok(None)
    }
}
