//! The engine crates' one Postgres layer: what a server's error means to
//! the caller ([`classify`]), and how a connection string's `sslmode` is
//! read and honoured ([`read_ssl_mode`], and with the `tls` feature
//! [`tls`]).
//!
//! The storage (`superschematic-engine-storage`), runtime mode
//! (`superschematic-rules-runtime`), development mode
//! (`superschematic-rules-dev`) and the behaviors' adapter
//! (`superschematic-rules-adapter`) each map an error to their own type
//! from the class here, so a SQLSTATE means one thing in every crate. The
//! HTTP runtime (`runtime/http/rust/src/postgres.rs`), a published crate
//! that cannot depend on this one, reads `sslmode` the same way.

mod classify;
mod sslmode;
#[cfg(feature = "tls")]
mod tls;

pub use classify::{class_of, classify, Class};
pub use sslmode::{read_ssl_mode, Verify};
#[cfg(feature = "tls")]
pub use tls::{tls, tls_for, Tls, TlsError};
