import { describe, expect, test } from "bun:test";
import { pathToFileURL } from "node:url";
import path from "node:path";

// Runs the HttpClient of an SDK with end-user auth against an injected
// fetch: the service credential (D37), its 401 rule and the forwarded end
// user. SDK_DIR is the SDK package.
const sdkDir = process.env.SDK_DIR;
if (!sdkDir) {
  throw new Error("SDK_DIR env var required");
}

const { HttpClient, toRequestOptions } = await import(
  pathToFileURL(path.join(sdkDir, "client.ts")).href
);
const { AuthenticationError } = await import(
  pathToFileURL(path.join(sdkDir, "types.ts")).href
);

function envelope(data) {
  return { data, meta: { requestId: "req-1" } };
}

function jsonResponse(body, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

const serviceRefusal = {
  type: "about:blank",
  title: "Unauthorized",
  status: 401,
  detail: "Invalid service credential",
  code: "service_unauthorized",
};
const userRefusal = {
  type: "about:blank",
  title: "Unauthorized",
  status: 401,
  detail: "Authentication required",
  code: "unauthorized",
};
// The Rust server's error envelope (D29).
const rustServiceRefusal = {
  error: { code: "service_unauthorized", message: "Invalid service credential" },
};

// clientWith returns a client whose fetch answers each request with the next
// of responses, after recording its headers, and whose service credential
// source and end-user refresh record their calls.
function clientWith(responses, { auth, serviceHeaders, completions } = {}) {
  const requests = [];
  const tokenCalls = [];
  const refreshCalls = [];
  let issued = 0;
  const client = new HttpClient({
    baseUrl: "https://api.example.com",
    auth: auth && {
      ...auth,
      refreshToken: async () => {
        refreshCalls.push(true);
        return "user-refreshed";
      },
    },
    serviceCredential: {
      token: async (fresh) => {
        tokenCalls.push(fresh);
        issued += 1;
        return `service-${issued}`;
      },
      headers: serviceHeaders,
    },
    onRequestComplete: completions && ((info) => completions.push(info)),
    fetch: async (_input, init = {}) => {
      requests.push(init.headers);
      const next = responses[requests.length - 1];
      if (!next) {
        throw new Error(`unexpected request ${requests.length}`);
      }
      return jsonResponse(next.body, next.status);
    },
  });
  return { client, requests, tokenCalls, refreshCalls };
}

const ok = { status: 200, body: envelope({ ok: true }) };

async function authenticationError(promise) {
  try {
    await promise;
  } catch (err) {
    expect(err).toBeInstanceOf(AuthenticationError);
    return err;
  }
  throw new Error("expected an AuthenticationError");
}

describe("service credential", () => {
  test("every request carries it in Service-Authorization by default", async () => {
    const { client, requests, tokenCalls } = clientWith([ok, ok], { auth: { token: "alice" } });
    await client.get("/v1/x");
    await client.post("/v1/y", { a: 1 });
    expect(requests.map((h) => h["Service-Authorization"])).toEqual(["Bearer service-1", "Bearer service-2"]);
    expect(requests.map((h) => h.Authorization)).toEqual(["Bearer alice", "Bearer alice"]);
    expect(tokenCalls).toEqual([false, false]);
  });

  test("it is sent in each configured header", async () => {
    const { client, requests } = clientWith([ok], {
      serviceHeaders: ["Service-Authorization", "X-Serverless-Authorization"],
    });
    await client.get("/v1/x");
    expect(requests[0]["Service-Authorization"]).toBe("Bearer service-1");
    expect(requests[0]["X-Serverless-Authorization"]).toBe("Bearer service-1");
    expect(requests[0].Authorization).toBeUndefined();
  });

  test("a service_unauthorized 401 asks for a fresh token once and does not refresh the user", async () => {
    const { client, requests, tokenCalls, refreshCalls } = clientWith(
      [{ status: 401, body: serviceRefusal }, ok],
      { auth: { token: "alice" } }
    );
    await expect(client.get("/v1/x")).resolves.toEqual({ ok: true });
    expect(tokenCalls).toEqual([false, true]);
    expect(refreshCalls).toHaveLength(0);
    expect(requests.map((h) => h["Service-Authorization"])).toEqual(["Bearer service-1", "Bearer service-2"]);
    expect(requests[1].Authorization).toBe("Bearer alice");
  });

  test("the Rust envelope's error.code is read too", async () => {
    const { client, tokenCalls, refreshCalls } = clientWith(
      [{ status: 401, body: rustServiceRefusal }, ok],
      { auth: { token: "alice" } }
    );
    await expect(client.get("/v1/x")).resolves.toEqual({ ok: true });
    expect(tokenCalls).toEqual([false, true]);
    expect(refreshCalls).toHaveLength(0);
  });

  test("a second service_unauthorized 401 is the caller's error", async () => {
    const completions = [];
    const { client, requests, tokenCalls, refreshCalls } = clientWith(
      [{ status: 401, body: serviceRefusal }, { status: 401, body: serviceRefusal }],
      { auth: { token: "alice" }, completions }
    );
    const err = await authenticationError(client.get("/v1/x"));
    expect(err.code).toBe("service_unauthorized");
    expect(requests).toHaveLength(2);
    expect(tokenCalls).toEqual([false, true]);
    expect(refreshCalls).toHaveLength(0);
    expect(completions.map((c) => c.statusCode)).toEqual([401]);
  });

  test("an end-user 401 refreshes the user and never asks for a fresh service token", async () => {
    const { client, requests, tokenCalls, refreshCalls } = clientWith(
      [{ status: 401, body: userRefusal }, ok],
      { auth: { token: "alice" } }
    );
    await expect(client.get("/v1/x")).resolves.toEqual({ ok: true });
    expect(refreshCalls).toHaveLength(1);
    expect(tokenCalls).toEqual([false, false]);
    expect(requests[1].Authorization).toBe("Bearer user-refreshed");
  });

  test("a 401 without a code is an end-user 401", async () => {
    const { client, tokenCalls, refreshCalls } = clientWith(
      [{ status: 401, body: { title: "Unauthorized" } }, ok],
      { auth: { token: "alice" } }
    );
    await expect(client.get("/v1/x")).resolves.toEqual({ ok: true });
    expect(refreshCalls).toHaveLength(1);
    expect(tokenCalls).toEqual([false, false]);
  });

  test("one call retries the service credential once and refreshes the user once", async () => {
    const { client, requests, tokenCalls, refreshCalls } = clientWith(
      [
        { status: 401, body: serviceRefusal },
        { status: 401, body: userRefusal },
        { status: 401, body: serviceRefusal },
      ],
      { auth: { token: "alice" } }
    );
    const err = await authenticationError(client.get("/v1/x"));
    expect(err.code).toBe("service_unauthorized");
    expect(requests).toHaveLength(3);
    expect(tokenCalls).toEqual([false, true, false]);
    expect(refreshCalls).toHaveLength(1);
  });

  test("without a service credential a service_unauthorized 401 does not refresh the user", async () => {
    const refreshCalls = [];
    let attempts = 0;
    const client = new HttpClient({
      baseUrl: "https://api.example.com",
      auth: {
        token: "alice",
        refreshToken: async () => {
          refreshCalls.push(true);
          return "user-refreshed";
        },
      },
      fetch: async () => {
        attempts += 1;
        return jsonResponse(serviceRefusal, 401);
      },
    });
    await authenticationError(client.get("/v1/x"));
    expect(attempts).toBe(1);
    expect(refreshCalls).toHaveLength(0);
  });
});

describe("forwarding the end user", () => {
  test("forward sends the forwarded token instead of the configured one", async () => {
    const { client, requests } = clientWith([ok], { auth: { token: "client-own" } });
    await client.get("/v1/x", { forward: { bearerToken: "alice" } });
    expect(requests[0].Authorization).toBe("Bearer alice");
    expect(requests[0]["Service-Authorization"]).toBe("Bearer service-1");
  });

  test("forward with no end user sends no Authorization", async () => {
    const { client, requests } = clientWith([ok, ok], { auth: { token: "client-own" } });
    await client.get("/v1/x", { forward: { bearerToken: null } });
    await client.get("/v1/x", { forward: {} });
    expect(requests.map((h) => h.Authorization)).toEqual([undefined, undefined]);
  });

  test("a forwarded call does not refresh on an end-user 401", async () => {
    const { client, requests, refreshCalls } = clientWith(
      [{ status: 401, body: userRefusal }],
      { auth: { token: "client-own" } }
    );
    const err = await authenticationError(client.get("/v1/x", { forward: { bearerToken: "alice" } }));
    expect(err.code).toBe("unauthorized");
    expect(requests).toHaveLength(1);
    expect(refreshCalls).toHaveLength(0);
  });

  test("a forwarded call keeps forwarding on its service credential retry", async () => {
    const { client, requests, tokenCalls } = clientWith(
      [{ status: 401, body: serviceRefusal }, ok],
      { auth: { token: "client-own" } }
    );
    await client.get("/v1/x", { forward: { bearerToken: "alice" } });
    expect(tokenCalls).toEqual([false, true]);
    expect(requests.map((h) => h.Authorization)).toEqual(["Bearer alice", "Bearer alice"]);
  });

  test("toRequestOptions reads an AbortSignal or RequestOptions", () => {
    const controller = new AbortController();
    const forward = { bearerToken: "alice" };
    expect(toRequestOptions(undefined)).toEqual({});
    expect(toRequestOptions(controller.signal)).toEqual({ signal: controller.signal });
    expect(toRequestOptions({ signal: controller.signal, forward })).toEqual({
      signal: controller.signal,
      forward,
    });
    expect(toRequestOptions({ forward })).toEqual({ signal: undefined, forward });
  });
});
