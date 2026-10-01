import { describe, expect, test } from "bun:test";
import { pathToFileURL } from "node:url";
import path from "node:path";

// Runs the TypeScript SDK of optional-json-api against an injected fetch.
// SDK_DIR is the SDK package, with its types package linked into
// node_modules.
const sdkDir = process.env.SDK_DIR;
if (!sdkDir) {
  throw new Error("SDK_DIR env var required");
}

const { OptionalJsonApiSDK } = await import(pathToFileURL(path.join(sdkDir, "index.ts")).href);

// sdkWith returns an SDK whose fetch records each request body as sent.
function sdkWith() {
  const bodies = [];
  const sdk = new OptionalJsonApiSDK({
    baseUrl: "https://api.example.com",
    fetch: async (_input, init = {}) => {
      bodies.push(init.body === undefined ? undefined : JSON.parse(init.body));
      return new Response(JSON.stringify({ data: true, meta: { requestId: "req-1" } }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    },
  });
  return { sdk, bodies };
}

describe("TypeScript SDK optional Generic.JSON", () => {
  test("a body argument set to null is sent as null; one left out is not sent", async () => {
    const { sdk, bodies } = sdkWith();
    await sdk.note.annotate("n1", { body: { a: 1 } });
    await sdk.note.annotate("n1", { body: { a: 1 }, extra: null });
    expect(bodies[0]).toStrictEqual({ body: { a: 1 } });
    expect(bodies[1]).toStrictEqual({ body: { a: 1 }, extra: null });
  });

  test("an input type field set to null is sent as null; one left out is not sent", async () => {
    const { sdk, bodies } = sdkWith();
    await sdk.note.revise({ body: "text" });
    await sdk.note.revise({ body: "text", extra: null });
    expect(bodies[0]).toStrictEqual({ body: "text" });
    expect(bodies[1]).toStrictEqual({ body: "text", extra: null });
  });
});
