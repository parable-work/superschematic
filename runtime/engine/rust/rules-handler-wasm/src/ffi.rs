//! The C ABI (D117), which `@superschematic/rules-handler` loads from the
//! `wasm32-unknown-unknown` build, as `@superschematic/rules` loads the
//! core's (`runtime/engine/rust/rules-wasm/src/ffi.rs`, whose convention
//! this one keeps). Every value is JSON; the module imports nothing.
//!
//! Each operation is `handler_<op>(in_ptr, in_len, out_ptr, out_len) ->
//! i32`: the input is `in_len` bytes of JSON at `in_ptr`, and the handler
//! writes a pointer to its output document and the document's length
//! through `out_ptr` and `out_len`. It returns [`HANDLER_OK`] with the
//! operation's output, [`HANDLER_ERROR`] with `{"error":{"code","message"}}`,
//! or [`HANDLER_BAD_ARGUMENTS`] when an out pointer is null, in which case
//! nothing is written. The caller releases the output with
//! `handler_free(ptr, len)`. A wasm host allocates the input with
//! `handler_alloc(len)` and releases it with `handler_dealloc(ptr, len)`; it
//! places the two out slots in memory from `handler_alloc` too, which
//! carries no alignment. `handler_alloc` and `handler_dealloc` exist in the
//! wasm32 build alone: a native host passes its own memory for the input
//! and the out slots, and frees each output with `handler_free`.
//!
//! The operations ([`crate`] says each one's documents): `handler_run`,
//! `handler_route`, `handler_respond`, `handler_events`, `handler_tool`,
//! `handler_bridge`, `handler_core` and `handler_layout`.

use std::panic::{catch_unwind, AssertUnwindSafe};

use crate::{error_document, run_json, Op};

/// The output is the operation's result.
pub const HANDLER_OK: i32 = 0;
/// The output is an error document.
pub const HANDLER_ERROR: i32 = 1;
/// An out pointer was null, or the input pointer was null with a non-zero
/// length; nothing was written.
pub const HANDLER_BAD_ARGUMENTS: i32 = 2;

/// # Safety
/// `in_ptr` must point to `in_len` readable bytes (it may be null when
/// `in_len` is 0), and `out_ptr` and `out_len` must be valid for writes.
unsafe fn call(
    op: Op,
    in_ptr: *const u8,
    in_len: usize,
    out_ptr: *mut *mut u8,
    out_len: *mut usize,
) -> i32 {
    if out_ptr.is_null() || out_len.is_null() || (in_ptr.is_null() && in_len != 0) {
        return HANDLER_BAD_ARGUMENTS;
    }
    let input: &[u8] = if in_len == 0 {
        &[]
    } else {
        std::slice::from_raw_parts(in_ptr, in_len)
    };
    // A panic is a defect, held here as an internal error document on a
    // native target. On wasm32-unknown-unknown it aborts the module
    // instead, which the host reports as the instance's trap.
    let (document, ok) =
        catch_unwind(AssertUnwindSafe(|| run_json(op, input))).unwrap_or_else(|_| {
            (
                error_document("internal", &format!("{} panicked", op.name())),
                false,
            )
        });
    let (ptr, len) = into_raw(document.into_bytes());
    out_ptr.write_unaligned(ptr);
    out_len.write_unaligned(len);
    if ok {
        HANDLER_OK
    } else {
        HANDLER_ERROR
    }
}

fn into_raw(bytes: Vec<u8>) -> (*mut u8, usize) {
    let bytes = bytes.into_boxed_slice();
    let len = bytes.len();
    (Box::into_raw(bytes).cast::<u8>(), len)
}

/// # Safety
/// `ptr` and `len` must come from one [`into_raw`] call, not yet released.
unsafe fn free_raw(ptr: *mut u8, len: usize) {
    if !ptr.is_null() {
        drop(Box::from_raw(std::ptr::slice_from_raw_parts_mut(ptr, len)));
    }
}

/// A zeroed buffer of `len` bytes, released with [`dealloc`].
pub fn alloc(len: usize) -> *mut u8 {
    into_raw(vec![0; len]).0
}

/// Releases a buffer from [`alloc`] or an output document.
///
/// # Safety
/// `ptr` and `len` must come from [`alloc`] or from an operation's output,
/// and must not have been released.
pub unsafe fn dealloc(ptr: *mut u8, len: usize) {
    free_raw(ptr, len)
}

macro_rules! export {
    ($(#[$doc:meta])* $name:ident, $op:expr) => {
        $(#[$doc])*
        ///
        /// # Safety
        /// The input must be readable for `in_len` bytes, and the out
        /// pointers valid for writes.
        #[no_mangle]
        pub unsafe extern "C" fn $name(
            in_ptr: *const u8,
            in_len: usize,
            out_ptr: *mut *mut u8,
            out_len: *mut usize,
        ) -> i32 {
            call($op, in_ptr, in_len, out_ptr, out_len)
        }
    };
}

export!(
    /// `run`: a request, its context and the reads answered so far, to the
    /// reads it asks or its answer and unit.
    handler_run,
    Op::Run
);
export!(
    /// `route`: an HTTP request to what it routes to.
    handler_route,
    Op::Route
);
export!(
    /// `respond`: a routed request's answer to its HTTP response.
    handler_respond,
    Op::Respond
);
export!(
    /// `events`: a read of the event queue, admitted, as a page, or as the
    /// frames a stream sends.
    handler_events,
    Op::Events
);
export!(
    /// `tool`: a tool's call and arguments to its request.
    handler_tool,
    Op::Tool
);
export!(
    /// `bridge`: one command of the core over the layout's rows.
    handler_bridge,
    Op::Bridge
);
export!(
    /// `core`: one command of the core alone, to its step.
    handler_core,
    Op::Core
);
export!(
    /// `layout`: the behaviors' tables and the host loop's bounds.
    handler_layout,
    Op::Layout
);

/// Releases an output document.
///
/// # Safety
/// `ptr` and `len` must be an output of one `handler_<op>` call, not yet
/// released.
#[no_mangle]
pub unsafe extern "C" fn handler_free(ptr: *mut u8, len: usize) {
    free_raw(ptr, len)
}

/// Allocates `len` bytes of linear memory for an input or the out slots.
#[cfg(target_arch = "wasm32")]
#[no_mangle]
pub extern "C" fn handler_alloc(len: usize) -> *mut u8 {
    alloc(len)
}

/// Releases memory from `handler_alloc`.
///
/// # Safety
/// `ptr` and `len` must come from one `handler_alloc` call, not yet
/// released.
#[cfg(target_arch = "wasm32")]
#[no_mangle]
pub unsafe extern "C" fn handler_dealloc(ptr: *mut u8, len: usize) {
    free_raw(ptr, len)
}

#[cfg(test)]
mod tests {
    use super::*;

    type Export = unsafe extern "C" fn(*const u8, usize, *mut *mut u8, *mut usize) -> i32;

    fn invoke(export: Export, input: &[u8]) -> (i32, String) {
        let buffer = alloc(input.len());
        let mut out: *mut u8 = std::ptr::null_mut();
        let mut out_len = 0usize;
        unsafe {
            std::ptr::copy_nonoverlapping(input.as_ptr(), buffer, input.len());
            let code = export(buffer, input.len(), &mut out, &mut out_len);
            dealloc(buffer, input.len());
            let text = std::str::from_utf8(std::slice::from_raw_parts(out, out_len))
                .expect("output is UTF-8")
                .to_owned();
            handler_free(out, out_len);
            (code, text)
        }
    }

    #[test]
    fn every_export_returns_its_document() {
        let (code, text) = invoke(handler_layout, b"");
        assert_eq!(code, HANDLER_OK, "{text}");
        assert!(text.contains("\"behaviors\""), "{text}");
        let (code, text) = invoke(
            handler_run,
            br#"{"request": {"kind": "types"}, "context": {"caller": {"subject": "a"}, "now": 1, "id": "00000000-0000-4000-8000-000000000001"}}"#,
        );
        assert_eq!(
            (code, text.as_str()),
            (HANDLER_OK, r#"{"read":[{"kind":"live"}]}"#)
        );
        let (code, text) = invoke(
            handler_route,
            br#"{"request": {"method": "GET", "path": "/namespaces/default/schemas"}}"#,
        );
        assert_eq!(code, HANDLER_OK, "{text}");
        assert!(text.contains(r#""kind":"types""#), "{text}");
        for export in [
            handler_respond,
            handler_events,
            handler_tool,
            handler_bridge,
            handler_core,
        ] {
            let (code, text) = invoke(export, b"{not json");
            assert_eq!(code, HANDLER_ERROR, "{text}");
            assert!(text.contains("invalid_json"), "{text}");
        }
    }

    #[test]
    fn null_out_pointers_write_nothing() {
        let input = b"{}";
        let mut out_len = 0usize;
        let code = unsafe {
            handler_run(
                input.as_ptr(),
                input.len(),
                std::ptr::null_mut(),
                &mut out_len,
            )
        };
        assert_eq!(code, HANDLER_BAD_ARGUMENTS);
        assert_eq!(out_len, 0);
    }
}
