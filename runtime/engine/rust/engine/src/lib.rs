//! The engine (D16) in Rust, which every server links through a binding
//! (D58): it takes schemas as data, keeps their instances and an event log
//! in Postgres or SQLite, runs the behaviors composed on their types, and
//! serves its routes, event stream and MCP endpoint.
//!
//! It is built in milestones, each a port of part of the TypeScript engine
//! (`runtime/engine/typescript`), with that engine's messages, codes and
//! hashes. Built so far: the refusals and their codes ([`errors`]), the
//! principal a call acts for ([`Principal`]) and the questions the access
//! policy answers, and JSON written as JavaScript writes it ([`json`]).
//! Until this crate passes the TypeScript engine's suite through its
//! Node-API addon, `@superschematic/engine` is the engine.

#![allow(dead_code)] // the engine that asks these questions comes next

mod access;
pub mod errors;
pub mod json;
mod principal;

pub use access::{AccessRequest, NamespaceOperation, SchemaAction};
pub use errors::{
    BehaviorError, EngineError, EngineErrorCode, EngineErrorKind, Error, Result, SchemaChange,
    SchemaIssue, ValidationIssue, Veto,
};
pub use principal::{
    actor_of, check_principal, Actor, Principal, PrincipalService, SERVICE_SUBJECT_PREFIX,
    SYSTEM_SUBJECT,
};
