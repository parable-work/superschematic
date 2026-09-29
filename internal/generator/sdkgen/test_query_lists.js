import { describe, expect, test } from "bun:test";
import { pathToFileURL } from "node:url";
import path from "node:path";

// Runs the TypeScript SDK of query-lists-api against an injected fetch.
// SDK_DIR is the SDK package, with its types package linked into
// node_modules.
const sdkDir = process.env.SDK_DIR;
if (!sdkDir) {
  throw new Error("SDK_DIR env var required");
}

const { QueryListsApiSDK, ValidationError } = await import(
  pathToFileURL(path.join(sdkDir, "index.ts")).href
);

const ids = ["0b9a4e1c-6f2d-4c1a-9b7e-2d5f8a3c1e40", "5d0c7e2a-1b3f-4a6d-8c9e-0f1a2b3c4d5e"];

// sdkWith returns an SDK whose fetch records each request's query pairs,
// decoded, and answers 7 in the success envelope.
function sdkWith() {
  const calls = [];
  const sdk = new QueryListsApiSDK({
    baseUrl: "https://api.example.com",
    fetch: async (input) => {
      const url = new URL(String(input));
      expect(url.pathname).toBe("/api/posts/count");
      calls.push([...url.searchParams]);
      return new Response(JSON.stringify({ data: 7, meta: { requestId: "req-1" } }), {
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

describe("TypeScript SDK list query parameters", () => {
  test("a list is one comma-separated value", async () => {
    const { sdk, calls } = sdkWith();
    const count = await sdk.post.countPosts({
      ids,
      shades: ["light", "dark"],
      ranks: [1, 100],
      codes: ["ab", "wxyz"],
      tags: ["a tag", "x"],
      flags: [true, false],
      limit: 10,
    });
    expect(count).toBe(7);
    expect(calls).toEqual([
      [
        ["ids", ids.join(",")],
        ["shades", "light,dark"],
        ["ranks", "1,100"],
        ["codes", "ab,wxyz"],
        ["tags", "a tag,x"],
        ["flags", "true,false"],
        ["limit", "10"],
      ],
    ]);
  });

  test("an empty list is left out, as an absent one is", async () => {
    const { sdk, calls } = sdkWith();
    // listMin bounds only a list that is sent: shades needs two items.
    await sdk.post.countPosts({ ids, shades: [], ranks: [], codes: [], tags: [], flags: [] });
    await sdk.post.countPosts({ ids, shades: null, tags: undefined });
    await sdk.post.countPosts({ ids });
    expect(calls).toEqual([[["ids", ids.join(",")]], [["ids", ids.join(",")]], [["ids", ids.join(",")]]]);
  });

  test("a required list with no item is refused", async () => {
    const { sdk, calls } = sdkWith();
    for (const query of [{ ids: [] }, { ids: null }, {}]) {
      const errors = await validationErrors(sdk.post.countPosts(query));
      expect(errors).toEqual({ ids: [{ validator: "required", message: "ids is required" }] });
    }
    expect(calls).toHaveLength(0);
  });

  test("an item the value cannot carry is refused at its path", async () => {
    const { sdk, calls } = sdkWith();
    const cases = [
      [["a,b"], "tags[0]", "pattern"],
      [["ok", " x"], "tags[1]", "pattern"],
      [["x\t"], "tags[0]", "pattern"],
      // The route trims what Go's strings.TrimSpace trims, NEL among it.
      [["\u0085x"], "tags[0]", "pattern"],
      [[""], "tags[0]", "required"],
      [["ok", "  "], "tags[1]", "required"],
    ];
    for (const [tags, itemPath, validator] of cases) {
      const errors = await validationErrors(sdk.post.countPosts({ ids, tags }));
      expect(Object.keys(errors)).toEqual([itemPath]);
      expect(errors[itemPath][0].validator).toBe(validator);
    }
    const errors = await validationErrors(sdk.post.countPosts({ ids: [ids[0], "a,b"], tags: ["", "c,d"] }));
    expect(errors).toEqual({
      "ids[1]": [{ validator: "pattern", message: "ids[1] must not contain a comma or surrounding space." }],
      "tags[0]": [{ validator: "required", message: "tags[0] is required." }],
      "tags[1]": [{ validator: "pattern", message: "tags[1] must not contain a comma or surrounding space." }],
    });
    expect(calls).toHaveLength(0);

    // A byte order mark is not white space to the route, so it travels.
    await sdk.post.countPosts({ ids, tags: ["\ufeffx"] });
    expect(calls).toEqual([[["ids", ids.join(",")], ["tags", "\ufeffx"]]]);
  });

  test("each item is checked against the argument's rules", async () => {
    const { sdk, calls } = sdkWith();
    const cases = [
      // The comma is refused before the item's own rules.
      [{ codes: ["ab,cd"] }, { "codes[0]": "pattern" }],
      [{ codes: ["ab", "a"] }, { "codes[1]": "minLength" }],
      [{ codes: ["abcde"] }, { "codes[0]": "maxLength" }],
      [{ codes: ["AB"] }, { "codes[0]": "pattern" }],
      [{ ranks: [0, 50, 101] }, { "ranks[0]": "min", "ranks[2]": "max" }],
      [{ shades: ["dark"] }, { shades: "listMin" }],
      [{ shades: ["dark", "light", "dark", "light"] }, { shades: "listMax" }],
    ];
    for (const [query, want] of cases) {
      const errors = await validationErrors(sdk.post.countPosts({ ids, ...query }));
      const got = Object.fromEntries(Object.entries(errors).map(([key, list]) => [key, list[0].validator]));
      expect(got).toEqual(want);
    }
    expect(calls).toHaveLength(0);
  });
});
