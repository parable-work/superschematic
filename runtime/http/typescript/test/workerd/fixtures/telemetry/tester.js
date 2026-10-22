// The tester of the telemetry fixture (D67, D64 amended): it calls the
// app with export on, twice at once with two incoming traceparents, and
// fires its scheduled handler, then reads what the receiver kept: one
// export per request, at the endpoint's v1/traces with the headers the
// bindings name; per request a server span named by its route, the child
// of its incoming traceparent, and a client span, its child, whose context
// the callee received, each request's spans in its own trace, which
// AsyncLocalStorage keeps apart, the client span's url.full without the
// call's query; no span for /healthz; the job's span; and
// the resource the defaults and OTEL_RESOURCE_ATTRIBUTES give. The app with
// no OTEL bindings exports nothing.

const TRACES = ['0af7651916cd43dd8448eb211c80319c', '4bf92f3577b34da6a3ce929d0e0e4736'];
const PARENTS = ['b7ad6b7169203331', '00f067aa0ba902b7'];

async function received(env) {
  return (await env.RECEIVER.fetch('http://receiver/received')).json();
}

/** Polls the receiver until it holds want spans, as each flush runs in waitUntil. */
async function spans(env, want) {
  for (let i = 0; i < 100; i++) {
    const got = await received(env);
    if (got.spans.length >= want) return got;
    await new Promise(resolve => setTimeout(resolve, 20));
  }
  return received(env);
}

export default {
  async test(_controller, env) {
    const failures = [];
    const answers = await Promise.all(
      TRACES.map((trace, i) =>
        env.APP.fetch(`https://shop-storefront/api/quotes/sku-${i}`, { headers: { traceparent: `00-${trace}-${PARENTS[i]}-01` } }).then(r => r.json())
      )
    );
    await env.APP.fetch('https://shop-storefront/healthz');
    await env.APP.scheduled({ cron: '0 3 * * *' });
    const got = await spans(env, 5);
    if (got.spans.length !== 5) failures.push(`the receiver holds ${got.spans.length} spans: ${JSON.stringify(got.spans.map(s => s.name))}`);
    for (const e of got.exports) {
      if (e.url !== 'http://collector.example/v1/traces' || e.key !== 'k1' || e.contentType !== 'application/x-protobuf') failures.push(`an export went as ${JSON.stringify(e)}`);
    }
    if (got.exports.length < 2 || got.exports.length > 4) failures.push(`${got.exports.length} exports, want one per request that ended spans`);
    TRACES.forEach((trace, i) => {
      const server = got.spans.find(s => s.traceId === trace && s.kind === 2);
      const client = got.spans.find(s => s.traceId === trace && s.kind === 3);
      if (!server || server.name !== 'GET /api/quotes/{sku}' || server.parentSpanId !== PARENTS[i] || server.attributes['http.route'] !== '/api/quotes/{sku}') {
        failures.push(`trace ${trace}'s server span is ${JSON.stringify(server)}`);
      }
      if (
        !client ||
        !server ||
        client.parentSpanId !== server.spanId ||
        client.attributes['server.address'] !== 'tel-stack-staging-shop-pricing' ||
        client.attributes['url.full'] !== `https://tel-stack-staging-shop-pricing/api/prices/sku-${i}`
      ) {
        failures.push(`trace ${trace}'s client span is ${JSON.stringify(client)}`);
      }
      const sent = answers[i].pricing.traceparent;
      if (!client || sent !== `00-${trace}-${client.spanId}-01`) failures.push(`the callee of trace ${trace} received traceparent ${sent}`);
      if (answers[i].enabled !== true) failures.push(`the app says export is ${answers[i].enabled}`);
    });
    const job = got.spans.find(s => s.name === 'SweepCarts');
    if (!job || job.attributes['superschematic.job'] !== 'SweepCarts' || job.parentSpanId !== '') failures.push(`the job's span is ${JSON.stringify(job)}`);
    const resource = got.spans[0]?.resource ?? {};
    for (const [key, value] of Object.entries({
      'service.name': 'shop-storefront',
      'service.namespace': 'tel-stack',
      'superschematic.deployable.kind': 'server',
      'deployment.environment.name': 'Staging',
    })) {
      if (resource[key] !== value) failures.push(`the resource's ${key} is ${resource[key]}, want ${value}`);
    }

    const before = got.exports.length;
    const off = await env.OFF.fetch('https://shop-storefront/api/quotes/sku-off', { headers: { traceparent: `00-${TRACES[0]}-${PARENTS[0]}-01` } }).then(r => r.json());
    await env.OFF.scheduled({ cron: '0 3 * * *' });
    await new Promise(resolve => setTimeout(resolve, 100));
    const after = await received(env);
    if (off.enabled !== false || after.exports.length !== before) failures.push(`with no OTEL bindings the app says ${off.enabled} and exported ${after.exports.length - before} times`);
    if (off.pricing.traceparent !== null) failures.push(`with export off the callee received traceparent ${off.pricing.traceparent}`);

    if (failures.length > 0) throw new Error(`trace export under workerd:\n${failures.join('\n')}`);
    console.log('trace export under workerd: two requests in their own traces, the callee in each, the job, and nothing with export off');
  },
};
