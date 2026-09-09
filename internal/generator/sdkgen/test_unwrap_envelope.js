import { describe, expect, test } from "bun:test";
import { pathToFileURL } from "node:url";
import path from "node:path";

const sdkDir = process.env.SDK_DIR;
if (!sdkDir) {
  throw new Error("SDK_DIR env var required");
}

const { HttpClient } = await import(
  pathToFileURL(path.join(sdkDir, "client.ts")).href
);
const { NetworkError } = await import(
  pathToFileURL(path.join(sdkDir, "types.ts")).href
);

const ERR_EXPECT_OBJECT =
  "SDK contract error: expected RFC 9457 envelope object with data/meta.requestId in success response";
const ERR_EXPECT_FIELDS =
  "SDK contract error: expected RFC 9457 envelope fields data and meta in success response";
const ERR_EXPECT_REQUEST_ID =
  "SDK contract error: expected RFC 9457 envelope meta.requestId in success response";

function responseForPayload(payload) {
  if (payload === undefined) {
    return new Response("", { status: 200, statusText: "OK" });
  }
  return new Response(JSON.stringify(payload), {
    status: 200,
    statusText: "OK",
    headers: { "Content-Type": "application/json" },
  });
}

async function unwrapViaClient(payload) {
  const client = new HttpClient({
    baseUrl: "https://api.example.com",
    fetch: async () => responseForPayload(payload),
  });
  return client.get("/v1/x");
}

describe("strict envelope unwrap via generated HttpClient", () => {
  const successCases = [
    {
      name: "valid envelope object",
      payload: { data: { id: "x" }, meta: { requestId: "abc" } },
      expected: { id: "x" },
    },
    {
      name: "valid envelope null data",
      payload: { data: null, meta: { requestId: "abc" } },
      expected: null,
    },
    {
      name: "valid envelope array data",
      payload: { data: [1, 2, 3], meta: { requestId: "abc" } },
      expected: [1, 2, 3],
    },
    {
      name: "valid envelope string data",
      payload: { data: "hello", meta: { requestId: "abc" } },
      expected: "hello",
    },
    {
      name: "extra keys are allowed",
      payload: { data: { id: "x" }, meta: { requestId: "abc" }, links: {} },
      expected: { id: "x" },
    },
    {
      name: "empty requestId is accepted",
      payload: { data: { id: "x" }, meta: { requestId: "" } },
      expected: { id: "x" },
    },
    {
      name: "undefined payload is passthrough",
      payload: undefined,
      expected: undefined,
    },
  ];

  for (const tc of successCases) {
    test(tc.name, async () => {
      await expect(unwrapViaClient(tc.payload)).resolves.toEqual(tc.expected);
    });
  }

  const errorCases = [
    {
      name: "null payload is contract violation",
      payload: null,
      expectedError: ERR_EXPECT_OBJECT,
    },
    {
      name: "string payload is contract violation",
      payload: "hello",
      expectedError: ERR_EXPECT_OBJECT,
    },
    {
      name: "array payload is contract violation",
      payload: [1, 2, 3],
      expectedError: ERR_EXPECT_FIELDS,
    },
    {
      name: "no data key",
      payload: { result: { id: "x" }, meta: { requestId: "abc" } },
      expectedError: ERR_EXPECT_FIELDS,
    },
    {
      name: "no meta key",
      payload: { data: { id: "x" } },
      expectedError: ERR_EXPECT_FIELDS,
    },
    {
      name: "meta missing requestId",
      payload: { data: { id: "x" }, meta: { other: "v" } },
      expectedError: ERR_EXPECT_REQUEST_ID,
    },
    {
      name: "meta not object",
      payload: { data: { id: "x" }, meta: "bad" },
      expectedError: ERR_EXPECT_REQUEST_ID,
    },
  ];

  for (const tc of errorCases) {
    test(tc.name, async () => {
      try {
        await unwrapViaClient(tc.payload);
        throw new Error("expected unwrapViaClient to throw");
      } catch (error) {
        expect(error).toBeInstanceOf(NetworkError);
        expect(error.message).toBe("Request error: " + tc.expectedError);
      }
    });
  }
});
