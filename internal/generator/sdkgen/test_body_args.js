import { describe, expect, test } from "bun:test";
import { pathToFileURL } from "node:url";
import path from "node:path";

// Runs the TypeScript SDK of body-args-api against its generated Go routes
// at BASE_URL; the Go test that serves them checks the arguments each call
// that reaches the implementation received. SDK_DIR is the SDK package,
// with its types package linked into node_modules.
const sdkDir = process.env.SDK_DIR;
const baseUrl = process.env.BASE_URL;
if (!sdkDir || !baseUrl) {
  throw new Error("SDK_DIR and BASE_URL env vars required");
}

const { BodyArgsApiSDK, ValidationError } = await import(
  pathToFileURL(path.join(sdkDir, "index.ts")).href
);

// sdkWith returns an SDK on the Go routes whose fetch records the method
// and query string of each request it sends.
function sdkWith() {
  const requests = [];
  const sdk = new BodyArgsApiSDK({
    baseUrl,
    fetch: (input, init = {}) => {
      requests.push({ method: init.method, search: new URL(String(input)).search });
      return fetch(input, init);
    },
  });
  return { sdk, requests };
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

describe("TypeScript SDK DELETE without an input type", () => {
  test("sends its arguments in the JSON body, none in the query string", async () => {
    const { sdk, requests } = sdkWith();
    expect(await sdk.tag.removeTags("p1", ["c"])).toStrictEqual(["c"]);
    expect(await sdk.tag.removeTags("p1", ["a", "b"], true, { by: ["editor"] })).toStrictEqual(["a", "b"]);
    // An optional Generic.JSON set to null is sent as null.
    expect(await sdk.tag.removeTags("p1", ["c"], undefined, null)).toStrictEqual(["c"]);
    expect(requests).toStrictEqual(Array(3).fill({ method: "DELETE", search: "" }));
  });

  test("takes its arguments as one object", async () => {
    const { sdk } = sdkWith();
    expect(await sdk.tag.removeTags("p1", { labels: ["d"], purge: true })).toStrictEqual(["d"]);
  });

  test("refuses a missing required argument before the request", async () => {
    const { sdk, requests } = sdkWith();
    for (const call of [
      () => sdk.tag.removeTags("p1"),
      () => sdk.tag.removeTags("p1", null),
      () => sdk.tag.removeTags("p1", { purge: true }),
    ]) {
      const errors = await validationErrors(call());
      expect(Object.keys(errors)).toStrictEqual(["labels"]);
      expect(errors.labels[0].validator).toBe("required");
    }
    expect(requests).toStrictEqual([]);
  });
});
