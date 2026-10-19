/*
The engine on the Rust engine (D58): the TypeScript Engine's synchronous,
principal-first API over the Node-API addon runtime/engine/rust/napi
builds (scripts/engine-napi.sh). The TypeScript engine stays the
package's engine until the Rust engine passes its suite; the suite runs
against this one with ENGINE_IMPL=rust (test/rust-pending.txt).
*/

export { NATIVE_ABI_VERSION, NATIVE_BINARY_NAME, loadAddon, nativePackageName, nativeTriple } from './addon.js';
export type { Addon, AddonEngine, AddonHost } from './addon.js';
export { AsyncNativeEngine, openEngineAsync } from './async.js';
export { NativeEngine, NativeSchemaValidator, NativeStorage, openNativeEngine } from './engine.js';
export type { EngineRoute } from './engine.js';
export { nativeEngineApp, nativeEngineMcp } from './http.js';
export type { ServedEngine } from './http.js';
export { NativeUnsupportedError, restoreError } from './errors.js';
export type { NativeGap } from './errors.js';
export { nativeOptions } from './options.js';
export type { HostedOptions, NativeEngineOptions, NativePostgresOptions, ResolvedNativeOptions } from './options.js';
