/*
 * C ABI of the behaviors' core (D103): runtime/engine/rust/rules-wasm,
 * src/ffi.rs, the native build of the module @superschematic/rules loads.
 *
 * Each operation reads in_len bytes of JSON at in_ptr and writes a pointer
 * to its output document, and the document's length, through out_ptr and
 * out_len. It returns RULES_OK with the operation's output, RULES_ERROR
 * with {"error":{"code","message"}}, or RULES_BAD_ARGUMENTS when an out
 * pointer is null (nothing is written). Release every output with
 * rules_free. rules_alloc and rules_dealloc exist in the wasm32 build alone.
 */
#ifndef SUPERSCHEMATIC_RULES_H
#define SUPERSCHEMATIC_RULES_H

#include <stddef.h>
#include <stdint.h>

#define RULES_OK 0
#define RULES_ERROR 1
#define RULES_BAD_ARGUMENTS 2

int32_t rules_run(const uint8_t *in_ptr, size_t in_len, uint8_t **out_ptr, size_t *out_len);
int32_t rules_declarations(const uint8_t *in_ptr, size_t in_len, uint8_t **out_ptr, size_t *out_len);
int32_t rules_check(const uint8_t *in_ptr, size_t in_len, uint8_t **out_ptr, size_t *out_len);
void rules_free(uint8_t *ptr, size_t len);

#endif
