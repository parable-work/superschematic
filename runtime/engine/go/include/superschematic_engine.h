/*
 * C ABI of the engine (D58): runtime/engine/rust/ffi, which a static archive,
 * libsuperschematic_engine.a, exports over the core's behaviors, and a
 * project's engine crate over its own (export_engine_c_abi!).
 *
 * Every document crosses as JSON: a function reads in_len bytes at in_ptr
 * and writes a pointer to its output, and the output's length, through
 * out_ptr and out_len. An empty output is a null pointer and a length of 0.
 * Release every output with eng_free. Each function returns:
 *
 *   ENG_OK             the output is the result;
 *   ENG_ERROR          the output is {"error": {"class", "code", "message",
 *                      "details"}}: class is the TypeScript engine's error
 *                      class (EngineError, SchemaDocumentError, TypeError,
 *                      ...), code its EngineErrorCode (absent for a class
 *                      with none; a number for SqliteError) and details the
 *                      class's other members;
 *   ENG_BAD_ARGUMENTS  an out pointer was null, or an input pointer was null
 *                      with a non-zero length; nothing was written;
 *   ENG_CLOSED         the engine is closed or was never opened, or, for a
 *                      stream, the stream is over; nothing was written.
 *
 * A panic is caught at every export and returned as ENG_ERROR with class
 * Error and code internal; one inside a transaction poisons the engine,
 * which then refuses every call and reports itself not ready (the health
 * call). runtime/engine/rust/ffi/src/lib.rs documents each document's shape.
 */
#ifndef SUPERSCHEMATIC_ENGINE_H
#define SUPERSCHEMATIC_ENGINE_H

#include <stddef.h>
#include <stdint.h>

#define ENG_ABI_VERSION 1

#define ENG_OK 0
#define ENG_ERROR 1
#define ENG_BAD_ARGUMENTS 2
#define ENG_CLOSED 3

/* An open engine (never 0). */
typedef uint64_t eng_engine;
/* An event stream an engine holds open for a response (never 0). */
typedef uint64_t eng_stream;

/* The version of this contract: ENG_ABI_VERSION. */
uint32_t eng_abi_version(void);

/* Opens an engine on its config and writes its handle through engine:
 * {path | postgres, busyTimeoutMs, policy, namespaces, ids, clock,
 * metaSchema, values, runner, retention, tools, project}. The output is
 * {abi, project, dialect, behaviors}. */
int32_t eng_open(const uint8_t *config, size_t config_len, eng_engine *engine,
                 uint8_t **out_ptr, size_t *out_len);

/* Shuts the engine down (its runner stops and every stream ends) and lets
 * the handle go; a call in flight finishes. */
int32_t eng_close(eng_engine engine);

/* Makes a call: {op, principal?, args?, now?}. op names one of the engine's
 * calls (schemas.define, instances.create, ...) or deploy, health or abi.
 * The output is the call's result as JSON, empty for none. */
int32_t eng_call(eng_engine engine, const uint8_t *request, size_t request_len,
                 uint8_t **out_ptr, size_t *out_len);

/* Serves a request: head {method, prefix, path, query, headers, caller,
 * requestId, options} and the body's bytes. head_out is {status, headers,
 * stream} and body_out the response body's bytes; a stream is the handle
 * eng_stream_poll pulls, its body empty. */
int32_t eng_http(eng_engine engine, const uint8_t *head, size_t head_len,
                 const uint8_t *body, size_t body_len,
                 uint8_t **head_out_ptr, size_t *head_out_len,
                 uint8_t **body_out_ptr, size_t *body_out_len);

/* Pulls a stream without waiting: ENG_OK with the bytes to send, empty when
 * it has none yet, or ENG_CLOSED once it is over. */
int32_t eng_stream_poll(eng_engine engine, eng_stream stream,
                        uint8_t **out_ptr, size_t *out_len);

/* Ends a stream: the client went away. */
int32_t eng_stream_close(eng_engine engine, eng_stream stream);

/* Waits until a commit moves the engine's epoch past after_epoch, or
 * timeout_ms passes, and writes the epoch: one waiting thread wakes every
 * stream of the engine. */
int32_t eng_wait(eng_engine engine, uint64_t after_epoch, uint32_t timeout_ms,
                 uint64_t *epoch);

/* Drives the runner: {action: "start" | "stop" | "run_due" | "status"}.
 * The output is the pass run_due ran or the runner's status, empty for
 * start and stop. */
int32_t eng_runner(eng_engine engine, const uint8_t *request, size_t request_len,
                   uint8_t **out_ptr, size_t *out_len);

/* Releases an output. */
void eng_free(uint8_t *ptr, size_t len);

#endif
