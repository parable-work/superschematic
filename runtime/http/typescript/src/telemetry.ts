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
- An https endpoint under googleapis.com, Cloud Trace's OTLP endpoint, gets the
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

This entry and ./workers-telemetry, its twin for Workers, alone import
OpenTelemetry's packages, which a Bun server, job and worker depend on,
so the runtime's other entries bundle without them; what the two share is
./telemetry-core. The context of a span reaches the code a request runs
through AsyncLocalStorage, which Bun implements.
*/

import { AsyncLocalStorageContextManager } from '@opentelemetry/context-async-hooks';
import { OTLPTraceExporter } from '@opentelemetry/exporter-trace-otlp-proto';
import { defaultResource, detectResources, envDetector, resourceFromAttributes } from '@opentelemetry/resources';
import { BatchSpanProcessor, TracerProvider } from '@opentelemetry/sdk-trace';
import type { MiddlewareHandler } from 'hono';
import { googleAccessTokenSource, type GoogleIdTokenSourceOptions } from './credentials.js';
import type { Logger } from './logger.js';
import {
  defaultAttributes,
  FLUSH_TIMEOUT_MS,
  installTelemetry,
  logDiagnostics,
  loggedEndpoint,
  OTLP_PROTOCOL,
  readConfig,
  uninstallTelemetry,
  type TelemetryDefaults,
  type TracedMessage,
} from './telemetry-core.js';

export { JOB_ATTRIBUTE, KIND_ATTRIBUTE, OTLP_PROTOCOL, type TelemetryDefaults, type TracedMessage } from './telemetry-core.js';

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

/**
 * Sets up export from the environment, as the module says. It throws when a
 * variable holds a value the runtime does not support.
 */
export function startTelemetry(defaults: TelemetryDefaults): Telemetry {
  const config = readConfig(process.env);
  if (!config.on) return off;
  const logger = defaults.logger;
  logDiagnostics(logger);
  const resource = defaultResource()
    .merge(resourceFromAttributes(defaultAttributes(defaults)))
    .merge(detectResources({ detectors: [envDetector] }));
  const google = googleAuth(config.endpoint, process.env);
  const provider = new TracerProvider({
    resource,
    sampler: config.sampler,
    spanProcessors: [new BatchSpanProcessor({ exporter: new OTLPTraceExporter(google ? { headers: googleAuthHeaders({}, logger) } : {}) })],
  });
  const hooks = installTelemetry(provider, new AsyncLocalStorageContextManager(), config);
  logger?.info('exporting traces', { endpoint: loggedEndpoint(config.endpoint), protocol: OTLP_PROTOCOL, googleAuth: google });
  let stopped = false;
  return {
    enabled: true,
    middleware: hooks.middleware,
    fetch: hooks.traced(globalThis.fetch.bind(globalThis)),
    job: hooks.job,
    message: hooks.message,
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
        uninstallTelemetry();
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

/**
 * Whether export to endpoint sends the access token of the workload's
 * account (D67, amended): whether it is an https URL, so the token never
 * goes in the clear, whose host is googleapis.com or under it, and neither OTEL_EXPORTER_OTLP_TRACES_HEADERS nor
 * OTEL_EXPORTER_OTLP_HEADERS names an authorization header.
 */
export function googleAuth(endpoint: string, env: Readonly<Record<string, string | undefined>>): boolean {
  let host: string;
  try {
    const url = new URL(endpoint);
    if (url.protocol !== 'https:') return false;
    host = url.hostname.toLowerCase();
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
