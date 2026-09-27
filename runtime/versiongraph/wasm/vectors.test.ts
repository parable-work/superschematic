// Runs every vector in runtime/versiongraph/testdata/vectors through the
// core built for wasm32-unknown-unknown, instantiated with WebAssembly and
// driven through its C ABI exports: the input goes into linear memory from
// vg_alloc, and the output document comes back through two out slots.
//
// Build the module first:
//   cd runtime/versiongraph/rust && cargo build --release --target wasm32-unknown-unknown
// VERSIONGRAPH_WASM overrides the module path.
import { expect, test } from "bun:test";
import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";

type Operation = (inPtr: number, inLen: number, outPtr: number, outLen: number) => number;

interface Core {
  memory: WebAssembly.Memory;
  vg_alloc(len: number): number;
  vg_dealloc(ptr: number, len: number): void;
  vg_free(ptr: number, len: number): void;
  vg_compose: Operation;
  vg_merge: Operation;
  vg_diff: Operation;
  vg_content_hash: Operation;
  vg_validate: Operation;
}

const VG_OK = 0;
const VG_ERROR = 1;

const here = import.meta.dir;
const wasmPath =
  process.env.VERSIONGRAPH_WASM ??
  join(here, "../rust/target/wasm32-unknown-unknown/release/superschematic_versiongraph.wasm");
const vectorsDir = join(here, "../testdata/vectors");

const { instance } = await WebAssembly.instantiate(readFileSync(wasmPath), {});
const core = instance.exports as unknown as Core;

const operations: Record<string, Operation> = {
  compose: core.vg_compose,
  merge: core.vg_merge,
  diff: core.vg_diff,
  content_hash: core.vg_content_hash,
  validate: core.vg_validate,
};

// Parse JSON keeping every number as written, so an integer wider than a
// double reaches the core, and is compared, exactly.
function parseExact(text: string): unknown {
  return JSON.parse(text, (_key, value, context?: { source?: string }) =>
    typeof value === "number" && context?.source !== undefined ? JSON.rawJSON(context.source) : value,
  );
}

function call(operation: Operation, input: string): { code: number; output: string } {
  const bytes = new TextEncoder().encode(input);
  const inPtr = core.vg_alloc(bytes.length);
  // Two 32-bit slots the core writes the output pointer and length into.
  const slots = core.vg_alloc(8);
  try {
    new Uint8Array(core.memory.buffer, inPtr, bytes.length).set(bytes);
    const code = operation(inPtr, bytes.length, slots, slots + 4);
    const view = new DataView(core.memory.buffer);
    const outPtr = view.getUint32(slots, true);
    const outLen = view.getUint32(slots + 4, true);
    const output = new TextDecoder().decode(new Uint8Array(core.memory.buffer, outPtr, outLen));
    core.vg_free(outPtr, outLen);
    return { code, output };
  } finally {
    core.vg_dealloc(inPtr, bytes.length);
    core.vg_dealloc(slots, 8);
  }
}

const files = readdirSync(vectorsDir)
  .filter((name) => name.endsWith(".json"))
  .sort();

test("vectors exist", () => {
  expect(files.length).toBeGreaterThan(0);
});

for (const file of files) {
  const vector = parseExact(readFileSync(join(vectorsDir, file), "utf8")) as {
    name: string;
    op: string;
    input: unknown;
    expect: Record<string, unknown>;
  };
  test(vector.name, () => {
    const operation = operations[vector.op];
    expect(operation).toBeDefined();
    const { code, output } = call(operation!, JSON.stringify(vector.input));
    expect(code).toBe("error" in vector.expect ? VG_ERROR : VG_OK);
    expect(JSON.stringify(parseExact(output))).toBe(JSON.stringify(vector.expect));
  });
}

test("an input that is not JSON returns an error document", () => {
  const { code, output } = call(core.vg_validate, "{not json");
  expect(code).toBe(VG_ERROR);
  expect((JSON.parse(output) as { error: { code: string } }).error.code).toBe("invalid_json");
});
