import { describe, expect, test } from "bun:test";
import { pathToFileURL } from "node:url";
import path from "node:path";

// Runs the TypeScript SDK of union-lists-api, with the operations
// union_lists_test.go adds, against an injected fetch. SDK_DIR is the SDK
// package, with its types package linked into node_modules.
const sdkDir = process.env.SDK_DIR;
if (!sdkDir) {
  throw new Error("SDK_DIR env var required");
}

const { UnionListsApiSDK } = await import(pathToFileURL(path.join(sdkDir, "index.ts")).href);

const when = "2026-01-02T03:04:05Z";
const circle = { radius: 1, drawnAt: when };
const square = { side: 2 };
const flag = { kind: "flag", raisedAt: when };
const pin = { kind: "pin", label: "here" };

// answers maps each operation's path to the data it answers with: circles
// and squares mixed, or null for the paths of operations that may be null
// and find nothing.
const answers = {
  "/api/shapes": [circle, square],
  "/api/shapes/rows": [[circle], [square, circle]],
  "/api/shapes/s-1": square,
  "/api/shapes/s-2": circle,
  "/api/shape-pages": { items: [square, circle], next: null },
  "/api/drawings/d-1": { title: "plan", shape: circle, shapes: [square, circle] },
  "/api/markers/m-1": flag,
  "/api/markers/m-2": pin,
  // A @strictJSON Pin with a key it does not declare.
  "/api/markers/m-3": { ...pin, note: "x" },
  // A kind no member of Marker has.
  "/api/markers/m-4": { kind: "star", label: "x" },
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
// answers entry, or its entry in overrides, in the success envelope.
function sdkWith(overrides = {}) {
  const data = { ...answers, ...overrides };
  return new UnionListsApiSDK({
    baseUrl: "https://api.example.com",
    fetch: async (input) => {
      const { pathname } = new URL(String(input));
      if (!(pathname in data)) {
        throw new Error(`unexpected path ${pathname}`);
      }
      return new Response(JSON.stringify({ data: data[pathname], meta: { requestId: "req-1" } }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    },
  });
}

// expectCircle checks a Circle read through Shape: its drawnAt is a Date.
function expectCircle(value) {
  expect(value.radius).toBe(1);
  expect(value.drawnAt).toBeInstanceOf(Date);
  expect(value.drawnAt.toISOString()).toBe("2026-01-02T03:04:05.000Z");
}

// refusal awaits a call the SDK must refuse and returns the types package's
// ParseError it rejects with.
async function refusal(promise) {
  try {
    await promise;
  } catch (error) {
    expect(error.name).toBe("ParseError");
    return error;
  }
  throw new Error("expected a ParseError");
}

describe("TypeScript SDK union responses", () => {
  test("a list of a union", async () => {
    const shapes = await sdkWith().shape.listShapes();
    expect(shapes).toHaveLength(2);
    expectCircle(shapes[0]);
    expect(shapes[1]).toEqual(square);
  });

  test("a list of lists of a union", async () => {
    const rows = await sdkWith().shape.shapeRows();
    expectCircle(rows[0][0]);
    expect(rows[1][0]).toEqual(square);
    expectCircle(rows[1][1]);
  });

  test("one value of a union", async () => {
    expect(await sdkWith().shape.getShape("s-1")).toEqual(square);
    expectCircle(await sdkWith().shape.getShape("s-2"));
  });

  test("a page of a union", async () => {
    const page = await sdkWith().shape.pageShapes();
    expect(page.items[0]).toEqual(square);
    expectCircle(page.items[1]);
    expect(page.next).toBeNull();
    const all = [];
    for await (const shape of sdkWith().shape.pageShapesAll()) {
      all.push(shape);
    }
    expectCircle(all[1]);
  });

  test("a model's union fields", async () => {
    const drawing = await sdkWith().shape.getDrawing("d-1");
    expect(drawing.title).toBe("plan");
    expectCircle(drawing.shape);
    expect(drawing.shapes[0]).toEqual(square);
    expectCircle(drawing.shapes[1]);
  });

  test("a discriminated union reads the member its kind names", async () => {
    const raised = await sdkWith().shape.getMarker("m-1");
    expect(raised.kind).toBe("flag");
    expect(raised.raisedAt).toBeInstanceOf(Date);
    expect(await sdkWith().shape.getMarker("m-2")).toEqual(pin);
  });

  test("a @strictJSON member the payload breaks, and an unknown kind, are refused", async () => {
    expect((await refusal(sdkWith().shape.getMarker("m-3"))).reason).toBe("unknown fields: note");
    expect((await refusal(sdkWith().shape.getMarker("m-4"))).reason).toBe("unknown discriminator");
  });

  test("a map of a union", async () => {
    const shapes = await sdkWith().shape.shapesByName();
    expect(Object.keys(shapes)).toEqual(["a", "b"]);
    expectCircle(shapes.a);
    expect(shapes.b).toEqual(square);
  });

  test("a map of lists of a union", async () => {
    const groups = await sdkWith().shape.shapeGroups();
    expect(Object.keys(groups)).toEqual(["round", "mixed", "none"]);
    expect(groups.round).toHaveLength(1);
    expectCircle(groups.round[0]);
    expect(groups.mixed[0]).toEqual(square);
    expectCircle(groups.mixed[1]);
    expect(groups.none).toEqual([]);
  });

  test("a map of a union whose value no member reads is refused", async () => {
    const shapes = sdkWith({ "/api/shape-index": { a: circle, b: "triangle" } }).shape.shapesByName();
    expect((await refusal(shapes)).reason).toBe("expected an object");
    const groups = sdkWith({ "/api/shape-groups": { round: [circle, 3] } }).shape.shapeGroups();
    expect((await refusal(groups)).reason).toBe("expected an object");
  });

  test("one value of a union that may be null", async () => {
    expectCircle(await sdkWith().shape.findShape("c-1"));
    expect(await sdkWith().shape.findShape("none")).toBeNull();
  });

  test("a list of a union that may be null", async () => {
    expect(await sdkWith().shape.findShapes()).toBeNull();
    const found = await sdkWith({ "/api/shape-search": [square, circle] }).shape.findShapes();
    expect(found[0]).toEqual(square);
    expectCircle(found[1]);
  });
});

describe("TypeScript SDK object maps and nulls", () => {
  test("a map of an object is parsed value by value", async () => {
    const circles = await sdkWith().shape.circlesByName();
    expect(Object.keys(circles)).toEqual(["a"]);
    expectCircle(circles.a);
  });

  test("a map of lists of an object is parsed element by element", async () => {
    const groups = await sdkWith().shape.circleGroups();
    expect(Object.keys(groups)).toEqual(["round", "none"]);
    expect(groups.round).toHaveLength(2);
    groups.round.forEach(expectCircle);
    expect(groups.none).toEqual([]);
  });

  test("an object that may be null", async () => {
    expectCircle(await sdkWith().shape.findCircle("c-1"));
    expect(await sdkWith().shape.findCircle("none")).toBeNull();
  });
});
