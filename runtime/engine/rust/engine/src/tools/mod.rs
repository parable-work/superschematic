//! What the engine serves about a namespace's schemas besides their
//! instances (D16, D58), the port of `src/tools/catalog.ts` and
//! `src/tools/behaviors.ts`: a describe document per schema, which carries
//! the instance type's display and its fields' titles and icons (D48), the
//! tools document in the shape the SDK generators write to
//! `tools/schema.json` (`ir.ToolManifest`), the calls those tools make, and
//! the behaviors the engine runs. The MCP endpoint and the HTTP routes
//! (`crate::api`) read them from here; every read and call goes through the
//! schema registry and the instance store, so the access policy answers
//! each one.
//!
//! A tool is one operation: create, get, list, update and delete of every
//! live schema the namespace reaches, lookup of one whose instance type has
//! a unique field, each operation its behaviors add (a schema-level one
//! takes its parameters and no instance id), three tools for writing
//! schemas (list, describe and define a draft), two that list and describe
//! the behaviors a schema may compose, `get_value`, the search across the
//! namespace's schemas where one composes Search, and four that list,
//! create, archive and unarchive namespaces. No tool publishes.
//!
//! Names follow the SDK generators: a tool's name is
//! `<namespace>.<method>`, the namespace the schema name in kebab case and
//! the method the operation's name; its MCP handle is the same two parts in
//! snake case (`order_create`). A handle @mcp would refuse, or one two
//! tools derive, hides both tools with the reason; so does a tool the
//! access policy refuses the caller, which can still be called by its
//! handle (the call is refused), and every tool that writes in an archived
//! namespace. A mount can narrow what a caller is offered further with a
//! [`ToolFilter`]: a tool it leaves out is hidden from that caller, and a
//! call of it is a tool the namespace does not have (`UnknownToolError`).
//!
//! The invocation policy and the vendor keys the documents are written
//! with are engine config (`EngineOptions.tools`, [`ToolOptions`]).

pub(crate) mod guidance;
pub(crate) mod options;
pub(crate) mod schema;

use std::collections::HashMap;
use std::sync::{Arc, Mutex, PoisonError};

use serde_json::{json, Map, Value};

use crate::access::{ListingAction, NamespaceOperation, SchemaAction};
use crate::behaviors::behavior::InstanceSchemaForm;
use crate::behaviors::declaration::{
    is_behavior_name, BehaviorDeclaration, OperationDeclaration, OperationScope, BEHAVIOR_NAME,
};
use crate::engine::Engine;
use crate::errors::{EngineError, EngineErrorCode, Error, Result};
use crate::instances::{CreateOptions, GetOptions, ListOptions, WriteOptions, INSTANCE_ID};
use crate::json;
use crate::namespaces::NAMESPACE_NAME;
use crate::paging::MAX_PAGE_SIZE;
use crate::principal::{check_principal, Principal};
use crate::registry::document::{json_key, type_of};
use crate::registry::fields::{patch_of, render_property, FieldSchemas, Property};
use crate::registry::{
    ComposedBehavior, DefineOptions, SchemaRecord, SchemaTarget, ValidateOptions, VersionRuntime,
    SCHEMA_NAME,
};
use crate::values::VALUE_HASH;

pub use guidance::{GuidanceErrorEntry, ToolGuidance};
pub use options::{InvocationPolicy, ToolKeys, ToolOptions, BUILTIN_TOOLS};
pub use schema::{is_mcp_handle, kebab_case, snake_case, type_arguments, MCP_HANDLE_MAX_LENGTH};

use guidance::{engine_guidance, version_guidance, FieldGuidance, VersionGuidance};
use options::{policy_entry, ResolvedToolOptions};
use schema::{arguments_digest, is_snake_handle, render_arguments, ArgumentSchema};

/// What a [`ToolFilter`] is told of one tool.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ToolSummary {
    /// Its MCP handle, `order_create`, `define_schema`.
    pub handle: String,
    /// Its name in the tools document, `order.create`, `engine.defineSchema`.
    pub name: String,
    /// The schema a schema's tool reaches; `None` for the engine's own.
    pub schema: Option<String>,
    /// The operation it runs: `create`, ..., a behavior's, or the engine
    /// tool's (`defineSchema`).
    pub operation: String,
    /// The behavior whose operation it runs; `None` for the others.
    pub behavior: Option<String>,
    pub writes: bool,
}

/// Narrows what one caller is offered: true keeps a tool, false leaves it
/// out of the caller's list and refuses its call. It narrows only: a tool
/// the access policy hides stays hidden.
pub trait ToolFilter: Send + Sync {
    fn keeps(&self, principal: &Principal, tool: &ToolSummary, namespace: &str) -> bool;
}

/// Where a tools call looks: a namespace (`default` when absent), and the
/// mount's filter.
#[derive(Clone, Copy, Default)]
pub struct ToolTarget<'a> {
    pub namespace: Option<&'a str>,
    pub filter: Option<&'a dyn ToolFilter>,
}

const TOOL_SCHEMA: &str = "https://json-schema.org/draft/2020-12/schema";

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum ToolKind {
    Create,
    Get,
    List,
    Update,
    Delete,
    Lookup,
    Operation,
    SchemaOperation,
    ListSchemas,
    DescribeSchema,
    DefineSchema,
    ListBehaviors,
    DescribeBehavior,
    Search,
    GetValue,
    ListNamespaces,
    CreateNamespace,
    ArchiveNamespace,
    UnarchiveNamespace,
}

impl ToolKind {
    fn name(self) -> &'static str {
        match self {
            ToolKind::Create => "create",
            ToolKind::Get => "get",
            ToolKind::List => "list",
            ToolKind::Update => "update",
            ToolKind::Delete => "delete",
            ToolKind::Lookup => "lookup",
            ToolKind::Operation => "operation",
            ToolKind::SchemaOperation => "schemaOperation",
            ToolKind::ListSchemas => "listSchemas",
            ToolKind::DescribeSchema => "describeSchema",
            ToolKind::DefineSchema => "defineSchema",
            ToolKind::ListBehaviors => "listBehaviors",
            ToolKind::DescribeBehavior => "describeBehavior",
            ToolKind::Search => "search",
            ToolKind::GetValue => "getValue",
            ToolKind::ListNamespaces => "listNamespaces",
            ToolKind::CreateNamespace => "createNamespace",
            ToolKind::ArchiveNamespace => "archiveNamespace",
            ToolKind::UnarchiveNamespace => "unarchiveNamespace",
        }
    }

    fn acts_on_namespaces(self) -> bool {
        matches!(
            self,
            ToolKind::ListNamespaces
                | ToolKind::CreateNamespace
                | ToolKind::ArchiveNamespace
                | ToolKind::UnarchiveNamespace
        )
    }

    // The listing question an engine tool that names its schema or its
    // namespace only when called asks the policy.
    fn listing(self) -> Option<(ListingAction, Option<NamespaceOperation>)> {
        match self {
            ToolKind::DefineSchema => Some((ListingAction::Define, None)),
            ToolKind::ListNamespaces => {
                Some((ListingAction::Manage, Some(NamespaceOperation::List)))
            }
            ToolKind::CreateNamespace => {
                Some((ListingAction::Manage, Some(NamespaceOperation::Create)))
            }
            ToolKind::ArchiveNamespace => {
                Some((ListingAction::Manage, Some(NamespaceOperation::Archive)))
            }
            ToolKind::UnarchiveNamespace => {
                Some((ListingAction::Manage, Some(NamespaceOperation::Unarchive)))
            }
            _ => None,
        }
    }
}

// One tool before it is rendered: its names, what it does, and who may see
// it.
#[derive(Clone)]
struct ToolSpec {
    kind: ToolKind,
    name: String,
    handle: String,
    namespace: String,
    method_name: String,
    operation_id: String,
    title: String,
    description: String,
    writes: bool,
    policy: String,
    http_method: &'static str,
    http_path: String,
    /// The schema a call reaches, for a schema's tool, with its runtime.
    schema: Option<(Arc<SchemaRecord>, Arc<VersionRuntime>)>,
    /// The behavior whose operation it runs, and the operation.
    behavior: Option<ComposedBehavior>,
    operation: Option<OperationDeclaration>,
    /// For create, the schema's behaviors that take create parameters.
    create_params: Vec<ComposedBehavior>,
    hidden: Option<String>,
    preconditions: Option<Property>,
    filter: Option<Property>,
    key: Option<Property>,
}

impl ToolSpec {
    fn record(&self) -> Option<&SchemaRecord> {
        self.schema.as_ref().map(|(record, _)| record.as_ref())
    }

    fn summary(&self) -> ToolSummary {
        ToolSummary {
            handle: self.handle.clone(),
            name: self.name.clone(),
            schema: self.record().map(|record| record.name.clone()),
            operation: self.method_name.clone(),
            behavior: self.behavior.as_ref().map(|behavior| behavior.name.clone()),
            writes: self.writes,
        }
    }
}

/// A version's argument schemas: its instance type's fields, and what its
/// behaviors hold them to in each form.
struct InstanceSchemas {
    properties: Vec<(String, Property)>,
    required: Vec<String>,
    instance_rules: Vec<Value>,
    patch_rules: Vec<Value>,
}

/// The tools' part of the engine: their options and what each published
/// version, which never changes, gives them.
pub(crate) struct ToolsCore {
    pub options: ResolvedToolOptions,
    schemas: Mutex<HashMap<String, Arc<InstanceSchemas>>>,
    guidance: Mutex<HashMap<String, Arc<VersionGuidance>>>,
}

impl ToolsCore {
    pub fn new(options: ResolvedToolOptions) -> Self {
        ToolsCore {
            options,
            schemas: Mutex::new(HashMap::new()),
            guidance: Mutex::new(HashMap::new()),
        }
    }
}

// version_key names a published version in the caches.
fn version_key(record: &SchemaRecord) -> String {
    format!(
        "{}\u{0}{}\u{0}{}\u{0}{}",
        record.namespace,
        record.name,
        record.version.unwrap_or_default(),
        record.hash
    )
}

/// The engine's tools (`engine.tools`).
pub struct Tools<'e> {
    pub(crate) engine: &'e Engine,
}

impl<'e> Tools<'e> {
    fn options(&self) -> &'e ResolvedToolOptions {
        &self.engine.tools.options
    }

    /// The invocation policy the documents are written with.
    pub fn invocation_policy(&self) -> &'e InvocationPolicy {
        &self.options().invocation_policy
    }

    /// The vendor keys the documents are written with.
    pub fn keys(&self) -> &'e ToolKeys {
        &self.options().keys
    }

    /// A summary of each behavior this engine runs, by name. Every caller
    /// may read it; the policy is not asked.
    pub fn list_behaviors(&self, principal: &Principal) -> Result<Value> {
        check_principal(principal)?;
        let registry = self.engine.catalog.registry();
        Ok(Value::Array(
            registry
                .names()
                .iter()
                .filter_map(|name| registry.lookup(name))
                .map(|behavior| behavior_summary(&behavior.declaration))
                .collect(),
        ))
    }

    /// The document of a behavior this engine runs: its declaration with
    /// every default filled in. `not_found` for a name it does not run.
    pub fn describe_behavior(&self, principal: &Principal, name: &str) -> Result<Value> {
        check_principal(principal)?;
        if !is_behavior_name(name) {
            return Err(EngineError::invalid_argument(format!(
                "{} is not a behavior name",
                json::quote(name)
            ))
            .into());
        }
        let registry = self.engine.catalog.registry();
        let Some(behavior) = registry.lookup(name) else {
            return Err(EngineError::new(
                EngineErrorCode::NotFound,
                format!("this engine runs no behavior {name}"),
            )
            .into());
        };
        Ok(behavior_document(
            &behavior.declaration,
            self.invocation_policy(),
        ))
    }

    /// The describe document of a schema's live version: it asks the policy
    /// for read, and refuses `not_found` without a live version and
    /// `unavailable` when the engine cannot run its behaviors.
    pub fn describe(
        &self,
        principal: &Principal,
        name: &str,
        target: ToolTarget<'_>,
    ) -> Result<Value> {
        check_principal(principal)?;
        let namespace = self.engine.namespaces().resolve(target.namespace)?;
        let schemas = self.engine.schemas();
        let Some(record) = schemas.live(
            principal,
            name,
            SchemaTarget {
                namespace: Some(&namespace),
            },
        )?
        else {
            return Err(EngineError::new(
                EngineErrorCode::NotFound,
                format!("schema {name} has no live version in namespace {namespace}"),
            )
            .into());
        };
        let behaviors = schemas.behaviors(
            principal,
            name,
            ValidateOptions {
                namespace: Some(&namespace),
                version: None,
            },
        )?;
        let record = Arc::new(record);
        let runtime = self.runtime(&record)?;
        let tools = self.schema_tools(&namespace, &record, &runtime, &behaviors)?;
        let policy_key = &self.invocation_policy().key;
        let type_def = type_of(&record.document, &record.instance_type);
        let said = self.guidance_of(&record, &runtime)?;
        let mut out = Map::new();
        out.insert("namespace".into(), Value::from(namespace.as_str()));
        out.insert("name".into(), Value::from(record.name.as_str()));
        out.insert(
            "schemaNamespace".into(),
            Value::from(record.namespace.as_str()),
        );
        out.insert("version".into(), Value::from(record.version));
        out.insert("hash".into(), Value::from(record.hash.as_str()));
        out.insert(
            "instanceType".into(),
            Value::from(record.instance_type.as_str()),
        );
        let description = type_def
            .and_then(|type_def| text_of(type_def, "description"))
            .or_else(|| text_of(&record.document, "description"));
        if let Some(description) = description {
            out.insert("description".into(), Value::from(description));
        }
        if let Some(display) = type_def.and_then(described_display) {
            out.insert("display".into(), display);
        }
        let fields: Vec<Value> = type_def
            .and_then(|type_def| type_def.get("fields"))
            .and_then(Value::as_array)
            .map(|fields| {
                fields
                    .iter()
                    .map(|field| {
                        let mut entry = Map::new();
                        entry.insert("name".into(), Value::from(json_key(field)));
                        if let Some(title) = text_of(field, "title") {
                            entry.insert("title".into(), Value::from(title));
                        }
                        if let Some(icon) = text_of(field, "icon") {
                            entry.insert("icon".into(), Value::from(icon));
                        }
                        Value::Object(entry)
                    })
                    .collect()
            })
            .unwrap_or_default();
        out.insert("fields".into(), Value::Array(fields));
        out.insert("instance".into(), self.instance_schema(&record, &runtime)?);
        out.insert("instanceBehaviors".into(), behaviors_schema(&behaviors));
        let mut described = Vec::new();
        for bound in &behaviors {
            let declaration = declaration_of(bound)?;
            let mut entry = Map::new();
            entry.insert("name".into(), Value::from(bound.name.as_str()));
            if let Some(description) = declaration.description.as_deref().filter(|d| !d.is_empty())
            {
                entry.insert("description".into(), Value::from(description));
            }
            entry.insert("config".into(), bound.config.clone());
            if let Some(summary) = said.summary(&bound.name) {
                entry.insert("summary".into(), Value::from(summary));
            }
            entry.insert("fields".into(), declared_fields(&declaration));
            entry.insert(
                "operations".into(),
                Value::Array(
                    declaration
                        .operations
                        .iter()
                        .map(|operation| Value::from(operation.name.as_str()))
                        .collect(),
                ),
            );
            entry.insert("vetoes".into(), declared_vetoes(&declaration));
            described.push(Value::Object(entry));
        }
        out.insert("behaviors".into(), Value::Array(described));
        let mut operations = Vec::new();
        for tool in &tools {
            let mut entry = Map::new();
            entry.insert("name".into(), Value::from(tool.method_name.as_str()));
            if let Some(behavior) = &tool.behavior {
                entry.insert("behavior".into(), Value::from(behavior.name.as_str()));
                entry.insert(
                    "scope".into(),
                    Value::from(if tool.kind == ToolKind::SchemaOperation {
                        "schema"
                    } else {
                        "instance"
                    }),
                );
            }
            entry.insert("title".into(), Value::from(tool.title.as_str()));
            entry.insert("description".into(), Value::from(tool.description.as_str()));
            entry.insert("writes".into(), Value::Bool(tool.writes));
            entry.insert(policy_key.clone(), Value::from(tool.policy.as_str()));
            entry.insert(
                "params".into(),
                render_arguments(&self.arguments(tool)?, &self.keys().scalar),
            );
            entry.insert("result".into(), self.result_schema(tool, &behaviors)?);
            entry.insert("tool".into(), Value::from(tool.name.as_str()));
            entry.insert("guidance".into(), to_json(&self.tool_guidance(tool)?)?);
            operations.push(Value::Object(entry));
        }
        out.insert("operations".into(), Value::Array(operations));
        Ok(Value::Object(out))
    }

    /// The tools document of a namespace: the engine's tools, then each
    /// live schema's it reaches that the principal may read, by name. A
    /// schema whose behaviors this engine cannot run has no tools.
    pub fn manifest(&self, principal: &Principal, target: ToolTarget<'_>) -> Result<Value> {
        check_principal(principal)?;
        let namespace = self.engine.namespaces().resolve(target.namespace)?;
        let tools = self.tool_set(principal, &namespace)?;
        let mut definitions = Vec::new();
        for tool in &tools {
            definitions.push(self.definition(principal, &namespace, tool, target.filter)?);
        }
        let mut out = Map::new();
        out.insert("$schema".into(), Value::from(TOOL_SCHEMA));
        out.insert(
            "title".into(),
            Value::from(format!("Namespace {namespace} Tool Definitions")),
        );
        out.insert(
            "description".into(),
            Value::from(format!(
                "MCP tool bindings for the schemas namespace {namespace} reaches"
            )),
        );
        out.insert("tools".into(), Value::Array(definitions));
        Ok(Value::Object(out))
    }

    /// The namespace's visible tools as MCP lists them: the handle as the
    /// name, the title and description, the argument schema, whether it
    /// only reads, and `_meta` with the guidance and the invocation policy.
    pub fn mcp_tools(&self, principal: &Principal, target: ToolTarget<'_>) -> Result<Vec<Value>> {
        let manifest = self.manifest(principal, target)?;
        let policy_key = &self.invocation_policy().key;
        let mut tools = Vec::new();
        for tool in manifest["tools"].as_array().into_iter().flatten() {
            let mcp = &tool["mcp"];
            if mcp["hidden"] == Value::Bool(true) {
                continue;
            }
            let mut meta = mcp
                .get("_meta")
                .and_then(Value::as_object)
                .cloned()
                .unwrap_or_default();
            meta.insert(policy_key.clone(), mcp[policy_key.as_str()].clone());
            let mut entry = Map::new();
            entry.insert("name".into(), mcp["handle"].clone());
            entry.insert("title".into(), mcp["name"].clone());
            entry.insert("description".into(), mcp["description"].clone());
            entry.insert("inputSchema".into(), tool["parameters"].clone());
            entry.insert(
                "annotations".into(),
                json!({"readOnlyHint": tool["replay"]["mode"] == "read_only"}),
            );
            entry.insert("_meta".into(), Value::Object(meta));
            tools.push(Value::Object(entry));
        }
        Ok(tools)
    }

    /// Runs a tool by its MCP handle with its arguments and returns what
    /// the operation returns. The call goes through the schema registry or
    /// the instance store, which ask the policy. A handle the namespace has
    /// no visible tool for is an `UnknownToolError`.
    pub fn call(
        &self,
        principal: &Principal,
        handle: &str,
        args: &Value,
        target: ToolTarget<'_>,
    ) -> Result<Value> {
        check_principal(principal)?;
        let engine = self.engine;
        let namespace = engine.namespaces().resolve(target.namespace)?;
        let tool = self
            .tool_set(principal, &namespace)?
            .into_iter()
            .find(|candidate| candidate.handle == handle && candidate.hidden.is_none());
        let Some(tool) = tool.filter(|tool| {
            target
                .filter
                .is_none_or(|filter| filter.keeps(principal, &tool.summary(), &namespace))
        }) else {
            return Err(EngineError::unknown_tool(handle, &namespace).into());
        };
        let input = arguments_of(&tool, args)?;
        let schema = tool
            .record()
            .map(|record| record.name.clone())
            .unwrap_or_default();
        let ns = Some(namespace.as_str());
        match tool.kind {
            ToolKind::ListSchemas => {
                only(&tool, &input, &[])?;
                to_json(
                    &engine
                        .schemas()
                        .list(principal, SchemaTarget { namespace: ns })?,
                )
            }
            ToolKind::DescribeSchema => {
                only(&tool, &input, &["name"])?;
                let name = required_string(&tool, &input, "name")?;
                self.describe(
                    principal,
                    &name,
                    ToolTarget {
                        namespace: ns,
                        filter: None,
                    },
                )
            }
            ToolKind::DefineSchema => {
                only(&tool, &input, &["document"])?;
                let document = input.get("document").unwrap_or(&Value::Null);
                if !document.is_object() {
                    return Err(EngineError::invalid_argument(format!(
                        "{}: document is a schema-file document, a JSON object",
                        tool.handle
                    ))
                    .into());
                }
                let record = engine.schemas().define(
                    principal,
                    document,
                    DefineOptions {
                        namespace: ns,
                        source: None,
                    },
                )?;
                Ok(schema_view(&record)?)
            }
            ToolKind::ListBehaviors => {
                only(&tool, &input, &[])?;
                self.list_behaviors(principal)
            }
            ToolKind::DescribeBehavior => {
                only(&tool, &input, &["name"])?;
                self.describe_behavior(principal, &required_string(&tool, &input, "name")?)
            }
            ToolKind::Create => {
                only(&tool, &input, &["id", "data", "behaviors"])?;
                let id = optional_string(&tool, &input, "id")?;
                let Some(data) = input.get("data") else {
                    return Err(EngineError::invalid_argument(format!(
                        "{}: data, the instance, is required",
                        tool.handle
                    ))
                    .into());
                };
                let behaviors = input.get("behaviors").filter(|value| !value.is_null());
                to_json(&engine.instances().create(
                    principal,
                    &schema,
                    data,
                    CreateOptions {
                        namespace: ns,
                        id: id.as_deref(),
                        behaviors,
                    },
                )?)
            }
            ToolKind::Get => {
                only(&tool, &input, &["id", "valueRefs"])?;
                let id = required_string(&tool, &input, "id")?;
                let found = engine.instances().get(
                    principal,
                    &schema,
                    &id,
                    GetOptions {
                        namespace: ns,
                        value_refs: value_refs_of(&tool, &input)?,
                    },
                )?;
                match found {
                    Some(record) => to_json(&record),
                    None => Err(EngineError::new(
                        EngineErrorCode::NotFound,
                        format!("{schema} {id} does not exist in namespace {namespace}"),
                    )
                    .into()),
                }
            }
            ToolKind::List => {
                let mut allowed = vec!["limit", "cursor", "valueRefs"];
                if tool.filter.is_some() {
                    allowed.push("where");
                }
                only(&tool, &input, &allowed)?;
                let limit = match input.get("limit").filter(|value| !value.is_null()) {
                    None => None,
                    Some(Value::Number(number)) => {
                        let read = json::number_value(number);
                        if read.fract() != 0.0 || !read.is_finite() {
                            return Err(crate::paging::page_size_refusal(
                                &crate::calls::js_string(&Value::Number(number.clone())),
                            )
                            .into());
                        }
                        Some(read as i64)
                    }
                    Some(_) => {
                        return Err(EngineError::invalid_argument(format!(
                            "{}: limit is an integer",
                            tool.handle
                        ))
                        .into())
                    }
                };
                let cursor = optional_string(&tool, &input, "cursor")?;
                let filter = input.get("where").filter(|value| !value.is_null());
                if filter.is_some_and(|value| !value.is_object()) {
                    return Err(EngineError::invalid_argument(format!(
                        "{}: where is a JSON object of field values, by field",
                        tool.handle
                    ))
                    .into());
                }
                to_json(&engine.instances().list(
                    principal,
                    &schema,
                    ListOptions {
                        namespace: ns,
                        limit,
                        cursor: cursor.as_deref(),
                        value_refs: value_refs_of(&tool, &input)?,
                        filter,
                    },
                )?)
            }
            ToolKind::Lookup => {
                only(&tool, &input, &["key", "valueRefs"])?;
                let key = input.get("key").filter(|value| value.is_object());
                let Some(key) = key else {
                    return Err(EngineError::invalid_argument(format!(
                        "{}: key, the values of one unique index's fields, is a JSON object, and required",
                        tool.handle
                    ))
                    .into());
                };
                let found = engine.instances().lookup(
                    principal,
                    &schema,
                    key,
                    GetOptions {
                        namespace: ns,
                        value_refs: value_refs_of(&tool, &input)?,
                    },
                )?;
                match found {
                    Some(record) => to_json(&record),
                    None => Err(EngineError::new(
                        EngineErrorCode::NotFound,
                        format!(
                            "no {schema} in namespace {namespace} holds {}",
                            json::stringify(key)
                        ),
                    )
                    .into()),
                }
            }
            ToolKind::Update => {
                let mut allowed = vec!["id", "patch", "expectedSeq"];
                if tool.preconditions.is_some() {
                    allowed.push("preconditions");
                }
                only(&tool, &input, &allowed)?;
                let id = required_string(&tool, &input, "id")?;
                let Some(patch) = input.get("patch") else {
                    return Err(EngineError::invalid_argument(format!(
                        "{}: patch, a JSON merge patch of the instance, is required",
                        tool.handle
                    ))
                    .into());
                };
                to_json(&engine.instances().update(
                    principal,
                    &schema,
                    &id,
                    patch,
                    WriteOptions {
                        namespace: ns,
                        expected_seq: expected_seq_of(&tool, &input)?,
                        preconditions: preconditions_of(&tool, &input)?,
                    },
                )?)
            }
            ToolKind::Delete => {
                let mut allowed = vec!["id", "expectedSeq"];
                if tool.preconditions.is_some() {
                    allowed.push("preconditions");
                }
                only(&tool, &input, &allowed)?;
                let id = required_string(&tool, &input, "id")?;
                let deleted = engine.instances().delete(
                    principal,
                    &schema,
                    &id,
                    WriteOptions {
                        namespace: ns,
                        expected_seq: expected_seq_of(&tool, &input)?,
                        preconditions: preconditions_of(&tool, &input)?,
                    },
                )?;
                if !deleted {
                    return Err(EngineError::new(
                        EngineErrorCode::NotFound,
                        format!("{schema} {id} does not exist in namespace {namespace}"),
                    )
                    .into());
                }
                Ok(Value::Null)
            }
            ToolKind::Operation => {
                let mut allowed = vec!["id", "params", "expectedSeq"];
                if tool.preconditions.is_some() {
                    allowed.push("preconditions");
                }
                only(&tool, &input, &allowed)?;
                let id = required_string(&tool, &input, "id")?;
                let empty = Value::Object(Map::new());
                let params = input
                    .get("params")
                    .filter(|value| !value.is_null())
                    .unwrap_or(&empty);
                Ok(engine
                    .instances()
                    .operate(
                        principal,
                        &schema,
                        &id,
                        &tool.method_name,
                        params,
                        WriteOptions {
                            namespace: ns,
                            expected_seq: expected_seq_of(&tool, &input)?,
                            preconditions: preconditions_of(&tool, &input)?,
                        },
                    )?
                    .result)
            }
            ToolKind::SchemaOperation => {
                only(&tool, &input, &["params"])?;
                let empty = Value::Object(Map::new());
                let params = input
                    .get("params")
                    .filter(|value| !value.is_null())
                    .unwrap_or(&empty);
                engine
                    .instances()
                    .invoke_schema(principal, &schema, &tool.method_name, params, ns)
            }
            ToolKind::Search => engine.search(principal, &Value::Object(input), ns),
            ToolKind::GetValue => {
                only(&tool, &input, &["hash"])?;
                to_json(&engine.values().get(
                    principal,
                    &required_string(&tool, &input, "hash")?,
                    ns,
                )?)
            }
            ToolKind::ListNamespaces => {
                only(&tool, &input, &[])?;
                to_json(&engine.namespaces().list(principal)?)
            }
            ToolKind::CreateNamespace => {
                only(&tool, &input, &["name"])?;
                to_json(
                    &engine
                        .namespaces()
                        .create(principal, &required_string(&tool, &input, "name")?)?,
                )
            }
            ToolKind::ArchiveNamespace => {
                only(&tool, &input, &["name"])?;
                to_json(
                    &engine
                        .namespaces()
                        .archive(principal, &required_string(&tool, &input, "name")?)?,
                )
            }
            ToolKind::UnarchiveNamespace => {
                only(&tool, &input, &["name"])?;
                to_json(
                    &engine
                        .namespaces()
                        .unarchive(principal, &required_string(&tool, &input, "name")?)?,
                )
            }
        }
    }

    fn runtime(&self, record: &SchemaRecord) -> Result<Arc<VersionRuntime>> {
        let engine = self.engine;
        engine.read(|tx| engine.catalog.runtime_of(tx, record))
    }

    // tool_set lists the namespace's tools: the engine's, then each live
    // schema's, with every handle checked and each tool's visibility
    // decided.
    fn tool_set(&self, principal: &Principal, namespace: &str) -> Result<Vec<ToolSpec>> {
        let mut schema_tools: Vec<ToolSpec> = Vec::new();
        for summary in self.engine.schemas().list(
            principal,
            SchemaTarget {
                namespace: Some(namespace),
            },
        )? {
            schema_tools.extend(self.live_tools(principal, namespace, &summary)?);
        }
        let searches = schema_tools.iter().any(|tool| {
            tool.kind == ToolKind::SchemaOperation
                && tool.behavior.as_ref().is_some_and(|b| b.name == "Search")
                && tool.method_name == "search"
        });
        let engine_tools: Vec<ToolSpec> = self
            .engine_tools(namespace)
            .into_iter()
            .filter(|tool| tool.kind != ToolKind::Search || searches)
            .collect();
        let reserved: Vec<(String, String)> = engine_tools
            .iter()
            .map(|tool| (tool.handle.clone(), tool.name.clone()))
            .collect();
        let handles: Vec<(String, String)> = schema_tools
            .iter()
            .map(|tool| (tool.handle.clone(), tool.name.clone()))
            .collect();
        for (index, tool) in schema_tools.iter_mut().enumerate() {
            let sharing: Vec<&str> = handles
                .iter()
                .enumerate()
                .filter(|(other, (handle, _))| *other != index && *handle == tool.handle)
                .map(|(_, (_, name))| name.as_str())
                .collect();
            if let Some((_, owner)) = reserved.iter().find(|(handle, _)| *handle == tool.handle) {
                tool.hidden = Some(format!(
                    "its MCP handle {} is the handle of the engine's tool {owner}",
                    tool.handle
                ));
            } else if !is_snake_handle(&tool.handle) {
                tool.hidden = Some(format!(
                    "its MCP handle {} is not lowercase snake case starting with a letter",
                    tool.handle
                ));
            } else if tool.handle.len() > MCP_HANDLE_MAX_LENGTH {
                tool.hidden = Some(format!(
                    "its MCP handle {} is longer than {MCP_HANDLE_MAX_LENGTH} characters",
                    tool.handle
                ));
            } else if !sharing.is_empty() {
                tool.hidden = Some(format!(
                    "its MCP handle {} is also the handle of {}",
                    tool.handle,
                    sharing.join(", ")
                ));
            }
        }
        let mut all = engine_tools;
        all.extend(schema_tools);
        Ok(all)
    }

    // live_tools lists the tools of a schema the namespace reaches: none
    // without a live version, or when the engine cannot run its behaviors.
    fn live_tools(
        &self,
        principal: &Principal,
        namespace: &str,
        summary: &crate::registry::SchemaSummary,
    ) -> Result<Vec<ToolSpec>> {
        if summary.live_version.is_none() {
            return Ok(Vec::new());
        }
        let schemas = self.engine.schemas();
        let target = SchemaTarget {
            namespace: Some(namespace),
        };
        let Some(record) = schemas.live(principal, &summary.name, target)? else {
            return Ok(Vec::new());
        };
        let unavailable = |error: &Error| error.code() == Some(EngineErrorCode::Unavailable);
        let behaviors = match schemas.behaviors(
            principal,
            &summary.name,
            ValidateOptions {
                namespace: Some(namespace),
                version: None,
            },
        ) {
            Ok(behaviors) => behaviors,
            Err(error) if unavailable(&error) => return Ok(Vec::new()),
            Err(error) => return Err(error),
        };
        let record = Arc::new(record);
        let runtime = match self.runtime(&record) {
            Ok(runtime) => runtime,
            Err(error) if unavailable(&error) => return Ok(Vec::new()),
            Err(error) => return Err(error),
        };
        self.schema_tools(namespace, &record, &runtime, &behaviors)
    }

    fn engine_tools(&self, namespace: &str) -> Vec<ToolSpec> {
        let options = self.options();
        let encoded = encode_component(namespace);
        let base = format!("/namespaces/{encoded}/schemas");
        let tool = |kind: ToolKind,
                    handle: &str,
                    title: &str,
                    description: &str,
                    writes: bool,
                    http_method: &'static str,
                    http_path: String| ToolSpec {
            kind,
            name: format!("engine.{}", kind.name()),
            handle: handle.to_owned(),
            namespace: "engine".to_owned(),
            method_name: kind.name().to_owned(),
            operation_id: format!("engine.{}", kind.name()),
            title: title.to_owned(),
            description: description.to_owned(),
            writes,
            policy: options.invocation_of(kind.name()).to_owned(),
            http_method,
            http_path,
            schema: None,
            behavior: None,
            operation: None,
            create_params: Vec::new(),
            hidden: None,
            preconditions: None,
            filter: None,
            key: None,
        };
        vec![
            tool(ToolKind::ListSchemas, "list_schemas", "List schemas", "Lists the schemas this namespace reaches, its own and the shared namespace's, with each one's live version and whether it has a draft.", false, "GET", base.clone()),
            tool(ToolKind::DescribeSchema, "describe_schema", "Describe a schema", "Returns a schema's describe document: how a UI shows an instance (its display, and each field's title and icon), the JSON Schema of an instance, the behaviors its type composes with their config, and every operation with its parameters, its result and its tool.", false, "GET", format!("{base}/{{name}}/describe")),
            tool(ToolKind::DefineSchema, "define_schema", "Define a schema draft", "Stores a schema-file document (the form superschematic format --to=json writes) as the draft of its name, replacing the draft before it. It does not publish: a draft goes live only through the HTTP publish route.", true, "POST", base.clone()),
            tool(ToolKind::ListBehaviors, "list_behaviors", "List behaviors", "Lists the behaviors this engine runs, which a schema's types may compose: each one's name and description, the behaviors it requires and conflicts with, and the fields and operations it adds.", false, "GET", "/behaviors".to_owned()),
            tool(ToolKind::DescribeBehavior, "describe_behavior", "Describe a behavior", "Returns a behavior's declaration: the JSON Schema of the config a type gives it, of the parameters a create gives it and of the entry a write's preconditions carry for it, the behaviors it requires and conflicts with, its fields, its operations with their scope, parameters, result and invocation policy, and the codes its vetoes carry.", false, "GET", "/behaviors/{name}".to_owned()),
            tool(ToolKind::GetValue, "get_value", "Get a stored value", "Returns a value the engine stores by its hash, { hash, bytes, value }: a field whose JSON is longer than the engine's threshold, which an event (and an instance read with valueRefs) carries as a ref, { \"$value\": <hash>, \"bytes\": <n> }. The caller must be able to read a schema of this namespace whose instances, events or behaviors hold the value.", false, "GET", format!("/namespaces/{encoded}/values/{{hash}}")),
            tool(ToolKind::ListNamespaces, "list_namespaces", "List namespaces", "Lists the namespaces the caller may see: each one's name, whether the engine's options configure it or a create made it, whether it is the shared namespace, and whether it is archived.", false, "GET", "/namespaces".to_owned()),
            tool(ToolKind::CreateNamespace, "create_namespace", "Create a namespace", "Creates a namespace, which holds schemas and instances of its own and looks schema names up in the shared namespace after itself. Its name is lowercase letters, digits and hyphens, starting with a letter.", true, "POST", "/namespaces".to_owned()),
            tool(ToolKind::ArchiveNamespace, "archive_namespace", "Archive a namespace", "Archives a namespace a create made: its schemas, instances and events stay readable, and it refuses every write, and runs no reaction or schedule, until it is unarchived.", true, "POST", "/namespaces/{name}/archive".to_owned()),
            tool(ToolKind::UnarchiveNamespace, "unarchive_namespace", "Unarchive a namespace", "Lets an archived namespace be written again; its reactions and schedules pick up where they stopped.", true, "POST", "/namespaces/{name}/unarchive".to_owned()),
            tool(ToolKind::Search, "search", "Search every schema", "Searches the instances of every schema this namespace reaches that composes Search and that the caller may read, and returns one ranking: by query, by vector (with its model), or both. Each hit names its schema and says how it matched. Before creating an instance, search for its text to find near-duplicates.", false, "POST", format!("/namespaces/{encoded}/search")),
        ]
    }

    // schema_tools lists a live schema's tools: create, get, list, update,
    // delete, lookup when its instance type has a unique field, then its
    // behaviors' operations.
    fn schema_tools(
        &self,
        namespace: &str,
        record: &Arc<SchemaRecord>,
        runtime: &Arc<VersionRuntime>,
        behaviors: &[ComposedBehavior],
    ) -> Result<Vec<ToolSpec>> {
        let options = self.options();
        let name = record.name.as_str();
        let kebab = kebab_case(name);
        let schema_path = format!(
            "/namespaces/{}/schemas/{}",
            encode_component(namespace),
            encode_component(name)
        );
        let instances = format!("{schema_path}/instances");
        let spec = |kind: ToolKind, method_name: &str| ToolSpec {
            kind,
            name: format!("{kebab}.{method_name}"),
            handle: format!("{}_{}", snake_case(name), snake_case(method_name)),
            namespace: kebab.clone(),
            method_name: method_name.to_owned(),
            operation_id: format!("{name}.{method_name}"),
            title: String::new(),
            description: String::new(),
            writes: false,
            policy: String::new(),
            http_method: "GET",
            http_path: String::new(),
            schema: Some((Arc::clone(record), Arc::clone(runtime))),
            behavior: None,
            operation: None,
            create_params: Vec::new(),
            hidden: None,
            preconditions: None,
            filter: None,
            key: None,
        };
        let preconditions = preconditions_property(behaviors)?;
        let create_params: Vec<ComposedBehavior> = behaviors
            .iter()
            .filter(|behavior| behavior.declaration.get("createParamsSchema").is_some())
            .cloned()
            .collect();
        let filter = self.where_property(record, runtime, behaviors)?;
        let unique: Vec<&crate::indexes::OwnIndex> = runtime
            .indexes
            .iter()
            .filter(|index| index.unique)
            .collect();
        let mut create = spec(ToolKind::Create, "create");
        create.title = format!("Create {name}");
        create.description = if create_params.is_empty() {
            format!("Creates a {name}: validates the instance, data, against the schema's live version and stores it under id, or under a new id when none is given.")
        } else {
            format!(
                "Creates a {name}: validates the instance, data, against the schema's live version and stores it under id, or under a new id when none is given. behaviors gives its behaviors their create parameters ({}), which hold from the create on.",
                create_params.iter().map(|b| b.name.as_str()).collect::<Vec<_>>().join(", ")
            )
        };
        create.writes = true;
        create.policy = options.invocation_of("create").to_owned();
        create.http_method = "POST";
        create.http_path = instances.clone();
        create.create_params = create_params;
        let mut get = spec(ToolKind::Get, "get");
        get.title = format!("Get {name}");
        get.description = format!(
            "Returns the {name} with the id: its own fields in data, its behaviors' in behaviors."
        );
        get.policy = options.invocation_of("get").to_owned();
        get.http_path = format!("{instances}/{{id}}");
        let mut list = spec(ToolKind::List, "list");
        list.title = format!("List {name}");
        list.description = if filter.is_none() {
            format!("Lists {name} instances in creation order, a page at a time: pass a page's next as cursor for the page after it.")
        } else {
            format!("Lists {name} instances in creation order, a page at a time: pass a page's next as cursor for the page after it. where keeps the instances whose fields hold the values it gives, a list meaning any of them; a filtered page can hold fewer than limit while next is not null.")
        };
        list.policy = options.invocation_of("list").to_owned();
        list.http_path = instances.clone();
        list.filter = filter;
        let mut update = spec(ToolKind::Update, "update");
        update.title = format!("Update {name}");
        update.description = format!("Applies a JSON merge patch (RFC 7386) to the {name} with the id: a member replaces the instance's, a nested object merges and null removes a member. With expectedSeq the update is refused unless the instance is still at that sequence.");
        update.writes = true;
        update.policy = options.invocation_of("update").to_owned();
        update.http_method = "PATCH";
        update.http_path = format!("{instances}/{{id}}");
        update.preconditions = preconditions.clone();
        let mut delete = spec(ToolKind::Delete, "delete");
        delete.title = format!("Delete {name}");
        delete.description = format!("Deletes the {name} with the id. With expectedSeq the delete is refused unless the instance is still at that sequence.");
        delete.writes = true;
        delete.policy = options.invocation_of("delete").to_owned();
        delete.http_method = "DELETE";
        delete.http_path = format!("{instances}/{{id}}");
        delete.preconditions = preconditions.clone();
        let mut tools = vec![create, get, list, update, delete];
        if !unique.is_empty() {
            let mut lookup = spec(ToolKind::Lookup, "lookup");
            lookup.title = format!("Look up {name}");
            lookup.description = format!(
                "Returns the {name} whose unique fields hold the values key gives ({}), its own fields in data, its behaviors' in behaviors.",
                unique
                    .iter()
                    .map(|index| index.keys.join(" and "))
                    .collect::<Vec<_>>()
                    .join("; ")
            );
            lookup.policy = options.invocation_of("lookup").to_owned();
            lookup.http_path = format!("{schema_path}/lookup");
            lookup.key = Some(self.key_property(record, runtime, &unique)?);
            tools.push(lookup);
        }
        for behavior in behaviors {
            let declaration = declaration_of(behavior)?;
            for operation in declaration.operations {
                let schema_level = operation.scope == OperationScope::Schema;
                let mut tool = spec(
                    if schema_level {
                        ToolKind::SchemaOperation
                    } else {
                        ToolKind::Operation
                    },
                    &operation.name,
                );
                tool.title = format!("{name}: {}", operation.name);
                tool.description = operation
                    .description
                    .clone()
                    .filter(|description| !description.is_empty())
                    .unwrap_or_else(|| {
                        format!(
                            "Operation {} of behavior {}.",
                            operation.name, behavior.name
                        )
                    });
                tool.writes = operation.writes;
                tool.policy = operation
                    .invocation_policy
                    .clone()
                    .unwrap_or_else(|| options.invocation_policy.default.clone());
                tool.http_method = "POST";
                tool.http_path = format!(
                    "{}/operations/{}",
                    if schema_level {
                        schema_path.clone()
                    } else {
                        format!("{instances}/{{id}}")
                    },
                    encode_component(&operation.name)
                );
                tool.behavior = Some(behavior.clone());
                if !schema_level {
                    tool.preconditions = preconditions.clone();
                }
                tool.operation = Some(operation);
                tools.push(tool);
            }
        }
        Ok(tools)
    }

    // definition renders a tool as tools/schema.json writes it. A tool the
    // policy refuses the principal, or the mount's filter leaves out, is
    // hidden with that reason.
    fn definition(
        &self,
        principal: &Principal,
        namespace: &str,
        tool: &ToolSpec,
        filter: Option<&dyn ToolFilter>,
    ) -> Result<Value> {
        let options = self.options();
        let keys = &options.keys;
        let guidance = self.tool_guidance(tool)?;
        let refusal = match &tool.hidden {
            None => self.refusal(principal, namespace, tool)?,
            Some(_) => None,
        };
        let filtered = (tool.hidden.is_none()
            && refusal.is_none()
            && filter.is_some_and(|filter| !filter.keeps(principal, &tool.summary(), namespace)))
        .then(|| {
            format!(
                "this mount's tool filter leaves it out of {}'s tools",
                principal.subject
            )
        });
        let hidden = tool.hidden.clone().or(refusal).or(filtered);
        let mcp = match hidden {
            Some(reason) => json!({"hidden": true, "hiddenReason": reason}),
            None => {
                let mut record = Map::new();
                record.insert("hidden".into(), Value::Bool(false));
                record.insert("name".into(), Value::from(tool.title.as_str()));
                record.insert("handle".into(), Value::from(tool.handle.as_str()));
                record.insert("description".into(), Value::from(tool.description.as_str()));
                record.extend(policy_entry(&options.invocation_policy, &tool.policy));
                if !keys.guidance.is_empty() {
                    let mut meta = Map::new();
                    meta.insert(keys.guidance.clone(), to_json(&guidance)?);
                    record.insert("_meta".into(), Value::Object(meta));
                }
                Value::Object(record)
            }
        };
        let args = self.arguments(tool)?;
        let mut out = Map::new();
        out.insert("name".into(), Value::from(tool.name.as_str()));
        out.insert(
            "operationId".into(),
            Value::from(tool.operation_id.as_str()),
        );
        out.insert("title".into(), Value::from(tool.title.as_str()));
        out.insert("mcp".into(), mcp);
        for key in ["capability", "lifecycle", "visibility", "audience"] {
            out.insert(key.into(), Value::from(""));
        }
        out.insert("guidance".into(), to_json(&guidance)?);
        out.insert(
            "replay".into(),
            if tool.writes {
                Value::Null
            } else {
                json!({"mode": "read_only", "idempotencyKeyPointers": [], "expectedRevisionPointers": []})
            },
        );
        out.insert("description".into(), Value::from(tool.description.as_str()));
        out.insert("namespace".into(), Value::from(tool.namespace.as_str()));
        out.insert("methodName".into(), Value::from(tool.method_name.as_str()));
        out.insert("httpMethod".into(), Value::from(tool.http_method));
        out.insert("httpPath".into(), Value::from(tool.http_path.as_str()));
        out.insert("requiresAuth".into(), Value::Bool(true));
        out.insert("requiredPermissions".into(), json!([]));
        out.insert("isScoped".into(), Value::Bool(false));
        out.insert("bindingStatus".into(), Value::from("ready"));
        out.insert(
            "inputSchemaDigest".into(),
            Value::from(arguments_digest(&args, &keys.scalar)),
        );
        out.insert("parameters".into(), render_arguments(&args, &keys.scalar));
        out.insert("returns".into(), returns(tool));
        Ok(Value::Object(out))
    }

    // refusal is why the policy hides a tool from the principal.
    fn refusal(
        &self,
        principal: &Principal,
        namespace: &str,
        tool: &ToolSpec,
    ) -> Result<Option<String>> {
        let engine = self.engine;
        if tool.writes
            && !tool.kind.acts_on_namespaces()
            && engine.namespaces().archived(namespace)?
        {
            return Ok(Some(format!(
                "namespace {namespace} is archived: it refuses every write until it is unarchived"
            )));
        }
        if let Some((action, operation)) = tool.kind.listing() {
            if engine
                .access
                .allows_listing(principal, action, namespace, operation)
            {
                return Ok(None);
            }
            return Ok(Some(format!(
                "the access policy refuses {} {}",
                principal.subject,
                match operation {
                    None => format!("{} in namespace {namespace}", action.as_str()),
                    Some(operation) =>
                        format!("{} ({}) of namespaces", action.as_str(), operation.as_str()),
                }
            )));
        }
        let Some(record) = tool.record() else {
            return Ok(None);
        };
        let action = if tool.writes {
            SchemaAction::Write
        } else {
            SchemaAction::Read
        };
        let operation = matches!(tool.kind, ToolKind::Operation | ToolKind::SchemaOperation)
            .then_some(tool.method_name.as_str());
        if engine
            .access
            .allows_operation(principal, action, namespace, &record.name, operation)
        {
            return Ok(None);
        }
        Ok(Some(format!(
            "the access policy refuses {} {} on {}{}",
            principal.subject,
            action.as_str(),
            record.name,
            operation.map_or(String::new(), |operation| format!(" ({operation})"))
        )))
    }

    // arguments is a tool's argument schema, with the vendor keys at its
    // root.
    fn arguments(&self, tool: &ToolSpec) -> Result<ArgumentSchema> {
        let vendor = self.options().keys.parameters.clone();
        let id = Property {
            kind: Some("string".into()),
            description: Some("The instance id".into()),
            pattern: Some(INSTANCE_ID.to_owned()),
            ..Property::default()
        };
        let expected_seq = Property {
            kind: Some("integer".into()),
            description: Some("The sequence the caller last read (an instance's seq): the call is refused unless the instance is still at it".into()),
            minimum: Some(0.0),
            ..Property::default()
        };
        let value_refs = Property::of("boolean", "Return each field the value store holds as its ref, { \"$value\": <hash>, \"bytes\": <n> }, listed in valueRefs, rather than its value, which get_value reads");
        let schema = |properties: Vec<(&str, Property)>, required: &[&str]| {
            let mut required: Vec<String> = required.iter().map(|key| (*key).to_owned()).collect();
            required.sort();
            ArgumentSchema {
                vendor: vendor.clone(),
                properties: properties
                    .into_iter()
                    .map(|(key, property)| (key.to_owned(), property))
                    .collect(),
                required,
            }
        };
        let named = |description: &str, pattern: &str| Property {
            kind: Some("string".into()),
            description: Some(description.to_owned()),
            pattern: Some(pattern.to_owned()),
            ..Property::default()
        };
        let preconditions = |tool: &ToolSpec| -> Vec<(&str, Property)> {
            tool.preconditions
                .clone()
                .map(|property| ("preconditions", property))
                .into_iter()
                .collect()
        };
        Ok(match tool.kind {
            ToolKind::ListSchemas | ToolKind::ListBehaviors | ToolKind::ListNamespaces => {
                schema(Vec::new(), &[])
            }
            ToolKind::DescribeSchema => schema(
                vec![("name", named("The schema name", SCHEMA_NAME))],
                &["name"],
            ),
            ToolKind::DefineSchema => schema(
                vec![(
                    "document",
                    Property::raw(json!({"type": "object", "description": "The schema-file document: kind General, a name, and the types; superschematic format --to=json writes it"})),
                )],
                &["document"],
            ),
            ToolKind::DescribeBehavior => schema(
                vec![("name", named("The behavior name", BEHAVIOR_NAME))],
                &["name"],
            ),
            ToolKind::Search => {
                let properties = self.search_properties();
                ArgumentSchema {
                    vendor: vendor.clone(),
                    properties: properties
                        .into_iter()
                        .map(|(key, raw)| (key, Property::raw(raw)))
                        .collect(),
                    required: Vec::new(),
                }
            }
            ToolKind::GetValue => schema(
                vec![(
                    "hash",
                    named(
                        "The value's hash: the SHA-256 of its canonical JSON, as a ref's $value holds it",
                        VALUE_HASH,
                    ),
                )],
                &["hash"],
            ),
            ToolKind::CreateNamespace | ToolKind::ArchiveNamespace | ToolKind::UnarchiveNamespace => {
                schema(
                    vec![("name", named("The namespace name", NAMESPACE_NAME))],
                    &["name"],
                )
            }
            ToolKind::Create => {
                let (record, runtime) = tool.schema.as_ref().ok_or_else(no_schema)?;
                let mut id = id.clone();
                id.description = Some("The instance id; the engine makes one when it is absent".into());
                let mut properties = vec![("id", id), ("data", self.data_property(record, runtime)?)];
                if !tool.create_params.is_empty() {
                    let said = self.guidance_of(record, runtime)?;
                    properties.push((
                        "behaviors",
                        Property::raw(create_params_of(&tool.create_params, &said)),
                    ));
                }
                schema(properties, &["data"])
            }
            ToolKind::Get => schema(vec![("id", id), ("valueRefs", value_refs)], &["id"]),
            ToolKind::List => {
                let mut properties = vec![
                    (
                        "limit",
                        Property {
                            kind: Some("integer".into()),
                            description: Some("How many instances a page holds, 50 when absent".into()),
                            minimum: Some(1.0),
                            maximum: Some(MAX_PAGE_SIZE as f64),
                            ..Property::default()
                        },
                    ),
                    ("cursor", Property::of("string", "The previous page's next")),
                ];
                if let Some(filter) = &tool.filter {
                    properties.push(("where", filter.clone()));
                }
                properties.push(("valueRefs", value_refs));
                schema(properties, &[])
            }
            ToolKind::Lookup => schema(
                vec![
                    ("key", tool.key.clone().ok_or_else(no_schema)?),
                    ("valueRefs", value_refs),
                ],
                &["key"],
            ),
            ToolKind::Update => {
                let (record, runtime) = tool.schema.as_ref().ok_or_else(no_schema)?;
                let schemas = self.schemas_of(record, runtime)?;
                let mut patch = patch_of(
                    &self.data_property(record, runtime)?,
                    Some(&format!("A JSON merge patch of the {}", record.instance_type)),
                );
                if !schemas.patch_rules.is_empty() {
                    patch.all_of = Some(schemas.patch_rules.clone());
                }
                let mut properties = vec![("id", id), ("patch", patch), ("expectedSeq", expected_seq)];
                properties.extend(preconditions(tool));
                schema(properties, &["id", "patch"])
            }
            ToolKind::Delete => {
                let mut properties = vec![("id", id), ("expectedSeq", expected_seq)];
                properties.extend(preconditions(tool));
                schema(properties, &["id"])
            }
            ToolKind::Operation => {
                let params = tool
                    .operation
                    .as_ref()
                    .map(|operation| operation.params_schema.clone())
                    .unwrap_or(Value::Null);
                let required = params_required(&params);
                let mut properties = vec![
                    ("id", id),
                    ("params", Property::raw(params)),
                    ("expectedSeq", expected_seq),
                ];
                properties.extend(preconditions(tool));
                schema(properties, if required { &["id", "params"] } else { &["id"] })
            }
            ToolKind::SchemaOperation => {
                let params = tool
                    .operation
                    .as_ref()
                    .map(|operation| operation.params_schema.clone())
                    .unwrap_or(Value::Null);
                let required = params_required(&params);
                schema(
                    vec![("params", Property::raw(params))],
                    if required { &["params"] } else { &[] },
                )
            }
        })
    }

    // search_properties are the search across schemas' parameters: the
    // Search behavior's search parameters, then a vector and its model.
    fn search_properties(&self) -> Vec<(String, Value)> {
        let registry = self.engine.catalog.registry();
        let mut properties: Vec<(String, Value)> = registry
            .lookup("Search")
            .and_then(|search| {
                search
                    .declaration
                    .operations
                    .iter()
                    .find(|op| op.name == "search")
                    .cloned()
            })
            .and_then(|operation| {
                operation
                    .params_schema
                    .get("properties")
                    .and_then(Value::as_object)
                    .cloned()
            })
            .map(|properties| properties.into_iter().collect())
            .unwrap_or_default();
        properties.retain(|(key, _)| key != "vector" && key != "model");
        properties.push((
            "vector".into(),
            json!({"description": "The vector to search for, computed with model: it ranks the schemas whose vectors come from that model, with as many dimensions.", "type": "array", "minItems": 1, "maxItems": 4096, "items": {"type": "number"}}),
        ));
        properties.push((
            "model".into(),
            json!({"description": "The model the vector was computed with; required with a vector.", "type": "string", "minLength": 1, "maxLength": 200}),
        ));
        properties
    }

    // data_property is the instance's own fields: the schema a create
    // takes, with what the behaviors hold them to.
    fn data_property(&self, record: &SchemaRecord, runtime: &VersionRuntime) -> Result<Property> {
        let schemas = self.schemas_of(record, runtime)?;
        Ok(Property {
            kind: Some("object".into()),
            description: Some(format!("{} object", record.instance_type)),
            closed: true,
            properties: Some(schemas.properties.clone()),
            required: Some(schemas.required.clone()),
            all_of: (!schemas.instance_rules.is_empty()).then(|| schemas.instance_rules.clone()),
            ..Property::default()
        })
    }

    // where_property is a list's where argument: a member per field the
    // version filters on; None when it filters on none.
    fn where_property(
        &self,
        record: &SchemaRecord,
        runtime: &VersionRuntime,
        behaviors: &[ComposedBehavior],
    ) -> Result<Option<Property>> {
        if runtime.filters.is_empty() {
            return Ok(None);
        }
        let mut properties = Map::new();
        for filterable in &runtime.filters {
            let one = self.filter_value_schema(record, runtime, behaviors, filterable, true)?;
            properties.insert(
                filterable.key.clone(),
                json!({"anyOf": [one, {"type": "array", "items": one, "minItems": 1, "maxItems": crate::instances::MAX_FILTER_VALUES}]}),
            );
        }
        Ok(Some(Property::raw(json!({
            "type": "object",
            "description": format!("The values the instances hold, by field: a value, null for none, or a list of 1 to {} meaning any of them; every member must hold", crate::instances::MAX_FILTER_VALUES),
            "additionalProperties": false,
            "properties": properties,
        }))))
    }

    // key_property is a lookup's key argument: the values of one unique
    // index's fields, each required.
    fn key_property(
        &self,
        record: &SchemaRecord,
        runtime: &VersionRuntime,
        unique: &[&crate::indexes::OwnIndex],
    ) -> Result<Property> {
        let mut branches = Vec::new();
        for index in unique {
            let mut properties = Map::new();
            for key in &index.keys {
                let filterable = runtime
                    .filters
                    .iter()
                    .find(|filterable| filterable.key == *key)
                    .ok_or_else(|| {
                        Error::internal(format!("a unique field {key} no list filters on"))
                    })?;
                properties.insert(
                    key.clone(),
                    self.filter_value_schema(record, runtime, &[], filterable, false)?,
                );
            }
            branches.push(json!({
                "type": "object",
                "additionalProperties": false,
                "properties": properties,
                "required": index.keys,
            }));
        }
        let description =
            "The values of the fields of one unique index of the instance type, by field";
        Ok(Property::raw(if branches.len() == 1 {
            let mut only = branches.remove(0);
            only["description"] = Value::from(description);
            only
        } else {
            json!({"type": "object", "description": description, "oneOf": branches})
        }))
    }

    // filter_value_schema is the JSON Schema of one value of a field a
    // filter or a key names.
    fn filter_value_schema(
        &self,
        record: &SchemaRecord,
        runtime: &VersionRuntime,
        behaviors: &[ComposedBehavior],
        filterable: &crate::instances::filters::Filterable,
        nullable: bool,
    ) -> Result<Value> {
        let key = filterable.key.as_str();
        let type_name = filterable.scalar.name();
        let behavior = filterable.behavior.as_deref();
        if behavior.is_none() {
            let schemas = self.schemas_of(record, runtime)?;
            if let Some((_, property)) = schemas.properties.iter().find(|(name, _)| name == key) {
                let mut property = property.clone();
                property.nullable = nullable;
                return Ok(render_property(&property, &self.keys().scalar));
            }
        }
        let name = match behavior {
            None => key,
            Some(behavior) => key.get(behavior.len() + 1..).unwrap_or(key),
        };
        let description = filterable.description.clone().or_else(|| {
            behaviors
                .iter()
                .find(|candidate| Some(candidate.name.as_str()) == behavior)
                .and_then(|candidate| candidate.declaration.get("fields"))
                .and_then(Value::as_array)
                .and_then(|fields| {
                    fields
                        .iter()
                        .find(|field| field.get("name").and_then(Value::as_str) == Some(name))
                })
                .and_then(|field| text_of(field, "description"))
                .map(str::to_owned)
        });
        let mut out = Map::new();
        out.insert(
            "type".into(),
            if nullable {
                json!([type_name, "null"])
            } else {
                Value::from(type_name)
            },
        );
        if let Some(description) = description.filter(|d| !d.is_empty()) {
            out.insert("description".into(), Value::from(description));
        }
        Ok(Value::Object(out))
    }

    // instance_schema is an instance's data as reads return it: its own
    // fields, with what the behaviors hold them to.
    fn instance_schema(&self, record: &SchemaRecord, runtime: &VersionRuntime) -> Result<Value> {
        let schemas = self.schemas_of(record, runtime)?;
        let mut schema = render_arguments(
            &ArgumentSchema {
                vendor: Vec::new(),
                properties: schemas.properties.clone(),
                required: schemas.required.clone(),
            },
            &self.keys().scalar,
        );
        if !schemas.instance_rules.is_empty() {
            schema["allOf"] = Value::Array(schemas.instance_rules.clone());
        }
        Ok(schema)
    }

    fn tool_guidance(&self, tool: &ToolSpec) -> Result<ToolGuidance> {
        let Some((record, runtime)) = &tool.schema else {
            return Ok(engine_guidance(tool.kind.name()));
        };
        let said = self.guidance_of(record, runtime)?;
        said.operation(&tool.method_name).cloned().ok_or_else(|| {
            Error::internal(format!(
                "no guidance for {}, an operation of the version",
                tool.name
            ))
        })
    }

    // guidance_of is what a version's behaviors say about it, computed once.
    fn guidance_of(
        &self,
        record: &SchemaRecord,
        runtime: &VersionRuntime,
    ) -> Result<Arc<VersionGuidance>> {
        let key = version_key(record);
        let cache = &self.engine.tools.guidance;
        if let Some(known) = cache
            .lock()
            .unwrap_or_else(PoisonError::into_inner)
            .get(&key)
        {
            return Ok(Arc::clone(known));
        }
        let computed = Arc::new(version_guidance(
            &record.name,
            &runtime.composition,
            &FieldGuidance {
                unique: runtime
                    .indexes
                    .iter()
                    .filter(|index| index.unique)
                    .map(|index| index.keys.clone())
                    .collect(),
                filters: runtime
                    .filters
                    .iter()
                    .map(|filterable| filterable.key.clone())
                    .collect(),
            },
        )?);
        cache
            .lock()
            .unwrap_or_else(PoisonError::into_inner)
            .insert(key, Arc::clone(&computed));
        Ok(computed)
    }

    fn schemas_of(
        &self,
        record: &SchemaRecord,
        runtime: &VersionRuntime,
    ) -> Result<Arc<InstanceSchemas>> {
        let key = version_key(record);
        let cache = &self.engine.tools.schemas;
        if let Some(known) = cache
            .lock()
            .unwrap_or_else(PoisonError::into_inner)
            .get(&key)
        {
            return Ok(Arc::clone(known));
        }
        let fields = FieldSchemas::new(&record.document);
        let (properties, required) = fields.object(&record.instance_type);
        let computed = Arc::new(InstanceSchemas {
            properties,
            required,
            instance_rules: self.instance_rules(runtime, &fields, InstanceSchemaForm::Instance)?,
            patch_rules: self.instance_rules(runtime, &fields, InstanceSchemaForm::Patch)?,
        });
        cache
            .lock()
            .unwrap_or_else(PoisonError::into_inner)
            .insert(key, Arc::clone(&computed));
        Ok(computed)
    }

    // instance_rules is what a version's behaviors hold an instance's own
    // fields to, in a form: each behavior's instanceSchema, in list order,
    // which renders the types its checkedTypes names as this document
    // renders a nested type.
    fn instance_rules(
        &self,
        runtime: &VersionRuntime,
        fields: &FieldSchemas<'_>,
        form: InstanceSchemaForm,
    ) -> Result<Vec<Value>> {
        let scalar_key = &self.keys().scalar;
        let mut rules = Vec::new();
        for bound in &runtime.composition.behaviors {
            let name = bound.behavior.name.as_str();
            let refused: std::cell::RefCell<Option<String>> = std::cell::RefCell::new(None);
            let render = |type_name: &str, nullable: bool| -> Value {
                if !bound.checked.iter().any(|checked| checked == type_name) {
                    refused.borrow_mut().get_or_insert_with(|| {
                        format!(
                            "instanceSchema: {type_name} is not a type its checkedTypes names ({})",
                            if bound.checked.is_empty() {
                                "none".to_owned()
                            } else {
                                bound.checked.join(", ")
                            }
                        )
                    });
                    return Value::Null;
                }
                let value = fields.type_schema(type_name);
                let mut shaped = match form {
                    InstanceSchemaForm::Patch => patch_of(&value, None),
                    InstanceSchemaForm::Instance => value,
                };
                shaped.nullable = nullable;
                render_property(&shaped, scalar_key)
            };
            let Some(answer) =
                bound
                    .behavior
                    .implementation
                    .instance_schema(&bound.config, form, &render)
            else {
                continue;
            };
            let schemas = answer?;
            if let Some(problem) = refused.into_inner() {
                return Err(Error::behavior(name, problem));
            }
            rules.extend(schemas.iter().map(json::js_copy));
        }
        Ok(rules)
    }

    fn result_schema(&self, tool: &ToolSpec, behaviors: &[ComposedBehavior]) -> Result<Value> {
        Ok(match tool.kind {
            ToolKind::Create | ToolKind::Get | ToolKind::Update | ToolKind::Lookup => {
                let (record, runtime) = tool.schema.as_ref().ok_or_else(no_schema)?;
                instance_record_schema(
                    self.instance_schema(record, runtime)?,
                    behaviors_schema(behaviors),
                )
            }
            ToolKind::List => {
                let (record, runtime) = tool.schema.as_ref().ok_or_else(no_schema)?;
                json!({
                    "type": "object",
                    "additionalProperties": false,
                    "properties": {
                        "items": {"type": "array", "items": instance_record_schema(self.instance_schema(record, runtime)?, behaviors_schema(behaviors))},
                        "next": {"type": ["string", "null"], "description": "The next page's cursor; null after the last page"}
                    },
                    "required": ["items", "next"]
                })
            }
            ToolKind::Delete => json!({"type": "null"}),
            ToolKind::Operation | ToolKind::SchemaOperation => tool
                .operation
                .as_ref()
                .map(|operation| operation.result_schema.clone())
                .unwrap_or(Value::Bool(true)),
            _ => Value::Bool(true),
        })
    }
}

fn no_schema() -> Error {
    Error::internal("a schema's tool without its schema")
}

// returns is a tool's return shape as tools/schema.json writes it
// (ir.ToolReturnSchema).
fn returns(tool: &ToolSpec) -> Value {
    let name = tool
        .record()
        .map(|record| record.name.clone())
        .unwrap_or_default();
    match tool.kind {
        ToolKind::Create | ToolKind::Get | ToolKind::Update | ToolKind::Lookup => {
            json!({"type": "object", "description": format!("{name} instance")})
        }
        ToolKind::List => {
            json!({"type": "object", "description": format!("A page of {name} instances")})
        }
        ToolKind::Delete => {
            json!({"type": "null", "description": "Nothing: the instance is deleted"})
        }
        ToolKind::ListSchemas => {
            json!({"type": "array", "description": "Array of schema summaries", "items": {"type": "object", "description": "A schema summary"}})
        }
        ToolKind::DescribeSchema => {
            json!({"type": "object", "description": "The describe document"})
        }
        ToolKind::DefineSchema => json!({"type": "object", "description": "The draft"}),
        ToolKind::ListBehaviors => {
            json!({"type": "array", "description": "Array of behavior summaries", "items": {"type": "object", "description": "A behavior summary"}})
        }
        ToolKind::DescribeBehavior => {
            json!({"type": "object", "description": "The behavior's declaration"})
        }
        ToolKind::Search => {
            json!({"type": "object", "description": "A page of hits across the schemas, best first"})
        }
        ToolKind::GetValue => {
            json!({"type": "object", "description": "The value, with its hash and its canonical JSON's length in bytes"})
        }
        ToolKind::ListNamespaces => {
            json!({"type": "array", "description": "Array of namespaces", "items": {"type": "object", "description": "A namespace"}})
        }
        ToolKind::CreateNamespace | ToolKind::ArchiveNamespace | ToolKind::UnarchiveNamespace => {
            json!({"type": "object", "description": "The namespace"})
        }
        ToolKind::Operation | ToolKind::SchemaOperation => {
            let result = tool
                .operation
                .as_ref()
                .map(|operation| operation.result_schema.clone())
                .unwrap_or(Value::Null);
            let kind = type_of_schema(&result);
            let description = result
                .get("description")
                .and_then(Value::as_str)
                .map(str::to_owned)
                .unwrap_or_else(|| format!("The result of {}", tool.method_name));
            let mut out = Map::new();
            out.insert("type".into(), kind.clone());
            out.insert("description".into(), Value::from(description));
            if kind == "array" {
                if let Some(items) = result.get("items") {
                    out.insert("items".into(), json!({"type": type_of_schema(items)}));
                }
            }
            Value::Object(out)
        }
    }
}

// type_of_schema is a result schema's type as ir.ToolSchemaType holds it:
// its type, or every JSON type when it names none.
fn type_of_schema(schema: &Value) -> Value {
    if let Some(kind) = schema.get("type") {
        match kind {
            Value::String(_) => return kind.clone(),
            Value::Array(kinds) if !kinds.is_empty() && kinds.iter().all(Value::is_string) => {
                return kind.clone()
            }
            _ => {}
        }
    }
    json!(["object", "array", "string", "number", "boolean", "null"])
}

/// A behavior's summary, as `list_behaviors` and `GET /behaviors` list it.
fn behavior_summary(declaration: &BehaviorDeclaration) -> Value {
    let mut out = Map::new();
    out.insert("name".into(), Value::from(declaration.name.as_str()));
    if let Some(description) = declaration.description.as_deref().filter(|d| !d.is_empty()) {
        out.insert("description".into(), Value::from(description));
    }
    out.insert("requires".into(), json!(declaration.requires));
    out.insert("conflicts".into(), json!(declaration.conflicts));
    out.insert(
        "fields".into(),
        Value::Array(
            declaration
                .fields
                .iter()
                .map(|field| Value::from(field.name.as_str()))
                .collect(),
        ),
    );
    out.insert(
        "operations".into(),
        Value::Array(
            declaration
                .operations
                .iter()
                .map(|operation| Value::from(operation.name.as_str()))
                .collect(),
        ),
    );
    Value::Object(out)
}

/// A behavior's document: its declaration with every list present and
/// each operation's scope, whether it writes and its invocation policy
/// under the policy's key.
fn behavior_document(declaration: &BehaviorDeclaration, policy: &InvocationPolicy) -> Value {
    let mut out = Map::new();
    out.insert("name".into(), Value::from(declaration.name.as_str()));
    if let Some(description) = declaration.description.as_deref().filter(|d| !d.is_empty()) {
        out.insert("description".into(), Value::from(description));
    }
    for (key, schema) in [
        ("configSchema", &declaration.config_schema),
        ("createParamsSchema", &declaration.create_params_schema),
        ("preconditionSchema", &declaration.precondition_schema),
    ] {
        if let Some(schema) = schema {
            out.insert(key.into(), json::js_copy(schema));
        }
    }
    out.insert("requires".into(), json!(declaration.requires));
    out.insert("conflicts".into(), json!(declaration.conflicts));
    out.insert("fields".into(), declared_fields(declaration));
    out.insert(
        "operations".into(),
        Value::Array(
            declaration
                .operations
                .iter()
                .map(|operation| {
                    let mut entry = Map::new();
                    entry.insert("name".into(), Value::from(operation.name.as_str()));
                    if let Some(description) =
                        operation.description.as_deref().filter(|d| !d.is_empty())
                    {
                        entry.insert("description".into(), Value::from(description));
                    }
                    entry.insert("scope".into(), Value::from(operation.scope.as_str()));
                    entry.insert("writes".into(), Value::Bool(operation.writes));
                    entry.insert(
                        policy.key.clone(),
                        Value::from(
                            operation
                                .invocation_policy
                                .clone()
                                .unwrap_or_else(|| policy.default.clone()),
                        ),
                    );
                    entry.insert(
                        "paramsSchema".into(),
                        json::js_copy(&operation.params_schema),
                    );
                    entry.insert(
                        "resultSchema".into(),
                        json::js_copy(&operation.result_schema),
                    );
                    Value::Object(entry)
                })
                .collect(),
        ),
    );
    out.insert("vetoes".into(), declared_vetoes(declaration));
    Value::Object(out)
}

fn declared_fields(declaration: &BehaviorDeclaration) -> Value {
    Value::Array(
        declaration
            .fields
            .iter()
            .map(|field| {
                let mut entry = Map::new();
                entry.insert("name".into(), Value::from(field.name.as_str()));
                if let Some(description) = field.description.as_deref().filter(|d| !d.is_empty()) {
                    entry.insert("description".into(), Value::from(description));
                }
                if field.volatile {
                    entry.insert("volatile".into(), Value::Bool(true));
                }
                Value::Object(entry)
            })
            .collect(),
    )
}

fn declared_vetoes(declaration: &BehaviorDeclaration) -> Value {
    Value::Array(
        declaration
            .vetoes
            .iter()
            .map(|veto| {
                let mut entry = Map::new();
                entry.insert("code".into(), Value::from(veto.code.as_str()));
                if let Some(description) = veto.description.as_deref().filter(|d| !d.is_empty()) {
                    entry.insert("description".into(), Value::from(description));
                }
                Value::Object(entry)
            })
            .collect(),
    )
}

fn declaration_of(behavior: &ComposedBehavior) -> Result<BehaviorDeclaration> {
    BehaviorDeclaration::parse(&behavior.declaration.to_string()).map_err(|problems| {
        Error::internal(format!(
            "behavior {}'s declaration does not read: {}",
            behavior.name,
            problems.join("; ")
        ))
    })
}

/// A create's behaviors argument: by behavior name, the create parameters
/// of each behavior that declares a createParamsSchema: what its
/// implementation narrows them to under its config, else the
/// declaration's.
fn create_params_of(behaviors: &[ComposedBehavior], said: &VersionGuidance) -> Value {
    let mut properties = Map::new();
    for behavior in behaviors {
        properties.insert(
            behavior.name.clone(),
            said.create_params(&behavior.name)
                .cloned()
                .or_else(|| behavior.declaration.get("createParamsSchema").cloned())
                .unwrap_or(Value::Null),
        );
    }
    json!({
        "type": "object",
        "description": "Each behavior's create parameters, by behavior name; they hold from the create on, in its transaction",
        "additionalProperties": false,
        "properties": properties,
    })
}

// params_required is whether an operation's parameters must hold a member:
// its schema requires one, or every branch of its anyOf or oneOf does.
fn params_required(params: &Value) -> bool {
    let Value::Object(members) = params else {
        return false;
    };
    if members
        .get("required")
        .and_then(Value::as_array)
        .is_some_and(|required| !required.is_empty())
    {
        return true;
    }
    ["anyOf", "oneOf"].iter().any(|key| {
        members
            .get(*key)
            .and_then(Value::as_array)
            .is_some_and(|branches| !branches.is_empty() && branches.iter().all(params_required))
    })
}

/// The JSON Schema of an instance's behaviors as reads return them: by
/// behavior name, for each behavior on the type that declares a field, an
/// object of its fields, read-only, a volatile one marked `volatile: true`.
fn behaviors_schema(behaviors: &[ComposedBehavior]) -> Value {
    let mut properties = Map::new();
    for behavior in behaviors {
        let fields = behavior
            .declaration
            .get("fields")
            .and_then(Value::as_array)
            .cloned()
            .unwrap_or_default();
        if fields.is_empty() {
            continue;
        }
        let mut members = Map::new();
        for field in &fields {
            let mut entry = Map::new();
            if let Some(description) = text_of(field, "description") {
                entry.insert("description".into(), Value::from(description));
            }
            entry.insert("readOnly".into(), Value::Bool(true));
            if field.get("volatile") == Some(&Value::Bool(true)) {
                entry.insert("volatile".into(), Value::Bool(true));
            }
            members.insert(
                text_of(field, "name").unwrap_or_default().to_owned(),
                Value::Object(entry),
            );
        }
        properties.insert(
            behavior.name.clone(),
            json!({
                "type": "object",
                "description": format!("The fields of behavior {}", behavior.name),
                "additionalProperties": false,
                "readOnly": true,
                "properties": members,
            }),
        );
    }
    let required: Vec<Value> = properties
        .keys()
        .map(|key| Value::from(key.as_str()))
        .collect();
    json!({
        "type": "object",
        "description": "The instance's behaviors' fields, by behavior name, apart from its own fields",
        "additionalProperties": false,
        "properties": properties,
        "required": required,
    })
}

/// The JSON Schema of an instance as the engine returns it.
fn instance_record_schema(data: Value, behaviors: Value) -> Value {
    json!({
        "type": "object",
        "additionalProperties": false,
        "properties": {
            "namespace": {"type": "string", "description": "The namespace that holds the instance"},
            "schema": {"type": "string"},
            "id": {"type": "string"},
            "schemaNamespace": {"type": "string", "description": "The namespace that holds the schema"},
            "version": {"type": "integer", "description": "The schema version the instance was last written with"},
            "seq": {"type": "integer", "description": "The sequence of the instance's last event: its entity tag"},
            "data": data,
            "behaviors": behaviors,
            "createdAt": {"type": "integer", "description": "Epoch milliseconds"},
            "createdBy": {"type": "string"},
            "updatedAt": {"type": "integer", "description": "Epoch milliseconds"},
            "updatedBy": {"type": "string"},
            "valueRefs": {
                "type": "array",
                "items": {"type": "string"},
                "description": "For a read with valueRefs: the JSON pointers into data of the fields that hold a ref, { \"$value\": <hash>, \"bytes\": <n> }, in place of a value the value store holds"
            }
        },
        "required": ["namespace", "schema", "id", "schemaNamespace", "version", "seq", "data", "behaviors", "createdAt", "createdBy", "updatedAt", "updatedBy"]
    })
}

// preconditions_property is the preconditions argument of a schema's
// writes: an entry for each behavior that declares a preconditionSchema;
// None when none does.
fn preconditions_property(behaviors: &[ComposedBehavior]) -> Result<Option<Property>> {
    let properties: Vec<(String, Property)> = behaviors
        .iter()
        .filter_map(|behavior| {
            behavior
                .declaration
                .get("preconditionSchema")
                .map(|schema| (behavior.name.clone(), Property::raw(schema.clone())))
        })
        .collect();
    if properties.is_empty() {
        return Ok(None);
    }
    Ok(Some(Property {
        kind: Some("object".into()),
        description: Some("Preconditions by behavior: each entry is checked against its behavior's schema and handed to its guard, which refuses the call when it does not hold".into()),
        closed: true,
        properties: Some(properties),
        required: Some(Vec::new()),
        ..Property::default()
    }))
}

/// The instance type's display (@display, D48) as the describe document
/// carries it: its title and summary fields by their keys in an
/// instance's data.
fn described_display(type_def: &Value) -> Option<Value> {
    let display = type_def.get("display")?.as_object()?;
    let fields = type_def
        .get("fields")
        .and_then(Value::as_array)
        .cloned()
        .unwrap_or_default();
    let key_of = |name: &str| -> String {
        fields
            .iter()
            .find(|field| field.get("name").and_then(Value::as_str) == Some(name))
            .map_or_else(|| name.to_owned(), |field| json_key(field).to_owned())
    };
    let mut out = display.clone();
    if let Some(title) = display.get("titleField").and_then(Value::as_str) {
        out.insert("titleField".into(), Value::from(key_of(title)));
    }
    if let Some(Value::Array(summary)) = display.get("summaryFields") {
        out.insert(
            "summaryFields".into(),
            Value::Array(
                summary
                    .iter()
                    .map(|name| Value::from(key_of(name.as_str().unwrap_or_default())))
                    .collect(),
            ),
        );
    }
    Some(Value::Object(out))
}

fn text_of<'v>(value: &'v Value, key: &str) -> Option<&'v str> {
    value
        .get(key)
        .and_then(Value::as_str)
        .filter(|text| !text.is_empty())
}

/// A schema version as the API returns it: the stored record without its
/// canonical text, which the hash identifies.
pub(crate) fn schema_view(record: &SchemaRecord) -> Result<Value> {
    let mut view = to_json(record)?;
    if let Value::Object(members) = &mut view {
        members.shift_remove("canonical");
    }
    Ok(view)
}

fn to_json(value: &impl serde::Serialize) -> Result<Value> {
    serde_json::to_value(value)
        .map_err(|error| Error::internal(format!("a result does not write as JSON: {error}")))
}

/// A name as JavaScript's `encodeURIComponent` writes it.
pub(crate) fn encode_component(text: &str) -> String {
    let mut out = String::with_capacity(text.len());
    for byte in text.bytes() {
        let keep = byte.is_ascii_alphanumeric()
            || matches!(
                byte,
                b'-' | b'_' | b'.' | b'!' | b'~' | b'*' | b'\'' | b'(' | b')'
            );
        if keep {
            out.push(char::from(byte));
        } else {
            out.push_str(&format!("%{byte:02X}"));
        }
    }
    out
}

// arguments_of reads a call's arguments: a JSON object, {} when absent.
fn arguments_of(tool: &ToolSpec, args: &Value) -> Result<Map<String, Value>> {
    match args {
        Value::Null => Ok(Map::new()),
        Value::Object(members) => Ok(members.clone()),
        _ => Err(EngineError::invalid_argument(format!(
            "{}: the arguments are a JSON object",
            tool.handle
        ))
        .into()),
    }
}

fn only(tool: &ToolSpec, args: &Map<String, Value>, allowed: &[&str]) -> Result<()> {
    let unknown: Vec<String> = args
        .keys()
        .filter(|key| !allowed.contains(&key.as_str()))
        .map(|key| json::quote(key))
        .collect();
    if unknown.is_empty() {
        return Ok(());
    }
    Err(EngineError::invalid_argument(format!(
        "{} takes {}, not {}",
        tool.handle,
        if allowed.is_empty() {
            "no arguments".to_owned()
        } else {
            allowed.join(", ")
        },
        unknown.join(", ")
    ))
    .into())
}

fn required_string(tool: &ToolSpec, args: &Map<String, Value>, key: &str) -> Result<String> {
    match args.get(key) {
        Some(Value::String(text)) => Ok(text.clone()),
        _ => Err(EngineError::invalid_argument(format!(
            "{}: {key} is a string, and required",
            tool.handle
        ))
        .into()),
    }
}

fn optional_string(
    tool: &ToolSpec,
    args: &Map<String, Value>,
    key: &str,
) -> Result<Option<String>> {
    match args.get(key) {
        None | Some(Value::Null) => Ok(None),
        Some(Value::String(text)) => Ok(Some(text.clone())),
        Some(_) => {
            Err(EngineError::invalid_argument(format!("{}: {key} is a string", tool.handle)).into())
        }
    }
}

fn value_refs_of(tool: &ToolSpec, args: &Map<String, Value>) -> Result<bool> {
    match args.get("valueRefs") {
        None | Some(Value::Null) => Ok(false),
        Some(Value::Bool(flag)) => Ok(*flag),
        Some(_) => Err(EngineError::invalid_argument(format!(
            "{}: valueRefs is a boolean",
            tool.handle
        ))
        .into()),
    }
}

fn expected_seq_of(tool: &ToolSpec, args: &Map<String, Value>) -> Result<Option<i64>> {
    match args.get("expectedSeq") {
        None | Some(Value::Null) => Ok(None),
        Some(Value::Number(number)) => {
            let read = json::number_value(number);
            if read.fract() != 0.0 || read < 0.0 || !read.is_finite() {
                return Err(
                    crate::instances::expected_seq_refusal(&crate::calls::js_string(
                        &Value::Number(number.clone()),
                    ))
                    .into(),
                );
            }
            Ok(Some(read as i64))
        }
        Some(_) => Err(EngineError::invalid_argument(format!(
            "{}: expectedSeq is a non-negative integer",
            tool.handle
        ))
        .into()),
    }
}

fn preconditions_of<'a>(
    tool: &ToolSpec,
    args: &'a Map<String, Value>,
) -> Result<Option<&'a Value>> {
    match args.get("preconditions") {
        None | Some(Value::Null) => Ok(None),
        Some(value @ Value::Object(_)) => Ok(Some(value)),
        Some(_) => Err(EngineError::invalid_argument(format!(
            "{}: preconditions is a JSON object of each behavior's entry by its name",
            tool.handle
        ))
        .into()),
    }
}
