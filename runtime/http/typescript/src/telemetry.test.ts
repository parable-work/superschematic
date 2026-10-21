import { afterEach, beforeEach, describe, expect, test } from 'bun:test';
import { trace } from '@opentelemetry/api';
import { Hono } from 'hono';
import { startReceiver, type ReceivedSpan, type Receiver } from './otlp.testing.js';
import { googleAuth, googleAuthHeaders, startTelemetry, type Telemetry } from './telemetry.js';

// The variables a test sets, cleared before each so the developer's shell
// does not turn export on or change it.
const VARIABLES = [
  'OTEL_EXPORTER_OTLP_ENDPOINT',
  'OTEL_EXPORTER_OTLP_TRACES_ENDPOINT',
  'OTEL_EXPORTER_OTLP_PROTOCOL',
  'OTEL_EXPORTER_OTLP_TRACES_PROTOCOL',
  'OTEL_EXPORTER_OTLP_HEADERS',
  'OTEL_SERVICE_NAME',
  'OTEL_RESOURCE_ATTRIBUTES',
  'OTEL_TRACES_SAMPLER',
  'OTEL_TRACES_SAMPLER_ARG',
  'OTEL_PROPAGATORS',
  'OTEL_SDK_DISABLED',
];

const saved = new Map<string, string | undefined>();
let receiver: Receiver | undefined;
let telemetry: Telemetry | undefined;

beforeEach(() => {
  for (const name of VARIABLES) {
    saved.set(name, process.env[name]);
    delete process.env[name];
  }
});

afterEach(async () => {
  await telemetry?.shutdown();
  telemetry = undefined;
  await receiver?.stop();
  receiver = undefined;
  for (const [name, value] of saved) {
    if (value === undefined) delete process.env[name];
    else process.env[name] = value;
  }
});

/** Points export at a receiver of the test's own, and starts it as a server's entrypoint does. */
function start(defaults: Parameters<typeof startTelemetry>[0] = { service: 'shop-storefront', stack: 'shop-stack', kind: 'server' }): Telemetry {
  receiver = startReceiver();
  process.env.OTEL_EXPORTER_OTLP_ENDPOINT = receiver.url;
  telemetry = startTelemetry(defaults);
  return telemetry;
}

/** A generated-style server: the telemetry middleware first, the probes, and an API's router mounted at '/'. */
function app(t: Telemetry, handle: (id: string) => Promise<Response>): Hono {
  const app = new Hono();
  app.use(t.middleware());
  app.get('/healthz', c => c.json({ status: 'ok' }));
  const router = new Hono();
  router.get('/api/orders/:id', c => handle(c.req.param('id')));
  app.route('/', router);
  return app;
}

function only(spans: ReceivedSpan[], kind: number): ReceivedSpan {
  const found = spans.filter(span => span.kind === kind);
  expect(found).toHaveLength(1);
  return found[0]!;
}

describe('startTelemetry', () => {
  test('with no endpoint it starts nothing', async () => {
    const provider = trace.getTracerProvider();
    const t = startTelemetry({ service: 'shop-storefront' });
    telemetry = t;
    expect(t.enabled).toBe(false);
    expect(trace.getTracer('x').startSpan('x').isRecording()).toBe(false);
    expect(trace.getTracerProvider()).toBe(provider);

    let traceparent: string | null = 'unset';
    const downstream = (globalThis as unknown as { Bun: { serve(o: object): { port: number; stop(f?: boolean): Promise<void> } } }).Bun.serve({
      port: 0,
      fetch: (request: Request) => {
        traceparent = request.headers.get('traceparent');
        return new Response('ok');
      },
    });
    try {
      const server = app(t, async () => t.fetch(`http://127.0.0.1:${downstream.port}/`));
      const response = await server.request('/api/orders/7', { headers: { traceparent: '00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01' } });
      expect(response.status).toBe(200);
      expect(traceparent).toBeNull();
    } finally {
      await downstream.stop(true);
    }
  });

  test('OTEL_SDK_DISABLED turns export off', async () => {
    receiver = startReceiver();
    process.env.OTEL_EXPORTER_OTLP_ENDPOINT = receiver.url;
    process.env.OTEL_SDK_DISABLED = 'True';
    telemetry = startTelemetry({ service: 'shop-storefront' });
    expect(telemetry.enabled).toBe(false);
    await telemetry.job('Nightly', async () => undefined);
    await telemetry.shutdown();
    expect(receiver.requests()).toBe(0);
  });

  test('a request is a server span named by its route, the child of its traceparent', async () => {
    const t = start();
    const server = app(t, async () => new Response(null, { status: 204 }));
    const response = await server.request('/api/orders/42', { headers: { traceparent: '00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01' } });
    expect(response.status).toBe(204);
    expect((await server.request('/healthz')).status).toBe(200);
    expect((await server.request('/api/nothing')).status).toBe(404);
    await t.shutdown();

    expect(receiver!.spans).toHaveLength(2);
    const span = receiver!.spans.find(s => s.attributes['url.path'] === '/api/orders/42')!;
    expect(span.name).toBe('GET /api/orders/{id}');
    expect(span.kind).toBe(2);
    expect(span.attributes['http.route']).toBe('/api/orders/{id}');
    expect(span.attributes['http.response.status_code']).toBe('204');
    expect(span.traceId).toBe('4bf92f3577b34da6a3ce929d0e0e4736');
    expect(span.parentSpanId).toBe('00f067aa0ba902b7');
    expect(span.resource['service.name']).toBe('shop-storefront');
    expect(span.resource['service.namespace']).toBe('shop-stack');
    expect(span.resource['superschematic.deployable.kind']).toBe('server');
    // A request no route takes is named by its method alone.
    expect(receiver!.spans.find(s => s.attributes['url.path'] === '/api/nothing')!.name).toBe('GET');
  });

  test('a call carries the trace in a client span', async () => {
    const t = start();
    let traceparent: string | null = null;
    const downstream = (globalThis as unknown as { Bun: { serve(o: object): { port: number; stop(f?: boolean): Promise<void> } } }).Bun.serve({
      port: 0,
      fetch: (request: Request) => {
        traceparent = request.headers.get('traceparent');
        return new Response('ok');
      },
    });
    try {
      const server = app(t, async () => {
        // The implementation awaits before it calls, as an SDK call does.
        await new Promise(resolve => setTimeout(resolve, 1));
        const answer = await t.fetch(`http://127.0.0.1:${downstream.port}/api/stock`, { method: 'POST', headers: { 'content-type': 'application/json' }, body: '{}' });
        return new Response(await answer.text());
      });
      expect((await server.request('/api/orders/7')).status).toBe(200);
    } finally {
      await downstream.stop(true);
    }
    await t.shutdown();

    const serverSpan = only(receiver!.spans, 2);
    const call = only(receiver!.spans, 3);
    expect(call.name).toBe('POST');
    expect(call.traceId).toBe(serverSpan.traceId);
    expect(call.parentSpanId).toBe(serverSpan.spanId);
    const parts = (traceparent ?? '').split('-');
    expect(parts).toHaveLength(4);
    expect(parts[1]).toBe(call.traceId);
    expect(parts[2]).toBe(call.spanId);
  });

  test('the variables override the defaults', async () => {
    process.env.OTEL_SERVICE_NAME = 'storefront';
    process.env.OTEL_RESOURCE_ATTRIBUTES = 'service.namespace=shop,deployment.environment.name=Dev';
    const t = start({ service: 'shop-storefront', stack: 'shop-stack', kind: 'job' });
    await t.job('Nightly', async () => undefined);
    await t.shutdown();
    const span = only(receiver!.spans, 1);
    expect(span.resource['service.name']).toBe('storefront');
    expect(span.resource['service.namespace']).toBe('shop');
    expect(span.resource['deployment.environment.name']).toBe('Dev');
    expect(span.resource['superschematic.deployable.kind']).toBe('job');
  });

  test('a job run and a message are spans that record their failure', async () => {
    const t = start({ service: 'shop-storefront-nightly', stack: 'shop-stack', kind: 'job' });
    await expect(t.job('Nightly', async () => Promise.reject(new Error('the carrier is down')))).rejects.toThrow('the carrier is down');
    expect(await t.message('OrderPlaced', { id: 'm-1', attempt: 2 }, async () => 'handled')).toBe('handled');
    await t.shutdown();

    const job = only(receiver!.spans, 1);
    expect(job.name).toBe('Nightly');
    expect(job.attributes['superschematic.job']).toBe('Nightly');
    expect(job.status).toBe(2);
    const message = only(receiver!.spans, 5);
    expect(message.name).toBe('process OrderPlaced');
    expect(message.parentSpanId).toBe('');
    expect(message.attributes['messaging.destination.name']).toBe('OrderPlaced');
    expect(message.attributes['messaging.message.id']).toBe('m-1');
    expect(message.attributes['superschematic.message.attempt']).toBe('2');
    expect(message.status).toBe(0);
  });

  test("a job's process flushes its span before it exits", async () => {
    receiver = startReceiver();
    const script = [
      "import { startTelemetry } from './src/telemetry.ts';",
      "const telemetry = startTelemetry({ service: 'shop-storefront-nightly', kind: 'job' });",
      "await telemetry.job('Nightly', async () => undefined);",
      'await telemetry.shutdown();',
      'process.exit(0);',
    ].join('\n');
    const env: Record<string, string> = {};
    for (const [name, value] of Object.entries(process.env)) if (value !== undefined) env[name] = value;
    // The batch is due five seconds after the span ends, long after the
    // process exits: only the shutdown sends it.
    env.OTEL_EXPORTER_OTLP_ENDPOINT = receiver.url;
    const bun = (globalThis as unknown as { Bun: { spawn(o: object): { exited: Promise<number> }; argv: string[] } }).Bun;
    const child = bun.spawn({ cmd: [process.execPath, '-e', script], cwd: new URL('..', import.meta.url).pathname, env, stdout: 'inherit', stderr: 'inherit' });
    expect(await child.exited).toBe(0);
    expect(receiver.spans.map(span => span.name)).toEqual(['Nightly']);
  });

  test('unsupported values are refused, naming the variable', () => {
    const cases: [string, string, string][] = [
      ['OTEL_EXPORTER_OTLP_PROTOCOL', 'grpc', 'OTEL_EXPORTER_OTLP_PROTOCOL is "grpc"'],
      ['OTEL_EXPORTER_OTLP_TRACES_PROTOCOL', 'http/json', 'OTEL_EXPORTER_OTLP_TRACES_PROTOCOL is "http/json"'],
      ['OTEL_PROPAGATORS', 'tracecontext,b3', 'OTEL_PROPAGATORS names "b3"'],
      ['OTEL_TRACES_SAMPLER', 'jaeger_remote', 'OTEL_TRACES_SAMPLER is "jaeger_remote"'],
      ['OTEL_EXPORTER_OTLP_ENDPOINT', 'localhost:4318', 'OTEL_EXPORTER_OTLP_ENDPOINT is "localhost:4318"'],
    ];
    for (const [name, value, message] of cases) {
      process.env.OTEL_EXPORTER_OTLP_ENDPOINT = 'http://localhost:4318';
      process.env[name] = value;
      expect(() => startTelemetry({ service: 'shop-storefront' })).toThrow(message);
      delete process.env[name];
    }
    process.env.OTEL_EXPORTER_OTLP_ENDPOINT = 'http://localhost:4318';
    process.env.OTEL_TRACES_SAMPLER = 'traceidratio';
    process.env.OTEL_TRACES_SAMPLER_ARG = '2';
    expect(() => startTelemetry({ service: 'shop-storefront' })).toThrow('OTEL_TRACES_SAMPLER_ARG is "2"');
  });

  test('the sampler and the propagators are read', async () => {
    process.env.OTEL_TRACES_SAMPLER = 'always_off';
    process.env.OTEL_PROPAGATORS = 'none';
    const t = start();
    let traceparent: string | null = 'unset';
    const downstream = (globalThis as unknown as { Bun: { serve(o: object): { port: number; stop(f?: boolean): Promise<void> } } }).Bun.serve({
      port: 0,
      fetch: (request: Request) => {
        traceparent = request.headers.get('traceparent');
        return new Response('ok');
      },
    });
    try {
      await t.fetch(`http://127.0.0.1:${downstream.port}/`);
    } finally {
      await downstream.stop(true);
    }
    await t.job('Nightly', async () => undefined);
    await t.shutdown();
    expect(traceparent).toBeNull();
    expect(receiver!.spans).toHaveLength(0);
  });
});

describe("Google's OTLP endpoint (D67, amended)", () => {
  test('an endpoint under googleapis.com alone gets the access token, unless a headers variable names authorization', () => {
    const cases: Array<[string, boolean]> = [
      ['https://telemetry.googleapis.com', true],
      ['https://telemetry.googleapis.com:443/v1/traces', true],
      ['https://TELEMETRY.GOOGLEAPIS.COM', true],
      ['https://telemetry.us-central1.rep.googleapis.com/', true],
      ['https://googleapis.com', true],
      ['http://collector.internal:4318', false],
      ['https://notgoogleapis.com', false],
      ['https://googleapis.com.example.com', false],
    ];
    for (const [endpoint, want] of cases) expect([endpoint, googleAuth(endpoint, {})]).toEqual([endpoint, want]);
    expect(googleAuth('https://telemetry.googleapis.com', { OTEL_EXPORTER_OTLP_HEADERS: 'Authorization=Bearer%20abc' })).toBe(false);
    expect(googleAuth('https://telemetry.googleapis.com', { OTEL_EXPORTER_OTLP_TRACES_HEADERS: 'x-a=1, authorization = Basic%20x' })).toBe(false);
    expect(googleAuth('https://telemetry.googleapis.com', { OTEL_EXPORTER_OTLP_HEADERS: 'x-goog-user-project=p' })).toBe(true);
  });

  test('each export carries the metadata server’s access token, held until five minutes before it expires', async () => {
    let now = Date.UTC(2026, 9, 20, 12);
    let fetched = 0;
    let failing = false;
    const asked: string[] = [];
    const headers = googleAuthHeaders({
      metadataHost: 'metadata.test',
      now: () => now,
      fetch: async (url, init) => {
        asked.push(`${url} ${init.headers['metadata-flavor']}`);
        if (failing) return new Response('no account', { status: 500 });
        fetched += 1;
        return Response.json({ access_token: `token-${fetched}`, expires_in: 3599, token_type: 'Bearer' });
      },
    });
    expect(await headers()).toEqual({ Authorization: 'Bearer token-1' });
    expect(await headers()).toEqual({ Authorization: 'Bearer token-1' });
    now += 54 * 60_000;
    expect(await headers()).toEqual({ Authorization: 'Bearer token-1' });
    now += 60_000;
    expect(await headers()).toEqual({ Authorization: 'Bearer token-2' });
    expect(asked).toEqual(Array(2).fill('http://metadata.test/computeMetadata/v1/instance/service-accounts/default/token Google'));
    failing = true;
    now += 60 * 60_000;
    expect(await headers()).toEqual({});
  });

  test('an export to the endpoint sends the token', async () => {
    const metadata = Bun.serve({
      port: 0,
      fetch: req =>
        new URL(req.url).pathname === '/computeMetadata/v1/instance/service-accounts/default/token' && req.headers.get('metadata-flavor') === 'Google'
          ? Response.json({ access_token: 'the-token', expires_in: 3599 })
          : new Response('no', { status: 404 }),
    });
    const authorizations: Array<string | null> = [];
    const collector = Bun.serve({
      port: 0,
      fetch: req => {
        authorizations.push(req.headers.get('authorization'));
        return new Response(null, { status: 200 });
      },
    });
    try {
      const { OTLPTraceExporter } = await import('@opentelemetry/exporter-trace-otlp-proto');
      const exporter = new OTLPTraceExporter({ url: `http://127.0.0.1:${collector.port}/v1/traces`, headers: googleAuthHeaders({ metadataHost: `127.0.0.1:${metadata.port}` }) });
      const done = await new Promise<number>(resolve => exporter.export([], result => resolve(result.code)));
      expect(done).toBe(0);
      await exporter.shutdown();
      expect(authorizations).toEqual(['Bearer the-token']);
    } finally {
      metadata.stop(true);
      collector.stop(true);
    }
  });
});
