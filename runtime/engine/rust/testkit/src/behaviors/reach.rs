//! The behavior `test/reach-fixtures.ts` defines and both
//! `test/behavior-reach.test.ts` and `test/core-dependencies.test.ts`
//! compose: test.Reader.

use serde_json::{json, Value};

use superschematic_engine::behaviors::{BehaviorMigration, ColumnSpec, OperationCx};
use superschematic_engine::storage::SqlValue;
use superschematic_engine::{
    Behavior, ConfigTarget, EngineError, Error, GuardRequest, Hooks, InstanceRecord, Result,
};

fn text<'v>(params: &'v Value, key: &str) -> &'v str {
    params.get(key).and_then(Value::as_str).unwrap_or_default()
}

// The params a call passes on, `{}` when it gives none.
fn passed(params: &Value) -> Value {
    params.get("params").cloned().unwrap_or_else(|| json!({}))
}

fn record_json(record: &InstanceRecord) -> Value {
    json!({ "data": record.data, "behaviors": record.behaviors })
}

// The name a TypeScript caller's catch reads.
fn error_name(error: &Error) -> &'static str {
    match error {
        Error::Engine(error) => error.name(),
        Error::Behavior(_) => "BehaviorError",
        Error::Config(_) => "BehaviorConfigError",
        Error::Usage(_) => "TypeError",
        _ => "Error",
    }
}

/// The config of [`Reader`].
#[derive(Clone, Debug, Default)]
pub struct ReaderConfig {
    /// The schema of the partner an instance's `partner` names; its own
    /// when absent.
    pub partner_schema: Option<String>,
}

/// test.Reader (`test/reach-fixtures.ts`): reads other instances and
/// invokes their operations, as the caller, and counts its pokes.
pub struct Reader;

static READER: std::sync::LazyLock<String> = std::sync::LazyLock::new(|| {
    let target = json!({"schema": {"type": "string", "minLength": 1}, "id": {"type": "string", "minLength": 1}});
    let mut call = target.clone();
    call["operation"] = json!({"type": "string", "minLength": 1});
    call["params"] = json!({"type": "object"});
    let schema_call = json!({"schema": target["schema"], "operation": {"type": "string", "minLength": 1}, "params": {"type": "object"}});
    let with = |base: &Value, more: Value| {
        let mut out = base.clone();
        for (key, value) in more.as_object().unwrap() {
            out[key] = value.clone();
        }
        out
    };
    let object = |required: Value, properties: Value| json!({"type": "object", "additionalProperties": false, "required": required, "properties": properties});
    let no_params = json!({"type": "object", "additionalProperties": false});
    let fields = json!({"type": "array", "items": {"type": "string"}});
    json!({
        "name": "test.Reader",
        "description": "Reads other instances and invokes their operations.",
        "configSchema": {"type": "object", "additionalProperties": false, "properties": {"partnerSchema": {"type": "string"}}},
        "fields": [
            {"name": "pokes", "description": "How many times poke ran on the instance."},
            {"name": "echo", "description": "The partner's title, read when the instance names one."}
        ],
        "operations": [
            {"name": "peek", "paramsSchema": object(json!(["schema", "id"]), with(&target, json!({"fields": fields}))), "resultSchema": true},
            {"name": "peekMany", "paramsSchema": object(json!(["schema", "ids"]), json!({"schema": target["schema"], "ids": {"type": "array", "items": {"type": "string"}}, "fields": fields})), "resultSchema": {"type": "array"}},
            {"name": "configOf", "paramsSchema": object(json!(["schema", "behavior"]), json!({"schema": target["schema"], "behavior": {"type": "string"}})), "resultSchema": true},
            {"name": "canRead", "paramsSchema": object(json!(["schema"]), json!({"schema": target["schema"]})), "resultSchema": {"type": "boolean"}},
            {"name": "poke", "paramsSchema": object(json!(["schema", "id", "operation"]), with(&call, json!({"fail": {"type": "boolean"}}))), "resultSchema": true, "writes": true},
            {"name": "pokeCatch", "paramsSchema": object(json!(["schema", "id", "operation"]), call.clone()), "resultSchema": true, "writes": true},
            {"name": "pokeRead", "paramsSchema": object(json!(["schema", "id", "operation"]), call.clone()), "resultSchema": true},
            {"name": "peekSchema", "paramsSchema": object(json!(["schema", "operation"]), schema_call.clone()), "resultSchema": true},
            {"name": "pokeSchema", "paramsSchema": object(json!(["schema", "operation"]), schema_call), "resultSchema": true, "writes": true},
            {"name": "guarded", "paramsSchema": object(json!(["schema", "id", "operation"]), call), "resultSchema": true, "writes": true},
            {"name": "failAfterWrite", "paramsSchema": no_params, "resultSchema": true, "writes": true}
        ]
    })
    .to_string()
});

fn poked(cx: &OperationCx<'_, ReaderConfig>) -> Result<()> {
    let pokes = cx.columns().get()?.i64("pokes")?;
    cx.columns().set(&[("pokes", SqlValue::Integer(pokes + 1))])
}

fn read_fields(params: &Value) -> Option<Vec<&str>> {
    params
        .get("fields")
        .and_then(Value::as_array)
        .map(|fields| fields.iter().filter_map(Value::as_str).collect())
}

impl Behavior for Reader {
    type Config = ReaderConfig;

    fn declaration(&self) -> &str {
        &READER
    }

    fn parse_config(&self, raw: &Value, _: &ConfigTarget<'_>) -> Result<ReaderConfig> {
        Ok(ReaderConfig {
            partner_schema: raw
                .get("partnerSchema")
                .and_then(Value::as_str)
                .map(str::to_owned),
        })
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            migrations: vec![BehaviorMigration::new(1, "pokes")
                .column("pokes", ColumnSpec::integer().not_null().default(0))],
            guard: Some(|_, view, request| {
                if let GuardRequest::Operation {
                    operation: "guarded",
                    params,
                    ..
                } = request
                {
                    view.instances().invoke(
                        text(params, "schema"),
                        text(params, "id"),
                        text(params, "operation"),
                        &passed(params),
                        None,
                    )?;
                }
                Ok(None)
            }),
            operations: vec![
                ("peek", |_, cx, params| {
                    let fields = read_fields(params);
                    let found = cx.instances().get(
                        text(params, "schema"),
                        text(params, "id"),
                        fields.as_deref(),
                    )?;
                    Ok(found.as_ref().map_or(Value::Null, record_json))
                }),
                ("peekMany", |_, cx, params| {
                    let fields = read_fields(params);
                    let ids: Vec<&str> = params["ids"]
                        .as_array()
                        .map(|ids| ids.iter().filter_map(Value::as_str).collect())
                        .unwrap_or_default();
                    let found =
                        cx.instances()
                            .get_many(text(params, "schema"), &ids, fields.as_deref())?;
                    Ok(Value::Array(
                        found
                            .iter()
                            .map(|(id, record)| json!([id, record_json(record)]))
                            .collect(),
                    ))
                }),
                ("configOf", |_, cx, params| {
                    Ok(cx
                        .schemas()
                        .config(text(params, "schema"), text(params, "behavior"))?
                        .unwrap_or(Value::Null))
                }),
                ("canRead", |_, cx, params| {
                    Ok(Value::Bool(cx.schemas().readable(text(params, "schema"))?))
                }),
                ("poke", |_, cx, params| {
                    poked(cx)?;
                    let result = cx.instances().invoke(
                        text(params, "schema"),
                        text(params, "id"),
                        text(params, "operation"),
                        &passed(params),
                        None,
                    )?;
                    if params.get("fail") == Some(&Value::Bool(true)) {
                        return Err(Error::Engine(EngineError::invalid_argument(
                            "poke failed after its invoke, as asked",
                        )));
                    }
                    Ok(result)
                }),
                ("pokeCatch", |_, cx, params| {
                    poked(cx)?;
                    Ok(
                        match cx.instances().invoke(
                            text(params, "schema"),
                            text(params, "id"),
                            text(params, "operation"),
                            &passed(params),
                            None,
                        ) {
                            Ok(result) => json!({ "result": result }),
                            Err(error) => json!({ "threw": error_name(&error) }),
                        },
                    )
                }),
                ("pokeRead", |_, cx, params| {
                    cx.instances().invoke(
                        text(params, "schema"),
                        text(params, "id"),
                        text(params, "operation"),
                        &passed(params),
                        None,
                    )
                }),
                ("peekSchema", |_, cx, params| {
                    cx.instances().invoke_schema(
                        text(params, "schema"),
                        text(params, "operation"),
                        &passed(params),
                    )
                }),
                ("pokeSchema", |_, cx, params| {
                    poked(cx)?;
                    cx.instances().invoke_schema(
                        text(params, "schema"),
                        text(params, "operation"),
                        &passed(params),
                    )
                }),
                ("guarded", |_, _, _| Ok(json!("ran"))),
                ("failAfterWrite", |_, cx, _| {
                    poked(cx)?;
                    Err(Error::Engine(EngineError::invalid_argument(
                        "failAfterWrite fails after its write",
                    )))
                }),
            ],
            fields: vec![
                ("pokes", |_, view| {
                    Ok(Some(Value::from(view.columns().get()?.i64("pokes")?)))
                }),
                ("echo", |_, view| {
                    let data = view.data()?;
                    let Some(partner) = data.get("partner").and_then(Value::as_str) else {
                        return Ok(None);
                    };
                    let schema = view
                        .config()
                        .partner_schema
                        .as_deref()
                        .unwrap_or(view.schema());
                    let found =
                        view.instances()
                            .get(schema, partner, Some(&["test.Reader.echo"]))?;
                    Ok(Some(
                        found
                            .and_then(|record| record.data.get("title").cloned())
                            .unwrap_or(Value::Null),
                    ))
                }),
            ],
            ..Hooks::default()
        }
    }
}
