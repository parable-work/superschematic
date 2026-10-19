import { describe, expect, test } from "bun:test";
import { pathToFileURL } from "node:url";
import path from "node:path";

// Runs the TypeScript SDK of union-lists-api against an injected fetch.
// SDK_DIR is the SDK package, with its types package linked into
// node_modules.
const sdkDir = process.env.SDK_DIR;
if (!sdkDir) {
  throw new Error("SDK_DIR env var required");
}

const { UnionListsApiSDK } = await import(pathToFileURL(path.join(sdkDir, "index.ts")).href);

const circle = { radius: 1 };
const square = { side: 2 };

// sdkWith returns an SDK whose fetch answers each operation's path with
// circles and squares mixed, in the success envelope.
function sdkWith() {
  return new UnionListsApiSDK({
    baseUrl: "https://api.example.com",
    fetch: async (input) => {
      const { pathname } = new URL(String(input));
      let data;
      if (pathname === "/api/shapes") {
        data = [circle, square];
      } else if (pathname === "/api/shapes/rows") {
        data = [[circle], [square, circle]];
      } else if (pathname === "/api/shapes/s-1") {
        data = square;
      } else {
        throw new Error(`unexpected path ${pathname}`);
      }
      return new Response(JSON.stringify({ data, meta: { requestId: "req-1" } }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    },
  });
}

describe("TypeScript SDK union responses", () => {
  test("a list of a union", async () => {
    expect(await sdkWith().shape.listShapes()).toEqual([circle, square]);
  });

  test("a list of lists of a union", async () => {
    expect(await sdkWith().shape.shapeRows()).toEqual([[circle], [square, circle]]);
  });

  test("one value of a union", async () => {
    expect(await sdkWith().shape.getShape("s-1")).toEqual(square);
  });
});
