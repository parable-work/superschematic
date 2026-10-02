import { describe, expect, test } from "bun:test";
import { pathToFileURL } from "node:url";
import path from "node:path";

// Runs the TypeScript SDK of apigen's body-args-api against an injected
// fetch. SDK_DIR is the SDK package, with its types package linked into
// node_modules.
const sdkDir = process.env.SDK_DIR;
if (!sdkDir) {
  throw new Error("SDK_DIR env var required");
}

const { BodyArgsApiSDK, ValidationError } = await import(pathToFileURL(path.join(sdkDir, "index.ts")).href);

const baseUrl = "https://api.example.com";

// sdkWith returns an SDK whose fetch records each request's method, URL and
// body as sent.
function sdkWith() {
  const requests = [];
  const sdk = new BodyArgsApiSDK({
    baseUrl,
    fetch: async (input, init = {}) => {
      requests.push({
        method: init.method,
        url: String(input),
        body: init.body === undefined ? undefined : JSON.parse(init.body),
      });
      return new Response(JSON.stringify({ data: true, meta: { requestId: "req-1" } }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    },
  });
  return { sdk, requests };
}

describe("TypeScript SDK map arguments", () => {
  test("a map is sent as a JSON object of its values; {} is a value, and an absent optional map is left out", async () => {
    const { sdk, requests } = sdkWith();
    // A plain object in the first argument's place is the arguments by
    // name, so a map in first place is passed that way.
    await sdk.tag.nameShades("p1", { shadeByName: { a: "light", b: "dark" }, linksByLocale: { en: ["https://a.test"], fr: [] } });
    await sdk.tag.nameShades("p2", { shadeByName: {} });
    await sdk.tag.placePoints("p3", { pointByName: { a: { x: 1, y: 2 }, b: { x: 0.5, y: -1 } } });
    expect(requests).toStrictEqual([
      {
        method: "PUT",
        url: `${baseUrl}/api/posts/p1/shade-names`,
        body: { shadeByName: { a: "light", b: "dark" }, linksByLocale: { en: ["https://a.test"], fr: [] } },
      },
      { method: "PUT", url: `${baseUrl}/api/posts/p2/shade-names`, body: { shadeByName: {} } },
      {
        method: "PUT",
        url: `${baseUrl}/api/posts/p3/points`,
        body: { pointByName: { a: { x: 1, y: 2 }, b: { x: 0.5, y: -1 } } },
      },
    ]);
  });

  test("a DELETE sends its arguments, a map among them, as the JSON body", async () => {
    const { sdk, requests } = sdkWith();
    await sdk.tag.removeTags("p1", ["a", "b"], true, { by: ["editor"] }, { a: "dark" });
    await sdk.tag.removeTags("p1", { labels: ["c"], shadeByLabel: {} });
    await sdk.tag.removeTags("p1", ["d"]);
    expect(requests).toStrictEqual([
      {
        method: "DELETE",
        url: `${baseUrl}/api/posts/p1/tags`,
        body: { labels: ["a", "b"], purge: true, reason: { by: ["editor"] }, shadeByLabel: { a: "dark" } },
      },
      { method: "DELETE", url: `${baseUrl}/api/posts/p1/tags`, body: { labels: ["c"], shadeByLabel: {} } },
      { method: "DELETE", url: `${baseUrl}/api/posts/p1/tags`, body: { labels: ["d"] } },
    ]);
  });

  // Each call and the errors it must raise, by path and validator, all at
  // once: the argument, name[key], name[key][i] or name[key].field, as the
  // route reports them.
  const refusals = [
    [(sdk) => sdk.tag.nameShades("p0", {}), { shadeByName: "required" }],
    [(sdk) => sdk.tag.nameShades("p0", { shadeByName: null }), { shadeByName: "required" }],
    [(sdk) => sdk.tag.nameShades("p0", { shadeByName: "light" }), { shadeByName: "type" }],
    [(sdk) => sdk.tag.nameShades("p0", { shadeByName: ["light"] }), { shadeByName: "type" }],
    [
      (sdk) => sdk.tag.nameShades("p0", { shadeByName: { a: "dim", b: null, c: 5 } }),
      { "shadeByName[a]": "enum", "shadeByName[b]": "required", "shadeByName[c]": "type" },
    ],
    [(sdk) => sdk.tag.nameShades("p0", { shadeByName: {}, linksByLocale: ["https://a.test"] }), { linksByLocale: "type" }],
    [
      (sdk) =>
        sdk.tag.nameShades("p0", {
          shadeByName: {},
          linksByLocale: { en: ["https://a.test", null, 5], fr: "https://a.test", de: null },
        }),
      {
        "linksByLocale[en][1]": "required",
        "linksByLocale[en][2]": "type",
        "linksByLocale[fr]": "type",
        "linksByLocale[de]": "required",
      },
    ],
    [
      (sdk) => sdk.tag.placePoints("p0", { pointByName: { a: { x: "one", y: 0 }, b: null, c: "here", d: { x: -1, y: 0 } } }),
      { "pointByName[a].x": "type", "pointByName[b]": "required", "pointByName[c]": "type", "pointByName[d].x": "min" },
    ],
    // The map is the argument, not a Point: each of its values is one.
    [(sdk) => sdk.tag.placePoints("p0", { pointByName: { x: 1, y: 2 } }), { "pointByName[x]": "type", "pointByName[y]": "type" }],
    [
      (sdk) => sdk.tag.removeTags("p0", ["a"], undefined, undefined, { a: "dim", b: null }),
      { "shadeByLabel[a]": "enum", "shadeByLabel[b]": "required" },
    ],
    [(sdk) => sdk.tag.removeTags("p0", ["a"], undefined, undefined, "dark"), { shadeByLabel: "type" }],
  ];

  test("a map the route would refuse is refused before the request, every failure at its path", async () => {
    const { sdk, requests } = sdkWith();
    for (const [call, want] of refusals) {
      let caught;
      try {
        await call(sdk);
      } catch (err) {
        caught = err;
      }
      expect(caught).toBeInstanceOf(ValidationError);
      expect(Object.keys(caught.errors).sort()).toStrictEqual(Object.keys(want).sort());
      for (const [field, validator] of Object.entries(want)) {
        expect(caught.errors[field].map((e) => e.validator)).toStrictEqual([validator]);
      }
    }
    expect(requests).toStrictEqual([]);
  });
});
