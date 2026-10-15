//! The engine (D16) in Rust, which every server links through a binding
//! (D58): it takes schemas as data, keeps their instances and an event log
//! in Postgres or SQLite, runs the behaviors composed on their types, and
//! serves its routes, event stream and MCP endpoint.
//!
//! It is built in milestones, each a port of part of the TypeScript engine
//! (`runtime/engine/typescript`), with that engine's messages, codes and
//! hashes. Built so far: opening an engine on its storage
//! ([`Engine::open`]) and running its migrations, the namespaces
//! ([`Namespaces`]), the schema registry ([`Schemas`]) with its validator
//! and compatibility rule, the access policy as data ([`PolicyConfig`]),
//! the instances ([`Instances`]) with their filters, unique indexes and
//! the value store that keeps a large member once, the event log
//! ([`Events`]), the behavior plug-in API ([`behaviors`]: a behavior is a
//! Rust type that implements [`Behavior`], composed on a schema's instance
//! type and run with every call on its instances), the refusals and their
//! codes ([`errors`]), the principal a call acts for ([`Principal`]), and
//! JSON written as JavaScript writes it ([`json`]). Until this crate passes
//! the TypeScript engine's suite through its Node-API addon,
//! `@superschematic/engine` is the engine.
//!
//! ```no_run
//! use serde_json::json;
//! use superschematic_engine::{DefineOptions, Engine, EngineOptions, Policy, Principal, SchemaTarget};
//!
//! let mut options = EngineOptions::sqlite("engine.db");
//! options.policy = Some(Policy::from_json(&json!({"rules": [
//!     {"action": ["define", "publish"], "require": "schemas.{action}"},
//!     {"require": "{schema}.{action}"}
//! ]}))?);
//! let engine = Engine::open(options)?;
//! let alice = Principal::new("alice", ["schemas", "Note"]);
//! let document = json!({
//!     "kind": "General",
//!     "name": "Note",
//!     "types": {"Note": {"name": "Note", "role": "EmbeddedStruct", "fields": [
//!         {"name": "body", "typeRef": {"name": "string"}, "required": true}
//!     ]}}
//! });
//! engine.schemas().define(&alice, &document, DefineOptions::default())?;
//! assert_eq!(engine.schemas().publish(&alice, "Note", SchemaTarget::default())?.version, 1);
//! # Ok::<(), superschematic_engine::Error>(())
//! ```

mod access;
pub mod api;
pub mod behaviors;
pub mod calls;
mod engine;
pub mod errors;
mod events;
mod indexes;
pub mod instances;
pub mod json;
mod namespaces;
mod options;
mod paging;
mod principal;
mod registry;
mod retention;
mod runner;
pub mod search;
pub mod tools;
mod values;

pub use access::{
    covers, has_any_permission, AccessPolicy, AccessRequest, Decision, DottedPermissions,
    ListingAction, NamespaceOperation, PermissionMatcher, PermissionTemplate, Policy, PolicyAction,
    PolicyConfig, PolicyRule, PolicyShorthand, RulePolicy, SchemaAction, ServiceMatch,
};
pub use behaviors::{
    json_config, Behavior, BehaviorSet, Behaviors, ConfigTarget, GuardRequest, Hooks,
    InstanceChange, InstanceCx, InstanceView, OperationCx, SchemaCx, ValidationRequest,
};
pub use engine::{
    seeded_uuid, Clock, Engine, EngineClock, EngineOptions, IdCounter, IdQueue, Ids, MetaSchema,
    Storage, UuidSource, SCENARIO_SEED,
};
pub use errors::{
    BehaviorError, EngineError, EngineErrorCode, EngineErrorKind, Error, Result, SchemaChange,
    SchemaIssue, Thrown, ValidationIssue, Veto,
};
pub use events::{
    cursor_refusal, EngineEvent, EventCause, EventPage, Events, EventsAfter, ReadEvents,
    EVENT_KINDS,
};
pub use instances::{
    is_instance_id, CreateOptions, GetOptions, InstancePage, InstanceRecord, Instances,
    ListOptions, OperationOutcome, WriteOptions, FILTER_SCAN_ROWS, INSTANCE_ID, MAX_FILTER_RANGES,
    MAX_FILTER_VALUES,
};
pub use namespaces::{
    is_namespace_name, name_refusal, NamespaceConfig, NamespaceOptions, NamespaceOrigin,
    NamespaceRecord, NamespaceState, Namespaces, DEFAULT_NAMESPACE, NAMESPACE_NAME,
};
pub use options::OPTION_MEMBERS;
pub use paging::{DEFAULT_PAGE_SIZE, MAX_PAGE_SIZE};
pub use principal::{
    actor_of, check_principal, Actor, Principal, PrincipalService, SERVICE_SUBJECT_PREFIX,
    SYSTEM_SUBJECT,
};
pub use registry::{
    is_schema_name, ComposedBehavior, DefineOptions, NormalizeMode, Normalized, PublishResult,
    SchemaCapabilities, SchemaInput, SchemaModel, SchemaRecord, SchemaSummary, SchemaTarget,
    SchemaValidator, Schemas, ValidateOptions, SCHEMA_NAME,
};
pub use retention::{
    RetentionOptions, DEFAULT_RETENTION_BATCH_SIZE, DEFAULT_RETENTION_EVERY_MS,
    MAX_RETENTION_BATCH_SIZE,
};
pub use runner::{
    describe as describe_error, PruneResult, RetentionNamespaceStatus, RetentionStatus, Runner,
    RunnerOptions, RunnerPass, RunnerStatus, ScheduleState, ScheduleStatus, SubscriptionFailure,
    SubscriptionKey, SubscriptionSkip, SubscriptionState, SubscriptionStatus, DEFAULT_BATCH_SIZE,
    DEFAULT_LEASE_RETRY_MS, DEFAULT_MAX_ATTEMPTS, DEFAULT_MAX_DEPTH, DEFAULT_RETRY_INITIAL_MS,
    DEFAULT_RETRY_MAX_MS,
};
/// The storage the engine runs on, for a host that opens it itself.
pub use superschematic_engine_storage as storage;
pub use tools::{
    InvocationPolicy, ToolFilter, ToolKeys, ToolOptions, ToolSummary, ToolTarget, Tools,
};
pub use values::{
    is_hash, DriverError, StoredValue, SweepResult, ValueDriver, ValueOptions, Values,
    DEFAULT_VALUE_CACHE_BYTES, DEFAULT_VALUE_MAX_BYTES, DEFAULT_VALUE_THRESHOLD,
    MIN_VALUE_THRESHOLD, VALUE_HASH, VALUE_REF_KEY,
};
