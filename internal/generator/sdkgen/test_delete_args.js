import { describe, expect, test } from "bun:test";
import { pathToFileURL } from "node:url";
import path from "node:path";

// Runs the TypeScript SDK of body-args-api against an injected fetch.
// SDK_DIR is the SDK package, with its types package linked into
// node_modules.
const sdkDir = process.env.SDK_DIR;
if (!sdkDir) {
  throw new Error("SDK_DIR env var required");
}

const { BodyArgsApiSDK } = await import(pathToFileURL(path.join(sdkDir, "index.ts")).href);

// sdkWith returns an SDK whose fetch records each request's method, path,
// query string and body as sent, and answers the labels in the success
// envelope.
function sdkWith() {
  const requests = [];
  const sdk = new BodyArgsApiSDK({
    baseUrl: "https://api.example.com",
    fetch: async (input, init = {}) => {
      const url = new URL(String(input));
      requests.push({
        method: init.method,
        path: url.pathname,
        query: url.search,
        body: init.body === undefined ? undefined : JSON.parse(init.body),
      });
      return new Response(JSON.stringify({ data: ["a", "b"], meta: { requestId: "req-1" } }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    },
  });
  return { sdk, requests };
}

describe("TypeScript SDK DELETE arguments", () => {
  test("a DELETE sends its arguments in the JSON body, as the route reads them", async () => {
    const { sdk, requests } = sdkWith();
    const args = {
      reason: "spam",
      labels: ["a", "b"],
      audit: { by: [1] },
      notes: { en: "gone" },
      vector: [0.5, -1],
    };
    expect(await sdk.tag.removeTags("p1", args)).toStrictEqual(["a", "b"]);
    expect(requests).toStrictEqual([{ method: "DELETE", path: "/api/posts/p1/tags", query: "", body: args }]);
  });

  test("positional arguments are sent the same way, and null as null", async () => {
    const { sdk, requests } = sdkWith();
    await sdk.tag.removeTags("p1", "spam", [], undefined, undefined, null);
    expect(requests).toStrictEqual([
      { method: "DELETE", path: "/api/posts/p1/tags", query: "", body: { reason: "spam", labels: [], audit: null } },
    ]);
  });
});
