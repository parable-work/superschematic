//! A fake Rust job that honours the contract of a generated job entrypoint
//! (docs/stack-model.md, section 8.7) for the local target's integration
//! test (D65): it runs once, serves nothing, and exits with the status
//! JOB_EXIT names, 0 for a run that succeeds and 1 for one that fails, as
//! a generated job exits 1 when its method fails. It says how it ended,
//! with GREETING, a config field of its API's, and exits 2 on a PORT,
//! which no job is given, or a JOB_EXIT that is neither. It uses the
//! standard library alone.

use std::env;
use std::process::exit;

fn main() {
    if let Ok(port) = env::var("PORT") {
        eprintln!("fake Rust job got PORT={port}; a job serves nothing");
        exit(2);
    }
    let greeting = env::var("GREETING").unwrap_or_default();
    match env::var("JOB_EXIT").as_deref() {
        Ok("0") => println!("fake Rust job done, greeting {greeting}"),
        Ok("1") => {
            println!("fake Rust job failed, greeting {greeting}");
            exit(1);
        }
        other => {
            eprintln!("fake Rust job: JOB_EXIT is {other:?}, not 0 or 1");
            exit(2);
        }
    }
}
