//! The core through its public API, with toy behaviors (D103): the read
//! exchange, vetoes and the order guards run in, a call between two
//! behaviors, two uses of one behavior on two fields and the own writes
//! their fields refuse, the config change that reads whether rows exist,
//! the define check, each command's change set, locks and the answers made
//! before them, deadlines, a call on another type's row and a create of
//! one, the keys a behavior makes, and the defects the core holds a
//! behavior to.

use std::collections::BTreeMap;

use serde_json::{json, Map, Value};

use superschematic_rules::{
    Action, Answer, Answers, Behavior, Caller, Change, Code, Command, ConfigCx, ConfigError,
    ConfigIssueKind, ConfigTarget, Cx, DeadlineChange, Declaration, GuardRequest, Hook, LockKey,
    Outcome, PermissionMatcher, ReadRequest, Refusal, Row, RowRef, RowWrite, SchemaIssue, Source,
    Step, Stop, SubRowChange, TypeSpec, UseSpec, ValidationIssue, ValidationRequest, Veto, View,
};

const NOW: i64 = 1_000_000;

// ---- the toy behaviors ----

// Counter: a count that starts at the config's start (or the create's)
// and moves only through add, which may need a permission and never passes
// max.
struct Counter;

struct CounterConfig {
    start: i64,
    max: i64,
    permission: Option<String>,
}

fn count_of(view: &View<'_, CounterConfig>) -> i64 {
    view.get("count").and_then(|v| v.as_i64()).unwrap_or(0)
}

impl Behavior for Counter {
    type Config = CounterConfig;

    fn declaration(&self) -> Declaration {
        Declaration::new("Counter")
            .value("count")
            .field("changedAt")
            .table("history")
            .operation("add", true)
            .operation("peek", false)
            .operation("sneak", false)
            .veto("over_max")
            .create_params()
    }

    fn parse_config(
        &self,
        raw: &Value,
        _target: &ConfigTarget<'_>,
    ) -> Result<CounterConfig, ConfigError> {
        // Its schema requires max, an integer.
        let max = match raw.get("max") {
            None => {
                return Err(ConfigError::Schema(vec![SchemaIssue::new(
                    "",
                    "must have required property 'max'",
                )]))
            }
            Some(max) => max.as_i64().ok_or_else(|| {
                ConfigError::Schema(vec![SchemaIssue::new("/max", "must be integer")])
            })?,
        };
        let start = raw.get("start").and_then(Value::as_i64).unwrap_or(0);
        if start > max {
            return Err(format!("start {start} is over max {max}").into());
        }
        Ok(CounterConfig {
            start,
            max,
            permission: raw
                .get("permission")
                .and_then(Value::as_str)
                .map(str::to_owned),
        })
    }

    fn config_change(
        &self,
        cx: &ConfigCx<'_>,
        before: Option<&CounterConfig>,
        after: Option<&CounterConfig>,
    ) -> Hook<Option<String>> {
        match (before, after) {
            (Some(before), Some(after)) => {
                if after.max >= before.max || !cx.has_rows()? {
                    return Ok(None);
                }
                Ok(Some(format!(
                    "a row may count up to {}, over the new max {}",
                    before.max, after.max
                )))
            }
            (None, _) => Ok(Some("the rows that exist have no count".to_owned())),
            (_, None) => Ok(None),
        }
    }

    fn check_create_params(&self, _config: &CounterConfig, params: &Value) -> Vec<SchemaIssue> {
        match params.get("start") {
            Some(start) if !start.is_i64() => {
                vec![SchemaIssue::new("/start", "must be an integer")]
            }
            _ => Vec::new(),
        }
    }

    fn check_params(
        &self,
        _config: &CounterConfig,
        operation: &str,
        params: &Value,
    ) -> Vec<SchemaIssue> {
        if operation != "add" {
            return Vec::new();
        }
        match params.get("by").and_then(Value::as_i64) {
            Some(by) if by > 0 => Vec::new(),
            _ => vec![SchemaIssue::new("/by", "must be a positive integer")],
        }
    }

    fn initialize(&self, cx: &Cx<'_, CounterConfig>, params: &Value) -> Hook<()> {
        let start = params
            .get("start")
            .and_then(Value::as_i64)
            .unwrap_or(cx.config().start);
        cx.set("count", start)?;
        cx.set("changedAt", cx.now())
    }

    fn guard(
        &self,
        view: &View<'_, CounterConfig>,
        request: &GuardRequest<'_>,
    ) -> Hook<Option<Veto>> {
        let Some(params) = request.operation_of(view.use_id(), "add") else {
            return Ok(None);
        };
        if let Some(permission) = &view.config().permission {
            if !view.can(permission)? {
                return Err(view.forbidden(format!(
                    "{} may not add to {} {}: it needs permission {permission}",
                    view.caller().subject,
                    view.type_name(),
                    view.key().unwrap_or_default()
                )));
            }
        }
        let by = params.get("by").and_then(Value::as_i64).unwrap_or(0);
        let count = count_of(view);
        if count + by > view.config().max {
            let mut details = Map::new();
            details.insert("count".into(), Value::from(count));
            details.insert("by".into(), Value::from(by));
            return Ok(Some(
                Veto::new(format!("{count} and {by} pass max {}", view.config().max))
                    .with_code("over_max")
                    .with_details(details),
            ));
        }
        Ok(None)
    }

    fn operation(&self, cx: &Cx<'_, CounterConfig>, name: &str, params: &Value) -> Hook<Value> {
        match name {
            "add" => {
                let by = params.get("by").and_then(Value::as_i64).unwrap_or(0);
                let count = count_of(cx) + by;
                cx.set("count", count)?;
                cx.set("changedAt", cx.now())?;
                let mut values = Map::new();
                values.insert("by".into(), Value::from(by));
                cx.insert_row("history", &format!("{}", cx.now()), values)?;
                cx.emit("added", json!({ "by": by, "count": count }))?;
                Ok(json!({ "count": count }))
            }
            "peek" => Ok(json!({ "count": count_of(cx) })),
            // A read-only operation that tries to write.
            "sneak" => {
                cx.set("count", 99)?;
                Ok(Value::Null)
            }
            _ => Err(cx.defect(format!("no operation {name}"))),
        }
    }
}

// Relay: an operation that moves a Counter use's count only through that
// use's add, and one that goes on past add's refusal.
struct Relay;

impl Behavior for Relay {
    type Config = String;

    fn declaration(&self) -> Declaration {
        Declaration::new("Relay")
            .operation("bump", true)
            .operation("tryBump", true)
            .operation("peekBump", false)
    }

    // The config names the Counter use it relays to, which must be one of
    // the type's uses.
    fn parse_config(&self, raw: &Value, target: &ConfigTarget<'_>) -> Result<String, ConfigError> {
        let name = raw
            .get("target")
            .and_then(Value::as_str)
            .ok_or("target names a use")?;
        if !target.uses().iter().any(|entry| entry.id() == name) {
            return Err(format!("target {name} is not a use of {}", target.type_name()).into());
        }
        Ok(name.to_owned())
    }

    fn operation(&self, cx: &Cx<'_, String>, name: &str, params: &Value) -> Hook<Value> {
        let by = params.get("by").cloned().unwrap_or(Value::from(1));
        match name {
            "bump" => cx.call(cx.config(), "add", &json!({ "by": by })),
            "tryBump" => {
                cx.emit("tried", json!({}))?;
                match cx.call(cx.config(), "add", &json!({ "by": by })) {
                    Err(Stop::Refuse(refusal)) => Ok(json!({ "refused": refusal.code.as_str() })),
                    other => other,
                }
            }
            "peekBump" => cx.call(cx.config(), "add", &json!({ "by": by })),
            _ => Err(cx.defect(format!("no operation {name}"))),
        }
    }
}

// Freeze: vetoes every write while the row's own field frozen is true, and
// holds title to a non-empty string.
struct Freeze;

impl Behavior for Freeze {
    type Config = ();

    fn declaration(&self) -> Declaration {
        Declaration::new("Freeze").veto("frozen")
    }

    fn parse_config(&self, _raw: &Value, _target: &ConfigTarget<'_>) -> Result<(), ConfigError> {
        Ok(())
    }

    fn guard(&self, view: &View<'_, ()>, request: &GuardRequest<'_>) -> Hook<Option<Veto>> {
        if matches!(request, GuardRequest::Create { .. }) {
            return Ok(None);
        }
        if view.own_field("frozen") == Some(Value::Bool(true)) {
            return Ok(Some(Veto::new("it is frozen").with_code("frozen")));
        }
        Ok(None)
    }

    // Emits what changed, with the title as the row has it after.
    fn after_change(&self, cx: &Cx<'_, ()>, change: &Change<'_>) -> Hook<()> {
        cx.emit(
            change.kind(),
            json!({ "title": cx.own_field("title"), "version": cx.version() }),
        )
    }

    fn validate(
        &self,
        _view: &View<'_, ()>,
        request: &ValidationRequest<'_>,
    ) -> Hook<Vec<ValidationIssue>> {
        let own = match request {
            ValidationRequest::Create { own } => own,
            ValidationRequest::Update { after, .. } => after,
            _ => return Ok(Vec::new()),
        };
        Ok(match own.get("title") {
            Some(Value::String(title)) if !title.is_empty() => Vec::new(),
            _ => vec![ValidationIssue::new(
                "title",
                "required",
                "a title is required",
            )],
        })
    }
}

// Mirror: copies another row's count into its own field, by a read.
struct Mirror;

impl Behavior for Mirror {
    type Config = Value;

    fn declaration(&self) -> Declaration {
        Declaration::new("Mirror")
            .field("copied")
            .operation("copy", true)
    }

    fn parse_config(&self, raw: &Value, _target: &ConfigTarget<'_>) -> Result<Value, ConfigError> {
        Ok(raw.clone())
    }

    fn operation(&self, cx: &Cx<'_, Value>, _name: &str, params: &Value) -> Hook<Value> {
        let from = params
            .get("from")
            .and_then(Value::as_str)
            .unwrap_or_default();
        let Some(row) = cx.read_row(cx.type_name(), from)? else {
            return Err(cx.veto(format!("{from} does not exist")));
        };
        let copied = row
            .owned("Counter", "count")
            .cloned()
            .unwrap_or(Value::Null);
        cx.set("copied", copied.clone())?;
        Ok(copied)
    }
}

// Stock: a rule across rows, which takes its lock before it reads, and
// arms and handles a deadline.
struct Stock;

impl Behavior for Stock {
    type Config = Value;

    fn declaration(&self) -> Declaration {
        Declaration::new("Stock")
            .field("held")
            .operation("reserve", true)
            .operation("release", true)
            .veto("sold_out")
    }

    fn parse_config(&self, raw: &Value, _target: &ConfigTarget<'_>) -> Result<Value, ConfigError> {
        Ok(raw.clone())
    }

    fn operation(&self, cx: &Cx<'_, Value>, name: &str, _params: &Value) -> Hook<Value> {
        match name {
            "reserve" => {
                cx.lock("Stock/shelf")?;
                if !cx.has_rows("Shelf")? {
                    return Err(cx.veto(Veto::new("nothing is stocked").with_code("sold_out")));
                }
                cx.set("held", true)?;
                cx.arm("expire", cx.now() + 60_000, json!({ "held": true }))?;
                Ok(Value::Bool(true))
            }
            _ => {
                cx.set("held", false)?;
                cx.clear("expire")?;
                Ok(Value::Bool(false))
            }
        }
    }

    fn deadline(&self, cx: &Cx<'_, Value>, name: &str, payload: &Value) -> Hook<()> {
        if name == "expire" && payload.get("held") == Some(&Value::Bool(true)) {
            cx.set("held", false)?;
            cx.emit("expired", json!({}))?;
        }
        Ok(())
    }
}

// Loop: an operation that calls itself, to the core's depth.
struct Loop;

impl Behavior for Loop {
    type Config = Value;

    fn declaration(&self) -> Declaration {
        Declaration::new("Loop")
            .operation("spin", true)
            .veto("undeclared_elsewhere")
    }

    fn parse_config(&self, raw: &Value, _target: &ConfigTarget<'_>) -> Result<Value, ConfigError> {
        Ok(raw.clone())
    }

    fn operation(&self, cx: &Cx<'_, Value>, name: &str, params: &Value) -> Hook<Value> {
        if params.get("veto").is_some() {
            return Err(cx.veto(Veto::new("no").with_code("not_listed")));
        }
        cx.call(cx.use_id(), name, params)
    }
}

// Bin: the units an item has on hand, which only take moves and never
// below none. Each take is noted in its sub-table under a key it makes,
// and each writing operation on it emits. Its config may name a type whose
// calls it refuses.
struct Bin;

struct BinConfig {
    refuse_from: Option<String>,
}

fn units_of(view: &View<'_, BinConfig>) -> i64 {
    view.get("units").and_then(|v| v.as_i64()).unwrap_or(0)
}

impl Behavior for Bin {
    type Config = BinConfig;

    fn declaration(&self) -> Declaration {
        Declaration::new("Bin")
            .value("units")
            .table("takes")
            .operation("take", true)
            .operation("count", false)
            .veto("short")
            .veto("refused")
            .create_params()
    }

    fn parse_config(
        &self,
        raw: &Value,
        _target: &ConfigTarget<'_>,
    ) -> Result<BinConfig, ConfigError> {
        Ok(BinConfig {
            refuse_from: raw
                .get("refuseFrom")
                .and_then(Value::as_str)
                .map(str::to_owned),
        })
    }

    fn initialize(&self, cx: &Cx<'_, BinConfig>, params: &Value) -> Hook<()> {
        cx.set(
            "units",
            params.get("units").and_then(Value::as_i64).unwrap_or(0),
        )
    }

    fn guard(&self, view: &View<'_, BinConfig>, request: &GuardRequest<'_>) -> Hook<Option<Veto>> {
        let Some(params) = request.operation_of(view.use_id(), "take") else {
            return Ok(None);
        };
        if let (
            GuardRequest::Operation {
                caller: Some(caller),
                caller_type: Some(from),
                ..
            },
            Some(refused),
        ) = (request, &view.config().refuse_from)
        {
            if from == refused {
                return Ok(Some(
                    Veto::new(format!("{from}'s {caller} may not take")).with_code("refused"),
                ));
            }
        }
        let by = params.get("by").and_then(Value::as_i64).unwrap_or(0);
        let units = units_of(view);
        Ok((by > units)
            .then(|| Veto::new(format!("{units} on hand, {by} asked")).with_code("short")))
    }

    fn operation(&self, cx: &Cx<'_, BinConfig>, name: &str, params: &Value) -> Hook<Value> {
        let units = units_of(cx);
        if name == "count" {
            return Ok(json!({ "units": units }));
        }
        let by = params.get("by").and_then(Value::as_i64).unwrap_or(0);
        cx.set("units", units - by)?;
        let mut values = Map::new();
        values.insert("by".into(), Value::from(by));
        cx.insert_row("takes", &cx.new_key("take")?, values)?;
        Ok(json!({ "units": units - by }))
    }

    fn after_change(&self, cx: &Cx<'_, BinConfig>, change: &Change<'_>) -> Hook<()> {
        if let Change::Operation { operation, .. } = change {
            cx.emit(
                "changed",
                json!({ "operation": operation, "units": units_of(cx) }),
            )?;
        }
        Ok(())
    }
}

// Picker: an order's picks from items' bins, through Bin's take on the
// item's row, and the items it stocks, through a create of one.
struct Picker;

fn picked_of(view: &View<'_, Value>) -> i64 {
    view.get("picked").and_then(|v| v.as_i64()).unwrap_or(0)
}

impl Behavior for Picker {
    type Config = Value;

    fn declaration(&self) -> Declaration {
        Declaration::new("Picker")
            .field("picked")
            .table("notes")
            .operation("pick", true)
            .operation("pickTwice", true)
            .operation("tryPick", true)
            .operation("peek", false)
            .operation("peekTake", false)
            .operation("stock", true)
            .operation("stockAndPick", true)
            .operation("note", true)
            .operation("configOf", false)
    }

    fn parse_config(&self, raw: &Value, _target: &ConfigTarget<'_>) -> Result<Value, ConfigError> {
        Ok(raw.clone())
    }

    fn operation(&self, cx: &Cx<'_, Value>, name: &str, params: &Value) -> Hook<Value> {
        let text = |key: &str| params.get(key).and_then(Value::as_str).unwrap_or_default();
        let by = params.get("by").cloned().unwrap_or(Value::from(1));
        let item = params.get("type").and_then(Value::as_str).unwrap_or("Item");
        let take = |by: &Value| cx.call_on(item, text("bin"), "Bin", "take", &json!({ "by": by }));
        match name {
            "pick" => {
                let taken = take(&by)?;
                cx.set("picked", picked_of(cx) + by.as_i64().unwrap_or(0))?;
                Ok(taken)
            }
            "pickTwice" => {
                take(&by)?;
                take(&by)
            }
            "tryPick" => {
                cx.emit("tried", json!({}))?;
                match take(&by) {
                    Err(Stop::Refuse(refusal)) => Ok(json!({ "refused": refusal.code.as_str() })),
                    other => other,
                }
            }
            "peek" => cx.call_on(item, text("bin"), "Bin", "count", &json!({})),
            "peekTake" => take(&by),
            "stock" | "stockAndPick" => {
                let own = object(json!({ "title": format!("Bin {}", text("bin")) }));
                let units = params.get("units").cloned().unwrap_or(Value::from(0));
                let create = object(json!({ "Bin": { "units": units } }));
                cx.create_row(item, text("bin"), &own, &create)?;
                if params.get("again").is_some() {
                    cx.create_row(item, text("bin"), &own, &create)?;
                }
                if name == "stockAndPick" {
                    return take(&by);
                }
                Ok(Value::Null)
            }
            "note" => {
                let first = cx.new_key("note")?;
                let second = cx.new_key("note")?;
                cx.insert_row("notes", &first, Map::new())?;
                cx.insert_row("notes", &second, Map::new())?;
                Ok(json!([first, second]))
            }
            "configOf" => Ok(cx.config_of(item, text("use"))?.unwrap_or(Value::Null)),
            _ => Err(cx.defect(format!("no operation {name}"))),
        }
    }
}

// ---- the harness ----

fn core() -> superschematic_rules::Core {
    superschematic_rules::Core::new()
        .with(Counter)
        .with(Relay)
        .with(Freeze)
        .with(Mirror)
        .with(Stock)
        .with(Loop)
        .with(Bin)
        .with(Picker)
}

fn alice() -> Caller {
    Caller::new("alice", Vec::<String>::new())
}

fn command(caller: Caller, action: Action) -> Command {
    Command::new(caller, NOW, action)
}

fn invoke(key: &str, use_id: &str, operation: &str, params: Value) -> Action {
    Action::Operation {
        key: key.into(),
        use_id: use_id.into(),
        operation: operation.into(),
        params,
    }
}

fn object(value: Value) -> Map<String, Value> {
    value.as_object().cloned().unwrap_or_default()
}

fn counter_spec(config: Value) -> TypeSpec {
    TypeSpec::new("Order").with(UseSpec::on_type("Counter", config))
}

fn row(own: Value, owned: Value, version: i64) -> Row {
    let owned: BTreeMap<String, Map<String, Value>> = object(owned)
        .into_iter()
        .map(|(id, fields)| (id, object(fields)))
        .collect();
    Row {
        own: object(own),
        owned,
        version,
    }
}

// Rows and types in memory, and every read and lock the core asked for,
// in order.
#[derive(Default)]
struct Memory {
    rows: BTreeMap<(String, String), Row>,
    specs: BTreeMap<String, TypeSpec>,
    log: Vec<String>,
}

impl Memory {
    fn with(mut self, type_name: &str, key: &str, row: Row) -> Self {
        self.rows.insert((type_name.into(), key.into()), row);
        self
    }

    fn with_spec(mut self, spec: TypeSpec) -> Self {
        self.specs.insert(spec.name.clone(), spec);
        self
    }
}

impl Source for Memory {
    fn answer(&mut self, request: &ReadRequest) -> Result<Answer, Refusal> {
        match request {
            ReadRequest::Row { type_name, key } => {
                self.log.push(format!("row {type_name} {key}"));
                Ok(Answer::Row(
                    self.rows.get(&(type_name.clone(), key.clone())).cloned(),
                ))
            }
            ReadRequest::HasRows { type_name } => {
                self.log.push(format!("has {type_name}"));
                Ok(Answer::HasRows(
                    self.rows.keys().any(|(name, _)| name == type_name),
                ))
            }
            ReadRequest::Spec { type_name } => {
                self.log.push(format!("spec {type_name}"));
                Ok(Answer::Spec(self.specs.get(type_name).cloned()))
            }
            other => Err(Refusal::internal(format!("an unexpected read: {other:?}"))),
        }
    }

    fn lock(&mut self, key: &LockKey) -> Result<(), Refusal> {
        self.log.push(format!("lock {}", key.as_str()));
        Ok(())
    }
}

fn changed(outcome: Outcome) -> superschematic_rules::ChangeSet {
    match outcome {
        Outcome::Changed(changes) => *changes,
        Outcome::Refused(refusal) => panic!("refused: {}", refusal.error_json()),
    }
}

fn refused(outcome: Outcome) -> Refusal {
    match outcome {
        Outcome::Refused(refusal) => refusal,
        Outcome::Changed(changes) => panic!("changed: {changes:?}"),
    }
}

fn order_at(count: i64, version: i64) -> Row {
    row(
        json!({"title": "Desk"}),
        json!({"Counter": {"count": count, "changedAt": 1}}),
        version,
    )
}

// ---- a core's id ----

/// A core's id is its own and changes when it adds a behavior, so a host
/// that keeps what it learns of a core by its id learns again of one made
/// where a dropped one was, or of one that changed.
#[test]
fn a_cores_id_is_its_own_and_changes_when_it_adds_a_behavior() {
    let mut one = superschematic_rules::Core::new().with(Counter);
    let other = superschematic_rules::Core::new().with(Counter);
    assert_ne!(one.id(), other.id());

    let before = one.id();
    one.push(Relay);
    assert_ne!(one.id(), before);
    assert_ne!(one.id(), other.id());

    let at = std::ptr::from_ref(&one) as usize;
    let dropped = one.id();
    one = superschematic_rules::Core::new().with(Counter);
    assert_eq!(std::ptr::from_ref(&one) as usize, at);
    assert_ne!(one.id(), dropped);
}

// ---- the read exchange ----

#[test]
fn a_run_that_needs_a_read_completes_once_answered() {
    let core = core();
    let spec = counter_spec(json!({"max": 10}));
    let add = command(alice(), invoke("o1", "Counter", "add", json!({"by": 2})));

    let mut answers = Answers::new();
    let first = core.run(&spec, &add, &answers);
    assert_eq!(first, Step::Read(vec![ReadRequest::row("Order", "o1")]));
    // The same inputs give the same step.
    assert_eq!(core.run(&spec, &add, &answers), first);

    answers.answer(
        ReadRequest::row("Order", "o1"),
        Answer::Row(Some(order_at(3, 7))),
    );
    let Step::Done(outcome) = core.run(&spec, &add, &answers) else {
        panic!("done");
    };
    let changes = changed(outcome);
    assert_eq!(changes.result, json!({"count": 5}));
    assert_eq!(
        changes.rows,
        [RowWrite::Update {
            type_name: "Order".into(),
            key: "o1".into(),
            expected_version: 7,
            own: Map::new(),
            owned: [(
                "Counter".to_owned(),
                object(json!({"count": 5, "changedAt": NOW}))
            )]
            .into_iter()
            .collect(),
        }]
    );
    assert_eq!(changes.sub_rows.len(), 1);
    assert_eq!(changes.sub_rows[0].row, RowRef::new("Order", Some("o1")));
    assert_eq!(changes.sub_rows[0].table, "history");
    assert_eq!(
        changes.sub_rows[0].change,
        SubRowChange::Insert {
            values: object(json!({"by": 2}))
        }
    );
    assert_eq!(changes.events.len(), 1);
    assert_eq!(changes.events[0].kind, "added");
    assert_eq!(changes.events[0].use_id, "Counter");
    assert_eq!(changes.events[0].row, RowRef::new("Order", Some("o1")));
}

#[test]
fn a_behaviors_own_read_follows_the_rows() {
    let core = core();
    let spec = TypeSpec::new("Order")
        .with(UseSpec::on_type("Counter", json!({"max": 10})))
        .with(UseSpec::on_type("Mirror", json!({})));
    let mut memory =
        Memory::default()
            .with("Order", "o1", order_at(1, 1))
            .with("Order", "o2", order_at(4, 1));
    let copy = command(
        alice(),
        invoke("o1", "Mirror", "copy", json!({"from": "o2"})),
    );
    let changes = changed(core.drive(&spec, &copy, &mut memory));
    assert_eq!(memory.log, ["row Order o1", "row Order o2"]);
    assert_eq!(changes.result, json!(4));
    assert_eq!(changes.owned("Mirror", "copied"), Some(&json!(4)));

    // A row the read finds none of is the behavior's to refuse.
    let missing = command(
        alice(),
        invoke("o1", "Mirror", "copy", json!({"from": "o9"})),
    );
    let refusal = refused(core.drive(&spec, &missing, &mut memory));
    assert_eq!(refusal.code, Code::Vetoed);
    assert_eq!(
        refusal.message,
        "behavior Mirror vetoes copy of Order o1: o9 does not exist"
    );
}

#[test]
fn a_lock_comes_before_the_reads_it_serializes() {
    let core = core();
    let spec = TypeSpec::new("Order").with(UseSpec::on_type("Stock", json!({})));
    let reserve = command(alice(), invoke("o1", "Stock", "reserve", json!({})));
    let base = Memory::default().with("Order", "o1", row(json!({}), json!({}), 1));

    // Step by step: the row, then the lock, then the read under it.
    let mut answers = Answers::new();
    assert_eq!(
        core.run(&spec, &reserve, &answers).reads(),
        [ReadRequest::row("Order", "o1")]
    );
    answers.answer(
        ReadRequest::row("Order", "o1"),
        Answer::Row(Some(row(json!({}), json!({}), 1))),
    );
    assert_eq!(
        core.run(&spec, &reserve, &answers).locks(),
        [LockKey::from("Stock/shelf")]
    );
    // The host takes the lock and forgets nothing; the core asks again
    // for the read it answered before the lock, then for the one under it.
    answers.hold(LockKey::from("Stock/shelf"));
    assert_eq!(
        core.run(&spec, &reserve, &answers).reads(),
        [ReadRequest::row("Order", "o1")]
    );
    answers.answer(
        ReadRequest::row("Order", "o1"),
        Answer::Row(Some(row(json!({}), json!({}), 1))),
    );
    assert_eq!(
        core.run(&spec, &reserve, &answers).reads(),
        [ReadRequest::has_rows("Shelf")]
    );

    // drive reads again under the lock.
    let mut memory = base;
    let refusal = refused(core.drive(&spec, &reserve, &mut memory));
    assert_eq!(
        memory.log,
        [
            "row Order o1",
            "lock Stock/shelf",
            "row Order o1",
            "has Shelf"
        ]
    );
    assert_eq!(
        refusal.details.as_deref().and_then(|d| d.get("vetoCode")),
        Some(&json!("sold_out"))
    );

    let mut stocked = Memory::default()
        .with("Order", "o1", row(json!({}), json!({}), 1))
        .with("Shelf", "s1", row(json!({}), json!({}), 1));
    let changes = changed(core.drive(&spec, &reserve, &mut stocked));
    assert_eq!(changes.owned("Stock", "held"), Some(&json!(true)));
    assert_eq!(
        changes.deadlines,
        [DeadlineChange::Arm {
            row: RowRef::new("Order", Some("o1")),
            use_id: "Stock".into(),
            name: "expire".into(),
            at: NOW + 60_000,
            payload: json!({"held": true}),
        }]
    );
}

// ---- vetoes ----

#[test]
fn a_guard_vetoes_and_the_first_veto_wins() {
    let core = core();
    let spec = counter_spec(json!({"max": 5}));
    let mut memory = Memory::default().with("Order", "o1", order_at(4, 2));
    let add = command(alice(), invoke("o1", "Counter", "add", json!({"by": 2})));
    let refusal = refused(core.drive(&spec, &add, &mut memory));
    assert_eq!(
        refusal.error_json(),
        json!({"class": "BehaviorVetoError", "code": "vetoed", "message": "behavior Counter vetoes add of Order o1: 4 and 2 pass max 5", "details": {"behavior": "Counter", "action": "add", "reason": "4 and 2 pass max 5", "vetoCode": "over_max", "vetoDetails": {"count": 4, "by": 2}}})
    );
    assert_eq!(refusal.status, 409);

    // Freeze listed first vetoes before Counter is asked.
    let frozen_spec = TypeSpec::new("Order")
        .with(UseSpec::on_type("Freeze", json!({})))
        .with(UseSpec::on_type("Counter", json!({"max": 5})));
    let mut frozen = Memory::default().with(
        "Order",
        "o1",
        row(
            json!({"title": "Desk", "frozen": true}),
            json!({"Counter": {"count": 4}}),
            2,
        ),
    );
    let refusal = refused(core.drive(&frozen_spec, &add, &mut frozen));
    assert_eq!(
        refusal.message,
        "behavior Freeze vetoes add of Order o1: it is frozen"
    );

    // An update and a delete ask the guards too.
    let delete = command(alice(), Action::Delete { key: "o1".into() });
    let refusal = refused(core.drive(&frozen_spec, &delete, &mut frozen));
    assert_eq!(
        refusal.message,
        "behavior Freeze vetoes delete of Order o1: it is frozen"
    );
}

#[test]
fn a_guard_refuses_a_caller_without_the_permission() {
    let core = core();
    let spec = counter_spec(json!({"max": 10, "permission": "orders.count"}));
    let add = |caller| command(caller, invoke("o1", "Counter", "add", json!({"by": 1})));
    let mut memory = Memory::default().with("Order", "o1", order_at(0, 1));

    let refusal = refused(core.drive(&spec, &add(alice()), &mut memory));
    assert_eq!(
        refusal.error_json(),
        json!({"class": "EngineError", "code": "forbidden", "message": "alice may not add to Order o1: it needs permission orders.count"})
    );
    // A permission covers the ones nested under it.
    assert!(core
        .drive(&spec, &add(Caller::new("olga", ["orders"])), &mut memory)
        .changes()
        .is_some());
    // A service standing in for an end user holds none.
    assert_eq!(
        core.drive(&spec, &add(Caller::standing_in("svc")), &mut memory)
            .refusal()
            .map(|r| r.code),
        Some(Code::Forbidden)
    );

    // A deployment's matcher: root holds everything, nothing else does.
    struct Root;
    impl PermissionMatcher for Root {
        fn matches(&self, held: &[String], _required: &[String]) -> bool {
            held.iter().any(|p| p == "root")
        }
    }
    let rooted = superschematic_rules::Core::new()
        .with(Counter)
        .with_matcher(Root);
    assert!(rooted
        .drive(
            &spec,
            &add(Caller::new("sam", ["orders.count"])),
            &mut memory
        )
        .refusal()
        .is_some());
    assert!(rooted
        .drive(&spec, &add(Caller::new("rita", ["root"])), &mut memory)
        .changes()
        .is_some());
}

#[test]
fn parameters_are_checked_before_any_guard() {
    let core = core();
    let spec = TypeSpec::new("Order")
        .with(UseSpec::on_type("Freeze", json!({})))
        .with(UseSpec::on_type("Counter", json!({"max": 5})));
    let mut memory = Memory::default().with(
        "Order",
        "o1",
        row(json!({"title": "Desk", "frozen": true}), json!({}), 1),
    );
    let add = command(alice(), invoke("o1", "Counter", "add", json!({"by": 0})));
    assert_eq!(
        refused(core.drive(&spec, &add, &mut memory)).error_json(),
        json!({"class": "OperationParamsError", "code": "invalid_argument", "message": "operation add of behavior Counter: /by: must be a positive integer", "details": {"issues": [{"path": "/by", "message": "must be a positive integer"}]}})
    );
}

// ---- calls ----

#[test]
fn a_call_runs_the_called_operations_guards_and_merges_its_effects() {
    let core = core();
    let spec = TypeSpec::new("Order")
        .with(UseSpec::on_type("Counter", json!({"max": 5})))
        .with(UseSpec::on_type("Relay", json!({"target": "Counter"})));
    let mut memory = Memory::default().with("Order", "o1", order_at(1, 3));

    let bump = command(alice(), invoke("o1", "Relay", "bump", json!({"by": 2})));
    let changes = changed(core.drive(&spec, &bump, &mut memory));
    assert_eq!(changes.result, json!({"count": 3}));
    assert_eq!(changes.owned("Counter", "count"), Some(&json!(3)));
    assert_eq!(changes.events.len(), 1);
    assert_eq!(changes.events[0].use_id, "Counter");

    // Counter's guard runs for the call: the call is refused as Counter's.
    let over = command(alice(), invoke("o1", "Relay", "bump", json!({"by": 9})));
    let refusal = refused(core.drive(&spec, &over, &mut memory));
    assert_eq!(
        refusal.message,
        "behavior Counter vetoes add of Order o1: 1 and 9 pass max 5"
    );

    // A refused call leaves none of its effects, and the caller's stay.
    let try_over = command(alice(), invoke("o1", "Relay", "tryBump", json!({"by": 9})));
    let changes = changed(core.drive(&spec, &try_over, &mut memory));
    assert_eq!(changes.result, json!({"refused": "vetoed"}));
    assert_eq!(changes.owned("Counter", "count"), None);
    assert!(changes.sub_rows.is_empty());
    let kinds: Vec<&str> = changes.events.iter().map(|e| e.kind.as_str()).collect();
    assert_eq!(kinds, ["tried"]);

    // A read-only operation calls no operation that writes.
    let peek = command(alice(), invoke("o1", "Relay", "peekBump", json!({})));
    assert_eq!(
        refused(core.drive(&spec, &peek, &mut memory)).message,
        "behavior Relay: a read-only operation cannot call Counter.add, which writes"
    );
}

#[test]
fn calls_nest_at_most_sixteen_deep() {
    let core = core();
    let spec = TypeSpec::new("Order").with(UseSpec::on_type("Loop", json!({})));
    let mut memory = Memory::default().with("Order", "o1", row(json!({}), json!({}), 1));
    let spin = command(alice(), invoke("o1", "Loop", "spin", json!({})));
    let refusal = refused(core.drive(&spec, &spin, &mut memory));
    assert_eq!(refusal.code, Code::BehaviorError);
    assert_eq!(refusal.status, 500);
    assert_eq!(
        refusal.message,
        "behavior Loop: operation spin: calls nest more than 16 deep"
    );
}

// ---- two uses of one behavior ----

#[test]
fn two_uses_of_one_behavior_on_two_fields_keep_apart() {
    let core = core();
    let spec = TypeSpec::new("Post")
        .with(UseSpec::on_field("likes", "Counter", json!({"max": 3})))
        .with(UseSpec::on_field(
            "views",
            "Counter",
            json!({"max": 100, "start": 10}),
        ));

    let create = command(
        alice(),
        Action::Create {
            id: Some("p1".into()),
            own: object(json!({"title": "Hello"})),
            params: object(json!({"likes": {"start": 1}})),
        },
    );
    let changes = changed(core.drive(&spec, &create, &mut Memory::default()));
    let [RowWrite::Create {
        key, own, owned, ..
    }] = changes.rows.as_slice()
    else {
        panic!("a create");
    };
    assert_eq!(key.as_deref(), Some("p1"));
    assert_eq!(own, &object(json!({"title": "Hello"})));
    assert_eq!(
        owned["likes"],
        object(json!({"count": 1, "changedAt": NOW}))
    );
    assert_eq!(
        owned["views"],
        object(json!({"count": 10, "changedAt": NOW}))
    );

    // An add on views moves views alone; likes' guard leaves it be, though
    // likes is at its max.
    let mut memory = Memory::default().with(
        "Post",
        "p1",
        row(
            json!({"title": "Hello"}),
            json!({"likes": {"count": 3}, "views": {"count": 10}}),
            1,
        ),
    );
    let view = command(alice(), invoke("p1", "views", "add", json!({"by": 5})));
    let changes = changed(core.drive(&spec, &view, &mut memory));
    assert_eq!(changes.owned("views", "count"), Some(&json!(15)));
    assert_eq!(changes.owned("likes", "count"), None);

    let like = command(alice(), invoke("p1", "likes", "add", json!({"by": 1})));
    let refusal = refused(core.drive(&spec, &like, &mut memory));
    assert_eq!(
        refusal.message,
        "behavior Counter vetoes add of Post p1: 3 and 1 pass max 3"
    );
    assert_eq!(
        refusal.details.as_deref().and_then(|d| d.get("use")),
        Some(&json!("likes"))
    );
}

#[test]
fn an_own_field_named_as_a_field_anchor_is_its_behaviors_and_no_write_sets_it() {
    let core = core();
    let spec = TypeSpec::new("Post")
        .with(UseSpec::on_type("Freeze", json!({})))
        .with(UseSpec::on_field("likes", "Counter", json!({"max": 3})))
        .with(UseSpec::on_field("views", "Counter", json!({"max": 100})));
    let issue = |field: &str| json!({"path": field, "rule": "owned", "message": format!("{field} is behavior Counter's field: only its operations change it")});
    // A create that names one is refused, with every use's issues.
    let create = command(
        alice(),
        Action::Create {
            id: Some("p1".into()),
            own: object(json!({"likes": 3, "views": 9})),
            params: Map::new(),
        },
    );
    let refusal = refused(core.drive(&spec, &create, &mut Memory::default()));
    assert_eq!(refusal.code, Code::InvalidInstance);
    assert_eq!(
        refusal.details.as_deref().map(|d| d["issues"].clone()),
        Some(json!([
            issue("likes"),
            issue("views"),
            {"path": "title", "rule": "required", "message": "a title is required"}
        ]))
    );
    // So is an update that sets one or clears it.
    let mut memory = Memory::default().with(
        "Post",
        "p1",
        row(
            json!({"title": "Hello"}),
            json!({"likes": {"count": 1}, "views": {"count": 10}}),
            2,
        ),
    );
    for patch in [json!({"views": 50}), json!({"views": null})] {
        let update = command(
            alice(),
            Action::Update {
                key: "p1".into(),
                patch: object(patch),
            },
        );
        let refusal = refused(core.drive(&spec, &update, &mut memory));
        assert_eq!(
            refusal.message,
            "Post: views: views is behavior Counter's field: only its operations change it"
        );
    }
    // A type anchor's fields sit apart from the own fields, which may take
    // any name.
    let typed = TypeSpec::new("Order")
        .with(UseSpec::on_type("Freeze", json!({})))
        .with(UseSpec::on_type("Counter", json!({"max": 3})));
    let named = command(
        alice(),
        Action::Create {
            id: Some("o1".into()),
            own: object(json!({"title": "Desk", "count": 7, "Counter": true})),
            params: Map::new(),
        },
    );
    let changes = changed(core.drive(&typed, &named, &mut Memory::default()));
    assert_eq!(changes.owned("Counter", "count"), Some(&json!(0)));
}

#[test]
fn a_behavior_that_declares_no_value_field_is_not_anchored_on_a_field() {
    let spec = TypeSpec::new("Order")
        .with(UseSpec::on_type("Counter", json!({"max": 3})))
        .with(UseSpec::on_field(
            "relay",
            "Relay",
            json!({"target": "Counter"}),
        ));
    let refusal = refused(
        core()
            .run(&spec, &command(alice(), Action::Define), &Answers::new())
            .outcome()
            .cloned()
            .unwrap(),
    );
    assert_eq!(
        refusal.details.as_deref().map(|d| d["issues"].clone()),
        Some(json!([
            {"use": "relay", "index": 1, "behavior": "Relay", "kind": "use", "path": "", "message": "type Order: behavior Relay declares no value field, so it cannot be anchored on field relay"}
        ]))
    );
}

// ---- other rows ----

fn picking() -> (TypeSpec, Memory) {
    let order = TypeSpec::new("Order").with(UseSpec::on_type("Picker", json!({})));
    let memory = Memory::default()
        .with_spec(TypeSpec::new("Item").with(UseSpec::on_type("Bin", json!({}))))
        .with(
            "Order",
            "o1",
            row(
                json!({"title": "Desk"}),
                json!({"Picker": {"picked": 0}}),
                3,
            ),
        )
        .with(
            "Item",
            "i1",
            row(json!({}), json!({"Bin": {"units": 4}}), 5),
        );
    (order, memory)
}

fn pick(operation: &str, params: Value) -> Command {
    command(alice(), invoke("o1", "Picker", operation, params)).with_request("r1")
}

#[test]
fn a_call_on_another_types_row_runs_its_rules_and_writes_both_rows() {
    let core = core();
    let (order, mut memory) = picking();
    let changes = changed(core.drive(
        &order,
        &pick("pick", json!({"bin": "i1", "by": 3})),
        &mut memory,
    ));
    // The row is read after its type's version, each once.
    assert_eq!(memory.log, ["row Order o1", "spec Item", "row Item i1"]);
    assert_eq!(changes.result, json!({"units": 1}));
    // Each row the command writes is held to the version it was read at.
    assert_eq!(
        changes.rows,
        [
            RowWrite::Update {
                type_name: "Order".into(),
                key: "o1".into(),
                expected_version: 3,
                own: Map::new(),
                owned: [("Picker".to_owned(), object(json!({"picked": 3})))]
                    .into_iter()
                    .collect(),
            },
            RowWrite::Update {
                type_name: "Item".into(),
                key: "i1".into(),
                expected_version: 5,
                own: Map::new(),
                owned: [("Bin".to_owned(), object(json!({"units": 1})))]
                    .into_iter()
                    .collect(),
            },
        ]
    );
    // The sub-table row, its key and the event are the item's, its
    // after-change hook having run.
    assert_eq!(changes.sub_rows.len(), 1);
    assert_eq!(changes.sub_rows[0].row, RowRef::new("Item", Some("i1")));
    assert_eq!(changes.sub_rows[0].key, "r1:Bin:take:1");
    assert_eq!(changes.events.len(), 1);
    assert_eq!(changes.events[0].row, RowRef::new("Item", Some("i1")));
    assert_eq!(
        changes.events[0].payload,
        json!({"operation": "take", "units": 1})
    );

    // The item's guard runs for the call, and refuses it as the item's.
    let over = refused(core.drive(
        &order,
        &pick("pick", json!({"bin": "i1", "by": 9})),
        &mut memory,
    ));
    assert_eq!(
        over.message,
        "behavior Bin vetoes take of Item i1: 4 on hand, 9 asked"
    );

    // A refused call leaves none of its effects, and the caller's stay.
    let tried = changed(core.drive(
        &order,
        &pick("tryPick", json!({"bin": "i1", "by": 9})),
        &mut memory,
    ));
    assert_eq!(tried.result, json!({"refused": "vetoed"}));
    assert_eq!(tried.rows.len(), 1);
    assert_eq!(tried.rows[0].row(), RowRef::new("Order", Some("o1")));
    assert!(tried.sub_rows.is_empty());
    let kinds: Vec<&str> = tried.events.iter().map(|e| e.kind.as_str()).collect();
    assert_eq!(kinds, ["tried"]);

    // A second call on the row sees the first's writes; the row is written
    // once, held to the version it was read at.
    let twice = changed(core.drive(
        &order,
        &pick("pickTwice", json!({"bin": "i1", "by": 2})),
        &mut memory,
    ));
    assert_eq!(twice.result, json!({"units": 0}));
    assert_eq!(twice.rows.len(), 2);
    assert_eq!(twice.rows[1].expected_version(), Some(5));
    assert_eq!(twice.rows[1].owned("Bin", "units"), Some(&json!(0)));
    let keys: Vec<&str> = twice.sub_rows.iter().map(|sub| sub.key.as_str()).collect();
    assert_eq!(keys, ["r1:Bin:take:1", "r1:Bin:take:2"]);
}

#[test]
fn a_call_on_another_row_is_held_to_what_it_reads_and_writes() {
    let core = core();
    let (order, mut memory) = picking();
    // A read-only call reads and writes no row.
    let peeked = changed(core.drive(&order, &pick("peek", json!({"bin": "i1"})), &mut memory));
    assert_eq!(peeked.result, json!({"units": 4}));
    assert!(peeked.rows.is_empty());
    // A read-only operation calls no operation that writes, on any row.
    assert_eq!(
        refused(core.drive(&order, &pick("peekTake", json!({"bin": "i1"})), &mut memory)).message,
        "behavior Picker: a read-only operation cannot call Bin.take of Item i1, which writes"
    );
    // A row or a type that does not exist is not found.
    for (params, message) in [
        (json!({"bin": "i9"}), "Item i9 does not exist"),
        (
            json!({"bin": "i1", "type": "Ghost"}),
            "type Ghost does not exist",
        ),
    ] {
        let refusal = refused(core.drive(&order, &pick("pick", params), &mut memory));
        assert_eq!(
            (refusal.code, refusal.message.as_str()),
            (Code::NotFound, message)
        );
    }
    // The called type's guards are told the calling use and its type.
    let mut wary = Memory::default()
        .with_spec(
            TypeSpec::new("Item").with(UseSpec::on_type("Bin", json!({"refuseFrom": "Order"}))),
        )
        .with("Order", "o1", row(json!({}), json!({}), 1))
        .with(
            "Item",
            "i1",
            row(json!({}), json!({"Bin": {"units": 4}}), 1),
        );
    assert_eq!(
        refused(core.drive(&order, &pick("pick", json!({"bin": "i1"})), &mut wary)).message,
        "behavior Bin vetoes take of Item i1: Order's Picker may not take"
    );
    // A behavior reads another type's config through its version.
    let config = changed(core.drive(&order, &pick("configOf", json!({"use": "Bin"})), &mut wary));
    assert_eq!(config.result, json!({"refuseFrom": "Order"}));
}

#[test]
fn a_create_of_another_types_row_runs_its_rules_and_joins_the_change_set() {
    let core = core();
    let (order, mut memory) = picking();
    let changes = changed(core.drive(
        &order,
        &pick("stock", json!({"bin": "i2", "units": 6})),
        &mut memory,
    ));
    assert_eq!(
        changes.rows,
        [
            RowWrite::Update {
                type_name: "Order".into(),
                key: "o1".into(),
                expected_version: 3,
                own: Map::new(),
                owned: BTreeMap::new(),
            },
            RowWrite::Create {
                type_name: "Item".into(),
                key: Some("i2".into()),
                own: object(json!({"title": "Bin i2"})),
                owned: [("Bin".to_owned(), object(json!({"units": 6})))]
                    .into_iter()
                    .collect(),
            },
        ]
    );
    // A row the run creates is the run's to call on: it stays a create.
    let stocked = changed(core.drive(
        &order,
        &pick("stockAndPick", json!({"bin": "i3", "units": 5, "by": 2})),
        &mut memory,
    ));
    assert_eq!(stocked.result, json!({"units": 3}));
    assert!(matches!(
        &stocked.rows[1],
        RowWrite::Create { key: Some(key), owned, .. } if key == "i3" && owned["Bin"]["units"] == json!(3)
    ));
    // A key the run reached already is a conflict.
    let again = refused(core.drive(
        &order,
        &pick("stock", json!({"bin": "i2", "again": true})),
        &mut memory,
    ));
    assert_eq!(
        (again.code, again.message.as_str()),
        (Code::Conflict, "Item i2 already exists")
    );
}

#[test]
fn a_key_a_behavior_makes_is_derived_from_the_request() {
    let core = core();
    let (order, mut memory) = picking();
    let noted = changed(core.drive(&order, &pick("note", json!({})), &mut memory));
    assert_eq!(
        noted.result,
        json!(["r1:Picker:note:1", "r1:Picker:note:2"])
    );
    // A run again makes the same keys.
    let again = changed(core.drive(&order, &pick("note", json!({})), &mut memory));
    assert_eq!(again.result, noted.result);
    // A command with no request id has none to derive from.
    let bare = command(alice(), invoke("o1", "Picker", "note", json!({})));
    let refusal = refused(core.drive(&order, &bare, &mut memory));
    assert_eq!(refusal.code, Code::BehaviorError);
    assert!(refusal
        .message
        .starts_with("behavior Picker: new_key: the command carries no request id"));
}

// ---- config changes ----

#[test]
fn a_config_change_reads_whether_rows_exist() {
    let core = core();
    let v1 = counter_spec(json!({"max": 10}));
    let v2 = counter_spec(json!({"max": 5}));
    let publish = command(
        alice(),
        Action::Publish {
            from: Some(v1.clone()),
        },
    );

    let mut answers = Answers::new();
    assert_eq!(
        core.run(&v2, &publish, &answers),
        Step::Read(vec![ReadRequest::has_rows("Order")])
    );
    answers.answer(ReadRequest::has_rows("Order"), Answer::HasRows(true));
    let Step::Done(outcome) = core.run(&v2, &publish, &answers) else {
        panic!("done");
    };
    let refusal = refused(outcome);
    assert_eq!(refusal.code, Code::IncompatibleChange);
    assert_eq!(
        refusal.details.as_deref().map(|d| d["changes"].clone()),
        Some(json!([{
            "path": "Order.behaviors.Counter",
            "message": "behavior Counter on type Order cannot change its config from {\"max\":10} to {\"max\":5}: a row may count up to 10, over the new max 5"
        }]))
    );

    // With no row, the max may shrink.
    let empty = Answers::new().with(ReadRequest::has_rows("Order"), Answer::HasRows(false));
    assert!(core
        .run(&v2, &publish, &empty)
        .outcome()
        .and_then(Outcome::changes)
        .is_some());

    // An identical config reads nothing; a first version checks nothing.
    let same = command(
        alice(),
        Action::Publish {
            from: Some(v1.clone()),
        },
    );
    assert!(matches!(
        core.run(&v1, &same, &Answers::new()),
        Step::Done(Outcome::Changed(_))
    ));
    let first = command(alice(), Action::Publish { from: None });
    assert!(matches!(
        core.run(&v2, &first, &Answers::new()),
        Step::Done(Outcome::Changed(_))
    ));

    // A use added to a type with rows asks its behavior; one without a
    // config_change refuses by default.
    let with_freeze = v1.clone().with(UseSpec::on_type("Freeze", json!({})));
    let added = core.run(&with_freeze, &publish, &answers);
    assert_eq!(
        added.outcome().and_then(Outcome::refusal).map(|r| r.message.as_str()),
        Some("type Order: behavior Freeze cannot be added to type Order, which has instances: it cannot be added to a schema that has instances")
    );
    let remove_freeze = command(
        alice(),
        Action::Publish {
            from: Some(with_freeze),
        },
    );
    let removed = core.run(&v1, &remove_freeze, &answers);
    assert_eq!(
        removed.outcome().and_then(Outcome::refusal).map(|r| r.message.as_str()),
        Some("type Order: behavior Freeze cannot be removed from type Order, which has instances: it cannot be removed from a schema that has instances")
    );
    // Counter allows its own removal, and refuses to join rows with no count.
    let bare = TypeSpec::new("Order");
    assert!(matches!(
        core.run(&bare, &publish, &answers),
        Step::Done(Outcome::Changed(_))
    ));
    let join = command(alice(), Action::Publish { from: Some(bare) });
    assert_eq!(
        core.run(&v1, &join, &answers)
            .outcome()
            .and_then(Outcome::refusal)
            .map(|r| r.message.as_str()),
        Some("type Order: behavior Counter cannot be added to type Order, which has instances: the rows that exist have no count")
    );
}

#[test]
fn a_field_uses_config_change_is_named_by_its_field() {
    let core = core();
    let v1 = TypeSpec::new("Post").with(UseSpec::on_field("likes", "Counter", json!({"max": 3})));
    let v2 = TypeSpec::new("Post").with(UseSpec::on_field("likes", "Counter", json!({"max": 2})));
    let publish = command(alice(), Action::Publish { from: Some(v1) });
    let answers = Answers::new().with(ReadRequest::has_rows("Post"), Answer::HasRows(true));
    let refusal = refused(
        core.run(&v2, &publish, &answers)
            .outcome()
            .cloned()
            .unwrap(),
    );
    assert_eq!(
        refusal
            .details
            .as_deref()
            .map(|d| d["changes"][0]["path"].clone()),
        Some(json!("Post.behaviors.likes"))
    );
    assert!(refusal.message.starts_with(
        "type Post: behavior Counter on field likes of type Post cannot change its config"
    ));
}

#[test]
fn a_publish_refusal_prints_each_config_with_its_members_sorted() {
    let core = core();
    // The host gives the members in an order of its own; the refusal reads
    // the same whatever the order, and a config given as {} and one not
    // given are one config.
    let v1 = TypeSpec::new("Post").with(UseSpec::on_field(
        "likes",
        "Counter",
        json!({"permission": "posts.like", "max": 3}),
    ));
    let v2 = TypeSpec::new("Post").with(UseSpec::on_field(
        "likes",
        "Counter",
        json!({"start": 0, "max": 2}),
    ));
    let publish = command(alice(), Action::Publish { from: Some(v1) });
    let answers = Answers::new().with(ReadRequest::has_rows("Post"), Answer::HasRows(true));
    let refusal = refused(
        core.run(&v2, &publish, &answers)
            .outcome()
            .cloned()
            .unwrap(),
    );
    assert_eq!(
        refusal.message,
        "type Post: behavior Counter on field likes of type Post cannot change its config from {\"max\":3,\"permission\":\"posts.like\"} to {\"max\":2,\"start\":0}: a row may count up to 3, over the new max 2"
    );
    let given = TypeSpec::new("Order").with(UseSpec::on_type("Freeze", json!({})));
    let bare = TypeSpec::new("Order").with(UseSpec::on_type("Freeze", None));
    let republish = command(alice(), Action::Publish { from: Some(given) });
    assert!(matches!(
        core.run(&bare, &republish, &answers),
        Step::Done(Outcome::Changed(_))
    ));
}

#[test]
fn a_source_that_cannot_answer_refuses_the_command() {
    // A source that does not take a kind of read, or whose read failed,
    // refuses with a refusal of its own; the run does not go on.
    struct Down;
    impl Source for Down {
        fn answer(&mut self, request: &ReadRequest) -> Result<Answer, Refusal> {
            Err(Refusal::internal(format!(
                "the database is down: {}",
                serde_json::to_string(request).unwrap()
            )))
        }
    }
    let refusal = refused(core().drive(
        &counter_spec(json!({"max": 3})),
        &command(alice(), Action::Delete { key: "o1".into() }),
        &mut Down,
    ));
    assert_eq!(refusal.code, Code::Internal);
    assert_eq!(
        refusal.error_json(),
        json!({"class": "Error", "message": "the database is down: {\"kind\":\"row\",\"type\":\"Order\",\"key\":\"o1\"}"})
    );

    // A lock the source cannot take refuses as it says.
    struct Contended(Memory);
    impl Source for Contended {
        fn answer(&mut self, request: &ReadRequest) -> Result<Answer, Refusal> {
            self.0.answer(request)
        }
        fn lock(&mut self, key: &LockKey) -> Result<(), Refusal> {
            Err(Refusal::new(
                Code::Busy,
                format!("{} is held", key.as_str()),
            ))
        }
    }
    let spec = TypeSpec::new("Order").with(UseSpec::on_type("Stock", json!({})));
    let take = command(alice(), invoke("o1", "Stock", "reserve", json!({})));
    let memory = Memory::default().with("Order", "o1", row(json!({}), json!({}), 1));
    let refusal = refused(core().drive(&spec, &take, &mut Contended(memory)));
    assert_eq!(refusal.code, Code::Busy);
}

#[test]
fn a_run_that_keeps_asking_is_an_internal_failure() {
    // Walk follows each row's `next` to the row it names, and a source
    // whose rows never end keeps it asking one read more each round.
    struct Walk;
    impl Behavior for Walk {
        type Config = ();
        fn declaration(&self) -> Declaration {
            Declaration::new("Walk").operation("walk", false)
        }
        fn parse_config(
            &self,
            _raw: &Value,
            _target: &ConfigTarget<'_>,
        ) -> Result<(), ConfigError> {
            Ok(())
        }
        fn operation(&self, cx: &Cx<'_, ()>, _name: &str, _params: &Value) -> Hook<Value> {
            let mut key = cx.key().unwrap_or_default().to_owned();
            loop {
                let Some(row) = cx.read_row(cx.type_name(), &key)? else {
                    return Ok(Value::from(key));
                };
                key = row
                    .own
                    .get("next")
                    .and_then(Value::as_str)
                    .unwrap_or_default()
                    .to_owned();
            }
        }
    }
    struct Endless;
    impl Source for Endless {
        fn answer(&mut self, request: &ReadRequest) -> Result<Answer, Refusal> {
            let ReadRequest::Row { key, .. } = request else {
                return Err(Refusal::internal("only rows"));
            };
            Ok(Answer::Row(Some(row(
                json!({"next": format!("{key}+")}),
                json!({}),
                1,
            ))))
        }
    }
    let core = superschematic_rules::Core::new().with(Walk);
    let spec = TypeSpec::new("Trail").with(UseSpec::on_type("Walk", None));
    let refusal = refused(core.drive(
        &spec,
        &command(alice(), invoke("t", "Walk", "walk", json!({}))),
        &mut Endless,
    ));
    assert_eq!(refusal.code, Code::Internal);
    assert_eq!(refusal.status, 500);
    assert_eq!(
        refusal.message,
        "walk of Trail: the run still asked for reads or locks after 256 rounds"
    );
}

// ---- define ----

#[test]
fn define_refuses_an_unknown_behavior_a_repeated_id_and_a_config_it_cannot_parse() {
    let core = core();
    let define = command(alice(), Action::Define);
    let good = counter_spec(json!({"max": 3}));
    assert_eq!(
        core.run(&good, &define, &Answers::new()),
        Step::Done(Outcome::Changed(Box::default()))
    );

    // The uses and the configs' shapes come first: while any is refused,
    // no config's rules are reported.
    let bad = TypeSpec::new("Order")
        .with(UseSpec::on_type("Counter", json!({"max": 3, "start": 4})))
        .with(UseSpec::on_type("Counter", json!({"max": 3})))
        .with(UseSpec::on_field("likes", "Nope", json!({})))
        .with(UseSpec::on_field("views", "Counter", json!({"max": "x"})))
        .with(UseSpec::on_type("Relay", json!({"target": "shares"})));
    let run = |spec: &TypeSpec| {
        refused(
            core.run(spec, &define, &Answers::new())
                .outcome()
                .cloned()
                .unwrap(),
        )
    };
    let refusal = run(&bad);
    assert_eq!(refusal.code, Code::InvalidSchema);
    assert_eq!(refusal.status, 422);
    assert_eq!(refusal.class, "SchemaDocumentError");
    assert_eq!(
        refusal.details.as_deref().map(|d| d["issues"].clone()),
        Some(json!([
            {"use": "Counter", "index": 1, "behavior": "Counter", "kind": "use", "path": "", "message": "type Order: two uses have the id Counter; a behavior is anchored on the type once, and each field carries one"},
            {"use": "likes", "index": 2, "behavior": "Nope", "kind": "use", "path": "", "message": "type Order: behavior Nope is not one this core runs (Counter, Relay, Freeze, Mirror, Stock, Loop, Bin, Picker)"},
            {"use": "views", "index": 3, "behavior": "Counter", "kind": "schema", "path": "/config/max", "message": "must be integer"}
        ]))
    );
    assert_eq!(
        refusal.message,
        "type Order: two uses have the id Counter; a behavior is anchored on the type once, and each field carries one; type Order: behavior Nope is not one this core runs (Counter, Relay, Freeze, Mirror, Stock, Loop, Bin, Picker); type Order: field views: behavior Counter config: /max: must be integer"
    );
    // With every shape sound, the rules.
    let rules = TypeSpec::new("Order")
        .with(UseSpec::on_type("Counter", json!({"max": 3, "start": 4})))
        .with(UseSpec::on_type("Relay", json!({"target": "shares"})));
    assert_eq!(
        run(&rules).details.as_deref().map(|d| d["issues"].clone()),
        Some(json!([
            {"use": "Counter", "index": 0, "behavior": "Counter", "kind": "rule", "path": "/config", "message": "type Order: behavior Counter config: start 4 is over max 3"},
            {"use": "Relay", "index": 1, "behavior": "Relay", "kind": "rule", "path": "/config", "message": "type Order: behavior Relay config: target shares is not a use of Order"}
        ]))
    );
    // A use that gives no config is checked as {}: one that needs a config
    // is refused at its entry, one that takes {} is not.
    let bare = TypeSpec::new("Order")
        .with(UseSpec::on_type("Freeze", None))
        .with(UseSpec::on_field("views", "Counter", None));
    let missing = run(&bare);
    assert_eq!(
        missing.details.as_deref().map(|d| d["issues"].clone()),
        Some(json!([
            {"use": "views", "index": 1, "behavior": "Counter", "kind": "schema", "path": "", "message": "must have required property 'config'"}
        ]))
    );
    assert_eq!(
        missing.message,
        "type Order: field views: behavior Counter: must have required property 'config'"
    );
    assert_eq!(
        missing.details.as_deref().unwrap()["issues"][0]["kind"],
        json!(ConfigIssueKind::Schema)
    );
    // Every command on such a type is refused so.
    let delete = command(alice(), Action::Delete { key: "o1".into() });
    assert_eq!(
        core.run(&bad, &delete, &Answers::new())
            .outcome()
            .and_then(Outcome::refusal)
            .map(|r| r.code),
        Some(Code::InvalidSchema)
    );
}

// ---- create, update, delete ----

#[test]
fn a_create_checks_its_params_validates_guards_and_initializes() {
    let core = core();
    let spec = TypeSpec::new("Order")
        .with(UseSpec::on_type("Freeze", json!({})))
        .with(UseSpec::on_type("Counter", json!({"max": 9, "start": 2})));
    let create = |own: Value, params: Value| {
        command(
            alice(),
            Action::Create {
                id: None,
                own: object(own),
                params: object(params),
            },
        )
    };

    let changes = changed(core.drive(
        &spec,
        &create(json!({"title": "Desk"}), json!({})),
        &mut Memory::default(),
    ));
    assert_eq!(
        changes.rows,
        [RowWrite::Create {
            type_name: "Order".into(),
            key: None,
            own: object(json!({"title": "Desk"})),
            owned: [(
                "Counter".to_owned(),
                object(json!({"count": 2, "changedAt": NOW}))
            )]
            .into_iter()
            .collect(),
        }]
    );
    assert_eq!(changes.result, Value::Null);
    assert_eq!(changes.events[0].kind, "create");
    // The row a create without an id makes is named with no key.
    assert_eq!(changes.events[0].row, RowRef::new("Order", None));
    assert_eq!(
        changes.events[0].payload,
        json!({"title": "Desk", "version": null})
    );

    let refusal = refused(core.drive(
        &spec,
        &create(
            json!({"title": "Desk"}),
            json!({"Counter": {"start": "x"}, "Stock": {}, "Freeze": {}}),
        ),
        &mut Memory::default(),
    ));
    // An entry for a use whose behavior takes no create parameters is
    // refused, {} included.
    assert_eq!(
        refusal.error_json(),
        json!({"class": "CreateParamsError", "code": "invalid_argument", "message": "create of Order: /Stock: Order has no behavior use Stock (its uses that take create parameters: Counter); /Freeze: behavior Freeze takes no create parameters; /Counter/start: must be an integer", "details": {"issues": [
            {"path": "/Stock", "message": "Order has no behavior use Stock (its uses that take create parameters: Counter)"},
            {"path": "/Freeze", "message": "behavior Freeze takes no create parameters"},
            {"path": "/Counter/start", "message": "must be an integer"}
        ]}})
    );

    let refusal = refused(core.drive(&spec, &create(json!({}), json!({})), &mut Memory::default()));
    assert_eq!(refusal.code, Code::InvalidInstance);
    assert_eq!(refusal.class, "InstanceValidationError");
    assert_eq!(
        refusal.details.as_deref().map(|d| d["issues"].clone()),
        Some(json!([{"path": "title", "rule": "required", "message": "a title is required"}]))
    );
}

#[test]
fn an_update_merges_its_patch_and_a_delete_is_held_to_the_version() {
    let core = core();
    let spec = TypeSpec::new("Order")
        .with(UseSpec::on_type("Freeze", json!({})))
        .with(UseSpec::on_type("Counter", json!({"max": 9})));
    let mut memory = Memory::default().with("Order", "o1", order_at(1, 4));

    let update = command(
        alice(),
        Action::Update {
            key: "o1".into(),
            patch: object(json!({"note": "fragile", "title": "Oak desk"})),
        },
    );
    let changes = changed(core.drive(&spec, &update, &mut memory));
    assert_eq!(
        changes.rows,
        [RowWrite::Update {
            type_name: "Order".into(),
            key: "o1".into(),
            expected_version: 4,
            own: object(json!({"note": "fragile", "title": "Oak desk"})),
            owned: BTreeMap::new(),
        }]
    );
    // After-change hooks see the row after the patch.
    assert_eq!(changes.events.len(), 1);
    assert_eq!(changes.events[0].kind, "update");
    assert_eq!(
        changes.events[0].payload,
        json!({"title": "Oak desk", "version": 4})
    );

    // Validation sees the own fields after the patch.
    let blank = command(
        alice(),
        Action::Update {
            key: "o1".into(),
            patch: object(json!({"title": null})),
        },
    );
    assert_eq!(
        refused(core.drive(&spec, &blank, &mut memory)).message,
        "Order: title: a title is required"
    );

    let delete = command(alice(), Action::Delete { key: "o1".into() });
    assert_eq!(
        changed(core.drive(&spec, &delete, &mut memory)).rows,
        [RowWrite::Delete {
            type_name: "Order".into(),
            key: "o1".into(),
            expected_version: 4,
        }]
    );
}

#[test]
fn a_missing_row_use_or_operation_is_not_found() {
    let core = core();
    let spec = counter_spec(json!({"max": 9}));
    let mut memory = Memory::default().with("Order", "o1", order_at(1, 1));
    let cases = [
        (
            invoke("o9", "Counter", "add", json!({"by": 1})),
            "Order o9 does not exist",
        ),
        (
            invoke("o1", "Stock", "reserve", json!({})),
            "Order has no behavior use Stock (its uses: Counter)",
        ),
        (
            invoke("o1", "Counter", "ship", json!({})),
            "Order has no operation ship of Counter (its operations: add, peek, sneak)",
        ),
    ];
    for (action, message) in cases {
        let refusal = refused(core.drive(&spec, &command(alice(), action), &mut memory));
        assert_eq!((refusal.code, refusal.status), (Code::NotFound, 404));
        assert_eq!(refusal.message, message);
    }
}

// ---- deadlines and operations that read ----

#[test]
fn a_deadline_falls_due_with_its_payload() {
    let core = core();
    let spec = TypeSpec::new("Order").with(UseSpec::on_type("Stock", json!({})));
    let mut memory = Memory::default().with(
        "Order",
        "o1",
        row(json!({}), json!({"Stock": {"held": true}}), 5),
    );
    let due = command(
        alice(),
        Action::Deadline {
            key: "o1".into(),
            use_id: "Stock".into(),
            name: "expire".into(),
            at: NOW,
            payload: json!({"held": true}),
        },
    );
    let changes = changed(core.drive(&spec, &due, &mut memory));
    assert_eq!(changes.owned("Stock", "held"), Some(&json!(false)));
    assert_eq!(changes.events[0].kind, "expired");
    assert!(matches!(
        changes.rows.as_slice(),
        [RowWrite::Update {
            expected_version: 5,
            ..
        }]
    ));

    let release = command(alice(), invoke("o1", "Stock", "release", json!({})));
    assert_eq!(
        changed(core.drive(&spec, &release, &mut memory)).deadlines,
        [DeadlineChange::Clear {
            row: RowRef::new("Order", Some("o1")),
            use_id: "Stock".into(),
            name: "expire".into(),
        }]
    );
}

#[test]
fn a_read_only_operation_writes_no_row() {
    let core = core();
    let spec = counter_spec(json!({"max": 9}));
    let mut memory = Memory::default().with("Order", "o1", order_at(6, 1));
    let peek = command(alice(), invoke("o1", "Counter", "peek", json!({})));
    let changes = changed(core.drive(&spec, &peek, &mut memory));
    assert!(changes.rows.is_empty());
    assert_eq!(changes.result, json!({"count": 6}));

    let sneak = command(alice(), invoke("o1", "Counter", "sneak", json!({})));
    assert_eq!(
        refused(core.drive(&spec, &sneak, &mut memory)).message,
        "behavior Counter: set(count): a read-only operation writes nothing"
    );
}

// ---- defects ----

#[test]
fn a_veto_code_the_declaration_does_not_list_is_a_defect() {
    let core = core();
    let spec = TypeSpec::new("Order").with(UseSpec::on_type("Loop", json!({})));
    let mut memory = Memory::default().with("Order", "o1", row(json!({}), json!({}), 1));
    let veto = command(alice(), invoke("o1", "Loop", "spin", json!({"veto": true})));
    assert_eq!(
        refused(core.drive(&spec, &veto, &mut memory)).error_json(),
        json!({"class": "BehaviorError", "message": "behavior Loop: a veto's code \"not_listed\" is not one its declaration lists (undeclared_elsewhere)", "details": {"behavior": "Loop"}})
    );
}

#[test]
fn a_host_answer_of_another_kind_is_refused() {
    let core = core();
    let spec = counter_spec(json!({"max": 9}));
    let add = command(alice(), invoke("o1", "Counter", "add", json!({"by": 1})));
    let answers = Answers::new().with(ReadRequest::row("Order", "o1"), Answer::HasRows(true));
    let refusal = core
        .run(&spec, &add, &answers)
        .outcome()
        .and_then(Outcome::refusal)
        .cloned();
    assert_eq!(refusal.map(|r| r.code), Some(Code::InvalidArgument));
}

#[test]
fn steps_and_outcomes_read_as_json() {
    let core = core();
    let spec = counter_spec(json!({"max": 9}));
    let add = command(alice(), invoke("o1", "Counter", "add", json!({"by": 1})));
    assert_eq!(
        serde_json::to_value(core.run(&spec, &add, &Answers::new())).unwrap(),
        json!({"read": [{"kind": "row", "type": "Order", "key": "o1"}]})
    );
    let step: Step = serde_json::from_value(json!({"done": {"refused": {
        "code": "not_found", "status": 404, "class": "EngineError", "message": "Order o9 does not exist"
    }}}))
    .unwrap();
    assert_eq!(
        step.outcome().and_then(Outcome::refusal).map(|r| r.code),
        Some(Code::NotFound)
    );
}

#[test]
fn a_core_is_shared_across_threads() {
    fn shared<T: Send + Sync>() {}
    shared::<superschematic_rules::Core>();
    shared::<superschematic_rules::TypeSpec>();
    shared::<superschematic_rules::Step>();
}
