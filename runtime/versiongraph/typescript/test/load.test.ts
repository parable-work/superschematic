// init accepts the wasm module as bytes, a URL, a Response or a promise of
// one, or a compiled module, and loads the bundled file by default. Each
// source is proved by running an operation on the core it produced.
import { afterAll, expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { init, VersionGraphError, type TreeInput, type VersionGraph } from "../dist/index.js";

const wasmUrl = new URL("../dist/superschematic_versiongraph.wasm", import.meta.url);
const wasmBytes = readFileSync(wasmUrl);

const input: TreeInput = {
  descriptor: {
    kinds: [
      {
        kind: "step",
        key: "entity_key",
        id: "id",
        ref: "ref",
        tombstone: "deleted_on_ref",
        version: "_version",
        singleton: true,
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

test("a failed response is refused", async () => {
  await expect(init(new URL("/missing", server.url))).rejects.toThrow("failed: 404");
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

test("every call releases its input", async () => {
  // 600 calls of 8 MiB each are more than the 4 GiB a wasm32 memory can
  // grow to, so a buffer left unreleased traps before the loop ends.
  const graph = await init(wasmBytes);
  const input = "{not json" + " ".repeat(8 * 1024 * 1024);
  for (let i = 0; i < 600; i++) {
    expect(() => graph.run("validate", input)).toThrow(VersionGraphError);
  }
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
