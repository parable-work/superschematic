/*
What trace export on Bun (./telemetry) and on Workers (./workers-telemetry)
share (D67, and D64 amended): reading OpenTelemetry's variables, with the
refusals of a value the runtime does not support, and the hooks an
entrypoint wires, the server middleware, a calls edge's fetch, a job's span
and a worker's message span, over a tracer provider the entry builds. It
imports OpenTelemetry's API, core and SDK packages, each of which runs on
Bun and in workerd alike, and nothing from node:, so each entry adds its
own context manager and exporter.
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
  type ContextManager,
  type Span,
  type TextMapGetter,
  type TextMapPropagator,
  type TextMapSetter,
} from '@opentelemetry/api';
import { CompositePropagator, W3CBaggagePropagator, W3CTraceContextPropagator } from '@opentelemetry/core';
import {
  AlwaysOffSampler,
  AlwaysOnSampler,
  ParentBasedSampler,
  TraceIdRatioBasedSampler,
  type Sampler,
  type TracerProvider,
} from '@opentelemetry/sdk-trace';
import type { Context as HonoContext, MiddlewareHandler } from 'hono';
import { matchedRoutes } from 'hono/route';
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
export const FLUSH_TIMEOUT_MS = 5_000;

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

/** A fetch, as an SDK client takes it. */
export type Fetch = (input: RequestInfo | URL, init?: RequestInit) => Promise<Response>;

/** The variables the runtime reads, by name: process.env on Bun, a Worker's string bindings on Workers. */
export type TelemetryEnv = Readonly<Record<string, string | undefined>>;

/** The variables read, and what export is. */
export interface TelemetryConfig {
  readonly on: boolean;
  /** The endpoint variable's value, and which variable it was. */
  readonly endpoint: string;
  readonly endpointVariable: string;
  readonly sampler: Sampler;
  readonly propagator: TextMapPropagator;
}

/** The resource attributes the defaults give, before OTEL_RESOURCE_ATTRIBUTES and OTEL_SERVICE_NAME. */
export function defaultAttributes(defaults: TelemetryDefaults): Attributes {
  const attributes: Attributes = { 'service.name': defaults.service };
  if (defaults.stack) attributes['service.namespace'] = defaults.stack;
  if (defaults.kind) attributes[KIND_ATTRIBUTE] = defaults.kind;
  return attributes;
}

/** Sends what OpenTelemetry's SDK reports, an export that failed among it, to the logger as warnings. */
export function logDiagnostics(logger: Logger | undefined): void {
  if (!logger) return;
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

const headerGetter: TextMapGetter<Headers> = {
  keys: headers => [...headers.keys()],
  get: (headers, key) => headers.get(key) ?? undefined,
};

const headerSetter: TextMapSetter<Headers> = {
  set: (headers, key, value) => headers.set(key, value),
};

/** The hooks an entrypoint wires, over the global tracer once export is on. */
export interface TelemetryHooks {
  /**
   * Hono middleware, registered first: a server span per request but
   * /healthz and /readyz, the child of an incoming traceparent, named by
   * its method and the route template that took it.
   */
  middleware(): MiddlewareHandler;
  /** Wraps a fetch: a client span per call, whose context the call carries in traceparent and tracestate. */
  traced(base: Fetch): Fetch;
  /** Runs a job's method in a span named after its @job class. */
  job<T>(name: string, run: () => Promise<T>): Promise<T>;
  /** Runs a worker's handler on a message of queue in a span: process <queue>. */
  message<T>(queue: string, message: TracedMessage, handle: () => Promise<T>): Promise<T>;
}

/** The hooks with export off: each only calls through. */
export const offHooks: TelemetryHooks = {
  middleware: () => (_c, next) => next(),
  traced: base => base,
  job: (_name, run) => run(),
  message: (_queue, _message, handle) => handle(),
};

/**
 * Installs provider, contextManager and the config's propagator as the
 * globals, and returns the hooks over them.
 */
export function installTelemetry(provider: TracerProvider, contextManager: ContextManager, config: TelemetryConfig): TelemetryHooks {
  context.setGlobalContextManager(contextManager.enable());
  propagation.setGlobalPropagator(config.propagator);
  trace.setGlobalTracerProvider(provider);
  const tracer = () => trace.getTracer(SCOPE);
  return {
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
    traced: base => async (input, init) => {
      const request = input instanceof Request ? input : undefined;
      const method = (init?.method ?? request?.method ?? 'GET').toUpperCase();
      const url = new URL(request?.url ?? (input instanceof URL ? input.href : String(input)));
      const span = tracer().startSpan(method, {
        kind: SpanKind.CLIENT,
        attributes: {
          'http.request.method': method,
          'url.full': spanURL(url),
          'server.address': url.hostname,
          ...(url.port ? { 'server.port': Number(url.port) } : {}),
        },
      });
      const headers = new Headers(init?.headers ?? request?.headers);
      propagation.inject(trace.setSpan(context.active(), span), headers, headerSetter);
      try {
        const response = await base(input, { ...init, headers });
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
  };
}

/**
 * The URL a client span records as url.full: the call's, with no userinfo,
 * query or fragment, which may carry a credential or personal data.
 */
export function spanURL(url: URL): string {
  const out = new URL(url.href);
  out.username = '';
  out.password = '';
  out.search = '';
  out.hash = '';
  return out.href;
}

/**
 * An endpoint as a log line names it: its origin and path, with no
 * userinfo, query or fragment, which may carry the collector's credential.
 */
export function loggedEndpoint(endpoint: string): string {
  try {
    return spanURL(new URL(endpoint));
  } catch {
    return '(no URL)';
  }
}

/** Removes the globals installTelemetry set. */
export function uninstallTelemetry(): void {
  trace.disable();
  propagation.disable();
  context.disable();
}

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

/** Reads the variables the entries list, and refuses what the runtime does not support. */
export function readConfig(env: TelemetryEnv): TelemetryConfig {
  const get = (name: string): string => (env[name] ?? '').trim();
  let endpointVariable = 'OTEL_EXPORTER_OTLP_TRACES_ENDPOINT';
  let endpoint = get(endpointVariable);
  if (endpoint === '') {
    endpointVariable = 'OTEL_EXPORTER_OTLP_ENDPOINT';
    endpoint = get(endpointVariable);
  }
  const disabled = get('OTEL_SDK_DISABLED').toLowerCase() === 'true';
  if (endpoint === '' || disabled) {
    return { on: false, endpoint, endpointVariable, sampler: new AlwaysOffSampler(), propagator: new CompositePropagator() };
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
  return { on: true, endpoint, endpointVariable, sampler, propagator };
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

/**
 * Reads a list of key=value pairs, as OTEL_RESOURCE_ATTRIBUTES and the
 * headers variables hold them, each part percent-decoded. A member without
 * = is a problem, named by the variable.
 */
export function readPairs(variable: string, value: string, problems: string[]): [string, string][] {
  const out: [string, string][] = [];
  for (const member of value.split(',')) {
    if (member.trim() === '') continue;
    const at = member.indexOf('=');
    if (at < 0) {
      problems.push(`${variable} has a member without =: ${JSON.stringify(member.trim())}`);
      continue;
    }
    const decode = (s: string): string => {
      try {
        return decodeURIComponent(s.trim());
      } catch {
        return s.trim();
      }
    };
    out.push([decode(member.slice(0, at)), decode(member.slice(at + 1))]);
  }
  return out;
}
