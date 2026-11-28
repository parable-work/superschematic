//! A runtime environment whose migration runner is a job (D117, D46): the
//! publish writes the plan, its record and a job document into a directory
//! standing in for the state bucket, a fake job (the Go runner's binary,
//! `superschematic-migrate job`, as a process of its own) reads them from
//! there and applies the plan, and the publish waits on the model the
//! database records. On Postgres the job connects as a migrator role that
//! owns the tables, and the servers as a role with D46's grants alone.

mod common;

use std::path::{Path, PathBuf};
use std::time::Duration;

use serde_json::{json, Value};
use superschematic_rules::{Code, Refusal};
use superschematic_rules_runtime::{
    Binaries, Directory, Execution, JobDatabase, JobRunner, Launch, ProcessLaunch, Publish, Runtime,
};

use common::{binaries, skipped};

fn scratch(name: &str) -> PathBuf {
    let dir = Path::new(env!("CARGO_TARGET_TMPDIR"))
        .join("rules-runtime-job")
        .join(format!("{name}-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&dir);
    std::fs::create_dir_all(&dir).expect("the test's directory");
    dir
}

/// A job runner over a directory, whose job is a process of the Go runner
/// that reaches the database as `database` says.
fn job_runner(binaries: &Binaries, bucket: &Path, database: JobDatabase) -> JobRunner {
    JobRunner::new(
        Directory(bucket.to_owned()),
        ProcessLaunch {
            migrate: binaries.migrate.clone(),
        },
        database,
    )
    .with_wait(Duration::from_secs(120), Duration::from_millis(50))
}

/// The SQLite file a job applies plans to, each server opening it with a
/// job runner of its own.
fn sqlite_opener(path: PathBuf, bucket: PathBuf) -> impl Fn() -> Runtime {
    move || {
        let binaries = binaries().expect("the binaries");
        let url = path.display().to_string();
        Runtime::open(&path)
            .expect("the file opens")
            .with_plan_runner(job_runner(&binaries, &bucket, JobDatabase::Url(url)))
    }
}

/// The scenarios a runtime environment runs, with the plan applied by the
/// job on the file.
#[test]
fn a_job_applies_the_publishes_of_a_sqlite_file() {
    let Some(binaries) = binaries() else {
        return skipped();
    };
    let dir = scratch("sqlite");
    let bucket = dir.join("bucket");
    common::publish_and_extend(
        &sqlite_opener(dir.join("extend.db"), bucket.clone()),
        &binaries,
    );
    common::rename(
        &sqlite_opener(dir.join("rename.db"), bucket.clone()),
        &binaries,
    );
    common::behavior_tables(
        &sqlite_opener(dir.join("behaviors.db"), bucket.clone()),
        &binaries,
    );

    // The job read its plan, the record and its job document from the
    // bucket, never from stdin.
    let jobs: Vec<PathBuf> = files(&bucket)
        .into_iter()
        .filter(|path| path.ends_with("job.json"))
        .collect();
    assert!(jobs.len() >= 5, "{jobs:?}");
    let job: Value =
        serde_json::from_slice(&std::fs::read(&jobs[0]).expect("a job document")).expect("JSON");
    let database = &job["databases"][0];
    assert_eq!(database["plan"], "plan.json", "{job}");
    assert_eq!(database["record"], "record.json", "{job}");
    let beside = jobs[0].parent().expect("its directory");
    assert!(beside.join("plan.json").exists() && beside.join("record.json").exists());
}

fn files(dir: &Path) -> Vec<PathBuf> {
    let mut out = Vec::new();
    for entry in std::fs::read_dir(dir).into_iter().flatten().flatten() {
        let path = entry.path();
        if path.is_dir() {
            out.extend(files(&path));
        } else {
            out.push(path);
        }
    }
    out.sort();
    out
}

/// A job that fails fails the publish with the runner's own error line,
/// and the database serves what it served.
#[test]
fn a_failed_job_fails_the_publish_with_the_runners_error() {
    let Some(binaries) = binaries() else {
        return skipped();
    };
    let dir = scratch("failed");
    let path = dir.join("tasks.db");
    let mut runtime =
        sqlite_opener(path.clone(), dir.join("bucket"))().with_binaries(binaries.clone());
    runtime
        .publish(&Publish::of(vec![common::v1()]))
        .expect("v1 publishes");
    let task = common::create(&mut runtime, "Task", json!({"title": "Sand it"}));

    // A job that reaches no database.
    let mut broken = Runtime::open(&path)
        .expect("the file opens")
        .with_binaries(binaries.clone())
        .with_plan_runner(job_runner(
            &binaries,
            &dir.join("bucket"),
            JobDatabase::Url(dir.join("missing/dir/tasks.db").display().to_string()),
        ));
    let refused = broken
        .publish(&Publish::of(vec![common::v2()]))
        .expect_err("the job fails");
    assert_eq!(refused.class, "PlanApplyError", "{refused:?}");
    assert!(
        refused.message.contains("superschematic-migrate: "),
        "the runner's own line: {}",
        refused.message
    );
    let read = common::ok(
        &mut runtime,
        json!({"kind": "get", "type": "Task", "id": task["id"]}),
    );
    assert!(read.get("estimate").is_none(), "v1 still: {read}");
}

/// An execution that never ends.
struct Never;

impl Execution for Never {
    fn failure(&mut self) -> Result<Option<String>, Refusal> {
        Ok(None)
    }

    fn name(&self) -> String {
        "execution never".into()
    }
}

struct Stalled;

impl Launch for Stalled {
    fn launch(&mut self, _job: &str) -> Result<Box<dyn Execution>, Refusal> {
        Ok(Box::new(Never))
    }
}

/// A publish waits for its job no longer than its timeout, and holds no
/// lock meanwhile: another server serves the old tables while it waits.
#[test]
fn a_publish_waits_for_its_job_no_longer_than_its_timeout() {
    let Some(binaries) = binaries() else {
        return skipped();
    };
    let dir = scratch("stalled");
    let path = dir.join("tasks.db");
    let mut runtime = Runtime::open(&path)
        .expect("the file opens")
        .with_binaries(binaries.clone());
    runtime
        .publish(&Publish::of(vec![common::v1()]))
        .expect("v1 publishes in process");
    let mut stalled = Runtime::open(&path)
        .expect("the file opens")
        .with_binaries(binaries)
        .with_plan_runner(
            JobRunner::new(
                Directory(dir.join("bucket")),
                Stalled,
                JobDatabase::Url(path.display().to_string()),
            )
            .with_wait(Duration::from_millis(300), Duration::from_millis(20)),
        );
    let timed_out = stalled
        .publish(&Publish::of(vec![common::v2()]))
        .expect_err("the job never finishes");
    assert_eq!(timed_out.code, Code::Busy);
    assert_eq!(timed_out.class, "PlanTimeoutError");
    common::ok(&mut runtime, json!({"kind": "list", "type": "Task"}));
}

#[cfg(feature = "postgres")]
mod on_postgres {
    //! On Postgres the job connects as a migrator role that owns the
    //! database and the tables a plan writes, and the servers as a role
    //! with D46's grants: USAGE, and SELECT, INSERT, UPDATE and DELETE on
    //! the tables, which the job extends to each new table in the
    //! transaction that finishes its plan, and SELECT alone on the
    //! runner's state and the record. Skipped without
    //! `SUPERSCHEMATIC_RULES_TEST_DATABASE_URL`, a server whose role may
    //! create roles and databases.

    use super::*;
    use ::postgres::{Client, NoTls};

    const PASSWORD: &str = "runtime-job";

    /// A database of its own owned by a migrator role, and a server role,
    /// dropped with it.
    struct Roles {
        admin: String,
        database: String,
        migrator: String,
        server: String,
    }

    impl Roles {
        fn new(test: &str) -> Option<Roles> {
            let Ok(admin) = std::env::var("SUPERSCHEMATIC_RULES_TEST_DATABASE_URL") else {
                println!("set SUPERSCHEMATIC_RULES_TEST_DATABASE_URL to run runtime mode's job on Postgres");
                return None;
            };
            let id = std::process::id();
            let roles = Roles {
                admin,
                database: format!("rules_job_{test}_{id}"),
                migrator: format!("rules_job_migrator_{test}_{id}"),
                server: format!("rules_job_server_{test}_{id}"),
            };
            roles.drop_all();
            let mut client = Client::connect(&roles.admin, NoTls).expect("the test server");
            for statement in [
                format!("CREATE ROLE {} LOGIN PASSWORD '{PASSWORD}'", roles.migrator),
                format!("CREATE ROLE {} LOGIN PASSWORD '{PASSWORD}'", roles.server),
                format!(
                    "CREATE DATABASE {} OWNER {}",
                    roles.database, roles.migrator
                ),
            ] {
                client
                    .batch_execute(&statement)
                    .unwrap_or_else(|error| panic!("{statement}: {error}"));
            }
            Some(roles)
        }

        fn url(&self, role: &str) -> String {
            as_role(&self.admin, role, &self.database)
        }

        fn drop_all(&self) {
            if let Ok(mut client) = Client::connect(&self.admin, NoTls) {
                let _ = client.batch_execute(&format!(
                    "DROP DATABASE IF EXISTS {} WITH (FORCE)",
                    self.database
                ));
                for role in [&self.server, &self.migrator] {
                    let _ = client.batch_execute(&format!("DROP ROLE IF EXISTS {role}"));
                }
            }
        }

        /// A server of the environment: the server role's connection, whose
        /// publish the job applies as the migrator.
        fn opener(&self, bucket: PathBuf) -> impl Fn() -> Runtime + '_ {
            move || {
                let binaries = binaries().expect("the binaries");
                Runtime::postgres(&self.url(&self.server))
                    .expect("the server role opens the database")
                    .with_plan_runner(
                        job_runner(
                            &binaries,
                            &bucket,
                            JobDatabase::Url(self.url(&self.migrator)),
                        )
                        .with_servers([self.server.clone()]),
                    )
            }
        }
    }

    impl Drop for Roles {
        fn drop(&mut self) {
            self.drop_all();
        }
    }

    /// The URL with another role, its password and another database.
    fn as_role(url: &str, role: &str, database: &str) -> String {
        let (scheme, rest) = url.split_once("://").expect("a URL");
        let rest = rest.rsplit_once('@').map_or(rest, |(_, host)| host);
        let (host, query) = match rest.split_once('?') {
            Some((host, query)) => (host, format!("?{query}")),
            None => (rest, String::new()),
        };
        let host = host.split_once('/').map_or(host, |(host, _)| host);
        format!("{scheme}://{role}:{PASSWORD}@{host}/{database}{query}")
    }

    /// The scenarios of a runtime environment, served by the server role
    /// while the migrator applies each publish: rows of a table the first
    /// publish wrote and of the columns, the relation and the index the
    /// second added, a behavior table with its sub-table, its shared table
    /// and its queues, a rename, and a list in SQL as development mode
    /// answers it.
    #[test]
    fn a_server_role_with_d46s_grants_serves_what_the_migrator_published() {
        let Some(binaries) = binaries() else {
            return skipped();
        };
        let dir = scratch("postgres");
        for (test, scenario) in [
            (
                "extend",
                common::publish_and_extend as fn(common::Open<'_>, &Binaries),
            ),
            ("rename", common::rename),
            ("parity", common::parity::lists_answer_as_development_mode),
        ] {
            let Some(roles) = Roles::new(test) else {
                return;
            };
            scenario(&roles.opener(dir.join(test)), &binaries);
            check_grants(&roles, &["task", "project"]);
        }
        // A table a later publish adds is the server's to write the
        // moment the publish returns: the job gave it in the transaction
        // that finished the plan.
        let Some(roles) = Roles::new("added") else {
            return;
        };
        let open = roles.opener(dir.join("added"));
        let mut server = common::host(&open, &binaries, &common::Clock::now());
        server
            .publish(&Publish::of(vec![common::v1()]))
            .expect("v1");
        let mut follower = common::host(&open, &binaries, &common::Clock::now());
        common::ok(&mut follower, json!({"kind": "list", "type": "Task"}));
        server
            .publish(&Publish::of(vec![with_note(common::v1())]))
            .expect("v1 and a Note");
        let note = common::create(&mut follower, "Note", json!({"body": "Sanded"}));
        assert_eq!(note["body"], "Sanded");
        check_grants(&roles, &["note", "task"]);

        let Some(roles) = Roles::new("behaviors") else {
            return;
        };
        common::behavior_tables(&roles.opener(dir.join("behaviors")), &binaries);
        check_grants(
            &roles,
            &[
                "ticket",
                "ticket_timers_deadlines",
                "ticket_deadline_queue",
                "ticket_change_queue",
                "invoice",
            ],
        );
    }

    /// A document with a Note table beside its own.
    fn with_note(mut document: Value) -> Value {
        document["types"]["Note"] = json!({"name": "Note", "role": "DBTable", "fields": [
            {"name": "id", "typeRef": {"name": "Identity.UUID"}, "required": true, "key": true, "autoGenerated": true},
            {"name": "body", "typeRef": {"name": "string"}, "required": true}
        ]});
        document
    }

    /// The migrator owns each table; the server may read and write it and
    /// create none, and may only read the runner's state and the record.
    fn check_grants(roles: &Roles, tables: &[&str]) {
        let mut migrator = Client::connect(&roles.url(&roles.migrator), NoTls).expect("migrator");
        let present: Vec<String> = migrator
            .query(
                "SELECT tablename FROM pg_tables WHERE schemaname = 'public' AND tablename = ANY($1)",
                &[&tables],
            )
            .expect("the tables")
            .iter()
            .map(|row| row.get(0))
            .collect();
        assert!(!present.is_empty(), "none of {tables:?}");
        for table in &present {
            let row = migrator
                .query_one(
                    "SELECT tableowner, has_table_privilege($2, $1, 'SELECT, INSERT, UPDATE, DELETE'), has_table_privilege($2, $1, 'TRUNCATE')
                     FROM pg_tables WHERE schemaname = 'public' AND tablename = $1",
                    &[table, &roles.server],
                )
                .expect("the table's privileges");
            assert_eq!(row.get::<_, String>(0), roles.migrator, "{table}'s owner");
            assert!(row.get::<_, bool>(1), "the server reads and writes {table}");
            assert!(!row.get::<_, bool>(2), "and holds no more on {table}");
        }
        for table in ["superschematic_schema_state", "superschematic_runtime"] {
            let row = migrator
                .query_one(
                    "SELECT has_table_privilege($2, $1, 'SELECT'), has_table_privilege($2, $1, 'INSERT'), has_table_privilege($2, $1, 'UPDATE')",
                    &[&table, &roles.server],
                )
                .expect("the state's privileges");
            assert!(row.get::<_, bool>(0), "the server reads {table}");
            assert!(
                !row.get::<_, bool>(1) && !row.get::<_, bool>(2),
                "and writes no {table}"
            );
        }
        let mut server = Client::connect(&roles.url(&roles.server), NoTls).expect("server");
        let refused = server
            .batch_execute("CREATE TABLE intruder (id int)")
            .expect_err("a server runs no DDL");
        assert!(
            refused.as_db_error().map(|e| e.code().code()) == Some("42501"),
            "{refused}"
        );
    }
}
