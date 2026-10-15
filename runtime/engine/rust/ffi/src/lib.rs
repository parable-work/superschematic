//! The core's engine archive (D58): the engine's C ABI
//! (`superschematic-engine-abi`) over the core's behaviors
//! ([`superschematic_engine_behaviors::core`]), which the release ships as
//! `libsuperschematic_engine.a` and the Go binding (`runtime/engine/go`)
//! links. `scripts/engine-archive.sh` builds it. A project whose schemas
//! compose an extension's behaviors builds an archive of its own from a
//! crate like this one over its behaviors, and links it in this one's
//! place; it never depends on this crate, whose exports its own would
//! define again.

pub use superschematic_engine_abi::{
    abi, Project, ABI_VERSION, ENG_BAD_ARGUMENTS, ENG_CLOSED, ENG_ERROR, ENG_OK,
};

superschematic_engine_abi::export_engine_c_abi!(behaviors = superschematic_engine_behaviors::core);
