/*
An OTLP/HTTP trace receiver in the test's process (D67): a Bun server that
takes the export requests startTelemetry's exporter sends, in protobuf, and
keeps their spans. It decodes the protobuf wire format itself, reading the
fields of ExportTraceServiceRequest a test checks and skipping the rest, so
the tests need no protobuf library.
*/

/** A span the receiver took, its IDs in lower-case hex. */
export interface ReceivedSpan {
  readonly traceId: string;
  readonly spanId: string;
  readonly parentSpanId: string;
  readonly name: string;
  /** OTLP's kind: 1 internal, 2 server, 3 client, 4 producer, 5 consumer. */
  readonly kind: number;
  /** OTLP's status code: 0 unset, 1 ok, 2 error. */
  readonly status: number;
  readonly attributes: Record<string, string>;
  readonly resource: Record<string, string>;
}

export interface Receiver {
  /** What OTEL_EXPORTER_OTLP_ENDPOINT names. */
  readonly url: string;
  readonly spans: ReceivedSpan[];
  /** How many export requests arrived. */
  requests(): number;
  stop(): Promise<void>;
}

interface BunServer {
  readonly port: number;
  stop(closeActiveConnections?: boolean): Promise<void>;
}

export function startReceiver(): Receiver {
  const spans: ReceivedSpan[] = [];
  let requests = 0;
  const bun = (globalThis as unknown as { Bun: { serve(o: { port: number; fetch(r: Request): Promise<Response> }): BunServer } }).Bun;
  const server = bun.serve({
    port: 0,
    fetch: async request => {
      requests++;
      const url = new URL(request.url);
      if (request.method !== 'POST' || url.pathname !== '/v1/traces' || request.headers.get('content-type') !== 'application/x-protobuf') {
        return new Response('no trace export', { status: 400 });
      }
      spans.push(...decodeExport(new Uint8Array(await request.arrayBuffer())));
      return new Response(new Uint8Array(), { headers: { 'content-type': 'application/x-protobuf' } });
    },
  });
  return {
    url: `http://127.0.0.1:${server.port}`,
    spans,
    requests: () => requests,
    stop: () => server.stop(true),
  };
}

type Field = { readonly number: number; readonly wire: number; readonly value: bigint | Uint8Array };

/** The fields of one protobuf message, in order. */
function fields(bytes: Uint8Array): Field[] {
  const out: Field[] = [];
  let at = 0;
  const varint = (): bigint => {
    let result = 0n;
    let shift = 0n;
    for (;;) {
      const byte = bytes[at++];
      if (byte === undefined) throw new Error('truncated varint');
      result |= BigInt(byte & 0x7f) << shift;
      if ((byte & 0x80) === 0) return result;
      shift += 7n;
    }
  };
  while (at < bytes.length) {
    const key = Number(varint());
    const number = key >> 3;
    const wire = key & 7;
    if (wire === 0) out.push({ number, wire, value: varint() });
    else if (wire === 1) {
      out.push({ number, wire, value: bytes.slice(at, at + 8) });
      at += 8;
    } else if (wire === 2) {
      const length = Number(varint());
      out.push({ number, wire, value: bytes.slice(at, at + length) });
      at += length;
    } else if (wire === 5) {
      out.push({ number, wire, value: bytes.slice(at, at + 4) });
      at += 4;
    } else throw new Error(`wire type ${wire}`);
  }
  return out;
}

const text = (value: bigint | Uint8Array): string => new TextDecoder().decode(value as Uint8Array);
const hex = (value: bigint | Uint8Array): string => Buffer.from(value as Uint8Array).toString('hex');
const each = (bytes: Uint8Array, number: number): Uint8Array[] =>
  fields(bytes)
    .filter(f => f.number === number && f.wire === 2)
    .map(f => f.value as Uint8Array);

/** KeyValue attributes, each value as text. */
function attributes(messages: Uint8Array[]): Record<string, string> {
  const out: Record<string, string> = {};
  for (const kv of messages) {
    let key = '';
    let value = '';
    for (const f of fields(kv)) {
      if (f.number === 1) key = text(f.value);
      if (f.number === 2) {
        for (const v of fields(f.value as Uint8Array)) {
          if (v.number === 1) value = text(v.value);
          else if (v.number === 2) value = String(v.value !== 0n);
          else if (v.number === 3) value = String(BigInt.asIntN(64, v.value as bigint));
          else if (v.number === 4) value = String(new DataView((v.value as Uint8Array).buffer).getFloat64(0, true));
        }
      }
    }
    out[key] = value;
  }
  return out;
}

function decodeExport(bytes: Uint8Array): ReceivedSpan[] {
  const spans: ReceivedSpan[] = [];
  for (const resourceSpans of each(bytes, 1)) {
    const resource = attributes(each(resourceSpans, 1).flatMap(r => each(r, 1)));
    for (const scopeSpans of each(resourceSpans, 2)) {
      for (const span of each(scopeSpans, 2)) {
        let traceId = '';
        let spanId = '';
        let parentSpanId = '';
        let name = '';
        let kind = 0;
        let status = 0;
        const attrs: Uint8Array[] = [];
        for (const f of fields(span)) {
          if (f.number === 1) traceId = hex(f.value);
          else if (f.number === 2) spanId = hex(f.value);
          else if (f.number === 4) parentSpanId = hex(f.value);
          else if (f.number === 5) name = text(f.value);
          else if (f.number === 6) kind = Number(f.value);
          else if (f.number === 9) attrs.push(f.value as Uint8Array);
          else if (f.number === 15) {
            for (const s of fields(f.value as Uint8Array)) if (s.number === 3) status = Number(s.value);
          }
        }
        spans.push({ traceId, spanId, parentSpanId, name, kind, status, attributes: attributes(attrs), resource });
      }
    }
  }
  return spans;
}
