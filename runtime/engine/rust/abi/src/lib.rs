//! The engine's C ABI (D58) as a library: what a Go server links, as the
//! static archive `libsuperschematic_engine.a`, through the binding in
//! `runtime/engine/go`. It follows the version graph's conventions
//! (`runtime/versiongraph/rust/src/ffi.rs`): JSON in and out, output the
//! engine allocates and `eng_free` releases, and a panic caught at every
//! export. `runtime/engine/go/include/superschematic_engine.h` is the
//! header, which the core's archive's tests hold to the exports.
//!
//! | Export | Input | Output |
//! |---|---|---|
//! | `eng_abi_version()` | | [`ABI_VERSION`] |
//! | `eng_open(config, *engine)` | the config ([`config`]) | `{abi, project, dialect, behaviors}`, and the handle |
//! | `eng_close(engine)` | | |
//! | `eng_call(engine, request)` | `{op, principal?, args?, now?}` | the call's result, empty for none |
//! | `eng_http(engine, head, body)` | `{method, prefix, path, query, headers, caller, requestId, options}` and the body's bytes | `{status, headers, stream}` and the body's bytes |
//! | `eng_stream_poll(engine, stream)` | | the bytes to send, empty for none yet; `ENG_CLOSED` once over |
//! | `eng_stream_close(engine, stream)` | | |
//! | `eng_wait(engine, after, timeout_ms, *epoch)` | | the notifier's wakes: its commits and the signals its streams kept (D77) |
//! | `eng_runner(engine, request)` | `{action: start, stop, run_due or status}` | the pass or the status, empty for start and stop |
//! | `eng_free(ptr, len)` | an output | |
//!
//! Each returns [`ENG_OK`]; [`ENG_ERROR`] with `{"error": {"class", "code",
//! "message", "details"}}`, an error as the TypeScript engine throws it
//! ([`Failure`]); [`ENG_BAD_ARGUMENTS`] when an out pointer is null, or an
//! input pointer null with a non-zero length, writing nothing; or
//! [`ENG_CLOSED`] for an engine that is closed or was never opened, and for
//! a stream that is over, writing nothing. An empty output is a null pointer
//! and a length of 0.
//!
//! `eng_call`'s `op` is a call of [`superschematic_engine::calls`] (its
//! principal and arguments as that module reads them, `now` the time a
//! manual clock is set to first) or one of the binding's own:
//!
//! - `deploy` with `[documents, {namespace?}]`: for each schema document in
//!   order, as the engine's own principal ([`Principal::system`]), nothing
//!   when its live version has the same hash, else a define and a publish;
//!   the result lists `{name, namespace, version, defined, published}`.
//!   A server deploys its documents when it starts, before it reports
//!   ready.
//! - `health`: `{ready, closed, poisoned, dialect, listening, error?}`,
//!   where `ready` is false once the engine closed, after a panic inside a
//!   transaction poisoned it, or while its database does not answer a read.
//! - `abi`: what `eng_open` returned.
//!
//! `eng_http` serves one request through
//! [`superschematic_engine::api::handle`]: `headers` are
//! `[name, value]` pairs, `caller` is `{principal, service, refused}` as
//! the host's authentication established it, and `options` are the
//! mount's (`{stream: {pageSize, heartbeatMs}, mcp: {serverInfo: {name,
//! version}, instructions}}`). A response whose body is an event stream
//! carries the stream's handle and no body; `eng_stream_poll` pulls it
//! without waiting, and the host waits for a commit or a signal on
//! `eng_wait`, one thread for every stream of the engine, or for the
//! heartbeat.
//!
//! An archive crate expands [`export_engine_c_abi!`] once, at its root,
//! over its [`Project`]: the behaviors its engine runs, or a configure of
//! its own. `superschematic-engine-ffi` is the core's, over
//! `superschematic_engine_behaviors::core()` (the release's archive); a
//! project whose schemas compose an extension's behaviors builds its own
//! from a crate that depends on this one, as the test kit's
//! (`superschematic-engine-testkit-ffi`) does. The library exports nothing
//! itself, so archive crates in one Cargo workspace, whose features Cargo
//! unifies, never define a symbol twice.

pub mod abi;
pub mod config;
mod failure;
mod macros;

pub use abi::{
    with_behaviors, Configure, Project, ABI_VERSION, ENG_BAD_ARGUMENTS, ENG_CLOSED, ENG_ERROR,
    ENG_OK,
};
pub use failure::Failure;
pub use serde_json::Value as JsonValue;
pub use superschematic_engine::{BehaviorSet, EngineOptions, Principal};
