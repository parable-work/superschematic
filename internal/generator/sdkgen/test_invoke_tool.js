// invokeTool against the generated SDK of fixture-tool-invoke-api, over a
// stubbed fetch: each tool must reach its route with the path, query and
// body the SDK method sends when it is called as declared.
import { describe, expect, test } from "bun:test";
import { pathToFileURL } from "node:url";
import path from "node:path";

const sdkDir = process.env.SDK_DIR;
if (!sdkDir) {
  throw new Error("SDK_DIR env var required");
}

const { FixtureToolInvokeApiSDK } = await import(pathToFileURL(path.join(sdkDir, "index.ts")).href);
const { invokeTool } = await import(pathToFileURL(path.join(sdkDir, "tools", "index.ts")).href);

const noteId = "0b6f1a52-3f4c-4d2e-9a7b-1c2d3e4f5a6b";

// sdkRecording returns an SDK whose requests are recorded in calls and
// answered with an empty note.
function sdkRecording(calls) {
  return new FixtureToolInvokeApiSDK({
    baseUrl: "https://api.example.com",
    fetch: async (input, init = {}) => {
      const url = new URL(String(input));
      calls.push({
        method: init.method,
        path: url.pathname,
        query: Object.fromEntries(url.searchParams),
        body: init.body === undefined ? undefined : JSON.parse(init.body),
      });
      return new Response(JSON.stringify({ data: { id: noteId }, meta: { requestId: "r" } }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    },
  });
}

async function invoke(tool, params) {
  const calls = [];
  await invokeTool(sdkRecording(calls), tool, params);
  expect(calls).toHaveLength(1);
  return calls[0];
}

describe("invokeTool", () => {
  test("an input type is sent with its own field names", async () => {
    const call = await invoke("note.createNote", { title: "Plan", createdBy: "ada" });
    expect(call.method).toBe("POST");
    expect(call.path).toBe("/api/notes");
    expect(call.body).toEqual({ title: "Plan", created_by: "ada" });
  });

  test("body arguments are the body; the path parameter is not", async () => {
    const call = await invoke("note.tagNote", { id: noteId, label: "urgent", weight: 2 });
    expect(call.method).toBe("PUT");
    expect(call.path).toBe(`/api/notes/${noteId}/tag`);
    expect(call.body).toEqual({ label: "urgent", weight: 2 });
  });

  test("a first body argument that is an object is not taken for the arguments object", async () => {
    const call = await invoke("note.configureNotes", { settings: { color: "blue" }, reason: "theme" });
    expect(call.path).toBe("/api/notes/configure");
    expect(call.body).toEqual({ settings: { color: "blue" }, reason: "theme" });
  });

  test("query parameters go to the query, body arguments to the body", async () => {
    const call = await invoke("note.moveNote", { title: "Plan", folder: "archive", dryRun: true });
    expect(call.path).toBe("/api/notes/move");
    expect(call.query).toEqual({ dryRun: "true" });
    expect(call.body).toEqual({ title: "Plan", folder: "archive" });
  });

  test("an encrypted operation takes the encryption key in its options", async () => {
    const call = await invoke("note.sealNote", {
      title: "Plan",
      body: "secret",
      dryRun: false,
      publicEncryptionKey: { publicKey: "unused", algorithm: "NONE", keyId: "key-1" },
    });
    expect(call.path).toBe("/api/notes/seal");
    expect(call.query).toEqual({ dryRun: "false" });
    expect(call.body.keyId).toBe("key-1");
    expect(JSON.parse(Buffer.from(call.body.payload, "base64").toString())).toEqual({ title: "Plan", body: "secret" });
  });

  test("a file upload is refused before any request", async () => {
    const calls = [];
    await expect(
      invokeTool(sdkRecording(calls), "note.attach", { file: "https://example.com/a.png", caption: "a" })
    ).rejects.toThrow("unsupported_multipart");
    expect(calls).toHaveLength(0);
  });
});
