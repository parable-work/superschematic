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
//! and compatibility rule, the refusals and their codes ([`errors`]), the
//! principal a call acts for ([`Principal`]), and JSON written as
//! JavaScript writes it ([`json`]). Until this crate passes the TypeScript
//! engine's suite through its Node-API addon, `@superschematic/engine` is
//! the engine.
//!
//! ```no_run
//! use serde_json::json;
//! use superschematic_engine::{DefineOptions, Engine, EngineOptions, Principal, SchemaTarget};
//!
//! let engine = Engine::open(EngineOptions::sqlite("engine.db"))?;
//! let alice = Principal::new("alice", ["Note"]);
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
mod composition;
mod engine;
pub mod errors;
mod events;
mod indexes;
pub mod json;
mod namespaces;
mod principal;
mod registry;
mod values;

pub use access::{AccessRequest, NamespaceOperation, SchemaAction};
pub use engine::{Clock, Engine, EngineClock, EngineOptions, Ids, MetaSchema, Storage};
pub use errors::{
    BehaviorError, EngineError, EngineErrorCode, EngineErrorKind, Error, Result, SchemaChange,
    SchemaIssue, ValidationIssue, Veto,
};
pub use namespaces::{
    is_namespace_name, NamespaceConfig, NamespaceOptions, NamespaceOrigin, NamespaceRecord,
    NamespaceState, Namespaces, DEFAULT_NAMESPACE, NAMESPACE_NAME,
};
pub use principal::{
    actor_of, check_principal, Actor, Principal, PrincipalService, SERVICE_SUBJECT_PREFIX,
    SYSTEM_SUBJECT,
};
pub use registry::{
    is_schema_name, ComposedBehavior, DefineOptions, NormalizeMode, Normalized, PublishResult,
    SchemaCapabilities, SchemaInput, SchemaModel, SchemaRecord, SchemaSummary, SchemaTarget,
    SchemaValidator, Schemas, ValidateOptions, SCHEMA_NAME,
};
/// The storage the engine runs on, for a host that opens it itself.
pub use superschematic_engine_storage as storage;
