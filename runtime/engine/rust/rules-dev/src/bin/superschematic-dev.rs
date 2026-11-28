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
//!
//! `move` publishes a store's schemas to runtime mode (D117), a runtime
//! database's first publish, and with `--rows` replays its rows there as
//! creates, each with its own fields and its id, listing the ones refused
//! (`superschematic_rules_runtime::Runtime::publish_from`). It prints the
//! publish's answer as JSON. The store is read and left as it was.
//!
//! ```text
//! superschematic-dev move --to FILE|URL (--db FILE | --postgres URL --schema NAME)
//!                         [--service NAME] [--rows] [--allow HAZARD]...
//! ```

use std::process::ExitCode;
use std::sync::{Arc, Mutex, PoisonError};
use std::time::Duration;

use superschematic_rules::Caller;
use superschematic_rules_dev::server::{dev_callers, router, Options};
use superschematic_rules_dev::Dev;
use superschematic_rules_handler::{Policy, Store};

const USAGE: &str = "usage: superschematic-dev [--db FILE | --memory] [--listen ADDR] [--policy JSON|@FILE] [--as SUBJECT] [--permissions P1,P2] [--postgres URL --schema NAME]\n       superschematic-dev move --to FILE|URL (--db FILE | --postgres URL --schema NAME) [--service NAME] [--rows] [--allow HAZARD]...";

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

const MOVE_USAGE: &str = "usage: superschematic-dev move --to FILE|URL (--db FILE | --postgres URL --schema NAME) [--service NAME] [--rows] [--allow HAZARD]...";

/// What `move` takes: the store, the runtime database it moves to, and the
/// publish's service, rows and allowed hazards.
struct MoveArgs {
    db: Option<String>,
    postgres: Option<String>,
    schema: String,
    to: String,
    service: Option<String>,
    rows: bool,
    allow: Vec<String>,
}

fn move_args(mut given: impl Iterator<Item = String>) -> Result<MoveArgs, String> {
    let mut args = MoveArgs {
        db: None,
        postgres: None,
        schema: "superschematic_dev".to_owned(),
        to: String::new(),
        service: None,
        rows: false,
        allow: Vec::new(),
    };
    while let Some(flag) = given.next() {
        let mut value = || given.next().ok_or_else(|| format!("{flag} needs a value"));
        match flag.as_str() {
            "--db" => args.db = Some(value()?),
            "--postgres" => args.postgres = Some(value()?),
            "--schema" => args.schema = value()?,
            "--to" => args.to = value()?,
            "--service" => args.service = Some(value()?),
            "--rows" => args.rows = true,
            "--allow" => args.allow.push(value()?),
            "--help" | "-h" => return Err(MOVE_USAGE.to_owned()),
            other => return Err(format!("unknown flag {other}\n{MOVE_USAGE}")),
        }
    }
    if args.to.is_empty() {
        return Err(format!(
            "move needs --to, the runtime database\n{MOVE_USAGE}"
        ));
    }
    if args.db.is_some() == args.postgres.is_some() {
        return Err(format!(
            "move reads one store: --db FILE or --postgres URL\n{MOVE_USAGE}"
        ));
    }
    Ok(args)
}

/// Publishes a store's schemas to the runtime database, and its rows with
/// --rows.
fn moved<S: Store>(
    runtime: &mut superschematic_rules_runtime::Runtime,
    store: &mut S,
    mut publish: superschematic_rules_runtime::Publish,
    rows: bool,
) -> Result<serde_json::Value, superschematic_rules::Refusal> {
    if rows {
        return runtime.publish_from(store, &publish);
    }
    publish.documents = superschematic_rules_runtime::development_documents(store)?;
    runtime.publish(&publish)
}

fn run_move(args: MoveArgs) -> Result<serde_json::Value, String> {
    use superschematic_rules_runtime::{Publish, Runtime};
    let refused = |refusal: superschematic_rules::Refusal| refusal.message;
    // A store that is not there is refused before the runtime database is
    // opened, which would create a file.
    if let Some(path) = &args.db {
        if !std::path::Path::new(path).exists() {
            return Err(format!("--db {path}: no such development store"));
        }
    }
    let postgres_url = args.to.starts_with("postgres://") || args.to.starts_with("postgresql://");
    let mut runtime = if postgres_url {
        #[cfg(feature = "postgres")]
        {
            Runtime::postgres(&args.to).map_err(refused)?
        }
        #[cfg(not(feature = "postgres"))]
        {
            return Err("a Postgres runtime database needs the postgres feature".to_owned());
        }
    } else {
        Runtime::open(&args.to).map_err(refused)?
    };
    let publish = Publish {
        service: args.service,
        allow: args.allow,
        ..Publish::default()
    };
    let declarations = superschematic_rules_handler::Declarations::ported();
    match (args.db, args.postgres) {
        (Some(path), _) => {
            let mut store = superschematic_rules_dev::SqliteStore::open(&path, &declarations)
                .map_err(|error| format!("--db {path}: {error}"))?;
            moved(&mut runtime, &mut store, publish, args.rows).map_err(refused)
        }
        (None, Some(url)) => {
            #[cfg(feature = "postgres")]
            {
                let mut store =
                    superschematic_rules_dev::PgStore::open(&url, &args.schema, &declarations)
                        .map_err(refused)?;
                moved(&mut runtime, &mut store, publish, args.rows).map_err(refused)
            }
            #[cfg(not(feature = "postgres"))]
            {
                let _ = url;
                Err("--postgres needs the postgres feature".to_owned())
            }
        }
        (None, None) => Err(MOVE_USAGE.to_owned()),
    }
}

fn main() -> ExitCode {
    let mut given = std::env::args().skip(1).peekable();
    if given.peek().map(String::as_str) == Some("move") {
        given.next();
        let args = match move_args(given) {
            Ok(args) => args,
            Err(message) => {
                eprintln!("{message}");
                return ExitCode::from(2);
            }
        };
        return match run_move(args) {
            Ok(answer) => {
                println!(
                    "{}",
                    serde_json::to_string_pretty(&answer).unwrap_or_default()
                );
                ExitCode::SUCCESS
            }
            Err(message) => {
                eprintln!("superschematic-dev move: {message}");
                ExitCode::FAILURE
            }
        };
    }
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
