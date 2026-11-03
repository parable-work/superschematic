/*
OpenTelemetry trace export for a stack's TypeScript server, job or worker
on Cloudflare Workers (D67, D64 amended, D92; section 18.5 of
docs/stack-model.md), the runtime's ./workers-telemetry entry, the twin of
./telemetry on Bun. The generated fetch.ts, scheduled.ts and drain.ts call
startWorkersTelemetry once per isolate, at module scope, with the Worker's
env, and everything else comes from OpenTelemetry's own variables, which a
Worker reads as its string bindings:

- OTEL_EXPORTER_OTLP_ENDPOINT, or OTEL_EXPORTER_OTLP_TRACES_ENDPOINT, turns
  export on; without either, or with OTEL_SDK_DISABLED=true, nothing is set
  up, and every hook only calls through.
- OTEL_EXPORTER_OTLP_HEADERS and OTEL_EXPORTER_OTLP_TIMEOUT, and their
  traces forms, reach the exporter.
- OTEL_EXPORTER_OTLP_PROTOCOL, OTEL_SERVICE_NAME, OTEL_RESOURCE_ATTRIBUTES,
  OTEL_TRACES_SAMPLER, OTEL_TRACES_SAMPLER_ARG and OTEL_PROPAGATORS are read
  as on Bun, with the same refusals, and a resource attribute or a header
  without = is refused too. A refusal throws at module scope, so Cloudflare
  refuses the version's upload, naming the variable.

A Worker differs from a Bun process in three ways the entry answers:

- The context of a request's span reaches the calls its code makes through
  AsyncLocalStorage, which workerd gives under the nodejs_als compatibility
  flag alone, without nodejs_compat: every Worker version of a server or a
  job names that flag, and bundle.ts keeps node:async_hooks external, the
  one node: import a bundle may hold.
- An isolate may not run I/O outside a request, so no timer exports:
  ended spans wait in the isolate until flush, which the entrypoint hands
  to the request's waitUntil, and a scheduled run's or a Workflow step's
  end awaits; each flush sends what every request of the isolate ended
  since the last, in one export.
- OpenTelemetry's browser exporter sends with fetch options workerd
  refuses, so the entry sends the OTLP/HTTP protobuf body itself, with
  OpenTelemetry's own serializer.
*/

import { AsyncLocalStorage } from 'node:async_hooks';
import { diag, ROOT_CONTEXT, TraceFlags, type Attributes, type Context, type ContextManager } from '@opentelemetry/api';
import { ProtobufTraceSerializer } from '@opentelemetry/otlp-transformer';
import { defaultResource, resourceFromAttributes } from '@opentelemetry/resources';
import { TracerProvider, type ReadableSpan, type SpanProcessor } from '@opentelemetry/sdk-trace';
import type { MiddlewareHandler } from 'hono';
import {
  defaultAttributes,
  installTelemetry,
  logDiagnostics,
  loggedEndpoint,
  OTLP_PROTOCOL,
  readConfig,
  readPairs,
  type Fetch,
  type TelemetryConfig,
  type TelemetryDefaults,
  type TelemetryEnv,
  type TracedMessage,
} from './telemetry-core.js';

export { JOB_ATTRIBUTE, KIND_ATTRIBUTE, OTLP_PROTOCOL, type Fetch, type TelemetryDefaults, type TracedMessage } from './telemetry-core.js';

/** The compatibility flag a Worker that bundles this entry runs under: AsyncLocalStorage, and no more of Node. */
export const WORKERS_COMPATIBILITY_FLAG = 'nodejs_als';

/** How long an export may take unless OTEL_EXPORTER_OTLP_TIMEOUT says, as OpenTelemetry's default. */
const DEFAULT_TIMEOUT_MS = 10_000;

/** The most ended spans an isolate keeps for its next flush; beyond, the oldest are dropped. */
const MAX_BUFFERED_SPANS = 2_048;

/** What startWorkersTelemetry returns: the hooks fetch.ts and scheduled.ts wire, and the flush. */
export interface WorkersTelemetry {
  /** Whether export is on. */
  readonly enabled: boolean;
  /** Hono middleware, registered first, as ./telemetry's. */
  middleware(): MiddlewareHandler;
  /** Wraps the fetch of a calls edge's SDK client, a service binding's: a client span per call that carries traceparent. */
  traced(base: Fetch): Fetch;
  /** Runs a job's method in a span named after its @job class. */
  job<T>(name: string, run: () => Promise<T>): Promise<T>;
  /** Runs a worker's handler on a message of queue in a span, process <queue>, as ./telemetry's does (D92). */
  message<T>(queue: string, message: TracedMessage, handle: () => Promise<T>): Promise<T>;
  /** Sends the spans ended since the last flush; never throws. */
  flush(): Promise<void>;
}

/** The telemetry with export off: every hook only calls through. */
const off: WorkersTelemetry = {
  enabled: false,
  middleware: () => (_c, next) => next(),
  traced: base => base,
  job: (_name, run) => run(),
  message: (_queue, _message, handle) => handle(),
  flush: async () => undefined,
};

/**
 * Sets up export from env, a Worker's bindings, as the module says, once
 * per isolate. It throws when a variable holds a value the runtime does not
 * support.
 */
export function startWorkersTelemetry(env: Readonly<Record<string, unknown>>, defaults: TelemetryDefaults): WorkersTelemetry {
  const vars = stringsOf(env);
  const config = readConfig(vars);
  if (!config.on) return off;
  const problems: string[] = [];
  const exporter = readExporter(vars, config, problems);
  const attributes: Attributes = defaultAttributes(defaults);
  for (const [key, value] of readPairs('OTEL_RESOURCE_ATTRIBUTES', vars.OTEL_RESOURCE_ATTRIBUTES ?? '', problems)) attributes[key] = value;
  const serviceName = (vars.OTEL_SERVICE_NAME ?? '').trim();
  if (serviceName !== '') attributes['service.name'] = serviceName;
  if (problems.length > 0) throw new Error(problems.join('; '));
  const logger = defaults.logger;
  logDiagnostics(logger);
  const processor = new FlushedSpanProcessor(exporter);
  const provider = new TracerProvider({
    resource: defaultResource().merge(resourceFromAttributes(attributes)),
    sampler: config.sampler,
    spanProcessors: [processor],
  });
  const hooks = installTelemetry(provider, new StorageContextManager(), config);
  logger?.info('exporting traces', { endpoint: loggedEndpoint(config.endpoint), protocol: OTLP_PROTOCOL });
  return { enabled: true, middleware: hooks.middleware, traced: hooks.traced, job: hooks.job, message: hooks.message, flush: () => processor.forceFlush() };
}

/** The string bindings of env, as the variables OpenTelemetry reads. */
function stringsOf(env: Readonly<Record<string, unknown>>): TelemetryEnv {
  const out: Record<string, string> = {};
  for (const [name, value] of Object.entries(env)) {
    if (name.startsWith('OTEL_') && typeof value === 'string') out[name] = value;
  }
  return out;
}

/** What the exporter sends to: the traces URL, the headers and the timeout. */
interface ExporterConfig {
  readonly url: string;
  readonly headers: Record<string, string>;
  readonly timeoutMs: number;
}

/**
 * The exporter's URL, headers and timeout: the traces endpoint as it is,
 * or the base endpoint with v1/traces, as OpenTelemetry composes them; the
 * traces headers over the common ones; and the traces timeout, else the
 * common one, in milliseconds.
 */
function readExporter(vars: TelemetryEnv, config: TelemetryConfig, problems: string[]): ExporterConfig {
  const url =
    config.endpointVariable === 'OTEL_EXPORTER_OTLP_TRACES_ENDPOINT'
      ? config.endpoint
      : `${config.endpoint}${config.endpoint.endsWith('/') ? '' : '/'}v1/traces`;
  const headers: Record<string, string> = {};
  for (const name of ['OTEL_EXPORTER_OTLP_HEADERS', 'OTEL_EXPORTER_OTLP_TRACES_HEADERS']) {
    for (const [key, value] of readPairs(name, vars[name] ?? '', problems)) headers[key.toLowerCase()] = value;
  }
  let timeoutMs = DEFAULT_TIMEOUT_MS;
  for (const name of ['OTEL_EXPORTER_OTLP_TRACES_TIMEOUT', 'OTEL_EXPORTER_OTLP_TIMEOUT']) {
    const value = (vars[name] ?? '').trim();
    if (value === '') continue;
    const ms = Number(value);
    if (!Number.isInteger(ms) || ms <= 0) {
      problems.push(`${name} is ${JSON.stringify(value)}; it is a whole number of milliseconds above 0`);
    } else {
      timeoutMs = ms;
    }
    break;
  }
  return { url, headers, timeoutMs };
}

/**
 * Keeps the sampled spans an isolate ends until forceFlush, which sends
 * them in one OTLP/HTTP protobuf export through fetch. It sets no timer:
 * a Worker runs I/O within a request alone.
 */
class FlushedSpanProcessor implements SpanProcessor {
  private spans: ReadableSpan[] = [];
  private dropped = 0;

  constructor(private readonly exporter: ExporterConfig) {}

  onStart(): void {}

  onEnd(span: ReadableSpan): void {
    if ((span.spanContext().traceFlags & TraceFlags.SAMPLED) === 0) return;
    this.spans.push(span);
    if (this.spans.length > MAX_BUFFERED_SPANS) {
      this.spans.shift();
      this.dropped++;
    }
  }

  async forceFlush(): Promise<void> {
    const spans = this.spans;
    if (spans.length === 0) return;
    this.spans = [];
    if (this.dropped > 0) {
      diag.warn(`dropped ${this.dropped} spans the isolate ended while ${MAX_BUFFERED_SPANS} waited for a flush`);
      this.dropped = 0;
    }
    const body = ProtobufTraceSerializer.serializeRequest(spans);
    if (!body) return;
    try {
      const response = await fetch(this.exporter.url, {
        method: 'POST',
        headers: { ...this.exporter.headers, 'content-type': 'application/x-protobuf' },
        body: body as Uint8Array<ArrayBuffer>,
        signal: AbortSignal.timeout(this.exporter.timeoutMs),
      });
      await response.body?.cancel();
      if (!response.ok) diag.warn(`the OTLP endpoint answered ${response.status} to an export of ${spans.length} spans`);
    } catch (error) {
      diag.warn(`an export of ${spans.length} spans failed: ${error instanceof Error ? error.message : String(error)}`);
    }
  }

  shutdown(): Promise<void> {
    return this.forceFlush();
  }
}

/** OpenTelemetry's context over workerd's AsyncLocalStorage. */
class StorageContextManager implements ContextManager {
  private readonly storage = new AsyncLocalStorage<Context>();

  active(): Context {
    return this.storage.getStore() ?? ROOT_CONTEXT;
  }

  with<A extends unknown[], F extends (...args: A) => ReturnType<F>>(context: Context, fn: F, thisArg?: ThisParameterType<F>, ...args: A): ReturnType<F> {
    return this.storage.run(context, () => fn.call(thisArg, ...args));
  }

  bind<T>(context: Context, target: T): T {
    if (typeof target !== 'function') return target;
    const manager = this;
    const fn = target as unknown as (...args: unknown[]) => unknown;
    return function (this: unknown, ...args: unknown[]) {
      return manager.with(context, () => fn.apply(this, args));
    } as unknown as T;
  }

  enable(): this {
    return this;
  }

  disable(): this {
    (this.storage as { disable?: () => void }).disable?.();
    return this;
  }
}
