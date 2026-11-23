// The handler's module (runtime/engine/rust/rules-handler-wasm, the crate
// superschematic-rules-handler-wasm, D117), built for wasm32-unknown-unknown
// and driven through its C ABI with no generated glue, as
// @superschematic/rules drives the core's (runtime/rules/typescript/src/
// index.ts is the model). Each operation copies its JSON input into the
// module's linear memory (handler_alloc), calls the handler_<op> export
// with the input's pointer and length and two out slots, also from
// handler_alloc, into which the export writes its output document's pointer
// and length as unaligned little-endian 32-bit words, reads the document
// and releases it (handler_free) and the input and slots (handler_dealloc).
// The export returns 0 with the operation's output, 1 with an error
// document {"error":{"code","message"}}, and 2 when an out pointer is null.
//
// The module imports nothing: it reads no clock and draws no random
// number. A host gives each request its time and a fresh UUID.

import type { Layout } from './contract.js';

/** The file the module is shipped as, next to this module. */
export const WASM_FILE = 'superschematic_rules_handler_wasm.wasm';

/**
 * Where the wasm module comes from: its bytes, a URL (a file: URL is read
 * from disk under Bun and Node; any other is fetched), a fetch Response or a
 * promise of one, or a compiled WebAssembly.Module.
 */
export type WasmSource = ArrayBuffer | ArrayBufferView | URL | string | Response | PromiseLike<Response> | WebAssembly.Module;

/** The codes of an input the module refused: JSON it could not read, JSON of another shape, or a defect of its own. */
export type HandlerErrorCode = 'invalid_json' | 'invalid_input' | 'internal';

/**
 * An input the module refused (an error document), never a refusal of a
 * request: a request the handler refuses is an answer, { done: { refused } }.
 */
export class HandlerError extends Error {
  readonly code: HandlerErrorCode;

  constructor(code: HandlerErrorCode, message: string) {
    super(message);
    this.name = 'HandlerError';
    this.code = code;
  }
}

/** An operation of the C ABI (runtime/engine/rust/rules-handler-wasm/src/ffi.rs). */
export type Operation = 'run' | 'route' | 'respond' | 'events' | 'tool' | 'bridge' | 'core' | 'layout';

/**
 * The handler's module, instantiated. Each call takes an operation's input
 * document as JSON text and returns its output document's text; the host
 * loop (./host.ts) reads and writes those documents.
 */
export interface HandlerModule {
  call(operation: Operation, input: string): string;
  /** The behaviors' tables and the host loop's bounds, read once. */
  layout(): Layout;
}

/**
 * Instantiates the module. With no source it loads the wasm file shipped
 * next to this module (WASM_FILE); a bundler that understands
 * `new URL(..., import.meta.url)` copies it into the build.
 */
export async function loadHandler(source?: WasmSource): Promise<HandlerModule> {
  const module = await compile(source ?? new URL(`./${WASM_FILE}`, import.meta.url));
  // The module imports nothing.
  return new Module(module, exportsOf(await WebAssembly.instantiate(module, {})));
}

/**
 * Instantiates the module synchronously: source is the module's bytes or a
 * compiled WebAssembly.Module. With no source, under Bun and Node, it
 * reads the wasm file shipped next to this module with the node:fs
 * process.getBuiltinModule returns; elsewhere it throws.
 */
export function loadHandlerSync(source?: ArrayBuffer | ArrayBufferView | WebAssembly.Module): HandlerModule {
  let module: WebAssembly.Module;
  if (source instanceof WebAssembly.Module) {
    module = source;
  } else if (source instanceof ArrayBuffer || ArrayBuffer.isView(source)) {
    module = new WebAssembly.Module(source as BufferSource);
  } else if (source === undefined) {
    module = new WebAssembly.Module(readBundledSync() as BufferSource);
  } else {
    throw new TypeError("rules-handler: loadHandlerSync takes the wasm module's bytes or a compiled WebAssembly.Module; loadHandler loads a URL or a Response");
  }
  return new Module(module, exportsOf(new WebAssembly.Instance(module, {})));
}

type Export = (inPtr: number, inLen: number, outPtr: number, outLen: number) => number;

interface Exports {
  memory: WebAssembly.Memory;
  handler_alloc(len: number): number;
  handler_dealloc(ptr: number, len: number): void;
  handler_free(ptr: number, len: number): void;
  handler_run: Export;
  handler_route: Export;
  handler_respond: Export;
  handler_events: Export;
  handler_tool: Export;
  handler_bridge: Export;
  handler_core: Export;
  handler_layout: Export;
}

const EXPORTS = [
  'memory',
  'handler_alloc',
  'handler_dealloc',
  'handler_free',
  'handler_run',
  'handler_route',
  'handler_respond',
  'handler_events',
  'handler_tool',
  'handler_bridge',
  'handler_core',
  'handler_layout',
] as const;

// The return codes of the C ABI.
const HANDLER_OK = 0;
const HANDLER_ERROR = 1;

function exportsOf(instance: WebAssembly.Instance): Exports {
  for (const name of EXPORTS) {
    if (!(name in instance.exports)) {
      throw new Error(`rules-handler: the wasm module does not export ${name}`);
    }
  }
  return instance.exports as unknown as Exports;
}

async function compile(source: WasmSource): Promise<WebAssembly.Module> {
  if (source instanceof WebAssembly.Module) {
    return source;
  }
  if (source instanceof ArrayBuffer || ArrayBuffer.isView(source)) {
    return WebAssembly.compile(source as BufferSource);
  }
  if (source instanceof URL || typeof source === 'string') {
    const url = source.toString();
    if (url.startsWith('file:')) {
      return WebAssembly.compile((await readFileUrl(url)) as BufferSource);
    }
    source = fetch(url);
  }
  const response = await (source as Response | PromiseLike<Response>);
  if (!response.ok) {
    throw new Error(`rules-handler: loading ${response.url || 'the wasm module'} failed: ${response.status}`);
  }
  if (typeof WebAssembly.compileStreaming === 'function' && response.headers.get('content-type')?.split(';')[0]?.trim() === 'application/wasm') {
    return WebAssembly.compileStreaming(response);
  }
  return WebAssembly.compile(await response.arrayBuffer());
}

// A file: URL is the default source under Bun and Node, whose fetch does not
// read files. The specifier is a variable so a browser bundler neither
// resolves nor bundles node:fs; a browser never takes this path.
async function readFileUrl(url: string): Promise<Uint8Array> {
  const specifier = 'node:fs/promises';
  const fs = (await import(/* @vite-ignore */ /* webpackIgnore: true */ specifier)) as {
    readFile(path: URL): Promise<Uint8Array>;
  };
  return fs.readFile(new URL(url));
}

// loadHandlerSync's default source: the wasm file next to this module, read
// with the node:fs that process.getBuiltinModule returns under Bun and
// Node, so the module keeps no imports and no bundler resolves node:fs.
function readBundledSync(): Uint8Array {
  const url = new URL(`./${WASM_FILE}`, import.meta.url);
  const runtime = (globalThis as { process?: { getBuiltinModule?: (id: string) => unknown } }).process;
  const fs = runtime?.getBuiltinModule?.('node:fs') as { readFileSync(path: URL): Uint8Array } | undefined;
  if (fs === undefined || url.protocol !== 'file:') {
    throw new Error(`rules-handler: loadHandlerSync has no source and cannot read ${url.href} synchronously here; pass the module's bytes or a WebAssembly.Module, or call loadHandler`);
  }
  return fs.readFileSync(url);
}

const encoder = new TextEncoder();
const decoder = new TextDecoder();

class Module implements HandlerModule {
  readonly #module: WebAssembly.Module;
  #exports: Exports;
  #layout: Layout | undefined;

  constructor(module: WebAssembly.Module, exports: Exports) {
    this.#module = module;
    this.#exports = exports;
  }

  layout(): Layout {
    this.#layout ??= JSON.parse(this.call('layout', '{}')) as Layout;
    return this.#layout;
  }

  call(operation: Operation, input: string): string {
    const { code, output } = this.#call(operation, encoder.encode(input));
    if (code === HANDLER_OK) {
      return output;
    }
    if (code === HANDLER_ERROR) {
      throw errorFrom(output);
    }
    throw new Error(`rules-handler: the module refused its arguments (${code})`);
  }

  #call(operation: Operation, bytes: Uint8Array): { code: number; output: string } {
    try {
      return this.#copy(operation, bytes);
    } catch (error) {
      // A trap is a panic in the handler, which aborts on
      // wasm32-unknown-unknown and may leave its memory mid-allocation: the
      // instance is replaced, so the next call runs on a fresh one, and the
      // call is refused as the module's own failure.
      if (error instanceof WebAssembly.RuntimeError) {
        this.#exports = exportsOf(new WebAssembly.Instance(this.#module, {}));
        throw new HandlerError('internal', `rules-handler: the module trapped while it ran ${operation} (${error.message}); it runs on a fresh instance from the next call`);
      }
      throw error;
    }
  }

  #copy(operation: Operation, bytes: Uint8Array): { code: number; output: string } {
    const exports = this.#exports;
    const call = exports[`handler_${operation}`];
    const inPtr = exports.handler_alloc(bytes.length);
    // Two 32-bit slots the module writes the output pointer and length into.
    const slots = exports.handler_alloc(8);
    try {
      // Views are taken after every call that may grow memory, which
      // detaches the previous buffer.
      new Uint8Array(exports.memory.buffer, inPtr, bytes.length).set(bytes);
      const code = call(inPtr, bytes.length, slots, slots + 4);
      if (code !== HANDLER_OK && code !== HANDLER_ERROR) {
        return { code, output: '' };
      }
      const view = new DataView(exports.memory.buffer);
      const outPtr = view.getUint32(slots, true);
      const outLen = view.getUint32(slots + 4, true);
      try {
        return { code, output: decoder.decode(new Uint8Array(exports.memory.buffer, outPtr, outLen)) };
      } finally {
        exports.handler_free(outPtr, outLen);
      }
    } finally {
      exports.handler_dealloc(inPtr, bytes.length);
      exports.handler_dealloc(slots, 8);
    }
  }
}

function errorFrom(output: string): Error {
  let document: { error?: { code?: unknown; message?: unknown } };
  try {
    document = JSON.parse(output) as typeof document;
  } catch {
    return new Error(`rules-handler: unreadable error document: ${output}`);
  }
  const error = document.error;
  if (typeof error?.code !== 'string' || typeof error.message !== 'string') {
    return new Error(`rules-handler: unreadable error document: ${output}`);
  }
  return new HandlerError(error.code as HandlerErrorCode, error.message);
}
