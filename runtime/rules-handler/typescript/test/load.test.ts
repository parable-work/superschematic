// The module imports nothing and exports its C ABI; loadHandler takes its
// bytes, a URL, a Response or a compiled module and loads the bundled file
// by default, and loadHandlerSync takes bytes or a module and reads the
// bundled file under Bun. An input the module cannot read throws
// HandlerError; a request it refuses is an answer.
import { afterAll, expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import { encode, HandlerError, loadHandler, loadHandlerSync, raw, WASM_FILE, type HandlerModule } from '../dist/index.js';

const wasmUrl = new URL(`../dist/${WASM_FILE}`, import.meta.url);
const wasmBytes = readFileSync(wasmUrl);

const server = Bun.serve({
  port: 0,
  fetch(request) {
    const path = new URL(request.url).pathname;
    if (path === '/streaming') return new Response(wasmBytes, { headers: { 'content-type': 'application/wasm' } });
    if (path === '/whole') return new Response(wasmBytes, { headers: { 'content-type': 'application/octet-stream' } });
    return new Response('not found', { status: 404 });
  },
});
afterAll(() => server.stop(true));

function expectWorks(handler: HandlerModule) {
  expect(handler.layout().maxAttempts).toBe(8);
  const step = JSON.parse(handler.call('run', '{"request":{"kind":"types"},"context":{"caller":{"subject":"a"},"now":1,"request":"r1","id":"00000000-0000-4000-8000-000000000001"}}'));
  expect(step).toEqual({ read: [{ kind: 'live' }] });
}

test('the module imports nothing and exports the C ABI', () => {
  const module = new WebAssembly.Module(wasmBytes);
  expect(WebAssembly.Module.imports(module)).toEqual([]);
  const names = WebAssembly.Module.exports(module).map(entry => entry.name);
  for (const name of ['memory', 'handler_alloc', 'handler_dealloc', 'handler_free', 'handler_run', 'handler_route', 'handler_respond', 'handler_events', 'handler_tool', 'handler_bridge', 'handler_core', 'handler_layout', 'handler_declare']) {
    expect(names).toContain(name);
  }
});

test('every source loads the handler', async () => {
  expectWorks(await loadHandler());
  expectWorks(loadHandlerSync());
  expectWorks(await loadHandler(wasmBytes));
  expectWorks(loadHandlerSync(wasmBytes));
  expectWorks(await loadHandler(wasmUrl));
  expectWorks(await loadHandler(wasmUrl.href));
  expectWorks(await loadHandler(new URL('/streaming', server.url)));
  expectWorks(await loadHandler(fetch(new URL('/whole', server.url))));
  const compiled = new WebAssembly.Module(wasmBytes);
  expectWorks(await loadHandler(compiled));
  expectWorks(loadHandlerSync(compiled));
  await expect(loadHandler(new URL('/missing', server.url))).rejects.toThrow('404');
});

test('an input the module cannot read throws HandlerError; a refusal is an answer', () => {
  const handler = loadHandlerSync();
  expect(() => handler.call('run', '{not json')).toThrow(HandlerError);
  try {
    handler.call('run', '{"request":{"kind":"types"},"context":{"caller":{"subject":"a"},"now":1,"id":"no"}}');
  } catch (error) {
    expect(error).toBeInstanceOf(HandlerError);
    expect((error as HandlerError).code).toBe('invalid_input');
  }
  const refused = JSON.parse(
    handler.call('run', '{"request":{"kind":"types"},"context":{"caller":{"subject":""},"now":1,"id":"00000000-0000-4000-8000-000000000001"}}'),
  );
  expect(refused.done.refused.code).toBe('invalid_argument');
});

test('encode splices raw JSON text as it is and leaves undefined members out', () => {
  expect(encode({ a: raw('{"n":9007199254740993}'), b: undefined, c: [1, undefined, 'x'] })).toBe('{"a":{"n":9007199254740993},"c":[1,null,"x"]}');
  expect(() => JSON.stringify(raw('1'))).toThrow(TypeError);
});
