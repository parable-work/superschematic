import { describe, expect, test } from "bun:test";
import { pathToFileURL } from "node:url";
import path from "node:path";

// Runs the TypeScript SDK of fixture-idempotent-api against an injected
// fetch (D140). SDK_DIR is the SDK package, with its types package linked
// into node_modules.
const sdkDir = process.env.SDK_DIR;
if (!sdkDir) {
  throw new Error("SDK_DIR env var required");
}

const { FixtureIdempotentApiSDK } = await import(pathToFileURL(path.join(sdkDir, "index.ts")).href);
const { newIdempotencyKey } = await import(pathToFileURL(path.join(sdkDir, "client.ts")).href);

const uuidV4 = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/u;
const order = { number: 1, sku: "tea", quantity: 2, cancelled: false };

// sdkWith returns an SDK whose fetch records each request's method and the
// Idempotency-Key it carries, undefined for none.
function sdkWith() {
  const seen = [];
  const sdk = new FixtureIdempotentApiSDK({
    baseUrl: "https://api.example.com",
    fetch: async (_input, init = {}) => {
      const headers = new Headers(init.headers);
      seen.push({ method: init.method, key: headers.get("Idempotency-Key") ?? undefined });
      const data = init.method === "GET" ? [] : order;
      return new Response(JSON.stringify({ data, meta: { requestId: "req-1" } }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    },
  });
  return { sdk, seen };
}

describe("TypeScript SDK Idempotency-Key", () => {
  test("each call to an @idempotent operation makes its own UUID", async () => {
    const { sdk, seen } = sdkWith();
    await sdk.orders.placeOrder({ sku: "tea", quantity: 2 });
    await sdk.orders.placeOrder({ sku: "tea", quantity: 2 });
    await sdk.orders.cancelOrder(1);
    expect(seen.map((request) => request.method)).toStrictEqual(["POST", "POST", "DELETE"]);
    for (const request of seen) {
      expect(request.key).toMatch(uuidV4);
    }
    expect(new Set(seen.map((request) => request.key)).size).toBe(3);
  });

  test("a call sends the key its options name", async () => {
    const { sdk, seen } = sdkWith();
    await sdk.orders.placeOrder({ sku: "tea", quantity: 2 }, { idempotencyKey: "order-7" });
    await sdk.orders.cancelOrder(1, { idempotencyKey: "order-7" });
    // An AbortSignal in the options' place leaves the key to the method.
    await sdk.orders.placeOrder({ sku: "tea", quantity: 2 }, new AbortController().signal);
    expect(seen[0]).toStrictEqual({ method: "POST", key: "order-7" });
    expect(seen[1]).toStrictEqual({ method: "DELETE", key: "order-7" });
    expect(seen[2].key).toMatch(uuidV4);
  });

  test("an operation that is not @idempotent sends none", async () => {
    const { sdk, seen } = sdkWith();
    await sdk.orders.listOrders();
    await sdk.orders.listOrders({ idempotencyKey: "order-7" });
    expect(seen).toStrictEqual([
      { method: "GET", key: undefined },
      { method: "GET", key: undefined },
    ]);
  });

  test("newIdempotencyKey is a UUID v4, with or without randomUUID", () => {
    expect(newIdempotencyKey()).toMatch(uuidV4);
    const randomUUID = globalThis.crypto.randomUUID;
    try {
      globalThis.crypto.randomUUID = undefined;
      const made = [newIdempotencyKey(), newIdempotencyKey()];
      expect(made[0]).toMatch(uuidV4);
      expect(made[1]).toMatch(uuidV4);
      expect(made[0]).not.toBe(made[1]);
    } finally {
      globalThis.crypto.randomUUID = randomUUID;
    }
  });
});
