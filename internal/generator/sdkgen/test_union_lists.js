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

// answers maps each operation's path to the data it answers with: circles
// and squares mixed, or null for the paths of operations that may be null
// and find nothing.
const answers = {
  "/api/shapes": [circle, square],
  "/api/shapes/rows": [[circle], [square, circle]],
  "/api/shapes/s-1": square,
  "/api/shape-index": { a: circle, b: square },
  "/api/shape-groups": { round: [circle], mixed: [square, circle], none: [] },
  "/api/shape-search/c-1": circle,
  "/api/shape-search/none": null,
  "/api/shape-search": null,
  "/api/circle-index": { a: circle },
  "/api/circle-groups": { round: [circle, circle], none: [] },
  "/api/circle-search/c-1": circle,
  "/api/circle-search/none": null,
};

// sdkWith returns an SDK whose fetch answers each operation's path with its
// answers entry, in the success envelope.
function sdkWith() {
  return new UnionListsApiSDK({
    baseUrl: "https://api.example.com",
    fetch: async (input) => {
      const { pathname } = new URL(String(input));
      if (!(pathname in answers)) {
        throw new Error(`unexpected path ${pathname}`);
      }
      const data = answers[pathname];
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

  test("a map of a union", async () => {
    expect(await sdkWith().shape.shapesByName()).toEqual({ a: circle, b: square });
  });

  test("a map of lists of a union", async () => {
    expect(await sdkWith().shape.shapeGroups()).toEqual({ round: [circle], mixed: [square, circle], none: [] });
  });

  test("one value of a union that may be null", async () => {
    expect(await sdkWith().shape.findShape("c-1")).toEqual(circle);
    expect(await sdkWith().shape.findShape("none")).toBeNull();
  });

  test("a list of a union that may be null", async () => {
    expect(await sdkWith().shape.findShapes()).toBeNull();
  });
});

describe("TypeScript SDK object maps and nulls", () => {
  test("a map of an object is parsed value by value", async () => {
    expect(await sdkWith().shape.circlesByName()).toEqual({ a: circle });
  });

  test("a map of lists of an object is parsed element by element", async () => {
    expect(await sdkWith().shape.circleGroups()).toEqual({ round: [circle, circle], none: [] });
  });

  test("an object that may be null", async () => {
    expect(await sdkWith().shape.findCircle("c-1")).toEqual(circle);
    expect(await sdkWith().shape.findCircle("none")).toBeNull();
  });
});
