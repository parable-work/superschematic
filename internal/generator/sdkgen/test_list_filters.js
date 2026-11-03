import { describe, expect, test } from "bun:test";
import { pathToFileURL } from "node:url";
import path from "node:path";

// Runs the TypeScript SDK of list-filters-api against an injected fetch
// (D101). SDK_DIR is the SDK package, with its types package linked into
// node_modules; it is written with and without lazyValidation.
const sdkDir = process.env.SDK_DIR;
if (!sdkDir) {
  throw new Error("SDK_DIR env var required");
}

const { ListFiltersApiSDK, ValidationError } = await import(
  pathToFileURL(path.join(sdkDir, "index.ts")).href
);

const start = "2026-01-01";
const end = "2026-01-31";
const food = "0b9a4e1c-6f2d-4c1a-9b7e-2d5f8a3c1e40";
const rent = "5d0c7e2a-1b3f-4a6d-8c9e-0f1a2b3c4d5e";

// sdkWith returns an SDK whose fetch records each request's path and query
// pairs, decoded, and answers 7 in the success envelope.
function sdkWith() {
  const calls = [];
  const sdk = new ListFiltersApiSDK({
    baseUrl: "https://api.example.com",
    fetch: async (input) => {
      const url = new URL(String(input));
      calls.push([url.pathname, ...url.searchParams]);
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

describe("TypeScript SDK whose only checked query parameters are lists", () => {
  test("a call without the lists reaches fetch", async () => {
    const { sdk, calls } = sdkWith();
    expect(await sdk.entry.countEntries({ start, end })).toBe(7);
    expect(calls).toEqual([["/api/entries/count", ["start", start], ["end", end]]]);
  });

  test("the lists are sent as one comma-separated value each", async () => {
    const { sdk, calls } = sdkWith();
    expect(await sdk.entry.countEntries({ start, end, categoryIds: [food, rent], userIds: [rent] })).toBe(7);
    expect(calls).toEqual([
      ["/api/entries/count", ["start", start], ["end", end], ["categoryIds", `${food},${rent}`], ["userIds", rent]],
    ]);
  });

  test("a blank item is refused at its path before any request", async () => {
    const { sdk, calls } = sdkWith();
    expect(await validationErrors(sdk.entry.countEntries({ start, end, categoryIds: [""] }))).toEqual({
      "categoryIds[0]": [{ validator: "required", message: "categoryIds[0] is required." }],
    });
    expect(await validationErrors(sdk.entry.countEntries({ start, end, categoryIds: [food], userIds: [rent, " "] }))).toEqual({
      "userIds[1]": [{ validator: "required", message: "userIds[1] is required." }],
    });
    expect(calls).toHaveLength(0);
  });

  test("a required argument with no rule is refused beside them", async () => {
    const { sdk, calls } = sdkWith();
    expect(await validationErrors(sdk.entry.countEntries({ end }))).toEqual({
      start: [{ validator: "required", message: "start is required" }],
    });
    expect(calls).toHaveLength(0);
  });

  test("a required list of numbers is refused without an item", async () => {
    const { sdk, calls } = sdkWith();
    for (const query of [{ years: [] }, { years: null }, {}]) {
      expect(await validationErrors(sdk.tally.sumYears(query))).toEqual({
        years: [{ validator: "required", message: "years is required" }],
      });
    }
    expect(calls).toHaveLength(0);
    expect(await sdk.tally.sumYears({ years: [2025, 2026] })).toBe(7);
    expect(calls).toEqual([["/api/tallies/sum", ["years", "2025,2026"]]]);
  });
});
