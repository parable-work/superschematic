//! `superschematic-dev`: development mode as a server you run (D117). It
//! serves the handler's routes, the event stream and the MCP endpoint over
//! HTTP on a store of its own: a SQLite file, a database in memory, or,
//! with the `postgres` feature, a schema of a Postgres database. It runs
//! the queues after each request and the deadlines as they fall due.
//!
//! ```text
//! superschematic-dev [--db FILE | --memory] [--listen ADDR] [--policy JSON|@FILE]
//!                    [--as SUBJECT] [--permissions P1,P2]
//!                    [--postgres URL --schema NAME]
//! ```
//!
//! A request's caller is the subject `X-Superschematic-Subject` names, with
//! the permissions `X-Superschematic-Permissions` lists, or the `--as`
//! caller when it names none; with neither, it is 401. Anyone who reaches
//! the server is whoever they say, so it listens on 127.0.0.1 unless told
//! otherwise.

use std::process::ExitCode;
use std::sync::{Arc, Mutex, PoisonError};
use std::time::Duration;

use superschematic_rules::Caller;
use superschematic_rules_dev::server::{dev_callers, router, Options};
use superschematic_rules_dev::Dev;
use superschematic_rules_handler::{Policy, Store};

const USAGE: &str = "usage: superschematic-dev [--db FILE | --memory] [--listen ADDR] [--policy JSON|@FILE] [--as SUBJECT] [--permissions P1,P2] [--postgres URL --schema NAME]";

struct Args {
    db: Option<String>,
    memory: bool,
    listen: String,
    policy: Policy,
    subject: Option<String>,
    permissions: Vec<String>,
    postgres: Option<String>,
    schema: String,
}

fn args() -> Result<Args, String> {
    let mut args = Args {
        db: None,
        memory: false,
        listen: "127.0.0.1:4500".to_owned(),
        policy: Policy::AllowAll,
        subject: Some("developer".to_owned()),
        permissions: Vec::new(),
        postgres: None,
        schema: "superschematic_dev".to_owned(),
    };
    let mut given = std::env::args().skip(1);
    while let Some(flag) = given.next() {
        let mut value = || given.next().ok_or_else(|| format!("{flag} needs a value"));
        match flag.as_str() {
            "--db" => args.db = Some(value()?),
            "--memory" => args.memory = true,
            "--listen" => args.listen = value()?,
            "--policy" => {
                let text = value()?;
                let text = match text.strip_prefix('@') {
                    Some(path) => std::fs::read_to_string(path)
                        .map_err(|error| format!("--policy {path}: {error}"))?,
                    None => text,
                };
                let json = serde_json::from_str(&text)
                    .or_else(|_| serde_json::from_str(&format!("\"{text}\"")))
                    .map_err(|error| format!("--policy is no JSON: {error}"))?;
                args.policy = Policy::from_json(&json)?;
            }
            "--as" => {
                let subject = value()?;
                args.subject = (!subject.is_empty()).then_some(subject);
            }
            "--permissions" => {
                args.permissions = value()?
                    .split(',')
                    .map(str::trim)
                    .filter(|permission| !permission.is_empty())
                    .map(str::to_owned)
                    .collect();
            }
            "--postgres" => args.postgres = Some(value()?),
            "--schema" => args.schema = value()?,
            "--help" | "-h" => return Err(USAGE.to_owned()),
            other => return Err(format!("unknown flag {other}\n{USAGE}")),
        }
    }
    Ok(args)
}

async fn run<S: Store + Send + 'static>(dev: Dev<S>, args: Args) -> Result<(), String> {
    let dev = Arc::new(Mutex::new(dev.with_policy(args.policy)));
    let default = args
        .subject
        .map(|subject| Caller::new(subject, args.permissions));
    let app = router(Arc::clone(&dev), Options::new(dev_callers(default)));
    // The deadlines fall due on the clock, not on a request: the runner
    // runs what is due each second.
    let runner = Arc::clone(&dev);
    tokio::spawn(async move {
        loop {
            tokio::time::sleep(Duration::from_secs(1)).await;
            let dev = Arc::clone(&runner);
            let ran = tokio::task::spawn_blocking(move || {
                dev.lock().unwrap_or_else(PoisonError::into_inner).run_due()
            })
            .await;
            if let Ok(Err(refusal)) = ran {
                eprintln!("superschematic-dev: the runner: {}", refusal.message);
            }
        }
    });
    let listener = tokio::net::TcpListener::bind(&args.listen)
        .await
        .map_err(|error| format!("listen on {}: {error}", args.listen))?;
    eprintln!(
        "superschematic-dev: serving http://{}/namespaces/default (MCP at /namespaces/default/mcp)",
        listener
            .local_addr()
            .map_or(args.listen.clone(), |addr| addr.to_string())
    );
    axum::serve(listener, app)
        .with_graceful_shutdown(shutdown())
        .await
        .map_err(|error| format!("serve: {error}"))
}

// shutdown waits for SIGINT, or SIGTERM where there is one.
async fn shutdown() {
    #[cfg(unix)]
    {
        let Ok(mut term) =
            tokio::signal::unix::signal(tokio::signal::unix::SignalKind::terminate())
        else {
            let _ = tokio::signal::ctrl_c().await;
            return;
        };
        tokio::select! {
            _ = tokio::signal::ctrl_c() => {}
            _ = term.recv() => {}
        }
    }
    #[cfg(not(unix))]
    {
        let _ = tokio::signal::ctrl_c().await;
    }
}

fn main() -> ExitCode {
    let args = match args() {
        Ok(args) => args,
        Err(message) => {
            eprintln!("{message}");
            return ExitCode::from(2);
        }
    };
    let runtime = match tokio::runtime::Builder::new_multi_thread()
        .enable_all()
        .build()
    {
        Ok(runtime) => runtime,
        Err(error) => {
            eprintln!("superschematic-dev: {error}");
            return ExitCode::FAILURE;
        }
    };
    let served = runtime.block_on(async {
        if let Some(url) = args.postgres.clone() {
            #[cfg(feature = "postgres")]
            {
                let schema = args.schema.clone();
                let dev = tokio::task::spawn_blocking(move || Dev::postgres(&url, &schema))
                    .await
                    .map_err(|error| error.to_string())?
                    .map_err(|refusal| refusal.message)?;
                return run(dev, args).await;
            }
            #[cfg(not(feature = "postgres"))]
            {
                let _ = url;
                return Err("--postgres needs the postgres feature".to_owned());
            }
        }
        let dev = match (&args.db, args.memory) {
            (Some(path), false) => Dev::open(path),
            (None, _) | (Some(_), true) => Dev::memory(),
        }
        .map_err(|error| format!("the store: {error}"))?;
        run(dev, args).await
    });
    match served {
        Ok(()) => ExitCode::SUCCESS,
        Err(message) => {
            eprintln!("superschematic-dev: {message}");
            ExitCode::FAILURE
        }
    }
}
