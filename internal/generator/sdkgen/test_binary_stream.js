import { afterAll, describe, expect, test } from "bun:test";
import { pathToFileURL } from "node:url";
import path from "node:path";

// Runs the TypeScript SDK of fixture-stream-api against a server that
// streams Reports.exportReport, an @httpBinaryStream operation (D167), as an
// Arrow IPC body in chunks with an X-Export-Id header. SDK_DIR is the SDK
// package, with its types package linked into node_modules.
const sdkDir = process.env.SDK_DIR;
if (!sdkDir) {
  throw new Error("SDK_DIR env var required");
}

const { FixtureStreamApiSDK, ApiError, NetworkError } = await import(pathToFileURL(path.join(sdkDir, "index.ts")).href);

const arrowStream = "application/vnd.apache.arrow.stream";

// arrowBody is shaped like an Arrow IPC stream: each message starts with the
// 0xFFFFFFFF continuation marker, which no JSON parser reads, and the stream
// ends with the end marker.
function arrowBody() {
  const parts = [];
  for (let i = 0; i < 64; i++) {
    parts.push(new Uint8Array([0xff, 0xff, 0xff, 0xff, i, 0, 0, 0]), new Uint8Array(1024).fill(i));
  }
  parts.push(new Uint8Array([0xff, 0xff, 0xff, 0xff, 0, 0, 0, 0]));
  return Buffer.concat(parts);
}

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

// state.mode picks the answer; state.requests records each request, and
// state.cancelled whether the endless stream's request ended.
const state = { mode: "stream", requests: [], cancelled: false };

const server = Bun.serve({
  port: 0,
  async fetch(request) {
    const url = new URL(request.url);
    const body = request.method === "POST" ? await request.json() : undefined;
    state.requests.push({ path: url.pathname, query: Object.fromEntries(url.searchParams), accept: request.headers.get("accept"), body });
    if (request.method === "GET" && url.pathname === "/api/reports/r-1") {
      return Response.json({ data: { reportId: "r-1", rows: 64 }, meta: { requestId: "req-0" } });
    }
    if (url.pathname !== "/api/reports/r-1/export") {
      return new Response("not found", { status: 404 });
    }
    switch (state.mode) {
      case "refuse":
        return new Response(
          JSON.stringify({ type: "about:blank", title: "Bad Request", status: 400, detail: "the report has no such column", code: "unknown_column" }),
          { status: 400, headers: { "Content-Type": "application/problem+json", "X-Request-ID": "req-1" } }
        );
      case "json":
        return Response.json({ data: {}, meta: { requestId: "req-2" } });
      case "slowBody":
        return new Response(
          new ReadableStream({
            async start(controller) {
              controller.enqueue(new Uint8Array([0xff, 0xff, 0xff, 0xff]));
              await sleep(150);
              controller.enqueue(new Uint8Array([0, 0, 0, 0]));
              controller.close();
            },
          }),
          { headers: { "Content-Type": arrowStream } }
        );
      case "slowHeaders":
        await sleep(150);
        return new Response(arrowBody(), { headers: { "Content-Type": arrowStream } });
      case "endless":
        return new Response(
          new ReadableStream({
            async pull(controller) {
              await sleep(5);
              controller.enqueue(new Uint8Array(4096).fill(0xff));
            },
            cancel() {
              state.cancelled = true;
            },
          }),
          { headers: { "Content-Type": arrowStream } }
        );
      default: {
        const want = arrowBody();
        return new Response(
          new ReadableStream({
            async start(controller) {
              for (let start = 0; start < want.length; start += 4096) {
                controller.enqueue(want.subarray(start, Math.min(start + 4096, want.length)));
                await sleep(1);
              }
              controller.close();
            },
          }),
          { headers: { "Content-Type": arrowStream, "X-Export-Id": "export-7" } }
        );
      }
    }
  },
});

afterAll(() => server.stop(true));

const sdk = new FixtureStreamApiSDK({ baseUrl: `http://localhost:${server.port}` });
const input = { columns: ["region", "total"] };

describe("an @httpBinaryStream operation", () => {
  test("resolves to the streamed body, unread, with its headers", async () => {
    state.mode = "stream";
    state.requests = [];
    const stream = await sdk.reports.exportReport("r-1", input, { format: "arrow" });
    expect(stream.contentType).toBe(arrowStream);
    expect(stream.status).toBe(200);
    expect(stream.headers["x-export-id"]).toBe("export-7");
    const body = Buffer.from(await new Response(stream.body).arrayBuffer());
    expect(body.equals(arrowBody())).toBe(true);
    const [sent] = state.requests;
    expect(sent.accept).toBe(`${arrowStream}, application/problem+json`);
    expect(sent.query).toEqual({ format: "arrow" });
    expect(sent.body).toEqual(input);

    // The JSON operation beside it still reads the envelope.
    expect(await sdk.reports.describeReport("r-1")).toEqual({ reportId: "r-1", rows: 64 });
  });

  test("is the ApiError of the problem when it is refused", async () => {
    state.mode = "refuse";
    try {
      await sdk.reports.exportReport("r-1", input);
      throw new Error("expected throw");
    } catch (err) {
      expect(err).toBeInstanceOf(ApiError);
      expect(err.statusCode).toBe(400);
      expect(err.message).toBe("the report has no such column");
      expect(err.code).toBe("unknown_column");
      expect(err.requestId).toBe("req-1");
    }
  });

  test("refuses a success of another media type", async () => {
    state.mode = "json";
    try {
      await sdk.reports.exportReport("r-1", input);
      throw new Error("expected throw");
    } catch (err) {
      expect(err).toBeInstanceOf(NetworkError);
      expect(err.message).toContain(`expected ${arrowStream} in streamed success response`);
    }
  });

  test("ends the request when the body is cancelled before its end", async () => {
    state.mode = "endless";
    state.cancelled = false;
    const stream = await sdk.reports.exportReport("r-1", input);
    const reader = stream.body.getReader();
    const first = await reader.read();
    expect(first.done).toBe(false);
    await reader.cancel();
    for (let i = 0; i < 100 && !state.cancelled; i++) {
      await sleep(10);
    }
    expect(state.cancelled).toBe(true);
  });

  test("bounds the wait for the headers, not the body, by the timeout", async () => {
    const quick = new FixtureStreamApiSDK({ baseUrl: `http://localhost:${server.port}`, timeout: 50 });
    state.mode = "slowBody";
    const stream = await quick.reports.exportReport("r-1", input);
    // The body pauses 150 ms, past the 50 ms timeout, and still reads whole.
    const body = new Uint8Array(await new Response(stream.body).arrayBuffer());
    expect([...body]).toEqual([0xff, 0xff, 0xff, 0xff, 0, 0, 0, 0]);

    state.mode = "slowHeaders";
    await expect(quick.reports.exportReport("r-1", input)).rejects.toBeInstanceOf(NetworkError);
  });
});
