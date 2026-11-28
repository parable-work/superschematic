// A channel adapter for a messaging network (D159) whose inbound side is a
// bridge: a process that connects outward, to the network for what it
// receives and to the app's inbound route to relay it, so its host needs
// no open port (plan section 9's rule 4). The app sends through the
// network's API, POST <URL>/v1/messages with a Bearer token. The bridge
// (runBridge, below) posts what the network received to the app's
// /webhooks/<slot>/inbound with the relay key, which inbound checks: the
// key can only relay messages, since the API resolves it to this
// provider's principal, whose role is messages.relay
// (extensionAuthenticator). Senders are handles, which a party's or a
// user's channel identity of kind handle names (D157).
import {
  defineProvider,
  type ChannelAdapter,
  type InboundMessage,
  type WebhookRequest,
} from '@superschematic/http-runtime/providers';

/** How the adapter reaches the network; globalThis.fetch unless a test gives another. */
export interface ChatBridgeOptions {
  readonly fetch?: typeof fetch;
}

/** One message the network received, as the bridge relays it. */
export interface RelayedMessage {
  /** The network's id of the message. */
  readonly id: string;
  /** The sender's handle on the network. */
  readonly from: string;
  readonly text: string;
  /** When the network received it, as ISO 8601. */
  readonly at: string;
  /** The network's id of the message it answers, when it threads. */
  readonly replyTo?: string;
}

/** What the bridge posts to the app's inbound route. */
export interface RelayBody {
  readonly messages: readonly RelayedMessage[];
}

/** Whether a and b are the same text, compared in constant time. */
function sameText(a: string, b: string): boolean {
  const encoder = new TextEncoder();
  const x = encoder.encode(a);
  const y = encoder.encode(b);
  let diff = x.length ^ y.length;
  for (let i = 0; i < Math.max(x.length, y.length); i++) diff |= (x[i] ?? 0) ^ (y[i] ?? 0);
  return diff === 0;
}

/** The definition, over options.fetch. */
export function chatBridge(options: ChatBridgeOptions = {}) {
  return defineProvider({
    name: 'chat-bridge',
    implements: 'channel',
    config: {
      URL: { description: "The messaging network's API base URL." },
      TOKEN: { secret: true, description: "The network's API token, which sends." },
      RELAY_KEY: {
        secret: true,
        description:
          "The key the bridge relays with: the inbound route takes a request that bears it, and the API resolves it to this provider's principal, which may relay messages and nothing else.",
      },
    },
    role: ['messages.relay'],
    create(settings, context): ChannelAdapter {
      const base = settings.text('URL').replace(/\/+$/u, '');
      const token = settings.secret('TOKEN');
      const relayKey = settings.secret('RELAY_KEY');
      const call = options.fetch ?? globalThis.fetch;

      /** The relayed body, once the request bears the relay key. */
      const relayed = (request: WebhookRequest): RelayBody => {
        const header = request.headers.authorization ?? '';
        if (!header.startsWith('Bearer ') || !sameText(header.slice('Bearer '.length), relayKey.reveal())) {
          throw new Error('chat-bridge: the request does not bear the relay key');
        }
        return JSON.parse(request.body) as RelayBody;
      };

      return {
        kind: 'chat',
        async send(message) {
          const response = await call(`${base}/v1/messages`, {
            method: 'POST',
            headers: {
              authorization: `Bearer ${token.reveal()}`,
              'content-type': 'application/json',
              ...(message.idempotencyKey ? { 'idempotency-key': message.idempotencyKey } : {}),
            },
            body: JSON.stringify({ to: message.to, text: message.body, ...(message.inReplyTo ? { replyTo: message.inReplyTo } : {}) }),
          });
          if (!response.ok) throw new Error(`chat-bridge: the network answered ${response.status}`);
          const sent = (await response.json()) as { id: string };
          context.log.info('chat message sent', { messageId: sent.id });
          return { providerMessageId: sent.id, state: 'sent' };
        },
        async health() {
          const response = await call(`${base}/v1/health`, { headers: { authorization: `Bearer ${token.reveal()}` } });
          return response.ok ? { ok: true } : { ok: false, detail: `the network answered ${response.status}` };
        },
        async inbound(request) {
          const body = relayed(request);
          return (body.messages ?? []).map(
            (message): InboundMessage => ({
              providerMessageId: message.id,
              from: message.from,
              body: message.text,
              receivedAt: new Date(message.at),
              ...(message.replyTo ? { inReplyTo: message.replyTo } : {}),
            })
          );
        },
      };
    },
  });
}

// ---- the bridge: the process that connects outward ----

/** What runBridge takes. */
export interface BridgeOptions {
  /** What the network received, as the bridge's connection to it yields it: pollNetwork's, or a test's. */
  readonly messages: AsyncIterable<RelayedMessage>;
  /** The app's inbound route of the slot this adapter fills: <api>/webhooks/<slot>/inbound. */
  readonly inbound: string;
  /** The relay key, the slot's RELAY_KEY, which may relay messages and nothing else. */
  readonly key: string;
  /** How the bridge reaches the app; globalThis.fetch unless a test gives another. */
  readonly fetch?: typeof fetch;
}

/**
 * Relays each message the network received to the app's inbound route,
 * one request each, bearing the relay key, and resolves to how many once
 * messages ends. A refusal ends it: the bridge's host restarts it, and the
 * network keeps what was not relayed.
 */
export async function runBridge(options: BridgeOptions): Promise<number> {
  const call = options.fetch ?? globalThis.fetch;
  let relayed = 0;
  for await (const message of options.messages) {
    const response = await call(options.inbound, {
      method: 'POST',
      headers: { authorization: `Bearer ${options.key}`, 'content-type': 'application/json' },
      body: JSON.stringify({ messages: [message] } satisfies RelayBody),
    });
    if (!response.ok) throw new Error(`chat-bridge: the app answered ${response.status}`);
    relayed++;
  }
  return relayed;
}

/** What pollNetwork takes. */
export interface PollOptions {
  /** The network's API base URL. */
  readonly url: string;
  /** The network's API token. */
  readonly token: string;
  readonly fetch?: typeof fetch;
  /** Ends the poll. */
  readonly signal?: AbortSignal;
  /** Where the poll starts: the cursor the network gave last. */
  readonly after?: string;
}

/**
 * The messages the network received, read outward: GET
 * <url>/v1/events?after=<cursor>, which the network holds open until
 * something arrives, answering { events, cursor }. It ends when signal
 * aborts or the network answers no cursor.
 */
export async function* pollNetwork(options: PollOptions): AsyncGenerator<RelayedMessage> {
  const call = options.fetch ?? globalThis.fetch;
  let after = options.after;
  const base = options.url.replace(/\/+$/u, '');
  while (!options.signal?.aborted) {
    const response = await call(`${base}/v1/events${after ? `?after=${encodeURIComponent(after)}` : ''}`, {
      headers: { authorization: `Bearer ${options.token}` },
      ...(options.signal ? { signal: options.signal } : {}),
    });
    if (!response.ok) throw new Error(`chat-bridge: the network answered ${response.status}`);
    const page = (await response.json()) as { events?: RelayedMessage[]; cursor?: string | null };
    for (const event of page.events ?? []) yield event;
    if (!page.cursor) return;
    after = page.cursor;
  }
}

/** The definition a server links, over the platform's fetch. */
export default chatBridge();
