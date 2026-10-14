//! `Shutdown::on_signals` in a process of its own, this test binary, which
//! sends itself one SIGTERM. It holds one test: a second signal would end
//! the process, as a second signal ends a worker.

#![cfg(unix)]

use std::process::Command;
use std::time::Duration;

use superschematic_http_runtime::Shutdown;

#[tokio::test]
async fn the_first_sigterm_cancels_the_shutdown() {
    let shutdown = Shutdown::on_signals().unwrap();
    let child = shutdown.child();
    assert!(!shutdown.is_cancelled());
    let sent = Command::new("kill")
        .args(["-TERM", &std::process::id().to_string()])
        .status()
        .unwrap();
    assert!(sent.success());
    tokio::time::timeout(Duration::from_secs(5), shutdown.cancelled())
        .await
        .expect("the signal cancels the shutdown");
    assert!(child.is_cancelled());
}
