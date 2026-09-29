import { describe, expect, test } from "bun:test";
import { pathToFileURL } from "node:url";
import path from "node:path";

// Runs the TypeScript SDK of fixture-nested-arrays-api, with grid.cell
// added, against an injected fetch that decodes the label segment once, as
// every server does, and answers with it. SDK_DIR is the SDK package, with
// its types package linked into node_modules.
const sdkDir = process.env.SDK_DIR;
if (!sdkDir) {
  throw new Error("SDK_DIR env var required");
}

const { FixtureNestedArraysApiSDK } = await import(pathToFileURL(path.join(sdkDir, "index.ts")).href);

const gridId = "0b9a4e1c-6f2d-4c1a-9b7e-2d5f8a3c1e40";

// Each label and the path segment encodeURIComponent writes for it.
const labels = [
  ["%", "%25"],
  ["a%25b", "a%2525b"],
  ["100%", "100%25"],
  ["x%41y", "x%2541y"],
  ["a/b", "a%2Fb"],
  ["a b", "a%20b"],
  ["café", "caf%C3%A9"],
  ["a?b", "a%3Fb"],
  ["a#b", "a%23b"],
  ["a+b", "a%2Bb"],
];

describe("TypeScript SDK path parameters", () => {
  test("each value is one path segment, encoded once", async () => {
    const urls = [];
    const sdk = new FixtureNestedArraysApiSDK({
      baseUrl: "https://api.example.com",
      fetch: async (input) => {
        const url = new URL(String(input));
        urls.push(url);
        const segments = url.pathname.split("/");
        const label = segments.length === 6 ? decodeURIComponent(segments[5]) : null;
        return new Response(JSON.stringify({ data: label, meta: { requestId: "req-1" } }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        });
      },
    });
    for (const [label, segment] of labels) {
      expect(await sdk.grid.cell(gridId, label)).toBe(label);
      const url = urls.pop();
      expect(url.pathname).toBe(`/api/grids/${gridId}/cells/${segment}`);
      expect(url.search).toBe("");
      expect(url.hash).toBe("");
    }
  });
});
