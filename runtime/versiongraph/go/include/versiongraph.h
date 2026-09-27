/*
 * C ABI of the version-graph core (runtime/versiongraph/rust, src/ffi.rs).
 *
 * Each operation reads in_len bytes of JSON at in_ptr and writes a pointer to
 * its output document, and the document's length, through out_ptr and
 * out_len. It returns VG_OK with the operation's output, VG_ERROR with
 * {"error":{"code","message"}}, or VG_BAD_ARGUMENTS when an out pointer is
 * null (nothing is written). Release every output with vg_free.
 */
#ifndef SUPERSCHEMATIC_VERSIONGRAPH_H
#define SUPERSCHEMATIC_VERSIONGRAPH_H

#include <stddef.h>
#include <stdint.h>

#define VG_OK 0
#define VG_ERROR 1
#define VG_BAD_ARGUMENTS 2

int32_t vg_compose(const uint8_t *in_ptr, size_t in_len, uint8_t **out_ptr, size_t *out_len);
int32_t vg_merge(const uint8_t *in_ptr, size_t in_len, uint8_t **out_ptr, size_t *out_len);
int32_t vg_diff(const uint8_t *in_ptr, size_t in_len, uint8_t **out_ptr, size_t *out_len);
int32_t vg_content_hash(const uint8_t *in_ptr, size_t in_len, uint8_t **out_ptr, size_t *out_len);
int32_t vg_validate(const uint8_t *in_ptr, size_t in_len, uint8_t **out_ptr, size_t *out_len);
void vg_free(uint8_t *ptr, size_t len);

#endif
