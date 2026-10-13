//! The engine (D16) in Rust, which every server links through a binding
//! (D58): it takes schemas as data, keeps their instances and an event log
//! in Postgres or SQLite, runs the behaviors composed on their types, and
//! serves its routes, event stream and MCP endpoint.
//!
//! Nothing is built yet. Until this crate passes the TypeScript engine's
//! suite through its Node-API addon, `@superschematic/engine` in
//! `runtime/engine/typescript` is the engine.
