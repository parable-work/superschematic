//! Runtime mode (D117): the request handler of
//! `superschematic-rules-handler` over each type's own table, the table a
//! build writes for the class, in a SQLite file or a Postgres database, and
//! a publish that is a migration.
//!
//! A runtime environment cannot generate and compile code, so it reads
//! nothing from generated code. A publish takes the database's schema
//! documents whole, as `format --to=json` writes them, and the host:
//!
//! 1. reads the model the database recorded (D27's state table);
//! 2. asks the compiler for the plan from that model to the documents' model
//!    and for the layout document a handler serves the tables through
//!    (`superschematic migrate plan --request -`, with the `superschematic`
//!    binary on `PATH` or named by [`Binaries`]), or takes a plan made
//!    where the compiler is ([`Planned`]);
//! 3. refuses a plan whose `from` is not the recorded model, and one with a
//!    `destructive` or `data-dependent` hazard the publish does not allow,
//!    with each hazard's id;
//! 4. has the environment's runner apply the plan, expand and contract back
//!    to back: the Rust runner, `superschematic-migrate`'s crate, on a
//!    connection of its own to a SQLite file, and the Go runner's binary,
//!    `superschematic-migrate apply --plan -`, beside a Postgres database;
//! 5. records the documents and the layout next to the model, in
//!    `superschematic_runtime`, beside the runner's two state tables.
//!
//! Every request reads the model the database is at in its transaction,
//! and reads the record again when that hash has moved, so every server of
//! the environment follows a publish with no restart. A plan in progress
//! serves no request. [`Runtime::plan`] answers a publish's plan, its steps
//! and each step's hazards, without applying it: the plan as a route and a
//! tool ([`Runtime::answer`], [`tools`]).
//!
//! ```no_run
//! use serde_json::json;
//! use superschematic_rules::Caller;
//! use superschematic_rules_runtime::{Publish, Runtime};
//!
//! let mut runtime = Runtime::open("shop.db")?;
//! let documents = vec![json!({"name": "shop-db", "kind": "DB", "types": {"Order": {
//!     "name": "Order", "role": "DBTable",
//!     "fields": [{"name": "title", "typeRef": {"name": "string"}, "required": true}]}}})];
//! runtime.publish(&Publish::of(documents))?;
//! let me = Caller::new("alice", Vec::<String>::new());
//! let order = runtime.answer(&me, &json!({"kind": "create", "type": "Order", "data": {"title": "Desk"}}))?;
//! # Ok::<(), superschematic_rules::Refusal>(())
//! ```

mod conn;
mod db;
mod query;
mod record;
mod store;
pub mod values;

use std::path::{Path, PathBuf};
use std::sync::Arc;
use std::time::{SystemTime, UNIX_EPOCH};

use serde::{Deserialize, Serialize};
use serde_json::{json, Map, Value};
use superschematic_rules::{Caller, Code, Core, Refusal};
use superschematic_rules_adapter::Layout;
use superschematic_rules_handler::{
    run_due, serve, Context, Declarations, Handler, RanDue, Reading, Request,
};

use crate::conn::{run_binary, Conn, SqliteConn};
use crate::db::Db;
pub use crate::record::{type_document, Record, RECORD_TABLE};
pub use crate::store::RuntimeStore;
pub use crate::values::Dialect;

/// The hazard classes a publish refuses unless it allows each hazard by
/// its id (D117): what deletes data and what fails on the rows a table
/// holds.
pub const REFUSED_CLASSES: [&str; 2] = ["destructive", "data-dependent"];

/// The version of the plan request the planner reads
/// (`migrateplan.RequestVersion`).
const REQUEST_VERSION: i64 = 1;

/// The binaries a runtime environment carries: the compiler, whose planner
/// a publish runs, and the Go runner, which applies a Postgres plan. A
/// SQLite plan is applied in process.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Binaries {
    pub superschematic: PathBuf,
    pub migrate: PathBuf,
}

impl Default for Binaries {
    /// `$SUPERSCHEMATIC_BIN` and `$SUPERSCHEMATIC_MIGRATE_BIN`, else
    /// `superschematic` and `superschematic-migrate` on `PATH`.
    fn default() -> Self {
        let from_env = |name: &str, default: &str| {
            std::env::var_os(name)
                .filter(|value| !value.is_empty())
                .map_or_else(|| PathBuf::from(default), PathBuf::from)
        };
        Binaries {
            superschematic: from_env("SUPERSCHEMATIC_BIN", "superschematic"),
            migrate: from_env("SUPERSCHEMATIC_MIGRATE_BIN", "superschematic-migrate"),
        }
    }
}

/// A plan made where the compiler is, sent with the schemas (D117): on a
/// platform that cannot spawn the compiler, a CLI or the platform's own
/// service plans, and the runner holds the plan to the database it meets.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Planned {
    /// The plan document, as `migrate plan --request` answers it.
    pub plan: Value,
    /// The layout document the same answer holds.
    pub layout: Value,
}

/// A publish, or the request for its plan: the database's schema documents
/// whole, each as `format --to=json` writes it, the renames it means (as
/// `--rename` takes them), the ids of the hazards it allows (as `--allow`
/// takes them), and a plan made elsewhere, if it carries one.
#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Publish {
    pub documents: Vec<Value>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub renames: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub allow: Vec<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub planned: Option<Planned>,
}

impl Publish {
    /// A publish of documents, with no rename and no hazard allowed.
    pub fn of(documents: Vec<Value>) -> Self {
        Publish {
            documents,
            ..Publish::default()
        }
    }

    /// The publish with a rename it means.
    pub fn rename(mut self, rename: impl Into<String>) -> Self {
        self.renames.push(rename.into());
        self
    }

    /// The publish with a hazard it allows.
    pub fn allow(mut self, hazard: impl Into<String>) -> Self {
        self.allow.push(hazard.into());
        self
    }
}

/// When the host runs its queues: after each request, as a worker that
/// claims a message the moment it is written would, or only when asked.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Default)]
pub enum Drain {
    #[default]
    AfterEachRequest,
    WhenAsked,
}

/// A plan, read and checked, with what a publish of it refuses.
struct Checked {
    service: String,
    document: Vec<u8>,
    plan: superschematic_migrate::Plan,
    summary: Value,
    layout: Layout,
    refused: Vec<Value>,
    recorded: Option<Record>,
}

/// The runtime host: the handler over a runtime store in one process, with
/// the core, the behaviors' declarations, the binaries a publish runs, a
/// clock and the caller its runner runs the queues as.
pub struct Runtime {
    core: Arc<Core>,
    declarations: Arc<Declarations>,
    store: RuntimeStore,
    binaries: Binaries,
    clock: Box<dyn Fn() -> i64 + Send>,
    runner: Caller,
    drain: Drain,
}

/// The system's time in epoch milliseconds.
fn system_millis() -> i64 {
    match SystemTime::now().duration_since(UNIX_EPOCH) {
        Ok(since) => i64::try_from(since.as_millis()).unwrap_or(i64::MAX),
        Err(before) => -i64::try_from(before.duration().as_millis()).unwrap_or(i64::MAX),
    }
}

impl Runtime {
    /// The host over a SQLite file, running the behaviors ported onto the
    /// core.
    pub fn open(path: impl AsRef<Path>) -> Result<Self, Refusal> {
        let conn = Conn::Sqlite(SqliteConn::open(path.as_ref())?);
        Self::over(conn)
    }

    /// The host over the Postgres database a URL names, whose publish runs
    /// the Go runner with the same URL.
    #[cfg(feature = "postgres")]
    pub fn postgres(url: &str) -> Result<Self, Refusal> {
        let conn = Conn::Postgres(conn::PgConn::open(url)?);
        Self::over(conn)
    }

    fn over(conn: Conn) -> Result<Self, Refusal> {
        let core = Arc::new(superschematic_rules_behaviors::core());
        let declarations = Arc::new(Declarations::ported());
        let store = RuntimeStore::new(conn, core.clone(), declarations.clone())?;
        Ok(Runtime {
            core,
            declarations,
            store,
            binaries: Binaries::default(),
            clock: Box::new(system_millis),
            runner: Caller::new("runtime-runner", Vec::<String>::new()),
            drain: Drain::default(),
        })
    }

    /// The host with the binaries a publish runs.
    pub fn with_binaries(mut self, binaries: Binaries) -> Self {
        self.binaries = binaries;
        self
    }

    /// The host with a clock of its own, in epoch milliseconds.
    pub fn with_clock(mut self, clock: impl Fn() -> i64 + Send + 'static) -> Self {
        self.clock = Box::new(clock);
        self
    }

    /// The host with the caller its runner runs the queues as.
    pub fn with_runner(mut self, runner: Caller) -> Self {
        self.runner = runner;
        self
    }

    /// The host with when it runs its queues.
    pub fn with_drain(mut self, drain: Drain) -> Self {
        self.drain = drain;
        self
    }

    pub fn store(&mut self) -> &mut RuntimeStore {
        &mut self.store
    }

    /// The serving connection of a SQLite file, for a test's or an
    /// operator's own SQL; `None` on Postgres.
    pub fn sqlite(&self) -> Option<&rusqlite::Connection> {
        match &self.store.db {
            Conn::Sqlite(conn) => Some(conn.connection()),
            #[cfg(feature = "postgres")]
            Conn::Postgres(_) => None,
        }
    }

    /// The serving connection of a Postgres database, for a test's or an
    /// operator's own SQL; `None` on SQLite.
    #[cfg(feature = "postgres")]
    pub fn postgres_client(&mut self) -> Option<&mut postgres::Client> {
        match &mut self.store.db {
            Conn::Postgres(conn) => Some(conn.client()),
            Conn::Sqlite(_) => None,
        }
    }

    pub fn dialect(&self) -> Dialect {
        self.store.dialect()
    }

    fn context(&self, caller: &Caller) -> Context {
        Context {
            caller: caller.clone(),
            now: (self.clock)(),
            request: uuid::Uuid::new_v4().to_string(),
            id: uuid::Uuid::new_v4().as_u128(),
        }
    }

    /// Serves one of the handler's requests as a caller, then, unless the
    /// host drains when asked, runs the queues. A define and the handler's
    /// publish of one type are refused: runtime mode publishes a database's
    /// schemas whole, as a migration ([`Runtime::publish`]).
    pub fn request(&mut self, caller: &Caller, request: &Request) -> Result<Value, Refusal> {
        if let Request::Define { .. } | Request::Publish { .. } = request {
            return Err(Refusal::invalid_argument(
                "runtime mode defines and publishes no type on its own: a publish takes the database's schema documents whole and migrates its tables (D117)",
            ));
        }
        let context = self.context(caller);
        let core = self.core.clone();
        let declarations = self.declarations.clone();
        let handler = Handler::new(&core, &declarations).with_reading(Reading::Queries);
        let response = serve(&handler, &mut self.store, request, &context);
        if self.drain == Drain::AfterEachRequest {
            self.run_due()?;
        }
        response
    }

    /// Runs the change queue and the deadlines due by the host's clock, as
    /// its runner.
    pub fn run_due(&mut self) -> Result<Vec<RanDue>, Refusal> {
        let core = self.core.clone();
        let declarations = self.declarations.clone();
        let handler = Handler::new(&core, &declarations).with_reading(Reading::Queries);
        let mut fresh = || {
            (
                uuid::Uuid::new_v4().to_string(),
                uuid::Uuid::new_v4().as_u128(),
            )
        };
        let now = (self.clock)();
        run_due(&handler, &mut self.store, now, &self.runner, &mut fresh)
    }

    /// Answers a request as its JSON: `plan` and `publish` with a
    /// [`Publish`]'s members, the routes and the tools of runtime mode
    /// ([`tools`]), and any other kind as the handler's [`Request`].
    pub fn answer(&mut self, caller: &Caller, request: &Value) -> Result<Value, Refusal> {
        match request.get("kind").and_then(Value::as_str) {
            Some("plan") => {
                let publish = publish_of(request)?;
                self.plan(&publish)
            }
            Some("publish") => {
                let publish = publish_of(request)?;
                self.publish(&publish)
            }
            _ => {
                let request: Request =
                    serde_json::from_value(request.clone()).map_err(|error| {
                        Refusal::invalid_argument(format!("not a request: {error}"))
                    })?;
                self.request(caller, &request)
            }
        }
    }

    /// The model the database recorded for a service, as its canonical
    /// JSON, and its hash; `None` for a database no plan has taken
    /// anywhere.
    fn recorded_model(&mut self, service: &str) -> Result<Option<(String, String)>, Refusal> {
        if !self.store.db.has_table("superschematic_schema_state")? {
            return Ok(None);
        }
        let p = db::placeholder(self.dialect(), 1);
        let rows = self.store.db.query(
            &format!(
                "SELECT model, model_hash FROM superschematic_schema_state WHERE service = {p}"
            ),
            &[Value::String(service.to_owned())],
        )?;
        Ok(rows.into_iter().next().and_then(|row| {
            let model = row.get("model").and_then(Value::as_str)?.to_owned();
            let hash = row.get("model_hash").and_then(Value::as_str)?.to_owned();
            (!model.is_empty()).then_some((model, hash))
        }))
    }

    /// Plans a publish and checks the plan: its service, its `from`
    /// against the recorded model, the hazards it does not allow, and the
    /// types the handler would serve over it.
    fn check(&mut self, publish: &Publish) -> Result<Checked, Refusal> {
        let service = record::service_of(&publish.documents)?;
        let recorded = self.store.read_record()?;
        if let Some(recorded) = &recorded {
            if recorded.service != service {
                return Err(Refusal::invalid_argument(format!(
                    "the database holds {}, and the documents are of {service}: a runtime database holds one DB service",
                    recorded.service
                )));
            }
        }
        let model = self.recorded_model(&service)?;
        let (plan_value, layout_value) = match &publish.planned {
            Some(planned) => (planned.plan.clone(), planned.layout.clone()),
            None => self.ask_planner(publish, model.as_ref().map(|(model, _)| model.as_str()))?,
        };
        let document = serde_json::to_vec(&plan_value)
            .map_err(|error| values::internal(format!("the plan is no JSON: {error}")))?;
        let plan = superschematic_migrate::read_plan(&document).map_err(|error| Refusal {
            class: "PlanError".to_owned(),
            ..Refusal::invalid_argument(format!("the plan does not read: {error}"))
        })?;
        if plan.dialect.to_string() != self.dialect().as_str() {
            return Err(Refusal::invalid_argument(format!(
                "the plan is for {}, and the database is {}",
                plan.dialect,
                self.dialect().as_str()
            )));
        }
        if plan.service != service {
            return Err(Refusal::invalid_argument(format!(
                "the plan migrates {}, and the documents are of {service}",
                plan.service
            )));
        }
        let at = model
            .as_ref()
            .map(|(_, hash)| hash.clone())
            .unwrap_or_default();
        if plan.from != at {
            let mut details = Map::new();
            details.insert("from".into(), Value::String(plan.from.clone()));
            details.insert("recorded".into(), Value::String(at.clone()));
            return Err(Refusal {
                class: "PlanBaselineError".to_owned(),
                details: Some(Box::new(details)),
                ..Refusal::new(
                    Code::Conflict,
                    format!(
                        "the plan starts from model {}, and {service} recorded model {}: plan again from the recorded model",
                        shown(&plan.from),
                        shown(&at)
                    ),
                )
            });
        }
        let layout: Layout = serde_json::from_value(layout_value).map_err(|error| {
            values::internal(format!(
                "the planner's layout is no layout document: {error}"
            ))
        })?;
        let mut hazards = Vec::new();
        let mut refused = Vec::new();
        let mut steps = Vec::new();
        for step in plan_value
            .get("steps")
            .and_then(Value::as_array)
            .into_iter()
            .flatten()
        {
            let step_hazards: Vec<Value> = step
                .get("hazards")
                .and_then(Value::as_array)
                .cloned()
                .unwrap_or_default();
            for hazard in &step_hazards {
                let id = hazard.get("id").and_then(Value::as_str).unwrap_or_default();
                let class = hazard
                    .get("class")
                    .and_then(Value::as_str)
                    .unwrap_or_default();
                if REFUSED_CLASSES.contains(&class)
                    && !publish.allow.iter().any(|allowed| allowed == id)
                {
                    refused.push(hazard.clone());
                }
                hazards.push(hazard.clone());
            }
            steps.push(json!({
                "index": step.get("index").cloned().unwrap_or(Value::Null),
                "phase": step.get("phase").cloned().unwrap_or(Value::Null),
                "op": step.get("op").cloned().unwrap_or(Value::Null),
                "subject": step.get("subject").cloned().unwrap_or(Value::Null),
                "statements": step.get("statements").cloned().unwrap_or_else(|| json!([])),
                "hazards": step_hazards,
            }));
        }
        let refused_ids: Vec<Value> = refused
            .iter()
            .filter_map(|hazard| hazard.get("id").cloned())
            .collect();
        let summary = json!({
            "service": service,
            "dialect": self.dialect().as_str(),
            "from": plan.from,
            "to": plan.to,
            "plan": plan.hash,
            "renames": plan.renames,
            "steps": steps,
            "hazards": hazards,
            "refused": refused_ids,
        });
        Ok(Checked {
            service,
            document,
            plan,
            summary,
            layout,
            refused,
            recorded,
        })
    }

    /// Runs the planner on a publish's documents from the recorded model:
    /// the plan and the layout document it answers.
    fn ask_planner(
        &mut self,
        publish: &Publish,
        model: Option<&str>,
    ) -> Result<(Value, Value), Refusal> {
        let from = match model {
            Some(model) => serde_json::from_str(model).map_err(|error| {
                values::internal(format!("the recorded model is no JSON: {error}"))
            })?,
            None => Value::Null,
        };
        let request = json!({
            "version": REQUEST_VERSION,
            "dialect": self.dialect().as_str(),
            "documents": publish.documents,
            "from": from,
            "renames": publish.renames,
            "layout": true,
        });
        let body = serde_json::to_vec(&request)
            .map_err(|error| values::internal(format!("the plan request is no JSON: {error}")))?;
        let (_, stdout, stderr) = run_binary(
            &self.binaries.superschematic,
            &["migrate", "plan", "--request", "-"],
            &body,
        )?;
        let response: Value = serde_json::from_slice(&stdout).map_err(|error| {
            values::internal(format!(
                "the planner answered no JSON ({error}): {}",
                if stderr.is_empty() {
                    String::from_utf8_lossy(&stdout).into_owned()
                } else {
                    stderr.clone()
                }
            ))
        })?;
        if let Some(error) = response.get("error").and_then(Value::as_str) {
            return Err(Refusal {
                class: "PlanError".to_owned(),
                ..Refusal::new(
                    Code::InvalidSchema,
                    format!("the planner refused the documents: {error}"),
                )
            });
        }
        let plan = response
            .get("plan")
            .cloned()
            .ok_or_else(|| values::internal("the planner answered no plan"))?;
        let layout = response
            .get("layout")
            .cloned()
            .ok_or_else(|| values::internal("the planner answered no layout"))?;
        Ok((plan, layout))
    }

    /// The plan a publish would apply, without applying it: its service,
    /// the models it goes from and to, its steps in order with each one's
    /// statements and hazards, every hazard, and the ids of those a publish
    /// refuses unless it allows them.
    pub fn plan(&mut self, publish: &Publish) -> Result<Value, Refusal> {
        let checked = self.check(publish)?;
        let mut summary = checked.summary;
        if let Value::Object(members) = &mut summary {
            members.insert("applies".into(), Value::Bool(checked.refused.is_empty()));
        }
        Ok(summary)
    }

    /// Publishes the database's schema documents: plans from the recorded
    /// model, refuses what the plan does not allow, applies the plan
    /// through the runner, records the documents and the layout next to
    /// the model, and serves the new layout from the next request.
    pub fn publish(&mut self, publish: &Publish) -> Result<Value, Refusal> {
        self.store.db.lock_publish()?;
        let published = self.publish_locked(publish);
        let unlocked = self.store.db.unlock_publish();
        let published = published?;
        unlocked?;
        Ok(published)
    }

    fn publish_locked(&mut self, publish: &Publish) -> Result<Value, Refusal> {
        let checked = self.check(publish)?;
        if !checked.refused.is_empty() {
            let ids: Vec<String> = checked
                .refused
                .iter()
                .filter_map(|hazard| hazard.get("id").and_then(Value::as_str).map(str::to_owned))
                .collect();
            let mut details = Map::new();
            details.insert("hazards".into(), Value::Array(checked.refused.clone()));
            return Err(Refusal {
                class: "PlanHazardError".to_owned(),
                details: Some(Box::new(details)),
                ..Refusal::new(
                    Code::IncompatibleChange,
                    format!(
                        "the plan has hazards the publish does not allow: {}; allow each by its id to publish",
                        ids.join(", ")
                    ),
                )
            });
        }
        let record = Record {
            service: checked.service.clone(),
            model_hash: checked.plan.to.clone(),
            versions: record::versions_after(
                checked.recorded.as_ref(),
                &publish.documents,
                &checked.layout,
            ),
            documents: publish.documents.clone(),
            layout: checked.layout,
        };
        // The handler must serve every table over the plan's layout before
        // the plan runs.
        record::serve(&record, &self.core, &self.declarations)?;
        self.store
            .db
            .apply_plan(&checked.document, &checked.plan, &self.binaries)?;
        self.store.db.begin()?;
        let written = self.store.write_record(&record);
        match written {
            Ok(()) => self.store.db.commit()?,
            Err(refusal) => {
                self.store.db.rollback()?;
                return Err(refusal);
            }
        }
        self.store.serve_record(&record)?;
        let mut answer = checked.summary;
        if let Value::Object(members) = &mut answer {
            members.insert("applied".into(), Value::Bool(true));
            members.insert("versions".into(), json!(record.versions));
        }
        Ok(answer)
    }
}

/// A hash as a message shows it: an empty database for none.
fn shown(hash: &str) -> String {
    if hash.is_empty() {
        "\"\" (an empty database)".to_owned()
    } else {
        hash.to_owned()
    }
}

fn publish_of(request: &Value) -> Result<Publish, Refusal> {
    serde_json::from_value(request.clone()).map_err(|error| {
        Refusal::invalid_argument(format!(
            "not a publish: {error}; want documents, and renames, allow and planned if any"
        ))
    })
}

/// The tools of runtime mode an MCP server lists beside the handler's:
/// `plan`, which answers a publish's plan without applying it, and
/// `publish`, which applies it. Each takes a [`Publish`]'s members.
pub fn tools() -> Vec<Value> {
    let input = json!({
        "type": "object",
        "required": ["documents"],
        "properties": {
            "documents": {
                "type": "array",
                "description": "Every schema document of the database's DB service, each as format --to=json writes it.",
                "items": {"type": "object"}
            },
            "renames": {
                "type": "array",
                "description": "The renames the publish means: old=new for a table, table.old=table.new for a column.",
                "items": {"type": "string"}
            },
            "allow": {
                "type": "array",
                "description": "The ids of the destructive or data-dependent hazards the publish allows, as the plan lists them.",
                "items": {"type": "string"}
            },
            "planned": {
                "type": "object",
                "description": "A plan made where the compiler is, with its layout, as migrate plan --request answers them.",
                "required": ["plan", "layout"],
                "properties": {"plan": {"type": "object"}, "layout": {"type": "object"}}
            }
        }
    });
    vec![
        json!({
            "name": "plan",
            "description": "The plan a publish of the schema documents would apply to the database, without applying it: its steps in order, each with its SQL and its hazards, and the ids of the hazards a publish refuses unless it allows them.",
            "inputSchema": input,
        }),
        json!({
            "name": "publish",
            "description": "Publishes the database's schema documents: plans the migration from the model the database recorded, refuses a destructive or data-dependent hazard it does not allow, applies the plan and serves the new tables from the next request.",
            "inputSchema": input,
        }),
    ]
}
