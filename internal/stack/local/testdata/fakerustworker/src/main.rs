//! A fake Rust worker that honours the contract of a generated worker
//! entrypoint (docs/stack-model.md, section 8.8) for the local target's
//! integration test (D65): it serves nothing and claims nothing, says it
//! runs with the concurrency WORKER_CONCURRENCY gives it, waits until
//! SIGTERM, and then says it stopped and exits 0, as a generated worker
//! releases its messages and exits. It exits 2 on a PORT, which no worker
//! is given. It uses the standard library alone.

use std::env;
use std::process::exit;
use std::thread;
use std::time::Duration;

fn main() {
    if let Ok(port) = env::var("PORT") {
        eprintln!("fake Rust worker got PORT={port}; a worker serves nothing");
        exit(2);
    }
    on_sigterm();
    let concurrency = env::var("WORKER_CONCURRENCY").unwrap_or_default();
    println!("fake Rust worker running, concurrency {concurrency}");
    loop {
        thread::sleep(Duration::from_secs(3600));
    }
}

/// on_sigterm makes SIGTERM print that the worker stopped, then exit 0.
#[cfg(unix)]
fn on_sigterm() {
    extern "C" {
        fn signal(signum: i32, handler: extern "C" fn(i32)) -> usize;
        fn write(fd: i32, buf: *const std::ffi::c_void, count: usize) -> isize;
        fn _exit(status: i32) -> !;
    }
    extern "C" fn stop(_: i32) {
        let message = b"fake Rust worker stopped\n";
        // write and _exit are safe in a signal handler; println! is not.
        unsafe {
            write(1, message.as_ptr().cast(), message.len());
            _exit(0);
        }
    }
    const SIGTERM: i32 = 15;
    unsafe {
        signal(SIGTERM, stop);
    }
}

#[cfg(not(unix))]
fn on_sigterm() {}
