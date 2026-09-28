// @superschematic/versiongraph: the version-graph core
// (runtime/versiongraph/rust) built for wasm32-unknown-unknown, with typed
// compose, merge, diff, contentHash and validate over its JSON contract
// (runtime/versiongraph/README.md).
//
// The module has no imports and no generated glue. Each operation copies its
// JSON input into the module's linear memory (vg_alloc), calls the
// vg_<op> export, which writes its output document's pointer and length into
// two out slots, reads the document and releases it (vg_free).

import type {
  ComposeInput,
  ComposeOutput,
  ContentHashOutput,
  DiffInput,
  DiffOutput,
  ErrorCode,
  ErrorDocument,
  MergeInput,
  MergeOutput,
  OperationName,
  TreeInput,
  ValidateOutput,
} from "./contract.js";

export type * from "./contract.js";

/**
 * Where the wasm module comes from: its bytes, a URL (a `file:` URL is read
 * from disk under bun and Node; any other is fetched), a fetch Response or a
 * promise of one, or a compiled WebAssembly.Module.
 */
export type WasmSource =
  | ArrayBuffer
  | ArrayBufferView
  | URL
  | string
  | Response
  | PromiseLike<Response>
  | WebAssembly.Module;

export interface InitOptions {
  /**
   * Parses every output document. The default, JSON.parse, reads a number
   * as a double, so an integer or numeric column wider than 53 bits loses
   * digits; pass a parser that keeps them (JSON.parse with a reviver that
   * returns JSON.rawJSON(context.source), say) when rows carry such values.
   */
  parse?: (text: string) => unknown;
  /** Serializes every input document. The default is JSON.stringify. */
  stringify?: (value: unknown) => string;
}

/** An input the core refused. `code` is stable; `message` names the offending part of the input. */
export class VersionGraphError extends Error {
  readonly code: ErrorCode;

  constructor(code: ErrorCode, message: string) {
    super(message);
    this.name = "VersionGraphError";
    this.code = code;
  }
}

/** The core, instantiated. Every operation is synchronous. */
export interface VersionGraph {
  /** Lays one ref's rows over a base tree by entity key. */
  compose(input: ComposeInput): ComposeOutput;
  /** A three-way merge per entity, then per conflict unit. */
  merge(input: MergeInput): MergeOutput;
  /** Each entity's ADD, UPDATE or DELETE from one tree to another. */
  diff(input: DiffInput): DiffOutput;
  /** SHA-256 over the canonical JSON of each kind's content columns. */
  contentHash(input: TreeInput): ContentHashOutput;
  /** Duplicate keys, the singleton rule, absent parents, cycles and orders out of range. */
  validate(input: TreeInput): ValidateOutput;
  /**
   * Runs one operation on a JSON document and returns the output document
   * as text, untouched by `parse`. Throws VersionGraphError for a refused
   * input, as the typed operations do.
   */
  run(operation: OperationName, input: string): string;
}

type Export = (inPtr: number, inLen: number, outPtr: number, outLen: number) => number;

interface Exports {
  memory: WebAssembly.Memory;
  vg_alloc(len: number): number;
  vg_dealloc(ptr: number, len: number): void;
  vg_free(ptr: number, len: number): void;
  vg_compose: Export;
  vg_merge: Export;
  vg_diff: Export;
  vg_content_hash: Export;
  vg_validate: Export;
}

const EXPORTS = [
  "memory",
  "vg_alloc",
  "vg_dealloc",
  "vg_free",
  "vg_compose",
  "vg_merge",
  "vg_diff",
  "vg_content_hash",
  "vg_validate",
] as const;

// The return codes of the C ABI (runtime/versiongraph/rust/src/ffi.rs).
const VG_OK = 0;
const VG_ERROR = 1;

/**
 * Instantiates the core. With no source it loads the wasm file shipped next
 * to this module; a bundler that understands `new URL(..., import.meta.url)`
 * copies it into the build.
 */
export async function init(source?: WasmSource, options: InitOptions = {}): Promise<VersionGraph> {
  const instance = await instantiate(
    source ?? new URL("./superschematic_versiongraph.wasm", import.meta.url),
  );
  for (const name of EXPORTS) {
    if (!(name in instance.exports)) {
      throw new Error(`versiongraph: the wasm module does not export ${name}`);
    }
  }
  return new Core(instance.exports as unknown as Exports, options);
}

async function instantiate(source: WasmSource): Promise<WebAssembly.Instance> {
  // The core imports nothing.
  return WebAssembly.instantiate(await compile(source), {});
}

async function compile(source: WasmSource): Promise<WebAssembly.Module> {
  if (source instanceof WebAssembly.Module) {
    return source;
  }
  if (source instanceof ArrayBuffer || ArrayBuffer.isView(source)) {
    return WebAssembly.compile(source as BufferSource);
  }
  if (source instanceof URL || typeof source === "string") {
    const url = source.toString();
    if (url.startsWith("file:")) {
      return WebAssembly.compile((await readFileUrl(url)) as BufferSource);
    }
    source = fetch(url);
  }
  const response = await (source as Response | PromiseLike<Response>);
  if (!response.ok) {
    throw new Error(`versiongraph: loading ${response.url || "the wasm module"} failed: ${response.status}`);
  }
  // Streaming compilation needs the application/wasm content type; any
  // other response is read whole.
  if (
    typeof WebAssembly.compileStreaming === "function" &&
    response.headers.get("content-type")?.split(";")[0]?.trim() === "application/wasm"
  ) {
    return WebAssembly.compileStreaming(response);
  }
  return WebAssembly.compile(await response.arrayBuffer());
}

// A file: URL is the default source under bun and Node, whose fetch does not
// read files. The specifier is a variable so a browser bundler neither
// resolves nor bundles node:fs; a browser never takes this path.
async function readFileUrl(url: string): Promise<Uint8Array> {
  const specifier = "node:fs/promises";
  const fs = (await import(/* @vite-ignore */ /* webpackIgnore: true */ specifier)) as {
    readFile(path: URL): Promise<Uint8Array>;
  };
  return fs.readFile(new URL(url));
}

const encoder = new TextEncoder();
const decoder = new TextDecoder();

class Core implements VersionGraph {
  readonly #exports: Exports;
  readonly #operations: Record<OperationName, Export>;
  readonly #parse: (text: string) => unknown;
  readonly #stringify: (value: unknown) => string;

  constructor(exports: Exports, options: InitOptions) {
    this.#exports = exports;
    this.#operations = {
      compose: exports.vg_compose,
      merge: exports.vg_merge,
      diff: exports.vg_diff,
      content_hash: exports.vg_content_hash,
      validate: exports.vg_validate,
    };
    this.#parse = options.parse ?? JSON.parse;
    this.#stringify = options.stringify ?? JSON.stringify;
  }

  compose(input: ComposeInput): ComposeOutput {
    return this.#typed("compose", input) as ComposeOutput;
  }

  merge(input: MergeInput): MergeOutput {
    return this.#typed("merge", input) as MergeOutput;
  }

  diff(input: DiffInput): DiffOutput {
    return this.#typed("diff", input) as DiffOutput;
  }

  contentHash(input: TreeInput): ContentHashOutput {
    return this.#typed("content_hash", input) as ContentHashOutput;
  }

  validate(input: TreeInput): ValidateOutput {
    return this.#typed("validate", input) as ValidateOutput;
  }

  run(operation: OperationName, input: string): string {
    const call = this.#operations[operation];
    if (call === undefined) {
      throw new Error(`versiongraph: unknown operation ${String(operation)}`);
    }
    const { code, output } = this.#call(call, encoder.encode(input));
    if (code === VG_OK) {
      return output;
    }
    if (code === VG_ERROR) {
      throw errorFrom(output);
    }
    throw new Error(`versiongraph: the core refused its arguments (${code})`);
  }

  #typed(operation: OperationName, input: unknown): unknown {
    return this.#parse(this.run(operation, this.#stringify(input)));
  }

  #call(operation: Export, bytes: Uint8Array): { code: number; output: string } {
    const core = this.#exports;
    const inPtr = core.vg_alloc(bytes.length);
    // Two 32-bit slots the core writes the output pointer and length into.
    const slots = core.vg_alloc(8);
    try {
      // Views are taken after every call that may grow memory, which
      // detaches the previous buffer.
      new Uint8Array(core.memory.buffer, inPtr, bytes.length).set(bytes);
      const code = operation(inPtr, bytes.length, slots, slots + 4);
      if (code !== VG_OK && code !== VG_ERROR) {
        return { code, output: "" };
      }
      const view = new DataView(core.memory.buffer);
      const outPtr = view.getUint32(slots, true);
      const outLen = view.getUint32(slots + 4, true);
      try {
        return { code, output: decoder.decode(new Uint8Array(core.memory.buffer, outPtr, outLen)) };
      } finally {
        core.vg_free(outPtr, outLen);
      }
    } finally {
      core.vg_dealloc(inPtr, bytes.length);
      core.vg_dealloc(slots, 8);
    }
  }
}

function errorFrom(output: string): Error {
  let document: Partial<ErrorDocument>;
  try {
    document = JSON.parse(output) as Partial<ErrorDocument>;
  } catch {
    return new Error(`versiongraph: unreadable error document: ${output}`);
  }
  const error = document.error;
  if (typeof error?.code !== "string" || typeof error.message !== "string") {
    return new Error(`versiongraph: unreadable error document: ${output}`);
  }
  return new VersionGraphError(error.code, error.message);
}
