import { describe, expect, test } from "bun:test";
import { pathToFileURL } from "node:url";
import path from "node:path";

// Runs the TypeScript SDK of encrypted-argument-api against an injected
// fetch. storeCard takes an EncryptedField<string> argument, so the SDK
// sends its body as an RSA-OAEP envelope; renameCard sends plain JSON.
// SDK_DIR is the SDK package, with its types package linked into
// node_modules.
const sdkDir = process.env.SDK_DIR;
if (!sdkDir) {
  throw new Error("SDK_DIR env var required");
}

const { EncryptedArgumentApiSDK } = await import(
  pathToFileURL(path.join(sdkDir, "index.ts")).href
);

const keys = await crypto.subtle.generateKey(
  { name: "RSA-OAEP", modulusLength: 2048, publicExponent: new Uint8Array([1, 0, 1]), hash: "SHA-256" },
  true,
  ["encrypt", "decrypt"],
);
const spki = Buffer.from(await crypto.subtle.exportKey("spki", keys.publicKey)).toString("base64");
const publicEncryptionKey = {
  publicKey: `-----BEGIN PUBLIC KEY-----\n${spki}\n-----END PUBLIC KEY-----`,
  algorithm: "RSA_OAEP_256",
  keyId: "key-1",
};

// sdkWith returns an SDK whose fetch records each request and answers a
// CardReceipt in the success envelope.
function sdkWith() {
  const requests = [];
  const sdk = new EncryptedArgumentApiSDK({
    baseUrl: "https://api.example.com",
    encryption: { publicEncryptionKey },
    fetch: async (input, init) => {
      requests.push({ url: new URL(String(input)), method: init.method, body: String(init.body) });
      return new Response(JSON.stringify({ data: { customerId: "c-1", label: "work" }, meta: { requestId: "req-1" } }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    },
  });
  return { sdk, requests };
}

async function decrypt(payload) {
  const plaintext = await crypto.subtle.decrypt({ name: "RSA-OAEP" }, keys.privateKey, Buffer.from(payload, "base64"));
  return JSON.parse(new TextDecoder().decode(plaintext));
}

describe("TypeScript SDK, an EncryptedField<T> argument", () => {
  test("the operation's body travels as an encrypted envelope", async () => {
    const { sdk, requests } = sdkWith();
    await sdk.card.storeCard("c-1", "4242424242424242", "work");
    expect(requests).toHaveLength(1);
    const [request] = requests;
    expect(request.method).toBe("POST");
    expect(request.url.pathname).toBe("/api/customers/c-1/cards");
    expect(request.body).not.toContain("4242424242424242");
    const envelope = JSON.parse(request.body);
    expect(envelope.algorithm).toBe("RSA_OAEP_256");
    expect(envelope.keyId).toBe("key-1");
    expect(await decrypt(envelope.payload)).toEqual({ number: "4242424242424242", label: "work" });
  });

  test("an operation without one sends plain JSON", async () => {
    const { sdk, requests } = sdkWith();
    await sdk.card.renameCard("card-1", "home");
    expect(requests).toHaveLength(1);
    expect(requests[0].method).toBe("PATCH");
    expect(JSON.parse(requests[0].body)).toEqual({ label: "home" });
  });
});
