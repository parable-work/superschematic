//! The sandbox provider (D159): where an agent run's instance runs
//! (D158). The dispatcher starts a fresh instance per run with the run's
//! token, the MCP URL and the credential references, and cancels it when
//! the run stops; only what the instance wrote through the API outlives
//! it.

use async_trait::async_trait;
use serde::{Deserialize, Serialize};
use std::collections::BTreeMap;
use std::time::SystemTime;

use super::SecretValue;
use crate::BoxError;

/// The limits a provider enforces on an instance.
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct SandboxLimits {
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub wall_seconds: Option<u64>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub cpu_seconds: Option<u64>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub memory_mb: Option<u64>,
}

/// What one run's instance gets.
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct SandboxSpec {
    /// The run the instance serves, which its records name.
    pub run_id: String,
    /// What the instance runs: an image for a container platform, a
    /// program for a local process.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub image: Option<String>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub command: Vec<String>,
    /// Plain settings: the MCP URL, the definition's version.
    #[serde(skip_serializing_if = "BTreeMap::is_empty")]
    pub env: BTreeMap<String, String>,
    /// Secrets the instance reads, the run token among them, which the
    /// provider passes without logging; they print masked.
    #[serde(skip_serializing_if = "BTreeMap::is_empty")]
    pub secrets: BTreeMap<String, SecretValue>,
    pub limits: SandboxLimits,
}

/// A started instance.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct SandboxInstance {
    pub id: String,
    #[serde(with = "super::time")]
    pub started_at: SystemTime,
}

/// Where an instance stands.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum SandboxState {
    Starting,
    Running,
    Succeeded,
    Failed,
    Cancelled,
}

/// An instance's state.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct SandboxStatus {
    pub id: String,
    pub state: SandboxState,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub exit_code: Option<i32>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub detail: Option<String>,
}

/// An amount a provider charges, as a decimal string, and its currency.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct Amount {
    pub value: String,
    pub currency: String,
}

/// What an instance cost so far, for the Budget behaviour (D104).
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct SandboxCost {
    pub id: String,
    pub wall_seconds: f64,
    pub cpu_seconds: f64,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub amount: Option<Amount>,
}

/// A sandbox provider.
#[async_trait]
pub trait SandboxProvider: Send + Sync {
    async fn start(&self, spec: &SandboxSpec) -> Result<SandboxInstance, BoxError>;
    /// Stops the instance; cancelling one that has stopped does nothing.
    async fn cancel(&self, id: &str) -> Result<(), BoxError>;
    async fn status(&self, id: &str) -> Result<SandboxStatus, BoxError>;
    async fn cost(&self, id: &str) -> Result<SandboxCost, BoxError>;
}
