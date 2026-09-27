//! The C ABI, shared by the Go binding (cgo over the static archive) and the
//! browser (the same exports built for `wasm32-unknown-unknown`).
//!
//! Each operation is `vg_<op>(in_ptr, in_len, out_ptr, out_len) -> i32`: the
//! input is `in_len` bytes of JSON at `in_ptr`, and the core writes a pointer
//! to its output document and the document's length through `out_ptr` and
//! `out_len`. It returns [`VG_OK`] with the operation's output, [`VG_ERROR`]
//! with `{"error":{"code","message"}}`, or [`VG_BAD_ARGUMENTS`] when an out
//! pointer is null, in which case nothing is written. The caller releases the
//! output with `vg_free(ptr, len)`. A wasm host allocates the input with
//! `vg_alloc(len)` and releases it with `vg_dealloc(ptr, len)`.

use std::panic::{catch_unwind, AssertUnwindSafe};

use crate::{error_document, run_json, Error, Op};

/// The output is the operation's result.
pub const VG_OK: i32 = 0;
/// The output is an error document.
pub const VG_ERROR: i32 = 1;
/// An out pointer was null, or the input pointer was null with a non-zero
/// length; nothing was written.
pub const VG_BAD_ARGUMENTS: i32 = 2;

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
        return VG_BAD_ARGUMENTS;
    }
    let input: &[u8] = if in_len == 0 {
        &[]
    } else {
        std::slice::from_raw_parts(in_ptr, in_len)
    };
    let (document, ok) =
        catch_unwind(AssertUnwindSafe(|| run_json(op, input))).unwrap_or_else(|_| {
            let error = Error::new("internal", format!("{} panicked", op.name()));
            (error_document(&error), false)
        });
    let (ptr, len) = into_raw(document.into_bytes());
    // A wasm host places the out slots in bytes from vg_alloc, which carry
    // no alignment.
    out_ptr.write_unaligned(ptr);
    out_len.write_unaligned(len);
    if ok {
        VG_OK
    } else {
        VG_ERROR
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

/// A zeroed buffer of `len` bytes, released with [`free_raw`]. The wasm
/// exports wrap it so a host can place its input in linear memory.
pub fn alloc(len: usize) -> *mut u8 {
    into_raw(vec![0; len]).0
}

/// Release a buffer from [`alloc`] or an output document.
///
/// # Safety
/// `ptr` and `len` must come from [`alloc`] or from an operation's output,
/// and must not have been released.
pub unsafe fn dealloc(ptr: *mut u8, len: usize) {
    free_raw(ptr, len)
}

/// `compose`: `{"descriptor", "base", "overlay"}`.
///
/// # Safety
/// See the module documentation: the input must be readable for `in_len`
/// bytes, and the out pointers valid for writes.
#[no_mangle]
pub unsafe extern "C" fn vg_compose(
    in_ptr: *const u8,
    in_len: usize,
    out_ptr: *mut *mut u8,
    out_len: *mut usize,
) -> i32 {
    call(Op::Compose, in_ptr, in_len, out_ptr, out_len)
}

/// `merge`: `{"descriptor", "base", "ours", "theirs", "resolutions"?}`.
///
/// # Safety
/// As [`vg_compose`].
#[no_mangle]
pub unsafe extern "C" fn vg_merge(
    in_ptr: *const u8,
    in_len: usize,
    out_ptr: *mut *mut u8,
    out_len: *mut usize,
) -> i32 {
    call(Op::Merge, in_ptr, in_len, out_ptr, out_len)
}

/// `diff`: `{"descriptor", "from", "to"}`.
///
/// # Safety
/// As [`vg_compose`].
#[no_mangle]
pub unsafe extern "C" fn vg_diff(
    in_ptr: *const u8,
    in_len: usize,
    out_ptr: *mut *mut u8,
    out_len: *mut usize,
) -> i32 {
    call(Op::Diff, in_ptr, in_len, out_ptr, out_len)
}

/// `content_hash`: `{"descriptor", "tree"}`.
///
/// # Safety
/// As [`vg_compose`].
#[no_mangle]
pub unsafe extern "C" fn vg_content_hash(
    in_ptr: *const u8,
    in_len: usize,
    out_ptr: *mut *mut u8,
    out_len: *mut usize,
) -> i32 {
    call(Op::ContentHash, in_ptr, in_len, out_ptr, out_len)
}

/// `validate`: `{"descriptor", "tree"}`.
///
/// # Safety
/// As [`vg_compose`].
#[no_mangle]
pub unsafe extern "C" fn vg_validate(
    in_ptr: *const u8,
    in_len: usize,
    out_ptr: *mut *mut u8,
    out_len: *mut usize,
) -> i32 {
    call(Op::Validate, in_ptr, in_len, out_ptr, out_len)
}

/// Release an output document.
///
/// # Safety
/// `ptr` and `len` must be an output of one `vg_<op>` call, not yet released.
#[no_mangle]
pub unsafe extern "C" fn vg_free(ptr: *mut u8, len: usize) {
    free_raw(ptr, len)
}

/// Allocate `len` bytes of linear memory for an input.
#[cfg(target_arch = "wasm32")]
#[no_mangle]
pub extern "C" fn vg_alloc(len: usize) -> *mut u8 {
    alloc(len)
}

/// Release memory from `vg_alloc`.
///
/// # Safety
/// `ptr` and `len` must come from one `vg_alloc` call, not yet released.
#[cfg(target_arch = "wasm32")]
#[no_mangle]
pub unsafe extern "C" fn vg_dealloc(ptr: *mut u8, len: usize) {
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
            vg_free(out, out_len);
            (code, text)
        }
    }

    const DESCRIPTOR: &str = r#"{"kinds":[{"kind":"step","key":"entity_key","id":"id","ref":"ref","tombstone":"deleted_on_ref","version":"_version"}]}"#;

    #[test]
    fn every_export_returns_its_document() {
        let tree = r#"{"step":[{"id":"r1","entity_key":"k1","ref":"a","deleted_on_ref":false,"_version":1,"title":"Boil"}]}"#;
        let cases: [(Export, String); 5] = [
            (
                vg_compose,
                format!(r#"{{"descriptor":{DESCRIPTOR},"base":{tree},"overlay":{{}}}}"#),
            ),
            (
                vg_merge,
                format!(
                    r#"{{"descriptor":{DESCRIPTOR},"base":{tree},"ours":{tree},"theirs":{tree}}}"#
                ),
            ),
            (
                vg_diff,
                format!(r#"{{"descriptor":{DESCRIPTOR},"from":{tree},"to":{{}}}}"#),
            ),
            (
                vg_content_hash,
                format!(r#"{{"descriptor":{DESCRIPTOR},"tree":{tree}}}"#),
            ),
            (
                vg_validate,
                format!(r#"{{"descriptor":{DESCRIPTOR},"tree":{tree}}}"#),
            ),
        ];
        for (export, input) in cases {
            let (code, text) = invoke(export, input.as_bytes());
            assert_eq!(code, VG_OK, "{text}");
            assert!(!text.contains("\"error\""), "{text}");
        }
    }

    #[test]
    fn a_refused_input_returns_an_error_document() {
        let (code, text) = invoke(vg_compose, b"{not json");
        assert_eq!(code, VG_ERROR);
        let document: serde_json::Value =
            serde_json::from_str(&text).expect("error document is JSON");
        assert_eq!(document["error"]["code"], "invalid_json");
    }

    #[test]
    fn an_empty_input_is_an_error_not_a_crash() {
        let mut out: *mut u8 = std::ptr::null_mut();
        let mut out_len = 0usize;
        let code = unsafe { vg_validate(std::ptr::null(), 0, &mut out, &mut out_len) };
        assert_eq!(code, VG_ERROR);
        unsafe { vg_free(out, out_len) };
    }

    #[test]
    fn null_out_pointers_write_nothing() {
        let input = b"{}";
        let mut out_len = 0usize;
        let code = unsafe {
            vg_diff(
                input.as_ptr(),
                input.len(),
                std::ptr::null_mut(),
                &mut out_len,
            )
        };
        assert_eq!(code, VG_BAD_ARGUMENTS);
        assert_eq!(out_len, 0);
        let mut out: *mut u8 = std::ptr::null_mut();
        let code = unsafe { vg_diff(std::ptr::null(), 4, &mut out, &mut out_len) };
        assert_eq!(code, VG_BAD_ARGUMENTS);
        assert!(out.is_null());
    }
}
