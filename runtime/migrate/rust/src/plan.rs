//! The plan and model documents as the runner reads them
//! (`runtime/migrate/README.md`, "The plan document"), with every check the
//! Go runner's `ReadPlan` and `ReadModel` make, in their order and with
//! their refusals.
//!
//! A member reads as Go's decoder reads it into the Go runner's types: a
//! member that is absent or null is its zero value, a member of another
//! type stops the read, and a member the runner does not know is ignored.
//! One difference: Go matches a member's name without regard to case, and
//! this reader takes the name as the contract writes it.

use std::collections::BTreeMap;
use std::fmt;

use crate::canonical::{self, canonical};
use crate::error::{refuse, Error};
use crate::json::{self, Value};

/// The only plan version this runner reads.
pub const PLAN_VERSION: u32 = 1;

/// The database a plan or a model is for. This runner applies
/// [`Dialect::Sqlite`] plans alone (D116) and reads the other so that it
/// can refuse one by name.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Dialect {
    /// `postgres`.
    Postgres,
    /// `sqlite`.
    Sqlite,
}

impl Dialect {
    /// The dialect's name in a document and in the state table.
    pub fn as_str(self) -> &'static str {
        match self {
            Dialect::Postgres => "postgres",
            Dialect::Sqlite => "sqlite",
        }
    }

    fn named(name: &str) -> Option<Dialect> {
        match name {
            "postgres" => Some(Dialect::Postgres),
            "sqlite" => Some(Dialect::Sqlite),
            _ => None,
        }
    }
}

impl fmt::Display for Dialect {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(self.as_str())
    }
}

/// When a step runs relative to the rollout of the new servers, and the
/// phases [`Runner::apply`](crate::Runner::apply) is asked for.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Phase {
    /// Expand steps run before the rollout.
    Expand,
    /// Contract steps run after it.
    Contract,
    /// Both phases, back to back. No step has it.
    All,
}

impl Phase {
    /// The phase's name in a document and in a log row.
    pub fn as_str(self) -> &'static str {
        match self {
            Phase::Expand => "expand",
            Phase::Contract => "contract",
            Phase::All => "all",
        }
    }
}

impl fmt::Display for Phase {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(self.as_str())
    }
}

/// A plan document as the runner reads it. [`read_plan`] is the only way to
/// make one, and it checks the plan first.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Plan {
    /// The database the plan is for.
    pub dialect: Dialect,
    /// The DB service the plan migrates. It keys the runner's state.
    pub service: String,
    /// The hash of the model the plan starts from; empty means an empty
    /// database.
    pub from: String,
    /// The hash of the model the plan ends at.
    pub to: String,
    /// The renames the plan was given, as written. The runner does not read
    /// them.
    pub renames: Vec<String>,
    /// Every expand step, then every contract step.
    pub steps: Vec<Step>,
    /// The plan's hash.
    pub hash: String,
    model: Model,
    expanded: Option<Model>,
}

impl Plan {
    /// The plan's `toModel`, the model it ends at.
    pub fn model(&self) -> &Model {
        &self.model
    }

    /// The plan's `expandedModel`: the model the database holds once the
    /// plan's expand steps have finished and until its contract runs. A
    /// plan with contract steps has one; a plan written before plans
    /// carried it does not.
    pub fn between_phases(&self) -> Option<&Model> {
        self.expanded.as_ref()
    }

    /// Reports whether the plan has a contract step.
    pub(crate) fn contracts(&self) -> bool {
        self.steps.iter().any(|step| step.phase == Phase::Contract)
    }
}

/// One step of a plan.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Step {
    /// The step's 1-based position in the plan.
    pub index: usize,
    /// [`Phase::Expand`] or [`Phase::Contract`].
    pub phase: Phase,
    /// The operation, for people. The runner does not read it.
    pub op: String,
    /// The object the step changes, as a path.
    pub subject: String,
    /// Complete SQL statements, run in order. It may be empty: the step
    /// changes nothing, and the runner logs it like any other.
    pub statements: Vec<String>,
    /// Whether the step runs in one transaction. Every SQLite step does.
    pub transactional: bool,
    /// The statements to run before a step that started and did not finish
    /// runs again. Only a step outside a transaction has them, so no SQLite
    /// step does.
    pub recovery: Vec<String>,
    /// Run the step with foreign key enforcement off and check every
    /// foreign key before the commit. Only plans written before D27's
    /// amendment on foreign keys set it.
    pub foreign_keys_off: bool,
    /// The step's hazards. The runner does not read them.
    pub hazards: Vec<Hazard>,
}

/// One hazard of a step. The runner does not read it.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Hazard {
    /// The hazard's ID, which `--allow` names.
    pub id: String,
    /// The hazard's class, `destructive` or `blocking`, say.
    pub class: String,
    /// The object the hazard is about.
    pub subject: String,
    /// The reader the hazard breaks, when it breaks one.
    pub reader: String,
    /// Why the step is a hazard, for people.
    pub reason: String,
}

/// A model document as the runner reads it: the members that key a state
/// row and the canonical JSON the row stores. The runner never reads the
/// rest of a model.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Model {
    /// The DB service the model is of.
    pub service: String,
    /// The database the model is for.
    pub dialect: Dialect,
    /// The model's canonical JSON.
    pub canonical: String,
    /// The model's hash: the SHA-256 of its canonical JSON.
    pub hash: String,
}

/// Reads a plan document in any JSON encoding and checks it before anything
/// runs: its version, its hash against its content, its `to` against the
/// hash of its `toModel`, its `expanded`, when it has one, against the hash
/// of its `expandedModel`, and the shape of its steps.
///
/// It reads a `postgres` plan too, which
/// [`Runner::apply`](crate::Runner::apply) then refuses by name.
pub fn read_plan(doc: &[u8]) -> Result<Plan, Error> {
    let value = json::parse(doc).map_err(|err| Error::Document(format!("read the plan: {err}")))?;
    let Value::Object(object) = value else {
        return Err(Error::Document(
            "read the plan: a plan is a JSON object".to_string(),
        ));
    };
    // The version comes first: a later version may change any other member.
    let Some(Value::Number(version)) = object.get("version") else {
        return Err(refuse!("the plan has no version"));
    };
    if version != "1" {
        return Err(refuse!(
            "the plan is version {version}; this runner reads version {PLAN_VERSION}"
        ));
    }
    let members = Members::of(&object, "read the plan");
    let dialect = members.string("dialect")?;
    let service = members.string("service")?;
    let from = members.string("from")?;
    let to = members.string("to")?;
    let expanded = members.string("expanded")?;
    let renames = members.strings("renames")?;
    let steps = raw_steps(&members)?;
    let hash = members.string("hash")?;

    let content = canonical::object_hash(&object);
    if hash.is_empty() {
        return Err(refuse!("the plan has no hash"));
    }
    if hash != content {
        return Err(refuse!(
            "the plan's hash is {hash} but its content hashes to {content}: it was changed after it was written"
        ));
    }
    let Some(to_model) = object.get("toModel") else {
        return Err(refuse!("the plan has no toModel"));
    };
    let model = model_of(to_model).map_err(|err| err.within("the plan's toModel"))?;
    if model.hash != to {
        return Err(refuse!(
            "the plan's toModel hashes to {}, not to the plan's to {to}",
            model.hash
        ));
    }

    let Some(dialect) = Dialect::named(&dialect) else {
        return Err(refuse!(
            "the plan's dialect is {dialect:?}; the runner knows postgres and sqlite"
        ));
    };
    if service.is_empty() {
        return Err(refuse!("the plan names no service"));
    }
    let steps = checked_steps(steps, dialect)?;
    let expanded = read_expanded(&expanded, object.get("expandedModel"), dialect, &service)?;
    Ok(Plan {
        dialect,
        service,
        from,
        to,
        renames,
        steps,
        hash,
        model,
        expanded,
    })
}

/// Reads a model document: the document [`Runner::adopt`](crate::Runner::adopt)
/// records, or a plan's `toModel`. Its service and dialect must be set.
pub fn read_model(doc: &[u8]) -> Result<Model, Error> {
    let value =
        json::parse(doc).map_err(|err| Error::Document(format!("read the model: {err}")))?;
    model_of(&value)
}

/// The model a value the decoder read holds.
fn model_of(value: &Value) -> Result<Model, Error> {
    let empty = BTreeMap::new();
    let object = match value {
        Value::Object(object) => object,
        // Go's decoder leaves a struct as it was for a null.
        Value::Null => &empty,
        _ => {
            return Err(Error::Document(
                "read the model: a model is a JSON object".to_string(),
            ))
        }
    };
    let members = Members::of(object, "read the model");
    let service = members.string("service")?;
    let dialect = members.string("dialect")?;
    if service.is_empty() {
        return Err(refuse!("the model names no service"));
    }
    let Some(dialect) = Dialect::named(&dialect) else {
        return Err(refuse!("the model's dialect is not postgres or sqlite"));
    };
    let canonical = canonical(value);
    let hash = canonical::hash(canonical.as_bytes());
    Ok(Model {
        service,
        dialect,
        canonical,
        hash,
    })
}

/// Reads the plan's `expandedModel`, the model between its phases: absent
/// when the plan has neither it nor `expanded`, and otherwise a model of
/// the plan's service and dialect that hashes to `expanded`.
fn read_expanded(
    expanded: &str,
    expanded_model: Option<&Value>,
    dialect: Dialect,
    service: &str,
) -> Result<Option<Model>, Error> {
    let value = match (expanded.is_empty(), expanded_model) {
        (true, None) => return Ok(None),
        (false, None) => return Err(refuse!("the plan has expanded but no expandedModel")),
        (true, Some(_)) => return Err(refuse!("the plan has an expandedModel but no expanded")),
        (false, Some(value)) => value,
    };
    let model = model_of(value).map_err(|err| err.within("the plan's expandedModel"))?;
    if model.hash != expanded {
        return Err(refuse!(
            "the plan's expandedModel hashes to {}, not to the plan's expanded {expanded}",
            model.hash
        ));
    }
    if model.service != service || model.dialect != dialect {
        return Err(refuse!(
            "the plan's expandedModel is of {} service {}; the plan is for {dialect} service {service}",
            model.dialect,
            model.service
        ));
    }
    Ok(Some(model))
}

/// A step as the document holds it, before the plan's checks: its index and
/// phase may be anything.
struct RawStep {
    index: i64,
    phase: String,
    op: String,
    subject: String,
    statements: Vec<String>,
    transactional: bool,
    recovery: Vec<String>,
    foreign_keys_off: bool,
    hazards: Vec<Hazard>,
}

/// Reads the plan's `steps`; a null step reads as `None`.
fn raw_steps(plan: &Members<'_>) -> Result<Vec<Option<RawStep>>, Error> {
    plan.array("steps")?
        .iter()
        .map(|step| {
            let Some(step) = plan.object_or_null(step, "steps")? else {
                return Ok(None);
            };
            let hazards = step
                .array("hazards")?
                .iter()
                .filter_map(|hazard| step.object_or_null(hazard, "hazards").transpose())
                .map(|hazard| {
                    let hazard = hazard?;
                    Ok(Hazard {
                        id: hazard.string("id")?,
                        class: hazard.string("class")?,
                        subject: hazard.string("subject")?,
                        reader: hazard.string("reader")?,
                        reason: hazard.string("reason")?,
                    })
                })
                .collect::<Result<_, Error>>()?;
            Ok(Some(RawStep {
                index: step.integer("index")?,
                phase: step.string("phase")?,
                op: step.string("op")?,
                subject: step.string("subject")?,
                statements: step.strings("statements")?,
                transactional: step.boolean("transactional")?,
                recovery: step.strings("recovery")?,
                foreign_keys_off: step.boolean("foreignKeysOff")?,
                hazards,
            }))
        })
        .collect()
}

/// Refuses steps the runner cannot run as written, and returns the rest as
/// the runner runs them.
fn checked_steps(steps: Vec<Option<RawStep>>, dialect: Dialect) -> Result<Vec<Step>, Error> {
    let mut contract = false;
    let mut checked = Vec::with_capacity(steps.len());
    for (i, step) in steps.into_iter().enumerate() {
        let position = i + 1;
        let Some(step) = step else {
            return Err(refuse!("the plan's step {position} is null"));
        };
        if usize::try_from(step.index) != Ok(position) {
            return Err(refuse!(
                "the plan's step {position} has index {}",
                step.index
            ));
        }
        let phase = match step.phase.as_str() {
            "expand" if contract => {
                return Err(refuse!(
                    "step {position} is an expand step after a contract step"
                ));
            }
            "expand" => Phase::Expand,
            "contract" => {
                contract = true;
                Phase::Contract
            }
            other => {
                return Err(refuse!(
                    "step {position} has phase {other:?}; a step is expand or contract"
                ));
            }
        };
        if dialect == Dialect::Sqlite && !step.transactional {
            return Err(refuse!(
                "step {position} is not transactional; SQLite runs every step in a transaction"
            ));
        }
        if step.foreign_keys_off && dialect != Dialect::Sqlite {
            return Err(refuse!(
                "step {position} turns foreign keys off, which only a SQLite step does"
            ));
        }
        checked.push(Step {
            index: position,
            phase,
            op: step.op,
            subject: step.subject,
            statements: step.statements,
            transactional: step.transactional,
            recovery: step.recovery,
            foreign_keys_off: step.foreign_keys_off,
            hazards: step.hazards,
        });
    }
    Ok(checked)
}

/// An object's members, read as Go's decoder reads them into a struct: a
/// member that is absent or null is its zero value, and a member of another
/// type stops the read with an error that begins with `reading`.
struct Members<'a> {
    object: &'a BTreeMap<String, Value>,
    reading: &'static str,
}

impl<'a> Members<'a> {
    fn of(object: &'a BTreeMap<String, Value>, reading: &'static str) -> Self {
        Members { object, reading }
    }

    fn mistyped(&self, name: &str, wants: &str) -> Error {
        Error::Document(format!("{}: {name} is not {wants}", self.reading))
    }

    fn string(&self, name: &str) -> Result<String, Error> {
        match self.object.get(name) {
            None | Some(Value::Null) => Ok(String::new()),
            Some(Value::String(text)) => Ok(text.clone()),
            Some(_) => Err(self.mistyped(name, "a string")),
        }
    }

    fn boolean(&self, name: &str) -> Result<bool, Error> {
        match self.object.get(name) {
            None | Some(Value::Null) => Ok(false),
            Some(Value::Bool(value)) => Ok(*value),
            Some(_) => Err(self.mistyped(name, "a boolean")),
        }
    }

    fn integer(&self, name: &str) -> Result<i64, Error> {
        match self.object.get(name) {
            None | Some(Value::Null) => Ok(0),
            Some(Value::Number(literal)) => literal
                .parse()
                .map_err(|_| self.mistyped(name, "an integer")),
            Some(_) => Err(self.mistyped(name, "an integer")),
        }
    }

    fn array(&self, name: &str) -> Result<&'a [Value], Error> {
        match self.object.get(name) {
            None | Some(Value::Null) => Ok(&[]),
            Some(Value::Array(items)) => Ok(items),
            Some(_) => Err(self.mistyped(name, "an array")),
        }
    }

    fn strings(&self, name: &str) -> Result<Vec<String>, Error> {
        self.array(name)?
            .iter()
            .map(|item| match item {
                Value::String(text) => Ok(text.clone()),
                Value::Null => Ok(String::new()),
                _ => Err(self.mistyped(name, "an array of strings")),
            })
            .collect()
    }

    /// The members of an element of the array `name`, or `None` for a null
    /// element.
    fn object_or_null(&self, item: &'a Value, name: &str) -> Result<Option<Members<'a>>, Error> {
        match item {
            Value::Null => Ok(None),
            Value::Object(object) => Ok(Some(Members::of(object, self.reading))),
            _ => Err(self.mistyped(name, "an array of objects")),
        }
    }
}
