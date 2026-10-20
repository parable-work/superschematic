//! The lines a worker's loop logs, as the `logging` feature writes them:
//! the Go and TypeScript workers' messages and fields, in a span naming
//! the queue. It holds one test, in a process of its own, since tracing
//! decides once per process which of its call sites any subscriber hears.

use std::collections::VecDeque;
use std::sync::{Arc, Mutex};
use std::time::{Duration, UNIX_EPOCH};

use async_trait::async_trait;
use superschematic_http_runtime::logging::json_lines;
use superschematic_http_runtime::queue::{Claim, QueueError};
use superschematic_http_runtime::worker::{run_worker, Options, WorkerQueue};
use superschematic_http_runtime::{BoxError, Shutdown};
use tracing_subscriber::layer::SubscriberExt as _;

/// A queue in memory whose first claim fails, then hands out its messages
/// once each, and whose first prune deletes three done messages.
struct Memory {
    claims: Mutex<usize>,
    ready: Mutex<VecDeque<u32>>,
    pruned: Mutex<bool>,
}

#[async_trait]
impl WorkerQueue for Memory {
    type Message = u32;

    fn name(&self) -> &str {
        "OrderPlaced"
    }

    fn lease(&self) -> Duration {
        Duration::from_secs(30)
    }

    async fn claim(&self, n: usize, _lease: Duration) -> Result<Vec<Claim<u32>>, QueueError> {
        let claims = {
            let mut claims = self.claims.lock().unwrap();
            *claims += 1;
            *claims
        };
        if claims == 1 {
            return Err(QueueError::Failed("the database is down".into()));
        }
        let mut ready = self.ready.lock().unwrap();
        let taken = n.min(ready.len());
        Ok(ready
            .drain(..taken)
            .map(|message| Claim {
                id: format!("m{message}"),
                token: format!("t{message}"),
                attempt: 1,
                message,
            })
            .collect())
    }

    async fn extend(&self, _id: &str, _token: &str, _lease: Duration) -> Result<(), QueueError> {
        Ok(())
    }

    async fn complete(&self, _id: &str, _token: &str) -> Result<(), QueueError> {
        Ok(())
    }

    async fn fail(&self, _id: &str, _token: &str, _cause: &str) -> Result<bool, QueueError> {
        Ok(false)
    }

    async fn release(&self, _id: &str, _token: &str) -> Result<(), QueueError> {
        Ok(())
    }

    async fn prune(&self, _limit: usize) -> Result<u64, QueueError> {
        let mut pruned = self.pruned.lock().unwrap();
        let n = if *pruned { 0 } else { 3 };
        *pruned = true;
        Ok(n)
    }
}

#[tokio::test(start_paused = true)]
async fn each_outcome_is_a_line_with_the_go_and_typescript_workers_fields() {
    let lines = Arc::new(Mutex::new(Vec::new()));
    let sink = Arc::clone(&lines);
    let layer = json_lines("shop-stack", "shop-orders-fulfil-orders")
        .with_clock(|| UNIX_EPOCH)
        .with_writer(move |line| sink.lock().unwrap().push(line.to_string()));
    let _default = tracing::subscriber::set_default(tracing_subscriber::registry().with(layer));

    let queue = Memory {
        claims: Mutex::new(0),
        ready: Mutex::new(VecDeque::from([1, 2])),
        pruned: Mutex::new(false),
    };
    let shutdown = Shutdown::new();
    let run = tokio::spawn(run_worker(
        queue,
        |n: u32| async move {
            if n == 2 {
                return Err::<(), BoxError>("out of stock".into());
            }
            Ok(())
        },
        Options {
            concurrency: 2,
            ..Options::default()
        },
        shutdown.clone(),
    ));
    tokio::time::sleep(Duration::from_millis(1500)).await;
    shutdown.cancel();
    run.await.unwrap().unwrap();
    let line = |level: &str, msg: &str, rest: &str| {
        format!(
            r#"{{"level":"{level}","time":"1970-01-01T00:00:00.000Z","msg":"{msg}","stack":"shop-stack","server":"shop-orders-fulfil-orders","queue":"OrderPlaced"{rest}}}"#
        )
    };
    assert_eq!(
        lines.lock().unwrap().as_slice(),
        [
            line(
                "error",
                "claim failed",
                r#","retryIn":1.0,"error":"the database is down""#
            ),
            line("info", "done messages pruned", r#","pruned":3"#),
            line(
                "info",
                "message handled",
                r#","message":"m1","attempt":1,"took":0.0"#
            ),
            line(
                "warn",
                "message failed and will be retried",
                r#","message":"m2","attempt":1,"took":0.0,"error":"out of stock""#
            ),
            line("info", "worker stopped", ""),
        ]
    );
}
