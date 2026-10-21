/*
engineApp and engineMcp on the Rust engine (D58): thin Hono adapters over
the engine's own routes (api::handle). The engine owns routing, decoding,
the media types, ETags and If-Match, the Preconditions header, the
envelope and the problem documents, Location, the D37 stand-in rule, the
event stream's every byte and MCP's JSON-RPC; these adapters keep what the
host keeps: the HTTP runtime's request id, rate limit, timeout, body limit
and authentication gate, the identity service's CORS and its /auth routes,
the mount's tool filter and its onError.

Every route of routes() mounts through the runtime's mountManualOperation
(engineApp the API's, engineMcp the MCP endpoint's), and its handler hands
the request to the addon: its method, the prefix it is mounted under, its
path and query as received, its headers, its body's bytes, and the caller
the runtime established. The answer's body comes back as bytes, or as an
event stream the engine holds open, which a ReadableStream pulls on this
thread: a pull that finds nothing waits for a commit (events.watch, whose
commits from elsewhere arrive through a weak threadsafe function) or the
heartbeat, and pulls again. A stream route's timeout covers its first page
alone, since the handler returns once the stream starts.

An engine a server opens in the background (openEngineInBackground, the
./server entry) is served from the start: until it opens, every route
answers 503 unavailable ("The engine is not available") before the
runtime's steps, after the identity service's CORS, as the Go binding's
enginehttp.Background answers, and once it opens, the open engine serves
them. A host that stops serving aborts the mount's done signal, which ends
every event stream, so its shutdown waits for none.

A tool filter written in TypeScript (the mount's tools option) is asked on
this thread, during the request. A behavior written in TypeScript that
throws inside a request answers 500, as on the TypeScript engine; the
mount's onError is asked about what it threw first.
*/

import { readFileSync } from 'node:fs';

import { HttpProblem, problemResponse, requestIdOf, type OperationSpec, type ParamSpec, type RequestContext } from '@superschematic/http-runtime';
import {
  DEFAULT_BODY_LIMIT_BYTES,
  disconnectSignal,
  errorHandler,
  honoPath,
  mountManualOperation,
  notFoundHandler,
  type RouterRuntimeOptions,
} from '@superschematic/http-runtime/hono';
import {
  identityCorsRoutes,
  identityOperationSpec,
  identityRouterOptions,
  identityRoutes,
  mountIdentityOperations,
} from '@superschematic/http-runtime/identity';
import { Hono, type Context } from 'hono';

import type { EngineHttpOptions } from '../http/app.js';
import { callerAuthenticator, principalOf } from '../http/callers.js';
import { engineProblem } from '../http/problems.js';
import { checkStreamOptions } from '../http/stream.js';
import type { EngineMcpOptions } from '../mcp/app.js';
import type { ToolFilter } from '../types/tools.js';
import { EngineBackground } from '../server/background.js';
import { loadAddon, type StreamPull } from './addon.js';
import { AsyncEngine } from './async.js';
import type { Engine, EngineRoute } from './engine.js';

/** An engine these adapters serve: the synchronous facade's, openEngineAsync's, or one opening in the background. */
export type ServedEngine = Engine | AsyncEngine | EngineBackground;

/**
 * The routes the engine serves (api::routes), for any engine: those of the
 * addon the path names, else of the one SUPERSCHEMATIC_ENGINE_ADDON names,
 * else of the core's, which are every addon's.
 */
export function engineRoutes(addon?: string): EngineRoute[] {
  return JSON.parse(loadAddon(addon).engineRoutes()) as EngineRoute[];
}

/**
 * The method and path of each of the engine's routes mounted at prefix,
 * its path parameters in braces: what a host's CORS matches the engine's
 * routes by, beside its APIs' operations (the HTTP runtime's
 * matchOperations and corsMethods). addon names the addon, as
 * engineRoutes's does.
 */
export function engineRouteSpecs(prefix: string, addon?: string): { readonly method: string; readonly path: string }[] {
  const base = prefix.replace(/\/+$/u, '');
  return engineRoutes(addon).map((route) => ({ method: route.method, path: base + route.path }));
}

/** What every route answers until a background engine opens. */
export function engineUnavailable(): HttpProblem {
  return new HttpProblem(503, 'The engine is not available', { code: 'unavailable' });
}

// What a route's handler needs of the engine: one request served, the
// event stream pulled and ended, and its commits watched.
interface Serving {
  routes(): EngineRoute[];
  serve(head: Record<string, unknown>, body: Buffer | undefined, filter: ToolFilter | undefined): Promise<{ answer: HttpAnswerLike; thrown?: { value: unknown } }>;
  /** A pull: at once on this thread (sync), or waiting off it up to the heartbeat (async). */
  pull(id: number, heartbeatMs: number): Promise<StreamPull>;
  /** Whether a pull that finds nothing should wait here for a commit or the heartbeat. */
  readonly waitsHere: boolean;
  close(id: number): void;
  watch(wake: () => void): (() => void) | undefined;
  /** Whether the engine serves yet: false until a background engine opens. */
  available(): boolean;
}

interface HttpAnswerLike {
  status: number;
  headers: string[][];
  body?: Buffer | null;
  stream?: number | null;
}

function servingOf(engine: ServedEngine): Serving {
  if (engine instanceof EngineBackground) {
    return backgroundServing(engine);
  }
  if (engine instanceof AsyncEngine) {
    return {
      routes: () => engine.routes(),
      serve: async (head, body, filter) => {
        if (filter !== undefined) {
          throw new TypeError('a tool filter written in TypeScript answers on the JavaScript thread: an engine opened with openEngineAsync serves without one');
        }
        return { answer: await engine.handleHttp(head, body) };
      },
      // The engine waits for a commit or its heartbeat off this thread.
      pull: (id, heartbeatMs) => engine.streamPull(id, Math.min(heartbeatMs, 1000)),
      waitsHere: false,
      close: (id) => engine.streamClose(id),
      watch: () => undefined,
      available: () => true,
    };
  }
  return {
    routes: () => engine.routes(),
    serve: async (head, body, filter) => engine.handleHttp(head, body, filter),
    pull: async (id) => engine.streamPull(id),
    waitsHere: true,
    close: (id) => engine.streamClose(id),
    watch: (wake) => engine.events.watch({ committed: wake, closed: wake }),
    available: () => true,
  };
}

// backgroundServing serves a background engine's routes: 503 unavailable
// until it opens, then the open engine's, through openEngineAsync's serving.
function backgroundServing(background: EngineBackground): Serving {
  let opened: { engine: AsyncEngine; serving: Serving } | undefined;
  const current = (): Serving => {
    const engine = background.current;
    if (engine === undefined) {
      throw engineUnavailable();
    }
    if (opened?.engine !== engine) {
      opened = { engine, serving: servingOf(engine) };
    }
    return opened.serving;
  };
  return {
    routes: () => background.routes(),
    serve: (head, body, filter) => current().serve(head, body, filter),
    pull: (id, heartbeatMs) => current().pull(id, heartbeatMs),
    waitsHere: false,
    close: (id) => {
      try {
        current().close(id);
      } catch {
        // Closed with the engine.
      }
    },
    watch: () => undefined,
    available: () => background.current !== undefined,
  };
}

// The routes whose body the runtime's body limit caps, as the TypeScript
// app's routes that take an input.
const BODY_ROUTES = new Set(['createNamespace', 'defineSchema', 'createInstance', 'updateInstance', 'invokeOperation', 'invokeSchemaOperation', 'searchSchemas']);

const INTEGER_PARAMS = new Set(['version']);

interface Mount {
  readonly routes: readonly EngineRoute[];
  readonly runtime: RouterRuntimeOptions;
  readonly rateLimitPerMinute?: number;
  readonly timeoutSeconds?: number;
  readonly bodyLimitBytes?: number;
  readonly tools?: ToolFilter;
  readonly onError?: RouterRuntimeOptions['onError'];
  /** The engine's own options for the request: stream, mcp. */
  readonly options: Record<string, unknown>;
  readonly heartbeatMs: number;
  /** Aborted when the host stops serving, which ends every event stream. */
  readonly done?: AbortSignal;
}

/** nativeEngineApp is engineApp for an engine on the Rust engine. */
export function nativeEngineApp(engine: ServedEngine, options: EngineHttpOptions = {}): Hono {
  const stream = checkStreamOptions(options.stream ?? {});
  const { identity } = options;
  const deployed = options.onError;
  const runtime: RouterRuntimeOptions = {
    ...options,
    authenticate: callerAuthenticator(identity ? identityRouterOptions(options, 'engineApp').authenticate : options.authenticate),
    onError: async (error, ctx) => engineProblem(error) ?? (deployed ? await deployed(error, ctx) : undefined),
  };
  const app = new Hono();
  const cors = identity ? identityCorsRoutes(app, identity) : () => {};
  const serving = servingOf(engine);
  mountRoutes(app, serving, cors, {
    routes: serving.routes().filter((route) => !route.mcp),
    runtime,
    rateLimitPerMinute: options.rateLimitPerMinute,
    timeoutSeconds: options.timeoutSeconds,
    bodyLimitBytes: options.bodyLimitBytes,
    tools: options.tools,
    onError: deployed,
    options: { stream },
    heartbeatMs: stream.heartbeatMs,
    ...(options.done !== undefined ? { done: options.done } : {}),
  });
  if (identity && options.identityRoutes?.sessions !== false) {
    const administration = options.identityRoutes?.administration ?? identity.store.hasRoles();
    const sets = {
      sessions: { register: options.identityRoutes?.register === true },
      ...(administration ? { administration: {} } : {}),
      namespace: 'engine',
      rateLimitPerMinute: options.rateLimitPerMinute,
      timeoutSeconds: options.timeoutSeconds,
    };
    for (const entry of identityRoutes(identity, sets)) cors(identityOperationSpec(identity, entry, sets));
    mountIdentityOperations(app, identity, sets, runtime);
  }
  app.notFound(notFoundHandler());
  app.onError(errorHandler((error) => engineProblem(error)));
  return app;
}

/** nativeEngineMcp is engineMcp for an engine on the Rust engine: its MCP endpoint, the engine's own. */
export function nativeEngineMcp(engine: ServedEngine, options: EngineMcpOptions = {}): Hono {
  const deployed = options.onError;
  const authenticate = options.identity ? identityRouterOptions(options, 'engineMcp').authenticate : options.authenticate;
  const runtime: RouterRuntimeOptions = {
    ...options,
    authenticate: callerAuthenticator(authenticate),
    onError: async (error, ctx) => engineProblem(error) ?? (deployed ? await deployed(error, ctx) : undefined),
  };
  const app = new Hono();
  const cors = options.identity ? identityCorsRoutes(app, options.identity) : () => {};
  const serverInfo = options.serverInfo ?? packageInfo();
  const serving = servingOf(engine);
  mountRoutes(app, serving, cors, {
    routes: serving.routes().filter((route) => route.mcp),
    runtime,
    rateLimitPerMinute: options.rateLimitPerMinute,
    timeoutSeconds: options.timeoutSeconds,
    tools: options.tools,
    onError: deployed,
    options: { mcp: { serverInfo, ...(options.instructions ? { instructions: options.instructions } : {}) } },
    heartbeatMs: 5_000,
    ...(options.done !== undefined ? { done: options.done } : {}),
  });
  app.notFound(notFoundHandler());
  app.onError(errorHandler((error) => engineProblem(error)));
  return app;
}

function mountRoutes(app: Hono, engine: Serving, cors: (spec: OperationSpec) => void, mount: Mount): void {
  for (const route of mount.routes) {
    const spec: OperationSpec = {
      name: route.name,
      namespace: 'engine',
      method: route.method,
      path: route.path,
      pathParams: [...route.path.matchAll(/\{([^{}]+)\}/gu)].map(
        (match): ParamSpec => ({ name: match[1], kind: INTEGER_PARAMS.has(match[1]) ? 'integer' : 'string', required: true })
      ),
      queryParams: [],
      bodyParams: [],
      auth: { public: false, required: true, permissions: [] },
      manual: true,
      ...(mount.rateLimitPerMinute ? { rateLimitPerMinute: mount.rateLimitPerMinute } : {}),
      ...(mount.timeoutSeconds ? { timeoutSeconds: mount.timeoutSeconds } : {}),
    };
    cors(spec);
    // Until a background engine opens, before the runtime's steps and
    // after the identity service's CORS.
    app.on(route.method, honoPath(route.path), async (c, next) => {
      if (engine.available()) {
        await next();
        return;
      }
      return problemResponse(engineUnavailable(), requestIdOf(c.req.raw));
    });
    const bodyLimitBytes = BODY_ROUTES.has(route.name) ? (mount.bodyLimitBytes ?? DEFAULT_BODY_LIMIT_BYTES) : undefined;
    mountManualOperation(app, spec, (c, ctx) => serve(engine, route, mount, c, ctx), mount.runtime, bodyLimitBytes === undefined ? {} : { bodyLimitBytes });
  }
}

async function serve(engine: Serving, route: EngineRoute, mount: Mount, c: Context, ctx: RequestContext): Promise<Response> {
  const principal = principalOf(ctx);
  const caller =
    principal.service?.standsIn === true
      ? { service: { deployable: principal.service.deployable, serves: principal.service.serves, subject: principal.service.subject } }
      : {
          principal: { subject: principal.subject, permissions: principal.permissions, ...(principal.claims !== undefined ? { claims: principal.claims } : {}) },
          ...(principal.service !== undefined
            ? { service: { deployable: principal.service.deployable, serves: principal.service.serves, subject: principal.service.subject } }
            : {}),
        };
  const url = new URL(c.req.url);
  const parts = url.pathname.split('/');
  const segments = route.path.split('/').length - 1;
  const prefix = parts.slice(0, Math.max(1, parts.length - segments)).join('/');
  const headers: string[][] = [];
  c.req.raw.headers.forEach((value, name) => headers.push([name, value]));
  const body = c.req.method === 'GET' || c.req.method === 'HEAD' ? undefined : Buffer.from(await c.req.arrayBuffer());
  const head = {
    method: c.req.method,
    prefix,
    path: url.pathname,
    query: url.search.startsWith('?') ? url.search.slice(1) : url.search,
    headers,
    caller,
    requestId: ctx.requestId,
    options: mount.options,
  };
  const { answer, thrown } = await engine.serve(head, body !== undefined && body.length > 0 ? body : undefined, mount.tools);
  if (thrown !== undefined && mount.onError !== undefined) {
    // What a behavior written in TypeScript threw: the mount's to map.
    const mapped = await mount.onError(thrown.value, ctx);
    if (mapped instanceof Response) {
      return mapped;
    }
    if (mapped instanceof HttpProblem) {
      return problemResponse(mapped, ctx.requestId);
    }
  }
  const responseHeaders = new Headers();
  for (const [name, value] of answer.headers) {
    responseHeaders.append(name, value);
  }
  if (answer.stream !== undefined && answer.stream !== null) {
    const signal = mount.done === undefined ? disconnectSignal(c) : AbortSignal.any([disconnectSignal(c), mount.done]);
    return new Response(eventBody(engine, answer.stream, mount.heartbeatMs, signal), { status: answer.status, headers: responseHeaders });
  }
  const bytes = answer.body ?? null;
  return new Response(bytes === null || answer.status === 204 || answer.status === 304 ? null : new Uint8Array(bytes), {
    status: answer.status,
    headers: responseHeaders,
  });
}

// eventBody pulls an event stream the engine holds open, on this thread:
// a pull that finds nothing waits for a commit or the heartbeat.
function eventBody(engine: Serving, id: number, heartbeatMs: number, signal: AbortSignal): ReadableStream<Uint8Array> {
  let done = false;
  let wake: (() => void) | undefined;
  // Whether a commit came since the last pull began: one that lands while
  // a pull is in flight, before the stream waits, must not be slept through.
  let woken = false;
  let beatAt: number | undefined;
  let unwatch: (() => void) | undefined;
  const teardown = () => {
    if (done) {
      return;
    }
    done = true;
    unwatch?.();
    signal.removeEventListener('abort', teardown);
    try {
      engine.close(id);
    } catch {
      // Closed with the engine.
    }
    wake?.();
  };
  return new ReadableStream<Uint8Array>({
    start() {
      try {
        unwatch = engine.watch(() => {
          woken = true;
          wake?.();
        });
      } catch {
        // The engine closed: the first pull ends the stream.
      }
      signal.addEventListener('abort', teardown);
      if (signal.aborted) {
        teardown();
      }
    },
    async pull(controller) {
      for (;;) {
        if (done) {
          end(controller);
          return;
        }
        let pulled: StreamPull;
        woken = false;
        try {
          pulled = await engine.pull(id, heartbeatMs);
        } catch {
          teardown();
          end(controller);
          return;
        }
        if (pulled.end) {
          teardown();
          end(controller);
          return;
        }
        if (pulled.data !== undefined && pulled.data !== null) {
          beatAt = undefined;
          controller.enqueue(new Uint8Array(pulled.data));
          return;
        }
        if (!engine.waitsHere || woken) {
          // The pull waited off this thread and found nothing yet, or a
          // commit landed while it pulled: pull again.
          continue;
        }
        // Nothing yet: wait for a commit, or until the engine's heartbeat
        // is due.
        beatAt ??= Date.now() + heartbeatMs;
        const delay = Math.max(1, beatAt - Date.now());
        await new Promise<void>((resolve) => {
          const timer = setTimeout(resolve, delay);
          wake = () => {
            clearTimeout(timer);
            resolve();
          };
        });
        wake = undefined;
      }
    },
    cancel() {
      teardown();
    },
  });
}

// Closing a stream the client already cancelled throws; either way it is over.
function end(controller: ReadableStreamDefaultController<Uint8Array>): void {
  try {
    controller.close();
  } catch {
    // Already closed.
  }
}

function packageInfo(): { name: string; version: string } {
  const manifest = JSON.parse(readFileSync(new URL('../../package.json', import.meta.url), 'utf8')) as { name: string; version: string };
  return { name: manifest.name, version: manifest.version };
}
