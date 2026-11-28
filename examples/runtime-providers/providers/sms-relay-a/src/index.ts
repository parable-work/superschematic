// A channel adapter for an SMS relay that takes JSON (D159). It sends with
// POST <URL>/v1/messages and a Bearer token, and reads the relay's
// callbacks, delivery statuses and inbound messages, from the request a
// @webhook route received, after checking the relay's HMAC-SHA256
// signature of the body with the same token. An inbound text's media it
// fetches from the relay, and STOP and START it reads as an opt-out and an
// opt-in (D157).
import {
  defineProvider,
  type ChannelAdapter,
  type DeliveryState,
  type WebhookRequest,
} from '@superschematic/http-runtime/providers';

/** How the adapter reaches the relay; globalThis.fetch unless a test gives another. */
export interface SmsRelayAOptions {
  readonly fetch?: typeof fetch;
}

/** The keywords SMS carriers treat as opting out of, and back into, a sender's messages (D157). */
const OPT_OUT = new Set(['STOP', 'STOPALL', 'UNSUBSCRIBE', 'CANCEL', 'END', 'QUIT']);
const OPT_IN = new Set(['START', 'UNSTOP']);

/** The relay's states, as the adapter's DeliveryState. */
const STATES: Readonly<Record<string, DeliveryState>> = { accepted: 'queued', sent: 'sent', delivered: 'delivered', undelivered: 'failed', failed: 'failed' };

async function hmacHex(key: string, body: string): Promise<string> {
  const encoder = new TextEncoder();
  const cryptoKey = await crypto.subtle.importKey('raw', encoder.encode(key), { name: 'HMAC', hash: 'SHA-256' }, false, ['sign']);
  const mac = new Uint8Array(await crypto.subtle.sign('HMAC', cryptoKey, encoder.encode(body)));
  return [...mac].map(byte => byte.toString(16).padStart(2, '0')).join('');
}

/** The definition, over options.fetch. */
export function smsRelayA(options: SmsRelayAOptions = {}) {
  return defineProvider({
    name: 'sms-relay-a',
    implements: 'channel',
    config: {
      URL: { description: "The relay's base URL." },
      TOKEN: { secret: true, description: "The relay's API token, which also signs its callbacks." },
      SENDER: { optional: true, description: "The number messages are sent from; the relay's default when unset." },
      CALLER_KEY: {
        secret: true,
        optional: true,
        description:
          "The key the relay calls the app with, to read the deliveries it sent (#744): the API resolves a request bearing it to this provider's principal, which may relay messages and nothing else. An environment that lets the relay call nothing leaves it unset.",
      },
    },
    role: ['messages.relay'],
    create(settings, context): ChannelAdapter {
      const base = settings.text('URL').replace(/\/+$/u, '');
      const token = settings.secret('TOKEN');
      const sender = settings.optionalText('SENDER');
      const call = options.fetch ?? globalThis.fetch;

      const verified = async (request: WebhookRequest): Promise<unknown> => {
        const signature = request.headers['x-relay-signature'] ?? '';
        if (signature !== (await hmacHex(token.reveal(), request.body))) throw new Error('sms-relay-a: the callback is not signed with the relay token');
        return JSON.parse(request.body);
      };

      return {
        kind: 'sms',
        async send(message) {
          const response = await call(`${base}/v1/messages`, {
            method: 'POST',
            headers: {
              authorization: `Bearer ${token.reveal()}`,
              'content-type': 'application/json',
              ...(message.idempotencyKey ? { 'idempotency-key': message.idempotencyKey } : {}),
            },
            body: JSON.stringify({ to: message.to, text: message.body, ...(sender ? { from: sender } : {}), ...(message.reference ? { reference: message.reference } : {}) }),
          });
          if (!response.ok) throw new Error(`sms-relay-a: the relay answered ${response.status}`);
          const sent = (await response.json()) as { id: string; status: string };
          context.log.info('sms sent', { messageId: sent.id });
          return { providerMessageId: sent.id, state: STATES[sent.status] ?? 'queued' };
        },
        async health() {
          const response = await call(`${base}/v1/health`, { headers: { authorization: `Bearer ${token.reveal()}` } });
          return response.ok ? { ok: true } : { ok: false, detail: `the relay answered ${response.status}` };
        },
        async status(request) {
          const body = (await verified(request)) as { events?: { id: string; status: string; at: string; error?: string }[] };
          return (body.events ?? []).map(event => ({
            providerMessageId: event.id,
            state: STATES[event.status] ?? 'queued',
            at: new Date(event.at),
            ...(event.error ? { detail: event.error } : {}),
          }));
        },
        async inbound(request) {
          const body = (await verified(request)) as {
            messages?: { id: string; from: string; text: string; at: string; replyTo?: string; media?: { url: string; type: string; name?: string }[] }[];
          };
          const out = [];
          for (const message of body.messages ?? []) {
            // A photo the text came with is the relay's to keep: the
            // adapter fetches its bytes, with the relay's token, so the
            // core files them (D157).
            const attachments = [];
            for (const [i, media] of (message.media ?? []).entries()) {
              const response = await call(media.url, { headers: { authorization: `Bearer ${token.reveal()}` } });
              if (!response.ok) throw new Error(`sms-relay-a: the relay's media answered ${response.status}`);
              attachments.push({ name: media.name ?? `media-${i + 1}`, mediaType: media.type, bytes: new Uint8Array(await response.arrayBuffer()) });
            }
            const keyword = message.text.trim().toUpperCase();
            out.push({
              providerMessageId: message.id,
              from: message.from,
              body: message.text,
              receivedAt: new Date(message.at),
              ...(message.replyTo ? { inReplyTo: message.replyTo } : {}),
              ...(attachments.length > 0 ? { attachments } : {}),
              // The opt-out and opt-in keywords of SMS are the adapter's to
              // read; the core records them and sends nothing after STOP.
              ...(OPT_OUT.has(keyword) ? { optOut: 'out' as const } : OPT_IN.has(keyword) ? { optOut: 'in' as const } : {}),
            });
          }
          return out;
        },
      };
    },
  });
}

/** The definition a server links, over the platform's fetch. */
export default smsRelayA();
