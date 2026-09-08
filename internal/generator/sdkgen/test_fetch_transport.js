import { describe, expect, test, beforeEach, afterEach } from "bun:test";
import { pathToFileURL } from "node:url";
import path from "node:path";

const sdkDir = process.env.SDK_DIR;
if (!sdkDir) {
  throw new Error("SDK_DIR env var required");
}

const { HttpClient } = await import(
  pathToFileURL(path.join(sdkDir, "client.ts")).href
);
const { ApiError, NetworkError } = await import(
  pathToFileURL(path.join(sdkDir, "types.ts")).href
);

function envelope(data, requestId = "req-1") {
  return { data, meta: { requestId } };
}

function jsonResponse(body, status = 200, headers = {}) {
  return new Response(JSON.stringify(body), {
    status,
    statusText: status === 200 ? "OK" : "Error",
    headers: { "Content-Type": "application/json", ...headers },
  });
}

describe("generated HttpClient fetch transport", () => {
  const originalFetch = globalThis.fetch;
  let calls;

  beforeEach(() => {
    calls = [];
  });

  afterEach(() => {
    globalThis.fetch = originalFetch;
  });

  test("default transport uses globalThis.fetch with method, url, headers, body", async () => {
    globalThis.fetch = async (input, init = {}) => {
      calls.push({ input: String(input), init });
      return jsonResponse(envelope({ id: "1" }));
    };

    const client = new HttpClient({ baseUrl: "https://api.example.com" });
    const data = await client.post("/v1/items", { name: "n" }, {
      headers: { "X-Test": "yes" },
    });

    expect(data).toEqual({ id: "1" });
    expect(calls).toHaveLength(1);
    expect(calls[0].input).toBe("https://api.example.com/v1/items");
    expect(calls[0].init.method).toBe("POST");
    expect(calls[0].init.headers["Content-Type"]).toBe("application/json");
    expect(calls[0].init.headers["X-Test"]).toBe("yes");
    expect(calls[0].init.body).toBe(JSON.stringify({ name: "n" }));
  });

  test("injected fetch receives generated url, method, headers, and body", async () => {
    const injected = async (input, init = {}) => {
      calls.push({ input: String(input), init });
      return jsonResponse(envelope({ ok: true }));
    };

    const client = new HttpClient({
      baseUrl: "https://api.example.com",
      fetch: injected,
    });
    await client.get("/v1/x", { params: { tier: ["a", "b"], q: "1" } });

    expect(calls).toHaveLength(1);
    expect(calls[0].input).toBe("https://api.example.com/v1/x?tier=a%2Cb&q=1");
    expect(calls[0].init.method).toBe("GET");
    expect(calls[0].init.body).toBeUndefined();
  });

  test("relative baseUrl stays relative and keeps path plus query", async () => {
    const client = new HttpClient({
      baseUrl: "/api/sdk/web",
      fetch: async (input, init = {}) => {
        calls.push({ input: String(input), init });
        return jsonResponse(envelope({ ok: true }));
      },
    });
    await client.get("/v1/x", { params: { tier: ["a", "b"], q: "1" } });

    expect(calls).toHaveLength(1);
    expect(calls[0].input).toBe("/api/sdk/web/v1/x?tier=a%2Cb&q=1");
  });

  test("non-JSON error body still maps to ApiError with the status code", async () => {
    const client = new HttpClient({
      baseUrl: "https://api.example.com",
      fetch: async () =>
        new Response("<html><body>Bad Gateway</body></html>", {
          status: 502,
          statusText: "Bad Gateway",
          headers: { "Content-Type": "text/html" },
        }),
    });
    try {
      await client.get("/v1/x");
      throw new Error("expected throw");
    } catch (err) {
      expect(err).toBeInstanceOf(ApiError);
      expect(err.statusCode).toBe(502);
      expect(err.message).toBe("Bad Gateway");
    }
  });

  test("RFC 9457 envelope unwrap works under injected fetch", async () => {
    const client = new HttpClient({
      baseUrl: "https://api.example.com",
      fetch: async () => jsonResponse(envelope({ hello: "world" }, "abc")),
    });
    await expect(client.get("/v1/x")).resolves.toEqual({ hello: "world" });
  });

  test("typed ApiError mapping works under injected fetch", async () => {
    const client = new HttpClient({
      baseUrl: "https://api.example.com",
      fetch: async () =>
        jsonResponse(
          { title: "bad", detail: "nope", status: 400 },
          400,
          { "x-request-id": "rid-9" }
        ),
    });
    try {
      await client.get("/v1/x");
      throw new Error("expected throw");
    } catch (err) {
      expect(err).toBeInstanceOf(ApiError);
      expect(err.statusCode).toBe(400);
      expect(err.message).toBe("nope");
      expect(err.requestId).toBe("rid-9");
    }
  });

  test("caller AbortSignal reaches injected fetch", async () => {
    const controller = new AbortController();
    let fetchSignal;
    const client = new HttpClient({
      baseUrl: "https://api.example.com",
      fetch: async (_input, init = {}) => {
        fetchSignal = init.signal;
        await new Promise((_, reject) => {
          init.signal.addEventListener("abort", () => {
            reject(new DOMException("Aborted", "AbortError"));
          });
        });
      },
    });
    const requestPromise = client.get("/v1/x", { signal: controller.signal });
    await new Promise((resolve) => setTimeout(resolve, 0));
    controller.abort();
    await expect(requestPromise).rejects.toBeInstanceOf(NetworkError);
    expect(fetchSignal.aborted).toBe(true);
  });

  test("timeout aborts the injected transport", async () => {
    let sawAbort = false;
    const client = new HttpClient({
      baseUrl: "https://api.example.com",
      timeout: 20,
      fetch: async (_input, init = {}) => {
        await new Promise((_, reject) => {
          init.signal.addEventListener("abort", () => {
            sawAbort = true;
            reject(new DOMException("Aborted", "AbortError"));
          });
        });
      },
    });
    await expect(client.get("/v1/slow")).rejects.toBeInstanceOf(NetworkError);
    expect(sawAbort).toBe(true);
  });

  test("requestInterceptor failure wraps as NetworkError and reports completion", async () => {
    const completions = [];
    let fetchCalled = false;
    const client = new HttpClient({
      baseUrl: "https://api.example.com",
      onRequestComplete: (info) => completions.push(info),
      requestInterceptor: () => {
        throw new Error("interceptor exploded");
      },
      fetch: async () => {
        fetchCalled = true;
        return jsonResponse(envelope({ ok: true }));
      },
    });

    try {
      await client.get("/v1/x");
      throw new Error("expected throw");
    } catch (err) {
      expect(err).toBeInstanceOf(NetworkError);
      expect(err.message).toBe("Request error: interceptor exploded");
    }
    expect(fetchCalled).toBe(false);
    expect(completions).toHaveLength(1);
    expect(completions[0].error).toBe(true);
    expect(completions[0].statusCode).toBe(0);
  });

  test("getToken failure wraps as NetworkError and reports completion", async () => {
    const completions = [];
    const client = new HttpClient({
      baseUrl: "https://api.example.com",
      onRequestComplete: (info) => completions.push(info),
      auth: {
        getToken: async () => {
          throw new Error("token store down");
        },
      },
      fetch: async () => jsonResponse(envelope({ ok: true })),
    });

    try {
      await client.get("/v1/x");
      throw new Error("expected throw");
    } catch (err) {
      expect(err).toBeInstanceOf(NetworkError);
      expect(err.message).toBe("Request error: token store down");
    }
    expect(completions).toHaveLength(1);
    expect(completions[0].error).toBe(true);
    expect(completions[0].statusCode).toBe(0);
  });

  test("GET with a body throws instead of dropping it", async () => {
    const completions = [];
    let fetchCalled = false;
    const client = new HttpClient({
      baseUrl: "https://api.example.com",
      onRequestComplete: (info) => completions.push(info),
      fetch: async () => {
        fetchCalled = true;
        return jsonResponse(envelope({ ok: true }));
      },
    });

    try {
      await client.get("/v1/x", { data: { search: "term" } });
      throw new Error("expected throw");
    } catch (err) {
      expect(err).toBeInstanceOf(NetworkError);
      expect(err.message).toMatch(/GET requests cannot carry a body/);
    }
    expect(fetchCalled).toBe(false);
    expect(completions).toHaveLength(1);
    expect(completions[0].error).toBe(true);
  });

  test("requestInterceptor can mutate headers before transport", async () => {
    const client = new HttpClient({
      baseUrl: "https://api.example.com",
      requestInterceptor: (cfg) => {
        cfg.headers = { ...cfg.headers, "X-Intercepted": "1" };
        return cfg;
      },
      fetch: async (_input, init = {}) => {
        calls.push(init.headers);
        return jsonResponse(envelope({ ok: true }));
      },
    });
    await client.get("/v1/x");
    expect(calls[0]["X-Intercepted"]).toBe("1");
  });

  test("401 refresh retry sends exactly one Authorization header", async () => {
    let attempt = 0;
    const client = new HttpClient({
      baseUrl: "https://api.example.com",
      auth: {
        token: "stale-static",
        refreshToken: async () => "fresh-token",
      },
      fetch: async (_input, init = {}) => {
        attempt += 1;
        calls.push(init.headers);
        if (attempt === 1) {
          return jsonResponse({ title: "unauthorized", detail: "expired" }, 401);
        }
        return jsonResponse(envelope({ ok: true }));
      },
    });

    // The lowercase caller header wins over the static auth.token, and must not
    // survive the retry next to the refreshed one: fetch would join both into a
    // single comma-separated bearer value and the server would 401 again.
    await expect(
      client.get("/v1/x", { headers: { authorization: "Bearer stale" } })
    ).resolves.toEqual({ ok: true });
    expect(attempt).toBe(2);
    expect(calls[0].authorization).toBe("Bearer stale");

    const retryAuthKeys = Object.keys(calls[1]).filter(
      (k) => k.toLowerCase() === "authorization"
    );
    expect(retryAuthKeys).toEqual(["Authorization"]);
    expect(calls[1].Authorization).toBe("Bearer fresh-token");
  });

  test("401 refresh retry preserves requestInterceptor headers and duration", async () => {
    let attempt = 0;
    const completions = [];
    const started = performance.now();
    const client = new HttpClient({
      baseUrl: "https://api.example.com",
      auth: {
        token: "stale",
        refreshToken: async () => {
          await new Promise((r) => setTimeout(r, 25));
          return "fresh-token";
        },
      },
      requestInterceptor: (cfg) => {
        cfg.headers = { ...cfg.headers, "X-From-Interceptor": "1" };
        return cfg;
      },
      onRequestComplete: (info) => completions.push(info),
      fetch: async (_input, init = {}) => {
        attempt += 1;
        calls.push({
          auth: init.headers?.Authorization,
          intercepted: init.headers?.["X-From-Interceptor"],
        });
        if (attempt === 1) {
          return jsonResponse({ title: "unauthorized" }, 401);
        }
        return jsonResponse(envelope({ ok: true }));
      },
    });

    await expect(client.get("/v1/x")).resolves.toEqual({ ok: true });
    expect(attempt).toBe(2);
    expect(calls[0].auth).toBe("Bearer stale");
    expect(calls[1].auth).toBe("Bearer fresh-token");
    expect(calls[1].intercepted).toBe("1");
    expect(completions).toHaveLength(1);
    expect(completions[0].error).toBe(false);
    expect(completions[0].durationMs).toBeGreaterThanOrEqual(20);
    expect(completions[0].durationMs).toBeLessThan(performance.now() - started + 50);
  });

  test("multipart FormData drops content-type regardless of header casing", async () => {
    const form = new FormData();
    form.append("file", new Blob(["hi"]), "a.txt");
    const client = new HttpClient({
      baseUrl: "https://api.example.com",
      fetch: async (_input, init = {}) => {
        calls.push(init.headers);
        return jsonResponse(envelope({ uploaded: true }));
      },
    });

    await client.postMultipart("/v1/upload", form, {
      headers: { "content-type": "multipart/form-data" },
    });

    const sent = calls[0];
    const contentTypeKeys = Object.keys(sent).filter(
      (k) => k.toLowerCase() === "content-type"
    );
    expect(contentTypeKeys).toHaveLength(0);
  });

  test("SDK contract error wraps as NetworkError and reports completion", async () => {
    const completions = [];
    const client = new HttpClient({
      baseUrl: "https://api.example.com",
      onRequestComplete: (info) => completions.push(info),
      fetch: async () => jsonResponse({ not: "an envelope" }),
    });

    try {
      await client.get("/v1/x");
      throw new Error("expected throw");
    } catch (err) {
      expect(err).toBeInstanceOf(NetworkError);
      expect(err.message).toMatch(/^Request error: SDK contract error:/);
    }
    expect(completions).toHaveLength(1);
    expect(completions[0].error).toBe(true);
    expect(completions[0].statusCode).toBe(200);
  });

  test("already-aborted signal rejects without calling fetch", async () => {
    const controller = new AbortController();
    controller.abort();
    let fetchCalled = false;
    const client = new HttpClient({
      baseUrl: "https://api.example.com",
      fetch: async () => {
        fetchCalled = true;
        return jsonResponse(envelope({ ok: true }));
      },
    });
    await expect(
      client.get("/v1/x", { signal: controller.signal })
    ).rejects.toBeInstanceOf(NetworkError);
    expect(fetchCalled).toBe(false);
  });

  test("timeout 0 disables timeout arm while caller signal still works", async () => {
    const controller = new AbortController();
    let fetchSignal;
    const client = new HttpClient({
      baseUrl: "https://api.example.com",
      timeout: 0,
      fetch: async (_input, init = {}) => {
        fetchSignal = init.signal;
        await new Promise((_, reject) => {
          init.signal.addEventListener("abort", () => {
            reject(new DOMException("Aborted", "AbortError"));
          });
        });
      },
    });
    const requestPromise = client.get("/v1/x", { signal: controller.signal });
    await new Promise((resolve) => setTimeout(resolve, 30));
    expect(fetchSignal.aborted).toBe(false);
    controller.abort();
    await expect(requestPromise).rejects.toBeInstanceOf(NetworkError);
  });

  test("RateLimitError reads Retry-After case-insensitively", async () => {
    const { RateLimitError } = await import(
      pathToFileURL(path.join(sdkDir, "types.ts")).href
    );
    const client = new HttpClient({
      baseUrl: "https://api.example.com",
      fetch: async () =>
        new Response(JSON.stringify({ detail: "slow down" }), {
          status: 429,
          statusText: "Too Many Requests",
          headers: { "Retry-After": "7", "Content-Type": "application/json" },
        }),
    });
    try {
      await client.get("/v1/x");
      throw new Error("expected throw");
    } catch (err) {
      expect(err).toBeInstanceOf(RateLimitError);
      expect(err.retryAfter).toBe(7);
    }
  });

  test("responseInterceptor runs before envelope unwrap", async () => {
    let sawMeta = false;
    const client = new HttpClient({
      baseUrl: "https://api.example.com",
      responseInterceptor: (res) => {
        sawMeta = res.data?.meta?.requestId === "rid-1";
        return res;
      },
      fetch: async () => jsonResponse(envelope({ ok: true }, "rid-1")),
    });
    await expect(client.get("/v1/x")).resolves.toEqual({ ok: true });
    expect(sawMeta).toBe(true);
  });
});
