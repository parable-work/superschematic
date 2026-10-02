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

const { BodyArgsApiSDK, ValidationError } = await import(pathToFileURL(path.join(sdkDir, "index.ts")).href);

// sdkWith returns an SDK whose fetch records each request's method, path
// and body as sent, and answers data in the success envelope.
function sdkWith(data) {
  const requests = [];
  const sdk = new BodyArgsApiSDK({
    baseUrl: "https://api.example.com",
    fetch: async (input, init = {}) => {
      requests.push({
        method: init.method,
        path: new URL(String(input)).pathname,
        body: init.body === undefined ? undefined : JSON.parse(init.body),
      });
      return new Response(JSON.stringify({ data, meta: { requestId: "req-1" } }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    },
  });
  return { sdk, requests };
}

// validationErrors returns the errors of the ValidationError the call
// throws, keyed by path.
async function validationErrors(promise) {
  try {
    await promise;
  } catch (err) {
    expect(err).toBeInstanceOf(ValidationError);
    return err.errors;
  }
  throw new Error("expected a ValidationError");
}

// rules maps each path of errors to the validators that failed there.
function rules(errors) {
  return Object.fromEntries(
    Object.entries(errors)
      .sort(([a], [b]) => a.localeCompare(b))
      .map(([key, failures]) => [key, failures.map((failure) => failure.validator)]),
  );
}

describe("TypeScript SDK map arguments", () => {
  test("a map of an enum and a map of lists are sent as JSON objects", async () => {
    const { sdk, requests } = sdkWith(true);
    const args = {
      shadeByName: { a: "light", b: "dark" },
      linksByLocale: { en: ["https://a.test", "https://b.test"], fr: [] },
    };
    expect(await sdk.tag.nameShades("p1", args)).toBe(true);
    expect(requests).toStrictEqual([{ method: "PUT", path: "/api/posts/p1/shade-names", body: args }]);
  });

  test("{} is a value of a required map, and an optional map is left out", async () => {
    const { sdk, requests } = sdkWith(true);
    await sdk.tag.nameShades("p1", { shadeByName: {} });
    expect(requests).toStrictEqual([{ method: "PUT", path: "/api/posts/p1/shade-names", body: { shadeByName: {} } }]);
  });

  test("a map of an object type is sent as a JSON object of objects", async () => {
    const { sdk, requests } = sdkWith(true);
    const args = { pointByName: { origin: { x: 0, y: 0 }, corner: { x: 3, y: -4 } } };
    expect(await sdk.tag.placePoints("p1", args)).toBe(true);
    expect(requests).toStrictEqual([{ method: "PUT", path: "/api/posts/p1/points", body: args }]);
  });

  test("a DELETE sends its maps in the body, positionally too", async () => {
    const { sdk, requests } = sdkWith(["a"]);
    const removed = await sdk.tag.removeTags("p1", "spam", ["a"], { a: "dark" }, { en: ["https://a.test"] });
    expect(removed).toStrictEqual(["a"]);
    expect(requests).toStrictEqual([
      {
        method: "DELETE",
        path: "/api/posts/p1/tags",
        body: { reason: "spam", labels: ["a"], shadeByLabel: { a: "dark" }, linksByLocale: { en: ["https://a.test"] } },
      },
    ]);
  });

  test("each value is checked at name[key], each list element at name[key][i]", async () => {
    const { sdk, requests } = sdkWith(true);
    const errors = await validationErrors(
      sdk.tag.nameShades("p1", {
        shadeByName: { a: "light", b: "dim", c: null },
        linksByLocale: { en: ["https://a.test", null], fr: null, de: "https://c.test", it: [] },
      }),
    );
    expect(rules(errors)).toStrictEqual({
      "linksByLocale[de]": ["type"],
      "linksByLocale[en][1]": ["required"],
      "linksByLocale[fr]": ["required"],
      "shadeByName[b]": ["enum"],
      "shadeByName[c]": ["required"],
    });
    expect(requests).toHaveLength(0);
  });

  test("a value of an object type runs its own validation at name[key]", async () => {
    const { sdk, requests } = sdkWith(true);
    const errors = await validationErrors(
      sdk.tag.placePoints("p1", { pointByName: { origin: { x: 0, y: 0 }, left: { x: -1, y: 0 } } }),
    );
    expect(Object.keys(errors)).toStrictEqual(["pointByName[left]"]);
    expect(requests).toHaveLength(0);
  });

  test("a missing required map is required, and a map that is not an object is type", async () => {
    const { sdk, requests } = sdkWith(true);
    expect(rules(await validationErrors(sdk.tag.placePoints("p1", {})))).toStrictEqual({
      pointByName: ["required"],
    });
    expect(rules(await validationErrors(sdk.tag.placePoints("p1", { pointByName: [{ x: 1, y: 1 }] })))).toStrictEqual({
      pointByName: ["type"],
    });
    expect(rules(await validationErrors(sdk.tag.removeTags("p1", "spam", [], "light", ["https://a.test"])))).toStrictEqual({
      linksByLocale: ["type"],
      shadeByLabel: ["type"],
    });
    expect(requests).toHaveLength(0);
  });
});
