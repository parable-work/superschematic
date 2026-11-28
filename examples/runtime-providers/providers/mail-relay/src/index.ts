// A channel adapter for a mail relay that takes JSON (D159, D153). It
// sends with POST <URL>/v1/mail and a Bearer token, from the address its
// config names, and reports no delivery: mail that the relay accepted is
// sent, as most relays leave it. Notifications send email through it,
// password reset codes among them (D81).
//
// It also reads the mail the relay received for the deployment's inbound
// address (D157), from the request a @webhook route received, after
// checking the relay's HMAC-SHA256 signature of the body with the same
// token: the sender, the addresses it was sent to, its subject and text,
// its Message-ID, In-Reply-To and References, the relay's SPF, DKIM and
// DMARC results, which the core judges, and its attachments, base64 in the
// callback. An outbound email carries the Message-ID, In-Reply-To,
// References and Reply-To the core gives it, so a reply threads.
import { defineProvider, type ChannelAdapter, type WebhookRequest } from '@superschematic/http-runtime/providers';

/** How the adapter reaches the relay; globalThis.fetch unless a test gives another. */
export interface MailRelayOptions {
  readonly fetch?: typeof fetch;
}

/** One email of the relay's inbound callback. */
interface RelayMail {
  readonly id: string;
  readonly from: string;
  readonly to?: string[];
  readonly subject?: string;
  readonly text: string;
  readonly at: string;
  readonly messageId?: string;
  readonly inReplyTo?: string;
  readonly references?: string[];
  readonly auth?: { readonly spf?: string; readonly dkim?: string; readonly dmarc?: string };
  readonly attachments?: { readonly name: string; readonly type: string; readonly content: string }[];
}

async function hmacHex(key: string, body: string): Promise<string> {
  const encoder = new TextEncoder();
  const cryptoKey = await crypto.subtle.importKey('raw', encoder.encode(key), { name: 'HMAC', hash: 'SHA-256' }, false, ['sign']);
  const mac = new Uint8Array(await crypto.subtle.sign('HMAC', cryptoKey, encoder.encode(body)));
  return [...mac].map(byte => byte.toString(16).padStart(2, '0')).join('');
}

function base64Bytes(text: string): Uint8Array {
  const binary = atob(text);
  const out = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) out[i] = binary.charCodeAt(i);
  return out;
}

/** The definition, over options.fetch. */
export function mailRelay(options: MailRelayOptions = {}) {
  return defineProvider({
    name: 'mail-relay',
    implements: 'channel',
    config: {
      URL: { description: "The relay's base URL." },
      TOKEN: { secret: true, description: "The relay's API token, which also signs its callbacks." },
      FROM: { description: 'The address mail is sent from.' },
      CALLER_KEY: {
        secret: true,
        optional: true,
        description:
          "The key the relay calls the app with (#744): the API resolves a request bearing it to this provider's principal, which may relay messages and nothing else. An environment that lets the relay call nothing leaves it unset.",
      },
    },
    role: ['messages.relay'],
    create(settings): ChannelAdapter {
      const base = settings.text('URL').replace(/\/+$/u, '');
      const token = settings.secret('TOKEN');
      const from = settings.text('FROM');
      const call = options.fetch ?? globalThis.fetch;

      const verified = async (request: WebhookRequest): Promise<unknown> => {
        const signature = request.headers['x-relay-signature'] ?? '';
        if (signature !== (await hmacHex(token.reveal(), request.body))) throw new Error('mail-relay: the callback is not signed with the relay token');
        return JSON.parse(request.body);
      };

      return {
        kind: 'email',
        async send(message) {
          const headers: Record<string, string> = {};
          if (message.messageId) headers['Message-ID'] = message.messageId;
          if (message.inReplyTo) headers['In-Reply-To'] = message.inReplyTo;
          if (message.references?.length) headers.References = message.references.join(' ');
          const response = await call(`${base}/v1/mail`, {
            method: 'POST',
            headers: {
              authorization: `Bearer ${token.reveal()}`,
              'content-type': 'application/json',
              ...(message.idempotencyKey ? { 'idempotency-key': message.idempotencyKey } : {}),
            },
            body: JSON.stringify({
              from,
              to: message.to,
              subject: message.subject ?? '',
              text: message.body,
              ...(message.replyTo ? { replyTo: message.replyTo } : {}),
              ...(Object.keys(headers).length > 0 ? { headers } : {}),
            }),
          });
          if (!response.ok) throw new Error(`mail-relay: the relay answered ${response.status}`);
          const sent = (await response.json()) as { id: string };
          return { providerMessageId: sent.id, state: 'sent' };
        },
        async health() {
          const response = await call(`${base}/v1/health`, { headers: { authorization: `Bearer ${token.reveal()}` } });
          return response.ok ? { ok: true } : { ok: false, detail: `the relay answered ${response.status}` };
        },
        async inbound(request) {
          const body = (await verified(request)) as { mail?: RelayMail[] };
          return (body.mail ?? []).map(mail => ({
            providerMessageId: mail.id,
            from: mail.from,
            body: mail.text,
            receivedAt: new Date(mail.at),
            ...(mail.to ? { to: mail.to } : {}),
            ...(mail.subject ? { subject: mail.subject } : {}),
            ...(mail.messageId ? { messageId: mail.messageId } : {}),
            ...(mail.inReplyTo ? { inReplyTo: mail.inReplyTo } : {}),
            ...(mail.references ? { references: mail.references } : {}),
            // The relay's verdicts as it gives them; the core decides what
            // a failing one means.
            ...(mail.auth ? { authentication: { ...mail.auth } } : {}),
            ...(mail.attachments?.length
              ? { attachments: mail.attachments.map(file => ({ name: file.name, mediaType: file.type, bytes: base64Bytes(file.content) })) }
              : {}),
          }));
        },
      };
    },
  });
}

/** The definition a server links, over the platform's fetch. */
export default mailRelay();
