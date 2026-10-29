// A FileDownload result (D81) through the generated HttpClient: the body is
// the file, a Blob with the filename Content-Disposition offered and the
// Content-Type, and an error still the ApiError of the problem. filenameOf
// reads back every name runtime/http/testdata/content_disposition_parity.json
// writes, as the servers write it. Run by TestFileDownloadRuntime_TypeScript
// with SDK_DIR set to a generated SDK and PARITY to the vectors.
import { describe, expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { pathToFileURL } from "node:url";
import path from "node:path";

const sdkDir = process.env.SDK_DIR;
const parityPath = process.env.PARITY;
if (!sdkDir || !parityPath) {
  throw new Error("SDK_DIR and PARITY env vars required");
}

const { HttpClient, filenameOf } = await import(pathToFileURL(path.join(sdkDir, "client.ts")).href);
const { ApiError } = await import(pathToFileURL(path.join(sdkDir, "types.ts")).href);

/** The name a server offers for a filename: its last path segment, without control characters. */
function offered(name) {
  const base = name.slice(Math.max(name.lastIndexOf("/"), name.lastIndexOf("\\")) + 1).replace(/[\u0000-\u001f\u007f]/gu, "");
  return base === "" || base === "." || base === ".." ? null : base;
}

describe("filenameOf", () => {
  const vectors = JSON.parse(readFileSync(parityPath, "utf8")).vectors;
  for (const vector of vectors) {
    test(vector.name, () => {
      expect(filenameOf(vector.header)).toBe(offered(vector.filename));
    });
  }

  test("reads filename alone, quoted or not, and refuses no header", () => {
    expect(filenameOf('attachment; filename="a \\"b\\".csv"')).toBe('a "b".csv');
    expect(filenameOf("attachment; filename=plain.csv")).toBe("plain.csv");
    expect(filenameOf("attachment; filename*=UTF-8''%E2%82%AC.csv; filename=\"_.csv\"")).toBe("\u20ac.csv");
    expect(filenameOf("attachment; filename*=UTF-8''%ZZ.csv; filename=\"fallback.csv\"")).toBe("fallback.csv");
    expect(filenameOf("attachment")).toBeNull();
    expect(filenameOf(null)).toBeNull();
  });
});

describe("a file response", () => {
  test("is the file, its name and its media type, not the envelope", async () => {
    const calls = [];
    const client = new HttpClient({
      baseUrl: "https://api.example.com",
      fetch: async (input, init = {}) => {
        calls.push({ input: String(input), init });
        return new Response("date,amount\n", {
          status: 200,
          headers: { "Content-Type": "text/csv; charset=utf-8", "Content-Disposition": "attachment; filename=\"m_rz.csv\"; filename*=UTF-8''m%C3%A4rz.csv" },
        });
      },
    });
    const file = await client.get("/api/statements/a/export", { responseType: "file", params: { month: "3" } });
    expect(calls[0].input).toBe("https://api.example.com/api/statements/a/export?month=3");
    expect(file.filename).toBe("m\u00e4rz.csv");
    expect(file.contentType).toBe("text/csv; charset=utf-8");
    expect(file.blob).toBeInstanceOf(Blob);
    expect(await file.blob.text()).toBe("date,amount\n");
  });

  test("is an ApiError when it is refused", async () => {
    const client = new HttpClient({
      baseUrl: "https://api.example.com",
      fetch: async () =>
        new Response(JSON.stringify({ type: "about:blank", title: "Not Found", status: 404, detail: "No such account", code: "not_found" }), {
          status: 404,
          headers: { "Content-Type": "application/problem+json" },
        }),
    });
    try {
      await client.get("/api/statements/a/export", { responseType: "file" });
      throw new Error("expected throw");
    } catch (err) {
      expect(err).toBeInstanceOf(ApiError);
      expect(err.statusCode).toBe(404);
      expect(err.code).toBe("not_found");
    }
  });

  test("survives the refresh of a token", async () => {
    let calls = 0;
    const client = new HttpClient({
      baseUrl: "https://api.example.com",
      auth: { token: "stale", refreshToken: async () => "fresh" },
      fetch: async (_input, init = {}) => {
        calls++;
        if (init.headers.Authorization === "Bearer stale") {
          return new Response(JSON.stringify({ status: 401, code: "unauthorized" }), { status: 401, headers: { "Content-Type": "application/problem+json" } });
        }
        return new Response(new Uint8Array([1, 2, 3]), { status: 200, headers: { "Content-Type": "application/octet-stream", "Content-Disposition": "attachment" } });
      },
    });
    const file = await client.get("/api/files/1", { responseType: "file" });
    expect(calls).toBe(2);
    expect(file.filename).toBeNull();
    expect(new Uint8Array(await file.blob.arrayBuffer())).toEqual(new Uint8Array([1, 2, 3]));
  });
});
