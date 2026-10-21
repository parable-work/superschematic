/*
OpenTelemetry trace export for a stack's TypeScript server, job or worker
on Bun (D67; section 8.6 of docs/stack-model.md), the runtime's
`./telemetry` entry. The generated main.ts calls startTelemetry once with
what it knows of its deployable, and everything else comes from
OpenTelemetry's own environment variables:

- OTEL_EXPORTER_OTLP_ENDPOINT, or OTEL_EXPORTER_OTLP_TRACES_ENDPOINT, turns
  export on. Without either, startTelemetry sets nothing up: no exporter,
  no context manager, no background work, and the global tracer stays a
  no-op, so the middleware, fetch, job and message it returns only call
  through.
- OTEL_EXPORTER_OTLP_HEADERS, OTEL_EXPORTER_OTLP_TIMEOUT and their
  per-signal forms reach the exporter, which reads them itself.
- An endpoint under googleapis.com, Cloud Trace's OTLP endpoint, gets the
  access token of the workload's account from the metadata server on each
  export (GCE_METADATA_HOST when set, else metadata.google.internal),
  unless a headers variable names an authorization header (D67, amended).
- OTEL_EXPORTER_OTLP_PROTOCOL, and its traces form, may only name
  http/protobuf, the one protocol the runtime exports over.
- OTEL_SERVICE_NAME and OTEL_RESOURCE_ATTRIBUTES override the defaults the
  entrypoint gives.
- OTEL_TRACES_SAMPLER and OTEL_TRACES_SAMPLER_ARG pick the sampler,
  parentbased_always_on unless set.
- OTEL_PROPAGATORS picks the propagators, tracecontext and baggage unless
  set.
- OTEL_SDK_DISABLED=true turns export off whatever else is set.

A value the runtime does not support throws an error that names the
variable, as a refused config field stops a server.

This entry alone imports OpenTelemetry's packages, which a Bun server,
job and worker depend on, so the runtime's other entries bundle without
them. The context of a span reaches the code a request runs through
AsyncLocalStorage, which Bun implements.
*/

import {
  context,
  diag,
  DiagLogLevel,
  propagation,
  ROOT_CONTEXT,
  SpanKind,
  SpanStatusCode,
  trace,
  type Attributes,
  type Span,
  type TextMapGetter,
  type TextMapPropagator,
  type TextMapSetter,
} from '@opentelemetry/api';
import { AsyncLocalStorageContextManager } from '@opentelemetry/context-async-hooks';
import { CompositePropagator, W3CBaggagePropagator, W3CTraceContextPropagator } from '@opentelemetry/core';
import { OTLPTraceExporter } from '@opentelemetry/exporter-trace-otlp-proto';
import { defaultResource, detectResources, envDetector, resourceFromAttributes } from '@opentelemetry/resources';
import {
  AlwaysOffSampler,
  AlwaysOnSampler,
  BatchSpanProcessor,
  ParentBasedSampler,
  TraceIdRatioBasedSampler,
  TracerProvider,
  type Sampler,
} from '@opentelemetry/sdk-trace';
import type { Context as HonoContext, MiddlewareHandler } from 'hono';
import { matchedRoutes } from 'hono/route';
import { googleAccessTokenSource, type GoogleIdTokenSourceOptions } from './credentials.js';
import type { Logger } from './logger.js';

/** The one OTLP protocol the runtime exports over. */
export const OTLP_PROTOCOL = 'http/protobuf';

/** The resource attribute that names the deployable's kind. */
export const KIND_ATTRIBUTE = 'superschematic.deployable.kind';

/** The span attribute that names the job a job's span runs. */
export const JOB_ATTRIBUTE = 'superschematic.job';

/** The instrumentation scope of the runtime's spans. */
const SCOPE = '@superschematic/http-runtime/telemetry';

/** How long shutdown waits for the spans still buffered to reach the collector. */
const FLUSH_TIMEOUT_MS = 5_000;

/** The paths the middleware makes no span for: the platform's probes. */
const HEALTH_PATHS = new Set(['/healthz', '/readyz']);

/** What the generated entrypoint knows of its deployable: its resource's defaults. */
export interface TelemetryDefaults {
  /** The deployable's name, the default service.name. */
  readonly service: string;
  /** The stack's name, service.namespace. */
  readonly stack?: string;
  /** The deployable's kind. */
  readonly kind?: 'server' | 'job' | 'worker';
  /** Logs that export is on and what the SDK reports, such as an export that failed. */
  readonly logger?: Logger;
}

/** A worker's message, as the claim loop hands it to a handler. */
export interface TracedMessage {
  readonly id: string;
  readonly attempt: number;
}

/** What startTelemetry returns: the hooks the entrypoint wires, and the flush. */
export interface Telemetry {
  /** Whether export is on. */
  readonly enabled: boolean;
  /**
   * Hono middleware, registered first: a server span per request but
   * /healthz and /readyz, the child of an incoming traceparent, named by
   * its method and the route template that took it.
   */
  middleware(): MiddlewareHandler;
  /**
   * The fetch of a calls edge's SDK client: a client span per call, whose
   * context the call carries in traceparent and tracestate.
   */
  readonly fetch: (input: RequestInfo | URL, init?: RequestInit) => Promise<Response>;
  /** Runs a job's method in a span named after its @job class. */
  job<T>(name: string, run: () => Promise<T>): Promise<T>;
  /** Runs a worker's handler on a message of queue in a span: process <queue>. */
  message<T>(queue: string, message: TracedMessage, handle: () => Promise<T>): Promise<T>;
  /** Flushes the spans still buffered and stops the exporter; with export off it does nothing. */
  shutdown(): Promise<void>;
}

const headerGetter: TextMapGetter<Headers> = {
  keys: headers => [...headers.keys()],
  get: (headers, key) => headers.get(key) ?? undefined,
};

const headerSetter: TextMapSetter<Headers> = {
  set: (headers, key, value) => headers.set(key, value),
};

/**
 * Sets up export from the environment, as the module says. It throws when a
 * variable holds a value the runtime does not support.
 */
export function startTelemetry(defaults: TelemetryDefaults): Telemetry {
  const config = readConfig(process.env);
  if (!config.on) return off;
  const logger = defaults.logger;
  if (logger) {
    diag.setLogger(
      {
        error: (msg, ...args) => logger.warn('telemetry', { error: [msg, ...args.map(String)].join(' ') }),
        warn: (msg, ...args) => logger.warn('telemetry', { error: [msg, ...args.map(String)].join(' ') }),
        info: () => undefined,
        debug: () => undefined,
        verbose: () => undefined,
      },
      DiagLogLevel.WARN
    );
  }
  const attributes: Attributes = { 'service.name': defaults.service };
  if (defaults.stack) attributes['service.namespace'] = defaults.stack;
  if (defaults.kind) attributes[KIND_ATTRIBUTE] = defaults.kind;
  const resource = defaultResource()
    .merge(resourceFromAttributes(attributes))
    .merge(detectResources({ detectors: [envDetector] }));
  const google = googleAuth(config.endpoint, process.env);
  const provider = new TracerProvider({
    resource,
    sampler: config.sampler,
    spanProcessors: [new BatchSpanProcessor({ exporter: new OTLPTraceExporter(google ? { headers: googleAuthHeaders({}, logger) } : {}) })],
  });
  const contextManager = new AsyncLocalStorageContextManager().enable();
  context.setGlobalContextManager(contextManager);
  propagation.setGlobalPropagator(config.propagator);
  trace.setGlobalTracerProvider(provider);
  logger?.info('exporting traces', { endpoint: config.endpoint, protocol: OTLP_PROTOCOL, googleAuth: google });
  const tracer = () => trace.getTracer(SCOPE);
  const baseFetch = globalThis.fetch.bind(globalThis);
  let stopped = false;
  return {
    enabled: true,
    middleware: () => async (c, next) => {
      if (HEALTH_PATHS.has(c.req.path)) return next();
      const method = c.req.method;
      const parent = propagation.extract(ROOT_CONTEXT, c.req.raw.headers, headerGetter);
      const span = tracer().startSpan(
        method,
        { kind: SpanKind.SERVER, attributes: { 'http.request.method': method, 'url.path': c.req.path, 'url.scheme': new URL(c.req.url).protocol.replace(/:$/u, '') } },
        parent
      );
      try {
        await context.with(trace.setSpan(parent, span), next);
      } catch (error) {
        failed(span, error);
        throw error;
      } finally {
        const route = routeOf(c);
        if (route) {
          span.updateName(`${method} ${route}`);
          span.setAttribute('http.route', route);
        }
        const status = c.res?.status;
        if (status !== undefined) {
          span.setAttribute('http.response.status_code', status);
          if (status >= 500) span.setStatus({ code: SpanStatusCode.ERROR });
        }
        span.end();
      }
    },
    fetch: async (input, init) => {
      const request = input instanceof Request ? input : undefined;
      const method = (init?.method ?? request?.method ?? 'GET').toUpperCase();
      const url = new URL(request?.url ?? (input instanceof URL ? input.href : String(input)));
      const span = tracer().startSpan(method, {
        kind: SpanKind.CLIENT,
        attributes: {
          'http.request.method': method,
          'url.full': url.href,
          'server.address': url.hostname,
          ...(url.port ? { 'server.port': Number(url.port) } : {}),
        },
      });
      const headers = new Headers(init?.headers ?? request?.headers);
      propagation.inject(trace.setSpan(context.active(), span), headers, headerSetter);
      try {
        const response = await baseFetch(input, { ...init, headers });
        span.setAttribute('http.response.status_code', response.status);
        if (response.status >= 500) span.setStatus({ code: SpanStatusCode.ERROR });
        return response;
      } catch (error) {
        failed(span, error);
        throw error;
      } finally {
        span.end();
      }
    },
    job: (name, run) =>
      tracer().startActiveSpan(name, { kind: SpanKind.INTERNAL, attributes: { [JOB_ATTRIBUTE]: name } }, ROOT_CONTEXT, span => settle(span, run)),
    message: (queue, message, handle) =>
      tracer().startActiveSpan(
        `process ${queue}`,
        {
          kind: SpanKind.CONSUMER,
          attributes: {
            'messaging.system': 'superschematic',
            'messaging.operation.name': 'process',
            'messaging.operation.type': 'process',
            'messaging.destination.name': queue,
            'messaging.message.id': message.id,
            'superschematic.message.attempt': message.attempt,
          },
        },
        ROOT_CONTEXT,
        span => settle(span, handle)
      ),
    shutdown: async () => {
      if (stopped) return;
      stopped = true;
      let timer: ReturnType<typeof setTimeout> | undefined;
      const timedOut = new Promise<void>(resolve => {
        timer = setTimeout(() => {
          logger?.warn('flush the traces', { error: `the collector took more than ${FLUSH_TIMEOUT_MS} ms` });
          resolve();
        }, FLUSH_TIMEOUT_MS);
      });
      try {
        await Promise.race([provider.shutdown(), timedOut]);
      } catch (error) {
        logger?.warn('flush the traces', { error });
      } finally {
        clearTimeout(timer);
        trace.disable();
        propagation.disable();
        context.disable();
      }
    },
  };
}

/** The telemetry of a deployable with export off: every hook only calls through. */
const off: Telemetry = {
  enabled: false,
  middleware: () => (_c, next) => next(),
  fetch: (input, init) => globalThis.fetch(input, init),
  job: (_name, run) => run(),
  message: (_queue, _message, handle) => handle(),
  shutdown: async () => undefined,
};

/** Ends span with run's outcome, recording its error. */
async function settle<T>(span: Span, run: () => Promise<T>): Promise<T> {
  try {
    return await run();
  } catch (error) {
    failed(span, error);
    throw error;
  } finally {
    span.end();
  }
}

function failed(span: Span, error: unknown): void {
  span.recordException(error instanceof Error ? error : String(error));
  span.setStatus({ code: SpanStatusCode.ERROR, message: error instanceof Error ? error.message : String(error) });
}

/**
 * The route template of the handler that answered c, as the other runtimes
 * write it (`/api/orders/{id}`), or undefined when no route took it: Hono's
 * matched route at the last handler that ran, unless it is a middleware.
 */
function routeOf(c: HonoContext): string | undefined {
  let route: { path: string; method: string } | undefined;
  try {
    route = matchedRoutes(c).at(c.req.routeIndex);
  } catch {
    return undefined;
  }
  if (!route || route.method === 'ALL') return undefined;
  return route.path.replace(/:([^/{}?]+)(\{[^/]*\})?\??/gu, '{$1}');
}

/**
 * Whether export to endpoint sends the access token of the workload's
 * account (D67, amended): whether its host is googleapis.com or under it,
 * and neither OTEL_EXPORTER_OTLP_TRACES_HEADERS nor
 * OTEL_EXPORTER_OTLP_HEADERS names an authorization header.
 */
export function googleAuth(endpoint: string, env: Readonly<Record<string, string | undefined>>): boolean {
  let host: string;
  try {
    host = new URL(endpoint).hostname.toLowerCase();
  } catch {
    return false;
  }
  if (host !== 'googleapis.com' && !host.endsWith('.googleapis.com')) return false;
  for (const name of ['OTEL_EXPORTER_OTLP_TRACES_HEADERS', 'OTEL_EXPORTER_OTLP_HEADERS']) {
    for (const pair of (env[name] ?? '').split(',')) {
      let key = pair.split('=')[0]?.trim() ?? '';
      try {
        key = decodeURIComponent(key);
      } catch {
        // A key that does not decode is no authorization header.
      }
      if (key.toLowerCase() === 'authorization') return false;
    }
  }
  return true;
}

/**
 * The exporter's headers for Google's OTLP endpoint: Authorization with
 * the account's access token. The exporter may not be thrown at, so a
 * token the metadata server does not give is logged, and the export goes
 * without one, which the endpoint refuses and the SDK reports.
 */
export function googleAuthHeaders(options: GoogleIdTokenSourceOptions, logger?: Logger): () => Promise<Record<string, string>> {
  const token = googleAccessTokenSource(options);
  return async (): Promise<Record<string, string>> => {
    try {
      return { Authorization: `Bearer ${await token(false)}` };
    } catch (error) {
      logger?.warn('telemetry', { error: `the access token for the OTLP endpoint: ${error instanceof Error ? error.message : String(error)}` });
      return {};
    }
  };
}

interface Config {
  readonly on: boolean;
  readonly endpoint: string;
  readonly sampler: Sampler;
  readonly propagator: TextMapPropagator;
}

/** Reads the variables the module lists, and refuses what the runtime does not support. */
function readConfig(env: Readonly<Record<string, string | undefined>>): Config {
  const get = (name: string): string => (env[name] ?? '').trim();
  let endpointVariable = 'OTEL_EXPORTER_OTLP_TRACES_ENDPOINT';
  let endpoint = get(endpointVariable);
  if (endpoint === '') {
    endpointVariable = 'OTEL_EXPORTER_OTLP_ENDPOINT';
    endpoint = get(endpointVariable);
  }
  const disabled = get('OTEL_SDK_DISABLED').toLowerCase() === 'true';
  if (endpoint === '' || disabled) {
    return { on: false, endpoint, sampler: new AlwaysOffSampler(), propagator: new CompositePropagator() };
  }
  const problems: string[] = [];
  let url: URL | undefined;
  try {
    url = new URL(endpoint);
  } catch {
    url = undefined;
  }
  if (!url || (url.protocol !== 'http:' && url.protocol !== 'https:') || url.host === '') {
    problems.push(`${endpointVariable} is ${JSON.stringify(endpoint)}: it is no http or https URL`);
  }
  for (const name of ['OTEL_EXPORTER_OTLP_TRACES_PROTOCOL', 'OTEL_EXPORTER_OTLP_PROTOCOL']) {
    const value = get(name);
    if (value !== '' && value !== OTLP_PROTOCOL) {
      problems.push(`${name} is ${JSON.stringify(value)}; the runtime exports over ${OTLP_PROTOCOL} alone`);
    }
  }
  const sampler = readSampler(get, problems);
  const propagator = readPropagators(get('OTEL_PROPAGATORS'), problems);
  if (problems.length > 0) throw new Error(problems.join('; '));
  return { on: true, endpoint, sampler, propagator };
}

function readSampler(get: (name: string) => string, problems: string[]): Sampler {
  const name = get('OTEL_TRACES_SAMPLER');
  const ratio = (): number => {
    const arg = get('OTEL_TRACES_SAMPLER_ARG');
    if (arg === '') return 1;
    const r = Number(arg);
    if (!Number.isFinite(r) || r < 0 || r > 1) {
      problems.push(`OTEL_TRACES_SAMPLER_ARG is ${JSON.stringify(arg)}; it is a number from 0 to 1`);
      return 1;
    }
    return r;
  };
  switch (name) {
    case '':
    case 'parentbased_always_on':
      return new ParentBasedSampler({ root: new AlwaysOnSampler() });
    case 'always_on':
      return new AlwaysOnSampler();
    case 'always_off':
      return new AlwaysOffSampler();
    case 'parentbased_always_off':
      return new ParentBasedSampler({ root: new AlwaysOffSampler() });
    case 'traceidratio':
      return new TraceIdRatioBasedSampler(ratio());
    case 'parentbased_traceidratio':
      return new ParentBasedSampler({ root: new TraceIdRatioBasedSampler(ratio()) });
  }
  problems.push(
    `OTEL_TRACES_SAMPLER is ${JSON.stringify(name)}; the runtime samples with always_on, always_off, traceidratio, parentbased_always_on, parentbased_always_off or parentbased_traceidratio`
  );
  return new AlwaysOffSampler();
}

function readPropagators(value: string, problems: string[]): TextMapPropagator {
  const names = [...new Set((value === '' ? 'tracecontext,baggage' : value).split(',').map(name => name.trim()).filter(name => name !== ''))];
  const propagators: TextMapPropagator[] = [];
  for (const name of names) {
    if (name === 'tracecontext') propagators.push(new W3CTraceContextPropagator());
    else if (name === 'baggage') propagators.push(new W3CBaggagePropagator());
    else if (name !== 'none') problems.push(`OTEL_PROPAGATORS names ${JSON.stringify(name)}; the runtime propagates tracecontext and baggage, or none`);
  }
  return new CompositePropagator({ propagators: names.includes('none') ? [] : propagators });
}
