// init accepts the wasm module as bytes, a URL, a Response or a promise of
// one, or a compiled module, and loads the bundled file by default; initSync
// takes bytes or a module and reads the bundled file under Bun. Each source
// is proved by running the core it produced.
import { afterAll, expect, spyOn, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import { init, initSync, RulesError, WASM_FILE, type RulesCore, type TypeSpec } from '../dist/index.js';

const wasmUrl = new URL(`../dist/${WASM_FILE}`, import.meta.url);
const wasmBytes = readFileSync(wasmUrl);

const order: TypeSpec = {
  name: 'Order',
  uses: [{ behavior: 'Workflow', config: { states: ['draft', 'paid'], transitions: [{ from: 'draft', to: 'paid' }] }, anchor: { field: 'status' } }],
};

function expectWorks(core: RulesCore) {
  expect(core.check([order])).toEqual([null]);
}

// Serves the module as application/wasm at /streaming (compiled while it
// streams), as application/octet-stream at /whole (read whole), and 404
// everywhere else.
const server = Bun.serve({
  port: 0,
  fetch(request) {
    const path = new URL(request.url).pathname;
    if (path === '/streaming') {
      return new Response(wasmBytes, { headers: { 'content-type': 'application/wasm' } });
    }
    if (path === '/whole') {
      return new Response(wasmBytes, { headers: { 'content-type': 'application/octet-stream' } });
    }
    return new Response('not found', { status: 404 });
  },
});
afterAll(() => server.stop(true));

test('the module imports nothing and exports the C ABI', () => {
  const module = new WebAssembly.Module(wasmBytes);
  expect(WebAssembly.Module.imports(module)).toEqual([]);
  const names = WebAssembly.Module.exports(module).map(e => e.name);
  for (const name of ['memory', 'rules_alloc', 'rules_dealloc', 'rules_free', 'rules_run', 'rules_declarations', 'rules_check']) {
    expect(names).toContain(name);
  }
});

test('the default loads the bundled file', async () => {
  expectWorks(await init());
  expectWorks(initSync());
});

test('bytes', async () => {
  expectWorks(await init(wasmBytes));
  expectWorks(await init(wasmBytes.buffer.slice(wasmBytes.byteOffset, wasmBytes.byteOffset + wasmBytes.byteLength)));
  expectWorks(initSync(wasmBytes));
});

test('a file URL, as a URL and as a string', async () => {
  expectWorks(await init(wasmUrl));
  expectWorks(await init(wasmUrl.href));
});

test('an http URL served as application/wasm and as another type', async () => {
  expectWorks(await init(new URL('/streaming', server.url)));
  expectWorks(await init(new URL('/whole', server.url).href));
});

test('a Response and a promise of one', async () => {
  expectWorks(await init(await fetch(new URL('/streaming', server.url))));
  expectWorks(await init(fetch(new URL('/whole', server.url))));
});

test('a compiled module, to init and to initSync', async () => {
  const module = await WebAssembly.compile(wasmBytes);
  expectWorks(await init(module));
  expectWorks(initSync(module));
});

test('initSync refuses a source it cannot read synchronously', () => {
  expect(() => initSync('file:///x.wasm' as unknown as ArrayBuffer)).toThrow('initSync takes the wasm module');
});

test('a failed response is refused before it is compiled', async () => {
  let thrown: unknown;
  try {
    await init(new URL('/missing', server.url));
  } catch (error) {
    thrown = error;
  }
  expect(thrown).not.toBeInstanceOf(WebAssembly.CompileError);
  expect((thrown as Error).message).toContain('failed: 404');
});

test("a module without the core's exports is refused", async () => {
  // The smallest valid module: the magic number and version, nothing else.
  const empty = new Uint8Array([0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00]);
  await expect(init(empty)).rejects.toThrow('does not export memory');
});

/**
 * A module with the core's exports whose every operation traps
 * (unreachable), as a panic aborts the core on wasm32-unknown-unknown: the
 * binary format written out by hand, since the checkout has no assembler.
 */
function trappingModule(): Uint8Array {
  const text = (name: string) => [name.length, ...new TextEncoder().encode(name)];
  const section = (id: number, body: number[]) => [id, body.length, ...body];
  const vector = (items: number[][]) => [items.length, ...items.flat()];
  const types = vector([
    [0x60, 1, 0x7f, 1, 0x7f], // alloc: (i32) -> i32
    [0x60, 2, 0x7f, 0x7f, 0], // dealloc, free: (i32, i32) -> ()
    [0x60, 4, 0x7f, 0x7f, 0x7f, 0x7f, 1, 0x7f], // an operation
  ]);
  const exports = vector([
    [...text('memory'), 2, 0],
    ...['rules_alloc', 'rules_dealloc', 'rules_free', 'rules_run', 'rules_declarations', 'rules_check'].map((name, i) => [...text(name), 0, i]),
  ]);
  const body = (code: number[]) => [code.length + 1, 0, ...code];
  const code = vector([body([0x41, 16, 0x0b]), body([0x0b]), body([0x0b]), body([0x00, 0x0b]), body([0x00, 0x0b]), body([0x00, 0x0b])]);
  return new Uint8Array([
    0x00, 0x61, 0x73, 0x6d, 1, 0, 0, 0,
    ...section(1, types),
    ...section(3, vector([[0], [1], [1], [2], [2], [2]])),
    ...section(5, vector([[0, 1]])),
    ...section(7, exports),
    ...section(10, code),
  ]);
}

test('a trap in the core is a RulesError of its own failure, and the core goes on', async () => {
  const core = await init(trappingModule());
  for (let i = 0; i < 2; i++) {
    let thrown: unknown;
    try {
      core.check([order]);
    } catch (error) {
      thrown = error;
    }
    expect(thrown).toBeInstanceOf(RulesError);
    expect((thrown as RulesError).code).toBe('internal');
    expect((thrown as RulesError).message).toContain('the core trapped while it ran check');
  }
});

test('every call releases what it allocates', async () => {
  // The core's memory is private to the package, so the test takes it from
  // the instance init creates. A call that keeps its input, its output
  // document or its two out slots grows the memory by at least 8 bytes a
  // call; 20,000 calls of each kind then grow it by more than a 64 KiB page.
  const instantiate = spyOn(WebAssembly, 'instantiate');
  let core: RulesCore;
  let memory: WebAssembly.Memory;
  try {
    core = await init(wasmBytes);
    const instance = (await instantiate.mock.results[0]!.value) as WebAssembly.Instance;
    memory = instance.exports.memory as WebAssembly.Memory;
  } finally {
    instantiate.mockRestore();
  }
  const transition = {
    caller: { subject: 'alice' },
    now: 1,
    action: { kind: 'operation' as const, key: 'o1', useId: 'status', operation: 'transition', params: { to: 'paid' } },
  };
  const calls = () => {
    expect('read' in core.run(order, transition)).toBe(true);
    expect(() => core.run({} as TypeSpec, transition)).toThrow('run:');
    expect(core.check([order])).toEqual([null]);
  };
  for (let i = 0; i < 1_000; i++) {
    calls();
  }
  const before = memory.buffer.byteLength;
  for (let i = 0; i < 20_000; i++) {
    calls();
  }
  expect(memory.buffer.byteLength).toBe(before);
});
