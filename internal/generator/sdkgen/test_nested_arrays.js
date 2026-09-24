import { describe, expect, test } from "bun:test";
import { pathToFileURL } from "node:url";
import path from "node:path";

// Runs the TypeScript SDK of fixture-nested-arrays-api, with grid.paint
// added, against an injected fetch. SDK_DIR is the SDK package, with its
// types package linked into node_modules.
const sdkDir = process.env.SDK_DIR;
if (!sdkDir) {
  throw new Error("SDK_DIR env var required");
}

const { FixtureNestedArraysApiSDK, ValidationError } = await import(
  pathToFileURL(path.join(sdkDir, "index.ts")).href
);

const gridId = "0b9a4e1c-6f2d-4c1a-9b7e-2d5f8a3c1e40";

const view = {
  id: gridId,
  labels: [["a", "b"], []],
  shades: [["light"], ["dark", "light"]],
  polygons: [[{ x: 0, y: 0 }, { x: 1, y: 2 }], []],
  weights: [[0.5], []],
};

// sdkWith returns an SDK whose fetch records each request and answers with
// data in the success envelope.
function sdkWith(data) {
  const calls = [];
  const sdk = new FixtureNestedArraysApiSDK({
    baseUrl: "https://api.example.com",
    fetch: async (input, init = {}) => {
      calls.push({
        url: String(input),
        method: init.method,
        body: init.body === undefined ? undefined : JSON.parse(init.body),
      });
      return new Response(JSON.stringify({ data, meta: { requestId: "req-1" } }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    },
  });
  return { sdk, calls };
}

async function validationErrors(promise) {
  try {
    await promise;
  } catch (err) {
    expect(err).toBeInstanceOf(ValidationError);
    return err.errors;
  }
  throw new Error("expected a ValidationError");
}

describe("TypeScript SDK arrays of arrays", () => {
  test("a list-of-lists body argument travels as nested JSON arrays", async () => {
    const { sdk, calls } = sdkWith(view);
    const result = await sdk.grid.replaceLabels(gridId, [["a", "b"], []]);
    expect(calls).toHaveLength(1);
    expect(calls[0].method).toBe("PUT");
    expect(calls[0].url).toBe(`https://api.example.com/api/grids/${gridId}/labels`);
    expect(calls[0].body).toEqual({ labels: [["a", "b"], []] });
    expect(result.labels).toEqual([["a", "b"], []]);
    expect(result.polygons).toEqual([[{ x: 0, y: 0 }, { x: 1, y: 2 }], []]);
  });

  test("a null inner list is refused at labels[i] before the request", async () => {
    const { sdk, calls } = sdkWith(view);
    const errors = await validationErrors(sdk.grid.replaceLabels(gridId, [["a"], null]));
    expect(Object.keys(errors)).toEqual(["labels[1]"]);
    expect(errors["labels[1]"][0].validator).toBe("required");
    expect(calls).toHaveLength(0);
  });

  test("a missing list of lists is refused as required", async () => {
    const { sdk, calls } = sdkWith(view);
    const errors = await validationErrors(sdk.grid.replaceLabels(gridId, undefined));
    expect(errors.labels[0].validator).toBe("required");
    expect(calls).toHaveLength(0);
  });

  test("a bare string[][] response comes back as nested arrays", async () => {
    const { sdk, calls } = sdkWith([["a", "b"], [], ["c"]]);
    const labels = await sdk.grid.gridLabels(gridId, { limit: 2 });
    expect(labels).toEqual([["a", "b"], [], ["c"]]);
    expect(calls[0].method).toBe("GET");
    expect(calls[0].url).toBe(`https://api.example.com/api/grids/${gridId}/labels?limit=2`);
  });

  test("an input type with lists of lists is validated by the types package", async () => {
    const { sdk, calls } = sdkWith(view);
    const errors = await validationErrors(
      sdk.grid.saveGrid({ labels: [["a"]], shades: [["light", "dim"]], polygons: [[]], weights: null }),
    );
    expect(Object.keys(errors)).toEqual(["shades[0][1]"]);
    expect(calls).toHaveLength(0);

    await sdk.grid.saveGrid({ labels: [["a"], []], shades: [["dark"]], polygons: [[{ x: 3, y: 4 }]], weights: [[1, 2], []] });
    expect(calls).toHaveLength(1);
    expect(calls[0].body).toEqual({ labels: [["a"], []], shades: [["dark"]], polygons: [[{ x: 3, y: 4 }]], weights: [[1, 2], []] });
  });

  test("list-of-lists enum and object arguments validate each element", async () => {
    const { sdk, calls } = sdkWith([]);
    const errors = await validationErrors(
      sdk.grid.paint(gridId, [["light", "dim"], null], [null]),
    );
    expect(Object.keys(errors).sort()).toEqual(["polygons[0]", "shades[0][1]", "shades[1]"]);
    expect(errors["shades[0][1]"][0].validator).toBe("enum");
    expect(calls).toHaveLength(0);
  });

  test("a list-of-lists object response is parsed element by element", async () => {
    const { sdk, calls } = sdkWith([[{ x: 1, y: 2 }], []]);
    const polygons = await sdk.grid.paint(gridId, [["dark"], []], [[{ x: 3, y: 4 }]]);
    expect(polygons).toEqual([[{ x: 1, y: 2 }], []]);
    expect(calls[0].body).toEqual({ shades: [["dark"], []], polygons: [[{ x: 3, y: 4 }]] });
  });
});
