// init accepts the wasm module as bytes, a URL, a Response or a promise of
// one, or a compiled module, and loads the bundled file by default. Each
// source is proved by running an operation on the core it produced.
import { afterAll, expect, spyOn, test } from "bun:test";
import { readFileSync } from "node:fs";
import { init, VersionGraphError, type TreeInput, type VersionGraph } from "../dist/index.js";

const wasmUrl = new URL("../dist/superschematic_versiongraph.wasm", import.meta.url);
const wasmBytes = readFileSync(wasmUrl);

const input: TreeInput = {
  descriptor: {
    version: 2,
    root: { table: "recipe", key: "id" },
    refTable: "recipe_ref",
    commitTable: "recipe_commit",
    patchTable: "recipe_patch",
    kinds: [
      {
        kind: "step",
        table: "step",
        historyTable: "step_history",
        key: "entity_key",
        id: "id",
        ref: "ref",
        tombstone: "deleted_on_ref",
        version: "_version",
        singleton: true,
        columns: { entity_key: "uuid", id: "uuid", ref: "uuid", _version: "integer", title: "string", deleted_on_ref: "boolean" },
      },
    ],
  },
  tree: {
    step: [
      { entity_key: "a", id: "1", ref: "r", _version: 1, title: "Chop" },
      { entity_key: "b", id: "2", ref: "r", _version: 1, title: "Boil" },
    ],
  },
};

function expectWorks(graph: VersionGraph) {
  expect(graph.validate(input).findings.map((finding) => finding.code)).toEqual(["singleton"]);
}

// Serves the module as application/wasm at /streaming (compiled while it
// streams), as application/octet-stream at /whole (read whole), and 404
// everywhere else.
const server = Bun.serve({
  port: 0,
  fetch(request) {
    const path = new URL(request.url).pathname;
    if (path === "/streaming") {
      return new Response(wasmBytes, { headers: { "content-type": "application/wasm" } });
    }
    if (path === "/whole") {
      return new Response(wasmBytes, { headers: { "content-type": "application/octet-stream" } });
    }
    return new Response("not found", { status: 404 });
  },
});
afterAll(() => server.stop(true));

test("the default loads the bundled file", async () => {
  expectWorks(await init());
});

test("bytes", async () => {
  expectWorks(await init(wasmBytes));
  expectWorks(await init(wasmBytes.buffer.slice(wasmBytes.byteOffset, wasmBytes.byteOffset + wasmBytes.byteLength)));
});

test("a file URL, as a URL and as a string", async () => {
  expectWorks(await init(wasmUrl));
  expectWorks(await init(wasmUrl.href));
});

test("an http URL served as application/wasm and as another type", async () => {
  expectWorks(await init(new URL("/streaming", server.url)));
  expectWorks(await init(new URL("/whole", server.url).href));
});

test("a Response and a promise of one", async () => {
  expectWorks(await init(await fetch(new URL("/streaming", server.url))));
  expectWorks(await init(fetch(new URL("/whole", server.url))));
});

test("a compiled module", async () => {
  expectWorks(await init(await WebAssembly.compile(wasmBytes)));
});

test("a failed response is refused before it is compiled", async () => {
  // Caught by hand: bun's rejects.toThrow(message) also passes for the
  // CompileError the 404 body would raise.
  let thrown: unknown;
  try {
    await init(new URL("/missing", server.url));
  } catch (error) {
    thrown = error;
  }
  expect(thrown).not.toBeInstanceOf(WebAssembly.CompileError);
  expect((thrown as Error).message).toContain("failed: 404");
});

test("a module without the core's exports is refused", async () => {
  // The smallest valid module: the magic number and version, nothing else.
  const empty = new Uint8Array([0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00]);
  await expect(init(empty)).rejects.toThrow("does not export memory");
});

test("an input that is not JSON is a VersionGraphError", async () => {
  const graph = await init(wasmBytes);
  let thrown: unknown;
  try {
    graph.run("validate", "{not json");
  } catch (error) {
    thrown = error;
  }
  expect(thrown).toBeInstanceOf(VersionGraphError);
  expect((thrown as VersionGraphError).code).toBe("invalid_json");
  expect((thrown as VersionGraphError).name).toBe("VersionGraphError");
});

test("run returns the output document as text", async () => {
  const graph = await init(wasmBytes);
  expect(JSON.parse(graph.run("validate", JSON.stringify(input)))).toEqual(graph.validate(input));
});

test("every call releases what it allocates", async () => {
  // The core's memory is private to the package, so the test takes it from
  // the instance init creates. A call that keeps its input, its output
  // document or its two out slots grows the memory by at least 8 bytes a
  // call; 100,000 calls then grow it by more than a 64 KiB page.
  const instantiate = spyOn(WebAssembly, "instantiate");
  let graph: VersionGraph;
  let memory: WebAssembly.Memory;
  try {
    graph = await init(wasmBytes);
    const instance = (await instantiate.mock.results[0]!.value) as WebAssembly.Instance;
    memory = instance.exports.memory as WebAssembly.Memory;
  } finally {
    instantiate.mockRestore();
  }
  const text = JSON.stringify(input);
  const calls = () => {
    // A success and a refusal, each with an output document.
    expect(JSON.parse(graph.run("validate", text)).findings).toHaveLength(1);
    expect(() => graph.run("validate", "{not json")).toThrow(VersionGraphError);
  };
  for (let i = 0; i < 1_000; i++) {
    calls();
  }
  const before = memory.buffer.byteLength;
  for (let i = 0; i < 100_000; i++) {
    calls();
  }
  expect(memory.buffer.byteLength).toBe(before);
});

test("parse and stringify replace the JSON codec", async () => {
  const seen: string[] = [];
  const graph = await init(wasmBytes, {
    stringify: (value) => {
      const text = JSON.stringify(value);
      seen.push(text);
      return text;
    },
    parse: (text) => ({ parsed: text }),
  });
  expect(graph.contentHash(input) as unknown).toEqual({ parsed: graph.run("content_hash", JSON.stringify(input)) });
  expect(seen).toEqual([JSON.stringify(input)]);
});
