//! The C ABI (D103), which two hosts load: `@superschematic/rules` from the
//! `wasm32-unknown-unknown` build, as runtime/versiongraph's `vg_*` is
//! loaded, and the Go binding (`runtime/rules/go`) from the native build's
//! static archive, whose header is `runtime/rules/go/include/superschematic_rules.h`.
//! Every value is JSON; the module imports nothing.
//!
//! Each operation is `rules_<op>(in_ptr, in_len, out_ptr, out_len) -> i32`:
//! the input is `in_len` bytes of JSON at `in_ptr`, and the core writes a
//! pointer to its output document and the document's length through
//! `out_ptr` and `out_len`. It returns [`RULES_OK`] with the operation's
//! output, [`RULES_ERROR`] with `{"error":{"code","message"}}`, or
//! [`RULES_BAD_ARGUMENTS`] when an out pointer is null, in which case
//! nothing is written. The caller releases the output with
//! `rules_free(ptr, len)`. A wasm host allocates the input with
//! `rules_alloc(len)` and releases it with `rules_dealloc(ptr, len)`; it
//! places the two out slots in memory from `rules_alloc` too, which carries
//! no alignment. `rules_alloc` and `rules_dealloc` exist in the wasm32
//! build alone: a native host passes its own memory for the input and the
//! out slots, and frees each output with `rules_free`. A native host may
//! call from any number of threads at once, all of which run one core.
//!
//! The operations ([`crate`] says each one's documents): `rules_run`,
//! `rules_declarations` and `rules_check`.

use std::panic::{catch_unwind, AssertUnwindSafe};

use crate::{error_document, run_json, Op};

/// The output is the operation's result.
pub const RULES_OK: i32 = 0;
/// The output is an error document.
pub const RULES_ERROR: i32 = 1;
/// An out pointer was null, or the input pointer was null with a non-zero
/// length; nothing was written.
pub const RULES_BAD_ARGUMENTS: i32 = 2;

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
        return RULES_BAD_ARGUMENTS;
    }
    let input: &[u8] = if in_len == 0 {
        &[]
    } else {
        std::slice::from_raw_parts(in_ptr, in_len)
    };
    // A panic is a defect, held here as an internal error document on a
    // native target (the release profile unwinds; a panic while the core is
    // first built poisons it, and every later call answers internal too).
    // On wasm32-unknown-unknown it aborts the module instead, which the
    // host reports as the instance's trap.
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
        RULES_OK
    } else {
        RULES_ERROR
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

/// `run`: `{"spec", "command", "answers"?}` to the core's step.
///
/// # Safety
/// The input must be readable for `in_len` bytes, and the out pointers
/// valid for writes.
#[no_mangle]
pub unsafe extern "C" fn rules_run(
    in_ptr: *const u8,
    in_len: usize,
    out_ptr: *mut *mut u8,
    out_len: *mut usize,
) -> i32 {
    call(Op::Run, in_ptr, in_len, out_ptr, out_len)
}

/// `declarations`: any input to the registered behaviors' declaration
/// files.
///
/// # Safety
/// As [`rules_run`].
#[no_mangle]
pub unsafe extern "C" fn rules_declarations(
    in_ptr: *const u8,
    in_len: usize,
    out_ptr: *mut *mut u8,
    out_len: *mut usize,
) -> i32 {
    call(Op::Declarations, in_ptr, in_len, out_ptr, out_len)
}

/// `check`: `{"specs", "caller"?, "now"?}` to each spec's define check.
///
/// # Safety
/// As [`rules_run`].
#[no_mangle]
pub unsafe extern "C" fn rules_check(
    in_ptr: *const u8,
    in_len: usize,
    out_ptr: *mut *mut u8,
    out_len: *mut usize,
) -> i32 {
    call(Op::Check, in_ptr, in_len, out_ptr, out_len)
}

/// Releases an output document.
///
/// # Safety
/// `ptr` and `len` must be an output of one `rules_<op>` call, not yet
/// released.
#[no_mangle]
pub unsafe extern "C" fn rules_free(ptr: *mut u8, len: usize) {
    free_raw(ptr, len)
}

/// Allocates `len` bytes of linear memory for an input or the out slots.
#[cfg(target_arch = "wasm32")]
#[no_mangle]
pub extern "C" fn rules_alloc(len: usize) -> *mut u8 {
    alloc(len)
}

/// Releases memory from `rules_alloc`.
///
/// # Safety
/// `ptr` and `len` must come from one `rules_alloc` call, not yet released.
#[cfg(target_arch = "wasm32")]
#[no_mangle]
pub unsafe extern "C" fn rules_dealloc(ptr: *mut u8, len: usize) {
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
            rules_free(out, out_len);
            (code, text)
        }
    }

    #[test]
    fn every_export_returns_its_document() {
        let (code, text) = invoke(rules_declarations, b"");
        assert_eq!(code, RULES_OK, "{text}");
        assert!(text.starts_with('['), "{text}");
        let (code, text) = invoke(rules_check, br#"{"specs": [{"name": "Note"}]}"#);
        assert_eq!((code, text.as_str()), (RULES_OK, "[null]"));
        let (code, text) = invoke(
            rules_run,
            br#"{"spec": {"name": "Note"}, "command": {"caller": {"subject": "a"}, "now": 1, "action": {"kind": "delete", "key": "n1"}}}"#,
        );
        assert_eq!(code, RULES_OK, "{text}");
        assert!(text.contains(r#""kind":"row""#), "{text}");
    }

    #[test]
    fn a_refused_input_returns_an_error_document() {
        let (code, text) = invoke(rules_run, b"{not json");
        assert_eq!(code, RULES_ERROR);
        assert!(text.contains("invalid_json"), "{text}");
    }

    #[test]
    fn a_native_host_runs_one_core_from_several_threads_at_once() {
        let input = br#"{"spec": {"name": "Order", "uses": [{"behavior": "Workflow", "config": {"states": ["draft", "paid"], "transitions": [{"from": "draft", "to": "paid"}]}, "anchor": {"field": "status"}}]}, "command": {"caller": {"subject": "sam"}, "now": 1000, "request": "r1", "action": {"kind": "operation", "key": "o1", "useId": "status", "operation": "transition", "params": {"to": "paid"}}}}"#;
        let barrier = std::sync::Barrier::new(16);
        let answers: Vec<(i32, String)> = std::thread::scope(|scope| {
            let threads: Vec<_> = (0..16)
                .map(|_| {
                    scope.spawn(|| {
                        barrier.wait();
                        (0..25)
                            .map(|_| invoke(rules_run, input))
                            .collect::<Vec<_>>()
                    })
                })
                .collect();
            threads
                .into_iter()
                .flat_map(|thread| thread.join().expect("a thread panicked"))
                .collect()
        });
        assert_eq!(answers.len(), 16 * 25);
        for (code, text) in &answers {
            assert_eq!(*code, RULES_OK, "{text}");
            assert_eq!(
                text,
                r#"{"read":[{"kind":"row","type":"Order","key":"o1"}]}"#
            );
        }
    }

    #[test]
    fn null_out_pointers_write_nothing() {
        let input = b"{}";
        let mut out_len = 0usize;
        let code = unsafe {
            rules_run(
                input.as_ptr(),
                input.len(),
                std::ptr::null_mut(),
                &mut out_len,
            )
        };
        assert_eq!(code, RULES_BAD_ARGUMENTS);
        assert_eq!(out_len, 0);
    }
}
