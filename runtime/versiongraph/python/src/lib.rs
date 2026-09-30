//! The Python binding of the version-graph core (`runtime/versiongraph/rust`),
//! built by maturin as the extension module `superschematic_versiongraph._native`.
//!
//! The module holds one function, `run(operation, input)`: an operation's
//! contract name (`compose`, `merge`, `diff`, `content_hash`, `validate`) and
//! its JSON input as bytes in, `(ok, output)` out. `output` is the
//! operation's output document when `ok` is true, and the core's
//! `{"error":{"code","message"}}` document when it is false, as the C ABI
//! returns them. The Python package raises `VersionGraphError` from the
//! error document and types the operations. The core runs with the GIL
//! released, and a panic in it becomes the `internal` error document, as it
//! does through the C ABI.

use std::panic::{catch_unwind, AssertUnwindSafe};

use pyo3::exceptions::PyValueError;
use pyo3::prelude::*;
use superschematic_versiongraph::{run_json, Op};

/// Runs one operation of the core on a JSON document and returns
/// `(ok, output)`. An unknown operation name raises `ValueError`.
#[pyfunction]
fn run(py: Python<'_>, operation: &str, input: &[u8]) -> PyResult<(bool, String)> {
    let op = Op::from_name(operation)
        .ok_or_else(|| PyValueError::new_err(format!("unknown operation {operation:?}")))?;
    Ok(py.detach(|| call(op, input)))
}

fn call(op: Op, input: &[u8]) -> (bool, String) {
    match catch_unwind(AssertUnwindSafe(|| run_json(op, input))) {
        Ok((output, ok)) => (ok, output),
        Err(_) => (false, panic_document(op)),
    }
}

/// The error document for a panic in an operation, the one the C ABI
/// returns: the `internal` code and `<operation> panicked`. The operation
/// names are plain ASCII, so the message needs no escaping.
fn panic_document(op: Op) -> String {
    format!(
        r#"{{"error":{{"code":"internal","message":"{} panicked"}}}}"#,
        op.name()
    )
}

#[pymodule]
fn _native(m: &Bound<'_, PyModule>) -> PyResult<()> {
    m.add_function(wrap_pyfunction!(run, m)?)?;
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    /// A panic's document is the C ABI's: the core's error document with
    /// the internal code, naming the operation.
    #[test]
    fn a_panic_is_the_internal_error_document() {
        assert_eq!(
            panic_document(Op::ContentHash),
            r#"{"error":{"code":"internal","message":"content_hash panicked"}}"#
        );
    }
}
