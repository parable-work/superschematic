// @superschematic/rules: the pure core of the behaviors (D103) built for
// wasm32-unknown-unknown (runtime/engine/rust/rules-wasm, the crate
// superschematic-rules-wasm), with the behaviors ported onto it, run over
// its JSON contract (./contract.ts, runtime/engine/rust/rules/README.md).
//
// The module imports nothing and has no generated glue, as
// @superschematic/versiongraph's does not (runtime/versiongraph/typescript
// is the model). Each operation copies its JSON input into the module's
// linear memory (rules_alloc), calls the rules_<op> export with the input's
// pointer and length and two out slots, also from rules_alloc, into which
// the export writes its output document's pointer and length as unaligned
// little-endian 32-bit words, reads the document and releases it
// (rules_free) and the input and slots (rules_dealloc). The export returns
// 0 with the operation's output, 1 with an error document
// {"error":{"code","message"}}, and 2 when an out pointer is null.
//
// The core is pure: it opens no connection, reads no clock and makes no id.
// A host runs a command by calling run with the answers it has, answering
// the reads and taking the locks a step asks for, and running it again,
// until the step is done; the HTTP runtime's adapter
// (@superschematic/http-runtime, src/rules.ts) is that host over a
// database, and takes a RulesCore from here, which the generated orm.ts
// passes it.

import type { Answers, BehaviorDeclaration, Caller, Command, Refusal, Step, TypeSpec } from './contract.js';

export type * from './contract.js';

/** The file the module is shipped as, next to this module. */
export const WASM_FILE = 'superschematic_rules_wasm.wasm';

/**
 * Where the wasm module comes from: its bytes, a URL (a file: URL is read
 * from disk under Bun and Node; any other is fetched), a fetch Response or a
 * promise of one, or a compiled WebAssembly.Module.
 */
export type WasmSource = ArrayBuffer | ArrayBufferView | URL | string | Response | PromiseLike<Response> | WebAssembly.Module;

export interface InitOptions {
  /**
   * Parses every output document. The default, JSON.parse, reads a number
   * as a double, so a value wider than 53 bits loses digits, which the core
   * would have kept (it keeps a number's text).
   */
  parse?: (text: string) => unknown;
  /** Serializes every input document. The default is JSON.stringify. */
  stringify?: (value: unknown) => string;
}

/** The codes of an input the module refused: JSON it could not read, JSON of another shape, or a defect of its own. */
export type RulesErrorCode = 'invalid_json' | 'invalid_input' | 'internal';

/**
 * An input the module refused (an error document), never a refusal of a
 * command: a command the core refuses is a step, { done: { refused } }.
 */
export class RulesError extends Error {
  readonly code: RulesErrorCode;

  constructor(code: RulesErrorCode, message: string) {
    super(message);
    this.name = 'RulesError';
    this.code = code;
  }
}

/** What check takes beside the specs. */
export interface CheckOptions {
  /** The caller the define check runs as; a subject of "" with no permissions unless set. */
  readonly caller?: Caller;
  /** The time it runs at, in epoch milliseconds; 0 unless set. */
  readonly now?: number;
}

/** The core, instantiated, with every behavior ported onto it registered. Every operation is synchronous. */
export interface RulesCore {
  /**
   * Runs a command on a type with the answers the host has so far, none
   * when absent, and returns one step: the reads to answer, the locks to
   * take, or the outcome. The same inputs give the same step.
   */
  run(spec: TypeSpec, command: Command, answers?: Answers): Step;
  /**
   * The declaration file of each behavior the core registers, in its
   * order: what a layout's owned columns and a repository's operations are
   * derived from.
   */
  declarations(): BehaviorDeclaration[];
  /**
   * The define check of each spec, in order: null for a spec the core
   * takes, its refusal (invalid_schema) for one it refuses. A host checks
   * its layout's specs when it first opens the core.
   */
  check(specs: readonly TypeSpec[], options?: CheckOptions): (Refusal | null)[];
}

/**
 * Instantiates the core. With no source it loads the wasm file shipped next
 * to this module (WASM_FILE); a bundler that understands
 * `new URL(..., import.meta.url)` copies it into the build.
 */
export async function init(source?: WasmSource, options: InitOptions = {}): Promise<RulesCore> {
  const module = await compile(source ?? new URL(`./${WASM_FILE}`, import.meta.url));
  // The core imports nothing.
  return coreOf(module, await WebAssembly.instantiate(module, {}), options);
}

/**
 * Instantiates the core synchronously, for a caller that cannot await, as
 * the generated orm.ts does when a repository first runs a behavior's
 * command. source is the module's bytes or a compiled WebAssembly.Module.
 * With no source, under Bun and Node, it reads the wasm file shipped next
 * to this module with the node:fs process.getBuiltinModule returns;
 * elsewhere it throws, and the caller passes the module or calls init.
 */
export function initSync(source?: ArrayBuffer | ArrayBufferView | WebAssembly.Module, options: InitOptions = {}): RulesCore {
  let module: WebAssembly.Module;
  if (source instanceof WebAssembly.Module) {
    module = source;
  } else if (source instanceof ArrayBuffer || ArrayBuffer.isView(source)) {
    module = new WebAssembly.Module(source as BufferSource);
  } else if (source === undefined) {
    module = new WebAssembly.Module(readBundledSync() as BufferSource);
  } else {
    throw new TypeError('rules: initSync takes the wasm module\'s bytes or a compiled WebAssembly.Module; init loads a URL or a Response');
  }
  // The core imports nothing.
  return coreOf(module, new WebAssembly.Instance(module, {}), options);
}

type Export = (inPtr: number, inLen: number, outPtr: number, outLen: number) => number;

interface Exports {
  memory: WebAssembly.Memory;
  rules_alloc(len: number): number;
  rules_dealloc(ptr: number, len: number): void;
  rules_free(ptr: number, len: number): void;
  rules_run: Export;
  rules_declarations: Export;
  rules_check: Export;
}

const EXPORTS = ['memory', 'rules_alloc', 'rules_dealloc', 'rules_free', 'rules_run', 'rules_declarations', 'rules_check'] as const;

/** An operation of the C ABI (runtime/engine/rust/rules-wasm/src/ffi.rs). */
type Operation = 'run' | 'declarations' | 'check';

// The return codes of the C ABI.
const RULES_OK = 0;
const RULES_ERROR = 1;

function coreOf(module: WebAssembly.Module, instance: WebAssembly.Instance, options: InitOptions): RulesCore {
  return new Core(module, exportsOf(instance), options);
}

function exportsOf(instance: WebAssembly.Instance): Exports {
  for (const name of EXPORTS) {
    if (!(name in instance.exports)) {
      throw new Error(`rules: the wasm module does not export ${name}`);
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
    throw new Error(`rules: loading ${response.url || 'the wasm module'} failed: ${response.status}`);
  }
  // Streaming compilation needs the application/wasm content type; any
  // other response is read whole.
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

// initSync's default source: the wasm file next to this module, read with
// the node:fs that process.getBuiltinModule returns under Bun and Node, so
// the module keeps no imports and no bundler resolves node:fs.
function readBundledSync(): Uint8Array {
  const url = new URL(`./${WASM_FILE}`, import.meta.url);
  const runtime = (globalThis as { process?: { getBuiltinModule?: (id: string) => unknown } }).process;
  const fs = runtime?.getBuiltinModule?.('node:fs') as { readFileSync(path: URL): Uint8Array } | undefined;
  if (fs === undefined || url.protocol !== 'file:') {
    throw new Error(`rules: initSync has no source and cannot read ${url.href} synchronously here; pass the module's bytes or a WebAssembly.Module, or call init`);
  }
  return fs.readFileSync(url);
}

const encoder = new TextEncoder();
const decoder = new TextDecoder();

class Core implements RulesCore {
  readonly #module: WebAssembly.Module;
  #exports: Exports;
  readonly #parse: (text: string) => unknown;
  readonly #stringify: (value: unknown) => string;

  constructor(module: WebAssembly.Module, exports: Exports, options: InitOptions) {
    this.#module = module;
    this.#exports = exports;
    this.#parse = options.parse ?? JSON.parse;
    this.#stringify = options.stringify ?? JSON.stringify;
  }

  run(spec: TypeSpec, command: Command, answers?: Answers): Step {
    return this.#typed('run', answers === undefined ? { spec, command } : { spec, command, answers }) as Step;
  }

  declarations(): BehaviorDeclaration[] {
    return this.#typed('declarations', {}) as BehaviorDeclaration[];
  }

  check(specs: readonly TypeSpec[], options: CheckOptions = {}): (Refusal | null)[] {
    const input: { specs: readonly TypeSpec[]; caller?: Caller; now?: number } = { specs };
    if (options.caller !== undefined) input.caller = options.caller;
    if (options.now !== undefined) input.now = options.now;
    return this.#typed('check', input) as (Refusal | null)[];
  }

  #typed(operation: Operation, input: unknown): unknown {
    return this.#parse(this.#run(operation, this.#stringify(input)));
  }

  #run(operation: Operation, input: string): string {
    const { code, output } = this.#call(operation, encoder.encode(input));
    if (code === RULES_OK) {
      return output;
    }
    if (code === RULES_ERROR) {
      throw errorFrom(output);
    }
    throw new Error(`rules: the core refused its arguments (${code})`);
  }

  #call(operation: Operation, bytes: Uint8Array): { code: number; output: string } {
    try {
      return this.#copy(operation, bytes);
    } catch (error) {
      // A trap is a panic in the core, which aborts on
      // wasm32-unknown-unknown and may leave its memory mid-allocation: the
      // instance is replaced, so the next call runs on a fresh one, and the
      // call is refused as the core's own failure.
      if (error instanceof WebAssembly.RuntimeError) {
        this.#exports = exportsOf(new WebAssembly.Instance(this.#module, {}));
        throw new RulesError('internal', `rules: the core trapped while it ran ${operation} (${error.message}); it runs on a fresh instance from the next call`);
      }
      throw error;
    }
  }

  #copy(operation: Operation, bytes: Uint8Array): { code: number; output: string } {
    const core = this.#exports;
    const call = operation === 'run' ? core.rules_run : operation === 'check' ? core.rules_check : core.rules_declarations;
    const inPtr = core.rules_alloc(bytes.length);
    // Two 32-bit slots the core writes the output pointer and length into.
    const slots = core.rules_alloc(8);
    try {
      // Views are taken after every call that may grow memory, which
      // detaches the previous buffer.
      new Uint8Array(core.memory.buffer, inPtr, bytes.length).set(bytes);
      const code = call(inPtr, bytes.length, slots, slots + 4);
      if (code !== RULES_OK && code !== RULES_ERROR) {
        return { code, output: '' };
      }
      const view = new DataView(core.memory.buffer);
      const outPtr = view.getUint32(slots, true);
      const outLen = view.getUint32(slots + 4, true);
      try {
        return { code, output: decoder.decode(new Uint8Array(core.memory.buffer, outPtr, outLen)) };
      } finally {
        core.rules_free(outPtr, outLen);
      }
    } finally {
      core.rules_dealloc(inPtr, bytes.length);
      core.rules_dealloc(slots, 8);
    }
  }
}

function errorFrom(output: string): Error {
  let document: { error?: { code?: unknown; message?: unknown } };
  try {
    document = JSON.parse(output) as typeof document;
  } catch {
    return new Error(`rules: unreadable error document: ${output}`);
  }
  const error = document.error;
  if (typeof error?.code !== 'string' || typeof error.message !== 'string') {
    return new Error(`rules: unreadable error document: ${output}`);
  }
  return new RulesError(error.code as RulesErrorCode, error.message);
}
