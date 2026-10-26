import { describe, expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { McpUnknownToolError, serveMcp, type McpCallOutcome, type McpContent, type McpServerOptions, type McpTool, type McpToolSet } from './mcp';

/*
The engine's MCP corpus (runtime/engine/testdata/http/mcp.json), which the
engine's tests record through the official MCP client, replayed through
this runtime's transport (D81). The transport is the engine's, copied: the
tools differ, so each exchange's tools answer what the engine's answered,
and the transport must frame them as the engine did, byte for byte in a
call's text: the statuses, the headers, the event stream of the 2025
revisions and the single JSON body of 2026-07-28, the ids, initialize and
server/discover, the 202 of a notification and the 405 of a GET, a result's
text and structured content, a tool error carrying its problem, and an
unknown tool's invalid-params error. Exchanges the transport does not
answer, the gate's 401 and the engine's 404 for a namespace it has not got,
are left out.
*/

const CORPUS = fileURLToPath(new URL('../../../engine/testdata/http/mcp.json', import.meta.url));

type Json = null | boolean | number | string | Json[] | { [key: string]: Json };

interface Exchange {
  readonly request: { readonly method: string; readonly path: string; readonly query?: string; readonly headers: Record<string, string>; readonly body?: Json };
  readonly response: { readonly status: number; readonly headers?: Record<string, string>; readonly body?: Json };
  readonly stream?: string;
}

interface Corpus {
  readonly shared: Record<string, Json>;
  readonly cases: readonly { readonly name: string; readonly steps: readonly Record<string, Json>[] }[];
}

const corpus = JSON.parse(readFileSync(CORPUS, 'utf8')) as Corpus;

/** value with each shared value it names, at any depth, in place. */
function resolve(value: Json): Json {
  if (Array.isArray(value)) return value.map(resolve);
  if (value === null || typeof value !== 'object') return value;
  if (typeof value.$shared === 'string') return resolve(corpus.shared[value.$shared]);
  return Object.fromEntries(Object.entries(value).map(([key, item]) => [key, resolve(item)]));
}

/** The frames a step's stream sent: every frames step for it after index, until it ends. */
function framesOf(steps: readonly Record<string, Json>[], index: number, stream: string): Json[] {
  const frames: Json[] = [];
  for (const step of steps.slice(index + 1)) {
    if (step.stream !== stream || 'request' in step) continue;
    if (step.end === true) break;
    frames.push(...(resolve(step.frames) as Json[]));
  }
  return frames;
}

/** The engine's serverInfo, which the corpus's engine package reports, unless the mount gave one. */
const ENGINE_SERVER = { name: '@superschematic/engine', version: '0.0.0' };

/** Tools that answer what the engine answered the recorded reply. */
function replayTools(reply: Json): McpToolSet {
  const message = reply as { result?: Record<string, Json>; error?: { code: number; message: string } };
  return {
    list: () => (message.result?.tools ?? []) as unknown as McpTool[],
    call: async (): Promise<McpCallOutcome> => {
      if (message.error) {
        if (message.error.code === -32602) throw new McpUnknownToolError(message.error.message);
        throw new Error(message.error.message);
      }
      const result = message.result as { content: McpContent[]; structuredContent?: Json; isError?: boolean };
      if (result.isError) return { problem: result.structuredContent as never };
      const [first, ...rest] = result.content;
      if (first?.type !== 'text' || rest.length > 0) return { content: result.content };
      try {
        return { result: JSON.parse(first.text) };
      } catch {
        // A value JSON cannot hold, a time, is a symbol in the text, and a
        // string in the structured content.
        return { result: result.structuredContent ?? first.text };
      }
    },
  };
}

/** The recorded reply a request's tools answer from: its one frame, or its JSON body. */
function replyOf(exchange: Exchange, frames: Json[]): Json {
  if (exchange.stream !== undefined) return (frames[0] as { data: Json }).data;
  return exchange.response.body ?? null;
}

/** The frames of an event-stream body, as the corpus records them. */
function parseFrames(text: string): Json[] {
  return text
    .split('\n\n')
    .filter(block => block.trim() !== '')
    .map(block => {
      const frame: Record<string, Json> = {};
      for (const line of block.split('\n')) {
        const colon = line.indexOf(': ');
        const field = line.slice(0, colon);
        const value = line.slice(colon + 2);
        frame[field] = field === 'data' ? (JSON.parse(value) as Json) : value;
      }
      return frame;
    });
}

/**
 * json with the text of each content item that holds a symbol ({{time:1}}
 * where the engine wrote a time) replaced by one mark: the corpus writes the
 * symbol bare in the text and quoted in the structured content, which is
 * compared instead.
 */
function withoutSymbolTexts(json: Json): Json {
  if (Array.isArray(json)) return json.map(withoutSymbolTexts);
  if (json === null || typeof json !== 'object') return json;
  const out: Record<string, Json> = {};
  for (const [key, value] of Object.entries(json)) {
    out[key] = key === 'text' && json.type === 'text' && typeof value === 'string' && value.includes('{{') ? '<symbol>' : withoutSymbolTexts(value);
  }
  return out;
}

/** The text of each tool result's content in a reply, which must match the engine's byte for byte. */
function texts(reply: Json): string[] {
  const result = (reply as { result?: { content?: { type: string; text?: string }[] } }).result;
  return (result?.content ?? []).filter(item => item.type === 'text').map(item => item.text ?? '');
}

const RECORDED_HEADERS = ['cache-control', 'content-type', 'mcp-session-id'];

interface Replay {
  readonly name: string;
  readonly exchange: Exchange;
  readonly frames: Json[];
  readonly server: McpServerOptions;
}

const replays: Replay[] = [];
for (const testCase of corpus.cases) {
  let server: McpServerOptions = { serverInfo: ENGINE_SERVER };
  testCase.steps.forEach((step, index) => {
    if ('app' in step) {
      const mounts = (step.mounts ?? [{ kind: step.kind, options: step.options }]) as { kind: string; options?: Record<string, Json> }[];
      const mcp = mounts.find(mount => mount.kind === 'mcp')?.options ?? {};
      server = {
        serverInfo: (mcp.serverInfo as unknown as McpServerOptions['serverInfo']) ?? ENGINE_SERVER,
        ...(typeof mcp.instructions === 'string' ? { instructions: mcp.instructions } : {}),
      };
      return;
    }
    if (!('request' in step)) return;
    const exchange = resolve(step) as unknown as Exchange;
    if (!exchange.request.path.endsWith('/mcp') || exchange.response.status === 401 || exchange.response.status === 404) return;
    const frames = exchange.stream !== undefined ? framesOf(testCase.steps, index, exchange.stream) : [];
    const body = exchange.request.body as { method?: string; id?: Json } | undefined;
    replays.push({
      name: `${testCase.name} > exchange ${index}: ${exchange.request.method} ${body?.method ?? ''}`,
      exchange,
      frames,
      server,
    });
  });
}

describe('the engine MCP corpus through serveMcp', () => {
  test('the corpus has exchanges the transport answers', () => {
    expect(replays.length).toBeGreaterThan(50);
    for (const kind of ['initialize', 'notifications/initialized', 'tools/list', 'tools/call', 'server/discover']) {
      expect(replays.some(replay => (replay.exchange.request.body as { method?: string } | undefined)?.method === kind)).toBe(true);
    }
  });

  for (const replay of replays) {
    test(replay.name, async () => {
      const { request, response } = replay.exchange;
      const url = `http://engine.test${request.path}${request.query ? `?${request.query}` : ''}`;
      const sent = new Request(url, {
        method: request.method,
        headers: request.headers,
        ...(request.body !== undefined ? { body: JSON.stringify(request.body) } : {}),
      });
      const answer = await serveMcp(sent, replayTools(replyOf(replay.exchange, replay.frames)), replay.server);
      expect(answer.status).toBe(response.status);
      for (const name of RECORDED_HEADERS) {
        expect(answer.headers.get(name) ?? undefined).toBe(response.headers?.[name]);
      }
      const text = await answer.text();
      if (replay.exchange.stream !== undefined) {
        const frames = withoutSymbolTexts(parseFrames(text)) as Json[];
        const recorded = withoutSymbolTexts(replay.frames) as Json[];
        expect(frames).toEqual(recorded);
        expect(frames.flatMap(frame => texts((frame as { data: Json }).data))).toEqual(recorded.flatMap(frame => texts((frame as { data: Json }).data)));
      } else if (response.body !== undefined) {
        const body = withoutSymbolTexts(JSON.parse(text) as Json);
        const recorded = withoutSymbolTexts(response.body);
        expect(body).toEqual(recorded);
        expect(texts(body)).toEqual(texts(recorded));
      } else {
        expect(text).toBe('');
      }
    });
  }
});
