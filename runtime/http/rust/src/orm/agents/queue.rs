//! The request queue as its worker claims it (D158, #622): the queue's
//! claims, whose prune deletes the transcripts past their retention too,
//! as the TypeScript worker's prune calls `agents.prune`, and as the
//! notifier's queue prunes its deliveries.

use std::time::Duration;

use async_trait::async_trait;

use super::dispatcher::{AgentRunRequest, Agents};
use crate::queue::{Claim, PostgresQueue, QueueError, Slot};
use crate::worker::WorkerQueue;

/// The request queue as a worker claims it ([`Agents::dispatch_queue`]).
pub struct AgentsQueue {
    queue: PostgresQueue<AgentRunRequest>,
    agents: Agents,
}

impl AgentsQueue {
    /// The queue's claims.
    pub fn queue(&self) -> &PostgresQueue<AgentRunRequest> {
        &self.queue
    }
}

impl Agents {
    /// The request queue as its worker claims it: [`Agents::worker_queue`],
    /// whose prune deletes the transcripts past their retention too
    /// ([`Agents::prune`]).
    pub fn dispatch_queue(&self) -> AgentsQueue {
        AgentsQueue {
            queue: self.worker_queue(),
            agents: self.clone(),
        }
    }
}

#[async_trait]
impl WorkerQueue for AgentsQueue {
    type Message = AgentRunRequest;

    fn name(&self) -> &str {
        self.queue.name()
    }

    fn lease(&self) -> Duration {
        self.queue.lease()
    }

    async fn claim(
        &self,
        n: usize,
        lease: Duration,
    ) -> Result<Vec<Claim<AgentRunRequest>>, QueueError> {
        self.queue.claim(n, lease).await
    }

    async fn extend(&self, id: &str, token: &str, lease: Duration) -> Result<(), QueueError> {
        self.queue.extend(id, token, lease).await
    }

    async fn complete(&self, id: &str, token: &str) -> Result<(), QueueError> {
        self.queue.complete(id, token).await
    }

    async fn fail(&self, id: &str, token: &str, cause: &str) -> Result<bool, QueueError> {
        self.queue.fail(id, token, cause).await
    }

    async fn release(&self, id: &str, token: &str) -> Result<(), QueueError> {
        self.queue.release(id, token).await
    }

    async fn prune(&self, limit: usize) -> Result<u64, QueueError> {
        let done = self.queue.prune(limit).await?;
        let transcripts = self
            .agents
            .prune(u32::try_from(limit).unwrap_or(u32::MAX))
            .await
            .map_err(|err| QueueError::Failed(err.to_string()))?;
        Ok(done + transcripts)
    }

    async fn slot(&self, slots: usize) -> Result<Option<Slot>, QueueError> {
        self.queue.slot(slots).await
    }

    async fn next_due(&self) -> Result<Option<Duration>, QueueError> {
        self.queue.next_due().await
    }
}
