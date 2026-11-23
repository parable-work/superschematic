//! Where a publish's plan is applied (D117): the environment's migration
//! runner, the one a deploy uses, and never a serving connection.
//!
//! In process, the plan runs before the publish returns: the Rust runner on
//! a connection of its own to a SQLite file (D116), or the Go runner's
//! binary beside a Postgres database. That is the default ([`Runtime`]'s
//! own), and it holds the database's requests off while it runs.
//!
//! A platform whose runner is a job (gcp's migration job, D46) applies the
//! plan on another machine. A [`JobRunner`] writes the job's documents where
//! the job reads them ([`JobStore`]): the plan, runtime mode's record of the
//! publish, and a job document (`superschematic-migrate job`) that names
//! them, the database and the servers' roles. It starts an execution
//! ([`Launch`]) and returns at once. The publish then polls the model the
//! database records until it is the plan's `to` with no plan in progress
//! and the record is this publish's, or until the execution reports a
//! failure, for at most the runner's timeout. It holds no lock while it
//! waits: the plan in progress, which the runner records before its first
//! step, refuses the servers' requests as `busy`, and the runner writes the
//! record and the servers' privileges on the new tables in the transaction
//! that finishes the plan, so no server reads the new model before it may
//! serve it.
//!
//! [`Runtime`]: crate::Runtime

use std::io::Read as _;
use std::path::PathBuf;
use std::process::{Child, Command, Stdio};
use std::time::Duration;

use serde_json::{json, Map, Value};
use superschematic_rules::Refusal;

use crate::values::{internal, Dialect};

/// One publish's plan, as a runner applies it.
#[derive(Clone, Copy, Debug)]
pub struct Applying<'a> {
    /// The DB service the plan migrates.
    pub service: &'a str,
    pub dialect: Dialect,
    /// The plan document, as the planner wrote it.
    pub plan: &'a [u8],
    /// The plan's hash, and the models it goes from and to.
    pub plan_hash: &'a str,
    pub from: &'a str,
    pub to: &'a str,
    /// Runtime mode's record of the publish, a record document
    /// (`superschematic-migrate apply --record`), which the transaction that
    /// finishes the plan writes.
    pub record: &'a [u8],
}

/// How a runner's application stands when it returns.
pub enum Applied {
    /// The plan finished, its record with it.
    Done,
    /// A job applies the plan elsewhere: the publish waits on the model the
    /// database records, and asks the execution whether it failed.
    Started(Started),
}

/// A job's execution a publish waits for: how long at most, and how long
/// it pauses between its reads of the model.
pub struct Started {
    pub execution: Box<dyn Execution>,
    pub timeout: Duration,
    pub poll: Duration,
}

/// A job's execution, as a waiting publish asks after it.
pub trait Execution: Send {
    /// The execution's failure, once it has failed: the runner's error, as
    /// its last line says it; `None` while it runs and once it succeeded.
    fn failure(&mut self) -> Result<Option<String>, Refusal>;

    /// The execution's name, for a message.
    fn name(&self) -> String;
}

/// The environment's migration runner, which applies a publish's plan.
pub trait PlanRunner: Send {
    /// Applies the plan: to its end before returning, or by starting a job
    /// that does.
    fn apply(&mut self, applying: &Applying<'_>) -> Result<Applied, Refusal>;
}

/// Where a job reads its documents: the state bucket on gcp (D46's job
/// documents), a directory standing in for it in a test.
pub trait JobStore: Send {
    /// Writes a document at a path under the store's prefix, and returns
    /// the reference the job reads it by: a path, a `gs://` URL.
    fn put(&mut self, path: &str, body: &[u8]) -> Result<String, Refusal>;
}

/// A directory standing in for the state bucket: a document is a file
/// under it, read by its path.
#[derive(Clone, Debug)]
pub struct Directory(pub PathBuf);

impl JobStore for Directory {
    fn put(&mut self, path: &str, body: &[u8]) -> Result<String, Refusal> {
        let file = self.0.join(path);
        if let Some(dir) = file.parent() {
            std::fs::create_dir_all(dir).map_err(|error| {
                internal(format!(
                    "cannot make the directory {}: {error}",
                    dir.display()
                ))
            })?;
        }
        std::fs::write(&file, body)
            .map_err(|error| internal(format!("cannot write {}: {error}", file.display())))?;
        Ok(file.display().to_string())
    }
}

/// What starts a job's execution: on gcp an execution of the stack's
/// migration job (D46), here a process.
pub trait Launch: Send {
    /// Starts an execution that runs `superschematic-migrate job --job
    /// <job>`, the job document's reference.
    fn launch(&mut self, job: &str) -> Result<Box<dyn Execution>, Refusal>;
}

/// A job that is a process of this machine: the Go runner's binary,
/// `superschematic-migrate job --job <reference>`, which reads its
/// documents from the store as a job on another machine does.
#[derive(Clone, Debug)]
pub struct ProcessLaunch {
    pub migrate: PathBuf,
}

/// A running process, as an execution.
struct Process {
    child: Option<Child>,
    name: String,
    failed: Option<String>,
}

impl Launch for ProcessLaunch {
    fn launch(&mut self, job: &str) -> Result<Box<dyn Execution>, Refusal> {
        let child = Command::new(&self.migrate)
            .args(["job", "--job", job])
            .stdin(Stdio::null())
            .stdout(Stdio::null())
            .stderr(Stdio::piped())
            .spawn()
            .map_err(|error| {
                internal(format!(
                    "cannot run {} (name the runner's binary): {error}",
                    self.migrate.display()
                ))
            })?;
        Ok(Box::new(Process {
            name: format!("process {}", child.id()),
            child: Some(child),
            failed: None,
        }))
    }
}

impl Execution for Process {
    fn failure(&mut self) -> Result<Option<String>, Refusal> {
        if self.failed.is_some() {
            return Ok(self.failed.clone());
        }
        let Some(child) = &mut self.child else {
            return Ok(None);
        };
        let Some(status) = child
            .try_wait()
            .map_err(|error| internal(format!("{} did not report: {error}", self.name)))?
        else {
            return Ok(None);
        };
        let mut stderr = String::new();
        if let Some(mut pipe) = child.stderr.take() {
            let _ = pipe.read_to_string(&mut stderr);
        }
        self.child = None;
        if status.success() {
            return Ok(None);
        }
        self.failed =
            Some(runner_error(&stderr).unwrap_or_else(|| format!("exited with {status}")));
        Ok(self.failed.clone())
    }

    fn name(&self) -> String {
        self.name.clone()
    }
}

impl Drop for Process {
    fn drop(&mut self) {
        // A publish that stopped waiting leaves the job to finish.
        if let Some(child) = &mut self.child {
            let _ = child.try_wait();
        }
    }
}

/// The line the runner writes when it fails, the last that begins with its
/// name, else the first line, as gcp's deploy reads it (D46, amended).
pub fn runner_error(stderr: &str) -> Option<String> {
    let lines: Vec<&str> = stderr
        .lines()
        .map(str::trim)
        .filter(|line| !line.is_empty())
        .collect();
    lines
        .iter()
        .rev()
        .find(|line| line.starts_with("superschematic-migrate: "))
        .or_else(|| lines.first())
        .map(|line| (*line).to_owned())
}

/// How a job reaches the database: at a URL, or on a Cloud SQL instance
/// through the Cloud SQL Go connector as an IAM database user (D46).
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum JobDatabase {
    Url(String),
    CloudSql {
        /// The instance's connection name, `project:region:instance`.
        instance: String,
        /// The migrator's IAM database user.
        user: String,
        /// The database's name on the instance.
        database: String,
    },
}

/// The default time a publish waits for its job: as long as one execution
/// of D46's job may run.
pub const JOB_TIMEOUT: Duration = Duration::from_secs(3600);

/// The default pause between a waiting publish's reads of the model.
pub const JOB_POLL: Duration = Duration::from_secs(2);

/// A runner whose plan a job applies on another machine (D46's job): the
/// publish writes the plan, the record and a job document into the store,
/// under `superschematic/migrations/runtime/<service>/<plan hash>/`, and
/// starts an execution of the job. The job gives each server role the read
/// and write privileges D46's privilege step gives, on the new tables too,
/// and the reads of the runner's state and the record a runtime server
/// follows a publish by.
pub struct JobRunner {
    store: Box<dyn JobStore>,
    launch: Box<dyn Launch>,
    database: JobDatabase,
    servers: Option<Vec<String>>,
    /// How long a publish waits for the job to finish its plan.
    pub timeout: Duration,
    /// How long it pauses between its reads of the model.
    pub poll: Duration,
}

impl JobRunner {
    /// A runner that writes into `store` and starts executions with
    /// `launch`, whose job reaches the database as `database` says.
    pub fn new(
        store: impl JobStore + 'static,
        launch: impl Launch + 'static,
        database: JobDatabase,
    ) -> Self {
        JobRunner {
            store: Box::new(store),
            launch: Box::new(launch),
            database,
            servers: None,
            timeout: JOB_TIMEOUT,
            poll: JOB_POLL,
        }
    }

    /// The runner with the database roles of the servers that serve the
    /// database, which the job gives their privileges to and takes them
    /// back from every other role. Without it the job changes none.
    pub fn with_servers(mut self, roles: impl IntoIterator<Item = impl Into<String>>) -> Self {
        self.servers = Some(roles.into_iter().map(Into::into).collect());
        self
    }

    /// The runner with how long a publish waits for its job and pauses
    /// between its reads of the model.
    pub fn with_wait(mut self, timeout: Duration, poll: Duration) -> Self {
        self.timeout = timeout;
        self.poll = poll;
        self
    }

    /// The job document of one publish: the phase `all`, the database, the
    /// plan and the record beside it, and the servers' privileges.
    pub fn job_document(&self, service: &str) -> Value {
        let mut database = Map::new();
        database.insert("service".into(), json!(service));
        let mut job = Map::new();
        job.insert("version".into(), json!(1));
        job.insert("phase".into(), json!("all"));
        match &self.database {
            JobDatabase::Url(url) => {
                database.insert("databaseUrl".into(), json!(url));
            }
            JobDatabase::CloudSql {
                instance,
                user,
                database: name,
            } => {
                job.insert(
                    "cloudSql".into(),
                    json!({"instance": instance, "user": user}),
                );
                database.insert("database".into(), json!(name));
            }
        }
        database.insert("plan".into(), json!(PLAN_DOCUMENT));
        database.insert("record".into(), json!(RECORD_DOCUMENT));
        if let Some(servers) = &self.servers {
            database.insert(
                "privileges".into(),
                json!({"readWrite": servers, "readState": true}),
            );
        }
        job.insert("databases".into(), json!([database]));
        Value::Object(job)
    }
}

/// The documents of one publish's job, beside its job document.
const PLAN_DOCUMENT: &str = "plan.json";
const RECORD_DOCUMENT: &str = "record.json";

/// Where a publish's documents go in the store.
pub fn job_prefix(service: &str, plan_hash: &str) -> String {
    format!("superschematic/migrations/runtime/{service}/{plan_hash}/")
}

impl PlanRunner for JobRunner {
    fn apply(&mut self, applying: &Applying<'_>) -> Result<Applied, Refusal> {
        if let (Dialect::Sqlite, Some(_)) = (applying.dialect, &self.servers) {
            return Err(internal(
                "a SQLite database has no roles to give privileges to: leave the servers out",
            ));
        }
        let prefix = job_prefix(applying.service, applying.plan_hash);
        self.store
            .put(&format!("{prefix}{PLAN_DOCUMENT}"), applying.plan)?;
        self.store
            .put(&format!("{prefix}{RECORD_DOCUMENT}"), applying.record)?;
        let job = serde_json::to_vec_pretty(&self.job_document(applying.service))
            .map_err(|error| internal(format!("the job document is no JSON: {error}")))?;
        let reference = self.store.put(&format!("{prefix}job.json"), &job)?;
        let execution = self.launch.launch(&reference)?;
        Ok(Applied::Started(Started {
            execution,
            timeout: self.timeout,
            poll: self.poll,
        }))
    }
}

/// The in-process runner of a Postgres database: the Go runner's binary,
/// `superschematic-migrate apply --plan - --record <file>`, beside it.
#[cfg(feature = "postgres")]
pub(crate) fn apply_postgres(
    migrate: &std::path::Path,
    url: &str,
    plan: &[u8],
    record: &[u8],
) -> Result<(), Refusal> {
    let file = std::env::temp_dir().join(format!(
        "superschematic-record-{}.json",
        uuid::Uuid::new_v4()
    ));
    std::fs::write(&file, record)
        .map_err(|error| internal(format!("cannot write {}: {error}", file.display())))?;
    let ran = crate::conn::run_binary(
        migrate,
        &[
            "apply",
            "--plan",
            "-",
            "--record",
            &file.display().to_string(),
            "--database-url",
            url,
        ],
        plan,
    );
    let _ = std::fs::remove_file(&file);
    let (ok, stdout, stderr) = ran?;
    if ok {
        return Ok(());
    }
    let said = if stderr.is_empty() {
        String::from_utf8_lossy(&stdout).trim().to_owned()
    } else {
        runner_error(&stderr).unwrap_or(stderr)
    };
    Err(crate::conn::apply_refusal(said))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_runners_error_is_its_last_line_of_its_own() {
        let stderr = "applying plan abc\nsuperschematic-migrate: step 2: boom\n  at statement x\n";
        assert_eq!(
            runner_error(stderr).as_deref(),
            Some("superschematic-migrate: step 2: boom")
        );
        assert_eq!(
            runner_error("panic: oh\nstack").as_deref(),
            Some("panic: oh")
        );
        assert_eq!(runner_error("  \n"), None);
    }

    #[test]
    fn a_job_document_names_the_database_the_plan_the_record_and_the_servers() {
        let runner = JobRunner::new(
            Directory(PathBuf::from("/tmp")),
            ProcessLaunch {
                migrate: PathBuf::from("superschematic-migrate"),
            },
            JobDatabase::CloudSql {
                instance: "p:us-central1:i".into(),
                user: "migrator@p.iam".into(),
                database: "tasks".into(),
            },
        )
        .with_servers(["api@p.iam"]);
        assert_eq!(
            runner.job_document("tasks-db"),
            json!({"version": 1, "phase": "all",
                "cloudSql": {"instance": "p:us-central1:i", "user": "migrator@p.iam"},
                "databases": [{"service": "tasks-db", "database": "tasks", "plan": "plan.json", "record": "record.json",
                    "privileges": {"readWrite": ["api@p.iam"], "readState": true}}]})
        );
    }
}
