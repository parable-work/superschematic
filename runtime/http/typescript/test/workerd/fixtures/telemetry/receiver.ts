// The OTLP/HTTP receiver of the telemetry fixture: the apps' global
// outbound, so every export reaches it whatever host the endpoint names. It
// keeps each export's spans, decoded as the runtime's Bun tests decode
// them, with the headers it came with; GET /received answers what it kept.
import { decodeExport, type ReceivedSpan } from '../../../../src/otlp.testing.ts';

const spans: ReceivedSpan[] = [];
const exports: { url: string; key: string | null; contentType: string | null }[] = [];

export default {
  async fetch(request: Request): Promise<Response> {
    const url = new URL(request.url);
    if (request.method === 'GET' && url.pathname === '/received') return Response.json({ exports, spans });
    if (request.method !== 'POST') return new Response('no trace export', { status: 400 });
    exports.push({ url: request.url, key: request.headers.get('x-collector-key'), contentType: request.headers.get('content-type') });
    spans.push(...decodeExport(new Uint8Array(await request.arrayBuffer())));
    return new Response(new Uint8Array(), { headers: { 'content-type': 'application/x-protobuf' } });
  },
};
