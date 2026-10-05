import { describe, expect, test } from "bun:test";
import { pathToFileURL } from "node:url";
import path from "node:path";

// Runs a generated method of query-lists-api with each form of its last
// argument: an AbortSignal, as methods have always taken, or RequestOptions
// with a signal and the end user to forward (D37). SDK_DIR is the SDK
// package, with its types package linked into node_modules.
const sdkDir = process.env.SDK_DIR;
if (!sdkDir) {
  throw new Error("SDK_DIR env var required");
}

const { QueryListsApiSDK } = await import(pathToFileURL(path.join(sdkDir, "index.ts")).href);

const ids = ["0b9a4e1c-6f2d-4c1a-9b7e-2d5f8a3c1e40"];

// sdkWith returns an SDK with a service credential whose fetch records each
// request's headers and signal, and answers 7 in the success envelope. With
// no timeout, the caller's signal reaches fetch as it is.
function sdkWith() {
  const requests = [];
  const sdk = new QueryListsApiSDK({
    baseUrl: "https://api.example.com",
    timeout: 0,
    serviceCredential: { token: async () => "service-token" },
    fetch: async (_input, init = {}) => {
      requests.push(init);
      return new Response(JSON.stringify({ data: 7, meta: { requestId: "req-1" } }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    },
  });
  return { sdk, requests };
}

describe("a method's last argument", () => {
  test("an AbortSignal alone still cancels the call", async () => {
    const { sdk, requests } = sdkWith();
    const controller = new AbortController();
    await sdk.post.countPosts({ ids }, controller.signal);
    expect(requests[0].signal).toBe(controller.signal);
    expect(requests[0].headers.Authorization).toBeUndefined();
    expect(requests[0].headers["Service-Authorization"]).toBe("Bearer service-token");
  });

  test("forward sends the end user of the request being served", async () => {
    const { sdk, requests } = sdkWith();
    // A server's RequestContext is a ForwardedUser: it has bearerToken.
    const ctx = { bearerToken: "alice", method: "GET", path: "/orders" };
    await sdk.post.countPosts({ ids }, { forward: ctx });
    expect(requests[0].headers.Authorization).toBe("Bearer alice");
    expect(requests[0].headers["Service-Authorization"]).toBe("Bearer service-token");
  });

  test("RequestOptions carries a signal beside forward", async () => {
    const { sdk, requests } = sdkWith();
    const controller = new AbortController();
    await sdk.post.countPosts({ ids }, { signal: controller.signal, forward: { bearerToken: null } });
    expect(requests[0].signal).toBe(controller.signal);
    expect(requests[0].headers.Authorization).toBeUndefined();
  });
});
